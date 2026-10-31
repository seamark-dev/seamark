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

// This file holds exploration delivery (contract L2): after a structured
// read, the lessons of the read file go to the agent that read it. It
// keeps its own dedupe state in reviews/explore_state.go. It never reads
// or writes the edit hook state, so an edit right after a read still gets
// its reminder (AC-26). It runs no check and blocks nothing.

// ExploreRequest is one structured read event to advise on.
type ExploreRequest struct {
	// Root is the canonical workspace root. Path normalization and the
	// exploration state use it.
	Root string
	// IdentityRoot is the root of the firing log, by the rule of
	// Request.IdentityRoot. Empty means Root.
	IdentityRoot string
	// ClientID is the resolved registry ID of the client, from the CLI
	// selection and never from the payload.
	ClientID string
	// Mechanism is the adapter's name for the reply path of the advice.
	Mechanism string
	// Event is the decoded read event. Event.Context is the receiver.
	Event integration.ExploreEvent
	// Config is the lessons configuration of the workspace. Nil means
	// the defaults.
	Config *reviews.Config
	// ConfigErr is the error of the configuration read, as in
	// Request.ConfigErr. The firing log then names the guidance invalid.
	ConfigErr error
	// RecordEvaluations is true when the hook runs with the recording
	// switch on. Only then does the firing log get the evaluation record
	// of a read that emitted nothing (contract L3).
	RecordEvaluations bool
	// EventID returns the event_id of the record. Nil means a new random
	// ID. Tests set it to get fixed records.
	EventID func() string
}

// ExploreSuppression says whether the exploration lease applied, and
// why not when it did not. Every value except ExploreSuppressionActive
// means that every selected item is emitted.
type ExploreSuppression string

// The exploration suppression states. The list is closed. It has no
// "off" value, because hook_delivery configures the edit hook only.
const (
	// ExploreSuppressionActive means the lease selected the items to emit.
	ExploreSuppressionActive ExploreSuppression = "active"
	// ExploreSuppressionNoContext means the event names no receiver.
	ExploreSuppressionNoContext ExploreSuppression = "no-receiving-context"
	// ExploreSuppressionNotResettable means no verified reset reaches the
	// receiver, for example a subagent. Suppressed advice would then stay
	// hidden after a compaction.
	ExploreSuppressionNotResettable ExploreSuppression = "context-not-resettable"
	// ExploreSuppressionStateUnavailable means the exploration state is
	// locked, unreadable, of another version, or refuses an item.
	ExploreSuppressionStateUnavailable ExploreSuppression = "state-unavailable"
	// ExploreSuppressionCommitFailed means the emission succeeds and the
	// state write fails. A later read emits the same advice again.
	ExploreSuppressionCommitFailed ExploreSuppression = "commit-failed"
)

// ExploreOutcome describes one exploration delivery for diagnostics and
// tests. The hook prints none of it.
type ExploreOutcome struct {
	Status Status
	// Files lists the normalized file of the read.
	Files []string
	// Emitted and Suppressed count lessons. HeldBackPins counts the
	// applicable pins that the hook budget left out of the advice.
	Emitted, Suppressed, HeldBackPins int
	// Generation is the exploration generation of the lease, or zero
	// without a lease. It is never the edit hook generation.
	Generation uint64
	// Suppression is empty when no item was selected, because Explore
	// then tries no lease. The closed list has no "off" value.
	Suppression ExploreSuppression
	// ContextBytes is the exact size of the emitted advisory text.
	ContextBytes int
	// AuditFailed is true when the firing-log append failed. The result
	// does not change: the log is best effort.
	AuditFailed bool
}

