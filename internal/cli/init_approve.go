package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/mcp"
	"github.com/seamark-dev/seamark/internal/skills"
)

// approveRules returns the Claude Code allow rules --approve-tools merges:
// one exact rule per seamark MCP tool and one exact Skill rule per shipped
// skill. Exact names, never Skill(seamark-*): a wildcard would pre-approve
// a skill that does not exist yet. The rules do not depend on whether the
// skills are installed, so an MCP-only setup can approve the tools alone;
// the benchmark's MCP-only arm needs exactly that.
func approveRules() ([]string, error) {
	var rules []string

	for _, tool := range mcp.ToolNames() {
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

	present := map[string]bool{}

	for _, v := range allow {
		if s, ok := v.(string); ok {
			present[s] = true
		}
	}

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

// missingAllow reports which rules permissions.allow lacks, reading the
// settings without creating anything: the caller only narrates.
func missingAllow(settings map[string]any, rules []string) []string {
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

	var missing []string

	for _, r := range rules {
		if !present[r] {
			missing = append(missing, r)
		}
	}

	return missing
}

// printApproved narrates the allow-rule merge in init's vocabulary and
// lists every rule it added: what a repository pre-approves must never
// require opening settings.json to find out.
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

// noteMissingApproval prints one note when skills were installed but
// allow rules --approve-tools writes are missing. It names what is
// missing, by kind, and says those calls can prompt. It does not assert
// which invocation path prompts: Claude Code's docs say a skill's own
// allowed-tools grant covers user and model invocation for one turn, but
// the 2.1.257 trial saw it apply to user invocation only. Persistent
// rules cover both, so the note is right either way.
func noteMissingApproval(w io.Writer, settings map[string]any) {
	rules, err := approveRules()
	if err != nil {
		return
	}

	missing := missingAllow(settings, rules)
	if len(missing) == 0 {
		return
	}

	tools, skillRules := 0, 0

	for _, r := range missing {
		if strings.HasPrefix(r, "Skill(") {
			skillRules++
		} else {
			tools++
		}
	}

	fmt.Fprintf(w, "  note    %d seamark allow rules missing from .claude/settings.json (%s); Claude Code can prompt\n"+
		"          for those in manual mode — `seamark init --approve-tools` adds them (additive; --print previews)\n",
		len(missing), missingKinds(tools, skillRules))
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
