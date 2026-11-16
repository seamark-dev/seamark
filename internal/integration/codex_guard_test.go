package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// TestCodexGuardDecodesApplyPatchOps checks the Codex guard decoder: an
// apply_patch event gives one file operation per section, in patch
// order, with the raw paths, and the identity of the event. An update
// with hunks has complete paths and Hunks set, because its content has
// no verified projection yet.
func TestCodexGuardDecodesApplyPatchOps(t *testing.T) {
	cases := []struct {
		fixture  string
		match    string
		receiver string
		ops      []lessoncheck.FileOp
	}{
		{"pre_tool_use_apply_patch_add.json", "call_synthetic_0001", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchAdd, Path: "docs/new-page.md", Content: strPtr("# New page\n\nBody.\n")},
		}},
		{"pre_tool_use_apply_patch_update.json", "call_synthetic_0002", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchUpdate, Path: "internal/api/handler.go", Hunks: true},
		}},
		{"pre_tool_use_apply_patch_delete.json", "call_synthetic_0003", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchDelete, Path: "internal/api/obsolete.go"},
		}},
		// A move with a hunk changes the content, so it is an update with
		// a move target, not a plain move.
		{"pre_tool_use_apply_patch_move.json", "call_synthetic_0004", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchUpdate, Path: "internal/api/old_name.go", MoveTo: "internal/api/new_name.go", Hunks: true},
		}},
		{"pre_tool_use_apply_patch_multi.json", "call_synthetic_0005", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchAdd, Path: "hello.txt", Content: strPtr("Hello world\n")},
			{Kind: lessoncheck.OpPatchUpdate, Path: "src/app.py", MoveTo: "src/main.py", Hunks: true},
			{Kind: lessoncheck.OpPatchDelete, Path: "obsolete.txt"},
		}},
		{"pre_tool_use_apply_patch_end_of_file.json", "call_synthetic_0010", receiverID("thr_synthetic_0001"), []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchUpdate, Path: "README.md", Hunks: true},
		}},
		// A subagent edit names the child receiver, by the rule of the
		// advisory decoder.
		{"pre_tool_use_apply_patch_subagent.json", "call_synthetic_0012",
			receiverID("thr_synthetic_0001", "agent_synthetic_0001"), []lessoncheck.FileOp{
				{Kind: lessoncheck.OpPatchUpdate, Path: "/workspace/repo/src/child.txt", Hunks: true},
			}},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			op, err := codexGuards{}.DecodeGuard(codexFixture(t, tc.fixture))
			require.NoError(t, err)

			assert.Equal(t, lessoncheck.Operation{
				Client:        CodexID,
				Tool:          "apply_patch",
				Ops:           tc.ops,
				SessionID:     "thr_synthetic_0001",
				MatchID:       tc.match,
				CWD:           "/workspace/repo",
				Receiver:      tc.receiver,
				PathsComplete: true,
			}, op)

			event, err := codexEdits{}.DecodeEdit(codexFixture(t, tc.fixture))
			require.NoError(t, err)
			require.NotNil(t, event.Context)
			assert.Equal(t, event.Context.ID, op.Receiver, "the guard and the advisory hook name one receiver")
		})
	}

	t.Run("a move without a hunk keeps its content", func(t *testing.T) {
		op, err := codexGuards{}.DecodeGuard(patchEvent(t,
			"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b/a.txt\n*** Delete File: c.txt\n*** End Patch"))
		require.NoError(t, err)
		assert.Equal(t, []lessoncheck.FileOp{
			{Kind: lessoncheck.OpPatchMove, Path: "a.txt", MoveTo: "b/a.txt"},
			{Kind: lessoncheck.OpPatchDelete, Path: "c.txt"},
		}, op.Ops)
	})

	t.Run("the client comes from the descriptor", func(t *testing.T) {
		var doc map[string]any
		require.NoError(t, json.Unmarshal(codexFixture(t, "pre_tool_use_apply_patch_add.json"), &doc))

		doc["client"] = ClaudeID

		payload, err := json.Marshal(doc)
		require.NoError(t, err)

		op, err := codexGuards{}.DecodeGuard(payload)
		require.NoError(t, err)
		assert.Equal(t, CodexID, op.Client)
	})

	t.Run("errors keep the identity and give no operation", func(t *testing.T) {
		missing, err := json.Marshal(map[string]any{
			"session_id": "thr_1", "cwd": "/workspace/repo", "hook_event_name": "PreToolUse",
			"tool_name": "apply_patch", "tool_use_id": "call_1", "tool_input": map[string]any{},
		})
		require.NoError(t, err)

		for _, tc := range []struct {
			name    string
			payload []byte
			want    error
			tool    string
		}{
			// unparseable-input: no envelope, no hunk, no command.
			{"a truncated envelope", codexFixture(t, "pre_tool_use_apply_patch_malformed.json"), ErrMalformedEvent, "apply_patch"},
			{"a patch without a hunk", patchEvent(t, "*** Begin Patch\n*** End Patch"), ErrMalformedEvent, "apply_patch"},
			{"no command", missing, ErrMalformedEvent, "apply_patch"},
			{"no tool_name", []byte(`{"session_id": "thr_1", "tool_use_id": "call_1"}`), ErrMalformedEvent, ""},
			// incomplete-paths: the grammar cannot name every path.
			{"an environment line", patchEvent(t,
				"*** Begin Patch\n*** Environment ID: env1\n*** Delete File: b.txt\n*** End Patch"), ErrUnsupportedGrammar, "apply_patch"},
			{"an unknown header", patchEvent(t, "*** Begin Patch\n*** Copy File: b.txt\n*** End Patch"),
				ErrUnsupportedGrammar, "apply_patch"},
			// tool-uncovered: the shell tool is never read as a patch.
			{"the shell tool", codexFixture(t, "pre_tool_use_bash.json"), ErrNotApplicable, "Bash"},
		} {
			op, err := codexGuards{}.DecodeGuard(tc.payload)
			require.ErrorIs(t, err, tc.want, tc.name)
			assert.Empty(t, op.Ops, tc.name)
			assert.False(t, op.PathsComplete, tc.name)
			assert.Equal(t, CodexID, op.Client, tc.name)
			assert.Equal(t, tc.tool, op.Tool, tc.name)
			assert.NotEmpty(t, op.MatchID, "%s: the audit record still joins by the match", tc.name)
		}
	})

	// codex-cli 0.169.0 removes a tab or a carriage return from a header
	// path when it writes. The guard must not judge the path that the
	// text names, so it refuses the patch. The advisory hook keeps its
	// paths, the character included.
	t.Run("a tab or a carriage return in a header path refuses the patch", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			patch    string
			advisory []string
		}{
			{"a carriage return in an add path",
				"*** Begin Patch\n*** Add File: sen\rtinel/x.txt\n+x\n*** End Patch", []string{"sen\rtinel/x.txt"}},
			{"a tab in an update path",
				"*** Begin Patch\n*** Update File: sen\ttinel/x.txt\n@@\n-a\n+b\n*** End Patch", []string{"sen\ttinel/x.txt"}},
			{"a carriage return in a delete path",
				"*** Begin Patch\n*** Delete File: sentinel\r/x.txt\n*** End Patch", []string{"sentinel\r/x.txt"}},
			{"a tab in a move target",
				"*** Begin Patch\n*** Update File: a.txt\n*** Move to: sen\ttinel/m.txt\n@@\n-a\n+b\n*** End Patch",
				[]string{"a.txt", "sen\ttinel/m.txt"}},
			{"a tab at the start of a path",
				"*** Begin Patch\n*** Add File: \tsentinel/x.txt\n+x\n*** End Patch", []string{"\tsentinel/x.txt"}},
			{"a carriage return in the second section of a clean patch",
				"*** Begin Patch\n*** Delete File: a.txt\n*** Add File: sen\rtinel/x.txt\n+x\n*** End Patch",
				[]string{"a.txt", "sen\rtinel/x.txt"}},
		} {
			op, err := codexGuards{}.DecodeGuard(patchEvent(t, tc.patch))
			require.ErrorIs(t, err, ErrUnsupportedGrammar, tc.name)
			assert.NotContains(t, err.Error(), "tinel", "%s: the error text names no path", tc.name)
			assert.Empty(t, op.Ops, tc.name)
			assert.False(t, op.PathsComplete, tc.name)
			assert.Equal(t, CodexID, op.Client, tc.name)
			assert.Equal(t, "apply_patch", op.Tool, tc.name)
			assert.Equal(t, "call_1", op.MatchID, "%s: the audit record still joins by the match", tc.name)

			event, err := codexEdits{}.DecodeEdit(patchEvent(t, tc.patch))
			require.NoError(t, err, tc.name)
			assert.Equal(t, tc.advisory, event.Paths, "%s: the advisory hook keeps the raw paths", tc.name)
		}

		// A tab or a carriage return at the end of a header line goes in
		// the line trim, in Codex and in the decoder, so no path holds it.
		op, err := codexGuards{}.DecodeGuard(patchEvent(t,
			"*** Begin Patch\n*** Add File: x.txt\t\r\n+x\n*** Update File: a.txt\n*** Move to: b.txt\t\n@@\n-a\n+b\n*** End Patch"))
		require.NoError(t, err)
		assert.Equal(t, []string{"x.txt", "a.txt", "b.txt"}, patchOpPaths(op.Ops))
	})

	t.Run("the command gate keeps refusing a patch", func(t *testing.T) {
		_, err := codexCommands{}.DecodeCommand(codexFixture(t, "pre_tool_use_apply_patch_add.json"))
		require.ErrorIs(t, err, ErrNotApplicable)
	})
}
