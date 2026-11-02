package maintain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// scopeAt is the stamp of every scope fixture that sets At itself. It
// differs from fixtureNow, so a test sees which of the two a row holds.
const scopeAt = int64(1_790_000_000)

// scopeDraft returns the edit that sets the regions of ref.
func scopeDraft(ref pinfile.EntryRef, regions ...string) AuthorRequest {
	return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: regions, ScopeSet: true}
}

// scopePlan plans the edit with the stamp scopeAt.
func (f *fixture) scopePlan(deps Deps, edit AuthorRequest) *Plan {
	f.t.Helper()

	p, err := PlanScope(deps, ScopeRequest{Edit: edit, At: scopeAt})
	require.NoError(f.t, err)

	return p
}

// linkedDraft returns the edit of the linked pin of authorFixture.
func (f *fixture) linkedDraft(regions ...string) AuthorRequest {
	f.t.Helper()

	return scopeDraft(f.ref("linked-rule", ""), regions...)
}

// holdsLink asserts that the pin at rule still links its one applied row.
func (f *fixture) holdsLink(rule string, id int64) {
	f.t.Helper()

	doc, err := pinfile.Read(f.root)
	require.NoError(f.t, err)

	rows, err := readLedger(f.st)
	require.NoError(f.t, err)

	links := pinfile.Classify(doc, rows.applied, rows.pending, rows.superseded)

	for i, e := range doc.Entries {
		if e.Rule == rule {
			require.Equal(f.t, pinfile.LinkLinked, links.Entries[i].Link)
			assert.Equal(f.t, id, links.Entries[i].Applied[0].ID)
			assert.Equal(f.t, reviews.ProposalKey(links.Entries[i].Applied[0]), e.Key)

			return
		}
	}

	require.FailNow(f.t, "no entry", rule)
}

// assertUnwritten asserts that the plan cannot write and that neither
// store changed.
func assertUnwritten(t *testing.T, f *fixture, p *Plan, lessons, ledger string) {
	t.Helper()

	assert.False(t, p.Confirmable)

	_, err := Commit(f.deps(), p)
	require.Error(t, err)
	assert.Equal(t, lessons, f.lessons())
	assert.Equal(t, ledger, f.ledger())
}

