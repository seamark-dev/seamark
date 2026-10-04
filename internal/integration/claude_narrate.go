package integration

import (
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

// This file holds the words of the Claude Code setup: what init prints
// for .claude/settings.json. The planner in claude_setup.go fills the
// facts while it plans, and the narrator here turns them into lines
// after the write. The split keeps the composition of the document
// apart from its narration, so a wording change touches no plan.

// claudeNarration holds what the settings.json lines report. The
// adapter fills it while it plans, and the narrator reads it after the
// write, so the lines describe exactly what was composed.
type claudeNarration struct {
	binary string
	// hooks is true when the hooks were requested, and specs lists the
	// hooks setup manages in this run. removedEnforce is true when the
	// merge removes --enforce from an owned gate command.
	hooks          bool
	hooksChanged   bool
	specs          []hooks.Spec
	removedEnforce bool
	// grants is the allow-rule plan; nil when none was requested.
	grants *approve.ClaudePlan
}

// changed reports whether the document is written.
func (n *claudeNarration) changed() bool {
	return n.hooksChanged || (n.grants != nil && len(n.grants.Missing) > 0)
}

// narrate prints the settings.json lines in the words init has always
// used: the hooks line with the exact hook commands, the note when
// enforcement leaves the hook, and the allow-rule lines.
func (n *claudeNarration) narrate(w io.Writer, status OpStatus) {
	preview := status == OpPlanned

	if n.hooks {
		switch {
		case !n.changed():
			fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", approve.ClaudeSettings)
		case n.hooksChanged:
			fmt.Fprintf(w, "  %s %s (gate + lessons + context reset hooks)\n", verb("updated", "would update", preview), approve.ClaudeSettings)
		default:
			fmt.Fprintf(w, "  %s %s (permissions; seamark hooks already wired)\n", verb("updated", "would update", preview), approve.ClaudeSettings)
		}

		// The exact hook commands: what runs on which tool must never
		// require opening settings.json to find out.
		for _, spec := range n.specs {
			where := spec.Event
			if spec.Matcher != "" {
				where += " " + spec.Matcher
			}

			fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(n.binary))
		}

		// The note states only what changed, the hook flag. Whether
		// anything still blocks is the effective-mode line's job: a kept
		// enforce policy blocks whatever the flag says.
		if n.removedEnforce {
			fmt.Fprintf(w, "  note    %s --enforce from the gate hook: the hook follows .seamark/policy.yaml\n"+
				"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n",
				verb("removed", "would remove", preview))
		}
	}

	if n.grants != nil {
		n.narrateGrants(w, preview)
	}
}

// narrateGrants lists every allow rule the run added: what a repository
// pre-approves must never require opening settings.json to find out.
// Explicit deny or ask entries are named as kept, like the Codex line
// does, so the user learns why a tool still prompts.
func (n *claudeNarration) narrateGrants(w io.Writer, preview bool) {
	kept := approve.KeptSuffix(n.grants.Conflicts)

	switch {
	case len(n.grants.Missing) == 0 && kept != "":
		// Nothing to add is not everything approved: the kept entries are
		// exactly the rules that still prompt.
		fmt.Fprintf(w, "  kept    %s permissions (nothing to add%s)\n", approve.ClaudeSettings, kept)
	case len(n.grants.Missing) == 0:
		fmt.Fprintf(w, "  kept    %s permissions (seamark tools and skills already approved)\n", approve.ClaudeSettings)
	default:
		fmt.Fprintf(w, "  %s %d Claude Code allow rules in %s (seamark MCP tools + skills%s)\n",
			verb("approved", "would approve", preview), len(n.grants.Missing), approve.ClaudeSettings, kept)

		for _, rule := range n.grants.Missing {
			fmt.Fprintf(w, "          %s\n", rule)
		}
	}
}

// verb picks the preview form of a narration verb.
func verb(applied, preview string, isPreview bool) string {
	if isPreview {
		return preview
	}

	return applied
}
