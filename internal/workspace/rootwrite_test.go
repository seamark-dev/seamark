package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// rootWriteDir returns a new directory by its resolved path, so a test
// compares the spelling that the rule sees.
func rootWriteDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

// rootWriteDB creates a file at path. The rule compares files and reads
// no content, so an empty file is enough.
func rootWriteDB(t *testing.T, path string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	return path
}

// TestCheckRootWrite pins the answer of the rule for each state. The
// console and the CLI read this one answer, so each state holds for both.
func TestCheckRootWrite(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) (Workspace, string)
		want  RootWrite
	}{
		{"a database that records this workspace", func(t *testing.T) (Workspace, string) {
			root := rootWriteDir(t)

			return Workspace{Root: root, DBPath: rootWriteDB(t, store.DefaultPath(root))}, root
		}, RootWriteAllowed},
		{"a database that records no root", func(t *testing.T) (Workspace, string) {
			root := rootWriteDir(t)

			return Workspace{Root: root, DBPath: rootWriteDB(t, store.DefaultPath(root))}, ""
		}, RootWriteAllowed},
		{"a link to this workspace as the recorded root", func(t *testing.T) (Workspace, string) {
			root := rootWriteDir(t)
			link := filepath.Join(rootWriteDir(t), "old")

			require.NoError(t, os.Symlink(root, link))

			return Workspace{Root: root, DBPath: rootWriteDB(t, store.DefaultPath(root))}, link
		}, RootWriteAllowed},
		{"the default database of a copy", func(t *testing.T) (Workspace, string) {
			origin, copied := rootWriteDir(t), rootWriteDir(t)

			rootWriteDB(t, store.DefaultPath(origin))

			return Workspace{Root: copied, DBPath: rootWriteDB(t, store.DefaultPath(copied))}, origin
		}, RootWriteCopied},
		{"a copy when the origin has no default database", func(t *testing.T) (Workspace, string) {
			origin, copied := rootWriteDir(t), rootWriteDir(t)

			return Workspace{Root: copied, DBPath: rootWriteDB(t, store.DefaultPath(copied))}, origin
		}, RootWriteCopied},
		{"a copy with a relative database path", func(t *testing.T) (Workspace, string) {
			origin, copied := rootWriteDir(t), rootWriteDir(t)

			rootWriteDB(t, store.DefaultPath(copied))
			t.Chdir(copied)

			return Workspace{Root: copied, DBPath: filepath.Join(store.DefaultDir, store.DefaultDBName)}, origin
		}, RootWriteCopied},
		{"a copy whose recorded root is gone", func(t *testing.T) (Workspace, string) {
			copied := rootWriteDir(t)
			gone := filepath.Join(rootWriteDir(t), "gone")

			return Workspace{Root: copied, DBPath: rootWriteDB(t, store.DefaultPath(copied))}, gone
		}, RootWriteCopied},
		{"the default database of the recorded root", func(t *testing.T) (Workspace, string) {
			origin := rootWriteDir(t)

			return Workspace{Root: rootWriteDir(t), DBPath: rootWriteDB(t, store.DefaultPath(origin))}, origin
		}, RootWriteAllowed},
		{"a linked database of the recorded root", func(t *testing.T) (Workspace, string) {
			origin, tree := rootWriteDir(t), rootWriteDir(t)
			link := store.DefaultPath(tree)

			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
			require.NoError(t, os.Symlink(rootWriteDB(t, store.DefaultPath(origin)), link))

			return Workspace{Root: tree, DBPath: link}, origin
		}, RootWriteAllowed},
		{"a custom database of a root without a default database", func(t *testing.T) (Workspace, string) {
			custom := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

			return Workspace{Root: rootWriteDir(t), DBPath: custom}, rootWriteDir(t)
		}, RootWriteAllowed},
		{"a custom database beside a default database", func(t *testing.T) (Workspace, string) {
			origin := rootWriteDir(t)
			custom := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

			rootWriteDB(t, store.DefaultPath(origin))

			return Workspace{Root: rootWriteDir(t), DBPath: custom}, origin
		}, RootWriteUnowned},
		{"a custom database of a root that is gone", func(t *testing.T) (Workspace, string) {
			custom := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

			return Workspace{Root: rootWriteDir(t), DBPath: custom}, filepath.Join(rootWriteDir(t), "gone")
		}, RootWriteUnowned},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, recorded := tc.setup(t)

			assert.Equal(t, tc.want, ws.CheckRootWrite(recorded))
		})
	}
}

