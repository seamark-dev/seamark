package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// doctorView is the Doctor destination: one row per check of `seamark
// doctor` and the detail of the selected check. It reads the shared
// doctor snapshot only. It shows each fix as text and never runs it,
// because a fix comes from diagnostics that can quote repository data.
// A check with a known workflow opens it with enter, through the same
// mapping as the Overview attention list.
type doctorView struct {
	st *state

	list listState
	// detailOff is the first visible line of the detail pane. It goes
	// back to the top when the selection moves.
	detailOff int
	// focusDetail gives the movement keys to the detail pane in the wide
	// layout.
	focusDetail bool
	// detailShown shows the detail instead of the list in the narrow layout.
	detailShown bool
	// want is a check name from a "check:<name>" intent. The selection
	// moves to it when the report is available.
	want string
}

// docKeys are the Doctor bindings. The help text follows the 1a footer.
var docKeys = struct {
	Move, Open, Pane, Scroll, Page, Back key.Binding
}{
	Move:   withHelp(nkeys.Down, "↑↓", "move"),
	Open:   withHelp(nkeys.Enter, "enter", "open"),
	Pane:   withHelp(nkeys.Next, "tab", "pane"),
	Scroll: withHelp(nkeys.Down, "↑↓", "scroll"),
	Page:   withHelp(nkeys.PageDown, "pgup pgdn", "scroll detail"),
	Back:   withHelp(nkeys.Back, "esc", "back"),
}

// docStateW is the width of the state column: "! warn" and one space.
// docNameGap separates the name column from the detail, so a short name
// does not read as the first word of its detail.
const (
	docStateW  = 7
	docNameGap = 2
)

// docMeaning explains each doctor state in the words of the doctor
// package, so the console and `seamark doctor` agree.
var docMeaning = map[string]string{
	doctor.StateOK:   "seamark can use this",
	doctor.StateInfo: "a fact that needs no action",
	doctor.StateWarn: "degrades a feature",
	doctor.StateFail: "seamark cannot do its job",
}

func newDoctor(st *state) *doctorView { return &doctorView{st: st} }

func (d *doctorView) title() string { return "Doctor" }

// open accepts "check:<name>" to select one check, for example from an
// Overview item. Any other intent opens the view as it was.
func (d *doctorView) open(intent string) tea.Cmd {
	if name, ok := strings.CutPrefix(intent, "check:"); ok && name != "" {
		d.want = name
		d.selectWanted()
	}

	return nil
}

// capturing is always false: the view has no text input and no overlay.
func (d *doctorView) capturing() bool { return false }

// tabCount returns the count of failing and warning checks. A healthy
// or unknown report shows no count, because a zero would read as a
// verified result.
func (d *doctorView) tabCount() string {
	r := d.report()
	if r == nil || r.Fails+r.Warns == 0 {
		return ""
	}

	return itoa(r.Fails + r.Warns)
}

// report returns the loaded doctor report, or nil when none is known.
func (d *doctorView) report() *doctor.Report {
	if !d.st.doctor.loaded {
		return nil
	}

	return d.st.doctor.value
}

// checks returns the checks of the loaded report.
func (d *doctorView) checks() []doctor.Check {
	if r := d.report(); r != nil {
		return r.Checks
	}

	return nil
}

// narrow reports the single-pane layout of the current terminal.
func (d *doctorView) narrow() bool {
	return d.st.width > 0 && !d.st.wide()
}

// detailActive reports that the movement keys scroll the detail.
func (d *doctorView) detailActive() bool {
	if d.narrow() {
		return d.detailShown
	}

	return d.focusDetail
}

// selectWanted moves the selection to the wanted check once the report
// holds it. The name is a stable doctor identifier.
func (d *doctorView) selectWanted() {
	if d.want == "" {
		return
	}

	for i, c := range d.checks() {
		if c.Name == d.want {
			d.list.cursor = i
			d.detailOff = 0
			d.want = ""

			return
		}
	}
}

func (d *doctorView) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadedMsg:
		if msg.kind == secDoctor {
			d.selectWanted()
		}
	case tea.KeyMsg:
		return d.key(msg)
	}

	return nil
}

