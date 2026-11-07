package lessonreport

// This file holds every type of the delivery/1 document, with the exact
// JSON tags. A section with no evidence to derive it holds an empty value.
// It never holds a default that reads like a result.

// Schema is the format name of the report.
const Schema = "delivery/1"

// Class is the action-level delivery class. The closed list is
// "at-action", "earlier-in-context", "other-lane", "region-not-matched",
// "not-emitted", and "unknown".
type Class string

// PairClass is the class of an occurrence: the six Class values plus
// "mixed".
type PairClass string

// Timing is the action-level decision timing. The closed list is
// "before-construction", "same-call-only", and "unknown".
type Timing string

// PairTiming is the timing of an occurrence: the three Timing values plus
// "mixed".
type PairTiming string

// Origin is the origin value of an occurrence. The closed list is
// "human", "user-unverified", and "peer". The occurrences are a fixed
// field, so no reading of this build sets an origin.
type Origin string

// Code is one reason code. The set is closed. Each file that derives
// reasons declares its own Code values.
type Code string

// Fact is a secondary fact of an action. The closed list is
// "after-correction", "earlier-injection-same-lane",
// "earlier-injection-other-lane", "earlier-exploration-same-lane", and
// "receiver-unknown-preparation".
type Fact string

// EvidenceKind names the kind of an evidence reference. The closed list
// is "same-call", "prior-turn", "model-boundary", "same-turn-earlier",
// "receiver-unknown-surface", and "timestamp-only".
type EvidenceKind string

// NextCode is the code of a next step. The set is closed: nextsteps.go
// declares every value.
type NextCode string

// Report is the delivery/1 document.
type Report struct {
	Schema         string            `json:"schema"`
	SeamarkVersion string            `json:"seamark_version"`
	AsOf           AsOf              `json:"as_of"`
	Capture        CaptureState      `json:"capture"`
	Units          map[string]string `json:"units"` // the unit table, verbatim
	Totals         Totals            `json:"totals"`
	// Pins holds one reading per pin entry of the current config, in file
	// order, one per active pin version.
	Pins []PinReading `json:"pins"`
	// Historical has the shape of Pins. It is a fixed field, so it is
	// always empty in this build.
	Historical []PinReading `json:"historical"`
	Unlinked   Unlinked     `json:"unlinked"`
	Coverage   Coverage     `json:"coverage"`
	// Checks reads the lesson-check stream in units of its own. Its counts
	// never add to a delivery count.
	Checks CheckReading `json:"checks"`
	// Selection holds the budgets of the current lessons.yaml and the
	// evidence state of the audit. A report of an older build has none.
	Selection *SelectionReading `json:"selection,omitempty"`
	Expanded  *Expansion        `json:"expanded,omitempty"` // --pin only
}

// AsOf states what each source was when the report read it.
type AsOf struct {
	GeneratedAt string      `json:"generated_at"`
	Audit       SourceRead  `json:"audit"`       // lessons-audit.jsonl
	CheckAudit  SourceRead  `json:"check_audit"` // lesson-check-audit.jsonl
	Archive     ArchiveRead `json:"archive"`     // a fixed field; see archiveRead
	Config      ConfigRead  `json:"config"`
}

// SourceRead is the state of one audit source.
type SourceRead struct {
	Path        string `json:"path"`        // repository-relative
	State       string `json:"state"`       // closed: "read", "absent", "unreadable", "over-limit"
	Generations int    `json:"generations"` // files read: 0 when absent, 1, or 2 for the lesson-check stream with its rotated generation
	Bytes       int64  `json:"bytes"`       // the prefix read: the size at open
	Records     int    `json:"records"`
	Legacy      int    `json:"legacy"`
	Malformed   int    `json:"malformed"`
	PartialTail bool   `json:"partial_tail"`
	NewerSchema int    `json:"newer_schema"`
	Error       string `json:"error,omitempty"` // sanitized
}

