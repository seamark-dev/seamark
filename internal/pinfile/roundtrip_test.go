package pinfile_test

// Round trips with the real writers. The CLI writes pins with
// distill.ApplyPins and removes them with distill.RemovePins, and
// `seamark init` writes the starter file. The console editor must read
// what they write and undo an apply byte for byte.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/scaffold"
)

// applied returns proposal rows in the shapes that distillation writes:
// one region, a region set, and a repository-wide row with a block note.
func applied() []model.Proposal {
	return []model.Proposal{
		{ID: 1, Rule: "single", Region: "api", Note: "Short note.", Agent: "claude/v3", Members: []int64{1}, Status: model.ProposalApplied},
		{ID: 12, Rule: "multi", Region: "api", Regions: []string{"api", "db"}, Note: "on", Agent: "claude/v3", Members: []int64{2, 3}, Status: model.ProposalApplied},
		{ID: 3, Rule: "repo", Note: "first line\n# not a comment\n\nlast line", Agent: "claude/v3", Members: []int64{4}, Status: model.ProposalApplied},
	}
}

// starter returns the lessons.yaml body that `seamark init` writes.
func starter(t *testing.T) string {
	t.Helper()

	for _, doc := range scaffold.Documents("") {
		if doc.Path == pinfile.File {
			body, err := doc.Compose(nil, false)
			require.NoError(t, err)

			return string(body)
		}
	}

	require.FailNow(t, "the scaffold has no lessons document")

	return ""
}

// rootWith writes body as the pin file under a new root.
func rootWith(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(body), 0o644))

	return root
}

// pins reads the pin file text under root.
func pins(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)

	return string(data)
}

// commit plans the edits on the file under root and writes the change.
func commit(t *testing.T, root string, edits func(d *pinfile.Document) []pinfile.Edit) *pinfile.Change {
	t.Helper()

	d, err := pinfile.Read(root)
	require.NoError(t, err)
	require.Empty(t, d.FileBlock)

	c, err := pinfile.Plan(d, edits(d), nil)
	require.NoError(t, err)

	p, err := pinfile.Prepare(integration.Builtin(), root, d.Guard, c.After)
	require.NoError(t, err)
	require.NoError(t, p.Apply())

	return c
}

// pruneAll prunes every entry with its linked provenance.
func pruneAll(d *pinfile.Document) []pinfile.Edit {
	var edits []pinfile.Edit

	for _, e := range d.Entries {
		edits = append(edits, pinfile.Edit{Ref: e.Ref(), Delete: true, Provenance: e.ProvenanceIDs})
	}

	return edits
}

func TestRenderPinsRoundTrip(t *testing.T) {
	const seed = "threshold: 2\npin:\n"

	root := rootWith(t, seed)
	rows := applied()
	require.NoError(t, distill.ApplyPins(root, rows))

	d, err := pinfile.Read(root)
	require.NoError(t, err)
	require.Empty(t, d.FileBlock)
	require.Len(t, d.Entries, len(rows))

	// ApplyPins inserts the block as written, in proposal order.
	for i, p := range rows {
		e := d.Entries[i]

		assert.Equal(t, []int64{p.ID}, e.ProvenanceIDs, "the provenance comment names the proposal")
		assert.Equal(t, reviews.ProposalKey(p), e.Key, "the rendered pin has the key of its row")
		assert.Equal(t, p.Note, e.Note)
	}

	links := pinfile.Classify(d, rows, nil, nil)
	for i, el := range links.Entries {
		assert.Equal(t, pinfile.LinkLinked, el.Link, d.Entries[i].Rule)
		assert.False(t, el.NoteChanged)
	}

	assert.Empty(t, links.NotPresent)

	// A prune of every entry undoes the apply byte for byte.
	commit(t, root, pruneAll)
	assert.Equal(t, seed, pins(t, root))
}

