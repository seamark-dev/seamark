package tui

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// The tests of the Proposals help (user decision of 2026-10-04: "help
// only"). The help lists every Proposals action and marks with n/a the
// actions that the cursor row, the selection, or the filters cannot
// use. esc is listed only with a run filter. The footer keeps the keys
// of Phase 2. A press of an n/a key keeps its behavior of Phase 2, but
// c on a pending row without a known pin shows the reason and opens no
// preview (a second user decision of 2026-10-04).

// propHelpFake is the ledger of lesLedger with one superseded proposal,
// so f reaches a superseded row too.
func propHelpFake() *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Superseded = []domain.Proposal{{
		ID: 8, Rule: "retire-old-flag", Region: "internal/cli", Note: "Remove the --legacy flag from new code.",
		Members: []int64{104}, Agent: "claude/v2", Status: domain.ProposalSuperseded,
	}}

	return f
}

// propHelpState is one state of the Proposals list: the services and the
// steps that reach the state after Lessons opens.
type propHelpState struct {
	name  string
	fake  func() *fakeServices
	drive func(h *harness)
}

// propHelpStates returns the states that the help and the footer tests
// share: the cursor on a pending, an applied, and a superseded row, an
// empty pending list, and the run filter of a distillation.
func propHelpStates() []propHelpState {
	empty := func() *fakeServices {
		f := propHelpFake()
		f.ledger.Pending = nil

		return f
	}

	return []propHelpState{
		{name: "pending", fake: propHelpFake, drive: func(*harness) {}},
		{name: "applied", fake: propHelpFake, drive: func(h *harness) { h.press("f") }},
		{name: "superseded", fake: propHelpFake, drive: func(h *harness) { h.press("f", "f", "f") }},
		{name: "empty", fake: empty, drive: func(*harness) {}},
		{name: "run", fake: propHelpFake, drive: func(h *harness) {
			h.run(lesProps(h).showRun([]int64{2, 3}, "this run", time.Now()))
		}},
	}
}

// propHelpOpen opens Lessons › Proposals in state st at w by hgt.
func propHelpOpen(t *testing.T, st propHelpState, w, hgt int) (*harness, *fakeServices) {
	t.Helper()

	f := st.fake()
	h := lesOpen(t, f, w, hgt)
	h.view()
	st.drive(h)
	h.view()

	return h, f
}

// propHelpGroup returns the LESSONS group of the help: the keys that
// work, then the unavailable keys after the n/a label row.
func propHelpGroup(h *harness) (on, off string) {
	on, off = helpParts(h)
	_, on, found := strings.Cut(on, "LESSONS ")
	require.True(h.t, found, "the help has a LESSONS group")

	return strings.TrimSpace(on), strings.TrimSpace(off)
}

// footerClock matches the snapshot clock at the end of a footer. The
// clock shows the load time, so the footer tests mask it.
var footerClock = regexp.MustCompile(`\d\d:\d\d:\d\d`)

func TestProposalsHelpListsEveryActionAndMarksTheUnavailable(t *testing.T) {
	const switches = "p proposals m pins s stats e evidence"

	// {pane} is the pane key: tab beside the list, enter below 100
	// columns.
	want := map[string][2]string{
		"pending": {
			"space select a apply d dismiss {pane} f state / region t extract triggers o open PR " + switches,
			"x n/a clear selection c n/a record applied",
		},
		"applied": {
			"{pane} f state / region t extract triggers " + switches,
			"space n/a select a n/a apply d n/a dismiss o n/a open PR x n/a clear selection c n/a record applied",
		},
		"superseded": {
			"{pane} f state / region " + switches,
			"space n/a select a n/a apply d n/a dismiss t n/a extract triggers o n/a open PR " +
				"x n/a clear selection c n/a record applied",
		},
		"empty": {
			"f state / region " + switches,
			"space n/a select a n/a apply d n/a dismiss enter n/a detail t n/a extract triggers " +
				"o n/a open PR x n/a clear selection c n/a record applied",
		},
		"run": {
			"space select a apply d dismiss {pane} esc all proposals f state / region t extract triggers " + switches,
			"o n/a open PR x n/a clear selection c n/a record applied",
		},
	}

	for _, st := range propHelpStates() {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h, _ := propHelpOpen(t, st, size[0], size[1])
			h.press("?")
			assertFrame(t, h.view(), size[0], size[1])

			pane := "enter detail"
			if size[0] >= wideWidth {
				pane = "tab detail"
			}

			on, off := propHelpGroup(h)
			assert.Equal(t, strings.ReplaceAll(want[st.name][0], "{pane}", pane), on, "%s at %dx%d", st.name, size[0], size[1])
			assert.Equal(t, want[st.name][1], off, "%s at %dx%d", st.name, size[0], size[1])
		}
	}
}

