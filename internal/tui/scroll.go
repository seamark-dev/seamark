package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// This file holds the position state of the console. scrollState is
// the position of a scrolled text. textPager pages a long text that a
// selection picks. listState is a plain list with a cursor. Each view
// keeps one value per text or list, so a resize or a refresh keeps the
// position. The views and box.go draw the lines. This file only picks
// the visible range and the position note.
//
// The run records of distillation and extraction keep a second cursor
// model on purpose: a cursor row, a follow flag and a scrollState for
// pinnedBox. There the arrows move the cursor and the page keys scroll
// the text, so listState does not fit them.

// scrollState is the vertical position of one scrollable text. The view
// records the page size and the last offset, because the key handler
// cannot know how tall the rendered text is. The modals and most panes
// share this type, so their texts scroll with the same keys.
type scrollState struct {
	off  int
	page int
	last int
}

// by moves the position by delta lines inside the recorded bounds.
func (sc *scrollState) by(delta int) {
	sc.off = max(0, min(sc.off+delta, sc.last))
}

// key applies a movement key and reports whether the key was one.
func (sc *scrollState) key(msg tea.KeyMsg) bool {
	page := max(1, sc.page)

	switch {
	case key.Matches(msg, nkeys.Up):
		sc.by(-1)
	case key.Matches(msg, nkeys.Down):
		sc.by(1)
	case key.Matches(msg, nkeys.PageUp):
		sc.by(-page)
	case key.Matches(msg, nkeys.PageDown):
		sc.by(page)
	case key.Matches(msg, nkeys.Top):
		sc.off = 0
	case key.Matches(msg, nkeys.Bottom):
		sc.off = sc.last
	default:
		return false
	}

	return true
}

// scrolls reports text that is taller than its window.
func (sc *scrollState) scrolls() bool {
	return sc.last > 0
}

// fit records the bounds of a text of n lines in a window of h lines
// and returns the visible range [from, to). It clamps the offset,
// because a resize or a refresh can make the text shorter. Every box
// that scrolls calls it once per render, so all boxes keep one rule.
func (sc *scrollState) fit(n, h int) (from, to int) {
	h = max(0, h)
	sc.page = h
	sc.last = max(0, n-h)
	sc.by(0)

	return sc.off, min(n, sc.off+h)
}

// reveal moves the offset so that the lines top to bottom show in a
// window of h lines. A negative top means no line to keep, so nothing
// moves. When the range is taller than the window, the top line wins,
// because it holds the heading of the cursor group. The caller runs fit
// next, which brings the offset back inside the bounds.
func (sc *scrollState) reveal(top, bottom, h int) {
	if top < 0 {
		return
	}

	if bottom >= sc.off+h {
		sc.off = bottom - h + 1
	}

	if top < sc.off {
		sc.off = top
	}
}

// textPager scrolls one long text that a selection picks, for example
// the inspection of a client or the diff of a planned write. A new id
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
// then cannot reach scrollState.key, which does not know the third rule.
type textPager struct {
	sc scrollState
	// id names the text of the last render.
	id string
	// n is the line count of the last render, for the position note.
	n int
}

// lines records the bounds of body in a window of h rows and returns the
// visible lines, each cut to w cells.
func (p *textPager) lines(id string, body []string, w, h int) []string {
	if len(body) == 0 {
		body = []string{""}
	}

	h = max(1, h)
	p.n = len(body)
	p.sc.page, p.sc.last = h, max(0, p.n-h)

	switch {
	case id != p.id:
		p.id, p.sc.off = id, 0
	case p.sc.off >= p.n:
		p.sc.off = p.sc.last
	}

	end := min(p.n, p.sc.off+h)
	out := make([]string, 0, end-p.sc.off)

	for _, line := range body[p.sc.off:end] {
		out = append(out, ansi.Truncate(line, w, ""))
	}

	return out
}

// key applies a movement key and reports whether the key was one. A
// down key keeps an offset past the bottom, which is the third rule of
// the type comment.
func (p *textPager) key(msg tea.KeyMsg) bool {
	if p.sc.off > p.sc.last && key.Matches(msg, nkeys.Down, nkeys.PageDown) {
		return true
	}

	return p.sc.key(msg)
}

// span returns the faint "a–b of n" of the visible lines, also when
// every line fits, because the diff box of the setup design shows it.
// It returns "" before the first render.
func (p *textPager) span(t *Theme) string {
	if p.n == 0 {
		return ""
	}

	return propRange(t, p.sc.off, min(p.n, p.sc.off+p.sc.page), p.n)
}

// foot returns span while the text is taller than its window, else "".
func (p *textPager) foot(t *Theme) string {
	if !p.sc.scrolls() {
		return ""
	}

	return p.span(t)
}

// listState is the cursor and scroll offset of one plain list. Screens
// keep one per list, so resize and refresh never reset the position.
type listState struct {
	cursor int
	offset int
	// window is the row count of the last render. A page key moves by
	// window-1 rows, so one row of the old page stays in view.
	window int
}

// clamp keeps the cursor inside n rows and the cursor row inside a
// window of h rows. It records a positive h as the window of the page
// keys. A key handler passes 0, because it does not know the height,
// and the recorded window then stays.
func (l *listState) clamp(n, h int) {
	if h > 0 {
		l.window = h
	}

	if n <= 0 {
		l.cursor, l.offset = 0, 0

		return
	}

	l.cursor = max(0, min(l.cursor, n-1))

	if h <= 0 {
		return
	}

	if l.cursor < l.offset {
		l.offset = l.cursor
	}

	if l.cursor >= l.offset+h {
		l.offset = l.cursor - h + 1
	}

	l.offset = max(0, min(l.offset, max(0, n-h)))
}

// move shifts the cursor by delta rows inside n rows.
func (l *listState) move(delta, n int) {
	if n == 0 {
		return
	}

	l.cursor = max(0, min(l.cursor+delta, n-1))
}

// key moves the cursor by one movement key inside n rows and reports
// whether the key was one. Before the first render the window is 0, so
// a page key moves one row.
func (l *listState) key(msg tea.KeyMsg, n int) bool {
	page := max(1, l.window-1)

	switch {
	case key.Matches(msg, nkeys.Up):
		l.move(-1, n)
	case key.Matches(msg, nkeys.Down):
		l.move(1, n)
	case key.Matches(msg, nkeys.PageUp):
		l.move(-page, n)
	case key.Matches(msg, nkeys.PageDown):
		l.move(page, n)
	case key.Matches(msg, nkeys.Top):
		l.move(-n, n)
	case key.Matches(msg, nkeys.Bottom):
		l.move(n, n)
	default:
		return false
	}

	return true
}
