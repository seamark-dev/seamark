package reviews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

// This file holds the dedupe state of exploration advice (contract L2).
// It has its own file and its own lock, apart from lessons-hook-state.json.
// Exploration code never reads or writes the edit hook state: an edit of
// a pin right after a read must still get its reminder (AC-26). Only the
// lock helpers of the edit state are shared, because they hold no state.

const (
	// exploreStateVersion is the one version that this binary reads.
	// The reader refuses every other version, so a newer file is never
	// overwritten and advice repeats instead.
	exploreStateVersion  = 1
	exploreStateFile     = "lessons-explore-state.json"
	exploreLockFile      = "lessons-explore-state.lock"
	maxExploreStateBytes = 1 << 20
	// exploreStateTTL equals deliveryStateTTL in value. It is its own
	// constant, because the contract freezes it apart from the edit state.
	exploreStateTTL = 24 * time.Hour
	// exploreResetLockWait bounds the wait of a reset for the exploration
	// lock. One lock covers every context, so a lease of another root
	// session can hold it. A lease holds it for about 5 ms. A failed reset
	// keeps the delivered items, so advice stays hidden after a compaction.
	// A failed lease only repeats advice, so a lease never waits.
	exploreResetLockWait = 200 * time.Millisecond
)

// ExploreSurface is the delivery surface of exploration advice. It is
// the surface part of every exploration item digest. Another structured
// read adapter adds its own surface name, so its items stay apart.
const ExploreSurface = "explore"

// exploreItemScheme labels the item digest. A change of the parts or of
// their order is a new label.
const exploreItemScheme = "explore-item/1"

// The closed kinds of an ExploreItem.
const (
	// ExploreItemPin is a pin. Its ID is the pin version (K2), so a
	// change of meaning delivers the pin again.
	ExploreItemPin = "pin"
	// ExploreItemLesson is a mined lesson. Its ID is the ClusterKey,
	// because a mined lesson has no version.
	ExploreItemLesson = "lesson"
)

// ExploreItemDigest is the hex SHA-256 of compose("explore-item/1",
// surface, kind, id). The state stores it for every delivered item. The
// edit hook state uses another digest of a lesson, so an entry of one
// file never matches an item of the other.
func ExploreItemDigest(surface, kind, id string) string {
	return composeDigest(exploreItemScheme, surface, kind, id)
}

// ExploreItem is one selected item of exploration advice.
type ExploreItem struct {
	// Kind is ExploreItemPin or ExploreItemLesson.
	Kind string
	// ID is the pin version or the ClusterKey, by Kind.
	ID string
	// Lesson is what the reminder renders. It is not part of the digest.
	Lesson model.Lesson
}

type exploreState struct {
	Version  int                            `json:"version"`
	Contexts map[string]exploreContextState `json:"contexts"`
}

