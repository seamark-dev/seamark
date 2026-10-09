package distill

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// These tests cover ExecuteExtract with Boundary off, the path that
// ExtractTriggers runs for the CLI. The CLI goldens pin its output; these
// tests pin the executor semantics that keep that output (D-1).

// executeUnguarded runs plan with Boundary off and a fixed clock, as
// ExtractTriggers does, and records the events.
func executeUnguarded(w *extractWorkspace, inv *extractAgent, plan *ExtractPlan) (*ExtractExecution, *extractEvents, error) {
	log := &extractEvents{}

	x, err := ExecuteExtract(context.Background(), w.st, inv, plan, ExtractExecuteOptions{
		OnEvent: log.record, Now: fixedClock(stampTime),
	})

	return x, log, err
}

// failStamps makes every write of a trigger stamp fail, as a store
// failure between the two CLI writes would.
func failStamps(t *testing.T, w *extractWorkspace) {
	t.Helper()

	w.exec(t, `CREATE TRIGGER fail_stamp BEFORE UPDATE OF trigger_checked_at ON proposal
		BEGIN SELECT RAISE(ABORT, 'injected stamp failure'); END`)
}

func TestExecuteExtractWithoutBoundarySkipsTheStoreChecks(t *testing.T) {
	// The note changes after planning. With Boundary on, the run stops
	// before the call. Without it, the CLI never compared rows, so the
	// batch runs and the answer is stored.
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	plan := w.plan(t, p.ID)

	w.exec(t, `UPDATE proposal SET note = 'edited' WHERE id = ?`, p.ID)

	guarded := replyWith(triggerReply(answerFor(p, "api/schemas.py")))
	x, _, err := execute(context.Background(), w, guarded, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, StopConflict, x.Stop, "the console refuses a changed row")
	assert.Empty(t, guarded.prompts())

	inv := replyWith(triggerReply(answerFor(p, "api/schemas.py")))
	x, log, err := executeUnguarded(w, inv, plan)
	require.NoError(t, err)

	assert.Equal(t, StopNone, x.Stop)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone}, log.kinds())
	assert.Equal(t, []string{plan.Batches[0].Prompt}, inv.prompts())
	assert.Equal(t, ExtractStored, x.Batches[0].Items[0].Status)

	row := w.row(t, p.ID)
	assert.Equal(t, []string{"api/schemas.py"}, row.TriggerPaths)
	assert.Equal(t, []string{"api/schemas.py"}, row.Regions, "a pending row narrows in the same run")
	assert.Equal(t, stampTime.Unix(), row.TriggerChecked)
}

func TestExecuteExtractWithoutBoundaryStampsARowAppliedDuringTheCall(t *testing.T) {
	// A0 defect D14, kept by D-1: the region write checks the status, so
	// the applied scope stays. The stamp has no guard, so the answer lands
	// on the applied row. The counters read the status at planning time.
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	plan := w.plan(t, p.ID)

	inv := &extractAgent{fn: func(context.Context, int, string) (string, error) {
		_, err := w.st.SetProposalStatus([]int64{p.ID}, model.ProposalApplied)
		require.NoError(t, err)

		return triggerReply(answerFor(p, "api/schemas.py")), nil
	}}

	x, _, err := executeUnguarded(w, inv, plan)
	require.NoError(t, err)

	assert.Equal(t, StopNone, x.Stop, "no answer is a conflict without Boundary")
	assert.Empty(t, x.Conflicts)

	item := x.Batches[0].Items[0]
	assert.Equal(t, ExtractStored, item.Status)
	assert.False(t, item.RegionsChanged, "the status check refuses the region write")
	assert.Equal(t, item.RegionsBefore, item.RegionsAfter)

	row := w.row(t, p.ID)
	assert.Equal(t, model.ProposalApplied, row.Status)
	assert.Equal(t, "web/src/api", row.Region, "the installed identity is untouched")
	assert.Empty(t, row.Regions)
	assert.Equal(t, []string{"api/schemas.py"}, row.TriggerPaths, "the unguarded stamp lands")

	assert.Equal(t, 1, x.Result.Stored)
	assert.Zero(t, x.Result.Retargeted)
	assert.Zero(t, x.Result.AppliedStored, "the row counts by its planned status")
}

