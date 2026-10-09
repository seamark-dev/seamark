package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// This file holds the fixtures of the trigger extraction tests. They
// build a preview in engine shape and typed executions. Helpers drive
// the record through the harness.

// extPlannedAt is the fixed preview time of the fixtures.
var extPlannedAt = time.Date(2026, 9, 29, 10, 4, 31, 0, time.Local)

// extRows are the eligible rows of the fixture in the CLI send order:
// pending newest first, then applied newest first. p22 has a negative
// answer to an older question, so its question is open again.
func extRows() []domain.Proposal {
	pending := func(id int64, rule, region, note string, members ...int64) domain.Proposal {
		return domain.Proposal{
			ID: id, Rule: rule, Regions: []string{region}, Note: note, Members: members,
			Status: domain.ProposalProposed,
		}
	}

	applied := func(id int64, rule, region, note string, members ...int64) domain.Proposal {
		return domain.Proposal{ID: id, Rule: rule, Region: region, Note: note, Members: members, Status: domain.ProposalApplied}
	}

	older := pending(22, "close-db-handles", "internal/apix", "Close database handles in the same function.", 103)
	older.TriggerChecked, older.TriggerPromptVersion = 1758000000, 1

	return []domain.Proposal{
		pending(23, "keep-index-fresh", "internal/index", "Rebuild the index after a schema change.", 121, 122),
		older,
		pending(21, "check-nil-errors", "internal/api", "Check every error returned by the store before you use "+
			"the value.", 101, 102),
		applied(6, "pin-go-version", "build", "Keep the Go version in go.mod and the CI image equal.", 150),
		applied(4, "wrap-sql-errors", "internal/store", "Wrap SQL errors with the query name.", 104),
	}
}

// extPreviewOpts shapes a fixture preview.
type extPreviewOpts struct {
	// rows are the eligible rows in send order; nil means extRows.
	rows []domain.Proposal
	// batch is the batch size; 0 means 10, as the engine batches.
	batch int
	// ineligible are the proposals that the plan does not ask.
	ineligible []distill.ExtractSkip
	// skipped are the Pins entries of the request without a proposal.
	skipped []distill.ExtractSkip
	// live are the live notes that differ from the recorded notes.
	live     map[int64]string
	blockers []string
	// workDirErr is the working-directory error of the preview. The
	// engine adds its line to blockers; a test lists that line itself.
	workDirErr error
	// none plans no batch, as for a selection without an open question.
	none bool
}

// extMixed is the review fixture. It has five eligible rows in one
// batch and three ineligible proposals. It also has one Pins entry
// without a proposal and one live note that differs.
func extMixed() extPreviewOpts {
	return extPreviewOpts{
		ineligible: []distill.ExtractSkip{
			{ID: 5, Reason: "dismissed"},
			{ID: 9, Reason: "already answered with trigger paths"},
			{ID: 12, Reason: "pin not present"},
		},
		skipped: []distill.ExtractSkip{{Reason: "pin 7 (lessons.yaml:31): no linked proposal"}},
		live:    map[int64]string{4: "Wrap SQL errors with the query name and the table."},
	}
}

// extPreview returns a preview in the shape of PlanExtract. Each item
// cites the evidence paths of its members; a pending row whose coverage
// differs from its regions discloses the change of a negative answer.
// The agent is the real claude preset.
func extPreview(o extPreviewOpts) *learning.ExtractPreview {
	rows := o.rows
	if rows == nil {
		rows = extRows()
	}

	if o.none {
		rows = nil
	}

	size := o.batch
	if size == 0 {
		size = 10
	}

	evidence := map[int64]string{
		101: "internal/api/handler.go", 102: "internal/api/store.go", 103: "internal/apix/db.go",
		104: "internal/store/store.go", 121: "internal/index/index.go", 122: "internal/index/config.go",
		150: "go.mod",
	}

	plan := &distill.ExtractPlan{Ineligible: o.ineligible, PlannedAt: extPlannedAt}

	for start := 0; start < len(rows); start += size {
		b := distill.ExtractBatch{Proposals: rows[start:min(start+size, len(rows))]}

		for _, row := range b.Proposals {
			it := distill.ExtractItem{ID: row.ID, Reason: distill.TriggerOpen}
			if row.TriggerChecked > 0 {
				it.Reason = distill.TriggerOpenObsoleteNegative
			}

			for _, m := range row.Members {
				if path, ok := evidence[m]; ok {
					it.Evidence = append(it.Evidence, path)
				}
			}

			if row.ID == 22 {
				it.Coverage, it.CoverageChanges = []string{"internal/apix", "internal/db"}, true
			}

			b.Items = append(b.Items, it)
		}

		b.PromptChars = 900 + 700*len(b.Proposals)
		b.Prompt = "fixture prompt"
		plan.Batches = append(plan.Batches, b)
		plan.Preflight.PromptChars += b.PromptChars
	}

	plan.Preflight.Proposals, plan.Preflight.Batches = len(rows), len(plan.Batches)

	return &learning.ExtractPreview{
		Request: extRequestOf(rows, o), Plan: plan, Invocation: agent.ClaudeCommand(), WorkDir: "/work/repo",
		RepoRoot: "/work/repo", DBPath: "/work/repo/.seamark/index.db", LiveNotes: o.live, Blockers: o.blockers,
		WorkDirErr: o.workDirErr, PlannedAt: extPlannedAt,
	}
}