func TestScopeEditRows(t *testing.T) {
	t.Run("row 6 a draft that keeps the regions saves through the pin review", func(t *testing.T) {
		for _, typed := range []string{"api", "api/", "./api"} {
			f := authorFixture(t)
			ledger := f.ledger()

			edit := f.linkedDraft(typed)
			edit.Note, edit.NoteSet = "A new note.", true

			scoped := f.scopePlan(f.deps(), edit)
			assert.False(t, scoped.Confirmable, typed)
			assert.Equal(t, ItemNoOp, scoped.Items[0].Status)
			assert.Equal(t, []string{reasonScopeKept}, scoped.Blocked)

			_, err := Commit(f.deps(), scoped)
			require.ErrorIs(t, err, ErrNotConfirmable)
			assert.Equal(t, authorLessons, f.lessons())

			p := f.authorPlan(f.deps(), edit)
			require.True(t, p.Confirmable, "%q: %v", typed, blockerLines(p))

			res, err := CommitAuthor(f.deps(), p)
			require.NoError(t, err)
			assert.Equal(t, Written, res.Outcome)
			assert.Contains(t, f.lessons(), "A new note.")
			assert.Equal(t, ledger, f.ledger())
			assert.Zero(t, f.row(1).ScopeChosen)
		}
	})

	t.Run("row 6 a stored trailing slash is no region change", func(t *testing.T) {
		f := authorFixture(t)
		f.write(strings.Replace(authorLessons, "region: api\n", "region: api/\n", 1), 0o644)

		edit := f.linkedDraft("api/")
		edit.Note, edit.NoteSet = "A new note.", true

		assert.Equal(t, []string{reasonScopeKept}, f.scopePlan(f.deps(), edit).Blocked)
		assert.Empty(t, blockerLines(f.authorPlan(f.deps(), edit)))
	})

	t.Run("row 7 a blocking issue shows in Blocked and keeps the old regions", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()

		p := f.scopePlan(f.deps(), f.linkedDraft("api", "api/"))

		require.Len(t, p.Blocked, 1)
		assert.Equal(t, "regions: region 2 repeats region 1", p.Blocked[0])
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.False(t, p.Items[0].Drafted)
		assert.Equal(t, []string{"api"}, p.Items[0].From)
		assert.Nil(t, p.Change)
		assert.Empty(t, p.Retarget)
		assertUnwritten(t, f, p, authorLessons, ledger)
	})

	t.Run("row 8 a hard link names the file route", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()

		require.NoError(t, os.Link(f.pinPath(), filepath.Join(f.root, "second-name")))

		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		assert.Equal(t, []string{integration.HardLinkReason + "; " + routeScopeFile}, p.Blocked)
		assert.Empty(t, p.FileBlock, "no text routes this draft to a hand edit alone")
		assert.True(t, p.Items[0].Drafted)
		assert.Equal(t, []string{"web"}, p.Items[0].To)
		assert.NotNil(t, p.Change, "the preview still shows the diff")
		assertUnwritten(t, f, p, authorLessons, ledger)
	})

	t.Run("row 8 a file block names the file route", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()
		crlf := strings.ReplaceAll(authorLessons, "\n", "\r\n")

		f.write(crlf, 0o644)

		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		require.Len(t, p.Blocked, 1)
		assert.True(t, strings.HasPrefix(p.Blocked[0], textFileBlock), p.Blocked[0])
		assert.True(t, strings.HasSuffix(p.Blocked[0], "; "+routeScopeFile), p.Blocked[0])
		assert.NotContains(t, p.Blocked[0], routeByHand)
		assert.Empty(t, p.FileBlock)
		assertUnwritten(t, f, p, crlf, ledger)
	})

	t.Run("row 9 an owner of the new key is a collision", func(t *testing.T) {
		for name, setup := range map[string]func(f *fixture){
			"a pin": func(*fixture) {},
			"an applied row": func(f *fixture) {
				f.propose(model.Proposal{Signature: "s2", Rule: "linked-rule", Region: "web", Status: model.ProposalApplied})
			},
			"a pending row": func(f *fixture) {
				f.propose(model.Proposal{Signature: "s3", Rule: "linked-rule", Region: "web", Status: model.ProposalProposed})
			},
		} {
			t.Run(name, func(t *testing.T) {
				f := authorFixture(t)

				lessons := authorLessons
				if name == "a pin" {
					lessons = strings.Replace(lessons, "  - rule: Manual-Rule\n    region: web\n    regions: [web, docs]\n",
						"  - rule: linked-rule\n    region: web\n", 1)
				}

				f.write(lessons, 0o644)
				setup(f)

				ledger := f.ledger()
				p := f.scopePlan(f.deps(), scopeDraft(f.ref("linked-rule", "api"), "web"))

				require.Len(t, p.Blocked, 1)
				assert.True(t, strings.HasPrefix(p.Blocked[0], textOwnedBy), p.Blocked[0])
				assert.True(t, strings.HasSuffix(p.Blocked[0], "; "+routeOwnerScope), p.Blocked[0])
				assert.True(t, p.Items[0].Drafted, "the preview keeps the new regions")
				assertUnwritten(t, f, p, lessons, ledger)
			})
		}
	})

	t.Run("row 10 another applied row owns the old key", func(t *testing.T) {
		f := authorFixture(t)
		f.propose(model.Proposal{Signature: "s2", Rule: "linked-rule", Region: "api", Status: model.ProposalApplied})

		ledger := f.ledger()
		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		assert.Equal(t, []string{"ambiguous: several applied proposals; " + reasonLedgerFixes}, p.Blocked)
		assertUnwritten(t, f, p, authorLessons, ledger)
	})

	t.Run("row 10 the link is gone", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()

		p := f.scopePlan(f.deps(), scopeDraft(f.ref("Manual-Rule", ""), "web"))

		assert.Equal(t, []string{pinfile.LinkNoProposal.Reason() + "; " + routeScopeLink}, p.Blocked)
		assert.Empty(t, p.Retarget)
		assertUnwritten(t, f, p, authorLessons, ledger)
	})

	t.Run("row 11 the draft fails the strict tier", func(t *testing.T) {
		f := newFixture(t, nil)
		f.write(checksNotesLessons, 0o644)
		f.propose(model.Proposal{Signature: "s1", Rule: "checked", Region: "api", Status: model.ProposalApplied})

		ledger := f.ledger()

		edit := scopeDraft(f.ref("checked", ""), "web")
		edit.Check, edit.CheckSet = "no-such-check", true

		p := f.scopePlan(f.deps(), edit)

		require.Len(t, p.Blocked, 1)
		assert.True(t, strings.HasSuffix(p.Blocked[0], "; "+routeCheckDraft), p.Blocked[0])
		require.Len(t, p.Impact, 1, "the impact shows before the strict tier refuses")
		assertUnwritten(t, f, p, checksNotesLessons, ledger)
	})

	t.Run("row 11 the file already fails the strict tier", func(t *testing.T) {
		f := newFixture(t, nil)
		invalid := strings.Replace(checksNotesLessons, "    check: frozen\n", "    check: frozen-gone\n", 1)

		f.write(invalid, 0o644)
		f.propose(model.Proposal{Signature: "s1", Rule: "checked", Region: "api", Status: model.ProposalApplied})

		ledger := f.ledger()
		p := f.scopePlan(f.deps(), scopeDraft(f.ref("checked", ""), "web"))

		require.Len(t, p.Blocked, 1)
		assert.True(t, strings.HasSuffix(p.Blocked[0], "; "+routeScopeFile), p.Blocked[0])
		assert.NotContains(t, p.Blocked[0], routeByHand)
		assertUnwritten(t, f, p, invalid, ledger)
	})

	t.Run("row 12 gate off keeps Blocked empty and refuses", func(t *testing.T) {
		f := authorFixture(t)
		f.gate(false)

		ledger := f.ledger()
		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		assert.Empty(t, p.Blocked, "the gate note prints it once")
		assert.False(t, p.Write)
		assert.Equal(t, ItemReady, p.Items[0].Status)
		assert.NotNil(t, p.Change)

		_, err := Commit(f.deps(), p)
		require.ErrorIs(t, err, ErrWriteDisabled)
		assert.Equal(t, authorLessons, f.lessons())
		assert.Equal(t, ledger, f.ledger())
	})

	t.Run("row 12 an unreadable gate keeps its error", func(t *testing.T) {
		f := authorFixture(t)
		f.config("distill: [\n")

		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		assert.Empty(t, p.Blocked)
		require.Error(t, p.GateErr)

		_, err := Commit(f.deps(), p)
		require.ErrorIs(t, err, ErrNotConfirmable)
	})

	t.Run("row 12 gate off never hides a collision", func(t *testing.T) {
		f := authorFixture(t)
		f.gate(false)
		f.propose(model.Proposal{Signature: "s3", Rule: "linked-rule", Region: "web", Status: model.ProposalProposed})

		p := f.scopePlan(f.deps(), f.linkedDraft("web"))

		require.Len(t, p.Blocked, 1)
		assert.Contains(t, p.Blocked[0], routeOwnerScope)
	})

	t.Run("row 13 a clean draft can be confirmed", func(t *testing.T) {
		f := authorFixture(t)
		p := f.scopePlan(f.deps(), f.linkedDraft("web", "docs"))

		require.True(t, p.Confirmable, "%v", p.Blocked)
		assert.Equal(t, ActionScope, p.Action)
		assert.Equal(t, Request{Action: ActionScope, Refs: []pinfile.EntryRef{f.ref("linked-rule", "")}}, p.Request)
		require.NotNil(t, p.Scoping)
		assert.Equal(t, scopeAt, p.Scoping.At)

		it := p.Items[0]
		assert.Equal(t, ItemReady, it.Status)
		assert.True(t, it.Drafted)
		assert.Equal(t, []string{"api"}, it.From)
		assert.Equal(t, []string{"web", "docs"}, it.To)
		assert.True(t, it.Broader)
		assert.True(t, it.Narrower)
		assert.False(t, it.RepoWide)
		assert.Equal(t, []string{"docs"}, it.Missing, "a region with no path is information")
		assert.Empty(t, p.Blocked)
		assert.Empty(t, p.FileBlock)

		require.Len(t, p.Retarget, 1)
		assert.Equal(t, "web", p.Retarget[0].Region)
		assert.Equal(t, []string{"web", "docs"}, p.Retarget[0].Regions)
		assert.Equal(t, scopeAt, p.Retarget[0].Chosen)
	})

	t.Run("row 15 a change after the preview writes nothing", func(t *testing.T) {
		for name, change := range map[string]func(f *fixture){
			"the file": func(f *fixture) { f.write(authorLessons+"# a comment\n", 0o644) },
			"the mode": func(f *fixture) { require.NoError(t, os.Chmod(f.pinPath(), 0o600)) },
			"the row":  func(f *fixture) { f.exec(`UPDATE proposal SET scope_chosen_at = 7 WHERE id = 1`) },
			"the gate": func(f *fixture) { f.gate(false) },
		} {
			t.Run(name, func(t *testing.T) {
				f := authorFixture(t)
				p := f.scopePlan(f.deps(), f.linkedDraft("web"))

				require.True(t, p.Confirmable)
				change(f)

				lessons, ledger := f.lessons(), f.ledger()

				_, err := Commit(f.deps(), p)
				require.ErrorIs(t, err, ErrStale)
				assert.Equal(t, lessons, f.lessons())
				assert.Equal(t, ledger, f.ledger())
			})
		}
	})

	t.Run("row 16 a failed file write changes nothing", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()
		deps := f.deps()
		deps.Files = &fileSeam{failBefore: errors.New("disk full")}

		res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))

		require.NoError(t, err)
		assert.Equal(t, noteFileFailed, res.Note)
		assert.False(t, res.FileWritten)
		assert.Nil(t, res.Recovery)
		assert.Equal(t, authorLessons, f.lessons())
		assert.Equal(t, ledger, f.ledger())
	})

	t.Run("row 17 a failed ledger write restores the file", func(t *testing.T) {
		f := authorFixture(t)
		ledger := f.ledger()
		deps := f.deps()
		deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("database is locked")}

		res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))

		require.NoError(t, err)
		assert.Equal(t, noteRestored, res.Note)
		assert.True(t, res.Restored)
		assert.Nil(t, res.Recovery)
		assert.Equal(t, authorLessons, f.lessons())
		assert.Equal(t, ledger, f.ledger())
	})

	t.Run("row 17 a file failure after the bytes landed restores the file", func(t *testing.T) {
		f := authorFixture(t)
		deps := f.deps()
		deps.Files = &fileSeam{failAfter: errors.New("fsync failed")}

		res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))

		require.NoError(t, err)
		assert.Equal(t, noteFileRestored, res.Note)
		assert.True(t, res.Restored)
		assert.Equal(t, authorLessons, f.lessons())
	})

	t.Run("row 18 a failed restore offers the completion", func(t *testing.T) {
		f := authorFixture(t)
		deps := f.deps()
		deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("database is locked")}
		deps.Files = &fileSeam{restoreErr: errors.New("read-only file system")}

		res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))

		require.NoError(t, err)
		assert.Equal(t, noteRestoreFailed, res.Note)
		assert.True(t, res.Partial())
		require.NotNil(t, res.Recovery)
		assert.Equal(t, ActionScope, res.Recovery.Action)

		plan, err := PlanRecovery(f.deps(), res.Recovery)
		require.NoError(t, err)
		require.True(t, plan.Confirmable, "%v", plan.Blocked)

		done, err := CommitRecovery(f.deps(), plan)
		require.NoError(t, err)
		assert.Equal(t, noteRecovered, done.Note)
		assert.Equal(t, scopeAt, f.row(1).ScopeChosen)
		assert.Equal(t, []string{"web"}, f.row(1).Regions)
		f.holdsLink("linked-rule", 1)
	})

	t.Run("row 18 changed rows leave the file and the proof names the edit", func(t *testing.T) {
		f := authorFixture(t)
		deps := f.deps()
		deps.Ledger = &ledgerSeam{
			Store: f.st, err: errors.New("database is locked"),
			before: func() { f.exec(`UPDATE proposal SET rule = 'moved' WHERE id = 1`) },
		}

		res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))

		require.NoError(t, err)
		assert.Equal(t, noteOtherState, res.Note)
		assert.False(t, res.Restored)
		require.NotNil(t, res.Recovery)

		// Another writer removes the edited entry after the partial save.
		f.write(authorLessons, 0o644)

		plan, err := PlanRecovery(f.deps(), res.Recovery)
		require.NoError(t, err)
		assert.False(t, plan.Confirmable)
		assert.Contains(t, plan.Items[0].Reason, reasonScopeGone)
		assert.NotContains(t, strings.ToLower(plan.Items[0].Reason), "retarget")
	})

	t.Run("row 19 both writes land", func(t *testing.T) {
		f := authorFixture(t)
		history := f.history()
		p := f.scopePlan(f.deps(), f.linkedDraft("web", "docs"))

		res, err := Commit(f.deps(), p)

		require.NoError(t, err)
		assert.Equal(t, noteDone, res.Note)
		assert.True(t, res.LedgerDone)
		assert.Equal(t, []int64{1}, res.Moved)
		assert.Equal(t, strings.Replace(authorLessons, "    region: api\n", "    region: web\n    regions: [web, docs]\n", 1),
			f.lessons())

		row := f.row(1)
		assert.Equal(t, "web", row.Region)
		assert.Equal(t, []string{"web", "docs"}, row.Regions)
		assert.Equal(t, scopeAt, row.ScopeChosen)
		assert.Equal(t, model.ProposalApplied, row.Status)
		assert.Equal(t, history, f.history(), "only the region fields and the stamp change")
		f.holdsLink("linked-rule", 1)
	})

	t.Run("row 19 another writer completed the ledger", func(t *testing.T) {
		for name, stamp := range map[string]int64{"at the same time": scopeAt, "with its own time": scopeAt + 60} {
			t.Run(name, func(t *testing.T) {
				f := authorFixture(t)
				p := f.scopePlan(f.deps(), f.linkedDraft("web"))
				deps := f.deps()
				deps.Ledger = &ledgerSeam{
					Store: f.st, err: errors.New("database is locked"),
					before: func() {
						require.NoError(t, f.st.RetargetApplied([]store.RegionChange{{
							Guard: store.NewRowGuard(f.row(1)), Region: "web", Regions: []string{"web"}, Chosen: stamp,
						}}))
					},
				}

				res, err := Commit(deps, p)

				require.NoError(t, err)
				assert.Equal(t, noteOtherWriter, res.Note)
				assert.True(t, res.LedgerDone)
				assert.False(t, res.Restored)
				f.holdsLink("linked-rule", 1)
			})
		}
	})

	t.Run("row 19 regions without a choice are no completion", func(t *testing.T) {
		f := authorFixture(t)
		p := f.scopePlan(f.deps(), f.linkedDraft("web"))
		deps := f.deps()
		deps.Ledger = &ledgerSeam{
			Store: f.st, err: errors.New("database is locked"),
			before: func() { f.retargetRow(1, "web") },
		}

		res, err := Commit(deps, p)

		require.NoError(t, err)
		assert.Equal(t, noteOtherState, res.Note)
		assert.False(t, res.LedgerDone)
	})
}

