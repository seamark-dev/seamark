package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the check-definition form of M in Lessons › Pins and
// its review (contract K1, writer row "Check-definition authoring").
// The form holds one definition: an ID, a checker kind, and the pattern
// of no-added-pattern/1. s plans the edit through services
// (PlanCheckDef). The review shows the predicate, the impact on every
// pin that binds the ID, each blocker, and the exact diff. Only y
// writes, under the mutation slot (CommitCheckDef). The edit never
// changes a pin: a pin binds a check in the pin form or with lessons
// --set-mode.

// checkDefModal is the open definition form and its review. The form
// shows while the embedded flow is closed. The Pins sub-view drops the
// modal when esc closes the form or the result.
type checkDefModal struct {
	previewFlow[maintain.CheckDefPlan]

	st  *state
	seq generation

	// row is the cursor row of the form, and editing is true while a
	// text row has the keys.
	row     int
	editing bool
	// id and pattern are the text rows; kind is the chosen kind.
	id, pattern ui.LineInput
	kind        lessoncheck.Kind
	// idLock and patternLock say why a text row is read-only, or "". The
	// stored text of a definition comes from the repository, so a row
	// that holds a control character stays read-only (pinInputLock).
	idLock, patternLock string
	// kinds are the kinds of the checker registry, in its order.
	kinds []lessoncheck.Kind

	// res and resErr are the outcome of the commit. reload watches the
	// reload of the pins after a written file.
	res    maintain.AuthorResult
	resErr error
	reload *refreshCheck

	// formScroll is the position of the form body.
	formScroll ui.ScrollState
}

// checkDefPlannedMsg carries a plan read of the review.
type checkDefPlannedMsg struct {
	m    *checkDefModal
	seq  int
	plan *maintain.CheckDefPlan
	err  error
}

// checkDefCommittedMsg carries the outcome of a commit.
type checkDefCommittedMsg struct {
	m   *checkDefModal
	res maintain.AuthorResult
	err error
}

// The fixed rows of the form. The kind rows follow the ID row, one row
// per kind, and the pattern row comes last.
const checkDefRowID = 0

// newCheckDefModal opens the form. A cursor pin with a defined check
// fills the form with that definition, so M edits the definition of the
// pin. Else the form starts with the ID of the pin's check, if any, and
// the first kind. The ID and the pattern come from lessons.yaml. A text
// that the input cannot hold exactly locks its row, as in the pin form,
// so a control byte of the repository never reaches the editing frame.
// The kind never enters a text input: only the radio rows name kinds.
func newCheckDefModal(st *state, in *maintain.Inspection, pin *maintain.Pin) *checkDefModal {
	m := &checkDefModal{
		st:      st,
		id:      ui.LineInput{Placeholder: "a check ID, for example no-todo", RefuseMultiline: true},
		pattern: ui.LineInput{Placeholder: "a Go regular expression, for example TODO\\(", RefuseMultiline: true},
		kinds:   lessoncheck.Kinds(),
	}

	m.kind = m.kinds[0]

	if pin == nil || pin.Check.Check == nil {
		return m
	}

	id, hand := pin.Check.Check.ID, pinStoredHand(st)
	m.idLock = pinWithHand(pinInputLock(&m.id, id), hand)

	if in == nil {
		return m
	}

	if def, ok := in.Checks.Defs[id]; ok {
		m.kind = lessoncheck.Kind(def.Kind)
		m.patternLock = pinWithHand(pinInputLock(&m.pattern, def.Pattern), hand)
	}

	return m
}

// modalScroll returns the scroll position of the shown stage. The form
// keeps its own position, so esc from the review returns to the form
// where the user left it.
func (m *checkDefModal) modalScroll() *ui.ScrollState {
	if m.stage == flowClosed {
		return &m.formScroll
	}

	return &m.scroll
}

// patternRow returns the row of the pattern.
func (m *checkDefModal) patternRow() int {
	return 1 + len(m.kinds)
}

