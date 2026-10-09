package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// lesLedger is a ledger with every state, advisories, and evidence
// with one missing member: finding 102 aged out of the index.
func lesLedger() *proposals.Ledger {
	created := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Unix()

	return &proposals.Ledger{
		Pending: []domain.Proposal{
			{
				ID: 1, Rule: "check-nil-errors", Regions: []string{"internal/api"},
				Note:    "Check every error returned by the store before you use the value.",
				Members: []int64{101, 102}, Agent: "claude/v2", Status: domain.ProposalProposed, CreatedAt: created,
			},
			{
				ID: 2, Rule: "close-db-handles", Regions: []string{"internal/apix"},
				Note: "Close database handles in the same function.", Members: []int64{103},
				Agent: "claude/v2", Status: domain.ProposalProposed, CreatedAt: created,
			},
			{
				ID: 3, Rule: "keep-changelog-current", Note: "Add a changelog line to every user-visible change.",
				Agent: "claude/v2", Status: domain.ProposalProposed, CreatedAt: created,
			},
		},
		Applied: []domain.Proposal{
			{
				ID: 4, Rule: "wrap-sql-errors", Region: "internal/store", Note: "Wrap SQL errors with the query name.",
				Members: []int64{104}, Agent: "claude/v2", Status: domain.ProposalApplied, CreatedAt: created,
			},
		},
		Dismissed: []domain.Proposal{
			{ID: 5, Rule: "prefer-tabs", Region: "docs", Note: "Use tabs.", Status: domain.ProposalDismissed},
		},
		Health: map[int64]report.ProposalHealth{
			1: {Tier: "strong", Facts: "2 cited, 1 living", Scope: "note names internal/api/db.go outside the regions"},
			4: {Tier: "fair", Facts: "1 cited", Outcome: "working: 3 firings, no recurrence", Escalate: true},
		},
		Evidence: map[int64]domain.Finding{
			101: {
				ID: 101, Path: "internal/api/handler.go", PR: 45, Reviewer: "coderabbit", Source: "review",
				URL: "https://example.com/pr/45#c101", Body: "The error from Get is ignored here.\nCheck it first.",
			},
			103: {ID: 103, Path: "internal/apix/db.go", PR: 46, Reviewer: "person", Source: "review"},
			104: {ID: 104, Path: "internal/store/store.go", Source: "fix:conventional"},
		},
		Write: true,
	}
}

// lesPreview builds the preview that the shared service returns for
// pending IDs of lesLedger.
func lesPreview(a proposals.Action, ids []int64, write bool) *proposals.Preview {
	pv := &proposals.Preview{Action: a, Write: write, Target: proposals.LessonsFile}

	for _, id := range ids {
		for _, p := range lesLedger().Pending {
			if p.ID == id {
				pv.Proposals = append(pv.Proposals, p)
				pv.Prints = append(pv.Prints, proposals.Fingerprint{ID: p.ID, Rule: p.Rule, Note: p.Note})
			}
		}
	}

	if a == proposals.ActionApply {
		pv.YAML = "  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)\n" +
			"  - rule: check-nil-errors\n    region: internal/api\n    note: Check every error.\n"
	}

	return pv
}

// lesOpen starts a console on the Lessons destination.
func lesOpen(t *testing.T, f *fakeServices, w, h int) *harness {
	t.Helper()

	hs := newHarness(t, f, w, h)
	hs.press("3")
	require.Equal(t, destLessons, hs.m.active)

	return hs
}

// lesFlat joins the words of a frame, so prose assertions do not depend
// on where the text wraps.
func lesFlat(frame string) string {
	return strings.Join(strings.Fields(frame), " ")
}

// lesProps returns the Proposals sub-view, for state assertions.
func lesProps(hs *harness) *propView {
	return hs.m.screens[destLessons].(*lessons).props
}

// lesBoxFlat flattens a frame without the box borders, so a phrase in
// a wrapped box line matches. Use it for a full-width box only: two
// boxes side by side would mix their lines.
func lesBoxFlat(frame string) string {
	return lesFlat(strings.ReplaceAll(frame, "│", " "))
}

