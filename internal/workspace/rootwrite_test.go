package workspace

import (
	"os"
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

// TestAbsoluteDBKeepsAnAbsolutePath covers both spellings. The console
// passes an absolute path, and the rule must not change it.
func TestAbsoluteDBKeepsAnAbsolutePath(t *testing.T) {
	dir := rootWriteDir(t)
	t.Chdir(dir)

	assert.Equal(t, filepath.Join(dir, "x.db"), Workspace{DBPath: filepath.Join(dir, "x.db")}.AbsoluteDB())
	assert.Equal(t, filepath.Join(dir, "x.db"), Workspace{DBPath: "x.db"}.AbsoluteDB())
}
