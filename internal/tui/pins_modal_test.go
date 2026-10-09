package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests of the retarget and prune preview (contracts §3.2, B2-maint).
// They drive the real console through the B1 hook openMaintMsg, as the
// Pins keys w and - do, over the fixture of pins_modal_fixture_test.go.

func TestMaintRetargetPreviewShowsTheReviewedChange(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)

	h := maintOpen(t, f, p.Request, 120, 40)
	require.True(t, maintHostOf(h).open())
	assert.Equal(t, []string{"PlanMaintenance:retarget"}, f.called(), "the preview plans once and commits nothing")

	text := maintContent(t, h)
	line := itoa(fx.pins[maintWrap].Entry.Line)

	for _, want := range []string{
		"Retarget p4 · wrap-sql-errors", "narrower",
		"Seamark sets the regions of 1 pin in .seamark/lessons.yaml to the scope that today's evidence supports",
		"distill.write is on. Nothing changes before you press y.",
		"SELECTED 1 pin · the cursor row; no row is marked",
		"wrap-sql-errors ✓ ready", ".seamark/lessons.yaml:" + line + " · linked p4",
		"− internal/store removed", "+ internal/store/sql added",
		"Narrower: the new regions reach fewer paths",
		"Why trigger internal/store/sql/query.go is cited evidence",
		"Delivery history restarts under the new pin identity",
		"LEDGER p4 internal/store › internal/store/sql",
		"WILL ~ change the region keys of 1 pin in .seamark/lessons.yaml",
		"WON'T · change the rule, the live note, the comments or the position of a pin",
		"DIFF +1 −1", "region: internal/store/sql",
		"Retarget 1 pin · y writes lessons.yaml and the ledger", "y retarget 1 pin esc cancel",
	} {
		assert.Contains(t, text, want)
	}

	assert.True(t, maintHasKey(h, "y"))
	assert.Contains(t, maintFooter(h), "y retarget")
	assert.Empty(t, maintMutations(f))
}

func TestMaintRetargetWarnsAboutAWiderScope(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintWide(t)
	f := maintFakeOf(fx, p, nil)

	h := maintOpen(t, f, p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"Retarget 2 pins", "! repository-wide", "SELECTED 2 pins",
		"! Broader: the new regions reach paths that the current regions do not.",
		"+ repository-wide added",
		"! Repository-wide: the pin reaches every path in the repository",
		"p12 internal/api, internal/db › internal", "p9 internal/api › repository-wide",
		"Retarget 2 pins · repository-wide · y writes lessons.yaml and the ledger",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "the cursor row", "marked rows are not the cursor row")
}

func TestMaintRetargetKeepsTheEditedLiveNote(t *testing.T) {
	fx := maintFixture(t)
	p := fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true, cursor: true}, maintSpec{
		entry: maintNilErrors, status: maintain.ItemReady, to: []string{"internal/api/handlers"},
	})

	for _, size := range [][2]int{{120, 40}, {60, 20}} {
		f := maintFakeOf(fx, p, nil)
		h := maintOpen(t, f, p.Request, size[0], size[1])

		text := maintContent(t, h)
		assert.Contains(t, text, "LIVE NOTE stays in the file")
		assert.Contains(t, text, "RECORDED p9")
		// The columns interleave at 120 cells, so the checks take words
		// that one column line holds.
		assert.Contains(t, text, "Check every error returned by the", "the recorded note shows")
		assert.Contains(t, text, "and wrap it with the call site.", "the live note shows")
		assert.Contains(t, text, "The retarget keeps the live note; the recorded note of p9 stays as history.")

		mod := maintHostOf(h).modal
		body := mod.content(modalBodyWidth(modalMinW, size[0])).body
		side := false

		for _, l := range body {
			l = ansi.Strip(l)
			side = side || (strings.Contains(l, "LIVE NOTE") && strings.Contains(l, "RECORDED"))
		}

		assert.Equal(t, size[0] >= 120, side, "two columns only where each keeps 34 cells (%v)", size)
	}
}

func TestMaintRevisedSelectionHidesTheConfirm(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintMixed(t)
	f := maintFakeOf(fx, p, nil)

	h := maintOpen(t, f, p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"This preview shows the exact change, but it cannot be confirmed, so y is off. Nothing was changed.",
		"! 2 of 3 selected pins cannot change, so y is off: revise the selection. Unmark them in Pins with " +
			"space, then press w again.",
		"wrap-sql-errors ✓ ready", "close-db-handles · no-op", "Regions already current.",
		"keep-changelog-current ! blocked",
		"! No linked proposal; the console changes only proposal-linked pins",
		"LEDGER preview only", "DIFF +1 −1 preview only", "The diff holds the ready pins only.",
		"! preview only", "! Retarget 3 pins · y is off: revise the selection", "esc close",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "WILL", "a plan that y cannot confirm promises no effect")
	assert.NotContains(t, text, "y retarget")
	assert.False(t, maintHasKey(h, "y"), "no y without a confirmable plan")

	h.press("y")
	assert.Equal(t, []string{"PlanMaintenance:retarget"}, f.called(), "y does nothing on a plan to revise")
	assert.True(t, maintHostOf(h).open())
}

func TestMaintCursorRowThatCannotChangeNamesAnotherPin(t *testing.T) {
	fx := maintFixture(t)

	// The scope of the cursor row became current after the list was read.
	p := fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true, cursor: true}, maintSpec{
		entry: maintWrap, status: maintain.ItemNoOp, reason: "regions already current",
		to: []string{"internal/store"},
	})
	require.Nil(t, p.Change, "no ready pin, so no file change")

	h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"This preview has no change to confirm, so y is off. Nothing was changed.",
		"! The cursor row cannot change, so y is off. Press esc, move to another pin in Pins, then press w again.",
		"SELECTED 1 pin · the cursor row; no row is marked",
		"DIFF No file change: no selected pin is ready.",
		"! Retarget 1 pin · y is off: the cursor row cannot change",
	} {
		assert.Contains(t, text, want)
	}

	// Space would mark the same row, and w would show the same preview.
	for _, gone := range []string{"shows the exact change", "Unmark them", "revise the selection"} {
		assert.NotContains(t, text, gone)
	}

	assert.False(t, maintHasKey(h, "y"))
}

func TestMaintPruneNamesOnlyExistingProvenanceComments(t *testing.T) {
	fx := maintFixture(t)

	open := func(p *maintain.Plan) string {
		return maintContent(t, maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40))
	}

	// A hand-installed pin that was later recorded as applied has no
	// provenance comment, so the prune removes only its own lines.
	p := fx.maintPrune(t, false)
	p.Items[0].Pin.Entry.ProvenanceIDs = nil

	text := open(p)
	assert.Contains(t, text, "− remove 1 pin from .seamark/lessons.yaml")
	assert.NotContains(t, text, "provenance comment")

	// A mixed selection names only the pins that carry a comment.
	p = fx.plan(t, maintain.ActionPrune, maintPlanOpts{write: true},
		maintSpec{entry: maintWrap, status: maintain.ItemReady},
		maintSpec{entry: maintOnError, status: maintain.ItemReady})
	require.Equal(t, []int64{4}, p.Items[0].Pin.Entry.ProvenanceIDs, "the fixture comment names p4")

	assert.Contains(t, open(p), "− remove 2 pins from .seamark/lessons.yaml, with the provenance comments that "+
		"name p4, p16")

	p.Items[0].Pin.Entry.ProvenanceIDs = nil
	assert.Contains(t, open(p), "− remove 2 pins from .seamark/lessons.yaml, with the provenance comment that "+
		"names p16")
}

