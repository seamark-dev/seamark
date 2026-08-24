package index

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestNestedGoModuleResolvesImports(t *testing.T) {
	root := t.TempDir()
	// The trading-tools shape: a Python repo with a Go module nested in a
	// subdirectory. Its internal imports must resolve against ITS go.mod,
	// not the (absent) root one.
	files := map[string]string{
		"main.py":         "def top():\n    return 1\n",
		"ingestor/go.mod": "module example.com/ingestor\n",
		"ingestor/cmd/run.go": `package main

import "example.com/ingestor/internal/bars"

func main() {
	bars.Transform("x")
}
`,
		"ingestor/internal/bars/bars.go": `package bars

func Transform(s string) string { return s }
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	transform := mustFind(t, st, "ingestor/internal/bars.Transform")
	callers, err := st.Callers(transform.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1, "cross-package call inside a nested module must resolve")
	assert.Equal(t, "ingestor/cmd.main", callers[0].FQN)
	assert.Equal(t, model.OriginQualified, callers[0].Origin)
}

func TestEffectDetectionAndPropagation(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fx\n",
		// Go: direct sink through an import-qualified call, propagated up
		// a two-level call chain.
		"runner.go": `package main

import "os/exec"

func runCmd() {
	exec.Command("ls").Run()
}

func level1() {
	runCmd()
}

func main() {
	level1()
}
`,
		// Python: a named-import bare call (from subprocess import run)
		// and a method-matcher sink (cur.execute).
		"jobs.py": `from subprocess import run


def shell(cmd):
    run(cmd)


def persist(cur, row):
    cur.execute("INSERT ...", row)
`,
	}
	for rel, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}

	sum, st := runIndex(t, root)
	assert.Positive(t, sum.Stats.Tagged, "summary reports tagged symbols")

	wantEffect := func(query, tag, origin string, depth int) {
		t.Helper()

		sym := mustFind(t, st, query)
		effs, err := st.EffectsForSymbol(sym.ID)
		require.NoError(t, err)
		require.Len(t, effs, 1, "effects of %s", query)
		assert.Equal(t, store.Effect{Tag: tag, Origin: origin, Depth: depth}, effs[0], query)
	}

	wantEffect("runCmd", "proc:exec", "direct", 0)
	wantEffect("level1", "proc:exec", "propagated", 1)
	wantEffect("main", "proc:exec", "propagated", 2)
	wantEffect("jobs.shell", "proc:exec", "direct", 0)
	wantEffect("jobs.persist", "db:write", "direct", 0)

	// Untagged symbols stay clean.
	other := mustFind(t, st, "jobs")
	effs, err := st.EffectsForSymbol(other.ID)
	require.NoError(t, err)
	assert.Empty(t, effs, "the module package symbol carries no effects")
}

func TestUniqueNameSkipsTestDoubles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		// The only repo-declared method named `begin` lives in a test
		// double. Production callers must never resolve into it; test
		// callers legitimately may.
		"tests/test_db.py": `class _FakeEngine:
    def begin(self):
        return None


def test_uses_fake(engine):
    engine.begin()
`,
		"api/service.py": `def save(session, row):
    session.begin()
    return row
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, st := runIndex(t, root)

	begin := mustFind(t, st, "tests/test_db._FakeEngine.begin")
	callers, err := st.Callers(begin.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1, "only the test-file caller may resolve to the double")
	assert.Equal(t, "tests/test_db.test_uses_fake", callers[0].FQN)

	save := mustFind(t, st, "api/service.save")
	callees, err := st.Callees(save.ID)
	require.NoError(t, err)
	assert.Empty(t, callees, "production session.begin() must not resolve into a test double")
}

func TestIndexFastPathSkipsFreshWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "initial")

	first, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, first.Skipped)

	// Unchanged workspace: the fast path answers from the fingerprint.
	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, second.Skipped, "unchanged workspace must not rebuild")
	assert.Equal(t, first.Stats, second.Stats, "skipped summary reports real stats")

	// Force overrides the fast path.
	forced, err := Run(Options{Root: root, Force: true})
	require.NoError(t, err)
	assert.False(t, forced.Skipped)

	// A content change defeats the fingerprint.
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.go"),
		[]byte("package main\n\nfunc added() {}\n"), 0o644))

	third, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, third.Skipped, "changed workspace must rebuild")

	// Re-editing an ALREADY-DIRTY tracked file must register too: its
	// `git status` line does not change, only its content does. The
	// fingerprint has to be content-sensitive, not status-sensitive.
	tracked := filepath.Join(root, "main.go")

	require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc main() {}\n"), 0o644))

	fourth, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, fourth.Skipped, "dirtying a tracked file must rebuild")

	require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc main() { _ = 1 }\n"), 0o644))

	fifth, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, fifth.Skipped, "editing an already-dirty file must rebuild")

	// And back to rest: nothing changed since the fifth run.
	sixth, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, sixth.Skipped)
}