// The preview reads the link and the rows of the real item. A surface shows
// the ledger row and the note comparison from these two fields.
func TestPlanScopeItemHoldsTheLinkAndTheRow(t *testing.T) {
	f := authorFixture(t)

	p := f.scopePlan(f.deps(), f.linkedDraft("web"))
	require.Len(t, p.Items, 1)

	it := p.Items[0]
	assert.Equal(t, "linked-rule", it.Pin.Entry.Rule)
	assert.Equal(t, pinfile.LinkLinked, it.Pin.Link.Link)
	assert.Equal(t, []model.Proposal{f.row(1)}, it.Pin.Link.Applied)
	assert.Equal(t, []model.Proposal{f.row(1)}, it.Rows)
}

// TestScopeEditRefusesAnotherRoot holds the engine refusal, which runs
// again at the commit. The console review keeps y off, but a root can
// change between the preview and the key.
func TestScopeEditRefusesAnotherRoot(t *testing.T) {
	f := authorFixture(t)
	ledger := f.ledger()
	foreign := errors.New("the index records another root")

	blocked := f.deps()
	blocked.ForeignRoot = foreign

	p := f.scopePlan(blocked, f.linkedDraft("web"))
	assert.False(t, p.Confirmable)
	assert.Equal(t, []string{foreign.Error() + "; " + routeOwnIndex}, p.Blocked)

	reviewed := f.scopePlan(f.deps(), f.linkedDraft("web"))
	require.True(t, reviewed.Confirmable)

	_, err := Commit(blocked, reviewed)
	require.ErrorIs(t, err, foreign)
	assert.Equal(t, authorLessons, f.lessons())
	assert.Equal(t, ledger, f.ledger())
}

