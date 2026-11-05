package lessoncheck

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBoundFindingsKeepsWholeLines checks the byte bound of a findings
// text for a client with a limited context channel. A text that fits
// keeps every byte. A longer text keeps the first whole lines that fit
// and ends with a line that counts the rest. The result fits in the
// bound at every size, so the line that counts the rest never pushes
// the text past it.
func TestBoundFindingsKeepsWholeLines(t *testing.T) {
	text := "summary\n" +
		"- unknown/missing-baseline: pin p, check c (no-added-pattern/1): src/a.go, src/b.go\n" +
		"No pre-edit baseline exists for this call.\n" +
		"Check again: seamark lessons --verify --file /r/src/a.go --file /r/src/b.go\n"

	t.Run("a text that fits keeps every byte", func(t *testing.T) {
		assert.Equal(t, text, BoundFindings(text, len(text)))
		assert.Equal(t, text, BoundFindings(text, len(text)+100))
		assert.Empty(t, BoundFindings("", 0), "an empty text stays empty")
	})

	t.Run("a longer text keeps the first lines that fit", func(t *testing.T) {
		room := len("summary\n") + len(cutFindingsLine(4))

		assert.Equal(t, "summary\n"+
			"- and 3 more lines, left out to fit the context limit of the client. Check again: seamark lessons --verify\n",
			BoundFindings(text, room))
		assert.Equal(t, "- and 1 more line, left out to fit the context limit of the client. Check again: seamark lessons --verify\n",
			cutFindingsLine(1), "one line takes the singular")
	})

	t.Run("the result fits in the bound at every size", func(t *testing.T) {
		for maxBytes := len(cutFindingsLine(4)); maxBytes < len(text); maxBytes++ {
			got := BoundFindings(text, maxBytes)
			require.LessOrEqual(t, len(got), maxBytes, "bound %d", maxBytes)

			lines := strings.SplitAfter(got, "\n")
			cut := lines[len(lines)-2]
			assert.True(t, strings.HasPrefix(cut, "- and "), "bound %d ends with the line that counts the rest", maxBytes)

			kept := strings.TrimSuffix(got, cut)
			assert.True(t, strings.HasPrefix(text, kept), "bound %d keeps the first lines of the text", maxBytes)
			assert.NotContains(t, got, "--file", "bound %d never cuts the rerun command", maxBytes)
		}
	})

	t.Run("a last line without a line end counts as a line", func(t *testing.T) {
		long := "summary\nlast line without a line end " + strings.Repeat("x", 200)
		got := BoundFindings(long, len("summary\n")+len(cutFindingsLine(2)))

		assert.Equal(t, "summary\n"+cutFindingsLine(1), got)
	})
}
