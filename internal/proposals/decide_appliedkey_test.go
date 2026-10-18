package proposals

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// These tests cover the refusal of a pending proposal whose pin key an
// applied proposal already records (ErrAppliedKey). Pins blocks a pin
// that two applied proposals record, so neither apply nor the record of
// an existing pin may make a second applied row for one key.

// appliedPinLessons holds the pin of the applied ttl-monotonic-clock row
// and the hand-written pin of handLessons.
const appliedPinLessons = handLessons + "  - rule: ttl-monotonic-clock\n    regions: [api, web]\n    note: Applied.\n"

func TestApplyRefusesTheKeyOfAnAppliedProposal(t *testing.T) {
	// Before, a selected row whose pin the file held was recorded as
	// applied next to the applied row. A row without that pin got a
	// second entry, or one entry that two applied rows record. The rule
	// case and the region order do not change the key.
	tests := []struct {
		name    string
		write   bool
		lessons string
	}{
		{name: "gate on, the pin is in the file", write: true, lessons: appliedPinLessons},
		{name: "gate off, the pin is in the file", write: false, lessons: appliedPinLessons},
		{name: "gate on, the pin left the file", write: true, lessons: handLessons},
		{name: "gate off, the pin left the file", write: false, lessons: handLessons},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.write)
			f.writeLessons(tt.lessons)

			applied := f.propose("ttl-monotonic-clock", "Use the monotonic clock.", model.ProposalApplied, "api", "web")
			same := f.proposeAs("s2", "TTL-Monotonic-Clock", "Compute expiry from a monotonic reading.", "web", "api")
			other := f.proposeAs("s3", "wrap-errors", "Wrap errors.", "api")
			selected := []int64{same.ID, other.ID}

			pv, err := PreviewApply(f.st, f.root, selected)
			require.ErrorIs(t, err, ErrAppliedKey)
			assert.NotErrorIs(t, err, ErrSharedKey)
			assert.NotErrorIs(t, err, ErrStale)
			assert.Nil(t, pv)
			assert.Equal(t, "p1 is already applied with this rule and regions; dismiss p2", err.Error())

			var taken *AppliedKeyError

			require.ErrorAs(t, err, &taken)
			assert.Equal(t, []AppliedKeyGroup{{Applied: []int64{applied.ID}, Pending: []int64{same.ID}}},
				taken.Groups, "the other key is not named")

			assert.Equal(t, tt.lessons, string(f.lessons()), "the pin file is unchanged")
			assert.Equal(t, selected, f.pending(selected...), "the ledger is unchanged")

			// The other row alone is a valid selection.
			_, err = PreviewApply(f.st, f.root, []int64{other.ID})
			require.NoError(t, err)
		})
	}
}

func TestReconcileRefusesTheKeyOfAnAppliedProposal(t *testing.T) {
	// The pin of the applied row is in the file, so the pin check alone
	// reads it as the pin of the pending row too. Before, the record made
	// two applied rows record that pin, and Pins then blocked it. Without
	// the pin, the refusal still wins over ErrNotPinned: its advice is the
	// final step. A note edited by hand and another region order keep the
	// pin key, so that entry is still the pin of p1.
	for name, lessons := range map[string]string{
		"the pin is in the file":          appliedPinLessons,
		"the pin left the file":           handLessons,
		"the pin note was edited by hand": handLessons + "  - rule: ttl-monotonic-clock\n    regions: [web, api]\n    note: Hand.\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			f.writeLessons(lessons)

			f.propose("ttl-monotonic-clock", "Use the monotonic clock.", model.ProposalApplied, "api", "web")
			same := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api", "web")

			pv, err := PreviewReconcile(f.st, f.root, []int64{same.ID})
			require.ErrorIs(t, err, ErrAppliedKey)
			assert.NotErrorIs(t, err, ErrNotPinned)
			assert.Nil(t, pv)
			assert.Equal(t, "p1 is already applied with this rule and regions; dismiss p2", err.Error())
			assert.Equal(t, lessons, string(f.lessons()), "the pin file is unchanged")
			assert.Equal(t, []int64{same.ID}, f.pending(same.ID), "the ledger is unchanged")
		})
	}
}

