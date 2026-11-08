package gate

import "os/exec"

// gitDiffHeadArgs are the git arguments of the git diff HEAD fallback.
// Each option keeps a user or repository git configuration from changing
// the diff text that EvalDiff parses. A changed text can drop files or
// lines, so a change can read as a clean allow.
var gitDiffHeadArgs = []string{
	// diff.relative=true limits the diff to the current directory and
	// strips that directory from the paths. A config override works on
	// every git version, and --no-relative needs git 2.28.
	"-c", "diff.relative=false",
	"diff",
	// color.ui or color.diff set to always wraps each line in color codes.
	"--no-color",
	// diff.external and GIT_EXTERNAL_DIFF replace the unified diff with
	// the output of another program.
	"--no-ext-diff",
	// A textconv driver of the gitattributes replaces the file content
	// before git compares it.
	"--no-textconv",
	// diff.noprefix and diff.mnemonicPrefix change the a/ and b/ path
	// prefixes. The explicit prefixes work on older git than
	// --default-prefix.
	"--src-prefix=a/", "--dst-prefix=b/",
	// diff.submodule=log or diff writes a submodule change without a
	// diff --git header. The short form keeps that header.
	"--submodule=short",
	"HEAD",
}

// GitDiffHead returns the output of git diff HEAD in root. Both check
// callers use it when they get no diff, so the CLI and MCP read the same
// text for the same tree.
func GitDiffHead(root string) (string, error) {
	cmd := exec.Command("git", gitDiffHeadArgs...)
	cmd.Dir = root
	out, err := cmd.Output()

	return string(out), err
}
