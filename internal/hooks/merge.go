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
	// Name is the short name of the hook for narration: "gate",
	// "lessons", or "context reset". A file line names what it wrote
	// from the specs it merged, so the name lives with the spec.
	Name string
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
	// OtherForms lists the markers of the same hook in the form of another
	// client, which this client also runs. The readers recognize them like
	// Marker. Merge does not rewrite them, because a document can hold
	// several of them. ReduceOtherForms decides which one stays.
	OtherForms []string
	// Status is the message the client shows while the hook runs.
	// Empty means no message: Merge then writes no statusMessage key.
	Status string
	// Timeout is the hook timeout in seconds.
	Timeout int
	// BackgroundKeys lists the handler keys that make the client run the
	// hook in the background. Merge removes each such key with the value
	// true from seamark's own entry. Only the gate and the pre-write
	// lesson-check specs set it: the client must wait for them, because a
	// gate verdict or a guard deny blocks only by exit status 2, and the
	// baseline must exist before the write.
	BackgroundKeys []string
}

// Foreground reports whether the hook must run in the foreground, so
// that its exit status reaches the client before the action.
func (s Spec) Foreground() bool {
	return len(s.BackgroundKeys) > 0
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
// marker first, then the legacy ones, then the forms of another client.
func (s Spec) Markers() []string {
	return append(s.ownMarkers(), s.OtherForms...)
}

// ownMarkers returns the current marker and the legacy ones: the
// spellings that Merge rewrites to the current marker.
func (s Spec) ownMarkers() []string {
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
			Name:  "gate",
			Event: "PreToolUse", Matcher: "Bash",
			Marker: GateMarker(gateMode), Legacy: []string{GateMarker(other)},
			Status: "seamark gate: classifying command", Timeout: 15,
			BackgroundKeys: ClaudeBackgroundKeys,
		},
		{
			Name:  "lessons",
			Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit",
			Marker: LessonsMarker,
			Status: "seamark: checking review lessons", Timeout: 10,
		},
		{
			Name:   "context reset",
			Event:  "PostCompact",
			Marker: LessonsResetMarker,
			Status: "seamark: resetting lesson delivery", Timeout: 10,
		},
	}
}

// CodexSpecs returns the hooks that setup installs into Codex for a
// gate mode. The gate hook comes first, as in ClaudeSpecs, so a caller
// finds it at index 0. Codex names its shell tool Bash in the event and
// in the matcher; the opposite mode's marker is listed as legacy, so a
// mode switch rewrites the existing gate hook in place. Codex reports
// every file edit as the apply_patch tool. Codex also runs a gate hook
// in the Claude Code form, without the client selector, and the Claude
// codec reads its input. Such a hook blocks, so the gate spec lists both
// Claude Code markers as other forms. CodexMatcher holds the rule
// that Codex uses to read a matcher, with the aliases Edit and Write of
// apply_patch. The timeout is seconds, as in Claude Code.
//
// The list holds no context-reset hook. The Codex edit decoder reads
// no receiver yet: the event names one, but the reset of a subagent is
// unverified, so suppression stays off by policy and a reset has
// nothing to clear. A hook without an effect still costs the user a
// trust review and one process for each compaction. The reset command
// and its decoder exist, and the hook joins the list when the edit
// decoder names a receiver with a verified reset.
func CodexSpecs(gateMode string) []Spec {
	other := ModeEnforce
	if gateMode == ModeEnforce {
		other = ModeWarn
	}

	return []Spec{
		{
			Name:  "gate",
			Event: "PreToolUse", Matcher: "Bash",
			Marker: CodexGateMarker(gateMode), Legacy: []string{CodexGateMarker(other)},
			OtherForms: []string{GateMarker(ModeWarn), GateMarker(ModeEnforce)},
			Status:     "seamark gate: classifying command", Timeout: 15,
			BackgroundKeys: CodexBackgroundKeys,
		},
		{
			Name:  "lessons",
			Event: "PreToolUse", Matcher: "apply_patch",
			Marker: CodexLessonsMarker,
			Status: "seamark: checking review lessons", Timeout: 10,
		},
	}
}