// lesModalLines renders the open decision modal at the harness size and
// returns its content lines without the border. The shell draws the
// modal over the frame, so a frame line mixes the modal with the
// backdrop; the modal alone keeps its own lines.
func lesModalLines(t *testing.T, hs *harness) []string {
	t.Helper()

	block, ok := lesProps(hs).modal(hs.m.st.width, hs.m.st.height-1)
	require.True(t, ok, "no decision modal is open")

	lines := strings.Split(ansi.Strip(block), "\n")
	for i, l := range lines {
		l = strings.TrimSuffix(strings.TrimPrefix(l, "│ "), " │")
		lines[i] = strings.TrimRight(l, " ")
	}

	return lines
}

// lesModalText returns the modal content on one line, so a phrase
// matches across wrapped lines.
func lesModalText(t *testing.T, hs *harness) string {
	t.Helper()

	return lesFlat(strings.Join(lesModalLines(t, hs), "\n"))
}

func TestLessonsWideAndCompactLayouts(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "Proposals")
	assert.Contains(t, frame, "Stats")
	assert.Contains(t, frame, "pending 3")
	assert.Contains(t, frame, "applied 1")
	assert.Contains(t, frame, "dismissed 1")
	assert.Contains(t, frame, "[ ] p1")
	assert.Contains(t, frame, "check-nil-errors")
	assert.Contains(t, frame, "Proposal p1", "the wide layout shows the detail beside the list")
	assert.Contains(t, frame, "finding 102: unavailable — aged out of the index")
	assert.Contains(t, frame, "distill.write on")
	assert.Contains(t, frame, "Pending proposals")
	assert.Contains(t, frame, "EVIDENCE  2 cited findings")

	// The 1a widths at 120 columns: a list box of 62 cells and a detail
	// box of 57 cells.
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "Pending proposals") {
			assert.True(t, strings.HasPrefix(line, "╭─ Pending proposals"), line)
			assert.Equal(t, 62, ui.Width(line[:strings.Index(line, "╮")+len("╮")]), line)
		}
	}

	// Tab moves focus to the detail; movement keys then scroll it and
	// leave the list cursor on the same proposal.
	h.press("tab", "down", "down")
	assert.True(t, lesProps(h).focusDetail)
	assert.Equal(t, int64(1), lesProps(h).cursorID)
	assert.Equal(t, 2, lesProps(h).detail.Off)

	// The page keys scroll the detail from the list too, as the hint
	// under the detail box says.
	h.press("shift+tab", "pgdown")
	assert.False(t, lesProps(h).focusDetail)
	assert.Equal(t, int64(1), lesProps(h).cursorID)
	assert.Greater(t, lesProps(h).detail.Off, 2)
	h.press("tab")

	h.press("shift+tab", "down")
	assert.False(t, lesProps(h).focusDetail)
	assert.Equal(t, int64(2), lesProps(h).cursorID)

	// Compact: one pane at a time, and the resize keeps the cursor.
	h.resize(70, 24)

	frame = h.view()
	assertFrame(t, frame, 70, 24)
	assert.Contains(t, frame, "Pending proposals")
	assert.NotContains(t, frame, "Proposal p2")
	assert.Equal(t, int64(2), lesProps(h).cursorID)

	h.press("enter")

	frame = h.view()
	assertFrame(t, frame, 70, 24)
	assert.Contains(t, frame, "Proposal p2")
	assert.Contains(t, frame, "Close database handles in the same function.")

	h.press("esc")
	assert.Contains(t, h.view(), "Pending proposals")
	assert.Equal(t, int64(2), lesProps(h).cursorID)

	assert.Empty(t, f.called(), "browsing must not preview or mutate")
}

