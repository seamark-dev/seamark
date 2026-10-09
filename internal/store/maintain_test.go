package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// ledgerProposal stores one proposal with a region set and returns it
// with its ID. Region holds the first region, as distill writes it.
func ledgerProposal(t *testing.T, s *Store, rule, status string, regions ...string) model.Proposal {
	t.Helper()

	p := model.Proposal{
		Signature: "sig-" + rule, Rule: rule, Note: "Note for " + rule + ".",
		Members: []int64{1, 2}, Agent: "claude/v2", Status: status, CreatedAt: 1700000000,
		Regions: regions,
	}

	if len(regions) > 0 {
		p.Region = regions[0]
	}

	require.NoError(t, s.InsertProposal(&p))

	return p
}

// allProposals reads every proposal row in ID order, whatever its
// status. Tests compare it before and after a refused write.
func allProposals(t *testing.T, s *Store) []model.Proposal {
	t.Helper()

	rows, err := s.db.Query(`SELECT ` + proposalCols + ` FROM proposal ORDER BY id`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	ps, err := scanProposals(rows)
	require.NoError(t, err)

	return ps
}

// proposalRow reads one row in any status.
func proposalRow(t *testing.T, s *Store, id int64) model.Proposal {
	t.Helper()

	ps, err := s.ProposalsWithIDs([]int64{id})
	require.NoError(t, err)
	require.Len(t, ps, 1)

	return ps[0]
}

// execSQL runs one statement directly: another writer that edits a row
// in a way no store method does.
func execSQL(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()

	_, err := s.db.Exec(query, args...)
	require.NoError(t, err)
}

// reuseID deletes p and inserts a different proposal that SQLite gives
// the same ID. p must be the newest row, because SQLite reuses only the
// highest rowid when the table has no AUTOINCREMENT.
func reuseID(t *testing.T, s *Store, p model.Proposal) {
	t.Helper()

	execSQL(t, s, `DELETE FROM proposal WHERE id = ?`, p.ID)

	reused := ledgerProposal(t, s, "newcomer", p.Status, p.Regions...)
	require.Equal(t, p.ID, reused.ID, "the new row takes the deleted row's ID")
}

// failUpdatesOf makes every UPDATE of row id fail inside SQLite: an
// injected ledger failure after earlier statements of the same
// transaction succeeded.
func failUpdatesOf(t *testing.T, s *Store, id int64) {
	t.Helper()

	execSQL(t, s, fmt.Sprintf(`CREATE TRIGGER fail_p%d BEFORE UPDATE ON proposal
		WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'injected failure'); END`, id, id))
}

// readOnlyStore seeds a new index file, closes it, and reopens it
// read-only.
func readOnlyStore(t *testing.T, fill func(s *Store)) *Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)
	fill(s)
	require.NoError(t, s.Close())

	ro, err := OpenReadOnly(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })

	return ro
}

func TestProposalsWithIDsReadsAnyStatusInIDOrder(t *testing.T) {
	s := openTestStore(t)

	pending := ledgerProposal(t, s, "pending", model.ProposalProposed, "api")
	applied := ledgerProposal(t, s, "applied", model.ProposalApplied, "api", "web")
	dismissed := ledgerProposal(t, s, "dismissed", model.ProposalDismissed)
	superseded := ledgerProposal(t, s, "superseded", model.ProposalSuperseded, "docs")

	got, err := s.ProposalsWithIDs([]int64{superseded.ID, 999, pending.ID, dismissed.ID, applied.ID, pending.ID})
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{pending, applied, dismissed, superseded}, got,
		"every status, ID order, each row once, missing IDs left out, every field round-trips")

	got, err = s.ProposalsWithIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	// ProposalsByIDs keeps its pending-only contract for the decision flow.
	legacy, err := s.ProposalsByIDs([]int64{pending.ID, applied.ID})
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{pending}, legacy)
}

func TestProposalsWithIDsWorksReadOnly(t *testing.T) {
	var applied model.Proposal

	ro := readOnlyStore(t, func(s *Store) {
		applied = ledgerProposal(t, s, "applied", model.ProposalApplied, "api")
	})

	got, err := ro.ProposalsWithIDs([]int64{applied.ID})
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{applied}, got)
}

