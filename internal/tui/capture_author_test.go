package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// authorIdentityNotes are the review notes of a note edit of the linked
// pin 1 (contracts §5). maintain keeps these texts unexported, so the
// scene copies them; the review shows the notes of the plan as they are.
var authorIdentityNotes = []string{
	"Earlier deliveries stay recorded under the earlier wording and scope. Nothing is reset or moved. " +
		"The new wording is a new delivery identity: a context that already saw the old wording can " +
		"receive it again, also in once-per-context mode.",
	"Proposal #4 measures its outcome by the live wording, so its Outcome reads 'untested' until the new " +
		"wording is delivered.",
	"Proposal #4 keeps its recorded rule, note, and evidence. Pins shows the difference as 'edited'. " +
		"Trigger extraction still asks about the recorded note. Saving does not extract triggers or retarget.",
	"The proposal ledger does not change.",
}

// authorCaptureScenes lists the Pins authoring scenes A0 to A9 of
// contracts §7. Each scene starts on Lessons › Pins. The fake plans with
// authorPlanFor and gives a create the diff of its own pin. An edit or a
// removal scene sets its own plan. So each review shows the pin of its
// draft in GUIDANCE and in DIFF.
func authorCaptureScenes() []phase4Scene {
	// fake returns the services of authorFake over pinsASCIIInspection
	// and a ready index. A removal review and a written result show the
	// Pins list, and its ASCII fixture keeps the ASCII variants ASCII
	// only. Pins 1 and 5 are the same in both fixtures. The plans link
	// pins through the ledger of an index, so the header shows one too.
	fake := func(edit func(*maintain.AuthorPlan)) *fakeServices {
		f := authorFake(func(p *maintain.AuthorPlan) {
			authorCreateDiff(p)

			if edit != nil {
				edit(p)
			}
		})
		f.pins = pinsASCIIInspection()
		f.index = ovReadyIndex(f.root, f.dbPath)

		return f
	}

	plain := func() *fakeServices { return fake(nil) }

	// editNote opens the form of pin n and appends text to its note.
	editNote := func(n int, text string) func(h *harness) {
		return func(h *harness) {
			pinsCursorTo(h, n)
			h.press("i", "down", "enter", "end")
			h.typeText(text)
			h.press("esc")
		}
	}

	// review drafts a new pin and opens its review.
	review := func(h *harness) {
		authorFill(h)
		h.press("s")
	}

	// committed returns services whose commit gives res and err.
	committed := func(res maintain.AuthorResult, err error) func() *fakeServices {
		return func() *fakeServices {
			f := fake(nil)
			f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) { return res, err }

			return f
		}
	}

	// reload stores in as the pin inspection of the open console and
	// reads the pins, as a change on disk does.
	reload := func(in func() *maintain.Inspection) func(h *harness) {
		return func(h *harness) {
			authorReload(h, h.m.st.svc.(*fakeServices), in())
		}
	}

	return []phase4Scene{
		{name: "A0-pin-form-new", want: "New pin", open: phase4OnPins(plain, func(h *harness) { h.press("n") })},
		{name: "A1-pin-form-filled", want: "Draft complete", open: phase4OnPins(plain, func(h *harness) {
			authorFill(h)
			h.press("a")
			h.typeText("internal/store")
			h.press("enter")
		})},
		{name: "A2-pin-form-edit-unlinked", want: "Edit pin 5 at line 21",
			open: phase4OnPins(plain, editNote(5, " Keep it short."))},
		{name: "A3-pin-form-edit-linked", want: "Read-only: linked to proposal #4", open: phase4OnPins(plain,
			func(h *harness) {
				pinsCursorTo(h, 1)
				h.press("i")
			})},
		{name: "A4-pin-review-create", want: "New pin new-rule", open: phase4OnPins(plain, review)},
		// The diff is the last part of the review body. Its last line is
		// the second note line of the draft; GUIDANCE shows that line
		// without the + of the diff.
		{name: "A4b-pin-review-create-diff", want: "DIFF", open: phase4OnPins(plain, func(h *harness) {
			review(h)
			captureScrollTo(h, "down", "+       Second line.")
		})},
		{name: "A5-pin-review-identity-changed", want: authorIdentityNotes[3], open: phase4OnPins(func() *fakeServices {
			return fake(func(p *maintain.AuthorPlan) {
				authorEditLinkedPin1Note(p)
				p.Notes = authorIdentityNotes
			})
		}, func(h *harness) {
			editNote(1, " Name the query.")(h)
			h.press("s")
			captureScrollTo(h, "down", authorIdentityNotes[3])
		})},
		{name: "A6-pin-review-remove", want: "Remove pin", open: phase4OnPins(func() *fakeServices {
			return fake(func(p *maintain.AuthorPlan) {
				authorDiff(p, authorBefore+"  - rule: keep-changelog-current\n    note: "+p.Old.Note+"\n", authorBefore)
			})
		}, func(h *harness) {
			pinsCursorTo(h, 5)
			h.press("d")
		})},
		// The Lessons tab line reads the same gate as the plan.
		{name: "A7-pin-review-gate-off", want: "y is off", open: phase4OnPins(func() *fakeServices {
			f := fake(authorGateOff)
			f.ledger.Write = false

			return f
		}, review)},
		// No index: the header, the Pins list and the plan agree.
		{name: "A7b-pin-review-no-index", want: "y is off", open: phase4OnPins(func() *fakeServices {
			f := authorFake(func(p *maintain.AuthorPlan) {
				authorEditPin1Note(p)
				authorNoLedger(p)
			})
			f.pins = pinsNoLedger()

			return f
		}, func(h *harness) {
			editNote(1, " More.")(h)
			h.press("s")
		})},
		{name: "A8-pin-result-written", want: "Pin written",
			open: phase4OnPins(committed(maintain.AuthorResult{Outcome: maintain.Written}, nil), func(h *harness) {
				review(h)
				h.press("y")
			})},
		{name: "A8b-pin-result-unknown", want: "Result unknown", open: phase4OnPins(
			committed(maintain.AuthorResult{Outcome: maintain.Unknown, Err: errors.New("rename: input/output error")}, nil),
			func(h *harness) {
				review(h)
				h.press("y")
			})},
		// The commit writes the file, and the next read of it fails.
		{name: "A8c-pin-result-refresh-failed", want: "Pin written", open: phase4OnPins(func() *fakeServices {
			f := fake(nil)
			f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
				f.pins = pinsDocErr()

				return maintain.AuthorResult{Outcome: maintain.Written}, nil
			}

			return f
		}, func(h *harness) {
			review(h)
			h.press("y")
			captureScrollTo(h, "down", "r reads again.")
		})},
		// Another writer changes the note of pin 5 under the draft.
		{name: "A9-pin-draft-changed", want: "entry changed", open: phase4OnPins(plain,
			func(h *harness) {
				editNote(5, " Keep it short.")(h)
				reload(authorChanged)(h)
			})},
		// Pin 5 leaves the file under the draft.
		{name: "A9b-pin-draft-orphaned", want: "orphaned draft", open: phase4OnPins(plain,
			func(h *harness) {
				editNote(5, " Keep it short.")(h)
				reload(authorGone)(h)
			})},
	}
}

