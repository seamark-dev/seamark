package lessoncheck

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the checker registry and Evaluate. The registry is a
// fixed map in the binary. No plugin and no command can add a checker,
// and every surface calls Evaluate, so no second checker exists.

// Outcome is the result of one checker run on one pin.
type Outcome string

// The closed list of outcomes.
const (
	OutcomePass          Outcome = "pass"
	OutcomeViolation     Outcome = "violation"
	OutcomeNotApplicable Outcome = "not-applicable"
	OutcomeUnknown       Outcome = "unknown"
)

// Listing is a pin state outside the outcome list. Evaluate never runs a
// listed pin, so a listing never implies a pass.
type Listing string

// The closed list of listings.
const (
	// ListingNotCheckable is an advisory pin without a check.
	ListingNotCheckable Listing = "not-checkable"
	// ListingAdvisoryWithCheck is an advisory pin with a check that does
	// not run until the mode changes.
	ListingAdvisoryWithCheck Listing = "advisory-with-check"
)

// Reason tells why an outcome is unknown.
type Reason string

// The closed list of reasons.
const (
	// ReasonUnsupportedContent is binary content, invalid UTF-8, a
	// gitlink, or another non-text type where a check needs text.
	ReasonUnsupportedContent Reason = "unsupported-content"
	// ReasonContentUnavailable is required content that the producer
	// cannot read: a vanished path, a permission error, or a path through
	// a link.
	ReasonContentUnavailable Reason = "content-unavailable"
	// ReasonMissingBaseline is a before state that is absent or was
	// discarded, or a HEAD blob that cannot be read.
	ReasonMissingBaseline Reason = "missing-baseline"
	// ReasonStaleSnapshot is an input that changed between the read and
	// the publication of a result.
	ReasonStaleSnapshot Reason = "stale-snapshot"
	// ReasonIncompletePaths is an affected path set that is not fully
	// known when no named limit caused it.
	ReasonIncompletePaths Reason = "incomplete-paths"
	// ReasonUnparseableInput is a covered hook request that does not
	// decode.
	ReasonUnparseableInput Reason = "unparseable-input"
	// ReasonUnprojectableEdit is a pending edit whose result cannot be
	// computed exactly.
	ReasonUnprojectableEdit Reason = "unprojectable-edit"
	// ReasonResourceLimit is a named limit that an evaluation reached.
	// Result.Limit names it.
	ReasonResourceLimit Reason = "resource-limit"
	// ReasonConfigurationError is a check set that cannot be established:
	// lessons.yaml does not decode, or a pin or a check is invalid.
	ReasonConfigurationError Reason = "configuration-error"
	// ReasonInterrupted is a cancellation, or an interrupted tool or hook,
	// before the evaluation finished.
	ReasonInterrupted Reason = "interrupted"
)

// reasonPrecedence is the order in which a result picks its reason from
// several unknown paths. The first reason in this list that is present
// wins.
var reasonPrecedence = []Reason{
	ReasonConfigurationError,
	ReasonUnparseableInput,
	ReasonIncompletePaths,
	ReasonMissingBaseline,
	ReasonUnprojectableEdit,
	ReasonContentUnavailable,
	ReasonUnsupportedContent,
	ReasonResourceLimit,
	ReasonInterrupted,
	ReasonStaleSnapshot,
}

// ChangeKind is the kind of one change.
type ChangeKind string

// The closed list of change kinds.
const (
	ChangeAdd    ChangeKind = "add"
	ChangeDelete ChangeKind = "delete"
	ChangeModify ChangeKind = "modify"
	ChangeMove   ChangeKind = "move"
)

// Evidence is one finding of a result, or one path that a result leaves
// undecided.
type Evidence struct {
	Path    string     `json:"path"`
	OldPath string     `json:"old_path,omitempty"`
	Change  ChangeKind `json:"change"`
	Line    int        `json:"line,omitempty"`    // 1-based line of the match in the after content
	Excerpt string     `json:"excerpt,omitempty"` // display only: redact.Secrets, render.Sanitize, MaxExcerptRunes; never persisted
	Reason  Reason     `json:"reason,omitempty"`  // for unknown_paths entries only
	// limit names the limit of a resource-limit unknown path. Verify reads
	// it to list a limit that only a checker reaches. It is unexported, so
	// the JSON output does not change.
	limit string
}

