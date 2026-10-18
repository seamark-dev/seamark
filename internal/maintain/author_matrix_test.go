package maintain

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// matrixLessons holds one entry per relationship. The rows of
// matrixFixture give each entry its link.
const matrixLessons = `pin:
  - rule: unlinked
    region: web
    note: No proposal.
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)
  - rule: linked
    region: api
    note: Recorded note.
  - rule: pending
    region: db
    note: A pending row has this key.
  - rule: twice
    region: lib
    note: First.
  - rule: twice
    region: lib
    note: Second.
  - rule: two-rows
    region: cli
    note: Two applied rows.
  - rule: fields
    region: '*'
    note: The pin of a row whose region fields disagree.
  - rule: amb
    region: x
    regions: [x, y]
    note: The key of a row whose region fields disagree.
`

// matrixFixture is a workspace with matrixLessons and seven rows:
//
//   - p1 links "linked", and p2 is pending on "pending";
//   - p3 and p4 are both applied on "two-rows";
//   - p5 has disagreeing region fields, and its apply wrote "fields";
//   - p6 has disagreeing region fields, and its key is the key of "amb";
//   - p7 is applied, and no entry holds its pin.
func matrixFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, nil)
	f.write(matrixLessons, 0o644)
	f.propose(
		model.Proposal{Signature: "s1", Rule: "linked", Region: "api", Note: "Recorded note.", Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "pending", Region: "db", Note: "Pending.", Status: model.ProposalProposed},
		model.Proposal{Signature: "s3", Rule: "two-rows", Region: "cli", Note: "One.", Status: model.ProposalApplied},
		model.Proposal{Signature: "s4", Rule: "two-rows", Region: "cli", Note: "Two.", Status: model.ProposalApplied},
		model.Proposal{Signature: "s5", Rule: "fields", Region: "a", Regions: []string{"*", "b"}, Note: "Fields.",
			Status: model.ProposalApplied},
		model.Proposal{Signature: "s6", Rule: "amb", Region: "x", Regions: []string{"y"}, Note: "Amb.",
			Status: model.ProposalApplied},
		model.Proposal{Signature: "s7", Rule: "gone", Region: "srv", Note: "No pin.", Status: model.ProposalApplied},
	)

	return f
}

func TestPlanAuthorRelationshipMatrix(t *testing.T) {
	rename := func(ref pinfile.EntryRef) AuthorRequest {
		return AuthorRequest{Op: AuthorEdit, Ref: ref, Rule: "a-new-rule", RuleSet: true}
	}
	rescope := func(ref pinfile.EntryRef) AuthorRequest {
		return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"elsewhere"}, ScopeSet: true}
	}
	note := func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, "A corrected note.") }
	remove := func(ref pinfile.EntryRef) AuthorRequest { return AuthorRequest{Op: AuthorRemove, Ref: ref} }

	// A draft that started while the relationship was unknown can carry
	// the rule or the scope of a linked pin. Equal values ask for no
	// Retarget.
	noteSameRule := func(ref pinfile.EntryRef) AuthorRequest {
		req := note(ref)
		req.Rule, req.RuleSet = "linked", true

		return req
	}
	noteSameScope := func(ref pinfile.EntryRef) AuthorRequest {
		req := note(ref)
		req.Scope, req.Regions, req.ScopeSet = ScopeRegions, []string{"api"}, true

		return req
	}

	const (
		linkedNote   = "linked to proposal #1; only its note can change here; " + routeRetarget
		ambiguousFix = "; " + reasonLedgerFixes
	)

	cases := []struct {
		name  string
		entry string
		req   func(ref pinfile.EntryRef) AuthorRequest
		link  pinfile.Link
		// extra holds rows that only this case adds to the ledger.
		extra []model.Proposal
		// blocker is the one expected blocker, or empty.
		blocker string
	}{
		{name: "unlinked rename", entry: "unlinked", req: rename, link: pinfile.LinkNoProposal},
		{name: "unlinked note", entry: "unlinked", req: note, link: pinfile.LinkNoProposal},
		{name: "unlinked rescope", entry: "unlinked", req: rescope, link: pinfile.LinkNoProposal},
		{name: "unlinked removal", entry: "unlinked", req: remove, link: pinfile.LinkNoProposal},
		{name: "linked note", entry: "linked", req: note, link: pinfile.LinkLinked},
		{name: "linked note with the same rule", entry: "linked", req: noteSameRule, link: pinfile.LinkLinked},
		{name: "linked note with the same scope", entry: "linked", req: noteSameScope, link: pinfile.LinkLinked},
		{name: "linked rename", entry: "linked", req: rename, link: pinfile.LinkLinked, blocker: linkedNote},
		{name: "linked rescope", entry: "linked", req: rescope, link: pinfile.LinkLinked, blocker: linkedNote},
		{
			name: "linked removal", entry: "linked", req: remove, link: pinfile.LinkLinked,
			blocker: "linked to proposal #1; Remove is only for unlinked pins; " + routePrune,
		},
		{
			name: "pending match", entry: "pending", req: note, link: pinfile.LinkPendingMatch,
			blocker: "matches a pending proposal; record it as applied first; " + routeRecord,
		},
		{
			name: "duplicate entries", entry: "twice", req: note, link: pinfile.LinkAmbiguousFile,
			blocker: "ambiguous: duplicate pins" + ambiguousFix,
		},
		{
			name: "two applied rows", entry: "two-rows", req: remove, link: pinfile.LinkAmbiguousLedger,
			blocker: "ambiguous: several applied proposals" + ambiguousFix,
		},
		{
			name: "a row with disagreeing fields on the key", entry: "amb", req: note, link: pinfile.LinkAmbiguousFields,
			blocker: "ambiguous: stored region fields disagree" + ambiguousFix,
		},
		{
			name: "an unlinked entry that a row's apply wrote", entry: "fields", req: note, link: pinfile.LinkNoProposal,
			blocker: "ambiguous: stored region fields disagree (applied #5)" + ambiguousFix,
		},
		{
			// p8 links no entry, but its apply writes the key of the
			// linked pin. The cause is in the ledger, not in the file.
			name: "a linked entry that a second row's apply wrote", entry: "linked", req: note, link: pinfile.LinkLinked,
			extra: []model.Proposal{{
				Signature: "s8", Rule: "linked", Region: "zzz", Regions: []string{"api"}, Note: "Disagrees.",
				Status: model.ProposalApplied,
			}},
			blocker: "ambiguous: stored region fields disagree (applied #8)" + ambiguousFix,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := matrixFixture(t)
			f.propose(tc.extra...)

			p := f.authorPlan(f.deps(), tc.req(f.ref(tc.entry, "")))

			assert.True(t, p.LinksKnown)
			assert.Equal(t, tc.link, p.Link)

			if tc.blocker == "" {
				assert.True(t, p.Confirmable, "blocked: %v", blockerLines(p))
				assert.Empty(t, p.Blockers)
				assert.NotNil(t, p.After)

				return
			}

			assert.False(t, p.Confirmable)
			assert.Equal(t, []string{tc.blocker}, blockerLines(p), "one cause gives one blocker")
			assert.Equal(t, BlockRelation, p.Blockers[0].Kind)
		})
	}
}

