package maintain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// Tests of the rule that an inferred scope replaces a chosen scope only
// in a console preview. A request carries ReplaceChosen only from such a
// preview, so a plan without it refuses the replacement.

// choose records that a person chose the regions of row id.
func (f *fixture) choose(id int64) {
	f.t.Helper()

	f.exec(fmt.Sprintf(`UPDATE proposal SET scope_chosen_at = %d WHERE id = %d`, scopeAt, id))
}

func TestRetargetRefusesAChosenRow(t *testing.T) {
	t.Run("a computed scope that differs blocks the row", func(t *testing.T) {
		f := retargetFixture(t)
		f.choose(1)

		lessons, ledger := f.lessons(), f.ledger()

		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
		require.Len(t, p.Items, 1)
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Equal(t, reasonChosen, p.Items[0].Reason)
		assert.Nil(t, p.Change)
		assert.Empty(t, p.Retarget)
		assertUnwritten(t, f, p, lessons, ledger)
	})

	t.Run("the other kinds keep their own lines", func(t *testing.T) {
		f := retargetFixture(t)
		f.choose(2)
		f.choose(3)

		p := f.plan(f.deps(), f.request(ActionRetarget, "already-current", "aged-out"))
		require.Len(t, p.Items, 2)
		assert.Equal(t, ItemNoOp, p.Items[0].Status)
		assert.Equal(t, reasonCurrent, p.Items[0].Reason)
		assert.Equal(t, reasonNoEvidence, p.Items[1].Reason)
	})

	t.Run("a row with no choice stays ready", func(t *testing.T) {
		f := retargetFixture(t)
		f.choose(1)

		p := f.plan(f.deps(), f.request(ActionRetarget, "two-areas"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
	})
}

// reasonChosen names the key w, so a row shows it only where the console
// offers that key. The other refusals keep the reason that stops w, and
// they end with the route of the key i.
func TestChosenRowKeepsTheReasonThatStopsTheRetargetKey(t *testing.T) {
	const anchor = "  - rule: already-current\n"

	cases := []struct {
		name  string
		setup func(f *fixture)
		want  string
	}{
		{
			name: "another pin owns the computed key",
			setup: func(f *fixture) {
				f.write(strings.Replace(retargetLessons, anchor,
					"  - rule: guard-empty-datasets\n    region: scripts\n    note: A hand-written twin.\n"+anchor, 1), 0o600)
			},
			want: "the target key is already in use: another pin already carries the key guard-empty-datasets @ scripts",
		},
		{
			name: "an applied row owns the computed key",
			setup: func(f *fixture) {
				f.propose(model.Proposal{Signature: "s7", Rule: "guard-empty-datasets", Region: "scripts", Note: "Other.",
					Members: []int64{1}, Status: model.ProposalApplied})
			},
			want: "the target key is already in use: an applied proposal already records the key " +
				"guard-empty-datasets @ scripts",
		},
		{
			name: "the file gate refuses every edit",
			setup: func(f *fixture) {
				f.write(strings.ReplaceAll(retargetLessons, "\n", "\r\n"), 0o600)
			},
			want: textFileBlock + "the file uses CRLF line breaks",
		},
		{
			name: "the file cannot be replaced",
			setup: func(f *fixture) {
				f.write(retargetLessons, 0o444)
			},
			want: textFileBlock + "the file is read-only (-r--r--r--); seamark does not replace it",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := retargetFixture(t)
			f.choose(1)
			c.setup(f)

			lessons, ledger := f.lessons(), f.ledger()

			p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
			require.Len(t, p.Items, 1)
			assert.Equal(t, ItemBlocked, p.Items[0].Status)
			assert.Equal(t, c.want+"; "+routeChosenForm, p.Items[0].Reason)
			assert.Nil(t, p.Change)
			assert.Empty(t, p.Retarget)
			assertUnwritten(t, f, p, lessons, ledger)
		})
	}
}

// twinFixture holds two pins of one rule that compute one key. Row 1 is
// chosen, and it is the entry in region web.
func twinFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, retargetFindings)
	f.write(`pin:
  - rule: twin-rule
    region: web
    note: First.
  - rule: twin-rule
    region: lib
    note: Second.
`, 0o644)
	f.propose(
		model.Proposal{Signature: "a", Rule: "twin-rule", Region: "web", Note: "First.", Members: []int64{1, 2},
			Status: model.ProposalApplied},
		model.Proposal{Signature: "b", Rule: "twin-rule", Region: "lib", Note: "Second.", Members: []int64{1, 2},
			Status: model.ProposalApplied},
	)
	f.choose(1)

	return f
}