func TestOverlayEditDefeatsFastPath(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "initial")

	first, err := Run(Options{Root: root})
	require.NoError(t, err)
	require.False(t, first.Skipped)

	// .seamark is excluded from the git fingerprint, so the overlay files
	// that shape index output are hashed explicitly. An indexing-config
	// edit must invalidate the fast path — otherwise a new exclude is
	// silently ignored until an unrelated file changes.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("index:\n  exclude:\n    - '**/util.go'\n"), 0o644))

	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	require.False(t, second.Skipped, "a config.yaml edit must rebuild, not fast-path")
	assert.Equal(t, 1, second.FilesSkipped, "and the new exclude must apply")

	// Steady state again: the stored fingerprint now covers the overlay.
	third, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, third.Skipped, "unchanged workspace (config included) fast-paths")

	// The effect overlay is hashed the same way.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"),
		[]byte("sinks:\n  - language: go\n    import: fmt\n    names: [Printf]\n    tag: infra:mutate\n"), 0o644))

	fourth, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, fourth.Skipped, "an effects.yaml edit must rebuild")
}

func TestRunEmitsProgressPhases(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	type event struct {
		phase       string
		done, total int
	}

	var events []event

	_, err := Run(Options{Root: root, Progress: func(phase string, done, total int) {
		events = append(events, event{phase, done, total})
	}})
	require.NoError(t, err)

	phases := map[string]bool{}
	lastParse := 0

	for _, e := range events {
		phases[e.phase] = true

		if e.phase == "parse" {
			assert.Greater(t, e.done, lastParse, "parse progress is monotonic")
			assert.Positive(t, e.total)
			lastParse = e.done
		}
	}

	assert.True(t, phases["scan"] && phases["parse"] && phases["write"],
		"the core phases must be reported: %v", phases)

	final := events[len(events)-1]
	assert.Equal(t, event{"write", 1, 1}, final, "the write phase closes the run")
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

func TestRefreshReviewsKeepsLessonsWhenSourceUnavailable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	// A git repo with NO GitHub remote: reviews.Mine reports "not a
	// GitHub repository" (Fetched=false), so RefreshReviews must leave any
	// previously-mined lessons intact rather than wiping them (finding #1).
	cmd := exec.Command("git", "init")
	cmd.Dir = root
	require.NoError(t, cmd.Run())

	dbPath := store.DefaultPath(root)

	_, err := Run(Options{Root: root, DBPath: dbPath})
	require.NoError(t, err)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "scripts\x00RUF001", Region: "scripts", Reviewer: "coderabbit",
			Symptom: "RUF001", Occurrences: 5, LastTS: 1},
	}, nil))
	require.NoError(t, st.Close())

	res, fixCount, err := RefreshReviews(root, dbPath, nil)
	require.NoError(t, err)
	assert.False(t, res.Fetched, "no GitHub remote → not a successful fetch")

	// Fix mining is local and independent: it runs and persists whatever
	// the review half does, so the two sources never take each other
	// down. (writeFixture's repo has no fix commits, hence zero.)
	assert.Zero(t, fixCount)

	fixStore, err := store.Open(dbPath)
	require.NoError(t, err)

	stored, err := fixStore.AllFindings()
	require.NoError(t, err)
	require.NoError(t, fixStore.Close())
	assert.Empty(t, stored)

	st, err = store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	kept, err := st.TopLessons(1, 10)
	require.NoError(t, err)
	assert.Len(t, kept, 1, "a failed fetch must not wipe stored lessons")
}

