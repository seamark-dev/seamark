package integration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexFixture reads one native payload from testdata/codex.
func codexFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "codex", name))
	require.NoError(t, err)

	return data
}

// patchEvent wraps a patch text in a minimal apply_patch event.
func patchEvent(t *testing.T, patch string) []byte {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"session_id": "thr_1", "cwd": "/workspace/repo", "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "call_1",
		"tool_input": map[string]any{"command": patch},
	})
	require.NoError(t, err)

	return payload
}

func TestCodexEditsDecodeTheCompletePathSet(t *testing.T) {
	cases := []struct {
		fixture string
		cwd     string
		paths   []string
	}{
		{"pre_tool_use_apply_patch_add.json", "/workspace/repo", []string{"docs/new-page.md"}},
		{"pre_tool_use_apply_patch_update.json", "/workspace/repo", []string{"internal/api/handler.go"}},
		{"pre_tool_use_apply_patch_delete.json", "/workspace/repo", []string{"internal/api/obsolete.go"}},
		// A move is in scope at both ends: the source and the destination.
		{"pre_tool_use_apply_patch_move.json", "/workspace/repo",
			[]string{"internal/api/old_name.go", "internal/api/new_name.go"}},
		{"pre_tool_use_apply_patch_multi.json", "/workspace/repo",
			[]string{"hello.txt", "src/app.py", "src/main.py", "obsolete.txt"}},
		// The decoder keeps a repeated path. The shared normalization
		// removes it, so every client gets one rule.
		{"pre_tool_use_apply_patch_repeated.json", "/workspace/repo",
			[]string{"internal/api/handler.go", "internal/api/handler.go"}},
		// The decoder keeps the paths as written. The event directory
		// travels with them, and the shared normalization resolves them.
		{"pre_tool_use_apply_patch_nested_cwd.json", "/workspace/repo/internal/api",
			[]string{"handler.go", "../../docs/from-nested.md"}},
		// An outside path is part of the complete set. The decoder does
		// not judge it: the shared normalization rejects and reports it.
		{"pre_tool_use_apply_patch_outside.json", "/workspace/repo", []string{"../escape.txt", "/etc/hosts"}},
		{"pre_tool_use_apply_patch_end_of_file.json", "/workspace/repo", []string{"README.md"}},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			event, err := codexEdits{}.DecodeEdit(codexFixture(t, tc.fixture))
			require.NoError(t, err)

			assert.Equal(t, tc.paths, event.Paths)
			assert.Equal(t, tc.cwd, event.CWD)
			assert.Equal(t, "apply_patch", event.NativeTool)
			assert.Equal(t, "thr_synthetic_0001", event.SessionID, "the session still joins the log records")
			assert.NotEmpty(t, event.MatchID)

			// The decoder reads no receiver yet: the reset of a subagent is
			// unverified, so suppression stays off by policy.
			assert.Nil(t, event.Context, "no receiving context means suppression stays off")
		})
	}
}

func TestCodexEditsReadNoReceiverFromASubagentEdit(t *testing.T) {
	// A native run of codex-cli 0.154.0 showed that an edit inside a
	// subagent carries agent_id and agent_type with the parent session id.
	// The decoder reads no receiver from it yet: the reset of a subagent
	// is unverified, so suppression stays off by policy. The test pins
	// that the identity is in the event and left unread, so a receiver
	// change starts from this fixture and not from a guess.
	payload := codexFixture(t, "pre_tool_use_apply_patch_subagent.json")

	var identity struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}

	require.NoError(t, json.Unmarshal(payload, &identity))
	require.NotEmpty(t, identity.AgentID, "the fixture carries the subagent identity")
	require.NotEmpty(t, identity.AgentType)

	event, err := codexEdits{}.DecodeEdit(payload)
	require.NoError(t, err)

	assert.Equal(t, "thr_synthetic_0001", event.SessionID, "a subagent edit reports the parent session")
	assert.Equal(t, []string{"/workspace/repo/src/child.txt"}, event.Paths, "the model wrote an absolute path")
	assert.Nil(t, event.Context, "no receiver is read until a native check verifies the reset of a subagent")
}

