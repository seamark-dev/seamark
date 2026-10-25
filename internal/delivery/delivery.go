// Package delivery is the shared advisory-delivery transaction of the
// edit hooks. One edit event from any client runs one sequence:
// normalize the paths, select the lessons, lease the once-per-context
// state, emit, commit, and audit. The package owns that order, so a
// client adapter cannot mark advice delivered before the client has it.
//
// The package does not know a native payload. A client adapter decodes
// the event before Deliver and encodes the advice inside the emit
// callback. The package makes no model call and never blocks an edit:
// every failure leaves the advice eligible for a later event.
package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Request is one edit event to advise on.
type Request struct {
	// Root is the canonical workspace root. Path normalization and the
	// suppression state use it.
	Root string
	// IdentityRoot is the root of the firing log: every identity digest
	// hashes over it, and the records go under it (contract K2, identity
	// root). Empty means Root. The CLI passes cli.hookIdentityRoot, so the
	// edit and reset records of one receiver carry one digest.
	IdentityRoot string
	// ClientID is the resolved registry ID of the client. It comes from
	// the CLI selection and never from the hook payload, so a payload
	// cannot claim the state of another client.
	ClientID string
	// Mechanism is the adapter's name for the native path of the advice.
	// The firing log records it beside the client.
	Mechanism string
	// Event is the decoded native event. Event.Context is the receiving
	// context; nil keeps suppression off.
	Event integration.EditEvent
	// Config is the lessons configuration of the workspace. Nil means
	// the defaults.
	Config *reviews.Config
	// ConfigErr is the error of the configuration read. Config then
	// holds the defaults, and the firing log names the guidance invalid.
	ConfigErr error
	// RecordEvaluations is true when the hook runs with the recording
	// switch on. Only then does the firing log get the evaluation records
	// of an event that emitted nothing (contract L3). The CLI reads the
	// switch, because this package reads no environment.
	RecordEvaluations bool
	// EventID is the event_id of the records. Empty means a new random
	// ID. Tests set it to get fixed records.
	EventID string
}

// Emit encodes the advisory text for the client and writes it. Deliver
// calls it at most once, after selection and suppression. An error
// means the client did not get the advice.
type Emit func(advice string) error

// Status is the visible result of one delivery.
type Status string

// The delivery results.
const (
	// StatusSilent means no lesson applies, so Deliver writes nothing.
	StatusSilent Status = "silent"
	// StatusEmitted means the emit callback accepts the advice.
	StatusEmitted Status = "emitted"
	// StatusSuppressed means this receiving context already has every
	// matching lesson, so Deliver writes nothing.
	StatusSuppressed Status = "suppressed"
)

// Suppression says whether once-per-context suppression applied, and
// why not when it did not. Every state except SuppressionActive means
// repeated delivery: the advice is emitted on every matching event.
type Suppression string

// The suppression states.
const (
	// SuppressionOff means the configuration asks for repeated delivery.
	SuppressionOff Suppression = "off"
	// SuppressionActive means the lease selected the lessons to emit.
	SuppressionActive Suppression = "active"
	// SuppressionNoContext means the adapter identified no receiving
	// context, for example an event without a session id, or a client
	// whose adapter reads no receiver yet.
	SuppressionNoContext Suppression = "no-receiving-context"
	// SuppressionNotResettable means no reset event reaches the context.
	// Suppressed advice would then stay hidden after a compaction.
	SuppressionNotResettable Suppression = "context-not-resettable"
	// SuppressionStateUnavailable means the state is locked, corrupt, of
	// an unknown version, or on a platform without file locks.
	SuppressionStateUnavailable Suppression = "state-unavailable"
	// SuppressionCommitFailed means the emission succeeds and the state
	// write fails. A later event emits the same advice again.
	SuppressionCommitFailed Suppression = "commit-failed"
)

// Outcome describes one delivery for diagnostics and tests. The hook
// prints none of it: a hook writes only the native reply.
type Outcome struct {
	Status Status
	// Files lists the normalized files that selection used.
	Files []string
	// Rejected holds the first rejected native paths, and RejectedCount
	// counts all of them. The values are raw client input.
	Rejected      []RejectedPath
	RejectedCount int
	// Emitted and Suppressed count lessons. HeldBackPins counts the
	// applicable pins that the budget left out of the advice.
	Emitted      int
	Suppressed   int
	HeldBackPins int
	// Generation is the context generation of the lease, or zero
	// without a lease.
	Generation uint64
	// ContextBytes is the exact size of the emitted advisory text.
	ContextBytes int
	Suppression  Suppression
	// AuditFailed is true when a firing-log append failed. The delivery
	// result does not change: the log is best effort.
	AuditFailed bool
}

