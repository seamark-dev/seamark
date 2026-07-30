package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// seedDecisions writes one row of every durable kind: a decided proposal,
// distillation memory, and a lesson with its finding.
func seedDecisions(t *testing.T, s *Store) {
	t.Helper()

	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "sig-1", Rule: "no-naked-returns", Region: "internal",
		Note: "avoid naked returns", Members: []int64{11, 12},
		Agent: "claude/v1", Status: model.ProposalDismissed, CreatedAt: 1700000000,
	}))
	require.NoError(t, s.MarkDistilled("sig-1", "internal", 1700000000))
	require.NoError(t, s.ReplaceLessons(
		[]model.Lesson{{ClusterKey: "ck", Region: "internal", Reviewer: "coderabbit",
			Symptom: "S1", Occurrences: 3, LastTS: 1700000000}},
		[]model.Finding{{ID: 11, LessonKey: "ck", Path: "a.go", Body: "fix it",
			Source: model.SourceReview}},
	))
}

func TestRebuildPreservesDecisions(t *testing.T) {
	s := openTestStore(t)
	seedDecisions(t, s)

	// A full rebuild — including the --force path and the MCP self-repair
	// — wipes only derived tables. THE durability invariant: no reindex
	// may destroy human decisions or paid inference.
	seed(t, s)

	dismissed, err := s.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, dismissed, 1, "the dismissal decision must survive a rebuild")
	assert.Equal(t, "sig-1", dismissed[0].Signature)

	marks, err := s.DistilledSignatures()
	require.NoError(t, err)
	assert.True(t, marks["sig-1"], "paid distillation memory must survive a rebuild")

	lessons, err := s.AllLessons(10)
	require.NoError(t, err)
	assert.Len(t, lessons, 1, "mined lessons must survive a rebuild")

	v, err := s.GetMeta(schemaVersionKey)
	require.NoError(t, err)
	assert.NotEmpty(t, v, "the schema stamp must survive a rebuild")
}

func TestExportImportRoundTrip(t *testing.T) {
	src := openTestStore(t)
	seedDecisions(t, src)

	state, err := src.ExportState()
	require.NoError(t, err)
	assert.Equal(t, StateVersion, state.Version)
	require.Len(t, state.Proposals, 1)
	require.Len(t, state.Distilled, 1)

	// Import into a fresh database: everything lands.
	dst := openTestStore(t)

	stats, err := dst.ImportState(state)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ProposalsAdded)
	assert.Equal(t, 1, stats.DistilledAdded)

	got, err := dst.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "no-naked-returns", got[0].Rule)
	assert.Equal(t, []int64{11, 12}, got[0].Members)

	marks, err := dst.DistilledSignatures()
	require.NoError(t, err)
	assert.True(t, marks["sig-1"])

	// Re-importing the same bundle is a no-op.
	stats, err = dst.ImportState(state)
	require.NoError(t, err)
	assert.Zero(t, stats.ProposalsAdded)
	assert.Equal(t, 1, stats.ProposalsSkipped)
	assert.Equal(t, 1, stats.DistilledSkipped)
}

func TestImportNeverOverwritesLocalDecisions(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "sig-1", Rule: "r", Note: "n",
		Status: model.ProposalApplied, CreatedAt: 1,
	}))

	// An imported dismissal for the same (signature, rule) must lose to
	// the local applied decision.
	stats, err := s.ImportState(&State{Version: 1, Proposals: []ProposalState{
		{Signature: "sig-1", Rule: "r", Note: "other", Status: model.ProposalDismissed, CreatedAt: 2},
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ProposalsSkipped)

	applied, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Len(t, applied, 1, "the local decision stands")
}

func TestImportResolvesPendingProposal(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "sig-1", Rule: "r", Note: "n",
		Status: model.ProposalProposed, CreatedAt: 1,
	}))

	// A local row still awaiting a decision adopts the imported one:
	// a decision beats no decision.
	stats, err := s.ImportState(&State{Version: 1, Proposals: []ProposalState{
		{Signature: "sig-1", Rule: "r", Note: "n", Status: model.ProposalDismissed, CreatedAt: 1},
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ProposalsUpdated)

	dismissed, err := s.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	assert.Len(t, dismissed, 1)
}

func TestImportRejectsBadBundles(t *testing.T) {
	s := openTestStore(t)

	_, err := s.ImportState(&State{Version: StateVersion + 1})
	require.Error(t, err, "a bundle from a newer seamark must be refused")

	_, err = s.ImportState(&State{Version: 1, Proposals: []ProposalState{
		{Signature: "sig", Rule: "r", Status: "banana"},
	}})
	require.Error(t, err, "an unknown status must be refused")

	_, err = s.ImportState(&State{Version: 1, Proposals: []ProposalState{
		{Signature: "", Rule: "r", Status: model.ProposalApplied},
	}})
	require.Error(t, err, "an empty proposal signature must be refused")

	_, err = s.ImportState(&State{Version: 1, Distilled: []DistilledMark{
		{Signature: ""},
	}})
	require.Error(t, err, "an empty distillation-mark signature must be refused")

	// A failed import leaves nothing behind (single transaction).
	proposals, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Empty(t, proposals)
}