func TestPruneMatchesRemovePinsOnTheEmittedLayout(t *testing.T) {
	// On the layout that the CLI writes, the console prune and the CLI
	// prune remove the same bytes. The console binds the entry, the CLI
	// removes every entry with the key; with unique keys they agree.
	rows := applied()

	console := rootWith(t, "pin:\n")
	require.NoError(t, distill.ApplyPins(console, rows))

	cli := rootWith(t, pins(t, console))

	commit(t, console, func(d *pinfile.Document) []pinfile.Edit {
		e := d.Entries[1]

		return []pinfile.Edit{{Ref: e.Ref(), Delete: true, Provenance: e.ProvenanceIDs}}
	})

	removed, err := distill.RemovePins(cli, []reviews.PinKey{reviews.NewPinKey("multi", "api", []string{"api", "db"})})
	require.NoError(t, err)
	require.Len(t, removed, 1)

	assert.Equal(t, pins(t, cli), pins(t, console))
}

func TestTheScaffoldedFileKeepsItsCommentsAroundAnEdit(t *testing.T) {
	// The starter file carries a commented example under pin: and a
	// column-0 pin_budget block after it. Both must stay byte-identical.
	body := starter(t)
	require.Contains(t, body, "pin:\n  # - rule: RUF001\n")
	require.Contains(t, body, "# pin_budget: 3\n")

	root := rootWith(t, body)
	require.NoError(t, distill.ApplyPins(root, applied()[:1]))

	withPin := pins(t, root)
	head, rest, ok := strings.Cut(withPin, "pin:\n")
	require.True(t, ok)

	head += "pin:\n"
	_, foot, ok := strings.Cut(rest, "  # - rule: RUF001")
	require.True(t, ok)

	foot = "  # - rule: RUF001" + foot

	t.Run("the starter lists the applied pin", func(t *testing.T) {
		d, err := pinfile.Read(root)
		require.NoError(t, err)
		require.Empty(t, d.FileBlock)
		require.Len(t, d.Entries, 1, "the commented example is not a pin")
		assert.Equal(t, "single", d.Entries[0].Rule)
	})

	t.Run("retarget", func(t *testing.T) {
		retarget := rootWith(t, withPin)

		commit(t, retarget, func(d *pinfile.Document) []pinfile.Edit {
			return []pinfile.Edit{{Ref: d.Entries[0].Ref(), Regions: []string{"api", "web"}}}
		})

		got := pins(t, retarget)
		assert.Equal(t, strings.Replace(withPin, "    region: api\n", "    region: api\n    regions: [api, web]\n", 1), got)
		assert.True(t, strings.HasSuffix(got, foot), "the commented example and the pin_budget block stay")
		assert.True(t, strings.HasPrefix(got, head))
	})

	t.Run("prune restores the starter", func(t *testing.T) {
		prune := rootWith(t, withPin)

		commit(t, prune, pruneAll)
		assert.Equal(t, body, pins(t, prune))
	})
}

func TestTheConsoleWriteKeepsTheEditWhereTheUserPutIt(t *testing.T) {
	// The CLI retarget moves the entry to the head of the list and writes
	// the stored note. The console retarget keeps the position, the live
	// note, the rule spelling, the comments, and the unknown keys.
	const file = `pin:
  - rule: first
    region: lib
    note: stays first
  # my own comment about the datasets pin
  - rule: Guard-Empty-Datasets
    region: data
    note: HAND EDITED live note that must survive.
    extra_field: kept
`
	root := rootWith(t, file)

	commit(t, root, func(d *pinfile.Document) []pinfile.Edit {
		return []pinfile.Edit{{Ref: d.Entries[1].Ref(), Regions: []string{"scripts"}}}
	})

	assert.Equal(t, strings.Replace(file, "    region: data\n", "    region: scripts\n", 1), pins(t, root))
}

