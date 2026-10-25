package integration

import (
	"encoding/json"
	"fmt"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// codexCompletions translates the Codex PostToolUse apply_patch event
// for the post-edit verification (contract K9). Codex has no separate
// failure event: a failed tool reports a non-zero exit status in
// tool_response (§5 row 37). The capture decoder declares the event name
// (codexPostToolUse).
type codexCompletions struct{}

// DecodeCompletion reads the outcome and the paths of one apply_patch
// completion. The outcome comes from the exit status in tool_response,
// by the rule of the capture decoder (codexOutcome): exit code 0 gives
// succeeded, another code gives failed, and no status gives unknown.
// The decoder reads no other part of tool_response, because the output
// text can hold file content.
//
// The paths come from the patch text by the rules of applyPatchPaths,
// so the post-edit hook scopes a missing baseline with the same paths as
// the advisory hook. A patch that the grammar does not cover gives the
// error of applyPatchPaths, and the Completion keeps its identity and
// outcome.
func (codexCompletions) DecodeCompletion(payload []byte) (lessoncheck.Completion, error) {
	var native struct {
		HookEventName string          `json:"hook_event_name"`
		SessionID     string          `json:"session_id"`
		CWD           string          `json:"cwd"`
		ToolUseID     string          `json:"tool_use_id"`
		ToolName      string          `json:"tool_name"`
		ToolResponse  json.RawMessage `json:"tool_response"`
	}

	c := lessoncheck.Completion{Client: CodexID}

	if err := json.Unmarshal(payload, &native); err != nil {
		return c, malformedJSON(err)
	}

	c.Tool, c.SessionID, c.MatchID, c.CWD = native.ToolName, native.SessionID, native.ToolUseID, native.CWD

	if native.HookEventName != codexPostToolUse {
		return c, fmt.Errorf("%w: the event does not report a tool result", ErrNotApplicable)
	}

	if native.ToolName == "" {
		return c, fmt.Errorf("%w: the event names no tool_name", ErrMalformedEvent)
	}

	if native.ToolName != codexApplyPatch {
		return c, fmt.Errorf("%w: the tool is not %s", ErrNotApplicable, codexApplyPatch)
	}

	c.Outcome = toolOutcomeOf(codexOutcome(native.ToolResponse))

	patch, err := codexPatchText(payload)
	if err != nil {
		return c, err
	}

	paths, err := applyPatchPaths(patch)
	if err != nil {
		return c, err
	}

	c.Paths = paths

	return c, nil
}

// EncodeFindings writes the findings to stderr with exit 0. The Codex
// hooks reference documents that a PostToolUse hook can give the model
// text this way. No native recording shows yet that the text reaches
// the model (§5 row 38), so the post-edit path stays active-unverified
// until plan step P4.11.
func (codexCompletions) EncodeFindings(text string, _ lessoncheck.ToolOutcome) (HookReply, error) {
	return HookReply{Stderr: []byte(text)}, nil
}

// toolOutcomeOf converts a capture outcome to the tool outcome of the
// lesson-check stream. Both lists hold the same words (contracts §1.8).
// The empty capture outcome gives unknown, because a post-edit record
// always names an outcome.
func toolOutcomeOf(o Outcome) lessoncheck.ToolOutcome {
	if o == OutcomeNone {
		return lessoncheck.ToolUnknown
	}

	return lessoncheck.ToolOutcome(o)
}