func TestProposalsWithIDsSpansChunks(t *testing.T) {
	s := openTestStore(t)

	batch := make([]model.Proposal, pendingChunk+20)
	for i := range batch {
		batch[i] = model.Proposal{Signature: "sig", Rule: fmt.Sprintf("r%d", i), Note: "n",
			Members: []int64{1}, Status: model.ProposalApplied}
	}

	saved, err := s.SaveDistilledGroup("sig", "", 1, batch)
	require.NoError(t, err)

	// The request lists the IDs newest first and repeats the first one.
	ids := make([]int64, 0, len(saved)+1)
	for i := len(saved) - 1; i >= 0; i-- {
		ids = append(ids, saved[i].ID)
	}

	ids = append(ids, saved[0].ID)

	got, err := s.ProposalsWithIDs(ids)
	require.NoError(t, err)
	require.Len(t, got, len(saved), "the list spans several statements")

	for i := range got {
		assert.Equal(t, saved[i].ID, got[i].ID, "ascending ID order across chunks")
	}
}

func TestRowGuardMatchesTheExactRow(t *testing.T) {
	row := model.Proposal{
		ID: 7, Status: model.ProposalApplied, Signature: "sig", Rule: "rule", Region: "api",
		Regions: []string{"api", "web"}, Note: "note", TriggerPaths: []string{"api/a.go"},
		TriggerChecked: 5, TriggerPromptVersion: 3,
	}

	guard := NewRowGuard(row)
	require.True(t, guard.Matches(row))

	tests := []struct {
		name  string
		edit  func(p *model.Proposal)
		match bool
	}{
		{"another ID", func(p *model.Proposal) { p.ID = 8 }, false},
		{"another status", func(p *model.Proposal) { p.Status = model.ProposalSuperseded }, false},
		{"another signature", func(p *model.Proposal) { p.Signature = "other" }, false},
		{"another rule", func(p *model.Proposal) { p.Rule = "Rule" }, false},
		{"another region", func(p *model.Proposal) { p.Region = "web" }, false},
		{"another region set", func(p *model.Proposal) { p.Regions = []string{"api"} }, false},
		{"another region order", func(p *model.Proposal) { p.Regions = []string{"web", "api"} }, false},
		{"another note", func(p *model.Proposal) { p.Note = "edited" }, true},
		{"another trigger stamp", func(p *model.Proposal) { p.TriggerChecked = 9 }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := row
			p.Regions = append([]string(nil), row.Regions...)
			tt.edit(&p)

			assert.Equal(t, tt.match, guard.Matches(p))
		})
	}

	// The guard owns its region list: a later edit of the source row
	// does not move the guard with it.
	row.Regions[0] = "changed"
	assert.Equal(t, []string{"api", "web"}, guard.Regions)

	// The store writes nil and empty lists as one empty column.
	empty := NewRowGuard(model.Proposal{ID: 1, Status: model.ProposalApplied, Regions: []string{}})
	assert.True(t, empty.Matches(model.Proposal{ID: 1, Status: model.ProposalApplied}))
}

func TestGuardErrorNamesTheRows(t *testing.T) {
	var err error = &GuardError{IDs: []int64{3, 12}}

	assert.Equal(t, "store: ledger rows changed since the plan: p3, p12", err.Error())
	require.ErrorIs(t, err, ErrGuard)

	wrapped := fmt.Errorf("maintain: %w", err)

	var ge *GuardError

	require.ErrorAs(t, wrapped, &ge)
	assert.Equal(t, []int64{3, 12}, ge.IDs)
}

func TestRetargetAppliedUpdatesOnlyTheRegions(t *testing.T) {
	s := openTestStore(t)

	first := ledgerProposal(t, s, "first", model.ProposalApplied, "api")
	second := ledgerProposal(t, s, "second", model.ProposalApplied, "web", "docs")
	other := ledgerProposal(t, s, "other", model.ProposalApplied, "api")

	execSQL(t, s, `UPDATE proposal SET trigger_paths = '["api/a.go"]', trigger_checked_at = 5,
		trigger_prompt_version = 3 WHERE id = ?`, first.ID)
	first = proposalRow(t, s, first.ID)

	err := s.RetargetApplied([]RegionChange{
		{Guard: NewRowGuard(first), Region: "api", Regions: []string{"api", "scripts"}},
		{Guard: NewRowGuard(second), Region: "", Regions: nil},
	})
	require.NoError(t, err)

	wantFirst := first
	wantFirst.Regions = []string{"api", "scripts"}

	wantSecond := second
	wantSecond.Region, wantSecond.Regions = "", nil

	assert.Equal(t, []model.Proposal{wantFirst, wantSecond, other}, allProposals(t, s),
		"only region and regions change; status, note, triggers and other rows stay")

	require.NoError(t, s.RetargetApplied(nil), "an empty change list writes nothing")
}

