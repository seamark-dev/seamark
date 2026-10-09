package pinfile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// swap returns s with the one occurrence of old replaced by next. A
// golden built with it shows the exact change, and the helper fails when
// old is not unique, so a golden never matches by accident.
func swap(t *testing.T, s, old, next string) string {
	t.Helper()

	require.Equal(t, 1, strings.Count(s, old), "the golden anchor must occur once: %q", old)

	return strings.Replace(s, old, next, 1)
}

// entryOf returns the one entry of d with the rule.
func entryOf(t *testing.T, d *Document, rule string) Entry {
	t.Helper()

	var found []Entry

	for _, e := range d.Entries {
		if e.Rule == rule {
			found = append(found, e)
		}
	}

	require.Len(t, found, 1, "entries with rule %q", rule)

	return found[0]
}

// planOne plans one edit of the entry with the rule and returns the
// candidate text, or the error.
func planOne(t *testing.T, before, rule string, edit Edit) (string, error) {
	t.Helper()

	d := Parse([]byte(before))
	edit.Ref = entryOf(t, d, rule).Ref()

	c, err := Plan(d, []Edit{edit}, nil)
	if err != nil {
		assert.Nil(t, c, "a single failing edit leaves no change")

		return "", err
	}

	require.NotNil(t, c)
	assert.Equal(t, before, string(c.Before))

	return string(c.After), nil
}

// emitted is the layout that distill.RenderPins and an apply write,
// with a block note that holds a "#" line and a blank line.
const emitted = "# Review-lesson tuning (see `seamark lessons --list`).\n" + `threshold: 3
mute:
  - rule: F541
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
      first line
      # not a comment

      last line
pin_budget: 2
`

// docsLayout is the layout of docs/lessons.md: aligned trailing comments
// on the keys, settings above the list.
const docsLayout = `threshold: 2                     # min recurrences to surface (default 2)
pin_budget: 3                    # pins the edit hook injects per edit (default 3)
hook_delivery: once-per-context  # suppress repeats until context compaction
mute:
  - rule: F541                   # hush a noisy rule everywhere
pin:                             # your "must not be ignored" list
  - rule: RUF001                 # surfaced always for its region
    region: scripts              # mining found it once or never
    note: "Keep scripts ASCII"
  - rule: validate-at-the-boundary
    region: api                  # a theme living in two places names both:
    regions: [api, db]           # a set beats a repo-wide star
    note: "Enforce closed sets at the edge."
`

// readmeLayout has dashes at column 0, a multi-line quoted note, and a
// provenance comment after the entry, as in README.md.
const readmeLayout = `pin:
- rule: keep-parallel-implementations-consistent
  region: api
  regions: [api, core]
  note: "When two runtimes share a contract, mirror validation
    strictness and regenerate derived artifacts."
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p60)
- rule: second
  note: x
`

// deepLayout has dashes at four spaces, keys in another order, comments
// and blank lines between and inside items, and a block regions list.
const deepLayout = `pin:
    # user comment about a
    - rule: a

      # why api
      region: api
      note: n

    - note: first key note
      rule: b
      regions:
        - api   # the api
        - db
      extra: kept
    - rule: c
      regions:
        - api
        - db
`

// regionsFirst puts the regions key first, on the dash line.
const regionsFirst = "pin:\n  - regions: [api, db]\n    rule: X\n    note: n\n"

// dashAlone puts the dash on its own line above the first key.
const dashAlone = `pin:
  -
    rule: lonely
    region: api
    note: n
  - rule: next
    note: m
`

