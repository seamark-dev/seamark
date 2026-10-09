package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The tests in this file cover the start, the progress and the stop of
// a run. They also cover every outcome kind, "prepare remaining work",
// the settings invalidation, and the proposals of one run.

// distRunCall records the arguments of one RunDistill call.
type distRunCall struct {
	ctx context.Context
	p   *learning.DistillPreview
}

// distScripted returns a run that records its calls and returns the
// execution that x builds from the preview.
func distScripted(calls *[]distRunCall, x func(p *learning.DistillPreview) *distill.Execution, err error) func(
	context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
	return func(ctx context.Context, p *learning.DistillPreview, _ func(distill.Event)) (*distill.Execution, error) {
		*calls = append(*calls, distRunCall{ctx: ctx, p: p})

		return x(p), err
	}
}

// distAllDone completes every group with two proposals.
func distAllDone(p *learning.DistillPreview) *distill.Execution {
	return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
		for i := range gs {
			distDone(&gs[i], distProposals("rule", int64(21+2*i), int64(22+2*i))...)
		}

		x.Removed = len(x.CleanupIDs)
	})
}

// distTallFlat renders the record in one tall compact box and flattens
// it, so prose assertions do not depend on wrapping or on a second pane.
func distTallFlat(h *harness) string {
	w, hh := h.m.st.width, h.m.st.height

	h.resize(80, 100)
	flat := lesBoxFlat(h.view())
	h.resize(w, hh)

	return flat
}

func TestDistillStartsExactlyTheReviewedPreviewOnce(t *testing.T) {
	var calls []distRunCall

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, distAllDone, nil)

	h := distOpen(t, f, 120, 40)
	h.press("enter")

	rec := distRecOf(t, h)
	reviewed := rec.prev.preview

	h.press("y", "y", "enter", "y")

	require.Len(t, calls, 1, "one y starts one run; the result never repeats it")
	assert.Same(t, reviewed, calls[0].p, "the run gets exactly the reviewed preview")
	require.NotNil(t, calls[0].ctx)
	assert.Equal(t, runDistill, h.m.st.lastRunKind)
	assert.False(t, h.m.st.busy(), "every terminal path frees the slot")
	assert.Nil(t, h.m.st.run)
	assert.Equal(t, stepResult, rec.step)
	assert.Error(t, calls[0].ctx.Err(), "the worker releases its context when it ends")
}

func TestDistillRunContextDerivesFromTheSession(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := distFake(distFullPreview(), func(ctx context.Context, p *learning.DistillPreview, _ func(distill.Event)) (*distill.Execution, error) {
		require.NoError(t, ctx.Err(), "the context is live while the run works")

		// An exit cancels the session root; the run context ends with it.
		cancel()
		require.Error(t, ctx.Err())

		return distAllDone(p), nil
	})

	h := distOpen(t, f, 120, 40)
	h.m.st.ctx = root

	h.press("enter", "y")
	assert.Equal(t, 1, f.callCount("RunDistill"))
}

func TestDistillRefusesToStartWhileBusy(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	h.press("enter")

	require.True(t, h.m.st.begin("Building the index"))
	h.press("y")

	assert.Zero(t, f.callCount("RunDistill"))
	assert.Equal(t, stepPreview, distRecOf(t, h).step, "the preview stays ready")
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
}

func TestDistillProgressShowsTypedEvents(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distMidRun(t, h)

	frame := h.view()
	assert.Contains(t, frame, "Distilling · 3 of 6 done · everywhere", "the notice counts over the frozen plan")
	assert.Contains(t, frame, "Sending group 4 of 6 · internal/api")
	assert.Contains(t, frame, "3 of 6 done")
	assert.Contains(t, frame, "2 proposals · p21 p22")
	assert.Contains(t, frame, "duplicates only")
	assert.Contains(t, frame, "1 proposal · p23")
	assert.Contains(t, frame, "sending")
	assert.Contains(t, frame, "waiting")
	assert.Contains(t, frame, "2 removed: p4, p7")
	assert.Contains(t, frame, "no change since the preview")
	assert.Contains(t, frame, "(estimated)")
	assert.Contains(t, footerLine(frame), "x stop")
	assert.Equal(t, 3, rec.run.cursor, "the cursor follows the group that runs")

	// The detail survives navigation.
	h.press("1")
	assert.Contains(t, h.view(), "Lessons › Run", "the notice names the run home elsewhere")

	h.press("3", "u")
	assert.Contains(t, h.view(), "Sending group 4 of 6")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "sending", "size %v keeps the running group visible", size)
	}

	// The activity holds typed facts only, and it stays bounded.
	assert.LessOrEqual(t, len(rec.run.activity), agentActivityKeep)
	assert.Equal(t, []string{"PlanDistill"}, f.called(), "the held run never reached the service")
}