func TestAnExistingPinWithoutAnAppliedProposalKeepsTheRecord(t *testing.T) {
	// A hand-written entry, or an entry that only a dismissed or
	// superseded row shares, has no applied row. Apply then reports it in
	// AlreadyPinned, and the record moves the row to applied.
	f := newFixture(t, true)
	f.writeLessons(appliedPinLessons)

	f.propose("ttl-monotonic-clock", "Dismissed earlier.", model.ProposalDismissed, "api", "web")

	old := f.proposeAs("s0", "ttl-monotonic-clock", "Pruned earlier.", "api", "web")
	_, err := f.st.SetProposalStatus([]int64{old.ID}, model.ProposalSuperseded)
	require.NoError(t, err)

	p := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api", "web")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)
	assert.Equal(t, []int64{p.ID}, pv.AlreadyPinned)

	rv, err := PreviewReconcile(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, rv)
	require.NoError(t, err)
	assert.Equal(t, []int64{p.ID}, res.Moved)
	assert.Equal(t, appliedPinLessons, string(f.lessons()), "the record writes only the ledger")

	// p is applied now, so the next row with its key meets the refusal.
	next := f.proposeAs("s3", "ttl-monotonic-clock", "A third reading.", "api", "web")

	_, err = PreviewReconcile(f.st, f.root, []int64{next.ID})
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.Equal(t, "p3 is already applied with this rule and regions; dismiss p4", err.Error())
}

func TestTheAppliedKeyRefusalComesBeforeTheSharedKeyRefusal(t *testing.T) {
	// p2 and p4 share a key, and applied p1 has it too. Apply one of them
	// meets the applied key refusal next, so the first refusal already
	// asks to dismiss both.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	f.propose("ttl-monotonic-clock", "Use the monotonic clock.", model.ProposalApplied, "api")
	f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")
	f.proposeAs("s3", "wrap-errors", "Wrap errors.", "api")
	f.proposeAs("s4", "ttl-monotonic-clock", "Never use the wall clock for TTLs.", "api")

	_, err := PreviewApply(f.st, f.root, []int64{2, 3, 4})
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.NotErrorIs(t, err, ErrSharedKey)
	assert.Equal(t, "p1 is already applied with this rule and regions; dismiss p2 and p4", err.Error())

	var taken *AppliedKeyError

	require.ErrorAs(t, err, &taken)
	assert.Equal(t, []int64{2, 4}, taken.Pending())
}

func TestAppliedKeyRefusalLinksAnAppliedRowByItsStoredFields(t *testing.T) {
	// The applied row holds Region api next to Regions ["*"]. Pins links
	// it by its stored fields (api), so a pending api row would make the
	// second applied row of that key.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	odd := model.Proposal{Signature: "sig-odd", Rule: "odd-fields", Region: "api", Regions: []string{"*"},
		Note: "Odd.", Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalApplied}
	require.NoError(t, storetest.InsertProposal(f.st, &odd))

	p := f.proposeAs("s2", "odd-fields", "Even.", "api")

	_, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.Equal(t, "p1 is already applied with this rule and regions; dismiss p2", err.Error())
}

