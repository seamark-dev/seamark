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

// DecodeGuard reads the complete operation of one apply_patch event. It
// parses the patch text with applyPatchOps and never runs it. A patch
// never reaches the command gate: codexCommands refuses apply_patch.
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
	return decodeGuard(CodexID, payload, "the tool is not "+codexApplyPatch,
		func(string) ([]lessoncheck.FileOp, error) {
			patch, err := codexPatchText(payload)
			if err != nil {
				return nil, err
			}

			return applyPatchOps(patch)
		})
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
