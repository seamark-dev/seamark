package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the list and detail pane of Proposals and Pins. They use
// a small row type, so they depend on neither the ledger nor the pin
// file. The view tests and the captures cover the views on top.

// paneRow is a row of the pane tests: an identity and a content key.
type paneRow struct {
	id  int
	key string
}

// paneRows returns rows with the identities ids and the content key
// "k<id>".
func paneRows(ids ...int) []paneRow {
	out := make([]paneRow, len(ids))
	for i, id := range ids {
		out[i] = paneRow{id: id, key: fmt.Sprintf("k%d", id)}
	}

	return out
}

// newTestPane returns a pane over paneRow. The pane reads the layout
// from *wide, so a test can change it between two calls.
func newTestPane(wide *bool) *listPane[paneRow, int] {
	return &listPane[paneRow, int]{
		wide:  func() bool { return *wide },
		ident: func(r paneRow) int { return r.id },
	}
}

func TestListPaneReconcileRecordsTheFirstRowOfTheFirstSnapshot(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)

	gone, lost := lp.reconcileCursor(paneRows(4, 5))

	assert.False(t, lost, "nothing was under the cursor, so nothing is lost")
	assert.Zero(t, gone)
	assert.Equal(t, 4, lp.cursorID, "the cursor records the first row")
}

func TestListPaneReconcileFollowsAMovedRow(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)
	lp.list.Cursor = 1
	lp.syncCursorID(paneRows(1, 2, 3))
	require.Equal(t, 2, lp.cursorID)

	gone, lost := lp.reconcileCursor(paneRows(7, 8, 2, 1))

	assert.False(t, lost)
	assert.Equal(t, 2, gone)
	assert.Equal(t, 2, lp.list.Cursor, "the cursor moves with its row")
	assert.Equal(t, 2, lp.cursorID)
}

func TestListPaneReconcileReportsALostRowAndKeepsThePosition(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)
	lp.list.Cursor = 1
	lp.syncCursorID(paneRows(1, 2, 3))

	gone, lost := lp.reconcileCursor(paneRows(1, 3, 4))

	assert.True(t, lost)
	assert.Equal(t, 2, gone, "the note names the lost row")
	assert.Equal(t, 1, lp.list.Cursor, "a refresh keeps the position, which shows the neighbor")
	assert.Equal(t, 3, lp.cursorID, "the cursor records the neighbor")
}

func TestListPaneFindByContentRecordsTheNewIdentity(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)

	// find matches the content key, as pinFind matches a moved pin.
	lp.find = func(rows []paneRow, id int) (int, bool) {
		want := fmt.Sprintf("k%d", id)
		i := slices.IndexFunc(rows, func(r paneRow) bool { return r.key == want })

		return max(i, 0), i >= 0
	}

	lp.cursorID = 2
	rows := []paneRow{{id: 9, key: "k1"}, {id: 10, key: "k2"}}

	gone, lost := lp.reconcileCursor(rows)

	assert.False(t, lost, "the content matches, so the row is the same")
	assert.Equal(t, 2, gone)
	assert.Equal(t, 1, lp.list.Cursor)
	assert.Equal(t, 10, lp.cursorID, "the cursor records the identity of the found row")
}

func TestListPaneRelocateOnAFilterChangeStartsAtTheTop(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)
	lp.list = ui.ListState{Cursor: 2, Offset: 1}
	lp.cursorID = 99

	assert.False(t, lp.relocate(paneRows(1, 2, 3, 4), false))
	assert.Equal(t, 0, lp.list.Cursor)
	assert.Equal(t, 0, lp.list.Offset)
	assert.Equal(t, 1, lp.cursorID)

	lp.cursorID = 99
	assert.False(t, lp.relocate(nil, false))
	assert.Zero(t, lp.cursorID, "an empty list records no row, so a refresh reports no loss again")
}

func TestListPaneShownDefaultsToVisible(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)
	assert.True(t, lp.shown(), "a nil visible means always visible")

	lp.visible = func() bool { return false }
	assert.False(t, lp.shown())
}

