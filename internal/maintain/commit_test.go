package maintain

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// retargetedP1 is retargetLessons after the retarget of p1 to [scripts]:
// only the region line of the reviewed entry changes. The rule spelling,
// the live note, the unknown key, the user comment, and the position stay.
var retargetedP1 = strings.Replace(retargetLessons,
	"  - rule: Guard-Empty-Datasets\n    region: '*'\n",
	"  - rule: Guard-Empty-Datasets\n    region: scripts\n", 1)

func TestCommitRetargetKeepsTheHandEditedPin(t *testing.T) {
	f := retargetFixture(t)
	history := f.history()

	// A delivered firing makes the log check stronger than "absent stays
	// absent".
	f.fire(reviews.PinRule{Rule: "guard-empty-datasets", Note: "Guard datasets for emptiness before reductions."},
		"2026-09-01T00:00:00Z")

	before := f.evidence()
	require.Len(t, before.findings, len(retargetFindings))
	require.NotEmpty(t, before.log)

	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	res, err := Commit(f.deps(), p)
	require.NoError(t, err)

	assert.True(t, res.FileWritten)
	require.NoError(t, res.FileErr)
	assert.True(t, res.LedgerDone)
	require.NoError(t, res.LedgerErr)
	assert.False(t, res.Restored)
	assert.False(t, res.Partial())
	assert.Nil(t, res.Recovery)
	assert.Equal(t, []int64{1}, res.Moved)
	assert.Equal(t, noteDone, res.Note)

	assert.Equal(t, retargetedP1, f.lessons())
	assert.Equal(t, os.FileMode(0o600), f.mode(), "the mode of the file stays")
	assert.Equal(t, strings.Replace(retargetLedger,
		`p1 applied "guard-empty-datasets" region="" regions=[]`,
		`p1 applied "guard-empty-datasets" region="scripts" regions=["scripts"]`, 1), f.ledger())
	assert.Equal(t, history, f.history(), "signatures, recorded notes, and cited findings stay")
	assert.Equal(t, before, f.evidence(), "the findings and the firing log stay (AC-5)")

	// The recorded note stays the historical note of the proposal.
	rows, err := f.st.ProposalsWithIDs([]int64{1})
	require.NoError(t, err)
	assert.Equal(t, "Guard datasets for emptiness before reductions.", rows[0].Note)

	// The pin keeps its link, now under the new key, and a second plan
	// finds it current.
	again := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
	assert.Equal(t, ItemNoOp, again.Items[0].Status)
	assert.Equal(t, reasonCurrent, again.Items[0].Reason)
}

func TestCommitRetargetsSeveralPinsInOneWrite(t *testing.T) {
	f := retargetFixture(t)

	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets", "two-areas", "scattered"))
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

	res, err := Commit(f.deps(), p)
	require.NoError(t, err)
	assert.True(t, res.LedgerDone)
	assert.Equal(t, []int64{1, 5, 6}, res.Moved)

	want := strings.Replace(retargetedP1,
		"  - rule: two-areas\n    region: api\n    note: Two areas.\n",
		"  - rule: two-areas\n    region: api\n    regions: [api, db]\n    note: Two areas.\n", 1)
	want = strings.Replace(want,
		"  - rule: scattered\n    region: a\n",
		"  - rule: scattered\n    region: '*'\n", 1)
	assert.Equal(t, want, f.lessons())

	assert.Equal(t, `p1 applied "guard-empty-datasets" region="scripts" regions=["scripts"] triggers=[] checked=0 v0
p2 applied "already-current" region="scripts" regions=[] triggers=[] checked=0 v0
p3 applied "aged-out" region="scripts" regions=[] triggers=[] checked=0 v0
p4 applied "missing-pin" region="" regions=[] triggers=[] checked=0 v0
p5 applied "two-areas" region="api" regions=["api" "db"] triggers=[] checked=0 v0
p6 applied "scattered" region="" regions=[] triggers=[] checked=0 v0
`, f.ledger(), "the ledger rows hold what the CLI retarget writes")
}

// pruneLessons holds one rule in two regions, each with its provenance
// comment, a hand-written pin, and settings around the list.
const pruneLessons = `threshold: 3
pin:
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)
  - rule: ruff-unused-import
    region: scripts
    note: Remove unused imports in scripts.
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p2)
  - rule: ruff-unused-import
    region: api
    note: Remove unused imports in api.
  - rule: hand-written
    region: web
    note: Keep this one.
mute:
  - rule: noisy
`

