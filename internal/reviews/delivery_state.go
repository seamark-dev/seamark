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
	deliveryStateVersion  = 1
	deliveryStateTTL      = 24 * time.Hour
	deliveryStateFile     = "lessons-hook-state.json"
	deliveryLockFile      = "lessons-hook-state.lock"
	maxDeliveryStateBytes = 1 << 20
)

type deliveryState struct {
	Version  int                             `json:"version"`
	Sessions map[string]deliverySessionState `json:"sessions"`
}

type deliverySessionState struct {
	Generation uint64          `json:"generation"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Delivered  map[string]bool `json:"delivered"`
}

// HookDeliveryLease is a locked selection of lessons not yet delivered in
// one provider context window. Commit must be called only after the selected
// context has been emitted successfully. Close releases the cross-process
// lock without marking anything delivered.
type HookDeliveryLease struct {
	dir        string
	lock       *os.File
	state      deliveryState
	sessionSHA string
	session    deliverySessionState
	inject     []model.Lesson
	suppressed []model.Lesson
	injectSHA  []string
	committed  bool
}

// BeginHookDelivery selects lessons for once-per-context delivery and keeps
// the state lock until Commit or Close. State leasing is supported on Unix,
// the platforms Seamark currently releases for. Other platforms return an
// error so callers inject normally instead of relying on unsafe, unlocked
// read-modify-write state. Callers must always fail open on any error: this
// state is an optimization, never permission to hide advice.
func BeginHookDelivery(root, sessionID string, lessons []model.Lesson) (*HookDeliveryLease, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("hook delivery session id is empty")
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

	sessionSHA := sessionDigest(root, sessionID)
	session := state.Sessions[sessionSHA]

	if session.Generation == 0 {
		session.Generation = 1
	}

	if session.Delivered == nil {
		session.Delivered = make(map[string]bool)
	}

	lease := &HookDeliveryLease{
		dir: dir, lock: lock, state: state, sessionSHA: sessionSHA, session: session,
	}

	pending := make(map[string]bool, len(lessons))

	for _, lesson := range lessons {
		digest := lessonDeliveryDigest(lesson)

		if session.Delivered[digest] || pending[digest] {
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
// benchmark diagnostics. It advances after every PostCompact reset.
func (l *HookDeliveryLease) Generation() uint64 {
	return l.session.Generation
}

// Commit marks the selected lessons delivered. It is intentionally separate
// from BeginHookDelivery so a failed stdout write never creates false state.
func (l *HookDeliveryLease) Commit() error {
	if l.committed || len(l.injectSHA) == 0 {
		l.committed = true
		return nil
	}

	for _, digest := range l.injectSHA {
		l.session.Delivered[digest] = true
	}

	l.session.UpdatedAt = time.Now().UTC()
	l.state.Sessions[l.sessionSHA] = l.session

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

// ResetHookDelivery advances a session's context generation and clears its
// delivered lesson set. Claude Code's PostCompact hook calls this so lessons
// may be injected once again after old context has been summarized away.
func ResetHookDelivery(root, sessionID string) error {
	if sessionID == "" {
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
	key := sessionDigest(root, sessionID)
	session := state.Sessions[key]
	session.Generation++

	if session.Generation == 0 {
		session.Generation = 1
	}

	session.UpdatedAt = now
	session.Delivered = make(map[string]bool)
	state.Sessions[key] = session

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

func readDeliveryState(path string) (deliveryState, error) {
	state := deliveryState{Version: deliveryStateVersion, Sessions: make(map[string]deliverySessionState)}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return state, nil
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

	if err := decoder.Decode(&state); err != nil {
		return deliveryState{}, fmt.Errorf("decode delivery state: %w", err)
	}

	if state.Version != deliveryStateVersion {
		return deliveryState{}, fmt.Errorf("unsupported delivery state version %d", state.Version)
	}

	if state.Sessions == nil {
		state.Sessions = make(map[string]deliverySessionState)
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
	for key, session := range state.Sessions {
		if session.UpdatedAt.IsZero() || now.Sub(session.UpdatedAt) > deliveryStateTTL {
			delete(state.Sessions, key)
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