// Coverage counts what a result looked at.
type Coverage struct {
	PathsInScope   int `json:"paths_in_scope"`        // change endpoints inside the scope
	PathsEvaluated int `json:"paths_evaluated"`       // endpoints with a decided per-path verdict
	AddedLines     int `json:"added_lines,omitempty"` // no-added-pattern/1: lines matched against the pattern
}

// Result is the outcome of one executable pin on one snapshot.
type Result struct {
	PinRef          string     `json:"pin_ref"`      // ShortPinRef
	PinIdentity     string     `json:"pin_identity"` // 64-hex identity digest
	PinVersion      string     `json:"pin_version"`
	Entry           int        `json:"entry"`
	Rule            string     `json:"rule"`  // render.Sanitize, MaxRuleRunes
	Mode            Mode       `json:"mode"`  // "check" or "enforce"
	Scope           []string   `json:"scope"` // sorted; non-nil; [] = repository-wide
	Check           CheckRef   `json:"check"`
	Outcome         Outcome    `json:"outcome"`
	Reason          Reason     `json:"reason,omitempty"`    // set iff Outcome == unknown
	Limit           string     `json:"limit,omitempty"`     // set iff Reason == resource-limit
	FirstRun        *FirstRun  `json:"first_run,omitempty"` // Verify only: the cause of a first-run unknown that went stale
	Evidence        []Evidence `json:"evidence,omitempty"`
	EvidenceOmitted int        `json:"evidence_omitted,omitempty"`
	UnknownPaths    []Evidence `json:"unknown_paths,omitempty"` // in-scope paths left undecided (also when Outcome == violation)
	Coverage        Coverage   `json:"coverage"`
}

// checker is one entry of the registry. evaluate gets the compiled
// definition, the snapshot, and the pin scope, and it fills the outcome
// part of a Result. Evaluate fills the pin part. The budget bounds the
// work of the checker: an ended budget gives unknown, never a pass.
//
// The registry holds no compile step. Validate compiles each definition
// once into Config.Checks, so a second compile step would only repeat it.
type checker struct {
	evaluate func(budget, Check, *Snapshot, []string) Result
	describe func(reviews.CheckDef) (string, error)
}

// registry holds the two /1 checkers. Validate refuses every other kind.
var registry = map[Kind]checker{
	KindForbidPathChange: {evaluate: evaluateForbid, describe: describeForbid},
	KindNoAddedPattern:   {evaluate: evaluatePattern, describe: describePattern},
}

// Kinds returns the checker kinds of the registry, sorted.
func Kinds() []Kind {
	return slices.Sorted(maps.Keys(registry))
}

// Describe returns the predicate of a check definition as one line of
// text. The CLI and the console show it, because a user must review the
// predicate and not assume that the pin note defines it. It returns an
// error for a definition that no checker can run.
func Describe(def reviews.CheckDef) (string, error) {
	c, ok := registry[Kind(def.Kind)]
	if !ok {
		return "", fmt.Errorf("lessoncheck: unknown checker kind %q", def.Kind)
	}

	return c.describe(def)
}

// Evaluate runs the checker of every executable pin on the snapshot and
// returns one Result per pin, in file order. It never evaluates an
// advisory pin, because an advisory pin runs no check. The slice is
// never nil, so it encodes as [].
//
// On a Config with Errors, Evaluate runs no checker. It returns one
// unknown/configuration-error result per executable pin, because the
// check set cannot be established. It then reads no snapshot, so snap
// can be nil.
//
// Evaluate is pure: it reads only its arguments, changes neither of
// them, and does no I/O.
func Evaluate(cfg *Config, snap *Snapshot) []Result {
	return evaluate(budget{}, cfg, snap)
}

// EvaluateWithin is Evaluate with a time budget. The guard and the
// post-edit verification call it with the context of their budget, and
// limit names that budget (LimitGuardBudget or LimitPostEditBudget).
//
// When ctx ends, every result that no checker finished is
// unknown/resource-limit with limit, or unknown/interrupted for a
// cancellation. So the end of the budget fails closed when an enforce
// pin applies, and it stays advisory for a check pin. When ctx does not
// end, or is nil, the results equal those of Evaluate.
func EvaluateWithin(ctx context.Context, limit string, cfg *Config, snap *Snapshot) []Result {
	return evaluate(budget{ctx: ctx, limit: limit}, cfg, snap)
}