func TestSupersedeExactMovesOnlyTheGuardedRows(t *testing.T) {
	s := openTestStore(t)

	pruned := ledgerProposal(t, s, "pruned", model.ProposalApplied, "api")
	kept := ledgerProposal(t, s, "kept", model.ProposalApplied, "api")

	require.NoError(t, s.SupersedeExact([]RowGuard{NewRowGuard(pruned)}))

	wantPruned := pruned
	wantPruned.Status = model.ProposalSuperseded

	assert.Equal(t, []model.Proposal{wantPruned, kept}, allProposals(t, s))

	// A repeat is a miss: the row is no longer applied.
	err := s.SupersedeExact([]RowGuard{NewRowGuard(pruned)})

	var ge *GuardError

	require.ErrorAs(t, err, &ge)
	assert.Equal(t, []int64{pruned.ID}, ge.IDs)

	require.NoError(t, s.SupersedeExact(nil), "an empty guard list writes nothing")
}

// guardedOps runs the two applied-row writers with one guard each, so
// every guard rule is proven for both. effect applies the one change
// that run makes to a matching row, so a test can build the expected row
// and see any other field change.
var guardedOps = []struct {
	name   string
	run    func(s *Store, guards []RowGuard) error
	effect func(p *model.Proposal)
}{
	{"retarget", func(s *Store, guards []RowGuard) error {
		changes := make([]RegionChange, len(guards))
		for i, g := range guards {
			changes[i] = RegionChange{Guard: g, Region: "moved", Regions: []string{"moved"}}
		}

		return s.RetargetApplied(changes)
	}, func(p *model.Proposal) {
		p.Region, p.Regions = "moved", []string{"moved"}
	}},
	{"supersede", func(s *Store, guards []RowGuard) error {
		return s.SupersedeExact(guards)
	}, func(p *model.Proposal) {
		p.Status = model.ProposalSuperseded
	}},
}

func TestGuardedWritesMissChangedRows(t *testing.T) {
	// Each case changes the target row after the plan read it. The write
	// must then change no row, the kept row included, and name the target.
	cases := []struct {
		name   string
		mutate func(t *testing.T, s *Store, p model.Proposal)
	}{
		{"superseded by another writer", func(t *testing.T, s *Store, p model.Proposal) {
			require.NoError(t, s.SupersedeExact([]RowGuard{NewRowGuard(p)}))
		}},
		{"dismissed", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET status = 'dismissed' WHERE id = ?`, p.ID)
		}},
		{"pending again", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET status = 'proposed' WHERE id = ?`, p.ID)
		}},
		{"retargeted by another writer", func(t *testing.T, s *Store, p model.Proposal) {
			require.NoError(t, s.RetargetApplied([]RegionChange{
				{Guard: NewRowGuard(p), Region: "scripts", Regions: []string{"scripts"}},
			}))
		}},
		{"region field changed alone", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET region = 'web' WHERE id = ?`, p.ID)
		}},
		{"region order changed", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET regions = '["web","api"]' WHERE id = ?`, p.ID)
		}},
		{"rule changed", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET rule = 'renamed' WHERE id = ?`, p.ID)
		}},
		{"signature changed", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET signature = 'other' WHERE id = ?`, p.ID)
		}},
		{"deleted", func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `DELETE FROM proposal WHERE id = ?`, p.ID)
		}},
		{"ID reused by a new row", func(t *testing.T, s *Store, p model.Proposal) {
			reuseID(t, s, p)
		}},
	}

	for _, op := range guardedOps {
		for _, tc := range cases {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				s := openTestStore(t)

				kept := ledgerProposal(t, s, "kept", model.ProposalApplied, "api")
				target := ledgerProposal(t, s, "target", model.ProposalApplied, "api", "web")
				guards := []RowGuard{NewRowGuard(kept), NewRowGuard(target)}

				tc.mutate(t, s, target)
				before := allProposals(t, s)

				err := op.run(s, guards)

				var ge *GuardError

				require.ErrorAs(t, err, &ge)
				assert.Equal(t, []int64{target.ID}, ge.IDs, "only the changed row is named")
				assert.Equal(t, before, allProposals(t, s), "a miss changes no row")
			})
		}
	}
}

