package proposals

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// handLessons is a pin file with user content that every apply must
// keep: a comment, a tuning value, and a hand-written pin.
const handLessons = `# Review-lesson tuning (hand-written).
threshold: 2
pin:
  - rule: hand-written
    region: web
    note: Keep this one.
`

// fixture is one workspace root with its own index and write gate.
type fixture struct {
	t    *testing.T
	root string
	st   *store.Store
}

func newFixture(t *testing.T, write bool) *fixture {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	f := &fixture{t: t, root: root, st: st}
	f.setGate(write)

	return f
}

// setGate writes the distill.write gate into .seamark/config.yaml.
func (f *fixture) setGate(on bool) {
	f.t.Helper()

	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, ".seamark", "config.yaml"),
		fmt.Appendf(nil, "distill:\n  write: %t\n", on), 0o644))
}

func (f *fixture) writeLessons(content string) {
	f.t.Helper()

	require.NoError(f.t, os.WriteFile(f.lessonsPath(), []byte(content), 0o644))
}

func (f *fixture) lessonsPath() string {
	return filepath.Join(f.root, ".seamark", "lessons.yaml")
}

// lessons returns the pin file bytes, or nil when the file is absent.
func (f *fixture) lessons() []byte {
	f.t.Helper()

	data, err := os.ReadFile(f.lessonsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(f.t, err)

	return data
}

// propose stores one proposal with the given status and returns it
// with its ID.
func (f *fixture) propose(rule, note, status string, regions ...string) model.Proposal {
	f.t.Helper()

	p := model.Proposal{
		Signature: "sig-" + rule, Rule: rule, Note: note,
		Members: []int64{1, 2}, Agent: "claude/v2", Status: status, CreatedAt: 1700000000,
	}

	if len(regions) > 0 {
		p.Region = regions[0]
	}

	if len(regions) > 1 {
		p.Regions = regions
	}

	require.NoError(f.t, storetest.InsertProposal(f.st, &p))

	return p
}

// pending returns the IDs still pending among ids.
func (f *fixture) pending(ids ...int64) []int64 {
	f.t.Helper()

	ps, err := f.st.ProposalsByIDs(ids)
	require.NoError(f.t, err)

	var out []int64
	for _, p := range ps {
		out = append(out, p.ID)
	}

	return out
}

// failingLedger fails every status write: the injected ledger failure
// after a successful pin write.
type failingLedger struct {
	Ledgerer
	err error
}

func (l failingLedger) SetProposalStatus([]int64, string) (int, error) {
	return 0, l.err
}

// editedLedger returns every row with a different note: another writer
// changed the proposal after the preview.
type editedLedger struct {
	Ledgerer
	note string
}

func (l editedLedger) ProposalsByIDs(ids []int64) ([]model.Proposal, error) {
	ps, err := l.Ledgerer.ProposalsByIDs(ids)
	for i := range ps {
		ps[i].Note = l.note
	}

	return ps, err
}

// racingLedger dismisses one row through the real store just before
// the first status write: another writer decided it mid-commit.
type racingLedger struct {
	*store.Store
	decide int64
	done   bool
}

func (l *racingLedger) SetProposalStatus(ids []int64, to string) (int, error) {
	if !l.done {
		l.done = true

		if _, err := l.Store.SetProposalStatus([]int64{l.decide}, model.ProposalDismissed); err != nil {
			return 0, err
		}
	}

	return l.Store.SetProposalStatus(ids, to)
}

func TestActionString(t *testing.T) {
	assert.Equal(t, "apply", ActionApply.String())
	assert.Equal(t, "dismiss", ActionDismiss.String())
	assert.Equal(t, "reconcile", ActionReconcile.String())
	assert.Equal(t, "Action(7)", Action(7).String())
}

func TestEmptySelectionIsRefused(t *testing.T) {
	f := newFixture(t, true)

	_, err := PreviewApply(f.st, f.root, nil)
	require.ErrorIs(t, err, ErrNothingSelected)

	_, err = PreviewDismiss(f.st, []int64{})
	require.ErrorIs(t, err, ErrNothingSelected)

	_, err = PreviewReconcile(f.st, f.root, nil)
	require.ErrorIs(t, err, ErrNothingSelected)

	_, err = Commit(f.st, f.root, nil)
	require.ErrorIs(t, err, ErrNothingSelected)

	// A preview whose every ID left the pending state has nothing to
	// commit: it is a refusal, not an empty success.
	_, err = Commit(f.st, f.root, &Preview{Action: ActionDismiss, Ineligible: []int64{4}})
	require.ErrorIs(t, err, ErrNothingSelected)
}

func TestApplyWithGateOffPreviewsYAMLAndCommitsNothing(t *testing.T) {
	f := newFixture(t, false)
	p1 := f.propose("pooled-state-reset", "Reset pooled state on every reuse.", model.ProposalProposed, "api")
	p2 := f.propose("wrap-errors", "Wrap errors with the operation name.", model.ProposalProposed, "api", "web")

	pv, err := PreviewApply(f.st, f.root, []int64{p2.ID, p1.ID, p1.ID})
	require.NoError(t, err)

	want, err := distill.RenderPins([]model.Proposal{p1, p2})
	require.NoError(t, err)

	assert.Equal(t, ActionApply, pv.Action)
	assert.False(t, pv.Write)
	assert.Equal(t, want, pv.YAML, "the block equals what the CLI prints with the gate off")
	assert.Equal(t, LessonsFile, pv.Target)
	assert.False(t, pv.Lessons.Exists)
	assert.Empty(t, pv.Ineligible)
	assert.Empty(t, pv.AlreadyPinned)
	assert.Equal(t, []int64{p1.ID, p2.ID}, pv.ids(), "duplicates collapse and the order is by ID")
	assert.Equal(t, []string{"api", "web"}, pv.Prints[1].Regions)

	res, err := Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrWriteDisabled)
	assert.False(t, res.FileWritten)
	assert.Nil(t, f.lessons(), "no pin file appears")
	assert.Equal(t, []int64{p1.ID, p2.ID}, f.pending(p1.ID, p2.ID), "the proposals stay pending")

	// Turning the gate on later does not turn the YAML-only preview
	// into an apply: the user never saw a write preview.
	f.setGate(true)

	_, err = Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrWriteDisabled)
	assert.Nil(t, f.lessons())
}

