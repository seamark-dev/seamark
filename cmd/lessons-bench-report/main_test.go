package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRequiresInputs(t *testing.T) {
	err := run(filepath.Join("..", "..", "bench", "claims.yaml"), "-", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no result files")
}

func TestWriteAtomicReplacesReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.md")
	require.NoError(t, writeAtomic(path, []byte("first\n")))
	require.NoError(t, writeAtomic(path, []byte("second\n")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second\n", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestRunRefusesToOverwriteEvidenceOrClaims(t *testing.T) {
	claims := filepath.Join("..", "..", "bench", "claims.yaml")
	input := filepath.Join(t.TempDir(), "results.jsonl")
	require.NoError(t, os.WriteFile(input, []byte("evidence\n"), 0o600))

	for _, out := range []string{input, claims} {
		err := run(claims, out, []string{input})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overwrite source evidence")
	}
}

func TestRejectOutputCollisionDetectsAliases(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	alias := filepath.Join(t.TempDir(), "alias.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("evidence\n"), 0o600))
	require.NoError(t, os.Symlink(source, alias))

	err := rejectOutputCollision(alias, []string{source})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aliases source evidence")
}