// linkedByOneKey asserts that each row links to exactly one entry of the
// pin file under root, for the console and for the CLI readers. The
// console list (Classify) and the CLI liveness check (FindPin) must name
// the same entry, because both read reviews.ProposalKey.
func linkedByOneKey(t *testing.T, root string, rows []model.Proposal) {
	t.Helper()

	d, err := pinfile.Read(root)
	require.NoError(t, err)
	require.Empty(t, d.FileBlock)

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	links := pinfile.Classify(d, rows, nil, nil)
	assert.Empty(t, links.NotPresent)

	for _, p := range rows {
		key := reviews.ProposalKey(p)
		assert.Equal(t, pinfile.LinkLinked, links.ByProposal[p.ID], "p%d", p.ID)

		i := slices.IndexFunc(d.Entries, func(e pinfile.Entry) bool { return e.Key == key })
		require.GreaterOrEqual(t, i, 0, "p%d has an entry with its key %s", p.ID, key)
		assert.Equal(t, []model.Proposal{p}, links.Entries[i].Applied, "p%d", p.ID)

		pin, live := cfg.FindPin(key)
		require.True(t, live, "the CLI liveness check finds the pin of p%d", p.ID)
		assert.Equal(t, d.Entries[i].Note, pin.Note, "both readers name one entry for p%d", p.ID)
	}
}

func TestEveryWriterPathWritesThePinOfTheOneKey(t *testing.T) {
	// The CLI apply (distill.ApplyPins) writes region and a flow regions
	// list. The console retarget (pinfile.Plan) edits those keys in place
	// and keeps a block list. Each layout must link by the one key.
	rows := applied()
	root := rootWith(t, "pin:\n")
	require.NoError(t, distill.ApplyPins(root, rows))

	linkedByOneKey(t, root, rows)

	// The console retarget writes the new sets. The ledger then takes
	// region = the first region and regions = the set, as maintain does.
	sets := map[string][]string{"single": {"api", "web"}, "multi": {"db"}, "repo": {"lib"}}

	commit(t, root, func(d *pinfile.Document) []pinfile.Edit {
		var edits []pinfile.Edit
		for _, e := range d.Entries {
			edits = append(edits, pinfile.Edit{Ref: e.Ref(), Regions: sets[e.Rule]})
		}

		return edits
	})

	for i := range rows {
		rows[i].Region, rows[i].Regions = sets[rows[i].Rule][0], sets[rows[i].Rule]
	}

	linkedByOneKey(t, root, rows)

	// A hand-written block list keeps its layout through a retarget, also
	// to a repository-wide scope.
	block := rootWith(t, "pin:\n  - rule: block\n    regions:\n      - api\n      - db\n    note: n\n")
	row := model.Proposal{ID: 7, Rule: "block", Region: "api", Regions: []string{"api", "db"}, Note: "n",
		Status: model.ProposalApplied}

	linkedByOneKey(t, block, []model.Proposal{row})

	commit(t, block, func(d *pinfile.Document) []pinfile.Edit {
		return []pinfile.Edit{{Ref: d.Entries[0].Ref(), Regions: []string{"db", "web"}}}
	})
	assert.Contains(t, pins(t, block), "    regions:\n      - db\n      - web\n")

	row.Region, row.Regions = "db", []string{"db", "web"}
	linkedByOneKey(t, block, []model.Proposal{row})

	commit(t, block, func(d *pinfile.Document) []pinfile.Edit {
		return []pinfile.Edit{{Ref: d.Entries[0].Ref(), Regions: nil}}
	})

	row.Region, row.Regions = "", nil
	linkedByOneKey(t, block, []model.Proposal{row})
}

