package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// These tests cover the scope choice of the ledger: the stamp that says
// a person chose the regions of a row. Only the guarded writers and the
// state bundle move it, so the tests cover each of them.

const chosenAt int64 = 1_800_000_000

// chooseScope stamps row p as if a person chose its regions and returns
// the row as stored.
func chooseScope(t *testing.T, s *Store, p model.Proposal) model.Proposal {
	t.Helper()

	execSQL(t, s, `UPDATE proposal SET scope_chosen_at = ? WHERE id = ?`, chosenAt, p.ID)

	return proposalRow(t, s, p.ID)
}

// requireGuardError requires a *GuardError that names exactly ids.
func requireGuardError(t *testing.T, err error, ids ...int64) {
	t.Helper()

	var ge *GuardError

	require.ErrorAs(t, err, &ge)
	require.ErrorIs(t, err, ErrGuard)
	assert.Equal(t, ids, ge.IDs)
}

func TestRowGuardBindsTheScopeChoice(t *testing.T) {
	row := model.Proposal{ID: 7, Status: model.ProposalApplied, ScopeChosen: chosenAt}

	assert.Equal(t, chosenAt, NewRowGuard(row).ScopeChosen)

	// A plan that predates a choice must not write over the choice.
	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			s := openTestStore(t)

			target := ledgerProposal(t, s, "target", model.ProposalApplied, "api")
			guard := NewRowGuard(target)

			chooseScope(t, s, target)

			before := allProposals(t, s)

			requireGuardError(t, op.run(s, []RowGuard{guard}), target.ID)
			assert.Equal(t, before, allProposals(t, s), "a stale plan changes no row")
		})
	}

	t.Run("trigger answer", func(t *testing.T) {
		s := openTestStore(t)

		target := ledgerProposal(t, s, "target", model.ProposalApplied, "api")
		answer := answerFor(target, "api/a.go")

		chooseScope(t, s, target)

		saved, err := s.SaveTriggerAnswer(answer)
		require.NoError(t, err)
		assert.False(t, saved, "an answer that predates a choice is a conflict")
	})
}

func TestRetargetAppliedWritesTheScopeChoice(t *testing.T) {
	s := openTestStore(t)

	target := ledgerProposal(t, s, "target", model.ProposalApplied, "api")
	other := ledgerProposal(t, s, "other", model.ProposalApplied, "web")

	// A manual scope edit sets the time.
	require.NoError(t, s.RetargetApplied([]RegionChange{{
		Guard: NewRowGuard(target), Region: "docs", Regions: []string{"docs", "api"}, Chosen: chosenAt,
	}}))

	want := target
	want.Region, want.Regions, want.ScopeChosen = "docs", []string{"docs", "api"}, chosenAt

	assert.Equal(t, []model.Proposal{want, other}, allProposals(t, s),
		"only the regions and the stamp of the target change")

	// A reviewed computed retarget replaces the choice, so it clears the stamp.
	require.NoError(t, s.RetargetApplied([]RegionChange{{
		Guard: NewRowGuard(want), Region: "api", Regions: []string{"api"},
	}}))

	want.Region, want.Regions, want.ScopeChosen = "api", []string{"api"}, 0

	assert.Equal(t, want, proposalRow(t, s, target.ID))
}

// The console and the CLI print the error of a region write as it is. Each
// route keeps its own words there, and the CLI goldens hold the words of a
// retarget.
func TestRegionWriteErrorsNameTheirRoute(t *testing.T) {
	tests := []struct {
		name          string
		chosen        int64
		begin, update string
	}{
		{"a manual scope edit", chosenAt, "store: begin guarded scope edit: ", "store: scope edit p%d: "},
		{"a computed retarget", 0, "store: begin guarded retarget: ", "store: retarget p%d: "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			target := ledgerProposal(t, s, "target", model.ProposalApplied, "api")
			edit := []RegionChange{{Guard: NewRowGuard(target), Region: "docs", Regions: []string{"docs"}, Chosen: tt.chosen}}

			execSQL(t, s, `CREATE TRIGGER fail_region BEFORE UPDATE OF region ON proposal
BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`)

			assert.ErrorContains(t, s.RetargetApplied(edit), fmt.Sprintf(tt.update, target.ID))

			// A closed store fails before the transaction starts, so no lock
			// wait slows the test.
			require.NoError(t, s.Close())
			assert.ErrorContains(t, s.RetargetApplied(edit), tt.begin)
		})
	}
}

func TestSupersedeExactKeepsTheScopeChoice(t *testing.T) {
	s := openTestStore(t)

	target := chooseScope(t, s, ledgerProposal(t, s, "target", model.ProposalApplied, "api"))

	require.NoError(t, s.SupersedeExact([]RowGuard{NewRowGuard(target)}))

	want := target
	want.Status = model.ProposalSuperseded
	assert.Equal(t, want, proposalRow(t, s, target.ID), "a prune keeps the stamp as history")
}