func TestMaintResultNamesWhatEscDrops(t *testing.T) {
	fx := maintFixture(t)

	// esc drops a proof that cannot come back. The pinned tail and the
	// keys say so at every size, so the user never needs to scroll for it.
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		h := maintOpen(t, maintFakeOf(fx, p, maintPartial(fx, true)), p.Request, size[0], size[1])
		h.press("y")
		require.NotNil(t, maintHostOf(h).modal.offer)

		shown := maintShown(h)
		assert.Contains(t, shown, "c previews completion · esc drops it", "the tail names esc at %v", size)
		assert.Contains(t, shown, "c preview the completion esc drop completion", "the keys at %v", size)
		assert.NotContains(t, shown, "dismiss", "%v", size)
		assert.Contains(t, maintFooter(h), "esc drop", "%v", size)

		// The lead names the consequence too, on the first screen of the
		// usual layouts.
		if size[1] >= 24 {
			assert.Contains(t, shown, "esc drops the completion, and it cannot come back.", "the lead at %v", size)
		}
	}
}

func TestMaintGateOffRoutesToSettings(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{cursor: true})
	f := maintFakeOf(fx, p, nil)

	h := maintOpen(t, f, p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"! preview only", "! distill.write is off, so this preview changes nothing. Press o for Settings › Pin " +
			"writing; after a save, press w in Pins for a new preview.",
		"DIFF +1 −1 preview only", "! Retarget 1 pin · y is off: distill.write is off",
		"o open Settings › Pin writing esc close",
	} {
		assert.Contains(t, text, want)
	}

	assert.False(t, maintHasKey(h, "y"))
	assert.True(t, maintHasKey(h, "o"))

	h.press("o")
	assert.Equal(t, destSettings, h.m.active, "o opens Settings › Pin writing")
	assert.False(t, maintHostOf(h).open(), "a preview of the old gate closes")
	assert.Equal(t, []string{"PlanMaintenance:retarget"}, f.called())

	// A gate that cannot be read has no Settings shortcut: the file
	// itself needs a fix, and a reader error never counts as off.
	q := fx.maintNarrow(t, maintPlanOpts{gateErr: errors.New("distill.write: not a boolean"), cursor: true})
	g := maintFakeOf(fx, q, nil)
	h = maintOpen(t, g, q.Request, 120, 40)

	assert.Contains(t, maintContent(t, h), "! distill.write cannot be read: distill.write: not a boolean. Fix "+
		".seamark/config.yaml first")
	assert.False(t, maintHasKey(h, "o"))

	h.press("o")
	assert.Equal(t, destLessons, h.m.active)
	assert.True(t, maintHostOf(h).open())
}

func TestMaintPruneNamesWhatLeavesAndWhatStays(t *testing.T) {
	fx := maintFixture(t)

	p := fx.maintPrune(t, true)
	h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"Prune p16 · close-db-handle-on-error",
		"Seamark removes 1 pin from .seamark/lessons.yaml and records p16 as superseded in the ledger.",
		"close-db-handle-on-error ✓ ready", "Regions internal/db",
		"Note Close the handle on every error path, also when the query fails.",
		"Keys unknown key owner (no reader uses it; it leaves with the entry)",
		"Retained close-db-handles (.seamark/lessons.yaml:" + itoa(fx.pins[maintHandles].Entry.Line) +
			" · p12): internal/api, internal/db Close database handles in the same function.",
		"Advisory: similar wording stays in 1 linked pin. It does not prove the same coverage; compare the notes " +
			"and the regions.",
		"LEDGER p16 applied › superseded",
		"− remove 1 pin from .seamark/lessons.yaml, with the provenance comment that names p16",
		"− move p16 from applied to superseded in the ledger",
		"· change other pins, also pins with the same rule in other regions",
		"· dismiss a proposal, or delete findings, signatures or the firing log",
		"· promise the same coverage from a similar pin",
		"DIFF −" + itoa(p.Change.Removed),
		"Prune 1 pin · p16 › superseded · y writes lessons.yaml and the ledger", "y prune 1 pin esc cancel",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "No retained equivalent")
	assert.NotContains(t, text, "Unknown owner", "the key never reads as an unknown owner")

	// Every label keeps a gap of two cells, also the longest one.
	body := ansi.Strip(strings.Join(maintHostOf(h).modal.content(modalBodyWidth(modalMinW, 120)).body, "\n"))
	assert.Contains(t, body, "Retained  close-db-handles")
	assert.Contains(t, body, "Keys      unknown key owner")

	p = fx.maintPrune(t, false)
	h = maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
	text = maintContent(t, h)

	assert.Contains(t, text, "! No retained equivalent is established; this is a deliberate retirement.")
	assert.Contains(t, text, "· promise the same coverage: no remaining pin is proven to replace a removed one")
	assert.NotContains(t, text, "Retained ")

	// A prune removes the live note, so an edited note shows beside the
	// recorded one.
	p = fx.plan(t, maintain.ActionPrune, maintPlanOpts{write: true},
		maintSpec{entry: maintNilErrors, status: maintain.ItemReady})
	h = maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
	text = maintContent(t, h)

	assert.Contains(t, text, "Note Check every error that the store returns before you use the value")
	assert.Contains(t, text, "Recorded Check every error returned by the store. (p9, as distilled; the live note "+
		"above differs)")
}

func TestMaintConfirmCommitsTheReviewedPlanOnce(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

	var got *maintain.Plan

	f := maintFakeOf(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		got = q

		return maintain.Result{
			Action: q.Action, FileWritten: true, LedgerDone: true, Moved: []int64{4},
			Note: "the pin file and the ledger changed together",
		}, nil
	})

	h := maintOpen(t, f, p.Request, 120, 40)
	pins := f.readCount("Pins")

	h.press("y")
	require.Same(t, p, got, "y commits the exact reviewed plan")
	assert.Equal(t, []string{"PlanMaintenance:retarget", "CommitMaintenance:retarget"}, f.called())
	assert.Empty(t, h.m.st.mutation, "the result frees the mutation slot")
	assert.Greater(t, f.readCount("Pins"), pins, "the result reads the pins again")

	text := maintContent(t, h)
	for _, want := range []string{
		"Retargeted 1 pin", "Wrote the new regions of 1 pin to .seamark/lessons.yaml and recorded them for p4.",
		"Pin file written: .seamark/lessons.yaml (1 pin retargeted)",
		"Ledger p4 internal/store › internal/store/sql",
		"Note The pin file and the ledger changed together.", "Nothing is retried automatically.",
		"✓ File written · ledger updated", "enter close",
	} {
		assert.Contains(t, text, want)
	}

	h.press("y")
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"), "a result never commits again")

	h.press("enter")
	assert.False(t, maintHostOf(h).open())
}

