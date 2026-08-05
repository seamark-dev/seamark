package distill

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// The fixtures mirror the measured proposal shapes from the analysis
// that motivated region sets (RFC-002 §1): every case names the real
// proposal whose citations it reproduces.
func TestCoverageRegions(t *testing.T) {
	cases := []struct {
		name  string
		cited []model.Finding
		want  []string
	}{
		{
			// p65 train-serve-parity: the fix's test out-churned it, and
			// commonDir(workers, tests) was "". Tests abstain; the
			// footprint points both events at workers.
			name: "test paths abstain (p65)",
			cited: []model.Finding{
				{ID: 1, Path: "workers/session_tracker.py",
					Paths: []string{"workers/session_tracker.py", "tests/test_session_tracker.py"}},
				{ID: 2, Path: "tests/test_session_tracker.py",
					Paths: []string{"tests/test_session_tracker.py", "workers/session_tracker.py"}},
			},
			want: []string{"workers"},
		},
		{
			// p3 validate-at-the-boundary at event grain: most events in
			// api, a solid block in db, one alembic outlier. Two regions
			// cover 90%; the outlier does not force `*`.
			name: "two regions cover the evidence (p3)",
			cited: []model.Finding{
				{ID: 1, PR: 1, Path: "api/routes.py"},
				{ID: 2, PR: 2, Path: "api/services.py"},
				{ID: 3, PR: 3, Path: "api/models.py"},
				{ID: 4, PR: 4, Path: "api/deps.py"},
				{ID: 5, PR: 5, Path: "api/routes.py"},
				{ID: 6, PR: 6, Path: "api/schemas.py"},
				{ID: 7, PR: 7, Path: "db/schema.py"},
				{ID: 8, PR: 8, Path: "db/session.py"},
				{ID: 9, PR: 9, Path: "db/engine.py"},
				{ID: 10, PR: 10, Path: "alembic/versions/0042_x.py"},
			},
			want: []string{"api", "db"},
		},
		{
			// p16 docs-code-drift: evidence lives in README, rfc/, and one
			// script. Most events abstain — no quorum, honestly repo-wide.
			name: "doc-heavy themes stay repo-wide (p16)",
			cited: []model.Finding{
				{ID: 1, Path: "README.md"},
				{ID: 2, Path: "rfc/plan.md"},
				{ID: 3, Path: "scripts/build.py"},
			},
			want: nil,
		},
		{
			// p30 ruff-ruf003: three events, three top-level trees —
			// three regions still beat `*` (each names a real home; a
			// star names none). Equal coverage ties break deeper-first,
			// so web/src leads.
			name: "one region per event still covers",
			cited: []model.Finding{
				{ID: 1, PR: 1, Path: "api/services.py"},
				{ID: 2, PR: 2, Path: "web/src/x.ts"},
				{ID: 3, PR: 3, Path: "scripts/y.py"},
			},
			want: []string{"web/src", "api", "scripts"},
		},
		{
			// Six comments on ONE pull request must not outvote two
			// independent events elsewhere: events vote, not findings.
			name: "events vote, findings do not",
			cited: []model.Finding{
				{ID: 1, PR: 7, Path: "api/a.py"},
				{ID: 2, PR: 7, Path: "api/b.py"},
				{ID: 3, PR: 7, Path: "api/c.py"},
				{ID: 4, PR: 7, Path: "api/d.py"},
				{ID: 5, PR: 7, Path: "api/e.py"},
				{ID: 6, PR: 7, Path: "api/f.py"},
				{ID: 7, PR: 8, Path: "db/schema.py"},
				{ID: 8, PR: 9, Path: "db/session.py"},
			},
			want: []string{"db", "api"},
		},
		{
			name: "single dir tightens to it",
			cited: []model.Finding{
				{ID: 1, Path: "scripts/a.py"},
				{ID: 2, Path: "scripts/b.py"},
			},
			want: []string{"scripts"},
		},
		{
			// Depth is capped: guidance at four directories deep is
			// spurious precision, and the next refactor orphans it.
			name: "depth capped at three",
			cited: []model.Finding{
				{ID: 1, Path: "v2/pkg/engine/resolve/loader.go"},
				{ID: 2, Path: "v2/pkg/engine/resolve/context.go"},
			},
			want: []string{"v2/pkg/engine"},
		},
		{
			// p13 select-fixture-by-key: every citation is a test file —
			// the lesson is ABOUT the tests (same rule as fixFiles: a
			// test-only fix is legitimately about the tests). Stripping
			// its region would spray a tests lesson over the whole repo.
			name: "test-only evidence keeps its test region",
			cited: []model.Finding{
				{ID: 1, Path: "tests/test_fixtures.py"},
				{ID: 2, Path: "tests/test_loader.py"},
			},
			want: []string{"tests"},
		},
		{
			// A root-level code file (main.go) has no directory to vote
			// for — but it IS code evidence, so the event's test file
			// must not inherit the vote and pull the region to tests.
			name: "root-level code blocks test promotion",
			cited: []model.Finding{
				{ID: 1, Path: "main.go",
					Paths: []string{"main.go", "tests/main_test.py"}},
			},
			want: nil,
		},
		{
			name:  "no citations, no regions",
			cited: nil,
			want:  nil,
		},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, CoverageRegions(c.cited), c.name)
	}
}

