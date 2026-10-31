package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitize(t *testing.T) {
	assert.Equal(t, "clean title", Sanitize("clean title"))
	assert.Equal(t, "evil[0;31mred", Sanitize("evil\x1b[0;31mred"),
		"ANSI escape byte stripped, printable remainder kept")
	assert.Equal(t, "tab\tkept", Sanitize("tab\tkept"))
	assert.Equal(t, "nonewlines", Sanitize("no\nnew\rlines"))
	assert.Equal(t, "del", Sanitize("del\x7f"))
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
