package pinfile

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestListingOfTheEmittedLayout(t *testing.T) {
	d := Parse([]byte(emitted))

	require.NoError(t, d.DecodeErr)
	require.NoError(t, d.ReaderErr)
	assert.Empty(t, d.FileBlock)
	assert.Zero(t, d.ErrLine)
	require.Len(t, d.Entries, 3)

	want := []Entry{
		{
			Index: 0, Line: 7, EndLine: 9, Rule: "single", Region: "api", Note: "Short note.",
			Raw: "  - rule: single\n    region: api\n    note: Short note.", Key: reviews.NewPinKey("single", "api", nil),
			ProvenanceIDs: []int64{1},
		},
		{
			Index: 1, Line: 11, EndLine: 14, Rule: "multi", Region: "api", Regions: []string{"api", "db"}, Note: "n",
			Raw: "  - rule: multi\n    region: api\n    regions: [api, db]\n    note: \"n\"", Key: reviews.NewPinKey("multi", "", []string{"api", "db"}),
			ProvenanceIDs: []int64{2},
		},
		{
			Index: 2, Line: 16, EndLine: 22, Rule: "repo", Region: "*", Note: "first line\n# not a comment\n\nlast line",
			Raw: "  - rule: repo\n    region: '*'\n    note: |-\n      first line\n      # not a comment\n\n      last line", Key: reviews.NewPinKey("repo", "", nil),
			ProvenanceIDs: []int64{3},
		},
	}
	assert.Equal(t, want, d.Entries)

	// The listing reads what the delivery reader reads.
	cfg, err := reviews.ParseConfig([]byte(emitted))
	require.NoError(t, err)

	for i, e := range d.Entries {
		assert.Equal(t, reviews.NewPinKey(cfg.Pin[i].Rule, cfg.Pin[i].Region, cfg.Pin[i].Regions), e.Key)
	}
}

func TestEntryRefHashesTheOwnLines(t *testing.T) {
	e := entryOf(t, Parse([]byte(emitted)), "multi")

	assert.Equal(t, EntryRef{Index: 1, Key: e.Key, RawSHA: sha256.Sum256([]byte(e.Raw))}, e.Ref())
}

func TestListingOfEverySupportedLayout(t *testing.T) {
	cases := []struct {
		name, text string
		rules      []string
		lines      [][2]int
	}{
		{"emitted", emitted, []string{"single", "multi", "repo"}, [][2]int{{7, 9}, {11, 14}, {16, 22}}},
		{"docs", docsLayout, []string{"RUF001", "validate-at-the-boundary"}, [][2]int{{7, 9}, {10, 13}}},
		{"zero indent", readmeLayout, []string{"keep-parallel-implementations-consistent", "second"}, [][2]int{{2, 7}, {8, 9}}},
		{"four-space dashes", deepLayout, []string{"a", "b", "c"}, [][2]int{{3, 7}, {9, 14}, {15, 18}}},
		{"dash alone", dashAlone, []string{"lonely", "next"}, [][2]int{{2, 5}, {6, 7}}},
		{"byte order mark", "\ufeffpin:\n  - rule: a\n    region: api\n", []string{"a"}, [][2]int{{2, 3}}},
		{"absent", "", nil, nil},
		{"comments only", "# nothing\n", nil, nil},
		{"null pin", "pin:\n", nil, nil},
		{"tilde pin", "threshold: 2\npin: ~\n", nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Parse([]byte(tc.text))

			assert.Empty(t, d.FileBlock)
			require.NoError(t, d.ReaderErr)
			require.Len(t, d.Entries, len(tc.rules))

			for i, e := range d.Entries {
				assert.Equal(t, tc.rules[i], e.Rule)
				assert.Equal(t, tc.lines[i], [2]int{e.Line, e.EndLine}, e.Rule)
				assert.Empty(t, e.Block, e.Rule)
			}
		})
	}
}

func TestListingKeepsUnknownKeysAndProvenanceOfTheRightEntry(t *testing.T) {
	d := Parse([]byte(deepLayout))
	assert.Equal(t, []string{"extra"}, entryOf(t, d, "b").Unknown)

	// A typo of regions is an unknown key, and the pin reads repo-wide.
	typo := Parse([]byte("pin:\n  - rule: t\n    regoins: [api]\n    enabled: false\n"))
	e := entryOf(t, typo, "t")
	assert.Equal(t, []string{"regoins", "enabled"}, e.Unknown)
	assert.Equal(t, reviews.NewPinKey("t", "", nil), e.Key)
	assert.Empty(t, typo.FileBlock, "an unknown key does not block an edit")

	// The provenance comment after an entry with column-0 dashes is an own
	// line of that entry, never the provenance of the next one.
	readme := Parse([]byte(readmeLayout))
	assert.Empty(t, entryOf(t, readme, "second").ProvenanceIDs)
	assert.Contains(t, entryOf(t, readme, "keep-parallel-implementations-consistent").Raw, "p60)")
}

