package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/store"
)

// The review fixes of the Phase 3 shell slice. Each test drives the real
// model through the harness, so it proves the fix where the user sees
// it.

// Review fix: a setup and the first-run guide create the starter
// lessons.yaml, but Pins kept "No pin file yet" until the user pressed r.
func TestSetupAndFirstRunApplyReloadThePins(t *testing.T) {
	assert.Contains(t, frRefreshKinds, secPins)

	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	h.press("m")
	require.Contains(t, h.view(), "No pin file yet")

	// The setup writes the pin file, so the next read finds it.
	f.pins = pinsInspection()
	reads := f.readCount("Pins")

	h.send(setupAppliedMsg{})
	assert.Equal(t, reads+1, f.readCount("Pins"), "a setup apply reads the pins again")
	assert.NotContains(t, h.view(), "No pin file yet")
	assert.Contains(t, h.view(), "wrap-sql-errors")

	// The result of the guide reloads the pins without the guide too.
	h.send(frApplyMsg{res: frAppliedResult()})
	assert.Equal(t, reads+2, f.readCount("Pins"), "a first-run apply reads the pins again")

	// The guide itself reloads them with its result.
	g := frFake(nil)
	hg := newHarness(t, g, 120, 40)
	require.NotNil(t, hg.m.welcome)

	reads = g.readCount("Pins")
	hg.send(frApplyMsg{res: frAppliedResult()})
	assert.Equal(t, reads+1, g.readCount("Pins"), "the guide reads the pins again")
}

// Review fix: a ledger that holds only superseded rows is not empty, so
// Overview never says that it holds no proposal.
func TestOverviewNamesNoEmptyLedgerForSupersededHistory(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.learning.DistilledGroups = 2
	f.ledger = &proposals.Ledger{Superseded: []domain.Proposal{{ID: 1, Rule: "a"}, {ID: 2, Rule: "b"}}}

	h := newHarness(t, f, 120, 40)

	assert.False(t, ovlHas(ovScreen(h), "learn:distill"), "Proposals › f shows the superseded rows")
	assert.NotContains(t, lesFlat(h.view()), "holds no proposal")

	// The same evidence with a truly empty ledger still suggests the
	// work, so only the superseded rows changed the answer.
	f.ledger = &proposals.Ledger{}
	h.run(h.m.st.load(secLedger))
	assert.True(t, ovlHas(ovScreen(h), "learn:distill"))
}

// Review fix: t pushed "f state" and "/ region" out of the 1a footer
// at 120×40. t now follows them, and the help lists it.
func TestProposalsFooterKeepsTheFilterKeysBeforeExtract(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	h := lesOpen(t, f, 120, 40)
	require.True(t, maintHasKey(h, "t"), "the cursor row has an open question")

	assert.Contains(t, shellAFooter(h.view()), "space select   a apply   d dismiss   tab detail   f state   / region")

	var order []string

	for _, b := range hookLessons(h).keys() {
		if b.Enabled() {
			order = append(order, b.Help().Key)
		}
	}

	assert.Less(t, indexOf(order, "/"), indexOf(order, "t"), "t follows the filter keys: %v", order)

	h.press("?")
	assert.Contains(t, h.view(), "extract triggers", "the help still lists t")
}

// indexOf returns the position of k in keys, or -1.
func indexOf(keys []string, k string) int {
	for i, s := range keys {
		if s == k {
			return i
		}
	}

	return -1
}

// Review fix: the scope summary of a modal never scrolls, so a long one
// pushed the actions and the bottom border out of the area.
func TestModalTailKeepsTheActionsAndTheBorder(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		th := newTheme(ascii)

		tail := make([]string, 30)
		for i := range tail {
			tail[i] = "scope line " + itoa(i+1)
		}

		o := modalOpts{
			Title: "Prune", Body: []string{"line 1", "line 2"}, Tail: tail,
			Actions: []key.Binding{withHelp(nkeys.Back, "esc", "cancel")},
		}

		// The modal area of a 60×20 frame is 19 rows above the footer.
		lines := strings.Split(ansi.Strip(th.modal(o, &scrollState{}, 60, 19)), "\n")
		n := len(lines)

		assert.LessOrEqual(t, n, 19-modalMargin, "the modal keeps its margin")
		assert.True(t, strings.HasPrefix(lines[n-1], th.Glyphs.CornerBL), "the bottom border stays: %q", lines[n-1])
		assert.Contains(t, lines[n-2], "esc cancel", "the actions stay last")
		assert.Contains(t, lines[n-3], th.Glyphs.Ellipsis, "a cut summary says that it goes on")
		assert.Contains(t, lines[1], "line 1", "one body row stays")
		assert.Contains(t, strings.Join(lines, "\n"), "scope line 1 ")
	}
}

// The same overflow end to end: a prune of 151 ledger rows names every
// row in its scope summary at 60×20.
func TestMaintPreviewOfAHugePruneKeepsItsActions(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintPrune(t, false)

	for i := range 150 {
		p.Supersede = append(p.Supersede, store.NewRowGuard(domain.Proposal{ID: int64(1000 + i)}))
	}

	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 60, 20)
	require.True(t, maintHostOf(h).open())

	assertFrame(t, h.view(), 60, 20)

	block, ok := h.m.modalBlock(60, 19)
	require.True(t, ok)

	lines := strings.Split(ansi.Strip(block), "\n")
	n := len(lines)
	g := h.m.st.theme.Glyphs

	assert.LessOrEqual(t, n, 19-modalMargin, "the modal keeps its margin")
	assert.True(t, strings.HasPrefix(lines[n-1], g.CornerBL), "the bottom border stays: %q", lines[n-1])
	assert.Contains(t, lines[n-2], "esc cancel", "the actions stay in the modal")
	assert.Contains(t, lines[n-2], "y prune")
	assert.Empty(t, maintMutations(f))
}

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

// The shell guard: any dropped draft that leaves no record moves the
// view off the Run sub-view, so no visible sub-view lacks its tab.
func TestDroppedDraftNeverLeavesAnUnlistedRunView(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	d := newDistillDraft(h.m.st, "")
	h.send(openRunMsg{rec: d, show: true})
	require.Equal(t, lesRun, les.sub)

	// Only the close message, without the navigation that a closed
	// scope step sends with it.
	h.send(closeRunMsg{rec: d})
	assert.False(t, les.runShown())
	assert.Equal(t, lesProposals, les.sub)
}
