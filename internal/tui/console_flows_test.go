package tui

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The cross-screen flow tests. Three screens share these flows: the
// Pins sub-view, the maintenance preview, and the extraction record.
// Each screen has its own tests. These tests drive the real console by
// key presses across all three, and they assert the exact service calls.
// The services are fakes, so a test can change the pin file that the
// next read returns, as a commit does.

// maintAppliedRows returns the applied rows of the maintenance fixture after f
// changes them, so a test can build the ledger that a commit leaves.
func maintAppliedRows(f func(rows []domain.Proposal) []domain.Proposal) []domain.Proposal {
	return f(maintRows())
}

// maintInspectionOf returns the Pins snapshot of a written pin file. The real
// reader parses the bytes, and the real classifier links them to the
// ledger rows, so the snapshot is what the engine reads after a commit.
func maintInspectionOf(t *testing.T, data []byte, applied, superseded []domain.Proposal) *maintain.Inspection {
	t.Helper()

	doc := pinfile.Parse(data)
	require.False(t, doc.Unreadable(), "the written pin file reads")

	links := pinfile.Classify(doc, applied, nil, superseded)
	pins := make([]maintain.Pin, len(doc.Entries))

	for i, e := range doc.Entries {
		pins[i] = maintain.Pin{Entry: e, Link: links.Entries[i]}
	}

	return &maintain.Inspection{
		Doc: doc, Links: links, Pins: pins, Superseded: superseded,
		Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
		At:    time.Date(2026, 10, 3, 9, 5, 0, 0, time.UTC),
	}
}

// maintCommitFake returns services over the maintenance fixture. The plan
// read records each request and returns plan; the commit stores the
// next Pins snapshot, as a real commit changes the file that the next
// read sees.
func maintCommitFake(fx maintFix, plan *maintain.Plan, after *maintain.Inspection, reqs *[]maintain.Request,
	got **maintain.Plan,
) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.pins = fx.inspection()
	f.planMaintenance = func(req maintain.Request) (*maintain.Plan, error) {
		*reqs = append(*reqs, req)

		return plan, nil
	}
	f.commitMaintenance = func(q *maintain.Plan) (maintain.Result, error) {
		*got = q
		f.pins = after

		return maintain.Result{
			Action: q.Action, FileWritten: true, LedgerDone: true, Moved: maintLedgerIDs(q),
			Note: "the pin file and the ledger changed together",
		}, nil
	}

	return f
}

// assertNoLossNote checks that no refresh reported a user-caused change as a
// loss: neither the status row nor the frame names a lost pin.
func assertNoLossNote(t *testing.T, h *harness) {
	t.Helper()

	for _, text := range []string{h.m.toast.text, h.view()} {
		assert.NotContains(t, text, "left this list")
		assert.NotContains(t, text, "left the selection")
	}
}

// cursorPin returns the installed pin under the Pins cursor.
func cursorPin(t *testing.T, h *harness) *maintain.Pin {
	t.Helper()

	p := pinsOf(h)

	cur, ok := p.current(p.rows())
	require.True(t, ok, "the Pins list has a cursor row")
	require.NotNil(t, cur.pin, "the cursor row is an installed pin")

	return cur.pin
}

// extPreviewFor returns an extraction preview in the shape of PlanExtract for
// req. It plans the rows of req.IDs that rows holds, in batches of size,
// and it keeps the request of the caller.
func extPreviewFor(req learning.ExtractRequest, rows []domain.Proposal, size int) *learning.ExtractPreview {
	var sel []domain.Proposal

	for _, id := range req.IDs {
		if i := slices.IndexFunc(rows, func(r domain.Proposal) bool { return r.ID == id }); i >= 0 {
			sel = append(sel, rows[i])
		}
	}

	p := extPreview(extPreviewOpts{rows: sel, batch: size, none: len(sel) == 0})
	p.Request = req

	return p
}

// extPlanFake returns services whose extraction plan records each
// request and plans the matching rows one per batch.
func extPlanFake(f *fakeServices, rows []domain.Proposal, reqs *[]learning.ExtractRequest) *fakeServices {
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		*reqs = append(*reqs, req)

		return extPreviewFor(req, rows, 1), nil
	}

	return f
}

// linkedPinRows returns the applied rows of the linked pins of in, in
// file order.
func linkedPinRows(in *maintain.Inspection) []domain.Proposal {
	var out []domain.Proposal

	for _, pin := range in.Pins {
		if pin.Link.Link == pinfile.LinkLinked {
			out = append(out, pin.Link.Applied...)
		}
	}

	return out
}

// extStoreAllRun returns a run that answers every batch with one stored
// path per item and sends the typed events of each batch.
func extStoreAllRun(path string) func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
	*distill.ExtractExecution, error,
) {
	return func(_ context.Context, p *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		x := extExec(p, nil)
		n := len(x.Batches)

		for i := range x.Batches {
			progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: i, Total: n})

			extAnswered(&x.Batches[i])

			for j := range x.Batches[i].Items {
				extStored(&x.Batches[i].Items[j], path)
			}

			b := x.Batches[i]
			progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: i, Total: n, Batch: &b})
		}

		return x, nil
	}
}

func TestConsoleFlowRetargetFromPinsRefreshesThePinWithoutALossNote(t *testing.T) {
	var (
		reqs []maintain.Request
		got  *maintain.Plan
	)

	fx := maintFixture(t)
	plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

	// The ledger after the commit: p4 records the reviewed regions.
	rows := maintAppliedRows(func(rows []domain.Proposal) []domain.Proposal {
		rows[0].Region, rows[0].Regions = "internal/store/sql", []string{"internal/store/sql"}

		return rows
	})
	after := maintInspectionOf(t, plan.Change.After, rows, nil)
	require.Equal(t, pinfile.LinkLinked, after.Pins[maintWrap].Link.Link, "the retargeted pin stays linked")

	f := maintCommitFake(fx, plan, after, &reqs, &got)
	h := pinsOpen(t, f, 120, 40)

	require.Equal(t, "wrap-sql-errors", cursorPin(t, h).Entry.Rule)
	require.True(t, maintHasKey(h, "w"), "a linked, editable pin offers the retarget")

	// w sends the cursor row; the preview plans it and offers y.
	h.press("w")
	require.Equal(t, []maintain.Request{{
		Action: maintain.ActionRetarget, Refs: []pinfile.EntryRef{fx.pins[maintWrap].Entry.Ref()}, Cursor: true,
	}}, reqs)
	require.True(t, maintHasKey(h, "y"))
	assert.Contains(t, maintContent(t, h), "Retarget 1 pin · y writes lessons.yaml and the ledger")

	reads := f.readCount("Pins")

	h.press("y")
	require.Same(t, plan, got, "y commits the reviewed plan")
	assert.Equal(t, []string{"PlanMaintenance:retarget", "CommitMaintenance:retarget"}, f.called())
	assert.False(t, h.m.st.busy(), "the result frees the slot")
	assert.Greater(t, f.readCount("Pins"), reads, "the result reads the pins again")
	assert.Contains(t, maintContent(t, h), "Retargeted 1 pin")

	// The Pins list shows the written file. The cursor stays on the pin
	// that the user retargeted, and no note calls it lost.
	p := pinsOf(h)
	require.Same(t, after, p.inspection())

	pin := cursorPin(t, h)
	assert.Equal(t, "wrap-sql-errors", pin.Entry.Rule)
	assert.Equal(t, []string{"internal/store/sql"}, pinEntryRegions(pin.Entry))
	assert.Equal(t, pin.Entry.Ref(), p.cursor.ref, "the cursor binds to the new identity")
	assertNoLossNote(t, h)

	h.press("enter")
	assert.False(t, maintHostOf(h).open())
	assertNoLossNote(t, h)
	assert.Len(t, f.called(), 2, "closing the result calls nothing")
}

