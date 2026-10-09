package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// The tests of lineInput, the one-line text input. They call the input
// directly, because the Proposals, Pins, Settings and Distill forms all
// edit through it. lessons_keys_test.go checks the cursor glyphs.

func TestInputOptions(t *testing.T) {
	th := newTheme(false)

	var region lineInput
	assert.Equal(t, "_ path, for example internal/api", ansi.Strip(region.view(th)), "zero values keep the region hint")

	days := lineInput{
		placeholder: "days, for example 730",
		limit:       4,
		accept:      func(r rune) bool { return r >= '0' && r <= '9' },
	}
	assert.Equal(t, "_ days, for example 730", ansi.Strip(days.view(th)))

	days.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7a3-0x12")})
	assert.Equal(t, "7301", days.value(), "the filter drops other runes and the limit stops the value")

	region.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", lineInputLimit+5))})
	assert.Len(t, region.value(), lineInputLimit, "zero values keep the region limit")
}

func TestLineInputEditingKeys(t *testing.T) {
	th := newTheme(false)

	var in lineInput

	in.set("abcd")
	in.key(tea.KeyMsg{Type: tea.KeyLeft})
	in.key(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "ab|cd", ansi.Strip(in.view(th)))

	in.key(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "a|cd", ansi.Strip(in.view(th)), "backspace deletes the rune before the cursor")

	in.key(tea.KeyMsg{Type: tea.KeyDelete})
	assert.Equal(t, "a|d", ansi.Strip(in.view(th)), "delete removes the rune under the cursor")

	in.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	assert.Equal(t, "ad_", ansi.Strip(in.view(th)))

	in.key(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "ad_", ansi.Strip(in.view(th)), "right stops at the end")

	in.key(tea.KeyMsg{Type: tea.KeyCtrlA})
	assert.Equal(t, "|ad", ansi.Strip(in.view(th)))

	in.key(tea.KeyMsg{Type: tea.KeyLeft})
	in.key(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "|ad", ansi.Strip(in.view(th)), "left and backspace stop at the start")

	in.key(tea.KeyMsg{Type: tea.KeyEnd})
	in.key(tea.KeyMsg{Type: tea.KeyLeft})
	in.key(tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Equal(t, "|d", ansi.Strip(in.view(th)), "ctrl+u deletes the text before the cursor")

	in.key(tea.KeyMsg{Type: tea.KeyHome})
	in.key(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	assert.Equal(t, " d", in.value(), "space types a space")
}

func TestLineInputDropsUnsafeRunes(t *testing.T) {
	var in lineInput

	in.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\x1bb\u202ec\nd\te\u009bf\u2066g")})
	assert.Equal(t, "abcdefg", in.value(),
		"controls, line breaks, tabs, C1 controls and direction marks never reach the value")
}
