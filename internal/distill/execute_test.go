package distill

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// ctxAgent scripts Invoke with the call context, so a test can block
// until a stop or cancel the run from inside a call. It records the
// first finding ID of every prompt, which names the group.
type ctxAgent struct {
	mu    sync.Mutex
	calls []int64
	fn    func(ctx context.Context, prompt string) (string, error)
}

func (a *ctxAgent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, firstID(prompt))
	a.mu.Unlock()

	return a.fn(ctx, prompt)
}

func (a *ctxAgent) Name() string { return "fake" }

// called returns the recorded group IDs.
func (a *ctxAgent) called() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]int64(nil), a.calls...)
}

// emptyReply answers every group with the explicit empty answer.
func emptyReply(context.Context, string) (string, error) { return `{"patterns":[]}`, nil }

// outcomeOf returns the outcome of the planned group whose first member
// is id. The execution order follows signatures, so tests look groups
// up by their members instead of by position.
func outcomeOf(t *testing.T, p *Plan, x *Execution, id int64) GroupOutcome {
	t.Helper()

	for i, pg := range p.Groups {
		if pg.Group.Findings[0].ID == id {
			return x.Groups[i]
		}
	}

	require.Failf(t, "no planned group", "first member %d", id)

	return GroupOutcome{}
}

// eventLog records the kinds of all events and keeps the events.
type eventLog struct {
	events []Event
}

func (l *eventLog) record(ev Event) { l.events = append(l.events, ev) }

func (l *eventLog) kinds() []EventKind {
	out := make([]EventKind, len(l.events))
	for i, ev := range l.events {
		out[i] = ev.Kind
	}

	return out
}

// planAll plans every group of the store with the given pins.
func planAll(t *testing.T, st *store.Store, pins []model.Proposal) *Plan {
	t.Helper()

	p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Pins: pins})
	require.NoError(t, err)

	return p
}

