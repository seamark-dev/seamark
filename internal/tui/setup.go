package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The setup wizard follows the shared setup engine: inspect, choose
// additions, review the plan, apply it, then inspect again. It keeps no
// durable state: reopening it derives every row from the files on disk.
// The review is the confirmation: "y" on the review applies the plan.
// While the user chooses or reviews, the global keys keep working and a
// destination switch keeps the choices, because a look elsewhere must
// not discard a reviewed selection. Only the running apply captures
// every key.

// setupStep is one step of the setup wizard. The last three steps use
// the preview words of the console (previewflow.go). The trail labels in
// setupStepNames stay as they are, because every capture shows them.
type setupStep int

const (
	setupStepClient setupStep = iota
	setupStepChoices
	setupStepPreview
	setupStepCommitting
	setupStepResult
)

// setupStepNames are the step names of the step trail.
var setupStepNames = []string{"Client", "Choose", "Review", "Apply"}

// setupIntent is one addition the wizard offers. Each intent maps onto
// one field of integration.ClientSetup.
type setupIntent int

const (
	setupMCP setupIntent = iota
	setupHooks
	setupSkills
	setupGrants
)

// setupIntents lists the additions in presentation order.
var setupIntents = []setupIntent{setupMCP, setupHooks, setupSkills, setupGrants}

// setupIntentLabels are the row labels of the additions.
var setupIntentLabels = map[setupIntent]string{
	setupMCP:    "MCP registration",
	setupHooks:  "Lifecycle hooks",
	setupSkills: "Managed skills",
	setupGrants: "Tool grants",
}

// setupIntentWords are the labels inside a sentence.
var setupIntentWords = map[setupIntent]string{
	setupMCP:    "MCP registration",
	setupHooks:  "lifecycle hooks",
	setupSkills: "managed skills",
	setupGrants: "tool grants",
}

// setupRowKind says what the wizard can do with one addition.
type setupRowKind int

const (
	// setupRowAdd is absent configuration: an optional checkbox.
	setupRowAdd setupRowKind = iota
	// setupRowRepair is partial, conflicting or unreadable configuration:
	// a checkbox that plans a repair.
	setupRowRepair
	// setupRowKept is current configuration. It is locked, because
	// unchecking it would imply an uninstall that setup never does.
	setupRowKept
	// setupRowUnsupported is an addition the client cannot plan.
	setupRowUnsupported
)

// setupRow is one addition for one client, derived from the current
// inspection.
type setupRow struct {
	intent setupIntent
	kind   setupRowKind
	state  integration.CapabilityState
	// describe is the current state in the shared diagnostic words.
	describe string
	// missing names the hooks that setup installs and that are absent.
	// It is empty for the other additions.
	missing []string
	// recommended rows start checked for a new client; skills and tool
	// grants stay an explicit choice.
	recommended bool
}

// selectable reports a row that the user can check or uncheck.
func (r setupRow) selectable() bool {
	return r.kind == setupRowAdd || r.kind == setupRowRepair
}

// setupPlannedMsg carries a plan result. owner names the wizard that
// asked for the plan, because every new wizard counts seq from zero
// again. seq drops a result that arrives after the user left the
// review of that wizard.
type setupPlannedMsg struct {
	owner   *setupWizard
	seq     int
	preview *services.SetupPreview
	err     error
}

// setupAppliedMsg carries an apply result. owner and seq have the same
// job as in setupPlannedMsg.
type setupAppliedMsg struct {
	owner  *setupWizard
	seq    int
	result integration.SetupResult
	err    error
}

// setupKeys are the action keys of the wizard.
var setupKeys = struct {
	Toggle   key.Binding
	Continue key.Binding
	About    key.Binding
	Apply    key.Binding
	Back     key.Binding
	Plan     key.Binding
	Done     key.Binding
}{
	Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
	Continue: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "review plan")),
	About:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "details")),
	// Apply takes only "y". An apply writes durable files, so enter, the
	// key that moves through the wizard, must never start it.
	Apply: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "apply")),
	Back:  key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "back")),
	Plan:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "plan again")),
	Done:  key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter", "done")),
}