// EmitError wraps an error of the emit callback. The caller can tell a
// failed client reply apart from a failed lesson lookup: the reply
// error is the hook's own failure, and a lookup error must stay quiet.
type EmitError struct {
	Err error
}

func (e *EmitError) Error() string { return e.Err.Error() }

// Unwrap returns the error of the emit callback.
func (e *EmitError) Unwrap() error { return e.Err }

// Deliver runs the delivery sequence for one edit event. st must be an
// open index: Deliver does not index, mine, or create a database.
//
// The error is an *EmitError when the emit callback fails. It is a
// plain error when the lesson lookup fails or ctx ends. In both cases
// Deliver marks no lesson delivered and records no injection.
//
// The firing log gets one event of record version 2 (contract L3). An
// event that emitted nothing gets an evaluation record only when
// req.RecordEvaluations is true, so without capture opt-in the log gets
// the record kinds of the version 1 writer only.
func Deliver(ctx context.Context, st *store.Store, req Request, emit Emit) (Outcome, error) {
	out := Outcome{Status: StatusSilent, Suppression: SuppressionOff}

	if err := ctx.Err(); err != nil {
		return out, err
	}

	cfg := req.Config
	if cfg == nil {
		cfg = reviews.DefaultConfig()
	}

	normalized := NormalizePaths(req.Root, req.Event.CWD, req.Event.Paths)
	out.Files = normalized.Files
	out.Rejected, out.RejectedCount = normalized.Rejected, normalized.RejectedCount

	if len(out.Files) == 0 {
		// Every input path is rejected. An event without a path evaluates
		// nothing, so it gets no record.
		if len(req.Event.Paths) > 0 {
			out.record(req, cfg, evaluationRecord(), nil, emissionNotNeeded, evaluationComplete)
		}

		return out, nil
	}

	sel, err := hookSelection(st, cfg, out.Files)
	if err != nil {
		out.record(req, cfg, evaluationRecord(), nil, emissionNotNeeded, EvaluationFailed)

		return out, fmt.Errorf("select lessons: %w", err)
	}

	lessons := sel.Lessons
	out.HeldBackPins = sel.HeldBack

	if len(lessons) == 0 {
		out.record(req, cfg, evaluationRecord(), sel.FiringPins(cfg, nil), emissionNotNeeded, evaluationComplete)

		return out, nil
	}

	lease, suppression := beginLease(req, cfg, lessons)
	out.Suppression = suppression

	selected, suppressed := lessons, []model.Lesson(nil)

	if lease != nil {
		// Close is safe after the explicit Close calls below. The defer
		// releases the lock on every early return.
		defer func() { _ = lease.Close() }()

		selected, suppressed = lease.Inject(), lease.Suppressed()
		out.Generation = lease.Generation()
	}

	out.Suppressed = len(suppressed)

	if len(selected) == 0 {
		// Only a lease can empty the selection. Release the lock before
		// the log append, as the emit path below does.
		if lease != nil {
			_ = lease.Close()
		}

		out.Status = StatusSuppressed
		out.record(req, cfg,
			[]reviews.EventRecord{{Status: reviews.DeliverySuppressedRepeat, Lessons: suppressed}},
			sel.FiringPins(cfg, pinDeliveries(sel, selected, reviews.PinInjected)),
			emissionNotNeeded, evaluationComplete)

		return out, nil
	}

	var advice strings.Builder

	_ = report.PrintEditReminder(&advice, out.Files, selected, sel.HeldBack)

	if err := ctx.Err(); err != nil {
		return out, err
	}

	// Emit comes before commit and before the audit record. A failed
	// reply must not mark a lesson delivered, and the client must never
	// wait for a log append.
	if err := emit(advice.String()); err != nil {
		// The lease commits nothing, so the advice stays eligible. The
		// lock goes before the log append, as on the emit path below.
		if lease != nil {
			_ = lease.Close()
		}

		out.record(req, cfg, evaluationRecord(),
			sel.FiringPins(cfg, pinDeliveries(sel, selected, reviews.PinEmitFailed)),
			emissionFailed, evaluationComplete)

		return out, &EmitError{Err: err}
	}

	out.Status = StatusEmitted
	out.Emitted = len(selected)
	out.ContextBytes = advice.Len()

	if lease != nil {
		// The advice already reached the client. A failed state write
		// means a repeated reminder later, never a failed hook.
		if err := lease.Commit(); err != nil {
			out.Suppression = SuppressionCommitFailed
		}

		// Release the lock before the log append, so another hook
		// process does not fail open while this one writes the log.
		_ = lease.Close()
	}

	// One event holds both records, so a reader counts the edit once.
	records := []reviews.EventRecord{
		{Status: reviews.DeliveryInjected, Lessons: selected, ContextBytes: out.ContextBytes},
	}
	if len(suppressed) > 0 {
		records = append(records, reviews.EventRecord{Status: reviews.DeliverySuppressedRepeat, Lessons: suppressed})
	}

	out.record(req, cfg, records,
		sel.FiringPins(cfg, pinDeliveries(sel, selected, reviews.PinInjected)),
		emissionEmitted, evaluationComplete)

	return out, nil
}