func TestApplyWithGateOnWritesTheBlockUnderThePinKey(t *testing.T) {
	// The commit adds the rendered block at the head of the list, right
	// under the pin: line, and keeps every hand-written line. The CLI
	// goldens pin the same bytes for `seamark lessons --apply`, which
	// also runs Commit.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	ids := []int64{
		f.propose("pooled-state-reset", "Reset pooled state: on every reuse.", model.ProposalProposed, "api").ID,
		f.propose("wrap-errors", "Wrap errors with the operation name.", model.ProposalProposed, "web", "api").ID,
	}

	pv, err := PreviewApply(f.st, f.root, ids)
	require.NoError(t, err)
	assert.True(t, pv.Write)
	assert.True(t, pv.Lessons.Exists)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	require.NoError(t, res.FileErr)
	require.NoError(t, res.LedgerErr)
	assert.Equal(t, ids, res.Moved)
	assert.Empty(t, res.NotMoved)
	assert.False(t, res.Partial())

	block, err := distill.RenderPins(pv.Proposals)
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(handLessons, "pin:\n", "pin:\n"+block, 1), string(f.lessons()),
		"the block goes under pin:, and the user pin follows it")

	assert.Empty(t, f.pending(ids...), "the ledger records the decision")

	applied, err := f.st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Len(t, applied, 2)

	// The written pins load through the real reader under their keys.
	cfg, err := reviews.LoadConfig(f.root)
	require.NoError(t, err)

	for _, p := range pv.Proposals {
		_, ok := cfg.FindPin(pinKey(p))
		assert.True(t, ok, "pin for p%d is live", p.ID)
	}
}

func TestCommitRejectsStaleInputs(t *testing.T) {
	tests := []struct {
		name string
		// change runs after the preview. It returns the ledger the
		// commit uses.
		change func(f *fixture, ids []int64) Ledgerer
	}{
		{
			name: "pin file edited",
			change: func(f *fixture, _ []int64) Ledgerer {
				f.writeLessons(handLessons + "  - rule: added-by-hand\n    region: web\n    note: Later.\n")

				return f.st
			},
		},
		{
			name: "pin file removed",
			change: func(f *fixture, _ []int64) Ledgerer {
				require.NoError(t, os.Remove(f.lessonsPath()))

				return f.st
			},
		},
		{
			name: "write gate turned off",
			change: func(f *fixture, _ []int64) Ledgerer {
				f.setGate(false)

				return f.st
			},
		},
		{
			name: "proposal dismissed elsewhere",
			change: func(f *fixture, ids []int64) Ledgerer {
				_, err := f.st.SetProposalStatus(ids[1:], model.ProposalDismissed)
				require.NoError(t, err)

				return f.st
			},
		},
		{
			name: "note changed",
			change: func(f *fixture, _ []int64) Ledgerer {
				return editedLedger{Ledgerer: f.st, note: "A different note."}
			},
		},
		{
			name: "regions retargeted",
			change: func(f *fixture, ids []int64) Ledgerer {
				// A trigger extraction retargets a pending row through the
				// guarded save, as the CLI and the console do.
				rows, err := f.st.ProposalsWithIDs(ids[:1])
				require.NoError(t, err)
				require.Len(t, rows, 1)

				ok, err := f.st.SaveTriggerAnswer(store.TriggerAnswer{
					Guard: store.NewRowGuard(rows[0]), CheckedAt: 1_700_000_000, Version: 3,
					SetRegions: true, Region: "web", Regions: []string{"web", "api"},
				})
				require.NoError(t, err)
				require.True(t, ok)

				return f.st
			},
		},
		{
			name: "ID reused after a prune",
			change: func(f *fixture, ids []int64) Ledgerer {
				// Pruning deletes the pending rows. SQLite reuses the
				// highest rowid, so a new proposal takes the old ID.
				_, err := f.st.PruneStaleProposals(map[string]bool{})
				require.NoError(t, err)

				f.propose("other-rule", "Other note.", model.ProposalProposed, "api")
				reused := f.propose("second-other", "Other note.", model.ProposalProposed, "api")
				require.Equal(t, ids[1], reused.ID, "the fixture reproduces the ID reuse")

				return f.st
			},
		},
		{
			name: "pin file replaced by a symbolic link",
			change: func(f *fixture, _ []int64) Ledgerer {
				elsewhere := filepath.Join(t.TempDir(), "lessons.yaml")
				require.NoError(t, os.WriteFile(elsewhere, []byte(handLessons), 0o644))
				require.NoError(t, os.Remove(f.lessonsPath()))
				require.NoError(t, os.Symlink(elsewhere, f.lessonsPath()))

				return f.st
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.writeLessons(handLessons)

			ids := []int64{
				f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api").ID,
				f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api").ID,
			}

			pv, err := PreviewApply(f.st, f.root, ids)
			require.NoError(t, err)

			ls := tt.change(f, ids)
			before, _ := os.ReadFile(f.lessonsPath())
			pendingBefore := f.pending(ids...)

			res, err := Commit(ls, f.root, pv)
			require.ErrorIs(t, err, ErrStale)
			assert.False(t, res.FileWritten)
			assert.Empty(t, res.Moved)

			after, _ := os.ReadFile(f.lessonsPath())
			assert.Equal(t, string(before), string(after), "the pin file is untouched")
			assert.Equal(t, pendingBefore, f.pending(ids...), "the ledger is untouched")
		})
	}
}

