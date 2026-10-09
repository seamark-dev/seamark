package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The tests in this file cover the trigger extraction record. They test
// the review and its blocked states, and the start, the progress and the
// stop of a run. They test every outcome kind of the result, its links,
// "prepare remaining", the session history, and safe rendering.

// extTallFlat renders the record in one tall compact box and flattens
// it, so prose assertions do not depend on wrapping or on a second pane.
func extTallFlat(h *harness) string {
	w, hh := h.m.st.width, h.m.st.height

	h.resize(80, 120)
	flat := lesBoxFlat(h.view())
	h.resize(w, hh)

	return flat
}

// extNoRun fails the test when a run starts.
func extNoRun(t *testing.T) func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
	*distill.ExtractExecution, error,
) {
	return func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (*distill.ExtractExecution, error) {
		t.Fatal("the run must not start")

		return nil, nil
	}
}

// extReturns returns a run that answers with the execution that x builds
// from the preview, and records the preview and the context it got.
func extReturns(seen *[]*learning.ExtractPreview, ctxs *[]context.Context,
	x func(p *learning.ExtractPreview) *distill.ExtractExecution,
) func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (*distill.ExtractExecution, error) {
	return func(ctx context.Context, p *learning.ExtractPreview, _ func(distill.ExtractEvent)) (*distill.ExtractExecution, error) {
		*seen = append(*seen, p)
		*ctxs = append(*ctxs, ctx)

		return x(p), nil
	}
}

func TestExtractReviewDisclosesThePreview(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, extNoRun(t))

	h := extOpen(t, f, p, 120, 40)
	rec := extRecOf(t, h)

	require.Equal(t, stepPreview, rec.step)
	assert.Equal(t, []string{"PlanExtract"}, f.called(), "the review plans once and runs nothing")
	assert.True(t, rec.draft(), "a review did no work")
	assert.False(t, rec.active())

	frame := h.view()
	for _, want := range []string{
		"Trigger extraction", "selected 9", "agent claude", "planned 10:04:31", "Review",
		"5 eligible of 9", "1 batch: 5 proposals", "tokens (estimated)", "claude -p · preset claude", "/work/repo",
		"p23", "p22", "p21", "p6", "p4", "older negative", "never asked",
		"NOT ASKED", "dismissed", "already answered with trigger paths", "pin not present",
		"pin 7 (lessons.yaml:31): no linked proposal",
		"Sends 5 proposals in 1 batch · 4 ineligible", "Nothing is sent until you press y.",
	} {
		assert.Contains(t, frame, want)
	}

	assert.Contains(t, footerLine(frame), "y start run")

	flat := extTallFlat(h)
	// The prompt builder sends the ID, the rule, the note and the evidence
	// paths; the finding metadata has no line number (D-18).
	assert.Contains(t, flat, "Sent per proposal: its ID, rule, recorded note and cited evidence paths; no finding "+
		"text.")
	assert.NotContains(t, flat, "lines;", "the prompt sends no line number")
	assert.NotContains(t, flat, "excerpt", "the console never claims an excerpt (D-18)")

	// The region words follow distill.RecomputeRegions: a confirmed
	// trigger can move the regions, and an unconfirmed answer falls back
	// to the coverage, as a negative answer does.
	assert.Contains(t, flat, "a confirmed trigger replaces the regions with its own region, which can be outside "+
		"the current regions. A negative or unconfirmed answer sets the evidence coverage.")
	assert.NotContains(t, flat, "narrow the regions")
	assert.Contains(t, flat, "A negative or unconfirmed answer sets: p22 internal/apix › internal/apix, internal/db")
	assert.Contains(t, flat, "Installed pin scopes never change here; a retarget is a separate preview")
	assert.Contains(t, flat, "p4: the live note differs from the recorded note. The question uses the recorded note.")
	assert.Contains(t, flat, "change an applied pin's scope or lessons.yaml")
	assert.Equal(t, []string{"PlanExtract"}, f.called(), "rendering reads nothing")
}

