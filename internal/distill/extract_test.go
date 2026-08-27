package distill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestExtractTriggersStoresAndRetargetsPending(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	pending := model.Proposal{Signature: "s1", Rule: "schema-sync", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalProposed}
	applied := model.Proposal{Signature: "s2", Rule: "schema-sync-applied", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalApplied}

	require.NoError(t, st.InsertProposal(&pending))
	require.NoError(t, st.InsertProposal(&applied))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	// The ghost path fails the existence rung; item 999 is not in the
	// batch and must be ignored.
	fake := &fakeAgent{fn: func(prompt string) (string, error) {
		return fmt.Sprintf(`{"triggers": [
			{"id": %d, "trigger_paths": ["api/schemas.py", "api/ghost.py"]},
			{"id": %d, "trigger_paths": ["api/schemas.py"]},
			{"id": 999, "trigger_paths": ["api/schemas.py"]}]}`,
			pending.ID, applied.ID), nil
	}}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{pending, applied}, meta, ExtractOptions{Root: root})
	require.NoError(t, err)

	assert.Equal(t, 2, res.Examined)
	assert.Equal(t, 2, res.Named)
	assert.Equal(t, 2, res.Stored)
	assert.Equal(t, 1, res.Retargeted, "only the pending row retargets")
	assert.Equal(t, 1, res.AppliedStored, "the applied-pin hint gates on this, not on arithmetic")
	assert.Zero(t, res.BatchesFailed)

	rows, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	assert.Equal(t, []string{"api/schemas.py"}, rows[0].TriggerPaths,
		"the ghost path must not survive the existence rung")
	assert.Equal(t, []string{"api/schemas.py"}, rows[0].Regions,
		"a pending row adopts the verified trigger scope in the same run")
	assert.Equal(t, "api/schemas.py", rows[0].Region)
	assert.Equal(t, TriggerPromptVersion, rows[0].TriggerPromptVersion)

	rows, err = st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	assert.Equal(t, []string{"api/schemas.py"}, rows[0].TriggerPaths)
	assert.Equal(t, "web/src/api", rows[0].Region,
		"an installed pin's delivery never changes without the user")
	assert.Empty(t, rows[0].Regions)
}

func TestExtractTriggersResumesAfterInterruption(t *testing.T) {
	// The interrupted state: a run died between the region write and
	// the trigger write, so the row carries the trigger scope but no
	// triggers — and the idempotency filter therefore repeats it. The
	// resumed run must store the triggers WITHOUT a second retarget.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	p := model.Proposal{Signature: "s1", Rule: "schema-sync", Region: "api/schemas.py",
		Regions: []string{"api/schemas.py"}, Note: "n", Members: []int64{1},
		Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	fake := &fakeAgent{fn: func(string) (string, error) {
		return fmt.Sprintf(`{"triggers": [{"id": %d, "trigger_paths": ["api/schemas.py"]}]}`, p.ID), nil
	}}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, meta, ExtractOptions{Root: root})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Stored, "the pair completes")
	assert.Zero(t, res.Retargeted, "the trigger scope was already stored — no second write")

	rows, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"api/schemas.py"}, rows[0].TriggerPaths)
	assert.Equal(t, []string{"api/schemas.py"}, rows[0].Regions)
}

func TestExtractTriggersReportsBatchProgress(t *testing.T) {
	// Twelve proposals split into batches of ten and two; each batch
	// announces its start and outcome, so a terminal shows life during
	// the long agent calls.
	root := scopeRoot(t, "api/schemas.py")
	st := scopeStore(t)

	var ps []model.Proposal

	for i := range 12 {
		p := model.Proposal{Signature: fmt.Sprintf("s%d", i), Rule: fmt.Sprintf("r%d", i),
			Note: "n", Members: []int64{1}, Status: model.ProposalProposed}
		require.NoError(t, st.InsertProposal(&p))
		ps = append(ps, p)
	}

	fake := &fakeAgent{fn: func(string) (string, error) {
		return `{"triggers": []}`, nil
	}}

	var starts, dones []string

	_, err := ExtractTriggers(context.Background(), st, fake, ps, nil, ExtractOptions{
		Root:         root,
		OnBatchStart: func(desc string) { starts = append(starts, desc) },
		OnBatchDone:  func(outcome string) { dones = append(dones, outcome) },
	})
	require.NoError(t, err)

	require.Len(t, starts, 2)
	assert.Contains(t, starts[0], "batch 1/2 — 10 proposal(s)")
	assert.Contains(t, starts[1], "batch 2/2 — 2 proposal(s)")

	require.Len(t, dones, 2)
	assert.Contains(t, dones[0], "0 of 10 named trigger paths")
}

