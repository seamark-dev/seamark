package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
)

// gateDocument returns a hook document with one Bash gate handler.
// extra holds more handler fields as raw JSON, each with a leading comma.
func gateDocument(command, extra string) string {
	return `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + command + `"` + extra + `}]}]}}`
}

func TestInitGateLineNeverClaimsABlockForAHookInTheBackground(t *testing.T) {
	t.Run("a Claude local gate in the background", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.local.json",
			gateDocument("timeout 10 /bin/seamark gate --enforce --hook", `,"asyncRewake":true`)))

		out := initClients(t, root, false, false, false, "", integration.ClaudeID)
		assert.Contains(t, out, "  gate    warn — verdicts are reported, nothing blocks")
		assert.NotContains(t, out, "exit 2 and block")
		assert.Contains(t, out, "note    .claude/settings.local.json: has `timeout 10 /bin/seamark gate --enforce --hook` "+
			"with `asyncRewake: true`")
	})

	t.Run("a Claude local copy of the managed gate in the background", func(t *testing.T) {
		root := t.TempDir()
		managed := `,"timeout":15,"statusMessage":"seamark gate: classifying command"`
		require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json",
			gateDocument("/bin/seamark gate --enforce --hook", managed)))
		require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.local.json",
			gateDocument("/bin/seamark gate --enforce --hook", managed+`,"async":true`)))

		out := initClients(t, root, false, false, false, "", integration.ClaudeID)
		assert.Contains(t, out, "  gate    report-only — the gate hook in .claude/settings.json, .claude/settings.local.json "+
			"runs in the background: verdicts")
		assert.Contains(t, out, "remove the background key from that hook")
		assert.NotContains(t, out, "exit 2 and block")
	})

	t.Run("a Codex wrapper in the background under an enforce request", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, inspecttest.WriteFile(root, ".codex/hooks.json",
			gateDocument("timeout 10 /opt/bin/seamark gate --enforce --hook --client codex", `,"async":true`)))

		out := initClients(t, root, false, false, false, "enforce", integration.CodexID)
		assert.Contains(t, out, "  gate    enforce — deny/require_approval verdicts exit 2 and block", "the managed gate blocks")
		assert.NotContains(t, out, "runs its own gate hook with --enforce", "the wrapper is no enforcing hook")
		assert.Contains(t, out, "which runs in the background; the run asked for enforce mode")
	})

	t.Run("a Codex wrapper in the background without a request", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, inspecttest.WriteFile(root, ".codex/hooks.json",
			gateDocument("timeout 10 /opt/bin/seamark gate --enforce --hook --client codex", `,"async":true`)))

		out := initClients(t, root, false, false, false, "", integration.CodexID)
		assert.Contains(t, out, "  gate    report-only — the gate hook in .codex/hooks.json runs in the background: verdicts")
		assert.NotContains(t, out, "discards its exit status")
	})

	t.Run("a Codex wrapper in the background beside one that discards its exit status", func(t *testing.T) {
		// Each hook has another cause, so the fix names both.
		root := t.TempDir()
		require.NoError(t, inspecttest.WriteFile(root, ".codex/hooks.json",
			gateDocument("timeout 10 /opt/bin/seamark gate --enforce --hook --client codex", `,"async":true`)))
		require.NoError(t, inspecttest.WriteFile(root, ".codex/config.toml", "[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\n"+
			"command = \"/opt/bin/seamark gate --enforce --hook --client codex || true\"\n"))

		out := initClients(t, root, false, false, false, "", integration.CodexID)
		assert.Contains(t, out, "  gate    report-only — the gate hook in .codex/hooks.json, .codex/config.toml [hooks] "+
			"discards its exit status or runs in the background: verdicts")
		assert.Contains(t, out, "edits a gate hook it does not manage; run the gate last and in the foreground in that hook, "+
			"or remove it and re-run")
	})
}

func TestInitWithoutClientNamesTheRemovedBackgroundKey(t *testing.T) {
	// A plain init hides information findings, so the removal is a
	// warning.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json",
		gateDocument("/bin/seamark gate --enforce --hook", `,"async":true`)))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

	out := b.String()
	assert.Contains(t, out, "note    .claude/settings.json: setup removed `async: true` from the seamark gate hook under PreToolUse, "+
		"because a hook in the background cannot block")
	assert.Contains(t, out, "  gate    enforce — deny/require_approval verdicts exit 2 and block")
	assert.NotContains(t, string(mustRead(t, root, ".claude/settings.json")), `"async"`)
}