func TestMaintBusySlotHoldsTheConfirm(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)

	h := lesOpen(t, f, 120, 40)
	h.press("m")
	require.True(t, h.m.st.begin("Building the index"))
	h.send(openMaintMsg{req: p.Request})

	text := maintContent(t, h)
	assert.Contains(t, text, "! Building the index is running. y is off until it ends.")
	assert.Contains(t, text, "! Retarget 1 pin · y waits: another operation is running")
	assert.False(t, maintHasKey(h, "y"), "y needs a free mutation slot")

	// A y during the other operation does nothing. The modal covers the
	// toast row, so the visible tail tells the user that y waits.
	for _, size := range [][2]int{{120, 40}, {60, 20}} {
		h.resize(size[0], size[1])
		h.press("y")
		assert.Empty(t, maintMutations(f))
		assert.Empty(t, h.m.toast.text, "no toast hides under the modal (%v)", size)
		assert.True(t, maintHostOf(h).open(), "the preview stays for a later y")
		assert.Contains(t, maintShown(h), "y waits: another operation is running", "%v", size)
	}

	h.send(mutationDoneMsg{})
	assert.True(t, maintHasKey(h, "y"), "y returns when the slot is free")

	h.press("y")
	assert.Equal(t, []string{"CommitMaintenance:retarget"}, maintMutations(f), "a later y commits")
}

func TestMaintEscLeavesEveryOpenStageWithoutAChange(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)

	h := lesOpen(t, f, 120, 40)
	h.press("m")

	// The plan read is held, so the preview waits in its first stage.
	host := maintHostOf(h)
	read, consumed := host.route(openMaintMsg{req: p.Request})
	require.True(t, consumed)
	require.NotNil(t, read)
	require.Equal(t, maintStagePreparing, host.modal.stage)
	assert.Contains(t, maintContent(t, h), "Preparing the retarget preview")
	assert.Contains(t, maintContent(t, h), "Nothing is written.")
	assert.Equal(t, "esc cancel", ansi.Strip(h.m.st.theme.hints(host.keys())))

	h.press("esc")
	assert.False(t, host.open())

	h.run(read)
	assert.False(t, host.open(), "a late plan reopens nothing")

	h.send(openMaintMsg{req: p.Request})
	require.True(t, host.open())

	h.press("j", "pgdown", "G", "g")
	assert.Equal(t, maintStagePreview, host.modal.stage, "movement keys scroll")

	h.press("esc")
	assert.False(t, host.open())
	assert.Empty(t, maintMutations(f), "esc changes nothing")
	assert.Equal(t, 2, f.callCount("PlanMaintenance:retarget"))
}

func TestMaintCommittingWaitsForTheResult(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintPrune(t, false)
	f := maintFakeOf(fx, p, nil)

	h := maintOpen(t, f, p.Request, 120, 40)
	mod := maintHostOf(h).modal

	commit := mod.commit()
	require.Equal(t, maintStageCommitting, mod.stage)
	assert.Equal(t, "Pruning 1 pin", h.m.st.mutation)
	assert.Contains(t, maintContent(t, h), "The operation cannot stop safely; the result appears here.")
	assert.Empty(t, hookLessons(h).keys(), "a commit offers no key")

	h.press("esc", "y", "c", "enter", "o")
	assert.Equal(t, maintStageCommitting, mod.stage, "keys wait for the result")
	assert.True(t, maintHostOf(h).open())
	assert.Contains(t, h.view(), "Pruning 1 pin")

	h.run(commit)
	assert.Equal(t, maintStageResult, mod.stage)
	assert.Empty(t, h.m.st.mutation)
	assert.Equal(t, 1, f.callCount("CommitMaintenance:prune"))
	assert.Contains(t, maintContent(t, h), "Pruned 1 pin")
	assert.Contains(t, maintContent(t, h), "Ledger p16 › superseded")
}

func TestMaintPartialResultOffersOnlyAProvenRecovery(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

	var offered, previewed *maintain.Recovery

	partial := maintPartial(fx, true)
	f := maintFakeOf(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		res, err := partial(q)
		offered = res.Recovery

		return res, err
	})

	var completed *maintain.Plan

	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) {
		previewed = r

		return fx.recoveryPlan(r, true), nil
	}
	f.commitRecovery = func(q *maintain.Plan) (maintain.Result, error) {
		completed = q

		return maintain.Result{Action: q.Action, LedgerDone: true, Moved: []int64{4},
			Note: "the ledger now matches the pin file"}, nil
	}

	h := maintOpen(t, f, p.Request, 120, 40)
	h.press("y")

	text := maintContent(t, h)
	for _, want := range []string{
		"Regions written, ledger incomplete",
		"! The pin file changed, but the ledger update did not complete. Do not retarget again",
		"esc drops the completion, and it cannot come back.",
		"Pin file written: .seamark/lessons.yaml (1 pin retargeted)",
		"Ledger no change: p4 still records the earlier regions", "Ledger error store: database is locked",
		"Restore failed: integration: .seamark/lessons.yaml: read-only file system",
		"Rows now p4 applied, internal/store", "Note Reconciliation required:",
		"Recovery available: c previews the ledger-only completion; esc drops it",
		"Nothing is retried automatically.",
		"! File changed · ledger incomplete · c previews completion · esc drops it",
		"c preview the completion esc drop completion",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "dismiss", "esc drops a proof; it dismisses no proposal")
	assert.Contains(t, maintFooter(h), "esc drop")

	assert.True(t, maintHasKey(h, "c"))

	h.press("enter")
	assert.Equal(t, maintStageResult, maintHostOf(h).modal.stage, "enter never dismisses a recovery")

	h.press("c")
	require.Same(t, offered, previewed, "c previews the recovery of this result")

	text = maintContent(t, h)
	for _, want := range []string{
		"Complete the ledger update · p4", "ledger only",
		"The pin file still holds the result of this session's retarget.", "PROOF", "wrap-sql-errors ✓ proven",
		"LEDGER p4 internal/store › internal/store/sql", "WON'T · change .seamark/lessons.yaml",
		"Ledger only · p4 · y completes the ledger update", "y complete the ledger update esc back",
	} {
		assert.Contains(t, text, want)
	}

	h.press("y")
	require.NotNil(t, completed)
	assert.Same(t, offered, completed.Recovery, "y completes the proof under review")
	assert.Equal(t, []string{"PlanMaintenance:retarget", "CommitMaintenance:retarget", "PlanRecovery:retarget",
		"CommitRecovery:retarget"}, f.called())

	text = maintContent(t, h)
	assert.Contains(t, text, "Ledger completed")
	assert.Contains(t, text, "The ledger now matches the pin file: p4 records the new regions.")
	assert.Contains(t, text, "Pin file unchanged: a ledger-only completion never writes it")
	assert.False(t, maintHasKey(h, "c"), "a completed recovery is no longer offered")

	h.press("enter")
	assert.False(t, maintHostOf(h).open())
}