func TestExtractTriggersDryRunSendsNothing(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py")
	st := scopeStore(t)

	p := model.Proposal{Signature: "s1", Rule: "r", Note: "n",
		Members: []int64{1}, Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	fake := &fakeAgent{fn: func(string) (string, error) {
		return "", errors.New("must not be called")
	}}

	var pf ExtractPreflight

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, nil, ExtractOptions{
			Root: root, DryRun: true,
			OnPreflight: func(got ExtractPreflight) { pf = got },
		})
	require.NoError(t, err)

	assert.Zero(t, fake.calls, "a dry run must not invoke the agent")
	assert.Zero(t, res.Examined)
	assert.Equal(t, 1, pf.Proposals)
	assert.Equal(t, 1, pf.Batches)
	assert.Positive(t, pf.PromptChars, "the disclosure prices the real prompt")
}

func TestBuildExtractPromptAllowsCitedTriggersAndKeepsExcerptUTF8(t *testing.T) {
	p := model.Proposal{ID: 7, Rule: "sync-generated-client", Note: "Keep both sides synchronized.",
		Members: []int64{1}}
	meta := map[int64]model.Finding{1: {
		ID: 1, Path: "repair/adapter.go",
		Paths: []string{"repair/adapter.go", "api/entry.go"},
		Body:  strings.Repeat("é", extractBodyCap),
	}}

	prompt := buildExtractPrompt([]model.Proposal{p}, meta)
	assert.Contains(t, prompt, "A trigger may be\none of the evidence files")
	assert.NotContains(t, prompt, "ONLY when that place differs")
	assert.Contains(t, prompt, "evidence files: repair/adapter.go, api/entry.go",
		"the re-asked question must expose companion footprint paths it may accept")
	assert.True(t, utf8.ValidString(prompt))
}

func TestBuildExtractPromptBoundsEvidencePathMetadata(t *testing.T) {
	var batch []model.Proposal
	meta := make(map[int64]model.Finding)

	for i := range extractBatch {
		id := int64(i + 1)
		p := model.Proposal{ID: id, Rule: fmt.Sprintf("rule-%d", i), Note: "n", Members: []int64{id}}
		batch = append(batch, p)

		paths := make([]string, 0, 20)
		for j := range 20 {
			paths = append(paths, fmt.Sprintf("generated/very-long-client-path-%02d-%02d-with-metadata.ts", i, j))
		}
		meta[id] = model.Finding{ID: id, Path: fmt.Sprintf("api/primary-%02d.go", i), Paths: paths}
	}

	prompt := buildExtractPrompt(batch, meta)
	pathBytes := 0

	for line := range strings.SplitSeq(prompt, "\n") {
		if files, ok := strings.CutPrefix(line, "    evidence files: "); ok {
			pathBytes += len(files)
		}
	}

	assert.LessOrEqual(t, pathBytes, extractPathBytesPerPrompt,
		"serialized evidence paths share one bounded batch budget")
	assert.NotContains(t, prompt, "with-metadata.ts, generated/very-long-client-path-00-10",
		"one finding cannot contribute an unbounded footprint")
}

func TestExtractTriggersRejectsAnswerlessReplies(t *testing.T) {
	// {} and {"triggers": null} parse as JSON but answer nothing.
	// Stamping rows on them would permanently record answers nobody
	// gave; the batch must fail and retry instead.
	for _, reply := range []string{`{}`, `{"triggers": null}`, `{"trigger": []}`} {
		t.Run(reply, func(t *testing.T) {
			root := scopeRoot(t, "api/schemas.py")
			st := scopeStore(t)

			p := model.Proposal{Signature: "s1", Rule: "r", Note: "n",
				Members: []int64{1}, Status: model.ProposalProposed}
			require.NoError(t, st.InsertProposal(&p))

			fake := &fakeAgent{fn: func(string) (string, error) { return reply, nil }}

			res, err := ExtractTriggers(context.Background(), st, fake,
				[]model.Proposal{p}, nil, ExtractOptions{Root: root})
			require.NoError(t, err)

			assert.Equal(t, 1, res.BatchesFailed)

			rows, err := st.Proposals(model.ProposalProposed)
			require.NoError(t, err)
			assert.Zero(t, rows[0].TriggerChecked, "no stamp — the next run retries")
		})
	}
}

