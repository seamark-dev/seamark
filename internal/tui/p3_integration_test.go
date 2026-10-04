package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold the cross-slice items of the Phase 3
// integration (C0). Each item joins files of two slices, so each test
// drives the real model through the harness from the key that the user
// presses to the frame that the user sees.

// C0 item 1, contracts D-15: the Proposals sub-view tells the review
// whether the cursor row stands in for an empty selection.
func TestExtractReviewNamesTheCursorRowFromProposals(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := newFake()
	f.ledger = lesLedger()
	f = p3ExtractFake(f, f.ledger.Pending, &reqs)
	f.runExtract = extNoRun(t)

	h := lesOpen(t, f, 120, 40)

	// Nothing is marked, so the cursor row p1 is the target.
	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{1}, reqs[0].IDs)
	require.Equal(t, extReview, extRecOf(t, h).step)
	assert.True(t, extRecOf(t, h).cursor)
	assert.Contains(t, h.view(), "selected 1 (cursor row)")
	assert.Contains(t, extTallFlat(h), "Selected 1 eligible of 1 · the cursor row; no row is marked")

	// esc returns to the origin, as the shell fix of slice B1 does.
	h.press("esc")
	require.Equal(t, lesProposals, hookLessons(h).sub)

	// Marked rows are the targets, so the review names no cursor row.
	h.press("space", "down", "space", "t")
	require.Len(t, reqs, 2)
	assert.Equal(t, []int64{1, 2}, reqs[1].IDs)
	assert.False(t, extRecOf(t, h).cursor)
	assert.NotContains(t, h.view(), "cursor row")
	assert.NotContains(t, extTallFlat(h), "cursor row")

	h.press("esc")
	assert.Equal(t, lesProposals, hookLessons(h).sub, "a marked request returns to its origin too")
	assert.Zero(t, f.callCount("RunExtract"))
}

// C0 item 1, contracts D-15: the Pins sub-view tells the review whether
// the cursor pin stands in for an empty selection.
func TestExtractReviewNamesTheCursorRowFromPins(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := p3ExtractFake(pinsFake(), p3LinkedRows(pinsInspection()), &reqs)
	f.runExtract = extNoRun(t)

	h := pinsOpen(t, f, 120, 40)

	// Pin 2 has an open question, so t asks about its proposal p9.
	pinsCursorTo(h, 2)
	require.True(t, maintHasKey(h, "t"))

	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{9}, reqs[0].IDs)
	assert.True(t, extRecOf(t, h).cursor)
	assert.Contains(t, h.view(), "selected 1 (cursor row)")
	assert.Contains(t, extTallFlat(h), "the cursor row; no row is marked")

	h.press("esc")
	require.Equal(t, lesPins, hookLessons(h).sub, "esc returns to Pins")

	// A marked pin is the target, so the review names no cursor row.
	pinsCursorTo(h, 2)
	h.press("space")
	require.Len(t, pinsOf(h).selected, 1)

	h.press("t")
	require.Len(t, reqs, 2)
	assert.Equal(t, []int64{9}, reqs[1].IDs)
	assert.False(t, extRecOf(t, h).cursor)
	assert.NotContains(t, h.view(), "cursor row")
	assert.NotContains(t, extTallFlat(h), "cursor row")

	h.press("esc")
	assert.Equal(t, lesPins, hookLessons(h).sub)
	assert.Zero(t, f.callCount("RunExtract"))
}

// C0 item 1: n on a result prepares the open questions that the run
// left. The run chose those IDs, so the new review names no cursor row,
// also when the first review came from a cursor row.
func TestExtractPrepareRemainingNamesNoCursorRow(t *testing.T) {
	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, extConflictExec)

	h := newHarness(t, f, 120, 40)
	h.send(openExtractMsg{req: p.Request, cursor: true})
	require.True(t, extRecOf(t, h).cursor)

	h.press("y")

	first := extRecOf(t, h)
	require.Equal(t, extResult, first.step)
	require.NotEmpty(t, first.remaining())

	h.press("n")

	next := extRecOf(t, h)
	require.NotSame(t, first, next, "n opens a new review")
	assert.Equal(t, extReview, next.step)
	assert.False(t, next.cursor)
	assert.NotContains(t, h.view(), "cursor row")
}

