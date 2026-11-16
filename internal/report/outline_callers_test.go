package report

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// edgeAt is one CALLS edge between two symbols, named by their index in
// the symbol list. An index works where two symbols share an FQN.
type edgeAt struct {
	caller, callee int
	origin         string
}

// seedGraph builds an index that holds syms in the given order and the
// edges between them. It writes no source file.
func seedGraph(t *testing.T, syms []model.Symbol, edges []edgeAt) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for i := range syms {
			if err := tx.InsertSymbol(&syms[i]); err != nil {
				return err
			}
		}

		for _, e := range edges {
			edge := model.Edge{Src: syms[e.caller].ID, Dst: syms[e.callee].ID, Kind: model.EdgeCalls, Origin: e.origin}
			if err := tx.InsertEdge(edge); err != nil {
				return err
			}
		}

		return nil
	}))

	return st, root
}

// sym returns a symbol of kind in file that starts on line. A symbol
// without a file is a package node, as the indexer stores it.
func sym(fqn string, kind model.SymbolKind, file string, line uint32) model.Symbol {
	name := fqn
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		name = fqn[i+1:]
	}

	return model.Symbol{FQN: fqn, Name: name, Kind: kind, File: file, Span: model.Span{StartLine: line, EndLine: line + 1}}
}

// splitCallersFixture returns the symbols and edges of store/store.go:
// Save has callers of each kind, helper has only test callers, and
// Tx.Commit has none.
func splitCallersFixture() ([]model.Symbol, []edgeAt) {
	syms := []model.Symbol{
		sym("store.Save", model.KindFunction, "store/store.go", 10),
		sym("store.helper", model.KindFunction, "store/store.go", 30),
		sym("store.Tx.Commit", model.KindMethod, "store/store.go", 20),
		sym("api.Handle", model.KindFunction, "api/handler.go", 5),
		sym("cli.X", model.KindFunction, "cli/x.go", 7),
		sym("store.TestSave", model.KindFunction, "store/store_test.go", 9),
		sym("e2e.Flow", model.KindFunction, "tests/e2e/flow.go", 11),
		sym("store", model.KindPackage, "", 0),
		sym("store.TestHelper", model.KindFunction, "store/store_test.go", 40),
		sym("store.TestHelper2", model.KindFunction, "store/store_test.go", 50),
	}

	edges := []edgeAt{
		{3, 0, model.OriginQualified},
		{4, 0, model.OriginUniqueName},
		{2, 0, model.OriginSamePackage},
		{5, 0, model.OriginSamePackage},
		{6, 0, model.OriginQualified},
		{7, 0, model.OriginQualified},
		{5, 1, model.OriginSamePackage},
		{8, 1, model.OriginSamePackage},
		{9, 1, model.OriginSamePackage},
	}

	return syms, edges
}

// entryLines returns the lines of the outline entry of fqn: the entry
// line and the indented lines under it.
func entryLines(t *testing.T, section, fqn string) []string {
	t.Helper()

	lines := strings.Split(section, "\n")

	for i, l := range lines {
		fields := strings.Fields(l)
		if len(fields) < 2 || fields[1] != fqn || strings.HasPrefix(l, "           ") {
			continue
		}

		out := []string{l}

		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "           ") {
				break
			}

			out = append(out, next)
		}

		return out
	}

	require.Failf(t, "no entry", "the outline has no entry %s:\n%s", fqn, section)

	return nil
}

func TestWhyFileOutlineCallerCounts(t *testing.T) {
	syms, edges := splitCallersFixture()
	st, root := seedGraph(t, syms, edges)

	section := definesSection(t, whyFile(t, st, root, "store/store.go"))

	assert.Equal(t,
		"           callers  6 · 2 in other non-test files · 2 in test files · 1 resolved by name match only",
		entryLines(t, section, "store.Save")[1])
	assert.Equal(t,
		"           callers  3 · 0 in other non-test files · 3 in test files",
		entryLines(t, section, "store.helper")[1])
}

