package history

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveRoot returns the git top level of dir, or the absolute dir when
// git does not know it. An empty dir means the current directory.
//
// The rule lives here, below index and workspace, so that both use it
// without an import between them. index.Run and the console then name
// the same index database. A git failure is not an error, because a
// directory outside a repository is still a workspace.
func ResolveRoot(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = abs

	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	return abs, nil
}
