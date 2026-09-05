package bench

import (
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// WorkflowTrace is what the transcript proves about the agent's process:
// whether it asked Seamark before editing, whether Seamark named the
// companion file, whether the agent followed that lead, whether it checked
// the finished diff, and which skills activated. Every field is read from
// the agent's own tool calls and their results, never from its prose.
type WorkflowTrace struct {
	// ChangeSetBeforeFirstEdit is true when a change_set call preceded the
	// first Edit or Write, or when the agent never edited at all.
	ChangeSetBeforeFirstEdit bool `json:"change_set_before_first_edit"`
	// ChangeSetFiles lists the files of the first change_set call.
	ChangeSetFiles []string `json:"change_set_files,omitempty"`
	// CompanionNamedByChangeSet is true when a change_set result named the
	// companion file that the agent had not put in the call itself.
	CompanionNamedByChangeSet bool `json:"companion_named_by_change_set"`
	// CompanionNamedByCheck is true when a check result listed the companion
	// under "history suggests also reviewing": the diff left it out and
	// history named it. The unindexed-files note also quotes paths, so only
	// that section counts.
	CompanionNamedByCheck bool `json:"companion_named_by_check"`
	// WhyFollowedCompanion is true when a why call whose query is the
	// companion path followed the change_set or check that named it. A
	// query by symbol or by bare file name does not count; the rule is the
	// same in both arms.
	WhyFollowedCompanion bool `json:"why_followed_companion"`
	// CompanionOpenedAfterNamed is true when a Read, Edit, Write, or
	// MultiEdit on the companion followed the call that named it. The first
	// cohort showed this is the step that decides the outcome: a named
	// companion the agent never opened was never acted on.
	CompanionOpenedAfterNamed bool `json:"companion_opened_after_named"`
	// CheckAfterLastEdit is true when a check call followed the last edit.
	CheckAfterLastEdit bool `json:"check_after_last_edit"`
	// CheckVerdict is the verdict line of the last check result, for
	// example "allow (mode: warn)"; empty when the call failed.
	CheckVerdict string `json:"check_verdict,omitempty"`
	// Activations lists the skills the agent loaded, in first-use order.
	Activations []string `json:"activations,omitempty"`
	// SeamarkCalls counts every seamark MCP tool call; SeamarkToolCalls
	// splits the count by tool name.
	SeamarkCalls     int            `json:"seamark_calls"`
	SeamarkToolCalls map[string]int `json:"seamark_tool_calls,omitempty"`
	// Edits counts Edit, Write, and MultiEdit calls. Edits made through
	// Bash are invisible here; the patch stays the record of what changed.
	Edits int `json:"edits"`
	// FirstEditSeq is the position of the first edit among all tool calls,
	// counted from 1; zero when there was no edit.
	FirstEditSeq int `json:"first_edit_seq,omitempty"`
}

// seamarkToolPrefix is how Claude Code names an MCP tool: server, then tool.
const seamarkToolPrefix = "mcp__seamark__"

// toolUse is one tool call in transcript order, joined with its result.
type toolUse struct {
	name   string
	input  json.RawMessage
	result string
}

// contentBlock is one block of an assistant or user message. The two block
// kinds the trace reads share one shape: tool_use carries id, name, and
// input; tool_result carries tool_use_id and content.
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// parseWorkflowTrace reads the stream-json transcript into a WorkflowTrace.
// Best-effort: a stub or plain-text agent yields an empty trace.
func parseWorkflowTrace(stdout []byte, companion string) WorkflowTrace {
	var (
		trace                                         WorkflowTrace
		lastEdit, firstChangeSet, namedSeq, lastCheck int
	)

	for i, use := range collectToolUses(stdout) {
		seq := i + 1
		switch {
		case strings.HasPrefix(use.name, seamarkToolPrefix):
			tool := strings.TrimPrefix(use.name, seamarkToolPrefix)
			trace.SeamarkCalls++

			if trace.SeamarkToolCalls == nil {
				trace.SeamarkToolCalls = map[string]int{}
			}

			trace.SeamarkToolCalls[tool]++

			switch tool {
			case "change_set":
				files := inputStrings(use.input, "files")
				if firstChangeSet == 0 {
					firstChangeSet = seq
					trace.ChangeSetFiles = files
				}

				if !trace.CompanionNamedByChangeSet && companionNamed(use.result, files, companion) {
					trace.CompanionNamedByChangeSet = true
					if namedSeq == 0 {
						namedSeq = seq
					}
				}
			case "why":
				if namedSeq > 0 && seq > namedSeq &&
					samePath(inputString(use.input, "query"), companion) {
					trace.WhyFollowedCompanion = true
				}
			case "check":
				lastCheck = seq
				trace.CheckVerdict = verdictLine(use.result)

				if !trace.CompanionNamedByCheck && companionSuggested(use.result, companion) {
					trace.CompanionNamedByCheck = true
					if namedSeq == 0 {
						namedSeq = seq
					}
				}
			}
		case editTool(use.name):
			trace.Edits++
			if trace.FirstEditSeq == 0 {
				trace.FirstEditSeq = seq
			}

			lastEdit = seq

			if namedSeq > 0 && seq > namedSeq && pathIs(inputString(use.input, "file_path"), companion) {
				trace.CompanionOpenedAfterNamed = true
			}
		case use.name == "Read":
			if namedSeq > 0 && seq > namedSeq && pathIs(inputString(use.input, "file_path"), companion) {
				trace.CompanionOpenedAfterNamed = true
			}
		case use.name == "Skill":
			name := inputString(use.input, "skill")
			if name == "" {
				name = inputString(use.input, "name")
			}

			if name != "" && !slices.Contains(trace.Activations, name) {
				trace.Activations = append(trace.Activations, name)
			}
		}
	}

	trace.ChangeSetBeforeFirstEdit = firstChangeSet > 0 && (trace.FirstEditSeq == 0 || firstChangeSet < trace.FirstEditSeq)
	trace.CheckAfterLastEdit = lastCheck > 0 && lastCheck > lastEdit

	return trace
}

// collectToolUses lists the agent's tool calls in order and attaches each
// tool result to its call by id. Only the agent's own messages create calls;
// tool results arrive in "user" messages.
func collectToolUses(stdout []byte) []toolUse {
	var uses []toolUse
	byID := map[string]int{}

	for line := range strings.SplitSeq(string(stdout), "\n") {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}

		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		switch msg.Type {
		case "assistant":
			for _, block := range contentBlocks(msg.Message.Content) {
				if block.Type != "tool_use" {
					continue
				}

				uses = append(uses, toolUse{name: block.Name, input: block.Input})
				if block.ID != "" {
					byID[block.ID] = len(uses) - 1
				}
			}
		case "user":
			for _, block := range contentBlocks(msg.Message.Content) {
				if block.Type != "tool_result" {
					continue
				}

				if i, ok := byID[block.ToolUseID]; ok {
					uses[i].result = blockText(block.Content)
				}
			}
		}
	}

	return uses
}

