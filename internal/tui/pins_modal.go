package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// This file holds the flow of the retarget and prune preview of Lessons
// › Pins: the keys, and the commands that plan and commit. The stages
// come from previewFlow (previewflow.go), and pins_modal_view.go renders
// them. The preview follows the decision modal of Proposals: a plan
// read, a preview, y, a commit that cannot stop, and a result. Nothing
// is written before y, and nothing is retried automatically.
//
// A commit can change the pin file and then fail on the ledger. The
// result can then carry a session recovery (maintain.Recovery). The
// preview keeps it in memory and offers its ledger-only completion as a
// new preview with its own y. Only esc on the result drops it, and the
// texts say so, because the proof does not survive the preview
// (contracts D-13).

// maintKeys are the preview keys besides y, enter and esc. o opens the
// write gate in Settings. c previews the ledger-only completion of a
// partial operation, as c records existing pins in Proposals.
var maintKeys = struct {
	Settings, Complete key.Binding
}{
	Settings: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "settings")),
	Complete: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "complete ledger")),
}

// The Pins keys that open each preview. The texts name them, so a
// closed preview tells the user how to open a new one (contracts §3.3).
const (
	maintRetargetKey = "w"
	maintPruneKey    = "-"
)

// maintPlannedMsg carries a plan read. m and seq drop the result of a
// preview that closed or that started a newer read.
type maintPlannedMsg struct {
	m    *maintModal
	seq  int
	plan *maintain.Plan
	err  error
}

// maintCommittedMsg carries the outcome of a commit or of a recovery
// completion. plan is the plan that the user confirmed.
type maintCommittedMsg struct {
	m    *maintModal
	plan *maintain.Plan
	res  maintain.Result
	err  error
}

// maintOutcome is the last commit outcome that the result stage shows.
type maintOutcome struct {
	plan *maintain.Plan
	res  maintain.Result
	// err is a refusal: the commit wrote nothing.
	err error
	// recovery is true for the outcome of a ledger-only completion.
	recovery bool
}

// maintModal is the preview of one retarget or prune request. The Pins
// sub-view hosts it through maintHost, which gives it every key while
// it is open and every non-key message. The embedded flow holds the
// stage and the shown plan. flowClosed tells the host to drop the
// preview.
type maintModal struct {
	previewFlow[maintain.Plan]

	st  *state
	req maintain.Request

	// recovering is true while the preview, the commit, or the plan read
	// concerns the ledger-only completion of offer.
	recovering bool
	// seq increases with every plan read, so a late result of a read
	// that the user left never replaces the current stage. Each open
	// builds a new modal, and its tickets start at 1 again, so a result
	// also names its modal (maintPlannedMsg.m).
	seq generation

	// foreign is the repository root that the index records when it is
	// not the workspace root (pinsForeignRoot), as the plan arrived. A
	// commit would then change the pin file of that root and the ledger
	// of this workspace, so the preview turns y off.
	foreign string

	out *maintOutcome
	// offer is the session recovery that the last result offers. It
	// stays until a completion succeeds or the user closes the result.
	offer *maintain.Recovery

	// acted holds the entries that the last commit or recovery changed,
	// until maintHost takes them (takeActed).
	acted []pinfile.EntryRef
}

// newMaintModal opens the preview of req. The returned command reads the
// plan, so no I/O runs in Update.
func newMaintModal(st *state, req maintain.Request) (*maintModal, tea.Cmd) {
	m := &maintModal{st: st, req: req}
	read := m.prepare()

	return m, read
}

// prepare starts the plan read of the request. A new read replaces the
// shown preview, and seq drops the result of an older read.
func (m *maintModal) prepare() tea.Cmd {
	seq := m.seq.start()
	m.startRead()
	m.recovering = false

	req := m.req

	var svc Maintenance = m.st.svc

	return func() tea.Msg {
		plan, err := svc.PlanMaintenance(req)

		return maintPlannedMsg{m: m, seq: seq, plan: plan, err: err}
	}
}

// prepareRecovery starts the plan read of the offered recovery. The read
// checks the proof against a fresh observation and writes nothing.
func (m *maintModal) prepareRecovery() tea.Cmd {
	seq := m.seq.start()
	m.startRead()
	m.recovering = true

	rec := m.offer

	var svc Maintenance = m.st.svc

	return func() tea.Msg {
		plan, err := svc.PlanRecovery(rec)

		return maintPlannedMsg{m: m, seq: seq, plan: plan, err: err}
	}
}

// update handles a key or a result message.
func (m *maintModal) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case maintPlannedMsg:
		m.planned(msg)
	case maintCommittedMsg:
		return m.committed(msg)
	case tea.KeyMsg:
		return m.key(msg)
	}

	return nil
}

