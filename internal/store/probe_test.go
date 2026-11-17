package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readOnlyDirDatabase returns a closed database whose directory refuses
// writes. A normal close removes the -wal and -shm files, so this is the
// state after `chmod 555 .seamark` on any index.
func readOnlyDirDatabase(t *testing.T) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory with mode 0555")
	}

	dir := filepath.Join(t.TempDir(), ".seamark")
	db := filepath.Join(dir, "index.db")

	st, err := Open(db)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(MetaRepoRoot, "/some/root"))
	require.NoError(t, st.Close())

	require.NoFileExists(t, db+"-wal", "a normal close removes the sidecars")
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	return db
}

func TestIsReadOnlyDirectory(t *testing.T) {
	db := readOnlyDirDatabase(t)

	_, err := ProbeRoot(db)
	assert.True(t, IsReadOnlyDirectory(err), "ProbeRoot: %v", err)

	_, err = ProbeVersion(db)
	assert.True(t, IsReadOnlyDirectory(err), "ProbeVersion: %v", err)

	ro, err := OpenReadOnly(db)
	if ro != nil {
		_ = ro.Close()
	}

	assert.True(t, IsReadOnlyDirectory(err), "OpenReadOnly wraps the driver error: %v", err)

	rw, err := Open(db)
	if rw != nil {
		_ = rw.Close()
	}

	assert.True(t, IsReadOnlyDirectory(err), "Open: %v", err)

	// Other failures are no read-only directory.
	assert.False(t, IsReadOnlyDirectory(nil))
	assert.False(t, IsReadOnlyDirectory(errors.New("attempt to write a readonly database (1544)")))

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	require.NoError(t, os.WriteFile(garbage, []byte("this file is no database, and it is long enough"), 0o644))

	_, err = ProbeRoot(garbage)
	require.Error(t, err)
	assert.False(t, IsReadOnlyDirectory(err), "a file that is no database: %v", err)
}

func TestProbeRootReadsAfterTheDirectoryAllowsWrites(t *testing.T) {
	db := readOnlyDirDatabase(t)

	require.NoError(t, os.Chmod(filepath.Dir(db), 0o755))

	root, err := ProbeRoot(db)
	require.NoError(t, err)
	assert.Equal(t, "/some/root", root)
}
