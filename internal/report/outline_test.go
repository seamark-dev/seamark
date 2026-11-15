package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// seedOutline builds an index that holds syms in the given order, plus
// one decision for each name in decided. It writes each entry of files
// under the workspace root, so a printed ref can be expanded.
func seedOutline(t *testing.T, syms []model.Symbol, files map[string]string, decided ...string) (st *store.Store, root string) {
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

		for _, file := range decided {
			err := tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit, Ref: strings.Repeat("a", 40), TS: 100,
				Title: "Add the file", Files: []string{file}})
			if err != nil {
				return err
			}
		}

		return nil
	}))

	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	return st, root
}

// oneLineFuncs returns n functions f1..fn in file, one per line, and the
// file text that declares them on those lines.
func oneLineFuncs(file string, n int) (syms []model.Symbol, src string) {
	syms = make([]model.Symbol, 0, n)

	var b strings.Builder

	for i := 1; i <= n; i++ {
		decl := fmt.Sprintf("func f%d() {}", i)
		syms = append(syms, model.Symbol{
			FQN: fmt.Sprintf("pkg.f%d", i), Name: fmt.Sprintf("f%d", i), Kind: model.KindFunction, File: file,
			Span: model.Span{StartLine: uint32(i), EndLine: uint32(i), EndCol: uint32(len(decl))},
			Sig:  fmt.Sprintf("func f%d()", i),
		})
		b.WriteString(decl + "\n")
	}

	return syms, b.String()
}

// definesSection returns the defines section of a why <file> report: its
// header line up to the blank line that ends it.
func definesSection(t *testing.T, out string) string {
	t.Helper()

	_, rest, found := strings.Cut(out, "\ndefines (")
	require.True(t, found, "the report has a defines section:\n%s", out)

	section, _, _ := strings.Cut(rest, "\n\n")

	return "defines (" + strings.TrimSuffix(section, "\n")
}

// whyFile runs why on file and returns the whole report.
func whyFile(t *testing.T, st *store.Store, root, file string) string {
	t.Helper()

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, file))

	return b.String()
}

func TestWhyFileOutlineTotals(t *testing.T) {
	cases := []struct {
		n      int
		header string
	}{
		{0, "defines (0)  no indexed symbols"},
		{1, "defines (1)"},
		{20, "defines (20)"},
		{21, "defines (21; showing 20)"},
		{45, "defines (45; showing 20)"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			syms, src := oneLineFuncs("pkg/many.go", tc.n)

			// A file without symbols reaches why only through history.
			st, root := seedOutline(t, syms, map[string]string{"pkg/many.go": src}, "pkg/many.go")

			section := definesSection(t, whyFile(t, st, root, "pkg/many.go"))
			lines := strings.Split(section, "\n")

			assert.Equal(t, tc.header, lines[0])
			assert.Equal(t, min(tc.n, whyOutlineLimit), strings.Count(section, "\n  function "))

			if tc.n > whyOutlineLimit {
				assert.Equal(t, 1, strings.Count(section, "more not shown"))
				assert.Contains(t, section, fmt.Sprintf("  … %d more not shown;", tc.n-whyOutlineLimit))
			} else {
				assert.NotContains(t, section, "more not shown")
			}
		})
	}
}

