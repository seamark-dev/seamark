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

// ExecuteExtract is the execute half of a reviewed extraction. It follows
// the stop contract of Execute: checks before each call, no call after a
// stop, and a reply that returned before the stop is stored for its whole
// batch. The console turns Boundary on. Each answer is then stored with
// store.SaveTriggerAnswer. It writes the paths, the stamp and a pending
// region change as one completion marker. It writes only while the row
// still matches the plan. ExtractTriggers leaves Boundary off, so the
// CLI stores each answer with the two unguarded writes that it always
// used.

// ExtractItemStatus is the result class of one planned item.
type ExtractItemStatus int

const (
	// ExtractUnattempted means the item was never sent: a stop, a
	// conflict, or an earlier store failure ended the run first.
	ExtractUnattempted ExtractItemStatus = iota
	// ExtractStored means a positive answer was stored and stamped.
	ExtractStored
	// ExtractNegative means a negative answer was stored and stamped: an
	// empty list, only invalid or absent paths, or no entry in a valid
	// reply (ExtractItemOutcome.Omitted). The question is closed under the
	// current prompt version.
	ExtractNegative
	// ExtractConflict means the row changed during the agent call, so the
	// answer was discarded and nothing was written. It also stops the run.
	ExtractConflict
	// ExtractFailed means the item has no stored answer: the agent call
	// failed, the reply was not valid, or a store write failed. The
	// question stays open.
	ExtractFailed
	// ExtractCancelled means a stop ended the item's batch before or
	// during its agent call. The question stays open.
	ExtractCancelled
)

// String returns a stable lower-case name for the status.
func (s ExtractItemStatus) String() string {
	switch s {
	case ExtractUnattempted:
		return "unattempted"
	case ExtractStored:
		return "stored"
	case ExtractNegative:
		return "negative"
	case ExtractConflict:
		return "conflict"
	case ExtractFailed:
		return "failed"
	case ExtractCancelled:
		return "cancelled"
	default:
		return "ExtractItemStatus(" + strconv.Itoa(int(s)) + ")"
	}
}

// ExtractBatchStatus is the result class of one planned agent call.
type ExtractBatchStatus int

const (
	// BatchUnattempted means the call never started.
	BatchUnattempted ExtractBatchStatus = iota
	// BatchCompleted means the reply was valid and every item was
	// handled. An item can still be a conflict.
	BatchCompleted
	// BatchFailed means the call or a store write failed.
	// ExtractBatchOutcome.Failure says why.
	BatchFailed
	// BatchCancelled means a stop ended the batch before or during its
	// agent call.
	BatchCancelled
)

// String returns a stable lower-case name for the status.
func (s ExtractBatchStatus) String() string {
	switch s {
	case BatchUnattempted:
		return "unattempted"
	case BatchCompleted:
		return "completed"
	case BatchFailed:
		return "failed"
	case BatchCancelled:
		return "cancelled"
	default:
		return "ExtractBatchStatus(" + strconv.Itoa(int(s)) + ")"
	}
}

// ExtractItemOutcome is what happened to one planned item.
type ExtractItemOutcome struct {
	// Proposal is the row as planned.
	Proposal model.Proposal
	Status   ExtractItemStatus
	Failure  FailureKind
	// Err is the cause of a failed, cancelled, or conflicting item. It can
	// hold agent output, so a surface must sanitize and redact it. It is
	// nil for a stored answer.
	Err error
	// Omitted is true when a valid reply had no entry for the item. The
	// prompt asks the model to list only the items that qualify, so the
	// item is a stored negative answer (D-19). A surface shows "omitted"
	// as the reason of that negative.
	Omitted bool
	// NamedRaw counts the paths of the item's answer as the model wrote
	// them. An answer of only invalid paths stays visible this way.
	NamedRaw int
	// Named are the answer's paths after the syntax rung.
	Named []string
	// Valid are the named paths that exist in the working tree. They are
	// the stored paths when Status is ExtractStored.
	Valid []string
	// Facts describe each valid path under today's history: direct,
	// confirmed by co-change, or unverified, and selected or not.
	Facts []TriggerFact
	// RegionsBefore and RegionsAfter are the effective region sets before
	// and after the answer. They differ only for a pending row whose
	// regions this run changed; an applied row keeps its regions.
	RegionsBefore  []string
	RegionsAfter   []string
	RegionsChanged bool
}

