package storetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.OpenMemory()
	require.NoError(t, err)

	t.Cleanup(func() { _ = st.Close() })

	return st
}

func TestInsertProposalStoresTheRowAsGiven(t *testing.T) {
	st := openStore(t)

	// A legacy trigger stamp (checked, version 0) and a second row with
	// the same signature and rule: ImportState would change or merge both.
	legacy := model.Proposal{
		Signature: "s1", Rule: "r", Region: "web/src/api", Regions: []string{"web/src/api", "api"},
		TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: 1700000000,
		Note: "n", Members: []int64{1, 2}, Agent: "claude/v4",
		Status: model.ProposalApplied, CreatedAt: 100,
	}
	twin := legacy
	twin.Note = "twin"

	require.NoError(t, InsertProposal(st, &legacy))
	require.NoError(t, InsertProposal(st, &twin))

	require.NotZero(t, legacy.ID, "the ID goes back into the caller's row")
	require.NotEqual(t, legacy.ID, twin.ID, "a twin row gets its own ID")

	got, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{twin, legacy}, got, "both rows are stored exactly as given")
}

func TestInsertProposalRecordsOnlyTheFixtureMark(t *testing.T) {
	st := openStore(t)

	for _, sig := range []string{"s1", "s2"} {
		p := model.Proposal{Signature: sig, Rule: "r", Note: "n", Status: model.ProposalProposed}
		require.NoError(t, InsertProposal(st, &p))
	}

	sigs, err := st.DistilledSignatures()
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{FixtureMark: true}, sigs,
		"the evidence groups of the rows stay unmarked")

	n, err := st.DistilledCount()
	require.NoError(t, err)
	assert.Equal(t, 1, n, "all calls update the same fixture mark")
}

func TestMarkDistilledUpdatesTheMarkOfASignature(t *testing.T) {
	st := openStore(t)

	require.NoError(t, MarkDistilled(st, "abc123", "v2/pkg", 100))
	require.NoError(t, MarkDistilled(st, "abc123", "v3/pkg", 200))

	state, err := st.ExportState()
	require.NoError(t, err)
	assert.Equal(t, []store.DistilledMark{{Signature: "abc123", Region: "v3/pkg", At: 200}}, state.Distilled)
	assert.Empty(t, state.Proposals, "a mark alone stores no proposal")
}
