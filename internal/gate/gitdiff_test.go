package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/history"
)

// runGit runs git in dir with a fixed identity and returns its stdout.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.Output()
	require.NoError(t, err, "git %v", args)

	return string(out)
}

// gitDiffRepo builds a repository with one committed file under sub/ and
// an unstaged change to it. Only repository config can then change the
// diff, because the test turns the global and system config off.
func gitDiffRepo(t *testing.T) string {
	t.Helper()

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()
	file := filepath.Join(root, "sub", "f.txt")

	runGit(t, root, "init", "-q", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("a\n\nb\n"), 0o644))
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	require.NoError(t, os.WriteFile(file, []byte("a\n\nc\n"), 0o644))

	return root
}

func TestGitDiffHeadIgnoresDiffConfig(t *testing.T) {
	// Each case sets a config that changes the text of a plain
	// git diff HEAD. GitDiffHead must still give the text of a
	// repository with no config.
	cases := []struct {
		name   string
		config [][2]string
		attrs  string // the content of .git/info/attributes
		env    [2]string
	}{
		{name: "color.ui always", config: [][2]string{{"color.ui", "always"}}},
		{name: "color.diff always", config: [][2]string{{"color.diff", "always"}}},
		{name: "diff.noprefix", config: [][2]string{{"diff.noprefix", "true"}}},
		{name: "diff.mnemonicPrefix", config: [][2]string{{"diff.mnemonicPrefix", "true"}}},
		{name: "diff.external", config: [][2]string{{"diff.external", "true"}}},
		{name: "GIT_EXTERNAL_DIFF", env: [2]string{"GIT_EXTERNAL_DIFF", "true"}},
		{
			name:   "textconv",
			config: [][2]string{{"diff.hide.textconv", "true"}},
			attrs:  "*.txt diff=hide\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := gitDiffRepo(t)

			want, err := GitDiffHead(root)
			require.NoError(t, err)
			require.Contains(t, want, "diff --git a/sub/f.txt b/sub/f.txt\n")

			for _, kv := range c.config {
				runGit(t, root, "config", kv[0], kv[1])
			}

			if c.attrs != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte(c.attrs), 0o644))
			}

			if c.env[0] != "" {
				t.Setenv(c.env[0], c.env[1])
			}

			// The case is real only when it changes a plain git diff HEAD.
			assert.NotEqual(t, want, runGit(t, root, "diff", "HEAD"), "plain git diff HEAD")

			got, err := GitDiffHead(root)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestGitDiffHeadIgnoresDiffRelative(t *testing.T) {
	// diff.relative applies when git runs in a subdirectory. Without the
	// config, the paths start at the top of the work tree.
	root := gitDiffRepo(t)
	sub := filepath.Join(root, "sub")

	want, err := GitDiffHead(sub)
	require.NoError(t, err)
	require.Contains(t, want, "diff --git a/sub/f.txt b/sub/f.txt\n")

	runGit(t, root, "config", "diff.relative", "true")
	assert.NotEqual(t, want, runGit(t, sub, "diff", "HEAD"), "plain git diff HEAD")

	got, err := GitDiffHead(sub)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestGitDiffHeadIgnoresDiffSubmodule(t *testing.T) {
	// diff.submodule=log writes a submodule change as a log summary with
	// no diff --git header, so the parser would find no changed file.
	root := gitDiffRepo(t)
	lib := t.TempDir()

	runGit(t, lib, "init", "-q", "-b", "main")
	runGit(t, lib, "commit", "-q", "--allow-empty", "-m", "first")

	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "lib")
	runGit(t, root, "commit", "-q", "-m", "add lib")
	runGit(t, filepath.Join(root, "lib"), "commit", "-q", "--allow-empty", "-m", "second")

	want, err := GitDiffHead(root)
	require.NoError(t, err)
	require.Contains(t, want, "diff --git a/lib b/lib\n")

	runGit(t, root, "config", "diff.submodule", "log")
	assert.NotContains(t, runGit(t, root, "diff", "HEAD"), "diff --git a/lib b/lib\n", "plain git diff HEAD")

	got, err := GitDiffHead(root)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// numberedLines returns n numbered lines, so git can match a rename or a
// copy of the file by its content.
func numberedLines(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}

	return b.String()
}

