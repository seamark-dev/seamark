//go:build unix

package reviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		unversionedItems([]model.Lesson{{Region: "api", Symptom: "lesson"}}))
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

// deliverItems runs one lease over items and commits it. It returns what
// the lease injected.
func deliverItems(t *testing.T, root string, dc DeliveryContext, items []DeliveryItem) []model.Lesson {
	t.Helper()

	lease, err := BeginContextDelivery(root, dc, items)
	require.NoError(t, err)

	defer func() { require.NoError(t, lease.Close()) }()

	require.NoError(t, lease.Commit())

	return lease.Inject()
}

func TestContextDeliveryKeysAPinByItsVersion(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session"}
	lesson := model.Lesson{Region: "api", Symptom: "frozen-assets — one text for two versions"}

	first := DeliveryItem{Lesson: lesson, PinVersion: strings.Repeat("a", 64)}
	second := DeliveryItem{Lesson: lesson, PinVersion: strings.Repeat("b", 64)}

	assert.Len(t, deliverItems(t, root, dc, []DeliveryItem{first}), 1)
	assert.Empty(t, deliverItems(t, root, dc, []DeliveryItem{first}), "one version reaches a context once")
	assert.Len(t, deliverItems(t, root, dc, []DeliveryItem{second}), 1, "a new version of one text is new to the context")
	assert.Empty(t, deliverItems(t, root, dc, []DeliveryItem{first}), "the context keeps the earlier version")

	// A failed reply commits nothing, so the new version stays eligible.
	third := DeliveryItem{Lesson: lesson, PinVersion: strings.Repeat("c", 64)}

	lease, err := BeginContextDelivery(root, dc, []DeliveryItem{third})
	require.NoError(t, err)
	assert.Len(t, lease.Inject(), 1)
	require.NoError(t, lease.Close())
	assert.Len(t, deliverItems(t, root, dc, []DeliveryItem{third}), 1)

	require.NoError(t, ResetContextDelivery(root, dc))
	assert.Len(t, deliverItems(t, root, dc, []DeliveryItem{first}), 1, "a reset clears every version")

	other := DeliveryContext{ClientID: "claude", ReceiverID: "child"}
	assert.Len(t, deliverItems(t, root, other, []DeliveryItem{first}), 1, "a version in one context hides nothing in another")
}

// TestContextDeliveryKeepsOneItemPerEvent proves the dedupe within one
// lease. A repeated pin version and a repeated mined lesson each go out once.
func TestContextDeliveryKeepsOneItemPerEvent(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session"}
	pin := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "pin"}, PinVersion: strings.Repeat("a", 64)}
	mined := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "RUF001"}}

	lease, err := BeginContextDelivery(root, dc, []DeliveryItem{pin, pin, mined, mined})
	require.NoError(t, err)

	defer func() { require.NoError(t, lease.Close()) }()

	assert.Equal(t, []model.Lesson{pin.Lesson, mined.Lesson}, lease.Inject())
	assert.Equal(t, []model.Lesson{pin.Lesson, mined.Lesson}, lease.Suppressed())
}

// TestContextDeliveryNamesTheInjectedPinVersions proves the list that names
// the pins of an event. One lesson text cannot do that, because two
// versions of a pin can render it. A mined lesson has no entry in the list.
func TestContextDeliveryNamesTheInjectedPinVersions(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session"}
	lesson := model.Lesson{Region: "api", Symptom: "one text"}
	delivered := DeliveryItem{Lesson: lesson, PinVersion: strings.Repeat("a", 64)}
	fresh := DeliveryItem{Lesson: lesson, PinVersion: strings.Repeat("b", 64)}
	mined := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "RUF001"}}

	require.Len(t, deliverItems(t, root, dc, []DeliveryItem{delivered}), 1)

	lease, err := BeginContextDelivery(root, dc, []DeliveryItem{delivered, fresh, fresh, mined})
	require.NoError(t, err)

	defer func() { require.NoError(t, lease.Close()) }()

	assert.Equal(t, []string{fresh.PinVersion}, lease.InjectedPinVersions())
	assert.Equal(t, []model.Lesson{lesson, mined.Lesson}, lease.Inject())
	assert.Equal(t, []model.Lesson{lesson, lesson}, lease.Suppressed())
}

