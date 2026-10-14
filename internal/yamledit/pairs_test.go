package yamledit

// Tests of the shared top-level helpers: where a new section goes, and
// how an editor compares the keys that it does not change.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSectionStartPlacesANewKeyAtTheDocumentEnd(t *testing.T) {
	doc := parsed(t, "threshold: 2\nmute:\n  - rule: F541\n")

	pos, col, lead := doc.SectionStart()
	assert.Equal(t, 3, pos, "after the last line")
	assert.Equal(t, 0, col)
	assert.Equal(t, []string{""}, lead, "a blank line separates the new block")

	doc = parsed(t, "threshold: 2\n\n")

	pos, _, lead = doc.SectionStart()
	assert.Equal(t, 2, pos)
	assert.Empty(t, lead, "a blank line is already there")

	doc = parsed(t, "  threshold: 2\n")

	_, col, _ = doc.SectionStart()
	assert.Equal(t, 2, col, "at the column of the existing keys")

	doc = parsed(t, "")

	pos, col, lead = doc.SectionStart()
	assert.Equal(t, 0, pos)
	assert.Equal(t, 0, col)
	assert.Empty(t, lead)
}

func TestPairsExceptAndSamePairsCompareTheOtherKeys(t *testing.T) {
	before := parsed(t, "threshold: 2\npin_budget: 3 # keep\nmute: []\npin: [a]\n")
	after := parsed(t, "threshold: 2\npin_budget: 3\nmute: [x]\npin: [a]\n")

	was, at := PairsExcept(before.Root, "mute")
	now, _ := PairsExcept(after.Root, "mute")

	assert.Equal(t, 2, at, "two pairs come before the skipped key")
	require.Len(t, was, 3)

	_, same := SamePairs(was, now)
	assert.True(t, same, "a trailing comment is not a value")

	changed := parsed(t, "threshold: 3\npin_budget: 3\nmute: []\npin: [a]\n")
	now, _ = PairsExcept(changed.Root, "mute")

	key, same := SamePairs(was, now)
	assert.False(t, same)
	assert.Equal(t, "threshold", key)

	fewer := parsed(t, "threshold: 2\nmute: []\npin: [a]\n")
	now, _ = PairsExcept(fewer.Root, "mute")

	key, same = SamePairs(was, now)
	assert.False(t, same)
	assert.Empty(t, key, "a count change names no key")

	pairs, at := PairsExcept(nil, "mute")
	assert.Nil(t, pairs)
	assert.Equal(t, -1, at)
}

func TestLastLineFindsTheLastStartLineOfASubtree(t *testing.T) {
	doc := parsed(t, "pin:\n  - rule: a\n    note: |\n      one\n      two\nmute: []\n")

	_, pin, _ := doc.Section("pin")
	require.NotNil(t, pin)

	assert.Equal(t, 3, LastLine(pin), "the note starts on line 3 and its text goes on below")
	assert.Equal(t, 1, LastLine(doc.Root.Content[0]), "a leaf is its own last line")
}
