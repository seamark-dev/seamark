package store

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// seedRanking inserts symbols in the order the indexer does: the package
// grouping symbols first (no file, the lowest ids), then the symbols
// that have a definition. It returns the symbols by FQN.
func seedRanking(t *testing.T, s *Store) map[string]*model.Symbol {
	t.Helper()

	pkg := func(fqn, name string) *model.Symbol {
		return &model.Symbol{FQN: fqn, Name: name, Kind: model.KindPackage}
	}
	def := func(fqn, name, file string) *model.Symbol {
		return &model.Symbol{
			FQN: fqn, Name: name, Kind: model.KindFunction, File: file,
			Span: model.Span{StartLine: 1, EndLine: 2},
		}
	}

	syms := []*model.Symbol{
		pkg("api", "api"),
		pkg("fastapi", "fastapi"),
		pkg("fmt", "fmt"),
		pkg("tests/test_api", "test_api"),
		pkg("vendor/Close", "Close"),
		pkg("vendor/xconn.Close", "xconn.Close"),
		pkg("lib.conn.Close", "lib.conn.Close"),
		pkg("Close", "Close"),
		pkg("lib/Open", "Open"),
		def("pkg/a.Run", "Run", "pkg/a/a.go"),
		def("Run", "Run", "main.go"),
		def("web/client.api", "api", "web/client.ts"),
		def("tests/test_api.TestApi", "TestApi", "tests/test_api.py"),
		def("api.Serve", "Serve", "api/api.go"),
		def("dbconn.Close", "Close", "dbconn/c.go"),
		def("pool/conn.Close", "Close", "pool/conn/c.go"),
		def("x.conn.Close", "Close", "x/c.go"),
	}

	for i := 1; i <= 6; i++ {
		n := string(rune('0' + i))
		syms = append(syms,
			def("t"+n+".Close", "Close", "t"+n+"/t.go"),
			def("g"+n+".Get", "Get", "g"+n+"/g.go"))
	}

	for i := 1; i <= 4; i++ {
		n := string(rune('0' + i))
		syms = append(syms, def("o"+n+".Open", "Open", "o"+n+"/o.go"))
	}

	byFQN := map[string]*model.Symbol{}

	require.NoError(t, s.Rebuild(func(tx *Tx) error {
		for _, sym := range syms {
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}

			byFQN[sym.FQN] = sym
		}

		return nil
	}))

	return byFQN
}

// oldFindSymbols is FindSymbols as it was before the stages ordered
// their rows: the same filters and limits, in row-id order. The tests
// compare result sets with it.
func oldFindSymbols(t *testing.T, s *Store, query string, limit int) []string {
	t.Helper()

	seen := map[int64]bool{}
	var out []string

	add := func(syms []model.Symbol) {
		for _, sym := range syms {
			if !seen[sym.ID] && len(out) < limit {
				seen[sym.ID] = true
				out = append(out, sym.FQN)
			}
		}
	}

	exact, err := s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol WHERE fqn = ? OR name = ? LIMIT ?`, query, query, limit)
	require.NoError(t, err)
	add(exact)

	suffix, err := s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol WHERE fqn LIKE ? ESCAPE '\' LIMIT ?`, "%"+escapeLike(query), limit)
	require.NoError(t, err)
	add(suffix)

	if match := ftsQuery(query); match != "" && len(out) < limit {
		fts, err := s.querySymbols(
			`SELECT `+symbolCols+` FROM symbol
			 JOIN (SELECT rowid, rank FROM symbol_fts
			       WHERE symbol_fts MATCH ? ORDER BY rank LIMIT ?) f
			   ON f.rowid = symbol.id
			 ORDER BY f.rank`, match, limit-len(out))
		require.NoError(t, err)
		add(fts)
	}

	return out
}

func fqns(syms []model.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.FQN
	}

	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)

	return out
}

