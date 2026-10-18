package pinfile_test

// Tests of the text of authored pins: EncodeNote and RenderPin. Each
// test reads the text back through the public Parse, the reader that the
// editor and the console share.

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// noteFile returns a pin file with one entry whose note is the encoded
// note enc. head is the text before the note line, and prefix is the
// text before the note key. The later lines of a block note go two
// spaces deeper than the key. An empty line stays empty.
func noteFile(head, prefix string, enc []string) string {
	pad := strings.Repeat(" ", len(prefix)+2)
	lines := []string{prefix + "note: " + enc[0]}

	for _, l := range enc[1:] {
		if l != "" {
			l = pad + l
		}

		lines = append(lines, l)
	}

	return head + strings.Join(lines, "\n") + "\n"
}

// readNote parses the file and returns its one entry, after it checks
// that the editing gate passes.
func readNote(t *testing.T, file string) pinfile.Entry {
	t.Helper()

	d := pinfile.Parse([]byte(file))
	require.Empty(t, d.FileBlock, "the note never blocks a later edit:\n%s", file)
	require.NoError(t, d.ReaderErr)
	require.Len(t, d.Entries, 1)

	return d.Entries[0]
}

func TestEncodeNoteChoosesAFormThatReadsBack(t *testing.T) {
	cases := []struct {
		name, note string
		want       []string
	}{
		{"one line", "Keep it short.", []string{"Keep it short."}},
		{"one line that needs quotes", "a: b # c", []string{"'a: b # c'"}},
		{"empty", "", []string{`""`}},
		{"lines", "first\n# not a comment\n\n  deeper", []string{"|-", "first", "# not a comment", "", "  deeper"}},
		{"document markers", "---\n...", []string{"|-", "---", "..."}},
		{"unicode", "ünï 日本\n«b»", []string{"|-", "ünï 日本", "«b»"}},
		// yaml.v3 escapes a character outside the basic plane, so it
		// writes no literal block for it.
		{"an emoji", "ok 😀\nb", []string{`"ok \U0001F600\nb"`}},
		{"a tab at a line start", "a\n\tb", []string{`"a\n\tb"`}},
		{"leading spaces", "  lead\nx", []string{`"  lead\nx"`}},
		{"a leading line break", "\nx", []string{`"\nx"`}},
		{"a final line break", "x\ny\n", []string{`"x\ny\n"`}},
		{"trailing spaces", "x \ny", []string{`"x \ny"`}},
		{"a control character", "a\x1b[31m\nb", []string{`"a\e[31m\nb"`}},
		{"a line separator", "a\u2028b\nc", []string{`"a\Lb\nc"`}},
		// YAML reads a last line of only a tab or a Unicode space as note
		// text. The line rules of the editor read it as blank, so the entry
		// would not own that line.
		{"a last line of an ideographic space", "x\n\u3000", []string{"\"x\\n\u3000\""}},
		{"a last line of a no-break space", "x\n\u00a0", []string{"\"x\\n\u00a0\""}},
		{"a last line of a tab", "x\n\t", []string{`"x\n\t"`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := pinfile.EncodeNote(tc.note)
			require.NoError(t, err)
			assert.Equal(t, tc.want, enc)

			e := readNote(t, noteFile("pin:\n  - rule: r\n", "    ", enc))
			assert.Equal(t, tc.note, e.Note, "the note reads back byte for byte")
		})
	}
}

