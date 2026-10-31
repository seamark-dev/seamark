//go:build unix

package reviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// contractPinVersion is the pin version of the L2 and L3 contract
// examples. The delivered key of the L2 state example is its item digest.
const contractPinVersion = "f128e3e12b9d12825e8f7a4396ebb003b517d1e162955edb04bf147a5aa89897"

// exploreTestContext is a resettable root context of Claude Code, with
// the receiver ID that integration.ReceivingContextOf builds.
var exploreTestContext = DeliveryContext{ClientID: "claude", ReceiverID: "3:s-1"}

// exploreTestItems returns one pin and one mined lesson for one file.
func exploreTestItems() []ExploreItem {
	return []ExploreItem{
		{
			Kind:   ExploreItemPin,
			ID:     contractPinVersion,
			Lesson: model.Lesson{Region: "internal/parse", Symptom: "keep-parser-strict — Reject unknown fields in the parser."},
		},
		{
			Kind:   ExploreItemLesson,
			ID:     "internal/parse|RUF001",
			Lesson: model.Lesson{ClusterKey: "internal/parse|RUF001", Region: "internal/parse", Symptom: "RUF001"},
		},
	}
}

// lessonsOf returns the lessons that the items render.
func lessonsOf(items []ExploreItem) []model.Lesson {
	out := make([]model.Lesson, 0, len(items))
	for _, item := range items {
		out = append(out, item.Lesson)
	}

	return out
}

// exploreStatePath is the exploration state file under root.
func exploreStatePath(root string) string {
	return filepath.Join(root, ".seamark", exploreStateFile)
}

// editStatePath is the edit hook state file under root.
func editStatePath(root string) string {
	return filepath.Join(root, ".seamark", deliveryStateFile)
}

// commitExplore begins a lease, commits it as after a successful emit,
// and closes it. It returns the injected items.
func commitExplore(t *testing.T, root string, dc DeliveryContext, items []ExploreItem) []ExploreItem {
	t.Helper()

	lease, err := BeginExploreDelivery(root, dc, items)
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	return lease.Inject()
}

// seedEditState commits an edit hook delivery of lessons, so the edit
// state file holds Delivered entries that exploration must keep.
func seedEditState(t *testing.T, root string, dc DeliveryContext, lessons []model.Lesson) []byte {
	t.Helper()

	lease, err := BeginContextDelivery(root, dc, unversionedItems(lessons))
	require.NoError(t, err)
	require.Equal(t, lessons, lease.Inject())
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	data, err := os.ReadFile(editStatePath(root))
	require.NoError(t, err)

	return data
}

// readExploreFile decodes the exploration state file as the reader does.
func readExploreFile(t *testing.T, root string) exploreState {
	t.Helper()

	state, err := readExploreState(exploreStatePath(root))
	require.NoError(t, err)

	return state
}

// itemDigests returns the item digests of items on the explore surface.
func itemDigests(items []ExploreItem) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[ExploreItemDigest(ExploreSurface, item.Kind, item.ID)] = true
	}

	return out
}

