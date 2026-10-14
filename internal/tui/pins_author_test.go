package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the pin authoring in Lessons › Pins (contracts §4.2).
// They drive the real console through the harness over pinsInspection.
// The fake answers the plans with authorPlanFor, so each test sets only
// the plan fields that it reads.

// authorBefore and authorAfter are the file bytes of the scripted plans.
const (
	authorBefore = "pin:\n  - rule: wrap-sql-errors\n    region: internal/store\n    note: Wrap SQL errors.\n"
	authorAfter  = authorBefore + "  - rule: new-rule\n    region: '*'\n    note: New note.\n"
)

// authorPlanFor returns a confirmable plan of req with a real diff. A
// test changes the fields that its case needs.
func authorPlanFor(req maintain.AuthorRequest) *maintain.AuthorPlan {
	var set []string
	if req.Scope == maintain.ScopeRegions {
		set = req.Regions
	}

	p := &maintain.AuthorPlan{
		Request: req, Root: "/work/repo", New: reviews.PinForRegions(req.Rule, req.Note, set),
		Write: true, LinksKnown: true, Notes: []string{"The proposal ledger does not change."}, Confirmable: true,
	}

	authorDiff(p, authorBefore, authorAfter)

	if req.Op != maintain.AuthorCreate {
		p.Link = pinfile.LinkNoProposal
		p.Old = reviews.PinRule{Rule: "keep-changelog-current", Region: "*", Note: "Add a changelog line."}
	}

	if req.Op == maintain.AuthorRemove {
		p.New = reviews.PinRule{}
	}

	return p
}

// authorDiff sets the file bytes of p and the exact diff between them.
func authorDiff(p *maintain.AuthorPlan, before, after string) {
	p.Before, p.After = []byte(before), []byte(after)
	p.Diff, p.Added, p.Removed = integration.UnifiedDiff(pinfile.File, p.Before, p.After)
}

// authorCreateDiff gives a create plan the diff of its own pin: the
// block of RenderPin after authorBefore. authorPlanFor sets one fixed
// diff for every request, so a review that shows its diff needs this
// one. Other operations stay as they are.
func authorCreateDiff(p *maintain.AuthorPlan) {
	if p.Request.Op != maintain.AuthorCreate {
		return
	}

	// A test draft that RenderPin refuses is a defect of the test, so
	// the helper stops at once and does not keep the fixed diff.
	block, err := pinfile.RenderPin(p.New)
	if err != nil {
		panic("the create draft is not a valid pin: " + err.Error())
	}

	authorDiff(p, authorBefore, authorBefore+block)
}

// authorFake returns services over pinsInspection whose plans come from
// authorPlanFor, changed by edit.
func authorFake(edit func(*maintain.AuthorPlan)) *fakeServices {
	f := pinsFake()
	f.planAuthor = func(req maintain.AuthorRequest) (*maintain.AuthorPlan, error) {
		p := authorPlanFor(req)
		if edit != nil {
			edit(p)
		}

		return p, nil
	}

	return f
}

// authorOf returns the authoring state of the Pins sub-view.
func authorOf(h *harness) *pinAuthor {
	return &pinsOf(h).author
}

// authorFill opens a new pin and types a complete draft: a rule, a note
// of two lines, and the scope "these regions" with one region.
func authorFill(h *harness) {
	h.press("n", "enter")
	h.typeText("new-rule")
	h.press("enter", "down", "enter")
	h.typeText("First line.")
	h.press("ctrl+j")
	h.typeText("Second line.")
	h.press("esc", "down", "down", "enter", "a")
	h.typeText("internal/api")
	h.press("enter")
}

// authorReq returns the request of the draft d.
func authorReq(t *testing.T, d *pinDraft) maintain.AuthorRequest {
	t.Helper()
	require.NotNil(t, d)

	return d.request()
}

// authorReviewFlat returns the title, the whole body and the tail of
// the open review on one line. It reads the modal content, not the
// frame, so a text below the scroll window counts too.
func authorReviewFlat(t *testing.T, h *harness) string {
	t.Helper()

	r := authorOf(h).review
	require.NotNil(t, r, "no pin review is open")

	o := r.content(ui.ModalBodyWidth(ui.ModalMinW, h.m.st.width))
	lines := append(append([]string{o.Title}, o.Body...), o.Tail...)

	return lesFlat(ansi.Strip(strings.Join(lines, "\n")))
}

// authorUnavailable returns the keys that the footer of Lessons lists
// as n/a.
func authorUnavailable(h *harness) []string {
	var out []string

	for _, b := range hookLessons(h).keys() {
		if ui.IsUnavailable(b) {
			out = append(out, b.Help().Key)
		}
	}

	return out
}

func TestPinFormCapturesEveryKey(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	h.press("n")
	require.NotNil(t, authorOf(h).form)
	assert.True(t, hookLessons(h).capturing(), "the open form captures for its whole life")

	// With no field focused, no global, switch, or Pins key acts.
	h.press("1", "2", "3", "4", "5", "q", "r", "p", "m", "e", "u", "w", "-", "t", "v", "f", "/", "z")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesPins, hookLessons(h).sub)
	assert.NotNil(t, authorOf(h).form)
	assert.False(t, h.quit)
	assert.NotContains(t, h.m.toast.text, "Reading the current state")
	assert.Empty(t, f.called(), "no key under the form plans or writes")
	assert.Contains(t, footerLine(h.view()), "ctrl+c quit")
	assert.NotContains(t, footerLine(h.view()), "q quit")

	// ? opens the help of the form while no field has focus.
	h.press("?")
	require.True(t, h.m.help)
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Pin form", h.m.page.title)
	h.press("esc")
	require.NotNil(t, authorOf(h).form, "the help returns to the form unchanged")

	// In a text field, every key is a typed character.
	h.press("enter")
	require.Equal(t, pinFieldRule, authorOf(h).form.field)
	h.typeText("12345qr?pmseu")
	assert.Equal(t, "12345qr?pmseu", authorOf(h).newDraft.ruleValue())
	assert.False(t, h.m.help)
	assert.Equal(t, destLessons, h.m.active)

	// A paste with a line break never enters the rule.
	h.paste("one\ntwo")
	assert.Equal(t, "12345qr?pmseu", authorOf(h).newDraft.ruleValue())
	assert.Contains(t, lesFlat(h.view()), "This field takes one line")

	// ctrl+c still reaches the shell, which asks about the draft.
	h.press("ctrl+c")
	assert.True(t, h.m.quitConfirm)
	assert.Contains(t, lesFlat(h.view()), pinfile.File)
	assert.Empty(t, f.called())
}

// TestPinGuidanceHeadingFitsTheMinimumWidth keeps the GUIDANCE heading
// inside the box at the minimum size. The heading line is not wrapped,
// so a longer label would run past the box edge.
func TestPinGuidanceHeadingFitsTheMinimumWidth(t *testing.T) {
	h := pinsOpen(t, authorFake(nil), ui.MinWidth, ui.MinHeight)
	h.press("n")

	inner := ui.BoxInner(ui.MinWidth)
	lines, _ := pinsOf(h).formLines(inner, 100)

	var heading string

	for _, l := range lines {
		if strings.HasPrefix(ansi.Strip(l), "GUIDANCE") {
			heading = l
		}
	}

	require.NotEmpty(t, heading, "the form shows the GUIDANCE heading")
	assert.LessOrEqual(t, ansi.StringWidth(heading), inner, ansi.Strip(heading))
}

func TestPinFormBlankScopeIsIncomplete(t *testing.T) {
	f := authorFake(func(p *maintain.AuthorPlan) {
		p.Issues = maintain.CheckDraft(p.Request, nil)
		p.After, p.Diff, p.Confirmable, p.New = nil, "", false, reviews.PinRule{}
	})
	h := pinsOpen(t, f, 120, 40)

	h.press("n", "enter")
	h.typeText("new-rule")
	h.press("enter", "down", "enter")
	h.typeText("A note.")
	h.press("esc")

	d := authorOf(h).newDraft
	assert.Equal(t, maintain.ScopeUnset, d.scope, "a blank form never reads as repository-wide")

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "Scope ( ) repository-wide ( ) these regions")
	assert.Contains(t, frame, "! Choose repository-wide or these regions.")
	assert.Contains(t, frame, "pin · scope not chosen · new-rule — A note.")
	assert.Contains(t, frame, "GUIDANCE the rendered pin; delivered on one line", "the label claims no delivered text")
	assert.Contains(t, frame, "! 1 item to fix · s still shows the review; y stays off")

	// The review shows the issue and offers no y.
	h.press("s")
	require.NotNil(t, authorOf(h).review)
	assert.Equal(t, maintain.ScopeUnset, authorOf(h).review.req.Scope, "the request carries no scope")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "GUIDANCE the rendered pin; delivered on one line")
	assert.Contains(t, review, "DRAFT CHECK")
	assert.Contains(t, review, "scope: choose repository-wide or these regions")
	assert.Contains(t, review, "y is off: the draft has items to fix")

	h.press("y")
	assert.Equal(t, []string{"PlanAuthor:create"}, f.called(), "y never commits an incomplete draft")

	// esc returns to the form; choosing the scope completes the draft.
	h.press("esc", "down")
	h.press("space")
	assert.Equal(t, maintain.ScopeRepoWide, d.scope)
	assert.Empty(t, d.issues())
	assert.Contains(t, lesBoxFlat(h.view()), "Draft complete")
}

