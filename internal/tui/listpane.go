package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the list and detail pane of the Lessons sub-views
// Proposals and Pins. listPane holds the cursor, the focus and the two
// boxes. filterInput holds the text input of the filter line. Both
// views embed the two types, so one rule defines the cursor identity,
// the pane keys, the box frames and the filter line format. Each view
// keeps its rows, filters, marks, actions, columns, detail text and
// empty states. A new list view with a cursor by identity embeds the
// same two types.
//
// Doctor and Overview keep their own list models. Their focus, page and
// enter rules differ, and the captures and tests record those rules.
//
// Go embedding does not dispatch to the outer type. A pane method never
// reaches a view method of the same name. So no view defines a method
// with the name of a pane method, and the views wrap the pane boxes as
// listBox and detailBox. TestListPaneViewsShadowNothing checks this
// rule.

// listPane is the list and detail state of one list view. R is the row
// type, and K is the identity of a row. The cursor records the identity
// and not the position, because a refresh can move every row (D-15).
type listPane[R any, K comparable] struct {
	list ui.ListState
	// cursorID is the identity of the cursor row. The zero K means no
	// row: before the first snapshot, or in an empty list.
	cursorID K
	// focusDetail is the focused pane in the wide layout and the shown
	// pane in the narrow layout. Resize keeps it.
	focusDetail bool
	detail      ui.ScrollState
	// detailID is the row of the last detail render. Another row starts
	// the detail at the top.
	detailID K

	// visible reports that the list is on screen. A refresh reports a
	// lost cursor row only then, because the note names the list that
	// the user sees. nil means always visible.
	visible func() bool
	// wide reports the side-by-side layout. The views set state.wide,
	// so the key handlers, the footer and the render agree.
	wide func() bool
	// ident returns the identity of a row.
	ident func(R) K
	// find returns the position of the row with identity id. nil
	// compares the ident values. Pins sets it, because a moved pin can
	// keep its identity by content (pinMatch).
	find func(rows []R, id K) (int, bool)
}

// cursorRow returns the row under the cursor. It clamps the cursor,
// because a refresh can make the list shorter.
func (lp *listPane[R, K]) cursorRow(rows []R) (R, bool) {
	if len(rows) == 0 {
		var none R

		return none, false
	}

	lp.list.Clamp(len(rows), 0)

	return rows[lp.list.Cursor], true
}

// syncCursorID records the identity of the cursor row, so a refresh can
// find the same row again. An empty list records no row. Else a refresh
// reports the last lost row again and again, because it never comes
// back.
func (lp *listPane[R, K]) syncCursorID(rows []R) {
	var none K
	lp.cursorID = none

	if cur, ok := lp.cursorRow(rows); ok {
		lp.cursorID = lp.ident(cur)
	}
}

// locate returns the position of the row with identity id.
func (lp *listPane[R, K]) locate(rows []R, id K) (int, bool) {
	if lp.find != nil {
		return lp.find(rows, id)
	}

	i := slices.IndexFunc(rows, func(r R) bool { return lp.ident(r) == id })

	return max(i, 0), i >= 0
}

// relocate puts the cursor on the recorded identity when it is visible.
// It records the identity of the found row, because find can match a
// row that moved. Else a refresh keeps the cursor position, which shows
// the neighbor of the lost row, and a filter change starts at the first
// row. It reports whether the identity was found.
func (lp *listPane[R, K]) relocate(rows []R, keepPosition bool) bool {
	if i, ok := lp.locate(rows, lp.cursorID); ok {
		lp.list.Cursor = i
		lp.cursorID = lp.ident(rows[i])

		return true
	}

	if !keepPosition {
		lp.list.Cursor, lp.list.Offset = 0, 0
	}

	lp.syncCursorID(rows)

	return false
}

// reconcileCursor applies a new snapshot to the cursor. lost reports a
// cursor row that the snapshot does not show, and gone is its identity.
// The view words the note, because only the view knows why a row is
// missing.
func (lp *listPane[R, K]) reconcileCursor(rows []R) (gone K, lost bool) {
	var none K

	gone = lp.cursorID
	if gone == none {
		// Nothing was under the cursor yet: the first snapshot, or an
		// empty list.
		lp.syncCursorID(rows)

		return gone, false
	}

	return gone, !lp.relocate(rows, true)
}

// shown reports that the list is on screen.
func (lp *listPane[R, K]) shown() bool {
	return lp.visible == nil || lp.visible()
}