func TestExecuteReportsEveryOutcomeKind(t *testing.T) {
	restore := perGroupTimeout
	perGroupTimeout = 100 * time.Millisecond

	t.Cleanup(func() { perGroupTimeout = restore })

	// Groups by first member: ok 21, dup 23, zero 25, bad 27, fail 29,
	// slow 31.
	st := openSeeded(t, areaFindings("ok", "dup", "zero", "bad", "fail", "slow"))
	pins := []model.Proposal{{Rule: "known-pin-rule", Note: "Keep the known pin guidance exactly."}}

	agent := &ctxAgent{fn: func(ctx context.Context, prompt string) (string, error) {
		switch firstID(prompt) {
		case 21:
			return patternReply("ok-rule", "Keep the ok handlers aligned.", 21, 22), nil
		case 23:
			return patternReply("known-pin-rule", "Keep the known pin guidance exactly.", 23, 24), nil
		case 25:
			return `{"patterns":[]}`, nil
		case 27:
			return "not json", nil
		case 29:
			return "", errors.New("agent fake: rate limited (exit status 1)")
		default:
			<-ctx.Done()

			return "", ctx.Err()
		}
	}}

	p := planAll(t, st, pins)
	require.Len(t, p.Groups, 6)

	var log eventLog

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Progress: log.record, Boundary: true})
	require.NoError(t, err)
	assert.Equal(t, StopNone, x.Stop)
	assert.Len(t, agent.called(), 6)

	ok := outcomeOf(t, p, x, 21)
	assert.Equal(t, GroupCompleted, ok.Status)
	require.Len(t, ok.Proposals, 1)
	assert.Positive(t, ok.Proposals[0].ID, "the outcome carries the stored ID")
	assert.Positive(t, ok.PromptChars)
	assert.Positive(t, ok.ReplyChars)
	assert.False(t, ok.Finished.Before(ok.Started))

	dup := outcomeOf(t, p, x, 23)
	assert.Equal(t, GroupCompleted, dup.Status, "a duplicate-only reply still answers the group")
	assert.Empty(t, dup.Proposals)
	assert.Equal(t, []Duplicate{{Rule: "known-pin-rule", Of: "known-pin-rule"}}, dup.Duplicates)

	zero := outcomeOf(t, p, x, 25)
	assert.Equal(t, GroupCompleted, zero.Status, "an empty answer is a successful zero-proposal group")
	assert.Empty(t, zero.Proposals)

	bad := outcomeOf(t, p, x, 27)
	assert.Equal(t, GroupFailed, bad.Status)
	assert.Equal(t, FailInvalidReply, bad.Failure)

	fail := outcomeOf(t, p, x, 29)
	assert.Equal(t, GroupFailed, fail.Status)
	assert.Equal(t, FailAgent, fail.Failure)
	assert.ErrorContains(t, fail.Err, "rate limited")

	slow := outcomeOf(t, p, x, 31)
	assert.Equal(t, GroupFailed, slow.Status, "a timeout is a failure, not a stop")
	assert.Equal(t, FailTimeout, slow.Failure)
	assert.Positive(t, slow.PromptChars, "the call started, so its prompt counts")

	assert.Equal(t, 3, x.Result.GroupsRead)
	assert.Equal(t, 3, x.Result.GroupsFailed)
	assert.Equal(t, 1, x.Result.Duplicates)
	require.Len(t, x.Result.Proposals, 1)

	// Each group starts and finishes once; the duplicate is reported
	// between the start and the finish of its group.
	started, finished := 0, 0

	for _, ev := range log.events {
		assert.Equal(t, 6, ev.Total, "every event carries the denominator")

		switch ev.Kind {
		case EventGroupStarted:
			started++
		case EventGroupFinished:
			finished++
		case EventDuplicate:
			assert.Equal(t, "known-pin-rule", ev.Dup.Rule)
		}
	}

	assert.Equal(t, 6, started)
	assert.Equal(t, 6, finished)
	assert.NotContains(t, log.kinds(), EventStopped)

	// The store agrees with the outcomes: completed groups are marked,
	// failed ones are not, and only the new pattern is pending.
	marks, err := st.DistilledSignatures()
	require.NoError(t, err)
	assert.Len(t, marks, 3)
	assert.True(t, marks[ok.Plan.Signature])
	assert.True(t, marks[dup.Plan.Signature])
	assert.True(t, marks[zero.Plan.Signature])

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "ok-rule", pending[0].Rule)

	// A new plan retries the failed groups and skips the completed ones.
	next := planAll(t, st, pins)
	assert.Len(t, next.Skipped, 3)

	var retried []int64
	for _, pg := range next.Groups {
		retried = append(retried, pg.Group.Findings[0].ID)
	}

	assert.ElementsMatch(t, []int64{27, 29, 31}, retried)
}

func TestExecuteStopsAtAStoreFailure(t *testing.T) {
	st, path := openFileStore(t, areaFindings("a", "b", "c"))

	agent := &ctxAgent{}
	agent.fn = func(context.Context, string) (string, error) {
		// The second reply arrives after the store broke.
		if len(agent.called()) == 2 {
			require.NoError(t, st.Close())
		}

		return `{"patterns":[]}`, nil
	}

	p := planAll(t, st, nil)

	var log eventLog

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Progress: log.record, Boundary: true})
	require.Error(t, err, "a store failure is the one error Execute returns")
	require.NotNil(t, x)
	assert.Equal(t, StopStoreError, x.Stop)
	assert.Equal(t, err, x.StopErr)

	assert.Equal(t, GroupCompleted, x.Groups[0].Status)
	assert.Equal(t, GroupFailed, x.Groups[1].Status)
	assert.Equal(t, FailStore, x.Groups[1].Failure)
	assert.Positive(t, x.Groups[1].PromptChars, "the paid call is metered")
	assert.Equal(t, GroupUnattempted, x.Groups[2].Status)
	assert.Equal(t, 1, x.Result.GroupsRead)
	assert.Zero(t, x.Result.GroupsFailed, "a store failure is not an agent failure")

	last := log.events[len(log.events)-1]
	assert.Equal(t, EventStopped, last.Kind)
	assert.Equal(t, 2, last.Index, "the stop names the first group that did not run")
	assert.Equal(t, StopStoreError, last.Stop)

	reopened, err := store.Open(path)
	require.NoError(t, err)

	defer func() { _ = reopened.Close() }()

	marks, err := reopened.DistilledSignatures()
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{x.Groups[0].Plan.Signature: true}, marks, "the earlier group stays recorded")
}

