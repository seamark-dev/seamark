package tui

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests of Lessons › Pins. They drive the real console through the
// harness over pinsInspection and assert what the user sees, which keys
// the footer offers, and which service calls happen.

// pinsOpen starts a console on Lessons › Pins.
func pinsOpen(t *testing.T, f *fakeServices, w, h int) *harness {
	t.Helper()

	hs := lesOpen(t, f, w, h)
	hs.press("m")
	require.Equal(t, lesPins, hookLessons(hs).sub)

	return hs
}

// pinsOf returns the Pins sub-view of h.
func pinsOf(h *harness) *pinsView {
	return hookLessons(h).pins
}

// pinsKeys returns the enabled key names that Lessons offers on Pins.
func pinsKeys(h *harness) []string {
	return lesHelpKeys(hookLessons(h))
}

// pinsDetail returns the detail of the cursor row on one line, rendered
// wide enough that no phrase wraps.
func pinsDetail(t *testing.T, h *harness) string {
	t.Helper()

	p := pinsOf(h)
	cur, ok := p.cursorRow(p.rows())
	require.True(t, ok, "the list has a cursor row")

	var lines []string
	if cur.pin != nil {
		lines = p.pinDetail(p.inspection(), cur, 400)
	} else {
		lines = p.propDetail(cur.prop, 400)
	}

	return lesFlat(ansi.Strip(strings.Join(lines, "\n")))
}

// pinsCursorTo moves the cursor to the 1-based list position n.
func pinsCursorTo(h *harness, n int) {
	h.press("home")

	for range n - 1 {
		h.press("down")
	}
}

// pinsRef returns the binding of pin n (1-based) of an inspection.
func pinsRef(in *maintain.Inspection, n int) pinfile.EntryRef {
	return in.Pins[n-1].Entry.Ref()
}

func TestPinsListShowsEveryRelationshipState(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 80, 24)
	frame := h.view()

	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "show [installed 11] not present 1  superseded 1")

	// File order and the number of the entry in the file.
	assert.Less(t, strings.Index(frame, "wrap-sql-errors"), strings.Index(frame, "check-nil-errors"))

	for _, label := range []string{
		"✓ p4 linked", "! p9 edited", "✓ p12 linked", "! no proposal, unknown key", "! pending p3",
		"! duplicate pin", "! ambiguous ", "! p30 blocked", "! ambiguous row",
	} {
		assert.Contains(t, frame, label, "the list labels the state %q", label)
	}

	assert.Contains(t, frame, "…/store", "a long region keeps its last component")
	assert.Contains(t, frame, "repo-wide")
	assert.Contains(t, frame, "2 regions")
	assert.Empty(t, f.called(), "listing pins plans nothing")
}

func TestPinsDetailOfALinkedPinShowsItsReadings(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)
	text := pinsDetail(t, h)

	for _, want := range []string{
		"wrap-sql-errors pin 1 of 11", "✓ p4 linked", "[ ] not selected", "tier strong",
		"NOTE live, from the file Wrap SQL errors with the query name.",
		"Regions internal/store", "Source .seamark/lessons.yaml:3",
		"Linked to one applied proposal by rule and regions.", "p4 ● applied claude/v2 · created 2026-09-20",
		"Evidence strong · 2 event(s), review, newest 14d, 1/2 citations living",
		"Citations 1 living of 2 cited PR #45 · coderabbit · internal/store/sql.go",
		"READINGS derived advice; it chooses no action",
		"Scope ✓ current · regions already current",
		"Triggers answered · already answered with trigger paths internal/store/sql.go internal/store/legacy.go " +
			"! trigger internal/store/legacy.go is confirmed by co-change (3 shared commits), but it sets no region: " +
			"the path is not in the working tree Outcome",
		"Outcome ✓ working — flagged 4× in ~12 region-commits before exposure; 0× in 9 since (fired 3×)",
		"(derived recurrence check; not proof of cause, and not a reason to prune)",
		"Exposure delivered 3× · first 2026-09-22 · matched 5×",
		"MAINTENANCE The - key previews a prune. The keys act on the selected pins, or on this pin when nothing is selected.",
		"Nothing changes before you confirm with y.",
		"No retarget: its regions are already current.",
		"No trigger extraction: its trigger paths are already stored.",
		"distill.write is on",
	} {
		assert.Contains(t, text, want)
	}

	// "selected" names the space marks in this view, so the readings
	// never use it for another meaning.
	assert.NotContains(t, text, "advice; nothing is selected")
	assert.NotContains(t, text, "w previews", "the regions are current, so no retarget is offered")

	// The wide layout shows the detail beside the list at every size.
	for _, size := range [][2]int{{120, 40}, {200, 50}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "╭─ Pin 1 ")
	}
}

func TestPinsDetailShowsTheRecordedNoteAndTheScopeChange(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)
	pinsCursorTo(h, 2)

	text := pinsDetail(t, h)

	for _, want := range []string{
		"! p9 edited",
		"The live note differs from the note that p9 recorded. Maintenance keeps the live note",
		"RECORDED NOTE p9 Check every error returned by the store before you use the value.",
		"NOTE live, from the file Check every error returned by the store before you use the value; wrap it",
		"Citations 2 living of 2 cited PR #52 · coderabbit · internal/api/handler.go " +
			"PR #53 · coderabbit · internal/store/db.go",
		"Scope ! would change + internal/store added internal/api kept",
		"! broader: reaches paths that the current regions do not",
		"Triggers open · never asked Outcome",
		"Outcome ? untested — never fired",
		"Exposure never delivered under its current rule, note and regions",
		"MAINTENANCE w previews a retarget to the computed regions. The - key previews a prune. " +
			"t prepares a trigger extraction, which writes no pin.",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "No retarget")
	assert.NotContains(t, text, "No trigger extraction")
}

func TestPinsDetailNamesTheRouteOfEveryBlockedPin(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)

	cases := []struct {
		row   int
		want  []string
		extra []string // keys that stay enabled
	}{
		{5, []string{
			"Keys unknown key regoin (no reader uses it)", "That does not prove a person wrote the pin.",
			"Read-only here: the console changes only pins with one linked proposal.",
		}, nil},
		{6, []string{"p3 ● pending", "Record p3 as applied first: press p, select it, then press c."}, nil},
		{7, []string{
			"more than one entry of the pin file", "also pin 8 · .seamark/lessons.yaml:33",
			"remove or change the duplicate entries",
		}, nil},
		{8, []string{"also pin 7 · .seamark/lessons.yaml:29"}, nil},
		{9, []string{"p20 ● applied", "p21 ● applied", "p20, p21 all record this pin"}, nil},
		{10, []string{
			"Retarget and prune are off: this entry cannot be edited safely (a multi-line rule value).",
			"Scope ? cannot recompute", "Citations 0 living of 2 cited",
			"No trigger extraction: no cited finding is living.",
		}, nil},
		{11, []string{"the stored region fields of the matching proposal disagree", "Blocked: ambiguous: stored"}, nil},
	}

	for _, c := range cases {
		pinsCursorTo(h, c.row)

		text := pinsDetail(t, h)
		for _, want := range c.want {
			assert.Contains(t, text, want, "pin %d", c.row)
		}

		keys := pinsKeys(h)
		assert.NotContains(t, keys, "w", "pin %d offers no retarget", c.row)
		assert.NotContains(t, keys, "-", "pin %d offers no prune", c.row)

		if len(c.extra) == 0 {
			assert.NotContains(t, keys, "t", "pin %d offers no extraction", c.row)
		}

		for _, k := range c.extra {
			assert.Contains(t, keys, k, "pin %d", c.row)
		}
	}
}

