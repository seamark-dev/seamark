package history

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testMaxStdout is a stdout bound that every small test command fits in.
const testMaxStdout = 1 << 20

// isolateGitConfig hides the global and system git configuration from
// every git process of the test. RunGit passes os.Environ() to git, so
// without this a user setting such as core.fsmonitor can change what the
// test observes. The fixed hash keeps the SHA-1 blob ids of the tests.
func isolateGitConfig(t *testing.T) {
	t.Helper()

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
}

// endlessReader returns line again and again and never reaches EOF.
type endlessReader struct {
	line []byte
	off  int
}

func (r *endlessReader) Read(p []byte) (int, error) {
	n := 0

	for n < len(p) {
		c := copy(p[n:], r.line[r.off:])
		n += c
		r.off = (r.off + c) % len(r.line)
	}

	return n, nil
}

func TestRunGitReturnsRawBytesAndExitCode(t *testing.T) {
	isolateGitConfig(t)

	ctx := context.Background()
	root, commit := gitRepo(t)

	// The blob holds CR, NUL, and no final newline: gitScan loses the
	// first two and splits on the third.
	raw := []byte("line one\r\nnul\x00inside\r\nno final newline")
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw.bin"), raw, 0o644))
	commit("raw bytes")

	t.Run("a blob comes back byte for byte", func(t *testing.T) {
		out, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"cat-file", "blob", "HEAD:raw.bin"}, MaxStdout: testMaxStdout})
		require.NoError(t, err)

		assert.Equal(t, raw, out.Stdout)
		assert.Equal(t, 0, out.ExitCode)
		assert.Empty(t, out.Stderr)
	})

	t.Run("NUL records stay in one unsplit output", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "new file.txt"), []byte("x\n"), 0o644))
		t.Cleanup(func() { _ = os.Remove(filepath.Join(root, "new file.txt")) })

		out, err := RunGit(ctx, GitCall{
			Dir:       root,
			Args:      []string{"-c", "core.quotePath=off", "status", "--porcelain=v2", "-z", "--untracked-files=all"},
			MaxStdout: testMaxStdout,
		})
		require.NoError(t, err)

		assert.Equal(t, "? new file.txt\x00", string(out.Stdout))
		assert.Equal(t, 0, out.ExitCode)
	})

	t.Run("stdin reaches git", func(t *testing.T) {
		out, err := RunGit(ctx, GitCall{
			Dir:       root,
			Args:      []string{"hash-object", "--stdin"},
			Stdin:     strings.NewReader("hello\n"),
			MaxStdout: testMaxStdout,
		})
		require.NoError(t, err)

		assert.Equal(t, "ce013625030ba8dba906f756967f9e9ca394464a\n", string(out.Stdout), "the SHA-1 blob id of \"hello\\n\"")
	})

	t.Run("exit 1 is a result, not an error", func(t *testing.T) {
		unborn, _ := gitRepo(t)

		out, err := RunGit(ctx, GitCall{Dir: unborn, Args: []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}, MaxStdout: testMaxStdout})
		require.NoError(t, err, "the K4 snapshot reads exit 1 as an unborn HEAD")

		assert.Equal(t, 1, out.ExitCode)
		assert.Empty(t, out.Stdout)
	})

	t.Run("exit 128 keeps the git message", func(t *testing.T) {
		out, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"cat-file", "-e", "HEAD:nope"}, MaxStdout: testMaxStdout})
		require.NoError(t, err)

		assert.Equal(t, 128, out.ExitCode)
		assert.Contains(t, string(out.Stderr), "nope")
	})

	t.Run("stderr keeps only its first part", func(t *testing.T) {
		// Each unmatched pathspec gives one error line, so 2000 paths give
		// about 250 KiB of stderr, far above the cap.
		args := []string{"ls-files", "--error-unmatch", "--"}
		for i := range 2000 {
			args = append(args, fmt.Sprintf("missing-path-%060d", i))
		}

		out, err := RunGit(ctx, GitCall{Dir: root, Args: args, MaxStdout: testMaxStdout})
		require.NoError(t, err, "a long stderr is no reason to fail")

		assert.Equal(t, 1, out.ExitCode)
		assert.Len(t, out.Stderr, maxGitStderrBytes)
		assert.True(t, bytes.HasPrefix(out.Stderr, []byte("error: pathspec 'missing-path-")), "the cap keeps the start")
	})

	t.Run("a missing directory is an error", func(t *testing.T) {
		_, err := RunGit(ctx, GitCall{Dir: filepath.Join(root, "missing"), Args: []string{"status"}, MaxStdout: testMaxStdout})
		require.Error(t, err, "git did not run")

		assert.NotErrorIs(t, err, ErrGitOutputLimit)
	})
}

