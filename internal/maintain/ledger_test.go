package maintain

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestAppliedKeysGivesOneKeyPerRowOutsideTheSelection(t *testing.T) {
	// Rows 3 and 4 have disagreeing region fields. Each row still reserves
	// one key: the key of its stored fields, which the CLI prune and the
	// liveness checks read too.
	rows := []model.Proposal{
		{ID: 1, Rule: "single", Region: "api", Status: model.ProposalApplied},
		{ID: 2, Rule: "set", Region: "api", Regions: []string{"api", "db"}, Status: model.ProposalApplied},
		{ID: 3, Rule: "wild", Region: "api", Regions: []string{"*", "db"}, Status: model.ProposalApplied},
		{ID: 4, Rule: "apart", Region: "x", Regions: []string{"a", "b"}, Status: model.ProposalApplied},
		{ID: 5, Rule: "selected", Region: "lib", Status: model.ProposalApplied},
	}

	keys := appliedKeys(rows, map[int64]bool{5: true})

	assert.Equal(t, []reviews.PinKey{
		reviews.NewPinKey("single", "api", nil),
		reviews.NewPinKey("set", "", []string{"api", "db"}),
		reviews.NewPinKey("wild", "", []string{"api", "db"}),
		reviews.NewPinKey("apart", "", []string{"a", "b", "x"}),
	}, keys, "a selected row reserves no key")

	for i, p := range rows[:4] {
		assert.Equal(t, reviews.NewPinKey(p.Rule, p.Region, p.Regions), keys[i], "the CLI form of p%d", p.ID)
	}
}
