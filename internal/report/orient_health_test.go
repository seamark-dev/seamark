package report

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// healthNow is the fixed clock of the health header tests.
var healthNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// healthStore builds an index with the given CALLS edges per origin and
// one DEFINES and one IMPORTS edge, which the header must not count. The
// meta values set the index time, the recorded fingerprint, and the
// coverage summary; an empty value stays unset.
func healthStore(t *testing.T, calls map[string]int, meta map[string]string) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	var err error

	st, err = store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	total := 0
	for _, n := range calls {
		total += n
	}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		dst := model.Symbol{FQN: "pkg.Target", Name: "Target", Kind: model.KindFunction, File: "pkg/target.go"}
		if err := tx.InsertSymbol(&dst); err != nil {
			return err
		}

		// Each call edge needs its own caller, because the edge key is
		// the source, the target, and the kind.
		callers := make([]int64, 0, total+1)

		for i := range total + 1 {
			src := model.Symbol{FQN: fmt.Sprintf("pkg.C%d", i), Name: fmt.Sprintf("C%d", i),
				Kind: model.KindFunction, File: "pkg/callers.go"}
			if err := tx.InsertSymbol(&src); err != nil {
				return err
			}

			callers = append(callers, src.ID)
		}

		i := 0

		for origin, n := range calls {
			for range n {
				if err := tx.InsertEdge(model.Edge{Src: callers[i], Dst: dst.ID,
					Kind: model.EdgeCalls, Origin: origin}); err != nil {
					return err
				}

				i++
			}
		}

		for _, kind := range []model.EdgeKind{model.EdgeDefines, model.EdgeImports} {
			if err := tx.InsertEdge(model.Edge{Src: callers[total], Dst: dst.ID,
				Kind: kind, Origin: model.OriginParse}); err != nil {
				return err
			}
		}

		return nil
	}))

	for k, v := range meta {
		if v != "" {
			require.NoError(t, st.SetMeta(k, v))
		}
	}

	return st, root
}

// orientWith runs orient with the fixed clock and a fake fingerprint.
func orientWith(t *testing.T, st *store.Store, root, fingerprint string) string {
	t.Helper()

	var b strings.Builder
	require.NoError(t, orient(&b, st, root, orientFooterMCP, healthNow,
		func(string) string { return fingerprint }))

	return b.String()
}

// headerLines returns the three header lines. They follow the title and
// the index line, because this store holds no lessons.
func headerLines(t *testing.T, out string) []string {
	t.Helper()

	lines := strings.Split(out, "\n")
	require.GreaterOrEqual(t, len(lines), 5, out)
	require.True(t, strings.HasPrefix(lines[1], "  index  "), out)

	return lines[2:5]
}

func unixAgo(d time.Duration) string {
	return strconv.FormatInt(healthNow.Add(-d).Unix(), 10)
}

func TestOrientPrintsTheHealthHeader(t *testing.T) {
	st, root := healthStore(t,
		map[string]int{model.OriginQualified: 623, model.OriginSamePackage: 301, model.OriginUniqueName: 76},
		map[string]string{
			store.MetaIndexedAt:    unixAgo(3 * time.Hour),
			store.MetaIndexedState: "fp",
			store.MetaIndexSummary: `{"files_seen":340,"files_parsed":120,"files_skipped":12,"files_deleted":1,"parse_errors":0}`,
		})

	out := orientWith(t, st, root, "fp")

	header := "  health indexed 2026-11-15 09:00 UTC (3h ago) · freshness current\n" +
		"         parsed 120 of 340 seen files · 12 skipped by config · 208 with no language extractor · " +
		"1 tracked file deleted, not staged\n" +
		"         call edges by origin: 62.3% qualified · 30.1% same-package · 7.6% unique-name (1000 calls)\n"

	assert.Equal(t, strings.Split(strings.TrimSuffix(header, "\n"), "\n"), headerLines(t, out))
	assert.Contains(t, out, header+"\nmodules", "the header comes before the modules")
	assert.NotContains(t, out, "WARN", "no parse error, no warning")
}

