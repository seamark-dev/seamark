package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of the ASCII glyph set of the theme. Each replaced symbol
// keeps its cell width, and only the console's own symbols change.
// Without color, the glyph is the only signal, so the glyphs differ.

// TestASCIIFrameKeepsTheWidth proves finding 17: every replaced
// symbol is one cell wide on both sides, so a frame keeps its layout.
func TestASCIIFrameKeepsTheWidth(t *testing.T) {
	pairs := asciiFramePairs()
	require.Zero(t, len(pairs)%2)

	for i := 0; i < len(pairs); i += 2 {
		from, to := pairs[i], pairs[i+1]

		assert.Equal(t, 1, lipgloss.Width(from), "symbol %q", from)
		assert.Equal(t, 1, lipgloss.Width(to), "replacement of %q", from)
		assert.Less(t, rune(to[0]), rune(128), "replacement of %q", from)
	}

	th := NewTheme(true)
	frame := "│ a — b – c … d × e → f ← g ↑ h ↓ │\n" +
		"› ‹ • · ✓ ✗ ▍ ─ ━ ╭╮╰╯ " + strings.Join(unicodeGlyphs.Spinner, "") + "\n" +
		"\x1b[1mbold — text\x1b[0m"

	out := th.ASCIIFrame(frame)

	in, got := strings.Split(frame, "\n"), strings.Split(out, "\n")
	require.Len(t, got, len(in))

	for i := range in {
		assert.Equal(t, lipgloss.Width(in[i]), lipgloss.Width(got[i]), "line %d changed its width", i)
	}

	for _, r := range out {
		require.Less(t, r, rune(128), "ASCII frame holds %q", r)
	}

	assert.Contains(t, out, "\x1b[1mbold - text\x1b[0m", "escape sequences stay")
	assert.Equal(t, "60x20 cells", th.ASCIIFrame("60×20 cells"))
	assert.Equal(t, "a apply~", th.ASCIIFrame("a apply…"))
}

// TestASCIIFrameKeepsOtherText proves the scope of the safety net:
// only the console's own symbols change; other scripts stay.
func TestASCIIFrameKeepsOtherText(t *testing.T) {
	user := "naïve café 日本語 Ελληνικά ü ß €"

	assert.Equal(t, user, NewTheme(true).ASCIIFrame(user))

	unicode := "│ a — b … ✓"
	assert.Equal(t, unicode, NewTheme(false).ASCIIFrame(unicode), "the Unicode theme changes nothing")
}

// TestASCIIGlyphsAreDistinct proves finding 28: in the ASCII set,
// the separator differs from the pane divider, because without color
// the glyph is the only signal.
func TestASCIIGlyphsAreDistinct(t *testing.T) {
	g := asciiGlyphs

	assert.Equal(t, "/", g.Sep)
	assert.NotEqual(t, g.Sep, g.Vertical)

	// Every ASCII glyph is ASCII.
	v := reflect.ValueOf(g)
	for i := range v.NumField() {
		field := v.Field(i)

		values := []string{}
		if field.Kind() == reflect.String {
			values = append(values, field.String())
		} else {
			values = append(values, field.Interface().([]string)...)
		}

		for _, s := range values {
			for _, r := range s {
				require.Less(t, r, rune(128), "glyph %s holds %q", v.Type().Field(i).Name, r)
			}
		}
	}
}