func TestExtractReviewShowsBothNotesOfALiveEdit(t *testing.T) {
	p := extPreview(extMixed())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

	h.press("down", "down", "down", "down")
	require.Equal(t, 4, extRecOf(t, h).prev.cursor)

	// The compact pane shows the detail of the cursor item in one box.
	h.resize(80, 120)
	h.press("tab")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "RECORDED NOTE sent")
	assert.Contains(t, flat, "Wrap SQL errors with the query name.")
	assert.Contains(t, flat, "LIVE NOTE lessons.yaml, not sent")
	assert.Contains(t, flat, "Wrap SQL errors with the query name and the table.")
	assert.Contains(t, flat, "The question uses the recorded note.")
	assert.Contains(t, flat, "trigger paths are stored as advice; the pin scope stays internal/store")
	assert.Contains(t, flat, "Cited internal/store/store.go")
}

func TestExtractReviewKeepsItsScopeAtTheMinimumSize(t *testing.T) {
	p := extPreview(extMixed())
	h := extOpen(t, extFake(p, extNoRun(t)), p, minWidth, minHeight)

	check := func(stage string) {
		frame := h.view()
		assertFrame(t, frame, minWidth, minHeight)
		assert.Contains(t, frame, "Sends 5 proposals in 1 batch · 4 ineligible", stage)
		assert.Contains(t, frame, "Nothing is sent until you press y.", stage)
		assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(frame)), "y start run"), stage)
	}

	check("top")

	h.press("pgdown", "pgdown", "pgdown")
	check("scrolled")
}

func TestExtractReviewOffersNoStartWithoutAnEligibleProposal(t *testing.T) {
	cases := []struct {
		name string
		opts func() extPreviewOpts
		want []string
		link string
	}{
		{
			name: "nothing eligible",
			opts: func() extPreviewOpts {
				o := extMixed()
				o.none = true

				return o
			},
			want: []string{"Nothing to extract", "Sends 0 proposals · 4 ineligible", "Nothing was sent to the agent."},
		},
		{
			name: "no agent",
			opts: func() extPreviewOpts {
				o := extMixed()
				o.blockers = []string{`agent cli "claude" not found on PATH`}

				return o
			},
			want: []string{`agent cli "claude" not found on PATH`, "Nothing was sent. Press enter to plan again."},
			link: "group:inference",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := extPreview(c.opts())
			f := extFake(p, extNoRun(t))
			h := extOpen(t, f, p, 120, 40)

			frame := h.view()
			for _, w := range c.want {
				assert.Contains(t, frame, w)
			}

			assert.NotContains(t, footerLine(frame), "y start", "no y without a confirmable plan")
			assert.False(t, extRecOf(t, h).runnable())

			h.press("y")
			assert.Zero(t, f.callCount("RunExtract"), "y starts nothing")
			assert.False(t, h.m.st.busy())

			if c.link != "" {
				assert.Contains(t, footerLine(frame), "o open Settings › Inference")

				h.press("o")
				assert.Equal(t, destSettings, h.m.active, "o only navigates")
			}

			assert.Zero(t, f.callCount("RunExtract"))
		})
	}
}

func TestExtractPlanErrorsNameTheirRoute(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		title string
		dest  destination
		sub   lesSub
		// none marks a cause without a place to fix it.
		none bool
	}{
		{
			name: "config.yaml", title: "The configuration blocks the plan", dest: destSettings,
			err: &learning.ExtractConfigError{Path: settingsFile, Err: errors.New("agent: unknown preset")},
		},
		{
			name: "lessons.yaml", title: "The configuration blocks the plan", dest: destLessons, sub: lesPins,
			err: &learning.ExtractConfigError{Path: pinfile.File, Err: errors.New("yaml: line 4: bad indent")},
		},
		{
			name: "not ready", title: "The index is not ready", dest: destOverview,
			err: fmt.Errorf("%w: index database is absent: build the index first", learning.ErrExtractNotReady),
		},
		{
			name: "stale", title: "The workspace changed while planning", none: true,
			err: fmt.Errorf("%w: the ledger changed while planning (p4); prepare again", distill.ErrExtractStale),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := extFake(extPreview(extMixed()), extNoRun(t))
			f.planExtract = func(learning.ExtractRequest) (*learning.ExtractPreview, error) { return nil, c.err }

			h := extOpen(t, f, extPreview(extMixed()), 120, 40)
			frame := lesBoxFlat(h.view())

			assert.Contains(t, frame, c.title)
			assert.Contains(t, frame, "Nothing was sent or written.")
			assert.NotContains(t, frame, distill.ErrExtractStale.Error()+":", "the stale prefix is cut")
			assert.NotContains(t, footerLine(h.view()), "y start")

			h.press("y")
			assert.Zero(t, f.callCount("RunExtract"))

			h.press("enter")
			assert.Equal(t, 2, f.callCount("PlanExtract"), "enter plans again")

			h.press("o")

			if c.none {
				assert.Equal(t, destLessons, h.m.active, "a stale plan has no route")
				assert.Equal(t, lesRun, hookLessons(h).sub)

				return
			}

			assert.Equal(t, c.dest, h.m.active)

			if c.dest == destLessons {
				assert.Equal(t, c.sub, hookLessons(h).sub)
			}
		})
	}
}

