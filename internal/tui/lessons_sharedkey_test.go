package tui

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// lesRows is a read-only ledger over fixed pending rows. It lets a test
// run the real preview checks of internal/proposals behind the fake
// services, so the console shows the exact refusal text of the engine.
type lesRows []domain.Proposal

func (r lesRows) ProposalsByIDs(ids []int64) ([]domain.Proposal, error) {
	var out []domain.Proposal

	for _, p := range r {
		if slices.Contains(ids, p.ID) {
			out = append(out, p)
		}
	}

	return out, nil
}

func (r lesRows) SetProposalStatus([]int64, string) (int, error) {
	return 0, errors.New("the test ledger is read-only")
}

// lesEnginePreview returns a fake preview function that runs the real
// apply and record previews over the pending rows of l. Their selection
// checks run before any read of the pin file, so the refusals under test
// need no workspace on disk.
func lesEnginePreview(t *testing.T, l *proposals.Ledger) func(proposals.Action, []int64) (*proposals.Preview, error) {
	t.Helper()

	root := t.TempDir()

	return func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		switch a {
		case proposals.ActionReconcile:
			return proposals.PreviewReconcile(lesRows(l.Pending), root, ids)
		case proposals.ActionDismiss:
			return proposals.PreviewDismiss(lesRows(l.Pending), ids)
		default:
			return proposals.PreviewApply(lesRows(l.Pending), root, ids)
		}
	}
}

// TestLessonsApplyRefusesASharedKey covers D16 in the console. p1 and p3
// have one rule and one region set. One pin for both reads ambiguous in
// Pins, and two pins read as duplicates, so the shared apply refuses the
// selection. The preview names both proposals and the next step, and
// neither y nor c writes anything.
func TestLessonsApplyRefusesASharedKey(t *testing.T) {
	l := lesLedger()
	l.Pending[2].Rule = l.Pending[0].Rule
	l.Pending[2].Regions = l.Pending[0].Regions

	f := newFake()
	f.ledger = l
	f.previewDecision = lesEnginePreview(t, l)

	h := lesOpen(t, f, 120, 40)
	h.press("space", "down", "down", "space", "a")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preview failed")

	p := lesProps(h)
	require.NotNil(t, p.dec)
	require.ErrorIs(t, p.dec.err, proposals.ErrSharedKey)

	body := lesModalText(t, h)
	assert.Contains(t, body, "The apply preview could not be built: p1 and p3 share rule and regions; "+
		"apply one and dismiss the other")
	assert.Contains(t, body, "Pins blocks a pin that two applied proposals record, so apply only one of these "+
		"proposals. Select one and press a, then dismiss the others with d.")
	assert.Contains(t, body, "Nothing was changed.")
	assert.Contains(t, body, "esc back to the list")
	assert.NotContains(t, body, "y apply")

	h.press("y", "c")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "y and c write nothing")
	assert.Equal(t, []int64{1, 3}, p.selectedIDs(), "the selection stays for the next step")
}

// TestLessonsRecordRefusesASharedKey covers D16 through c. Pins reads p1
// and p3 as pending matches of one entry, so c works for both marks. A
// record of both makes two applied proposals record that entry, and Pins
// then blocks it. So the record refuses the selection, as apply does,
// and its hint names c.
func TestLessonsRecordRefusesASharedKey(t *testing.T) {
	l := lesLedger()
	l.Pending[2].Rule = l.Pending[0].Rule
	l.Pending[2].Regions = l.Pending[0].Regions

	f := newFake()
	f.ledger = l
	f.pins = pinsInspection()
	f.pins.Links.ByProposal[1] = pinfile.LinkPendingMatch
	f.pins.Links.ByProposal[3] = pinfile.LinkPendingMatch
	f.previewDecision = lesEnginePreview(t, l)

	h := lesOpen(t, f, 120, 40)
	h.press("space", "down", "down", "space")

	p := lesProps(h)
	require.True(t, p.hasPinned([]int64{1, 3}), "Pins knows a pin for both marks")

	h.press("c")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preview failed")

	require.NotNil(t, p.dec)
	require.ErrorIs(t, p.dec.err, proposals.ErrSharedKey)

	body := lesModalText(t, h)
	assert.Contains(t, body, "The record-as-applied preview could not be built: p1 and p3 share rule and "+
		"regions; apply one and dismiss the other")
	assert.Contains(t, body, "Pins blocks a pin that two applied proposals record, so record only one of these "+
		"proposals as applied. Select one and press c, then dismiss the others with d.")
	assert.Contains(t, body, "Nothing was changed.")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:reconcile"}, f.called(), "y writes nothing")
	assert.Equal(t, []int64{1, 3}, p.selectedIDs(), "the selection stays for the next step")
}

// TestPropSharedKeyHintFollowsTheActionAndTheGroups checks that the hint
// names the key of the refused action. With several groups it asks for
// one proposal of each group, as the engine error does.
func TestPropSharedKeyHintFollowsTheActionAndTheGroups(t *testing.T) {
	one := &proposals.SharedKeyError{Groups: [][]int64{{1, 3}}}
	two := &proposals.SharedKeyError{Groups: [][]int64{{1, 4}, {2, 3}}}

	tests := []struct {
		name   string
		action proposals.Action
		err    error
		want   string
	}{
		{
			name: "apply, one group", action: proposals.ActionApply, err: one,
			want: "Pins blocks a pin that two applied proposals record, so apply only one of these proposals. " +
				"Select one and press a, then dismiss the others with d.",
		},
		{
			name: "apply, two groups", action: proposals.ActionApply, err: two,
			want: "Pins blocks a pin that two applied proposals record, so apply only one proposal of each " +
				"group. Select one of each group and press a, then dismiss the others with d.",
		},
		{
			name: "record, two groups", action: proposals.ActionReconcile, err: two,
			want: "Pins blocks a pin that two applied proposals record, so record only one proposal of each " +
				"group as applied. Select one of each group and press c, then dismiss the others with d.",
		},
		{
			name: "the bare sentinel", action: proposals.ActionApply, err: proposals.ErrSharedKey,
			want: "Pins blocks a pin that two applied proposals record, so apply only one of these proposals. " +
				"Select one and press a, then dismiss the others with d.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := propSharedKeyHint(&propDecision{action: tt.action, err: tt.err})
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPinsPendingMatchOfSeveralProposalsAsksForOneRecord covers the
// route of a pin that two pending proposals match. The record refuses
// both at once, so the detail asks for one record and dismissals. One
// pending match keeps the route of one record.
func TestPinsPendingMatchOfSeveralProposalsAsksForOneRecord(t *testing.T) {
	const entry = 5 // pin 6, prefer-table-tests, matches pending p3

	f := pinsFake()
	link := &f.pins.Pins[entry].Link
	require.Equal(t, pinfile.LinkPendingMatch, link.Link)

	p3 := link.Pending[0]
	p40 := p3
	p40.ID = 40
	link.Pending = append(link.Pending, p40)
	f.pins.Links.Entries[entry] = *link

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, entry+1)
	require.Equal(t, "prefer-table-tests", cursorPin(t, h).Entry.Rule)

	text := pinsDetail(t, h)
	assert.Contains(t, text, "Record one of p3, p40 as applied first, and dismiss the others: press p, "+
		"select one and press c, then select the others and press d. The pin stays read-only here until one "+
		"applied proposal links it.")
	assert.NotContains(t, text, "Record p3, p40 as applied first")
}
