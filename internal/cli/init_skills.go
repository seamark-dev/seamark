package cli

import (
	"fmt"
	"io"
	"slices"

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
