package approve

import (
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"
)

// CodexInlineHooks reports the hook commands that .codex/config.toml
// defines inline under [hooks]. Codex merges the inline hooks with
// .codex/hooks.json, so a seamark hook in both sources runs twice.
// present is true when the file has a non-empty [hooks] table, also
// when the table holds no command the function can read.
//
// The function reads only. The inline table is the user's, and setup
// manages the one representation in hooks.json.
func CodexInlineHooks(data []byte) (commands []string, present bool, err error) {
	cfg := map[string]any{}

	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, false, fmt.Errorf("%s: %w", CodexConfig, err)
	}

	table, ok := cfg["hooks"].(map[string]any)
	if !ok || len(table) == 0 {
		return nil, false, nil
	}

	commands = collectCommands(table, nil)

	// A map walk has no stable order, and a finding must be reproducible.
	slices.Sort(commands)

	return commands, true, nil
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
