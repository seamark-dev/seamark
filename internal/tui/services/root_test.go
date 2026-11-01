package services

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

func TestSameDirAcceptsALinkToTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	assert.True(t, store.SameDirectory(dir, dir+"/"))
	assert.True(t, store.SameDirectory(link, dir), "a link spelling names the same directory")
	assert.False(t, store.SameDirectory(dir, t.TempDir()))
	assert.False(t, store.SameDirectory(dir, filepath.Join(dir, "missing")))
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

		got := engNew(ws, Deps{}).Pins().Database
		assert.Equal(t, store.DatabaseDefault, got.Kind, "a link spelling is the default database")
		assert.Equal(t, origin.Root, got.ForeignRoot)
		assert.ErrorIs(t, got.WriteBlock, ErrForeignRoot)
	})

	t.Run("a database of another repository", func(t *testing.T) {
		origin, _ := maintWorkspace(t, true)

		ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
		require.NoError(t, err)

		got := engNew(ws, Deps{}).Pins().Database
		assert.Equal(t, store.DatabaseOther, got.Kind)
		assert.Equal(t, origin.Root, got.ForeignRoot)
		assert.ErrorIs(t, got.BuildBlock, ErrForeignRoot, "a build refuses the database of another repository")
		assert.NoError(t, got.WriteBlock, "the recorded root owns the file, so a prune changes one repository")
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

// TestBuildIndexRecordsAMovedRepository covers a git repository that
// moved with its custom file. The fingerprint holds no path, so a plain
// build skipped and the old root stayed. The file lies inside the
// workspace, so the plain build must end the state.
func TestBuildIndexRecordsAMovedRepository(t *testing.T) {
	origin, _ := servicestest.Workspace(t, "", "api")
	moved := filepath.Join(rootDir(t), "moved")
	db := filepath.Join(moved, "custom.db")

	require.NoError(t, os.Rename(origin.Root, moved))
	require.NoError(t, os.Rename(store.DefaultPath(moved), db))

	ws, err := workspace.Resolve(moved, db)
	require.NoError(t, err)

	e := engNew(ws, Deps{})
	got := e.Pins().Database

	require.NoError(t, got.BuildBlock)
	require.ErrorContains(t, got.WriteBlock, "run `seamark index --move-root --db "+db+"` in "+moved+" first")

	require.NoError(t, rootBuild(e, false))
	assert.Equal(t, moved, rootRecorded(t, db), "a plain build must not skip")
	assert.NoError(t, e.Pins().Database.WriteBlock)
	assert.NoDirExists(t, origin.Root, "the build recreates no directory")
}

// TestBuildIndexKeepsARootOnADetachedDisk covers a gone root with a file
// outside the workspace. A stat cannot tell a detached disk from a moved
// repository, so the console refuses and names the flag of the CLI. The
// old root cannot open, so no route names its console.
func TestBuildIndexKeepsARootOnADetachedDisk(t *testing.T) {
	origin, _ := servicestest.Workspace(t, "", "api")
	moved := filepath.Join(rootDir(t), "moved")
	db := filepath.Join(rootDir(t), "shared.db")

	require.NoError(t, os.Rename(origin.DBPath, db))
	require.NoError(t, os.Rename(origin.Root, moved))

	ws, err := workspace.Resolve(moved, db)
	require.NoError(t, err)

	want := "run `seamark index --move-root --db " + db + "` in " + moved + " first"

	e := engNew(ws, Deps{})
	got := e.Pins().Database

	require.ErrorIs(t, got.BuildBlock, ErrForeignRoot)
	assert.Contains(t, got.BuildBlock.Error(), want)
	assert.Contains(t, got.WriteBlock.Error(), want)
	assert.NotContains(t, got.WriteBlock.Error(), "open the console in")
	assert.Equal(t, want, got.RootRoute)

	var foreign *ForeignRootError
	require.ErrorAs(t, got.WriteBlock, &foreign)
	assert.True(t, foreign.OtherDB, "a build refuses, so no screen names it")

	require.ErrorIs(t, rootBuild(e, false), ErrForeignRoot)
	require.ErrorIs(t, rootBuild(e, true), ErrForeignRoot)
	assert.Equal(t, origin.Root, rootRecorded(t, db))
}

// TestRouteOfARootThatCannotBeReachedNamesNoConsole covers EACCES. The
// root can still exist, so the refusal stays. Its console cannot open,
// so the route is the move, as in the CLI.
func TestRouteOfARootThatCannotBeReachedNamesNoConsole(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory with mode 0000")
	}

	base, _ := servicestest.Workspace(t, "", "api")
	parent := rootDir(t)
	hidden := filepath.Join(parent, "hidden")
	db := filepath.Join(rootDir(t), "custom.db")

	require.NoError(t, os.Mkdir(hidden, 0o755))

	st, err := store.Open(db)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, hidden))
	require.NoError(t, st.SetMeta(store.MetaIndexedAt, "1"))
	require.NoError(t, st.Close())

	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	ws, err := workspace.Resolve(base.Root, db)
	require.NoError(t, err)

	got := engNew(ws, Deps{}).Pins().Database
	want := "run `seamark index --move-root --db " + db + "` in " + base.Root + " first"

	require.ErrorIs(t, got.BuildBlock, ErrForeignRoot)
	assert.Contains(t, got.BuildBlock.Error(), want)
	assert.NotContains(t, got.BuildBlock.Error(), "open the console in")
	assert.Equal(t, want, got.RootRoute)
}

