package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The tests of a check that reads git diff HEAD because it gets no diff.
// Where git diff HEAD cannot run, in a folder outside git or in a
// repository with no commit, check must say why, name a route that runs,
// and write nothing.

// checkOnDevNull runs check in root with stdin opened from /dev/null, as
// `seamark check </dev/null` does.
func checkOnDevNull(t *testing.T, root string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)

	defer func() { _ = devNull.Close() }()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(devNull)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"-C", root, "check"}, args...))

	err = cmd.Execute()

	return out.String(), errOut.String(), err
}

// runCheckOnDevNull runs a check that must fail on /dev/null. It returns
// the error and stderr.
func runCheckOnDevNull(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()

	stdout, stderr, err := checkOnDevNull(t, root, args...)
	assert.Empty(t, stdout, "a failed check prints no verdict")

	return stderr, err
}

// assertBrokenCheck asserts the exit code 1 of Execute: a git failure is
// a broken check, not a policy block or a lesson violation.
func assertBrokenCheck(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.False(t, errors.Is(err, gate.ErrBlocked), "a git failure is no policy block")
	assert.False(t, errors.Is(err, lessoncheck.ErrViolation), "a git failure is no lesson violation")
}

func TestCheckWithoutGitDiffHead(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	t.Run("outside git", func(t *testing.T) {
		for _, args := range [][]string{nil, {"--enforce"}} {
			parent := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", parent)

			dir := filepath.Join(parent, "plain")
			require.NoError(t, os.Mkdir(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644))

			stderr, err := runCheckOnDevNull(t, dir, args...)
			assertBrokenCheck(t, err)
			assert.Equal(t, "no diff given, and "+dir+" is not inside a git work tree, so git diff HEAD cannot run"+
				"; pipe a unified diff to check on stdin", err.Error(), "%v", args)
			assert.Empty(t, stderr, "%v: no index refresh line", args)
			assert.NoDirExists(t, filepath.Join(dir, ".seamark"), "%v: the refusal writes nothing", args)
		}
	})

	t.Run("no commit", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644))

		gitInit := exec.Command("git", "init", "-q", "-b", "main")
		gitInit.Dir = root
		out, err := gitInit.CombinedOutput()
		require.NoError(t, err, "%s", out)

		// check works in the git top level, which names the real path.
		top, err := filepath.EvalSymlinks(root)
		require.NoError(t, err)

		stderr, err := runCheckOnDevNull(t, root)
		assertBrokenCheck(t, err)
		assert.Equal(t, "no diff given, and the repository at "+top+
			" has no commit yet, so git diff HEAD has nothing to compare"+
			"; pipe a unified diff to check on stdin", err.Error())
		assert.Empty(t, stderr)
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	})

	t.Run("an existing index keeps its bytes", func(t *testing.T) {
		parent := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", parent)

		dir := filepath.Join(parent, "plain")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))

		_, err := run(t, "-C", dir, "index")
		require.NoError(t, err)

		db := filepath.Join(dir, ".seamark", "index.db")

		beforeInfo, err := os.Stat(db)
		require.NoError(t, err)

		beforeBytes, err := os.ReadFile(db)
		require.NoError(t, err)

		// A new file would make the index stale, so a refresh before the
		// git read would rewrite index.db.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n\nfunc b() {}\n"), 0o644))

		_, err = runCheckOnDevNull(t, dir)
		assertBrokenCheck(t, err)

		afterInfo, err := os.Stat(db)
		require.NoError(t, err)

		afterBytes, err := os.ReadFile(db)
		require.NoError(t, err)

		assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "index.db has a new mtime")
		assert.Equal(t, beforeBytes, afterBytes, "index.db has new bytes")
		assert.NoFileExists(t, filepath.Join(dir, ".seamark", "audit.jsonl"), "the refusal writes no audit entry")
	})

	t.Run("an empty or idle pipe names the same route", func(t *testing.T) {
		// An empty pipe, and an idle pipe in warn mode, fall back to git
		// diff HEAD after the index refresh. That fallback must add the CLI
		// route too, so each stdin gets the same reason.
		parent := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", parent)

		dir := filepath.Join(parent, "plain")
		require.NoError(t, os.Mkdir(dir, 0o755))

		want := "no diff given, and " + dir + " is not inside a git work tree, so git diff HEAD cannot run" +
			"; pipe a unified diff to check on stdin"

		// The long wait costs no time: EOF ends the first read.
		got := readDiffWithin(t, strings.NewReader(""), dir, time.Minute, false)
		require.Error(t, got.err)
		assert.Equal(t, want, got.err.Error(), "empty pipe")
		assert.Empty(t, got.stderr, "empty pipe")

		for _, pipe := range stdinPipes {
			stdin, _ := pipe.open(t)

			got := readDiffWithin(t, stdin, dir, 20*time.Millisecond, false)
			require.Error(t, got.err, "idle %s", pipe.name)
			assert.Equal(t, want, got.err.Error(), "idle %s", pipe.name)
			assert.Equal(t, "seamark: stdin idle after 20ms; checking git diff HEAD\n", got.stderr, "idle %s", pipe.name)
		}
	})
}

// gitCallLog puts a git first on PATH that writes the arguments of each
// call as one line to a log file and then runs the real git. It returns
// the path of the log file.
func gitCallLog(t *testing.T) string {
	t.Helper()

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(calls) + "\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return calls
}

func TestCheckReadsGitDiffHeadOnce(t *testing.T) {
	// With /dev/null on stdin, check reads git diff HEAD before the index
	// refresh. readDiff must not read it again, because a second read
	// starts one more git process for the same text.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := checkLegacyFixture(t)
	calls := gitCallLog(t)

	stdout, stderr, err := checkOnDevNull(t, root)
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "verdict  require_approval (mode: warn)")

	logged, err := os.ReadFile(calls)
	require.NoError(t, err)

	// The freshness check of the index also runs git diff HEAD, with a
	// pathspec after HEAD. Only the exact GitDiffHead form counts here.
	diffHead := strings.Join(history.ParsedGitArgs("diff", "HEAD"), " ")
	reads := 0

	for line := range strings.SplitSeq(string(logged), "\n") {
		if line == diffHead {
			reads++
		}
	}

	assert.Equal(t, 1, reads, "git diff HEAD reads")
}
