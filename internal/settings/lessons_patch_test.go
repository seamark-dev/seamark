package settings

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The planner, verifier, and save tests of the lessons document.

// richLessons holds comments, blank lines, an unknown top-level key, a
// mute list with comments and unknown keys, and a pin list.
const richLessons = `# Lesson tuning. Reviewed like code.
custom: [1, 2]     # unknown top-level key

threshold: 3       # three recurrences

mute:
  - rule: F541     # noisy
  - region: vendor/
    reason: generated   # unknown key
  # - rule: E501

pin:
  - rule: RUF001
    region: scripts
    note: |-
      Keep scripts ASCII.
      # not a comment
`

// add returns a mute add edit.
func add(rule, region string) Edit {
	return Edit{Field: Mutes, Mute: &MuteEdit{Op: MuteAdd, Rule: rule, Region: region}}
}

// change returns a mute change edit of the entry at index i of snap.
func change(t *testing.T, snap *Snapshot, i int, rule, region string) Edit {
	t.Helper()

	return Edit{Field: Mutes, Mute: &MuteEdit{Op: MuteChange, Ref: entry(t, snap, i).Ref(), Rule: rule, Region: region}}
}

// remove returns a mute removal edit of the entry at index i of snap.
func remove(t *testing.T, snap *Snapshot, i int) Edit {
	t.Helper()

	return Edit{Field: Mutes, Mute: &MuteEdit{Op: MuteRemove, Ref: entry(t, snap, i).Ref()}}
}

// saveSnap plans edits against snap and saves them.
func saveSnap(t *testing.T, snap *Snapshot, edits ...Edit) *Plan {
	t.Helper()

	p, err := PlanPatch(snap, integration.Builtin(), edits)
	require.NoError(t, err)

	_, err = Save(p)
	require.NoError(t, err)

	return p
}

func TestLessonsSaveCreatesAMissingFileWithTheNeededKeysOnly(t *testing.T) {
	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"one delivery key", []Edit{{Field: Threshold, Int: 5}}, "threshold: 5\n"},
		{"two delivery keys in one block", []Edit{{Field: Threshold, Int: 5}, {Field: HookDelivery, String: "once-per-context"}},
			"threshold: 5\nhook_delivery: once-per-context\n"},
		{"one mute", []Edit{add("F541", "api/")}, "mute:\n  - rule: F541\n    region: api/\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()

			p := planOf(t, root, c.edits...)
			assert.True(t, p.Preview.Created)

			res, err := Save(p)
			require.NoError(t, err)
			assert.Equal(t, Written, res.Outcome)
			assert.True(t, res.Written)
			assert.True(t, res.Created)
			assert.Equal(t, lessonsPath(inspect(t, root).Root), res.Path)

			assert.Equal(t, c.want, readLessons(t, root), "no header comment and no other key")
			assert.Equal(t, []string{".seamark/lessons.yaml"}, keys(tree(t, root)))
		})
	}

	t.Run("a reset on a missing file is a no-op", func(t *testing.T) {
		root := t.TempDir()

		p := planOf(t, root, Edit{Field: PinBudget, Reset: true})
		assert.True(t, p.NoOp)

		res, err := Save(p)
		require.NoError(t, err)
		assert.Equal(t, NotWritten, res.Outcome)

		_, err = os.Stat(filepath.Join(root, ".seamark"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestMuteAddOnTheStarterKeepsEveryOtherLine(t *testing.T) {
	starter := starterLessons(t)
	root := withLessons(t, starter)

	p := planOf(t, root, add("F541", ""))
	assert.Equal(t, 1, p.Preview.Added)
	assert.Zero(t, p.Preview.Removed)

	_, err := Save(p)
	require.NoError(t, err)

	examples := "  # - region: alembic/versions   # generated migrations — ignore lessons here\n"
	assert.Equal(t, replaceOnce(t, starter, examples, examples+"  - rule: F541\n"), readLessons(t, root),
		"the item goes after the commented examples, and every other line stays")

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []reviews.MuteRule{{Rule: "F541"}}, cfg.Mute)
}

func TestMuteAddAppendsAfterTheLastItem(t *testing.T) {
	root := withLessons(t, richLessons)

	saveLessons(t, root, add("E501", "web/"))

	assert.Equal(t, replaceOnce(t, richLessons,
		"    reason: generated   # unknown key\n",
		"    reason: generated   # unknown key\n  - rule: E501\n    region: web/\n"), readLessons(t, root),
		"the item follows the last item, before the comment at the list column")

	t.Run("at the dash column of the list", func(t *testing.T) {
		root := withLessons(t, "mute:\n- rule: a\npin: []\n")

		saveLessons(t, root, add("b", ""))
		assert.Equal(t, "mute:\n- rule: a\n- rule: b\npin: []\n", readLessons(t, root))
	})

	t.Run("an absent mute key goes at the end", func(t *testing.T) {
		root := withLessons(t, "threshold: 2\npin:\n  - rule: a\n    note: n\n")

		saveLessons(t, root, add("", "gen/"))
		assert.Equal(t, "threshold: 2\npin:\n  - rule: a\n    note: n\n\nmute:\n  - region: gen/\n", readLessons(t, root))
	})

	t.Run("a typed value that needs quotes", func(t *testing.T) {
		root := withLessons(t, "mute:\n")

		saveLessons(t, root, add("on", "#x"))
		assert.Equal(t, "mute:\n  - rule: \"on\"\n    region: '#x'\n", readLessons(t, root))

		cfg, err := reviews.LoadConfig(root)
		require.NoError(t, err)
		assert.Equal(t, []reviews.MuteRule{{Rule: "on", Region: "#x"}}, cfg.Mute)
	})

	t.Run("an explicit null refuses", func(t *testing.T) {
		root := withLessons(t, "mute: ~\n")

		_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{add("a", "")})
		require.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "explicit null")
	})
}

func TestFlowMuteListRefusesEveryOperation(t *testing.T) {
	const text = "mute: [{rule: a}, {region: b}]\n"

	root := withLessons(t, text)
	snap := inspect(t, root)

	for _, e := range []Edit{add("c", ""), change(t, snap, 0, "d", ""), remove(t, snap, 1)} {
		_, err := PlanPatch(snap, integration.Builtin(), []Edit{e})
		require.ErrorIs(t, err, ErrBlocked, "op %d", e.Mute.Op)
		assert.Contains(t, err.Error(), "flow style [ ]")
	}

	assert.Equal(t, text, readLessons(t, root))
}

func TestNullAndEmptyItemsCanBeRemoved(t *testing.T) {
	const text = "mute:\n  -\n  - rule: a\n  - {}\n  - region: b\n"

	t.Run("null item", func(t *testing.T) {
		root := withLessons(t, text)
		snap := inspect(t, root)

		saveSnap(t, snap, remove(t, snap, 0))
		assert.Equal(t, "mute:\n  - rule: a\n  - {}\n  - region: b\n", readLessons(t, root))
	})

	t.Run("empty flow item", func(t *testing.T) {
		root := withLessons(t, text)
		snap := inspect(t, root)

		p := saveSnap(t, snap, remove(t, snap, 2))
		assert.Equal(t, "mute:\n  -\n  - rule: a\n  - region: b\n", readLessons(t, root))
		assert.Contains(t, p.Notes, noteMuteRemove)
	})

	t.Run("a null item cannot change", func(t *testing.T) {
		snap := inspect(t, withLessons(t, text))

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{change(t, snap, 0, "z", "")})
		require.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "remove the entry and add a new one")
	})

	t.Run("an add after a null item", func(t *testing.T) {
		root := withLessons(t, "mute:\n  - rule: a\n  -\n")

		saveLessons(t, root, add("b", ""))
		assert.Equal(t, "mute:\n  - rule: a\n  -\n  - rule: b\n", readLessons(t, root))
	})

	t.Run("removing the last item leaves the key", func(t *testing.T) {
		root := withLessons(t, "mute:\n  - rule: a\npin: []\n")
		snap := inspect(t, root)

		saveSnap(t, snap, remove(t, snap, 0))
		assert.Equal(t, "mute:\npin: []\n", readLessons(t, root))
	})
}

