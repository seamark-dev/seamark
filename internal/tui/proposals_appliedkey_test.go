package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// lesAppliedKeyLedger gives pending p1 the rule and region of applied
// p4, as a later distillation of new evidence can do.
func lesAppliedKeyLedger() *proposals.Ledger {
	l := lesLedger()
	l.Pending[0].Rule = l.Applied[0].Rule
	l.Pending[0].Regions = []string{l.Applied[0].Region}

	return l
}

// lesAppliedKeyHint is the hint of the refusal of p1 next to p4.
const lesAppliedKeyHint = "Pins blocks a pin that two applied proposals record, so p1 cannot become applied " +
	"while p4 is applied. Select p1 and press d to dismiss it."

// lesAppliedKeyNote is the reason line of a and c for p1 next to p4: the
// text of the preview refusal and the key of the dismiss.
const lesAppliedKeyNote = "p4 is already applied with this rule and regions; dismiss p1 with d."

// lesAppliedKeyQuestion is the trigger question line of p1 next to p4.
// Pins reads p1 as a pending match, but c is n/a for p1, so the line
// names the dismiss and no key.
const lesAppliedKeyQuestion = "a pin already carries it, and p4 is already applied with this rule and regions; " +
	"dismiss p1"

// lesAppliedKeyFake returns services whose ledger snapshot gives p1 the
// pin key of applied p4. Pins reads p1 as a pending match of the entry
// that p4 records, so only the applied key rule keeps c from working.
func lesAppliedKeyFake(t *testing.T) *fakeServices {
	t.Helper()

	l := lesAppliedKeyLedger()

	f := newFake()
	f.ledger = l
	f.pins = pinsInspection()
	f.pins.Links.ByProposal[1] = pinfile.LinkPendingMatch
	f.previewDecision = lesEnginePreview(t, l)

	return f
}

