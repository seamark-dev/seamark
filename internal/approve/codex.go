package approve

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
)

// CodexConfig is the project configuration Codex reads for trusted
// projects; the per-tool approvals and the server registration live in
// it. Repository-relative, slash-separated.
const CodexConfig = ".codex/config.toml"

// CodexServer is the registration name init uses when none exists.
const CodexServer = "seamark"

// approvalModes are the values Codex documents for approval_mode and
// default_tools_approval_mode; anything else fails preflight, because
// Codex refuses the file.
var approvalModes = map[string]bool{"auto": true, "prompt": true, "writes": true, "approve": true}

// CodexPlan is the outcome of inspecting .codex/config.toml before any
// write: what exists, what the appended block adds, and what seamark
// leaves alone.
type CodexPlan struct {
	// Exists reports whether the file is present.
	Exists bool
	// Server is the registration the approvals attach to; empty when a
	// conflict prevents both reuse and registration.
	Server string
	// Registered is true when Server names a registration the file
	// already holds; Register is true when the block adds one. Both are
	// false when the file cannot be extended.
	Registered bool
	Register   bool
	// Missing lists the tools the block approves; Approved the tools
	// already approved, by their own entry or by an approve default.
	Missing  []string
	Approved []string
	// Conflicts lists explicit settings left alone, described.
	Conflicts []string
	// block is the TOML text to append; empty when nothing is added.
	block string
}

// PlanCodex inspects .codex/config.toml without writing. It rejects a
// symlinked path, malformed TOML, and wrong-typed or invalid values on
// the seamark server, because init must fail before its first write
// and Codex would refuse the file. A conflicting registration or an
// explicit restrictive setting, a disabled server or a server-wide
// approval default included, is reported in the plan and left alone,
// because a person put it there. The appended block is validated by
// parsing the would-be file, and inline tables are detected from the
// decoded keys, so a layout that appending cannot extend is reported
// instead of written.
func PlanCodex(root string) (*CodexPlan, error) {
	if link, err := skills.SymlinkIn(root, CodexConfig); err != nil {
		return nil, err
	} else if link != "" {
		return nil, fmt.Errorf("%s: symlink at %s; seamark writes only real paths inside the repository", CodexConfig, link)
	}

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CodexConfig)))

	p := &CodexPlan{Exists: err == nil}

	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", CodexConfig, err)
	}

	cfg := map[string]any{}

	if p.Exists {
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return nil, fmt.Errorf("%s: %s", CodexConfig, render.Sanitize(err.Error()))
		}
	}

	servers, err := serversTable(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", CodexConfig, err)
	}

	// Codex refuses the whole file when any server carries a wrong-typed
	// field, so every server is validated before the registration is
	// chosen; a seamark server with args = "mcp" must fail here, not
	// pass as a foreign registration.
	if err := validateServers(servers); err != nil {
		return nil, fmt.Errorf("%s: %w", CodexConfig, err)
	}

	p.Server = registration(servers)
	p.Registered = p.Server != ""

	switch {
	case p.Registered:
		server, _ := servers[p.Server].(map[string]any)
		p.Approved, p.Missing, p.Conflicts = classifyTools(server)
	case servers[CodexServer] != nil:
		p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcp_servers.%s runs another command", CodexServer))
	default:
		p.Server, p.Register = CodexServer, true
		p.Missing = slices.Clone(Tools)
	}

	if p.Register || len(p.Missing) > 0 {
		// TOML closes an inline table: a [table] header cannot extend it
		// later, and Codex's parser enforces that even where this one
		// does not. Detect the layout from the decoded keys, then parse
		// the would-be file for every other rule.
		switch {
		case inlineDefined(data, p.Server, p.Missing):
			p.Conflicts = append(p.Conflicts, "existing inline tables cannot be extended by appending")
			p.Missing, p.Register = nil, false
		default:
			p.block = renderBlock(p, data)

			if _, err := toml.Decode(string(data)+p.block, &map[string]any{}); err != nil {
				reason, _, _ := strings.Cut(err.Error(), "\n")
				p.Conflicts = append(p.Conflicts, "existing tables cannot be extended by appending ("+render.Sanitize(reason)+")")
				p.Missing, p.Register, p.block = nil, false, ""
			}
		}
	}

	return p, nil
}

