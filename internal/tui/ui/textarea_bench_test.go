package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// areaText returns text of n runes in lines of about 300 runes, as a
// long note that a person pastes. The tests of ui must not import a
// seamark fixture package (TestUIStaysBelowTheConsole). So the text does
// not come from pinfiletest.Note, but it has the same line length.
func areaText(n int) string {
	line := strings.Repeat("Step checks the value before the store uses it. ", 6)

	return strings.Repeat(line+"\n", n/len(line)+1)[:n]
}

// BenchmarkPhase4TextArea measures one key press in the note field of
// the pin form (make tui-measure). A press is the edit and the next
// View, because the console renders a frame after each key. The press alternates between
// a typed rune and backspace, so the text keeps its size. The cursor
// stays at the end of the text, where a person types. The note sizes
// are the note sizes of the Phase 4 fixtures (pinfiletest.Sweep); the
// areas are the minimum and a large terminal.
func BenchmarkPhase4TextArea(b *testing.B) {
	th := NewTheme(false)

	// The two key messages exist before the timed loops, so the loops
	// time the area and not the building of a message.
	keys := [2]tea.KeyMsg{keyMsg("x"), keyMsg("backspace")}

	for _, n := range []int{1000, 10000, 100000} {
		for _, size := range [][2]int{{60, 20}, {200, 50}} {
			b.Run(fmt.Sprintf("note=%d/size=%dx%d", n, size[0], size[1]), func(b *testing.B) {
				var a TextArea

				a.SetValue(areaText(n))

				for i := 0; b.Loop(); i++ {
					a.Key(keys[i%2])

					if a.View(th, size[0], size[1]) == "" {
						b.Fatal("the view is empty")
					}
				}
			})
		}
	}
}
