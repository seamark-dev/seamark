package render

import (
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitize(t *testing.T) {
	assert.Equal(t, "clean title", Sanitize("clean title"))
	assert.Equal(t, "evil[0;31mred", Sanitize("evil\x1b[0;31mred"),
		"ANSI escape byte stripped, printable remainder kept")
	assert.Equal(t, "tab\tkept", Sanitize("tab\tkept"))
	assert.Equal(t, "no newlines", Sanitize("no\nnew\rlines"), "a line break reads as a space, a lone CR is dropped")
	assert.Equal(t, "del", Sanitize("del\x7f"))
}

// TestSanitizeJoinsLinesWithASpace covers multi-line text on a one-line
// surface. A dropped break would join two words, so an inner break reads
// as one space. Breaks at the ends go away and add no space. The end trim
// removes only line breaks, so text without a break keeps its tabs and
// spaces byte for byte.
func TestSanitizeJoinsLinesWithASpace(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{name: "inner break", in: "First line.\nSecond line.", want: "First line. Second line."},
		{name: "CRLF", in: "a\r\nb", want: "a b"},
		{name: "end breaks", in: "\nnote\n", want: "note"},
		{name: "trailing CRLF", in: "note\r\n", want: "note"},
		{name: "only a break", in: "\n", want: ""},
		{name: "scrub before trim", in: "x\n\x1b", want: "x"},
		{name: "tab at the ends stays", in: "\tx\t", want: "\tx\t"},
		{name: "space at the ends stays", in: " x ", want: " x "},
		{name: "tab after the last break stays", in: "x\n\t", want: "x \t"},
		{name: "blank line keeps both spaces", in: "a\n\nb", want: "a  b"},
		{name: "tab after a break stays", in: "a\n\tb", want: "a \tb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Sanitize(tc.in))
		})
	}
}

// TestSanitizeIsSanitizeBlockOnOneLine keeps the two sanitizers on one
// rune rule. Sanitize must equal the line join of SanitizeBlock, hold no
// line break, and never grow the text, because callers cut after it.
func TestSanitizeIsSanitizeBlockOnOneLine(t *testing.T) {
	join := func(s string) string {
		return strings.ReplaceAll(strings.Trim(s, "\n"), "\n", " ")
	}

	for _, in := range []string{
		"a\x00\nb\x1f", "\x7f\nx\n", "c1\u0085\nnext\u009b2J", "\u202ab\nidi\u202e",
		"\u2066iso\n\u2069late", "\r\n\x1b[31m\r\n", "plain\ttab\nline", "",
	} {
		got := Sanitize(in)

		assert.Equal(t, join(SanitizeBlock(in)), got, "input %q", in)
		assert.NotContains(t, got, "\n", "input %q", in)
		assert.NotContains(t, got, "\r", "input %q", in)
		assert.LessOrEqual(t, utf8.RuneCountInString(got), utf8.RuneCountInString(in), "input %q", in)
	}
}

func TestSanitizeStripsC1AndBidiControls(t *testing.T) {
	assert.Equal(t, "2Jevilx", Sanitize("\u009b2J\u202eevil\u2066x\u2069"))
	assert.Equal(t, "héllo wörld", Sanitize("héllo wörld"), "printable text outside ASCII stays")
}

func TestCutBytes(t *testing.T) {
	assert.Equal(t, "short", CutBytes("short", 10))
	assert.Equal(t, "exact", CutBytes("exact", 5), "a text that fits is never cut")
	assert.Equal(t, "lon", CutBytes("longer", 3))
	assert.Equal(t, "", CutBytes("anything", 0))
	assert.Equal(t, "é", CutBytes("éa", 2), "the limit ends on a rune boundary")
	assert.Equal(t, "é", CutBytes("éé", 3), "the cut moves back to the rune start")
	assert.Equal(t, "", CutBytes("日本", 2), "the first rune does not fit")
}

func TestCutBytesOnInvalidUTF8(t *testing.T) {
	assert.Equal(t, "a", CutBytes("ab\x80\x80\x80\x80", 4), "the rune start stops the walk back")
	assert.Equal(t, "", CutBytes("\x80\x80\x80\x80\x80\x80\x80\x80", 7),
		"the walk back has no cap, so no rune start cuts to nothing")
	assert.Equal(t, "ab\xff", CutBytes("ab\xffcd", 3), "an invalid lead byte counts as a rune start")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", Truncate("short", 10))
	assert.Equal(t, "exact", Truncate("exact", 5))
	assert.Equal(t, "long…", Truncate("longer", 5))
	assert.Equal(t, "héll…", Truncate("héllo wörld", 5), "runes, not bytes")
	assert.Equal(t, "", Truncate("anything", 0), "zero budget must not panic")
	assert.Equal(t, "", Truncate("anything", -1))
}

func TestSanitizeBlockKeepsLinesAndStripsControls(t *testing.T) {
	in := "line one\n\tline two\x1b[31m red\x07\r\u009b2J\u202eevil\u2066x\u2069\x7f"

	assert.Equal(t, "line one\n\tline two[31m red2Jevilx", SanitizeBlock(in))
}

// TestShellWordGivesOneWordInARealShell covers each kind of path. A plain
// path stays as it is, so normal output does not change. Any other path
// must reach a real shell as one word with the same text.
func TestShellWordGivesOneWordInARealShell(t *testing.T) {
	for _, plain := range []string{"/a/b.db", "/tmp/x-y_z.1/index.db", "/Users/me/.seamark/index.db", "/дом/x.db"} {
		assert.Equal(t, plain, ShellWord(plain), "a plain path stays as it is")
	}

	for _, path := range []string{
		"/my repo/x.db", "/it's/x.db", `/a"b/x.db`, "/$HOME/x.db", "/a;b/x.db", "/a*b/x.db", "/a`b/x.db", `/a\b/x.db`, "/a\tb/x.db",
	} {
		word := ShellWord(path)
		assert.NotEqual(t, path, word)

		out, err := exec.Command("sh", "-c", "printf %s "+word).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Equal(t, path, string(out))
	}

	assert.Equal(t, "''", ShellWord(""), "an empty word stays a word")
	assert.Equal(t, "/a/b.db", ShellWord("/a/b\x1b.db"), "the text passes Sanitize")
}
