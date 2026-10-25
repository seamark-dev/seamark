package maintain

// Tests of the check-definition authoring (contract K1, writer row
// "Check-definition authoring", AC-27). The splice changes only the
// checks.<id> entry and lesson_checks_version. The pin list keeps its
// bytes, every comment stays, and a candidate that fails the strict
// check tier is refused before any write.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// checkDefFixture is a workspace with lessons as the pin file and the
// gate on. An empty lessons means no pin file.
func checkDefFixture(t *testing.T, lessons string) *fixture {
	t.Helper()

	f := newFixture(t, nil)

	if lessons != "" {
		f.write(lessons, 0o644)
	}

	return f
}

// checkDefPlan plans req and requires no error.
func (f *fixture) checkDefPlan(req CheckDefRequest) *CheckDefPlan {
	f.t.Helper()

	p, err := PlanCheckDef(f.deps(), req)
	require.NoError(f.t, err)

	return p
}

// pinSection returns the text from the line "pin:" to the next
// top-level key or the end, as the test reads it: by text, not by the
// node lines of the verifier.
func pinSection(text string) string {
	at := strings.Index(text, "\npin:\n")
	if at < 0 {
		return ""
	}

	rest := text[at+1:]
	if end := strings.Index(rest, "\nmute:"); end >= 0 {
		return rest[:end]
	}

	return rest
}

// The definitions of the tests.
var (
	todoFixme  = CheckDefRequest{ID: "no-todo", Kind: "no-added-pattern/1", Pattern: "FIXME"}
	newFrozen  = CheckDefRequest{ID: "frozen", Kind: "forbid-path-change/1"}
	newPattern = CheckDefRequest{ID: "fixme", Kind: "no-added-pattern/1", Pattern: `FIXME\(`}
	newTodo    = CheckDefRequest{ID: "no-todo", Kind: "no-added-pattern/1", Pattern: `TODO\(`}
)

// plainLessons holds two advisory pins, a tuning value, and a mute
// list, and no lesson-check key.
const plainLessons = `threshold: 3
pin:
  # a comment above the first pin
  - rule: keep-docs
    region: docs
    note: Keep the docs current.
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    check: no-todo
mute:
  - rule: noisy
`

