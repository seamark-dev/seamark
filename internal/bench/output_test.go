package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAtomicReplacesReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.md")
	require.NoError(t, WriteAtomic(path, []byte("first\n")))
	require.NoError(t, WriteAtomic(path, []byte("second\n")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second\n", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	// The temporary file is renamed or removed, never left beside the
	// report where the next reader would take it for evidence.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "report.md", entries[0].Name())
}

func TestRejectOutputCollisionDetectsAliases(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	alias := filepath.Join(t.TempDir(), "alias.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("evidence\n"), 0o600))
	require.NoError(t, os.Symlink(source, alias))

	err := RejectOutputCollision(alias, []string{source})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aliases source evidence")

	err = RejectOutputCollision(source, []string{source})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overwrite source evidence")

	assert.NoError(t, RejectOutputCollision("-", []string{source}), "stdout collides with nothing")
	assert.NoError(t, RejectOutputCollision(filepath.Join(t.TempDir(), "fresh.md"), []string{source}))
}