// ArchiveRead is the as_of.archive state, a fixed field. This build
// always gives the state absent and ArchiveNotReadError (archiveRead).
type ArchiveRead struct {
	State         string `json:"state"` // closed: "read", "absent", "older-schema", "newer-schema", "busy", "unreadable"
	SchemaVersion int    `json:"schema_version,omitempty"`
	SnapshotAt    string `json:"snapshot_at,omitempty"`
	Error         string `json:"error,omitempty"`
	// RootEqual is nil when the state is not read, so it is always nil in
	// this build.
	RootEqual *bool `json:"root_equal,omitempty"`
}

// ConfigRead is the state of lessons.yaml.
type ConfigRead struct {
	State          string `json:"state"` // closed: "ok", "absent", "invalid"
	GuidanceSHA256 string `json:"guidance_sha256,omitempty"`
	Pins           int    `json:"pins"`
}

// CaptureState is the capture field, a fixed field. This build always
// gives the state unavailable with the reason no-archive (captureState).
type CaptureState struct {
	State   string `json:"state"`             // closed: "available", "unavailable"
	Reason  string `json:"reason,omitempty"`  // closed: "no-archive", "older-schema", "newer-schema", "busy", "unreadable"
	Message string `json:"message,omitempty"` // "capture evidence unavailable" when unavailable
}

// Totals are the existing delivery totals of the same read
// (reviews.Summarize over the audit prefix lines, plus the v2 counts).
type Totals struct {
	DeliveredRecords      map[string]int `json:"delivered_records"` // by surface: "hook", "explore", "change_set", "prepare", "check"
	SuppressedHookRecords int            `json:"suppressed_hook_records"`
	EvaluationRecords     int            `json:"evaluation_records"`
	ResetRecords          int            `json:"reset_records"`
	Events                int            `json:"events"`                // distinct event_id among v2 records
	DistinctPreparations  int            `json:"distinct_preparations"` // distinct (surface, scope_sha256, guidance_sha256) among change_set and prepare records
}

// PinReading is the reading of one pin version.
type PinReading struct {
	PinRef      string             `json:"pin_ref"` // Config.ShortPinRef of the config entry that the reading comes from
	PinIdentity string             `json:"pin_identity"`
	PinVersion  string             `json:"pin_version"`
	Entry       int                `json:"entry,omitempty"`
	Rule        string             `json:"rule"` // sanitized
	Scope       []string           `json:"scope"`
	Mode        string             `json:"mode"`
	Delivery    DeliveryCounts     `json:"delivery"`
	Occurrences OccurrenceCounts   `json:"occurrences"`
	Timing      map[PairTiming]int `json:"timing"` // unit occurrences
	Observation Observation        `json:"observation"`
	Facts       map[Fact]int       `json:"facts"`       // unit occurrences
	Reasons     map[Code]int       `json:"reasons"`     // unknown action reasons, unit actions
	Limitations []Code             `json:"limitations"` // pin-level codes, sorted
	NextSteps   []NextStep         `json:"next_steps"`
	Checks      PinCheckReading    `json:"checks"` // lesson-check reading per pin version; separate units
}

// DeliveryCounts are the audit counts of one pin version.
type DeliveryCounts struct {
	Records          map[string]int `json:"records"` // v2 records whose pins name this version, by surface; unit records
	Injected         int            `json:"injected"`
	SuppressedRepeat int            `json:"suppressed_repeat"`
	HeldBack         int            `json:"held_back"`
	// HeldBackCauses splits HeldBack by surface, then by cause, in records.
	// Its counts sum to HeldBack. An audit that does not read leaves it absent.
	HeldBackCauses        map[string]map[string]int `json:"held_back_causes,omitempty"`
	EmitFailed            int                       `json:"emit_failed"`
	Returned              int                       `json:"returned"`
	Sessions              int                       `json:"sessions"`                // unit delivered sessions
	LegacyIdentityRecords int                       `json:"legacy_identity_records"` // v1 records naming the identity (PinIdentity, note included); every reading is of the current version; no version, so never diagnosed
}

// SelectionReading states the limits that selection applies now and
// whether the audit counts can be read at all. It feeds no class, no timing,
// and no observation window, so it cannot change a delivery reading.
type SelectionReading struct {
	Budgets  SelectionBudgets  `json:"budgets"`
	Evidence SelectionEvidence `json:"evidence"`
}

