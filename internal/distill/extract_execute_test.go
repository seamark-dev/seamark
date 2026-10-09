package distill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// extractAgent scripts Invoke with the call context and the 1-based call
// number, so a test can stop the run from inside a call. It records every
// prompt it receives.
type extractAgent struct {
	mu   sync.Mutex
	sent []string
	fn   func(ctx context.Context, call int, prompt string) (string, error)
}

func (a *extractAgent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.sent = append(a.sent, prompt)
	call := len(a.sent)
	a.mu.Unlock()

	return a.fn(ctx, call, prompt)
}

func (a *extractAgent) Name() string { return "fake" }

// prompts returns the recorded prompts.
func (a *extractAgent) prompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.sent...)
}

// replyWith answers every call with the same reply.
func replyWith(reply string) *extractAgent {
	return &extractAgent{fn: func(context.Context, int, string) (string, error) { return reply, nil }}
}

// triggerReply builds a reply in the requested contract.
func triggerReply(answers ...extractAnswer) string {
	if answers == nil {
		answers = []extractAnswer{}
	}

	data, _ := json.Marshal(map[string]any{"triggers": answers})

	return string(data)
}

// answerFor answers proposal p with paths. An empty call gives the
// explicit negative answer [].
func answerFor(p model.Proposal, paths ...string) extractAnswer {
	if paths == nil {
		paths = []string{}
	}

	return extractAnswer{ID: p.ID, TriggerPaths: paths}
}

// negativeForAll answers every item of every prompt with [].
func negativeForAll(plan *ExtractPlan) *extractAgent {
	return &extractAgent{fn: func(_ context.Context, call int, _ string) (string, error) {
		var answers []extractAnswer
		for _, p := range plan.Batches[call-1].Proposals {
			answers = append(answers, answerFor(p))
		}

		return triggerReply(answers...), nil
	}}
}

// extractEvents records the events of one execution.
type extractEvents struct {
	events []ExtractEvent
}

func (l *extractEvents) record(ev ExtractEvent) { l.events = append(l.events, ev) }

func (l *extractEvents) kinds() []ExtractEventKind {
	out := make([]ExtractEventKind, len(l.events))
	for i, ev := range l.events {
		out[i] = ev.Kind
	}

	return out
}

// fixedClock returns a clock that always reads at.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// stampTime is the fixed stamp of the executor tests.
var stampTime = time.Unix(1_800_000_000, 0)

// execute runs plan on the console path (Boundary on) with a fixed
// clock and records the events.
func execute(ctx context.Context, w *extractWorkspace, inv *extractAgent, plan *ExtractPlan,
	check func() error,
) (*ExtractExecution, *extractEvents, error) {
	log := &extractEvents{}

	x, err := ExecuteExtract(ctx, w.st, inv, plan, ExtractExecuteOptions{
		Boundary: true, Check: check, OnEvent: log.record, Now: fixedClock(stampTime),
	})

	return x, log, err
}

// manyPending stores n unanswered pending rows with living evidence.
func manyPending(t *testing.T, w *extractWorkspace, n int) []int64 {
	t.Helper()

	ids := make([]int64, n)
	for i := range n {
		ids[i] = w.pending(t, fmt.Sprintf("rule-%02d", i), 1).ID
	}

	return ids
}

// stamped reports whether p carries a trigger stamp.
func stamped(t *testing.T, w *extractWorkspace, id int64) bool {
	t.Helper()

	return w.row(t, id).TriggerChecked != 0
}

func TestExecuteExtractStoresValidPathsAndKeepsAppliedScopes(t *testing.T) {
	w := newExtractWorkspace(t)
	pending := w.pending(t, "schema-sync", 1)
	installed := w.applied(t, "schema-sync-applied", 1)
	plan := w.plan(t, pending.ID, installed.ID)

	// The ghost path fails the existence rung; item 999 is not in the
	// batch and must be ignored.
	reply := triggerReply(
		answerFor(pending, "api/schemas.py", "api/ghost.py"),
		answerFor(installed, "api/schemas.py"),
		extractAnswer{ID: 999, TriggerPaths: []string{"api/schemas.py"}},
	)
	inv := replyWith(reply)

	x, log, err := execute(context.Background(), w, inv, plan, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{plan.Batches[0].Prompt}, inv.prompts(), "the frozen prompt is sent")
	assert.Equal(t, StopNone, x.Stop)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone}, log.kinds())

	b := x.Batches[0]
	assert.Equal(t, BatchCompleted, b.Status)
	assert.Equal(t, len(plan.Batches[0].Prompt), b.PromptChars)
	assert.Equal(t, len(reply), b.ReplyChars)
	assert.Equal(t, stampTime, b.Started)
	assert.Equal(t, stampTime, b.Finished)

	p := b.Items[0]
	assert.Equal(t, ExtractStored, p.Status)
	assert.Equal(t, 2, p.NamedRaw)
	assert.Equal(t, []string{"api/schemas.py", "api/ghost.py"}, p.Named)
	assert.Equal(t, []string{"api/schemas.py"}, p.Valid)
	assert.Equal(t, []TriggerFact{{Path: "api/schemas.py", Region: "api/schemas.py", Together: 38, Selected: true}},
		p.Facts)
	assert.Equal(t, []string{"web/src/api"}, p.RegionsBefore)
	assert.Equal(t, []string{"api/schemas.py"}, p.RegionsAfter)
	assert.True(t, p.RegionsChanged)

	a := b.Items[1]
	assert.Equal(t, ExtractStored, a.Status)
	assert.Equal(t, []string{"api/schemas.py"}, a.Valid)
	assert.Len(t, a.Facts, 1, "facts describe an applied row's stored trigger too")
	assert.Equal(t, a.RegionsBefore, a.RegionsAfter)
	assert.False(t, a.RegionsChanged)

	row := w.row(t, pending.ID)
	assert.Equal(t, []string{"api/schemas.py"}, row.TriggerPaths)
	assert.Equal(t, "api/schemas.py", row.Region)
	assert.Equal(t, []string{"api/schemas.py"}, row.Regions)
	assert.Equal(t, stampTime.Unix(), row.TriggerChecked)
	assert.Equal(t, TriggerPromptVersion, row.TriggerPromptVersion)

	row = w.row(t, installed.ID)
	assert.Equal(t, []string{"api/schemas.py"}, row.TriggerPaths)
	assert.Equal(t, "web/src/api", row.Region, "an installed pin's scope never changes here")
	assert.Empty(t, row.Regions)
	assert.Equal(t, model.ProposalApplied, row.Status)

	assert.Equal(t, []int64{pending.ID, installed.ID}, x.Persisted)
	assert.Empty(t, x.Failed)
	assert.Empty(t, x.Conflicts)
	assert.Equal(t, ExtractResult{Examined: 2, Named: 2, Stored: 2, Retargeted: 1, AppliedStored: 1,
		PromptChars: b.PromptChars, ReplyChars: len(reply)}, x.Result)
}

