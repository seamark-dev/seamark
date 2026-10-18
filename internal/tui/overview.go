package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovFocus is the focused pane of the wide layout.
type ovFocus int

const (
	ovFocusList ovFocus = iota
	ovFocusDetail
)

// ovCardsMinBody is the smallest body height that shows the summary
// cards: five card rows, a blank row, an attention box of ten rows and
// the blank row above the footer, plus two rows of margin. A shorter
// body shows the one-line summary instead, so the list keeps its rows.
const ovCardsMinBody = 19

// ovFar is a move larger than any list or detail. Home and end use it,
// because a detail can be longer than the list that selects it.
const ovFar = 1 << 20

// ovIntentBuild is the open intent that shows the build confirmation at
// once. The first-run flow uses it for its "Build the index" step.
const ovIntentBuild = "build"

// ovKeys are the Overview action keys.
var ovKeys = struct {
	Build, Force, SetUp key.Binding
}{
	Build: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "build index")),
	Force: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "rebuild from scratch")),
	SetUp: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "set up")),
}

// overview is the Overview destination: the summary cards, the grouped
// attention list with its detail, and the explicit index build.
// Browsing it never writes; only a confirmed build does.
type overview struct {
	st *state

	// list is the attention list position. selKey keeps the selected
	// item by identity, so a refresh never moves the cursor to a
	// different fact.
	list   ui.ListState
	selKey string
	// listH is the last list height, for page moves.
	listH int

	focus ovFocus
	// detailOpen shows the detail instead of the list in the narrow
	// layout.
	detailOpen bool
	detailOff  int

	// confirm shows the build confirmation.
	confirm bool
	// build is the latest index build; showBuild keeps its modal open.
	build     *ovBuild
	showBuild bool
	buildSeq  generation

	// freshSeen records that the user left the empty first-run state
	// for the full list. It lasts for this invocation only.
	freshSeen bool
}

// newOverview builds the Overview over the shared state.
func newOverview(st *state) *overview { return &overview{st: st} }

// title is the destination name in the tab bar.
func (o *overview) title() string { return "Overview" }

// shortTitle is the tab label on a narrow terminal.
func (o *overview) shortTitle() string { return "Over" }

// open accepts the build intent, which opens the build confirmation at
// once. Every other intent opens the Overview normally.
func (o *overview) open(intent string) tea.Cmd {
	if intent == ovIntentBuild {
		return o.openBuild()
	}

	return nil
}

// capturing is true while the build confirmation is open, so the global
// keys cannot fire through it. The progress and result modals do not
// capture: q must reach the shell while a build runs, because the shell
// asks whether to exit after the build.
func (o *overview) capturing() bool { return o.confirm }

// tabCount is the number of attention items beside the tab. It stays
// empty until every source has loaded, because a partial count would
// read as a result.
func (o *overview) tabCount() string {
	n, ok := o.attentionCount()
	if !ok {
		return ""
	}

	return strconv.Itoa(n)
}

// banner returns the stale-index row for the notice row: a full-width
// warn background, the age of the last build, and the rebuild key. No
// commit or file counts appear, because the index records none.
func (o *overview) banner(w int) (string, bool) {
	if !o.indexStale() {
		return "", false
	}

	t := o.st.theme

	msg := "The workspace changed since the last build"
	if at := ovIndexedAt(o.st.index.value); at > 0 {
		msg += " " + ovAge(at, time.Now())
	}

	left := " " + t.Attention.Bold(true).Render(t.Glyphs.Warn) + " " + t.Body.Render("Index is stale.") + " " +
		t.Muted.Render(msg+".")
	right := t.Key.Render("b") + " " + t.Body.Render("rebuild now") + " "

	// The banner cuts the message itself, because spread would cut it
	// with the Unicode ellipsis in the ASCII theme too.
	left = ui.Truncate(left, w-ui.Width(right)-1, t.Glyphs.Ellipsis)

	return t.Highlight(ui.Spread(left, right, w), w, ui.BgWarn), true
}

// indexStale reports a ready index whose semantic health says stale.
func (o *overview) indexStale() bool {
	sec := &o.st.index

	return sec.loaded && sec.value.Probe.State == workspace.IndexReady &&
		sec.value.Status != nil && sec.value.Status.Freshness == status.FreshStale
}

// update handles the build messages and the keys. The snapshots live
// in the shared state, so load results need no handling here.
func (o *overview) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case ovBuildEventMsg:
		return o.onBuildEvent(msg)
	case ovBuildDoneMsg:
		return o.onBuildDone(msg)
	case tea.KeyMsg:
		return o.onKey(msg)
	}

	return nil
}

// collapsed reports the layout where the cards collapse into one line:
// a narrow terminal, or a wide one too short for the cards.
func (o *overview) collapsed() bool {
	return !o.st.wide() || o.st.height-chromeHeight < ovCardsMinBody
}

// indexNewer reports an index database from a newer seamark.
func (o *overview) indexNewer() bool {
	return o.st.index.loaded && o.st.index.value.Probe.State == workspace.IndexNewer
}