func TestWhyFileOutlineKeepsSourceOrderAndIdentity(t *testing.T) {
	file := "pkg/a.go"
	sp := func(start, end uint32) model.Span { return model.Span{StartLine: start, EndLine: end, EndCol: 1} }

	// The rows go in out of line order. The two names of one spec share a
	// span, and b goes in first, so b has the lower ID.
	syms := []model.Symbol{
		{FQN: "pkg.B.Close", Name: "Close", Kind: model.KindMethod, File: file, Span: sp(10, 12)},
		{FQN: "pkg.init", Name: "init", Kind: model.KindFunction, File: file, Span: sp(30, 31)},
		{FQN: "pkg.A.Close", Name: "Close", Kind: model.KindMethod, File: file, Span: sp(5, 7)},
		{FQN: "pkg.init", Name: "init", Kind: model.KindFunction, File: file, Span: sp(20, 21)},
		{FQN: "pkg.b", Name: "b", Kind: model.KindVar, File: file, Span: sp(2, 2)},
		{FQN: "pkg.a", Name: "a", Kind: model.KindVar, File: file, Span: sp(2, 2)},
	}

	// Two declarations share line 40. The right one goes in first, so the
	// store returns it first, and only the start column puts it second.
	right := model.Span{StartLine: 40, StartCol: 20, EndLine: 40, EndCol: 34}
	left := model.Span{StartLine: 40, StartCol: 0, EndLine: 40, EndCol: 18}

	syms = append(syms,
		model.Symbol{FQN: "pkg.right", Name: "right", Kind: model.KindFunction, File: file, Span: right},
		model.Symbol{FQN: "pkg.left", Name: "left", Kind: model.KindFunction, File: file, Span: left},
	)

	st, root := seedOutline(t, syms, nil)

	section := definesSection(t, whyFile(t, st, root, file))

	// No symbol has a caller. So each function and method says that the
	// index has no edge, and one note closes the section.
	none := "           callers  none indexed"
	want := []string{
		fmt.Sprintf("  %-8s %-44s %s", "var", "pkg.b", "pkg/a.go:2-2"),
		fmt.Sprintf("  %-8s %-44s %s", "var", "pkg.a", "pkg/a.go:2-2"),
		fmt.Sprintf("  %-8s %-44s %s", "method", "pkg.A.Close", "pkg/a.go:5-7"), none,
		fmt.Sprintf("  %-8s %-44s %s", "method", "pkg.B.Close", "pkg/a.go:10-12"), none,
		fmt.Sprintf("  %-8s %-44s %s", "function", "pkg.init", "pkg/a.go:20-21"), none,
		fmt.Sprintf("  %-8s %-44s %s", "function", "pkg.init", "pkg/a.go:30-31"), none,
		fmt.Sprintf("  %-8s %-44s %s", "function", "pkg.left", "pkg/a.go:40-40"), none,
		fmt.Sprintf("  %-8s %-44s %s", "function", "pkg.right", "pkg/a.go:40-40"), none,
		"  none indexed: the index skips some calls, so no edge does not prove that nothing calls the symbol;" +
			" why on the symbol lists the skip rules",
	}

	assert.Equal(t, "defines (8)\n"+strings.Join(want, "\n"), section)
}

func TestSourceOrderSortsByLineThenColumnThenID(t *testing.T) {
	at := func(id int64, line, col uint32) model.Symbol {
		return model.Symbol{ID: id, Span: model.Span{StartLine: line, StartCol: col, EndLine: line, EndCol: col + 1}}
	}

	// The input holds the IDs in reverse, so the input order and the ID
	// order both differ from the expected order. Only the full sort key
	// gives it. IDs 4 and 3 share one span, so only their IDs order them.
	in := []model.Symbol{at(5, 9, 0), at(4, 2, 0), at(3, 2, 0), at(2, 2, 8), at(1, 1, 0)}

	ids := func(syms []model.Symbol) []int64 {
		out := make([]int64, 0, len(syms))
		for _, s := range syms {
			out = append(out, s.ID)
		}

		return out
	}

	assert.Equal(t, []int64{1, 3, 4, 2, 5}, ids(sourceOrder(in)))
	assert.Equal(t, []int64{5, 4, 3, 2, 1}, ids(in), "the sort works on a copy")
}

func TestInclusiveLines(t *testing.T) {
	cases := []struct {
		span       model.Span
		start, end uint32
	}{
		{model.Span{StartLine: 3, StartCol: 0, EndLine: 7, EndCol: 1}, 3, 7},
		{model.Span{StartLine: 3, StartCol: 0, EndLine: 7, EndCol: 0}, 3, 6},
		{model.Span{StartLine: 3, StartCol: 0, EndLine: 3, EndCol: 0}, 3, 3},
		{model.Span{StartLine: 3, StartCol: 0, EndLine: 0, EndCol: 0}, 3, 3},
		{model.Span{StartLine: 3, StartCol: 5, EndLine: 4, EndCol: 0}, 3, 3},
	}

	for _, tc := range cases {
		start, end := inclusiveLines(tc.span)
		assert.Equal(t, tc.start, start, "%+v", tc.span)
		assert.Equal(t, tc.end, end, "%+v", tc.span)
	}
}