// key moves the selection, the focus or the detail scroll, or opens the
// workflow of the selected check. No key of this view calls a service:
// a workflow opens in its own destination, which previews any change.
func (d *doctorView) key(msg tea.KeyMsg) tea.Cmd {
	n := len(d.checks())
	if n == 0 {
		return nil
	}

	switch {
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		if !d.narrow() {
			d.focusDetail = !d.focusDetail
		}
	case key.Matches(msg, nkeys.Enter):
		return d.enter()
	case key.Matches(msg, nkeys.Back):
		d.detailShown = false
		d.focusDetail = false
	case key.Matches(msg, nkeys.PageDown):
		d.detailOff += 10
	case key.Matches(msg, nkeys.PageUp):
		d.detailOff = max(0, d.detailOff-10)
	case key.Matches(msg, nkeys.Up):
		if d.detailActive() {
			d.detailOff = max(0, d.detailOff-1)
		} else {
			d.moveTo(d.list.cursor - 1)
		}
	case key.Matches(msg, nkeys.Down):
		if d.detailActive() {
			d.detailOff++
		} else {
			d.moveTo(d.list.cursor + 1)
		}
	case key.Matches(msg, nkeys.Top):
		d.moveTo(0)
	case key.Matches(msg, nkeys.Bottom):
		d.moveTo(n - 1)
	}

	return nil
}

// enter follows the Overview rule. In the narrow list it shows the
// detail. Otherwise it opens the workflow of the selected check, or
// focuses the detail when no workflow exists.
func (d *doctorView) enter() tea.Cmd {
	if d.narrow() && !d.detailShown {
		d.detailShown = true

		return nil
	}

	if act := d.action(); act.kind != ovActNone {
		return docOpenAction(act)
	}

	if !d.narrow() {
		d.focusDetail = true
	}

	return nil
}

// selected returns the selected check; ok is false without a check.
func (d *doctorView) selected() (doctor.Check, bool) {
	checks := d.checks()
	if len(checks) == 0 {
		return doctor.Check{}, false
	}

	return checks[max(0, min(d.list.cursor, len(checks)-1))], true
}

// action returns the workflow of the selected check, or no action.
func (d *doctorView) action() ovAction {
	c, ok := d.selected()
	if !ok {
		return ovAction{}
	}

	return checkAction(d.st, c)
}

// docOpenAction opens the workflow of an action. The build confirmation
// belongs to the Overview, so a build action opens it there; the build
// still waits for the confirmation.
func docOpenAction(act ovAction) tea.Cmd {
	switch act.kind {
	case ovActNavigate:
		return navigate(act.dest, act.intent)
	case ovActBuild:
		return navigate(destOverview, ovIntentBuild)
	default:
		return nil
	}
}

// checkAction maps a doctor check to a known in-app workflow. The
// Overview attention list and the Doctor tab share it, so a check opens
// the same workflow from both. The check name is a stable identifier;
// the detail text is never read, because diagnostic wording is not a
// contract. An ok check needs no workflow.
func checkAction(st *state, c doctor.Check) ovAction {
	if c.State == doctor.StateOK {
		return ovAction{}
	}

	switch c.Name {
	case "index":
		// Seamark never rebuilds a newer database, so the check offers
		// no build.
		if st.index.loaded && st.index.value.Probe.State == workspace.IndexNewer {
			return ovAction{}
		}

		return ovBuildAction
	case "hooks", "mcp", "approvals":
		return ovNav(destIntegrations, "setup", "open setup")
	case "skills":
		return ovNav(destIntegrations, "", "open Integrations")
	case "agent":
		// The check warns about the agent section of config.yaml, an
		// unresolved preset, or a missing executable. The inference group
		// edits all three causes.
		return ovSettingsLink(settings.GroupInference)
	case "gh":
		// gh serves only the review fetch. Evidence shows the gh state and
		// also offers the local-fix refresh, which needs no gh.
		return ovEvidenceLink("open Evidence")
	}

	for _, insp := range st.integrations.value {
		if insp.ClientID == c.Name {
			return ovNav(destIntegrations, "client:"+c.Name, "inspect "+ovClientName(insp))
		}
	}

	return ovAction{}
}

// moveTo selects row i and shows its detail from the top.
func (d *doctorView) moveTo(i int) {
	n := len(d.checks())
	i = max(0, min(i, n-1))

	if i != d.list.cursor {
		d.detailOff = 0
	}

	d.list.cursor = i
}

// keys returns the bindings of the visible state, so the footer never
// offers a key that does nothing.
func (d *doctorView) keys() []key.Binding {
	if len(d.checks()) == 0 {
		return nil
	}

	// The enter hint names the workflow only when one exists. Without
	// one, enter keeps its detail role, so the footer never promises a
	// workflow that does not exist.
	act := d.action()
	open := docKeys.Open

	if act.kind != ovActNone {
		open = withHelp(nkeys.Enter, "enter", act.label)
	}

	switch {
	case d.narrow() && d.detailShown:
		// esc comes first: at the minimum width the footer has room for
		// one hint, and esc is the only way back to the list.
		if act.kind != ovActNone {
			return []key.Binding{docKeys.Back, open, docKeys.Scroll}
		}

		return []key.Binding{docKeys.Back, docKeys.Scroll}
	case d.narrow():
		return []key.Binding{docKeys.Move, docKeys.Open}
	case d.focusDetail:
		if act.kind != ovActNone {
			return []key.Binding{docKeys.Scroll, open, docKeys.Pane, docKeys.Back}
		}

		return []key.Binding{docKeys.Scroll, docKeys.Pane, docKeys.Back}
	default:
		return []key.Binding{docKeys.Move, open, docKeys.Pane, docKeys.Page}
	}
}

