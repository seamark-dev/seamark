package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// This file holds the regression tests of the whole-phase review of
// Lessons: the switch keys in the help, the cursor note of a refresh,
// the sub-view line at the minimum width, and the gate-off apply text.

// lesHelpKeys returns the enabled key names that Lessons lists for the
// help and the footer.
func lesHelpKeys(les *lessons) []string {
	var out []string

	for _, b := range les.keys() {
		if b.Enabled() && b.Help().Key != "" {
			out = append(out, b.Help().Key)
		}
	}

	return out
}

func TestLessonsHelpListsEverySwitchKeyThatWorks(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	// Before the first run, u does nothing, so no list names it.
	assert.Subset(t, lesHelpKeys(les), []string{"s", "e"})
	assert.NotContains(t, lesHelpKeys(les), "u")

	h.send(openRunMsg{rec: runRec(h, "Evidence refresh")})
	require.True(t, les.runShown())

	names := map[string]string{"p": "p proposals", "s": "s stats", "e": "e evidence", "u": "u run"}

	for _, c := range []struct {
		key  string
		sub  lesSub
		want []string
	}{
		{"p", lesProposals, []string{"s", "e", "u"}},
		{"s", lesStats, []string{"p", "e", "u"}},
		{"e", lesEvidence, []string{"p", "s", "u"}},
		{"u", lesRun, []string{"p", "s", "e"}},
	} {
		h.press(c.key)
		require.Equal(t, c.sub, les.sub)

		keys := lesHelpKeys(les)
		assert.Subset(t, keys, c.want, "sub-view %q lists every switch key", c.key)
		assert.NotContains(t, keys, c.key, "sub-view %q does not name itself", c.key)

		// The help overlay reads the same list.
		h.press("?")

		help := lesFlat(h.view())
		for _, k := range c.want {
			assert.Contains(t, help, names[k], "the help of %q names %q", c.key, k)
		}

		h.press("esc")
	}

	// The switch keys go last, so the compact footer keeps its first hints.
	h.press("p")
	h.resize(60, 20)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(shellAFooter(h.view())), "space select"),
		shellAFooter(h.view()))
}

func TestLessonsSubTabsKeepTheGapBeforeTheGate(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false

	h := lesOpen(t, f, 60, 20)

	// At the minimum width the hint drops, so the faint hint and the
	// faint gate never read as one phrase.
	line := lesSubTabs(h)
	assert.True(t, strings.HasSuffix(line, "distill.write off"), line)
	assert.NotContains(t, line, "p / s / e", line)

	// A wider line keeps the hint with at least three cells before the gate.
	for _, w := range []int{61, 64, 80, 120} {
		h.resize(w, 24)

		line = lesSubTabs(h)
		if strings.Contains(line, "p / s / e") {
			assert.Regexp(t, regexp.MustCompile(`p / s / e {3,}distill\.write off$`), line, "width %d", w)
		}
	}

	assert.Contains(t, lesSubTabs(h), "p / s / e", "a wide line shows the hint")
}

// lesOneProposal is a ledger with one pending proposal, p1.
func lesOneProposal() *proposals.Ledger {
	l := lesLedger()
	l.Pending = l.Pending[:1]

	return l
}

// lesNoCursorNote reports whether the frame names a lost cursor row.
func lesNoCursorNote(h *harness) bool {
	return !strings.Contains(h.view(), "left this list")
}

func TestLessonsLastDismissedRowIsNotReportedAgain(t *testing.T) {
	f := newFake()
	f.ledger = lesOneProposal()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		after := lesOneProposal()
		after.Dismissed = append(after.Dismissed, after.Pending[0])
		after.Pending = nil
		f.ledger = after

		return proposals.Result{Action: pv.Action, Moved: []int64{1}}, nil
	}

	h := lesOpen(t, f, 120, 32)
	require.Equal(t, int64(1), lesProps(h).cursorID)

	// The user dismisses the last row; the commit refresh empties the list.
	h.press("d", "y", "enter")
	require.Empty(t, lesProps(h).rows())
	assert.True(t, lesNoCursorNote(h), "the user's own decision is no outside change")
	assert.Zero(t, lesProps(h).cursorID, "an empty list records no cursor row")

	// Every later refresh stays quiet, on Proposals and on Overview.
	for range 2 {
		h.m.toast = toastMsg{}
		h.press("r")
		assert.True(t, lesNoCursorNote(h), "a refresh reports a lost row once at most")
	}

	h.press("1")
	h.m.toast = toastMsg{}
	h.press("r")
	assert.True(t, lesNoCursorNote(h))
}