// setupNothingSelected is the inline hint of the choice step when the
// user asks for a plan without a checked row.
const setupNothingSelected = "Select at least one addition to review a plan."

// setupWizard is the open setup flow of one client.
type setupWizard struct {
	st      *state
	clients []integration.Client

	step   setupStep
	client integration.Client
	// direct is true when the wizard opened with a client, so esc on the
	// choice step closes the wizard instead of showing the client list.
	direct bool
	pick   ui.ListState

	rows ui.ListState
	// aboutOpen shows the full explanation of the selected addition in
	// its own scrolling box. A short terminal cannot show it beside or
	// under the rows.
	aboutOpen bool
	about     ui.TextPager
	// choice holds the user's explicit choices. A row without one uses
	// its default, so late inspection data still gets defaults.
	choice map[setupIntent]bool
	// forceSkills requests the managed skills even when they are current.
	// A skill refresh sets it, because the user asked for exactly that.
	forceSkills bool
	// hint is the inline warning of the choice step. A toggle clears it.
	hint string

	// seq identifies the newest plan or apply request.
	seq generation
	// preparing reports that the plan read runs: the review step shows
	// the Preparing stage until the result arrives.
	preparing bool
	preview   *services.SetupPreview
	planErr   error
	// setups is the request of the current plan; plan again reuses it.
	setups []integration.ClientSetup
	paths  ui.ListState
	// pathOpen shows the diff box of the selected path in the narrow
	// layout.
	pathOpen bool
	// planOff is the first visible line of the plan notes when they do
	// not fit the Plan box.
	planOff int
	// notesFocus sends the movement keys of the wide review to the plan
	// notes. The notes hold trust and scope findings, so they must stay
	// reachable at every size.
	notesFocus bool
	pager      ui.TextPager

	result *setupOutcome
	closed bool
}

// setupOutcome is the result of one apply and the reinspection after it.
type setupOutcome struct {
	res integration.SetupResult
	err error
	// gen is the ticket of the inspection load that reflects the
	// applied files.
	gen       int
	inspected bool
}

func newSetupWizard(st *state, clients []integration.Client) *setupWizard {
	return &setupWizard{st: st, clients: clients, choice: map[setupIntent]bool{}}
}

// choose opens the choice step for a client.
func (wz *setupWizard) choose(c integration.Client) {
	wz.client = c
	wz.direct = true
	wz.step = setupStepChoices
}

// startRefresh plans a managed skill refresh for a client at once.
func (wz *setupWizard) startRefresh(c integration.Client) tea.Cmd {
	wz.choose(c)
	wz.forceSkills = true
	wz.choice = map[setupIntent]bool{setupMCP: false, setupHooks: false, setupSkills: true, setupGrants: false}

	return wz.plan(wz.request())
}

// capturing is true only during the apply. The other steps have no text
// input and no dialog, so the global keys (help, quit, refresh,
// destinations) stay available, and the screen keeps the wizard.
func (wz *setupWizard) capturing() bool {
	return wz.step == setupStepCommitting
}

// running reports an apply in progress.
func (wz *setupWizard) running() bool {
	return wz.step == setupStepCommitting
}

// inspected marks the reinspection after an apply as arrived.
func (wz *setupWizard) inspected(gen int) {
	if wz.result != nil && gen >= wz.result.gen {
		wz.result.inspected = true
	}
}

// planned stores a plan result unless the user left the review. A
// result of another wizard is dropped, so a closed wizard's late plan
// can never become the reviewed plan of a new one.
func (wz *setupWizard) planned(msg setupPlannedMsg) {
	if msg.owner != wz || !wz.seq.current(msg.seq) || wz.step != setupStepPreview {
		return
	}

	wz.preparing = false
	wz.preview, wz.planErr = msg.preview, msg.err
	wz.paths = ui.ListState{}
	wz.pathOpen = false
	wz.planOff = 0
}