func TestLessonsDetailShowsEvidenceAdvisoriesAndReadOnlyState(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 60)

	frame := h.view()
	detail := lesFixDetail(h)
	assert.Contains(t, detail, "NOTE Check every error returned by the store before you use the value.")
	assert.Contains(t, detail, "Regions internal/api")
	assert.Contains(t, detail, "Agent claude/v2 · created 2026-09-20")
	assert.Contains(t, frame, "PR #45 · coderabbit · internal/api/handler.go")
	assert.Contains(t, frame, "The error from Get is ignored here.")
	assert.NotContains(t, frame, "Check it first.", "the summary keeps the first sentence only")
	assert.Contains(t, frame, "● pending   [ ] not selected   tier strong")
	assert.Contains(t, frame, "finding 102: unavailable — aged out of the index",
		"missing evidence is labelled, never silently omitted")
	assert.Contains(t, detail, "ADVISORIES Evidence strong · 2 cited, 1 living")
	assert.Contains(t, detail, "note names internal/api/db.go outside the regions")
	assert.Contains(t, detail, "distill.write is on")

	// Applied rows are read-only: no checkbox, and the outcome carries
	// its label.
	h.press("f")

	frame = h.view()
	detail = lesFixDetail(h)
	assert.Contains(t, frame, "p4")
	assert.NotContains(t, frame, "[ ] p4")
	assert.Contains(t, frame, "● applied")
	assert.Contains(t, detail, "(derived recurrence check, not proof of effectiveness)")
	assert.Contains(t, detail, "The pin fires and the mistake still recurs.")
	assert.Contains(t, detail, "Applied proposals are read-only here")

	// Dismissed rows get no advisories, which the pane says.
	h.press("f")

	frame = h.view()
	assert.Contains(t, frame, "prefer-tabs")
	assert.Contains(t, frame, "Advisories cover pending and applied proposals only.")
	assert.Contains(t, frame, "Dismissed proposals are read-only")
}

func TestLessonsDetailReportsSourceFailures(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Evidence = nil
	f.ledger.EvidenceErr = errors.New("findings table is locked")
	f.ledger.Health = nil
	f.ledger.HealthErr = errors.New("lessons.yaml: bad hook_delivery")
	f.ledger.Write = false
	f.ledger.WriteErr = errors.New("distill config: yaml: line 3")

	h := lesOpen(t, f, 120, 60)

	detail := lesFixDetail(h)
	assert.Contains(t, detail, "The cited evidence could not be read: findings table is locked")
	assert.Contains(t, detail, "Cited finding IDs: 101, 102")
	assert.Contains(t, detail, "Advisories are unavailable: lessons.yaml: bad hook_delivery")
	assert.Contains(t, detail, "The write gate could not be read: distill config: yaml: line 3")
	assert.Contains(t, h.view(), "distill.write ! unreadable")
}

func TestLessonsStateFilterClearsSelectionVisibly(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)

	h.press("space", "down", "space")
	assert.Equal(t, []int64{1, 2}, lesProps(h).selectedIDs())
	assert.Contains(t, h.view(), "2 selected")
	assert.Contains(t, h.view(), "[✓] p1")

	h.press("x")
	assert.Empty(t, lesProps(h).selectedIDs())

	h.press("space")
	require.Equal(t, []int64{2}, lesProps(h).selectedIDs())

	h.press("f")

	frame := h.view()
	assert.Empty(t, lesProps(h).selectedIDs())
	assert.Contains(t, frame, "Selection cleared: filter changed")
	assert.Contains(t, frame, "[applied 1]")
	assert.Contains(t, frame, "wrap-sql-errors")
	assert.NotContains(t, frame, "check-nil-errors")

	// A change without a selection says nothing about the selection. The
	// cycle passes the dismissed and the superseded history (Phase 3).
	h.press("f", "f", "f")
	assert.Contains(t, h.view(), "state [pending 3] applied 1  dismissed 1  superseded 0")
}

func TestLessonsSpaceOnReadOnlyRowDoesNothing(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	h.press("f", "space")

	assert.Empty(t, lesProps(h).selectedIDs())
	assert.NotContains(t, h.view(), "[✓]")

	h.press("a")
	h.press("d")
	h.press("c")
	assert.Nil(t, lesProps(h).dec, "read-only rows cannot enter a decision")
	assert.Empty(t, f.called())

	for _, b := range h.m.screens[destLessons].keys() {
		if b.Help().Key == "a" || b.Help().Key == "d" || b.Help().Key == "c" || b.Help().Key == "space" {
			assert.False(t, b.Enabled(), "%s must be disabled on read-only rows", b.Help().Key)
		}
	}
}

