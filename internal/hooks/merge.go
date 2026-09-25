package hooks

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Spec is one lifecycle hook seamark installs into a client's hook
// document. The document shape is the one Claude Code and Codex share:
// an event name that maps to entries, each with a matcher and a list of
// command hooks.
type Spec struct {
	// Event is the native event name, for example "PreToolUse".
	Event string
	// Matcher is the native tool matcher. Empty means the event has none.
	Matcher string
	// Marker is the command's argument tail, everything after the binary
	// path. Merge matches it as a suffix, with the joining space, so an
	// existing seamark hook is recognized after the binary path changes.
	// An unrelated command that only contains the same text never matches.
	Marker string
	// Legacy lists older marker spellings of the same hook. Merge
	// recognizes them like Marker and rewrites them to it, so a re-run
	// migrates an existing hook and never adds a duplicate beside it.
	Legacy []string
	// Status is the message the client shows while the hook runs.
	Status string
	// Timeout is the hook timeout in seconds.
	Timeout int
}

// Command returns the full hook command for a binary path.
func (s Spec) Command(bin string) string {
	return ShellQuote(bin) + " " + s.Marker
}

// Tools returns the tool names the matcher lists. A spec without a
// matcher returns one empty name, which stands for the event itself, so
// coverage is computed the same way for both kinds of hook.
func (s Spec) Tools() []string {
	return strings.Split(s.Matcher, "|")
}

// Markers returns every spelling that identifies the hook: the current
// marker first, then the legacy ones.
func (s Spec) Markers() []string {
	return append([]string{s.Marker}, s.Legacy...)
}

// ClaudeSpecs returns the hooks init installs into Claude Code for a
// gate mode. The opposite mode's marker is listed as legacy, so a mode
// switch rewrites the existing gate hook in place.
func ClaudeSpecs(gateMode string) []Spec {
	other := ModeEnforce
	if gateMode == ModeEnforce {
		other = ModeWarn
	}

	return []Spec{
		{
			Event: "PreToolUse", Matcher: "Bash",
			Marker: GateMarker(gateMode), Legacy: []string{GateMarker(other)},
			Status: "seamark gate: classifying command", Timeout: 15,
		},
		{
			Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit",
			Marker: LessonsMarker,
			Status: "seamark: checking review lessons", Timeout: 10,
		},
		{
			Event:  "PostCompact",
			Marker: LessonsResetMarker,
			Status: "seamark: resetting lesson delivery", Timeout: 10,
		},
	}
}

// CodexSpecs returns the hooks that setup installs into Codex. Codex
// reports every file edit as the apply_patch tool. Its matcher is a
// regular expression, and "apply_patch" also matches the documented
// aliases Edit and Write. The timeout is seconds, as in Claude Code.
// The command gate joins the list with its own slice.
//
// The list holds no context-reset hook. The Codex edit decoder reads
// no receiver yet: the event names one, but the reset of a subagent is
// unverified, so suppression stays off by policy and a reset has
// nothing to clear. A hook without an effect still costs the user a
// trust review and one process for each compaction. The reset command
// and its decoder exist, and the hook joins the list when the edit
// decoder names a receiver with a verified reset.
func CodexSpecs() []Spec {
	return []Spec{
		{
			Event: "PreToolUse", Matcher: "apply_patch",
			Marker: CodexLessonsMarker,
			Status: "seamark: checking review lessons", Timeout: 10,
		},
	}
}

// WrappedCommand is a hook command that setup does not own and that
// runs, or can run, a seamark hook.
type WrappedCommand struct {
	Command string
	// Certain is true when the shell executes the seamark hook, and
	// false when another program gets the seamark command as arguments.
	Certain bool
}

