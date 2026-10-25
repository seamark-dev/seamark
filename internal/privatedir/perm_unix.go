//go:build unix

package privatedir

import "os"

// TightenMode sets the exact mode on path. A private directory holds
// private data, so TightenMode resets a looser mode from an earlier run
// or from the user.
func TightenMode(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