func TestListPaneFocusKeys(t *testing.T) {
	rows := paneRows(1, 2)
	wide := false
	lp := newTestPane(&wide)

	assert.True(t, lp.navKey(keyMsg("tab"), rows))
	assert.False(t, lp.focusDetail, "tab changes nothing in the narrow layout")

	wide = true
	lp.navKey(keyMsg("tab"), rows)
	assert.True(t, lp.focusDetail, "tab focuses the detail in the wide layout")

	lp.navKey(keyMsg("shift+tab"), rows)
	assert.False(t, lp.focusDetail, "shift+tab gives the focus back")

	lp.navKey(keyMsg("tab"), nil)
	assert.False(t, lp.focusDetail, "an empty list has no detail to focus")

	lp.navKey(keyMsg("enter"), nil)
	assert.False(t, lp.focusDetail, "enter needs a row")

	lp.navKey(keyMsg("enter"), rows)
	assert.True(t, lp.focusDetail, "enter focuses the detail")

	lp.navKey(keyMsg("esc"), rows)
	assert.False(t, lp.focusDetail, "esc gives the focus back to the list")

	assert.False(t, lp.navKey(keyMsg("x"), rows), "x is no pane key")
}

func TestListPaneMovementKeysMoveTheCursorOrScrollTheDetail(t *testing.T) {
	rows := paneRows(1, 2, 3, 4, 5, 6, 7, 8)
	wide := true
	lp := newTestPane(&wide)
	lp.list.Clamp(len(rows), 3)
	lp.detail.Fit(14, 4)

	lp.navKey(keyMsg("down"), rows)
	assert.Equal(t, 1, lp.list.Cursor, "down moves the cursor while the list has focus")
	assert.Equal(t, 2, lp.cursorID, "a cursor move records the identity")
	assert.Zero(t, lp.detail.Off)

	lp.navKey(keyMsg("pgdown"), rows)
	assert.Equal(t, 4, lp.detail.Off, "pgdn scrolls the unfocused detail in the wide layout")
	assert.Equal(t, 1, lp.list.Cursor)

	lp.focusDetail = true
	lp.navKey(keyMsg("down"), rows)
	assert.Equal(t, 5, lp.detail.Off, "down scrolls the focused detail")
	assert.Equal(t, 1, lp.list.Cursor)

	wide = false
	lp.focusDetail = false
	lp.navKey(keyMsg("pgdown"), rows)
	assert.Equal(t, 5, lp.detail.Off, "the narrow list hides the detail")
	assert.Equal(t, 3, lp.list.Cursor, "pgdn pages the list by window-1 rows")
	assert.Equal(t, 4, lp.cursorID)
}

func TestListPanePaneKeyNamesTheFocusChange(t *testing.T) {
	wide := true
	lp := newTestPane(&wide)

	none := lp.paneKey(false)
	assert.False(t, none.Enabled(), "without a row there is no detail")
	assert.Equal(t, key.Help{Key: "enter", Desc: "detail"}, none.Help())

	assert.Equal(t, key.Help{Key: "tab", Desc: "detail"}, lp.paneKey(true).Help())

	lp.focusDetail = true
	assert.Equal(t, key.Help{Key: "tab", Desc: "list"}, lp.paneKey(true).Help())

	wide = false
	assert.Equal(t, key.Help{Key: "esc", Desc: "back"}, lp.paneKey(true).Help())

	lp.focusDetail = false
	assert.Equal(t, key.Help{Key: "enter", Desc: "detail"}, lp.paneKey(true).Help())
}