// serversTable returns mcp_servers as a table, an empty table when
// absent, and an error when present with another type.
func serversTable(cfg map[string]any) (map[string]any, error) {
	v, ok := cfg["mcp_servers"]
	if !ok {
		return map[string]any{}, nil
	}

	servers, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("mcp_servers is not a table")
	}

	return servers, nil
}

// registration returns the name of the server that runs `seamark mcp`,
// preferring the conventional name when several exist. The basename
// rule is doctor's: exact "seamark", tolerating the Windows suffix.
func registration(servers map[string]any) string {
	if isSeamarkMCP(servers[CodexServer]) {
		return CodexServer
	}

	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if isSeamarkMCP(servers[name]) {
			return name
		}
	}

	return ""
}

func isSeamarkMCP(v any) bool {
	server, ok := v.(map[string]any)
	if !ok {
		return false
	}

	cmd, _ := server["command"].(string)
	if strings.TrimSuffix(filepath.Base(cmd), ".exe") != "seamark" {
		return false
	}

	for _, a := range stringList(server["args"]) {
		if a == "mcp" {
			return true
		}
	}

	return false
}

// validateServers checks every server in name order, so the error
// names the same server on repeat.
func validateServers(servers map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		server, ok := servers[name].(map[string]any)
		if !ok {
			return fmt.Errorf("mcp_servers.%s must be a table", name)
		}

		if err := validateServer(name, server); err != nil {
			return err
		}
	}

	return nil
}

// validateServer checks the types and values Codex requires on the
// fields seamark reads or extends. A wrong type passes a syntax-only
// parse but makes Codex refuse the file, so init must stop here.
func validateServer(name string, server map[string]any) error {
	at := func(field string) string { return fmt.Sprintf("mcp_servers.%s.%s", name, field) }

	if v, ok := server["command"]; ok {
		if _, isString := v.(string); !isString {
			return errors.New(at("command") + " must be a string")
		}
	}

	if v, ok := server["enabled"]; ok {
		if _, isBool := v.(bool); !isBool {
			return errors.New(at("enabled") + " must be a boolean")
		}
	}

	for _, field := range []string{"args", "disabled_tools", "enabled_tools"} {
		if v, ok := server[field]; ok && !isStringArray(v) {
			return errors.New(at(field) + " must be an array of strings")
		}
	}

	if v, ok := server["default_tools_approval_mode"]; ok {
		if mode, isString := v.(string); !isString || !approvalModes[mode] {
			return errors.New(at("default_tools_approval_mode") + " must be one of auto, prompt, writes, approve")
		}
	}

	if v, ok := server["tools"]; ok {
		tools, isTable := v.(map[string]any)
		if !isTable {
			return errors.New(at("tools") + " must be a table")
		}

		for tool, entry := range tools {
			settings, isTable := entry.(map[string]any)
			if !isTable {
				return errors.New(at("tools."+tool) + " must be a table")
			}

			if v, ok := settings["approval_mode"]; ok {
				if mode, isString := v.(string); !isString || !approvalModes[mode] {
					return errors.New(at("tools."+tool+".approval_mode") + " must be one of auto, prompt, writes, approve")
				}
			}
		}
	}

	return nil
}

// classifyTools sorts the five tools into approved, missing, and
// conflicting for one registered server. A disabled server, a server
// default other than approve, a disabled tool, a tool an enabled_tools
// allow list omits, or an approval_mode other than "approve" is an
// explicit choice, reported and kept: a per-tool override would defeat
// it, since Codex lets the override win.
func classifyTools(server map[string]any) (approved, missing, conflicts []string) {
	if enabled, ok := server["enabled"].(bool); ok && !enabled {
		return nil, nil, []string{"enabled = false"}
	}

	tools, _ := server["tools"].(map[string]any)
	disabled := stringList(server["disabled_tools"])
	enabled, hasEnabled := server["enabled_tools"]
	enabledList := stringList(enabled)
	defaultMode, _ := server["default_tools_approval_mode"].(string)

	var governed []string

	for _, t := range Tools {
		entry, _ := tools[t].(map[string]any)
		mode, _ := entry["approval_mode"].(string)

		switch {
		case slices.Contains(disabled, t):
			conflicts = append(conflicts, "disabled_tools lists "+t)
		case hasEnabled && !slices.Contains(enabledList, t):
			conflicts = append(conflicts, "enabled_tools omits "+t)
		case mode == "approve":
			approved = append(approved, t)
		case mode != "":
			conflicts = append(conflicts, fmt.Sprintf("tools.%s.approval_mode = %q", t, mode))
		case defaultMode == "approve":
			approved = append(approved, t)
		case defaultMode != "":
			governed = append(governed, t)
		default:
			missing = append(missing, t)
		}
	}

	if len(governed) > 0 {
		conflicts = append(conflicts, fmt.Sprintf("default_tools_approval_mode = %q governs %s", defaultMode, strings.Join(governed, ", ")))
	}

	return approved, missing, conflicts
}

