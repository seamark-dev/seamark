package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/report"
)

// stdinPipes are the two kinds of pipe that check can get as stdin. An
// agent harness gives an *os.File pipe. A caller of the command API can
// give any io.Reader, and an io.Pipe is one that blocks.
var stdinPipes = []struct {
	name string
	open func(t *testing.T) (io.Reader, io.WriteCloser)
}{
	{name: "os pipe", open: func(t *testing.T) (io.Reader, io.WriteCloser) {
		t.Helper()

		r, w, err := os.Pipe()
		require.NoError(t, err)

		// The cleanup also ends a read that a failed test leaves open.
		t.Cleanup(func() {
			_ = w.Close()
			_ = r.Close()
		})

		return r, w
	}},
	{name: "io pipe", open: func(t *testing.T) (io.Reader, io.WriteCloser) {
		t.Helper()

		r, w := io.Pipe()

		t.Cleanup(func() {
			_ = w.Close()
			_ = r.Close()
		})

		return r, w
	}},
}

// readDiffResult is what one readDiff call returns, with its stderr.
type readDiffResult struct {
	diff, input, stderr string
	err                 error
}

// readDiffWithin runs readDiff and fails the test when readDiff does not
// return in 5 s. A hang then fails one test and does not stop the suite.
func readDiffWithin(t *testing.T, stdin io.Reader, root string, wait time.Duration, enforced bool) readDiffResult {
	t.Helper()

	done := make(chan readDiffResult, 1)

	go func() {
		var errOut bytes.Buffer

		diff, input, err := readDiff(stdin, &errOut, root, wait, enforced)
		done <- readDiffResult{diff: diff, input: input, stderr: errOut.String(), err: err}
	}()

	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		require.FailNow(t, "readDiff did not return within 5s")

		return readDiffResult{}
	}
}

// checkLegacyHeadDiff returns the first part of checkLegacyDiff. That
// part is what `git diff HEAD` gives in checkLegacyFixture, because the
// untracked store/doc.go is not in it.
func checkLegacyHeadDiff(t *testing.T) string {
	t.Helper()

	head, _, found := strings.Cut(checkLegacyDiff, "diff --git a/store/doc.go")
	require.True(t, found)

	return head
}

// TestCheckIdleStdinFallsBackToGitDiffHead covers an idle stdin: an
// agent harness can give check a stdin pipe that stays open and sends
// nothing. In warn mode check then uses `git diff HEAD` after the wait
// and names that fallback on stderr. The goroutine that waits for the
// first byte ends when the pipe closes.
func TestCheckIdleStdinFallsBackToGitDiffHead(t *testing.T) {
	// The production wait prints as "1s" in the stderr line.
	// TestCheckIdleStdinProcess checks that check passes this wait.
	assert.Equal(t, "1s", stdinIdleWait.String())

	root := checkLegacyFixture(t)
	wantHead := checkLegacyHeadDiff(t)

	for _, pipe := range stdinPipes {
		t.Run(pipe.name, func(t *testing.T) {
			stdin, writer := pipe.open(t)
			before := runtime.NumGoroutine()

			got := readDiffWithin(t, stdin, root, 20*time.Millisecond, false)

			require.NoError(t, got.err)
			assert.Equal(t, wantHead, got.diff)
			assert.Equal(t, report.PolicyInputGitDiffHead, got.input)
			assert.Equal(t, "seamark: stdin idle after 20ms; checking git diff HEAD\n", got.stderr)

			// The closed pipe ends the read of the waiting goroutine, so no
			// goroutine of readDiff stays after it. The loop polls without
			// assert.Eventually, because Eventually adds a goroutine to the
			// count.
			require.NoError(t, writer.Close())

			for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > before && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}

			assert.LessOrEqual(t, runtime.NumGoroutine(), before, "the goroutine of the first read must end")
		})
	}
}