func TestPinsActionKeysOpenTheReviewedRequests(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)
	in := f.pins

	// Pin 2 has a changed scope and an open trigger question.
	pinsCursorTo(h, 2)
	assert.Subset(t, pinsKeys(h), []string{"space", "w", "-", "t"}, "an eligible cursor row offers every action")

	// The cursor row stands in for an empty selection, and says so.
	msg, ok := p.requestMaint(maintain.ActionRetarget)().(openMaintMsg)
	require.True(t, ok)
	assert.Equal(t, maintain.Request{Action: maintain.ActionRetarget, Refs: []pinfile.EntryRef{pinsRef(in, 2)},
		Cursor: true}, msg.req)

	// Marked rows replace the cursor row, in file order. A marked row
	// that cannot be edited stays in the request, so the preview can
	// name why it is blocked.
	pinsCursorTo(h, 5)
	h.press("space")
	pinsCursorTo(h, 3)
	h.press("space")
	pinsCursorTo(h, 1)

	msg, ok = p.requestMaint(maintain.ActionPrune)().(openMaintMsg)
	require.True(t, ok)
	assert.Equal(t, maintain.Request{Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{pinsRef(in, 3), pinsRef(in, 5)}},
		msg.req)

	// Extraction asks only about linked pins; every other target is
	// listed with its reason. Pin 3 has no living evidence, so pin 2
	// makes the selection eligible; the review lists pin 3 itself.
	pinsCursorTo(h, 6)
	h.press("space")
	pinsCursorTo(h, 2)
	h.press("space")

	ext, ok := p.extract()().(openExtractMsg)
	require.True(t, ok)
	assert.Equal(t, []int64{9, 12}, ext.req.IDs)
	require.Len(t, ext.req.Skipped, 2)
	assert.Equal(t, int64(0), ext.req.Skipped[0].ID)
	assert.Equal(t, `pin "keep-changelog-current": no linked proposal`, ext.req.Skipped[0].Reason)
	assert.Equal(t, int64(3), ext.req.Skipped[1].ID, "a pending match names its one proposal")

	assert.Empty(t, f.called(), "building a request plans nothing")

	// The keys send the same requests through the shell.
	h.press("w")
	assert.True(t, p.host.open(), "w opens the maintenance preview over Pins")
	assert.Empty(t, hookMutations(f), "a preview commits nothing")
}

func TestPinsTOpensTheExtractionReview(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 2)
	h.press("t")

	les := hookLessons(h)
	require.Equal(t, lesRun, les.sub, "t opens the extraction review in Run")

	_, ok := les.runv.cur.(*extRecord)
	assert.True(t, ok)
	assert.Empty(t, hookMutations(f), "the review runs no agent")
}

func TestPinsHidesEveryMutationKeyWithoutAnEligibleTarget(t *testing.T) {
	type check struct {
		name string
		in   func() *maintain.Inspection
		keys []string
		row  int
		want []string // enabled keys among space, w, -, t
	}

	readOnly := func() *maintain.Inspection {
		in := pinsInspection()
		in.Doc.FileBlock = "the file is read-only"

		return in
	}

	foreign := func() *maintain.Inspection {
		in := pinsInspection()
		in.Index.Root = "/work/original"

		return in
	}

	for _, c := range []check{
		// The readings of a linked pin decide w and t: a current scope
		// gives no retarget, an answered question no extraction (R-5).
		{"linked, current and answered", pinsInspection, nil, 1, []string{"space", "-"}},
		{"linked, changed scope and open question", pinsInspection, nil, 2, []string{"space", "w", "-", "t"}},
		{"linked, no living evidence", pinsInspection, nil, 3, []string{"space", "-"}},
		{"linked, unknown scope", pinsInspection, nil, 4, []string{"space", "-", "t"}},
		{"no proposal", pinsInspection, nil, 5, []string{"space"}},
		{"blocked entry", pinsInspection, nil, 10, []string{"space"}},
		{"read-only file", readOnly, nil, 2, []string{"space", "t"}},
		// The pin file planner refuses a target that another pin or an
		// applied proposal already has (pinfile ErrCollision).
		{"computed regions of another pin", pinsLiveCollision, nil, 2, []string{"space", "-", "t"}},
		{"computed regions of an applied row", pinsAppliedCollision, nil, 2, []string{"space", "-", "t"}},
		// The index of a copied checkout names the original repository.
		{"foreign index root", foreign, nil, 2, []string{"space", "t"}},
		{"not present", pinsInspection, []string{"f"}, 1, nil},
		{"superseded", pinsInspection, []string{"f", "f"}, 1, nil},
		{"malformed file", pinsMalformed, nil, 1, nil},
		{"no ledger", pinsNoLedger, nil, 1, nil},
	} {
		f := pinsFake()
		f.pins = c.in()
		h := pinsOpen(t, f, 120, 40)
		h.press(c.keys...)
		pinsCursorTo(h, c.row)

		var got []string

		for _, k := range pinsKeys(h) {
			if k == "space" || k == "w" || k == "-" || k == "t" {
				got = append(got, k)
			}
		}

		assert.ElementsMatch(t, c.want, got, c.name)

		// A hidden key does nothing but explain itself.
		for _, k := range []string{"w", "-"} {
			if !slices.Contains(c.want, k) {
				h.press(k)
				assert.False(t, pinsOf(h).host.open(), "%s: %s opens no preview", c.name, k)
			}
		}

		if !slices.Contains(c.want, "t") {
			h.press("t")
			assert.Equal(t, lesPins, hookLessons(h).sub, "%s: no review opens", c.name)
		}

		assert.Empty(t, f.called(), c.name)
	}
}

func TestPinsHiddenKeysNameTheTargetAndTheReason(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)

	// Without marks the cursor pin is the target, so the note names it
	// after the reason.
	h.press("w")
	assert.Equal(t, `No retarget: its regions are already current ("wrap-sql-errors").`, h.m.toast.text)

	h.press("t")
	assert.Equal(t, `No trigger extraction: its trigger paths are already stored ("wrap-sql-errors").`,
		h.m.toast.text)

	pinsCursorTo(h, 4)
	h.press("w")
	assert.Equal(t, "No retarget: its scope is unknown: scope advice needs an indexed database (the index is "+
		"stale); build it with `seamark index` (\"close-database-handles\").", h.m.toast.text)

	// With marks the selection is the target, so the note says so and
	// names the selected pin whose reason it gives.
	pinsCursorTo(h, 3)
	h.press("space")
	pinsCursorTo(h, 1)
	h.press("w")
	assert.Equal(t, `No retarget: no cited finding is living, so no scope can be computed ("close-db-handles", `+
		"the selected pin).", h.m.toast.text)

	h.press("t")
	assert.Equal(t, `No trigger extraction: no cited finding is living ("close-db-handles", the selected pin).`,
		h.m.toast.text)

	// With two marks the note names the first one.
	pinsCursorTo(h, 4)
	h.press("space", "w")
	assert.Equal(t, `No retarget: no cited finding is living, so no scope can be computed ("close-db-handles", `+
		"the first of 2 selected pins).", h.m.toast.text)

	// A history row is no target at all.
	h.press("x", "f", "w")
	assert.Equal(t, "No retarget in a history list. Press f to show installed pins.", h.m.toast.text)

	assert.False(t, pinsOf(h).host.open())
	assert.Equal(t, lesPins, hookLessons(h).sub)
	assert.Empty(t, f.called(), "a hidden key plans nothing")
}

