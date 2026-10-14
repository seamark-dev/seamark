package tui

import (
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds what follows a confirmed write of a settings file or
// of lessons.yaml. It holds the receivers of lessonsSavedMsg (contracts
// P4-12) and the check of the reload after the write (P4-18).
//
// lessonsSavedMsg reaches every holder through the usual fan-out:
//
//   - the shell broadcasts it to every screen;
//   - Lessons gives it to every sub-view;
//   - the Run sub-view gives it to its current record;
//   - the Pins sub-view gives it to its open preview.
//
// So a holder joins with a case in its update, and no list of instances
// exists.
//
// The senders are the writes of lessons.yaml with a Written or an
// Unknown outcome. They are the apply of Proposals, the retarget, prune,
// and pin authoring commits of Pins, and a Settings save of a
// lessons.yaml group. The sender itself shows its result, which is
// after its commit, so it keeps that result.

// lessonsBound is a holder whose open preview binds the bytes of
// lessons.yaml. lessonsSaved closes that preview while it reads or
// waits for y. It keeps the draft and a result, because they do not
// depend on the old bytes. It returns the message that names the closed
// preview, or nil. The holder calls it from its update for a
// lessonsSavedMsg.
type lessonsBound interface {
	lessonsSaved(msg lessonsSavedMsg) tea.Cmd
}

// The holders of lessonsBound. A new holder whose preview reads
// lessons.yaml joins this list and the table of
// TestLessonsSavedReceivers. The evidence refresh preview is not one:
// its plan binds config.yaml and the evidence, not lessons.yaml, so it
// stays open.
var (
	_ lessonsBound = (*propView)(nil)
	_ lessonsBound = (*distRecord)(nil)
	_ lessonsBound = (*extRecord)(nil)
	_ lessonsBound = (*maintModal)(nil)
	_ lessonsBound = (*pinReview)(nil)
	_ lessonsBound = (*settingsScreen)(nil)
)

// sendLessonsSaved returns the command of a lessonsSavedMsg. A sender
// returns it next to its result, so every holder gets the message in
// the same loop. at is the end of the write, and by names its place
// (lessonsSavedMsg).
func sendLessonsSaved(at time.Time, by string) tea.Cmd {
	return func() tea.Msg { return lessonsSavedMsg{at: at, by: by} }
}

// lessonsChanged starts the message about a preview that a write of
// lessons.yaml closed, for example "Lessons › Pins changed
// .seamark/lessons.yaml". The receiver adds what closed and how to open
// it again, as the receivers of settingsSavedMsg do.
func lessonsChanged(msg lessonsSavedMsg) string {
	return ui.OneLine(msg.by) + " changed " + pinfile.File
}

// refreshCheck watches the reload after a write that landed (P4-18). The
// write is done, and the console never runs it again. When reading the
// file again fails, the result of the write says so, or a message says
// so when the result is closed.
type refreshCheck struct {
	// file is the written file relative to the root, for example
	// .seamark/lessons.yaml.
	file string
	// after holds the newest ticket of each watched section at the time
	// of the write. A load with a newer ticket started after the write,
	// so it reads the new bytes. A load with this ticket or an older one
	// can hold the bytes from before the write, so it never counts.
	after map[sectionKind]int
	// failed is the reason of the first failed reload, or "".
	failed string
	// shown reports whether the result of the write is on screen. The
	// result then shows the failure, and no message is needed. nil means
	// that no result shows.
	shown func() bool
}

// readsAgain reports whether section k reads file, so that its reload
// is the check of a write of file. The Pins inspection reads
// lessons.yaml. The settings snapshot reads both settings documents. A
// section that reads another file is not watched: its failure says
// nothing about the written file.
func readsAgain(k sectionKind, file string) bool {
	switch k {
	case secPins:
		return file == pinfile.File
	case secSettings:
		return file == pinfile.File || file == settingsFile
	}

	return false
}

// watchRefresh returns the reload of kinds after a write of file that
// landed, and the check of that reload. It records the newest ticket of
// each section among kinds that reads file. The reload starts later,
// through refreshMsg, so its tickets are newer. shown reports whether
// the result of the write is on screen (refreshCheck.shown). The holder
// keeps the check to show its failure in the result.
func (s *state) watchRefresh(file string, shown func() bool, kinds ...sectionKind) (*refreshCheck, tea.Cmd) {
	c := &refreshCheck{file: file, after: map[sectionKind]int{}, shown: shown}

	for _, k := range kinds {
		if readsAgain(k, file) {
			c.after[k] = s.gens[k].latest()
		}
	}

	if len(c.after) > 0 {
		s.checks = append(s.checks, c)
	}

	return c, refresh(kinds...)
}

// checkRefresh applies a stored load result to the open checks. It
// returns a message for each new failure whose result is closed. A
// check ends when each of its sections has loaded once after the write.
func (s *state) checkRefresh(msg loadedMsg) tea.Cmd {
	var cmds []tea.Cmd

	for _, c := range s.checks {
		if c.loaded(msg) && (c.shown == nil || !c.shown()) {
			cmds = append(cmds, toast(ui.ToneAttention, c.text()))
		}
	}

	s.checks = slices.DeleteFunc(s.checks, func(c *refreshCheck) bool { return len(c.after) == 0 })

	return tea.Batch(cmds...)
}

// loaded applies one load result. It reports true for the first reload
// of a watched section after the write when that reload failed. A later
// failure of another section reports nothing, because one message is
// enough.
func (c *refreshCheck) loaded(msg loadedMsg) bool {
	ticket, ok := c.after[msg.kind]
	if !ok || msg.gen <= ticket {
		return false
	}

	delete(c.after, msg.kind)

	if c.failed != "" {
		return false
	}

	c.failed = reloadFailure(msg, c.file)

	return c.failed != ""
}

// text is the result row or the message of a failed reload.
func (c *refreshCheck) text() string {
	return "Saved: " + c.file + " holds the reviewed bytes. Reading it again failed: " + c.failed +
		". Nothing is retried; r reads again."
}

// shows reports a failed reload, which the result of the write shows as
// a row. A nil check shows nothing.
func (c *refreshCheck) shows() bool {
	return c != nil && c.failed != ""
}

// row returns the result lines of a failed reload: a blank line and the
// text, or nil when the reload did not fail. Every result of a write
// that landed shows it the same way.
func (c *refreshCheck) row(t *ui.Theme, w int) []string {
	if !c.shows() {
		return nil
	}

	return append([]string{""}, propNote(t, ui.ToneAttention, c.text(), w)...)
}

// readAgain is the r key of a result that shows a failed reload. The row
// says "r reads again", and an open result captures every key, so the
// result routes r here. It reads kinds again under a new check of the
// same write. So the row shows the newest read, and it goes away when
// that read succeeds. It never runs the write again.
func (s *state) readAgain(c *refreshCheck, kinds ...sectionKind) (*refreshCheck, tea.Cmd) {
	return s.watchRefresh(c.file, c.shown, kinds...)
}

// unknownOutcomeText is the result row or the message of a write whose
// outcome is unknown (P4-18). The write failed, and the readback could
// not tell whether the bytes landed. Every writer of a settings file or
// of lessons.yaml shows this text, so it has one owner.
func unknownOutcomeText(file string) string {
	return "Result unknown: check " + file + "."
}

// reloadFailure returns why a reload of a watched section failed after a
// write of file, or "". The Pins inspection fails when it cannot read
// the file or gives no reliable listing. The settings snapshot fails
// when the read returns an error or no snapshot, or when the reading of
// the written document failed. A failure of the other document is not a
// failure of this reload: its file did not change (B1 deviation 13).
func reloadFailure(msg loadedMsg, file string) string {
	var reason string

	switch msg.kind {
	case secPins:
		in, _ := msg.value.(*maintain.Inspection)

		switch {
		case in == nil:
			reason = "the pin inspection returned nothing"
		case in.DocErr != nil:
			reason = ui.ErrText(in.DocErr)
		case in.Doc.Unreadable():
			reason = pinUnreadableText(in.Doc)
		}
	case secSettings:
		snap, _ := msg.value.(*settings.Snapshot)

		switch {
		case msg.err != nil:
			reason = ui.ErrText(msg.err)
		case snap == nil:
			reason = "the settings read returned no snapshot"
		case file == pinfile.File && snap.Lessons == nil:
			reason = "the settings read holds no reading of " + pinfile.File
		case file == pinfile.File && snap.Lessons.ReaderErr != nil:
			reason = ui.ErrText(snap.Lessons.ReaderErr)
		case file == settingsFile && snap.ReadErr != nil:
			reason = ui.ErrText(snap.ReadErr)
		}
	}

	// The text adds its own full stop.
	return strings.TrimSuffix(reason, ".")
}