func TestProposalsHelpFollowsTheSelectionThePinsAndTheGate(t *testing.T) {
	f := propHelpFake()
	h := lesOpen(t, f, 120, 40)
	h.view()

	// A mark makes x work. The help names what x clears.
	h.press("space")

	on, off := propHelpGroup(h)
	assert.Contains(t, on, "o open PR x clear selection p proposals")
	assert.Equal(t, "c n/a record applied", off)

	// A known existing pin makes c work.
	lesProps(h).pinned[1] = true

	on, off = propHelpGroup(h)
	assert.Contains(t, on, "x clear selection c record applied p proposals")
	assert.Empty(t, off, "every key works, and esc has no run filter to end")

	// The busy slot holds back only the commit in the decision modal. The
	// list keys open read-only previews, so the help does not change.
	require.True(t, h.m.st.begin("Building the index"))

	busyOn, busyOff := propHelpGroup(h)
	assert.Equal(t, on, busyOn)
	assert.Equal(t, off, busyOff)

	h.send(mutationDoneMsg{})

	// With the pin-write gate off, a only shows the YAML, and the help
	// says so.
	f.ledger.Write = false

	on, _ = propHelpGroup(h)
	assert.Contains(t, on, "a YAML")
}

func TestProposalsFooterKeepsThePhase2Keys(t *testing.T) {
	// The footers of Phase 2, with the clock masked. The help change must
	// not change one byte of them.
	want := map[string]map[int]string{
		"pending": {
			120: " space select   a apply   d dismiss   tab detail   f state   / region  …         r refresh   ? help   q quit   hh:mm:ss ",
			80:  " space select   a apply   d dismiss  …   r refresh   ? help   q quit   hh:mm:ss ",
			60:  " space select  …     r refresh   ? help   q quit   hh:mm:ss ",
		},
		"applied": {
			120: " tab detail   f state   / region   t extract triggers   m pins   s stats  …      r refresh   ? help   q quit   hh:mm:ss ",
			80:  " enter detail   f state   / region  …    r refresh   ? help   q quit   hh:mm:ss ",
			60:  " enter detail  …     r refresh   ? help   q quit   hh:mm:ss ",
		},
		"superseded": {
			120: " tab detail   f state   / region   m pins   s stats   e evidence                 r refresh   ? help   q quit   hh:mm:ss ",
			80:  " enter detail   f state   / region  …    r refresh   ? help   q quit   hh:mm:ss ",
			60:  " enter detail  …     r refresh   ? help   q quit   hh:mm:ss ",
		},
		"empty": {
			120: " f state   / region   m pins   s stats   e evidence                              r refresh   ? help   q quit   hh:mm:ss ",
			80:  " f state   / region   m pins  …          r refresh   ? help   q quit   hh:mm:ss ",
			60:  " f state  …          r refresh   ? help   q quit   hh:mm:ss ",
		},
		"run": {
			120: " space select   a apply   d dismiss   tab detail   esc all proposals  …          r refresh   ? help   q quit   hh:mm:ss ",
			80:  " space select   a apply   d dismiss  …   r refresh   ? help   q quit   hh:mm:ss ",
			60:  " space select  …     r refresh   ? help   q quit   hh:mm:ss ",
		},
	}

	for _, st := range propHelpStates() {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h, _ := propHelpOpen(t, st, size[0], size[1])

			footer := footerClock.ReplaceAllString(footerLine(h.view()), "hh:mm:ss")
			assert.Equal(t, want[st.name][size[0]], footer, "%s at %dx%d", st.name, size[0], size[1])
			assert.NotContains(t, footer, unavailableWord, "%s at %dx%d", st.name, size[0], size[1])
		}
	}
}

