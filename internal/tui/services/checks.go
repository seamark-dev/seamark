package services

import (
	"context"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
)

// The lesson-check services. The capability report, the verification,
// and the revalidation call the engines of doctor, status, and lessons
// --verify, so the console and the CLI give one account. The
// check-definition authoring is part of Maintenance, because it writes
// .seamark/lessons.yaml as the pin authoring does.

// Checks covers the executable lesson checks of the workspace: what the
// installed hooks cover, the verification of the working tree, and the
// freshness of a shown verification. Lessons › Pins and the Integrations
// detail use it. VerifyLessons appends one record to the lesson-check
// audit stream, so the area is not part of Reader.
type Checks interface {
	// LessonCapability joins insps, the inspections that Integrations
	// returned, with .seamark/lessons.yaml of the workspace root into
	// the lesson-capability/1 report. The hooks of the workspace read
	// that file, so the report uses it. It reads only and runs no hook
	// binary.
	LessonCapability(insps []integration.Inspection) lessoncheck.CapabilityReport
	// VerifyLessons verifies the working tree of the workspace root
	// against every executable pin, as `seamark lessons --verify` does.
	// paths holds repository-relative paths; empty is the default
	// selection. The result names the caller tui. It appends one audit
	// record and sweeps the stale pending baselines.
	VerifyLessons(ctx context.Context, paths []string) (*lessoncheck.Verification, error)
	// RevalidateLessons reports whether the binding of an earlier
	// result of VerifyLessons still holds (lessoncheck.Revalidate). It
	// runs no checker and writes nothing.
	RevalidateLessons(ctx context.Context, v *lessoncheck.Verification) (lessoncheck.Freshness, error)
}

// LessonCapability joins the inspections that the console holds, so one
// refresh inspects each client once.
func (e *engine) LessonCapability(insps []integration.Inspection) lessoncheck.CapabilityReport {
	return integration.LessonCapability(insps, e.ws.Root, e.self())
}

// VerifyLessons runs the verification of lessons --verify with the
// caller tui and the default limits. Revalidate reads with the default
// limits too, so a shown result stays fresh while nothing changes.
//
// It takes no engine lock. Its writes are one append to the audit
// stream, which holds its own file lock, and the crash sweep of pending
// baselines, which the hooks also run at any time. Neither write changes
// a file that another console write reads, so a verification can run
// beside a long run.
func (e *engine) VerifyLessons(ctx context.Context, paths []string) (*lessoncheck.Verification, error) {
	return lessoncheck.Verify(ctx, lessoncheck.VerifyRequest{
		Root:           e.ws.Root,
		Paths:          paths,
		Caller:         lessoncheck.CallerTUI,
		Now:            e.deps.Now,
		SeamarkVersion: e.version,
	})
}

func (e *engine) RevalidateLessons(ctx context.Context, v *lessoncheck.Verification) (lessoncheck.Freshness, error) {
	return lessoncheck.Revalidate(ctx, e.ws.Root, v)
}

// PlanCheckDef plans one check-definition edit of the pin file at the
// effective root R, the file that Pins lists. It writes nothing and
// takes no lock. A foreign root is a blocker of the plan, as for the
// pin authoring.
func (e *engine) PlanCheckDef(req maintain.CheckDefRequest) (*maintain.CheckDefPlan, error) {
	p, err := maintain.PlanCheckDef(e.authorDeps(nil, e.probe()), req)
	if err != nil {
		return nil, err
	}

	e.joinActive(p.Impact, p.After)

	return p, nil
}

// CommitCheckDef writes a reviewed definition plan under the mutation
// lock. maintain.CommitCheckDef refuses a foreign root, a closed write
// gate, and a file that changed since the plan, and writes nothing then.
func (e *engine) CommitCheckDef(p *maintain.CheckDefPlan) (maintain.AuthorResult, error) {
	if !e.mu.TryLock() {
		return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: ErrBusy}, ErrBusy
	}
	defer e.mu.Unlock()

	if p == nil {
		return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: errNoPlan}, errNoPlan
	}

	return maintain.CommitCheckDef(e.authorDeps(nil, e.probe()), p)
}

// joinActive replaces the fixed active text of each impact block with
// the capability join on the candidate bytes (the "active:" line), as
// lessons --set-mode does. The candidate holds the mode that each pin
// requests after the save, also when the save is blocked. A candidate
// that does not decode keeps the fixed text, because its plan is blocked
// anyway. The engine of maintain has no binary identity, so the adapter
// joins, like the CLI.
func (e *engine) joinActive(impacts []lessoncheck.Impact, after []byte) {
	if len(impacts) == 0 || after == nil {
		return
	}

	cfg, errs, err := lessoncheck.Parse(after)
	if err != nil {
		return
	}

	report := integration.LessonCapabilityOf(e.reg.Inspect(e.ws.Root), cfg, errs, e.self())

	for i := range impacts {
		// Pins holds every entry in file order, so entry n is Pins[n-1].
		if n := impacts[i].Entry; n >= 1 && n <= len(cfg.Pins) {
			impacts[i].Active = report.ActiveText(cfg.Pins[n-1].Ref)
		}
	}
}

// self names the running binary with the version of the console, so the
// report identifies the hook binaries as doctor and status do.
func (e *engine) self() integration.BinaryIdentity {
	return integration.SelfIdentity(e.version)
}
