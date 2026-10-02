package distill

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// GroupStatus is the result class of one planned group.
type GroupStatus int

const (
	// GroupUnattempted means the group never started: a stop, a
	// conflict, or an earlier store failure ended the run first.
	GroupUnattempted GroupStatus = iota
	// GroupCompleted means the reply was valid and its proposals and
	// signature mark were committed, with zero to five proposals.
	GroupCompleted
	// GroupFailed means the group is not marked; a later plan selects
	// it again. GroupOutcome.Failure says why.
	GroupFailed
	// GroupCancelled means a stop ended the group before or during its
	// agent call. The group is not marked, and the run stops with
	// StopCancelled.
	GroupCancelled
	// GroupSkipped means another writer recorded the group, so this run
	// stored nothing for it.
	GroupSkipped
)

// String returns a stable lower-case name for the status.
func (s GroupStatus) String() string {
	switch s {
	case GroupUnattempted:
		return "unattempted"
	case GroupCompleted:
		return "completed"
	case GroupFailed:
		return "failed"
	case GroupCancelled:
		return "cancelled"
	case GroupSkipped:
		return "skipped"
	default:
		return "GroupStatus(" + strconv.Itoa(int(s)) + ")"
	}
}

// FailureKind classifies a failed group, so a surface can show a
// distinct and actionable outcome for each cause.
type FailureKind int

const (
	// FailNone is the kind of every group that did not fail.
	FailNone FailureKind = iota
	// FailAgent means the invoker returned an error while the run was
	// live: a missing login, a rate limit, or an exit status.
	FailAgent
	// FailTimeout means the per-group time limit ended the call.
	FailTimeout
	// FailInvalidReply means the reply was not the requested JSON.
	FailInvalidReply
	// FailStore means a store read or write failed after the reply. The
	// run stops, because later groups would build on unsaved patterns.
	FailStore
)