func TestExecuteStopsBeforeTheNextGroup(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b", "c"))
	agent := &ctxAgent{fn: emptyReply}
	p := planAll(t, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var log eventLog

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true, Progress: func(ev Event) {
		log.record(ev)

		if ev.Kind == EventGroupFinished && ev.Index == 0 {
			cancel()
		}
	}})
	require.NoError(t, err, "a stop is data, not an error")

	assert.Len(t, agent.called(), 1, "no call starts after the stop")
	assert.Equal(t, StopCancelled, x.Stop)
	require.ErrorIs(t, x.StopErr, context.Canceled)
	assert.Equal(t, GroupCompleted, x.Groups[0].Status)

	for _, o := range x.Groups[1:] {
		assert.Equal(t, GroupUnattempted, o.Status)
		assert.Zero(t, o.PromptChars, "an unattempted group costs nothing")
		assert.True(t, o.Started.IsZero())
	}

	assert.Equal(t, 1, x.Result.GroupsRead)
	assert.Equal(t, 2, x.Result.GroupsPending)

	last := log.events[len(log.events)-1]
	assert.Equal(t, EventStopped, last.Kind)
	assert.Equal(t, 1, last.Index)

	marks, err := st.DistilledSignatures()
	require.NoError(t, err)
	assert.Len(t, marks, 1, "the committed group stays")
}

func TestExecuteStopDuringACallCancelsThatGroup(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b", "c"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := &ctxAgent{}
	agent.fn = func(callCtx context.Context, _ string) (string, error) {
		if len(agent.called()) == 1 {
			return `{"patterns":[]}`, nil
		}

		// The operator stops while the second call runs.
		cancel()
		<-callCtx.Done()

		return "", callCtx.Err()
	}

	p := planAll(t, st, nil)

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true})
	require.NoError(t, err)

	assert.Len(t, agent.called(), 2)
	assert.Equal(t, StopCancelled, x.Stop)
	assert.Equal(t, GroupCompleted, x.Groups[0].Status)
	assert.Equal(t, GroupCancelled, x.Groups[1].Status, "a stopped call is cancelled, not failed")
	assert.Equal(t, FailNone, x.Groups[1].Failure)
	assert.Positive(t, x.Groups[1].PromptChars, "the process could have started")
	assert.Equal(t, GroupUnattempted, x.Groups[2].Status)
	assert.Zero(t, x.Result.GroupsFailed)
	assert.Equal(t, 2, x.Result.GroupsPending, "cancelled and unattempted groups stay eligible")

	marks, err := st.DistilledSignatures()
	require.NoError(t, err)
	assert.Len(t, marks, 1, "the cancelled group is not marked")
}

func TestExecuteKeepsAReplyThatArrivedBeforeTheStop(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := &ctxAgent{fn: func(_ context.Context, prompt string) (string, error) {
		// The reply is complete; the stop arrives just after it.
		cancel()

		id := firstID(prompt)

		return patternReply("late-rule", "Keep the late reply.", id, id+1), nil
	}}

	p := planAll(t, st, nil)

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true})
	require.NoError(t, err)

	assert.Len(t, agent.called(), 1)
	assert.Equal(t, StopCancelled, x.Stop)
	assert.Equal(t, GroupCompleted, x.Groups[0].Status, "a finished reply passes the commit boundary")
	require.Len(t, x.Groups[0].Proposals, 1)
	assert.Equal(t, GroupUnattempted, x.Groups[1].Status)
	assert.Equal(t, 1, x.Result.GroupsRead)
	assert.Equal(t, 1, x.Result.GroupsPending, "the late group is never counted twice")

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "late-rule", pending[0].Rule)
}