func TestMaintPartialResultWithoutProofOffersNoRecovery(t *testing.T) {
	fx := maintFixture(t)

	// The engine offers no recovery only for a write that reported a
	// failure and that no read confirmed. So the result never claims a
	// file change, and it names both sides and the manual route.
	cases := []struct {
		name string
		plan *maintain.Plan
		want []string
	}{
		{
			name: "prune", plan: fx.maintPrune(t, false),
			want: []string{
				"Ledger no change: p16 still applied",
				"If a pin is gone while the ledger still lists its proposal (p16) as applied, Pins shows that " +
					"proposal under not present. To reconcile, restore the entry with git if it is gone, then press - " +
					"in Pins for a new preview; or keep the file and accept the mismatch. Do not prune again while " +
					"the file keeps the change.",
			},
		},
		{
			name: "retarget", plan: fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true}),
			want: []string{
				"Ledger no change: p4 still records the earlier regions",
				"If the file holds the new regions while the ledger still records the earlier ones (p4), Pins shows " +
					"the mismatch. To reconcile, restore the earlier entry with git if it changed, then press w in " +
					"Pins for a new preview; or keep the file and accept the mismatch. Do not retarget again while " +
					"the file keeps the change.",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.plan.Action.String()
			f := maintFakeOf(fx, tc.plan, maintPartial(fx, false))

			h := maintOpen(t, f, tc.plan.Request, 120, 40)
			h.press("y")

			text := maintContent(t, h)
			for _, want := range append(tc.want,
				"Pin file state unknown, ledger incomplete",
				"! The pin file write reported a failure, and the file could not be read again, so its state is "+
					"unknown. This operation did not change the ledger. No completion can be proven, so none is "+
					"offered. Check the file with git diff .seamark/lessons.yaml.",
				"Pin file changed or not checked: the write reported a failure",
				"File error integration: .seamark/lessons.yaml: rename: disk full; the file could not be read "+
					"after the failure: permission denied",
				"Recovery none can be proven: reconcile by hand",
				"! File not checked · ledger incomplete · fix by hand",
			) {
				assert.Contains(t, text, want)
			}

			// The title and the tail never claim a change that no read saw.
			for _, gone := range []string{"Pin removed", "Regions written", "File changed", "File written"} {
				assert.NotContains(t, text, gone)
			}

			assert.False(t, maintHasKey(h, "c"))

			h.press("c")
			assert.Zero(t, f.callCount("PlanRecovery:"+a), "no recovery without a session proof")

			h.press("enter")
			assert.False(t, maintHostOf(h).open())
		})
	}
}

func TestMaintRecoveryPreviewKeepsTheOffer(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, maintPartial(fx, true))

	proven := true
	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) { return fx.recoveryPlan(r, proven), nil }

	h := maintOpen(t, f, p.Request, 120, 40)
	h.press("y", "c")
	mod := maintHostOf(h).modal
	require.True(t, mod.recovering)

	h.press("esc")
	assert.Equal(t, maintStageResult, mod.stage, "esc returns to the result")
	assert.NotNil(t, mod.offer)
	assert.True(t, maintHasKey(h, "c"))

	h.press("c")
	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.Equal(t, maintStageResult, mod.stage, "a save returns to the result and keeps the proof")
	assert.NotNil(t, mod.offer)
	assert.Contains(t, h.m.toast.text, "so the ledger completion preview closed. Press c to check it again.")

	proven = false
	h.press("c")

	text := maintContent(t, h)
	for _, want := range []string{
		"The result of the earlier operation is not proven now, so y is off. Nothing was changed.",
		"wrap-sql-errors ! not proven", "! The retargeted pin is not in the file with the reviewed content.",
		"Reconcile by hand. Check the file with git diff .seamark/lessons.yaml.",
		"restore the earlier entry with git if it changed, then press w in Pins for a new preview",
		"esc returns to the result, which keeps this offer until esc there drops it.",
		"LEDGER preview only", "! Ledger only · y is off: the result is not proven", "esc back",
	} {
		assert.Contains(t, text, want)
	}

	assert.False(t, maintHasKey(h, "y"))

	h.press("y")
	assert.Zero(t, f.callCount("CommitRecovery:retarget"), "an unproven completion never commits")

	// With the gate off, o opens Settings and the result keeps the proof,
	// because the proof lives only in this preview.
	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) {
		q := fx.recoveryPlan(r, true)
		q.Write, q.Confirmable = false, false

		return q, nil
	}

	// A gate that alone blocks y leaves the proof valid, so neither the
	// lead nor the tail says "not proven".
	h.press("esc", "c")
	text = maintContent(t, h)
	assert.Contains(t, text, "A fresh read proved the result of the earlier operation, but distill.write is off, "+
		"so y is off. Nothing was changed.")
	assert.Contains(t, text, "wrap-sql-errors ✓ proven")
	assert.Contains(t, text, "! distill.write is off. Press o for Settings › Pin writing")
	assert.Contains(t, text, "! Ledger only · y is off: distill.write is off")
	assert.NotContains(t, text, "not proven")
	assert.True(t, maintHasKey(h, "o"))

	h.press("o")
	assert.Equal(t, destSettings, h.m.active)
	assert.Equal(t, maintStageResult, mod.stage)
	assert.NotNil(t, mod.offer, "a visit to Settings keeps the proof")

	// A gate that cannot be read names the file fix and keeps the offer.
	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) {
		q := fx.recoveryPlan(r, true)
		q.Write, q.Confirmable, q.GateErr = false, false, errors.New("distill.write: not a boolean")

		return q, nil
	}

	h.press("3", "c")
	require.True(t, mod.recovering)

	text = maintContent(t, h)
	assert.Contains(t, text, "A fresh read proved the result of the earlier operation, but distill.write cannot "+
		"be read, so y is off.")
	assert.Contains(t, text, "! distill.write cannot be read: distill.write: not a boolean. Fix .seamark/config.yaml "+
		"first. esc returns to the result, which keeps this offer; press c there to check the proof again.")
	assert.Contains(t, text, "! Ledger only · y is off: distill.write cannot be read")
	assert.NotContains(t, text, "not proven")
	assert.False(t, maintHasKey(h, "o"), "a reader error needs a file fix, not the Settings shortcut")
}