// hookSelection reads the evidence of files once and folds it with the
// hook budget of cfg. The lessons and the held-back count equal those
// of report.LessonsForFilesBudget, and the selection also names each
// held-back pin for the firing log.
func hookSelection(st *store.Store, cfg *reviews.Config, files []string) (report.Selection, error) {
	// HookSelection refuses a config that ReadEvidence did not read for,
	// so both calls take the same pointer.
	ev, err := report.ReadEvidence(st, files, cfg)
	if err != nil {
		return report.Selection{}, err
	}

	return ev.HookSelection(cfg)
}

// pinDeliveries returns the delivery of each selected pin of sel, in
// the order of sel.Pins. kept holds the lessons that the event sends:
// a pin whose lesson is in kept gets sent, and every other selected
// pin gets suppressed-repeat.
//
// kept is a subsequence of sel.Lessons in the same order, and the pins
// come first in sel.Lessons. One forward walk therefore pairs each kept
// lesson with its own pin. The walk also keeps two pins with one lesson
// apart: the lease keeps the first and suppresses the repeat.
func pinDeliveries(sel report.Selection, kept []model.Lesson, sent reviews.PinDelivery) []reviews.PinDelivery {
	deliveries := make([]reviews.PinDelivery, len(sel.Pins))
	next := 0

	for i := range sel.Pins {
		if next < len(kept) && kept[next] == sel.Lessons[i] {
			deliveries[i] = sent
			next++

			continue
		}

		deliveries[i] = reviews.PinSuppressedRepeat
	}

	return deliveries
}

// ResetRequest is one context-reset event.
type ResetRequest struct {
	// Root is the canonical workspace root of the suppression state.
	Root string
	// IdentityRoot is the root of the firing log, by the rule of
	// Request.IdentityRoot. Empty means Root.
	IdentityRoot string
	// ClientID is the resolved registry ID of the client.
	ClientID string
	// Event is the decoded native reset event.
	Event integration.ResetEvent
	// RecordEvaluations is true when the hook runs with the recording
	// switch on. Only then does the firing log get the reset record.
	RecordEvaluations bool
	// EventID is the event_id of the reset record. Empty means a new
	// random ID.
	EventID string
}

// Reset starts a new delivery generation for the receiving context of
// the event, so its lessons can be emitted once again. An event that
// names no resettable context resets nothing: a missing identity must
// not reset an unrelated context. Reset never creates state: it is a
// no-op until once-per-context delivery has been used.
//
// The reset starts a new generation in the edit hook state and in the
// exploration state (contract L2, Behavior 7). The two resets are
// independent: a failure of one never stops the other, and neither
// creates its file.
//
// With req.RecordEvaluations, the firing log gets one reset record for
// every reset of a named context, also when no state exists yet. A
// reader takes a missing reset record as proof that the context kept
// its earlier advice, so the record must not depend on the edit state.
// The error is the first error of the two state writes; the log is best
// effort.
func Reset(req ResetRequest) error {
	receiving := req.Event.Context
	if !receiving.Resettable || req.ClientID == "" || receiving.ID == "" {
		return nil
	}

	dc := deliveryContext(req.ClientID, &receiving)
	generation, err := reviews.ResetContextGeneration(req.Root, dc)
	exploreGeneration, exploreErr := reviews.ResetExploreDelivery(req.Root, dc)

	if req.RecordEvaluations {
		recordReset(req, generation, exploreGeneration)
	}

	if err != nil {
		return err
	}

	return exploreErr
}

// recordReset appends the reset record of a context (contract L3). A
// generation is zero when its state was not reset, and the record then
// has no key for it.
func recordReset(req ResetRequest, generation, exploreGeneration uint64) {
	receiving := req.Event.Context
	resettable := receiving.Resettable

	_, _ = reviews.RecordEvent(reviews.AuditEvent{
		Root:      identityRoot(req.IdentityRoot, req.Root),
		ClientID:  req.ClientID,
		SessionID: req.Event.SessionID, Turn: req.Event.Turn, ReceiverID: receiving.ID,
		Lane: laneOf(&receiving), Resettable: &resettable,
		Records:           []reviews.EventRecord{{Status: reviews.DeliveryReset}},
		CaptureOptIn:      true,
		Generation:        generation,
		ExploreGeneration: exploreGeneration,
		EventID:           req.EventID,
	})
}

