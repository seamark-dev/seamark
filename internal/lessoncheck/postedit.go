package lessoncheck

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds EvaluatePostEdit, the pure evaluation of the post-edit
// verification (contract K9), and the text of its findings. The CLI
// handler decodes the event, reads the pending record, observes the
// result, writes the reply, the audit record, and deletes the record.
// EvaluatePostEdit does no I/O, so its result depends only on its input.

// maxFindingLines bounds the result lines of the findings text (an
// output cap). One more line counts the rest. The text goes into the
// context of the agent, so it stays short.
const maxFindingLines = 20

// PostEditInput is what the post-edit handler gives to EvaluatePostEdit.
type PostEditInput struct {
	// Config and LoadErr are the results of Load. LoadErr is a tier-1
	// error, and Config.Errors holds the tier-2 errors.
	Config  *Config
	LoadErr error
	// Pending is the record of the call, or nil when no record can serve
	// as the baseline.
	Pending *Pending
	// Snapshot and ObserveErr are the results of ObserveAfter. Both are
	// nil when Pending is nil.
	Snapshot   *Snapshot
	ObserveErr error
	// Paths are the paths of the completion event at the git top level.
	// PathsComplete is false when the handler could not decode the event
	// or resolve one of its paths.
	Paths         []string
	PathsComplete bool
	// NoBaseline is the reason of each result when no record serves:
	// missing-baseline, or the reason of an event that did not decode.
	// The zero value means missing-baseline.
	NoBaseline Reason
	// Root is the git top level, for the absolute paths of the rerun
	// command (contracts §1.3).
	Root    string
	Tool    string
	Outcome ToolOutcome
	// Context carries the post-edit budget of the run, and the handler
	// gives the same context that bounded the observation. The
	// evaluation stops when it ends, and each pin that no checker
	// finished is unknown/resource-limit with post-edit-budget. The zero
	// value leaves the evaluation unbounded.
	Context context.Context
}

// PostEdit is the outcome of one post-edit evaluation.
type PostEdit struct {
	// Results holds one result per applicable executable pin, in file
	// order. It is never nil.
	Results []Result
	// Pins holds the audit form of Results. It is never nil.
	Pins        []AuditPin
	State       SummaryState
	Attribution Attribution
	// Findings is the text of the reply. It is "" when every result is a
	// pass or not-applicable, because then the hook says nothing.
	Findings string
}

// EvaluatePostEdit evaluates the actual result of one covered edit
// (contract K9). The applicable pins are the check and enforce pins
// whose scope holds a path of the operation: a path of the record or of
// the event. The budgets, mute rules, restatement, and lease state of
// the advisory hook never apply (ruling M2). When a path is unresolved,
// every executable pin applies, because the path can lie in any scope.
//
// Without a record, each applicable pin is unknown with the NoBaseline
// reason, and the attribution is no-baseline. An invalid check set
// gives configuration-error for every executable pin, because the scope
// of a broken entry is not known. The evaluation never denies, rewinds,
// or reverts anything: the tool already ran (spec L122).
func EvaluatePostEdit(in PostEditInput) PostEdit {
	out := PostEdit{Results: []Result{}, Attribution: AttributionNoBaseline}
	if in.Snapshot != nil {
		out.Attribution = in.Snapshot.attribution
	}

	paths, complete := in.operationPaths()

	switch {
	case in.LoadErr != nil || in.Config == nil:
		// No pin is known, so no result exists. The state is incomplete.
	case len(in.Config.Errors) > 0:
		out.Results = Evaluate(in.Config, nil)
	case in.Snapshot != nil:
		out.Results = applicableResults(EvaluateWithin(in.Context, LimitPostEditBudget, in.Config, in.Snapshot), paths, complete)
		markUnrecorded(out.Results, in.Paths, in.Snapshot.Selection.Paths)
	default:
		out.Results = withoutBaseline(in.Config, paths, complete, in.noBaselineReason())
	}

	out.Pins = auditPinsOf(out.Results)
	out.State = postEditState(in.Config, in.LoadErr, out.Results)

	if out.State == SummaryViolation || out.State == SummaryIncomplete {
		out.Findings = findingsText(in, out, paths, complete)
	}

	return out
}

// operationPaths returns the paths of the operation and whether the
// path set is resolved. A record names the paths that the guard or the
// baseline handler resolved. A record whose path set is incomplete
// without a named limit has a path that no scope test can place.
func (in PostEditInput) operationPaths() (paths []string, complete bool) {
	if in.Snapshot == nil {
		return sortedUnique(in.Paths), in.PathsComplete
	}

	paths = sortedUnique(append(append([]string{}, in.Snapshot.Selection.Paths...), in.Paths...))
	unresolved := !in.Snapshot.PathsComplete && incompletePaths(in.Snapshot).reason == ReasonIncompletePaths

	return paths, !unresolved
}

