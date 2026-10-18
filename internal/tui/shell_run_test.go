package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the run slot of the shell. One stoppable run holds the
// slot, its notice shows on every destination, and quit stops the run
// before the console exits.

// claimDistillRun claims the slot for a stoppable run of 10 groups, as a
// distillation screen does, and returns the context of the work.
func claimDistillRun(t *testing.T, h *harness) context.Context {
	t.Helper()

	ctx, cancel := h.m.st.runContext()
	t.Cleanup(cancel)

	require.True(t, h.m.st.beginRun("Distilling proposals", runNote{
		verb: "Distilling", detail: "internal/api", done: 3, total: 10, stop: cancel,
		started: time.Now().Add(-134 * time.Second), home: navigateMsg{dest: destLessons, intent: "run"},
	}))
	h.m.st.lastRunKind = runDistill

	return ctx
}

// noticeRow returns the notice row of the current frame: the row under
// the destination tabs. The run tests and the cross-screen flow tests
// read it.
func noticeRow(h *harness) string {
	return strings.Split(h.view(), "\n")[3]
}

func TestShellRunClaimsTheSingleSlot(t *testing.T) {
	st := &state{}

	ctx, cancel := st.runContext()
	defer cancel()

	require.True(t, st.beginRun("Refreshing evidence", runNote{verb: "Refreshing evidence", stop: cancel}))
	assert.True(t, st.busy())
	assert.False(t, st.run.started.IsZero(), "a zero start becomes the start time")
	assert.False(t, st.begin("Applying the setup"), "a run holds the one mutation slot")
	assert.False(t, st.beginRun("Distilling", runNote{}), "a second run is refused")

	st.updateRun(func(n *runNote) { n.done, n.total = 1, 2 })
	assert.Equal(t, 1, st.run.done)

	require.True(t, st.requestStop())
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "a stop cancels the context of the work")
	assert.True(t, st.run.stopping)
	assert.True(t, st.requestStop(), "a repeated stop is harmless")
	assert.True(t, st.busy(), "the work decides when it ends; the slot stays until then")

	plain := &state{}
	require.True(t, plain.begin("Building the index"))
	assert.False(t, plain.requestStop(), "work without a stop cannot stop")
	plain.updateRun(func(n *runNote) { n.done = 5 })
	assert.Nil(t, plain.run, "an update without a run creates nothing")
}

func TestShellMutationDoneClearsTheRun(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	claimDistillRun(t, h)

	h.send(mutationDoneMsg{})
	assert.Nil(t, h.m.st.run)
	assert.False(t, h.m.st.busy())
	assert.Equal(t, runDistill, h.m.st.lastRunKind, "the session keeps the kind of its last run")
	assert.NotContains(t, noticeRow(h), "Distilling")
}

func TestShellNoticeShowsTheRunAcrossDestinations(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	claimDistillRun(t, h)

	for _, dest := range []string{"1", "2", "4", "5"} {
		h.press(dest)

		row := noticeRow(h)
		assert.Contains(t, row, "Distilling · 3 of 10 done · internal/api · 2m 14s", "destination %s", dest)
		assert.Regexp(t, `Lessons › Run $`, row, "destination %s: the hint names the run detail", dest)
	}

	// Without a known total the row shows no count.
	h.m.st.updateRun(func(n *runNote) { n.total = 0 })
	assert.NotContains(t, noticeRow(h), " of ")

	// A stop shows at once, before the work ends.
	h.m.st.requestStop()
	assert.Contains(t, noticeRow(h), "Distilling · Stopping…")
}

// The design map §6 leaves out the hint on the Run view itself, because
// the user sees the run detail there already.
func TestShellNoticeLeavesOutTheHintOnTheRunHome(t *testing.T) {
	h := newHarness(t, newFake(), 120, 30)
	claimDistillRun(t, h)

	h.press("3", "u")
	les := h.m.screens[destLessons].(*lessons)
	require.Equal(t, lesRun, les.sub)

	row := noticeRow(h)
	assert.Contains(t, row, "Distilling · 3 of 10 done · internal/api · 2m 14s")
	assert.NotContains(t, row, "›", "the Run view is the run home, so the row names no way to it")
	assert.LessOrEqual(t, ui.Width(row), 120)

	// Every other Lessons sub-view keeps the hint.
	for _, sub := range []string{"p", "s", "e"} {
		h.press(sub)
		assert.Regexp(t, `Lessons › Run $`, noticeRow(h), "sub-view %s", sub)
	}

	// The hint comes back when the user leaves the run home.
	h.press("u", "1")
	assert.Regexp(t, `Lessons › Run $`, noticeRow(h))
}