func TestPinEditLinkedShowsReadOnlyRuleAndScope(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	h.press("i")

	form := authorOf(h).form
	require.NotNil(t, form)

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "Edit pin 1 at line 3")
	assert.Contains(t, frame, "Read-only: linked to proposal #4; change the scope with w Retarget.")
	assert.Contains(t, frame, "Read-only: linked to proposal #4; to change the rule, use - Prune and add a new pin with n.",
		"the rule row names the route of a rule, not the retarget")

	// enter on the rule and on a scope row is refused; the note edits.
	h.press("enter")
	assert.Equal(t, pinFieldNone, form.field)
	assert.Equal(t, "Read-only: linked to proposal #4; to change the rule, use - Prune and add a new pin with n.",
		h.m.toast.text)

	h.press("down", "down", "enter")
	assert.Equal(t, maintain.ScopeRegions, form.draft.scope, "the scope stays as the entry has it")

	h.press("up", "enter", "end")
	h.typeText(" Name the query.")
	h.press("esc")

	req := authorReq(t, form.draft)
	assert.True(t, req.NoteSet)
	assert.False(t, req.RuleSet)
	assert.False(t, req.ScopeSet)
	assert.Equal(t, "Wrap SQL errors with the query name. Name the query.", req.Note)

	// a adds no region to a linked pin.
	h.press("a")
	assert.Equal(t, pinFieldNone, form.field)
}

func TestPinRemoveLinkedRoutesToPrune(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	assert.Contains(t, footerLine(h.view()), "n/a: d")

	h.press("d")
	assert.Equal(t, `No remove: it is linked to p4; use - Prune ("wrap-sql-errors").`, h.m.toast.text)
	assert.Nil(t, authorOf(h).review)
	assert.Empty(t, f.called())

	// An unlinked pin opens its removal review, which skips the form.
	pinsCursorTo(h, 5)
	h.press("d")
	require.NotNil(t, authorOf(h).review)
	assert.Nil(t, authorOf(h).form)
	assert.Equal(t, []string{"PlanAuthor:remove"}, f.called())
	assert.Equal(t, pinsRef(pinsInspection(), 5), authorOf(h).review.req.Ref)
	assert.Contains(t, authorReviewFlat(t, h), "Seamark removes pin 5 at line 21 from .seamark/lessons.yaml.")

	h.press("esc")
	assert.Nil(t, authorOf(h).review)
	assert.False(t, hookLessons(h).capturing(), "esc on a removal review returns to the list")
}

func TestPinAuthoringIsNAForPendingAndAmbiguous(t *testing.T) {
	cases := []struct {
		name string
		in   func() *maintain.Inspection
		row  int
		want string
	}{
		{"pending", pinsInspection, 6, "only p3 matches, and it is pending; record it as applied in Proposals first"},
		{"duplicate", pinsInspection, 7, "its link is ambiguous; edit .seamark/lessons.yaml by hand"},
		{"two applied rows", pinsInspection, 9, "its link is ambiguous; edit .seamark/lessons.yaml by hand"},
		{"disagreeing fields", pinsInspection, 11, "its link is ambiguous; edit .seamark/lessons.yaml by hand"},
		{"entry block", pinsInspection, 10, "the entry cannot be edited safely (a multi-line rule value); edit it by " +
			"hand in .seamark/lessons.yaml"},
		{"unreadable file", pinsMalformed, 1, "the pin file could not be read; fix it, then press r"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			f.pins = c.in()
			h := pinsOpen(t, f, 200, 50)
			pinsCursorTo(h, c.row)

			assert.Subset(t, authorUnavailable(h), []string{"i", "d"}, "i and d read n/a")

			for _, k := range []string{"i", "d"} {
				h.press(k)
				assert.Contains(t, h.m.toast.text, c.want, "key %s", k)
				assert.False(t, authorOf(h).open(), "key %s opens nothing", k)
			}

			assert.Empty(t, f.called())
		})
	}
}

// authorNoLedger turns p into the plan of a workspace without a readable
// index: the relationship is unknown, and the ledger blocker routes to
// the index build (P4-8).
func authorNoLedger(p *maintain.AuthorPlan) {
	p.LinksKnown, p.Link, p.Confirmable = false, pinfile.LinkUnknown, false
	p.Blockers = []maintain.AuthorBlocker{{
		Kind: maintain.BlockLedger, Text: "no readable ledger: no readable index",
		Route: "build the index in Overview, then review again",
	}}
}

func TestPinAuthoringDraftsWithUnknownRelationship(t *testing.T) {
	f := authorFake(authorNoLedger)
	f.pins = pinsNoLedger()
	h := pinsOpen(t, f, 120, 40)

	// The linked pin 1 reads as unknown: i opens a form with every row,
	// and the form names the limit.
	pinsCursorTo(h, 1)
	assert.Contains(t, footerLine(h.view()), "i edit")
	h.press("i")
	require.NotNil(t, authorOf(h).form)

	d := authorOf(h).form.draft
	assert.Empty(t, d.ruleLock, "an unknown relationship locks no row")
	assert.Contains(t, lesBoxFlat(h.view()), "Relationship unknown: a linked pin allows only a note edit; saving "+
		"needs a readable index of this workspace.")

	h.press("down", "enter", "end")
	h.typeText(" More.")
	h.press("esc", "s")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "no readable ledger: no readable index; build the index in Overview, then review "+
		"again. Press o to open Overview › build.")
	assert.Contains(t, review, "Link unknown: no readable ledger of this workspace")
	assert.Contains(t, review, "y is off: no readable index")

	h.press("y")
	assert.Equal(t, []string{"PlanAuthor:edit"}, f.called())

	// o closes the review, keeps the draft and opens the index build.
	h.press("o")
	assert.Equal(t, destOverview, h.m.active)
	assert.Nil(t, authorOf(h).review)
	assert.Same(t, d, authorOf(h).form.draft, "the draft stays")

	// d also reviews without a ledger. esc closes the build question
	// first, because it captures every key.
	h.press("esc", "3", "esc")
	pinsCursorTo(h, 5)
	h.press("d")
	require.NotNil(t, authorOf(h).review)
	assert.Equal(t, maintain.AuthorRemove, authorOf(h).review.req.Op)
}

// authorMoved returns the inspection after an entry was added at the
// head of the list: every entry keeps its own lines and moves down.
func authorMoved() *maintain.Inspection {
	in := pinsInspection()
	head := pinsEntry(0, 3, "added-at-head", "docs", nil, "A new first pin.")
	entries := append([]pinfile.Entry{head}, in.Doc.Entries...)
	pins := append([]maintain.Pin{{Entry: head, Link: pinfile.EntryLink{Link: pinfile.LinkNoProposal}}}, in.Pins...)

	for i := range entries {
		entries[i].Index, entries[i].Line = i, 3+4*i
		pins[i].Entry = entries[i]
	}

	in.Doc.Entries, in.Pins = entries, pins
	in.Links.Entries = append([]pinfile.EntryLink{{Link: pinfile.LinkNoProposal}}, in.Links.Entries...)

	return in
}

// authorChanged returns authorMoved after another writer changed the
// note of pin 5 (now at index 5). Its key still matches one entry, but
// its own lines do not, so an edit draft of the pin rebinds and is
// flagged as changed.
func authorChanged() *maintain.Inspection {
	in := authorMoved()
	e := &in.Pins[5].Entry
	e.Note = "A note that another writer changed."
	e.Raw += "# changed\n"
	in.Doc.Entries[5] = *e

	return in
}

// authorReload stores in as the pin inspection and reads the pins.
func authorReload(h *harness, f *fakeServices, in *maintain.Inspection) {
	f.pins = in
	h.send(refreshMsg{kinds: []sectionKind{secPins}})
}

func TestPinDraftRebindsAMovedEntryAndFlagsAChangedOne(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i", "down", "enter", "end")
	h.typeText(" Keep it short.")
	h.press("esc")

	d := authorOf(h).form.draft
	require.True(t, d.dirty())

	// (a) The entry moved: its key and its own lines match one entry.
	authorReload(h, f, authorMoved())

	moved := authorMoved().Pins[5].Entry.Ref()
	assert.Equal(t, moved, d.ref, "the draft follows the moved entry")
	assert.False(t, d.changed)
	assert.Empty(t, d.orphan)
	assert.Equal(t, "Add a changelog line to every user-visible change. Keep it short.", d.noteValue())
	assert.Same(t, d, authorOf(h).edits[moved])

	// (b) The entry changed: its key matches one entry, its lines do not.
	// The edited note keeps the draft; the unedited rule takes the
	// current value.
	changed := authorChanged()
	e := &changed.Pins[5].Entry
	authorReload(h, f, changed)

	assert.True(t, d.changed, "the draft is flagged")
	assert.Equal(t, e.Ref(), d.ref)
	assert.Equal(t, "Add a changelog line to every user-visible change. Keep it short.", d.noteValue())
	assert.Equal(t, "A note that another writer changed.", d.base.Note)
	assert.Contains(t, h.m.toast.text, "changed since the draft started")

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "Entry changed since the draft started.")
	assert.Contains(t, frame, "entry changed")

	// The review shows the current value next to the draft.
	f.planAuthor = func(req maintain.AuthorRequest) (*maintain.AuthorPlan, error) {
		p := authorPlanFor(req)
		p.Old = reviews.PinRule{Rule: e.Rule, Region: e.Region, Note: e.Note}

		return p, nil
	}

	h.press("s")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "ENTRY CHANGED since the draft started")
	assert.Contains(t, review, "Note now: A note that another writer changed. draft: Add a changelog line to every "+
		"user-visible change. Keep it short.")
	assert.Equal(t, e.Ref(), authorOf(h).review.req.Ref, "the review binds the current entry")

	// A reload that moves the entry again closes the open review, because
	// its plan names the old binding. The draft stays and follows.
	authorReload(h, f, pinsInspection())
	assert.Nil(t, authorOf(h).review)
	assert.Contains(t, h.m.toast.text, "The pin review closed, because its pin changed")
	assert.NotNil(t, authorOf(h).form)
	assert.Empty(t, d.orphan)
	assert.Equal(t, 0, f.callCount("CommitAuthor:edit"))
}