func TestPinsExtractionGateMatchesTheSharedTriggerRule(t *testing.T) {
	// The readings come from distill.TriggerQuestionOpen. Each trigger
	// state of the linked pin decides t as the extraction planner does.
	cases := []struct {
		name string
		tr   maintain.TriggerReading
		want bool
	}{
		{"never asked", maintain.TriggerReading{Known: true, Open: true, State: distill.TriggerOpen}, true},
		{"older negative", maintain.TriggerReading{Known: true, Open: true, State: distill.TriggerOpenObsoleteNegative},
			true},
		{"current negative", maintain.TriggerReading{Known: true, State: distill.TriggerAnsweredCurrentNegative}, false},
		{"stored paths", maintain.TriggerReading{Known: true, State: distill.TriggerAnsweredPositive}, false},
		{"unknown: the review decides", maintain.TriggerReading{Reason: "unknown: the cited findings could not be read"},
			true},
	}

	for _, c := range cases {
		f := pinsFake()
		f.pins.Pins[1].Readings.Trigger = c.tr
		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 2)

		assert.Equal(t, c.want, slices.Contains(pinsKeys(h), "t"), c.name)
	}
}

func TestPinsFilterCyclesAndClearsTheSelectionVisibly(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.press("space")
	require.Len(t, p.selected, 1)
	assert.Contains(t, h.view(), "1 selected")

	h.press("f")
	assert.Empty(t, p.selected)
	assert.Contains(t, h.view(), "Selection cleared: filter changed")
	assert.Contains(t, h.view(), "show installed 11 [not present 1] superseded 1")
	assert.Contains(t, pinsDetail(t, h), "The ledger records p7 as applied, but no entry of .seamark/lessons.yaml")

	// Bare absence proves nothing about who removed the pin, so the
	// detail names both manual outcomes and offers no repair.
	assert.Contains(t, pinsDetail(t, h), "To restore the pin, add the rule, the note and the regions above to "+
		".seamark/lessons.yaml by hand, then press r.")
	assert.Contains(t, pinsDetail(t, h), "If the pin was meant to go, the row stays here as a known mismatch; "+
		"the console offers no repair for it.")
	assert.Contains(t, h.view(), "! not present")

	h.press("space")
	assert.Empty(t, p.selected, "a history row cannot be marked")

	h.press("f")
	assert.Contains(t, h.view(), "superseded 1]")
	assert.Contains(t, pinsDetail(t, h), "Superseded: its pin was pruned. Read-only; not restorable here.")
	assert.Contains(t, h.view(), "○ superseded")

	h.press("f")
	assert.Equal(t, pinInstalled, p.filter, "f cycles back to the installed pins")
	assert.Empty(t, f.called())
}

func TestPinsRegionFilterAndTextSearch(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.press("space", "/")
	assert.True(t, hookLessons(h).capturing(), "the region input takes every key")
	h.typeText("internal/db")
	assert.Contains(t, h.view(), "region internal/db_", "the input shows a cursor glyph")
	h.press("enter")

	// Path boundaries in both directions: internal/db/sql is inside the
	// filter. A repository-wide pin reaches every path, so it stays, as
	// in the Proposals filter.
	var rules []string
	for _, r := range p.rows() {
		rules = append(rules, r.pin.Entry.Rule)
	}

	assert.Equal(t, []string{"close-db-handles", "close-database-handles", "keep-changelog-current"}, rules)
	assert.Empty(t, p.selected, "a filter change clears the selection")
	assert.Contains(t, h.view(), "Selection cleared: filter changed")

	h.press("z")
	h.typeText("EVERY database")
	h.press("enter")
	require.Len(t, p.rows(), 1, "the search ignores case and reads the live note")
	assert.Contains(t, h.view(), `text "EVERY database"`)

	// esc keeps the applied value.
	h.press("z")
	h.typeText("xyz")
	h.press("esc")
	assert.Equal(t, "EVERY database", p.text)

	// A search without matches says how to show every row.
	h.press("z", "ctrl+u")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("no such text")
	h.press("enter")
	assert.Contains(t, lesBoxFlat(h.view()), "No pins match region internal/db and text \"no such text\"")
	assert.Contains(t, lesBoxFlat(h.view()), "Press / or z and submit an empty value")

	// The history filters apply the same filters to the recorded fields.
	h.press("z")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.press("enter", "/")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("internal/old")
	h.press("enter", "f")
	require.Len(t, p.rows(), 1)
	assert.Equal(t, int64(7), p.rows()[0].prop.ID)
	assert.Empty(t, f.called())
}

func TestPinsRefreshKeepsTheSelectionByIdentity(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.press("space")
	pinsCursorTo(h, 3)
	h.press("space")
	require.Len(t, p.selected, 2)

	// Another writer inserts a pin at the top and edits the note of pin
	// 1. Pin 3 moves but keeps its content, so it stays marked and under
	// the cursor; pin 1 changed, so it leaves with a note.
	next := pinsInspection()
	top := pinsEntry(0, 1, "new-top-rule", "web", nil, "Inserted by hand.")
	edited := next.Pins[0]
	edited.Entry.Note = "Wrap SQL errors with the query name and the table."
	edited.Entry.Raw += "  # edited\n"

	pins := []maintain.Pin{{Entry: top, Link: pinfile.EntryLink{Link: pinfile.LinkNoProposal}}, edited}

	for _, pin := range next.Pins[1:] {
		pin.Entry.Index++
		pins = append(pins, pin)
	}

	next.Pins = pins
	f.pins = next

	h.press("r")

	require.Len(t, p.selected, 1)
	assert.True(t, p.selected.has(next.Pins[3].Entry.Ref()), "the moved pin stays marked under its new binding")

	cur, ok := p.cursorRow(p.rows())
	require.True(t, ok)
	assert.Equal(t, "close-db-handles", cur.pin.Entry.Rule, "the cursor follows the pin, not the position")
	assert.Contains(t, h.view(), `"wrap-sql-errors" left the selection: changed or no longer in the pin file`)
	assert.Empty(t, f.called())
}