// SelectionBudgets holds the three limits of the current lessons.yaml. A
// record stores no budget, so no reader can join a limit to a past count.
type SelectionBudgets struct {
	PinCap       BudgetLimit `json:"pin_cap"`
	HookTotal    BudgetLimit `json:"hook_total"`
	ChangeBudget BudgetLimit `json:"change_budget"`
}

// BudgetLimit is one limit: what the config asks for and what selection
// applies. The two differ when the fixed total clamps the pin cap.
type BudgetLimit struct {
	Key       string   `json:"key,omitempty"`       // the lessons.yaml key; empty for the fixed total
	Source    string   `json:"source"`              // closed: "file", "default", "fixed", "unread"
	Requested int      `json:"requested,omitempty"` // absent for a fixed or unread limit
	Effective int      `json:"effective"`
	Unit      string   `json:"unit"` // pins or lessons per injection, or lessons per answer
	Surfaces  []string `json:"surfaces"`
}

// SelectionEvidence says whether a zero count can be read as a zero.
type SelectionEvidence struct {
	State string `json:"state"` // closed: "read", "unknown"
}

// OccurrenceCounts are the occurrences of one pin version, a fixed
// field. Every count is zero and every map is empty in this build.
type OccurrenceCounts struct {
	Occurrences     int               `json:"occurrences"`    // occurrence keys (canonical feedback, identity, version)
	Incidents       int               `json:"incidents"`      // distinct incident digests among them
	UnknownAction   int               `json:"unknown_action"` // occurrences without a located action; never incidents
	ByClass         map[PairClass]int `json:"by_class"`       // sums to Occurrences
	ByOrigin        map[Origin]int    `json:"by_origin"`      // one count per distinct origin, so it can sum above Occurrences
	Excluded        int               `json:"excluded"`       // newest review excluded; not in Occurrences
	ChangedEvidence int               `json:"changed_evidence"`
	// IndependenceUnresolved means two counted occurrences share a session,
	// incident, or joined anchor component. It is always false in this
	// build.
	IndependenceUnresolved bool `json:"independence_unresolved"`
}

// Observation is the window partition of the delivered sessions of one
// pin version. Every delivered session of this build counts as
// incomplete, so Linked and NoLinkedCorrection stay zero.
type Observation struct {
	Unit               string               `json:"unit"`                 // "delivered sessions"
	Linked             int                  `json:"linked"`               // a counted occurrence has a located action in the window
	NoLinkedCorrection int                  `json:"no_linked_correction"` // qualified negative observation (NoLinkedCorrectionText)
	Incomplete         int                  `json:"incomplete"`           // neither linked nor a qualified negative observation
	IncompleteByReason map[Code]int         `json:"incomplete_by_reason"` // a session can have several reasons
	ByTiming           map[Timing]Partition `json:"by_timing"`            // by decision timing
	Endpoint           string               `json:"endpoint"`             // always ObservationEndpoint
	AssessmentAsOf     string               `json:"assessment_as_of,omitempty"`
}

// Partition is one timing row of an observation.
type Partition struct {
	Linked             int `json:"linked"`
	NoLinkedCorrection int `json:"no_linked_correction"`
	Incomplete         int `json:"incomplete"`
}

// Unlinked is the unlinked section, a fixed field. Count is zero and
// the map and the list are empty in this build.
type Unlinked struct {
	Unit      string       `json:"unit"` // "feedback items"
	Count     int          `json:"count"`
	ByReason  map[Code]int `json:"by_reason"` // unlinked codes only
	NextSteps []NextStep   `json:"next_steps"`
}