func TestPinDraftIsOrphanedWithoutAUniqueMatch(t *testing.T) {
	cases := []struct {
		name string
		in   func() *maintain.Inspection
	}{
		{"the entry left the file", func() *maintain.Inspection {
			in := pinsInspection()
			in.Pins = append(in.Pins[:4:4], in.Pins[5:]...)
			in.Doc.Entries = append(in.Doc.Entries[:4:4], in.Doc.Entries[5:]...)

			return in
		}},
		{"another entry now has its key and its lines", func() *maintain.Inspection {
			in := pinsInspection()
			twin := in.Pins[4]
			twin.Entry.Index, twin.Entry.Line = len(in.Pins), 50
			in.Pins = append(in.Pins, twin)
			in.Doc.Entries = append(in.Doc.Entries, twin.Entry)

			return in
		}},
		{"another pin sits at its position", func() *maintain.Inspection {
			in := pinsInspection()
			e := pinsEntry(4, 21, "other-rule", "web", nil, "Another pin at the same index.")
			in.Pins[4].Entry, in.Doc.Entries[4] = e, e

			return in
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			h := pinsOpen(t, f, 120, 40)
			copied := ""
			h.m.st.copyText = func(text string) error { copied = text; return nil }

			// The reload comes while the note field has the keys.
			pinsCursorTo(h, 5)
			h.press("i", "down", "enter", "end")
			h.typeText(" Orphan me.")

			d := authorOf(h).form.draft
			authorReload(h, f, c.in())
			assert.Equal(t, pinFieldNone, authorOf(h).form.field, "an orphaned draft takes no more typing")

			assert.NotEmpty(t, d.orphan)
			assert.Contains(t, authorOf(h).orphans, d)
			assert.NotContains(t, authorOf(h).edits, d.ref, "no entry binds an orphaned draft")

			frame := lesBoxFlat(h.view())
			assert.Contains(t, frame, "This draft lost its pin")
			assert.Contains(t, frame, "orphaned draft")
			assert.Contains(t, footerLine(h.view()), "c copy   x discard   esc close")

			// No review opens, and no row edits.
			h.press("s", "enter", "a")
			assert.Empty(t, f.called(), "an orphaned draft plans nothing")
			assert.Nil(t, authorOf(h).review)
			assert.Equal(t, pinFieldNone, authorOf(h).form.field)

			h.press("c")
			assert.Contains(t, copied, "Orphan me.", "c copies the text")

			// Closed, the Pins list names it. i acts on the cursor pin, so
			// the route to the orphan is a list without a pin row.
			h.press("esc")
			assert.Contains(t, h.view(), "1 edit draft lost its pin: press f, then i, to see it.")
			assert.Equal(t, []string{pinfile.File}, pinsOf(h).drafts(), "the quit question still names the file")

			h.press("f", "i")
			require.NotNil(t, authorOf(h).form)
			assert.Same(t, d, authorOf(h).form.draft)

			h.press("x", "d")
			assert.Empty(t, authorOf(h).orphans)
			assert.Nil(t, authorOf(h).form)
			assert.Empty(t, pinsOf(h).drafts())
		})
	}
}

// authorResultCase is one outcome of TestPinResultRowsNeverRetry.
type authorResultCase struct {
	name    string
	res     maintain.AuthorResult
	err     error
	want    []string
	again   bool
	keeps   bool
	senders int
}

func TestPinResultRowsNeverRetry(t *testing.T) {
	stale := &maintain.StaleError{Differences: []string{"file bytes", "new owner: pending #9"}}

	cases := []authorResultCase{
		{"written", maintain.AuthorResult{Outcome: maintain.Written}, nil, []string{
			"Pin written", "Added the new pin after the last pin of .seamark/lessons.yaml.",
			"Pin file written: .seamark/lessons.yaml", "Ledger no change: pin authoring never writes the proposal ledger",
			"Nothing is retried automatically.", "File written · ledger unchanged",
		}, false, false, 1},
		{"written with an error", maintain.AuthorResult{Outcome: maintain.Written, Err: errors.New("fsync failed")}, nil,
			[]string{"Pin written", "Write error fsync failed"}, false, false, 1},
		{"not written", maintain.AuthorResult{Outcome: maintain.NotWritten, Err: errors.New("rename failed")}, nil,
			[]string{
				"Not written", "still holds its earlier bytes", "Error rename failed", "The draft stays; press enter for " +
					"a fresh review.", "File unchanged · enter reviews again",
			}, true, true, 0},
		{"unknown", maintain.AuthorResult{Outcome: maintain.Unknown, Err: errors.New("disk gone")}, nil, []string{
			"Result unknown", "Result unknown: check .seamark/lessons.yaml.", "Pin file unknown: check " +
				".seamark/lessons.yaml", "Result unknown · enter reviews again",
		}, true, true, 1},
		{"stale", maintain.AuthorResult{Outcome: maintain.NotWritten, Err: stale}, stale, []string{
			"Not written", "Something changed since the review — review again. Nothing was written.", "WHAT CHANGED",
			"· file bytes", "· new owner: pending #9", "Nothing was written · enter reviews again",
		}, true, true, 0},
		{"busy", maintain.AuthorResult{Outcome: maintain.NotWritten, Err: services.ErrBusy}, services.ErrBusy,
			[]string{"Another operation is running, so this one did not start."}, true, true, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) { return c.res, c.err }

			h := pinsOpen(t, f, 120, 40)
			probe := &settingsProbe{}
			h.m.screens[destDoctor] = probe

			authorFill(h)
			h.press("s", "y")
			require.Equal(t, 1, f.callCount("CommitAuthor:create"))

			r := authorOf(h).review
			require.NotNil(t, r)
			require.Equal(t, flowResult, r.stage)
			assert.False(t, h.m.st.busy(), "the result frees the mutation slot")

			frame := authorReviewFlat(t, h)
			for _, want := range c.want {
				assert.Contains(t, frame, want)
			}

			assert.Len(t, probe.lessons, c.senders, "lessonsSavedMsg follows Written or Unknown only")

			if c.senders > 0 {
				assert.Equal(t, "Lessons › Pins", probe.lessons[0].by)
			}

			assert.Equal(t, c.keeps, authorOf(h).newDraft != nil, "the draft stays unless the file was written")

			// No key of the result writes again. enter reviews again or
			// closes; y does nothing.
			h.press("y", "enter")
			assert.Equal(t, 1, f.callCount("CommitAuthor:create"), "a result never retries")

			if c.again {
				assert.Equal(t, 2, f.callCount("PlanAuthor:create"), "enter builds a fresh review")
				assert.Equal(t, flowPreview, authorOf(h).review.stage)
			} else {
				assert.Nil(t, authorOf(h).review)
				assert.False(t, authorOf(h).open(), "a written draft closes its form")
			}
		})
	}
}

func TestPinResultShowsAFailedReloadAndReadsAgain(t *testing.T) {
	f := authorFake(nil)
	f.commitAuthor = func(p *maintain.AuthorPlan) (maintain.AuthorResult, error) {
		f.pins = pinsDocErr()

		return maintain.AuthorResult{Outcome: maintain.Written}, nil
	}

	h := pinsOpen(t, f, 120, 40)
	authorFill(h)
	h.press("s", "y")

	r := authorOf(h).review
	require.NotNil(t, r)
	require.True(t, r.out.check.shows())

	frame := authorReviewFlat(t, h)
	assert.Contains(t, frame, "Saved: .seamark/lessons.yaml holds the reviewed bytes. Reading it again failed:")
	assert.Contains(t, frame, "Nothing is retried; r reads again.")

	// r reads again; it never writes again. A good read removes the row.
	// r reads the sources of the commit (pinWriteRefresh), so Doctor and
	// the evidence view also reload (T-3 of the gate 7 recheck).
	f.pins = pinsInspection()
	fresh := &doctor.Report{}
	f.doctor = fresh
	reads, learning := f.readCount("Pins"), f.readCount("Learning")

	h.press("r")
	assert.Equal(t, reads+1, f.readCount("Pins"))
	assert.Same(t, fresh, h.m.st.doctor.value, "r reads Doctor again")
	assert.Greater(t, f.readCount("Learning"), learning, "r reads the evidence again")
	assert.Equal(t, 1, f.callCount("CommitAuthor:create"))
	assert.False(t, r.out.check.shows())
}

func TestPinAuthoringHostileTextIsInert(t *testing.T) {
	const hostile = "\x1b]0;pwned\x07\x1b[2J\u009b31m\u202e"

	in := pinsASCIIInspection()
	e := &in.Pins[4].Entry
	e.Rule = "keep-" + hostile + "changelog"
	e.Note = "Note " + hostile + " text."
	e.Region = "docs" + hostile
	in.Doc.Entries[4] = *e

	f := authorFake(func(p *maintain.AuthorPlan) {
		p.Confirmable = false
		p.Old = reviews.PinRule{Rule: e.Rule, Region: e.Region, Note: e.Note}
		p.New = reviews.PinRule{Rule: "new-" + hostile, Region: "api" + hostile, Note: "New " + hostile}
		p.Diff, _, _ = integration.UnifiedDiff(pinfile.File, []byte(authorBefore), []byte(authorBefore+"  - rule: "+hostile+"\n"))
		p.Blockers = []maintain.AuthorBlocker{{Kind: maintain.BlockOwner, Text: "held by " + hostile, Route: hostile}}
		p.Issues = []maintain.FieldIssue{{Field: maintain.FieldRegions, Index: 0, Text: "bad " + hostile}}
		p.Missing = []string{"future" + hostile}
		p.History = []domain.Proposal{pinsRow(2, domain.ProposalSuperseded, "old-"+hostile, "x", nil, "n")}
		p.Notes = []string{"note " + hostile}
	})
	f.pins = in

	stale := &maintain.StaleError{Differences: []string{"diff " + hostile}}
	f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: stale}, stale
	}

	for _, ascii := range []bool{false, true} {
		h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
		h.run(h.m.Init())

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			h.press("3", "m")
			pinsCursorTo(h, 5)

			check := func(scene string) {
				raw := h.m.View()
				for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u202e"} {
					assert.NotContains(t, raw, bad, "%s at %v", scene, size)
				}

				assertFrame(t, h.view(), size[0], size[1])

				if ascii {
					for _, r := range h.view() {
						require.Less(t, r, rune(128), "ASCII frame of %s at %v", scene, size)
					}
				}
			}

			h.press("i")
			check("form")
			assert.Contains(t, lesBoxFlat(h.view()), "U+001B", "a locked row shows the escape")

			h.press("s")
			check("review")

			h.press("end")
			check("review end")

			// The removal review and its stale result.
			h.press("esc", "esc", "d")
			check("removal review")

			authorOf(h).review.preview.Confirmable = true
			h.press("y")
			check("stale result")

			h.press("esc")
			pinsOf(h).discardDrafts()
		}
	}
}

