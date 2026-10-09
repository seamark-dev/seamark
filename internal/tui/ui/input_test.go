package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// The tests of LineInput, the one-line text input. They call the input
// directly, because the Proposals, Pins, Settings and Distill forms all
// edit through it. internal/tui/lessons_keys_test.go checks the cursor
// glyphs.

func TestInputOptions(t *testing.T) {
	th := NewTheme(false)

	var region LineInput
	assert.Equal(t, "_ path, for example internal/api", ansi.Strip(region.View(th)), "zero values keep the region hint")

	days := LineInput{
		Placeholder: "days, for example 730",
		Limit:       4,
		Accept:      func(r rune) bool { return r >= '0' && r <= '9' },
	}
	assert.Equal(t, "_ days, for example 730", ansi.Strip(days.View(th)))

	days.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7a3-0x12")})
	assert.Equal(t, "7301", days.Value(), "the filter drops other runes and the limit stops the value")

	region.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", lineInputLimit+5))})
	assert.Len(t, region.Value(), lineInputLimit, "zero values keep the region limit")
}

func TestLineInputEditingKeys(t *testing.T) {
	th := NewTheme(false)

	var in LineInput

	in.Set("abcd")
	in.Key(tea.KeyMsg{Type: tea.KeyLeft})
	in.Key(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "ab|cd", ansi.Strip(in.View(th)))

	in.Key(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "a|cd", ansi.Strip(in.View(th)), "backspace deletes the rune before the cursor")

	in.Key(tea.KeyMsg{Type: tea.KeyDelete})
	assert.Equal(t, "a|d", ansi.Strip(in.View(th)), "delete removes the rune under the cursor")

	in.Key(tea.KeyMsg{Type: tea.KeyCtrlE})
	assert.Equal(t, "ad_", ansi.Strip(in.View(th)))

	in.Key(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "ad_", ansi.Strip(in.View(th)), "right stops at the end")

	in.Key(tea.KeyMsg{Type: tea.KeyCtrlA})
	assert.Equal(t, "|ad", ansi.Strip(in.View(th)))

	in.Key(tea.KeyMsg{Type: tea.KeyLeft})
	in.Key(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "|ad", ansi.Strip(in.View(th)), "left and backspace stop at the start")

	in.Key(tea.KeyMsg{Type: tea.KeyEnd})
	in.Key(tea.KeyMsg{Type: tea.KeyLeft})
	in.Key(tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Equal(t, "|d", ansi.Strip(in.View(th)), "ctrl+u deletes the text before the cursor")

	in.Key(tea.KeyMsg{Type: tea.KeyHome})
	in.Key(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	assert.Equal(t, " d", in.Value(), "space types a space")
}

func TestLineInputDropsUnsafeRunes(t *testing.T) {
	var in LineInput

	in.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\x1bb\u202ec\nd\te\u009bf\u2066g")})
	assert.Equal(t, "abcdefg", in.Value(),
		"controls, line breaks, tabs, C1 controls and direction marks never reach the value")
}
