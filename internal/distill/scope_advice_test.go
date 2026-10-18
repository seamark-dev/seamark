package distill

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// adviceFindings are evidence shapes with known coverage: two scripts
// findings cover [scripts], an api and a db finding cover [api, db], and
// four top-level trees are too scattered for any region.
var (
	adviceScripts   = []model.Finding{reviewFinding(1, "scripts/a.py"), reviewFinding(2, "scripts/b.py")}
	adviceTwoAreas  = []model.Finding{reviewFinding(3, "api/h.go"), reviewFinding(4, "db/q.go")}
	adviceScattered = []model.Finding{
		reviewFinding(5, "a/x.go"), reviewFinding(6, "b/x.go"),
		reviewFinding(7, "c/x.go"), reviewFinding(8, "d/x.go"),
	}
)

func TestRecommendScopeClassifiesTheComputedSet(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	cases := []struct {
		name    string
		p       model.Proposal
		living  []model.Finding
		want    ScopeAdvice
		comment string
	}{
		{
			name:   "no living evidence",
			p:      model.Proposal{Region: "scripts"},
			living: nil,
			want:   ScopeAdvice{Kind: ScopeNoEvidence},
		},
		{
			name:   "current",
			p:      model.Proposal{Region: "scripts"},
			living: adviceScripts,
			want:   ScopeAdvice{Kind: ScopeCurrent, Regions: []string{"scripts"}, Facts: []TriggerFact{}},
		},
		{
			name:    "current in another order",
			p:       model.Proposal{Region: "db", Regions: []string{"db", "api"}},
			living:  adviceTwoAreas,
			want:    ScopeAdvice{Kind: ScopeCurrent, Regions: []string{"api", "db"}, Facts: []TriggerFact{}},
			comment: "the pin key compares sets, not their order",
		},
		{
			name:    "current with a trailing slash",
			p:       model.Proposal{Region: "scripts/"},
			living:  adviceScripts,
			want:    ScopeAdvice{Kind: ScopeCurrent, Regions: []string{"scripts"}, Facts: []TriggerFact{}},
			comment: "the pin key trims a trailing slash",
		},
		{
			name:   "repository-wide to one region is narrower",
			p:      model.Proposal{},
			living: adviceScripts,
			want: ScopeAdvice{
				Kind: ScopeChanged, Regions: []string{"scripts"}, Facts: []TriggerFact{}, Narrower: true,
			},
		},
		{
			name:   "one region to two is broader",
			p:      model.Proposal{Region: "api"},
			living: adviceTwoAreas,
			want: ScopeAdvice{
				Kind: ScopeChanged, Regions: []string{"api", "db"}, Facts: []TriggerFact{}, Broader: true,
			},
		},
		{
			name:   "one region to repository-wide",
			p:      model.Proposal{Region: "a"},
			living: adviceScattered,
			want: ScopeAdvice{
				Kind: ScopeChanged, Facts: []TriggerFact{}, RepoWide: true, Broader: true,
			},
		},
		{
			name:   "repository-wide stays current",
			p:      model.Proposal{Regions: []string{"*"}},
			living: adviceScattered,
			want:   ScopeAdvice{Kind: ScopeCurrent, Facts: []TriggerFact{}, RepoWide: true},
		},
		{
			name:   "a parent region to its child is narrower only",
			p:      model.Proposal{Region: "api"},
			living: []model.Finding{reviewFinding(9, "api/v1/a.go"), reviewFinding(10, "api/v1/b.go")},
			want: ScopeAdvice{
				Kind: ScopeChanged, Regions: []string{"api/v1"}, Facts: []TriggerFact{}, Narrower: true,
			},
		},
		{
			name: "a confirmed trigger moves the scope sideways",
			p: model.Proposal{
				Region: "web/src/api", TriggerPaths: []string{"api/schemas.py"},
			},
			living: []model.Finding{reviewFinding(1, "web/src/api/schema.ts")},
			want: ScopeAdvice{
				Kind: ScopeChanged, Regions: []string{"api/schemas.py"},
				Facts: []TriggerFact{{
					Path: "api/schemas.py", Region: "api/schemas.py", Together: 38, Selected: true,
				}},
				Broader: true, Narrower: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RecommendScope(st, root, tc.p, tc.living)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, tc.comment)

			if tc.want.Kind == ScopeNoEvidence {
				return
			}

			// The advice is the shared engine, not a second inference.
			regions, facts, err := RecomputeRegions(st, root, tc.p, tc.living)
			require.NoError(t, err)
			assert.Equal(t, regions, got.Regions)
			assert.Equal(t, facts, got.Facts)
		})
	}
}

func TestRecommendScopeNeedsNoStoreWithoutEvidence(t *testing.T) {
	got, err := RecommendScope(nil, "", model.Proposal{Region: "api"}, nil)
	require.NoError(t, err, "no living evidence reads nothing")
	assert.Equal(t, ScopeAdvice{Kind: ScopeNoEvidence}, got)
}

func TestRecommendScopeRefusesAMissingStore(t *testing.T) {
	_, err := RecommendScope(nil, scopeRoot(t), model.Proposal{Region: "api"}, adviceScripts)
	require.ErrorIs(t, err, errScopeNoIndex)
}

func TestRecommendScopeReturnsStoreErrors(t *testing.T) {
	st := scopeStore(t)
	require.NoError(t, st.Close())

	// A stored trigger that is not directly cited needs the co-change
	// history, so a closed store fails the read.
	p := model.Proposal{Region: "scripts", TriggerPaths: []string{"lib/x.go"}}

	_, err := RecommendScope(st, scopeRoot(t, "lib/x.go"), p, adviceScripts)
	require.Error(t, err)
}

func TestReachesComparesDeliveredPaths(t *testing.T) {
	cases := []struct {
		next, cur []string
		want      bool
	}{
		{nil, nil, false},
		{[]string{"api"}, nil, false},
		{nil, []string{"api"}, true},
		{[]string{"api"}, []string{"api"}, false},
		{[]string{"api/v1"}, []string{"api"}, false},
		{[]string{"api"}, []string{"api/v1"}, true},
		{[]string{"api", "db"}, []string{"api"}, true},
		{[]string{"apiv2"}, []string{"api"}, true},
		{[]string{"api/"}, []string{"api"}, false},
		{[]string{"api/v1"}, []string{"api/"}, false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, reaches(tc.next, tc.cur), "reaches(%q, %q)", tc.next, tc.cur)
	}
}

func TestScopeKindStrings(t *testing.T) {
	assert.Equal(t, "cannot recompute: no living evidence", ScopeNoEvidence.String())
	assert.Equal(t, "regions already current", ScopeCurrent.String())
	assert.Equal(t, "a different scope is computed", ScopeChanged.String())
	assert.Equal(t, "ScopeKind(7)", ScopeKind(7).String())
	assert.Equal(t, ScopeNoEvidence, ScopeAdvice{}.Kind, "the zero advice is never a change")
}