func TestExecuteExtractWithoutBoundaryRecomputesOnlyPendingRows(t *testing.T) {
	// The CLI loop recomputed regions for a pending row only. An applied
	// row then makes no history read, so it gets no facts.
	w := newExtractWorkspace(t)
	pending := w.pending(t, "schema-sync", 1)
	installed := w.applied(t, "schema-sync-applied", 1)
	plan := w.plan(t, pending.ID, installed.ID)

	inv := replyWith(triggerReply(answerFor(pending, "api/schemas.py"), answerFor(installed, "api/schemas.py")))

	x, _, err := executeUnguarded(w, inv, plan)
	require.NoError(t, err)

	byID := map[int64]ExtractItemOutcome{}
	for _, item := range x.Batches[0].Items {
		byID[item.Proposal.ID] = item
	}

	assert.Len(t, byID[pending.ID].Facts, 1)
	assert.Empty(t, byID[installed.ID].Facts)
	assert.Equal(t, ExtractStored, byID[installed.ID].Status)
	assert.Equal(t, []string{"api/schemas.py"}, w.row(t, installed.ID).TriggerPaths)
	assert.Equal(t, ExtractResult{Examined: 2, Named: 2, Stored: 2, Retargeted: 1, AppliedStored: 1,
		PromptChars: x.Result.PromptChars, ReplyChars: x.Result.ReplyChars}, x.Result)
}

func TestExecuteExtractWithoutBoundaryKeepsTheRegionsOfAFailedStamp(t *testing.T) {
	// The CLI writes the pending regions first and the stamp second, in
	// two statements. A failed stamp keeps the new regions and leaves the
	// question open, so the next run finishes the pair. The batch still
	// counts every named item of the reply, as the CLI line did.
	w := newExtractWorkspace(t)
	older := w.pending(t, "older", 1)
	newer := w.pending(t, "newer", 1)
	plan := w.plan(t, older.ID, newer.ID)
	require.Equal(t, []int64{newer.ID, older.ID}, plan.IDs())

	failStamps(t, w)

	inv := replyWith(triggerReply(answerFor(newer, "api/schemas.py"), answerFor(older, "api/schemas.py")))

	x, log, err := executeUnguarded(w, inv, plan)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprintf("store: update proposal %d trigger paths: ", newer.ID)),
		"the CLI error text is kept: %s", err)
	assert.Contains(t, err.Error(), "injected stamp failure")

	assert.Equal(t, StopStoreError, x.Stop)
	assert.Equal(t, []ExtractEventKind{ExtractBatchStarted, ExtractBatchDone, ExtractStopped}, log.kinds())

	b := x.Batches[0]
	assert.Equal(t, BatchFailed, b.Status)
	assert.Equal(t, FailStore, b.Failure)
	assert.Equal(t, 2, b.Named, "the whole reply counts, also the item after the failure")

	first := b.Items[0]
	assert.Equal(t, ExtractFailed, first.Status)
	assert.True(t, first.RegionsChanged, "the first write landed")

	row := w.row(t, newer.ID)
	assert.Equal(t, []string{"api/schemas.py"}, row.Regions)
	assert.Zero(t, row.TriggerChecked, "the question stays open")

	row = w.row(t, older.ID)
	assert.Equal(t, "web/src/api", row.Region, "an item after the failure is not written")
	assert.Empty(t, row.Regions)
	assert.Zero(t, row.TriggerChecked)

	assert.Equal(t, ExtractResult{Examined: 2, Named: 1, Retargeted: 1,
		PromptChars: b.PromptChars, ReplyChars: b.ReplyChars, Duration: x.Result.Duration}, x.Result)
}