func TestExploreItemDigestVectors(t *testing.T) {
	assert.Equal(t, "explore", ExploreSurface)
	assert.Equal(t, "explore-item/1", exploreItemScheme)
	assert.Equal(t, "14:explore-item/17:explore3:pin2:v1",
		compose(exploreItemScheme, ExploreSurface, ExploreItemPin, "v1"))

	// Each digest was computed outside Go from the §1.2 composition.
	vectors := []struct {
		name              string
		surface, kind, id string
		want              string
	}{
		{"the pin of the L2 state example", "explore", "pin", contractPinVersion,
			"5266b096177b70b2cd45f6112f0d701f9150d60da10e28dec56b6a9ea1d0afaf"},
		{"a mined lesson by its cluster key", "explore", "lesson", "internal/parse|RUF001",
			"858aef72092d9fdf40a67f0eed5424d422a35ad9ab03c8a1e3a4d719ef1a95ec"},
		{"the same pin on another surface", "grep", "pin", contractPinVersion,
			"16b312f13f8ce3e18a6236e998e714aa8902ab10ebd944ef592b88d3e1d99133"},
		{"empty parts keep their length prefix", "", "pin", "",
			"65ceaf0e9c078058e6dac9e8c4bf9b7fbd284b02efef92e9bfc240fa65a3525a"},
	}

	seen := make(map[string]string, len(vectors))

	for _, v := range vectors {
		got := ExploreItemDigest(v.surface, v.kind, v.id)
		assert.Equal(t, v.want, got, v.name)

		other, dup := seen[got]
		assert.False(t, dup, "%s and %s share a digest", v.name, other)
		seen[got] = v.name
	}

	// The kind is a part: a pin and a lesson with one ID stay apart.
	assert.NotEqual(t, ExploreItemDigest(ExploreSurface, ExploreItemPin, "x"),
		ExploreItemDigest(ExploreSurface, ExploreItemLesson, "x"))

	// The exploration digest never aliases a digest of the edit state or
	// of the firing log, so no key of one scheme selects the other.
	lesson := exploreTestItems()[1].Lesson
	item := ExploreItemDigest(ExploreSurface, ExploreItemLesson, lesson.ClusterKey)
	assert.NotEqual(t, lessonDeliveryDigest(lesson), item)
	assert.NotEqual(t, ContextDigest("/repo", "claude", "3:s-1"), item)
	assert.NotEqual(t, SessionDigest("/repo", "s-1"), item)
}

func TestExploreStateRefusesWhatItCannotRead(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	valid := `{"version":1,"contexts":{}}`

	cases := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"another version", writeExploreText(`{"version":2,"contexts":{}}`)},
		{"no version", writeExploreText(`{"contexts":{}}`)},
		{"the sessions key of the edit state", writeExploreText(`{"version":1,"contexts":{},"sessions":{}}`)},
		{"an unknown top-level field", writeExploreText(`{"version":1,"contexts":{},"surface":"explore"}`)},
		{"an unknown field in an entry", writeExploreText(
			`{"version":1,"contexts":{"k":{"generation":1,"updated_at":"` + now + `","delivered":{},"lane":"root"}}}`)},
		{"several JSON values", writeExploreText(valid + "\n" + valid)},
		{"invalid JSON", writeExploreText("not json")},
		{"a file above 1 MiB", writeExploreText(string(bytes.Repeat([]byte(" "), maxExploreStateBytes+1)))},
		{"a symlink to a valid file", func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(filepath.Dir(path)), "target.json")
			require.NoError(t, os.WriteFile(target, []byte(valid), 0o600))
			require.NoError(t, os.Symlink(target, path))
		}},
		{"a directory", func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o700))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".seamark")
			require.NoError(t, os.Mkdir(dir, 0o700))

			path := exploreStatePath(root)
			tc.write(t, path)
			before := exploreFileSnapshot(t, path)

			lease, err := BeginExploreDelivery(root, exploreTestContext, exploreTestItems())
			require.Error(t, err, "the caller then emits every item without suppression")
			assert.Nil(t, lease)

			generation, err := ResetExploreDelivery(root, exploreTestContext)
			require.Error(t, err)
			assert.Zero(t, generation)

			// This binary never overwrites state that it cannot read.
			assert.Equal(t, before, exploreFileSnapshot(t, path))

			leftovers, err := filepath.Glob(filepath.Join(dir, ".lessons-explore-state-*"))
			require.NoError(t, err)
			assert.Empty(t, leftovers, "no temporary state file stays")

			// The lock is free again: a later run with a readable file works.
			require.NoError(t, os.RemoveAll(path))

			lease, err = BeginExploreDelivery(root, exploreTestContext, exploreTestItems())
			require.NoError(t, err)
			require.NoError(t, lease.Close())
		})
	}

	t.Run("a linked state directory", func(t *testing.T) {
		root := t.TempDir()
		elsewhere := filepath.Join(root, "elsewhere")
		require.NoError(t, os.Mkdir(elsewhere, 0o700))
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".seamark")))

		_, err := BeginExploreDelivery(root, exploreTestContext, exploreTestItems())
		require.ErrorContains(t, err, "not a real directory")

		entries, err := os.ReadDir(elsewhere)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("a linked lock", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o700))
		require.NoError(t, os.WriteFile(exploreStatePath(root), []byte(valid), 0o600))

		target := filepath.Join(root, "lock-target")
		require.NoError(t, os.WriteFile(target, nil, 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(root, ".seamark", exploreLockFile)))

		_, err := BeginExploreDelivery(root, exploreTestContext, exploreTestItems())
		require.ErrorContains(t, err, "lock is a symlink")

		_, err = ResetExploreDelivery(root, exploreTestContext)
		require.ErrorContains(t, err, "lock is a symlink")
	})

	t.Run("the contract shape reads", func(t *testing.T) {
		// The L2 example, with a fresh updated_at and this root's key: the
		// delivered digest is the pin of exploreTestItems.
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o700))

		key := ContextDigest(root, exploreTestContext.ClientID, exploreTestContext.ReceiverID)
		example := fmt.Sprintf(`{"version": 1, "contexts": {%q: {"generation": 1, "updated_at": %q, `+
			`"delivered": {"5266b096177b70b2cd45f6112f0d701f9150d60da10e28dec56b6a9ea1d0afaf": true}}}}`, key, now)
		require.NoError(t, os.WriteFile(exploreStatePath(root), []byte(example), 0o600))

		items := exploreTestItems()
		lease, err := BeginExploreDelivery(root, exploreTestContext, items)
		require.NoError(t, err)
		assert.Equal(t, items[1:], lease.Inject())
		assert.Equal(t, items[:1], lease.Suppressed())
		assert.Equal(t, uint64(1), lease.Generation())
		require.NoError(t, lease.Close())
	})

	t.Run("an invalid item", func(t *testing.T) {
		root := t.TempDir()

		for _, item := range []ExploreItem{{Kind: "rule", ID: "x"}, {Kind: ExploreItemPin}, {Kind: ExploreItemLesson}} {
			_, err := BeginExploreDelivery(root, exploreTestContext, []ExploreItem{item})
			require.Error(t, err, "%+v", item)
		}

		_, err := BeginExploreDelivery(root, DeliveryContext{ClientID: "claude"}, exploreTestItems())
		require.Error(t, err, "a partial identity never selects state")
		assert.NoDirExists(t, filepath.Join(root, ".seamark"), "a refused call creates nothing")
	})
}