// propFooterStates returns the states of propHelpStates and three more:
// a marked row, a marked row with a known pin, and a link that is not a
// PR. In these states the footer lists x, c, or "o open link". A
// 120-column footer cuts its hints before x, o, and c. So only a wider
// footer shows these labels.
func propFooterStates() []propHelpState {
	issueLink := func() *fakeServices {
		f := propHelpFake()
		f.ledger.Evidence[101] = domain.Finding{
			ID: 101, Path: "internal/api/handler.go", Reviewer: "coderabbit", Source: "review",
			URL: "https://example.com/issues/7",
		}

		return f
	}

	return append(propHelpStates(),
		propHelpState{name: "marked", fake: propHelpFake, drive: func(h *harness) { h.press("space") }},
		propHelpState{name: "marked pin", fake: propHelpFake, drive: func(h *harness) {
			h.press("space")
			lesProps(h).pinned[1] = true
		}},
		propHelpState{name: "issue link", fake: issueLink, drive: func(*harness) {}},
	)
}

func TestProposalsWideFooterKeepsThePhase2LabelOfEveryKey(t *testing.T) {
	// At 240 columns no footer of these states cuts a hint, so the test
	// sees the labels of x, o, and c. At 200 columns the marked pin state
	// cuts e. The help names x "clear selection", but the footer keeps
	// the Phase 2 label "x clear" (user decision of 2026-10-04). keys and
	// helpKeys give their x label to the same listKeys. So one changed
	// argument changes the footer, and this test must see it.
	const right = "r refresh   ? help   q quit   hh:mm:ss "

	want := map[string]string{
		"pending":    " space select   a apply   d dismiss   tab detail   f state   / region   t extract triggers   o open PR   m pins   s stats   e evidence",
		"applied":    " tab detail   f state   / region   t extract triggers   m pins   s stats   e evidence",
		"superseded": " tab detail   f state   / region   m pins   s stats   e evidence",
		"empty":      " f state   / region   m pins   s stats   e evidence",
		"run":        " space select   a apply   d dismiss   tab detail   esc all proposals   f state   / region   t extract triggers   m pins   s stats   e evidence",
		"marked":     " space select   a apply   d dismiss   tab detail   f state   / region   t extract triggers   o open PR   x clear   m pins   s stats   e evidence",
		"marked pin": " space select   a apply   d dismiss   tab detail   f state   / region   t extract triggers   o open PR   x clear   c record applied   m pins   s stats   e evidence",
		"issue link": " space select   a apply   d dismiss   tab detail   f state   / region   t extract triggers   o open link   m pins   s stats   e evidence",
	}

	for _, st := range propFooterStates() {
		h, _ := propHelpOpen(t, st, 240, 50)

		footer := footerClock.ReplaceAllString(footerLine(h.view()), "hh:mm:ss")
		left, found := strings.CutSuffix(footer, right)
		require.True(t, found, "%s: the footer ends with the global keys: %q", st.name, footer)

		assert.Equal(t, want[st.name], strings.TrimRight(left, " "), st.name)
		assert.NotContains(t, footer, unavailableWord, st.name)
	}
}

func TestProposalsUnavailableKeyKeepsItsPhase2Behavior(t *testing.T) {
	const readOnly = "Applied, dismissed and superseded proposals are read-only. Press f to show pending ones."

	states := map[string]propHelpState{}
	for _, st := range propHelpStates() {
		states[st.name] = st
	}

	cases := []struct {
		state, key, toast string
	}{
		{"applied", "a", readOnly},
		{"applied", "d", readOnly},
		{"applied", "c", readOnly},
		{"applied", "o", "No cited finding of p4 has a link."},
		{"applied", "space", ""},
		{"applied", "x", ""},
		{"superseded", "t", "Dismissed and superseded proposals have no trigger question to ask."},
		{"superseded", "a", readOnly},
		{"empty", "a", ""},
		{"empty", "d", ""},
		{"empty", "enter", ""},
		{"pending", "x", ""},
		{"pending", "esc", ""},
	}

	for _, c := range cases {
		t.Run(c.state+" "+c.key, func(t *testing.T) {
			h, f := propHelpOpen(t, states[c.state], 120, 40)
			before := len(lesProps(h).selected)

			h.press(c.key)

			assert.Empty(t, f.called(), "an n/a key plans and writes nothing")
			assert.Equal(t, c.toast, h.m.toast.text)
			assert.Nil(t, lesProps(h).dec, "an n/a key opens no preview")
			assert.False(t, hookLessons(h).capturing())
			assert.Len(t, lesProps(h).selected, before, "an n/a key changes no mark")
		})
	}
}

