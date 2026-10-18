package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestLineInputRefusesAMultilinePaste(t *testing.T) {
	plain := LineInput{}
	plain.Key(pasteMsg("one\ntwo"))
	assert.Equal(t, "onetwo", plain.Value(), "without the opt-in the lines join, as a filter needs")
	assert.Empty(t, plain.Notice())

	in := LineInput{RefuseMultiline: true}
	in.Set("kept")

	for _, paste := range []string{"one\ntwo", "one\r\ntwo", "one\rtwo"} {
		in.Key(pasteMsg(paste))
		assert.Equal(t, "kept", in.Value(), "paste %q: nothing goes in", paste)
		assert.Equal(t, "This field takes one line, and the input has line breaks, so nothing was added.", in.Notice())
	}

	in.Key(keyMsg("!"))
	assert.Equal(t, "kept!", in.Value())
	assert.Empty(t, in.Notice(), "the next key clears the notice")
}

func TestLineInputRefusesInputPastTheLimit(t *testing.T) {
	plain := LineInput{Limit: 5}
	plain.Key(pasteMsg("abcdefgh"))
	assert.Equal(t, "abcde", plain.Value(), "without the opt-in the limit cuts, as before")

	in := LineInput{Limit: 5, RefuseOverLimit: true}
	in.Key(pasteMsg("abcdefgh"))
	assert.Empty(t, in.Value(), "the opt-in never cuts: the whole paste is refused")
	assert.Equal(t, "This field holds at most 5 characters, so nothing was added.", in.Notice())

	in.Key(pasteMsg("ab\x1bcde"))
	assert.Equal(t, "abcde", in.Value(), "a dropped control does not count against the limit")
	assert.Equal(t, "Left out 1 control or direction character.", in.Notice())

	in.Key(keyMsg("f"))
	assert.Equal(t, "abcde", in.Value())
	assert.NotEmpty(t, in.Notice(), "a typed rune past the limit is refused visibly")
	assert.False(t, in.ReadOnly())
}

