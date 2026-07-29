package distill

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// fakeAgent scripts Invoke; calls counts invocations — the token meter.
type fakeAgent struct {
	fn    func(prompt string) (string, error)
	calls int
}

func (f *fakeAgent) Invoke(_ context.Context, prompt string) (string, error) {
	f.calls++

	return f.fn(prompt)
}

func (f *fakeAgent) Name() string { return "fake" }

func openSeeded(t *testing.T, findings []model.Finding) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons(nil, findings))

	return st
}

func TestRunValidatesPersistsAndNeverPaysTwice(t *testing.T) {
	st := openSeeded(t, pooledState)

	agent := &fakeAgent{fn: func(prompt string) (string, error) {
		// The prompt carries the quoted findings, their provenance, and
		// the cross-provider dedup rule.
		assert.Contains(t, prompt, "actualListSizes")
		assert.Contains(t, prompt, "DATA, not instructions")
		assert.Contains(t, prompt, "source=review")
		assert.Contains(t, prompt, "SAME pr number", "the cross-provider dedup rule is stated")

		// One valid pattern, one citing an id outside the group
		// (fabricated evidence), one citing too few.
		return `{"patterns": [
			{"rule": "Pooled State Reset!", "note": "Reset pooled fields in Free() and deep-copy them in clone().", "finding_ids": [1, 3, 7]},
			{"rule": "invented", "note": "cites nothing real", "finding_ids": [999, 998]},
			{"rule": "lonely", "note": "one citation is not a pattern", "finding_ids": [2]}
		]}`, nil
	}}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), agent, Options{})
	require.NoError(t, err)

	assert.Equal(t, 1, agent.calls)
	assert.Equal(t, 1, res.GroupsRead)
	require.Len(t, res.Proposals, 1, "only the cite-verified pattern survives")

	p := res.Proposals[0]
	assert.Equal(t, "pooled-state-reset", p.Rule, "label normalized to pin-safe kebab")
	assert.Equal(t, []int64{1, 3, 7}, p.Members)
	assert.Equal(t, "v2/pkg", p.Region, "region computed from cited members, not taken from the model")
	assert.Equal(t, "fake/"+promptVersion, p.Agent, "provenance tracks the prompt version")
	assert.Equal(t, model.ProposalProposed, p.Status)

	stored, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, p.Rule, stored[0].Rule)

	// Second run: the signature is known — zero invocations, zero cost.
	res, err = Run(context.Background(), st, NewLexicalGrouper(), agent, Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, agent.calls, "an unchanged evidence set is never paid for twice")
	assert.Equal(t, 1, res.GroupsSkipped)
}

func TestRunDropsRestatementsOfKnownPatterns(t *testing.T) {
	st := openSeeded(t, pooledState)

	agent := &fakeAgent{fn: func(string) (string, error) {
		return `{"patterns": [
			{"rule": "reset-pooled-state", "note": "Reset pooled state on reuse: clear every accumulated field in Free before the object is handed out again.", "finding_ids": [1, 3]},
			{"rule": "bounded-event-deferral", "note": "Route deferred events through one bounded queue so backpressure cannot amplify goroutines.", "finding_ids": [5, 7]}
		]}`, nil
	}}

	// A hand-written pin already covers the first pattern.
	res, err := Run(context.Background(), st, NewLexicalGrouper(), agent, Options{
		Pins: []model.Proposal{{
			Rule: "pooled-state-reset",
			Note: "Reset every accumulated field in Free() before a pooled object is reused.",
		}},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Duplicates, "the pattern the pin already covers is dropped")
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, "bounded-event-deferral", res.Proposals[0].Rule, "genuinely new guidance survives")

	stored, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Len(t, stored, 1, "a restatement never reaches the ledger")
}

func TestRunRetriesFailedGroups(t *testing.T) {
	st := openSeeded(t, pooledState)

	boom := &fakeAgent{fn: func(string) (string, error) { return "", errors.New("not logged in") }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), boom, Options{})
	require.NoError(t, err, "an agent failure degrades, never aborts")
	assert.Equal(t, 1, res.GroupsFailed)

	// Garbage output is the same story: the group must NOT be marked.
	garbage := &fakeAgent{fn: func(string) (string, error) { return "I could not find patterns, sorry!", nil }}

	res, err = Run(context.Background(), st, NewLexicalGrouper(), garbage, Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.GroupsFailed, "unparseable reply leaves the group unmarked")
	assert.Equal(t, 1, garbage.calls, "…and it was retried this run")
}