func TestMuteChangeKeepsUnknownKeysAndComments(t *testing.T) {
	cases := []struct {
		name, before string
		rule, region string
		after        string
	}{
		{
			"values in place",
			"mute:\n  - rule: F541   # why\n    region: api   # where\n    extra: x   # keep\n",
			"E1", "web/",
			"mute:\n  - rule: E1   # why\n    region: web/   # where\n    extra: x   # keep\n",
		},
		{
			"a new side after the last key",
			"mute:\n  - rule: F541\n    extra: x\n      # deeper note\n",
			"F541", "api",
			"mute:\n  - rule: F541\n    extra: x\n    region: api\n      # deeper note\n",
		},
		{
			"an emptied side on its own line",
			"mute:\n  - rule: F541\n    region: api\n    extra: x\n",
			"F541", "",
			"mute:\n  - rule: F541\n    extra: x\n",
		},
		{
			"an emptied side on the dash line moves the dash",
			"mute:\n  - rule: F541\n    region: api\n",
			"", "api",
			"mute:\n  - region: api\n",
		},
		{
			"a comment under the emptied dash line keeps a bare dash",
			"mute:\n  - rule: F541\n    # about the region\n    region: api\n",
			"", "api",
			"mute:\n  -\n    # about the region\n    region: api\n",
		},
		{
			"a swap of the sides",
			"mute:\n  - rule: F541\n",
			"", "api",
			"mute:\n  - region: api\n",
		},
		{
			"a case change of the rule",
			"mute:\n  - rule: f541\n",
			"F541", "",
			"mute:\n  - rule: F541\n",
		},
		{
			"a dash alone on its line",
			"mute:\n  -\n    rule: a\n    region: b\n",
			"", "b",
			"mute:\n  -\n    region: b\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.before)
			snap := inspect(t, root)

			saveSnap(t, snap, change(t, snap, 0, c.rule, c.region))
			assert.Equal(t, c.after, readLessons(t, root))
		})
	}

	t.Run("an unchanged side keeps its text", func(t *testing.T) {
		root := withLessons(t, "mute:\n  - rule: a\n    region: 7\n")
		snap := inspect(t, root)

		saveSnap(t, snap, change(t, snap, 0, "b", "7"))
		assert.Equal(t, "mute:\n  - rule: b\n    region: 7\n", readLessons(t, root))
	})

	t.Run("the same values are a no-op", func(t *testing.T) {
		root := withLessons(t, "mute:\n  - rule: a\n")
		snap := inspect(t, root)

		p, err := PlanPatch(snap, integration.Builtin(), []Edit{change(t, snap, 0, "a", "")})
		require.NoError(t, err)
		assert.True(t, p.NoOp)
		assert.Equal(t, p.Bytes.Before, p.Bytes.After)
	})
}

// A comment between the dash line and the next key belongs to the entry.
// If the dash moved below the comment, the comment would join the entry
// above, and a removal of that entry would then refuse.
func TestMuteChangeKeepsACommentInsideItsEntry(t *testing.T) {
	const before = "mute:\n  - rule: x\n  - rule: F541\n    # about the region\n    region: api\n"

	root := withLessons(t, before)
	snap := inspect(t, root)

	saveSnap(t, snap, change(t, snap, 1, "", "api"))
	assert.Equal(t, "mute:\n  - rule: x\n  -\n    # about the region\n    region: api\n", readLessons(t, root))

	snap = inspect(t, root)
	require.Len(t, snap.Lessons.Mutes, 2)
	assert.Equal(t, "api", snap.Lessons.Mutes[1].Region)

	saveSnap(t, snap, remove(t, snap, 0))
	assert.Equal(t, "mute:\n  -\n    # about the region\n    region: api\n", readLessons(t, root),
		"the entry above holds no comment, so its removal works")
}

func TestMuteChangeRefusals(t *testing.T) {
	cases := []struct {
		name, text   string
		index        int
		rule, region string
		err          error
		reason       string
	}{
		{"flow item", "mute:\n  - {rule: a}\n", 0, "b", "", ErrBlocked, "uses flow style { }; remove the entry and add a new one"},
		{"flow item without comment", "mute:\n  - {rule: a, region: b}\n", 0, "a", "c", ErrBlocked, "remove the entry and add a new one"},
		{"comment in the removed lines", "mute:\n  - rule: a\n    region: b   # why\n", 0, "a", "", ErrBlocked, "a comment sits in the lines that the change removes"},
		{"multi-line value", "mute:\n  - rule: |-\n      a\n    region: b\n", 0, "c", "b", ErrBlocked, "spans several lines"},
		{"duplicate of another entry", "mute:\n  - rule: a\n  - rule: B\n", 0, "b", "", ErrInvalidEdit, "the same mute is already at line 3"},
		{"both sides empty", "mute:\n  - rule: a\n", 0, "", "", ErrInvalidEdit, "a mute needs a rule, a region, or both"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.text)
			snap := inspect(t, root)

			_, err := PlanPatch(snap, integration.Builtin(), []Edit{change(t, snap, c.index, c.rule, c.region)})
			require.ErrorIs(t, err, c.err)
			assert.Contains(t, err.Error(), c.reason)
			assert.Equal(t, c.text, readLessons(t, root))
		})
	}

	t.Run("an entry that changed since the draft", func(t *testing.T) {
		root := withLessons(t, "mute:\n  - rule: a\n")
		old := inspect(t, root)
		e := change(t, old, 0, "b", "")

		writeLessons(t, root, "mute:\n  - rule: c\n")

		_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{e})
		require.ErrorIs(t, err, ErrInvalidEdit)
		assert.Contains(t, err.Error(), "the mute entry changed since the draft started")
	})
}

