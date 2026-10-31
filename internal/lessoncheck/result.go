package lessoncheck

import (
	"errors"
	"fmt"

	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds the verification result lesson-verification/1 and
// the combined verdict of check --lessons. The CLI, the MCP server, and
// the console print the same result, so one type and one exit rule
// exist.

// VerificationSchema is the format name of a verification result.
const VerificationSchema = "lesson-verification/1"

// Caller names the interface that ran a lesson check. The lesson-check
// audit records it. It is not the delivery surface of the lessons audit.
type Caller string

// The closed list of callers.
const (
	CallerCLI  Caller = "cli"
	CallerMCP  Caller = "mcp"
	CallerTUI  Caller = "tui"
	CallerHook Caller = "hook"
)

// known reports whether c is in the closed list of callers. Verify and
// the audit writer both refuse an unknown caller, so they share the one
// list.
func (c Caller) known() bool {
	switch c {
	case CallerCLI, CallerMCP, CallerTUI, CallerHook:
		return true
	default:
		return false
	}
}

// SummaryState is the state of one verification.
type SummaryState string

// The closed list of summary states.
const (
	// SummaryPassed means that at least one executable pin was evaluated
	// and every result is pass or not-applicable.
	SummaryPassed SummaryState = "passed"
	// SummaryNoApplicableChecks means that executable pins exist and
	// every result is not-applicable.
	SummaryNoApplicableChecks SummaryState = "no-applicable-checks"
	// SummaryNoExecutableChecks means that no pin has the mode check or
	// enforce.
	SummaryNoExecutableChecks SummaryState = "no-executable-checks"
	// SummaryIncomplete means no violation, but at least one unknown
	// result, a stale snapshot, or a configuration error.
	SummaryIncomplete SummaryState = "incomplete"
	// SummaryViolation means at least one violation. The unknown results
	// stay in the result, because a violation does not decide them.
	SummaryViolation SummaryState = "violation"
)

// Freshness tells whether the inputs of a result were unchanged when it
// was published.
type Freshness string

// The closed list of freshness values.
const (
	FreshnessFresh Freshness = "fresh"
	FreshnessStale Freshness = "stale"
)

// Binding names the exact inputs of a result. A held result is stale
// when one of them changes.
type Binding struct {
	SnapshotSHA256  string `json:"snapshot_sha256"`
	ConfigSHA256    string `json:"config_sha256"`    // lessons.yaml bytes ("" when absent)
	PinsSHA256      string `json:"pins_sha256"`      // compose("lesson-pins/1", sorted "entry:version" of evaluated pins)
	CheckerVersions []Kind `json:"checker_versions"` // sorted kinds used; non-nil
}

// PinListing is one pin that the verification lists and does not
// evaluate: an advisory pin, with or without a check.
type PinListing struct {
	PinRef     string    `json:"pin_ref"`
	PinVersion string    `json:"pin_version"`
	Entry      int       `json:"entry"`
	Rule       string    `json:"rule"`
	Listing    Listing   `json:"listing"`
	Check      *CheckRef `json:"check,omitempty"` // advisory-with-check only
}

// FirstRun is the cause of an unknown result before the stale rewrite
// made it unknown/stale-snapshot. The cause can name a limit to raise,
// so a race must not hide it.
type FirstRun struct {
	Reason Reason `json:"reason"`
	Limit  string `json:"limit,omitempty"` // set iff Reason == resource-limit
}

// Summary counts the results and the listings of one verification.
type Summary struct {
	State             SummaryState `json:"state"`
	PinsEvaluated     int          `json:"pins_evaluated"`
	Pass              int          `json:"pass"`
	Violation         int          `json:"violation"`
	NotApplicable     int          `json:"not_applicable"`
	Unknown           int          `json:"unknown"`
	NotCheckable      int          `json:"not_checkable"`
	AdvisoryWithCheck int          `json:"advisory_with_check"`
	Changes           int          `json:"changes"`
}

// Verification is the lesson-verification/1 document.
type Verification struct {
	Schema         string    `json:"schema"` // VerificationSchema
	SeamarkVersion string    `json:"seamark_version"`
	CheckedAt      string    `json:"checked_at"` // RFC 3339 UTC, after revalidation
	Caller         Caller    `json:"caller"`
	Boundary       Boundary  `json:"boundary"`
	Baseline       Baseline  `json:"baseline"`
	Selection      Selection `json:"selection"`
	Binding        Binding   `json:"binding"`
	Freshness      Freshness `json:"freshness"`
	Rerun          string    `json:"rerun,omitempty"` // exact command, set unless the state is passed or no-*-checks
	PathsComplete  bool      `json:"paths_complete"`
	// UnreadableDirectories and UnreadableDirectoriesOmitted are the
	// census evidence of the snapshot (Snapshot.UnreadableDirectories).
	// The text output prints them beside the incomplete path set.
	UnreadableDirectories        []string     `json:"unreadable_directories,omitempty"`
	UnreadableDirectoriesOmitted int          `json:"unreadable_directories_omitted,omitempty"`
	Limits                       []LimitHit   `json:"limits,omitempty"`      // the snapshot hits plus the checker hits, sorted by name
	Limitations                  []string     `json:"limitations,omitempty"` // the snapshot-level limitations, sorted
	Config                       ConfigReport `json:"config"`
	Summary                      Summary      `json:"summary"`
	Results                      []Result     `json:"results"`           // non-nil; [] when empty
	Listings                     []PinListing `json:"listings"`          // non-nil; [] when empty
	Changes                      []Change     `json:"changes,omitempty"` // the JSON output only; the text output omits them
	// firstRun holds the pass or not-applicable outcome that a result had
	// before the publication made it unknown, by pin entry. Only the text
	// output shows that outcome, because a script must not read a pass
	// that the publication did not confirm. A first-run unknown keeps its
	// cause in Result.FirstRun.
	firstRun map[int]Outcome
	// statusStderr is the first git status stderr line of the snapshot
	// (Snapshot.statusStderr). The text output prints it as the notice of
	// the limitation git-stderr. It is unexported, so the JSON output
	// does not change.
	statusStderr string
	// auditErr is the error of the verify audit record, or nil. The record
	// never changes the result, so the error stays out of the JSON
	// output. AuditError returns it.
	auditErr error
}

// ErrViolation is the sentinel of a verification with a violation. The
// CLI exits 2 for it, as for a policy block.
var ErrViolation = errors.New("lesson check violation")

// ErrIncomplete is the sentinel of an incomplete verification. The CLI
// exits 1 for it.
var ErrIncomplete = errors.New("lesson checks incomplete")

// ViolationError is the error of a verification with a violation. Its
// text is the exact stderr line without the "seamark: " prefix, which
// cli.Execute adds.
type ViolationError struct {
	Violations int // results with outcome violation
	Evaluated  int // summary.pins_evaluated
}

// Error returns the stderr text.
func (e *ViolationError) Error() string {
	return fmt.Sprintf("%s: %d of %d evaluated pins (see the report)", ErrViolation, e.Violations, e.Evaluated)
}

// Is makes every ViolationError match ErrViolation, so
// errors.Is(err, ErrViolation) stays the one test for exit 2.
func (e *ViolationError) Is(target error) bool { return target == ErrViolation }

// IncompleteError is the error of an incomplete verification. Its text
// is the exact stderr line without the "seamark: " prefix.
type IncompleteError struct {
	Unknown int    // summary.unknown
	Rerun   string // the rerun command of the result
	// Config is the first configuration error of an invalid config, or
	// nil. It replaces the unknown count in the text, because the user
	// must fix the file before a rerun can help.
	Config *ConfigError
}

// Error returns the stderr text. Sanitize removes control characters,
// because a path in the rerun command comes from the file system and
// the text goes to a terminal.
func (e *IncompleteError) Error() string {
	cause := fmt.Sprintf("%d unknown", e.Unknown)
	if e.Config != nil {
		cause = e.Config.Error()
	}

	return render.Sanitize(fmt.Sprintf("%s: %s; rerun %s", ErrIncomplete, cause, e.Rerun))
}

// Is makes every IncompleteError match ErrIncomplete.
func (e *IncompleteError) Is(target error) bool { return target == ErrIncomplete }

// ExitError returns the error that the summary state gives: nil for the
// exit-0 states, a *ViolationError for violation, and an
// *IncompleteError for incomplete. The CLI returns it after it prints
// the report, so lessons --verify and check --lessons print the same
// stderr line.
func (v *Verification) ExitError() error {
	switch state := v.Summary.State; {
	case state.passing():
		return nil
	case state == SummaryViolation:
		return &ViolationError{Violations: v.Summary.Violation, Evaluated: v.Summary.PinsEvaluated}
	}

	// Any other state, also a value outside the closed list, is
	// incomplete. An unknown state must never give exit 0.
	e := &IncompleteError{Unknown: v.Summary.Unknown, Rerun: v.Rerun}
	if !v.Config.Valid && len(v.Config.Errors) > 0 {
		e.Config = &v.Config.Errors[0]
	}

	return e
}

// passing reports whether a summary state gives exit 0: passed,
// no-applicable-checks, or no-executable-checks. ExitError and Combine
// share it, so lessons --verify and check --lessons give the same exit
// code. A state outside the closed list does not pass.
func (s SummaryState) passing() bool {
	switch s {
	case SummaryPassed, SummaryNoApplicableChecks, SummaryNoExecutableChecks:
		return true
	default:
		return false
	}
}

// CombinedState is the closed state of check --lessons: "pass", "fail",
// or "incomplete".
type CombinedState string

// The closed list of combined states. The exit code decides the state.
const (
	CombinedPass       CombinedState = "pass"
	CombinedFail       CombinedState = "fail"
	CombinedIncomplete CombinedState = "incomplete"
)

// The values of Combined.Policy.
const (
	policyPass    = "pass"
	policyBlocked = "blocked"
)

// Combined is the combined verdict of the command policy and the lesson
// checks.
type Combined struct {
	State    CombinedState // by ExitCode: 0 "pass", 2 "fail", 1 "incomplete"
	ExitCode int           // 2 when policyBlocking or lessons is "violation"; else 1 when lessons is "incomplete"; else 0
	Policy   string        // "blocked" iff policyBlocking (gate.Decision.Blocking()), else "pass"
}

// Combine owns the combined state and the exit rule of check --lessons
// for the CLI and the MCP server, so the two surfaces cannot differ. It
// is pure: the caller passes the summary state and
// gate.Decision.Blocking(), because lessoncheck must not import gate.
//
// A summary state outside the closed list gives exit 1. A check whose
// state is not known must never pass.
func Combine(lessons SummaryState, policyBlocking bool) Combined {
	c := Combined{Policy: policyPass}

	if policyBlocking {
		c.Policy = policyBlocked
	}

	switch {
	case policyBlocking || lessons == SummaryViolation:
		c.State, c.ExitCode = CombinedFail, 2
	case lessons.passing():
		c.State, c.ExitCode = CombinedPass, 0
	default:
		c.State, c.ExitCode = CombinedIncomplete, 1
	}

	return c
}