func TestRetargetGoldens(t *testing.T) {
	cases := []struct {
		name, before, rule string
		regions            []string
		after              func(t *testing.T, s string) string
	}{
		// Case C: only region.
		{"C one region", emitted, "single", []string{"web"}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n    region: api\n", "  - rule: single\n    region: web\n")
		}},
		{"C set", emitted, "single", []string{"api", "web"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    note: Short note.", "    region: api\n    regions: [api, web]\n    note: Short note.")
		}},
		{"C repository-wide", emitted, "single", nil, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n    region: api\n", "  - rule: single\n    region: '*'\n")
		}},
		{"C from star to one region before a block note", emitted, "repo", []string{"api"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: '*'\n", "    region: api\n")
		}},
		{"C from star to a set", emitted, "repo", []string{"api", "db"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: '*'\n", "    region: api\n    regions: [api, db]\n")
		}},
		// Case A: region and regions.
		{"A set", emitted, "multi", []string{"db", "web"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    regions: [api, db]\n", "    region: db\n    regions: [db, web]\n")
		}},
		{"A one region removes regions", emitted, "multi", []string{"db"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    regions: [api, db]\n", "    region: db\n")
		}},
		{"A repository-wide", emitted, "multi", nil, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    regions: [api, db]\n", "    region: '*'\n")
		}},
		// Case B: only regions.
		{"B set", "pin:\n  - rule: setonly\n    regions: [api, db]   # chosen by hand\n    note: n\n", "setonly", []string{"web"},
			func(t *testing.T, s string) string {
				return swap(t, s, "[api, db]   #", "[web]   #")
			}},
		{"B repository-wide", "pin:\n  - rule: setonly\n    regions: [api, db]\n    note: n\n", "setonly", nil,
			func(t *testing.T, s string) string {
				return swap(t, s, "    regions: [api, db]\n", "    region: '*'\n")
			}},
		// Case D: neither key.
		{"D one region", "pin:\n  - rule: bare\n    note: everywhere\n", "bare", []string{"api"}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: bare\n", "  - rule: bare\n    region: api\n")
		}},
		{"D set", "pin:\n  - rule: bare\n    note: everywhere\n", "bare", []string{"api", "db"}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: bare\n", "  - rule: bare\n    region: api\n    regions: [api, db]\n")
		}},
		// Trailing comments keep their place after the new value.
		{"docs layout keeps every comment", docsLayout, "validate-at-the-boundary", []string{"core", "db"}, func(t *testing.T, s string) string {
			s = swap(t, s, "    region: api                  # a theme", "    region: core                  # a theme")

			return swap(t, s, "    regions: [api, db]           # a set", "    regions: [core, db]           # a set")
		}},
		{"docs layout single region", docsLayout, "RUF001", []string{"bin"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: scripts              # mining", "    region: bin              # mining")
		}},
		// Dashes at column 0 with a multi-line note and a provenance line inside.
		{"zero indent set", readmeLayout, "keep-parallel-implementations-consistent", []string{"api", "core", "web"}, func(t *testing.T, s string) string {
			return swap(t, s, "  regions: [api, core]\n", "  regions: [api, core, web]\n")
		}},
		{"zero indent insert after rule", readmeLayout, "second", []string{"lib"}, func(t *testing.T, s string) string {
			return swap(t, s, "- rule: second\n", "- rule: second\n  region: lib\n")
		}},
		// Four-space dashes, blank lines and comments inside items.
		{"deep layout region", deepLayout, "a", []string{"api", "db"}, func(t *testing.T, s string) string {
			return swap(t, s, "      region: api\n      note: n\n", "      region: api\n      regions: [api, db]\n      note: n\n")
		}},
		{"block list replaces by position and keeps the comment", deepLayout, "b", []string{"web"}, func(t *testing.T, s string) string {
			return swap(t, s, "        - api   # the api\n        - db\n      extra", "        - web   # the api\n      extra")
		}},
		{"block list grows with the item indent", deepLayout, "c", []string{"api", "db", "lib"}, func(t *testing.T, s string) string {
			return swap(t, s, "        - api\n        - db\n", "        - api\n        - db\n        - lib\n")
		}},
		{"block list without comments becomes repository-wide", deepLayout, "c", nil, func(t *testing.T, s string) string {
			return swap(t, s, "      regions:\n        - api\n        - db\n", "      region: '*'\n")
		}},
		// The dash alone on its line.
		{"dash alone", dashAlone, "lonely", []string{"db"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n", "    region: db\n")
		}},
		// Keys in another order: the region key is the last key.
		{"region as the last key", "pin:\n  - note: first\n    rule: late\n    region: api\n", "late", []string{"api", "db"},
			func(t *testing.T, s string) string {
				return s + "    regions: [api, db]\n"
			}},
		// An empty region value is replaced in place.
		{"empty region value", "pin:\n  - rule: e\n    region:\n    note: n\n", "e", []string{"api"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region:\n", "    region: api\n")
		}},
		// Quoted values take the encoder's text; the comments stay.
		{"quoted values", "pin:\n  - rule: q\n    region: \"api\"   # quoted\n    regions: ['api', \"db\"]  # set\n", "q", []string{"web", "db"},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: q\n    region: web   # quoted\n    regions: [web, db]  # set\n"
			}},
		{"empty region value with a comment", "pin:\n  - rule: e\n    region:   # later\n", "e", []string{"api"}, func(t *testing.T, s string) string {
			return swap(t, s, "    region:   # later\n", "    region: api   # later\n")
		}},
		// The regions key is the first key, on the dash line. The dash stays.
		{"B on the dash line becomes repository-wide", regionsFirst, "X", nil, func(t *testing.T, s string) string {
			return swap(t, s, "  - regions: [api, db]\n", "  - region: '*'\n")
		}},
		{"B on the dash line keeps a new set", regionsFirst, "X", []string{"web"}, func(t *testing.T, s string) string {
			return swap(t, s, "  - regions: [api, db]\n", "  - regions: [web]\n")
		}},
		{"B block list on the dash line becomes repository-wide", "pin:\n  - regions:\n      - api\n      - db\n    rule: X\n", "X", nil,
			func(t *testing.T, s string) string {
				return "pin:\n  - region: '*'\n    rule: X\n"
			}},
		{"A on the dash line lifts the region key", "pin:\n  - regions: [api, db]\n    region: api   # mine\n    rule: X\n", "X", []string{"web"},
			func(t *testing.T, s string) string {
				return "pin:\n  - region: web   # mine\n    rule: X\n"
			}},
		{"A on the dash line lifts the region key over another key", "pin:\n  - regions: [api, db]\n    rule: X\n    region: api\n    note: n\n", "X", nil,
			func(t *testing.T, s string) string {
				return "pin:\n  - region: '*'\n    rule: X\n    note: n\n"
			}},
		{"A block list on the dash line lifts the region key", "pin:\n  - regions:\n      - api\n      - db\n    region: api\n    rule: X\n", "X", []string{"db"},
			func(t *testing.T, s string) string {
				return "pin:\n  - region: db\n    rule: X\n"
			}},
		{"A on the dash line keeps a new set", "pin:\n  - regions: [api, db]\n    region: api\n    rule: X\n", "X", []string{"db", "web"},
			func(t *testing.T, s string) string {
				return "pin:\n  - regions: [db, web]\n    region: db\n    rule: X\n"
			}},
		// A byte order mark stays in front of the first line.
		{"byte order mark", "\ufeffpin:\n  - rule: a\n    region: api\n", "a", []string{"db"}, func(t *testing.T, s string) string {
			return "\ufeffpin:\n  - rule: a\n    region: db\n"
		}},
		// No final line break: the inserted line ends the file the same way.
		{"no final line break", "pin:\n  - rule: last", "last", []string{"api"}, func(t *testing.T, s string) string {
			return s + "\n    region: api"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planOne(t, tc.before, tc.rule, Edit{Regions: tc.regions})

			require.NoError(t, err)
			assert.Equal(t, tc.after(t, tc.before), got)

			// The candidate reads back with the target key and the reviewed
			// rule and note.
			before, after := entryOf(t, Parse([]byte(tc.before)), tc.rule), entryOf(t, Parse([]byte(got)), tc.rule)
			assert.Equal(t, reviews.NewPinKey(tc.rule, "", tc.regions), after.Key)
			assert.Equal(t, before.Note, after.Note)
			assert.Equal(t, before.Unknown, after.Unknown)
			assert.Equal(t, before.Index, after.Index, "the entry keeps its position")
		})
	}
}

