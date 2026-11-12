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

func TestAnIdleGateBesideARunningManagedGateNamesItsRemoval(t *testing.T) {
	// The idle handler sits beside a managed handler that already runs for
	// Bash. A matcher edit would start a second gate handler, so the route
	// removes the idle one.
	codexRemoval := "remove that handler from .codex/hooks.json"

	t.Run("codex inspection", func(t *testing.T) {
		root := t.TempDir()
		codexHooksWith(t, root, hookEntry("ash", codexWarnGate), hookEntry("Bash", codexWarnGate))

		insp := codexSetup{}.Inspect(root)

		assert.Equal(t, StateCurrent, entryOf(t, insp, CapabilityCommands).State)
		assert.Equal(t, hooks.ModeWarn, insp.GateMode)
		assert.Empty(t, warningsWith(insp.Findings, `set the matcher of that hook to "Bash" in .codex/hooks.json`))

		found := warningsWith(insp.Findings, codexRemoval)
		require.Len(t, found, 1)
		assert.Equal(t, codexHooksFile, found[0].Path)
		assert.Equal(t, "has `"+codexWarnGate+"` under PreToolUse \"ash\", which never runs for Bash; "+
			"another handler of the managed gate hook already runs for Bash", found[0].Reason)
	})

	t.Run("codex plan equals the inspection after the write", func(t *testing.T) {
		root := t.TempDir()
		codexHooksWith(t, root, hookEntry("ash", codexWarnGate), hookEntry("Bash", claudeFormWarn))

		plan := planCodex(t, root, ClientSetup{Hooks: true})
		planned := warningsWith(plan.Findings, codexRemoval)
		require.Len(t, planned, 1)

		_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
			{ClientID: CodexID, Hooks: true},
		}}), ApplyOptions{})
		require.NoError(t, err)

		assert.Equal(t, planned, warningsWith(codexSetup{}.Inspect(root).Findings, codexRemoval))
	})

	t.Run("claude", func(t *testing.T) {
		claudeRemoval := "remove that handler from .claude/settings.json"
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`,`+
			hookEntry("Bash", warnGateCommand)+`,`+hookEntry("Edit|Write|MultiEdit", testBinary+" "+hooks.LessonsMarker)+`]}}`)

		plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
		require.NoError(t, err)

		insp := claudeSetup{}.Inspect(root)

		for _, findings := range [][]Finding{plan.Findings, insp.Findings} {
			assert.Empty(t, warningsWith(findings, `set the matcher of that hook to "Bash" in .claude/settings.json`))
			assert.Len(t, warningsWith(findings, claudeRemoval), 1)
		}

		assert.Equal(t, warningsWith(plan.Findings, claudeRemoval), warningsWith(insp.Findings, claudeRemoval))

		found := warningsWith(insp.Findings, claudeRemoval)
		require.Len(t, found, 1)
		assert.Equal(t, ".claude/settings.json", found[0].Path)
		assert.Equal(t, "has `"+warnGateCommand+"` under PreToolUse \"Shell\", which never runs for Bash; "+
			"another handler of the managed gate hook already runs for Bash", found[0].Reason)
	})

	t.Run("codex plan counts the handlers that the merge repairs", func(t *testing.T) {
		// Both handlers name a binary that is missing. The merge rewrites
		// them to the running binary, so the Bash handler runs after the
		// write. A matcher edit on the ash handler would then start a second
		// gate handler.
		staleGate := absentBinary + " " + hooks.CodexGateMarker(hooks.ModeWarn)
		running := map[string]string{
			"the Codex form under Bash":       staleGate,
			"the Claude Code form under Bash": absentBinary + " " + hooks.GateMarker(hooks.ModeWarn),
		}

		for name, command := range running {
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				codexHooksWith(t, root, hookEntry("ash", staleGate), hookEntry("Bash", command))

				plan := planCodex(t, root, ClientSetup{Hooks: true})

				assert.Empty(t, warningsWith(plan.Findings, `set the matcher of that hook to "Bash" in .codex/hooks.json`))

				planned := warningsWith(plan.Findings, codexRemoval)
				require.Len(t, planned, 1)
				assert.Contains(t, planned[0].Reason, "has `"+codexWarnGate+"`", "the finding names the command of the written file")

				_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
					{ClientID: CodexID, Hooks: true},
				}}), ApplyOptions{})
				require.NoError(t, err)

				assert.Equal(t, planned, warningsWith(codexSetup{}.Inspect(root).Findings, codexRemoval))
			})
		}
	})

	t.Run("codex lessons", func(t *testing.T) {
		lessons := testBinary + " " + hooks.CodexLessonsMarker
		root := t.TempDir()
		codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", lessons))

		plan := planCodex(t, root, ClientSetup{Hooks: true})
		insp := codexSetup{}.Inspect(root)

		for _, findings := range [][]Finding{plan.Findings, insp.Findings} {
			assert.Empty(t, warningsWith(findings, `set the matcher of that hook to "apply_patch" in .codex/hooks.json`))

			found := warningsWith(findings, codexRemoval)
			require.Len(t, found, 1)
			assert.Equal(t, "has `"+lessons+"` under PreToolUse \"Bash\", which never runs for apply_patch; "+
				"another handler of the managed lessons hook already runs for apply_patch", found[0].Reason)
		}
	})

	t.Run("a finding of an earlier handler stays", func(t *testing.T) {
		// The idle handler is not the first one with a finding, so the
		// removal must replace its own finding and keep the earlier one.
		background := `{"matcher":"Bash","hooks":[{"type":"command","command":"` + codexWarnGate + `","async":true}]}`
		root := t.TempDir()
		codexHooksWith(t, root, background, hookEntry("ash", codexWarnGate))

		insp := codexSetup{}.Inspect(root)

		assert.Len(t, findingsWith(insp.Findings, "so the client runs the seamark gate hook in the background"), 1)

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		for _, findings := range [][]Finding{plan.Findings, insp.Findings} {
			assert.Empty(t, warningsWith(findings, `set the matcher of that hook to "Bash" in .codex/hooks.json`))
			assert.Len(t, warningsWith(findings, codexRemoval), 1)
		}
	})

	t.Run("terminal control bytes", func(t *testing.T) {
		// The matcher and the command hold escape bytes. The finding keeps
		// the text and drops the bytes, so it cannot drive the terminal.
		hostile := hookEntry(`a\u001b[31mX`, `/opt/a\u001bX/seamark `+hooks.CodexGateMarker(hooks.ModeWarn))
		root := t.TempDir()
		codexHooksWith(t, root, hostile, hookEntry("Bash", codexWarnGate))

		found := warningsWith(codexSetup{}.Inspect(root).Findings, codexRemoval)
		require.Len(t, found, 1)
		assert.Equal(t, "has `/opt/aX/seamark "+hooks.CodexGateMarker(hooks.ModeWarn)+"` under PreToolUse \"a[31mX\", "+
			"which never runs for Bash; another handler of the managed gate hook already runs for Bash", found[0].Reason)
	})

	t.Run("claude lessons handlers that run for every tool together", func(t *testing.T) {
		claudeRemoval := "remove that handler from .claude/settings.json"
		lessons := testBinary + " " + hooks.LessonsMarker
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Bash", warnGateCommand)+`,`+
			hookEntry("Foo", lessons)+`,`+hookEntry("Edit", lessons)+`,`+hookEntry("Write|MultiEdit", lessons)+`]}}`)

		plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
		require.NoError(t, err)

		for _, findings := range [][]Finding{plan.Findings, claudeSetup{}.Inspect(root).Findings} {
			found := warningsWith(findings, claudeRemoval)
			require.Len(t, found, 1)
			assert.Equal(t, "has `"+lessons+"` under PreToolUse \"Foo\", which never runs for Edit, Write, MultiEdit; "+
				"another handler of the managed lessons hook already runs for Edit, Write, MultiEdit", found[0].Reason)
		}
	})
}

