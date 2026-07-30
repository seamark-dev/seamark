//go:build !unix

package gate

import "os"

// Non-unix platforms (not supported targets today) get plain opens and no
// advisory locking: best-effort single-writer behaviour. The Lstat-based
// symlink refusal in audit.go still applies.
const noFollow = 0

func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
