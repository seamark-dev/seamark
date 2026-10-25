//go:build !unix

package privatedir

import "os"

// TightenMode is best effort, because the platform has no POSIX modes.
func TightenMode(string, os.FileMode) error { return nil }
