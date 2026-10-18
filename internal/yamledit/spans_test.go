package yamledit

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// spanLayout is one pin list layout with the expected line span of each
// item. The layouts come from the lessons.yaml map (api-maps/pins-yaml.md
// section 8): emitted, starter, documented, and hand-written forms.
type spanLayout struct {
	name, text string
	spans      [][2]int
}

var spanLayouts = []spanLayout{
	{
		name: "emitted by RenderPins",
		text: `threshold: 2
pin:
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)
  - rule: single
    region: api
    note: Short note.
  # distilled by claude/v3 from 2 findings (seamark lessons --distill, p2)
  - rule: multi
    region: api
    regions: [api, db]
    note: "n"
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p3)
  - rule: repo
    region: '*'
    note: |-
      line one
      # not a comment

      line three
mute:
  - rule: F541
`,
		spans: [][2]int{{4, 6}, {8, 11}, {13, 19}},
	},
	{
		name: "starter file with one applied entry",
		text: `threshold: 2

# Your exceptions that must NOT be ignored.
pin:
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p7)
  - rule: RUF001
    region: scripts
    note: "Keep scripts ASCII"
  # - rule: RUF001
  #   region: scripts
  #   note: "Keep scripts ASCII — smart quotes from chat have bitten us"

# How many pins the EDIT HOOK injects per edit.
# pin_budget: 3
`,
		spans: [][2]int{{6, 8}},
	},
	{
		name: "documented layout with aligned trailing comments",
		text: `threshold: 2                     # min recurrences to surface
pin:                             # your "must not be ignored" list
  - rule: RUF001                 # surfaced always for its region
    region: scripts              # mining found it once or never
    note: "Keep scripts ASCII — smart quotes have bitten us"
  - rule: validate-at-the-boundary
    region: api                  # a theme living in two places
    regions: [api, db]           # a set beats a repo-wide *
    note: "Enforce closed sets and non-null contracts at the edge."
`,
		spans: [][2]int{{3, 5}, {6, 9}},
	},
	{
		name: "zero-indent dashes with a multi-line plain note and a trailing provenance comment",
		text: `pin:
- rule: keep-parallel-implementations-consistent
  region: api
  regions: [api, core]
  note: "When two runtimes or call sites share a contract, mirror validation
    strictness and regenerate derived artifacts: run make sync-api after
    schemas.py changes, and keep the Go/Python seed loaders equally
    fail-fast."
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p60)
# a comment at the dash column
- rule: b
  note: plain note
    continued on the next line
# foot
`,
		spans: [][2]int{{2, 9}, {11, 13}},
	},
	{
		name: "four-space dashes with blank lines and comments inside the items",
		text: `pin:
    - rule: a

      # inner comment
      note: x

    # head comment of b

    - note: the first key is the note
      rule: b
      regions:
        - api   # trailing
        - db

      # deeper comment after a blank line

threshold: 3
`,
		spans: [][2]int{{2, 5}, {9, 15}},
	},
	{
		name: "dash alone, comment on the dash, null item, anchor, flow item",
		text: `pin:
  -
    rule: a
  - # head comment on the dash line
    rule: b
  -
  - &x
    rule: c
  - {rule: d, region: api}
  -

    # comment between the dash and the first key
    rule: e
`,
		spans: [][2]int{{2, 3}, {4, 5}, {6, 6}, {7, 8}, {9, 9}, {10, 13}},
	},
	{
		name:  "quoted continuation left of the dash column",
		text:  "pin:\n  - rule: a\n    note: \"x\n# still the note\"\n  - rule: b\n",
		spans: [][2]int{{2, 4}, {5, 5}},
	},
	{
		name:  "flow list over two lines inside an item",
		text:  "pin:\n  - rule: a\n    regions: [api,\n      db]\n    note: x\n  - rule: b\n",
		spans: [][2]int{{2, 5}, {6, 6}},
	},
	{
		name:  "duplicate entries and unknown keys",
		text:  "pin:\n  - rule: a\n    region: api\n    enabled: false\n  - rule: A\n    region: api/\n    regoins: [db]\n",
		spans: [][2]int{{2, 4}, {5, 7}},
	},
	{
		name:  "end marker",
		text:  "pin:\n  - rule: a\n    note: x\n...\n# after the marker\n",
		spans: [][2]int{{2, 3}},
	},
	{
		name:  "no final line break",
		text:  "pin:\n  - rule: a\n    note: x",
		spans: [][2]int{{2, 3}},
	},
	{
		name:  "anchored sequence",
		text:  "pin: &pins\n  # comment\n  - rule: a\n  -\n    rule: b\n",
		spans: [][2]int{{3, 3}, {4, 5}},
	},
	{
		name:  "tagged sequence",
		text:  "pin: !!seq\n- rule: a\n- rule: b\n",
		spans: [][2]int{{2, 2}, {3, 3}},
	},
	{
		name:  "dash alone before a nested list",
		text:  "pin:\n  -\n    - a\n  - b\n",
		spans: [][2]int{{2, 3}, {4, 4}},
	},
	{
		name:  "byte order mark",
		text:  "\ufeffthreshold: 2\npin:\n  - rule: a\n",
		spans: [][2]int{{3, 3}},
	},
}