// Changeable is the rule of the planner for a mute change, so a form
// that asks it agrees with the review. An entry that matches nothing
// can still change when it is a block mapping. Only a flow item, a null
// item, and a stale reference refuse.
func TestChangeableIsTheRuleOfTheMuteChange(t *testing.T) {
	const text = "mute:\n  - rule: ''\n    why: legacy\n  -\n  - {rule: a}\n  - rule: b\n"

	root := withLessons(t, text)
	snap := inspect(t, root)
	lp := snap.Lessons

	cases := []struct {
		name   string
		index  int
		err    error
		reason string
	}{
		{"empty rule in a block mapping", 0, nil, ""},
		{"null item", 1, ErrBlocked, "the mute entry holds no keys; remove the entry and add a new one"},
		{"flow item", 2, ErrBlocked, "the mute entry uses flow style { }; remove the entry and add a new one"},
		{"plain entry", 3, nil, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref := entry(t, snap, c.index).Ref()
			err := lp.Changeable(ref)

			_, planErr := PlanPatch(snap, integration.Builtin(), []Edit{change(t, snap, c.index, "W1", "")})

			if c.err == nil {
				require.NoError(t, err)
				require.NoError(t, planErr, "the planner agrees with Changeable")

				return
			}

			require.ErrorIs(t, err, c.err)
			assert.Contains(t, err.Error(), c.reason)
			require.ErrorIs(t, planErr, c.err, "the planner agrees with Changeable")
			assert.Contains(t, planErr.Error(), c.reason)
		})
	}

	t.Run("the change keeps the unknown key", func(t *testing.T) {
		saveSnap(t, snap, change(t, snap, 0, "W1", ""))
		assert.Equal(t, "mute:\n  - rule: W1\n    why: legacy\n  -\n  - {rule: a}\n  - rule: b\n", readLessons(t, root))
	})

	t.Run("a stale reference", func(t *testing.T) {
		ref := entry(t, snap, 3).Ref()
		ref.RawSHA[0] ^= 1

		_, err := lp.Target(ref)
		require.ErrorIs(t, err, ErrInvalidEdit)
		assert.Contains(t, err.Error(), "the mute entry changed since the draft started")
		require.ErrorIs(t, lp.Changeable(ref), ErrInvalidEdit)

		_, err = lp.Target(MuteRef{Index: 9})
		require.ErrorIs(t, err, ErrInvalidEdit, "an index past the list")

		var none *LessonsPart

		_, err = none.Target(ref)
		require.ErrorIs(t, err, ErrInvalidEdit, "a missing reading holds no entry")
	})
}

func TestDuplicateAndOverlappingMutes(t *testing.T) {
	root := withLessons(t, "mute:\n  - rule: F541\n  - region: api/\n")

	_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{add("f541", "")})
	require.ErrorIs(t, err, ErrInvalidEdit)
	assert.Contains(t, err.Error(), "the same mute is already at line 2")

	p := planOf(t, root, add("", "api"))
	assert.Contains(t, p.Notes, "Another entry also matches some of the same lessons: line 3, region api/.",
		`a legacy "api/" against a new "api" is an overlap, not a duplicate`)
	assert.Contains(t, p.Notes, OverlapNote(entry(t, inspect(t, root), 1)), "a form shows the note of the review")
	assert.Contains(t, p.Notes, noteCommit)
	assert.NotContains(t, p.Notes, noteMuteRemove)

	// Neither mute holds the other, but both hide W1 lessons under api/.
	p = planOf(t, root, add("W1", "api"))
	assert.Contains(t, p.Notes, "Another entry also matches some of the same lessons: line 3, region api/.",
		"a combined mute next to a region mute that it crosses is an overlap")

	p = planOf(t, root, add("", "web"))
	assert.Equal(t, []string{noteCommit}, p.Notes, "a region-only mute next to a rule-only mute is no overlap note")
}

func TestMuteRemoval(t *testing.T) {
	const text = "# Mutes\nmute:\n  - rule: F541\n  - region: vendor/\n    reason: generated\n  # - rule: E501\n"

	root := withLessons(t, text)
	snap := inspect(t, root)

	p := saveSnap(t, snap, remove(t, snap, 1))

	assert.Equal(t, replaceOnce(t, text, "  - region: vendor/\n    reason: generated\n", ""), readLessons(t, root))
	assert.Contains(t, p.Notes, noteMuteRemove)

	t.Run("a comment line inside the item refuses", func(t *testing.T) {
		const text = "mute:\n  - rule: a\n    # why\n    region: b\n"

		root := withLessons(t, text)
		snap := inspect(t, root)

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{remove(t, snap, 0)})
		require.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "a comment sits inside the mute entry (line 2); edit .seamark/lessons.yaml by hand")
		assert.Equal(t, text, readLessons(t, root))
	})

	t.Run("a comment after the text of an item line refuses", func(t *testing.T) {
		root := withLessons(t, richLessons)
		snap := inspect(t, root)

		for i := range 2 {
			_, err := PlanPatch(snap, integration.Builtin(), []Edit{remove(t, snap, i)})
			require.ErrorIs(t, err, ErrBlocked)
			assert.Contains(t, err.Error(), "a comment sits inside the mute entry")
		}

		assert.Equal(t, richLessons, readLessons(t, root))
	})
}

// TestLessonsEditsKeepACommentAfterTheValueText covers the comment scan
// of the lessons.yaml splices. A reset, a mute removal, and the removed
// side of a mute change delete lines. A comment after the value text of
// such a line blocks the edit. Before the fix, the reset and the removal
// found only whole comment lines. The change scanned its key line from
// the key, so a quote in plain text hid the comment.
func TestLessonsEditsKeepACommentAfterTheValueText(t *testing.T) {
	const (
		inValue = "a comment sits inside the value of threshold"
		inEntry = "a comment sits inside the mute entry"
		inSide  = "a comment sits in the lines that the change removes; remove the entry and add a new one"
	)

	resetThreshold := func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} }
	removeFirst := func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) }
	dropRule := func(t *testing.T, snap *Snapshot) Edit { return change(t, snap, 0, "", "api") }

	cases := []struct {
		name, before string
		edit         func(t *testing.T, snap *Snapshot) Edit
		reason       string
		line         int
	}{
		{"a reset of a value on its own line", "threshold:\n  3   # keep\nmute:\n", resetThreshold, inValue, 1},
		{"a removal with a comment on the dash line", "mute:\n  - rule: a   # keep\n  - rule: b\n", removeFirst, inEntry, 2},
		{"a removal with a comment on a later line", "mute:\n  - rule: a\n    region: b   # keep\n", removeFirst, inEntry, 2},
		{"a removal with a quote before the comment", "mute:\n  - rule: rock 'n roll  # keep\n", removeFirst, inEntry, 2},
		{"a removal of a plain continuation", "mute:\n  - rule: a\n      b # keep\n", removeFirst, inEntry, 2},
		{"a removal of a quoted continuation", "mute:\n  - rule: 'a\n      b' # keep\n", removeFirst, inEntry, 2},
		{"a removal of a flow item", "mute:\n  - {rule: a}  # keep\n", removeFirst, inEntry, 2},
		{"a removal of a null item", "mute:\n  -   # keep\n  - rule: a\n", removeFirst, inEntry, 2},
		{"a change with a quote before the comment", "mute:\n  - rule: rock 'n roll  # keep\n    region: api\n",
			dropRule, inSide, 2},
		{"a change of a plain continuation", "mute:\n  - rule: a\n      b # keep\n    region: api\n", dropRule, inSide, 2},
		{"a change of a quoted continuation", "mute:\n  - region: api\n    rule: \"a\n      b\" # keep\n", dropRule, inSide, 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.before)
			snap := inspect(t, root)

			_, err := PlanPatch(snap, integration.Builtin(), []Edit{c.edit(t, snap)})
			require.ErrorIs(t, err, ErrBlocked)
			assert.Contains(t, err.Error(), fmt.Sprintf("%s (line %d); edit .seamark/lessons.yaml by hand", c.reason, c.line))
			assert.Equal(t, c.before, readLessons(t, root))
		})
	}

	// A "#" inside quotes, inside a flow item, or without white space
	// before it is value text, so these edits still plan. A comment on the
	// key line of a reset key goes with the key.
	keeps := []struct {
		name, before string
		edit         func(t *testing.T, snap *Snapshot) Edit
		after        string
	}{
		{"a removal of a quoted hash", "mute:\n  - rule: 'a # b'\n  - rule: c\n", removeFirst, "mute:\n  - rule: c\n"},
		{"a removal of a hash in a flow item", "mute:\n  - {rule: 'a # b'}\n  - rule: c\n", removeFirst, "mute:\n  - rule: c\n"},
		{"a removal of a hash without white space", "mute:\n  - rule: a#b\n  - rule: c\n", removeFirst, "mute:\n  - rule: c\n"},
		{"a removal of a quoted continuation with a hash", "mute:\n  - rule: \"a\n      # b\"\n  - rule: c\n", removeFirst,
			"mute:\n  - rule: c\n"},
		{"a removal of an apostrophe", "mute:\n  - rule: it's\n  - rule: c\n", removeFirst, "mute:\n  - rule: c\n"},
		{"a change of a quoted hash", "mute:\n  - rule: 'a # b'\n    region: api\n", dropRule, "mute:\n  - region: api\n"},
		{"a reset with a comment on the key line", "threshold: 3   # three\nmute:\n", resetThreshold, "mute:\n"},
	}

	for _, c := range keeps {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.before)
			snap := inspect(t, root)

			saveSnap(t, snap, c.edit(t, snap))
			assert.Equal(t, c.after, readLessons(t, root))
		})
	}
}