// TestCaptureAuthoringReviewsShowTheirOwnPin keeps each review scene
// true to its draft. GUIDANCE shows the rules of the plan, and DIFF
// shows its file bytes, so both must name one pin. The test reads the
// plan, because a small frame cuts the diff. It writes nothing.
func TestCaptureAuthoringReviewsShowTheirOwnPin(t *testing.T) {
	ops := map[maintain.AuthorOp]bool{}

	for _, sc := range authorCaptureScenes() {
		h := sc.open(t, false, 120, 40)

		r := authorOf(h).review
		if r == nil || r.preview == nil {
			continue
		}

		p := r.preview
		ops[p.Request.Op] = true

		// holds checks that one side of the file bytes holds the rule and
		// every note line of pin.
		holds := func(side string, data []byte, pin reviews.PinRule) {
			assert.Contains(t, string(data), "rule: "+pin.Rule, "scene %s: the %s bytes hold the rule", sc.name, side)

			for _, line := range strings.Split(pin.Note, "\n") {
				assert.Contains(t, string(data), line, "scene %s: the %s bytes hold the note", sc.name, side)
			}
		}

		if p.Request.Op != maintain.AuthorRemove {
			holds("new", p.After, p.New)
		}

		if p.Request.Op == maintain.AuthorCreate {
			continue
		}

		holds("old", p.Before, p.Old)

		// The old rule is the rule of the pin that the request binds.
		pins := h.m.st.svc.(*fakeServices).pins.Pins
		i := slices.IndexFunc(pins, func(pin maintain.Pin) bool { return pin.Entry.Ref() == p.Request.Ref })
		require.GreaterOrEqual(t, i, 0, "scene %s binds a pin of the list", sc.name)
		assert.Equal(t, pins[i].Entry.Rule, p.Old.Rule, "scene %s: the old rule is the rule of the bound pin", sc.name)
	}

	assert.Len(t, ops, 3, "the scenes review a create, an edit, and a removal")
}