func TestEncodeNoteRoundTripsRandomNotes(t *testing.T) {
	// The property: every note that EncodeNote encodes reads back as the
	// same text, at any key column, and the file stays editable. The
	// alphabet holds the characters that change a YAML form: line
	// breaks, tabs, spaces, indicators, quotes, escapes, separators,
	// bidirectional controls, and wide text.
	alphabet := []rune("aZ09 \n\n\n\t#:-'\"|>*&!%@`{}[],\\?~é日😀\r\x00\x1b\x7f\u0085\u00a0\u2028\u2029\u202e\ufeff")
	// Random characters seldom make a literal block. Half of the notes
	// therefore join whole lines. The pool holds lines that YAML reads
	// as text but that the line rules of the editor read as blank.
	pool := []string{"a", "日本 b", "# x", "  deep", "- item", "k: v", "", "\t", "　", " ", " ", "x\ty"}
	// Each place puts the note key at another column, once on the dash
	// line with a key after it. removed is the file after the removal of
	// the entry with the note.
	places := []struct{ head, prefix, tail, removed string }{
		{"pin:\n  - rule: r\n", "    ", "", "pin:\n"},
		{"pin:\n", "  - ", "    rule: r\n", "pin:\n"},
		{"pin:\n- rule: r\n", "  ", "", "pin:\n"},
		{"threshold: 2\npin:\n    - rule: r\n      region: api\n", "      ", "    - rule: s\n", "threshold: 2\npin:\n    - rule: s\n"},
	}

	rng := rand.New(rand.NewPCG(4, 2))

	for i := range 3000 {
		var b strings.Builder

		for range rng.IntN(40) {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}

		note := b.String()

		if i%2 == 1 {
			lines := make([]string, 1+rng.IntN(4))
			for j := range lines {
				lines[j] = pool[rng.IntN(len(pool))]
			}

			note = strings.Join(lines, "\n")
		}

		enc, err := pinfile.EncodeNote(note)
		require.NoError(t, err, "%q", note)

		for _, at := range places {
			d := pinfile.Parse([]byte(noteFile(at.head, at.prefix, enc) + at.tail))
			require.Empty(t, d.FileBlock, "%q at %q", note, at.prefix)
			require.NotEmpty(t, d.Entries)
			require.Equal(t, note, d.Entries[0].Note, "%q at %q", note, at.prefix)

			// The entry owns every line of the note, so a later edit
			// replaces all of them and a removal leaves none behind.
			edited, where := "edited", fmt.Sprintf("%q at %q", note, at.prefix)
			want := noteFile(at.head, at.prefix, []string{edited}) + at.tail
			require.Equal(t, want, planText(t, d, pinfile.Edit{Rewrite: &pinfile.Rewrite{Note: &edited}}, where), where)
			require.Equal(t, at.removed, planText(t, d, pinfile.Edit{Delete: true}, where), where)
		}
	}
}

// planText plans the edit of the first entry of d and returns the
// candidate as text. where names the case in a failure.
func planText(t *testing.T, d *pinfile.Document, edit pinfile.Edit, where string) string {
	t.Helper()

	edit.Ref = d.Entries[0].Ref()

	c, err := pinfile.Plan(d, []pinfile.Edit{edit}, nil)
	require.NoError(t, err, where)

	return string(c.After)
}

func TestRenderPinWritesTheLayoutOfAnApply(t *testing.T) {
	// A manual pin has the layout of an applied pin without the
	// provenance comment. It has the same keys in the same order. The
	// value text is the same for the notes that both renderers can write.
	rows := []model.Proposal{
		{ID: 7, Rule: "pooled-state-reset", Region: "v2/pkg/engine/resolve",
			Note: `Reset pooled fields in Free() and deep-copy them in clone(): "both, always".`, Agent: "claude/v1"},
		{ID: 9, Rule: "validate-at-the-boundary", Region: "api", Regions: []string{"api", "db"},
			Note: "Enforce closed sets.", Agent: "claude/v3"},
		{ID: 1, Rule: "r", Note: "n", Agent: "a/v1"},
		{ID: 3, Rule: "repo", Note: "first line\n# not a comment\n\nlast line", Agent: "claude/v3"},
	}

	for _, p := range rows {
		applied, err := distill.RenderPins([]model.Proposal{p})
		require.NoError(t, err)

		_, entry, ok := strings.Cut(applied, "\n")
		require.True(t, ok)

		manual, err := pinfile.RenderPin(reviews.PinForRegions(p.Rule, p.Note, p.RegionSet()))
		require.NoError(t, err)
		assert.Equal(t, entry, manual, "p%d", p.ID)
	}
}

