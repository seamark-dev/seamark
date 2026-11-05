package delivery

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
)

// This file holds the one text renderer of a preparation (contract L1).
// The CLI prints the full form. MCP change_set appends the supplement
// form after its own text, which already lists the lesson text, so the
// supplement never repeats a pin note or a mined symptom.

// The text forms of RenderPreparation. The list is closed.
const (
	// FormFull is the text of seamark lessons --prepare.
	FormFull = "full"
	// FormSupplement is the second text part of MCP change_set.
	FormSupplement = "supplement"
)

// shownHex is the printed length of a digest, as of a pin-ref.
const shownHex = 12

// RenderPreparation writes p as text in form, FormFull or FormSupplement.
// An unknown form writes nothing.
//
// The supplement form writes nothing when p has no pin, no check, no
// omission, and no rejected path. MCP then adds no second text part,
// because the first part already says everything.
//
// Every value that comes from a file, the index, or the caller passes
// render.Sanitize, so no control byte reaches a terminal or an agent.
func RenderPreparation(w io.Writer, p *Preparation, form string) {
	full := form == FormFull

	switch {
	case form != FormFull && form != FormSupplement:
		return
	case !full && len(p.Pins) == 0 && len(p.Checks) == 0 && len(p.Omitted) == 0 && p.Request.RejectedCount == 0:
		return
	}

	fmt.Fprintf(w, "lesson preparation (%s) for %d path(s) — receiver %s\n",
		PreparationSchema, len(p.Request.Paths), render.Sanitize(p.Receiver.Lane))

	writeGuidance(w, p)

	if full {
		writeScopePaths(w, p)
	}

	writeRejected(w, p)
	writePins(w, p, full)

	if full {
		writeLessons(w, p)
	}

	writeChecks(w, p)
	writeOmitted(w, p)

	fmt.Fprintf(w, "\nprepare again when the file set grows, when .seamark/lessons.yaml changes (guidance %s), "+
		"or when another agent or subagent will construct the change\n", guidanceRef(p.Guidance))
	fmt.Fprintln(w, p.Notice)
}

// writeGuidance writes the guidance line and one line for each state
// that limits the selection, so the reader knows why a list is short.
func writeGuidance(w io.Writer, p *Preparation) {
	g := p.Guidance

	fmt.Fprintf(w, "guidance %s · change budget %d · mined lessons %s · checks %s\n",
		guidanceRef(g), g.Budget, render.Sanitize(g.MinedStatus), render.Sanitize(g.ChecksStatus))

	switch g.Status {
	case report.GuidanceAbsent:
		fmt.Fprintln(w, "  no .seamark/lessons.yaml exists, so no pin applies")
	case report.GuidanceInvalid:
		fmt.Fprintln(w, "  .seamark/lessons.yaml does not read or decode, so the defaults apply and no pin is selected")
	}

	switch g.MinedStatus {
	case MinedNoIndex:
		fmt.Fprintln(w, "  no index exists, so the selection holds pins only (`seamark index` builds it)")
	case MinedUnavailable:
		fmt.Fprintln(w, "  the index did not open, so the selection holds pins only")
	}

	if g.ChecksStatus == checksInvalid {
		fmt.Fprintf(w, "  checks are invalid: %s\n", render.Sanitize(g.ChecksError))
	}

	if len(p.Request.Paths) == 0 {
		fmt.Fprintln(w, "  no requested path is inside the workspace, so nothing is selected")
	}
}

// guidanceRef is the short guidance digest, or the guidance status when
// the file gave no digest.
func guidanceRef(g Guidance) string {
	if g.GuidanceSHA256 == "" {
		return render.Sanitize(g.Status)
	}

	return shortHex(g.GuidanceSHA256)
}

// writeScopePaths writes each resolved path with its state and counts.
func writeScopePaths(w io.Writer, p *Preparation) {
	if len(p.Request.Paths) == 0 {
		return
	}

	fmt.Fprintln(w, "\npaths")

	for _, sp := range p.Request.Paths {
		fmt.Fprintf(w, "  %s (%s) · %s · %s\n", render.Sanitize(sp.Path), render.Sanitize(sp.State),
			countText(sp.Pins, "pin", "pins"), countText(sp.Checks, "check", "checks"))
	}
}

// writeRejected writes the rejected inputs. The document keeps the first
// few, so the line names the full count.
func writeRejected(w io.Writer, p *Preparation) {
	if p.Request.RejectedCount == 0 {
		return
	}

	fmt.Fprintf(w, "\nrejected %s (first %d listed)\n",
		countText(p.Request.RejectedCount, "input", "inputs"), len(p.Request.Rejected))

	for _, r := range p.Request.Rejected {
		fmt.Fprintf(w, "  %s: %s\n", render.Sanitize(r.Path), render.Sanitize(r.Reason))
	}
}

