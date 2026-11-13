package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// These tests cover the execution fields of a hook handler: the keys
// that run a hook in the background, and the Claude Code if filter. A
// hook in the background cannot block, and a filtered hook covers only
// the calls that match.

// entryWith returns one hook document with a single command handler
// under the event and the matcher. extra holds more handler fields as
// raw JSON, each with a leading comma.
func entryWith(event, matcher, command, extra string) string {
	m := ""
	if matcher != "" {
		m = `"matcher":"` + matcher + `",`
	}

	return `{"hooks":{"` + event + `":[{` + m + `"hooks":[{"type":"command","command":"` + command + `"` + extra + `}]}]}}`
}

// gateWith returns a hook document with one Bash gate command.
func gateWith(command, extra string) string {
	return entryWith("PreToolUse", "Bash", command, extra)
}

// The owned Codex gate commands of the test binary.
var (
	codexEnforceGate = testBinary + " " + hooks.CodexGateMarker(hooks.ModeEnforce)
	codexWarnGate    = testBinary + " " + hooks.CodexGateMarker(hooks.ModeWarn)
)

// handlerOf returns the first handler of the event in a planned JSON
// write.
func handlerOf(t *testing.T, plan ClientPlan, rel, event string) map[string]any {
	t.Helper()

	document := writeFor(t, plan, rel)
	entries, _ := document["hooks"].(map[string]any)[event].([]any)
	require.NotEmpty(t, entries)

	handlers, _ := entries[0].(map[string]any)["hooks"].([]any)
	require.NotEmpty(t, handlers)

	handler, ok := handlers[0].(map[string]any)
	require.True(t, ok)

	return handler
}

func TestClaudeEnforceRemovesABackgroundKeyFromTheOwnedGate(t *testing.T) {
	for _, key := range []string{"async", "asyncRewake"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, approve.ClaudeSettings, gateWith(warnGateCommand, `,"`+key+`":true`))

			plan := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})

			gate := handlerOf(t, plan, approve.ClaudeSettings, "PreToolUse")
			assert.Equal(t, enforceGateCommand, gate["command"])
			assert.NotContains(t, gate, key)
			assert.Equal(t, []GateHook{{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true}}, plan.GateHooks)

			removed := findingsWith(plan.Findings, "removed `"+key+": true` from the seamark gate hook under PreToolUse")
			require.Len(t, removed, 1)
			assert.Equal(t, FindingWarning, removed[0].Level, "a plain init prints warnings only")
			assert.Equal(t, approve.ClaudeSettings, removed[0].Path)
			assert.Contains(t, removed[0].Reason, "a hook in the background cannot block")
			assert.NotContains(t, removed[0].Reason, "/hooks", "Claude Code has no trust review")
		})
	}
}

func TestCodexEnforceRemovesAsyncFromTheOwnedGate(t *testing.T) {
	// Before the fix, the plan kept async and listed a managed enforce
	// gate, while Codex never waited for its verdict.
	root := t.TempDir()
	writeRel(t, root, codexHooksFile, gateWith(codexWarnGate, `,"async":true`))

	plan := planCodex(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})

	gate := handlerOf(t, plan, codexHooksFile, "PreToolUse")
	assert.Equal(t, codexEnforceGate, gate["command"])
	assert.NotContains(t, gate, "async")
	assert.Equal(t, []GateHook{{Path: codexHooksFile, Mode: hooks.ModeEnforce, Managed: true}}, plan.GateHooks)

	removed := findingsWith(plan.Findings, "removed `async: true` from the seamark gate hook")
	require.Len(t, removed, 1)
	assert.Equal(t, FindingWarning, removed[0].Level)
	assert.Contains(t, removed[0].Reason, "Codex asks you to review that hook again in /hooks")
	assert.Contains(t, plan.Findings, codexTrustFinding)

	// The plan reads the gate before the merge. The removal finding
	// replaces the warning about the key, which would ask for this run.
	assert.Empty(t, findingsWith(plan.Findings, "with `async: true`"))

	// Codex ignores asyncRewake, so setup leaves it.
	root = t.TempDir()
	writeRel(t, root, codexHooksFile, gateWith(codexEnforceGate, `,"timeout":15,`+
		`"statusMessage":"seamark gate: classifying command","asyncRewake":true`))

	plan = planCodex(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, findingsWith(plan.Findings, "removed `"))
	assert.Contains(t, string(plan.Writes[0].After), `"asyncRewake": true`)
}

