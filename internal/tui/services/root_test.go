package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
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

// rootCopy returns a full copy of the workspace tree with its index. The
// copy still records the root of ws, as a copied checkout does before
// its first build.
func rootCopy(t *testing.T, ws workspace.Workspace, name string) string {
	t.Helper()

	dst := filepath.Join(rootDir(t), name)
	require.NoError(t, os.CopyFS(dst, os.DirFS(ws.Root)))

	return dst
}

// rootDir returns a new directory by its resolved path, because the
// index records the resolved root.
func rootDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

// rootLink returns a new link to dir.
func rootLink(t *testing.T, dir string) string {
	t.Helper()

	link := filepath.Join(rootDir(t), "link")
	require.NoError(t, os.Symlink(dir, link))

	return link
}

// rootPrune plans a prune of the linked pin. It requires a plan that can
// be confirmed, so a refusal can only come from the commit.
func rootPrune(t *testing.T, e *engine) *maintain.Plan {
	t.Helper()

	plan, err := e.PlanMaintenance(maintPrune(t, e))
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blocked)

	return plan
}

// TestOwnRootComparesFilesNotPathStrings covers a copied checkout before
// its first build. Each spelling names the default database of the copy,
// so a prune would write the pin file of the origin and the ledger of
// the copy. The refusal must not depend on the spelling.
func TestOwnRootComparesFilesNotPathStrings(t *testing.T) {
	cases := []struct {
		name    string
		resolve func(t *testing.T, root string) (workspace.Workspace, error)
	}{
		{"the exact default path", func(_ *testing.T, root string) (workspace.Workspace, error) {
			return workspace.Resolve(root, store.DefaultPath(root))
		}},
		{"a path through a link", func(t *testing.T, root string) (workspace.Workspace, error) {
			return workspace.Resolve(root, store.DefaultPath(rootLink(t, root)))
		}},
		{"a relative path from a linked directory", func(t *testing.T, root string) (workspace.Workspace, error) {
			t.Chdir(rootLink(t, root))

			return workspace.Resolve(".", filepath.Join(store.DefaultDir, store.DefaultDBName))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin, _ := maintWorkspace(t, true)
			root := rootCopy(t, origin, "copy")

			ws, err := c.resolve(t, root)
			require.NoError(t, err)
			require.Equal(t, root, ws.Root)

			before, err := os.ReadFile(filepath.Join(origin.Root, pinfile.File))
			require.NoError(t, err)

			e := engNew(ws, Deps{})
			require.Equal(t, origin.Root, e.Pins().Index.Root, "the copy still records the origin")

			_, err = e.CommitMaintenance(rootPrune(t, e))
			require.ErrorIs(t, err, ErrForeignRoot)

			after, err := os.ReadFile(filepath.Join(origin.Root, pinfile.File))
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the pin file of the origin stays")
		})
	}
}

// TestOwnRootKeepsTheRefusalForALinkedDefaultPath covers a default path
// that links to the database of a copy. That database records the
// origin, so the ledger and the pin file lie in two repositories. The
// file is outside this root and so no default database, but only a file
// that the default path does not name lifts the refusal.
func TestOwnRootKeepsTheRefusalForALinkedDefaultPath(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	shared := rootCopy(t, origin, "copy")
	side := rootCopy(t, origin, "side")

	require.NoError(t, os.Remove(store.DefaultPath(side)))
	require.NoError(t, os.Symlink(store.DefaultPath(shared), store.DefaultPath(side)))

	ws, err := workspace.Resolve(side, "")
	require.NoError(t, err)

	e := engNew(ws, Deps{})
	assert.Equal(t, store.DatabaseLinked, e.Pins().Database.Kind)

	_, err = e.CommitMaintenance(rootPrune(t, e))
	assert.ErrorIs(t, err, ErrForeignRoot)
}

// TestOwnRootKeepsTheExceptionOfAnotherDatabase covers the case that the
// exception exists for. The ledger and the pin file both belong to the
// other repository, so one commit changes one repository.
func TestOwnRootKeepsTheExceptionOfAnotherDatabase(t *testing.T) {
	origin, _ := maintWorkspace(t, true)

	ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	res, err := e.CommitMaintenance(rootPrune(t, e))
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.True(t, res.LedgerDone)
}

// TestPinsAnswerTheDatabaseFacts pins the answers that the console reads
// instead of the disk.
func TestPinsAnswerTheDatabaseFacts(t *testing.T) {
	t.Run("the default database of its own root", func(t *testing.T) {
		origin, _ := maintWorkspace(t, true)

		assert.Equal(t, workspace.DatabaseFacts{}, engNew(origin, Deps{}).Pins().Database)
	})

	t.Run("a copied checkout through a link", func(t *testing.T) {
		origin, _ := maintWorkspace(t, true)
		root := rootCopy(t, origin, "copy")

		ws, err := workspace.Resolve(root, store.DefaultPath(rootLink(t, root)))
		require.NoError(t, err)

		assert.Equal(t, workspace.DatabaseFacts{ForeignRoot: origin.Root}, engNew(ws, Deps{}).Pins().Database,
			"a link spelling is the default database")
	})

	t.Run("a database of another repository", func(t *testing.T) {
		origin, _ := maintWorkspace(t, true)

		ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
		require.NoError(t, err)

		assert.Equal(t, workspace.DatabaseFacts{Kind: store.DatabaseOther, ForeignRoot: origin.Root},
			engNew(ws, Deps{}).Pins().Database)
	})

	t.Run("a moved repository with a link at its old path", func(t *testing.T) {
		origin, _ := maintWorkspace(t, true)
		root := rootCopy(t, origin, "moved")

		st, err := store.Open(store.DefaultPath(root))
		require.NoError(t, err)
		require.NoError(t, st.SetMeta(store.MetaRepoRoot, rootLink(t, root)))
		require.NoError(t, st.Close())

		ws, err := workspace.Resolve(root, "")
		require.NoError(t, err)

		assert.Equal(t, workspace.DatabaseFacts{}, engNew(ws, Deps{}).Pins().Database,
			"the old path names this directory, so the root is not foreign")
	})
}
