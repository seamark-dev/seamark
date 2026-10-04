package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/status"
)

// The regression tests of the Phase C review of the retarget and prune
// preview, over the fixture of pins_modal_fixture_test.go.

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
