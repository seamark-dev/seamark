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

// hooksDetail names the managed hooks in init's words, from the specs
// that were merged: "gate + lessons hooks", or the one that was. Claude
// Code and Codex share it, so a hooks line names the specs that it
// installs.
func hooksDetail(specs []hooks.Spec) string {
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
	case !change.changed():
		fmt.Fprintf(w, "  kept    %s (%s)\n", codexHooksFile, change.keptDetail(specs))
	case change.created:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("wrote  ", "would write", preview), codexHooksFile, change.detail(specs))
	default:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("updated", "would update", preview), codexHooksFile, change.detail(specs))
	}

	narrateHookSpecs(w, binary, specs)
	change.lessons.narrateCommands(w)

	if change.removedEnforce {
		narrateRemovedEnforce(w, "the Codex gate hook", preview)
	}

	change.lessons.narrateLine(w)
}

// codexHooksChange says what the plan does to hooks.json.
type codexHooksChange struct {
	created bool // the file does not exist yet
	// hooksChanged is true when the merge of the gate and lesson hooks
	// changes the file.
	hooksChanged bool
	// removedEnforce is true when the merge removes --enforce from an
	// owned gate command.
	removedEnforce bool
	// lessons is what the plan does to the lesson-check hooks.
	lessons lessonChange
}

// changed reports whether the plan writes the file: the merge of the
// managed hooks or the lesson-check change alters it.
func (c codexHooksChange) changed() bool { return c.hooksChanged || c.lessons.changed() }

// detail says what the write does, in init's words. A plan without a
// lesson-check change names the managed hooks, as init always did.
func (c codexHooksChange) detail(specs []hooks.Spec) string {
	return withLessonDetail(hooksDetail(specs), c.hooksChanged, c.lessons)
}

// keptDetail says why setup keeps the file, in init's words. The plan
// and the narration both use it, so the kept line and the plan detail
// agree. Without a managed hook, the hooks run from definitions that
// setup does not manage.
func (c codexHooksChange) keptDetail(specs []hooks.Spec) string {
	if len(specs) == 0 && len(c.lessons.specs) == 0 {
		return "no hook installed: the seamark hooks run from definitions that setup does not manage"
	}

	return "seamark hooks already wired"
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
