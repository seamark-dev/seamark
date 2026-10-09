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

// legacyMatchesRegion is a verbatim copy of MatchesRegion before it
// delegated to MatchesRegionSet. TestMatchesRegionKeepsItsResults
// compares the two, so the refactor provably changes no result.
func legacyMatchesRegion(p model.Proposal, filter string) bool {
	filter = strings.Trim(strings.TrimSpace(filter), "/")
	if filter == "" {
		return true
	}

	set := p.RegionSet()
	if len(set) == 0 {
		return true
	}

	for _, r := range set {
		r = strings.Trim(strings.TrimSpace(r), "/")

		if reviews.RegionMatches(filter, r) || reviews.RegionMatches(r, filter) {
			return true
		}
	}

	return false
}

func TestMatchesRegionKeepsItsResults(t *testing.T) {
	ps := []model.Proposal{
		{},
		{Region: "*"},
		{Region: "api"},
		{Region: "api/v1"},
		{Region: "api/"},
		{Region: "apix"},
		{Region: " "},
		{Region: "web", Regions: []string{"web", "api/v1"}},
		{Region: "x", Regions: []string{"a", "b"}},
		{Region: "api", Regions: []string{"*", "api"}},
		{Region: "api", Regions: []string{"", "api"}},
		{Regions: []string{" ", "docs"}},
	}

	filters := []string{"", " ", "/", "*", "api", "api/", "/api/", " api ", "api/v1",
		"api/v1/handlers", "apix", "web", "docs", "a", "deep/path"}

	for _, p := range ps {
		for _, filter := range filters {
			assert.Equal(t, legacyMatchesRegion(p, filter), MatchesRegion(p, filter),
				"region %q regions %q filter %q", p.Region, p.Regions, filter)
		}
	}
}

func TestMatchesRegionSet(t *testing.T) {
	// A pin with a "*" entry and a region is delivered to that region
	// only: AllRegions drops the "*". A repo-wide pin has no regions.
	starAndAPI := reviews.PinRule{Rule: "r", Region: "*", Regions: []string{"api"}}.AllRegions()
	repoWidePin := reviews.PinRule{Rule: "r", Region: "*"}.AllRegions()

	tests := []struct {
		name   string
		set    []string
		filter string
		want   bool
	}{
		{"empty filter matches all", []string{"api"}, "", true},
		{"blank filter matches all", []string{"api"}, " / ", true},
		{"empty set is repo-wide", nil, "deep/path", true},
		{"a repo-wide pin matches any filter", repoWidePin, "web", true},
		{"a star entry beside a region keeps the pin scoped", starAndAPI, "web", false},
		{"the scoped pin still matches its region", starAndAPI, "api/v1", true},
		{"exact region", []string{"api"}, "api", true},
		{"filter above the region", []string{"api/v1"}, "api", true},
		{"filter below the region", []string{"api"}, "api/v1/handlers", true},
		{"prefix without a boundary", []string{"api"}, "apix", false},
		{"region prefix without a boundary", []string{"apix"}, "api", false},
		{"trailing slash in the set", []string{"api/"}, "api", true},
		{"slashes and spaces in the filter", []string{"api/v1"}, " /api/ ", true},
		{"any listed region", []string{"web", "api/v1"}, "api", true},
		{"no listed region", []string{"web", "api/v1"}, "docs", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchesRegionSet(tt.set, tt.filter))
		})
	}
}

func TestMatchesText(t *testing.T) {
	const (
		rule = "Reset-Pooled-State"
		note = "Reset the pooled connection before reuse; see api/pool.go."
	)

	tests := []struct {
		name string
		q    string
		want bool
	}{
		{"empty query matches all", "", true},
		{"blank query matches all", "   ", true},
		{"rule ignores case", "pooled-state", true},
		{"note ignores case", "CONNECTION BEFORE", true},
		{"a path in the note", "api/pool.go", true},
		{"surrounding spaces are ignored", "  reuse  ", true},
		{"inner spaces count", "the  pooled", false},
		{"no match", "migration", false},
		{"no match across rule and note", "state reset", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchesText(rule, note, tt.q))
		})
	}

	assert.True(t, MatchesText("École-Rule", "", "école"), "non-ASCII letters ignore case too")
	assert.False(t, MatchesText("", "", "x"), "empty text matches only a blank query")
}