func TestAppliedKeyErrorNamesEachGroup(t *testing.T) {
	tests := []struct {
		name   string
		groups []AppliedKeyGroup
		want   string
	}{
		{
			name:   "one row",
			groups: []AppliedKeyGroup{{Applied: []int64{1}, Pending: []int64{3}}},
			want:   "p1 is already applied with this rule and regions; dismiss p3",
		},
		{
			name:   "two pending rows",
			groups: []AppliedKeyGroup{{Applied: []int64{1}, Pending: []int64{3, 5}}},
			want:   "p1 is already applied with this rule and regions; dismiss p3 and p5",
		},
		{
			name:   "two applied rows",
			groups: []AppliedKeyGroup{{Applied: []int64{1, 2}, Pending: []int64{3}}},
			want:   "p1 and p2 are already applied with this rule and regions; dismiss p3",
		},
		{
			name: "two keys",
			groups: []AppliedKeyGroup{
				{Applied: []int64{2}, Pending: []int64{4}},
				{Applied: []int64{1}, Pending: []int64{5, 6}},
			},
			want: "p2 is already applied with the rule and regions of p4; p1 is already applied with the rule " +
				"and regions of p5 and p6; dismiss p4, p5 and p6",
		},
		{
			name: "no group",
			want: "selected proposals have the rule and regions of an applied proposal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &AppliedKeyError{Groups: tt.groups}

			assert.Equal(t, tt.want, err.Error())
			require.ErrorIs(t, err, ErrAppliedKey)
			assert.NotErrorIs(t, err, ErrSharedKey)
			assert.NotErrorIs(t, err, ErrStale)
		})
	}
}

func TestCheckAppliedKeysReadsOnlyTheRowsItGets(t *testing.T) {
	// The console checks its ledger snapshot with this rule, so the rule
	// needs no store. The applied rows link by their stored fields, and
	// the rule case and the region order do not change the key.
	applied := []model.Proposal{
		{ID: 3, Rule: "wrap-errors", Region: "api", Status: model.ProposalApplied},
		{ID: 1, Rule: "wrap-errors", Regions: []string{"api"}, Status: model.ProposalApplied},
		{ID: 2, Rule: "ttl-monotonic-clock", Regions: []string{"api", "web"}, Status: model.ProposalApplied},
	}

	pending := []model.Proposal{
		{ID: 4, Rule: "TTL-Monotonic-Clock", Regions: []string{"web", "api"}},
		{ID: 5, Rule: "close-handles", Regions: []string{"db"}},
		{ID: 6, Rule: "wrap-errors", Regions: []string{"api"}},
	}

	taken := CheckAppliedKeys(applied, pending)
	require.NotNil(t, taken)
	assert.Equal(t, []AppliedKeyGroup{
		{Applied: []int64{2}, Pending: []int64{4}},
		{Applied: []int64{1, 3}, Pending: []int64{6}},
	}, taken.Groups, "the groups follow the lowest pending ID, and the applied IDs keep ID order")

	assert.Nil(t, CheckAppliedKeys(applied, pending[1:2]), "no applied row has the key of p5")
	assert.Nil(t, CheckAppliedKeys(nil, pending), "no applied row, no refusal")
}

func TestApplyRefusalNamesEveryKeyOfAnAppliedProposal(t *testing.T) {
	// The groups follow the lowest pending ID. The applied IDs of a key
	// keep ID order, although the store lists them newest first. An old
	// ledger can hold two applied rows of one key, here p1 and p3.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	f.propose("wrap-errors", "Wrap errors.", model.ProposalApplied, "api")
	f.propose("ttl-monotonic-clock", "Use the monotonic clock.", model.ProposalApplied, "db")

	again := f.proposeAs("s3", "wrap-errors", "Wrap errors again.", "api")
	_, err := f.st.SetProposalStatus([]int64{again.ID}, model.ProposalApplied)
	require.NoError(t, err)

	f.proposeAs("s4", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "db")
	f.proposeAs("s5", "wrap-errors", "Wrap every error.", "api")
	f.proposeAs("s6", "close-handles", "Close handles.", "db")

	_, err = PreviewApply(f.st, f.root, []int64{4, 5, 6})
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.Equal(t, "p2 is already applied with the rule and regions of p4; p1 and p3 are already applied "+
		"with the rule and regions of p5; dismiss p4 and p5", err.Error())

	var taken *AppliedKeyError

	require.ErrorAs(t, err, &taken)
	assert.Equal(t, []AppliedKeyGroup{
		{Applied: []int64{2}, Pending: []int64{4}},
		{Applied: []int64{1, 3}, Pending: []int64{5}},
	}, taken.Groups)
	assert.Equal(t, []int64{1, 2, 3}, taken.Applied())
	assert.Equal(t, []int64{4, 5}, taken.Pending())
}

