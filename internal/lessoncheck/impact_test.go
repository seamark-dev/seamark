package lessoncheck

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// impactChecks is the checks map of the impact fixtures: the two
// definitions of referencePatternYAML, and frozen-copy, a second ID
// with the definition of frozen-assets.
const impactChecks = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  frozen-copy:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
`

// impactBefore holds an enforced pin, a checked pin, an advisory pin
// with a check, and a plain advisory pin.
const impactBefore = impactChecks + `pin:
  - rule: frozen-assets
    region: generated
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: armed-later
    region: docs
    note: A check that is not armed yet.
    check: no-todo
  - rule: plain
    region: web
    note: An advisory pin.
`

// rawConfig decodes text with the lenient reader, as the writers do.
func rawConfig(t *testing.T, text string) *reviews.Config {
	t.Helper()

	cfg, err := reviews.ParseConfig([]byte(text))
	require.NoError(t, err)

	return cfg
}

// replaced returns impactBefore with old replaced by repl once. It fails
// the test when old does not occur, so a fixture edit cannot pass as a
// no-op.
func replaced(t *testing.T, old, repl string) string {
	t.Helper()

	require.Contains(t, impactBefore, old, "the fixture anchor must occur")

	return strings.Replace(impactBefore, old, repl, 1)
}

// pinRefOf and versionOf give the expected header and version parts of
// the pin at entry n of cfg.
func pinRefOf(cfg *reviews.Config, n int) string {
	p := cfg.Pin[n-1]

	return cfg.ShortPinRef(reviews.NewPinKey(p.Rule, p.Region, p.Regions))
}

func versionOf(cfg *reviews.Config, n int) string {
	return cfg.PinVersion(cfg.Pin[n-1])[:12]
}

// TestLessonCheckImpact proves the checker impact block that a writer
// shows before the apply: its five lines, the rule that an advisory pin
// gets no block, the "unchanged" forms, the fixed active line, and the
// deterministic text of an invalid reference.
func TestLessonCheckImpact(t *testing.T) {
	before := rawConfig(t, impactBefore)
	forbid := "violation if any change touches a file in scope: an add, a content change, a deletion, " +
		"a file mode or type change, or either end of a move; content is not read"
	frozenVersion := reviews.CheckVersion(before.Checks["frozen-assets"])[:12]
	todoVersion := reviews.CheckVersion(before.Checks["no-todo"])[:12]

	t.Run("a scope change of an enforced pin", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    region: generated\n", "    region: generated\n    regions: [generated, assets]\n"))

		impacts, err := ImpactOf(before, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)

		im := impacts[0]
		assert.Equal(t, 1, im.Entry)
		assert.Equal(t, CheckRef{ID: "frozen-assets", Kind: KindForbidPathChange, Version: reviews.CheckVersion(before.Checks["frozen-assets"])}, im.Check)
		assert.Equal(t, ModeEnforce, im.Mode)
		assert.Equal(t, []string{"generated"}, im.ScopeBefore)
		assert.Equal(t, []string{"assets", "generated"}, im.ScopeAfter)
		assert.True(t, im.PredicateChanged)
		assert.Equal(t, before.PinVersion(before.Pin[0]), im.VersionBefore)
		assert.Equal(t, after.PinVersion(after.Pin[0]), im.VersionAfter)
		assert.NotEqual(t, im.VersionBefore, im.VersionAfter, "the regions are a part of the pin version")
		assert.Equal(t, ActiveRequested, im.Active)

		assert.Equal(t, []string{
			"pin " + pinRefOf(before, 1) + " frozen-assets: check frozen-assets (forbid-path-change/1, " +
				frozenVersion + "), mode enforce",
			"  scope generated -> assets, generated",
			"  predicate: " + forbid,
			"  version " + versionOf(before, 1) + " -> " + versionOf(after, 1),
			"  active: requested; inactive: not-installed",
		}, ImpactLines(impacts))
	})

	t.Run("a note-only change keeps the scope and the predicate", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    note: Do not add TODO markers.\n", "    note: Never add TODO markers.\n"))

		impacts, err := ImpactOf(before, after, []int{2})
		require.NoError(t, err)

		assert.Equal(t, []string{
			"pin " + pinRefOf(before, 2) + " no-todo: check no-todo (no-added-pattern/1, " + todoVersion + "), mode check",
			"  scope unchanged",
			"  predicate unchanged",
			"  version " + versionOf(before, 2) + " -> " + versionOf(after, 2),
			"  active: requested; inactive: not-installed",
		}, ImpactLines(impacts))
	})

	t.Run("an advisory pin gets no block", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    region: web\n", "    region: '*'\n"))

		impacts, err := ImpactOf(before, after, []int{4})
		require.NoError(t, err)
		assert.Empty(t, impacts)
		assert.Empty(t, ImpactLines(impacts), "no line, so an advisory surface keeps its bytes")

		// An advisory pin with a check is still advisory.
		after = rawConfig(t, replaced(t, "    region: docs\n", "    region: guide\n"))

		impacts, err = ImpactOf(before, after, []int{3})
		require.NoError(t, err)
		assert.Empty(t, impacts)
	})

	t.Run("a mode that arms a check", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    note: A check that is not armed yet.\n",
			"    note: A check that is not armed yet.\n    mode: check\n"))

		impacts, err := ImpactOf(before, after, []int{3})
		require.NoError(t, err)

		assert.Equal(t, []string{
			"pin " + pinRefOf(before, 3) + " armed-later: check no-todo (no-added-pattern/1, " + todoVersion + "), mode check",
			"  scope unchanged",
			"  predicate: violation if a line that a change adds in scope matches the Go regular expression " +
				"'TODO\\('; a new file adds every line; binary or unreadable content gives unknown",
			"  version " + versionOf(before, 3) + " -> " + versionOf(after, 3),
			"  active: requested; inactive: not-installed",
		}, ImpactLines(impacts))
	})

	t.Run("a mode that disarms a check", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    mode: enforce\n", ""))

		impacts, err := ImpactOf(before, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.Equal(t, ModeAdvisory, impacts[0].Mode)
		assert.True(t, impacts[0].PredicateChanged, "the checkers stop evaluating the pin")
		assert.Equal(t, ActiveAdvisory, impacts[0].Active)
		assert.Equal(t, "  active: not requested; mode advisory runs no check", ImpactLines(impacts)[4])

		// check -> enforce keeps the predicate; only the mode changes.
		after = rawConfig(t, replaced(t, "    mode: check\n", "    mode: enforce\n"))

		impacts, err = ImpactOf(before, after, []int{2})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.False(t, impacts[0].PredicateChanged)
		assert.Equal(t, ModeEnforce, impacts[0].Mode)
		assert.NotEqual(t, impacts[0].VersionBefore, impacts[0].VersionAfter, "the mode is a part of the version")
	})

	t.Run("a renamed check with one definition keeps the predicate", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    check: frozen-assets\n", "    check: frozen-copy\n"))

		impacts, err := ImpactOf(before, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.Equal(t, "frozen-copy", impacts[0].Check.ID, "the block names the check after the edit")
		assert.False(t, impacts[0].PredicateChanged)
		assert.Equal(t, impacts[0].VersionBefore, impacts[0].VersionAfter,
			"the pin version holds the definition, not the ID")
	})

	t.Run("a repository-wide scope", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    region: generated\n", "    region: '*'\n"))

		impacts, err := ImpactOf(before, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.Equal(t, []string{}, impacts[0].ScopeAfter, "an empty scope is non-nil")
		assert.Equal(t, "  scope generated -> repository-wide", ImpactLines(impacts)[1])
	})

	t.Run("an undefined check keeps the block deterministic", func(t *testing.T) {
		after := rawConfig(t, replaced(t, "    check: no-todo\n", "    check: no-todos\n"))

		impacts, err := ImpactOf(before, after, []int{2})
		require.NoError(t, err)
		require.Len(t, impacts, 1)

		lines := ImpactLines(impacts)
		assert.Equal(t, "pin "+pinRefOf(before, 2)+" no-todo: check no-todos (not defined), mode check", lines[0])
		assert.Equal(t, `  predicate: not available: check "no-todos" is not defined under checks`, lines[2])

		again, err := ImpactOf(before, after, []int{2})
		require.NoError(t, err)
		assert.Equal(t, impacts, again)

		// An executable pin without a check names none.
		after = rawConfig(t, replaced(t, "    check: frozen-assets\n", ""))

		impacts, err = ImpactOf(before, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.Equal(t, "frozen-assets", impacts[0].Check.ID, "the block names the check that the edit unbinds")
	})

	t.Run("several pins keep the order of entries", func(t *testing.T) {
		text := replaced(t, "    region: generated\n", "    region: assets\n")
		text = strings.Replace(text, "    region: src\n", "    region: lib\n", 1)
		text = strings.Replace(text, "    region: web\n", "    region: app\n", 1)
		after := rawConfig(t, text)

		impacts, err := ImpactOf(before, after, []int{2, 4, 1})
		require.NoError(t, err)
		require.Len(t, impacts, 2, "the advisory pin 4 gets no block")
		assert.Equal(t, 2, impacts[0].Entry)
		assert.Equal(t, 1, impacts[1].Entry)
		assert.Len(t, ImpactLines(impacts), 10, "five lines per block")
	})

	t.Run("the rule is sanitized and bounded", func(t *testing.T) {
		long := strings.Repeat("r", 100)
		text := strings.Replace(impactBefore, "  - rule: frozen-assets\n", "  - rule: \"bell\\a"+long+"\"\n", 1)
		cfg := rawConfig(t, text)
		after := rawConfig(t, strings.Replace(text, "    region: generated\n", "    region: assets\n", 1))

		impacts, err := ImpactOf(cfg, after, []int{1})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.NotContains(t, impacts[0].Rule, "\a")
		assert.LessOrEqual(t, len([]rune(impacts[0].Rule)), DefaultLimits().MaxRuleRunes)
	})

	t.Run("an entry outside a pin list is an error", func(t *testing.T) {
		_, err := ImpactOf(before, before, []int{5})
		require.Error(t, err)

		_, err = ImpactOf(before, before, []int{0})
		require.Error(t, err)

		impacts, err := ImpactOf(before, before, nil)
		require.NoError(t, err)
		assert.Empty(t, impacts)
	})
}