// propNoPin is the toast of c on a cursor row without a known pin.
const propNoPin = "No record applied: no pin in .seamark/lessons.yaml carries p1 yet. Press r after you edit the file."

// propNoMarkedPin returns the toast of c on marks without a known pin
// for each of them.
func propNoMarkedPin(counts string) string {
	return "No record applied: " + counts + " no pin in .seamark/lessons.yaml yet. Press r after you edit the file."
}

// assertRecordRefused checks that c showed the toast and nothing else:
// no service call, no modal, no change of the marks.
func assertRecordRefused(t *testing.T, h *harness, f *fakeServices, toast string, marks []int64) {
	t.Helper()

	assert.Equal(t, toast, h.m.toast.text)
	assert.Equal(t, ToneNeutral, h.m.toast.tone)
	assert.Empty(t, f.called(), "c without a known pin plans and writes nothing")
	assert.Nil(t, lesProps(h).dec, "c without a known pin opens no preview")
	_, modal := h.m.modalBlock(h.m.st.width, h.m.st.height-1)
	assert.False(t, modal, "c without a known pin opens no modal")
	assert.False(t, hookLessons(h).capturing())
	assert.Equal(t, marks, lesProps(h).selectedIDs(), "c changes no mark")
}

// assertRecordPreviewed checks that c opened the ledger-only preview of
// ids, as in Phase 2.
func assertRecordPreviewed(t *testing.T, h *harness, f *fakeServices, ids []int64) {
	t.Helper()

	assert.Equal(t, []string{"PreviewDecision:reconcile"}, f.called())
	require.NotNil(t, lesProps(h).dec, "c opens the record preview")
	assert.Equal(t, proposals.ActionReconcile, lesProps(h).dec.action)
	assert.Equal(t, ids, lesProps(h).dec.ids)
	assert.Empty(t, h.m.toast.text)
}

func TestProposalsRecordAppliedWithoutAKnownPinShowsTheReason(t *testing.T) {
	// The help marks c n/a on a pending row whose pin the console does
	// not know. So its press shows the reason in one line and opens no
	// preview (user decision of 2026-10-04). The notice row cuts the end
	// of the line on a narrow terminal, so the reason comes first.
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		f := propHelpFake()
		h := lesOpen(t, f, size[0], size[1])
		h.view()

		h.press("c")

		assertRecordRefused(t, h, f, propNoPin, []int64{})

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "No record applied: no pin in", "%dx%d", size[0], size[1])
	}

	// A known pin keeps the press of Phase 2: the ledger-only preview.
	f := propHelpFake()
	h := lesOpen(t, f, 120, 40)
	lesProps(h).pinned[1] = true

	h.press("c")

	assertRecordPreviewed(t, h, f, []int64{1})

	// The Pins inspection also knows a pin: the pending match of a row.
	f = propHelpFake()
	f.pins = pinsInspection()
	f.pins.Links.ByProposal[1] = pinfile.LinkPendingMatch
	h = lesOpen(t, f, 120, 40)
	require.Empty(t, lesProps(h).pinned, "no preview has run")

	h.press("c")

	assertRecordPreviewed(t, h, f, []int64{1})
}

