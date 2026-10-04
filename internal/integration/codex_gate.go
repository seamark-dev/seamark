package integration

import (
	"encoding/json"
	"fmt"
)

// codexShellTool is the name of the Codex shell tool in a PreToolUse
// event and in the matcher of the gate hook (hooks reference, consulted
// 2026-09-26; a native run of codex-cli 0.154.0 on 2026-09-21 reported a
// shell command under this name).
const codexShellTool = "Bash"

// codexCommands translates the Codex PreToolUse Bash event for the
// gate. The decision and failure replies use the shared exit protocol:
// the Codex hooks reference documents exit 2 with stderr as a block.
type codexCommands struct {
	exitProtocolGate
}

// DecodeCommand reads the shell text of one Bash event. An event of
// another tool gives ErrNotApplicable: Codex also fires PreToolUse for
// apply_patch, for MCP tools, and for other function tools, and the
// gate evaluates shell commands only. A patch in particular is an edit
// and is never read as a command. The installed matcher selects Bash,
// so such an event reaches the gate only through a matcher the user
// widened; the caller then blocks under enforcement and names the tool,
// because a gate that skips what it cannot classify is a bypass.
//
// A Bash event without a string command is malformed. The decoder
// reads no receiving context: the gate needs none.
func (codexCommands) DecodeCommand(payload []byte) (CommandEvent, error) {
	var native struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command *string `json:"command"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return CommandEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	if native.ToolName != codexShellTool {
		return CommandEvent{}, fmt.Errorf("%w: tool %q is not %s; the gate evaluates shell commands only, "+
			"and the gate hook's matcher selects %s", ErrNotApplicable, native.ToolName, codexShellTool, codexShellTool)
	}

	if native.ToolInput.Command == nil {
		return CommandEvent{}, fmt.Errorf("%w: %s event without tool_input.command", ErrMalformedEvent, codexShellTool)
	}

	return CommandEvent{
		EventMeta: EventMeta{
			SessionID:  native.SessionID,
			MatchID:    native.ToolUseID,
			NativeTool: native.ToolName,
			CWD:        native.CWD,
		},
		Command: *native.ToolInput.Command,
	}, nil
}