// pinSequence parses text and returns the document, the pin sequence,
// and the first line after the pin section.
func pinSequence(t *testing.T, text string) (doc *Doc, seq *yaml.Node, stop int) {
	t.Helper()

	doc = parsed(t, text)

	_, seq, idx := doc.Section("pin")
	require.NotNil(t, seq)

	return doc, seq, doc.SectionEnd(idx) + 1
}

func TestItemSpanFindsTheOwnLinesOfEachItem(t *testing.T) {
	for _, c := range spanLayouts {
		t.Run(c.name, func(t *testing.T) {
			doc, seq, stop := pinSequence(t, c.text)
			require.Len(t, seq.Content, len(c.spans))

			for i, want := range c.spans {
				first, last, ok := doc.ItemSpan(seq, i, stop)
				require.True(t, ok, "item %d", i)
				assert.Equal(t, want, [2]int{first, last}, "item %d", i)
			}
		})
	}
}

// TestDeletingAnItemSpanRemovesExactlyThatItem proves the spans on the
// parsed result: without the lines of item i, the pin list holds every
// other item unchanged, and every other key stays.
func TestDeletingAnItemSpanRemovesExactlyThatItem(t *testing.T) {
	for _, c := range spanLayouts {
		t.Run(c.name, func(t *testing.T) {
			doc, seq, stop := pinSequence(t, c.text)
			before := decodeTop(t, c.text)

			for i := range seq.Content {
				first, last, ok := doc.ItemSpan(seq, i, stop)
				require.True(t, ok)

				lines := slices.Delete(slices.Clone(doc.Lines), first-1, last)
				after := string(JoinLines(lines, doc.FinalEOL))

				_, issue := Parse([]byte(after))
				require.Nil(t, issue, "item %d:\n%s", i, after)

				want := decodeTop(t, c.text)
				items, _ := want["pin"].([]any)
				want["pin"] = slices.Delete(slices.Clone(items), i, i+1)

				if len(items) == 1 {
					want["pin"] = nil
				}

				assert.Equal(t, want, decodeTop(t, after), "item %d:\n%s", i, after)
				assert.Equal(t, before, decodeTop(t, c.text), "the source text stays as it was")
			}
		})
	}
}

// decodeTop decodes the whole document, unknown keys included. It keeps
// the pin items in order, with their multiplicity and their unknown keys.
func decodeTop(t *testing.T, text string) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out))

	return out
}