// writeExploreText returns a writer of fixed state file bytes.
func writeExploreText(text string) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}
}

// exploreFileSnapshot describes what is at path without following a
// link: the link target, a directory marker, or the file bytes.
func exploreFileSnapshot(t *testing.T, path string) string {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		require.NoError(t, err)

		data, err := os.ReadFile(target)
		require.NoError(t, err)

		return "link " + target + " " + string(data)
	case info.IsDir():
		return "directory"
	default:
		data, err := os.ReadFile(path)
		require.NoError(t, err)

		return string(data)
	}
}

func TestExploreLeaseCommitsOnlyAfterEmitAndIsExclusive(t *testing.T) {
	root := t.TempDir()
	items := exploreTestItems()

	first, err := BeginExploreDelivery(root, exploreTestContext, items)
	require.NoError(t, err)
	assert.Equal(t, items, first.Inject())
	assert.Empty(t, first.Suppressed())
	assert.Equal(t, uint64(1), first.Generation())

	// The lease is exclusive and never waits: a second hook process gets
	// an error and emits without suppression. Only a reset waits.
	start := time.Now()
	_, err = BeginExploreDelivery(root, exploreTestContext, items)
	require.Error(t, err, "lock contention must return so the hook can fail open")
	assert.Less(t, time.Since(start), exploreResetLockWait, "a lease never takes the wait of a reset")

	_, err = BeginExploreDelivery(root, DeliveryContext{ClientID: "claude", ReceiverID: "3:s-2"}, items)
	require.Error(t, err, "one lock covers every context of the file")

	// A failed emit, a crash, or a cancel closes without Commit, and
	// nothing is marked delivered.
	require.NoError(t, first.Close())
	require.NoError(t, first.Close(), "Close is safe to repeat")
	assert.NoFileExists(t, exploreStatePath(root))

	assert.Equal(t, items, commitExplore(t, root, exploreTestContext, items), "the uncommitted items come again")

	// The committed file: version first, the context key of the firing
	// log, generation 1, and one digest per item. It is private.
	raw, err := os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(raw, []byte(`{"version":1,"contexts":{`)), "%s", raw)

	state := readExploreFile(t, root)
	key := ContextDigest(root, exploreTestContext.ClientID, exploreTestContext.ReceiverID)
	require.Contains(t, state.Contexts, key)
	assert.Len(t, state.Contexts, 1)
	assert.Equal(t, uint64(1), state.Contexts[key].Generation)
	assert.Equal(t, itemDigests(items), state.Contexts[key].Delivered)

	for _, name := range []string{exploreStateFile, exploreLockFile} {
		info, err := os.Stat(filepath.Join(root, ".seamark", name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}

	// After the commit, the same items in the same generation are
	// suppressed, and a lease without injected items writes nothing.
	again, err := BeginExploreDelivery(root, exploreTestContext, items)
	require.NoError(t, err)
	assert.Empty(t, again.Inject())
	assert.Equal(t, items, again.Suppressed())
	assert.Equal(t, uint64(1), again.Generation())
	require.NoError(t, again.Commit())
	require.NoError(t, again.Close())

	unchanged, err := os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)
	assert.Equal(t, raw, unchanged)

	// A new pin version, another receiver, and another client are new
	// items or a new context, so they deliver again.
	changed := exploreTestItems()
	changed[0].ID = "a-new-pin-version"
	lease, err := BeginExploreDelivery(root, exploreTestContext, changed)
	require.NoError(t, err)
	assert.Equal(t, changed[:1], lease.Inject(), "a change of meaning delivers the pin again")
	assert.Equal(t, changed[1:], lease.Suppressed())
	require.NoError(t, lease.Close())

	for _, dc := range []DeliveryContext{
		{ClientID: "claude", ReceiverID: "3:s-17:agent-1"},
		{ClientID: "codex", ReceiverID: exploreTestContext.ReceiverID},
	} {
		lease, err := BeginExploreDelivery(root, dc, items)
		require.NoError(t, err)
		assert.Equal(t, items, lease.Inject(), "%+v", dc)
		require.NoError(t, lease.Close())
	}

	// One call that selects one item twice delivers it once.
	fresh := DeliveryContext{ClientID: "claude", ReceiverID: "3:s-3"}
	lease, err = BeginExploreDelivery(root, fresh, []ExploreItem{items[0], items[0]})
	require.NoError(t, err)
	assert.Equal(t, items[:1], lease.Inject())
	assert.Equal(t, items[:1], lease.Suppressed())

	// A Commit after Close would write without the lock, so it fails and
	// changes nothing.
	require.NoError(t, lease.Close())
	require.Error(t, lease.Commit())

	unchanged, err = os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)
	assert.Equal(t, raw, unchanged)

	// AC-26: exploration never created the edit hook state or its lock.
	// So an edit of the same pin right after the read still gets it.
	assert.NoFileExists(t, editStatePath(root))
	assert.NoFileExists(t, filepath.Join(root, ".seamark", deliveryLockFile))

	edit, err := BeginContextDelivery(root, exploreTestContext, unversionedItems(lessonsOf(items)))
	require.NoError(t, err)
	assert.Equal(t, lessonsOf(items), edit.Inject(), "the edit reminder still fires after exploration")
	assert.Empty(t, edit.Suppressed())

	// The two leases use two locks: an open edit lease never blocks
	// exploration.
	parallel, err := BeginExploreDelivery(root, fresh, items)
	require.NoError(t, err, "the edit lock is not the exploration lock")
	require.NoError(t, parallel.Close())
	require.NoError(t, edit.Close())

	// AC-26 with a populated edit state: exploration commits and resets on
	// the same context leave the edit state file byte for byte.
	populated := t.TempDir()
	editBefore := seedEditState(t, populated, exploreTestContext, lessonsOf(items))

	commitExplore(t, populated, exploreTestContext, items)

	generation, err := ResetExploreDelivery(populated, exploreTestContext)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), generation)

	commitExplore(t, populated, exploreTestContext, items)

	editAfter, err := os.ReadFile(editStatePath(populated))
	require.NoError(t, err)
	assert.Equal(t, editBefore, editAfter, "exploration never writes deliveryContextState.Delivered")

	// The edit hook still suppresses what it delivered itself, and only that.
	edit, err = BeginContextDelivery(populated, exploreTestContext, unversionedItems(lessonsOf(items)))
	require.NoError(t, err)
	assert.Empty(t, edit.Inject())
	assert.Equal(t, lessonsOf(items), edit.Suppressed())
	require.NoError(t, edit.Close())
}

