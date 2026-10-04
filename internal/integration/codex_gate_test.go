package integration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
)

func TestCodexCommandsDecodeTheShellEvent(t *testing.T) {
	event, err := codexCommands{}.DecodeCommand(codexFixture(t, "pre_tool_use_bash.json"))
	require.NoError(t, err)

	assert.Equal(t, "git push --force origin main", event.Command)
	assert.Equal(t, "Bash", event.NativeTool)
	assert.Equal(t, "thr_synthetic_0001", event.SessionID)
	assert.Equal(t, "call_synthetic_0011", event.MatchID)
	assert.Equal(t, "/workspace/repo", event.CWD)
	assert.Nil(t, event.Context, "the gate reads no receiving context")

	// An empty command is a valid event; the gate refuses it.
	event, err = codexCommands{}.DecodeCommand([]byte(`{"tool_name":"Bash","tool_input":{"command":""}}`))
	require.NoError(t, err)
	assert.Empty(t, event.Command)
}

func TestCodexCommandsNeverReadAPatchAsACommand(t *testing.T) {
	// The edit event reaches the gate only through a widened matcher.
	// The patch text must not be evaluated as shell: the decoder says
	// the event is not applicable and names the tool.
	for _, fixture := range []string{
		"pre_tool_use_apply_patch_multi.json", "pre_tool_use_apply_patch_subagent.json",
	} {
		_, err := codexCommands{}.DecodeCommand(codexFixture(t, fixture))
		require.ErrorIs(t, err, ErrNotApplicable, fixture)
		assert.Contains(t, err.Error(), `tool "apply_patch" is not Bash`)
	}

	_, err := codexCommands{}.DecodeCommand([]byte(`{"tool_name":"mcp__filesystem__read_file","tool_input":{"path":"x"}}`))
	require.ErrorIs(t, err, ErrNotApplicable)
	assert.Contains(t, err.Error(), "mcp__filesystem__read_file")
}

func TestCodexCommandsRejectAMalformedEvent(t *testing.T) {
	for name, payload := range map[string]string{
		"not JSON":              "{not json",
		"no tool_input":         `{"tool_name":"Bash"}`,
		"no command":            `{"tool_name":"Bash","tool_input":{}}`,
		"command is not a text": `{"tool_name":"Bash","tool_input":{"command":["ls"]}}`,
		"command is null":       `{"tool_name":"Bash","tool_input":{"command":null}}`,
	} {
		_, err := codexCommands{}.DecodeCommand([]byte(payload))
		assert.ErrorIs(t, err, ErrMalformedEvent, name)
	}
}

func TestExitProtocolGateBlocksWithTheReasons(t *testing.T) {
	deny := &gate.Decision{Verdict: gate.VerdictDeny, Mode: "enforce", Matches: []gate.Match{
		{RuleID: "no-force-push", Verdict: gate.VerdictDeny, Message: "force-push to main\x1b rewrites history"},
		{RuleID: "prod-db", Verdict: gate.VerdictApproval, Message: "database write"},
	}}

	for _, codec := range []CommandHooks{codexCommands{}, claudeCommands{}} {
		reply, err := codec.EncodeDecision(deny)
		require.NoError(t, err)
		assert.Equal(t, 2, reply.ExitCode, "exit 2 is the block both clients read")
		assert.Equal(t, "force-push to main rewrites history; database write", string(reply.Stderr),
			"the reasons travel on stderr, with control characters removed")
		assert.Empty(t, reply.Stdout)

		// A require_approval verdict blocks like a deny: neither client
		// supports a native ask for the hook.
		ask := &gate.Decision{Verdict: gate.VerdictApproval, Mode: "enforce",
			Matches: []gate.Match{{RuleID: "prod-db", Verdict: gate.VerdictApproval, Message: "database write"}}}
		reply, err = codec.EncodeDecision(ask)
		require.NoError(t, err)
		assert.Equal(t, 2, reply.ExitCode)
		assert.Equal(t, "database write", string(reply.Stderr))

		// Warn mode never blocks, whatever the verdict.
		warn := *deny
		warn.Mode = "warn"
		reply, err = codec.EncodeDecision(&warn)
		require.NoError(t, err)
		assert.Equal(t, HookReply{}, reply)

		reply, err = codec.EncodeDecision(&gate.Decision{Verdict: gate.VerdictAllow, Mode: "enforce"})
		require.NoError(t, err)
		assert.Equal(t, HookReply{}, reply)

		// A failure blocks with its text; the caller decides whether the
		// mode requires blocking.
		reply = codec.EncodeFailure(errors.New("policy unreadable\x1b"))
		assert.Equal(t, HookReply{ExitCode: 2, Stderr: []byte("policy unreadable")}, reply)
	}
}
