package hooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionRulesReadEachClientsKeys(t *testing.T) {
	// Claude Code reads async, asyncRewake, and if. Codex reads async
	// only. Only the boolean true puts a handler in the background.
	for name, tc := range map[string]struct {
		handler       map[string]any
		claude, codex Execution
	}{
		"no extra key":         {map[string]any{"command": "x"}, Execution{}, Execution{}},
		"async true":           {map[string]any{"async": true}, Execution{Background: "async"}, Execution{Background: "async"}},
		"asyncRewake true":     {map[string]any{"asyncRewake": true}, Execution{Background: "asyncRewake"}, Execution{}},
		"async false":          {map[string]any{"async": false}, Execution{}, Execution{}},
		"async as a string":    {map[string]any{"async": "true"}, Execution{}, Execution{}},
		"both keys":            {map[string]any{"async": true, "asyncRewake": true}, Execution{Background: "async"}, Execution{Background: "async"}},
		"an if filter":         {map[string]any{"if": "Bash(rm *)"}, Execution{Filter: "Bash(rm *)"}, Execution{}},
		"an empty if":          {map[string]any{"if": ""}, Execution{}, Execution{}},
		"an if of a bool type": {map[string]any{"if": true}, Execution{Filter: "true"}, Execution{}},
		"onFailure block":      {map[string]any{"onFailure": "block"}, Execution{}, Execution{}},
	} {
		assert.Equal(t, tc.claude, ClaudeExecution(tc.handler), name)
		assert.Equal(t, tc.codex, CodexExecution(tc.handler), name)
	}
}

func TestClaudeToolEventNamesTheEventsWithAToolCall(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied"} {
		assert.True(t, ClaudeToolEvent(event), event)
	}

	for _, event := range []string{"PostCompact", "Stop", "UserPromptSubmit", "SessionStart"} {
		assert.False(t, ClaudeToolEvent(event), event)
	}
}

// mergeJSON merges the specs into a document parsed from raw and returns
// the result and the merge report.
func mergeJSON(t *testing.T, raw string, specs []Spec) (map[string]any, Merged) {
	t.Helper()

	document, err := ParseDocument([]byte(raw))
	require.NoError(t, err)

	merged, err := Merge(document, "/bin/seamark", specs)
	require.NoError(t, err)

	return document, merged
}

// firstHandler returns the first handler under the event.
func firstHandler(t *testing.T, document map[string]any, event string) map[string]any {
	t.Helper()

	entries, _ := document["hooks"].(map[string]any)[event].([]any)
	require.NotEmpty(t, entries)

	handlers, _ := entries[0].(map[string]any)["hooks"].([]any)
	require.NotEmpty(t, handlers)

	handler, ok := handlers[0].(map[string]any)
	require.True(t, ok)

	return handler
}