// Wrapped returns the commands of a parsed hook document that run the
// hook of the spec, or can run it, and are not seamark's own command:
// a wrapper, a shell condition, a redirect. Merge does not touch such a
// command. A caller that adds the spec beside it makes the hook run
// twice. A command that only prints the hook text is not in the result.
func Wrapped(settings map[string]any, spec Spec) []WrappedCommand {
	var out []WrappedCommand

	hookMap, _ := settings["hooks"].(map[string]any)
	eventHooks, _ := hookMap[spec.Event].([]any)

	ForEachCommand(eventHooks, func(_ string, _ map[string]any, cmd string) {
		if OwnedBySeamark(cmd, spec.Markers()) {
			return
		}

		if use := SeamarkHookUse(cmd, spec.Markers()); use != HookNotRun {
			out = append(out, WrappedCommand{Command: cmd, Certain: use == HookRuns})
		}
	})

	return out
}

// Owned reports whether the document holds seamark's own command for
// the spec, in any entry of the spec's event.
func Owned(settings map[string]any, spec Spec) bool {
	found := false

	forEachSpecCommand(settings, spec, func(string, map[string]any, string) { found = true })

	return found
}

// Merge adds the hooks into a parsed hook document. It keeps every
// other hook, and it updates a seamark hook that is already present,
// so a re-run never adds a duplicate. It reports whether the document
// changed. A "hooks" field or an event field with the wrong type is an
// error: init must never overwrite the user's data to install a hook.
func Merge(settings map[string]any, bin string, specs []Spec) (changed bool, err error) {
	hookMap, err := ChildMap(settings, "hooks")
	if err != nil {
		return false, err
	}

	for _, spec := range specs {
		eventHooks, err := ChildSlice(hookMap, spec.Event)
		if err != nil {
			return false, err
		}

		want := spec.Command(bin)

		found, updated := applyExisting(eventHooks, spec.Markers(), want)
		if found {
			changed = changed || updated

			continue
		}

		entry := map[string]any{
			"hooks": []any{map[string]any{
				"type":          "command",
				"command":       want,
				"timeout":       spec.Timeout,
				"statusMessage": spec.Status,
			}},
		}
		if spec.Matcher != "" {
			entry["matcher"] = spec.Matcher
		}

		eventHooks = append(eventHooks, entry)
		hookMap[spec.Event] = eventHooks
		changed = true
	}

	return changed, nil
}

// MatcherRule reports whether an installed matcher fires for one tool.
// The rule is the client's, not seamark's: each client documents its own
// matcher syntax, so the caller passes the rule of the client that reads
// the document.
type MatcherRule func(matcher, tool string) bool

// Covered returns the spec's tools that the document really runs a
// seamark handler for, and the first such command. An entry counts only
// when the client runs it: a "command"-typed hook whose matcher fires
// for the tool. Merge is wider on purpose: it rewrites the owned entry
// under any matcher. Setup uses Covered to report a second hook source
// that runs the same handler, and an entry that never fires is not a
// duplicate.
//
// Coverage is per tool, so the report can name exactly the tools that
// two sources both handle, and the tools that no source handles.
func Covered(settings map[string]any, spec Spec, fires MatcherRule) (tools []string, command string) {
	forEachSpecCommand(settings, spec, func(matcher string, h map[string]any, cmd string) {
		if t, _ := h["type"].(string); t != "command" {
			return
		}

		for _, tool := range spec.Tools() {
			// The empty tool stands for an event without a matcher, where
			// every entry fires.
			if (tool == "" || fires(matcher, tool)) && !slices.Contains(tools, tool) {
				tools = append(tools, tool)
			}
		}

		if len(tools) > 0 && command == "" {
			command = cmd
		}
	})

	return tools, command
}

// EffectiveGateMode returns the mode of the gate hooks that the client
// really runs from one document: enforce when any of them enforces, else
// warn, else "" when none runs. Enforce wins, because one enforcing hook
// blocks whatever the other hooks do. The caller passes the gate spec of
// either mode; the spec's markers cover both.
func EffectiveGateMode(settings map[string]any, gate Spec, fires MatcherRule) string {
	mode := ""

	forEachSpecCommand(settings, gate, func(matcher string, h map[string]any, cmd string) {
		if t, _ := h["type"].(string); t != "command" {
			return
		}

		if !slices.ContainsFunc(gate.Tools(), func(tool string) bool { return tool == "" || fires(matcher, tool) }) {
			return
		}

		switch {
		case OwnedBySeamark(cmd, []string{GateMarker(ModeEnforce)}):
			mode = ModeEnforce
		case mode == "":
			mode = ModeWarn
		}
	})

	return mode
}