func TestWhyFileOutlineRefsStopAtTheHalfOpenEnd(t *testing.T) {
	syms, src := oneLineFuncs("pkg/many.go", whyOutlineLimit+1)

	// Each span ends at column 0 of the next line, so that line is not
	// part of the symbol. The entry refs and the expand route must name
	// the declaration line only.
	for i := range syms {
		syms[i].Span.EndLine++
		syms[i].Span.EndCol = 0
	}

	st, root := seedOutline(t, syms, map[string]string{"pkg/many.go": src})

	section := definesSection(t, whyFile(t, st, root, "pkg/many.go"))

	assert.Contains(t, section, fmt.Sprintf("  %-8s %-44s %s\n", "function", "pkg.f1", "pkg/many.go:1-1"))
	assert.Contains(t, section, fmt.Sprintf("  %-8s %-44s %s\n", "function", "pkg.f20", "pkg/many.go:20-20"))
	assert.Contains(t, section, "  … 1 more not shown; source of the next one (pkg.f21): expand pkg/many.go:21-21 (MCP)")
}

func TestWhyFileOutlineSignatures(t *testing.T) {
	file := "pkg/s.go"
	sym := func(line uint32, name, sig string) model.Symbol {
		return model.Symbol{FQN: "pkg." + name, Name: name, Kind: model.KindVar, File: file,
			Span: model.Span{StartLine: line, EndLine: line, EndCol: 1}, Sig: sig}
	}

	long := "var long = " + strings.Repeat("x", 300)
	cjk := "var cjk = " + strings.Repeat("界", 150)

	// The password starts before the cut at rune 99 and ends after it. A cut
	// before redaction removes the "@" that the URL pattern needs, so the
	// head of the password prints.
	dsnName := "nightlyAnalyticsExportReplicaConnectionStringForReportingJob"
	dsn := "var " + dsnName + ` = "postgres://admin:S3cretPassw0rdValue@db.internal:5432/app"`

	beforePassword, _, _ := strings.Cut(dsn, "S3cret")
	require.Less(t, utf8.RuneCountInString(beforePassword), 99)
	require.Greater(t, utf8.RuneCountInString(beforePassword)+len("S3cretPassw0rdValue"), 99)

	st, root := seedOutline(t, []model.Symbol{
		sym(1, "absent", ""),
		sym(2, "long", long),
		sym(3, "ctrl", "var ctrl = \x1b[31m\"red\"\u202e\tand\nmore"),
		sym(4, "apiToken", `apiToken = "hunter2"`),
		sym(5, "cjk", cjk),
		sym(6, "blank", " \t\x1b\n"),
		sym(7, "last", "var last = 1"),
		sym(8, "header", "var header = \"\x1b[1mBearer s3cr3t\""),
		sym(9, dsnName, dsn),
	}, nil)

	section := definesSection(t, whyFile(t, st, root, file))
	lines := strings.Split(section, "\n")

	sigOf := func(fqn string) string {
		for i, l := range lines {
			if strings.Contains(l, " "+fqn+" ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "           ") {
				return strings.TrimPrefix(lines[i+1], "           ")
			}
		}

		return ""
	}

	// An absent signature and one that the scrub empties print no line.
	for i, l := range lines {
		if strings.Contains(l, " pkg.absent ") || strings.Contains(l, " pkg.blank ") {
			require.Less(t, i+1, len(lines))
			assert.True(t, strings.HasPrefix(lines[i+1], "  var "), "the next line is the next entry: %q", lines[i+1])
		}
	}

	// The test types the cap as a literal, so a change of the constant fails it.
	longSig := sigOf("pkg.long")
	assert.Equal(t, 100, utf8.RuneCountInString(longSig))
	assert.True(t, strings.HasSuffix(longSig, "…"))

	ctrl := sigOf("pkg.ctrl")
	assert.NotEmpty(t, ctrl)

	for _, r := range ctrl {
		assert.GreaterOrEqual(t, r, rune(0x20), "no control rune in %q", ctrl)
		assert.NotEqual(t, '\u202e', r)
	}

	assert.NotContains(t, ctrl, "  ")
	assert.NotContains(t, section, "hunter2")
	assert.Contains(t, sigOf("pkg.apiToken"), "[REDACTED]")

	cjkSig := sigOf("pkg.cjk")
	assert.True(t, utf8.ValidString(cjkSig))
	assert.Equal(t, 100, utf8.RuneCountInString(cjkSig))

	assert.Equal(t, "var last = 1", sigOf("pkg.last"))

	// The escape before Bearer stands in for the word boundary that the
	// pattern needs. Sanitize removes only the ESC byte, so redaction must
	// run first, while the escape is still there.
	assert.NotContains(t, section, "s3cr3t")
	assert.Equal(t, `var header = "[1mBearer [REDACTED]`, sigOf("pkg.header"))

	// Redaction sees the whole signature, so the cut falls after the marker.
	assert.NotContains(t, section, "S3cret")
	assert.Equal(t, "var "+dsnName+` = "postgres://admin:[REDACTED]@db.…`, sigOf("pkg."+dsnName))
}