func TestExecuteExtractNegativeAnswersAreStamped(t *testing.T) {
	cases := []struct {
		name     string
		paths    []string
		widened  bool
		namedRaw int
		named    []string
		changed  bool
	}{
		{name: "explicit empty list"},
		{name: "only invalid paths", paths: []string{"/abs/x.go", "../escape.go", " "}, namedRaw: 3},
		{name: "only absent paths", paths: []string{"api/ghost.py"}, namedRaw: 1, named: []string{"api/ghost.py"}},
		{name: "negative answer restores evidence coverage", widened: true, changed: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)

			p := w.pending(t, "schema-sync", 1)
			if tc.widened {
				p = w.add(t, model.Proposal{Signature: "s-w", Rule: "widened", Region: "web/src/api",
					Regions: []string{"web/src/api", "api"}, Note: "n", Members: []int64{1},
					Status: model.ProposalProposed})
			}

			plan := w.plan(t, p.ID)

			x, _, err := execute(context.Background(), w, replyWith(triggerReply(answerFor(p, tc.paths...))), plan, nil)
			require.NoError(t, err)

			item := x.Batches[0].Items[0]
			assert.Equal(t, ExtractNegative, item.Status, "a valid negative answer is a completed question")
			assert.Equal(t, tc.namedRaw, item.NamedRaw)
			assert.Equal(t, tc.named, item.Named)
			assert.Empty(t, item.Valid)
			assert.Equal(t, tc.changed, item.RegionsChanged)
			assert.Equal(t, []int64{p.ID}, x.Persisted)

			row := w.row(t, p.ID)
			assert.Nil(t, row.TriggerPaths)
			assert.Equal(t, stampTime.Unix(), row.TriggerChecked, "the negative answer is stamped")
			assert.Equal(t, TriggerPromptVersion, row.TriggerPromptVersion)
			assert.Equal(t, []string{"web/src/api"}, row.RegionSet(), "the pending row holds evidence coverage")

			again := w.plan(t, p.ID)
			assert.Empty(t, again.Batches, "the answered question is not bought again")
		})
	}
}

func TestExecuteExtractFailedCallsStampNothing(t *testing.T) {
	restore := perExtractTimeout
	perExtractTimeout = 50 * time.Millisecond

	t.Cleanup(func() { perExtractTimeout = restore })

	cases := []struct {
		name    string
		fn      func(ctx context.Context) (string, error)
		failure FailureKind
	}{
		{"prose", func(context.Context) (string, error) { return "I could not decide.", nil }, FailInvalidReply},
		{"no triggers key", func(context.Context) (string, error) { return `{}`, nil }, FailInvalidReply},
		{"null triggers", func(context.Context) (string, error) { return `{"triggers": null}`, nil }, FailInvalidReply},
		{"misspelled key", func(context.Context) (string, error) { return `{"trigger": []}`, nil }, FailInvalidReply},
		{"agent error", func(context.Context) (string, error) {
			return "", errors.New("agent fake: rate limited (exit status 1)")
		}, FailAgent},
		{"timeout", func(ctx context.Context) (string, error) {
			<-ctx.Done()

			return "", fmt.Errorf("agent fake: %w", ctx.Err())
		}, FailTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			a := w.pending(t, "first", 1)
			b := w.applied(t, "second", 2)
			plan := w.plan(t, a.ID, b.ID)

			inv := &extractAgent{fn: func(ctx context.Context, _ int, _ string) (string, error) { return tc.fn(ctx) }}

			x, _, err := execute(context.Background(), w, inv, plan, nil)
			require.NoError(t, err, "a lost batch is data, not an error")

			batch := x.Batches[0]
			assert.Equal(t, BatchFailed, batch.Status)
			assert.Equal(t, tc.failure, batch.Failure)
			require.Error(t, batch.Err)
			assert.Equal(t, plan.Batches[0].PromptChars, batch.PromptChars, "the call was paid for")

			for _, item := range batch.Items {
				assert.Equal(t, ExtractFailed, item.Status)
				assert.Equal(t, tc.failure, item.Failure)
				assert.False(t, stamped(t, w, item.Proposal.ID), "no stamp: the question stays open")
			}

			assert.Equal(t, StopNone, x.Stop)
			assert.Equal(t, []int64{a.ID, b.ID}, x.Failed)
			assert.Empty(t, x.Persisted)
			assert.Equal(t, 1, x.Result.BatchesFailed)
			assert.Equal(t, 2, x.Result.Examined)
			assert.Equal(t, []int64{a.ID, b.ID}, w.plan(t, a.ID, b.ID).IDs(), "both stay eligible")
		})
	}
}

