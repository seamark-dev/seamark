package pinfile

// Tests of the rewrite: the authoring edit of the rule, the note, and
// the region set of one entry, in place. The layouts are the layouts of
// TestRetargetGoldens and TestPruneGoldens.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// text returns a pointer to s, for a field of Rewrite.
func text(s string) *string {
	return &s
}

// regionSet returns a pointer to the set, for Rewrite.Regions.
func regionSet(regions ...string) *[]string {
	if regions == nil {
		regions = []string{}
	}

	return &regions
}

// rewriteOne plans the rewrite rw of the entry with the rule.
func rewriteOne(t *testing.T, before, rule string, rw Rewrite) (string, error) {
	t.Helper()

	return planOne(t, before, rule, Edit{Rewrite: &rw})
}

func TestRewriteGoldens(t *testing.T) {
	cases := []struct {
		name, before, rule string
		rw                 Rewrite
		after              func(t *testing.T, s string) string
	}{
		// The rule.
		{"rule rename", emitted, "single", Rewrite{Rule: text("alone")}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n", "  - rule: alone\n")
		}},
		{"rule case only keeps the key but is an edit", emitted, "single", Rewrite{Rule: text("Single")}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n", "  - rule: Single\n")
		}},
		{"rule keeps an aligned comment", docsLayout, "RUF001", Rewrite{Rule: text("RUF002")}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: RUF001                 #", "  - rule: RUF002                 #")
		}},
		{"rule of a dash-alone entry", dashAlone, "lonely", Rewrite{Rule: text("alone")}, func(t *testing.T, s string) string {
			return swap(t, s, "    rule: lonely\n", "    rule: alone\n")
		}},
		{"rule that needs quotes", emitted, "single", Rewrite{Rule: text("a: b")}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: single\n", "  - rule: 'a: b'\n")
		}},
		// The note: inline, block, and quoted inputs.
		{"inline note to inline", emitted, "single", Rewrite{Note: text("Longer: with a colon")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: Short note.\n", "    note: 'Longer: with a colon'\n")
		}},
		{"inline note to a block", emitted, "single", Rewrite{Note: text("first\n# a hash line\n\n  indented")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: Short note.\n", "    note: |-\n      first\n      # a hash line\n\n        indented\n")
		}},
		{"quoted note to plain", emitted, "multi", Rewrite{Note: text("m")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: \"n\"\n", "    note: m\n")
		}},
		{"block note to inline", emitted, "repo", Rewrite{Note: text("one line")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: |-\n      first line\n      # not a comment\n\n      last line\n", "    note: one line\n")
		}},
		{"block note to a block", emitted, "repo", Rewrite{Note: text("a\nb")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: |-\n      first line\n      # not a comment\n\n      last line\n", "    note: |-\n      a\n      b\n")
		}},
		{"multi-line double-quoted note", readmeLayout, "keep-parallel-implementations-consistent", Rewrite{Note: text("short")},
			func(t *testing.T, s string) string {
				return swap(t, s, "  note: \"When two runtimes share a contract, mirror validation\n    strictness and regenerate derived artifacts.\"\n", "  note: short\n")
			}},
		{"multi-line single-quoted note", "pin:\n  - rule: q\n    note: 'one\n      two'\n  - rule: r\n", "q", Rewrite{Note: text("x\ny")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: q\n    note: |-\n      x\n      y\n  - rule: r\n"
			}},
		{"folded note", "pin:\n  - rule: f\n    note: >-\n      folded\n      text\n", "f", Rewrite{Note: text("plain")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: f\n    note: plain\n"
			}},
		{"plain note on the next line", "pin:\n  - rule: a\n    note:\n      plain text\n      more\n", "a", Rewrite{Note: text("x")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: x\n"
			}},
		{"inline note keeps its comment", "pin:\n  - rule: a\n    note: old   # why\n", "a", Rewrite{Note: text("new")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: new   # why\n"
			}},
		{"inline note to a block keeps its comment", "pin:\n  - rule: a\n    note: old   # why\n", "a", Rewrite{Note: text("l1\nl2")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: |-   # why\n      l1\n      l2\n"
			}},
		{"block header keeps its comment", "pin:\n  - rule: a\n    note: |-  # why\n      l1\n      l2\n", "a", Rewrite{Note: text("x")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: x  # why\n"
			}},
		{"a comment on the line after the colon stays", "pin:\n  - rule: a\n    note:   # why\n      plain text\n", "a", Rewrite{Note: text("x")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: x   # why\n"
			}},
		// A quoted note that goes on below its key line has no comment on
		// that line. A " #" inside it is note text, also after an escaped
		// quote.
		{"a multi-line single-quoted note carries no comment", "pin:\n  - rule: a\n    note: 'Don''t # really\n      ever'\n", "a",
			Rewrite{Note: text("Keep it.")}, func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: Keep it.\n"
			}},
		{"a multi-line single-quoted note to a block", "pin:\n  - rule: a\n    note: 'a'' #b'' c\n      d'\n", "a", Rewrite{Note: text("x\ny")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: |-\n      x\n      y\n"
			}},
		{"an inline single-quoted note keeps its comment", "pin:\n  - rule: a\n    note: 'it''s # x'  # real\n", "a", Rewrite{Note: text("new")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    note: new  # real\n"
			}},
		{"note of a deep layout", deepLayout, "a", Rewrite{Note: text("x\ny")}, func(t *testing.T, s string) string {
			return swap(t, s, "      note: n\n", "      note: |-\n        x\n        y\n")
		}},
		{"note of a zero indent layout", readmeLayout, "second", Rewrite{Note: text("two\nlines")}, func(t *testing.T, s string) string {
			return swap(t, s, "  note: x\n", "  note: |-\n    two\n    lines\n")
		}},
		{"note with unicode", emitted, "single", Rewrite{Note: text("ünï 日本\n«b»")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: Short note.\n", "    note: |-\n      ünï 日本\n      «b»\n")
		}},
		{"a tab at a line start takes double quotes", emitted, "single", Rewrite{Note: text("a\n\tb")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: Short note.\n", "    note: \"a\\n\\tb\"\n")
		}},
		{"leading spaces take double quotes", emitted, "single", Rewrite{Note: text("  lead\nx")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: Short note.\n", "    note: \"  lead\\nx\"\n")
		}},
		{"no final line break", "pin:\n  - rule: last\n    note: a", "last", Rewrite{Note: text("b\nc")}, func(t *testing.T, s string) string {
			return "pin:\n  - rule: last\n    note: |-\n      b\n      c"
		}},
		// The dash line.
		{"note on the dash line", deepLayout, "b", Rewrite{Note: text("changed\ntwice")}, func(t *testing.T, s string) string {
			return swap(t, s, "    - note: first key note\n", "    - note: |-\n        changed\n        twice\n")
		}},
		{"an empty note on the dash line keeps the key", deepLayout, "b", Rewrite{Note: text("")}, func(t *testing.T, s string) string {
			return swap(t, s, "    - note: first key note\n", "    - note: \"\"\n")
		}},
		{"rule on the dash line", emitted, "repo", Rewrite{Rule: text("everywhere")}, func(t *testing.T, s string) string {
			return swap(t, s, "  - rule: repo\n", "  - rule: everywhere\n")
		}},
		// Removal and insertion of the note key.
		{"an empty note removes the key", emitted, "single", Rewrite{Note: text("")}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    note: Short note.\n", "    region: api\n")
		}},
		{"an empty note removes a block note", emitted, "repo", Rewrite{Note: text("")}, func(t *testing.T, s string) string {
			return swap(t, s, "    note: |-\n      first line\n      # not a comment\n\n      last line\n", "")
		}},
		{"an empty note removes a multi-line single-quoted note", "pin:\n  - rule: a\n    note: 'Don''t # really\n      ever'\n    region: api\n", "a",
			Rewrite{Note: text("")}, func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n    region: api\n"
			}},
		{"an empty note removes an inline single-quoted note", "pin:\n  - rule: a\n    note: 'it''s # x'\n", "a", Rewrite{Note: text("")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: a\n"
			}},
		{"an empty note on the dash line replaces a multi-line quoted note", "pin:\n  - note: 'Don''t # really\n      ever'\n    rule: a\n", "a",
			Rewrite{Note: text("")}, func(t *testing.T, s string) string {
				return "pin:\n  - note: \"\"\n    rule: a\n"
			}},
		{"an absent note goes after the rule", deepLayout, "c", Rewrite{Note: text("new note")}, func(t *testing.T, s string) string {
			return swap(t, s, "    - rule: c\n", "    - rule: c\n      note: new note\n")
		}},
		{"an absent block note goes after the rule", "pin:\n  - rule: bare\n    region: api\n", "bare", Rewrite{Note: text("a\nb")},
			func(t *testing.T, s string) string {
				return "pin:\n  - rule: bare\n    note: |-\n      a\n      b\n    region: api\n"
			}},
		// The regions, through the retarget splices.
		{"regions as a retarget", emitted, "multi", Rewrite{Regions: regionSet("db", "web")}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    regions: [api, db]\n", "    region: db\n    regions: [db, web]\n")
		}},
		{"a new order of the set is an edit", emitted, "multi", Rewrite{Regions: regionSet("db", "api")}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    regions: [api, db]\n", "    region: db\n    regions: [db, api]\n")
		}},
		{"repository-wide", emitted, "single", Rewrite{Regions: regionSet()}, func(t *testing.T, s string) string {
			return swap(t, s, "    region: api\n    note: Short", "    region: '*'\n    note: Short")
		}},
		// The dash-line layouts of TestRetargetGoldens: the region splices
		// and the rule and note splices meet near the dash line.
		{"regions first keep a new set", regionsFirst, "X", Rewrite{Rule: text("renamed"), Note: text("a\nb"), Regions: regionSet("web")},
			func(t *testing.T, s string) string {
				return "pin:\n  - regions: [web]\n    rule: renamed\n    note: |-\n      a\n      b\n"
			}},
		{"regions first become repository-wide", regionsFirst, "X", Rewrite{Rule: text("renamed"), Note: text(""), Regions: regionSet()},
			func(t *testing.T, s string) string {
				return "pin:\n  - region: '*'\n    rule: renamed\n"
			}},
		{"a lifted region key and an absent note", "pin:\n  - regions: [api, db]\n    region: api   # mine\n    rule: X\n", "X",
			Rewrite{Rule: text("renamed"), Note: text("new"), Regions: regionSet("web")}, func(t *testing.T, s string) string {
				return "pin:\n  - region: web   # mine\n    rule: renamed\n    note: new\n"
			}},
		{"a lifted region key over another key and a removed note", "pin:\n  - regions: [api, db]\n    rule: X\n    region: api\n    note: n\n", "X",
			Rewrite{Rule: text("renamed"), Note: text(""), Regions: regionSet()}, func(t *testing.T, s string) string {
				return "pin:\n  - region: '*'\n    rule: renamed\n"
			}},
		// Several fields at once.
		{"rule, note, and regions", emitted, "single", Rewrite{Rule: text("alone"), Note: text("Two\nlines"), Regions: regionSet("api", "web")},
			func(t *testing.T, s string) string {
				return swap(t, s, "  - rule: single\n    region: api\n    note: Short note.\n",
					"  - rule: alone\n    region: api\n    regions: [api, web]\n    note: |-\n      Two\n      lines\n")
			}},
		{"new region keys and a new note keep the rendered order", "pin:\n  - rule: bare\n", "bare",
			Rewrite{Note: text("n"), Regions: regionSet("api", "db")}, func(t *testing.T, s string) string {
				return "pin:\n  - rule: bare\n    region: api\n    regions: [api, db]\n    note: \"n\"\n"
			}},
		{"unknown keys and comments stay", deepLayout, "b", Rewrite{Rule: text("bee"), Note: text("x")}, func(t *testing.T, s string) string {
			s = swap(t, s, "    - note: first key note\n      rule: b\n", "    - note: x\n      rule: bee\n")

			return s
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rewriteOne(t, tc.before, tc.rule, tc.rw)

			require.NoError(t, err)
			assert.Equal(t, tc.after(t, tc.before), got)

			// The candidate reads back with the rewritten fields, and every
			// other field of the entry stays.
			before := entryOf(t, Parse([]byte(tc.before)), tc.rule)

			rule := before.Rule
			if tc.rw.Rule != nil {
				rule = *tc.rw.Rule
			}

			after := entryOf(t, Parse([]byte(got)), rule)
			assert.Equal(t, before.Index, after.Index, "the entry keeps its position")
			assert.Equal(t, before.Unknown, after.Unknown)
			assert.Empty(t, after.Block)
			assert.Empty(t, Parse([]byte(got)).FileBlock, "the candidate stays editable")

			if tc.rw.Note != nil {
				assert.Equal(t, *tc.rw.Note, after.Note)
			} else {
				assert.Equal(t, before.Note, after.Note)
			}

			if tc.rw.Regions != nil {
				assert.Equal(t, reviews.NewPinKey(rule, "", *tc.rw.Regions), after.Key)
			} else {
				assert.Equal(t, reviews.NewPinKey(rule, before.Region, before.Regions), after.Key)
			}
		})
	}
}