func TestShellShowsHomeMatchesTheVisibleView(t *testing.T) {
	h := newHarness(t, newFake(), 120, 30)
	h.m.st.lastRunKind = runDistill
	les := h.m.screens[destLessons].(*lessons)

	cases := []struct {
		name   string
		keys   []string
		home   navigateMsg
		expect bool
	}{
		{"another destination", []string{"1"}, navigateMsg{dest: destLessons, intent: "run"}, false},
		{"the destination without an intent", []string{"2"}, navigateMsg{dest: destIntegrations}, true},
		{"a screen without sub-views keeps the hint", []string{"2"}, navigateMsg{dest: destIntegrations, intent: "setup:codex"}, false},
		{"the Run sub-view", []string{"3", "u"}, navigateMsg{dest: destLessons, intent: "run"}, true},
		{"the Run sub-view for a distill intent", []string{"3", "u"}, navigateMsg{dest: destLessons, intent: "distill"}, true},
		{"the Evidence sub-view", []string{"3", "e"}, navigateMsg{dest: destLessons, intent: "evidence"}, true},
		{"the Stats sub-view", []string{"3", "s"}, navigateMsg{dest: destLessons, intent: "stats"}, true},
		{"the Proposals sub-view", []string{"3", "p"}, navigateMsg{dest: destLessons, intent: "proposals"}, true},
		{"the proposals of a run need the run filter", []string{"3", "p"}, navigateMsg{dest: destLessons, intent: "proposals:run"}, false},
		{"another sub-view", []string{"3", "p"}, navigateMsg{dest: destLessons, intent: "run"}, false},
		{"an unknown intent", []string{"3", "p"}, navigateMsg{dest: destLessons, intent: "unknown"}, false},
	}

	for _, c := range cases {
		h.press(c.keys...)
		assert.Equal(t, c.expect, h.m.showsHome(c.home), c.name)
	}

	// With the run filter set, the Proposals sub-view shows the proposals of the run.
	h.press("3", "p")
	les.props.run = &propRunFilter{ids: map[int64]bool{}}
	assert.True(t, h.m.showsHome(navigateMsg{dest: destLessons, intent: "proposals:run"}))
}

func TestShellToastSharesTheRowWithTheRun(t *testing.T) {
	for _, w := range []int{60, 120} {
		h := newHarness(t, newFake(), w, 24)
		claimDistillRun(t, h)

		h.send(toastMsg{tone: ui.ToneSuccess, text: "Saved."})

		row := noticeRow(h)
		assert.Contains(t, row, "✓ Saved.", "width %d: the message stays", w)
		assert.Contains(t, row, "Distilling · 3 of 10 done", "width %d: the run stays visible", w)
		assert.LessOrEqual(t, ui.Width(row), w)
	}
}

func TestShellOtherWorkKeepsItsNotice(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	require.True(t, h.m.st.begin("Building the index"))

	row := noticeRow(h)
	assert.Contains(t, row, "Building the index")
	assert.NotContains(t, row, "›", "work that cannot stop has no run detail")

	h.send(toastMsg{tone: ui.ToneNeutral, text: "Reading the current state."})
	assert.NotContains(t, noticeRow(h), "Building the index", "a message takes the row, as before")
}

func TestShellQuitStopsAStoppableRunFirst(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	ctx := claimDistillRun(t, h)

	h.press("q")
	require.True(t, h.m.quitConfirm)

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "Stop and exit?")
	assert.Contains(t, frame, "Distilling proposals is running. Seamark stops the external")
	assert.Contains(t, frame, "│ enter stop and exit   esc keep working")

	_, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Distilling proposals is running. Seamark stops the external work, waits for it to end, "+
		"then exits. Completed work is kept.", body)
	assert.Contains(t, footerLine(frame), "enter stop and exit")
	require.NoError(t, ctx.Err(), "the question alone stops nothing")

	h.press("enter")
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "enter stops the work")
	assert.True(t, h.m.quitAfter)
	assert.False(t, h.quit, "the console waits for the work to end")
	assert.WithinDuration(t, time.Now().Add(exitWait), h.m.exitBy, time.Second)
	assert.Contains(t, noticeRow(h), "Stopping…")

	h.send(mutationDoneMsg{})
	assert.True(t, h.quit, "the console exits when the stopped work ends")
}

func TestShellKeepWorkingDoesNotStop(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	ctx := claimDistillRun(t, h)

	h.press("q", "esc")
	assert.False(t, h.m.quitConfirm)
	require.NoError(t, ctx.Err())

	h.send(mutationDoneMsg{})
	assert.False(t, h.quit)
}

func TestShellExitDeadlineBoundsTheWait(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	claimDistillRun(t, h)

	h.press("q", "enter")
	require.True(t, h.m.quitAfter)

	by, seq := h.m.exitBy, h.m.exitSeq.last
	h.press("ctrl+c", "enter")
	assert.Equal(t, by, h.m.exitBy, "a repeated yes keeps the first deadline")
	assert.Equal(t, seq, h.m.exitSeq.last)

	h.send(exitDeadlineMsg{seq: h.m.exitSeq.last - 1})
	assert.False(t, h.quit, "the timer of an earlier exit question is dropped")

	h.send(exitDeadlineMsg{seq: h.m.exitSeq.last})
	assert.True(t, h.quit, "the console exits when the stopped work does not end in time")

	// esc cancels the exit; its timer then does nothing.
	h2 := newHarness(t, newFake(), 120, 32)
	claimDistillRun(t, h2)
	h2.press("q", "enter")
	cancelled := h2.m.exitSeq.last
	h2.press("q", "esc")
	assert.True(t, h2.m.exitBy.IsZero())

	h2.send(exitDeadlineMsg{seq: cancelled})
	assert.False(t, h2.quit)
}

