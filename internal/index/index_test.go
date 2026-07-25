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

func TestIndexTypeScriptModules(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"web/src/api/client.ts": `export async function fetchJSON(path: string) {
	return fetch(path);
}
`,
		"web/src/api/snapshots.ts": `import { fetchJSON } from "./client";
import * as util from "../util";

export function load(id: string) {
	fetchJSON("/snap/" + id);
	util.clean(id);
}
`,
		"web/src/util.ts": `export function clean(s: string) {
	return s.trim();
}
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	sum, st := runIndex(t, root)
	assert.Equal(t, 3, sum.FilesParsed)

	// Named import: bare fetchJSON() call resolves across modules.
	fetchJSON := mustFind(t, st, "web/src/api/client.fetchJSON")
	callers, err := st.Callers(fetchJSON.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1)
	assert.Equal(t, "web/src/api/snapshots.load", callers[0].FQN)

	// Namespace import: util.clean() resolves through the qualifier.
	clean := mustFind(t, st, "web/src/util.clean")
	callers, err = st.Callers(clean.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1)
	assert.Equal(t, "web/src/api/snapshots.load", callers[0].FQN)

	// Module-level IMPORTS edges: snapshots -> client and util.
	snapMod := mustFind(t, st, "web/src/api/snapshots")
	require.Equal(t, model.KindPackage, snapMod.Kind)
	targets, err := st.EdgesFrom(snapMod.ID, model.EdgeImports)
	require.NoError(t, err)

	var fqns []string
	for _, s := range targets {
		fqns = append(fqns, s.FQN)
	}
	assert.ElementsMatch(t, []string{"web/src/api/client", "web/src/util"}, fqns)
}

func TestUniqueNameJudgedAcrossEcmaDialects(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		// render exists in BOTH a .ts and a .tsx file: not unique within
		// the ECMA family, so the unique-name fallback must abstain.
		// Per-dialect buckets would see one candidate each and fabricate
		// an edge.
		"store.ts": `export class Store {
	render(): string { return "s"; }
}
`,
		"panel.tsx": `export class Panel {
	render(): string { return "p"; }
}
`,
		"caller.ts": `export function use(x: { render(): string }) {
	x.render();
}
`,
	}
	for rel, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	use := mustFind(t, st, "caller.use")
	callees, err := st.Callees(use.ID)
	require.NoError(t, err)
	assert.Empty(t, callees, "ambiguous method name across dialects must produce no edge")
}

func TestNoCrossLanguagePackageKeyCollision(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/app\n",
		// Go package whose directory key "web/api" textually equals the
		// module key a TS import of "./api" resolves to.
		"web/api/api.go": `package api

func Parse() {}
`,
		"web/consumer.ts": `import { Parse } from "./api";

export function use(s: string) {
	Parse(s);
}
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	// The TS import must not resolve into the Go namespace.
	parse := mustFind(t, st, "web/api.Parse")
	callers, err := st.Callers(parse.ID)
	require.NoError(t, err)
	assert.Empty(t, callers, "a TS import must never resolve to a Go function")

	// Two package nodes share FQN "web/api" (the Go directory and the TS
	// import target); the Go one is the node that DEFINES Parse. It must
	// have no importer from the TS side.
	pkgs, err := st.FindSymbols("web/api", 10)
	require.NoError(t, err)

	var goPkg *model.Symbol

	for i := range pkgs {
		defined, err := st.EdgesFrom(pkgs[i].ID, model.EdgeDefines)
		require.NoError(t, err)

		for _, d := range defined {
			if d.FQN == "web/api.Parse" {
				goPkg = &pkgs[i]
			}
		}
	}

	require.NotNil(t, goPkg, "the Go package node must DEFINE Parse")

	importers, err := st.EdgesTo(goPkg.ID, model.EdgeImports)
	require.NoError(t, err)

	for _, imp := range importers {
		assert.NotEqual(t, "web/consumer", imp.FQN,
			"the TS module must not gain an IMPORTS edge into the Go package")
	}
}

