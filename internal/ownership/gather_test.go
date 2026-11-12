package ownership

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// plainDir returns a new directory by its resolved path, so a test compares
// the spelling that the rule sees.
func plainDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

// gitInit makes dir a git top level and returns it.
func gitInit(t *testing.T, dir string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o755))

	out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	return dir
}

// checkout returns a new resolved directory with a state directory.
func checkout(t *testing.T) string {
	t.Helper()

	dir := plainDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, store.DefaultDir), 0o755))

	return dir
}

// database creates a real database at path that records root.
func database(t *testing.T, path, root string) string {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))
	require.NoError(t, st.Close())

	return path
}

// emptyFile creates a file at path.
func emptyFile(t *testing.T, path string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	return path
}

// link makes the default path of root a link to target.
func link(t *testing.T, root, target string) string {
	t.Helper()

	path := store.DefaultPath(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.Symlink(target, path))

	return path
}

// hiddenRoot returns a path that a stat cannot reach, because its parent
// gives no permission. The test skips as root, because root ignores the
// file mode.
func hiddenRoot(t *testing.T) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root reads a directory with mode 0000")
	}

	parent := plainDir(t)
	root := filepath.Join(parent, "root")

	require.NoError(t, os.Mkdir(root, 0o755))
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	return root
}

// TestGather builds each state on real repositories. It checks the facts
// and the decision, so a state on the disk reaches the answer that the
// name says.
func TestGather(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the workspace and the database.
		setup func(t *testing.T) (string, string)
		facts Facts
		want  Decision
	}{
		{"no recorded root", func(t *testing.T) (string, string) {
			root := plainDir(t)

			return root, database(t, store.DefaultPath(root), "")
		}, Facts{File: FileDefault}, Decision{BuildAllowed, WriteAllowed, RouteNone}},
		{"the recorded root is the workspace", func(t *testing.T) (string, string) {
			root := plainDir(t)

			return root, database(t, store.DefaultPath(root), root)
		}, Facts{File: FileDefault, Root: RootSame}, Decision{BuildAllowed, WriteAllowed, RouteNone}},
		{"a link spelling of the workspace", func(t *testing.T) (string, string) {
			root := plainDir(t)
			spelling := filepath.Join(plainDir(t), "old")

			require.NoError(t, os.Symlink(root, spelling))

			return root, database(t, filepath.Join(plainDir(t), "custom.db"), spelling)
		}, Facts{Root: RootSame}, Decision{BuildAllowed, WriteAllowed, RouteNone}},
		{"no file", func(t *testing.T) (string, string) {
			return plainDir(t), filepath.Join(plainDir(t), "new.db")
		}, Facts{File: FileAbsent}, Decision{BuildAllowed, WriteNoIndex, RouteIndexHere}},
		{"a path below a regular file", func(t *testing.T) (string, string) {
			file := emptyFile(t, filepath.Join(plainDir(t), "main.go"))

			return plainDir(t), filepath.Join(file, "x.db")
		}, Facts{File: FileAbsent}, Decision{BuildAllowed, WriteNoIndex, RouteIndexHere}},
		{"a path below a directory that a stat cannot reach", func(t *testing.T) (string, string) {
			return plainDir(t), filepath.Join(hiddenRoot(t), "x.db")
		}, Facts{Root: RootUnreadable}, Decision{BuildRefused, WriteRefused, RouteFixFile}},
		{"an empty file", func(t *testing.T) (string, string) {
			return plainDir(t), emptyFile(t, filepath.Join(plainDir(t), "empty.db"))
		}, Facts{File: FileAbsent}, Decision{BuildAllowed, WriteNoIndex, RouteIndexHere}},
		{"a copied checkout", func(t *testing.T) (string, string) {
			copied := gitInit(t, plainDir(t))

			return copied, database(t, store.DefaultPath(copied), gitInit(t, plainDir(t)))
		}, Facts{File: FileDefault, Root: RootFound}, Decision{BuildRecords, WriteRefused, RouteIndexHere}},
		{"a moved checkout", func(t *testing.T) (string, string) {
			moved := gitInit(t, plainDir(t))

			return moved, database(t, store.DefaultPath(moved), filepath.Join(plainDir(t), "gone"))
		}, Facts{File: FileDefault, Root: RootGone}, Decision{BuildRecords, WriteRefused, RouteIndexHere}},
		{"a custom file in a copy", func(t *testing.T) (string, string) {
			copied, origin := gitInit(t, plainDir(t)), gitInit(t, plainDir(t))

			database(t, store.DefaultPath(origin), origin)

			return copied, database(t, filepath.Join(copied, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerWorkspace, FileInWorkspace: true},
			Decision{BuildRefused, WriteRefused, RouteMoveHere}},
		{"a copy nested in the origin", func(t *testing.T) (string, string) {
			origin := checkout(t)
			copied := filepath.Join(origin, "inner")

			database(t, store.DefaultPath(origin), origin)
			require.NoError(t, os.MkdirAll(filepath.Join(copied, store.DefaultDir), 0o755))

			return copied, database(t, filepath.Join(copied, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerWorkspace, FileInWorkspace: true},
			Decision{BuildRefused, WriteRefused, RouteMoveHere}},
		{"the known limit of an origin without a default database", func(t *testing.T) (string, string) {
			copied := checkout(t)

			return copied, database(t, filepath.Join(copied, store.DefaultDir, "renamed.db"), gitInit(t, plainDir(t)))
		}, Facts{Root: RootFound, Owner: OwnerWorkspace, FileInWorkspace: true, RootOwnsFile: true},
			Decision{BuildRefused, WriteAllowed, RouteMoveHere}},
		{"the default database of another repository", func(t *testing.T) (string, string) {
			origin := gitInit(t, plainDir(t))

			return plainDir(t), database(t, store.DefaultPath(origin), origin)
		}, Facts{Root: RootFound, Owner: OwnerRoot, RootDefault: true, RootOwnsFile: true},
			Decision{BuildRefused, WriteAllowed, RouteRunInRoot}},
		{"a custom file of a repository without a default database", func(t *testing.T) (string, string) {
			origin := gitInit(t, plainDir(t))

			return plainDir(t), database(t, filepath.Join(origin, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerRoot, RootOwnsFile: true},
			Decision{BuildRefused, WriteAllowed, RouteRunInRootDB}},
		{"a custom file beside a default database", func(t *testing.T) (string, string) {
			origin := gitInit(t, plainDir(t))

			database(t, store.DefaultPath(origin), origin)

			return plainDir(t), database(t, filepath.Join(origin, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerRoot}, Decision{BuildRefused, WriteRefused, RouteRunInRootDB}},
		{"a file of a submodule stays with the root", func(t *testing.T) (string, string) {
			origin := checkout(t)
			submodule := filepath.Join(origin, "sub")

			database(t, store.DefaultPath(origin), origin)
			require.NoError(t, os.MkdirAll(filepath.Join(submodule, store.DefaultDir), 0o755))

			return plainDir(t), database(t, filepath.Join(submodule, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerRoot}, Decision{BuildRefused, WriteRefused, RouteRunInRootDB}},
		{"a linked worktree", func(t *testing.T) (string, string) {
			origin, tree := gitInit(t, plainDir(t)), gitInit(t, plainDir(t))

			return tree, link(t, tree, database(t, store.DefaultPath(origin), origin))
		}, Facts{File: FileLinked, Root: RootFound, Owner: OwnerRoot, RootDefault: true, RootOwnsFile: true},
			Decision{BuildRefused, WriteAllowed, RouteRunInRoot}},
		{"a linked file that the root does not own", func(t *testing.T) (string, string) {
			origin, tree := gitInit(t, plainDir(t)), gitInit(t, plainDir(t))

			database(t, store.DefaultPath(origin), origin)

			return tree, link(t, tree, database(t, filepath.Join(origin, "custom.db"), origin))
		}, Facts{File: FileLinked, Root: RootFound, Owner: OwnerRoot},
			Decision{BuildRefused, WriteRefused, RouteRunInRootDB}},
		{"a shared file without an owner", func(t *testing.T) (string, string) {
			origin := gitInit(t, plainDir(t))

			return plainDir(t), database(t, filepath.Join(plainDir(t), "shared.db"), origin)
		}, Facts{Root: RootFound, RootOwnsFile: true}, Decision{BuildRefused, WriteAllowed, RouteRunInRootDB}},
		{"a shared file in a git work tree", func(t *testing.T) (string, string) {
			origin := gitInit(t, plainDir(t))

			database(t, store.DefaultPath(origin), origin)

			return plainDir(t), database(t, filepath.Join(gitInit(t, plainDir(t)), "shared.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerGitOnly}, Decision{BuildRefused, WriteRefused, RouteRunInRootDB}},
		{"a file that moved with the workspace", func(t *testing.T) (string, string) {
			moved := gitInit(t, plainDir(t))

			return moved, database(t, filepath.Join(moved, "custom.db"), filepath.Join(plainDir(t), "gone"))
		}, Facts{Root: RootGone, Owner: OwnerWorkspace, FileInWorkspace: true},
			Decision{BuildRecords, WriteRefused, RouteIndexHereDB}},
		{"a file elsewhere that the workspace can take", func(t *testing.T) (string, string) {
			return plainDir(t), database(t, filepath.Join(plainDir(t), "shared.db"), filepath.Join(plainDir(t), "gone"))
		}, Facts{Root: RootGone}, Decision{BuildRefused, WriteRefused, RouteMoveHere}},
		{"a third checkout with a live root", func(t *testing.T) (string, string) {
			origin, copied := gitInit(t, plainDir(t)), checkout(t)

			database(t, store.DefaultPath(origin), origin)

			return plainDir(t), database(t, filepath.Join(copied, "custom.db"), origin)
		}, Facts{Root: RootFound, Owner: OwnerThird}, Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"the known limit of an origin without a default database", func(t *testing.T) (string, string) {
			return plainDir(t), database(t, filepath.Join(checkout(t), "custom.db"), gitInit(t, plainDir(t)))
		}, Facts{Root: RootFound, Owner: OwnerThird, RootOwnsFile: true},
			Decision{BuildRefused, WriteAllowed, RouteAnotherDB}},
		{"a third checkout with a gone root", func(t *testing.T) (string, string) {
			return plainDir(t), database(t, filepath.Join(checkout(t), "custom.db"), filepath.Join(plainDir(t), "gone"))
		}, Facts{Root: RootGone, Owner: OwnerThird}, Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"a git work tree with a gone root", func(t *testing.T) (string, string) {
			return plainDir(t), database(t, filepath.Join(gitInit(t, plainDir(t)), "custom.db"),
				filepath.Join(plainDir(t), "gone"))
		}, Facts{Root: RootGone, Owner: OwnerGitOnly}, Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"a repository nested in the workspace with a gone root", func(t *testing.T) (string, string) {
			root := gitInit(t, plainDir(t))
			nested := gitInit(t, filepath.Join(root, "lib"))

			return root, database(t, filepath.Join(nested, "custom.db"), filepath.Join(root, "gone"))
		}, Facts{Root: RootGone, Owner: OwnerGitOnly, FileInWorkspace: true},
			Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"a hidden root", func(t *testing.T) (string, string) {
			return plainDir(t), database(t, filepath.Join(plainDir(t), "custom.db"), hiddenRoot(t))
		}, Facts{Root: RootHidden}, Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"a hidden root with a file of the workspace", func(t *testing.T) (string, string) {
			root := gitInit(t, plainDir(t))

			return root, database(t, filepath.Join(root, "custom.db"), hiddenRoot(t))
		}, Facts{Root: RootHidden, Owner: OwnerWorkspace, FileInWorkspace: true},
			Decision{BuildRefused, WriteRefused, RouteMoveHere}},
		{"a root that git widens", func(t *testing.T) (string, string) {
			top := gitInit(t, plainDir(t))
			recorded := filepath.Join(top, "R")

			require.NoError(t, os.Mkdir(recorded, 0o755))

			return plainDir(t), database(t, filepath.Join(recorded, "custom.db"), recorded)
		}, Facts{Root: RootWidened, Owner: OwnerRoot, RootOwnsFile: true},
			Decision{BuildRefused, WriteRefused, RouteAnotherDB}},
		{"a root that git widens with a file of the workspace", func(t *testing.T) (string, string) {
			root := gitInit(t, plainDir(t))
			recorded := filepath.Join(root, "R")

			require.NoError(t, os.Mkdir(recorded, 0o755))

			return root, database(t, filepath.Join(recorded, "custom.db"), recorded)
		}, Facts{Root: RootWidened, Owner: OwnerWorkspace, FileInWorkspace: true, RootOwnsFile: true},
			Decision{BuildRefused, WriteRefused, RouteMoveHere}},
		{"a file that is no database", func(t *testing.T) (string, string) {
			path := filepath.Join(plainDir(t), "garbage.db")

			require.NoError(t, os.WriteFile(path, []byte("this file is no database, and it is long enough"), 0o644))

			return plainDir(t), path
		}, Facts{Root: RootUnreadable}, Decision{BuildRefused, WriteRefused, RouteFixFile}},
		{"a default file that is no database", func(t *testing.T) (string, string) {
			root := plainDir(t)
			path := emptyFile(t, store.DefaultPath(root))

			require.NoError(t, os.WriteFile(path, []byte("this file is no database, and it is long enough"), 0o644))

			return root, path
		}, Facts{File: FileDefault, Root: RootUnreadable}, Decision{BuildRefused, WriteRefused, RouteFixFile}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", plainDir(t))

			root, db := tc.setup(t)
			state := Gather(root, db)

			assert.Equal(t, tc.facts, state.Facts)
			assert.Equal(t, tc.want, Decide(state.Facts))
			assert.False(t, state.DirRefusesWrites(), "only a directory that refuses writes gets its words")
		})
	}
}

// TestGatherNamesADirectoryThatRefusesWrites covers a database in a
// directory with mode 0555 and no sidecars. The facts and the decision
// stay those of an unreadable root, and only the words name the cause.
// The second case puts an ESC byte in the path, because -C and --db come
// from the command line. The cause and the route then reach a terminal or
// an MCP client.
func TestGatherNamesADirectoryThatRefusesWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory with mode 0555")
	}

	cases := []struct{ name, dir string }{
		{"a plain path", "w"},
		{"a path with a control byte", "w" + string(rune(0x1b)) + "[31mred"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(plainDir(t), tc.dir)
			require.NoError(t, os.MkdirAll(root, 0o755))

			db := database(t, store.DefaultPath(root), root)
			dir := filepath.Dir(db)

			require.NoError(t, os.Chmod(dir, 0o555))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

			state := Gather(root, db)

			assert.Equal(t, Facts{File: FileDefault, Root: RootUnreadable}, state.Facts)
			assert.Equal(t, Decision{BuildRefused, WriteRefused, RouteFixFile}, Decide(state.Facts))
			assert.True(t, state.DirRefusesWrites())

			cause := state.ReadOnlyDirCause()
			assert.NotContains(t, cause, "\x1b")
			assert.Equal(t, "the index database cannot be read, because its directory refuses writes ("+render.Sanitize(db)+
				": attempt to write a readonly database (1544)), and SQLite must create its WAL/SHM sidecars there to read it",
				cause)

			for _, surface := range []Surface{SurfaceBuild, SurfaceCommand, SurfaceConsole} {
				route := state.Route(RouteFixFile, surface)
				assert.NotContains(t, route, "\x1b")
				assert.Equal(t, "make "+render.Sanitize(dir)+" writable or use another --db", route)
			}

			// After the directory allows writes, the root reads again.
			require.NoError(t, os.Chmod(dir, 0o755))
			assert.Equal(t, RootSame, Gather(root, db).Root)
		})
	}
}

// TestGatherReadsANewerSchema covers the one fact that a pin write and a
// build both need before the open.
func TestGatherReadsANewerSchema(t *testing.T) {
	root := plainDir(t)
	db := database(t, store.DefaultPath(root), root)

	assert.False(t, Gather(root, db).NewerSchema)

	st, err := store.Open(db)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaSchemaVersion, "9999"))
	require.NoError(t, st.Close())

	assert.True(t, Gather(root, db).NewerSchema)
}

// TestGatherCommandCountsADirectoryInsideTheRootAsTheRoot covers the one
// difference from the console. Only a directory inside the recorded root
// without a mark changes its answer.
func TestGatherCommandCountsADirectoryInsideTheRootAsTheRoot(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root, custom string) (string, string)
		want  Write
	}{
		{"a directory inside the root without git", func(t *testing.T, root, custom string) (string, string) {
			inside := filepath.Join(root, "pkg")

			require.NoError(t, os.MkdirAll(inside, 0o755))

			return inside, custom
		}, WriteAllowed},
		{"a directory with its own git top level", func(t *testing.T, root, custom string) (string, string) {
			return gitInit(t, filepath.Join(root, "nested")), custom
		}, WriteRefused},
		{"the default database of a directory inside the root", func(t *testing.T, root, _ string) (string, string) {
			inside := filepath.Join(root, "pkg")

			return inside, database(t, store.DefaultPath(inside), root)
		}, WriteRefused},
		{"a directory outside the root", func(t *testing.T, _, custom string) (string, string) {
			return plainDir(t), custom
		}, WriteRefused},
		{"a file in the state directory of a directory inside the root", func(t *testing.T, root, _ string) (string, string) {
			inside := filepath.Join(root, "backup")

			return inside, database(t, filepath.Join(inside, store.DefaultDir, "renamed.db"), root)
		}, WriteRefused},
		{"a file beside the state directory of a directory inside the root", func(t *testing.T, root, _ string) (string, string) {
			inside := filepath.Join(root, "backup")

			require.NoError(t, os.MkdirAll(filepath.Join(inside, store.DefaultDir), 0o755))

			return inside, database(t, filepath.Join(inside, "custom.db"), root)
		}, WriteRefused},
		{"a file in the state directory of the root", func(t *testing.T, root, _ string) (string, string) {
			inside := filepath.Join(root, "pkg")

			require.NoError(t, os.MkdirAll(inside, 0o755))

			return inside, database(t, filepath.Join(root, store.DefaultDir, "renamed.db"), root)
		}, WriteAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", plainDir(t))

			root := plainDir(t)
			custom := database(t, filepath.Join(plainDir(t), "custom.db"), root)

			database(t, store.DefaultPath(root), root)

			dir, db := tc.setup(t, root, custom)

			assert.Equal(t, WriteRefused, Decide(Gather(dir, db).Facts).Write, "the console keeps this answer")
			assert.Equal(t, tc.want, Decide(GatherCommand(dir, db).Facts).Write)
		})
	}
}

// TestStateRouteNamesOneStepOnEachSurface pins the text of each route. A
// path with a space stays one shell word.
func TestStateRouteNamesOneStepOnEachSurface(t *testing.T) {
	state := State{Workspace: "/w", Recorded: "/r", DB: "/my files/x.db"}
	db := "'/my files/x.db'"

	cases := []struct {
		name                   string
		route                  Route
		build, command, screen string
	}{
		{"none", RouteNone, "", "", ""},
		{
			"index here", RouteIndexHere,
			"run `seamark index` in /w first", "run `seamark index` in /w first", "run seamark index here first",
		},
		{
			"index here with --db", RouteIndexHereDB,
			"run `seamark index --db " + db + "` in /w first",
			"run `seamark index --db " + db + "` in /w first",
			"run `seamark index --db " + db + "` in /w first",
		},
		{
			"move here", RouteMoveHere,
			"run `seamark index --move-root --db " + db + "` in /w to move the root",
			"run `seamark index --move-root --db " + db + "` in /w first",
			"run `seamark index --move-root --db " + db + "` in /w first",
		},
		{"run in the root", RouteRunInRoot, "run `seamark index` in /r", "run this command in /r", "open the console in /r"},
		{
			"run in the root with --db", RouteRunInRootDB,
			"run `seamark index --db " + db + "` in /r",
			"run this command in /r with --db " + db,
			"open the console in /r with this --db",
		},
		{"another file", RouteAnotherDB, "use another --db", "use another --db", "use another --db"},
		{
			"fix the file", RouteFixFile,
			"fix the file or use another --db", "fix the file or use another --db", "fix the file or use another --db",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.build, state.Route(tc.route, SurfaceBuild))
			assert.Equal(t, tc.command, state.Route(tc.route, SurfaceCommand))
			assert.Equal(t, tc.screen, state.Route(tc.route, SurfaceConsole))
		})
	}
}

// TestStatRootReadsEachState covers each answer. ENOTDIR means that
// nothing exists at the path. Another failure leaves the root unknown.
func TestStatRootReadsEachState(t *testing.T) {
	dir := plainDir(t)
	file := emptyFile(t, filepath.Join(dir, "file"))

	assert.Equal(t, RootFound, statRoot(dir))
	assert.Equal(t, RootGone, statRoot(filepath.Join(dir, "missing")))
	assert.Equal(t, RootGone, statRoot(filepath.Join(file, "below")))
	assert.Equal(t, RootHidden, statRoot(hiddenRoot(t)))
}

// TestCheckoutOfNamesTheNearestMark covers each answer. A nearer mark
// wins, so a nested repository owns its files. A link does not move a
// file, and .git is a file in a linked worktree.
func TestCheckoutOfNamesTheNearestMark(t *testing.T) {
	outer := checkout(t)
	nested := filepath.Join(outer, "a", "nested")
	file := emptyFile(t, filepath.Join(nested, "b", "custom.db"))

	emptyFile(t, filepath.Join(nested, ".git"))

	spelling := filepath.Join(outer, "link.db")
	require.NoError(t, os.Symlink(file, spelling))

	got, ok := checkoutOf(file)
	assert.True(t, ok)
	assert.Equal(t, nested, got)

	got, ok = checkoutOf(spelling)
	assert.True(t, ok)
	assert.Equal(t, nested, got, "a link does not move the file")

	got, ok = checkoutOf(filepath.Join(outer, "a", "x.db"))
	assert.True(t, ok)
	assert.Equal(t, outer, got)

	got, ok = checkoutOf(filepath.Join(outer, store.DefaultDir, "sub", "x.db"))
	assert.True(t, ok)
	assert.Equal(t, outer, got, "the state directory belongs to its checkout")

	_, ok = checkoutOf(filepath.Join(plainDir(t), "custom.db"))
	assert.False(t, ok)
}

// TestCheckoutOfSkipsTheHomeDirectory covers a mark in the home directory.
// An earlier run there leaves a state directory, and dotfiles can make it
// a git work tree. Neither says that the home directory owns a file below
// it. A checkout below the home directory still counts.
func TestCheckoutOfSkipsTheHomeDirectory(t *testing.T) {
	for _, mark := range []string{store.DefaultDir, ".git"} {
		t.Run(mark, func(t *testing.T) {
			home := plainDir(t)
			repo := filepath.Join(home, "work", "repo")

			require.NoError(t, os.Mkdir(filepath.Join(home, mark), 0o755))
			t.Setenv("HOME", home)
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))

			_, ok := checkoutOf(filepath.Join(home, "dbs", "shared.db"))
			assert.False(t, ok, "the home directory owns no file")

			got, ok := checkoutOf(filepath.Join(repo, "custom.db"))
			assert.True(t, ok)
			assert.Equal(t, repo, got)
		})
	}
}

// TestNearestDirectoryTestsTheFilesystemRoot covers a workspace at the
// filesystem root, as in a container. The test needs no mark at the real
// root, because the walk takes the test as a value.
func TestNearestDirectoryTestsTheFilesystemRoot(t *testing.T) {
	seen := []string{}

	got, ok := nearestDirectory("/a/b", func(dir string) bool {
		seen = append(seen, dir)

		return dir == "/"
	})

	assert.True(t, ok)
	assert.Equal(t, "/", got)
	assert.Equal(t, []string{"/a/b", "/a", "/"}, seen)

	_, ok = nearestDirectory("/a", func(string) bool { return false })
	assert.False(t, ok, "the walk ends at the filesystem root")
}
