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
