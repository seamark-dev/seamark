package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
)

// legacyNotes prints the client-dependent lines of a run without
// --client. The run addresses the two first clients by the rules it
// always had, so these lines name them, in the words init always used.
type legacyNotes struct {
	reg             *integration.Registry
	skillsRequested bool
}

// showInfo is false: a run without --client keeps its historical output.
func (legacyNotes) showInfo() bool { return false }

// afterSetup prints the skills line, or notes what the skills install
// left unapproved: every client the skills reached that --approve-tools
// did not. With a bare --skills that is Codex when .agents/ exists
// without .codex/, because the two artifacts are detected by different
// directories.
func (n legacyNotes) afterSetup(run initRun, setups []integration.ClientSetup) {
	if !n.skillsRequested {
		reportInstalledSkills(run.w, run.root, n.reg)

		return
	}

	for _, s := range setups {
		if !s.Skills || s.ApproveTools {
			continue
		}

		switch s.ClientID {
		case integration.ClaudeID:
			// The file as it is on disk now. The permissions in it are
			// what the note counts, and no hook merge changes them.
			if settings, err := hooks.ReadSettings(run.root); err == nil {
				noteMissingClaude(run.w, run.root, settings)
			}
		case integration.CodexID:
			noteMissingCodex(run.w, run.root)
		}
	}
}

// selectedNotes prints the client-dependent lines of a run with
// --client. It names no client: every line comes from the registry and
// from each adapter's own inspection.
type selectedNotes struct {
	reg             *integration.Registry
	skillsRequested bool
}

// showInfo is true: an explicit selection reports partial support.
func (selectedNotes) showInfo() bool { return true }

// afterSetup prints the skills line for the selected destinations, or
// notes each selected client whose tools the run left unapproved.
func (n selectedNotes) afterSetup(run initRun, setups []integration.ClientSetup) {
	if !n.skillsRequested {
		reportSelectedSkills(run.w, run.root, n.reg, setups)

		return
	}

	for _, s := range setups {
		client, ok := n.reg.Lookup(s.ClientID)
		if !ok || s.ApproveTools || !client.SetupOps.ApproveTools {
			continue
		}

		for _, entry := range client.Setup.Inspect(run.root).Capabilities {
			if entry.Capability != integration.CapabilityToolGrants || entry.State == integration.StateCurrent {
				continue
			}

			// The detail names the state ("0/8 rules"); the line adds the
			// client, as doctor and status do.
			fmt.Fprintf(run.w, "  note    %s %s; the client can prompt for the seamark tools —\n"+
				"          `seamark init --client %s --approve-tools` adds the approvals (additive; --print previews)\n",
				client.ID, render.Sanitize(entry.Detail), client.ID)
		}
	}
}

// noteMissingClaude prints the note for a Claude Code skills install
// without --approve-tools: what is missing, and that those calls can
// prompt. It does not assert which invocation path prompts: Claude
// Code's docs say a skill's own allowed-tools grant covers user and
// model invocation for one turn, but the 2.1.257 trial saw it apply to
// user invocation only. Persistent rules cover every path, so the note
// is right either way.
func noteMissingClaude(w io.Writer, root string, settings map[string]any) {
	// A broken .mcp.json stops --approve-tools, not the note: the rules
	// are counted for the conventional name, which an unreadable
	// registration falls back to, so the user still learns what is
	// missing; doctor names the broken file.
	reg, _ := approve.ClaudeRegistration(root)

	plan, err := approve.PlanClaude(settings, reg.ServerName())
	if err != nil {
		return
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

// noteMissingCodex prints the note for a Codex skills install without
// --approve-tools. Codex approves MCP tools only through its own
// configuration, so a missing approval always prompts.
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
