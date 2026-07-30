package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func mustMerge(t *testing.T, settings map[string]any, bin, gateMode string) bool {
	t.Helper()

	changed, err := mergeHooks(settings, bin, gateMode)
	require.NoError(t, err)

	return changed
}

func TestMergeHooksIntoEmpty(t *testing.T) {
	settings := map[string]any{}

	assert.True(t, mustMerge(t, settings, "/bin/seamark", gateModeWarn))

	// The default gate hook carries no --enforce: policy.yaml alone
	// decides whether anything blocks.
	cmds := commands(t, settings)
	assert.Contains(t, cmds, "/bin/seamark gate --hook")
	assert.Contains(t, cmds, "/bin/seamark lessons --hook")
	assert.NotContains(t, cmds, "/bin/seamark gate --enforce --hook")
}

func TestMergeHooksEnforceMode(t *testing.T) {
	settings := map[string]any{}

	assert.True(t, mustMerge(t, settings, "/bin/seamark", gateModeEnforce))
	assert.Contains(t, commands(t, settings), "/bin/seamark gate --enforce --hook")
}

func TestMergeHooksSwitchesGateMode(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/bin/seamark", gateModeEnforce))

	// enforce → warn rewrites the hook in place: one gate hook, no flag.
	assert.True(t, mustMerge(t, settings, "/bin/seamark", gateModeWarn))

	cmds := commands(t, settings)
	assert.Len(t, cmds, 2, "mode switch must rewrite, not duplicate")
	assert.Contains(t, cmds, "/bin/seamark gate --hook")

	// And back: warn → enforce.
	assert.True(t, mustMerge(t, settings, "/bin/seamark", gateModeEnforce))

	cmds = commands(t, settings)
	assert.Len(t, cmds, 2)
	assert.Contains(t, cmds, "/bin/seamark gate --enforce --hook")
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

	require.True(t, mustMerge(t, settings, "/bin/seamark", gateModeWarn))

	cmds := commands(t, settings)
	assert.Contains(t, cmds, "my-own-linter", "the user's hook must survive")
	assert.Contains(t, cmds, "my-tool --lessons --hook-dir=/x",
		"a command merely containing the marker must not be rewritten")
	assert.Contains(t, cmds, "/bin/seamark gate --hook")
	assert.Contains(t, cmds, "/bin/seamark lessons --hook")

	assert.Equal(t, "opus", settings["model"], "unrelated settings untouched")

	hooks := settings["hooks"].(map[string]any)
	assert.NotNil(t, hooks["Stop"], "other hook events untouched")
}

func TestMergeHooksIdempotent(t *testing.T) {
	for _, mode := range []string{gateModeWarn, gateModeEnforce} {
		settings := map[string]any{}
		require.True(t, mustMerge(t, settings, "/bin/seamark", mode))

		// A second merge with the same binary and mode changes nothing.
		assert.False(t, mustMerge(t, settings, "/bin/seamark", mode),
			"re-run in %s mode must be a no-op", mode)
		assert.Len(t, commands(t, settings), 2, "no duplicate hooks")
	}
}

func TestMergeHooksUpdatesChangedPath(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/old/seamark", gateModeWarn))

	// A moved binary: the command path is rewritten, not duplicated.
	assert.True(t, mustMerge(t, settings, "/new/seamark", gateModeWarn))

	cmds := commands(t, settings)
	assert.Len(t, cmds, 2)
	for _, c := range cmds {
		assert.Contains(t, c, "/new/seamark")
	}
}

func TestMergeHooksQuotesPathWithSpaces(t *testing.T) {
	settings := map[string]any{}
	require.True(t, mustMerge(t, settings, "/Apps/My Tools/seamark", gateModeWarn))

	for _, c := range commands(t, settings) {
		assert.Contains(t, c, "'/Apps/My Tools/seamark'", "a spaced path must be shell-quoted")
	}

	// And a quoted install is still recognized on re-run (idempotent).
	assert.False(t, mustMerge(t, settings, "/Apps/My Tools/seamark", gateModeWarn))
}

func TestMergeHooksRejectsMalformedHooks(t *testing.T) {
	// A present-but-wrong-typed hooks field must error, not be silently
	// discarded (finding #3).
	for _, bad := range []any{"a string", []any{"x"}, 42.0} {
		settings := map[string]any{"hooks": bad}
		_, err := mergeHooks(settings, "/bin/seamark", gateModeWarn)
		assert.Error(t, err, "hooks=%v (%T) must be rejected", bad, bad)
	}

	settings := map[string]any{"hooks": map[string]any{"PreToolUse": "not an array"}}
	_, err := mergeHooks(settings, "/bin/seamark", gateModeWarn)
	assert.Error(t, err, "a non-array PreToolUse must be rejected")
}

func TestRunInitScaffoldsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()

	var b1 testWriter
	require.NoError(t, runInit(&b1, root, "/bin/seamark", gateModeWarn, false))

	// Files exist with expected content.
	for _, rel := range []string{
		".seamark/policy.yaml", ".seamark/lessons.yaml", ".seamark/config.yaml", ".gitignore",
	} {
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
	require.NoError(t, runInit(&b2, root, "/bin/seamark", gateModeWarn, false))
	assert.Contains(t, b2.String(), "kept")

	data, err = os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Len(t, commands(t, settings), 2, "re-run must not duplicate hooks")
}