func TestMaintRecoveryFailureKeepsTheOffer(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, maintPartial(fx, true))
	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) { return fx.recoveryPlan(r, true), nil }
	// CommitRecovery reads the rows again after a failed ledger write.
	observed := []domain.Proposal{p.Items[0].Rows[0]}
	f.commitRecovery = func(q *maintain.Plan) (maintain.Result, error) {
		return maintain.Result{Action: q.Action, LedgerErr: errors.New("store: database is locked"),
			Recovery: q.Recovery, Observed: observed, Note: "the ledger update failed again; nothing changed, and " +
				"the recovery stays available"}, nil
	}

	h := maintOpen(t, f, p.Request, 120, 40)
	h.press("y", "c", "y")

	mod := maintHostOf(h).modal
	text := maintContent(t, h)
	assert.Contains(t, text, "Ledger update failed again")
	assert.Contains(t, text, "Nothing changed. Press c to check the proof again. esc drops the completion, and it "+
		"cannot come back.")
	assert.Contains(t, text, "Ledger no change: p4 still records the earlier regions", "the rows hold their guards")
	assert.Contains(t, text, "Ledger error store: database is locked")
	assert.Contains(t, text, "! Ledger unchanged · c checks the proof again · esc drops it")
	require.NotNil(t, mod.offer)

	// A row that another writer changed is never called unchanged.
	dismissed := p.Items[0].Rows[0]
	dismissed.Status = domain.ProposalDismissed
	observed = []domain.Proposal{dismissed}

	h.press("c", "y")
	text = maintContent(t, h)
	assert.Contains(t, text, "Ledger not changed by this operation; current rows: see Rows now")
	assert.Contains(t, text, "Rows now p4 dismissed, internal/store")
	assert.NotContains(t, text, "still records")
	require.NotNil(t, mod.offer)

	// A refused completion wrote nothing, so the proof stays offered.
	f.commitRecovery = func(q *maintain.Plan) (maintain.Result, error) {
		return maintain.Result{Action: q.Action}, fmt.Errorf("%w: the pin file changed", maintain.ErrStale)
	}

	h.press("c", "y")
	text = maintContent(t, h)
	assert.Contains(t, text, "Not written")
	assert.Contains(t, text, "Press c to check the proof again. esc drops the completion, and it cannot come back.")
	assert.Contains(t, text, "! Nothing was written · c checks the proof again · esc drops it")
	assert.NotNil(t, mod.offer)
	assert.True(t, maintHasKey(h, "c"))
	assert.Equal(t, 3, f.callCount("CommitRecovery:retarget"))
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"), "a completion never repeats the file write")
}

func TestMaintStaleResultOffersAFreshPreview(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		return maintain.Result{Action: q.Action}, fmt.Errorf("%w: the pin file changed", maintain.ErrStale)
	})

	h := maintOpen(t, f, p.Request, 120, 40)
	h.press("y")

	text := maintContent(t, h)
	// The title says what Seamark did, so it never contradicts the lead,
	// which says that something else changed.
	assert.True(t, strings.HasPrefix(text, "Not written "), text)
	assert.NotContains(t, text, "Nothing changed")
	assert.Contains(t, text, "! Something changed since the preview — preview again. Nothing was written.")
	assert.Contains(t, text, "Details: the pin file, the ledger, the write gate, or the evidence changed since the "+
		"preview: the pin file changed")
	assert.Contains(t, text, "! Nothing was written · enter previews again")
	assert.Contains(t, text, "enter preview again esc close")
	assert.Empty(t, h.m.st.mutation, "a refusal frees the slot")

	h.press("enter")
	assert.Equal(t, maintStagePreview, maintHostOf(h).modal.stage, "enter builds a fresh preview")
	assert.Equal(t, 2, f.callCount("PlanMaintenance:retarget"))
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"), "nothing is retried automatically")
}

func TestMaintResultRowsStaySeparate(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	q := fx.maintPrune(t, false)

	// row returns the planned row of a plan as another writer left it.
	row := func(p *maintain.Plan, change func(r *domain.Proposal)) []domain.Proposal {
		r := p.Items[0].Rows[0]
		change(&r)

		return []domain.Proposal{r}
	}
	guardMiss := errors.New("store: the row changed since the plan")
	unread := errors.Join(errors.New("integration: rename failed"),
		errors.New("the file could not be read after the failure: permission denied"))
	other := "reconciliation required: the ledger rows changed in another way, so nothing was restored"

	cases := []struct {
		name  string
		plan  *maintain.Plan
		offer bool
		res   maintain.Result
		want  []string
		gone  []string
	}{
		{
			name: "restored",
			res: maintain.Result{FileWritten: true, LedgerErr: errors.New("store: disk I/O error"), Restored: true,
				Note: "the ledger update failed; the pin file holds the reviewed content again, and the ledger did " +
					"not change"},
			want: []string{"Nothing changed", "The ledger update failed, so Seamark put the earlier pin file back.",
				"Fix the cause named under Ledger error, then press w in Pins for a new preview.",
				"Pin file restored: it holds its earlier content again", "Ledger no change: p4",
				"Ledger error store: disk I/O error", "Restore done: the earlier pin file is back",
				"! File restored · ledger unchanged", "enter close"},
			gone: []string{"Recovery"},
		},
		{
			name: "file stale",
			res: maintain.Result{FileErr: fmt.Errorf("%w: pinfile: the file changed", maintain.ErrStale),
				Note: "the pin file write failed; this operation changed nothing"},
			want: []string{"Pin file not written", "Another writer changed .seamark/lessons.yaml first.",
				"Pin file not changed", "File error", "Ledger no change", "! File unchanged · ledger unchanged",
				"enter preview again"},
			gone: []string{"Restore ", "Fix the cause"},
		},
		{
			// A write error that is not a stale file needs a fix first;
			// enter only closes, so the lead names the Pins key.
			name: "file not written", plan: q,
			res: maintain.Result{FileErr: errors.New("integration: .seamark/lessons.yaml: read-only file system"),
				Note: "the pin file write failed; this operation changed nothing"},
			want: []string{"Pin file not written", "The pin file was not changed, and the ledger was not changed. " +
				"Fix the cause named under File error, then press - in Pins for a new preview.",
				"File error integration: .seamark/lessons.yaml: read-only file system", "enter close"},
			gone: []string{"enter preview again"},
		},
		{
			name: "completed elsewhere",
			res: maintain.Result{FileWritten: true, LedgerDone: true, LedgerErr: errors.New("store: busy"),
				Note: "the ledger was completed by another writer"},
			want: []string{"Ledger completed elsewhere", "The pin file changed, and another writer already recorded",
				"Ledger completed by another writer: p4 internal/store › internal/store/sql",
				"! File written · ledger completed elsewhere"},
		},
		{
			// A failed write that no read confirmed, while another writer
			// completed the ledger: nothing claims the file changed.
			name: "completed elsewhere, file not checked",
			res: maintain.Result{FileWritten: true, LedgerDone: true, FileErr: unread,
				Note: "the ledger was completed by another writer"},
			want: []string{"Ledger completed elsewhere", "The pin file write reported a failure, so Seamark cannot " +
				"say what the file holds. Check it with git diff .seamark/lessons.yaml before you rely on it",
				"Pin file changed or not checked: the write reported a failure",
				"File error integration: rename failed; the file could not be read after the failure: permission " +
					"denied",
				"! File changed or not checked · ledger completed elsewhere"},
			gone: []string{"File written", "The pin file changed,", "Nothing more is needed"},
		},
		{
			name: "retarget, row status changed", offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, Note: other,
				Observed: row(p, func(r *domain.Proposal) { r.Status = domain.ProposalDismissed })},
			want: []string{"Ledger not changed by this operation; current rows: see Rows now",
				"Rows now p4 dismissed, internal/store", "Note Reconciliation required: the ledger rows changed"},
			gone: []string{"still records"},
		},
		{
			name: "retarget, row regions changed", offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, Note: other,
				Observed: row(p, func(r *domain.Proposal) { r.Region = "internal/api" })},
			want: []string{"Ledger not changed by this operation; current rows: see Rows now",
				"Rows now p4 applied, internal/api"},
			gone: []string{"still records"},
		},
		{
			name: "retarget, row missing", offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, Note: other, Observed: []domain.Proposal{}},
			want: []string{"Ledger not changed by this operation; current rows: see Rows now",
				"Rows now p4 not in the ledger"},
			gone: []string{"still records"},
		},
		{
			name: "retarget, rows unread", offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, ObservedErr: errors.New("store: disk I/O error"),
				Note: "reconciliation required: the ledger rows could not be read again, so nothing was restored"},
			want: []string{"Ledger not changed by this operation; current state unknown",
				"Rows now unknown: store: disk I/O error"},
			gone: []string{"still records"},
		},
		{
			name: "prune, row status changed", plan: q, offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, Note: other,
				Observed: row(q, func(r *domain.Proposal) { r.Status = domain.ProposalDismissed })},
			want: []string{"Ledger not changed by this operation; current rows: see Rows now",
				"Rows now p16 dismissed, internal/db"},
			gone: []string{"still applied"},
		},
		{
			name: "prune, rows unread", plan: q, offer: true,
			res: maintain.Result{FileWritten: true, LedgerErr: guardMiss, ObservedErr: errors.New("store: disk I/O error"),
				Note: "reconciliation required: the ledger rows could not be read again, so nothing was restored"},
			want: []string{"Ledger not changed by this operation; current state unknown",
				"Rows now unknown: store: disk I/O error"},
			gone: []string{"still applied"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := tc.plan
			if plan == nil {
				plan = p
			}

			res := tc.res
			res.Action = plan.Action

			if tc.offer {
				res.Recovery = fx.recovery(plan)
			}

			f := maintFakeOf(fx, plan, func(*maintain.Plan) (maintain.Result, error) { return res, nil })
			h := maintOpen(t, f, plan.Request, 120, 40)
			h.press("y")

			text := maintContent(t, h)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}

			for _, gone := range tc.gone {
				assert.NotContains(t, text, gone)
			}

		})
	}
}