func TestPinHistoryRowIsNotTheSource(t *testing.T) {
	p2 := pinsRow(2, domain.ProposalSuperseded, "new-rule", "internal/api", nil, "An old note.")
	p5 := pinsRow(5, domain.ProposalDismissed, "new-rule", "internal/api", nil, "A dismissed note.")

	f := authorFake(func(p *maintain.AuthorPlan) { p.History = []domain.Proposal{p2, p5} })
	h := pinsOpen(t, f, 120, 40)

	authorFill(h)
	h.press("s")

	r := authorOf(h).review
	require.NotNil(t, r.preview)
	assert.Nil(t, r.preview.Proposal, "a created pin has no source proposal")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "HISTORY not the source of this pin")
	assert.Contains(t, review, "p2 ○ superseded new-rule")
	assert.Contains(t, review, "p5 ● dismissed new-rule")
	assert.Contains(t, review, "The review never adopts one of them.")
	assert.NotContains(t, review, "linked to p2")
	assert.NotContains(t, review, "Link ", "a create names no relationship")
}

// authorEditPin1Note turns p into the plan of a note edit of pin 1
// (wrap-sql-errors): its old and new rule, and the diff between them.
// authorPlanFor gives an edit the rule of pin 5 and a create diff, so
// a review of pin 1 needs this plan. A new note is a new firing
// identity (reviews.PinIdentity), so IdentityChanged is true, as in
// maintain.
func authorEditPin1Note(p *maintain.AuthorPlan) {
	p.Old = reviews.PinRule{Rule: "wrap-sql-errors", Region: "internal/store", Note: "Wrap SQL errors with the query name."}
	p.New = reviews.PinRule{Rule: "wrap-sql-errors", Region: "internal/store", Note: p.Request.Note}
	p.IdentityChanged = true

	head := "pin:\n  - rule: wrap-sql-errors\n    region: internal/store\n    note: "
	authorDiff(p, head+p.Old.Note+"\n", head+p.New.Note+"\n")
}

// authorEditLinkedPin1Note is authorEditPin1Note with the link of pin 1
// to proposal #4, as in pinsInspection.
func authorEditLinkedPin1Note(p *maintain.AuthorPlan) {
	authorEditPin1Note(p)

	row := pinsInspection().Pins[0].Link.Applied[0]
	p.Link, p.Proposal = pinfile.LinkLinked, &row
}

func TestPinLinkedNoteSaveCallsNoMaintenanceOrExtraction(t *testing.T) {
	f := authorFake(func(p *maintain.AuthorPlan) {
		authorEditLinkedPin1Note(p)
		p.Notes = []string{
			"Earlier deliveries stay recorded under the earlier wording and scope.",
			"Proposal #4 keeps its recorded rule, note, and evidence.",
			"The proposal ledger does not change.",
		}
	})
	f.commitAuthor = func(p *maintain.AuthorPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.Written, NewRef: &pinfile.EntryRef{Index: 0}}, nil
	}

	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	h.press("i", "down", "enter", "end")
	h.typeText(" Name the query.")
	h.press("esc", "s")

	review := authorReviewFlat(t, h)
	del := h.m.st.theme.Glyphs.Del
	assert.Contains(t, review, "Link linked to p4")
	assert.Contains(t, review, del+" pin · internal/store · wrap-sql-errors — Wrap SQL errors with the query name.")
	assert.Contains(t, review, "+ pin · internal/store · wrap-sql-errors — Wrap SQL errors with the query name. "+
		"Name the query.")
	assert.Contains(t, review, "Proposal #4 keeps its recorded rule, note, and evidence.")

	h.press("y")
	assert.Equal(t, []string{"PlanAuthor:edit", "CommitAuthor:edit"}, f.called(),
		"a note save calls no maintenance, no extraction, and no decision")
}

func TestPinMarksAreIgnored(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	h.press("space", "down", "space")
	require.Len(t, pinsOf(h).selected, 2)

	pinsCursorTo(h, 5)
	h.press("i")

	d := authorOf(h).form.draft
	assert.Equal(t, pinsRef(pinsInspection(), 5), d.ref, "i edits the cursor pin")
	assert.Contains(t, lesBoxFlat(h.view()), "Marks are ignored: this acts on the cursor pin only, and the marks stay.")

	h.press("down", "enter")
	h.typeText(" More.")
	h.press("esc", "s")
	assert.Equal(t, pinsRef(pinsInspection(), 5), authorOf(h).review.req.Ref)
	assert.Contains(t, authorReviewFlat(t, h), "Marks are ignored")

	h.press("esc", "esc", "d")
	assert.Equal(t, pinsRef(pinsInspection(), 5), authorOf(h).review.req.Ref, "d removes the cursor pin only")
	assert.Len(t, pinsOf(h).selected, 2, "the marks stay")
}

// authorGateOff turns p into the plan of a workspace whose write gate
// is off. The blocker has the texts of the gate blocker of maintain, and
// it routes to Settings › Pin writing.
func authorGateOff(p *maintain.AuthorPlan) {
	p.Write, p.Confirmable = false, false
	p.Blockers = []maintain.AuthorBlocker{{
		Kind: maintain.BlockGate, Text: "distill.write is off: this preview changes nothing",
		Route: "turn on Settings › Pin writing and preview again",
	}}
}

func TestPinReviewRoutesTheGateToSettings(t *testing.T) {
	f := authorFake(authorGateOff)
	h := pinsOpen(t, f, 120, 40)

	authorFill(h)
	h.press("s")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "distill.write is off: this preview changes nothing; turn on Settings › Pin writing and "+
		"preview again. Press o to open Settings › Pin writing.")
	assert.Contains(t, review, "y is off: distill.write is off")

	h.press("o")
	assert.Equal(t, destSettings, h.m.active)
	assert.Nil(t, authorOf(h).review)
	assert.NotNil(t, authorOf(h).form, "the form waits with its draft")

	// A save of Pin writing closes a review that is open again.
	h.press("3", "s")
	require.NotNil(t, authorOf(h).review)
	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.Nil(t, authorOf(h).review)
	assert.Equal(t, "Settings › Pin writing changed; press s in the pin form to review again.", h.m.toast.text)
	assert.NotNil(t, authorOf(h).newDraft)

	// Another group binds nothing in the review.
	h.press("s")
	h.send(settingsSavedMsg{group: settings.GroupInference})
	assert.NotNil(t, authorOf(h).review)
}

func TestPinDraftsNameTheFileAtQuit(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	// A form without a change leaves no draft.
	h.press("n", "esc")
	assert.Nil(t, authorOf(h).newDraft)
	assert.Empty(t, pinsOf(h).drafts())

	h.press("n", "enter")
	h.typeText("x")
	h.press("esc", "esc")
	assert.Equal(t, []string{pinfile.File}, pinsOf(h).drafts())

	h.press("q")
	require.True(t, h.m.quitConfirm)

	_, body, _ := h.m.quitCopy(false)
	assert.Contains(t, body, "Quitting discards every unsaved draft of .seamark/lessons.yaml.")

	h.press("enter")
	assert.True(t, h.quit)
	assert.Nil(t, authorOf(h).newDraft, "the exit drops the drafts")
}

func TestPinFormKeysOfTheRegionRows(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	// The draft check reads the regions once the scope is chosen.
	h.press("n", "down", "down", "down", "space", "a")
	h.typeText("./api/")
	h.press("enter", "a")
	h.typeText("web")
	h.press("esc")

	d := authorOf(h).newDraft
	assert.Equal(t, maintain.ScopeRegions, d.scope)
	assert.Equal(t, []string{"./api/", "web"}, d.regions, "the rows keep the typed text")

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, `Leading "./" removed.`)
	assert.Contains(t, frame, `Trailing "/" removed.`)
	assert.Contains(t, frame, "A region matches that file, or everything under that directory; no wildcards.")

	// enter edits the cursor region; an empty value removes the row.
	clearKey := tea.KeyMsg{Type: tea.KeyCtrlU}

	h.press("up", "enter")
	h.send(clearKey)
	h.typeText("docs")
	h.press("enter")
	assert.Equal(t, []string{"docs", "web"}, d.regions)

	h.press("down")
	assert.Contains(t, footerLine(h.view()), "x remove region")
	h.press("x")
	assert.Equal(t, []string{"docs"}, d.regions)

	h.press("up", "enter")
	h.send(clearKey)
	h.press("enter")
	assert.Empty(t, d.regions)

	// x on another row asks before it discards the draft.
	h.press("home", "x")
	require.True(t, authorOf(h).form.prompt)
	assert.Contains(t, h.view(), "Discard this draft?")
	assert.Contains(t, lesBoxFlat(h.view()), "The draft of the new pin goes away.")
	h.press("esc")
	assert.False(t, authorOf(h).form.prompt)
	h.press("x", "d")
	assert.Nil(t, authorOf(h).newDraft)
	assert.False(t, authorOf(h).open())
	assert.Empty(t, f.called())
}

func TestPinReviewClosesOnALessonsSaveAndKeepsTheDraft(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	authorFill(h)
	h.press("s")
	require.NotNil(t, authorOf(h).review)

	h.send(pinsSaved)
	assert.Nil(t, authorOf(h).review)
	assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the pin review closed. Press s in the pin form to review again.",
		h.m.toast.text)
	assert.NotNil(t, authorOf(h).form, "the form and its draft stay")

	h.press("y")
	assert.Equal(t, 0, f.callCount("CommitAuthor:create"))

	// A removal review names its own key.
	h.press("esc")
	pinsCursorTo(h, 5)
	h.press("d")
	h.send(pinsSaved)
	assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the pin review closed. Press d in Pins to "+
		"review it again.", h.m.toast.text)
}