func TestSaveTriggerAnswerKeepsTheScopeChoice(t *testing.T) {
	s := openTestStore(t)

	target := chooseScope(t, s, ledgerProposal(t, s, "target", model.ProposalApplied, "api"))

	answer := answerFor(target, "web/a.go")
	answer.SetRegions, answer.Region, answer.Regions = true, "web", []string{"web"}

	saved, err := s.SaveTriggerAnswer(answer)
	require.NoError(t, err)
	require.True(t, saved)

	got := proposalRow(t, s, target.ID)
	assert.Equal(t, []string{"api"}, got.Regions, "trigger extraction never changes an applied scope")
	assert.Equal(t, chosenAt, got.ScopeChosen, "trigger extraction never writes the stamp")
}

func TestStateKeepsTheScopeChoice(t *testing.T) {
	// bundle returns one row of the wire form with the given stamp.
	bundle := func(version int, stamp int64) *State {
		return &State{Version: version, Proposals: []ProposalState{{
			Signature: "sig", Rule: "r", Note: "n", Region: "api", Regions: []string{"api", "web"},
			Status: model.ProposalApplied, CreatedAt: 1, ScopeChosen: stamp,
		}}}
	}

	t.Run("an export and an import round trip", func(t *testing.T) {
		src := openTestStore(t)

		plain := ledgerProposal(t, src, "plain", model.ProposalApplied, "api")
		chooseScope(t, src, ledgerProposal(t, src, "chosen", model.ProposalApplied, "web"))

		state, err := src.ExportState()
		require.NoError(t, err)
		assert.Equal(t, 4, state.Version)

		wire, err := json.Marshal(state)
		require.NoError(t, err)
		assert.Contains(t, string(wire), fmt.Sprintf(`"scope_chosen_at":%d`, chosenAt))

		var back State

		require.NoError(t, json.Unmarshal(wire, &back))

		dst := openTestStore(t)

		stats, err := dst.ImportState(&back)
		require.NoError(t, err)
		assert.Equal(t, 2, stats.ProposalsAdded)

		rows := allProposals(t, dst)
		require.Len(t, rows, 2)
		assert.Equal(t, plain.Rule, rows[0].Rule)
		assert.Zero(t, rows[0].ScopeChosen)
		assert.Equal(t, chosenAt, rows[1].ScopeChosen)

		plainOnly, err := json.Marshal(ProposalState{Signature: "sig", Rule: "r"})
		require.NoError(t, err)
		assert.NotContains(t, string(plainOnly), "scope_chosen_at", "a row with no choice writes no key")
	})

	t.Run("an adopted decision takes the stamp with the regions", func(t *testing.T) {
		s := openTestStore(t)

		require.NoError(t, s.InsertProposal(&model.Proposal{
			Signature: "sig", Rule: "r", Note: "n", Status: model.ProposalProposed, CreatedAt: 1,
		}))

		stats, err := s.ImportState(bundle(StateVersion, chosenAt))
		require.NoError(t, err)
		assert.Equal(t, 1, stats.ProposalsUpdated)

		rows := allProposals(t, s)
		require.Len(t, rows, 1)
		assert.Equal(t, []string{"api", "web"}, rows[0].Regions)
		assert.Equal(t, chosenAt, rows[0].ScopeChosen)
	})

	t.Run("a local decision keeps its own stamp", func(t *testing.T) {
		for _, tt := range []struct {
			name        string
			local, wire int64
		}{
			{"a local choice stays under a bundle without one", chosenAt, 0},
			{"a local row takes no imported choice", 0, chosenAt},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s := openTestStore(t)

				require.NoError(t, s.InsertProposal(&model.Proposal{
					Signature: "sig", Rule: "r", Note: "n", Status: model.ProposalApplied,
					CreatedAt: 1, ScopeChosen: tt.local,
				}))

				stats, err := s.ImportState(bundle(StateVersion, tt.wire))
				require.NoError(t, err)
				assert.Equal(t, 1, stats.ProposalsSkipped)

				rows := allProposals(t, s)
				require.Len(t, rows, 1)
				assert.Equal(t, tt.local, rows[0].ScopeChosen)
			})
		}
	})

	t.Run("a row of an older bundle reads as no choice", func(t *testing.T) {
		for version := 1; version <= 3; version++ {
			wire := fmt.Sprintf(`{"seamark_state_version":%d,"proposals":[`+
				`{"signature":"sig","rule":"r","note":"n","status":"applied","created_at":1}]}`, version)

			var old State

			require.NoError(t, json.Unmarshal([]byte(wire), &old))

			s := openTestStore(t)

			stats, err := s.ImportState(&old)
			require.NoError(t, err)
			assert.Equal(t, 1, stats.ProposalsAdded)

			rows := allProposals(t, s)
			require.Len(t, rows, 1)
			assert.Zero(t, rows[0].ScopeChosen)
		}
	})
}
