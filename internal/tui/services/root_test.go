package services

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
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

		got := engNew(ws, Deps{}).Pins().Database
		assert.Equal(t, store.DatabaseOther, got.Kind)
		assert.Equal(t, origin.Root, got.ForeignRoot)
		assert.ErrorIs(t, got.BuildBlock, ErrForeignRoot, "a build refuses the database of another repository")
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

// rootSchema stamps the database at path with another schema version.
func rootSchema(t *testing.T, path string, version int) {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaSchemaVersion, strconv.Itoa(version)))
	require.NoError(t, st.Close())
}

// rootRecorded reads the root at path without the migration of an open.
func rootRecorded(t *testing.T, path string) string {
	t.Helper()

	root, err := store.ProbeRoot(path)
	require.NoError(t, err)

	return root
}

// rootBuild runs a build and drops its progress.
func rootBuild(e *engine, force bool) error {
	_, err := e.BuildIndex(force, func(IndexEvent) {})

	return err
}

// TestBuildIndexRefusesTheDatabaseOfAnotherRepository covers the build
// that moved the recorded root. The other checkout then read no pins and
// lost its maintenance. An older schema hides the root from the probe.
func TestBuildIndexRefusesTheDatabaseOfAnotherRepository(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, origin string)
	}{
		{"a ready database", func(*testing.T, string) {}},
		{"an older schema", func(t *testing.T, origin string) {
			rootSchema(t, origin, store.SupportedSchema()-1)
		}},
		{"a newer schema", func(t *testing.T, origin string) {
			rootSchema(t, origin, store.SupportedSchema()+1)
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin, _ := servicestest.Workspace(t, "", "api")
			other, _ := servicestest.Workspace(t, "", "api")

			c.setup(t, origin.DBPath)

			ws, err := workspace.Resolve(other.Root, origin.DBPath)
			require.NoError(t, err)

			before, err := os.ReadFile(origin.DBPath)
			require.NoError(t, err)

			e := engNew(ws, Deps{})

			var refused error

			for _, force := range []bool{false, true} {
				refused = rootBuild(e, force)
				require.ErrorIs(t, refused, ErrForeignRoot)
				assert.Contains(t, refused.Error(), "open the console in "+origin.Root, "the refusal names the route")
			}

			after, err := os.ReadFile(origin.DBPath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "the database stays as it is")
			assert.Equal(t, origin.Root, rootRecorded(t, origin.DBPath))

			require.EqualError(t, e.Index().Database.BuildBlock, refused.Error(), "the read gives the words of the refusal")
			require.EqualError(t, e.Pins().Database.BuildBlock, refused.Error())
		})
	}
}

// TestBuildIndexRefusesALinkedDefaultPath covers a worktree that shares
// the database of another worktree. No --db is set, but the file belongs
// to the other worktree, so a build here moves its root.
func TestBuildIndexRefusesALinkedDefaultPath(t *testing.T) {
	origin, _ := servicestest.Workspace(t, "", "api")
	side := rootCopy(t, origin, "side")

	require.NoError(t, os.Remove(store.DefaultPath(side)))
	require.NoError(t, os.Symlink(origin.DBPath, store.DefaultPath(side)))

	// A new commit defeats the fast path, so only the gate keeps the root.
	require.NoError(t, os.WriteFile(filepath.Join(side, "b.go"), []byte("package eng\n"), 0o644))
	servicestest.Git(t, side, "add", "-A")
	servicestest.Git(t, side, "commit", "-m", "second")

	ws, err := workspace.Resolve(side, "")
	require.NoError(t, err)

	err = rootBuild(engNew(ws, Deps{}), false)
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Contains(t, err.Error(), "open the console in "+origin.Root)
	assert.Equal(t, origin.Root, rootRecorded(t, origin.DBPath))
}