// TestRouteOfARootThatCannotBeReachedKeepsAFileWithItsOwner bounds the
// rule above. A move here would split the file from its checkout. A
// Seamark checkout takes the step. Any other checkout leaves no step
// here, so only another file remains.
func TestRouteOfARootThatCannotBeReachedKeepsAFileWithItsOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory with mode 0000")
	}

	base, _ := servicestest.Workspace(t, "", "api")
	parent := rootDir(t)
	hidden := filepath.Join(parent, "hidden")
	owner, repo := rootDir(t), rootDir(t)
	owned := filepath.Join(owner, store.DefaultDir, "keep.db")
	other := filepath.Join(repo, "keep.db")

	require.NoError(t, os.Mkdir(hidden, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(owned), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))

	for _, db := range []string{owned, other} {
		st, err := store.Open(db)
		require.NoError(t, err)
		require.NoError(t, st.SetMeta(store.MetaRepoRoot, hidden))
		require.NoError(t, st.Close())
	}

	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	ws, err := workspace.Resolve(base.Root, owned)
	require.NoError(t, err)
	assert.Equal(t, "run `seamark index --move-root --db "+owned+"` in "+owner+" first",
		engNew(ws, Deps{}).Pins().Database.RootRoute)

	ws, err = workspace.Resolve(base.Root, other)
	require.NoError(t, err)
	assert.Equal(t, "use another --db", engNew(ws, Deps{}).Pins().Database.RootRoute)
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

// rootPending adds one pending proposal to the database at path.
func rootPending(t *testing.T, path, rule, region, note string) domain.Proposal {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	p := domain.Proposal{
		Signature: "sig-" + rule, Rule: rule, Region: region, Note: note, Members: []int64{41},
		Status: domain.ProposalProposed,
	}
	require.NoError(t, storetest.InsertProposal(st, &p))

	return p
}

// rootStatus reads the status of proposal id in the database at path.
func rootStatus(t *testing.T, path string, id int64) string {
	t.Helper()

	st, err := store.OpenReadOnly(path)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	for _, status := range []string{domain.ProposalProposed, domain.ProposalApplied, domain.ProposalDismissed} {
		rows, err := st.Proposals(status)
		require.NoError(t, err)

		for _, r := range rows {
			if r.ID == id {
				return status
			}
		}
	}

	return ""
}

// rootFile reads the pin file of root.
func rootFile(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, pinfile.File))
	require.NoError(t, err)

	return string(data)
}