func TestExtractTriggersReportsTheWholeReplyBeforeAStoreFailure(t *testing.T) {
	// The CLI loop printed the batch outcome before its writes. The
	// adapter prints the same line after a store failure. ExtractTriggers
	// returns the partial result together with the error.
	w := newExtractWorkspace(t)
	older := w.pending(t, "older", 1)
	newer := w.pending(t, "newer", 1)
	ps := []model.Proposal{w.row(t, newer.ID), w.row(t, older.ID)}

	meta, err := w.st.FindingsMetaByIDs(memberIDs(ps))
	require.NoError(t, err)

	failStamps(t, w)

	inv := replyWith(triggerReply(answerFor(newer, "api/schemas.py"), answerFor(older, "api/handlers.go")))

	var dones, logs []string

	res, err := ExtractTriggers(context.Background(), w.st, inv, ps, meta, ExtractOptions{
		Root:        w.root,
		OnBatchDone: func(outcome string) { dones = append(dones, extractSeconds.ReplaceAllString(outcome, "<dur>,")) },
		Logf:        func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("store: update proposal %d trigger paths: ", newer.ID))

	assert.Equal(t, []string{"<dur>, 2 of 2 named trigger paths"}, dones)
	assert.Equal(t, []string{"extracting batch 1/1 — 2 proposal(s)"}, logs,
		"OnBatchDone replaces the outcome line only, and the store failure adds no warning")

	require.NotNil(t, res)
	assert.Equal(t, 2, res.Examined)
	assert.Zero(t, res.Stored)
	assert.Zero(t, res.Duration, "a failed run reports no duration")
}

func TestExtractTriggersAsksEveryGivenRowInTheGivenOrder(t *testing.T) {
	// The CLI selects its rows itself and keeps a wider set than the
	// console (D-1). A row whose evidence aged out is asked and stamped
	// (A0 defect D5), and the caller's order is the send order.
	w := newExtractWorkspace(t)
	agedOut := w.pending(t, "aged-out", 99)
	pending := w.pending(t, "schema-sync", 1)
	installed := w.applied(t, "schema-sync-applied", 1)
	ps := []model.Proposal{w.row(t, agedOut.ID), w.row(t, installed.ID), w.row(t, pending.ID)}

	meta, err := w.st.FindingsMetaByIDs(memberIDs(ps))
	require.NoError(t, err)

	inv := replyWith(`{"triggers": []}`)

	res, err := ExtractTriggers(context.Background(), w.st, inv, ps, meta, ExtractOptions{Root: w.root})
	require.NoError(t, err)

	require.Len(t, inv.prompts(), 1)

	var items []string

	for line := range strings.SplitSeq(inv.prompts()[0], "\n") {
		if strings.HasPrefix(line, "--- item ") {
			items = append(items, line)
		}
	}

	assert.Equal(t, []string{
		fmt.Sprintf("--- item id=%d rule=aged-out", agedOut.ID),
		fmt.Sprintf("--- item id=%d rule=schema-sync-applied", installed.ID),
		fmt.Sprintf("--- item id=%d rule=schema-sync", pending.ID),
	}, items)

	for _, p := range ps {
		assert.True(t, stamped(t, w, p.ID), "p%d is stamped", p.ID)
	}

	assert.Equal(t, 3, res.Examined)
}

func TestExtractTriggersRefusesAnEmptyRoot(t *testing.T) {
	// Without a root no named path is verified, so every answer would be
	// stored as a negative. The executor refuses before any call.
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	inv := replyWith(triggerReply(answerFor(p, "api/schemas.py")))

	_, err := ExtractTriggers(context.Background(), w.st, inv, []model.Proposal{p}, nil, ExtractOptions{})
	require.ErrorIs(t, err, ErrExtractRoot)

	assert.Empty(t, inv.prompts())
	assert.False(t, stamped(t, w, p.ID))
}
