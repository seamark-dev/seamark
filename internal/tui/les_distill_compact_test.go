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
)

// The tests in this file cover the small layouts of a distillation: the
// footer keeps the key of each step at 60 columns, the compact box keeps
// the headline and the stop note, the bar counts saved groups only, and
// the stop question keeps the width of the quit question.

// distFooterHead returns the first hint of the footer of the frame.
func distFooterHead(h *harness) string {
	return strings.TrimSpace(shellAFooter(h.view()))
}

// distASCIIOpen opens a distillation draft with the ASCII theme.
func distASCIIOpen(t *testing.T, f *fakeServices, w, hh int) *harness {
	t.Helper()

	h := &harness{t: t, m: newModel(f, newTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(w, hh))
	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	return h
}

func TestDistillFooterLeadsWithTheStepKeyAtEverySize(t *testing.T) {
	var calls []distRunCall

	failed := func(p *learning.DistillPreview) *distill.Execution {
		return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
			for i := range gs {
				distFail(&gs[i], distill.FailAgent, errors.New("not logged in"))
			}
		})
	}

	sizes := [][2]int{{120, 40}, {80, 24}, {60, 20}}

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, distAllDone, nil)
	h := distOpen(t, f, 120, 40)

	scope := map[int]string{120: "enter review plan", 80: "enter plan", 60: "enter plan"}
	for _, size := range sizes {
		h.resize(size[0], size[1])
		assert.True(t, strings.HasPrefix(distFooterHead(h), scope[size[0]]), "scope %v: %q", size, distFooterHead(h))
	}

	h.resize(120, 40)
	h.press("enter")

	for _, size := range sizes {
		h.resize(size[0], size[1])
		assert.True(t, strings.HasPrefix(distFooterHead(h), "y start run"), "review %v: %q", size, distFooterHead(h))
	}

	h.resize(120, 40)
	h.press("y")

	result := map[int]string{120: "enter review proposals   n plan again", 80: "enter review   n plan again", 60: "enter review"}
	for _, size := range sizes {
		h.resize(size[0], size[1])
		assert.True(t, strings.HasPrefix(distFooterHead(h), result[size[0]]), "result %v: %q", size, distFooterHead(h))
	}

	// A result without new proposals leads with the new plan.
	f2 := distFake(distFullPreview(), nil)
	f2.runDistill = distScripted(&calls, failed, nil)
	h2 := distOpen(t, f2, 120, 40)
	h2.press("enter", "y")

	for _, size := range sizes {
		h2.resize(size[0], size[1])
		assert.True(t, strings.HasPrefix(distFooterHead(h2), "n plan again"), "failed %v: %q", size, distFooterHead(h2))
	}
}

func TestDistillCompactRunKeepsTheStopKeyVisible(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		f := distFake(distFullPreview(), distNoRun(t))

		h := distOpen(t, f, 120, 40)
		if ascii {
			h = distASCIIOpen(t, f, 120, 40)
		}

		rec := distMidRun(t, h)

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			assert.True(t, strings.HasPrefix(distFooterHead(h), "x stop"),
				"ascii %v %v: the stop key of a paid run leads the footer: %q", ascii, size, distFooterHead(h))
			assert.Contains(t, lesBoxFlat(h.view()), "x stops the run", "ascii %v %v: the box names the stop key", ascii,
				size)
		}

		// Moving the cursor to the last group scrolls the table, never the
		// headline or the stop note.
		h.press("down", "down")
		require.Equal(t, 5, rec.run.cursor)

		frame := h.view()
		assert.Contains(t, frame, "Sending group 4 of 6")
		assert.Contains(t, frame, "x stops the run. Finished groups stay saved.")
		assert.Contains(t, frame, "internal/gate", "the cursor group shows")
	}
}

func TestDistillCompactStoppingKeepsTheHeadline(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 60, 20)
	distMidRun(t, h)

	h.press("x", "y")

	frame := h.view()
	assert.Contains(t, frame, "Stopping after group 4 of 6")
	assert.Contains(t, frame, "Seamark waits for the agent call of group 4 to end.")
	assert.NotContains(t, shellAFooter(frame), "x stop", "a second stop is not offered")
}

func TestDistillCompactResultOpensAtTheVerdict(t *testing.T) {
	var calls []distRunCall

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, distPartialExec, nil)

	h := distOpen(t, f, 60, 20)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	require.Equal(t, distResult, rec.step)
	require.Equal(t, 3, rec.run.cursor, "the cursor starts on the first open group")

	// The notice row also shows the verdict, so the check reads the box
	// line with its border.
	headline := "│ ! Distillation stopped "

	frame := h.view()
	assert.Contains(t, frame, headline, "the verdict is the first box line")
	assert.Contains(t, frame, "You stopped the run.", "the lead shows before the table")

	// A move follows the cursor through the table; the verdict stays.
	h.press("down", "down")

	frame = h.view()
	assert.Contains(t, frame, headline)
	assert.Contains(t, frame, "internal/gate", "the box follows the moved cursor")
}

