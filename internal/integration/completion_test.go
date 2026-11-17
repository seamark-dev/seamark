package integration

import (
	"encoding/json"
	"fmt"
	"strings"
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

// TestCompletionDecodersReadToolOutcome checks the completion decoders
// of the post-edit verification: Claude PostToolUse is succeeded and
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
		// The synthetic fixture of an apply_patch success.
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
			// The synthetic fixture of a failed shell command.
			{"a Bash failure", codexFixture(t, "post_tool_use_nonzero_exit.json"), ErrNotApplicable},
			{"PreToolUse", codexEvent("PreToolUse", "apply_patch", nil, addPatch), ErrNotApplicable},
			{"no tool_name", codexEvent("PostToolUse", "", nil, addPatch), ErrMalformedEvent},
			{"no command", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n", nil),
				ErrMalformedEvent},
			{"a truncated envelope", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Delete File: a.txt\n"), ErrMalformedEvent},
			{"an environment line", codexEvent("PostToolUse", "apply_patch", "Exit code: 1\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Environment ID: env1\n*** Delete File: a.txt\n*** End Patch"), ErrUnsupportedGrammar},
			// Codex removes a tab or a carriage return from a header path
			// when it writes, so the named path is not the path on disk.
			{"a tab in a delete path", codexEvent("PostToolUse", "apply_patch", "Exit code: 0\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Delete File: sen\ttinel/x.txt\n*** End Patch"), ErrUnsupportedGrammar},
			{"a carriage return in a move target", codexEvent("PostToolUse", "apply_patch", "Exit code: 0\nWall time: 0 seconds\nOutput:\n",
				"*** Begin Patch\n*** Update File: a.txt\n*** Move to: sen\rtinel/m.txt\n@@\n-a\n+b\n*** End Patch"), ErrUnsupportedGrammar},
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

		// A refused path keeps the outcome too: the patch wrote a file.
		c, err = codexCompletions{}.DecodeCompletion(codexEvent("PostToolUse", "apply_patch",
			"Exit code: 0\nWall time: 0 seconds\nOutput:\n", "*** Begin Patch\n*** Add File: sen\rtinel/x.txt\n+x\n*** End Patch"))
		require.ErrorIs(t, err, ErrUnsupportedGrammar)
		assert.NotContains(t, err.Error(), "tinel", "the error text names no path")
		assert.Equal(t, lessoncheck.ToolSucceeded, c.Outcome)
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

		// Codex has no failure event, so every outcome gets the
		// PostToolUse reply. On codex-cli 0.169.0 a patch that fails
		// during its execution gets no PostToolUse, so the failed outcome
		// has no native case. Its stderr text with exit 0 never reached
		// the model on codex-cli 0.164.0.
		for _, outcome := range []lessoncheck.ToolOutcome{
			lessoncheck.ToolSucceeded, lessoncheck.ToolFailed, lessoncheck.ToolUnknown,
		} {
			reply, err := codexCompletions{}.EncodeFindings("post-edit finding", outcome)
			require.NoError(t, err)
			assert.Equal(t, HookReply{
				Stdout: []byte(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"post-edit finding"}}` + "\n"),
			}, reply, "the reply is the exact PostToolUse context on stdout with exit 0 (%s)", outcome)
		}
	})

	t.Run("every Outcome converts to a tool outcome", func(t *testing.T) {
		assert.Equal(t, lessoncheck.ToolUnknown, toolOutcomeOf(OutcomeNone))

		for _, o := range []Outcome{OutcomeSucceeded, OutcomeFailed, OutcomeInterrupted, OutcomeDenied, OutcomeUnknown} {
			assert.Equal(t, string(o), string(toolOutcomeOf(o)), "both lists hold the same words")
		}
	})
}

// TestCodexFindingsReplyAtTheLineCap checks the Codex reply for a
// findings text with all the result lines that the line cap of the
// post-edit hook keeps: the summary line, 20 result lines, the line that
// counts the rest, the note of a failed tool, and the rerun line. The
// paths are short, so the text fits in codexMaxFindingsBytes and the
// reply holds every byte. The line cap alone does not bound the bytes;
// TestCodexFindingsReplyBoundsALongPatch covers a longer text. The JSON
// encoder writes a line end and "&" as escapes, and a JSON reader gets
// the same text back.
func TestCodexFindingsReplyAtTheLineCap(t *testing.T) {
	var b strings.Builder

	b.WriteString("seamark post-edit lesson checks after this apply_patch call: violation " +
		"(23 evaluated pins: 23 violation, 0 unknown; snapshot 0123456789ab)\n")

	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "- violation: pin %012x, check no-todo (no-added-pattern/1): docs/R&D/note-%02d.md:%d "+
			"adds a line that matches the pattern\n", i, i, i)
	}

	b.WriteString("- and 3 more\n")
	b.WriteString("The apply_patch tool failed, so the checks ran on the files as they are now.\n")
	b.WriteString("The files stay as written; repair the violation in a later edit. Check again: seamark lessons --verify\n")

	text := b.String()
	require.Equal(t, 21, strings.Count(text, "\n- "), "20 result lines and the line that counts the rest")
	require.LessOrEqual(t, len(text), codexMaxFindingsBytes, "this text fits the byte bound of the reply")

	reply, err := codexCompletions{}.EncodeFindings(text, lessoncheck.ToolFailed)
	require.NoError(t, err)

	escaped := strings.NewReplacer("\n", `\n`, "&", fmt.Sprintf(`\u%04x`, '&')).Replace(text)
	assert.Equal(t, HookReply{
		Stdout: []byte(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"` + escaped + `"}}` + "\n"),
	}, reply, "the exact JSON of the text at the line cap")

	var decoded hookContextReply
	require.NoError(t, json.Unmarshal(reply.Stdout, &decoded))
	assert.Equal(t, text, decoded.HookSpecificOutput.AdditionalContext, "the model context holds the same bytes")
}