// finished stores an apply result and the inspection ticket to wait
// for. A result of another wizard is dropped for the same reason as in
// planned.
func (wz *setupWizard) finished(msg setupAppliedMsg, gen int) {
	if msg.owner != wz || !wz.seq.current(msg.seq) {
		return
	}

	wz.step = setupStepResult
	wz.result = &setupOutcome{res: msg.result, err: msg.err, gen: gen}
	wz.pager = ui.TextPager{}
}

// inspection returns the current inspection of the wizard's client.
func (wz *setupWizard) inspection() (integration.Inspection, bool) {
	return integFind(wz.st, wz.client.ID)
}

// integFind returns the inspection of one client from the snapshot.
func integFind(st *state, id string) (integration.Inspection, bool) {
	if !st.integrations.loaded {
		return integration.Inspection{}, false
	}

	for _, insp := range st.integrations.value {
		if insp.ClientID == id {
			return insp, true
		}
	}

	return integration.Inspection{}, false
}

// currentRows derives the additions from the current inspection. It
// returns nil before the first inspection arrives.
func (wz *setupWizard) currentRows() []setupRow {
	insp, ok := wz.inspection()
	if !ok {
		return nil
	}

	return setupRows(wz.client, insp)
}

// setupHookCaps lists the hook capabilities that setup installs for a
// client. A client without a reset hook never lists the reset, so its
// absence does not make the hooks partial.
func setupHookCaps(c integration.Client) []integration.Capability {
	var caps []integration.Capability

	if c.Supports(integration.CapabilityEdits) {
		caps = append(caps, integration.CapabilityEdits)
	}

	if c.SetupOps.GateHook {
		caps = append(caps, integration.CapabilityCommands)
	}

	if c.SetupOps.ResetHook {
		caps = append(caps, integration.CapabilityResets)
	}

	return caps
}

// setupHookNames are the hook names inside a sentence.
var setupHookNames = map[integration.Capability]string{
	integration.CapabilityEdits:    "edit lessons",
	integration.CapabilityCommands: "command gate",
	integration.CapabilityResets:   "context reset",
}

// setupCombined folds several capability states into one row state.
// The worst problem wins; a mix of current and absent is partial.
func setupCombined(states []integration.CapabilityState) integration.CapabilityState {
	if len(states) == 0 {
		return integration.StateAbsent
	}

	count := map[integration.CapabilityState]int{}
	for _, s := range states {
		count[s]++
	}

	switch {
	case count[integration.StateUnreadable] > 0:
		return integration.StateUnreadable
	case count[integration.StateConflict] > 0:
		return integration.StateConflict
	case count[integration.StateCurrent] == len(states):
		return integration.StateCurrent
	case count[integration.StateAbsent] == len(states):
		return integration.StateAbsent
	default:
		return integration.StatePartial
	}
}

// setupRows derives one row per addition from the inspection. The
// descriptor decides support; the inspection decides the state.
func setupRows(c integration.Client, insp integration.Inspection) []setupRow {
	rows := make([]setupRow, 0, len(setupIntents))

	for _, intent := range setupIntents {
		row := setupRow{intent: intent, recommended: intent == setupMCP || intent == setupHooks}

		var supported bool

		switch intent {
		case setupMCP:
			supported = c.SetupOps.RegisterMCP
			row.state, row.describe = setupEntryState(insp, integration.CapabilityMCPRegistration)
		case setupHooks:
			supported = c.SetupOps.Hooks

			var states []integration.CapabilityState

			for _, capability := range setupHookCaps(c) {
				st, _ := setupEntryState(insp, capability)
				states = append(states, st)

				if st == integration.StateAbsent {
					row.missing = append(row.missing, setupHookNames[capability])
				}
			}

			row.state, row.describe = setupCombined(states), insp.DescribeHooks()
		case setupSkills:
			supported = c.Supports(integration.CapabilitySkills)
			row.state, row.describe = setupEntryState(insp, integration.CapabilitySkills)
		case setupGrants:
			supported = c.SetupOps.ApproveTools
			row.state, row.describe = setupEntryState(insp, integration.CapabilityToolGrants)
		}

		switch {
		case !supported:
			row.kind = setupRowUnsupported
		case row.state == integration.StateCurrent:
			row.kind = setupRowKept
		case row.state == integration.StateAbsent:
			row.kind = setupRowAdd
		default:
			row.kind = setupRowRepair
		}

		rows = append(rows, row)
	}

	return rows
}

