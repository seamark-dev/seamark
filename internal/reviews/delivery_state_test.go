//go:build unix

package reviews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestHookDeliveryStateFailsOpenOnAUnknownVersion freezes what this
// binary does when a newer writer has moved the state file to another
// version: the lease is refused, so the caller falls back to repeated
// delivery, and the newer file is left untouched.
func TestHookDeliveryStateFailsOpenOnAUnknownVersion(t *testing.T) {
	for _, future := range []string{
		`{"version":3,"contexts":{}}` + "\n",
		`{"version":0,"contexts":{}}` + "\n",
		`{"version":3,"receivers":{}}` + "\n",
	} {
		root := t.TempDir()
		dir := filepath.Join(root, ".seamark")
		require.NoError(t, os.MkdirAll(dir, 0o755))

		target := filepath.Join(dir, deliveryStateFile)
		require.NoError(t, os.WriteFile(target, []byte(future), 0o600))

		lessons := []model.Lesson{{ClusterKey: "k", Region: "a", Symptom: "s", Occurrences: 1}}

		_, err := BeginHookDelivery(root, "session", lessons)
		require.Error(t, err, "an unsupported version refuses the lease so advice repeats instead of hiding")

		after, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, future, string(after), "an older writer must not overwrite a newer state file")

		// A reset is equally hands-off: no state is rewritten for a version
		// this binary does not understand.
		require.Error(t, ResetHookDelivery(root, "session"))
		after, err = os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, future, string(after))
	}
}

// versionOneState is a frozen copy of the state shape and the strict
// reader of the binaries before version 2. The tests below use it to
// show what such a binary does with a file that this binary wrote.
type versionOneState struct {
	Version  int `json:"version"`
	Sessions map[string]struct {
		Generation uint64          `json:"generation"`
		UpdatedAt  time.Time       `json:"updated_at"`
		Delivered  map[string]bool `json:"delivered"`
	} `json:"sessions"`
}

func readAsVersionOne(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var state versionOneState
	if err := decoder.Decode(&state); err != nil {
		return err
	}

	if state.Version != 1 {
		return fmt.Errorf("unsupported delivery state version %d", state.Version)
	}

	return nil
}

func TestHookDeliveryStateVersionTwoStopsAnOlderBinary(t *testing.T) {
	// An older binary reads before it writes. Its read of a version 2
	// file must fail, because a successful read lets it write version 1
	// over the entries of this binary. The failed read means repeated
	// delivery for that binary, which is the safe result.
	root := t.TempDir()

	lease, err := BeginContextDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "session"},
		[]model.Lesson{{Region: "api", Symptom: "lesson"}})
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	path := filepath.Join(root, ".seamark", deliveryStateFile)
	require.Error(t, readAsVersionOne(path))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version":2`)
	assert.NotContains(t, string(raw), `"sessions"`, "no writer of this version emits the version 1 key")
	assert.NotContains(t, string(raw), "session\"", "the raw receiver never reaches the state file")

	// Even an empty version 2 file stops the older reader.
	require.NoError(t, os.WriteFile(path, []byte(`{"version":2,"contexts":{}}`+"\n"), 0o600))
	require.Error(t, readAsVersionOne(path))
}