// TestCheckDefinitionEditKeepsPins proves the K1 writer row
// "Check-definition authoring". Each splice changes only checks.<id> and
// lesson_checks_version, keeps the pin list byte for byte with its
// comments, shows the impact block of each bound executable pin before
// the save, and writes through the guarded pin file write.
func TestCheckDefinitionEditKeepsPins(t *testing.T) {
	cases := []struct {
		name, lessons string
		req           CheckDefRequest
		want          string
		create        bool
		version       bool
		impact        []int
	}{
		{
			name: "a pattern edit replaces the entry", lessons: checkWritersLessons, req: todoFixme,
			want: strings.Replace(checkWritersLessons, "    pattern: 'TODO\\('\n", "    pattern: FIXME\n", 1),
			// The two executable pins that bind no-todo get a block; the
			// advisory pin with the check gets none.
			impact: []int{1, 2},
		},
		{
			name: "a new definition joins the map", lessons: checkWritersLessons, req: newPattern,
			want: strings.Replace(checkWritersLessons, "    pattern: 'TODO\\('\npin:\n",
				"    pattern: 'TODO\\('\n  fixme:\n    kind: no-added-pattern/1\n    pattern: FIXME\\(\npin:\n", 1),
			create: true,
		},
		{
			name: "both keys are new", lessons: plainLessons, req: newTodo,
			want: plainLessons + "\nlesson_checks_version: 1\n\nchecks:\n  no-todo:\n    kind: no-added-pattern/1\n" +
				"    pattern: TODO\\(\n",
			create: true, version: true,
		},
		{
			name: "an explicit version 0 changes in place", req: newTodo,
			lessons: "lesson_checks_version: 0 # set later\n" + plainLessons,
			want: "lesson_checks_version: 1 # set later\n" + plainLessons +
				"\nchecks:\n  no-todo:\n    kind: no-added-pattern/1\n    pattern: TODO\\(\n",
			create: true, version: true,
		},
		{
			name: "an empty checks key takes the entry", req: newTodo,
			lessons: "lesson_checks_version: 1\nchecks: # none yet\n" + plainLessons,
			want: "lesson_checks_version: 1\nchecks: # none yet\n  no-todo:\n    kind: no-added-pattern/1\n" +
				"    pattern: TODO\\(\n" + plainLessons,
			create: true,
		},
		{
			name: "an absent file is created", req: newFrozen,
			want:   "lesson_checks_version: 1\n\nchecks:\n  frozen:\n    kind: forbid-path-change/1\n",
			create: true, version: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := checkDefFixture(t, tc.lessons)

			p := f.checkDefPlan(tc.req)
			require.True(t, p.Confirmable, "blocked: %v", p.Blockers)
			assert.Equal(t, tc.want, string(p.After))
			assert.Equal(t, tc.create, p.Create)
			assert.Equal(t, tc.version, p.Version)
			assert.Equal(t, pinSection(tc.lessons), pinSection(string(p.After)), "the pin list keeps its bytes")

			predicate, err := lessoncheck.Describe(tc.req.def())
			require.NoError(t, err)
			assert.Equal(t, predicate, p.Predicate)

			var entries []int
			for _, im := range p.Impact {
				entries = append(entries, im.Entry)
				assert.Equal(t, tc.req.ID, im.Check.ID)
				assert.Equal(t, reviews.CheckVersion(tc.req.def()), im.Check.Version, "the block names the new version")
				assert.True(t, im.PredicateChanged)
				assert.Equal(t, "predicate: "+predicate, strings.TrimSpace(lessoncheck.ImpactLines([]lessoncheck.Impact{im})[2]))
				assert.NotEqual(t, im.VersionBefore, im.VersionAfter, "the definition is a pin-version part")
			}

			assert.Equal(t, tc.impact, entries)

			if tc.lessons == "" {
				_, err := os.Stat(f.pinPath())
				require.ErrorIs(t, err, os.ErrNotExist, "a plan writes nothing")
			} else {
				assert.Equal(t, tc.lessons, f.lessons(), "a plan writes nothing")
			}

			res, err := CommitCheckDef(f.deps(), p)
			require.NoError(t, err)
			require.NoError(t, res.Err)
			assert.Equal(t, Written, res.Outcome)
			assert.Equal(t, tc.want, f.lessons())

			// The candidate passes the strict tier, and the reader reads the
			// pins as before: rule, mode, and check of each pin.
			if tc.lessons != "" {
				before, err := reviews.ParseConfig([]byte(tc.lessons))
				require.NoError(t, err)

				after, err := reviews.ParseConfig([]byte(f.lessons()))
				require.NoError(t, err)
				assert.Equal(t, before.Pin, after.Pin)
			}

			_, errs, err := lessoncheck.Parse([]byte(f.lessons()))
			require.NoError(t, err)
			assert.Empty(t, errs)
		})
	}

	t.Run("an equal definition changes nothing", func(t *testing.T) {
		f := checkDefFixture(t, checkWritersLessons)

		p := f.checkDefPlan(CheckDefRequest{ID: "no-todo", Kind: "no-added-pattern/1", Pattern: `TODO\(`})
		assert.True(t, p.NoOp)
		assert.False(t, p.Confirmable)
		assert.Nil(t, p.After)

		_, err := CommitCheckDef(f.deps(), p)
		require.ErrorIs(t, err, ErrNotConfirmable)
	})

	t.Run("the gate-off preview carries the block and writes nothing", func(t *testing.T) {
		f := checkDefFixture(t, checkWritersLessons)
		f.gate(false)

		p := f.checkDefPlan(todoFixme)
		assert.False(t, p.Confirmable)
		require.Len(t, p.Impact, 2)
		assert.Equal(t, []AuthorBlocker{{Kind: BlockGate, Text: textGateOff, Route: routeGateOff}}, p.Blockers)

		_, err := CommitCheckDef(f.deps(), p)
		require.ErrorIs(t, err, ErrWriteDisabled)
		assert.Equal(t, checkWritersLessons, f.lessons())
	})

	t.Run("a gate that turns off after the plan blocks the write", func(t *testing.T) {
		f := checkDefFixture(t, checkWritersLessons)

		p := f.checkDefPlan(todoFixme)
		require.True(t, p.Confirmable)
		f.gate(false)

		_, err := CommitCheckDef(f.deps(), p)
		require.ErrorIs(t, err, ErrWriteDisabled)
		assert.Equal(t, checkWritersLessons, f.lessons())
	})

	t.Run("a file that changed after the plan is stale", func(t *testing.T) {
		f := checkDefFixture(t, checkWritersLessons)

		p := f.checkDefPlan(todoFixme)
		require.True(t, p.Confirmable)

		edited := checkWritersLessons + "# a later edit\n"
		f.write(edited, 0o644)

		res, err := CommitCheckDef(f.deps(), p)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, NotWritten, res.Outcome)
		assert.Equal(t, edited, f.lessons())
	})

	t.Run("a comment inside the replaced entry refuses the splice", func(t *testing.T) {
		lessons := strings.Replace(checkWritersLessons, "    pattern: 'TODO\\('\n",
			"    # the pattern of the TODO check\n    pattern: 'TODO\\('\n", 1)
		f := checkDefFixture(t, lessons)

		p := f.checkDefPlan(todoFixme)
		assert.False(t, p.Confirmable)
		assert.Nil(t, p.After)
		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockFile, p.Blockers[0].Kind)
		assert.Equal(t, ".seamark/lessons.yaml cannot be changed here: a comment sits inside checks.no-todo (line 5)",
			p.Blockers[0].Text)
		assert.Equal(t, routeByHand, p.Blockers[0].Route)
	})

	t.Run("a foreign root blocks the plan and the commit", func(t *testing.T) {
		f := checkDefFixture(t, checkWritersLessons)
		d := f.deps()
		d.ForeignRoot = ErrStale

		p, err := PlanCheckDef(d, todoFixme)
		require.NoError(t, err)
		assert.False(t, p.Confirmable)
		assert.Equal(t, BlockForeignRoot, p.Blockers[0].Kind)

		_, err = CommitCheckDef(d, p)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, checkWritersLessons, f.lessons())
	})
}

