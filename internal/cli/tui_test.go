package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
)

// TestTUIWithoutTerminalPrintsPlainGuidance proves the noninteractive
// path: a pipe gets the equivalent plain commands and a failure status,
// never escape sequences, and the workspace stays untouched.
func TestTUIWithoutTerminalPrintsPlainGuidance(t *testing.T) {
	root := writeFixture(t)
	before := snapshot(t, root)

	out, err := run(t, "-C", root, "tui")
	require.ErrorIs(t, err, errNotInteractive)

	assert.Contains(t, out, "interactive terminal")
	assert.Contains(t, out, "seamark doctor")
	assert.Contains(t, out, "seamark init --client")
	assert.Contains(t, out, "seamark lessons --proposals")

	// The pin maintenance of the console has CLI routes too. The text
	// names the difference of the CLI retarget, because it can lose a
	// hand-edited note.
	assert.Contains(t, out, "seamark lessons --retarget <ids>")
	assert.Contains(t, out, "seamark lessons --prune <ids>")
	assert.Contains(t, out, "seamark lessons --extract-triggers --dry-run")
	assert.Contains(t, out, "The CLI retarget rewrites the whole entry")
	assert.NotContains(t, out, "\x1b", "plain guidance carries no escape sequence")
	assert.Equal(t, before, snapshot(t, root), "a refused launch writes nothing")
}

// TestTUIRejectsInvalidWorkspaceFirst proves that a bad workspace fails
// before any terminal handling, with a readable error.
func TestTUIRejectsInvalidWorkspaceFirst(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	out, err := run(t, "-C", missing, "tui")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errNotInteractive)
	assert.Empty(t, out, "no guidance for a workspace that does not exist")
}

// TestTUIDevNullIsNotInteractive proves finding 27: /dev/null is a
// character device but not a terminal, so the console does not start
// on it and cannot hang waiting for a key.
func TestTUIDevNullIsNotInteractive(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)

	defer func() { _ = devNull.Close() }()

	// The guard fails here, before a regression can start the console
	// on /dev/null and block the test.
	require.False(t, interactive(devNull, devNull))

	root := writeFixture(t)

	var errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(devNull)
	cmd.SetOut(devNull)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"-C", root, "tui"})

	require.ErrorIs(t, cmd.Execute(), errNotInteractive)
}

// TestTUIReadOnlyGitEnv proves the environment changes that keep git
// from writing .git/index, including the append to existing entries.
func TestTUIReadOnlyGitEnv(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := env[k]

			return v, ok
		}
	}

	assert.Equal(t, [][2]string{
		{"GIT_OPTIONAL_LOCKS", "0"},
		{"GIT_CONFIG_KEY_0", "diff.autoRefreshIndex"},
		{"GIT_CONFIG_VALUE_0", "false"},
		{"GIT_CONFIG_COUNT", "1"},
	}, readOnlyGitEnv(lookup(nil)))

	assert.Equal(t, [][2]string{
		{"GIT_OPTIONAL_LOCKS", "0"},
		{"GIT_CONFIG_KEY_2", "diff.autoRefreshIndex"},
		{"GIT_CONFIG_VALUE_2", "false"},
		{"GIT_CONFIG_COUNT", "3"},
	}, readOnlyGitEnv(lookup(map[string]string{"GIT_CONFIG_COUNT": "2"})), "existing entries stay")

	assert.Equal(t, [][2]string{{"GIT_OPTIONAL_LOCKS", "0"}},
		readOnlyGitEnv(lookup(map[string]string{"GIT_CONFIG_COUNT": "many"})),
		"a broken count gets no new entry")
}

// TestTUIReadOnlyGitKeepsTheGitIndex proves finding 26: with the console
// environment, the index freshness check leaves .git/index unchanged,
// although a tracked file has a new modification time.
func TestTUIReadOnlyGitKeepsTheGitIndex(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	gitIndex := filepath.Join(root, ".git", "index")
	tracked := filepath.Join(root, "a.go")

	touch := func(age time.Duration) {
		at := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(tracked, at, at))
	}

	read := func() []byte {
		data, err := os.ReadFile(gitIndex)
		require.NoError(t, err)

		return data
	}

	// Control: a plain git status rewrites the index after the touch,
	// so the fixture can show a write.
	touch(2 * time.Hour)

	before := read()

	status := exec.Command("git", "status", "--porcelain")
	status.Dir = root
	status.Env = append(tuiEnvWithoutGit(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := status.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NotEqual(t, before, read(), "the fixture must make plain git status write the index")

	// The console environment: the same check writes nothing.
	for _, k := range tuiGitEnvKeys {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	require.NoError(t, useReadOnlyGit())
	assert.Equal(t, "0", os.Getenv("GIT_OPTIONAL_LOCKS"))

	touch(3 * time.Hour)

	before = read()
	require.NotEmpty(t, index.WorkspaceState(root))
	assert.Equal(t, before, read(), "the freshness check rewrote .git/index")
}

// tuiGitEnvKeys are the variables that useReadOnlyGit may set. The test
// resets them first, so the result does not depend on the caller's
// environment, and t.Setenv restores them afterwards.
var tuiGitEnvKeys = []string{
	"GIT_OPTIONAL_LOCKS", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
}

// tuiEnvWithoutGit returns the process environment without the git
// variables that change index writes.
func tuiEnvWithoutGit() []string {
	var env []string

	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_OPTIONAL_LOCKS=") || strings.HasPrefix(kv, "GIT_CONFIG_") {
			continue
		}

		env = append(env, kv)
	}

	return env
}