// pruneLedger is the ledger of pruneFixture before any change.
const pruneLedger = `p1 applied "ruff-unused-import" region="scripts" regions=[] triggers=[] checked=0 v0
p2 applied "ruff-unused-import" region="api" regions=[] triggers=[] checked=0 v0
p3 applied "ruff-unused-import" region="db" regions=[] triggers=[] checked=0 v0
`

// pruneFixture seeds pruneLessons with p1 and p2 linked, and p3 applied
// with the same rule but no entry in the file.
func pruneFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, retargetFindings)
	f.write(pruneLessons, 0o640)
	f.propose(
		model.Proposal{Signature: "s1", Rule: "ruff-unused-import", Region: "scripts",
			Note: "Remove unused imports in scripts.", Members: []int64{1, 2}, Agent: "claude/v2", Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "ruff-unused-import", Region: "api",
			Note: "Remove unused imports in api.", Members: []int64{3, 4}, Agent: "claude/v2", Status: model.ProposalApplied},
		model.Proposal{Signature: "s3", Rule: "ruff-unused-import", Region: "db",
			Note: "Remove unused imports in db.", Members: []int64{4}, Agent: "claude/v2", Status: model.ProposalApplied},
	)
	require.Equal(t, pruneLedger, f.ledger())

	return f
}

// prunedP1 is pruneLessons without the scripts pin and its provenance.
const prunedP1 = `threshold: 3
pin:
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p2)
  - rule: ruff-unused-import
    region: api
    note: Remove unused imports in api.
  - rule: hand-written
    region: web
    note: Keep this one.
mute:
  - rule: noisy
`

func TestCommitPruneRemovesOneOfTwoSameRulePins(t *testing.T) {
	f := pruneFixture(t)
	history := f.history()

	// The pruned pin fired before, so its delivery history is on disk.
	f.fire(reviews.PinRule{Rule: "ruff-unused-import", Region: "scripts", Note: "Remove unused imports in scripts."},
		"2026-09-01T00:00:00Z")

	before := f.evidence()
	require.Len(t, before.findings, len(retargetFindings))
	require.NotEmpty(t, before.log)

	p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import@scripts"))
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

	require.Len(t, p.Items, 1)
	it := p.Items[0]
	assert.Equal(t, ItemReady, it.Status)
	assert.Equal(t, []string{"scripts"}, it.From)
	require.Len(t, it.Kept, 1, "the api pin restates the rule and stays linked")
	assert.Equal(t, "api", it.Kept[0].Entry.Region)
	assert.Equal(t, "similar wording stays in 1 linked pin(s); similar wording does not prove the same coverage",
		it.Reason)
	assert.Equal(t, []store.RowGuard{store.NewRowGuard(it.Rows[0])}, p.Supersede)
	assert.Empty(t, p.Retarget)
	assert.Equal(t, [32]byte{}, p.Evidence, "a prune binds no evidence")

	res, err := Commit(f.deps(), p)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.True(t, res.LedgerDone)
	assert.Equal(t, []int64{1}, res.Moved)

	assert.Equal(t, prunedP1, f.lessons())
	assert.Equal(t, os.FileMode(0o640), f.mode())
	assert.Equal(t, strings.Replace(pruneLedger, "p1 applied", "p1 superseded", 1), f.ledger(),
		"only the pruned row moves; the same rule in other regions stays applied")
	assert.Equal(t, history, f.history(), "signatures, recorded notes, and cited findings stay")
	assert.Equal(t, before, f.evidence(), "the findings and the firing log stay (AC-5)")

	// The superseded row is history, read-only in the Pins view.
	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.Len(t, in.Superseded, 1)
	assert.Equal(t, int64(1), in.Superseded[0].ID)
}