func TestExtractStartsExactlyTheReviewedPreviewOnce(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extConflictExec)

	h := extOpen(t, f, p, 120, 40)
	rec := extRecOf(t, h)
	reviewed := rec.prev.preview

	h.press("y", "y", "enter", "y")

	require.Len(t, seen, 1, "one y starts one run; the result never repeats it")
	assert.Same(t, reviewed, seen[0], "the run gets exactly the reviewed preview")
	assert.Equal(t, runExtract, h.m.st.lastRunKind)
	assert.False(t, h.m.st.busy(), "every terminal path frees the slot")
	assert.Nil(t, h.m.st.run)
	assert.Equal(t, stepResult, rec.step)
	assert.False(t, rec.draft(), "a finished record stays in the session list")
	assert.Error(t, ctxs[0].Err(), "the worker releases its context when it ends")
	assert.Equal(t, 1, f.callCount("PlanExtract"), "the start plans nothing again")
}

func TestExtractRefusesToStartWhileBusy(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)

	require.True(t, h.m.st.begin("Building the index"))
	h.press("y")

	assert.Zero(t, f.callCount("RunExtract"))
	assert.Equal(t, stepPreview, extRecOf(t, h).step, "the preview stays ready")
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
}

// TestExtractStopsAThreeBatchRunInTheSynchronousHarness proves the queue
// size. The harness runs the worker to its end before it reads an event,
// so the queue must hold every event and the end message. The user stops
// the run during the call of batch 2 of 3.
func TestExtractStopsAThreeBatchRunInTheSynchronousHarness(t *testing.T) {
	var (
		h    *harness
		seen context.Context
	)

	p := extPreview(extThreeBatches())
	require.Len(t, p.Plan.Batches, 3)

	f := extFake(p, func(ctx context.Context, rp *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		seen = ctx
		x := extStoppedExec(rp)
		n := len(rp.Plan.Batches)

		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 0, Total: n})

		b0 := x.Batches[0]
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 0, Total: n, Batch: &b0})
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 1, Total: n})

		// The user stops while the call of batch 2 runs. Update runs on
		// this goroutine, so the context ends at once.
		require.NoError(t, ctx.Err())
		h.press("x", "y")
		require.Error(t, ctx.Err(), "y cancels the run context")

		b1 := x.Batches[1]
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 1, Total: n, Batch: &b1})
		progress(distill.ExtractEvent{Kind: distill.ExtractStopped, Index: 2, Total: n, Stop: distill.StopCancelled,
			Err: context.Canceled})

		return x, nil
	})

	h = extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	require.Equal(t, stepResult, rec.step, "the run ended and every queued event arrived")
	assert.Equal(t, 1, f.callCount("RunExtract"))
	assert.Error(t, seen.Err())
	assert.Equal(t, 2, rec.run.finished, "both done events arrived")
	require.True(t, rec.run.stopped, "the stop event arrived")
	assert.Equal(t, distill.StopCancelled, rec.run.stopWhy)
	assert.True(t, rec.run.checked)
	assert.False(t, h.m.st.busy())
	assert.Equal(t, []int64{21, 6, 4}, rec.remaining())

	flat := extTallFlat(h)
	assert.Contains(t, flat, "Trigger extraction stopped")
	assert.Contains(t, flat, "You stopped the run.")
	assert.Contains(t, flat, "Cancelled p21, p6")
	assert.Contains(t, flat, "Not attempted p4")
	assert.Contains(t, flat, "1 proposal was not attempted.")
	assert.NotContains(t, flat, "✓ completed", "a stopped run is never a success")
}

