package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// completionPayload builds one native completion event from fields.
func completionPayload(t *testing.T, fields map[string]any) []byte {
	t.Helper()

	payload, err := json.Marshal(fields)
	require.NoError(t, err)

	return payload
}

// TestCompletionDecodersReadToolOutcome checks the K9 completion
// decoders (§5 row 37): Claude PostToolUse is succeeded and
// PostToolUseFailure is failed or interrupted; Codex PostToolUse takes
// the exit status of tool_response. The paths come from the tool input,
// and an error keeps the identity for the match join. The findings
// reply never blocks.
func TestCompletionDecodersReadToolOutcome(t *testing.T) {
	const handler = "/workspace/repo/internal/api/handler.go"

	claudeEvent := func(event, tool string, extra map[string]any) []byte {
		fields := map[string]any{
			"session_id": "synthetic-claude-session-0001", "cwd": "/workspace/repo",
			"hook_event_name": event, "tool_name": tool, "tool_use_id": "toolu_synthetic_0001",
			"tool_input": map[string]any{"file_path": handler, "old_string": "a", "new_string": "b"},
			// The decoder never reads the response, so an odd shape there
			// changes nothing.
			"tool_response": map[string]any{"filePath": 7, "structuredPatch": "SECRET"},
		}

		for k, v := range extra {
			fields[k] = v
		}

		return completionPayload(t, fields)
	}

	t.Run("claude", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			payload []byte
			tool    string
			outcome lessoncheck.ToolOutcome
		}{
			{"PostToolUse Edit", claudeEvent("PostToolUse", "Edit", nil), "Edit", lessoncheck.ToolSucceeded},
			{"PostToolUse Write", claudeEvent("PostToolUse", "Write", nil), "Write", lessoncheck.ToolSucceeded},
			{"PostToolUseFailure MultiEdit", claudeEvent("PostToolUseFailure", "MultiEdit",
				map[string]any{"error": "String to replace not found", "is_interrupt": false}), "MultiEdit", lessoncheck.ToolFailed},
			{"PostToolUseFailure interrupted", claudeEvent("PostToolUseFailure", "Edit",
				map[string]any{"error": "The operation was aborted.", "is_interrupt": true}), "Edit", lessoncheck.ToolInterrupted},
		} {
			c, err := claudeCompletions{}.DecodeCompletion(tc.payload)
			require.NoError(t, err, tc.name)
			assert.Equal(t, lessoncheck.Completion{
				Client: ClaudeID, Tool: tc.tool, SessionID: "synthetic-claude-session-0001",
				MatchID: "toolu_synthetic_0001", CWD: "/workspace/repo", Outcome: tc.outcome, Paths: []string{handler},
			}, c, tc.name)
		}

		for _, tc := range []struct {
			name    string
			payload []byte
			want    error
		}{
			{"PreToolUse", claudeEvent("PreToolUse", "Edit", nil), ErrNotApplicable},
			{"no event name", claudeEvent("", "Edit", nil), ErrNotApplicable},
			// The native recording of a failed Bash call: another tool.
			{"a recorded Bash failure", claudeFixture(t, "native/hooks/004-PostToolUseFailure.json"), ErrNotApplicable},
			{"a Read", claudeEvent("PostToolUse", "Read", nil), ErrNotApplicable},
			{"no tool_name", claudeEvent("PostToolUse", "", nil), ErrMalformedEvent},
			{"no file_path", claudeEvent("PostToolUse", "Write", map[string]any{"tool_input": map[string]any{}}), ErrMalformedEvent},
			{"an interrupt flag that is no bool", claudeEvent("PostToolUseFailure", "Edit",
				map[string]any{"is_interrupt": "yes"}), ErrMalformedEvent},
		} {
			c, err := claudeCompletions{}.DecodeCompletion(tc.payload)
			require.ErrorIs(t, err, tc.want, tc.name)
			assert.Empty(t, c.Paths, tc.name)
			assert.Equal(t, ClaudeID, c.Client, tc.name)
			assert.NotContains(t, err.Error(), "SECRET", tc.name)
		}

		// The client never comes from the payload.
		c, err := claudeCompletions{}.DecodeCompletion(claudeEvent("PostToolUse", "Edit", map[string]any{"client": CodexID}))
		require.NoError(t, err)
		assert.Equal(t, ClaudeID, c.Client)
	})

	codexEvent := func(event, tool string, response any, command any) []byte {
		return completionPayload(t, map[string]any{
			"session_id": "thr_synthetic_0101", "turn_id": "turn_synthetic_0101", "cwd": "/workspace/repo",
			"hook_event_name": event, "tool_name": tool, "tool_use_id": "call_synthetic_0101",
			"tool_input": map[string]any{"command": command}, "tool_response": response,
		})
	}

	addPatch := "*** Begin Patch\n*** Add File: notes.txt\n+note\n*** Update File: a.txt\n*** Move to: b.txt\n@@\n-x\n+y\n*** End Patch\n"

	t.Run("codex", func(t *testing.T) {
		// The synthetic capture fixture of an apply_patch success.
		c, err := codexCompletions{}.DecodeCompletion(codexFixture(t, "post_tool_use.json"))
		require.NoError(t, err)
		assert.Equal(t, lessoncheck.Completion{
			Client: CodexID, Tool: "apply_patch", SessionID: "thr_synthetic_0101", MatchID: "call_synthetic_0101",
			CWD: "/workspace/repo", Outcome: lessoncheck.ToolSucceeded, Paths: []string{"notes.txt"},
		}, c)

		for _, tc := range []struct {
			name     string
			response any
			outcome  lessoncheck.ToolOutcome
		}{
			{"the exec header with exit 0", "Exit code: 0\nWall time: 0 seconds\nOutput:\nSuccess.\n", lessoncheck.ToolSucceeded},
			{"the exec header with exit 1", "Exit code: 1\nWall time: 0.1 seconds\nOutput:\nerror: no match\n", lessoncheck.ToolFailed},
			{"an object with a non-zero exit_code", map[string]any{"exit_code": 2, "output": "x"}, lessoncheck.ToolFailed},
			{"an object with exit_code 0", map[string]any{"exit_code": 0}, lessoncheck.ToolSucceeded},
			{"text without the header", "Success. Updated the following files:\nA notes.txt\n", lessoncheck.ToolUnknown},
			{"no response", nil, lessoncheck.ToolUnknown},
		} {
			c, err := codexCompletions{}.DecodeCompletion(codexEvent("PostToolUse", "apply_patch", tc.response, addPatch))
			require.NoError(t, err, tc.name)
			assert.Equal(t, tc.outcome, c.Outcome, tc.name)
			assert.Equal(t, []string{"notes.txt", "a.txt", "b.txt"}, c.Paths, "%s: a move names both ends", tc.name)
		}

		for _, tc := range []struct {
			name    string
			payload []byte
			want    error
		}{
			// The synthetic capture fixture of a failed shell command.
			{"a Bash failure", codexFixture(t, "post_tool_use_nonzero_exit.json"), ErrNotApplicable},
			{"PreToolUse", codexEvent("PreToolUse", "apply_patch", nil, addPatch), ErrNotApplicable},
			{"no tool_name", codexEvent("PostToolUse", "", nil, addPatch), ErrMalformedEvent},
			{"no command", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n", nil),
				ErrMalformedEvent},
			{"a truncated envelope", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Delete File: a.txt\n"), ErrMalformedEvent},
			{"an environment line", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Environment ID: env1\n*** Delete File: a.txt\n*** End Patch"), ErrUnsupportedGrammar},
		} {
			c, err := codexCompletions{}.DecodeCompletion(tc.payload)
			require.ErrorIs(t, err, tc.want, tc.name)
			assert.Empty(t, c.Paths, tc.name)
			assert.Equal(t, CodexID, c.Client, tc.name)
			assert.NotEmpty(t, c.MatchID, "%s: the post-edit record still joins the guard record", tc.name)
		}

		// A patch error keeps the outcome: the tool ran and failed.
		c, err = codexCompletions{}.DecodeCompletion(codexEvent("PostToolUse", "apply_patch",
			"Exit code: 1\nWall time: 0 seconds\nOutput:\n", "*** Begin Patch\n*** Copy File: a.txt\n*** End Patch"))
		require.ErrorIs(t, err, ErrUnsupportedGrammar)
		assert.Equal(t, lessoncheck.ToolFailed, c.Outcome)
	})

	t.Run("findings replies never block", func(t *testing.T) {
		for _, tc := range []struct {
			outcome lessoncheck.ToolOutcome
			event   string
		}{
			{lessoncheck.ToolSucceeded, "PostToolUse"},
			{lessoncheck.ToolFailed, "PostToolUseFailure"},
			{lessoncheck.ToolInterrupted, "PostToolUseFailure"},
		} {
			reply, err := claudeCompletions{}.EncodeFindings("post-edit finding", tc.outcome)
			require.NoError(t, err)
			assert.Zero(t, reply.ExitCode)
			assert.Empty(t, reply.Stderr)
			assert.JSONEq(t,
				`{"hookSpecificOutput":{"hookEventName":"`+tc.event+`","additionalContext":"post-edit finding"}}`,
				string(reply.Stdout), "the reply names the event that it answers (%s)", tc.outcome)
		}

		reply, err := codexCompletions{}.EncodeFindings("post-edit finding", lessoncheck.ToolFailed)
		require.NoError(t, err)
		assert.Zero(t, reply.ExitCode, "stderr with exit 0 reaches the model and never blocks")
		assert.Empty(t, reply.Stdout)
		assert.Equal(t, "post-edit finding", string(reply.Stderr))
	})

	t.Run("every capture outcome converts to a tool outcome", func(t *testing.T) {
		assert.Equal(t, lessoncheck.ToolUnknown, toolOutcomeOf(OutcomeNone))

		for _, o := range []Outcome{OutcomeSucceeded, OutcomeFailed, OutcomeInterrupted, OutcomeDenied, OutcomeUnknown} {
			assert.Equal(t, string(o), string(toolOutcomeOf(o)), "contracts §1.8: one word list")
		}
	})
}
