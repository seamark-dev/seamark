package approve

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"
)

// InlineHook is one hook that .codex/config.toml defines inline under
// [hooks], with the identity Codex reads it by. A duplicate check needs
// the identity: a seamark gate command under another event, or under a
// matcher that never fires for the shell tool, runs no gate for shell
// commands, and the command text alone cannot tell.
type InlineHook struct {
	// Event is the key under [hooks], for example "PreToolUse".
	Event string
	// Matcher is the entry's matcher; empty when the entry has none.
	Matcher string
	// Type is the hook type, for example "command".
	Type    string
	Command string
	// Known is true when the walk read the entry in the documented
	// layout: an array of entries per event, each with an optional
	// matcher and a hooks array. A command found in any other layout has
	// its event and its command only; its matcher and type are unknown,
	// not empty.
	Known bool
}

// CodexInlineHooks reports the hook commands that .codex/config.toml
// defines inline under [hooks]. Codex merges the inline hooks with
// .codex/hooks.json, so a seamark hook in both sources runs twice.
// present is true when the file has a non-empty [hooks] table, also
// when the table holds no command the function can read.
//
// The function reads only. The inline table is the user's, and setup
// manages the one representation in hooks.json.
func CodexInlineHooks(data []byte) (commands []string, present bool, err error) {
	entries, present, err := CodexInlineHookEntries(data)
	if err != nil || !present {
		return nil, present, err
	}

	for _, entry := range entries {
		commands = append(commands, entry.Command)
	}

	// A map walk has no stable order, and a finding must be reproducible.
	slices.Sort(commands)

	return commands, true, nil
}

// CodexInlineHookEntries is CodexInlineHooks with the identity of each
// hook. The entries are sorted by event, matcher, and command, so a
// finding is reproducible.
func CodexInlineHookEntries(data []byte) (entries []InlineHook, present bool, err error) {
	cfg := map[string]any{}

	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, false, fmt.Errorf("%s: %w", CodexConfig, err)
	}

	table, ok := cfg["hooks"].(map[string]any)
	if !ok || len(table) == 0 {
		return nil, false, nil
	}

	for event, value := range table {
		entries = append(entries, inlineEntries(event, value)...)
	}

	slices.SortFunc(entries, func(a, b InlineHook) int {
		if c := cmp.Compare(a.Event, b.Event); c != 0 {
			return c
		}

		if c := cmp.Compare(a.Matcher, b.Matcher); c != 0 {
			return c
		}

		return cmp.Compare(a.Command, b.Command)
	})

	return entries, true, nil
}

// inlineEntries reads the hooks of one event. The documented layout is
// an array of entries, each with an optional matcher and a hooks array
// of typed commands; a hook read from it is Known. Any other layout is
// walked for command strings at any depth, and each one is reported
// with its event only, so an unusual file still names its commands.
func inlineEntries(event string, value any) []InlineHook {
	var out []InlineHook

	for _, entry := range tableList(value) {
		matcher, _ := entry["matcher"].(string)
		hooks, exact := entry["hooks"]

		for _, hook := range tableList(hooks) {
			command, ok := hook["command"].(string)
			if !ok {
				continue
			}

			hookType, _ := hook["type"].(string)
			out = append(out, InlineHook{Event: event, Matcher: matcher, Type: hookType, Command: command, Known: true})
		}

		if !exact {
			for _, command := range collectCommands(entry, nil) {
				out = append(out, InlineHook{Event: event, Command: command})
			}
		}
	}

	if len(tableList(value)) == 0 {
		for _, command := range collectCommands(value, nil) {
			out = append(out, InlineHook{Event: event, Command: command})
		}
	}

	return out
}

// tableList returns the tables of a decoded TOML array, in either
// representation the decoder uses. A value that is no array of tables
// gives nil.
func tableList(value any) []map[string]any {
	switch v := value.(type) {
	case []map[string]any:
		return v
	case []any:
		var out []map[string]any

		for _, child := range v {
			if table, ok := child.(map[string]any); ok {
				out = append(out, table)
			}
		}

		return out
	default:
		return nil
	}
}

// collectCommands walks a decoded TOML value and returns every string
// under a "command" key, at any depth. The walk does not depend on the
// exact inline layout, which Codex documents only by example.
func collectCommands(value any, out []string) []string {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if command, ok := child.(string); ok && key == "command" {
				out = append(out, command)

				continue
			}

			out = collectCommands(child, out)
		}
	case []map[string]any:
		for _, child := range v {
			out = collectCommands(child, out)
		}
	case []any:
		for _, child := range v {
			out = collectCommands(child, out)
		}
	}

	return out
}