// TestBuildIndexKeepsEveryDatabaseWithoutAnotherOwner holds the cases
// that the refusal must not reach.
func TestBuildIndexKeepsEveryDatabaseWithoutAnotherOwner(t *testing.T) {
	// own builds a database outside the workspace that records it.
	own := func(t *testing.T, root string) string {
		t.Helper()

		ws, err := workspace.Resolve(root, filepath.Join(rootDir(t), "own.db"))
		require.NoError(t, err)
		require.NoError(t, rootBuild(engNew(ws, Deps{}), false))

		return ws.DBPath
	}

	cases := []struct {
		name string
		// db returns the --db value for the workspace at root. "" uses
		// the default database.
		db func(t *testing.T, root string) string
	}{
		{"a --db file that does not exist", func(t *testing.T, _ string) string {
			return filepath.Join(rootDir(t), "new.db")
		}},
		{"a new empty --db file", func(t *testing.T, _ string) string {
			path := filepath.Join(rootDir(t), "empty.db")
			require.NoError(t, os.WriteFile(path, nil, 0o644))

			return path
		}},
		{"a --db that records this workspace", own},
		{"a --db that records this workspace through a link", func(t *testing.T, root string) string {
			path := own(t, root)

			st, err := store.Open(path)
			require.NoError(t, err)
			require.NoError(t, st.SetMeta(store.MetaRepoRoot, rootLink(t, root)))
			require.NoError(t, st.Close())

			return path
		}},
		{"a --db with an empty recorded root", func(t *testing.T, root string) string {
			path := own(t, root)

			st, err := store.Open(path)
			require.NoError(t, err)
			require.NoError(t, st.SetMeta(store.MetaRepoRoot, ""))
			require.NoError(t, st.Close())

			return path
		}},
		{"an older --db that records this workspace", func(t *testing.T, root string) string {
			path := own(t, root)
			rootSchema(t, path, store.SupportedSchema()-1)

			return path
		}},
		{"the default database", func(*testing.T, string) string { return "" }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, _ := servicestest.Workspace(t, "", "api")

			ws, err := workspace.Resolve(base.Root, c.db(t, base.Root))
			require.NoError(t, err)

			e := engNew(ws, Deps{})
			require.NoError(t, e.Index().Database.BuildBlock)
			require.NoError(t, rootBuild(e, true))
			assert.Equal(t, base.Root, rootRecorded(t, ws.DBPath))
		})
	}
}

// TestBuildIndexRebuildsACopiedCheckout holds the route of a copy for
// each spelling of its own database. The build ends the foreign root.
func TestBuildIndexRebuildsACopiedCheckout(t *testing.T) {
	for name, db := range map[string]func(t *testing.T, root string) string{
		"the default database": func(*testing.T, string) string { return "" },
		"a path through a link": func(t *testing.T, root string) string {
			return store.DefaultPath(rootLink(t, root))
		},
	} {
		t.Run(name, func(t *testing.T) {
			origin, _ := servicestest.Workspace(t, "", "api")
			root := rootCopy(t, origin, "copy")

			ws, err := workspace.Resolve(root, db(t, root))
			require.NoError(t, err)

			e := engNew(ws, Deps{})
			require.Equal(t, origin.Root, e.probe().Root)
			require.NoError(t, e.Index().Database.BuildBlock)

			require.NoError(t, rootBuild(e, false))
			assert.Equal(t, root, rootRecorded(t, store.DefaultPath(root)))
			assert.Equal(t, origin.Root, rootRecorded(t, origin.DBPath), "the origin keeps its own root")
		})
	}
}

// TestBuildIndexFailsClosedOnARootThatDoesNotRead covers a file with
// content that gives no root. It can belong to another repository, so
// nothing writes it.
func TestBuildIndexFailsClosedOnARootThatDoesNotRead(t *testing.T) {
	base, _ := servicestest.Workspace(t, "", "api")

	path := filepath.Join(rootDir(t), "garbage.db")
	garbage := []byte("this file is no database, and it is long enough to read")
	require.NoError(t, os.WriteFile(path, garbage, 0o644))

	ws, err := workspace.Resolve(base.Root, path)
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	err = rootBuild(e, false)
	require.ErrorIs(t, err, errRootUnknown)
	assert.Contains(t, err.Error(), path, "the reason names the file")
	assert.Contains(t, err.Error(), "use another --db", "the reason names the next route")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, garbage, after)

	require.ErrorIs(t, e.Index().Database.BuildBlock, errRootUnknown)
}

// TestForeignRootErrorTellsTheSettingsItsRoute covers the answer that a
// settings block carries to the view.
func TestForeignRootErrorTellsTheSettingsItsRoute(t *testing.T) {
	read := func(t *testing.T, ws workspace.Workspace) *ForeignRootError {
		t.Helper()

		snap, err := engNew(ws, Deps{}).Settings()
		require.NoError(t, err)

		block := snap.Editable(settings.GroupDelivery)
		require.ErrorIs(t, block, ErrForeignRoot)

		var foreign *ForeignRootError
		require.ErrorAs(t, block, &foreign)

		return foreign
	}

	origin, _ := maintWorkspace(t, true)

	ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
	require.NoError(t, err)

	got := read(t, ws)
	assert.True(t, got.OtherDB, "a build refuses the database of another repository")
	assert.Equal(t, origin.Root, got.Root)

	ws, err = workspace.Resolve(rootCopy(t, origin, "copy"), "")
	require.NoError(t, err)
	assert.False(t, read(t, ws).OtherDB, "a build ends the state of a copied checkout")
}