func TestRewriteRefusesWhatItCannotProve(t *testing.T) {
	cases := []struct {
		name, before, rule string
		edit               Edit
		want               error
		reason             string
	}{
		{"no field", emitted, "single", Edit{Rewrite: &Rewrite{}}, ErrNoOp, "already current"},
		{"the same values", emitted, "multi", Edit{Rewrite: &Rewrite{Rule: text("multi"), Note: text("n"), Regions: regionSet("api", "db")}},
			ErrNoOp, "already current"},
		{"an empty note of an entry without a note", deepLayout, "c", Edit{Rewrite: &Rewrite{Note: text("")}}, ErrNoOp, "already current"},
		{"a removal with a rewrite", emitted, "single", Edit{Delete: true, Rewrite: &Rewrite{Rule: text("x")}}, ErrUnsupported, "removal"},
		{"a comment line under an inline note", "pin:\n  - rule: a\n    note: x\n      # why\n", "a", Edit{Rewrite: &Rewrite{Note: text("y")}},
			ErrUnsupported, "drop a comment"},
		{"a comment line inside a plain note", "pin:\n  - rule: a\n    note:\n      # why\n      text\n", "a", Edit{Rewrite: &Rewrite{Note: text("y")}},
			ErrUnsupported, "drop a comment"},
		{"a removal that drops the comment of the key line", "pin:\n  - rule: a\n    note: x  # why\n", "a", Edit{Rewrite: &Rewrite{Note: text("")}},
			ErrUnsupported, "comment of its key line"},
		{"a removal that drops the comment after a single-quoted note", "pin:\n  - rule: a\n    note: 'it''s # x'  # real\n", "a",
			Edit{Rewrite: &Rewrite{Note: text("")}}, ErrUnsupported, "comment of its key line"},
		// yaml.v3 reads a quoted note that goes on at or left of its key
		// column. The note lines are then not the lines below the key.
		{"a quoted note that goes on at the key column", "pin:\n  - rule: a\n    note: \"first\n    second\"\n", "a",
			Edit{Rewrite: &Rewrite{Note: text("x")}}, ErrUnsupported, "the note goes on at a line that is not deeper than its key"},
		{"a quoted note that goes on at column 0", "pin:\n  - rule: a\n    note: 'first\nsecond'\n", "a",
			Edit{Rewrite: &Rewrite{Note: text("x")}}, ErrUnsupported, "the note goes on at a line that is not deeper than its key"},
		{"a removal of a quoted note that goes on at the key column", "pin:\n  - rule: a\n    note: \"first\n    second\"\n    region: api\n", "a",
			Edit{Rewrite: &Rewrite{Note: text("")}}, ErrUnsupported, "the note goes on at a line that is not deeper than its key"},
		{"a note without a rule key", "pin:\n  - region: api\n", "", Edit{Rewrite: &Rewrite{Note: text("x")}},
			ErrUnsupported, "no rule key"},
		{"a rule without a rule key", "pin:\n  - region: api\n    note: n\n", "", Edit{Rewrite: &Rewrite{Rule: text("x")}},
			ErrUnsupported, "no rule key"},
		{"an empty region", emitted, "single", Edit{Rewrite: &Rewrite{Regions: regionSet("")}}, ErrUnsupported, "empty region"},
		{"a rule without safe YAML text", emitted, "single", Edit{Rewrite: &Rewrite{Rule: text("bad\xff")}},
			ErrUnsupported, "the entry cannot be edited safely: the rule has no safe single-line YAML text"},
		{"a region without safe YAML text", emitted, "single", Edit{Rewrite: &Rewrite{Regions: regionSet("bad\xff")}},
			ErrUnsupported, "the entry cannot be edited safely: a region has no safe single-line YAML text"},
		{"a rename onto another live key", emitted, "single", Edit{Rewrite: &Rewrite{Rule: text("MULTI"), Regions: regionSet("db", "api")}},
			ErrCollision, "another pin already carries the key multi @ api, db"},
		{"a flow entry", "pin:\n  - {rule: f, note: n}\n", "f", Edit{Rewrite: &Rewrite{Note: text("m")}}, ErrUnsupported, "flow style"},
		{"a CRLF file", crlf("pin:\n  - rule: a\n    note: n\n"), "a", Edit{Rewrite: &Rewrite{Note: text("m")}}, ErrUnsupported, "CRLF"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planOne(t, tc.before, tc.rule, tc.edit)

			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.reason)
			require.Len(t, EditErrors(err), 1)
		})
	}
}

