package maintain

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// ledgerRows are the proposal rows that the link rules read, each in
// ascending ID order.
type ledgerRows struct {
	applied, pending, superseded []model.Proposal
}

// readLedger reads the applied, pending, and superseded rows. A nil store
// is a ledger that cannot be read, never an empty ledger: an empty one
// would make every pin read as having no linked proposal.
func readLedger(st *store.Store) (ledgerRows, error) {
	if st == nil {
		return ledgerRows{}, fmt.Errorf("%w: no readable index", ErrNoLedger)
	}

	var (
		out  ledgerRows
		dest = []*[]model.Proposal{&out.applied, &out.pending, &out.superseded}
	)

	for i, status := range []string{model.ProposalApplied, model.ProposalProposed, model.ProposalSuperseded} {
		ps, err := st.Proposals(status)
		if err != nil {
			return ledgerRows{}, fmt.Errorf("%w: %w", ErrNoLedger, err)
		}

		*dest[i] = slices.SortedFunc(slices.Values(ps), byID)
	}

	return out, nil
}

// byID orders proposals by ascending ID.
func byID(a, b model.Proposal) int {
	return cmp.Compare(a.ID, b.ID)
}

// livingOf returns the cited findings of p that meta still holds. A
// cited finding that left the index has aged out.
func livingOf(p model.Proposal, meta map[int64]model.Finding) []model.Finding {
	var living []model.Finding

	for _, id := range p.Members {
		if f, ok := meta[id]; ok {
			living = append(living, f)
		}
	}

	return living
}

// appliedKeys returns the reviews.ProposalKey of every applied row
// outside selected. A retarget to one of these keys would give two
// proposals one pin. The CLI and every other reader link a row by the
// same key, so one key per row is enough. The plan and the recovery use
// this one rule, so they agree.
func appliedKeys(applied []model.Proposal, selected map[int64]bool) []reviews.PinKey {
	var keys []reviews.PinKey

	for _, p := range applied {
		if !selected[p.ID] {
			keys = append(keys, reviews.ProposalKey(p))
		}
	}

	return keys
}

// members returns the cited finding IDs of every row.
func members(rows []model.Proposal) []int64 {
	var ids []int64

	for _, p := range rows {
		ids = append(ids, p.Members...)
	}

	return ids
}

// pinRule returns the decoded pin of a live entry, as the delivery code
// reads it.
func pinRule(e pinfile.Entry) reviews.PinRule {
	return reviews.PinRule{Rule: e.Rule, Region: e.Region, Regions: e.Regions, Note: e.Note}
}