// navKey applies a pane key: tab and shift+tab, enter, esc, a detail
// scroll key, or a cursor key. It reports whether the key was one. The
// views call it after their action keys. The letter actions share no
// key with ui.Nav (TestListPaneKeysStayApart). Proposals takes esc first
// on purpose: with the focus on the list, esc ends a run filter. So the
// order of the key switch matters for esc.
func (lp *listPane[R, K]) navKey(msg tea.KeyMsg, rows []R) bool {
	switch {
	case key.Matches(msg, ui.Nav.Next, ui.Nav.Prev):
		if lp.wide() && len(rows) > 0 {
			lp.focusDetail = !lp.focusDetail
		}
	case key.Matches(msg, ui.Nav.Enter):
		if len(rows) > 0 {
			lp.focusDetail = true
		}
	case key.Matches(msg, ui.Nav.Back):
		lp.focusDetail = false
	case lp.showsDetail(rows) && lp.detailKey(msg):
	case lp.list.Key(msg, len(rows)):
		lp.syncCursorID(rows)
	default:
		return false
	}

	return true
}

// showsDetail reports a visible detail pane: beside the list in the
// wide layout, or alone in the narrow layout.
func (lp *listPane[R, K]) showsDetail(rows []R) bool {
	return len(rows) > 0 && (lp.wide() || lp.focusDetail)
}

// detailKey scrolls the detail pane. The page keys always scroll it in
// the wide layout, as the "pgdn" hint under the pane says. The other
// movement keys scroll it only while it has focus. It reports whether
// the key scrolled.
func (lp *listPane[R, K]) detailKey(msg tea.KeyMsg) bool {
	paging := key.Matches(msg, ui.Nav.PageUp, ui.Nav.PageDown)
	if !lp.focusDetail && (!lp.wide() || !paging) {
		return false
	}

	return lp.detail.Key(msg)
}

// paneKey names the key that changes the visible or focused pane.
func (lp *listPane[R, K]) paneKey(hasRow bool) key.Binding {
	switch {
	case !hasRow:
		b := ui.WithHelp(ui.Nav.Enter, "enter", "detail")
		b.SetEnabled(false)

		return b
	case lp.wide() && lp.focusDetail:
		return ui.WithHelp(ui.Nav.Next, "tab", "list")
	case lp.wide():
		return ui.WithHelp(ui.Nav.Next, "tab", "detail")
	case lp.focusDetail:
		return ui.WithHelp(ui.Nav.Back, "esc", "back")
	default:
		return ui.WithHelp(ui.Nav.Enter, "enter", "detail")
	}
}

// split renders the list box and the detail box of rows in h rows. The
// wide layout puts them side by side, with a list of listW cells and a
// gap of one cell. The narrow layout shows only the shown pane, at the
// full width w. The list renders first, because it clamps the cursor
// that the detail reads.
func (lp *listPane[R, K]) split(rows []R, w, h, listW int,
	list, detail func(rows []R, w, h int, focused bool) string,
) string {
	if lp.wide() {
		return ui.HJoin(1, list(rows, listW, h, !lp.focusDetail), detail(rows, w-listW-1, h, lp.focusDetail))
	}

	if lp.focusDetail {
		return detail(rows, w, h, true)
	}

	return list(rows, w, h, true)
}

// renderList renders the list box: the styled header, the visible rows
// and a foot. row renders one row, and the pane adds the selection
// background to the cursor row of the focused box. The foot shows the
// position when the rows overflow. In the narrow layout it says how to
// open the detail.
func (lp *listPane[R, K]) renderList(t *ui.Theme, rows []R, w, h int, focused bool, title, header string,
	row func(r R, cursor bool) string,
) string {
	inner := ui.BoxInner(w)
	window := max(1, ui.BoxRows(h)-1)
	lp.list.Clamp(len(rows), window)

	lines := []string{header}
	end := min(len(rows), lp.list.Offset+window)

	for i := lp.list.Offset; i < end; i++ {
		cursor := i == lp.list.Cursor

		line := row(rows[i], cursor)
		if cursor && focused {
			line = t.Highlight(line, inner, ui.BgSel)
		}

		lines = append(lines, line)
	}

	var foot string

	switch {
	case len(rows) > window:
		foot = t.RangeNote(lp.list.Offset, end, len(rows))
	case !lp.wide():
		foot = t.Faint.Render("enter for detail")
	}

	return t.Box(ui.BoxOpts{
		W: w, H: h, Focus: focused, Lines: lines, Foot: foot,
		Title: ui.BoxTitle(t, title, focused), Right: t.Muted.Render(strconv.Itoa(len(rows))),
	})
}