func TestRewriteCollidesOnlyWithOtherOwners(t *testing.T) {
	d := Parse([]byte(emitted))
	single := entryOf(t, d, "single")
	own := reviews.NewPinKey("single", "api", nil)

	t.Run("a note edit keeps the key of the entry", func(t *testing.T) {
		c, err := Plan(d, []Edit{{Ref: single.Ref(), Rewrite: &Rewrite{Note: text("Edited.")}}}, nil)
		require.NoError(t, err)
		assert.Equal(t, []reviews.PinKey{own}, c.NewKeys)
	})

	t.Run("its own applied row is excluded by the caller", func(t *testing.T) {
		// AuthorKeys leaves out the rows linked to the edited entry. A
		// caller that passes them anyway makes the note edit collide with
		// its own row (trap T6).
		_, err := Plan(d, []Edit{{Ref: single.Ref(), Rewrite: &Rewrite{Note: text("Edited.")}}}, []reviews.PinKey{own})
		require.ErrorIs(t, err, ErrCollision)
	})

	t.Run("a rename onto an applied key", func(t *testing.T) {
		_, err := Plan(d, []Edit{{Ref: single.Ref(), Rewrite: &Rewrite{Rule: text("taken")}}},
			[]reviews.PinKey{reviews.NewPinKey("taken", "api", nil)})
		require.ErrorIs(t, err, ErrCollision)
		assert.Contains(t, err.Error(), "an applied proposal already records the key taken @ api")
	})
}

