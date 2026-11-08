package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
