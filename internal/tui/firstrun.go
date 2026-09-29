package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
)

// firstRun is the guide of a fresh workspace (screen 11 of the 1a
// design). It replaces the body with three steps until the user starts
// a step that opens a destination or chooses the read-only view. The
// choice lasts for this invocation only: no onboarding marker is
// written, because the console writes only files that the user reviewed.
type firstRun struct {
	st *state

	stage  frStage
	cursor int

	// agents are the IDs of the clients whose agent command is on PATH.
	// agentsKnown stays false until the PATH check returns.
	agents      []string
	agentsKnown bool

	// preview is the plan of the Create config step; planErr is the
	// planning failure.
	preview *SetupPreview
	planErr error

	// result and resultErr are the outcome of the last apply.
	result    integration.SetupResult
	resultErr error
	// configDone is true after an apply that wrote or kept every file.
	configDone bool
}

// frStage is the part of the Create config step that the guide shows.
type frStage int

const (
	// frSteps shows the three steps.
	frSteps frStage = iota
	// frPlanning waits for the plan of the shared documents.
	frPlanning
	// frReview shows the plan and waits for y.
	frReview
	// frApplying waits for the apply of the reviewed plan.
	frApplying
	// frResult shows what the apply wrote.
	frResult
)

// The steps in their display order.
const (
	frStepConfig = iota
	frStepIndex
	frStepAgents
	frStepCount
)

// frStepNames are the step labels of the 1a design.
var frStepNames = [frStepCount]string{"Create config", "Build the index", "Connect agents"}

// frMutation names the apply in the notice row and the quit dialog.
const frMutation = "Creating the Seamark config"

// frRefreshKinds are the sources that the shared documents change: the
// policy feeds the doctor checks, the Guard card and the integrations.
var frRefreshKinds = []sectionKind{secDoctor, secIndex, secIntegrations}

// frKeys are the bindings of the guide.
var frKeys = struct {
	Step, Start, ReadOnly, Apply, Back, Steps key.Binding
}{
	Step:     withHelp(nkeys.Down, "↑↓", "step"),
	Start:    withHelp(nkeys.Enter, "enter", "start"),
	ReadOnly: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "read-only view")),
	Apply:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "apply")),
	Back:     key.NewBinding(key.WithKeys("esc", "n"), key.WithHelp("esc", "back")),
	Steps:    withHelp(nkeys.Enter, "enter", "back to the steps"),
}

// firstRunMsg marks the result messages of the guide. The guide is not
// a screen, so the shell routes these messages to it by this type.
type firstRunMsg interface {
	isFirstRunMsg()
}

// frAgentsMsg carries the result of the PATH check.
type frAgentsMsg struct{ ids []string }

// frPlanMsg carries the plan of the shared documents.
type frPlanMsg struct {
	preview *SetupPreview
	err     error
}

// frApplyMsg carries the outcome of the apply.
type frApplyMsg struct {
	res integration.SetupResult
	err error
}

func (frAgentsMsg) isFirstRunMsg() {}
func (frPlanMsg) isFirstRunMsg()   {}
func (frApplyMsg) isFirstRunMsg()  {}

// startFirstRun shows the guide and starts the PATH check. The check
// runs as a command, because Update and View never call a service.
func (m *model) startFirstRun() tea.Cmd {
	m.welcome = &firstRun{st: m.st}
	svc := m.st.svc

	return func() tea.Msg { return frAgentsMsg{ids: svc.AgentsOnPath()} }
}

// firstRunResult routes a result message to the guide. An apply result
// frees the mutation slot even when the guide is gone, so the slot can
// never stay taken.
func (m *model) firstRunResult(msg firstRunMsg) tea.Cmd {
	if m.welcome != nil {
		return m.welcome.update(msg)
	}

	if _, ok := msg.(frApplyMsg); ok {
		return tea.Batch(mutationDone, refresh(frRefreshKinds...))
	}

	return nil
}

// welcomeKey handles a key while the guide shows. The tabs are disabled
// here, so the number keys do nothing. q quits from every stage; while
// the apply runs, the shell asks first.
func (m *model) welcomeKey(msg tea.KeyMsg) tea.Cmd {
	fr := m.welcome

	if key.Matches(msg, gkeys.Quit) {
		return m.requestQuit()
	}

	switch fr.stage {
	case frSteps:
		return m.firstRunStepKey(msg)
	case frPlanning:
		// A late plan after esc is dropped, see update.
		if key.Matches(msg, frKeys.Back) {
			fr.stage = frSteps
		}
	case frReview:
		switch {
		case key.Matches(msg, frKeys.Apply) && fr.canApply():
			return fr.apply()
		case key.Matches(msg, frKeys.Back):
			fr.backToSteps()
		}
	case frResult:
		switch {
		case key.Matches(msg, nkeys.Enter, frKeys.Back):
			fr.backToSteps()
		case key.Matches(msg, frKeys.ReadOnly):
			m.welcome = nil
		}
	case frApplying:
		// The apply cannot stop safely, so only quit acts here.
	}

	return nil
}

