package report

import (
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/render"
)

// Decision writes the verdict block shared by `seamark gate`, `seamark
// check`, and the MCP check tool. Rule messages come from workspace
// policy files — untrusted in a cloned repo, hence the wash.
func Decision(w io.Writer, d *gate.Decision) {
	fmt.Fprintf(w, "verdict  %s (mode: %s)\n", d.Verdict, d.Mode)

	if len(d.Effects) > 0 {
		fmt.Fprintf(w, "effects  %s\n", render.Sanitize(fmt.Sprint(d.Effects)))
	}

	for _, m := range d.Matches {
		fmt.Fprintf(w, "  [%s] %s: %s\n", m.Verdict, m.RuleID, render.Sanitize(m.Message))
	}
}
