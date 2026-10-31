package lessonreport

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
)

// RenderText prints the overview or the expansion of the selected
// identity, one version at a time. It presents existing readings and
// suggestions. It never infers delivery, timing, or efficacy from counts.
// Every dynamic line passes through render.Sanitize.
//
// The text shows only what the audit logs give. This build reads no
// capture archive, so the text leaves out the capture and archive state,
// the occurrences, the decision timing, the observation windows, the
// historical versions, the unlinked feedback, and the coverage counts.
// The JSON document keeps these fields with their fixed values.
func RenderText(w io.Writer, report Report) error {
	p := &textReport{}
	p.line("Lesson delivery (%s; seamark %s)", report.Schema, report.SeamarkVersion)
	p.line("as of: %s", report.AsOf.GeneratedAt)
	p.source("delivery audit", report.AsOf.Audit)
	p.source("check audit", report.AsOf.CheckAudit)
	p.line("config: %s; pins %d; guidance %s", report.AsOf.Config.State,
		report.AsOf.Config.Pins, report.AsOf.Config.GuidanceSHA256)
	p.line("Delivery totals (records, not corrections): %s", textCounts(report.Totals.DeliveredRecords))
	p.line("  suppressed hook records: %d; evaluations: %d; resets: %d; events: %d; distinct preparations: %d",
		report.Totals.SuppressedHookRecords, report.Totals.EvaluationRecords, report.Totals.ResetRecords,
		report.Totals.Events, report.Totals.DistinctPreparations)

	p.line("Units:")
	for _, key := range slices.Sorted(maps.Keys(report.Units)) {
		p.line("  %s: %s", key, report.Units[key])
	}

	p.line("Current pins:")
	for _, pin := range report.Pins {
		// The expansion names its pin by the same rule, so this
		// comparison selects the expanded identity.
		ref := shownPinRef(pin.PinRef, pin.PinIdentity)
		if report.Expanded == nil || ref == report.Expanded.PinRef {
			p.pin(pin, ref)
		}
	}

	p.checks(report.Checks)

	if report.Expanded != nil {
		p.expansion(*report.Expanded)
	}

	n, err := io.WriteString(w, p.String())
	if err == nil && n != p.Len() {
		return io.ErrShortWrite
	}

	return err
}

// textReport buffers a report so write failures reach the caller. Newlines
// come only from the renderer, not from configured or captured strings.
type textReport struct{ strings.Builder }

func (p *textReport) line(format string, args ...any) {
	p.WriteString(render.Sanitize(fmt.Sprintf(format, args...)))
	p.WriteByte('\n')
}

func (p *textReport) source(name string, s SourceRead) {
	p.line("%s: %s (%s); generations %d; bytes %d; records %d; legacy %d; malformed %d; newer schema %d; partial tail %t; %s",
		name, s.State, s.Path, s.Generations, s.Bytes, s.Records, s.Legacy, s.Malformed, s.NewerSchema, s.PartialTail, s.Error)
}

// pin prints one reading under its shown pin-ref.
func (p *textReport) pin(pin PinReading, ref string) {
	p.line("  pin %s; version %s; mode %s; rule: %s", ref, pin.PinVersion, pin.Mode, pin.Rule)
	p.line("    scope: %s", strings.Join(pin.Scope, ", "))
	p.line("    delivery records: %s; injected %d; suppressed-repeat %d; held-back %d; emit-failed %d; returned %d",
		textCounts(pin.Delivery.Records), pin.Delivery.Injected, pin.Delivery.SuppressedRepeat,
		pin.Delivery.HeldBack, pin.Delivery.EmitFailed, pin.Delivery.Returned)
	p.line("    delivered sessions: %d; legacy identity records (unversioned): %d", pin.Delivery.Sessions, pin.Delivery.LegacyIdentityRecords)
	p.line("    check pin evaluations: %d; outcomes: %s; requested denies (guard attempts): %d",
		pin.Checks.PinEvaluations, textCounts(pin.Checks.Outcomes), pin.Checks.RequestedDenies)

	p.line("    expand: %s", expandCommand(ref))
	p.steps(pin.NextSteps)
}

func (p *textReport) steps(steps []NextStep) {
	for _, step := range steps {
		p.line("    next [%s; %s]: %s", step.Code, step.Scope, step.Text)
		if step.Command != "" {
			p.line("      %s", step.Command)
		}
	}
}

func (p *textReport) checks(c CheckReading) {
	p.line("Checks / guard (separate from delivery): %s", c.State)
	p.line("  check operations: %s; pin evaluations: %d; outcomes: %s; unknown reasons: %s",
		textCounts(c.Operations), c.PinEvaluations, textCounts(c.Outcomes), textCounts(c.UnknownByReason))
	g := c.GuardAttempts
	p.line("  guard attempts: allowed %d; requested denies %d; verified native %d; reply not written %d",
		g.Allowed, g.RequestedDenies, g.VerifiedNative, g.ReplyNotWritten)
	p.line("  executed after deny: %d; no execution observed: %d; prevented: %d; inactive enforcement pins: %d",
		g.ExecutedAfterDeny, g.NoExecutionObserved, g.Prevented, c.InactiveEnforcement)
}

// expansion lists the versions of the selected pin. The occurrences and
// the session windows of each version need a capture archive, so the
// text leaves them out, as it does for the overview.
func (p *textReport) expansion(exp Expansion) {
	p.line("Expansion: pin %s (versions are never pooled)", exp.PinRef)

	for _, version := range exp.Versions {
		p.line("  version %s", version)
	}
}

func textCounts[K ~string](counts map[K]int) string {
	parts := make([]string, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}

	if len(parts) == 0 {
		return "none"
	}

	return strings.Join(parts, ", ")
}
