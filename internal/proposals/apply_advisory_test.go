package proposals

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// checksLessons is a pin file with the example check definitions, an
// enforced pin, and a tuning value. An apply must keep all of it byte
// for byte.
const checksLessons = `threshold: 2
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

// TestApplyNewPinIsAdvisory covers the proposal apply writer.
// distill.RenderPins writes no mode and no check, so a pin that an
// apply adds is advisory. The apply adds the block under the pin key
// and keeps the checks map and the enforced pin. The candidate passes
// the strict check tier of pinfile.PrepareAppend.
func TestApplyNewPinIsAdvisory(t *testing.T) {
	f := newFixture(t, true)
	f.writeLessons(checksLessons)

	ids := []int64{
		f.propose("pooled-state-reset", "Reset pooled state on every reuse.", model.ProposalProposed, "api").ID,
		f.propose("wrap-errors", "Wrap errors with the operation name.", model.ProposalProposed, "web", "api").ID,
	}

	pv, err := PreviewApply(f.st, f.root, ids)
	require.NoError(t, err)
	require.True(t, pv.Write)

	// The block holds no mode key and no check key, so the previewed
	// text and the written text say the same.
	const block = `  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)
  - rule: pooled-state-reset
    region: api
    note: Reset pooled state on every reuse.
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p2)
  - rule: wrap-errors
    region: web
    regions: [web, api]
    note: Wrap errors with the operation name.
`
	assert.Equal(t, block, pv.YAML)

	res, err := Commit(f.st, f.root, pv)
	require.NoError(t, err)
	require.NoError(t, res.FileErr)
	require.NoError(t, res.LedgerErr)
	assert.True(t, res.FileWritten)
	assert.Equal(t, ids, res.Moved)

	got := string(f.lessons())
	require.Equal(t, 1, strings.Count(checksLessons, "pin:\n"))
	assert.Equal(t, strings.Replace(checksLessons, "pin:\n", "pin:\n"+block, 1), got,
		"the block goes under pin:, and the enforced pin follows it")

	cfg, errs, err := lessoncheck.Parse([]byte(got))
	require.NoError(t, err)
	require.Empty(t, errs)

	modes := map[string]string{}
	for _, p := range cfg.Pins {
		modes[p.Rule.Rule] = string(p.Mode) + " " + p.CheckID
	}

	assert.Equal(t, map[string]string{
		"pooled-state-reset": "advisory ",
		"wrap-errors":        "advisory ",
		"frozen-assets":      "enforce frozen-assets",
	}, modes)

	before, err := reviews.ParseConfig([]byte(checksLessons))
	require.NoError(t, err)
	assert.Equal(t, before.Checks, cfg.Raw.Checks, "the apply keeps the checks map")
	assert.Equal(t, before.LessonChecksVersion, cfg.Raw.LessonChecksVersion)
}