// forEachSpecCommand visits every seamark-owned command of the spec's
// event, whatever its matcher or type.
func forEachSpecCommand(settings map[string]any, spec Spec, fn func(matcher string, h map[string]any, cmd string)) {
	hookMap, _ := settings["hooks"].(map[string]any)
	eventHooks, _ := hookMap[spec.Event].([]any)

	ForEachCommand(eventHooks, func(matcher string, h map[string]any, cmd string) {
		if OwnedBySeamark(cmd, spec.Markers()) {
			fn(matcher, h, cmd)
		}
	})
}

// exactMatcher is the character set of a Claude Code matcher that lists
// exact names. Any other character puts the matcher on the
// regular-expression path.
var exactMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)

// ClaudeMatcher is Claude Code's documented matcher rule (hooks
// reference, consulted 2026-09-20):
//
//   - "*", an empty matcher, and an omitted matcher fire for every tool.
//   - A matcher of only letters, digits, "_", "-", spaces, "," and "|" is
//     a list of exact names. "|" and "," separate the names, and spaces
//     around a name do not count. "Edit" does not fire for MultiEdit.
//   - Any other matcher is an unanchored regular expression: it fires
//     when it matches anywhere in the tool name. "Edit$" fires for
//     MultiEdit too.
//
// Claude Code evaluates the expression as JavaScript. A pattern that Go
// cannot compile, such as one with a lookahead, covers nothing here. A
// missed duplicate costs a second reminder; a tool wrongly counted as
// covered would get no handler at all.
func ClaudeMatcher(matcher, tool string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}

	if exactMatcher.MatchString(matcher) {
		names := strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' })

		return slices.ContainsFunc(names, func(name string) bool { return strings.TrimSpace(name) == tool })
	}

	pattern, err := regexp.Compile(matcher)

	return err == nil && pattern.MatchString(tool)
}

// applyExisting rewrites seamark's own hook command to want when it is
// present. OwnedBySeamark recognizes the command, so an unrelated
// command that only contains or ends with the marker text stays as it
// is. It reports whether such a hook exists and whether it changed.
func applyExisting(eventHooks []any, markers []string, want string) (found, updated bool) {
	ForEachCommand(eventHooks, func(_ string, h map[string]any, cmd string) {
		if !OwnedBySeamark(cmd, markers) {
			return
		}

		found = true

		if cmd != want {
			h["command"] = want
			updated = true
		}
	})

	return found, updated
}

// shellSpecial lists the characters that make a shell split or
// interpret a word. ShellQuote and the ownership rule share the list,
// so setup recognizes exactly the commands that it writes.
const shellSpecial = " \t\"'\\$`(){}[]*?&|;<>#~"

// ShellQuote single-quotes a path that a shell would split or
// interpret. A clean path returns unchanged. Clients run hook commands
// through a shell, so a binary path with spaces needs the quotes.
func ShellQuote(s string) string {
	if !strings.ContainsAny(s, shellSpecial) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ChildMap returns m[key] as a map and creates it in place when absent.
// A present value with another type is an error, not an overwrite:
// setup must never destroy the user's data to install its own.
func ChildMap(m map[string]any, key string) (map[string]any, error) {
	switch v := m[key].(type) {
	case map[string]any:
		return v, nil
	case nil:
		child := map[string]any{}
		m[key] = child

		return child, nil
	default:
		return nil, fmt.Errorf("%q is present but not an object; refusing to overwrite it", key)
	}
}

// ChildSlice returns m[key] as a slice, nil when absent, or an error
// when present with another type, for the same reason as ChildMap.
func ChildSlice(m map[string]any, key string) ([]any, error) {
	switch v := m[key].(type) {
	case []any:
		return v, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%q is present but not an array; refusing to overwrite it", key)
	}
}