func TestProposalsRecordAppliedOnMarksNeedsAPinForEveryMark(t *testing.T) {
	cases := []struct {
		name   string
		marks  []int64
		pinned []int64
		toast  string
	}{
		{"mixed", []int64{1, 2}, []int64{1}, propNoMarkedPin("1 of 2 marked proposals has")},
		{"none known", []int64{1, 2}, nil, propNoMarkedPin("2 of 2 marked proposals have")},
		{"one mark, the cursor row known", []int64{2}, []int64{1}, propNoMarkedPin("1 of 1 marked proposal has")},
		{"all known", []int64{1, 2}, []int64{1, 2}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := propHelpFake()
			h := lesOpen(t, f, 120, 40)
			p := lesProps(h)

			// The marks are the targets, so the cursor row on p1 does not
			// count when it is not marked.
			for _, id := range c.marks {
				p.selected[id] = true
			}

			for _, id := range c.pinned {
				p.pinned[id] = true
			}

			// The footer and the help decide c by the same rule as the
			// press.
			_, off := propHelpGroup(h)
			assert.Equal(t, c.toast == "", lesFixBinding(t, h, "c").Enabled())
			assert.Equal(t, c.toast != "", strings.Contains(off, "c n/a record applied"))

			h.press("c")

			if c.toast == "" {
				assertRecordPreviewed(t, h, f, c.marks)

				return
			}

			assertRecordRefused(t, h, f, c.toast, c.marks)
		})
	}
}

// propPinsUnread is the toast of c when the last Pins inspection could
// not read the pin file or the ledger.
const propPinsUnread = "No record applied: the pin file or the ledger could not be read; m (Pins) names the error. " +
	"Press r after you fix it."

// propPinsNotRead is the toast of c before the first Pins inspection.
const propPinsNotRead = "No record applied: the console has not read .seamark/lessons.yaml yet. Press r to read it."

func TestProposalsRecordAppliedWithoutAPinReadDoesNotClaimAnAbsentPin(t *testing.T) {
	// Without a good read of the pin file and the ledger, the console
	// does not know whether a pin exists. So c must not say that the file
	// has no pin. Its toast names the route to the read error instead.
	// The help and the footer still mark c n/a (review fix of 2026-10-04).
	unknownLinks := func() *maintain.Inspection {
		in := pinsInspection()
		in.LinkErr = pinfile.ErrUnreadable
		in.Links.ByProposal[1] = pinfile.LinkUnknown

		return in
	}
	noFile := func() *maintain.Inspection {
		in := pinsMalformed()
		in.Doc, in.DocErr = nil, errors.New("read .seamark/lessons.yaml: not a regular file")
		in.Pins = nil

		return in
	}

	cases := []struct {
		name string
		pins func() *maintain.Inspection
	}{
		{"unknown links", unknownLinks},
		{"malformed file", pinsMalformed},
		{"no file read", noFile},
		{"no ledger", pinsNoLedger},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The read gap is one fact for every target, so the toast of
			// marks counts nothing.
			for _, marks := range [][]int64{{}, {1, 2}} {
				f := propHelpFake()
				f.pins = c.pins()
				h := lesOpen(t, f, 120, 40)

				for _, id := range marks {
					lesProps(h).selected[id] = true
				}

				_, off := propHelpGroup(h)
				assert.Contains(t, off, "c n/a record applied", "marks %v", marks)
				assert.False(t, lesFixBinding(t, h, "c").Enabled(), "marks %v", marks)

				h.press("c")

				assertRecordRefused(t, h, f, propPinsUnread, marks)
			}
		})
	}

	// m opens Pins, whose state line names the error of the toast.
	f := propHelpFake()
	f.pins = unknownLinks()
	h := lesOpen(t, f, 120, 40)

	h.press("c", "m")

	assert.Contains(t, h.view(), "Relationships are unknown: .seamark/lessons.yaml could not be read.")

	// Before the first Pins inspection, the console has read nothing.
	f = propHelpFake()
	h = lesOpen(t, f, 120, 40)
	lesProps(h).st.pins = section[*maintain.Inspection]{loading: true}

	h.press("c")

	assertRecordRefused(t, h, f, propPinsNotRead, []int64{})

	// A preview that found the pin still makes it known, so c keeps the
	// preview of Phase 2. Its read names the real error of the file.
	f = propHelpFake()
	f.pins = pinsMalformed()
	h = lesOpen(t, f, 120, 40)
	lesProps(h).pinned[1] = true

	h.press("c")

	assertRecordPreviewed(t, h, f, []int64{1})
}