func TestWhyFileOutlineRouteRuns(t *testing.T) {
	syms, src := oneLineFuncs("pkg/many.go", 21)
	st, root := seedOutline(t, syms, map[string]string{"pkg/many.go": src})

	section := definesSection(t, whyFile(t, st, root, "pkg/many.go"))

	assert.Equal(t, 1, strings.Count(section, "expand "))
	assert.Contains(t, section, "  … 1 more not shown; source of the next one (pkg.f21): expand pkg/many.go:21-21 (MCP)")

	_, ref, found := strings.Cut(section, "expand ")
	require.True(t, found)

	// Other closing lines can follow the route, so the ref ends at its marker.
	ref, _, found = strings.Cut(ref, " (MCP)")
	require.True(t, found)

	var b strings.Builder
	require.NoError(t, Expand(&b, st, root, ref))

	got := strings.Split(b.String(), "\n")
	assert.Equal(t, "pkg/many.go:21-21", got[0])
	assert.Contains(t, got[1], "func f21() {}")
}

func TestWhyFileOutlineWithholdsARouteThatCannotRun(t *testing.T) {
	// The two scrubbed paths exist on disk, so expand could read them. Only
	// the scrub check withholds their route, because the printed ref would
	// name a different file.
	cases := []struct {
		name  string
		file  string
		write bool
		short bool
	}{
		{"path with a colon", "pkg/a:b.go", true, false},
		{"path with an escape", "pkg/\x1b]0;x\x07.go", true, false},
		{"path that redaction changes", "pkg/token=s3cr3t.go", true, false},
		{"file absent on disk", "pkg/gone.go", false, false},
		{"file shorter than the start line", "pkg/short.go", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			syms, src := oneLineFuncs(tc.file, 21)

			files := map[string]string{}
			if tc.write {
				files[tc.file] = src
			}

			if tc.short {
				files[tc.file] = "package pkg\n"
			}

			st, root := seedOutline(t, syms, files)

			section := definesSection(t, whyFile(t, st, root, tc.file))

			// The fallback names no route: no "expand <ref>" and no MCP label.
			assert.NotContains(t, section, ": expand ")
			assert.NotContains(t, section, "(MCP)")
			assert.Contains(t, section, "  … 1 more not shown; the next one is pkg.f21 at ")
			assert.Contains(t, section, "(no expand route: this ref does not resolve)")
			assert.NotContains(t, section, "\x1b")
			assert.NotContains(t, section, "s3cr3t")
		})
	}
}

func TestWhyFileOutlineScrubsEntriesAndTheOmissionLine(t *testing.T) {
	osc := "\x1b]0;x\x07"
	file := "pkg/" + osc + "token=s3cr3t.go"

	// Each name and the path hold a terminal escape and a secret. The file
	// has one symbol more than the outline shows, so the omission line
	// names one of them too.
	syms, _ := oneLineFuncs(file, whyOutlineLimit+1)
	for i := range syms {
		syms[i].FQN += osc + "token=s3cr3t"
	}

	st, root := seedOutline(t, syms, nil)

	section := definesSection(t, whyFile(t, st, root, file))

	assert.NotContains(t, section, "\x1b")
	assert.NotContains(t, section, "\x07")
	assert.NotContains(t, section, "s3cr3t")

	assert.Contains(t, section, fmt.Sprintf("  %-8s %-44s %s\n",
		"function", "pkg.f1]0;xtoken=[REDACTED]", "pkg/]0;xtoken=[REDACTED]:1-1"))
	assert.Contains(t, section, "  … 1 more not shown; the next one is pkg.f21]0;xtoken=[REDACTED]"+
		" at pkg/]0;xtoken=[REDACTED]:21-21 (no expand route: this ref does not resolve)")
}