func TestNewPlanPruneNamesOnlyRetainedAlternatives(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		kept  [][]string
	}{
		{name: "the partner stays", names: []string{"ruff-unused-import@api"}, kept: [][]string{{"scripts"}}},
		{
			name:  "both partners leave",
			names: []string{"ruff-unused-import@scripts", "ruff-unused-import@api"},
			kept:  [][]string{nil, nil},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := pruneFixture(t)

			p := f.plan(f.deps(), f.request(ActionPrune, tc.names...))
			require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
			require.Len(t, p.Items, len(tc.kept))

			for i, want := range tc.kept {
				var got []string
				for _, k := range p.Items[i].Kept {
					got = append(got, k.Entry.Region)
				}

				assert.Equal(t, want, got, "item %d", i)

				if want == nil {
					assert.Equal(t, reasonRetirement, p.Items[i].Reason, "no promise of preserved coverage")
				}
			}
		})
	}

	t.Run("an unlinked or absent pin is never an alternative", func(t *testing.T) {
		f := pruneFixture(t)

		// The hand-written twin has no ledger row, and p3 has no entry.
		f.write(strings.Replace(pruneLessons, "  - rule: hand-written\n",
			"  - rule: ruff-unused-import\n    region: lib\n    note: A hand-written twin.\n  - rule: hand-written\n", 1), 0o640)

		p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import@scripts", "ruff-unused-import@api"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

		for _, it := range p.Items {
			assert.Empty(t, it.Kept)
			assert.Equal(t, reasonRetirement, it.Reason)
		}
	})
}

func TestCommitRefusesStalePlans(t *testing.T) {
	cases := []struct {
		name   string
		action Action
		mutate func(f *fixture)
		reason string
	}{
		{
			name: "another pin edited", action: ActionRetarget, reason: "the pin file changed",
			mutate: func(f *fixture) {
				f.write(strings.Replace(retargetLessons, "note: Two areas.", "note: Two areas, edited.", 1), 0o600)
			},
		},
		{
			name: "the reviewed pin edited", action: ActionRetarget, reason: "the plan of entry 2 changed",
			mutate: func(f *fixture) {
				f.write(strings.Replace(retargetLessons, "HAND EDITED", "EDITED AGAIN", 1), 0o600)
			},
		},
		{
			name: "the row superseded elsewhere", action: ActionRetarget, reason: "the ledger rows changed",
			mutate: func(f *fixture) { f.supersede(1) },
		},
		{
			name: "the row retargeted elsewhere", action: ActionRetarget, reason: "the ledger rows changed",
			mutate: func(f *fixture) { f.retargetRow(1, "lib") },
		},
		{
			name: "the gate turned off", action: ActionRetarget, reason: "distill.write changed",
			mutate: func(f *fixture) { f.gate(false) },
		},
		{
			name: "the config broke", action: ActionPrune, reason: "distill.write changed",
			mutate: func(f *fixture) { f.config("distill: [\n") },
		},
		{
			name: "a cited finding aged out", action: ActionRetarget, reason: "the evidence or the index changed",
			mutate: func(f *fixture) {
				require.NoError(f.t, f.st.ReplaceLessons(nil, retargetFindings[:1]))
			},
		},
		{
			name: "the trigger question was answered", action: ActionRetarget, reason: "the evidence or the index changed",
			mutate: func(f *fixture) {
				// An extraction run answers the question of p1 through the
				// guarded save, as the CLI and the console do.
				rows, err := f.st.ProposalsWithIDs([]int64{1})
				require.NoError(f.t, err)
				require.Len(f.t, rows, 1)

				saved, err := f.st.SaveTriggerAnswer(store.TriggerAnswer{
					Guard: store.NewRowGuard(rows[0]), CheckedAt: 1_700_000_000, Version: 3,
				})
				require.NoError(f.t, err)
				require.True(f.t, saved)
			},
		},
		{
			name: "the row superseded before a prune", action: ActionPrune, reason: "the ledger rows changed",
			mutate: func(f *fixture) { f.supersede(1) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := retargetFixture(t)
			p := f.plan(f.deps(), f.request(tc.action, "Guard-Empty-Datasets"))
			require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

			tc.mutate(f)

			lessons, ledger := f.lessons(), f.ledger()

			res, err := Commit(f.deps(), p)
			require.ErrorIs(t, err, ErrStale)
			assert.Contains(t, err.Error(), tc.reason)
			assert.False(t, res.FileWritten)
			assert.Equal(t, lessons, f.lessons(), "no write")
			assert.Equal(t, ledger, f.ledger(), "no ledger change")
		})
	}

	t.Run("prune", func(t *testing.T) {
		pruneCases := []struct {
			name   string
			mutate func(f *fixture)
		}{
			{
				// An entry that disappeared before the apply is a conflict,
				// never an already-successful prune.
				name:   "the reviewed entry removed by hand",
				mutate: func(f *fixture) { f.write(prunedP1, 0o640) },
			},
			{
				// The preview named the api pin as the retained alternative.
				// It is no longer linked, so the reviewed claim is false.
				name:   "the retained partner superseded",
				mutate: func(f *fixture) { f.supersede(2) },
			},
		}

		for _, tc := range pruneCases {
			t.Run(tc.name, func(t *testing.T) {
				f := pruneFixture(t)
				p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import@scripts"))
				require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
				require.Len(t, p.Items[0].Kept, 1)

				tc.mutate(f)

				lessons, ledger := f.lessons(), f.ledger()

				res, err := Commit(f.deps(), p)
				require.ErrorIs(t, err, ErrStale)
				assert.Contains(t, err.Error(), "the plan of entry 1 changed")
				assert.False(t, res.FileWritten)
				assert.Empty(t, res.Moved)
				assert.Equal(t, lessons, f.lessons(), "no write")
				assert.Equal(t, ledger, f.ledger(), "no ledger change")
				assert.Contains(t, f.ledger(), `p1 applied "ruff-unused-import" region="scripts"`)
			})
		}
	})

	t.Run("the index was rebuilt", func(t *testing.T) {
		f := retargetFixture(t)
		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

		deps := f.deps()
		deps.Probe.IndexedAt++

		_, err := Commit(deps, p)
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "the evidence or the index changed")
		assert.Equal(t, retargetLessons, f.lessons())
	})
}

