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
	// hooksChanged is true when the merge of the managed hooks changes
	// the file. specs lists the hooks that setup manages in this run, and
	// it is empty when the run requested no hooks. removedEnforce is true
	// when the merge removes --enforce from an owned gate command.
	hooksChanged   bool
	specs          []hooks.Spec
	removedEnforce bool
	// grants is the allow-rule plan; nil when none was requested.
	grants *approve.ClaudePlan
	// lessons is what the plan does to the lesson-check hooks.
	lessons lessonChange
}

// changed reports whether the document is written.
func (n *claudeNarration) changed() bool {
	return n.hooksChanged || n.lessons.changed() || (n.grants != nil && len(n.grants.Missing) > 0)
}

// narrate prints the settings.json lines in the words init has always
// used: the hooks line with the exact hook commands, the note when
// enforcement leaves the hook, and the allow-rule lines.
func (n *claudeNarration) narrate(w io.Writer, status OpStatus) {
	preview := status == OpPlanned

	if len(n.specs) > 0 {
		hooksPart := withLessonDetail(hooksDetail(n.specs), n.hooksChanged, n.lessons)

		switch {
		case !n.changed():
			fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", approve.ClaudeSettings)
		case n.hooksChanged || n.lessons.changed():
			fmt.Fprintf(w, "  %s %s (%s)\n", verb("updated", "would update", preview), approve.ClaudeSettings, hooksPart)
		default:
			fmt.Fprintf(w, "  %s %s (permissions; seamark hooks already wired)\n", verb("updated", "would update", preview), approve.ClaudeSettings)
		}

		narrateHookSpecs(w, n.binary, n.specs)
		n.lessons.narrateCommands(w)

		if n.removedEnforce {
			narrateRemovedEnforce(w, "the gate hook", preview)
		}

		n.lessons.narrateLine(w)
	}

	if n.grants != nil {
		n.narrateGrants(w, preview)
	}
}

// narrateGrants lists every allow rule the run added: what a repository
// pre-approves must never require opening settings.json to find out.
// It names explicit deny or ask entries as kept, like the Codex line,
// so the user learns why a tool still prompts.
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

// withLessonDetail joins the hooks text of a settings file with the
// lesson-check detail. A plan without a lesson-check change keeps the
// line that init has always printed. Claude Code and Codex share this
// rule, so their lines stay in the same form.
func withLessonDetail(hooksPart string, hooksChanged bool, lessons lessonChange) string {
	if !lessons.changed() {
		return hooksPart
	}

	if hooksChanged {
		return hooksPart + "; " + lessons.detail()
	}

	return lessons.detail()
}

// narrateHookSpecs prints the exact command of each managed hook, with
// its event and matcher. What runs on which tool must never require
// opening the settings file to find out.
func narrateHookSpecs(w io.Writer, binary string, specs []hooks.Spec) {
	for _, spec := range specs {
		where := spec.Event
		if spec.Matcher != "" {
			where += " " + spec.Matcher
		}

		fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(binary))
	}
}

// narrateRemovedEnforce prints the note when the merge removes --enforce
// from the gate hook that gateHook names. The note states only what
// changed, the hook flag. Whether anything still blocks is the job of
// the effective-mode line, because a kept enforce policy blocks whatever
// the flag says.
func narrateRemovedEnforce(w io.Writer, gateHook string, preview bool) {
	fmt.Fprintf(w, "  note    %s --enforce from %s: the hook follows .seamark/policy.yaml\n"+
		"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n",
		verb("removed", "would remove", preview), gateHook)
}

// verb picks the preview form of a narration verb.
func verb(applied, preview string, isPreview bool) string {
	if isPreview {
		return preview
	}

	return applied
}