func TestDistillStopAsksFirstAndStopsOnce(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distMidRun(t, h)

	h.press("x")
	require.True(t, rec.capturing())

	modal, ok := rec.modal(120, 39)
	require.True(t, ok)
	assert.Contains(t, modal, "Stop the distillation?")
	assert.Contains(t, lesBoxFlat(modal), "cancels the agent call of group 4")
	assert.Contains(t, lesBoxFlat(modal), "A provider can still bill the cancelled call.")
	assert.Contains(t, footerLine(h.view()), "y stop the run   esc keep running")

	h.press("esc")
	assert.False(t, rec.run.stopping, "esc keeps the run going")
	assert.False(t, h.m.st.run.stopping)

	h.press("x", "y")
	assert.True(t, rec.run.stopping)
	assert.True(t, h.m.st.run.stopping, "the shell cancels the context of the work")

	frame := h.view()
	assert.Contains(t, frame, "Stopping")
	assert.Contains(t, frame, "waiting for the agent call of group 4")
	assert.Contains(t, frame, "Stopping after group 4 of 6")
	assert.NotContains(t, footerLine(frame), "x stop", "x is off while stopping")

	h.press("x")
	assert.False(t, rec.capturing(), "no second stop question")

	// The work ends: the record shows the result it reached.
	x := distPartialExec(rec.prev.preview)
	h.send(distDoneMsg{rec: rec, exec: x, at: time.Now()})

	assert.Equal(t, stepResult, rec.step)
	assert.False(t, h.m.st.busy())

	flat := distTallFlat(h)
	assert.Contains(t, flat, "Distillation stopped")
	assert.Contains(t, flat, "You stopped the run.")
	assert.Contains(t, flat, "1 group was cancelled before its result was saved, so it stays eligible.")
	assert.Contains(t, flat, "not attempted")
	assert.NotContains(t, flat, "✓ completed", "a stopped run is never a success")
}

func TestDistillStopCancelsTheRunContext(t *testing.T) {
	var h *harness

	f := distFake(distFullPreview(), func(ctx context.Context, p *learning.DistillPreview, _ func(distill.Event)) (*distill.Execution, error) {
		require.NoError(t, ctx.Err())

		// The user stops while the call runs. The cancel happens in
		// Update, on this goroutine, so the context ends at once.
		h.press("x", "y")
		require.Error(t, ctx.Err(), "y cancels the run context")

		return distPartialExec(p), nil
	})

	h = distOpen(t, f, 120, 40)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	assert.Equal(t, stepResult, rec.step)
	assert.Equal(t, 1, f.callCount("RunDistill"))
	assert.Contains(t, distTallFlat(h), "Distillation stopped")
}

func TestDistillLateCompletionAfterAStopIsCompleted(t *testing.T) {
	var h *harness

	f := distFake(distFullPreview(), func(_ context.Context, p *learning.DistillPreview, _ func(distill.Event)) (*distill.Execution, error) {
		h.press("x", "y")

		// Every reply arrived before the stop took effect.
		return distAllDone(p), nil
	})

	h = distOpen(t, f, 120, 40)
	h.press("enter", "y")

	flat := distTallFlat(h)
	assert.Contains(t, flat, "Distillation completed")
	assert.Contains(t, flat, "The stop came after the last reply, so every group finished.")
	assert.NotContains(t, flat, "cancelled")
}