func TestExtractQueueHoldsEveryEventOfARun(t *testing.T) {
	// A short queue would block the worker in the harness, so the size
	// is checked first and the test stops before it can hang.
	for n := range 20 {
		require.GreaterOrEqual(t, cap(agentQueue(n)), 2*n+2, "2n+1 engine events and the end message fit")
	}

	// A run that sends the most events the engine sends: a start and a
	// done event per batch, and a stop after the last batch.
	p := extPreview(extThreeBatches())
	f := extFake(p, func(_ context.Context, rp *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		x := extExec(rp, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
			for i := range bs {
				extAnswered(&bs[i])

				for j := range bs[i].Items {
					extNegative(&bs[i].Items[j], false)
				}
			}

			x.Stop, x.StopErr = distill.StopConflict, errors.New("the lessons file changed")
		})

		for i := range x.Batches {
			b := x.Batches[i]
			progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: i, Total: 3})
			progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: i, Total: 3, Batch: &b})
		}

		progress(distill.ExtractEvent{Kind: distill.ExtractStopped, Index: 3, Total: 3, Stop: distill.StopConflict})

		return x, nil
	})

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	assert.Equal(t, stepResult, rec.step)
	assert.Equal(t, 3, rec.run.finished)
	assert.Contains(t, extTallFlat(h), "Trigger extraction finished with a conflict")
}

func TestExtractProgressShowsTypedEvents(t *testing.T) {
	p := extPreview(extThreeBatches())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)
	rec := extMidRun(t, h)

	frame := h.view()
	assert.Contains(t, frame, "Extracting triggers · 2 of 5 done · batch 2 of 3", "the notice counts proposals")
	assert.Contains(t, frame, "Sending batch 2 of 3 · 2 proposals")
	assert.Contains(t, frame, "no change since the preview")
	assert.Contains(t, frame, "1 of 3 batches done")
	assert.Contains(t, frame, "stored: 1 path")
	assert.Contains(t, frame, "negative (omitted)")
	assert.Contains(t, frame, "sending")
	assert.Contains(t, frame, "waiting")
	assert.Contains(t, frame, "2 of 5 saved")
	assert.Contains(t, frame, "(estimated)")
	assert.Contains(t, footerLine(frame), "x stop")
	assert.Equal(t, 2, rec.run.cursor, "the cursor follows the batch that runs")

	// The notice row follows the run on every destination.
	h.press("1")
	assert.Contains(t, h.view(), "Extracting triggers · 2 of 5 done")
	assert.Contains(t, lesBoxFlat(h.view()), "Trigger extraction running · 2 of 5 done")

	h.press("2")
	assert.Contains(t, h.view(), "Extracting triggers · 2 of 5 done")
	assert.Contains(t, h.view(), "Lessons › Run")
}

func TestExtractStopAsksFirstAndStopsOnce(t *testing.T) {
	p := extPreview(extThreeBatches())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)
	rec := extMidRun(t, h)

	h.press("x")
	require.True(t, rec.capturing())

	modal, ok := rec.modal(120, 39)
	require.True(t, ok)
	assert.Contains(t, modal, "Stop the trigger extraction?")
	assert.Contains(t, lesBoxFlat(modal), "cancels the agent call of batch 2")
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
	assert.Contains(t, frame, "waiting for the agent call of batch 2")
	assert.Contains(t, frame, "Stopping after batch 2 of 3")
	assert.NotContains(t, footerLine(frame), "x stop", "x is off while stopping")

	h.press("x")
	assert.False(t, rec.capturing(), "no second stop question")

	h.send(extDoneMsg{rec: rec, exec: extStoppedExec(rec.prev.preview), at: time.Now()})
	assert.Equal(t, stepResult, rec.step)
	assert.False(t, h.m.st.busy())
	assert.Contains(t, extTallFlat(h), "Trigger extraction stopped")
}