// TestRemoveTopKeyRefusesACommentAfterAContinuation runs the reset splice
// on shapes that the lessons reader rejects, so no plan reaches them. A
// threshold or a budget is a number, and a mode is one word. The splice
// must still refuse them, because its rule does not depend on the reader.
func TestRemoveTopKeyRefusesACommentAfterAContinuation(t *testing.T) {
	for _, text := range []string{
		"threshold: 3\n  4 # keep\nmute:\n",
		"threshold: rock\n  'n roll # keep\nmute:\n",
		"threshold: '3\n  4' # keep\nmute:\n",
		"threshold: \"3\n  4\" # keep\nmute:\n",
	} {
		doc := parsed(t, text)

		span, ok := doc.KeySpan(doc.Root, "threshold", doc.Limit)
		require.True(t, ok)

		_, err := removeTopKey(doc, span)
		require.ErrorIs(t, err, ErrBlocked, "text %q", text)
		assert.Contains(t, err.Error(), "a comment sits inside the value of threshold (line 1)", "text %q", text)
	}

	for _, text := range []string{
		"threshold: '3\n  # 4'\nmute:\n",
		"threshold: 3\n  4#5\nmute:\n",
		"threshold: |\n  3 # 4\nmute:\n",
	} {
		doc := parsed(t, text)

		span, ok := doc.KeySpan(doc.Root, "threshold", doc.Limit)
		require.True(t, ok)

		out, err := removeTopKey(doc, span)
		require.NoError(t, err, "text %q", text)
		assert.Equal(t, "mute:\n", string(out))
	}
}

func TestDeliveryEdits(t *testing.T) {
	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"set keeps the trailing comment", []Edit{{Field: Threshold, Int: 5}},
			replaceOnce(t, richLessons, "threshold: 3       # three recurrences", "threshold: 5       # three recurrences")},
		{"an absent key goes at the end", []Edit{{Field: PinBudget, Int: 4}},
			richLessons + "\npin_budget: 4\n"},
		{"reset removes the key line", []Edit{{Field: Threshold, Reset: true}},
			replaceOnce(t, richLessons, "threshold: 3       # three recurrences\n", "")},
		{"a mode set", []Edit{{Field: HookDelivery, String: "once-per-context"}},
			richLessons + "\nhook_delivery: once-per-context\n"},
		{"three keys in one plan", []Edit{{Field: Threshold, Int: 4}, {Field: ChangeBudget, Int: 9}, {Field: PinBudget, Int: 2}},
			replaceOnce(t, richLessons, "threshold: 3 ", "threshold: 4 ") + "\nchange_budget: 9\npin_budget: 2\n"},
		// A reset and an insertion in one plan: the line rule meets a
		// region and the insertion point in one pass. The blank lines
		// around the removed key stay, so two blank lines meet.
		{"a reset and an absent key", []Edit{{Field: Threshold, Reset: true}, {Field: PinBudget, Int: 4}},
			replaceOnce(t, richLessons, "threshold: 3       # three recurrences\n", "") + "\npin_budget: 4\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, richLessons)

			res := saveLessons(t, root, c.edits...)
			assert.Equal(t, Written, res.Outcome)
			assert.False(t, res.Created)
			assert.Equal(t, c.want, readLessons(t, root))
		})
	}

	t.Run("reset removes the value lines too", func(t *testing.T) {
		root := withLessons(t, "hook_delivery:\n  always\nthreshold: 2\n")

		saveLessons(t, root, Edit{Field: HookDelivery, Reset: true})
		assert.Equal(t, "threshold: 2\n", readLessons(t, root))
	})

	t.Run("reset refuses a comment inside the value", func(t *testing.T) {
		const text = "threshold:\n  # the team wants 3\n  3\n"

		root := withLessons(t, text)

		_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{{Field: Threshold, Reset: true}})
		require.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "a comment sits inside the value of threshold (line 1)")
		assert.Equal(t, text, readLessons(t, root))
	})

	// The pin text ends at the last line of the pin node. The comments
	// after the removed key are top-level lines, not pin text.
	t.Run("reset of the key after the pin list", func(t *testing.T) {
		for _, before := range []string{
			"pin:\n  - rule: P\n    note: n\npin_budget: 3\n\n# Ambient delivery\nhook_delivery: once-per-context\n",
			"pin:\n  - rule: P\npin_budget: 3\n# foot\n",
			"pin:\n  # - rule: RUF001\n\npin_budget: 3\n\n# Ambient edit-hook delivery.\n# hook_delivery: once-per-context\n",
		} {
			root := withLessons(t, before)

			saveLessons(t, root, Edit{Field: PinBudget, Reset: true})
			assert.Equal(t, replaceOnce(t, before, "pin_budget: 3\n", ""), readLessons(t, root))
		}
	})

	t.Run("a value on its own line refuses a set", func(t *testing.T) {
		root := withLessons(t, "threshold:\n  3\n")

		_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{{Field: Threshold, Int: 4}})
		require.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "starts on its own line")
	})
}

func TestStoredLegacyValuesSurviveOtherEdits(t *testing.T) {
	const (
		legacy = "threshold: 0x10\npin_budget: -1\nchange_budget: 1.5\nhook_delivery: ''\nmute:\n  - rule: a\n"
		zeros  = "threshold: 0\nchange_budget: 0\nmute:\n  - rule: a\n"
	)

	// The field and the note of each stored line after the edit. An
	// empty note means that the stored text reads as it is written.
	notes := map[string]struct {
		field Field
		note  string
	}{
		"threshold: 0x10\n":    {Threshold, "stored 0x10 → 16"},
		"pin_budget: -1\n":     {PinBudget, "stored -1 → default 3"},
		"change_budget: 1.5\n": {ChangeBudget, "stored 1.5 → 1"},
		"hook_delivery: ''\n":  {HookDelivery, ""},
		"threshold: 0\n":       {Threshold, "stored 0 → default 2"},
		"change_budget: 0\n":   {ChangeBudget, "stored 0 → default 6"},
	}

	cases := []struct {
		name, text string
		edit       Edit
		kept       []string
	}{
		{"a pin budget set", legacy, Edit{Field: PinBudget, Int: 4},
			[]string{"threshold: 0x10\n", "change_budget: 1.5\n", "hook_delivery: ''\n"}},
		{"a mute add", legacy, add("b", ""),
			[]string{"threshold: 0x10\n", "pin_budget: -1\n", "change_budget: 1.5\n", "hook_delivery: ''\n"}},
		{"a mode set", legacy, Edit{Field: HookDelivery, String: "always"},
			[]string{"threshold: 0x10\n", "pin_budget: -1\n", "change_budget: 1.5\n"}},
		{"a stored 0 and a pin budget set", zeros, Edit{Field: PinBudget, Int: 4},
			[]string{"threshold: 0\n", "change_budget: 0\n"}},
		{"a stored 0 and a mute add", zeros, add("b", ""),
			[]string{"threshold: 0\n", "change_budget: 0\n"}},
		{"a stored 0 and a threshold reset", zeros, Edit{Field: Threshold, Reset: true},
			[]string{"change_budget: 0\n"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.text)

			saveLessons(t, root, c.edit)

			got := readLessons(t, root)
			snap := inspect(t, root)

			for _, line := range c.kept {
				assert.Contains(t, got, line, "the stored text stays as written")

				want, ok := notes[line]
				require.True(t, ok, line)

				st := field(t, snap, want.field)
				if want.note == "" {
					assert.Empty(t, st.Notes, line)

					continue
				}

				assert.Equal(t, []string{want.note}, st.Notes, line)
			}
		})
	}
}

