package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The checker impact in the retarget preview of Pins. The engine fills
// maintain.Plan.Impact, and the preview shows its lines before y with
// the renderer of the CLI.

// checkImpactWrapYAML is the wrap-sql-errors pin of the maintenance
// fixture as an enforce pin with a no-added-pattern/1 check. region is
// its region line, so a test can move the scope.
func checkImpactWrapYAML(rule, region, note string) string {
	return "lesson_checks_version: 1\nchecks:\n  no-raw-sql:\n    kind: no-added-pattern/1\n" +
		"    pattern: 'db\\.Exec\\('\npin:\n  - rule: " + rule + "\n    region: " + region + "\n    note: " + note +
		"\n    mode: enforce\n    check: no-raw-sql\n"
}

// checkImpactOf returns the impact of the first pin between two files,
// as the engine computes it (lessoncheck.ImpactOf).
func checkImpactOf(t *testing.T, before, after string) []lessoncheck.Impact {
	t.Helper()

	old, err := reviews.ParseConfig([]byte(before))
	require.NoError(t, err)

	next, err := reviews.ParseConfig([]byte(after))
	require.NoError(t, err)

	impacts, err := lessoncheck.ImpactOf(old, next, []int{1})
	require.NoError(t, err)
	require.Len(t, impacts, 1)

	return impacts
}

// assertImpactBeforeConfirm checks that every line of the engine block
// shows in text, and that the block comes before the confirm key.
func assertImpactBeforeConfirm(t *testing.T, text string, impacts []lessoncheck.Impact, confirm string) {
	t.Helper()

	head := strings.Index(text, "CHECK IMPACT")
	require.GreaterOrEqual(t, head, 0, "the preview shows the impact block")

	for _, line := range lessoncheck.ImpactLines(impacts) {
		assert.Contains(t, text, lesFlat(line), "the preview shows the engine line")
	}

	assert.Less(t, head, strings.LastIndex(text, confirm), "the block shows before the confirm key")
}

func TestMaintRetargetPreviewShowsCheckImpact(t *testing.T) {
	fx := maintFixture(t)
	note := "Wrap SQL errors with the query name."

	t.Run("an enforce pin shows its block before y", func(t *testing.T) {
		p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		p.Impact = checkImpactOf(t, checkImpactWrapYAML("wrap-sql-errors", "internal/store", note),
			checkImpactWrapYAML("wrap-sql-errors", "internal/store/sql", note))

		f := maintFakeOf(fx, p, nil)
		h := maintOpen(t, f, p.Request, 120, 40)

		text := maintContent(t, h)
		assertImpactBeforeConfirm(t, text, p.Impact, "y retarget 1 pin")
		assert.Contains(t, text, "CHECK IMPACT what the checkers do after the apply")
		assert.Contains(t, text, "scope internal/store -> internal/store/sql")
		assert.Contains(t, text, "active: requested; inactive: not-installed")

		// The block names the selected pin: it follows the item and comes
		// before the next item or the history note.
		assert.Less(t, strings.Index(text, "wrap-sql-errors ✓ ready"), strings.Index(text, "CHECK IMPACT"))
		assert.Less(t, strings.Index(text, "CHECK IMPACT"), strings.Index(text, "Delivery history restarts"))

		assert.True(t, maintHasKey(h, "y"), "y stays the confirm key")
		assert.Equal(t, []string{"PlanMaintenance:retarget"}, f.called(), "the preview commits nothing")
	})

	t.Run("an advisory pin keeps the preview of before", func(t *testing.T) {
		p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)

		assert.NotContains(t, maintContent(t, h), "CHECK IMPACT")
	})

	t.Run("a block that names no ready item still shows", func(t *testing.T) {
		p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		p.Impact = checkImpactOf(t, checkImpactWrapYAML("other-rule", "lib", note),
			checkImpactWrapYAML("other-rule", "lib/sub", note))

		h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)

		assertImpactBeforeConfirm(t, maintContent(t, h), p.Impact, "y retarget 1 pin")
	})
}