func TestLedgerFailureAfterPinWriteNeedsReconcile(t *testing.T) {
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	ids := []int64{
		f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api").ID,
		f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api", "web").ID,
	}

	pv, err := PreviewApply(f.st, f.root, ids)
	require.NoError(t, err)

	busy := errors.New("database is locked")

	res, err := Commit(failingLedger{Ledgerer: f.st, err: busy}, f.root, pv)
	require.NoError(t, err, "once writing starts, the result carries the outcome")
	assert.True(t, res.FileWritten)
	require.ErrorIs(t, res.LedgerErr, busy)
	assert.Equal(t, ids, res.NotMoved)
	assert.Empty(t, res.Moved)
	assert.True(t, res.Partial())
	assert.Equal(t, ids, f.pending(ids...), "the ledger still says pending")

	written := f.lessons()
	assert.Equal(t, 1, strings.Count(string(written), "rule: pooled-state-reset"))

	// A fresh apply preview sees the pins, and the commit refuses to
	// write them a second time.
	again, err := PreviewApply(f.st, f.root, ids)
	require.NoError(t, err)
	assert.Equal(t, ids, again.AlreadyPinned)

	_, err = Commit(f.st, f.root, again)
	require.ErrorIs(t, err, ErrNeedsReconcile)
	assert.Contains(t, err.Error(), fmt.Sprintf("p%d, p%d", ids[0], ids[1]))
	assert.Equal(t, string(written), string(f.lessons()), "no duplicate pin")

	// Reconcile records the existing pins as applied and writes only
	// the ledger.
	rv, err := PreviewReconcile(f.st, f.root, ids)
	require.NoError(t, err)
	assert.Equal(t, ActionReconcile, rv.Action)
	assert.Empty(t, rv.YAML, "reconcile writes no pins")

	res, err = Commit(f.st, f.root, rv)
	require.NoError(t, err)
	assert.False(t, res.FileWritten)
	assert.Equal(t, ids, res.Moved)
	assert.Empty(t, res.NotMoved)
	require.NoError(t, res.LedgerErr)
	assert.Empty(t, f.pending(ids...))
	assert.Equal(t, string(written), string(f.lessons()), "the pin file keeps its bytes")
	assert.Equal(t, 1, strings.Count(string(f.lessons()), "rule: pooled-state-reset"))
}

func TestReconcileRequiresExistingPins(t *testing.T) {
	f := newFixture(t, false)
	f.writeLessons(handLessons + "  - rule: pooled-state-reset\n    region: api\n    note: Reset.\n")

	pinned := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")
	unpinned := f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api")

	_, err := PreviewReconcile(f.st, f.root, []int64{pinned.ID, unpinned.ID})
	require.ErrorIs(t, err, ErrNotPinned)
	assert.Contains(t, err.Error(), fmt.Sprintf("p%d", unpinned.ID))
	assert.NotContains(t, err.Error(), fmt.Sprintf("p%d,", pinned.ID))

	// The gate does not guard a ledger-only change.
	rv, err := PreviewReconcile(f.st, f.root, []int64{pinned.ID})
	require.NoError(t, err)
	assert.False(t, rv.Write)

	// The pin leaves the file after the preview: the commit refuses.
	f.writeLessons(handLessons)

	_, err = Commit(f.st, f.root, rv)
	require.ErrorIs(t, err, ErrStale)
	assert.Equal(t, []int64{pinned.ID}, f.pending(pinned.ID))
}

// regionLedger returns every row with another Region: another writer
// changed the raw region field after the preview. RegionSet ignores
// Region when Regions is set, so the fingerprint stays the same.
type regionLedger struct {
	Ledgerer
	region string
}

func (l regionLedger) ProposalsByIDs(ids []int64) ([]model.Proposal, error) {
	ps, err := l.Ledgerer.ProposalsByIDs(ids)
	for i := range ps {
		ps[i].Region = l.region
	}

	return ps, err
}