// freshEmpty reports the empty first-run state: no index and no client
// with any Seamark configuration.
func (o *overview) freshEmpty() bool {
	if o.freshSeen || !o.st.index.loaded || !o.st.integrations.loaded {
		return false
	}

	if o.st.index.value.Probe.State != workspace.IndexAbsent {
		return false
	}

	return o.nothingSetUp()
}

// nothingSetUp reports that no registered client has any Seamark
// configuration, so the setup key is the natural next step.
func (o *overview) nothingSetUp() bool {
	sec := &o.st.integrations
	if !sec.loaded {
		return false
	}

	for _, insp := range sec.value {
		if !ovNothingConfigured(insp) {
			return false
		}
	}

	return true
}

// onKey routes a key to the open modal first, then to the empty
// first-run state, then to the list.
func (o *overview) onKey(msg tea.KeyMsg) tea.Cmd {
	if o.confirm {
		return o.confirmKey(msg)
	}

	if o.build != nil && o.showBuild {
		// Enter and esc both close a finished result, the same keys as
		// the setup and proposal results. A running build ignores them;
		// b then only explains that the build still runs.
		switch {
		case key.Matches(msg, ovKeys.Build):
			return o.openBuild()
		case key.Matches(msg, ui.Nav.Enter, ui.Nav.Back) && !o.build.running:
			o.showBuild = false
		}

		return nil
	}

	if o.freshEmpty() {
		switch {
		case key.Matches(msg, ovKeys.SetUp):
			return navigate(destIntegrations, "setup")
		case key.Matches(msg, ovKeys.Build):
			return o.openBuild()
		case key.Matches(msg, ui.Nav.Enter):
			o.freshSeen = true
		}

		return nil
	}

	return o.listKey(msg)
}

// listKey handles the keys of the attention list and its detail.
func (o *overview) listKey(msg tea.KeyMsg) tea.Cmd {
	items := o.attention()
	rows := o.rows(items)
	sel := o.sync(rows, items)
	wide := o.st.wide()
	inDetail := (wide && o.focus == ovFocusDetail) || (!wide && o.detailOpen)

	page := max(1, o.listH-1)

	switch {
	case key.Matches(msg, ovKeys.Build):
		return o.openBuild()
	case key.Matches(msg, ovKeys.SetUp):
		if o.nothingSetUp() {
			return navigate(destIntegrations, "setup")
		}
	case key.Matches(msg, ui.Nav.Next, ui.Nav.Prev):
		if wide {
			o.focus = 1 - o.focus
		}
	case key.Matches(msg, ui.Nav.Back):
		switch {
		case !wide && o.detailOpen:
			o.detailOpen = false
		case wide && o.focus == ovFocusDetail:
			o.focus = ovFocusList
		}
	case key.Matches(msg, ui.Nav.Enter):
		return o.activate(items, sel, wide)
	case key.Matches(msg, ui.Nav.Up):
		o.step(inDetail, -1, rows, items)
	case key.Matches(msg, ui.Nav.Down):
		o.step(inDetail, 1, rows, items)
	case key.Matches(msg, ui.Nav.PageUp):
		o.step(inDetail, -page, rows, items)
	case key.Matches(msg, ui.Nav.PageDown):
		o.step(inDetail, page, rows, items)
	case key.Matches(msg, ui.Nav.Top):
		o.step(inDetail, -ovFar, rows, items)
	case key.Matches(msg, ui.Nav.Bottom):
		o.step(inDetail, ovFar, rows, items)
	}

	return nil
}

// step scrolls the detail or moves the list cursor.
func (o *overview) step(inDetail bool, delta int, rows []ovRow, items []ovItem) {
	if inDetail {
		o.detailOff = max(0, o.detailOff+delta)

		return
	}

	o.move(delta, rows, items)
}

// activate runs the enter key: in the narrow list it opens the detail;
// otherwise it starts the item's workflow, or focuses the detail when
// no in-app workflow exists.
func (o *overview) activate(items []ovItem, sel int, wide bool) tea.Cmd {
	if sel < 0 {
		return nil
	}

	if !wide && !o.detailOpen {
		o.detailOpen, o.detailOff = true, 0

		return nil
	}

	it := items[sel]

	switch it.act.kind {
	case ovActNavigate:
		return navigate(it.act.dest, it.act.intent)
	case ovActBuild:
		return o.openBuild()
	}

	if wide {
		o.focus = ovFocusDetail
	}

	return nil
}

// confirmKey handles the build confirmation. A build writes only
// derived data, so enter may confirm it; esc cancels without a change.
func (o *overview) confirmKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, ui.Nav.Enter), msg.String() == "y":
		return o.startBuild(false)
	case key.Matches(msg, ovKeys.Force):
		return o.startBuild(true)
	case key.Matches(msg, ui.Nav.Back), msg.String() == "n":
		o.confirm = false
	}

	return nil
}