// Coverage holds report-level counts, each with its unit in the unit
// table. This build fills UnknownLaneRecords, EventsWithoutPins, and
// DeniedCallInjections from the audit. The other counts are fixed fields
// and stay zero.
type Coverage struct {
	UnmatchedAuditSessions    int `json:"unmatched_audit_sessions"`   // always 0 in this build
	UnmatchedArchiveSessions  int `json:"unmatched_archive_sessions"` // always 0 in this build
	UnknownLaneRecords        int `json:"unknown_lane_records"`
	UnknownTimingActions      int `json:"unknown_timing_actions"`
	MissingManifests          int `json:"missing_manifests"`      // always 0 in this build
	IncompleteComparisons     int `json:"incomplete_comparisons"` // always 0 in this build
	UnsupportedOldAssessments int `json:"unsupported_old_assessments"`
	MissingActions            int `json:"missing_actions"`             // occurrences without a located action
	AmbiguousPeers            int `json:"ambiguous_peers"`             // always 0 in this build
	SourceDeletedAssociations int `json:"source_deleted_associations"` // always 0 in this build
	EventsWithoutPins         int `json:"events_without_pins"`
	DeniedCallInjections      int `json:"denied_call_injections"` // injected hook records on a call the guard denied (equal match_sha256); kept out of delivered sessions and windows
	CollapsedCopies           int `json:"collapsed_copies"`       // always 0 in this build
	RootMismatchSessions      int `json:"root_mismatch_sessions"` // always 0 in this build
}

// NextStep is one suggestion with a fixed text. Nothing runs it.
type NextStep struct {
	Code    NextCode `json:"code"`
	Text    string   `json:"text"`              // the fixed sentence of its NextCode, refs filled
	Command string   `json:"command,omitempty"` // exact shipped command; never executed
	Scope   string   `json:"scope"`             // closed: "pin", "repository"
}

// CheckReading is the lesson-check stream reading. Its units are check
// operations, pin evaluations, and guard attempts.
type CheckReading struct {
	State               string         `json:"state"`      // as SourceRead.State
	Operations          map[string]int `json:"operations"` // record kinds verify, post-edit, guard
	PinEvaluations      int            `json:"pin_evaluations"`
	Outcomes            map[string]int `json:"outcomes"`          // check outcomes
	UnknownByReason     map[string]int `json:"unknown_by_reason"` // check Reason values
	GuardAttempts       GuardAttempts  `json:"guard_attempts"`
	InactiveEnforcement int            `json:"inactive_enforcement"` // pins with the requested-inactive badge; 0 without the capability input
}

// GuardAttempts separates a requested deny, verified native blocking, and
// the observed tool execution. The report derives the observation through
// match_sha256, because the guard writes its record before the tool would
// run.
type GuardAttempts struct {
	Allowed             int `json:"allowed"`
	RequestedDenies     int `json:"requested_denies"` // decision deny
	VerifiedNative      int `json:"verified_native"`  // of those, native_blocking "verified"
	ReplyNotWritten     int `json:"reply_not_written"`
	ExecutedAfterDeny   int `json:"executed_after_deny"`   // a later post-edit record has the deny's match_sha256: missing enforcement coverage
	NoExecutionObserved int `json:"no_execution_observed"` // no post-edit record for the match
	Prevented           int `json:"prevented"`             // reply_written and native_blocking "verified"; never more than VerifiedNative
}

// PinCheckReading is the lesson-check reading of one pin version.
type PinCheckReading struct {
	PinEvaluations  int            `json:"pin_evaluations"`
	Outcomes        map[string]int `json:"outcomes"`
	RequestedDenies int            `json:"requested_denies"`
}

// Expansion is the --pin detail of one pin identity.
type Expansion struct {
	PinRef      string             `json:"pin_ref"`
	Versions    []string           `json:"versions"` // every version of this identity in the snapshot, newest config first; never pooled
	Occurrences []OccurrenceDetail `json:"occurrences"`
	Sessions    []SessionWindow    `json:"sessions"`
}

