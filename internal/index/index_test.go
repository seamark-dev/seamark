package index

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// writeFixture lays out a two-package Go module.
func writeFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/app/internal/util"

func main() {
	run()
}

func run() {
	util.Greet("world")
}
`,
		"internal/util/util.go": `package util

import "fmt"

// Greet prints a greeting.
func Greet(name string) {
	fmt.Printf("hello %s", name)
	normalize(name)
}

func normalize(s string) string { return s }
`,
		// Unsupported and skipped files must not break the run.
		"README.md":           "# fixture\n",
		"testdata/broken.go":  "package \x00 not go at all",
		"vendor/dep/dep.go":   "package dep\nfunc Hidden() {}\n",
		"node_modules/x/x.go": "package x\nfunc AlsoHidden() {}\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

func runIndex(t *testing.T, root string) (*Summary, *store.Store) {
	t.Helper()
	sum, err := Run(Options{Root: root, Logf: t.Logf})
	require.NoError(t, err, "index run")
	st, err := store.Open(sum.DBPath)
	require.NoError(t, err, "open produced index")
	t.Cleanup(func() { _ = st.Close() })
	return sum, st
}

func mustFind(t *testing.T, st *store.Store, query string) model.Symbol {
	t.Helper()
	syms, err := st.FindSymbols(query, 5)
	require.NoError(t, err, "FindSymbols(%q)", query)
	require.NotEmpty(t, syms, "FindSymbols(%q)", query)
	return syms[0]
}

func TestIndexPlainDirectory(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)
	sum, st := runIndex(t, root)

	assert.False(t, sum.HistoryMined, "no git repo, history must be skipped")
	assert.Equal(t, 2, sum.FilesParsed, "main.go and util.go")

	greet := mustFind(t, st, "internal/util.Greet")
	assert.Equal(t, model.KindFunction, greet.Kind)
	assert.Equal(t, "internal/util/util.go", greet.File)

	// Qualified cross-package call: run -> util.Greet.
	callers, err := st.Callers(greet.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1)
	assert.Equal(t, "run", callers[0].FQN)

	// Same-package bare call: main -> run.
	run := mustFind(t, st, "run")
	mainSym := mustFind(t, st, "main")
	callees, err := st.Callees(mainSym.ID)
	require.NoError(t, err)
	require.Len(t, callees, 1)
	assert.Equal(t, run.ID, callees[0].ID)

	// Skipped trees stay out of the graph.
	for _, hidden := range []string{"Hidden", "AlsoHidden"} {
		syms, err := st.FindSymbols(hidden, 5)
		require.NoError(t, err)
		assert.Empty(t, syms, "symbol %s from a skipped dir leaked into the index", hidden)
	}
}

func TestIndexImportEdges(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)
	_, st := runIndex(t, root)

	// Root package "." imports internal/util; util imports external fmt.
	rootPkg := mustFind(t, st, ".")
	require.Equal(t, model.KindPackage, rootPkg.Kind)
	utilPkg := mustFind(t, st, "internal/util")

	rootImports, err := st.EdgesFrom(rootPkg.ID, model.EdgeImports)
	require.NoError(t, err)
	require.Len(t, rootImports, 1)
	assert.Equal(t, "internal/util", rootImports[0].FQN)

	utilImports, err := st.EdgesFrom(utilPkg.ID, model.EdgeImports)
	require.NoError(t, err)
	require.Len(t, utilImports, 1)
	assert.Equal(t, "fmt", utilImports[0].FQN)
	assert.Equal(t, model.KindPackage, utilImports[0].Kind)

	// Importers of util, queried in reverse.
	importers, err := st.EdgesTo(utilPkg.ID, model.EdgeImports)
	require.NoError(t, err)
	require.Len(t, importers, 1)
	assert.Equal(t, ".", importers[0].FQN)
}

func TestIndexWithGitHistory(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "initial layout")
	// Two more commits touching main.go and util.go together.
	for _, msg := range []string{"feat: greet loudly", "fix: greet politely"} {
		for _, f := range []string{"main.go", "internal/util/util.go"} {
			p := filepath.Join(root, f)
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p, append(data, []byte("// "+msg+"\n")...), 0o644))
		}
		git("add", "-A")
		git("commit", "-m", msg)
	}

	sum, st := runIndex(t, root)
	require.True(t, sum.HistoryMined)
	assert.Equal(t, 3, sum.Stats.Decisions)

	partners, err := st.CoChangePartners("main.go", 0, 10)
	require.NoError(t, err)
	found := false
	for _, p := range partners {
		if p.File == "internal/util/util.go" && p.Together >= 2 {
			found = true
		}
	}
	assert.True(t, found, "expected main.go/util.go co-change, got %+v", partners)

	decisions, err := st.DecisionsForFile("main.go", 10)
	require.NoError(t, err)
	require.Len(t, decisions, 3)
	assert.Equal(t, "fix: greet politely", decisions[0].Title, "newest decision first")
}

func TestMethodCallNotResolvedToPackageFunction(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	// Package w has a function Close AND a method call x.db.Close() whose
	// operand is a complex expression. The old resolver saw the call as a
	// bare identifier and fabricated w.Run CALLS w.Close (same-package).
	w := `package w

import "database/sql"

type W struct{ db *sql.DB }

func Close() {}

func (x *W) Run() {
	x.db.Close()
}
`
	p := filepath.Join(root, "internal", "w", "w.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(w), 0o644))

	_, st := runIndex(t, root)

	closeFn := mustFind(t, st, "internal/w.Close")
	callers, err := st.Callers(closeFn.ID)
	require.NoError(t, err)
	assert.Empty(t, callers, "x.db.Close() must not resolve to the package function Close")

	run := mustFind(t, st, "internal/w.W.Run")
	callees, err := st.Callees(run.ID)
	require.NoError(t, err)
	assert.Empty(t, callees, "no repo symbol matches x.db.Close(); no edge should exist")
}

func TestIndexEmptyGitRepoSummary(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init\n%s", out)

	// A repo with zero commits is empty history — mined successfully, not
	// a failure and not "not a git repository".
	sum, _ := runIndex(t, root)
	assert.True(t, sum.HistoryMined)
	assert.Zero(t, sum.Stats.Decisions)
	assert.Empty(t, sum.HistorySkipNote)
}

func TestIndexIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	first, _ := runIndex(t, root)
	second, st := runIndex(t, root)
	assert.Equal(t, first.Stats, second.Stats, "re-index must not change stats")

	// No duplicate symbols after the second pass.
	syms, err := st.FindSymbols("Greet", 10)
	require.NoError(t, err)
	assert.Len(t, syms, 1, "Greet must appear exactly once after re-index")
}