func TestExtractTriggersNegativeAnswerRestoresWidening(t *testing.T) {
	// The orphaned state: an earlier run widened the regions, died
	// before the trigger write, and now the model names nothing. The
	// row must not stay widened with no stored trigger explaining it.
	root := scopeRoot(t, "web/src/api/schema.ts")
	st := scopeStore(t)

	p := model.Proposal{Signature: "s1", Rule: "schema-sync", Region: "web/src/api",
		Regions: []string{"web/src/api", "api"}, Note: "n", Members: []int64{1},
		Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	fake := &fakeAgent{fn: func(string) (string, error) { return `{"triggers": []}`, nil }}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, meta, ExtractOptions{Root: root})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Retargeted, "the unexplained widening is restored")
	assert.Zero(t, res.Stored)

	rows, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Equal(t, []string{"web/src/api"}, rows[0].Regions, "back to evidence coverage")
	assert.Nil(t, rows[0].TriggerPaths)
	assert.Positive(t, rows[0].TriggerChecked, "the negative answer is stamped")
}

func TestExtractTriggersRespectsConcurrentApply(t *testing.T) {
	// The row was 'proposed' when extraction fetched it; another
	// command applied it during the agent call. The stale-status
	// region write must not land: the installed yaml pin and the
	// ledger would carry different identities.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	p := model.Proposal{Signature: "s1", Rule: "schema-sync", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	// The concurrent apply happens while the "agent" is thinking.
	fake := &fakeAgent{fn: func(string) (string, error) {
		if _, err := st.SetProposalStatus([]int64{p.ID}, model.ProposalApplied); err != nil {
			return "", err
		}

		return fmt.Sprintf(`{"triggers": [{"id": %d, "trigger_paths": ["api/schemas.py"]}]}`, p.ID), nil
	}}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, meta, ExtractOptions{Root: root})
	require.NoError(t, err)

	assert.Zero(t, res.Retargeted, "the stale-status write must not land")

	rows, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "web/src/api", rows[0].Region, "the applied identity is untouched")
	assert.Empty(t, rows[0].Regions)
	assert.Equal(t, []string{"api/schemas.py"}, rows[0].TriggerPaths,
		"triggers still store — they do not change identity")
}

func TestExtractTriggersReturnsPartialResultOnStoreError(t *testing.T) {
	// A store failure mid-run must not discard the counters: the CLI
	// reports what completed, and stamped rows stay done.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t)

	p := model.Proposal{Signature: "s1", Rule: "r", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	fake := &fakeAgent{fn: func(string) (string, error) {
		_ = st.Close() // the store dies while the agent is thinking

		return fmt.Sprintf(`{"triggers": [{"id": %d, "trigger_paths": ["api/schemas.py"]}]}`, p.ID), nil
	}}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, meta, ExtractOptions{Root: root})

	require.Error(t, err)
	require.NotNil(t, res, "the partial result rides along with the error")
	assert.Equal(t, 1, res.Examined)
}

func TestExtractTriggersFailedBatchIsRetryable(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py")
	st := scopeStore(t)

	p := model.Proposal{Signature: "s1", Rule: "r", Note: "n",
		Members: []int64{1}, Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	fake := &fakeAgent{fn: func(string) (string, error) {
		return "", errors.New("agent down")
	}}

	res, err := ExtractTriggers(context.Background(), st, fake,
		[]model.Proposal{p}, nil, ExtractOptions{Root: root})
	require.NoError(t, err, "a lost batch is logged, not fatal")

	assert.Equal(t, 1, res.BatchesFailed)
	assert.Zero(t, res.Stored)

	rows, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Nil(t, rows[0].TriggerPaths, "nothing stored — the next run retries")
}
