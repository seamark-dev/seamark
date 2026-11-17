package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

func TestReceivingContextOfMatchesTheDecoders(t *testing.T) {
	// The rules, one row each. The IDs keep the receiverID bytes, so the
	// state keys and the context digests of Claude Code do not move.
	rules := []struct {
		name                   string
		client, session, agent string
		want                   *ReceivingContext
	}{
		{"claude root", ClaudeID, "s-1", "", &ReceivingContext{ID: "3:s-1", Resettable: true, Lane: LaneRoot}},
		{"claude child", ClaudeID, "s-1", "a-1", &ReceivingContext{ID: "3:s-13:a-1", Lane: LaneChild}},
		{"claude without session", ClaudeID, "", "", nil},
		{"claude agent without session", ClaudeID, "", "a-1", nil},
		{"codex root", CodexID, "s-1", "", &ReceivingContext{ID: "3:s-1", Lane: LaneRoot}},
		{"codex child", CodexID, "s-1", "a-1", &ReceivingContext{ID: "3:s-13:a-1", Lane: LaneChild}},
		{"codex without session", CodexID, "", "a-1", nil},
		{"client without a rule", "third", "s-1", "", nil},
		{"no client", "", "s-1", "", nil},
	}

	for _, rule := range rules {
		assert.Equal(t, rule.want, ReceivingContextOf(rule.client, rule.session, rule.agent), rule.name)
	}

	// Every decoder derives its receiver through ReceivingContextOf. A
	// receiver that the delivery report computes again from the stored
	// identity then equals the recorded one.
	type decoded struct {
		client  string
		fixture string
		context func([]byte) (*ReceivingContext, error)
	}

	builtin := Builtin()

	claude, ok := builtin.Lookup(ClaudeID)
	require.True(t, ok)

	codex, ok := builtin.Lookup(CodexID)
	require.True(t, ok)

	editOf := func(hooks EditHooks) func([]byte) (*ReceivingContext, error) {
		return func(payload []byte) (*ReceivingContext, error) {
			event, err := hooks.DecodeEdit(payload)

			return event.Context, err
		}
	}

	// A Claude reset holds its context as a value. The zero value stands
	// for no context, as in delivery.Reset.
	claudeReset := func(payload []byte) (*ReceivingContext, error) {
		event, err := claude.Resets.DecodeReset(payload)
		if err != nil || event.Context == (ReceivingContext{}) {
			return nil, err
		}

		return &event.Context, nil
	}

	cases := []decoded{
		{ClaudeID, "claude/pre_tool_use_edit.json", editOf(claude.Edits)},
		{ClaudeID, "claude/pre_tool_use_edit_subagent.json", editOf(claude.Edits)},
		{ClaudeID, "claude/pre_tool_use_write.json", editOf(claude.Edits)},
		{ClaudeID, "claude/pre_tool_use_multiedit.json", editOf(claude.Edits)},
		{ClaudeID, "claude/post_compact.json", claudeReset},
		{ClaudeID, "claude/post_compact_subagent.json", claudeReset},
		{ClaudeID, "claude/native/hooks/019-PostCompact.json", claudeReset},
	}

	codexPatches, err := filepath.Glob(filepath.Join("testdata", "codex", "pre_tool_use_apply_patch_*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, codexPatches)

	for _, path := range codexPatches {
		if strings.HasSuffix(path, "_malformed.json") {
			continue
		}

		cases = append(cases, decoded{CodexID, filepath.Join("codex", filepath.Base(path)), editOf(codex.Edits)})
	}

	lanes := map[string]int{}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			payload, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(tc.fixture)))
			require.NoError(t, err)

			var native struct {
				SessionID string `json:"session_id"`
				AgentID   string `json:"agent_id"`
			}

			require.NoError(t, json.Unmarshal(payload, &native))

			got, err := tc.context(payload)
			require.NoError(t, err)
			require.NotNil(t, got, "every fixture names a session")

			assert.Equal(t, ReceivingContextOf(tc.client, native.SessionID, native.AgentID), got)

			lanes[tc.client+"/"+got.Lane]++
		})
	}

	// The fixtures cover both lanes of both clients.
	for _, lane := range []string{"claude/root", "claude/child", "codex/root", "codex/child"} {
		assert.Positive(t, lanes[lane], lane)
	}
}

func TestResetReachesFollowsTheReceiverRule(t *testing.T) {
	// A path context and a lane share their words, so the timing evidence
	// can ask the lane rule with a path context.
	require.Equal(t, LaneRoot, string(lessoncheck.ContextRoot))
	require.Equal(t, LaneChild, string(lessoncheck.ContextChild))

	for _, client := range []string{ClaudeID, CodexID} {
		for _, context := range []lessoncheck.PathContext{lessoncheck.ContextRoot, lessoncheck.ContextChild} {
			agent := ""
			if context == lessoncheck.ContextChild {
				agent = "a-1"
			}

			receiver := ReceivingContextOf(client, "s-1", agent)
			require.NotNil(t, receiver)
			assert.Equal(t, receiver.Resettable, resetReaches(client, context), "%s %s", client, context)
		}
	}

	assert.False(t, resetReaches("third", lessoncheck.ContextRoot), "a client without a rule has no resettable lane")
}