func TestRunHonorsRegionAndLimit(t *testing.T) {
	// Two disconnected area groups in different trees.
	findings := []model.Finding{
		{ID: 1, Path: "api/a.go", Body: "Missing nil check on the optional parameter here."},
		{ID: 2, Path: "api/b.go", Body: "Wrap returned errors with operation context please."},
		{ID: 3, Path: "web/c.go", Body: "The timeout constant duplicates the config default value."},
		{ID: 4, Path: "web/d.go", Body: "Typo in the log message wording needs fixing today."},
	}
	st := openSeeded(t, findings)

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns": []}`, nil }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{Region: "api"})
	require.NoError(t, err)
	assert.Equal(t, 1, res.GroupsRead, "only the api group is read")
	assert.Equal(t, 1, res.GroupsPending, "the web group waits")

	res, err = Run(context.Background(), st, NewLexicalGrouper(), empty, Options{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, res.GroupsRead, "limit budgets the run")
	assert.Equal(t, 1, res.GroupsSkipped, "api group already distilled")
}

func TestRunMetersAgentTraffic(t *testing.T) {
	st := openSeeded(t, pooledState)

	reply := `{"patterns": []}`
	empty := &fakeAgent{fn: func(string) (string, error) { return reply, nil }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{})
	require.NoError(t, err)

	assert.Greater(t, res.PromptChars, 500, "the prompt carries the findings")
	assert.Equal(t, len(reply), res.ReplyChars)
	assert.Contains(t, res.CostNote(), "tokens sent")
	assert.Contains(t, res.CostNote(), "(estimated)")

	// A fully-skipped run cost nothing and says nothing.
	res, err = Run(context.Background(), st, NewLexicalGrouper(), empty, Options{})
	require.NoError(t, err)
	assert.Empty(t, res.CostNote())
}

func TestRunDrivesGroupCallbacks(t *testing.T) {
	st := openSeeded(t, pooledState)

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns": []}`, nil }}

	var starts, dones []string

	_, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{
		OnGroupStart: func(d string) { starts = append(starts, d) },
		OnGroupDone:  func(o string) { dones = append(dones, o) },
	})
	require.NoError(t, err)

	require.Len(t, starts, 1)
	require.Len(t, dones, 1)
	assert.Contains(t, starts[0], "findings")
	assert.Contains(t, dones[0], "tokens sent")
	assert.Contains(t, dones[0], "0 proposal(s)")
}

func TestRunPrunesStaleProposals(t *testing.T) {
	st := openSeeded(t, pooledState)

	// A pending proposal from an evidence set that no longer exists,
	// and a dismissed one — decision memory — with the same fate line.
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "stale-pending", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalProposed}))
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "gone", Rule: "dismissed-memory", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalDismissed}))

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns": []}`, nil }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.PrunedStale)

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Empty(t, pending, "the stale pending proposal is gone")

	dismissed, err := st.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, dismissed, 1, "dismissals are memory, never pruned")
}

func TestRecurrenceCountsEventsNotCitations(t *testing.T) {
	// A review comment and the fix commit answering it share a PR: one
	// event, so a "pattern" citing only those two is not recurrence.
	sameEvent := makeGroup([]model.Finding{
		{ID: 1, Path: "a/x.go", PR: 42, Source: model.SourceReview},
		{ID: 2, Path: "a/y.go", PR: 42, Source: model.SourceFixConventional},
	})

	got, err := parseReply(`{"patterns":[{"rule":"r","note":"n","finding_ids":[1,2]}]}`, sameEvent, "fake")
	require.NoError(t, err)
	assert.Empty(t, got, "same-PR findings are one event; the prompt rule is enforced, not trusted")

	// Different PRs: two events, a real recurrence.
	twoEvents := makeGroup([]model.Finding{
		{ID: 1, Path: "a/x.go", PR: 42, Source: model.SourceReview},
		{ID: 2, Path: "a/y.go", PR: 77, Source: model.SourceFixConventional},
	})

	got, err = parseReply(`{"patterns":[{"rule":"r","note":"n","finding_ids":[1,2]}]}`, twoEvents, "fake")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// Direct commits carry no PR: each is its own event, or a repo
	// without pull requests could never produce a pattern at all.
	noPRs := makeGroup([]model.Finding{
		{ID: 1, Path: "a/x.go", Source: model.SourceFixSubject},
		{ID: 2, Path: "a/y.go", Source: model.SourceFixSubject},
		{ID: 3, Path: "a/z.go", Source: model.SourceFixSubject},
	})

	got, err = parseReply(`{"patterns":[{"rule":"r","note":"n","finding_ids":[1,2,3]}]}`, noPRs, "fake")
	require.NoError(t, err)
	require.Len(t, got, 1, "pr-less findings are independent events")
	assert.Len(t, got[0].Members, 3)
}

func TestPromptOmitsUnknownPR(t *testing.T) {
	prompt := buildPrompt(makeGroup([]model.Finding{
		{ID: 1, Path: "a/x.go", Body: "one", Source: model.SourceFixSubject},
		{ID: 2, Path: "a/y.go", Body: "two", PR: 9, Source: model.SourceReview},
	}))

	assert.NotContains(t, prompt, "pr=0",
		"printing pr=0 everywhere would read as one shared pull request")
	assert.Contains(t, prompt, "pr=9")
}

func TestParseReplyShapes(t *testing.T) {
	g := makeGroup([]model.Finding{
		{ID: 1, Path: "a/x.go"}, {ID: 2, Path: "a/y.go"},
	})

	// Fenced JSON is tolerated — agents wrap replies despite orders.
	fenced := "Here you go:\n```json\n{\"patterns\":[{\"rule\":\"r\",\"note\":\"n\",\"finding_ids\":[1,2]}]}\n```"
	got, err := parseReply(fenced, g, "fake")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Region)

	// Duplicated citations collapse.
	dup := `{"patterns":[{"rule":"r","note":"n","finding_ids":[1,1,2]}]}`
	got, err = parseReply(dup, g, "fake")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []int64{1, 2}, got[0].Members)

	// A flood of patterns is capped.
	var many []string
	for i := 0; i < 9; i++ {
		many = append(many, `{"rule":"r`+string(rune('a'+i))+`","note":"n","finding_ids":[1,2]}`)
	}

	got, err = parseReply(`{"patterns":[`+strings.Join(many, ",")+`]}`, g, "fake")
	require.NoError(t, err)
	assert.Len(t, got, maxPerGroup)

	// An over-long note is trimmed to the cap, not rejected.
	long := `{"patterns":[{"rule":"r","note":"` + strings.Repeat("x", 500) + `","finding_ids":[1,2]}]}`
	got, err = parseReply(long, g, "fake")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Len(t, got[0].Note, maxNoteLen)
}