func TestCodexEditsNeverReportAPartialPathSet(t *testing.T) {
	malformed := []struct {
		name    string
		payload []byte
	}{
		{"truncated envelope", codexFixture(t, "pre_tool_use_apply_patch_malformed.json")},
		{"no begin line", patchEvent(t, "*** Update File: a.go\n@@\n-x\n+y\n*** End Patch\n")},
		{"text after the end line", patchEvent(t, "*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n*** Delete File: b.go\n")},
		{"no hunk", patchEvent(t, "*** Begin Patch\n*** End Patch\n")},
		{"empty patch", patchEvent(t, "")},
		{"invalid JSON", []byte("{not json")},
		{"command of another type", []byte(`{"tool_name":"apply_patch","tool_input":{"command":["a"]}}`)},
		{"no command", []byte(`{"tool_name":"apply_patch","tool_input":{}}`)},
	}

	for _, tc := range malformed {
		t.Run("malformed/"+tc.name, func(t *testing.T) {
			event, err := codexEdits{}.DecodeEdit(tc.payload)
			require.ErrorIs(t, err, ErrMalformedEvent)
			assert.Empty(t, event.Paths, "an invalid event gives no paths at all")
		})
	}

	unsupported := map[string]string{
		// A named environment can have another root than the event cwd.
		// Until a native capture shows otherwise, its paths are unknown.
		"environment line":       "*** Begin Patch\n*** Environment ID: env_1\n*** Delete File: a.go\n*** End Patch\n",
		"environment line alone": "*** Begin Patch\n*** Environment ID: env_1\n*** End Patch\n",
		// An unknown header can name a file, so the set is not complete.
		"unknown header":         "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** Copy File: b.go\n*** End Patch\n",
		"header without a name":  "*** Begin Patch\n*** Add File: \n+x\n*** End Patch\n",
		"move outside an update": "*** Begin Patch\n*** Add File: a.go\n*** Move to: b.go\n+x\n*** End Patch\n",
		"move after a change":    "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n*** Move to: b.go\n*** End Patch\n",
		"two moves":              "*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n*** Move to: c.go\n*** End Patch\n",
		"line before a hunk":     "*** Begin Patch\n+stray\n*** Add File: a.go\n+x\n*** End Patch\n",
		"line in a delete hunk":  "*** Begin Patch\n*** Delete File: a.go\n-x\n*** End Patch\n",
		"bad line in an add":     "*** Begin Patch\n*** Add File: a.go\nno plus sign\n*** End Patch\n",
		"bad line in an update":  "*** Begin Patch\n*** Update File: a.go\n@@\n?x\n*** End Patch\n",
		"end of file in an add":  "*** Begin Patch\n*** Add File: a.go\n+x\n*** End of File\n*** End Patch\n",
		"second begin line":      "*** Begin Patch\n*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n",
		"end line in the middle": "*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n*** Delete File: b.go\n*** End Patch\n",
	}

	for name, patch := range unsupported {
		t.Run("unsupported/"+name, func(t *testing.T) {
			event, err := codexEdits{}.DecodeEdit(patchEvent(t, patch))
			require.ErrorIs(t, err, ErrUnsupportedGrammar)
			assert.Empty(t, event.Paths, "a part of the paths must not look like complete coverage")
		})
	}
}

func TestCodexEditsAcceptTheKnownGrammarVariants(t *testing.T) {
	cases := map[string]struct {
		patch string
		paths []string
	}{
		// Codex trims a header line before it reads the name. A kept space
		// names a file that Codex does not touch.
		"trailing space on a header": {
			"*** Begin Patch\n*** Update File: a.go \n@@\n-x\n+y\n*** End Patch\n", []string{"a.go"}},
		"trailing space on a move": {
			"*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\t \n@@\n-x\n+y\n*** End Patch\n", []string{"a.go", "b.go"}},
		"indented header outside an update": {
			"*** Begin Patch\n  *** Delete File: a.go\n\t*** Add File: b.go\n+x\n*** End Patch\n", []string{"a.go", "b.go"}},
		// Inside an update hunk a leading space marks a context line.
		"indented header inside an update": {
			"*** Begin Patch\n*** Update File: a.go\n@@\n *** Delete File: b.go\n-x\n+y\n*** End Patch\n", []string{"a.go"}},
		"leading space in the name": {
			"*** Begin Patch\n*** Add File:  lead.go\n+x\n*** End Patch\n", []string{" lead.go"}},
		"heredoc wrapper": {
			"<<'EOF'\n*** Begin Patch\n*** Delete File: a.go\n*** End Patch\nEOF\n", []string{"a.go"}},
		"spaces around the envelope lines": {
			"*** Begin Patch  \n*** Delete File: a.go\n   *** End Patch\n", []string{"a.go"}},
		"no final newline": {"*** Begin Patch\n*** Delete File: a.go\n*** End Patch", []string{"a.go"}},
		"blank lines around the envelope": {
			"\n\n*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n\n", []string{"a.go"}},
		"carriage returns": {
			"*** Begin Patch\r\n*** Update File: a.go\r\n@@\r\n-x\r\n+y\r\n*** End Patch\r\n", []string{"a.go"}},
		"empty context line": {
			"*** Begin Patch\n*** Update File: a.go\n@@ func A() {\n a\n\n-b\n+c\n*** End Patch\n", []string{"a.go"}},
		"rename without a change": {
			"*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n*** End Patch\n", []string{"a.go", "b.go"}},
		// A content line that looks like a header starts with its marker,
		// so it never names a file.
		"header text inside the content": {
			"*** Begin Patch\n*** Add File: doc.md\n+*** Add File: fake.go\n+*** End Patch\n*** End Patch\n",
			[]string{"doc.md"}},
		"name with spaces and unicode": {
			"*** Begin Patch\n*** Add File: docs/my notes — v2.md\n+x\n*** End Patch\n",
			[]string{"docs/my notes — v2.md"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			event, err := codexEdits{}.DecodeEdit(patchEvent(t, tc.patch))
			require.NoError(t, err)
			assert.Equal(t, tc.paths, event.Paths)
		})
	}
}

