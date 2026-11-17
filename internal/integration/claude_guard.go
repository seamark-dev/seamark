package integration

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The Claude Code edit tools that the guard covers. They are the tools
// of the installed matcher Edit|Write|MultiEdit, in that order. Every
// other writer, for example NotebookEdit or Bash, has no projection.
const (
	claudeToolEdit      = "Edit"
	claudeToolWrite     = "Write"
	claudeToolMultiEdit = "MultiEdit"
)

// claudeGuardTools lists the covered tools in matcher order.
var claudeGuardTools = []string{claudeToolEdit, claudeToolWrite, claudeToolMultiEdit}

// claudeGuards translates the Claude Code PreToolUse edit event for the
// lesson-check guard. The deny reply uses the exit protocol of the gate.
type claudeGuards struct {
	exitProtocolGate
}

// claudeTextEdit is one replacement of an Edit or a MultiEdit input.
// The pointers tell a missing field from an empty one: an empty
// old_string is a valid request that the projection judges, and a
// missing one is a request that the guard cannot read.
type claudeTextEdit struct {
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll *bool   `json:"replace_all"`
}

// DecodeGuard reads the complete operation of one edit event. Unlike
// DecodeEdit, it checks tool_name, because a guard decision about
// another tool with the same input shape would be a guess. The receiver
// comes from ReceivingContextOf, as in claudeContext, so the guard and
// the advisory hook name one context alike.
//
// The decoder does not read hook_event_name. The installed matcher
// selects PreToolUse, and a request of another event still decodes as
// an edit, so the guard judges it and never allows it unseen.
//
// The error texts name no payload value. The guard puts the text into
// its deny reason, and a payload value can hold file content.
func (claudeGuards) DecodeGuard(payload []byte) (lessoncheck.Operation, error) {
	return decodeGuard(ClaudeID, payload, "the tool is not one of the covered edit tools",
		func(tool string) ([]lessoncheck.FileOp, error) {
			fop, err := claudeFileOp(tool, payload)
			if err != nil {
				return nil, err
			}

			return []lessoncheck.FileOp{fop}, nil
		})
}

// claudeFileOp reads the tool input of one covered tool into its file
// operation. It decodes the whole payload again into the input shape of
// that tool, so a JSON type error names the field with its full path,
// for example tool_input.replace_all.
func claudeFileOp(tool string, payload []byte) (lessoncheck.FileOp, error) {
	var native struct {
		ToolInput struct {
			FilePath string `json:"file_path"`
			// Content is the full content of a Write.
			Content *string `json:"content"`
			// The replacement of an Edit. These fields repeat
			// claudeTextEdit and are not an embedded struct, because
			// Go 1.25.0 names an embedded struct in the field path of a
			// JSON type error, and the error text must name the input
			// field alone.
			OldString  *string `json:"old_string"`
			NewString  *string `json:"new_string"`
			ReplaceAll *bool   `json:"replace_all"`
			// Edits are the replacements of a MultiEdit, in order.
			Edits []claudeTextEdit `json:"edits"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return lessoncheck.FileOp{}, malformedJSON(err)
	}

	input := native.ToolInput
	if input.FilePath == "" {
		return lessoncheck.FileOp{}, fmt.Errorf("%w: the %s request names no file_path", ErrMalformedEvent, tool)
	}

	fop := lessoncheck.FileOp{Path: input.FilePath}

	switch tool {
	case claudeToolWrite:
		if input.Content == nil {
			return lessoncheck.FileOp{}, fmt.Errorf("%w: the Write request has no content", ErrMalformedEvent)
		}

		fop.Kind, fop.Content = lessoncheck.OpWrite, input.Content
	case claudeToolEdit:
		single := claudeTextEdit{OldString: input.OldString, NewString: input.NewString, ReplaceAll: input.ReplaceAll}

		edit, err := single.textEdit("the Edit request")
		if err != nil {
			return lessoncheck.FileOp{}, err
		}

		fop.Kind, fop.Edits = lessoncheck.OpEdit, []lessoncheck.TextEdit{edit}
	default:
		if len(input.Edits) == 0 {
			return lessoncheck.FileOp{}, fmt.Errorf("%w: the MultiEdit request has no edits", ErrMalformedEvent)
		}

		fop.Kind = lessoncheck.OpMultiEdit

		for i, e := range input.Edits {
			edit, err := e.textEdit(fmt.Sprintf("edit %d of the MultiEdit request", i+1))
			if err != nil {
				return lessoncheck.FileOp{}, err
			}

			fop.Edits = append(fop.Edits, edit)
		}
	}

	return fop, nil
}

// textEdit returns the replacement, or ErrMalformedEvent when a string
// field is missing. The tool schema requires both strings, so a request
// without one is not a request that the native tool runs. replace_all
// is optional and defaults to false, as in the tool schema. subject
// names the request part in the error text.
func (e claudeTextEdit) textEdit(subject string) (lessoncheck.TextEdit, error) {
	switch {
	case e.OldString == nil:
		return lessoncheck.TextEdit{}, fmt.Errorf("%w: %s has no old_string", ErrMalformedEvent, subject)
	case e.NewString == nil:
		return lessoncheck.TextEdit{}, fmt.Errorf("%w: %s has no new_string", ErrMalformedEvent, subject)
	}

	return lessoncheck.TextEdit{Old: *e.OldString, New: *e.NewString, ReplaceAll: e.ReplaceAll != nil && *e.ReplaceAll}, nil
}

// decodeGuard reads the identity of one pre-write event of the client
// and checks its tool against guardTools. Both clients use it, so the
// guard decodes a tool exactly when the tool has a pre-write path.
//
// It sets the identity fields right after the JSON parse, before the
// tool and tool-input checks. So an event that these checks refuse
// still names its tool and its call, and its audit record joins the
// later records of the same call. notCovered is the error text of an
// uncovered tool. fileOps reads the tool input of a covered tool.
// decodeGuard sets Ops and PathsComplete only when fileOps succeeds.
func decodeGuard(clientID string, payload []byte, notCovered string,
	fileOps func(tool string) ([]lessoncheck.FileOp, error),
) (lessoncheck.Operation, error) {
	var native struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
	}

	op := lessoncheck.Operation{Client: clientID}

	if err := json.Unmarshal(payload, &native); err != nil {
		return op, malformedJSON(err)
	}

	op.Tool, op.SessionID, op.MatchID, op.CWD = native.ToolName, native.SessionID, native.ToolUseID, native.CWD
	op.Receiver = receiverIDOf(ReceivingContextOf(clientID, native.SessionID, native.AgentID))

	if native.ToolName == "" {
		return op, fmt.Errorf("%w: the request names no tool_name", ErrMalformedEvent)
	}

	if !slices.Contains(guardTools[clientID], native.ToolName) {
		return op, fmt.Errorf("%w: %s", ErrNotApplicable, notCovered)
	}

	ops, err := fileOps(native.ToolName)
	if err != nil {
		return op, err
	}

	op.Ops, op.PathsComplete = ops, true

	return op, nil
}

// receiverIDOf returns the ID of a receiving context, or "" when the
// adapter names none. The guard handler hashes it into the context
// digest, as the advisory hook hashes EventMeta.Context, so the records
// of both hooks name one receiver alike.
func receiverIDOf(receiver *ReceivingContext) string {
	if receiver == nil {
		return ""
	}

	return receiver.ID
}
