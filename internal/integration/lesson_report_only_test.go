package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

// TestReportOnlyStepNamesTheDefinition proves the step text of each
// cause. The enforce refusal and doctor print these texts, so each one
// names the document, the command, and the edit that makes the hook
// block again.
func TestReportOnlyStepNamesTheDefinition(t *testing.T) {
	for name, tc := range map[string]struct {
		client string
		def    ReportOnlyDefinition
		want   string
	}{
		"discards the exit status": {
			client: ClaudeID,
			def:    ReportOnlyDefinition{Path: claudeLocalSettings, Command: "/opt/bin/seamark lessons --guard --hook || true"},
			want: ".claude/settings.local.json: remove the definition `/opt/bin/seamark lessons --guard --hook || true`, " +
				"which discards the exit status of the lesson-check pre-write hook",
		},
		"runs in the background": {
			client: CodexID,
			def:    ReportOnlyDefinition{Path: codexInlineHooksPath, Command: "/opt/bin/seamark x", Background: "async"},
			want: ".codex/config.toml [hooks]: remove the definition `/opt/bin/seamark x`, which has `async: true`, " +
				"so the client runs the lesson-check pre-write hook in the background",
		},
		"the managed entry in the background": {
			client: ClaudeID,
			def: ReportOnlyDefinition{
				Path: approve.ClaudeSettings, Command: "/opt/bin/seamark x", Background: "asyncRewake", Managed: true,
			},
			want: ".claude/settings.json: run `seamark init --client claude`, which removes `asyncRewake` from " +
				"the lesson-check pre-write hook",
		},
		"the managed Codex entry in the background": {
			// The init step names the client of the record. An init of
			// another client leaves the background key in place.
			client: CodexID,
			def:    ReportOnlyDefinition{Path: codexHooksFile, Command: "/opt/bin/seamark x", Background: "async", Managed: true},
			want: ".codex/hooks.json: run `seamark init --client codex`, which removes `async` from " +
				"the lesson-check pre-write hook",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.def.Step(tc.client))
		})
	}

	// The command passes through describeCommand, as the refusal of a
	// second handler does, so a credential never reaches the terminal.
	// Each removal step names a command, so the test reads each cause.
	const secret = "sk-live-1234567890abcdef"

	for name, background := range map[string]string{
		"a secret in a command that discards the status": "",
		"a secret in a command in the background":        "async",
	} {
		t.Run(name, func(t *testing.T) {
			def := ReportOnlyDefinition{Path: approve.ClaudeSettings, Command: "hook-runner --token=" + secret +
				" /opt/bin/seamark x || true", Background: background}

			step := def.Step(ClaudeID)
			assert.Contains(t, step, ": remove the definition `", "a removal step names the command")
			assert.NotContains(t, step, secret)
			assert.Contains(t, step, "[REDACTED]")
		})
	}
}

// TestReportOnlyStepsDropOnlyAnEqualRecord proves that a repeated
// record gives one step, and that a record that differs in one field
// keeps its own step.
func TestReportOnlyStepsDropOnlyAnEqualRecord(t *testing.T) {
	wrapper := ReportOnlyDefinition{Path: claudeLocalSettings, Command: "x || true"}
	sameCommandOtherFile := ReportOnlyDefinition{Path: approve.ClaudeSettings, Command: "x || true"}
	sameFileOtherCause := ReportOnlyDefinition{Path: claudeLocalSettings, Command: "x || true", Background: "async"}

	steps := ReportOnlySteps(ClaudeID, []ReportOnlyDefinition{wrapper, sameCommandOtherFile, wrapper, sameFileOtherCause})

	assert.Equal(t, []string{
		wrapper.Step(ClaudeID), sameCommandOtherFile.Step(ClaudeID), sameFileOtherCause.Step(ClaudeID),
	}, steps)
	assert.Empty(t, ReportOnlySteps(ClaudeID, nil))
}

// TestReportOnlyStepsPutTheInitStepLast proves the order of the steps.
// The records hold the managed document first. Init refuses while a
// local definition runs the hook beside the managed handler, so each
// removal comes before the init step of the managed entry.
func TestReportOnlyStepsPutTheInitStepLast(t *testing.T) {
	managed := ReportOnlyDefinition{
		Path: approve.ClaudeSettings, Command: "/opt/bin/seamark x", Background: "async", Managed: true,
	}
	wrapper := ReportOnlyDefinition{Path: claudeLocalSettings, Command: "/opt/bin/seamark x || true"}
	background := ReportOnlyDefinition{Path: claudeLocalSettings, Command: "/opt/bin/seamark y", Background: "async"}

	assert.Equal(t, []string{wrapper.Step(ClaudeID), background.Step(ClaudeID), managed.Step(ClaudeID)},
		ReportOnlySteps(ClaudeID, []ReportOnlyDefinition{managed, wrapper, background}))
}

