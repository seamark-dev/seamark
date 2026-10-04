//go:build darwin

package settings

// Tests of a settings file that an access control list protects. The
// editor shares the write engine of the pin file, so it must refuse the
// same file with the same text. Only macOS sets an entry without extra
// tools.

import (
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

// denyWrite adds an access control entry that denies this user the
// write of path. The owner write bit stays set. It skips the test where
// the entry has no effect, so a pass always proves the refusal.
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

func TestAFileThisUserCannotWriteIsRefused(t *testing.T) {
	root := withConfig(t, richConfig)
	denyWrite(t, configPath(root))

	snap := inspect(t, root)
	require.ErrorIs(t, snap.Blocked, ErrBlocked)

	var blocked *BlockedError
	require.ErrorAs(t, snap.Blocked, &blocked)
	assert.Equal(t, "this user cannot write the file; seamark does not replace it", blocked.Reason)
	assert.Equal(t, integration.WriteRefusal(snap.Guard), blocked.Reason, "the engine text")

	_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
	require.ErrorIs(t, err, ErrBlocked)
	assert.Equal(t, richConfig, readConfig(t, root))
}