// Explore runs the exploration delivery for one structured read. st must
// be an open index: Explore does not index, mine, or create a database.
//
// The selection is the one of the edit hook for the same file, under
// the same budget (spec L128). Dedupe applies only to a resettable
// receiver, whatever hook_delivery says, because exploration state is
// separate from the edit hook state (§5 row 88).
//
// The error is an *EmitError when the emit callback fails, and a plain
// error when the lesson lookup fails or ctx ends. Then no item is
// marked delivered. The firing log gets at most one record per event.
func Explore(ctx context.Context, st *store.Store, req ExploreRequest, emit Emit) (ExploreOutcome, error) {
	out := ExploreOutcome{Status: StatusSilent}

	if err := ctx.Err(); err != nil {
		return out, err
	}

	// A structured read names one file (contract L2). The advice header
	// names that file, so an event with another count is not advised on.
	if len(req.Event.Paths) != 1 {
		return out, nil
	}

	cfg := req.Config
	if cfg == nil {
		cfg = reviews.DefaultConfig()
	}

	normalized := NormalizePaths(req.Root, req.Event.CWD, req.Event.Paths)
	out.Files = normalized.Files
	ev := exploreEvent{req: req, cfg: cfg, out: &out, rejected: normalized.RejectedCount}

	if len(out.Files) == 0 {
		ev.record(evaluationRecord(), nil, emissionNotNeeded, evaluationComplete)

		return out, nil
	}

	sel, err := hookSelection(st, cfg, out.Files)
	if err != nil {
		ev.record(evaluationRecord(), nil, emissionNotNeeded, EvaluationFailed)

		return out, fmt.Errorf("select lessons: %w", err)
	}

	out.HeldBackPins = sel.HeldBack

	if len(sel.Lessons) == 0 {
		ev.record(evaluationRecord(), sel.FiringPins(cfg, nil), emissionNotNeeded, evaluationComplete)

		return out, nil
	}

	items := exploreItems(cfg, sel)

	lease, suppression := beginExploreLease(req, items)
	out.Suppression = suppression

	selected, suppressed := items, []reviews.ExploreItem(nil)

	if lease != nil {
		// Close is safe after the explicit Close calls below. The defer
		// releases the lock on every early return.
		defer func() { _ = lease.Close() }()

		selected, suppressed = lease.Inject(), lease.Suppressed()
		out.Generation = lease.Generation()
	}

	out.Suppressed = len(suppressed)

	if len(selected) == 0 {
		// Every item reached this receiver before. Only a lease can empty
		// the selection. Release the lock before the log append, as the
		// emit path below does.
		if lease != nil {
			_ = lease.Close()
		}

		out.Status = StatusSuppressed
		ev.record(evaluationRecord(),
			sel.FiringPins(cfg, explorePinDeliveries(cfg, sel, nil, reviews.PinInjected)),
			emissionNotNeeded, evaluationComplete)

		return out, nil
	}

	lessons := itemLessons(selected)

	var advice strings.Builder

	_ = report.PrintExploreReminder(&advice, out.Files[0], req.Event.Read, lessons)

	if err := ctx.Err(); err != nil {
		return out, err
	}

	// Emit comes before commit and before the audit record. A failed
	// reply must not mark an item delivered, and the client must never
	// wait for a log append.
	if err := emit(advice.String()); err != nil {
		if lease != nil {
			_ = lease.Close()
		}

		ev.record(evaluationRecord(),
			sel.FiringPins(cfg, explorePinDeliveries(cfg, sel, selected, reviews.PinEmitFailed)),
			emissionFailed, evaluationComplete)

		return out, &EmitError{Err: err}
	}

	out.Status = StatusEmitted
	out.Emitted = len(lessons)
	out.ContextBytes = advice.Len()

	if lease != nil {
		// The advice already reached the client. A failed state write
		// means repeated advice later, never a failed hook.
		if err := lease.Commit(); err != nil {
			out.Suppression = ExploreSuppressionCommitFailed
		}

		_ = lease.Close()
	}

	// One record only: the L3 explore rows write no suppressed-repeat
	// record. The pins of the record name the suppressed pins.
	ev.record([]reviews.EventRecord{{Status: reviews.DeliveryInjected, Lessons: lessons, ContextBytes: out.ContextBytes}},
		sel.FiringPins(cfg, explorePinDeliveries(cfg, sel, selected, reviews.PinInjected)),
		emissionEmitted, evaluationComplete)

	return out, nil
}

// explorePinDeliveries returns the delivery of each selected pin of sel,
// in the order of sel.Pins. sentItems holds the items that the event
// sends, and pinDeliveries holds the rule. The match uses the pin version,
// the key of the exploration state.
func explorePinDeliveries(cfg *reviews.Config, sel report.Selection, sentItems []reviews.ExploreItem,
	sent reviews.PinDelivery,
) []reviews.PinDelivery {
	sentPins := make([]string, 0, len(sentItems))

	for _, item := range sentItems {
		if item.Kind == reviews.ExploreItemPin {
			sentPins = append(sentPins, item.ID)
		}
	}

	return pinDeliveries(selectedVersions(cfg, sel), sentPins, sent)
}