// TestLessonsApplyAndRecordAreUnavailableForTheKeyOfAnAppliedProposal
// covers the console rule of docs/tui.md: a key that cannot act reads n/a,
// and its press opens nothing. The ledger snapshot shows that applied p4
// records the pin key of p1, and the preview refuses such a row. So a and
// c leave the footer, the help marks them n/a, the detail says why, and a
// press shows the reason and calls no service. d stays the route.
func TestLessonsApplyAndRecordAreUnavailableForTheKeyOfAnAppliedProposal(t *testing.T) {
	f := lesAppliedKeyFake(t)

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)
	require.True(t, p.hasPinned([]int64{1}), "Pins knows a pin for p1")

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	// The footer lists only the keys that work, so it drops a and c.
	footer := footerLine(frame)
	assert.True(t, strings.HasPrefix(footer, " space select   d dismiss   tab detail"), "footer %q", footer)
	assert.False(t, lesFixBinding(t, h, "a").Enabled())
	assert.False(t, lesFixBinding(t, h, "c").Enabled())

	// The help lists both actions as n/a.
	on, off := propHelpGroup(h)
	assert.NotContains(t, on, "a apply")
	assert.NotContains(t, on, "record applied")
	assert.Contains(t, off, "a n/a apply")
	assert.Contains(t, off, "c n/a record applied")

	// The detail gives the reason, and no line of it offers c for the pin.
	detail := lesFixDetail(h)
	assert.Contains(t, detail, "a and c do not work for p1: Pins blocks a pin that two applied proposals "+
		"record. "+lesAppliedKeyNote)
	assert.Contains(t, detail, "Trigger question: "+lesAppliedKeyQuestion+".")
	assert.NotContains(t, detail, "Press c to record it as applied")
	assert.NotContains(t, detail, "record it as applied first")

	// A press shows the reason in one line and opens nothing.
	h.press("a")
	assert.Empty(t, f.called(), "a asks for no preview")
	assert.Nil(t, p.dec)
	assert.Equal(t, "No apply: "+lesAppliedKeyNote, h.m.toast.text)

	h.press("c")
	assert.Empty(t, f.called(), "c asks for no preview")
	assert.Nil(t, p.dec)
	assert.Equal(t, "No record applied: "+lesAppliedKeyNote, h.m.toast.text)

	h.press("t")
	assert.Empty(t, f.called(), "t opens no review")
	assert.Equal(t, "Nothing to extract for p1: "+lesAppliedKeyQuestion+".", h.m.toast.text)

	// The marks follow the same rule: p1 among them keeps a and c off.
	h.press("space", "down", "space")
	require.Equal(t, []int64{1, 2}, p.selectedIDs())
	assert.False(t, lesFixBinding(t, h, "a").Enabled())
	assert.False(t, lesFixBinding(t, h, "c").Enabled())

	on, off = propHelpGroup(h)
	assert.NotContains(t, on, "a apply")
	assert.Contains(t, off, "a n/a apply")
	assert.Contains(t, off, "c n/a record applied")

	// d dismisses every marked row, also p2, which apply can take. So the
	// reason asks for an unmark first. The cursor is on p2, and its detail
	// gives the reason of the n/a too.
	const markedNote = "p4 is already applied with this rule and regions; dismiss p1 with d after you unmark the " +
		"other proposals."

	assert.Contains(t, lesFixDetail(h), "a and c do not work for the selection: Pins blocks a pin that two applied "+
		"proposals record. "+markedNote)

	h.press("a")
	assert.Empty(t, f.called(), "a asks for no preview")
	assert.Nil(t, p.dec)
	assert.Equal(t, "No apply: "+markedNote, h.m.toast.text)

	h.press("c")
	assert.Empty(t, f.called(), "c asks for no preview")
	assert.Nil(t, p.dec)
	assert.Equal(t, "No record applied: "+markedNote, h.m.toast.text)

	// Without p1, a works again. The cursor is on p1, which a and c no
	// longer target, but the detail of p1 still offers no c.
	h.press("up", "space")
	require.Equal(t, []int64{2}, p.selectedIDs())
	assert.True(t, lesFixBinding(t, h, "a").Enabled())

	detail = lesFixDetail(h)
	assert.NotContains(t, detail, "a and c do not work")
	assert.NotContains(t, detail, "Press c to record it as applied")

	// With p1 as the only mark, d dismisses p1 only, so the step is d.
	h.press("space", "down", "space", "up")
	require.Equal(t, []int64{1}, p.selectedIDs())
	assert.Contains(t, lesFixDetail(h), "a and c do not work for the selection: Pins blocks a pin that two "+
		"applied proposals record. "+lesAppliedKeyNote)

	h.press("a")
	assert.Equal(t, "No apply: "+lesAppliedKeyNote, h.m.toast.text)

	// d is the route for p1, and it still previews.
	h.press("x", "d")
	assert.Equal(t, []string{"PreviewDecision:dismiss"}, f.called())
	require.NotNil(t, p.dec)
	assert.Equal(t, []int64{1}, p.dec.ids)
}

// lesAppliedKeyStale returns the ledger of lesAppliedKeyLedger as an old
// snapshot shows it: another writer applied p4 after the snapshot. So the
// console offers a and c for p1, and only the preview reads the refusal.
func lesAppliedKeyStale() *proposals.Ledger {
	l := lesAppliedKeyLedger()
	l.Applied = nil

	return l
}

// TestLessonsApplyRefusesTheKeyOfAnAppliedProposal covers the apply of a
// pending proposal whose pin key applied p4 records, when the snapshot is
// old. The preview reads the ledger again, so it stays the authority: a
// second applied row of that key makes Pins block the pin, so the preview
// refuses p1 and asks for a dismiss. Neither y nor c writes anything.
func TestLessonsApplyRefusesTheKeyOfAnAppliedProposal(t *testing.T) {
	f := newFake()
	f.ledger = lesAppliedKeyStale()
	f.previewDecision = lesEnginePreview(t, lesAppliedKeyLedger())

	h := lesOpen(t, f, 120, 40)
	require.True(t, lesFixBinding(t, h, "a").Enabled(), "the old snapshot shows no applied p4")

	h.press("space", "a")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preview failed")

	p := lesProps(h)
	require.NotNil(t, p.dec)
	require.ErrorIs(t, p.dec.err, proposals.ErrAppliedKey)

	body := lesModalText(t, h)
	assert.Contains(t, body, "The apply preview could not be built: p4 is already applied with this rule and "+
		"regions; dismiss p1")
	assert.Contains(t, body, lesAppliedKeyHint)
	assert.Contains(t, body, "Nothing was changed.")
	assert.Contains(t, body, "esc back to the list")
	assert.NotContains(t, body, "y apply")

	h.press("y", "c")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "y and c write nothing")
	assert.Equal(t, []int64{1}, p.selectedIDs(), "the selection stays for the dismiss")
}