func TestPinsRefreshDropsAMarkWhoseLinkChanged(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.press("space")
	require.True(t, p.selected.has(pinsRef(f.pins, 1)))

	// Another applied proposal now records the same pin: the content is
	// the same, but the pin is no longer linked to one proposal.
	next := pinsInspection()
	next.Pins[0].Link = pinfile.EntryLink{
		Link: pinfile.LinkAmbiguousLedger, Applied: append(next.Pins[0].Link.Applied, next.Pins[1].Link.Applied...),
	}
	f.pins = next

	h.press("r")
	assert.Empty(t, p.selected)
	assert.Contains(t, h.view(), `"wrap-sql-errors" left the selection: the link to the ledger changed`)
	assert.NotContains(t, pinsKeys(h), "w", "the cursor pin offers no retarget any more")
}

func TestPinsRefreshReportsALostCursorRow(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)

	next := pinsInspection()
	next.Pins = append(next.Pins[:1:1], next.Pins[2:]...)
	f.pins = next

	h.press("r")
	assert.Contains(t, h.view(), `"check-nil-errors" left this list; the cursor moved to "close-db-handles"`)
}

func TestPinsActedEntriesLeaveWithoutANote(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.press("space")
	ref := pinsRef(f.pins, 1)

	// A commit result reaches the host first; the same update hands the
	// changed entries to the sub-view.
	p.host.acted = []pinfile.EntryRef{ref}
	h.run(p.update(struct{}{}))
	assert.Empty(t, p.selected, "the changed entry leaves the selection")

	next := pinsInspection()
	next.Pins = next.Pins[1:]
	f.pins = next

	h.press("r")
	assert.NotContains(t, h.view(), "left this list", "the user caused the change")
	assert.NotContains(t, h.view(), "left the selection")
	assert.Empty(t, p.acted, "the next load ends the note")
}

func TestPinsUnreadableSourcesClearTheSelection(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)

	h.press("space")
	f.pins = pinsMalformed()
	h.press("r")

	assert.Empty(t, pinsOf(h).selected)
	assert.Contains(t, h.view(), "Selection cleared: the pin file or the ledger could not be read")
}

func TestPinsCompareIsReadOnlyAndShowsBothScopes(t *testing.T) {
	f := pinsFake()

	// A third similar pin, so tab has a next pin.
	f.pins.Pins[2].Readings.Similar = []int{3, 0}

	h := pinsOpen(t, f, 120, 40)

	h.press("v")
	assert.Nil(t, pinsOf(h).compare, "a pin without similar pins has no comparison")
	assert.NotContains(t, pinsKeys(h), "v")

	pinsCursorTo(h, 3)
	assert.Contains(t, pinsKeys(h), "v")
	h.press("v")
	require.NotNil(t, pinsOf(h).compare)
	assert.True(t, hookLessons(h).capturing(), "the comparison takes every key")

	block, ok := hookLessons(h).modal(120, 39)
	require.True(t, ok)

	text := lesFlat(strings.ReplaceAll(ansi.Strip(block), "│", " "))
	for _, want := range []string{
		"Compare pin 3 · pin 4", "1 of 2", "Similar wording does not mean identical coverage.",
		"This view is read-only and changes nothing.", "PIN 3 line 12 · p12", "PIN 4 line 16 · p14",
		"close-db-handles", "close-database-handles", "Close database handles in the same",
		"Only pin 3 reaches internal/db outside internal/db/sql", "Only pin 4 reaches internal/cache",
		"Both reach internal/db/sql", "tab next similar pin", "esc close",
	} {
		assert.Contains(t, text, want)
	}

	h.press("tab")
	block, _ = hookLessons(h).modal(120, 39)
	assert.Contains(t, lesFlat(ansi.Strip(block)), "Compare pin 3 · pin 1")

	// Switch keys and action keys wait while it is open.
	h.press("p", "w", "space")
	assert.Equal(t, lesPins, hookLessons(h).sub)
	assert.Empty(t, pinsOf(h).selected)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "esc close", "the close key stays visible at %v", size)
	}

	h.press("esc")
	assert.Nil(t, pinsOf(h).compare)
	assert.Empty(t, f.called(), "a comparison plans and writes nothing")
}

func TestPinsCompareClosesWhenAPinChanges(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 3)
	h.press("v")
	require.NotNil(t, pinsOf(h).compare)

	next := pinsInspection()
	next.Pins[3].Entry.Raw += "  # edited\n"
	f.pins = next

	h.send(refreshMsg{kinds: []sectionKind{secPins}})
	assert.Nil(t, pinsOf(h).compare)
	assert.Contains(t, h.view(), "The comparison closed: a compared pin changed")
}

func TestPinsLinkFocusesThePinWithoutSelecting(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	// A filter hides the pin: the link clears it and says so.
	h.press("/")
	h.typeText("docs")
	h.press("enter")

	h.run(openPins(14))

	cur, ok := p.cursorRow(p.rows())
	require.True(t, ok)
	assert.Equal(t, "close-database-handles", cur.pin.Entry.Rule)
	assert.Empty(t, p.region)
	assert.Empty(t, p.selected, "a link never selects")
	assert.False(t, p.host.open(), "a link never opens a preview")
	assert.Contains(t, h.view(), `Filters cleared to show "close-database-handles"`)

	// A missing pin shows under its filter.
	h.run(openPins(7))
	assert.Equal(t, pinNotPresent, p.filter)

	h.run(openPins(999))
	assert.Contains(t, h.view(), "No installed pin links p999 now.")
	assert.Empty(t, f.called())
}

func TestPinsLinkBeforeTheFirstReadWaits(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	p := pinsOf(h)

	h.m.st.pins.loaded = false
	h.send(focusPinMsg{id: 12})
	assert.Equal(t, int64(12), p.focusID)

	h.press("r")

	cur, ok := p.cursorRow(p.rows())
	require.True(t, ok)
	assert.Equal(t, "close-db-handles", cur.pin.Entry.Rule)
	assert.Zero(t, p.focusID)
}