func TestRunGitOutputLimit(t *testing.T) {
	isolateGitConfig(t)

	ctx := context.Background()
	root, commit := gitRepo(t)

	big := bytes.Repeat([]byte("0123456789abcdef"), 16<<10) // 256 KiB
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644))
	commit("big blob")

	catBig := []string{"cat-file", "blob", "HEAD:big.bin"}

	t.Run("output above the limit gives the error and no stdout", func(t *testing.T) {
		out, err := RunGit(ctx, GitCall{Dir: root, Args: catBig, MaxStdout: 1024})
		require.ErrorIs(t, err, ErrGitOutputLimit)

		assert.Empty(t, out.Stdout, "a truncated output must never become a result")
	})

	t.Run("output equal to the limit passes", func(t *testing.T) {
		out, err := RunGit(ctx, GitCall{Dir: root, Args: catBig, MaxStdout: int64(len(big))})
		require.NoError(t, err)

		assert.Equal(t, big, out.Stdout)
	})

	t.Run("one byte above the limit fails", func(t *testing.T) {
		_, err := RunGit(ctx, GitCall{Dir: root, Args: catBig, MaxStdout: int64(len(big)) - 1})
		require.ErrorIs(t, err, ErrGitOutputLimit)
	})

	t.Run("a zero limit accepts no output", func(t *testing.T) {
		_, err := RunGit(ctx, GitCall{Dir: root, Args: catBig})
		require.ErrorIs(t, err, ErrGitOutputLimit, "a call without a bound must fail, not read without a bound")

		out, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"cat-file", "-e", "HEAD:big.bin"}})
		require.NoError(t, err, "a command without output fits a zero limit")

		assert.Equal(t, 0, out.ExitCode)
	})

	t.Run("endless output stops git", func(t *testing.T) {
		// cat-file --batch prints the blob again for each request line,
		// and the requests never end. Only the stop can end git.
		oid, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"rev-parse", "HEAD:big.bin"}, MaxStdout: testMaxStdout})
		require.NoError(t, err)

		start := time.Now()
		_, err = RunGit(ctx, GitCall{
			Dir:       root,
			Args:      []string{"cat-file", "--batch"},
			Stdin:     &endlessReader{line: oid.Stdout},
			MaxStdout: testMaxStdout,
		})

		require.ErrorIs(t, err, ErrGitOutputLimit)
		assert.Less(t, time.Since(start), 10*time.Second, "the limit stops git")
	})
}

func TestRunGitHonoursContext(t *testing.T) {
	isolateGitConfig(t)

	root, commit := gitRepo(t)
	commit("init", "a.txt")

	t.Run("an ended context runs no git", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"status"}, MaxStdout: testMaxStdout})
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("a deadline stops git that waits", func(t *testing.T) {
		// git waits for request lines on a pipe that never gets one. An
		// *os.File goes to git directly, so no copy holds the wait.
		pr, pw, err := os.Pipe()
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = pw.Close()
			_ = pr.Close()
		})

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err = RunGit(ctx, GitCall{Dir: root, Args: []string{"cat-file", "--batch"}, Stdin: pr, MaxStdout: testMaxStdout})

		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 5*time.Second, "the deadline ends the wait")
	})

	t.Run("a child of git that keeps the pipes open does not hold the return", func(t *testing.T) {
		// The alias runs sleep as a child of git. The stop kills git only,
		// so sleep keeps stdout and stderr open; gitWaitDelay bounds that.
		previous := gitWaitDelay
		gitWaitDelay = 200 * time.Millisecond

		t.Cleanup(func() { gitWaitDelay = previous })

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := RunGit(ctx, GitCall{Dir: root, Args: []string{"-c", "alias.hold=!sleep 20", "hold"}, MaxStdout: testMaxStdout})

		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 10*time.Second, "the wait bound ends the return, not the child")
	})
}