func TestLessonsEmptyFilterForgetsTheCursorRow(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Applied = nil

	h := lesOpen(t, f, 120, 32)
	h.press("f")
	require.Equal(t, propApplied, lesProps(h).filter)
	require.Empty(t, lesProps(h).rows())

	// p1 is still pending, only this filter hides it.
	for range 2 {
		h.m.toast = toastMsg{}
		h.press("r")
		assert.True(t, lesNoCursorNote(h), "a filter without rows loses no proposal")
	}
}

func TestLessonsCursorNoteWaitsForTheVisibleList(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	require.Equal(t, int64(1), lesProps(h).cursorID)

	// Another writer dismisses p1 while Overview shows.
	dismissP1 := func() {
		next := lesLedger()
		next.Dismissed = append(next.Dismissed, next.Pending[0])
		next.Pending = next.Pending[1:]
		f.ledger = next
	}

	dismissP1()
	h.press("1", "r")
	assert.True(t, lesNoCursorNote(h), "Overview shows no note about a hidden list")

	h.press("3")
	assert.Equal(t, int64(2), lesProps(h).cursorID, "the cursor still moved to the neighbor")

	// The same holds on another Lessons sub-view.
	f.ledger = lesLedger()
	h.press("r", "g")
	require.Equal(t, int64(1), lesProps(h).cursorID)

	dismissP1()
	h.m.toast = toastMsg{}
	h.press("s", "r")
	assert.True(t, lesNoCursorNote(h), "Stats shows no note about the Proposals cursor")

	// On Proposals the note shows, as before.
	f.ledger = lesLedger()
	h.press("p", "r", "g")
	require.Equal(t, int64(1), lesProps(h).cursorID)

	dismissP1()
	h.press("r")
	assert.Contains(t, h.view(), "p1 left this list; the cursor moved to p2")
}

func TestLessonsDistillCleanupIsNoOutsideChange(t *testing.T) {
	for _, c := range []struct {
		name string
		send func(h *harness, rec *distRecord)
	}{
		{"run cleanup", func(h *harness, rec *distRecord) {
			h.send(distEventMsg{rec: rec, ch: distClosed(), ev: distill.Event{Kind: distill.EventCleanupDone, Removed: 1}})
		}},
		{"cleanup-only plan", func(h *harness, rec *distRecord) {
			h.send(distCleanedMsg{rec: rec, removed: 1})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.ledger = lesLedger()

			h := lesOpen(t, f, 120, 32)
			h.press("space")
			require.Equal(t, int64(1), lesProps(h).cursorID)

			// The confirmed plan names p1 as a stale pending proposal.
			rec := newDistRecord(h.m.st)
			rec.plan.preview = &learning.DistillPreview{Plan: &distill.Plan{
				Cleanup: []domain.Proposal{{ID: 1, Rule: "check-nil-errors"}},
			}}

			c.send(h, rec)

			next := lesLedger()
			next.Pending = next.Pending[1:]
			f.ledger = next

			h.m.toast = toastMsg{}
			h.press("r")
			assert.True(t, lesNoCursorNote(h), "the plan disclosed the removal")
			assert.NotContains(t, h.view(), "left the selection")
			assert.Equal(t, int64(2), lesProps(h).cursorID)
		})
	}
}

func TestLessonsGateOffApplyNamesTheSettingsRoute(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, false), nil
	}

	h := lesOpen(t, f, 120, 40)
	h.press("a")

	text := lesModalText(t, h)
	assert.Contains(t, text, "turn on distill.write in Settings › Pin writing (5), then press a again for a new preview")
	assert.NotContains(t, text, "refresh with r", "every a reads the gate again")
}