// String returns a stable lower-case name for the kind.
func (k FailureKind) String() string {
	switch k {
	case FailNone:
		return "none"
	case FailAgent:
		return "agent"
	case FailTimeout:
		return "timeout"
	case FailInvalidReply:
		return "invalid reply"
	case FailStore:
		return "store"
	default:
		return "FailureKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Duplicate is one returned pattern that restates a known one. The
// executor drops it before persistence.
type Duplicate struct {
	// Rule is the dropped pattern's label.
	Rule string
	// Of is the label of the known pattern it restates.
	Of string
}

// GroupOutcome is what happened to one planned group.
type GroupOutcome struct {
	// Plan is the group's preflight entry.
	Plan    GroupPlan
	Status  GroupStatus
	Failure FailureKind
	// Err is the cause of a failed, cancelled, or skipped group. It can
	// hold agent output, so a surface must sanitize and redact it.
	Err error
	// Proposals are the persisted rows with their IDs.
	Proposals  []model.Proposal
	Duplicates []Duplicate
	// PromptChars and ReplyChars meter the agent traffic. They stay 0
	// when no agent call started for the group.
	PromptChars int
	ReplyChars  int
	// Started and Finished bound the group's work. Both are zero for an
	// unattempted group.
	Started  time.Time
	Finished time.Time
}

// EventKind names one progress event.
type EventKind int

const (
	// EventCleanupDone follows the stale-proposal cleanup.
	EventCleanupDone EventKind = iota
	// EventGroupStarted precedes the agent call of one group.
	EventGroupStarted
	// EventDuplicate reports one dropped restatement.
	EventDuplicate
	// EventGroupFinished reports a group outcome.
	EventGroupFinished
	// EventStopped reports that no further group runs.
	EventStopped
)

// String returns a stable lower-case name for the kind.
func (k EventKind) String() string {
	switch k {
	case EventCleanupDone:
		return "cleanup done"
	case EventGroupStarted:
		return "group started"
	case EventDuplicate:
		return "duplicate"
	case EventGroupFinished:
		return "group finished"
	case EventStopped:
		return "stopped"
	default:
		return "EventKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Event is one typed progress report. The pointers refer to copies, so
// a receiver on another goroutine never shares memory with the run.
type Event struct {
	Kind EventKind
	// Index is the 0-based position in Plan.Groups. For EventStopped it
	// is the first group that did not run.
	Index int
	// Total is len(Plan.Groups), a known denominator for progress.
	Total int
	// Group is set on EventGroupStarted.
	Group *GroupPlan
	// Outcome is set on EventGroupFinished.
	Outcome *GroupOutcome
	// Dup is set on EventDuplicate.
	Dup *Duplicate
	// Removed is set on EventCleanupDone.
	Removed int
	// Stop and Err are set on EventStopped.
	Stop StopReason
	Err  error
}

// ExecuteOptions tune one execution.
type ExecuteOptions struct {
	// Progress receives events synchronously on the executing goroutine,
	// never while a transaction is open. A slow receiver delays the next
	// group, not the current agent call. Nil discards the events.
	Progress func(Event)
	// Revalidate runs before the cleanup and before each group. A
	// non-nil error stops the run with StopConflict. A service passes
	// its file and workspace checks here. Nil checks nothing.
	Revalidate func() error
	// Boundary turns on the store checks: the full plan revalidation
	// before the cleanup, the per-group checks before each group, and
	// the guarded save that refuses a group another writer recorded.
	Boundary bool
	// SkipCleanup leaves the planned cleanup undone. The legacy Run
	// sets it, because it already pruned during planning.
	SkipCleanup bool
}

// StopReason says why an execution ended before its last group.
type StopReason int

const (
	// StopNone means every planned group had its turn and no stop
	// cancelled one. A stop that comes after the last reply prevents no
	// work, so the run keeps StopNone.
	StopNone StopReason = iota
	// StopCancelled means the context ended before the cleanup, before
	// a group, or during a group. The position of the group does not
	// matter: a stop in the last group also gives StopCancelled.
	StopCancelled
	// StopConflict means an input changed; StopErr wraps ErrStale.
	StopConflict
	// StopStoreError means a store read or write failed.
	StopStoreError
)

// String returns a stable lower-case name for the reason.
func (r StopReason) String() string {
	switch r {
	case StopNone:
		return "none"
	case StopCancelled:
		return "cancelled"
	case StopConflict:
		return "conflict"
	case StopStoreError:
		return "store error"
	default:
		return "StopReason(" + strconv.Itoa(int(r)) + ")"
	}
}

// Execution is the typed result of one execution.
type Execution struct {
	// Groups are the outcomes, parallel to Plan.Groups.
	Groups []GroupOutcome
	// CleanupIDs are the planned cleanup rows.
	CleanupIDs []int64
	// Removed counts the rows the cleanup actually deleted. A row that
	// another writer decided meanwhile stays, so it can be lower.
	Removed int
	Stop    StopReason
	StopErr error
	// Result holds the legacy counters, derived from the outcomes.
	Result Result
}

// ErrNoInvoker reports a plan with groups but no invoker to run them.
var ErrNoInvoker = errors.New("distill: the plan has groups but no agent invoker")

// Execute runs exactly the planned cleanup and groups. It checks ctx
// before the cleanup and before each group, and it starts no agent call
// after a stop. A stop that cancels a group, the last one included,
// ends the run with StopCancelled and EventStopped. A reply that
// returns before the stop is persisted and counted as completed, never
// also as cancelled.
//
// It always returns a non-nil *Execution. The error is non-nil only for
// a store failure or an unusable call (no plan, no invoker); a stop or a
// conflict is data in Execution.Stop, not an error.
func Execute(ctx context.Context, st *store.Store, inv agent.Invoker, p *Plan, opts ExecuteOptions) (*Execution, error) {
	start := time.Now()

	if p == nil {
		return &Execution{}, errors.New("distill: no plan to execute")
	}

	// NewPlan keeps the two lists parallel. A plan built another way
	// could not report its outcomes, so it is refused before any work.
	if len(p.Preflight.Groups) != len(p.Groups) {
		return &Execution{}, errors.New("distill: the plan's preflight does not match its groups")
	}

	x := &Execution{Groups: make([]GroupOutcome, len(p.Groups))}

	for i := range p.Groups {
		x.Groups[i] = GroupOutcome{Plan: p.Preflight.Groups[i], Status: GroupUnattempted}
	}

	for _, c := range p.Cleanup {
		x.CleanupIDs = append(x.CleanupIDs, c.ID)
	}

	e := &executor{ctx: ctx, st: st, inv: inv, plan: p, opts: opts, x: x, known: p.known.clone()}

	err := e.run()

	x.Result = x.tally(p)
	x.Result.Duration = time.Since(start)

	return x, err
}

// executor holds the state of one Execute call.
type executor struct {
	ctx   context.Context
	st    *store.Store
	inv   agent.Invoker
	plan  *Plan
	opts  ExecuteOptions
	x     *Execution
	known *Known
}

// run performs the cleanup and the group loop. It returns an error only
// for a store failure or a missing invoker.
func (e *executor) run() error {
	if len(e.plan.Groups) > 0 && e.inv == nil {
		return ErrNoInvoker
	}

	if reason, err := e.startCheck(); reason != StopNone {
		e.stop(0, reason, err)

		return storeError(reason, err)
	}

	if !e.opts.SkipCleanup && len(e.x.CleanupIDs) > 0 {
		removed, err := e.st.DeletePendingProposals(e.x.CleanupIDs)
		if err != nil {
			e.stop(0, StopStoreError, err)

			return err
		}

		e.x.Removed = removed
		e.emit(Event{Kind: EventCleanupDone, Removed: removed})
	}

	for i := range e.plan.Groups {
		reason, skip, err := e.groupCheck(i)
		if reason != StopNone {
			if skip {
				e.x.Groups[i].Status, e.x.Groups[i].Err = GroupSkipped, err
			}

			e.stop(i, reason, err)

			return storeError(reason, err)
		}

		if err := e.runGroup(i); err != nil {
			return err
		}

		// runGroup records a cancel or a conflict in its own group, so
		// the run ends here and never also at the next check.
		if e.x.Stop != StopNone {
			return nil
		}
	}

	return nil
}

// startCheck runs before the cleanup: the context, the full store
// revalidation, then the caller's checks. The context is checked again
// after the slower checks, because the cleanup is a durable write that
// must not start after a stop.
func (e *executor) startCheck() (StopReason, error) {
	if err := e.ctx.Err(); err != nil {
		return StopCancelled, err
	}

	if e.opts.Boundary {
		if err := e.plan.Revalidate(e.st); err != nil {
			return classify(err), err
		}
	}

	if e.opts.Revalidate != nil {
		if err := e.opts.Revalidate(); err != nil {
			return StopConflict, err
		}
	}

	if err := e.ctx.Err(); err != nil {
		return StopCancelled, err
	}

	return StopNone, nil
}

// groupCheck runs before group i. The context is checked again after
// the slower checks, so a stop during them still starts no call.
func (e *executor) groupCheck(i int) (reason StopReason, skip bool, err error) {
	if err := e.ctx.Err(); err != nil {
		return StopCancelled, false, err
	}

	if e.opts.Revalidate != nil {
		if err := e.opts.Revalidate(); err != nil {
			return StopConflict, false, err
		}
	}

	if e.opts.Boundary {
		if skip, err := checkGroup(e.st, e.plan.Groups[i]); err != nil {
			return classify(err), skip, err
		}
	}

	if err := e.ctx.Err(); err != nil {
		return StopCancelled, false, err
	}

	return StopNone, false, nil
}

// runGroup sends group i to the agent and persists the valid reply. It
// returns an error only for a store failure, which also stops the run.
func (e *executor) runGroup(i int) error {
	g := e.plan.Groups[i].Group
	out := &e.x.Groups[i]

	started := out.Plan
	e.emit(Event{Kind: EventGroupStarted, Index: i, Group: &started})

	out.Started = time.Now()

	// A stop can arrive while the receiver handles the start event. The
	// call then never starts, so the group meters no traffic.
	if err := e.ctx.Err(); err != nil {
		e.cancelGroup(i, err)

		return nil
	}

	prompt := buildPrompt(g, e.known.Labels(g.Region, maxKnownLabels))

	// The per-group limit bounds one call. Its expiry is read before
	// cancel, which would otherwise overwrite it with Canceled.
	callCtx, cancel := context.WithTimeout(e.ctx, perGroupTimeout)
	reply, err := e.inv.Invoke(callCtx, prompt)
	timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded)

	cancel()

	// Failed calls count too: the traffic was paid for.
	out.PromptChars, out.ReplyChars = len(prompt), len(reply)

	if err != nil {
		// An ended parent context means the stop ended the call, so the
		// group is cancelled, not failed.
		if e.ctx.Err() != nil {
			e.cancelGroup(i, err)

			return nil
		}

		out.Status, out.Failure, out.Err = GroupFailed, FailAgent, err

		if timedOut {
			out.Failure = FailTimeout
		}

		e.finish(i)

		return nil
	}

	proposals, err := parseReply(reply, g, e.inv.Name())
	if err != nil {
		out.Status, out.Failure, out.Err = GroupFailed, FailInvalidReply, err
		e.finish(i)

		return nil
	}

	fresh := e.dropRestatements(i, proposals)

	if err := e.narrowTriggers(g, fresh); err != nil {
		return e.storeFailure(i, err)
	}

	saved, err := e.save(g, fresh)

	switch {
	case errors.Is(err, store.ErrAlreadyDistilled):
		out.Status = GroupSkipped
		out.Err = fmt.Errorf("%w: group %s was distilled by another writer during the call", ErrStale, g.Signature)
		e.finish(i)
		e.stop(i+1, StopConflict, out.Err)

		return nil
	case err != nil:
		return e.storeFailure(i, err)
	}

	out.Status, out.Proposals = GroupCompleted, saved
	e.finish(i)

	return nil
}

// dropRestatements removes the patterns that restate a known one.
// Groups are read independently, so a repo-wide mistake surfaces in
// several of them; without this the pin file fills with the same
// lesson under new names.
func (e *executor) dropRestatements(i int, proposals []model.Proposal) []model.Proposal {
	out := &e.x.Groups[i]
	fresh := proposals[:0]

	for _, p := range proposals {
		if of, dup := e.known.Restated(p); dup {
			d := Duplicate{Rule: p.Rule, Of: of}
			out.Duplicates = append(out.Duplicates, d)
			e.emit(Event{Kind: EventDuplicate, Index: i, Dup: &d})

			continue
		}

		// Known immediately, so two groups in one run cannot both
		// propose it either.
		e.known.Add(p)
		fresh = append(fresh, p)
	}

	return fresh
}

// narrowTriggers runs the remaining trigger rungs: existence in the
// working tree, then direct-evidence or co-change confirmation, which
// may replace broad evidence coverage with a precise delivery scope.
func (e *executor) narrowTriggers(g Group, fresh []model.Proposal) error {
	for i := range fresh {
		p := &fresh[i]

		// Every v5 proposal carries an explicit trigger answer. The
		// stamp keeps the backfill from re-purchasing it.
		p.TriggerChecked = time.Now().Unix()
		p.TriggerPromptVersion = TriggerPromptVersion

		p.TriggerPaths = validateTriggerPaths(e.plan.Options.Root, p.TriggerPaths)
		if len(p.TriggerPaths) == 0 {
			continue
		}

		var cited []model.Finding

		for _, id := range p.Members {
			if f, ok := memberFinding(g, id); ok {
				cited = append(cited, f)
			}
		}

		regions, _, err := RecomputeRegions(e.st, e.plan.Options.Root, *p, cited)
		if err != nil {
			return err
		}

		p.Regions = regions
		p.Region = ""

		if len(regions) > 0 {
			p.Region = regions[0]
		}
	}

	return nil
}

// save persists the proposals and the signature mark in one
// transaction. Partial persistence would either duplicate proposals on
// the retry or silently discard what a paid agent call found. With
// Boundary on, the save refuses a group another writer recorded.
func (e *executor) save(g Group, fresh []model.Proposal) ([]model.Proposal, error) {
	at := time.Now().Unix()

	if e.opts.Boundary {
		return e.st.SaveDistilledGroupIfNew(g.Signature, g.Region, at, fresh)
	}

	return e.st.SaveDistilledGroup(g.Signature, g.Region, at, fresh)
}

// cancelGroup records that a stop ended group i and stops the run. The
// stop is recorded here and not at the next group check, because the
// last planned group has no next check. err is the cause for the group;
// the run's stop error is the context error, as at a group check.
func (e *executor) cancelGroup(i int, err error) {
	out := &e.x.Groups[i]
	out.Status, out.Err = GroupCancelled, err

	e.finish(i)
	e.stop(i+1, StopCancelled, e.ctx.Err())
}

// storeFailure records a store failure of group i and stops the run.
// Earlier groups were saved and marked, and the result says so.
func (e *executor) storeFailure(i int, err error) error {
	out := &e.x.Groups[i]
	out.Status, out.Failure, out.Err = GroupFailed, FailStore, err

	e.finish(i)
	e.stop(i+1, StopStoreError, err)

	return err
}

// finish stamps the end of group i and reports its outcome.
func (e *executor) finish(i int) {
	e.x.Groups[i].Finished = time.Now()

	outcome := e.x.Groups[i]
	e.emit(Event{Kind: EventGroupFinished, Index: i, Outcome: &outcome})
}

// stop records why the run ends and reports it. The groups from index
// on keep their current status, unattempted unless set otherwise.
func (e *executor) stop(index int, reason StopReason, err error) {
	e.x.Stop, e.x.StopErr = reason, err
	e.emit(Event{Kind: EventStopped, Index: index, Stop: reason, Err: err})
}

// emit sends one event to the progress receiver, if any.
func (e *executor) emit(ev Event) {
	if e.opts.Progress == nil {
		return
	}

	ev.Total = len(e.plan.Groups)
	e.opts.Progress(ev)
}

// classify maps a check error to its stop reason: a stale input is a
// conflict, and anything else is a store failure.
func classify(err error) StopReason {
	if errors.Is(err, ErrStale) {
		return StopConflict
	}

	return StopStoreError
}

// storeError returns err for a store failure and nil for other stops,
// which Execution reports as data.
func storeError(reason StopReason, err error) error {
	if reason == StopStoreError {
		return err
	}

	return nil
}

// tally derives the legacy counters from the outcomes.
func (x *Execution) tally(p *Plan) Result {
	res := Result{
		GroupsTotal:   p.Total,
		GroupsSkipped: len(p.Skipped),
		GroupsPending: len(p.Deferred),
		PrunedStale:   x.Removed,
	}

	for _, o := range x.Groups {
		res.PromptChars += o.PromptChars
		res.ReplyChars += o.ReplyChars
		res.Duplicates += len(o.Duplicates)

		switch o.Status {
		case GroupCompleted:
			res.GroupsRead++
			res.Proposals = append(res.Proposals, o.Proposals...)
		case GroupFailed:
			// A store failure is not an agent failure; the legacy
			// counter never included it.
			if o.Failure != FailStore {
				res.GroupsFailed++
			}
		case GroupSkipped:
			res.GroupsSkipped++
		default:
			// Unattempted and cancelled groups are still new evidence
			// for another run.
			res.GroupsPending++
		}
	}

	return res
}
