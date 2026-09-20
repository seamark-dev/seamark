package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
)

// skillsHint is the one line init prints when --skills was not given
// and no managed skill is installed. Skills stay opt-in until the
// workflow evaluation decides otherwise, so init says how without
// doing it.
const skillsHint = "  skills  not installed — seamark init --skills adds the seamark agent skills " +
	"(.claude/skills, .agents/skills)\n"

// reportInstalledSkills prints init's skills line when --skills was not
// given: a summary of what is installed, so a plain re-run never claims
// "not installed" over skills an earlier run wrote, or over a directory
// that `seamark init --skills` would refuse to touch; else the hint.
func reportInstalledSkills(w io.Writer, root string) {
	states := skills.Inspect(root)

	if slices.ContainsFunc(states, skills.ClientState.Notable) {
		fmt.Fprintf(w, "  skills  %s\n", render.Sanitize(skills.Summary(states)))

		return
	}

	fmt.Fprint(w, skillsHint)
}

// reportSelectedSkills is the skills line of a run with --client and
// without --skills. It covers the skill directories of the selected
// clients only, and a shared directory names every client that reads
// it. A shared directory is no proof that a client is set up, so the
// line reports the directory, never the client's state.
func reportSelectedSkills(w io.Writer, root string, reg *integration.Registry, setups []integration.ClientSetup) {
	var selected []integration.Client

	for _, s := range setups {
		if c, ok := reg.Lookup(s.ClientID); ok {
			selected = append(selected, c)
		}
	}

	dests := integration.SkillDestinations(selected)
	if len(dests) == 0 {
		return
	}

	states := skills.InspectTargets(root, integration.SkillTargets(dests))

	if slices.ContainsFunc(states, skills.ClientState.Notable) {
		fmt.Fprintf(w, "  skills  %s\n", render.Sanitize(skills.Summary(states)))

		return
	}

	var dirs []string
	for _, d := range dests {
		dirs = append(dirs, d.Dir)
	}

	fmt.Fprintf(w, "  skills  not installed — add --skills to install the seamark agent skills (%s)\n",
		strings.Join(dirs, ", "))
}