func TestPruneGoldens(t *testing.T) {
	cases := []struct {
		name, before, rule string
		provenance         []int64
		after              func(t *testing.T, s string) string
	}{
		{"entry with its provenance", emitted, "single", []int64{1}, func(t *testing.T, s string) string {
			return swap(t, s, "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)\n  - rule: single\n    region: api\n    note: Short note.\n", "")
		}},
		{"next entry keeps its provenance", emitted, "multi", []int64{2}, func(t *testing.T, s string) string {
			return swap(t, s, "  # distilled by claude/v3 from 2 findings (seamark lessons --distill, p2)\n  - rule: multi\n    region: api\n    regions: [api, db]\n    note: \"n\"\n", "")
		}},
		{"last entry with a block note", emitted, "repo", []int64{3}, func(t *testing.T, s string) string {
			return swap(t, s, "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p3)\n  - rule: repo\n    region: '*'\n    note: |-\n      first line\n      # not a comment\n\n      last line\n", "")
		}},
		{"an unlinked provenance line stays", emitted, "single", nil, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n    region: api\n    note: Short note.\n", "")
		}},
		{"docs layout", docsLayout, "RUF001", nil, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: RUF001                 # surfaced always for its region\n    region: scripts              # mining found it once or never\n    note: \"Keep scripts ASCII\"\n", "")
		}},
		{"zero indent takes the provenance line after the entry", readmeLayout, "keep-parallel-implementations-consistent", nil,
			func(t *testing.T, s string) string {
				return "pin:\n- rule: second\n  note: x\n"
			}},
		{"deep layout keeps the head comment and the blank line", deepLayout, "a", nil, func(t *testing.T, s string) string {
			return swap(t, s, "    - rule: a\n\n      # why api\n      region: api\n      note: n\n", "")
		}},
		{"unknown keys go with the entry", deepLayout, "b", nil, func(t *testing.T, s string) string {
			return swap(t, s, "    - note: first key note\n      rule: b\n      regions:\n        - api   # the api\n        - db\n      extra: kept\n", "")
		}},
		{"dash alone", dashAlone, "lonely", nil, func(t *testing.T, s string) string {
			return "pin:\n  - rule: next\n    note: m\n"
		}},
		{"the only entry leaves an empty pin key", "threshold: 2\npin:\n  - rule: a\n    note: n\npin_budget: 1\n", "a", nil,
			func(t *testing.T, s string) string {
				return "threshold: 2\npin:\npin_budget: 1\n"
			}},
		{"byte order mark", "\ufeffpin:\n  - rule: a\n  - rule: b\n", "a", nil, func(t *testing.T, s string) string {
			return "\ufeffpin:\n  - rule: b\n"
		}},
		{"no final line break", "pin:\n  - rule: a\n  - rule: b", "b", nil, func(t *testing.T, s string) string {
			return "pin:\n  - rule: a"
		}},
		// A mute item has the same "- rule:" shape as a pin. The prune
		// binds the entry of the pin list, so the mute of the same name
		// stays.
		{"a mute with the same rule stays", "mute:\n  - rule: shared\n\npin:\n  - rule: shared\n    region: api\n    note: n\n",
			"shared", nil, func(t *testing.T, s string) string {
				return "mute:\n  - rule: shared\n\npin:\n"
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planOne(t, tc.before, tc.rule, Edit{Delete: true, Provenance: tc.provenance})

			require.NoError(t, err)
			assert.Equal(t, tc.after(t, tc.before), got)

			for _, e := range Parse([]byte(got)).Entries {
				assert.NotEqual(t, tc.rule, e.Rule, "the pruned entry is gone")
			}
		})
	}
}