func TestLoadReadsSupersededHistory(t *testing.T) {
	f := newFixture(t, true)

	// Finding 3 is cited only by a superseded proposal. Its history view
	// still needs the evidence.
	cited := model.Finding{
		ID: 3, LessonKey: "api\x00retry", Path: "api/retry.go", PR: 9,
		Reviewer: "person", Body: "retry with a bound", URL: "u3", CreatedAt: 90,
		Source: model.SourceReview,
	}
	require.NoError(t, f.st.ReplaceLessons(nil, []model.Finding{cited}))

	// The ledger records no prune time, so the names show only the insert
	// order, and the insert order gives the IDs.
	applied := f.propose("kept", "Kept.", model.ProposalApplied, "api")
	first := f.propose("superseded-first", "One.", model.ProposalSuperseded, "api")
	second := f.propose("superseded-second", "Two.", model.ProposalSuperseded, "web")

	only := model.Proposal{
		Signature: "sig-only", Rule: "only-cites-three", Note: "Three.", Members: []int64{3},
		Status: model.ProposalSuperseded, CreatedAt: 1700000000,
	}
	require.NoError(t, f.st.InsertProposal(&only))

	ls, err := Load(f.st, f.root, time.Unix(1700000000, 0))
	require.NoError(t, err)

	assert.Equal(t, []int64{first.ID, second.ID, only.ID}, ids(ls.Superseded), "ascending ID order")
	assert.Equal(t, []int64{applied.ID}, ids(ls.Applied), "superseded rows are not applied")
	assert.Empty(t, ls.Pending)
	assert.Empty(t, ls.Dismissed)

	require.NoError(t, ls.HealthErr)
	assert.Len(t, ls.Health, 1, "only the applied row carries an advisory")
	assert.Contains(t, ls.Health, applied.ID)

	require.NoError(t, ls.EvidenceErr)
	assert.Equal(t, map[int64]model.Finding{3: cited}, ls.Evidence,
		"a superseded row's citation is loaded")
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

	_, err = appliedOutcomes(f.st, f.root, nil)
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)
	assert.Contains(t, err.Error(), "failed to load lessons config")
}

func TestMeasureOutcomesKeepsTheCLIErrorText(t *testing.T) {
	f := newFixture(t, true)

	// A closed store fails the findings read inside the measurement.
	require.NoError(t, f.st.Close())

	applied := []model.Proposal{{ID: 1, Rule: "r", Members: []int64{1}, Status: model.ProposalApplied}}

	_, err := MeasureOutcomes(f.st, reviews.DefaultConfig(), applied, nil)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "failed to gather applied proposal outcomes: "), err.Error())
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
	assert.Equal(t, []string{
		"Commit", "ErrNothingSelected", "Health", "Ledgerer", "Preview", "PreviewApply",
		"PreviewDismiss", "PreviewReconcile", "RegionSetsEqual", "Result",
	}, exportsUsedIn(t, "cli"))

	// `lessons --stats` and the console's Stats view measure outcomes
	// through internal/lessonstats, as the doc says.
	assert.Equal(t, []string{"MeasureOutcomes"}, exportsUsedIn(t, "lessonstats"))

	file, err := parser.ParseFile(token.NewFileSet(), "proposals.go", nil, parser.PackageClauseOnly|parser.ParseComments)
	require.NoError(t, err)
	require.NotNil(t, file.Doc)

	// The doc wraps lines, so the check joins them first.
	doc := strings.Join(strings.Fields(file.Doc.Text()), " ")
	assert.Contains(t, doc, "share the evidence helpers (Health and RegionSetsEqual)")
	assert.Contains(t, doc, "the decision flow (PreviewApply, PreviewDismiss, PreviewReconcile, Commit, "+
		"with their Ledgerer, Preview, Result and ErrNothingSelected)")
	assert.Contains(t, doc, "use MeasureOutcomes through internal/lessonstats")
	assert.Contains(t, doc, "Only the console uses the ledger view")
}

// exportsUsedIn returns the sorted, distinct exports of this package
// that the non-test sources of internal/<pkg> name.
func exportsUsedIn(t *testing.T, pkg string) []string {
	t.Helper()

	sources, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
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

	return slices.Compact(used)
}
