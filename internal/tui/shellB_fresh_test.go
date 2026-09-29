package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/workspace"
)

// shellBFresh resolves dir the way `seamark tui` does and returns the
// first-run verdict of the production engine.
func shellBFresh(t *testing.T, dir, db string) bool {
	t.Helper()

	ws, err := workspace.Resolve(dir, db)
	require.NoError(t, err)

	fresh, err := NewServices(ws, "test", "/usr/local/bin/seamark").Fresh()
	require.NoError(t, err)

	return fresh
}

// shellBWrite creates rel under root with a small content.
func shellBWrite(t *testing.T, root, rel string) {
	t.Helper()

	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x: 1\n"), 0o600))
}

// TestShellBFreshSeesAnySeamarkState proves finding 20: any entry of the
// .seamark directory, or an existing database path, is Seamark state, so
// the console does not show the clean-install welcome.
func TestShellBFreshSeesAnySeamarkState(t *testing.T) {
	assert.True(t, shellBFresh(t, t.TempDir(), ""), "an empty directory is fresh")

	for _, rel := range []string{
		".seamark/effects.yaml",
		".seamark/lessons-audit.jsonl",
		".seamark/config.yaml",
	} {
		root := t.TempDir()
		shellBWrite(t, root, rel)
		assert.False(t, shellBFresh(t, root, ""), "%s is Seamark state", rel)
	}

	empty := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(empty, ".seamark"), 0o755))
	assert.False(t, shellBFresh(t, empty, ""), "an empty .seamark directory is partial state")

	// The probe follows links and calls a dangling link absent. The
	// database path still exists, so the workspace is not fresh.
	linked := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.Symlink(filepath.Join(linked, "missing.db"), db))
	assert.False(t, shellBFresh(t, linked, db), "an existing database path is Seamark state")

	// A generic agent directory without a Seamark marker stays fresh.
	generic := t.TempDir()
	shellBWrite(t, generic, ".claude/notes.md")
	assert.True(t, shellBFresh(t, generic, ""), "a generic .claude directory is not Seamark setup")
}
