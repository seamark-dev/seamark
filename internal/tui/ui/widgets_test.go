package ui

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
	assert.Equal(t, "3–12 of 40", NewTheme(false).PositionNote(3, 12, 40))
	assert.Equal(t, "3-12 of 40", NewTheme(true).PositionNote(3, 12, 40))
	assert.Equal(t, "1–1 of 1", NewTheme(false).PositionNote(1, 1, 1))
}

func TestPositionNoteHelpersKeepTheirWords(t *testing.T) {
	for _, tc := range []struct {
		ascii bool
		dash  string
	}{
		{ascii: false, dash: "–"},
		{ascii: true, dash: "-"},
	} {
		th := NewTheme(tc.ascii)

		// RangeNote takes the 0-based first row and the window end.
		assert.Equal(t, "3"+tc.dash+"12 of 40", ansi.Strip(th.RangeNote(2, 12, 40)))

		// ScrollNote clamps the window to the row count and pads left.
		// The note "6–7 of 7" has 8 cells, so 16 cells give 8 spaces.
		assert.Equal(t, strings.Repeat(" ", 8)+"6"+tc.dash+"7 of 7", ansi.Strip(th.ScrollNote(5, 4, 7, 16)))
	}
}
