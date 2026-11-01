package history

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outsideRepo returns a fresh directory that git cannot place in a
// repository. The ceiling stops the git search above the directory, so
// a temporary directory inside some checkout still counts as outside.
// Git resolves symbolic links in the ceiling, so one form is enough.
func outsideRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	return dir
}

// realPath resolves symbolic links. Git prints the resolved top level,
// and the macOS temporary directory is a symbolic link.
func realPath(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return resolved
}

// TestTopLevelTellsARepositoryFromAPlainDirectory covers the answer that
// ResolveRoot hides: it returns a path in both cases.
func TestTopLevelTellsARepositoryFromAPlainDirectory(t *testing.T) {
	root, commit := gitRepo(t)
	commit("init", "pkg/a.go")

	top, ok := TopLevel(filepath.Join(root, "pkg"))
	assert.True(t, ok)
	assert.Equal(t, realPath(t, root), top)

	_, ok = TopLevel(outsideRepo(t))
	assert.False(t, ok, "git knows no top level outside a repository")

	_, ok = TopLevel(filepath.Join(outsideRepo(t), "missing"))
	assert.False(t, ok, "a missing directory has no top level")
}

func TestResolveRoot(t *testing.T) {
	t.Run("a subdirectory widens to the git top level", func(t *testing.T) {
		root, commit := gitRepo(t)
		commit("init", "pkg/a.go")

		got, err := ResolveRoot(filepath.Join(root, "pkg"))
		require.NoError(t, err)

		assert.Equal(t, realPath(t, root), got, "index.Run and the console must name one database for the whole repository")
	})

	t.Run("a directory outside git stays as its absolute path", func(t *testing.T) {
		dir := outsideRepo(t)

		got, err := ResolveRoot(dir)
		require.NoError(t, err, "a directory outside a repository is still a workspace")

		assert.Equal(t, dir, got)
	})

	t.Run("a missing directory falls back without an error", func(t *testing.T) {
		missing := filepath.Join(outsideRepo(t), "missing")

		got, err := ResolveRoot(missing)
		require.NoError(t, err, "workspace.Locate relies on the fallback and checks the directory itself")

		assert.Equal(t, missing, got)
	})

	t.Run("an empty dir means the current directory", func(t *testing.T) {
		dir := outsideRepo(t)
		t.Chdir(dir)

		got, err := ResolveRoot("")
		require.NoError(t, err)

		assert.Equal(t, realPath(t, dir), realPath(t, got))
	})

	t.Run("a relative dir resolves against the current directory", func(t *testing.T) {
		root, commit := gitRepo(t)
		commit("init", "pkg/a.go")
		t.Chdir(root)

		got, err := ResolveRoot("pkg")
		require.NoError(t, err)

		assert.Equal(t, realPath(t, root), got)
	})
}
