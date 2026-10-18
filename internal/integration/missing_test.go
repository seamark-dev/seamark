package integration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMissingNamesOnlyAbsentPaths(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "api"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api", "h.go"), nil, 0o600))
	require.NoError(t, os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")))

	outside := filepath.Join(t.TempDir(), "absent.go")
	underFile := filepath.Join(root, "api", "h.go", "abs")

	got := Missing(root, []string{"api", "api/h.go", "web/new.go", "dangling", outside, filepath.Join(root, "api"), "api/h.go/sub", underFile})

	assert.Equal(t, []string{"web/new.go", outside, "api/h.go/sub", underFile}, got,
		"a dangling link exists; an absolute name is checked as it is; nothing exists below a file (ENOTDIR)")
	assert.Nil(t, Missing(root, nil))
}

// An error other than absence keeps the name out of the result, because
// the name can still exist (error-is-not-absence).
func TestMissingKeepsANameThatAnErrorHides(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")

	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "h.go"), nil, 0o600))
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	// The superuser and some platforms ignore the mode, so the test runs
	// only where the stat really fails with a permission error.
	if _, err := os.Lstat(filepath.Join(locked, "h.go")); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("the directory does not deny the stat here: %v", err)
	}

	assert.Nil(t, Missing(root, []string{"locked/h.go", "locked/new.go"}),
		"a denied name is never missing, even when no file has the name")
	assert.Equal(t, []string{"open/new.go"}, Missing(root, []string{"locked/h.go", "open/new.go"}))
}