func TestExecuteStartsNothingAfterAnEarlyStop(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{21, 22}, Status: model.ProposalProposed,
	}))

	p := planAll(t, st, nil)
	require.Len(t, p.Cleanup, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agent := &ctxAgent{fn: emptyReply}

	var log eventLog

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true, Progress: log.record})
	require.NoError(t, err)

	assert.Empty(t, agent.called())
	assert.Equal(t, StopCancelled, x.Stop)
	assert.Zero(t, x.Removed, "the cleanup waits for a live context too")
	assert.Equal(t, []EventKind{EventStopped}, log.kinds())
	assert.Zero(t, log.events[0].Index)

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
}

func TestExecuteStopDuringTheStartEventStartsNoCall(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))
	p := planAll(t, st, nil)
	agent := &ctxAgent{fn: emptyReply}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Progress: func(ev Event) {
		if ev.Kind == EventGroupStarted {
			cancel()
		}
	}})
	require.NoError(t, err)

	assert.Empty(t, agent.called())
	assert.Equal(t, GroupCancelled, x.Groups[0].Status)
	assert.Zero(t, x.Groups[0].PromptChars, "no process could have started")
	assert.Equal(t, GroupUnattempted, x.Groups[1].Status)
	assert.Zero(t, x.Result.PromptChars)
}

// A stop that cancels a group must stop the run wherever the group is.
// The last planned group has no next check, so the regression case is
// a stop in the last or only group.
func TestExecuteStopInAnyGroupStopsTheRun(t *testing.T) {
	cases := []struct {
		name   string
		dirs   []string
		stopAt int
		// inCall stops during the agent call; otherwise the stop comes
		// in the EventGroupStarted receiver, before the call.
		inCall bool
	}{
		{"only group, during the call", []string{"a"}, 0, true},
		{"only group, during the start event", []string{"a"}, 0, false},
		{"last of two, during the call", []string{"a", "b"}, 1, true},
		{"last of two, during the start event", []string{"a", "b"}, 1, false},
		{"first of two, during the call", []string{"a", "b"}, 0, true},
		{"first of two, during the start event", []string{"a", "b"}, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openSeeded(t, areaFindings(tc.dirs...))
			p := planAll(t, st, nil)
			require.Len(t, p.Groups, len(tc.dirs))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			agent := &ctxAgent{}
			agent.fn = func(callCtx context.Context, _ string) (string, error) {
				if !tc.inCall || len(agent.called()) <= tc.stopAt {
					return `{"patterns":[]}`, nil
				}

				cancel()
				<-callCtx.Done()

				return "", callCtx.Err()
			}

			var log eventLog

			x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true, Progress: func(ev Event) {
				log.record(ev)

				if !tc.inCall && ev.Kind == EventGroupStarted && ev.Index == tc.stopAt {
					cancel()
				}
			}})
			require.NoError(t, err, "a stop is data, not an error")

			assert.Equal(t, StopCancelled, x.Stop)
			require.ErrorIs(t, x.StopErr, context.Canceled)

			for i, o := range x.Groups {
				switch {
				case i < tc.stopAt:
					assert.Equal(t, GroupCompleted, o.Status, "group %d", i)
				case i == tc.stopAt:
					assert.Equal(t, GroupCancelled, o.Status, "group %d", i)
				default:
					assert.Equal(t, GroupUnattempted, o.Status, "group %d", i)
				}
			}

			wantCalls := tc.stopAt
			if tc.inCall {
				wantCalls++
			}

			assert.Len(t, agent.called(), wantCalls, "no call starts after the stop")

			// Exactly one stop event ends the stream, and it names the
			// first group that did not run.
			stops := 0

			for _, ev := range log.events {
				if ev.Kind == EventStopped {
					stops++
				}
			}

			assert.Equal(t, 1, stops)

			last := log.events[len(log.events)-1]
			assert.Equal(t, EventStopped, last.Kind)
			assert.Equal(t, tc.stopAt+1, last.Index)
			assert.Equal(t, StopCancelled, last.Stop)
			require.ErrorIs(t, last.Err, context.Canceled)

			marks, err := st.DistilledSignatures()
			require.NoError(t, err)
			assert.Len(t, marks, tc.stopAt, "the cancelled group is not marked")
		})
	}
}

