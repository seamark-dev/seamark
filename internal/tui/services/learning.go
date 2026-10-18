package services

import (
	"context"
	"errors"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
)

// The learning services: the evidence snapshot, the evidence refresh,
// and the distillation. The learning package resolves the effective
// root from the index probe itself, so these methods pass the whole
// workspace. That is the same root that openRead gives the ledger.

// errForeignPlan reports a plan that another workspace produced. A run
// must not write the database of a workspace that the user did not
// review.
var errForeignPlan = errors.New("the plan belongs to another workspace; prepare it again")

// Learning reads the evidence state. It opens the store read-only and
// runs local git only to resolve the origin remote.
func (e *engine) Learning() learning.Snapshot {
	return learning.Inspect(e.ws)
}

// PlanEvidence previews an evidence refresh. It runs no gh and writes
// nothing, so it does not take the mutation lock.
func (e *engine) PlanEvidence(mode index.SourceMode) (*learning.Plan, error) {
	return learning.PlanRefresh(e.ws, mode)
}

// RunEvidence runs a reviewed evidence plan under the mutation lock. It
// holds the lock for the whole run, including the GitHub fetch, because
// the engine refuses every second mutation until the run ends.
func (e *engine) RunEvidence(ctx context.Context, p *learning.Plan,
	progress func(index.RefreshEvent),
) (learning.Outcome, error) {
	if !e.mu.TryLock() {
		return e.refusedOutcome(ErrBusy), ErrBusy
	}
	defer e.mu.Unlock()

	if p != nil && p.Workspace != e.ws {
		return e.refusedOutcome(errForeignPlan), errForeignPlan
	}

	return learning.RunRefresh(ctx, p, e.deps.Fetch, progress)
}

// refusedOutcome is the outcome of a refresh that did not start. Both
// sources read "not attempted", and the counts read unknown, because a
// zero Outcome would read as known zero counts.
func (e *engine) refusedOutcome(err error) learning.Outcome {
	refused := index.SourceOutcome{
		State: index.SourceNotAttempted, Cause: index.CauseRefused, Reason: err.Error(), Err: err,
	}

	out := learning.Outcome{Fixes: refused, Reviews: refused, FinishedAt: e.deps.Now()}
	out.After.CountsErr = err

	return out
}

// PlanDistill previews a distillation. It opens the store read-only and
// checks only that the agent executable is on PATH; it never runs it.
func (e *engine) PlanDistill(req learning.DistillRequest) (*learning.DistillPreview, error) {
	return learning.PlanDistill(e.ws, e.reg, req)
}

// RunDistill runs a reviewed distillation preview under the mutation
// lock. It holds the lock for the whole run, because the engine refuses
// every second mutation until the last group ends. The learning service
// refuses a preview of another workspace itself, so this method adds no
// such check.
func (e *engine) RunDistill(ctx context.Context, p *learning.DistillPreview,
	progress func(distill.Event),
) (*distill.Execution, error) {
	if !e.mu.TryLock() {
		return nil, ErrBusy
	}
	defer e.mu.Unlock()

	return learning.RunDistill(ctx, e.ws, e.reg, p, e.deps.Invoke, progress)
}

// CleanupPending removes the stale pending proposals of a cleanup-only
// preview under the mutation lock. It runs no agent.
func (e *engine) CleanupPending(p *learning.DistillPreview) (int, error) {
	if !e.mu.TryLock() {
		return 0, ErrBusy
	}
	defer e.mu.Unlock()

	return learning.CleanupPending(e.ws, p)
}