func TestWhyFileOutlineScrubsNamesAndPaths(t *testing.T) {
	osc := "\x1b]0;x\x07"
	file := "pkg/" + osc + ".go"

	st, root := seedOutline(t, []model.Symbol{{
		FQN: "pkg.f" + osc, Name: "f", Kind: model.KindFunction, File: file,
		Span: model.Span{StartLine: 1, EndLine: 1, EndCol: 1},
	}}, nil)

	section := definesSection(t, whyFile(t, st, root, file))

	assert.NotContains(t, section, "\x1b")
	assert.NotContains(t, section, "\x07")
	assert.Contains(t, section, "pkg.f]0;x")
	assert.Contains(t, section, "pkg/]0;x.go:1-1")
}

func TestWhySymbolSigIsRedacted(t *testing.T) {
	file := "pkg/s.go"
	long := "var longValue = " + strings.Repeat("x", 300)

	st, root := seedOutline(t, []model.Symbol{
		{FQN: "pkg.apiToken", Name: "apiToken", Kind: model.KindVar, File: file,
			Span: model.Span{StartLine: 1, EndLine: 1, EndCol: 1}, Sig: `apiToken = "hunter2"`},
		{FQN: "pkg.longValue", Name: "longValue", Kind: model.KindVar, File: file,
			Span: model.Span{StartLine: 2, EndLine: 2, EndCol: 1}, Sig: long},
		{FQN: "pkg.multiLine", Name: "multiLine", Kind: model.KindFunction, File: file,
			Span: model.Span{StartLine: 3, EndLine: 6, EndCol: 1}, Sig: "func multiLine(\n\ta int,\n\tb int,\n)"},
	}, nil)

	out := whyFile(t, st, root, "pkg.apiToken")
	assert.Contains(t, out, "  sig      apiToken = [REDACTED]\n")
	assert.NotContains(t, out, "hunter2")

	// Sanitize keeps tabs, so only the whitespace fold puts a signature
	// that spans lines on one line with single spaces.
	out = whyFile(t, st, root, "pkg.multiLine")
	assert.Contains(t, out, "  sig      func multiLine( a int, b int, )\n")

	// The symbol report is the full view, so it does not cut the signature.
	out = whyFile(t, st, root, "pkg.longValue")
	assert.Contains(t, out, "  sig      "+long+"\n")
}

func TestExpandSymbolRespectsHalfOpenEnd(t *testing.T) {
	file := "pkg/h.go"

	var src strings.Builder
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&src, "line %d\n", i)
	}

	// openEnd stops at column 0 of line 7, so line 7 is not part of it.
	// closedEnd reaches into line 7.
	st, root := seedOutline(t, []model.Symbol{
		{FQN: "pkg.openEnd", Name: "openEnd", Kind: model.KindFunction, File: file,
			Span: model.Span{StartLine: 3, EndLine: 7, EndCol: 0}},
		{FQN: "pkg.closedEnd", Name: "closedEnd", Kind: model.KindFunction, File: file,
			Span: model.Span{StartLine: 3, EndLine: 7, EndCol: 1}},
	}, map[string]string{file: src.String()})

	expand := func(ref string) string {
		var b strings.Builder
		require.NoError(t, Expand(&b, st, root, ref))

		return b.String()
	}

	// The test reads each ref from the outline that a reader sees. So a
	// change in the outline ref fails here as well as a change in expand.
	section := definesSection(t, whyFile(t, st, root, file))

	outlineRef := func(fqn string) string {
		for _, l := range strings.Split(section, "\n") {
			if strings.Contains(l, " "+fqn+" ") {
				fields := strings.Fields(l)

				return fields[len(fields)-1]
			}
		}

		return ""
	}

	openRef := outlineRef("pkg.openEnd")
	assert.Equal(t, "pkg/h.go:3-6", openRef)

	byName := expand("pkg.openEnd")
	assert.True(t, strings.HasPrefix(byName, "pkg/h.go:3-6\n"), byName)
	assert.Contains(t, byName, "line 6\n")
	assert.NotContains(t, byName, "line 7")
	assert.Equal(t, byName, expand(openRef), "the name and the outline ref show the same lines")

	closedRef := outlineRef("pkg.closedEnd")
	assert.Equal(t, "pkg/h.go:3-7", closedRef)

	closed := expand("pkg.closedEnd")
	assert.True(t, strings.HasPrefix(closed, "pkg/h.go:3-7\n"), closed)
	assert.Contains(t, closed, "line 7\n")
	assert.Equal(t, closed, expand(closedRef), "the name and the outline ref show the same lines")
}
