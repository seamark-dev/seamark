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

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", Truncate("short", 10))
	assert.Equal(t, "exact", Truncate("exact", 5))
	assert.Equal(t, "long…", Truncate("longer", 5))
	assert.Equal(t, "héll…", Truncate("héllo wörld", 5), "runes, not bytes")
	assert.Equal(t, "", Truncate("anything", 0), "zero budget must not panic")
	assert.Equal(t, "", Truncate("anything", -1))
}
