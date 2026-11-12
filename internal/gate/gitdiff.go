package gate

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/render"
)

// The routes that each check surface adds to a GitDiffHead error. Both
// texts sit here, so the CLI and MCP give the same reason for the same
// tree. Each route can run in every state, because neither needs git.
const (
	DiffRouteCLI = "pipe a unified diff to check on stdin"
	DiffRouteMCP = "pass the unified diff in the diff argument"
)

// maxGitReasonBytes bounds the git stderr line in a GitDiffHead error.
// Outside git, git diff writes its full usage text, which is many lines
// and of no use to a check caller.
const maxGitReasonBytes = 200

// GitDiffHead returns the output of git diff HEAD in root. Both check
// callers use it when they get no diff, so the CLI and MCP read the same
// text for the same tree. The fixed options of history.ParsedGitArgs keep
// git config from changing that text. A changed text can drop files or
// lines, so a change can read as a clean allow. history.ReadOnlyGitEnviron
// keeps git diff from rewriting .git/index, so check does not race a
// concurrent git command of the user for .git/index.lock.
//
// When git diff HEAD fails, the error says why in one sanitized line. See
// diffHeadFailure.
func GitDiffHead(root string) (string, error) {
	cmd := exec.Command("git", history.ParsedGitArgs("diff", "HEAD")...)
	cmd.Dir = root
	cmd.Env = history.ReadOnlyGitEnviron()

	out, err := cmd.Output()
	if err != nil {
		return "", diffHeadFailure(root, err)
	}

	return string(out), nil
}

// diffHeadFailure turns a failed git diff HEAD into a reason that a check
// caller can act on. It asks git for the two common causes, a folder
// outside git and a repository with no commit, because the stderr text of
// git depends on the locale and the git version. Outside git, the first
// stderr line also offers --no-index, which check cannot use. Only other
// failures show the git text. The probes run after the failure, so a
// successful check keeps one git process.
func diffHeadFailure(root string, err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("no diff given, and git diff HEAD did not run: %s", render.Sanitize(err.Error()))
	}

	where := render.Sanitize(root)

	if !history.IsRepo(root) {
		return fmt.Errorf("no diff given, and %s is not inside a git work tree, so git diff HEAD cannot run", where)
	}

	if !hasCommit(root) {
		return fmt.Errorf("no diff given, and the repository at %s has no commit yet, so git diff HEAD has nothing to compare", where)
	}

	reason := fmt.Sprintf("no diff given, and git diff HEAD failed (exit status %d)", exitErr.ExitCode())

	if line := firstLine(exitErr.Stderr); line != "" {
		reason += ": " + line
	}

	return errors.New(reason)
}

// hasCommit reports whether HEAD names a commit in root. git rev-parse
// exits 1 for an unborn HEAD.
func hasCommit(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Dir = root
	cmd.Env = history.ReadOnlyGitEnviron()

	return cmd.Run() == nil
}

// firstLine returns the first non-empty line of stderr, sanitized for a
// terminal and cut to maxGitReasonBytes. Sanitize runs on one line only,
// because it joins lines and the rest of the text has no use here.
func firstLine(stderr []byte) string {
	for line := range strings.SplitSeq(string(stderr), "\n") {
		if clean := strings.TrimSpace(render.Sanitize(line)); clean != "" {
			return render.CutBytes(clean, maxGitReasonBytes)
		}
	}

	return ""
}
