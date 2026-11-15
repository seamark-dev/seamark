package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// call is one CALLS edge between two symbols named by FQN.
type call struct{ caller, callee, origin string }

// buildGraph stores syms and the calls between them in one rebuild and
// returns the ID of each symbol by FQN. Each FQN names one symbol.
func buildGraph(t *testing.T, s *Store, syms []model.Symbol, calls []call) map[string]int64 {
	t.Helper()

	ids := map[string]int64{}

	require.NoError(t, s.Rebuild(func(tx *Tx) error {
		for i := range syms {
			if err := tx.InsertSymbol(&syms[i]); err != nil {
				return err
			}

			ids[syms[i].FQN] = syms[i].ID
		}

		for _, c := range calls {
			edge := model.Edge{Src: ids[c.caller], Dst: ids[c.callee], Kind: model.EdgeCalls, Origin: c.origin}
			if err := tx.InsertEdge(edge); err != nil {
				return err
			}
		}

		return nil
	}))

	return ids
}

// fn returns a function symbol in file. A symbol without a file is a
// package node, as the indexer stores it.
func fn(fqn, file string) model.Symbol {
	kind := model.KindFunction
	if file == "" {
		kind = model.KindPackage
	}

	return model.Symbol{FQN: fqn, Name: fqn, Kind: kind, File: file, Span: model.Span{StartLine: 1, EndLine: 2}}
}

func TestCallerStatsSplitsCallers(t *testing.T) {
	s := openTestStore(t)

	syms := []model.Symbol{
		fn("store.Save", "store/store.go"),
		fn("store.loop", "store/store.go"),
		fn("store.a", "store/store.go"),
		fn("store.b", "store/store.go"),
		fn("store.idle", "store/store.go"),
		fn("api.Handle", "api/handler.go"),
		fn("cli.X", "cli/x.go"),
		fn("store.Tx.Commit", "store/store.go"),
		fn("store.TestSave", "store/store_test.go"),
		fn("e2e.Flow", "tests/e2e/flow.go"),
		fn("store", ""),
	}

	calls := []call{
		{"api.Handle", "store.Save", model.OriginQualified},
		{"cli.X", "store.Save", model.OriginUniqueName},
		{"store.Tx.Commit", "store.Save", model.OriginSamePackage},
		{"store.TestSave", "store.Save", model.OriginQualified},
		{"e2e.Flow", "store.Save", model.OriginQualified},
		{"store", "store.Save", model.OriginQualified},
		// The indexer gives no edge to a call of the caller itself. A
		// seeded self edge still must not count as a caller in another file.
		{"store.loop", "store.loop", model.OriginSamePackage},
		{"store.a", "store.b", model.OriginSamePackage},
		{"store.b", "store.a", model.OriginSamePackage},
	}

	ids := buildGraph(t, s, syms, calls)

	stats, err := s.CallerStats("store/store.go")
	require.NoError(t, err)

	assert.Equal(t, CallerStat{Total: 6, OtherFiles: 2, Tests: 2, NameMatch: 1}, stats[ids["store.Save"]])
	assert.Equal(t, CallerStat{Total: 1}, stats[ids["store.loop"]])
	assert.Equal(t, CallerStat{Total: 1}, stats[ids["store.a"]])
	assert.Equal(t, CallerStat{Total: 1}, stats[ids["store.b"]])

	_, found := stats[ids["store.idle"]]
	assert.False(t, found, "a symbol without callers has no entry")

	counts, err := s.CallerCounts("store/store.go")
	require.NoError(t, err)

	for id, st := range stats {
		callers, err := s.Callers(id)
		require.NoError(t, err)

		assert.Len(t, callers, st.Total)
		assert.Equal(t, counts[id], st.Total)
	}
}

