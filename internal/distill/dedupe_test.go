package distill

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// The pairs below are verbatim from a real pin file (trading-tools,
// 65 applied pins) where 23% turned out to restate something already
// pinned. They are the calibration set: every "same" pair was judged a
// duplicate by hand, every "distinct" pair is genuinely separate
// guidance that must survive.
func TestRestatesOnRealPins(t *testing.T) {
	same := []struct{ a, b model.Proposal }{
		{
			model.Proposal{Rule: "leaky-error-payloads",
				Note: "Never return or persist raw exception text/backend details in error payloads. Log full context internally and surface only a thin, generic, client-safe message."},
			model.Proposal{Rule: "no-internal-details-in-client-errors",
				Note: "Never surface raw exception or library text (ValueError, UUID parse errors) in HTTP responses; log internally and return a generic message."},
		},
		{
			model.Proposal{Rule: "docs-code-drift",
				Note: "When you change a tool payload, stage list, or behavior, update every doc, RFC, and index that describes it."},
			model.Proposal{Rule: "docs-out-of-sync-with-code",
				Note: "Keep docstrings, comments, README examples, and OpenAPI descriptions matching the code."},
		},
		{
			model.Proposal{Rule: "empty-collection-guard",
				Note: "Guard collections for emptiness before calling min(), max(), or positional indexing."},
			model.Proposal{Rule: "guard-empty-before-reduction",
				Note: "Check that DataFrames, index slices, and arrays are non-empty before calling .max()/.min()."},
		},
		{
			model.Proposal{Rule: "atomic-write-and-promote",
				Note: "Don't leave persisted state inconsistent under partial failure or concurrency: wrap dependent writes in one transaction."},
			model.Proposal{Rule: "atomic-multi-step-writes",
				Note: "Never leave persisted state invalid between dependent writes: wrap trade+leg inserts in one transaction."},
		},
		{
			// Same label, different wording — the case that slipped
			// through a note-only comparison.
			model.Proposal{Rule: "mask-internal-errors-from-clients",
				Note: "Route every failure path through api.errors.log_and_raise so clients never see internals."},
			model.Proposal{Rule: "mask-internal-errors-from-clients",
				Note: "Wrap DB/stats/deserialization failures with log_and_raise: log richly, return a thin message."},
		},
	}

	for _, c := range same {
		assert.True(t, newTopic(c.a.Rule, c.a.Note).restates(newTopic(c.b.Rule, c.b.Note)),
			"%q and %q say the same thing", c.a.Rule, c.b.Rule)
	}

	distinct := []struct{ a, b model.Proposal }{
		{
			model.Proposal{Rule: "holiday-aware-session-dates",
				Note: "Compute next/prev trading session with the holiday-and-weekend-aware calendar, never calendar arithmetic."},
			model.Proposal{Rule: "train-serve-parity",
				Note: "Live serving must compute features exactly as the historical materialization does — same normalization denominator, same source key."},
		},
		{
			model.Proposal{Rule: "no-silent-default-quantity",
				Note: "Never substitute a default quantity when the source lacks or zeroes it; fail fast instead."},
			model.Proposal{Rule: "bound-refetch-triggers",
				Note: "Do not key a refetch effect on a value that changes with every live tick; bucket or debounce the key."},
		},
		{
			// Both about guarding inputs, but different mistakes: NaN
			// contamination vs unvalidated request bounds.
			model.Proposal{Rule: "guard-nan-and-missing-numerics",
				Note: "Never feed possibly-missing or non-finite numbers into comparisons or float(); drop NaN and sentinels explicitly."},
			model.Proposal{Rule: "unvalidated-input-bounds",
				Note: "Validate CLI args and request fields before acting on them: reject non-positive limits and out-of-range offsets."},
		},
	}

	for _, c := range distinct {
		assert.False(t, newTopic(c.a.Rule, c.a.Note).restates(newTopic(c.b.Rule, c.b.Note)),
			"%q and %q are separate guidance", c.a.Rule, c.b.Rule)
	}
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