func TestGuardedWritesKeepUnguardedFields(t *testing.T) {
	// The note and the trigger fields are not part of the guard: an
	// extraction that stamped an applied row, or a note edit, does not
	// block a reviewed scope change or prune. The write also keeps those
	// edits, because superseded history shows the note and the trigger
	// answer as they were (D-22).
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			target := ledgerProposal(t, s, "target", model.ProposalApplied, "api")
			guard := NewRowGuard(target)

			execSQL(t, s, `UPDATE proposal SET note = 'edited', trigger_paths = '["api/a.go"]',
				trigger_checked_at = 9, trigger_prompt_version = 3 WHERE id = ?`, target.ID)

			edited := proposalRow(t, s, target.ID)
			require.Equal(t, "edited", edited.Note)
			require.Equal(t, []string{"api/a.go"}, edited.TriggerPaths)

			require.NoError(t, op.run(s, []RowGuard{guard}))

			want := edited
			op.effect(&want)

			assert.Equal(t, want, proposalRow(t, s, target.ID),
				"only the intended change is written; the note and the trigger fields stay")
		})
	}
}

func TestGuardedWritesAreAllOrNothing(t *testing.T) {
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			var guards []RowGuard

			for _, rule := range []string{"a", "b", "c", "d"} {
				guards = append(guards, NewRowGuard(ledgerProposal(t, s, rule, model.ProposalApplied, "api")))
			}

			// Two of four rows change after the plan; the request lists
			// them out of order.
			execSQL(t, s, `UPDATE proposal SET status = 'superseded' WHERE id = ?`, guards[3].ID)
			execSQL(t, s, `UPDATE proposal SET region = 'web' WHERE id = ?`, guards[1].ID)

			before := allProposals(t, s)

			err := op.run(s, []RowGuard{guards[3], guards[0], guards[2], guards[1]})

			var ge *GuardError

			require.ErrorAs(t, err, &ge)
			assert.Equal(t, []int64{guards[1].ID, guards[3].ID}, ge.IDs, "every miss, ascending")
			assert.Equal(t, before, allProposals(t, s), "the matching rows stay unchanged too")
		})
	}
}

func TestGuardedWritesRollBackAFailedUpdate(t *testing.T) {
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			first := ledgerProposal(t, s, "first", model.ProposalApplied, "api")
			second := ledgerProposal(t, s, "second", model.ProposalApplied, "api")

			// Both guards match; the second UPDATE fails inside SQLite
			// after the first one succeeded.
			failUpdatesOf(t, s, second.ID)

			before := allProposals(t, s)

			err := op.run(s, []RowGuard{NewRowGuard(first), NewRowGuard(second)})
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrGuard, "a failed write is not a conflict")
			assert.Contains(t, err.Error(), fmt.Sprintf("p%d", second.ID))
			assert.Contains(t, err.Error(), "injected failure")
			assert.Equal(t, before, allProposals(t, s), "the first update rolls back")
		})
	}
}

func TestGuardedWritesRequireAppliedGuards(t *testing.T) {
	for _, op := range guardedOps {
		for _, status := range []string{model.ProposalProposed, model.ProposalDismissed, model.ProposalSuperseded} {
			t.Run(op.name+"/"+status, func(t *testing.T) {
				s := openTestStore(t)

				row := ledgerProposal(t, s, "row", status, "api")
				before := allProposals(t, s)

				// A guard that says the row is not applied is a caller
				// error, not a conflict.
				err := op.run(s, []RowGuard{NewRowGuard(row)})
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrGuard)
				assert.Contains(t, err.Error(), `it needs "applied"`)

				// A guard that says applied for a row in another status
				// is a miss.
				guard := NewRowGuard(row)
				guard.Status = model.ProposalApplied

				err = op.run(s, []RowGuard{guard})
				require.ErrorIs(t, err, ErrGuard)
				assert.Equal(t, before, allProposals(t, s))
			})
		}
	}
}

func TestGuardedWritesRejectARepeatedID(t *testing.T) {
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			row := ledgerProposal(t, s, "row", model.ProposalApplied, "api")
			before := allProposals(t, s)

			err := op.run(s, []RowGuard{NewRowGuard(row), NewRowGuard(row)})
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrGuard)
			assert.Contains(t, err.Error(), fmt.Sprintf("lists p%d twice", row.ID))
			assert.Equal(t, before, allProposals(t, s))
		})
	}
}