func TestOrientHealthTimeAndAge(t *testing.T) {
	cases := map[string]string{
		unixAgo(5 * time.Minute): "  health indexed 2026-11-15 11:55 UTC (5m ago) · ",
		unixAgo(3 * time.Hour):   "  health indexed 2026-11-15 09:00 UTC (3h ago) · ",
		unixAgo(50 * time.Hour):  "  health indexed 2026-11-13 10:00 UTC (2d ago) · ",
		"":                       "  health indexed unknown (no completed index run) · ",
		"abc":                    "  health indexed unknown (no completed index run) · ",
		"0":                      "  health indexed unknown (no completed index run) · ",
		"-60":                    "  health indexed unknown (no completed index run) · ",
		unixAgo(-time.Hour):      "  health indexed 2026-11-15 13:00 UTC (age unknown) · ",
	}

	for indexedAt, want := range cases {
		st, root := healthStore(t, nil, map[string]string{store.MetaIndexedAt: indexedAt})

		line := headerLines(t, orientWith(t, st, root, ""))[0]
		assert.True(t, strings.HasPrefix(line, want), "%q: %s", indexedAt, line)
	}
}

// TestOrientHealthTimeIgnoresTheLocalZone proves that the index time
// prints in UTC under a local zone that is not UTC. CI machines often run
// in UTC, where a local time and a UTC time print the same text.
func TestOrientHealthTimeIgnoresTheLocalZone(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("UTC+05:30", 5*60*60+30*60)
	t.Cleanup(func() { time.Local = local })

	st, root := healthStore(t, nil, map[string]string{store.MetaIndexedAt: unixAgo(3 * time.Hour)})

	line := headerLines(t, orientWith(t, st, root, ""))[0]
	assert.True(t, strings.HasPrefix(line, "  health indexed 2026-11-15 09:00 UTC (3h ago) · "), line)
}

// TestOrientHealthHeaderFollowsTheLessonsLine proves the header order in
// a store with lessons: the title, the index line, the lessons line, and
// then the header. The lessons line belongs to the index summary.
func TestOrientHealthHeaderFollowsTheLessonsLine(t *testing.T) {
	st, root := healthStore(t, nil, nil)
	require.NoError(t, st.ReplaceLessons([]model.Lesson{{
		ClusterKey: "pkg\x00RUF001", Region: "pkg", Reviewer: "coderabbit",
		Symptom: "RUF001", Occurrences: 2, LastTS: 100,
	}}, nil))

	lines := strings.Split(orientWith(t, st, root, ""), "\n")
	require.GreaterOrEqual(t, len(lines), 6, lines)

	assert.True(t, strings.HasPrefix(lines[1], "  index  "), lines[1])
	assert.Equal(t, "         1 review lessons (run `seamark index --reviews` to refresh)", lines[2])
	assert.True(t, strings.HasPrefix(lines[3], "  health indexed "), lines[3])
	assert.True(t, strings.HasPrefix(lines[4], "         coverage "), lines[4])
	assert.True(t, strings.HasPrefix(lines[5], "         call edges by origin: "), lines[5])
}

func TestOrientHealthFreshnessWords(t *testing.T) {
	cases := []struct {
		recorded, current, want string
	}{
		{"", "fp", "freshness unknown (no git fingerprint)"},
		{"fp", "fp", "freshness current"},
		{"fp", "other", "freshness stale (the workspace changed since this index)"},
		{"fp", "", "freshness stale (the workspace changed since this index)"},
	}

	for _, c := range cases {
		st, root := healthStore(t, nil, map[string]string{store.MetaIndexedState: c.recorded})

		line := headerLines(t, orientWith(t, st, root, c.current))[0]
		assert.True(t, strings.HasSuffix(line, " · "+c.want), "%+v: %s", c, line)
		assert.NotContains(t, line, "seamark index", "the header offers no index route")
	}
}