// docListWidth returns the list pane width: 64 cells at 120 columns,
// as in the 1a design. A wider terminal gives the rest to the detail.
func docListWidth(w int) int {
	return min(64, (w*64+60)/120)
}

// view renders the list and the detail. The boxes leave the last row
// blank, as the 1a design keeps one row between a pane and the footer.
func (d *doctorView) view(w, h int) string {
	boxH := max(4, h-1)

	if len(d.checks()) == 0 {
		return d.stateView(w, boxH)
	}

	if w < wideWidth {
		if d.detailShown {
			return d.detailBox(w, boxH, true)
		}

		return d.listBox(w, boxH, true)
	}

	lw := docListWidth(w)

	return hjoin(1, d.listBox(lw, boxH, !d.focusDetail), d.detailBox(w-lw-1, boxH, d.focusDetail))
}

// stateView renders the loading, error and unknown states in one box.
// A missing or empty report is unknown, never healthy.
func (d *doctorView) stateView(w, h int) string {
	t := d.st.theme
	sec := &d.st.doctor
	inner := boxInner(w)
	rows := boxRows(h)

	next := t.Faint.Render("same as ") + t.command("seamark doctor")

	var body string

	switch {
	case !sec.loaded && sec.err != nil:
		body = t.empty(ToneFailure, "Could not run the doctor checks", errText(sec.err),
			t.hints([]key.Binding{withHelp(gkeys.Refresh, "r", "run the checks again")})+"\n"+next, inner, rows)
	case !sec.loaded:
		body = t.empty(ToneNeutral, "Running the doctor checks"+t.Glyphs.Ellipsis,
			"The checks read the binary, git, the index, the policy, the hooks and the agent tools. "+
				"They change nothing.", next, inner, rows)
	case sec.value == nil:
		body = t.empty(ToneUnknown, "No doctor report",
			"The doctor checks returned no report, so the state of the installation is unknown.",
			t.hints([]key.Binding{withHelp(gkeys.Refresh, "r", "run the checks again")})+"\n"+next, inner, rows)
	default:
		body = t.empty(ToneUnknown, "No doctor checks",
			"The doctor report lists no check. The state of the installation is unknown, not healthy.",
			t.hints([]key.Binding{withHelp(gkeys.Refresh, "r", "run the checks again")})+"\n"+next, inner, rows)
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: true, Lines: strings.Split(body, "\n"),
		Title: t.Title.Render("Doctor checks"),
	})
}

// docBadge renders a doctor state as a glyph and a word, so the state
// reads without color: "✓ ok", "· info", "! warn", "! fail".
func docBadge(t *Theme, state string) string {
	g := t.Glyphs

	switch state {
	case doctor.StateOK:
		return t.Success.Render(g.OK + " ok")
	case doctor.StateInfo:
		return t.Info.Render(g.Info + " info")
	case doctor.StateWarn:
		return t.Attention.Render(g.Warn + " warn")
	case doctor.StateFail:
		return t.Failure.Render(g.Fail + " fail")
	default:
		return t.Unknown.Render(truncate(g.Unknown+" "+oneLine(state), docStateW-1, g.Ellipsis))
	}
}

// summary renders the count of each state, for example
// "✓ 13 ok   · 2 info   ! 0 warn   ! 0 fail". A zero count is faint.
func (d *doctorView) summary() string {
	t := d.st.theme
	g := t.Glyphs

	counts := map[string]int{}
	for _, c := range d.checks() {
		counts[c.State]++
	}

	part := func(state, glyph string, style lipgloss.Style) string {
		text := glyph + " " + itoa(counts[state]) + " " + state
		if counts[state] == 0 {
			return t.Faint.Render(text)
		}

		return style.Render(text)
	}

	return strings.Join([]string{
		part(doctor.StateOK, g.OK, t.Success), part(doctor.StateInfo, g.Info, t.Info),
		part(doctor.StateWarn, g.Warn, t.Attention), part(doctor.StateFail, g.Fail, t.Failure),
	}, "   ")
}

