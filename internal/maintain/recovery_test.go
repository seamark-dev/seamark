package maintain

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// partial commits the plan of req while another writer appends a line to
// the written file and the ledger write fails. The restore is then
// refused, and the result carries a recovery.
func partial(t *testing.T, f *fixture, req Request, tail string) (*Plan, Result) {
	t.Helper()

	p := f.plan(f.deps(), req)
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

	deps := f.deps()
	deps.Files = &fileSeam{after: func() { f.write(string(p.Change.After)+tail, f.mode()) }}
	deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	require.True(t, res.Partial())
	require.Equal(t, refusedRestore, res.RestoreRefused)
	require.NotNil(t, res.Recovery)

	return p, res
}

func TestRecoveryCompletesARetargetWhileTheProofHolds(t *testing.T) {
	f := retargetFixture(t)
	p, res := partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

	r := res.Recovery
	assert.Equal(t, ActionRetarget, r.Action)
	assert.Equal(t, p.Request, r.Request)
	assert.Equal(t, p.Change.Before, r.Before)
	assert.Equal(t, p.Change.After, r.After)
	assert.Equal(t, p.Retarget, r.Retarget)

	rp, err := PlanRecovery(f.deps(), r)
	require.NoError(t, err)
	assert.True(t, rp.Confirmable, "blocked: %v", rp.Blocked)
	assert.Same(t, r, rp.Recovery)
	assert.Nil(t, rp.Change, "a recovery never changes the file")
	assert.Equal(t, p.Retarget, rp.Retarget)
	assert.Equal(t, []string{noteRecovery}, rp.Notes)
	require.Len(t, rp.Items, 1)
	assert.Equal(t, ItemReady, rp.Items[0].Status)

	_, err = Commit(f.deps(), rp)
	require.Error(t, err, "a recovery plan never goes through Commit")

	lessons := f.lessons()

	done, err := CommitRecovery(f.deps(), rp)
	require.NoError(t, err)
	assert.True(t, done.LedgerDone)
	assert.False(t, done.FileWritten)
	assert.Equal(t, []int64{1}, done.Moved)
	assert.Equal(t, noteRecovered, done.Note)
	assert.Nil(t, done.Recovery)
	assert.Equal(t, lessons, f.lessons(), "the file stays as it is")
	assert.Equal(t, strings.Replace(retargetLedger,
		`p1 applied "guard-empty-datasets" region="" regions=[]`,
		`p1 applied "guard-empty-datasets" region="scripts" regions=["scripts"]`, 1), f.ledger())

	// A second completion finds the rows moved: no blind repetition.
	ledger := f.ledger()

	_, err = CommitRecovery(f.deps(), rp)
	require.ErrorIs(t, err, ErrStale)
	assert.Equal(t, ledger, f.ledger())
}

func TestRecoveryCompletesAPrune(t *testing.T) {
	f := pruneFixture(t)
	_, res := partial(t, f, f.request(ActionPrune, "ruff-unused-import@scripts"), "# another writer\n")

	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	require.True(t, rp.Confirmable, "blocked: %v", rp.Blocked)

	done, err := CommitRecovery(f.deps(), rp)
	require.NoError(t, err)
	assert.True(t, done.LedgerDone)
	assert.Equal(t, []int64{1}, done.Moved)
	assert.Equal(t, strings.Replace(pruneLedger, "p1 applied", "p1 superseded", 1), f.ledger())
	assert.Equal(t, prunedP1+"# another writer\n", f.lessons())
}