func TestAnOwnedGateInTheBackgroundReadsReportOnlyUntilSetupRuns(t *testing.T) {
	// A hand edit after setup puts the owned gate in the background.
	// Inspection reads report-only, and the managed mode stays the marker
	// mode, because the next setup run keeps it and removes the key.
	for name, tc := range map[string]struct {
		setup             SetupAdapter
		rel, command, key string
		plan              func(*testing.T, string, ClientSetup) ClientPlan
	}{
		"claude async":       {claudeSetup{}, approve.ClaudeSettings, enforceGateCommand, "async", planClaude},
		"claude asyncRewake": {claudeSetup{}, approve.ClaudeSettings, enforceGateCommand, "asyncRewake", planClaude},
		"codex async":        {codexSetup{}, codexHooksFile, codexEnforceGate, "async", planCodex},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, tc.rel, gateWith(tc.command, `,"`+tc.key+`":true`))

			insp := tc.setup.Inspect(root)
			commands, _ := insp.Entry(CapabilityCommands)
			assert.Equal(t, "gate hook (report-only) installed", commands.Detail)
			assert.Equal(t, GateModeReportOnly, insp.GateMode)
			assert.True(t, insp.GateBackground)
			assert.Equal(t, hooks.ModeEnforce, insp.ManagedGateMode)
			assert.Equal(t, insp.ManagedGateMode, tc.setup.ManagedGateMode(root))

			background := findingsWith(insp.Findings, "with `"+tc.key+": true`")
			require.Len(t, background, 1)
			assert.Equal(t, FindingWarning, background[0].Level)
			assert.Contains(t, background[0].Reason, "no verdict blocks")
			assert.Contains(t, background[0].Action, "seamark init --client ")

			// A plain re-run keeps the enforce mode and removes the key.
			plan := tc.plan(t, root, ClientSetup{Hooks: true})

			gate := handlerOf(t, plan, tc.rel, "PreToolUse")
			assert.Equal(t, tc.command, gate["command"], "the run keeps --enforce")
			assert.NotContains(t, gate, tc.key)
			assert.Len(t, findingsWith(plan.Findings, "removed `"+tc.key+": true`"), 1)
			assert.Empty(t, findingsWith(plan.Findings, "with `"+tc.key+": true`"), "the run removes the key")
			assert.Contains(t, plan.GateHooks, GateHook{Path: tc.rel, Mode: hooks.ModeEnforce, Managed: true})
			assert.NotContains(t, narrated(t, plan, tc.rel, OpPlanned), "--enforce flag", "no enforce removal")
		})
	}
}

func TestClaudeLocalGateInTheBackgroundIsReportOnly(t *testing.T) {
	for _, key := range []string{"async", "asyncRewake"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, claudeLocalSettings, gateWith("timeout 10 "+enforceGateCommand, `,"`+key+`":true`))

			plan := planClaude(t, root, ClientSetup{Hooks: true})

			assert.Equal(t, []GateHook{
				{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true},
				{Path: claudeLocalSettings, Mode: GateModeReportOnly, Background: true},
			}, plan.GateHooks)

			background := findingsWith(plan.Findings, "with `"+key+": true`")
			require.Len(t, background, 1)
			assert.Equal(t, claudeLocalSettings, background[0].Path)
			assert.Equal(t, "remove `"+key+"` from that definition", background[0].Action)
		})
	}
}