func TestPinsEmptyStatesOfferANewPin(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)
	authorReload(h, f, fakePins())

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "Press n to write a pin; the review creates the file.")
	assert.Contains(t, frame, "n new pin p open Proposals e open Evidence")
	assert.NotContains(t, frame, "Pins come from applied proposals")

	in := fakePins()
	in.Doc.Guard.Exists, in.Doc.Bytes = true, []byte("pin: []\n")
	authorReload(h, f, in)
	assert.Contains(t, lesBoxFlat(h.view()), "The file has no pin entry. Press n to write one, or apply a proposal in "+
		"Proposals.")

	h.press("n")
	assert.NotNil(t, authorOf(h).form, "n works on an empty list")
}

// TestPinWorkNames pins the mutation names, because the notice row and
// the quit question quote them.
func TestPinWorkNames(t *testing.T) {
	for op, want := range map[maintain.AuthorOp]string{
		maintain.AuthorCreate: "Writing a new pin", maintain.AuthorEdit: "Editing a pin",
		maintain.AuthorRemove: "Removing a pin",
	} {
		assert.Equal(t, want, pinWorkName(op), op.String())
	}

	assert.Equal(t, "U+001B a U+2028", pinEscaped("\x1b a \u2028"))
	assert.Equal(t, "U+200E U+200F U+061C U+2029", pinEscaped("\u200e \u200f \u061c \u2029"),
		"a bidi mark that the sanitizer keeps still gets an escape (B2-C1)")
	assert.Equal(t, "a\n\tb", strings.ReplaceAll(pinEscaped("a\n\tb"), "    ", "\t"), "a line break and a tab stay")
	assert.Equal(t, strings.Repeat("x", 3), pinEscaped("xxx"))
}

func TestPinEditOpensUnholdableTextReadOnly(t *testing.T) {
	in := pinsInspection()
	e := &in.Pins[4].Entry
	e.Rule = strings.Repeat("r", 300)
	e.Note = "A note with a \x1b[2J control."
	e.Region = "docs\u0085"
	in.Doc.Entries[4] = *e

	f := authorFake(nil)
	f.pins = in
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i")

	d := authorOf(h).form.draft
	rule := pinRuleInput()
	assert.Equal(t, pinLengthLock(rule.MaxRunes()), d.ruleLock, "a rule over the limit names its length")
	assert.Equal(t, pinTextLock, d.noteLock, "a note with a control opens read-only")
	assert.Equal(t, pinTextLock, d.scopeLock, "a region with a C1 control opens read-only")

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "Read-only: this text holds characters the editor cannot show; edit "+
		".seamark/lessons.yaml by hand.")
	assert.Contains(t, frame, "A note with a U+001B[2J control.", "the form shows the escape")

	// No row edits, and the request sets no field.
	h.press("enter")
	assert.Equal(t, pinFieldNone, authorOf(h).form.field)
	h.press("down", "enter")
	assert.Equal(t, pinFieldNone, authorOf(h).form.field)

	req := d.request()
	assert.False(t, req.RuleSet || req.NoteSet || req.ScopeSet, "an untouched field is never set")
	assert.Equal(t, e.Note, req.Note, "the note keeps every byte")
	assert.False(t, d.dirty())
}

// TestPinEditLocksAStoredTextOverTheLimitByLength covers L21 of gate 7.
// A stored rule or region longer than the limit of its input opens
// read-only, and the reason names the length before any key. The note
// has no limit, so the same length stays editable there.
func TestPinEditLocksAStoredTextOverTheLimitByLength(t *testing.T) {
	rule, region := pinRuleInput(), pathInput()

	in := pinsInspection()
	e := &in.Pins[4].Entry
	e.Rule = strings.Repeat("r", rule.MaxRunes()+1)
	e.Note = strings.Repeat("n", rule.MaxRunes()+1)
	e.Region = strings.Repeat("d", region.MaxRunes()+1)
	in.Doc.Entries[4] = *e

	f := authorFake(nil)
	f.pins = in
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i")

	form := authorOf(h).form
	d := form.draft
	want := "Read-only: this text is longer than " + strconv.Itoa(rule.MaxRunes()) +
		" characters; edit .seamark/lessons.yaml by hand."

	assert.Equal(t, pinLengthLock(rule.MaxRunes()), d.ruleLock, "a rule over the limit names its length")
	assert.Equal(t, pinLengthLock(region.MaxRunes()), d.scopeLock, "a region over the limit locks the scope at load")
	assert.Empty(t, d.noteLock, "a note has no limit")

	frame := lesBoxFlat(h.view())
	assert.Equal(t, 2, strings.Count(frame, want), "the rule and the scope rows say why before a key")
	assert.NotContains(t, frame, "cannot show", "no row names characters")

	// enter on the rule row refuses with the reason; the note opens.
	h.press("enter")
	assert.Equal(t, pinFieldNone, form.field)
	assert.Equal(t, want, h.m.toast.text)

	h.press("down", "enter")
	assert.Equal(t, pinFieldNote, form.field, "the note edits")

	h.press("esc")

	// enter and x on the region row refuse with the same reason, and the
	// region stays.
	form.row = pinRowFirstRegion
	h.m.toast.text = ""

	h.press("enter")
	assert.Equal(t, pinFieldNone, form.field)
	assert.Equal(t, want, h.m.toast.text)

	h.m.toast.text = ""

	h.press("x")
	assert.Equal(t, want, h.m.toast.text)
	assert.Equal(t, []string{e.Region}, d.regions)

	req := d.request()
	assert.False(t, req.RuleSet || req.NoteSet || req.ScopeSet, "an untouched field is never set")
}

// TestPinDraftFollowDropsTheLengthLockOfATypedValue checks a reload that
// brings a stored text over the limit for a field that the user edited.
// The typed value replaces the stored text, so the length lock goes
// away, as the characters lock does. The pin key trims the rule and the
// regions, so such an entry can still match the draft by key alone.
func TestPinDraftFollowDropsTheLengthLockOfATypedValue(t *testing.T) {
	rule, region := pinRuleInput(), pathInput()
	pin := pinsInspection().Pins[4]
	d := newEditDraft(&pin, true)
	require.Empty(t, d.ruleLock)
	require.Empty(t, d.scopeLock)

	d.rule.Set("typed-rule")
	d.scope, d.regions = maintain.ScopeRegions, []string{"typed/region"}

	long := pin
	long.Entry.Rule = strings.Repeat("r", rule.MaxRunes()+1)
	long.Entry.Region = strings.Repeat("d", region.MaxRunes()+1)

	assert.Empty(t, d.follow(&long, true, rebindChanged), "an unlinked pin keeps every edit")
	assert.Empty(t, d.ruleLock, "the typed rule replaces the long stored rule")
	assert.Empty(t, d.scopeLock, "the typed regions replace the long stored region")
	assert.Equal(t, "typed-rule", d.ruleValue())
	assert.Equal(t, []string{"typed/region"}, d.regions)

	// A field without an edit takes the stored text and its lock.
	plain := newEditDraft(&pin, true)
	assert.Empty(t, plain.follow(&long, true, rebindChanged))
	assert.Equal(t, pinLengthLock(rule.MaxRunes()), plain.ruleLock)
	assert.Equal(t, pinLengthLock(region.MaxRunes()), plain.scopeLock)
}

// TestPinInputLockNamesCharactersBeforeLength checks the order of
// pinInputLock (Y-3 of the gate 7 recheck). A text over the limit with a
// refused character gets the characters reason, because a shorter text
// would still stay read-only.
func TestPinInputLockNamesCharactersBeforeLength(t *testing.T) {
	for _, c := range []struct {
		name  string
		input func() ui.LineInput
		text  string
	}{
		{"rule", pinRuleInput, "\x1b"},
		{"region", pathInput, "docs\u0085"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := c.input()
			long := strings.Repeat("a", in.MaxRunes()+1)

			assert.Empty(t, pinInputLock(&in, "plain"))
			assert.Equal(t, pinLengthLock(in.MaxRunes()), pinInputLock(&in, long))
			assert.Equal(t, pinTextLock, pinInputLock(&in, c.text))
			assert.Equal(t, pinTextLock, pinInputLock(&in, c.text+long), "the characters reason comes first")
		})
	}
}

// TestPinEditRegionLockNamesCharactersBeforeLength checks the scope lock
// of a pin with several regions (Y-1 of the gate 7 recheck). One lock
// covers every region row, so a refused character in any region comes
// before a length, as in pinInputLock. A linked pin keeps its own reason.
func TestPinEditRegionLockNamesCharactersBeforeLength(t *testing.T) {
	region := pathInput()
	long := strings.Repeat("a", region.MaxRunes()+1)
	length := pinLengthLock(region.MaxRunes())

	for _, c := range []struct {
		name    string
		pin     int
		regions []string
		want    string
	}{
		{"long before a control", 4, []string{long, "docs\u0085"}, pinTextLock},
		{"control before long", 4, []string{"docs\u0085", long}, pinTextLock},
		{"long before plain", 4, []string{long, "docs"}, length},
		{"plain before long", 4, []string{"docs", long}, length},
		{"linked", 0, []string{long, "docs\u0085"}, "linked to proposal #4; change the scope with w Retarget"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pin := pinsInspection().Pins[c.pin]
			pin.Entry.Region, pin.Entry.Regions = "", c.regions

			d := newEditDraft(&pin, true)
			require.Equal(t, c.regions, d.regions)
			assert.Equal(t, c.want, d.scopeLock)
		})
	}
}

func TestPinEditKeepsALegacyNoteLineBreak(t *testing.T) {
	in := pinsInspection()
	e := &in.Pins[4].Entry
	e.Note = "A legacy block note.\n"
	in.Doc.Entries[4] = *e

	f := authorFake(nil)
	f.pins = in
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i")

	d := authorOf(h).form.draft
	assert.Empty(t, d.noteLock, "a trailing line break is ordinary text")
	assert.Equal(t, e.Note, d.noteValue(), "the note loads unchanged")
	assert.Empty(t, d.issues(), "an untouched note meets no check")

	// Only an edit meets the verdict of the shared text check.
	h.press("down", "enter", "up", "home")
	h.typeText("Edited: ")
	h.press("esc")

	assert.True(t, d.request().NoteSet)
	assert.Contains(t, lesBoxFlat(h.view()), "the note ends with an empty line; remove it")
}

