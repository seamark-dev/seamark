package settings

// Tests of the lesson-check keys in the settings editor of
// lessons.yaml.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// invalidChecksLessons is an invalid check configuration with one
// delivery key. A hand edit wrote it, and two errors are in it.
const invalidChecksLessons = `threshold: 3
lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todos
`

// validChecksLessons is the example check configuration with one
// delivery key.
const validChecksLessons = `threshold: 3
lesson_checks_version: 1
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
    mode: enforce
    check: frozen-assets
`

// TestSettingsSaveRefusesInvalidChecks proves that the settings editor
// runs the strict check tier on its candidate. It writes lessons.yaml
// without pinfile.Prepare, so verifyLessons refuses with the reason of
// pinfile.CandidateRefusal. The settings edit does not touch the check
// keys, so the error comes from an earlier hand edit. The refusal comes
// from the plan, so no save can write, and the file stays byte for
// byte. A valid config with checks still plans.
func TestSettingsSaveRefusesInvalidChecks(t *testing.T) {
	refusals := []struct {
		name, body string
		edits      []Edit
		reason     string
	}{
		{
			"a delivery edit over an invalid check definition", invalidChecksLessons,
			[]Edit{{Field: Threshold, Int: 5}},
			"settings: refusing to write .seamark/lessons.yaml: the candidate does not validate: " +
				"checks.no-todo: pattern does not compile: error parsing regexp: missing closing ): `TODO(` (and 1 more)",
		},
		{
			"a mute edit over an invalid check definition", invalidChecksLessons,
			[]Edit{add("F541", "api/")},
			"settings: refusing to write .seamark/lessons.yaml: the candidate does not validate: " +
				"checks.no-todo: pattern does not compile: error parsing regexp: missing closing ): `TODO(` (and 1 more)",
		},
		{
			"a delivery edit in a newer format version", "threshold: 3\nlesson_checks_version: 2\n",
			[]Edit{{Field: Threshold, Int: 5}},
			"settings: refusing to write .seamark/lessons.yaml: the candidate does not validate: " +
				"lesson_checks_version 2 is not supported; this seamark reads up to 1 (and 0 more)",
		},
	}

	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.body)
			before := tree(t, root)

			_, err := PlanPatch(inspect(t, root), integration.Builtin(), c.edits)
			require.Error(t, err)
			assert.Equal(t, c.reason, err.Error())

			assert.Equal(t, c.body, readLessons(t, root))
			assert.Equal(t, before, tree(t, root), "nothing else is written")
		})
	}

	t.Run("a valid config with checks still saves", func(t *testing.T) {
		root := withLessons(t, validChecksLessons)

		res := saveLessons(t, root, Edit{Field: Threshold, Int: 5})
		assert.Equal(t, Written, res.Outcome)
		assert.Equal(t, "threshold: 5\n"+validChecksLessons[len("threshold: 3\n"):], readLessons(t, root))
	})
}

// TestSettingsSaveKeepsChecks covers the settings editor. A delivery or
// mute edit changes only its own top-level key. The
// lesson_checks_version key, the checks map, and the pin text stay byte
// for byte, so every pin keeps its mode and its check. The saved file
// passes the strict check tier.
func TestSettingsSaveKeepsChecks(t *testing.T) {
	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"a delivery value set", []Edit{{Field: Threshold, Int: 5}},
			replaceOnce(t, validChecksLessons, "threshold: 3\n", "threshold: 5\n")},
		{"a delivery key reset", []Edit{{Field: Threshold, Reset: true}},
			replaceOnce(t, validChecksLessons, "threshold: 3\n", "")},
		{"an absent key goes at the end", []Edit{{Field: PinBudget, Int: 4}},
			validChecksLessons + "\npin_budget: 4\n"},
		{"a mode set", []Edit{{Field: HookDelivery, String: "once-per-context"}},
			validChecksLessons + "\nhook_delivery: once-per-context\n"},
		{"a mute added", []Edit{add("F541", "api/")},
			validChecksLessons + "\nmute:\n  - rule: F541\n    region: api/\n"},
	}

	// checkText is the part of the file that no settings edit may touch:
	// the version key, the checks map, and the pin list.
	checkText := validChecksLessons[len("threshold: 3\n"):]

	before, err := reviews.ParseConfig([]byte(validChecksLessons))
	require.NoError(t, err)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, validChecksLessons)

			res := saveLessons(t, root, c.edits...)
			assert.Equal(t, Written, res.Outcome)

			got := readLessons(t, root)
			assert.Equal(t, c.want, got)
			assert.Contains(t, got, checkText)

			after, err := reviews.ParseConfig([]byte(got))
			require.NoError(t, err)
			assert.Empty(t, lessoncheck.Validate(after))
			assert.Equal(t, before.LessonChecksVersion, after.LessonChecksVersion)
			assert.Equal(t, before.Checks, after.Checks)
			assert.Equal(t, before.Pin, after.Pin, "every pin keeps its mode and its check")
		})
	}
}