func TestRewriteVerifiesItsCandidate(t *testing.T) {
	a, p := boundEdit(t, "single", Edit{Rewrite: &Rewrite{Rule: text("alone"), Note: text("New\nnote")}})
	good := string(a.candidate([]*planned{p}))
	require.NoError(t, a.verify([]byte(good), []*planned{p}))

	cases := []struct {
		name, from, to, reason string
	}{
		{"another entry changes", "    note: \"n\"", "    note: \"m\"", "differs from the intended change"},
		{"the rule is not renamed", "rule: alone", "rule: single", "differs from the intended change"},
		{"the note reads as other text", "      note\n", "      Note\n", "differs from the intended change"},
		{"a setting changes", "threshold: 3", "threshold: 4", "a setting outside the pin list changes"},
		{"a comment changes", "# Review-lesson tuning", "# Review lesson tuning", "differ from the reviewed lines"},
		{"the region line moves below the note", "    region: api\n    note: |-\n      New\n      note\n",
			"    note: |-\n      New\n      note\n    region: api\n", "differ from the reviewed lines"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forged := strings.Replace(good, tc.from, tc.to, 1)
			require.NotEqual(t, good, forged, "the forgery must change the candidate")

			err := a.verify([]byte(forged), []*planned{p})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}

	t.Run("a rewritten entry that does not read back", func(t *testing.T) {
		a, p := boundEdit(t, "single", Edit{Rewrite: &Rewrite{Regions: regionSet("api", "web")}})
		good := string(a.candidate([]*planned{p}))
		forged := strings.Replace(good, "    regions: [api, web]\n", "    regions: [api,\n      web]\n", 1)
		require.NotEqual(t, good, forged)

		require.EqualError(t, a.verify([]byte(forged), []*planned{p}), "a rewritten entry does not read back as its target")
	})
}

func TestRewriteVerifierRefusesASpanDefect(t *testing.T) {
	// Check 7 derives the note lines from the nodes, not from the key
	// extent that built the splice. A defect there, simulated by a forged
	// splice, must block the rewrite although every value stays right.
	bind := func(t *testing.T, file, rule string, rw Rewrite) (*analysis, *planned) {
		t.Helper()

		a := analyze([]byte(file))
		p := a.bind(Edit{Ref: entryOf(t, Parse([]byte(file)), rule).Ref(), Rewrite: &rw}, false)
		require.NoError(t, p.err)
		require.NoError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "the correct splices pass")

		return a, p
	}

	t.Run("the note splice takes the next provenance line", func(t *testing.T) {
		a, p := bind(t, emitted, "single", Rewrite{Note: text("x")})
		p.splices = []splice{{from: 9, to: 10, lines: []string{"    note: x", a.lines[9]}}}

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 10 is outside the reviewed entry")
	})

	t.Run("the note splice takes the next top-level key", func(t *testing.T) {
		a, p := bind(t, emitted, "repo", Rewrite{Note: text("x")})
		p.splices = []splice{{from: 18, to: 23, lines: []string{"    note: x", a.lines[22]}}}

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 23 is outside the reviewed entry")
	})

	t.Run("the note splice takes the head comment of the next key", func(t *testing.T) {
		const file = "pin:\n  - rule: a\n    note: n\n    # about the region\n    region: api\n"

		a, p := bind(t, file, "a", Rewrite{Note: text("x")})
		p.splices = []splice{{from: 3, to: 4, lines: []string{"    note: x"}}}

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 4 is outside the reviewed entry")
	})

	t.Run("the note splice drops a deeper comment under an inline note", func(t *testing.T) {
		// The planner refuses this file. A span defect that let the splice
		// through must still meet check 7.
		const file = "pin:\n  - rule: a\n    note: x\n      # why\n  - rule: b\n"

		a := analyze([]byte(file))
		p := a.bind(Edit{Ref: entryOf(t, Parse([]byte(file)), "a").Ref(), Rewrite: &Rewrite{Note: text("y")}}, false)
		require.ErrorIs(t, p.err, ErrUnsupported)

		p.err, p.splices = nil, []splice{{from: 3, to: 4, lines: []string{"    note: y"}}}

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}),
			"the note at line 3 holds a comment, which the edit would drop")
	})

	t.Run("an absent note goes in after another key", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n    region: api\n", "a", Rewrite{Note: text("n")})
		p.splices[0].from, p.splices[0].to = 4, 3

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}),
			"an inserted line at line 4 is outside the reviewed entry")
	})

	t.Run("the rule splice touches the region line", func(t *testing.T) {
		a, p := bind(t, emitted, "single", Rewrite{Rule: text("alone")})
		p.splices = append(p.splices, splice{from: 8, to: 8, lines: []string{a.lines[7]}})

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 8 is outside the reviewed entry")
	})

	t.Run("a note-only rewrite touches the rule line", func(t *testing.T) {
		a, p := bind(t, emitted, "single", Rewrite{Note: text("x")})
		p.splices = append(p.splices, splice{from: 7, to: 7, lines: []string{a.lines[6]}})

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 7 is outside the reviewed entry")
	})
}