func TestLessonsRegionFilterUsesPathBoundaries(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)

	// The input captures every key, including the destination digits.
	h.press("/")
	assert.True(t, h.m.screens[destLessons].capturing())
	h.typeText("x3")
	assert.Equal(t, destLessons, h.m.active)
	h.press("backspace", "backspace")

	h.typeText("internal/api")
	h.press("enter")
	assert.False(t, h.m.screens[destLessons].capturing())

	frame := h.view()
	assert.Contains(t, frame, "region internal/api")
	assert.Contains(t, frame, "check-nil-errors")
	assert.Contains(t, frame, "keep-changelog-current", "repo-wide proposals stay visible")
	assert.NotContains(t, frame, "close-db-handles", "internal/apix is not inside internal/api")

	// A selection is cleared by a region change, visibly.
	h.press("space")
	require.Equal(t, []int64{1}, lesProps(h).selectedIDs())

	h.press("/")
	h.typeText("x")
	h.press("enter")
	assert.Equal(t, "internal/apix", lesProps(h).region)
	assert.Empty(t, lesProps(h).selectedIDs())
	assert.Contains(t, h.view(), "Selection cleared: filter changed")
	assert.Contains(t, h.view(), "close-db-handles")
	assert.NotContains(t, h.view(), "check-nil-errors")

	// Esc cancels an edit and keeps the filter.
	h.press("/")
	h.typeText("zzz")
	h.press("esc")
	assert.Equal(t, "internal/apix", lesProps(h).region)

	// A filter that hides every row explains how to clear it.
	h.press("f", "/")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("web")
	h.press("enter")

	frame = lesBoxFlat(h.view())
	assert.Contains(t, frame, "No applied proposals match region web")
	assert.Contains(t, frame, "Press / and submit an empty filter to show every region.")

	h.press("/")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.press("enter")
	assert.Equal(t, "", lesProps(h).region)
	assert.Contains(t, h.view(), "wrap-sql-errors")
	assert.Empty(t, f.called())
}

func TestLessonsRefreshKeepsEligibleSelectionAndReportsLosses(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	h.press("space", "down", "space", "down", "space", "up")
	require.Equal(t, []int64{1, 2, 3}, lesProps(h).selectedIDs())
	require.Equal(t, int64(2), lesProps(h).cursorID)

	// Another writer dismisses p2.
	next := lesLedger()
	next.Dismissed = append(next.Dismissed, next.Pending[1])
	next.Pending = []domain.Proposal{next.Pending[0], next.Pending[2]}
	f.ledger = next

	h.press("r")

	frame := h.view()
	assert.Equal(t, []int64{1, 3}, lesProps(h).selectedIDs())
	assert.Contains(t, frame, "p2 left the selection: no longer pending")
	assert.Contains(t, frame, "p2 left this list; the cursor moved to p3")
	assert.Equal(t, int64(3), lesProps(h).cursorID)

	// A refresh without a change keeps everything and says nothing new.
	h.press("r")
	assert.Equal(t, []int64{1, 3}, lesProps(h).selectedIDs())
	assert.Equal(t, int64(3), lesProps(h).cursorID)
	assert.Empty(t, f.called())
}

func TestLessonsRefreshDropsSelectionOutsideRegionFilter(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	h.press("/")
	h.typeText("internal/api")
	h.press("enter", "space")
	require.Equal(t, []int64{1}, lesProps(h).selectedIDs())

	// A trigger extraction retargets p1 outside the filter.
	next := lesLedger()
	next.Pending[0].Regions = []string{"web"}
	f.ledger = next

	h.press("r")
	assert.Empty(t, lesProps(h).selectedIDs())
	assert.Contains(t, h.view(), "p1 left the selection: outside the region filter")
}

func TestLessonsApplyWithWriteOffShowsYAMLOnly(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, false), nil
	}

	h := lesOpen(t, f, 120, 32)
	h.press("space", "a")

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "╭─ YAML p1 · check-nil-errors")
	assert.Contains(t, frame, "4 lines")

	modal := lesModalText(t, h)
	assert.Contains(t, modal, "distill.write is off in .seamark/config.yaml, so nothing is written.")
	assert.Contains(t, modal, "Copy this block under pin: in .seamark/lessons.yaml")
	assert.Contains(t, modal, "The proposals stay pending. Nothing was changed.")
	assert.Contains(t, modal, "c copy to clipboard esc close")
	assert.NotContains(t, modal, "WILL", "the gate is off, so nothing will change")
	assert.NotContains(t, modal, "y apply")

	// The block is numbered and loses its common indent, as in the 1a
	// design; c copies the exact block.
	lines := lesModalLines(t, h)
	assert.Contains(t, lines, " 2  - rule: check-nil-errors")
	assert.Contains(t, lines, " 4    note: Check every error.")
	assert.NotContains(t, frame, "Applied")
	assert.True(t, h.m.screens[destLessons].capturing())

	// No confirm action exists, and global keys do not leak through.
	h.press("y", "enter", "1")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called())

	h.press("esc")
	assert.Nil(t, lesProps(h).dec)
	assert.Equal(t, []int64{1}, lesProps(h).selectedIDs(), "esc keeps the selection")
	assert.False(t, h.m.screens[destLessons].capturing())

	h.resize(70, 24)
	h.press("a")
	assertFrame(t, h.view(), 70, 24)
}