// TestCheckCommandRootWrite pins the one difference from the console.
// Only a directory inside the recorded root without a git top level
// changes its answer, and only from RootWriteUnowned. console holds the
// answer of CheckRootWrite, which must stay as it is for that state.
func TestCheckCommandRootWrite(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, root, custom string) Workspace
		console RootWrite
		command RootWrite
	}{
		{"a directory inside the root without git", func(t *testing.T, root, custom string) Workspace {
			inside := filepath.Join(root, "pkg")

			require.NoError(t, os.MkdirAll(inside, 0o755))

			return Workspace{Root: inside, DBPath: custom}
		}, RootWriteUnowned, RootWriteAllowed},
		{"a directory with its own git top level", func(t *testing.T, root, custom string) Workspace {
			nested := filepath.Join(root, "nested")

			require.NoError(t, os.MkdirAll(nested, 0o755))

			out, err := exec.Command("git", "init", "-q", nested).CombinedOutput()
			require.NoError(t, err, string(out))

			return Workspace{Root: nested, DBPath: custom}
		}, RootWriteUnowned, RootWriteUnowned},
		{"the default database of a directory inside the root", func(t *testing.T, root, _ string) Workspace {
			inside := filepath.Join(root, "pkg")

			return Workspace{Root: inside, DBPath: rootWriteDB(t, store.DefaultPath(inside))}
		}, RootWriteCopied, RootWriteCopied},
		{"a directory outside the root", func(t *testing.T, _, custom string) Workspace {
			return Workspace{Root: rootWriteDir(t), DBPath: custom}
		}, RootWriteUnowned, RootWriteUnowned},
		{"a file in the state directory of a directory inside the root", func(t *testing.T, root, _ string) Workspace {
			inside := filepath.Join(root, "backup")

			return Workspace{Root: inside, DBPath: rootWriteDB(t, filepath.Join(inside, store.DefaultDir, "renamed.db"))}
		}, RootWriteUnowned, RootWriteUnowned},
		{"a file beside the state directory of a directory inside the root", func(t *testing.T, root, _ string) Workspace {
			inside := filepath.Join(root, "backup")

			rootWriteDB(t, filepath.Join(inside, store.DefaultDir, "renamed.db"))

			return Workspace{Root: inside, DBPath: rootWriteDB(t, filepath.Join(inside, "custom.db"))}
		}, RootWriteUnowned, RootWriteUnowned},
		{"a file below the state directory of a directory inside the root", func(t *testing.T, root, _ string) Workspace {
			inside := filepath.Join(root, "backup")

			return Workspace{Root: inside, DBPath: rootWriteDB(t, filepath.Join(inside, store.DefaultDir, "sub", "x.db"))}
		}, RootWriteUnowned, RootWriteUnowned},
		{"a file in the state directory of the root", func(t *testing.T, root, _ string) Workspace {
			inside := filepath.Join(root, "pkg")

			require.NoError(t, os.MkdirAll(inside, 0o755))

			return Workspace{Root: inside, DBPath: rootWriteDB(t, filepath.Join(root, store.DefaultDir, "renamed.db"))}
		}, RootWriteUnowned, RootWriteAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := rootWriteDir(t)
			custom := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

			rootWriteDB(t, store.DefaultPath(root))

			ws := tc.setup(t, root, custom)

			assert.Equal(t, tc.console, ws.CheckRootWrite(root), "the console keeps this answer")
			assert.Equal(t, tc.command, ws.CheckCommandRootWrite(root))
		})
	}
}

// TestCustomDatabaseNamesOnlyAFileThatARouteCanKeep covers the route.
// The route of a custom file keeps --db, and a file of another checkout
// loses it, because a command in the root would then write two
// repositories.
func TestCustomDatabaseNamesOnlyAFileThatARouteCanKeep(t *testing.T) {
	origin, copied := rootWriteDir(t), rootWriteDir(t)

	rootWriteDB(t, store.DefaultPath(origin))

	custom := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))
	renamed := rootWriteDB(t, filepath.Join(copied, store.DefaultDir, "renamed.db"))

	third := rootWriteDir(t)

	assert.True(t, Workspace{Root: third, DBPath: custom}.CustomDatabase(origin))
	assert.False(t, Workspace{Root: third, DBPath: renamed}.CustomDatabase(origin), "a file of another checkout")
	assert.False(t, Workspace{Root: third, DBPath: store.DefaultPath(origin)}.CustomDatabase(origin),
		"the default database of the root")
	assert.False(t, Workspace{Root: copied, DBPath: rootWriteDB(t, store.DefaultPath(copied))}.CustomDatabase(origin),
		"the default database of this workspace")
}