func TestPinContextHelpNamesTheFormAndTheReview(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	authorFill(h)

	h.press("?")
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Pin form", h.m.page.title)
	assert.Contains(t, lesFlat(h.view()), "A region matches that file, or everything under that directory; no wildcards.")
	h.press("esc")

	h.press("s", "?")
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Pin review", h.m.page.title)
	assert.Contains(t, h.m.page.text, sampleOpenHelp, "a plan with a candidate offers s")
	h.press("esc")
	assert.NotNil(t, authorOf(h).review, "the help returns to the review unchanged")

	// The discard question has no help of its own: ? does nothing.
	h.press("esc", "home", "x", "?")
	assert.False(t, h.m.help)
	assert.True(t, authorOf(h).form.prompt)
}

func TestPinReviewHelpNamesWhereEscReturns(t *testing.T) {
	const (
		ledger = "Pin authoring never changes the proposal ledger."
		toForm = "esc returns to the form and keeps the draft."
		toList = "esc closes the review and returns to the Pins list."
	)

	// helpText opens the help of the open review and returns its
	// paragraphs as one text, so a sentence inside a paragraph matches.
	helpText := func(h *harness) string {
		t.Helper()

		h.press("?")
		require.NotNil(t, h.m.page)
		require.Equal(t, "Pin review", h.m.page.title)

		text := strings.Join(h.m.page.text, "\n")
		h.press("esc")

		return text
	}

	f := authorFake(nil)
	f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.Written}, nil
	}

	h := pinsOpen(t, f, 120, 40)

	// The review of a draft lies over its form, so esc returns there.
	authorFill(h)
	h.press("s")
	require.NotNil(t, authorOf(h).form)

	text := helpText(h)
	assert.Contains(t, text, ledger)
	assert.Contains(t, text, toForm)
	assert.NotContains(t, text, toList)

	// A written draft goes away with its form, so esc returns to the list.
	h.press("y")
	require.NotNil(t, authorOf(h).review)
	require.Nil(t, authorOf(h).form, "the written draft closes its form")

	text = helpText(h)
	assert.Contains(t, text, toList)
	assert.NotContains(t, text, toForm)
	h.press("enter")

	// A removal skips the form, so esc returns to the list.
	pinsCursorTo(h, 5)
	h.press("d")
	require.NotNil(t, authorOf(h).review)
	require.Nil(t, authorOf(h).form, "a removal opens no form")

	text = helpText(h)
	assert.Contains(t, text, ledger, "a removal also leaves the ledger alone")
	assert.Contains(t, text, toList)
	assert.NotContains(t, text, toForm)

	h.press("esc")
	assert.Nil(t, authorOf(h).review)
	assert.Nil(t, authorOf(h).form, "esc on a removal review returns to the list")
}

func TestPinWrittenPutsTheCursorOnTheNewPin(t *testing.T) {
	f := authorFake(nil)
	last := pinsRef(pinsInspection(), 11)
	f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.Written, NewRef: &last}, nil
	}

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 1)
	authorFill(h)
	h.press("s", "y", "enter")

	cur, ok := pinsOf(h).cursorRow(pinsOf(h).rows())
	require.True(t, ok)
	assert.Equal(t, last, cur.pin.Entry.Ref(), "the cursor follows the written entry")
	assert.Empty(t, h.m.toast.text, "the user caused the change, so no loss is reported")
}

// The regression tests of the B2 review round. Each one names the
// review finding that it covers.

// authorGone returns pinsInspection without pin 5, so an edit draft of
// pin 5 has no entry to follow.
func authorGone() *maintain.Inspection {
	in := pinsInspection()
	in.Pins = append(in.Pins[:4:4], in.Pins[5:]...)
	in.Doc.Entries = append(in.Doc.Entries[:4:4], in.Doc.Entries[5:]...)

	return in
}

// authorOrphan types a note edit of pin 5, orphans the draft with a
// reload, and closes its form. It returns the orphaned draft.
func authorOrphan(t *testing.T, h *harness, f *fakeServices) *pinDraft {
	t.Helper()

	pinsCursorTo(h, 5)
	h.press("i", "down", "enter", "end")
	h.typeText(" Orphan me.")
	h.press("esc")

	d := authorOf(h).form.draft
	authorReload(h, f, authorGone())
	h.press("esc")
	require.Equal(t, []*pinDraft{d}, authorOf(h).orphans)
	require.Nil(t, authorOf(h).form)

	return d
}

// TestPinDraftUneditedFieldTakesTheCurrentValue covers B2-C2: after a
// rebind by key alone, a field that the user did not edit takes the
// value of the other writer. A stale value must never count as an edit,
// because the review would then revert that change.
func TestPinDraftUneditedFieldTakesTheCurrentValue(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i", "enter", "end")
	h.typeText("-now")
	h.press("esc")

	d := authorOf(h).form.draft
	require.Equal(t, "keep-changelog-current-now", d.ruleValue())

	changed := pinsInspection()
	e := &changed.Pins[4].Entry
	e.Note = "A note that another writer changed."
	e.Raw += "# changed\n"
	changed.Doc.Entries[4] = *e
	authorReload(h, f, changed)

	require.True(t, d.changed, "a rebind by key alone flags the draft")
	assert.Equal(t, "A note that another writer changed.", d.noteValue(), "the unedited note takes the current value")
	assert.Equal(t, "keep-changelog-current-now", d.ruleValue(), "the edited rule keeps the draft value")

	req := d.request()
	assert.True(t, req.RuleSet)
	assert.False(t, req.NoteSet, "the note of the other writer is no edit of the draft")
	assert.False(t, req.ScopeSet)
}

// TestPinDraftDropsRuleAndScopeEditsWhenTheLinkAppears covers B2-BEH-1:
// a reload that shows a linked pin locks its rule and scope. The edits
// that the user made while the relationship was unknown go away with a
// note, and a locked row keeps no focus.
func TestPinDraftDropsRuleAndScopeEditsWhenTheLinkAppears(t *testing.T) {
	f := authorFake(nil)
	f.pins = pinsNoLedger()
	h := pinsOpen(t, f, 120, 40)

	// While the relationship is unknown, every row edits.
	pinsCursorTo(h, 1)
	h.press("i", "down", "enter", "end")
	h.typeText(" Name the query.")
	h.press("esc", "a")
	h.typeText("internal/extra")
	h.press("enter", "home", "enter", "end")
	h.typeText("-edited")

	form := authorOf(h).form
	d := form.draft
	require.Equal(t, pinFieldRule, form.field)

	req := d.request()
	require.True(t, req.RuleSet && req.NoteSet && req.ScopeSet)

	// The index build of the o route reads the pins again: pin 1 is
	// linked to p4 now, so only its note edits.
	authorReload(h, f, pinsInspection())

	assert.Equal(t, pinFieldNone, form.field, "a locked row keeps no focus")
	assert.Equal(t, "wrap-sql-errors", d.ruleValue(), "the rule edit goes away")
	assert.Equal(t, []string{"internal/store"}, d.regions, "the scope edit goes away")
	assert.Less(t, form.row, form.rows())

	req = d.request()
	assert.False(t, req.RuleSet)
	assert.False(t, req.ScopeSet)
	assert.True(t, req.NoteSet, "the note edit stays")
	assert.Equal(t, "Wrap SQL errors with the query name. Name the query.", req.Note)
	assert.Contains(t, h.m.toast.text, `The rule and scope edits of "wrap-sql-errors" went away: the pin is linked `+
		"to p4, so only its note edits; change the scope with w Retarget")

	// No row takes the typing now.
	h.typeText("-more")
	assert.Equal(t, "wrap-sql-errors", d.ruleValue())
	assert.Contains(t, lesBoxFlat(h.view()), "Read-only: linked to proposal #4; change the scope with w Retarget.")
}

// TestPinDraftReloadKeepsTheCursorOfAFocusedField covers B2-BEH-3: a
// reload of an unchanged entry keeps the text widgets, so the next key
// lands at the cursor. It holds for an edited field and for a field
// where the user only moved the cursor.
func TestPinDraftReloadKeepsTheCursorOfAFocusedField(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	// The cursor moved, and nothing is typed yet.
	pinsCursorTo(h, 5)
	h.press("i", "down", "enter", "home")

	d := authorOf(h).form.draft
	authorReload(h, f, pinsInspection())
	h.typeText("AAA ")
	assert.Equal(t, "AAA Add a changelog line to every user-visible change.", d.noteValue())

	// The field holds an edit.
	authorReload(h, f, pinsInspection())
	h.typeText("BBB ")
	assert.Equal(t, "AAA BBB Add a changelog line to every user-visible change.", d.noteValue())

	// The same for the rule.
	h.press("esc", "up", "enter", "home")
	authorReload(h, f, pinsInspection())
	h.typeText("x-")
	authorReload(h, f, pinsInspection())
	h.typeText("y-")
	assert.Equal(t, "x-y-keep-changelog-current", d.ruleValue())
}

