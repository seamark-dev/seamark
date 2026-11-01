package history

import (
	"os"
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

	if top, ok := TopLevel(abs); ok {
		return top, nil
	}

	return abs, nil
}

// WidenedRoot returns the git top level above dir when dir is no top level
// itself. A command in dir then runs in that top level, so a route into
// dir leads back to the same state. ok is false for a top level and for a
// directory outside git.
func WidenedRoot(dir string) (string, bool) {
	top, ok := TopLevel(dir)
	if !ok {
		return "", false
	}

	topInfo, errTop := os.Stat(top)
	dirInfo, errDir := os.Stat(dir)

	if errTop != nil || errDir != nil || os.SameFile(topInfo, dirInfo) {
		return "", false
	}

	return top, true
}

// TopLevel returns the git top level of dir. ok is false when git does
// not know one. A caller then cannot tell a repository root from a plain
// directory, so it can apply the rule of a workspace without git.
func TopLevel(dir string) (top string, ok bool) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	return strings.TrimSpace(string(out)), true
}
