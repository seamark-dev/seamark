package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalRuntimeIDIncludesFixtureToolchains(t *testing.T) {
	goOnly := LocalRuntimeID("agent-test", []Command{{Name: "go"}, {Name: "go", Args: []string{"vet"}}})
	assert.Contains(t, goOnly, "claude-native-sandbox-v2;")
	assert.Contains(t, goOnly, "agent=agent-test")
	assert.Contains(t, goOnly, "go=go version", "go answers `go version`, not --version")
	assert.Equal(t, 1, strings.Count(goOnly, "go="), "one toolchain is named once however many checks call it")
	assert.NotContains(t, goOnly, "python3=")

	schema := LocalRuntimeID("agent-test", SchemaSyncInstance().Checks)
	assert.Contains(t, schema, "python3=")
	assert.Contains(t, schema, "make=")
}

// writeStartedScript writes an executable script and starts it once, with no
// time limit. The first start of a new executable is slow on macOS, and it
// waits longer while other new executables start, as in a full test run.
// That wait can pass the 10 s limit of CommandVersion. Later starts of the
// same file are fast, so the timed call then measures only the script.
func writeStartedScript(t *testing.T, path, script string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	_ = exec.Command(path).Run() // The exit status does not matter for this first start.
}

func TestCommandVersionReadsTheFirstLineOrUnknown(t *testing.T) {
	dir := t.TempDir()

	chatty := filepath.Join(dir, "chatty")
	writeStartedScript(t, chatty, "#!/bin/sh\nprintf 'tool 1.2.3\\nbuilt yesterday\\n'\n")
	assert.Equal(t, "tool 1.2.3", CommandVersion(chatty, "--version"))

	// A timeout also gives "unknown". The first start above makes sure that
	// these two cases get "unknown" from their output, not from the limit.
	silent := filepath.Join(dir, "silent")
	writeStartedScript(t, silent, "#!/bin/sh\nexit 0\n")
	assert.Equal(t, "unknown", CommandVersion(silent, "--version"))

	failing := filepath.Join(dir, "failing")
	writeStartedScript(t, failing, "#!/bin/sh\necho oops\nexit 2\n")
	assert.Equal(t, "unknown", CommandVersion(failing, "--version"))

	assert.Equal(t, "unknown", CommandVersion(filepath.Join(dir, "absent")))
}

func TestExactModelIDRejectsAliases(t *testing.T) {
	for _, alias := range []string{"", "default", "opus", "Sonnet", "claude-sonnet-4-latest", "gpt-5"} {
		assert.False(t, ExactModelID(alias), alias)
	}

	assert.True(t, ExactModelID("claude-haiku-4-5-20251001"))
}