// firstRunStepKey moves between the steps or starts one. Steps 2 and 3
// open the destination that owns the workflow, so the guide adds no
// second build or setup flow.
func (m *model) firstRunStepKey(msg tea.KeyMsg) tea.Cmd {
	fr := m.welcome

	switch {
	case key.Matches(msg, nkeys.Up, nkeys.Prev, nkeys.Left):
		fr.cursor = max(0, fr.cursor-1)
	case key.Matches(msg, nkeys.Down, nkeys.Next, nkeys.Right):
		fr.cursor = min(frStepCount-1, fr.cursor+1)
	case key.Matches(msg, nkeys.Top):
		fr.cursor = 0
	case key.Matches(msg, nkeys.Bottom):
		fr.cursor = frStepCount - 1
	case key.Matches(msg, frKeys.ReadOnly, nkeys.Back):
		m.welcome = nil
	case key.Matches(msg, nkeys.Enter):
		switch fr.cursor {
		case frStepConfig:
			return fr.plan()
		case frStepIndex:
			m.welcome = nil

			return m.goTo(destOverview, "build")
		case frStepAgents:
			m.welcome = nil

			return m.goTo(destIntegrations, "setup")
		}
	}

	return nil
}

// update records a result message.
func (fr *firstRun) update(msg firstRunMsg) tea.Cmd {
	switch msg := msg.(type) {
	case frAgentsMsg:
		fr.agents, fr.agentsKnown = msg.ids, true
	case frPlanMsg:
		// esc during planning went back to the steps; the plan is then
		// out of date before anyone reviews it.
		if fr.stage != frPlanning {
			return nil
		}

		fr.stage = frReview
		fr.preview, fr.planErr = msg.preview, msg.err
	case frApplyMsg:
		fr.stage = frResult
		fr.result, fr.resultErr = msg.res, msg.err
		fr.configDone = msg.err == nil && frAllLanded(msg.res)

		return tea.Batch(mutationDone, refresh(frRefreshKinds...))
	}

	return nil
}

// plan asks for the plan of the shared Seamark documents only. A nil
// client list plans no agent file. Planning reads; it writes nothing.
func (fr *firstRun) plan() tea.Cmd {
	fr.stage = frPlanning
	fr.preview, fr.planErr = nil, nil
	svc := fr.st.svc

	return func() tea.Msg {
		p, err := svc.PlanSetup(nil)

		return frPlanMsg{preview: p, err: err}
	}
}

// canApply reports a plan that has writes and that is still current.
func (fr *firstRun) canApply() bool {
	return fr.planErr == nil && fr.preview != nil && len(fr.preview.Writes) > 0 && !fr.planStale()
}

// planStale reports a planned file that changed or failed to read after
// planning. The apply would refuse such a plan.
func (fr *firstRun) planStale() bool {
	if fr.preview == nil {
		return false
	}

	for _, w := range fr.preview.Writes {
		if w.Stale || w.Err != nil {
			return true
		}
	}

	return false
}

// apply writes exactly the reviewed plan through the shared setup
// engine. It claims the single mutation slot first, so a second key
// press cannot start a second write.
func (fr *firstRun) apply() tea.Cmd {
	if !fr.st.begin(frMutation) {
		return toast(ToneAttention, "Another operation is running: "+fr.st.mutation)
	}

	fr.stage = frApplying
	svc, p := fr.st.svc, fr.preview

	return func() tea.Msg {
		res, err := svc.ApplySetup(p)

		return frApplyMsg{res: res, err: err}
	}
}

// backToSteps shows the steps again. After a complete apply the cursor
// moves to the next step.
func (fr *firstRun) backToSteps() {
	fr.stage = frSteps
	fr.preview, fr.planErr = nil, nil

	if fr.configDone {
		fr.cursor = frStepIndex
	}
}

// frAllLanded reports an apply that reported operations and wrote or
// kept every one of them.
func frAllLanded(res integration.SetupResult) bool {
	ops := append(append([]integration.OpResult(nil), res.Ops...), res.Skills...)
	if len(ops) == 0 {
		return false
	}

	for _, op := range ops {
		if op.Status != integration.OpApplied && op.Status != integration.OpKept {
			return false
		}
	}

	return true
}