// TestPlanScopeRequest holds what the request must carry. The stamp stays
// the time of the review, so the plan of the commit is identical.
func TestPlanScopeRequest(t *testing.T) {
	f := authorFixture(t)
	ref := f.ref("linked-rule", "")

	for _, edit := range []AuthorRequest{
		{Op: AuthorEdit, Ref: ref, Note: "A note.", NoteSet: true},
		{Op: AuthorRemove, Ref: ref, ScopeSet: true},
		{Op: AuthorCreate, Scope: ScopeRegions, Regions: []string{"web"}, ScopeSet: true},
	} {
		_, err := PlanScope(f.deps(), ScopeRequest{Edit: edit})
		require.Error(t, err)
	}

	p, err := PlanScope(f.deps(), ScopeRequest{Edit: f.linkedDraft("web")})
	require.NoError(t, err)
	assert.Equal(t, fixtureNow.Unix(), p.Scoping.At)

	later := f.deps()
	later.Now = func() time.Time { return fixtureNow.Add(time.Hour) }

	_, err = Commit(later, p)
	require.NoError(t, err)
	assert.Equal(t, fixtureNow.Unix(), f.row(1).ScopeChosen)
}

// TestPlanAuthorStillRefusesARegionChange holds the last check: the file
// commit never writes regions that leave the row behind.
func TestPlanAuthorStillRefusesARegionChange(t *testing.T) {
	f := authorFixture(t)

	p := f.authorPlan(f.deps(), f.linkedDraft("web"))

	assert.False(t, p.Confirmable)
	assert.Equal(t, []string{"linked to proposal #1; a change of its regions needs the scope preview"}, blockerLines(p))

	p = f.authorPlan(f.deps(), f.linkedDraft("api", "docs"))
	assert.False(t, p.Confirmable)

	_, err := CommitAuthor(f.deps(), p)
	require.Error(t, err)
	assert.Equal(t, authorLessons, f.lessons())
}