func TestEveryReaderLinksARowWithDisagreeingFieldsToOneEntry(t *testing.T) {
	// For these rows the two region fields disagree (pinfile.FieldsAgree).
	// The console used to link them by the region set that RenderPins
	// writes and also by the stored fields; the CLI used the stored
	// fields only. Now every reader uses reviews.ProposalKey, so the
	// console list, the CLI liveness check, and the CLI prune name the
	// same entry: the one with the stored fields.
	cases := []struct {
		name    string
		region  string
		regions []string
		stored  string
	}{
		{"wildcard next to a real region", "api", []string{"*", "db"}, "    region: api\n    regions: ['*', db]\n"},
		{"wildcard as the whole set", "api", []string{"*"}, "    region: api\n    regions: ['*']\n"},
		{"empty region in the set", "api", []string{"", "db"}, "    region: api\n    regions: ['', db]\n"},
		{"region outside the set", "x", []string{"a", "b"}, "    region: x\n    regions: [a, b]\n"},
		{"set without a real region", "api", []string{"/"}, "    region: api\n    regions: [/]\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Proposal{ID: 4, Rule: "Odd", Region: tc.region, Regions: tc.regions, Note: "rendered",
				Agent: "claude/v3", Members: []int64{1}, Status: model.ProposalApplied}
			require.False(t, pinfile.FieldsAgree(p))

			// The file holds the pin that the CLI apply renders for p, and
			// a pin with the stored fields of p.
			rendered, err := distill.RenderPins([]model.Proposal{p})
			require.NoError(t, err)

			body := "pin:\n" + rendered + "  - rule: odd\n" + tc.stored + "    note: stored\n"
			root := rootWith(t, body)

			key := reviews.ProposalKey(p)
			assert.Equal(t, reviews.NewPinKey(p.Rule, p.Region, p.Regions), key, "the CLI form of the key")

			d, err := pinfile.Read(root)
			require.NoError(t, err)
			require.Len(t, d.Entries, 2)
			assert.NotEqual(t, key, d.Entries[0].Key, "the rendered pin has another key")
			assert.Equal(t, key, d.Entries[1].Key, "the stored-fields pin has the key")

			// The console list: the row links to the stored-fields pin
			// only, and it stays ambiguous, so no action can start.
			links := pinfile.Classify(d, []model.Proposal{p}, nil, nil)
			assert.Equal(t, pinfile.LinkNoProposal, links.Entries[0].Link)
			assert.Empty(t, links.Entries[0].Applied)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.Entries[1].Link)
			assert.Equal(t, []model.Proposal{p}, links.Entries[1].Applied)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.ByProposal[p.ID])

			// A pending row with the same fields matches the same entry. It
			// stays ambiguous too, so Pins never routes it to the record of
			// an existing pin, which refuses it.
			pending := p
			pending.Status = model.ProposalProposed
			links = pinfile.Classify(d, nil, []model.Proposal{pending}, nil)
			assert.Empty(t, links.Entries[0].Pending)
			assert.Equal(t, []model.Proposal{pending}, links.Entries[1].Pending)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.Entries[1].Link)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.ByProposal[pending.ID])

			// With only the rendered pin in the file, no entry has the key.
			// The pin that the apply wrote is still live, so the row reads
			// ambiguous, not "not present". It stays in the NotPresent list,
			// so a reader of the lists still sees it.
			only := pinfile.Parse([]byte("pin:\n" + rendered))
			require.Len(t, only.Entries, 1)

			links = pinfile.Classify(only, []model.Proposal{p}, nil, nil)
			assert.Equal(t, pinfile.LinkNoProposal, links.Entries[0].Link)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.ByProposal[p.ID])
			assert.Equal(t, []model.Proposal{p}, links.NotPresent)

			links = pinfile.Classify(only, nil, []model.Proposal{pending}, nil)
			assert.Equal(t, pinfile.LinkNoProposal, links.Entries[0].Link)
			assert.Equal(t, pinfile.LinkAmbiguousFields, links.ByProposal[pending.ID])

			// The CLI liveness check finds the same entry.
			cfg, err := reviews.LoadConfig(root)
			require.NoError(t, err)

			pin, live := cfg.FindPin(key)
			require.True(t, live)
			assert.Equal(t, "stored", pin.Note)

			// The CLI prune removes the same entry and keeps the other.
			removed, err := distill.RemovePins(root, []reviews.PinKey{key})
			require.NoError(t, err)
			assert.Equal(t, []reviews.PinKey{key}, removed)
			assert.Equal(t, "pin:\n"+rendered, pins(t, root))
		})
	}
}
