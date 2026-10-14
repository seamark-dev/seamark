package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The installed lesson services: the Pins read, the retarget and prune
// maintenance, its session recovery, and the trigger extraction. Reads
// and plans take no lock, so the views stay responsive during a run.
// Commits and runs take the engine lock with TryLock, so a second
// mutation fails with ErrBusy and never waits (contracts D-21).

// errNoPlan reports a commit without a reviewed plan.
var errNoPlan = errors.New("no reviewed plan")

// Pins reads the installed pins and relates them to the ledger. It needs
// no index: without a readable store the pins stay listed and every link
// reads unknown, because the pin file alone defines what is installed.
// It writes nothing, takes no lock, and records no delivery.
func (e *engine) Pins() *maintain.Inspection {
	st, probe, err := e.openRead()
	if err != nil {
		return maintain.Inspect(probe.Root, nil, probe, e.deps.Now())
	}
	defer func() { _ = st.Close() }()

	return maintain.Inspect(probe.Root, st, probe, e.deps.Now())
}

// PlanMaintenance plans a retarget or a prune from a read-only store. It
// writes nothing, so it takes no lock.
func (e *engine) PlanMaintenance(req maintain.Request) (*maintain.Plan, error) {
	st, probe, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	return maintain.NewPlan(e.maintDeps(st, probe), req)
}

// CommitMaintenance commits a reviewed maintenance plan under the
// mutation lock. maintain.Commit plans again from fresh observations and
// refuses a stale plan before any write.
func (e *engine) CommitMaintenance(p *maintain.Plan) (maintain.Result, error) {
	if !e.mu.TryLock() {
		return maintResult(p), ErrBusy
	}
	defer e.mu.Unlock()

	if p == nil {
		return maintain.Result{}, errNoPlan
	}

	st, probe, err := e.openWrite()
	if err != nil {
		return maintResult(p), err
	}
	defer func() { _ = st.Close() }()

	if err := e.ownRoot(probe); err != nil {
		return maintResult(p), err
	}

	return maintain.Commit(e.maintDeps(st, probe), p)
}

// PlanRecovery plans the ledger-only completion of a partial operation
// of this session from a read-only store. It writes nothing.
func (e *engine) PlanRecovery(r *maintain.Recovery) (*maintain.Plan, error) {
	st, probe, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	return maintain.PlanRecovery(e.maintDeps(st, probe), r)
}

// CommitRecovery completes the ledger update of a reviewed recovery plan
// under the mutation lock. It never writes the pin file.
func (e *engine) CommitRecovery(p *maintain.Plan) (maintain.Result, error) {
	if !e.mu.TryLock() {
		return maintResult(p), ErrBusy
	}
	defer e.mu.Unlock()

	if p == nil {
		return maintain.Result{}, errNoPlan
	}

	st, probe, err := e.openWrite()
	if err != nil {
		return maintResult(p), err
	}
	defer func() { _ = st.Close() }()

	if err := e.ownRoot(probe); err != nil {
		return maintResult(p), err
	}

	return maintain.CommitRecovery(e.maintDeps(st, probe), p)
}

// PlanExtract previews a trigger extraction. It opens the store
// read-only and checks only that the agent executable is on PATH; it
// never runs the agent, so it takes no lock.
func (e *engine) PlanExtract(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
	return learning.PlanExtract(e.ws, e.reg, req)
}

// RunExtract runs a reviewed extraction preview under the mutation lock.
// It holds the lock for the whole run, as RunDistill does. The learning
// service refuses a preview of another workspace itself.
func (e *engine) RunExtract(ctx context.Context, p *learning.ExtractPreview,
	progress func(distill.ExtractEvent),
) (*distill.ExtractExecution, error) {
	if !e.mu.TryLock() {
		return nil, ErrBusy
	}
	defer e.mu.Unlock()

	return learning.RunExtract(ctx, e.ws, e.reg, p, e.deps.Invoke, progress)
}

// openWrite opens the index for a short write. A fresh probe gives the
// effective root and the index state, because the maintenance commit
// plans again from them. Only a ready or a not yet indexed database
// holds a ledger that a commit may write.
func (e *engine) openWrite() (*store.Store, workspace.IndexProbe, error) {
	probe := e.probe()

	if probe.State != workspace.IndexReady && probe.State != workspace.IndexNotIndexed {
		return nil, probe, fmt.Errorf("%w (%s)", ErrNoLedger, probe.State)
	}

	st, err := store.Open(e.ws.DBPath)
	if err != nil {
		return nil, probe, err
	}

	return st, probe, nil
}

// maintDeps binds the maintenance engine to one store and one probe.
// The store is both the reader and the ledger writer, and the pin file
// writes go through the guarded atomic write of the setup engine.
func (e *engine) maintDeps(st *store.Store, probe workspace.IndexProbe) maintain.Deps {
	return maintain.Deps{Root: probe.Root, Reg: e.reg, Reader: st, Probe: probe, Now: e.deps.Now}
}

// maintResult is the result of a commit that did not start. It names the
// action of the plan, so a screen can still say what was refused.
func maintResult(p *maintain.Plan) maintain.Result {
	if p == nil {
		return maintain.Result{}
	}

	return maintain.Result{Action: p.Action}
}
