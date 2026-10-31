//go:build unix

package privatedir

import (
	"os"
	"syscall"
)

// NoFollow makes an open fail on a symbolic link. A link that appears
// between an Lstat check and the open then cannot redirect the open.
const NoFollow = syscall.O_NOFOLLOW

// TightenMode sets the exact mode on path. A private directory holds
// private data, so TightenMode resets a looser mode from an earlier run
// or from the user.
func TightenMode(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// lockFile takes an exclusive advisory flock. Concurrent hooks then run
// the tighten, rotate, and append steps of a log one at a time. The
// kernel releases the lock when the process ends.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the lock of lockFile.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