// frApplied counts the operations that wrote a file.
func frApplied(res integration.SetupResult) int {
	n := 0

	for _, op := range append(append([]integration.OpResult(nil), res.Ops...), res.Skills...) {
		if op.Status == integration.OpApplied {
			n++
		}
	}

	return n
}

// keys returns the footer bindings of the visible stage.
func (fr *firstRun) keys() []key.Binding {
	switch fr.stage {
	case frSteps:
		return []key.Binding{frKeys.Step, frKeys.Start}
	case frPlanning:
		return []key.Binding{frKeys.Back}
	case frReview:
		if fr.canApply() {
			return []key.Binding{frKeys.Apply, frKeys.Back}
		}

		return []key.Binding{frKeys.Back}
	case frResult:
		return []key.Binding{frKeys.Steps}
	default:
		return nil
	}
}

// boxHints returns the key row inside the box. It names every key of
// the stage, quit included, as in the 1a design.
func (fr *firstRun) boxHints() []key.Binding {
	switch fr.stage {
	case frSteps:
		return []key.Binding{frKeys.Start, frKeys.ReadOnly, gkeys.Quit}
	case frReview:
		if fr.canApply() {
			return []key.Binding{frKeys.Apply, frKeys.Back, gkeys.Quit}
		}

		return []key.Binding{frKeys.Back, gkeys.Quit}
	case frResult:
		return []key.Binding{frKeys.Steps, frKeys.ReadOnly, gkeys.Quit}
	case frPlanning:
		return []key.Binding{frKeys.Back, gkeys.Quit}
	default:
		return []key.Binding{gkeys.Quit}
	}
}

// view renders the guide in w by h cells: a box of 76 cells at most, as
// in the 1a design. The key row always stays visible.
func (fr *firstRun) view(w, h int) string {
	t := fr.st.theme
	bw := max(20, min(w-4, 76))
	inner := boxInner(bw)

	var body []string

	switch fr.stage {
	case frSteps:
		body = fr.stepsBody(inner)
	case frPlanning:
		body = []string{fr.stepHead("plan"), "",
			t.Muted.Render(truncate("Reading the files to plan the shared documents"+t.Glyphs.Ellipsis, inner, t.Glyphs.Ellipsis)), ""}
	case frReview:
		body = fr.reviewBody(inner)
	case frApplying:
		body = []string{fr.stepHead("writing"), "",
			t.Accent.Render(t.spinnerFrame(fr.st.tick)) + " " + t.Body.Render(frMutation+t.Glyphs.Ellipsis),
			t.Muted.Render(truncate("The write cannot stop safely at this point.", inner, t.Glyphs.Ellipsis)), ""}
	case frResult:
		body = fr.resultBody(inner)
	}

	tail := []string{t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner)), t.hints(fr.boxHints()), ""}
	lines := frFitLines(body, tail, boxRows(h), t.Faint.Render(t.Glyphs.Ellipsis))

	box := t.box(boxOpts{
		W: bw, H: len(lines) + 2, Focus: true, Lines: lines,
		Title: t.Title.Render("Set up seamark in this repository"),
	})

	// The box sits a little above the middle, as in the 1a design: two
	// fifths of the free rows go above it, so at 120x40 its top border is
	// on row 12. The shell pads the rows below.
	top := max(0, (h-len(lines)-2)*2/5)

	return strings.Repeat("\n", top) + lipgloss.PlaceHorizontal(w, lipgloss.Center, box)
}

// frFitLines fits the body and the tail into rows lines. The tail holds
// the key row, so it stays; the trailing blank line goes first, then
// the blank body lines from the end, then the last body lines, which a
// faint ellipsis replaces.
func frFitLines(body, tail []string, rows int, more string) []string {
	if len(body)+len(tail) > rows && len(tail) > 0 && isBlank(tail[len(tail)-1]) {
		tail = tail[:len(tail)-1]
	}

	if excess := len(body) + len(tail) - rows; excess > 0 {
		body = dropBlankLines(body, excess)
	}

	if room := rows - len(tail); len(body) > room {
		if room <= 0 {
			return tail[max(0, len(tail)-rows):]
		}

		body = append(append([]string(nil), body[:room-1]...), more)
	}

	return append(append([]string(nil), body...), tail...)
}

// stepsBody renders the lead line, the three steps and the write note.
func (fr *firstRun) stepsBody(inner int) []string {
	t := fr.st.theme

	lead := "No .seamark/ directory in "
	path := truncateLeft(oneLine(displayPath(fr.st.svc.Root())), max(8, inner-width(lead)-1), t.Glyphs.Ellipsis)

	lines := []string{t.Muted.Render(lead) + t.Body.Render(path) + t.Muted.Render("."), ""}

	for i := range frStepCount {
		lines = append(lines, fr.stepRow(i, inner))
	}

	lines = append(lines, "")

	return append(append(lines, frWriteNote(t, inner)...), "")
}

