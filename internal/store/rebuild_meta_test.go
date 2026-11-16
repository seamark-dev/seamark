package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRebuildMetaCommitsWithTheGraph proves that a meta value written in
// the rebuild transaction commits and rolls back with the graph. The index
// run records the workspace state this way, so a failed rebuild keeps the
// state of the graph that stays.
func TestRebuildMetaCommitsWithTheGraph(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.Rebuild(func(tx *Tx) error {
		return tx.SetMeta(MetaIndexedState, "first")
	}))

	got, err := s.GetMeta(MetaIndexedState)
	require.NoError(t, err)
	assert.Equal(t, "first", got)

	boom := errors.New("boom")

	err = s.Rebuild(func(tx *Tx) error {
		require.NoError(t, tx.SetMeta(MetaIndexedState, "second"))

		return boom
	})
	require.ErrorIs(t, err, boom)

	got, err = s.GetMeta(MetaIndexedState)
	require.NoError(t, err)
	assert.Equal(t, "first", got, "a failed rebuild keeps the recorded state")
}