func TestCodexGateInTheBackgroundIsReportOnly(t *testing.T) {
	wrapper := "timeout 10 " + codexEnforceGate

	t.Run("a wrapper in hooks.json under an enforce request", func(t *testing.T) {
		// Before the fix, setup read the wrapper as enforce and installed
		// no gate of its own, so nothing gated the shell.
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, gateWith(wrapper, `,"async":true`))

		plan := planCodex(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})

		assert.Equal(t, []GateHook{
			{Path: codexHooksFile, Mode: GateModeReportOnly, Background: true},
			{Path: codexHooksFile, Mode: hooks.ModeEnforce, Managed: true},
		}, plan.GateHooks)

		asked := findingsWith(plan.Findings, "the run asked for enforce mode")
		require.Len(t, asked, 1)
		assert.Contains(t, asked[0].Reason, "which runs in the background")
		assert.NotContains(t, asked[0].Reason, "discards")
		assert.Len(t, findingsWith(plan.Findings, "with `async: true`"), 1)
		assert.Contains(t, string(plan.Writes[0].After), `"async": true`, "setup edits no wrapper")
	})

	t.Run("a wrapper without a gate mode request", func(t *testing.T) {
		// As for a wrapper that discards its exit status, setup installs no
		// second gate.
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, gateWith(wrapper, `,"async":true`))

		plan := planCodex(t, root, ClientSetup{Hooks: true})
		assert.Equal(t, []GateHook{{Path: codexHooksFile, Mode: GateModeReportOnly, Background: true}}, plan.GateHooks)
	})

	t.Run("inline in config.toml", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \""+codexEnforceGate+"\"\nasync = true\n")

		plan := planCodex(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})

		assert.Equal(t, []GateHook{
			{Path: codexInlineHooksPath, Mode: GateModeReportOnly, Background: true},
			{Path: codexHooksFile, Mode: hooks.ModeEnforce, Managed: true},
		}, plan.GateHooks)

		background := findingsWith(plan.Findings, "with `async: true`")
		require.Len(t, background, 1)
		assert.Equal(t, codexInlineHooksPath, background[0].Path)
	})
}

func TestClaudeFilteredGateIsUncertain(t *testing.T) {
	t.Run("a local gate", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, `,"if":"Bash(rm *)"`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{
			{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true},
			{Path: claudeLocalSettings, Mode: hooks.ModeEnforce, Uncertain: true, Filtered: true},
		}, plan.GateHooks)

		filter := findingsWith(plan.Findings, "with the if filter `Bash(rm *)`")
		require.Len(t, filter, 1)
		assert.Equal(t, claudeLocalSettings, filter[0].Path)
		assert.Equal(t, "remove the `if` field, or remove the definition, so setup can tell", filter[0].Action)
		assert.Empty(t, findingsWith(plan.Findings, "both apply"))
	})

	t.Run("the owned gate", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(warnGateCommand, `,"if":"Bash(git *)"`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true, Uncertain: true, Filtered: true}},
			plan.GateHooks)
		assert.Equal(t, "Bash(git *)", handlerOf(t, plan, approve.ClaudeSettings, "PreToolUse")["if"], "setup keeps the field")

		filter := findingsWith(plan.Findings, "with the if filter `Bash(git *)`")
		require.Len(t, filter, 1)
		assert.Equal(t, approve.ClaudeSettings, filter[0].Path)

		insp := claudeSetup{}.Inspect(root)
		assert.Empty(t, insp.GateMode, "no gate certainly runs")
		assert.Equal(t, hooks.ModeWarn, insp.PossibleGateMode)

		// The filter is a field of the definition, so the action names it.
		commands, _ := insp.Entry(CapabilityCommands)
		assert.Equal(t, StatePartial, commands.State)
		assert.Equal(t, "remove the `if` field from that definition, or remove the definition, so setup can tell",
			commands.Action)
	})

	t.Run("a filtered wrapper is not named where the gate runs", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith("timeout 5 /opt/bin/seamark gate --hook", `,"if":"Bash(rm *)"`))
		writeRel(t, root, claudeLocalSettings, gateWith("timeout 10 /opt/bin/seamark gate --hook", ""))

		commands, _ := claudeSetup{}.Inspect(root).Entry(CapabilityCommands)
		assert.Equal(t, "gate hook (warn) runs from .claude/settings.local.json (not managed by setup)", commands.Detail)
	})

	t.Run("a filtered hook that discards its status beside a gate in the background", func(t *testing.T) {
		// The filtered hook only can run, so the cause of the report-only
		// mode is the background key of the certain one.
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, `,"async":true`))
		writeRel(t, root, claudeLocalSettings, gateWith("timeout 5 /opt/bin/seamark gate --hook || true", `,"if":"Bash(rm *)"`))

		insp := claudeSetup{}.Inspect(root)
		assert.Equal(t, GateModeReportOnly, insp.GateMode)
		assert.True(t, insp.GateBackground)
	})

	t.Run("a Codex entry with if", func(t *testing.T) {
		// Codex has no if field, so the entry reads as it did.
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, gateWith("timeout 5 "+codexEnforceGate, `,"if":"Bash(rm *)"`))

		plan := planCodex(t, root, ClientSetup{Hooks: true})
		assert.Equal(t, []GateHook{{Path: codexHooksFile, Mode: hooks.ModeEnforce}}, plan.GateHooks)
		assert.Empty(t, findingsWith(plan.Findings, "if filter"))
	})
}