func TestCodexEditsLeaveOtherEventsAlone(t *testing.T) {
	// A shell event is not an edit. The patch decoder never reads a shell
	// command, and the gate never reads a patch.
	for _, fixture := range []string{"pre_tool_use_bash.json", "post_compact.json", "subagent_start.json"} {
		_, err := codexEdits{}.DecodeEdit(codexFixture(t, fixture))
		require.ErrorIs(t, err, ErrNotApplicable, fixture)
	}

	// A shell command that holds a patch text stays a shell command.
	shell := strings.Replace(string(patchEvent(t, "*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n")),
		`"tool_name":"apply_patch"`, `"tool_name":"Bash"`, 1)
	_, err := codexEdits{}.DecodeEdit([]byte(shell))
	require.ErrorIs(t, err, ErrNotApplicable)
}

func TestCodexResetsNameNoContext(t *testing.T) {
	event, err := codexResets{}.DecodeReset(codexFixture(t, "post_compact.json"))
	require.NoError(t, err)
	assert.Equal(t, ResetEvent{}, event,
		"the edit decoder reads no receiver yet, so no state exists that a reset can clear")

	// Only the installed event is a reset. The session start that follows
	// a compaction is another event, and setup installs no hook for it.
	for _, fixture := range []string{"session_start_compact.json", "subagent_start.json", "pre_tool_use_bash.json"} {
		_, err := codexResets{}.DecodeReset(codexFixture(t, fixture))
		require.ErrorIs(t, err, ErrNotApplicable, fixture)
	}

	_, err = codexResets{}.DecodeReset([]byte("{not json"))
	require.ErrorIs(t, err, ErrMalformedEvent)
}

func TestCodexEditsEncodeTheNativeReply(t *testing.T) {
	// The documented PreToolUse reply: additionalContext reaches the model
	// and the tool is not blocked. encoding/json escapes <, >, and &.
	reply, err := codexEdits{}.EncodeAdvice("seamark — lessons for 2 files:\n- [pin · api] keep <T> & co\n")
	require.NoError(t, err)

	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":` +
		`"seamark — lessons for 2 files:\n- [pin · api] keep \u003cT\u003e \u0026 co\n"}}` + "\n"
	assert.Equal(t, want, string(reply.Stdout))
	assert.Empty(t, reply.Stderr)
	assert.Zero(t, reply.ExitCode, "advice never blocks the edit")
	assert.Equal(t, "pre-tool-use-context", codexEdits{}.AdviceMechanism())
}

// TestCodexEditsAgreeWithTheCodexParser is the differential check of
// the patch decoder. testdata/codex/apply_patch_oracle.json holds what
// codex-cli 0.154.0 did with each patch when it applied the patch
// offline. For every patch that Codex accepts, the decoder must name
// every file that Codex touched, by exactly the path that Codex used.
func TestCodexEditsAgreeWithTheCodexParser(t *testing.T) {
	var oracle struct {
		Cases []struct {
			Name     string   `json:"name"`
			Patch    string   `json:"patch"`
			Accepted bool     `json:"accepted"`
			Touched  []string `json:"touched"`
		} `json:"cases"`
	}

	require.NoError(t, json.Unmarshal(codexFixture(t, "apply_patch_oracle.json"), &oracle))
	require.NotEmpty(t, oracle.Cases)

	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			event, err := codexEdits{}.DecodeEdit(patchEvent(t, tc.Patch))

			switch {
			case !tc.Accepted:
				// Codex applies nothing, so any answer is safe. The decoder
				// must only stay inside its three documented results.
				if err != nil {
					assert.True(t, errors.Is(err, ErrMalformedEvent) || errors.Is(err, ErrUnsupportedGrammar), "%v", err)
				}
			case strings.Contains(tc.Patch, "*** Environment ID:"):
				// A deliberate refusal: the root of a named environment is
				// not known, so its paths cannot be resolved.
				require.ErrorIs(t, err, ErrUnsupportedGrammar)
				assert.Empty(t, event.Paths)
			default:
				require.NoError(t, err, "Codex accepts this patch, so the decoder must read it")
				assert.Subset(t, event.Paths, tc.Touched, "every file that Codex touched is in the set")

				// A move adds its source, which Codex does not report.
				// Without a move the two sets are equal.
				if !strings.Contains(tc.Patch, "*** Move to: ") {
					assert.ElementsMatch(t, tc.Touched, event.Paths)
				}
			}
		})
	}
}
