//go:build !unix

package privatedir

import "os"

// NoFollow is 0, because the platform has no O_NOFOLLOW. The Lstat checks
// before each open still refuse a symbolic link.
const NoFollow = 0

// TightenMode is best effort, because the platform has no POSIX modes.
func TightenMode(string, os.FileMode) error { return nil }

// lockFile takes no lock, because the platform has no flock. The writers
// stay best effort there: one write per record and O_APPEND keep whole
// lines.
func lockFile(*os.File) error { return nil }

// unlockFile matches lockFile.
func unlockFile(*os.File) error { return nil }
