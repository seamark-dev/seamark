//go:build unix

package settings

// The tests in this file change the umask, which only unix has. The
// umask is process-wide state, so no test of this package may run in
// parallel with them. None of them is parallel.

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A save that creates lessons.yaml gives it the permission of a plain
// write: 0644 through the umask. A replaced file keeps its exact mode
// instead (TestSaveKeepsTheFileMode).
func TestACreatedLessonsFileHonoursTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	root := t.TempDir()

	res := saveLessons(t, root, add("F541", ""))
	require.True(t, res.Created)

	info, err := os.Stat(lessonsPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a private umask yields a private file, the same as a plain write")
}