func TestPinFieldsAreTheKeysOfPinRule(t *testing.T) {
	// The listing calls every other key "unknown key (no reader uses it)".
	// A new PinRule field must therefore join pinFields, or the console
	// would call a key that the readers use unknown.
	var keys []string

	pin := reflect.TypeFor[reviews.PinRule]()
	for i := range pin.NumField() {
		name, _, _ := strings.Cut(pin.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(pin.Field(i).Name)
		}

		keys = append(keys, name)
	}

	assert.ElementsMatch(t, keys, pinFields)
}

func TestListingCountsEmptyItemsInTheIndex(t *testing.T) {
	// reviews.Config drops an empty item, so its Pin slice is shorter than
	// the sequence. Index follows the YAML text.
	d := Parse([]byte("pin:\n  - rule: a\n  -\n  - rule: b\n"))

	require.Len(t, d.Entries, 2)
	assert.Equal(t, 0, d.Entries[0].Index)
	assert.Equal(t, 2, d.Entries[1].Index)
	assert.Equal(t, 4, d.Entries[1].Line)
	assert.Equal(t, "the pin list holds an empty entry (line 3)", d.FileBlock)
}

func TestListingOfACRLFFile(t *testing.T) {
	crlf := strings.ReplaceAll(emitted, "\n", "\r\n")
	d := Parse([]byte(crlf))

	assert.Equal(t, "the file uses CRLF line breaks", d.FileBlock)
	require.NoError(t, d.ReaderErr)

	lf := Parse([]byte(emitted))
	require.Len(t, d.Entries, len(lf.Entries))

	for i := range lf.Entries {
		assert.Equal(t, lf.Entries[i], d.Entries[i], "the line numbers and the text of the LF form")
	}

	_, err := Plan(d, []Edit{{Ref: d.Entries[0].Ref(), Regions: []string{"web"}}}, nil)
	require.ErrorIs(t, err, ErrUnsupported)
	assert.Contains(t, err.Error(), "CRLF")
}

func TestReadErrorsNeverEmptyTheListing(t *testing.T) {
	t.Run("reader rejects a value", func(t *testing.T) {
		d := Parse([]byte("hook_delivery: sometimes\npin:\n  - rule: a\n    region: api\n"))

		require.ErrorIs(t, d.ReaderErr, reviews.ErrLessonsConfig)
		require.NoError(t, d.DecodeErr)
		require.Len(t, d.Entries, 1)
		assert.Equal(t, "the lessons reader rejects the file", d.FileBlock)
		assert.Zero(t, d.ErrLine)
	})

	t.Run("reader rejects one entry", func(t *testing.T) {
		d := Parse([]byte("pin:\n  - rule: a\n    regions: api\n  - rule: b\n    region: db\n"))

		require.Error(t, d.ReaderErr)
		assert.Equal(t, 3, d.ErrLine)
		assert.Equal(t, "the lessons reader rejects the file (line 3)", d.FileBlock)
		require.Len(t, d.Entries, 2)
		assert.Equal(t, "the entry does not load as a pin (line 2)", d.Entries[0].Block)
		assert.Equal(t, "a", d.Entries[0].Rule, "the fields that decode stay")
		assert.Equal(t, reviews.NewPinKey("b", "db", nil), d.Entries[1].Key)
	})

	t.Run("syntax error", func(t *testing.T) {
		d := Parse([]byte("pin:\n  - rule: a\n    note: [open\n"))

		require.Error(t, d.DecodeErr)
		require.Error(t, d.ReaderErr)
		assert.Equal(t, 3, d.ErrLine)
		assert.Contains(t, d.FileBlock, "the file is not valid YAML")
		assert.Empty(t, d.Entries)
	})

	t.Run("second document", func(t *testing.T) {
		d := Parse([]byte("pin:\n  - rule: a\n---\npin:\n  - rule: b\n"))

		require.NoError(t, d.ReaderErr)
		require.Len(t, d.Entries, 1, "the readers read the first document only")
		assert.Equal(t, "a", d.Entries[0].Rule)
		assert.Equal(t, "the file holds more than one YAML document (line 3)", d.FileBlock)
		assert.Equal(t, 2, d.Entries[0].EndLine)
	})
}

// The pin files whose delivered pins the listing cannot see: an alias
// pin value and a pin key from a top-level merge key.
const (
	aliasPinValue = "defs: &p\n  - rule: X\n    region: api\npin: *p\n"
	mergedPinKey  = "base: &b\n  pin:\n    - rule: X\n      region: api\n<<: *b\n"
)