// TestApplyRefusesTheTwoRepositoryWrite covers a proposal apply in a
// copied checkout. The pin would land in the file of the origin and the
// row would turn applied in the ledger of the copy.
func TestApplyRefusesTheTwoRepositoryWrite(t *testing.T) {
	for name, db := range map[string]func(t *testing.T, root string) string{
		"the default database": func(*testing.T, string) string { return "" },
		"a path through a link": func(t *testing.T, root string) string {
			return store.DefaultPath(rootLink(t, root))
		},
	} {
		t.Run(name, func(t *testing.T) {
			origin, _ := maintWorkspace(t, true)
			row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")
			root := rootCopy(t, origin, "copy")
			before := rootFile(t, origin.Root)

			ws, err := workspace.Resolve(root, db(t, root))
			require.NoError(t, err)

			e := engNew(ws, Deps{})

			pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
			require.NoError(t, err)
			require.True(t, pv.Write)
			require.ErrorIs(t, pv.RootBlock, ErrForeignRoot, "the preview names the refusal of the commit")

			res, err := e.CommitDecision(pv)
			require.ErrorIs(t, err, ErrForeignRoot)
			assert.False(t, res.FileWritten)
			assert.Equal(t, before, rootFile(t, origin.Root), "the pin file of the origin stays")
			assert.Equal(t, domain.ProposalProposed, rootStatus(t, store.DefaultPath(root), row.ID))
		})
	}
}

// TestApplyWorksAfterTheBuildOfACopy holds the route that the refusal
// names: after the build the pin lands in the file of the copy.
func TestApplyWorksAfterTheBuildOfACopy(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")
	root := rootCopy(t, origin, "copy")
	before := rootFile(t, origin.Root)

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	e := engNew(ws, Deps{})
	require.NoError(t, rootBuild(e, false))

	pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)
	require.NoError(t, pv.RootBlock)

	res, err := e.CommitDecision(pv)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.Contains(t, rootFile(t, root), "new-rule")
	assert.Equal(t, before, rootFile(t, origin.Root))
}

// TestLedgerDecisionsWorkInACopy holds the actions that write only the
// ledger. They change one repository, so the refusal must not reach them.
func TestLedgerDecisionsWorkInACopy(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	dropped := rootPending(t, origin.DBPath, "dropped-rule", "api", "A dropped rule.")
	pinned := rootPending(t, origin.DBPath, "keep-docs-current", "", "Keep the docs current.")

	root := rootCopy(t, origin, "copy")
	before := rootFile(t, origin.Root)

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	pv, err := e.PreviewDecision(proposals.ActionDismiss, []int64{dropped.ID})
	require.NoError(t, err)

	_, err = e.CommitDecision(pv)
	require.NoError(t, err)
	assert.Equal(t, domain.ProposalDismissed, rootStatus(t, store.DefaultPath(root), dropped.ID))

	pv, err = e.PreviewDecision(proposals.ActionReconcile, []int64{pinned.ID})
	require.NoError(t, err)

	_, err = e.CommitDecision(pv)
	require.NoError(t, err)
	assert.Equal(t, domain.ProposalApplied, rootStatus(t, store.DefaultPath(root), pinned.ID))

	assert.Equal(t, before, rootFile(t, origin.Root))
}

// TestApplyKeepsTheExceptionOfAnotherDatabase holds the case without a
// second repository: the pin and the ledger both belong to the origin.
func TestApplyKeepsTheExceptionOfAnotherDatabase(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")

	ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)
	require.NoError(t, pv.RootBlock)

	_, err = e.CommitDecision(pv)
	require.NoError(t, err)
	assert.Contains(t, rootFile(t, origin.Root), "new-rule")
	assert.Equal(t, domain.ProposalApplied, rootStatus(t, origin.DBPath, row.ID))
}

// rootThirdDirectory returns a workspace outside origin whose --db names
// the database of a copy, and the path of that database. The file records
// origin, but origin has its own default database.
func rootThirdDirectory(t *testing.T, origin workspace.Workspace) (ws workspace.Workspace, db string) {
	t.Helper()

	db = store.DefaultPath(rootCopy(t, origin, "copy"))

	ws, err := workspace.Resolve(rootDir(t), db)
	require.NoError(t, err)

	return ws, db
}

// rootRenamed returns a copy of origin whose database moved to another
// name, so the default path of the copy names no file.
func rootRenamed(t *testing.T, origin workspace.Workspace) (ws workspace.Workspace, db string) {
	t.Helper()

	root := rootCopy(t, origin, "copy")
	db = filepath.Join(root, store.DefaultDir, "keep.db")
	require.NoError(t, os.Rename(store.DefaultPath(root), db))

	ws, err := workspace.Resolve(root, db)
	require.NoError(t, err)

	return ws, db
}

