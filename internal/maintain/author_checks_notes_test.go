package maintain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// checksNotesLessons holds one pin with a mode and a check, and a second
// definition that a check edit can bind. The same entry is linked in
// the linked cases and unlinked in the others.
const checksNotesLessons = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
  frozen-too:
    kind: forbid-path-change/1
pin:
  - rule: checked
    region: api
    note: Recorded note.
    mode: check
    check: frozen
`

// TestPlanAuthorChecksEditsGetTheirOwnReviewNotes covers the review
// notes of a lesson-check edit. A mode-only or check-only edit changes
// no spelling and no note, so its review must not claim a spelling row
// in Stats or an 'edited' mark in Pins. A note edit keeps its notes. An
// edit that moves the pin version says so, because a context then
// receives the pin again. A check ID with the same definition keeps the
// version, so it stays quiet.
func TestPlanAuthorChecksEditsGetTheirOwnReviewNotes(t *testing.T) {
	mode := func(ref pinfile.EntryRef) AuthorRequest {
		return AuthorRequest{Op: AuthorEdit, Ref: ref, Mode: lessoncheck.ModeEnforce, ModeSet: true}
	}
	check := func(ref pinfile.EntryRef) AuthorRequest {
		return AuthorRequest{Op: AuthorEdit, Ref: ref, Check: "frozen-too", CheckSet: true}
	}
	both := func(ref pinfile.EntryRef) AuthorRequest {
		req := mode(ref)
		req.Check, req.CheckSet = "frozen-too", true

		return req
	}
	// A draft can set a field to its current value. Only the changed
	// fields decide the kind of the edit.
	modeWithSameNote := func(ref pinfile.EntryRef) AuthorRequest {
		req := mode(ref)
		req.Note, req.NoteSet = "Recorded note.", true

		return req
	}

	cases := []struct {
		name string
		req  func(ref pinfile.EntryRef) AuthorRequest
		keys string
		// version says whether the edit moves the pin version.
		version bool
	}{
		{name: "mode only", req: mode, keys: "mode", version: true},
		{name: "check only", req: check, keys: "check"},
		{name: "mode and check", req: both, keys: "mode and check", version: true},
		{name: "mode with the same note", req: modeWithSameNote, keys: "mode", version: true},
	}

	for _, link := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s linked=%v", tc.name, link), func(t *testing.T) {
				f := newFixture(t, nil)
				f.write(checksNotesLessons, 0o644)

				if link {
					f.propose(model.Proposal{
						Signature: "s1", Rule: "checked", Region: "api", Note: "Recorded note.",
						Status: model.ProposalApplied,
					})
				}

				doc, err := pinfile.Read(f.root)
				require.NoError(t, err)

				p := f.authorPlan(f.deps(), tc.req(doc.Entries[0].Ref()))

				require.True(t, p.Confirmable, "blocked: %v issues: %v", p.Blockers, p.Issues)
				assert.False(t, p.IdentityChanged)
				assert.Equal(t, p.Old.Note, p.New.Note)

				want := []string{fmt.Sprintf(textChecksKept, tc.keys)}
				if tc.version {
					want = append(want, textVersionChanged)
				}

				if link {
					want = append(want, fmt.Sprintf(textLinkedChecks, p.Proposal.ID))
				}

				want = append(want, textLedgerEffect, noteRedacted)
				assert.Equal(t, want, p.Notes)

				for _, n := range p.Notes {
					assert.NotContains(t, n, "separate rows", "no spelling changes")
					assert.NotContains(t, n, "shows the difference", "no note differs")
				}
			})
		}
	}

	t.Run("a note edit of a linked pin keeps its notes", func(t *testing.T) {
		f := newFixture(t, nil)
		f.write(checksNotesLessons, 0o644)
		f.propose(model.Proposal{
			Signature: "s1", Rule: "checked", Region: "api", Note: "Recorded note.", Status: model.ProposalApplied,
		})

		doc, err := pinfile.Read(f.root)
		require.NoError(t, err)

		p := f.authorPlan(f.deps(), editNote(doc.Entries[0].Ref(), "A corrected note."))

		require.True(t, p.Confirmable, "blocked: %v issues: %v", p.Blockers, p.Issues)
		assert.Contains(t, p.Notes, textIdentityChanged)
		assert.Contains(t, p.Notes, fmt.Sprintf(textLinkedKeeps, p.Proposal.ID))
		assert.NotContains(t, p.Notes, fmt.Sprintf(textChecksKept, "mode"))
		assert.NotContains(t, p.Notes, textVersionChanged, "the identity text already names the repeat")
	})
}

// TestPlanAuthorLinkedBlockerNamesNoRetargetKey holds the layer rule. The
// plan has no retarget state, so the blocker of a linked pin cannot name w.
// A retarget can read n/a for that pin. Pins gives the route that fits.
func TestPlanAuthorLinkedBlockerNamesNoRetargetKey(t *testing.T) {
	f := newFixture(t, nil)
	f.write(checksNotesLessons, 0o644)
	f.propose(model.Proposal{
		Signature: "s1", Rule: "checked", Region: "api", Note: "Recorded note.", Status: model.ProposalApplied,
	})

	doc, err := pinfile.Read(f.root)
	require.NoError(t, err)

	p := f.authorPlan(f.deps(), AuthorRequest{
		Op: AuthorEdit, Ref: doc.Entries[0].Ref(), Scope: ScopeRegions, Regions: []string{"db"}, ScopeSet: true,
	})

	require.False(t, p.Confirmable)

	lines := strings.Join(blockerLines(p), "\n")
	assert.Contains(t, lines, "a change of its regions needs the scope preview")
	assert.NotContains(t, lines, "only its note")
	assert.NotContains(t, lines, "Retarget")
}