func TestAListingThatMissesDeliveredPinsIsUnreadable(t *testing.T) {
	cases := []struct {
		name, text, reason string
	}{
		{"alias pin value", aliasPinValue, "the listing differs from the delivered pins: the pin section uses an alias (*p) (line 4)"},
		{"pin key from a merge key", mergedPinKey, "the listing differs from the delivered pins: the top level uses a merge key (<<) (line 5)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The reader delivers the pin, so the file is not empty.
			cfg, err := reviews.ParseConfig([]byte(tc.text))
			require.NoError(t, err)
			require.Len(t, cfg.Pin, 1)

			d := Parse([]byte(tc.text))

			require.NoError(t, d.DecodeErr)
			require.NoError(t, d.ReaderErr)
			assert.Empty(t, d.Entries)
			require.EqualError(t, d.ListErr, tc.reason)
			assert.True(t, d.Unreadable(), "an empty listing of delivered pins is an error, not an absence")
			assert.NotEmpty(t, d.FileBlock)
		})
	}
}

func TestAListingThatMatchesTheReaderIsReadable(t *testing.T) {
	cases := map[string]string{
		"emitted":     emitted,
		"empty":       "",
		"null pin":    "pin:\n",
		"null item":   "pin:\n  - rule: a\n  -\n  - rule: b\n",
		"empty entry": "pin:\n  - {}\n  - rule: a\n",
		"duplicates":  "pin:\n  - rule: a\n  - rule: A\n",
		"second doc":  "pin:\n  - rule: a\n---\npin:\n  - rule: b\n",
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			d := Parse([]byte(text))

			require.NoError(t, d.ListErr)
			assert.False(t, d.Unreadable())
		})
	}

	var none *Document
	assert.True(t, none.Unreadable(), "no document gives no listing")
}

func TestTheEditingGateBlocksUnsafeLayouts(t *testing.T) {
	cases := []struct {
		name, text, reason string
		entries            int
	}{
		{"flow pin list", "pin: [{rule: a, region: api}]\n", "the pin section is not a block list (line 1)", 1},
		{"empty flow pin list", "pin: []\n", "the pin section is not a block list (line 1)", 0},
		{"anchor and alias", "pin:\n  - &x\n    rule: a\n  - *x\n", "the pin section uses an anchor (&x) (line 2)", 2},
		{"merge key", "base: &b\n  note: n\npin:\n  - <<: *b\n    rule: a\n", "the pin section uses a merge key (<<) (line 4)", 1},
		{"tag", "pin:\n  - rule: !!str a\n", "the pin section uses a tag (!!str) (line 2)", 1},
		{"tagged list", "pin: !!seq\n  - rule: a\n", "the pin section uses a tag (!!seq) (line 1)", 1},
		{"anchored pin key", "&k pin:\n  - rule: a\n", "the pin key carries a tag or an anchor (line 1)", 1},
		{"duplicate key in an item", "pin:\n  - rule: a\n    rule: b\n", "the lessons reader rejects the file (line 3)", 1},
		{"duplicate top key", "pin:\n  - rule: a\npin:\n  - rule: b\n", `duplicate key "pin" (first at line 1) (line 3)`, 1},
		{"empty item", "pin:\n  -\n  - rule: a\n", "the pin list holds an empty entry (line 2)", 1},
		{"tab in a block note", "pin:\n  - rule: a\n    note: |\n      x\n      \tcode\n", "the file indents a line with a tab (line 5)", 1},
		{"tab before a comment", "pin:\n  - rule: a\n\t# tab\n", "found a tab character that violates indentation", 0},
		{"bare CR", "pin:\r  - rule: a\r", "the file uses bare CR line breaks (line 1)", 1},
		{"mixed line breaks", "pin:\r\n  - rule: a\n", "the file mixes CRLF and LF line breaks", 1},
		{"top level list", "- rule: a\n", "the top level of the file is not a mapping (line 1)", 0},
		{"pin mapping", "pin:\n  rule: a\n", "the lessons reader rejects the file (line 2)", 0},
		{"nested list item", "pin:\n  - - a\n", "the lessons reader rejects the file (line 2)", 0},
		{"line separator", "pin:\n  - rule: a\u2028b\n", "the file holds the line break U+2028", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Parse([]byte(tc.text))

			assert.Contains(t, d.FileBlock, tc.reason)
			assert.Len(t, d.Entries, tc.entries, "the listing stays")

			for _, e := range d.Entries {
				_, err := Plan(d, []Edit{{Ref: e.Ref(), Delete: true}}, nil)
				require.ErrorIs(t, err, ErrUnsupported)
			}
		})
	}
}