// exploreItems maps a selection to the items of the exploration state,
// in the order of sel.Lessons. A pin is keyed by its K2 version, so a
// changed pin reaches the receiver again. A mined lesson is keyed by its
// cluster, because it has no version.
func exploreItems(cfg *reviews.Config, sel report.Selection) []reviews.ExploreItem {
	items := make([]reviews.ExploreItem, 0, len(sel.Lessons))

	for i, sp := range sel.Pins {
		items = append(items, reviews.ExploreItem{
			Kind: reviews.ExploreItemPin, ID: cfg.PinVersion(sp.Pin), Lesson: sel.Lessons[i],
		})
	}

	for _, l := range sel.Mined {
		items = append(items, reviews.ExploreItem{Kind: reviews.ExploreItemLesson, ID: l.ClusterKey, Lesson: l})
	}

	return items
}

// itemLessons returns the lessons of items, in order.
func itemLessons(items []reviews.ExploreItem) []model.Lesson {
	if len(items) == 0 {
		return nil
	}

	lessons := make([]model.Lesson, len(items))
	for i, item := range items {
		lessons[i] = item.Lesson
	}

	return lessons
}

// beginExploreLease opens the exploration lease when the receiver allows
// suppression. A nil lease means that every item is emitted, and the
// suppression value says why. Only a resettable receiver may suppress:
// a subagent gets no verified reset, so a hidden item could stay hidden
// after its compaction. hook_delivery is not read here.
func beginExploreLease(req ExploreRequest, items []reviews.ExploreItem) (*reviews.ExploreLease, ExploreSuppression) {
	receiving := req.Event.Context

	switch {
	case receiving == nil || receiving.ID == "":
		return nil, ExploreSuppressionNoContext
	case !receiving.Resettable:
		return nil, ExploreSuppressionNotResettable
	}

	lease, err := reviews.BeginExploreDelivery(req.Root, deliveryContext(req.ClientID, receiving), items)
	if err != nil {
		// The state is an optimization, never permission to hide advice.
		return nil, ExploreSuppressionStateUnavailable
	}

	return lease, ExploreSuppressionActive
}

// exploreEvent holds the values that every record of one exploration
// event shares.
type exploreEvent struct {
	req      ExploreRequest
	cfg      *reviews.Config
	out      *ExploreOutcome
	rejected int
}

// record appends the one record of an exploration event to the firing
// log. It is best effort: the result only sets AuditFailed. An
// evaluation needs capture opt-in, so without it record writes nothing
// for one (contract L3, explore rows).
func (e exploreEvent) record(records []reviews.EventRecord, pins []reviews.FiringPin, emission, evaluation string) {
	req := e.req
	if records[0].Status == reviews.DeliveryEvaluation && !req.RecordEvaluations {
		return
	}

	// The config came from Root, so the guidance status reads there.
	digest, status := report.GuidanceOf(req.Root, e.cfg, req.ConfigErr)
	read := req.Event.Read

	ev := reviews.AuditEvent{
		Root:      identityRoot(req.IdentityRoot, req.Root),
		Surface:   reviews.ExploreSurface,
		Tool:      req.Event.NativeTool,
		ClientID:  req.ClientID,
		Mechanism: req.Mechanism,
		Files:     e.out.Files,
		SessionID: req.Event.SessionID, MatchID: req.Event.MatchID, Turn: req.Event.Turn,
		Lane:              laneOf(req.Event.Context),
		Records:           records,
		Pins:              pins,
		Emission:          emission,
		Evaluation:        evaluation,
		GuidanceSHA:       digest,
		GuidanceStatus:    status,
		PathsComplete:     e.rejected == 0,
		RejectedPaths:     e.rejected,
		CaptureOptIn:      req.RecordEvaluations,
		ExploreGeneration: e.out.Generation,
		Read:              &read,
	}

	if req.EventID != nil {
		ev.EventID = req.EventID()
	}

	// The log gets the receiver whenever the client names one, as the
	// edit hook record does. It never holds an identity that the client
	// did not report.
	if receiving := req.Event.Context; receiving != nil && receiving.ID != "" {
		resettable := receiving.Resettable
		ev.ReceiverID, ev.Resettable = receiving.ID, &resettable
	}

	if _, err := reviews.RecordEvent(ev); err != nil {
		e.out.AuditFailed = true
	}
}
