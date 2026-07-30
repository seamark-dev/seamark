//go:build !unix

package gate

import "os"

// Non-unix platforms (not supported targets today) get plain opens and no
// advisory locking: best-effort single-writer behaviour. The Lstat-based
// symlink refusal in audit.go still applies.
const noFollow = 0

// lockFile is a no-op without flock: concurrent writers are unserialized
// on this platform, and rely on O_APPEND keeping whole lines intact.
func lockFile(*os.File) error { return nil }

// unlockFile matches the no-op lockFile.
func unlockFile(*os.File) error { return nil }