func TestHookDeliveryStateDiscardsVersionOneEntries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	lesson := model.Lesson{Region: "api", Symptom: "lesson"}

	// A version 1 file that marks the lesson delivered for the session.
	legacy := fmt.Sprintf(`{"version":1,"sessions":{%q:{"generation":4,"updated_at":%q,"delivered":{%q:true}}}}`,
		sessionDigest(root, "session"), time.Now().UTC().Format(time.RFC3339), lessonDeliveryDigest(lesson))
	path := filepath.Join(dir, deliveryStateFile)
	require.NoError(t, os.WriteFile(path, []byte(legacy+"\n"), 0o600))
	require.NoError(t, readAsVersionOne(path), "the fixture is a valid version 1 file")

	// The session-keyed entry says nothing about a receiving context, so
	// the lesson is delivered again. An extra reminder is the safe error.
	lease, err := BeginHookDelivery(root, "session", []model.Lesson{lesson})
	require.NoError(t, err)
	assert.Len(t, lease.Inject(), 1)
	assert.Equal(t, uint64(1), lease.Generation(), "the generation restarts with the new key")

	// Nothing is written until a commit, so a read alone keeps the file.
	require.NoError(t, lease.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, legacy+"\n", string(raw))

	lease, err = BeginHookDelivery(root, "session", []model.Lesson{lesson})
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version":2`)
	assert.NotContains(t, string(raw), sessionDigest(root, "session"), "the version 1 entry is gone")

	lease, err = BeginHookDelivery(root, "session", []model.Lesson{lesson})
	require.NoError(t, err)
	assert.Empty(t, lease.Inject(), "suppression works again under the new key")
	require.NoError(t, lease.Close())

	// A reset upgrades a version 1 file as well.
	require.NoError(t, os.WriteFile(path, []byte(legacy+"\n"), 0o600))
	require.NoError(t, ResetHookDelivery(root, "session"))

	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version":2`)
}

func TestContextDeliverySeparatesClientsAndReceivers(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "lesson"}}

	deliver := func(receiver DeliveryContext) int {
		t.Helper()

		lease, err := BeginContextDelivery(root, receiver, lessons)
		require.NoError(t, err)
		require.NoError(t, lease.Commit())
		require.NoError(t, lease.Close())

		return len(lease.Inject())
	}

	parent := DeliveryContext{ClientID: "claude", ReceiverID: "session-1"}

	assert.Equal(t, 1, deliver(parent))
	assert.Zero(t, deliver(parent), "the same receiver gets the lesson once")

	assert.Equal(t, 1, deliver(DeliveryContext{ClientID: "codex", ReceiverID: "session-1"}),
		"another client with the same session string is another receiver")
	assert.Equal(t, 1, deliver(DeliveryContext{ClientID: "claude", ReceiverID: "session-1/agent-7"}),
		"a subagent with its own identity is another receiver")

	// The legacy entry point is the Claude receiver of the same session.
	lease, err := BeginHookDelivery(root, "session-1", lessons)
	require.NoError(t, err)
	assert.Empty(t, lease.Inject())
	require.NoError(t, lease.Close())

	// A reset reaches exactly one receiver.
	require.NoError(t, ResetContextDelivery(root, parent))
	assert.Equal(t, 1, deliver(parent))
	assert.Zero(t, deliver(DeliveryContext{ClientID: "codex", ReceiverID: "session-1"}))
	assert.Zero(t, deliver(DeliveryContext{ClientID: "claude", ReceiverID: "session-1/agent-7"}))

	// A NUL byte inside a part must not move the border between the
	// parts: these are two different identities.
	assert.NotEqual(t,
		contextDigest(root, DeliveryContext{ClientID: "a", ReceiverID: "b\x00c"}),
		contextDigest(root, DeliveryContext{ClientID: "a\x00b", ReceiverID: "c"}))
}

func TestContextDeliveryRefusesAPartialIdentity(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "lesson"}}

	partial := []DeliveryContext{{}, {ClientID: "claude"}, {ReceiverID: "session"}}

	for _, dc := range partial {
		_, err := BeginContextDelivery(root, dc, lessons)
		require.Error(t, err, "%+v", dc)
		require.NoError(t, ResetContextDelivery(root, dc), "a partial identity resets nothing")
	}

	assert.NoDirExists(t, filepath.Join(root, ".seamark"))

	// The rule matters most when state exists: a reset with a partial
	// identity must not rewrite the file or add an entry to it.
	lease, err := BeginContextDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "session"}, lessons)
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	path := filepath.Join(root, ".seamark", deliveryStateFile)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, dc := range partial {
		require.NoError(t, ResetContextDelivery(root, dc))
	}

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}