func TestGuardedWritesSpanChunks(t *testing.T) {
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			batch := make([]model.Proposal, pendingChunk+20)
			for i := range batch {
				batch[i] = model.Proposal{Signature: "sig", Rule: fmt.Sprintf("r%d", i), Note: "n",
					Members: []int64{1}, Status: model.ProposalApplied, Region: "api"}
			}

			saved, err := s.SaveDistilledGroup("sig", "", 1, batch)
			require.NoError(t, err)

			guards := make([]RowGuard, len(saved))
			for i, p := range saved {
				guards[i] = NewRowGuard(p)
			}

			// One miss in the second chunk refuses the whole list.
			last := saved[len(saved)-1]
			execSQL(t, s, `UPDATE proposal SET rule = 'renamed' WHERE id = ?`, last.ID)

			before := allProposals(t, s)

			err = op.run(s, guards)

			var ge *GuardError

			require.ErrorAs(t, err, &ge)
			assert.Equal(t, []int64{last.ID}, ge.IDs)
			assert.Equal(t, before, allProposals(t, s))

			// Without the miss, every listed row of every chunk is written.
			require.NoError(t, op.run(s, guards[:len(guards)-1]))

			after := allProposals(t, s)
			require.Len(t, after, len(before))

			for i := range len(after) - 1 {
				assert.NotEqual(t, before[i], after[i], "p%d is written", after[i].ID)
			}

			assert.Equal(t, before[len(before)-1], after[len(after)-1], "the unlisted row stays")
		})
	}
}

func TestGuardsCompareDecodedValues(t *testing.T) {
	s := openTestStore(t)

	row := ledgerProposal(t, s, "row", model.ProposalApplied, "api", "web")
	guard := NewRowGuard(row)

	// Another writer stored the same list with other JSON spacing. The
	// decoded values are equal, so the guard still matches.
	execSQL(t, s, `UPDATE proposal SET regions = '[ "api" , "web" ]' WHERE id = ?`, row.ID)

	require.NoError(t, s.RetargetApplied([]RegionChange{{Guard: guard, Region: "api", Regions: []string{"api"}}}))

	// A legacy empty JSON list decodes as an empty list and matches a
	// guard without regions.
	execSQL(t, s, `UPDATE proposal SET regions = '[]', region = '' WHERE id = ?`, row.ID)

	bare := guard
	bare.Region, bare.Regions = "", nil

	require.NoError(t, s.SupersedeExact([]RowGuard{bare}))

	// The trigger stamp is compared decoded too.
	pending := ledgerProposal(t, s, "pending", model.ProposalProposed, "api")
	execSQL(t, s, `UPDATE proposal SET trigger_paths = '[ "api/a.go" ]', trigger_checked_at = 4,
		trigger_prompt_version = 2 WHERE id = ?`, pending.ID)

	saved, err := s.SaveTriggerAnswer(TriggerAnswer{
		Guard: NewRowGuard(pending), PriorChecked: 4, PriorVersion: 2, PriorPaths: []string{"api/a.go"},
		Paths: []string{"api/b.go"}, CheckedAt: 10, Version: 3,
	})
	require.NoError(t, err)
	assert.True(t, saved)
}

func TestGuardedWritesFailOnAnUnreadableRow(t *testing.T) {
	// A row whose stored JSON cannot be decoded is an error, never a
	// missing row: error is not absence.
	s := openTestStore(t)

	row := ledgerProposal(t, s, "row", model.ProposalApplied, "api")
	execSQL(t, s, `UPDATE proposal SET members = 'not json' WHERE id = ?`, row.ID)

	_, err := s.ProposalsWithIDs([]int64{row.ID})
	require.Error(t, err)

	for _, op := range guardedOps {
		err := op.run(s, []RowGuard{NewRowGuard(row)})
		require.Error(t, err, op.name)
		assert.NotErrorIs(t, err, ErrGuard, op.name)
	}

	_, err = s.SaveTriggerAnswer(TriggerAnswer{Guard: NewRowGuard(row), CheckedAt: 1, Version: 3})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrGuard)
}

