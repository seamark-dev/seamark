package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// The tests of Lessons for a proposal whose stored region fields
// disagree (pinfile.FieldsAgree). Every reader links such a row by its
// stored fields, but its apply writes a pin with other regions. So the
// console never claims a link, an absence, or a record for it.

func TestPinsTargetOwnerReadsAnAppliedRowByItsStoredFields(t *testing.T) {
	// The fields of p41 disagree: they read internal/api and
	// internal/store, and RenderPins would write a repository-wide pin.
	// The owner check uses the stored fields only, as
	// maintain.appliedKeys and the CLI do.
	in := pinsInspection()
	p41 := pinsRow(41, domain.ProposalApplied, "check-nil-errors", "internal/store",
		[]string{"*", "internal/api"}, "Odd fields.")
	in.Links.NotPresent = append(in.Links.NotPresent, p41)

	pin := &in.Pins[1]

	assert.Equal(t, "the applied proposal p41 already records this rule with the computed regions",
		pinTargetOwner(in, pin, []string{"internal/store", "internal/api"}))
	assert.Empty(t, pinTargetOwner(in, pin, nil), "the rendered repository-wide key belongs to no row")
}

// pinsDisagreeingRow adds the applied p41 to the not present list, as
// pinfile.Classify lists a row whose fields disagree and that no entry
// links. Its apply wrote a repository-wide pin.
func pinsDisagreeingRow(f *fakeServices) {
	p41 := pinsRow(41, domain.ProposalApplied, "odd-fields", "internal/api", []string{"*"}, "Odd fields.")
	f.pins.Links.NotPresent = append(f.pins.Links.NotPresent, p41)
	f.pins.Links.ByProposal[41] = pinfile.LinkAmbiguousFields
}

func TestPinsNotPresentListNeverClaimsTheAbsenceOfAnAmbiguousRow(t *testing.T) {
	f := pinsFake()
	pinsDisagreeingRow(f)

	h := pinsOpen(t, f, 120, 40)
	h.press("f")

	// p7 is a plain missing pin and keeps its words.
	text := pinsDetail(t, h)
	assert.Contains(t, text, "retired-guidance p7 ! not present")
	assert.Contains(t, text, "MISMATCH")

	pinsCursorTo(h, 2)

	text = pinsDetail(t, h)
	assert.Contains(t, text, "odd-fields p41 ! ambiguous row ● applied in the ledger")
	assert.Contains(t, text, "AMBIGUOUS The stored region fields of p41 disagree.")
	assert.Contains(t, text, "Seamark cannot prove that its pin is gone")
	assert.NotContains(t, text, "not present")
	assert.NotContains(t, text, "To restore the pin", "the pin that its apply writes can already exist")

	frame := lesFlat(ansi.Strip(h.view()))
	assert.Contains(t, frame, "ambiguous row", "the list row reads ambiguous too")
}

// lesDisagreeingLedger is lesLedger with stored region fields of p1
// that disagree: its key reads internal/api, and its apply writes a
// repository-wide pin.
func lesDisagreeingLedger() *proposals.Ledger {
	l := lesLedger()
	l.Pending[0].Region = "internal/api"
	l.Pending[0].Regions = []string{"*"}

	return l
}

func TestProposalsRecordAppliedIsOffForARowWhoseRegionFieldsDisagree(t *testing.T) {
	require.False(t, pinfile.FieldsAgree(lesDisagreeingLedger().Pending[0]))

	// An older read of Pins and a pin hint both claim a pin for p1. The
	// record refuses such a row, so c stays off and asks for no preview.
	f := newFake()
	f.ledger = lesDisagreeingLedger()
	f.pins = pinsInspection()
	f.pins.Links.ByProposal[1] = pinfile.LinkPendingMatch

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)
	p.pinned[1] = true

	assert.False(t, p.hasPin(1))
	assert.False(t, lesFixBinding(t, h, "c").Enabled())
	assert.NotContains(t, lesFixDetail(h), "Press c to record it")

	h.press("c")
	assert.Empty(t, f.called(), "c asks for no preview")
	assert.Nil(t, p.dec)
	assert.Equal(t, "No record applied: the stored region fields of p1 disagree, so no pin is proven to be its own.",
		h.m.toast.text)

	// A row whose fields agree keeps its hint.
	p.pinned[2] = true
	assert.True(t, p.hasPin(2))
}

func TestProposalsRecordRefusalNamesTheRegionFields(t *testing.T) {
	// The list read p1 with fields that agree, and a preview found its
	// pin, so c opens the record preview. Another writer changed the
	// stored fields of p1 after that read. The record refuses p1, and the
	// modal says why and what helps.
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = lesEnginePreview(t, lesDisagreeingLedger())

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)
	p.pinned[1] = true

	h.press("c")
	assert.Equal(t, []string{"PreviewDecision:reconcile"}, f.called())
	require.NotNil(t, p.dec)

	m := p.modalContent(p.dec, 200)
	body := lesFlat(ansi.Strip(strings.Join(m.body, "\n")))
	assert.Contains(t, body, "the stored region fields of the selected proposals disagree: p1")
	assert.Contains(t, body, "Pins links these proposals by other regions than the ones that their apply writes, "+
		"so no pin is proven to be theirs. Dismiss them with d, or resolve them by hand.")
	assert.Contains(t, body, "Nothing was changed.")
}

func TestProposalsApplyRefusesARowWhoseRegionFieldsDisagree(t *testing.T) {
	// F1: the apply of p1 writes a repository-wide pin, but Pins links
	// p1 by internal/api. The shared apply refuses p1, so the preview
	// names the reason and the next step, and y writes nothing.
	f := newFake()
	f.ledger = lesDisagreeingLedger()
	f.previewDecision = lesEnginePreview(t, f.ledger)

	h := lesOpen(t, f, 120, 40)
	h.press("a")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preview failed")

	p := lesProps(h)
	require.NotNil(t, p.dec)
	require.ErrorIs(t, p.dec.err, proposals.ErrFieldsDisagree)

	body := lesModalText(t, h)
	assert.Contains(t, body, "The apply preview could not be built: the stored region fields of the selected "+
		"proposals disagree: p1; resolve them by hand or dismiss them")
	assert.Contains(t, body, "Pins links these proposals by other regions than the ones that their apply writes. "+
		"So Pins cannot link the new pin to them. Dismiss them with d, or resolve them by hand.")
	assert.Contains(t, body, "Nothing was changed.")
	assert.NotContains(t, body, "y apply")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "y writes nothing")
}