func TestResetExploreDeliveryCreatesNoFile(t *testing.T) {
	t.Run("no state directory", func(t *testing.T) {
		root := t.TempDir()

		generation, err := ResetExploreDelivery(root, exploreTestContext)
		require.NoError(t, err)
		assert.Zero(t, generation)
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	})

	t.Run("no exploration state", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".seamark")
		require.NoError(t, os.Mkdir(dir, 0o700))

		generation, err := ResetExploreDelivery(root, exploreTestContext)
		require.NoError(t, err)
		assert.Zero(t, generation, "0 means that nothing was reset")

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "a reset creates no state file and no lock")
	})

	t.Run("only edit state", func(t *testing.T) {
		// The reset hook resets both states. The exploration reset never
		// creates its file beside the edit state, and the edit reset
		// never touches the exploration state.
		root := t.TempDir()
		seedEditState(t, root, exploreTestContext, lessonsOf(exploreTestItems()))

		generation, err := ResetExploreDelivery(root, exploreTestContext)
		require.NoError(t, err)
		assert.Zero(t, generation)
		assert.NoFileExists(t, exploreStatePath(root))
		assert.NoFileExists(t, filepath.Join(root, ".seamark", exploreLockFile))

		require.NoError(t, ResetContextDelivery(root, exploreTestContext))
		assert.NoFileExists(t, exploreStatePath(root))
	})

	t.Run("an existing entry", func(t *testing.T) {
		root := t.TempDir()
		items := exploreTestItems()
		editBefore := seedEditState(t, root, exploreTestContext, lessonsOf(items))
		commitExplore(t, root, exploreTestContext, items)

		other := DeliveryContext{ClientID: "claude", ReceiverID: "3:s-2"}
		commitExplore(t, root, other, items)

		generation, err := ResetExploreDelivery(root, exploreTestContext)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), generation, "a reset begins a new exploration generation")

		state := readExploreFile(t, root)
		key := ContextDigest(root, exploreTestContext.ClientID, exploreTestContext.ReceiverID)
		assert.Equal(t, uint64(2), state.Contexts[key].Generation)
		assert.Empty(t, state.Contexts[key].Delivered)

		raw, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"generation":2,`)
		assert.Contains(t, string(raw), `"delivered":{}`, "a cleared set encodes as an object, never null")

		otherKey := ContextDigest(root, other.ClientID, other.ReceiverID)
		assert.Equal(t, uint64(1), state.Contexts[otherKey].Generation, "a reset names one context")
		assert.Equal(t, itemDigests(items), state.Contexts[otherKey].Delivered)

		// A context without an entry gets generation 1. The rule mirrors
		// the edit state reset, so both generations start alike.
		unused := DeliveryContext{ClientID: "claude", ReceiverID: "3:s-9"}
		generation, err = ResetExploreDelivery(root, unused)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), generation)

		lease, err := BeginExploreDelivery(root, exploreTestContext, items)
		require.NoError(t, err)
		assert.Equal(t, items, lease.Inject(), "the items reach the new generation again")
		assert.Equal(t, uint64(2), lease.Generation())
		require.NoError(t, lease.Close())

		// The exploration reset never writes the edit state, and the edit
		// reset never writes the exploration state.
		editAfter, err := os.ReadFile(editStatePath(root))
		require.NoError(t, err)
		assert.Equal(t, editBefore, editAfter)

		exploreBefore, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		require.NoError(t, ResetContextDelivery(root, exploreTestContext))

		exploreAfter, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		assert.Equal(t, exploreBefore, exploreAfter)
	})

	t.Run("a partial identity", func(t *testing.T) {
		root := t.TempDir()
		commitExplore(t, root, exploreTestContext, exploreTestItems())

		before, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)

		for _, dc := range []DeliveryContext{{ClientID: "claude"}, {ReceiverID: "3:s-1"}, {}} {
			generation, err := ResetExploreDelivery(root, dc)
			require.NoError(t, err)
			assert.Zero(t, generation, "%+v", dc)
		}

		after, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("a busy lock", func(t *testing.T) {
		root := t.TempDir()
		items := exploreTestItems()
		commitExplore(t, root, exploreTestContext, items)

		lease, err := BeginExploreDelivery(root, exploreTestContext, items)
		require.NoError(t, err)

		before, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)

		// The lease stays open for the whole wait, so the reset fails. The
		// wait has a bound, so a held lock never stops the reset hook.
		start := time.Now()
		generation, err := ResetExploreDelivery(root, exploreTestContext)
		elapsed := time.Since(start)

		require.Error(t, err, "a lease that outlasts the wait fails the reset")
		assert.Zero(t, generation)
		assert.GreaterOrEqual(t, elapsed, exploreResetLockWait, "the reset waits before it fails")
		assert.Less(t, elapsed, 5*time.Second, "the wait has a bound")
		require.NoError(t, lease.Close())

		after, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("stale entries go on the next write", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o700))

		old := time.Now().UTC().Add(-exploreStateTTL - time.Hour)
		seed := exploreState{Version: exploreStateVersion, Contexts: map[string]exploreContextState{
			"stale": {Generation: 4, UpdatedAt: old, Delivered: map[string]bool{"d": true}},
			ContextDigest(root, exploreTestContext.ClientID, exploreTestContext.ReceiverID): {
				Generation: 3, UpdatedAt: time.Now().UTC(), Delivered: map[string]bool{"d": true},
			},
		}}

		data, err := json.Marshal(seed)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(exploreStatePath(root), data, 0o600))

		generation, err := ResetExploreDelivery(root, exploreTestContext)
		require.NoError(t, err)
		assert.Equal(t, uint64(4), generation)

		state := readExploreFile(t, root)
		assert.NotContains(t, state.Contexts, "stale", "an entry older than 24 hours is pruned")
		assert.Len(t, state.Contexts, 1)
	})
}

// TestResetExploreDeliveryWaitsForALease shows that a reset waits a short
// time for a lease of another context. One lock covers every context of
// the file. A reset that fails keeps the old delivered items, so advice
// stays hidden after a compaction (review finding cross-cutting:F2).
func TestResetExploreDeliveryWaitsForALease(t *testing.T) {
	root := t.TempDir()
	items := exploreTestItems()
	commitExplore(t, root, exploreTestContext, items)

	// The other context holds its lease as a parallel root session does:
	// begin, a short emit, commit, and close.
	other := DeliveryContext{ClientID: "claude", ReceiverID: "3:s-2"}
	held := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		lease, err := BeginExploreDelivery(root, other, items)
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

	generation, resetErr := ResetExploreDelivery(root, exploreTestContext)
	require.NoError(t, <-done, "the other context commits its lease")
	require.NoError(t, resetErr, "a reset waits for a short lease of another context")
	assert.Equal(t, uint64(2), generation)

	state := readExploreFile(t, root)
	key := ContextDigest(root, exploreTestContext.ClientID, exploreTestContext.ReceiverID)
	assert.Equal(t, uint64(2), state.Contexts[key].Generation)
	assert.Empty(t, state.Contexts[key].Delivered)

	// The reset reads the file after the commit of the other context, so
	// it keeps that commit.
	otherKey := ContextDigest(root, other.ClientID, other.ReceiverID)
	assert.Equal(t, uint64(1), state.Contexts[otherKey].Generation)
	assert.Equal(t, itemDigests(items), state.Contexts[otherKey].Delivered)

	lease, err := BeginExploreDelivery(root, exploreTestContext, items)
	require.NoError(t, err)
	assert.Equal(t, items, lease.Inject(), "the items reach the new generation again")
	assert.Equal(t, uint64(2), lease.Generation())
	require.NoError(t, lease.Close())
}