func TestPruneBindsProvenanceByTheWholeID(t *testing.T) {
	// p12 must never read as p1. Two provenance lines above one entry: an
	// orphan from a removed pin and the entry's own line.
	const file = `pin:
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p12)
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)
  - rule: a
    note: n
  - rule: b
`
	p12 := "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p12)\n"
	p1 := "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)\n"
	own := "  - rule: a\n    note: n\n"

	assert.Equal(t, []int64{12, 1}, entryOf(t, Parse([]byte(file)), "a").ProvenanceIDs)

	got, err := planOne(t, file, "a", Edit{Delete: true, Provenance: []int64{1}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, file, p1+own, ""), got, "only the p1 line goes")

	got, err = planOne(t, file, "a", Edit{Delete: true, Provenance: []int64{12}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, file, p12+p1+own, p1), got, "only the p12 line goes")

	got, err = planOne(t, file, "a", Edit{Delete: true, Provenance: []int64{1, 12}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, file, p12+p1+own, ""), got)
}

func TestProvenanceRunStopsAtAUserComment(t *testing.T) {
	// The provenance run is the contiguous seamark comments right above the
	// dash. A user comment between them ends the run, so it stays and the
	// line above it is not bound to the entry.
	const file = `pin:
  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)
  # keep this: it explains the pin
  - rule: a
`
	e := entryOf(t, Parse([]byte(file)), "a")
	assert.Empty(t, e.ProvenanceIDs)

	got, err := planOne(t, file, "a", Edit{Delete: true, Provenance: []int64{1}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, file, "  - rule: a\n", ""), got)
}