// setupEntryState returns the state and the description of one
// capability. A missing entry reads as absent.
func setupEntryState(insp integration.Inspection, c integration.Capability) (state integration.CapabilityState, describe string) {
	entry, ok := insp.Entry(c)
	if !ok {
		return integration.StateAbsent, ""
	}

	return entry.State, entry.Describe()
}

// checked reports whether a row is part of the next plan.
func (wz *setupWizard) checked(r setupRow) bool {
	if !r.selectable() {
		return false
	}

	if v, ok := wz.choice[r.intent]; ok {
		return v
	}

	return r.kind == setupRowRepair || r.recommended
}

// request builds the setup intent from the checked rows. Locked rows
// are never requested: current configuration needs no plan, and an
// unsupported one cannot have one. It returns nil when nothing is
// selected.
func (wz *setupWizard) request() []integration.ClientSetup {
	cs := integration.ClientSetup{ClientID: wz.client.ID, GateMode: "", CheckHookSources: true}
	selected := false

	for _, r := range wz.currentRows() {
		if !wz.checked(r) {
			continue
		}

		selected = true

		switch r.intent {
		case setupMCP:
			cs.RegisterMCP = true
		case setupHooks:
			cs.Hooks = true
		case setupSkills:
			cs.Skills = true
		case setupGrants:
			cs.ApproveTools = true
		}
	}

	if wz.forceSkills && wz.client.Supports(integration.CapabilitySkills) {
		cs.Skills, selected = true, true
	}

	if !selected {
		return nil
	}

	return []integration.ClientSetup{cs}
}

// plan starts a plan of the given request in the background. The
// review step shows a planning state until the result arrives. An
// empty request plans nothing and shows the inline hint instead.
func (wz *setupWizard) plan(setups []integration.ClientSetup) tea.Cmd {
	if len(setups) == 0 {
		wz.hint = setupNothingSelected

		return nil
	}

	seq := wz.seq.start()

	wz.step = setupStepPreview
	wz.preparing, wz.preview, wz.planErr, wz.setups = true, nil, nil, setups
	wz.paths = ui.ListState{}
	wz.pathOpen = false
	wz.planOff = 0
	wz.pager = ui.TextPager{}
	wz.aboutOpen = false
	wz.hint = ""

	var svc services.Setup = wz.st.svc

	return func() tea.Msg {
		p, err := svc.PlanSetup(setups)

		return setupPlannedMsg{owner: wz, seq: seq, preview: p, err: err}
	}
}

// key routes a key press to the current step.
func (wz *setupWizard) key(msg tea.KeyMsg) tea.Cmd {
	switch wz.step {
	case setupStepClient:
		return wz.clientKey(msg)
	case setupStepChoices:
		return wz.choicesKey(msg)
	case setupStepPreview:
		return wz.previewKey(msg)
	case setupStepResult:
		return wz.resultKey(msg)
	}

	// The apply step takes no keys: the write cannot stop safely.
	return nil
}

func (wz *setupWizard) clientKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, ui.Nav.Up):
		wz.pick.Move(-1, len(wz.clients))
	case key.Matches(msg, ui.Nav.Down):
		wz.pick.Move(1, len(wz.clients))
	case key.Matches(msg, ui.Nav.Enter):
		if len(wz.clients) > 0 {
			wz.pick.Clamp(len(wz.clients), 0)

			// Explicit choices belong to one client; another client
			// starts from its own defaults.
			if next := wz.clients[wz.pick.Cursor]; next.ID != wz.client.ID {
				wz.client = next
				wz.choice = map[setupIntent]bool{}
				wz.rows = ui.ListState{}
				wz.hint = ""
			}

			wz.step = setupStepChoices
		}
	case key.Matches(msg, ui.Nav.Back):
		wz.closed = true
	}

	return nil
}