func TestGitDiffHeadIgnoresRenameConfig(t *testing.T) {
	// diff.renames=false writes a rename as a deletion and an addition of
	// every line. diff.renames=copies writes a near-copy as one record with
	// the differing lines only. Both change the files and lines that check
	// reads, so GitDiffHead must keep git's default rename detection.
	body := numberedLines(20)

	rename := func(t *testing.T, root string) {
		runGit(t, root, "mv", "sub/r.txt", "sub/s.txt")
	}

	nearCopy := func(t *testing.T, root string) {
		edited := strings.Replace(body, "line 3\n", "line three\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "r.txt"), []byte(edited), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "copy.txt"), []byte(edited+"extra\n"), 0o644))
		runGit(t, root, "add", "-A")
	}

	// Each setting changes only one shape. diff.renames=copies writes a
	// pure rename as the default does, and diff.renames=false leaves a copy
	// as it is, because the default finds no copies.
	cases := []struct {
		name  string
		value string
		setup func(t *testing.T, root string)
	}{
		{name: "rename", value: "false", setup: rename},
		{name: "near-copy", value: "copies", setup: nearCopy},
	}

	for _, c := range cases {
		t.Run(c.name+" diff.renames="+c.value, func(t *testing.T) {
			root := gitDiffRepo(t)

			require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "r.txt"), []byte(body), 0o644))
			runGit(t, root, "add", "-A")
			runGit(t, root, "commit", "-q", "-m", "add r")
			c.setup(t, root)

			want, err := GitDiffHead(root)
			require.NoError(t, err)

			plain := runGit(t, root, "diff", "HEAD")
			require.Equal(t, want, plain, "the default config gives the plain text")

			runGit(t, root, "config", "diff.renames", c.value)

			// The case is real only when it changes a plain git diff HEAD.
			assert.NotEqual(t, plain, runGit(t, root, "diff", "HEAD"), "plain git diff HEAD")

			got, err := GitDiffHead(root)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// clearGitIndexEnv removes the inherited variables that already stop git
// from writing .git/index, so a plain git command can show the write.
// t.Setenv restores them after the test.
func clearGitIndexEnv(t *testing.T) {
	t.Helper()

	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_OPTIONAL_LOCKS" || strings.HasPrefix(k, "GIT_CONFIG_") {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}
}

func TestGitDiffHeadKeepsGitIndex(t *testing.T) {
	// A touch changes no content. The check fallback must read the diff
	// without a rewrite of .git/index, because the rewrite takes
	// .git/index.lock and a concurrent git command of the user then fails.
	clearGitIndexEnv(t)

	root := gitDiffRepo(t)
	other := filepath.Join(root, "sub", "g.txt")
	gitIndex := filepath.Join(root, ".git", "index")

	require.NoError(t, os.WriteFile(other, []byte("g\n"), 0o644))
	runGit(t, root, "add", "sub/g.txt")
	runGit(t, root, "commit", "-q", "-m", "add g")
	runGit(t, root, "status", "--porcelain")

	at := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(other, at, at))

	beforeInfo, err := os.Stat(gitIndex)
	require.NoError(t, err)

	beforeBytes, err := os.ReadFile(gitIndex)
	require.NoError(t, err)

	got, err := GitDiffHead(root)
	require.NoError(t, err)

	afterInfo, err := os.Stat(gitIndex)
	require.NoError(t, err)

	afterBytes, err := os.ReadFile(gitIndex)
	require.NoError(t, err)

	assert.True(t, os.SameFile(beforeInfo, afterInfo), ".git/index has a new inode")
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), ".git/index has a new mtime")
	assert.Equal(t, beforeBytes, afterBytes, ".git/index has new bytes")

	// The plain command gives the same text, and it writes the index, so
	// the fixture can show the write that GitDiffHead avoids.
	assert.Equal(t, runGit(t, root, "diff", "HEAD"), got)

	plainBytes, err := os.ReadFile(gitIndex)
	require.NoError(t, err)
	assert.NotEqual(t, beforeBytes, plainBytes, "a plain git diff HEAD must write the index")
}