// TestCheckIdleStdinBlocksUnderEnforcement covers enforcement: under
// enforcement an idle stdin blocks and check reads no `git diff HEAD`.
// A diff producer that is slow to start would otherwise get the verdict
// of `git diff HEAD`, and that verdict can allow what the diff blocks.
func TestCheckIdleStdinBlocksUnderEnforcement(t *testing.T) {
	root := checkLegacyFixture(t)

	for _, pipe := range stdinPipes {
		t.Run(pipe.name, func(t *testing.T) {
			stdin, _ := pipe.open(t)

			got := readDiffWithin(t, stdin, root, 20*time.Millisecond, true)

			require.ErrorIs(t, got.err, gate.ErrBlocked)
			assert.Equal(t, "blocked by policy: stdin idle after 20ms; "+
				"enforce mode does not fall back to git diff HEAD (redirect stdin from /dev/null to check it)", got.err.Error())
			assert.Empty(t, got.diff)
			assert.Empty(t, got.input)
			assert.Empty(t, got.stderr, "the fallback notice must not appear when check does not fall back")
		})
	}
}

// TestCheckEmptyStdinFallsBackQuietly covers an empty stdin: a stdin
// that ends before its first byte is empty, not idle. check then uses
// `git diff HEAD` and prints no notice, in both modes. A hook or a
// script that closes stdin, or pipes no bytes, depends on this.
func TestCheckEmptyStdinFallsBackQuietly(t *testing.T) {
	root := checkLegacyFixture(t)
	wantHead := checkLegacyHeadDiff(t)

	empties := []struct {
		name string
		open func(t *testing.T) io.Reader
	}{
		{name: "empty reader", open: func(*testing.T) io.Reader { return strings.NewReader("") }},
	}

	for _, pipe := range stdinPipes {
		empties = append(empties, struct {
			name string
			open func(t *testing.T) io.Reader
		}{name: "closed " + pipe.name, open: func(t *testing.T) io.Reader {
			t.Helper()

			stdin, writer := pipe.open(t)
			require.NoError(t, writer.Close())

			return stdin
		}})
	}

	for _, empty := range empties {
		for _, enforced := range []bool{false, true} {
			name := empty.name + " warn"
			if enforced {
				name = empty.name + " enforce"
			}

			t.Run(name, func(t *testing.T) {
				// The long wait costs no time: EOF ends the first read.
				got := readDiffWithin(t, empty.open(t), root, time.Minute, enforced)

				require.NoError(t, got.err)
				assert.Equal(t, wantHead, got.diff)
				assert.Equal(t, report.PolicyInputGitDiffHead, got.input)
				assert.Empty(t, got.stderr)
			})
		}
	}
}

// TestCheckReadsADiffThatStartsWithinTheWait covers a late writer: a
// writer that starts late, but within the wait, gives the whole diff.
// The first byte arrives alone, so a lost first read shows as a missing
// "d".
func TestCheckReadsADiffThatStartsWithinTheWait(t *testing.T) {
	root := checkLegacyFixture(t)

	for _, pipe := range stdinPipes {
		t.Run(pipe.name, func(t *testing.T) {
			stdin, writer := pipe.open(t)

			go func() {
				// The writer errors go nowhere. A failed write shows as a
				// diff that is not equal to checkLegacyDiff.
				time.Sleep(20 * time.Millisecond)

				_, _ = io.WriteString(writer, checkLegacyDiff[:1])

				time.Sleep(20 * time.Millisecond)

				_, _ = io.WriteString(writer, checkLegacyDiff[1:])
				_ = writer.Close()
			}()

			// The long wait costs no time: the read ends when the writer
			// closes the pipe. Enforcement does not change a diff that
			// arrives.
			got := readDiffWithin(t, stdin, root, time.Minute, true)

			require.NoError(t, got.err)
			assert.Equal(t, checkLegacyDiff, got.diff)
			assert.Equal(t, report.PolicyInputStdinDiff, got.input)
			assert.Empty(t, got.stderr)
		})
	}
}