func (wz *setupWizard) choicesKey(msg tea.KeyMsg) tea.Cmd {
	if wz.aboutOpen {
		return wz.aboutKey(msg)
	}

	rows := wz.currentRows()

	switch {
	case key.Matches(msg, ui.Nav.Up):
		wz.rows.Move(-1, len(rows))
	case key.Matches(msg, ui.Nav.Down):
		wz.rows.Move(1, len(rows))
	case key.Matches(msg, setupKeys.Toggle):
		if len(rows) == 0 {
			return nil
		}

		wz.rows.Clamp(len(rows), 0)
		r := rows[wz.rows.Cursor]

		if !r.selectable() {
			return toast(ui.ToneNeutral, wz.lockedReason(r))
		}

		wz.choice[r.intent] = !wz.checked(r)
		wz.hint = ""
	case key.Matches(msg, setupKeys.About):
		wz.aboutOpen = len(rows) > 0
	case key.Matches(msg, setupKeys.Continue):
		if rows == nil {
			return nil
		}

		return wz.plan(wz.request())
	case key.Matches(msg, ui.Nav.Back):
		if wz.direct {
			wz.closed = true

			return nil
		}

		wz.step = setupStepClient
	}

	return nil
}

// aboutKey handles the keys while the detail box of the selected
// addition is open. The box only reads, so it takes no action key.
func (wz *setupWizard) aboutKey(msg tea.KeyMsg) tea.Cmd {
	if key.Matches(msg, setupKeys.About, ui.Nav.Back) {
		wz.aboutOpen = false

		return nil
	}

	wz.about.Key(msg)

	return nil
}

// lockedReason explains why a locked row cannot change.
func (wz *setupWizard) lockedReason(r setupRow) string {
	if r.kind == setupRowUnsupported {
		return setupIntentLabels[r.intent] + " is not supported by " + ui.OneLine(wz.client.Name) + "."
	}

	return setupIntentLabels[r.intent] + " is already configured. Setup keeps it; it never uninstalls."
}

// keys returns the footer bindings of the current step.
func (wz *setupWizard) keys() []key.Binding {
	switch wz.step {
	case setupStepClient:
		return []key.Binding{ui.WithHelp(ui.Nav.Down, "↑↓", "move"), ui.WithHelp(ui.Nav.Enter, "enter", "choose"),
			ui.WithHelp(ui.Nav.Back, "esc", "close")}
	case setupStepChoices:
		if wz.aboutOpen {
			return []key.Binding{ui.WithHelp(ui.Nav.Down, "↑↓", "scroll"), ui.WithHelp(ui.Nav.Back, "esc", "choices")}
		}

		back := ui.WithHelp(ui.Nav.Back, "esc", "back")
		if wz.direct {
			back = ui.WithHelp(ui.Nav.Back, "esc", "close")
		}

		return []key.Binding{ui.WithHelp(ui.Nav.Down, "↑↓", "move"), setupKeys.Toggle, setupKeys.Continue,
			setupKeys.About, back}
	case setupStepPreview:
		return wz.previewKeys()
	case setupStepResult:
		plan := setupKeys.Plan
		plan.SetEnabled(wz.result != nil && wz.result.err != nil)

		return []key.Binding{ui.WithHelp(ui.Nav.Down, "↑↓", "scroll"), setupKeys.Done, plan}
	}

	return nil
}