// takesPattern reports whether the chosen kind reads a pattern. Only
// no-added-pattern/1 does (contract K3).
func (m *checkDefModal) takesPattern() bool {
	return m.kind == lessoncheck.KindNoAddedPattern
}

// request returns the definition of the form. A kind without a pattern
// sends none, so a pattern typed for another kind never reaches the file.
func (m *checkDefModal) request() maintain.CheckDefRequest {
	req := maintain.CheckDefRequest{ID: m.id.Value(), Kind: string(m.kind)}
	if m.takesPattern() {
		req.Pattern = m.pattern.Value()
	}

	return req
}

// plan starts the plan read of the review. The read writes nothing.
func (m *checkDefModal) plan() tea.Cmd {
	seq := m.seq.start()
	m.startRead()

	req := m.request()

	var svc services.Maintenance = m.st.svc

	return func() tea.Msg {
		plan, err := svc.PlanCheckDef(req)

		return checkDefPlannedMsg{m: m, seq: seq, plan: plan, err: err}
	}
}

// commit claims the mutation slot and writes the confirmed plan as
// registered work, so a signal exit waits for the write.
func (m *checkDefModal) commit() tea.Cmd {
	plan := m.preview

	var svc services.Maintenance = m.st.svc

	cmd, ok := m.startCommit(m.st, "Writing a check definition", m.st.goWork(func() tea.Msg {
		res, err := svc.CommitCheckDef(plan)

		return checkDefCommittedMsg{m: m, res: res, err: err}
	}))
	if !ok {
		return toast(ui.ToneAttention, "Another operation is running: "+m.st.mutation)
	}

	m.scroll = ui.ScrollState{}

	return cmd
}

// confirmable reports a review that y commits now.
func (m *checkDefModal) confirmable() bool {
	return m.showsPreview() && m.preview.Confirmable && !m.st.busy()
}

// lessonsSaved closes a review that reads or shows a plan after another
// write of lessons.yaml (lessonsBound), because the plan read the old
// bytes. The form keeps its values.
func (m *checkDefModal) lessonsSaved(msg lessonsSavedMsg) tea.Cmd {
	if !m.beforeCommit() {
		return nil
	}

	m.seq.cancel()
	m.close()

	return toast(ui.ToneAttention, lessonsChanged(msg)+", so the check-definition review closed. Press s in the "+
		"form to review again.")
}

// key handles a key of the form or of the current stage of the review.
// It reports whether the whole modal closes.
func (m *checkDefModal) key(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch m.stage {
	case flowClosed:
		return m.formKey(msg)
	case flowPreparing:
		if key.Matches(msg, ui.Nav.Back) {
			m.seq.cancel()
			m.close()
		}
	case flowPreview:
		switch {
		case key.Matches(msg, ui.Nav.Back):
			m.close()
		case key.Matches(msg, lesKeys.Confirm):
			if m.confirmable() {
				return m.commit(), false
			}
		default:
			m.scroll.Key(msg)
		}
	case flowCommitting:
		// The commit cannot stop safely. Keys wait for the result.
	case flowResult:
		switch {
		case key.Matches(msg, gkeys.Refresh) && m.reload.shows():
			var cmd tea.Cmd

			m.reload, cmd = m.st.readAgain(m.reload, pinWriteRefresh...)

			return cmd, false
		case key.Matches(msg, ui.Nav.Enter, ui.Nav.Back):
			return nil, true
		default:
			m.scroll.Key(msg)
		}
	}

	return nil, false
}