// TestCheckFirstReadReadyAtTheDeadlineWins covers a tie: when the first
// read and the deadline are both ready, the diff wins. One select picks
// at random between ready channels. 100 rounds then show such a loss
// with a chance of 1 - 2^-100.
func TestCheckFirstReadReadyAtTheDeadlineWins(t *testing.T) {
	deadline := make(chan time.Time)
	close(deadline)

	for range 100 {
		first := make(chan firstRead, 1)
		first <- firstRead{data: []byte("d")}

		got, ok := awaitFirstRead(first, deadline)

		require.True(t, ok, "a first read that is ready at the deadline is not idle")
		require.Equal(t, []byte("d"), got.data)
	}
}

// seamarkProcessIdleStdin runs this test binary as `seamark <args>` in
// dir. Its fd 0 is the read end of an os.Pipe, and the test keeps the
// write end open and sends nothing, as an agent harness does. The real
// fd 0, Execute and stdinIdleWait then run, which no in-process run
// covers. The child must exit within 10 s, so a hang fails one test.
func seamarkProcessIdleStdin(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	encoded, err := json.Marshal(args)
	require.NoError(t, err)

	r, w, err := os.Pipe()
	require.NoError(t, err)

	// The write end stays open until the child ends, so the child gets
	// no EOF. Closed after the run, it does not hold the pipe.
	defer func() {
		_ = w.Close()
		_ = r.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer

	// TestLessonsVerifyExitCodes runs Execute when it finds
	// verifyProcessEnv, as seamarkProcess uses it.
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLessonsVerifyExitCodes$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), verifyProcessEnv+"="+string(encoded))
	cmd.Stdin = r // an *os.File becomes fd 0 of the child, with no copy
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = 2 * time.Second

	err = cmd.Run()

	require.NoError(t, ctx.Err(), "seamark did not exit within 10s; stderr:\n%s", errOut.String())

	var exitErr *exec.ExitError

	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		require.NoError(t, err)
	}

	return out.String(), errOut.String(), code
}

// TestCheckIdleStdinProcess covers an idle stdin through a real seamark
// process with an idle pipe as fd 0 and the production wait. In warn
// mode check falls back to `git diff HEAD` after 1 s. Under
// enforcement, from the flag or from the policy, the idle stdin blocks
// with exit 2 and check prints no verdict.
func TestCheckIdleStdinProcess(t *testing.T) {
	const (
		notice  = "seamark: stdin idle after 1s; checking git diff HEAD"
		blocked = "seamark: blocked by policy: stdin idle after 1s; " +
			"enforce mode does not fall back to git diff HEAD (redirect stdin from /dev/null to check it)"
	)

	cases := []struct {
		name    string
		policy  string // "" keeps the warn policy of checkLegacyFixture
		args    []string
		code    int
		lines   []string
		verdict bool // stdout is the verdict of `git diff HEAD`
	}{
		{name: "warn", args: []string{"check"}, code: 0, lines: []string{notice}, verdict: true},
		{name: "enforce flag", args: []string{"check", "--enforce"}, code: 2, lines: []string{blocked}},
		{
			name: "enforce policy",
			policy: "mode: enforce\nrequire_approval:\n  - id: any-change\n    when: 'size(diff.files) > 0'\n" +
				"    message: the change touches files\n",
			args:  []string{"check"},
			code:  2,
			lines: []string{blocked},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := checkLegacyFixture(t)

			if c.policy != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(c.policy), 0o644))
			}

			stdout, stderr, code := seamarkProcessIdleStdin(t, root, c.args...)

			// The index refresh line depends on the run order, so it is
			// not part of the comparison.
			lines := slices.DeleteFunc(seamarkLines(stderr), func(line string) bool {
				return line == "seamark: index refreshed"
			})

			assert.Equal(t, c.code, code, "stderr:\n%s", stderr)
			assert.Equal(t, c.lines, lines, "stderr:\n%s", stderr)

			if !c.verdict {
				assert.Empty(t, stdout)

				return
			}

			// TestCheckLessonsAbsentKeepsPlainJSON freezes this output of
			// an in-process check with an empty stdin.
			want, _, err := runIn(t, "", "-C", root, "check")
			require.NoError(t, err)
			assert.Equal(t, want, stdout)
		})
	}
}