func TestRecoveryIsRefusedWithoutProof(t *testing.T) {
	cases := []struct {
		name   string
		prune  bool
		mutate func(t *testing.T, f *fixture)
		reason string
	}{
		{
			name: "the retargeted pin was edited", reason: reasonTargetGone,
			mutate: func(t *testing.T, f *fixture) {
				f.write(strings.Replace(f.lessons(), "HAND EDITED", "EDITED AGAIN", 1), 0o600)
			},
		},
		{
			name: "the earlier pin is back", reason: reasonOldKeyBack + "; " + reasonTargetGone,
			mutate: func(t *testing.T, f *fixture) { f.write(retargetLessons, 0o600) },
		},
		{
			name: "the pruned pin is back", prune: true, reason: reasonPrunedBack,
			mutate: func(t *testing.T, f *fixture) { f.write(pruneLessons, 0o640) },
		},
		{
			name: "the row changed", reason: "p1: " + reasonRowChanged,
			mutate: func(t *testing.T, f *fixture) { f.supersede(1) },
		},
		{
			name: "the pin file cannot be read", reason: reasonFileUnread,
			mutate: func(t *testing.T, f *fixture) { f.write(f.lessons()+"hook_delivery: sometimes\n", 0o600) },
		},
		{
			// The reader delivers the pruned pin through an alias, and no
			// listed entry shows it. The listing then proves no absence.
			name: "an alias delivers the pruned pin", prune: true, reason: reasonFileUnread,
			mutate: func(t *testing.T, f *fixture) {
				f.write("defs: &p\n  - rule: ruff-unused-import\n    region: scripts\n"+
					"    note: Remove unused imports in scripts.\npin: *p\n", 0o640)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				f   *fixture
				req Request
			)

			if tc.prune {
				f = pruneFixture(t)
				req = f.request(ActionPrune, "ruff-unused-import@scripts")
			} else {
				f = retargetFixture(t)
				req = f.request(ActionRetarget, "Guard-Empty-Datasets")
			}

			_, res := partial(t, f, req, "# another writer\n")
			tc.mutate(t, f)

			ledger := f.ledger()

			rp, err := PlanRecovery(f.deps(), res.Recovery)
			require.NoError(t, err)
			assert.False(t, rp.Confirmable)
			assert.Equal(t, ItemBlocked, rp.Items[0].Status)
			assert.Equal(t, tc.reason, rp.Items[0].Reason)
			assert.Contains(t, rp.Blocked, reasonUnproven)

			_, err = CommitRecovery(f.deps(), rp)
			require.ErrorIs(t, err, ErrNotConfirmable)
			assert.Equal(t, ledger, f.ledger())
		})
	}
}

func TestRecoveryChecksEachItemOnItsOwn(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, f *fixture)
		reasons []string
	}{
		{
			name: "only the two-areas target was edited",
			mutate: func(t *testing.T, f *fixture) {
				f.write(replaceOnce(t, f.lessons(), "note: Two areas.", "note: Two areas, edited."), 0o600)
			},
			reasons: []string{reasonTargetGone, ""},
		},
		{
			name:    "only the guard-empty-datasets row changed",
			mutate:  func(t *testing.T, f *fixture) { f.supersede(1) },
			reasons: []string{"", "p1: " + reasonRowChanged},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := retargetFixture(t)

			// The selection order (p5, p1) differs from the ID order and from
			// the file order, so a proof that matched items by position in
			// another order would blame the wrong entry.
			_, res := partial(t, f, f.request(ActionRetarget, "two-areas", "Guard-Empty-Datasets"), "# another writer\n")
			tc.mutate(t, f)

			rp, err := PlanRecovery(f.deps(), res.Recovery)
			require.NoError(t, err)
			assert.False(t, rp.Confirmable)
			require.Len(t, rp.Items, len(tc.reasons))

			for i, want := range tc.reasons {
				status := ItemReady
				if want != "" {
					status = ItemBlocked
				}

				assert.Equal(t, status, rp.Items[i].Status, "item %d", i)
				assert.Equal(t, want, rp.Items[i].Reason, "item %d", i)
			}
		})
	}
}

func TestRecoveryRefusesATargetKeyThatAnotherRowNowRecords(t *testing.T) {
	f := retargetFixture(t)

	// A pending row with the target key does not block the plan, because
	// the plan checks the keys of applied rows only (D-9 (6)).
	f.propose(model.Proposal{Signature: "s7", Rule: "guard-empty-datasets", Region: "scripts", Note: "A twin.",
		Members: []int64{1}, Status: model.ProposalProposed})

	_, res := partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	require.True(t, rp.Confirmable, "a pending row does not block the recovery either: %v", rp.Blocked)

	// The Pins view sends the written pin to its pending twin. After the
	// user records the twin as applied, a completion would give two
	// applied proposals one key.
	moved, err := f.st.SetProposalStatus([]int64{7}, model.ProposalApplied)
	require.NoError(t, err)
	require.Equal(t, 1, moved)

	ledger := f.ledger()

	again, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	assert.False(t, again.Confirmable)
	assert.Equal(t, ItemBlocked, again.Items[0].Status)
	assert.Equal(t, reasonKeyTaken, again.Items[0].Reason)
	assert.Contains(t, again.Blocked, reasonUnproven)

	_, err = CommitRecovery(f.deps(), again)
	require.ErrorIs(t, err, ErrNotConfirmable)

	// The recovery preview from before the twin was recorded is stale.
	_, err = CommitRecovery(f.deps(), rp)
	require.ErrorIs(t, err, ErrStale)
	assert.Equal(t, ledger, f.ledger(), "p1 keeps its earlier regions")
}

