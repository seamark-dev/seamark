package integration

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

// The tests of a Claude Code handler copy. Claude Code keys a command
// handler by its type, shell, command, args, and if fields, and runs one
// handler per key. So a local copy of a managed handler runs once.
// Setup accepts it only when every other field except statusMessage is
// equal too, because Claude Code can run the fields of either copy.

// managedHandler returns a clone of the managed handler that runs the
// marker under the event in the shared settings file.
func managedHandler(t *testing.T, root, event, marker string) map[string]any {
	t.Helper()

	document := parseRel(t, root, approve.ClaudeSettings)
	entries, _ := document["hooks"].(map[string]any)[event].([]any)

	var found map[string]any

	hooks.ForEachCommand(entries, func(_ string, h map[string]any, command string) {
		if found == nil && hooks.OwnedBySeamark(command, []string{marker}) {
			found = maps.Clone(h)
		}
	})

	require.NotNil(t, found, "the shared file holds the managed %s handler", marker)

	return found
}

// copyDocument returns a hook document with one handler under the event
// and the matcher. An empty matcher writes no matcher key.
func copyDocument(t *testing.T, event, matcher string, handler map[string]any) string {
	t.Helper()

	entry := map[string]any{"hooks": []any{handler}}
	if matcher != "" {
		entry["matcher"] = matcher
	}

	data, err := json.Marshal(map[string]any{"hooks": map[string]any{event: []any{entry}}})
	require.NoError(t, err)

	return string(data)
}

// collapsedReason is the information finding for a verbatim copy.
func collapsedReason(command, name string) string {
	return "Claude Code runs `" + command + "` once, because it equals the managed " + name +
		" hook in .claude/settings.json; setup does not manage that copy"
}

// lessonCheckCopy names one managed lesson-check handler to copy.
type lessonCheckCopy struct {
	name, installed, event, marker, spec string
}

// lessonCheckCopies lists each managed lesson-check handler: the guard
// under enforce, the baseline under check, and the verify handler on
// both post-edit events.
var lessonCheckCopies = []lessonCheckCopy{
	{"a guard", hooks.LessonChecksEnforce, "PreToolUse", hooks.LessonGuardMarker, "lesson guard"},
	{"a baseline", hooks.LessonChecksCheck, "PreToolUse", hooks.LessonBaselineMarker, "lesson baseline"},
	{"a verify handler", hooks.LessonChecksCheck, "PostToolUse", hooks.LessonVerifyMarker, "lesson verify"},
	{"a failure verify handler", hooks.LessonChecksCheck, "PostToolUseFailure", hooks.LessonVerifyMarker, "lesson verify"},
}

func TestClaudeAcceptsAVerbatimLessonCheckCopy(t *testing.T) {
	runs := map[string]ClientSetup{
		"plain init":         {ClientID: ClaudeID, Hooks: true},
		"init --client":      {ClientID: ClaudeID, Hooks: true, CheckHookSources: true},
		"a gate mode change": {ClientID: ClaudeID, Hooks: true, CheckHookSources: true, GateMode: hooks.ModeEnforce},
	}

	for _, tc := range lessonCheckCopies {
		for _, matcher := range []string{"Edit|Write|MultiEdit", "Edit"} {
			for name, run := range runs {
				t.Run(tc.name+" under "+matcher+", "+name, func(t *testing.T) {
					root := t.TempDir()
					applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: tc.installed})

					handler := managedHandler(t, root, tc.event, tc.marker)
					writeRel(t, root, claudeLocalSettings, copyDocument(t, tc.event, matcher, handler))

					plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{run}})
					require.NoError(t, err, "Claude Code runs the copy once")

					collapsed := findingsWith(plan.Findings, collapsedReason(handler["command"].(string), tc.spec))
					require.Len(t, collapsed, 1)
					assert.Equal(t, FindingInfo, collapsed[0].Level)
					assert.Equal(t, claudeLocalSettings, collapsed[0].Path)
					assert.Empty(t, findingsWith(plan.Findings, "runs twice"))
				})
			}
		}
	}

	t.Run("another statusMessage", func(t *testing.T) {
		root := t.TempDir()
		applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})

		handler := managedHandler(t, root, "PreToolUse", hooks.LessonGuardMarker)
		handler["statusMessage"] = "my own spinner text"
		writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit", handler))

		plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
			{ClientID: ClaudeID, Hooks: true, CheckHookSources: true},
		}})
		require.NoError(t, err, "statusMessage sets only the spinner text")
		assert.Len(t, findingsWith(plan.Findings, collapsedReason(handler["command"].(string), "lesson guard")), 1)
	})
}