func TestGuardedWritesFailOnAReadOnlyStore(t *testing.T) {
	var applied, pending model.Proposal

	ro := readOnlyStore(t, func(s *Store) {
		applied = ledgerProposal(t, s, "applied", model.ProposalApplied, "api")
		pending = ledgerProposal(t, s, "pending", model.ProposalProposed, "api")
	})

	for _, op := range guardedOps {
		err := op.run(ro, []RowGuard{NewRowGuard(applied)})
		require.Error(t, err, op.name)
		assert.NotErrorIs(t, err, ErrGuard, op.name)
	}

	saved, err := ro.SaveTriggerAnswer(TriggerAnswer{Guard: NewRowGuard(pending), CheckedAt: 1, Version: 3})
	require.Error(t, err)
	assert.False(t, saved)

	got, err := ro.ProposalsWithIDs([]int64{applied.ID, pending.ID})
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{applied, pending}, got, "nothing changed")

	// Empty lists never open a transaction, so they succeed even here.
	require.NoError(t, ro.RetargetApplied(nil))
	require.NoError(t, ro.SupersedeExact(nil))
}

// sharedStores opens two stores on one new index file. The writer stands
// for another process, and the console is the store under test.
func sharedStores(t *testing.T) (writer, console *Store) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "index.db")

	writer, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })

	console, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = console.Close() })

	return writer, console
}

// holdWriteLock runs one statement in an open transaction of writer.
// The writer then holds the write lock, and other connections cannot see
// the change yet. A goroutine commits after 100 ms and sends the result
// on the returned channel. The writer's only connection stays busy until
// then, so the caller must not use writer before it reads the channel.
func holdWriteLock(t *testing.T, writer *Store, query string, args ...any) <-chan error {
	t.Helper()

	tx, err := writer.db.Begin()
	require.NoError(t, err)

	_, err = tx.Exec(query, args...)
	require.NoError(t, err)

	committed := make(chan error, 1)

	go func() {
		time.Sleep(100 * time.Millisecond)
		committed <- tx.Commit()
	}()

	return committed
}

func TestGuardedWritesWaitForAnotherWriter(t *testing.T) {
	// Another process holds the write lock for a short time. A guarded
	// write waits for it like every other write. A transaction that read
	// first would fail at once with "database is locked" instead.
	writer, console := sharedStores(t)

	row := ledgerProposal(t, writer, "row", model.ProposalApplied, "api")

	committed := holdWriteLock(t, writer, `INSERT INTO proposal (signature, rule, note, members, status)
		VALUES ('other', 'other', 'n', '[]', 'proposed')`)

	err := console.RetargetApplied([]RegionChange{{Guard: NewRowGuard(row), Region: "web", Regions: []string{"web"}}})
	require.NoError(t, <-committed)
	require.NoError(t, err)

	assert.Equal(t, []string{"web"}, proposalRow(t, console, row.ID).Regions)
}

func TestGuardedWritesReadTheGuardInsideTheLock(t *testing.T) {
	// Another process supersedes the guarded row in a transaction that is
	// still open when the console write starts. A guard read before the
	// lock still sees the applied row, because the change is not
	// committed. The guard must read after the lock, see the change, and
	// write nothing (D-11).
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			writer, console := sharedStores(t)

			row := ledgerProposal(t, writer, "row", model.ProposalApplied, "api")
			guard := NewRowGuard(row)

			committed := holdWriteLock(t, writer, `UPDATE proposal SET status = 'superseded' WHERE id = ?`, row.ID)

			err := op.run(console, []RowGuard{guard})
			require.NoError(t, <-committed)

			var ge *GuardError

			require.ErrorAs(t, err, &ge, "the row changed while the console waited for the lock")
			assert.Equal(t, []int64{row.ID}, ge.IDs)

			want := row
			want.Status = model.ProposalSuperseded

			assert.Equal(t, want, proposalRow(t, console, row.ID), "the other writer's row stays as it committed it")
		})
	}
}

func TestSaveTriggerAnswerReadsTheRowInsideTheLock(t *testing.T) {
	// Another process applies the pending row in a transaction that is
	// still open when the answer is saved. A read before the lock still
	// sees a pending row. The answer must read after the lock and write
	// nothing, because an applied row never takes regions from an
	// extraction (AC-10).
	writer, console := sharedStores(t)

	row := ledgerProposal(t, writer, "row", model.ProposalProposed, "api")

	a := answerFor(row, "api/a.go")
	a.SetRegions, a.Region, a.Regions = true, "web", []string{"web"}

	committed := holdWriteLock(t, writer, `UPDATE proposal SET status = 'applied' WHERE id = ?`, row.ID)

	saved, err := console.SaveTriggerAnswer(a)
	require.NoError(t, <-committed)
	require.NoError(t, err, "a conflict is a result, not an error")
	assert.False(t, saved)

	want := row
	want.Status = model.ProposalApplied

	assert.Equal(t, want, proposalRow(t, console, row.ID), "no regions and no trigger answer are written")
}

