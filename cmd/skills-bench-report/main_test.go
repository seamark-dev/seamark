package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRefusesToOverwriteEvidence(t *testing.T) {
	dir := t.TempDir()
	claims := filepath.Join(dir, "workflow-claims.yaml")
	rows := filepath.Join(dir, "rows.jsonl")
	activation := filepath.Join(dir, "activation.jsonl")
	prompts := filepath.Join(dir, "prompts.yaml")
	require.NoError(t, os.WriteFile(claims, []byte("schema_version: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(rows, []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(activation, []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(prompts, []byte("schema_version: 1\n"), 0o600))

	for _, out := range []string{claims, rows, activation, prompts} {
		err := run(claims, out, prompts, []string{activation}, []string{rows})
		require.ErrorContains(t, err, "source evidence")
	}
}

func TestRunRequiresValidClaimsAndInputs(t *testing.T) {
	dir := t.TempDir()
	claims := filepath.Join(dir, "workflow-claims.yaml")
	require.NoError(t, os.WriteFile(claims, []byte("schema_version: 1\nclaims: []\n"), 0o600))

	err := run(claims, "-", "", nil, []string{filepath.Join(dir, "rows.jsonl")})
	require.ErrorContains(t, err, "registry is empty")

	err = run(filepath.Join(dir, "missing.yaml"), "-", "", nil, []string{filepath.Join(dir, "rows.jsonl")})
	require.Error(t, err)
}

func TestPathListCollectsRepeatedFlags(t *testing.T) {
	var list pathList
	require.NoError(t, list.Set("a.jsonl"))
	require.NoError(t, list.Set("b.jsonl"))
	require.Error(t, list.Set(" "))
	assert.Equal(t, pathList{"a.jsonl", "b.jsonl"}, list)
	assert.Equal(t, "a.jsonl,b.jsonl", list.String())
}