// renderDetail renders the detail box of row cur with its body lines.
// The body scrolls inside the box, and another row starts at the top.
// The caller renders body before this call, so the body never reads a
// scroll position of another row.
func (lp *listPane[R, K]) renderDetail(t *ui.Theme, cur R, title string, body []string, w, h int, focused bool) string {
	if id := lp.ident(cur); lp.detailID != id {
		lp.detail.Off = 0
		lp.detailID = id
	}

	from, to := lp.detail.Fit(len(body), ui.BoxRows(h))
	sep := " " + t.Glyphs.Sep + " "

	var foot string

	switch {
	case lp.detail.Off < lp.detail.Last:
		foot = t.Faint.Render("↓ more" + sep + "pgdn")
	case lp.detail.Off > 0:
		foot = t.Faint.Render("↑ more" + sep + "pgup")
	}

	return t.Box(ui.BoxOpts{
		W: w, H: h, Focus: focused, Foot: foot,
		Title: ui.BoxTitle(t, title, focused),
		Lines: body[from:to],
	})
}

// filterField names the field of a filter line that the text input
// edits.
type filterField int

const (
	// filterNone means no open input.
	filterNone filterField = iota
	// filterRegion is the region filter. Its value is a path prefix.
	filterRegion
	// filterText is the text search. Its value is free text.
	filterText
)

// filterInput is the text input of a filter line. At most one field is
// open, so one input serves every field.
type filterInput struct {
	editing filterField
	input   ui.LineInput
	// textHint is the faint hint of an empty text search. The region
	// field keeps the hint of ui.LineInput.
	textHint string
}

// startEdit opens field with its current value. It resets the input, so
// the hint follows the field.
func (f *filterInput) startEdit(field filterField, value string) {
	f.editing = field
	f.input = ui.LineInput{}

	if field == filterText {
		f.input.Placeholder = f.textHint
	}

	f.input.Set(value)
}

// inputKey applies a key to the open field. enter closes the field and
// returns it with its normalized value, and done is true. esc closes the
// field and keeps the previous value. Any other key edits the value. A
// region gets the form that proposals use (propNormalizeRegion), and a
// text loses its outer spaces.
func (f *filterInput) inputKey(msg tea.KeyMsg) (field filterField, value string, done bool) {
	switch {
	case key.Matches(msg, ui.Nav.Enter):
		field = f.editing
		f.editing = filterNone

		if field == filterText {
			return field, strings.TrimSpace(f.input.Value()), true
		}

		return field, propNormalizeRegion(f.input.Value()), true
	case key.Matches(msg, ui.Nav.Back):
		f.editing = filterNone

		return filterNone, "", false
	}

	f.input.Key(msg)

	return filterNone, "", false
}

// editKeys returns the footer keys of the open field.
func (f *filterInput) editKeys() []key.Binding {
	label := "apply filter"
	if f.editing == filterText {
		label = "apply search"
	}

	return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", label), ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
}

// fieldView renders the value of field on the filter line: the open
// input, the muted empty word, or the value. A text value shows in
// quotes, so its outer words read as one search.
func (f *filterInput) fieldView(t *ui.Theme, field filterField, value, empty string) string {
	switch {
	case f.editing == field:
		return f.input.View(t)
	case value == "":
		return t.Muted.Render(empty)
	case field == filterText:
		return t.Body.Render(`"` + ui.OneLine(value) + `"`)
	default:
		return t.Body.Render(ui.OneLine(value))
	}
}

// filterChips renders the n filter states, for example "[pending 6]
// applied 1  dismissed 0". The active state has brackets as well as the
// accent, so it stays visible without color. One space separates a
// bracket from its neighbor, and two spaces separate plain labels.
func filterChips[F ~int](t *ui.Theme, n, active F, label func(F) string) string {
	var b strings.Builder

	for f := F(0); f < n; f++ {
		if f > 0 {
			if f == active || f-1 == active {
				b.WriteString(" ")
			} else {
				b.WriteString("  ")
			}
		}

		if f == active {
			b.WriteString(t.TabActive.Render("[" + label(f) + "]"))
		} else {
			b.WriteString(t.Muted.Render(label(f)))
		}
	}

	return b.String()
}

// filterLines joins the states segment and the rest of a filter line
// with five spaces. A line wider than w becomes two lines, and each one
// loses its end.
func filterLines(t *ui.Theme, states, rest string, w int) []string {
	if one := " " + states + "     " + rest; ui.Width(one) <= w {
		return []string{one}
	}

	return []string{
		" " + ui.Truncate(states, w-1, t.Glyphs.Ellipsis),
		" " + ui.Truncate(rest, w-1, t.Glyphs.Ellipsis),
	}
}