func TestRewriteIgnoresTheRetargetRegions(t *testing.T) {
	// With Rewrite set, Edit.Regions is not a request. The region keys
	// stay byte for byte, and only the note changes.
	got, err := planOne(t, emitted, "single", Edit{Regions: []string{"zzz"}, Rewrite: &Rewrite{Note: text("Edited.")}})
	require.NoError(t, err)
	assert.Equal(t, swap(t, emitted, "    note: Short note.\n", "    note: Edited.\n"), got)
}

func TestRewriteKeepsTheRenderedOrderInALargePlan(t *testing.T) {
	// Each rewrite inserts its note and its region keys after its rule
	// key, at one line. Only a stable sort keeps the region keys above the
	// note. Go sorts up to 12 splices by insertion sort, which is stable
	// anyway, so this plan holds 14 splices.
	var before, want strings.Builder

	before.WriteString("pin:\n")
	want.WriteString("pin:\n")

	for i := range 7 {
		fmt.Fprintf(&before, "  - rule: bare%d\n", i)
		fmt.Fprintf(&want, "  - rule: bare%d\n    region: api\n    regions: [api, db]\n    note: Note %d.\n", i, i)
	}

	d := Parse([]byte(before.String()))

	var edits []Edit

	for i, e := range d.Entries {
		edits = append(edits, Edit{Ref: e.Ref(), Rewrite: &Rewrite{Note: text(fmt.Sprintf("Note %d.", i)), Regions: regionSet("api", "db")}})
	}

	c, err := Plan(d, edits, nil)
	require.NoError(t, err)
	assert.Equal(t, want.String(), string(c.After))
}

