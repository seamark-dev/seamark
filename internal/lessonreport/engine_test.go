package lessonreport

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestLinksFromApplied maps the pin identity of an applied proposal to
// its ID, through the digest of the ledger's own key.
func TestLinksFromApplied(t *testing.T) {
	assert.NotNil(t, LinksFromApplied(nil), "the map is never nil")
	assert.Empty(t, LinksFromApplied(nil))

	applied := []model.Proposal{
		{ID: 7, Status: model.ProposalApplied, Rule: "Keep-Parser-Strict", Region: "internal/parse/"},
		{ID: 9, Status: model.ProposalApplied, Rule: "keep-parser-strict", Regions: []string{"internal/parse"}},
		{ID: 4, Status: model.ProposalApplied, Rule: "frozen-assets", Region: "generated"},
		{ID: 12, Status: "proposed", Rule: "never-linked", Region: "x"},
		{ID: 13, Status: model.ProposalApplied, Rule: "repo-wide"},
	}
	want := map[string]int64{
		reviews.PinIdentityDigest(reviews.NewPinKey("keep-parser-strict", "internal/parse", nil)): 9,
		reviews.PinIdentityDigest(reviews.NewPinKey("frozen-assets", "generated", nil)):           4,
		reviews.PinIdentityDigest(reviews.NewPinKey("repo-wide", "", nil)):                        13,
	}

	assert.Equal(t, want, LinksFromApplied(applied), "two spellings of one pin share one identity; the newest wins")

	// The order of the list never changes the result.
	reversed := []model.Proposal{applied[4], applied[3], applied[2], applied[1], applied[0]}
	assert.Equal(t, want, LinksFromApplied(reversed))
}