func TestConsoleFlowPruneOfAMarkedPinKeepsTheCursorByIdentity(t *testing.T) {
	var (
		reqs []maintain.Request
		got  *maintain.Plan
	)

	fx := maintFixture(t)
	plan := fx.plan(t, maintain.ActionPrune, maintPlanOpts{write: true}, maintSpec{
		entry: maintOnError, status: maintain.ItemReady,
		reason: "no retained equivalent is established; this is a deliberate retirement",
	})

	// The ledger after the commit: p16 moved to superseded.
	var gone domain.Proposal

	rows := maintAppliedRows(func(rows []domain.Proposal) []domain.Proposal {
		i := slices.IndexFunc(rows, func(r domain.Proposal) bool { return r.ID == 16 })
		gone = rows[i]
		gone.Status = domain.ProposalSuperseded

		return slices.Delete(rows, i, i+1)
	})
	after := maintInspectionOf(t, plan.Change.After, rows, []domain.Proposal{gone})
	require.Len(t, after.Pins, 4, "the prune removes one entry")

	f := maintCommitFake(fx, plan, after, &reqs, &got)
	h := pinsOpen(t, f, 120, 40)

	// Mark p16 and leave the cursor on a later pin: the marked entry is
	// the target, not the cursor row.
	pinsCursorTo(h, maintOnError+1)
	h.press("space")
	pinsCursorTo(h, maintNilErrors+1)
	require.Equal(t, "check-nil-errors", cursorPin(t, h).Entry.Rule)

	h.press("-")
	require.Equal(t, []maintain.Request{{
		Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{fx.pins[maintOnError].Entry.Ref()},
	}}, reqs)
	assert.Contains(t, maintContent(t, h), "No retained equivalent is established; this is a deliberate retirement.")
	require.True(t, maintHasKey(h, "y"))

	h.press("y")
	require.Same(t, plan, got)
	assert.Equal(t, []string{"PlanMaintenance:prune", "CommitMaintenance:prune"}, f.called())

	text := maintContent(t, h)
	assert.Contains(t, text, "Pruned 1 pin")
	assert.Contains(t, text, "Ledger p16 › superseded")

	// The pruned entry left the selection without a note. The cursor pin
	// moved up one row and the cursor followed it.
	p := pinsOf(h)
	assert.Empty(t, p.selected, "the pruned entry leaves the selection")
	assert.Equal(t, "check-nil-errors", cursorPin(t, h).Entry.Rule, "the cursor follows the pin, not the row")
	assert.Equal(t, maintNilErrors-1, p.list.cursor)
	assertNoLossNote(t, h)

	// The superseded history shows the pruned proposal, read-only.
	h.press("enter", "f", "f")
	require.Equal(t, pinSuperseded, p.filter)

	cur, ok := p.current(p.rows())
	require.True(t, ok)
	assert.Equal(t, int64(16), cur.prop.ID)
	assert.False(t, maintHasKey(h, "space"), "history rows are read-only")
	assert.Len(t, f.called(), 2, "browsing the history calls nothing")
}

func TestConsoleFlowGateOffRoutesToSettingsAndNeedsAFreshPreview(t *testing.T) {
	var got *maintain.Plan

	fx := maintFixture(t)
	svc := settingsFake(t, "distill:\n  write: false\n")
	svc.ledger = lesLedger()
	svc.pins = fx.inspection()

	// The plan reads distill.write from the file, as the engine does at
	// plan time (D-14).
	svc.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
		data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(settings.File)))
		require.NoError(t, err)

		return fx.maintNarrow(t, maintPlanOpts{write: strings.Contains(string(data), "write: true"), cursor: true}), nil
	}
	svc.commitMaintenance = func(q *maintain.Plan) (maintain.Result, error) {
		got = q

		return maintain.Result{Action: q.Action, FileWritten: true, LedgerDone: true, Moved: []int64{4}}, nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "m")
	require.Equal(t, lesPins, hookLessons(h).sub)

	// The gate is off: the preview shows the change, offers o, and no y.
	h.press("w")

	first := maintHostOf(h).modal
	require.NotNil(t, first)
	assert.Contains(t, maintContent(t, h), "! Retarget 1 pin · y is off: distill.write is off")
	assert.False(t, maintHasKey(h, "y"))
	require.True(t, maintHasKey(h, "o"))

	h.press("y")
	assert.Empty(t, maintMutations(svc.fakeServices), "y does nothing while the gate is off")

	// o closes the preview and opens Settings › Pin writing.
	h.press("o")
	require.Equal(t, destSettings, h.m.active)
	assert.False(t, maintHostOf(h).open(), "a preview of the old gate closes")

	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, 1, svc.callCount("SaveSettings"))
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	// Back in Pins, no preview waits. The old plan cannot be confirmed;
	// only a fresh preview offers y.
	h.press("3")
	require.Equal(t, lesPins, hookLessons(h).sub)
	assert.False(t, maintHostOf(h).open(), "the save leaves no preview of the old gate")

	h.press("y")
	assert.Empty(t, maintMutations(svc.fakeServices), "y in the list commits nothing")

	h.press("w")
	require.True(t, maintHostOf(h).open())
	assert.NotSame(t, first, maintHostOf(h).modal, "w builds a new preview")
	require.True(t, maintHasKey(h, "y"), "the fresh preview reads the new gate")

	h.press("y")
	require.NotNil(t, got)
	assert.True(t, got.Write, "the commit gets the fresh plan")
	assert.Equal(t, []string{
		"PlanMaintenance:retarget", "PlanSettings", "SaveSettings", "PlanMaintenance:retarget",
		"CommitMaintenance:retarget",
	}, svc.called())
}

