package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeCommandsDecodeTheShellEvent(t *testing.T) {
	event, err := claudeCommands{}.DecodeCommand(claudeFixture(t, "pre_tool_use_bash.json"))
	require.NoError(t, err)

	assert.Equal(t, "git push --force origin main", event.Command)
	assert.Equal(t, "Bash", event.NativeTool)
	assert.Equal(t, "synthetic-claude-session-0001", event.SessionID)
	assert.Equal(t, "toolu_synthetic_0004", event.MatchID)
	assert.Equal(t, "/workspace/repo", event.CWD)
	assert.Nil(t, event.Context)

	// The rule the gate hook always had: the matcher selects the tool,
	// so the decoder does not check tool_name, and a payload without a
	// command decodes to an empty command for the gate to refuse.
	event, err = claudeCommands{}.DecodeCommand([]byte(`{"tool_input":{"command":"ls -la"}}`))
	require.NoError(t, err)
	assert.Equal(t, "ls -la", event.Command)

	event, err = claudeCommands{}.DecodeCommand([]byte(`{"tool_name":"Edit","tool_input":{"file_path":"a.go"}}`))
	require.NoError(t, err)
	assert.Empty(t, event.Command)

	_, err = claudeCommands{}.DecodeCommand([]byte("{not json"))
	assert.ErrorIs(t, err, ErrMalformedEvent)

	_, err = claudeCommands{}.DecodeCommand([]byte(`{"tool_input":{"command":["ls"]}}`))
	assert.ErrorIs(t, err, ErrMalformedEvent)
}