func TestReconcileRefusesARowWhoseRegionFieldsDisagree(t *testing.T) {
	// The fields of the row disagree: its key reads odd-fields @ api, as
	// Pins and the CLI link it, and its apply writes odd-fields @ *.
	// Either pin alone cannot prove which pin is its own.
	layouts := map[string]string{
		"a pin with the stored fields":      "  - rule: odd-fields\n    region: api\n    note: Hand.\n",
		"only the pin that its apply wrote": "  - rule: odd-fields\n    region: '*'\n    note: Rendered.\n",
	}

	for name, entry := range layouts {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			f.writeLessons(handLessons + entry)

			p := model.Proposal{Signature: "sig-odd", Rule: "odd-fields", Region: "api", Regions: []string{"*"},
				Note: "Odd.", Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalProposed}
			require.NoError(t, storetest.InsertProposal(f.st, &p))

			_, err := PreviewReconcile(f.st, f.root, []int64{p.ID})
			require.ErrorIs(t, err, ErrFieldsDisagree)
			assert.NotErrorIs(t, err, ErrNotPinned)
			assert.Contains(t, err.Error(), fmt.Sprintf("p%d", p.ID))
			assert.Equal(t, []int64{p.ID}, f.pending(p.ID), "the ledger is unchanged")
		})
	}
}

func TestReconcileCommitRefusesRegionFieldsThatChangedToDisagree(t *testing.T) {
	f := newFixture(t, false)
	f.writeLessons(handLessons + "  - rule: wrap-errors\n    region: api\n    regions: [api, web]\n    note: Wrap.\n")

	p := f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api", "web")

	rv, err := PreviewReconcile(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	// Region now names a region outside Regions. RegionSet and so the
	// fingerprint stay the same, but the fields disagree.
	_, err = Commit(regionLedger{Ledgerer: f.st, region: "lib"}, f.root, rv)
	require.ErrorIs(t, err, ErrStale)
	require.ErrorIs(t, err, ErrFieldsDisagree)
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID), "the ledger is unchanged")
}

func TestDismissMovesOnlyEligibleRows(t *testing.T) {
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	pending := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")
	applied := f.propose("wrap-errors", "Wrap errors.", model.ProposalApplied, "api")

	pv, err := PreviewDismiss(f.st, []int64{999, applied.ID, pending.ID})
	require.NoError(t, err)
	assert.Equal(t, ActionDismiss, pv.Action)
	assert.Equal(t, []int64{pending.ID}, pv.ids())
	assert.Equal(t, []int64{applied.ID, 999}, pv.Ineligible, "decided and unknown IDs are reported")
	assert.Empty(t, pv.YAML)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	assert.Equal(t, []int64{pending.ID}, res.Moved)
	assert.Empty(t, res.NotMoved)
	assert.False(t, res.FileWritten)
	assert.False(t, res.Partial())

	dismissed, err := f.st.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, dismissed, 1)
	assert.Equal(t, pending.ID, dismissed[0].ID)

	stillApplied, err := f.st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, stillApplied, 1, "an applied row cannot be dismissed")
	assert.Equal(t, handLessons, string(f.lessons()), "dismiss never touches pins")

	// A second commit of the same preview is stale, not a silent no-op.
	_, err = Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrStale)
}

func TestCommitReportsRowsDecidedDuringTheWrite(t *testing.T) {
	f := newFixture(t, true)

	ids := []int64{
		f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api").ID,
		f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api").ID,
	}

	pv, err := PreviewApply(f.st, f.root, ids)
	require.NoError(t, err)

	res, err := Commit(&racingLedger{Store: f.st, decide: ids[1]}, f.root, pv)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	require.NoError(t, res.LedgerErr)
	assert.Equal(t, []int64{ids[0]}, res.Moved)
	assert.Equal(t, []int64{ids[1]}, res.NotMoved, "the row decided elsewhere is named")
	assert.True(t, res.Partial(), "its pin is in the file, but the ledger says dismissed")
}

func TestPinWriteFailureLeavesTheLedgerAlone(t *testing.T) {
	f := newFixture(t, true)

	// A flow-style pin section is a file that the append cannot edit
	// safely. It fails before it writes a byte.
	const flow = "pin: []\n"
	f.writeLessons(flow)

	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	require.Error(t, res.FileErr)
	assert.Contains(t, res.FileErr.Error(), "flow-style")
	assert.False(t, res.FileWritten)
	assert.Equal(t, []int64{p.ID}, res.NotMoved)
	assert.False(t, res.Partial())
	assert.Equal(t, flow, string(f.lessons()))
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID))
}

func TestApplyCreatesAMissingPinFile(t *testing.T) {
	// The first apply of a workspace creates the pin file with a header
	// comment, as the CLI always did.
	f := newFixture(t, true)
	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)
	require.False(t, pv.Lessons.Exists)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	require.NoError(t, res.FileErr)
	assert.True(t, res.FileWritten)
	assert.Equal(t, []int64{p.ID}, res.Moved)

	assert.Equal(t, "# Review-lesson tuning (see `seamark lessons --list`).\n\npin:\n"+pv.YAML, string(f.lessons()))
	assert.Empty(t, f.pending(p.ID))
}