// TestScopeEditKeepsTheRuleOfTheProposal holds the scope of the route: a
// person chooses regions here, and a rule change stays refused.
func TestScopeEditKeepsTheRuleOfTheProposal(t *testing.T) {
	f := authorFixture(t)
	edit := f.linkedDraft("web")
	edit.Rule, edit.RuleSet = "renamed", true

	p := f.scopePlan(f.deps(), edit)

	assert.Equal(t, []string{
		"linked to proposal #1; only its note, mode, check, and regions can change here; " + routeLinkedPin,
	}, p.Blocked)
	assert.False(t, p.Confirmable)
}

func TestChangesRegions(t *testing.T) {
	wide := reviews.PinRule{Rule: "r", Region: "*"}
	bare := reviews.PinRule{Rule: "r"}
	one := reviews.PinRule{Rule: "r", Region: "api"}
	slash := reviews.PinRule{Rule: "r", Region: "api/"}
	two := reviews.PinRule{Rule: "r", Region: "api", Regions: []string{"api", "web"}}

	regions := func(set ...string) AuthorRequest {
		return AuthorRequest{Op: AuthorEdit, Scope: ScopeRegions, Regions: set, ScopeSet: true}
	}
	repoWide := AuthorRequest{Op: AuthorEdit, Scope: ScopeRepoWide, ScopeSet: true}

	cases := []struct {
		name string
		old  reviews.PinRule
		req  AuthorRequest
		want bool
	}{
		{"no scope in the draft", one, AuthorRequest{Op: AuthorEdit, Note: "n", NoteSet: true}, false},
		{"the same region", one, regions("api"), false},
		{"a typed trailing slash", one, regions("api/"), false},
		{"a typed dot prefix", one, regions("./api"), false},
		{"a stored trailing slash", slash, regions("api/"), false},
		{"another region", one, regions("web"), true},
		{"an added region", one, regions("api", "web"), true},
		{"a removed region", two, regions("api"), true},
		{"a new order", two, regions("web", "api"), true},
		{"the same order", two, regions("api", "web"), false},
		{"repository-wide stays", wide, repoWide, false},
		{"repository-wide without a key stays", bare, repoWide, false},
		{"regions to repository-wide", one, repoWide, true},
		{"repository-wide to regions", wide, regions("api"), true},
		{"a scope issue goes to the preview", one, regions("api", "api/"), true},
		{"no choice goes to the preview", one, AuthorRequest{Op: AuthorEdit, ScopeSet: true}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChangesRegions(tc.old, tc.req))
		})
	}
}