func TestCommitFailureBeforeTheReplaceChangesNothing(t *testing.T) {
	for _, action := range []Action{ActionRetarget, ActionPrune} {
		t.Run(action.String(), func(t *testing.T) {
			f := retargetFixture(t)
			p := f.plan(f.deps(), f.request(action, "Guard-Empty-Datasets"))

			files := &fileSeam{failBefore: errors.New("injected: disk full")}
			deps := f.deps()
			deps.Files = files

			res, err := Commit(deps, p)
			require.NoError(t, err, "the write was attempted, so the outcome is in the result")
			assert.False(t, res.FileWritten)
			require.EqualError(t, res.FileErr, "injected: disk full")
			assert.False(t, res.LedgerDone)
			assert.False(t, res.Partial())
			assert.Nil(t, res.Recovery)
			assert.Zero(t, files.restores)
			assert.Equal(t, noteFileFailed, res.Note)
			assert.Equal(t, retargetLessons, f.lessons())
			assert.Equal(t, retargetLedger, f.ledger())
		})
	}
}

func TestCommitReportsAFileThatChangedBeforeTheWrite(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	// Another writer edits the file between the revalidation and the
	// guarded write; the setup engine refuses it.
	other := retargetLessons + "# another writer\n"
	files := &fileSeam{}
	deps := f.deps()
	deps.Files = replaceFirst{inner: files, before: func() { f.write(other, 0o600) }}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	assert.False(t, res.FileWritten)
	require.ErrorIs(t, res.FileErr, ErrStale)
	require.ErrorIs(t, res.FileErr, pinfile.ErrStale)
	assert.Equal(t, other, f.lessons(), "the other writer's edit stays")
	assert.Equal(t, retargetLedger, f.ledger())
}

// replaceFirst runs before ahead of the inner replace.
type replaceFirst struct {
	inner  FileWriter
	before func()
}

func (r replaceFirst) Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error {
	r.before()

	return r.inner.Replace(reg, root, expect, after)
}

func (r replaceFirst) Restore(reg *integration.Registry, root string, written [32]byte, original []byte) error {
	return r.inner.Restore(reg, root, written, original)
}

func TestCommitRestoresWhenTheLedgerFails(t *testing.T) {
	t.Run("an injected writer error", func(t *testing.T) {
		f := retargetFixture(t)
		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

		ledger := &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}
		deps := f.deps()
		deps.Ledger = ledger

		res, err := Commit(deps, p)
		require.NoError(t, err)
		assert.True(t, res.FileWritten)
		require.EqualError(t, res.LedgerErr, "injected: database is locked")
		assert.False(t, res.LedgerDone)
		assert.True(t, res.Restored)
		require.NoError(t, res.RestoreErr)
		assert.False(t, res.Partial())
		assert.Nil(t, res.Recovery)
		assert.Equal(t, noteRestored, res.Note)
		assert.Equal(t, 1, ledger.reads)
		require.Len(t, res.Observed, 1)
		assert.Empty(t, res.Moved)

		assert.Equal(t, retargetLessons, f.lessons())
		assert.Equal(t, os.FileMode(0o600), f.mode())
		assert.Equal(t, retargetLedger, f.ledger())
	})

	t.Run("a failure inside the ledger transaction", func(t *testing.T) {
		f := pruneFixture(t)
		p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import@scripts"))
		f.failUpdates("status")

		res, err := Commit(f.deps(), p)
		require.NoError(t, err)
		assert.True(t, res.FileWritten)
		require.ErrorContains(t, res.LedgerErr, "injected ledger failure")
		assert.True(t, res.Restored)
		assert.False(t, res.Partial())
		assert.Equal(t, pruneLessons, f.lessons())
		assert.Equal(t, os.FileMode(0o640), f.mode())
		assert.Equal(t, pruneLedger, f.ledger())
	})
}