// A reply that completes the last group before the stop is observed
// leaves no work that the stop prevented, so the run is not stopped.
func TestExecuteLateReplyOnTheLastGroupIsNotAStop(t *testing.T) {
	st := openSeeded(t, areaFindings("a"))
	p := planAll(t, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := &ctxAgent{fn: func(_ context.Context, prompt string) (string, error) {
		cancel()

		id := firstID(prompt)

		return patternReply("late-rule", "Keep the late reply.", id, id+1), nil
	}}

	var log eventLog

	x, err := Execute(ctx, st, agent, p, ExecuteOptions{Boundary: true, Progress: log.record})
	require.NoError(t, err)

	assert.Equal(t, StopNone, x.Stop)
	require.NoError(t, x.StopErr)
	assert.Equal(t, GroupCompleted, x.Groups[0].Status, "the reply passed the commit boundary")
	assert.Equal(t, []EventKind{EventGroupStarted, EventGroupFinished}, log.kinds())
	assert.Equal(t, 1, x.Result.GroupsRead)
	assert.Zero(t, x.Result.GroupsPending, "the late group is never counted twice")
}

// The start checks can be slow, so a stop during them must still keep
// the durable cleanup from running.
func TestExecuteStopDuringTheStartChecksSkipsTheCleanup(t *testing.T) {
	cases := []struct {
		name string
		dirs []string
	}{
		{"with groups", []string{"a", "b"}},
		{"cleanup only", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openSeeded(t, areaFindings(tc.dirs...))
			require.NoError(t, st.InsertProposal(&model.Proposal{
				Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: model.ProposalProposed,
			}))

			p := planAll(t, st, nil)
			require.Len(t, p.Cleanup, 1)
			require.Len(t, p.Groups, len(tc.dirs))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			agent := &ctxAgent{fn: emptyReply}

			var log eventLog

			x, err := Execute(ctx, st, agent, p, ExecuteOptions{
				Boundary: true,
				Progress: log.record,
				Revalidate: func() error {
					// The operator stops while the caller's checks run.
					cancel()

					return nil
				},
			})
			require.NoError(t, err)

			assert.Empty(t, agent.called())
			assert.Equal(t, StopCancelled, x.Stop)
			require.ErrorIs(t, x.StopErr, context.Canceled)
			assert.Zero(t, x.Removed, "the cleanup does not start after the stop")
			assert.Equal(t, []EventKind{EventStopped}, log.kinds())
			assert.Zero(t, log.events[0].Index)

			pending, err := st.Proposals(model.ProposalProposed)
			require.NoError(t, err)
			require.Len(t, pending, 1, "the stale row stays for another run")
			assert.Equal(t, "stale", pending[0].Rule)
		})
	}
}

func TestExecuteStopsWhenAnotherWriterRecordsTheNextGroup(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b", "c"))
	p := planAll(t, st, nil)

	agent := &ctxAgent{fn: func(context.Context, string) (string, error) {
		// Another process distills the second planned group meanwhile.
		require.NoError(t, st.MarkDistilled(p.Groups[1].Group.Signature, "", 1))

		return `{"patterns":[]}`, nil
	}}

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Boundary: true})
	require.NoError(t, err)

	assert.Len(t, agent.called(), 1)
	assert.Equal(t, StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, ErrStale)
	assert.Contains(t, x.StopErr.Error(), "distilled by another writer")
	assert.Equal(t, GroupCompleted, x.Groups[0].Status)
	assert.Equal(t, GroupSkipped, x.Groups[1].Status)
	assert.Equal(t, GroupUnattempted, x.Groups[2].Status)
	assert.Equal(t, 1, x.Result.GroupsSkipped)
}

