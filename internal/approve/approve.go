// Package approve manages the configuration that lets a coding agent
// call the seamark MCP tools without a permission prompt: Claude Code
// allow rules in .claude/settings.json and Codex per-tool approvals in
// .codex/config.toml. init writes both on --approve-tools; doctor and
// status read them. Both files are project configuration: evidence of
// intent, never a guarantee, because user-level and managed client
// policy still apply on top.
package approve

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Tools is the seamark MCP tool surface, in definition order. The list
// lives here rather than in internal/mcp because status imports this
// package and mcp imports status; a test in internal/mcp pins the two
// lists to each other.
var Tools = []string{"orient", "why", "change_set", "check", "expand"}

// Client names, as narrated.
const (
	ClientClaude = "claude"
	ClientCodex  = "codex"
)

// ClaudeSettings is the Claude Code project file that carries the rules.
const ClaudeSettings = ".claude/settings.json"

// MCPConfig is the Claude Code project file that registers MCP servers.
// The server name in it is the prefix of every tool rule, so the rules
// must be derived from it, never assumed.
const MCPConfig = ".mcp.json"

// ClaudeServer is the server name the rules use when .mcp.json does not
// register seamark: the name `claude mcp add seamark` gives it.
const ClaudeServer = "seamark"

// ClaudeRules returns the rules for the conventional server name. The
// benchmark and the tests use it; init, doctor, and status derive the
// name from .mcp.json through ClaudeRulesFor.
func ClaudeRules() ([]string, error) {
	return ClaudeRulesFor(ClaudeServer)
}

// ClaudeRulesFor returns the allow rules --approve-tools merges into
// .claude/settings.json for one server name: one exact rule per MCP
// tool and one exact Skill rule per shipped skill. Exact names, never
// Skill(seamark-*): a wildcard would pre-approve a skill that does not
// exist yet. The rules do not depend on whether the skills are
// installed, so an MCP-only setup can approve the tools alone; the
// benchmark's MCP-only arm needs exactly that.
func ClaudeRulesFor(server string) ([]string, error) {
	var rules []string

	for _, tool := range Tools {
		rules = append(rules, ToolRule(server, tool))
	}

	names, err := skills.Names()
	if err != nil {
		return nil, err
	}

	for _, name := range names {
		rules = append(rules, "Skill("+name+")")
	}

	return rules, nil
}

// ToolRule spells the Claude Code permission rule for one MCP tool.
func ToolRule(server, tool string) string {
	return ServerRule(server) + "__" + tool
}

// ServerRule spells the Claude Code rule that covers every tool of one
// server. Claude Code documents it beside the per-tool form.
func ServerRule(server string) string {
	return "mcp__" + server
}

// Registration describes what .mcp.json says about seamark.
type Registration struct {
	// Exists reports whether the file is present.
	Exists bool
	// Server is the name whose command is seamark; empty when none.
	Server string
}

// ServerName returns the name the tool rules must use: the registered
// one, or the conventional name when nothing is registered yet.
func (r Registration) ServerName() string {
	if r.Server != "" {
		return r.Server
	}

	return ClaudeServer
}

// ClaudeRegistration reads .mcp.json and finds the seamark server by the
// same basename rule doctor and hooks use: exact "seamark", tolerating
// the Windows suffix. Several matches prefer the conventional name, then
// the first in name order, so repeated runs agree. A missing file is not
// an error; an unparseable one is, because a rule prefix guessed from a
// broken file would be reported as current while every call prompts.
func ClaudeRegistration(root string) (Registration, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(MCPConfig)))
	if errors.Is(err, os.ErrNotExist) {
		return Registration{}, nil
	}

	if err != nil {
		return Registration{}, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	var cfg struct {
		Servers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Registration{Exists: true}, fmt.Errorf("%s: %w", MCPConfig, err)
	}

	reg := Registration{Exists: true}

	isSeamark := func(name string) bool {
		srv, ok := cfg.Servers[name]

		return ok && strings.TrimSuffix(filepath.Base(srv.Command), ".exe") == "seamark"
	}

	if isSeamark(ClaudeServer) {
		reg.Server = ClaudeServer

		return reg, nil
	}

	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		if isSeamark(name) {
			reg.Server = name

			break
		}
	}

	return reg, nil
}

// AllowSet returns the string entries of permissions.allow, reading the
// settings without creating anything.
func AllowSet(settings map[string]any) map[string]bool {
	return permissionSet(settings, "allow")
}

// permissionSet returns the string entries of one permissions list.
func permissionSet(settings map[string]any, list string) map[string]bool {
	present := map[string]bool{}

	if perms, ok := settings["permissions"].(map[string]any); ok {
		if entries, ok := perms[list].([]any); ok {
			for _, v := range entries {
				if s, ok := v.(string); ok {
					present[s] = true
				}
			}
		}
	}

	return present
}

