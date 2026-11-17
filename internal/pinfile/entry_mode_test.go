package pinfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestEntryCarriesModeAndCheck proves that the listing reads the
// lesson-check keys. mode and check are reader keys now, so the listing
// never calls them unknown, and the entry carries their raw text. The
// pin key ignores both, because the firing identity of a pin must not
// change. The top-level checks and lesson_checks_version keys block no
// edit.
func TestEntryCarriesModeAndCheck(t *testing.T) {
	const body = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: plain
    region: api
    note: n
  - rule: odd
    region: db
    note: n
    mode: sometimes
    check: missing
    enabled: false
`

	d := Parse([]byte(body))

	require.NoError(t, d.DecodeErr)
	require.NoError(t, d.ReaderErr)
	assert.Empty(t, d.FileBlock, "the lesson-check keys block no edit")
	require.Len(t, d.Entries, 3)

	bound := entryOf(t, d, "frozen-assets")
	assert.Equal(t, "enforce", bound.Mode)
	assert.Equal(t, "frozen-assets", bound.Check)
	assert.Empty(t, bound.Unknown, "mode and check are reader keys")
	assert.Empty(t, bound.Block)
	assert.Equal(t, reviews.NewPinKey("frozen-assets", "generated/", nil), bound.Key)
	assert.Contains(t, bound.Raw, "    mode: enforce\n    check: frozen-assets")

	plain := entryOf(t, d, "plain")
	assert.Empty(t, plain.Mode, "an absent key reads as empty text")
	assert.Empty(t, plain.Check)
	assert.Empty(t, plain.Unknown)

	// The listing does not validate the values; lessoncheck.Validate
	// owns that rule. A key outside pinFields stays unknown.
	odd := entryOf(t, d, "odd")
	assert.Equal(t, "sometimes", odd.Mode)
	assert.Equal(t, "missing", odd.Check)
	assert.Equal(t, []string{"enabled"}, odd.Unknown)
	assert.Empty(t, odd.Block)

	// The reader and the listing agree on the new fields.
	cfg, err := reviews.ParseConfig([]byte(body))
	require.NoError(t, err)
	require.Len(t, cfg.Pin, len(d.Entries))

	for i, e := range d.Entries {
		assert.Equal(t, cfg.Pin[i].Mode, e.Mode, "entry %d", i)
		assert.Equal(t, cfg.Pin[i].Check, e.Check, "entry %d", i)
	}
}
