package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// The tests of the "a–b of n" position note and of the helpers that
// style it. The Unicode glyph set gets an en dash and the ASCII set a
// hyphen, because a view outside the shell skips the ASCII frame swap.

func TestPositionNoteUsesTheDashOfTheGlyphSet(t *testing.T) {
	assert.Equal(t, "3–12 of 40", newTheme(false).positionNote(3, 12, 40))
	assert.Equal(t, "3-12 of 40", newTheme(true).positionNote(3, 12, 40))
	assert.Equal(t, "1–1 of 1", newTheme(false).positionNote(1, 1, 1))
}

func TestPositionNoteHelpersKeepTheirWords(t *testing.T) {
	for _, tc := range []struct {
		ascii bool
		dash  string
	}{
		{ascii: false, dash: "–"},
		{ascii: true, dash: "-"},
	} {
		th := newTheme(tc.ascii)

		// propRange takes the 0-based first row and the window end.
		assert.Equal(t, "3"+tc.dash+"12 of 40", ansi.Strip(propRange(th, 2, 12, 40)))

		// scrollNote clamps the window to the row count and pads left.
		// The note "6–7 of 7" has 8 cells, so 16 cells give 8 spaces.
		assert.Equal(t, strings.Repeat(" ", 8)+"6"+tc.dash+"7 of 7", ansi.Strip(th.scrollNote(5, 4, 7, 16)))
	}
}
