package pinfile

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

// readPins returns the pin file text under root.
func readPins(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(pinPath(root))
	require.NoError(t, err)

	return string(data)
}

// retargetSingle reads the file under root and plans the retarget of the
// entry "single" to web.
func retargetSingle(t *testing.T, root string) (*Document, *Change) {
	t.Helper()

	d, err := Read(root)
	require.NoError(t, err)

	c, err := Plan(d, []Edit{{Ref: entryOf(t, d, "single").Ref(), Regions: []string{"web"}}}, nil)
	require.NoError(t, err)

	return d, c
}

func TestPrepareAndApplyWriteTheCandidate(t *testing.T) {
	root := writeFile(t, emitted, 0o640)
	d, c := retargetSingle(t, root)

	p, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(c.After), p.Written)
	assert.Equal(t, d.Guard, p.Guard)
	assert.Equal(t, emitted, readPins(t, root), "Prepare writes nothing")

	require.NoError(t, p.Apply())
	assert.Equal(t, string(c.After), readPins(t, root))

	info, err := os.Stat(pinPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the mode stays")

	require.ErrorIs(t, p.Apply(), ErrStale, "a second apply finds the file changed")
}

func TestPrepareComparesOnlyExistenceAndContent(t *testing.T) {
	// A mode change after the read is not a content change: the write
	// goes ahead and keeps the new mode.
	root := writeFile(t, emitted, 0o644)
	d, c := retargetSingle(t, root)
	require.NoError(t, os.Chmod(pinPath(root), 0o600))

	p, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
	require.NoError(t, err)
	require.NoError(t, p.Apply())

	info, err := os.Stat(pinPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestStaleFilesAreNeverWritten(t *testing.T) {
	const other = "pin:\n  - rule: other writer\n"

	t.Run("changed before Prepare", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, c := retargetSingle(t, root)
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o644))

		_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, other, readPins(t, root))
	})

	t.Run("changed between Prepare and Apply", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, c := retargetSingle(t, root)

		p, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o644))

		require.ErrorIs(t, p.Apply(), ErrStale)
		assert.Equal(t, other, readPins(t, root))
	})

	t.Run("removed before Prepare", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, c := retargetSingle(t, root)
		require.NoError(t, os.Remove(pinPath(root)))

		_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrStale)
		assert.NoFileExists(t, pinPath(root))
	})

	t.Run("an absent file is never created", func(t *testing.T) {
		root := t.TempDir()

		_, err := Prepare(integration.Builtin(), root, integration.FileGuard{}, []byte("pin:\n"))
		require.ErrorIs(t, err, ErrStale)
		assert.NoFileExists(t, pinPath(root))
	})
}

func TestPrepareRefusesWhatTheEngineDoesNotReplace(t *testing.T) {
	t.Run("symlinked file", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real.yaml")
		require.NoError(t, os.WriteFile(target, []byte(emitted), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.Symlink("../real.yaml", pinPath(root)))

		d, c := retargetSingle(t, root)
		assert.Contains(t, d.FileBlock, "symlink")
		assert.Contains(t, c.Diff, "+    region: web\n", "the change is still shown")

		_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrNotWritable)
		assert.Contains(t, err.Error(), "symlink at .seamark/lessons.yaml")

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, emitted, string(data), "the link target is untouched")
	})

	t.Run("read-only file", func(t *testing.T) {
		root := writeFile(t, emitted, 0o444)
		d, c := retargetSingle(t, root)

		_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrNotWritable)
		assert.Contains(t, err.Error(), "read-only")
		assert.Equal(t, emitted, readPins(t, root))
	})

	t.Run("bytes the reader rejects", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		_, err = Prepare(integration.Builtin(), root, d.Guard, []byte("hook_delivery: sometimes\n"))
		require.ErrorIs(t, err, ErrUnverified)
	})

	t.Run("bytes the file already holds", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		_, err = Prepare(integration.Builtin(), root, d.Guard, []byte(emitted))
		require.ErrorIs(t, err, ErrNoOp)
	})

	t.Run("no registry and no root", func(t *testing.T) {
		root := writeFile(t, emitted, 0o644)
		d, c := retargetSingle(t, root)

		_, err := Prepare(nil, root, d.Guard, c.After)
		require.Error(t, err)

		_, err = Prepare(integration.Builtin(), "", d.Guard, c.After)
		require.Error(t, err)

		var p *Prepared
		require.Error(t, p.Apply())
	})
}

func TestRestoreNeedsTheWrittenBytes(t *testing.T) {
	// written applies the retarget and returns the root and the hash of
	// the written bytes.
	written := func(t *testing.T) (string, [32]byte) {
		t.Helper()

		root := writeFile(t, emitted, 0o640)
		d, c := retargetSingle(t, root)

		p, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.NoError(t, err)
		require.NoError(t, p.Apply())

		return root, p.Written
	}

	t.Run("the file holds the written bytes", func(t *testing.T) {
		root, sum := written(t)

		require.NoError(t, Restore(integration.Builtin(), root, sum, []byte(emitted)))
		assert.Equal(t, emitted, readPins(t, root))

		info, err := os.Stat(pinPath(root))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	})

	t.Run("another writer changed the file", func(t *testing.T) {
		root, sum := written(t)
		const other = "pin:\n  - rule: later edit\n"
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o640))

		err := Restore(integration.Builtin(), root, sum, []byte(emitted))
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "another writer changed the file; nothing restored")
		assert.Equal(t, other, readPins(t, root), "the later edit is never overwritten")
	})

	t.Run("the file is gone", func(t *testing.T) {
		root, sum := written(t)
		require.NoError(t, os.Remove(pinPath(root)))

		require.ErrorIs(t, Restore(integration.Builtin(), root, sum, []byte(emitted)), ErrStale)
		assert.NoFileExists(t, pinPath(root))
	})

	t.Run("the file became read-only", func(t *testing.T) {
		root, sum := written(t)
		require.NoError(t, os.Chmod(pinPath(root), 0o444))

		require.ErrorIs(t, Restore(integration.Builtin(), root, sum, []byte(emitted)), ErrNotWritable)
	})

	t.Run("another writer already put the original back", func(t *testing.T) {
		// The file and the ledger agree again, but this restore wrote
		// nothing. It must not report a restore (D-12).
		root, sum := written(t)
		require.NoError(t, os.WriteFile(pinPath(root), []byte(emitted), 0o640))

		err := Restore(integration.Builtin(), root, sum, []byte(emitted))
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "another writer changed the file; nothing restored")
		assert.Equal(t, emitted, readPins(t, root))
	})

	t.Run("the guard is the hash of the written bytes", func(t *testing.T) {
		// Prepare refuses a no-op, so the written bytes always differ from
		// the original and the guard can never be the original hash.
		_, sum := written(t)
		assert.NotEqual(t, sha256.Sum256([]byte(emitted)), sum)
	})
}