func TestOrientHealthCoverageLine(t *testing.T) {
	const unknown = "         coverage unknown (no readable coverage summary) — `seamark status` has details"

	// A seen file that is not parsed, skipped, or a parse error has no
	// language extractor. The line names that count only when it is above
	// zero.
	cases := map[string]string{
		`{"files_seen":7,"files_parsed":7,"parse_errors":0}`:                   "         parsed 7 of 7 seen files",
		`{"files_seen":8,"files_parsed":7,"files_skipped":1,"parse_errors":0}`: "         parsed 7 of 8 seen files · 1 skipped by config",
		`{"files_seen":9,"files_parsed":7,"parse_errors":0}`:                   "         parsed 7 of 9 seen files · 2 with no language extractor",
		`{"files_seen":9,"files_parsed":7,"files_skipped":0,"parse_errors":0}`: "         parsed 7 of 9 seen files · 2 with no language extractor",
		`{"files_seen":30,"files_parsed":7,"files_skipped":12,"parse_errors":0}`: "         parsed 7 of 30 seen files · 12 skipped by config · " +
			"11 with no language extractor",
		`{"files_seen":9,"files_parsed":7,"files_deleted":1,"parse_errors":0}`: "         parsed 7 of 9 seen files · 2 with no language extractor · " +
			"1 tracked file deleted, not staged",
		`{"files_seen":9,"files_parsed":7,"files_deleted":3,"parse_errors":0}`: "         parsed 7 of 9 seen files · 2 with no language extractor · " +
			"3 tracked files deleted, not staged",
		`{"files_seen":9,"files_parsed":7,"files_skipped":1,"files_deleted":3,"parse_errors":0}`: "         parsed 7 of 9 seen files · " +
			"1 skipped by config · 1 with no language extractor · 3 tracked files deleted, not staged",
		// The parse error is left to the warning line and is not counted
		// as a file with no extractor.
		`{"files_seen":4,"files_parsed":2,"parse_errors":1}`: "         parsed 2 of 4 seen files · 1 with no language extractor",
	}

	for summary, want := range cases {
		st, root := healthStore(t, nil, map[string]string{store.MetaIndexSummary: summary})

		assert.Equal(t, want, headerLines(t, orientWith(t, st, root, ""))[1], summary)
	}

	// Unknown and malformed summaries never read as parsed coverage, and
	// a parse-error count inside them does not print a warning.
	for _, summary := range []string{
		"",
		"not json",
		"null",
		"{}",
		`{"files_seen":3,"files_parsed":5,"parse_errors":2}`,
		`{"files_seen":"3","files_parsed":1,"parse_errors":1}`,
		`{"files_seen":-1,"files_parsed":0,"parse_errors":0}`,
	} {
		st, root := healthStore(t, nil, map[string]string{store.MetaIndexSummary: summary})
		out := orientWith(t, st, root, "")

		assert.Equal(t, unknown, headerLines(t, out)[1], summary)
		assert.NotContains(t, out, "parsed ", summary)
		assert.NotContains(t, out, "WARN", summary)
	}
}

// TestOrientHealthKeepsTheParseWarning proves that known coverage with
// parse errors keeps the warning line after the header, and that the
// coverage line leaves the parse errors to it.
func TestOrientHealthKeepsTheParseWarning(t *testing.T) {
	st, root := healthStore(t, nil, map[string]string{
		store.MetaIndexSummary: `{"files_seen":3,"files_parsed":2,"parse_errors":1}`,
	})

	out := orientWith(t, st, root, "")

	assert.Equal(t, "         parsed 2 of 3 seen files", headerLines(t, out)[1])
	assert.Equal(t, "  WARN   1 file failed to parse and are invisible below — `seamark status` has details",
		strings.Split(out, "\n")[5])
}

func TestOrientHealthCountsOnlyCallEdges(t *testing.T) {
	st, root := healthStore(t, map[string]int{model.OriginSamePackage: 2}, nil)
	assert.Equal(t, "         call edges by origin: 100.0% same-package (2 calls)",
		headerLines(t, orientWith(t, st, root, ""))[2], "DEFINES and IMPORTS edges are not calls")

	// The store holds structural edges and no call edge: a valid index
	// with an empty call graph.
	st, root = healthStore(t, nil, nil)
	assert.Equal(t, "         call edges by origin: none indexed (0 calls)",
		headerLines(t, orientWith(t, st, root, ""))[2])
}

// TestOrientHealthFoldsManyOrigins proves the bound of the origin line
// for a database with more origins than the indexer writes.
func TestOrientHealthFoldsManyOrigins(t *testing.T) {
	st, root := healthStore(t, map[string]int{
		model.OriginQualified: 6, model.OriginSamePackage: 5, model.OriginUniqueName: 4,
		model.OriginSameClass: 3, "": 2, "x\x1b[31mred": 1, "y": 1,
	}, nil)

	line := headerLines(t, orientWith(t, st, root, ""))[2]
	assert.Equal(t, "         call edges by origin: 27.3% qualified · 22.7% same-package · 18.2% unique-name · "+
		"13.6% same-class · 9.1% (unknown) · 9.1% in 2 more origins (22 calls)", line)
}

// TestReportDoesNotImportTheStatusGatherer keeps orient off the full
// status gatherer, which loads the agent config, the policy, and every
// client inspection on each call.
func TestReportDoesNotImportTheStatusGatherer(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)

			assert.NotEqual(t, "github.com/seamark-dev/seamark/internal/status", p, name)
			assert.NotContains(t, p, "github.com/seamark-dev/seamark/internal/integration", name)
		}
	}
}
