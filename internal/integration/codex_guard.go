package integration

import (
	"encoding/json"
	"fmt"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// codexGuardTools lists the Codex tools that the guard covers. Codex
// reports every file edit as apply_patch, so it is the one covered tool.
// The shell tool writes files too, but only in free text, so it stays
// an uncovered writer.
var codexGuardTools = []string{codexApplyPatch}

// codexGuards translates the Codex PreToolUse apply_patch event for the
// lesson-check guard. The deny reply uses the exit protocol of the gate:
// the Codex hooks reference documents exit 2 with stderr as a block.
type codexGuards struct {
	exitProtocolGate
}

// DecodeGuard reads the complete operation of one apply_patch event
// (contract K8, "Input decoding per client"). It parses the patch text
// with applyPatchOps and never runs it. A patch never reaches the
// command gate: codexCommands refuses apply_patch.
//
// The errors follow the patch grammar. An envelope error and a patch
// without a hunk give ErrMalformedEvent (the guard reason
// unparseable-input). A line that the grammar does not cover and an
// environment line give ErrUnsupportedGrammar (incomplete-paths). An
// event of another tool gives ErrNotApplicable (tool-uncovered).
//
// The receiver comes from session_id and agent_id by the rules of
// ReceivingContextOf, as in DecodeEdit. The error texts name no
// payload value, because the guard puts the text into its deny reason.
func (codexGuards) DecodeGuard(payload []byte) (lessoncheck.Operation, error) {
	var native struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
	}

	op := lessoncheck.Operation{Client: CodexID}

	if err := json.Unmarshal(payload, &native); err != nil {
		return op, malformedJSON(err)
	}

	op.Tool, op.SessionID, op.MatchID, op.CWD = native.ToolName, native.SessionID, native.ToolUseID, native.CWD
	op.Receiver = receiverIDOf(ReceivingContextOf(CodexID, native.SessionID, native.AgentID))

	if native.ToolName == "" {
		return op, fmt.Errorf("%w: the request names no tool_name", ErrMalformedEvent)
	}

	if native.ToolName != codexApplyPatch {
		return op, fmt.Errorf("%w: the tool is not %s", ErrNotApplicable, codexApplyPatch)
	}

	patch, err := codexPatchText(payload)
	if err != nil {
		return op, err
	}

	ops, err := applyPatchOps(patch)
	if err != nil {
		return op, err
	}

	op.Ops, op.PathsComplete = ops, true

	return op, nil
}

// codexPatchText returns the patch text of an apply_patch event:
// tool_input.command as a string. A missing or null command gives
// ErrMalformedEvent, as in DecodeEdit.
func codexPatchText(payload []byte) (string, error) {
	var native struct {
		ToolInput struct {
			Command *string `json:"command"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return "", malformedJSON(err)
	}

	if native.ToolInput.Command == nil {
		return "", fmt.Errorf("%w: %s event without tool_input.command", ErrMalformedEvent, codexApplyPatch)
	}

	return *native.ToolInput.Command, nil
}
