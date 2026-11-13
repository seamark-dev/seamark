package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/settings"
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

	// The pin maintenance of the console has CLI routes too. They share
	// the engine of the console, so the text no longer warns that the
	// CLI retarget rewrites the whole entry.
	assert.Contains(t, out, "seamark lessons --retarget <ids>")
	assert.Contains(t, out, "seamark lessons --prune <ids>")
	assert.Contains(t, out, "seamark lessons --extract-triggers --dry-run")
	assert.NotContains(t, out, "rewrites the whole entry")

	// Pin authoring and the selection sample have no command of their
	// own. So the text names the closest view of each.
	assert.Contains(t, out, "seamark lessons --list ")
	assert.Contains(t, out, "seamark lessons --file <path>")
	assert.Contains(t, out, "Pins that you write live there too")

	// The console Settings edit both files, so the text names both.
	assert.Contains(t, out, "Settings live in .seamark/config.yaml")
	assert.Contains(t, out, "Lesson delivery and mutes live in .seamark/lessons.yaml")
	assert.NotContains(t, out, "\x1b", "plain guidance carries no escape sequence")
	assert.Equal(t, before, snapshot(t, root), "a refused launch writes nothing")
}

// TestTUIGuidanceNamesLessonChecks proves that the plain guidance names
// the command route of each lesson-check action of the console: the
// verification of V, the mode edit of the pin form, the definitions that
// M edits, and the init flags that install and remove the hooks, which
// the console only shows. A refused launch still writes nothing.
func TestTUIGuidanceNamesLessonChecks(t *testing.T) {
	root := writeFixture(t)
	before := snapshot(t, root)

	out, err := run(t, "-C", root, "tui")
	require.ErrorIs(t, err, errNotInteractive)

	assert.Contains(t, out, "seamark lessons --verify ")
	assert.Contains(t, out, "seamark lessons --set-mode <mode> --pin <pin-ref>")
	assert.Contains(t, out, "--lesson-checks <off|check|enforce>")
	assert.Contains(t, out, "--lesson-exploration <off|on>")
	assert.Contains(t, out, "Check definitions live there under checks:")
	assert.NotContains(t, out, "\x1b", "plain guidance carries no escape sequence")

	for _, line := range strings.Split(out, "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 100, "a guidance line stays readable in a narrow terminal: %q", line)
	}

	assert.Equal(t, before, snapshot(t, root), "a refused launch writes nothing")
}

// TestTUIGuidanceGatedCommandsMatchSettings proves that the plain
// guidance and the distill.write setting name the same gated commands.
// Both texts list the commands by hand, so a new gated command in only
// one text fails here.
func TestTUIGuidanceGatedCommandsMatchSettings(t *testing.T) {
	flag := regexp.MustCompile(`--[a-z][a-z-]*`)

	guidance := map[string]bool{}

	for _, line := range strings.Split(tuiGuidance, "\n") {
		if !strings.Contains(line, "(needs distill.write)") {
			continue
		}

		// The first flag of a guidance line is its command.
		cmd := flag.FindString(line)
		require.NotEmpty(t, cmd, "a gated guidance line names a flag: %q", line)

		guidance[cmd] = true
	}

	setting := map[string]bool{}
	for _, f := range flag.FindAllString(settings.PinWriteCommands, -1) {
		setting[f] = true
	}

	want := map[string]bool{"--apply": true, "--retarget": true, "--prune": true, "--set-mode": true}
	assert.Equal(t, want, guidance, "the guidance marks these commands as gated")
	assert.Equal(t, guidance, setting, "the setting names every gated command of the guidance")
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

// TestUseReadOnlyGitSetsBothParts proves that the console sets both
// parts of the read-only git setting for its process.
func TestUseReadOnlyGitSetsBothParts(t *testing.T) {
	for _, k := range tuiGitEnvKeys {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	require.NoError(t, useReadOnlyGit())

	assert.Equal(t, "0", os.Getenv("GIT_OPTIONAL_LOCKS"))
	assert.Equal(t, "1", os.Getenv("GIT_CONFIG_COUNT"))
	assert.Equal(t, "diff.autoRefreshIndex", os.Getenv("GIT_CONFIG_KEY_0"))
	assert.Equal(t, "false", os.Getenv("GIT_CONFIG_VALUE_0"))
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
