package integration

import (
	"encoding/json"
	"fmt"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// codexCompletions translates the Codex PostToolUse apply_patch event
// for the post-edit verification. Codex has no separate failure event,
// so the decoder reads a failed tool from a non-zero exit status in
// tool_response. No native recording shows a hook after a failed patch:
// codex-cli 0.169.0 refused a patch without a hunk in its own patch
// check, before any hook. codex_events.go declares the event name
// (codexPostToolUse).
type codexCompletions struct{}

// DecodeCompletion reads the outcome and the paths of one apply_patch
// completion. The outcome comes from the exit status in tool_response,
// by the outcome rule in tool_outcome.go (codexOutcome): exit code 0 gives
// succeeded, another code gives failed, and no status gives unknown.
// The decoder reads no other part of tool_response, because the output
// text can hold file content.
//
// The paths come from the patch text by the rules of guardPatchOps, so
// the post-edit hook reads the same paths as the guard. A patch that the
// grammar does not cover gives the error of guardPatchOps, and the
// Completion keeps its identity and outcome. A header path with a tab
// or a carriage return is such a patch: Codex writes another path than
// the text names, so the post-edit check must not observe the named one.
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

	ops, err := guardPatchOps(patch)
	if err != nil {
		return c, err
	}

	c.Paths = patchOpPaths(ops)

	return c, nil
}

// EncodeFindings wraps the findings in the additionalContext reply of
// PostToolUse, on stdout with exit 0. Codex has no failure event, so a
// failed patch that reaches the hook gets the same event name. A native
// guard recording of codex-cli 0.164.0 showed that PostToolUse text on
// stderr never reaches the model. That binary embeds the PostToolUse
// output schema with hookSpecificOutput and additionalContext. The
// advisory edit hook uses the same form on PreToolUse, and Codex adds
// that context to the thread. The guard recording of codex-cli 0.169.0
// shows the PostToolUse context in the thread too: a hook developer
// record after the patch and before the next model step, in the root
// and in a worker. The post-edit path still reads active-unverified,
// because its evidence (postEditEvidence) names no client version.
//
// The text in the reply holds at most codexMaxFindingsBytes. A text
// that fits keeps every byte, and a longer text loses its last lines.
func (codexCompletions) EncodeFindings(text string, _ lessoncheck.ToolOutcome) (HookReply, error) {
	return encodeContextReply(codexPostToolUse, lessoncheck.BoundFindings(text, codexMaxFindingsBytes))
}

// codexMaxFindingsBytes bounds the findings text in the PostToolUse
// reply. Codex bounds model-visible hook context with
// additionalContextLimit, about 2,500 tokens by default. A text of this
// size stays inside that bound while a token holds 1.23 bytes or more
// on average. English text holds about 4 bytes per token. Without the
// bound, a patch of 100 files can give a text of more than 12 KB,
// because each unknown line and the rerun command list every path.
const codexMaxFindingsBytes = 3072

// toolOutcomeOf converts the Outcome of the completion decoders to the
// tool outcome of the lesson-check stream. Both lists hold the same
// words, so a type conversion is enough. The empty Outcome gives
// unknown, because a post-edit record always names an outcome.
func toolOutcomeOf(o Outcome) lessoncheck.ToolOutcome {
	if o == OutcomeNone {
		return lessoncheck.ToolUnknown
	}

	return lessoncheck.ToolOutcome(o)
}
