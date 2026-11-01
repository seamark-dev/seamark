package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The words that a hand edit changes with the state of the recorded root.
const (
	handAbsolute = "/work/original/.seamark/lessons.yaml"
	handReplace  = "use another --db, then "
	handBuild    = "build the index of this checkout from Overview (press 1, then b), then "
)

// handWords holds the exact text of one surface in each state of the
// recorded root, in the order of handStates.
type handWords [4]string

// handStates are the four states of a hand edit. Each edit sets the facts
// that the service reports for that state.
var handStates = []struct {
	name string
	edit func(f *fakeServices)
}{
	{"the recorded root is the workspace", func(*fakeServices) {}},
	{"the database owns a reachable root", pinsAnotherDB},
	{"no console of the root can take this", func(f *fakeServices) {
		pinsAnotherDB(f)

		f.database = &workspace.DatabaseFacts{
			Kind: store.DatabaseOther, ForeignRoot: "/work/original", RootRoute: "use another --db",
		}
	}},
	{"a copied checkout before its build", pinsCopiedCheckout},
}

// forEachHandState runs check once per state, with the words of that state.
// edit changes the fake before the first read.
func forEachHandState(t *testing.T, want handWords, edit func(f *fakeServices), check func(t *testing.T, h *harness, want string)) {
	t.Helper()

	for i, s := range handStates {
		t.Run(s.name, func(t *testing.T) {
			f := authorFake(nil)
			f.pins = pinsInspection()

			if edit != nil {
				edit(f)
			}

			s.edit(f)

			check(t, pinsOpen(t, f, 120, 40), want[i])
		})
	}
}

// TestPinHandRouteFollowsTheRecordedRoot covers the route of editGate and
// of unlinkedNext, also as the sentence of a toast.
func TestPinHandRouteFollowsTheRecordedRoot(t *testing.T) {
	want := handWords{
		"edit the pin by hand in .seamark/lessons.yaml",
		"edit the pin by hand in " + handAbsolute,
		handReplace + "edit the pin by hand in .seamark/lessons.yaml",
		handBuild + "edit the pin by hand in .seamark/lessons.yaml",
	}

	t.Run("unlinkedNext", func(t *testing.T) {
		forEachHandState(t, want, nil, func(t *testing.T, h *harness, want string) {
			pinsCursorTo(h, 9)

			p := pinsOf(h)
			assert.Equal(t, want, p.retargetNext(p.rows()[8].pin))

			h.press("w")
			assert.True(t, strings.HasSuffix(h.m.toast.text, pinRouteSentence(want)), "toast: %s", h.m.toast.text)
		})
	})

	t.Run("editGate", func(t *testing.T) {
		unsafe := func(f *fakeServices) { f.pins.Pins[1].Entry.Block = "a multi-line rule value" }

		forEachHandState(t, want, unsafe, func(t *testing.T, h *harness, want string) {
			p := pinsOf(h)
			why, next := p.editGate(p.rows()[1].pin)

			assert.Equal(t, "the entry cannot be edited safely (a multi-line rule value)", why)
			assert.Equal(t, want, next)
		})
	})
}

// TestPinFormLockFollowsTheRecordedRoot covers the locks of the form. A
// lock of the stored text stays such a lock in every state, so a reload
// still removes it from a field with typed text.
func TestPinFormLockFollowsTheRecordedRoot(t *testing.T) {
	const reason = "this text holds characters the editor cannot show; "

	want := handWords{
		reason + "edit .seamark/lessons.yaml by hand",
		reason + "edit " + handAbsolute + " by hand",
		reason + handReplace + "edit .seamark/lessons.yaml by hand",
		reason + handBuild + "edit .seamark/lessons.yaml by hand",
	}

	hostile := func(f *fakeServices) {
		f.pins.Pins[4].Entry.Note = "A note with a \x1b[2J control."
		f.pins.Doc.Entries[4] = f.pins.Pins[4].Entry
	}

	forEachHandState(t, want, hostile, func(t *testing.T, h *harness, want string) {
		pinsCursorTo(h, 5)
		h.press("i")

		form := authorOf(h).form
		require.NotNil(t, form)

		d := form.draft
		assert.Equal(t, want, d.noteLock)
		assert.True(t, pinStoredLock(d.noteLock))
		assert.Equal(t, d.base.Note, d.noteValue(), "a locked note keeps its stored text")

		h.press("down", "enter")
		assert.Contains(t, h.m.toast.text, "Read-only: "+want+".")
	})
}

// TestPinStoredLockHoldsInEveryState keeps a reload correct. It removes a
// lock of the stored text and keeps a lock of the link, also when a link
// lock ends with a hand route.
func TestPinStoredLockHoldsInEveryState(t *testing.T) {
	forEachHandState(t, handWords{}, nil, func(t *testing.T, h *harness, _ string) {
		st := pinsOf(h).st
		hand := pinStoredHand(st)

		assert.True(t, pinStoredLock(pinWithHand(pinTextLock, hand)))
		assert.True(t, pinStoredLock(pinWithHand(pinLengthLock(4), hand)))

		link := "linked to proposal #4; the pin file cannot be edited (x); " + pinRouteByHand(st)
		assert.False(t, pinStoredLock(link))
		assert.False(t, strings.HasSuffix(link, " by hand"), "a link lock never ends in the stored-text words")
		assert.False(t, pinStoredLock(pinRouteRetarget))
	})

	assert.Empty(t, pinWithHand("", pinsHandLock), "a row without a lock stays without one")
}