func TestRemovalOfAnUnlinkedEntryTakesItsProvenanceRun(t *testing.T) {
	// An unlinked entry can still carry the provenance comment of a row
	// that was dismissed later. The removal passes Entry.ProvenanceIDs,
	// so the comment goes with the entry. Without them, the comment would
	// stay and read as the provenance of the next entry.
	d := Parse([]byte(emitted))
	e := entryOf(t, d, "single")
	require.Equal(t, []int64{1}, e.ProvenanceIDs)

	c, err := Plan(d, []Edit{{Ref: e.Ref(), Delete: true, Provenance: e.ProvenanceIDs}}, nil)
	require.NoError(t, err)

	after := Parse(c.After)
	assert.Equal(t, []int64{2}, entryOf(t, after, "multi").ProvenanceIDs)

	c, err = Plan(d, []Edit{{Ref: e.Ref(), Delete: true}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, entryOf(t, Parse(c.After), "multi").ProvenanceIDs, "the run that stays reads as the next entry's")
}

// TestRewriteReadsAnEscapedQuoteBeforeAHash covers a single-quoted value
// that holds a quote and then " #". YAML writes that quote as two quote
// characters. The comment scan once read the "#" as a comment, so the
// editor refused to remove the line, because a removal keeps every
// comment.
func TestRewriteReadsAnEscapedQuoteBeforeAHash(t *testing.T) {
	body := "pin:\n  - rule: hand\n    regions:\n      - api\n      - 'docs/it''s #1'\n"
	cases := []struct {
		name    string
		regions []string
		want    string
	}{
		{"drop the quoted item", []string{"api"}, "pin:\n  - rule: hand\n    regions:\n      - api\n"},
		{"drop every region", []string{}, "pin:\n  - rule: hand\n    region: '*'\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Parse([]byte(body))

			ch, err := Plan(d, []Edit{{Ref: d.Entries[0].Ref(), Rewrite: &Rewrite{Regions: &c.regions}}}, nil)
			require.NoError(t, err)
			assert.Equal(t, c.want, string(ch.After))
		})
	}
}

