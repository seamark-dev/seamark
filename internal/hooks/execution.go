package hooks

import (
	"fmt"
	"slices"
)

// Execution is what one command handler says about how the client runs
// it, apart from its command and its matcher. Setup reads it before it
// claims that a hook blocks or covers a tool. The command text alone
// cannot tell a hook that blocks from one that the client runs in the
// background.
type Execution struct {
	// Background names the first key of the handler that makes the
	// client run it in the background, or "". The client does not wait
	// for such a handler, so its exit status never blocks.
	Background string
	// Filter is the value of a Claude Code if field, or "". Claude Code
	// then runs the handler only for the tool calls that match the
	// filter, and on an event without a tool it never runs the handler.
	Filter string
}

// ExecutionRule reads the execution of one handler. The rule is the
// client's, as MatcherRule is: each client documents its own handler
// keys, so the caller passes the rule of the client that reads the
// document.
type ExecutionRule func(handler map[string]any) Execution

// The handler keys that make each client run a handler in the
// background. Claude Code documents async and asyncRewake. In Claude
// Code 2.1.314, asyncRewake runs in the background only under a
// condition, and setup reads it as background in every case, because
// that reading never claims a block that does not happen. Codex reads
// async only, and it ignores asyncRewake.
var (
	ClaudeBackgroundKeys = []string{"async", "asyncRewake"}
	CodexBackgroundKeys  = []string{"async"}
)

// claudeFilterKey is the Claude Code handler field that filters a hook
// by a permission rule.
const claudeFilterKey = "if"

// ClaudeExecution is the execution rule of Claude Code. It reads the
// background keys and the if field. It does not read onFailure: a deny
// exits with status 2, and status 2 blocks whatever onFailure says.
func ClaudeExecution(handler map[string]any) Execution {
	return Execution{Background: backgroundKey(handler, ClaudeBackgroundKeys), Filter: claudeFilter(handler)}
}

// CodexExecution is the execution rule of Codex. A Codex handler has no
// if field and no asyncRewake, so the rule reads async only.
func CodexExecution(handler map[string]any) Execution {
	return Execution{Background: backgroundKey(handler, CodexBackgroundKeys)}
}

// InBackground reports whether the handler sets the background key to
// the boolean true. False and an absent key leave the handler in the
// foreground. A value of another type does not count as background here.
// Claude Code also tests for the boolean true, and Codex refuses a hook
// file with such a value. The execution rules, Merge, and setup's
// comparison of two handler copies use this test, so their readings of
// one handler agree.
func InBackground(handler map[string]any, key string) bool {
	on, _ := handler[key].(bool)

	return on
}

// backgroundKey returns the first of keys that the handler sets to the
// boolean true, or "".
func backgroundKey(handler map[string]any, keys []string) string {
	i := slices.IndexFunc(keys, func(key string) bool { return InBackground(handler, key) })
	if i < 0 {
		return ""
	}

	return keys[i]
}

// claudeFilter returns the if field of a handler, or "" when the field
// is absent or empty. A value of another type still counts as a filter,
// because setup cannot tell what Claude Code does with it. A filter that
// setup ignores could claim coverage that the client does not give.
func claudeFilter(handler map[string]any) string {
	switch value := handler[claudeFilterKey].(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

// claudeToolEvents are the Claude Code events that carry a tool call.
// Claude Code evaluates an if filter against that call. On any other
// event a hook with an if filter never runs.
var claudeToolEvents = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied"}

// ClaudeToolEvent reports whether a Claude Code event carries a tool
// call, so an if filter on a hook of that event can match.
func ClaudeToolEvent(event string) bool {
	return slices.Contains(claudeToolEvents, event)
}

// BackgroundRemoval is one background key that Merge removed from
// seamark's own entry.
type BackgroundRemoval struct {
	// Name is the name of the spec, for example "gate".
	Name string
	// Event is the event of the entry, for example "PreToolUse".
	Event string
	// Key is the removed key, for example "async".
	Key string
}

// removeBackground removes each of the spec's background keys that the
// handler sets to the boolean true. A key with another value stays, so
// a handler without such a key keeps its bytes. It returns the removals.
func removeBackground(spec Spec, handler map[string]any) []BackgroundRemoval {
	var removed []BackgroundRemoval

	for _, key := range spec.BackgroundKeys {
		if InBackground(handler, key) {
			delete(handler, key)
			removed = append(removed, BackgroundRemoval{Name: spec.Name, Event: spec.Event, Key: key})
		}
	}

	return removed
}
