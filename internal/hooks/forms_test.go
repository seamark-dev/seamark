package hooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gate commands of both forms, for the test binary.
const (
	formBinary        = "/bin/seamark"
	claudeWarnGate    = formBinary + " gate --hook"
	claudeEnforceGate = formBinary + " gate --enforce --hook"
	codexWarnGate     = formBinary + " gate --hook --client codex"
	codexEnforceGate  = formBinary + " gate --enforce --hook --client codex"
)

// formDocument parses one hook document of the test.
func formDocument(t *testing.T, raw string) map[string]any {
	t.Helper()

	document, err := ParseDocument([]byte(raw))
	require.NoError(t, err)

	return document
}

// preToolUse returns the PreToolUse entries of a document as JSON, so a
// test compares the whole shape.
func preToolUse(t *testing.T, document map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(document["hooks"].(map[string]any)["PreToolUse"])
	require.NoError(t, err)

	return string(raw)
}

func TestCodexGateReadsTheClaudeCodeFormAndClaudeNeverReadsCodex(t *testing.T) {
	codex := CodexSpecs(ModeWarn)[0]
	claude := ClaudeSpecs(ModeWarn)[0]

	for cmd, mode := range map[string]string{claudeWarnGate: ModeWarn, claudeEnforceGate: ModeEnforce} {
		_, marker, ok := OwnedCommand(cmd, codex.Markers())
		require.True(t, ok, "the Codex gate owns %q", cmd)
		assert.Equal(t, mode, GateMarkerMode(marker))
		assert.Contains(t, codex.OtherForms, marker)

		wrapped := "sh -c '" + cmd + "'"
		assert.NotEqual(t, HookNotRun, ReadHook(wrapped, codex.Markers()).Use(), "the Codex reader sees %q", wrapped)
	}

	for _, cmd := range []string{codexWarnGate, codexEnforceGate} {
		assert.False(t, OwnedBySeamark(cmd, claude.Markers()), "a Claude marker never claims %q", cmd)
		assert.Equal(t, HookNotRun, ReadHook("sh -c '"+cmd+"'", claude.Markers()).Use())
	}

	// The Claude Code specs keep their markers.
	assert.Empty(t, claude.OtherForms)
}

