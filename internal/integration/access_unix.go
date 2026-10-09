//go:build unix

package integration

import (
	"errors"
	"io/fs"
	"syscall"
)

// accessWrite is W_OK of <unistd.h>. The syscall package does not export
// it on every Unix, and the value is 2 on all of them.
const accessWrite = 0x2

// writeDenied reports whether this user may not write the existing file
// at abs. The engine replaces a file by rename, and a rename needs write
// access to the directory only. The mode bits do not show an access
// control list or another owner, so the engine asks access(2). It applies
// the rules of the open of a plain write, so the engine refuses a file
// that a plain write would also fail on.
// access(2) checks the real user ID; seamark never runs setuid, so that
// ID is the effective one.
//
// Only a refusal counts. Another error, such as a file that went away
// after the read, leaves the decision to the stale check of the guard.
func writeDenied(abs string) bool {
	err := syscall.Access(abs, accessWrite)

	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}