// deliveryContext maps the client and its receiving context to the
// identity of the suppression state and the firing log. The client ID
// is part of the identity, so two clients that report one session
// string never share a state entry. A nil receiving context gives an
// identity that the state functions refuse.
func deliveryContext(clientID string, receiving *integration.ReceivingContext) reviews.DeliveryContext {
	if receiving == nil {
		return reviews.DeliveryContext{ClientID: clientID}
	}

	return reviews.DeliveryContext{ClientID: clientID, ReceiverID: receiving.ID}
}

// beginLease opens the once-per-context lease when the configuration
// and the event allow suppression. A nil lease means repeated delivery,
// and the Suppression value says why. Suppression needs a receiver that
// the client names and can reset. A parent session id is not a receiver:
// it hides advice from a subagent that never got it.
func beginLease(req Request, cfg *reviews.Config, lessons []model.Lesson) (*reviews.HookDeliveryLease, Suppression) {
	if cfg.HookDelivery() != reviews.HookDeliveryOncePerContext {
		return nil, SuppressionOff
	}

	receiving := req.Event.Context

	switch {
	case receiving == nil || receiving.ID == "":
		return nil, SuppressionNoContext
	case !receiving.Resettable:
		return nil, SuppressionNotResettable
	}

	lease, err := reviews.BeginContextDelivery(req.Root, deliveryContext(req.ClientID, receiving), lessons)
	if err != nil {
		// The state is an optimization, never permission to hide advice.
		return nil, SuppressionStateUnavailable
	}

	return lease, SuppressionActive
}

// The emission values of a firing record (contract L3). The list is
// closed; preparation adds "returned".
const (
	emissionEmitted   = "emitted"
	emissionNotNeeded = "not-needed"
	emissionFailed    = "failed"
)

// The evaluation values of a firing record. The list is closed. The
// MCP change_set runner writes EvaluationFailed on the fallback record
// of a failed preparation, so it is exported.
const (
	evaluationComplete = "complete"
	EvaluationFailed   = "failed"
)

// laneUnknown is the lane of a record without a receiving context. The
// adapters name the known lanes (integration.LaneRoot, LaneChild).
const laneUnknown = "unknown"

// evaluationRecord is the one record of an event that emitted nothing.
// Its fired list is empty: the pins of the event say what matched.
func evaluationRecord() []reviews.EventRecord {
	return []reviews.EventRecord{{Status: reviews.DeliveryEvaluation}}
}

// record appends the records of one edit event to the firing log. It
// is best effort: the result only sets AuditFailed. An evaluation needs
// capture opt-in, so without it record writes nothing for one.
func (o *Outcome) record(req Request, cfg *reviews.Config, records []reviews.EventRecord,
	pins []reviews.FiringPin, emission, evaluation string,
) {
	if records[0].Status == reviews.DeliveryEvaluation && !req.RecordEvaluations {
		return
	}

	// The config came from Root, so the guidance status reads there.
	digest, status := report.GuidanceOf(req.Root, cfg, req.ConfigErr)

	ev := reviews.AuditEvent{
		Root:      identityRoot(req.IdentityRoot, req.Root),
		Tool:      req.Event.NativeTool,
		ClientID:  req.ClientID,
		Mechanism: req.Mechanism,
		Files:     o.Files,
		SessionID: req.Event.SessionID, MatchID: req.Event.MatchID, Turn: req.Event.Turn,
		Lane:           laneOf(req.Event.Context),
		Records:        records,
		Pins:           pins,
		Emission:       emission,
		Evaluation:     evaluation,
		GuidanceSHA:    digest,
		GuidanceStatus: status,
		PathsComplete:  o.RejectedCount == 0,
		RejectedPaths:  o.RejectedCount,
		CaptureOptIn:   req.RecordEvaluations,
		Generation:     o.Generation,
		EventID:        req.EventID,
	}

	// The log gets the receiving context whenever the client names one,
	// also when suppression is off. It gets no context otherwise: the log
	// never holds an identity that the client did not report.
	if receiving := req.Event.Context; receiving != nil && receiving.ID != "" {
		resettable := receiving.Resettable
		ev.ReceiverID, ev.Resettable = receiving.ID, &resettable
	}

	if _, err := reviews.RecordEvent(ev); err != nil {
		o.AuditFailed = true
	}
}

// identityRoot returns the root of the firing log: the identity root
// when the caller names one, else the workspace root.
func identityRoot(identity, root string) string {
	if identity != "" {
		return identity
	}

	return root
}

// laneOf returns the lane of a receiving context for the firing log.
// A context without a lane comes from no adapter rule, so its lane is
// unknown too.
func laneOf(receiving *integration.ReceivingContext) string {
	if receiving == nil || receiving.Lane == "" {
		return laneUnknown
	}

	return receiving.Lane
}
