//go:build darwin

package pinfile

// Tests of a pin file that an access control list protects. The CLI pin
// writes fail on such a file, so the console write must refuse it too.
// Only macOS sets an entry without extra tools.

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
	root := writeFile(t, emitted, 0o644)
	denyWrite(t, pinPath(root))

	d, c := retargetSingle(t, root)
	assert.Equal(t, "this user cannot write the file; seamark does not replace it", d.FileBlock)
	assert.Equal(t, integration.WriteRefusal(d.Guard), d.FileBlock, "the engine text")
	assert.Contains(t, c.Diff, "+    region: web\n", "the change is still shown")

	_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
	require.ErrorIs(t, err, ErrNotWritable)
	assert.Contains(t, err.Error(), d.FileBlock)
	assert.Equal(t, emitted, readPins(t, root))
}