// C0 item 2: an earlier extraction record, shown with [, offers no enter
// and no n. The Run sub-view passes an earlier record only the movement
// keys, so the frame must not show those keys either.
func TestEarlierExtractionRecordShowsNoResultKeysInTheRunView(t *testing.T) {
	h := extResultOf(t, extTwoBatches(), 120, 40, extConflictExec)
	rec := extRecOf(t, h)

	// p4 stored trigger paths, so its result row links Pins.
	h.press("down")
	h.resize(80, 120)
	require.Contains(t, lesBoxFlat(h.view()), "NEXT enter show p4 in Lessons › Pins")

	// A later record makes the extraction an earlier run.
	h.send(openRunMsg{rec: runRec(h, "Distillation"), show: true})
	require.Len(t, runViewOf(h).past, 1)
	require.Same(t, rec, runViewOf(h).past[0])

	h.press("[")
	require.True(t, runViewOf(h).browsing())

	frame := h.view()
	flat := lesBoxFlat(frame)
	assert.Contains(t, frame, "Earlier run 1 of 1")
	assert.Contains(t, flat, "Trigger extraction finished with a conflict", "the result itself stays")
	assert.NotContains(t, flat, "NEXT")
	assert.NotContains(t, flat, "enter show")
	assert.NotContains(t, flat, "prepare a new preview")
	assert.NotContains(t, shellAFooter(frame), "enter", "an extraction has no proposals to review")

	// The detail pane is a movement away, and it names no enter either.
	h.press("tab")
	assert.NotContains(t, lesBoxFlat(h.view()), "enter shows")

	// The read-only form lasts for the render only.
	assert.False(t, rec.browsed)
}

// C0 item 3: the pinned scope summary of a prune names a bounded ID list,
// so the clause that says what y does stays visible at the minimum size.
func TestMaintPreviewOfAHugePruneKeepsTheScopeClause(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		fx := maintFixture(t)
		p := fx.maintPrune(t, false)

		for i := range 150 {
			p.Supersede = append(p.Supersede, store.NewRowGuard(domain.Proposal{ID: int64(1000 + i)}))
		}

		f := maintFakeOf(fx, p, nil)
		h := maintOpen(t, f, p.Request, 60, 20)
		h.m.st.theme = newTheme(ascii)
		require.True(t, maintHostOf(h).open())

		frame := h.view()
		assertFrame(t, frame, 60, 20)

		// maintShown drops the Unicode side borders; the ASCII border is
		// a plain bar.
		g := h.m.st.theme.Glyphs
		shown := lesFlat(strings.ReplaceAll(maintShown(h), "|", " "))
		assert.Contains(t, shown, "Prune 1 pin "+g.Sep+" p16, p1000, p1001, "+g.Ellipsis+" (+148) "+g.Arrow+
			" superseded "+g.Sep+" y writes lessons.yaml and the ledger", "ascii %v", ascii)
		assert.NotContains(t, shown, "p1149 "+g.Arrow, "the summary names no full list")
		assert.Contains(t, maintContent(t, h), "p1149", "the LEDGER section of the body lists every row")

		if ascii {
			for _, r := range ansi.Strip(frame) {
				require.Less(t, r, rune(128), "an ASCII frame has no wide glyph")
			}
		}

		assert.Empty(t, maintMutations(f))
	}
}

// C0 item 3: a short ID list stays whole in the summary.
func TestMaintTailIDListBoundsOnlyALongList(t *testing.T) {
	th := newTheme(false)

	assert.Equal(t, "p1, p2, p3", maintTailIDList(th, []int64{1, 2, 3}))
	assert.Equal(t, "p1, p2, p3, … (+1)", maintTailIDList(th, []int64{1, 2, 3, 4}))
	assert.Equal(t, "p1, p2, p3, ... (+2)", maintTailIDList(newTheme(true), []int64{1, 2, 3, 4, 5}))
	assert.Empty(t, maintTailIDList(th, nil))
	assert.False(t, strings.Contains(maintTailIDList(th, []int64{7}), ","))
}