func TestProvenanceRunStopsAtABlankLine(t *testing.T) {
	// Only the comment lines directly above the dash are bound (D-8). A
	// blank line separates this provenance line from b, so it stays.
	const file = `pin:
  - rule: a
  # distilled by c from 1 findings (seamark lessons --distill, p2)

  - rule: b
`
	d := Parse([]byte(file))
	assert.Empty(t, entryOf(t, d, "b").ProvenanceIDs)
	assert.Empty(t, entryOf(t, d, "a").ProvenanceIDs)

	got, err := planOne(t, file, "b", Edit{Delete: true, Provenance: []int64{2}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, file, "  - rule: b\n", ""), got)
}

func TestProvenanceIDNeedsTheCanonicalDigitRun(t *testing.T) {
	for line, want := range map[string]int64{
		"  # distilled by c from 1 findings (seamark lessons --distill, p1)":   1,
		"  # distilled by c from 1 findings (seamark lessons --distill, p12)":  12,
		"# distilled by c from 1 findings (seamark lessons --distill, p7)   ":  7,
		"  # distilled by c from 1 findings (seamark lessons --distill, p012)": -1,
		"  # distilled by c from 1 findings (seamark lessons --distill, p1x)":  -1,
		"  # distilled by c from 1 findings (another tool, p1)":                -1,
		"  - rule: a # (seamark lessons --distill, p1)":                        -1,
		"  # (seamark lessons --distill, p99999999999999999999)":               -1,
	} {
		id, ok := provenanceID(line)
		if want < 0 {
			assert.False(t, ok, line)

			continue
		}

		assert.True(t, ok, line)
		assert.Equal(t, want, id, line)
	}
}

func TestRetargetQuotesOddRegionStrings(t *testing.T) {
	// A region text can hold flow indicators, YAML 1.1 words, and a "#".
	// Each must read back as the same string inside the flow list and as
	// the single region value.
	odd := []string{"a,b", "x]y", "on", "#c", "*star", "with space", "née/日本", "a: b", "- dash", "'q'", `"dq"`, "null"}

	for _, first := range odd {
		regions := []string{first, "plain"}

		got, err := planOne(t, emitted, "multi", Edit{Regions: regions})
		require.NoError(t, err, first)

		e := entryOf(t, Parse([]byte(got)), "multi")
		assert.Equal(t, first, e.Region, "the region value reads back")
		assert.Equal(t, regions, e.Regions, "the flow list reads back")
		assert.Equal(t, reviews.NewPinKey("multi", "", regions), e.Key)
	}

	got, err := planOne(t, emitted, "multi", Edit{Regions: []string{"a,b", "x]y", "on"}})
	require.NoError(t, err)
	assert.Contains(t, got, "    region: a,b\n    regions: ['a,b', 'x]y', \"on\"]\n", "the flow context quotes what the block context does not")
}