func TestExtractQuitQuestionStopsTheRun(t *testing.T) {
	p := extPreview(extThreeBatches())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)
	rec := extMidRun(t, h)

	h.press("q")
	require.Contains(t, h.view(), "Stop and exit?")
	assert.Contains(t, lesBoxFlat(h.view()), "Extracting trigger information is running.")

	h.press("enter")
	assert.True(t, h.m.st.run.stopping)
	assert.True(t, rec.stopping(), "the record shows the stop of the shell")
	assert.Contains(t, h.view(), "Stopping after batch 2 of 3")

	h.send(extDoneMsg{rec: rec, exec: extStoppedExec(rec.prev.preview), at: time.Now()})
	assert.Equal(t, stepResult, rec.step)
	assert.True(t, h.quit, "the console exits after the stopped work ends")
}

func TestExtractResultGroupsEveryOutcome(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extConflictExec)

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	require.Equal(t, stepResult, rec.step)

	flat := extTallFlat(h)
	for _, want := range []string{
		"Trigger extraction finished with a conflict",
		"p6 changed during the agent call; its answer was not saved.",
		"4 answers are saved: 2 with trigger paths, 2 negative.",
		"Batches 2 completed",
		"Answers 4 saved: 2 with trigger paths, 2 negative (1 omitted from the reply)",
		"Paths named for 3 proposals, valid for 2, stored for 2",
		"Pending 2 region sets changed: p23, p22 (still pending)",
		"Applied p4 stored trigger paths as advice; the pin scope stays",
		"review p4 against the live note",
		"Stored p23, p4",
		"Negative p22, p21; omitted from the reply: p22",
		"Conflict p6: changed during the agent call; not saved",
		"prepare a new preview of the 1 open question (p6)",
		"Nothing is retried automatically, and nothing chains into a retarget or an apply.",
		"stored: 2 paths · new regions", "negative (omitted) · new regions", "negative: 1 path named, not found",
		"conflict: not saved",
	} {
		assert.Contains(t, flat, want)
	}

	assert.NotContains(t, flat, "✓ completed", "a run with an open question is no success")

	// The conflict came in the last batch, so every batch finished and no
	// Stop row contradicts the headline.
	assert.NotContains(t, flat, "Stop an input changed")
	assert.Equal(t, []int64{6}, rec.remaining())
	assert.Contains(t, h.view(), "Trigger extraction finished with a conflict: 4 answers saved.", "the toast")
}

func TestExtractResultNamesFailedAndUnattemptedItems(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extThreeBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, func(p *learning.ExtractPreview) *distill.ExtractExecution {
		return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
			extFailBatch(&bs[0], distill.FailInvalidReply, errors.New("parse reply: invalid character 'S'"))
			extFailBatch(&bs[1], distill.FailTimeout, context.DeadlineExceeded)
			x.Stop, x.StopErr = distill.StopStoreError, errors.New("database is locked")
		})
	})

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	flat := extTallFlat(h)
	assert.Contains(t, flat, "Trigger extraction stopped on a store error")
	assert.Contains(t, flat, "A store read or write failed: database is locked.")
	assert.Contains(t, flat, "Failed p23, p22 (invalid reply); p21, p6 (timeout)")
	assert.Contains(t, flat, "Not attempted p4")
	assert.Contains(t, flat, "Answers none saved")
	assert.Contains(t, flat, "failed: invalid reply")

	// The cursor opens on the first item that needs attention.
	assert.Equal(t, 0, extRecOf(t, h).run.cursor)

	h.resize(80, 120)
	h.press("tab")
	assert.Contains(t, lesBoxFlat(h.view()), "failed: invalid reply: parse reply: invalid character 'S'. The "+
		"question stays open")
}

func TestExtractResultLinksOnlyNavigate(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extConflictExec)
	f.ledger.Pending = append(f.ledger.Pending, extRows()[0])

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	before := len(f.called())

	// The cursor opens on the conflict p6, which links nothing.
	require.Equal(t, 3, rec.run.cursor)
	assert.NotContains(t, footerLine(h.view()), "enter open")

	h.press("enter")
	assert.Equal(t, lesRun, hookLessons(h).sub, "enter on p6 opens nothing")

	// p4 is applied and stored trigger paths: enter opens its pin.
	h.press("down")
	assert.Contains(t, footerLine(h.view()), "enter open pins")
	assert.Contains(t, extTallFlat(h), "enter show p4 in Lessons › Pins; a retarget is a separate preview there")

	h.press("enter")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesPins, hookLessons(h).sub, "the link shows the pin")
	assert.False(t, hookLessons(h).pins.host.open(), "the link opens no preview")

	// p23 is pending and stored an answer: enter shows the proposal.
	h.press("u")
	require.Same(t, rec, extRecOf(t, h))

	for range 4 {
		h.press("up")
	}

	require.Equal(t, 0, rec.run.cursor)
	h.press("enter")
	assert.Equal(t, lesProposals, hookLessons(h).sub)
	assert.Equal(t, int64(23), lesProps(h).cursorID, "the cursor shows the proposal of the result")

	assert.Len(t, f.called(), before, "links plan, commit and run nothing")
}

