package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// claudeCompletions translates the Claude Code events after an edit
// tool ran, for the post-edit verification. PostToolUse fires after a
// tool succeeds and PostToolUseFailure after it fails. The native
// recording of Claude Code 2.1.300
// (testdata/claude/native/hooks/004-PostToolUseFailure.json) shows the
// failure fields error and is_interrupt.
type claudeCompletions struct{}

// DecodeCompletion reads the outcome and the path of one completion
// event. PostToolUse gives succeeded. PostToolUseFailure gives
// interrupted when is_interrupt is true, and failed otherwise, by the
// rule of the capture decoder (toolFailure). The decoder checks the
// event and the tool, because a finding about another tool would name
// an edit that did not happen.
//
// The decoder never reads tool_response or error: they can hold file
// content, and the verification reads the file itself.
func (claudeCompletions) DecodeCompletion(payload []byte) (lessoncheck.Completion, error) {
	var native struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		CWD           string `json:"cwd"`
		ToolUseID     string `json:"tool_use_id"`
		ToolName      string `json:"tool_name"`
		IsInterrupt   bool   `json:"is_interrupt"`
		// ToolInput stays raw until the tool is known, because the input
		// of an uncovered tool has another shape.
		ToolInput json.RawMessage `json:"tool_input"`
	}

	c := lessoncheck.Completion{Client: ClaudeID}

	if err := json.Unmarshal(payload, &native); err != nil {
		return c, malformedJSON(err)
	}

	c.Tool, c.SessionID, c.MatchID, c.CWD = native.ToolName, native.SessionID, native.ToolUseID, native.CWD

	switch native.HookEventName {
	case claudePostToolUse:
		c.Outcome = lessoncheck.ToolSucceeded
	case claudePostToolUseFailure:
		c.Outcome = lessoncheck.ToolFailed
		if native.IsInterrupt {
			c.Outcome = lessoncheck.ToolInterrupted
		}
	default:
		return c, fmt.Errorf("%w: the event does not report a tool result", ErrNotApplicable)
	}

	if native.ToolName == "" {
		return c, fmt.Errorf("%w: the event names no tool_name", ErrMalformedEvent)
	}

	if !slices.Contains(claudeGuardTools, native.ToolName) {
		return c, fmt.Errorf("%w: the tool is not one of the covered edit tools", ErrNotApplicable)
	}

	var input struct {
		FilePath string `json:"file_path"`
	}

	if len(native.ToolInput) > 0 {
		if err := json.Unmarshal(native.ToolInput, &input); err != nil {
			return c, malformedJSON(err)
		}
	}

	if input.FilePath == "" {
		return c, fmt.Errorf("%w: the %s event names no file_path", ErrMalformedEvent, native.ToolName)
	}

	c.Paths = []string{input.FilePath}

	return c, nil
}

// EncodeFindings wraps the findings in the additionalContext reply of
// the event that the hook answers: PostToolUseFailure after a failed or
// interrupted tool, PostToolUse otherwise. The Claude Code hooks
// reference documents this form. No native recording shows yet that the
// text reaches the model (§5 row 38), so the post-edit path stays
// active-unverified until plan step P4.11.
func (claudeCompletions) EncodeFindings(text string, outcome lessoncheck.ToolOutcome) (HookReply, error) {
	var reply claudeAdvice

	reply.HookSpecificOutput.HookEventName = claudePostToolUse
	if outcome == lessoncheck.ToolFailed || outcome == lessoncheck.ToolInterrupted {
		reply.HookSpecificOutput.HookEventName = claudePostToolUseFailure
	}

	reply.HookSpecificOutput.AdditionalContext = text

	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(reply); err != nil {
		return HookReply{}, err
	}

	return HookReply{Stdout: out.Bytes()}, nil
}