func TestListPaneSplitShowsBothPanesOnlyWhenWide(t *testing.T) {
	rows := paneRows(1)
	wide := true
	lp := newTestPane(&wide)

	var calls []string

	box := func(name string) func([]paneRow, int, int, bool) string {
		return func(_ []paneRow, w, h int, focused bool) string {
			calls = append(calls, fmt.Sprintf("%s %dx%d %t", name, w, h, focused))

			return name
		}
	}

	assert.Equal(t, "list detail", lp.split(rows, 100, 20, 40, box("list"), box("detail")))
	assert.Equal(t, []string{"list 40x20 true", "detail 59x20 false"}, calls,
		"the list renders first, with a gap of one cell")

	calls = nil
	lp.focusDetail = true
	lp.split(rows, 100, 20, 40, box("list"), box("detail"))
	assert.Equal(t, []string{"list 40x20 false", "detail 59x20 true"}, calls)

	calls = nil
	wide = false
	assert.Equal(t, "detail", lp.split(rows, 60, 20, 40, box("list"), box("detail")))
	assert.Equal(t, []string{"detail 60x20 true"}, calls, "the narrow layout shows the focused pane alone")

	calls = nil
	lp.focusDetail = false
	assert.Equal(t, "list", lp.split(rows, 60, 20, 40, box("list"), box("detail")))
	assert.Equal(t, []string{"list 60x20 true"}, calls)
}

func TestListPaneRenderListWindowAndFoot(t *testing.T) {
	th := ui.NewTheme(true)
	wide := true
	lp := newTestPane(&wide)
	lp.list.Cursor = 5

	var cursorRows []int

	row := func(r paneRow, cursor bool) string {
		if cursor {
			cursorRows = append(cursorRows, r.id)
		}

		return "row " + strconv.Itoa(r.id)
	}

	rows := paneRows(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	out := ansi.Strip(lp.renderList(th, rows, 40, 6, true, "Items", "HEAD", row))

	// A page key moves by the window less one row, so pgdn from the cursor
	// shows the window that the render recorded.
	paged := lp.list
	paged.Key(keyMsg("pgdown"), len(rows))
	assert.Equal(t, lp.list.Cursor+2, paged.Cursor, "the header takes one of the four box rows")
	assert.Equal(t, 3, lp.list.Offset, "the window keeps the cursor row")
	assert.Equal(t, []int{6}, cursorRows, "only the cursor row renders as the cursor")
	assert.Contains(t, out, "HEAD")
	assert.Contains(t, out, "row 4")
	assert.NotContains(t, out, "row 3", "the rows above the window stay hidden")
	assert.Contains(t, out, "4-6 of 10", "an overflowing list shows its position")
	assert.Contains(t, out, "Items")
	assert.Contains(t, out, "10", "the right label counts the rows")

	short := paneRows(1, 2)
	assert.NotContains(t, ansi.Strip(lp.renderList(th, short, 40, 6, true, "Items", "HEAD", row)),
		"enter for detail", "the wide layout shows the detail beside the list")

	wide = false
	assert.Contains(t, ansi.Strip(lp.renderList(th, short, 40, 6, true, "Items", "HEAD", row)),
		"enter for detail", "the narrow layout says how to open the detail")
}

func TestListPaneRenderListHighlightsOnlyTheFocusedCursorRow(t *testing.T) {
	distWithColors(t)

	th := ui.NewTheme(false)
	// The highlight of an empty row is the background sequence and the
	// reset, so the test finds the sequence without a private helper.
	seq := strings.TrimSuffix(th.Highlight("", 0, ui.BgSel), "\x1b[0m")
	require.NotEmpty(t, seq)

	wide := true
	lp := newTestPane(&wide)
	row := func(r paneRow, _ bool) string { return "row " + strconv.Itoa(r.id) }
	rows := paneRows(1, 2)

	focused := lp.renderList(th, rows, 40, 6, true, "Items", "HEAD", row)
	assert.Equal(t, 1, strings.Count(focused, seq), "the focused box highlights the cursor row")

	unfocused := lp.renderList(th, rows, 40, 6, false, "Items", "HEAD", row)
	assert.NotContains(t, unfocused, seq, "the unfocused box shows the cursor glyph only")
}

func TestListPaneRenderDetailScrollsAndResetsForAnotherRow(t *testing.T) {
	th := ui.NewTheme(false)
	wide := true
	lp := newTestPane(&wide)

	body := make([]string, 12)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i)
	}

	out := ansi.Strip(lp.renderDetail(th, paneRow{id: 1}, "Item 1", body, 40, 6, true))
	paged := lp.detail
	paged.Key(keyMsg("pgdown"))
	assert.Equal(t, 4, paged.Off, "the detail records its page")
	assert.Equal(t, 8, lp.detail.Last, "the detail records its last offset")
	assert.Contains(t, out, "↓ more · pgdn")
	assert.Contains(t, out, "line 0")

	lp.detail.Off = 8
	out = ansi.Strip(lp.renderDetail(th, paneRow{id: 1}, "Item 1", body, 40, 6, true))
	assert.Contains(t, out, "↑ more · pgup")
	assert.Contains(t, out, "line 11")

	lp.detail.Off = 3
	lp.renderDetail(th, paneRow{id: 2}, "Item 2", body, 40, 6, true)
	assert.Zero(t, lp.detail.Off, "another row starts at the top")
	assert.Equal(t, 2, lp.detailID)

	lp.detail.Off = 3
	lp.renderDetail(th, paneRow{id: 2}, "Item 2", body, 40, 6, true)
	assert.Equal(t, 3, lp.detail.Off, "the same row keeps its offset")
}

