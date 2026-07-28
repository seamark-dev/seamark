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

var applyFixture = []model.Proposal{
	{ID: 7, Rule: "pooled-state-reset", Region: "v2/pkg/engine/resolve",
		Note:    `Reset pooled fields in Free() and deep-copy them in clone(): "both, always".`,
		Members: []int64{1, 2, 3}, Agent: "claude/v1", Status: model.ProposalProposed},
}

func TestApplyInsertsUnderExistingPinPreservingHandwrittenContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	existing := `# My tuning notes — hard-won, do not lose.
threshold: 3

mute:
  - rule: E702        # scripts are what they are

pin:
  # Keep scripts ASCII — smart quotes from chat have bitten us.
  - rule: RUF001
    region: scripts
    note: "ASCII only"
`
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o644))

	require.NoError(t, ApplyPins(root, applyFixture))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)

	// Every hand-written byte survives: comments, mute, the old pin.
	assert.Contains(t, got, "hard-won, do not lose")
	assert.Contains(t, got, "smart quotes from chat have bitten us")
	assert.Contains(t, got, "rule: E702")
	assert.Contains(t, got, "rule: RUF001")
	assert.Contains(t, got, "threshold: 3")

	// The new pin is in, with provenance.
	assert.Contains(t, got, "pooled-state-reset")
	assert.Contains(t, got, "distilled by claude/v1 from 3 findings")

	// And the file loads through the REAL config path with everything
	// in effect — the whole point of the parse-before-write guard.
	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Threshold)
	require.Len(t, cfg.Pin, 2)
	assert.Equal(t, "pooled-state-reset", cfg.Pin[0].Rule, "inserted at the head of the list")
	assert.Contains(t, cfg.Pin[0].Note, `"both, always"`, "quotes in the note survive marshaling")
	assert.Equal(t, "RUF001", cfg.Pin[1].Rule)
}

func TestApplyCreatesPinSectionWhenAbsent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("threshold: 2\n"), 0o644))

	require.NoError(t, ApplyPins(root, applyFixture))

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)
	assert.Equal(t, "pooled-state-reset", cfg.Pin[0].Rule)
	assert.Equal(t, 2, cfg.Threshold)
}

func TestApplyCreatesFileWhenMissing(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, ApplyPins(root, applyFixture))

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)
}

func TestApplyRefusesFlowStylePin(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	original := "pin: []\n"
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	err := ApplyPins(root, applyFixture)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "by hand")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data), "a refused apply writes nothing")
}

func TestRepoWideRegionRendersAsStar(t *testing.T) {
	// A cross-tree proposal (region "") pins repo-wide; the entry must
	// say so explicitly and survive the YAML round trip (bare * is an
	// alias indicator — quoting is the marshaler's job).
	root := t.TempDir()
	require.NoError(t, ApplyPins(root, []model.Proposal{
		{ID: 1, Rule: "r", Region: "", Note: "n", Members: []int64{1, 2}, Agent: "a/v1"},
	}))

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)
	assert.Equal(t, "*", cfg.Pin[0].Region)
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