func TestPinsEmptyAndErrorStates(t *testing.T) {
	syntax := fakePins()
	syntax.Doc.DecodeErr = errors.New("yaml: line 4: mapping values are not allowed in this context")
	syntax.Doc.ErrLine = 4
	syntax.LinkErr = pinfile.ErrUnreadable
	syntax.Links = &pinfile.Links{NotPresentErr: pinfile.ErrUnreadable}

	emptyFile := fakePins()
	emptyFile.Doc.Guard = integration.FileGuard{Exists: true}
	emptyFile.Doc.Bytes = []byte("pin:\n")

	notRegular := &maintain.Inspection{DocErr: errors.New("open .seamark/lessons.yaml: is a directory")}

	// The index is ready, but the ledger read failed: a new read may pass.
	ledgerErr := pinsNoLedger()
	ledgerErr.Index.State = workspace.IndexReady
	ledgerErr.LinkErr = errors.New("unknown: the proposal ledger could not be read: database is locked")
	ledgerErr.LedgerErr = ledgerErr.LinkErr

	cases := []struct {
		name string
		in   *maintain.Inspection
		keys []string
		want []string
	}{
		{"no file", fakePins(), nil, []string{
			"No pin file yet", ".seamark/lessons.yaml does not exist, so no pin is installed.",
			"p open Proposals e open Evidence",
		}},
		{"empty file", emptyFile, nil, []string{"No pins in .seamark/lessons.yaml", "p open Proposals"}},
		{"syntax error", syntax, nil, []string{
			"The pin file could not be read", "(line 4): yaml: line 4: mapping values are not allowed",
			"That is not an empty list: no pin is shown as removed.", "show [installed ?]",
		}},
		{"not a file", notRegular, nil, []string{
			"The pin file could not be read", "is a directory", "Press r", "show [installed ?]",
		}},
		{"malformed, listed read-only", pinsMalformed(), nil, []string{
			"! .seamark/lessons.yaml could not be read (line 9)", "? link unknown", "wrap-sql-errors",
			"not present ?",
		}},
		{"no ledger", pinsNoLedger(), nil, []string{
			"? Relationships are unknown: the proposal ledger could not be read: no readable index.",
			"? link unknown", "not present ?", "superseded ?",
		}},
		{"missing pins unknown", pinsMalformed(), []string{"f"}, []string{
			"Unknown which pins are missing", "Seamark never reads an error as a missing pin.",
			"Fix .seamark/lessons.yaml, then press r.", "f cycles back to the installed pins, which stay listed",
		}},
		{"missing pins unknown without an index", pinsNoLedger(), []string{"f"}, []string{
			"Unknown which pins are missing", "Press 1, then b to build the index, or run: seamark index",
			"f cycles back to the installed pins",
		}},
		{"missing pins unknown, ledger error", ledgerErr, []string{"f"}, []string{
			"Unknown which pins are missing", "database is locked", "Press r to read the ledger again.",
		}},
		{"history unknown", pinsNoLedger(), []string{"f", "f"}, []string{
			"The history is unknown", "Press 1, then b to build the index, or run: seamark index",
			"f cycles back to the installed pins",
		}},
		{"history unknown, ledger error", ledgerErr, []string{"f", "f"}, []string{
			"The history is unknown", "database is locked", "Press r to read the ledger again.",
		}},
		{"every pin present", fakePins(), []string{"f"}, []string{"Every applied proposal has its pin"}},
		{"no history", fakePins(), []string{"f", "f"}, []string{"No superseded proposals"}},
	}

	for _, c := range cases {
		f := pinsFake()
		f.pins = c.in

		h := pinsOpen(t, f, 120, 40)
		h.press(c.keys...)

		text := lesBoxFlat(h.view())
		for _, want := range c.want {
			assert.Contains(t, text, want, c.name)
		}

		for _, k := range []string{"space", "w", "-", "t"} {
			assert.NotContains(t, pinsKeys(h), k, "%s: no action key", c.name)
		}

		for _, size := range [][2]int{{80, 24}, {60, 20}} {
			h.resize(size[0], size[1])
			assertFrame(t, h.view(), size[0], size[1])
		}

		assert.Empty(t, f.called(), c.name)
	}
}

func TestPinsMalformedFileListsEntriesAndExplainsTheRoute(t *testing.T) {
	f := pinsFake()
	f.pins = pinsMalformed()
	h := pinsOpen(t, f, 120, 40)

	text := pinsDetail(t, h)
	assert.Contains(t, text, "Relationships are unknown: .seamark/lessons.yaml could not be read.")
	assert.Contains(t, text, "Rows with this rule and these regions; no link is proven: p4 ● applied")
	assert.Contains(t, text, "Read-only: .seamark/lessons.yaml could not be read (line 9)")
	assert.Contains(t, text, "Fix the file and press r.")
	assert.NotContains(t, text, "w previews", "an unreadable file offers no maintenance")
}

func TestPinsNoLedgerNamesTheBuildRoute(t *testing.T) {
	f := pinsFake()
	f.pins = pinsNoLedger()
	h := pinsOpen(t, f, 120, 40)

	text := pinsDetail(t, h)
	assert.Contains(t, text, "Build the index from Overview (press 1, then b), or run this command in a shell: seamark index")
	assert.Contains(t, text, "Exposure never delivered under its current rule, note and regions",
		"exposure needs no ledger")
}

func TestPinsCompactLayoutShowsOnePane(t *testing.T) {
	f := pinsFake()

	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h := pinsOpen(t, f, size[0], size[1])

		assert.NotContains(t, h.view(), "╭─ Pin 1 ")
		assert.Contains(t, pinsKeys(h), "enter")

		h.press("enter")
		assert.Contains(t, h.view(), "╭─ Pin 1 ")
		assert.NotContains(t, h.view(), "Installed pins")
		assert.Contains(t, footerLine(h.view()), "space select")

		h.press("pgdown", "pgdown")
		assertFrame(t, h.view(), size[0], size[1])

		h.press("esc")
		assert.Contains(t, h.view(), "Installed pins")
		assertFrame(t, h.view(), size[0], size[1])
	}

	assert.Empty(t, f.called())
}

func TestPinsBrowsingCallsNoService(t *testing.T) {
	f := pinsFake()
	c := &pinsLedgerCounter{fakeServices: f}

	h := newHarness(t, c, 120, 40)
	h.press("3", "m")
	require.Equal(t, lesPins, hookLessons(h).sub)

	// R-13: browsing starts no implicit refresh, so the read counts after
	// the first load stay the same.
	pins, ledger := f.readCount("Pins"), c.count()
	require.Positive(t, pins)
	require.Positive(t, ledger)

	h.press("down", "down", "tab", "pgdown", "pgup", "tab", "end", "home", "enter", "esc", "space", "x")
	h.press("v", "down", "down", "v", "tab", "esc", "f", "f", "f", "/", "esc", "z", "esc")

	for _, size := range [][2]int{{80, 24}, {60, 20}, {120, 40}} {
		h.resize(size[0], size[1])
		h.press("down", "enter", "esc")
	}

	assert.Empty(t, f.called(), "navigation, filters and comparisons plan, run and write nothing")
	assert.Equal(t, pins, f.readCount("Pins"), "browsing reads the pin file no more")
	assert.Equal(t, ledger, c.count(), "browsing reads the ledger no more")
}

// pinsLedgerCounter counts the ledger reads of a fake. The shared fake
// counts only some reads, and R-13 needs the ledger count as well.
type pinsLedgerCounter struct {
	*fakeServices

	mu    sync.Mutex
	reads int
}

// Ledger counts the read and returns the ledger of the fake.
func (c *pinsLedgerCounter) Ledger() (*proposals.Ledger, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()

	return c.fakeServices.Ledger()
}

// count returns how often the ledger was read.
func (c *pinsLedgerCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reads
}

