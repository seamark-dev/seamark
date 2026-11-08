package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// warningsWith keeps the warnings whose action is action.
func warningsWith(findings []Finding, action string) []Finding {
	var out []Finding

	for _, f := range findings {
		if f.Level == FindingWarning && f.Action == action {
			out = append(out, f)
		}
	}

	return out
}

func TestSetupNamesTheMatcherOfAManagedGateThatNeverFires(t *testing.T) {
	claudeAction := `set the matcher of that hook to "Bash" in .claude/settings.json`

	t.Run("plain claude init", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`]}}`)

		plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
		require.NoError(t, err)

		found := warningsWith(plan.Findings, claudeAction)
		require.Len(t, found, 1)
		assert.Equal(t, ".claude/settings.json", found[0].Path)
		assert.Contains(t, found[0].Reason, "has `"+warnGateCommand+"` under PreToolUse \"Shell\", which never runs for Bash")
		assert.Contains(t, found[0].Reason, "setup does not change the matcher of an existing hook")
		assert.Empty(t, findingsWith(plan.Findings, "it is not a handler of that hook"), "the warning replaces the info")
	})

	t.Run("explicit claude init", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`]}}`)

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Len(t, warningsWith(plan.Findings, claudeAction), 1)
		assert.Empty(t, findingsWith(plan.Findings, "no hook runs `seamark gate --hook`"), "no second coverage warning")
		assert.Empty(t, warningsWith(plan.Findings, `set the hook's matcher to "Bash"`))
	})

	t.Run("codex init", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+hookEntry("ash", codexWarnGate)+`]}}`)

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		found := warningsWith(plan.Findings, `set the matcher of that hook to "Bash" in .codex/hooks.json`)
		require.Len(t, found, 1)
		assert.Contains(t, found[0].Reason, `under PreToolUse "ash", which never runs for Bash`)

		// Setup never changes the matcher.
		assert.Equal(t, []any{"ash", "apply_patch"}, matchersOf(writeFor(t, plan, codexHooksFile), "PreToolUse"))
	})
}

func TestSetupKeepsTheInfoForALessonCheckHookThatNeverFires(t *testing.T) {
	// Only the lifecycle hooks get the matcher warning. A lesson-check hook
	// keeps the information finding that it is no handler of its hook.
	t.Run("claude", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+
			hookEntry("Read", testBinary+" "+hooks.LessonGuardMarker)+`]}}`)

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Len(t, findingsWith(plan.Findings, "it is not a handler of that hook"), 1)
		assert.Empty(t, findingsWith(plan.Findings, "setup does not change the matcher of an existing hook"))
	})

	t.Run("codex", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+
			hookEntry("Read", testBinary+" "+hooks.CodexLessonGuardMarker)+`]}}`)

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		assert.Len(t, findingsWith(plan.Findings, "it is not a handler of that hook"), 1)
		assert.Empty(t, findingsWith(plan.Findings, "setup does not change the matcher of an existing hook"))
	})
}

func TestInspectionNamesTheMatcherOfAManagedHookThatNeverFires(t *testing.T) {
	t.Run("claude gate", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`,`+
			hookEntry("Edit|Write|MultiEdit", testBinary+" "+hooks.LessonsMarker)+`]}}`)

		insp := claudeSetup{}.Inspect(root)
		commands := entryOf(t, insp, CapabilityCommands)

		assert.Equal(t, StatePartial, commands.State)
		assert.Equal(t, `gate hook never runs from .claude/settings.json: its matcher "Shell" does not fire for Bash`, commands.Detail)
		assert.Equal(t, `set the matcher of that hook to "Bash" in .claude/settings.json`, commands.Action)
		assert.Len(t, warningsWith(insp.Findings, commands.Action), 1)
	})

	t.Run("codex gate", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+hookEntry("ash", codexWarnGate)+`,`+
			hookEntry("apply_patch", testBinary+" "+hooks.CodexLessonsMarker)+`]}}`)

		insp := codexSetup{}.Inspect(root)
		commands := entryOf(t, insp, CapabilityCommands)

		assert.Equal(t, StatePartial, commands.State)
		assert.Contains(t, commands.Detail, `its matcher "ash" does not fire for Bash`)
		assert.Equal(t, `set the matcher of that hook to "Bash" in .codex/hooks.json`, commands.Action)
	})

	t.Run("another definition runs the hook", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`]}}`)
		writeRel(t, root, claudeLocalSettings, bashHook(enforceGateCommand))

		insp := claudeSetup{}.Inspect(root)

		assert.Equal(t, StateCurrent, entryOf(t, insp, CapabilityCommands).State)
		assert.Equal(t, hooks.ModeEnforce, insp.GateMode)
		assert.Len(t, warningsWith(insp.Findings, `set the matcher of that hook to "Bash" in .claude/settings.json`), 1)
	})

	t.Run("an entry of another type keeps the info", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"prompt","command":"`+warnGateCommand+`"}]}]}}`)

		insp := claudeSetup{}.Inspect(root)

		assert.Equal(t, StateAbsent, entryOf(t, insp, CapabilityCommands).State)
		assert.Len(t, findingsWith(insp.Findings, "it is not a handler of that hook"), 1)
	})
}
