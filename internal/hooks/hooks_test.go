package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnedBySeamark(t *testing.T) {
	markers := []string{"gate --hook", "gate --enforce --hook"}

	for cmd, want := range map[string]bool{
		"/bin/seamark gate --hook":                        true,
		"/bin/seamark gate --enforce --hook":              true,
		"'/Apps/My Tools/seamark' gate --hook":            true,
		"/c/tools/seamark.exe gate --hook":                true,
		"'/c/my tools/seamark.exe' gate --enforce --hook": true,
		"/usr/bin/company-security gate --hook":           false,
		"/bin/seamarketing-tool gate --hook":              false,
		"/bin/seamark2 gate --enforce --hook":             false,
		"/bin/seamark2.exe gate --hook":                   false,
		"/bin/seamark lessons --hook":                     false, // not a gate marker
		"my-tool --lessons --hook-dir=/x":                 false,
	} {
		assert.Equal(t, want, OwnedBySeamark(cmd, markers), "cmd: %s", cmd)
	}
}

func TestInstalledGateModeAt(t *testing.T) {
	root := t.TempDir()

	// Absent settings: no hook, no error.
	mode, err := InstalledGateModeAt(root)
	require.NoError(t, err)
	assert.Empty(t, mode)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	path := filepath.Join(root, ".claude", "settings.json")

	// Unparseable settings are an ERROR, not "no hook": the two findings
	// must never be conflated.
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	mode, err = InstalledGateModeAt(root)
	require.Error(t, err)
	assert.Empty(t, mode)

	write := func(matcher, hookType, cmd string) {
		require.NoError(t, os.WriteFile(path, []byte(
			`{"hooks":{"PreToolUse":[{"matcher":"`+matcher+`","hooks":[`+
				`{"type":"`+hookType+`","command":"`+cmd+`"}]}]}}`), 0o644))
	}

	mustMode := func(want string) {
		t.Helper()

		mode, err := InstalledGateModeAt(root)
		require.NoError(t, err)
		assert.Equal(t, want, mode)
	}

	write("Bash", "command", "/usr/bin/company-security gate --enforce --hook")
	mustMode("") // a foreign hook must not read as ours

	write("Bash", "command", "/bin/seamark gate --hook")
	mustMode(ModeWarn)

	write("Bash", "command", "/bin/seamark gate --enforce --hook")
	mustMode(ModeEnforce)

	// A gate command that Claude Code would never fire on Bash commands
	// is not an operational gate hook.
	write("Edit", "command", "/bin/seamark gate --enforce --hook")
	mustMode("")

	write("Bash", "notify", "/bin/seamark gate --enforce --hook")
	mustMode("")
}
