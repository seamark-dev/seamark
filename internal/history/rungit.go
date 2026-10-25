package history

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// maxGitStderrBytes is max-git-stderr-bytes (contracts K3 output caps).
// Stderr serves messages only, so the runner keeps the start and discards
// the rest.
const maxGitStderrBytes = 64 << 10

// gitWaitDelay bounds pipe cleanup after git ends or the context ends,
// even when a child process of git keeps a pipe open. The guard budget
// is 5 s (contracts K3), so the delay stays short. Tests can shorten it.
var gitWaitDelay = time.Second

// ErrGitOutputLimit reports that git wrote more stdout than
// GitCall.MaxStdout allows. RunGit then stops git and returns no stdout,
// because a truncated output must never become a result.
var ErrGitOutputLimit = errors.New("history: git output exceeds the limit")

// GitCall is one git invocation for RunGit.
type GitCall struct {
	// Dir is the working directory of git. An empty Dir means the
	// current directory.
	Dir string
	// Args are the git arguments without the "git" program name.
	Args []string
	// Stdin feeds git. A nil Stdin reads as empty. A reader that blocks
	// keeps RunGit waiting after git ends, because os/exec waits for
	// the copy to finish. Use an in-memory reader or an *os.File.
	Stdin io.Reader
	// MaxStdout is the largest stdout in bytes that RunGit accepts.
	// More output stops git and gives ErrGitOutputLimit. Zero accepts
	// no output, so every call states its bound.
	MaxStdout int64
}

// GitOutput is the result of a git invocation that ran to its end.
type GitOutput struct {
	// Stdout is the raw output, not split and not trimmed.
	Stdout []byte
	// Stderr holds the first maxGitStderrBytes of the error output. It
	// is for messages only, because the runner discards the rest.
	Stderr []byte
	// ExitCode is the exit status of git. It is -1 when a signal ended
	// git. It has a meaning only when RunGit returns no error.
	ExitCode int
}

// RunGit runs git with ctx. It appends GIT_OPTIONAL_LOCKS=0,
// GIT_TERMINAL_PROMPT=0, and LC_ALL=C to gitEnviron(). The first stops
// the optional index lock. The second stops a password prompt. The third
// keeps the git messages in one language for every user, because the K4
// snapshot shows a status stderr line as a notice. The error is non-nil
// only when git did not run, ctx ended (ctx.Err()), or stdout passed
// call.MaxStdout (ErrGitOutputLimit). A non-zero exit of git is not an
// error, because some exit codes are answers. For example, `rev-parse
// --verify --quiet` exits 1 in an unborn repository. The caller reads
// GitOutput.ExitCode.
//
// RunGit is the one runner of this kind (contracts K4), for these reasons:
//   - gitScan splits lines, drops a trailing "\r", stops after 5 s, and
//     hides the exit code. NUL records, raw blobs, and the exit code of
//     an unborn HEAD need all three.
//   - The other runners live in packages that lesson checks must not
//     import (index, cli, fixes, learning; BRIEF A1).
//   - A plain `git status` rewrites the stat cache in .git/index. RunGit
//     cannot import cli.readOnlyGitEnv, so it sets GIT_OPTIONAL_LOCKS=0
//     itself.
//
// RunGit does not set diff.autoRefreshIndex=false, so a `git diff` through
// it can still write .git/index. The K4 snapshot runs no `git diff`.
func RunGit(ctx context.Context, call GitCall) (GitOutput, error) {
	// runCtx also ends when stdout passes the limit, so that exec kills
	// git at once instead of after the next write.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "git", call.Args...)
	cmd.Dir = call.Dir
	cmd.Stdin = call.Stdin
	cmd.Env = append(gitEnviron(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.WaitDelay = gitWaitDelay

	stdout := &limitedStdout{limit: call.MaxStdout, stop: cancel}
	stderr := &cappedStderr{limit: maxGitStderrBytes}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	// The copy goroutines end before Run returns, so the flag is safe to
	// read here. The limit comes first, because the stop also ends runCtx.
	if stdout.exceeded {
		return GitOutput{}, ErrGitOutputLimit
	}

	if err != nil && ctx.Err() != nil {
		return GitOutput{}, ctx.Err()
	}

	var exitErr *exec.ExitError

	if err != nil && !errors.As(err, &exitErr) {
		return GitOutput{}, fmt.Errorf("history: run git: %w", err)
	}

	out := GitOutput{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}

	if exitErr != nil {
		out.ExitCode = exitErr.ExitCode()
	}

	return out, nil
}

// gitEnviron returns os.Environ() without three kinds of entry:
//   - The GIT_TRACE variables, for example GIT_TRACE, GIT_TRACE2, and
//     GIT_TRACE_SETUP. A trace setting writes lines with a time to stderr.
//     Trace output names no path, so it must not add a git-stderr notice
//     to every lesson verification.
//   - LC_ALL, because RunGit sets LC_ALL=C, and one entry per name keeps
//     the child environment clear.
//   - LANGUAGE, because gettext can read it before LC_ALL and translate
//     the git messages (ruling D2).
func gitEnviron() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "GIT_TRACE") || strings.HasPrefix(entry, "LC_ALL=") || strings.HasPrefix(entry, "LANGUAGE=")
	})
}

// limitedStdout keeps git's stdout up to limit bytes. The first write
// past the limit sets exceeded, calls stop, and fails, so os/exec stops
// the copy and git gets no more room to write.
type limitedStdout struct {
	buf      bytes.Buffer
	limit    int64
	stop     func()
	exceeded bool
}

// Write appends p, or fails with ErrGitOutputLimit when p passes the limit.
func (w *limitedStdout) Write(p []byte) (int, error) {
	if w.exceeded || int64(w.buf.Len())+int64(len(p)) > w.limit {
		w.exceeded = true
		w.stop()

		return 0, ErrGitOutputLimit
	}

	return w.buf.Write(p)
}

// cappedStderr keeps the first limit bytes of git's stderr. It reports
// every write as complete, because a failed write stops the copy, and git
// then blocks on a full stderr pipe.
type cappedStderr struct {
	buf   bytes.Buffer
	limit int
}

// Write keeps what fits under the limit and discards the rest.
func (w *cappedStderr) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(room, len(p))])
	}

	return len(p), nil
}
