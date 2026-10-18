package pinfile

// Failure injection for the verifier. A forged candidate tests checks 1
// to 5, which read only the candidate bytes. Check 7 tests the splices,
// so its cases change the splices or the spans that the planner uses,
// as a defect in the span code would.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundEdit binds one edit of the entry with rule in the emitted file.
func boundEdit(t *testing.T, rule string, edit Edit) (*analysis, *planned) {
	t.Helper()

	a := analyze([]byte(emitted))
	edit.Ref = entryOf(t, Parse([]byte(emitted)), rule).Ref()

	p := a.bind(edit, false)
	require.NoError(t, p.err)

	return a, p
}

func TestVerifyAcceptsTheSplicedCandidate(t *testing.T) {
	for _, edit := range []Edit{{Regions: []string{"web"}}, {Delete: true, Provenance: []int64{1}}} {
		a, p := boundEdit(t, "single", edit)

		require.NoError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}))
	}
}

func TestVerifyRefusesAForgedRetarget(t *testing.T) {
	a, p := boundEdit(t, "single", Edit{Regions: []string{"web"}})
	good := string(a.candidate([]*planned{p}))

	cases := []struct {
		name, from, to, reason string
	}{
		{"a mute changes", "  - rule: F541", "  - rule: F542", "a setting outside the pin list changes"},
		{"a setting changes", "threshold: 3", "threshold: 4", "a setting outside the pin list changes"},
		{"a top-level key moves", "threshold: 3\nmute:\n  - rule: F541\n", "mute:\n  - rule: F541\nthreshold: 3\n", "a setting outside the pin list changes"},
		{"another entry changes", "    note: \"n\"", "    note: \"m\"", "differs from the intended change"},
		{"the rule spelling changes", "rule: single", "rule: Single", "differs from the intended change"},
		{"the note changes", "note: Short note.", "note: Other.", "differs from the intended change"},
		{"two entries swap", "  - rule: single\n    region: web\n    note: Short note.\n", "", "differs from the intended change"},
		{"a comment changes", "# Review-lesson tuning", "# Review lesson tuning", "differ from the reviewed lines"},
		{"a provenance comment goes", "  # distilled by claude/v3 from 2 findings (seamark lessons --distill, p2)\n", "", "differ from the reviewed lines"},
		{"a blank line appears", "pin_budget: 2\n", "\npin_budget: 2\n", "differ from the reviewed lines"},
		{"the final line break goes", "pin_budget: 2\n", "pin_budget: 2", "differ from the reviewed lines"},
		{"the reader rejects it", "threshold: 3\n", "threshold: 3\nhook_delivery: sometimes\n", "fails the editing gate"},
		{"a mute gets an anchor", "  - rule: F541", "  - rule: &r F541", "a setting outside the pin list changes"},
		{"a pin gets an anchor", "    note: Short note.", "    note: &r Short note.", "fails the editing gate"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forged := strings.Replace(good, tc.from, tc.to, 1)
			require.NotEqual(t, good, forged, "the forgery must change the candidate")

			if tc.name == "two entries swap" {
				forged = strings.Replace(forged, "  - rule: repo\n", "  - rule: single\n    region: web\n    note: Short note.\n  - rule: repo\n", 1)
			}

			err := a.verify([]byte(forged), []*planned{p})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestVerifyRefusesARetargetThatDoesNotReadBack(t *testing.T) {
	// The values equal the intended change, but the regions list spans two
	// lines, so the entry is no longer editable. Only check 5 sees that.
	a, p := boundEdit(t, "single", Edit{Regions: []string{"api", "web"}})
	good := string(a.candidate([]*planned{p}))
	forged := strings.Replace(good, "    regions: [api, web]\n", "    regions: [api,\n      web]\n", 1)
	require.NotEqual(t, good, forged)

	err := a.verify([]byte(forged), []*planned{p})
	require.EqualError(t, err, "a retargeted entry does not read back as its target")
}

func TestVerifyRefusesAForgedPrune(t *testing.T) {
	a, p := boundEdit(t, "multi", Edit{Delete: true, Provenance: []int64{2}})
	good := string(a.candidate([]*planned{p}))

	cases := []struct {
		name, from, to, reason string
	}{
		{"a neighbour goes too", "  - rule: repo\n    region: '*'\n    note: |-\n      first line\n      # not a comment\n\n      last line\n", "", "differs from the intended change"},
		{"a half entry stays", "  - rule: repo\n    region: '*'\n", "", "fails the editing gate"},
		{"the next provenance goes", "  # distilled by claude/v3 from 1 findings (seamark lessons --distill, p3)\n", "", "differ from the reviewed lines"},
		{"a value line of the block note goes", "      # not a comment\n", "", "differs from the intended change"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forged := strings.Replace(good, tc.from, tc.to, 1)
			require.NotEqual(t, good, forged)

			err := a.verify([]byte(forged), []*planned{p})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestVerifyRefusesASpliceOutsideTheReviewedLines(t *testing.T) {
	// A splice that rewrites a line outside the entry with the same text
	// passes every value check. Only the line rule can refuse it.
	t.Run("retarget touches a comment", func(t *testing.T) {
		a, p := boundEdit(t, "single", Edit{Regions: []string{"web"}})
		p.splices = append(p.splices, splice{from: 6, to: 6, lines: []string{a.lines[5]}})

		err := a.verify(a.candidate([]*planned{p}), []*planned{p})
		require.ErrorContains(t, err, "line 6 is outside the reviewed entry")
	})

	t.Run("retarget inserts away from its keys", func(t *testing.T) {
		a, p := boundEdit(t, "single", Edit{Regions: []string{"web"}})
		p.splices = append(p.splices, splice{from: 10, to: 9, lines: []string{"  # inserted"}})

		err := a.verify(a.candidate([]*planned{p}), []*planned{p})
		require.ErrorContains(t, err, "an inserted line at line 10 is outside the reviewed entry")
	})

	t.Run("prune adds text", func(t *testing.T) {
		a, p := boundEdit(t, "single", Edit{Delete: true, Provenance: []int64{1}})
		p.splices[0].lines = []string{"  # left behind"}

		err := a.verify(a.candidate([]*planned{p}), []*planned{p})
		require.Error(t, err)
	})

	t.Run("prune deletes a line it does not own", func(t *testing.T) {
		a, p := boundEdit(t, "single", Edit{Delete: true})
		p.splices = append(p.splices, splice{from: 6, to: 6})

		err := a.verify(a.candidate([]*planned{p}), []*planned{p})
		require.ErrorContains(t, err, "line 6 is outside the reviewed entry")
	})
}

func TestVerifyRefusesASpanDefect(t *testing.T) {
	// Check 7 does not reuse the span code that built the splices. A
	// defect there, simulated by a moved span or an extra bound line,
	// must block the prune although every value stays right.
	bind := func(t *testing.T, file, rule string, provenance []int64) (*analysis, *planned) {
		t.Helper()

		a := analyze([]byte(file))
		p := a.bind(Edit{Ref: entryOf(t, Parse([]byte(file)), rule).Ref(), Delete: true, Provenance: provenance}, false)
		require.NoError(t, p.err)
		require.NoError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "the correct splices pass")

		return a, p
	}

	t.Run("the span takes the head comment of the next entry", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n    note: n\n  # user head comment of b\n  - rule: b\n", "a", nil)
		p.item.last++
		p.splices = a.prune(p)

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 4 is outside the reviewed entry")
	})

	t.Run("the span takes a trailing blank line", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n\n  - rule: b\n", "a", nil)
		p.item.last++
		p.splices = a.prune(p)

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 3 is outside the reviewed entry")
	})

	t.Run("the span leaves an own comment behind", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n    note: n\n    # about a\n  - rule: b\n", "a", nil)
		p.item.last--
		p.splices = a.prune(p)

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 4 of the reviewed entry stays")
	})

	t.Run("the span starts below the dash", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n    note: n\n  - rule: b\n", "a", nil)
		p.item.first++
		p.splices = a.prune(p)

		require.Error(t, a.verify(a.candidate([]*planned{p}), []*planned{p}))
	})

	t.Run("a blank line separates the provenance line", func(t *testing.T) {
		a, p := bind(t, "pin:\n  - rule: a\n  # distilled by c from 1 findings (seamark lessons --distill, p2)\n\n  - rule: b\n", "b", []int64{2})
		p.splices = append(p.splices, splice{from: 3, to: 3})

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 3 is outside the reviewed entry")
	})

	t.Run("a user comment separates the provenance line", func(t *testing.T) {
		a, p := bind(t, "pin:\n  # distilled by c from 1 findings (seamark lessons --distill, p1)\n  # mine\n  - rule: a\n", "a", []int64{1})
		p.splices = append(p.splices, splice{from: 2, to: 2})

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 2 is outside the reviewed entry")
	})

	t.Run("the provenance line is an own line of the entry above", func(t *testing.T) {
		a, p := bind(t, readmeLayout, "second", []int64{60})
		p.splices = append(p.splices, splice{from: 7, to: 7})

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 7 is outside the reviewed entry")
	})

	t.Run("a retarget span takes the next key", func(t *testing.T) {
		a, p := boundEdit(t, "multi", Edit{Regions: []string{"db"}})
		p.splices = []splice{
			{from: 12, to: 12, lines: []string{"    region: db"}},
			{from: 13, to: 14, lines: []string{"    note: \"n\""}},
		}

		require.EqualError(t, a.verify(a.candidate([]*planned{p}), []*planned{p}), "line 14 is outside the reviewed entry")
	})
}

func TestPlanReportsAnUnverifiedCombination(t *testing.T) {
	// The planner names every edit of a combination that fails, and leaves
	// no change. A forged splice stands in for a splice defect.
	a := analyze([]byte(emitted))
	d := Parse([]byte(emitted))

	ps := []*planned{
		a.bind(Edit{Ref: entryOf(t, d, "single").Ref(), Regions: []string{"web"}}, false),
		a.bind(Edit{Ref: entryOf(t, d, "repo").Ref(), Regions: []string{"lib"}}, false),
	}
	ps[1].splices = append(ps[1].splices, splice{from: 2, to: 2, lines: []string{"threshold: 9"}})

	assert.Nil(t, a.change(d.Bytes, ps))

	for _, p := range ps {
		require.ErrorIs(t, p.err, ErrUnverified)
		assert.Contains(t, p.err.Error(), "cannot prove the edit preserves the file")
	}
}