func TestPinsHostileTextIsInertInEveryView(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		f := pinsFake()
		f.pins = pinsASCIIInspection()
		f.pins.Pins[3].Entry.Rule = "close-\x1b[2Jdatabase-handles"
		f.pins.Pins[3].Entry.Note = "note\x07with\u009b1mbell"
		f.pins.Pins[3].Entry.Regions = []string{"internal/\x1b]8;;evil\x07sql"}
		f.pins.Links.NotPresent[0].Rule = "retired-\x1b[31mguidance"

		h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
		h.run(h.m.Init())

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})

			scenes := [][]string{
				{"3", "m", "home"},
				{"end", "up"},
				{"enter"},
				{"esc", "home", "down", "down", "v"},
				{"esc", "f"},
			}

			for _, keys := range scenes {
				h.press(keys...)

				raw := h.m.View()
				assert.NotContains(t, raw, "\x1b]", "no OSC sequence at %v", size)
				assert.NotContains(t, raw, "\x07", "no bell at %v", size)
				assert.NotContains(t, raw, "\u009b", "no C1 CSI at %v", size)
				assert.NotContains(t, raw, "\x1b[2J", "no erase sequence at %v", size)
				assertFrame(t, h.view(), size[0], size[1])

				if ascii {
					for _, r := range h.view() {
						require.Less(t, r, rune(128), "ASCII frame at %v after %v", size, keys)
					}
				}
			}

			h.press("esc", "f", "f")
		}
	}
}

func TestPinsHelpListsTheKeys(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)

	// The help lists the enabled keys, and pin 2 offers every action.
	pinsCursorTo(h, 2)
	h.press("?")

	help := lesFlat(h.view())
	for _, want := range []string{"space select", "w retarget", "- prune", "t extract triggers", "f show",
		"/ region", "z search", "p proposals", "s stats", "e evidence"} {
		assert.Contains(t, help, want)
	}
}

func TestPinsDetailOfAProposalRowNamesTheAgent(t *testing.T) {
	f := pinsFake()
	f.pins.Links.NotPresent[0].TriggerPaths = []string{"internal/old/client.go"}
	f.pins.Links.NotPresent[0].Agent = ""
	f.pins.Links.NotPresent[0].CreatedAt = 0

	h := pinsOpen(t, f, 120, 40)
	h.press("f")

	text := pinsDetail(t, h)
	assert.Contains(t, text, "retired-guidance p7 ! not present ● applied in the ledger")
	assert.Contains(t, text, "NOTE recorded with the proposal Use the old client.")
	assert.Contains(t, text, "Triggers internal/old/client.go")
	assert.Contains(t, text, "Agent unknown")
}

func TestPinsUnreadableRefreshNeverReportsTheCursorPinAsGone(t *testing.T) {
	syntax := func() *maintain.Inspection {
		in := fakePins()
		in.Doc.DecodeErr = errors.New("yaml: line 4: mapping values are not allowed in this context")
		in.Doc.ErrLine = 4
		in.LinkErr = pinfile.ErrUnreadable
		in.Links = &pinfile.Links{NotPresentErr: pinfile.ErrUnreadable}

		return in
	}

	denied := func() *maintain.Inspection {
		return &maintain.Inspection{DocErr: errors.New("open .seamark/lessons.yaml: permission denied")}
	}

	cases := []struct {
		name   string
		in     func() *maintain.Inspection
		filter []string
		row    int
		box    string
	}{
		{"syntax error", syntax, nil, 2, "That is not an empty list: no pin is shown as removed."},
		{"permission denied", denied, nil, 2, "permission denied"},
		{"ledger error on not present", pinsNoLedger, []string{"f"}, 1, "Seamark never reads an error as a missing pin."},
	}

	for _, c := range cases {
		f := pinsFake()
		h := pinsOpen(t, f, 120, 40)
		h.press(c.filter...)
		pinsCursorTo(h, c.row)

		want := pinsOf(h).cursorID
		require.NotEqual(t, pinID{}, want, c.name)

		// A read error empties the list. The rows are unknown, not gone,
		// so no note calls the cursor row lost (D-5).
		f.pins = c.in()
		h.press("r")
		assert.NotContains(t, h.view(), "left this list", c.name)
		assert.NotContains(t, h.m.toast.text, "left this list", c.name)
		assert.Contains(t, lesBoxFlat(h.view()), c.box, c.name)

		// The next good read finds the same row again, without a note.
		f.pins = pinsInspection()
		h.press("r")
		assert.Equal(t, want, pinsOf(h).cursorID, c.name)
		assert.NotContains(t, h.m.toast.text, "left this list", c.name)
	}
}

func TestPinsPartialListingCallsAMissingCursorPinUnknown(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)

	// The reader rejects the file, and the decoded listing lacks pin 2.
	next := pinsMalformed()
	next.Pins = append(next.Pins[:1:1], next.Pins[2:]...)
	f.pins = next

	h.press("r")
	assert.Equal(t, `"check-nil-errors" is unknown: the pin file could not be read; the cursor moved to `+
		`"close-db-handles"`, h.m.toast.text)
}

func TestPinsUnreadableRefreshClosesTheComparisonWithTheReadError(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 3)
	h.press("v")
	require.NotNil(t, pinsOf(h).compare)

	f.pins = &maintain.Inspection{DocErr: errors.New("open .seamark/lessons.yaml: permission denied")}
	h.send(refreshMsg{kinds: []sectionKind{secPins}})

	assert.Nil(t, pinsOf(h).compare)
	assert.Equal(t, "The comparison closed: the pin file could not be read", h.m.toast.text)
}

func TestPinsCompareStaysOpenWhenThePinsOnlyMove(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 3)
	h.press("v", "tab")
	require.NotNil(t, pinsOf(h).compare)

	// Another writer inserts a pin at the top. Every entry moves down one
	// position and keeps its key and its own lines.
	next := pinsInspection()
	top := maintain.Pin{
		Entry: pinsEntry(0, 1, "new-top-rule", "web", nil, "Inserted by hand."),
		Link:  pinfile.EntryLink{Link: pinfile.LinkNoProposal},
	}
	pins := []maintain.Pin{top}

	for _, pin := range next.Pins {
		pin.Entry.Index++
		pins = append(pins, pin)
	}

	next.Pins = pins
	f.pins = next

	h.send(refreshMsg{kinds: []sectionKind{secPins}})

	require.NotNil(t, pinsOf(h).compare, "a move is not a change")
	assert.NotContains(t, h.m.toast.text, "The comparison closed")

	block, ok := hookLessons(h).modal(120, 39)
	require.True(t, ok)
	assert.Contains(t, lesFlat(ansi.Strip(block)), "Compare pin 4 · pin 5", "both pins show at their new positions")
	assert.NotContains(t, lesFlat(ansi.Strip(block)), "A compared pin changed")
	assert.Empty(t, f.called())
}

func TestPinsStateLabelFitsItsColumnWithoutLosingWords(t *testing.T) {
	theme := ui.NewTheme(false)
	ell := theme.Glyphs.Ellipsis
	s := pinState{glyph: "!", lead: "p9", sep: " ", flags: []string{"blocked", "unknown key", "edited"}}

	assert.Equal(t, "! p9 blocked, unknown key, edited", ansi.Strip(s.fit(40, ell)), "a label that fits stays whole")
	assert.Equal(t, "! p9 blocked +2", ansi.Strip(s.fit(17, ell)), "the most urgent word stays, the others count")
	assert.Equal(t, "! p9 +3", ansi.Strip(s.fit(12, ell)), "a narrow column counts every word")
	assert.Equal(t, "! p…", ansi.Strip(s.fit(4, ell)), "a column without room cuts the label")

	one := pinState{glyph: "!", lead: "no proposal", sep: ", ", flags: []string{"unknown key"}}
	assert.Equal(t, "! no proposal +1", ansi.Strip(one.fit(17, ell)))
}