func TestMaintSettingsSaveClosesAPreviewOnly(t *testing.T) {
	fx := maintFixture(t)

	p := fx.maintPrune(t, false)
	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 120, 40)

	h.send(settingsSavedMsg{group: settings.GroupInference})
	assert.False(t, maintHostOf(h).open(), "any save closes a preview (D-14)")
	assert.Contains(t, h.m.toast.text, "so the prune preview closed. Press - in Pins to preview it again.")

	h.send(openMaintMsg{req: p.Request})
	h.press("y")
	require.Equal(t, maintStageResult, maintHostOf(h).modal.stage)

	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.True(t, maintHostOf(h).open(), "a result stays after a save")
	assert.Equal(t, 1, f.callCount("CommitMaintenance:prune"))
}

func TestMaintPreviewFailureExplains(t *testing.T) {
	fx := maintFixture(t)
	f := maintFakeOf(fx, nil, nil)
	f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
		return nil, fmt.Errorf("%w (not indexed)", ErrNoLedger)
	}

	req := maintain.Request{Action: maintain.ActionRetarget, Cursor: true,
		Refs: []pinfile.EntryRef{fx.pins[maintWrap].Entry.Ref()}}

	h := maintOpen(t, f, req, 120, 40)
	text := maintContent(t, h)
	assert.Contains(t, text, "Preview failed")
	assert.Contains(t, text, "! The retarget preview could not be built:")
	assert.Contains(t, text, "Build it from Overview (press 1, then b).")
	assert.Contains(t, text, "Nothing was changed.")
	assert.False(t, maintHasKey(h, "y"))

	h.press("esc")
	assert.False(t, maintHostOf(h).open())

	f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) { return nil, nil }
	h.send(openMaintMsg{req: req})
	assert.Contains(t, maintContent(t, h), "the preview came back empty")
	assert.Empty(t, maintMutations(f))
}

func TestMaintChangedNamesOnlyWrittenEntries(t *testing.T) {
	refs := []pinfile.EntryRef{{Index: 2}}
	p := &maintain.Plan{Request: maintain.Request{Refs: refs}}

	assert.Equal(t, refs, maintChanged(maintCommittedMsg{plan: p, res: maintain.Result{FileWritten: true,
		LedgerDone: true}}))
	assert.Equal(t, refs, maintChanged(maintCommittedMsg{plan: p, res: maintain.Result{FileWritten: true}}),
		"a partial commit changed the entry")
	assert.Equal(t, refs, maintChanged(maintCommittedMsg{plan: p, res: maintain.Result{LedgerDone: true}}),
		"a completion changed the link")
	assert.Nil(t, maintChanged(maintCommittedMsg{plan: p, res: maintain.Result{FileWritten: true, Restored: true}}),
		"a restored file keeps the selection")
	assert.Nil(t, maintChanged(maintCommittedMsg{plan: p, err: maintain.ErrStale}), "a refusal changes nothing")
}

func TestMaintRefusalsSayNothingWasWritten(t *testing.T) {
	th := newTheme(false)

	for _, err := range []error{
		maintain.ErrStale, maintain.ErrWriteDisabled, maintain.ErrNotConfirmable, ErrBusy, ErrNoLedger,
		maintain.ErrNoLedger, errors.New("other"),
	} {
		text := maintRefusal(th, fmt.Errorf("wrapped: %w", err))
		assert.Regexp(t, `nothing (was|is) (written|changed)|did not start|refused before any write`,
			strings.ToLower(text), "%v", err)
	}
}

// maintHostile is text with an OSC title, a bell, an SGR sequence and
// a one-byte CSI. None of it may reach the terminal.
const maintHostile = "evil\x1b]0;pwned\x07\x1b[31m\u009b2J"

// maintHostilePlan builds a plan whose every display field holds
// hostile text.
func maintHostilePlan(a maintain.Action) *maintain.Plan {
	row := domain.Proposal{ID: 7, Rule: "r" + maintHostile, Note: "recorded " + maintHostile,
		Status: domain.ProposalApplied, Region: "api" + maintHostile}
	pin := maintain.Pin{
		Entry: pinfile.Entry{Line: 3, Rule: "rule-" + maintHostile, Note: "note " + maintHostile,
			Region: "api" + maintHostile, Unknown: []string{"key" + maintHostile}},
		Link: pinfile.EntryLink{Link: pinfile.LinkLinked, NoteChanged: true, Applied: []domain.Proposal{row}},
	}
	kept := maintain.Pin{Entry: pinfile.Entry{Line: 9, Rule: "kept" + maintHostile, Region: "db" + maintHostile},
		Link: pinfile.EntryLink{Applied: []domain.Proposal{{ID: 8}}}}

	ready := maintain.PlanItem{
		Ref: pinfile.EntryRef{Key: reviews.NewPinKey("k", "", nil)}, Pin: pin, Status: maintain.ItemReady,
		From: []string{"api" + maintHostile}, To: []string{"db" + maintHostile}, Broader: true,
		Facts: []string{"fact " + maintHostile}, Rows: []domain.Proposal{row}, Kept: []maintain.Pin{kept},
	}
	blocked := maintain.PlanItem{Ref: pinfile.EntryRef{Index: 4, Key: reviews.NewPinKey("gone"+maintHostile, "", nil)},
		Reason: "reason " + maintHostile}

	p := &maintain.Plan{
		Action: a, Request: maintain.Request{Action: a, Refs: []pinfile.EntryRef{ready.Ref, blocked.Ref}},
		Items: []maintain.PlanItem{ready, blocked},
		Change: &pinfile.Change{Diff: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old " + maintHostile + "\n+new " + maintHostile +
			"\n", Added: 1, Removed: 1},
		FileBlock: "block " + maintHostile, Blocked: []string{"blocked " + maintHostile},
	}

	guard := store.NewRowGuard(row)
	if a == maintain.ActionPrune {
		p.Supersede = []store.RowGuard{guard}
	} else {
		p.Retarget = []store.RegionChange{{Guard: guard, Region: "db" + maintHostile, Regions: []string{"db" + maintHostile}}}
	}

	return p
}

