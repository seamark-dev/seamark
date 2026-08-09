//go:build unix

package reviews

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestHookDeliveryStateSuppressesOnlyAfterCommitAndResets(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{
		{Region: "api", Symptom: "sync the generated client"},
		{Region: "db", Symptom: "bump the cache namespace"},
	}

	lease, err := BeginHookDelivery(root, "session-one", lessons)
	require.NoError(t, err)
	assert.Equal(t, lessons, lease.Inject())
	assert.Empty(t, lease.Suppressed())
	require.NoError(t, lease.Close(), "closing without commit must not mark delivery")

	lease, err = BeginHookDelivery(root, "session-one", lessons)
	require.NoError(t, err)
	assert.Equal(t, lessons, lease.Inject())
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	lease, err = BeginHookDelivery(root, "session-one", lessons)
	require.NoError(t, err)
	assert.Empty(t, lease.Inject())
	assert.Equal(t, lessons, lease.Suppressed())
	require.NoError(t, lease.Close())

	require.NoError(t, ResetHookDelivery(root, "session-one"))
	lease, err = BeginHookDelivery(root, "session-one", lessons)
	require.NoError(t, err)
	assert.Equal(t, lessons, lease.Inject(), "compaction begins a new context generation")
	require.NoError(t, lease.Close())
}

func TestResetHookDeliveryDoesNotCreateUnusedState(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, ResetHookDelivery(root, "session"))
	assert.NoFileExists(t, filepath.Join(root, ".seamark", deliveryStateFile))
}

func TestHookDeliveryStateIsSessionAndContentScoped(t *testing.T) {
	root := t.TempDir()
	original := model.Lesson{Region: "api", Symptom: "sync the generated client"}

	lease, err := BeginHookDelivery(root, "session-one", []model.Lesson{original})
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	lease, err = BeginHookDelivery(root, "session-two", []model.Lesson{original})
	require.NoError(t, err)
	assert.Len(t, lease.Inject(), 1, "another session must receive the lesson")
	require.NoError(t, lease.Close())

	changed := original
	changed.Symptom = "sync the generated client after every schema change"
	lease, err = BeginHookDelivery(root, "session-one", []model.Lesson{changed})
	require.NoError(t, err)
	assert.Len(t, lease.Inject(), 1, "meaningfully changed content has a new identity")
	require.NoError(t, lease.Close())
}

func TestHookDeliveryStateRejectsCorruptOrLinkedState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.Mkdir(dir, 0o755))
	statePath := filepath.Join(dir, deliveryStateFile)
	require.NoError(t, os.WriteFile(statePath, []byte("not json"), 0o600))

	_, err := BeginHookDelivery(root, "session", []model.Lesson{{Region: "api", Symptom: "lesson"}})
	require.ErrorContains(t, err, "decode delivery state")

	require.NoError(t, os.Remove(statePath))
	target := filepath.Join(root, "target")
	require.NoError(t, os.WriteFile(target, []byte(`{"version":1,"sessions":{}}`), 0o600))
	require.NoError(t, os.Symlink(target, statePath))

	_, err = BeginHookDelivery(root, "session", []model.Lesson{{Region: "api", Symptom: "lesson"}})
	require.ErrorContains(t, err, "not a regular file")
}

func TestHookDeliveryStateRejectsOversizedState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, deliveryStateFile),
		make([]byte, maxDeliveryStateBytes+1), 0o600))

	_, err := BeginHookDelivery(root, "session", []model.Lesson{{Region: "api", Symptom: "lesson"}})
	require.ErrorContains(t, err, "exceeds")
}

func TestHookDeliveryStateFileIsPrivate(t *testing.T) {
	root := t.TempDir()
	lease, err := BeginHookDelivery(root, "session", []model.Lesson{{Region: "api", Symptom: "lesson"}})
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	info, err := os.Stat(filepath.Join(root, ".seamark", deliveryStateFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestHookDeliveryStateDoesNotWaitOnConcurrentLease(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "lesson"}}
	first, err := BeginHookDelivery(root, "session-one", lessons)
	require.NoError(t, err)
	defer func() { _ = first.Close() }()

	_, err = BeginHookDelivery(root, "session-two", lessons)
	require.Error(t, err, "lock contention must return so the hook can fail open")
}