// TestCheckDefinitionRefusesInvalidPattern proves the K1 writer rule of
// the definition editor: the candidate runs the strict check tier, and
// a definition that fails it is refused with the K1 text before any
// write. The review still shows the candidate and the impact on the
// bound pins. An invalid ID stops before the splice.
func TestCheckDefinitionRefusesInvalidPattern(t *testing.T) {
	refused := func(t *testing.T, lessons string, req CheckDefRequest) *CheckDefPlan {
		t.Helper()

		f := checkDefFixture(t, lessons)
		p := f.checkDefPlan(req)
		assert.False(t, p.Confirmable)

		_, err := CommitCheckDef(f.deps(), p)
		require.ErrorIs(t, err, ErrNotConfirmable)
		assert.Equal(t, lessons, f.lessons(), "nothing is written")

		return p
	}

	t.Run("a pattern that does not compile", func(t *testing.T) {
		p := refused(t, checkWritersLessons, CheckDefRequest{ID: "no-todo", Kind: "no-added-pattern/1", Pattern: "TODO("})

		assert.Equal(t, []AuthorBlocker{{
			Kind: BlockCheck,
			Text: "the candidate does not validate: checks.no-todo: pattern does not compile: error parsing regexp: " +
				"missing closing ): `TODO(` (and 0 more)",
			Route: routeCheckDef,
		}}, p.Blockers)
		assert.NotNil(t, p.After, "the review shows the candidate")
		assert.Contains(t, p.Predicate, "'TODO('", "the preview names the typed pattern")
		require.Len(t, p.Impact, 2, "the impact shows next to the refusal")
		assert.Equal(t, p.Predicate, p.Impact[0].Predicate)
	})

	t.Run("an unknown kind", func(t *testing.T) {
		p := refused(t, checkWritersLessons, CheckDefRequest{ID: "no-todo", Kind: "no-added-word/1", Pattern: "TODO"})

		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockCheck, p.Blockers[0].Kind)
		assert.Contains(t, p.Blockers[0].Text, `checks.no-todo: unknown checker kind "no-added-word/1"`)
	})

	t.Run("a pattern on a path check", func(t *testing.T) {
		p := refused(t, checkWritersLessons, CheckDefRequest{ID: "frozen-assets", Kind: "forbid-path-change/1", Pattern: "x"})

		require.Len(t, p.Blockers, 1)
		assert.Contains(t, p.Blockers[0].Text, "checks.frozen-assets: forbid-path-change/1 takes no pattern")
	})

	t.Run("an invalid ID stops before the splice", func(t *testing.T) {
		p := refused(t, checkWritersLessons, CheckDefRequest{ID: "No Todo", Kind: "no-added-pattern/1", Pattern: "x"})

		assert.Equal(t, []AuthorBlocker{{
			Kind: BlockCheck, Text: `checks: check id "No Todo" must match [a-z0-9][a-z0-9._-]{0,63}`, Route: routeCheckDef,
		}}, p.Blockers)
		assert.Nil(t, p.After)
	})

	t.Run("an error that the file holds needs a manual fix", func(t *testing.T) {
		lessons := strings.Replace(checkWritersLessons, "lesson_checks_version: 1\n", "lesson_checks_version: 2\n", 1)
		p := refused(t, lessons, todoFixme)

		require.Len(t, p.Blockers, 1)
		assert.Equal(t, BlockCheck, p.Blockers[0].Kind)
		assert.Contains(t, p.Blockers[0].Text, "lesson_checks_version 2 is not supported; this seamark reads up to 1")
		assert.Equal(t, routeByHand, p.Blockers[0].Route)
		assert.Contains(t, string(p.After), "lesson_checks_version: 2\n", "a newer version is never downgraded")
	})
}

