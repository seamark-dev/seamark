// Package storetest builds index rows for the tests of other packages.
//
// The store package exports no fixture writer, because no production
// code needs one. A test file of the store package is not visible to
// other packages. So the fixture writers live here, and they use only
// the exported store API.
package storetest

import (
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// FixtureMark is the signature of the distillation mark that
// InsertProposal records. The signature of an evidence group holds hex
// digits only. The colon keeps FixtureMark apart from all such
// signatures. So the distiller never skips a real evidence group because
// of this mark.
const FixtureMark = "storetest:fixture"

// InsertProposal stores p as one proposal row and sets p.ID.
//
// It writes through SaveDistilledGroup, because that writer stores the
// row exactly as given. ImportState merges rows with an equal signature
// and rule, and it changes legacy trigger stamps. So ImportState cannot
// build the duplicate rows and the legacy rows that some tests need.
//
// SaveDistilledGroup also records a distillation mark. InsertProposal
// records it under FixtureMark, not under p.Signature, so the distiller
// still treats the evidence group of p as new. All calls update the same
// mark, so a store holds at most one fixture mark. A test that counts or
// exports the marks must allow for it.
//
// A distillation plan also reads the marks. So when the store holds no
// fixture mark yet, a call after a plan changes two inputs of the plan:
// the ledger and the distilled signatures. A test that must change only
// the ledger records FixtureMark with MarkDistilled before the plan.
func InsertProposal(st *store.Store, p *model.Proposal) error {
	saved, err := st.SaveDistilledGroup(FixtureMark, "", 0, []model.Proposal{*p})
	if err != nil {
		return err
	}

	// The store sets the ID on its copy of the row. The callers read the
	// ID from p, so the stored row goes back into p.
	*p = saved[0]

	return nil
}

// MarkDistilled records the distillation mark of one evidence group.
// SaveDistilledGroup without proposals runs the same upsert as the
// distiller, so a second call for a signature updates its region and time.
func MarkDistilled(st *store.Store, signature, region string, at int64) error {
	_, err := st.SaveDistilledGroup(signature, region, at, nil)

	return err
}