// noneIndexedNote is the closing line of an outline with a function that
// has no indexed caller.
const noneIndexedNote = "  none indexed: the index skips some calls, so no edge does not prove that nothing calls the symbol;" +
	" why on the symbol lists the skip rules"

func TestWhyFileOutlineZeroCallers(t *testing.T) {
	syms := []model.Symbol{
		sym("pkg.idle", model.KindFunction, "pkg/a.go", 1),
		sym("pkg.T.Idle", model.KindMethod, "pkg/a.go", 5),
		sym("pkg.limit", model.KindVar, "pkg/a.go", 9),
		sym("pkg.used", model.KindFunction, "pkg/b.go", 1),
		sym("main.main", model.KindFunction, "main.go", 1),
	}

	st, root := seedGraph(t, syms, []edgeAt{{4, 3, model.OriginQualified}})

	section := definesSection(t, whyFile(t, st, root, "pkg/a.go"))

	assert.Equal(t, []string{
		fmt.Sprintf("  %-8s %-44s %s", "function", "pkg.idle", "pkg/a.go:1-1"),
		"           callers  none indexed",
	}, entryLines(t, section, "pkg.idle"))
	assert.Equal(t, []string{
		fmt.Sprintf("  %-8s %-44s %s", "method", "pkg.T.Idle", "pkg/a.go:5-5"),
		"           callers  none indexed",
	}, entryLines(t, section, "pkg.T.Idle"))
	assert.Equal(t, []string{
		fmt.Sprintf("  %-8s %-44s %s", "var", "pkg.limit", "pkg/a.go:9-9"),
	}, entryLines(t, section, "pkg.limit"))

	assert.Equal(t, 1, strings.Count(section, noneIndexedNote))
	assert.True(t, strings.HasSuffix(section, "\n"+noneIndexedNote), "the note follows the entries:\n%s", section)

	called := definesSection(t, whyFile(t, st, root, "pkg/b.go"))
	assert.NotContains(t, called, "none indexed")
}

func TestWhyFileOutlineCallerDefinitions(t *testing.T) {
	syms, edges := splitCallersFixture()

	// A caller name can carry terminal control bytes from the source, and
	// a caller path can carry them or a secret from the repository. All three
	// call Tx.Commit.
	first := len(syms)
	syms = append(syms,
		sym("x.\x1b]0;x\x07Evil", model.KindFunction, "x/x.go", 3),
		sym("y.Pwn", model.KindFunction, "use/\x1b]0;pwn\x07t/c.go", 5),
		sym("z.Key", model.KindFunction, "use/api_key=hunter2/c.go", 5),
	)

	for i := first; i < len(syms); i++ {
		edges = append(edges, edgeAt{i, 2, model.OriginQualified})
	}

	st, root := seedGraph(t, syms, edges)

	out := whyFile(t, st, root, "store/store.go")
	section := definesSection(t, out)

	// The callers in other files lead, and the test callers stay out,
	// because Save has callers outside test files.
	assert.Equal(t,
		"           caller definitions  api.Handle api/handler.go:5 · cli.X cli/x.go:7 (name match) · store (external) · +3 more: why store.Save",
		entryLines(t, section, "store.Save")[2])

	// A symbol with only test callers shows them.
	assert.Equal(t,
		"           caller definitions  store.TestHelper store/store_test.go:40 · store.TestHelper2 store/store_test.go:50 · store.TestSave store/store_test.go:9",
		entryLines(t, section, "store.helper")[2])

	commit := entryLines(t, section, "store.Tx.Commit")
	require.Len(t, commit, 3)
	assert.Equal(t,
		"           caller definitions  x.]0;xEvil x/x.go:3 · y.Pwn use/]0;pwnt/c.go:5 · z.Key use/api_key=[REDACTED]",
		commit[2])
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x07")
	assert.NotContains(t, out, "hunter2")

	assert.Equal(t, out, whyFile(t, st, root, "store/store.go"), "two runs print the same bytes")
}