// evaluate is the shared body of Evaluate and EvaluateWithin.
func evaluate(b budget, cfg *Config, snap *Snapshot) []Result {
	results := []Result{}

	for _, pin := range cfg.Pins {
		if pin.Mode.Executable() {
			results = append(results, evaluatePin(b, cfg, snap, pin))
		}
	}

	return results
}

// evaluatePin runs the checker of one executable pin and adds the pin
// fields to its result. A pin that starts after the budget ended runs
// no checker.
func evaluatePin(b budget, cfg *Config, snap *Snapshot, pin Pin) Result {
	var res Result

	check, run, ok := runnable(cfg, pin)

	switch cause, ended := b.ended(); {
	case !ok:
		res = Result{Outcome: OutcomeUnknown, Reason: ReasonConfigurationError, Check: rawCheckRef(cfg.Raw, pin.CheckID)}
	case ended:
		res = Result{Outcome: OutcomeUnknown, Reason: cause.reason, Limit: cause.limit, Check: checkRef(check)}
	default:
		res = run.evaluate(b, check, snap, pin.Scope)
		res.Check = checkRef(check)
	}

	return withPin(res, pin, snap.limits().MaxRuleRunes)
}

// runnable returns the compiled check and the checker of an executable
// pin. ok is false when no checker can run it: the config is invalid,
// or the check is missing or of an unknown kind. Validate guarantees a
// compiled, known check for every executable pin of a valid config. A
// hand-built Config can lack such a check, so a missing check also gives
// configuration-error, never a pass. Evaluate and the binding of
// Revalidate use this one rule, so their checker kinds agree.
func runnable(cfg *Config, pin Pin) (check Check, run checker, ok bool) {
	check, compiled := cfg.Checks[pin.CheckID]
	run, known := registry[check.Kind]

	return check, run, len(cfg.Errors) == 0 && compiled && known
}

// withPin adds the pin fields to the outcome part of a result. The
// post-edit evaluation also builds results without a checker run, for a
// call without a baseline, so the pin fields have one writer.
func withPin(res Result, pin Pin, maxRuleRunes int) Result {
	res.PinRef = pin.Ref
	res.PinIdentity = pin.Identity
	res.PinVersion = pin.Version
	res.Entry = pin.Entry
	res.Rule = ruleText(pin.Rule.Rule, maxRuleRunes)
	res.Mode = pin.Mode
	// The copy keeps the result apart from the Config, so a caller that
	// edits a result never edits the pin. The copy of a non-nil scope is
	// non-nil, so a repository-wide scope encodes as [].
	res.Scope = append([]string{}, pin.Scope...)

	return res
}

// ruleText returns the display form of a pin rule: sanitized and cut to
// maxRunes. The results and the impact block share it, so a rule reads
// the same in both. The guard prints Result.Rule as it is, because
// withPin already cut it.
func ruleText(rule string, maxRunes int) string {
	return render.Truncate(render.Sanitize(rule), maxRunes)
}

// checkRef names the compiled check of a pin. Every result of a runnable
// pin names its check with it, so the ID, the kind, and the version
// always come from one compiled check.
func checkRef(check Check) CheckRef {
	return CheckRef{ID: check.ID, Kind: check.Kind, Version: check.Version}
}

// rawCheckRef names the check of a pin in an invalid config. It reads the
// written definition, because Config.Checks holds no refused definition.
// The kind and the version stay "" when the ID names no definition.
func rawCheckRef(raw *reviews.Config, id string) CheckRef {
	ref := CheckRef{ID: id}

	if raw == nil {
		return ref
	}

	if def, ok := raw.Checks[id]; ok && id != "" {
		ref.Kind = Kind(def.Kind)
		ref.Version = reviews.CheckVersion(def)
	}

	return ref
}

// inScope reports whether a path is in the scope of a pin. An empty scope
// is repository-wide. The test goes in one direction only: a region
// covers the paths below it, and a path never covers a region.
func inScope(path string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}

	for _, region := range scope {
		if reviews.RegionMatches(region, path) {
			return true
		}
	}

	return false
}