// TestPinFollowDropsTheStoredLockInEveryState walks a reload of a form whose
// field has typed text: the lock of the old stored text goes away.
func TestPinFollowDropsTheStoredLockInEveryState(t *testing.T) {
	for _, s := range handStates {
		t.Run(s.name, func(t *testing.T) {
			f := authorFake(nil)
			f.pins = pinsInspection()
			s.edit(f)

			h := pinsOpen(t, f, 120, 40)
			p := pinsOf(h)
			pin := p.rows()[4].pin
			require.NotNil(t, pin)

			d := newEditDraft(pin, false, p.lockRoutes(pin))
			d.rule.Set("an edited rule")

			stored := *pin
			stored.Entry.Rule = strings.Repeat("r", 300)

			assert.Empty(t, d.follow(&stored, false, rebindChanged, p.lockRoutes(pin)))
			assert.Empty(t, d.ruleLock)
			assert.Equal(t, "an edited rule", d.ruleValue())
		})
	}
}

// TestPinBlockedNoteFollowsTheRecordedRoot covers a blocked note and the
// text of an action that refuses the row. Only an unsafe entry reaches
// this note in a copied checkout, so the row carries one in every state.
func TestPinBlockedNoteFollowsTheRecordedRoot(t *testing.T) {
	const lead = "Blocked: this entry cannot be edited safely (a multi-line rule value). "

	want := handWords{
		lead + "Edit it by hand in .seamark/lessons.yaml.",
		lead + "Edit it by hand in " + handAbsolute + ".",
		lead + "Use another --db, then edit it by hand in .seamark/lessons.yaml.",
		lead + "Build the index of this checkout from Overview (press 1, then b), then edit it by hand in .seamark/lessons.yaml.",
	}

	unsafe := func(f *fakeServices) { f.pins.Pins[4].Entry.Block = "a multi-line rule value" }

	forEachHandState(t, want, unsafe, func(t *testing.T, h *harness, want string) {
		pinsCursorTo(h, 5)
		assert.Contains(t, pinsDetail(t, h), want)

	})
}

// TestPinAuthorBlockFollowsTheRecordedRoot covers the refusal of the form
// key on an unsafe entry.
func TestPinAuthorBlockFollowsTheRecordedRoot(t *testing.T) {
	const lead = "the entry cannot be edited safely (a multi-line rule value); "

	want := handWords{
		lead + "edit it by hand in .seamark/lessons.yaml",
		lead + "edit it by hand in " + handAbsolute,
		lead + handReplace + "edit it by hand in .seamark/lessons.yaml",
		lead + handBuild + "edit it by hand in .seamark/lessons.yaml",
	}

	unsafe := func(f *fakeServices) { f.pins.Pins[4].Entry.Block = "a multi-line rule value" }

	forEachHandState(t, want, unsafe, func(t *testing.T, h *harness, want string) {
		p := pinsOf(h)
		in := p.inspection()

		assert.Equal(t, want, pinAuthorBlock(p.st, in, p.rows()[4].pin, maintain.AuthorEdit, p.authorKnown(in)))
	})
}

// TestPinDuplicateNoteFollowsTheRecordedRoot covers the note of duplicate
// entries. The copied checkout shows foreignLines for this row instead.
func TestPinDuplicateNoteFollowsTheRecordedRoot(t *testing.T) {
	const tail = " by hand. Maintenance and extraction stay off until one entry remains."

	const verb = "remove or change the duplicate entries in "

	want := handWords{
		"Blocked: " + verb + ".seamark/lessons.yaml" + tail,
		"Blocked: " + verb + handAbsolute + tail,
		"Blocked: " + handReplace + verb + ".seamark/lessons.yaml" + tail,
	}

	for i, s := range handStates[:3] {
		t.Run(s.name, func(t *testing.T) {
			f := pinsFake()
			s.edit(f)

			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, 7)
			assert.Contains(t, pinsDetail(t, h), want[i])
		})
	}
}

// TestPinMismatchTextFollowsTheRecordedRoot covers the text of an applied
// proposal whose pin is gone.
func TestPinMismatchTextFollowsTheRecordedRoot(t *testing.T) {
	const tail = " by hand, then press r."

	const verb = "To restore the pin, "

	const add = "add the rule, the note and the regions above to "

	want := handWords{
		verb + add + ".seamark/lessons.yaml" + tail,
		verb + add + handAbsolute + tail,
		verb + handReplace + add + ".seamark/lessons.yaml" + tail,
		verb + handBuild + add + ".seamark/lessons.yaml" + tail,
	}

	forEachHandState(t, want, nil, func(t *testing.T, h *harness, want string) {
		h.press("f")
		assert.Contains(t, pinsDetail(t, h), "MISMATCH")
		assert.Contains(t, pinsDetail(t, h), want)
	})
}