func TestLessonsApplyConfirmCommitsTheReviewedPreviewOnce(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	var previewed, committed *proposals.Preview

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		previewed = lesPreview(a, ids, true)

		return previewed, nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		committed = pv

		return proposals.Result{Action: pv.Action, FileWritten: true, Moved: []int64{1}}, nil
	}

	h := lesOpen(t, f, 120, 32)

	// Nothing selected: the cursor row stands in, and the preview says so.
	h.press("a")

	frame := h.view()
	assert.Contains(t, frame, "╭─ Apply p1 · check-nil-errors")

	modal := lesModalText(t, h)
	assert.Contains(t, modal, "Nothing was selected, so this covers the proposal under the cursor: p1.")
	assert.Contains(t, modal, "WILL + add 1 pin under pin: in .seamark/lessons.yaml (the file is created)")
	assert.Contains(t, modal, "+ record p1 as applied in the ledger (pending › applied)")
	assert.Contains(t, modal, "WON'T · change other pins or other proposals · write anything before you press y")
	assert.Contains(t, modal, "y apply 1 pin c copy to clipboard esc cancel")

	h.press("y")
	require.NotNil(t, committed)
	assert.Same(t, previewed, committed, "the commit carries exactly the reviewed preview")
	assert.Equal(t, []string{"PreviewDecision:apply", "CommitDecision:apply"}, f.called())
	assert.False(t, h.m.st.busy(), "the result frees the mutation slot")

	frame = h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "╭─ Applied 1 pin")

	modal = lesModalText(t, h)
	assert.Contains(t, modal, "WHAT HAPPENED")
	assert.Contains(t, modal, "written: .seamark/lessons.yaml")
	assert.Contains(t, modal, "p1 › applied")
	assert.Contains(t, modal, "Nothing is retried automatically.")

	// A second y on the result does not commit again.
	h.press("y", "enter")
	assert.Nil(t, lesProps(h).dec)
	assert.Equal(t, []string{"PreviewDecision:apply", "CommitDecision:apply"}, f.called())
}

func TestLessonsPartialApplyGuidesLedgerOnlyReconciliation(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		if pv.Action != proposals.ActionApply {
			return proposals.Result{Action: pv.Action, Moved: []int64{3}}, nil
		}

		return proposals.Result{
			Action: pv.Action, FileWritten: true, Moved: []int64{1}, NotMoved: []int64{3},
			LedgerErr: errors.New("database is locked"),
		}, nil
	}

	h := lesOpen(t, f, 120, 32)
	h.press("space", "down", "down", "space", "a", "y")

	assertFrame(t, h.view(), 120, 32)
	assert.Contains(t, h.view(), "Pins written, ledger incomplete")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "Pins were written to .seamark/lessons.yaml but the ledger was not fully updated")
	assert.Contains(t, frame, "(p3 still pending)")
	assert.Contains(t, frame, "Do not apply again")
	assert.Contains(t, frame, "press c to record them as applied (ledger only)")
	assert.Contains(t, frame, "database is locked")
	assert.NotContains(t, frame, "Applied 2")

	h.press("enter")
	assert.Equal(t, []int64{3}, lesProps(h).selectedIDs(), "not moved proposals stay selected")

	h.press("c")
	assert.Contains(t, h.view(), "Record p3 · keep-changelog-current as applied")
	assert.Contains(t, h.view(), "ledger only")
	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:apply", "CommitDecision:apply",
		"PreviewDecision:reconcile", "CommitDecision:reconcile"}, f.called())
	assert.Contains(t, h.view(), "Recorded as applied 1 proposal")
}

