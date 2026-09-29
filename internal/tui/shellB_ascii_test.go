package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShellBASCIIFrameKeepsTheWidth proves finding 17: every replaced
// symbol is one cell wide on both sides, so a frame keeps its layout.
func TestShellBASCIIFrameKeepsTheWidth(t *testing.T) {
	pairs := asciiFramePairs()
	require.Zero(t, len(pairs)%2)

	for i := 0; i < len(pairs); i += 2 {
		from, to := pairs[i], pairs[i+1]

		assert.Equal(t, 1, lipgloss.Width(from), "symbol %q", from)
		assert.Equal(t, 1, lipgloss.Width(to), "replacement of %q", from)
		assert.Less(t, rune(to[0]), rune(128), "replacement of %q", from)
	}

	th := newTheme(true)
	frame := "│ a — b – c … d × e → f ← g ↑ h ↓ │\n" +
		"› ‹ • · ✓ ✗ ▍ ─ ━ ╭╮╰╯ " + strings.Join(unicodeGlyphs.Spinner, "") + "\n" +
		"\x1b[1mbold — text\x1b[0m"

	out := th.asciiFrame(frame)

	in, got := strings.Split(frame, "\n"), strings.Split(out, "\n")
	require.Len(t, got, len(in))

	for i := range in {
		assert.Equal(t, lipgloss.Width(in[i]), lipgloss.Width(got[i]), "line %d changed its width", i)
	}

	for _, r := range out {
		require.Less(t, r, rune(128), "ASCII frame holds %q", r)
	}

	assert.Contains(t, out, "\x1b[1mbold - text\x1b[0m", "escape sequences stay")
	assert.Equal(t, "60x20 cells", th.asciiFrame("60×20 cells"))
	assert.Equal(t, "a apply~", th.asciiFrame("a apply…"))
}

// TestShellBASCIIFrameKeepsOtherText proves the scope of the safety net:
// only the console's own symbols change; other scripts stay.
func TestShellBASCIIFrameKeepsOtherText(t *testing.T) {
	user := "naïve café 日本語 Ελληνικά ü ß €"

	assert.Equal(t, user, newTheme(true).asciiFrame(user))

	unicode := "│ a — b … ✓"
	assert.Equal(t, unicode, newTheme(false).asciiFrame(unicode), "the Unicode theme changes nothing")
}

// TestShellBASCIIGlyphsAreDistinct proves finding 28: in the ASCII set,
// focus differs from the cursor and the separator differs from the pane
// divider, because without color the glyph is the only signal.
func TestShellBASCIIGlyphsAreDistinct(t *testing.T) {
	g := asciiGlyphs

	assert.Equal(t, "#", g.Focus)
	assert.Equal(t, "/", g.Sep)
	assert.NotEqual(t, g.Focus, g.Cursor)
	assert.NotEqual(t, g.Sep, g.Vertical)
	assert.NotEqual(t, g.Focus, g.BrandLeft)
	assert.NotEqual(t, g.Focus, g.BrandMid)
	assert.NotEqual(t, g.Focus, g.BrandRight)
	assert.NotEqual(t, g.Focus, g.Bullet)

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

	// A focused pane title and a cursor row start with different marks.
	th := newTheme(true)
	title := ansi.Strip(th.paneTitle("Pending proposals", true, 40))
	row := ansi.Strip(th.renderList([]string{"p1 rule"}, &listState{}, 40, 3, true))

	assert.True(t, strings.HasPrefix(title, "# "), "focused title: %q", title)
	assert.True(t, strings.HasPrefix(row, "> "), "cursor row: %q", row)
}