// renderBlock builds the TOML to append: the registration when missing,
// then one table per missing tool. Per-tool tables, never
// default_tools_approval_mode, so a future tool is not approved by
// accident. The block starts on its own line whatever the file ends with.
func renderBlock(p *CodexPlan, existing []byte) string {
	var b strings.Builder

	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteString("\n")
	}

	if len(existing) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("# seamark: added by `seamark init --approve-tools`; remove these tables to undo.\n")

	key := tomlKey(p.Server)

	if p.Register {
		fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n", key)
	}

	for i, t := range p.Missing {
		if i > 0 || p.Register {
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "[mcp_servers.%s.tools.%s]\napproval_mode = \"approve\"\n", key, t)
	}

	return b.String()
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey quotes a table key that is not a bare key.
func tomlKey(name string) string {
	if bareKey.MatchString(name) {
		return name
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
}

// inlineDefined reports whether the file defines, as an inline table,
// any path the appended block would extend: mcp_servers itself, the
// server, its tools table, or one of the tools about to be added. Keys
// are decoded by the TOML parser itself, so quoted and escaped
// spellings compare by value. The scan tracks [table] headers so dotted
// keys resolve to full paths, tracks multiline strings so a line inside
// one is never read as a header, and keeps the current header when a
// bracketed line is not a valid header, such as a nested array element.
func inlineDefined(data []byte, server string, missing []string) bool {
	closed := map[string]bool{
		keyPath("mcp_servers"):                  true,
		keyPath("mcp_servers", server):          true,
		keyPath("mcp_servers", server, "tools"): true,
	}

	for _, t := range missing {
		closed[keyPath("mcp_servers", server, "tools", t)] = true
	}

	var (
		header []string
		multi  byte // the delimiter of an open multiline string, or 0
	)

	for _, line := range strings.Split(string(data), "\n") {
		if multi != 0 {
			multi = scanStrings(line, multi)

			continue
		}

		t := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(t, "[["):
			header = nil
		case strings.HasPrefix(t, "["):
			if segs, ok := decodeHeader(t); ok {
				header = segs
			}
		default:
			if key, inline := splitInlineAssignment(t); inline {
				if segs, ok := decodeKey(key); ok && closed[keyPath(append(append([]string(nil), header...), segs...)...)] {
					return true
				}
			}
		}

		multi = scanStrings(line, 0)
	}

	return false
}

// scanStrings walks one line and returns the multiline-string delimiter
// still open at its end: 0 for none, otherwise the quote character of
// the open string, double for basic and single for literal. Single-line
// strings and comments are skipped, and a basic multiline string ends
// only at an unescaped closing delimiter.
func scanStrings(line string, multi byte) byte {
	for i := 0; i < len(line); {
		if multi != 0 {
			switch {
			case multi == '"' && line[i] == '\\':
				i += 2
			case strings.HasPrefix(line[i:], string([]byte{multi, multi, multi})):
				multi = 0
				i += 3
			default:
				i++
			}

			continue
		}

		switch {
		case strings.HasPrefix(line[i:], `"""`):
			multi = '"'
			i += 3
		case strings.HasPrefix(line[i:], `'''`):
			multi = '\''
			i += 3
		case line[i] == '"' || line[i] == '\'':
			i = skipString(line, i)
		case line[i] == '#':
			return multi
		default:
			i++
		}
	}

	return multi
}

// skipString returns the index after the single-line string opening at
// line[i], honoring backslash escapes in basic strings.
func skipString(line string, i int) int {
	quote := line[i]

	for j := i + 1; j < len(line); j++ {
		switch {
		case quote == '"' && line[j] == '\\':
			j++
		case line[j] == quote:
			return j + 1
		}
	}

	return len(line)
}