// formKey handles the rows of the form, or the focused text row.
func (m *checkDefModal) formKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.editing {
		switch {
		case key.Matches(msg, ui.Nav.Back, ui.Nav.Enter):
			m.editing = false
		case m.row == checkDefRowID:
			m.id.Key(msg)
		default:
			m.pattern.Key(msg)
		}

		return nil, false
	}

	switch {
	case key.Matches(msg, ui.Nav.Back):
		return nil, true
	case key.Matches(msg, ui.Nav.Up):
		m.row = max(0, m.row-1)
	case key.Matches(msg, ui.Nav.Down):
		m.row = min(m.patternRow(), m.row+1)
	case key.Matches(msg, ui.Nav.Top):
		m.row = checkDefRowID
	case key.Matches(msg, ui.Nav.Bottom):
		m.row = m.patternRow()
	case key.Matches(msg, authorKeys.Review):
		return m.plan(), false
	case key.Matches(msg, ui.Nav.Enter, ui.Nav.Toggle):
		switch {
		case m.row > checkDefRowID && m.row < m.patternRow():
			m.kind = m.kinds[m.row-1]
		case key.Matches(msg, ui.Nav.Toggle):
			// space is a typed character in a text row, so it opens none.
		case m.row == m.patternRow() && !m.takesPattern():
			return toast(ui.ToneNeutral, "Read-only: "+string(m.kind)+" takes no pattern."), false
		case m.row == checkDefRowID && m.idLock != "":
			return refusedToast(m.idLock), false
		case m.row == m.patternRow() && m.patternLock != "":
			return refusedToast(m.patternLock), false
		default:
			m.editing = true
		}
	}

	return nil, false
}

// refusedToast tells why a text row takes no keys. It uses the words of
// the pin form, so both forms name the same by-hand route.
func refusedToast(lock string) tea.Cmd {
	return toast(ui.ToneNeutral, "Read-only: "+ui.OneLine(lock)+".")
}

// keys returns the footer keys of the form or of the current stage.
func (m *checkDefModal) keys() []key.Binding {
	back := func(label string) key.Binding { return ui.WithHelp(ui.Nav.Back, "esc", label) }

	switch m.stage {
	case flowClosed:
		if m.editing {
			return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "done"), back("done")}
		}

		action := "edit"
		if m.row > checkDefRowID && m.row < m.patternRow() {
			action = "choose"
		}

		return []key.Binding{
			ui.WithHelp(ui.Nav.Enter, "enter", action), back("close"), authorKeys.Review,
			ui.WithHelp(ui.Nav.Down, "↑↓", "rows"),
		}
	case flowPreparing:
		return []key.Binding{back("cancel")}
	case flowPreview:
		confirm := ui.WithHelp(lesKeys.Confirm, "y", "write the definition")
		confirm.SetEnabled(m.confirmable())

		return []key.Binding{confirm, back("back to the form"), ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")}
	case flowCommitting:
		return nil
	}

	return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "close")}
}

// content builds the modal of the form or of the current stage.
func (m *checkDefModal) content(w int) ui.ModalOpts {
	t := m.st.theme

	switch m.stage {
	case flowClosed:
		return m.formContent(w)
	case flowPreparing:
		return flowWaitModal(m.st, "Preparing the check-definition review", "Seamark reads "+pinfile.File+
			" and the write gate. Nothing is written.", true, w)
	case flowCommitting:
		return flowWaitModal(m.st, m.st.mutation, "Seamark checks the review again, then replaces "+pinfile.File+
			". The result appears here.", false, w)
	case flowResult:
		return m.resultContent(w)
	}

	if m.err != nil {
		return previewFailedModal(t, "The check-definition review", m.err, "Fix the cause named above, then "+
			"press s in the form to review again.", "Nothing was changed.", m.keys(), w)
	}

	return m.reviewContent(w)
}