func TestClaudeLocalGateCopyRunsInPlaceOfTheManagedGate(t *testing.T) {
	// Claude Code keys a handler by type, shell, command, args, and if.
	// A local copy that differs only in other fields runs in place of the
	// managed gate.
	managed := `,"timeout":15,"statusMessage":"seamark gate: classifying command"`

	t.Run("a copy in the background", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
		writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, managed+`,"async":true`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Equal(t, []GateHook{
			{Path: approve.ClaudeSettings, Mode: GateModeReportOnly, Managed: true, Background: true},
			{Path: claudeLocalSettings, Mode: GateModeReportOnly, Background: true},
		}, plan.GateHooks)

		copies := findingsWith(plan.Findings, "has a copy of the managed gate hook")
		require.Len(t, copies, 1)
		assert.Equal(t, claudeLocalSettings, copies[0].Path)
		assert.Contains(t, copies[0].Reason, "differs in async")
		assert.Contains(t, copies[0].Reason, "so no verdict blocks")

		insp := claudeSetup{}.Inspect(root)
		assert.Equal(t, GateModeReportOnly, insp.GateMode)
		assert.True(t, insp.GateBackground)
		assert.Equal(t, hooks.ModeEnforce, insp.ManagedGateMode)
	})

	t.Run("a copy with another timeout", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
		writeRel(t, root, claudeLocalSettings,
			gateWith(enforceGateCommand, `,"timeout":600,"statusMessage":"seamark gate: classifying command"`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Contains(t, plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true})

		copies := findingsWith(plan.Findings, "has a copy of the managed gate hook")
		require.Len(t, copies, 1)
		assert.Contains(t, copies[0].Reason, "differs in timeout")
		assert.NotContains(t, copies[0].Reason, "no verdict blocks")
	})

	t.Run("a copy with asyncRewake", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
		writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, managed+`,"asyncRewake":true`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Contains(t, plan.GateHooks,
			GateHook{Path: approve.ClaudeSettings, Mode: GateModeReportOnly, Managed: true, Background: true})

		copies := findingsWith(plan.Findings, "has a copy of the managed gate hook")
		require.Len(t, copies, 1)
		assert.Contains(t, copies[0].Reason, "differs in asyncRewake")
		assert.Contains(t, copies[0].Reason, "that copy has `asyncRewake: true`, so no verdict blocks")
	})

	t.Run("a copy with another onFailure", func(t *testing.T) {
		// Exit status 2 blocks whatever onFailure says, so the mode stays.
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
		writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, managed+`,"onFailure":"block"`))

		plan := planClaude(t, root, ClientSetup{Hooks: true})

		assert.Contains(t, plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true})

		copies := findingsWith(plan.Findings, "has a copy of the managed gate hook")
		require.Len(t, copies, 1)
		assert.Contains(t, copies[0].Reason, "differs in onFailure")
		assert.NotContains(t, copies[0].Reason, "no verdict blocks")
	})

	// A background key with the value false leaves a handler in the
	// foreground, as an absent key does. Setup keeps such a key as it is,
	// so a false key on either copy must not make the copies differ.
	for _, key := range hooks.ClaudeBackgroundKeys {
		falseKey := `,"` + key + `":false`

		for name, documents := range map[string][2]string{
			"on the copy":          {managed, managed + falseKey},
			"on the managed entry": {managed + falseKey, managed},
		} {
			t.Run("a copy with "+key+" false "+name+" is equal", func(t *testing.T) {
				root := t.TempDir()
				writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, documents[0]))
				writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, documents[1]))

				plan := planClaude(t, root, ClientSetup{Hooks: true})
				assert.Empty(t, findingsWith(plan.Findings, "has a copy of the managed gate hook"))
				assert.Contains(t, plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true})

				insp := claudeSetup{}.Inspect(root)
				assert.Empty(t, findingsWith(insp.Findings, "has a copy of the managed gate hook"))
				assert.Equal(t, hooks.ModeEnforce, insp.GateMode)
			})
		}

		t.Run("a copy with "+key+" false and another timeout names the timeout only", func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
			writeRel(t, root, claudeLocalSettings,
				gateWith(enforceGateCommand, `,"timeout":600,"statusMessage":"seamark gate: classifying command"`+falseKey))

			for _, findings := range [][]Finding{
				planClaude(t, root, ClientSetup{Hooks: true}).Findings,
				claudeSetup{}.Inspect(root).Findings,
			} {
				copies := findingsWith(findings, "has a copy of the managed gate hook")
				require.Len(t, copies, 1)
				assert.Contains(t, copies[0].Reason, "differs in timeout;")
				assert.NotContains(t, copies[0].Reason, key)
			}
		})
	}

	// The if, shell, and args fields are in the handler key, so a copy
	// that differs in one of them is another handler. Claude Code runs it
	// beside the managed gate, and the managed gate keeps its mode.
	for name, extra := range map[string]string{
		"if":    `,"if":"Bash(rm *)"`,
		"shell": `,"shell":"powershell"`,
		"args":  `,"args":["--verbose"]`,
	} {
		t.Run("a copy with another "+name+" is another handler", func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, approve.ClaudeSettings, gateWith(enforceGateCommand, managed))
			writeRel(t, root, claudeLocalSettings, gateWith(enforceGateCommand, managed+extra+`,"async":true`))

			plan := planClaude(t, root, ClientSetup{Hooks: true})
			assert.Empty(t, findingsWith(plan.Findings, "has a copy of the managed gate hook"))
			assert.Contains(t, plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: hooks.ModeEnforce, Managed: true})
		})
	}
}