// TestOwnRootRefusesADatabaseThatTheRecordedRootDoesNotOwn covers a file
// that names no default path of this workspace but still is not the
// ledger of the recorded root. A write would change two repositories.
func TestOwnRootRefusesADatabaseThatTheRecordedRootDoesNotOwn(t *testing.T) {
	cases := map[string]struct {
		resolve func(*testing.T, workspace.Workspace) (workspace.Workspace, string)
		// route returns the route of the refusal. The file of a copy names
		// the move, because a console of the origin opens another ledger.
		route func(origin, ws workspace.Workspace, db string) string
	}{
		"a third directory with the database of a copy": {rootThirdDirectory,
			func(origin, _ workspace.Workspace, _ string) string { return "open the console in " + origin.Root }},
		"a copy with a renamed database": {rootRenamed, func(_, ws workspace.Workspace, db string) string {
			return "run `seamark index --move-root --db " + db + "` in " + ws.Root + " first"
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			origin, linked := maintWorkspace(t, true)
			row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")
			ws, db := tc.resolve(t, origin)
			before := rootFile(t, origin.Root)

			e := engNew(ws, Deps{})
			require.Equal(t, store.DatabaseOther, e.Pins().Database.Kind)
			require.ErrorIs(t, e.Pins().Database.WriteBlock, ErrForeignRoot)

			_, err := e.CommitMaintenance(rootPrune(t, e))
			require.ErrorIs(t, err, ErrForeignRoot)
			assert.Contains(t, err.Error(), tc.route(origin, ws, db))
			assert.NotContains(t, err.Error(), "with this --db", "this file with a console in the origin splits two repositories")

			pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
			require.NoError(t, err)
			require.ErrorIs(t, pv.RootBlock, ErrForeignRoot)

			_, err = e.CommitDecision(pv)
			require.ErrorIs(t, err, ErrForeignRoot)

			assert.Equal(t, before, rootFile(t, origin.Root), "the pin file of the origin stays")
			assert.Equal(t, domain.ProposalApplied, rootStatus(t, db, linked.ID), "the ledger of the file stays")
			assert.Equal(t, domain.ProposalProposed, rootStatus(t, db, row.ID))

			// The console of the recorded root writes its own file and its
			// own ledger.
			own := engNew(origin, Deps{})

			res, err := own.CommitMaintenance(rootPrune(t, own))
			require.NoError(t, err)
			assert.True(t, res.LedgerDone)
		})
	}
}

// TestOwnRootKeepsACustomDatabaseOfARootWithoutADefault covers a
// repository that keeps its index only in a --db file. That file is the
// ledger of the root, so one write changes one repository.
func TestOwnRootKeepsACustomDatabaseOfARootWithoutADefault(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")

	db := filepath.Join(rootDir(t), "custom.db")
	require.NoError(t, os.Rename(origin.DBPath, db))

	ws, err := workspace.Resolve(rootDir(t), db)
	require.NoError(t, err)

	e := engNew(ws, Deps{})
	require.NoError(t, e.Pins().Database.WriteBlock)

	pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)
	require.NoError(t, pv.RootBlock)

	_, err = e.CommitDecision(pv)
	require.NoError(t, err)
	assert.Contains(t, rootFile(t, origin.Root), "new-rule")

	res, err := e.CommitMaintenance(rootPrune(t, e))
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
}

// TestOwnRootRefusesARootThatNoLongerExists covers a database whose
// recorded root is gone. A write under that root would recreate the old
// path, and the ledger would then name a pin that nobody reads.
func TestOwnRootRefusesARootThatNoLongerExists(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	row := rootPending(t, origin.DBPath, "new-rule", "api", "A new rule.")

	db := filepath.Join(rootDir(t), "custom.db")
	require.NoError(t, os.Rename(origin.DBPath, db))
	require.NoError(t, os.RemoveAll(origin.Root))

	ws, err := workspace.Resolve(rootDir(t), db)
	require.NoError(t, err)

	e := engNew(ws, Deps{})
	require.Error(t, e.Pins().Database.WriteBlock)

	pv, err := e.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)
	require.Error(t, pv.RootBlock)

	_, err = e.CommitDecision(pv)
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.NoDirExists(t, origin.Root, "the refusal recreates no directory")
}