func TestLineInputOpensALegacyValueOverTheLimitReadOnly(t *testing.T) {
	th := NewTheme(false)

	plain := LineInput{Limit: 3}
	plain.Set("abcdef")
	assert.False(t, plain.ReadOnly(), "without the opt-in an existing field keeps its behaviour")

	plain.Key(keyMsg("backspace"))
	assert.Equal(t, "abcde", plain.Value())

	in := LineInput{Limit: 3, RefuseOverLimit: true}
	in.Set("abcdef")
	require.True(t, in.ReadOnly())

	for _, k := range []string{"x", "backspace", "delete", "space"} {
		in.Key(keyMsg(k))
		assert.Equal(t, "abcdef", in.Value(), "key %s changes no rune", k)
		assert.Equal(t, "This text is longer than 3 characters, so it cannot change here.", in.Notice())
	}

	in.Key(tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Equal(t, "abcdef", in.Value())

	in.Key(keyMsg("left"))
	assert.Equal(t, "abcde|f", ansi.Strip(in.View(th)), "the cursor still moves")
	assert.Empty(t, in.Notice())
}

// TestLineInputMaxRunesNamesTheBoundOfReadOnly checks that MaxRunes is
// the bound that ReadOnly and the notices use. A form names it in the
// reason of a read-only row, so the bound has one owner.
func TestLineInputMaxRunesNamesTheBoundOfReadOnly(t *testing.T) {
	var plain LineInput
	assert.Equal(t, lineInputLimit, plain.MaxRunes(), "a zero Limit keeps the default bound")

	in := LineInput{Limit: 3, RefuseOverLimit: true}
	assert.Equal(t, 3, in.MaxRunes())

	in.Set("abc")
	assert.False(t, in.ReadOnly(), "a value at the bound stays editable")

	in.Set("abcd")
	assert.True(t, in.ReadOnly(), "a value past the bound is read only")
}

func TestLineInputHolds(t *testing.T) {
	in := LineInput{}
	assert.True(t, in.Holds("plain text, 日本"))

	for _, s := range []string{"a\nb", "a\tb", "a\x1bb", "a\u202eb", "a\u009bb"} {
		assert.False(t, in.Holds(s), "%q", s)
	}

	digits := LineInput{Accept: func(r rune) bool { return r >= '0' && r <= '9' }}
	assert.True(t, digits.Holds("730"))
	assert.False(t, digits.Holds("7a"), "the accept filter counts too")
}

func TestLineInputMovesByGraphemeCluster(t *testing.T) {
	th := NewTheme(false)

	var in LineInput

	// The line input shares the editing core of TextArea, so it also
	// steps over a whole cluster.
	in.Set("éx")
	in.Key(keyMsg("left"))
	in.Key(keyMsg("left"))
	assert.Equal(t, "|e\u0301x", ansi.Strip(in.View(th)), "left steps over the whole cluster")

	in.Key(keyMsg("right"))
	in.Key(keyMsg("backspace"))
	assert.Equal(t, "x", in.Value(), "backspace removes the whole cluster")
}

// TestLineInputDropsTheSeparators covers L20 of the gate 7 review. The
// sanitizer keeps U+2028 and U+2029, but a terminal shows them as
// nothing, and the shared text check refuses them. So the input drops
// them, counts them in the notice, and opens such a value read-only.
func TestLineInputDropsTheSeparators(t *testing.T) {
	for _, r := range []rune{'\u2028', '\u2029'} {
		require.Equal(t, string(r), OneLine(string(r)), "the sanitizer keeps %U", r)

		var probe LineInput
		assert.False(t, probe.Holds("a"+string(r)+"b"), "rune %U", r)
	}

	in := LineInput{RefuseMultiline: true, RefuseOverLimit: true}
	in.Key(pasteMsg("rule\u2028two\u2029"))
	assert.Equal(t, "ruletwo", in.Value())
	assert.Equal(t, "Left out 2 control or direction characters.", in.Notice())

	plain := LineInput{}
	plain.Key(pasteMsg("api\u2028"))
	assert.Equal(t, "api", plain.Value(), "a filter drops them silently, as it drops a control")
}

// The direction marks pass the sanitizer, show nothing, and fail the
// shared text check of a pin. So a paste loses them with a notice.
func TestLineInputDropsTheDirectionMarks(t *testing.T) {
	for _, r := range []rune{'\u200e', '\u200f', '\u061c'} {
		require.Equal(t, string(r), OneLine(string(r)), "the sanitizer keeps %U", r)

		var probe LineInput
		assert.False(t, probe.Holds("a"+string(r)+"b"), "rune %U", r)
	}

	in := LineInput{RefuseMultiline: true, RefuseOverLimit: true}
	in.Key(pasteMsg("a\u200eb\u200fc\u061c"))
	assert.Equal(t, "abc", in.Value())
	assert.Equal(t, "Left out 3 control or direction characters.", in.Notice())
}

func TestLineInputReportsWhatItLeavesOut(t *testing.T) {
	plain := LineInput{}
	plain.Key(pasteMsg("rule\twith tab\x1b"))
	assert.Equal(t, "rulewith tab", plain.Value())
	assert.Empty(t, plain.Notice(), "without an opt-in the drop stays silent, as a filter needs")

	// A field with an opt-in never changes a paste silently: a tab would
	// join two words without a word.
	for _, in := range []LineInput{{RefuseMultiline: true}, {RefuseOverLimit: true, Limit: 20}} {
		in.Key(pasteMsg("rule\twith tab\x1b"))
		assert.Equal(t, "rulewith tab", in.Value())
		assert.Equal(t, "Left out 2 control or direction characters.", in.Notice())
	}

	digits := LineInput{RefuseOverLimit: true, Limit: 4, Accept: func(r rune) bool { return r >= '0' && r <= '9' }}
	digits.Key(pasteMsg("7a3\x1b"))
	assert.Equal(t, "73", digits.Value())
	assert.Equal(t, "Left out 1 control or direction character. Left out 1 character that this field does not "+
		"accept.", digits.Notice())

	digits.Key(keyMsg("1"))
	assert.Empty(t, digits.Notice(), "a key that applies in full clears the notice")
}