func TestPlanRefusesUnsafeEdits(t *testing.T) {
	cases := []struct {
		name, before, rule string
		edit               Edit
		want               error
		reason             string
	}{
		{"same set is a no-op", emitted, "multi", Edit{Regions: []string{"db", "api"}}, ErrNoOp, "already current"},
		{"no region is a no-op", "pin:\n  - rule: bare\n", "bare", Edit{}, ErrNoOp, "already current"},
		{"removing a commented regions key", "pin:\n  - rule: s\n    region: api\n    regions: [api, db]  # mine\n", "s",
			Edit{Regions: []string{"api"}}, ErrUnsupported, "would drop a comment"},
		{"removing a commented block item", deepLayout, "b", Edit{Regions: nil}, ErrUnsupported, "would drop a comment"},
		{"removing a block item with a comment", "pin:\n  - rule: s\n    regions:\n      - api\n      - db  # keep\n", "s",
			Edit{Regions: []string{"web"}}, ErrUnsupported, "would drop a comment"},
		{"removing a commented regions key on the dash line", "pin:\n  - regions: [api, db]  # mine\n    region: api\n    rule: s\n", "s",
			Edit{Regions: []string{"web"}}, ErrUnsupported, "would drop a comment"},
		{"lifting a region key that owns a comment line", "pin:\n  - regions: [api, db]\n    region: api\n      # about api\n    rule: s\n", "s",
			Edit{Regions: []string{"web"}}, ErrUnsupported, "would separate a comment"},
		{"an empty region", emitted, "single", Edit{Regions: []string{""}}, ErrUnsupported, "empty region"},
		{"a star region", emitted, "single", Edit{Regions: []string{"api", "*"}}, ErrUnsupported, "empty region"},
		{"white space around a region", emitted, "single", Edit{Regions: []string{" api"}}, ErrUnsupported, "white space"},
		{"a repeated region", emitted, "single", Edit{Regions: []string{"db", "db"}}, ErrUnsupported, "repeats"},
		{"a region with a line break", emitted, "single", Edit{Regions: []string{"a\nb"}}, ErrUnsupported, "control character"},
		{"a region with an escape", emitted, "single", Edit{Regions: []string{"a\x1b[31m"}}, ErrUnsupported, "control character"},
		{"a region that is not UTF-8", emitted, "single", Edit{Regions: []string{"bad\xff"}}, ErrUnsupported, "safe single-line"},
		{"a flow entry", "pin:\n  - {rule: f, region: api}\n", "f", Edit{Regions: []string{"db"}}, ErrUnsupported, "flow style"},
		{"a multi-line rule", "pin:\n  - rule: long\n      rule\n    region: api\n", "long rule", Edit{Regions: []string{"db"}}, ErrUnsupported, "rule value spans"},
		{"a multi-line region", "pin:\n  - rule: r\n    region: \"api\n      x\"\n", "r", Edit{Regions: []string{"db"}}, ErrUnsupported, "region value spans"},
		{"a multi-line flow list", "pin:\n  - rule: r\n    regions: [api,\n      db]\n", "r", Edit{Regions: []string{"web"}}, ErrUnsupported, "regions list spans"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planOne(t, tc.before, tc.rule, tc.edit)

			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.reason)

			list := EditErrors(err)
			require.Len(t, list, 1)
			assert.Equal(t, entryOf(t, Parse([]byte(tc.before)), tc.rule).Ref(), list[0].Ref)
		})
	}
}

func TestPlanBindsTheReviewedEntry(t *testing.T) {
	d := Parse([]byte(emitted))
	ref := entryOf(t, d, "single").Ref()

	t.Run("an edited note is stale", func(t *testing.T) {
		now := Parse([]byte(swap(t, emitted, "note: Short note.", "note: Edited.")))

		_, err := Plan(now, []Edit{{Ref: ref, Delete: true}}, nil)
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "changed since it was read")
	})

	t.Run("a moved entry is stale", func(t *testing.T) {
		now := Parse([]byte(swap(t, emitted, "pin:\n", "pin:\n  - rule: inserted\n")))

		_, err := Plan(now, []Edit{{Ref: ref, Delete: true}}, nil)
		require.ErrorIs(t, err, ErrStale)
	})

	t.Run("a removed entry is stale", func(t *testing.T) {
		_, err := Plan(Parse([]byte("pin:\n")), []Edit{{Ref: ref, Delete: true}}, nil)
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), "no longer in the file")
	})

	t.Run("an absent file is stale", func(t *testing.T) {
		_, err := Plan(Parse(nil), []Edit{{Ref: ref, Delete: true}}, nil)
		require.ErrorIs(t, err, ErrStale)
	})

	t.Run("the same entry twice", func(t *testing.T) {
		_, err := Plan(d, []Edit{{Ref: ref, Delete: true}, {Ref: ref, Regions: []string{"db"}}}, nil)
		require.ErrorIs(t, err, ErrUnsupported)
		assert.Len(t, EditErrors(err), 2)
	})

	t.Run("Plan reads the bytes, not the listing", func(t *testing.T) {
		forged := Parse([]byte(emitted))
		forged.Entries[0].Line, forged.Entries[0].Raw = 99, "forged"
		forged.FileBlock = ""

		c, err := Plan(forged, []Edit{{Ref: ref, Delete: true, Provenance: []int64{1}}}, nil)
		require.NoError(t, err)
		assert.NotContains(t, string(c.After), "rule: single")
	})

	t.Run("no edits", func(t *testing.T) {
		_, err := Plan(d, nil, nil)
		require.Error(t, err)
	})

	t.Run("no document", func(t *testing.T) {
		_, err := Plan(nil, []Edit{{Ref: ref}}, nil)
		require.Error(t, err)
	})
}