func TestCommitNeverRestoresOverAnotherWriter(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	other := retargetedP1 + "# another writer was here\n"
	files := &fileSeam{after: func() { f.write(other, 0o600) }}
	deps := f.deps()
	deps.Files = files
	deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.False(t, res.Restored)
	require.ErrorIs(t, res.RestoreErr, pinfile.ErrStale)
	assert.Equal(t, refusedRestore, res.RestoreRefused)
	assert.True(t, res.Partial())
	assert.Equal(t, noteRestoreFailed, res.Note)
	require.NotNil(t, res.Recovery)
	assert.Equal(t, 1, files.restores)

	assert.Equal(t, other, f.lessons(), "the later edit of another writer is never overwritten")
	assert.Equal(t, retargetLedger, f.ledger())
}

func TestCommitSeesALedgerCompletedByAnotherWriter(t *testing.T) {
	for _, action := range []Action{ActionRetarget, ActionPrune} {
		t.Run(action.String(), func(t *testing.T) {
			f := retargetFixture(t)
			p := f.plan(f.deps(), f.request(action, "Guard-Empty-Datasets"))

			// The guarded write lands, and then the connection reports a
			// failure: the rows hold their targets.
			files := &fileSeam{}
			deps := f.deps()
			deps.Files = files
			deps.Ledger = &ledgerSeam{Store: f.st, real: true, err: errors.New("injected: commit reply lost")}

			res, err := Commit(deps, p)
			require.NoError(t, err)
			assert.True(t, res.FileWritten)
			assert.True(t, res.LedgerDone)
			require.EqualError(t, res.LedgerErr, "injected: commit reply lost", "the error text stays visible")
			assert.False(t, res.Restored)
			assert.Zero(t, files.restores, "a completed ledger is never undone")
			assert.False(t, res.Partial())
			assert.Nil(t, res.Recovery)
			assert.Empty(t, res.Moved, "this commit did not see its own write succeed")
			assert.Equal(t, noteOtherWriter, res.Note)
			assert.Equal(t, string(p.Change.After), f.lessons())
		})
	}
}

func TestCommitReportsAFailedRestore(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	files := &fileSeam{restoreErr: errors.New("injected: permission denied")}
	deps := f.deps()
	deps.Files = files
	deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.False(t, res.Restored, "a failed restore is never reported as restored")
	require.EqualError(t, res.RestoreErr, "injected: permission denied")
	assert.Empty(t, res.RestoreRefused)
	assert.True(t, res.Partial())
	assert.Equal(t, noteRestoreFailed, res.Note)
	require.NotNil(t, res.Recovery)

	assert.Equal(t, retargetedP1, f.lessons())
	assert.Equal(t, retargetLedger, f.ledger())

	// The written file and the planned rows still prove the reviewed
	// result, so the completion can be confirmed.
	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	assert.True(t, rp.Confirmable, "blocked: %v", rp.Blocked)
}

func TestCommitDoesNotRestoreWithoutARereadOfTheRows(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	files := &fileSeam{}
	deps := f.deps()
	deps.Files = files
	deps.Ledger = &ledgerSeam{
		Store: f.st, err: errors.New("injected: database is locked"), readErr: errors.New("injected: disk I/O error"),
	}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	require.EqualError(t, res.ObservedErr, "injected: disk I/O error")
	assert.Nil(t, res.Observed)
	assert.Zero(t, files.restores, "an unread ledger never authorizes a restore")
	assert.False(t, res.Restored)
	assert.True(t, res.Partial())
	assert.Equal(t, noteReread, res.Note)
	require.NotNil(t, res.Recovery)
	assert.Equal(t, retargetedP1, f.lessons())

	// A later read of the rows can prove the reviewed result again.
	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	assert.True(t, rp.Confirmable, "blocked: %v", rp.Blocked)
}

