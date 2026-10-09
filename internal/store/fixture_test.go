package store

import "github.com/seamark-dev/seamark/internal/model"

// The two methods in this file build fixture rows for the tests of this
// package. No production code calls them, so they live in a test file and
// stay out of the production API. The tests of other packages use the
// storetest package, because a test file of this package is not visible
// to them.

// InsertProposal stores one proposal row exactly as given and sets p.ID.
// It records no distillation mark, so each test controls the marks itself.
func (s *Store) InsertProposal(p *model.Proposal) error {
	return insertProposal(s.db, p)
}

// MarkDistilled records the distillation mark of one evidence group.
// SaveDistilledGroup without proposals runs the same upsert as the
// distiller, so the tests check the production statement.
func (s *Store) MarkDistilled(signature, region string, at int64) error {
	_, err := s.SaveDistilledGroup(signature, region, at, nil)

	return err
}
