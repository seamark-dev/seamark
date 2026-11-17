package integration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// strPtr returns a pointer to a copy of s, for the expected content of
// a write or an add.
func strPtr(s string) *string { return &s }

// claudeGuardPayload builds a PreToolUse event of one tool with the
// given tool input, for the cases that no fixture covers.
func claudeGuardPayload(t *testing.T, tool string, input any) []byte {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"session_id": "synthetic-claude-session-0001", "cwd": "/workspace/repo",
		"hook_event_name": "PreToolUse", "tool_name": tool, "tool_use_id": "toolu_synthetic_0099",
		"tool_input": input,
	})
	require.NoError(t, err)

	return payload
}

// TestClaudeGuardDecodesEditTools checks the Claude guard decoder: each
// covered tool gives one complete file operation with its raw path, its
// content or its replacements, and the identity of the event. The
// receiver is the one of the advisory edit decoder.
func TestClaudeGuardDecodesEditTools(t *testing.T) {
	const handler = "/workspace/repo/internal/api/handler.go"

	root := receiverID("synthetic-claude-session-0001")
	child := receiverID("synthetic-claude-session-0001", "synthetic-agent-0007")

	cases := []struct {
		fixture  string
		tool     string
		match    string
		receiver string
		op       lessoncheck.FileOp
	}{
		{"pre_tool_use_edit.json", "Edit", "toolu_synthetic_0001", root, lessoncheck.FileOp{
			Kind: lessoncheck.OpEdit, Path: handler,
			Edits: []lessoncheck.TextEdit{{Old: "return nil", New: "return err"}},
		}},
		{"pre_tool_use_edit_subagent.json", "Edit", "toolu_synthetic_0005", child, lessoncheck.FileOp{
			Kind: lessoncheck.OpEdit, Path: handler,
			Edits: []lessoncheck.TextEdit{{Old: "return nil", New: "return err"}},
		}},
		{"pre_tool_use_edit_replace_all.json", "Edit", "toolu_synthetic_0006", root, lessoncheck.FileOp{
			Kind: lessoncheck.OpEdit, Path: handler,
			Edits: []lessoncheck.TextEdit{{Old: "oldName", New: "newName", ReplaceAll: true}},
		}},
		{"pre_tool_use_write.json", "Write", "toolu_synthetic_0002", root, lessoncheck.FileOp{
			Kind: lessoncheck.OpWrite, Path: "/workspace/repo/docs/new-page.md", Content: strPtr("# New page\n"),
		}},
		{"pre_tool_use_write_new_file.json", "Write", "toolu_synthetic_0008", child, lessoncheck.FileOp{
			Kind: lessoncheck.OpWrite, Path: "/workspace/repo/internal/api/new_file.go",
			Content: strPtr("package api\n\n// TODO: fill in\n"),
		}},
		{"pre_tool_use_multiedit.json", "MultiEdit", "toolu_synthetic_0003", root, lessoncheck.FileOp{
			Kind: lessoncheck.OpMultiEdit, Path: handler,
			Edits: []lessoncheck.TextEdit{{Old: "a", New: "b"}, {Old: "c", New: "d"}},
		}},
		// The decoder keeps an empty old_string. The projection then gives
		// an add of the absent file with the new_string of the first edit.
		{"pre_tool_use_multiedit_create.json", "MultiEdit", "toolu_synthetic_0007", root, lessoncheck.FileOp{
			Kind: lessoncheck.OpMultiEdit, Path: "/workspace/repo/docs/created-by-multiedit.md",
			Edits: []lessoncheck.TextEdit{{Old: "", New: "# Created\n\nDraft.\n"}, {Old: "Draft.", New: "Final."}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			op, err := claudeGuards{}.DecodeGuard(claudeFixture(t, tc.fixture))
			require.NoError(t, err)

			assert.Equal(t, lessoncheck.Operation{
				Client:        ClaudeID,
				Tool:          tc.tool,
				Ops:           []lessoncheck.FileOp{tc.op},
				SessionID:     "synthetic-claude-session-0001",
				MatchID:       tc.match,
				CWD:           "/workspace/repo",
				Receiver:      tc.receiver,
				PathsComplete: true,
			}, op)

			// The guard and the advisory hook name one receiver for one
			// event, so their context digests join.
			event, err := claudeEdits{}.DecodeEdit(claudeFixture(t, tc.fixture))
			require.NoError(t, err)
			require.NotNil(t, event.Context)
			assert.Equal(t, event.Context.ID, op.Receiver)
		})
	}

	t.Run("the client comes from the descriptor", func(t *testing.T) {
		var doc map[string]any
		require.NoError(t, json.Unmarshal(claudeFixture(t, "pre_tool_use_edit.json"), &doc))

		doc["client"], doc["client_id"] = CodexID, CodexID

		payload, err := json.Marshal(doc)
		require.NoError(t, err)

		op, err := claudeGuards{}.DecodeGuard(payload)
		require.NoError(t, err)
		assert.Equal(t, ClaudeID, op.Client)
	})

	t.Run("an absent replace_all and a null one are false", func(t *testing.T) {
		for _, input := range []map[string]any{
			{"file_path": "a.go", "old_string": "x", "new_string": "y"},
			{"file_path": "a.go", "old_string": "x", "new_string": "y", "replace_all": nil},
		} {
			op, err := claudeGuards{}.DecodeGuard(claudeGuardPayload(t, "Edit", input))
			require.NoError(t, err)
			require.Len(t, op.Ops, 1)
			assert.Equal(t, []lessoncheck.TextEdit{{Old: "x", New: "y"}}, op.Ops[0].Edits)
		}
	})

	t.Run("an empty write content and an empty old_string are requests", func(t *testing.T) {
		op, err := claudeGuards{}.DecodeGuard(claudeGuardPayload(t, "Write", map[string]any{"file_path": "a.go", "content": ""}))
		require.NoError(t, err)
		assert.Equal(t, strPtr(""), op.Ops[0].Content)

		op, err = claudeGuards{}.DecodeGuard(claudeGuardPayload(t, "Edit",
			map[string]any{"file_path": "a.go", "old_string": "", "new_string": "new"}))
		require.NoError(t, err)
		assert.Equal(t, []lessoncheck.TextEdit{{Old: "", New: "new"}}, op.Ops[0].Edits)
	})

	t.Run("an uncovered tool is not applicable and keeps its identity", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			payload []byte
			tool    string
		}{
			{"Bash", claudeFixture(t, "pre_tool_use_bash.json"), "Bash"},
			{"NotebookEdit", claudeGuardPayload(t, "NotebookEdit",
				map[string]any{"notebook_path": "/workspace/repo/a.ipynb", "new_source": "x"}), "NotebookEdit"},
			{"a lowercase name", claudeGuardPayload(t, "edit",
				map[string]any{"file_path": "a.go", "old_string": "x", "new_string": "y"}), "edit"},
		} {
			op, err := claudeGuards{}.DecodeGuard(tc.payload)
			require.ErrorIs(t, err, ErrNotApplicable, tc.name)
			assert.Equal(t, tc.tool, op.Tool, "the audit names the uncovered tool")
			assert.Equal(t, ClaudeID, op.Client)
			assert.NotEmpty(t, op.MatchID)
			assert.Empty(t, op.Ops)
			assert.False(t, op.PathsComplete)
		}
	})
}