// extStoppedPinsRun is the run of the Pins extraction flow: batch 1 stores a
// path, the user stops during batch 2 with x and y, and batch 3 never
// starts.
func extStoppedPinsRun(t *testing.T, hp **harness) func(context.Context, *learning.ExtractPreview,
	func(distill.ExtractEvent)) (*distill.ExtractExecution, error) {
	return func(ctx context.Context, p *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		h := *hp
		x := extExec(p, nil)
		n := len(x.Batches)
		require.Equal(t, 3, n)

		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 0, Total: n})
		extAnswered(&x.Batches[0])
		extStored(&x.Batches[0].Items[0], "internal/store/sql.go")

		b0 := x.Batches[0]
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 0, Total: n, Batch: &b0})
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 1, Total: n})

		// The record runs. x asks first; y stops and cancels the call.
		rec := extRecOf(t, h)
		require.Equal(t, extRunning, rec.step)
		require.NoError(t, ctx.Err())

		h.press("x")
		require.True(t, rec.capturing(), "x asks before it stops")
		require.NoError(t, ctx.Err(), "the question alone stops nothing")

		h.press("y")
		require.ErrorIs(t, ctx.Err(), context.Canceled, "y cancels the run context")

		extCancelBatch(&x.Batches[1])

		b1 := x.Batches[1]
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 1, Total: n, Batch: &b1})
		progress(distill.ExtractEvent{Kind: distill.ExtractStopped, Index: 2, Total: n, Stop: distill.StopCancelled,
			Err: context.Canceled})

		x.Stop, x.StopErr = distill.StopCancelled, context.Canceled

		return x, nil
	}
}

// pinsMixedReview opens Pins and marks a mixed selection: three
// linked pins, a pin without a proposal, a pending match, and a
// duplicate pin. t then opens the extraction review. The run is
// extStoppedPinsRun. It returns the console at the review, the record and the
// recorded requests.
func pinsMixedReview(t *testing.T) (*harness, *fakeServices, *extRecord, *[]learning.ExtractRequest) {
	t.Helper()

	var h *harness

	reqs := &[]learning.ExtractRequest{}
	f := extPlanFake(pinsFake(), linkedPinRows(pinsInspection()), reqs)
	f.runExtract = extStoppedPinsRun(t, &h)

	h = pinsOpen(t, f, 120, 40)

	for _, n := range []int{1, 2, 3, 5, 6, 7} {
		pinsCursorTo(h, n)
		h.press("space")
	}

	require.Len(t, pinsOf(h).selected, 6)
	require.True(t, maintHasKey(h, "t"), "a linked target offers t")

	h.press("t")
	require.Len(t, *reqs, 1)
	require.Equal(t, lesRun, hookLessons(h).sub)

	rec := extRecOf(t, h)
	require.Equal(t, extReview, rec.step)

	return h, f, rec, reqs
}

func TestConsoleFlowPinsExtractionOfTheCursorPinAsksItsProposal(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := extPlanFake(pinsFake(), linkedPinRows(pinsInspection()), &reqs)
	f.runExtract = extNoRun(t)

	h := pinsOpen(t, f, 120, 40)

	// t on the linked cursor pin asks about its one proposal. Pin 1 has
	// stored trigger paths, so it offers no t; pin 2 has an open question.
	require.False(t, maintHasKey(h, "t"), "an answered question offers no extraction")
	pinsCursorTo(h, 2)
	require.Equal(t, "check-nil-errors", cursorPin(t, h).Entry.Rule)
	require.True(t, maintHasKey(h, "t"))

	h.press("t")
	require.Equal(t, []learning.ExtractRequest{{IDs: []int64{9}}}, reqs)
	require.Equal(t, lesRun, hookLessons(h).sub)
	assert.True(t, extRecOf(t, h).runnable())
	assert.Contains(t, footerLine(h.view()), "y start run")

	// esc drops the review, which did no work; m returns to Pins.
	h.press("esc")
	assert.Nil(t, runViewOf(h).cur, "the dropped review leaves no record")

	h.press("m")
	require.Equal(t, lesPins, hookLessons(h).sub)
	assert.Equal(t, []string{"PlanExtract"}, f.called())
}

func TestConsoleFlowPinsExtractionStopsAndPreparesTheRemaining(t *testing.T) {
	h, f, rec, reqs := pinsMixedReview(t)

	// Only linked pins give IDs; every other marked entry is skipped with
	// its reason, and the review lists it as not asked.
	assert.Equal(t, []int64{4, 9, 12}, (*reqs)[0].IDs)
	assert.Equal(t, []distill.ExtractSkip{
		{Reason: `pin "keep-changelog-current": no linked proposal`},
		{ID: 3, Reason: `pin "prefer-table-tests": matches a pending proposal; record it as applied first`},
		{Reason: `pin "dup-rule": ambiguous: duplicate pins`},
	}, (*reqs)[0].Skipped)

	assert.Contains(t, extTallFlat(h), "Sends 3 proposals in 3 batches · 3 ineligible")

	// The compact layout shows the Proposals pane after tab, in one
	// tall box, so no reason is cut.
	h.resize(80, 120)
	h.press("tab")

	review := lesBoxFlat(h.view())
	assert.Contains(t, review, "NOT ASKED")
	assert.Contains(t, review, `pin "keep-changelog-current": no linked proposal`)
	assert.Contains(t, review, `pin "prefer-table-tests": matches a pending proposal; record it as applied first`)
	assert.Contains(t, review, `pin "dup-rule": ambiguous: duplicate pins`)

	h.press("tab")
	h.resize(120, 40)
	require.True(t, rec.runnable())

	// y runs; inside the run x and y stop it (extStoppedPinsRun).
	h.press("y")
	require.Equal(t, extResult, rec.step)

	// The result names the stop, the cancelled and the unattempted IDs.
	flat := extTallFlat(h)
	assert.Contains(t, flat, "Trigger extraction stopped")
	assert.Contains(t, flat, "You stopped the run.")
	assert.Contains(t, flat, "Cancelled p9")
	assert.Contains(t, flat, "Not attempted p12")
	assert.NotContains(t, flat, "✓ completed", "a stopped run is never a success")
	assert.False(t, h.m.st.busy())
	assert.Nil(t, h.m.st.run)
	assert.Equal(t, []int64{9, 12}, rec.remaining())

	// n prepares a new preview of the open questions; it runs nothing.
	h.press("n")

	next := extRecOf(t, h)
	require.NotSame(t, rec, next)
	assert.Equal(t, extReview, next.step, "the new record waits for its own y")
	require.Len(t, *reqs, 2)
	assert.Equal(t, learning.ExtractRequest{IDs: []int64{9, 12}}, (*reqs)[1])
	assert.Equal(t, []string{"PlanExtract", "RunExtract", "PlanExtract"}, f.called())
}

