package reviews

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// firingLogWriters are the two entry points that append to the firing
// log: the v1 record of the hooks and change_set, and the v2 event.
var firingLogWriters = []struct {
	name   string
	append func(root string) error
}{
	{"RecordFiring", func(root string) error {
		return RecordFiring(root, "api/a.py", "Edit", []model.Lesson{{Region: "api", Symptom: "E501"}})
	}},
	{"RecordEvent", func(root string) error {
		_, err := RecordEvent(twoRecordEvent(root))

		return err
	}},
}

// skipWithoutLinks skips a test on a platform where a symbolic link
// needs extra rights.
func skipWithoutLinks(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on windows")
	}
}

// requireUnchanged asserts that the file at path still holds want.
func requireUnchanged(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(data), "nothing reaches the file outside the repository")
}

// TestFiringLogRefusesALinkedLog proves that both writers refuse a
// symbolic link at .seamark/lessons-audit.jsonl and write nothing to its
// target outside the repository. Before the fix, the append followed the
// link. The readers refuse the link too and leave the target as it is.
func TestFiringLogRefusesALinkedLog(t *testing.T) {
	skipWithoutLinks(t)

	for _, w := range firingLogWriters {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o755))

			sentinel := filepath.Join(t.TempDir(), "outside.jsonl")
			require.NoError(t, os.WriteFile(sentinel, nil, 0o644))
			require.NoError(t, os.Symlink(sentinel, AuditLogPath(root)))

			err := w.append(root)
			require.ErrorIs(t, err, errUnsafeAuditPath)
			assert.Contains(t, err.Error(), "is a symbolic link")
			requireUnchanged(t, sentinel, "")

			info, err := os.Lstat(AuditLogPath(root))
			require.NoError(t, err)
			assert.NotZero(t, info.Mode()&os.ModeSymlink, "the writer leaves the link in place")
		})
	}

	t.Run("the readers", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o755))

		const outside = `{"file":"outside.py","tool":"Edit","fired":[]}` + "\n"

		sentinel := filepath.Join(t.TempDir(), "outside.jsonl")
		require.NoError(t, os.WriteFile(sentinel, []byte(outside), 0o644))
		require.NoError(t, os.Symlink(sentinel, AuditLogPath(root)))

		firings, _, err := ReadFiringsCounted(root)
		require.ErrorIs(t, err, errUnsafeAuditPath, "a link is not an empty log")
		assert.Empty(t, firings)

		prefix, err := ReadAuditPrefix(root, 1<<20)
		require.ErrorIs(t, err, errUnsafeAuditPath)
		assert.Equal(t, AuditPrefix{}, prefix)

		requireUnchanged(t, sentinel, outside)
	})
}

// TestFiringLogRefusesALinkedStateDirectory proves that both writers
// refuse a .seamark that is a symbolic link to a directory outside the
// repository. Before the fix, the append created the log in that
// directory.
func TestFiringLogRefusesALinkedStateDirectory(t *testing.T) {
	skipWithoutLinks(t)

	for _, w := range firingLogWriters {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			require.NoError(t, os.Symlink(outside, filepath.Join(root, ".seamark")))

			err := w.append(root)
			require.ErrorIs(t, err, errUnsafeAuditPath)
			assert.Contains(t, err.Error(), "is not a real directory")

			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries, "no file lands in the linked directory")
		})
	}
}

// TestFiringLogIsOwnerOnly proves the modes of the writer. A new log is
// 0600 in a new 0700 .seamark, as the lesson-check audit stream creates
// them. An existing 0644 log from an earlier version is tightened to
// 0600 and keeps its records. An existing .seamark keeps its mode.
func TestFiringLogIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform has no POSIX modes")
	}

	for _, w := range firingLogWriters {
		t.Run(w.name+" creates the log", func(t *testing.T) {
			root := t.TempDir()

			require.NoError(t, w.append(root))
			requireFileMode(t, filepath.Join(root, ".seamark"), 0o700)
			requireFileMode(t, AuditLogPath(root), 0o600)

			firings, err := ReadFirings(root)
			require.NoError(t, err)
			assert.NotEmpty(t, firings, "a normal append still works")
		})
	}

	t.Run("an existing loose log is tightened", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".seamark")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		const earlier = `{"file":"earlier.py","tool":"Edit","fired":[]}` + "\n"

		require.NoError(t, os.WriteFile(AuditLogPath(root), []byte(earlier), 0o644))
		require.NoError(t, os.Chmod(AuditLogPath(root), 0o644))

		require.NoError(t, firingLogWriters[0].append(root))
		requireFileMode(t, AuditLogPath(root), 0o600)
		requireFileMode(t, dir, 0o755)

		firings, err := ReadFirings(root)
		require.NoError(t, err)
		require.Len(t, firings, 2)
		assert.Equal(t, "earlier.py", firings[0].File)
		assert.Equal(t, "api/a.py", firings[1].File)
	})
}

// requireFileMode asserts the permission bits of path.
func requireFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm(), path)
}