// endpointsInScope counts the ends of a change inside the scope. A move
// has two ends, the source and the destination. A change touches the
// scope when it has at least one end inside it.
func endpointsInScope(ch Change, scope []string) int {
	n := 0

	if inScope(ch.Path, scope) {
		n++
	}

	if ch.OldPath != "" && inScope(ch.OldPath, scope) {
		n++
	}

	return n
}

// touching returns the changes that touch the scope, sorted by path and
// then old path. The sort makes the evidence order independent of the
// producer order.
func touching(snap *Snapshot, scope []string) []Change {
	var changes []Change

	for _, ch := range snap.Changes {
		if endpointsInScope(ch, scope) > 0 {
			changes = append(changes, ch)
		}
	}

	sortChanges(changes)

	return changes
}

// reasonHit is one cause of an unknown outcome. limit names the limit of
// a resource-limit reason and is "" for every other reason.
type reasonHit struct {
	reason Reason
	limit  string
}

// changeEvidence returns the evidence entry that names a change. The
// checkers add the line, the reason, or the limit that an entry needs.
func changeEvidence(ch Change) Evidence {
	return Evidence{Path: ch.Path, OldPath: ch.OldPath, Change: ch.Kind}
}

// unknownPath returns the unknown-path entry of a change that cause
// leaves undecided. The entry keeps the limit, so Verify can list it.
func unknownPath(ch Change, cause reasonHit) Evidence {
	e := changeEvidence(ch)
	e.Reason, e.limit = cause.reason, cause.limit

	return e
}

// unknownCauses returns the causes of an unknown outcome: the cause of
// each unknown path in order, then the cause of a path set that is not
// complete. The checkers read the causes from their unknown paths, so
// the two lists cannot differ.
func unknownCauses(unknown []Evidence, snap *Snapshot) []reasonHit {
	var hits []reasonHit

	for _, u := range unknown {
		hits = append(hits, reasonHit{reason: u.Reason, limit: u.limit})
	}

	if !snap.PathsComplete {
		hits = append(hits, incompletePaths(snap))
	}

	return hits
}

// pickReason returns the reason and the limit of an unknown outcome from
// its causes, by reasonPrecedence. For resource-limit, the limit of the
// first cause with that reason wins. hits must not be empty.
func pickReason(hits []reasonHit) (reason Reason, limit string) {
	for _, reason := range reasonPrecedence {
		for _, hit := range hits {
			if hit.reason == reason {
				return hit.reason, hit.limit
			}
		}
	}

	// A reason outside the closed list is a programming error. The first
	// cause still gives an unknown outcome, never a pass.
	return hits[0].reason, hits[0].limit
}

// incompletePaths returns the cause of a path set that is not complete.
// A named limit on the path set gives resource-limit with its name. A
// content limit counts only when it left a path undecided
// (Snapshot.undecidedLimit), because a content limit on another path
// does not cut the path set. Otherwise the cause is incomplete-paths.
func incompletePaths(snap *Snapshot) reasonHit {
	for _, hit := range snap.Limits {
		if !isContentLimit(hit.Name) {
			return reasonHit{reason: ReasonResourceLimit, limit: hit.Name}
		}
	}

	if snap.undecidedLimit != "" {
		return reasonHit{reason: ReasonResourceLimit, limit: snap.undecidedLimit}
	}

	return reasonHit{reason: ReasonIncompletePaths}
}

// unresolvedPaths reports whether the path set of a snapshot holds a
// path that no scope test can place: the set is incomplete without a
// named limit. The guard and the post-edit evaluation share this rule,
// so both apply every pin to such a path set.
func unresolvedPaths(snap *Snapshot) bool {
	return !snap.PathsComplete && incompletePaths(snap).reason == ReasonIncompletePaths
}

// isContentLimit reports whether a limit bounds the content of one path
// instead of the path set.
func isContentLimit(name string) bool {
	switch name {
	case LimitMaxFileBytes, LimitMaxSnapshotBytes, LimitMaxDiffLines, LimitMaxBaselineBytes:
		return true
	default:
		return false
	}
}

// boundEntries keeps the first max entries and counts the rest. The cut
// changes the display only and never the outcome. The evidence of a
// result, the unreadable directories of a snapshot, and the deny texts
// use it.
func boundEntries[T any](all []T, maxEntries int) (kept []T, omitted int) {
	maxEntries = max(maxEntries, 0)

	if len(all) <= maxEntries {
		return all, 0
	}

	return all[:maxEntries], len(all) - maxEntries
}
