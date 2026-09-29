package proposals

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestMatchesRegion(t *testing.T) {
	api := model.Proposal{Region: "api"}
	nested := model.Proposal{Region: "api/v1"}
	multi := model.Proposal{Region: "web", Regions: []string{"web", "api/v1"}}
	repoWide := model.Proposal{Region: "*"}

	tests := []struct {
		name   string
		p      model.Proposal
		filter string
		want   bool
	}{
		{"empty filter matches all", nested, "", true},
		{"blank filter matches all", nested, "  / ", true},
		{"repo-wide matches any filter", repoWide, "deep/path", true},
		{"exact region", api, "api", true},
		{"filter above the region", nested, "api", true},
		{"filter below the region", api, "api/v1/handlers", true},
		{"prefix without a boundary", api, "apix", false},
		{"region prefix without a boundary", model.Proposal{Region: "apix"}, "api", false},
		{"unrelated region", api, "web", false},
		{"slashes and spaces trimmed", nested, " /api/ ", true},
		{"any listed region", multi, "api", true},
		{"no listed region", multi, "docs", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchesRegion(tt.p, tt.filter))
		})
	}
}

func TestLoadOrdersByIDAndKeepsSourceErrors(t *testing.T) {
	f := newFixture(t, true)

	// Finding 1 survives in the index; finding 2 aged out.
	living := model.Finding{
		ID: 1, LessonKey: "api\x00reset", Path: "api/pool.go", PR: 7,
		Reviewer: "person", Body: "reset the pooled state", URL: "u1", CreatedAt: 80,
		Source: model.SourceReview,
	}
	require.NoError(t, f.st.ReplaceLessons(nil, []model.Finding{living}))

	p1 := f.propose("first", "One.", model.ProposalProposed, "api")
	p2 := f.propose("second", "Two.", model.ProposalProposed, "api")
	applied := f.propose("third", "Three.", model.ProposalApplied, "api")
	dismissed := f.propose("fourth", "Four.", model.ProposalDismissed, "api")
	p5 := f.propose("fifth", "Five.", model.ProposalProposed, "web")

	now := time.Unix(1700000000, 0)

	ls, err := Load(f.st, f.root, now)
	require.NoError(t, err)

	assert.Equal(t, []int64{p1.ID, p2.ID, p5.ID}, ids(ls.Pending), "ascending ID order")
	assert.Equal(t, []int64{applied.ID}, ids(ls.Applied))
	assert.Equal(t, []int64{dismissed.ID}, ids(ls.Dismissed))
	assert.Equal(t, now, ls.ObservedAt)

	require.NoError(t, ls.EvidenceErr)
	assert.Equal(t, map[int64]model.Finding{1: living}, ls.Evidence, "an aged-out finding is absent")

	require.NoError(t, ls.HealthErr)
	assert.Len(t, ls.Health, 4, "pending and applied rows carry advisories")
	assert.NotContains(t, ls.Health, dismissed.ID)
	assert.True(t, ls.Health[applied.ID].Pruned, "an applied row without its pin is named")

	require.NoError(t, ls.WriteErr)
	assert.True(t, ls.Write)

	// A malformed pin file fails the advisories only. The proposals and
	// the other sources still load.
	f.writeLessons("hook_delivery: sometimes\n")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".seamark", "config.yaml"),
		[]byte("distill: [\n"), 0o644))

	ls, err = Load(f.st, f.root, now)
	require.NoError(t, err)
	require.ErrorIs(t, ls.HealthErr, reviews.ErrLessonsConfig)
	assert.Nil(t, ls.Health)
	require.Error(t, ls.WriteErr)
	assert.False(t, ls.Write)
	assert.Len(t, ls.Pending, 3)
	assert.Len(t, ls.Evidence, 1)
}

func TestLoadFailsWhenProposalsCannotBeRead(t *testing.T) {
	f := newFixture(t, true)
	require.NoError(t, f.st.Close())

	_, err := Load(f.st, f.root, time.Now())
	require.Error(t, err)
}

func TestHealthIsTheCLIAdvisory(t *testing.T) {
	f := newFixture(t, true)

	// No proposals means no advisories and no config read: an empty
	// ledger must not fail on a malformed pin file.
	f.writeLessons("hook_delivery: sometimes\n")

	h, err := Health(f.st, f.root, nil)
	require.NoError(t, err)
	assert.Nil(t, h)

	_, err = Health(f.st, f.root, []model.Proposal{{ID: 1, Status: model.ProposalApplied}})
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)
	assert.Contains(t, err.Error(), "lessons proposalHealth: reading .seamark/lessons.yaml",
		"the CLI error text is unchanged")

	_, _, _, err = AppliedOutcomes(f.st, f.root, nil)
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)
	assert.Contains(t, err.Error(), "failed to load lessons config")
}

func TestRegionSetsEqualIgnoresOrder(t *testing.T) {
	assert.True(t, RegionSetsEqual([]string{"api", "web"}, []string{"web/", "api"}))
	assert.True(t, RegionSetsEqual(nil, []string{"*"}), "both are repo-wide")
	assert.False(t, RegionSetsEqual([]string{"api"}, []string{"api", "web"}))
}

// ids returns the proposal IDs in order.
func ids(ps []model.Proposal) []int64 {
	out := make([]int64, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}

	return out
}

func TestPackageDocNamesWhatTheCLIShares(t *testing.T) {
	// The package doc tells a reader which rules the two surfaces share.
	// A CLI change that calls another export must update the doc too, or
	// the doc claims a parity that the code does not have.
	sources, err := filepath.Glob(filepath.Join("..", "cli", "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, sources)

	ref := regexp.MustCompile(`\bproposals\.([A-Z]\w*)`)

	var used []string

	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}

		data, err := os.ReadFile(src)
		require.NoError(t, err)

		for _, m := range ref.FindAllStringSubmatch(string(data), -1) {
			used = append(used, m[1])
		}
	}

	slices.Sort(used)
	assert.Equal(t, []string{"AppliedOutcomes", "Health", "RegionSetsEqual"}, slices.Compact(used))

	file, err := parser.ParseFile(token.NewFileSet(), "proposals.go", nil, parser.PackageClauseOnly|parser.ParseComments)
	require.NoError(t, err)
	require.NotNil(t, file.Doc)

	doc := file.Doc.Text()
	assert.Contains(t, doc, "share only the evidence helpers: Health,\nAppliedOutcomes and RegionSetsEqual")
	assert.Contains(t, doc, "Only the console uses the ledger view")
	assert.Contains(t, doc, "keep their own path in internal/cli")
}