// listBox renders the list pane: an optional stale note, the state
// counts, then one row per check.
func (d *doctorView) listBox(w, h int, focus bool) string {
	t := d.st.theme
	g := t.Glyphs
	checks := d.checks()
	inner := boxInner(w)
	rows := boxRows(h)

	var lines []string

	if sec := &d.st.doctor; sec.stale() {
		lines = append(lines, t.Attention.Render(truncate(g.Warn+" stale: "+errText(sec.err)+" "+g.Sep+" r runs the checks again",
			inner, g.Ellipsis)))
	}

	lines = append(lines, d.summary(), "")

	window := max(1, rows-len(lines))
	d.list.clamp(len(checks), window)

	nameW := 6
	for _, c := range checks {
		nameW = max(nameW, width(oneLine(c.Name)))
	}

	nameW = min(nameW, max(6, inner/3))

	end := min(len(checks), d.list.offset+window)
	for i := d.list.offset; i < end; i++ {
		lines = append(lines, d.row(checks[i], nameW, inner, i == d.list.cursor, focus))
	}

	foot := ""

	switch {
	case len(checks) > window:
		foot = propRange(t, d.list.offset, end, len(checks))
	case d.narrow():
		foot = t.Faint.Render("enter for detail")
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focus, Lines: lines, Foot: foot,
		Title: t.Title.Render("Doctor checks"),
		Right: t.Muted.Render(itoa(len(checks)) + " " + docPlural(len(checks), "check", "checks")),
	})
}

// row renders one check: the cursor, the state, the name and the
// detail clipped to the pane. The detail pane shows the full text.
func (d *doctorView) row(c doctor.Check, nameW, inner int, selected, focus bool) string {
	t := d.st.theme
	g := t.Glyphs

	cursor := "  "
	name := t.Body

	if selected {
		cursor = t.Key.Render(g.Cursor) + " "
		name = t.Title
	}

	rest := max(1, inner-2-docStateW-nameW-docNameGap)

	row := t.cols(cell(cursor, 2), cell(docBadge(t, c.State), docStateW), cell(name.Render(oneLine(c.Name)), nameW+docNameGap)) +
		t.Muted.Render(truncate(diag(c.Detail), rest, g.Ellipsis))

	if selected && focus {
		row = t.highlight(row, inner, bgSel)
	}

	return row
}

// detailLines renders the selected check: the state, the full detail,
// the fix as selectable text, and the equivalent command.
func (d *doctorView) detailLines(c doctor.Check, inner int) []string {
	t := d.st.theme
	g := t.Glyphs

	lines := []string{docBadge(t, c.State) + "  " + t.Faint.Render(docMeaning[c.State]), ""}

	if detail := diag(c.Detail); detail != "" {
		lines = append(lines, strings.Split(t.Body.Render(wrap(detail, inner)), "\n")...)
	} else {
		lines = append(lines, t.Faint.Render("The check reports no detail."))
	}

	// NEXT names the in-app workflow, as in the Overview detail. It
	// shows only when a workflow exists.
	if act := checkAction(d.st, c); act.kind != ovActNone {
		lines = append(lines, "", t.Heading.Render("NEXT"),
			t.Key.Render("enter")+" "+t.Body.Render(act.label))
	}

	lines = append(lines, "", t.Heading.Render("FIX"))

	switch fix := diag(c.Fix); {
	case fix != "":
		for _, l := range strings.Split(wrap(fix, inner), "\n") {
			lines = append(lines, t.command(l))
		}

		lines = append(lines, strings.Split(t.Faint.Render(wrap("Run it in a shell when you choose to. "+
			"The console never runs it.", inner)), "\n")...)
	case c.State == doctor.StateOK || c.State == doctor.StateInfo:
		lines = append(lines, t.Success.Render("No action needed."))
	default:
		lines = append(lines, t.Muted.Render("The check names no fix."))
	}

	return append(lines, "", t.Faint.Render("same as seamark doctor "+g.Sep+" r runs the checks again"))
}

// detailBox renders the detail pane of the selected check. The pane
// scrolls when the detail is longer than the pane.
func (d *doctorView) detailBox(w, h int, focus bool) string {
	t := d.st.theme
	g := t.Glyphs
	checks := d.checks()
	inner := boxInner(w)
	rows := boxRows(h)

	c := checks[max(0, min(d.list.cursor, len(checks)-1))]
	lines := d.detailLines(c, inner)

	last := max(0, len(lines)-rows)
	d.detailOff = max(0, min(d.detailOff, last))

	foot := ""

	switch {
	case d.detailOff < last:
		foot = t.Faint.Render("↓ more " + g.Sep + " pgdn")
	case last > 0:
		foot = t.Faint.Render("↑ pgup")
	}

	title := t.Muted.Render(oneLine(c.Name))
	if focus {
		title = t.Title.Render(oneLine(c.Name))
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focus, Lines: lines[d.detailOff:], Foot: foot, Title: title,
	})
}

// docPlural returns one or many by n.
func docPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}