// TestContextDeliveryFailsSafeOverEarlierContentKeys holds the upgrade
// rule. A file from a binary with content keys for pins stays readable. A
// pin in it is delivered once more and never hidden. A mined lesson keeps
// its entry. The rewritten file keeps the shape that an older binary reads.
func TestContextDeliveryFailsSafeOverEarlierContentKeys(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session"}
	pin := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "pin text"}, PinVersion: strings.Repeat("a", 64)}
	mined := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "RUF001"}}

	// The state of an earlier binary: both lessons are delivered under
	// their content digests, at generation 4.
	earlier := fmt.Sprintf(
		`{"version":2,"contexts":{%q:{"generation":4,"updated_at":%q,"delivered":{%q:true,%q:true}}}}`,
		contextDigest(root, dc), time.Now().UTC().Format(time.RFC3339),
		lessonDeliveryDigest(pin.Lesson), lessonDeliveryDigest(mined.Lesson))
	path := filepath.Join(dir, deliveryStateFile)
	require.NoError(t, os.WriteFile(path, []byte(earlier+"\n"), 0o600))

	lease, err := BeginContextDelivery(root, dc, []DeliveryItem{pin, mined})
	require.NoError(t, err)
	assert.Equal(t, []model.Lesson{pin.Lesson}, lease.Inject(), "at worst a pin is delivered again")
	assert.Equal(t, []model.Lesson{mined.Lesson}, lease.Suppressed(), "a mined lesson keeps its entry")
	assert.Equal(t, uint64(4), lease.Generation(), "the context keeps its generation")
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version":2`)

	// The file has no new field, so the strict reader of an older binary
	// accepts it. Its digest finds no entry for the new key, and the older
	// binary delivers again instead of hiding the pin.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var older struct {
		Version  int `json:"version"`
		Contexts map[string]struct {
			Generation uint64          `json:"generation"`
			UpdatedAt  time.Time       `json:"updated_at"`
			Delivered  map[string]bool `json:"delivered"`
		} `json:"contexts"`
	}

	require.NoError(t, decoder.Decode(&older))

	delivered := older.Contexts[contextDigest(root, dc)].Delivered
	assert.True(t, delivered[pin.digest()])
	assert.NotEqual(t, lessonDeliveryDigest(pin.Lesson), pin.digest())

	assert.Empty(t, deliverItems(t, root, dc, []DeliveryItem{pin, mined}), "suppression works under the new key")
}

