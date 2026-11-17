package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

func TestPlansMarkTheGateHooksWhenTheHooksAreOff(t *testing.T) {
	t.Run("claude shared true marks the managed and a local hook", func(t *testing.T) {
		root := t.TempDir()
		setClaudeKey(t, root, approve.ClaudeSettings, "disableAllHooks", "true")
		writeRel(t, root, claudeLocalSettings, bashHook("/home/me/bin/seamark gate --hook"))

		plan := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})

		assert.Equal(t, []GateHook{
			{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true, HooksOff: true},
			{Path: claudeLocalSettings, Mode: hooks.ModeWarn, HooksOff: true},
		}, plan.GateHooks)
	})

	t.Run("claude local false over shared true marks none", func(t *testing.T) {
		root := t.TempDir()
		setClaudeKey(t, root, approve.ClaudeSettings, "disableAllHooks", "true")
		setClaudeKey(t, root, claudeLocalSettings, "disableAllHooks", "false")

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true}}, plan.GateHooks)
	})

	t.Run("claude without the key marks none", func(t *testing.T) {
		plan := planClaude(t, t.TempDir(), ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true}}, plan.GateHooks)
	})

	t.Run("codex hooks false marks the codex hooks", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[features]\nhooks = false\n")

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{{Path: codexHooksFile, Mode: hooks.ModeWarn, Managed: true, HooksOff: true}}, plan.GateHooks)
	})

	t.Run("codex without the key marks none", func(t *testing.T) {
		plan := planCodex(t, t.TempDir(), ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{{Path: codexHooksFile, Mode: hooks.ModeWarn, Managed: true}}, plan.GateHooks)
	})
}

func TestInspectionsMarkTheHooksOff(t *testing.T) {
	// The inspection takes the finding of the warning, so status and the
	// warning describe one state.
	root := t.TempDir()
	applyLessonSetupWithBinary(t, root, testBinary,
		ClientSetup{ClientID: ClaudeID, Hooks: true}, ClientSetup{ClientID: CodexID, Hooks: true})

	assert.False(t, claudeSetup{}.Inspect(root).HooksOff)
	assert.False(t, codexSetup{}.Inspect(root).HooksOff)

	setClaudeKey(t, root, approve.ClaudeSettings, "disableAllHooks", "true")
	writeRel(t, root, approve.CodexConfig, "[features]\nhooks = false\n")

	claude, codex := claudeSetup{}.Inspect(root), codexSetup{}.Inspect(root)
	require.Len(t, findingsWith(claude.Findings, disabledReason), 1)
	assert.True(t, claude.HooksOff)
	assert.True(t, codex.HooksOff)
}