// exploreContextState is the exploration entry of one receiving context.
// Generation is the verified context generation of exploration. It is
// independent of the generation in the edit hook state, and nothing
// compares the two.
type exploreContextState struct {
	Generation uint64          `json:"generation"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Delivered  map[string]bool `json:"delivered"`
}

// ExploreLease is a locked selection of exploration items not yet
// delivered to one receiving context. It works as HookDeliveryLease
// does: Commit only after a successful emit, and Close always.
type ExploreLease struct {
	dir        string
	lock       *os.File
	state      exploreState
	contextSHA string
	context    exploreContextState
	inject     []ExploreItem
	suppressed []ExploreItem
	injectSHA  []string
	committed  bool
}

// BeginExploreDelivery splits items into the items to inject and the
// items already delivered to the context in its current generation. It
// keeps the exploration lock until Close. A caller fails open on every
// error: it emits every item and suppresses nothing.
//
// The caller decides whether the context may suppress at all. Only a
// resettable root context may, because only its reset is verified.
func BeginExploreDelivery(root string, dc DeliveryContext, items []ExploreItem) (*ExploreLease, error) {
	if !dc.valid() {
		return nil, fmt.Errorf("explore delivery context names no client or no receiver")
	}

	for i, item := range items {
		if err := item.validate(); err != nil {
			return nil, fmt.Errorf("explore item %d: %w", i+1, err)
		}
	}

	dir, lock, err := openExploreState(root, 0)
	if err != nil {
		return nil, err
	}

	state, err := readExploreState(filepath.Join(dir, exploreStateFile))
	if err != nil {
		_ = unlockDeliveryFile(lock)
		_ = lock.Close()

		return nil, err
	}

	pruneExploreState(&state, time.Now().UTC())

	contextSHA := contextDigest(root, dc)
	current := state.Contexts[contextSHA]

	if current.Generation == 0 {
		current.Generation = 1
	}

	if current.Delivered == nil {
		current.Delivered = make(map[string]bool)
	}

	lease := &ExploreLease{dir: dir, lock: lock, state: state, contextSHA: contextSHA, context: current}
	pending := make(map[string]bool, len(items))

	for _, item := range items {
		digest := ExploreItemDigest(ExploreSurface, item.Kind, item.ID)

		if current.Delivered[digest] || pending[digest] {
			lease.suppressed = append(lease.suppressed, item)
			continue
		}

		pending[digest] = true
		lease.inject = append(lease.inject, item)
		lease.injectSHA = append(lease.injectSHA, digest)
	}

	return lease, nil
}

// validate refuses an item that the digest cannot keep apart from other
// items. An empty ID would make every such item one entry, and a later
// item would then be hidden without its own delivery.
func (item ExploreItem) validate() error {
	if item.Kind != ExploreItemPin && item.Kind != ExploreItemLesson {
		return fmt.Errorf("kind %q is not %q or %q", item.Kind, ExploreItemPin, ExploreItemLesson)
	}

	if item.ID == "" {
		return fmt.Errorf("the %s item has no id", item.Kind)
	}

	return nil
}

// Inject returns the items that still need to reach the context.
func (l *ExploreLease) Inject() []ExploreItem {
	return l.inject
}

// Suppressed returns the items already delivered to the context in its
// current generation.
func (l *ExploreLease) Suppressed() []ExploreItem {
	return l.suppressed
}

// Generation returns the verified exploration generation of the context.
// The audit records it as explore_generation.
func (l *ExploreLease) Generation() uint64 {
	return l.context.Generation
}

// Commit marks the injected items delivered. The caller calls it only
// after the reply reached the client, so a failed write never hides
// advice. A lease without an injected item writes nothing.
func (l *ExploreLease) Commit() error {
	if l.committed || len(l.injectSHA) == 0 {
		l.committed = true

		return nil
	}

	// Without the lock, another hook process can write the file at the
	// same time, and one of the two updates is lost.
	if l.lock == nil {
		return fmt.Errorf("explore delivery lease is closed")
	}

	for _, digest := range l.injectSHA {
		l.context.Delivered[digest] = true
	}

	l.context.UpdatedAt = time.Now().UTC()
	l.state.Contexts[l.contextSHA] = l.context

	if err := writeExploreState(filepath.Join(l.dir, exploreStateFile), l.state); err != nil {
		return err
	}

	l.committed = true

	return nil
}

// Close releases the exploration lock. It is safe to call after Commit
// and more than once.
func (l *ExploreLease) Close() error {
	if l.lock == nil {
		return nil
	}

	err := unlockDeliveryFile(l.lock)
	closeErr := l.lock.Close()
	l.lock = nil

	if err != nil {
		return err
	}

	return closeErr
}

// ResetExploreDelivery advances the exploration generation of a context
// and clears its delivered items. The context-reset hook calls it beside
// ResetContextDelivery, and each reset is independent of the other. It
// returns the new generation, or 0 when nothing was reset.
//
// It never creates the state file or the lock: a file exists only after
// exploration advice reached a context. A partial identity resets
// nothing, because it can name an unrelated context. It waits up to
// exploreResetLockWait for the lock, because a lease of another context
// can hold it.
func ResetExploreDelivery(root string, dc DeliveryContext) (uint64, error) {
	if !dc.valid() {
		return 0, nil
	}

	if _, err := os.Lstat(filepath.Join(root, ".seamark", exploreStateFile)); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	dir, lock, err := openExploreState(root, exploreResetLockWait)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unlockDeliveryFile(lock); _ = lock.Close() }()

	path := filepath.Join(dir, exploreStateFile)

	state, err := readExploreState(path)
	if err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	pruneExploreState(&state, now)

	key := contextDigest(root, dc)
	current := state.Contexts[key]
	current.Generation++

	if current.Generation == 0 {
		current.Generation = 1
	}

	current.UpdatedAt = now
	current.Delivered = make(map[string]bool)
	state.Contexts[key] = current

	if err := writeExploreState(path, state); err != nil {
		return 0, err
	}

	return current.Generation, nil
}

// openExploreState opens and locks the exploration lock file. The checks
// equal those of the edit state: .seamark must be a real directory, and
// the lock must not be a symlink. When another process holds the lock,
// the open tries again until wait ends, and then it returns an error. A
// lease passes no wait: its caller fails open, and that only repeats
// advice. A reset passes exploreResetLockWait, because a failed reset
// hides advice after a compaction.
func openExploreState(root string, wait time.Duration) (string, *os.File, error) {
	dir := filepath.Join(root, ".seamark")

	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", nil, fmt.Errorf("explore state directory is not a real directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", nil, err
		}
	} else {
		return "", nil, err
	}

	lockPath := filepath.Join(dir, exploreLockFile)
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("explore state lock is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}

	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY|deliveryNoFollow, 0o600)
	if err != nil {
		return "", nil, err
	}

	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()

		return "", nil, err
	}

	if err := lockDeliveryFileWithin(lock, wait); err != nil {
		_ = lock.Close()

		return "", nil, err
	}

	return dir, lock, nil
}

// readExploreState loads the state file. An absent file is an empty
// state. The reader refuses a symlink, a file that is not regular, a
// file above 1 MiB, another version, an unknown field, and a second
// JSON value. The caller then emits every item and never writes the
// file, so this binary never overwrites state that it cannot read.
func readExploreState(path string) (exploreState, error) {
	empty := exploreState{Version: exploreStateVersion, Contexts: make(map[string]exploreContextState)}

	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}

	if err != nil {
		return exploreState{}, err
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return exploreState{}, fmt.Errorf("explore state is not a regular file")
	}

	if info.Size() > maxExploreStateBytes {
		return exploreState{}, fmt.Errorf("explore state exceeds %d bytes", maxExploreStateBytes)
	}

	f, err := os.OpenFile(path, os.O_RDONLY|deliveryNoFollow, 0)
	if err != nil {
		return exploreState{}, err
	}
	defer func() { _ = f.Close() }()

	decoder := json.NewDecoder(io.LimitReader(f, maxExploreStateBytes+1))
	decoder.DisallowUnknownFields()

	var onDisk exploreState
	if err := decoder.Decode(&onDisk); err != nil {
		return exploreState{}, fmt.Errorf("decode explore state: %w", err)
	}

	if onDisk.Version != exploreStateVersion {
		return exploreState{}, fmt.Errorf("unsupported explore state version %d", onDisk.Version)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return exploreState{}, fmt.Errorf("decode explore state: multiple JSON values")
		}

		return exploreState{}, fmt.Errorf("decode explore state: %w", err)
	}

	state := empty
	if onDisk.Contexts != nil {
		state.Contexts = onDisk.Contexts
	}

	return state, nil
}

// writeExploreState replaces the state file through a private temporary
// file and a rename, so a reader never sees a partial file.
func writeExploreState(path string, state exploreState) error {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(state); err != nil {
		return err
	}

	if encoded.Len() > maxExploreStateBytes {
		return fmt.Errorf("explore state exceeds %d bytes", maxExploreStateBytes)
	}

	f, err := os.CreateTemp(filepath.Dir(path), ".lessons-explore-state-*")
	if err != nil {
		return err
	}

	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()

		return err
	}

	if _, err := f.Write(encoded.Bytes()); err != nil {
		_ = f.Close()

		return err
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()

		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

// pruneExploreState drops the entries that no exploration updated in the
// last 24 hours, so the file stays small.
func pruneExploreState(state *exploreState, now time.Time) {
	for key, entry := range state.Contexts {
		if entry.UpdatedAt.IsZero() || now.Sub(entry.UpdatedAt) > exploreStateTTL {
			delete(state.Contexts, key)
		}
	}
}