// ExtractBatchOutcome is what happened to one planned agent call.
type ExtractBatchOutcome struct {
	// Index is the position in ExtractPlan.Batches.
	Index   int
	Status  ExtractBatchStatus
	Failure FailureKind
	// Err is the cause of a failed or cancelled batch. It can hold agent
	// output, so a surface must sanitize and redact it.
	Err error
	// Items are parallel to the planned batch's proposals.
	Items []ExtractItemOutcome
	// Named counts the items whose answer in a valid reply names a path
	// that the syntax rung accepts. It counts the whole reply before any
	// write, so a store failure in the batch does not lower it. The CLI
	// prints it in the batch outcome line, as it did before a write.
	Named int
	// PromptChars and ReplyChars meter the agent traffic. They stay 0
	// when no agent call started for the batch.
	PromptChars int
	ReplyChars  int
	// Started and Finished bound the batch's work. Both are zero for an
	// unattempted batch.
	Started  time.Time
	Finished time.Time
}

// ExtractEventKind names one extraction progress event.
type ExtractEventKind int

const (
	// ExtractBatchStarted precedes the agent call of one batch.
	ExtractBatchStarted ExtractEventKind = iota
	// ExtractBatchDone reports a batch outcome after its answers are
	// stored, so a receiver never shows an unsaved answer as done.
	ExtractBatchDone
	// ExtractStopped reports that no further batch runs.
	ExtractStopped
)

