//go:build !unix

package lessoncheck

import "os"

// noFollowFlag is 0, because the platform has no O_NOFOLLOW. The Lstat
// checks before each open still refuse a symbolic link.
const noFollowFlag = 0

// lockFile takes no lock, because the platform has no flock. The
// writers stay best effort there: one Write per record and O_APPEND
// keep whole lines.
func lockFile(*os.File) error { return nil }

// unlockFile matches lockFile.
func unlockFile(*os.File) error { return nil }
