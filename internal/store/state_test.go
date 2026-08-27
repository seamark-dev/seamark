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
		TriggerPaths: []string{"cmd/gen.go"}, TriggerChecked: 1700000100,
		TriggerPromptVersion: 2,
		Agent:                "claude/v1", Status: model.ProposalDismissed, CreatedAt: 1700000000,
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
	// may destroy reviewed decisions or paid inference.
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
	assert.Equal(t, []string{"cmd/gen.go"}, got[0].TriggerPaths,
		"trigger paths survive the wire")
	assert.Equal(t, int64(1700000100), got[0].TriggerChecked,
		"the answered-question stamp survives the wire — an import must not re-purchase it")
	assert.Equal(t, 2, got[0].TriggerPromptVersion)

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

func TestImportFillsUnansweredTriggerQuestions(t *testing.T) {
	// Two clones, both applied the same proposal; only one paid for
	// extraction. The import fills the unanswered local row — and
	// never overwrites a local answer.
	src := openTestStore(t)
	require.NoError(t, src.InsertProposal(&model.Proposal{
		Signature: "sig-1", Rule: "r", Note: "n", Status: model.ProposalApplied,
		TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: 1700000100, CreatedAt: 1,
	}))

	state, err := src.ExportState()
	require.NoError(t, err)

	t.Run("unanswered local row adopts the answer", func(t *testing.T) {
		dst := openTestStore(t)
		require.NoError(t, dst.InsertProposal(&model.Proposal{
			Signature: "sig-1", Rule: "r", Note: "n", Status: model.ProposalApplied, CreatedAt: 1,
		}))

		stats, err := dst.ImportState(state)
		require.NoError(t, err)
		assert.Equal(t, 1, stats.TriggersFilled)
		assert.Equal(t, 1, stats.ProposalsSkipped, "the row itself stays local")

		got, err := dst.Proposals(model.ProposalApplied)
		require.NoError(t, err)
		assert.Equal(t, []string{"api/schemas.py"}, got[0].TriggerPaths)
		assert.Equal(t, int64(1700000100), got[0].TriggerChecked)
	})

	t.Run("adopting an unanswered decision keeps the local answer", func(t *testing.T) {
		// The imported row decided the proposal but never asked the
		// trigger question; the local machine did. Adoption takes the
		// decision and keeps the paid answer.
		unanswered := openTestStore(t)
		require.NoError(t, unanswered.InsertProposal(&model.Proposal{
			Signature: "sig-1", Rule: "r", Note: "n",
			Status: model.ProposalDismissed, CreatedAt: 1,
		}))

		bundle, err := unanswered.ExportState()
		require.NoError(t, err)

		dst := openTestStore(t)
		require.NoError(t, dst.InsertProposal(&model.Proposal{
			Signature: "sig-1", Rule: "r", Note: "n", Status: model.ProposalProposed,
			TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: 1700000300, CreatedAt: 1,
		}))

		stats, err := dst.ImportState(bundle)
		require.NoError(t, err)
		assert.Equal(t, 1, stats.ProposalsUpdated)

		got, err := dst.Proposals(model.ProposalDismissed)
		require.NoError(t, err)
		require.Len(t, got, 1, "the decision is adopted")
		assert.Equal(t, []string{"api/schemas.py"}, got[0].TriggerPaths,
			"the locally paid answer survives the adoption")
		assert.Equal(t, int64(1700000300), got[0].TriggerChecked)
	})

	t.Run("a local answer is never overwritten", func(t *testing.T) {
		dst := openTestStore(t)
		require.NoError(t, dst.InsertProposal(&model.Proposal{
			Signature: "sig-1", Rule: "r", Note: "n", Status: model.ProposalApplied,
			TriggerPaths: []string{"db/other.py"}, TriggerChecked: 1700000200, CreatedAt: 1,
		}))

		stats, err := dst.ImportState(state)
		require.NoError(t, err)
		assert.Zero(t, stats.TriggersFilled)

		got, err := dst.Proposals(model.ProposalApplied)
		require.NoError(t, err)
		assert.Equal(t, []string{"db/other.py"}, got[0].TriggerPaths, "local answers win")
	})
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

func TestImportedDecisionCarriesItsRegions(t *testing.T) {
	// The decision was made against the imported row's content: a local
	// pending row adopting the status must adopt the region set too, or
	// pin-identity checks (liveness, prune) look for a stale repo-wide
	// key while lessons.yaml carries the multi-region pin.
	s := openTestStore(t)

	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "sig-2", Rule: "validate-at-the-boundary", Note: "n",
		Status: model.ProposalProposed, CreatedAt: 1,
	}))

	stats, err := s.ImportState(&State{Version: 1, Proposals: []ProposalState{
		{Signature: "sig-2", Rule: "validate-at-the-boundary", Note: "n",
			Region: "api", Regions: []string{"api", "db"},
			Status: model.ProposalApplied, CreatedAt: 1},
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ProposalsUpdated)

	applied, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	assert.Equal(t, "api", applied[0].Region)
	assert.Equal(t, []string{"api", "db"}, applied[0].Regions)
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

	_, err = s.ImportState(&State{Version: StateVersion, Proposals: []ProposalState{{
		Signature: "sig", Rule: "r", Status: model.ProposalApplied,
		TriggerPaths: []string{"a", "b", "c", "d"},
	}}})
	require.Error(t, err, "import cannot bypass the live trigger-path cap")

	// A failed import leaves nothing behind (single transaction).
	proposals, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Empty(t, proposals)
}
