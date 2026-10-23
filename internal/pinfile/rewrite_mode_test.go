package pinfile

// Tests of the lesson-check keys in the pin writers: the mode and check
// splices of Rewrite (contract K6), the strict check tier of prepare
// (contract K1, writer rule), and the round trip of the keys through
// the writers that do not set them (contract K1, writer table).

import (
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// checksLayout holds the check definitions of the spec example and one
// entry per layout that a mode or check splice meets: a note, region
// keys without a note, a rule only, a commented mode, a mode on the dash
// line, and the bound and unbound states.
const checksLayout = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
  - rule: no-note
    region: api
    regions: [api, db]
  - rule: rule-only
  - rule: enforced
    region: lib
    note: n
    mode: enforce
    check: frozen-assets
  - rule: commented
    region: src
    note: Do not add TODO markers.
    mode: check     # keep this comment
    check: no-todo
  - rule: with-check
    region: docs
    note: n
    check: no-todo
  - rule: with-mode
    region: cmd
    note: n
    mode: advisory
  - mode: check
    rule: dash-mode
    region: web
    check: no-todo
pin_budget: 2
`

// TestRewriteSetsModeAndCheck proves the K6 splices of Rewrite. A new
// key goes after the note, else after the last region key, else after
// the rule key, in the order mode, check. A present key changes in
// place with its trailing comment, and an empty value removes it. Every
// candidate reads back with the requested mode and check, and every
// other line stays byte for byte (the golden compares the whole file).
func TestRewriteSetsModeAndCheck(t *testing.T) {
	cases := []struct {
		name, rule string
		rw         Rewrite
		old, next  string
	}{
		{
			"K6 example: both keys after the note", "frozen-assets",
			Rewrite{Mode: text("enforce"), Check: text("frozen-assets")},
			"    note: Changes to these files require a maintainer-managed update.\n",
			"    note: Changes to these files require a maintainer-managed update.\n    mode: enforce\n    check: frozen-assets\n",
		},
		{
			"no note: after the last region key", "no-note",
			Rewrite{Mode: text("check"), Check: text("no-todo")},
			"    regions: [api, db]\n",
			"    regions: [api, db]\n    mode: check\n    check: no-todo\n",
		},
		{
			"no note and no region: after the rule key", "rule-only",
			Rewrite{Mode: text("check"), Check: text("no-todo")},
			"  - rule: rule-only\n",
			"  - rule: rule-only\n    mode: check\n    check: no-todo\n",
		},
		{
			"K6 example: advisory removes the mode and keeps the check", "enforced",
			Rewrite{Mode: text("")},
			"    note: n\n    mode: enforce\n    check: frozen-assets\n",
			"    note: n\n    check: frozen-assets\n",
		},
		{
			"mode in place keeps the trailing comment", "commented",
			Rewrite{Mode: text("enforce")},
			"    mode: check     # keep this comment\n",
			"    mode: enforce     # keep this comment\n",
		},
		{
			"check in place", "commented",
			Rewrite{Check: text("frozen-assets")},
			"    check: no-todo\n  - rule: with-check\n",
			"    check: frozen-assets\n  - rule: with-check\n",
		},
		{
			"empty check removes the key", "enforced",
			Rewrite{Check: text("")},
			"    mode: enforce\n    check: frozen-assets\n",
			"    mode: enforce\n",
		},
		{
			"new mode goes above a present check", "with-check",
			Rewrite{Mode: text("check")},
			"    region: docs\n    note: n\n    check: no-todo\n",
			"    region: docs\n    note: n\n    mode: check\n    check: no-todo\n",
		},
		{
			"new check goes below a mode that stays", "with-mode",
			Rewrite{Check: text("no-todo")},
			"    mode: advisory\n",
			"    mode: advisory\n    check: no-todo\n",
		},
		{
			"mode removed and check added in one rewrite", "with-mode",
			Rewrite{Mode: text(""), Check: text("no-todo")},
			"    region: cmd\n    note: n\n    mode: advisory\n",
			"    region: cmd\n    note: n\n    check: no-todo\n",
		},
		{
			"new note, mode, and check after the rule key, in that order", "rule-only",
			Rewrite{Note: text("Keep it."), Mode: text("check"), Check: text("no-todo")},
			"  - rule: rule-only\n",
			"  - rule: rule-only\n    note: Keep it.\n    mode: check\n    check: no-todo\n",
		},
		{
			"region change and new mode in one rewrite", "no-note",
			Rewrite{Regions: regionSet("api"), Mode: text("check"), Check: text("no-todo")},
			"    region: api\n    regions: [api, db]\n",
			"    region: api\n    mode: check\n    check: no-todo\n",
		},
		{
			"mode on the dash line changes in place", "dash-mode",
			Rewrite{Mode: text("enforce")},
			"  - mode: check\n    rule: dash-mode\n",
			"  - mode: enforce\n    rule: dash-mode\n",
		},
		{
			"a check ID that needs quotes", "rule-only",
			Rewrite{Check: text("1.0")},
			"  - rule: rule-only\n",
			"  - rule: rule-only\n    check: \"1.0\"\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := entryOf(t, Parse([]byte(checksLayout)), c.rule)

			got, err := rewriteOne(t, checksLayout, c.rule, c.rw)
			require.NoError(t, err)
			assert.Equal(t, swap(t, checksLayout, c.old, c.next), got)

			// The entry reads back with the request. A nil field keeps
			// the value, and the pin key never changes with the mode or
			// the check (contract K2).
			wantMode, wantCheck := before.Mode, before.Check
			if c.rw.Mode != nil {
				wantMode = *c.rw.Mode
			}

			if c.rw.Check != nil {
				wantCheck = *c.rw.Check
			}

			after := entryOf(t, Parse([]byte(got)), c.rule)
			assert.Equal(t, wantMode, after.Mode)
			assert.Equal(t, wantCheck, after.Check)
			assert.Empty(t, after.Unknown)
			assert.Empty(t, after.Block)

			if c.rw.Regions == nil {
				assert.Equal(t, before.Key, after.Key)
			}
		})
	}

	t.Run("refusals", func(t *testing.T) {
		refusals := []struct {
			name, rule string
			rw         Rewrite
			sentinel   error
			reason     string
		}{
			{"removal would drop the key comment", "commented", Rewrite{Mode: text("")}, ErrUnsupported, "removing the mode key would drop a comment"},
			{"removal of a mode on the dash line", "dash-mode", Rewrite{Mode: text("")}, ErrUnsupported, "the mode key is on the dash line"},
			{"the values are already current", "commented", Rewrite{Mode: text("check"), Check: text("no-todo")}, ErrNoOp, "the fields are already current"},
			{"an empty value for an absent key is no change", "rule-only", Rewrite{Mode: text(""), Check: text("")}, ErrNoOp, "the fields are already current"},
		}

		for _, c := range refusals {
			t.Run(c.name, func(t *testing.T) {
				_, err := rewriteOne(t, checksLayout, c.rule, c.rw)
				require.ErrorIs(t, err, c.sentinel)
				assert.Contains(t, err.Error(), c.reason)
			})
		}
	})

	// The planner writes the text that it gets and does not validate it.
	// The strict check tier of Prepare refuses an unknown mode.
	t.Run("the planner does not validate the mode word", func(t *testing.T) {
		got, err := rewriteOne(t, checksLayout, "with-check", Rewrite{Mode: text("sometimes")})
		require.NoError(t, err)
		assert.Equal(t, "sometimes", entryOf(t, Parse([]byte(got)), "with-check").Mode)
	})
}

// invalidChecks is the invalid config of contract K1 with one more,
// unrelated pin. A hand edit wrote it, and two errors are in it.
const invalidChecks = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO('
pin:
  - rule: single
    region: api
    note: Short note.
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todos
`

// TestPrepareRefusesInvalidChecks proves the K1 writer rule in prepare.
// A candidate that fails lessoncheck.Validate is refused with the K1
// text, which wraps ErrUnverified, and the file stays as it was. An
// error that an earlier hand edit made also refuses an unrelated edit
// (§5 row 5). A valid candidate with checks passes, and a restore is
// not a prepare, so it puts back the original bytes as they were.
func TestPrepareRefusesInvalidChecks(t *testing.T) {
	t.Run("an earlier hand edit refuses an unrelated retarget", func(t *testing.T) {
		root := writeFile(t, invalidChecks, 0o644)
		d, c := retargetSingle(t, root)

		_, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrUnverified)
		assert.Equal(t, "pinfile: the candidate does not validate: "+
			"checks.no-todo: pattern does not compile: error parsing regexp: missing closing ): `TODO(` (and 1 more)", err.Error())
		assert.Equal(t, invalidChecks, readPins(t, root))
	})

	t.Run("a rewrite that breaks the config is refused", func(t *testing.T) {
		root := writeFile(t, checksLayout, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		edit := Edit{Ref: entryOf(t, d, "frozen-assets").Ref(), Rewrite: &Rewrite{Mode: text("enforce")}}
		c, err := Plan(d, []Edit{edit}, nil)
		require.NoError(t, err)

		_, err = Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrUnverified)
		assert.Equal(t, "pinfile: the candidate does not validate: "+
			"pin 1 (frozen-assets @ generated): mode enforce requires check: <check-id> (and 0 more)", err.Error())
		assert.Equal(t, checksLayout, readPins(t, root))
	})

	t.Run("an unknown mode is refused", func(t *testing.T) {
		root := writeFile(t, checksLayout, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		edit := Edit{Ref: entryOf(t, d, "with-check").Ref(), Rewrite: &Rewrite{Mode: text("sometimes")}}
		c, err := Plan(d, []Edit{edit}, nil)
		require.NoError(t, err)

		_, err = Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.ErrorIs(t, err, ErrUnverified)
		assert.Equal(t, `pinfile: the candidate does not validate: `+
			`pin 6 (with-check @ docs): unknown mode "sometimes"; use advisory, check, or enforce (and 0 more)`, err.Error())
	})

	t.Run("an append refuses a newer format version", func(t *testing.T) {
		root := writeFile(t, "lesson_checks_version: 2\npin: []\n", 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		after := []byte("lesson_checks_version: 2\npin:\n  - rule: a\n    region: api\n    note: n\n")

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrUnverified)
		assert.Equal(t, "pinfile: the candidate does not validate: "+
			"lesson_checks_version 2 is not supported; this seamark reads up to 1 (and 0 more)", err.Error())
	})

	t.Run("a tier-1 error keeps its own text", func(t *testing.T) {
		root := writeFile(t, checksLayout, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		_, err = Prepare(integration.Builtin(), root, d.Guard, []byte("lesson_checks_version: -1\n"))
		require.ErrorIs(t, err, ErrUnverified)
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)
		assert.Contains(t, err.Error(), "the candidate does not load: lessons config: lesson_checks_version must be 1, got -1")
	})

	t.Run("a valid candidate with checks is written", func(t *testing.T) {
		root := writeFile(t, checksLayout, 0o644)
		d, err := Read(root)
		require.NoError(t, err)

		rw := Rewrite{Mode: text("enforce"), Check: text("frozen-assets")}
		c, err := Plan(d, []Edit{{Ref: entryOf(t, d, "frozen-assets").Ref(), Rewrite: &rw}}, nil)
		require.NoError(t, err)

		p, err := Prepare(integration.Builtin(), root, d.Guard, c.After)
		require.NoError(t, err)
		require.NoError(t, p.Apply())
		assert.Equal(t, string(c.After), readPins(t, root))
	})

	t.Run("a restore puts back an invalid original", func(t *testing.T) {
		root := writeFile(t, invalidChecks, 0o644)
		fixed := []byte("pin:\n  - rule: single\n    region: api\n    note: Short note.\n")
		require.NoError(t, os.WriteFile(pinPath(root), fixed, 0o644))

		require.NoError(t, Restore(integration.Builtin(), root, sha256.Sum256(fixed), []byte(invalidChecks)))
		assert.Equal(t, invalidChecks, readPins(t, root))
	})
}

// modeChecks returns the mode and the check of each entry of text, in
// file order. The pairs leave out the rule, so a rename keeps the list.
func modeChecks(t *testing.T, text string) []string {
	t.Helper()

	var out []string

	for _, e := range Parse([]byte(text)).Entries {
		out = append(out, fmt.Sprintf("mode=%q check=%q", e.Mode, e.Check))
	}

	return out
}

// requireValidChecks requires that text passes the strict check tier.
// A writer that refuses its candidate would otherwise look like a writer
// that drops the keys.
func requireValidChecks(t *testing.T, text string) {
	t.Helper()

	cfg, err := reviews.ParseConfig([]byte(text))
	require.NoError(t, err)
	require.Empty(t, lessoncheck.Validate(cfg))
}

// TestRewriteKeepsModeAndCheck proves the K1 writer row "Rewrite". An
// edit that sets no mode and no check keeps both keys of every entry.
// The cases are a rule, note, or region rewrite, a retarget, and the
// removal of an entry. Prepare accepts each candidate, the file gets
// the golden bytes, and the checks map stays.
func TestRewriteKeepsModeAndCheck(t *testing.T) {
	requireValidChecks(t, checksLayout)

	cases := []struct {
		name, rule string
		edit       Edit
		old, next  string
	}{
		{
			"a rule rename of an enforced pin", "enforced",
			Edit{Rewrite: &Rewrite{Rule: text("enforced-assets")}},
			"  - rule: enforced\n", "  - rule: enforced-assets\n",
		},
		{
			"a note rewrite above a commented mode", "commented",
			Edit{Rewrite: &Rewrite{Note: text("Never add TODO markers.")}},
			"    note: Do not add TODO markers.\n", "    note: Never add TODO markers.\n",
		},
		{
			"a region rewrite of a pin with a check", "with-check",
			Edit{Rewrite: &Rewrite{Regions: regionSet("docs", "site")}},
			"    region: docs\n", "    region: docs\n    regions: [docs, site]\n",
		},
		{
			"a retarget of a pin with the mode on the dash line", "dash-mode",
			Edit{Regions: []string{"app"}},
			"    region: web\n", "    region: app\n",
		},
		{
			"a retarget of an enforced pin to two regions", "enforced",
			Edit{Regions: []string{"lib", "src"}},
			"    region: lib\n", "    region: lib\n    regions: [lib, src]\n",
		},
		{
			"the removal of an entry", "rule-only",
			Edit{Delete: true},
			"  - rule: rule-only\n", "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeFile(t, checksLayout, 0o644)
			d, err := Read(root)
			require.NoError(t, err)

			before := entryOf(t, d, c.rule)
			c.edit.Ref = before.Ref()

			change, err := Plan(d, []Edit{c.edit}, nil)
			require.NoError(t, err)

			p, err := Prepare(integration.Builtin(), root, d.Guard, change.After)
			require.NoError(t, err)
			require.NoError(t, p.Apply())

			got := readPins(t, root)
			assert.Equal(t, swap(t, checksLayout, c.old, c.next), got)
			requireValidChecks(t, got)

			// Every other entry keeps its mode and check. The edited entry
			// keeps them too, unless the edit removes it.
			want := modeChecks(t, checksLayout)
			if c.edit.Delete {
				at := slices.IndexFunc(d.Entries, func(e Entry) bool { return e.Rule == c.rule })
				want = slices.Delete(want, at, at+1)
			}

			assert.Equal(t, want, modeChecks(t, got))
		})
	}
}