func TestOnFailureNeverChangesTheGateMode(t *testing.T) {
	// A deny exits with status 2, which blocks whatever onFailure says.
	for _, value := range []string{``, `,"onFailure":"continue"`, `,"onFailure":"block"`} {
		root := t.TempDir()
		writeRel(t, root, claudeLocalSettings, gateWith("timeout 5 "+enforceGateCommand, value))

		plan := planClaude(t, root, ClientSetup{Hooks: true})
		assert.Contains(t, plan.GateHooks, GateHook{Path: claudeLocalSettings, Mode: hooks.ModeEnforce}, value)
		assert.Empty(t, findingsWith(plan.Findings, "if filter"), value)
		assert.Empty(t, findingsWith(plan.Findings, "in the background"), value)
	}
}

// lessonDocument returns a hook document with the owned lesson-check
// hooks of the client in enforce mode. guardExtra and verifyExtra hold
// more handler fields of the guard and of the verify hooks.
func lessonDocument(clientID, guardExtra, verifyExtra string) string {
	client := lessonHookClients[clientID]
	specs := client.specs(hooks.LessonChecksEnforce)

	entries := map[string][]string{}
	order := []string{}

	for i, spec := range specs {
		extra := verifyExtra
		if i == 0 {
			extra = guardExtra
		}

		if _, ok := entries[spec.Event]; !ok {
			order = append(order, spec.Event)
		}

		entries[spec.Event] = append(entries[spec.Event], `{"matcher":"`+spec.Matcher+`","hooks":[{"type":"command",`+
			`"command":"`+spec.Command(testBinary)+`","timeout":`+map[bool]string{true: "10", false: "15"}[i == 0]+extra+`}]}`)
	}

	var events []string
	for _, event := range order {
		events = append(events, `"`+event+`":[`+strings.Join(entries[event], ",")+`]`)
	}

	return `{"hooks":{` + strings.Join(events, ",") + `}}`
}

// lessonDocumentPath is the hook document of the client's lesson hooks.
var lessonDocumentPath = map[string]string{ClaudeID: approve.ClaudeSettings, CodexID: codexHooksFile}