func TestExecuteExtractFailedBatchDoesNotStopTheRun(t *testing.T) {
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)
	require.Len(t, plan.Batches, 2)

	inv := &extractAgent{fn: func(_ context.Context, call int, _ string) (string, error) {
		if call == 1 {
			return "", errors.New("agent fake: boom")
		}

		return triggerReply(answerFor(plan.Batches[1].Proposals[0]), answerFor(plan.Batches[1].Proposals[1])), nil
	}}

	x, _, err := execute(context.Background(), w, inv, plan, nil)
	require.NoError(t, err)

	assert.Equal(t, BatchFailed, x.Batches[0].Status)
	assert.Equal(t, BatchCompleted, x.Batches[1].Status, "batches fail independently")
	assert.Len(t, x.Failed, 10)
	assert.Len(t, x.Persisted, 2)
	assert.Equal(t, StopNone, x.Stop)
}

func TestExecuteExtractOmittedItemsAreStampedNegatives(t *testing.T) {
	// The prompt asks the model to list only the items that qualify, so an
	// item without an entry in a valid reply is a completed negative answer
	// (D-19, revision 3), as in the CLI. The omitted row cites
	// api/handlers.go but is stored at web/src/api, so its negative answer
	// moves it back to its evidence coverage.
	cases := []struct {
		name  string
		reply func(answered model.Proposal) string
		// answered is true when the reply has an entry for the first row.
		answered bool
	}{
		{name: "one item left out", answered: true,
			reply: func(answered model.Proposal) string { return triggerReply(answerFor(answered)) }},
		{name: "the literal empty reply of the prompt",
			reply: func(model.Proposal) string { return `{"triggers": []}` }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			first := w.pending(t, "first", 1)
			omitted := w.pending(t, "omitted", 2)
			installed := w.applied(t, "installed", 2)
			plan := w.plan(t, first.ID, omitted.ID, installed.ID)

			x, _, err := execute(context.Background(), w, replyWith(tc.reply(first)), plan, nil)
			require.NoError(t, err)

			assert.Equal(t, BatchCompleted, x.Batches[0].Status, "the reply itself was valid")
			assert.Equal(t, StopNone, x.Stop)

			byID := map[int64]ExtractItemOutcome{}
			for _, item := range x.Batches[0].Items {
				byID[item.Proposal.ID] = item
			}

			for _, id := range []int64{first.ID, omitted.ID, installed.ID} {
				item := byID[id]
				assert.Equal(t, ExtractNegative, item.Status, "p%d is a completed negative", id)
				assert.Equal(t, id != first.ID || !tc.answered, item.Omitted, "p%d shows why it is negative", id)
				require.NoError(t, item.Err, "p%d is stored, not failed", id)
				assert.Zero(t, item.NamedRaw)
				assert.True(t, stamped(t, w, id), "p%d is stamped", id)
				assert.Equal(t, TriggerPromptVersion, w.row(t, id).TriggerPromptVersion)
			}

			gone := byID[omitted.ID]
			assert.True(t, gone.RegionsChanged, "the omitted pending row returns to its evidence coverage")
			assert.Equal(t, []string{"web/src/api"}, gone.RegionsBefore)
			assert.Equal(t, []string{"api"}, gone.RegionsAfter)
			assert.Equal(t, []string{"api"}, w.row(t, omitted.ID).RegionSet())

			assert.False(t, byID[installed.ID].RegionsChanged, "an installed pin keeps its scope")
			assert.Equal(t, []string{"web/src/api"}, w.row(t, installed.ID).RegionSet())

			assert.Equal(t, plan.IDs(), x.Persisted, "every item is stored, in send order")
			assert.Empty(t, x.Failed)
			assert.Empty(t, w.plan(t, first.ID, omitted.ID, installed.ID).Batches,
				"the answered questions are not bought again")
		})
	}
}

func TestExecuteExtractDuplicateAnswersKeepTheFirstNamedPath(t *testing.T) {
	// The console and the CLI choose the same answer of an item: the first
	// one with a path that passes the syntax rung, else the first one.
	cases := []struct {
		name    string
		answers [][]string
		status  ExtractItemStatus
		named   []string
		valid   []string
	}{
		{name: "an empty first answer does not hide a later path",
			answers: [][]string{{}, {"api/schemas.py"}},
			status:  ExtractStored, named: []string{"api/schemas.py"}, valid: []string{"api/schemas.py"}},
		{name: "an invalid first answer does not hide a later path",
			answers: [][]string{{"../outside"}, {"api/schemas.py"}},
			status:  ExtractStored, named: []string{"api/schemas.py"}, valid: []string{"api/schemas.py"}},
		{name: "the first named path wins over a later one",
			answers: [][]string{{"api/schemas.py"}, {"api/handlers.go"}},
			status:  ExtractStored, named: []string{"api/schemas.py"}, valid: []string{"api/schemas.py"}},
		{name: "a clean first path wins even when it does not exist",
			answers: [][]string{{"api/ghost.py"}, {"api/schemas.py"}},
			status:  ExtractNegative, named: []string{"api/ghost.py"}},
		{name: "only empty or invalid answers are a negative",
			answers: [][]string{{}, {"/abs/x.go"}},
			status:  ExtractNegative},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			p := w.pending(t, "schema-sync", 1)
			plan := w.plan(t, p.ID)

			answers := make([]extractAnswer, len(tc.answers))
			for i, paths := range tc.answers {
				answers[i] = answerFor(p, paths...)
			}

			x, _, err := execute(context.Background(), w, replyWith(triggerReply(answers...)), plan, nil)
			require.NoError(t, err)

			item := x.Batches[0].Items[0]
			assert.Equal(t, tc.status, item.Status)
			assert.Equal(t, tc.named, item.Named)
			assert.Equal(t, tc.valid, item.Valid)
			assert.Equal(t, tc.valid, w.row(t, p.ID).TriggerPaths)
			assert.Equal(t, tc.named, namedTriggers(answers, plan.Batches[0].Proposals)[p.ID],
				"the CLI keeps the same answer")
		})
	}
}

