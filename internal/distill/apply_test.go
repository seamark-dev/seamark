package distill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// renderFixture holds the three row shapes of distillation: one region,
// a region set, and a repository-wide row. The first note holds quotes
// and a colon, which a hand-quoted render would break.
var renderFixture = []model.Proposal{
	{ID: 7, Rule: "pooled-state-reset", Region: "v2/pkg/engine/resolve",
		Note:    `Reset pooled fields in Free() and deep-copy them in clone(): "both, always".`,
		Members: []int64{1, 2, 3}, Agent: "claude/v1", Status: model.ProposalProposed},
	{ID: 9, Rule: "validate-at-the-boundary", Region: "api", Regions: []string{"api", "db"},
		Note:    "Enforce closed sets and non-null contracts at the edge.",
		Members: []int64{1, 2}, Agent: "claude/v3", Status: model.ProposalProposed},
	{ID: 1, Rule: "r", Note: "n", Members: []int64{1, 2}, Agent: "a/v1", Status: model.ProposalProposed},
}

func TestRenderPinsWritesTheBlockThatAnApplyAdds(t *testing.T) {
	// The block goes under `pin:` as it is: pinfile.PlanAppend only moves
	// its lines to the indentation of the list. So these bytes are the
	// bytes of an apply on a two-space list, and the CLI apply goldens
	// depend on them.
	block, err := RenderPins(renderFixture)
	require.NoError(t, err)

	assert.Equal(t, `  # distilled by claude/v1 from 3 findings (seamark lessons --distill, p7)
  - rule: pooled-state-reset
    region: v2/pkg/engine/resolve
    note: 'Reset pooled fields in Free() and deep-copy them in clone(): "both, always".'
  # distilled by claude/v3 from 2 findings (seamark lessons --distill, p9)
  - rule: validate-at-the-boundary
    region: api
    regions: [api, db]
    note: Enforce closed sets and non-null contracts at the edge.
  # distilled by a/v1 from 2 findings (seamark lessons --distill, p1)
  - rule: r
    region: '*'
    note: "n"
`, block)
}

func TestRenderPinsReadsBackAsTheRowsKeys(t *testing.T) {
	// The block loads through the real lessons reader. Each pin has the
	// key of its row, so every reader links the pin to the row.
	block, err := RenderPins(renderFixture)
	require.NoError(t, err)

	cfg, err := reviews.ParseConfig([]byte("pin:\n" + block))
	require.NoError(t, err)
	require.Len(t, cfg.Pin, len(renderFixture))

	for i, p := range renderFixture {
		pin := cfg.Pin[i]

		assert.Equal(t, reviews.ProposalKey(p), reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions), "p%d", p.ID)
		assert.Equal(t, p.Note, pin.Note, "quotes and colons in the note survive the marshaling")
	}

	// A region set renders both keys: region holds the first region for
	// readers that predate sets, and regions holds the whole set.
	assert.Equal(t, "api", cfg.Pin[1].Region)
	assert.Equal(t, []string{"api", "db"}, cfg.Pin[1].AllRegions())

	// A repository-wide row says so with an explicit "*". The bare star
	// is a YAML alias indicator, so the marshaler quotes it.
	assert.Equal(t, "*", cfg.Pin[2].Region)
	assert.Empty(t, cfg.Pin[2].Regions)
}

func TestDistillConfigGate(t *testing.T) {
	// Absent: write off — apply prints instead of editing.
	cfg, err := LoadConfig(t.TempDir())
	require.NoError(t, err)
	assert.False(t, cfg.Distill.Write)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("index:\n  generated: true\ndistill:\n  write: true\n"), 0o644))

	cfg, err = LoadConfig(root)
	require.NoError(t, err)
	assert.True(t, cfg.Distill.Write, "reads its own section of the shared file")
}