func TestClaudeRefusesALessonCheckCopyWithOtherFields(t *testing.T) {
	const tools = "Edit|Write|MultiEdit"

	edits := map[string]func(map[string]any){
		"async":          func(h map[string]any) { h["async"] = true },
		"asyncRewake":    func(h map[string]any) { h["asyncRewake"] = true },
		"timeout 600":    func(h map[string]any) { h["timeout"] = 600 },
		"no timeout":     func(h map[string]any) { delete(h, "timeout") },
		"onFailure":      func(h map[string]any) { h["onFailure"] = "block" },
		"if":             func(h map[string]any) { h["if"] = "Edit(src/**)" },
		"shell":          func(h map[string]any) { h["shell"] = "bash" },
		"args":           func(h map[string]any) { h["args"] = []any{"--quiet"} },
		"an unknown key": func(h map[string]any) { h["future"] = "value" },
		// A null value is a present key, so it is not equal to an
		// absent key, whatever Claude Code does with it.
		"a null onFailure":   func(h map[string]any) { h["onFailure"] = nil },
		"a null unknown key": func(h map[string]any) { h["future"] = nil },
		"another binary":     func(h map[string]any) { h["command"] = "/opt/bin/seamark " + hooks.LessonGuardMarker },
		"a wrapper": func(h map[string]any) {
			h["command"] = "timeout 5 " + testBinary + " " + hooks.LessonGuardMarker
		},
	}

	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})

			handler := managedHandler(t, root, "PreToolUse", hooks.LessonGuardMarker)
			edit(handler)
			writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", tools, handler))

			got := refusedLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, CheckHookSources: true})
			assert.Contains(t, got, "already runs a handler from "+claudeLocalSettings)
		})
	}

	t.Run("a guard copy after a switch to check mode", func(t *testing.T) {
		root := t.TempDir()
		applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})

		handler := managedHandler(t, root, "PreToolUse", hooks.LessonGuardMarker)
		writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", tools, handler))

		got := refusedLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksCheck})
		assert.Contains(t, got, "already runs a handler from "+claudeLocalSettings)
	})

	t.Run("a copy of the old binary path after a relocation", func(t *testing.T) {
		root := t.TempDir()
		applyLessonSetupWithBinary(t, root, "/old/bin/seamark",
			ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})

		handler := managedHandler(t, root, "PreToolUse", hooks.LessonGuardMarker)
		writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", tools, handler))

		// Merge rewrites the shared command to the new binary. The copy
		// keeps the old path, so Claude Code runs both.
		got := refusedLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, CheckHookSources: true})
		assert.Contains(t, got, "`/old/bin/seamark "+hooks.LessonGuardMarker+"`")
	})
}

func TestCodexStillRefusesAnIdenticalLessonCheckCopy(t *testing.T) {
	// Codex runs every copy, so an inline copy with the managed command
	// runs the check twice.
	root := t.TempDir()
	applyLessonSetup(t, root, ClientSetup{ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck})
	writeRel(t, root, ".codex/config.toml", "[[hooks.PostToolUse]]\nmatcher = \"apply_patch\"\n"+
		"[[hooks.PostToolUse.hooks]]\ntype = \"command\"\n"+
		"command = \""+testBinary+" "+hooks.CodexLessonVerifyMarker+"\"\n")

	got := refusedLessonSetup(t, root, ClientSetup{ClientID: CodexID, Hooks: true})
	assert.Contains(t, got, "lesson checks: codex PostToolUse")
	assert.Contains(t, got, "setup already manages this handler, so remove that definition")
}