func TestExecuteExtractStopsBetweenBatches(t *testing.T) {
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)
	require.Len(t, plan.Batches, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inv := negativeForAll(plan)
	log := &extractEvents{}

	x, err := ExecuteExtract(ctx, w.st, inv, plan, ExtractExecuteOptions{
		Boundary: true,
		OnEvent: func(ev ExtractEvent) {
			log.record(ev)

			if ev.Kind == ExtractBatchDone && ev.Index == 0 {
				cancel() // the stop arrives while the first result is shown
			}
		},
		Now: fixedClock(stampTime),
	})
	require.NoError(t, err)

	assert.Len(t, inv.prompts(), 1, "no call starts after the stop")
	assert.Equal(t, StopCancelled, x.Stop)
	require.ErrorIs(t, x.StopErr, context.Canceled)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())
	assert.Equal(t, 1, log.events[2].Index, "the stop names the first batch that did not run")

	assert.Equal(t, BatchCompleted, x.Batches[0].Status, "committed results stay")
	assert.Equal(t, BatchUnattempted, x.Batches[1].Status)
	assert.Zero(t, x.Batches[1].PromptChars, "an unstarted batch meters nothing")
	assert.True(t, x.Batches[1].Started.IsZero())

	assert.Equal(t, plan.Batches[0].Proposals[0].ID, x.Persisted[0])
	assert.Len(t, x.Persisted, 10)
	assert.Len(t, x.Unattempted, 2)
	assert.Equal(t, 10, x.Result.Examined)
	assert.Equal(t, plan.Batches[0].PromptChars, x.Result.PromptChars)

	for _, p := range plan.Batches[1].Proposals {
		assert.False(t, stamped(t, w, p.ID), "p%d stays open", p.ID)
	}
}

func TestExecuteExtractStopDuringTheStartEventStartsNoCall(t *testing.T) {
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inv := negativeForAll(plan)
	log := &extractEvents{}

	x, err := ExecuteExtract(ctx, w.st, inv, plan, ExtractExecuteOptions{
		Boundary: true,
		OnEvent: func(ev ExtractEvent) {
			log.record(ev)

			if ev.Kind == ExtractBatchStarted {
				cancel()
			}
		},
	})
	require.NoError(t, err)

	assert.Empty(t, inv.prompts())
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())
	assert.Equal(t, BatchCancelled, x.Batches[0].Status)
	assert.Zero(t, x.Batches[0].PromptChars, "the call never started")
	assert.Len(t, x.Cancelled, 10)
	assert.Len(t, x.Unattempted, 2)
	assert.Zero(t, x.Result.Examined)
	assert.Equal(t, StopCancelled, x.Stop)
}

func TestExecuteExtractStopDuringACallCancelsThatBatch(t *testing.T) {
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inv := &extractAgent{fn: func(callCtx context.Context, _ int, _ string) (string, error) {
		cancel()
		<-callCtx.Done()

		return "", fmt.Errorf("agent fake: %w", callCtx.Err())
	}}

	x, log, err := execute(ctx, w, inv, plan, nil)
	require.NoError(t, err)

	b := x.Batches[0]
	assert.Equal(t, BatchCancelled, b.Status, "a stopped call is cancelled, never failed")
	assert.Equal(t, FailNone, b.Failure)
	assert.Equal(t, plan.Batches[0].PromptChars, b.PromptChars, "the started call is metered")
	require.ErrorIs(t, b.Err, context.Canceled)

	for _, item := range b.Items {
		assert.Equal(t, ExtractCancelled, item.Status)
		assert.False(t, stamped(t, w, item.Proposal.ID))
	}

	assert.Equal(t, StopCancelled, x.Stop)
	assert.Equal(t, BatchUnattempted, x.Batches[1].Status)
	assert.Zero(t, x.Result.BatchesFailed)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())
}

func TestExecuteExtractKeepsAReplyThatArrivedBeforeTheStop(t *testing.T) {
	for _, n := range []int{3, 12} {
		t.Run(fmt.Sprintf("%d proposals", n), func(t *testing.T) {
			w := newExtractWorkspace(t)
			ids := manyPending(t, w, n)
			plan := w.plan(t, ids...)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			reply := negativeForAll(plan)
			inv := &extractAgent{fn: func(callCtx context.Context, call int, prompt string) (string, error) {
				cancel() // the stop arrives together with the reply

				return reply.fn(callCtx, call, prompt)
			}}

			x, _, err := execute(ctx, w, inv, plan, nil)
			require.NoError(t, err)

			assert.Equal(t, BatchCompleted, x.Batches[0].Status, "a stop never splits a parsed batch")

			for _, p := range plan.Batches[0].Proposals {
				assert.True(t, stamped(t, w, p.ID), "p%d is stored", p.ID)
			}

			if len(plan.Batches) == 1 {
				assert.Equal(t, StopNone, x.Stop, "a stop after the last reply prevents nothing")

				return
			}

			assert.Equal(t, StopCancelled, x.Stop)
			assert.Equal(t, BatchUnattempted, x.Batches[1].Status)
		})
	}
}