func TestSQLFunctionsMatchTheModelRules(t *testing.T) {
	s := openTestStore(t)

	paths := []string{
		"", "a.go", "a_test.go", "pkg/a_test.go", "test_a.py", "a_test.py", "a.test.ts", "a.spec.ts",
		"x.spec.ts", "tests/a.go", "a/test/b.go", "src/__tests__/a.js", "contest.go", "testdata/a.go",
		"latest/a.go", "test/", "attest_a.py",
	}

	for _, p := range paths {
		var got int

		require.NoError(t, s.db.QueryRow(`SELECT `+sqlIsTest+`(?)`, p).Scan(&got))

		want := 0
		if model.IsTestPath(p) {
			want = 1
		}

		assert.Equal(t, want, got, "path %q", p)
	}

	origins := []string{
		"", model.OriginQualified, model.OriginSameClass, model.OriginSamePackage,
		model.OriginUniqueName, model.OriginParse, "resolved",
	}

	for _, o := range origins {
		var got int

		require.NoError(t, s.db.QueryRow(`SELECT `+sqlOriginRank+`(?)`, o).Scan(&got))
		assert.Equal(t, model.OriginConfidence(o), got, "origin %q", o)
	}

	// NULL is no path and no origin, so it never ranks as a match.
	var isTest, rank int

	require.NoError(t, s.db.QueryRow(`SELECT `+sqlIsTest+`(NULL), `+sqlOriginRank+`(NULL)`).Scan(&isTest, &rank))
	assert.Equal(t, 0, isTest)
	assert.Equal(t, 0, rank)
}

func TestCallerSampleOrdersAndLimits(t *testing.T) {
	syms := []model.Symbol{
		fn("pkg.Hot", "pkg/hot.go"),
		fn("pkg.Other", "pkg/hot.go"),
		fn("z.Q", "z/z.go"),
		fn("a.U", "a/a.go"),
		fn("b.Q", "b/b.go"),
		fn("pkg.self", "pkg/hot.go"),
		fn("pkg", ""),
		fn("pkg.TestHot", "pkg/hot_test.go"),
		fn("e2e.Run", "tests/e2e/run.go"),
		fn("c.Other", "c/c.go"),
	}

	calls := []call{
		{"z.Q", "pkg.Hot", model.OriginQualified},
		{"a.U", "pkg.Hot", model.OriginUniqueName},
		{"b.Q", "pkg.Hot", model.OriginQualified},
		{"pkg.self", "pkg.Hot", model.OriginSamePackage},
		{"pkg", "pkg.Hot", model.OriginSamePackage},
		{"pkg.TestHot", "pkg.Hot", model.OriginSamePackage},
		{"e2e.Run", "pkg.Hot", model.OriginQualified},
		{"c.Other", "pkg.Other", model.OriginQualified},
	}

	fqns := func(edges []CallEdge) []string {
		out := make([]string, 0, len(edges))
		for _, e := range edges {
			out = append(out, e.FQN)
		}

		return out
	}

	sample := func(syms []model.Symbol, calls []call, limit int) []string {
		s := openTestStore(t)
		ids := buildGraph(t, s, syms, calls)

		got, err := s.CallerSample("pkg/hot.go", []int64{ids["pkg.Hot"]}, limit)
		require.NoError(t, err)

		_, found := got[ids["pkg.Other"]]
		assert.False(t, found, "a symbol outside ids has no sample")

		return fqns(got[ids["pkg.Hot"]])
	}

	// Callers in other non-test files lead, and the qualified b.Q and z.Q sort
	// before the name match a.U. Same-file and package callers follow,
	// and test callers come last.
	all := []string{"b.Q", "z.Q", "a.U", "pkg", "pkg.self", "e2e.Run", "pkg.TestHot"}
	assert.Equal(t, all, sample(syms, calls, 10))
	assert.Equal(t, all[:3], sample(syms, calls, 3))

	// The insert order changes the IDs and the table order, not the sample.
	reversedSyms := make([]model.Symbol, 0, len(syms))
	for i := len(syms) - 1; i >= 0; i-- {
		reversedSyms = append(reversedSyms, syms[i])
	}

	reversedCalls := make([]call, 0, len(calls))
	for i := len(calls) - 1; i >= 0; i-- {
		reversedCalls = append(reversedCalls, calls[i])
	}

	assert.Equal(t, all, sample(reversedSyms, reversedCalls, 10))
	assert.Equal(t, all[:3], sample(reversedSyms, reversedCalls, 3))

	s := openTestStore(t)

	got, err := s.CallerSample("pkg/hot.go", nil, 3)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = s.CallerSample("pkg/hot.go", []int64{1}, 0)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// counts records the queries that a store runs and the rows it reads.
type counts struct{ queries, rows int }

// countingConnector opens connections of the sqlite driver and counts the
// work on them. It lives in the test, so production code has no hook.
type countingConnector struct {
	base driver.Driver
	dsn  string
	n    *counts
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}

	return countingConn{Conn: conn, n: c.n}, nil
}