// String returns a stable lower-case name for the kind.
func (k ExtractEventKind) String() string {
	switch k {
	case ExtractBatchStarted:
		return "batch started"
	case ExtractBatchDone:
		return "batch done"
	case ExtractStopped:
		return "stopped"
	default:
		return "ExtractEventKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// ExtractEvent is one typed progress report. A batch sends at most one
// ExtractBatchStarted and one ExtractBatchDone, and a run at most one
// ExtractStopped, so a receiver can size a queue for 2*batches+1 events.
type ExtractEvent struct {
	Kind ExtractEventKind
	// Index is the 0-based position in ExtractPlan.Batches. For
	// ExtractStopped it is the first batch that did not run.
	Index int
	// Total is len(ExtractPlan.Batches), a known denominator.
	Total int
	// Batch is set on ExtractBatchDone. It points to a deep copy, slices
	// included, so a receiver on another goroutine never shares memory
	// with the run. Only Err values are shared, because errors are
	// immutable.
	Batch *ExtractBatchOutcome
	// Stop and Err are set on ExtractStopped.
	Stop StopReason
	Err  error
}

// ExtractExecuteOptions tune one extraction execution.
type ExtractExecuteOptions struct {
	// Check runs before the first batch and before each batch: the
	// caller's cheap checks, such as the file hashes and the invocation.
	// A non-nil error stops the run with StopConflict. Nil checks nothing.
	Check func() error
	// OnEvent receives events synchronously on the executing goroutine,
	// never while a transaction is open. Nil discards the events.
	OnEvent func(ExtractEvent)
	// Now is the clock for the stamps and the batch times. Nil means
	// time.Now.
	Now func() time.Time
	// Boundary turns on three store checks. The full plan revalidation
	// runs before the first batch. The row and evidence checks run before
	// each batch. The guarded save refuses a row that another writer
	// changed. The console sets it. The legacy ExtractTriggers leaves it
	// off, because the CLI output and writes must not change (D-1).
	Boundary bool
}

// ExtractExecution is the typed result of one extraction execution.
type ExtractExecution struct {
	// Batches are the outcomes, parallel to ExtractPlan.Batches.
	Batches []ExtractBatchOutcome
	Stop    StopReason
	StopErr error
	// Persisted lists the items with a stored answer, positive or
	// negative. The other lists name the items in each remaining class.
	// Every list is in send order.
	Persisted   []int64
	Failed      []int64
	Cancelled   []int64
	Conflicts   []int64
	Unattempted []int64
	// Result holds the legacy counters, derived from the outcomes.
	// Examined counts the items of the batches whose agent call started.
	Result ExtractResult
}

// ExecuteExtract runs exactly the planned batches. Before the first batch
// it checks ctx, revalidates the whole plan, runs opts.Check, and checks
// ctx again. Before each batch it checks ctx, runs opts.Check, compares
// the batch's rows and evidence with the store, and checks ctx again. A
// change stops the run with StopConflict, and the remaining items need a
// new plan. A row that changes during an agent call is a conflict too: its
// answer is discarded, the rest of its batch is stored, and the run stops,
// because another writer is active (D-20). A stop that ends an agent call
// cancels its batch; a call that already replied is stored for all its
// items.
//
// Without opts.Boundary, the store comparisons do not run and no answer
// is a conflict. Each answer then takes the unguarded CLI writes (see
// saveUnguarded). The context checks and opts.Check still run.
//
// It always returns a non-nil *ExtractExecution. The error is non-nil only
// for a store failure or an unusable call (no plan, no root, no invoker);
// a stop or a conflict is data in ExtractExecution.Stop, not an error.
func ExecuteExtract(ctx context.Context, st *store.Store, inv agent.Invoker, p *ExtractPlan,
	opts ExtractExecuteOptions,
) (*ExtractExecution, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	start := now()

	if p == nil {
		return &ExtractExecution{}, errors.New("distill: no extraction plan to execute")
	}

	x := &ExtractExecution{Batches: make([]ExtractBatchOutcome, len(p.Batches))}

	for i, b := range p.Batches {
		x.Batches[i] = ExtractBatchOutcome{Index: i, Items: make([]ExtractItemOutcome, len(b.Proposals))}

		// Both sets start equal, so an item without a stored region
		// change never reads as a scope change. They are separate copies,
		// so a write to one never changes the other.
		for j, row := range b.Proposals {
			set := row.RegionSet()
			x.Batches[i].Items[j] = ExtractItemOutcome{Proposal: row, RegionsBefore: set,
				RegionsAfter: cloneSlice(set)}
		}
	}

	e := &extractor{ctx: ctx, st: st, inv: inv, plan: p, opts: opts, x: x, now: now}

	err := e.run()

	x.tally()
	x.Result.Duration = now().Sub(start)

	return x, err
}

// extractor holds the state of one ExecuteExtract call.
type extractor struct {
	ctx  context.Context
	st   *store.Store
	inv  agent.Invoker
	plan *ExtractPlan
	opts ExtractExecuteOptions
	x    *ExtractExecution
	now  func() time.Time
}

// run performs the batch loop. It returns an error only for a store
// failure or an unusable plan.
func (e *extractor) run() error {
	if err := e.usable(); err != nil || len(e.plan.Batches) == 0 {
		return err
	}

	if reason, err := e.startCheck(); reason != StopNone {
		e.stop(0, reason, err)

		return storeError(reason, err)
	}

	for i := range e.plan.Batches {
		if reason, err := e.batchCheck(i); reason != StopNone {
			e.stop(i, reason, err)

			return storeError(reason, err)
		}

		if err := e.runBatch(i); err != nil {
			return err
		}

		// runBatch records a cancel or a store failure in its own batch,
		// so the run ends here and never also at the next check.
		if e.x.Stop != StopNone {
			return nil
		}
	}

	return nil
}

// usable refuses a plan that cannot run safely before any check or call.
func (e *extractor) usable() error {
	if e.plan.Options.Root == "" {
		return ErrExtractRoot
	}

	for i, b := range e.plan.Batches {
		if len(b.Items) != len(b.Proposals) {
			return fmt.Errorf("distill: extraction batch %d has %d items for %d proposals",
				i+1, len(b.Items), len(b.Proposals))
		}
	}

	if len(e.plan.Batches) > 0 && e.inv == nil {
		return ErrNoInvoker
	}

	return nil
}

// startCheck runs before the first batch: the context, the full plan
// revalidation with Boundary on, then the caller's checks. It does not
// check the context again, because batchCheck(0) runs next and checks the
// context first.
func (e *extractor) startCheck() (StopReason, error) {
	if err := e.ctx.Err(); err != nil {
		return StopCancelled, err
	}

	if e.opts.Boundary {
		if err := e.plan.Revalidate(e.st); err != nil {
			return classify(err), err
		}
	}

	if e.opts.Check != nil {
		if err := e.opts.Check(); err != nil {
			return StopConflict, err
		}
	}

	return StopNone, nil
}

// batchCheck runs before batch i: the context, the caller's checks, the
// batch's rows and evidence with Boundary on, and the context again. The
// second context check catches a stop that arrives during the slower
// checks, so the batch never starts and stays unattempted.
func (e *extractor) batchCheck(i int) (StopReason, error) {
	if err := e.ctx.Err(); err != nil {
		return StopCancelled, err
	}

	if e.opts.Check != nil {
		if err := e.opts.Check(); err != nil {
			return StopConflict, err
		}
	}

	if e.opts.Boundary {
		b := e.plan.Batches[i]
		if err := checkExtractItems(e.st, b.Proposals, b.Items); err != nil {
			return classify(err), err
		}
	}

	if err := e.ctx.Err(); err != nil {
		return StopCancelled, err
	}

	return StopNone, nil
}

// runBatch sends batch i and stores its valid reply. It returns an error
// only for a store failure, which also stops the run.
func (e *extractor) runBatch(i int) error {
	b := e.plan.Batches[i]
	out := &e.x.Batches[i]

	e.emit(ExtractEvent{Kind: ExtractBatchStarted, Index: i})

	out.Started = e.now()

	// A stop can arrive while the receiver handles the start event. The
	// call then never starts, so the batch meters no traffic.
	if err := e.ctx.Err(); err != nil {
		e.cancelBatch(i, err)

		return nil
	}

	// The per-batch limit bounds one call. Its expiry is read before
	// cancel, which would otherwise overwrite it with Canceled.
	callCtx, cancel := context.WithTimeout(e.ctx, perExtractTimeout)
	reply, err := e.inv.Invoke(callCtx, b.Prompt)
	timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded)

	cancel()

	// Failed calls count too: the traffic was paid for.
	out.PromptChars, out.ReplyChars = len(b.Prompt), len(reply)

	if err != nil {
		// An ended parent context means the stop ended the call, so the
		// batch is cancelled, not failed.
		if e.ctx.Err() != nil {
			e.cancelBatch(i, err)

			return nil
		}

		kind := FailAgent
		if timedOut {
			kind = FailTimeout
		}

		e.failBatch(i, kind, err)

		return nil
	}

	answers, err := parseExtractReply(reply)
	if err != nil {
		e.failBatch(i, FailInvalidReply, err)

		return nil
	}

	return e.persist(i, answers)
}

// persist stores the answer of every item of batch i. It never checks
// the context: a stop never splits a parsed batch, because the reply is
// already paid for. An item without an entry in the reply is a negative
// answer, as in the CLI, because the prompt asks the model to list only
// the items that qualify (D-19). A store failure fails this item and the
// unsaved rest of the batch, and stops the run. Without Boundary, the
// failed item can keep the regions of its first write (saveUnguarded). A
// conflict stops the run after the whole batch is stored.
func (e *extractor) persist(i int, answers []extractAnswer) error {
	b := e.plan.Batches[i]
	out := &e.x.Batches[i]
	chosen := itemAnswers(answers, b.Proposals)
	checkedAt := e.now().Unix()
	out.Named = len(namedTriggers(answers, b.Proposals))

	var conflict error

	for j, row := range b.Proposals {
		item := &out.Items[j]

		ans, ok := chosen[row.ID]
		if !ok {
			ans, item.Omitted = extractAnswer{ID: row.ID}, true
		}

		if err := e.answer(item, b.Items[j], ans, checkedAt); err != nil {
			for k := j; k < len(out.Items); k++ {
				if k == j || out.Items[k].Status == ExtractUnattempted {
					out.Items[k].Status, out.Items[k].Failure = ExtractFailed, FailStore
					out.Items[k].Err = fmt.Errorf("not saved: %w", err)
				}
			}

			out.Status, out.Failure, out.Err = BatchFailed, FailStore, err
			e.finish(i)
			e.stop(i+1, StopStoreError, err)

			return err
		}

		if item.Status == ExtractConflict && conflict == nil {
			conflict = item.Err
		}
	}

	out.Status = BatchCompleted
	e.finish(i)

	// Another writer changed a row during the call, as in a save conflict
	// of Execute. The next calls would race the same writer, so the run
	// stops and the user prepares the remaining items again (D-20).
	if conflict != nil {
		e.stop(i+1, StopConflict, conflict)
	}

	return nil
}

// answer validates one item's answer, recomputes its regions from the
// frozen evidence, and stores it. With Boundary on, it stores the answer
// with the row's guard. A guard miss is then a conflict: the paid answer
// is discarded rather than written over a row that another writer
// changed. With Boundary off, saveUnguarded stores it.
func (e *extractor) answer(item *ExtractItemOutcome, planned ExtractItem, ans extractAnswer, checkedAt int64) error {
	row := item.Proposal
	root := e.plan.Options.Root

	item.NamedRaw = len(ans.TriggerPaths)
	item.Named = cleanTriggerPaths(ans.TriggerPaths)
	item.Valid = validateTriggerPaths(root, item.Named)

	save := store.TriggerAnswer{
		Guard:        store.NewRowGuard(row),
		PriorChecked: row.TriggerChecked, PriorVersion: row.TriggerPromptVersion, PriorPaths: row.TriggerPaths,
		Paths: item.Valid, CheckedAt: checkedAt, Version: TriggerPromptVersion,
	}

	// The console shows the trigger facts of every row. The CLI loop
	// recomputed only a pending row. Without Boundary, an applied row
	// therefore makes no store read, so it cannot fail where the CLI
	// never failed.
	if len(planned.Living) > 0 && (e.opts.Boundary || row.Status == model.ProposalProposed) {
		asked := row
		asked.TriggerPaths = item.Valid

		recomputed, facts, err := RecomputeRegions(e.st, root, asked, planned.Living)
		if err != nil {
			return err
		}

		item.Facts = facts

		// Only a pending row takes regions here. An installed pin
		// changes scope only through a reviewed retarget.
		if row.Status == model.ProposalProposed && regionsDiffer(row, recomputed) {
			save.SetRegions, save.Regions = true, recomputed

			if len(recomputed) > 0 {
				save.Region = recomputed[0]
			}
		}
	}

	if !e.opts.Boundary {
		return e.saveUnguarded(item, save)
	}

	saved, err := e.st.SaveTriggerAnswer(save)
	if err != nil {
		return err
	}

	if !saved {
		item.Status = ExtractConflict
		item.Err = fmt.Errorf("%w: p%d changed during the agent call; its answer was not saved",
			ErrExtractStale, row.ID)

		return nil
	}

	item.Status = answeredStatus(item.Valid)

	if save.SetRegions {
		item.RegionsAfter = (model.Proposal{Region: save.Region, Regions: save.Regions}).RegionSet()
		item.RegionsChanged = true
	}

	return nil
}

// saveUnguarded stores one answer with the two writes of the CLI loop, in
// its order (D-1). The regions of a pending row write first, and only
// while the row is still pending. A row applied during the agent call
// thus keeps the identity of its installed pin. The stamped answer
// writes second and binds no guard, so it also lands on a row that
// another writer changed. The stamp is the completion marker that the
// CLI selection reads. A run that stops between the two writes therefore
// asks the row again and finishes the pair.
func (e *extractor) saveUnguarded(item *ExtractItemOutcome, save store.TriggerAnswer) error {
	if save.SetRegions {
		row := item.Proposal
		row.Region, row.Regions = save.Region, save.Regions

		changed, err := e.st.UpdateProposalRegionsIfPending(row)
		if err != nil {
			return err
		}

		if changed {
			item.RegionsAfter = row.RegionSet()
			item.RegionsChanged = true
		}
	}

	if err := e.st.UpdateProposalTriggers(save.Guard.ID, save.Paths, save.CheckedAt, save.Version); err != nil {
		return err
	}

	item.Status = answeredStatus(item.Valid)

	return nil
}

// answeredStatus is the status of a stored answer: stored when a valid
// path survives, otherwise negative.
func answeredStatus(valid []string) ExtractItemStatus {
	if len(valid) > 0 {
		return ExtractStored
	}

	return ExtractNegative
}

// itemAnswers chooses one answer per batch member and drops answers for
// IDs outside the batch. It keeps the first answer, in reply order, that
// has a path the syntax rung accepts. Only when no answer of the member
// has such a path does it keep the member's first answer, a negative one.
// namedTriggers uses the same rule for the batch count. So an empty or
// invalid first answer never hides a later answer that names a path.
func itemAnswers(answers []extractAnswer, batch []model.Proposal) map[int64]extractAnswer {
	member := make(map[int64]bool, len(batch))
	for _, p := range batch {
		member[p.ID] = true
	}

	out := make(map[int64]extractAnswer, len(batch))
	named := make(map[int64]bool, len(batch))

	for _, a := range answers {
		if !member[a.ID] || named[a.ID] {
			continue
		}

		if len(cleanTriggerPaths(a.TriggerPaths)) > 0 {
			out[a.ID], named[a.ID] = a, true

			continue
		}

		if _, seen := out[a.ID]; !seen {
			out[a.ID] = a
		}
	}

	return out
}

// cancelBatch records that a stop ended batch i and stops the run. The
// stop is recorded here and not at the next check, because the last
// batch has no next check.
func (e *extractor) cancelBatch(i int, err error) {
	out := &e.x.Batches[i]
	out.Status, out.Err = BatchCancelled, err

	for j := range out.Items {
		out.Items[j].Status, out.Items[j].Err = ExtractCancelled, err
	}

	e.finish(i)
	e.stop(i+1, StopCancelled, e.ctx.Err())
}

// failBatch records a failed agent call or reply of batch i. The run
// goes on: batches fail independently, as distillation groups do.
func (e *extractor) failBatch(i int, kind FailureKind, err error) {
	out := &e.x.Batches[i]
	out.Status, out.Failure, out.Err = BatchFailed, kind, err

	for j := range out.Items {
		out.Items[j].Status, out.Items[j].Failure, out.Items[j].Err = ExtractFailed, kind, err
	}

	e.finish(i)
}

// finish stamps the end of batch i and reports a deep copy of its
// outcome. The receiver can run on another goroutine, so it must not
// share a slice with the result that the run still returns.
func (e *extractor) finish(i int) {
	e.x.Batches[i].Finished = e.now()

	outcome := e.x.Batches[i].clone()

	e.emit(ExtractEvent{Kind: ExtractBatchDone, Index: i, Batch: &outcome})
}

// stop records why the run ends and reports it. The batches from index on
// keep their current status, unattempted unless set otherwise.
func (e *extractor) stop(index int, reason StopReason, err error) {
	e.x.Stop, e.x.StopErr = reason, err
	e.emit(ExtractEvent{Kind: ExtractStopped, Index: index, Stop: reason, Err: err})
}

// emit sends one event to the receiver, if any.
func (e *extractor) emit(ev ExtractEvent) {
	if e.opts.OnEvent == nil {
		return
	}

	ev.Total = len(e.plan.Batches)
	e.opts.OnEvent(ev)
}

// clone returns a copy of b that shares no slice with b.
func (b ExtractBatchOutcome) clone() ExtractBatchOutcome {
	b.Items = cloneSlice(b.Items)

	for j := range b.Items {
		b.Items[j] = b.Items[j].clone()
	}

	return b
}

// clone returns a copy of item that shares no slice with item.
func (item ExtractItemOutcome) clone() ExtractItemOutcome {
	item.Proposal.Regions = cloneSlice(item.Proposal.Regions)
	item.Proposal.TriggerPaths = cloneSlice(item.Proposal.TriggerPaths)
	item.Proposal.Members = cloneSlice(item.Proposal.Members)
	item.Named = cloneSlice(item.Named)
	item.Valid = cloneSlice(item.Valid)
	item.Facts = cloneSlice(item.Facts)
	item.RegionsBefore = cloneSlice(item.RegionsBefore)
	item.RegionsAfter = cloneSlice(item.RegionsAfter)

	return item
}

// cloneSlice returns a copy of s. It keeps a nil slice nil, so a copy
// compares equal to its source. The package cannot import the standard
// slices package, because a function of this package has that name.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}

	return append(make([]T, 0, len(s)), s...)
}