// frWriteNote renders "Each step shows its plan first. Nothing is
// written until you press y." with the key in the key style.
func frWriteNote(t *Theme, inner int) []string {
	lines := strings.Split(wrap("Each step shows its plan first. Nothing is written until you press y.", inner), "\n")

	for i, l := range lines {
		if rest, ok := strings.CutSuffix(l, "y."); ok && i == len(lines)-1 {
			lines[i] = t.Faint.Render(rest) + t.Key.Render("y") + t.Faint.Render(".")

			continue
		}

		lines[i] = t.Faint.Render(l)
	}

	return lines
}

// stepRow renders one step: the cursor, the number and label, and what
// the step covers. The selected row spans the full inner width.
func (fr *firstRun) stepRow(i, inner int) string {
	t := fr.st.theme

	labelW := 24
	if inner < 64 {
		labelW = 20
	}

	selected := i == fr.cursor
	cursor := "  "
	label, desc := t.Muted, t.Faint

	if selected {
		cursor = t.Key.Render(t.Glyphs.Cursor) + " "
		label, desc = t.Title, t.Muted
	}

	row := t.cols(cell(cursor, 2), cell(label.Render(itoa(i+1)+"  "+frStepNames[i]), labelW),
		cell(fr.stepDesc(i, desc), max(1, inner-2-labelW)))

	if selected {
		row = t.highlight(row, inner, bgSel)
	}

	return row
}

// stepDesc says what a step covers, from real data only: the shared
// documents, the local build, and the agents found on PATH.
func (fr *firstRun) stepDesc(i int, style lipgloss.Style) string {
	t := fr.st.theme
	g := t.Glyphs

	switch i {
	case frStepConfig:
		if fr.configDone {
			n := frApplied(fr.result)

			return t.Success.Render(g.OK+" done") + t.Faint.Render(" "+g.Sep+" "+itoa(n)+" "+docPlural(n, "file", "files")+" written")
		}

		return style.Render(".seamark/ policy, lessons, config " + g.Sep + " .gitignore")
	case frStepIndex:
		return style.Render("local parse " + g.Sep + " git history")
	default:
		switch {
		case !fr.agentsKnown:
			return style.Render("checking PATH" + g.Ellipsis)
		case len(fr.agents) == 0:
			return style.Render("no agent command on PATH")
		}

		names := make([]string, 0, len(fr.agents))
		for _, id := range fr.agents {
			names = append(names, t.Body.Render(oneLine(id)))
		}

		return style.Render("found on PATH: ") + strings.Join(names, style.Render(", "))
	}
}

// stepHead renders the heading of the Create config step, for example
// "1  Create config · plan".
func (fr *firstRun) stepHead(phase string) string {
	t := fr.st.theme

	return t.Title.Render("1  "+frStepNames[frStepConfig]) + t.Faint.Render("  "+t.Glyphs.Sep+" "+phase)
}

// reviewBody renders the plan: one row per planned write and per kept
// file, what the step does not touch, and the write note.
func (fr *firstRun) reviewBody(inner int) []string {
	t := fr.st.theme
	g := t.Glyphs

	lines := []string{fr.stepHead("plan"), ""}

	if fr.planErr != nil || fr.preview == nil || fr.preview.Plan == nil {
		msg := "The planner returned no plan."
		if fr.planErr != nil {
			msg = errText(fr.planErr)
		}

		lines = append(lines, t.Failure.Render(g.Fail+" Could not plan the config."))

		return append(lines, frWrapped(t.Muted, msg, inner)...)
	}

	pathW := min(30, max(12, inner/2))

	for _, w := range fr.preview.Writes {
		lines = append(lines, fr.writeRow(w, pathW, inner))
	}

	for _, k := range fr.preview.Plan.Kept {
		lines = append(lines, t.cols(cell(t.Faint.Render("="), 2),
			cell(t.Muted.Render(truncateLeft(oneLine(k.Path), pathW-1, g.Ellipsis)), pathW),
			cell(t.Success.Render("kept")+t.Faint.Render(frDetail(g, k.Detail)), max(1, inner-2-pathW))))
	}

	for _, f := range fr.preview.Plan.Findings {
		badge := t.Badge(ToneNeutral, "")
		if f.Level >= integration.FindingWarning {
			badge = t.Badge(ToneAttention, "")
		}

		lines = append(lines, truncate(badge+t.Body.Render(diag(f.Reason)), inner, g.Ellipsis))
	}

	if len(fr.preview.Writes) == 0 {
		lines = append(lines, t.Success.Render(g.OK+" Every shared document is present. Nothing to write."))
	}

	lines = append(lines, "", truncate(t.Heading.Render("WON'T")+" "+t.Muted.Render("touch agent files or the index."),
		inner, g.Ellipsis))

	switch {
	case fr.planStale():
		lines = append(lines, "", t.Attention.Render(truncate(g.Warn+" A file changed after planning. esc and start again.",
			inner, g.Ellipsis)))
	case len(fr.preview.Writes) > 0:
		lines = append(lines, "", t.Body.Render("Nothing is written until you press ")+t.Key.Render("y")+t.Body.Render("."))
	}

	return lines
}