func TestExtractPrepareRemainingPlansANewPreview(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
		reqs []learning.ExtractRequest
	)

	p := extPreview(extThreeBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extStoppedExec)
	plan := f.planExtract
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		reqs = append(reqs, req)

		return plan(req)
	}

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	first := extRecOf(t, h)
	require.Equal(t, stepResult, first.step)
	assert.Contains(t, footerLine(h.view()), "n prepare remaining")

	h.press("n")

	next := extRecOf(t, h)
	require.NotSame(t, first, next, "n opens a new record")
	assert.Equal(t, stepPreview, next.step, "the new record plans and waits for y")
	require.Len(t, reqs, 2)
	assert.Equal(t, []int64{21, 6, 4}, reqs[1].IDs, "it plans exactly the open questions")
	assert.Empty(t, reqs[1].Skipped)
	assert.Equal(t, 1, f.callCount("RunExtract"), "nothing runs again by itself")
	assert.Contains(t, runViewOf(h).past, runRecord(first), "the result stays in the session list")
}

func TestExtractRefusedStartReturnsToTheReview(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		return nil, fmt.Errorf("%w: p21 is applied now", distill.ErrExtractStale)
	})

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	assert.Equal(t, stepPreview, rec.step)
	assert.False(t, h.m.st.busy())
	assert.False(t, rec.runnable())
	assert.Contains(t, h.view(), "The trigger extraction did not start: p21 is applied now.")
	assert.Contains(t, extTallFlat(h), "The preview is out of date: p21 is applied now. Nothing was sent.")
	assert.NotContains(t, footerLine(h.view()), "y start")

	h.press("enter")
	assert.Equal(t, 2, f.callCount("PlanExtract"), "only a new preview helps")
	assert.True(t, rec.runnable())
}

func TestExtractSettingsSaveMarksTheReviewStale(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)

	h.send(settingsSavedMsg{group: settings.GroupInference, at: time.Now()})

	rec := extRecOf(t, h)
	assert.False(t, rec.runnable(), "the preview binds the old configuration")

	// Nobody pressed y, so the note reports an old preview, not a refused
	// start.
	flat := extTallFlat(h)
	assert.Contains(t, flat, "This preview is out of date: Settings changed (inference). Nothing was sent. "+
		"Press enter to plan again.")
	assert.NotContains(t, flat, "The run did not start")

	h.press("y")
	assert.Zero(t, f.callCount("RunExtract"))
}

func TestExtractInternalErrorKeepsTheEventsThatArrived(t *testing.T) {
	p := extPreview(extThreeBatches())
	f := extFake(p, func(_ context.Context, rp *learning.ExtractPreview, progress func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		x := extStoppedExec(rp)
		b0 := x.Batches[0]

		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 0, Total: 3})
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchDone, Index: 0, Total: 3, Batch: &b0})
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Index: 1, Total: 3})

		panic("store handle lost")
	})

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	rec := extRecOf(t, h)
	require.Equal(t, stepResult, rec.step)
	assert.True(t, rec.run.internal)
	assert.False(t, h.m.st.busy(), "a panic frees the slot")

	flat := extTallFlat(h)
	assert.Contains(t, flat, "Trigger extraction stopped on an internal error")
	assert.Contains(t, flat, "panic: store handle lost")
	assert.Contains(t, flat, "Batch 2 was sending when the error happened")
	assert.Contains(t, flat, "Unknown p21, p6")
	assert.Contains(t, flat, "Stored p23")
}