// ClaudePlan is the outcome of inspecting the permissions in
// .claude/settings.json for one server name: what is approved, what
// --approve-tools adds, and what it leaves alone.
type ClaudePlan struct {
	// Server is the name the tool rules are spelled with.
	Server string
	// Rules is the complete rule set for the server.
	Rules []string
	// Approved lists the rules permissions.allow covers; Missing the
	// rules the merge appends; Conflicts the explicit deny or ask
	// entries that keep a rule from working, described.
	Approved  []string
	Missing   []string
	Conflicts []string
}

// Conflicting counts the rules the conflicts keep from working. It is
// not len(Conflicts): one server-wide deny entry covers five tool rules.
func (p *ClaudePlan) Conflicting() int {
	return len(p.Rules) - len(p.Approved) - len(p.Missing)
}

// PlanClaude classifies every rule against the three permission lists.
// Claude Code applies deny, then ask, then allow. A rule in deny or ask
// is therefore a conflict however allow reads. Appending an allow rule
// for it would change nothing, and counting it as approved would hide a
// prompt. The server-wide rule counts for every tool of the server, in
// each list. An entry that covers several tools is reported once.
func PlanClaude(settings map[string]any, server string) (*ClaudePlan, error) {
	rules, err := ClaudeRulesFor(server)
	if err != nil {
		return nil, err
	}

	p := &ClaudePlan{Server: server, Rules: rules}

	lists := []struct {
		name    string
		entries map[string]bool
	}{
		{"deny", permissionSet(settings, "deny")},
		{"ask", permissionSet(settings, "ask")},
	}

	allow := permissionSet(settings, "allow")
	reported := map[string]bool{}

	for _, rule := range rules {
		conflict := false

		for _, list := range lists {
			entry, ok := covering(list.entries, rule, server, true)
			if !ok {
				continue
			}

			conflict = true

			if note := fmt.Sprintf("permissions.%s lists %s", list.name, entry); !reported[note] {
				reported[note] = true
				p.Conflicts = append(p.Conflicts, note)
			}

			break
		}

		if conflict {
			continue
		}

		if _, ok := covering(allow, rule, server, false); ok {
			p.Approved = append(p.Approved, rule)
		} else {
			p.Missing = append(p.Missing, rule)
		}
	}

	return p, nil
}

// covering returns the entry of one permission list that covers the
// rule: the rule itself, the server-wide rule for a tool rule, or a
// wildcard entry the rule matches. The lists differ on wildcards. A
// deny or ask entry is read as widely as a glob allows, because an
// over-read restriction only costs a note while a missed one hides a
// prompt. An allow entry counts only in the form Claude Code accepts, a
// tool prefix anchored under the server such as mcp__seamark__ch*;
// an unanchored pattern such as * or mcp__* is ignored by Claude Code,
// so counting it would skip the exact rules init should add.
func covering(entries map[string]bool, rule, server string, restrictive bool) (string, bool) {
	if entries[rule] {
		return rule, true
	}

	toolPrefix := ServerRule(server) + "__"

	if wide := ServerRule(server); strings.HasPrefix(rule, toolPrefix) && entries[wide] {
		return wide, true
	}

	for _, entry := range slices.Sorted(maps.Keys(entries)) {
		if !strings.Contains(entry, "*") {
			continue
		}

		if !restrictive && !anchoredToolGlob(entry, toolPrefix) {
			continue
		}

		// Rule names carry no slash, so path.Match is a plain glob here.
		if ok, err := path.Match(entry, rule); err == nil && ok {
			return entry, true
		}
	}

	return "", false
}

// anchoredToolGlob reports whether an allow entry is a tool prefix under
// the server with one trailing *, the one wildcard form Claude Code
// honours in an allow rule.
func anchoredToolGlob(entry, toolPrefix string) bool {
	body, ok := strings.CutSuffix(entry, "*")

	return ok && strings.HasPrefix(body, toolPrefix) && !strings.Contains(body, "*")
}

// ClientApproval summarizes one client's approval configuration for
// init, doctor, and status.
type ClientApproval struct {
	Client string `json:"client"`
	// Path is the repository-relative configuration file.
	Path string `json:"path"`
	// Registered is the server name that runs `seamark mcp`: the
	// .codex/config.toml table for Codex, the .mcp.json entry for Claude
	// Code. Empty when nothing is registered.
	Registered string `json:"registered,omitempty"`
	// Approved counts the rules (Claude Code) or tools (Codex) present;
	// Total is what a complete configuration holds.
	Approved int `json:"approved"`
	Total    int `json:"total"`
	// Conflicts lists explicit settings seamark leaves alone, such as a
	// tool set to prompt, a denied rule, or a server name in use.
	Conflicts []string `json:"conflicts,omitempty"`
	// Err reports a file that cannot be read or parsed.
	Err string `json:"error,omitempty"`
}