// TestRecordedRootInAnotherCaseIsNotForeign covers a case-insensitive
// volume: the index can record a spelling that the user typed. It names
// this directory, so no rule may read it as another repository.
func TestRecordedRootInAnotherCaseIsNotForeign(t *testing.T) {
	origin, _ := maintWorkspace(t, true)

	// The last element of a test directory holds digits only, so the
	// whole path changes case.
	variant := strings.ToUpper(origin.Root)
	if _, err := os.Stat(variant); err != nil {
		t.Skip("the volume is case-sensitive")
	}

	st, err := store.Open(origin.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, variant))
	require.NoError(t, st.Close())

	db := filepath.Join(rootDir(t), "own.db")
	require.NoError(t, os.Rename(origin.DBPath, db))

	ws, err := workspace.Resolve(origin.Root, db)
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	got := e.Pins().Database
	assert.Empty(t, got.ForeignRoot)
	assert.NoError(t, got.BuildBlock)
	assert.NoError(t, got.WriteBlock)
}

// rootGate saves distill.write through the engine and returns the result
// of the plan and of the save.
func rootGate(e *engine) (settings.Result, error) {
	snap, err := e.Settings()
	if err != nil {
		return settings.Result{}, err
	}

	plan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	if err != nil {
		return settings.Result{}, err
	}

	return e.SaveSettings(plan)
}

// rootConfig reads the config.yaml of root.
func rootConfig(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(settings.File)))
	require.NoError(t, err)

	return string(data)
}

// TestConfigSaveRefusesTheFileOfAnotherRepository covers a Settings save
// that changed the config.yaml of the origin from a copied checkout. The
// same rule covers a database that the recorded root does not own.
func TestConfigSaveRefusesTheFileOfAnotherRepository(t *testing.T) {
	cases := map[string]func(*testing.T, workspace.Workspace) workspace.Workspace{
		"a copied checkout": func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			ws, err := workspace.Resolve(rootCopy(t, origin, "copy"), "")
			require.NoError(t, err)

			return ws
		},
		"a third directory with the database of a copy": func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			ws, _ := rootThirdDirectory(t, origin)

			return ws
		},
		"a copy with a renamed database": func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			ws, _ := rootRenamed(t, origin)

			return ws
		},
	}

	for name, resolve := range cases {
		t.Run(name, func(t *testing.T) {
			origin, _ := maintWorkspace(t, false)
			ws := resolve(t, origin)
			before := rootConfig(t, origin.Root)

			e := engNew(ws, Deps{})

			snap, err := e.Settings()
			require.NoError(t, err)

			for _, g := range []settings.Group{
				settings.GroupInference, settings.GroupPinWrite, settings.GroupEvidence, settings.GroupIndexing,
			} {
				require.ErrorIs(t, snap.Editable(g), ErrForeignRoot, "%s shows the refusal before a draft", g)
			}

			_, err = rootGate(e)
			require.ErrorIs(t, err, ErrForeignRoot)
			assert.Equal(t, before, rootConfig(t, origin.Root), "the config.yaml of the origin stays")
		})
	}
}

// TestConfigSaveRefusesARootThatTurnedForeignAfterThePlan covers the check
// of the commit. A plan that passed writes nothing once the root moved.
func TestConfigSaveRefusesARootThatTurnedForeignAfterThePlan(t *testing.T) {
	origin, _ := maintWorkspace(t, false)
	e := engNew(origin, Deps{})

	snap, err := e.Settings()
	require.NoError(t, err)

	plan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)

	before := rootConfig(t, origin.Root)

	st, err := store.Open(origin.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, rootDir(t)))
	require.NoError(t, st.Close())

	_, err = e.SaveSettings(plan)
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Equal(t, before, rootConfig(t, origin.Root))
}