func TestProposalsNewPinsReadDropsAPinHintThatItContradicts(t *testing.T) {
	// The inspection of each case reads p1 in its own way. readNotPresent
	// is the newer read after a hand edit removed the pin.
	readAs := func(link pinfile.Link) func() *maintain.Inspection {
		return func() *maintain.Inspection {
			in := pinsInspection()
			in.Links.ByProposal[1] = link

			return in
		}
	}
	notListed := func() *maintain.Inspection {
		in := pinsInspection()
		delete(in.Links.ByProposal, 1)

		return in
	}

	cases := []struct {
		name string
		pins func() *maintain.Inspection
		keep bool
	}{
		// The newer read does not find the pin, so the hint goes.
		{"not present", readAs(pinfile.LinkNotPresent), false},
		// The newer read finds the pin, as after a partial apply.
		{"pending match", readAs(pinfile.LinkPendingMatch), true},
		// A read without links cannot contradict the hint.
		{"malformed file", pinsMalformed, true},
		{"no ledger", pinsNoLedger, true},
		// The read did not list p1, so it says nothing about its pin.
		{"not listed", notListed, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := propHelpFake()
			f.pins = readAs(pinfile.LinkNotPresent)()
			f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
				pv := lesPreview(a, ids, true)
				pv.AlreadyPinned = []int64{1}

				return pv, nil
			}

			h := lesOpen(t, f, 120, 40)
			require.False(t, lesFixBinding(t, h, "c").Enabled(), "the first read finds no pin")

			// The apply preview is newer than the first read, and it finds
			// the pin. So c works.
			h.press("a", "esc")
			require.True(t, lesFixBinding(t, h, "c").Enabled(), "the preview found the pin")

			// r reads the pin file again.
			f.pins = c.pins()

			h.press("r")

			assert.Equal(t, c.keep, lesFixBinding(t, h, "c").Enabled())
			assert.Equal(t, c.keep, lesProps(h).pinned[1])

			_, off := propHelpGroup(h)
			assert.Equal(t, !c.keep, strings.Contains(off, "c n/a record applied"))

			h.press("c")

			if c.keep {
				assert.Equal(t, []string{"PreviewDecision:apply", "PreviewDecision:reconcile"}, f.called())
				require.NotNil(t, lesProps(h).dec, "c opens the record preview")

				return
			}

			assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "c asks for no refused preview")
			assert.Equal(t, propNoPin, h.m.toast.text)
			assert.Nil(t, lesProps(h).dec)
		})
	}
}

func TestProposalsHelpListsEscOnlyWithARunFilter(t *testing.T) {
	// Without a run filter, esc has nothing to end. So the help leaves
	// the row out, and it does not mark it n/a (user decision of
	// 2026-10-04). The footer did not list it before, and it does not
	// now.
	var run propHelpState

	for _, st := range propHelpStates() {
		if st.name == "run" {
			run = st

			continue
		}

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h, _ := propHelpOpen(t, st, size[0], size[1])

			on, off := propHelpGroup(h)
			assert.NotContains(t, on+" "+off, "esc", "%s at %dx%d", st.name, size[0], size[1])
			assert.NotContains(t, on+" "+off, "all proposals", "%s at %dx%d", st.name, size[0], size[1])
		}
	}

	// With a run filter, esc ends it.
	h, _ := propHelpOpen(t, run, 120, 40)
	require.NotNil(t, lesProps(h).run)

	on, off := propHelpGroup(h)
	assert.Contains(t, on, "tab detail esc all proposals f state")
	assert.NotContains(t, off, "esc")

	// The detail pane takes esc first, so the help marks the row n/a
	// until the list has the focus again.
	h.press("tab")

	on, off = propHelpGroup(h)
	assert.NotContains(t, on, "all proposals")
	assert.Contains(t, off, "esc n/a all proposals")

	// esc gives the focus back, and a second esc ends the run filter. The
	// row then leaves the help.
	h.press("esc", "esc")
	require.Nil(t, lesProps(h).run)

	on, off = propHelpGroup(h)
	assert.NotContains(t, on+" "+off, "esc")
}