func TestApplyFollowsTheIndentationOfTheList(t *testing.T) {
	// The CLI used to insert two-space entries above a list at another
	// column. The result did not parse, so the apply failed. The append
	// moves the new lines to the column of the list.
	f := newFixture(t, true)
	f.writeLessons("pin:\n    - rule: hand-written\n      region: web\n      note: Keep this one.\n")

	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	require.NoError(t, res.FileErr)

	assert.Equal(t, "pin:\n"+
		"    # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)\n"+
		"    - rule: pooled-state-reset\n"+
		"      region: api\n"+
		"      note: Reset pooled state.\n"+
		"    - rule: hand-written\n      region: web\n      note: Keep this one.\n", string(f.lessons()))
	assert.Empty(t, f.pending(p.ID))
}

func TestApplyStillWritesFilesThatTheEditGateRefuses(t *testing.T) {
	// The apply inserts at the head with the zero pinfile.Placement. It
	// keeps its behavior byte for byte (P4-6). A CRLF file keeps its line
	// breaks. An empty entry or a tab-led note line does not stop it,
	// although the authoring of pins refuses such files.
	tests := []struct {
		name   string
		before string
		want   func(block string) string
	}{
		{
			name:   "CRLF line breaks",
			before: strings.ReplaceAll(handLessons, "\n", "\r\n"),
			want: func(block string) string {
				return strings.ReplaceAll(strings.Replace(handLessons, "pin:\n", "pin:\n"+block, 1), "\n", "\r\n")
			},
		},
		{
			name:   "an empty entry",
			before: "pin:\n  -\n  - rule: hand-written\n    note: Keep this one.\n",
			want: func(block string) string {
				return "pin:\n" + block + "  -\n  - rule: hand-written\n    note: Keep this one.\n"
			},
		},
		{
			name:   "a note line that starts with a tab",
			before: "pin:\n  - rule: hand-written\n    note: |-\n      Keep this one.\n      \tIndented with a tab.\n",
			want: func(block string) string {
				return "pin:\n" + block + "  - rule: hand-written\n    note: |-\n      Keep this one.\n      \tIndented with a tab.\n"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.writeLessons(tt.before)

			p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

			pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
			require.NoError(t, err)

			res, err := Commit(f.st, f.root, pv)
			require.NoError(t, err)
			require.NoError(t, res.FileErr)
			assert.True(t, res.FileWritten)
			assert.Equal(t, []int64{p.ID}, res.Moved)

			assert.Equal(t, tt.want(pv.YAML), string(f.lessons()))
			assert.Empty(t, f.pending(p.ID))
		})
	}
}

func TestApplyKeepsAReadOnlyPinFile(t *testing.T) {
	// The CLI used to fail in os.WriteFile with "permission denied". The
	// guarded write refuses the file before the write and names the mode.
	f := newFixture(t, true)
	f.writeLessons(handLessons)
	require.NoError(t, os.Chmod(f.lessonsPath(), 0o444))

	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	require.ErrorIs(t, res.FileErr, pinfile.ErrNotWritable)
	assert.Equal(t, "the pin file cannot be replaced: the file is read-only (-r--r--r--); seamark does not replace it",
		res.FileErr.Error())
	assert.False(t, res.FileWritten)
	assert.Equal(t, []int64{p.ID}, res.NotMoved)
	assert.Equal(t, handLessons, string(f.lessons()))
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID))
}

func TestAppendPinsBindsToThePreviewedFile(t *testing.T) {
	// Commit compares the file with the preview before it calls
	// appendPins. appendPins compares again on its own read, so a change
	// in between writes nothing; Commit then reports ErrStale.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	const other = "pin:\n  - rule: other writer\n    note: Later.\n"
	f.writeLessons(other)

	err = appendPins(f.root, pv.Lessons, pv.YAML)
	require.ErrorIs(t, err, pinfile.ErrStale)
	assert.Equal(t, other, string(f.lessons()))

	// A file that appears after a preview of an absent file is stale too.
	g := newFixture(t, true)
	q := g.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err = PreviewApply(g.st, g.root, []int64{q.ID})
	require.NoError(t, err)

	g.writeLessons(other)

	err = appendPins(g.root, pv.Lessons, pv.YAML)
	require.ErrorIs(t, err, pinfile.ErrStale)
	assert.Equal(t, other, string(g.lessons()))
}

// The guarded write checks the file again after appendPins reads it.
// Another writer that changes the file in between makes the write
// refuse with pinfile.ErrStale, and nothing is written. Commit reports
// that as ErrStale, so the caller previews again. It is not a failed
// write: the result names no file error and no row left behind.
func TestCommitApplyReportsAStaleGuardedWriteAsStale(t *testing.T) {
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	const other = "pin:\n  - rule: other writer\n    note: Later.\n"

	read := readPinFile
	t.Cleanup(func() { readPinFile = read })

	readPinFile = func(root string) (*pinfile.Document, error) {
		d, err := read(root)
		f.writeLessons(other)

		return d, err
	}

	res, err := Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrStale)
	assert.Equal(t, "the ledger, the pin file, or the write gate changed since the preview: "+
		".seamark/lessons.yaml changed after the preview", err.Error())
	require.NoError(t, res.FileErr)
	assert.False(t, res.FileWritten)
	assert.Empty(t, res.NotMoved)
	assert.Empty(t, res.Moved)

	assert.Equal(t, other, string(f.lessons()), "the other writer's bytes stay")
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID), "the proposal stays pending")
}

