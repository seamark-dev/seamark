package bench

import (
	"os"
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

func TestCommandVersionReadsTheFirstLineOrUnknown(t *testing.T) {
	dir := t.TempDir()

	chatty := filepath.Join(dir, "chatty")
	require.NoError(t, os.WriteFile(chatty, []byte("#!/bin/sh\nprintf 'tool 1.2.3\\nbuilt yesterday\\n'\n"), 0o755))
	assert.Equal(t, "tool 1.2.3", CommandVersion(chatty, "--version"))

	silent := filepath.Join(dir, "silent")
	require.NoError(t, os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	assert.Equal(t, "unknown", CommandVersion(silent, "--version"))

	failing := filepath.Join(dir, "failing")
	require.NoError(t, os.WriteFile(failing, []byte("#!/bin/sh\necho oops\nexit 2\n"), 0o755))
	assert.Equal(t, "unknown", CommandVersion(failing, "--version"))

	assert.Equal(t, "unknown", CommandVersion(filepath.Join(dir, "absent")))
}

func TestExactModelIDRejectsAliases(t *testing.T) {
	for _, alias := range []string{"", "default", "opus", "Sonnet", "claude-sonnet-4-latest", "gpt-5"} {
		assert.False(t, ExactModelID(alias), alias)
	}

	assert.True(t, ExactModelID("claude-haiku-4-5-20251001"))
}