func TestInvalidLessonsEditsAreRejected(t *testing.T) {
	snap := inspect(t, withLessons(t, richLessons))

	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"zero", []Edit{{Field: Threshold}}, "from 1 to 2147483647"},
		{"negative", []Edit{{Field: PinBudget, Int: -1}}, "from 1 to 2147483647"},
		{"overflow", []Edit{{Field: ChangeBudget, Int: math.MaxInt32 + 1}}, "from 1 to 2147483647"},
		{"unknown mode", []Edit{{Field: HookDelivery, String: "Always"}}, `hook_delivery must be "always" or "once-per-context"`},
		{"a number for the mode", []Edit{{Field: HookDelivery, Int: 1}}, "does not take that kind of value"},
		{"a mode for a number", []Edit{{Field: Threshold, String: "3"}}, "does not take that kind of value"},
		{"two documents", []Edit{{Field: Threshold, Int: 3}, {Field: DistillWrite, Bool: true}}, "one plan edits one settings group"},
		{"delivery and a mute", []Edit{{Field: Threshold, Int: 3}, add("a", "")}, "one plan edits one settings group"},
		{"a mute field without an operation", []Edit{{Field: Mutes}}, "does not take that kind of value"},
		{"an unknown operation", []Edit{{Field: Mutes, Mute: &MuteEdit{Rule: "a"}}}, "unknown mute operation"},
		{"a mute reset", []Edit{{Field: Mutes, Reset: true}}, "the mute list has no reset"},
		{"a removal with values", []Edit{{Field: Mutes, Mute: &MuteEdit{Op: MuteRemove, Rule: "a"}}}, "takes no rule and no region"},
		{"an add with a reference", []Edit{{Field: Mutes, Mute: &MuteEdit{Op: MuteAdd, Rule: "a", Ref: MuteRef{Index: 1}}}}, "takes no entry reference"},
		{"a bad rule", []Edit{add(" a", "")}, "the rule starts or ends with a space"},
		{"a bad region", []Edit{add("", "../x")}, `the region contains ".."`},
		{"a removal of a missing entry", []Edit{{Field: Mutes, Mute: &MuteEdit{Op: MuteRemove, Ref: MuteRef{Index: 9}}}}, "choose it again"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := PlanPatch(snap, integration.Builtin(), c.edits)
			require.ErrorIs(t, err, ErrInvalidEdit)
			assert.Contains(t, err.Error(), c.want)
		})
	}

	p := planOf(t, withLessons(t, richLessons), Edit{Field: ChangeBudget, Int: math.MaxInt32})
	assert.Contains(t, string(p.Bytes.After), "change_budget: 2147483647\n", "the largest value saves")
}

// B3: the number editor of the console asks CheckNumber, the rule of
// the planner, so it shows the verdict of the save before the review.
func TestCheckNumberIsTheRuleOfThePlanner(t *testing.T) {
	for _, n := range []int{1, 7, math.MaxInt32} {
		assert.NoError(t, CheckNumber(PinBudget, n))
	}

	for _, n := range []int{0, -1, math.MaxInt32 + 1} {
		err := CheckNumber(PinBudget, n)
		require.Error(t, err)
		assert.Equal(t, "pin_budget must be a whole number from 1 to 2147483647", err.Error())
		assert.NotErrorIs(t, err, ErrInvalidEdit, "the console shows the text without the planner prefix")
	}
}

// B3 review of the delivery editor: a reset of the only key of the file
// refused with "the final line break is gone". The candidate is then an
// empty file, which has no line to end.
func TestDeliveryResetOfTheOnlyKeyEmptiesTheFile(t *testing.T) {
	root := withLessons(t, "threshold: 4\n")

	p := planOf(t, root, Edit{Field: Threshold, Reset: true})
	assert.Empty(t, p.Bytes.After)

	res := save(t, root, Edit{Field: Threshold, Reset: true})
	assert.Equal(t, Written, res.Outcome)

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pinfile.File)))
	require.NoError(t, err)
	assert.Empty(t, data)

	st := field(t, inspect(t, root), Threshold)
	assert.False(t, st.Stored.Present)
	assert.Equal(t, "2", st.Effective)
}

// A byte order mark is no block, and an edit that keeps the first line
// keeps the mark. yaml.v3 does not count the mark as a column, so the
// splices skip it (yamledit.Doc.Offset).
func TestLessonsEditsKeepAByteOrderMark(t *testing.T) {
	const bom = "\ufeff"

	cases := []struct {
		name, before string
		edit         func(t *testing.T, snap *Snapshot) Edit
		after        string
	}{
		{
			"a value on the first line", bom + "threshold: 2\nmute:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Int: 5} },
			bom + "threshold: 5\nmute:\n  - rule: a\n",
		},
		{
			"a mute add", bom + "threshold: 2\nmute:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return add("F541", "") },
			bom + "threshold: 2\nmute:\n  - rule: a\n  - rule: F541\n",
		},
		{
			"a mute change under the first key", bom + "mute:\n  - rule: a\n",
			func(t *testing.T, snap *Snapshot) Edit { return change(t, snap, 0, "b", "") },
			bom + "mute:\n  - rule: b\n",
		},
		{
			"a mute removal under the first key", bom + "mute:\n  - rule: a\n",
			func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) },
			bom + "mute:\n",
		},
		{
			"a new key", bom + "mute:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Int: 5} },
			bom + "mute:\n  - rule: a\n\nthreshold: 5\n",
		},
		// Finding N1: a reset of the key on line 1 deleted the mark at the
		// start of that line. The next line now takes the mark.
		{
			"a reset of the first line", bom + "threshold: 2\nmute:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} },
			bom + "mute:\n  - rule: a\n",
		},
		{
			"a reset before a comment line", bom + "threshold: 2\n# the mutes\nmute:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} },
			bom + "# the mutes\nmute:\n  - rule: a\n",
		},
		{
			"a reset before a blank line", bom + "threshold: 2\n\npin:\n  - rule: a\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} },
			bom + "\npin:\n  - rule: a\n",
		},
		// An empty file holds no text to mark, and the lessons reader reads
		// it as the defaults, as it reads a file without the mark.
		{
			"a reset of the only key", bom + "threshold: 2\n",
			func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} },
			"",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.before)
			snap := inspect(t, root)

			require.NoError(t, snap.Lessons.Blocked, "a byte order mark is no block")
			require.NoError(t, snap.Lessons.ReaderErr)
			assert.NoError(t, snap.Editable(GroupDelivery))
			assert.NoError(t, snap.Editable(GroupMutes))

			saveSnap(t, snap, c.edit(t, snap))
			assert.Equal(t, c.after, readLessons(t, root))

			_, err := reviews.LoadConfig(root)
			require.NoError(t, err, "the reader reads the saved file")
		})
	}

	// The second reset parses the text of the first one, whose line 1 has
	// the mark again. So the mark moves twice.
	t.Run("two resets of the first lines", func(t *testing.T) {
		root := withLessons(t, bom+"threshold: 2\npin_budget: 3\nmute:\n")

		saveLessons(t, root, Edit{Field: Threshold, Reset: true}, Edit{Field: PinBudget, Reset: true})
		assert.Equal(t, bom+"mute:\n", readLessons(t, root))
	})
}

