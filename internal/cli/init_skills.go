package cli

import (
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/skills"
)

// skillsHint is the one line init prints when --skills was not given
// and no managed skill is installed. Skills stay opt-in until the
// workflow evaluation decides otherwise, so init says how without
// doing it.
const skillsHint = "  skills  not installed — seamark init --skills adds the seamark agent skills " +
	"(.claude/skills, .agents/skills)\n"

// planSkills validates the mode and inspects the client directories
// before init writes anything, for the same reason the settings merge
// runs first: an unreadable directory must abort the run while the
// tree is untouched. An empty mode plans nothing.
func planSkills(root string, targets []skills.Target) ([]skills.Entry, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	return skills.Plan(root, targets)
}

// reportSkills prints init's skills block. With a mode it applies the
// plan; without one it summarizes what is installed, so a plain re-run
// never claims "not installed" over skills an earlier run wrote, or
// over a directory that `seamark init --skills` would refuse to touch.
func reportSkills(w io.Writer, root, mode string, plan []skills.Entry, printOnly bool) error {
	if mode != "" {
		return skills.Apply(w, root, plan, printOnly)
	}

	states := skills.Inspect(root)

	for _, s := range states {
		if s.Installed() || s.Foreign > 0 || s.Err != "" {
			fmt.Fprintf(w, "  skills  %s\n", skills.Summary(states))

			return nil
		}
	}

	fmt.Fprint(w, skillsHint)

	return nil
}