// Approval states, from nothing to done. Conflicting and unreadable
// need a person; partial and not configured need --approve-tools.
const (
	StateUnreadable    = "unreadable"
	StateConflicting   = "conflicting"
	StateNotConfigured = "not configured"
	StatePartial       = "partial"
	StateCurrent       = "current"
)

// State classifies the record by one rule for both clients. A client
// with no registration and no approvals is not configured, which is
// information. A registered client with fewer approvals than tools is
// partial, zero included: a registration alone approves nothing, so
// every call prompts and the re-run hint is due. Codex approvals without
// a registration are partial too, because the re-run adds the missing
// registration.
func (c ClientApproval) State() string {
	switch {
	case c.Err != "":
		return StateUnreadable
	case len(c.Conflicts) > 0:
		return StateConflicting
	case c.Approved == 0 && c.Registered == "":
		return StateNotConfigured
	case c.Approved < c.Total, c.Client == ClientCodex && c.Registered == "":
		return StatePartial
	default:
		return StateCurrent
	}
}

// Describe renders one client in a few words, for example
// "claude 8/8 rules" or "codex registered as \"seamark\", 5/5 tools
// approved". init, doctor, and status all print it. A Claude Code
// server under a name other than the conventional one is named, because
// the rules are spelled with it. "not configured" is reserved for a
// client with no registration and no rules, so the words agree with
// State: a registered client with no rules is partial and reads
// "claude 0/8 rules", the same way Codex reads "0/5 tools approved".
func (c ClientApproval) Describe() string {
	if c.Err != "" {
		return fmt.Sprintf("%s unreadable (%s)", c.Client, c.Err)
	}

	var s string

	switch {
	case c.Client == ClientCodex && c.Registered == "" && c.Approved == 0:
		s = c.Client + " not registered"
	case c.Client == ClientCodex && c.Registered == "":
		s = fmt.Sprintf("%s not registered, %d/%d tools approved", c.Client, c.Approved, c.Total)
	case c.Client == ClientCodex:
		s = fmt.Sprintf("%s registered as %q, %d/%d tools approved", c.Client, c.Registered, c.Approved, c.Total)
	case c.Approved == 0 && c.Registered == "":
		s = c.Client + " not configured"
	default:
		s = fmt.Sprintf("%s %d/%d rules", c.Client, c.Approved, c.Total)
	}

	if c.Client == ClientClaude && c.Registered != "" && c.Registered != ClaudeServer {
		s += fmt.Sprintf(" for server %q", c.Registered)
	}

	if len(c.Conflicts) > 0 {
		s += fmt.Sprintf(", %d explicit setting(s) kept: %s", len(c.Conflicts), strings.Join(c.Conflicts, "; "))
	}

	return s
}

// KeptSuffix renders the conflicts for an init line, or "" when there
// are none. init's Claude and Codex lines share it, so the two read alike.
func KeptSuffix(conflicts []string) string {
	if len(conflicts) == 0 {
		return ""
	}

	return "; kept explicit settings: " + strings.Join(conflicts, "; ")
}

// Inspect reports both clients regardless of detection, so a partial
// configuration in a directory auto would skip stays visible. Errors
// are recorded on the client, never returned: status must describe a
// broken setup, not fail on it.
func Inspect(root string) []ClientApproval {
	return []ClientApproval{inspectClaude(root), inspectCodex(root)}
}

func inspectClaude(root string) ClientApproval {
	c := ClientApproval{Client: ClientClaude, Path: ClaudeSettings}

	// A broken .mcp.json is this record's fault to report: the server
	// name in it spells every rule, and status has no other check that
	// reads the file. Counting under a guessed name would say current
	// while every call prompts.
	reg, err := ClaudeRegistration(root)
	if err != nil {
		c.Err = render.Sanitize(err.Error())

		return c
	}

	c.Registered = reg.Server

	settings, err := hooks.ReadSettings(root)
	if err != nil {
		c.Err = render.Sanitize(err.Error())

		return c
	}

	p, err := PlanClaude(settings, reg.ServerName())
	if err != nil {
		c.Err = err.Error()

		return c
	}

	c.Total = len(p.Rules)
	c.Approved = len(p.Approved)
	c.Conflicts = p.Conflicts

	return c
}

// Summary renders the one-line view status prints, for example
// "claude 8/8 rules · codex not registered". A partial configuration
// names the corrective command once at the end.
func Summary(states []ClientApproval) string {
	var (
		parts   []string
		partial bool
	)

	for _, s := range states {
		parts = append(parts, s.Describe())
		partial = partial || s.State() == StatePartial
	}

	line := strings.Join(parts, " · ")

	if partial {
		line += " (re-run seamark init --approve-tools)"
	}

	return line
}