func TestCommitDoesNotRestoreRowsInAnotherState(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	// Another writer supersedes the row after the revalidation; the
	// guarded transaction then refuses it.
	files := &fileSeam{}
	deps := f.deps()
	deps.Files = files
	deps.Ledger = &ledgerSeam{Store: f.st, before: func() { f.supersede(1) }}

	res, err := Commit(deps, p)
	require.NoError(t, err)
	require.ErrorIs(t, res.LedgerErr, store.ErrGuard)
	assert.Zero(t, files.restores)
	assert.False(t, res.Restored)
	assert.False(t, res.LedgerDone)
	assert.True(t, res.Partial())
	assert.Equal(t, noteOtherState, res.Note)
	require.Len(t, res.Observed, 1)
	assert.Equal(t, model.ProposalSuperseded, res.Observed[0].Status)
	require.NotNil(t, res.Recovery)
	assert.Equal(t, retargetedP1, f.lessons())

	// The recovery cannot prove the planned rows, so it is not offered as
	// a confirmable completion.
	rp, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)
	assert.False(t, rp.Confirmable)
	assert.Equal(t, "p1: "+reasonRowChanged, rp.Items[0].Reason)
}

func TestCommitObservesAWriteThatReportedAFailure(t *testing.T) {
	cases := []struct {
		name       string
		restoreErr error
		restored   bool
		recovery   bool
		lessons    string
		note       string
	}{
		{name: "the restore succeeds", restored: true, lessons: retargetLessons, note: noteFileRestored},
		{
			name: "the restore fails", restoreErr: errors.New("injected: permission denied"),
			recovery: true, lessons: retargetedP1, note: noteFileKept,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := retargetFixture(t)
			p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

			files := &fileSeam{failAfter: errors.New("injected: fsync failed"), restoreErr: tc.restoreErr}
			deps := f.deps()
			deps.Files = files
			deps.Ledger = &ledgerSeam{Store: f.st}

			res, err := Commit(deps, p)
			require.NoError(t, err)
			assert.True(t, res.FileWritten, "the file holds the new bytes despite the error")
			require.EqualError(t, res.FileErr, "injected: fsync failed")
			require.NoError(t, res.LedgerErr, "the ledger is never written after a failed file write")
			assert.Equal(t, tc.restored, res.Restored)
			assert.Equal(t, !tc.restored, res.Partial())
			assert.Equal(t, tc.recovery, res.Recovery != nil, "the session saw its own write land")
			assert.Equal(t, tc.note, res.Note, "the note names the file write, never the ledger")
			assert.Equal(t, tc.lessons, f.lessons())
			assert.Equal(t, retargetLedger, f.ledger())
		})
	}
}

func TestCommitTreatsAnUncheckedFileAsWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser reads a directory without permissions")
	}

	cases := []struct {
		name    string
		fixture func(t *testing.T) *fixture
		action  Action
		entry   string
		lessons string
	}{
		{
			name: "retarget", fixture: retargetFixture, action: ActionRetarget, entry: "Guard-Empty-Datasets",
			lessons: retargetLessons,
		},
		{
			name: "prune", fixture: pruneFixture, action: ActionPrune, entry: "ruff-unused-import@scripts",
			lessons: pruneLessons,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			p := f.plan(f.deps(), f.request(tc.action, tc.entry))

			// The write fails, and the directory then refuses every read.
			dir := f.root + "/.seamark"
			files := &fileSeam{failBefore: errors.New("injected: disk full")}
			deps := f.deps()
			deps.Files = replaceFirst{inner: files, before: func() { require.NoError(t, os.Chmod(dir, 0o000)) }}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

			res, err := Commit(deps, p)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0o755))

			assert.True(t, res.FileWritten, "an unchecked file is never claimed untouched")
			require.ErrorContains(t, res.FileErr, "injected: disk full")
			require.ErrorContains(t, res.FileErr, "the file could not be read after the failure")
			require.NoError(t, res.LedgerErr)
			assert.False(t, res.Restored)
			assert.True(t, res.Partial())
			assert.Equal(t, noteFileUnread, res.Note)
			assert.Equal(t, tc.lessons, f.lessons(), "nothing was written in fact")

			// No write was seen, so nothing proves that this session removed
			// or changed the entry. A prune recovery would otherwise
			// supersede the proposal on a later absence of the pin.
			assert.Nil(t, res.Recovery)
		})
	}
}

