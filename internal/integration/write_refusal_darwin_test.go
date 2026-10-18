//go:build darwin

package integration

// Tests of the write refusal for a file that an access control list
// protects. The owner write bit stays set on such a file. A rename needs
// write access to the directory only, so the engine must ask access(2)
// and not the mode bits. Only macOS sets an entry without extra tools.

import (
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyWrite adds an access control entry that denies this user the
// write of path. It skips the test where the entry has no effect, so a
// pass always proves the refusal.
func denyWrite(t *testing.T, path string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("the superuser ignores a deny entry")
	}

	me, err := user.Current()
	require.NoError(t, err)

	if out, err := exec.Command("chmod", "+a", me.Username+" deny write,append", path).CombinedOutput(); err != nil {
		t.Skipf("the file system takes no access control entry: %s", out)
	}

	// The cleanup removes the entry before the temporary directory goes.
	t.Cleanup(func() { _ = exec.Command("chmod", "-N", path).Run() })

	fh, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err == nil {
		_ = fh.Close()
	}

	require.ErrorIs(t, err, fs.ErrPermission, "a plain write fails on the file")
}

func TestWriteRefusalHonoursAnAccessControlList(t *testing.T) {
	const denied = "this user cannot write the file; seamark does not replace it"

	t.Run("the read records the refusal", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, "doc.txt", "old\n")
		denyWrite(t, filepath.Join(root, "doc.txt"))

		guard, _, err := ReadInput(root, "doc.txt")
		require.NoError(t, err)
		assert.True(t, guard.Denied)
		assert.NotZero(t, guard.Mode&0o200, "the owner write bit stays set")
		assert.Equal(t, denied, WriteRefusal(guard))
	})

	t.Run("apply refuses the file", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, "doc.txt", "old\n")
		denyWrite(t, filepath.Join(root, "doc.txt"))

		plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{guardedCommon("doc.txt", []byte("new\n"))}})

		_, err := ApplySetup(plan, ApplyOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), denied)
		assert.Equal(t, "old\n", readRel(t, root, "doc.txt"))
	})

	t.Run("an entry added after the plan makes the plan stale", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, "doc.txt", "old\n")

		plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{guardedCommon("doc.txt", []byte("new\n"))}})
		require.Empty(t, WriteRefusal(plan.Reads[0]))

		denyWrite(t, filepath.Join(root, "doc.txt"))

		_, err := ApplySetup(plan, ApplyOptions{})
		require.ErrorIs(t, err, ErrStalePlan)
		assert.Equal(t, "old\n", readRel(t, root, "doc.txt"))
	})
}