func TestRenderPinGoldens(t *testing.T) {
	cases := []struct {
		name string
		pin  reviews.PinRule
		want string
	}{
		{"repository-wide", reviews.PinRule{Rule: "r", Note: "Keep it."},
			"  - rule: r\n    region: '*'\n    note: Keep it.\n"},
		{"a set from Regions alone", reviews.PinRule{Rule: "r", Regions: []string{"db", "api"}, Note: "x"},
			"  - rule: r\n    region: db\n    regions: [db, api]\n    note: x\n"},
		{"one region", reviews.PinRule{Rule: "r", Region: "api", Note: "x"},
			"  - rule: r\n    region: api\n    note: x\n"},
		{"a block note keeps its empty line indented", reviews.PinRule{Rule: "r", Note: "a\n\nb"},
			"  - rule: r\n    region: '*'\n    note: |-\n      a\n  \n      b\n"},
		{"a tab-led note line takes double quotes", reviews.PinRule{Rule: "r", Note: "a\n\tb"},
			"  - rule: r\n    region: '*'\n    note: \"a\\n\\tb\"\n"},
		{"odd region text", reviews.PinRule{Rule: "r", Regions: []string{"a,b", "on"}, Note: "x"},
			"  - rule: r\n    region: a,b\n    regions: ['a,b', \"on\"]\n    note: x\n"},
		// A rewrite writes a trailing slash as it is, so a create does too.
		{"a trailing slash stays", reviews.PinRule{Rule: "r", Regions: []string{"api/", "db"}, Note: "x"},
			"  - rule: r\n    region: api/\n    regions: [api/, db]\n    note: x\n"},
		{"the layout of PinForRegions", reviews.PinForRegions("r", "x", []string{"api", "db"}),
			"  - rule: r\n    region: api\n    regions: [api, db]\n    note: x\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, err := pinfile.RenderPin(tc.pin)
			require.NoError(t, err)
			assert.Equal(t, tc.want, block)

			e := readNote(t, "pin:\n"+block)
			assert.Equal(t, tc.pin.Note, e.Note)
			assert.Equal(t, reviews.NewPinKey(tc.pin.Rule, tc.pin.Region, tc.pin.Regions), e.Key)
			assert.Empty(t, e.ProvenanceIDs, "a manual pin has no provenance comment")
		})
	}
}

func TestRenderPinRefusesTheRegionSetsThatARewriteRefuses(t *testing.T) {
	// A create and an edit of one pin follow one region rule. RenderPin
	// writes the set of the draft or fails. It never drops a region.
	sets := map[string][]string{
		"a repeated region":           {"api", "api"},
		"an empty region":             {"api", ""},
		"a repository-wide star":      {"api", "*"},
		"white space around":          {" api"},
		"the region again at the end": {"api", "db", "api"},
	}

	for name, set := range sets {
		t.Run(name, func(t *testing.T) {
			d := pinfile.Parse([]byte("pin:\n  - rule: r\n    region: web\n"))

			_, edit := pinfile.Plan(d, []pinfile.Edit{{Ref: d.Entries[0].Ref(), Rewrite: &pinfile.Rewrite{Regions: &set}}}, nil)
			require.ErrorIs(t, edit, pinfile.ErrUnsupported)

			_, create := pinfile.RenderPin(reviews.PinRule{Rule: "r", Note: "x", Regions: set})
			require.ErrorIs(t, create, pinfile.ErrUnsupported)
			assert.Equal(t, edit.Error(), create.Error())
		})
	}

	t.Run("a region that repeats the first region of the set", func(t *testing.T) {
		_, err := pinfile.RenderPin(reviews.PinRule{Rule: "r", Region: "db", Regions: []string{"api", "db"}, Note: "x"})
		require.ErrorIs(t, err, pinfile.ErrUnsupported)
		assert.Contains(t, err.Error(), "the new region set repeats a region")
	})
}

func TestRenderPinRefusesTextWithoutYAML(t *testing.T) {
	_, err := pinfile.RenderPin(reviews.PinRule{Rule: "bad\xff", Note: "x"})
	require.ErrorIs(t, err, pinfile.ErrUnsupported)
	assert.Contains(t, err.Error(), "the rule has no safe single-line YAML text")

	_, err = pinfile.RenderPin(reviews.PinRule{Rule: "r", Note: "bad\xff\nx"})
	require.ErrorIs(t, err, pinfile.ErrUnsupported)
	assert.Contains(t, err.Error(), "the note has no safe YAML text")
}
