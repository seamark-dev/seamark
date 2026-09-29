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
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
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

	require.NoError(f.t, f.st.InsertProposal(&p))

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

func TestApplyWithGateOnMatchesTheCLIWrite(t *testing.T) {
	// Two identical workspaces: one takes the console commit, the other
	// the call that `seamark lessons --apply` makes.
	console, cli := newFixture(t, true), newFixture(t, true)

	var ids []int64

	for _, f := range []*fixture{console, cli} {
		f.writeLessons(handLessons)

		ids = ids[:0]
		ids = append(ids,
			f.propose("pooled-state-reset", "Reset pooled state: on every reuse.", model.ProposalProposed, "api").ID,
			f.propose("wrap-errors", "Wrap errors with the operation name.", model.ProposalProposed, "web", "api").ID,
		)
	}

	pv, err := PreviewApply(console.st, console.root, ids)
	require.NoError(t, err)
	assert.True(t, pv.Write)
	assert.True(t, pv.Lessons.Exists)

	res, err := Commit(console.st, console.root, pv)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	require.NoError(t, res.FileErr)
	require.NoError(t, res.LedgerErr)
	assert.Equal(t, ids, res.Moved)
	assert.Empty(t, res.NotMoved)
	assert.False(t, res.Partial())

	ps, err := cli.st.ProposalsByIDs(ids)
	require.NoError(t, err)
	require.NoError(t, distill.ApplyPins(cli.root, ps))

	assert.Equal(t, string(cli.lessons()), string(console.lessons()), "byte-identical pin file")
	assert.Contains(t, string(console.lessons()), "rule: hand-written", "user pins survive")

	assert.Empty(t, console.pending(ids...), "the ledger records the decision")

	applied, err := console.st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	assert.Len(t, applied, 2)

	// The written pins load through the real reader under their keys.
	cfg, err := reviews.LoadConfig(console.root)
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
				ok, err := f.st.UpdateProposalRegionsIfPending(model.Proposal{
					ID: ids[0], Region: "web", Regions: []string{"web", "api"},
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

	// A flow-style pin section is a file that ApplyPins cannot edit
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