func TestEnforceRemovesABackgroundKeyFromTheOwnedGuard(t *testing.T) {
	for _, clientID := range []string{ClaudeID, CodexID} {
		t.Run(clientID, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, lessonDocumentPath[clientID], lessonDocument(clientID, `,"async":true`, ""))

			out, findings := applyLessonSetup(t, root, ClientSetup{
				ClientID: clientID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
			})

			assert.NotContains(t, out, "report-only-wrapper")
			assert.NotContains(t, readRel(t, root, lessonDocumentPath[clientID]), `"async"`)

			removed := findingsWith(findings, "removed `async: true` from the seamark lesson guard hook under PreToolUse")
			require.Len(t, removed, 1)
			assert.Equal(t, clientID == CodexID, strings.Contains(removed[0].Reason, "/hooks"))
		})
	}

	// Check mode removes the key from the owned baseline too: the
	// baseline must exist before the write.
	root := t.TempDir()
	writeRel(t, root, approve.ClaudeSettings, entryWith("PreToolUse", "Edit|Write|MultiEdit",
		testBinary+" "+hooks.LessonBaselineMarker, `,"async":true`))

	out, findings := applyLessonSetup(t, root, ClientSetup{
		ClientID: ClaudeID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksCheck,
	})

	assert.NotContains(t, readRel(t, root, approve.ClaudeSettings), `"async"`)
	assert.Len(t, findingsWith(findings, "removed `async: true` from the seamark lesson baseline hook"), 1)
	assert.NotContains(t, out, "report-only-wrapper", "the summary does not count the key that the run removes")
}

func TestEnforceIgnoresAUserHookInTheBackground(t *testing.T) {
	// A hook that does not run the pre-write hook blocks no write in any
	// case, so its background key keeps nothing inactive.
	userHook := "./scripts/fmt.sh"

	t.Run("claude", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, entryWith("PreToolUse", "Edit", userHook, `,"async":true`))

		out, _ := applyLessonSetup(t, root, ClientSetup{
			ClientID: ClaudeID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
		})

		assert.NotContains(t, out, "report-only-wrapper")
		assert.Contains(t, readRel(t, root, approve.ClaudeSettings), `"async": true`, "setup edits no user hook")
	})

	t.Run("codex inline", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[[hooks.PreToolUse]]\nmatcher = \"apply_patch\"\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \""+userHook+"\"\nasync = true\n")

		out, _ := applyLessonSetup(t, root, ClientSetup{
			ClientID: CodexID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
		})

		assert.NotContains(t, out, "report-only-wrapper")
	})
}

func TestEnforceRefusesAPreWriteHookInTheBackground(t *testing.T) {
	refuse := func(t *testing.T, root, clientID, step string) {
		t.Helper()

		before := treeFiles(t, root)

		plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
			ClientID: clientID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
		}}})
		require.Error(t, err)
		assert.Nil(t, plan)
		assert.Equal(t, "lesson checks: enforce for "+clientID+" would stay inactive (report-only-wrapper); "+step+"; then rerun",
			err.Error())
		assert.Equal(t, before, treeFiles(t, root))
	}

	t.Run("a Claude local guard", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, claudeLocalSettings, entryWith("PreToolUse", "Edit|Write|MultiEdit",
			"/opt/bin/seamark "+hooks.LessonGuardMarker, `,"async":true`))

		refuse(t, root, ClaudeID, ".claude/settings.local.json: remove the definition `/opt/bin/seamark lessons --guard --hook`, "+
			"which has `async: true`, so the client runs the lesson-check pre-write hook in the background")
	})

	t.Run("a Claude wrapper in the background", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, entryWith("PreToolUse", "Edit|Write|MultiEdit",
			"timeout 5 /opt/bin/seamark "+hooks.LessonGuardMarker, `,"asyncRewake":true`))

		refuse(t, root, ClaudeID, ".claude/settings.json: remove the definition `timeout 5 /opt/bin/seamark lessons --guard --hook`, "+
			"which has `asyncRewake: true`, so the client runs the lesson-check pre-write hook in the background")
	})

	t.Run("a Codex inline guard", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[[hooks.PreToolUse]]\nmatcher = \"apply_patch\"\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"/opt/bin/seamark "+hooks.CodexLessonGuardMarker+
			"\"\nasync = true\n")

		refuse(t, root, CodexID, ".codex/config.toml [hooks]: remove the definition "+
			"`/opt/bin/seamark lessons --guard --hook --client codex`, which has `async: true`, so the client runs "+
			"the lesson-check pre-write hook in the background")
	})
}

