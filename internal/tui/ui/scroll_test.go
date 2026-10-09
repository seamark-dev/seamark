package ui

import (
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// The tests of the position state: ScrollState for a scrolled text,
// TextPager for a long text that a selection picks, and ListState for a
// list with a cursor. They call the types directly, because every
// modal, pane and list moves through them.

func TestScrollStateKeysStayInsideTheRecordedBounds(t *testing.T) {
	sc := ScrollState{page: 5, Last: 12}

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
		assert.True(t, sc.Key(s.msg), "%s is a movement key", s.msg)
		assert.Equal(t, s.off, sc.Off, "offset after %s", s.msg)
	}

	assert.False(t, sc.Key(keyMsg("x")), "x is not a movement key")
	assert.Equal(t, 0, sc.Off, "a key that is not a movement key keeps the offset")
}

func TestScrollStatePagesByOneLineBeforeTheFirstRender(t *testing.T) {
	sc := ScrollState{Last: 3}

	assert.True(t, sc.Key(keyMsg("pgdown")))
	assert.Equal(t, 1, sc.Off, "a zero page moves one line, so a page key always moves")
}

func TestScrollStateByClampsToTheRecordedBounds(t *testing.T) {
	sc := ScrollState{Last: 4}

	sc.by(10)
	assert.Equal(t, 4, sc.Off)

	sc.by(-10)
	assert.Equal(t, 0, sc.Off)

	// A shorter text lowers last. by(0) then pulls the offset back.
	sc = ScrollState{Off: 9, Last: 2}
	sc.by(0)
	assert.Equal(t, 2, sc.Off)
}

func TestScrollStateScrollsOnlyWhenTheTextIsTaller(t *testing.T) {
	var sc ScrollState
	assert.False(t, sc.Scrolls(), "the zero value has nothing to scroll")

	sc.Last = 1
	assert.True(t, sc.Scrolls())
}

func TestListStateClampKeepsTheCursorRowInTheWindow(t *testing.T) {
	l := ListState{Cursor: 10}
	l.Clamp(30, 5)
	assert.Equal(t, ListState{Cursor: 10, Offset: 6, window: 5}, l, "the window moves down as little as possible")

	l.Cursor = 2
	l.Clamp(30, 5)
	assert.Equal(t, ListState{Cursor: 2, Offset: 2, window: 5}, l, "the window moves up as little as possible")

	l = ListState{Cursor: 20, Offset: 18}
	l.Clamp(8, 5)
	assert.Equal(t, ListState{Cursor: 7, Offset: 3, window: 5}, l, "a shorter list pulls the cursor and the window back")

	l = ListState{Cursor: 9, Offset: 4, window: 6}
	l.Clamp(5, 0)
	assert.Equal(t, ListState{Cursor: 4, Offset: 4, window: 6}, l,
		"without a window only the cursor moves, and the recorded window stays")

	l.Clamp(0, 5)
	assert.Equal(t, ListState{window: 5}, l, "an empty list resets the position")
}

func TestListStateMoveStopsAtTheEnds(t *testing.T) {
	var l ListState

	l.Move(-1, 5)
	assert.Equal(t, 0, l.Cursor)

	l.Move(3, 5)
	assert.Equal(t, 3, l.Cursor)

	l.Move(10, 5)
	assert.Equal(t, 4, l.Cursor)

	l.Move(-2, 0)
	assert.Equal(t, 4, l.Cursor, "an empty list keeps the cursor")
}

func TestListStateKeyPagesByTheWindowLessOne(t *testing.T) {
	var l ListState

	assert.True(t, l.Key(keyMsg("pgdown"), 30))
	assert.Equal(t, 1, l.Cursor, "before the first render a page moves one row")

	l.Clamp(30, 8)
	assert.True(t, l.Key(keyMsg("pgdown"), 30))
	assert.Equal(t, 8, l.Cursor, "a page moves by window-1, so one row of the old page stays")

	l.Clamp(30, 0)
	assert.True(t, l.Key(keyMsg("pgup"), 30))
	assert.Equal(t, 1, l.Cursor, "a key handler clamp keeps the recorded window")

	steps := []struct {
		key    string
		cursor int
	}{
		{"down", 2}, {"j", 3}, {"up", 2}, {"k", 1}, {"end", 29}, {"down", 29}, {"home", 0}, {"up", 0}, {"G", 29}, {"g", 0},
	}

	for _, s := range steps {
		assert.True(t, l.Key(keyMsg(s.key), 30), "%s is a movement key", s.key)
		assert.Equal(t, s.cursor, l.Cursor, "cursor after %s", s.key)
	}

	assert.False(t, l.Key(keyMsg("x"), 30), "x is not a movement key")
	assert.Equal(t, 0, l.Cursor)

	l.Cursor = 3
	assert.True(t, l.Key(keyMsg("down"), 0))
	assert.Equal(t, 3, l.Cursor, "an empty list keeps the cursor")
}

