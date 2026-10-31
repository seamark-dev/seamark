package privatedir

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// This file holds the hardening of an append-only JSON Lines log in a
// private directory. The policy gate audit log and the lesson-check
// audit stream both use it. Each caller keeps its own file names,
// limits, record encoding, and error texts.

// The scanner sizes for the first line of a log. Every append reads this
// line, so the first buffer is small. The scanner grows the buffer up to
// the maximum for a longer line, so the first size sets no limit.
const (
	firstLineStartBytes = 4 << 10
	firstLineMaxBytes   = 1 << 20
)

// RefuseLink returns refused(path) when path is a symbolic link. An
// absent path gives no error. A cloned repository can commit a link, and
// a write or a chmod through it can change a file outside the repository.
//
// The caller gives refused, because each caller keeps its own error text.
// An open also passes NoFollow, as a second guard against a link that
// appears after this check.
func RefuseLink(path string, refused func(path string) error) error {
	info, err := os.Lstat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return refused(path)
	}

	return nil
}

// LockLog takes the exclusive lock of a log and returns the function that
// releases the lock. path is a separate lock file, because a rotation
// renames the log, and a lock on the log itself would move with it.
//
// A link at path gives refused(path). An open through the link can create
// a file outside the repository, or lock and chmod an existing file. The
// lock file gets mode also when it exists, so a loose mode does not stay.
func LockLog(path string, mode os.FileMode, refused func(path string) error) (func(), error) {
	if err := RefuseLink(path, refused); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|NoFollow, mode)
	if err != nil {
		return nil, err
	}

	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, err
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}

	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}

// RotateLog prepares the log at path for an append. The caller holds the
// lock of LockLog. One rotated generation stays at path.1.
//
// The function sets mode before the rotation, because a rename keeps the
// mode, and a loose rotated file keeps the history readable for other
// users. The live log rotates when it reaches maxBytes, or when its
// oldest record is older than maxAge at now. The caller gives now, so
// one clock stamps and ages the records of that caller.
//
// The function removes a rotated generation older than maxAge. Its
// modification time is the time of its newest record. The file system
// sets that time, so this check uses the wall clock and not now.
func RotateLog(path string, maxBytes int64, maxAge time.Duration, mode os.FileMode, now time.Time) error {
	rotated := path + ".1"

	// The rotated generation gets the rules of the live log: a link is
	// removed and never followed, an old generation expires, and a loose
	// mode is tightened.
	if info, err := os.Lstat(rotated); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0, time.Since(info.ModTime()) > maxAge:
			if err := os.Remove(rotated); err != nil {
				return err
			}
		case info.Mode().Perm() != mode:
			if err := os.Chmod(rotated, mode); err != nil {
				return err
			}
		}
	}

	// A missing live log has nothing to tighten or rotate.
	f, err := os.OpenFile(path, os.O_RDONLY|NoFollow, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	// The chmod acts on the open file, never on a path that a writer can
	// swap after the check.
	chmodErr := f.Chmod(mode)
	info, statErr := f.Stat()
	stale := oldestRecordStale(f, maxAge, now)

	_ = f.Close()

	if err := errors.Join(chmodErr, statErr); err != nil {
		return err
	}

	if info.Size() >= maxBytes || stale {
		// A rename replaces a link at the destination itself, not its
		// target.
		return os.Rename(path, rotated)
	}

	return nil
}

// oldestRecordStale reads the "ts" field of the first record of an open
// log. A first line that does not read or parse counts as stale too,
// because a rotation moves it aside and keeps it readable in the rotated
// generation.
func oldestRecordStale(f *os.File, maxAge time.Duration, now time.Time) bool {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, firstLineStartBytes), firstLineMaxBytes)

	if !sc.Scan() {
		// An empty log has nothing to age. A read error counts as stale.
		return sc.Err() != nil
	}

	var first struct {
		TS time.Time `json:"ts"`
	}

	if err := json.Unmarshal(sc.Bytes(), &first); err != nil || first.TS.IsZero() {
		return true
	}

	return now.Sub(first.TS) > maxAge
}