// diffShim puts a git first on PATH that fails each diff call with exit 3
// and the given stderr, and passes every other call to the real git.
func diffShim(t *testing.T, stderr string) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = diff ]; then printf '" + stderr + "' >&2; exit 3; fi\n" +
		"done\n" +
		"exec " + strconv.Quote(realGit) + " \"$@\"\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGitDiffHeadFailureReasons(t *testing.T) {
	// A failed git diff HEAD must say why in one safe line. The two common
	// causes get a fixed reason, because git's text names --no-index or a
	// raw revision error that a check caller cannot act on.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	t.Run("outside git", func(t *testing.T) {
		parent := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", parent)

		dir := filepath.Join(parent, "plain")
		require.NoError(t, os.Mkdir(dir, 0o755))

		_, err := GitDiffHead(dir)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and "+dir+" is not inside a git work tree, so git diff HEAD cannot run", err.Error())
	})

	t.Run("no commit", func(t *testing.T) {
		root := t.TempDir()
		runGit(t, root, "init", "-q", "-b", "main")

		_, err := GitDiffHead(root)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and the repository at "+root+
			" has no commit yet, so git diff HEAD has nothing to compare", err.Error())
	})

	t.Run("other failure keeps the first stderr line, sanitized", func(t *testing.T) {
		root := gitDiffRepo(t)
		diffShim(t, `\n\033[31mfatal: boom\033[0m\nsecond line\n`)

		_, err := GitDiffHead(root)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and git diff HEAD failed (exit status 3): [31mfatal: boom[0m", err.Error())
		assert.NotContains(t, err.Error(), "\x1b")
		assert.NotContains(t, err.Error(), "second line")
	})

	t.Run("a long stderr line is cut", func(t *testing.T) {
		root := gitDiffRepo(t)
		diffShim(t, "fatal: "+strings.Repeat("x", 300)+`\n`)

		_, err := GitDiffHead(root)
		require.Error(t, err)

		prefix := "no diff given, and git diff HEAD failed (exit status 3): "
		assert.Equal(t, prefix+"fatal: "+strings.Repeat("x", maxGitReasonBytes-len("fatal: ")), err.Error())
	})

	t.Run("other failure without stderr", func(t *testing.T) {
		root := gitDiffRepo(t)
		diffShim(t, "")

		_, err := GitDiffHead(root)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and git diff HEAD failed (exit status 3)", err.Error())
	})

	t.Run("git does not start", func(t *testing.T) {
		root := gitDiffRepo(t)
		t.Setenv("PATH", t.TempDir())

		_, err := GitDiffHead(root)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "no diff given, and git diff HEAD did not run: "), err.Error())
	})

	// A folder name can hold an escape sequence and a line break. A reason
	// shows such a name in its clean form on one line, because a terminal
	// can act on the control bytes.
	const rawName, cleanName = "\x1b[31mred\nline", "[31mred line"

	t.Run("the root is sanitized", func(t *testing.T) {
		parent := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", parent)

		plain := filepath.Join(parent, "plain"+rawName)
		require.NoError(t, os.Mkdir(plain, 0o755))

		_, err := GitDiffHead(plain)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and "+filepath.Join(parent, "plain"+cleanName)+
			" is not inside a git work tree, so git diff HEAD cannot run", err.Error())

		unborn := filepath.Join(parent, "unborn"+rawName)
		require.NoError(t, os.Mkdir(unborn, 0o755))
		runGit(t, unborn, "init", "-q", "-b", "main")

		_, err = GitDiffHead(unborn)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and the repository at "+filepath.Join(parent, "unborn"+cleanName)+
			" has no commit yet, so git diff HEAD has nothing to compare", err.Error())
	})

	t.Run("git does not start in a missing root", func(t *testing.T) {
		// An MCP server keeps its root after a user removes the folder. The
		// start error of Go then names the root, so the reason must show
		// the clean form of that name too.
		parent := t.TempDir()
		gone := filepath.Join(parent, "gone"+rawName)

		_, err := GitDiffHead(gone)
		require.Error(t, err)
		assert.Equal(t, "no diff given, and git diff HEAD did not run: chdir "+
			filepath.Join(parent, "gone"+cleanName)+": no such file or directory", err.Error())
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
		"printf '%s\\n' \"$*\" >> " + strconv.Quote(calls) + "\n" +
		"exec " + strconv.Quote(realGit) + " \"$@\"\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return calls
}

func TestGitDiffHeadRunsOneGitProcess(t *testing.T) {
	// The probes for a failure reason run only after git diff HEAD fails.
	// A successful read starts one git process, so the reason costs a
	// check nothing on the success path.
	root := gitDiffRepo(t)
	calls := gitCallLog(t)

	got, err := GitDiffHead(root)
	require.NoError(t, err)
	require.Contains(t, got, "diff --git a/sub/f.txt b/sub/f.txt\n")

	logged, err := os.ReadFile(calls)
	require.NoError(t, err)
	assert.Equal(t, strings.Join(history.ParsedGitArgs("diff", "HEAD"), " ")+"\n", string(logged))
}
