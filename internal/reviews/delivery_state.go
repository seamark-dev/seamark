package reviews

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

const (
	// deliveryStateVersion 2 keys the state by client and receiving
	// context. The key of version 1 is the provider session alone. Two
	// clients, or a parent agent and its subagent, can report one session
	// string, and they then share one version 1 entry.
	deliveryStateVersion  = 2
	deliveryStateTTL      = 24 * time.Hour
	deliveryStateFile     = "lessons-hook-state.json"
	deliveryLockFile      = "lessons-hook-state.lock"
	maxDeliveryStateBytes = 1 << 20
)

// LegacyClientID is the client of the session-keyed entry points
// BeginHookDelivery and ResetHookDelivery. Claude Code was the only
// client when those entry points were the API, so they keep its name.
// The receiver of those entry points is the raw session string. A
// client adapter builds its own receiver ID, so an adapter and these
// entry points do not share a state entry.
const LegacyClientID = "claude"

// DeliveryContext identifies the conversation that receives advice:
// the client, and the client's own identity of the receiver. The state
// and the firing log store a repository-scoped digest, never ReceiverID.
type DeliveryContext struct {
	// ClientID is the registry ID of the client. It separates two
	// clients that report the same receiver string.
	ClientID string
	// ReceiverID is the actual receiver, not merely a parent session.
	ReceiverID string
}

// valid reports whether the context names a client and a receiver. A
// partial identity must never select state: it can name another context.
func (c DeliveryContext) valid() bool {
	return c.ClientID != "" && c.ReceiverID != ""
}

type deliveryState struct {
	Version  int                             `json:"version"`
	Contexts map[string]deliveryContextState `json:"contexts"`
}

