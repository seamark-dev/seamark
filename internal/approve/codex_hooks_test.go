package approve

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexInlineHooksReadsEveryCommand(t *testing.T) {
	commands, present, err := CodexInlineHooks([]byte(`model = "x"

[[hooks.PreToolUse]]
matcher = "apply_patch"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "/usr/local/bin/seamark lessons --hook --client codex"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "/opt/audit.sh"

[[hooks.Stop]]
hooks = [{ type = "command", command = "/opt/notify.sh" }]
`))
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, []string{
		"/opt/audit.sh", "/opt/notify.sh", "/usr/local/bin/seamark lessons --hook --client codex",
	}, commands, "sorted, so a finding is reproducible")
}

func TestCodexInlineHooksWithoutATable(t *testing.T) {
	for _, config := range []string{
		"",
		"model = \"x\"\n[mcp_servers.seamark]\ncommand = \"seamark\"\n",
		"[hooks]\n",
		// The feature flag is another table: it defines no hook.
		"[features]\nhooks = false\n",
	} {
		commands, present, err := CodexInlineHooks([]byte(config))
		require.NoError(t, err, config)
		assert.False(t, present, config)
		assert.Empty(t, commands, config)
	}

	// A hooks table in a layout the walk cannot read is still reported.
	_, present, err := CodexInlineHooks([]byte("[hooks]\nPreToolUse = \"something\"\n"))
	require.NoError(t, err)
	assert.True(t, present)

	_, _, err = CodexInlineHooks([]byte("model = [broken"))
	require.Error(t, err)
}