func TestScopeCoverage(t *testing.T) {
	cases := []struct {
		name     string
		old      string
		regions  []string
		want     string
		repoWide bool
	}{
		{name: "narrower", old: "'*'", regions: []string{"api"}, want: textCoverageLose},
		{name: "broader", old: "api", regions: []string{"api", "web"}, want: textCoverageGain},
		{name: "repository-wide", old: "api", want: textCoverageGain, repoWide: true},
		{name: "neither narrower nor broader", old: "api", regions: []string{"web"}, want: textCoverageBoth},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.write("pin:\n  - rule: linked\n    region: "+tc.old+"\n    note: A note.\n", 0o644)
			f.propose(model.Proposal{
				Signature: "s1", Rule: "linked", Region: strings.Trim(tc.old, "'"), Status: model.ProposalApplied,
			})

			edit := scopeDraft(f.ref("linked", ""), tc.regions...)
			if tc.repoWide {
				edit.Scope = ScopeRepoWide
			}

			p := f.scopePlan(f.deps(), edit)

			require.True(t, p.Confirmable, "%v", p.Blocked)
			assert.Equal(t, tc.repoWide, p.Items[0].RepoWide)
			assert.Equal(t, tc.want, p.Items[0].Coverage())
		})
	}

	assert.Equal(t, textCoverageSame, PlanItem{}.Coverage(), "a new order covers the same paths")
}

func TestScopePlanNotesFollowTheChange(t *testing.T) {
	const twoRegions = `pin:
  - rule: two
    region: api
    regions: [api, web]
    note: A note.
`

	setup := func(t *testing.T) *fixture {
		t.Helper()

		f := newFixture(t, nil)
		f.write(twoRegions, 0o644)
		f.propose(model.Proposal{
			Signature: "s1", Rule: "two", Region: "api", Regions: []string{"api", "web"}, Note: "A note.",
			Status: model.ProposalApplied,
		})

		return f
	}

	t.Run("a changed set", func(t *testing.T) {
		f := setup(t)
		p := f.scopePlan(f.deps(), scopeDraft(f.ref("two", ""), "api"))

		assert.Equal(t, []string{
			textIdentityChanged, strings.ReplaceAll(textIdentityLinked, "%d", "1"), textScopeLedger, noteRedacted,
		}, p.Notes)
	})

	t.Run("a new order", func(t *testing.T) {
		f := setup(t)
		p := f.scopePlan(f.deps(), scopeDraft(f.ref("two", ""), "web", "api"))

		assert.Equal(t, []string{textScopeOrder, textScopeLedger, noteRedacted}, p.Notes)
		assert.Equal(t, []string{"web", "api"}, p.Items[0].To)
		assert.False(t, p.Items[0].Broader || p.Items[0].Narrower)
		require.True(t, p.Confirmable, "%v", p.Blocked)
	})

	t.Run("a note change in the same draft", func(t *testing.T) {
		f := setup(t)
		edit := scopeDraft(f.ref("two", ""), "api")
		edit.Note, edit.NoteSet = "Another note.", true

		p := f.scopePlan(f.deps(), edit)

		assert.Equal(t, []string{
			textIdentityChanged, strings.ReplaceAll(textIdentityLinked, "%d", "1"),
			strings.ReplaceAll(textScopeKeeps, "%d", "1"), textScopeLedger, noteRedacted,
		}, p.Notes)
		assert.NotContains(t, strings.ToLower(strings.Join(p.Notes, " ")), "retarget",
			"each route keeps its own words")
	})

	t.Run("a blocked plan still states what it would do", func(t *testing.T) {
		f := setup(t)
		f.gate(false)

		p := f.scopePlan(f.deps(), scopeDraft(f.ref("two", ""), "api"))

		assert.Contains(t, p.Notes, textScopeLedger)
		assert.NotContains(t, p.Notes, textLedgerEffect)
	})
}