// TestCodexLessonSourcesNameEveryReportOnlyDefinition proves that the
// Codex source state keeps each definition, the managed hooks.json
// first, then the inline hooks of the configuration.
func TestCodexLessonSourcesNameEveryReportOnlyDefinition(t *testing.T) {
	wrapped := testBinary + " " + hooks.CodexLessonGuardMarker + " || true"

	document, err := hooks.ParseDocument([]byte(`{"hooks": {"PreToolUse": [{"matcher": "apply_patch", ` +
		`"hooks": [{"type": "command", "command": "` + wrapped + `"}]}]}}`))
	require.NoError(t, err)

	inline := []approve.InlineHook{{
		Event: "PreToolUse", Matcher: "apply_patch", Type: "command", Known: true, Async: true,
		Command: testBinary + " " + hooks.CodexLessonGuardMarker,
	}}

	state := codexLessonSources(document, inline, &codexConfigRead{}, false)
	assert.Equal(t, []ReportOnlyDefinition{
		{Path: codexHooksFile, Command: wrapped},
		{Path: codexInlineHooksPath, Command: inline[0].Command, Background: "async"},
	}, state.reportOnly)

	t.Run("the managed guard in the background", func(t *testing.T) {
		// Inspection does not merge, so the owned entry is a managed
		// record. A plan that merges the pre-write hook removes the key,
		// so the record does not count there.
		guard := testBinary + " " + hooks.CodexLessonGuardMarker

		document, err := hooks.ParseDocument([]byte(`{"hooks": {"PreToolUse": [{"matcher": "apply_patch", ` +
			`"hooks": [{"type": "command", "command": "` + guard + `", "async": true}]}]}}`))
		require.NoError(t, err)

		assert.Equal(t, []ReportOnlyDefinition{{Path: codexHooksFile, Command: guard, Background: "async", Managed: true}},
			codexLessonSources(document, nil, &codexConfigRead{}, false).reportOnly)
		assert.Empty(t, codexLessonSources(document, nil, &codexConfigRead{}, true).reportOnly)
	})
}

// TestReportOnlyReadersKeepEachDefinitionThatRuns proves which
// definitions the readers keep, and the cause of each record. Only a
// "command" hook of the pre-write event under a matcher that fires runs.
// An inline entry in a layout that the reader does not know counts as
// one that runs. A definition that discards the exit status and runs in
// the background gets the discard cause, because removing the key alone
// leaves the status discarded.
func TestReportOnlyReadersKeepEachDefinitionThatRuns(t *testing.T) {
	t.Run("a Claude Code hook document", func(t *testing.T) {
		wrapped := testBinary + " " + hooks.LessonGuardMarker + " || true"

		for name, tc := range map[string]struct {
			document string
			want     []ReportOnlyDefinition
		}{
			"a wrapper in the background": {
				document: entryWith("PreToolUse", "Edit|Write", wrapped, `,"async":true`),
				want:     []ReportOnlyDefinition{{Path: claudeLocalSettings, Command: wrapped}},
			},
			"a wrapper under a matcher that does not fire": {
				document: entryWith("PreToolUse", "Bash", wrapped, ""),
			},
			"a wrapper of another hook type": {
				document: `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write","hooks":[{"type":"prompt","command":"` +
					wrapped + `"}]}]}}`,
			},
		} {
			t.Run(name, func(t *testing.T) {
				document, err := hooks.ParseDocument([]byte(tc.document))
				require.NoError(t, err)

				assert.Equal(t, tc.want, reportOnlyIn(document, claudeLessonHooks, claudeLocalSettings, false, false))
			})
		}
	})

	t.Run("the inline Codex hooks", func(t *testing.T) {
		wrapped := testBinary + " " + hooks.CodexLessonGuardMarker + " || true"
		entry := func(edit func(*approve.InlineHook)) approve.InlineHook {
			e := approve.InlineHook{Event: "PreToolUse", Matcher: "apply_patch", Type: "command", Known: true, Command: wrapped}
			edit(&e)

			return e
		}
		record := []ReportOnlyDefinition{{Path: codexInlineHooksPath, Command: wrapped}}

		for name, tc := range map[string]struct {
			entry approve.InlineHook
			want  []ReportOnlyDefinition
		}{
			"a wrapper in the background": {entry: entry(func(e *approve.InlineHook) { e.Async = true }), want: record},
			"a wrapper under a matcher that does not fire": {
				entry: entry(func(e *approve.InlineHook) { e.Matcher = "Bash" }),
			},
			"a wrapper of another hook type": {entry: entry(func(e *approve.InlineHook) { e.Type = "prompt" })},
			"a wrapper under another event":  {entry: entry(func(e *approve.InlineHook) { e.Event = "PostToolUse" })},
			"a wrapper in a layout that the reader does not know": {
				entry: entry(func(e *approve.InlineHook) { e.Known, e.Matcher, e.Type = false, "", "" }),
				want:  record,
			},
		} {
			t.Run(name, func(t *testing.T) {
				assert.Equal(t, tc.want, reportOnlyInline([]approve.InlineHook{tc.entry}, codexLessonHooks))
			})
		}
	})
}