// ManagedRuns reports whether the client runs the managed command of
// the spec, spec.Command(bin), for a tool of the spec: a "command"-typed
// entry with exactly that command under a matcher that fires. The check
// names the command that Merge wrote, so it holds for any binary path,
// also one whose basename OwnedBySeamark does not accept. An entry that
// Merge kept under a matcher that never fires is not a running hook.
func ManagedRuns(settings map[string]any, spec Spec, bin string, fires MatcherRule) bool {
	hookMap, _ := settings["hooks"].(map[string]any)
	eventHooks, _ := hookMap[spec.Event].([]any)
	want := spec.Command(bin)
	runs := false

	ForEachCommand(eventHooks, func(matcher string, h map[string]any, cmd string) {
		if t, _ := h["type"].(string); t == "command" && cmd == want && Fires(spec, matcher, fires) {
			runs = true
		}
	})

	return runs
}

// Fires reports whether an entry with the matcher runs for a tool of
// the spec under the client's rule. An event without a matcher fires
// for every entry.
func Fires(spec Spec, matcher string, fires MatcherRule) bool {
	return slices.ContainsFunc(spec.Tools(), func(tool string) bool { return tool == "" || fires(matcher, tool) })
}

// Owned reports whether the document holds seamark's own command for
// the spec, in any entry of the spec's event.
func Owned(settings map[string]any, spec Spec) bool {
	found := false

	forEachSpecCommand(settings, spec, func(string, map[string]any, string) { found = true })

	return found
}

// Merged reports what Merge did to a document. The narration of a
// setup run reads it, so the narration describes the rewrite that
// Merge made, not one that the narrator works out again.
type Merged struct {
	// Changed is true when the document changed.
	Changed bool
	// Rewritten lists each owned command that Merge replaced, in
	// document order.
	Rewritten []Rewrite
	// Removed lists each background key that Merge removed from an owned
	// entry, in spec order. Setup reports each removal, because Codex
	// asks the user to review a changed hook again.
	Removed []BackgroundRemoval
}

// Rewrite is one owned command that Merge replaced with the command of
// a spec.
type Rewrite struct {
	// From is the command as the document held it.
	From string
	// To is the command that Merge wrote in its place.
	To string
}

// RemovesEnforce reports whether a rewrite replaced a command with the
// enforce marker by one without it. Setup narrates such a rewrite,
// because a plain re-run can remove a flag that the user wrote. The
// caller passes the enforce marker of its client.
func (m Merged) RemovesEnforce(enforceMarker string) bool {
	return slices.ContainsFunc(m.Rewritten, func(r Rewrite) bool {
		return OwnedBySeamark(r.From, []string{enforceMarker}) && !OwnedBySeamark(r.To, []string{enforceMarker})
	})
}

