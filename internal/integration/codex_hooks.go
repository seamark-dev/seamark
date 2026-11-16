package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// These are the Codex names that this codec reads and writes. Codex
// reports every file edit as the apply_patch tool, also when the
// installed matcher says Edit or Write.
const (
	codexApplyPatch  = "apply_patch"
	codexPreToolUse  = "PreToolUse"
	codexPostCompact = "PostCompact"
)

// The markers of the apply_patch text. The rules in applyPatchOps
// follow the parser of codex-cli 0.154.0 (codex-rs/apply-patch, tag
// rust-v0.154.0). They do not follow the shorter grammar that Codex
// shows to the model. The parser accepts more than that grammar says,
// and the hook must read what the parser reads.
const (
	patchHeaderPrefix = "***"
	patchBegin        = "*** Begin Patch"
	patchEnd          = "*** End Patch"
	patchEnvironment  = "*** Environment ID:"
	patchAddFile      = "*** Add File: "
	patchDeleteFile   = "*** Delete File: "
	patchUpdateFile   = "*** Update File: "
	patchMoveTo       = "*** Move to: "
	patchEndOfFile    = "*** End of File"
)

// codexEdits translates the Codex PreToolUse apply_patch event.
type codexEdits struct{}

// codexResets translates the Codex PostCompact event.
type codexResets struct{}