// keyPath joins decoded key segments with a separator no key contains.
func keyPath(segs ...string) string {
	return strings.Join(segs, "\x00")
}

// decodeHeader decodes a "[a.b]" line through the TOML parser and
// returns the table's key segments.
func decodeHeader(line string) ([]string, bool) {
	var m map[string]any

	md, err := toml.Decode(line+"\n", &m)
	if err != nil || len(md.Keys()) == 0 {
		return nil, false
	}

	return []string(md.Keys()[0]), true
}

// decodeKey decodes a dotted key through the TOML parser, so quotes and
// escapes resolve exactly as Codex resolves them.
func decodeKey(raw string) ([]string, bool) {
	var m map[string]any

	md, err := toml.Decode(raw+" = 0\n", &m)
	if err != nil || len(md.Keys()) == 0 {
		return nil, false
	}

	keys := md.Keys()

	return []string(keys[len(keys)-1]), true
}

// splitInlineAssignment finds the first "=" outside quotes and reports
// whether the value starts an inline table. It returns the raw key text
// for decodeKey.
func splitInlineAssignment(line string) (key string, inline bool) {
	var quote byte

	for i := 0; i < len(line); i++ {
		c := line[i]

		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return "", false
		case c == '=':
			rest := strings.TrimSpace(line[i+1:])

			return strings.TrimSpace(line[:i]), strings.HasPrefix(rest, "{")
		}
	}

	return "", false
}

// ApplyCodex appends the planned block and narrates in init's
// vocabulary. Nothing is rewritten: existing bytes, comments included,
// stay where they are. The path is checked for links again right before
// the write, because the plan may be older than the tree.
func ApplyCodex(w io.Writer, root string, p *CodexPlan, printOnly bool) error {
	kept := ""
	if len(p.Conflicts) > 0 {
		kept = "; kept explicit settings: " + strings.Join(p.Conflicts, "; ")
	}

	if p.block == "" {
		if p.Registered {
			fmt.Fprintf(w, "  kept    %s (seamark registered as %q; %d/%d tools approved%s)\n",
				CodexConfig, p.Server, len(p.Approved), len(Tools), kept)
		} else {
			fmt.Fprintf(w, "  kept    %s (seamark not registered%s)\n", CodexConfig, kept)
		}

		return nil
	}

	if !printOnly {
		if link, err := skills.SymlinkIn(root, CodexConfig); err != nil {
			return err
		} else if link != "" {
			return fmt.Errorf("%s: symlink at %s; seamark writes only real paths inside the repository", CodexConfig, link)
		}

		path := filepath.Join(root, filepath.FromSlash(CodexConfig))

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("%s: %w", CodexConfig, err)
		}

		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("%s: %w", CodexConfig, err)
		}

		if _, err := f.WriteString(p.block); err != nil {
			_ = f.Close()

			return fmt.Errorf("%s: %w", CodexConfig, err)
		}

		if err := f.Close(); err != nil {
			return fmt.Errorf("%s: %w", CodexConfig, err)
		}
	}

	verb := "updated"

	switch {
	case printOnly && !p.Exists:
		verb = "would write"
	case printOnly:
		verb = "would update"
	case !p.Exists:
		verb = "wrote  "
	}

	detail := fmt.Sprintf("approved %d tools: %s", len(p.Missing), strings.Join(p.Missing, ", "))
	if p.Register {
		detail = "registered seamark mcp; " + detail
	}

	fmt.Fprintf(w, "  %s %s (%s%s)\n", verb, CodexConfig, detail, kept)

	return nil
}

func inspectCodex(root string) ClientApproval {
	c := ClientApproval{Client: ClientCodex, Path: CodexConfig, Total: len(Tools)}

	p, err := PlanCodex(root)
	if err != nil {
		c.Err = render.Sanitize(err.Error())

		return c
	}

	if p.Registered {
		c.Registered = p.Server
	}

	c.Approved = len(p.Approved)
	c.Conflicts = p.Conflicts

	return c
}

func stringList(v any) []string {
	items, _ := v.([]any)

	var out []string

	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

func isStringArray(v any) bool {
	items, ok := v.([]any)
	if !ok {
		return false
	}

	for _, it := range items {
		if _, ok := it.(string); !ok {
			return false
		}
	}

	return true
}
