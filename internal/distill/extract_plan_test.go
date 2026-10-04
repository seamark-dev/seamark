package distill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// extractFindings are the indexed findings of the extraction fixture.
// Their bodies carry markers, so a test can prove that no finding text
// reaches a console prompt.
var extractFindings = []model.Finding{
	{ID: 1, Path: "web/src/api/schema.ts", PR: 1, Body: "BODY-MARKER-ONE"},
	{ID: 2, Path: "api/handlers.go", PR: 2, Body: "BODY-MARKER-TWO"},
}

// extractWorkspace is the console extraction fixture: a working tree, a
// file store whose co-change confirms api/schemas.py for the schema
// client, and the indexed findings.
type extractWorkspace struct {
	root string
	path string
	st   *store.Store
}

// newExtractWorkspace builds the fixture with an open writable store.
func newExtractWorkspace(t *testing.T) *extractWorkspace {
	t.Helper()

	w := &extractWorkspace{
		root: scopeRoot(t, "api/schemas.py", "api/handlers.go", "web/src/api/schema.ts"),
		path: filepath.Join(t.TempDir(), "index.db"),
	}

	st, err := store.Open(w.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))
	}))
	require.NoError(t, st.ReplaceLessons(nil, extractFindings))

	w.st = st

	return w
}

// add stores p and returns it with its ID.
func (w *extractWorkspace) add(t *testing.T, p model.Proposal) model.Proposal {
	t.Helper()

	require.NoError(t, w.st.InsertProposal(&p))

	return p
}

// pending stores an unanswered pending row scoped to the schema client.
func (w *extractWorkspace) pending(t *testing.T, rule string, members ...int64) model.Proposal {
	t.Helper()

	return w.add(t, model.Proposal{Signature: "sig-" + rule, Rule: rule, Region: "web/src/api",
		Note: "Note for " + rule + ".", Members: members, Status: model.ProposalProposed})
}

// applied stores an unanswered applied row scoped to the schema client.
func (w *extractWorkspace) applied(t *testing.T, rule string, members ...int64) model.Proposal {
	t.Helper()

	return w.add(t, model.Proposal{Signature: "sig-" + rule, Rule: rule, Region: "web/src/api",
		Note: "Note for " + rule + ".", Members: members, Status: model.ProposalApplied})
}

// row reads one proposal in any status.
func (w *extractWorkspace) row(t *testing.T, id int64) model.Proposal {
	t.Helper()

	rows, err := w.st.ProposalsWithIDs([]int64{id})
	require.NoError(t, err)
	require.Len(t, rows, 1, "p%d exists", id)

	return rows[0]
}

// exec runs one statement through a second connection, as another
// process would.
func (w *extractWorkspace) exec(t *testing.T, query string, args ...any) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+w.path+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	_, err = db.Exec(query, args...)
	require.NoError(t, err)
}

// plan plans ids against the fixture's root and store.
func (w *extractWorkspace) plan(t *testing.T, ids ...int64) *ExtractPlan {
	t.Helper()

	p, err := NewExtractPlan(w.st, ExtractPlanOptions{IDs: ids, Root: w.root,
		Spec: agent.CommandSpec{Name: "fake", Argv: []string{"fake", "-p"}}})
	require.NoError(t, err)

	return p
}

// skips returns the ineligible reasons keyed by ID.
func skips(p *ExtractPlan) map[int64]string {
	out := make(map[int64]string, len(p.Ineligible))
	for _, s := range p.Ineligible {
		out[s.ID] = s.Reason
	}

	return out
}

func TestNewExtractPlanRefusesAnEmptyRoot(t *testing.T) {
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)

	plan, err := NewExtractPlan(w.st, ExtractPlanOptions{IDs: []int64{p.ID}})

	require.ErrorIs(t, err, ErrExtractRoot)
	assert.Nil(t, plan)
}

