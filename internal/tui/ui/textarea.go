package ui

import (
	"math"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// This file holds TextArea, the multi-line text input of the console.
// input.go says why no bubbles textarea is used. TextArea shares the
// editing core runeEdit and the cursor glyphs with LineInput.
//
// The value keeps every rune that MultiLine keeps, tabs and line breaks
// included, but not the line and paragraph separators (areaKeeps). The
// area never cuts an input: it has no length limit, and a rune that it
// cannot show goes away with a Notice. A tab shows as the four spaces
// of MultiLine, so the width arithmetic stays exact. The area measures,
// wraps and moves per grapheme cluster (runeEdit), as the box and the
// terminal measure.

// TextArea is a multi-line text input with soft wrap. enter inserts a
// line break, so a form leaves the area with another key, for example
// esc. The arrows, home and end, ctrl+a and ctrl+e, pgup and pgdown move
// the cursor. The zero value is an empty area.
type TextArea struct {
	runeEdit

	// Placeholder is the faint hint of an empty area. "" shows only the
	// cursor.
	Placeholder string

	// notice explains the last input that lost runes.
	notice string

	// goal is the display column that up and down keep, so a move
	// through a short row returns to the column. hasGoal is false after
	// any other key.
	goal    int
	hasGoal bool

	// width and page are the size of the last View. The key handler has
	// no render size, so up and down use the rows of that layout. Before
	// the first View, width is 0 and the rows are the lines.
	width, page int

	// scroll keeps the cursor row inside the window of View.
	scroll ScrollState

	// rows caches the layout of rowsW cells. An edit clears it, because
	// a long value takes time to wrap again.
	rows  []areaRow
	rowsW int
}

// areaRow is one display row: the runes [start, end) of the value. A
// row never holds a line break, and it starts and ends between two
// grapheme clusters. The rows of one line follow each other without a
// gap, so the end of a row is the start of the next one.
type areaRow struct {
	start, end int
}

// areaTab is the display width of a tab: MultiLine writes it as four
// spaces.
var areaTab = Width(MultiLine("\t"))

// areaKeeps reports whether the area keeps r. It is the rune rule of
// MultiLine. A tab and a line break stay; a control, a C1 control or a
// direction override goes away. MultiLine changes the tab into spaces,
// so the tab needs its own case. A line or paragraph separator and a
// direction mark also go away, as in LineInput (hiddenRune).
func areaKeeps(r rune) bool {
	s := string(r)

	return r == '\t' || (!hiddenRune(r) && MultiLine(s) == s)
}

// cellsOf returns the display width of the cluster g in the area. A tab
// is a cluster of its own, and MultiLine draws it as four spaces.
func (a *TextArea) cellsOf(g grapheme) int {
	if a.runes[g.start] == '\t' {
		return areaTab
	}

	return g.cells
}

// Value returns the text of the area.
func (a *TextArea) Value() string {
	return string(a.runes)
}

// SetValue replaces the text and puts the cursor at its end. A rune that
// the area cannot hold goes away, and Notice then says how many went
// away. A form checks a stored text with Holds first, because it must
// not change a text that the user did not edit.
func (a *TextArea) SetValue(s string) {
	kept, dropped := areaFilter([]rune(s))

	a.set(kept)
	a.changed()
	a.scroll = ScrollState{}
	a.hasGoal = false
	a.notice = dropNotice(dropped)
}

// Holds reports whether the area can show and edit s exactly: it keeps
// every rune of s.
func (a *TextArea) Holds(s string) bool {
	for _, r := range s {
		if !areaKeeps(r) {
			return false
		}
	}

	return true
}

// Notice returns why the last input lost runes, or "". A form shows it
// under the area, because a dropped rune must be visible.
func (a *TextArea) Notice() string {
	return a.notice
}

// Key applies one key press. A paste arrives as one key with every rune.
// A terminal without bracketed paste sends a line break as enter or
// ctrl+j, and a tab as tab. So these keys insert their rune.
func (a *TextArea) Key(msg tea.KeyMsg) {
	a.notice = ""

	if msg.Type != tea.KeyUp && msg.Type != tea.KeyDown && msg.Type != tea.KeyPgUp && msg.Type != tea.KeyPgDown {
		a.hasGoal = false
	}

	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		a.typeRunes(msg.Runes)
	case tea.KeyEnter, tea.KeyCtrlJ:
		a.insert([]rune{'\n'})
		a.changed()
	case tea.KeyTab:
		a.insert([]rune{'\t'})
		a.changed()
	case tea.KeyBackspace:
		a.backspace()
		a.changed()
	case tea.KeyDelete:
		a.deleteForward()
		a.changed()
	case tea.KeyCtrlU:
		a.deleteFrom(a.lineStart(a.pos))
		a.changed()
	case tea.KeyLeft:
		a.left()
	case tea.KeyRight:
		a.right()
	case tea.KeyHome, tea.KeyCtrlA:
		a.moveTo(a.lineStart(a.pos))
	case tea.KeyEnd, tea.KeyCtrlE:
		a.moveTo(a.lineEnd(a.pos))
	case tea.KeyUp:
		a.moveRows(-1)
	case tea.KeyDown:
		a.moveRows(1)
	case tea.KeyPgUp:
		a.moveRows(-max(1, a.page))
	case tea.KeyPgDown:
		a.moveRows(max(1, a.page))
	}
}