// contentBlocks decodes a message's content list. Plain-string content (a
// user prompt) has no blocks.
func contentBlocks(raw json.RawMessage) []contentBlock {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}

	return blocks
}

// blockText renders a tool result's content as text. Claude Code writes
// either one string or a list of text blocks.
func blockText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}

	var out strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			out.WriteString(part.Text)
			out.WriteByte('\n')
		}
	}

	return out.String()
}

func inputString(input json.RawMessage, key string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}

	var value string
	if json.Unmarshal(fields[key], &value) != nil {
		return ""
	}

	return value
}

func inputStrings(input json.RawMessage, key string) []string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return nil
	}

	var values []string
	if json.Unmarshal(fields[key], &values) != nil {
		return nil
	}

	return values
}

// companionNamed reports whether a change_set result named the companion as
// evidence rather than echoing a file the agent already planned: the result
// contains the path and the call did not list it.
func companionNamed(result string, files []string, companion string) bool {
	if !strings.Contains(result, companion) {
		return false
	}

	for _, file := range files {
		if samePath(file, companion) {
			return false
		}
	}

	return true
}

// companionSuggested reports whether a check result lists the companion
// under "history suggests also reviewing". The section ends at the first
// blank line; a path quoted elsewhere in the result, such as the
// unindexed-files note, does not count.
func companionSuggested(result, companion string) bool {
	inSection := false

	for line := range strings.SplitSeq(result, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "history suggests also reviewing"):
			inSection = true
		case trimmed == "":
			inSection = false
		case inSection:
			if field, _, _ := strings.Cut(trimmed, " "); samePath(field, companion) {
				return true
			}
		}
	}

	return false
}

// samePath compares two repository-relative paths after normalization, so
// "./web/src/api/generated.ts" and "web/src/api/generated.ts" are equal.
func samePath(a, b string) bool {
	return cleanPath(a) == cleanPath(b)
}

// pathIs reports whether a tool's file_path, which Claude Code writes as an
// absolute path inside the trial, is the repository-relative file.
func pathIs(filePath, file string) bool {
	clean := cleanPath(filePath)

	return clean == cleanPath(file) || strings.HasSuffix(clean, "/"+cleanPath(file))
}

func cleanPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}

	return path.Clean(strings.TrimPrefix(value, "./"))
}

// verdictLine returns the text after "verdict" on the check result's verdict
// line, or "" when the result carries none.
func verdictLine(result string) string {
	for line := range strings.SplitSeq(result, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "verdict")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}

		return strings.TrimSpace(rest)
	}

	return ""
}