func TestExecuteExtractStopsOnABoundaryConflict(t *testing.T) {
	errConfig := errors.New("config.yaml changed since the preview")

	cases := []struct {
		name string
		// before changes the store after planning and before the run.
		before func(t *testing.T, w *extractWorkspace, plan *ExtractPlan)
		// during changes the store inside the first agent call.
		during func(t *testing.T, w *extractWorkspace, plan *ExtractPlan)
		// failCheck is the 1-based call of Check that fails.
		failCheck int
		calls     int
		stopIndex int
		want      string
	}{
		{name: "row changed before the start", before: func(t *testing.T, w *extractWorkspace, plan *ExtractPlan) {
			_, err := w.st.SetProposalStatus([]int64{plan.Batches[1].Proposals[0].ID}, model.ProposalApplied)
			require.NoError(t, err)
		}, want: "is applied now"},
		{name: "evidence aged out before the start", before: func(t *testing.T, w *extractWorkspace, _ *ExtractPlan) {
			require.NoError(t, w.st.ReplaceLessons(nil, nil))
		}, want: "the living evidence of"},
		{name: "row status changed during the first batch",
			during: func(t *testing.T, w *extractWorkspace, plan *ExtractPlan) {
				_, err := w.st.SetProposalStatus([]int64{plan.Batches[1].Proposals[0].ID}, model.ProposalDismissed)
				require.NoError(t, err)
			}, calls: 1, stopIndex: 1, want: "is dismissed now"},
		{name: "config hash changed before the start", failCheck: 1, want: errConfig.Error()},
		{name: "config hash changed after the first batch", failCheck: 3, calls: 1, stopIndex: 1,
			want: errConfig.Error()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			ids := manyPending(t, w, 12)
			plan := w.plan(t, ids...)

			if tc.before != nil {
				tc.before(t, w, plan)
			}

			answers := negativeForAll(plan)
			inv := &extractAgent{fn: func(ctx context.Context, call int, prompt string) (string, error) {
				if tc.during != nil {
					tc.during(t, w, plan)
				}

				return answers.fn(ctx, call, prompt)
			}}

			checks := 0
			check := func() error {
				checks++
				if checks == tc.failCheck {
					return errConfig
				}

				return nil
			}

			x, log, err := execute(context.Background(), w, inv, plan, check)
			require.NoError(t, err, "a conflict is data, not an error")

			assert.Equal(t, StopConflict, x.Stop)
			require.Error(t, x.StopErr)
			assert.Contains(t, x.StopErr.Error(), tc.want)
			assert.Len(t, inv.prompts(), tc.calls)
			assert.Equal(t, ExtractStopped, log.events[len(log.events)-1].Kind)
			assert.Equal(t, tc.stopIndex, log.events[len(log.events)-1].Index)

			for i := tc.stopIndex; i < len(x.Batches); i++ {
				assert.Equal(t, BatchUnattempted, x.Batches[i].Status, "remaining work needs a new plan")
				assert.Zero(t, x.Batches[i].PromptChars)
			}

			if tc.failCheck == 0 {
				require.ErrorIs(t, x.StopErr, ErrExtractStale)
			}
		})
	}
}

func TestExecuteExtractConflictAtPersist(t *testing.T) {
	cases := []struct {
		name    string
		applied bool
		mutate  func(t *testing.T, w *extractWorkspace, target model.Proposal)
		status  string
	}{
		{name: "applied during the call", mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			_, err := w.st.SetProposalStatus([]int64{p.ID}, model.ProposalApplied)
			require.NoError(t, err)
		}, status: model.ProposalApplied},
		{name: "dismissed during the call", mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			_, err := w.st.SetProposalStatus([]int64{p.ID}, model.ProposalDismissed)
			require.NoError(t, err)
		}, status: model.ProposalDismissed},
		{name: "superseded during the call", applied: true,
			mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
				_, err := w.st.SupersedeProposals([]int64{p.ID})
				require.NoError(t, err)
			}, status: model.ProposalSuperseded},
		{name: "retargeted during the call", applied: true,
			mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
				require.NoError(t, w.st.RetargetApplied([]store.RegionChange{{
					Guard: store.NewRowGuard(p), Region: "api", Regions: []string{"api"},
				}}))
			}, status: model.ProposalApplied},
		{name: "pending regions changed during the call", mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			p.Region, p.Regions = "api", []string{"api"}
			_, err := w.st.UpdateProposalRegionsIfPending(p)
			require.NoError(t, err)
		}, status: model.ProposalProposed},
		{name: "answered by another extraction", mutate: func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			require.NoError(t, w.st.UpdateProposalTriggers(p.ID, []string{"api/handlers.go"}, 5, TriggerPromptVersion))
		}, status: model.ProposalProposed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			other := w.pending(t, "other", 1)

			target := w.pending(t, "target", 1)
			if tc.applied {
				target = w.applied(t, "target", 1)
			}

			plan := w.plan(t, other.ID, target.ID)
			before := w.row(t, target.ID)

			inv := &extractAgent{fn: func(context.Context, int, string) (string, error) {
				tc.mutate(t, w, target)

				return triggerReply(answerFor(other, "api/schemas.py"), answerFor(target, "api/schemas.py")), nil
			}}

			x, _, err := execute(context.Background(), w, inv, plan, nil)
			require.NoError(t, err)

			byID := map[int64]ExtractItemOutcome{}
			for _, item := range x.Batches[0].Items {
				byID[item.Proposal.ID] = item
			}

			got := byID[target.ID]
			assert.Equal(t, ExtractConflict, got.Status)
			require.ErrorIs(t, got.Err, ErrExtractStale)
			assert.False(t, got.RegionsChanged, "a discarded answer changes no scope")
			assert.Equal(t, ExtractStored, byID[other.ID].Status, "the rest of the batch is stored")
			assert.Equal(t, BatchCompleted, x.Batches[0].Status)
			assert.Equal(t, StopConflict, x.Stop, "another writer is active, so the run stops")
			require.ErrorIs(t, x.StopErr, ErrExtractStale)
			assert.Equal(t, []int64{target.ID}, x.Conflicts)
			assert.Equal(t, []int64{other.ID}, x.Persisted)

			if row, err := w.st.ProposalsWithIDs([]int64{target.ID}); assert.NoError(t, err) && len(row) == 1 {
				assert.Equal(t, tc.status, row[0].Status)
				assert.NotEqual(t, stampTime.Unix(), row[0].TriggerChecked, "the paid answer is not written")

				if tc.status == model.ProposalApplied && !tc.applied {
					assert.Equal(t, before.RegionSet(), row[0].RegionSet(),
						"no result redirects a now-applied pin's scope")
				}
			}
		})
	}
}