// TestFindSymbolsRanksDefinitionsAndBoundaries proves the order of the
// exact and suffix stages: a symbol with a definition before a package
// grouping symbol, an exact FQN before an exact name, a component
// boundary suffix before a raw suffix, then id. When same-named
// definitions fill the limit, the best exact-match package takes the
// last slot, for a limit of two or more. Where the limit does not cut,
// the set equals the set of the old unordered queries; where it cuts,
// every run returns the same rows.
func TestFindSymbolsRanksDefinitionsAndBoundaries(t *testing.T) {
	s := openTestStore(t)
	seedRanking(t, s)

	cases := []struct {
		name  string
		query string
		limit int
		want  []string
		cut   bool
	}{
		{
			name: "defined symbol before a same-named package", query: "api", limit: 10,
			want: []string{"web/client.api", "api", "tests/test_api.TestApi", "fastapi", "tests/test_api", "api.Serve"},
		},
		{
			name: "expand's two rows", query: "api", limit: 2, cut: true,
			want: []string{"web/client.api", "api"},
		},
		{
			name: "exact FQN before exact name", query: "Run", limit: 5,
			want: []string{"Run", "pkg/a.Run"},
		},
		{
			// Nine definitions fill the limit; the package Close, an FQN
			// match, takes the last slot before vendor/Close, a name match
			// with a lower id.
			name: "more exact matches than the limit keep a package", query: "Close", limit: 5, cut: true,
			want: []string{"dbconn.Close", "pool/conn.Close", "x.conn.Close", "t1.Close", "Close"},
		},
		{
			name: "expand's two rows keep a package", query: "Close", limit: 2, cut: true,
			want: []string{"dbconn.Close", "Close"},
		},
		{
			name: "a limit of one keeps the definition", query: "Close", limit: 1, cut: true,
			want: []string{"dbconn.Close"},
		},
		{
			name: "limit minus one definitions and a package", query: "Open", limit: 5,
			want: []string{"o1.Open", "o2.Open", "o3.Open", "o4.Open", "lib/Open"},
		},
		{
			name: "more definitions than the limit and no package", query: "Get", limit: 5, cut: true,
			want: []string{"g1.Get", "g2.Get", "g3.Get", "g4.Get", "g5.Get"},
		},
		{
			name: "only a package matches", query: "fmt", limit: 5,
			want: []string{"fmt"},
		},
		{
			name: "boundary suffix before raw suffix", query: "conn.Close", limit: 10,
			want: []string{"pool/conn.Close", "x.conn.Close", "lib.conn.Close", "dbconn.Close", "vendor/xconn.Close"},
		},
		{
			name: "single exact match", query: "Serve", limit: 5,
			want: []string{"api.Serve"},
		},
		{
			name: "the FQN of a defined symbol", query: "web/client.api", limit: 5,
			want: []string{"web/client.api"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.FindSymbols(tc.query, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, fqns(got))

			again, err := s.FindSymbols(tc.query, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, fqns(got), fqns(again), "the order is deterministic")

			all := oldFindSymbols(t, s, tc.query, 1000)
			if tc.cut {
				require.Greater(t, len(all), tc.limit, "the case must cut")
				assert.Subset(t, all, fqns(got))

				return
			}

			require.LessOrEqual(t, len(all), tc.limit, "the case must not cut")
			assert.Equal(t, sorted(oldFindSymbols(t, s, tc.query, tc.limit)), sorted(fqns(got)),
				"the set of matches is unchanged")
		})
	}
}