func TestConsoleFlowExtractionResultLinksToThePinWithoutAPreview(t *testing.T) {
	h, f, rec, _ := pinsMixedReview(t)
	p := pinsOf(h)
	marked := maps.Clone(p.selected)

	cur, ok := p.current(p.rows())
	require.True(t, ok)
	require.Equal(t, "dup-rule", cur.pin.Entry.Rule, "the Pins cursor rests on the last marked pin")

	h.press("y")
	require.Equal(t, extResult, rec.step)

	// The result opens on the first open question (p9). p4 is applied
	// and stored a trigger path, so enter on it shows its pin.
	require.Equal(t, 1, rec.run.cursor)
	h.press("up")
	assert.Contains(t, footerLine(h.view()), "enter open pins")

	calls := f.called()

	h.press("enter")
	require.Equal(t, destLessons, h.m.active)
	require.Equal(t, lesPins, hookLessons(h).sub, "the link shows Pins")
	assert.Equal(t, "wrap-sql-errors", cursorPin(t, h).Entry.Rule, "the cursor is on the pin of p4")
	assert.Equal(t, pinInstalled, p.filter)
	assert.False(t, p.host.open(), "the link opens no preview")
	assert.Equal(t, marked, p.selected, "the link selects nothing")
	assert.Equal(t, calls, f.called(), "the link plans, commits and runs nothing")
}

func TestConsoleFlowProposalsExtractionOfAPendingAndAnAppliedRow(t *testing.T) {
	var reqs []learning.ExtractRequest

	fx := maintFixture(t)
	f := newFake()
	f.ledger = lesLedger()
	f.pins = fx.inspection()
	extPlanFake(f, append(slices.Clone(f.ledger.Pending), f.ledger.Applied...), &reqs)
	f.runExtract = extStoreAllRun("internal/api/handler.go")

	h := lesOpen(t, f, 120, 40)

	// The Pins cursor rests on another pin, so the later link must move
	// it.
	h.press("m")
	pinsCursorTo(h, maintNilErrors+1)
	require.Equal(t, "check-nil-errors", cursorPin(t, h).Entry.Rule)

	// A pending row: t asks about it, y runs, and the result links back
	// to the proposal.
	h.press("p")
	require.Equal(t, propPending, lesProps(h).filter)
	require.Equal(t, int64(1), lesProps(h).cursorID)
	require.True(t, maintHasKey(h, "t"), "the pending row has an open question")

	h.press("t")
	require.Equal(t, []learning.ExtractRequest{{IDs: []int64{1}}}, reqs)
	require.Equal(t, lesRun, hookLessons(h).sub)

	first := extRecOf(t, h)
	h.press("y")
	require.Equal(t, extResult, first.step)
	assert.Contains(t, footerLine(h.view()), "enter open proposals")

	h.press("enter")
	require.Equal(t, lesProposals, hookLessons(h).sub)
	assert.Equal(t, int64(1), lesProps(h).cursorID, "the link shows the answered proposal")

	// An applied row: t asks about it, and the result links to its pin.
	h.press("f")
	require.Equal(t, propApplied, lesProps(h).filter)
	require.Equal(t, int64(4), lesProps(h).cursorID)
	require.True(t, maintHasKey(h, "t"), "the applied row of a linked pin has an open question")

	h.press("t")
	require.Len(t, reqs, 2)
	assert.Equal(t, learning.ExtractRequest{IDs: []int64{4}}, reqs[1])

	second := extRecOf(t, h)
	require.NotSame(t, first, second)

	h.press("y")
	require.Equal(t, extResult, second.step)
	assert.Contains(t, extTallFlat(h), "the pin scope stays", "an applied answer never changes the pin scope")
	assert.Contains(t, footerLine(h.view()), "enter open pins")

	h.press("enter")
	require.Equal(t, lesPins, hookLessons(h).sub)
	assert.Equal(t, "wrap-sql-errors", cursorPin(t, h).Entry.Rule, "the link shows the pin of p4")
	assert.False(t, maintHostOf(h).open(), "the link opens no preview")
	assert.Empty(t, pinsOf(h).selected, "the link selects nothing")
	assert.Equal(t, []string{"PlanExtract", "RunExtract", "PlanExtract", "RunExtract"}, f.called())
}