func TestItemSpanOfCRLFTextAfterNormalization(t *testing.T) {
	text := spanLayouts[0].text

	data, crlf, issue := NormalizeBreaks([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
	require.Nil(t, issue)
	require.True(t, crlf)
	assert.Equal(t, text, string(data))

	doc, seq, stop := pinSequence(t, string(data))

	for i, want := range spanLayouts[0].spans {
		first, last, ok := doc.ItemSpan(seq, i, stop)
		require.True(t, ok)
		assert.Equal(t, want, [2]int{first, last})
	}
}

func TestItemSpanOfANestedBlockList(t *testing.T) {
	doc, seq, stop := pinSequence(t, spanLayouts[4].text)

	_, last, ok := doc.ItemSpan(seq, 1, stop)
	require.True(t, ok)

	regions, ok := doc.KeySpan(seq.Content[1], "regions", last+1)
	require.True(t, ok)
	assert.Equal(t, [2]int{11, 13}, [2]int{regions.First, regions.Last}, "a comment at the key column is not part of the key")

	var spans [][2]int

	for j := range regions.Value.Content {
		first, last, ok := doc.ItemSpan(regions.Value, j, regions.Last+1)
		require.True(t, ok)

		spans = append(spans, [2]int{first, last})
	}

	assert.Equal(t, [][2]int{{12, 12}, {13, 13}}, spans)
}

func TestItemSpanRefusesLayoutsWithoutSafeLines(t *testing.T) {
	doc, seq, stop := pinSequence(t, "pin: [a, b]\n")
	_, _, ok := doc.ItemSpan(seq, 0, stop)
	assert.False(t, ok, "a flow list has no line per item")

	doc, seq, stop = pinSequence(t, "pin:\n  - - a\n    - b\n")
	_, _, ok = doc.ItemSpan(seq, 0, stop)
	require.True(t, ok, "the outer list has its own dashes")

	inner := seq.Content[0]
	_, _, ok = doc.ItemSpan(inner, 0, stop)
	assert.False(t, ok, "the first dash of a compact nested list shares its line with the outer dash")

	doc, seq, stop = pinSequence(t, "pin:\n  - rule: a\n")
	_, _, ok = doc.ItemSpan(seq, 1, stop)
	assert.False(t, ok, "an index after the last item")

	_, _, ok = doc.ItemSpan(seq, -1, stop)
	assert.False(t, ok, "a negative index")

	_, _, ok = doc.ItemSpan(seq, 0, 2)
	assert.False(t, ok, "a stop line at the dash line")

	_, _, ok = doc.ItemSpan(nil, 0, stop)
	assert.False(t, ok, "no node")

	_, v, _ := doc.Section("pin")
	_, _, ok = doc.ItemSpan(v.Content[0], 0, stop)
	assert.False(t, ok, "a mapping is not a sequence")
}

func TestItemSpanRefusesADashAtAnotherColumn(t *testing.T) {
	// A hand-made node claims a dash column that the text does not show.
	// The scan must refuse it, never guess another line.
	doc := parsed(t, "pin:\n  - rule: a\n  - rule: b\n")
	_, seq, idx := doc.Section("pin")

	moved := *seq
	moved.Column = 5

	_, _, ok := doc.ItemSpan(&moved, 0, doc.SectionEnd(idx)+1)
	assert.False(t, ok)
}

// keyCase is one value shape for KeySpan: the span lines, whether the
// value is inline, and the text between Start and End.
type keyCase struct {
	name, text, key string
	item            int
	first, last     int
	inline          bool
	value           string
}

var keyCases = []keyCase{
	{"plain value with an aligned comment", "pin:\n  - rule: RUF001                 # c\n    region: scripts              # m\n", "region", 0, 3, 3, true, "scripts"},
	{"first key on the dash line", "pin:\n  - rule: RUF001                 # c\n    region: scripts\n", "rule", 0, 2, 2, true, "RUF001"},
	{"flow list with a comment", "pin:\n  - rule: a\n    regions: [api, \"d,b\"]   # set\n", "regions", 0, 3, 3, true, `[api, "d,b"]`},
	{"empty flow list", "pin:\n  - rule: a\n    regions: []\n", "regions", 0, 3, 3, true, `[]`},
	{"double-quoted value with a hash", "pin:\n  - rule: a\n    note: \"Keep # quoted\"  # c\n", "note", 0, 3, 3, true, `"Keep # quoted"`},
	{"single-quoted star", "pin:\n  - rule: a\n    region: '*'\n", "region", 0, 3, 3, true, `'*'`},
	{"empty value with a comment", "pin:\n  - rule: a\n    region:   # c\n    note: x\n", "region", 0, 3, 3, true, ""},
	{"empty value at the line end", "pin:\n  - rule: a\n    region:\n    note: x\n", "region", 0, 3, 3, true, ""},
	{"tilde", "pin:\n  - rule: a\n    region: ~\n", "region", 0, 3, 3, true, "~"},
	{"tagged value", "pin:\n  - rule: a\n    region: !!str api # t\n", "region", 0, 3, 3, true, "!!str api"},
	{"bare tag", "pin:\n  - rule: a\n    region: !!null  # c\n", "region", 0, 3, 3, true, "!!null"},
	{"value with multi-byte text before it", "pin:\n  - rule: é\n    region: ü/api\n", "region", 0, 3, 3, true, "ü/api"},
	{"block list", "pin:\n  - rule: a\n    regions:\n      - api\n      - db\n", "regions", 0, 3, 5, false, ""},
	{"block scalar", "pin:\n  - rule: a\n    note: |\n      block\n", "note", 0, 3, 4, false, ""},
	{"multi-line plain scalar", "pin:\n  - rule: a\n    note: multi\n      line plain\n", "note", 0, 3, 4, false, ""},
	{"multi-line flow list", "pin:\n  - rule: a\n    regions: [a,\n      b]\n", "regions", 0, 3, 4, false, ""},
	{"multi-line quoted scalar", "pin:\n  - rule: a\n    note: \"a\n      # b\"\n", "note", 0, 3, 4, false, ""},
	{"value on its own line", "pin:\n  - rule: a\n    region:\n      api\n", "region", 0, 3, 4, false, ""},
	{"alias", "base: &r api\npin:\n  - rule: a\n    region: *r\n", "region", 0, 4, 4, false, ""},
	{"flow mapping value", "pin:\n  - rule: a\n    region: {a: b}\n", "region", 0, 3, 3, false, ""},
	{"dash alone", "pin:\n  -\n    rule: d\n    region: x\n", "rule", 0, 3, 3, true, "d"},
	{
		"comments after a key", "pin:\n  - rule: a\n    regions:\n      - api\n      # deeper comment in the list\n    # comment at the key column\n    note: x\n",
		"regions", 0, 3, 5, false, "",
	},
	{"second item", "pin:\n  - rule: a\n  - rule: b\n    region: api\n  - rule: c\n", "region", 1, 4, 4, true, "api"},
}

func TestDashAtNeedsWhiteSpaceOrTheLineEnd(t *testing.T) {
	// The pin verifier finds the dash line with this rule, so it must
	// tell an item dash from a document marker and a negative number.
	cases := []struct {
		line string
		at   int
		want bool
	}{
		{"  - rule: a", 2, true},
		{"  -", 2, true},
		{"-\trule: a", 0, true},
		{"  -1", 2, false},
		{"---", 0, false},
		{"  rule: a", 2, false},
		{"  -", 3, false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, DashAt(tc.line, tc.at), "%q at %d", tc.line, tc.at)
	}
}

func TestKeySpanLocatesAValueInAnItem(t *testing.T) {
	for _, c := range keyCases {
		t.Run(c.name, func(t *testing.T) {
			doc, seq, stop := pinSequence(t, c.text)

			_, last, ok := doc.ItemSpan(seq, c.item, stop)
			require.True(t, ok)

			s, ok := doc.KeySpan(seq.Content[c.item], c.key, last+1)
			require.True(t, ok)

			assert.Equal(t, c.key, s.Key.Value)
			assert.Same(t, seq.Content[c.item].Content[s.Index+1], s.Value, "Index is the key index in the mapping")
			assert.Equal(t, c.first, s.First)
			assert.Equal(t, c.last, s.Last)
			assert.Equal(t, c.inline, s.Inline)

			if c.inline {
				assert.Equal(t, c.value, doc.Lines[s.First-1][s.Start:s.End])
			} else {
				assert.Zero(t, s.Start)
				assert.Zero(t, s.End)
			}
		})
	}
}

func TestKeySpanRefusesWhatItCannotLocate(t *testing.T) {
	doc, seq, stop := pinSequence(t, "pin:\n  - rule: a\n  - {rule: b}\n  - text\n")

	_, ok := doc.KeySpan(seq.Content[0], "region", stop)
	assert.False(t, ok, "an absent key")

	_, ok = doc.KeySpan(seq.Content[1], "rule", stop)
	assert.False(t, ok, "a flow mapping has no line per key")

	_, ok = doc.KeySpan(seq.Content[2], "rule", stop)
	assert.False(t, ok, "a scalar item is not a mapping")

	_, ok = doc.KeySpan(nil, "rule", stop)
	assert.False(t, ok, "no node")
}

func TestReplaceValueKeepsTheKeyAndTheComment(t *testing.T) {
	cases := []struct {
		name, text, key, value, want string
	}{
		{"plain value", "pin:\n  - rule: a\n    region: scripts              # m\n", "region", "api", "    region: api              # m"},
		{"empty value with a comment", "pin:\n  - rule: a\n    region:   # c\n", "region", "'*'", "    region: '*'   # c"},
		{"empty value at the line end", "pin:\n  - rule: a\n    region:\n", "region", "'*'", "    region: '*'"},
		{"flow list", "pin:\n  - rule: a\n    regions: [api, \"d,b\"]   # set\n", "regions", "[api, db, core]", "    regions: [api, db, core]   # set"},
		{"tag goes with the old value", "pin:\n  - rule: a\n    region: !!str api # t\n", "region", "db", "    region: db # t"},
		{"bare tag", "pin:\n  - rule: a\n    region: !!null  # c\n", "region", "x", "    region: x  # c"},
		{"first key on the dash line", "pin:\n  - rule: a   # c\n", "rule", "b", "  - rule: b   # c"},
		{"quoted value", "pin:\n  - rule: a\n    region: 'it''s' # q\n", "region", "x", "    region: x # q"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, seq, stop := pinSequence(t, c.text)

			s, ok := doc.KeySpan(seq.Content[0], c.key, stop)
			require.True(t, ok)

			line, ok := doc.ReplaceValue(s, c.value)
			require.True(t, ok)
			assert.Equal(t, c.want, line)

			lines := slices.Clone(doc.Lines)
			lines[s.First-1] = line

			after := decodeTop(t, string(JoinLines(lines, doc.FinalEOL)))
			want := decodeTop(t, c.text)

			var value any
			require.NoError(t, yaml.Unmarshal([]byte(c.value), &value))

			want["pin"].([]any)[0].(map[string]any)[c.key] = value
			assert.Equal(t, want, after, "only the value changed")
		})
	}
}

func TestReplaceValueRefusesAValueThatIsNotInline(t *testing.T) {
	doc, seq, stop := pinSequence(t, "pin:\n  - rule: a\n    regions:\n      - api\n")

	s, ok := doc.KeySpan(seq.Content[0], "regions", stop)
	require.True(t, ok)

	_, ok = doc.ReplaceValue(s, "[db]")
	assert.False(t, ok)
}
