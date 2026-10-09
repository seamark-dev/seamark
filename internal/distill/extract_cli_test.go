package distill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// These tests cover ExtractTriggers, the CLI path of the shared executor.
// The CLI goldens pin its output. These tests pin the executor semantics
// and the adapter lines behind that output. The CLI keeps its wider
// selection until a separate decision. It runs the store checks and the
// guarded save of the console.

// cliRun is one ExtractTriggers run with its captured log lines.
type cliRun struct {
	res  *ExtractResult
	err  error
	logs []string
}

// extractCLI runs ExtractTriggers over the given rows as the CLI does. It
// reads the rows and their finding metadata from the store, as the CLI
// selection does. So the run sees the same values as its checks.
func extractCLI(t *testing.T, w *extractWorkspace, inv *extractAgent, ids ...int64) cliRun {
	t.Helper()

	ps := make([]model.Proposal, len(ids))
	for i, id := range ids {
		ps[i] = w.row(t, id)
	}

	meta, err := w.st.FindingsMetaByIDs(memberIDs(ps))
	require.NoError(t, err)

	var run cliRun

	run.res, run.err = ExtractTriggers(context.Background(), w.st, inv, ps, meta, ExtractOptions{
		Root: w.root,
		Logf: func(format string, args ...any) {
			line := extractSeconds.ReplaceAllString(fmt.Sprintf(format, args...), "${1}<dur>,")
			run.logs = append(run.logs, line)
		},
	})

	return run
}

// failStamps makes every write of a trigger stamp fail, as a store
// failure during the guarded save would.
func failStamps(t *testing.T, w *extractWorkspace) {
	t.Helper()

	w.exec(t, `CREATE TRIGGER fail_stamp BEFORE UPDATE OF trigger_checked_at ON proposal
		BEGIN SELECT RAISE(ABORT, 'injected stamp failure'); END`)
}

func TestExtractTriggersLeavesARowAppliedDuringTheCallUnchanged(t *testing.T) {
	// A0 defect D14: the CLI used to stamp a row that another command
	// applied during the agent call. The guarded save now refuses it, and
	// the adapter names the row.
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)

	inv := &extractAgent{fn: func(context.Context, int, string) (string, error) {
		_, err := w.st.SetProposalStatus([]int64{p.ID}, model.ProposalApplied)
		require.NoError(t, err)

		return triggerReply(answerFor(p, "api/schemas.py")), nil
	}}

	run := extractCLI(t, w, inv, p.ID)
	require.NoError(t, run.err, "a conflict is data, not an error")

	assert.Equal(t, []string{
		"extracting batch 1/1 — 1 proposal(s)",
		"  <dur>, 1 of 1 named trigger paths",
		fmt.Sprintf("conflict: p%d changed during the agent call — its answer was not saved and p%d is left unchanged",
			p.ID, p.ID),
	}, run.logs, "a conflict in the last batch leaves nothing unsent, so no stop line follows")

	assert.Equal(t, ExtractResult{Examined: 1, Named: 1, Conflicts: 1,
		PromptChars: run.res.PromptChars, ReplyChars: run.res.ReplyChars, Duration: run.res.Duration}, *run.res)

	row := w.row(t, p.ID)
	assert.Equal(t, model.ProposalApplied, row.Status)
	assert.Equal(t, "web/src/api", row.Region, "the installed identity is untouched")
	assert.Empty(t, row.Regions)
	assert.Empty(t, row.TriggerPaths, "the paid answer is not written")
	assert.Zero(t, row.TriggerChecked, "the question stays open for the next run")
}

func TestExtractTriggersStopsOnARowChangedBeforeTheCall(t *testing.T) {
	// The CLI reads its rows, and another writer edits one before the run
	// starts. The start check refuses the changed row, so nothing is sent.
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)

	ps := []model.Proposal{w.row(t, p.ID)}

	meta, err := w.st.FindingsMetaByIDs(memberIDs(ps))
	require.NoError(t, err)

	w.exec(t, `UPDATE proposal SET note = 'edited' WHERE id = ?`, p.ID)

	inv := replyWith(triggerReply(answerFor(p, "api/schemas.py")))

	var logs []string

	res, err := ExtractTriggers(context.Background(), w.st, inv, ps, meta, ExtractOptions{
		Root: w.root,
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	})
	require.NoError(t, err)

	assert.Empty(t, inv.prompts(), "no call starts after the conflict")
	assert.Equal(t, []string{
		fmt.Sprintf("extraction stopped: p%d changed — 1 proposal(s) not sent and left unchanged; re-running resumes", p.ID),
	}, logs)
	assert.Equal(t, ExtractResult{Unattempted: 1, Duration: res.Duration}, *res)
	assert.False(t, stamped(t, w, p.ID))
}

func TestExtractTriggersStopsAfterAConflictDuringACall(t *testing.T) {
	// A row of the first batch is dismissed during its call. The rest of
	// the batch is stored, and the run stops before the second batch,
	// because another writer is active (D-20).
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	order := w.plan(t, ids...)
	require.Len(t, order.Batches, 2)

	target := order.Batches[0].Proposals[0]
	answers := negativeForAll(order)
	inv := &extractAgent{fn: func(ctx context.Context, call int, prompt string) (string, error) {
		if call == 1 {
			_, err := w.st.SetProposalStatus([]int64{target.ID}, model.ProposalDismissed)
			require.NoError(t, err)
		}

		return answers.fn(ctx, call, prompt)
	}}

	run := extractCLI(t, w, inv, order.IDs()...)
	require.NoError(t, run.err)

	assert.Len(t, inv.prompts(), 1)
	assert.Equal(t, []string{
		"extracting batch 1/2 — 10 proposal(s)",
		"  <dur>, 0 of 10 named trigger paths",
		fmt.Sprintf("conflict: p%d changed during the agent call — its answer was not saved and p%d is left unchanged",
			target.ID, target.ID),
		"extraction stopped: another writer is active — 2 proposal(s) not sent and left unchanged; re-running resumes",
	}, run.logs)

	assert.Equal(t, 10, run.res.Examined)
	assert.Equal(t, 1, run.res.Conflicts)
	assert.Equal(t, 2, run.res.Unattempted)
	assert.False(t, stamped(t, w, target.ID))

	for _, p := range order.Batches[0].Proposals[1:] {
		assert.True(t, stamped(t, w, p.ID), "p%d of the same batch is stored", p.ID)
	}

	for _, p := range order.Batches[1].Proposals {
		assert.False(t, stamped(t, w, p.ID), "p%d is not sent", p.ID)
	}
}