// TestCodexFindingsReplyAtTheByteCap checks the exact JSON of the Codex
// reply at codexMaxFindingsBytes. A text of exactly that size keeps
// every byte. One more line makes the text too long, so the reply keeps
// the first lines that fit and a line that counts the rest. That line
// names the rerun command without --file arguments, because the reply
// never holds a cut command.
func TestCodexFindingsReplyAtTheByteCap(t *testing.T) {
	const head = "seamark post-edit lesson checks after this apply_patch call: incomplete " +
		"(1 evaluated pin: 0 violation, 1 unknown; snapshot none)\n"

	// The result line fills the text to the bound exactly.
	const resultHead = "- unknown/missing-baseline: pin 0123456789ab, check no-todo (no-added-pattern/1): "

	atCap := head + resultHead + strings.Repeat("x", codexMaxFindingsBytes-len(head)-len(resultHead)-1) + "\n"
	require.Len(t, atCap, codexMaxFindingsBytes)

	reply, err := codexCompletions{}.EncodeFindings(atCap, lessoncheck.ToolSucceeded)
	require.NoError(t, err)
	assert.Equal(t, HookReply{
		Stdout: []byte(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"` +
			strings.ReplaceAll(atCap, "\n", `\n`) + `"}}` + "\n"),
	}, reply, "a text at the bound keeps every byte")

	overCap := atCap + "Check again: seamark lessons --verify --file /work/repo/src/a.go\n"

	reply, err = codexCompletions{}.EncodeFindings(overCap, lessoncheck.ToolSucceeded)
	require.NoError(t, err)
	assert.Equal(t, HookReply{
		Stdout: []byte(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"` +
			strings.ReplaceAll(head, "\n", `\n`) +
			`- and 2 more lines, left out to fit the context limit of the client. Check again: seamark lessons --verify\n"}}` + "\n"),
	}, reply, "a text above the bound keeps the lines that fit and counts the rest")
}

// TestCodexFindingsReplyBoundsALongPatch checks the Codex reply for the
// findings of one apply_patch call on 100 files without a baseline. The
// line cap keeps the text to a few lines, but the unknown line and the
// rerun command list every path, so the text holds many kilobytes.
// Codex bounds model-visible context with additionalContextLimit, about
// 2,500 tokens by default. So the reply keeps the first lines that fit
// in codexMaxFindingsBytes, and it never holds a cut rerun command.
func TestCodexFindingsReplyBoundsALongPatch(t *testing.T) {
	cfg, errs, err := lessoncheck.Parse([]byte(`lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: no-todo
    region: src
    mode: check
    check: no-todo
`))
	require.NoError(t, err)
	require.Empty(t, errs)

	paths := make([]string, 100)
	for i := range paths {
		paths[i] = fmt.Sprintf("src/module-%03d/internal/handler_test.go", i)
	}

	out := lessoncheck.EvaluatePostEdit(lessoncheck.PostEditInput{
		Config: cfg, Paths: paths, PathsComplete: true,
		Root: "/Users/someone/Documents/projects/example-repo", Tool: codexApplyPatch, Outcome: lessoncheck.ToolSucceeded,
	})
	require.Greater(t, len(out.Findings), 3*codexMaxFindingsBytes, "the findings of a long patch are far above the bound")
	require.Equal(t, 4, strings.Count(out.Findings, "\n"),
		"the summary, the unknown line, the note of the missing baseline, and the rerun line")

	reply, err := codexCompletions{}.EncodeFindings(out.Findings, lessoncheck.ToolSucceeded)
	require.NoError(t, err)
	assert.Empty(t, reply.Stderr)
	assert.Zero(t, reply.ExitCode)

	var decoded hookContextReply
	require.NoError(t, json.Unmarshal(reply.Stdout, &decoded))

	modelContext := decoded.HookSpecificOutput.AdditionalContext
	assert.LessOrEqual(t, len(modelContext), codexMaxFindingsBytes, "the model context stays inside the default additionalContextLimit")

	// The unknown line alone is longer than the bound, so only the
	// summary line stays.
	assert.Equal(t, strings.SplitAfter(out.Findings, "\n")[0]+
		"- and 3 more lines, left out to fit the context limit of the client. Check again: seamark lessons --verify\n",
		modelContext, "the reply keeps the first lines that fit and counts the rest")
	assert.NotContains(t, modelContext, "--file", "the reply never holds a cut rerun command")
}