// Merge adds the hooks into a parsed hook document. It keeps every
// other hook, and it updates a seamark hook that is already present,
// so a re-run never adds a duplicate. It reports whether the document
// changed and which owned commands it rewrote. A "hooks" field or an
// event field with the wrong type is an error: init must never
// overwrite the user's data to install a hook.
func Merge(settings map[string]any, bin string, specs []Spec) (Merged, error) {
	var merged Merged

	hookMap, err := ChildMap(settings, "hooks")
	if err != nil {
		return Merged{}, err
	}

	for _, spec := range specs {
		eventHooks, err := ChildSlice(hookMap, spec.Event)
		if err != nil {
			return Merged{}, err
		}

		want := spec.Command(bin)

		found, rewritten, removed := applyExisting(eventHooks, spec, want)
		if found {
			merged.Changed = merged.Changed || len(rewritten) > 0 || len(removed) > 0
			merged.Rewritten = append(merged.Rewritten, rewritten...)
			merged.Removed = append(merged.Removed, removed...)

			continue
		}

		hook := map[string]any{"type": "command", "command": want, "timeout": spec.Timeout}

		// Both clients document the status message as optional. A spec
		// without one gets no key, so the client shows its own default.
		if spec.Status != "" {
			hook["statusMessage"] = spec.Status
		}

		entry := map[string]any{"hooks": []any{hook}}
		if spec.Matcher != "" {
			entry["matcher"] = spec.Matcher
		}

		eventHooks = append(eventHooks, entry)
		hookMap[spec.Event] = eventHooks
		merged.Changed = true
	}

	return merged, nil
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
// either mode and of either client; the spec's markers cover both modes,
// and the mode is read from the marker that owns the command.
func EffectiveGateMode(settings map[string]any, gate Spec, fires MatcherRule) string {
	mode := ""

	forEachSpecCommand(settings, gate, func(matcher string, h map[string]any, cmd string) {
		if t, _ := h["type"].(string); t != "command" {
			return
		}

		if !Fires(gate, matcher, fires) {
			return
		}

		switch {
		case ownedGateMode(cmd, gate) == ModeEnforce:
			mode = ModeEnforce
		case mode == "":
			mode = ModeWarn
		}
	})

	return mode
}

// ownedGateMode returns the mode of a seamark-owned gate command: the
// mode of the spec's marker that owns it. A command that no marker of
// the spec owns gives "".
func ownedGateMode(cmd string, gate Spec) string {
	for _, marker := range gate.Markers() {
		if OwnedBySeamark(cmd, []string{marker}) {
			return gateMarkerMode(marker)
		}
	}

	return ""
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

// codexExactMatcher is the character set of a Codex matcher that lists
// exact names. It holds no space, "-", or ",", so Codex 0.164.0 reads a
// matcher with one of them as a regular expression.
var codexExactMatcher = regexp.MustCompile(`^[A-Za-z0-9_|]*$`)

// codexAliases lists the other names that Codex 0.164.0 tests a matcher
// against for a tool. Codex selects a handler when the matcher fires for
// the tool name or for one alias. Only real tool names have a row. A
// tool name without a row, such as "*", has no alias, so a narrowed
// matcher for it reads as partial and not as full coverage.
var codexAliases = map[string][]string{
	"apply_patch": {"Write", "Edit"},
	"spawn_agent": {"Agent"},
}

// CodexMatcher is the matcher rule of the Codex 0.164.0 source. The
// hooks reference calls every matcher a regular expression, but the
// source reads a name-only matcher as exact names:
//
//   - "*" and an empty or omitted matcher fire for every tool.
//   - A matcher of only ASCII letters, digits, "_", and "|" is a list of
//     exact names, split on "|". The split gives "Bash|" the names
//     "Bash" and "". No tool has an empty name, so "Bash|" fires for
//     Bash only.
//   - Any other matcher is a regular expression. It fires when it
//     matches anywhere in the name, so "^apply_patch$" anchors itself.
//
// Each rule tests the tool name and each of its aliases, so "Edit"
// fires for apply_patch. Codex skips a matcher group that its Rust
// regex crate cannot compile, so no handler of that group runs. Here a
// pattern that Go cannot compile fires for no tool.
//
// Seamark has no Rust regex engine, so this reading uses Go's regexp.
// The two engines compile or match some rare patterns differently. For
// example, Go rejects "a{1001}" and Rust accepts it, and Rust rejects
// "[[]" and Go accepts it.
func CodexMatcher(matcher, tool string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}

	names := append([]string{tool}, codexAliases[tool]...)

	if codexExactMatcher.MatchString(matcher) {
		listed := strings.Split(matcher, "|")

		return slices.ContainsFunc(names, func(name string) bool { return slices.Contains(listed, name) })
	}

	pattern, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(names, pattern.MatchString)
}

// applyExisting rewrites seamark's own hook command to want when it is
// present. A command in another client's form is not rewritten here,
// because ReduceOtherForms keeps at most one of them. OwnedBySeamark
// recognizes the command, so an unrelated
// command that only contains or ends with the marker text stays as it
// is. A foreground spec also loses its background keys, under any
// matcher, because such a hook cannot block. It reports whether such a
// hook exists, each command it rewrote, and each background key it
// removed.
func applyExisting(eventHooks []any, spec Spec, want string) (
	found bool, rewritten []Rewrite, removed []BackgroundRemoval,
) {
	markers := spec.ownMarkers()

	ForEachCommand(eventHooks, func(_ string, h map[string]any, cmd string) {
		if !OwnedBySeamark(cmd, markers) {
			return
		}

		found = true

		if cmd != want {
			h["command"] = want
			rewritten = append(rewritten, Rewrite{From: cmd, To: want})
		}

		removed = append(removed, removeBackground(spec, h)...)
	})

	return found, rewritten, removed
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