func TestLessonsPlansKeepThePinsByteForByte(t *testing.T) {
	root := withLessons(t, richLessons)
	snap := inspect(t, root)

	at := strings.Index(richLessons, "pin:\n")
	require.GreaterOrEqual(t, at, 0)

	pins := richLessons[at:]

	for _, e := range []Edit{
		{Field: Threshold, Int: 9},
		{Field: Threshold, Reset: true},
		{Field: PinBudget, Int: 1},
		{Field: HookDelivery, String: "once-per-context"},
		add("Z9", ""),
		change(t, snap, 0, "F542", "web"),
	} {
		p, err := PlanPatch(snap, integration.Builtin(), []Edit{e})
		require.NoError(t, err, "edit %s", e.Field)

		after := string(p.Bytes.After)
		assert.Contains(t, after, pins, "edit %s", e.Field)

		cfg, err := reviews.ParseConfig(p.Bytes.After)
		require.NoError(t, err)
		assert.Equal(t, snap.Lessons.Config.Pin, cfg.Pin)
	}

	// Each mute entry of richLessons holds a comment after its text, so a
	// removal refuses there. The entry without the comment can go.
	bare := inspect(t, withLessons(t, replaceOnce(t, richLessons, "  - rule: F541     # noisy\n", "  - rule: F541\n")))

	p, err := PlanPatch(bare, integration.Builtin(), []Edit{remove(t, bare, 0)})
	require.NoError(t, err)
	assert.Contains(t, string(p.Bytes.After), pins)
}

func TestLessonsPlanNamesItsFileAndBytes(t *testing.T) {
	root := withConfig(t, richConfig)
	writeLessons(t, root, richLessons)

	p := planOf(t, root, Edit{Field: Threshold, Int: 4})

	assert.Equal(t, pinfile.File, p.File)
	assert.Equal(t, richLessons, string(p.Bytes.Before))
	require.NotNil(t, p.Setup)
	assert.Equal(t, p.Setup.Writes[0].After, p.Bytes.After)
	assert.Equal(t, pinfile.File, p.Preview.Path)
	assert.Equal(t, 1, p.Preview.Added)
	assert.Equal(t, 1, p.Preview.Removed)

	noop := planOf(t, root, Edit{Field: Threshold, Int: 3})
	assert.True(t, noop.NoOp)
	assert.Equal(t, pinfile.File, noop.File)
	assert.Equal(t, richLessons, string(noop.Bytes.After), "a no-op plan carries its bytes too")

	cp := planOf(t, root, Edit{Field: DistillWrite, Bool: true})
	assert.Equal(t, File, cp.File)
	assert.Zero(t, cp.Bytes, "a config.yaml plan has no lessons bytes")

	assert.Equal(t, filepath.Join(root, filepath.FromSlash(pinfile.File)), p.Path(), "a lessons plan names lessons.yaml")
	assert.Equal(t, filepath.Join(root, filepath.FromSlash(File)), cp.Path(), "a config plan names config.yaml")
	assert.Empty(t, (*Plan)(nil).Path())

	cnoop := planOf(t, root, Edit{Field: AgentCLI, String: "claude"})
	require.True(t, cnoop.NoOp)
	assert.Equal(t, []string{noteNoOp}, noop.Notes)
	assert.Equal(t, noop.Notes, cnoop.Notes, "both documents give the one no-op note")

	require.Len(t, p.Before, 4)
	assert.Equal(t, "3", p.Before[0].Effective)
	assert.Equal(t, "4", p.After[0].Effective)
}

func TestLessonsActivationTexts(t *testing.T) {
	cases := []struct {
		edit Edit
		when string
	}{
		{Edit{Field: Threshold, Int: 5}, "the next edit hook, change_set, check, why, seamark lessons --file, and the Stats lesson counts; mined lessons only, never pins"},
		{add("a", ""), "the next edit hook, change_set, check, why, seamark lessons --file, and the Stats lesson counts; mined lessons only, never pins"},
		{Edit{Field: PinBudget, Int: 5}, "the edit hook only, at most 8 lessons in total per edit; pins come first, so a budget at the total leaves no room for mined lessons"},
		{Edit{Field: ChangeBudget, Int: 5}, "one change_set answer or one check advisory, shared by all its files; not the hook or why"},
		{Edit{Field: HookDelivery, String: "once-per-context"}, "repetition in the edit hook only; change_set, check, why, and CLI views always show their selection"},
	}

	for _, c := range cases {
		p := planOf(t, withLessons(t, richLessons), c.edit)

		require.Len(t, p.Activation, 1)
		assert.Equal(t, c.edit.Field, p.Activation[0].Field)
		assert.Equal(t, c.when, p.Activation[0].When)
		assert.Contains(t, p.Activation[0].Notes, "Applies when a consumer next reads the file; a hook that runs now does not change.")
		assert.Equal(t, c.edit.Field.Activation(), p.Activation[0], "a plan adds no snapshot note to a lessons field")
		assert.NotContains(t, p.Notes, noteIndexStale, "the index fingerprint does not cover lessons.yaml")
		assert.Contains(t, p.Notes, noteCommit)

		if c.edit.Field.Group() == GroupDelivery {
			assert.Contains(t, p.Activation[0].Notes,
				"Saving does not install or change hooks, grant trust, reset or delete delivery state, re-mine, or call a model.")
			assert.Contains(t, p.Activation[0].Notes, "The hook state file stays; switching back can reuse still-valid context state.")
		}
	}
}

func TestLessonsPlanStaleness(t *testing.T) {
	t.Run("a change after the inspection", func(t *testing.T) {
		root := withLessons(t, richLessons)
		snap := inspect(t, root)

		writeLessons(t, root, richLessons+"# edited\n")

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: Threshold, Int: 4}})
		require.ErrorIs(t, err, ErrStale)
		assert.Contains(t, err.Error(), ".seamark/lessons.yaml; inspect it again")
	})

	t.Run("a mode change after the plan", func(t *testing.T) {
		root := withLessons(t, richLessons)
		p := planOf(t, root, Edit{Field: Threshold, Int: 4})

		require.NoError(t, os.Chmod(lessonsPath(root), 0o600))

		res, err := Save(p)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, NotWritten, res.Outcome)
		assert.Equal(t, richLessons, readLessons(t, root))
	})

	t.Run("a change after a no-op plan", func(t *testing.T) {
		root := withLessons(t, richLessons)
		p := planOf(t, root, Edit{Field: Threshold, Int: 3})
		require.True(t, p.NoOp)

		writeLessons(t, root, "threshold: 9\n")

		_, err := Save(p)
		require.ErrorIs(t, err, ErrStale)
	})
}