// The result prints the ledger error of the real store. A seam scripts that
// text, so this test fails the real write, in the commit and in the recovery.
func TestScopeLedgerFailureKeepsTheRouteWords(t *testing.T) {
	f := authorFixture(t)
	f.failUpdates("region")

	deps := f.deps()
	deps.Files = &fileSeam{restoreErr: errors.New("read-only file system")}

	res, err := Commit(deps, f.scopePlan(f.deps(), f.linkedDraft("web")))
	require.NoError(t, err)
	require.ErrorContains(t, res.LedgerErr, "scope edit p1")
	assert.NotContains(t, strings.ToLower(res.LedgerErr.Error()), "retarget")
	require.NotNil(t, res.Recovery)

	plan, err := PlanRecovery(f.deps(), res.Recovery)
	require.NoError(t, err)

	done, err := CommitRecovery(f.deps(), plan)
	require.NoError(t, err)
	require.ErrorContains(t, done.LedgerErr, "scope edit p1")
	assert.NotContains(t, strings.ToLower(done.LedgerErr.Error()), "retarget")
}

// A draft with a blocking issue has no new note, so the plan claims no
// note change.
func TestScopePlanNotesOfABlockingIssueNameNoNote(t *testing.T) {
	f := authorFixture(t)

	p := f.scopePlan(f.deps(), f.linkedDraft("../outside"))
	require.NotEmpty(t, p.Blocked)
	assert.False(t, p.Items[0].Drafted)

	for _, n := range p.Notes {
		assert.NotContains(t, n, "keeps its recorded rule")
	}
}

// TestScopeEditOfANewOrderKeepsTheIdentity holds the kept key: both
// stores change and the stamp is set, but deliveries stay valid. The
// pin's own row is no collision, because a new order keeps the key.
func TestScopeEditOfANewOrderKeepsTheIdentity(t *testing.T) {
	const twoRegions = `pin:
  - rule: two
    region: api
    regions: [api, web]
    note: A note.
`

	f := newFixture(t, nil)
	f.write(twoRegions, 0o644)
	f.propose(model.Proposal{
		Signature: "s1", Rule: "two", Region: "api", Regions: []string{"api", "web"}, Note: "A note.",
		Status: model.ProposalApplied,
	})

	before, err := reviews.ParseConfig([]byte(twoRegions))
	require.NoError(t, err)

	p := f.scopePlan(f.deps(), scopeDraft(f.ref("two", ""), "web", "api"))
	require.True(t, p.Confirmable, "%v", p.Blocked)

	_, err = Commit(f.deps(), p)
	require.NoError(t, err)

	after, err := reviews.ParseConfig([]byte(f.lessons()))
	require.NoError(t, err)

	assert.Equal(t, reviews.PinIdentity(before.Pin[0]), reviews.PinIdentity(after.Pin[0]))
	assert.Equal(t, before.PinVersion(before.Pin[0]), after.PinVersion(after.Pin[0]))
	assert.Contains(t, f.lessons(), "regions: [web, api]")
	assert.Equal(t, "web", f.row(1).Region)
	assert.Equal(t, []string{"web", "api"}, f.row(1).Regions)
	assert.Equal(t, scopeAt, f.row(1).ScopeChosen)
	f.holdsLink("two", 1)
}

func TestScopeEditKeepsModeAndCheck(t *testing.T) {
	f := newFixture(t, nil)
	f.write(checksNotesLessons, 0o644)
	f.propose(model.Proposal{Signature: "s1", Rule: "checked", Region: "api", Status: model.ProposalApplied})

	t.Run("the plan shows the impact", func(t *testing.T) {
		p := f.scopePlan(f.deps(), scopeDraft(f.ref("checked", ""), "web"))

		require.True(t, p.Confirmable, "%v", p.Blocked)
		require.Len(t, p.Impact, 1)
		assert.Equal(t, lessoncheck.ModeCheck, p.Impact[0].Mode)
		assert.Equal(t, "frozen", p.Impact[0].Check.ID)
		assert.Equal(t, []string{"api"}, p.Impact[0].ScopeBefore)
		assert.Equal(t, []string{"web"}, p.Impact[0].ScopeAfter)
	})

	t.Run("the commit keeps both keys and the note", func(t *testing.T) {
		p := f.scopePlan(f.deps(), scopeDraft(f.ref("checked", ""), "web"))

		_, err := Commit(f.deps(), p)
		require.NoError(t, err)
		assert.Equal(t, strings.Replace(checksNotesLessons, "    region: api\n", "    region: web\n", 1), f.lessons())
		f.holdsLink("checked", 1)
	})
}