func TestDistillEveryOutcomeKindRenders(t *testing.T) {
	storeErr := errors.New("database is locked")

	cases := []struct {
		name  string
		x     func(p *learning.DistillPreview) *distill.Execution
		err   error
		word  string
		wants []string
	}{
		{"completed", distAllDone, nil, "✓ completed", []string{"Distillation completed", "12 new proposals"}},
		{"zero proposals", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
				for i := range gs {
					distDone(&gs[i])
				}
			})
		}, nil, "✓ completed", []string{"the agent proposed nothing new", "0 proposals"}},
		{"duplicates only", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
				for i := range gs {
					distDone(&gs[i])
					gs[i].Duplicates = []distill.Duplicate{{Rule: "dup", Of: "known"}}
				}
			})
		}, nil, "✓ completed", []string{"duplicates only", "6 groups returned only known patterns"}},
		{"partial", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
				distDone(&gs[0], distProposals("r", 21)...)
				distFail(&gs[1], distill.FailAgent, errors.New("exit status 1: rate limited"))
				distFail(&gs[2], distill.FailTimeout, context.DeadlineExceeded)
				distFail(&gs[3], distill.FailInvalidReply, errors.New("parse reply: not JSON"))
				distDone(&gs[4])
				distDone(&gs[5])
			})
		}, nil, "! partial", []string{"Distillation finished with failed groups", "failed: agent error",
			"failed: timeout", "failed: invalid reply", "3 groups failed"}},
		{"all failed", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
				for i := range gs {
					distFail(&gs[i], distill.FailAgent, errors.New("not logged in"))
				}
			})
		}, nil, "! failed", []string{"Distillation failed", "Every group failed, so nothing was saved."}},
		{"store error", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				distDone(&gs[0], distProposals("r", 21)...)
				distFail(&gs[1], distill.FailStore, storeErr)
				x.Stop, x.StopErr = distill.StopStoreError, storeErr
			})
		}, storeErr, "! failed", []string{"Distillation stopped on a store error", "database is locked",
			"failed: store error", "not attempted", "store error"}},
		{"conflict", func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				distDone(&gs[0], distProposals("r", 21)...)
				gs[1].Status, gs[1].Err = distill.GroupSkipped, errors.New("group sig-1 was distilled by another writer")
				x.Stop = distill.StopConflict
				x.StopErr = errors.Join(distill.ErrStale, errors.New(".seamark/config.yaml changed since the preview"))
			})
		}, nil, "! stopped", []string{"Distillation stopped: an input changed", "config.yaml changed since the preview",
			"skipped: recorded elsewhere", "an input changed"}},
		{"cancelled", distPartialExec, nil, "! stopped", []string{"Distillation stopped", "stopped by you", "cancelled"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []distRunCall

			f := distFake(distFullPreview(), nil)
			f.runDistill = distScripted(&calls, c.x, c.err)

			h := distOpen(t, f, 120, 40)
			h.press("enter", "y")

			rec := distRecOf(t, h)
			require.Equal(t, stepResult, rec.step)
			assert.Contains(t, h.view(), c.word)

			flat := distTallFlat(h)
			for _, want := range c.wants {
				assert.Contains(t, flat, want)
			}

			assert.Contains(t, flat, "Nothing is retried automatically.")

			if c.word != "✓ completed" {
				assert.NotContains(t, h.view(), "✓ completed", "a run with an open group never reads as a success")
			}

			for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
				h.resize(size[0], size[1])
				assertFrame(t, h.view(), size[0], size[1])
			}

			// The Groups pane of the compact layout shows the detail.
			h.press("tab")
			assertFrame(t, h.view(), 60, 20)
			assert.Len(t, calls, 1)
		})
	}
}

func TestDistillRefusedStartReturnsToTheReview(t *testing.T) {
	var calls []distRunCall

	stale := errors.Join(distill.ErrStale, errors.New(".seamark/lessons.yaml changed since the preview"))

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, func(*learning.DistillPreview) *distill.Execution { return nil }, stale)

	h := distOpen(t, f, 80, 60)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	require.Equal(t, stepPreview, rec.step, "a refused start has no result")
	assert.False(t, h.m.st.busy())
	assert.Contains(t, lesBoxFlat(h.view()), "The plan is out of date")
	assert.Contains(t, lesBoxFlat(h.view()), "lessons.yaml changed since the preview")
	assert.NotContains(t, footerLine(h.view()), "y start run")

	h.press("y")
	assert.Len(t, calls, 1, "y never repeats a refused start")

	h.press("enter")
	assert.Equal(t, 2, f.callCount("PlanDistill"), "only a new plan helps")
	assert.Nil(t, rec.prev.stale)
}