// TestEnforceRefusalRouteRuns proves that the steps of each refusal
// run. The test reads the steps from the refusal text and applies each
// one to the fixture. Enforce then plans without an error, so no step
// leads to the refusal of a second lesson handler.
func TestEnforceRefusalRouteRuns(t *testing.T) {
	const guard = "/opt/bin/seamark " + hooks.LessonGuardMarker

	codexGuard := "/opt/bin/seamark " + hooks.CodexLessonGuardMarker

	for name, tc := range map[string]struct {
		client string
		files  map[string]string
	}{
		"a Claude wrapper in the shared file": {
			client: ClaudeID,
			files:  map[string]string{approve.ClaudeSettings: entryWith("PreToolUse", "Edit|Write", guard+" || true", "")},
		},
		"a Claude wrapper in the local file": {
			client: ClaudeID,
			files:  map[string]string{claudeLocalSettings: entryWith("PreToolUse", "Edit|Write", guard+" || true", "")},
		},
		"a Claude local guard in the background": {
			client: ClaudeID,
			files:  map[string]string{claudeLocalSettings: entryWith("PreToolUse", "Edit|Write", guard, `,"async":true`)},
		},
		"a Codex wrapper in hooks.json": {
			client: CodexID,
			files:  map[string]string{codexHooksFile: entryWith("PreToolUse", "apply_patch", codexGuard+" || true", "")},
		},
		"a Codex inline guard in the background": {
			client: CodexID,
			files: map[string]string{approve.CodexConfig: "[[hooks.PreToolUse]]\nmatcher = \"apply_patch\"\n" +
				"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"" + codexGuard + "\"\nasync = true\n"},
		},
		"disableAllHooks beside a local wrapper": {
			client: ClaudeID,
			files: map[string]string{
				approve.ClaudeSettings: `{"disableAllHooks": true}`,
				claudeLocalSettings:    entryWith("PreToolUse", "Edit|Write", guard+" || true", ""),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for rel, text := range tc.files {
				writeRel(t, root, rel, text)
			}

			setup := ClientSetup{ClientID: tc.client, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce}
			req := SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{setup}}

			_, err := PlanSetup(Builtin(), req)
			require.Error(t, err)

			for _, step := range refusalSteps(t, err.Error()) {
				applyStep(t, root, step)
			}

			_, err = PlanSetup(Builtin(), req)
			require.NoError(t, err, "the printed steps lead to a state where enforce runs")
		})
	}
}

// refusalSteps returns the steps of an enforce refusal: the text after
// the reasons and before the closing "then rerun".
func refusalSteps(t *testing.T, refusal string) []string {
	t.Helper()

	_, rest, ok := strings.Cut(refusal, "); ")
	require.True(t, ok, refusal)

	body, ok := strings.CutSuffix(rest, "; then rerun")
	require.True(t, ok, refusal)

	return strings.Split(body, "; ")
}

// applyStep makes the edit that one refusal step names. A removal
// deletes the named definition from the named document. The inline
// Codex fixture holds one hook block, so the removal there drops the
// hook tables. A disableAllHooks step deletes the key.
func applyStep(t *testing.T, root, step string) {
	t.Helper()

	where, action, ok := strings.Cut(step, ": ")
	require.True(t, ok, step)

	if where == codexInlineHooksPath {
		_, command, _ := strings.Cut(action, "`")
		command, _, _ = strings.Cut(command, "`")
		require.NotEmpty(t, command, step)

		data, err := os.ReadFile(filepath.Join(root, approve.CodexConfig))
		require.NoError(t, err)
		require.Contains(t, string(data), command, step)

		head, _, _ := strings.Cut(string(data), "[[hooks.")
		writeRel(t, root, approve.CodexConfig, head)

		return
	}

	path := filepath.Join(root, where)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	switch {
	case strings.HasPrefix(action, "remove disableAllHooks"):
		delete(doc, claudeDisableAllHooks)
	case strings.HasPrefix(action, "remove the definition `"):
		command, _, _ := strings.Cut(strings.TrimPrefix(action, "remove the definition `"), "`")
		require.NotEmpty(t, command, step)
		require.True(t, removeDefinition(doc, command), "the step names a definition of %s: %s", where, step)
	default:
		require.Failf(t, "unknown step", "%s", step)
	}

	data, err = json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// removeDefinition deletes every handler with the command from a parsed
// hook document, and each matcher entry that it leaves empty. It
// reports whether it removed a handler.
func removeDefinition(doc map[string]any, command string) bool {
	hookMap, _ := doc["hooks"].(map[string]any)
	removed := false

	for event, value := range hookMap {
		entries, _ := value.([]any)
		kept := []any{}

		for _, e := range entries {
			entry, _ := e.(map[string]any)
			handlers, _ := entry["hooks"].([]any)
			left := []any{}

			for _, h := range handlers {
				if handler, _ := h.(map[string]any); handler["command"] == command {
					removed = true

					continue
				}

				left = append(left, h)
			}

			if len(left) > 0 {
				entry["hooks"] = left
				kept = append(kept, entry)
			}
		}

		hookMap[event] = kept
	}

	return removed
}