func TestLessonsStaleCommitAsksForANewPreview(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{Action: pv.Action}, fmt.Errorf("%w: p1 changed", proposals.ErrStale)
	}

	h := lesOpen(t, f, 120, 32)
	h.press("a", "y")

	frame := h.view()
	assert.Contains(t, frame, "Nothing changed")
	assert.Contains(t, lesModalText(t, h), "Something changed since the preview — preview again.")
	assert.Contains(t, lesModalText(t, h), "p1 changed")
	assert.Contains(t, lesModalText(t, h), "enter preview again esc close")
	assert.NotContains(t, frame, "Applied")
	assert.False(t, h.m.st.busy())

	h.press("enter")
	assert.Equal(t, []string{"PreviewDecision:apply", "CommitDecision:apply", "PreviewDecision:apply"}, f.called(),
		"preview again builds a new preview and never repeats the commit")
	assert.Contains(t, h.view(), "Apply p1")
}

func TestLessonsCommitRefusalsHaveClearMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		// The sentences name the enter action by its label, "preview
		// again", as the Pins results do.
		{proposals.ErrWriteDisabled, "distill.write is off, so nothing was written. Preview again to get the YAML block."},
		{fmt.Errorf("%w: p1", proposals.ErrNeedsReconcile), "a blind apply would duplicate them. Preview again and press c"},
		// A key that another writer applied after the preview is stale:
		// the new preview names the refusal and its hint.
		{
			fmt.Errorf("%w: %w", proposals.ErrStale, &proposals.AppliedKeyError{
				Groups: []proposals.AppliedKeyGroup{{Applied: []int64{4}, Pending: []int64{1}}},
			}),
			"Details: the ledger, the pin file, or the write gate changed since the preview: p4 is already " +
				"applied with this rule and regions; dismiss p1",
		},
		{proposals.ErrNothingSelected, "No pending proposal was selected."},
		{services.ErrBusy, "Another operation is running"},
	} {
		f := newFake()
		f.ledger = lesLedger()
		f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
			return lesPreview(a, ids, true), nil
		}
		f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
			return proposals.Result{Action: pv.Action}, tc.err
		}

		h := lesOpen(t, f, 120, 32)
		h.press("a", "y")

		assert.Contains(t, h.view(), "Nothing changed", tc.err.Error())
		assert.Contains(t, lesModalText(t, h), tc.want, tc.err.Error())
	}
}

// TestPropDecisionKeepsTheReadFailureAndTheRefusalApart checks that only
// a commit refusal at Result offers a new preview. The err of the flow is
// the read failure of the Preview stage, and it is never a refusal.
func TestPropDecisionKeepsTheReadFailureAndTheRefusalApart(t *testing.T) {
	stale := fmt.Errorf("%w: p1 changed", proposals.ErrStale)

	failed := &propDecision{previewFlow: previewFlow[proposals.Preview]{stage: flowPreview, err: stale}}
	assert.False(t, failed.reviewAgain(), "a read failure is no refusal")

	refused := &propDecision{previewFlow: previewFlow[proposals.Preview]{stage: flowResult}, refusal: stale}
	assert.True(t, refused.reviewAgain())

	empty := &propDecision{
		previewFlow: previewFlow[proposals.Preview]{stage: flowResult},
		refusal:     proposals.ErrNothingSelected,
	}
	assert.False(t, empty.reviewAgain(), "a new preview cannot select proposals")

	applied := &propDecision{previewFlow: previewFlow[proposals.Preview]{stage: flowResult}}
	assert.False(t, applied.reviewAgain(), "a commit without a refusal has nothing to review")
}

func TestLessonsAlreadyPinnedOffersLedgerOnlyRecord(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, true)
		if a == proposals.ActionApply {
			pv.AlreadyPinned = []int64{1}
		}

		return pv, nil
	}

	h := lesOpen(t, f, 120, 32)
	h.press("a")

	assert.Contains(t, h.view(), "Pins already exist")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "a blind apply would duplicate them")
	assert.Contains(t, frame, "c record as applied (ledger only)")
	assert.NotContains(t, frame, "y apply")
	assert.NotContains(t, frame, "copy to clipboard", "a copy would paste the existing pins again")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "y cannot apply over existing pins")

	h.press("c")
	assert.Contains(t, lesModalText(t, h), "The pins of these proposals already exist")
	assert.Contains(t, lesModalText(t, h), "WON'T · change .seamark/lessons.yaml: the pins already exist")
	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:apply", "PreviewDecision:reconcile", "CommitDecision:reconcile"}, f.called())
}