// writePins writes the selected pins with their exact versions. The full
// form adds the pin text. The supplement form names the rule only,
// because the first MCP part already shows the pin text.
func writePins(w io.Writer, p *Preparation, full bool) {
	if len(p.Pins) == 0 {
		return
	}

	fmt.Fprintln(w, "\npins")

	for _, pin := range p.Pins {
		fmt.Fprintf(w, "- pin %s · version %s · %s · entry %d · %s",
			render.Sanitize(pin.PinRef), shortHex(pin.PinVersion), render.Sanitize(pin.Mode), pin.Entry,
			render.Sanitize(pin.Rule))

		if full && pin.Note != "" {
			fmt.Fprintf(w, " — %s", render.Sanitize(pin.Note))
		}

		fmt.Fprintln(w)

		if full {
			fmt.Fprintf(w, "  scope %s · paths %s\n", scopeText(pin.Scope), joinSanitized(pin.Paths))
		}
	}
}

// writeLessons writes the selected mined lessons with the tags of the
// change_set lesson block.
func writeLessons(w io.Writer, p *Preparation) {
	if len(p.Lessons) == 0 {
		return
	}

	fmt.Fprintln(w, "\nmined lessons")

	for _, l := range p.Lessons {
		fmt.Fprintf(w, "- [×%d · %s] %s\n", l.Occurrences, render.Sanitize(l.Region), render.Sanitize(l.Symptom))
	}
}

// writeChecks writes every check in scope. The list has no budget, so
// the header says that every check counts.
func writeChecks(w io.Writer, p *Preparation) {
	if len(p.Checks) == 0 {
		return
	}

	fmt.Fprintln(w, "\nchecks (every check in scope; no budget applies)")

	for _, c := range p.Checks {
		runs := "runs"
		if !c.Runs {
			runs = "does not run (advisory)"
		}

		fmt.Fprintf(w, "- pin %s · version %s · %s · %s · check %s\n",
			render.Sanitize(c.PinRef), shortHex(c.PinVersion), render.Sanitize(c.Mode), runs, checkText(c))

		if c.Predicate != "" {
			fmt.Fprintf(w, "  %s\n", render.Sanitize(c.Predicate))
		}

		fmt.Fprintf(w, "  paths %s\n", joinSanitized(c.Paths))

		if len(c.Enforcement) > 0 {
			fmt.Fprintf(w, "  enforcement %s\n", enforcementText(c.Enforcement))
		}
	}
}

// enforcementText names the badge of each client, with its reasons and
// limits, for example "claude enforced-unverified
// (native-deny-unverified) · codex requested-inactive (not-installed)".
// The badges come from the capability report, so preparation never
// computes one.
func enforcementText(clients []lessoncheck.PinClient) string {
	parts := make([]string, 0, len(clients))

	for _, c := range clients {
		text := render.Sanitize(c.Client) + " " + render.Sanitize(c.Badge)
		if note := c.Note(); note != "" {
			text += " (" + render.Sanitize(note) + ")"
		}

		parts = append(parts, text)
	}

	return strings.Join(parts, " · ")
}

// checkText names a check by ID, kind, and short version. A reference
// without a definition has no kind and no version, and an executable
// pin without a reference has no ID either.
func checkText(c CheckSummary) string {
	id := render.Sanitize(c.Check.ID)

	switch {
	case id == "":
		return "(none bound)"
	case c.Check.Kind == "":
		return id + " (not defined)"
	}

	return fmt.Sprintf("%s (%s, version %s)", id, render.Sanitize(string(c.Check.Kind)), shortHex(c.Check.Version))
}

// writeOmitted writes each left-out pin and mined lesson with the
// command that shows it. The first MCP part does not show these lessons,
// so the rule text is not a repeat.
func writeOmitted(w io.Writer, p *Preparation) {
	if len(p.Omitted) == 0 {
		return
	}

	fmt.Fprintln(w, "\nomitted")

	for _, o := range p.Omitted {
		// A cluster key is the region, a NUL byte, and the symptom. The
		// line prints the symptom as the rule, so it names the region only.
		region, _, _ := strings.Cut(o.ClusterKey, "\x00")

		name := "lesson " + render.Sanitize(region)
		if o.Kind == omittedPin {
			name = fmt.Sprintf("pin %s · version %s", render.Sanitize(o.PinRef), shortHex(o.PinVersion))
		}

		fmt.Fprintf(w, "- %s · %s · %s: %s\n",
			name, render.Sanitize(o.Reason), render.Sanitize(o.Rule), render.Sanitize(o.Inspect))
	}
}

// scopeText prints a pin scope. An empty scope is repository-wide.
func scopeText(scope []string) string {
	if len(scope) == 0 {
		return "repository-wide"
	}

	return joinSanitized(scope)
}

// joinSanitized joins sanitized values with a comma.
func joinSanitized(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = render.Sanitize(v)
	}

	return strings.Join(out, ", ")
}

// shortHex is the printed prefix of a digest.
func shortHex(digest string) string {
	return render.Sanitize(digest[:min(len(digest), shownHex)])
}

// countText prints a count with the singular or the plural noun.
func countText(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}

	return fmt.Sprintf("%d %s", n, many)
}
