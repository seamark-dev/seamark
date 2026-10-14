package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of TextArea, the multi-line text input. They call the area
// directly, as the LineInput tests do, because the forms of the console
// edit through it.

// areaView renders a without styles.
func areaView(a *TextArea, w, h int) string {
	return ansi.Strip(a.View(NewTheme(false), w, h))
}

// assertAreaFits checks that every row of a view fits w cells and that
// the view has at most h rows.
func assertAreaFits(t *testing.T, view string, w, h int) {
	t.Helper()

	lines := strings.Split(view, "\n")
	require.LessOrEqual(t, len(lines), h, "the view is taller than its window")

	for _, l := range lines {
		assert.LessOrEqual(t, ansi.StringWidth(l), w, "row %q is wider than %d cells", l, w)
	}
}

func TestTextAreaEnterInsertsALineBreak(t *testing.T) {
	var a TextArea

	for _, k := range []string{"o", "n", "e", "enter", "t", "w", "o", "ctrl+j", "x", "alt+enter", "y"} {
		a.Key(keyMsg(k))
	}

	assert.Equal(t, "one\ntwo\nx\ny", a.Value(), "enter, ctrl+j and alt+enter insert a line break")
	assert.Equal(t, "one\ntwo\nx\ny_", areaView(&a, 20, 10))

	a.Key(keyMsg("tab"))
	assert.Equal(t, "one\ntwo\nx\ny\t", a.Value(), "a tab key inserts a tab")
	assert.Empty(t, a.Notice())
}

func TestTextAreaPasteKeepsLineBreaksAndTabs(t *testing.T) {
	var a TextArea

	a.Key(pasteMsg("first\r\nsecond\rthird\nfourth\tend"))
	assert.Equal(t, "first\nsecond\nthird\nfourth\tend", a.Value(),
		"CR LF and a lone CR become LF, and a tab stays in the value")
	assert.Empty(t, a.Notice(), "nothing went away")

	// A tab shows as the four spaces of MultiLine, so the width
	// arithmetic stays exact.
	assert.Equal(t, "first\nsecond\nthird\nfourth    end_", areaView(&a, 40, 10))
}

func TestTextAreaDropsWhatMultiLineDrops(t *testing.T) {
	var a TextArea

	a.Key(pasteMsg("a\x1b[31mb\u202ec\u009bd\x00e\x7ff\u2066g"))
	assert.Equal(t, "a[31mbcdefg", a.Value(), "ESC, bidi, C1, NUL and DEL never reach the value")
	assert.Equal(t, "Left out 6 control or direction characters.", a.Notice())

	a.Key(keyMsg("h"))
	assert.Empty(t, a.Notice(), "the next key clears the notice")

	a.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0x1b}})
	assert.Equal(t, "a[31mbcdefgh", a.Value())
	assert.Equal(t, "Left out 1 control or direction character.", a.Notice(), "a typed control is reported too")

	// The rune rule is the rule of MultiLine: a rune stays when MultiLine
	// keeps it, and a tab stays although MultiLine draws it as spaces.
	// The two separators are the exception (TestTextAreaDropsTheSeparators).
	for _, r := range []rune{'\t', '\n', ' ', 'x', 'é', '日', 0x00, 0x1b, '\r', 0x7f, 0x85, 0x9b, 0x202a, 0x202e, 0x2066, 0x2069} {
		s := string(r)
		want := r == '\t' || MultiLine(s) == s

		assert.Equal(t, want, a.Holds(s), "rune %U", r)
	}
}

// TestTextAreaDropsTheSeparators covers L20 of the gate 7 review. The
// sanitizer keeps U+2028 and U+2029, but a terminal shows them as
// nothing, and the shared text check refuses them. So the area drops
// them and counts them in the notice.
func TestTextAreaDropsTheSeparators(t *testing.T) {
	var a TextArea

	for _, r := range []rune{'\u2028', '\u2029'} {
		require.Equal(t, string(r), MultiLine(string(r)), "the sanitizer keeps %U", r)
		assert.False(t, a.Holds("a"+string(r)+"b"), "rune %U", r)
	}

	a.Key(pasteMsg("one\u2028two\u2029three"))
	assert.Equal(t, "onetwothree", a.Value())
	assert.Equal(t, "Left out 2 control or direction characters.", a.Notice())

	a.SetValue("x\u2028y")
	assert.Equal(t, "xy", a.Value())
	assert.Equal(t, "Left out 1 control or direction character.", a.Notice())
}

// The area drops the direction marks as LineInput does.
func TestTextAreaDropsTheDirectionMarks(t *testing.T) {
	var a TextArea

	for _, r := range []rune{'\u200e', '\u200f', '\u061c'} {
		require.Equal(t, string(r), MultiLine(string(r)), "the sanitizer keeps %U", r)
		assert.False(t, a.Holds("a"+string(r)+"b"), "rune %U", r)
	}

	a.Key(pasteMsg("one\u200etwo\nthree\u061c"))
	assert.Equal(t, "onetwo\nthree", a.Value())
	assert.Equal(t, "Left out 2 control or direction characters.", a.Notice())
}