func TestMergeRemovesBackgroundKeysFromOwnedForegroundEntries(t *testing.T) {
	// A gate or pre-write hook in the background cannot block, so Merge
	// removes the key from seamark's own entry and reports it. Every
	// other key of the entry stays.
	entry := func(command, extra string) string {
		return `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + command +
			`","timeout":15,"statusMessage":"mine","onFailure":"continue"` + extra + `}]}]}}`
	}

	for name, tc := range map[string]struct {
		raw     string
		specs   []Spec
		removed []BackgroundRemoval
	}{
		"Claude gate with async": {
			entry("/bin/seamark gate --hook", `,"async":true`), ClaudeSpecs(ModeEnforce),
			[]BackgroundRemoval{{Name: "gate", Event: "PreToolUse", Key: "async"}},
		},
		"Claude gate with asyncRewake": {
			entry("/bin/seamark gate --enforce --hook", `,"asyncRewake":true`), ClaudeSpecs(ModeEnforce),
			[]BackgroundRemoval{{Name: "gate", Event: "PreToolUse", Key: "asyncRewake"}},
		},
		"Codex gate with async": {
			entry("/bin/seamark gate --hook --client codex", `,"async":true`), CodexSpecs(ModeEnforce),
			[]BackgroundRemoval{{Name: "gate", Event: "PreToolUse", Key: "async"}},
		},
		"Claude baseline with async": {
			`{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command",` +
				`"command":"/bin/seamark lessons --baseline --hook","timeout":15,"statusMessage":"mine","onFailure":"continue",` +
				`"async":true}]}]}}`,
			ClaudeLessonCheckSpecs(LessonChecksCheck)[:1],
			[]BackgroundRemoval{{Name: "lesson baseline", Event: "PreToolUse", Key: "async"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			document, merged := mergeJSON(t, tc.raw, tc.specs[:1])

			assert.True(t, merged.Changed)
			assert.Equal(t, tc.removed, merged.Removed)

			handler := firstHandler(t, document, "PreToolUse")
			assert.NotContains(t, handler, tc.removed[0].Key)
			assert.InDelta(t, 15.0, handler["timeout"], 0, "the merge keeps every other key")
			assert.Equal(t, "mine", handler["statusMessage"])
			assert.Equal(t, "continue", handler["onFailure"])
		})
	}
}

func TestMergeKeepsKeysThatDoNotMakeAForegroundHookBlockless(t *testing.T) {
	// Codex ignores asyncRewake, a false value is not a background key,
	// and a hook that delivers late keeps its key. A user's entry is never
	// seamark's own. None of these changes the document.
	codexGate := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"/bin/seamark gate --hook --client codex","timeout":15,` +
		`"statusMessage":"seamark gate: classifying command","asyncRewake":true}]}]}}`
	claudeFalse := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"/bin/seamark gate --hook","timeout":15,` +
		`"statusMessage":"seamark gate: classifying command","async":false}]}]}}`

	for name, tc := range map[string]struct {
		raw   string
		specs []Spec
	}{
		"Codex gate with asyncRewake only": {codexGate, CodexSpecs(ModeWarn)[:1]},
		"Claude gate with async false":     {claudeFalse, ClaudeSpecs(ModeWarn)[:1]},
		"Claude lessons hook with async": {
			`{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[` +
				`{"type":"command","command":"/bin/seamark lessons --hook","timeout":10,"async":true}]}]}}`,
			ClaudeSpecs(ModeWarn)[1:2],
		},
		"Claude reset hook with async": {
			`{"hooks":{"PostCompact":[{"hooks":[` +
				`{"type":"command","command":"/bin/seamark lessons --hook-reset","timeout":10,"async":true}]}]}}`,
			ClaudeSpecs(ModeWarn)[2:],
		},
		"Claude verify hook with async": {
			`{"hooks":{"PostToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[` +
				`{"type":"command","command":"/bin/seamark lessons --verify --hook","timeout":15,"async":true}]}]}}`,
			ClaudeLessonCheckSpecs(LessonChecksEnforce)[1:2],
		},
	} {
		t.Run(name, func(t *testing.T) {
			before, err := ParseDocument([]byte(tc.raw))
			require.NoError(t, err)

			document, merged := mergeJSON(t, tc.raw, tc.specs)

			assert.Empty(t, merged.Removed)

			// The specs of one entry change nothing in a current entry.
			if len(tc.specs) == 1 {
				assert.False(t, merged.Changed)
			}

			// Only an absent spec of the list adds an entry. The entry that the
			// raw document holds keeps every key.
			beforeJSON, err := json.Marshal(before["hooks"])
			require.NoError(t, err)

			afterHandler := firstHandler(t, document, firstEvent(before))
			beforeHandler := firstHandler(t, before, firstEvent(before))
			assert.Equal(t, beforeHandler, afterHandler, string(beforeJSON))
		})
	}

	// A user's own gate with async stays as it is.
	user := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"timeout 5 /bin/seamark gate --hook","async":true}]}]}}`
	document, merged := mergeJSON(t, user, ClaudeSpecs(ModeWarn)[:1])

	assert.Empty(t, merged.Removed)
	assert.Equal(t, true, firstHandler(t, document, "PreToolUse")["async"])
}

// firstEvent returns the only event of a hook document.
func firstEvent(document map[string]any) string {
	for event := range document["hooks"].(map[string]any) {
		return event
	}

	return ""
}