// TestPinDraftOrphanKeepsATypedRegionAndDropsAnUntouchedDraft covers
// B2-BEH-4 and B2-C4. A region that the user typed but did not confirm
// stays in an orphaned draft. A draft without a change is no lost work,
// so it goes away with its form instead of turning into an orphan.
func TestPinDraftOrphanKeepsATypedRegionAndDropsAnUntouchedDraft(t *testing.T) {
	t.Run("a typed region", func(t *testing.T) {
		f := authorFake(nil)
		h := pinsOpen(t, f, 120, 40)
		copied := ""
		h.m.st.copyText = func(text string) error { copied = text; return nil }

		pinsCursorTo(h, 5)
		h.press("i", "a")
		h.typeText("internal/typed")

		d := authorOf(h).form.draft
		authorReload(h, f, authorGone())

		require.NotEmpty(t, d.orphan)
		assert.Equal(t, pinFieldNone, authorOf(h).form.field)
		assert.Equal(t, []string{"internal/typed"}, d.regions, "the typed region stays in the draft")

		h.press("c")
		assert.Contains(t, copied, "internal/typed", "c copies the typed region")
	})

	t.Run("an untouched draft", func(t *testing.T) {
		f := authorFake(nil)
		h := pinsOpen(t, f, 120, 40)

		pinsCursorTo(h, 5)
		h.press("i", "down", "enter")
		authorReload(h, f, authorGone())

		assert.Empty(t, authorOf(h).orphans, "a draft without a change is no lost work")
		assert.Empty(t, authorOf(h).edits)
		assert.Nil(t, authorOf(h).form, "its form closes")
		assert.Empty(t, pinsOf(h).drafts())
		assert.Contains(t, h.m.toast.text, `The edit form of "keep-changelog-current" closed: its pin left `+
			".seamark/lessons.yaml")
		assert.NotContains(t, h.view(), "lost its pin")
	})
}

// TestPinEditKeepsTheCursorPinWhileAnOrphanWaits covers B2-BEH-5 and
// B2-C3: i acts on the cursor pin (contracts §4.2), also while an
// orphaned draft waits. Only a list without a pin row leads i to the
// orphaned draft.
func TestPinEditKeepsTheCursorPinWhileAnOrphanWaits(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)
	orphan := authorOrphan(t, h, f)

	// An editable cursor pin: i edits it.
	pinsCursorTo(h, 1)
	assert.NotContains(t, authorUnavailable(h), "i")
	h.press("i")
	require.NotNil(t, authorOf(h).form)
	assert.Equal(t, pinsRef(pinsInspection(), 1), authorOf(h).form.draft.ref, "i edits the cursor pin")
	h.press("esc")

	// The pending pin moved up to row 5: i reads n/a and names the
	// reason and the route.
	pinsCursorTo(h, 5)
	assert.Contains(t, authorUnavailable(h), "i")
	h.press("i")
	assert.Contains(t, h.m.toast.text, "only p3 matches, and it is pending")
	assert.Nil(t, authorOf(h).form)

	// A history list has no pin row, so i shows the orphaned draft there.
	// i acts on no list target, so the pane hints, with f as the way out
	// of the list, still come before the n/a group.
	assert.Contains(t, h.view(), "1 edit draft lost its pin: press f, then i, to see it.")
	h.press("f")
	assert.NotContains(t, authorUnavailable(h), "i")
	assert.Contains(t, footerLine(h.view()), "n new pin   i edit   tab detail   f show   / region   z search   n/a:")

	h.press("i")
	require.NotNil(t, authorOf(h).form)
	assert.Same(t, orphan, authorOf(h).form.draft)
}

// TestPinsOrphanLineKeepsTheLimitLine covers B2-BEH-6: the line about
// an orphaned draft never hides the line about a source that limits
// the view.
func TestPinsOrphanLineKeepsTheLimitLine(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)
	authorOrphan(t, h, f)

	authorReload(h, f, pinsMalformed())
	require.Len(t, authorOf(h).orphans, 1, "an unreadable file binds nothing, so the orphan stays")

	frame := lesFlat(h.view())
	assert.Contains(t, frame, "! .seamark/lessons.yaml could not be read (line 9)", "the limit line stays")
	assert.Contains(t, frame, "! 1 edit draft lost its pin: press f, then i, to see it.")
}

// TestPinsDetailOfABlockedFileNamesTheBlock covers B2-BEH-7: when the
// pin file cannot be changed, the detail never promises that i and d
// change it. The keys still draft and review (contracts §4.2), and the
// review names the block.
func TestPinsDetailOfABlockedFileNamesTheBlock(t *testing.T) {
	f := authorFake(nil)
	in := pinsInspection()
	in.Doc.FileBlock = "the pin file is a symbolic link"
	f.pins = in
	h := pinsOpen(t, f, 200, 50)

	pinsCursorTo(h, 5)
	detail := pinsDetail(t, h)
	assert.NotContains(t, detail, "i edits the rule, the note and the regions of this pin")
	assert.Contains(t, detail, "The pin file cannot be edited (the pin file is a symbolic link). i and d still "+
		"draft a change and show the review; y stays off. Edit the pin by hand in .seamark/lessons.yaml.")

	pinsCursorTo(h, 1)
	assert.Contains(t, pinsDetail(t, h), "i still drafts a note edit and shows the review; y stays off.")
}

// TestPinEditOpensSeparatorAndBidiTextReadOnly covers B2-BEH-2 and
// B2-C1: a legacy rule, note or region with a separator or a bidi mark
// opens read-only, and the form and the review show each such
// character as a U+XXXX escape. A trailing line break stays editable
// (TestPinEditKeepsALegacyNoteLineBreak).
func TestPinEditOpensSeparatorAndBidiTextReadOnly(t *testing.T) {
	const marks = "\u200e\u200f\u061c\u2028\u2029"

	in := pinsInspection()
	e := &in.Pins[4].Entry
	e.Rule = "keep\u200fchangelog"
	e.Note = "Line one\u2028line two \u200eltr\u061c end."
	e.Region = "docs\u2029"
	in.Doc.Entries[4] = *e

	// The plan quotes the entry in its diff, its blockers, its notes and
	// its missing regions, as a real plan does.
	f := authorFake(func(p *maintain.AuthorPlan) {
		p.Old = reviews.PinRule{Rule: e.Rule, Region: e.Region, Note: e.Note}
		p.New = p.Old
		before := "pin:\n  - rule: " + e.Rule + "\n    region: " + e.Region + "\n    note: " + e.Note + "\n"
		p.Diff, p.Added, p.Removed = integration.UnifiedDiff(pinfile.File, []byte(before), []byte(before+"  - rule: x\n"))
		p.Blockers = []maintain.AuthorBlocker{{Kind: maintain.BlockOwner, Text: "held by " + e.Rule, Route: e.Region}}
		p.Notes = []string{"Notes quote " + e.Note}
		p.Missing = []string{e.Region}
	})
	f.pins = in
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i")

	d := authorOf(h).form.draft
	assert.Equal(t, pinTextLock, d.ruleLock, "a rule with a bidi mark opens read-only")
	assert.Equal(t, pinTextLock, d.noteLock, "a note with a separator opens read-only")
	assert.Equal(t, pinTextLock, d.scopeLock, "a region with a separator opens read-only")

	frame := lesBoxFlat(h.view())
	assert.Contains(t, frame, "keepU+200Fchangelog")
	assert.Contains(t, frame, "Line oneU+2028line two U+200EltrU+061C end.")
	assert.Contains(t, frame, "docsU+2029")
	assert.NotContains(t, h.m.View(), "\u200e", "the form shows no raw mark")

	for _, r := range marks {
		assert.NotContains(t, h.m.View(), string(r), "form: %U", r)
	}

	h.press("s")

	review := authorReviewFlat(t, h)
	assert.Contains(t, review, "U+200E")
	assert.Contains(t, review, "U+2028")

	for _, r := range marks {
		assert.NotContains(t, review, string(r), "review: %U", r)
		assert.NotContains(t, h.m.View(), string(r), "review frame: %U", r)
	}
}

// TestPinBlockWordsOfARemoval covers B2-C14: a removal has no draft, so
// its entry blocker names the review instead.
func TestPinBlockWordsOfARemoval(t *testing.T) {
	f := authorFake(func(p *maintain.AuthorPlan) {
		p.Confirmable = false
		p.Blockers = []maintain.AuthorBlocker{{Kind: maintain.BlockEntry, Text: "the entry changed"}}
	})
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("d")
	assert.Contains(t, authorReviewFlat(t, h), "y is off: the pin changed since Pins read it")
	assert.NotContains(t, authorReviewFlat(t, h), "since the draft started")
}

// TestPinAuthoringTreatsAForeignRootAsUnknown covers B2-C13: in a copied
// checkout the index records another root, so the authoring keys treat
// the relationship as unknown (contracts §4.2). A linked pin then opens
// every row with the unknown note, and the review names the blocker.
func TestPinAuthoringTreatsAForeignRootAsUnknown(t *testing.T) {
	f := authorFake(nil)
	in := pinsInspection()
	in.Index.Root = "/work/original"
	f.pins = in
	h := pinsOpen(t, f, 200, 50)

	pinsCursorTo(h, 1)
	assert.NotContains(t, authorUnavailable(h), "d", "d is no dead route to the prune that the root stops")
	assert.Contains(t, pinsDetail(t, h), "i and d still draft a change; saving needs a readable index of this "+
		"workspace.")

	h.press("i")
	require.NotNil(t, authorOf(h).form)

	d := authorOf(h).form.draft
	assert.Empty(t, d.ruleLock)
	assert.True(t, d.unknown)
	assert.Contains(t, lesBoxFlat(h.view()), "Relationship unknown: a linked pin allows only a note edit")
}

// TestPinPlanReachesOnlyTheOpenReview covers B2-C11: a plan message of a
// review that is no longer open changes nothing.
func TestPinPlanReachesOnlyTheOpenReview(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	old := newPinReview(h.m.st, maintain.AuthorRequest{Op: maintain.AuthorRemove}, nil, false, "")
	seq := old.seq.start()
	old.startRead()

	h.send(pinPlannedMsg{r: old, seq: seq, plan: authorPlanFor(old.req)})
	assert.Equal(t, flowPreparing, old.stage, "a detached review gets no plan")
	assert.Nil(t, authorOf(h).review)
}