// TestRestoreKeepsChecksMap proves the K1 writer row "Compensation". A
// maintenance commit whose ledger write fails restores the original
// bytes through pinfile.Restore (maintain pinWriter.Restore). Here the
// original holds the checks map and pins with a mode and a check.
// Restore puts back every byte of it and keeps the file mode.
func TestRestoreKeepsChecksMap(t *testing.T) {
	cases := []struct {
		name, rule string
		edit       Edit
	}{
		{"after a retarget of an enforced pin", "enforced", Edit{Regions: []string{"lib", "src"}}},
		{"after the removal of a mode", "enforced", Edit{Rewrite: &Rewrite{Mode: text("")}}},
		{"after the removal of a bound pin", "dash-mode", Edit{Delete: true}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeFile(t, checksLayout, 0o640)
			d, err := Read(root)
			require.NoError(t, err)

			c.edit.Ref = entryOf(t, d, c.rule).Ref()

			change, err := Plan(d, []Edit{c.edit}, nil)
			require.NoError(t, err)

			p, err := Prepare(integration.Builtin(), root, d.Guard, change.After)
			require.NoError(t, err)
			require.NoError(t, p.Apply())
			require.Equal(t, string(change.After), readPins(t, root))

			require.NoError(t, Restore(integration.Builtin(), root, sha256.Sum256(change.After), d.Bytes))
			assert.Equal(t, checksLayout, readPins(t, root))
			assert.Equal(t, modeChecks(t, checksLayout), modeChecks(t, readPins(t, root)))

			again, err := Read(root)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o640), again.Guard.Mode, "the restore keeps the file mode")
		})
	}
}
