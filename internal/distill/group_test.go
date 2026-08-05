package distill

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// pooledState reproduces the benchmark case this package exists for
// (wundergraph/graphql-go-tools, ten findings across eight files): the
// same bug shape in different words. Bodies carry the vocabulary real
// review comments carry — shared identifiers and the reset/pooled/Free
// lexicon — which is the trace the lexical grouper follows.
var pooledState = []model.Finding{
	{ID: 1, Path: "v2/pkg/engine/resolve/context.go",
		Body: "Clear actualListSizes in Context.Free to avoid stale retention when the Context is pooled and reused."},
	{ID: 2, Path: "execution/graphql/request.go",
		Body: "Restore the actualListSizes nil-reset path — without it pooled reuse leaks the previous request's state."},
	{ID: 3, Path: "v2/pkg/engine/resolve/resolvable.go",
		Body: "Reset forwarded extension state between resolves; the resolvable is pooled and the old extensions leak through."},
	{ID: 4, Path: "v2/pkg/engine/resolve/loader.go",
		Body: "Reset mutation cache population state on loader reuse, otherwise the pooled loader keeps stale cache flags."},
	{ID: 5, Path: "v2/pkg/engine/resolve/loader.go",
		Body: "Clear pooled arena references before reslicing, or the arena retains pointers after Free."},
	{ID: 6, Path: "v2/pkg/engine/resolve/resolve.go",
		Body: "Reset hash state before the raw-input fallback; reused state produces wrong hashes."},
	{ID: 7, Path: "v2/pkg/astvisitor/visitor.go",
		Body: "Pooled state can leak buffer size across walks — reset the visitor buffers when the walker is reused."},
	{ID: 8, Path: "v2/pkg/astnormalization/defer_internal.go",
		Body: "Reset defer visitor state in EnterDocument; pooled visitors carry the previous document's state."},
}

// unrelated is filler with disjoint vocabulary, so tests exercise the
// separation between theme and noise.
func unrelated(n int) []model.Finding {
	topics := []string{
		"Add a doc comment describing the exported symbol's contract.",
		"Wrap the returned error with operation context for the caller.",
		"The timeout constant duplicates the config default; import it.",
		"Missing nil check on the optional parameter dereference.",
		"Typo in the log message: 'recieved' should be 'received'.",
	}

	out := make([]model.Finding, n)
	for i := range out {
		out[i] = model.Finding{
			ID:   int64(100 + i),
			Path: fmt.Sprintf("pkg/mod%d/file%d.go", i%5, i),
			Body: topics[i%len(topics)] + fmt.Sprintf(" (site %d)", i),
		}
	}

	return out
}

func TestPooledStateFormsOneThemeGroup(t *testing.T) {
	findings := append(append([]model.Finding{}, pooledState...), unrelated(20)...)

	groups := NewLexicalGrouper().Group(findings)
	require.NotEmpty(t, groups)

	// The benchmark: all eight pooled-state findings land in ONE group,
	// despite eight files, two top-level trees, and zero shared wording
	// in several pairs (transitive token bridges connect them).
	top := groups[0]
	require.Len(t, top.Findings, len(pooledState),
		"the pooled-state theme must form a single candidate group")

	for i, f := range top.Findings {
		assert.Equal(t, pooledState[i].ID, f.ID)
	}

	assert.Equal(t, "", top.Region, "spans v2/ and execution/ — a repo-wide theme batch")
	assert.Len(t, top.Signature, 16)
}