func TestPlanAuthorLinkedNoteEditKeepsItsLink(t *testing.T) {
	f := matrixFixture(t)
	ref := f.ref("linked", "")

	p := f.authorPlan(f.deps(), editNote(ref, "A corrected note."))

	require.True(t, p.Confirmable, "a linked note edit never collides with its own row: %v", blockerLines(p))
	assert.Equal(t, []int64{1}, p.EntryApplied)
	assert.Empty(t, p.EntryPending)
	require.NotNil(t, p.Proposal)
	assert.Equal(t, int64(1), p.Proposal.ID)
	require.NotNil(t, p.ProposalGuard)
	assert.True(t, p.ProposalGuard.Matches(*p.Proposal))

	// The own row holds the unchanged key, but a note edit adds no owner.
	assert.False(t, p.KeyChanged)
	assert.Equal(t, []int64{1}, proposalIDs(p.NewOwners.Applied))
	assert.Empty(t, p.NewOwners.Entries, "the edited entry is never its own owner")

	assert.Equal(t, strings.Replace(matrixLessons, "note: Recorded note.", "note: A corrected note.", 1), string(p.After))
	assert.True(t, p.IdentityChanged)
	assert.Equal(t, []string{
		"Earlier deliveries stay recorded under the earlier wording and scope. Nothing is reset or moved. " +
			"The new wording is a new delivery identity: a context that already saw the old wording can receive " +
			"it again, also in once-per-context mode.",
		"Proposal #1 measures its outcome by the live wording, so its Outcome reads 'untested' until the new " +
			"wording is delivered.",
		"Proposal #1 keeps its recorded rule, note, and evidence. Pins shows the difference as 'edited'. " +
			"Trigger extraction still asks about the recorded note. Saving does not extract triggers or retarget.",
		"The proposal ledger does not change.",
		noteRedacted,
	}, p.Notes)
}