func TestDistillBarCountsSavedGroupsOnly(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distStartHeld(t, h)

	distSend(h, rec, distStarted(rec, 0))
	distSend(h, rec, distFinished(rec, 0, distill.GroupOutcome{
		Status: distill.GroupCompleted, Proposals: distProposals("r", 21),
	}))
	distSend(h, rec, distStarted(rec, 1))
	distSend(h, rec, distFinished(rec, 1, distill.GroupOutcome{
		Status: distill.GroupFailed, Failure: distill.FailAgent, Err: errors.New("exit status 1"),
	}))

	frame := h.view()
	assert.Contains(t, frame, "GROUPS  1 of 6 saved", "the success bar never counts a failed group")
	assert.Contains(t, frame, "1 / 6")
	assert.Contains(t, frame, "2 of 6 done", "the progress still counts every finished group")

	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assert.Contains(t, h.view(), "1 of 6 saved", "size %v", size)
	}

	// A result has no bar: its table and counts tell each outcome apart.
	x := distExec(rec.plan.preview, func(gs []distill.GroupOutcome, _ *distill.Execution) {
		for i := range gs {
			distFail(&gs[i], distill.FailAgent, errors.New("not logged in"))
		}
	})
	h.send(distDoneMsg{rec: rec, exec: x, at: time.Now()})
	require.Equal(t, distResult, rec.step)

	bar := h.m.st.theme.Glyphs.BarFull

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assert.Contains(t, frame, "! failed", "size %v", size)
		assert.NotContains(t, frame, bar, "size %v: a failed run shows no filled bar", size)
	}
}

func TestDistillStopQuestionKeepsTheQuitWidth(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distMidRun(t, h)

	h.press("x")
	require.True(t, rec.run.stopAsk)

	for _, size := range [][2]int{{200, 50}, {120, 40}, {80, 24}, {60, 20}} {
		block, ok := rec.modal(size[0], size[1]-1)
		require.True(t, ok)

		for _, line := range strings.Split(block, "\n") {
			assert.LessOrEqual(t, width(line), 64, "size %v: the stop question is at most 64 cells wide", size)
		}
	}
}

func TestDistillPanicAfterWorkShowsAFailedResult(t *testing.T) {
	f := distFake(distFullPreview(), nil)
	f.runDistill = func(_ context.Context, p *learning.DistillPreview, forward func(distill.Event)) (*distill.Execution, error) {
		n := len(p.Plan.Groups)
		g0, g1 := p.Plan.Preflight.Groups[0], p.Plan.Preflight.Groups[1]
		saved := distill.GroupOutcome{
			Plan: g0, Status: distill.GroupCompleted, Proposals: distProposals("r", 31, 32), PromptChars: 7600,
		}

		forward(distill.Event{Kind: distill.EventCleanupDone, Removed: 2})
		forward(distill.Event{Kind: distill.EventGroupStarted, Index: 0, Total: n, Group: &g0})
		forward(distill.Event{Kind: distill.EventGroupFinished, Index: 0, Total: n, Outcome: &saved})
		forward(distill.Event{Kind: distill.EventGroupStarted, Index: 1, Total: n, Group: &g1})

		panic("nil map write")
	}

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	require.Equal(t, distResult, rec.step, "work began, so the record shows a result, not the review")
	assert.False(t, h.m.st.busy(), "the slot frees")
	assert.Nil(t, rec.plan.stale)

	ids, _, at := rec.generated()
	assert.Equal(t, []int64{31, 32}, ids, "the saved proposals stay reviewable")
	assert.False(t, at.IsZero())

	frame := h.view()
	assert.Contains(t, frame, "! failed")
	assert.NotContains(t, frame, "did not start")

	flat := distTallFlat(h)
	for _, want := range []string{
		"Distillation stopped on an internal error", "panic: nil map write",
		"1 group is saved with 2 new proposals.",
		"Group 2 was sending when the error happened, so its result is not known.",
		"1 saved · 1 result unknown · 4 not attempted", "result unknown", "2 of 2 stale pending removed",
	} {
		assert.Contains(t, flat, want)
	}

	assert.NotContains(t, flat, "Nothing was sent", "a group was sent before the error")
	assert.NotContains(t, flat, "✓ completed")

	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "Distillation stopped on an internal error", "size %v", size)
	}
}

func TestDistillPanicBeforeAnyEventNeverClaimsASafeCleanup(t *testing.T) {
	f := distFake(distFullPreview(), func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
		panic("boom")
	})

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	require.Equal(t, distResult, rec.step, "a panic is a failed run, never a refused start")
	assert.False(t, h.m.st.busy())

	flat := distTallFlat(h)
	assert.Contains(t, flat, "Distillation stopped on an internal error")
	assert.Contains(t, flat, "6 groups were not attempted.")
	assert.Contains(t, flat, "not known: the run stopped on an internal error before the cleanup reported")
	assert.NotContains(t, flat, "result unknown", "no group started")
	assert.NotContains(t, flat, "Nothing was sent")
}