func TestExecuteStopsWhenTheNextGroupsFindingsChange(t *testing.T) {
	findings := areaFindings("a", "b")
	st := openSeeded(t, findings)
	p := planAll(t, st, nil)

	second := p.Groups[1].Group.Findings[0].ID

	agent := &ctxAgent{fn: func(context.Context, string) (string, error) {
		edited := append([]model.Finding(nil), findings...)

		for i := range edited {
			if edited[i].ID == second {
				edited[i].Body += " Edited meanwhile."
			}
		}

		require.NoError(t, st.ReplaceLessons(nil, edited))

		return `{"patterns":[]}`, nil
	}}

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Boundary: true})
	require.NoError(t, err)

	assert.Len(t, agent.called(), 1)
	assert.Equal(t, StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, ErrStale)
	assert.Contains(t, x.StopErr.Error(), "findings of group")
	assert.Equal(t, GroupUnattempted, x.Groups[1].Status, "changed evidence is not sent")
}

func TestExecuteRefusesAGroupRecordedDuringItsOwnCall(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))
	p := planAll(t, st, nil)

	agent := &ctxAgent{fn: func(_ context.Context, prompt string) (string, error) {
		require.NoError(t, st.MarkDistilled(p.Groups[0].Group.Signature, "", 1))

		id := firstID(prompt)

		return patternReply("raced-rule", "Keep the raced reply.", id, id+1), nil
	}}

	var log eventLog

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Boundary: true, Progress: log.record})
	require.NoError(t, err)

	assert.Equal(t, StopConflict, x.Stop)
	assert.Equal(t, GroupSkipped, x.Groups[0].Status)
	require.ErrorIs(t, x.Groups[0].Err, ErrStale)
	assert.Empty(t, x.Groups[0].Proposals)
	assert.Equal(t, GroupUnattempted, x.Groups[1].Status)
	assert.Equal(t, []EventKind{EventGroupStarted, EventGroupFinished, EventStopped}, log.kinds())
	assert.Equal(t, 1, log.events[2].Index)

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Empty(t, pending, "the guarded save stores no duplicate proposals")
}

func TestExecuteStopsOnACallerConflict(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))
	p := planAll(t, st, nil)
	agent := &ctxAgent{fn: emptyReply}

	changed := errors.New("config.yaml changed")
	checks := 0

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Revalidate: func() error {
		checks++

		// The first two checks guard the cleanup and group one.
		if checks > 2 {
			return changed
		}

		return nil
	}})
	require.NoError(t, err)

	assert.Len(t, agent.called(), 1)
	assert.Equal(t, StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, changed)
	assert.Equal(t, GroupUnattempted, x.Groups[1].Status)
}

func TestExecuteRejectsAStalePlanBeforeAnyWrite(t *testing.T) {
	st := openSeeded(t, areaFindings("a"))
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{21, 22}, Status: model.ProposalProposed,
	}))

	p := planAll(t, st, nil)

	// A decision after the plan changes the ledger.
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "other", Rule: "decided", Note: "n", Members: []int64{90, 91}, Status: model.ProposalDismissed,
	}))

	agent := &ctxAgent{fn: emptyReply}

	x, err := Execute(context.Background(), st, agent, p, ExecuteOptions{Boundary: true})
	require.NoError(t, err)

	assert.Empty(t, agent.called())
	assert.Equal(t, StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, ErrStale)
	assert.Zero(t, x.Removed)

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "the planned cleanup did not run")
}

