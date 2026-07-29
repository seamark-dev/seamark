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

func TestRemovePinsTakesEntryAndProvenanceOnly(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	original := `# My tuning notes — hard-won.
threshold: 3
pin_budget: 4

mute:
  - rule: E702

pin:
  # Keep scripts ASCII — smart quotes have bitten us.
  - rule: ascii-only
    region: scripts
    note: "ASCII only"
  # distilled by claude/v2 from 3 findings (seamark lessons --distill, p16)
  - rule: docs-code-drift
    region: "*"
    note: Update every doc that describes changed behavior.
  - rule: keep-me
    region: api
    note: A hand-written pin that must survive untouched.
`
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	removed, err := RemovePins(root, []PinKey{{Rule: "docs-code-drift", Region: "*"}})
	require.NoError(t, err)
	assert.Equal(t, []PinKey{{Rule: "docs-code-drift", Region: "*"}}, removed)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)

	assert.NotContains(t, got, "docs-code-drift")
	assert.NotContains(t, got, "seamark lessons --distill, p16", "its provenance comment goes too")

	// Everything else is byte-intact — including the neighbour above,
	// whose comment must not be swept up with the entry below it.
	assert.Contains(t, got, "# Keep scripts ASCII — smart quotes have bitten us.")
	assert.Contains(t, got, "hard-won")
	assert.Contains(t, got, "rule: E702")

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 2)
	assert.Equal(t, "ascii-only", cfg.Pin[0].Rule)
	assert.Equal(t, "keep-me", cfg.Pin[1].Rule)
	assert.Equal(t, "A hand-written pin that must survive untouched.", cfg.Pin[1].Note)
	assert.Equal(t, 3, cfg.Threshold)
	assert.Equal(t, 4, cfg.PinBudget)
	assert.Len(t, cfg.Mute, 1)
}

func TestRemovePinsLeavesMuteRulesAlone(t *testing.T) {
	// A mute entry has the very same `- rule: x` shape as a pin, so a
	// scan that ignores sections would delete the mute too — and the
	// verification would then refuse the whole prune.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"mute:\n  - rule: shared-name\n\npin:\n  - rule: shared-name\n    region: api\n    note: A pin.\n"),
		0o644))

	removed, err := RemovePins(root, []PinKey{{Rule: "shared-name", Region: "api"}})
	require.NoError(t, err)
	require.Len(t, removed, 1)

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	assert.Empty(t, cfg.Pin, "the pin is gone")
	require.Len(t, cfg.Mute, 1, "the mute rule of the same name survives")
	assert.Equal(t, "shared-name", cfg.Mute[0].Rule)
}

func TestRemovePinsDistinguishesRegions(t *testing.T) {
	// The same rule pinned in two areas is two pins; pruning one must
	// not take its namesake.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"pin:\n  - rule: dup\n    region: api\n    note: For api.\n"+
			"  - rule: dup\n    region: web\n    note: For web.\n"+
			"  - rule: dup\n    region: \"*\"\n    note: Repo-wide.\n"), 0o644))

	removed, err := RemovePins(root, []PinKey{{Rule: "dup", Region: "web"}})
	require.NoError(t, err)
	require.Equal(t, []PinKey{{Rule: "dup", Region: "web"}}, removed)

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 2)
	assert.Equal(t, "api", cfg.Pin[0].Region)
	assert.Equal(t, "*", cfg.Pin[1].Region)

	// An empty region means repo-wide, the form ApplyPins writes as "*".
	removed, err = RemovePins(root, []PinKey{{Rule: "dup", Region: ""}})
	require.NoError(t, err)
	require.Len(t, removed, 1)

	cfg, err = reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)
	assert.Equal(t, "api", cfg.Pin[0].Region, "only the repo-wide one went")
}

func TestRemovePinsHandlesAbsentAndMissingFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	// No file at all: nothing to prune, not an error.
	removed, err := RemovePins(root, []PinKey{{Rule: "anything"}})
	require.NoError(t, err)
	assert.Empty(t, removed)

	original := "pin:\n  - rule: kept\n    region: api\n    note: n\n"
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	// Already pruned by hand: still not an error, and nothing is written.
	removed, err = RemovePins(root, []PinKey{{Rule: "gone-already"}})
	require.NoError(t, err)
	assert.Empty(t, removed)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestRemovePinsRefusesUnparseableFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	broken := "pin: [\n  - rule: x\n"
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))

	_, err := RemovePins(root, []PinKey{{Rule: "x"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix it before pruning")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, broken, string(data), "a refused prune writes nothing")
}

func TestRemovePinsRoundTripsWithApply(t *testing.T) {
	// What apply writes, prune must remove exactly — leaving the file
	// as it was before the apply.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	before := "threshold: 2\n\npin:\n  - rule: original\n    region: api\n    note: Keep this one.\n"
	path := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(path, []byte(before), 0o644))

	require.NoError(t, ApplyPins(root, applyFixture))

	removed, err := RemovePins(root, []PinKey{{Rule: applyFixture[0].Rule, Region: applyFixture[0].Region}})
	require.NoError(t, err)
	require.Len(t, removed, 1)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, string(data), "prune undoes apply byte for byte")
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