// TestConfigSaveKeepsTheDatabaseOfTheRecordedRoot holds the exception: the
// file and the database belong to one repository.
func TestConfigSaveKeepsTheDatabaseOfTheRecordedRoot(t *testing.T) {
	origin, _ := maintWorkspace(t, false)

	ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
	require.NoError(t, err)

	_, err = rootGate(engNew(ws, Deps{}))
	require.NoError(t, err)
	assert.Contains(t, rootConfig(t, origin.Root), "write: true")
}

// TestConfigSaveWorksWithoutARecordedRoot holds the first-run routes: no
// index, a new --db file, and a database that records no root.
func TestConfigSaveWorksWithoutARecordedRoot(t *testing.T) {
	repo := func(t *testing.T) string {
		t.Helper()

		root := rootDir(t)
		servicestest.Git(t, root, "init", "-b", "main")

		return root
	}

	cases := map[string]func(t *testing.T, root string) string{
		"no index":        func(*testing.T, string) string { return "" },
		"a new --db file": func(t *testing.T, _ string) string { return filepath.Join(rootDir(t), "new.db") },
		"an empty --db file": func(t *testing.T, _ string) string {
			path := filepath.Join(rootDir(t), "empty.db")
			require.NoError(t, os.WriteFile(path, nil, 0o644))

			return path
		},
	}

	for name, db := range cases {
		t.Run(name, func(t *testing.T) {
			root := repo(t)

			ws, err := workspace.Resolve(root, db(t, root))
			require.NoError(t, err)

			_, err = rootGate(engNew(ws, Deps{}))
			require.NoError(t, err)
			assert.Contains(t, rootConfig(t, root), "write: true", "the save creates the file of this workspace")
		})
	}
}

// TestCopyReadsItsOwnConfigAfterItsBuild states what a build changes. The
// copy shows the file of the origin before it and its own file after it,
// so the gate can read another value. A save then stays in the copy.
func TestCopyReadsItsOwnConfigAfterItsBuild(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	root := rootCopy(t, origin, "copy")

	// The two files differ, as they do once the origin moves on.
	own := filepath.Join(root, filepath.FromSlash(settings.File))
	data, err := os.ReadFile(own)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(own, []byte(strings.ReplaceAll(string(data), "write: true", "write: false")), 0o644))

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	before, err := e.Settings()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(origin.Root, filepath.FromSlash(settings.File)), before.Path)
	assert.True(t, before.Distill.Distill.Write, "before the build the screen reads the origin")

	require.NoError(t, rootBuild(e, false))

	after, err := e.Settings()
	require.NoError(t, err)
	assert.Equal(t, own, after.Path)
	assert.False(t, after.Distill.Distill.Write, "after the build the copy reads its own file")
	require.NoError(t, after.Editable(settings.GroupPinWrite))

	originConfig := rootConfig(t, origin.Root)

	_, err = rootGate(e)
	require.NoError(t, err)
	assert.Contains(t, rootConfig(t, root), "write: true")
	assert.Equal(t, originConfig, rootConfig(t, origin.Root), "the save stays in the copy")
}

// TestRouteOfACustomDatabaseKeepsItsDB covers a --db file outside any
// checkout. The default database of the recorded root is another ledger,
// so the plain route would not list the rows of this file.
func TestRouteOfACustomDatabaseKeepsItsDB(t *testing.T) {
	origin, _ := maintWorkspace(t, true)

	db := filepath.Join(rootDir(t), "custom.db")

	data, err := os.ReadFile(origin.DBPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(db, data, 0o644))

	row := rootPending(t, db, "custom-rule", "api", "A custom rule.")

	ws, err := workspace.Resolve(rootDir(t), db)
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	_, err = e.CommitMaintenance(rootPrune(t, e))
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Contains(t, err.Error(), "open the console in "+origin.Root+" with this --db")

	require.ErrorContains(t, e.Index().Database.BuildBlock, "open the console in "+origin.Root+" with this --db")
	assert.True(t, e.Pins().Database.CustomDB)

	var foreign *ForeignRootError
	require.ErrorAs(t, err, &foreign)
	assert.True(t, foreign.CustomDB)

	// The route works: the console of the root with this file lists the
	// row and writes it. The plain route opens another ledger.
	named, err := workspace.Resolve(origin.Root, db)
	require.NoError(t, err)

	own := engNew(named, Deps{})

	pv, err := own.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)

	_, err = own.CommitDecision(pv)
	require.NoError(t, err)
	assert.Contains(t, rootFile(t, origin.Root), "custom-rule")
	assert.Equal(t, domain.ProposalApplied, rootStatus(t, db, row.ID))
	assert.Empty(t, rootStatus(t, origin.DBPath, row.ID), "the default ledger never held this row")
}

