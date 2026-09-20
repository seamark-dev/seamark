package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// The Codex names this codec reads and writes. Codex reports every file
// edit as the apply_patch tool, also when the installed matcher says
// Edit or Write.
const (
	codexApplyPatch  = "apply_patch"
	codexPreToolUse  = "PreToolUse"
	codexPostCompact = "PostCompact"
	// codexAdviceMechanism names the reply path of codexEdits: the
	// additionalContext field of a PreToolUse hook reply.
	codexAdviceMechanism = "pre-tool-use-context"
)

// The markers of the apply_patch text. The rules in applyPatchPaths
// follow the parser of codex-cli 0.154.0 (codex-rs/apply-patch, tag
// rust-v0.154.0), not the shorter grammar that Codex shows to the model:
// the parser accepts more than that grammar says, and the hook must read
// what the parser reads.
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

// codexAdvice is the PreToolUse reply shape. Codex adds
// additionalContext to the model context and does not block the tool.
type codexAdvice struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// DecodeEdit reads the complete path set of one apply_patch event. It
// parses the patch text and never runs it. An event of another tool
// gives ErrNotApplicable.
//
// The event carries no receiving context. Codex documents that a
// subagent hook reports the session id of its parent. PreToolUse names
// no subagent. The session id therefore does not say who gets the
// advice. Suppression stays off, and the advice repeats. The session id
// still reaches the firing log as a digest.
func (codexEdits) DecodeEdit(payload []byte) (EditEvent, error) {
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
		},
		Paths: paths,
	}, nil
}

// EncodeAdvice wraps the advisory text in the PreToolUse reply.
func (codexEdits) EncodeAdvice(text string) (HookReply, error) {
	var reply codexAdvice

	reply.HookSpecificOutput.HookEventName = codexPreToolUse
	reply.HookSpecificOutput.AdditionalContext = text

	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(reply); err != nil {
		return HookReply{}, err
	}

	return HookReply{Stdout: out.Bytes()}, nil
}

// AdviceMechanism returns the reply path that the firing log records.
func (codexEdits) AdviceMechanism() string { return codexAdviceMechanism }

// DecodeReset reads a PostCompact event. The result names no context:
// a Codex edit event names no receiver, so no suppression state exists
// for a Codex context, and a reset has nothing to clear. The installed
// hook still runs this decoder. When a native check establishes a
// receiver identity, the edit decoder and this decoder change together
// and the installed hook stays as it is.
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
// in patch order: the file of each add, update, and delete hunk, and the
// destination of each move after its source. Repeated paths stay; the
// shared normalization removes them.
//
// The result is complete or it is an error. A missing envelope or a
// patch without a hunk gives ErrMalformedEvent. A line that the known
// rules do not cover gives ErrUnsupportedGrammar.
//
// The function follows the Codex parser for every rule that decides
// which line is a header and what its path is. A header is matched
// after the parser's own trim, so "*** Update File: a.go " names a.go.
// A wrong path here means advice for a file that Codex does not touch,
// and no advice for the file that it does. The function does not check
// the change lines as strictly as Codex does: a patch that only Codex
// rejects is never applied, so its advice costs nothing.
func applyPatchPaths(patch string) ([]string, error) {
	lines, err := patchBody(patch)
	if err != nil {
		return nil, err
	}

	var (
		paths   []string
		hunk    string // the marker of the open hunk, or "" before the first
		movable bool   // true directly after an update header
	)

	for _, raw := range lines {
		// Codex trims both ends of a line outside an update hunk. Inside
		// one it trims the end only: a leading space marks a context line,
		// so an indented header there is file content.
		line := strings.TrimSpace(raw)
		if hunk == patchUpdateFile {
			line = strings.TrimRightFunc(raw, unicode.IsSpace)
		}

		kind, name, isHeader := patchHeader(line)

		switch {
		case isHeader && kind == patchMoveTo:
			// Codex reads a move only directly under its update header.
			if hunk != patchUpdateFile || !movable {
				return nil, fmt.Errorf("%w: %q outside an update header", ErrUnsupportedGrammar, patchMoveTo)
			}

			paths, movable = append(paths, name), false
		case isHeader && kind == patchEndOfFile:
			if hunk != patchUpdateFile {
				return nil, fmt.Errorf("%w: %q outside an update hunk", ErrUnsupportedGrammar, patchEndOfFile)
			}

			movable = false
		case isHeader:
			paths, hunk, movable = append(paths, name), kind, kind == patchUpdateFile
		case strings.HasPrefix(line, patchEnvironment):
			// The line names an environment that is not the primary one.
			// No native capture shows that the event cwd belongs to that
			// environment, so the paths cannot be resolved with confidence.
			return nil, fmt.Errorf("%w: the patch names an environment", ErrUnsupportedGrammar)
		case strings.HasPrefix(line, patchHeaderPrefix):
			// An unknown header can name a file.
			return nil, fmt.Errorf("%w: unknown header line", ErrUnsupportedGrammar)
		case !patchBodyLine(hunk, raw):
			return nil, fmt.Errorf("%w: unexpected line in the patch body", ErrUnsupportedGrammar)
		default:
			movable = false
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: the patch holds no hunk", ErrMalformedEvent)
	}

	return paths, nil
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
// is the text after the marker, as Codex takes it: a leading space
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