func TestCrampedQuitNamesTheStop(t *testing.T) {
	w, hgt := ui.MinWidth-10, ui.MinHeight-2
	h := newHarness(t, newFake(), w, hgt)
	ctx := claimDistillRun(t, h)

	h.press("q")

	frame := h.view()
	assertFrame(t, frame, w, hgt)
	assert.Contains(t, frame, "Stop and exit?")
	assert.Contains(t, lesFlat(frame), "Seamark stops it, waits for it to end, then exits.")
	assert.Contains(t, frame, "stop and exit")

	h.press("enter")
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.False(t, h.quit)
}

func TestShellFooterNamesCtrlCWhileAScreenCaptures(t *testing.T) {
	h := lesOpen(t, pendingLedgerFake(), 120, 32)

	h.press("/")
	require.True(t, h.m.screens[destLessons].capturing())

	footer := footerLine(h.view())
	assert.Contains(t, footer, "ctrl+c quit")
	assert.NotContains(t, footer, " q quit", "q is typed into the region input")

	h.press("esc")
	assert.Contains(t, footerLine(h.view()), "q quit")
}

// TestShellQuitDialogFooterNamesOnlyKeysThatAct pins the review fix:
// the quit question ignores q, so its footer names ctrl+c, and a second
// ctrl+c answers yes.
func TestShellQuitDialogFooterNamesOnlyKeysThatAct(t *testing.T) {
	for _, w := range []int{ui.MinWidth, 120} {
		h := newHarness(t, newFake(), w, 32)
		ctx := claimDistillRun(t, h)

		h.press("q")
		require.True(t, h.m.quitConfirm)

		frame := h.view()
		assertFrame(t, frame, w, 32)

		footer := footerLine(frame)
		assert.Contains(t, footer, "enter stop and exit", "width %d", w)
		assert.Contains(t, footer, "ctrl+c quit", "width %d: the key that acts in the dialog", w)
		assert.NotContains(t, footer, " q quit", "width %d: q does nothing in the dialog", w)
		assert.NotContains(t, footer, "? help", "width %d: the dialog takes ? too", w)

		h.press("q")
		assert.True(t, h.m.quitConfirm, "width %d: a repeated q keeps the question open", w)
		assert.False(t, h.m.quitAfter, "width %d", w)
		require.NoError(t, ctx.Err(), "width %d: a repeated q stops nothing", w)

		h.press("ctrl+c")
		assert.False(t, h.m.quitConfirm, "width %d: ctrl+c answers the question", w)
		assert.ErrorIs(t, ctx.Err(), context.Canceled, "width %d: ctrl+c stops the work like enter", w)
		assert.True(t, h.m.quitAfter, "width %d", w)
		assert.False(t, h.quit, "width %d: the console waits for the work to end", w)

		h.send(mutationDoneMsg{})
		assert.True(t, h.quit, "width %d: the console exits when the stopped work ends", w)
	}
}

// TestShellQuitDialogCtrlCKeepsTheBuildRunning proves that ctrl+c in
// the question of work that cannot stop only latches the exit.
func TestShellQuitDialogCtrlCKeepsTheBuildRunning(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	require.True(t, h.m.st.begin("Building the index"))

	h.press("ctrl+c")
	require.Contains(t, h.view(), "Exit after the running operation?")

	footer := footerLine(h.view())
	assert.Contains(t, footer, "ctrl+c quit")
	assert.NotContains(t, footer, " q quit")

	h.press("ctrl+c")
	assert.True(t, h.m.quitAfter, "a second ctrl+c answers yes")
	assert.False(t, h.quit, "the build cannot stop, so the console waits for it")

	h.send(mutationDoneMsg{})
	assert.True(t, h.quit)
}

func TestShellRunElapsed(t *testing.T) {
	assert.Equal(t, "0s", runElapsed(-time.Second))
	assert.Equal(t, "41s", runElapsed(41*time.Second+300*time.Millisecond))
	assert.Equal(t, "2m 14s", runElapsed(134*time.Second))
	assert.Equal(t, "1h 02m", runElapsed(time.Hour+2*time.Minute+5*time.Second))
}

func TestShellRunNoticeAndStopDialogStayASCII(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {60, 20}, {ui.MinWidth - 10, ui.MinHeight - 2}} {
		h := &harness{t: t, m: newModel(newFake(), ui.NewTheme(true))}
		h.run(h.m.Init())
		h.resize(size[0], size[1])
		claimDistillRun(t, h)

		h.send(toastMsg{tone: ui.ToneSuccess, text: "Saved."})
		h.press("q")

		frames := []string{h.view()}

		h.press("enter")
		frames = append(frames, h.view())

		for _, frame := range frames {
			assertFrame(t, frame, size[0], size[1])

			for _, r := range frame {
				require.Less(t, r, rune(0x80), "size %v: frame holds %q:\n%s", size, r, frame)
			}
		}
	}
}
