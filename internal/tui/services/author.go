package services

import (
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The pin authoring services: a plan and a commit of one create, edit,
// or removal of a pin in .seamark/lessons.yaml (contracts §3.4, §3.5).
// Both read the ledger from a read-only store, because an authoring
// commit writes the pin file only and never the ledger. Both use the
// effective root R, the root of the file that Pins lists.

// PlanAuthor plans one authoring operation. It writes nothing and takes
// no lock. Without a readable index the plan still holds the candidate,
// the diff, and the identity, and a blocker names the missing ledger
// (P4-8). A foreign root is a blocker of the plan too (P4-16).
func (e *engine) PlanAuthor(req maintain.AuthorRequest) (*maintain.AuthorPlan, error) {
	st, probe, err := e.openRead()
	if err == nil {
		defer func() { _ = st.Close() }()
	}

	return maintain.PlanAuthor(e.authorDeps(st, probe), req)
}

// CommitAuthor commits a reviewed authoring plan under the mutation
// lock. maintain.CommitAuthor refuses a foreign root first, then plans
// again from fresh observations and refuses a stale plan before any
// write. The store opens read-only, so the commit cannot write the
// ledger.
func (e *engine) CommitAuthor(p *maintain.AuthorPlan) (maintain.AuthorResult, error) {
	if !e.mu.TryLock() {
		return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: ErrBusy}, ErrBusy
	}
	defer e.mu.Unlock()

	if p == nil {
		return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: errNoPlan}, errNoPlan
	}

	st, probe, err := e.openRead()
	if err == nil {
		defer func() { _ = st.Close() }()
	}

	return maintain.CommitAuthor(e.authorDeps(st, probe), p)
}

// authorDeps binds the authoring engine to one probe and to a read-only
// store, or to no store when the index is not readable. The maintenance
// planner ignores ForeignRoot, so only authoring sets it. Unlike ownRoot,
// the rule has no --db exception (P4-16).
func (e *engine) authorDeps(st *store.Store, probe workspace.IndexProbe) maintain.Deps {
	d := e.maintDeps(st, probe)
	d.ForeignRoot = e.foreignRoot(probe)

	return d
}
