package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeFixture reads one native payload from testdata/claude.
func claudeFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "claude", name))
	require.NoError(t, err)

	return data
}

func TestClaudeEditsDecodeTheNativeFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		tool    string
		match   string
		path    string
	}{
		{"pre_tool_use_edit.json", "Edit", "toolu_synthetic_0001", "/workspace/repo/internal/api/handler.go"},
		{"pre_tool_use_write.json", "Write", "toolu_synthetic_0002", "/workspace/repo/docs/new-page.md"},
		{"pre_tool_use_multiedit.json", "MultiEdit", "toolu_synthetic_0003", "/workspace/repo/internal/api/handler.go"},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			event, err := claudeEdits{}.DecodeEdit(claudeFixture(t, tc.fixture))
			require.NoError(t, err)

			assert.Equal(t, []string{tc.path}, event.Paths, "every Claude edit tool carries one file_path")
			assert.Equal(t, tc.tool, event.NativeTool)
			assert.Equal(t, tc.match, event.MatchID)
			assert.Equal(t, "synthetic-claude-session-0001", event.SessionID)
			assert.Equal(t, "/workspace/repo", event.CWD)

			// The session is the receiving context of the session-keyed
			// hook, and PostCompact reports the same id.
			require.NotNil(t, event.Context)
			assert.Equal(t, ReceivingContext{ID: "synthetic-claude-session-0001", Resettable: true}, *event.Context)
		})
	}
}

func TestClaudeEditsSeparateEmptyEventsFromMalformedInput(t *testing.T) {
	// A shell event is valid and holds no path: nothing to advise on.
	event, err := claudeEdits{}.DecodeEdit(claudeFixture(t, "pre_tool_use_bash.json"))
	require.NoError(t, err)
	assert.Empty(t, event.Paths)

	// No session id means no receiving context, so suppression stays off.
	event, err = claudeEdits{}.DecodeEdit([]byte(`{"tool_name":"Edit","tool_input":{"file_path":"a.go"}}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a.go"}, event.Paths)
	assert.Nil(t, event.Context)

	for _, payload := range []string{"", "{not json", `{"tool_input":"text"}`, `[]`} {
		_, err := claudeEdits{}.DecodeEdit([]byte(payload))
		require.ErrorIs(t, err, ErrMalformedEvent, "payload %q", payload)
	}
}

func TestClaudeEditsEncodeTheHistoricalReply(t *testing.T) {
	// The bytes are frozen: the reply the hook wrote before the adapter.
	// encoding/json escapes <, >, and & and ends the value with a newline.
	reply, err := claudeEdits{}.EncodeAdvice("seamark — lessons for a.go:\n- [×3] E501 <line> & too long\n")
	require.NoError(t, err)

	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":` +
		`"seamark — lessons for a.go:\n- [×3] E501 \u003cline\u003e \u0026 too long\n"}}` + "\n"
	assert.Equal(t, want, string(reply.Stdout))
	assert.Empty(t, reply.Stderr)
	assert.Zero(t, reply.ExitCode, "advice never blocks the edit")
}

func TestClaudeResetsDecodeTheCompactedContext(t *testing.T) {
	event, err := claudeResets{}.DecodeReset(claudeFixture(t, "post_compact.json"))
	require.NoError(t, err)
	assert.Equal(t, ReceivingContext{ID: "synthetic-claude-session-0001", Resettable: true}, event.Context)

	// The reset names the context that the edit event names.
	edit, err := claudeEdits{}.DecodeEdit(claudeFixture(t, "pre_tool_use_edit.json"))
	require.NoError(t, err)
	assert.Equal(t, *edit.Context, event.Context)

	// No session id is valid and resets nothing.
	event, err = claudeResets{}.DecodeReset([]byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, event.Context.ID)

	_, err = claudeResets{}.DecodeReset([]byte(`{not json`))
	require.ErrorIs(t, err, ErrMalformedEvent)
}

func TestReadHookPayloadRefusesAnOversizedPayload(t *testing.T) {
	atLimit, err := ReadHookPayload(bytes.NewReader(make([]byte, MaxHookPayload)))
	require.NoError(t, err)
	assert.Len(t, atLimit, MaxHookPayload)

	// A truncated prefix could decode as a smaller, complete event.
	_, err = ReadHookPayload(strings.NewReader(strings.Repeat("x", MaxHookPayload+1)))
	require.ErrorIs(t, err, ErrMalformedEvent)
}
