//go:build unix

package gate

import (
	"os"
	"syscall"
)

// noFollow makes audit-log opens refuse symlinks at the syscall level,
// closing the window between the Lstat check and the open.
const noFollow = syscall.O_NOFOLLOW

// lockFile takes an exclusive advisory flock so concurrent gate hooks
// serialize their tighten → rotate → append sequence. The kernel releases
// the lock if the process dies.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