func TestPlanAuthorRefusesAKeyThatAnotherOwnerHolds(t *testing.T) {
	cases := []struct {
		name  string
		req   func(f *fixture) AuthorRequest
		owner string
	}{
		{
			name:  "a live entry, in any case",
			req:   func(*fixture) AuthorRequest { return newPin("UNLINKED", "Another note.", "web/") },
			owner: "the pin at line 2",
		},
		{
			name:  "an applied row without a pin",
			req:   func(*fixture) AuthorRequest { return newPin("gone", "Guidance.", "srv") },
			owner: "applied #7",
		},
		{
			name:  "a pending row",
			req:   func(*fixture) AuthorRequest { return newPin("pending", "Guidance.", "db") },
			owner: "the pin at line 9, pending #2",
		},
		{
			name:  "the pin that a row with disagreeing fields writes",
			req:   func(*fixture) AuthorRequest { return newPin("fields", "Guidance.") },
			owner: "the pin at line 21, applied #5",
		},
		{
			name: "a rename onto a linked pin",
			req: func(f *fixture) AuthorRequest {
				return AuthorRequest{
					Op: AuthorEdit, Ref: f.ref("unlinked", ""), Rule: "linked", RuleSet: true,
					Scope: ScopeRegions, Regions: []string{"api"}, ScopeSet: true,
				}
			},
			owner: "the pin at line 6, applied #1",
		},
		{
			name: "a rescope onto an applied row",
			req: func(f *fixture) AuthorRequest {
				return AuthorRequest{
					Op: AuthorEdit, Ref: f.ref("unlinked", ""), Rule: "gone", RuleSet: true,
					Scope: ScopeRegions, Regions: []string{"srv"}, ScopeSet: true,
				}
			},
			owner: "applied #7",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := matrixFixture(t)

			p := f.authorPlan(f.deps(), tc.req(f))

			assert.False(t, p.Confirmable)
			assert.True(t, p.NewOwners.Any())
			assert.Equal(t, []string{textOwnedBy + tc.owner + "; " + routeOwner}, blockerLines(p),
				"the planner's own collision is not repeated")
			assert.Equal(t, BlockOwner, p.Blockers[0].Kind)
		})
	}
}

func TestPlanAuthorWithTheGateOffIsACompletePreview(t *testing.T) {
	f := authorFixture(t)
	f.gate(false)

	p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

	assert.False(t, p.Write)
	require.NoError(t, p.GateErr)
	assert.NotNil(t, p.After)
	assert.NotEmpty(t, p.Diff)
	assert.Equal(t, []string{
		"distill.write is off: this preview changes nothing; turn on Settings › Pin writing and preview again",
	}, blockerLines(p))
	assert.Equal(t, BlockGate, p.Blockers[0].Kind)
	assert.False(t, p.Confirmable)

	res, err := CommitAuthor(f.deps(), p)
	require.ErrorIs(t, err, ErrWriteDisabled)
	assert.Equal(t, NotWritten, res.Outcome)
	assert.Equal(t, authorLessons, f.lessons())

	f.config("distill: [\n")

	p = f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))
	require.Error(t, p.GateErr)
	assert.Equal(t, []string{"distill.write cannot be read; fix .seamark/config.yaml first"}, blockerLines(p))

	_, err = CommitAuthor(f.deps(), p)
	require.ErrorIs(t, err, ErrNotConfirmable)
}

func TestPlanAuthorWithoutALedgerStillPreviews(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		f := authorFixture(t)
		deps := f.deps()
		deps.Reader = nil

		p := f.authorPlan(deps, editNote(f.ref("linked-rule", ""), "A corrected note."))

		assert.False(t, p.LinksKnown)
		assert.Equal(t, pinfile.LinkUnknown, p.Link, "an unknown link is never read as unlinked")
		assert.Nil(t, p.Proposal)
		assert.Equal(t, strings.Replace(authorLessons, "note: Recorded note.", "note: A corrected note.", 1), string(p.After))
		assert.NotEmpty(t, p.Diff)
		assert.True(t, p.IdentityChanged, "the identity is still computed (P4-8)")
		assert.Equal(t, []string{
			"unknown: the proposal ledger could not be read: no readable index; " + routeIndex,
		}, blockerLines(p))
		assert.Equal(t, BlockLedger, p.Blockers[0].Kind)
		assert.False(t, p.Confirmable)

		_, err := CommitAuthor(deps, p)
		require.ErrorIs(t, err, ErrNotConfirmable)
		assert.Equal(t, authorLessons, f.lessons())
	})

	t.Run("a ledger read that fails", func(t *testing.T) {
		f := authorFixture(t)
		f.exec(`ALTER TABLE proposal RENAME TO proposal_gone`)

		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		assert.False(t, p.LinksKnown)
		assert.NotNil(t, p.After)
		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockLedger, p.Blockers[0].Kind)
		assert.True(t, strings.HasPrefix(p.Blockers[0].Text, ErrNoLedger.Error()))
	})
}