func TestMaintHostileTextStaysInert(t *testing.T) {
	hostileErr := errors.New("failed " + maintHostile)

	for _, a := range []maintain.Action{maintain.ActionRetarget, maintain.ActionPrune} {
		p := maintHostilePlan(a)
		p.GateErr = hostileErr
		recovery := &maintain.Recovery{Action: a, Request: p.Request, Items: p.Items, Retarget: p.Retarget,
			Supersede: p.Supersede}

		f := newFake()
		f.ledger = lesLedger()
		f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) { return p, nil }
		f.planRecovery = func(*maintain.Recovery) (*maintain.Plan, error) {
			q := *p
			q.Recovery = recovery

			return &q, nil
		}

		for _, ascii := range []bool{false, true} {
			for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
				h := &harness{t: t, m: newModel(f, newTheme(ascii))}
				h.run(h.m.Init())
				h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				h.press("3", "m")
				h.send(openMaintMsg{req: p.Request})

				mod := maintHostOf(h).modal
				require.NotNil(t, mod)

				check := func(stage string) {
					t.Helper()

					v := mod.content(modalBodyWidth(modalMinW, size[0]))
					all := strings.Join(append(append([]string{v.title, v.right}, v.body...), v.tail...), "\n") +
						h.m.View()

					for _, bad := range []string{"\x1b", "\x07", "\u009b"} {
						assert.NotContains(t, all, bad, "%s %s at %v (ascii %t)", a, stage, size, ascii)
					}

					frame := h.view()
					assertFrame(t, frame, size[0], size[1])

					if ascii {
						for _, r := range frame {
							require.Less(t, r, rune(128), "%s %s at %v", a, stage, size)
						}
					}
				}

				check("preview")

				// The result, the recovery preview and a refusal hold
				// engine errors and ledger rows.
				mod.stage, mod.offer = maintStageResult, recovery
				mod.out = &maintOutcome{plan: p, res: maintain.Result{
					Action: a, FileWritten: true, FileErr: hostileErr, LedgerErr: hostileErr, RestoreErr: hostileErr,
					RestoreRefused: "refused " + maintHostile, ObservedErr: nil,
					Observed: []domain.Proposal{{ID: 7, Status: "applied" + maintHostile, Region: "x" + maintHostile}},
					Note:     "note " + maintHostile, Recovery: recovery,
				}}
				check("partial result")

				mod.out.res.Observed, mod.out.res.ObservedErr = nil, hostileErr
				check("unread rows")

				mod.out.err = hostileErr
				check("refusal")

				mod.stage, mod.recovering, mod.plan, mod.err = maintStagePreview, true, nil, nil
				h.run(mod.prepareRecovery())
				check("recovery preview")

				mod.recovering, mod.plan, mod.err = false, nil, hostileErr
				check("plan failure")
			}
		}
	}
}

func TestMaintLayoutsKeepTheScopeVisible(t *testing.T) {
	fx := maintFixture(t)

	scenes := []struct {
		name  string
		plan  func(t *testing.T) *maintain.Plan
		keys  []string
		label string
	}{
		{"retarget", func(t *testing.T) *maintain.Plan {
			return fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		}, nil, "y retarget 1 pin"},
		{"wide", fx.maintWide, nil, "y retarget 2 pins"},
		{"revise", fx.maintMixed, nil, "esc close"},
		{"gate off", func(t *testing.T) *maintain.Plan {
			return fx.maintNarrow(t, maintPlanOpts{cursor: true})
		}, nil, "o open Settings › Pin writing"},
		{"prune", func(t *testing.T) *maintain.Plan { return fx.maintPrune(t, true) }, nil, "y prune 1 pin"},
		{"partial", func(t *testing.T) *maintain.Plan {
			return fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		}, []string{"y"}, "c preview the completion"},
		{"recovery", func(t *testing.T) *maintain.Plan {
			return fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		}, []string{"y", "c"}, "y complete the ledger update"},
	}

	for _, sc := range scenes {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			p := sc.plan(t)
			f := maintFakeOf(fx, p, maintPartial(fx, true))
			f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) { return fx.recoveryPlan(r, true), nil }

			h := maintOpen(t, f, p.Request, size[0], size[1])
			h.press(sc.keys...)

			frame := h.view()
			assertFrame(t, frame, size[0], size[1])

			v := maintHostOf(h).modal.content(modalBodyWidth(modalMinW, size[0]))
			require.NotEmpty(t, v.tail, "%s keeps a scope summary", sc.name)

			first := strings.TrimSpace(ansi.Strip(v.tail[0]))
			assert.Contains(t, frame, first, "%s at %v shows its scope summary", sc.name, size)
			assert.Contains(t, frame, sc.label, "%s at %v shows its action", sc.name, size)
		}
	}
}

// The regression tests below hold the review findings of the retarget
// and prune preview, over the fixture of pins_modal_fixture_test.go.

func TestMaintFixtureBackdropCountsEveryCitation(t *testing.T) {
	fx := maintFixture(t)
	f := maintFakeOf(fx, fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true}), nil)
	h := pinsOpen(t, f, 120, 40)

	// The captures of scenes 80-89 show this detail behind the preview,
	// so its counts must agree with the facts of the readings.
	text := pinsDetail(t, h)
	assert.Contains(t, text, "1/2 citations living")
	assert.Contains(t, text, "Citations 1 living of 2 cited")
	assert.Contains(t, text, "? finding 200: no longer in the index (aged out, reverted or removed)")
	assert.NotContains(t, text, "of 0 cited")
}