func TestGroupingIsOrderInsensitive(t *testing.T) {
	findings := append(append([]model.Finding{}, pooledState...), unrelated(30)...)

	first := NewLexicalGrouper().Group(findings)

	shuffled := make([]model.Finding, len(findings))
	copy(shuffled, findings)
	rand.New(rand.NewSource(42)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	second := NewLexicalGrouper().Group(shuffled)

	require.Equal(t, len(first), len(second))

	for i := range first {
		assert.Equal(t, first[i].Signature, second[i].Signature,
			"group %d: same evidence must yield the same signature regardless of input order", i)
	}
}

func TestSignatureTracksMembership(t *testing.T) {
	g := NewLexicalGrouper()

	base := g.Group(pooledState)
	require.Len(t, base, 1)

	// A ninth finding joining the theme changes the signature — that is
	// the re-distill trigger.
	grown := append(append([]model.Finding{}, pooledState...), model.Finding{
		ID: 9, Path: "v2/pkg/engine/resolve/authorization.go",
		Body: "Reset the pooled authorization state in Free, stale state leaks across reuse.",
	})

	after := g.Group(grown)
	require.Len(t, after, 1)
	assert.NotEqual(t, base[0].Signature, after[0].Signature,
		"new evidence must produce a new signature")
	assert.Len(t, after[0].Findings, 9)
}

func TestUnthemedFindingsBucketByDirectoryAndDropSingletons(t *testing.T) {
	findings := []model.Finding{
		// Two disjoint-topic findings in one directory: an area group.
		{ID: 1, Path: "api/a.go", Body: "Missing nil check on the optional dereference parameter."},
		{ID: 2, Path: "api/b.go", Body: "Wrap the returned error with operation context for callers."},
		// A lone finding in its own directory: dropped (no recurrence).
		{ID: 3, Path: "web/c.go", Body: "Typo in the log message wording, fix the spelling."},
	}

	groups := NewLexicalGrouper().Group(findings)

	require.Len(t, groups, 1)
	assert.Equal(t, "api", groups[0].Region)
	assert.Len(t, groups[0].Findings, 2)
}

func TestUbiquitousTokensDoNotChainTheRepo(t *testing.T) {
	// 120 findings across 12 directories, every one sharing the tokens
	// "resolver" and "subgraph" (the repo's household words) plus a
	// per-directory topic. Without the document-frequency cut the whole
	// repo would collapse into one component.
	var findings []model.Finding

	for i := 0; i < 120; i++ {
		dir := i % 12
		findings = append(findings, model.Finding{
			ID:   int64(i + 1),
			Path: fmt.Sprintf("mod%d/f%d.go", dir, i),
			Body: fmt.Sprintf("The resolver subgraph topic%d needs attention in area%d.", dir, dir),
		})
	}

	groups := NewLexicalGrouper().Group(findings)

	for _, g := range groups {
		assert.LessOrEqual(t, len(g.Findings), 10,
			"household words must not chain directories together (group %s region %q)",
			g.Signature, g.Region)
	}
}

func TestOversizedGroupsRespectCap(t *testing.T) {
	// 100 findings in one directory sharing a strong theme: capped
	// slices, never one giant prompt.
	var findings []model.Finding

	for i := 0; i < 100; i++ {
		findings = append(findings, model.Finding{
			ID:   int64(i + 1),
			Path: "pkg/hot/hot.go",
			Body: fmt.Sprintf("Reset pooled buffer state before reuse at call site %d.", i),
		})
	}

	groups := NewLexicalGrouper().Group(findings)
	require.NotEmpty(t, groups)

	total := 0

	for _, g := range groups {
		assert.LessOrEqual(t, len(g.Findings), 40, "cap must hold")
		total += len(g.Findings)
	}

	assert.Equal(t, 100, total, "splitting must not lose findings")
}

func TestTrailingSingletonIsRebalancedNotLost(t *testing.T) {
	// 41 themed findings: naive slicing cuts 40+1, and the trailing
	// single-member chunk used to be dropped as "no recurrence" — a
	// finding with 40 companions silently lost to arithmetic. The
	// rebalance cuts 39+2 instead.
	var fs []model.Finding

	for i := 0; i < 41; i++ {
		fs = append(fs, model.Finding{
			ID:   int64(i + 1),
			Path: "pkg/hot/hot.go",
			Body: fmt.Sprintf("Reset pooled buffer state before reuse at site %d.", i),
		})
	}

	groups := NewLexicalGrouper().Group(fs)

	total := 0

	for _, g := range groups {
		assert.GreaterOrEqual(t, len(g.Findings), 2)
		assert.LessOrEqual(t, len(g.Findings), 40)
		total += len(g.Findings)
	}

	assert.Equal(t, 41, total, "a finding with company must never be lost to chunking")
}

func TestBoundedSplitIsStableUnderGrowth(t *testing.T) {
	// The signature economics stand on this: one new finding must
	// reopen one group, not re-bill the whole component the way
	// positional slicing did (boundaries shift, every slice churns).
	g := NewLexicalGrouper().(*lexicalGrouper)

	var fs []model.Finding

	for i := 0; i < 100; i++ {
		fs = append(fs, model.Finding{ID: int64(1000 + i*7), Path: "pkg/hot/hot.go"})
	}

	sigsOf := func(parts [][]model.Finding) map[string]bool {
		out := map[string]bool{}

		for _, part := range parts {
			out[makeGroup(part).Signature] = true
		}

		return out
	}

	before := g.bounded(fs)

	total := 0

	for _, part := range before {
		assert.GreaterOrEqual(t, len(part), 2, "no singleton escapes the merge")
		assert.LessOrEqual(t, len(part), g.maxGroup, "cap must hold")
		total += len(part)
	}

	assert.Equal(t, 100, total, "bucketing must not lose findings")

	// The newcomer's id sorts BEFORE every existing one: a regression
	// to positional slicing would shift every boundary and churn every
	// signature, so this catches it — a max-id append would not (it
	// only extends the last positional slice).
	grown := append([]model.Finding{{ID: 7, Path: "pkg/hot/hot.go"}}, fs...)
	beforeSigs, afterSigs := sigsOf(before), sigsOf(g.bounded(grown))

	fresh := 0

	for s := range afterSigs {
		if !beforeSigs[s] {
			fresh++
		}
	}

	assert.Equal(t, 1, fresh, "one new finding reopens exactly one group")
}

// TestGroupRealFindings is the live benchmark: it reads a populated
// index (testdb/index.db at the repo root, or $SEAMARK_DISTILL_DB) and
// reports how the grouper carves real findings. Skipped when no such
// database exists or it predates the finding table.
func TestGroupRealFindings(t *testing.T) {
	path := os.Getenv("SEAMARK_DISTILL_DB")
	if path == "" {
		path = filepath.Join("..", "..", "testdb", "index.db")
	}

	if _, err := os.Stat(path); err != nil {
		t.Skipf("no real index at %s", path)
	}

	st, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	findings, err := st.AllFindings()
	require.NoError(t, err)

	if len(findings) == 0 {
		t.Skip("index has no findings — re-mine with a current seamark")
	}

	groups := NewLexicalGrouper().Group(findings)
	require.NotEmpty(t, groups)

	covered := 0
	for _, g := range groups {
		covered += len(g.Findings)
	}

	t.Logf("%d findings → %d groups covering %d (largest %d, region %q)",
		len(findings), len(groups), covered,
		len(groups[0].Findings), groups[0].Region)

	for i, g := range groups[:min(8, len(groups))] {
		t.Logf("group %d: %d findings  region=%q  sig=%s", i, len(g.Findings), g.Region, g.Signature)
	}

	for _, g := range groups {
		assert.GreaterOrEqual(t, len(g.Findings), 2)
		assert.LessOrEqual(t, len(g.Findings), 40)
	}
}
