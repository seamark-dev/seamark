package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/skills"
)

// approvalTargets names the clients --approve-tools configures, by one
// rule whether or not --skills is given. Claude Code is a target unless
// --skills=codex. Codex is a target when --skills names it (codex or
// all), or, without an explicit client, when a .codex/ directory exists,
// because that is where its configuration lives; --skills=claude means
// Claude only. A bare --skills detects the skills target by .agents/ and
// the approvals target by .codex/, each by the place its own artifact
// lives; init notes the gap when only one exists. Before this rule,
// --skills --approve-tools skipped a repository with .codex/ and no
// .agents/, right after the documentation promised the one-liner would
// configure it.
func approvalTargets(root, skillsMode string) (claude, codex bool, err error) {
	if skillsMode != "" && !slices.Contains(skills.Modes, skillsMode) {
		return false, false, fmt.Errorf("skills: unknown install mode %q (accepted: %s)",
			skillsMode, strings.Join(skills.Modes, ", "))
	}

	claude = skillsMode != skills.ModeCodex

	switch skillsMode {
	case skills.ModeClaude:
		return claude, false, nil
	case skills.ModeCodex, skills.ModeAll:
		return claude, true, nil
	}

	// A missing .codex/ means no Codex configuration. Any other stat
	// error is reported before init writes anything, because a directory
	// that cannot be read must not be silently treated as absent.
	info, err := os.Stat(filepath.Join(root, ".codex"))
	if errors.Is(err, os.ErrNotExist) {
		return claude, false, nil
	}

	if err != nil {
		return false, false, err
	}

	return claude, info.IsDir(), nil
}

// skillsClients reports which clients the resolved skills targets
// install into, so the approvals note can name the ones this run did
// not approve. The targets are resolved once in runInit.
func skillsClients(targets []skills.Target) (claude, codex bool) {
	for _, t := range targets {
		switch t.Client {
		case skills.ModeClaude:
			claude = true
		case skills.ModeCodex:
			codex = true
		}
	}

	return claude, codex
}

// mergeAllow appends the rules the plan reports missing to
// permissions.allow, in order, and returns the ones it added. Existing
// entries, the user's or ours, stay in place. A rule the plan lists as a
// conflict is never appended: an allow entry cannot override a deny or
// ask entry, so adding one would only claim what is not so. A
// present-but-wrong-typed field is an error, not an overwrite, like the
// hooks merge: init never clobbers the user's data.
func mergeAllow(settings map[string]any, plan *approve.ClaudePlan) (added []string, err error) {
	perms, err := childMap(settings, "permissions")
	if err != nil {
		return nil, err
	}

	allow, err := childSlice(perms, "allow")
	if err != nil {
		return nil, err
	}

	for _, r := range plan.Missing {
		allow = append(allow, r)
		added = append(added, r)
	}

	if len(added) > 0 {
		perms["allow"] = allow
	}

	return added, nil
}

// printApproved narrates the Claude Code allow-rule merge in init's
// vocabulary and lists every rule it added: what a repository
// pre-approves must never require opening settings.json to find out.
// Explicit deny or ask entries are named as kept, like the Codex line
// does, so the user learns why a tool still prompts.
func printApproved(w io.Writer, plan *approve.ClaudePlan, added []string, printOnly bool) {
	kept := approve.KeptSuffix(plan.Conflicts)

	if len(added) == 0 && kept != "" {
		// Nothing to add is not everything approved: the kept entries are
		// exactly the rules that still prompt.
		fmt.Fprintf(w, "  kept    .claude/settings.json permissions (nothing to add%s)\n", kept)

		return
	}

	if len(added) == 0 {
		fmt.Fprintf(w, "  kept    .claude/settings.json permissions (seamark tools and skills already approved)\n")

		return
	}

	verb := "approved"
	if printOnly {
		verb = "would approve"
	}

	fmt.Fprintf(w, "  %s %d Claude Code allow rules in .claude/settings.json (seamark MCP tools + skills%s)\n", verb, len(added), kept)

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
		noteMissingClaude(w, root, settings)
	}

	if codex {
		noteMissingCodex(w, root)
	}
}

func noteMissingClaude(w io.Writer, root string, settings map[string]any) {
	plan, err := planClaude(root, settings)
	if err != nil {
		// A broken .mcp.json stops --approve-tools, not the note: the
		// rules are counted for the conventional name so the user still
		// learns what is missing, and doctor names the broken file.
		if plan, err = approve.PlanClaude(settings, approve.ClaudeServer); err != nil {
			return
		}
	}

	tools, skillRules := 0, 0

	for _, r := range plan.Missing {
		if strings.HasPrefix(r, "Skill(") {
			skillRules++
		} else {
			tools++
		}
	}

	// A rule under permissions.deny or permissions.ask prompts as surely
	// as a missing one, and --approve-tools cannot add it, so the note
	// names the kept entries and says the fix is by hand.
	kept := strings.Join(plan.Conflicts, "; ")

	switch {
	case tools+skillRules > 0 && kept != "":
		fmt.Fprintf(w, "  note    %d seamark allow rules missing from .claude/settings.json (%s); Claude Code can prompt\n"+
			"          for those in manual mode — `seamark init --approve-tools` adds them (additive; --print previews);\n"+
			"          kept explicit settings still prompt: %s\n",
			tools+skillRules, missingKinds(tools, skillRules), kept)
	case tools+skillRules > 0:
		fmt.Fprintf(w, "  note    %d seamark allow rules missing from .claude/settings.json (%s); Claude Code can prompt\n"+
			"          for those in manual mode — `seamark init --approve-tools` adds them (additive; --print previews)\n",
			tools+skillRules, missingKinds(tools, skillRules))
	case kept != "":
		fmt.Fprintf(w, "  note    explicit settings in .claude/settings.json keep %s from being approved (%s);\n"+
			"          Claude Code prompts for those — edit the file by hand if they should run without prompts\n",
			plural(plan.Conflicting(), "seamark rule"), kept)
	}
}

// planClaude reads the server name .mcp.json registers and classifies
// the rules against the settings. The name decides how every tool rule
// is spelled, so init and the note use the same lookup as doctor and
// status.
func planClaude(root string, settings map[string]any) (*approve.ClaudePlan, error) {
	reg, err := approve.ClaudeRegistration(root)
	if err != nil {
		return nil, err
	}

	return approve.PlanClaude(settings, reg.ServerName())
}

func noteMissingCodex(w io.Writer, root string) {
	p, err := approve.PlanCodex(root)
	if err != nil || (!p.Register && len(p.Missing) == 0 && len(p.Conflicts) == 0) {
		return
	}

	// An explicit restrictive setting prompts as surely as a missing
	// approval, and --approve-tools leaves it alone, so the note names
	// it and says the fix is by hand, as the Claude note does.
	if !p.Register && len(p.Missing) == 0 {
		fmt.Fprintf(w, "  note    explicit settings in %s keep seamark tools from being approved (%s);\n"+
			"          Codex prompts for those — edit the file by hand if they should run without prompts\n",
			approve.CodexConfig, strings.Join(p.Conflicts, "; "))

		return
	}

	state := fmt.Sprintf("%d/%d seamark tools approved in %s", len(p.Approved), len(approve.Tools), approve.CodexConfig)
	if p.Register {
		state = "seamark mcp is not registered in " + approve.CodexConfig
	}

	if len(p.Conflicts) > 0 {
		state += "; kept explicit settings still prompt: " + strings.Join(p.Conflicts, "; ")
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