// formContent renders the rows of the form.
func (m *checkDefModal) formContent(w int) ui.ModalOpts {
	t := m.st.theme
	vw := max(1, w-2-pinFormLabelW)

	rows := formRows{t: t, labelW: pinFormLabelW, w: w, cursor: m.row, keep: [2]int{-1, -1}}
	rows.lines = propLines(t.Muted, "One definition under checks in "+pinfile.File+". A pin runs it only when "+
		"its check names the ID and its mode is check or enforce.", w)
	rows.lines = append(rows.lines, "")

	// A locked row shows its stored text with U+XXXX names for the
	// characters that the editor cannot show (pinEscapedLine).
	text := func(in *ui.LineInput, row int, lock string) []string {
		switch {
		case m.editing && m.row == row:
			return pinInputLines(in.View(t), vw)
		case in.Value() == "":
			return []string{t.Faint.Render("(empty)")}
		case lock != "":
			return propLines(t.Muted, pinEscapedLine(in.Value()), vw)
		}

		return propLines(t.Body, ui.OneLine(in.Value()), vw)
	}

	rows.add(checkDefRowID, "ID", text(&m.id, checkDefRowID, m.idLock), noticeLines(t, m.idLock, m.id.Notice(), vw))

	for i, kind := range m.kinds {
		label := ""
		if i == 0 {
			label = "Kind"
		}

		rows.add(1+i, label, []string{radio(t, m.kind == kind, string(kind))}, nil)
	}

	pattern := text(&m.pattern, m.patternRow(), m.patternLock)
	if !m.takesPattern() {
		pattern = []string{t.Faint.Render("(" + string(m.kind) + " takes no pattern)")}
	}

	rows.add(m.patternRow(), "Pattern", pattern, noticeLines(t, m.patternNoteLock(), m.pattern.Notice(), vw))

	return ui.ModalOpts{
		Title: t.Title.Render("Check definition"), Right: t.Attention.Render("draft"), Body: rows.lines,
		Tail:    propLines(t.Muted, "s reviews the change; nothing is written before y", w),
		Actions: m.keys(), Follow: true, Keep: rows.keep,
	}
}

// patternNoteLock returns the lock that the pattern row names. A kind
// without a pattern hides the row value, so the lock stays quiet then.
func (m *checkDefModal) patternNoteLock() string {
	if !m.takesPattern() {
		return ""
	}

	return m.patternLock
}

// noticeLines renders the read-only reason and the notice of an input,
// or nothing.
func noticeLines(t *ui.Theme, lock, notice string, w int) []string {
	var out []string

	if lock != "" {
		out = append(out, propLines(t.Muted, "Read-only: "+ui.OneLine(lock)+".", w)...)
	}

	if notice != "" {
		out = append(out, propNote(t, ui.ToneAttention, ui.OneLine(notice), w)...)
	}

	return out
}

// reviewContent renders a plan: the definition, the predicate, every
// blocker, the impact on the bound pins, and the exact diff.
func (m *checkDefModal) reviewContent(w int) ui.ModalOpts {
	t := m.st.theme
	p := m.preview
	sep := " " + t.Glyphs.Sep + " "

	op := "Replaces the definition"
	if p.Create {
		op = "Adds the definition"
	}

	rows := [][2]string{
		{"Check", ui.OneLine(p.Request.ID)},
		{"Kind", ui.OneLine(p.Request.Kind)},
	}

	if p.Request.Pattern != "" {
		rows = append(rows, [2]string{"Pattern", ui.OneLine(p.Request.Pattern)})
	}

	rows = append(rows, [2]string{"File", op + " under checks in " + pinfile.File + ". The pin list keeps its bytes."})

	if p.Version {
		rows = append(rows, [2]string{"Version", "also writes lesson_checks_version: 1"})
	}

	body := propKV(t, rows, w)

	for _, b := range p.Blockers {
		body = append(body, "")
		body = append(body, propNote(t, ui.ToneAttention, pinSentence(ui.OneLine(pinBlockerLine(m.st, b))), w)...)
	}

	body = append(body, "", t.Heading.Render("PREDICATE"))

	if p.Predicate == "" {
		body = append(body, propLines(t.Attention, "not available: no checker reads this kind", w)...)
	} else {
		body = append(body, propLines(t.Body, ui.OneLine(p.Predicate), w)...)
	}

	body = append(body, "", t.Heading.Render("CHECK IMPACT")+"  "+t.Faint.Render("the pins that bind "+
		ui.OneLine(p.Request.ID)))
	body = append(body, impactLines(t, p.Impact, "No check or enforce pin binds this ID, so no checker changes.", w)...)

	body = append(body, "", t.Heading.Render("DIFF")+"  "+maintCounts(t, p.Added, p.Removed))

	if p.After == nil {
		body = append(body, propLines(t.Muted, "No file change: a blocker stops the plan.", w)...)
	} else {
		body = append(body, setupDiffLines(t, p.Diff, w)...)
	}

	tail := propLines(t.Body, "Check definition"+sep+"y writes "+pinfile.File, w)

	switch {
	case p.NoOp:
		tail = propLines(t.Muted, "Check definition"+sep+"no change: checks already holds this definition", w)
	case !p.Confirmable:
		tail = propNote(t, ui.ToneAttention, "Check definition"+sep+"y is off: a blocker above names the fix", w)
	case m.st.busy():
		tail = propNote(t, ui.ToneAttention, "Check definition"+sep+"y waits: another operation is running", w)
	}

	return ui.ModalOpts{
		Title: t.Title.Render("Review the check definition"), Body: body, Tail: tail, Actions: m.keys(),
	}
}