func TestTextAreaKeepsALongPasteWhole(t *testing.T) {
	var b strings.Builder

	for b.Len() < 40000 {
		b.WriteString("word 日本\tlong-token_without_spaces_that_wraps_at_the_cell_limit\n")
	}

	text := string([]rune(b.String())[:10000])
	require.Len(t, []rune(text), 10000)

	var a TextArea

	a.Key(pasteMsg(text))
	assert.Equal(t, text, a.Value(), "a 10k-rune paste is not cut")
	assert.Empty(t, a.Notice())

	for _, size := range [][2]int{{60, 20}, {10, 5}, {200, 50}} {
		assertAreaFits(t, areaView(&a, size[0], size[1]), size[0], size[1])
	}
}

func TestTextAreaWrapsAndRevealsTheCursorAtWidth10(t *testing.T) {
	var a TextArea

	a.SetValue("the quick brown fox jumps over the lazy dog")

	// A row breaks after its last space and keeps the space, so the
	// cursor can stand on it. One cell stays free for the cursor glyph.
	view := areaView(&a, 10, 3)
	assert.Equal(t, "jumps \nover the \nlazy dog_", view, "the window follows the cursor at the end")
	assertAreaFits(t, view, 10, 3)

	a.Key(keyMsg("up"))
	assert.Equal(t, "jumps \nover the| \nlazy dog", areaView(&a, 10, 3), "up keeps the display column")

	a.Key(keyMsg("up"))
	assert.Equal(t, "jumps| \nover the \nlazy dog", areaView(&a, 10, 3), "a short row stops at its end")

	a.Key(keyMsg("down"))
	assert.Equal(t, "jumps \nover the| \nlazy dog", areaView(&a, 10, 3), "down returns to the goal column")

	a.Key(tea.KeyMsg{Type: tea.KeyCtrlA})
	assert.Equal(t, "|the \nquick \nbrown ", areaView(&a, 10, 3), "the window follows the cursor to the top")

	// The whole text keeps every rune: the rows join to the value.
	a.Key(keyMsg("pgdown"))
	a.Key(keyMsg("pgdown"))
	a.Key(keyMsg("pgdown"))
	assert.Equal(t, len([]rune(a.Value())), a.pos, "a move past the last row goes to the end")

	all := strings.ReplaceAll(strings.ReplaceAll(areaView(&a, 10, 20), "\n", ""), "_", "")
	assert.Equal(t, a.Value(), all)

	// A word wider than a row breaks at the cell limit.
	a.SetValue("abcdefghijklmnopqrstuvwxyz")
	assert.Equal(t, "abcdefghi\njklmnopqr\nstuvwxyz_", areaView(&a, 10, 5))

	// A wide rune counts two cells.
	a.SetValue("日本語テキスト")
	view = areaView(&a, 6, 5)
	assert.Equal(t, "日本\n語テ\nキス\nト_", view)
	assertAreaFits(t, view, 6, 5)
}