// TestPinChangedEntryKeepsATypedRegion covers the reload of a changed
// entry while a new region is typed. The typed region joins the rows
// before the reload, so the scope counts as edited and keeps the rows of
// the draft. The input closes, and the cursor stays on the new region.
func TestPinChangedEntryKeepsATypedRegion(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("i")

	form := authorOf(h).form
	require.NotNil(t, form)

	d := form.draft
	require.Empty(t, d.regions)

	for form.row < form.addRow() {
		h.press("down")
	}

	h.press("enter")
	h.typeText("internal/new")
	require.Equal(t, pinFieldRegion, form.field)

	// The added regions are only spaces, so the key of the entry stays
	// the same and the draft follows the changed entry.
	changed := pinsInspection()
	e := &changed.Pins[4].Entry
	e.Regions = []string{" ", "  "}
	e.Raw += "# regions\n"
	changed.Doc.Entries[4] = *e
	authorReload(h, f, changed)

	require.Same(t, d, authorOf(h).form.draft, "the draft follows the entry")
	assert.True(t, d.changed)
	assert.Equal(t, []string{"internal/new"}, d.regions, "the typed region stays, and the scope keeps the draft rows")
	assert.Equal(t, pinFieldNone, form.field, "the input closes")
	assert.Equal(t, pinRowFirstRegion, form.row, "the cursor stays on the new region")
}

// TestPinFormFooterKeepsAnExitAtTheSmallSizes covers M2 of the gate 7
// review. The footer is the only key row of the form, and a cut drops
// the hints from the end. So esc comes second, and esc close stays in
// view at 60x20 and at 80x24 on every kind of row.
func TestPinFormFooterKeepsAnExitAtTheSmallSizes(t *testing.T) {
	rows := []struct {
		name string
		keys []string
	}{
		{"the rule row", []string{"home"}},
		{"a scope row", []string{"home", "down", "down"}},
		{"the row that adds a region", []string{"end"}},
	}

	for _, ascii := range []bool{false, true} {
		for _, size := range [][2]int{{60, 20}, {80, 24}} {
			h := lesOpenTheme(t, authorFake(nil), ascii)
			h.press("m")
			h.resize(size[0], size[1])
			h.press("n")
			require.NotNil(t, authorOf(h).form)

			for _, row := range rows {
				h.press(row.keys...)

				frame := h.view()
				assertFrame(t, frame, size[0], size[1])

				footer := footerLine(frame)
				assert.Contains(t, footer, "esc close", "%s at %dx%d, ascii %t", row.name, size[0], size[1], ascii)

				if size[0] >= 80 {
					assert.Contains(t, footer, "s review", "%s at 80x24, ascii %t", row.name, ascii)
				}
			}
		}
	}
}

// TestPinReviewAgainFollowsAMovedEntry covers L16 of the gate 7 review
// and T-2 of its recheck. A removal has no draft, so its review finds
// its entry again by the rule of pinMatch. An edit draft follows its
// entry at the reload (rebindDrafts). enter after a refusal then plans
// the moved entry and names its new position, as the result text
// promises.
func TestPinReviewAgainFollowsAMovedEntry(t *testing.T) {
	cases := []struct {
		op    string
		again string
		open  func(h *harness)
	}{
		{"remove", "Press enter to review the removal again from the current state.", func(h *harness) {
			h.press("d")
		}},
		{"edit", "Press enter for a new review from the current state.", func(h *harness) {
			h.press("i", "down", "enter", "end")
			h.typeText(" Keep it short.")
			h.press("esc", "s")
		}},
	}

	for _, c := range cases {
		t.Run(c.op, func(t *testing.T) {
			stale := &maintain.StaleError{Differences: []string{"file bytes"}}

			var reqs []maintain.AuthorRequest

			f := authorFake(nil)
			f.planAuthor = func(req maintain.AuthorRequest) (*maintain.AuthorPlan, error) {
				reqs = append(reqs, req)

				return authorPlanFor(req), nil
			}

			// Another writer adds a pin at the head before the commit, so
			// the engine refuses the stale plan. The reload after the
			// refusal reads the moved entry.
			f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
				f.pins = authorMoved()

				return maintain.AuthorResult{Outcome: maintain.NotWritten, Err: stale}, stale
			}

			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, 5)
			c.open(h)
			require.Contains(t, authorReviewFlat(t, h), "pin 5 at line 21", "the first review names the old position")

			h.press("y")

			r := authorOf(h).review
			require.NotNil(t, r)
			require.Equal(t, flowResult, r.stage)
			require.True(t, r.reviewAgain())
			assert.Contains(t, authorReviewFlat(t, h), c.again)

			h.press("enter")
			require.Len(t, reqs, 2)

			moved := authorMoved().Pins[5].Entry.Ref()
			assert.Equal(t, moved, reqs[1].Ref, "the fresh review binds the moved entry")
			assert.Equal(t, flowPreview, authorOf(h).review.stage)

			flat := authorReviewFlat(t, h)
			assert.Contains(t, flat, "pin 6 at line 23", "the review names the new position")
			assert.NotContains(t, flat, "pin 5 at line 21", "the old position goes away")
			assert.Equal(t, 1, f.callCount("CommitAuthor:"+c.op), "enter never commits")
		})
	}
}

// TestPinRemovalReviewClosesWhenItsEntryMoves covers L16 of the gate 7
// review before the commit. The plan names the old binding, so the
// review closes, as the review of an edit draft does.
func TestPinRemovalReviewClosesWhenItsEntryMoves(t *testing.T) {
	f := authorFake(nil)
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 5)
	h.press("d")
	require.NotNil(t, authorOf(h).review)

	// A reload of the same entries keeps the review.
	authorReload(h, f, pinsInspection())
	require.NotNil(t, authorOf(h).review)

	authorReload(h, f, authorMoved())
	assert.Nil(t, authorOf(h).review)
	assert.Contains(t, h.m.toast.text, "The pin review closed, because its pin changed")

	h.press("y")
	assert.Zero(t, f.callCount("CommitAuthor:remove"))
}

// TestPinEditReviewClosesWhenTheReloadChangesItsDraft covers L17 of the
// gate 7 review. A new link drops the rule and scope edits of a draft at
// its binding. The open review then shows a plan that the draft no
// longer holds, so it closes before y can commit it.
func TestPinEditReviewClosesWhenTheReloadChangesItsDraft(t *testing.T) {
	f := authorFake(nil)
	f.pins = pinsNoLedger()
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	h.press("i", "enter", "end")
	h.typeText("-edited")
	h.press("esc", "s")

	r := authorOf(h).review
	require.NotNil(t, r)
	require.True(t, r.req.RuleSet)

	// A reload that keeps the draft keeps the review.
	authorReload(h, f, pinsNoLedger())
	require.Same(t, r, authorOf(h).review)

	// The pin is linked to p4 now, so only its note edits.
	authorReload(h, f, pinsInspection())
	assert.Nil(t, authorOf(h).review)
	assert.Contains(t, h.m.toast.text, "The pin review closed, because the reload changed its draft")
	assert.Contains(t, h.m.toast.text, `The rule edit of "wrap-sql-errors" went away`)

	h.press("y")
	assert.Zero(t, f.callCount("CommitAuthor:edit"))
	assert.NotNil(t, authorOf(h).form, "the form of the draft shows again")
}

// TestPinAuthoringTreatsAForeignRootAsUnknownWithADBFlag covers L18 of
// the gate 7 review. The engine refuses every pin write under another
// root, also with --db (P4-16), so the form treats the relationship as
// unknown. Retarget and prune keep their --db exception. The detail of
// a pin file that cannot be edited follows the same rule (T-1 of the
// gate 7 recheck).
func TestPinAuthoringTreatsAForeignRootAsUnknownWithADBFlag(t *testing.T) {
	// open shows the linked pin 1 under another root with --db. block
	// is the reason why the pin file cannot be edited, or "".
	open := func(block string) *harness {
		f := authorFake(nil)
		in := pinsInspection()
		in.Index.Root = "/work/original"
		in.Doc.FileBlock = block
		f.pins = in
		f.dbPath = "/elsewhere/index.db"
		h := pinsOpen(t, f, 200, 50)

		pinsCursorTo(h, 1)

		return h
	}

	// assertUnknown opens the form and checks that no row is locked.
	assertUnknown := func(h *harness, where string) {
		h.press("i")
		require.NotNil(t, authorOf(h).form, where)

		d := authorOf(h).form.draft
		assert.True(t, d.unknown, where)
		assert.Empty(t, d.ruleLock, "%s: an unknown relationship locks no row", where)
		assert.Empty(t, d.scopeLock, where)
	}

	h := open("")
	assert.NotContains(t, authorUnavailable(h), "-", "a prune with --db stays offered")

	detail := pinsDetail(t, h)
	assert.Contains(t, detail, pinDraftsStill)
	assert.NotContains(t, detail, "i edits the note of this pin only")
	assertUnknown(h, "an editable pin file")

	// The block turns retarget and prune off. The relationship stays
	// unknown, so d stays on, and i edits more than the note.
	h = open("the pin file is a symbolic link")
	assert.Contains(t, authorUnavailable(h), "-")
	assert.NotContains(t, authorUnavailable(h), "d", "a removal with an unknown relationship stays offered")

	detail = pinsDetail(t, h)
	assert.Contains(t, detail, "Retarget and prune are off: the pin file cannot be edited")
	assert.Contains(t, detail, pinBlockedDrafts)
	assert.NotContains(t, detail, "i still drafts a note edit")
	assertUnknown(h, "a blocked pin file")
}

// TestPinCommitReloadsDoctorAndEvidence covers L19 of the gate 7 review.
// Doctor and the evidence view read lessons.yaml, so a pin write reloads
// them, as a lessons save of Settings does. A refusal reloads them too,
// because a refusal means that the state changed.
func TestPinCommitReloadsDoctorAndEvidence(t *testing.T) {
	for _, outcome := range []maintain.WriteOutcome{maintain.Written, maintain.NotWritten} {
		f := authorFake(nil)
		fresh := &doctor.Report{}
		f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
			f.doctor = fresh

			return maintain.AuthorResult{Outcome: outcome}, nil
		}

		h := pinsOpen(t, f, 120, 40)
		authorFill(h)
		h.press("s")

		learning := f.readCount("Learning")

		h.press("y")
		assert.Same(t, fresh, h.m.st.doctor.value, "outcome %v", outcome)
		assert.Greater(t, f.readCount("Learning"), learning, "outcome %v", outcome)
	}
}