func TestConsoleFlowExtractionRunKeepsReadsAndRefusesOtherWrites(t *testing.T) {
	var (
		h    *harness
		reqs []learning.ExtractRequest
		rec  *extRecord
	)

	fx := maintFixture(t)
	f := newFake()
	f.ledger = lesLedger()
	f.pins = fx.inspection()
	f.settings = fakeSettingsSnapshot(t, "distill:\n  write: false\n")
	extPlanFake(f, maintRows(), &reqs)
	f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
		return fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true}), nil
	}
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}

	// during runs inside the agent call of batch 1. The harness runs
	// commands on one goroutine, so the keys reach the console while the
	// run holds the mutation slot.
	during := func(ctx context.Context) {
		require.Equal(t, extractRunName, h.m.st.mutation)
		require.Equal(t, extRunning, rec.step)

		// Every destination shows the run in the notice row.
		for _, d := range []struct {
			key  string
			dest destination
		}{{"1", destOverview}, {"2", destIntegrations}, {"4", destDoctor}, {"5", destSettings}} {
			h.press(d.key)
			require.Equal(t, d.dest, h.m.active)
			assert.Contains(t, noticeRow(h), "Extracting triggers", "the run notice shows on %v", d.dest)
		}

		// A Settings save waits for the run: s plans nothing.
		h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
		h.press("enter", "down", "space", "enter")
		require.Equal(t, 1, settingsScr(h).draftTotal())

		h.press("s")
		assert.Contains(t, h.view(), "Saving waits for the running operation: Extracting trigger information.")

		// The draft asks before the user leaves; d discards it.
		h.press("3", "d")
		require.Equal(t, destLessons, h.m.active)
		require.Equal(t, lesRun, hookLessons(h).sub, "Lessons returns to the running record")

		// Pins reads still work.
		h.press("m")
		require.Equal(t, lesPins, hookLessons(h).sub)

		reads := f.readCount("Pins")
		h.press("r")
		assert.Greater(t, f.readCount("Pins"), reads, "r reads the pins during the run")
		assert.Contains(t, noticeRow(h), "Extracting triggers")

		// A retarget preview opens and reads its plan, but y waits.
		h.press("w")
		require.True(t, maintHostOf(h).open())

		text := maintContent(t, h)
		assert.Contains(t, text, "Extracting trigger information is running. y is off until it ends.")
		assert.Contains(t, text, "y waits: another operation is running")
		assert.False(t, maintHasKey(h, "y"), "no y while the run holds the slot")

		h.press("y")
		assert.Zero(t, f.callCount("CommitMaintenance:retarget"), "y commits nothing during the run")
		// The preview says that y waits, so no toast hides under the modal.
		assert.NotContains(t, h.m.toast.text, "Another operation is running")
		assert.True(t, maintHostOf(h).open(), "the preview stays for a later y")

		// A resize keeps the preview and the run.
		h.resize(minWidth, minHeight)
		assertFrame(t, h.view(), minWidth, minHeight)
		h.resize(120, 40)
		require.True(t, maintHostOf(h).open())

		h.press("esc")
		require.False(t, maintHostOf(h).open())

		// A second extraction request waits for the running one.
		h.press("t")
		assert.Same(t, rec, runViewOf(h).cur, "the running record keeps the Run view")
		assert.Len(t, reqs, 1, "the refused request plans nothing")

		// A proposal decision previews, but its y waits too.
		h.press("p", "d")
		require.Equal(t, lesProposals, hookLessons(h).sub)
		require.NotNil(t, lesProps(h).dec, "d opens the decision preview")

		h.press("y")
		assert.Zero(t, f.callCount("CommitDecision:dismiss"), "y commits no decision during the run")
		assert.Contains(t, h.m.toast.text, "Another operation is running: Extracting trigger information")

		h.press("esc")
		require.Nil(t, lesProps(h).dec)

		// The Overview run item leads back to the running record.
		h.press("1")
		ovSelect(t, h, "learn:run")
		h.press("enter")
		require.Equal(t, destLessons, h.m.active)
		require.Equal(t, lesRun, hookLessons(h).sub)
		assert.Same(t, rec, extRecOf(t, h))
		assert.Equal(t, extRunning, rec.step)
		require.NoError(t, ctx.Err(), "browsing never stops the run")
	}

	f.runExtract = func(ctx context.Context, p *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		x := extExec(p, nil)
		n := len(x.Batches)

		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 0, Total: n})
		during(ctx)

		extAnswered(&x.Batches[0])
		extStored(&x.Batches[0].Items[0], "internal/store/sql.go")

		b := x.Batches[0]
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 0, Total: n, Batch: &b})

		return x, nil
	}

	h = lesOpen(t, f, 120, 40)
	h.press("m", "t")
	require.Equal(t, []learning.ExtractRequest{{IDs: []int64{4}}}, reqs)

	rec = extRecOf(t, h)
	h.press("y")
	require.Equal(t, extResult, rec.step)
	assert.False(t, h.m.st.busy())

	// After the run, the same preview offers y again.
	h.press("m", "w")
	assert.True(t, maintHasKey(h, "y"), "the free slot gives y back")
	assert.Equal(t, []string{
		"PlanExtract", "RunExtract", "PlanMaintenance:retarget", "PreviewDecision:dismiss", "PlanMaintenance:retarget",
	}, f.called())
}

func TestConsoleFlowResizeToTheMinimumKeepsTheConfirmAndTheScope(t *testing.T) {
	fx := maintFixture(t)

	// check asserts the first scope line and the action of the open
	// maintenance preview at the current size.
	check := func(t *testing.T, h *harness, label string) {
		t.Helper()

		frame := h.view()
		assertFrame(t, frame, h.m.st.width, h.m.st.height)

		v := maintHostOf(h).modal.content(modalBodyWidth(modalMinW, h.m.st.width))
		require.NotEmpty(t, v.tail)
		assert.Contains(t, frame, strings.TrimSpace(ansi.Strip(v.tail[0])))
		assert.Contains(t, frame, label)
	}

	t.Run("retarget", func(t *testing.T) {
		var (
			reqs []maintain.Request
			got  *maintain.Plan
		)

		plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		f := maintCommitFake(fx, plan, fx.inspection(), &reqs, &got)
		h := pinsOpen(t, f, 120, 40)

		h.press("w")
		check(t, h, "y retarget 1 pin")

		h.resize(minWidth, minHeight)
		check(t, h, "y retarget 1 pin")

		h.press("pgdown", "pgdown", "end")
		check(t, h, "y retarget 1 pin")

		// Below the minimum the preview is hidden, so y never reaches it.
		h.resize(minWidth-1, minHeight-1)
		assert.Contains(t, h.view(), "More space needed")

		h.press("y")
		assert.Nil(t, got, "y never confirms a hidden preview")

		h.resize(minWidth, minHeight)
		check(t, h, "y retarget 1 pin")

		h.press("y")
		require.Same(t, plan, got, "y after the resize commits the reviewed plan")
		assert.Equal(t, []string{"PlanMaintenance:retarget", "CommitMaintenance:retarget"}, f.called())
	})

	t.Run("prune", func(t *testing.T) {
		var (
			reqs []maintain.Request
			got  *maintain.Plan
		)

		plan := fx.maintPrune(t, true)
		f := maintCommitFake(fx, plan, fx.inspection(), &reqs, &got)
		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, maintOnError+1)

		h.press("-")
		h.resize(minWidth, minHeight)
		check(t, h, "y prune 1 pin")

		h.press("end")
		check(t, h, "y prune 1 pin")
		assert.Equal(t, []string{"PlanMaintenance:prune"}, f.called())
	})

	t.Run("extraction review", func(t *testing.T) {
		h, f, rec, _ := pinsMixedReview(t)

		h.resize(minWidth, minHeight)

		for _, keys := range [][]string{nil, {"pgdown", "pgdown", "pgdown"}} {
			h.press(keys...)

			frame := h.view()
			assertFrame(t, frame, minWidth, minHeight)
			assert.Contains(t, frame, "Sends 3 proposals in 3 batches · 3 ineligible")
			assert.Contains(t, frame, "Nothing is sent until you press y.")
			assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(frame)), "y start run"))
		}

		require.True(t, rec.runnable())

		// Below the minimum the review is hidden, so y starts nothing.
		h.resize(minWidth-1, minHeight-1)
		h.press("y")
		assert.Equal(t, extReview, rec.step, "y never starts a hidden review")
		assert.Equal(t, []string{"PlanExtract"}, f.called())
	})
}