func TestTextAreaWrapsAndRevealsTheCursorAtWidth200(t *testing.T) {
	var a TextArea

	long := strings.Repeat("lorem ipsum ", 30)
	a.SetValue("short line\n" + long + "\nlast")

	view := areaView(&a, 200, 3)
	assertAreaFits(t, view, 200, 3)

	lines := strings.Split(view, "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, "last_", lines[2], "the cursor row shows at the bottom")
	assert.Equal(t, "lorem ipsum ", lines[1][len(lines[1])-12:], "the long line wraps after a space")

	a.Key(keyMsg("up"))
	a.Key(keyMsg("up"))
	a.Key(keyMsg("up"))
	assert.Equal(t, "shor|t line", strings.Split(areaView(&a, 200, 3), "\n")[0],
		"up keeps column 4 through the wrapped rows")
}

func TestTextAreaMovesTheCursorByLines(t *testing.T) {
	var a TextArea

	a.SetValue("one\ntwo\nthree")

	// Before the first View the area knows no width, so up and down
	// move by lines.
	a.Key(keyMsg("home"))
	assert.Equal(t, 8, a.pos, "home goes to the start of the line")

	a.Key(tea.KeyMsg{Type: tea.KeyCtrlE})
	assert.Equal(t, 13, a.pos, "ctrl+e goes to the end of the line")

	a.Key(keyMsg("up"))
	assert.Equal(t, 7, a.pos, "up stops at the end of a shorter line")

	a.Key(keyMsg("up"))
	a.Key(keyMsg("up"))
	assert.Equal(t, 0, a.pos, "up on the first line goes to the start")

	a.Key(keyMsg("left"))
	assert.Equal(t, 0, a.pos, "left stops at the start")

	a.Key(keyMsg("right"))
	a.Key(keyMsg("end"))
	assert.Equal(t, 3, a.pos, "end stops before the line break")

	a.Key(keyMsg("delete"))
	assert.Equal(t, "onetwo\nthree", a.Value(), "delete at the end of a line joins the lines")

	a.Key(keyMsg("down"))
	a.Key(tea.KeyMsg{Type: tea.KeyCtrlA})
	a.Key(keyMsg("backspace"))
	assert.Equal(t, "onetwothree", a.Value(), "backspace at the start of a line joins the lines")

	a.SetValue("keep\ndrop this")
	a.Key(keyMsg("left"))
	a.Key(tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Equal(t, "keep\ns", a.Value(), "ctrl+u deletes from the start of the line")
}

func TestTextAreaSetValueReportsWhatItCannotHold(t *testing.T) {
	var a TextArea

	assert.True(t, a.Holds("a\tb\nc"))
	assert.False(t, a.Holds("a\rb"), "a CR is a control; only typed input turns it into a line break")
	assert.False(t, a.Holds("a\u202eb"))

	a.SetValue("a\u202eb\x1bc")
	assert.Equal(t, "abc", a.Value())
	assert.Equal(t, "Left out 2 control or direction characters.", a.Notice())

	// A new value forgets the goal column of the old one.
	a.SetValue("abcdef\nxy\nabcdef")
	a.Key(keyMsg("up"))
	a.Key(keyMsg("up"))
	require.Equal(t, 6, a.pos, "up keeps column 6 from the last line")

	a.SetValue("abcdefgh\nab")
	a.Key(keyMsg("up"))
	assert.Equal(t, 2, a.pos, "up after SetValue starts from the cursor column 2, not the old column 6")

	a.SetValue("")
	assert.Empty(t, a.Notice())
	assert.Equal(t, "_", areaView(&a, 10, 3))

	a.Placeholder = "free text, for example why"
	assert.Equal(t, "_ free t…", areaView(&a, 9, 3), "the hint is cut to the width")
	assert.Empty(t, areaView(&a, 0, 3))
}

func TestTextAreaMeasuresGraphemeClusters(t *testing.T) {
	var a TextArea

	// A variation selector makes the warning sign two cells wide. The
	// box measures per grapheme cluster, so the area must too, or the box
	// cuts the row end and hides the text and the cursor.
	a.Key(pasteMsg("⚠️ " + strings.Repeat("x", 36) + " tail"))
	a.Key(keyMsg("home"))

	view := areaView(&a, 40, 10)
	assertAreaFits(t, view, 40, 10)
	assert.Equal(t, "|⚠️ \n"+strings.Repeat("x", 36)+" \ntail", view,
		"the bar comes before the whole cluster, and the sign takes two cells, so the word moves down")

	// Nine red hearts are 18 cells, so a row of 10 cells holds four.
	a.SetValue(strings.Repeat("❤️", 9))
	view = areaView(&a, 10, 5)
	assertAreaFits(t, view, 10, 5)
	assert.Equal(t, "❤️❤️❤️❤️\n❤️❤️❤️❤️\n❤️_", view)
}

func TestTextAreaMovesByGraphemeCluster(t *testing.T) {
	var a TextArea

	// e plus a combining acute accent is one character on the screen.
	a.SetValue("éx")
	a.Key(keyMsg("left"))
	assert.Equal(t, "e\u0301|x", areaView(&a, 10, 3), "left steps over x")

	a.Key(keyMsg("left"))
	assert.Equal(t, "|e\u0301x", areaView(&a, 10, 3), "left steps over the whole cluster")

	a.Key(keyMsg("right"))
	assert.Equal(t, "e\u0301|x", areaView(&a, 10, 3), "right steps over the whole cluster")

	a.Key(keyMsg("backspace"))
	assert.Equal(t, "x", a.Value(), "backspace removes the whole cluster, so no orphan accent stays")

	a.SetValue("⚠️x")
	a.Key(keyMsg("left"))
	a.Key(keyMsg("left"))
	a.Key(keyMsg("delete"))
	assert.Equal(t, "x", a.Value(), "delete removes the sign and its variation selector")

	// A typed letter can join an accent after the cursor. The cursor
	// then goes after the new cluster, never inside it.
	a.SetValue("́x")
	a.Key(keyMsg("home"))
	a.Key(keyMsg("a"))
	assert.Equal(t, "áx", a.Value())
	assert.Equal(t, "a\u0301|x", areaView(&a, 10, 3))

	// up and down keep the display column in cells, per cluster.
	a.SetValue("⚠️⚠️ab\nabcdef")
	a.Key(keyMsg("up"))
	assert.Equal(t, "⚠️⚠️ab_\nabcdef", areaView(&a, 20, 3), "column 6 is the end of the first line")

	a.Key(keyMsg("down"))
	a.Key(keyMsg("left"))
	a.Key(keyMsg("left"))
	a.Key(keyMsg("up"))
	assert.Equal(t, "⚠️⚠️|ab\nabcdef", areaView(&a, 20, 3), "column 4 is after the two signs")
}

func TestTextAreaFitsFromFiveCells(t *testing.T) {
	var a TextArea

	// A tab takes four cells and the cursor one, so five cells is the
	// narrowest width at which every row fits.
	a.SetValue("a\tb日本⚠️\tc d")

	for _, k := range []string{"", "home", "right", "right", "right"} {
		if k != "" {
			a.Key(keyMsg(k))
		}

		assertAreaFits(t, areaView(&a, 5, 20), 5, 20)
	}
}