func TestAnOwnedGuardInTheBackgroundReadsReportOnly(t *testing.T) {
	self := BinaryIdentity{Path: testBinary}

	for _, clientID := range []string{ClaudeID, CodexID} {
		t.Run(clientID, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, lessonDocumentPath[clientID], lessonDocument(clientID, `,"async":true`, ""))

			insp := inspectionOf(t, root, clientID)
			require.NotNil(t, insp.LessonHooks)
			require.Len(t, insp.LessonHooks.ReportOnly, 1)
			assert.Equal(t, lessonDocumentPath[clientID], insp.LessonHooks.ReportOnly[0].Path)
			assert.Equal(t, "async", insp.LessonHooks.ReportOnly[0].Background)
			assert.True(t, insp.LessonHooks.ReportOnly[0].Managed, "setup removes the key of the owned entry")

			for _, p := range LessonCheckPaths(insp, self) {
				if p.Stage == lessoncheck.StagePreWrite {
					assert.Contains(t, p.Reasons, lessoncheck.CapReportOnlyWrapper, pathKey(p))
				}
			}

			background := findingsWith(insp.Findings, "seamark lesson guard hook in the background")
			require.Len(t, background, 1)
			assert.Equal(t, "run `seamark init --client "+clientID+"`, which removes `async` from that hook",
				background[0].Action)
		})
	}
}

func TestClaudeFilteredLessonHooks(t *testing.T) {
	self := BinaryIdentity{Path: testBinary}

	t.Run("an owned guard with if", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, lessonDocument(ClaudeID, `,"if":"Edit(src/**)"`, ""))

		insp := inspectionOf(t, root, ClaudeID)

		for _, p := range LessonCheckPaths(insp, self) {
			if p.Stage == lessoncheck.StagePreWrite {
				assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapToolUncovered}, p.Reasons, pathKey(p))
			}
		}

		filter := findingsWith(insp.Findings, "with the if filter `Edit(src/**)`")
		require.Len(t, filter, 1)
		assert.Equal(t, approve.ClaudeSettings, filter[0].Path)
	})

	t.Run("an owned guard with if under an enforce request", func(t *testing.T) {
		// Setup accepts enforce, keeps the field, and narrates the
		// uncovered paths.
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, lessonDocument(ClaudeID, `,"if":"Edit(src/**)"`, ""))

		out, findings := applyLessonSetup(t, root, ClientSetup{
			ClientID: ClaudeID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
		})

		assert.Contains(t, out, "pre-write Edit inactive (tool-uncovered)")
		assert.Contains(t, readRel(t, root, approve.ClaudeSettings), `"if": "Edit(src/**)"`)
		assert.Len(t, findingsWith(findings, "with the if filter `Edit(src/**)`"), 1)
	})

	t.Run("an owned verify hook with if", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, lessonDocument(ClaudeID, "", `,"if":"Write"`))

		for _, p := range LessonCheckPaths(inspectionOf(t, root, ClaudeID), self) {
			if p.Stage == lessoncheck.StagePostEdit {
				assert.Contains(t, p.Reasons, lessoncheck.CapToolUncovered, pathKey(p))
			}
		}
	})

	t.Run("a local guard with if", func(t *testing.T) {
		// Inspection reads the local file too, so doctor names its filter.
		root := t.TempDir()
		writeRel(t, root, claudeLocalSettings, entryWith("PreToolUse", "Edit|Write|MultiEdit",
			"/opt/bin/seamark "+hooks.LessonGuardMarker, `,"if":"Edit(src/**)"`))

		filter := findingsWith(claudeSetup{}.Inspect(root).Findings, "with the if filter `Edit(src/**)`")
		require.Len(t, filter, 1)
		assert.Equal(t, claudeLocalSettings, filter[0].Path)
	})

	t.Run("an owned exploration hook with if", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, entryWith("PostToolUse", "Read",
			testBinary+" "+hooks.LessonExploreMarker, `,"if":"Read(docs/**)"`))

		filter := findingsWith(claudeSetup{}.Inspect(root).Findings, "with the if filter `Read(docs/**)`")
		require.Len(t, filter, 1)
		assert.Equal(t, approve.ClaudeSettings, filter[0].Path)
	})

	t.Run("a local guard with if beside the managed one", func(t *testing.T) {
		// Claude Code keys a handler by its if too, so the copy runs beside
		// the managed handler. Setup refuses it as a second handler.
		root := t.TempDir()
		writeRel(t, root, claudeLocalSettings, entryWith("PreToolUse", "Edit|Write|MultiEdit",
			"/opt/bin/seamark "+hooks.LessonGuardMarker, `,"if":"Edit(src/**)"`))

		_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
			ClientID: ClaudeID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
		}}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already runs a handler from .claude/settings.local.json")
	})
}