func TestConsoleFlowEscAtEveryStageChangesNothing(t *testing.T) {
	fx := maintFixture(t)

	t.Run("maintenance preparing and previews", func(t *testing.T) {
		var h *harness

		plans := map[string]*maintain.Plan{
			"ready":    fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true}),
			"gate off": fx.maintNarrow(t, maintPlanOpts{cursor: true}),
			"revise":   fx.maintMixed(t),
		}

		f := newFake()
		f.ledger = lesLedger()
		f.pins = fx.inspection()

		next, escInRead := "ready", true
		f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
			if escInRead {
				// esc arrives while the plan read runs: the preview
				// leaves its Preparing stage, and the late plan opens
				// nothing.
				escInRead = false

				require.Equal(t, maintStagePreparing, maintHostOf(h).modal.stage)
				h.press("esc")
				require.False(t, maintHostOf(h).open())
			}

			return plans[next], nil
		}

		h = pinsOpen(t, f, 120, 40)

		h.press("w")
		assert.False(t, maintHostOf(h).open(), "a late plan reopens nothing")

		for _, name := range []string{"ready", "gate off", "revise"} {
			next = name

			h.press("w")
			require.True(t, maintHostOf(h).open(), name)
			require.Equal(t, maintStagePreview, maintHostOf(h).modal.stage, name)

			h.press("esc")
			assert.False(t, maintHostOf(h).open(), "%s: esc closes the preview", name)
		}

		assert.Equal(t, destLessons, h.m.active)
		assert.Equal(t, lesPins, hookLessons(h).sub)
		assert.False(t, h.m.st.busy())
		assert.Equal(t, []string{
			"PlanMaintenance:retarget", "PlanMaintenance:retarget", "PlanMaintenance:retarget",
			"PlanMaintenance:retarget",
		}, f.called())
	})

	t.Run("maintenance result and recovery", func(t *testing.T) {
		var h *harness

		plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		f := maintFakeOf(fx, plan, maintPartial(fx, true))

		escInRead := true
		f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) {
			if escInRead {
				escInRead = false

				require.Equal(t, maintStagePreparing, maintHostOf(h).modal.stage)
				h.press("esc")
				require.Equal(t, maintStageResult, maintHostOf(h).modal.stage, "esc returns to the result")
			}

			return fx.recoveryPlan(r, true), nil
		}

		h = pinsOpen(t, f, 120, 40)
		h.press("w", "y")
		require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)

		// c, then esc during the recovery read: the result keeps its
		// offer.
		h.press("c")
		require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)
		require.NotNil(t, maintHostOf(h).modal.offer)

		// c, then esc on the recovery preview: back to the result.
		h.press("c")
		require.Equal(t, maintStagePreview, maintHostOf(h).modal.stage)
		require.True(t, maintHasKey(h, "y"))

		h.press("esc")
		require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)
		require.NotNil(t, maintHostOf(h).modal.offer, "esc on the recovery preview keeps the offer")

		// esc on the result closes it and dismisses the offer.
		h.press("esc")
		assert.False(t, maintHostOf(h).open())
		assert.False(t, h.m.st.busy())
		assert.Equal(t, []string{
			"PlanMaintenance:retarget", "CommitMaintenance:retarget", "PlanRecovery:retarget",
			"PlanRecovery:retarget",
		}, f.called(), "no esc commits, and nothing completes the ledger")
	})

	t.Run("extraction planning, review and run", func(t *testing.T) {
		var h *harness

		p := extPreview(extThreeBatches())
		f := extFake(p, nil)

		escInPlan := true
		plan := f.planExtract
		f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
			if escInPlan {
				// esc arrives while the preview plans: the draft goes,
				// and the late plan opens nothing. The harness runs this
				// read before the navigation of the same batch. The user
				// sees Lessons › Run before any key, so the navigation
				// arrives first here too.
				escInPlan = false

				h.send(navigateMsg{dest: destLessons, intent: "run"})
				require.Equal(t, extPlanning, extRecOf(t, h).step)
				h.press("esc")
				require.Nil(t, runViewOf(h).cur)
			}

			return plan(req)
		}

		f.runExtract = func(c context.Context, rp *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
			*distill.ExtractExecution, error,
		) {
			// esc while running, and esc on the stop question, keep the
			// run going.
			rec := extRecOf(t, h)
			require.Equal(t, extRunning, rec.step)

			h.press("esc")
			require.False(t, rec.capturing())

			h.press("x")
			require.True(t, rec.capturing())

			h.press("esc")
			require.False(t, rec.capturing())
			require.False(t, rec.stopping())
			require.NoError(t, c.Err(), "esc never stops the run")

			return extStoreAllRun("internal/index/schema.go")(c, rp, progress)
		}

		h = newHarness(t, f, 120, 40)

		h.send(openExtractMsg{req: p.Request})
		assert.Nil(t, runViewOf(h).cur, "esc while planning leaves no record")

		h.send(openExtractMsg{req: p.Request})
		require.Equal(t, extReview, extRecOf(t, h).step)

		h.press("esc")
		assert.Nil(t, runViewOf(h).cur, "esc drops the review")
		assert.Equal(t, []string{"PlanExtract", "PlanExtract"}, f.called(), "no esc runs the agent")

		h.send(openExtractMsg{req: p.Request})
		h.press("y")

		rec := extRecOf(t, h)
		require.Equal(t, extResult, rec.step)
		assert.NotContains(t, extTallFlat(h), "stopped", "the escaped stop question stopped nothing")
		assert.Nil(t, rec.run.stopped, "the run ended without a stop")
		assert.False(t, rec.run.stopping)
		assert.Equal(t, []string{"PlanExtract", "PlanExtract", "PlanExtract", "RunExtract"}, f.called())
	})

	t.Run("pins inputs and comparison", func(t *testing.T) {
		f := pinsFake()
		h := pinsOpen(t, f, 120, 40)
		p := pinsOf(h)

		h.press("space")
		marked := maps.Clone(p.selected)

		// esc in the region input and in the text search keeps the old
		// filter and the selection.
		h.press("/")
		h.typeText("docs")
		h.press("esc")
		h.press("z")
		h.typeText("handles")
		h.press("esc")
		assert.Empty(t, p.region)
		assert.Empty(t, p.text)
		assert.Equal(t, marked, p.selected)

		// esc closes the comparison.
		pinsCursorTo(h, 3)
		h.press("v")
		require.NotNil(t, p.compare)

		h.press("esc")
		assert.Nil(t, p.compare)
		assert.Equal(t, marked, p.selected)
		assert.Empty(t, f.called(), "no esc plans or writes")
	})
}