func TestExecuteExtractConflictAtPersistStopsTheRun(t *testing.T) {
	// A save conflict means another writer is active. The executor stops
	// as Execute does, so it buys no further call that the same writer
	// could make worthless (D-20).
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)
	require.Len(t, plan.Batches, 2)

	target := plan.Batches[0].Proposals[0]
	answers := negativeForAll(plan)
	inv := &extractAgent{fn: func(ctx context.Context, call int, prompt string) (string, error) {
		if call == 1 {
			_, err := w.st.SetProposalStatus([]int64{target.ID}, model.ProposalDismissed)
			require.NoError(t, err)
		}

		return answers.fn(ctx, call, prompt)
	}}

	x, log, err := execute(context.Background(), w, inv, plan, nil)
	require.NoError(t, err, "a conflict is data, not an error")

	assert.Len(t, inv.prompts(), 1, "no call starts after the conflict")
	assert.Equal(t, StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, ErrExtractStale)
	assert.Contains(t, x.StopErr.Error(), fmt.Sprintf("p%d changed during the agent call", target.ID))
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())
	assert.Equal(t, 1, log.events[2].Index, "the stop names the first batch that did not run")

	assert.Equal(t, BatchCompleted, x.Batches[0].Status, "the rest of the paid batch is stored")
	assert.Equal(t, []int64{target.ID}, x.Conflicts)
	assert.Len(t, x.Persisted, 9)
	assert.Equal(t, BatchUnattempted, x.Batches[1].Status)
	assert.Zero(t, x.Batches[1].PromptChars)
	assert.Len(t, x.Unattempted, 2)
}

func TestExecuteExtractStoreFailureKeepsEarlierAnswersVisible(t *testing.T) {
	// The third save of the batch fails. The two answers stored before it
	// stay stored and visible (AC-10); only the failing item and the
	// items after it are reported as not saved.
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 5)
	plan := w.plan(t, ids...)
	batch := plan.Batches[0].Proposals
	failing := batch[2].ID

	w.exec(t, fmt.Sprintf(`CREATE TRIGGER boom BEFORE UPDATE ON proposal WHEN NEW.id = %d
		BEGIN SELECT RAISE(ABORT, 'boom'); END`, failing))

	var answers []extractAnswer
	for _, p := range batch {
		answers = append(answers, answerFor(p, "api/schemas.py"))
	}

	x, log, err := execute(context.Background(), w, replyWith(triggerReply(answers...)), plan, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")

	b := x.Batches[0]
	assert.Equal(t, BatchFailed, b.Status)
	assert.Equal(t, FailStore, b.Failure)
	assert.Equal(t, StopStoreError, x.Stop)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())

	for j, item := range b.Items {
		id := item.Proposal.ID

		if j < 2 {
			assert.Equal(t, ExtractStored, item.Status, "p%d was saved before the failure", id)
			require.NoError(t, item.Err)
			assert.True(t, stamped(t, w, id), "p%d is stamped", id)

			continue
		}

		assert.Equal(t, ExtractFailed, item.Status, "p%d is not saved", id)
		assert.Equal(t, FailStore, item.Failure)
		require.Error(t, item.Err)
		assert.False(t, stamped(t, w, id), "p%d is not stamped", id)
	}

	assert.Equal(t, []int64{batch[0].ID, batch[1].ID}, x.Persisted)
	assert.Equal(t, []int64{batch[2].ID, batch[3].ID, batch[4].ID}, x.Failed)
	assert.Equal(t, 2, x.Result.Stored)
}