func TestEnsureGitignoreAddsMissingCarveouts(t *testing.T) {
	root := t.TempDir()

	// A .gitignore from an older seamark: block present, but from before
	// the config.yaml carve-out existed.
	old := "bin/\n\n# Seamark: local state.\n.seamark/*\n!.seamark/policy.yaml\n" +
		"!.seamark/effects.yaml\n!.seamark/lessons.yaml\n"
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(old), 0o644))

	var b testWriter
	require.NoError(t, ensureGitignore(&b, root, false))
	assert.Contains(t, b.String(), "updated")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)

	// Only the missing line was appended — no duplicated block — and the
	// re-include lands after `.seamark/*`, as gitignore precedence needs.
	assert.Contains(t, got, "!.seamark/config.yaml")
	assert.Equal(t, 1, strings.Count(got, ".seamark/*"))
	assert.Greater(t, strings.Index(got, "!.seamark/config.yaml"), strings.Index(got, ".seamark/*"))

	// Re-run: nothing left to repair.
	var b2 testWriter
	require.NoError(t, ensureGitignore(&b2, root, false))
	assert.Contains(t, b2.String(), "kept")
}

func TestRunInitPrintWritesNothing(t *testing.T) {
	root := t.TempDir()

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", gateModeWarn, true))

	assert.NoFileExists(t, filepath.Join(root, ".seamark", "policy.yaml"))
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
	assert.Contains(t, b.String(), "would write")

	// The preview shows the exact hook commands.
	assert.Contains(t, b.String(), "/bin/seamark gate --hook")
	assert.Contains(t, b.String(), "/bin/seamark lessons --hook")
}

func TestRunInitKeepsExistingConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: enforce\n"), 0o644))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", gateModeWarn, false))

	// An existing policy is never clobbered.
	got, err := os.ReadFile(filepath.Join(root, ".seamark", "policy.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "mode: enforce\n", string(got))
	assert.Contains(t, b.String(), "kept")
}

func TestRunInitStatesGateMode(t *testing.T) {
	// Every init run states the effective blocking behaviour — the trust
	// contract is that enforcement is never a surprise.
	root := t.TempDir()

	var warn testWriter
	require.NoError(t, runInit(&warn, root, "/bin/seamark", gateModeWarn, false))
	assert.Contains(t, warn.String(), "gate    warn")
	assert.Contains(t, warn.String(), "nothing blocks")

	var enforce testWriter
	require.NoError(t, runInit(&enforce, t.TempDir(), "/bin/seamark", gateModeEnforce, false))
	assert.Contains(t, enforce.String(), "gate    enforce")
}

func TestRunInitReportsKeptEnforcePolicy(t *testing.T) {
	// `init --gate-mode enforce` then `init --gate-mode warn`: the policy
	// file is kept, still says enforce, and the warn hook follows it — so
	// the summary must say "enforce", never "nothing blocks".
	root := t.TempDir()

	var first testWriter
	require.NoError(t, runInit(&first, root, "/bin/seamark", gateModeEnforce, false))

	var second testWriter
	require.NoError(t, runInit(&second, root, "/bin/seamark", gateModeWarn, false))
	assert.Contains(t, second.String(), "gate    enforce")
	assert.Contains(t, second.String(), "policy.yaml", "the summary must point at the kept policy")
	assert.NotContains(t, second.String(), "nothing blocks")
}

func TestRunInitReportsKeptWarnPolicyUnderEnforce(t *testing.T) {
	// The mirror case: --gate-mode enforce over a kept warn policy. The
	// hook enforces, but plain gate/check runs follow the file — the
	// divergence must be stated, not hidden.
	root := t.TempDir()

	var first testWriter
	require.NoError(t, runInit(&first, root, "/bin/seamark", gateModeWarn, false))

	var second testWriter
	require.NoError(t, runInit(&second, root, "/bin/seamark", gateModeEnforce, false))
	assert.Contains(t, second.String(), "gate    enforce")
	assert.Contains(t, second.String(), "mode: warn", "the kept policy's differing mode must be named")
}

func TestRunInitReportsBrokenPolicy(t *testing.T) {
	// A policy file that fails to load changes what the hook does with
	// every command; the summary must say which way it fails.
	broken := []byte("mode: [broken\n")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), broken, 0o644))

	var warn testWriter
	require.NoError(t, runInit(&warn, root, "/bin/seamark", gateModeWarn, false))
	assert.Contains(t, warn.String(), "failed to load")
	assert.Contains(t, warn.String(), "fails open")

	root = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), broken, 0o644))

	var enforce testWriter
	require.NoError(t, runInit(&enforce, root, "/bin/seamark", gateModeEnforce, false))
	assert.Contains(t, enforce.String(), "failed to load")
	assert.Contains(t, enforce.String(), "fails closed")
}