func TestFilterInputHintFollowsTheField(t *testing.T) {
	th := ui.NewTheme(true)
	f := filterInput{textHint: "text in the rule"}

	f.startEdit(filterText, "")
	assert.Contains(t, f.input.View(th), "text in the rule")

	f.startEdit(filterRegion, "")
	assert.Contains(t, f.input.View(th), "internal/api", "the region field keeps the hint of lineInput")
}

func TestFilterInputEnterNormalizesTheValue(t *testing.T) {
	cases := []struct {
		field filterField
		typed string
		want  string
	}{
		{filterRegion, "./internal/api/", "internal/api"},
		{filterRegion, "*", ""},
		{filterText, " foo ", "foo"},
		{filterText, "./a/", "./a/"},
	}

	for _, c := range cases {
		var f filterInput

		f.startEdit(c.field, c.typed)

		field, value, done := f.inputKey(keyMsg("enter"))
		assert.True(t, done, "%q", c.typed)
		assert.Equal(t, c.field, field, "%q", c.typed)
		assert.Equal(t, c.want, value, "%q", c.typed)
		assert.Equal(t, filterNone, f.editing, "enter closes the field")
	}
}

func TestFilterInputEscKeepsThePreviousValue(t *testing.T) {
	var f filterInput

	f.startEdit(filterRegion, "internal")

	_, _, done := f.inputKey(keyMsg("a"))
	assert.False(t, done, "a typed rune keeps the field open")
	assert.Equal(t, filterRegion, f.editing)
	assert.Equal(t, "internala", f.input.Value())

	_, _, done = f.inputKey(keyMsg("esc"))
	assert.False(t, done, "esc applies nothing")
	assert.Equal(t, filterNone, f.editing)
}

func TestFilterInputEditKeysNameTheField(t *testing.T) {
	var f filterInput

	f.startEdit(filterRegion, "")
	assert.Equal(t, []key.Help{{Key: "enter", Desc: "apply filter"}, {Key: "esc", Desc: "cancel"}},
		paneHelps(f.editKeys()))

	f.startEdit(filterText, "")
	assert.Equal(t, []key.Help{{Key: "enter", Desc: "apply search"}, {Key: "esc", Desc: "cancel"}},
		paneHelps(f.editKeys()))
}