func TestExecuteExtractStopDuringACheckStartsNoBatch(t *testing.T) {
	// Check runs once before the start and once at each batch boundary. A
	// stop that arrives during a check is seen before the next batch
	// starts, so that batch stays unattempted and meters nothing.
	cases := []struct {
		name string
		// cancelOn is the 1-based call of Check that stops the run.
		cancelOn int
		calls    int
		events   []ExtractEventKind
		stopped  int
	}{
		{name: "during the start check", cancelOn: 1,
			events: []ExtractEventKind{ExtractStopped}},
		{name: "during the check of the first batch", cancelOn: 2,
			events: []ExtractEventKind{ExtractStopped}},
		{name: "during the check of the second batch", cancelOn: 3, calls: 1, stopped: 1,
			events: []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			ids := manyPending(t, w, 12)
			plan := w.plan(t, ids...)
			require.Len(t, plan.Batches, 2)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			checks := 0
			check := func() error {
				checks++
				if checks == tc.cancelOn {
					cancel()
				}

				return nil
			}

			inv := negativeForAll(plan)

			x, log, err := execute(ctx, w, inv, plan, check)
			require.NoError(t, err)

			assert.Len(t, inv.prompts(), tc.calls)
			assert.Equal(t, StopCancelled, x.Stop)
			assert.Equal(t, tc.events, log.kinds(), "the stopped batch never reports a start")
			assert.Equal(t, tc.stopped, log.events[len(log.events)-1].Index)

			for i := tc.stopped; i < len(x.Batches); i++ {
				assert.Equal(t, BatchUnattempted, x.Batches[i].Status)
				assert.Zero(t, x.Batches[i].PromptChars)
			}
		})
	}
}

func TestExecuteExtractConflictWhenTheRowIsDeleted(t *testing.T) {
	w := newExtractWorkspace(t)
	target := w.pending(t, "target", 1)
	plan := w.plan(t, target.ID)

	inv := &extractAgent{fn: func(context.Context, int, string) (string, error) {
		_, err := w.st.DeletePendingProposals([]int64{target.ID})
		require.NoError(t, err)

		return triggerReply(answerFor(target, "api/schemas.py")), nil
	}}

	x, _, err := execute(context.Background(), w, inv, plan, nil)
	require.NoError(t, err)

	assert.Equal(t, []int64{target.ID}, x.Conflicts, "a deleted row is never counted stored")
	assert.Zero(t, x.Result.Stored)
}

func TestExecuteExtractStoreFailureStopsTheRun(t *testing.T) {
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	plan := w.plan(t, ids...)

	inv := &extractAgent{fn: func(_ context.Context, _ int, _ string) (string, error) {
		_ = w.st.Close() // the store dies while the agent is thinking

		var answers []extractAnswer
		for _, p := range plan.Batches[0].Proposals {
			answers = append(answers, answerFor(p, "api/schemas.py"))
		}

		return triggerReply(answers...), nil
	}}

	x, log, err := execute(context.Background(), w, inv, plan, nil)
	require.Error(t, err)
	require.NotNil(t, x)

	b := x.Batches[0]
	assert.Equal(t, BatchFailed, b.Status)
	assert.Equal(t, FailStore, b.Failure)

	for _, item := range b.Items {
		assert.Equal(t, ExtractFailed, item.Status)
		assert.Equal(t, FailStore, item.Failure)
	}

	assert.Equal(t, StopStoreError, x.Stop)
	assert.Equal(t, BatchUnattempted, x.Batches[1].Status)
	assert.Zero(t, x.Result.BatchesFailed, "a store failure is not an agent failure")
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())

	reopened, err := store.Open(w.path)
	require.NoError(t, err)

	defer func() { _ = reopened.Close() }()

	rows, err := reopened.ProposalsWithIDs(ids)
	require.NoError(t, err)

	for _, row := range rows {
		assert.Zero(t, row.TriggerChecked, "p%d is not stamped", row.ID)
	}
}

func TestExecuteExtractEmptyPlanNeverInvokes(t *testing.T) {
	w := newExtractWorkspace(t)
	dismissed := w.add(t, model.Proposal{Signature: "s-d", Rule: "dismissed", Note: "n",
		Members: []int64{1}, Status: model.ProposalDismissed})
	answered := w.add(t, model.Proposal{Signature: "s-a", Rule: "answered", Note: "n", Members: []int64{1},
		Status: model.ProposalProposed, TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion})

	for name, plan := range map[string]*ExtractPlan{
		"empty selection":      w.plan(t),
		"nothing is eligible":  w.plan(t, dismissed.ID, answered.ID),
		"no living evidence":   w.plan(t, w.pending(t, "aged-out", 99).ID),
		"selection not stored": w.plan(t, 404),
	} {
		t.Run(name, func(t *testing.T) {
			inv := replyWith(`{"triggers": []}`)

			x, log, err := execute(context.Background(), w, inv, plan, nil)
			require.NoError(t, err)

			assert.Empty(t, inv.prompts(), "an empty plan never invokes the agent")
			assert.Empty(t, log.events)
			assert.Empty(t, x.Batches)
			assert.Equal(t, StopNone, x.Stop)
			assert.Zero(t, x.Result.PromptChars)

			nilAgent, err := ExecuteExtract(context.Background(), w.st, nil, plan, ExtractExecuteOptions{Boundary: true})
			require.NoError(t, err, "no invoker is needed when nothing runs")
			assert.Empty(t, nilAgent.Batches)
		})
	}

	assert.False(t, stamped(t, w, dismissed.ID))
}