// maintCollision marks p4 and p9 for a retarget. The computed regions of
// p4, internal/store/sql, are what an applied proposal without a pin
// (p40) already records, so the pin file planner refuses that target.
// Pins offers w for the selection, because p9 can change.
func maintCollision(t *testing.T, fx maintFix) (*maintain.Plan, *fakeServices) {
	t.Helper()

	p := fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true}, maintSpec{
		entry: maintWrap, reason: "the target key is already in use: an applied proposal already records the key " +
			"wrap-sql-errors @ internal/store/sql",
	}, maintSpec{
		entry: maintNilErrors, status: maintain.ItemReady, to: []string{"internal/api/handlers"}, narrower: true,
		facts: []string{"no confirmed trigger: the scope is the coverage of 1 living finding(s)"},
	})
	p.Items[0].Pin.Readings.Scope = maintain.ScopeReading{Known: true, Advice: distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Regions: []string{"internal/store/sql"}, Narrower: true,
	}}

	// The links of the fixture are shared by every scene, so the extra
	// row goes to a copy.
	f := maintFakeOf(fx, p, nil)
	links := *f.pins.Links
	links.NotPresent = append(slices.Clone(links.NotPresent), domain.Proposal{
		ID: 40, Status: domain.ProposalApplied, Rule: "wrap-sql-errors", Region: "internal/store/sql",
	})
	f.pins.Links = &links

	return p, f
}

func TestMaintBlockedRetargetShowsItsRegionsAndTheOwner(t *testing.T) {
	fx := maintFixture(t)
	p, f := maintCollision(t, fx)

	h := maintOpen(t, f, p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"wrap-sql-errors ! blocked", "check-nil-errors ✓ ready",
		"− internal/store removed", "+ internal/store/sql added",
		"The applied proposal p40 already records this rule with the computed regions.",
		"! The target key is already in use: an applied proposal already records the key " +
			"wrap-sql-errors @ internal/store/sql.",
	} {
		assert.Contains(t, text, want)
	}

	assert.False(t, maintHasKey(h, "y"))

	// Any other blocked item has no computed regions to show.
	q := fx.maintMixed(t)
	h = maintOpen(t, maintFakeOf(fx, q, nil), q.Request, 120, 40)
	assert.NotContains(t, maintContent(t, h), "already has this rule")
}

func TestMaintForeignIndexRootTurnsOffY(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)
	f.pins.Index.Root = "/work/original"

	// The Pins keys stay off, but enter after a refusal plans again
	// without them, so the preview holds its own block.
	h := maintOpen(t, f, p.Request, 120, 40)
	text := maintContent(t, h)

	for _, want := range []string{
		"This preview shows the exact change, but it cannot be confirmed, so y is off.",
		"! The index of this workspace records another repository, /work/original, so y is off. A commit would " +
			"write /work/original/.seamark/lessons.yaml and the ledger of this workspace, not " +
			"/work/repo/.seamark/lessons.yaml. Run seamark index in this checkout",
		"! Retarget 1 pin · y is off: the index records another repository",
		"LEDGER preview only",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "WILL")
	assert.False(t, maintHasKey(h, "y"))
	assert.True(t, p.Confirmable, "the plan of the engine stays as it is")

	h.press("y")
	assert.Empty(t, maintMutations(f), "y writes no repository")
	assert.Contains(t, maintShown(h), "y is off: the index records another repository")
}

func TestMaintRetargetDisclosesAStaleIndex(t *testing.T) {
	fx := maintFixture(t)

	stale := func(f *fakeServices) {
		st := *f.index.Status
		st.Freshness = status.FreshStale
		f.index.Status = &st
	}

	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	note := "! Scope advice reads a stale index built 2h ago. Build it from Overview (press 1, then b) to compute " +
		"the regions from the current history."

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		f := maintFakeOf(fx, p, nil)
		stale(f)
		h := maintOpen(t, f, p.Request, size[0], size[1])

		text := maintContent(t, h)
		assert.Contains(t, text, note, "%v", size)
		assert.Contains(t, text, "! Retarget 1 pin · index stale · y writes lessons.yaml and the ledger", "%v", size)
		assert.True(t, maintHasKey(h, "y"), "D-16: a stale index still plans a retarget")

		// The tail stays on screen at every size, also when the body
		// scrolls.
		assert.Contains(t, maintShown(h), "index stale", "%v", size)
	}

	// A current index, and a prune that reads no scope, say nothing.
	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 120, 40)
	assert.NotContains(t, maintContent(t, h), "stale")

	q := fx.maintPrune(t, false)
	f = maintFakeOf(fx, q, nil)
	stale(f)
	h = maintOpen(t, f, q.Request, 120, 40)
	assert.NotContains(t, maintContent(t, h), "stale")
}

func TestMaintPinGoneSinceTheRequestIsNamedByItsRule(t *testing.T) {
	fx := maintFixture(t)

	// gone is the plan of a request whose entry left the file: the
	// engine gives a zero Pin and its changed reason.
	gone := func() *maintain.Plan {
		p := fx.plan(t, maintain.ActionPrune, maintPlanOpts{write: true, cursor: true}, maintSpec{
			entry: maintWrap, reason: "the pin changed since the list was read; refresh the Pins view",
		})
		p.Items[0].Pin = maintain.Pin{}

		return p
	}

	cases := []struct {
		name string
		in   func(in *maintain.Inspection)
		want []string
		gone []string
	}{
		{"removed", func(in *maintain.Inspection) { in.Pins = in.Pins[1:] }, []string{
			"wrap-sql-errors ! blocked", ".seamark/lessons.yaml · no longer in the file",
			"! No pin in .seamark/lessons.yaml has this rule and these regions now. It was removed, or its rule " +
				"or regions changed.",
		}, []string{"refresh the Pins view", "Press r"}},
		{"changed", func(in *maintain.Inspection) { in.Pins[0].Entry.Raw += "  owner: team\n" }, []string{
			".seamark/lessons.yaml · changed since the list was read",
			"! The pin changed since the list was read. The Pins list shows its current content.",
		}, []string{"refresh the Pins view", "Press r", "no longer in the file"}},
		{"list not read again", func(*maintain.Inspection) {}, []string{
			"! The pin changed since the list was read. Press r in Pins to read the list again.",
		}, []string{"no longer in the file"}},
	}

	for _, c := range cases {
		p := gone()
		f := maintFakeOf(fx, p, nil)
		c.in(f.pins)

		h := maintOpen(t, f, p.Request, 120, 40)
		text := maintContent(t, h)

		for _, want := range c.want {
			assert.Contains(t, text, want, c.name)
		}

		for _, g := range append(c.gone, "entry 1") {
			assert.NotContains(t, text, g, c.name)
		}
	}
}

func TestMaintBlockScalarNotesHaveNoTrailingBlankRow(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintPrune(t, true)
	p.Items[0].Pin.Entry.Note = "Close the handle on every error path.\nAlso when the query fails.\n"
	p.Items[0].Kept[0].Entry.Note = "Close database handles.\nIn the same function.\n"

	h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
	body := maintHostOf(h).modal.content(modalBodyWidth(modalMinW, 120)).body
	found := 0

	for i, l := range body {
		l = strings.TrimSpace(ansi.Strip(l))
		if l != "Also when the query fails." && l != "In the same function." {
			continue
		}

		found++

		require.Less(t, i+1, len(body))
		assert.NotEmpty(t, strings.TrimSpace(ansi.Strip(body[i+1])), "no blank row follows %q", l)
	}

	assert.Equal(t, 2, found, "the live note and the retained note both show")
}
