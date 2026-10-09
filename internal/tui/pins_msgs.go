package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// This file holds the hooks between the Lessons shell and the screens of
// installed lesson maintenance. Three files own the screens:
//
//   - pins.go: the Pins sub-view (pinsView, newPinsView). It lists the
//     installed pins and sends openMaintMsg and openExtractMsg.
//   - pins_modal.go: the retarget and prune preview (maintModal,
//     newMaintModal). The Pins sub-view hosts it through maintHost.
//   - extract.go: the trigger extraction record (extRecord,
//     newExtractRecord). The Run sub-view opens it for openExtractMsg.
//
// The shell side stays here and in lessons.go, run.go and state.go,
// so each screen changes only its own files. The messages carry typed
// requests, never list positions, because a plan binds to the entries as
// the user reviewed them (contracts D-15).

// openMaintMsg asks the Pins sub-view to open the preview of a retarget
// or a prune. req names the action, the reviewed entries, and whether
// the entries come from the cursor row because nothing was marked.
// Lessons shows the Pins sub-view for it, and maintHost.route opens the
// preview. The preview plans in a command, so the message itself reads
// and writes nothing.
type openMaintMsg struct {
	req maintain.Request
}

// openMaint returns a command that opens the maintenance preview of req.
// The Pins sub-view returns it for w (retarget) and - (prune).
func openMaint(req maintain.Request) tea.Cmd {
	return func() tea.Msg { return openMaintMsg{req: req} }
}

// openExtractMsg asks the Run sub-view to open a trigger extraction
// record for req. req.IDs are the selected proposals; req.Skipped names
// the selected Pins entries without exactly one linked proposal, so the
// review lists them as ineligible. The Run sub-view refuses the record
// while another record is active, as for every run.
type openExtractMsg struct {
	req learning.ExtractRequest
	// cursor is true when the cursor row stands in for an empty
	// selection. The review then names the cursor row (contracts D-15).
	// Only the sender knows it, so the message carries it. Lessons keys
	// its return route on this message, so the flag cannot travel on an
	// openRunMsg instead.
	cursor bool
}

// openExtract returns a command that opens the extraction review of req.
// The Pins and the Proposals sub-views return it for t, with cursor set
// when no row is marked. A request from a result sets cursor to false,
// because its IDs come from the run, not from a cursor row.
func openExtract(req learning.ExtractRequest, cursor bool) tea.Cmd {
	return func() tea.Msg { return openExtractMsg{req: req, cursor: cursor} }
}

// focusPinMsg asks the Pins sub-view to put its cursor on the entry that
// proposal id links to. Lessons shows the Pins sub-view for it. A link
// from an extraction result uses it, so the user reviews the pin; it
// never opens a preview or selects the entry (contracts §3.2).
type focusPinMsg struct {
	id int64
}

// openPins returns a command that opens Lessons › Pins with the cursor
// on the entry of proposal id.
func openPins(id int64) tea.Cmd {
	return tea.Batch(navigate(destLessons, "pins"), func() tea.Msg { return focusPinMsg{id: id} })
}

// extractKey opens the trigger extraction review. Pins and Proposals
// share the binding, so t means the same thing in both sub-views. A
// sub-view shows it only while an eligible selection exists.
var extractKey = key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "extract triggers"))

// The names of an extraction run. extractRunName is the mutation name,
// which the quit question quotes as "<name> is running". extractRunVerb
// leads the run line of the notice row.
const (
	extractRunName = "Extracting trigger information"
	extractRunVerb = "Extracting triggers"
)

// The sources that a maintenance commit and an extraction run change.
// The result handlers refresh them with mutationDone, also after a
// refusal, because a refusal means that the state changed. A retarget
// or a prune changes the pin file and the ledger rows; an extraction
// changes trigger fields and pending regions, which the Pins readings
// and the proposal health read.
var (
	maintRefresh   = []sectionKind{secPins, secLedger, secStats, secIndex}
	extractRefresh = []sectionKind{secPins, secLedger, secStats, secIndex}
)

// maintHost holds the open maintenance preview of the Pins sub-view. The
// Pins sub-view embeds it as its field host and calls it first:
//
//	update:    if cmd, done := p.host.route(msg); done { return cmd }
//	capturing: p.host.open() || <own input>
//	keys:      if p.host.open() { return p.host.keys() }
//	modal:     return p.host.view(w, h)
//
// So the preview gets every key while it is open, and the results of
// its commands reach it while the user looks at another view.
type maintHost struct {
	st    *state
	modal *maintModal
	// acted holds the entries that a commit or a recovery of the preview
	// changed, until the Pins sub-view takes them with takeActed.
	acted []pinfile.EntryRef
}

// open reports an open maintenance preview.
func (h *maintHost) open() bool {
	return h.modal != nil
}

// route gives msg to the open preview, or opens a preview for an
// openMaintMsg. consumed is true when the Pins sub-view must not handle
// msg as well: an openMaintMsg, and every key while a preview is open.
// Other messages also reach the sub-view, because a refresh of the pins
// concerns both. A settings save goes to maintModal.settingsSaved, so a
// preview that names the old write gate closes (D-14). A preview that
// reports closed() after a call leaves the host.
func (h *maintHost) route(msg tea.Msg) (cmd tea.Cmd, consumed bool) {
	switch m := msg.(type) {
	case openMaintMsg:
		if h.modal != nil {
			// The open preview captures every key, so a second request
			// cannot come from the keyboard. A request from elsewhere
			// never replaces a preview that the user is reviewing.
			return nil, true
		}

		h.modal, cmd = newMaintModal(h.st, m.req)

		return cmd, true
	case tea.KeyMsg:
		if h.modal == nil {
			return nil, false
		}

		return h.after(h.modal.update(msg)), true
	case settingsSavedMsg:
		if h.modal == nil {
			return nil, false
		}

		return h.after(h.modal.settingsSaved(m)), false
	}

	if h.modal == nil {
		return nil, false
	}

	return h.after(h.modal.update(msg)), false
}

// after collects the entries that the preview changed, drops a closed
// preview, and passes cmd on.
func (h *maintHost) after(cmd tea.Cmd) tea.Cmd {
	if h.modal != nil {
		h.acted = append(h.acted, h.modal.takeActed()...)

		if h.modal.closed() {
			h.modal = nil
		}
	}

	return cmd
}

// takeActed returns the entries that a commit or a recovery changed
// since the last call, and forgets them. The Pins sub-view calls it in
// the same Update as the result. A message would race with the reload
// of the pins that the result starts, and the reload could then report
// a user-caused change as a loss.
func (h *maintHost) takeActed() []pinfile.EntryRef {
	refs := h.acted
	h.acted = nil

	return refs
}

// view renders the open preview for the shell, which dims the frame
// under it.
func (h *maintHost) view(w, ht int) (string, bool) {
	if h.modal == nil {
		return "", false
	}

	return h.modal.modal(w, ht), true
}

// keys returns the footer keys of the open preview.
func (h *maintHost) keys() []key.Binding {
	if h.modal == nil {
		return nil
	}

	return h.modal.keys()
}
