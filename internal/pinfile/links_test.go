package pinfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// row returns a proposal row with the given region fields.
func row(id int64, status, rule, region string, regions []string, note string) model.Proposal {
	return model.Proposal{ID: id, Status: status, Rule: rule, Region: region, Regions: regions, Note: note}
}

// linkFile holds one entry for each link state.
const linkFile = `pin:
  - rule: linked
    region: api
    note: live note
  - rule: edited
    region: api
    note: edited live note
  - rule: manual
    note: hand written
  - rule: waiting
    region: db
  - rule: dup
    region: x
  - rule: DUP
    region: x/
  - rule: twice
    region: api
  - rule: star
    region: '*'
  - rule: older
    region: api
`

func TestProposalKeyIsTheRenderedRegionSet(t *testing.T) {
	assert.Equal(t, reviews.NewPinKey("r", "", []string{"api", "db"}), ProposalKey(row(1, "", "R", "api", []string{"api", "db"}, "")))
	assert.Equal(t, reviews.NewPinKey("r", "api", nil), ProposalKey(row(1, "", "r", "api", nil, "")))
	assert.Equal(t, reviews.NewPinKey("r", "*", nil), ProposalKey(row(1, "", "r", "", nil, "")))

	// A "*" anywhere in Regions makes the set repository-wide, as
	// RenderPins writes it; the stored key would drop the star instead.
	odd := row(1, "", "r", "api", []string{"*", "db"}, "")
	assert.Equal(t, reviews.NewPinKey("r", "*", nil), ProposalKey(odd))
	assert.NotEqual(t, storedKey(odd), ProposalKey(odd))

	// A Region outside Regions is ignored by the rendered form.
	apart := row(1, "", "r", "x", []string{"a", "b"}, "")
	assert.Equal(t, reviews.NewPinKey("r", "", []string{"a", "b"}), ProposalKey(apart))
	assert.False(t, FieldsAgree(apart))
	assert.True(t, FieldsAgree(row(1, "", "r", "a", []string{"a", "b"}, "")))
}

func TestClassifyDistinguishesEveryState(t *testing.T) {
	d := Parse([]byte(linkFile))
	require.Empty(t, d.FileBlock)

	applied := []model.Proposal{
		row(1, model.ProposalApplied, "linked", "api", nil, "live note"),
		row(2, model.ProposalApplied, "edited", "api", nil, "recorded note"),
		row(3, model.ProposalApplied, "twice", "api", nil, "a"),
		row(4, model.ProposalApplied, "twice", "api", []string{"api"}, "b"),
		row(5, model.ProposalApplied, "star", "api", []string{"*", "api"}, "s"),
		row(6, model.ProposalApplied, "dup", "x", nil, "d"),
		row(7, model.ProposalApplied, "gone", "lib", nil, "g"),
		row(8, model.ProposalApplied, "apart", "x", []string{"a", "b"}, "p"),
		row(9, model.ProposalApplied, "older", "api", []string{"*"}, "o"),
	}
	pending := []model.Proposal{
		row(10, model.ProposalProposed, "waiting", "db", nil, "w"),
		row(11, model.ProposalProposed, "linked", "api", nil, "again"),
		row(12, model.ProposalProposed, "future", "lib", nil, "f"),
	}
	superseded := []model.Proposal{
		row(20, model.ProposalSuperseded, "linked", "api", nil, "old wording"),
		// Its ProposalKey is "older @ *", and its stored key is "older @ api".
		row(21, model.ProposalSuperseded, "older", "api", []string{"*"}, "old"),
	}

	links := Classify(d, applied, pending, superseded)

	require.Len(t, links.Entries, len(d.Entries))

	want := map[string]Link{
		"linked": LinkLinked, "edited": LinkLinked, "manual": LinkNoProposal, "waiting": LinkPendingMatch,
		"dup": LinkAmbiguousFile, "DUP": LinkAmbiguousFile, "twice": LinkAmbiguousLedger,
		"star": LinkAmbiguousFields, "older": LinkAmbiguousFields,
	}

	for i, e := range d.Entries {
		assert.Equal(t, want[e.Rule], links.Entries[i].Link, e.Rule)
	}

	ids := func(rows []model.Proposal) []int64 {
		var out []int64
		for _, p := range rows {
			out = append(out, p.ID)
		}

		return out
	}

	linked := links.Entries[0]
	assert.Equal(t, []int64{1}, ids(linked.Applied))
	assert.Equal(t, []int64{11}, ids(linked.Pending), "a pending row of a linked pin is listed")
	assert.Equal(t, []int64{20}, ids(linked.Superseded), "history stays visible")
	assert.False(t, linked.NoteChanged)

	assert.True(t, links.Entries[1].NoteChanged, "the live note differs from the recorded note")
	assert.Equal(t, []int64{6}, ids(links.Entries[4].Applied), "both duplicates list the row")
	assert.Equal(t, []int64{6}, ids(links.Entries[5].Applied))
	assert.Equal(t, []int64{3, 4}, ids(links.Entries[6].Applied), "an ambiguous entry lists every match")
	assert.Equal(t, []int64{9}, ids(links.Entries[8].Applied), "a row with disagreeing fields matches by its stored key too")
	assert.Equal(t, []int64{21}, ids(links.Entries[8].Superseded), "superseded history matches by either key form, as applied rows do")
	assert.Equal(t, LinkAmbiguousFields, links.Entries[8].Link, "history never changes the link")

	for i, el := range links.Entries {
		if i != 0 && i != 8 {
			assert.Empty(t, el.Superseded, d.Entries[i].Rule)
		}
	}

	assert.Equal(t, map[int64]Link{
		1: LinkLinked, 2: LinkLinked, 3: LinkAmbiguousLedger, 4: LinkAmbiguousLedger, 5: LinkAmbiguousFields,
		6: LinkAmbiguousFile, 7: LinkNotPresent, 8: LinkNotPresent, 9: LinkAmbiguousFields,
		10: LinkPendingMatch, 11: LinkPendingMatch, 12: LinkNotPresent,
	}, links.ByProposal)

	assert.Equal(t, []int64{7, 8}, ids(links.NotPresent))
	require.NoError(t, links.NotPresentErr)
}

