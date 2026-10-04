package integration

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds the words of the Codex setup: what init prints for
// .codex/hooks.json and .codex/config.toml. The planner in
// codex_setup.go fills the facts while it plans, and the narrator here
// turns them into lines after the write. The split keeps the
// composition of the documents apart from their narration, so a
// wording change touches no plan.

// codexHooksDetail names the managed hooks in init's words, from the
// specs that were merged: "gate + lessons hooks", or the one that was.
func codexHooksDetail(specs []hooks.Spec) string {
	names := make([]string, 0, len(specs))

	for _, spec := range specs {
		names = append(names, spec.Name)
	}

	if len(names) == 1 {
		return names[0] + " hook"
	}

	return strings.Join(names, " + ") + " hooks"
}

// narrateCodexHooks prints the hooks.json lines in the form of the
// Claude Code lines: the file line, then each managed hook command, then
// the note when enforcement leaves the gate hook. What runs on which
// tool must never require opening the file to find out.
func narrateCodexHooks(w io.Writer, binary string, specs []hooks.Spec, change codexHooksChange, preview bool) {
	switch {
	case len(specs) == 0:
		fmt.Fprintf(w, "  kept    %s (no hook installed: the seamark hooks run from definitions that setup does not manage)\n", codexHooksFile)
	case !change.changed:
		fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", codexHooksFile)
	case change.created:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("wrote  ", "would write", preview), codexHooksFile, codexHooksDetail(specs))
	default:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("updated", "would update", preview), codexHooksFile, codexHooksDetail(specs))
	}

	for _, spec := range specs {
		where := spec.Event
		if spec.Matcher != "" {
			where += " " + spec.Matcher
		}

		fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(binary))
	}

	// The note states only what changed, the hook flag. Whether anything
	// still blocks is the effective-mode line's job: a kept enforce
	// policy blocks whatever the flag says.
	if change.removedEnforce {
		fmt.Fprintf(w, "  note    %s --enforce from the Codex gate hook: the hook follows .seamark/policy.yaml\n"+
			"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n",
			verb("removed", "would remove", preview))
	}
}

// codexHooksChange says what the plan does to hooks.json.
type codexHooksChange struct {
	changed bool // the plan writes the file
	created bool // the file does not exist yet
	// removedEnforce is true when the merge removes --enforce from an
	// owned gate command.
	removedEnforce bool
}

// codexWriteDetail says what the write adds, in init's words.
func codexWriteDetail(config *approve.CodexPlan) string {
	var parts []string

	if config.Register {
		parts = append(parts, "registered seamark mcp")
	}

	if len(config.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("approved %d tools: %s", len(config.Missing), strings.Join(config.Missing, ", ")))
	}

	return strings.Join(parts, "; ")
}

// codexKeptDetail says why the file needs nothing.
func codexKeptDetail(config *approve.CodexPlan) string {
	if !config.Registered {
		return "seamark not registered"
	}

	return fmt.Sprintf("seamark registered as %q; %d/%d tools approved",
		render.Sanitize(config.Server), len(config.Approved), len(approve.Tools))
}