func TestExecuteCleanupDeletesOnlyPlannedRows(t *testing.T) {
	st := openSeeded(t, nil)
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "planned", Note: "n", Members: []int64{1, 2}, Status: model.ProposalProposed,
	}))

	p := planAll(t, st, nil)
	require.Empty(t, p.Groups)
	require.Len(t, p.Cleanup, 1)

	// Another writer adds a row with a stale signature after the plan.
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone-too", Rule: "later", Note: "n", Members: []int64{3, 4}, Status: model.ProposalProposed,
	}))

	var log eventLog

	// A cleanup-only plan needs no invoker.
	x, err := Execute(context.Background(), st, nil, p, ExecuteOptions{Progress: log.record})
	require.NoError(t, err)

	assert.Equal(t, []int64{p.Cleanup[0].ID}, x.CleanupIDs)
	assert.Equal(t, 1, x.Removed)
	assert.Equal(t, 1, x.Result.PrunedStale)
	assert.Equal(t, []EventKind{EventCleanupDone}, log.kinds())
	assert.Equal(t, 1, log.events[0].Removed)

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "later", pending[0].Rule, "a row outside the plan survives")
}

func TestExecuteNeedsAPlanAndAnInvoker(t *testing.T) {
	st := openSeeded(t, areaFindings("a"))
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: model.ProposalProposed,
	}))

	x, err := Execute(context.Background(), st, nil, nil, ExecuteOptions{})
	require.Error(t, err)
	require.NotNil(t, x, "the execution is never nil")

	p := planAll(t, st, nil)

	x, err = Execute(context.Background(), st, &ctxAgent{fn: emptyReply}, &Plan{Groups: p.Groups}, ExecuteOptions{})
	require.Error(t, err, "a hand-built plan without its preflight is refused")
	require.NotNil(t, x)

	x, err = Execute(context.Background(), st, nil, p, ExecuteOptions{})
	require.ErrorIs(t, err, ErrNoInvoker)
	require.NotNil(t, x)
	assert.Zero(t, x.Removed, "nothing runs without an invoker")
	assert.Equal(t, GroupUnattempted, x.Groups[0].Status)
}

func TestExecuteNeverTeachesThePlan(t *testing.T) {
	st := openSeeded(t, areaFindings("a", "b"))
	p := planAll(t, st, nil)
	before := len(p.known.entries)

	agent := &ctxAgent{fn: func(_ context.Context, prompt string) (string, error) {
		id := firstID(prompt)

		return patternReply("rule-"+string(rune('a'+id%26)), "Keep this guidance.", id, id+1), nil
	}}

	_, err := Execute(context.Background(), st, agent, p, ExecuteOptions{})
	require.NoError(t, err)
	assert.Len(t, p.known.entries, before, "the run learns into a clone")
}

func TestEnumNamesAreStable(t *testing.T) {
	assert.Equal(t, []string{"unattempted", "completed", "failed", "cancelled", "skipped", "GroupStatus(9)"},
		[]string{GroupUnattempted.String(), GroupCompleted.String(), GroupFailed.String(),
			GroupCancelled.String(), GroupSkipped.String(), GroupStatus(9).String()})
	assert.Equal(t, []string{"none", "agent", "timeout", "invalid reply", "store", "FailureKind(9)"},
		[]string{FailNone.String(), FailAgent.String(), FailTimeout.String(),
			FailInvalidReply.String(), FailStore.String(), FailureKind(9).String()})
	assert.Equal(t, []string{"cleanup done", "group started", "duplicate", "group finished", "stopped", "EventKind(9)"},
		[]string{EventCleanupDone.String(), EventGroupStarted.String(), EventDuplicate.String(),
			EventGroupFinished.String(), EventStopped.String(), EventKind(9).String()})
	assert.Equal(t, []string{"none", "cancelled", "conflict", "store error", "StopReason(9)"},
		[]string{StopNone.String(), StopCancelled.String(), StopConflict.String(),
			StopStoreError.String(), StopReason(9).String()})
}