func TestPinsListKeepsTheRelationshipAndBlockedVisible(t *testing.T) {
	f := pinsFake()

	// p9 gets every attention word: edited, an unknown key, and a block.
	pin := &f.pins.Pins[1]
	pin.Entry.Unknown = []string{"regoin"}
	pin.Entry.Block = "a multi-line rule value"

	for _, size := range [][2]int{{120, 40}, {160, 40}, {200, 50}, {80, 24}} {
		h := pinsOpen(t, f, size[0], size[1])
		frame := h.view()
		assertFrame(t, frame, size[0], size[1])

		var row string

		for line := range strings.SplitSeq(frame, "\n") {
			if strings.Contains(line, "check-nil-errors") {
				row = line
			}
		}

		assert.Contains(t, row, "! p9 blocked +2", "the relationship and the block show at %v", size)

		// A label with two words shows whole where the list has room.
		if size[0] != 120 && size[0] != 160 {
			assert.Contains(t, frame, "! no proposal, unknown key", "at %v", size)
		} else {
			assert.Contains(t, frame, "! no proposal +1", "at %v", size)
		}
	}

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)
	assert.Contains(t, pinsDetail(t, h), "check-nil-errors pin 2 of 11 ! p9 blocked, unknown key, edited",
		"the detail status line shows every word")
}

func TestPinsFixtureReadingsAreStatesTheEnginesGive(t *testing.T) {
	// The captures of the visual review show these readings: the Pins
	// fixture, and the maintenance fixture behind the previews.
	pinsAssertEngineStates(t, pinsInspection())
	pinsAssertEngineStates(t, maintFixture(t).inspection())
}

// pinsAssertEngineStates holds the rules that join the readings of one
// pin, so the review never sees a scope, a trigger or a citation count
// that the engines cannot give together.
func pinsAssertEngineStates(t *testing.T, in *maintain.Inspection) {
	t.Helper()

	for i, pin := range in.Pins {
		r := pin.Readings
		if !r.Confidence.Known {
			continue
		}

		row := pin.Link.Applied[0]
		living := len(r.Confidence.Living)

		// confidence.Assess rates a pin without a living finding weak.
		if living == 0 {
			assert.Equal(t, confidence.TierWeak, r.Confidence.Tier, "pin %d", i+1)
		}

		// distill.RecommendScope gives ScopeNoEvidence exactly when no
		// cited finding is living.
		if r.Scope.Known {
			assert.Equal(t, living == 0, r.Scope.Advice.Kind == distill.ScopeNoEvidence, "pin %d", i+1)
		}

		// The trigger reading is distill.TriggerQuestionOpen of the row.
		if r.Trigger.Known {
			open, state := distill.TriggerQuestionOpen(row)
			assert.Equal(t, open, r.Trigger.Open, "pin %d", i+1)
			assert.Equal(t, state, r.Trigger.State, "pin %d", i+1)
		}

		// A blocked trigger is a stored trigger path that the computed
		// scope does not reach.
		for _, b := range r.Trigger.Blocked {
			assert.Contains(t, row.TriggerPaths, b.Path, "pin %d", i+1)
			assert.True(t, r.Scope.Known, "pin %d", i+1)

			// A direct trigger is a path of a living citation.
			if b.Direct {
				assert.True(t, slices.ContainsFunc(r.Confidence.Living, func(f domain.Finding) bool {
					return f.Path == b.Path
				}), "pin %d", i+1)
			}
		}

		for _, finding := range r.Confidence.Living {
			assert.Contains(t, row.Members, finding.ID, "pin %d: a living finding is a cited member", i+1)
		}

		assert.Equal(t, living, r.Confidence.Facts.Living, "pin %d", i+1)

		// confidence.Assess counts every cited member, living or not, so
		// the facts and the Citations row name one count.
		assert.Equal(t, len(row.Members), r.Confidence.Facts.Cited, "pin %d: cited members", i+1)
	}
}

// The regression tests below hold the findings of the review of Lessons
// › Pins: a key without an eligible action, a copied workspace,
// citations, trigger words, notes, and outcomes.

// pinsLiveCollision gives pin 2 (p9) computed regions that pin 12, a
// later entry with the same rule, already has.
func pinsLiveCollision() *maintain.Inspection {
	in := pinsInspection()
	in.Pins[1].Readings.Scope.Advice = distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Regions: []string{"internal/store"}, Broader: true,
	}

	e := pinsEntry(11, 50, "check-nil-errors", "internal/store", nil, "A second copy for the store.")
	link := pinfile.EntryLink{Link: pinfile.LinkNoProposal}

	in.Doc.Entries = append(in.Doc.Entries, e)
	in.Links.Entries = append(in.Links.Entries, link)
	in.Pins = append(in.Pins, maintain.Pin{Entry: e, Link: link, Readings: pinsUnknown(link.Link.Reason())})

	return in
}

// pinsAppliedCollision gives pin 2 (p9) computed regions that an applied
// proposal without a pin (p40) already records.
func pinsAppliedCollision() *maintain.Inspection {
	in := pinsInspection()
	p40 := pinsRow(40, domain.ProposalApplied, "check-nil-errors", "internal/api",
		[]string{"internal/api", "internal/store"}, "Recorded for both regions.")
	in.Links.NotPresent = append(in.Links.NotPresent, p40)

	return in
}

func TestPinsRetargetNamesThePinThatHasTheComputedRegions(t *testing.T) {
	cases := []struct {
		name  string
		in    func() *maintain.Inspection
		owner string
	}{
		{"another pin", pinsLiveCollision, "pin 12 (line 50) already has this rule with the computed regions"},
		{"an applied row", pinsAppliedCollision,
			"the applied proposal p40 already records this rule with the computed regions"},
	}

	for _, c := range cases {
		f := pinsFake()
		f.pins = c.in()
		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 2)

		assert.NotContains(t, pinsKeys(h), "w", c.name)

		text := pinsDetail(t, h)
		assert.Contains(t, text, "No retarget: "+c.owner+".", c.name)
		assert.NotContains(t, text, "w previews a retarget", c.name)

		h.press("w")
		assert.Equal(t, "No retarget: "+c.owner+` ("check-nil-errors").`, h.m.toast.text, c.name)
		assert.False(t, pinsOf(h).host.open(), "%s: w opens no preview that can only refuse", c.name)
		assert.Empty(t, f.called(), c.name)
	}

	// The pin's own proposal is not another owner, so a pin keeps w for
	// the regions that its own row records.
	f := pinsFake()
	own := pinsInspection()
	own.Pins[1].Link.Applied[0].Regions = []string{"internal/api", "internal/store"}
	f.pins = own
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)
	assert.Contains(t, pinsKeys(h), "w")
}