// sync places the cursor on the selected item and returns its index,
// or -1. Without a selection it picks the first item after the summary,
// so the detail opens on the most urgent fact. When the selected item
// is gone after a refresh, the cursor stays at the same row position.
func (o *overview) sync(rows []ovRow, items []ovItem) int {
	if o.selKey != "" {
		for i, r := range rows {
			if r.kind == ovRowItem && items[r.item].key == o.selKey {
				o.list.Cursor = i

				return r.item
			}
		}
	} else {
		for i, r := range rows {
			if r.kind == ovRowItem && items[r.item].group != ovSummary {
				return o.selectRow(i, rows, items)
			}
		}
	}

	c := max(0, min(o.list.Cursor, len(rows)-1))

	for i := c; i < len(rows); i++ {
		if rows[i].kind == ovRowItem {
			return o.selectRow(i, rows, items)
		}
	}

	for i := c; i >= 0 && i < len(rows); i-- {
		if rows[i].kind == ovRowItem {
			return o.selectRow(i, rows, items)
		}
	}

	o.selKey = ""

	return -1
}

// selectRow puts the cursor on row i and records the item identity.
func (o *overview) selectRow(i int, rows []ovRow, items []ovItem) int {
	o.list.Cursor = i
	o.selKey = items[rows[i].item].key

	return rows[i].item
}

// move shifts the selection by delta selectable rows, skipping the
// headings, the notes and the client and doctor lines.
func (o *overview) move(delta int, rows []ovRow, items []ovItem) {
	if o.sync(rows, items) < 0 || delta == 0 {
		return
	}

	dir, left := 1, delta

	if delta < 0 {
		dir, left = -1, -delta
	}

	last := o.list.Cursor

	for i := o.list.Cursor + dir; i >= 0 && i < len(rows) && left > 0; i += dir {
		if rows[i].kind == ovRowItem {
			last = i
			left--
		}
	}

	if last != o.list.Cursor {
		o.selectRow(last, rows, items)
		o.detailOff = 0
	}
}

// pendingSources names the attention sources that have not loaded
// yet. Every source of an attention item is here, because the tab count
// and the summary item wait for all of them. The doctor line states its
// own loading state.
func (o *overview) pendingSources() []string {
	var names []string

	if !o.st.index.loaded && o.st.index.err == nil {
		names = append(names, "reading the index")
	}

	if !o.st.integrations.loaded && o.st.integrations.err == nil {
		names = append(names, "inspecting the integrations")
	}

	// The learning items read the ledger, the stored evidence, and the
	// settings. Each source loads in parallel with the others.
	if !o.st.ledger.loaded && o.st.ledger.err == nil {
		names = append(names, "reading the decision ledger")
	}

	if !o.st.learning.loaded && o.st.learning.err == nil {
		names = append(names, "reading the evidence store")
	}

	if !o.st.settings.loaded && o.st.settings.err == nil {
		names = append(names, "reading the settings")
	}

	return names
}

// moveLabel names the move keys in the glyph set of the theme, so the
// ASCII theme shows no arrow glyphs.
func (o *overview) moveLabel() string {
	if o.st.theme.ASCII {
		return "j/k"
	}

	return "↑↓"
}

// ovSentence capitalizes the first letter of an action label.
func ovSentence(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

// keys returns the contextual bindings. They follow the visible state,
// so the footer never offers a key that does nothing.
func (o *overview) keys() []key.Binding {
	switch {
	case o.confirm:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "build"), ovKeys.Force, ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	case o.build != nil && o.showBuild && o.build.running:
		return nil
	case o.build != nil && o.showBuild:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "close"), ui.WithHelp(ovKeys.Build, "b", "build again")}
	case o.freshEmpty():
		return []key.Binding{ui.WithHelp(ovKeys.SetUp, "s", "set up"), ovKeys.Build, ui.WithHelp(ui.Nav.Enter, "enter", "show list")}
	}

	build := ovKeys.Build
	build.SetEnabled(!o.indexNewer())

	setUp := ovKeys.SetUp
	setUp.SetEnabled(o.nothingSetUp())

	move := o.moveLabel()

	if !o.st.wide() {
		if o.detailOpen {
			// esc comes first: it is the way back to the list, and a
			// narrow footer drops the last hints first.
			out := []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "back"), ui.WithHelp(ui.Nav.Up, move, "scroll")}

			items := o.attention()
			if sel := o.sync(o.rows(items), items); sel >= 0 && items[sel].act.kind != ovActNone {
				out = append(out, ui.WithHelp(ui.Nav.Enter, "enter", items[sel].act.label))
			}

			return out
		}

		build.SetHelp("b", "build")

		return []key.Binding{ui.WithHelp(ui.Nav.Up, move, "move"), ui.WithHelp(ui.Nav.Enter, "enter", "detail"), build, setUp}
	}

	if o.focus == ovFocusDetail {
		return []key.Binding{
			ui.WithHelp(ui.Nav.Up, move, "scroll"), ui.WithHelp(ui.Nav.Next, "tab", "pane"),
			ui.WithHelp(ui.Nav.Back, "esc", "back"), build, setUp,
		}
	}

	return []key.Binding{
		ui.WithHelp(ui.Nav.Up, move, "move"), ui.WithHelp(ui.Nav.Enter, "enter", "open"),
		ui.WithHelp(ui.Nav.Next, "tab", "pane"), build, setUp,
	}
}