func TestDistillPrepareRemainingWorkPlansAgain(t *testing.T) {
	var calls []distRunCall

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, distPartialExec, nil)
	reqs := distRequests(f)

	h := distOpen(t, f, 120, 40)
	h.press("down", "down", "down", "space", "enter", "y")

	done := distRecOf(t, h)
	require.Equal(t, stepResult, done.step)

	h.press("n")

	next := distRecOf(t, h)
	require.NotSame(t, done, next, "a new record plans the remaining work")
	assert.Equal(t, stepPreview, next.step, "the new record plans at once, read-only")
	require.Len(t, *reqs, 2)
	assert.Equal(t, (*reqs)[0], (*reqs)[1], "the same inputs: the explicit all choice stays explicit")
	assert.True(t, (*reqs)[1].AllGroups)
	assert.Len(t, calls, 1, "planning again never starts a run")

	ids, _, at := runViewOf(h).generated()
	assert.Equal(t, done.newIDs(), ids, "the finished run stays in the session list")
	assert.False(t, at.IsZero())

	// Closing the new draft shows the finished run again.
	h.press("esc")
	assert.Same(t, done, runViewOf(h).cur)
}

func TestDistillSettingsSaveDropsTheReadyPlan(t *testing.T) {
	for _, g := range []settings.Group{settings.GroupInference, settings.GroupPinWrite, settings.GroupIndexing} {
		t.Run(g.String(), func(t *testing.T) {
			f := distFake(distFullPreview(), distNoRun(t))
			h := distOpen(t, f, 120, 40)
			h.press("enter")

			rec := distRecOf(t, h)
			require.True(t, rec.runnable())

			h.send(settingsSavedMsg{group: g, at: time.Now()})

			assert.Equal(t, stepScope, rec.step, "the reviewed plan binds the old settings")
			assert.Contains(t, rec.form.note, "Settings changed ("+g.String()+")")

			h.press("y")
			assert.Zero(t, f.callCount("RunDistill"), "a dropped plan never starts")
		})
	}

	// A save also drops a plan whose read still runs.
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	require.NotNil(t, rec.startPlan())
	seq := rec.prev.seq.last
	h.send(settingsSavedMsg{group: settings.GroupInference})
	h.send(distPlannedMsg{rec: rec, seq: seq, preview: distFullPreview()})
	assert.Equal(t, stepScope, rec.step, "a late plan result is ignored")

	// A running record keeps going: the engine checks the file itself.
	h2 := distOpen(t, distFake(distFullPreview(), distNoRun(t)), 120, 40)
	running := distStartHeld(t, h2)
	h2.send(settingsSavedMsg{group: settings.GroupInference})
	assert.Equal(t, stepRunning, running.step)
}

func TestProposalsApplyPreviewClosesOnAPinWriteSave(t *testing.T) {
	f := pendingLedgerFake()
	h := lesOpen(t, f, 120, 40)
	props := lesProps(h)

	h.press("a")
	require.NotNil(t, props.dec)

	h.send(settingsSavedMsg{group: settings.GroupEvidence})
	assert.NotNil(t, props.dec, "an unrelated save keeps the preview")

	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.Nil(t, props.dec, "the preview named the old write gate")
	assert.Contains(t, h.view(), "Pin writing changed in Settings")

	h.press("y")
	assert.Zero(t, f.callCount("CommitDecision:apply"))

	// A dismiss preview does not depend on the write gate.
	h.press("d")
	require.NotNil(t, props.dec)
	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.NotNil(t, props.dec)
}

func TestDistillReviewGeneratedProposalsFiltersByTheRunIDs(t *testing.T) {
	var calls []distRunCall

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, func(p *learning.DistillPreview) *distill.Execution {
		return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
			distDone(&gs[0], domain.Proposal{ID: 2, Rule: "close-db-handles"}, domain.Proposal{ID: 3, Rule: "x"})
		})
	}, nil)

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y")
	require.Contains(t, footerLine(h.view()), "enter review proposals")

	h.press("enter")

	les := h.m.screens[destLessons].(*lessons)
	props := les.props
	require.Equal(t, lesProposals, les.sub)
	require.NotNil(t, props.run)

	frame := h.view()
	assert.Contains(t, frame, "run "+distRecOfPast(t, h).run.ended.Format("15:04")+" · 2 proposals", "the chip is visible")
	assert.Contains(t, frame, "esc all proposals")
	assert.Equal(t, []int64{2, 3}, lesRowIDs(props), "the list shows exactly the returned IDs")

	h.press("esc")
	assert.Nil(t, props.run, "esc clears the run filter")
	assert.Equal(t, []int64{1, 2, 3}, lesRowIDs(props))
	assert.NotContains(t, h.view(), "2 proposals ·")

	// f and / clear it too.
	h.send(navigateMsg{dest: destLessons, intent: "proposals:run"})
	require.NotNil(t, props.run)
	h.press("f")
	assert.Nil(t, props.run)

	h.send(navigateMsg{dest: destLessons, intent: "proposals:run"})
	require.NotNil(t, props.run)
	h.press("/")
	assert.Nil(t, props.run)
	h.press("esc")

	// The Overview link means every pending proposal.
	h.send(navigateMsg{dest: destLessons, intent: "proposals:run"})
	h.send(navigateMsg{dest: destLessons, intent: "proposals"})
	assert.Nil(t, props.run)
}