// answerFor returns an answer bound to p as it is now.
func answerFor(p model.Proposal, paths ...string) TriggerAnswer {
	return TriggerAnswer{
		Guard: NewRowGuard(p), PriorChecked: p.TriggerChecked, PriorVersion: p.TriggerPromptVersion,
		PriorPaths: p.TriggerPaths, Paths: paths, CheckedAt: 1700000900, Version: 3,
	}
}

func TestSaveTriggerAnswerWritesOneCompletionMarker(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		setRegions bool
		paths      []string
		wantRegion string
		wantSet    []string
	}{
		{"pending takes the regions", model.ProposalProposed, true, []string{"api/a.go"}, "scripts", []string{"scripts", "api"}},
		{"pending without SetRegions keeps them", model.ProposalProposed, false, []string{"api/a.go"}, "web", []string{"web"}},
		{"applied never takes regions", model.ProposalApplied, true, []string{"api/a.go"}, "web", []string{"web"}},
		{"a negative answer is stamped", model.ProposalProposed, true, nil, "scripts", []string{"scripts", "api"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)

			row := ledgerProposal(t, s, "row", tt.status, "web")
			other := ledgerProposal(t, s, "other", tt.status, "web")

			a := answerFor(row, tt.paths...)
			a.SetRegions, a.Region, a.Regions = tt.setRegions, "scripts", []string{"scripts", "api"}

			saved, err := s.SaveTriggerAnswer(a)
			require.NoError(t, err)
			require.True(t, saved)

			want := row
			want.TriggerPaths, want.TriggerChecked, want.TriggerPromptVersion = tt.paths, 1700000900, 3
			want.Region, want.Regions = tt.wantRegion, tt.wantSet

			assert.Equal(t, []model.Proposal{want, other}, allProposals(t, s))
		})
	}
}

func TestSaveTriggerAnswerReasksAnObsoleteNegative(t *testing.T) {
	// A negative answer to an older question carries a non-zero stamp.
	// The prior fields bind that stamp, and the new answer replaces it.
	s := openTestStore(t)

	row := ledgerProposal(t, s, "row", model.ProposalApplied, "api")

	old := answerFor(row)
	old.CheckedAt, old.Version = 100, 1

	saved, err := s.SaveTriggerAnswer(old)
	require.NoError(t, err)
	require.True(t, saved)

	row = proposalRow(t, s, row.ID)

	saved, err = s.SaveTriggerAnswer(answerFor(row, "api/a.go"))
	require.NoError(t, err)
	require.True(t, saved)

	got := proposalRow(t, s, row.ID)
	assert.Equal(t, []string{"api/a.go"}, got.TriggerPaths)
	assert.Equal(t, int64(1700000900), got.TriggerChecked)
	assert.Equal(t, 3, got.TriggerPromptVersion)
}