func TestRefreshFixesMinesReachableHistoryWithoutReplacingReviews(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "initial")

	mainPath := filepath.Join(root, "main.go")
	data, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mainPath,
		append(data, []byte("\nfunc resetHelperState() {}\n")...), 0o644))
	git("add", "-A")
	git("commit", "-m", "fix: reset helper state before reuse (#42)")

	_, err = Run(Options{Root: root})
	require.NoError(t, err)

	dbPath := store.DefaultPath(root)
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons([]model.Lesson{{
		ClusterKey: "main.go\x00review", Region: "main.go", Reviewer: "human",
		Symptom: "review", Occurrences: 1,
	}}, []model.Finding{{
		ID: 7, LessonKey: "main.go\x00review", Path: "main.go",
		Body: "existing review", Source: model.SourceReview,
	}}))
	require.NoError(t, st.Close())

	count, err := RefreshFixes(root, dbPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	st, err = store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	findings, err := st.AllFindings()
	require.NoError(t, err)
	require.Len(t, findings, 2)

	sources := map[string]int{}
	for _, finding := range findings {
		sources[finding.Source]++
	}
	assert.Equal(t, 1, sources[model.SourceReview])
	assert.Equal(t, 1, sources[model.SourceFixConventional])

	lessons, err := st.TopLessons(1, 10)
	require.NoError(t, err)
	assert.Len(t, lessons, 1, "local fix refresh must not replace the review provider's data")

	stamp, err := st.GetMeta(store.MetaFixesMinedAt)
	require.NoError(t, err)
	assert.NotEmpty(t, stamp)
}

func TestParseCacheReusesUnchanged(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	first, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Equal(t, first.FilesParsed, first.FilesReparsed, "cold index reparses every file")
	require.NotZero(t, first.FilesParsed)

	// A non-git fixture has no fingerprint fast-path, so this Run really
	// executes the parse loop — and must serve every file from cache.
	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Equal(t, first.FilesParsed, second.FilesParsed)
	assert.Zero(t, second.FilesReparsed, "unchanged files come from the cache")
	assert.Equal(t, first.Stats, second.Stats, "cached result identical to fresh")
}

func TestParseCacheReparsesChangedAndMatchesFull(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	_, err := Run(Options{Root: root})
	require.NoError(t, err)

	// Change one file's content; a second file stays byte-identical.
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n\nfunc added() {}\n"), 0o644))

	incr, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Equal(t, 1, incr.FilesReparsed, "only the changed file reparses")

	// The incremental graph MUST equal a from-scratch rebuild of the same
	// state — this is the invariant the whole feature rests on.
	full, err := Run(Options{Root: root, Force: true})
	require.NoError(t, err)
	assert.Equal(t, full.FilesParsed, full.FilesReparsed, "--force reparses all")
	assert.Equal(t, full.Stats, incr.Stats, "incremental == full rebuild")
}

func TestParseCachePrunesDeletedFile(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	_, err := Run(Options{Root: root})
	require.NoError(t, err)

	// Delete a source file and reindex: its symbols must vanish and its
	// cache row must be pruned (not linger to reappear).
	require.NoError(t, os.Remove(filepath.Join(root, "internal", "util", "util.go")))

	_, st := runIndex(t, root)

	syms, err := st.FindSymbols("Greet", 5)
	require.NoError(t, err)
	assert.Empty(t, syms, "a deleted file's symbols must not survive")
}

func TestParseCacheVersionBumpReparses(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	_, err := Run(Options{Root: root})
	require.NoError(t, err)

	// Simulate a seamark upgrade: a stale cache version forces a reparse.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("parse_cache_version", "stale"))
	require.NoError(t, st.Close())

	again, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Equal(t, again.FilesParsed, again.FilesReparsed,
		"a version mismatch reparses everything")
}

// graphDump renders the full graph — symbols with spans/sig/doc-hash,
// edges with their derivation origin, and effect tags with depth — into a
// stable, id-independent string (keyed by FQN, not autoincrement id) so
// two indexes can be compared for byte-identical CONTENT, not just counts.
func graphDump(t *testing.T, dbPath string) string {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var b strings.Builder

	dumpRows(t, &b, db, "SYM",
		`SELECT fqn, kind, file, start_line, start_col, end_line, end_col, sig, doc_hash
		 FROM symbol ORDER BY fqn, start_line, kind`)
	dumpRows(t, &b, db, "EDGE",
		`SELECT s.fqn, d.fqn, e.kind, e.origin
		 FROM edge e JOIN symbol s ON s.id = e.src JOIN symbol d ON d.id = e.dst
		 ORDER BY 1, 2, 3, 4`)
	dumpRows(t, &b, db, "EFFECT",
		`SELECT s.fqn, ef.tag, ef.origin, ef.depth
		 FROM effect ef JOIN symbol s ON s.id = ef.symbol_id
		 ORDER BY 1, 2, 3`)

	return b.String()
}

