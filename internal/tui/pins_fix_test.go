package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The regression tests of the Phase C review of Lessons › Pins. Each one
// holds a finding of the review: a key without an eligible action, a
// copied workspace, citations, trigger words, notes and outcomes.

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
	cur, ok := p.current(p.rows())
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