func TestRecoveryRefusesAProofThatBrokeAfterThePreview(t *testing.T) {
	f := retargetFixture(t)
	_, res := partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	require.True(t, rp.Confirmable)

	t.Run("an unrelated edit", func(t *testing.T) {
		f.write(f.lessons()+"# one more line\n", 0o600)

		_, err := CommitRecovery(f.deps(), rp)
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "the pin file changed")
		assert.Equal(t, retargetLedger, f.ledger())
	})

	t.Run("the gate turned off", func(t *testing.T) {
		rp, err := PlanRecovery(f.deps(), res.Recovery)
		require.NoError(t, err)

		f.gate(false)
		defer f.gate(true)

		_, err = CommitRecovery(f.deps(), rp)
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "distill.write changed")

		off, err := PlanRecovery(f.deps(), res.Recovery)
		require.NoError(t, err)
		assert.False(t, off.Confirmable)

		_, err = CommitRecovery(f.deps(), off)
		require.ErrorIs(t, err, ErrWriteDisabled)
		assert.Equal(t, retargetLedger, f.ledger())
	})
}

func TestRecoveryKeepsItselfWhenTheLedgerFailsAgain(t *testing.T) {
	f := retargetFixture(t)
	_, res := partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)

	deps := f.deps()
	deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}

	again, err := CommitRecovery(deps, rp)
	require.NoError(t, err)
	require.EqualError(t, again.LedgerErr, "injected: database is locked")
	assert.False(t, again.LedgerDone)
	assert.Same(t, res.Recovery, again.Recovery)
	assert.Equal(t, noteRecoverFailed, again.Note)
	require.Len(t, again.Observed, 1)
	assert.Equal(t, retargetLedger, f.ledger())
}

func TestRecoveryRefusesAMalformedProof(t *testing.T) {
	f := retargetFixture(t)
	_, res := partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

	broken := *res.Recovery
	broken.Edits = nil

	rp, err := PlanRecovery(f.deps(), &broken)
	require.NoError(t, err)
	assert.False(t, rp.Confirmable)
	assert.Equal(t, reasonUnproven, rp.Items[0].Reason)

	_, err = PlanRecovery(f.deps(), nil)
	require.EqualError(t, err, "maintain: no recovery")

	deps := f.deps()
	deps.Reader = nil

	_, err = PlanRecovery(deps, res.Recovery)
	require.ErrorIs(t, err, ErrNoLedger)
}

func TestAfterARestartAbsenceNeverSupersedes(t *testing.T) {
	t.Run("prune", func(t *testing.T) {
		f := pruneFixture(t)
		partial(t, f, f.request(ActionPrune, "ruff-unused-import@scripts"), "# another writer\n")

		// A new session has no recovery: it sees only the mismatch.
		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.NoError(t, in.LinkErr)

		var absent []int64
		for _, p := range in.Links.NotPresent {
			absent = append(absent, p.ID)
		}

		assert.Equal(t, []int64{1, 3}, absent, "p1 shows as not present, not as superseded")
		assert.Empty(t, in.Superseded)
		assert.Equal(t, pruneLedger, f.ledger())
	})

	t.Run("retarget", func(t *testing.T) {
		f := retargetFixture(t)
		partial(t, f, f.request(ActionRetarget, "Guard-Empty-Datasets"), "# another writer\n")

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		assert.Equal(t, pinfile.LinkNotPresent, in.Links.ByProposal[1])

		// The written pin has no linked proposal now, so the same edit
		// cannot be planned again blindly.
		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Contains(t, p.Items[0].Reason, "no linked proposal")
		assert.False(t, p.Confirmable)
	})
}
