package reviews

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func mined() []model.Lesson {
	return []model.Lesson{
		{Region: "scripts", Symptom: "E702", Occurrences: 20, Reviewer: "coderabbit"},
		{Region: "scripts", Symptom: "F541", Occurrences: 3, Reviewer: "coderabbit"},
		{Region: "scripts", Symptom: "RUF001", Occurrences: 1, Reviewer: "coderabbit"},
		{Region: "alembic/versions", Symptom: "E501", Occurrences: 5, Reviewer: "coderabbit"},
	}
}

func symptoms(lessons []model.Lesson) []string {
	out := make([]string, len(lessons))
	for i, l := range lessons {
		out[i] = l.Symptom
	}

	return out
}

func TestDefaultThresholdFiltersOneOffs(t *testing.T) {
	cfg := &Config{Threshold: DefaultThreshold}

	got := cfg.Surface(mined(), "scripts")

	// RUF001 (×1) is below threshold; the rest survive.
	assert.ElementsMatch(t, []string{"E702", "F541", "E501"}, symptoms(got))
}

func TestMuteByRuleAndRegion(t *testing.T) {
	cfg := &Config{
		Threshold: 2,
		Mute: []MuteRule{
			{Rule: "F541"},               // everywhere
			{Region: "alembic/versions"}, // whole generated tree
		},
	}

	got := cfg.Surface(mined(), "scripts")

	assert.ElementsMatch(t, []string{"E702"}, symptoms(got),
		"F541 muted by rule, E501 muted by region, RUF001 below threshold")
}

func TestThresholdOverride(t *testing.T) {
	cfg := &Config{Threshold: 1} // surface even one-offs

	got := cfg.Surface(mined(), "scripts")
	assert.Contains(t, symptoms(got), "RUF001")
}

func TestPinAlwaysSurfacesForRegion(t *testing.T) {
	cfg := &Config{
		Threshold: 2,
		Pin: []PinRule{
			{Rule: "RUF001", Region: "scripts", Note: "keep it ASCII"},
		},
	}

	got := cfg.Surface(mined(), "scripts")

	require.NotEmpty(t, got)
	assert.Equal(t, "pinned", got[0].Reviewer, "pins rank first")
	assert.Contains(t, got[0].Symptom, "RUF001")
	assert.Contains(t, got[0].Symptom, "keep it ASCII", "the note travels into the symptom")
}

func TestSurfaceBudgetRanksBySpecificity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Pin = []PinRule{
		{Rule: "repo-wide", Region: "*", Note: "n"},
		{Rule: "on-the-file", Region: "a/b/x.go", Note: "n"},
		{Rule: "on-the-tree", Region: "a", Note: "n"},
		{Rule: "on-the-package", Region: "a/b", Note: "n"},
	}

	out, trimmed := cfg.SurfaceBudget(nil, "a/b/x.go", 2)

	require.Len(t, out, 2)
	assert.Equal(t, 2, trimmed)
	assert.Contains(t, out[0].Symptom, "on-the-file", "deepest region wins the budget")
	assert.Contains(t, out[1].Symptom, "on-the-package")

	// Unbudgeted (deliberate surfaces): everything, still ranked.
	out, trimmed = cfg.SurfaceBudget(nil, "a/b/x.go", 0)
	assert.Len(t, out, 4)
	assert.Zero(t, trimmed)

	// Equal specificity keeps the config file's order (user priority).
	cfg.Pin = []PinRule{
		{Rule: "first", Region: "a/b", Note: "n"},
		{Rule: "second", Region: "a/b", Note: "n"},
	}

	out, _ = cfg.SurfaceBudget(nil, "a/b/x.go", 1)
	require.Len(t, out, 1)
	assert.Contains(t, out[0].Symptom, "first")
}

func TestHookPinBudgetDefaultAndOverride(t *testing.T) {
	assert.Equal(t, DefaultPinBudget, DefaultConfig().HookPinBudget())

	cfg := DefaultConfig()
	cfg.PinBudget = 7
	assert.Equal(t, 7, cfg.HookPinBudget())
}

func TestPinScopedOutOfRegion(t *testing.T) {
	cfg := &Config{
		Threshold: 2,
		Pin:       []PinRule{{Rule: "RUF001", Region: "scripts"}},
	}

	// A pin for scripts must not appear when surfacing api/.
	got := cfg.Surface([]model.Lesson{
		{Region: "api", Symptom: "E501", Occurrences: 3},
	}, "api/x.py")

	assert.NotContains(t, symptoms(got), "RUF001")
}

func TestRepoWidePinAppliesEverywhere(t *testing.T) {
	cfg := &Config{
		Threshold: 2,
		Pin:       []PinRule{{Rule: "NOSECRETS", Region: "*", Note: "never commit secrets"}},
	}

	got := cfg.Surface(nil, "anywhere/at/all.py")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Symptom, "NOSECRETS")
}

func TestLoadConfigDefaultsWhenAbsent(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir()) // no .seamark/lessons.yaml
	require.NoError(t, err)
	assert.Equal(t, DefaultThreshold, cfg.Threshold)
	assert.Empty(t, cfg.Mute)
	assert.Empty(t, cfg.Pin)
}

func TestLoadConfigParsesFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(`
threshold: 3
mute:
  - rule: F541
  - region: alembic/versions
pin:
  - rule: RUF001
    region: scripts
    note: keep it ascii
`), 0o644))

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Threshold)
	require.Len(t, cfg.Mute, 2)
	require.Len(t, cfg.Pin, 1)
	assert.Equal(t, "scripts", cfg.Pin[0].Region)
}

func TestLoadConfigRejectsMalformed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("threshold: [not a number\n"), 0o644))

	_, err := LoadConfig(root)
	require.Error(t, err, "a typo'd config must fail loudly, not silently ignore mutes")
}