func (c countingConnector) Driver() driver.Driver { return c.base }

type countingConn struct {
	driver.Conn
	n *counts
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.n.queries++

	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}

	return countingRows{Rows: rows, n: c.n}, nil
}

type countingRows struct {
	driver.Rows
	n *counts
}

func (r countingRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == nil {
		r.n.rows++
	}

	return err
}

// hotFile stores a file of 20 functions. The first one has hotOther
// callers in other files and hotTests test callers. The other 19 have 5
// callers each. It returns the database path and the symbol IDs in
// source order.
func hotFile(t *testing.T, hotOther, hotTests int) (path string, ids []int64) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)

	var (
		syms  []model.Symbol
		calls []call
	)

	for i := range 20 {
		callee := fmt.Sprintf("hot.f%02d", i)
		sym := fn(callee, "hot/hot.go")
		sym.Span = model.Span{StartLine: uint32(i + 1), EndLine: uint32(i + 1), EndCol: 1}
		syms = append(syms, sym)

		other, tests := 5, 0
		if i == 0 {
			other, tests = hotOther, hotTests
		}

		for j := range other + tests {
			caller := fmt.Sprintf("use.c%02d_%05d", i, j)
			file := "use/" + caller + ".go"

			if j >= other {
				file = "use/" + caller + "_test.go"
			}

			syms = append(syms, fn(caller, file))
			calls = append(calls, call{caller, callee, model.OriginQualified})
		}
	}

	byFQN := buildGraph(t, s, syms, calls)
	require.NoError(t, s.Close())

	for i := range 20 {
		ids = append(ids, byFQN[fmt.Sprintf("hot.f%02d", i)])
	}

	return path, ids
}

// openCounting opens the database at path read-only behind a counting
// connector.
func openCounting(t *testing.T, path string) (*Store, *counts) {
	t.Helper()

	probe, err := sql.Open("sqlite", "")
	require.NoError(t, err)

	base := probe.Driver()
	require.NoError(t, probe.Close())

	n := &counts{}
	db := sql.OpenDB(countingConnector{base: base, dsn: dsn(path, "mode=ro"), n: n})
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	t.Cleanup(func() { _ = s.Close() })

	return s, n
}

func TestCallerQueriesStayBoundedOnAHotSymbol(t *testing.T) {
	measure := func(hotOther, hotTests int) (statsWork, sampleWork counts, hot CallerStat) {
		path, ids := hotFile(t, hotOther, hotTests)
		s, n := openCounting(t, path)

		stats, err := s.CallerStats("hot/hot.go")
		require.NoError(t, err)
		require.Len(t, stats, 20)

		statsWork = *n
		*n = counts{}

		sample, err := s.CallerSample("hot/hot.go", ids, 3)
		require.NoError(t, err)
		require.Len(t, sample, 20)

		for _, id := range ids {
			require.Len(t, sample[id], 3)
		}

		return statsWork, *n, stats[ids[0]]
	}

	statsWork, sampleWork, hot := measure(5000, 1000)

	assert.Equal(t, CallerStat{Total: 6000, OtherFiles: 5000, Tests: 1000}, hot)
	assert.Equal(t, 1, statsWork.queries)
	assert.LessOrEqual(t, statsWork.rows, 20)
	assert.Equal(t, 1, sampleWork.queries)
	assert.LessOrEqual(t, sampleWork.rows, 60)

	smallStats, smallSample, _ := measure(5, 1)

	assert.Equal(t, smallStats, statsWork, "the count work does not grow with the fan-in")
	assert.Equal(t, smallSample, sampleWork, "the sample work does not grow with the fan-in")
}