// OccurrenceDetail is one occurrence of the expansion. The occurrences
// are a fixed field, so Expansion.Occurrences is always empty in this
// build.
type OccurrenceDetail struct {
	AssociationRef string         `json:"association_ref"` // "as-" + 12 hex
	PinVersion     string         `json:"pin_version"`
	EpisodeRef     string         `json:"episode_ref"` // 12 hex + "@" + revision
	Family         string         `json:"family"`      // closed: "user", "peer"
	Origin         Origin         `json:"origin"`
	Origins        []Origin       `json:"origins,omitempty"` // every distinct origin, sorted, when the members differ; absent otherwise, so one origin keeps its bytes
	Assessment     string         `json:"assessment"`        // prompt version of the plan
	Review         string         `json:"review"`            // closed: "counted", "excluded", "changed-evidence"
	Class          PairClass      `json:"class"`
	Reason         Code           `json:"reason,omitempty"` // set when class is unknown
	Timing         PairTiming     `json:"decision_timing"`
	Facts          []Fact         `json:"facts"`
	Actions        []ActionDetail `json:"actions"`
	Limitations    []Code         `json:"limitations"`
	Inspect        []string       `json:"inspect"` // exact commands
}

// ActionDetail is one action of an occurrence.
type ActionDetail struct {
	ActionRef    string        `json:"action_ref"` // 12 hex of the observation source key
	Incident     string        `json:"incident"`   // 12 hex of MatchDigest(session.root, native_session_id, tool_use_id)
	Lane         string        `json:"lane"`       // closed: "root", "child", "unknown"
	Class        Class         `json:"class"`
	Reason       Code          `json:"reason,omitempty"`
	Cause        string        `json:"cause,omitempty"` // not-emitted: "restated", "over-pin-cap", "over-total", "emit-failed"
	Secondary    []Fact        `json:"secondary"`       // other established facts
	Timing       Timing        `json:"decision_timing"`
	TimingReason Code          `json:"timing_reason,omitempty"`
	Evidence     []EvidenceRef `json:"evidence"`
	Paths        []string      `json:"paths,omitempty"` // region-not-matched hint, from the action's record
}

// EvidenceRef points at one audit record that supports a reading.
type EvidenceRef struct {
	Kind    EvidenceKind `json:"kind"`
	Surface string       `json:"surface"` // "hook", "explore", "change_set", "prepare"
	Record  string       `json:"record"`  // "lessons-audit.jsonl:<line>"
	Limit   Code         `json:"limitation,omitempty"`
}

// SessionWindow is the observation window of one delivered session.
type SessionWindow struct {
	SessionRef  string `json:"session_ref"` // 12 hex of session_sha256
	PinVersion  string `json:"pin_version"` // the pin version whose delivery opened the window
	Lane        string `json:"lane"`
	Partition   string `json:"partition"` // closed: "linked", "no-linked-correction", "incomplete"
	Timing      Timing `json:"decision_timing"`
	Reasons     []Code `json:"reasons"`
	StartRecord string `json:"start_record,omitempty"` // the opening injection
	Endpoint    string `json:"endpoint,omitempty"`     // ObservationEndpoint when reached; never set in this build
}

// unitTable returns the units map of the report, verbatim. Each call
// returns a new map, so no Report shares a map with another Report.
func unitTable() map[string]string {
	return map[string]string{
		"records":            "lines of .seamark/lessons-audit.jsonl; one edit can write two",
		"events":             "surface invocations (distinct event_id)",
		"sessions":           "distinct captured or audited sessions (session_sha256)",
		"delivered sessions": "sessions with at least one injected record (hook or explore) of the pin version, not counting injections on calls the guard denied",
		"occurrences":        "assessed feedback occurrences: one feedback item and one pin version (association key; known copies of one feedback count once); by_origin counts each distinct origin of an occurrence once, so one occurrence can count under two origins",
		"incidents":          "distinct corrected actions (session and tool-use identity)",
		"actions":            "cited corrected actions",
		"feedback items":     "captured feedback (episode keys)",
		"check operations":   "lesson-check-audit.jsonl records of kind verify, post-edit, guard, in the retained generations (the live file and one rotated file)",
		"pin evaluations":    "executable pins evaluated inside check operations",
		"guard attempts":     "pre-write guard decisions; a requested deny is not a prevented write: it is prevented only with a written deny reply, no post-edit record for its tool call, and a verified native deny path; never an edit or a correction",
	}
}