func TestFilterInputFieldView(t *testing.T) {
	th := ui.NewTheme(true)

	var f filterInput

	assert.Equal(t, "all", ansi.Strip(f.fieldView(th, filterRegion, "", "all")))
	assert.Equal(t, "internal", ansi.Strip(f.fieldView(th, filterRegion, "internal", "all")))
	assert.Equal(t, `"two words"`, ansi.Strip(f.fieldView(th, filterText, "two words", "any")),
		"a text value shows in quotes")

	f.startEdit(filterText, "ab")
	assert.Equal(t, "ab_", ansi.Strip(f.fieldView(th, filterText, "old", "any")), "the open field shows the input")
	assert.Equal(t, "internal", ansi.Strip(f.fieldView(th, filterRegion, "internal", "all")),
		"a closed field shows its value")
}

func TestFilterChipsSpacing(t *testing.T) {
	th := ui.NewTheme(true)
	label := func(f propState) string { return f.label() }

	assert.Equal(t, "[pending] applied  dismissed  superseded",
		ansi.Strip(filterChips(th, propStateCount, propPending, label)))
	assert.Equal(t, "pending  applied [dismissed] superseded",
		ansi.Strip(filterChips(th, propStateCount, propDismissed, label)),
		"one space beside the brackets, two between plain labels")
}

func TestFilterLinesBreakOnlyWhenTooWide(t *testing.T) {
	th := ui.NewTheme(true)
	one := " state x     region y"

	assert.Equal(t, []string{one}, filterLines(th, "state x", "region y", ui.Width(one)))
	assert.Equal(t, []string{" state x", " region y"}, filterLines(th, "state x", "region y", ui.Width(one)-1))
}

// TestListPaneKeysStayApart guards the key order of the views. Their
// action keys come before navKey, so an action key that is also a pane
// key would hide the pane key. esc is the one planned exception:
// Proposals takes it first to end a run filter, and the run filter
// tests cover that order.
func TestListPaneKeysStayApart(t *testing.T) {
	pane := []key.Binding{
		ui.Nav.Up, ui.Nav.Down, ui.Nav.PageUp, ui.Nav.PageDown, ui.Nav.Top, ui.Nav.Bottom,
		ui.Nav.Enter, ui.Nav.Back, ui.Nav.Next, ui.Nav.Prev,
	}

	actions := []key.Binding{
		ui.Nav.Toggle, extractKey,
		lesKeys.Proposals, lesKeys.Pins, lesKeys.Stats, lesKeys.Evidence, lesKeys.Run,
		lesKeys.Clear, lesKeys.State, lesKeys.Region, lesKeys.Open,
		lesKeys.Apply, lesKeys.Dismiss, lesKeys.Reconcile, lesKeys.Confirm,
		pinKeys.Search, pinKeys.Compare, pinKeys.Retarget, pinKeys.Prune,
		pinKeys.New, pinKeys.Edit, pinKeys.Remove,
	}

	for _, a := range actions {
		for _, k := range a.Keys() {
			for _, p := range pane {
				assert.NotContains(t, p.Keys(), k, "action key %q is also the pane key %q", k, p.Help().Desc)
			}
		}
	}
}

// TestListPaneViewsShadowNothing reads the syntax trees of the package.
// A view that embeds listPane or filterInput must not declare a field or
// a method with the name of a member of either type. Go embedding has no
// virtual dispatch, so a pane method would keep its own member.
func TestListPaneViewsShadowNothing(t *testing.T) {
	scan := scanEmbedding(t)
	views := []string{"propView", "pinsView"}

	scan.assertShadowsNothing(t, "listPane",
		[]string{"list", "cursorID", "focusDetail", "navKey", "cursorRow", "showsDetail"}, views)
	scan.assertShadowsNothing(t, "filterInput", []string{"startEdit", "inputKey", "fieldView"}, views)

	// Both types sit at the same depth in a view. A shared name would make
	// the promoted member ambiguous.
	for _, name := range scan.members["filterInput"] {
		assert.NotContains(t, scan.members["listPane"], name, "listPane and filterInput both declare %s", name)
	}
}

// paneHelps returns the help texts of bindings.
func paneHelps(bindings []key.Binding) []key.Help {
	out := make([]key.Help, len(bindings))
	for i, b := range bindings {
		out[i] = b.Help()
	}

	return out
}