func TestScrollStateFitRecordsBoundsAndClamps(t *testing.T) {
	var sc ScrollState

	from, to := sc.Fit(5, 8)
	assert.Equal(t, [2]int{0, 5}, [2]int{from, to}, "a short text shows every line")
	assert.False(t, sc.Scrolls())

	from, to = sc.Fit(30, 8)
	assert.Equal(t, [2]int{0, 8}, [2]int{from, to})
	assert.Equal(t, ScrollState{Off: 0, page: 8, Last: 22}, sc)

	sc.Off = 20
	from, to = sc.Fit(30, 8)
	assert.Equal(t, [2]int{20, 28}, [2]int{from, to}, "an offset inside the bounds stays")

	from, to = sc.Fit(12, 8)
	assert.Equal(t, [2]int{4, 12}, [2]int{from, to}, "a shorter text pulls the offset back to the last page")

	from, to = sc.Fit(12, 0)
	assert.Equal(t, [2]int{4, 4}, [2]int{from, to}, "a box without rows shows no line")
	assert.Equal(t, ScrollState{Off: 4, page: 0, Last: 12}, sc)

	from, to = sc.Fit(0, 8)
	assert.Equal(t, [2]int{0, 0}, [2]int{from, to}, "an empty text resets the offset")

	from, to = sc.Fit(3, -2)
	assert.Equal(t, [2]int{0, 0}, [2]int{from, to}, "a negative height counts as no rows")
	assert.Equal(t, 0, sc.page)
}

func TestScrollStateRevealKeepsTheRange(t *testing.T) {
	sc := ScrollState{Off: 10}

	sc.Reveal(-1, 40, 5)
	assert.Equal(t, 10, sc.Off, "a negative top keeps the offset")

	sc.Reveal(12, 13, 5)
	assert.Equal(t, 10, sc.Off, "a range in the window keeps the offset")

	sc.Reveal(20, 20, 5)
	assert.Equal(t, 16, sc.Off, "a line below the window becomes the bottom row")

	sc.Reveal(3, 3, 5)
	assert.Equal(t, 3, sc.Off, "a line above the window becomes the top row")

	sc.Reveal(8, 20, 5)
	assert.Equal(t, 8, sc.Off, "in a range taller than the window the top line wins")
}

func TestTextPagerStartsAtTopForANewText(t *testing.T) {
	body := pagerBody(30)

	var p TextPager

	p.Lines("a", body, 20, 8)
	assert.True(t, p.Key(keyMsg("pgdown")))
	assert.Equal(t, []string{"line 9", "line 10"}, p.Lines("a", body, 20, 8)[:2], "the same id keeps the offset")

	assert.Equal(t, "line 1", p.Lines("b", body, 20, 8)[0], "a new id starts at the top")
}

func TestTextPagerSpanShowsWhenTheTextFits(t *testing.T) {
	th := NewTheme(false)

	var p TextPager
	assert.Empty(t, p.Span(th), "no position before the first render")
	assert.Empty(t, p.Foot(th))

	p.Lines("a", pagerBody(3), 20, 8)
	assert.Equal(t, th.Faint.Render("1–3 of 3"), p.Span(th), "the diff box shows the range of a short text")
	assert.Empty(t, p.Foot(th), "a box border shows the range only while the text scrolls")

	p.Lines("a", pagerBody(30), 20, 8)
	assert.Equal(t, th.Faint.Render("1–8 of 30"), p.Foot(th))

	p.Lines("a", nil, 20, 8)
	assert.Equal(t, th.Faint.Render("1–1 of 1"), p.Span(th), "an empty text is one empty line")
}

func TestTextPagerKeepsAnOffsetPastTheBottomAfterTheBoxGrows(t *testing.T) {
	th := NewTheme(false)
	body := pagerBody(30)

	var p TextPager

	p.Lines("a", body, 20, 8)
	assert.True(t, p.Key(keyMsg("end")))
	assert.Equal(t, 22, p.sc.Off)

	// The box grows to 12 rows. The offset stays, because it is not past
	// the last line, so the text ends above the bottom row.
	lines := p.Lines("a", body, 20, 12)
	assert.Equal(t, 22, p.sc.Off)
	assert.Len(t, lines, 8)
	assert.Equal(t, th.Faint.Render("23–30 of 30"), p.Foot(th))

	assert.True(t, p.Key(keyMsg("down")))
	assert.Equal(t, 22, p.sc.Off, "a down key keeps an offset past the bottom")
	assert.True(t, p.Key(keyMsg("pgdown")))
	assert.Equal(t, 22, p.sc.Off)

	assert.True(t, p.Key(keyMsg("up")))
	assert.Equal(t, 18, p.sc.Off, "an up key moves to the last full page")

	// A text shorter than the offset moves the offset to its last page.
	p.sc.Off = 29
	p.Lines("a", body[:10], 20, 4)
	assert.Equal(t, 6, p.sc.Off)
}

func TestTextPagerCutsAWideLineWithoutAnEllipsis(t *testing.T) {
	var p TextPager

	lines := p.Lines("a", []string{"0123456789abc", "short"}, 10, 4)
	assert.Equal(t, []string{"0123456789", "short"}, lines)

	assert.Len(t, p.Lines("a", pagerBody(5), 10, 0), 1, "the window keeps one row")
}

// pagerBody returns n lines "line 1" to "line n".
func pagerBody(n int) []string {
	body := make([]string, n)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i+1)
	}

	return body
}