// TestMoveRootRouteNamesOnlyAFileOfThisWorkspace covers the route of a
// custom file that belongs to this workspace. A route to the recorded
// root with that file would write two repositories, so CustomDatabase
// must not name it either.
func TestMoveRootRouteNamesOnlyAFileOfThisWorkspace(t *testing.T) {
	origin, copied := rootWriteDir(t), rootWriteDir(t)

	rootWriteDB(t, store.DefaultPath(origin))

	nested := filepath.Join(origin, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	out, err := exec.Command("git", "-C", nested, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	plain := filepath.Join(origin, "plain")

	tree := rootWriteDir(t)

	out, err = exec.Command("git", "-C", tree, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	treeFile := rootWriteDB(t, filepath.Join(tree, "custom.db"))
	custom := rootWriteDB(t, filepath.Join(copied, "custom.db"))
	ownFile := rootWriteDB(t, filepath.Join(nested, "custom.db"))
	plainFile := rootWriteDB(t, filepath.Join(plain, "custom.db"))
	rootFile := rootWriteDB(t, filepath.Join(origin, "custom.db"))
	renamed := rootWriteDB(t, filepath.Join(copied, store.DefaultDir, "renamed.db"))
	outside := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

	route := func(db, root string) string {
		return "run `seamark index --move-root --db " + db + "` in " + root + " first"
	}

	cases := []struct {
		name       string
		ws         Workspace
		wantRoute  string
		wantCustom bool
	}{
		{"a custom file of a copy without git", Workspace{Root: copied, DBPath: custom}, route(custom, copied), false},
		{"a custom file of a copy with git", Workspace{Root: tree, DBPath: treeFile}, route(treeFile, tree), false},
		{"a file of a repository inside the root", Workspace{Root: nested, DBPath: ownFile}, route(ownFile, nested), false},
		{"a file of a directory inside the root without git", Workspace{Root: plain, DBPath: plainFile}, "", true},
		{"a file of a plain directory", Workspace{Root: filepath.Dir(outside), DBPath: outside}, "", true},
		{"a file of the root from the parent", Workspace{Root: filepath.Dir(origin), DBPath: rootFile}, "", true},
		{"a file of the root from a third directory", Workspace{Root: rootWriteDir(t), DBPath: rootFile}, "", true},
		{"a file in the state directory of a copy", Workspace{Root: copied, DBPath: renamed}, route(renamed, copied), false},
		{"a file outside any checkout", Workspace{Root: rootWriteDir(t), DBPath: outside}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRoute, tc.ws.MoveRootRoute(origin))
			assert.Equal(t, tc.wantCustom, tc.ws.CustomDatabase(origin))
		})
	}
}

// TestCustomDatabaseLeavesAFileOfAnotherWorkTree covers a custom file of
// a copy from a third directory. git names the owner of that file, so a
// route to the recorded root must not keep it. A custom file inside the
// recorded root stays a file that the route names.
func TestCustomDatabaseLeavesAFileOfAnotherWorkTree(t *testing.T) {
	origin, copied, third := rootWriteDir(t), rootWriteDir(t), rootWriteDir(t)

	rootWriteDB(t, store.DefaultPath(origin))

	for _, dir := range []string{origin, copied} {
		out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput()
		require.NoError(t, err, string(out))
	}

	rootWriteDB(t, store.DefaultPath(copied))

	other := rootWriteDB(t, filepath.Join(copied, "custom.db"))
	own := rootWriteDB(t, filepath.Join(origin, "custom.db"))

	home := rootWriteDir(t)

	out, err := exec.Command("git", "-C", home, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	shared := rootWriteDB(t, filepath.Join(home, ".cache", "seamark", "shared.db"))

	assert.False(t, Workspace{Root: third, DBPath: other}.CustomDatabase(origin))
	assert.True(t, Workspace{Root: third, DBPath: own}.CustomDatabase(origin))
	assert.True(t, Workspace{Root: third, DBPath: shared}.CustomDatabase(origin),
		"a git work tree without a state directory owns no database")
	assert.Empty(t, Workspace{Root: third, DBPath: shared}.OwnerRoute(origin))
	assert.Contains(t, Workspace{Root: third, DBPath: other}.OwnerRoute(origin), "` in "+copied+" first")
}

// TestCustomDatabaseLeavesAFileOfACheckoutOutsideGit covers the same file
// without git. The state directory names the checkout, also for a file
// in a directory below it. A plain directory names no checkout.
func TestCustomDatabaseLeavesAFileOfACheckoutOutsideGit(t *testing.T) {
	origin, copied, third := rootWriteDir(t), rootWriteDir(t), rootWriteDir(t)

	rootWriteDB(t, store.DefaultPath(origin))
	require.NoError(t, os.MkdirAll(filepath.Join(copied, store.DefaultDir), 0o755))

	beside := rootWriteDB(t, filepath.Join(copied, "custom.db"))
	below := rootWriteDB(t, filepath.Join(copied, "state", "deep", "custom.db"))
	own := rootWriteDB(t, filepath.Join(origin, "custom.db"))
	plain := rootWriteDB(t, filepath.Join(rootWriteDir(t), "custom.db"))

	assert.False(t, Workspace{Root: third, DBPath: beside}.CustomDatabase(origin))
	assert.False(t, Workspace{Root: third, DBPath: below}.CustomDatabase(origin))
	assert.True(t, Workspace{Root: third, DBPath: own}.CustomDatabase(origin))
	assert.True(t, Workspace{Root: third, DBPath: plain}.CustomDatabase(origin))
}

// TestAbsoluteDBKeepsAnAbsolutePath covers both spellings. The console
// passes an absolute path, and the rule must not change it.
func TestAbsoluteDBKeepsAnAbsolutePath(t *testing.T) {
	dir := rootWriteDir(t)
	t.Chdir(dir)

	assert.Equal(t, filepath.Join(dir, "x.db"), Workspace{DBPath: filepath.Join(dir, "x.db")}.AbsoluteDB())
	assert.Equal(t, filepath.Join(dir, "x.db"), Workspace{DBPath: "x.db"}.AbsoluteDB())
}
