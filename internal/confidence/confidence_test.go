package confidence

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestAssessTiers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	day := 24 * time.Hour

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api", "a.go"), []byte("live"), 0o644))

	live := func(id int64, pr int, source string, age time.Duration) model.Finding {
		return model.Finding{ID: id, PR: pr, Path: "api/a.go", Source: source,
			CreatedAt: now.Add(-age).Unix()}
	}

	findings := map[int64]model.Finding{
		1: live(1, 1, model.SourceReview, 20*day),
		2: live(2, 2, model.SourceFixConventional, 30*day),
		3: live(3, 3, model.SourceReview, 40*day),
		// Same PR as finding 1: one event, not two.
		4: live(4, 1, model.SourceFixConventional, 20*day),
		// Old evidence and a deleted file.
		5: {ID: 5, PR: 5, Path: "api/gone.go", Source: model.SourceReview,
			CreatedAt: now.Add(-600 * day).Unix()},
		// Fresh, corroborated — but every cited file is gone.
		6: {ID: 6, PR: 6, Path: "api/gone.go", Source: model.SourceReview,
			CreatedAt: now.Add(-20 * day).Unix()},
		7: {ID: 7, PR: 7, Path: "api/gone.go", Source: model.SourceFixConventional,
			CreatedAt: now.Add(-30 * day).Unix()},
		// Living files, corroborated — but past the decay horizon.
		8: live(8, 8, model.SourceReview, 600*day),
		9: live(9, 9, model.SourceFixConventional, 620*day),
	}

	cases := []struct {
		name    string
		p       model.Proposal
		want    Tier
		explain string
	}{
		{"recent multi-source recurrence",
			model.Proposal{Members: []int64{1, 2, 3}}, TierStrong,
			"3 events, review+fix, recent, all paths alive"},
		{"single event cannot be strong",
			model.Proposal{Members: []int64{1, 4}}, TierWeak,
			"a review comment and the fix on its PR are ONE event — the v1-era pin shape"},
		{"aged-out citations",
			model.Proposal{Members: []int64{900, 901}}, TierWeak,
			"nothing lives in the store anymore"},
		{"stale evidence",
			model.Proposal{Members: []int64{5, 3}}, TierFair,
			"two events but one cites deleted code — not strong, not dead"},
		{"all cited files deleted",
			model.Proposal{Members: []int64{6, 7}}, TierWeak,
			"two fresh corroborated events, but guidance entirely about deleted code"},
		{"decayed past the horizon",
			model.Proposal{Members: []int64{8, 9}}, TierWeak,
			"two live-path corroborated events, newest older than the 540d weak bound"},
	}

	for _, c := range cases {
		got, _ := Assess(c.p, findings, root, now)
		assert.Equal(t, c.want, got, "%s: %s", c.name, c.explain)
	}
}

func TestAssessFactsLine(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	findings := map[int64]model.Finding{
		1: {ID: 1, PR: 1, Path: "api/a.go", Source: model.SourceReview,
			CreatedAt: now.Add(-14 * 24 * time.Hour).Unix()},
		2: {ID: 2, PR: 2, Path: "api/b.go", Source: model.SourceFixConventional,
			CreatedAt: now.Add(-20 * 24 * time.Hour).Unix()},
	}

	_, facts := Assess(model.Proposal{Members: []int64{1, 2, 999}}, findings, "", now)

	line := facts.Line()
	assert.Contains(t, line, "2 event(s)")
	assert.Contains(t, line, "fix+review")
	assert.Contains(t, line, "newest 14d")
	assert.Contains(t, line, "2/3 citations living")

	_, none := Assess(model.Proposal{Members: []int64{999}}, findings, "", now)
	assert.Equal(t, "evidence aged out of the store", none.Line())
}

func TestUnknownTimestampsAreNotRecent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	// Three events, two sources, all paths alive — but no citation
	// carries a timestamp: recency is unknown, and unknown must not
	// qualify as fresh.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api", "a.go"), []byte("x"), 0o644))

	findings := map[int64]model.Finding{
		1: {ID: 1, PR: 1, Path: "api/a.go", Source: model.SourceReview},
		2: {ID: 2, PR: 2, Path: "api/a.go", Source: model.SourceFixConventional},
		3: {ID: 3, PR: 3, Path: "api/a.go", Source: model.SourceReview},
	}

	tier, facts := Assess(model.Proposal{Members: []int64{1, 2, 3}}, findings, root, now)
	assert.Equal(t, TierFair, tier, "unknown age caps at fair, never strong")
	assert.False(t, facts.AgeKnown)
	assert.Contains(t, facts.Line(), "age unknown")
	assert.NotContains(t, facts.Line(), "newest 0d")
}