func TestExtractSessionHistoryKeepsTheRun(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extConflictExec)

	h := extOpen(t, f, p, 120, 40)
	h.press("y")

	first := extRecOf(t, h)

	// A second review keeps the finished run as an earlier record.
	h.send(openExtractMsg{req: learning.ExtractRequest{IDs: []int64{21}}})
	require.NotSame(t, first, extRecOf(t, h))

	h.press("[")
	frame := h.view()
	assert.Contains(t, frame, "Earlier run 1 of 1 · read-only · ] later")
	assert.Contains(t, frame, "Trigger extraction finished with a conflict")

	// An earlier record moves but never starts work.
	h.press("down", "enter", "n", "y")
	assert.Equal(t, 1, f.callCount("RunExtract"))
	assert.Equal(t, 2, f.callCount("PlanExtract"))

	h.press("]")
	assert.NotContains(t, h.view(), "Earlier run")
}

func TestExtractHostileTextStaysInert(t *testing.T) {
	evil := "\x1b[31mred\x1b[0m\x1b]0;title\x07\u009b2J"
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

	o := extThreeBatches()
	o.live = map[int64]string{4: "live " + evil}
	o.ineligible = append(o.ineligible, distill.ExtractSkip{ID: 40, Reason: "reason " + evil})
	o.blockers = nil

	rows := extRows()
	rows[0].Rule = "rule" + evil
	rows[0].Note = "note " + evil + "\nsecond line " + evil
	o.rows = rows

	p := extPreview(o)
	p.Invocation.Argv = []string{"claude", "--token=" + token, evil}
	p.Invocation.Name = "claude" + evil
	p.Plan.Batches[0].Items[0].Evidence = []string{"a/" + evil + ".go"}

	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, func(p *learning.ExtractPreview) *distill.ExtractExecution {
		return extExec(p, func(bs []distill.ExtractBatchOutcome, _ *distill.ExtractExecution) {
			extAnswered(&bs[0])
			extStored(&bs[0].Items[0], "path/"+evil+".go")
			extFailBatch(&bs[1], distill.FailInvalidReply, errors.New("reply "+evil+" token="+token))
		})
	})

	h := extOpen(t, f, p, 120, 40)

	check := func(stage string) {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			raw := h.m.View()
			extAssertInert(t, raw, stage, size[0], size[1])
			assert.NotContains(t, raw, token, "%s %v: the token is redacted", stage, size)
		}

		h.resize(120, 40)
	}

	check("review")

	h.press("tab")
	check("review plan")

	h.press("tab", "y")
	check("result")

	h.press("down", "down", "tab")
	check("result detail")
}

func TestExtractFramesAreASCIIOnlyAndFit(t *testing.T) {
	p := extPreview(extThreeBatches())

	h := &harness{t: t, m: newModel(extFake(p, extNoRun(t)), newTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(120, 40))
	h.send(openExtractMsg{req: p.Request})

	check := func(stage string) {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			frame := h.view()
			assertFrame(t, frame, size[0], size[1])

			for _, r := range frame {
				require.Less(t, r, rune(128), "%s %v holds %q", stage, size, r)
			}
		}

		h.resize(120, 40)
	}

	check("review")

	h.press("down", "down", "down", "down")
	check("review live note")

	rec := extMidRun(t, h)
	check("running")

	h.press("x")
	check("stop question")

	h.press("y")
	check("stopping")

	h.send(extDoneMsg{rec: rec, exec: extStoppedExec(rec.prev.preview), at: time.Now()})
	check("result")
	assert.Contains(t, extTallFlat(h), "(estimated)", "an approximate number carries its word in ASCII")
}

func TestExtractReviewEscDropsTheDraft(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)

	h.press("esc")

	// The shell decides which sub-view shows after the drop, so the test
	// checks only that the draft left no record.
	assert.Nil(t, runViewOf(h).cur, "a review did no work, so it leaves no record")
	assert.NotContains(t, h.view(), "Trigger extraction")
	assert.Equal(t, []string{"PlanExtract"}, f.called())
}

func TestExtractRequestWaitsForARunningExtraction(t *testing.T) {
	p := extPreview(extThreeBatches())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)
	rec := extMidRun(t, h)

	// t in Proposals asks for another review while the run works.
	h.press("p", "t")

	assert.Same(t, rec, runViewOf(h).cur, "the running record keeps the view")
	assert.Contains(t, h.view(), "A run is in progress: Trigger extraction")
	assert.Equal(t, 1, f.callCount("PlanExtract"), "a refused request plans nothing")
}