func TestPinsForeignIndexRootTurnsOffFileWrites(t *testing.T) {
	f := pinsFake()
	f.pins.Index.Root = "/work/original"
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)

	frame := h.view()
	assert.Contains(t, frame, "! The index records another repository: /work/original. Retarget and prune are off.")

	keys := pinsKeys(h)
	assert.NotContains(t, keys, "w")
	assert.NotContains(t, keys, "-")
	assert.Contains(t, keys, "t", "an extraction writes no pin file")

	text := pinsDetail(t, h)
	for _, want := range []string{
		"Retarget and prune are off: the index of this workspace records another repository, /work/original.",
		"A change would write /work/original/.seamark/lessons.yaml and the ledger of this workspace, not " +
			"/work/repo/.seamark/lessons.yaml.",
		"Build the index of this checkout from Overview (press 1, then b), or run this command here, then press r:",
		"seamark index",
	} {
		assert.Contains(t, text, want)
	}

	h.press("-")
	assert.Equal(t, `No prune: the index records another repository, /work/original ("check-nil-errors").`,
		h.m.toast.text)
	assert.False(t, pinsOf(h).host.open())
	assert.Empty(t, f.called())

	// A --db flag points at another database on purpose, so its root
	// is no sign of a copy.
	f = pinsFake()
	f.pins.Index.Root = "/work/original"
	f.dbPath = "/elsewhere/index.db"
	h = pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)
	assert.Subset(t, pinsKeys(h), []string{"w", "-"})
	assert.NotContains(t, h.view(), "another repository")
}

func TestPinsCitationsNameEachFindingAndTheGoneOnes(t *testing.T) {
	day := func(d int) int64 { return time.Date(2026, 6, d, 9, 0, 0, 0, time.UTC).Unix() }
	fix := func(id int64, at int64) domain.Finding {
		return domain.Finding{ID: id, Source: domain.SourceFixConventional, Path: "internal/store/rows.go", CreatedAt: at}
	}

	f := pinsFake()
	pin := &f.pins.Pins[0]
	pin.Link.Applied[0].Members = []int64{301, 302, 303, 304}
	pin.Readings.Confidence.Living = []domain.Finding{fix(301, day(1)), fix(302, day(1)), fix(303, day(3))}

	h := pinsOpen(t, f, 120, 40)
	text := pinsDetail(t, h)

	for _, want := range []string{
		"Citations 3 living of 4 cited",
		"fix:conventional · 2026-06-01 · finding 301 · internal/store/rows.go",
		"fix:conventional · 2026-06-01 · finding 302 · internal/store/rows.go",
		"fix:conventional · 2026-06-03 · internal/store/rows.go",
		"? finding 304: no longer in the index (aged out, reverted or removed)",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "finding 303", "a row that differs needs no ID")
}

func TestPinsTriggerReadingNamesAHandEditOnlyWithoutARetarget(t *testing.T) {
	blocked := distill.TriggerFact{Path: "internal/store/rows.go", Together: 3}
	hand := "edit the regions in .seamark/lessons.yaml by hand"

	f := pinsFake()
	f.pins.Pins[1].Readings.Trigger.Blocked = []distill.TriggerFact{blocked}
	f.pins.Pins[1].Link.Applied[0].TriggerPaths = []string{blocked.Path}
	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 2)
	require.Contains(t, pinsKeys(h), "w")

	text := pinsDetail(t, h)
	assert.Contains(t, text, "! trigger internal/store/rows.go is confirmed by co-change (3 shared commits), but it "+
		"sets no region: the path is not in the working tree")
	assert.NotContains(t, text, "by hand", "w makes the change, so no hand edit is a route")
	assert.NotContains(t, text, blocked.BlockedLine(), "the CLI words stay out of the console")

	// Without a retarget, the hand edit is the route, and the words say
	// so. A full region set is told apart from a gone path.
	full := distill.TriggerFact{Path: "internal/store/rows.go", Region: "internal/store/rows.go", Direct: true}
	f = pinsFake()
	f.pins.Pins[3].Readings.Trigger.Blocked = []distill.TriggerFact{full}
	f.pins.Pins[3].Link.Applied[0].TriggerPaths = []string{full.Path}
	h = pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 4)
	require.NotContains(t, pinsKeys(h), "w")

	assert.Contains(t, pinsDetail(t, h), "! trigger internal/store/rows.go is directly cited by the evidence, but it "+
		"sets no region: the region set is full; "+hand)
}

func TestPinsBlockScalarNoteHasNoTrailingBlankRow(t *testing.T) {
	note := "Return an error.\nThe recover middleware hides it.\n"
	assert.Equal(t, "Return an error.\nThe recover middleware hides it.", pinNoteText(note))
	assert.Equal(t, "Keep\n\nthe gap.", pinNoteText("Keep\n\nthe gap.\n\n"), "inner blank lines stay")

	f := pinsFake()
	f.pins.Pins[0].Entry.Note = note
	h := pinsOpen(t, f, 120, 40)

	p := pinsOf(h)
	cur, ok := p.cursorRow(p.rows())
	require.True(t, ok)

	lines := p.pinDetail(p.inspection(), cur, 120)
	for i, l := range lines {
		if ansi.Strip(l) == "The recover middleware hides it." {
			require.Less(t, i+2, len(lines))
			assert.Empty(t, ansi.Strip(lines[i+1]), "one blank row separates the note")
			assert.True(t, strings.HasPrefix(ansi.Strip(lines[i+2]), "Regions"), "the facts follow at once")

			return
		}
	}

	t.Fatal("the note does not show")
}

func TestPinsNotLandingOutcomeIsAdviceOnly(t *testing.T) {
	reading := func(v outcome.Verdict, post int) outcome.Reading {
		return outcome.Reading{
			Exposed: true, Firings: 4, Matches: 4, PreEvents: 3, PreCommits: 10, PostEvents: post, PostCommits: 6,
			Verdict: v,
		}
	}

	open := func(r outcome.Reading) *harness {
		f := pinsFake()
		f.pins.Pins[1].Readings.Outcome = maintain.OutcomeReading{Known: true, Reading: r}
		f.pins.Pins[1].Readings.Exposure = maintain.ExposureReading{
			Known: true, Fired: true, Exposure: reviews.Exposure{Count: 4, Matches: 4},
		}

		h := pinsOpen(t, f, 120, 40)
		assert.Empty(t, pinsOf(h).selected, "a load marks no pin")
		pinsCursorTo(h, 2)

		return h
	}

	notLanding := open(reading(outcome.VerdictNotLanding, 2))
	working := open(reading(outcome.VerdictWorking, 0))

	text := pinsDetail(t, notLanding)
	assert.Contains(t, text, "Outcome ! not landing — recurred 2× since exposure (fired 4×) "+
		"(derived recurrence check; not proof of cause, and not a reason to prune)")
	assert.Empty(t, pinsOf(notLanding).selected, "the verdict selects nothing")

	// AC-3: the verdict changes no key. A pin that is not landing offers
	// what a working pin with the same scope offers, and no more.
	assert.Equal(t, pinsKeys(working), pinsKeys(notLanding))
	assert.Contains(t, text, "MAINTENANCE w previews a retarget to the computed regions. The - key previews a prune.")
}