func TestHardLinkedLessonsFileIsRefused(t *testing.T) {
	link := func(t *testing.T, root string) {
		t.Helper()

		if err := os.Link(lessonsPath(root), filepath.Join(root, "other.yaml")); err != nil {
			t.Skipf("no hard links here: %v", err)
		}
	}

	t.Run("at inspection", func(t *testing.T) {
		root := withLessons(t, richLessons)
		link(t, root)

		snap := inspect(t, root)
		if linked, _ := integration.HardLinked(root, pinfile.File); !linked {
			t.Skip("the platform reports no link count")
		}

		require.ErrorIs(t, snap.Lessons.Blocked, ErrBlocked)
		assert.Contains(t, snap.Lessons.Blocked.Error(), "replacing this file would break its hard link; edit .seamark/lessons.yaml by hand")

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: Threshold, Int: 4}})
		require.ErrorIs(t, err, ErrBlocked)
	})

	t.Run("after the plan", func(t *testing.T) {
		root := withLessons(t, richLessons)
		p := planOf(t, root, Edit{Field: Threshold, Int: 4})

		link(t, root)

		if linked, _ := integration.HardLinked(root, pinfile.File); !linked {
			t.Skip("the platform reports no link count")
		}

		res, err := Save(p)
		require.ErrorIs(t, err, ErrBlocked)
		assert.Equal(t, NotWritten, res.Outcome)
		assert.Equal(t, richLessons, readLessons(t, root))
	})
}

func TestSaveOutcomesThroughTheWriter(t *testing.T) {
	failure := errors.New("disk full")

	cases := []struct {
		name    string
		write   func(t *testing.T, root string) Writer
		outcome Outcome
		check   func(t *testing.T, root string)
	}{
		{
			"written: the bytes landed before the error",
			func(_ *testing.T, _ string) Writer {
				return func(p *integration.SetupPlan, o integration.ApplyOptions) (integration.SetupResult, error) {
					res, err := integration.ApplySetup(p, o)
					if err != nil {
						return res, err
					}

					return res, failure
				}
			},
			Written,
			func(t *testing.T, root string) { assert.Contains(t, readLessons(t, root), "threshold: 4") },
		},
		{
			"not written: the file holds the old bytes",
			func(_ *testing.T, _ string) Writer {
				return func(*integration.SetupPlan, integration.ApplyOptions) (integration.SetupResult, error) {
					return integration.SetupResult{}, failure
				}
			},
			NotWritten,
			func(t *testing.T, root string) { assert.Equal(t, richLessons, readLessons(t, root)) },
		},
		{
			"unknown: the file cannot be read again",
			func(t *testing.T, root string) Writer {
				return func(*integration.SetupPlan, integration.ApplyOptions) (integration.SetupResult, error) {
					require.NoError(t, os.Remove(lessonsPath(root)))
					require.NoError(t, os.Mkdir(lessonsPath(root), 0o755))

					return integration.SetupResult{}, failure
				}
			},
			Unknown,
			func(t *testing.T, root string) {},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, richLessons)
			p := planOf(t, root, Edit{Field: Threshold, Int: 4})

			res, err := SaveWith(p, c.write(t, root))
			require.ErrorIs(t, err, failure, "the write error stays")
			assert.Equal(t, err, res.Err)
			assert.Equal(t, c.outcome, res.Outcome)
			assert.Equal(t, c.outcome == Written, res.Written)
			c.check(t, root)

			if c.outcome == Unknown {
				assert.Contains(t, err.Error(), "reading .seamark/lessons.yaml again failed")
			}
		})
	}

	t.Run("a stale plan is a refusal before the write", func(t *testing.T) {
		root := withLessons(t, richLessons)
		p := planOf(t, root, Edit{Field: Threshold, Int: 4})

		res, err := SaveWith(p, func(*integration.SetupPlan, integration.ApplyOptions) (integration.SetupResult, error) {
			return integration.SetupResult{}, integration.ErrStalePlan
		})
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, NotWritten, res.Outcome)
	})

	t.Run("the config.yaml save takes the same seam", func(t *testing.T) {
		root := withConfig(t, richConfig)
		p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})

		res, err := SaveWith(p, func(*integration.SetupPlan, integration.ApplyOptions) (integration.SetupResult, error) {
			return integration.SetupResult{}, failure
		})
		require.ErrorIs(t, err, failure)
		assert.Equal(t, NotWritten, res.Outcome)
		assert.Equal(t, configPath(inspect(t, root).Root), res.Path)
	})

	t.Run("no writer", func(t *testing.T) {
		_, err := SaveWith(planOf(t, withLessons(t, richLessons), Edit{Field: Threshold, Int: 4}), nil)
		require.Error(t, err)
	})
}

// verifyCase plans one edit on richLessons and runs the verifier on a
// changed candidate instead of the planned bytes.
func verifyCase(t *testing.T, e Edit, mutate func(after string) string) error {
	t.Helper()

	snap := inspect(t, withLessons(t, richLessons))

	p, err := PlanPatch(snap, integration.Builtin(), []Edit{e})
	require.NoError(t, err, "the plan itself verifies")

	_, err = verifyLessons(snap.Lessons, e.Field.Group(), orderEdits([]Edit{e}), []byte(mutate(string(p.Bytes.After))))

	return err
}

