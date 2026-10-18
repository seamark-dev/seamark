package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the Proposals list: the region and rule columns, the
// superseded glyph, and the pending match. They hold the findings of
// the real-binary trials and the traceability review. Each test drives
// the real model through the harness, so it proves the fix where the
// user sees it.

// proposalsListRow returns the frame line of the Proposals list row that
// starts with the ID and the rule prefix. The detail title shows the
// rule before the ID, so it never matches.
func proposalsListRow(t *testing.T, frame, id, rule string) string {
	t.Helper()

	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, id+"  "+rule) {
			return line
		}
	}

	require.Failf(t, "row not found", "no list row shows %s %q in:\n%s", id, rule, frame)

	return ""
}

// Trial finding: the REGION column cut every region from the right, so
// internal/mail, internal/orders and internal/billing all read
// "internal/…". A rule that filled its column also touched the region.
func TestProposalsRegionKeepsThePathTailAndAGap(t *testing.T) {
	const long = "validate-quantity-before-reserving-stock-in-every-warehouse"

	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Health = nil
	f.ledger.Pending = []domain.Proposal{
		{ID: 2, Rule: "send-mail-once", Regions: []string{"internal/mail"}, Status: domain.ProposalProposed},
		{ID: 6, Rule: long, Regions: []string{"internal/orders"}, Status: domain.ProposalProposed},
		{ID: 7, Rule: "round-billing-totals", Regions: []string{"internal/billing"}, Status: domain.ProposalProposed},
	}

	for _, size := range [][2]int{{120, 40}, {200, 50}, {80, 24}} {
		h := lesOpen(t, f, size[0], size[1])
		frame := h.view()

		assert.NotContains(t, frame, "internal/…", "no region collapses to its first component at %v", size)
		assert.Contains(t, proposalsListRow(t, frame, "p2", "send-mail-once"), "…/mail", "at %v", size)
		assert.Contains(t, proposalsListRow(t, frame, "p7", "round-billing-totals"), "…/billing", "at %v", size)

		// The cut rule ends with the ellipsis, then one blank cell, then
		// the region.
		assert.Regexp(t, `…\s+…/orders`, proposalsListRow(t, frame, "p6", "validate-quantity"), "at %v", size)
		assert.NotContains(t, frame, "……/orders", "the rule never touches the region at %v", size)
	}
}

// Trial finding: a rule of exactly the column width filled the cell, so
// it touched the region ("refresh-token-before-expirypkg/retry").
func TestProposalsRuleOfTheColumnWidthKeepsAGap(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)
	t.Cleanup(func() { assert.Empty(t, f.called()) })

	c := p.columns(p.rows(), ui.BoxInner(62))
	require.Positive(t, c.regW)

	pr := domain.Proposal{ID: 2, Rule: strings.Repeat("r", c.ruleW), Regions: []string{"pkg/retry"}}
	row := ansi.Strip(c.row(h.m.st.theme, pr, false, false, ""))

	assert.Contains(t, row, "r… pkg/retry", "a full rule keeps one blank cell before the region")
	assert.Equal(t, ui.Width(c.header(h.m.st.theme)), ui.Width(row), "the row keeps the column widths")
}

// Trial finding: Proposals showed a superseded row with the filled dot
// of pending and applied rows, while Pins showed the hollow one.
func TestProposalsSupersededUsesTheHollowGlyphOfPins(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Superseded = []domain.Proposal{propSupersededRow()}

	for _, ascii := range []bool{false, true} {
		h := lesOpenTheme(t, f, ascii)
		h.press("f", "f", "f")
		require.Equal(t, propSuperseded, lesProps(h).filter)

		g := h.m.st.theme.Glyphs
		assert.Contains(t, h.view(), g.Hollow+" superseded", "ascii %t", ascii)
		assert.NotContains(t, h.view(), g.Dot+" superseded", "ascii %t", ascii)
	}
}

// lesOpenTheme starts a console with the given glyph set on Lessons.
func lesOpenTheme(t *testing.T, f *fakeServices, ascii bool) *harness {
	t.Helper()

	h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
	h.run(h.m.Init())
	h.resize(120, 40)
	h.press("3")
	require.Equal(t, destLessons, h.m.active)

	return h
}

// Trial finding: the Proposals detail named an existing pin, and the
// footer offered c, only after an apply preview. Pins already knew the
// pending match and sent the user to "press p, select it, then press c".
func TestProposalsShowsAPendingMatchBeforeAnyPreview(t *testing.T) {
	f := pinsFake()
	p3 := f.pins.Pins[5].Link.Pending[0]
	f.ledger.Pending[2] = p3
	f.pins.Links.ByProposal[p3.ID] = pinfile.LinkPendingMatch

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)
	hint := "A pin for p3 already exists in"

	// p1 has no pin, so it shows neither the hint nor c.
	require.Equal(t, int64(1), p.cursorID)
	assert.NotContains(t, propDetailText(h), hint)
	assert.NotContains(t, lesHelpKeys(hookLessons(h)), "c")

	for p.cursorID != p3.ID {
		h.press("down")
	}

	assert.Empty(t, p.pinned, "no preview has run")
	assert.Contains(t, propDetailText(h), hint+" "+".seamark/lessons.yaml. Press c to record it as applied")
	assert.Contains(t, lesHelpKeys(hookLessons(h)), "c", "the footer and the help offer the Pins route")
	assert.Empty(t, f.called(), "the hint comes from the Pins inspection, not from a preview")

	// A new inspection that cannot link the row never invents a pin.
	unknown := pinsInspection()
	unknown.Links.ByProposal[p3.ID] = pinfile.LinkUnknown
	f.pins = unknown
	h.press("r")
	require.Same(t, unknown, h.m.st.pins.value, "r reads the pins again")
	assert.NotContains(t, propDetailText(h), hint)
	assert.NotContains(t, lesHelpKeys(hookLessons(h)), "c")
}