func TestClaudeInspectionNamesAVerbatimCopyOnce(t *testing.T) {
	specs := hooks.ClaudeSpecs(hooks.ModeWarn)

	for _, spec := range specs {
		t.Run(spec.Name, func(t *testing.T) {
			root := t.TempDir()
			applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

			handler := managedHandler(t, root, spec.Event, spec.Marker)
			writeRel(t, root, claudeLocalSettings, copyDocument(t, spec.Event, spec.Matcher, handler))

			insp := claudeSetup{}.Inspect(root)
			collapsed := findingsWith(insp.Findings, collapsedReason(handler["command"].(string), spec.Name))
			require.Len(t, collapsed, 1)
			assert.Equal(t, FindingInfo, collapsed[0].Level)

			for _, f := range insp.Findings {
				assert.NotEqual(t, FindingWarning, f.Level, f.Reason)
			}

			plan := planClaude(t, root, ClientSetup{Hooks: true})
			assert.Len(t, findingsWith(plan.Findings, collapsedReason(handler["command"].(string), spec.Name)), 1)
			assert.Empty(t, findingReasons(plan, FindingWarning))
		})
	}

	t.Run("a copy with other fields runs once with the fields of one copy", func(t *testing.T) {
		root := t.TempDir()
		applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

		handler := managedHandler(t, root, "PostCompact", hooks.LessonsResetMarker)
		handler["timeout"] = 30
		writeRel(t, root, claudeLocalSettings, copyDocument(t, "PostCompact", "", handler))

		warnings := findingsWith(claudeSetup{}.Inspect(root).Findings, "differs in timeout")
		require.Len(t, warnings, 1)
		assert.Equal(t, "has a copy of the managed context reset hook of .claude/settings.json that differs in timeout; "+
			"Claude Code runs it once, with the fields of one copy that setup cannot name", warnings[0].Reason)
		assert.Equal(t, "remove that copy, or make it equal to the managed hook", warnings[0].Action)
	})
}

func TestClaudeGateCopyWithOtherFieldsIsNotSaidToRunTwice(t *testing.T) {
	root := t.TempDir()
	applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

	handler := managedHandler(t, root, "PreToolUse", hooks.GateMarker(hooks.ModeWarn))
	handler["async"] = true
	writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Bash", handler))

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Len(t, findingsWith(plan.Findings, "Claude Code runs only one of the two copies"), 1)
	assert.Empty(t, findingsWith(plan.Findings, "runs twice"))
	assert.Empty(t, findingsWith(claudeSetup{}.Inspect(root).Findings, "runs twice"))
}

func TestClaudeComparesANewManagedTimeoutByValue(t *testing.T) {
	// Merge writes the timeout of a new entry as an int. The local file
	// reads it as a float64. Both stand for one JSON number.
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, gateWith(warnGateCommand,
		`,"timeout":15,"statusMessage":"seamark gate: classifying command"`))

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Len(t, findingsWith(plan.Findings, collapsedReason(warnGateCommand, "gate")), 1)
	assert.Empty(t, findingReasons(plan, FindingWarning))
}

func TestClaudeFilteredCopyStillRunsTwiceForTheCallsItMatches(t *testing.T) {
	// An if field is part of the handler key. So Claude Code runs a
	// filtered copy beside the unfiltered managed hook.
	root := t.TempDir()
	applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

	handler := managedHandler(t, root, "PreToolUse", hooks.LessonsMarker)
	handler["if"] = "Edit(src/**)"
	writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit", handler))

	for name, findings := range map[string][]Finding{
		"plan":       planClaude(t, root, ClientSetup{Hooks: true}).Findings,
		"inspection": claudeSetup{}.Inspect(root).Findings,
	} {
		twice := findingsWith(findings, "runs twice")
		require.Len(t, twice, 1, name)
		assert.Equal(t, FindingWarning, twice[0].Level, name)
		assert.Contains(t, twice[0].Reason, "also runs `"+testBinary+" lessons --hook` for Edit, Write, MultiEdit; "+
			"the managed lessons hook runs too, so the hook runs twice for each call that both definitions run", name)
		assert.Len(t, findingsWith(findings, "with the if filter `Edit(src/**)`"), 1, name)
	}
}

