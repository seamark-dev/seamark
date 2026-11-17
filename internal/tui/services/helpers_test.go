package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds test helpers that tests of several services share.

// committedWorkspace is a committed Git workspace without an index.
func committedWorkspace(t *testing.T) workspace.Workspace {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte("fixture\n"), 0o644))

	servicestest.Git(t, root, "init", "-b", "main")
	servicestest.Git(t, root, "add", "README")
	servicestest.Git(t, root, "commit", "-m", "initial")

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	return ws
}