func dumpRows(t *testing.T, b *strings.Builder, db *sql.DB, tag, query string) {
	t.Helper()

	rows, err := db.Query(query)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	require.NoError(t, err)

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}

		require.NoError(t, rows.Scan(ptrs...))
		fmt.Fprintf(b, "%s %v\n", tag, vals)
	}

	require.NoError(t, rows.Err())
}

// TestParseCacheGraphContentMatchesFull is the strong form of the core
// invariant: a cached (incremental) index must reconstruct a graph
// IDENTICAL in content — spans, signatures, edge origins, effect depths —
// to a from-scratch rebuild, not merely equal in row counts. It would
// catch a gob field silently dropped, a span mangled, or an edge origin
// downgraded, all of which leave the counts untouched.
func TestParseCacheGraphContentMatchesFull(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	_, err := Run(Options{Root: root}) // cold: populate cache
	require.NoError(t, err)

	// Change one file; the others must be served from cache on the next run.
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nimport \"example.com/app/internal/util\"\n\nfunc main() { run() }\n\nfunc run() { util.Greet(\"hi\") }\n"), 0o644))

	incr, err := Run(Options{Root: root}) // incremental (cache active)
	require.NoError(t, err)
	require.Less(t, incr.FilesReparsed, incr.FilesParsed, "some files must come from cache")

	incrDump := graphDump(t, store.DefaultPath(root))

	_, err = Run(Options{Root: root, Force: true}) // full rebuild of the same state
	require.NoError(t, err)

	fullDump := graphDump(t, store.DefaultPath(root))

	assert.Equal(t, fullDump, incrDump,
		"the cached graph must be identical in content to a full rebuild, not just in counts")
}

func TestForceRepopulatesCacheForNextRun(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	_, err := Run(Options{Root: root})
	require.NoError(t, err)

	forced, err := Run(Options{Root: root, Force: true})
	require.NoError(t, err)
	assert.Equal(t, forced.FilesParsed, forced.FilesReparsed, "--force reparses all")

	// …but it must leave a fresh cache behind, so the NEXT non-force run
	// benefits rather than reparsing everything again.
	next, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Zero(t, next.FilesReparsed, "--force must repopulate the cache")
}

func TestGeneratedFilesSkippedByDefault(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	// A generated file dropped alongside the hand-written ones.
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen.go"),
		[]byte("// Code generated by protoc. DO NOT EDIT.\n\npackage main\n\nfunc Generated() {}\n"), 0o644))

	sum, st := runIndex(t, root)
	assert.Equal(t, 1, sum.FilesSkipped, "the generated file is skipped")

	syms, err := st.FindSymbols("Generated", 5)
	require.NoError(t, err)
	assert.Empty(t, syms, "a generated symbol must not enter the graph")

	// The hand-written symbols are still indexed.
	assert.NotEmpty(t, mustFind(t, st, "run").FQN)
}

func TestGeneratedFilesIncludedWhenConfigured(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen.go"),
		[]byte("// Code generated by protoc. DO NOT EDIT.\n\npackage main\n\nfunc Generated() {}\n"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("index:\n  generated: true\n"), 0o644))

	sum, st := runIndex(t, root)
	assert.Zero(t, sum.FilesSkipped)
	assert.NotEmpty(t, mustFind(t, st, "Generated").FQN, "generated:true re-includes it")
}

func TestExcludeGlobSkipsFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("index:\n  exclude:\n    - '**/util.go'\n"), 0o644))

	sum, st := runIndex(t, root)
	assert.Equal(t, 1, sum.FilesSkipped)

	syms, err := st.FindSymbols("Greet", 5) // Greet lives in the excluded util.go
	require.NoError(t, err)
	assert.Empty(t, syms, "an excluded file's symbols must not be indexed")
}
