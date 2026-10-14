package pinfile

// Tests of the key owners of the authoring of pins.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestKeyOwnersListsEntriesAndRowsOfTheKey(t *testing.T) {
	const file = `pin:
  - rule: a
    region: api
  - rule: A
    region: api/
  - rule: b
    region: db
`
	d := Parse([]byte(file))
	first, second := d.Entries[0], d.Entries[1]
	key := reviews.NewPinKey("a", "api", nil)

	applied := []model.Proposal{
		{ID: 1, Rule: "a", Region: "api", Status: model.ProposalApplied},
		{ID: 2, Rule: "b", Region: "db", Status: model.ProposalApplied},
		// The fields disagree: the apply wrote the pin of the set {api},
		// and the link key also holds db. The row owns both keys.
		{ID: 3, Rule: "a", Region: "db", Regions: []string{"api"}, Status: model.ProposalApplied},
	}
	pending := []model.Proposal{
		{ID: 4, Rule: "A", Region: "api", Status: model.ProposalProposed},
		{ID: 5, Rule: "a", Region: "web", Status: model.ProposalProposed},
		// A pending row whose rendered key is a @ api.
		{ID: 6, Rule: "a", Region: "lib", Regions: []string{"api", "*"}, Status: model.ProposalProposed},
	}

	require.False(t, FieldsAgree(applied[2]))
	require.False(t, FieldsAgree(pending[2]))

	t.Run("every owner", func(t *testing.T) {
		o := KeyOwners(d, applied, pending, key, nil)

		assert.Equal(t, []Entry{first, second}, o.Entries)
		assert.Equal(t, []model.Proposal{applied[0], applied[2]}, o.Applied, "row 3 owns the key of the pin that its apply wrote")
		assert.Equal(t, []model.Proposal{pending[0]}, o.Pending)
		assert.True(t, o.Any())
	})

	t.Run("the edited entry is not its own owner", func(t *testing.T) {
		ref := first.Ref()
		o := KeyOwners(d, applied, pending, key, &ref)

		assert.Equal(t, []Entry{second}, o.Entries)
	})

	t.Run("a disagreeing row also owns its link key", func(t *testing.T) {
		o := KeyOwners(d, applied, pending, reviews.NewPinKey("a", "db", []string{"api"}), nil)
		assert.Empty(t, o.Entries)
		assert.Equal(t, []model.Proposal{applied[2]}, o.Applied)

		o = KeyOwners(d, nil, pending, reviews.NewPinKey("a", "", nil), nil)
		assert.Equal(t, []model.Proposal{pending[2]}, o.Pending, "the rendered key of a set with * is repository-wide")

		o = KeyOwners(d, nil, pending, reviews.NewPinKey("a", "lib", []string{"api"}), nil)
		assert.Equal(t, []model.Proposal{pending[2]}, o.Pending)
	})

	t.Run("a free key", func(t *testing.T) {
		o := KeyOwners(d, applied, pending, reviews.NewPinKey("new", "", nil), nil)

		assert.False(t, o.Any())
		assert.Equal(t, Owners{}, o)
	})

	t.Run("an absent file has no entry owners", func(t *testing.T) {
		o := KeyOwners(nil, applied[:2], nil, key, nil)

		assert.Empty(t, o.Entries)
		assert.Equal(t, []model.Proposal{applied[0]}, o.Applied, "an applied row owns its key also without its pin")
	})
}

func TestAuthorKeysLeavesOutTheLinkedRows(t *testing.T) {
	applied := []model.Proposal{
		{ID: 1, Rule: "a", Region: "api"},
		{ID: 2, Rule: "b", Region: "db"},
		{ID: 3, Rule: "c", Region: "db", Regions: []string{"api"}},
	}

	assert.Equal(t, []reviews.PinKey{
		reviews.NewPinKey("a", "api", nil),
		reviews.NewPinKey("b", "db", nil),
		reviews.NewPinKey("c", "db", []string{"api"}),
		reviews.NewPinKey("c", "", []string{"api"}),
	}, AuthorKeys(applied, nil), "a disagreeing row gives its link key and its rendered key")

	assert.Equal(t, []reviews.PinKey{reviews.NewPinKey("b", "db", nil)}, AuthorKeys(applied, []int64{1, 3}))
	assert.Empty(t, AuthorKeys(nil, nil))
}

func TestALinkedNoteEditPlansWithTheAuthorKeys(t *testing.T) {
	// The caller passes the keys of every applied row except the row
	// linked to the edited entry. The note edit then keeps the key of
	// its own row without a collision, and a rename onto the key of
	// another row still collides.
	d := Parse([]byte(emitted))
	single := entryOf(t, d, "single")
	applied := []model.Proposal{
		{ID: 1, Rule: "single", Region: "api", Note: "Short note."},
		{ID: 2, Rule: "multi", Region: "api", Regions: []string{"api", "db"}, Note: "n"},
		{ID: 9, Rule: "elsewhere", Region: "web"},
	}

	links := Classify(d, applied, nil, nil)
	require.Equal(t, LinkLinked, links.Entries[0].Link)

	var own []int64
	for _, p := range links.Entries[0].Applied {
		own = append(own, p.ID)
	}

	others := AuthorKeys(applied, own)

	_, err := Plan(d, []Edit{{Ref: single.Ref(), Rewrite: &Rewrite{Note: text("Edited note.")}}}, others)
	require.NoError(t, err)

	_, err = Plan(d, []Edit{{Ref: single.Ref(), Rewrite: &Rewrite{Rule: text("elsewhere"), Regions: regionSet("web")}}}, others)
	require.ErrorIs(t, err, ErrCollision)
}
