package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests of the first-run verdict of the production engine. The
// verdict decides whether the console shows the clean-install welcome.

// engineFresh resolves dir the way `seamark tui` does and returns the
// first-run verdict of the production engine.
func engineFresh(t *testing.T, dir, db string) bool {
	t.Helper()

	ws, err := workspace.Resolve(dir, db)
	require.NoError(t, err)

	fresh, err := New(ws, "test", "/usr/local/bin/seamark").Fresh()
	require.NoError(t, err)

	return fresh
}

// writeSmallFile creates rel under root with a small content.
func writeSmallFile(t *testing.T, root, rel string) {
	t.Helper()

	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x: 1\n"), 0o600))
}

// TestFreshSeesAnySeamarkState proves finding 20: any entry of the
// .seamark directory, or an existing database path, is Seamark state, so
// the console does not show the clean-install welcome.
func TestFreshSeesAnySeamarkState(t *testing.T) {
	assert.True(t, engineFresh(t, t.TempDir(), ""), "an empty directory is fresh")

	for _, rel := range []string{
		".seamark/effects.yaml",
		".seamark/lessons-audit.jsonl",
		".seamark/config.yaml",
	} {
		root := t.TempDir()
		writeSmallFile(t, root, rel)
		assert.False(t, engineFresh(t, root, ""), "%s is Seamark state", rel)
	}

	empty := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(empty, ".seamark"), 0o755))
	assert.False(t, engineFresh(t, empty, ""), "an empty .seamark directory is partial state")

	// The probe follows links and calls a dangling link absent. The
	// database path still exists, so the workspace is not fresh.
	linked := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.Symlink(filepath.Join(linked, "missing.db"), db))
	assert.False(t, engineFresh(t, linked, db), "an existing database path is Seamark state")

	// A generic agent directory without a Seamark marker stays fresh.
	generic := t.TempDir()
	writeSmallFile(t, generic, ".claude/notes.md")
	assert.True(t, engineFresh(t, generic, ""), "a generic .claude directory is not Seamark setup")
}