// fanIn returns n caller functions of the callee at index callee, each in
// its own file under dir. The names are zero-padded, so their FQN order is
// their number order.
func fanIn(prefix string, n, callee, first int) ([]model.Symbol, []edgeAt) {
	syms := make([]model.Symbol, 0, n)
	edges := make([]edgeAt, 0, n)

	for i := range n {
		name := fmt.Sprintf("%s%05d", prefix, i)
		syms = append(syms, sym("use."+name, model.KindFunction, "use/"+name+".go", 1))
		edges = append(edges, edgeAt{first + i, callee, model.OriginQualified})
	}

	return syms, edges
}

func TestWhyFileOutlineCallerRouteRuns(t *testing.T) {
	t.Run("the route reaches the symbol", func(t *testing.T) {
		syms := []model.Symbol{sym("pkg.Hot", model.KindFunction, "pkg/hot.go", 4)}
		callers, edges := fanIn("c", 7, 0, len(syms))
		st, root := seedGraph(t, append(syms, callers...), edges)

		section := definesSection(t, whyFile(t, st, root, "pkg/hot.go"))
		sample := entryLines(t, section, "pkg.Hot")[2]

		assert.Equal(t, 3, strings.Count(sample, " use/"))
		assert.True(t, strings.HasSuffix(sample, " · +4 more: why pkg.Hot"), sample)

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "pkg.Hot"))

		lines := strings.Split(b.String(), "\n")
		assert.Equal(t, "pkg.Hot  (function)", lines[0])
		assert.Equal(t, "  defined  pkg/hot.go:4", lines[1])
		assert.Contains(t, b.String(), "\ncallers (7)  — can be incomplete: ")
	})

	const noRoute = "+5 more (no why route: this name does not resolve to this symbol)"

	t.Run("two symbols share the FQN", func(t *testing.T) {
		syms := []model.Symbol{
			sym("pkg.init", model.KindFunction, "pkg/a.go", 3),
			sym("pkg.init", model.KindFunction, "pkg/a.go", 9),
		}
		first, firstEdges := fanIn("a", 8, 0, len(syms))
		syms = append(syms, first...)
		second, secondEdges := fanIn("b", 8, 1, len(syms))
		syms = append(syms, second...)

		st, root := seedGraph(t, syms, append(firstEdges, secondEdges...))

		section := definesSection(t, whyFile(t, st, root, "pkg/a.go"))

		assert.Equal(t, 2, strings.Count(section, " · "+noRoute))
		assert.NotContains(t, section, "more: why ")
	})

	t.Run("a root-package name matches another symbol", func(t *testing.T) {
		syms := []model.Symbol{
			sym("hot", model.KindFunction, "hot.go", 3),
			sym("other.hot", model.KindFunction, "other/other.go", 3),
		}
		callers, edges := fanIn("c", 8, 0, len(syms))
		st, root := seedGraph(t, append(syms, callers...), edges)

		section := definesSection(t, whyFile(t, st, root, "hot.go"))

		assert.True(t, strings.HasSuffix(entryLines(t, section, "hot")[2], " · "+noRoute), section)
		assert.NotContains(t, section, "more: why ")
	})

	t.Run("a name that is also an indexed file", func(t *testing.T) {
		// Why opens the report of a file before it looks for a symbol, so
		// "why tools/gen" cannot reach the function tools/gen.
		syms := []model.Symbol{
			sym("tools/gen", model.KindFunction, "tools/gen.go", 3),
			sym("tools/gen.main", model.KindFunction, "tools/gen", 1),
		}
		callers, edges := fanIn("c", 8, 0, len(syms))
		st, root := seedGraph(t, append(syms, callers...), edges)

		section := definesSection(t, whyFile(t, st, root, "tools/gen.go"))

		assert.True(t, strings.HasSuffix(entryLines(t, section, "tools/gen")[2], " · "+noRoute), section)
		assert.NotContains(t, section, "more: why ")
		assert.True(t, strings.HasPrefix(whyFile(t, st, root, "tools/gen"), "tools/gen\n\ndefines (1)\n"))
	})

	t.Run("an empty name", func(t *testing.T) {
		// An empty key is no name. The store matches it against each empty
		// name, and the MCP tool refuses a blank query.
		syms := []model.Symbol{sym("", model.KindFunction, "pkg/anon.go", 3)}
		callers, edges := fanIn("c", 8, 0, len(syms))
		st, root := seedGraph(t, append(syms, callers...), edges)

		section := definesSection(t, whyFile(t, st, root, "pkg/anon.go"))

		assert.True(t, strings.HasSuffix(callerDefinitions(t, section), " · "+noRoute), section)
		assert.NotContains(t, section, "more: why ")
	})

	t.Run("a name that the scrub changes", func(t *testing.T) {
		// The report prints the scrubbed name, and the index holds no
		// symbol by that name. The route also must not print the raw name.
		for _, fqn := range []string{"sec/esc\x1b[31mred.S", "sec/api_key=hunter2.S"} {
			t.Run(scrubText(fqn), func(t *testing.T) {
				syms := []model.Symbol{sym(fqn, model.KindFunction, "sec/s.go", 3)}
				callers, edges := fanIn("c", 8, 0, len(syms))
				st, root := seedGraph(t, append(syms, callers...), edges)

				section := definesSection(t, whyFile(t, st, root, "sec/s.go"))

				assert.True(t, strings.HasSuffix(callerDefinitions(t, section), " · "+noRoute), section)
				assert.NotContains(t, section, "more: why ")
				assert.NotContains(t, section, "\x1b")
				assert.NotContains(t, section, "hunter2")
			})
		}
	})

	t.Run("a name that needs shell quotes gets a route for each surface", func(t *testing.T) {
		// An ECMA name starts with its module path, so a route directory
		// such as "(shop)" or "[id]" is part of the name. A shell reads
		// "(" as syntax, expands "$cents" to nothing, and splits at a space.
		for _, fqn := range []string{"app/(shop)/util.formatPrice", "app/[id]/money.$cents", "my dir/util.spaced"} {
			t.Run(fqn, func(t *testing.T) {
				file := fqn[:strings.LastIndex(fqn, ".")] + ".ts"
				syms := []model.Symbol{sym(fqn, model.KindFunction, file, 1)}
				callers, edges := fanIn("c", 5, 0, len(syms))
				st, root := seedGraph(t, append(syms, callers...), edges)

				line := callerDefinitions(t, definesSection(t, whyFile(t, st, root, file)))
				want := fmt.Sprintf(" · +2 more: why %s (MCP) or `seamark why '%s'`", fqn, fqn)
				require.True(t, strings.HasSuffix(line, want), line)

				// The MCP query is the name as printed.
				assert.True(t, strings.HasPrefix(whyFile(t, st, root, fqn), fqn+"  (function)\n"))

				// The CLI word reaches a real shell as the same name.
				_, word, _ := strings.Cut(line, "`seamark why ")
				word = strings.TrimSuffix(word, "`")

				arg, err := exec.Command("sh", "-c", "printf %s "+word).CombinedOutput()
				require.NoError(t, err, string(arg))
				assert.Equal(t, fqn, string(arg))
			})
		}
	})
}