func TestPlanAuthorNamesAForeignRoot(t *testing.T) {
	f := authorFixture(t)
	foreign := errors.New("the index records another repository root (/elsewhere), not this workspace")
	deps := f.deps()
	deps.ForeignRoot = foreign

	p := f.authorPlan(deps, newPin("new-rule", "Guidance."))

	assert.NotNil(t, p.After, "the candidate is still computed (P4-16)")
	assert.Equal(t, []string{foreign.Error() + "; " + routeOwnIndex}, blockerLines(p))
	assert.Equal(t, BlockForeignRoot, p.Blockers[0].Kind)
	assert.False(t, p.Confirmable)

	// A plan from before the root changed is refused with the error of
	// the caller, so the caller can match its own sentinel.
	ok := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))
	require.True(t, ok.Confirmable)

	res, err := CommitAuthor(deps, ok)
	require.ErrorIs(t, err, foreign)
	assert.Equal(t, NotWritten, res.Outcome)
	assert.Equal(t, authorLessons, f.lessons())
}

func TestPlanAuthorRefusesAHardLinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the link count is read on unix only")
	}

	for name, req := range map[string]func(f *fixture) AuthorRequest{
		"create": func(*fixture) AuthorRequest { return newPin("new-rule", "Guidance.") },
		"edit":   func(f *fixture) AuthorRequest { return editNote(f.ref("Manual-Rule", ""), "New.") },
		"remove": func(f *fixture) AuthorRequest { return AuthorRequest{Op: AuthorRemove, Ref: f.ref("Manual-Rule", "")} },
	} {
		t.Run(name, func(t *testing.T) {
			f := authorFixture(t)
			require.NoError(t, os.Link(f.pinPath(), filepath.Join(f.root, "lessons-copy.yaml")))

			p := f.authorPlan(f.deps(), req(f))

			assert.Equal(t, []string{"replacing this file would break its hard link; edit .seamark/lessons.yaml by hand"},
				blockerLines(p), "the words of the settings editor (P4-17)")
			assert.Equal(t, integration.HardLinkReason, p.Blockers[0].Text)
			assert.Equal(t, BlockFile, p.Blockers[0].Kind)
			assert.NotNil(t, p.After, "a hard link blocks the write, not the review")
			assert.False(t, p.Confirmable)
		})
	}
}

func TestPlanAuthorRefusesACRLFFile(t *testing.T) {
	crlf := strings.ReplaceAll(authorLessons, "\n", "\r\n")

	for name, req := range map[string]func(f *fixture) AuthorRequest{
		"create": func(*fixture) AuthorRequest { return newPin("new-rule", "Guidance.") },
		"edit":   func(f *fixture) AuthorRequest { return editNote(f.ref("Manual-Rule", ""), "New.") },
		"remove": func(f *fixture) AuthorRequest { return AuthorRequest{Op: AuthorRemove, Ref: f.ref("Manual-Rule", "")} },
	} {
		t.Run(name, func(t *testing.T) {
			f := authorFixture(t)
			f.write(crlf, 0o644)

			p := f.authorPlan(f.deps(), req(f))

			// Every Phase 4 write refuses CRLF and names the manual route
			// (P4-6). The planners refuse for the same reason, so the plan
			// states it once.
			assert.Equal(t, []string{
				"the pin file cannot be changed: the file uses CRLF line breaks; edit .seamark/lessons.yaml by hand",
			}, blockerLines(p))
			assert.Nil(t, p.After)
			assert.False(t, p.Confirmable)
			assert.True(t, p.LinksKnown, "the listing of a CRLF file is still reliable")
		})
	}
}

func TestPlanAuthorNamesWhatBlocksTheFile(t *testing.T) {
	t.Run("a read-only file", func(t *testing.T) {
		f := authorFixture(t)
		f.write(authorLessons, 0o444)

		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		assert.Equal(t, []string{"the pin file cannot be changed: the file is read-only (-r--r--r--); " +
			"seamark does not replace it; edit .seamark/lessons.yaml by hand"}, blockerLines(p))
		assert.NotNil(t, p.After, "the engine refuses the write, so the review still shows the change")
	})

	t.Run("a file that the reader rejects", func(t *testing.T) {
		f := authorFixture(t)
		f.write("pin:\n  - rule: [unclosed\n", 0o644)

		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockFile, p.Blockers[0].Kind)
		assert.True(t, strings.HasPrefix(p.Blockers[0].Text, textFileBlock), p.Blockers[0].Text)
		assert.False(t, p.LinksKnown, "an unreliable listing proves no owner absent")
		assert.Nil(t, p.After)
	})

	t.Run("a path that is not a file", func(t *testing.T) {
		f := newFixture(t, nil)
		require.NoError(t, os.MkdirAll(f.pinPath(), 0o755))

		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockFile, p.Blockers[0].Kind)
		assert.Nil(t, p.After)
		assert.False(t, p.Confirmable)
	})
}
