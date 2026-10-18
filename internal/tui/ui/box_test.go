package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of Theme.Modal. A modal scrolls its body and never its tail,
// so the actions and the bottom border stay in view at every size. The
// maintenance previews of Lesson › Pins depend on this, because their
// scope summary sits in the tail.

func TestModalScrollsItsBodyAndKeepsTheActions(t *testing.T) {
	th := NewTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i+1)
	}

	var sc ScrollState

	o := ModalOpts{
		Title: "Preview", Right: "draft", Body: body,
		Actions: []key.Binding{WithHelp(Nav.Back, "esc", "close")},
	}

	lines := strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")

	// 20 rows lose the margin, the border and the three action rows.
	require.Len(t, lines, 13+3+2)
	assert.Equal(t, 80, Width(lines[0]), "the modal is 80 cells wide at 120 columns")
	assert.Contains(t, lines[0], "Preview")
	assert.Contains(t, lines[0], "draft")
	assert.Contains(t, lines[1], "line 1 ")
	assert.Contains(t, lines[len(lines)-2], "esc close", "the actions stay under the body")
	assert.Contains(t, lines[len(lines)-1], "1–13 of 30")
	assert.Equal(t, 13, sc.page)
	assert.Equal(t, 17, sc.Last)

	sc.by(100)
	lines = strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[13], "line 30", "the offset stops at the last page")
	assert.Contains(t, lines[len(lines)-1], "18–30 of 30")

	// A smaller floor gives a narrower modal on a mid-size terminal.
	o.MinW = 64
	assert.Equal(t, 64, Width(strings.Split(ansi.Strip(th.Modal(o, nil, 90, 20)), "\n")[0]))
	assert.Equal(t, 76, ModalBodyWidth(0, 120), "the body width is the inner width of the box")
}

// A form in a modal moves its cursor below the window. Follow scrolls
// the body to the kept lines; a modal without Follow keeps its offset.
func TestModalFollowShowsTheKeptLines(t *testing.T) {
	th := NewTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i+1)
	}

	var sc ScrollState

	o := ModalOpts{Title: "Form", Body: body, Actions: []key.Binding{WithHelp(Nav.Back, "esc", "close")}}

	lines := strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[len(lines)-1], "1–13 of 30", "a new modal starts at the top")

	o.Follow, o.Keep = true, [2]int{19, 20}
	lines = strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[len(lines)-1], "9–21 of 30", "the kept lines end the window")
	assert.Contains(t, lines[13], "line 21")

	o.Keep = [2]int{2, 2}
	lines = strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[1], "line 3 ", "a kept line above the window starts it")

	o.Follow = false
	o.Keep = [2]int{29, 29}
	lines = strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[1], "line 3 ", "without Follow the offset stays")

	o.Follow, o.Keep = true, [2]int{-1, -1}
	lines = strings.Split(ansi.Strip(th.Modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[1], "line 3 ", "a negative range keeps the offset")
}

func TestModalTailStaysAboveTheActions(t *testing.T) {
	th := NewTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i+1)
	}

	var sc ScrollState

	o := ModalOpts{
		Title: "Remove p16", Body: body,
		Tail:    []string{"Removes 1 pin · p16 › superseded"},
		Actions: []key.Binding{WithHelp(Nav.Back, "esc", "cancel")},
	}

	// The body area of a 60×20 frame is 15 rows under the chrome.
	for _, off := range []int{0, 100} {
		sc.by(off)

		lines := strings.Split(ansi.Strip(th.Modal(o, &sc, 60, 15)), "\n")
		n := len(lines)

		assert.LessOrEqual(t, n, 15-modalMargin, "the modal keeps its margin")
		assert.Contains(t, lines[n-2], "esc cancel", "the actions stay last")
		assert.Contains(t, lines[n-3], "Removes 1 pin · p16 › superseded", "the scope stays above the actions")
		assert.Contains(t, lines[n-4], strings.Repeat(th.Glyphs.RuleLight, 10), "the rule separates the body")
		assert.True(t, sc.Scrolls(), "the body scrolls under the fixed rows")
	}

	// Without actions the scope summary still stays under the rule.
	o.Actions = nil
	lines := strings.Split(ansi.Strip(th.Modal(o, &ScrollState{}, 60, 15)), "\n")
	assert.Contains(t, lines[len(lines)-2], "Removes 1 pin")
}

// Review fix: the scope summary of a modal never scrolls, so a long one
// pushed the actions and the bottom border out of the area.
func TestModalTailKeepsTheActionsAndTheBorder(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		th := NewTheme(ascii)

		tail := make([]string, 30)
		for i := range tail {
			tail[i] = "scope line " + strconv.Itoa(i+1)
		}

		o := ModalOpts{
			Title: "Prune", Body: []string{"line 1", "line 2"}, Tail: tail,
			Actions: []key.Binding{WithHelp(Nav.Back, "esc", "cancel")},
		}

		// The modal area of a 60×20 frame is 19 rows above the footer.
		lines := strings.Split(ansi.Strip(th.Modal(o, &ScrollState{}, 60, 19)), "\n")
		n := len(lines)

		assert.LessOrEqual(t, n, 19-modalMargin, "the modal keeps its margin")
		assert.True(t, strings.HasPrefix(lines[n-1], th.Glyphs.CornerBL), "the bottom border stays: %q", lines[n-1])
		assert.Contains(t, lines[n-2], "esc cancel", "the actions stay last")
		assert.Contains(t, lines[n-3], th.Glyphs.Ellipsis, "a cut summary says that it goes on")
		assert.Contains(t, lines[1], "line 1", "one body row stays")
		assert.Contains(t, strings.Join(lines, "\n"), "scope line 1 ")
	}
}