// callerDefinitions returns the one caller definitions line of section.
// A test uses it when the entry name holds a space or is empty, because
// entryLines finds an entry by its second field.
func callerDefinitions(t *testing.T, section string) string {
	t.Helper()

	var found []string

	for _, l := range strings.Split(section, "\n") {
		if strings.HasPrefix(l, "           caller definitions  ") {
			found = append(found, l)
		}
	}

	require.Len(t, found, 1, section)

	return found[0]
}

func TestWhyFileOutlineCallerBudget(t *testing.T) {
	syms, _ := oneLineFuncs("pkg/many.go", 20)

	var edges []edgeAt

	for i := range 20 {
		callers, more := fanIn(fmt.Sprintf("f%02d_", i), 4, i, len(syms))
		syms = append(syms, callers...)
		edges = append(edges, more...)
	}

	st, root := seedGraph(t, syms, edges)

	section := definesSection(t, whyFile(t, st, root, "pkg/many.go"))

	for i := 1; i <= 20; i++ {
		entry := entryLines(t, section, fmt.Sprintf("pkg.f%d", i))
		assert.Equal(t, "           callers  4 · 4 in other non-test files · 0 in test files", entry[2])

		if i <= 10 {
			require.Len(t, entry, 4)
			assert.Equal(t, 3, strings.Count(entry[3], " use/"))
		} else {
			assert.Len(t, entry, 3, "entry %d is past the budget", i)
		}
	}

	assert.Contains(t, section,
		"\n  … caller definitions not shown for 10 symbols (outline budget: 30); their count lines still show")
}

