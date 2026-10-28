package lessonreport

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
)

// RenderText prints the overview or selected identity's version-isolated
// expansion. It presents existing readings and suggestions, never infers
// delivery, timing, or efficacy from counts. All dynamic lines are sanitized.
func RenderText(w io.Writer, report Report) error {
	p := &textReport{}
	p.line("Capture delivery (%s; seamark %s)", report.Schema, report.SeamarkVersion)
	p.line("as of: %s", report.AsOf.GeneratedAt)
	p.line("capture: %s %s", report.Capture.State, report.Capture.Message)
	p.source("delivery audit", report.AsOf.Audit)
	p.source("check audit", report.AsOf.CheckAudit)
	p.line("archive: %s; schema %d; snapshot %s; %s", report.AsOf.Archive.State,
		report.AsOf.Archive.SchemaVersion, report.AsOf.Archive.SnapshotAt, report.AsOf.Archive.Error)

	if equal := report.AsOf.Archive.RootEqual; equal != nil {
		p.line("archive identity root equal: %t", *equal)
	}

	p.line("config: %s; pins %d; guidance %s", report.AsOf.Config.State,
		report.AsOf.Config.Pins, report.AsOf.Config.GuidanceSHA256)
	p.line("Delivery totals (records, not corrections): %s", textCounts(report.Totals.DeliveredRecords))
	p.line("  suppressed hook records: %d; evaluations: %d; resets: %d; events: %d; distinct preparations: %d",
		report.Totals.SuppressedHookRecords, report.Totals.EvaluationRecords, report.Totals.ResetRecords,
		report.Totals.Events, report.Totals.DistinctPreparations)

	p.line("Units:")
	for _, key := range textKeys(report.Units) {
		p.line("  %s: %s", key, report.Units[key])
	}

	for _, group := range []struct {
		name string
		pins []PinReading
	}{{"Current pins", report.Pins}, {"Historical versions", report.Historical}} {
		p.line("%s:", group.name)
		for _, pin := range group.pins {
			if report.Expanded == nil || textSelected(pin, report.Expanded) {
				p.pin(pin)
			}
		}
	}

	p.line("Unlinked feedback items: %d; reasons: %s", report.Unlinked.Count, textCounts(report.Unlinked.ByReason))
	p.steps(report.Unlinked.NextSteps)
	p.line("Coverage (units above):")
	p.coverage(report.Coverage)
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

func textSelected(pin PinReading, exp *Expansion) bool {
	ref := pin.PinRef
	if ref == "" {
		ref = shortRef(pin.PinIdentity)
	}

	return ref == exp.PinRef
}

func (p *textReport) pin(pin PinReading) {
	ref := pin.PinRef
	if ref == "" {
		ref = shortRef(pin.PinIdentity)
	}

	p.line("  pin %s; version %s; mode %s; rule: %s", ref, pin.PinVersion, pin.Mode, pin.Rule)
	p.line("    scope: %s", strings.Join(pin.Scope, ", "))
	p.line("    delivery records: %s; injected %d; suppressed-repeat %d; held-back %d; emit-failed %d; returned %d",
		textCounts(pin.Delivery.Records), pin.Delivery.Injected, pin.Delivery.SuppressedRepeat,
		pin.Delivery.HeldBack, pin.Delivery.EmitFailed, pin.Delivery.Returned)
	p.line("    delivered sessions: %d; legacy identity records (unversioned): %d", pin.Delivery.Sessions, pin.Delivery.LegacyIdentityRecords)
	p.line("    assessed correction occurrences: %d; incidents: %d; unknown action: %d; classes: %s; origins: %s",
		pin.Occurrences.Occurrences, pin.Occurrences.Incidents, pin.Occurrences.UnknownAction,
		textCounts(pin.Occurrences.ByClass), textCounts(pin.Occurrences.ByOrigin))
	p.line("    excluded: %d; changed-evidence: %d; independence unresolved: %t",
		pin.Occurrences.Excluded, pin.Occurrences.ChangedEvidence, pin.Occurrences.IndependenceUnresolved)
	p.line("    decision timing (occurrences): %s", textCounts(pin.Timing))
	p.line("    observation (%s): %d linked; %d no linked correction observed (qualified); %d incomplete",
		pin.Observation.Unit, pin.Observation.Linked, pin.Observation.NoLinkedCorrection, pin.Observation.Incomplete)
	p.line("    endpoint: %s; assessment as of: %s; incomplete reasons: %s",
		pin.Observation.Endpoint, pin.Observation.AssessmentAsOf, textCounts(pin.Observation.IncompleteByReason))

	for _, timing := range textKeys(pin.Observation.ByTiming) {
		part := pin.Observation.ByTiming[timing]
		p.line("      %s (delivered sessions): %d linked; %d no linked correction observed (qualified); %d incomplete",
			timing, part.Linked, part.NoLinkedCorrection, part.Incomplete)
	}

	p.line("    facts (occurrences): %s; unknown reasons (actions): %s; limitations: %v",
		textCounts(pin.Facts), textCounts(pin.Reasons), pin.Limitations)
	p.line("    check pin evaluations: %d; outcomes: %s; requested denies (guard attempts): %d",
		pin.Checks.PinEvaluations, textCounts(pin.Checks.Outcomes), pin.Checks.RequestedDenies)

	p.line("    expand: seamark lessons --delivery --pin %s", ref)
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

func (p *textReport) coverage(c Coverage) {
	p.line("  unmatched audit sessions: %d; unmatched archive sessions: %d; root mismatch sessions: %d",
		c.UnmatchedAuditSessions, c.UnmatchedArchiveSessions, c.RootMismatchSessions)
	p.line("  unknown lane records: %d; unknown timing actions: %d; missing manifests: %d; incomplete comparisons: %d",
		c.UnknownLaneRecords, c.UnknownTimingActions, c.MissingManifests, c.IncompleteComparisons)
	p.line("  unsupported old assessments: %d; missing actions: %d; ambiguous peers: %d; source-deleted associations: %d",
		c.UnsupportedOldAssessments, c.MissingActions, c.AmbiguousPeers, c.SourceDeletedAssociations)
	p.line("  events without pins: %d; denied-call injections: %d; collapsed copies: %d",
		c.EventsWithoutPins, c.DeniedCallInjections, c.CollapsedCopies)
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

func (p *textReport) expansion(exp Expansion) {
	p.line("Expansion: pin %s (versions are never pooled)", exp.PinRef)

	for _, version := range exp.Versions {
		p.line("  version %s", version)
		for _, d := range exp.Occurrences {
			if d.PinVersion == version {
				p.occurrence(d)
			}
		}

		for _, s := range exp.Sessions {
			if s.PinVersion != version {
				continue
			}

			partition := s.Partition
			if partition == "no-linked-correction" {
				partition = "no linked correction observed (qualified)"
			}

			p.line("    session %s; lane %s; %s; decision timing %s; reasons %v; opening %s; endpoint %s",
				s.SessionRef, s.Lane, partition, s.Timing, s.Reasons, s.StartRecord, s.Endpoint)
		}
	}
}

func (p *textReport) occurrence(d OccurrenceDetail) {
	p.line("    association %s; episode %s; family %s; origin %s; assessment %s; review %s",
		d.AssociationRef, d.EpisodeRef, d.Family, d.Origin, d.Assessment, d.Review)
	p.line("      class %s; reason %s; decision timing %s; facts %v; limitations %v",
		d.Class, d.Reason, d.Timing, d.Facts, d.Limitations)

	for _, a := range d.Actions {
		p.line("      action %s; incident %s; lane %s; class %s; reason %s; cause %s",
			a.ActionRef, a.Incident, a.Lane, a.Class, a.Reason, a.Cause)
		p.line("        %s", classText(a))
		p.line("        decision timing %s; timing reason %s; secondary %v; paths %v",
			a.Timing, a.TimingReason, a.Secondary, a.Paths)
		for _, e := range a.Evidence {
			p.line("        evidence: %s; surface %s; record %s; limitation %s", e.Kind, e.Surface, e.Record, e.Limit)
		}
	}

	for _, command := range d.Inspect {
		p.line("      inspect: %s", command)
	}
}

func classText(a ActionDetail) string {
	switch a.Class {
	case ClassAtAction:
		return "Reminder emitted for an already-constructed action."
	case ClassEarlierInContext:
		return "Delivered earlier in this context."
	case ClassOtherLane:
		return "Delivery recorded only in another lane."
	case ClassRegionNotMatched:
		return "Corrected action outside this pin's regions."
	case ClassNotEmitted:
		return "Not emitted: " + a.Cause
	default:
		return "Unknown: " + string(a.Reason)
	}
}

func textKeys[K ~string, V any](values map[K]V) []K {
	keys := make([]K, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	return keys
}

func textCounts[K ~string](counts map[K]int) string {
	parts := make([]string, 0, len(counts))
	for _, key := range textKeys(counts) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}

	if len(parts) == 0 {
		return "none"
	}

	return strings.Join(parts, ", ")
}