// planned shows a plan read, unless the user left the read.
func (m *maintModal) planned(msg maintPlannedMsg) {
	if msg.m != m || !m.readDone(m.seq.current(msg.seq), msg.plan, msg.err, "the preview came back empty") {
		return
	}

	m.foreign = ""

	// The Pins keys stay off for a foreign root (editBlock). A preview
	// can still open through enter after a refusal, so the plan itself
	// loses y. The change goes to a copy, so the plan of the engine stays
	// as it is. A recovery plan follows a commit of this session and keeps
	// its proof.
	if root := pinsForeignRoot(m.st); root != "" && m.preview != nil && !m.recovering {
		blocked := *m.preview
		blocked.Confirmable = false
		m.preview, m.foreign = &blocked, root
	}
}

// key handles a key of the current stage.
func (m *maintModal) key(msg tea.KeyMsg) tea.Cmd {
	switch m.stage {
	case flowPreparing:
		if key.Matches(msg, nkeys.Back) {
			// The read may still finish; seq drops its result.
			m.seq.cancel()
			m.back()
		}
	case flowPreview:
		return m.previewKey(msg)
	case flowCommitting:
		// The commit cannot stop safely. Keys wait for the result.
	case flowResult:
		return m.resultKey(msg)
	}

	return nil
}

// back leaves a preview without a change. A recovery preview returns to
// the result that offered it, so the offer stays; any other preview
// closes.
func (m *maintModal) back() {
	if m.recovering {
		m.recovering = false
		m.toResult()

		return
	}

	m.close()
}

// previewKey handles the preview. esc leaves with no change; only y
// commits, and only a confirmable plan.
func (m *maintModal) previewKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Back):
		m.back()
	case key.Matches(msg, lesKeys.Confirm):
		// A y during another operation does nothing. The preview already
		// says that y waits, and a toast would be drawn under the modal.
		if m.confirmable() {
			return m.commit()
		}
	case key.Matches(msg, maintKeys.Settings):
		if m.gateOff() {
			// A preview of the old gate never becomes confirmable, so it
			// closes; after a save, w or - builds a new one.
			m.back()

			return navigate(destSettings, "group:pinwrite")
		}
	default:
		m.scroll.key(msg)
	}

	return nil
}

// resultKey handles the result. c previews the offered recovery. enter
// builds a new preview after a refusal; it never repeats the commit.
// esc closes, which drops an offered recovery.
func (m *maintModal) resultKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, maintKeys.Complete):
		if m.offer != nil {
			return m.prepareRecovery()
		}
	case key.Matches(msg, nkeys.Enter):
		switch {
		case m.offer != nil:
			// enter never drops a recovery by accident; esc does.
		case m.reviewAgain():
			return m.prepare()
		default:
			m.close()
		}
	case key.Matches(msg, nkeys.Back):
		m.close()
	default:
		m.scroll.key(msg)
	}

	return nil
}

// confirmable reports a preview that y commits now: a confirmable plan
// and a free mutation slot.
func (m *maintModal) confirmable() bool {
	return m.showsPreview() && m.preview.Confirmable && !m.st.busy()
}

// gateOff reports a preview whose only route is the write gate: the plan
// read distill.write as off, not as unreadable.
func (m *maintModal) gateOff() bool {
	return m.showsPreview() && !m.preview.Write && m.preview.GateErr == nil
}

// reviewAgain reports a result that a new preview can resolve: a
// refusal, or a file write that another writer prevented.
func (m *maintModal) reviewAgain() bool {
	o := m.out
	if o == nil || o.recovery {
		return false
	}

	if o.err != nil {
		return !errors.Is(o.err, maintain.ErrNothingSelected)
	}

	return !o.res.FileWritten && errors.Is(o.res.FileErr, maintain.ErrStale)
}

// commit claims the mutation slot and runs the confirmed plan as
// registered work, so a signal exit waits for it.
func (m *maintModal) commit() tea.Cmd {
	p, recovering := m.preview, m.recovering

	var svc Maintenance = m.st.svc

	cmd, ok := m.startCommit(m.st, maintWorkName(p, recovering), m.st.goWork(func() tea.Msg {
		var (
			res maintain.Result
			err error
		)

		if recovering {
			res, err = svc.CommitRecovery(p)
		} else {
			res, err = svc.CommitMaintenance(p)
		}

		return maintCommittedMsg{m: m, plan: p, res: res, err: err}
	}))
	if !ok {
		return toast(ToneAttention, "Another operation is running: "+m.st.mutation)
	}

	// The wait starts at the top. startCommit keeps the position, because
	// the other flows keep it.
	m.scroll = scrollState{}

	return cmd
}

// maintWorkName is the mutation name of a commit. The notice row and
// the quit question show it.
func maintWorkName(p *maintain.Plan, recovering bool) string {
	switch {
	case recovering:
		return "Completing the ledger update"
	case p.Action == maintain.ActionPrune:
		return "Pruning " + propPlural(len(p.Items), "pin", "pins")
	default:
		return "Retargeting " + propPlural(len(p.Items), "pin", "pins")
	}
}

