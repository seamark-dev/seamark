package lessonreport

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// refKind returns the PinRefError kind of err, or fails the test.
func refKind(t *testing.T, err error) *reviews.PinRefError {
	t.Helper()

	var refErr *reviews.PinRefError

	require.True(t, errors.As(err, &refErr), "want a PinRefError, got %v", err)
	require.ErrorIs(t, err, reviews.ErrPinRef)

	return refErr
}

// TestResolveRef covers the lookup in the current config. Each failure
// carries the PinRefError kind of the config.
func TestResolveRef(t *testing.T) {
	cfg := &reviews.Config{Pin: []reviews.PinRule{
		{Rule: "keep-parser-strict", Region: "internal/parse", Note: "n"},
		{Rule: "frozen-assets", Region: "generated", Note: "n"},
	}}
	identity := func(rule, region string) string {
		return reviews.PinIdentityDigest(reviews.NewPinKey(rule, region, nil))
	}
	current := identity("keep-parser-strict", "internal/parse")

	t.Run("current ref, full and shown", func(t *testing.T) {
		got, err := ResolveRef(cfg, cfg.ShortPinRef(reviews.NewPinKey("keep-parser-strict", "internal/parse", nil)))
		require.NoError(t, err)
		assert.Equal(t, current, got)

		got, err = ResolveRef(cfg, strings.ToUpper(" "+current+" "))
		require.NoError(t, err)
		assert.Equal(t, current, got, "the input rule of the config applies: trimmed and lowercased")
	})

	t.Run("missing ref", func(t *testing.T) {
		_, err := ResolveRef(cfg, "cccccccc")
		refErr := refKind(t, err)
		assert.Equal(t, reviews.PinRefMissing, refErr.Kind)

		// The stable current-reference clause stays when historical
		// inspection is available.
		text := err.Error()
		assert.True(t, strings.HasPrefix(text, "pin reference cccccccc names no pin in .seamark/lessons.yaml;"), text)
		assert.Contains(t, text, "`seamark lessons --verify` lists the current references")
	})

	t.Run("malformed ref", func(t *testing.T) {
		_, err := ResolveRef(cfg, "xyz")
		assert.Equal(t, reviews.PinRefMalformed, refKind(t, err).Kind)
	})

	t.Run("empty config", func(t *testing.T) {
		_, err := ResolveRef(&reviews.Config{}, current[:12])
		assert.Equal(t, reviews.PinRefMissing, refKind(t, err).Kind)

		_, err = ResolveRef(nil, current[:12])
		assert.Equal(t, reviews.PinRefMissing, refKind(t, err).Kind, "a nil config reads as empty")
	})

	t.Run("duplicate entries stay an error", func(t *testing.T) {
		dup := &reviews.Config{Pin: []reviews.PinRule{
			{Rule: "keep-parser-strict", Region: "internal/parse", Note: "a"},
			{Rule: "keep-parser-strict", Region: "internal/parse", Note: "b"},
		}}

		_, err := ResolveRef(dup, current[:12])
		refErr := refKind(t, err)
		assert.Equal(t, reviews.PinRefDuplicateEntries, refErr.Kind)
		assert.Equal(t, []int{1, 2}, refErr.Entries)
	})
}