func TestExtractTriggersStopsBeforeABatchWhoseRowChanged(t *testing.T) {
	// A row of the second batch is applied during the first call. The
	// check before the second batch names it, and nothing more is sent.
	w := newExtractWorkspace(t)
	ids := manyPending(t, w, 12)
	order := w.plan(t, ids...)
	require.Len(t, order.Batches, 2)

	later := order.Batches[1].Proposals[0]
	answers := negativeForAll(order)
	inv := &extractAgent{fn: func(ctx context.Context, call int, prompt string) (string, error) {
		_, err := w.st.SetProposalStatus([]int64{later.ID}, model.ProposalApplied)
		require.NoError(t, err)

		return answers.fn(ctx, call, prompt)
	}}

	run := extractCLI(t, w, inv, order.IDs()...)
	require.NoError(t, run.err)

	assert.Len(t, inv.prompts(), 1)
	assert.Equal(t, []string{
		"extracting batch 1/2 — 10 proposal(s)",
		"  <dur>, 0 of 10 named trigger paths",
		fmt.Sprintf("extraction stopped: p%d is applied now — 2 proposal(s) not sent and left unchanged; "+
			"re-running resumes", later.ID),
	}, run.logs)

	assert.Equal(t, 10, run.res.Examined)
	assert.Zero(t, run.res.Conflicts, "no answer was refused")
	assert.Equal(t, 2, run.res.Unattempted)
	assert.False(t, stamped(t, w, later.ID))
}

func TestExtractTriggersWritesNoRegionsWhenTheSaveFails(t *testing.T) {
	// One transaction holds the paths, the stamp and a pending region
	// change. A failed save therefore writes none of them. Before, the
	// CLI wrote the regions first, so a failed stamp kept new regions.
	w := newExtractWorkspace(t)
	older := w.pending(t, "older", 1)
	newer := w.pending(t, "newer", 1)

	failStamps(t, w)

	inv := replyWith(triggerReply(answerFor(newer, "api/schemas.py"), answerFor(older, "api/schemas.py")))

	run := extractCLI(t, w, inv, newer.ID, older.ID)
	require.Error(t, run.err)
	assert.True(t, strings.HasPrefix(run.err.Error(), fmt.Sprintf("store: save trigger answer of p%d: ", newer.ID)),
		"the error names the guarded save: %s", run.err)
	assert.Contains(t, run.err.Error(), "injected stamp failure")

	assert.Equal(t, []string{
		"extracting batch 1/1 — 2 proposal(s)",
		"  <dur>, 2 of 2 named trigger paths",
	}, run.logs, "the whole reply counts, and the store failure adds no conflict line")

	for _, id := range []int64{newer.ID, older.ID} {
		row := w.row(t, id)
		assert.Equal(t, "web/src/api", row.Region, "p%d keeps its regions", id)
		assert.Empty(t, row.Regions)
		assert.Zero(t, row.TriggerChecked, "the question of p%d stays open", id)
	}

	assert.Equal(t, ExtractResult{Examined: 2, Named: 1,
		PromptChars: run.res.PromptChars, ReplyChars: run.res.ReplyChars}, *run.res)
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
	assert.Contains(t, err.Error(), fmt.Sprintf("store: save trigger answer of p%d: ", newer.ID))

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
	// console until a separate decision. A row whose evidence aged out is
	// asked and stamped (A0 defect D5), and the caller's order is the send
	// order. The row checks pass, because the rows and the metadata come
	// from the store.
	w := newExtractWorkspace(t)
	agedOut := w.pending(t, "aged-out", 99)
	pending := w.pending(t, "schema-sync", 1)
	installed := w.applied(t, "schema-sync-applied", 1)

	inv := replyWith(`{"triggers": []}`)

	run := extractCLI(t, w, inv, agedOut.ID, installed.ID, pending.ID)
	require.NoError(t, run.err)
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

	for _, id := range []int64{agedOut.ID, installed.ID, pending.ID} {
		assert.True(t, stamped(t, w, id), "p%d is stamped", id)
	}

	assert.Equal(t, 3, run.res.Examined)
	assert.Zero(t, run.res.Unattempted)
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

func TestExtractStaleCauseDropsTheSentinelText(t *testing.T) {
	stale := fmt.Errorf("%w: p12 is applied now; the living evidence of p13 changed", ErrExtractStale)

	assert.Equal(t, "p12 is applied now; the living evidence of p13 changed", extractStaleCause(stale))
	assert.Equal(t, "store: read proposals by id: boom", extractStaleCause(errors.New("store: read proposals by id: boom")),
		"another error keeps its whole text")
	assert.Equal(t, ErrExtractStale.Error(), extractStaleCause(ErrExtractStale), "a bare sentinel names no change")
	assert.Equal(t, "an input changed", extractStaleCause(nil))
}