func TestLessonsVerifierRefusesASpliceDefect(t *testing.T) {
	snap := inspect(t, withLessons(t, richLessons))

	cases := []struct {
		name   string
		edit   Edit
		mutate func(string) string
		reason string
	}{
		{"a comment elsewhere changes", Edit{Field: Threshold, Int: 4},
			func(s string) string { return strings.Replace(s, "# Lesson tuning.", "# Lesson tuning!", 1) },
			"line 1 changed outside the edited lines"},
		{"a blank line goes", Edit{Field: Threshold, Int: 4},
			func(s string) string { return strings.Replace(s, "\n\nmute:", "\nmute:", 1) },
			"changed outside the edited lines"},
		{"the final line break goes", Edit{Field: Threshold, Int: 4},
			func(s string) string { return strings.TrimSuffix(s, "\n") },
			"the final line break is gone"},
		{"another key changes", Edit{Field: Threshold, Int: 4},
			func(s string) string { return strings.Replace(s, "custom: [1, 2]", "custom: [1, 3]", 1) },
			"the custom key changed"},
		{"the edited key moves", Edit{Field: Threshold, Int: 4},
			func(s string) string {
				s = strings.Replace(s, "threshold: 4       # three recurrences\n", "", 1)

				return "threshold: 4\n" + s
			},
			"the threshold key moved"},
		{"a pin comment changes", Edit{Field: PinBudget, Int: 4},
			func(s string) string {
				return strings.Replace(s, "    region: scripts\n", "    region: scripts   # where\n", 1)
			},
			"the pin list changed"},
		{"a deeper line joins the pin list", Edit{Field: PinBudget, Int: 4},
			func(s string) string {
				return strings.Replace(s, "      # not a comment\n", "      # not a comment\n  # added\n", 1)
			},
			"the pin list changed"},
		{"a pin value changes", Edit{Field: PinBudget, Int: 4},
			func(s string) string { return strings.Replace(s, "      # not a comment", "      # not a comment!", 1) },
			"the pin key changed"},
		{"a reset leaves the key with the default", Edit{Field: Threshold, Reset: true},
			func(s string) string { return strings.Replace(s, "\nmute:", "threshold: 2\n\nmute:", 1) },
			"the threshold key is not in the intended state"},
		{"a set writes another value", Edit{Field: PinBudget, Int: 4},
			func(s string) string { return strings.Replace(s, "pin_budget: 4", "pin_budget: 5", 1) },
			"does not read exactly the intended lesson delivery change"},
		{"an add writes another item", add("E501", ""),
			func(s string) string { return strings.Replace(s, "  - rule: E501\n", "  - rule: E502\n", 1) },
			"the mute list does not hold exactly the intended change"},
		{"an add drops an unknown key", add("E501", ""),
			func(s string) string { return strings.Replace(s, "    reason: generated   # unknown key\n", "", 1) },
			"the mute list does not hold exactly the intended change"},
		{"a change touches another item", change(t, snap, 1, "W1", "vendor/"),
			func(s string) string { return strings.Replace(s, "rule: F541", "rule: F542", 1) },
			"the mute list does not hold exactly the intended change"},
		{"a change drops a comment outside the item", change(t, snap, 1, "W1", "vendor/"),
			func(s string) string { return strings.Replace(s, "  # - rule: E501\n", "", 1) },
			"changed outside the edited lines"},
		{"a text that does not parse", Edit{Field: Threshold, Int: 4},
			func(s string) string { return s + "x: [\n" },
			"the edited text does not parse"},
		{"a text that the reader rejects", Edit{Field: HookDelivery, String: "always"},
			func(s string) string { return strings.Replace(s, "hook_delivery: always", "hook_delivery: Always", 1) },
			"the lessons reader would reject the result"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyCase(t, c.edit, c.mutate)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to write .seamark/lessons.yaml")
			assert.Contains(t, err.Error(), c.reason)
		})
	}

	require.NoError(t, verifyCase(t, Edit{Field: Threshold, Int: 4}, func(s string) string { return s }),
		"the planned bytes pass")

	// The splices refuse to drop these comments and the mark, so no plan
	// gives such a candidate. The verifier must refuse it on its own,
	// because the line rule lets an allowed region take any lines.
	const (
		lineChanged = "a comment line changed"
		valueLost   = "a comment after a value changed"
	)

	resetThreshold := func(*testing.T, *Snapshot) Edit { return Edit{Field: Threshold, Reset: true} }

	crafted := []struct {
		name, before string
		edit         func(t *testing.T, snap *Snapshot) Edit
		candidate    string
		reason       string
	}{
		{"a change drops a comment inside the item",
			"mute:\n  - rule: a\n    # why region r: generated code\n    region: r\n  - rule: b\n",
			func(t *testing.T, snap *Snapshot) Edit { return change(t, snap, 0, "z", "r") },
			"mute:\n  - rule: z\n    region: r\n  - rule: b\n", lineChanged},
		{"a removal drops a comment line of the item",
			"mute:\n  - rule: a\n    # why region r: generated code\n    region: r\n  - rule: b\n",
			func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) },
			"mute:\n  - rule: b\n", lineChanged},
		{"a reset drops a comment inside the value", "threshold:\n  # about\n  3\nmute:\n", resetThreshold,
			"mute:\n", lineChanged},
		{"a removal keeps the item", "mute:\n  - rule: a\n  - rule: b\n",
			func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) },
			"mute:\n  - rule: a\n  - rule: b\n", "the mute list does not hold exactly the intended change"},
		// The M1/L1 class: a comment after the value text of a removed line.
		{"a reset drops the comment of a value line", "threshold:\n  3   # keep\nmute:\n", resetThreshold,
			"mute:\n", valueLost},
		{"a removal drops the comment of the dash line", "mute:\n  - rule: a   # keep\n  - rule: b\n",
			func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) },
			"mute:\n  - rule: b\n", valueLost},
		{"a removal drops the comment of a later item line", "mute:\n  - rule: a\n    region: b   # keep\n  - rule: c\n",
			func(t *testing.T, snap *Snapshot) Edit { return remove(t, snap, 0) },
			"mute:\n  - rule: c\n", valueLost},
		{"a change drops the comment of the removed key", "mute:\n  - rule: rock 'n roll  # keep\n    region: api\n",
			func(t *testing.T, snap *Snapshot) Edit { return change(t, snap, 0, "", "api") },
			"mute:\n  - region: api\n", valueLost},
		// Finding N1: a reset of line 1 must keep the mark.
		{"a reset drops the byte order mark", "\ufeffthreshold: 2\nmute:\n  - rule: a\n", resetThreshold,
			"mute:\n  - rule: a\n", "the byte order mark at the start of the file would change"},
	}

	for _, c := range crafted {
		t.Run(c.name, func(t *testing.T) {
			snap := inspect(t, withLessons(t, c.before))
			e := c.edit(t, snap)

			_, err := verifyLessons(snap.Lessons, e.Field.Group(), orderEdits([]Edit{e}), []byte(c.candidate))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to write .seamark/lessons.yaml: "+c.reason)
		})
	}

	// A reset deletes its key line with the comment on it, by design: the
	// comment describes the key. The verifier compares lines without the
	// mark, so a comment line that moves onto line 1 still matches.
	accepted := []struct {
		name, before, candidate string
	}{
		{"the comment of the reset key line", "threshold: 3   # three\nmute:\n", "mute:\n"},
		{"a comment line after the mark", "\ufeffthreshold: 2\n# the mutes\nmute:\n", "\ufeff# the mutes\nmute:\n"},
	}

	for _, c := range accepted {
		t.Run(c.name, func(t *testing.T) {
			snap := inspect(t, withLessons(t, c.before))
			e := Edit{Field: Threshold, Reset: true}

			_, err := verifyLessons(snap.Lessons, GroupDelivery, []Edit{e}, []byte(c.candidate))
			require.NoError(t, err)
		})
	}
}

func TestLineRuleAllowsOnlyTheNamedLines(t *testing.T) {
	before := parsed(t, "a: 1\nb: 2\nc: 3\n")

	cases := []struct {
		name    string
		after   string
		regions [][2]int
		inserts int
		ok      bool
	}{
		{"same text", "a: 1\nb: 2\nc: 3\n", nil, 0, true},
		{"a region replaced", "a: 1\nb: 9\nc: 3\n", [][2]int{{2, 2}}, 0, true},
		{"a region removed", "a: 1\nc: 3\n", [][2]int{{2, 2}}, 0, true},
		{"a change outside the region", "a: 0\nb: 9\nc: 3\n", [][2]int{{2, 2}}, 0, false},
		{"one insertion", "a: 1\nx: 0\nb: 2\nc: 3\n", nil, 1, true},
		{"an insertion at the end", "a: 1\nb: 2\nc: 3\nx: 0\n", nil, 1, true},
		{"two insertions", "a: 1\nx: 0\nb: 2\ny: 0\nc: 3\n", nil, 1, false},
		{"an insertion without a point", "a: 1\nx: 0\nb: 2\nc: 3\n", nil, 0, false},
		{"a reorder", "b: 2\na: 1\nc: 3\n", nil, 1, false},
		{"adjacent regions", "a: 1\nz: 0\n", [][2]int{{2, 2}, {3, 3}}, 0, true},
	}

	for _, c := range cases {
		err := lineRule(before, parsed(t, c.after), c.regions, c.inserts)

		if c.ok {
			assert.NoError(t, err, c.name)
		} else {
			assert.Error(t, err, c.name)
		}
	}
}