// tally fills the ID lists and the legacy counters from the outcomes.
func (x *ExtractExecution) tally() {
	res := ExtractResult{}

	for _, b := range x.Batches {
		res.PromptChars += b.PromptChars
		res.ReplyChars += b.ReplyChars

		if b.PromptChars > 0 {
			res.Examined += len(b.Items)
		}

		// A store failure is not an agent failure; the legacy counter
		// never included it.
		if b.Status == BatchFailed && b.Failure != FailStore {
			res.BatchesFailed++
		}

		for _, item := range b.Items {
			x.tallyItem(&res, item)
		}
	}

	x.Result = res
}

// tallyItem adds one item outcome to the lists and the counters.
func (x *ExtractExecution) tallyItem(res *ExtractResult, item ExtractItemOutcome) {
	id := item.Proposal.ID

	if len(item.Named) > 0 {
		res.Named++
	}

	if item.RegionsChanged {
		res.Retargeted++
	}

	switch item.Status {
	case ExtractStored:
		res.Stored++

		if item.Proposal.Status == model.ProposalApplied {
			res.AppliedStored++
		}

		x.Persisted = append(x.Persisted, id)
	case ExtractNegative:
		x.Persisted = append(x.Persisted, id)
	case ExtractConflict:
		x.Conflicts = append(x.Conflicts, id)
	case ExtractFailed:
		x.Failed = append(x.Failed, id)
	case ExtractCancelled:
		x.Cancelled = append(x.Cancelled, id)
	default:
		x.Unattempted = append(x.Unattempted, id)
	}
}
