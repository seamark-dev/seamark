package integration

import (
	"encoding/json"
	"fmt"
)

// claudeCommands translates the Claude Code PreToolUse Bash event for
// the gate. The decision and failure replies use the shared exit
// protocol, the form the Claude Code gate hook always used.
type claudeCommands struct {
	exitProtocolGate
}

// DecodeCommand reads the shell text of one event. It keeps the rule
// the gate hook always had: the installed matcher selects the tool, so
// the decoder does not check tool_name, and it reads tool_input.command
// wherever the event comes from. A missing command decodes to an empty
// command, which the gate refuses, so a payload without one still fails
// closed under enforcement.
func (claudeCommands) DecodeCommand(payload []byte) (CommandEvent, error) {
	var native struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return CommandEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	return CommandEvent{
		EventMeta: EventMeta{
			SessionID:  native.SessionID,
			MatchID:    native.ToolUseID,
			NativeTool: native.ToolName,
			CWD:        native.CWD,
		},
		Command: native.ToolInput.Command,
	}, nil
}