func TestExecuteExtractRefusesUnusableCalls(t *testing.T) {
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	plan := w.plan(t, p.ID)

	x, err := ExecuteExtract(context.Background(), w.st, replyWith(`{"triggers": []}`), nil, ExtractExecuteOptions{Boundary: true})
	require.Error(t, err)
	assert.NotNil(t, x, "the result is never nil")

	inv := replyWith(triggerReply(answerFor(p)))

	x, err = ExecuteExtract(context.Background(), w.st, nil, plan, ExtractExecuteOptions{Boundary: true})
	require.ErrorIs(t, err, ErrNoInvoker)
	assert.Equal(t, []int64{p.ID}, x.Unattempted)

	rootless := *plan
	rootless.Options.Root = ""

	log := &extractEvents{}

	x, err = ExecuteExtract(context.Background(), w.st, inv, &rootless, ExtractExecuteOptions{Boundary: true, OnEvent: log.record})
	require.ErrorIs(t, err, ErrExtractRoot, "an empty root would store every answer as a negative")
	assert.Empty(t, inv.prompts())
	assert.Empty(t, log.events)
	assert.Equal(t, []int64{p.ID}, x.Unattempted)

	mismatched := *plan
	mismatched.Batches = []ExtractBatch{{Proposals: plan.Batches[0].Proposals, Prompt: "x"}}

	_, err = ExecuteExtract(context.Background(), w.st, inv, &mismatched, ExtractExecuteOptions{Boundary: true})
	require.Error(t, err)
	assert.Empty(t, inv.prompts())
	assert.False(t, stamped(t, w, p.ID))
}

func TestExecuteExtractEventsFollowPersistence(t *testing.T) {
	// Three batches prove the event bound a TUI queue relies on, and each
	// ExtractBatchDone arrives only after its answers are in the store.
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 25)
	plan := w.plan(t, ids...)
	require.Len(t, plan.Batches, 3)

	log := &extractEvents{}

	x, err := ExecuteExtract(context.Background(), w.st, negativeForAll(plan), plan, ExtractExecuteOptions{
		Boundary: true,
		OnEvent: func(ev ExtractEvent) {
			log.record(ev)

			assert.Equal(t, 3, ev.Total)

			if ev.Kind != ExtractBatchDone {
				return
			}

			require.NotNil(t, ev.Batch)

			for _, p := range plan.Batches[ev.Index].Proposals {
				assert.True(t, stamped(t, w, p.ID), "p%d is stored before its batch is reported", p.ID)
			}

			ev.Batch.Items[0].Status = ExtractFailed // a receiver cannot change the run
		},
	})
	require.NoError(t, err)

	assert.Len(t, log.events, 6)
	assert.LessOrEqual(t, len(log.events), 2*len(plan.Batches)+1)
	assert.Equal(t, ExtractNegative, x.Batches[0].Items[0].Status, "events carry copies")
	assert.Len(t, x.Persisted, 25)
}

func TestExecuteExtractEventsShareNoSliceWithTheRun(t *testing.T) {
	// A receiver writes into every slice of the reported outcome. The run's
	// result must not change, because a TUI receiver runs on another
	// goroutine and may sort or edit what it shows.
	w := newExtractWorkspace(t)
	pending := w.pending(t, "schema-sync", 1)
	installed := w.applied(t, "installed", 1)
	plan := w.plan(t, pending.ID, installed.ID)

	reply := triggerReply(answerFor(pending, "api/schemas.py"), answerFor(installed, "api/schemas.py"))

	x, err := ExecuteExtract(context.Background(), w.st, replyWith(reply), plan, ExtractExecuteOptions{
		Boundary: true,
		OnEvent: func(ev ExtractEvent) {
			if ev.Kind != ExtractBatchDone {
				return
			}

			for i := range ev.Batch.Items {
				item := &ev.Batch.Items[i]
				item.Proposal.Members[0] = -1
				item.Named[0] = "MUTATED"
				item.Valid[0] = "MUTATED"
				item.Facts[0].Path = "MUTATED"
				item.RegionsBefore[0] = "MUTATED"
				item.RegionsAfter[0] = "MUTATED"
			}
		},
		Now: fixedClock(stampTime),
	})
	require.NoError(t, err)

	p, a := x.Batches[0].Items[0], x.Batches[0].Items[1]

	assert.Equal(t, []int64{1}, p.Proposal.Members)
	assert.Equal(t, []string{"api/schemas.py"}, p.Named)
	assert.Equal(t, []string{"api/schemas.py"}, p.Valid)
	assert.Equal(t, "api/schemas.py", p.Facts[0].Path)
	assert.Equal(t, []string{"web/src/api"}, p.RegionsBefore)
	assert.Equal(t, []string{"api/schemas.py"}, p.RegionsAfter)
	assert.Equal(t, []string{"web/src/api"}, a.RegionsBefore)
	assert.Equal(t, []string{"web/src/api"}, a.RegionsAfter)

	// An unchanged row has equal sets, but not one shared slice.
	a.RegionsAfter[0] = "elsewhere"
	assert.Equal(t, []string{"web/src/api"}, a.RegionsBefore)
}

func TestExtractStatusStrings(t *testing.T) {
	assert.Equal(t, []string{"unattempted", "stored", "negative", "conflict", "failed", "cancelled",
		"ExtractItemStatus(6)"}, []string{
		ExtractUnattempted.String(), ExtractStored.String(), ExtractNegative.String(), ExtractConflict.String(),
		ExtractFailed.String(), ExtractCancelled.String(), ExtractItemStatus(6).String(),
	})
	assert.Equal(t, []string{"unattempted", "completed", "failed", "cancelled", "ExtractBatchStatus(4)"},
		[]string{BatchUnattempted.String(), BatchCompleted.String(), BatchFailed.String(),
			BatchCancelled.String(), ExtractBatchStatus(4).String()})
	assert.Equal(t, []string{"batch started", "batch done", "stopped", "ExtractEventKind(3)"},
		[]string{ExtractBatchStarted.String(), ExtractBatchDone.String(), ExtractStopped.String(),
			ExtractEventKind(3).String()})
}