func TestApplyCommitRefusesAKeyAppliedAfterThePreview(t *testing.T) {
	// The fingerprints cover only the selected rows. Another writer
	// applies p1 with the key of p2 after the preview, so the commit
	// refuses p2 as stale and names the applied row.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api")
	same := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")

	pv, err := PreviewApply(f.st, f.root, []int64{same.ID})
	require.NoError(t, err)

	_, err = f.st.SetProposalStatus([]int64{first.ID}, model.ProposalApplied)
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrStale)
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.Equal(t, "the ledger, the pin file, or the write gate changed since the preview: "+
		"p1 is already applied with this rule and regions; dismiss p2", err.Error())

	var taken *AppliedKeyError

	require.ErrorAs(t, err, &taken)
	assert.Equal(t, []int64{same.ID}, taken.Pending())

	assert.False(t, res.FileWritten)
	assert.Equal(t, handLessons, string(f.lessons()), "the pin file is unchanged")
	assert.Equal(t, []int64{same.ID}, f.pending(same.ID), "the ledger is unchanged")
}

func TestReconcileCommitRefusesAKeyAppliedAfterThePreview(t *testing.T) {
	// Pins reads p1 and p2 as pending matches of one entry. A record of
	// p1 by another writer after the preview of p2 makes p2 a second
	// applied row of that entry, so the commit refuses it.
	f := newFixture(t, false)
	f.writeLessons(handLessons + "  - rule: ttl-monotonic-clock\n    region: api\n    note: By hand.\n")

	first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api")
	same := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")

	rv, err := PreviewReconcile(f.st, f.root, []int64{same.ID})
	require.NoError(t, err)

	_, err = f.st.SetProposalStatus([]int64{first.ID}, model.ProposalApplied)
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, rv)
	require.ErrorIs(t, err, ErrStale)
	require.ErrorIs(t, err, ErrAppliedKey)
	assert.Empty(t, res.Moved)
	assert.Equal(t, []int64{same.ID}, f.pending(same.ID), "the ledger is unchanged")
}

// appliedReadLedger fails the read of the applied rows.
type appliedReadLedger struct {
	Ledgerer
	err error
}

func (l appliedReadLedger) Proposals(string) ([]model.Proposal, error) {
	return nil, l.err
}

func TestAnAppliedReadFailureStopsTheDecision(t *testing.T) {
	// No applied rows is not proven when the read fails, so apply and the
	// record stop. The failure is not stale: a new preview meets it too.
	f := newFixture(t, true)
	f.writeLessons(handLessons + "  - rule: wrap-errors\n    region: api\n    note: By hand.\n")

	p := f.proposeAs("s1", "wrap-errors", "Wrap errors.", "api")
	busy := errors.New("database is locked")
	ls := appliedReadLedger{Ledgerer: f.st, err: busy}

	_, err := PreviewApply(ls, f.root, []int64{p.ID})
	require.ErrorIs(t, err, busy)
	assert.Equal(t, "read the applied proposals: database is locked", err.Error())

	_, err = PreviewReconcile(ls, f.root, []int64{p.ID})
	require.ErrorIs(t, err, busy)

	rv, err := PreviewReconcile(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	_, err = Commit(ls, f.root, rv)
	require.ErrorIs(t, err, busy)
	assert.NotErrorIs(t, err, ErrStale)
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID), "the ledger is unchanged")
}