// typeRunes inserts the runes of one key press. A terminal sends a
// pasted line break as CR LF, CR or LF, so each becomes one LF. Then the
// rune rule of areaKeeps applies to each rune, and Notice counts the
// runes that went away. Nothing else is removed, so a long paste stays
// whole.
func (a *TextArea) typeRunes(rs []rune) {
	text := strings.ReplaceAll(string(rs), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	kept, dropped := areaFilter([]rune(text))

	a.insert(kept)
	a.changed()
	a.notice = dropNotice(dropped)
}

// areaFilter returns the runes that the area keeps and the count of the
// others.
func areaFilter(rs []rune) (kept []rune, dropped int) {
	kept = slices.DeleteFunc(slices.Clone(rs), func(r rune) bool { return !areaKeeps(r) })

	return kept, len(rs) - len(kept)
}

// changed forgets the layout after an edit.
func (a *TextArea) changed() {
	a.rows = nil
}

// moveRows moves the cursor by delta display rows and keeps the goal
// column. A move past the first row goes to the start of the text. A
// move past the last row goes to its end. Most editors do the same.
func (a *TextArea) moveRows(delta int) {
	rows := a.layout(a.width)
	cur := areaRowOf(rows, a.pos)

	if !a.hasGoal {
		a.goal, a.hasGoal = a.cells(rows[cur].start, a.pos), true
	}

	next := cur + delta

	switch {
	case next < 0:
		a.moveTo(0)
	case next >= len(rows):
		a.moveTo(len(a.runes))
	default:
		a.moveTo(a.atColumn(rows, next, a.goal))
	}
}

// atColumn returns the cluster start in row r that is nearest to
// display column col without passing it. The end of a wrapped row is the
// start of the next row, so the cursor stops before the last cluster.
func (a *TextArea) atColumn(rows []areaRow, r, col int) int {
	row := rows[r]
	wrapped := r+1 < len(rows) && rows[r+1].start == row.end && row.end > row.start

	at, cells := row.start, 0

	for g := range graphemes(a.runes, row.start, row.end) {
		c := a.cellsOf(g)
		if cells+c > col || (wrapped && g.end == row.end) {
			break
		}

		cells += c
		at = g.end
	}

	return at
}

// cells returns the display width of the runes [from, to). from is the
// start of a row, so it is the start of a cluster.
func (a *TextArea) cells(from, to int) int {
	n := 0

	for g := range graphemes(a.runes, from, to) {
		n += a.cellsOf(g)
	}

	return n
}

// layout returns the display rows for a view of w cells. One cell of
// each row stays free for the cursor glyph, so a row with the cursor
// still fits. A width of 0 or less wraps nothing, so the rows are the
// lines.
func (a *TextArea) layout(w int) []areaRow {
	if a.rows != nil && a.rowsW == w {
		return a.rows
	}

	wrap := math.MaxInt
	if w > 0 {
		wrap = max(1, w-1)
	}

	var rows []areaRow

	start := 0

	for {
		end := slices.Index(a.runes[start:], '\n')
		if end < 0 {
			end = len(a.runes)
		} else {
			end += start
		}

		rows = a.wrapLine(rows, start, end, wrap)

		if end == len(a.runes) {
			break
		}

		start = end + 1
	}

	a.rows, a.rowsW = rows, w

	return rows
}

// wrapLine appends the rows of the line [start, end) at wrap cells. A
// row breaks after its last space, so a word stays whole. A word wider
// than a row breaks at the cell limit. A row holds whole clusters only.
// This splitter does not use ui.Wrap, because ui.Wrap drops the spaces
// at a break. An editor must keep every rune on a row, so the cursor can
// stand on it.
func (a *TextArea) wrapLine(rows []areaRow, start, end, wrap int) []areaRow {
	gs := slices.Collect(graphemes(a.runes, start, end))
	first := 0

	for {
		cells, i, space := 0, first, -1

		for i < len(gs) {
			c := a.cellsOf(gs[i])
			if cells+c > wrap && i > first {
				break
			}

			cells += c

			if a.runes[gs[i].start] == ' ' {
				space = i + 1
			}

			i++
		}

		if i == len(gs) {
			return append(rows, areaRow{start: start, end: end})
		}

		if space > first {
			i = space
		}

		rows = append(rows, areaRow{start: start, end: gs[i].start})
		first, start = i, gs[i].start
	}
}

// areaRowOf returns the row that shows the cursor at index p: the last
// row that starts at or before p. So a cursor at a wrap point shows at
// the start of the next row. A cursor before a line break shows at the
// end of its line.
func areaRowOf(rows []areaRow, p int) int {
	i, _ := slices.BinarySearchFunc(rows, p+1, func(r areaRow, target int) int {
		if r.start < target {
			return -1
		}

		return 1
	})

	return max(0, i-1)
}

// View renders at most h rows of w cells. It scrolls the rows so that
// the cursor row shows, and it draws the cursor with the glyphs of
// LineInput (cursorView). A tab shows as spaces (MultiLine).
//
// w must be at least 5 cells. A row always holds its first cluster, and
// a tab takes 4 cells plus 1 for the cursor. So a narrower view can
// hold a row wider than w. A form gives the area a far wider box.
func (a *TextArea) View(t *Theme, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}

	a.width, a.page = w, h

	if len(a.runes) == 0 {
		hint := t.Accent.Render("_")
		if a.Placeholder != "" {
			hint += t.Faint.Render(" " + OneLine(a.Placeholder))
		}

		return Truncate(hint, w, t.Glyphs.Ellipsis)
	}

	rows := a.layout(w)
	cur := areaRowOf(rows, a.pos)

	a.scroll.Reveal(cur, cur, h)
	from, to := a.scroll.Fit(len(rows), h)

	lines := make([]string, 0, to-from)

	for i := from; i < to; i++ {
		row := rows[i]
		shown := []rune(MultiLine(string(a.runes[row.start:row.end])))

		at := -1
		if i == cur {
			at = len([]rune(MultiLine(string(a.runes[row.start:a.pos]))))
		}

		lines = append(lines, cursorView(t, shown, at))
	}

	return strings.Join(lines, "\n")
}