type deliveryContextState struct {
	Generation uint64          `json:"generation"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Delivered  map[string]bool `json:"delivered"`
}

// deliveryStateFileShape is what a state file can hold on disk. The
// sessions key belongs to version 1. The reader accepts the key and
// ignores its entries, and no writer of this version emits it.
type deliveryStateFileShape struct {
	Version  int                             `json:"version"`
	Contexts map[string]deliveryContextState `json:"contexts"`
	Sessions map[string]json.RawMessage      `json:"sessions"`
}

// HookDeliveryLease is a locked selection of lessons not yet delivered in
// one provider context window. Commit must be called only after the selected
// context has been emitted successfully. Close releases the cross-process
// lock without marking anything delivered.
type HookDeliveryLease struct {
	dir        string
	lock       *os.File
	state      deliveryState
	contextSHA string
	context    deliveryContextState
	inject     []model.Lesson
	suppressed []model.Lesson
	injectSHA  []string
	committed  bool
}

// BeginHookDelivery is BeginContextDelivery for the session-keyed Claude
// Code hook: the session is the receiver and the client is LegacyClientID.
func BeginHookDelivery(root, sessionID string, lessons []model.Lesson) (*HookDeliveryLease, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("hook delivery session id is empty")
	}

	return BeginContextDelivery(root, DeliveryContext{ClientID: LegacyClientID, ReceiverID: sessionID}, lessons)
}

// BeginContextDelivery selects lessons for once-per-context delivery and keeps
// the state lock until Commit or Close. State leasing is supported on Unix,
// the platforms Seamark currently releases for. Other platforms return an
// error so callers inject normally instead of relying on unsafe, unlocked
// read-modify-write state. Callers must always fail open on any error: this
// state is an optimization, never permission to hide advice.
func BeginContextDelivery(root string, dc DeliveryContext, lessons []model.Lesson) (*HookDeliveryLease, error) {
	if !dc.valid() {
		return nil, fmt.Errorf("hook delivery context names no client or no receiver")
	}

	dir, lock, err := openDeliveryState(root)
	if err != nil {
		return nil, err
	}

	state, err := readDeliveryState(filepath.Join(dir, deliveryStateFile))
	if err != nil {
		_ = unlockDeliveryFile(lock)
		_ = lock.Close()

		return nil, err
	}

	now := time.Now().UTC()
	pruneDeliveryState(&state, now)

	contextSHA := contextDigest(root, dc)
	current := state.Contexts[contextSHA]

	if current.Generation == 0 {
		current.Generation = 1
	}

	if current.Delivered == nil {
		current.Delivered = make(map[string]bool)
	}

	lease := &HookDeliveryLease{
		dir: dir, lock: lock, state: state, contextSHA: contextSHA, context: current,
	}

	pending := make(map[string]bool, len(lessons))

	for _, lesson := range lessons {
		digest := lessonDeliveryDigest(lesson)

		if current.Delivered[digest] || pending[digest] {
			lease.suppressed = append(lease.suppressed, lesson)
			continue
		}

		pending[digest] = true
		lease.inject = append(lease.inject, lesson)
		lease.injectSHA = append(lease.injectSHA, digest)
	}

	return lease, nil
}

// Inject returns the lessons that still need to reach this context window.
func (l *HookDeliveryLease) Inject() []model.Lesson {
	return l.inject
}

// Suppressed returns matching lessons already delivered in this context.
func (l *HookDeliveryLease) Suppressed() []model.Lesson {
	return l.suppressed
}

// Generation identifies the current provider context window for audit and
// benchmark diagnostics. It advances after every context reset.
func (l *HookDeliveryLease) Generation() uint64 {
	return l.context.Generation
}

// Commit marks the selected lessons delivered. It is intentionally separate
// from BeginHookDelivery so a failed stdout write never creates false state.
func (l *HookDeliveryLease) Commit() error {
	if l.committed || len(l.injectSHA) == 0 {
		l.committed = true
		return nil
	}

	for _, digest := range l.injectSHA {
		l.context.Delivered[digest] = true
	}

	l.context.UpdatedAt = time.Now().UTC()
	l.state.Contexts[l.contextSHA] = l.context

	if err := writeDeliveryState(filepath.Join(l.dir, deliveryStateFile), l.state); err != nil {
		return err
	}

	l.committed = true

	return nil
}

// Close releases a delivery lease. It is safe to call after Commit.
func (l *HookDeliveryLease) Close() error {
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

// ResetHookDelivery is ResetContextDelivery for the session-keyed Claude
// Code hook. An empty session resets nothing.
func ResetHookDelivery(root, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	return ResetContextDelivery(root, DeliveryContext{ClientID: LegacyClientID, ReceiverID: sessionID})
}

// ResetContextDelivery advances a receiving context's generation and clears
// its delivered lesson set. A client's context-reset hook calls this after the
// client summarizes old context, so the lessons can reach the agent again. A
// context that names no client or no receiver resets nothing: a partial
// identity must not reset an unrelated context.
func ResetContextDelivery(root string, dc DeliveryContext) error {
	if !dc.valid() {
		return nil
	}
	// Do not create state merely because init installs the lifecycle hook. A
	// file exists only after once-per-context delivery has actually been used.
	if _, err := os.Lstat(filepath.Join(root, ".seamark", deliveryStateFile)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	dir, lock, err := openDeliveryState(root)
	if err != nil {
		return err
	}
	defer func() { _ = unlockDeliveryFile(lock); _ = lock.Close() }()

	path := filepath.Join(dir, deliveryStateFile)
	state, err := readDeliveryState(path)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	pruneDeliveryState(&state, now)
	key := contextDigest(root, dc)
	current := state.Contexts[key]
	current.Generation++

	if current.Generation == 0 {
		current.Generation = 1
	}

	current.UpdatedAt = now
	current.Delivered = make(map[string]bool)
	state.Contexts[key] = current

	return writeDeliveryState(path, state)
}

func openDeliveryState(root string) (string, *os.File, error) {
	dir := filepath.Join(root, ".seamark")

	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", nil, fmt.Errorf("delivery state directory is not a real directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", nil, err
		}
	} else {
		return "", nil, err
	}

	lockPath := filepath.Join(dir, deliveryLockFile)
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("delivery state lock is a symlink")
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

	if err := lockDeliveryFile(lock); err != nil {
		_ = lock.Close()
		return "", nil, err
	}

	return dir, lock, nil
}

// readDeliveryState loads the state file. It accepts two versions.
// Version 2 is the current shape. Version 1 reads as an empty state.
// A version 1 entry names a provider session, not a receiving context.
// An extra reminder is better than advice hidden from a context that
// never got it. The next state write replaces the file with version 2.
// Any other version is an error, so the caller delivers repeatedly and
// this binary never overwrites a file of a newer version.
func readDeliveryState(path string) (deliveryState, error) {
	empty := deliveryState{Version: deliveryStateVersion, Contexts: make(map[string]deliveryContextState)}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return deliveryState{}, err
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return deliveryState{}, fmt.Errorf("delivery state is not a regular file")
	}

	if info.Size() > maxDeliveryStateBytes {
		return deliveryState{}, fmt.Errorf("delivery state exceeds %d bytes", maxDeliveryStateBytes)
	}

	f, err := os.OpenFile(path, os.O_RDONLY|deliveryNoFollow, 0)
	if err != nil {
		return deliveryState{}, err
	}
	defer func() { _ = f.Close() }()

	decoder := json.NewDecoder(io.LimitReader(f, maxDeliveryStateBytes+1))
	decoder.DisallowUnknownFields()

	var onDisk deliveryStateFileShape
	if err := decoder.Decode(&onDisk); err != nil {
		return deliveryState{}, fmt.Errorf("decode delivery state: %w", err)
	}

	state := empty

	switch onDisk.Version {
	case deliveryStateVersion:
		if onDisk.Contexts != nil {
			state.Contexts = onDisk.Contexts
		}
	case 1:
		// Discard the session-keyed entries; see the function comment.
	default:
		return deliveryState{}, fmt.Errorf("unsupported delivery state version %d", onDisk.Version)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return deliveryState{}, fmt.Errorf("decode delivery state: multiple JSON values")
		}

		return deliveryState{}, fmt.Errorf("decode delivery state: %w", err)
	}

	return state, nil
}

func writeDeliveryState(path string, state deliveryState) error {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(state); err != nil {
		return err
	}

	if encoded.Len() > maxDeliveryStateBytes {
		return fmt.Errorf("delivery state exceeds %d bytes", maxDeliveryStateBytes)
	}

	f, err := os.CreateTemp(filepath.Dir(path), ".lessons-hook-state-*")
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

func pruneDeliveryState(state *deliveryState, now time.Time) {
	for key, entry := range state.Contexts {
		if entry.UpdatedAt.IsZero() || now.Sub(entry.UpdatedAt) > deliveryStateTTL {
			delete(state.Contexts, key)
		}
	}
}

func lessonDeliveryDigest(lesson model.Lesson) string {
	identity := (FiredLesson{Region: lesson.Region, Symptom: lesson.Symptom}).canonicalIdentity()
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity.Region+"\x00"+identity.Symptom)))
}

func sessionDigest(root, sessionID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(root+"\x00"+sessionID)))
}

// contextDigest is the on-disk identity of a receiving context. The
// root makes the digest useless for correlation across repositories.
// The leading label keeps the digest apart from sessionDigest, so a
// version 2 key never equals a version 1 key. Each part carries its
// length. A receiver string is client input and can hold any byte.
// With a plain separator, two different identities can give one digest.
func contextDigest(root string, dc DeliveryContext) string {
	h := sha256.New()

	for _, part := range []string{"context", root, dc.ClientID, dc.ReceiverID} {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