func TestNewExtractPlanEligibilityMatrix(t *testing.T) {
	// AC-8: every selection state of a proposal, with the shared
	// predicate first and the caller's exclusions after it.
	w := newExtractWorkspace(t)

	missing := w.pending(t, "missing", 1)
	obsoletePending := w.add(t, model.Proposal{Signature: "s-ob", Rule: "obsolete-negative", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalProposed,
		TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion - 1})
	obsoleteApplied := w.add(t, model.Proposal{Signature: "s-ob0", Rule: "obsolete-unversioned", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalApplied, TriggerChecked: 100})
	currentNegative := w.add(t, model.Proposal{Signature: "s-cn", Rule: "current-negative", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalProposed,
		TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion})
	currentPositive := w.add(t, model.Proposal{Signature: "s-cp", Rule: "current-positive", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Status: model.ProposalApplied,
		TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: 100, TriggerPromptVersion: 1})
	dismissed := w.add(t, model.Proposal{Signature: "s-d", Rule: "dismissed", Note: "n",
		Members: []int64{1}, Status: model.ProposalDismissed})
	superseded := w.add(t, model.Proposal{Signature: "s-s", Rule: "superseded", Note: "n",
		Members: []int64{1}, Status: model.ProposalSuperseded})
	agedOut := w.pending(t, "aged-out", 99)
	notPresent := w.applied(t, "pin-not-present", 1)
	ambiguous := w.applied(t, "pin-ambiguous", 2)

	var excludeCalls []int64

	plan, err := NewExtractPlan(w.st, ExtractPlanOptions{
		IDs: []int64{
			ambiguous.ID, missing.ID, obsoletePending.ID, obsoleteApplied.ID, currentNegative.ID,
			currentPositive.ID, dismissed.ID, superseded.ID, agedOut.ID, notPresent.ID, 999, missing.ID,
		},
		Root: w.root,
		Exclude: func(p model.Proposal) (string, bool) {
			excludeCalls = append(excludeCalls, p.ID)

			switch p.ID {
			case notPresent.ID:
				return "pin not present", true
			case ambiguous.ID:
				return "ambiguous: duplicate pins", true
			default:
				return "", false
			}
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []int64{obsoletePending.ID, missing.ID, obsoleteApplied.ID}, plan.IDs(),
		"pending newest first, then applied newest first; a repeated ID plans once")

	assert.Equal(t, map[int64]string{
		currentNegative.ID: "already answered negative under the current question",
		currentPositive.ID: "already answered with trigger paths",
		dismissed.ID:       "dismissed",
		superseded.ID:      "superseded",
		agedOut.ID:         "no living evidence: every cited finding aged out of the index",
		notPresent.ID:      "pin not present",
		ambiguous.ID:       "ambiguous: duplicate pins",
		999:                "not found in the ledger",
	}, skips(plan))

	for i := 1; i < len(plan.Ineligible); i++ {
		assert.Less(t, plan.Ineligible[i-1].ID, plan.Ineligible[i].ID, "ineligible rows in ID order")
	}

	assert.ElementsMatch(t, []int64{missing.ID, obsoletePending.ID, obsoleteApplied.ID, agedOut.ID,
		notPresent.ID, ambiguous.ID}, excludeCalls,
		"the caller's exclusions run only for open pending and applied rows")

	reasons := map[int64]TriggerReason{}

	for _, item := range plan.Batches[0].Items {
		reasons[item.ID] = item.Reason
	}

	assert.Equal(t, map[int64]TriggerReason{
		missing.ID:         TriggerOpen,
		obsoletePending.ID: TriggerOpenObsoleteNegative,
		obsoleteApplied.ID: TriggerOpenObsoleteNegative,
	}, reasons)
}

func TestNewExtractPlanEmptySelectionPlansNothing(t *testing.T) {
	w := newExtractWorkspace(t)
	w.pending(t, "never-selected", 1)

	for _, ids := range [][]int64{nil, {}} {
		plan := w.plan(t, ids...)

		assert.Empty(t, plan.Batches, "no proposal is added to an empty selection")
		assert.Empty(t, plan.Ineligible)
		assert.Zero(t, plan.Preflight.Proposals)
		assert.Zero(t, plan.Preflight.PromptChars)
	}
}

func TestNewExtractPlanWritesNothingAndWorksReadOnly(t *testing.T) {
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	require.NoError(t, w.st.Close())

	before := fileDigest(t, w.path)

	ro, err := store.OpenReadOnly(w.path)
	require.NoError(t, err)

	defer func() { _ = ro.Close() }()

	plan, err := NewExtractPlan(ro, ExtractPlanOptions{IDs: []int64{p.ID}, Root: w.root})
	require.NoError(t, err)
	require.Len(t, plan.Batches, 1)
	require.NoError(t, plan.Revalidate(ro), "revalidation reads only")

	assert.Equal(t, before, fileDigest(t, w.path), "planning writes nothing")
}

func TestNewExtractPlanDisclosesWhatItSends(t *testing.T) {
	w := newExtractWorkspace(t)
	current := w.pending(t, "already-covered", 1)
	widened := w.add(t, model.Proposal{Signature: "s-w", Rule: "widened", Region: "web/src/api",
		Regions: []string{"web/src/api", "api"}, Note: "Widened note.", Members: []int64{1, 99},
		Status: model.ProposalProposed})
	installed := w.applied(t, "installed", 2)

	plan := w.plan(t, current.ID, widened.ID, installed.ID)
	require.Len(t, plan.Batches, 1)

	b := plan.Batches[0]
	require.Len(t, b.Items, 3)

	assert.Equal(t, []int64{widened.ID, current.ID, installed.ID}, plan.IDs())
	assert.Equal(t, b.Prompt, buildExtractPrompt(b.Proposals, map[int64]model.Finding{
		1: {ID: 1, Path: "web/src/api/schema.ts", PR: 1, Source: model.SourceReview},
		2: {ID: 2, Path: "api/handlers.go", PR: 2, Source: model.SourceReview},
	}), "the prompt is the CLI prompt over finding metadata")
	assert.Equal(t, len(b.Prompt), b.PromptChars)
	assert.NotContains(t, b.Prompt, "BODY-MARKER", "no finding text is sent")
	assert.NotContains(t, b.Prompt, "evidence excerpt")

	for i, item := range b.Items {
		assert.Equal(t, b.Proposals[i].ID, item.ID, "items are parallel to the proposals")

		line := "    evidence files: " + strings.Join(item.Evidence, ", ") + "\n"
		assert.Contains(t, b.Prompt, line, "p%d discloses exactly its prompt line", item.ID)
	}

	assert.Equal(t, []string{"web/src/api/schema.ts"}, b.Items[0].Evidence)
	assert.Len(t, b.Items[0].Living, 1, "an aged-out member is not living evidence")
	assert.Equal(t, []string{"api/handlers.go"}, b.Items[2].Evidence)

	assert.Equal(t, []string{"web/src/api"}, b.Items[1].Coverage)
	assert.False(t, b.Items[1].CoverageChanges, "a negative answer keeps a covered pending row")
	assert.True(t, b.Items[0].CoverageChanges, "a negative answer narrows a widened pending row")
	assert.Nil(t, b.Items[2].Coverage, "an applied row never changes regions here")
	assert.False(t, b.Items[2].CoverageChanges)

	assert.Equal(t, ExtractPreflight{Proposals: 3, Batches: 1, PromptChars: b.PromptChars,
		Agent: []string{"fake", "-p"}}, plan.Preflight, "no excerpt cap: no finding text is sent")
	assert.False(t, plan.PlannedAt.IsZero())

	again := w.plan(t, installed.ID, current.ID, widened.ID)
	assert.Equal(t, plan.Snapshot, again.Snapshot, "the snapshot depends on the inputs, not the selection order")
	assert.NotEqual(t, ExtractSnapshot{}, plan.Snapshot)
}

func TestNewExtractPlanBatchesLikeTheCLI(t *testing.T) {
	// The console must send what the CLI sends for the same rows: the
	// same order, the same batch boundaries, and the same prompt bytes.
	w := newExtractWorkspace(t)

	var ids []int64

	for i := range 13 {
		var p model.Proposal
		if i%3 == 0 {
			p = w.applied(t, fmt.Sprintf("applied-%02d", i), 1, 2)
		} else {
			p = w.pending(t, fmt.Sprintf("pending-%02d", i), int64(1+i%2))
		}

		ids = append(ids, p.ID)
	}

	plan := w.plan(t, ids...)
	require.Len(t, plan.Batches, 2)
	assert.Len(t, plan.Batches[0].Proposals, 10)
	assert.Len(t, plan.Batches[1].Proposals, 3)

	var candidates []model.Proposal

	for _, status := range []string{model.ProposalProposed, model.ProposalApplied} {
		rows, err := w.st.Proposals(status)
		require.NoError(t, err)

		for _, p := range rows {
			if open, _ := TriggerQuestionOpen(p); open {
				candidates = append(candidates, p)
			}
		}
	}

	meta, err := w.st.FindingsMetaByIDs(memberIDs(candidates))
	require.NoError(t, err)

	cli := &extractAgent{fn: func(context.Context, int, string) (string, error) {
		return "", errors.New("characterization only")
	}}

	var pf ExtractPreflight

	_, err = ExtractTriggers(context.Background(), w.st, cli, candidates, meta, ExtractOptions{
		Root: w.root, OnPreflight: func(got ExtractPreflight) { pf = got },
	})
	require.NoError(t, err)

	require.Len(t, cli.prompts(), 2)
	assert.Equal(t, plan.Batches[0].Prompt, cli.prompts()[0])
	assert.Equal(t, plan.Batches[1].Prompt, cli.prompts()[1])
	assert.Equal(t, pf.PromptChars, plan.Preflight.PromptChars)
	assert.Equal(t, pf.Proposals, plan.Preflight.Proposals)
}

func TestExtractPlanRevalidateNamesChangedInputs(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, w *extractWorkspace, p model.Proposal)
		want   string
	}{
		{"status", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			_, err := w.st.SetProposalStatus([]int64{p.ID}, model.ProposalDismissed)
			require.NoError(t, err)
		}, "p%d is dismissed now"},
		{"answered by another writer", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			require.NoError(t, w.st.UpdateProposalTriggers(p.ID, nil, 200, TriggerPromptVersion))
		}, "the trigger answer of p%d changed"},
		{"regions", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			p.Region, p.Regions = "api", []string{"api"}
			_, err := w.st.UpdateProposalRegionsIfPending(p)
			require.NoError(t, err)
		}, "the regions of p%d changed"},
		{"note", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			w.exec(t, `UPDATE proposal SET note = 'edited' WHERE id = ?`, p.ID)
		}, "p%d changed"},
		{"deleted", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			_, err := w.st.DeletePendingProposals([]int64{p.ID})
			require.NoError(t, err)
		}, "p%d is gone"},
		{"ID reused", func(t *testing.T, w *extractWorkspace, p model.Proposal) {
			_, err := w.st.DeletePendingProposals([]int64{p.ID})
			require.NoError(t, err)

			reused := w.pending(t, "another-rule", 1)
			require.Equal(t, p.ID, reused.ID, "SQLite reuses the highest deleted rowid")
		}, "p%d names another proposal now"},
		{"evidence aged out", func(t *testing.T, w *extractWorkspace, _ model.Proposal) {
			require.NoError(t, w.st.ReplaceLessons(nil, extractFindings[:1]))
		}, "the living evidence of p%d changed"},
		{"evidence moved", func(t *testing.T, w *extractWorkspace, _ model.Proposal) {
			moved := append([]model.Finding(nil), extractFindings...)
			moved[1].Path = "api/moved.go"
			require.NoError(t, w.st.ReplaceLessons(nil, moved))
		}, "the living evidence of p%d changed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newExtractWorkspace(t)
			p := w.pending(t, "schema-sync", 1, 2)
			plan := w.plan(t, p.ID)

			require.NoError(t, plan.Revalidate(w.st), "an unchanged store is current")

			tc.change(t, w, p)

			err := plan.Revalidate(w.st)
			require.ErrorIs(t, err, ErrExtractStale)
			require.ErrorIs(t, err, ErrStale, "one check serves both kinds of plan")
			assert.Equal(t, "trigger extraction inputs changed since the plan: "+fmt.Sprintf(tc.want, p.ID),
				err.Error())
		})
	}
}

func TestExtractPlanRevalidateNamesEveryChange(t *testing.T) {
	w := newExtractWorkspace(t)
	a := w.pending(t, "first", 1)
	b := w.pending(t, "second", 1)
	plan := w.plan(t, a.ID, b.ID)

	_, err := w.st.SetProposalStatus([]int64{a.ID, b.ID}, model.ProposalApplied)
	require.NoError(t, err)

	err = plan.Revalidate(w.st)
	require.ErrorIs(t, err, ErrExtractStale)
	assert.Contains(t, err.Error(), fmt.Sprintf("p%d is applied now; p%d is applied now", b.ID, a.ID))
}

func TestExtractPlanRevalidateReturnsReadErrorsUnwrapped(t *testing.T) {
	w := newExtractWorkspace(t)
	p := w.pending(t, "schema-sync", 1)
	plan := w.plan(t, p.ID)

	require.NoError(t, w.st.Close())

	err := plan.Revalidate(w.st)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStale, "a read failure says nothing about staleness")

	var nilPlan *ExtractPlan
	require.Error(t, nilPlan.Revalidate(w.st))
}