// linkLessons replaces the pin file with a symbolic link to a
// hand-written pin file outside the root, and returns the target path.
func (f *fixture) linkLessons() string {
	f.t.Helper()

	elsewhere := filepath.Join(f.t.TempDir(), "lessons.yaml")
	require.NoError(f.t, os.WriteFile(elsewhere, []byte(handLessons), 0o644))
	require.NoError(f.t, os.Symlink(elsewhere, f.lessonsPath()))

	return elsewhere
}

// linkSeamarkDir moves .seamark out of the root and puts a symbolic
// link to it in its place. The configs stay readable through the link.
func (f *fixture) linkSeamarkDir() {
	f.t.Helper()

	elsewhere := filepath.Join(f.t.TempDir(), "seamark")
	require.NoError(f.t, os.Rename(filepath.Join(f.root, ".seamark"), elsewhere))
	require.NoError(f.t, os.Symlink(elsewhere, filepath.Join(f.root, ".seamark")))
}

func TestPreviewApplyRefusesUnsafeOrMalformedFiles(t *testing.T) {
	t.Run("symbolic link with the gate on", func(t *testing.T) {
		f := newFixture(t, true)
		p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")
		f.linkLessons()

		_, err := PreviewApply(f.st, f.root, []int64{p.ID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")

		_, err = PreviewReconcile(f.st, f.root, []int64{p.ID})
		require.Error(t, err)
	})

	t.Run("linked seamark directory with the gate on", func(t *testing.T) {
		f := newFixture(t, true)
		p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")
		f.linkSeamarkDir()

		_, err := PreviewApply(f.st, f.root, []int64{p.ID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
	})

	t.Run("reconcile with the gate off still refuses a link", func(t *testing.T) {
		// Reconcile writes the ledger on the evidence of the pin file,
		// so the gate does not relax its guard.
		f := newFixture(t, false)
		p := f.propose("hand-written", "Keep this one.", model.ProposalProposed, "web")
		f.linkLessons()

		_, err := PreviewReconcile(f.st, f.root, []int64{p.ID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
	})

	t.Run("malformed lessons config", func(t *testing.T) {
		f := newFixture(t, true)
		f.writeLessons("hook_delivery: sometimes\n")
		p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

		_, err := PreviewApply(f.st, f.root, []int64{p.ID})
		require.ErrorIs(t, err, reviews.ErrLessonsConfig, "the reader's error, never defaults")
	})

	t.Run("malformed distill config", func(t *testing.T) {
		f := newFixture(t, true)
		require.NoError(t, os.WriteFile(filepath.Join(f.root, ".seamark", "config.yaml"),
			[]byte("distill: [\n"), 0o644))
		p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

		_, err := PreviewApply(f.st, f.root, []int64{p.ID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "distill config")
	})
}

func TestGateOffPreviewShowsTheBlockDespiteALinkedPinFile(t *testing.T) {
	// With the gate off, apply only prints the block for a manual paste,
	// as the CLI does. A link at the pin file path writes nothing here,
	// so the preview records the guard failure and still shows the block.
	for name, link := range map[string]func(*fixture){
		"linked pin file":          func(f *fixture) { f.linkLessons() },
		"linked seamark directory": (*fixture).linkSeamarkDir,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")
			link(f)

			before := f.lessons()

			pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
			require.NoError(t, err)

			want, err := distill.RenderPins([]model.Proposal{p})
			require.NoError(t, err)

			assert.False(t, pv.Write)
			assert.Equal(t, want, pv.YAML, "the block equals what the CLI prints with the gate off")
			require.Error(t, pv.LessonsErr)
			assert.Contains(t, pv.LessonsErr.Error(), "symlink")
			assert.Equal(t, FileObservation{}, pv.Lessons, "no observation stands for the linked file")
			assert.Empty(t, pv.AlreadyPinned)

			// The commit refuses, and turning the gate on later does not
			// make the old preview a write authorization.
			_, err = Commit(f.st, f.root, pv)
			require.ErrorIs(t, err, ErrWriteDisabled)

			f.setGate(true)

			_, err = Commit(f.st, f.root, pv)
			require.ErrorIs(t, err, ErrWriteDisabled)
			assert.Equal(t, before, f.lessons(), "the linked file is unchanged")
			assert.Equal(t, []int64{p.ID}, f.pending(p.ID))

			// A new preview with the gate on refuses the link at once.
			_, err = PreviewApply(f.st, f.root, []int64{p.ID})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "symlink")
		})
	}
}

func TestCommitRefusesAWritePreviewWithoutAPinFileObservation(t *testing.T) {
	// PreviewApply never makes this preview, but a caller can build one.
	// Without an observation, the stale check has no baseline.
	f := newFixture(t, true)
	p := f.propose("pooled-state-reset", "Reset pooled state.", model.ProposalProposed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	pv.LessonsErr = errors.New("symlink at .seamark/lessons.yaml")

	_, err = Commit(f.st, f.root, pv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not observe")
	assert.Nil(t, f.lessons(), "no pin file appears")
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID))
}

func TestPreviewApplyReportsIneligibleSelections(t *testing.T) {
	f := newFixture(t, true)
	dismissed := f.propose("wrap-errors", "Wrap errors.", model.ProposalDismissed, "api")

	pv, err := PreviewApply(f.st, f.root, []int64{dismissed.ID, 42})
	require.NoError(t, err, "a preview of only ineligible IDs still describes them")
	assert.Empty(t, pv.Proposals)
	assert.Equal(t, []int64{dismissed.ID, 42}, pv.Ineligible)
	assert.Empty(t, pv.YAML)

	_, err = Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrNothingSelected)
}

// proposeAs stores one pending proposal with its own signature, so two
// rows can share a rule. It returns the row with its ID.
func (f *fixture) proposeAs(signature, rule, note string, regions ...string) model.Proposal {
	f.t.Helper()

	p := model.Proposal{
		Signature: signature, Rule: rule, Note: note, Region: regions[0], Regions: regions,
		Members: []int64{1, 2}, Agent: "claude/v2", Status: model.ProposalProposed, CreatedAt: 1700000000,
	}

	if len(regions) == 1 {
		p.Regions = nil
	}

	require.NoError(f.t, storetest.InsertProposal(f.st, &p))

	return p
}

func TestApplyRefusesASelectionThatSharesAPinKey(t *testing.T) {
	// D16: two selected rows with one canonical key used to give two
	// entries with that key, and the link rules then blocked both. One
	// entry for both rows reads ambiguous and is blocked too, so apply
	// refuses the selection. The rule case and the region order do not
	// change the key.
	tests := []struct {
		name    string
		write   bool
		lessons string
	}{
		{name: "gate on", write: true, lessons: handLessons},
		{name: "gate off", write: false, lessons: handLessons},
		{
			name: "the pin file already holds the key", write: true,
			lessons: handLessons + "  - rule: ttl-monotonic-clock\n    regions: [api, web]\n    note: By hand.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.write)
			f.writeLessons(tt.lessons)

			first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api", "web")
			same := f.proposeAs("s2", "TTL-Monotonic-Clock", "Compute expiry from a monotonic reading.", "web", "api")
			other := f.proposeAs("s3", "wrap-errors", "Wrap errors.", "api")
			selected := []int64{first.ID, same.ID, other.ID}

			pv, err := PreviewApply(f.st, f.root, selected)
			require.ErrorIs(t, err, ErrSharedKey)
			assert.Nil(t, pv)
			assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())

			var shared *SharedKeyError

			require.ErrorAs(t, err, &shared)
			assert.Equal(t, [][]int64{{first.ID, same.ID}}, shared.Groups, "the other key is not named")

			assert.Equal(t, tt.lessons, string(f.lessons()), "the pin file is unchanged")
			assert.Equal(t, selected, f.pending(selected...), "the ledger is unchanged")

			// Each row alone is a valid selection.
			_, err = PreviewApply(f.st, f.root, []int64{first.ID, other.ID})
			require.NoError(t, err)
		})
	}
}

func TestApplyRefusalNamesEveryRowOfASharedKey(t *testing.T) {
	f := newFixture(t, true)

	f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api")
	f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")
	f.proposeAs("s3", "wrap-errors", "Wrap errors.", "api")
	f.proposeAs("s4", "close-handles", "Close handles.", "db")
	f.proposeAs("s5", "ttl-monotonic-clock", "Never use the wall clock for TTLs.", "api")

	_, err := PreviewApply(f.st, f.root, []int64{1, 2, 3, 4, 5})
	require.ErrorIs(t, err, ErrSharedKey)
	assert.Equal(t, "p1, p2 and p5 share rule and regions; apply one and dismiss the others", err.Error())
}

func TestSharedKeyErrorNamesEachGroup(t *testing.T) {
	tests := []struct {
		name   string
		groups [][]int64
		want   string
	}{
		{
			name:   "two rows",
			groups: [][]int64{{1, 2}},
			want:   "p1 and p2 share rule and regions; apply one and dismiss the other",
		},
		{
			name:   "three rows",
			groups: [][]int64{{1, 2, 5}},
			want:   "p1, p2 and p5 share rule and regions; apply one and dismiss the others",
		},
		{
			name:   "two keys",
			groups: [][]int64{{1, 4}, {2, 3}},
			want: "p1 and p4 share rule and regions; p2 and p3 share rule and regions; " +
				"apply one of each group and dismiss the others",
		},
		{
			name: "no group",
			want: "selected proposals share rule and regions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &SharedKeyError{Groups: tt.groups}

			assert.Equal(t, tt.want, err.Error())
			require.ErrorIs(t, err, ErrSharedKey)
			assert.NotErrorIs(t, err, ErrStale)
		})
	}
}

