package approve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// ClaudeMCPPlan is the outcome of inspecting .mcp.json before any
// write: what is registered, what the plan registers, and what seamark
// leaves alone.
type ClaudeMCPPlan struct {
	// Exists reports whether the file is present.
	Exists bool
	// Server is the registration name: the one the file already holds, or
	// the one the plan adds. Empty when a conflict prevents both.
	Server string
	// Registered is true when the file already registers seamark under
	// Server. Register is true when the plan adds the registration.
	Registered bool
	Register   bool
	// Conflicts lists the entries left alone, described.
	Conflicts []string
	// doc is the parsed file with the registration added; nil when the
	// plan adds nothing.
	doc map[string]any
}

// PlanClaudeMCP plans the seamark registration in .mcp.json from bytes
// the caller already read. exists is false when the file is absent.
//
// The rule for "this server is seamark" is ClaudeRegistration's: the
// command's base name. A registration under another name is reused,
// because the allow rules are spelled with that name. An entry named
// "seamark" that runs another command is a person's choice, so the plan
// reports it and adds nothing. Malformed JSON and wrong-typed fields
// are errors: setup must stop before its first write.
//
// The added entry uses the bare command name. The file is committed
// and shared, so an absolute path of one machine must not enter it.
func PlanClaudeMCP(data []byte, exists bool) (*ClaudeMCPPlan, error) {
	p := &ClaudeMCPPlan{Exists: exists}
	doc := map[string]any{}

	if exists {
		var err error

		if doc, err = decodeMCPConfig(data); err != nil {
			return nil, err
		}
	}

	servers, err := hooks.ChildMap(doc, "mcpServers")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	commands := map[string]string{}

	for _, name := range slices.Sorted(maps.Keys(servers)) {
		// A null entry and a null command hold nothing. ClaudeRegistration
		// reads both as "no command", so this planner does the same.
		if servers[name] == nil {
			continue
		}

		server, ok := servers[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: mcpServers.%s must be an object", MCPConfig, name)
		}

		if v := server["command"]; v != nil {
			cmd, isString := v.(string)
			if !isString {
				return nil, fmt.Errorf("%s: mcpServers.%s.command must be a string", MCPConfig, name)
			}

			commands[name] = cmd
		}
	}

	if name := seamarkServer(commands); name != "" {
		p.Server, p.Registered = name, true

		return p, nil
	}

	if _, taken := servers[ClaudeServer]; taken {
		if cmd, hasCmd := commands[ClaudeServer]; hasCmd {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcpServers.%s runs another command (%q)", ClaudeServer, cmd))
		} else {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcpServers.%s has no command", ClaudeServer))
		}

		return p, nil
	}

	servers[ClaudeServer] = map[string]any{"command": "seamark", "args": []any{"mcp"}}
	p.Server, p.Register, p.doc = ClaudeServer, true, doc

	return p, nil
}

// decodeMCPConfig parses the file as exactly one JSON object. The
// syntax check is json.Unmarshal, the same call ClaudeRegistration
// makes, so both report a syntax error in the same words, and content
// after the first value is malformed for both: the rewrite would drop
// it. A wrong-typed field is reported by PlanClaudeMCP in its own
// words, which name the field. A top-level null is an empty object, because it holds nothing to
// preserve.
func decodeMCPConfig(data []byte) (map[string]any, error) {
	var probe any

	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	// UseNumber keeps a large integer in a foreign entry exact across the
	// rewrite; a float64 would round it.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	doc := map[string]any{}

	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	if doc == nil {
		doc = map[string]any{}
	}

	return doc, nil
}

// seamarkServer returns the server whose command is the seamark binary.
// The conventional name wins, then the first in name order, so repeated
// runs agree. It returns "" when no server runs seamark.
func seamarkServer(commands map[string]string) string {
	if hooks.IsSeamarkBinary(commands[ClaudeServer]) {
		return ClaudeServer
	}

	for _, name := range slices.Sorted(maps.Keys(commands)) {
		if hooks.IsSeamarkBinary(commands[name]) {
			return name
		}
	}

	return ""
}

// Document returns the complete file as the plan leaves it, or nil when
// the plan adds nothing. Keys are written in name order with two-space
// indentation. HTML escaping is off, so a foreign value that holds "<"
// or "&" keeps its spelling.
func (p *ClaudeMCPPlan) Document() ([]byte, error) {
	if !p.Register {
		return nil, nil
	}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(p.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	return buf.Bytes(), nil
}