// extRequestOf returns the request that selected the fixture: every
// planned and ineligible ID, and the Pins entries without a proposal.
func extRequestOf(rows []domain.Proposal, o extPreviewOpts) learning.ExtractRequest {
	req := learning.ExtractRequest{Skipped: o.skipped}

	for _, r := range rows {
		req.IDs = append(req.IDs, r.ID)
	}

	for _, s := range o.ineligible {
		req.IDs = append(req.IDs, s.ID)
	}

	return req
}

// extFake returns services whose plan returns p for any request, with
// the request of the caller. run is the scripted RunExtract; nil keeps
// the fake's default. The index is ready, because PlanExtract refuses
// to plan without a ready index.
func extFake(p *learning.ExtractPreview,
	run func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (*distill.ExtractExecution, error),
) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		out := *p
		out.Request = req

		return &out, nil
	}
	f.runExtract = run

	return f
}

// extExec returns an execution of p whose outcomes fill sets. The
// outcomes start unattempted, as the engine starts them.
func extExec(p *learning.ExtractPreview, fill func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution)) *distill.ExtractExecution {
	x := &distill.ExtractExecution{Batches: extOutcomes(p.Plan)}

	if fill != nil {
		fill(x.Batches, x)
	}

	return x
}

// extAnswered marks batch b completed with its traffic.
func extAnswered(b *distill.ExtractBatchOutcome) {
	b.Status = distill.BatchCompleted
	b.PromptChars, b.ReplyChars = 900+700*len(b.Items), 120*len(b.Items)
}

// extStored stores valid paths for item i. The first path is cited
// evidence, the others are unverified.
func extStored(it *distill.ExtractItemOutcome, paths ...string) {
	it.Status, it.NamedRaw, it.Named, it.Valid = distill.ExtractStored, len(paths), paths, paths

	for i, p := range paths {
		it.Facts = append(it.Facts, distill.TriggerFact{Path: p, Direct: i == 0, Selected: i == 0})
	}
}

// extNegative stores a negative answer for item i. omitted marks an
// item that a valid reply left out.
func extNegative(it *distill.ExtractItemOutcome, omitted bool) {
	it.Status, it.Omitted = distill.ExtractNegative, omitted
}

// extChange records a stored pending region change of item i.
func extChange(it *distill.ExtractItemOutcome, after ...string) {
	it.RegionsAfter, it.RegionsChanged = after, true
}

// extFailBatch fails batch b with a kind and a cause, as the engine does
// for a failed call or reply.
func extFailBatch(b *distill.ExtractBatchOutcome, kind distill.FailureKind, err error) {
	b.Status, b.Failure, b.Err = distill.BatchFailed, kind, err
	b.PromptChars = 900 + 700*len(b.Items)

	for i := range b.Items {
		b.Items[i].Status, b.Items[i].Failure, b.Items[i].Err = distill.ExtractFailed, kind, err
	}
}

// extCancelBatch cancels batch b, as a stop during its call does.
func extCancelBatch(b *distill.ExtractBatchOutcome) {
	b.Status, b.Err = distill.BatchCancelled, context.Canceled
	b.PromptChars = 900 + 700*len(b.Items)

	for i := range b.Items {
		b.Items[i].Status, b.Items[i].Err = distill.ExtractCancelled, context.Canceled
	}
}

// extOpen opens the review of p in Lessons › Run, as t does.
func extOpen(t *testing.T, f *fakeServices, p *learning.ExtractPreview, w, h int) *harness {
	t.Helper()

	hs := newHarness(t, f, w, h)
	hs.send(openExtractMsg{req: p.Request})
	require.Equal(t, lesRun, hookLessons(hs).sub)

	return hs
}

// extRecOf returns the extraction record that the Run sub-view shows.
func extRecOf(t *testing.T, h *harness) *extRecord {
	t.Helper()

	rec, ok := runViewOf(h).cur.(*extRecord)
	require.True(t, ok, "the Run sub-view shows no extraction record")

	return rec
}

// extStartHeld starts a reviewed run without running its worker, so a
// test can send progress events one by one. The harness runs commands
// on one goroutine, so a real worker would finish before any frame.
func extStartHeld(t *testing.T, h *harness) *extRecord {
	t.Helper()

	rec := extRecOf(t, h)
	require.Equal(t, stepPreview, rec.step)
	require.True(t, rec.runnable())

	require.NotNil(t, rec.start(), "start returns the worker and the wait")
	require.Equal(t, stepRunning, rec.step)

	return rec
}

// extSend delivers one progress event to the held record.
func extSend(h *harness, rec *extRecord, ev distill.ExtractEvent) {
	h.send(extEventMsg{rec: rec, ch: distClosed(), ev: ev, at: time.Now()})
}

// extStartedEv builds the start event of batch i.
func extStartedEv(rec *extRecord, i int) distill.ExtractEvent {
	return distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: i, Total: len(rec.run.units)}
}

