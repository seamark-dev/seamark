package lessoncheck

import (
	"context"
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds DecideGuard, the pure decision of the pre-write guard
// (contract K8, steps 1, 3, and 7), and the deny texts. The CLI handler
// decodes the request, builds the snapshot, writes the reply, the
// pending record, and the audit record. DecideGuard does no I/O, so a
// decision depends only on its input.

// GuardSchema is the format name of a guard decision.
const GuardSchema = "lesson-guard/1"

// The closed list of decisions (GuardDecision.Decision).
const (
	GuardAllow = "allow"
	GuardDeny  = "deny"
)

// The closed list of decision causes (GuardDecision.Cause). The last two
// go with an allow only. A deny cause wins over an allow cause.
const (
	GuardCauseViolation          = "violation"
	GuardCauseUnknown            = "unknown"
	GuardCauseConfigurationError = "configuration-error"
	GuardCauseUnparseableInput   = "unparseable-input"
	GuardCauseInternalError      = "internal-error"
	// GuardCauseToolUncovered is a tool outside the covered list that
	// reaches the guard, for example through a hand-edited matcher.
	GuardCauseToolUncovered = "tool-uncovered"
	// GuardCauseOutsideRoot is an operation path that is lexically
	// outside the root. It is outside the scope of every pin.
	GuardCauseOutsideRoot = "outside-root"
)

// The closed list of requested replies (GuardDecision.Requested). An
// allow requests nothing: the guard writes an empty reply and never
// forces an allow (spec L166).
const (
	GuardRequestedDeny = "deny"
	GuardRequestedNone = "none"
)

// GuardProtocolExit2 is the deny protocol: exit status 2 with the
// reasons on stderr. A JSON permission decision replaces it only after
// a native recording shows that a client ignores exit 2 (§6 A7).
const GuardProtocolExit2 = "exit-2-stderr"

// The closed list of native blocking levels (GuardDecision.NativeBlocking).
// The CLI handler copies the level from the capability report of the
// client. DecideGuard leaves the field empty.
const (
	GuardNativeVerified   = "verified"
	GuardNativeUnverified = "unverified"
)

// The output caps of the deny texts (contract K3, output caps). A cap
// cuts the text and never changes the decision.
const (
	// maxDenyReasons bounds the deny texts. One more text counts the rest.
	maxDenyReasons = 5
	// maxDenyReasonBytes bounds one deny text, ellipsis included.
	maxDenyReasonBytes = 300
	// maxDenyExcerptRunes bounds the excerpt inside a deny text.
	maxDenyExcerptRunes = 120
)

// denyEllipsis marks a cut deny text. It is the mark of render.Truncate.
const denyEllipsis = "…"

// unresolvedSubject names the path of an unknown result when the guard
// cannot name an operation path in the pin scope: the operation holds a
// path that the guard cannot resolve.
const unresolvedSubject = "a path that the guard cannot resolve"

// GuardInput is what the guard handler gives to DecideGuard.
type GuardInput struct {
	// Config and LoadErr are the results of Load. LoadErr is a tier-1
	// error, and Config.Errors holds the tier-2 errors.
	Config  *Config
	LoadErr error
	// Operation is the decoded edit with its paths normalized.
	Operation Operation
	// Snapshot and ProjectErr are the results of ProjectOperation.
	Snapshot   *Snapshot
	ProjectErr error
	// Context carries the guard budget of the run, and the handler gives
	// the same context that bounded the projection. The evaluation stops
	// when it ends, and every applicable enforce pin then gives
	// unknown/resource-limit, which denies (contract K8, step 2). The
	// zero value leaves the evaluation unbounded, for tests and callers
	// without a budget.
	Context context.Context
}

// beforeDecide runs at the start of DecideGuard when it is not nil.
// Only tests set it: it is the one point where a test can make the
// decision panic and check the recovery of step 7.
var beforeDecide func()

// DecideGuard decides one guard request (contract K8). It returns the
// decision and, for a deny, the deny texts for stderr. The decision
// holds codes, digests, and counts only, because the audit record
// embeds it (contracts §1.6). The texts can hold a path and a redacted
// excerpt, and they go to stderr only.
//
// The applicable pins are the enforce pins whose scope holds a path of
// the operation. The budgets, mute rules, restatement, and lease state
// of the advisory hook never apply (ruling M2). When the guard cannot
// resolve a path of the operation, every enforce pin applies, because
// the path can lie in any scope. Any violation or unknown result of an
// applicable pin denies. A check pin never denies (spec L261).
//
// DecideGuard leaves ReplyWritten and NativeBlocking empty. The handler
// sets them, because only the handler writes the reply and reads the
// capability report. A panic gives a deny with cause internal-error
// (step 7).
func DecideGuard(in GuardInput) (decision GuardDecision, reasons []string) {
	defer func() {
		if recover() != nil {
			decision, reasons = denyInternal(in.Operation.Tool)
		}
	}()

	if beforeDecide != nil {
		beforeDecide()
	}

	if in.LoadErr != nil {
		return denyConfiguration(nil, strings.TrimPrefix(in.LoadErr.Error(), configErrorPrefix))
	}

	if in.Config == nil {
		return denyInternal(in.Operation.Tool)
	}

	if len(in.Config.Errors) > 0 {
		return denyConfiguration(enforceResults(Evaluate(in.Config, nil), nil), in.Config.Errors[0].Message)
	}

	applicable := applicablePins(in.Config, in.Operation, in.Snapshot)
	if len(applicable) == 0 {
		return allowDecision(in.Operation, in.Snapshot, []Result{}), nil
	}

	if in.ProjectErr != nil || in.Snapshot == nil {
		return denyInternal(in.Operation.Tool)
	}

	results := enforceResults(EvaluateWithin(in.Context, LimitGuardBudget, in.Config, in.Snapshot), applicable)

	cause := ""

	for _, r := range results {
		switch {
		case r.Outcome == OutcomeViolation:
			cause = GuardCauseViolation
		case r.Outcome == OutcomeUnknown && cause == "":
			cause = GuardCauseUnknown
		}
	}

	if cause == "" {
		return allowDecision(in.Operation, in.Snapshot, results), nil
	}

	decision = GuardDecision{
		Schema:        GuardSchema,
		Decision:      GuardDeny,
		Cause:         cause,
		Reasons:       unknownReasons(results),
		InputSHA256:   in.Snapshot.SHA256,
		PathsComplete: in.Snapshot.PathsComplete,
		Pins:          auditPinsOf(results),
		Requested:     GuardRequestedDeny,
		Protocol:      GuardProtocolExit2,
	}

	return decision, denyTexts(in.Operation, in.Snapshot, results)
}

// DenyUnparseable returns the deny of a covered request that does not
// decode (contract K8, fail-closed cases). tool is the native tool name
// from the request, or "" when the request names none. The decoder
// error goes into the text redacted and sanitized, because it can quote
// a part of the request.
func DenyUnparseable(tool string, err error) (decision GuardDecision, texts []string) {
	subject := "this request"
	if tool != "" {
		subject = "this " + render.Sanitize(tool) + " request"
	}

	detail := "the request does not decode"
	if err != nil {
		detail = redact.Secrets(err.Error())
	}

	decision = denyOnly(GuardCauseUnparseableInput, []Reason{ReasonUnparseableInput})
	texts = []string{denyText("lesson check guard cannot read " + subject + ": " + detail)}

	return decision, texts
}

// applicablePins returns the entries of the enforce pins that the
// operation reaches. A pin applies when its scope holds a path of the
// operation. Every enforce pin applies when the operation holds a path
// whose real location the guard or the projection cannot name.
func applicablePins(cfg *Config, op Operation, snap *Snapshot) map[int]bool {
	unresolved := !op.PathsComplete ||
		(snap != nil && !snap.PathsComplete && incompletePaths(snap).reason == ReasonIncompletePaths)

	paths := operationPaths(op.Ops)
	applicable := map[int]bool{}

	for _, pin := range cfg.Pins {
		if pin.Mode != ModeEnforce {
			continue
		}

		if unresolved || anyInScope(paths, pin.Scope) {
			applicable[pin.Entry] = true
		}
	}

	return applicable
}

// anyInScope reports whether a scope holds one of the paths.
func anyInScope(paths, scope []string) bool {
	for _, p := range paths {
		if inScope(p, scope) {
			return true
		}
	}

	return false
}

// enforceResults keeps the results of the enforce pins. A nil set keeps
// every enforce pin. The check pins never decide the guard (spec L261).
func enforceResults(results []Result, applicable map[int]bool) []Result {
	out := []Result{}

	for _, r := range results {
		if r.Mode == ModeEnforce && (applicable == nil || applicable[r.Entry]) {
			out = append(out, r)
		}
	}

	return out
}

// unknownReasons returns the reasons of the unknown results, once each,
// in the reason precedence order of contract K3.
func unknownReasons(results []Result) []Reason {
	var out []Reason

	for _, reason := range reasonPrecedence {
		for _, r := range results {
			if r.Outcome == OutcomeUnknown && r.Reason == reason {
				out = append(out, reason)

				break
			}
		}
	}

	return out
}

// allowDecision returns an allow. A path outside the root gives the
// cause outside-root, because the guard dropped an operation path.
func allowDecision(op Operation, snap *Snapshot, results []Result) GuardDecision {
	decision := GuardDecision{
		Schema:        GuardSchema,
		Decision:      GuardAllow,
		PathsComplete: op.PathsComplete,
		Pins:          auditPinsOf(results),
		Requested:     GuardRequestedNone,
		Protocol:      GuardProtocolExit2,
	}

	if snap != nil {
		decision.InputSHA256 = snap.SHA256
		decision.PathsComplete = snap.PathsComplete
	}

	if len(op.Dropped) > 0 {
		decision.Cause = GuardCauseOutsideRoot
	}

	return decision
}

// denyOnly returns a deny without a snapshot: the guard decided before
// a projection existed, or without one.
func denyOnly(cause string, reasons []Reason) GuardDecision {
	return GuardDecision{
		Schema:    GuardSchema,
		Decision:  GuardDeny,
		Cause:     cause,
		Reasons:   reasons,
		Pins:      []AuditPin{},
		Requested: GuardRequestedDeny,
		Protocol:  GuardProtocolExit2,
	}
}

// denyConfiguration returns the deny of a check set that cannot be
// established (step 1). results holds the configuration-error result
// of each enforce pin of a tier-2 error, and nil for a tier-1 error.
// No fallback to DefaultConfig exists, because a fallback would allow
// what the broken file meant to forbid (spec L166).
func denyConfiguration(results []Result, message string) (decision GuardDecision, texts []string) {
	decision = denyOnly(GuardCauseConfigurationError, []Reason{ReasonConfigurationError})
	if results != nil {
		decision.Pins = auditPinsOf(results)
	}

	const (
		head = "lesson checks cannot be established: "
		tail = "; ask the user to fix .seamark/lessons.yaml"
	)

	// The cut falls inside the message, so the text keeps its tail.
	room := maxDenyReasonBytes - len(head) - len(tail)
	message = render.Sanitize(redact.Secrets(message))

	if len(message) > room {
		message = cutBytes(message, room-len(denyEllipsis)) + denyEllipsis
	}

	texts = []string{head + message + tail}

	return decision, texts
}

// denyInternal returns the deny of a guard that cannot decide: a panic,
// a projection error, or a missing configuration (step 7).
func denyInternal(tool string) (decision GuardDecision, texts []string) {
	subject := "this request"
	if tool != "" {
		subject = "this " + render.Sanitize(tool) + " request"
	}

	decision = denyOnly(GuardCauseInternalError, nil)
	texts = []string{denyText("lesson check guard cannot decide " + subject + ": internal error")}

	return decision, texts
}

// denyTexts returns the deny texts of the denying results, in pin file
// order: one text per finding of a violation and one per undecided path
// of an unknown result. The first maxDenyReasons texts stay, and one
// more text counts the rest, the evidence that a result omits included.
func denyTexts(op Operation, snap *Snapshot, results []Result) []string {
	tool := render.Sanitize(op.Tool)
	if tool == "" {
		tool = "the edit"
	}

	paths := operationPaths(op.Ops)

	var (
		texts   []string
		omitted int
	)

	for _, r := range results {
		switch r.Outcome {
		case OutcomeViolation:
			for _, e := range r.Evidence {
				texts = append(texts, violationText(r, tool, e))
			}

			omitted += r.EvidenceOmitted
		case OutcomeUnknown:
			texts = append(texts, unknownTexts(r, snap, paths)...)
		}
	}

	if len(texts) > maxDenyReasons {
		omitted += len(texts) - maxDenyReasons
		texts = texts[:maxDenyReasons]
	}

	for i := range texts {
		texts[i] = denyText(texts[i])
	}

	if omitted > 0 {
		texts = append(texts, fmt.Sprintf("and %d more", omitted))
	}

	return texts
}

// checkHead names the check and the pin of a result, as each deny text
// of contract K8 starts.
func checkHead(r Result) string {
	return fmt.Sprintf("lesson check %s (%s) of pin %s",
		render.Sanitize(r.Check.ID), render.Sanitize(string(r.Check.Kind)), render.Sanitize(r.PinRef))
}

// violationText returns the deny text of one finding. A pattern finding
// shows the excerpt, which the checker already redacted and sanitized.
// The guard cuts it again to its own bound.
func violationText(r Result, tool string, e Evidence) string {
	// The checker already sanitized and cut the rule (MaxRuleRunes). %q
	// also escapes a quote inside the rule, so the rule text has one end.
	head := fmt.Sprintf("%s %q: %s", checkHead(r), r.Rule, tool)

	if r.Check.Kind == KindNoAddedPattern && e.Line > 0 {
		return fmt.Sprintf("%s adds a line matching the pattern at %s:%d: %s",
			head, evidencePath(e), e.Line, render.Truncate(e.Excerpt, maxDenyExcerptRunes))
	}

	return fmt.Sprintf("%s would change %s", head, evidencePath(e))
}

// unknownTexts returns the deny texts of one unknown result: one per
// undecided path. A result without an undecided path, such as a cut by
// the guard budget, names the operation paths in its scope instead.
func unknownTexts(r Result, snap *Snapshot, paths []string) []string {
	if len(r.UnknownPaths) == 0 {
		subject := sanitizeJoin(scopedPaths(paths, r.Scope), ", ")
		if subject == "" {
			subject = unresolvedSubject
		}

		return []string{fmt.Sprintf("%s cannot evaluate %s: %s", checkHead(r), subject, reasonText(r.Reason, r.Limit))}
	}

	texts := make([]string, 0, len(r.UnknownPaths))

	for _, u := range r.UnknownPaths {
		text := fmt.Sprintf("%s cannot evaluate %s: %s", checkHead(r), evidencePath(u), reasonText(u.Reason, u.limit))

		if note := projectionNote(snap, u); note != "" {
			text += "; " + note
		}

		texts = append(texts, text)
	}

	return texts
}

// scopedPaths returns the paths that a scope holds, in their order.
func scopedPaths(paths, scope []string) []string {
	var out []string

	for _, p := range paths {
		if inScope(p, scope) {
			out = append(out, p)
		}
	}

	return out
}

// projectionNote returns the detail of an unprojectable change, such
// as "old_string occurs 2 times", or "" when the path has none.
func projectionNote(snap *Snapshot, u Evidence) string {
	if u.Reason != ReasonUnprojectableEdit {
		return ""
	}

	for _, ch := range snap.Changes {
		if ch.Path == u.Path && ch.OldPath == u.OldPath {
			return ch.unprojectable
		}
	}

	return ""
}

// reasonText names an unknown reason and, for resource-limit, the limit.
func reasonText(reason Reason, limit string) string {
	if limit == "" {
		return string(reason)
	}

	return string(reason) + " (" + limit + ")"
}

// evidencePath names the path of a finding, with the source of a move.
func evidencePath(e Evidence) string {
	if e.OldPath == "" {
		return render.Sanitize(e.Path)
	}

	return render.Sanitize(e.OldPath) + " -> " + render.Sanitize(e.Path)
}

// denyText sanitizes one deny text and cuts it to maxDenyReasonBytes,
// ellipsis included. The bound is in bytes, because stderr of a hook
// is a byte stream that a client can cut.
func denyText(text string) string {
	text = render.Sanitize(text)

	if len(text) <= maxDenyReasonBytes {
		return text
	}

	return cutBytes(text, maxDenyReasonBytes-len(denyEllipsis)) + denyEllipsis
}