// distRecOfPast returns the newest finished distillation of the session.
func distRecOfPast(t *testing.T, h *harness) *distRecord {
	t.Helper()

	for _, rec := range runViewOf(h).records() {
		if d, ok := rec.(*distRecord); ok && d.step == stepResult {
			return d
		}
	}

	t.Fatal("no finished distillation")

	return nil
}

// lesRowIDs returns the IDs of the visible proposal rows.
func lesRowIDs(p *propView) []int64 {
	var ids []int64

	for _, pr := range p.rows() {
		ids = append(ids, pr.ID)
	}

	return ids
}

func TestProposalsRunFilterWaitsForANewerLedger(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	props := lesProps(h)
	end := h.m.st.ledger.at.Add(time.Minute)

	// The run saved p9, which the older snapshot cannot hold yet.
	assert.Nil(t, props.showRun([]int64{9}, "run 14:05", end))

	frame := h.view()
	assert.Contains(t, frame, "run 14:05 · 1 proposal")
	assert.Contains(t, frame, "Reading the proposals of run 14:05")
	assert.NotContains(t, frame, "No pending proposals from", "an older ledger never calls an ID missing")

	// A newer ledger holds p9 as pending: the row shows.
	f.ledger.Pending = append(f.ledger.Pending, domain.Proposal{ID: 9, Rule: "new-rule", Status: domain.ProposalProposed})
	h.send(loadedMsg{kind: secLedger, gen: h.m.st.gens[secLedger].latest(), value: f.ledger, at: end.Add(time.Second)})
	assert.Equal(t, []int64{9}, lesRowIDs(props))

	// A newer ledger without it counts it as missing.
	f.ledger.Pending = f.ledger.Pending[:len(f.ledger.Pending)-1]
	h.send(loadedMsg{kind: secLedger, gen: h.m.st.gens[secLedger].latest(), value: f.ledger, at: end.Add(2 * time.Second)})

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "No pending proposals from run 14:05")
	assert.Contains(t, flat, "1 no longer in the ledger")
	assert.Contains(t, flat, "Press esc to show every pending proposal.")

	// A prune keeps the row as superseded history, so it is not missing.
	f.ledger.Superseded = append(f.ledger.Superseded, domain.Proposal{ID: 9, Rule: "new-rule", Status: domain.ProposalSuperseded})
	h.send(loadedMsg{kind: secLedger, gen: h.m.st.gens[secLedger].latest(), value: f.ledger, at: end.Add(3 * time.Second)})

	flat = lesBoxFlat(h.view())
	assert.Contains(t, flat, "0 applied, 0 dismissed, 1 superseded.")
	assert.NotContains(t, flat, "no longer in the ledger")
}

func TestProposalsRunFilterReconcilesTheSelection(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	props := lesProps(h)

	props.showRun([]int64{1, 2}, "run 14:05", time.Time{})
	h.press("space")
	require.Equal(t, map[int64]bool{1: true}, props.selected)

	// The filter narrows to p2 only; the selected p1 leaves it.
	props.run.ids = map[int64]bool{2: true}
	h.send(loadedMsg{kind: secLedger, gen: h.m.st.gens[secLedger].latest(), value: f.ledger, at: time.Now()})

	assert.Empty(t, props.selected, "a hidden row never stays an action target")
	assert.Contains(t, h.view(), "p1 left the selection: outside the run filter")
}