func TestIndexPythonModules(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"api/client.py": `def fetch_json(path):
    return path
`,
		"api/tracker.py": `def record(x):
    return x
`,
		"api/snapshots.py": `from .client import fetch_json
from . import tracker


class Store:
    def load(self, snap_id):
        fetch_json(snap_id)
        tracker.record(snap_id)
        return self.decorate(snap_id)

    def decorate(self, snap_id):
        return snap_id
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	load := mustFind(t, st, "api/snapshots.Store.load")
	callees, err := st.Callees(load.ID)
	require.NoError(t, err)

	var fqns []string
	for _, c := range callees {
		fqns = append(fqns, c.FQN)
	}
	assert.ElementsMatch(t, []string{
		"api/client.fetch_json",        // relative named import, bare call
		"api/tracker.record",           // `from . import tracker` qualifier
		"api/snapshots.Store.decorate", // self-call, same class
	}, fqns)
}

func TestSameClassResolution(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		// Two classes with a method named refresh: unique-name would
		// abstain, but self/this calls resolve within the caller's class.
		"a.py": `class Cache:
    def refresh(self):
        return 1

    def tick(self):
        return self.refresh()
`,
		"b.ts": `export class Panel {
	refresh(): number { return 2; }

	tick(): number { return this.refresh(); }
}
`,
	}
	for rel, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	pyTick := mustFind(t, st, "a.Cache.tick")
	callees, err := st.Callees(pyTick.ID)
	require.NoError(t, err)
	require.Len(t, callees, 1, "self.refresh() must resolve despite the cross-file duplicate")
	assert.Equal(t, "a.Cache.refresh", callees[0].FQN)

	tsTick := mustFind(t, st, "b.Panel.tick")
	callees, err = st.Callees(tsTick.ID)
	require.NoError(t, err)
	require.Len(t, callees, 1, "this.refresh() must resolve despite the cross-file duplicate")
	assert.Equal(t, "b.Panel.refresh", callees[0].FQN)
}

func TestLocalNamedSelfDoesNotFabricateSameClassEdge(t *testing.T) {
	root := t.TempDir()
	// Worker has a helper; so does Other — two candidates, so unique-name
	// must abstain, and the local `cls` must not trigger the same-class
	// tier (which would confidently pick Worker.helper, wrongly).
	src := `def get_other():
    return None


class Worker:
    def run(self):
        cls = get_other()
        return cls.helper()

    def helper(self):
        return 1


class Other:
    def helper(self):
        return 2
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "m.py"), []byte(src), 0o644))

	_, st := runIndex(t, root)

	run := mustFind(t, st, "m.Worker.run")
	callees, err := st.Callees(run.ID)
	require.NoError(t, err)

	for _, c := range callees {
		assert.NotEqual(t, "m.Worker.helper", c.FQN,
			"a local variable named cls must not resolve as the receiver")
	}
}

func TestInitShadowsSiblingModule(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		// api.py and api/__init__.py both claim key "api"; Python's import
		// system gives the package precedence and api.py is unreachable.
		"api.py": `def f():
    return "module"
`,
		"api/__init__.py": `def g():
    return "package"
`,
		"consumer.py": `from api import f, g


def use():
    f()
    g()
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	use := mustFind(t, st, "consumer.use")
	callees, err := st.Callees(use.ID)
	require.NoError(t, err)

	var fqns []string
	for _, c := range callees {
		fqns = append(fqns, c.FQN)
	}
	// g resolves into the package; f exists only in the shadowed api.py,
	// where that import would raise ImportError — no edge.
	assert.Equal(t, []string{"api.g"}, fqns)
}

func TestBestOriginWinsPerEdge(t *testing.T) {
	root := t.TempDir()
	// tick reaches refresh twice: via a complex operand (unique-name
	// guess) first in source order, then via self (same-class). The
	// stored edge must carry the stronger origin.
	src := `class Cache:
    def tick(self):
        get_cache().refresh()
        self.refresh()

    def refresh(self):
        return 1


def get_cache():
    return Cache()
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "c.py"), []byte(src), 0o644))

	_, st := runIndex(t, root)

	tick := mustFind(t, st, "c.Cache.tick")
	callees, err := st.Callees(tick.ID)
	require.NoError(t, err)

	found := false

	for _, c := range callees {
		if c.FQN == "c.Cache.refresh" {
			found = true

			assert.Equal(t, model.OriginSameClass, c.Origin,
				"the stronger derivation must win over the source-order-first guess")
		}
	}

	require.True(t, found, "tick must reach refresh")
}

func TestEscapedRelativeImportLeavesNoJunkNode(t *testing.T) {
	root := t.TempDir()
	src := `from ....outside import thing


def use():
    thing()
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "top.py"), []byte(src), 0o644))

	_, st := runIndex(t, root)

	syms, err := st.FindSymbols("outside", 10)
	require.NoError(t, err)
	assert.Empty(t, syms, "an import escaping the repo must not materialize a package node")
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
