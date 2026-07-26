package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/gate"
)

// commands flattens every PreToolUse command string in a settings map.
func commands(t *testing.T, settings map[string]any) []string {
	t.Helper()

	hooks, _ := settings["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)

	var out []string

	forEachCommand(pre, func(_ map[string]any, cmd string) {
		out = append(out, cmd)
	})

	return out
}

func mustMerge(t *testing.T, settings map[string]any, bin string) bool {
	t.Helper()

	changed, err := mergeHooks(settings, bin)
	require.NoError(t, err)

	return changed
}

func TestMergeHooksIntoEmpty(t *testing.T) {
	settings := map[string]any{}

	assert.True(t, mustMerge(t, settings, "/bin/seamark"))

	cmds := commands(t, settings)
	assert.Contains(t, cmds, "/bin/seamark gate --enforce --hook")
	assert.Contains(t, cmds, "/bin/seamark lessons --hook")
}

func TestMergeHooksPreservesExisting(t *testing.T) {
	settings := map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "my-own-linter"},
					},
				},
				// A command that CONTAINS a marker as a substring but is not
				// seamark's — must NOT be clobbered (finding #1).
				map[string]any{
					"matcher": "Edit",
					"hooks": []any{
						map[string]any{"type": "command", "command": "my-tool --lessons --hook-dir=/x"},
					},
				},
			},
			"Stop": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "notify"},
				}},
			},
		},
	}

	require.True(t, mustMerge(t, settings, "/bin/seamark"))

	cmds := commands(t, settings)
	assert.Contains(t, cmds, "my-own-linter", "the user's hook must survive")
	assert.Contains(t, cmds, "my-tool --lessons --hook-dir=/x",
		"a command merely containing the marker must not be rewritten")
	assert.Contains(t, cmds, "/bin/seamark gate --enforce --hook")
	assert.Contains(t, cmds, "/bin/seamark lessons --hook")

	assert.Equal(t, "opus", settings["model"], "unrelated settings untouched")

	hooks := settings["hooks"].(map[string]any)
	assert.NotNil(t, hooks["Stop"], "other hook events untouched")
}

func TestMergeHooksIdempotent(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/bin/seamark"))

	// A second merge with the same binary changes nothing.
	assert.False(t, mustMerge(t, settings, "/bin/seamark"), "re-run must be a no-op")
	assert.Len(t, commands(t, settings), 2, "no duplicate hooks")
}

func TestMergeHooksUpdatesChangedPath(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/old/seamark"))

	// A moved binary: the command path is rewritten, not duplicated.
	assert.True(t, mustMerge(t, settings, "/new/seamark"))

	cmds := commands(t, settings)
	assert.Len(t, cmds, 2)
	for _, c := range cmds {
		assert.Contains(t, c, "/new/seamark")
	}
}

func TestMergeHooksQuotesPathWithSpaces(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/Apps/My Tools/seamark"))

	for _, c := range commands(t, settings) {
		assert.Contains(t, c, "'/Apps/My Tools/seamark'", "a spaced path must be shell-quoted")
	}

	// And a quoted install is still recognized on re-run (idempotent).
	assert.False(t, mustMerge(t, settings, "/Apps/My Tools/seamark"))
}

func TestMergeHooksRejectsMalformedHooks(t *testing.T) {
	// A present-but-wrong-typed hooks field must error, not be silently
	// discarded (finding #3).
	for _, bad := range []any{"a string", []any{"x"}, 42.0} {
		settings := map[string]any{"hooks": bad}
		_, err := mergeHooks(settings, "/bin/seamark")
		assert.Error(t, err, "hooks=%v (%T) must be rejected", bad, bad)
	}

	settings := map[string]any{"hooks": map[string]any{"PreToolUse": "not an array"}}
	_, err := mergeHooks(settings, "/bin/seamark")
	assert.Error(t, err, "a non-array PreToolUse must be rejected")
}

func TestRunInitScaffoldsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()

	var b1 testWriter
	require.NoError(t, runInit(&b1, root, "/bin/seamark", false))

	// Files exist with expected content.
	for _, rel := range []string{".seamark/policy.yaml", ".seamark/lessons.yaml", ".gitignore"} {
		assert.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
	}

	gi, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gi), ".seamark/*")

	var settings map[string]any
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Len(t, commands(t, settings), 2)

	// Re-run: everything kept, no duplication.
	var b2 testWriter
	require.NoError(t, runInit(&b2, root, "/bin/seamark", false))
	assert.Contains(t, b2.String(), "kept")

	data, err = os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Len(t, commands(t, settings), 2, "re-run must not duplicate hooks")
}

func TestRunInitPrintWritesNothing(t *testing.T) {
	root := t.TempDir()

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", true))

	assert.NoFileExists(t, filepath.Join(root, ".seamark", "policy.yaml"))
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
	assert.Contains(t, b.String(), "would write")
}

func TestRunInitKeepsExistingConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: enforce\n"), 0o644))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", false))

	// An existing policy is never clobbered.
	got, err := os.ReadFile(filepath.Join(root, ".seamark", "policy.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "mode: enforce\n", string(got))
	assert.Contains(t, b.String(), "kept")
}

func TestStarterPolicyLoadsAndEvaluates(t *testing.T) {
	// The scaffolded policy REPLACES the embedded default gate policy, so
	// a CEL typo in the template would break the gate on every command for
	// every init'd user. Load and evaluate it through the real path.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte(starterPolicy), 0o644))

	policy, err := gate.LoadPolicy(root)
	require.NoError(t, err, "starter policy must parse and its CEL must compile")

	catalog, err := effects.Load(root)
	require.NoError(t, err)

	d, err := gate.EvalCommand(policy, catalog, root, "ls -la")
	require.NoError(t, err, "starter policy must evaluate a command without error")
	assert.Equal(t, "warn", d.Mode, "starter ships in warn mode")
}

// testWriter is a minimal io.Writer capturing output.
type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *testWriter) String() string              { return string(w.b) }