func TestReduceOtherFormsKeepsOneGateHandler(t *testing.T) {
	entry := func(matcher, command string) string {
		return `{"matcher":"` + matcher + `","hooks":[{"type":"command","command":"` + command + `","timeout":30}]}`
	}

	cases := map[string]struct {
		document string
		want     string
		changes  []FormChange
	}{
		"both forms under Bash": {
			`{"hooks":{"PreToolUse":[` + entry("Bash", codexWarnGate) + `,` + entry("Bash", claudeEnforceGate) + `]}}`,
			`[` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeEnforceGate, Matcher: "Bash"}},
		},
		"only the Claude Code form": {
			`{"hooks":{"PreToolUse":[` + entry("Bash", claudeEnforceGate) + `]}}`,
			`[` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeEnforceGate, To: codexWarnGate, Matcher: "Bash"}},
		},
		"the Codex form never fires": {
			`{"hooks":{"PreToolUse":[` + entry("ash", codexWarnGate) + `,` + entry("Bash", claudeWarnGate) + `]}}`,
			`[` + entry("ash", codexWarnGate) + `,` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, To: codexWarnGate, Matcher: "Bash"}},
		},
		"two Claude Code handlers": {
			`{"hooks":{"PreToolUse":[` + entry("Bash", claudeWarnGate) + `,` + entry("Bash", claudeEnforceGate) + `]}}`,
			`[` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, To: codexWarnGate, Matcher: "Bash"}, {From: claudeEnforceGate, Matcher: "Bash"}},
		},
		"a handler beside a foreign hook": {
			`{"hooks":{"PreToolUse":[` + entry("Bash", codexWarnGate) + `,` +
				`{"matcher":"Bash","hooks":[{"type":"command","command":"my-linter"},` +
				`{"type":"command","command":"` + claudeEnforceGate + `"}]}]}}`,
			`[` + entry("Bash", codexWarnGate) + `,{"hooks":[{"command":"my-linter","type":"command"}],"matcher":"Bash"}]`,
			[]FormChange{{From: claudeEnforceGate, Matcher: "Bash"}},
		},
		"only Claude Code handlers that never fire": {
			`{"hooks":{"PreToolUse":[` + entry("ash", claudeWarnGate) + `,` + entry("sh", claudeEnforceGate) + `]}}`,
			`[` + entry("ash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, To: codexWarnGate, Matcher: "ash"}, {From: claudeEnforceGate, Matcher: "sh"}},
		},
		"a Claude Code handler that never fires before one that runs": {
			`{"hooks":{"PreToolUse":[` + entry("Read", claudeWarnGate) + `,` + entry("Bash", claudeEnforceGate) + `]}}`,
			`[` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, Matcher: "Read"}, {From: claudeEnforceGate, To: codexWarnGate, Matcher: "Bash"}},
		},
		"handlers of both forms that never fire": {
			`{"hooks":{"PreToolUse":[` + entry("ash", codexWarnGate) + `,` + entry("Read", claudeWarnGate) + `]}}`,
			`[` + entry("ash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, Matcher: "Read"}},
		},
		"a Claude Code handler of another type before a command": {
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"prompt","command":"` + claudeWarnGate + `"}]},` +
				entry("Bash", claudeEnforceGate) + `]}}`,
			`[` + entry("Bash", codexWarnGate) + `]`,
			[]FormChange{{From: claudeWarnGate, Matcher: "Bash"}, {From: claudeEnforceGate, To: codexWarnGate, Matcher: "Bash"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			document := formDocument(t, tc.document)

			// The removal list follows the same rule and changes nothing.
			removals := OtherFormRemovals(document, CodexSpecs(ModeWarn)[0], CodexMatcher)
			assert.Equal(t, formDocument(t, tc.document), document)

			changes := ReduceOtherForms(document, CodexSpecs(ModeWarn)[0], formBinary, CodexMatcher)

			assert.Equal(t, tc.changes, changes)

			var removed []FormChange

			for _, c := range changes {
				if c.To == "" {
					removed = append(removed, c)
				}
			}

			assert.Equal(t, removed, removals)
			assert.JSONEq(t, tc.want, preToolUse(t, document))

			// A second pass finds nothing in the other form.
			assert.Empty(t, ReduceOtherForms(document, CodexSpecs(ModeWarn)[0], formBinary, CodexMatcher))
		})
	}
}

func TestReduceOtherFormsLeavesADocumentWithoutThemAlone(t *testing.T) {
	raw := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + codexEnforceGate + `"}]}]}}`
	document := formDocument(t, raw)

	assert.Empty(t, ReduceOtherForms(document, CodexSpecs(ModeWarn)[0], formBinary, CodexMatcher))
	assert.Equal(t, formDocument(t, raw), document)

	// A Claude Code spec has no other form, so it changes nothing.
	claude := formDocument(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+
		codexWarnGate+`"}]}]}}`)
	assert.Empty(t, ReduceOtherForms(claude, ClaudeSpecs(ModeWarn)[0], formBinary, ClaudeMatcher))
}

func TestMergeDoesNotRewriteAnotherForm(t *testing.T) {
	// Merge alone leaves a handler in the Claude Code form, because only
	// ReduceOtherForms chooses which one stays.
	document := formDocument(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+
		claudeEnforceGate+`"}]}]}}`)

	_, err := Merge(document, formBinary, CodexSpecs(ModeWarn)[:1])
	require.NoError(t, err)

	assert.Contains(t, preToolUse(t, document), claudeEnforceGate)
	assert.Contains(t, preToolUse(t, document), codexWarnGate)
}