// TestExactSymbolsKeepsItsSet proves that the order changes nothing a
// caller counts: without a cut the set equals the unordered query, and
// a why route's two-row check still sees the same number of rows.
func TestExactSymbolsKeepsItsSet(t *testing.T) {
	s := openTestStore(t)
	seedRanking(t, s)

	for _, query := range []string{"api", "Run", "Close", "Open", "Get", "fmt", "Serve", "web/client.api", "conn.Close", "nothing"} {
		got, err := s.ExactSymbols(query, 1000)
		require.NoError(t, err)

		old, err := s.querySymbols(`SELECT `+symbolCols+` FROM symbol WHERE fqn = ? OR name = ?`, query, query)
		require.NoError(t, err)
		assert.Equal(t, sorted(fqns(old)), sorted(fqns(got)), "ExactSymbols(%q) set", query)

		two, err := s.ExactSymbols(query, 2)
		require.NoError(t, err)

		oldTwo, err := s.querySymbols(
			`SELECT `+symbolCols+` FROM symbol WHERE fqn = ? OR name = ? LIMIT 2`, query, query)
		require.NoError(t, err)
		assert.Len(t, two, len(oldTwo), "ExactSymbols(%q, 2) row count", query)
	}

	// FindSymbols reserves the package, ExactSymbols does not: its two
	// rows stay the two best definitions.
	two, err := s.ExactSymbols("Close", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"dbconn.Close", "pool/conn.Close"}, fqns(two))
}

// TestExactSymbolsStaysAnIndexedLookup proves that the ordered exact
// stage and the package reserve still read the fqn and name indexes
// instead of scanning the table; the sort runs over the matched rows
// only.
func TestExactSymbolsStaysAnIndexedLookup(t *testing.T) {
	s := openTestStore(t)
	seedRanking(t, s)

	for _, c := range []struct {
		sql  string
		args []any
	}{
		{exactSymbolsSQL, []any{"api", "api", "api", 5}},
		{exactPackageSQL, []any{"api", "api", string(model.KindPackage), "api"}},
	} {
		joined := queryPlan(t, s, c.sql, c.args...)
		assert.Contains(t, joined, "USING INDEX symbol_fqn (fqn=?)", joined)
		assert.Contains(t, joined, "USING INDEX symbol_name (name=?)", joined)
		assert.NotContains(t, joined, "SCAN symbol", joined)
	}
}

func queryPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()

	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var plan []string

	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}

	require.NoError(t, rows.Err())

	return strings.Join(plan, "\n")
}

// TestPackageInRepository proves the rule that tells a package of this
// repository from an import target: a package with members of its own,
// or with a symbol that has a file under its key and "/", belongs here.
// A file name with a dot ("next.config") does not make the import
// target "next" a package of this repository.
func TestPackageInRepository(t *testing.T) {
	s := openTestStore(t)

	pkg := func(fqn string) *model.Symbol {
		return &model.Symbol{FQN: fqn, Name: fqn, Kind: model.KindPackage}
	}
	def := func(fqn, file string) *model.Symbol {
		return &model.Symbol{FQN: fqn, Name: fqn, Kind: model.KindFunction, File: file}
	}

	gopkg, pypkg, next, fmtPkg, nested, sub := pkg("internal/mcp"), pkg("api"), pkg("next"), pkg("fmt"), pkg("golang.org/x"), pkg("golang.org/x/tools")
	member := def("internal/mcp.Serve", "internal/mcp/server.go")
	syms := []*model.Symbol{gopkg, pypkg, next, fmtPkg, nested, sub, member,
		def("api/client.get", "api/client.py"), def("next.config.plugins", "next.config.ts")}

	require.NoError(t, s.Rebuild(func(tx *Tx) error {
		for _, sym := range syms {
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}
		}

		return tx.InsertEdge(model.Edge{Src: gopkg.ID, Dst: member.ID, Kind: model.EdgeDefines, Origin: model.OriginParse})
	}))

	for _, tc := range []struct {
		pkg  *model.Symbol
		want bool
	}{
		{gopkg, true},   // a Go directory with a DEFINES edge
		{pypkg, true},   // a Python package with an empty __init__.py and a submodule
		{next, false},   // an import target beside the file next.config.ts
		{fmtPkg, false}, // an import target only
		{nested, false}, // an import target with only an import target under it
	} {
		got, err := s.PackageInRepository(*tc.pkg)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, tc.pkg.FQN)
	}
}