// TestLessonsRecordRefusesTheKeyOfAnAppliedProposal covers c when the
// snapshot is old. Pins reads p1 as a pending match of the entry that p4
// records, so c works for it. Before, the record made p1 a second applied
// row of that entry, and Pins then blocked it. Now the record preview
// refuses p1 as apply does.
func TestLessonsRecordRefusesTheKeyOfAnAppliedProposal(t *testing.T) {
	f := newFake()
	f.ledger = lesAppliedKeyStale()
	f.pins = pinsInspection()
	f.pins.Links.ByProposal[1] = pinfile.LinkPendingMatch
	f.previewDecision = lesEnginePreview(t, lesAppliedKeyLedger())

	h := lesOpen(t, f, 120, 40)
	h.press("space")

	p := lesProps(h)
	require.True(t, p.hasPinned([]int64{1}), "Pins knows a pin for p1")
	require.True(t, lesFixBinding(t, h, "c").Enabled(), "the old snapshot shows no applied p4")

	h.press("c")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preview failed")

	require.NotNil(t, p.dec)
	require.ErrorIs(t, p.dec.err, proposals.ErrAppliedKey)

	body := lesModalText(t, h)
	assert.Contains(t, body, "The record-as-applied preview could not be built: p4 is already applied with "+
		"this rule and regions; dismiss p1")
	assert.Contains(t, body, lesAppliedKeyHint)
	assert.Contains(t, body, "Nothing was changed.")

	h.press("y")
	assert.Equal(t, []string{"PreviewDecision:reconcile"}, f.called(), "y writes nothing")
}

// TestPropAppliedKeyHintNamesTheIDs checks that the hint names the IDs
// of the engine error. The step is a dismiss for apply and for the
// record, so the action does not change the hint.
func TestPropAppliedKeyHintNamesTheIDs(t *testing.T) {
	tests := []struct {
		name   string
		action proposals.Action
		err    error
		want   string
	}{
		{
			name: "apply, one row", action: proposals.ActionApply,
			err:  &proposals.AppliedKeyError{Groups: []proposals.AppliedKeyGroup{{Applied: []int64{4}, Pending: []int64{1}}}},
			want: lesAppliedKeyHint,
		},
		{
			name: "record, one row", action: proposals.ActionReconcile,
			err:  &proposals.AppliedKeyError{Groups: []proposals.AppliedKeyGroup{{Applied: []int64{4}, Pending: []int64{1}}}},
			want: lesAppliedKeyHint,
		},
		{
			name: "two keys", action: proposals.ActionApply,
			err: &proposals.AppliedKeyError{Groups: []proposals.AppliedKeyGroup{
				{Applied: []int64{6}, Pending: []int64{1}},
				{Applied: []int64{4, 5}, Pending: []int64{2, 3}},
			}},
			want: "Pins blocks a pin that two applied proposals record, so p1, p2, p3 cannot become applied " +
				"while p4, p5, p6 are applied. Select p1, p2, p3 and press d to dismiss them.",
		},
		{
			name: "the bare sentinel", action: proposals.ActionApply, err: proposals.ErrAppliedKey,
			want: "Pins blocks a pin that two applied proposals record, so these proposals cannot become " +
				"applied. Select them and press d to dismiss them.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := propAppliedKeyHint(&propDecision{previewFlow: previewFlow[proposals.Preview]{err: tt.err}, action: tt.action})
			assert.Equal(t, tt.want, got)
		})
	}
}

// lesTwoAppliedKeysLedger adds applied p6 with the rule and region of
// pending p2 to lesAppliedKeyLedger. So the marks p1 and p2 meet two
// refused pin keys.
func lesTwoAppliedKeysLedger() *proposals.Ledger {
	l := lesAppliedKeyLedger()
	l.Applied = append(l.Applied, domain.Proposal{
		ID: 6, Rule: l.Pending[1].Rule, Region: l.Pending[1].Regions[0], Note: "Close handles.",
		Members: []int64{106}, Agent: "claude/v2", Status: domain.ProposalApplied, CreatedAt: l.Applied[0].CreatedAt,
	})

	return l
}