func TestCommitRefusesMisuse(t *testing.T) {
	f := retargetFixture(t)
	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	_, err := Commit(f.deps(), nil)
	require.EqualError(t, err, "maintain: no plan")

	_, err = Commit(f.deps(), &Plan{Action: ActionRetarget, Confirmable: true, Recovery: &Recovery{}})
	require.EqualError(t, err, "maintain: a recovery plan commits through CommitRecovery")

	_, err = CommitRecovery(f.deps(), p)
	require.EqualError(t, err, "maintain: not a recovery plan")

	deps := f.deps()
	deps.Reader = nil

	_, err = Commit(deps, p)
	require.ErrorIs(t, err, ErrNoLedger)

	_, err = NewPlan(deps, p.Request)
	require.ErrorIs(t, err, ErrNoLedger)
	assert.Equal(t, retargetLessons, f.lessons())
}

func TestResultPartial(t *testing.T) {
	cases := []struct {
		res  Result
		want bool
	}{
		{Result{}, false},
		{Result{FileWritten: true}, true},
		{Result{FileWritten: true, LedgerDone: true}, false},
		{Result{FileWritten: true, Restored: true}, false},
		{Result{LedgerDone: true}, false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.res.Partial(), "%+v", tc.res)
	}
}

// RowsHoldPlanned is the one rule that both the commit and a result
// screen use to describe the planned rows after a failure.
func TestRowsHoldPlanned(t *testing.T) {
	applied := model.Proposal{
		ID: 4, Status: model.ProposalApplied, Signature: "sig-4", Rule: "wrap-sql-errors",
		Region: "internal/store", Regions: []string{"internal/store"},
	}
	other := model.Proposal{ID: 9, Status: model.ProposalApplied, Signature: "sig-9", Rule: "check-nil-errors"}

	prune := &Plan{Action: ActionPrune, Supersede: []store.RowGuard{store.NewRowGuard(applied)}}
	retarget := &Plan{Action: ActionRetarget, Retarget: []store.RegionChange{{
		Guard: store.NewRowGuard(applied), Region: "internal/store/sql", Regions: []string{"internal/store/sql"},
	}}}
	both := &Plan{Action: ActionPrune, Supersede: []store.RowGuard{
		store.NewRowGuard(applied), store.NewRowGuard(other),
	}}

	with := func(p model.Proposal, change func(*model.Proposal)) model.Proposal {
		p.Regions = slices.Clone(p.Regions)
		change(&p)

		return p
	}

	superseded := with(applied, func(p *model.Proposal) { p.Status = model.ProposalSuperseded })
	dismissed := with(applied, func(p *model.Proposal) { p.Status = model.ProposalDismissed })
	moved := with(applied, func(p *model.Proposal) {
		p.Region, p.Regions = "internal/store/sql", []string{"internal/store/sql"}
	})
	elsewhere := with(applied, func(p *model.Proposal) { p.Region, p.Regions = "internal/api", []string{"internal/api"} })

	cases := []struct {
		name            string
		plan            *Plan
		observed        []model.Proposal
		planned, target bool
	}{
		{"prune: row as planned", prune, []model.Proposal{applied}, true, false},
		{"prune: row superseded", prune, []model.Proposal{superseded}, false, true},
		{"prune: row dismissed", prune, []model.Proposal{dismissed}, false, false},
		{"prune: row missing", prune, nil, false, false},
		{"retarget: row as planned", retarget, []model.Proposal{applied}, true, false},
		{"retarget: row retargeted", retarget, []model.Proposal{moved}, false, true},
		{"retarget: row in other regions", retarget, []model.Proposal{elsewhere}, false, false},
		{"two rows: one planned, one done", both, []model.Proposal{applied, with(other, func(p *model.Proposal) {
			p.Status = model.ProposalSuperseded
		})}, false, false},
		{"two rows: both planned, any order", both, []model.Proposal{other, applied}, true, false},
		{"no ledger change", &Plan{Action: ActionPrune}, nil, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planned, target := RowsHoldPlanned(tc.plan, tc.observed)
			assert.Equal(t, tc.planned, planned, "planned")
			assert.Equal(t, tc.target, target, "target")
		})
	}
}
