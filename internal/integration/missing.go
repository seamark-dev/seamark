package integration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Missing returns the names that name no file or directory, in the order
// of names. A relative name resolves against root, and an absolute name
// is checked as it is. A lesson can name a future file, so callers show
// the result as information only.
//
// Only absence means missing (see absent). Another error, such as a
// denied directory, keeps the name out of the result, because the name
// can still exist. Missing uses Lstat, so a dangling link exists.
func Missing(root string, names []string) []string {
	var out []string

	for _, name := range names {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(name))
		}

		if _, err := os.Lstat(path); absent(err) {
			out = append(out, name)
		}
	}

	return out
}

// absent reports whether a stat error proves that the path does not
// exist: fs.ErrNotExist, or syscall.ENOTDIR. A file in the middle of a
// path gives ENOTDIR, and nothing can exist below a file. Missing and
// StatGuarded share this rule, so they agree about absence.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
