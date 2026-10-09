package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/ui"
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
// The stages use the preview words of the console (previewflow.go). The
// guide keeps its own stage type, because it reads its plan without a
// ticket.
type frStage int

const (
	// frSteps shows the three steps.
	frSteps frStage = iota
	// frPreparing waits for the plan of the shared documents.
	frPreparing
	// frPreview shows the plan and waits for y.
	frPreview
	// frCommitting waits for the apply of the reviewed plan.
	frCommitting
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
// The starter config.yaml feeds the settings and the review window of
// the evidence view. The starter lessons.yaml feeds the Pins sub-view.
var frRefreshKinds = []sectionKind{secDoctor, secIndex, secIntegrations, secSettings, secLearning, secPins}

// frKeys are the bindings of the guide.
var frKeys = struct {
	Step, Start, ReadOnly, Apply, Back, Steps key.Binding
}{
	Step:     ui.WithHelp(ui.Nav.Down, "↑↓", "step"),
	Start:    ui.WithHelp(ui.Nav.Enter, "enter", "start"),
	ReadOnly: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "read-only view")),
	Apply:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "apply")),
	Back:     key.NewBinding(key.WithKeys("esc", "n"), key.WithHelp("esc", "back")),
	Steps:    ui.WithHelp(ui.Nav.Enter, "enter", "back to the steps"),
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

	var svc Reader = m.st.svc

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
	case frPreparing:
		// A late plan after esc is dropped, see update.
		if key.Matches(msg, frKeys.Back) {
			fr.stage = frSteps
		}
	case frPreview:
		switch {
		case key.Matches(msg, frKeys.Apply) && fr.canApply():
			return fr.apply()
		case key.Matches(msg, frKeys.Back):
			fr.backToSteps()
		}
	case frResult:
		switch {
		case key.Matches(msg, ui.Nav.Enter, frKeys.Back):
			fr.backToSteps()
		case key.Matches(msg, frKeys.ReadOnly):
			m.welcome = nil
		}
	case frCommitting:
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
	case key.Matches(msg, ui.Nav.Up, ui.Nav.Prev, ui.Nav.Left):
		fr.cursor = max(0, fr.cursor-1)
	case key.Matches(msg, ui.Nav.Down, ui.Nav.Next, ui.Nav.Right):
		fr.cursor = min(frStepCount-1, fr.cursor+1)
	case key.Matches(msg, ui.Nav.Top):
		fr.cursor = 0
	case key.Matches(msg, ui.Nav.Bottom):
		fr.cursor = frStepCount - 1
	case key.Matches(msg, frKeys.ReadOnly, ui.Nav.Back):
		m.welcome = nil
	case key.Matches(msg, ui.Nav.Enter):
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
		if fr.stage != frPreparing {
			return nil
		}

		fr.stage = frPreview
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
	fr.stage = frPreparing
	fr.preview, fr.planErr = nil, nil

	var svc Setup = fr.st.svc

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
		return toast(ui.ToneAttention, "Another operation is running: "+fr.st.mutation)
	}

	fr.stage = frCommitting
	p := fr.preview

	var svc Setup = fr.st.svc

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
	case frPreparing:
		return []key.Binding{frKeys.Back}
	case frPreview:
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
	case frPreview:
		if fr.canApply() {
			return []key.Binding{frKeys.Apply, frKeys.Back, gkeys.Quit}
		}

		return []key.Binding{frKeys.Back, gkeys.Quit}
	case frResult:
		return []key.Binding{frKeys.Steps, frKeys.ReadOnly, gkeys.Quit}
	case frPreparing:
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
	inner := ui.BoxInner(bw)

	var body []string

	switch fr.stage {
	case frSteps:
		body = fr.stepsBody(inner)
	case frPreparing:
		body = []string{fr.stepHead("plan"), "",
			t.Muted.Render(ui.Truncate("Reading the files to plan the shared documents"+t.Glyphs.Ellipsis, inner, t.Glyphs.Ellipsis)), ""}
	case frPreview:
		body = fr.reviewBody(inner)
	case frCommitting:
		body = []string{fr.stepHead("writing"), "",
			t.Accent.Render(t.SpinnerFrame(fr.st.tick)) + " " + t.Body.Render(frMutation+t.Glyphs.Ellipsis),
			t.Muted.Render(ui.Truncate("The write cannot stop safely at this point.", inner, t.Glyphs.Ellipsis)), ""}
	case frResult:
		body = fr.resultBody(inner)
	}

	tail := []string{t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner)), t.Hints(fr.boxHints()), ""}
	lines := frFitLines(body, tail, ui.BoxRows(h), t.Faint.Render(t.Glyphs.Ellipsis))

	box := t.Box(ui.BoxOpts{
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
	if len(body)+len(tail) > rows && len(tail) > 0 && ui.IsBlank(tail[len(tail)-1]) {
		tail = tail[:len(tail)-1]
	}

	if excess := len(body) + len(tail) - rows; excess > 0 {
		body = ui.DropBlankLines(body, excess)
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
	path := ui.TruncateLeft(ui.OneLine(ui.DisplayPath(fr.st.svc.Root())), max(8, inner-ui.Width(lead)-1), t.Glyphs.Ellipsis)

	lines := []string{t.Muted.Render(lead) + t.Body.Render(path) + t.Muted.Render("."), ""}

	for i := range frStepCount {
		lines = append(lines, fr.stepRow(i, inner))
	}

	lines = append(lines, "")

	return append(append(lines, frWriteNote(t, inner)...), "")
}

// frWriteNote renders "Each step shows its plan first. Nothing is
// written until you press y." with the key in the key style.
func frWriteNote(t *ui.Theme, inner int) []string {
	lines := strings.Split(ui.Wrap("Each step shows its plan first. Nothing is written until you press y.", inner), "\n")

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

	row := t.Cols(ui.Cell(cursor, 2), ui.Cell(label.Render(strconv.Itoa(i+1)+"  "+frStepNames[i]), labelW),
		ui.Cell(fr.stepDesc(i, desc), max(1, inner-2-labelW)))

	if selected {
		row = t.Highlight(row, inner, ui.BgSel)
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

			return t.Success.Render(g.OK+" done") + t.Faint.Render(" "+g.Sep+" "+strconv.Itoa(n)+" "+docPlural(n, "file", "files")+" written")
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
			names = append(names, t.Body.Render(ui.OneLine(id)))
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
			msg = ui.ErrText(fr.planErr)
		}

		lines = append(lines, t.Failure.Render(g.Fail+" Could not plan the config."))

		return append(lines, frWrapped(t.Muted, msg, inner)...)
	}

	pathW := min(30, max(12, inner/2))

	for _, w := range fr.preview.Writes {
		lines = append(lines, fr.writeRow(w, pathW, inner))
	}

	for _, k := range fr.preview.Plan.Kept {
		lines = append(lines, t.Cols(ui.Cell(t.Faint.Render("="), 2),
			ui.Cell(t.Muted.Render(ui.TruncateLeft(ui.OneLine(k.Path), pathW-1, g.Ellipsis)), pathW),
			ui.Cell(t.Success.Render("kept")+t.Faint.Render(frDetail(g, k.Detail)), max(1, inner-2-pathW))))
	}

	for _, f := range fr.preview.Plan.Findings {
		badge := t.Badge(ui.ToneNeutral, "")
		if f.Level >= integration.FindingWarning {
			badge = t.Badge(ui.ToneAttention, "")
		}

		lines = append(lines, ui.Truncate(badge+t.Body.Render(ui.Diag(f.Reason)), inner, g.Ellipsis))
	}

	if len(fr.preview.Writes) == 0 {
		lines = append(lines, t.Success.Render(g.OK+" Every shared document is present. Nothing to write."))
	}

	lines = append(lines, "", ui.Truncate(t.Heading.Render("WON'T")+" "+t.Muted.Render("touch agent files or the index."),
		inner, g.Ellipsis))

	switch {
	case fr.planStale():
		lines = append(lines, "", t.Attention.Render(ui.Truncate(g.Warn+" A file changed after planning. esc and start again.",
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
			parts = append(parts, t.Success.Render(g.Add+strconv.Itoa(w.Added)))
		}

		if w.Removed > 0 {
			parts = append(parts, t.Failure.Render(g.Del+strconv.Itoa(w.Removed)))
		}

		delta = strings.Join(parts, " ")
	}

	switch {
	case w.Err != nil:
		delta = t.Failure.Render(g.Fail + " " + ui.ErrText(w.Err))
	case w.Stale:
		delta = t.Attention.Render(g.Warn + " changed after planning")
	default:
		delta += t.Faint.Render(frDetail(g, w.Detail))
	}

	return t.Cols(ui.Cell(mark, 2), ui.Cell(t.Code.Render(ui.TruncateLeft(ui.OneLine(w.Path), pathW-1, g.Ellipsis)), pathW),
		ui.Cell(delta, max(1, inner-2-pathW)))
}

// frWrapped wraps plain text to w cells and styles each line, so every
// box line carries its own style and no color runs into the border.
func frWrapped(style lipgloss.Style, text string, w int) []string {
	lines := strings.Split(ui.Wrap(text, w), "\n")

	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

// frDetail renders a planner detail after a separator, or "".
func frDetail(g ui.Glyphs, detail string) string {
	if detail = ui.OneLine(detail); detail != "" {
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
		tone := ui.ToneNeutral

		switch op.Status {
		case integration.OpApplied:
			tone = ui.ToneSuccess
		case integration.OpFailed:
			tone = ui.ToneFailure
		case integration.OpNotAttempted:
			tone = ui.ToneAttention
		}

		lines = append(lines, t.Cols(ui.Cell(t.Badge(tone, op.Status.String()), statusW),
			ui.Cell(t.Code.Render(ui.OneLine(op.Path)), max(1, inner-statusW))))

		if op.Err != nil {
			lines = append(lines, strings.Repeat(" ", statusW)+t.Failure.Render(ui.Truncate(ui.ErrText(op.Err), max(1, inner-statusW), g.Ellipsis)))
		}
	}

	if len(ops) > 0 {
		lines = append(lines, "")
	}

	tone, verdict := ui.ToneAttention, "Some operations did not land. Seamark does not roll back: applied files stay written."

	switch {
	case fr.resultErr == nil && len(ops) == 0:
		tone, verdict = ui.ToneUnknown, "The setup engine reported no operation, so nothing is known to have changed."
	case fr.configDone:
		tone, verdict = ui.ToneSuccess, "The config is written. Next: build the index."
	case errors.Is(fr.resultErr, integration.ErrStalePlan) && frApplied(fr.result) == 0:
		verdict = "A file changed after the plan. Nothing was written. Start the step again to plan again."
	case fr.resultErr != nil:
		tone = ui.ToneFailure
		verdict = "Setup stopped: " + ui.ErrText(fr.resultErr) + ". Seamark does not roll back: applied files stay written."
	}

	return append(lines, frWrapped(t.ToneStyle(tone), t.ToneGlyph(tone)+" "+verdict, inner)...)
}