func TestLessonsDismissShowsTheDurableEffectBeforeConfirmation(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{Action: pv.Action, Moved: []int64{1, 2}}, nil
	}

	h := lesOpen(t, f, 70, 24)
	h.press("space", "down", "space", "d")

	assertFrame(t, h.view(), 70, 24)
	assert.Contains(t, h.view(), "Dismiss p1, p2 · 2 proposals")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "remembered: the same evidence will not come back")
	assert.Contains(t, frame, "WILL − move p1, p2 to dismissed in the ledger (pending › dismissed)")
	assert.Contains(t, frame, "WON'T · change .seamark/lessons.yaml: pins stay unchanged")
	assert.Contains(t, frame, "y dismiss 2 proposals esc cancel")
	assert.Contains(t, frame, "Pins stay unchanged.")
	assert.Contains(t, frame, "no undo")
	assert.Contains(t, frame, "p1")
	assert.Contains(t, frame, "p2")

	h.press("enter")
	assert.Equal(t, []string{"PreviewDecision:dismiss"}, f.called(), "enter does not confirm a durable decision")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:dismiss", "CommitDecision:dismiss"}, f.called())

	frame = h.view()
	assertFrame(t, frame, 70, 24)
	assert.Contains(t, frame, "Dismissed 2 proposals")
	assert.Contains(t, lesModalText(t, h), "Pin file unchanged")
	assert.Empty(t, lesProps(h).selectedIDs(), "decided proposals leave the selection")
}

func TestLessonsBusyGuardRejectsASecondMutation(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}

	h := lesOpen(t, f, 120, 32)
	require.True(t, h.m.st.begin("Building the index"))

	h.press("a", "y")
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called())
	require.NotNil(t, lesProps(h).dec)
	assert.Equal(t, flowPreview, lesProps(h).dec.stage, "the preview stays open for a later confirmation")
}

func TestLessonsPreviewFailureAndCancelledPreview(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(proposals.Action, []int64) (*proposals.Preview, error) {
		return nil, errors.New("distill config: yaml: line 2: bad indentation")
	}

	h := lesOpen(t, f, 120, 32)
	h.press("a")

	assert.Contains(t, h.view(), "Preview failed")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "distill config: yaml: line 2: bad indentation")
	assert.Contains(t, frame, "Nothing was changed.")

	h.press("esc")
	assert.Nil(t, lesProps(h).dec)

	// A preview result that arrives after a cancel is dropped.
	p := lesProps(h)
	p.preview(proposals.ActionApply, []int64{1}, true)
	stale := p.seq.last
	h.press("esc")
	h.send(propPreviewMsg{seq: stale, pv: lesPreview(proposals.ActionApply, []int64{1}, true)})
	assert.Nil(t, lesProps(h).dec)
}

func TestLessonsEmptyAndUnavailableStates(t *testing.T) {
	// No readable index: explain where proposals live.
	f := newFake()
	f.ledgerErr = fmt.Errorf("%w (absent)", services.ErrNoLedger)

	h := lesOpen(t, f, 120, 32)

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "No index yet")
	assert.Contains(t, lesBoxFlat(frame), "Proposals live in the index database")
	assert.Contains(t, frame, "seamark index")
	assert.NotContains(t, frame, "pending 0", "an unknown count never renders as zero")

	// Zero proposals: explain the CLI pipeline without starting it.
	f = newFake()
	f.ledger = &proposals.Ledger{}
	f.index.Status = &status.Status{DistillAgent: "claude -p"}

	h = lesOpen(t, f, 120, 32)

	frame = lesBoxFlat(h.view())
	assert.Contains(t, frame, "No proposals yet")
	assert.Contains(t, frame, "seamark index --reviews")
	assert.Contains(t, frame, "seamark lessons --distill")
	assert.Contains(t, frame, "through gh")
	assert.Contains(t, frame, "claude -p")
	assert.Contains(t, frame, "Nothing starts from this message")
	assert.Empty(t, f.called())

	// An empty state filter points at the others.
	f = newFake()
	f.ledger = lesLedger()
	f.ledger.Dismissed = nil

	h = lesOpen(t, f, 120, 32)
	h.press("f", "f")
	assert.Contains(t, h.view(), "No dismissed proposals")
	assert.Contains(t, lesBoxFlat(h.view()), "Press f to show another state.")

	// A failed reload keeps the last snapshot and marks it stale. Two
	// presses pass the superseded history back to pending (Phase 3).
	h.press("f", "f")

	f.ledgerErr = errors.New("database is locked")
	h.press("r")

	frame = h.view()
	assert.Contains(t, frame, "stale: database is locked")
	assert.Contains(t, frame, "check-nil-errors", "a stale snapshot stays visible")

	// Before the first load the view says what it is reading.
	v := newPropView(&state{svc: newFake(), theme: ui.NewTheme(false)})
	assert.Contains(t, v.view(100, 20), "Reading the proposal ledger")
}