// TestPinAmbiguousRowFollowsTheRecordedRoot covers the row whose region
// fields disagree. Resolve is the route, not a restore.
func TestPinAmbiguousRowFollowsTheRecordedRoot(t *testing.T) {
	const tail = " or in the ledger, then press r."

	want := handWords{
		"This row is read-only here. Resolve it by hand in .seamark/lessons.yaml" + tail,
		"This row is read-only here. Resolve it by hand in " + handAbsolute + tail,
		"This row is read-only here. Use another --db, then resolve it by hand in .seamark/lessons.yaml" + tail,
		"This row is read-only here. Build the index of this checkout from Overview (press 1, then b), then " +
			"resolve it by hand in .seamark/lessons.yaml" + tail,
	}

	forEachHandState(t, want, pinsDisagreeingRow, func(t *testing.T, h *harness, want string) {
		h.press("f")
		pinsCursorTo(h, 2)

		assert.Contains(t, pinsDetail(t, h), "AMBIGUOUS")
		assert.Contains(t, pinsDetail(t, h), want)
	})
}

// handState returns the console state of a first read in one state.
func handState(t *testing.T, edit func(f *fakeServices)) *state {
	t.Helper()

	f := pinsFake()
	edit(f)

	return pinsOf(pinsOpen(t, f, 120, 40)).st
}

// TestPinTriggerRouteFollowsTheRecordedRoot covers the trigger text, which
// names the hand edit only while w offers no retarget.
func TestPinTriggerRouteFollowsTheRecordedRoot(t *testing.T) {
	const lead = "trigger internal/store/rows.go is confirmed by co-change (3 shared commits), but it sets no " +
		"region: the path is not in the working tree; "

	want := handWords{
		lead + "edit the regions in .seamark/lessons.yaml by hand",
		lead + "edit the regions in " + handAbsolute + " by hand",
		lead + handReplace + "edit the regions in .seamark/lessons.yaml by hand",
		lead + handBuild + "edit the regions in .seamark/lessons.yaml by hand",
	}

	fact := distill.TriggerFact{Path: "internal/store/rows.go", Together: 3}

	blocked := func(f *fakeServices) {
		f.pins.Pins[3].Readings.Trigger.Blocked = []distill.TriggerFact{fact}
		f.pins.Pins[3].Link.Applied[0].TriggerPaths = []string{fact.Path}
	}

	forEachHandState(t, want, blocked, func(t *testing.T, h *harness, want string) {
		pinsCursorTo(h, 4)
		require.NotContains(t, pinsKeys(h), "w")

		assert.Contains(t, pinsDetail(t, h), want)
	})

	t.Run("no route", func(t *testing.T) {
		assert.NotContains(t, pinTriggerFact(fact, ""), "by hand")
	})
}

// TestPinFileBlockNoteFollowsTheRecordedRoot covers the note of a retarget
// or a replace preview. The first sentence names the file that the step
// leaves true.
func TestPinFileBlockNoteFollowsTheRecordedRoot(t *testing.T) {
	want := handWords{
		"The console cannot change .seamark/lessons.yaml: the file is read-only. Edit the file by hand.",
		"The console cannot change " + handAbsolute + ": the file is read-only. Edit the file by hand.",
		"The console cannot change .seamark/lessons.yaml: the file is read-only. Use another --db, then edit the file by hand.",
		"The console cannot change .seamark/lessons.yaml: the file is read-only. Build the index of this checkout " +
			"from Overview (press 1, then b), then edit the file by hand.",
	}

	for i, s := range handStates {
		t.Run(s.name, func(t *testing.T) {
			assert.Equal(t, want[i], pinFileBlockNote(handState(t, s.edit), "the file is read-only"))
		})
	}
}

// TestPinHandPlaceSanitizesTheRoot covers an untrusted root, which reaches
// a terminal as a part of the words.
func TestPinHandPlaceSanitizesTheRoot(t *testing.T) {
	st := handState(t, pinsAnotherDB)
	st.pins.value.Database.ForeignRoot = "/work/\x1b[2Jevil\nroot"

	text := ansi.Strip(pinRouteByHand(st))

	assert.Equal(t, text, pinRouteByHand(st), "no escape sequence reaches the terminal")
	assert.NotContains(t, pinRouteByHand(st), "\x1b")
	assert.NotContains(t, pinRouteByHand(st), "\n")
	assert.True(t, strings.HasSuffix(pinRouteByHand(st), "/.seamark/lessons.yaml"))
}

// TestPinHandPlaceWithoutARead keeps the relative name while no read has
// finished, because no answer of the service names another root.
func TestPinHandPlaceWithoutARead(t *testing.T) {
	step, file := pinHandPlace(&state{})

	assert.Empty(t, step)
	assert.Equal(t, ".seamark/lessons.yaml", file)
}