// TestRouteOfACustomFileInACopyRecordsThatCopy covers a custom file that
// belongs to a copied checkout. The console of the origin with this file
// would change the pin file of the origin and the ledger of the copy.
// So the route records the copy in the file. The test runs the index run
// of that route, and the console of the copy then writes only the copy.
func TestRouteOfACustomFileInACopyRecordsThatCopy(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	root := rootCopy(t, origin, "copy")
	db := filepath.Join(root, "custom.db")

	require.NoError(t, os.Rename(store.DefaultPath(root), db))

	row := rootPending(t, db, "custom-rule", "api", "A custom rule.")
	before := rootFile(t, origin.Root)

	ws, err := workspace.Resolve(root, db)
	require.NoError(t, err)

	want := "run `seamark index --move-root --db " + db + "` in " + root + " first"

	e := engNew(ws, Deps{})
	got := e.Pins().Database

	assert.Equal(t, want, got.RootRoute)
	assert.False(t, got.CustomDB, "a route to the origin must not keep this file")
	require.ErrorContains(t, got.WriteBlock, want)
	require.ErrorContains(t, got.BuildBlock, want)
	assert.NotContains(t, got.WriteBlock.Error(), "with this --db")
	assert.NotContains(t, got.BuildBlock.Error(), "with this --db")

	_, err = e.CommitMaintenance(rootPrune(t, e))
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Contains(t, err.Error(), want)

	var foreign *ForeignRootError
	require.ErrorAs(t, err, &foreign)
	assert.Equal(t, want, foreign.RootRoute)

	require.Error(t, rootBuild(e, true), "the console has no flag that allows the move")

	_, err = index.Run(index.Options{Root: root, DBPath: db, MoveRoot: true})
	require.NoError(t, err)

	own := engNew(ws, Deps{})
	require.NoError(t, own.Pins().Database.WriteBlock)

	pv, err := own.PreviewDecision(proposals.ActionApply, []int64{row.ID})
	require.NoError(t, err)

	_, err = own.CommitDecision(pv)
	require.NoError(t, err)

	assert.Contains(t, rootFile(t, root), "custom-rule")
	assert.Equal(t, domain.ProposalApplied, rootStatus(t, db, row.ID))
	assert.Equal(t, before, rootFile(t, origin.Root), "the origin stays as it is")
}

// TestRouteOfADatabaseOfTheRootNamesNoDB holds the plain route: the
// default database of the root is this file, so a console there opens it.
func TestRouteOfADatabaseOfTheRootNamesNoDB(t *testing.T) {
	origin, _ := maintWorkspace(t, true)

	ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
	require.NoError(t, err)

	got := engNew(ws, Deps{}).Pins().Database
	assert.False(t, got.CustomDB)
	assert.NotContains(t, got.BuildBlock.Error(), "--db")
}

// TestRouteOfALinkedDefaultPathNamesNoDB covers a default path that links
// to a file outside any checkout. The user gave no --db, so the route has
// none to keep.
func TestRouteOfALinkedDefaultPathNamesNoDB(t *testing.T) {
	origin, _ := maintWorkspace(t, true)
	side := rootCopy(t, origin, "side")

	shared := filepath.Join(rootDir(t), "shared.db")
	require.NoError(t, os.Rename(store.DefaultPath(side), shared))
	require.NoError(t, os.Symlink(shared, store.DefaultPath(side)))

	ws, err := workspace.Resolve(side, "")
	require.NoError(t, err)

	e := engNew(ws, Deps{})

	_, err = e.CommitMaintenance(rootPrune(t, e))
	require.ErrorIs(t, err, ErrForeignRoot)
	assert.Contains(t, err.Error(), "open the console in "+origin.Root)
	assert.NotContains(t, err.Error(), "--db")
}