// noBaselineReason returns the reason of the results of a run without a
// usable baseline. A record whose observation a cancellation stopped
// gives interrupted. Every other failure to use a record is a missing
// baseline.
func (in PostEditInput) noBaselineReason() Reason {
	switch {
	case in.Pending != nil && errors.Is(in.ObserveErr, context.Canceled):
		return ReasonInterrupted
	case in.Pending != nil, in.NoBaseline == "":
		return ReasonMissingBaseline
	default:
		return in.NoBaseline
	}
}

// applicableResults keeps the results of the pins whose scope holds a
// path of the operation. An unresolved path set keeps every result.
func applicableResults(results []Result, paths []string, complete bool) []Result {
	out := []Result{}

	for _, r := range results {
		if !complete || anyInScope(paths, r.Scope) {
			out = append(out, r)
		}
	}

	return out
}

// markUnrecorded makes a result unknown/missing-baseline when its scope
// holds an event path that the record does not hold. Such a path has no
// before state, so a pass or not-applicable would claim an edit that no
// check saw. The record can lack a path when lessons.yaml changed
// between the two hooks. A violation stays: it was observed.
func markUnrecorded(results []Result, eventPaths, recorded []string) {
	var missing []string

	for _, p := range eventPaths {
		if !slices.Contains(recorded, p) {
			missing = append(missing, p)
		}
	}

	for i := range results {
		r := &results[i]
		if r.Outcome == OutcomeViolation || !anyInScope(missing, r.Scope) {
			continue
		}

		hits := []reasonHit{{reason: ReasonMissingBaseline}}
		if r.Outcome == OutcomeUnknown {
			hits = append(hits, reasonHit{reason: r.Reason, limit: r.Limit})
		}

		r.Outcome = OutcomeUnknown
		r.Reason, r.Limit = pickReason(hits)
		r.Evidence, r.EvidenceOmitted = nil, 0
	}
}

// withoutBaseline returns one unknown result with reason for each
// executable pin whose scope holds a path. No checker runs, because no
// before state exists to compare (contract K9, no record).
func withoutBaseline(cfg *Config, paths []string, complete bool, reason Reason) []Result {
	results := []Result{}
	maxRuleRunes := DefaultLimits().MaxRuleRunes

	for _, pin := range cfg.Pins {
		if !isExecutable(pin.Mode) {
			continue
		}

		scoped := scopedPaths(paths, pin.Scope)
		if complete && len(scoped) == 0 {
			continue
		}

		ref := rawCheckRef(cfg.Raw, pin.CheckID)
		if check, ok := cfg.Checks[pin.CheckID]; ok {
			ref = CheckRef{ID: check.ID, Kind: check.Kind, Version: check.Version}
		}

		res := Result{Outcome: OutcomeUnknown, Reason: reason, Check: ref}
		res.Coverage.PathsInScope = len(scoped)

		results = append(results, withPin(res, pin, maxRuleRunes))
	}

	return results
}

// postEditState returns the summary state of a post-edit run, in the
// order of contract K5: violation, incomplete, no-executable-checks,
// no-applicable-checks, passed. A check set that cannot be established
// is incomplete even without a result.
func postEditState(cfg *Config, loadErr error, results []Result) SummaryState {
	var pass, violation, unknown int

	for _, r := range results {
		switch r.Outcome {
		case OutcomePass:
			pass++
		case OutcomeViolation:
			violation++
		case OutcomeNotApplicable:
		default:
			// An outcome outside the closed list counts as unknown, so it
			// never counts toward a pass.
			unknown++
		}
	}

	switch {
	case violation > 0:
		return SummaryViolation
	case unknown > 0 || loadErr != nil || cfg == nil || len(cfg.Errors) > 0:
		return SummaryIncomplete
	case !hasExecutable(cfg):
		return SummaryNoExecutableChecks
	case pass == 0:
		return SummaryNoApplicableChecks
	default:
		return SummaryPassed
	}
}

// hasExecutable reports whether the check set holds a pin of mode check
// or enforce.
func hasExecutable(cfg *Config) bool {
	for _, pin := range cfg.Pins {
		if isExecutable(pin.Mode) {
			return true
		}
	}

	return false
}