func TestAnIdleHandlerKeepsTheMatcherRouteWithoutAFullManagedCover(t *testing.T) {
	// The removal route needs managed handlers that certainly run the hook
	// for each of its tools. Without them, the idle handler keeps the
	// matcher advice.
	t.Run("a wrapper in the managed document", func(t *testing.T) {
		// Setup does not manage the wrapper, so the route keeps the managed
		// handler and another finding names the wrapper.
		root := t.TempDir()
		codexHooksWith(t, root, hookEntry("ash", codexWarnGate), hookEntry("Bash", "sh -c '"+codexWarnGate+"'"))

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		for _, findings := range [][]Finding{plan.Findings, codexSetup{}.Inspect(root).Findings} {
			assert.Len(t, warningsWith(findings, `set the matcher of that hook to "Bash" in .codex/hooks.json`), 1)
			assert.Empty(t, warningsWith(findings, "remove that handler from .codex/hooks.json"))
		}
	})

	t.Run("a managed handler with an if filter", func(t *testing.T) {
		// The filter makes the running handler uncertain, so it proves no
		// coverage.
		filtered := `{"matcher":"Bash","hooks":[{"type":"command","command":"` + warnGateCommand + `","if":"Bash(git *)"}]}`
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Shell", warnGateCommand)+`,`+
			filtered+`,`+hookEntry("Edit|Write|MultiEdit", testBinary+" "+hooks.LessonsMarker)+`]}}`)

		plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
		require.NoError(t, err)

		for _, findings := range [][]Finding{plan.Findings, claudeSetup{}.Inspect(root).Findings} {
			assert.Len(t, warningsWith(findings, `set the matcher of that hook to "Bash" in .claude/settings.json`), 1)
			assert.Empty(t, warningsWith(findings, "remove that handler from .claude/settings.json"))
		}
	})

	t.Run("claude lessons handlers that run for some tools", func(t *testing.T) {
		// A removal would leave Write and MultiEdit without the hook.
		lessons := testBinary + " " + hooks.LessonsMarker
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+hookEntry("Bash", warnGateCommand)+`,`+
			hookEntry("Foo", lessons)+`,`+hookEntry("Edit", lessons)+`]}}`)

		plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
		require.NoError(t, err)

		for _, findings := range [][]Finding{plan.Findings, claudeSetup{}.Inspect(root).Findings} {
			assert.Len(t, warningsWith(findings, `set the matcher of that hook to "Edit|Write|MultiEdit" in .claude/settings.json`), 1)
			assert.Empty(t, warningsWith(findings, "remove that handler from .claude/settings.json"))
		}
	})
}