// committed shows a commit outcome. It always frees the mutation slot
// and reads the pins, the ledger and the statistics again, because a
// refusal also means that the state changed. A committing preview never
// closes, so the outcome always reaches the preview that started it.
func (m *maintModal) committed(msg maintCommittedMsg) tea.Cmd {
	if msg.m == m && m.commitDone() {
		m.out = &maintOutcome{plan: msg.plan, res: msg.res, err: msg.err, recovery: m.recovering}

		// A refused completion wrote nothing, so its proof can still
		// hold. Every other outcome replaces the offer.
		if !m.recovering || msg.err == nil {
			m.offer = msg.res.Recovery
		}

		m.recovering = false
	}

	m.acted = append(m.acted, maintChanged(msg)...)

	return tea.Batch(mutationDone, refresh(maintRefresh...))
}

// takeActed returns the entries that the last commit or recovery
// changed, and forgets them. maintHost hands them to the Pins sub-view
// in the same Update, before the refresh that the result starts can
// arrive.
func (m *maintModal) takeActed() []pinfile.EntryRef {
	refs := m.acted
	m.acted = nil

	return refs
}

// maintChanged returns the reviewed entries that an outcome changed. A
// refusal and a restored file change no entry, so the Pins sub-view
// keeps their selection.
func maintChanged(msg maintCommittedMsg) []pinfile.EntryRef {
	if msg.err != nil || msg.plan == nil {
		return nil
	}

	if (msg.res.FileWritten && !msg.res.Restored) || msg.res.LedgerDone {
		return msg.plan.Request.Refs
	}

	return nil
}

// settingsSaved drops a preview after a settings save, because the plan
// read the old write gate (contracts D-14). A recovery preview returns
// to its result, so the session proof stays available. A commit and a
// result stay.
func (m *maintModal) settingsSaved(msg settingsSavedMsg) tea.Cmd {
	if !m.beforeCommit() {
		return nil
	}

	m.seq.cancel()

	changed := "Settings " + m.st.theme.Glyphs.Arrow + " " + msg.group.String() + " changed " + settingsFile

	if m.recovering {
		m.back()

		return toast(ToneAttention, changed+", so the ledger completion preview closed. Press c to check it again.")
	}

	m.back()

	return toast(ToneAttention, changed+", so the "+m.req.Action.String()+" preview closed. Press "+
		maintOpenKey(m.req.Action)+" in Pins to preview it again.")
}

// maintOpenKey names the Pins key that opens the preview of an action.
func maintOpenKey(a maintain.Action) string {
	if a == maintain.ActionPrune {
		return maintPruneKey
	}

	return maintRetargetKey
}

// closed reports a preview that the host drops.
func (m *maintModal) closed() bool {
	return !m.active()
}

// modal renders the current stage. The scope summary of a preview stays
// above the actions, so the minimum layout still shows what y confirms.
func (m *maintModal) modal(w, h int) string {
	return m.st.theme.modal(m.content(modalBodyWidth(modalMinW, w)), &m.scroll, w, h)
}

// keys returns the footer keys of the current stage. The modal names the
// same keys with longer labels.
func (m *maintModal) keys() []key.Binding {
	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(m.scroll.scrolls())

	switch m.stage {
	case flowPreparing:
		return []key.Binding{withHelp(nkeys.Back, "esc", "cancel")}
	case flowCommitting:
		return nil
	case flowResult:
		return append(m.resultActions(true), scroll)
	}

	return append(m.previewActions(true), scroll)
}

// previewActions returns the keys of a preview. short selects the
// footer labels. y shows only while it commits; o shows only while the
// write gate is off.
func (m *maintModal) previewActions(short bool) []key.Binding {
	var out []key.Binding

	if m.confirmable() {
		out = append(out, withHelp(lesKeys.Confirm, "y", m.confirmLabel(short)))
	}

	if m.gateOff() {
		label := "open Settings " + m.st.theme.Glyphs.Arrow + " Pin writing"
		if short {
			label = "settings"
		}

		out = append(out, withHelp(maintKeys.Settings, "o", label))
	}

	back := "close"

	switch {
	case m.recovering:
		back = "back"
	case m.confirmable():
		back = "cancel"
	}

	return append(out, withHelp(nkeys.Back, "esc", back))
}

// confirmLabel names what y does, with the count in the modal.
func (m *maintModal) confirmLabel(short bool) string {
	n := len(m.preview.Items)

	switch {
	case m.recovering && short:
		return "complete"
	case m.recovering:
		return "complete the ledger update"
	case short:
		return m.preview.Action.String()
	default:
		return m.preview.Action.String() + " " + propPlural(n, "pin", "pins")
	}
}

// resultActions returns the keys of a result. An offered recovery takes
// c and makes esc the only way to drop it. The esc label names the
// completion, because "dismiss" is also a proposal decision in Proposals,
// and esc here drops a proof that cannot come back.
func (m *maintModal) resultActions(short bool) []key.Binding {
	if m.offer != nil {
		label, drop := "preview the completion", "drop completion"
		if short {
			label, drop = "complete", "drop"
		}

		return []key.Binding{withHelp(maintKeys.Complete, "c", label), withHelp(nkeys.Back, "esc", drop)}
	}

	if m.reviewAgain() {
		return []key.Binding{withHelp(nkeys.Enter, "enter", "preview again"), withHelp(nkeys.Back, "esc", "close")}
	}

	return []key.Binding{withHelp(nkeys.Enter, "enter", "close")}
}