// extDoneEv builds the done event of batch i with outcome b.
func extDoneEv(rec *extRecord, i int, b distill.ExtractBatchOutcome) distill.ExtractEvent {
	return distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: i, Total: len(rec.run.units), Batch: &b}
}

// extThreeBatches is the run fixture: the five rows in batches of two,
// so a run has three batches.
func extThreeBatches() extPreviewOpts {
	o := extMixed()
	o.batch = 2

	return o
}

// extMidRun drives a held run of extThreeBatches to "batch 2 of 3
// sending": batch 1 stored one answer and one negative answer.
func extMidRun(t *testing.T, h *harness) *extRecord {
	t.Helper()

	rec := extStartHeld(t, h)

	b := rec.run.units[0]
	extAnswered(&b)
	extStored(&b.Items[0], "internal/index/schema.go")
	extNegative(&b.Items[1], true)

	extSend(h, rec, extStartedEv(rec, 0))
	extSend(h, rec, extDoneEv(rec, 0, b))
	extSend(h, rec, extStartedEv(rec, 1))

	return rec
}

// extStoppedExec is the result of a stop during batch 2 of 3: batch 1
// answered, batch 2 cancelled, batch 3 never sent.
func extStoppedExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
		extAnswered(&bs[0])
		extStored(&bs[0].Items[0], "internal/index/schema.go")
		extNegative(&bs[0].Items[1], true)
		extCancelBatch(&bs[1])
		x.Stop, x.StopErr = distill.StopCancelled, context.Canceled
	})
}

// extTwoBatches is the result fixture: the five rows in batches of
// three, so the applied rows share the last batch.
func extTwoBatches() extPreviewOpts {
	o := extMixed()
	o.batch = 3

	return o
}

// extConflictExec is a run of extTwoBatches that answered both batches.
// A stored answer narrowed pending regions, and an omitted negative set
// the coverage. A negative named a path that does not exist. An applied
// row stored trigger paths, and its live note differs. A conflict in the
// last batch ends the run.
func extConflictExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
		for i := range bs {
			extAnswered(&bs[i])
		}

		extStored(&bs[0].Items[0], "internal/index/schema.go", "internal/index/migrate.go")
		extChange(&bs[0].Items[0], "internal/index/schema.go")
		extNegative(&bs[0].Items[1], true)
		extChange(&bs[0].Items[1], "internal/apix", "internal/db")
		extNegative(&bs[0].Items[2], false)
		bs[0].Items[2].NamedRaw, bs[0].Items[2].Named = 1, []string{"internal/api/gone.go"}

		bs[1].Items[0].Status = distill.ExtractConflict
		bs[1].Items[0].Err = fmt.Errorf("%w: p6 changed during the agent call; its answer was not saved",
			distill.ErrExtractStale)
		extStored(&bs[1].Items[1], "internal/store/query.go", "internal/store/tx.go")

		x.Stop = distill.StopConflict
		x.StopErr = bs[1].Items[0].Err
	})
}

// extInputChangedExec is a run of extThreeBatches that a conflict in
// batch 1 stopped. Batch 1 is saved; batches 2 and 3 never ran.
func extInputChangedExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
		extAnswered(&bs[0])
		extStored(&bs[0].Items[0], "internal/index/schema.go")

		bs[0].Items[1].Status = distill.ExtractConflict
		bs[0].Items[1].Err = fmt.Errorf("%w: p22 changed during the agent call; its answer was not saved",
			distill.ErrExtractStale)

		x.Stop, x.StopErr = distill.StopConflict, bs[0].Items[1].Err
	})
}

// extCompletedExec is a run of extTwoBatches that saved an answer for
// every proposal.
func extCompletedExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, _ *distill.ExtractExecution) {
		for i := range bs {
			extAnswered(&bs[i])
		}

		extStored(&bs[0].Items[0], "internal/index/schema.go")
		extChange(&bs[0].Items[0], "internal/index")
		extNegative(&bs[0].Items[1], true)
		extNegative(&bs[0].Items[2], false)
		extNegative(&bs[1].Items[0], false)
		extStored(&bs[1].Items[1], "internal/store/query.go")
	})
}

// extStoreErrorExec is a run of extThreeBatches whose store failed in
// batch 2. Batch 1 is saved, and batch 3 never ran.
func extStoreErrorExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
		extAnswered(&bs[0])
		extStored(&bs[0].Items[0], "internal/index/schema.go")
		extNegative(&bs[0].Items[1], false)
		extFailBatch(&bs[1], distill.FailStore, errors.New("database is locked"))
		x.Stop, x.StopErr = distill.StopStoreError, errors.New("database is locked")
	})
}

// extPanicRun is a run of extThreeBatches that saved batch 1, started
// batch 2, and then panicked, so the record shows an internal error.
func extPanicRun(_ context.Context, rp *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
	*distill.ExtractExecution, error,
) {
	x := extStoppedExec(rp)
	b0 := x.Batches[0]

	progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 0, Total: 3})
	progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 0, Total: 3, Batch: &b0})
	progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 1, Total: 3})

	panic("store handle lost")
}