func TestApplyCommitRefusesAPreviewWithASharedKey(t *testing.T) {
	// PreviewApply never makes this preview, because the fingerprint
	// holds the rule and the region set. A caller can build one, so the
	// commit checks the keys again before it writes.
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api")
	same := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")

	pv, err := PreviewApply(f.st, f.root, []int64{first.ID})
	require.NoError(t, err)

	pv.Proposals = append(pv.Proposals, same)
	pv.Prints = append(pv.Prints, fingerprint(same))

	_, err = Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrSharedKey)
	assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())
	assert.Equal(t, handLessons, string(f.lessons()), "the pin file is unchanged")
	assert.Equal(t, []int64{first.ID, same.ID}, f.pending(first.ID, same.ID), "the ledger is unchanged")
}

func TestReconcileRefusesASelectionThatSharesAPinKey(t *testing.T) {
	// D16 through the record: the pin file holds one entry, and Pins
	// reads both pending rows as its pending match. A record of both
	// makes two applied rows record the entry, and Pins then blocks it.
	// So the record refuses the selection, as apply does.
	f := newFixture(t, true)
	lessons := handLessons + "  - rule: ttl-monotonic-clock\n    regions: [api, web]\n    note: By hand.\n"
	f.writeLessons(lessons)

	first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api", "web")
	same := f.proposeAs("s2", "TTL-Monotonic-Clock", "Compute expiry from a monotonic reading.", "web", "api")
	selected := []int64{first.ID, same.ID}

	pv, err := PreviewReconcile(f.st, f.root, selected)
	require.ErrorIs(t, err, ErrSharedKey)
	assert.NotErrorIs(t, err, ErrNotPinned)
	assert.Nil(t, pv)
	assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())
	assert.Equal(t, lessons, string(f.lessons()), "the pin file is unchanged")
	assert.Equal(t, selected, f.pending(selected...), "the ledger is unchanged")

	// Each row alone is a valid record.
	_, err = PreviewReconcile(f.st, f.root, []int64{first.ID})
	require.NoError(t, err)
}

