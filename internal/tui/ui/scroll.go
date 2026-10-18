package ui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// This file holds the position state of the console. ScrollState is
// the position of a scrolled text. TextPager pages a long text that a
// selection picks. ListState is a plain list with a cursor. Each view
// keeps one value per text or list, so a resize or a refresh keeps the
// position. The views and box.go draw the lines. This file only picks
// the visible range and the position note.
//
// Some screens keep a second cursor model on purpose: a cursor row, a
// follow flag and a ScrollState for StickyBox. There the arrows move
// the cursor and the page keys scroll the text, so ListState does not
// fit them.

// ScrollState is the vertical position of one scrollable text. The view
// records the page size and the last offset, because the key handler
// cannot know how tall the rendered text is. The modals and most panes
// share this type, so their texts scroll with the same keys.
type ScrollState struct {
	Off  int
	page int
	Last int
}

// by moves the position by delta lines inside the recorded bounds.
func (sc *ScrollState) by(delta int) {
	sc.Off = max(0, min(sc.Off+delta, sc.Last))
}

// Key applies a movement key and reports whether the key was one.
func (sc *ScrollState) Key(msg tea.KeyMsg) bool {
	page := max(1, sc.page)

	switch {
	case key.Matches(msg, Nav.Up):
		sc.by(-1)
	case key.Matches(msg, Nav.Down):
		sc.by(1)
	case key.Matches(msg, Nav.PageUp):
		sc.by(-page)
	case key.Matches(msg, Nav.PageDown):
		sc.by(page)
	case key.Matches(msg, Nav.Top):
		sc.Off = 0
	case key.Matches(msg, Nav.Bottom):
		sc.Off = sc.Last
	default:
		return false
	}

	return true
}

// Scrolls reports text that is taller than its window.
func (sc *ScrollState) Scrolls() bool {
	return sc.Last > 0
}

// Fit records the bounds of a text of n lines in a window of h lines
// and returns the visible range [from, to). It clamps the offset,
// because a resize or a refresh can make the text shorter. Every box
// that scrolls calls it once per render, so all boxes keep one rule.
func (sc *ScrollState) Fit(n, h int) (from, to int) {
	h = max(0, h)
	sc.page = h
	sc.Last = max(0, n-h)
	sc.by(0)

	return sc.Off, min(n, sc.Off+h)
}

// Reveal moves the offset so that the lines top to bottom show in a
// window of h lines. A negative top means no line to keep, so nothing
// moves. When the range is taller than the window, the top line wins,
// because it holds the heading of the cursor group. The caller runs fit
// next, which brings the offset back inside the bounds.
func (sc *ScrollState) Reveal(top, bottom, h int) {
	if top < 0 {
		return
	}

	if bottom >= sc.Off+h {
		sc.Off = bottom - h + 1
	}

	if top < sc.Off {
		sc.Off = top
	}
}

// TextPager scrolls one long text that a selection picks, for example
// the detail of a list row or the diff of a planned write. A new id
// starts at the top, because a new selection is a new text. The same id
// keeps the offset, so a refresh keeps the position.
//
// The pager replaces a bubbles/viewport and keeps three of its rules,
// so every frame stays the same:
//   - An empty text is one empty line, and the window has at least one
//     row.
//   - A line wider than the box loses its end without an ellipsis.
//   - The offset moves back only when it passes the last line. After the
//     box grows, the text can end above the bottom row. A down key then
//     keeps the offset, and an up key moves to the last full page.
//
// The scroll state is a named field and not an embedded one. A caller
// then cannot reach ScrollState.Key, which does not know the third rule.
type TextPager struct {
	sc ScrollState
	// id names the text of the last render.
	id string
	// n is the line count of the last render, for the position note.
	n int
}

// Lines records the bounds of body in a window of h rows and returns the
// visible lines, each cut to w cells.
func (p *TextPager) Lines(id string, body []string, w, h int) []string {
	if len(body) == 0 {
		body = []string{""}
	}

	h = max(1, h)
	p.n = len(body)
	p.sc.page, p.sc.Last = h, max(0, p.n-h)

	switch {
	case id != p.id:
		p.id, p.sc.Off = id, 0
	case p.sc.Off >= p.n:
		p.sc.Off = p.sc.Last
	}

	end := min(p.n, p.sc.Off+h)
	out := make([]string, 0, end-p.sc.Off)

	for _, line := range body[p.sc.Off:end] {
		out = append(out, ansi.Truncate(line, w, ""))
	}

	return out
}

// Key applies a movement key and reports whether the key was one. A
// down key keeps an offset past the bottom, which is the third rule of
// the type comment.
func (p *TextPager) Key(msg tea.KeyMsg) bool {
	if p.sc.Off > p.sc.Last && key.Matches(msg, Nav.Down, Nav.PageDown) {
		return true
	}

	return p.sc.Key(msg)
}

// Span returns the faint "a–b of n" of the visible lines, also when
// every line fits, because a diff box of the 1a design shows it.
// It returns "" before the first render.
func (p *TextPager) Span(t *Theme) string {
	if p.n == 0 {
		return ""
	}

	return t.RangeNote(p.sc.Off, min(p.n, p.sc.Off+p.sc.page), p.n)
}

// Foot returns span while the text is taller than its window, else "".
func (p *TextPager) Foot(t *Theme) string {
	if !p.sc.Scrolls() {
		return ""
	}

	return p.Span(t)
}

// ListState is the cursor and scroll offset of one plain list. Screens
// keep one per list, so resize and refresh never reset the position.
type ListState struct {
	Cursor int
	Offset int
	// window is the row count of the last render. A page key moves by
	// window-1 rows, so one row of the old page stays in view.
	window int
}

// Clamp keeps the cursor inside n rows and the cursor row inside a
// window of h rows. It records a positive h as the window of the page
// keys. A key handler passes 0, because it does not know the height,
// and the recorded window then stays.
func (l *ListState) Clamp(n, h int) {
	if h > 0 {
		l.window = h
	}

	if n <= 0 {
		l.Cursor, l.Offset = 0, 0

		return
	}

	l.Cursor = max(0, min(l.Cursor, n-1))

	if h <= 0 {
		return
	}

	if l.Cursor < l.Offset {
		l.Offset = l.Cursor
	}

	if l.Cursor >= l.Offset+h {
		l.Offset = l.Cursor - h + 1
	}

	l.Offset = max(0, min(l.Offset, max(0, n-h)))
}

// Move shifts the cursor by delta rows inside n rows.
func (l *ListState) Move(delta, n int) {
	if n == 0 {
		return
	}

	l.Cursor = max(0, min(l.Cursor+delta, n-1))
}

// Key moves the cursor by one movement key inside n rows and reports
// whether the key was one. Before the first render the window is 0, so
// a page key moves one row.
func (l *ListState) Key(msg tea.KeyMsg, n int) bool {
	page := max(1, l.window-1)

	switch {
	case key.Matches(msg, Nav.Up):
		l.Move(-1, n)
	case key.Matches(msg, Nav.Down):
		l.Move(1, n)
	case key.Matches(msg, Nav.PageUp):
		l.Move(-page, n)
	case key.Matches(msg, Nav.PageDown):
		l.Move(page, n)
	case key.Matches(msg, Nav.Top):
		l.Move(-n, n)
	case key.Matches(msg, Nav.Bottom):
		l.Move(n, n)
	default:
		return false
	}

	return true
}