// TestLessonsAppliedKeyNoticeCountsSeveralKeys covers the notice row of a
// refusal with two keys. The notice row cuts a line at the terminal
// width, and the full text names each refused ID twice. So the line
// counts the refused rows and keeps its end. The detail names every ID.
func TestLessonsAppliedKeyNoticeCountsSeveralKeys(t *testing.T) {
	f := newFake()
	f.ledger = lesTwoAppliedKeysLedger()

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)

	// The refusal names every mark, so d is the step.
	h.press("space", "down", "space")
	require.Equal(t, []int64{1, 2}, p.selectedIDs())
	assert.False(t, lesFixBinding(t, h, "a").Enabled())
	assert.False(t, lesFixBinding(t, h, "c").Enabled())

	h.press("a")
	assert.Contains(t, noticeRow(h), "No apply: 2 marked proposals have the rule and regions of applied "+
		"proposals; dismiss them with d.")
	assert.Contains(t, lesFixDetail(h), "a and c do not work for the selection: Pins blocks a pin that two applied "+
		"proposals record. p4 is already applied with the rule and regions of p1; p6 is already applied with the "+
		"rule and regions of p2; dismiss p1 and p2 with d.")

	// p3 has no applied key, and d would dismiss it too. The line has no
	// room for the IDs of the step, so it points to the detail.
	h.press("down", "space")
	require.Equal(t, []int64{1, 2, 3}, p.selectedIDs())

	h.press("a")
	assert.Contains(t, noticeRow(h), "No apply: 2 of 3 marked proposals have the rule and regions of applied "+
		"proposals; the detail names them.")

	h.press("c")
	assert.Contains(t, noticeRow(h), "No record applied: 2 of 3 marked proposals have the rule and regions of "+
		"applied proposals; the detail names them.")
	assert.Contains(t, lesFixDetail(h), "p6 is already applied with the rule and regions of p2; dismiss p1 and p2 "+
		"with d after you unmark the other proposals.")
	assert.Empty(t, f.called(), "a and c ask for no preview")
	assert.Nil(t, p.dec)
}

// TestAppliedKeyTextNamesTheStepOfTheTargets checks the reason texts of
// an applied key refusal. d dismisses every target, so the step changes
// when the targets also hold rows that the refusal does not name.
func TestAppliedKeyTextNamesTheStepOfTheTargets(t *testing.T) {
	one := &proposals.AppliedKeyError{Groups: []proposals.AppliedKeyGroup{{Applied: []int64{4}, Pending: []int64{1}}}}
	two := &proposals.AppliedKeyError{Groups: []proposals.AppliedKeyGroup{
		{Applied: []int64{4}, Pending: []int64{1}},
		{Applied: []int64{6}, Pending: []int64{2}},
	}}

	const both = "p4 is already applied with the rule and regions of p1; p6 is already applied with the rule " +
		"and regions of p2; dismiss p1 and p2"

	tests := []struct {
		name   string
		taken  *proposals.AppliedKeyError
		ids    []int64
		text   string
		notice string
	}{
		{
			name: "one key, every target", taken: one, ids: []int64{1},
			text: lesAppliedKeyNote, notice: lesAppliedKeyNote,
		},
		{
			name: "one key, other marks", taken: one, ids: []int64{1, 3},
			text:   "p4 is already applied with this rule and regions; dismiss p1 with d after you unmark the other proposals.",
			notice: "p4 is already applied with this rule and regions; dismiss p1 with d after you unmark the other proposals.",
		},
		{
			name: "two keys, every target", taken: two, ids: []int64{1, 2},
			text:   both + " with d.",
			notice: "2 marked proposals have the rule and regions of applied proposals; dismiss them with d.",
		},
		{
			name: "two keys, other marks", taken: two, ids: []int64{1, 2, 3},
			text:   both + " with d after you unmark the other proposals.",
			notice: "2 of 3 marked proposals have the rule and regions of applied proposals; the detail names them.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.text, appliedKeyText(tt.taken, tt.ids))
			assert.Equal(t, tt.notice, appliedKeyNotice(tt.taken, tt.ids))
		})
	}
}
