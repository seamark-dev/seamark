package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The two Claude Code events that report a structured read. The reply
// repeats the name of the event that it answers.
const (
	claudePostToolUse        = "PostToolUse"
	claudePostToolUseFailure = "PostToolUseFailure"
)

// claudeReadTool is the one tool that the explore decoder accepts. Its
// tool_input.file_path names the file without inference. Shell, search,
// and listing tools name files only in free text, so they stay out.
const claudeReadTool = "Read"

// claudeExploreMechanism names the reply path of claudeExplores: the
// additionalContext field of a PostToolUse hook reply.
const claudeExploreMechanism = "post-tool-use-context"

// claudeExplores translates the Claude Code Read events after the tool
// ran. The native recording of Claude Code 2.1.300
// (testdata/claude/native/hooks/023-PostToolUse.json) is the evidence
// for the payload shape.
type claudeExplores struct{}

// DecodeExplore reads the path and the identity of one Read event. The
// decoder checks the event name and the tool name itself, because the
// advice text claims that the agent read the file. A user matcher that
// selects more tools must not make that claim true for another tool.
//
// The decoder never reads tool_response: it holds file content, and the
// advice needs the path only. offset, limit, and pages stay unread too,
// because a partial read still names the file.
//
// The receiving context and the turn follow the rules of the edit
// decoder: ReceivingContextOf for session_id and agent_id, and prompt_id
// for the turn.
func (claudeExplores) DecodeExplore(payload []byte) (ExploreEvent, error) {
	var native struct {
		HookEventName string `json:"hook_event_name"`
		ToolName      string `json:"tool_name"`
		SessionID     string `json:"session_id"`
		AgentID       string `json:"agent_id"`
		PromptID      string `json:"prompt_id"`
		CWD           string `json:"cwd"`
		ToolUseID     string `json:"tool_use_id"`
		// ToolInput stays raw until the event and the tool are known.
		// The input of another tool can have another shape, and that
		// event is not applicable, not malformed.
		ToolInput json.RawMessage `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return ExploreEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	// The error texts name no payload value: the event and tool names
	// are client input of any length.
	if native.HookEventName != claudePostToolUse && native.HookEventName != claudePostToolUseFailure {
		return ExploreEvent{}, fmt.Errorf("%w: the event does not report a tool result", ErrNotApplicable)
	}

	if native.ToolName != claudeReadTool {
		return ExploreEvent{}, fmt.Errorf("%w: the tool is not %s", ErrNotApplicable, claudeReadTool)
	}

	var input struct {
		FilePath string `json:"file_path"`
	}

	if len(native.ToolInput) > 0 {
		if err := json.Unmarshal(native.ToolInput, &input); err != nil {
			return ExploreEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
		}
	}

	if input.FilePath == "" {
		return ExploreEvent{}, fmt.Errorf("%w: the Read event names no file_path", ErrMalformedEvent)
	}

	return ExploreEvent{
		EventMeta: EventMeta{
			SessionID:  native.SessionID,
			MatchID:    native.ToolUseID,
			NativeTool: native.ToolName,
			CWD:        native.CWD,
			Context:    claudeContext(native.SessionID, native.AgentID),
			Turn:       native.PromptID,
		},
		Paths: []string{input.FilePath},
		Read:  native.HookEventName == claudePostToolUse,
	}, nil
}

// EncodeExploreAdvice wraps the advisory text in the reply to the event
// that the hook answers: PostToolUse after a read, PostToolUseFailure
// after a failed read. The reply never blocks, because the tool already
// ran.
func (claudeExplores) EncodeExploreAdvice(text string, read bool) (HookReply, error) {
	var reply claudeAdvice

	reply.HookSpecificOutput.HookEventName = claudePostToolUseFailure
	if read {
		reply.HookSpecificOutput.HookEventName = claudePostToolUse
	}

	reply.HookSpecificOutput.AdditionalContext = text

	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(reply); err != nil {
		return HookReply{}, err
	}

	return HookReply{Stdout: out.Bytes()}, nil
}

// ExploreMechanism returns the reply path that the firing log records.
func (claudeExplores) ExploreMechanism() string { return claudeExploreMechanism }