func TestEntryLevelBlocksLeaveTheOtherEntriesEditable(t *testing.T) {
	cases := []struct {
		name, text, rule, reason string
	}{
		{"flow mapping item", "pin:\n  - {rule: f, region: api}\n  - rule: ok\n", "f", "the entry uses flow style { } (line 2)"},
		{"multi-line plain rule", "pin:\n  - rule: long\n      rule\n  - rule: ok\n", "long rule", "the rule value spans several lines (line 2)"},
		{"block scalar region", "pin:\n  - rule: r\n    region: |\n      api\n  - rule: ok\n", "r", "the region value spans several lines (line 3)"},
		{"region on the next line", "pin:\n  - rule: r\n    region:\n      api\n  - rule: ok\n", "r", "the region value spans several lines (line 3)"},
		{"two-line flow regions", "pin:\n  - rule: r\n    regions: [api,\n      db]\n  - rule: ok\n", "r", "the regions list spans several lines (line 3)"},
		{"block regions item on two lines", "pin:\n  - rule: r\n    regions:\n      - \"api\n        x\"\n  - rule: ok\n", "r", "a regions item is not one text value on its own line (line 4)"},
		{"empty block regions item", "pin:\n  - rule: r\n    regions:\n      -\n      - db\n  - rule: ok\n", "r", "a regions item is not one text value on its own line (line 4)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Parse([]byte(tc.text))

			require.NoError(t, d.ReaderErr)
			assert.Empty(t, d.FileBlock)
			assert.Equal(t, tc.reason, entryOf(t, d, tc.rule).Block)
			assert.Empty(t, entryOf(t, d, "ok").Block)

			c, err := Plan(d, []Edit{{Ref: entryOf(t, d, "ok").Ref(), Delete: true}}, nil)
			require.NoError(t, err)
			assert.NotContains(t, string(c.After), "rule: ok")
		})
	}
}

// writeFile writes body as the pin file under a new root with the mode.
func writeFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(pinPath(root), []byte(body), mode))
	require.NoError(t, os.Chmod(pinPath(root), mode))

	return root
}

// pinPath returns the absolute path of the pin file under root.
func pinPath(root string) string {
	return filepath.Join(root, ".seamark", "lessons.yaml")
}

func TestReadObservesTheFile(t *testing.T) {
	t.Run("absent file", func(t *testing.T) {
		d, err := Read(t.TempDir())

		require.NoError(t, err)
		assert.False(t, d.Guard.Exists)
		assert.Empty(t, d.Entries)
		assert.Empty(t, d.FileBlock)
		assert.Nil(t, d.Bytes)
	})

	t.Run("regular file", func(t *testing.T) {
		d, err := Read(writeFile(t, emitted, 0o640))

		require.NoError(t, err)
		assert.True(t, d.Guard.Exists)
		assert.Equal(t, sha256.Sum256([]byte(emitted)), d.Guard.SHA256)
		assert.Equal(t, os.FileMode(0o640), d.Guard.Mode)
		assert.Equal(t, emitted, string(d.Bytes))
		assert.Len(t, d.Entries, 3)
		assert.Empty(t, d.FileBlock)
	})

	t.Run("symlinked file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "real.yaml"), []byte(emitted), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.Symlink("../real.yaml", pinPath(root)))

		d, err := Read(root)

		require.NoError(t, err)
		assert.Equal(t, ".seamark/lessons.yaml", d.Guard.Linked)
		assert.Len(t, d.Entries, 3, "the readers follow the link, so the listing does too")
		assert.Equal(t, "symlink at .seamark/lessons.yaml; seamark writes only real paths inside the repository", d.FileBlock)
		assert.Equal(t, integration.WriteRefusal(d.Guard), d.FileBlock, "the engine text")
	})

	t.Run("symlinked directory", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "elsewhere", "lessons.yaml"), []byte(emitted), 0o644))
		require.NoError(t, os.Symlink("elsewhere", filepath.Join(root, ".seamark")))

		d, err := Read(root)

		require.NoError(t, err)
		assert.Contains(t, d.FileBlock, "symlink at .seamark;")
	})

	t.Run("read-only file", func(t *testing.T) {
		d, err := Read(writeFile(t, emitted, 0o444))

		require.NoError(t, err)
		assert.Equal(t, "the file is read-only (-r--r--r--); seamark does not replace it", d.FileBlock)
		assert.Equal(t, integration.WriteRefusal(d.Guard), d.FileBlock, "the engine text")
	})

	t.Run("a structural block names the structure first", func(t *testing.T) {
		d, err := Read(writeFile(t, "pin:\n  -\n", 0o444))

		require.NoError(t, err)
		assert.Contains(t, d.FileBlock, "empty entry")
	})

	t.Run("not a regular file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(pinPath(root), 0o755))

		_, err := Read(root)
		require.ErrorContains(t, err, "not a regular file")
	})

	t.Run("no root", func(t *testing.T) {
		_, err := Read("")
		require.Error(t, err)
	})
}