func TestContextDeliveryOpenReadsTheDeliveredSet(t *testing.T) {
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session"}
	pin := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "pin text"}, PinVersion: strings.Repeat("a", 64)}
	other := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "other text"}, PinVersion: strings.Repeat("b", 64)}
	mined := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "RUF001"}}

	t.Run("an open writes no state and selects nothing", func(t *testing.T) {
		root := t.TempDir()

		lease, err := OpenContextDelivery(root, dc)
		require.NoError(t, err)
		assert.False(t, lease.PinDelivered(pin.PinVersion))
		assert.Equal(t, uint64(1), lease.Generation())
		assert.Empty(t, lease.Inject())
		assert.Empty(t, lease.Suppressed())
		require.NoError(t, lease.Commit(), "a commit with no selection has nothing to mark")
		require.NoError(t, lease.Close())

		assert.NoFileExists(t, filepath.Join(root, ".seamark", deliveryStateFile))
	})

	t.Run("the set names the exact version of this context", func(t *testing.T) {
		root := t.TempDir()
		deliverItems(t, root, dc, []DeliveryItem{pin, mined})

		lease, err := OpenContextDelivery(root, dc)
		require.NoError(t, err)
		assert.True(t, lease.PinDelivered(pin.PinVersion))
		assert.False(t, lease.PinDelivered(other.PinVersion), "a new version stays eligible")
		assert.False(t, lease.PinDelivered(mined.digest()), "a mined key is not a pin version")
		require.NoError(t, lease.Close())

		lease, err = OpenContextDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "other"})
		require.NoError(t, err)
		assert.False(t, lease.PinDelivered(pin.PinVersion), "another receiver has its own set")
		require.NoError(t, lease.Close())

		require.NoError(t, ResetContextDelivery(root, dc))

		lease, err = OpenContextDelivery(root, dc)
		require.NoError(t, err)
		assert.False(t, lease.PinDelivered(pin.PinVersion), "a reset empties the set")
		require.NoError(t, lease.Close())
	})

	t.Run("a content key of an earlier binary matches nothing", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".seamark")
		require.NoError(t, os.MkdirAll(dir, 0o755))

		// The state holds the content key of the pin text, and also the
		// bare version, which no scheme ever wrote.
		earlier := fmt.Sprintf(
			`{"version":2,"contexts":{%q:{"generation":4,"updated_at":%q,"delivered":{%q:true,%q:true}}}}`,
			contextDigest(root, dc), time.Now().UTC().Format(time.RFC3339),
			lessonDeliveryDigest(pin.Lesson), pin.PinVersion)
		require.NoError(t, os.WriteFile(filepath.Join(dir, deliveryStateFile), []byte(earlier+"\n"), 0o600))

		lease, err := OpenContextDelivery(root, dc)
		require.NoError(t, err)
		assert.False(t, lease.PinDelivered(pin.PinVersion), "at worst a pin repeats once")
		assert.Equal(t, uint64(4), lease.Generation(), "the context keeps its generation")
		require.NoError(t, lease.Close())
	})

	t.Run("select after an open equals one begin", func(t *testing.T) {
		root := t.TempDir()
		deliverItems(t, root, dc, []DeliveryItem{pin})

		items := []DeliveryItem{pin, other, other, mined}

		lease, err := OpenContextDelivery(root, dc)
		require.NoError(t, err)

		lease.Select(items)
		assert.Equal(t, []model.Lesson{other.Lesson, mined.Lesson}, lease.Inject())
		assert.Equal(t, []model.Lesson{pin.Lesson, other.Lesson}, lease.Suppressed())
		assert.Equal(t, []string{other.PinVersion}, lease.InjectedPinVersions())
		require.NoError(t, lease.Close(), "closing without commit marks nothing")

		begun, err := BeginContextDelivery(root, dc, items)
		require.NoError(t, err)
		assert.Equal(t, []model.Lesson{other.Lesson, mined.Lesson}, begun.Inject())
		assert.Equal(t, []model.Lesson{pin.Lesson, other.Lesson}, begun.Suppressed())
		assert.Equal(t, []string{other.PinVersion}, begun.InjectedPinVersions())
		require.NoError(t, begun.Close())
	})

	t.Run("an open keeps one lock attempt", func(t *testing.T) {
		root := t.TempDir()

		lease, err := OpenContextDelivery(root, dc)
		require.NoError(t, err)

		defer func() { require.NoError(t, lease.Close()) }()

		start := time.Now()
		_, err = OpenContextDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "other"})
		require.Error(t, err, "lock contention must return so the hook can fail open")
		assert.Less(t, time.Since(start), deliveryResetLockWait, "a lease never takes the wait of a reset")
	})

	t.Run("a partial identity opens nothing", func(t *testing.T) {
		root := t.TempDir()

		_, err := OpenContextDelivery(root, DeliveryContext{ClientID: "claude"})
		require.Error(t, err)
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	})
}

// TestContextDeliveryResetWaitsForALease shows that a reset waits a short
// time for a lease of another context. One lock covers every context of
// the file. A reset that fails keeps the delivered pins hidden after a
// compaction until the TTL ends.
func TestContextDeliveryResetWaitsForALease(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session-one"}
	other := DeliveryContext{ClientID: "claude", ReceiverID: "session-two"}
	pin := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "pin text"}, PinVersion: strings.Repeat("a", 64)}

	deliverItems(t, root, dc, []DeliveryItem{pin})

	// The other context holds its lease as a parallel root session does:
	// begin, a short emit, commit, and close.
	held := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		lease, err := BeginContextDelivery(root, other, []DeliveryItem{pin})
		close(held)

		if err != nil {
			done <- err

			return
		}

		time.Sleep(20 * time.Millisecond)

		commitErr := lease.Commit()
		done <- errors.Join(commitErr, lease.Close())
	}()

	<-held

	generation, resetErr := ResetContextGeneration(root, dc)
	require.NoError(t, <-done, "the other context commits its lease")
	require.NoError(t, resetErr, "a reset waits for a short lease of another context")
	assert.Equal(t, uint64(2), generation)

	lease, err := OpenContextDelivery(root, dc)
	require.NoError(t, err)
	assert.False(t, lease.PinDelivered(pin.PinVersion), "the reset restores the pin")
	assert.Equal(t, uint64(2), lease.Generation())
	require.NoError(t, lease.Close())

	// The reset reads the file after the commit of the other context, so
	// it keeps that commit.
	lease, err = OpenContextDelivery(root, other)
	require.NoError(t, err)
	assert.True(t, lease.PinDelivered(pin.PinVersion))
	require.NoError(t, lease.Close())
}