// stepIndex returns the position of the current step in the step
// trail. A successful apply marks every step done.
func (wz *setupWizard) stepIndex() int {
	switch wz.step {
	case setupStepClient:
		return 0
	case setupStepChoices:
		return 1
	case setupStepPreview:
		return 2
	case setupStepResult:
		// A run without a reported operation proves nothing, so only a
		// run with operations that all landed marks the last step done.
		if o := wz.result; o != nil && setupSucceeded(o) && len(o.res.Ops)+len(o.res.Skills) > 0 {
			return len(setupStepNames)
		}
	}

	return 3
}

// title names the wizard for the notice row.
func (wz *setupWizard) title() string {
	name := ui.OneLine(wz.client.Name)

	switch {
	case wz.forceSkills:
		return "Refresh managed skills " + wz.st.theme.Glyphs.Sep + " " + name
	case wz.client.ID != "":
		return "Set up " + name
	default:
		return "Set up an integration"
	}
}

// banner renders the wizard title at the left of the notice row and the
// step trail at the right. A narrow row keeps only the current step, so
// the title stays readable.
func (wz *setupWizard) banner(w int) string {
	t := wz.st.theme
	g := t.Glyphs
	current := wz.stepIndex()
	left := " " + t.Title.Render(wz.title())

	trail := stepper(t, setupStepNames, current)
	if ui.Width(left)+ui.Width(trail)+3 > w {
		if current < len(setupStepNames) {
			trail = t.TabActive.Render(g.Dot+" "+setupStepNames[current]) +
				t.Faint.Render(" "+strconv.Itoa(current+1)+"/"+strconv.Itoa(len(setupStepNames)))
		} else {
			trail = t.Success.Render(g.OK + " " + setupStepNames[len(setupStepNames)-1])
		}
	}

	if ui.Width(left)+ui.Width(trail)+3 > w {
		return ui.Truncate(left, w, g.Ellipsis)
	}

	return ui.Spread(left, trail+" ", w)
}

// view renders the current step under a blank line, as in the 1a
// design. The boxes leave the last row blank above the footer.
func (wz *setupWizard) view(w, h int) string {
	boxH := max(3, h-2)

	var body string

	switch wz.step {
	case setupStepClient:
		body = wz.clientView(w, boxH)
	case setupStepChoices:
		body = wz.choicesView(w, boxH)
	case setupStepPreview:
		body = wz.previewView(w, boxH)
	case setupStepCommitting:
		body = wz.committingView(w, boxH)
	case setupStepResult:
		body = wz.resultView(w, boxH)
	}

	return "\n" + body
}

// setupClip keeps the lines that fit in h lines. A longer list gives
// its last visible line to a note that names the details key, so a cut
// explanation always shows the way to the full text.
func setupClip(t *ui.Theme, lines []string, w, h int) []string {
	if len(lines) <= h {
		return lines
	}

	if h <= 0 {
		return nil
	}

	note := ui.Truncate(t.Muted.Render(t.Glyphs.Ellipsis+" press ")+t.Key.Render("i")+
		t.Muted.Render(" to read the full text"), w, t.Glyphs.Ellipsis)

	return append(slices.Clone(lines[:h-1]), note)
}

// setupHookWords names the hooks setup installs for a client.
func setupHookWords(c integration.Client) string {
	var names []string

	for _, capability := range setupHookCaps(c) {
		names = append(names, setupHookNames[capability])
	}

	if len(names) == 0 {
		return "lifecycle"
	}

	return setupJoinWords(names)
}

// setupJoinWords joins words as "a", "a and b" or "a, b and c".
func setupJoinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	default:
		return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
	}
}

// setupSharedNote names the other consumers of the client's skill
// directories, so a refresh of a shared directory is no surprise.
func setupSharedNote(clients []integration.Client, c integration.Client) string {
	var b strings.Builder

	for _, d := range integration.SkillDestinations(clients) {
		if len(d.Consumers) < 2 || !slices.Contains(c.SkillDirs, d.Dir) {
			continue
		}

		b.WriteString(ui.OneLine(d.Dir) + " is shared by " + integConsumers(clients, d.Consumers) + ". ")
	}

	return b.String()
}
