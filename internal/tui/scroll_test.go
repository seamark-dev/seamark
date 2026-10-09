package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// The tests of the position state: scrollState for a scrolled text,
// textPager for a long text that a selection picks, and listState for a
// list with a cursor. They call the types directly, because every
// modal, pane and list moves through them.

func TestScrollStateKeysStayInsideTheRecordedBounds(t *testing.T) {
	sc := scrollState{page: 5, last: 12}

	steps := []struct {
		msg tea.KeyMsg
		off int
	}{
		{keyMsg("down"), 1}, {keyMsg("j"), 2}, {keyMsg("pgdown"), 7}, {keyMsg("pgdown"), 12},
		{keyMsg("down"), 12}, {keyMsg("up"), 11}, {keyMsg("k"), 10}, {keyMsg("pgup"), 5},
		{keyMsg("pgup"), 0}, {keyMsg("up"), 0}, {keyMsg("end"), 12}, {keyMsg("home"), 0},
		{keyMsg("G"), 12}, {keyMsg("g"), 0},
		{tea.KeyMsg{Type: tea.KeyCtrlD}, 5}, {tea.KeyMsg{Type: tea.KeyCtrlU}, 0},
	}

	for _, s := range steps {
		assert.True(t, sc.key(s.msg), "%s is a movement key", s.msg)
		assert.Equal(t, s.off, sc.off, "offset after %s", s.msg)
	}

	assert.False(t, sc.key(keyMsg("x")), "x is not a movement key")
	assert.Equal(t, 0, sc.off, "a key that is not a movement key keeps the offset")
}

func TestScrollStatePagesByOneLineBeforeTheFirstRender(t *testing.T) {
	sc := scrollState{last: 3}

	assert.True(t, sc.key(keyMsg("pgdown")))
	assert.Equal(t, 1, sc.off, "a zero page moves one line, so a page key always moves")
}

func TestScrollStateByClampsToTheRecordedBounds(t *testing.T) {
	sc := scrollState{last: 4}

	sc.by(10)
	assert.Equal(t, 4, sc.off)

	sc.by(-10)
	assert.Equal(t, 0, sc.off)

	// A shorter text lowers last. by(0) then pulls the offset back.
	sc = scrollState{off: 9, last: 2}
	sc.by(0)
	assert.Equal(t, 2, sc.off)
}

func TestScrollStateScrollsOnlyWhenTheTextIsTaller(t *testing.T) {
	var sc scrollState
	assert.False(t, sc.scrolls(), "the zero value has nothing to scroll")

	sc.last = 1
	assert.True(t, sc.scrolls())
}

func TestListStateClampKeepsTheCursorRowInTheWindow(t *testing.T) {
	l := listState{cursor: 10}
	l.clamp(30, 5)
	assert.Equal(t, listState{cursor: 10, offset: 6, window: 5}, l, "the window moves down as little as possible")

	l.cursor = 2
	l.clamp(30, 5)
	assert.Equal(t, listState{cursor: 2, offset: 2, window: 5}, l, "the window moves up as little as possible")

	l = listState{cursor: 20, offset: 18}
	l.clamp(8, 5)
	assert.Equal(t, listState{cursor: 7, offset: 3, window: 5}, l, "a shorter list pulls the cursor and the window back")

	l = listState{cursor: 9, offset: 4, window: 6}
	l.clamp(5, 0)
	assert.Equal(t, listState{cursor: 4, offset: 4, window: 6}, l,
		"without a window only the cursor moves, and the recorded window stays")

	l.clamp(0, 5)
	assert.Equal(t, listState{window: 5}, l, "an empty list resets the position")
}

func TestListStateMoveStopsAtTheEnds(t *testing.T) {
	var l listState

	l.move(-1, 5)
	assert.Equal(t, 0, l.cursor)

	l.move(3, 5)
	assert.Equal(t, 3, l.cursor)

	l.move(10, 5)
	assert.Equal(t, 4, l.cursor)

	l.move(-2, 0)
	assert.Equal(t, 4, l.cursor, "an empty list keeps the cursor")
}

func TestListStateKeyPagesByTheWindowLessOne(t *testing.T) {
	var l listState

	assert.True(t, l.key(keyMsg("pgdown"), 30))
	assert.Equal(t, 1, l.cursor, "before the first render a page moves one row")

	l.clamp(30, 8)
	assert.True(t, l.key(keyMsg("pgdown"), 30))
	assert.Equal(t, 8, l.cursor, "a page moves by window-1, so one row of the old page stays")

	l.clamp(30, 0)
	assert.True(t, l.key(keyMsg("pgup"), 30))
	assert.Equal(t, 1, l.cursor, "a key handler clamp keeps the recorded window")

	steps := []struct {
		key    string
		cursor int
	}{
		{"down", 2}, {"j", 3}, {"up", 2}, {"k", 1}, {"end", 29}, {"down", 29}, {"home", 0}, {"up", 0}, {"G", 29}, {"g", 0},
	}

	for _, s := range steps {
		assert.True(t, l.key(keyMsg(s.key), 30), "%s is a movement key", s.key)
		assert.Equal(t, s.cursor, l.cursor, "cursor after %s", s.key)
	}

	assert.False(t, l.key(keyMsg("x"), 30), "x is not a movement key")
	assert.Equal(t, 0, l.cursor)

	l.cursor = 3
	assert.True(t, l.key(keyMsg("down"), 0))
	assert.Equal(t, 3, l.cursor, "an empty list keeps the cursor")
}