func TestReconcileCommitRefusesAPreviewWithASharedKey(t *testing.T) {
	// PreviewReconcile never makes this preview, because the fingerprint
	// holds the rule and the region set. A caller can build one, so the
	// commit checks the keys again before it writes the ledger.
	f := newFixture(t, false)
	f.writeLessons(handLessons + "  - rule: ttl-monotonic-clock\n    region: api\n    note: By hand.\n")

	first := f.proposeAs("s1", "ttl-monotonic-clock", "Use the monotonic clock.", "api")
	same := f.proposeAs("s2", "ttl-monotonic-clock", "Compute expiry from a monotonic reading.", "api")

	pv, err := PreviewReconcile(f.st, f.root, []int64{first.ID})
	require.NoError(t, err)

	pv.Proposals = append(pv.Proposals, same)
	pv.Prints = append(pv.Prints, fingerprint(same))

	res, err := Commit(f.st, f.root, pv)
	require.ErrorIs(t, err, ErrSharedKey)
	assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())
	assert.Empty(t, res.Moved)
	assert.Equal(t, []int64{first.ID, same.ID}, f.pending(first.ID, same.ID), "the ledger is unchanged")
}

func TestApplyRefusesARowWhoseRegionFieldsDisagree(t *testing.T) {
	// F1: the key of the row reads odd-fields @ api, as every reader
	// links it, and its apply writes odd-fields @ *. No reader would link
	// that pin to the row, so apply refuses it with the gate on or off.
	for _, write := range []bool{true, false} {
		t.Run(fmt.Sprintf("gate %t", write), func(t *testing.T) {
			f := newFixture(t, write)
			f.writeLessons(handLessons)

			ok := f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api")
			odd := model.Proposal{Signature: "sig-odd", Rule: "odd-fields", Region: "api", Regions: []string{"*"},
				Note: "Odd.", Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalProposed}
			require.NoError(t, storetest.InsertProposal(f.st, &odd))

			pv, err := PreviewApply(f.st, f.root, []int64{ok.ID, odd.ID})
			require.ErrorIs(t, err, ErrFieldsDisagree)
			assert.Nil(t, pv)
			assert.Equal(t, "the stored region fields of the selected proposals disagree: p2; "+
				"resolve them by hand or dismiss them", err.Error())
			assert.Equal(t, handLessons, string(f.lessons()), "the pin file is unchanged")
			assert.Equal(t, []int64{ok.ID, odd.ID}, f.pending(ok.ID, odd.ID), "the ledger is unchanged")
		})
	}
}

func TestApplyCommitRefusesRegionFieldsThatChangedToDisagree(t *testing.T) {
	f := newFixture(t, true)
	f.writeLessons(handLessons)

	p := f.propose("wrap-errors", "Wrap errors.", model.ProposalProposed, "api", "web")

	pv, err := PreviewApply(f.st, f.root, []int64{p.ID})
	require.NoError(t, err)

	// Region now names a region outside Regions. RegionSet and so the
	// fingerprint stay the same, but the fields disagree.
	_, err = Commit(regionLedger{Ledgerer: f.st, region: "lib"}, f.root, pv)
	require.ErrorIs(t, err, ErrStale)
	require.ErrorIs(t, err, ErrFieldsDisagree)
	assert.Equal(t, handLessons, string(f.lessons()), "the pin file is unchanged")
	assert.Equal(t, []int64{p.ID}, f.pending(p.ID), "the ledger is unchanged")
}
