package privatedir_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/privatedir"
)

// The limits of the rotation tests.
const (
	testMaxBytes = 1 << 10
	testMaxAge   = 30 * 24 * time.Hour
)

// linkRefused stands for the error text of a caller.
func linkRefused(path string) error {
	return fmt.Errorf("%w: %s refused", errUnsafe, path)
}

// logRecord returns one log line with the time ts.
func logRecord(ts time.Time) string {
	return `{"ts":"` + ts.UTC().Format(time.RFC3339) + `"}` + "\n"
}

// writeLog writes body to a new log in a temporary directory with a
// loose mode and returns the path of the log.
func writeLog(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))

	return path
}

// rotateLog runs RotateLog with the test limits.
func rotateLog(t *testing.T, path string, now time.Time) {
	t.Helper()

	require.NoError(t, privatedir.RotateLog(path, testMaxBytes, testMaxAge, 0o600, now))
}

func TestPrivateDirRefuseLink(t *testing.T) {
	t.Run("an absent path passes", func(t *testing.T) {
		assert.NoError(t, privatedir.RefuseLink(filepath.Join(t.TempDir(), "absent"), linkRefused))
	})

	t.Run("a regular file passes", func(t *testing.T) {
		assert.NoError(t, privatedir.RefuseLink(writeLog(t, "x\n"), linkRefused))
	})

	t.Run("a link gives the error of the caller", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "link")
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), path))

		err := privatedir.RefuseLink(path, linkRefused)
		require.ErrorIs(t, err, errUnsafe)
		assert.Equal(t, "unsafe test path: "+path+" refused", err.Error())
	})

	t.Run("another Lstat failure stays as it is", func(t *testing.T) {
		if !posixModes {
			t.Skip("the platform gives a different error for this path")
		}

		err := privatedir.RefuseLink(filepath.Join(writeLog(t, "x\n"), "child"), linkRefused)
		require.Error(t, err)
		assert.NotErrorIs(t, err, errUnsafe)
	})
}