func TestScopeEditKeepsAChangedModeInOneOperation(t *testing.T) {
	f := newFixture(t, nil)
	f.write(checksNotesLessons, 0o644)
	f.propose(model.Proposal{Signature: "s1", Rule: "checked", Region: "api", Status: model.ProposalApplied})

	edit := scopeDraft(f.ref("checked", ""), "web")
	edit.Check, edit.CheckSet = "frozen-too", true
	edit.Note, edit.NoteSet = "Another note.", true

	deps := f.deps()
	deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("database is locked")}

	res, err := Commit(deps, f.scopePlan(f.deps(), edit))

	require.NoError(t, err)
	assert.True(t, res.Restored, "a failure restores every field of the draft")
	assert.Equal(t, checksNotesLessons, f.lessons())

	_, err = Commit(f.deps(), f.scopePlan(f.deps(), edit))
	require.NoError(t, err)

	got := f.lessons()
	assert.Contains(t, got, "    check: frozen-too\n")
	assert.Contains(t, got, "    note: Another note.\n")
	assert.Contains(t, got, "    region: web\n")
}

func TestScopeEditKeepsProvenance(t *testing.T) {
	f := authorFixture(t)
	f.exec(`UPDATE proposal SET trigger_paths = '["api/a.go"]', trigger_checked_at = 9 WHERE id = 1`)

	history, before := f.history(), f.row(1)

	_, err := Commit(f.deps(), f.scopePlan(f.deps(), f.linkedDraft("web")))
	require.NoError(t, err)

	assert.Contains(t, f.lessons(), "  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)\n"+
		"  - rule: linked-rule\n    region: web\n    note: Recorded note.\n  # a person wrote this one\n")
	assert.Equal(t, history, f.history())

	after := f.row(1)
	before.Region, before.Regions, before.ScopeChosen = after.Region, after.Regions, after.ScopeChosen
	assert.Equal(t, before, after, "only Region, Regions, and ScopeChosen change")
}

func TestScopeEditOfARepositoryWidePin(t *testing.T) {
	cases := []struct {
		name, entry string
	}{
		{name: "a star region", entry: "    region: '*'\n"},
		{name: "no region key", entry: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil, "api/a.go")
			f.write("pin:\n  - rule: wide\n"+tc.entry+"    note: A note.\n", 0o644)
			f.propose(model.Proposal{Signature: "s1", Rule: "wide", Note: "A note.", Status: model.ProposalApplied})

			p := f.scopePlan(f.deps(), scopeDraft(f.ref("wide", ""), "api", "web"))

			require.True(t, p.Confirmable, "%v", p.Blocked)
			assert.Nil(t, p.Items[0].From, "the old set reads as repository-wide")
			assert.True(t, p.Items[0].Narrower)
			assert.False(t, p.Items[0].Broader)
			assert.Equal(t, []string{"web"}, p.Items[0].Missing)

			_, err := Commit(f.deps(), p)
			require.NoError(t, err)

			assert.Equal(t, "api", f.row(1).Region)
			assert.Equal(t, []string{"api", "web"}, f.row(1).Regions)
			f.holdsLink("wide", 1)

			// The way back is a manual edit too.
			back := AuthorRequest{Op: AuthorEdit, Ref: f.ref("wide", ""), Scope: ScopeRepoWide, ScopeSet: true}

			p = f.scopePlan(f.deps(), back)
			require.True(t, p.Confirmable, "%v", p.Blocked)
			assert.True(t, p.Items[0].RepoWide)
			assert.False(t, p.Items[0].Narrower)

			_, err = Commit(f.deps(), p)
			require.NoError(t, err)
			assert.Empty(t, f.row(1).Regions)
			assert.Empty(t, f.row(1).Region)
			f.holdsLink("wide", 1)
		})
	}
}

// TestScopeEditHoldsMoreThanTheComputedBound holds a chosen set above the
// bound of a computed set, which applies to inference only.
func TestScopeEditHoldsMoreThanTheComputedBound(t *testing.T) {
	f := authorFixture(t)

	_, err := Commit(f.deps(), f.scopePlan(f.deps(), f.linkedDraft("a", "b", "c", "d", "e")))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, f.row(1).Regions)
	f.holdsLink("linked-rule", 1)
}

// TestScopeEditNeedsNoIndex holds that the computed scope decides no
// outcome of this edit. A retarget needs a ready index for its scope
// advice, but this edit works without an index.
func TestScopeEditNeedsNoIndex(t *testing.T) {
	f := newFixture(t, nil)
	f.write(authorLessons, 0o644)
	f.propose(model.Proposal{
		Signature: "s1", Rule: "linked-rule", Region: "api", Status: model.ProposalApplied,
	})

	deps := f.deps()
	deps.Probe.State = 0

	p := f.scopePlan(deps, f.linkedDraft("web"))
	require.True(t, p.Confirmable, "%v", p.Blocked)

	_, err := Commit(deps, p)
	require.NoError(t, err)
	assert.Equal(t, "web", f.row(1).Region)
	assert.Equal(t, scopeAt, f.row(1).ScopeChosen)
}