func TestSaveTriggerAnswerRefusesChangedRows(t *testing.T) {
	// Each case changes the row during the agent call. The paid answer
	// is then a conflict: no write at all, the regions included.
	tests := []struct {
		name   string
		status string
		mutate func(t *testing.T, s *Store, p model.Proposal)
	}{
		{"applied during the call", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			_, err := s.SetProposalStatus([]int64{p.ID}, model.ProposalApplied)
			require.NoError(t, err)
		}},
		{"dismissed during the call", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			_, err := s.SetProposalStatus([]int64{p.ID}, model.ProposalDismissed)
			require.NoError(t, err)
		}},
		{"superseded during the call", model.ProposalApplied, func(t *testing.T, s *Store, p model.Proposal) {
			require.NoError(t, s.SupersedeExact([]RowGuard{NewRowGuard(p)}))
		}},
		{"deleted by a cleanup", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			_, err := s.DeletePendingProposals([]int64{p.ID})
			require.NoError(t, err)
		}},
		{"ID reused after a cleanup", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			_, err := s.DeletePendingProposals([]int64{p.ID})
			require.NoError(t, err)
			reused := ledgerProposal(t, s, "newcomer", model.ProposalProposed, "web")
			require.Equal(t, p.ID, reused.ID)
		}},
		{"pending regions changed", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET region = 'docs', regions = '["docs"]' WHERE id = ?`, p.ID)
		}},
		{"applied row retargeted", model.ProposalApplied, func(t *testing.T, s *Store, p model.Proposal) {
			require.NoError(t, s.RetargetApplied([]RegionChange{
				{Guard: NewRowGuard(p), Region: "docs", Regions: []string{"docs"}},
			}))
		}},
		{"stamped by another extraction", model.ProposalProposed, func(t *testing.T, s *Store, p model.Proposal) {
			other := answerFor(p)
			other.CheckedAt = 50

			saved, err := s.SaveTriggerAnswer(other)
			require.NoError(t, err)
			require.True(t, saved)
		}},
		{"prompt version changed alone", model.ProposalApplied, func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET trigger_prompt_version = 2 WHERE id = ?`, p.ID)
		}},
		{"stored paths changed alone", model.ProposalApplied, func(t *testing.T, s *Store, p model.Proposal) {
			execSQL(t, s, `UPDATE proposal SET trigger_paths = '["api/x.go"]' WHERE id = ?`, p.ID)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)

			row := ledgerProposal(t, s, "row", tt.status, "web")

			a := answerFor(row, "api/a.go")
			a.SetRegions, a.Region, a.Regions = true, "api", []string{"api"}

			tt.mutate(t, s, row)
			before := allProposals(t, s)

			saved, err := s.SaveTriggerAnswer(a)
			require.NoError(t, err, "a conflict is a result, not an error")
			assert.False(t, saved)
			assert.Equal(t, before, allProposals(t, s), "nothing is written")
		})
	}
}

func TestSaveTriggerAnswerRejectsInvalidAnswers(t *testing.T) {
	s := openTestStore(t)

	row := ledgerProposal(t, s, "row", model.ProposalProposed, "api")
	before := allProposals(t, s)

	tooMany := answerFor(row, "a.go", "b.go", "c.go", "d.go")

	saved, err := s.SaveTriggerAnswer(tooMany)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum is 3")
	assert.False(t, saved)

	for _, status := range []string{model.ProposalDismissed, model.ProposalSuperseded, ""} {
		a := answerFor(row, "a.go")
		a.Guard.Status = status

		saved, err := s.SaveTriggerAnswer(a)
		require.Error(t, err, "status %q", status)
		assert.NotErrorIs(t, err, ErrGuard)
		assert.False(t, saved)
	}

	// A stamp or a prompt version of zero or less reads as "never asked".
	// The write would not mark completion, so it is a caller error. The
	// zero time.Time gives a negative Unix stamp, as a broken clock does.
	stamps := []struct {
		name      string
		checkedAt int64
		version   int
	}{
		{"zero stamp", 0, 3},
		{"zero time", time.Time{}.Unix(), 3},
		{"zero version", 1700000900, 0},
		{"negative version", 1700000900, -1},
	}

	for _, tt := range stamps {
		a := answerFor(row) // a negative answer: the stamp is its only trace
		a.CheckedAt, a.Version = tt.checkedAt, tt.version

		saved, err := s.SaveTriggerAnswer(a)
		require.Error(t, err, tt.name)
		assert.NotErrorIs(t, err, ErrGuard, tt.name)
		assert.Contains(t, err.Error(), "both must be positive", tt.name)
		assert.False(t, saved, tt.name)
	}

	assert.Equal(t, before, allProposals(t, s))
}

func TestSaveTriggerAnswerRollsBackAFailedWrite(t *testing.T) {
	s := openTestStore(t)

	row := ledgerProposal(t, s, "row", model.ProposalProposed, "api")
	failUpdatesOf(t, s, row.ID)

	before := allProposals(t, s)

	a := answerFor(row, "api/a.go")
	a.SetRegions, a.Region, a.Regions = true, "web", []string{"web"}

	saved, err := s.SaveTriggerAnswer(a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected failure")
	assert.False(t, saved)
	assert.Equal(t, before, allProposals(t, s))

	// The connection is clean after the rollback: the next write works.
	other := ledgerProposal(t, s, "other", model.ProposalProposed, "api")

	saved, err = s.SaveTriggerAnswer(answerFor(other, "api/a.go"))
	require.NoError(t, err)
	assert.True(t, saved)
}