// resultContent reports what the commit did to the file. The title never
// claims a change that did not happen, and nothing is retried.
func (m *checkDefModal) resultContent(w int) ui.ModalOpts {
	t := m.st.theme

	title, tone, lead := "Not written", ui.ToneFailure, "The definition was not written, and "+pinfile.File+
		" keeps its earlier bytes."

	switch {
	case m.resErr != nil:
		lead += " " + ui.ErrText(m.resErr)
	case m.res.Outcome == maintain.Written:
		title, tone, lead = "Check definition written", ui.ToneSuccess, "Wrote the definition into "+pinfile.File+
			". Review the diff and commit it."
	case m.res.Outcome == maintain.Unknown:
		title, tone, lead = "Result unknown", ui.ToneAttention, unknownOutcomeText(pinfile.File)
	}

	body := propNote(t, tone, lead, w)

	if m.resErr == nil && m.res.Err != nil {
		body = append(body, "")
		body = append(body, propLines(t.Muted, "Error: "+maintErrText(m.res.Err), w)...)
	}

	body = append(body, m.reload.row(t, w)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return ui.ModalOpts{Title: t.ToneStyle(tone).Render(title), Body: body, Actions: m.keys()}
}

// impactLines renders the K1 impact blocks with the renderer of the
// engine (lessoncheck.ImpactLines), so the console shows the words of
// the CLI. none is the line of a plan without a block.
func impactLines(t *ui.Theme, impacts []lessoncheck.Impact, none string, w int) []string {
	blocks := lessoncheck.ImpactLines(impacts)
	if len(blocks) == 0 {
		return propLines(t.Muted, none, w)
	}

	var out []string

	for _, line := range blocks {
		out = append(out, hangLines(t.Body, line, w)...)
	}

	return out
}

// openCheckDef opens the definition form of M. It works without a pin
// under the cursor: the form then starts empty and adds a definition.
func (p *pinsView) openCheckDef() tea.Cmd {
	in := p.inspection()

	var pin *maintain.Pin
	if cur, ok := p.cursorRow(p.rows()); ok {
		pin = cur.pin
	}

	p.checkDef = newCheckDefModal(p.st, in, pin)

	return nil
}

// checkDefCommitted applies a commit outcome. A written file reloads the
// pins under a check of that reload, and every holder of a preview of
// lessons.yaml learns of the write.
func (p *pinsView) checkDefCommitted(msg checkDefCommittedMsg) tea.Cmd {
	m := msg.m
	reload := refresh(pinWriteRefresh...)
	written := msg.err == nil && msg.res.Outcome == maintain.Written
	unknown := msg.err == nil && msg.res.Outcome == maintain.Unknown

	if m.commitDone() {
		m.res, m.resErr = msg.res, msg.err

		if written {
			shown := func() bool { return p.checkDef == m && m.stage == flowResult }
			m.reload, reload = p.st.watchRefresh(pinfile.File, shown, pinWriteRefresh...)
		}
	}

	cmds := []tea.Cmd{mutationDone, reload}
	if written || unknown {
		cmds = append(cmds, sendLessonsSaved(time.Now(), "Lessons "+p.st.theme.Glyphs.Arrow+" Pins"))
	}

	return tea.Batch(cmds...)
}
