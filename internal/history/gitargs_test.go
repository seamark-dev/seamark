package history

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolatedGit runs git in dir with a fixed identity and no global or
// system config, and returns its stdout.
func isolatedGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)

	out, err := cmd.Output()
	require.NoError(t, err, "git %v", args)

	return string(out)
}

func TestParsedGitArgsList(t *testing.T) {
	assert.Equal(t, []string{
		"-c", "core.quotePath=off",
		"-c", "diff.relative=false",
		"-c", "log.follow=false",
		"-c", "log.showSignature=false",
		"log",
		"--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", "--submodule=short",
		"-p", "--", "a.go",
	}, ParsedGitArgs("log", "-p", "--", "a.go"))
}

func TestParsedGitArgsRunWithEachSubcommand(t *testing.T) {
	// git must accept the fixed options with every subcommand that uses
	// them. With no config and ASCII paths, the output must also stay
	// the same, so a stored fingerprint stays valid after an upgrade.
	root := t.TempDir()
	file := filepath.Join(root, "a.go")

	isolatedGit(t, root, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(file, []byte("package a\n"), 0o644))
	isolatedGit(t, root, "add", "-A")
	isolatedGit(t, root, "commit", "-q", "-m", "init")
	require.NoError(t, os.WriteFile(file, []byte("package a\n\nvar x = 1\n"), 0o644))
	isolatedGit(t, root, "commit", "-q", "-am", "add x")
	require.NoError(t, os.WriteFile(file, []byte("package a\n\nvar x = 2\n"), 0o644))

	cases := [][]string{
		{"diff", "HEAD"},
		{"log", "-p"},
		{"log", "--numstat", "--format=%H"},
		{"show"},
		{"show", "-s", "--format=%H%x00%s"},
	}

	for _, c := range cases {
		t.Run(strings.Join(c, " "), func(t *testing.T) {
			want := isolatedGit(t, root, c...)
			require.NotEmpty(t, want)

			got := isolatedGit(t, root, ParsedGitArgs(c[0], c[1:]...)...)
			assert.Equal(t, want, got)
		})
	}
}