func TestLessonsSubViewsAndIntents(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	les := h.m.screens[destLessons].(*lessons)

	assert.Contains(t, h.view(), "Proposals   Pins   Stats   Evidence   p / m / s / e")

	h.press("s")
	assert.Equal(t, lesStats, les.sub)

	h.press("p")
	assert.Equal(t, lesProposals, les.sub)

	h.send(navigateMsg{dest: destLessons, intent: "stats"})
	assert.Equal(t, lesStats, les.sub)

	// The proposals intent opens the pending list, as Overview promises.
	h.press("p", "f")
	require.Equal(t, propApplied, les.props.filter)
	h.press("s")
	h.send(navigateMsg{dest: destLessons, intent: "proposals"})
	assert.Equal(t, lesProposals, les.sub)
	assert.Equal(t, propPending, les.props.filter)

	// An overlay captures p and s.
	h.press("/")
	h.typeText("sp")
	assert.Equal(t, lesProposals, les.sub)
	assert.Equal(t, "sp", les.props.input.Value())
	assert.Empty(t, f.called())
}

func TestLessonsControlSequencesStayInert(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Pending[0].Rule = "\x1b[31mred-rule\x1b[0m"
	f.ledger.Pending[0].Note = "\x1b]0;evil title\x07note text\u009b31m"
	f.ledger.Pending[0].Agent = "claude/\x1b[2Jv2"
	f.ledger.Evidence[101] = domain.Finding{ID: 101, Path: "a\x1b[1m.go", Body: "body\x1b]8;;http://x\x07link", URL: "http://x/\x1b[5m"}
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, false)
		pv.Proposals[0].Rule = f.ledger.Pending[0].Rule
		pv.YAML = "  # distilled by \x1b[31mevil\x1b[0m\n  - rule: x\n"

		return pv, nil
	}

	h := lesOpen(t, f, 120, 60)

	for _, frame := range []string{h.m.View(), func() string { h.press("a"); return h.m.View() }()} {
		// The test color profile emits no escape sequence, so any ESC
		// byte would come from the data.
		assert.NotContains(t, frame, "\x1b")
		assert.NotContains(t, frame, "\x1b[31m")
		assert.NotContains(t, frame, "\x1b]")
		assert.NotContains(t, frame, "\x1b[2J")
		assert.NotContains(t, frame, "\x07")
		assert.NotContains(t, frame, "\u009b")
	}

	assert.Contains(t, h.view(), "YAML p1")
}

func TestLessonsASCIIThemeUsesNoUnicodeGlyphs(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}

	h := &harness{t: t, m: newModel(f, ui.NewTheme(true))}
	h.run(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 120, Height: 32})
	h.press("3")

	for _, frame := range []string{h.view(), func() string { h.press("a"); return h.view() }()} {
		for _, r := range frame {
			require.Less(t, r, rune(128), "non-ASCII rune %q in the ASCII theme:\n%s", r, frame)
		}
	}

	assert.Contains(t, h.view(), "pending > applied")
	assert.Contains(t, h.view(), "y apply 1 pin")
}

// The tests below hold review findings of Lessons: the cursor note of a
// refresh.

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
			h.send(distEventMsg{rec: rec, ch: agentClosed(), ev: distill.Event{Kind: distill.EventCleanupDone, Removed: 1}})
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
			rec.prev.preview = &learning.DistillPreview{Plan: &distill.Plan{
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