func TestPlanRefusesCollisions(t *testing.T) {
	const file = `pin:
  - rule: a
    region: api
  - rule: b
    region: api
  - rule: a
    region: db
  - rule: dup
    region: x
  - rule: DUP
    region: x/
`
	d := Parse([]byte(file))
	a := d.Entries[0]
	b := d.Entries[1]
	aDB := d.Entries[2]

	t.Run("another live entry", func(t *testing.T) {
		_, err := Plan(d, []Edit{{Ref: a.Ref(), Regions: []string{"db"}}}, nil)
		require.ErrorIs(t, err, ErrCollision)
		assert.Contains(t, err.Error(), "another pin already carries the key a @ db")
	})

	t.Run("two planned targets", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: a.Ref(), Regions: []string{"web"}}, {Ref: aDB.Ref(), Regions: []string{"web"}}}, nil)
		require.ErrorIs(t, err, ErrCollision)
		assert.Nil(t, c)
		assert.Len(t, EditErrors(err), 2, "both edits are blocked")
		assert.Contains(t, err.Error(), "another selected entry targets the key a @ web")
	})

	t.Run("an applied proposal elsewhere", func(t *testing.T) {
		_, err := Plan(d, []Edit{{Ref: b.Ref(), Regions: []string{"lib"}}}, []reviews.PinKey{reviews.NewPinKey("B", "lib/", nil)})
		require.ErrorIs(t, err, ErrCollision)
		assert.Contains(t, err.Error(), "an applied proposal already records the key")
	})

	t.Run("a duplicate key blocks both actions", func(t *testing.T) {
		for _, e := range d.Entries[3:] {
			for _, edit := range []Edit{{Ref: e.Ref(), Delete: true}, {Ref: e.Ref(), Regions: []string{"y"}}} {
				_, err := Plan(d, []Edit{edit}, nil)
				require.ErrorIs(t, err, ErrCollision)
				assert.Contains(t, err.Error(), "another pin carries the same key")
			}
		}
	})

	t.Run("the same rule in other regions stays", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: a.Ref(), Delete: true}}, nil)
		require.NoError(t, err)
		assert.Equal(t, swap(t, file, "  - rule: a\n    region: api\n", ""), string(c.After))
	})
}

func TestPlanKeepsTheReadyEditsOfAPartialSelection(t *testing.T) {
	// The planner returns the verified part for review and names every
	// other edit. The caller then shows the reasons and offers no confirm.
	d := Parse([]byte(emitted))
	single, multi := entryOf(t, d, "single"), entryOf(t, d, "multi")

	c, err := Plan(d, []Edit{
		{Ref: single.Ref(), Regions: []string{"web"}},
		{Ref: multi.Ref(), Regions: []string{"db", "api"}},
	}, nil)

	require.ErrorIs(t, err, ErrNoOp)
	require.NotNil(t, c)
	assert.Equal(t, swap(t, emitted, "  - rule: single\n    region: api\n", "  - rule: single\n    region: web\n"), string(c.After))
	require.Len(t, c.Edits, 1)
	assert.Equal(t, single.Ref(), c.Edits[0].Ref)

	list := EditErrors(err)
	require.Len(t, list, 1)
	assert.Equal(t, multi.Ref(), list[0].Ref)
}

