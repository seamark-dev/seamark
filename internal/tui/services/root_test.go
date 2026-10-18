package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

func TestSameDirAcceptsALinkToTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	assert.True(t, sameDir(dir, dir+"/"))
	assert.True(t, sameDir(link, dir), "a link spelling names the same directory")
	assert.False(t, sameDir(dir, t.TempDir()))
	assert.False(t, sameDir(dir, filepath.Join(dir, "missing")))
}

// TestTheRootRulesDifferOnlyInTheDBException pins the two root rules.
// Phase 3 maintenance accepts another root with a --db flag. The Phase 4
// lessons operations accept it never (P4-16).
func TestTheRootRulesDifferOnlyInTheDBException(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	foreign := workspace.IndexProbe{Root: other}
	own := workspace.IndexProbe{Root: root}

	defaultDB := engNew(workspace.Workspace{Root: root, DBPath: store.DefaultPath(root)}, Deps{})
	flagDB := engNew(workspace.Workspace{Root: root, DBPath: filepath.Join(other, "index.db")}, Deps{})

	for name, e := range map[string]*engine{"the default database": defaultDB, "a --db flag": flagDB} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, e.foreignRoot(own))
			require.NoError(t, e.ownRoot(own))

			err := e.foreignRoot(foreign)
			require.ErrorIs(t, err, ErrForeignRoot, "a lessons operation has no --db exception")
			assert.Equal(t, "the index records another repository root ("+other+"), not this workspace ("+root+")",
				err.Error(), "the text names both roots and no route")
		})
	}

	err := defaultDB.ownRoot(foreign)
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Equal(t, "the index records another repository root ("+other+"), not this workspace ("+root+
		"); run seamark index here first", err.Error(), "the maintenance text keeps its Phase 3 route")

	assert.NoError(t, flagDB.ownRoot(foreign), "maintenance keeps the --db exception")
}