func TestClassifyOfAnAbsentFileMakesEveryAppliedRowNotPresent(t *testing.T) {
	applied := []model.Proposal{row(1, model.ProposalApplied, "a", "api", nil, "n")}

	links := Classify(Parse(nil), applied, []model.Proposal{row(2, model.ProposalProposed, "b", "", nil, "")}, nil)

	assert.Empty(t, links.Entries)
	assert.Equal(t, applied, links.NotPresent)
	require.NoError(t, links.NotPresentErr)
	assert.Equal(t, map[int64]Link{1: LinkNotPresent, 2: LinkNotPresent}, links.ByProposal)
}

func TestClassifyNeverTurnsAReadErrorIntoAbsence(t *testing.T) {
	applied := []model.Proposal{
		row(1, model.ProposalApplied, "a", "api", nil, "n"),
		row(2, model.ProposalApplied, "gone", "lib", nil, "n"),
	}
	pending := []model.Proposal{row(3, model.ProposalProposed, "a", "api", nil, "")}

	cases := map[string]*Document{
		"reader error": Parse([]byte("hook_delivery: sometimes\npin:\n  - rule: a\n    region: api\n")),
		"syntax error": Parse([]byte("pin:\n  - rule: a\n    note: [open\n")),
		"no document":  nil,
		// The reader delivers pin a @ api, but the listing sees no entry.
		"alias pin value":          Parse([]byte("defs: &p\n  - rule: a\n    region: api\npin: *p\n")),
		"pin key from a merge key": Parse([]byte("base: &b\n  pin:\n    - rule: a\n      region: api\n<<: *b\n")),
	}

	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			links := Classify(d, applied, pending, nil)

			assert.Nil(t, links.NotPresent, "a read error never lists a row as not present")
			require.ErrorIs(t, links.NotPresentErr, ErrUnreadable)
			assert.Equal(t, "unknown: .seamark/lessons.yaml could not be read", links.NotPresentErr.Error())
			assert.Equal(t, map[int64]Link{1: LinkUnknown, 2: LinkUnknown, 3: LinkUnknown}, links.ByProposal)

			for _, el := range links.Entries {
				assert.Equal(t, LinkUnknown, el.Link)
				assert.False(t, el.NoteChanged)
			}
		})
	}

	// The entries that still decode keep their matches for display.
	links := Classify(cases["reader error"], applied, pending, nil)
	require.Len(t, links.Entries, 1)
	assert.Len(t, links.Entries[0].Applied, 1)
	assert.Len(t, links.Entries[0].Pending, 1)
}

func TestLinkReasonsArePlainText(t *testing.T) {
	want := map[Link]string{
		LinkUnknown:         "pin file unreadable",
		LinkLinked:          "linked to one applied proposal",
		LinkNoProposal:      "no linked proposal",
		LinkPendingMatch:    "matches a pending proposal; record it as applied first",
		LinkAmbiguousFile:   "ambiguous: duplicate pins",
		LinkAmbiguousLedger: "ambiguous: several applied proposals",
		LinkAmbiguousFields: "ambiguous: stored region fields disagree",
		LinkNotPresent:      "pin not present",
	}

	for link, text := range want {
		assert.Equal(t, text, link.Reason())

		for _, r := range text {
			assert.True(t, r >= 0x20 && r < 0x7f, "%q holds a character outside printable ASCII", text)
		}
	}

	var zero Link
	assert.Equal(t, LinkUnknown, zero, "an unset link never reads as linked")
	assert.Equal(t, "pin file unreadable", Link(99).Reason())
}
