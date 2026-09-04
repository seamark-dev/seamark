package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/skills"
)

// approvalTargets names the clients --approve-tools configures. With
// --skills the set follows the skills mode, so `--skills=codex
// --approve-tools` touches Codex only. Without it, Claude Code always
// and Codex when a .codex/ directory exists, because that is where its
// configuration lives; skills detect Codex by .agents/ instead, since
// the two artifacts live in different places.
func approvalTargets(root, skillsMode string) (claude, codex bool, err error) {
	if skillsMode != "" {
		targets, err := skills.Targets(root, skillsMode)
		if err != nil {
			return false, false, err
		}

		for _, t := range targets {
			switch t.Client {
			case skills.ModeClaude:
				claude = true
			case skills.ModeCodex:
				codex = true
			}
		}

		return claude, codex, nil
	}

	info, err := os.Stat(filepath.Join(root, ".codex"))

	return true, err == nil && info.IsDir(), nil
}

// mergeAllow appends the rules missing from permissions.allow, in order,
// and returns the ones it added. Existing entries, the user's or ours,
// stay in place. A present-but-wrong-typed field is an error, not an
// overwrite, like the hooks merge: init never clobbers the user's data.
func mergeAllow(settings map[string]any, rules []string) (added []string, err error) {
	perms, err := childMap(settings, "permissions")
	if err != nil {
		return nil, err
	}

	allow, err := childSlice(perms, "allow")
	if err != nil {
		return nil, err
	}

	present := approve.AllowSet(settings)

	for _, r := range rules {
		if !present[r] {
			allow = append(allow, r)
			added = append(added, r)
		}
	}

	if len(added) > 0 {
		perms["allow"] = allow
	}

	return added, nil
}

// printApproved narrates the Claude Code allow-rule merge in init's
// vocabulary and lists every rule it added: what a repository
// pre-approves must never require opening settings.json to find out.
func printApproved(w io.Writer, added []string, printOnly bool) {
	if len(added) == 0 {
		fmt.Fprintf(w, "  kept    .claude/settings.json permissions (seamark tools and skills already approved)\n")

		return
	}

	verb := "approved"
	if printOnly {
		verb = "would approve"
	}

	fmt.Fprintf(w, "  %s %d Claude Code allow rules in .claude/settings.json (seamark MCP tools + skills)\n", verb, len(added))

	for _, r := range added {
		fmt.Fprintf(w, "          %s\n", r)
	}
}

// noteMissingApproval prints one note per targeted client whose
// approvals are missing after a skills install without --approve-tools.
// It names what is missing and says those calls can prompt. It does not
// assert which invocation path prompts: Claude Code's docs say a skill's
// own allowed-tools grant covers user and model invocation for one turn,
// but the 2.1.257 trial saw it apply to user invocation only, and Codex
// approves MCP tools only through its own configuration. Persistent
// rules cover every path, so the note is right either way.
func noteMissingApproval(w io.Writer, root string, settings map[string]any, claude, codex bool) {
	if claude {
		noteMissingClaude(w, settings)
	}

	if codex {
		noteMissingCodex(w, root)
	}
}

func noteMissingClaude(w io.Writer, settings map[string]any) {
	rules, err := approve.ClaudeRules()
	if err != nil {
		return
	}

	present := approve.AllowSet(settings)
	tools, skillRules := 0, 0

	for _, r := range rules {
		switch {
		case present[r]:
		case strings.HasPrefix(r, "Skill("):
			skillRules++
		default:
			tools++
		}
	}

	if tools+skillRules == 0 {
		return
	}

	fmt.Fprintf(w, "  note    %d seamark allow rules missing from .claude/settings.json (%s); Claude Code can prompt\n"+
		"          for those in manual mode — `seamark init --approve-tools` adds them (additive; --print previews)\n",
		tools+skillRules, missingKinds(tools, skillRules))
}

func noteMissingCodex(w io.Writer, root string) {
	p, err := approve.PlanCodex(root)
	if err != nil || (!p.Register && len(p.Missing) == 0) {
		return
	}

	state := fmt.Sprintf("%d/%d seamark tools approved in %s", len(p.Approved), len(approve.Tools), approve.CodexConfig)
	if p.Register {
		state = "seamark mcp is not registered in " + approve.CodexConfig
	}

	fmt.Fprintf(w, "  note    %s; Codex prompts for the rest —\n"+
		"          `seamark init --skills=codex --approve-tools` registers the server and approves the five tools\n", state)
}

// missingKinds renders the missing rules by kind, for example
// "5 MCP tools, 3 skills" or "3 skills".
func missingKinds(tools, skillRules int) string {
	var parts []string

	if tools > 0 {
		parts = append(parts, plural(tools, "MCP tool"))
	}

	if skillRules > 0 {
		parts = append(parts, plural(skillRules, "skill"))
	}

	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}

	return fmt.Sprintf("%d %ss", n, noun)
}
