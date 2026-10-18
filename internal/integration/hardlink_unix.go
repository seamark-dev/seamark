//go:build unix

package integration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// HardLinked reports whether the file rel under root has more than one
// hard link. The engine replaces a file by rename, and a rename gives
// the path a new file: every other name of the old file keeps the old
// bytes. So an editor refuses such a file and names the manual route.
// An absent file has no link and reports false.
func HardLinked(root, rel string) (bool, error) {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	st, ok := info.Sys().(*syscall.Stat_t)

	return ok && info.Mode().IsRegular() && st.Nlink > 1, nil
}