func TestClaudeFilteredManagedHookRunsTwiceOnlyForTheCallsItMatches(t *testing.T) {
	// An if filter on the managed handler gives it another handler key
	// than an unfiltered local copy. The managed handler runs only for
	// the calls that its filter matches, so the hook does not run twice
	// for every call.
	root := t.TempDir()
	applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

	handler := managedHandler(t, root, "PreToolUse", hooks.LessonsMarker)
	writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit", handler))

	filtered := maps.Clone(handler)
	filtered["if"] = "Edit(src/**)"
	writeRel(t, root, approve.ClaudeSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit", filtered))

	for name, findings := range map[string][]Finding{
		"plan":       planClaude(t, root, ClientSetup{Hooks: true}).Findings,
		"inspection": claudeSetup{}.Inspect(root).Findings,
	} {
		twice := findingsWith(findings, "runs twice")
		require.Len(t, twice, 1, name)
		assert.Equal(t, claudeLocalSettings, twice[0].Path, name)
		assert.Contains(t, twice[0].Reason, "the managed lessons hook runs too, "+
			"so the hook runs twice for each call that both definitions run", name)
	}
}

func TestClaudeUncertainDefinitionBesideAFilteredManagedHook(t *testing.T) {
	// The local definition only can run the hook. An if filter on the
	// managed handler limits it to the calls that the filter matches. So
	// the hook can run twice only for the calls that both definitions run.
	const uncertain = "/opt/bin/hook-runner " + testBinary + " lessons --hook"

	managedFilters := map[string]struct{ filter, ending string }{
		"an unfiltered managed hook": {"", "the hook runs twice"},
		"a filtered managed hook":    {"Edit(src/**)", "the hook runs twice for each call that both definitions run"},
	}

	for name, tc := range managedFilters {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

			managed := managedHandler(t, root, "PreToolUse", hooks.LessonsMarker)
			if tc.filter != "" {
				managed["if"] = tc.filter
			}

			writeRel(t, root, approve.ClaudeSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit", managed))
			writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Edit|Write|MultiEdit",
				map[string]any{"type": "command", "command": uncertain}))

			want := "has `" + uncertain + "`, which can run the seamark lessons hook; setup cannot tell, " +
				"and when it does, " + tc.ending

			for run, findings := range map[string][]Finding{
				"plan":       planClaude(t, root, ClientSetup{Hooks: true}).Findings,
				"inspection": claudeSetup{}.Inspect(root).Findings,
			} {
				warnings := findingsWith(findings, "which can run the seamark lessons hook")
				require.Len(t, warnings, 1, run)
				assert.Equal(t, FindingWarning, warnings[0].Level, run)
				assert.Equal(t, claudeLocalSettings, warnings[0].Path, run)
				assert.Equal(t, want, warnings[0].Reason, run)
			}
		})
	}
}

func TestClaudeCopyMustShareAToolWithTheManagedHook(t *testing.T) {
	// The user narrowed the managed matcher to Edit, and setup keeps it.
	// A local copy for Write and MultiEdit never runs on the same call
	// as the managed handler, so Claude Code never collapses the two.
	copies := map[string]func(map[string]any){
		"a verbatim copy": func(map[string]any) {},
		"another timeout": func(h map[string]any) { h["timeout"] = 30 },
	}

	for name, edit := range copies {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true})

			handler := managedHandler(t, root, "PreToolUse", hooks.LessonsMarker)
			writeRel(t, root, approve.ClaudeSettings, copyDocument(t, "PreToolUse", "Edit", handler))

			copied := maps.Clone(handler)
			edit(copied)
			writeRel(t, root, claudeLocalSettings, copyDocument(t, "PreToolUse", "Write|MultiEdit", copied))

			for run, findings := range map[string][]Finding{
				"plan":       planClaude(t, root, ClientSetup{Hooks: true}).Findings,
				"inspection": claudeSetup{}.Inspect(root).Findings,
			} {
				assert.Empty(t, findingsWith(findings, "because it equals the managed"), run)
				assert.Empty(t, findingsWith(findings, "has a copy of the managed"), run)

				unmanaged := findingsWith(findings, "runs the seamark lessons hook as `"+testBinary+" lessons --hook`; "+
					"setup does not manage that definition")
				require.Len(t, unmanaged, 1, run)
				assert.Equal(t, FindingInfo, unmanaged[0].Level, run)
				assert.Equal(t, claudeLocalSettings, unmanaged[0].Path, run)
			}
		})
	}
}
