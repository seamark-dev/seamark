package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// pendingProposal stores one proposal and returns it with its ID.
func pendingProposal(t *testing.T, s *Store, signature, rule, status string) model.Proposal {
	t.Helper()

	p := model.Proposal{Signature: signature, Rule: rule, Note: "n", Members: []int64{1, 2}, Status: status}
	require.NoError(t, s.InsertProposal(&p))

	return p
}

// pendingRules lists the rules of the stored proposals in one state.
func pendingRules(t *testing.T, s *Store, status string) []string {
	t.Helper()

	ps, err := s.Proposals(status)
	require.NoError(t, err)

	rules := make([]string, 0, len(ps))
	for _, p := range ps {
		rules = append(rules, p.Rule)
	}

	return rules
}

func TestPendingProposalsOutsideListsWhatThePruneDeletes(t *testing.T) {
	s := openTestStore(t)

	pendingProposal(t, s, "live", "kept", model.ProposalProposed)
	stale := pendingProposal(t, s, "gone", "stale-a", model.ProposalProposed)
	staleToo := pendingProposal(t, s, "gone", "stale-b", model.ProposalProposed)
	pendingProposal(t, s, "gone", "decided", model.ProposalDismissed)

	live := map[string]bool{"live": true}

	listed, err := s.PendingProposalsOutside(live)
	require.NoError(t, err)
	require.Len(t, listed, 2, "decided rows and live rows are not listed")
	assert.Equal(t, []int64{stale.ID, staleToo.ID}, []int64{listed[0].ID, listed[1].ID}, "ID order")
	assert.Equal(t, "stale-a", listed[0].Rule)

	assert.Equal(t, []string{"stale-b", "stale-a", "kept"}, pendingRules(t, s, model.ProposalProposed),
		"listing writes nothing")

	// The listed rows are exactly the rows the legacy prune deletes.
	pruned, err := s.PruneStaleProposals(live)
	require.NoError(t, err)
	assert.Equal(t, len(listed), pruned)
}

func TestPendingProposalsOutsideWorksReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)
	pendingProposal(t, s, "gone", "stale", model.ProposalProposed)
	require.NoError(t, s.Close())

	ro, err := OpenReadOnly(path)
	require.NoError(t, err)

	defer func() { _ = ro.Close() }()

	listed, err := ro.PendingProposalsOutside(nil)
	require.NoError(t, err)
	assert.Len(t, listed, 1)
}

func TestDeletePendingProposalsDeletesOnlyListedPendingRows(t *testing.T) {
	s := openTestStore(t)

	planned := pendingProposal(t, s, "gone", "planned", model.ProposalProposed)
	decided := pendingProposal(t, s, "gone", "decided-later", model.ProposalProposed)
	pendingProposal(t, s, "gone", "created-later", model.ProposalProposed)

	// Another writer decides one planned row before the cleanup runs.
	moved, err := s.SetProposalStatus([]int64{decided.ID}, model.ProposalDismissed)
	require.NoError(t, err)
	require.Equal(t, 1, moved)

	deleted, err := s.DeletePendingProposals([]int64{planned.ID, decided.ID})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "only the still-pending planned row goes")

	assert.Equal(t, []string{"created-later"}, pendingRules(t, s, model.ProposalProposed),
		"a row outside the plan survives even with a stale signature")
	assert.Equal(t, []string{"decided-later"}, pendingRules(t, s, model.ProposalDismissed),
		"a decision is memory")

	deleted, err = s.DeletePendingProposals(nil)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeletePendingProposalsHandlesLongLists(t *testing.T) {
	s := openTestStore(t)

	ids := make([]int64, 0, pendingChunk+20)

	for range pendingChunk + 20 {
		ids = append(ids, pendingProposal(t, s, "gone", "r", model.ProposalProposed).ID)
	}

	deleted, err := s.DeletePendingProposals(ids)
	require.NoError(t, err)
	assert.Equal(t, len(ids), deleted, "the list spans several statements")
	assert.Empty(t, pendingRules(t, s, model.ProposalProposed))
}

func TestDeletePendingProposalsFailsOnAReadOnlyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)
	p := pendingProposal(t, s, "gone", "stale", model.ProposalProposed)
	require.NoError(t, s.Close())

	ro, err := OpenReadOnly(path)
	require.NoError(t, err)

	defer func() { _ = ro.Close() }()

	_, err = ro.DeletePendingProposals([]int64{p.ID})
	require.Error(t, err)

	listed, err := ro.PendingProposalsOutside(nil)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "a failed cleanup removes nothing")
}

func TestSaveDistilledGroupIfNewRecordsOnce(t *testing.T) {
	s := openTestStore(t)

	first := []model.Proposal{{Signature: "sig", Rule: "first", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalProposed}}

	saved, err := s.SaveDistilledGroupIfNew("sig", "api", 10, first)
	require.NoError(t, err)
	require.Len(t, saved, 1)
	assert.Positive(t, saved[0].ID)

	marks, err := s.DistilledSignatures()
	require.NoError(t, err)
	assert.True(t, marks["sig"])

	// A second writer with the same group stores nothing.
	second := []model.Proposal{{Signature: "sig", Rule: "second", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalProposed}}

	_, err = s.SaveDistilledGroupIfNew("sig", "api", 20, second)
	require.ErrorIs(t, err, ErrAlreadyDistilled)
	assert.Zero(t, second[0].ID, "the refused proposal is not inserted")
	assert.Equal(t, []string{"first"}, pendingRules(t, s, model.ProposalProposed))

	// A zero-proposal group is still recorded, like SaveDistilledGroup.
	_, err = s.SaveDistilledGroupIfNew("empty", "", 30, nil)
	require.NoError(t, err)

	marks, err = s.DistilledSignatures()
	require.NoError(t, err)
	assert.True(t, marks["empty"])
}

func TestSaveDistilledGroupIfNewRollsBackAFailedInsert(t *testing.T) {
	s := openTestStore(t)

	tooMany := make([]string, model.MaxTriggerPaths+1)
	for i := range tooMany {
		tooMany[i] = "a.go"
	}

	_, err := s.SaveDistilledGroupIfNew("sig", "", 10, []model.Proposal{
		{Signature: "sig", Rule: "ok", Note: "n", Status: model.ProposalProposed},
		{Signature: "sig", Rule: "bad", Note: "n", TriggerPaths: tooMany, Status: model.ProposalProposed},
	})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrAlreadyDistilled))

	marks, err := s.DistilledSignatures()
	require.NoError(t, err)
	assert.Empty(t, marks, "the mark rolls back with the proposals")
	assert.Empty(t, pendingRules(t, s, model.ProposalProposed))
}
