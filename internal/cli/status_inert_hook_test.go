package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
)

// editGateHook rewrites the seamark gate definition of the Claude Code
// settings that init wrote. edit gets the matcher entry and the handler
// of that definition.
func editGateHook(t *testing.T, root string, edit func(entry, handler map[string]any)) {
	t.Helper()

	path := filepath.Join(root, ".claude", "settings.json")

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	hooksDoc, ok := doc["hooks"].(map[string]any)
	require.True(t, ok)

	entries, ok := hooksDoc["PreToolUse"].([]any)
	require.True(t, ok)

	edited := 0

	for _, e := range entries {
		entry, _ := e.(map[string]any)
		handlers, _ := entry["hooks"].([]any)

		for _, h := range handlers {
			handler, _ := h.(map[string]any)
			if command, _ := handler["command"].(string); strings.Contains(command, " gate ") {
				edit(entry, handler)
				edited++
			}
		}
	}

	require.Equal(t, 1, edited, "init writes one gate definition")

	data, err = json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

// initWithFixtureBinary runs a plain init that writes inspecttest.Binary
// into the hooks. inspecttest treats its binary as one that the client
// can start, so no file needs to exist at that path.
func initWithFixtureBinary(t *testing.T, root string) {
	t.Helper()

	var b testWriter

	require.NoError(t, runInit(&b, root, inspecttest.Binary, "", false, "", false))
}

func TestStatusAndDoctorNameAGateHookThatNeverRuns(t *testing.T) {
	// A gate hook that the client never runs is present, so status must
	// not offer a setup run as if none were installed. Status, doctor, and
	// the clients line name one cause and one repair that runs. Status
	// names each of them once: the gate line says only that no gate hook
	// runs.
	t.Run("matcher never fires", func(t *testing.T) {
		root := writeFixture(t)

		initWithFixtureBinary(t, root)

		editGateHook(t, root, func(entry, _ map[string]any) { entry["matcher"] = "Shell" })

		_, err := run(t, "-C", root, "index")
		require.NoError(t, err)

		detail := `gate hook never runs from .claude/settings.json: its matcher "Shell" does not fire for Bash`
		action := `set the matcher of that hook to "Bash" in .claude/settings.json`

		status, err := run(t, "-C", root, "status")
		require.NoError(t, err)
		assert.Contains(t, status, "gate           policy mode warn; no gate hook runs for shell commands\nskills ")
		assert.Contains(t, status, "claude  lessons hook installed; "+detail)
		assert.Equal(t, 1, strings.Count(status, detail), "the clients line names the cause")
		assert.Equal(t, 1, strings.Count(status, action), "the warning names the repair")

		doctor, err := run(t, "-C", root, "doctor")
		require.NoError(t, err)
		assert.Contains(t, doctor, detail)

		for _, out := range []string{status, doctor} {
			assert.NotContains(t, out, "gate (mode unknown)")
			assert.NotContains(t, out, "no gate hook installed")
		}

		// A setup run keeps the matcher, which is why no line offers one.
		before, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
		require.NoError(t, err)

		initWithFixtureBinary(t, root)

		after, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})

	t.Run("binary cannot start", func(t *testing.T) {
		root := writeFixture(t)

		initWithFixtureBinary(t, root)

		editGateHook(t, root, func(_, handler map[string]any) {
			handler["command"] = "/nonexistent/seamark gate --hook"
		})

		_, err := run(t, "-C", root, "index")
		require.NoError(t, err)

		status, err := run(t, "-C", root, "status")
		require.NoError(t, err)
		assert.Contains(t, status, "gate           policy mode warn; no gate hook runs for shell commands\nskills ")
		assert.Equal(t, 1, strings.Count(status, "gate hook cannot start from .claude/settings.json: /nonexistent/seamark is missing"),
			"the clients line names the cause")
		assert.Equal(t, 1, strings.Count(status, "which rewrites the hook command to the running seamark binary"),
			"the warning names the repair")
		assert.NotContains(t, status, "no gate hook installed")
	})
}
