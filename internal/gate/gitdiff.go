package gate

import (
	"os/exec"

	"github.com/seamark-dev/seamark/internal/history"
)

// GitDiffHead returns the output of git diff HEAD in root. Both check
// callers use it when they get no diff, so the CLI and MCP read the same
// text for the same tree. The fixed options of history.ParsedGitArgs keep
// git config from changing that text. A changed text can drop files or
// lines, so a change can read as a clean allow. history.ReadOnlyGitEnviron
// keeps git diff from rewriting .git/index, so check does not race a
// concurrent git command of the user for .git/index.lock.
func GitDiffHead(root string) (string, error) {
	cmd := exec.Command("git", history.ParsedGitArgs("diff", "HEAD")...)
	cmd.Dir = root
	cmd.Env = history.ReadOnlyGitEnviron()
	out, err := cmd.Output()

	return string(out), err
}