// DecodeEdit reads the complete path set of one apply_patch event. It
// parses the patch text and never runs it. An event of another tool
// gives ErrNotApplicable.
//
// The decoder names the receiver from session_id and agent_id, by the
// rules of ReceivingContextOf. That context is never resettable, because
// no reset of a Codex context is verified. So suppression stays off, and
// the advice repeats. The lane still reaches the firing log. This
// decoder and DecodeReset change together when a native check
// establishes a reset policy.
//
// turn_id is the turn. The decoder reads identity from the top-level
// fields only, never from the patch text.
func (codexEdits) DecodeEdit(payload []byte) (EditEvent, error) {
	var native struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		TurnID    string `json:"turn_id"`
		CWD       string `json:"cwd"`
		ToolUseID string `json:"tool_use_id"`
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command *string `json:"command"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return EditEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	if native.ToolName != codexApplyPatch {
		return EditEvent{}, fmt.Errorf("%w: tool %q is not %s", ErrNotApplicable, native.ToolName, codexApplyPatch)
	}

	if native.ToolInput.Command == nil {
		return EditEvent{}, fmt.Errorf("%w: %s event without tool_input.command", ErrMalformedEvent, codexApplyPatch)
	}

	paths, err := applyPatchPaths(*native.ToolInput.Command)
	if err != nil {
		return EditEvent{}, err
	}

	return EditEvent{
		EventMeta: EventMeta{
			SessionID:  native.SessionID,
			MatchID:    native.ToolUseID,
			NativeTool: native.ToolName,
			CWD:        native.CWD,
			Context:    ReceivingContextOf(CodexID, native.SessionID, native.AgentID),
			Turn:       native.TurnID,
		},
		Paths: paths,
	}, nil
}

// EncodeAdvice wraps the advisory text in the PreToolUse reply. Codex
// adds additionalContext to the model context and does not block the
// tool.
func (codexEdits) EncodeAdvice(text string) (HookReply, error) {
	return encodeContextReply(codexPreToolUse, text)
}

// AdviceMechanism returns the reply path that the firing log records.
func (codexEdits) AdviceMechanism() string { return preToolUseContextMechanism }

// DecodeReset reads a PostCompact event. The result names no context.
// DecodeEdit names a receiver, but that receiver is never resettable. So
// no suppression state exists for a Codex context, and a reset has
// nothing to clear. Setup installs no PostCompact hook for Codex,
// because a hook without an effect still costs a trust review. The
// command `lessons --hook-reset --client codex` still runs this decoder,
// so a hook that a user wires stays valid. When a native check
// establishes the reset policy of each receiver, the edit decoder and
// this decoder change together.
func (codexResets) DecodeReset(payload []byte) (ResetEvent, error) {
	var native struct {
		Event string `json:"hook_event_name"`
	}

	if err := json.Unmarshal(payload, &native); err != nil {
		return ResetEvent{}, fmt.Errorf("%w: %v", ErrMalformedEvent, err)
	}

	if native.Event != codexPostCompact {
		return ResetEvent{}, fmt.Errorf("%w: event %q is not %s", ErrNotApplicable, native.Event, codexPostCompact)
	}

	return ResetEvent{}, nil
}

// applyPatchPaths returns every path that an apply_patch text affects,
// in patch order. These are the file of each add, update, and delete
// hunk, and the destination of each move after its source. Repeated
// paths stay; the shared normalization removes them.
//
// The result is complete or it is an error, by the rules of
// applyPatchOps. The advisory edit hook and the guard then read the
// paths of one patch alike. The guard and the post-edit check also
// refuse a header path with a tab or a carriage return (guardPatchOps).
// The advisory hook keeps such a path, so its replies do not change.
func applyPatchPaths(patch string) ([]string, error) {
	ops, err := applyPatchOps(patch)
	if err != nil {
		return nil, err
	}

	return patchOpPaths(ops), nil
}

// patchBody returns the lines between the begin line and the end line.
// Codex trims the whole text, drops one carriage return at each line
// end, and compares the two envelope lines after a trim. It also accepts
// the text inside a shell heredoc, "<<'EOF'" to "EOF", because some
// models send that form.
func patchBody(patch string) ([]string, error) {
	lines := strings.Split(strings.TrimSpace(patch), "\n")

	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}

	if !patchEnvelope(lines) {
		first, last := lines[0], lines[len(lines)-1]
		heredoc := first == "<<EOF" || first == "<<'EOF'" || first == `<<"EOF"`

		if !heredoc || !strings.HasSuffix(last, "EOF") || len(lines) < 4 || !patchEnvelope(lines[1:len(lines)-1]) {
			return nil, fmt.Errorf("%w: the patch has no %q to %q envelope", ErrMalformedEvent, patchBegin, patchEnd)
		}

		lines = lines[1 : len(lines)-1]
	}

	return lines[1 : len(lines)-1], nil
}

// patchEnvelope reports whether the lines start with the begin line and
// end with the end line. One line cannot be both.
func patchEnvelope(lines []string) bool {
	return len(lines) >= 2 &&
		strings.TrimSpace(lines[0]) == patchBegin && strings.TrimSpace(lines[len(lines)-1]) == patchEnd
}

// patchHeader reads a header from a line that the caller already
// trimmed by the Codex rule of the open hunk. kind is the marker. name
// is the text after the marker, as Codex reads it: a leading space
// stays a part of the name. A file header without a name is no header,
// so the caller reports the line as an unknown header.
func patchHeader(line string) (kind, name string, ok bool) {
	if line == patchEndOfFile {
		return patchEndOfFile, "", true
	}

	for _, marker := range []string{patchAddFile, patchDeleteFile, patchUpdateFile, patchMoveTo} {
		if rest, found := strings.CutPrefix(line, marker); found && rest != "" {
			return marker, rest, true
		}
	}

	return "", "", false
}

// patchBodyLine reports whether a raw line that is no header can stand
// in the open hunk. An add hunk holds "+" lines. An update hunk holds
// "+", "-", and " " lines, "@@" context lines, and empty lines, which
// Codex reads as empty context. A delete hunk and the space before the
// first hunk hold no line.
func patchBodyLine(hunk, raw string) bool {
	switch hunk {
	case patchAddFile:
		return strings.HasPrefix(raw, "+")
	case patchUpdateFile:
		line := strings.TrimRightFunc(raw, unicode.IsSpace)

		return line == "" || strings.HasPrefix(line, "@@") || strings.ContainsRune("+- ", rune(raw[0]))
	default:
		return false
	}
}