func TestDistillHostileTextStaysInert(t *testing.T) {
	evil := "\x1b[31mred\x1b[0m\x1b]0;title\x07"
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

	p := distFullPreview()
	p.Region = "internal/" + evil
	p.Invocation.Argv = []string{"claude", "--token=" + token, evil}
	p.Invocation.Name = "claude" + evil
	p.Plan.Preflight.Groups[0].Region = evil
	p.Plan.Preflight.Groups[0].Evidence[0].Path = "a/" + evil + ".go"
	p.Plan.Preflight.Groups[0].Evidence[0].Source = "review" + evil
	p.Plan.Cleanup[0].Rule = "rule" + evil
	p.Blockers = nil

	var calls []distRunCall

	f := distFake(p, nil)
	f.runDistill = distScripted(&calls, func(p *learning.DistillPreview) *distill.Execution {
		return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
			distDone(&gs[0], domain.Proposal{ID: 21, Rule: "rule" + evil})
			distFail(&gs[1], distill.FailInvalidReply, errors.New("reply "+evil+" token="+token))
			gs[1].Duplicates = []distill.Duplicate{{Rule: evil, Of: evil}}
		})
	}, nil)

	h := distOpen(t, f, 120, 40)

	check := func(stage string) {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			// The test profile renders no style, so any escape byte
			// would come from the data.
			raw := h.m.View()
			assert.NotContains(t, raw, "\x1b", "%s %v: no escape sequence reaches the terminal", stage, size)
			assert.NotContains(t, raw, token, "%s %v: the token is redacted", stage, size)
		}

		h.resize(120, 40)
	}

	h.press("enter")
	check("review")

	h.press("c")
	check("review")

	h.press("y")
	check("result")

	h.press("down", "tab")
	check("result detail")
}

func TestDistillFramesAreASCIIOnly(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))

	h := &harness{t: t, m: newModel(f, newTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(120, 40))
	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	ascii := func(stage string) {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			for _, r := range h.view() {
				require.Less(t, r, rune(128), "%s %v holds %q", stage, size, r)
			}
		}

		h.resize(120, 40)
	}

	ascii("scope")

	h.press("enter")
	ascii("review")

	rec := distRecOf(t, h)
	require.NotNil(t, rec.start())
	distSend(h, rec, distStarted(rec, 0))
	ascii("running")

	h.press("x")
	ascii("stop question")

	h.press("y")
	ascii("stopping")

	h.send(distDoneMsg{rec: rec, exec: distPartialExec(rec.prev.preview), at: time.Now()})
	ascii("result")
	assert.True(t, strings.Contains(h.view(), "(estimated)"), "an approximate number carries its word in ASCII")
}

func TestDistillSessionRecordsKeepTheResult(t *testing.T) {
	var calls []distRunCall

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, distAllDone, nil)

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	ids, label, at := rec.generated()
	assert.Equal(t, []int64{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}, ids)
	assert.Equal(t, "run "+at.Format("15:04"), label)
	assert.False(t, rec.draft())
	assert.False(t, rec.active())

	// esc leaves the result in place and opens Evidence.
	h.press("esc")
	assert.Equal(t, lesEvidence, h.m.screens[destLessons].(*lessons).sub)
	assert.Same(t, rec, runViewOf(h).cur)

	// Another "distill" intent starts a new draft; the result stays listed.
	h.send(navigateMsg{dest: destLessons, intent: "distill"})
	assert.NotSame(t, rec, runViewOf(h).cur)
	assert.Contains(t, runViewOf(h).past, runRecord(rec))
}

func TestDistillShowsAStopFromTheQuitQuestion(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distMidRun(t, h)

	h.press("q")
	require.Contains(t, h.view(), "Stop and exit?")

	h.press("enter")
	assert.True(t, h.m.st.run.stopping)
	assert.True(t, rec.stopping(), "the record shows the stop of the shell")
	assert.Contains(t, h.view(), "Stopping after group 4 of 6")

	// The end of the work reports the observed outcome and quits.
	h.send(distDoneMsg{rec: rec, exec: distPartialExec(rec.prev.preview), at: time.Now()})
	assert.Equal(t, stepResult, rec.step)
	assert.True(t, h.quit, "the console exits after the stopped work ends")
}

// A real-binary trial of the trigger extraction found that the summary
// said "no agent traffic yet" while the prompt was with the agent. The
// distillation record counts the prompt that is with the agent too.

func TestDistillUsageCountsThePromptThatIsWithTheAgent(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distStartHeld(t, h)

	assert.Equal(t, "no agent traffic yet", rec.usageText())

	distSend(h, rec, distStarted(rec, 0))

	first := distTokens(rec.prev.preview.Plan.Preflight.Groups[0].PromptChars)
	assert.Equal(t, first+" tokens sent to group 1 (estimated); reply pending", rec.usageText())
}
