// Package approve manages the configuration that lets a coding agent
// call the seamark MCP tools without a permission prompt: Claude Code
// allow rules in .claude/settings.json and Codex per-tool approvals in
// .codex/config.toml. init writes both on --approve-tools; doctor and
// status read them. Both files are project configuration: evidence of
// intent, never a guarantee, because user-level and managed client
// policy still apply on top.
package approve

import (
	"fmt"
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

// ClaudeRules returns the allow rules --approve-tools merges into
// .claude/settings.json: one exact rule per MCP tool and one exact Skill
// rule per shipped skill. Exact names, never Skill(seamark-*): a
// wildcard would pre-approve a skill that does not exist yet. The rules
// do not depend on whether the skills are installed, so an MCP-only
// setup can approve the tools alone; the benchmark's MCP-only arm needs
// exactly that.
func ClaudeRules() ([]string, error) {
	var rules []string

	for _, tool := range Tools {
		rules = append(rules, "mcp__seamark__"+tool)
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

// AllowSet returns the string entries of permissions.allow, reading the
// settings without creating anything.
func AllowSet(settings map[string]any) map[string]bool {
	present := map[string]bool{}

	if perms, ok := settings["permissions"].(map[string]any); ok {
		if allow, ok := perms["allow"].([]any); ok {
			for _, v := range allow {
				if s, ok := v.(string); ok {
					present[s] = true
				}
			}
		}
	}

	return present
}

// ClientApproval summarizes one client's approval configuration for
// init, doctor, and status.
type ClientApproval struct {
	Client string `json:"client"`
	// Path is the repository-relative configuration file.
	Path string `json:"path"`
	// Registered is the Codex server name that runs `seamark mcp`;
	// empty for Claude Code, whose registration lives in .mcp.json.
	Registered string `json:"registered,omitempty"`
	// Approved counts the rules (Claude Code) or tools (Codex) present;
	// Total is what a complete configuration holds.
	Approved int `json:"approved"`
	Total    int `json:"total"`
	// Conflicts lists explicit settings seamark leaves alone, such as a
	// tool set to prompt, a disabled tool, or a server name in use.
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

// State classifies the record.
func (c ClientApproval) State() string {
	switch {
	case c.Err != "":
		return StateUnreadable
	case len(c.Conflicts) > 0:
		return StateConflicting
	case c.Approved == 0 && c.Registered == "":
		return StateNotConfigured
	case c.Approved < c.Total:
		return StatePartial
	default:
		return StateCurrent
	}
}

// Describe renders one client in a few words, for example
// "claude 8/8 rules" or "codex registered as \"seamark\", 5/5 tools
// approved". init, doctor, and status all print it.
func (c ClientApproval) Describe() string {
	if c.Err != "" {
		return fmt.Sprintf("%s unreadable (%s)", c.Client, c.Err)
	}

	var s string

	switch {
	case c.Client == ClientCodex && c.Registered == "" && c.Approved == 0:
		s = c.Client + " not registered"
	case c.Client == ClientCodex:
		s = fmt.Sprintf("%s registered as %q, %d/%d tools approved", c.Client, c.Registered, c.Approved, c.Total)
	case c.Approved == 0:
		s = c.Client + " not configured"
	default:
		s = fmt.Sprintf("%s %d/%d rules", c.Client, c.Approved, c.Total)
	}

	if len(c.Conflicts) > 0 {
		s += fmt.Sprintf(", %d explicit setting(s) kept: %s", len(c.Conflicts), strings.Join(c.Conflicts, "; "))
	}

	return s
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

	rules, err := ClaudeRules()
	if err != nil {
		c.Err = err.Error()

		return c
	}

	c.Total = len(rules)

	settings, err := hooks.ReadSettings(root)
	if err != nil {
		c.Err = render.Sanitize(err.Error())

		return c
	}

	present := AllowSet(settings)

	for _, r := range rules {
		if present[r] {
			c.Approved++
		}
	}

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