// TestClaudeGuardRefusesMalformedInput checks that the Claude guard
// decoder fails closed: every covered request that it cannot read is
// ErrMalformedEvent (the guard reason unparseable-input), never a part
// of the edit. The error text names no payload value, because the guard
// puts it into the deny reason.
func TestClaudeGuardRefusesMalformedInput(t *testing.T) {
	const secret = "SECRET-CONTENT-7f3a"

	cases := []struct {
		name    string
		payload []byte
		tool    string // the tool that the error result still names
		text    string // a part of the error text
	}{
		{"invalid JSON", []byte(`{"tool_name": "Write", "tool_input": {"content": "` + secret), "", "invalid JSON syntax"},
		{"a payload that is no object", []byte(`["` + secret + `"]`), "", "wrong JSON type"},
		{"no tool_name", claudeGuardPayload(t, "", map[string]any{"file_path": "a.go", "content": secret}), "", "no tool_name"},
		{"no tool_input", claudeGuardPayload(t, "Write", nil), "Write", "names no file_path"},
		{"a tool_input that is no object", claudeGuardPayload(t, "Edit", secret), "Edit", "tool_input has the wrong JSON type"},
		{"Write without file_path", claudeGuardPayload(t, "Write", map[string]any{"content": secret}), "Write", "names no file_path"},
		{"Write with an empty file_path", claudeGuardPayload(t, "Write",
			map[string]any{"file_path": "", "content": secret}), "Write", "names no file_path"},
		{"Write without content", claudeGuardPayload(t, "Write", map[string]any{"file_path": "a.go"}), "Write", "has no content"},
		{"Write with a null content", claudeGuardPayload(t, "Write",
			map[string]any{"file_path": "a.go", "content": nil}), "Write", "has no content"},
		{"Write with a content that is no string", claudeGuardPayload(t, "Write",
			map[string]any{"file_path": "a.go", "content": 7}), "Write", "tool_input.content has the wrong JSON type"},
		{"Edit without file_path", claudeGuardPayload(t, "Edit",
			map[string]any{"old_string": secret, "new_string": "y"}), "Edit", "names no file_path"},
		{"Edit without old_string", claudeGuardPayload(t, "Edit",
			map[string]any{"file_path": "a.go", "new_string": secret}), "Edit", "has no old_string"},
		{"Edit without new_string", claudeGuardPayload(t, "Edit",
			map[string]any{"file_path": "a.go", "old_string": secret}), "Edit", "has no new_string"},
		{"Edit with a replace_all that is no bool", claudeGuardPayload(t, "Edit",
			map[string]any{"file_path": "a.go", "old_string": "x", "new_string": "y", "replace_all": "true"}),
			"Edit", "tool_input.replace_all has the wrong JSON type"},
		{"MultiEdit without file_path", claudeGuardPayload(t, "MultiEdit",
			map[string]any{"edits": []any{map[string]any{"old_string": "x", "new_string": "y"}}}), "MultiEdit", "names no file_path"},
		{"MultiEdit without edits", claudeGuardPayload(t, "MultiEdit", map[string]any{"file_path": "a.go"}), "MultiEdit", "has no edits"},
		{"MultiEdit with empty edits", claudeGuardPayload(t, "MultiEdit",
			map[string]any{"file_path": "a.go", "edits": []any{}}), "MultiEdit", "has no edits"},
		{"MultiEdit with edits that are no list", claudeGuardPayload(t, "MultiEdit",
			map[string]any{"file_path": "a.go", "edits": secret}), "MultiEdit", "tool_input.edits has the wrong JSON type"},
		{"MultiEdit with a step without new_string", claudeGuardPayload(t, "MultiEdit", map[string]any{
			"file_path": "a.go",
			"edits":     []any{map[string]any{"old_string": "x", "new_string": "y"}, map[string]any{"old_string": secret}},
		}), "MultiEdit", "edit 2 of the MultiEdit request has no new_string"},
		{"MultiEdit with a step replace_all that is no bool", claudeGuardPayload(t, "MultiEdit", map[string]any{
			"file_path": "a.go",
			"edits":     []any{map[string]any{"old_string": "x", "new_string": "y", "replace_all": 1}},
		}), "MultiEdit", "replace_all has the wrong JSON type"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := claudeGuards{}.DecodeGuard(tc.payload)
			require.ErrorIs(t, err, ErrMalformedEvent)
			assert.Contains(t, err.Error(), tc.text)
			assert.NotContains(t, err.Error(), secret, "the error text names no payload value")

			assert.Empty(t, op.Ops, "a request that does not decode gives no file operation")
			assert.False(t, op.PathsComplete)
			assert.Equal(t, ClaudeID, op.Client)
			assert.Equal(t, tc.tool, op.Tool)
		})
	}

	t.Run("an oversized payload never reaches the decoder", func(t *testing.T) {
		big := claudeGuardPayload(t, "Write", map[string]any{"file_path": "a.go", "content": strings.Repeat("x", MaxHookPayload)})

		_, err := ReadHookPayload(bytes.NewReader(big))
		require.ErrorIs(t, err, ErrMalformedEvent, "the size cap refuses the payload, never a prefix")
	})
}