func TestClaudeFilteredResetHookNeverRuns(t *testing.T) {
	// PostCompact carries no tool call, so Claude Code never runs a hook
	// with an if field there.
	root := t.TempDir()
	writeRel(t, root, approve.ClaudeSettings, entryWith("PostCompact", "",
		testBinary+" "+hooks.LessonsResetMarker, `,"if":"Bash"`))

	insp := claudeSetup{}.Inspect(root)
	resets, _ := insp.Entry(CapabilityResets)
	assert.Equal(t, StatePartial, resets.State, "a re-run of setup cannot repair it")
	assert.Equal(t, "context reset hook never runs from .claude/settings.json: it has an if field on PostCompact", resets.Detail)
	assert.Equal(t, "remove the `if` field from the PostCompact hook in .claude/settings.json", resets.Action)
	assert.True(t, resets.Inert, "the client never runs the hook")

	never := findingsWith(insp.Findings, "with an if field on PostCompact")
	require.Len(t, never, 1)
	assert.Equal(t, FindingWarning, never[0].Level)

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Len(t, findingsWith(plan.Findings, "with an if field on PostCompact"), 1)
	assert.Equal(t, "Bash", handlerOf(t, plan, approve.ClaudeSettings, "PostCompact")["if"], "setup keeps the field")

	// The if field stops the hook, not the matcher. So no finding asks
	// for a matcher change, and the run does not repeat a false action.
	assert.Empty(t, findingsWith(plan.Findings, "setup does not change the matcher"))
}

func TestClaudeFilteredLocalLessonsHookIsNamed(t *testing.T) {
	// The managed lessons hook runs for Edit. A local one with an if runs
	// for some Write and MultiEdit calls only, so a warning names it.
	root := t.TempDir()
	writeRel(t, root, approve.ClaudeSettings, entryWith("PreToolUse", "Edit", testBinary+" "+hooks.LessonsMarker, ""))
	writeRel(t, root, claudeLocalSettings, entryWith("PreToolUse", "Write|MultiEdit",
		testBinary+" "+hooks.LessonsMarker, `,"if":"Write(docs/**)"`))

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	filter := findingsWith(plan.Findings, "with the if filter `Write(docs/**)`")
	require.Len(t, filter, 1)
	assert.Equal(t, FindingWarning, filter[0].Level)
	assert.Equal(t, claudeLocalSettings, filter[0].Path)
	assert.False(t, slices.ContainsFunc(plan.Findings, func(f Finding) bool {
		return f.Level == FindingInfo && strings.Contains(f.Reason, "setup does not manage that definition")
	}), "the warning replaces the information line")
}

func TestBackgroundKeysOnOtherHooksKeepTheirReading(t *testing.T) {
	// A lessons, verify, or reset hook in the background delivers late,
	// and it still runs for every call. Its reading stays.
	root := t.TempDir()
	writeRel(t, root, approve.ClaudeSettings, entryWith("PostCompact", "", testBinary+" "+hooks.LessonsResetMarker,
		`,"async":true`))

	insp := claudeSetup{}.Inspect(root)
	resets, _ := insp.Entry(CapabilityResets)
	assert.Equal(t, StateCurrent, resets.State)
	assert.Empty(t, findingsWith(insp.Findings, "in the background"))

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, findingsWith(plan.Findings, "removed `"))
	assert.Equal(t, true, handlerOf(t, plan, approve.ClaudeSettings, "PostCompact")["async"])
}