// findingsText returns the reply text of a run with a violation or an
// unknown result. It names the state, the snapshot digest, and for each
// such result the pin-ref, the check, the outcome, and the paths with
// their lines. It holds no excerpt and no rule text: a hook reply
// carries paths, line numbers, digests, codes, and counts (contracts
// §1.6). It never says that the edit was stopped, because the tool
// already ran (spec L122, L168).
func findingsText(in PostEditInput, out PostEdit, paths []string, complete bool) string {
	var (
		b                   strings.Builder
		violations, unknown int
		lines               []string
		omitted             int
	)

	for _, r := range out.Results {
		switch r.Outcome {
		case OutcomeViolation:
			violations++

			for _, e := range r.Evidence {
				lines = append(lines, fmt.Sprintf("- violation: %s: %s", resultHead(r), findingEvidence(r, e)))
			}

			omitted += r.EvidenceOmitted
		case OutcomeUnknown:
			unknown++

			lines = append(lines, fmt.Sprintf("- %s: %s: %s", outcomeText(r), resultHead(r), unknownSubject(r, paths)))
		}
	}

	tool := render.Sanitize(in.Tool)
	if tool == "" {
		tool = "edit"
	}

	snapshot := "none"
	if in.Snapshot != nil {
		snapshot = shortID(in.Snapshot.SHA256)
	}

	fmt.Fprintf(&b, "seamark post-edit lesson checks after this %s call: %s (%d evaluated %s: %d violation, %d unknown; snapshot %s)\n",
		tool, out.State, len(out.Results), plural(len(out.Results), "pin", "pins"), violations, unknown, snapshot)

	if message := configMessage(in); message != "" {
		fmt.Fprintf(&b, "- unknown/configuration-error: lesson checks cannot be established: %s; ask the user to fix .seamark/lessons.yaml\n", message)
	}

	if len(lines) > maxFindingLines {
		omitted += len(lines) - maxFindingLines
		lines = lines[:maxFindingLines]
	}

	for _, line := range lines {
		b.WriteString(render.Sanitize(line))
		b.WriteByte('\n')
	}

	if omitted > 0 {
		fmt.Fprintf(&b, "- and %d more\n", omitted)
	}

	writeNotes(&b, in, tool)

	rerun := verifyCommand
	if complete && len(paths) > 0 {
		rerun = rerunCommand(in.Root, paths)
	}

	if violations > 0 {
		fmt.Fprintf(&b, "The files stay as written; repair the violation in a later edit. Check again: %s\n", render.Sanitize(rerun))
	} else {
		fmt.Fprintf(&b, "Check again: %s\n", render.Sanitize(rerun))
	}

	return b.String()
}

// writeNotes writes the notes that qualify the results: a failed or
// interrupted tool, an observed result that differs from the proposed
// edit, and a call without a baseline.
func writeNotes(b *strings.Builder, in PostEditInput, tool string) {
	switch in.Outcome {
	case ToolFailed:
		fmt.Fprintf(b, "The %s tool failed, so the checks ran on the files as they are now.\n", tool)
	case ToolInterrupted:
		fmt.Fprintf(b, "The %s tool was interrupted, so the checks ran on the files as they are now.\n", tool)
	case ToolSucceeded, ToolDenied, ToolUnknown:
	}

	if in.Snapshot != nil && in.Snapshot.attribution == AttributionProjectionDiffers {
		b.WriteString("The observed result differs from the proposed edit, so the change can include a change by another writer.\n")
	}

	if in.Pending == nil {
		b.WriteString("No pre-edit baseline exists for this call, so the checks cannot tell what this edit changed.\n")
	}
}

// configMessage returns the first configuration error of the run,
// redacted, sanitized, and cut, or "" for a valid check set.
func configMessage(in PostEditInput) string {
	var message string

	switch {
	case in.LoadErr != nil:
		message = strings.TrimPrefix(in.LoadErr.Error(), configErrorPrefix)
	case in.Config != nil && len(in.Config.Errors) > 0:
		message = in.Config.Errors[0].Message
	default:
		return ""
	}

	return render.Truncate(render.Sanitize(redact.Secrets(message)), DefaultLimits().MaxExcerptRunes)
}

// resultHead names the pin and the check of a result.
func resultHead(r Result) string {
	return fmt.Sprintf("pin %s, check %s (%s)", r.PinRef, r.Check.ID, r.Check.Kind)
}

// findingEvidence names one finding by path and line, without the
// excerpt.
func findingEvidence(r Result, e Evidence) string {
	if r.Check.Kind == KindNoAddedPattern && e.Line > 0 {
		return fmt.Sprintf("%s:%d adds a line that matches the pattern", evidencePath(e), e.Line)
	}

	return fmt.Sprintf("%s %s", e.Change, evidencePath(e))
}

// unknownSubject names the paths of an unknown result: its undecided
// paths, else the operation paths in its scope.
func unknownSubject(r Result, paths []string) string {
	var subjects []string

	for _, u := range r.UnknownPaths {
		subjects = append(subjects, evidencePath(u))
	}

	if len(subjects) == 0 {
		subjects = scopedPaths(paths, r.Scope)
	}

	if len(subjects) == 0 {
		return "the paths of this call"
	}

	return strings.Join(sortedUnique(subjects), ", ")
}