func TestCoverageRegionsIsOrderInsensitive(t *testing.T) {
	cited := []model.Finding{
		{ID: 3, PR: 3, Path: "api/models.py"},
		{ID: 1, PR: 1, Path: "db/schema.py"},
		{ID: 2, PR: 2, Path: "api/routes.py"},
		{ID: 4, PR: 4, Path: "api/deps.py"},
	}

	first := CoverageRegions(cited)

	reversed := []model.Finding{cited[3], cited[2], cited[1], cited[0]}
	assert.Equal(t, first, CoverageRegions(reversed),
		"citation order must not change the region set")
}

// TestRecomputeRegionsRealProposals is the live acceptance check: it
// reads a populated index ($SEAMARK_DISTILL_DB) and reports what
// coverageRegions would assign each stored proposal, next to what it
// carries. Genuinely read-only — OpenReadOnly applies no schema and no
// migrations, so a diagnostic run cannot upgrade the target database
// out from under an older seamark. Skipped without a database.
func TestRecomputeRegionsRealProposals(t *testing.T) {
	path := os.Getenv("SEAMARK_DISTILL_DB")
	if path == "" {
		t.Skip("set SEAMARK_DISTILL_DB to run against a real index")
	}

	st, err := store.OpenReadOnly(path)
	require.NoError(t, err, "SEAMARK_DISTILL_DB must name an existing, current-schema index")
	t.Cleanup(func() { _ = st.Close() })

	findings, err := st.AllFindings()
	require.NoError(t, err)

	byID := make(map[int64]model.Finding, len(findings))
	for _, f := range findings {
		byID[f.ID] = f
	}

	starsBefore, starsAfter, total := 0, 0, 0

	for _, status := range []string{model.ProposalApplied, model.ProposalProposed, model.ProposalSuperseded} {
		proposals, err := st.Proposals(status)
		require.NoError(t, err)

		for _, p := range proposals {
			var cited []model.Finding

			for _, id := range p.Members {
				if f, ok := byID[id]; ok {
					cited = append(cited, f)
				}
			}

			if len(cited) == 0 {
				continue
			}

			total++

			regions := CoverageRegions(cited)

			old := p.Region
			if old == "" {
				old = "*"

				starsBefore++
			}

			recomputed := strings.Join(regions, ", ")
			if recomputed == "" {
				recomputed = "*"

				starsAfter++
			}

			if old != recomputed {
				t.Logf("p%-4d %-44s %-12s → %s", p.ID, p.Rule, old, recomputed)
			}
		}
	}

	t.Logf("%d proposals: %d repo-wide before, %d after recompute", total, starsBefore, starsAfter)
}