// noteCommentShapes hold a comment after the text of a note line below
// the key line (finding M1). Each note decodes to text without the
// comment, so a rewrite of the note lines would drop the comment.
var noteCommentShapes = []struct{ name, before string }{
	{"a plain note that goes on", "pin:\n  - rule: a\n    note: x\n      more # keep\n    region: api\n"},
	{"a plain note line that starts with a quote", "pin:\n  - rule: a\n    note: x\n      'n roll # keep\n    region: api\n"},
	{"a single-quoted note", "pin:\n  - rule: a\n    note: 'x\n      more' # keep\n    region: api\n"},
	{"a double-quoted note", "pin:\n  - rule: a\n    note: \"x\n      more\" # keep\n    region: api\n"},
	{"a plain note below its key", "pin:\n  - rule: a\n    note:\n      text # keep\n    region: api\n"},
	{"a quoted note below its key", "pin:\n  - rule: a\n    note:\n      'it''s # x' # keep\n    region: api\n"},
}

// TestRewriteRefusesToDropACommentAfterTheNoteText covers finding M1.
// The planner found only whole comment lines below the note key, so a
// change or a removal of the note dropped the comment after the text.
func TestRewriteRefusesToDropACommentAfterTheNoteText(t *testing.T) {
	for _, s := range noteCommentShapes {
		for _, note := range []string{"y", ""} {
			t.Run(fmt.Sprintf("%s, note %q", s.name, note), func(t *testing.T) {
				_, err := rewriteOne(t, s.before, "a", Rewrite{Note: text(note)})

				require.ErrorIs(t, err, ErrUnsupported)
				assert.Contains(t, err.Error(), "changing the note would drop a comment")
			})
		}
	}
}