// writeRow renders one planned write: "+" for a new file, "M" for a
// change, the path, the line counts and the planner's detail.
func (fr *firstRun) writeRow(w integration.WritePreview, pathW, inner int) string {
	t := fr.st.theme
	g := t.Glyphs

	mark := t.Attention.Render("M")
	delta := ""

	if w.Created {
		mark = t.Success.Render(g.Add)
		delta = t.Faint.Render("new")
	} else {
		var parts []string
		if w.Added > 0 {
			parts = append(parts, t.Success.Render(g.Add+itoa(w.Added)))
		}

		if w.Removed > 0 {
			parts = append(parts, t.Failure.Render(g.Del+itoa(w.Removed)))
		}

		delta = strings.Join(parts, " ")
	}

	switch {
	case w.Err != nil:
		delta = t.Failure.Render(g.Fail + " " + errText(w.Err))
	case w.Stale:
		delta = t.Attention.Render(g.Warn + " changed after planning")
	default:
		delta += t.Faint.Render(frDetail(g, w.Detail))
	}

	return t.cols(cell(mark, 2), cell(t.Code.Render(truncateLeft(oneLine(w.Path), pathW-1, g.Ellipsis)), pathW),
		cell(delta, max(1, inner-2-pathW)))
}

// frWrapped wraps plain text to w cells and styles each line, so every
// box line carries its own style and no color runs into the border.
func frWrapped(style lipgloss.Style, text string, w int) []string {
	lines := strings.Split(wrap(text, w), "\n")

	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

// frDetail renders a planner detail after a separator, or "".
func frDetail(g Glyphs, detail string) string {
	if detail = oneLine(detail); detail != "" {
		return " " + g.Sep + " " + detail
	}

	return ""
}

// resultBody renders one row per operation and a verdict. Seamark does
// not roll back, so the verdict says which writes stay.
func (fr *firstRun) resultBody(inner int) []string {
	t := fr.st.theme
	g := t.Glyphs
	ops := append(append([]integration.OpResult(nil), fr.result.Ops...), fr.result.Skills...)

	lines := []string{fr.stepHead("result"), ""}

	const statusW = 16

	for _, op := range ops {
		tone := ToneNeutral

		switch op.Status {
		case integration.OpApplied:
			tone = ToneSuccess
		case integration.OpFailed:
			tone = ToneFailure
		case integration.OpNotAttempted:
			tone = ToneAttention
		}

		lines = append(lines, t.cols(cell(t.Badge(tone, op.Status.String()), statusW),
			cell(t.Code.Render(oneLine(op.Path)), max(1, inner-statusW))))

		if op.Err != nil {
			lines = append(lines, strings.Repeat(" ", statusW)+t.Failure.Render(truncate(errText(op.Err), max(1, inner-statusW), g.Ellipsis)))
		}
	}

	if len(ops) > 0 {
		lines = append(lines, "")
	}

	tone, verdict := ToneAttention, "Some operations did not land. Seamark does not roll back: applied files stay written."

	switch {
	case fr.resultErr == nil && len(ops) == 0:
		tone, verdict = ToneUnknown, "The setup engine reported no operation, so nothing is known to have changed."
	case fr.configDone:
		tone, verdict = ToneSuccess, "The config is written. Next: build the index."
	case errors.Is(fr.resultErr, integration.ErrStalePlan) && frApplied(fr.result) == 0:
		verdict = "A file changed after the plan. Nothing was written. Start the step again to plan again."
	case fr.resultErr != nil:
		tone = ToneFailure
		verdict = "Setup stopped: " + errText(fr.resultErr) + ". Seamark does not roll back: applied files stay written."
	}

	return append(lines, frWrapped(t.ToneStyle(tone), t.ToneGlyph(tone)+" "+verdict, inner)...)
}