// A chosen row writes nothing, so another selected entry with the same
// computed key is no collision. The console reads w per row and gives the
// same answer, so reasonChosen stays true for the chosen row.
func TestChosenRowIsPlannedAloneAgainstTheSelection(t *testing.T) {
	f := twinFixture(t)

	p := f.plan(f.deps(), f.request(ActionRetarget, "twin-rule@web", "twin-rule@lib"))
	require.Len(t, p.Items, 2)
	assert.Equal(t, ItemBlocked, p.Items[0].Status)
	assert.Equal(t, reasonChosen, p.Items[0].Reason)
	assert.Equal(t, ItemReady, p.Items[1].Status)

	require.NotNil(t, p.Change)
	assert.Len(t, p.Change.Edits, 1)
	assert.False(t, p.Confirmable, "a mixed selection never acts on a subset")
}

// The same selection with the chosen row second. The other entry now plans
// first, so a plan that read its edit would report a collision here.
func TestChosenRowIsPlannedAloneAfterAnEarlierEntry(t *testing.T) {
	f := twinFixture(t)

	p := f.plan(f.deps(), f.request(ActionRetarget, "twin-rule@lib", "twin-rule@web"))
	require.Len(t, p.Items, 2)
	assert.Equal(t, ItemReady, p.Items[0].Status)
	assert.Equal(t, ItemBlocked, p.Items[1].Status)
	assert.Equal(t, reasonChosen, p.Items[1].Reason)

	require.NotNil(t, p.Change)
	assert.Len(t, p.Change.Edits, 1)
	assert.False(t, p.Confirmable, "a mixed selection never acts on a subset")
}

func TestRetargetWithReplaceChosenClearsTheStamp(t *testing.T) {
	f := retargetFixture(t)
	f.choose(1)

	req := f.request(ActionRetarget, "Guard-Empty-Datasets")
	req.ReplaceChosen = true

	p := f.plan(f.deps(), req)
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
	assert.Contains(t, p.Notes, fmt.Sprintf(noteChoiceReplaced, 1, model.ScopeChoiceDate(scopeAt)))
	assert.Zero(t, p.Retarget[0].Chosen)

	res, err := Commit(f.deps(), p)
	require.NoError(t, err)
	require.True(t, res.LedgerDone, "ledger error: %v", res.LedgerErr)

	row := f.row(1)
	assert.Equal(t, []string{"scripts"}, row.RegionSet())
	assert.Zero(t, row.ScopeChosen, "the reviewed computed scope ends the choice")
}

func TestRetargetPlanOfBeforeAChoiceGoesStale(t *testing.T) {
	f := retargetFixture(t)

	req := f.request(ActionRetarget, "Guard-Empty-Datasets")
	p := f.plan(f.deps(), req)
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

	f.choose(1)

	_, err := Commit(f.deps(), p)
	require.ErrorIs(t, err, ErrStale, "a plan that predates a choice writes nothing")
}

// Only a scope edit accepts another stamp, because only a person at another
// console writes the same regions with another time. Any other stamp on the
// row of a retarget means that another change ran.
func TestRowsHoldPlannedAcceptsAnotherStampOnlyForAScopeEdit(t *testing.T) {
	const other = scopeAt + 60

	t.Run("a retarget", func(t *testing.T) {
		f := retargetFixture(t)

		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

		change := p.Retarget[0]
		change.Chosen = other
		require.NoError(t, f.st.RetargetApplied([]store.RegionChange{change}))

		_, target := RowsHoldPlanned(p, f.rows())
		assert.False(t, target)
	})

	t.Run("a scope edit", func(t *testing.T) {
		f := authorFixture(t)

		p := f.scopePlan(f.deps(), f.linkedDraft("web"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

		change := p.Retarget[0]
		change.Chosen = other
		require.NoError(t, f.st.RetargetApplied([]store.RegionChange{change}))

		_, target := RowsHoldPlanned(p, f.rows())
		assert.True(t, target)
	})
}
