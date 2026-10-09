package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

// The tests of wrap. It breaks lines only at spaces, so flags and paths
// stay whole. It breaks a token only when the token is wider than the
// line.

// TestWrapBreaksOnlyAtSpaces proves finding 32: flags and paths
// that fit on a line stay whole.
func TestWrapBreaksOnlyAtSpaces(t *testing.T) {
	assert.Equal(t, "seamark init --client codex\n--approve-tools",
		wrap("seamark init --client codex --approve-tools", 30))

	assert.Equal(t, "Run or apply: seamark init\n--client codex --approve-tools",
		wrap("Run or apply: seamark init --client codex --approve-tools", 30))

	assert.Equal(t, "Seamark adds the pin block below under\npin: in .seamark/lessons.yaml, then",
		wrap("Seamark adds the pin block below under pin: in .seamark/lessons.yaml, then", 40))
}

// TestWrapHardBreaksLongTokens proves the overflow guard: a token
// wider than the line still breaks, so no line is too wide.
func TestWrapHardBreaksLongTokens(t *testing.T) {
	path := "/very/long/path/that/does/not/fit/in/one/line/at/all.go"

	out := wrap("see "+path+" now", 20)
	for _, l := range strings.Split(out, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(l), 20, "line %q", l)
	}

	assert.Equal(t, "see "+path+" now", strings.ReplaceAll(strings.Replace(out, "\n", " ", 1), "\n", ""))
	assert.True(t, strings.HasPrefix(out, "see\n"), "the long token starts on its own line: %q", out)
}

// TestWrapKeepsLineBreaksAndIndentation proves that wrap keeps the
// structure of its input.
func TestWrapKeepsLineBreaksAndIndentation(t *testing.T) {
	assert.Equal(t, "one\n\ntwo", wrap("one\n\ntwo", 10))
	assert.Equal(t, "    code line", wrap("    code line", 20))
	assert.Equal(t, "  alpha beta\ngamma", wrap("  alpha beta gamma", 12))
	assert.Equal(t, "a  b", wrap("a  b", 10), "a space run inside a fitting line stays")
	assert.Equal(t, "alpha\nbeta", wrap("alpha    beta", 7), "spaces at a break go away")
	assert.Empty(t, wrap("text", 0))
}

// TestWrapFitsEveryWidth proves that no output line is wider than
// the limit for a mix of words, flags, paths and wide characters.
func TestWrapFitsEveryWidth(t *testing.T) {
	text := "Run `seamark lessons --extract-triggers` in /work/repo/.seamark/ — 日本語のテキスト " +
		"and a-very-long-hyphenated-token-that-keeps-going plus    spaces."

	for w := 4; w <= 80; w++ {
		for _, l := range strings.Split(wrap(text, w), "\n") {
			assert.LessOrEqual(t, lipgloss.Width(l), w, "width %d: line %q", w, l)
		}
	}
}
