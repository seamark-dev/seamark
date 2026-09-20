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
	// Root is the canonical workspace root.
	Root string
	// ClientID is the resolved registry ID of the client. It comes from
	// the CLI selection and never from the hook payload, so a payload
	// cannot claim the state of another client.
	ClientID string
	// Event is the decoded native event. Event.Context is the receiving
	// context; nil keeps suppression off.
	Event integration.EditEvent
	// Config is the lessons configuration of the workspace. Nil means
	// the defaults.
	Config *reviews.Config
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
	// context, for example a subagent that reports its parent session.
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
		return out, nil
	}

	lessons, heldBack, err := report.LessonsForFilesBudget(st, cfg, out.Files, report.HookBudget(cfg))
	if err != nil {
		return out, fmt.Errorf("select lessons: %w", err)
	}

	out.HeldBackPins = heldBack

	if len(lessons) == 0 {
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
		out.record(req, suppressed, reviews.DeliverySuppressedRepeat, 0)

		return out, nil
	}

	var advice strings.Builder

	_ = report.PrintEditReminder(&advice, out.Files, selected, heldBack)

	if err := ctx.Err(); err != nil {
		return out, err
	}

	// Emit comes before commit and before the audit record. A failed
	// reply must not mark a lesson delivered, and the client must never
	// wait for a log append.
	if err := emit(advice.String()); err != nil {
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

	out.record(req, selected, reviews.DeliveryInjected, out.ContextBytes)
	out.record(req, suppressed, reviews.DeliverySuppressedRepeat, 0)

	return out, nil
}

// ResetRequest is one context-reset event.
type ResetRequest struct {
	// Root is the canonical workspace root.
	Root string
	// ClientID is the resolved registry ID of the client.
	ClientID string
	// Event is the decoded native reset event.
	Event integration.ResetEvent
}

// Reset starts a new delivery generation for the receiving context of
// the event, so its lessons can be emitted once again. An event that
// names no resettable context resets nothing. Reset never creates
// state: it is a no-op until once-per-context delivery has been used.
func Reset(req ResetRequest) error {
	if req.Event.Context.ID == "" || !req.Event.Context.Resettable {
		return nil
	}

	return reviews.ResetHookDelivery(req.Root, req.Event.Context.ID)
}

// beginLease opens the once-per-context lease when the configuration
// and the event allow suppression. A nil lease means repeated delivery,
// and the Suppression value says why. This function is the only place
// that maps a receiving context to the suppression state.
func beginLease(req Request, cfg *reviews.Config, lessons []model.Lesson) (*reviews.HookDeliveryLease, Suppression) {
	if cfg.HookDelivery() != reviews.HookDeliveryOncePerContext {
		return nil, SuppressionOff
	}

	receiver := req.Event.Context

	switch {
	case receiver == nil || receiver.ID == "":
		return nil, SuppressionNoContext
	case !receiver.Resettable:
		return nil, SuppressionNotResettable
	}

	lease, err := reviews.BeginHookDelivery(req.Root, receiver.ID, lessons)
	if err != nil {
		// The state is an optimization, never permission to hide advice.
		return nil, SuppressionStateUnavailable
	}

	return lease, SuppressionActive
}

// record appends one firing record for the event. It is best effort:
// the result only sets AuditFailed.
func (o *Outcome) record(req Request, lessons []model.Lesson, status reviews.DeliveryStatus, contextBytes int) {
	err := reviews.RecordHookDeliveryFiles(req.Root, o.Files, req.Event.NativeTool, lessons,
		reviews.HookDelivery{
			Status: status, SessionID: req.Event.SessionID, MatchID: req.Event.MatchID,
			Generation: o.Generation, ContextBytes: contextBytes,
		})
	if err != nil {
		o.AuditFailed = true
	}
}
