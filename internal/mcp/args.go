package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
)

// The caps of the argument note. A frame can hold 16 MB of keys, and the
// reply must not grow with the input of the client.
const (
	maxNamedKeys = 8
	maxKeyRunes  = 64
)

// toolProperties returns the sorted property names of the inputSchema of
// the named tool. toolDefs is the one source of the known keys, so a new
// schema property is known with no second list to update.
func toolProperties(name string) []string {
	for _, def := range toolDefs {
		if def["name"] != name {
			continue
		}

		schema, _ := def["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)

		names := make([]string, 0, len(props))
		for key := range props {
			names = append(names, key)
		}

		slices.Sort(names)

		return names
	}

	return nil
}

// unknownArgsNote returns the call note that names the top-level keys of
// args that the tool ignores, or "". A key is known when it matches a
// property name under strings.EqualFold, because encoding/json binds a
// key with that rule. Arguments that are not an object get no note: the
// runners keep their own handling of that input.
func unknownArgsNote(tool string, args json.RawMessage) string {
	var fields map[string]json.RawMessage

	if err := json.Unmarshal(args, &fields); err != nil || len(fields) == 0 {
		return ""
	}

	props := toolProperties(tool)

	var unknown []string

	for key := range fields {
		if !slices.ContainsFunc(props, func(p string) bool { return strings.EqualFold(p, key) }) {
			unknown = append(unknown, key)
		}
	}

	if len(unknown) == 0 {
		return ""
	}

	slices.Sort(unknown)

	named := unknown[:min(len(unknown), maxNamedKeys)]
	quoted := make([]string, len(named))

	// A key is client text that comes back in the reply, so it loses its
	// control characters first. %q then shows an empty key as "".
	for i, key := range named {
		quoted[i] = strconv.Quote(render.Truncate(render.Sanitize(key), maxKeyRunes))
	}

	list := strings.Join(quoted, ", ")
	if more := len(unknown) - len(named); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}

	takes := tool + " takes no arguments."
	if len(props) > 0 {
		takes = tool + " takes " + strings.Join(props, ", ") + "."
	}

	if len(unknown) == 1 {
		return "note: seamark ignored the unknown argument " + list + " and ran the call without it; " + takes + "\n"
	}

	return "note: seamark ignored the unknown arguments " + list + " and ran the call without them; " + takes + "\n"
}