func TestRunGitTakesNoIndexLock(t *testing.T) {
	isolateGitConfig(t)

	root, commit := gitRepo(t)
	commit("init", "a.txt", "pkg/b.go")

	// An mtime that differs from the stat cache makes git re-read the
	// file. A plain `git status` then rewrites .git/index (inventory §4.2).
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, "a.txt"), past, past))

	indexPath := filepath.Join(root, ".git", "index")

	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	beforeInfo, err := os.Stat(indexPath)
	require.NoError(t, err)

	out, err := RunGit(context.Background(), GitCall{
		Dir:       root,
		Args:      []string{"-c", "core.quotePath=off", "status", "--porcelain=v2", "-z", "--untracked-files=all"},
		MaxStdout: testMaxStdout,
	})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode, string(out.Stderr))

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	afterInfo, err := os.Stat(indexPath)
	require.NoError(t, err)

	assert.Equal(t, before, after, "RunGit must leave .git/index as it was")
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime())
	assert.NoFileExists(t, filepath.Join(root, ".git", "index.lock"))

	// The control proves that the stat cache was stale: the same status
	// without GIT_OPTIONAL_LOCKS=0 rewrites the index.
	plain := exec.Command("git", "status", "--porcelain=v2", "-z")
	plain.Dir = root
	plain.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=1")

	plainOut, err := plain.CombinedOutput()
	require.NoError(t, err, string(plainOut))

	refreshed, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	assert.NotEqual(t, before, refreshed, "a plain status refreshes the stale stat cache, so the check above is not empty")
}

// TestRunGitDropsTraceVariables proves that RunGit hides the GIT_TRACE
// variables from git. A trace setting writes lines with a time to
// stderr. The K4 snapshot shows status stderr as a git-stderr notice, so
// a trace setting of the user's shell would add that notice to every
// lesson verification.
func TestRunGitDropsTraceVariables(t *testing.T) {
	isolateGitConfig(t)

	root, commit := gitRepo(t)
	commit("init", "a.txt")

	// The fixture runs git before the variables exist, so no trace line
	// mixes into the output of a fixture command.
	for _, name := range []string{"GIT_TRACE", "GIT_TRACE2", "GIT_TRACE2_PERF", "GIT_TRACE_SETUP", "GIT_TRACE_PERFORMANCE"} {
		t.Setenv(name, "1")
	}

	args := []string{"status", "--porcelain=v2", "-z"}

	out, err := RunGit(context.Background(), GitCall{Dir: root, Args: args, MaxStdout: testMaxStdout})
	require.NoError(t, err)

	// The message shows only the first line, because trace output runs
	// to many lines.
	first, _, _ := strings.Cut(string(out.Stderr), "\n")

	assert.Equal(t, 0, out.ExitCode)
	assert.Zero(t, len(out.Stderr), "RunGit must not pass a trace setting to git; first stderr line %q", first)

	// The control proves that the variables make this git write trace
	// lines, so the check above is not empty.
	plain := exec.Command("git", args...)
	plain.Dir = root

	var stderr bytes.Buffer

	plain.Stderr = &stderr

	require.NoError(t, plain.Run())
	assert.Contains(t, stderr.String(), "trace", "the same status without RunGit writes trace lines")
}

// TestRunGitSetsTheCLocale proves that RunGit runs git in the C locale
// (ruling D2). A git with gettext translates its messages in another
// locale. The K4 snapshot shows the first status stderr line as a
// notice, so the same tree must give the same text for every user.
func TestRunGitSetsTheCLocale(t *testing.T) {
	isolateGitConfig(t)

	root, commit := gitRepo(t)
	commit("init", "a.txt")

	// The fixture runs git before the variables exist, so the fixture
	// commands keep the locale of the test process.
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	// The alias runs env as a child of git, so its output is the
	// environment that git got from RunGit.
	out, err := RunGit(context.Background(), GitCall{
		Dir:       root,
		Args:      []string{"-c", "alias.showenv=!env", "showenv"},
		MaxStdout: testMaxStdout,
	})
	require.NoError(t, err)
	require.Equal(t, 0, out.ExitCode, string(out.Stderr))

	var locale []string

	for _, line := range strings.Split(string(out.Stdout), "\n") {
		if strings.HasPrefix(line, "LC_ALL=") || strings.HasPrefix(line, "LANGUAGE=") {
			locale = append(locale, line)
		}
	}

	assert.Equal(t, []string{"LC_ALL=C"}, locale, "RunGit must set LC_ALL=C and drop LANGUAGE")
}
