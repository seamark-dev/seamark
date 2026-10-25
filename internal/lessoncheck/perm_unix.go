//go:build unix

package lessoncheck

import (
	"os"
	"syscall"
)

// noFollowFlag makes a write-side open refuse a symbolic link at the
// system call. So a link that appears between the Lstat check and the
// open cannot redirect a record (contracts K7, K9).
const noFollowFlag = syscall.O_NOFOLLOW

// lockFile takes an exclusive advisory flock. Concurrent hooks then run
// the tighten, rotate, and append steps of the audit stream one at a
// time. The kernel releases the lock when the process ends.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the lock of lockFile.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