// TestCheckDefCommitRefusesAHardLinkMadeAfterPreview proves that the
// commit repeats the hard-link refusal of the plan. The write replaces
// the file by rename, so a link that appears after the preview would
// split the shared file: the path gets the new bytes and the other name
// keeps the old ones. The refusal is stale and writes nothing (P4-17).
func TestCheckDefCommitRefusesAHardLinkMadeAfterPreview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the link count is read on unix only")
	}

	f := checkDefFixture(t, checkWritersLessons)

	p := f.checkDefPlan(todoFixme)
	require.True(t, p.Confirmable)

	alias := filepath.Join(t.TempDir(), "lessons-alias.yaml")
	require.NoError(t, os.Link(f.pinPath(), alias))

	res, err := CommitCheckDef(f.deps(), p)
	require.ErrorIs(t, err, ErrStale)
	assert.Contains(t, err.Error(), integration.HardLinkReason+"; "+routeByHand)
	assert.Equal(t, NotWritten, res.Outcome)
	assert.Equal(t, checkWritersLessons, f.lessons(), "the path keeps the reviewed bytes")

	aliasBytes, err := os.ReadFile(alias)
	require.NoError(t, err)
	assert.Equal(t, checkWritersLessons, string(aliasBytes), "the other name keeps the reviewed bytes")

	linked, err := integration.HardLinked(f.root, ".seamark/lessons.yaml")
	require.NoError(t, err)
	assert.True(t, linked, "the refusal leaves the shared file as it was")
}