// TestRewriteVerifierRefusesADroppedNoteComment proves that check 7
// finds the comments of finding M1 without the planner. A planner
// without its comment check splices the note lines as below.
func TestRewriteVerifierRefusesADroppedNoteComment(t *testing.T) {
	for _, s := range noteCommentShapes {
		for _, note := range []string{"y", ""} {
			t.Run(fmt.Sprintf("%s, note %q", s.name, note), func(t *testing.T) {
				a := analyze([]byte(s.before))
				p := a.bind(Edit{Ref: entryOf(t, Parse([]byte(s.before)), "a").Ref(), Rewrite: &Rewrite{Note: text(note)}}, false)
				require.ErrorIs(t, p.err, ErrUnsupported)

				var lines []string
				if note != "" {
					lines = []string{`    note: "` + note + `"`}
				}

				p.err, p.splices = nil, []splice{{from: 3, to: 4, lines: lines}}

				require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}),
					"the note at line 3 holds a comment, which the edit would drop")
			})
		}
	}
}

// TestRewriteReadsAHashInsideTheNoteText holds the other side of M1. A
// "#" inside quotes, inside a block note, or without white space before
// it is note text, so the note changes and goes without a refusal.
func TestRewriteReadsAHashInsideTheNoteText(t *testing.T) {
	cases := []struct{ name, before string }{
		{"a hash inside a double-quoted note that goes on", "pin:\n  - rule: a\n    note: \"x\n      more # in\"\n    region: api\n"},
		{"a hash inside a single-quoted note that goes on", "pin:\n  - rule: a\n    note: 'x\n      more # in'\n    region: api\n"},
		{"a hash without white space before it", "pin:\n  - rule: a\n    note: x\n      more#text\n    region: api\n"},
		{"a hash in a line of a block note", "pin:\n  - rule: a\n    note: |-\n      text # in\n    region: api\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := rewriteOne(t, c.before, "a", Rewrite{Note: text("y")})
			require.NoError(t, err)
			assert.Equal(t, "pin:\n  - rule: a\n    note: \"y\"\n    region: api\n", got)

			got, err = rewriteOne(t, c.before, "a", Rewrite{Note: text("")})
			require.NoError(t, err)
			assert.Equal(t, "pin:\n  - rule: a\n    region: api\n", got)
		})
	}
}

// TestRewriteKeepsTheCommentOfAPlainRegionItem covers finding L1. A
// plain region item can hold a quote after white space. The comment scan
// once started at column 0. It read that quote as an opening quote, so
// it missed the comment. The removal of the item then dropped it.
func TestRewriteKeepsTheCommentOfAPlainRegionItem(t *testing.T) {
	const body = "pin:\n  - rule: a\n    regions:\n      - api\n      - rock 'n roll"

	_, err := rewriteOne(t, body+" # keep me\n", "a", Rewrite{Regions: regionSet("api")})
	require.ErrorIs(t, err, ErrUnsupported)
	assert.Contains(t, err.Error(), "removing a regions item would drop a comment")

	got, err := rewriteOne(t, body+"\n", "a", Rewrite{Regions: regionSet("api")})
	require.NoError(t, err)
	assert.Equal(t, "pin:\n  - rule: a\n    regions:\n      - api\n", got, "an item without a comment goes")
}
