package privatedir

import (
	"errors"
	"fmt"
	"os"
)

// EnsureRealDir creates dir with mode when it is absent. An existing
// entry must be a real directory. A cloned repository can commit a
// symbolic link, and a link must never redirect private data.
//
// A refused entry wraps unsafe, so the caller's errors.Is checks hold.
// A failed create starts with createPrefix, for example "archive:
// create", so the caller keeps its message text.
func EnsureRealDir(dir string, mode os.FileMode, unsafe error, createPrefix string) error {
	info, err := os.Lstat(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(dir, mode); err != nil {
			return fmt.Errorf("%s %s: %w", createPrefix, dir, err)
		}

		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%w: %s is not a real directory", unsafe, dir)
	}

	return nil
}

// EnsureGitignore writes body to the .gitignore at path when the file
// is absent or when its content differs. Then it sets mode on the file.
// The repository's .gitignore can lack a block for the directory, so the
// directory excludes itself.
//
// A symbolic link at path wraps unsafe, because a write through a link
// can change a file outside the directory.
func EnsureGitignore(path, body string, mode os.FileMode, unsafe error) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symbolic link", unsafe, path)
	}

	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if string(data) == body {
			return TightenMode(path, mode)
		}
	}

	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return err
	}

	return TightenMode(path, mode)
}