func TestPrivateDirLockLog(t *testing.T) {
	t.Run("a new lock file gets the mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.lock")

		unlock, err := privatedir.LockLog(path, 0o600, linkRefused)
		require.NoError(t, err)

		defer unlock()

		assertMode(t, path, 0o600)
	})

	t.Run("an existing loose lock file is tightened", func(t *testing.T) {
		path := writeLog(t, "")

		unlock, err := privatedir.LockLog(path, 0o600, linkRefused)
		require.NoError(t, err)

		defer unlock()

		assertMode(t, path, 0o600)
	})

	t.Run("a link is refused and its target stays absent", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		path := filepath.Join(t.TempDir(), "audit.lock")
		require.NoError(t, os.Symlink(target, path))

		unlock, err := privatedir.LockLog(path, 0o600, linkRefused)
		require.ErrorIs(t, err, errUnsafe)
		assert.Nil(t, unlock)
		assert.NoFileExists(t, target)
	})

	t.Run("a second lock waits for the first", func(t *testing.T) {
		if !posixModes {
			t.Skip("the platform has no flock")
		}

		path := filepath.Join(t.TempDir(), "audit.lock")

		unlock, err := privatedir.LockLog(path, 0o600, linkRefused)
		require.NoError(t, err)

		acquired := make(chan error, 1)

		go func() {
			next, err := privatedir.LockLog(path, 0o600, linkRefused)
			if err == nil {
				next()
			}

			acquired <- err
		}()

		select {
		case err := <-acquired:
			require.Fail(t, "the second lock must wait while the first lock is held", "lock error: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		unlock()
		require.NoError(t, <-acquired)
	})
}

func TestPrivateDirRotateLog(t *testing.T) {
	wall := time.Now()

	t.Run("a missing log is no error and makes no file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")

		rotateLog(t, path, wall)
		assert.NoFileExists(t, path)
		assert.NoFileExists(t, path+".1")
	})

	t.Run("a fresh log stays and is tightened", func(t *testing.T) {
		path := writeLog(t, logRecord(wall))

		rotateLog(t, path, wall)
		assert.NoFileExists(t, path+".1")
		assertMode(t, path, 0o600)
	})

	t.Run("a log below the size limit stays", func(t *testing.T) {
		path := writeLog(t, logRecord(wall)+strings.Repeat("x", testMaxBytes-1-len(logRecord(wall))))

		rotateLog(t, path, wall)
		assert.NoFileExists(t, path+".1")
	})

	t.Run("a log at the size limit rotates with a tight mode", func(t *testing.T) {
		body := logRecord(wall) + strings.Repeat("x", testMaxBytes-len(logRecord(wall)))
		path := writeLog(t, body)

		rotateLog(t, path, wall)
		assert.NoFileExists(t, path, "the next append starts a new live log")
		assertMode(t, path+".1", 0o600)

		data, err := os.ReadFile(path + ".1")
		require.NoError(t, err)
		assert.Equal(t, body, string(data))
	})

	t.Run("a rotation replaces the earlier generation", func(t *testing.T) {
		path := writeLog(t, "not json\n")
		require.NoError(t, os.WriteFile(path+".1", []byte("earlier\n"), 0o600))

		rotateLog(t, path, wall)

		data, err := os.ReadFile(path + ".1")
		require.NoError(t, err)
		assert.Equal(t, "not json\n", string(data))
	})

	t.Run("now ages the live log, not the wall clock", func(t *testing.T) {
		stamped := wall.Add(-90 * 24 * time.Hour)

		path := writeLog(t, logRecord(stamped))
		rotateLog(t, path, stamped.Add(testMaxAge-time.Hour))
		assert.NoFileExists(t, path+".1", "the record is inside the age limit at now")

		rotateLog(t, path, stamped.Add(testMaxAge+time.Hour))
		assert.FileExists(t, path+".1", "the record is past the age limit at now")

		future := writeLog(t, logRecord(wall))
		rotateLog(t, future, wall.Add(testMaxAge+time.Hour))
		assert.FileExists(t, future+".1", "a fresh record by the wall clock ages at a later now")
	})

	t.Run("the wall clock ages the rotated generation, not now", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		require.NoError(t, os.WriteFile(path+".1", []byte("history\n"), 0o600))

		rotateLog(t, path, wall.Add(10*testMaxAge))
		assert.FileExists(t, path+".1", "a later now does not expire a fresh generation")

		past := wall.Add(-testMaxAge - time.Hour)
		require.NoError(t, os.Chtimes(path+".1", past, past))

		rotateLog(t, path, past)
		assert.NoFileExists(t, path+".1", "an earlier now does not keep an old generation")
	})

	t.Run("a loose rotated generation is tightened and keeps its data", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		require.NoError(t, os.WriteFile(path+".1", []byte("history\n"), 0o644))
		require.NoError(t, os.Chmod(path+".1", 0o644))

		rotateLog(t, path, wall)
		assertMode(t, path+".1", 0o600)

		data, err := os.ReadFile(path + ".1")
		require.NoError(t, err)
		assert.Equal(t, "history\n", string(data))
	})

	t.Run("a link at the rotated generation is removed and its target stays", func(t *testing.T) {
		target := writeLog(t, "keep\n")
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		require.NoError(t, os.Symlink(target, path+".1"))

		rotateLog(t, path, wall)

		_, err := os.Lstat(path + ".1")
		require.ErrorIs(t, err, os.ErrNotExist)
		assertMode(t, target, 0o644)

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})

	t.Run("a link at the live log fails and its target stays", func(t *testing.T) {
		if !posixModes {
			t.Skip("the platform has no O_NOFOLLOW")
		}

		target := writeLog(t, "keep\n")
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		require.NoError(t, os.Symlink(target, path))

		require.Error(t, privatedir.RotateLog(path, testMaxBytes, testMaxAge, 0o600, wall))
		assertMode(t, target, 0o644)
		assert.NoFileExists(t, path+".1")
	})
}

// TestPrivateDirRotateLogFirstLine covers each form of a first line. The
// size limit stays above every body, so only the first line decides.
func TestPrivateDirRotateLogFirstLine(t *testing.T) {
	const noSizeLimit = 1 << 30

	wall := time.Now()
	padded := `{"ts":"` + wall.UTC().Format(time.RFC3339) + `","pad":"` + strings.Repeat("x", 512<<10) + `"}` + "\n"

	for _, tc := range []struct {
		name    string
		body    string
		rotates bool
	}{
		{name: "an empty log has nothing to age", body: "", rotates: false},
		{name: "a line past the first buffer stays readable", body: padded, rotates: false},
		{name: "a line that is not JSON rotates", body: "not json\n", rotates: true},
		{name: "a record without a time rotates", body: `{"kind":"x"}` + "\n", rotates: true},
		{name: "a line past the scanner limit rotates", body: strings.Repeat("x", 2<<20), rotates: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLog(t, tc.body)

			require.NoError(t, privatedir.RotateLog(path, noSizeLimit, testMaxAge, 0o600, wall))

			if tc.rotates {
				assert.FileExists(t, path+".1")
				return
			}

			assert.NoFileExists(t, path+".1")
		})
	}
}
