package distill

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// The wording calibration set lives with the wording package; the
// tests here cover what dedupe adds on top of it: evidence identity.
//
// The regression shape is real: group signatures churn as the corpus
// grows, and two different groups once cited the byte-identical
// 7-finding subset — both proposals were applied ("run-linters-
// before-commit" / "lint-clean-before-push") because their wordings
// shared almost nothing. Identity must catch what wording cannot.
func TestRestatedByEvidenceIdentity(t *testing.T) {
	known := NewKnown([]model.Proposal{
		{ID: 54, Rule: "run-linters-before-commit",
			Note:    "Run every linter the repo configures before committing.",
			Members: []int64{101, 102, 103, 104, 105, 106, 107}},
	})

	cases := []struct {
		name    string
		members []int64
		dup     bool
	}{
		{"identical evidence", []int64{101, 102, 103, 104, 105, 106, 107}, true},
		// Containment is NOT identity: one finding can flag two
		// mistakes, so a subset can be a genuinely different pattern —
		// and suppressing it would mark its group distilled with the
		// lesson unextracted. Wording still judges these.
		{"subset of known evidence", []int64{102, 105}, false},
		{"superset of known evidence", []int64{100, 101, 102, 103, 104, 105, 106, 107}, false},
		{"overlapping but not nested", []int64{106, 107, 200}, false},
		{"disjoint evidence", []int64{200, 201}, false},
	}

	for _, c := range cases {
		// Wording is deliberately unrelated: identity alone must decide.
		of, dup := known.Restated(model.Proposal{
			Rule:    "atomic-snapshot-promotion",
			Note:    "Stage a full generation before promoting the live archive.",
			Members: c.members,
		})

		assert.Equal(t, c.dup, dup, c.name)

		if c.dup {
			assert.Equal(t, "run-linters-before-commit", of, c.name)
		}
	}
}

func TestRestatedIdentitySparesSameReplyPatterns(t *testing.T) {
	// One finding can flag two mistakes: a model reading a group may
	// honestly cite the same members for distinct patterns in one
	// reply. Identity must not collapse batch-mates — wording still
	// guards the batch against padding.
	known := NewKnown([]model.Proposal{
		{Rule: "leaky-error-payloads", Signature: "sig-a",
			Note:    "Never surface raw exception text in responses; log internally, return a generic message.",
			Members: []int64{11, 12}},
	})

	_, dup := known.Restated(model.Proposal{
		Rule: "pooled-state-reset", Signature: "sig-a",
		Note:    "Reset every pooled field in Free() before the struct is reused.",
		Members: []int64{11, 12},
	})
	assert.False(t, dup, "same reply, distinct wording: both patterns stand")

	of, dup := known.Restated(model.Proposal{
		Rule: "pooled-state-reset", Signature: "sig-b",
		Note:    "Reset every pooled field in Free() before the struct is reused.",
		Members: []int64{11, 12},
	})
	assert.True(t, dup, "the same citations from a DIFFERENT batch are a re-derivation")
	assert.Equal(t, "leaky-error-payloads", of)
}

func TestRestatedIdentityIgnoresConfigPins(t *testing.T) {
	// Config pins cite no findings; identity must say nothing about
	// them, in either direction.
	known := NewKnown([]model.Proposal{
		{Rule: "ascii-only-scripts", Note: "Keep scripts ASCII — smart quotes have bitten us."},
	})

	_, dup := known.Restated(model.Proposal{
		Rule: "bounded-event-deferral",
		Note: "Route deferred events through one bounded forwarding queue so backpressure cannot amplify goroutines.",
		// Any members at all: nested-against-nothing must not trigger.
		Members: []int64{1, 2},
	})
	assert.False(t, dup, "evidence can never nest inside an evidence-free pin")
}

func TestKnownCoversPinsAndEveryDecision(t *testing.T) {
	known := NewKnown([]model.Proposal{
		// A hand-written pin: no id, just rule and note.
		{Rule: "ascii-only-scripts", Note: "Keep scripts ASCII — smart quotes from chat have bitten us."},
	}, []model.Proposal{
		{ID: 4, Rule: "docs-code-drift", Note: "Update every doc, RFC, and index that describes changed behavior.",
			Status: model.ProposalDismissed},
	})

	of, dup := known.Restated(model.Proposal{
		Rule: "docs-out-of-sync-with-code",
		Note: "Keep docstrings, comments, and README examples matching the code when behavior changes."})
	assert.True(t, dup, "a dismissal is a decision; re-proposing it relitigates")
	assert.Equal(t, "docs-code-drift", of)

	_, dup = known.Restated(model.Proposal{
		Rule: "ascii-only-in-scripts",
		Note: "Keep scripts ASCII: smart quotes pasted from chat have bitten us before."})
	assert.True(t, dup, "hand-written pins count as known too")

	_, dup = known.Restated(model.Proposal{
		Rule: "bounded-event-deferral",
		Note: "Route deferred events through one bounded forwarding queue so backpressure cannot amplify goroutines."})
	assert.False(t, dup, "genuinely new guidance passes")
}

func TestClustersGroupsAndOrders(t *testing.T) {
	ps := []model.Proposal{
		{ID: 1, Rule: "empty-collection-guard", Note: "Guard collections for emptiness before min(), max(), or indexing."},
		{ID: 2, Rule: "train-serve-parity", Note: "Serving must compute features exactly as the historical materialization does."},
		{ID: 3, Rule: "guard-empty-datasets", Note: "Before indexing or resampling, guard for empty datasets and frames."},
		{ID: 4, Rule: "guard-empty-before-reduction", Note: "Check arrays and frames are non-empty before calling max() or min()."},
		{ID: 5, Rule: "docs-code-drift", Note: "Update every doc and comment that describes changed behavior."},
		{ID: 6, Rule: "docs-out-of-sync-with-code", Note: "Keep docstrings, comments and README examples matching the code."},
	}

	got := Clusters(ps)

	require.Len(t, got, 2, "two themes recur; the singleton is not a cluster")
	assert.Len(t, got[0], 3, "largest cluster first")
	assert.Len(t, got[1], 2)

	for _, c := range got {
		for _, p := range c {
			assert.NotEqual(t, int64(2), p.ID, "distinct guidance is never clustered")
		}
	}

	assert.Empty(t, Clusters(ps[:1]), "a lone pin has nothing to restate")
}

func TestClustersCatchRederivedEvidence(t *testing.T) {
	// The p54/p56 shape: identical citations, wordings that share
	// almost nothing. The audit must name them one cluster so the
	// human gets a prune suggestion.
	ps := []model.Proposal{
		{ID: 54, Rule: "run-linters-before-commit",
			Note:    "Run every linter the repo configures before committing.",
			Members: []int64{101, 102, 103}},
		{ID: 56, Rule: "atomic-snapshot-promotion",
			Note:    "Stage a full generation before promoting the live archive.",
			Members: []int64{101, 102, 103}},
		{ID: 60, Rule: "train-serve-parity",
			Note:    "Serving must compute features exactly as the historical materialization does.",
			Members: []int64{300, 301}},
	}

	got := Clusters(ps)

	require.Len(t, got, 1)
	require.Len(t, got[0], 2)
	assert.NotEqual(t, int64(60), got[0][0].ID)
	assert.NotEqual(t, int64(60), got[0][1].ID)
}
