package privatedir_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/privatedir"
)

// errUnsafe stands for the sentinel of a caller, such as
// archive.ErrUnsafePath. The helpers must wrap the value they receive.
var errUnsafe = errors.New("unsafe test path")

// posixModes is false where the platform has no POSIX modes. There
// TightenMode is best effort, so the tests skip the mode assertions.
const posixModes = runtime.GOOS != "windows"

// assertMode checks the permission bits of path where the platform
// enforces them.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	if !posixModes {
		return
	}

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm(), "mode of %s", path)
}

func TestPrivateDirEnsureRealDir(t *testing.T) {
	t.Run("absent directory is created with the mode", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "private")

		require.NoError(t, privatedir.EnsureRealDir(dir, 0o700, errUnsafe, "test: create"))

		info, err := os.Lstat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assertMode(t, dir, 0o700)
	})

	t.Run("existing directory is kept as it is", func(t *testing.T) {
		if !posixModes {
			t.Skip("POSIX modes are not enforced on this platform")
		}

		dir := filepath.Join(t.TempDir(), "private")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		// EnsureRealDir never changes a mode. The caller calls TightenMode
		// for that, because a parent such as .seamark keeps 0755.
		require.NoError(t, privatedir.EnsureRealDir(dir, 0o700, errUnsafe, "test: create"))
		assertMode(t, dir, 0o755)
	})

	t.Run("symbolic link is refused", func(t *testing.T) {
		elsewhere := t.TempDir()
		dir := filepath.Join(t.TempDir(), "private")
		require.NoError(t, os.Symlink(elsewhere, dir))

		err := privatedir.EnsureRealDir(dir, 0o700, errUnsafe, "test: create")
		require.ErrorIs(t, err, errUnsafe)
		assert.Equal(t, "unsafe test path: "+dir+" is not a real directory", err.Error())

		entries, err := os.ReadDir(elsewhere)
		require.NoError(t, err)
		assert.Empty(t, entries, "nothing may be written through the link")
	})

	t.Run("regular file is refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "private")
		require.NoError(t, os.WriteFile(dir, []byte("x"), 0o600))

		err := privatedir.EnsureRealDir(dir, 0o700, errUnsafe, "test: create")
		require.ErrorIs(t, err, errUnsafe)
		assert.Equal(t, "unsafe test path: "+dir+" is not a real directory", err.Error())
	})

	t.Run("failed create starts with the caller's prefix", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing", "private")

		err := privatedir.EnsureRealDir(dir, 0o700, errUnsafe, "test: create")
		require.ErrorIs(t, err, os.ErrNotExist)
		assert.NotErrorIs(t, err, errUnsafe, "a missing parent is not an unsafe path")

		var pathErr *os.PathError
		require.ErrorAs(t, err, &pathErr)
		assert.Equal(t, "test: create "+dir+": "+pathErr.Error(), err.Error())
	})
}

func TestPrivateDirEnsureGitignore(t *testing.T) {
	const body = "*\n"

	t.Run("absent file is written with the mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".gitignore")

		require.NoError(t, privatedir.EnsureGitignore(path, body, 0o600, errUnsafe))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, body, string(data))
		assertMode(t, path, 0o600)
	})

	t.Run("same body with a loose mode is tightened", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".gitignore")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		require.NoError(t, os.Chmod(path, 0o644))

		require.NoError(t, privatedir.EnsureGitignore(path, body, 0o600, errUnsafe))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, body, string(data))
		assertMode(t, path, 0o600)
	})

	t.Run("different body is replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".gitignore")
		require.NoError(t, os.WriteFile(path, []byte("bin/\n"), 0o644))
		require.NoError(t, os.Chmod(path, 0o644))

		require.NoError(t, privatedir.EnsureGitignore(path, body, 0o600, errUnsafe))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, body, string(data))
		assertMode(t, path, 0o600)
	})

	t.Run("symbolic link is refused", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "elsewhere")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))

		path := filepath.Join(t.TempDir(), ".gitignore")
		require.NoError(t, os.Symlink(target, path))

		err := privatedir.EnsureGitignore(path, body, 0o600, errUnsafe)
		require.ErrorIs(t, err, errUnsafe)
		assert.Equal(t, "unsafe test path: "+path+" is a symbolic link", err.Error())

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data), "nothing may be written through the link")
	})
}

func TestPrivateDirTightenMode(t *testing.T) {
	if !posixModes {
		t.Skip("POSIX modes are not enforced on this platform")
	}

	root := t.TempDir()

	dir := filepath.Join(root, "private")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))

	file := filepath.Join(dir, "data")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	require.NoError(t, os.Chmod(file, 0o644))

	require.NoError(t, privatedir.TightenMode(dir, 0o700))
	require.NoError(t, privatedir.TightenMode(file, 0o600))

	assertMode(t, dir, 0o700)
	assertMode(t, file, 0o600)

	err := privatedir.TightenMode(filepath.Join(root, "missing"), 0o600)
	assert.ErrorIs(t, err, os.ErrNotExist, "a missing path is an error, not a silent pass")
}