func TestRunInitRejectsMalformedSettings(t *testing.T) {
	// Malformed .claude/settings.json: resolveGateMode falls back to warn
	// (nothing worse can be inferred), but ensureHooks must refuse loudly
	// rather than silently overwriting the user's file.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))

	path := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	mode, err := resolveGateMode(root, "")
	require.NoError(t, err)
	assert.Equal(t, gateModeWarn, mode, "an unreadable install must not resolve to enforce")

	var b testWriter
	err = runInit(&b, root, "/bin/seamark", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix or move it")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{not json", string(data), "the malformed file must survive untouched")
}

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
		assert.Equal(t, want, ownedBySeamark(cmd, markers), "cmd: %s", cmd)
	}
}

func TestRunInitShowsHookCommandsWhenKept(t *testing.T) {
	// The exact-command listing must not depend on whether settings.json
	// needed an update: a no-op re-run (or its --print preview) shows the
	// same commands a fresh install does.
	root := t.TempDir()

	var first testWriter
	require.NoError(t, runInit(&first, root, "/bin/seamark", gateModeWarn, false))

	var kept testWriter
	require.NoError(t, runInit(&kept, root, "/bin/seamark", "", true))
	assert.Contains(t, kept.String(), "kept    .claude/settings.json")
	assert.Contains(t, kept.String(), "/bin/seamark gate --hook")
	assert.Contains(t, kept.String(), "/bin/seamark lessons --hook")
}

// seedSettings writes a .claude/settings.json with the given PreToolUse
// command wired on Bash.
func seedSettings(t *testing.T, root, command string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))

	settings := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"` + command + `"}]}]}}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(settings), 0o644))
}

func readSettings(t *testing.T, root string) map[string]any {
	t.Helper()

	var settings map[string]any

	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &settings))

	return settings
}

func TestRunInitExplicitWarnMigratesEnforceHook(t *testing.T) {
	// An enforce hook (from --gate-mode enforce, or a pre-warn-default
	// init) is downgraded only on an EXPLICIT --gate-mode warn — and the
	// change is reported loudly, because it alters blocking behaviour.
	root := t.TempDir()
	seedSettings(t, root, "/bin/seamark gate --enforce --hook")

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", gateModeWarn, false))
	assert.Contains(t, b.String(), "note", "dropping enforcement must be reported")
	assert.Contains(t, b.String(), "--gate-mode enforce", "the note must say how to restore it")

	cmds := commands(t, readSettings(t, root))
	assert.Contains(t, cmds, "/bin/seamark gate --hook", "the enforce hook is rewritten in place")
	assert.NotContains(t, cmds, "/bin/seamark gate --enforce --hook")
	assert.Len(t, cmds, 2, "gate rewritten + lessons added — no duplicates")
}

func TestRunInitDefaultKeepsInstalledEnforce(t *testing.T) {
	// A plain re-run of init (no --gate-mode) must never weaken an
	// installed enforce hook: enforcement is only removed explicitly.
	root := t.TempDir()
	seedSettings(t, root, "/bin/seamark gate --enforce --hook")

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false))

	cmds := commands(t, readSettings(t, root))
	assert.Contains(t, cmds, "/bin/seamark gate --enforce --hook", "enforce survives a plain re-init")
	assert.Contains(t, b.String(), "gate    enforce")
	assert.NotContains(t, b.String(), "note    ", "nothing changed mode, nothing to warn about")
}

func TestRunInitLeavesForeignGateHookAlone(t *testing.T) {
	// A hook that merely ends with our marker but runs someone else's
	// binary is not ours: it must survive untouched, and it must not be
	// mistaken for an installed seamark mode.
	root := t.TempDir()
	seedSettings(t, root, "/usr/bin/company-security gate --enforce --hook")

	assert.Empty(t, installedGateMode(readSettings(t, root)),
		"a foreign gate hook must not read as seamark's")

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false))

	cmds := commands(t, readSettings(t, root))
	assert.Contains(t, cmds, "/usr/bin/company-security gate --enforce --hook",
		"the foreign hook survives untouched")
	assert.Contains(t, cmds, "/bin/seamark gate --hook", "seamark's own hook is added beside it")
	assert.Contains(t, b.String(), "gate    warn", "a foreign enforce hook must not flip our default")
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

func TestStarterPolicyEnforceVariant(t *testing.T) {
	// The enforce scaffold is the warn template with one line swapped; a
	// template edit that breaks the swap would silently ship warn to a
	// user who explicitly asked for enforce.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte(starterPolicyFor(gateModeEnforce)), 0o644))

	policy, err := gate.LoadPolicy(root)
	require.NoError(t, err, "the enforce starter must still parse and compile")
	assert.Equal(t, "enforce", policy.Mode)

	assert.NotEqual(t, starterPolicy, starterPolicyFor(gateModeEnforce),
		"the mode-line swap must actually change the template")
	assert.Equal(t, starterPolicy, starterPolicyFor(gateModeWarn))
}

// testWriter is a minimal io.Writer capturing output.
type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *testWriter) String() string              { return string(w.b) }
