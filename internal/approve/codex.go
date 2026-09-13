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

	"github.com/seamark-dev/seamark/internal/hooks"
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
	// block is the TOML text to append; empty when nothing is appended.
	block string
	// insert is the TOML text placed at insertAt, right after an existing
	// [mcp_servers.<server>] header whose table lacks a command. The
	// registration keys then land in the table they belong to; a second
	// header for the same table would be invalid TOML. insertKeys lists
	// the keys the table lacks, because a key it already holds must not
	// be written twice. Empty when nothing is inserted.
	insert     string
	insertAt   int
	insertKeys []string
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
		server, _ := servers[CodexServer].(map[string]any)
		planNamedTable(p, server, data)
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
			p.insert, p.block = renderBlock(p, data)

			if _, err := toml.Decode(string(p.wouldBe(data)), &map[string]any{}); err != nil {
				reason, _, _ := strings.Cut(err.Error(), "\n")
				p.Conflicts = append(p.Conflicts, "existing tables cannot be extended by appending ("+render.Sanitize(reason)+")")
				p.Missing, p.Register, p.block, p.insert = nil, false, "", ""
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

// planNamedTable decides what to do with a [mcp_servers.seamark] table
// that does not run `seamark mcp`. A table without a command is what
// "remove these tables to undo" leaves behind, or a half-written
// registration. The plan completes it: the two keys go under the
// existing header, or the header is appended when only sub-tables
// define the table. Leftover per-tool tables count as approvals. Any
// other command, an HTTP transport, and any explicit setting such as
// enabled = false, is a person's choice: it is reported with its value
// and nothing is written.
func planNamedTable(p *CodexPlan, server map[string]any, data []byte) {
	cmd, hasCmd := server["command"].(string)
	args, hasArgs := server["args"]
	url, hasURL := server["url"]

	switch {
	case hasCmd:
		p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcp_servers.%s runs another command (%q)", CodexServer, cmd))

		return
	case hasURL:
		// An HTTP transport has no command by design. Inserting one would
		// make Codex refuse the file: url is not supported for stdio.
		p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcp_servers.%s uses an HTTP transport (url = %v)", CodexServer, url))

		return
	case hasArgs && !slices.Contains(stringList(args), "mcp"):
		p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcp_servers.%s has no command and its args do not run mcp", CodexServer))

		return
	}

	p.Approved, p.Missing, p.Conflicts = classifyTools(server)
	if len(p.Conflicts) > 0 {
		// A parked server (enabled = false) or a restricted tool set is
		// deliberate; registering it would switch it back on behind the
		// user's back.
		p.Missing = nil

		return
	}

	// With a header the keys are inserted under it. With sub-table
	// headers only, the table header is appended; TOML allows a super
	// table after its sub-tables. A table defined by dotted keys or an
	// inline table cannot take a header later; this decoder accepts that
	// layout, Codex's does not, so it is reported and kept.
	switch at, ok := headerEnd(data, CodexServer); {
	case ok:
		p.Server, p.Register, p.insertAt = CodexServer, true, at
		p.insertKeys = []string{`command = "seamark"`}

		if !hasArgs {
			p.insertKeys = append(p.insertKeys, `args = ["mcp"]`)
		}
	case subTableDefined(data, CodexServer):
		p.Server, p.Register = CodexServer, true
	default:
		p.Missing = nil
		p.Conflicts = append(p.Conflicts, fmt.Sprintf("mcp_servers.%s is defined without a table header", CodexServer))
	}
}

// subTableDefined reports whether a [mcp_servers.<server>.<...>] header
// defines the server table from below, with no header of its own.
func subTableDefined(data []byte, server string) bool {
	prefix := keyPath("mcp_servers", server) + "\x00"

	for _, line := range scanTOML(data) {
		if line.header != nil && strings.HasPrefix(keyPath(line.header...), prefix) {
			return true
		}
	}

	return false
}

// wouldBe returns the file as the plan would leave it: the insert spliced
// in at its offset, then the appended block.
func (p *CodexPlan) wouldBe(existing []byte) []byte {
	out := make([]byte, 0, len(existing)+len(p.insert)+len(p.block))
	out = append(out, existing[:p.insertAt]...)
	out = append(out, p.insert...)
	out = append(out, existing[p.insertAt:]...)

	return append(out, p.block...)
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
	if !hooks.IsSeamarkBinary(cmd) {
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

		// Name order, so a file with two faults names the same one on
		// every run and the message can be tested.
		for _, tool := range slices.Sorted(maps.Keys(tools)) {
			settings, isTable := tools[tool].(map[string]any)
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

// renderBlock builds the TOML the plan adds: the registration when
// missing, then one table per missing tool. Per-tool tables, never
// default_tools_approval_mode, so a future tool is not approved by
// accident. A new registration is a header appended with the tools; a
// registration completing an existing header-only table is two keys
// inserted under that header, because TOML forbids a second header for
// the same table. The appended block starts on its own line whatever the
// file ends with.
func renderBlock(p *CodexPlan, existing []byte) (insert, block string) {
	key := tomlKey(p.Server)

	if p.Register && p.insertAt > 0 {
		insert = strings.Join(p.insertKeys, "\n") + "\n"

		// The header line lacks its newline only when the file ends with
		// it; the keys then need one to start their own line.
		if existing[p.insertAt-1] != '\n' {
			insert = "\n" + insert
		}
	}

	appendsHeader := p.Register && insert == ""

	if len(p.Missing) == 0 && !appendsHeader {
		return insert, ""
	}

	var b strings.Builder

	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteString("\n")
	}

	if len(existing) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("# seamark: added by `seamark init --approve-tools`; remove these tables to undo.\n")

	if appendsHeader {
		fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n", key)
	}

	for i, t := range p.Missing {
		if i > 0 || appendsHeader {
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "[mcp_servers.%s.tools.%s]\napproval_mode = \"approve\"\n", key, t)
	}

	return insert, b.String()
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey quotes a table key that is not a bare key.
func tomlKey(name string) string {
	if bareKey.MatchString(name) {
		return name
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
}

// tomlLine is one physical line of the file as the layout scan reads
// it. The scan is not a TOML parser. It only needs to know where a
// [table] header starts and which lines sit inside a value that spans
// lines. A header-shaped line inside a multiline string or a nested
// array is then never mistaken for a header. The decoder validates the
// rest.
type tomlLine struct {
	text string
	// end is the byte offset right after the line, its newline included.
	end int
	// inValue is true when the line starts inside a multiline string or
	// an open array, where nothing on it can be a header or a key.
	inValue bool
	// header holds the decoded key segments when the line is a [table]
	// header; arrayHeader marks a [[table]] line, whose keys the scan does
	// not resolve.
	header      []string
	arrayHeader bool
}

// scanState carries what the layout scan knows between lines: the
// delimiter of an open multiline string, or 0, and how many arrays are
// open. Both can only be closed by later lines.
type scanState struct {
	multi byte
	depth int
}

// scanTOML splits the file into lines and marks headers and value
// continuations. Header lines are decoded through the TOML parser, so
// quoted and escaped spellings compare by value.
func scanTOML(data []byte) []tomlLine {
	var (
		lines []tomlLine
		st    scanState
		pos   int
	)

	for _, raw := range strings.SplitAfter(string(data), "\n") {
		if raw == "" {
			break
		}

		pos += len(raw)
		line := tomlLine{text: strings.TrimSuffix(raw, "\n"), end: pos, inValue: st.multi != 0 || st.depth > 0}

		if !line.inValue {
			t := strings.TrimSpace(line.text)

			switch {
			case strings.HasPrefix(t, "[["):
				line.arrayHeader = true
			case strings.HasPrefix(t, "["):
				line.header, _ = decodeHeader(t)
			}
		}

		st = scanLine(line.text, st)
		lines = append(lines, line)
	}

	return lines
}

// inlineDefined reports whether the file defines, as an inline table,
// any path the appended block would extend: mcp_servers itself, the
// server, its tools table, or one of the tools about to be added. Keys
// are decoded by the TOML parser itself, so quoted and escaped
// spellings compare by value. Dotted keys resolve under the last [table]
// header; a [[table]] header resets it, because the scan does not follow
// array-of-table paths.
func inlineDefined(data []byte, server string, missing []string) bool {
	closed := map[string]bool{
		keyPath("mcp_servers"):                  true,
		keyPath("mcp_servers", server):          true,
		keyPath("mcp_servers", server, "tools"): true,
	}

	for _, t := range missing {
		closed[keyPath("mcp_servers", server, "tools", t)] = true
	}

	var header []string

	for _, line := range scanTOML(data) {
		switch {
		case line.inValue:
		case line.arrayHeader:
			header = nil
		case line.header != nil:
			header = line.header
		default:
			if key, inline := splitInlineAssignment(strings.TrimSpace(line.text)); inline {
				if segs, ok := decodeKey(key); ok && closed[keyPath(append(append([]string(nil), header...), segs...)...)] {
					return true
				}
			}
		}
	}

	return false
}

// headerEnd returns the byte offset right after the [mcp_servers.<server>]
// header line, or false when no header line defines that table.
func headerEnd(data []byte, server string) (int, bool) {
	want := keyPath("mcp_servers", server)

	for _, line := range scanTOML(data) {
		if line.header != nil && keyPath(line.header...) == want {
			return line.end, true
		}
	}

	return 0, false
}

// scanLine walks one line and returns the state at its end: the
// multiline-string delimiter still open (double for basic, single for
// literal) and the array depth. Single-line strings and comments are
// skipped, a basic multiline string ends only at an unescaped closing
// delimiter, and brackets count only outside strings and comments.
// TOML lets one or two content quotes sit right before the closing
// delimiter, so """foo"""" is one string that ends with the last three
// quotes. The scan consumes those extra quotes with the delimiter. A
// leftover quote would open a false single-line string, hide the
// array's closing bracket, and keep every later line marked as a value.
func scanLine(line string, st scanState) scanState {
	for i := 0; i < len(line); {
		if st.multi != 0 {
			switch {
			case st.multi == '"' && line[i] == '\\':
				i += 2
			case strings.HasPrefix(line[i:], string([]byte{st.multi, st.multi, st.multi})):
				i += 3

				for extra := 0; extra < 2 && i < len(line) && line[i] == st.multi; extra++ {
					i++
				}

				st.multi = 0
			default:
				i++
			}

			continue
		}

		switch {
		case strings.HasPrefix(line[i:], `"""`):
			st.multi = '"'
			i += 3
		case strings.HasPrefix(line[i:], `'''`):
			st.multi = '\''
			i += 3
		case line[i] == '"' || line[i] == '\'':
			i = skipString(line, i)
		case line[i] == '#':
			return st
		case line[i] == '[':
			st.depth++
			i++
		case line[i] == ']':
			if st.depth > 0 {
				st.depth--
			}

			i++
		default:
			i++
		}
	}

	return st
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

// ApplyCodex writes the planned TOML and narrates in init's vocabulary.
// Every existing byte, comments included, stays where it is: the block
// is appended, and the two registration keys that complete a header-only
// table are inserted under that header. The path is checked for links
// again right before the write, because the plan may be older than the
// tree.
func ApplyCodex(w io.Writer, root string, p *CodexPlan, printOnly bool) error {
	kept := KeptSuffix(p.Conflicts)

	if p.block == "" && p.insert == "" {
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

		if err := writeCodexPlan(path, p); err != nil {
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
		verb = "wrote"
	}

	var parts []string

	if p.Register {
		parts = append(parts, "registered seamark mcp")
	}

	if len(p.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("approved %d tools: %s", len(p.Missing), strings.Join(p.Missing, ", ")))
	}

	detail := strings.Join(parts, "; ")

	fmt.Fprintf(w, "  %-7s %s (%s%s)\n", verb, CodexConfig, detail, kept)

	return nil
}

// writeCodexPlan appends the block, or rewrites the file with the insert
// spliced in when the plan completes an existing table. The rewrite
// re-reads the file and checks that the header still ends at the planned
// offset: a file edited since the plan would otherwise take the keys in
// the wrong table, or out of bounds.
func writeCodexPlan(path string, p *CodexPlan) error {
	if p.insert == "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}

		if _, err := f.WriteString(p.block); err != nil {
			_ = f.Close()

			return err
		}

		return f.Close()
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if at, ok := headerEnd(existing, p.Server); !ok || at != p.insertAt {
		return errors.New("changed since it was planned; re-run seamark init --approve-tools")
	}

	return os.WriteFile(path, p.wouldBe(existing), 0o644)
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
