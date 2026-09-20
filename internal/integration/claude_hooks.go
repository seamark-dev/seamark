package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// claudePreToolUse is the hook event name Claude Code expects back in
// an advisory reply to an edit event.
const claudePreToolUse = "PreToolUse"

// claudeAdviceMechanism names the reply path of claudeEdits: the
// additionalContext field of a PreToolUse hook reply.
const claudeAdviceMechanism = "pre-tool-use-context"

// claudeEdits translates the Claude Code PreToolUse edit event. Edit,
// Write, and MultiEdit all carry one tool_input.file_path, so every
// Claude edit event holds at most one path.
type claudeEdits struct{}

// claudeResets translates the Claude Code PostCompact event.
type claudeResets struct{}

// claudeAdvice is the PreToolUse reply shape. Claude Code adds
// additionalContext to the agent's context and does not block the tool.
type claudeAdvice struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// DecodeEdit reads the path and the identity of one edit event. The
// decoder does not check tool_name: the installed matcher selects the
// tools, and a user can point the hook at another tool with file_path.
// An event without file_path is valid and holds no paths.
func (claudeEdits) DecodeEdit(payload []byte) (EditEvent, error) {
	var native struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return EditEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	event := EditEvent{EventMeta: EventMeta{
		SessionID:  native.SessionID,
		MatchID:    native.ToolUseID,
		NativeTool: native.ToolName,
		CWD:        native.CWD,
		Context:    claudeContext(native.SessionID, native.AgentID),
	}}

	if native.ToolInput.FilePath != "" {
		event.Paths = []string{native.ToolInput.FilePath}
	}

	return event, nil
}

// EncodeAdvice wraps the advisory text in the PreToolUse reply. The
// bytes equal what the hook wrote before the adapter existed.
func (claudeEdits) EncodeAdvice(text string) (HookReply, error) {
	var reply claudeAdvice

	reply.HookSpecificOutput.HookEventName = claudePreToolUse
	reply.HookSpecificOutput.AdditionalContext = text

	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(reply); err != nil {
		return HookReply{}, err
	}

	return HookReply{Stdout: out.Bytes()}, nil
}

// AdviceMechanism returns the reply path that the firing log records.
func (claudeEdits) AdviceMechanism() string { return claudeAdviceMechanism }

// DecodeReset reads the context that PostCompact resets. An event
// without session_id is valid and names no context, so nothing resets.
// The decoder reads agent_id by the rule of DecodeEdit. A reset that
// fires inside a subagent then names the subagent context, which is not
// resettable, and it can never reset the context of the parent.
func (claudeResets) DecodeReset(payload []byte) (ResetEvent, error) {
	var native struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return ResetEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	event := ResetEvent{}
	if receiver := claudeContext(native.SessionID, native.AgentID); receiver != nil {
		event.Context = *receiver
	}

	return event, nil
}

// claudeContext maps the identity of a Claude Code event to its
// receiving context. An empty session gives no context, and suppression
// stays off.
//
// The main conversation is the session. PostCompact reports the same
// session id, so that context is resettable.
//
// Claude Code adds agent_id to an event that fires inside a subagent.
// A subagent has its own context window, so it is its own receiver: a
// lesson that the parent got is not in the window of the subagent. No
// documented reset event reaches a subagent, so its context is not
// resettable. The delivery service then repeats the advice there and
// never hides it.
func claudeContext(sessionID, agentID string) *ReceivingContext {
	switch {
	case sessionID == "":
		return nil
	case agentID != "":
		return &ReceivingContext{ID: receiverID(sessionID, agentID)}
	default:
		return &ReceivingContext{ID: receiverID(sessionID), Resettable: true}
	}
}