func TestScrollStateFitRecordsBoundsAndClamps(t *testing.T) {
	var sc scrollState

	from, to := sc.fit(5, 8)
	assert.Equal(t, [2]int{0, 5}, [2]int{from, to}, "a short text shows every line")
	assert.False(t, sc.scrolls())

	from, to = sc.fit(30, 8)
	assert.Equal(t, [2]int{0, 8}, [2]int{from, to})
	assert.Equal(t, scrollState{off: 0, page: 8, last: 22}, sc)

	sc.off = 20
	from, to = sc.fit(30, 8)
	assert.Equal(t, [2]int{20, 28}, [2]int{from, to}, "an offset inside the bounds stays")

	from, to = sc.fit(12, 8)
	assert.Equal(t, [2]int{4, 12}, [2]int{from, to}, "a shorter text pulls the offset back to the last page")

	from, to = sc.fit(12, 0)
	assert.Equal(t, [2]int{4, 4}, [2]int{from, to}, "a box without rows shows no line")
	assert.Equal(t, scrollState{off: 4, page: 0, last: 12}, sc)

	from, to = sc.fit(0, 8)
	assert.Equal(t, [2]int{0, 0}, [2]int{from, to}, "an empty text resets the offset")

	from, to = sc.fit(3, -2)
	assert.Equal(t, [2]int{0, 0}, [2]int{from, to}, "a negative height counts as no rows")
	assert.Equal(t, 0, sc.page)
}

func TestScrollStateRevealKeepsTheRange(t *testing.T) {
	sc := scrollState{off: 10}

	sc.reveal(-1, 40, 5)
	assert.Equal(t, 10, sc.off, "a negative top keeps the offset")

	sc.reveal(12, 13, 5)
	assert.Equal(t, 10, sc.off, "a range in the window keeps the offset")

	sc.reveal(20, 20, 5)
	assert.Equal(t, 16, sc.off, "a line below the window becomes the bottom row")

	sc.reveal(3, 3, 5)
	assert.Equal(t, 3, sc.off, "a line above the window becomes the top row")

	sc.reveal(8, 20, 5)
	assert.Equal(t, 8, sc.off, "in a range taller than the window the top line wins")
}

func TestTextPagerStartsAtTopForANewText(t *testing.T) {
	body := pagerBody(30)

	var p textPager

	p.lines("a", body, 20, 8)
	assert.True(t, p.key(keyMsg("pgdown")))
	assert.Equal(t, []string{"line 9", "line 10"}, p.lines("a", body, 20, 8)[:2], "the same id keeps the offset")

	assert.Equal(t, "line 1", p.lines("b", body, 20, 8)[0], "a new id starts at the top")
}

func TestTextPagerSpanShowsWhenTheTextFits(t *testing.T) {
	th := newTheme(false)

	var p textPager
	assert.Empty(t, p.span(th), "no position before the first render")
	assert.Empty(t, p.foot(th))

	p.lines("a", pagerBody(3), 20, 8)
	assert.Equal(t, th.Faint.Render("1–3 of 3"), p.span(th), "the diff box shows the range of a short text")
	assert.Empty(t, p.foot(th), "a box border shows the range only while the text scrolls")

	p.lines("a", pagerBody(30), 20, 8)
	assert.Equal(t, th.Faint.Render("1–8 of 30"), p.foot(th))

	p.lines("a", nil, 20, 8)
	assert.Equal(t, th.Faint.Render("1–1 of 1"), p.span(th), "an empty text is one empty line")
}

func TestTextPagerKeepsAnOffsetPastTheBottomAfterTheBoxGrows(t *testing.T) {
	th := newTheme(false)
	body := pagerBody(30)

	var p textPager

	p.lines("a", body, 20, 8)
	assert.True(t, p.key(keyMsg("end")))
	assert.Equal(t, 22, p.sc.off)

	// The box grows to 12 rows. The offset stays, because it is not past
	// the last line, so the text ends above the bottom row.
	lines := p.lines("a", body, 20, 12)
	assert.Equal(t, 22, p.sc.off)
	assert.Len(t, lines, 8)
	assert.Equal(t, th.Faint.Render("23–30 of 30"), p.foot(th))

	assert.True(t, p.key(keyMsg("down")))
	assert.Equal(t, 22, p.sc.off, "a down key keeps an offset past the bottom")
	assert.True(t, p.key(keyMsg("pgdown")))
	assert.Equal(t, 22, p.sc.off)

	assert.True(t, p.key(keyMsg("up")))
	assert.Equal(t, 18, p.sc.off, "an up key moves to the last full page")

	// A text shorter than the offset moves the offset to its last page.
	p.sc.off = 29
	p.lines("a", body[:10], 20, 4)
	assert.Equal(t, 6, p.sc.off)
}

func TestTextPagerCutsAWideLineWithoutAnEllipsis(t *testing.T) {
	var p textPager

	lines := p.lines("a", []string{"0123456789abc", "short"}, 10, 4)
	assert.Equal(t, []string{"0123456789", "short"}, lines)

	assert.Len(t, p.lines("a", pagerBody(5), 10, 0), 1, "the window keeps one row")
}

// pagerBody returns n lines "line 1" to "line n".
func pagerBody(n int) []string {
	body := make([]string, n)
	for i := range body {
		body[i] = "line " + itoa(i+1)
	}

	return body
}