func TestWhyFileOutlineCallerBudgetEndsInsideAnEntry(t *testing.T) {
	// f1 has one caller, f2 to f14 have four each, and f15 to f21 have
	// none. f1 to f10 spend 28 of the 30 caller definitions, so f11 gets
	// the last two. f21 is past the outline limit.
	syms, _ := oneLineFuncs("pkg/many.go", 21)

	var edges []edgeAt

	for i := range 14 {
		n := 4
		if i == 0 {
			n = 1
		}

		callers, more := fanIn(fmt.Sprintf("f%02d_", i), n, i, len(syms))
		syms = append(syms, callers...)
		edges = append(edges, more...)
	}

	st, root := seedGraph(t, syms, edges)

	section := definesSection(t, whyFile(t, st, root, "pkg/many.go"))

	assert.Equal(t, 30, strings.Count(section, " use/"))
	assert.Equal(t,
		"           caller definitions  use.f10_00000 use/f10_00000.go:1 · use.f10_00001 use/f10_00001.go:1 · +2 more: why pkg.f11",
		entryLines(t, section, "pkg.f11")[3])

	for _, fqn := range []string{"pkg.f12", "pkg.f13", "pkg.f14"} {
		assert.Len(t, entryLines(t, section, fqn), 3, "%s is past the budget", fqn)
	}

	// The omission line ends the entries, and the closing lines follow it.
	lines := strings.Split(section, "\n")
	require.GreaterOrEqual(t, len(lines), 3)

	tail := lines[len(lines)-3:]
	assert.True(t, strings.HasPrefix(tail[0], "  … 1 more not shown; "), tail[0])
	assert.Equal(t, "  … caller definitions not shown for 3 symbols (outline budget: 30); their count lines still show", tail[1])
	assert.Equal(t, noneIndexedNote, tail[2])
}

func TestWhyFileOutlineHotSymbolSize(t *testing.T) {
	digits := regexp.MustCompile(`\d+`)

	outline := func(hotOther, hotTests int) []string {
		syms, _ := oneLineFuncs("hot/hot.go", 20)

		var edges []edgeAt

		for i := range 20 {
			other, tests := 5, 0
			if i == 0 {
				other, tests = hotOther, hotTests
			}

			callers, more := fanIn(fmt.Sprintf("c%02d_", i), other+tests, i, len(syms))

			for j := other; j < len(callers); j++ {
				callers[j].File = strings.TrimSuffix(callers[j].File, ".go") + "_test.go"
			}

			syms = append(syms, callers...)
			edges = append(edges, more...)
		}

		st, root := seedGraph(t, syms, edges)

		return strings.Split(definesSection(t, whyFile(t, st, root, "hot/hot.go")), "\n")
	}

	small := outline(5, 1)
	hot := outline(5000, 1000)

	assert.LessOrEqual(t, len(hot), 84)
	require.Len(t, hot, len(small))

	for i := range hot {
		if hot[i] == small[i] {
			continue
		}

		// Only the counts and the number of omitted callers change.
		assert.True(t, strings.HasPrefix(hot[i], "           caller"), "line %d: %q", i, hot[i])
		assert.Equal(t, digits.ReplaceAllString(small[i], "#"), digits.ReplaceAllString(hot[i], "#"))
	}

	assert.Contains(t, hot, "           callers  6000 · 5000 in other non-test files · 1000 in test files")
}