// TestContextDeliveryResetFailsAfterTheWait shows that the wait has a
// bound, so a held lock never stops the reset hook. The state file keeps
// its bytes, because the reset fails before it reads the file.
func TestContextDeliveryResetFailsAfterTheWait(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session-one"}
	pin := DeliveryItem{Lesson: model.Lesson{Region: "api", Symptom: "pin text"}, PinVersion: strings.Repeat("a", 64)}

	deliverItems(t, root, dc, []DeliveryItem{pin})

	path := filepath.Join(root, ".seamark", deliveryStateFile)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	lease, err := OpenContextDelivery(root, dc)
	require.NoError(t, err)

	start := time.Now()
	generation, err := ResetContextGeneration(root, dc)
	elapsed := time.Since(start)

	require.Error(t, err, "a lease that outlasts the wait fails the reset")
	assert.Zero(t, generation)
	assert.GreaterOrEqual(t, elapsed, deliveryResetLockWait, "the reset waits before it fails")
	assert.Less(t, elapsed, 5*time.Second, "the wait has a bound")
	require.NoError(t, lease.Close())

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestContextDeliverySeparatesClientsAndReceivers(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "lesson"}}

	deliver := func(receiver DeliveryContext) int {
		t.Helper()

		lease, err := BeginContextDelivery(root, receiver, unversionedItems(lessons))
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
		_, err := BeginContextDelivery(root, dc, unversionedItems(lessons))
		require.Error(t, err, "%+v", dc)
		require.NoError(t, ResetContextDelivery(root, dc), "a partial identity resets nothing")
	}

	assert.NoDirExists(t, filepath.Join(root, ".seamark"))

	// The rule matters most when state exists: a reset with a partial
	// identity must not rewrite the file or add an entry to it.
	lease, err := BeginContextDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "session"}, unversionedItems(lessons))
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

func TestResetContextGenerationReportsTheNewGeneration(t *testing.T) {
	root := t.TempDir()
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "session-one"}
	lessons := []model.Lesson{{Region: "api", Symptom: "sync the generated client"}}

	// Without state, nothing resets and nothing is created.
	generation, err := ResetContextGeneration(root, dc)
	require.NoError(t, err)
	assert.Zero(t, generation)
	assert.NoDirExists(t, filepath.Join(root, ".seamark"))

	lease, err := BeginContextDelivery(root, dc, unversionedItems(lessons))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), lease.Generation())
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	// Each reset reports the generation that the next lease reads.
	for _, want := range []uint64{2, 3} {
		generation, err = ResetContextGeneration(root, dc)
		require.NoError(t, err)
		assert.Equal(t, want, generation)

		lease, err = BeginContextDelivery(root, dc, unversionedItems(lessons))
		require.NoError(t, err)
		assert.Equal(t, want, lease.Generation())
		assert.Equal(t, lessons, lease.Inject(), "the reset restores the delivered lesson")
		require.NoError(t, lease.Close())
	}

	// A partial identity resets nothing and reports no generation.
	for _, partial := range []DeliveryContext{{ClientID: "claude"}, {ReceiverID: "session-one"}} {
		generation, err = ResetContextGeneration(root, partial)
		require.NoError(t, err)
		assert.Zero(t, generation)
	}

	// The wrapper keeps its API and advances the same counter.
	require.NoError(t, ResetContextDelivery(root, dc))

	generation, err = ResetContextGeneration(root, dc)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), generation)

	// A state that cannot be read reports its error and no generation.
	path := filepath.Join(root, ".seamark", deliveryStateFile)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	generation, err = ResetContextGeneration(root, dc)
	require.Error(t, err)
	assert.Zero(t, generation)
}