func TestPlanCombinesEditsOfSeveralEntries(t *testing.T) {
	d := Parse([]byte(emitted))
	single, multi, repo := entryOf(t, d, "single"), entryOf(t, d, "multi"), entryOf(t, d, "repo")

	t.Run("prune", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: repo.Ref(), Delete: true, Provenance: []int64{3}}, {Ref: single.Ref(), Delete: true, Provenance: []int64{1}}}, nil)
		require.NoError(t, err)

		want := swap(t, emitted, "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)\n  - rule: single\n    region: api\n    note: Short note.\n", "")
		want = swap(t, want, "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p3)\n  - rule: repo\n    region: '*'\n    note: |-\n      first line\n      # not a comment\n\n      last line\n", "")
		assert.Equal(t, want, string(c.After))
		assert.Equal(t, []reviews.PinKey{{}, {}}, c.NewKeys)
	})

	t.Run("retarget", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: single.Ref(), Regions: []string{"web"}}, {Ref: multi.Ref(), Regions: []string{"db"}}}, nil)
		require.NoError(t, err)

		want := swap(t, emitted, "  - rule: single\n    region: api\n", "  - rule: single\n    region: web\n")
		want = swap(t, want, "    region: api\n    regions: [api, db]\n", "    region: db\n")
		assert.Equal(t, want, string(c.After))
		assert.Equal(t, []reviews.PinKey{reviews.NewPinKey("single", "web", nil), reviews.NewPinKey("multi", "db", nil)}, c.NewKeys)
		assert.Equal(t, 2, c.Added)
		assert.Equal(t, 3, c.Removed)
		assert.Contains(t, c.Diff, "-    regions: [api, db]\n")
		assert.Contains(t, c.Diff, "+    region: web\n")
	})

	t.Run("an insertion and a deletion start on the same line", func(t *testing.T) {
		// The regions line of a goes after line 4. The provenance line of b
		// is line 5. Both splices start on line 5, and the deletion must
		// run first, or it removes the inserted line instead.
		const file = "pin:\n  - rule: a\n    note: n\n    region: api\n" +
			"  # distilled by c from 1 findings (seamark lessons --distill, p2)\n  - rule: b\n    region: db\n"

		d := Parse([]byte(file))

		c, err := Plan(d, []Edit{
			{Ref: entryOf(t, d, "a").Ref(), Regions: []string{"api", "web"}},
			{Ref: entryOf(t, d, "b").Ref(), Delete: true, Provenance: []int64{2}},
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "pin:\n  - rule: a\n    note: n\n    region: api\n    regions: [api, web]\n", string(c.After))

		a := analyze([]byte(file))
		ps := []*planned{a.bind(c.Edits[0], false), a.bind(c.Edits[1], false)}
		assert.Equal(t, strings.Split(strings.TrimSuffix(string(c.After), "\n"), "\n"), a.spliced(ps), "the splices give the candidate")
		assert.Equal(t, a.spliced(ps), a.spliced([]*planned{ps[1], ps[0]}), "the order of the edits does not matter")
	})

	t.Run("a prune and a retarget below it", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: single.Ref(), Delete: true, Provenance: []int64{1}}, {Ref: repo.Ref(), Regions: []string{"lib"}}}, nil)
		require.NoError(t, err)

		want := swap(t, emitted, "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p1)\n  - rule: single\n    region: api\n    note: Short note.\n", "")
		want = swap(t, want, "    region: '*'\n", "    region: lib\n")
		assert.Equal(t, want, string(c.After))
	})
}

func TestPlanComputesTheChangeOfAWriteBlockedFile(t *testing.T) {
	// A link or a read-only mode blocks the write, not the review: the
	// user sees the exact change and the reason (D-10).
	d := Parse([]byte(emitted))
	d.FileBlock = "symlink at .seamark; seamark writes only real paths inside the repository"

	c, err := Plan(d, []Edit{{Ref: entryOf(t, d, "single").Ref(), Regions: []string{"web"}}}, nil)

	require.NoError(t, err)
	assert.Contains(t, string(c.After), "    region: web\n")
}