func TestConsoleFlowQuitDuringAMaintenanceCommitWaitsForIt(t *testing.T) {
	var h *harness

	fx := maintFixture(t)
	plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

	f := maintFakeOf(fx, plan, func(q *maintain.Plan) (maintain.Result, error) {
		// The user quits while the commit runs. The preview takes every
		// key, so q does nothing; ctrl+c reaches the shell, as the footer
		// says. The commit cannot stop, so the console asks and waits.
		h.press("q")
		require.False(t, h.quit)
		require.False(t, h.m.quitConfirm, "the committing preview takes q")
		assert.Contains(t, footerLine(h.view()), "ctrl+c quit")

		h.press("ctrl+c")
		require.False(t, h.quit)

		frame := lesFlat(h.view())
		assert.Contains(t, frame, "Exit after the running operation?")
		assert.Contains(t, frame, "Retargeting 1 pin is running.")

		h.press("enter")
		require.False(t, h.quit, "the console waits for the commit")
		assert.Equal(t, maintStageCommitting, maintHostOf(h).modal.stage)

		// The commit is registered work, so the exit wait of Run sees it.
		assert.False(t, h.m.st.work.wait(20*time.Millisecond), "the exit waits for the running commit")

		return maintain.Result{Action: q.Action, FileWritten: true, LedgerDone: true, Moved: []int64{4}}, nil
	})

	h = pinsOpen(t, f, 120, 40)
	h.press("w", "y")

	assert.True(t, h.quit, "the console exits after the commit ends")
	assert.True(t, h.m.st.work.wait(time.Second), "no work runs after the exit")
	assert.Equal(t, []string{"PlanMaintenance:retarget", "CommitMaintenance:retarget"}, f.called())
}

func TestConsoleFlowGateOffDuringARecoveryKeepsTheOfferAndNeedsAFreshCheck(t *testing.T) {
	var completed *maintain.Plan

	fx := maintFixture(t)
	plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

	svc := settingsFake(t, "distill:\n  write: true\n")
	svc.ledger = lesLedger()
	svc.pins = fx.inspection()
	svc.planMaintenance = func(maintain.Request) (*maintain.Plan, error) { return plan, nil }
	svc.commitMaintenance = maintPartial(fx, true)

	// The recovery check reads distill.write from the file, as the engine
	// does (D-14).
	svc.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) {
		data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(settings.File)))
		require.NoError(t, err)

		p := fx.recoveryPlan(r, true)
		p.Write = strings.Contains(string(data), "write: true")
		p.Confirmable = p.Write

		return p, nil
	}
	svc.commitRecovery = func(q *maintain.Plan) (maintain.Result, error) {
		completed = q

		return maintain.Result{Action: q.Action, LedgerDone: true, Moved: []int64{4}}, nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "m", "w", "y")
	require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)
	require.NotNil(t, maintHostOf(h).modal.offer, "the partial commit offers a recovery")

	// Another editor turns the gate off; the recovery check shows o.
	settingsWrite(t, svc.root, "distill:\n  write: false\n")
	h.press("c")
	require.Equal(t, maintStagePreview, maintHostOf(h).modal.stage)
	assert.False(t, maintHasKey(h, "y"))
	require.True(t, maintHasKey(h, "o"))

	// o returns the preview to its result and opens Settings.
	h.press("o")
	require.Equal(t, destSettings, h.m.active)
	require.Equal(t, maintStageResult, maintHostOf(h).modal.stage, "the proof stays with the result")

	// Navigation reads nothing (R-13), so Settings still shows the gate
	// of its last read. r reads the file of the other editor.
	h.press("r")
	assert.NotEmpty(t, settingsLine(h.view(), "distill.write", "off", "config.yaml"))

	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	// Back in Pins the result still offers c, and only a fresh check
	// offers y.
	h.press("3")
	require.Equal(t, lesPins, hookLessons(h).sub)
	require.True(t, maintHostOf(h).open())
	require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)
	require.True(t, maintHasKey(h, "c"))

	h.press("y")
	assert.Nil(t, completed, "y on the result completes nothing")

	h.press("c")
	require.True(t, maintHasKey(h, "y"), "the fresh check reads the new gate")

	h.press("y")
	require.NotNil(t, completed)
	assert.True(t, completed.Write)
	assert.Equal(t, []string{
		"PlanMaintenance:retarget", "CommitMaintenance:retarget", "PlanRecovery:retarget", "PlanSettings",
		"SaveSettings", "PlanRecovery:retarget", "CommitRecovery:retarget",
	}, svc.called())
}

func TestConsoleFlowExtractionReviewOfABrokenPinFileRoutesToPins(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := newFake()
	f.ledger = lesLedger()
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		reqs = append(reqs, req)

		return nil, &learning.ExtractConfigError{Path: pinfile.File, Err: errors.New("yaml: line 4: bad indent")}
	}
	f.runExtract = extNoRun(t)

	h := lesOpen(t, f, 120, 40)

	// t on a pending proposal plans; the plan refuses the pin file.
	h.press("t")
	require.Equal(t, []learning.ExtractRequest{{IDs: []int64{1}}}, reqs)

	rec := extRecOf(t, h)
	require.Equal(t, extReview, rec.step)
	assert.False(t, rec.runnable(), "a configuration error offers no y")
	assert.NotContains(t, footerLine(h.view()), "y start")
	assert.Contains(t, lesFlat(h.view()), "yaml: line 4: bad indent")

	h.press("y")
	assert.False(t, h.m.st.busy(), "y starts nothing")

	// o only navigates: Pins shows, and the review stays in Run.
	h.press("o")
	require.Equal(t, destLessons, h.m.active)
	require.Equal(t, lesPins, hookLessons(h).sub)
	assert.False(t, maintHostOf(h).open())

	h.press("u")
	assert.Same(t, rec, extRecOf(t, h), "the review waits in Run")
	assert.Equal(t, []string{"PlanExtract"}, f.called(), "no y and no link runs the agent")
}

func TestConsoleFlowExtractionBlockerRoutesToSettingsAndNeedsAFreshPlan(t *testing.T) {
	var ran []*learning.ExtractPreview

	svc := settingsFake(t, "")
	svc.ledger = lesLedger()
	svc.index = ovReadyIndex(svc.fakeServices.root, svc.dbPath)

	// The plan resolves the agent from config.yaml, as PlanExtract does:
	// the default claude is not on PATH here, and codex is.
	svc.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		p := extPreviewFor(req, svc.ledger.Pending, 1)

		data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(settings.File)))
		if err != nil || !strings.Contains(string(data), "codex") {
			p.Blockers = []string{`agent cli "claude" not found on PATH`}
		}

		return p, nil
	}
	svc.runExtract = func(_ context.Context, p *learning.ExtractPreview, _ func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		ran = append(ran, p)

		return extExec(p, nil), nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "t")

	rec := extRecOf(t, h)
	require.Equal(t, extReview, rec.step)
	assert.False(t, rec.runnable(), "a blocker offers no y")
	assert.Contains(t, footerLine(h.view()), "o open Settings › Inference")

	// o opens Settings › Inference; the user saves codex.
	h.press("o")
	require.Equal(t, destSettings, h.m.active)

	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, "agent:\n  cli: codex\n", settingsBytes(t, svc))

	// The review read the old configuration, so it is out of date: no y
	// until enter plans again.
	h.press("3")
	require.Equal(t, lesRun, hookLessons(h).sub)
	require.Same(t, rec, extRecOf(t, h))
	assert.False(t, rec.runnable(), "the saved settings make the review stale")

	h.press("y")
	assert.Empty(t, ran, "y on a stale review runs nothing")

	h.press("enter")
	require.True(t, rec.runnable(), "the fresh plan reads codex")

	h.press("y")
	require.Len(t, ran, 1)
	assert.Empty(t, ran[0].Blockers, "the run gets the fresh plan")
	assert.Equal(t, []string{"PlanExtract", "PlanSettings", "SaveSettings", "PlanExtract", "RunExtract"},
		svc.called())
}

func TestConsoleFlowPendingMatchRouteThroughProposalsLinksThePin(t *testing.T) {
	const entry = 5 // pin 6, prefer-table-tests, matches pending p3

	before := pinsInspection()
	p3 := before.Pins[entry].Link.Pending[0]
	require.Equal(t, pinfile.LinkPendingMatch, before.Pins[entry].Link.Link)

	// After the record, p3 is applied and links the pin.
	after := pinsInspection()
	applied := p3
	applied.Status = domain.ProposalApplied
	after.Pins[entry].Link = pinfile.EntryLink{Link: pinfile.LinkLinked, Applied: []domain.Proposal{applied}}

	// A real snapshot computes the readings of a linked pin. A changed
	// scope makes Pins offer w (R-5); the trigger reading stays unknown,
	// so the review decides about t.
	after.Pins[entry].Readings.Scope = maintain.ScopeReading{
		Known: true, Advice: distill.ScopeAdvice{Kind: distill.ScopeChanged, Regions: []string{"internal/cli/flags"}},
	}

	f := pinsFake()
	f.ledger.Pending[2] = p3

	// A real inspection links every pending row by its ID. So Proposals
	// knows the pending match, and c records it (user decision of
	// 2026-10-04: c without a known pin opens no preview).
	f.pins.Links.ByProposal[p3.ID] = pinfile.LinkPendingMatch
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return &proposals.Preview{
			Action: a, Write: true, Target: proposals.LessonsFile, Proposals: []domain.Proposal{p3},
			Prints: []proposals.Fingerprint{{ID: p3.ID, Rule: p3.Rule, Note: p3.Note}},
		}, nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		f.pins = after

		return proposals.Result{Action: pv.Action, Moved: []int64{p3.ID}}, nil
	}

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, entry+1)
	require.Equal(t, "prefer-table-tests", cursorPin(t, h).Entry.Rule)
	assert.False(t, maintHasKey(h, "w"), "a pending match is read-only")
	assert.Contains(t, pinsDetail(t, h), "Record p3 as applied first: press p, select it, then press c.")

	// The route of the detail: p, select p3, c, y.
	h.press("p")
	require.Equal(t, lesProposals, hookLessons(h).sub)

	for lesProps(h).cursorID != p3.ID {
		h.press("down")
	}

	h.press("space", "c")
	require.NotNil(t, lesProps(h).dec, "c opens the record preview")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:reconcile", "CommitDecision:reconcile"}, f.called())

	// enter closes the result. Pins reads the new link: the pin is
	// linked, and its keys appear.
	h.press("enter", "m")
	require.Equal(t, lesPins, hookLessons(h).sub)

	pin := cursorPin(t, h)
	assert.Equal(t, "prefer-table-tests", pin.Entry.Rule, "the cursor stays on the pin")
	assert.Equal(t, pinfile.LinkLinked, pin.Link.Link)
	assert.True(t, maintHasKey(h, "w"))
	assert.True(t, maintHasKey(h, "t"))
	assertNoLossNote(t, h)
}

func TestConsoleFlowSettingsSaveMakesAReadyExtractionReviewStale(t *testing.T) {
	var ran []*learning.ExtractPreview

	svc := settingsFake(t, "distill:\n  write: false\n")
	svc.ledger = lesLedger()
	svc.index = ovReadyIndex(svc.fakeServices.root, svc.dbPath)
	svc.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		return extPreviewFor(req, svc.ledger.Pending, 1), nil
	}
	svc.runExtract = func(_ context.Context, p *learning.ExtractPreview, _ func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		ran = append(ran, p)

		return extExec(p, nil), nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "t")

	rec := extRecOf(t, h)
	reviewed := rec.plan.preview
	require.True(t, rec.runnable(), "the review is ready")

	// The review takes no navigation key, so the user can save a setting
	// meanwhile. The run binds config.yaml, so any save makes the review
	// out of date (D-20).
	h.press("5")
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	h.press("3")
	require.Same(t, rec, extRecOf(t, h))
	assert.False(t, rec.runnable(), "a save makes the ready review stale")
	assert.NotContains(t, footerLine(h.view()), "y start")

	h.press("y")
	assert.Empty(t, ran, "y on a stale review runs nothing")

	h.press("enter")
	require.True(t, rec.runnable())
	require.NotSame(t, reviewed, rec.plan.preview, "enter plans a fresh preview")

	h.press("y")
	require.Len(t, ran, 1)
	assert.Same(t, rec.plan.preview, ran[0], "the run gets the fresh preview")
	assert.Equal(t, []string{"PlanExtract", "PlanSettings", "SaveSettings", "PlanExtract", "RunExtract"},
		svc.called())
}
