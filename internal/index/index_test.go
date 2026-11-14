package index

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
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
		ClusterKey: "main.go\x00review", Region: "main.go", Reviewer: "person",
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
	require.NoError(t, st.SetMeta(store.MetaParseCacheVersion, "stale"))
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

// sourceFixture builds an indexed git repository with one fix commit
// and a seeded review corpus. origin is the origin URL; "" means no
// remote. Both mine stamps read "1000", so a test sees any new stamp.
func sourceFixture(t *testing.T, origin string) (root, dbPath string) {
	t.Helper()

	root = t.TempDir()
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

	if origin != "" {
		git("remote", "add", "origin", origin)
	}

	sum, err := Run(Options{Root: root})
	require.NoError(t, err)

	dbPath = sum.DBPath

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	require.NoError(t, st.ReplaceLessons([]model.Lesson{{
		ClusterKey: "main.go\x00seeded", Region: "main.go", Reviewer: "person",
		Symptom: "seeded", Occurrences: 1,
	}}, []model.Finding{{
		ID: 900, LessonKey: "main.go\x00seeded", Path: "main.go",
		Body: "seeded review", Source: model.SourceReview,
	}}))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, "1000"))
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, "1000"))

	return root, dbPath
}

// evidenceState is the stored part of the corpus that a refresh can
// change.
type evidenceState struct {
	lessons   []string
	findings  map[string]int
	reviewsAt string
	fixesAt   string
}

// readEvidenceState reads evidenceState from the database at dbPath.
func readEvidenceState(t *testing.T, dbPath string) evidenceState {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	lessons, err := st.TopLessons(1, 100)
	require.NoError(t, err)

	keys := make([]string, 0, len(lessons))
	for _, l := range lessons {
		keys = append(keys, l.Symptom)
	}

	counts, err := st.FindingCounts()
	require.NoError(t, err)

	reviewsAt, err := st.GetMeta(store.MetaReviewsMinedAt)
	require.NoError(t, err)

	fixesAt, err := st.GetMeta(store.MetaFixesMinedAt)
	require.NoError(t, err)

	return evidenceState{lessons: keys, findings: counts, reviewsAt: reviewsAt, fixesAt: fixesAt}
}

// assertReviewsKept checks that the review part of the fixture corpus
// is unchanged, as a refresh that must keep the corpus leaves it.
func assertReviewsKept(t *testing.T, got evidenceState) {
	t.Helper()

	assert.Equal(t, []string{"seeded"}, got.lessons, "the stored lessons stay")
	assert.Equal(t, 1, got.findings[model.SourceReview], "the stored review findings stay")
	assert.Equal(t, "1000", got.reviewsAt, "the review stamp stays")
}

// ghPageJSON is one gh page: an inline comment that clusters into one
// lesson and a PR-level comment that clustering drops.
const ghPageJSON = `[{"id":11,"user":{"login":"rev","type":"User"},` +
	`"body":"Reset pooled state before reuse here.","path":"main.go","line":3,` +
	`"html_url":"https://github.com/o/r/pull/1#discussion_r11","created_at":"2026-07-21T10:00:00Z",` +
	`"pull_request_url":"https://api.github.com/repos/o/r/pulls/1"},` +
	`{"id":12,"user":{"login":"rev","type":"User"},"body":"Overall fine.","path":"",` +
	`"created_at":"2026-07-22T10:00:00Z","pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

// staticFetch returns a FetchFunc that answers with raw and err.
func staticFetch(raw string, err error) reviews.FetchFunc {
	return func(context.Context, string, string, reviews.Options) ([]byte, error) {
		if err != nil {
			return nil, err
		}

		return []byte(raw), nil
	}
}

// ghExitError returns a real *exec.ExitError with the given code.
func ghExitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)

	return err
}

func TestRefreshSourcesLocalModeNeverFetches(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesLocal,
		Fetch: func(context.Context, string, string, reviews.Options) ([]byte, error) {
			t.Fatal("local mode must not contact GitHub")

			return nil, nil
		},
	})
	require.NoError(t, err)

	assert.Equal(t, SourceRefreshed, res.Fixes.State)
	assert.Equal(t, CauseNone, res.Fixes.Cause)
	assert.Equal(t, 1, res.Fixes.Stored)
	assert.Positive(t, res.Fixes.MinedAt)
	assert.Equal(t, 1, res.FixCount)
	assert.Equal(t, SourceNotAttempted, res.Reviews.State)

	got := readEvidenceState(t, dbPath)
	assertReviewsKept(t, got)
	assert.Equal(t, 1, got.findings[model.SourceFixConventional])
	assert.Equal(t, fmt.Sprint(res.Fixes.MinedAt), got.fixesAt, "the outcome reports the stored stamp")
}

func TestRefreshSourcesReviewMatrix(t *testing.T) {
	missing := fmt.Errorf("gh api: %w (is the GitHub CLI installed?)",
		&exec.Error{Name: "gh", Err: exec.ErrNotFound})

	cases := []struct {
		name    string
		fetch   reviews.FetchFunc
		state   SourceState
		cause   SourceCause
		lessons []string
		reason  string
	}{
		{name: "refreshed", fetch: staticFetch(ghPageJSON, nil), state: SourceRefreshed,
			lessons: []string{"reset pooled state before reuse here"}},
		{name: "successful empty", fetch: staticFetch("[]", nil), state: SourceEmpty,
			lessons: []string{}, reason: "no review comments found"},
		{name: "zero lessons", fetch: staticFetch(`[{"id":12,"user":{"login":"rev","type":"User"},`+
			`"body":"Overall fine.","path":"","created_at":"2026-07-22T10:00:00Z"}]`, nil),
			state: SourceEmpty, lessons: []string{}},
		{name: "gh missing", fetch: staticFetch("", missing), state: SourceUnavailable,
			cause: CauseGHMissing, reason: "executable file not found"},
		{name: "auth", fetch: staticFetch("", fmt.Errorf("gh api: To get started with GitHub CLI: %w", ghExitError(t, 4))),
			state: SourceFailed, cause: CauseAuth, reason: "To get started with GitHub CLI"},
		{name: "other exit code", fetch: staticFetch("", fmt.Errorf("gh api: HTTP 500: %w", ghExitError(t, 1))),
			state: SourceFailed, cause: CauseFetchFailed, reason: "HTTP 500"},
		{name: "fetch failed", fetch: staticFetch("", errors.New("gh api: HTTP 404: Not Found")),
			state: SourceFailed, cause: CauseFetchFailed, reason: "HTTP 404"},
		{name: "unreadable", fetch: staticFetch("not json", nil), state: SourceFailed,
			cause: CauseUnreadable, reason: "review comments unreadable"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

			res, err := RefreshSources(context.Background(), RefreshRequest{
				Root: root, Mode: SourcesReviewsAndLocal, Fetch: c.fetch,
			})
			require.NoError(t, err, "a degraded source is an outcome, not an error")

			// The local source succeeds on its own in every row.
			assert.Equal(t, SourceRefreshed, res.Fixes.State)
			assert.Equal(t, 1, res.FixCount)

			assert.Equal(t, c.state, res.Reviews.State)
			assert.Equal(t, c.cause, res.Reviews.Cause, "the cause is data, so no caller parses Reason")
			assert.Contains(t, res.Reviews.Reason, c.reason)

			got := readEvidenceState(t, dbPath)
			assert.Equal(t, 1, got.findings[model.SourceFixConventional])
			assert.NotEqual(t, "1000", got.fixesAt)

			if c.lessons == nil {
				assertReviewsKept(t, got)
				assert.Zero(t, res.Reviews.MinedAt)

				return
			}

			// A successful fetch, even an empty one, replaces the corpus.
			assert.Equal(t, c.lessons, got.lessons)
			assert.Equal(t, len(c.lessons), res.Reviews.Lessons)
			assert.Equal(t, fmt.Sprint(res.Reviews.MinedAt), got.reviewsAt)
		})
	}
}

// ghOnPath puts a gh script first on PATH. The script writes stderr and
// exits with code. The rest of PATH stays, so git still resolves.
func ghOnPath(t *testing.T, stderr string, code int) {
	t.Helper()

	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s' >&2\nexit %d\n", strings.ReplaceAll(stderr, "'", `'\''`), code)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRefreshSourcesClassifiesGHFailures runs the default gh fetcher.
// The stderr lines are the ones that gh 2.20.2 writes for each case. gh
// api exits with code 1 for every HTTP error, so the status names the cause.
func TestRefreshSourcesClassifiesGHFailures(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		code   int
		cause  SourceCause
		reason string
	}{
		{name: "no login", stderr: "To get started with GitHub CLI, please run:  gh auth login", code: 4,
			cause: CauseAuth, reason: "gh auth login"},
		{name: "bad or expired token", stderr: "gh: Bad credentials (HTTP 401)", code: 1,
			cause: CauseAuth, reason: "Bad credentials (HTTP 401)"},
		{name: "rate limit", stderr: "gh: API rate limit exceeded for user ID 1. (HTTP 403)", code: 1,
			cause: CauseRateLimited, reason: "rate limit exceeded"},
		{name: "too many requests", stderr: "gh: You have exceeded a secondary rate limit. (HTTP 429)", code: 1,
			cause: CauseRateLimited, reason: "(HTTP 429)"},
		{name: "unknown repository", stderr: "gh: Not Found (HTTP 404)", code: 1,
			cause: CauseFetchFailed, reason: "Not Found (HTTP 404)"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, dbPath := sourceFixture(t, "git@github.com:o/r.git")
			ghOnPath(t, c.stderr, c.code)

			res, err := RefreshSources(context.Background(), RefreshRequest{Root: root, Mode: SourcesReviewsAndLocal})
			require.NoError(t, err, "a degraded source is an outcome, not an error")

			assert.Equal(t, SourceFailed, res.Reviews.State)
			assert.Equal(t, c.cause, res.Reviews.Cause)
			assert.Contains(t, res.Reviews.Reason, c.reason)
			assert.Zero(t, res.Reviews.MinedAt)
			assertReviewsKept(t, readEvidenceState(t, dbPath))
		})
	}
}

func TestRefreshSourcesWithoutGitHubOrigin(t *testing.T) {
	root, dbPath := sourceFixture(t, "")

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal,
		Fetch: func(context.Context, string, string, reviews.Options) ([]byte, error) {
			t.Fatal("no GitHub origin, so no fetch")

			return nil, nil
		},
	})
	require.NoError(t, err)

	assert.Equal(t, SourceRefreshed, res.Fixes.State)
	assert.Equal(t, SourceUnavailable, res.Reviews.State)
	assert.Equal(t, CauseNoRepository, res.Reviews.Cause)
	assert.Equal(t, "not a GitHub repository", res.Reviews.Reason)
	assertReviewsKept(t, readEvidenceState(t, dbPath))
}

// readReviewRecord returns the no-review-repository record, the fixes
// stamp, and the evidence horizon of the database at dbPath.
func readReviewRecord(t *testing.T, dbPath string) (record, fixesAt string, horizon int64) {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	record, err = st.GetMeta(store.MetaNoReviewRepositoryAt)
	require.NoError(t, err)

	fixesAt, err = st.GetMeta(store.MetaFixesMinedAt)
	require.NoError(t, err)

	return record, fixesAt, st.EvidenceHorizon()
}

// setOrigin replaces the origin remote of the repository at root.
func setOrigin(t *testing.T, root, origin string) {
	t.Helper()

	for _, args := range [][]string{{"remote", "remove", "origin"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		_ = cmd.Run() // remove fails when no origin exists yet, and that is the wanted state
	}
}

func TestRefreshSourcesRecordsANoReviewRepository(t *testing.T) {
	noFetch := func(t *testing.T) reviews.FetchFunc {
		return func(context.Context, string, string, reviews.Options) ([]byte, error) {
			t.Error("an origin that names no GitHub repository is never fetched")

			return nil, errors.New("unexpected fetch")
		}
	}

	for _, origin := range []string{"", "git@gitlab.com:o/r.git"} {
		for _, mode := range []SourceMode{SourcesLocal, SourcesReviewsAndLocal} {
			t.Run(fmt.Sprintf("origin %q mode %v", origin, mode), func(t *testing.T) {
				root, dbPath := sourceFixture(t, origin)

				res, err := RefreshSources(context.Background(), RefreshRequest{
					Root: root, Mode: mode, Fetch: noFetch(t),
				})
				require.NoError(t, err)
				require.Equal(t, SourceRefreshed, res.Fixes.State)

				record, fixesAt, horizon := readReviewRecord(t, dbPath)
				assert.NotEqual(t, "1000", fixesAt)
				assert.Equal(t, fixesAt, record, "the record holds the fixes stamp of its write")
				assert.Equal(t, res.Fixes.MinedAt, horizon, "fix findings alone set the horizon")
			})
		}
	}

	t.Run("github origin", func(t *testing.T) {
		root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

		_, err := RefreshSources(context.Background(), RefreshRequest{Root: root, Mode: SourcesLocal})
		require.NoError(t, err)

		record, _, horizon := readReviewRecord(t, dbPath)
		assert.Empty(t, record)
		assert.Equal(t, int64(1000), horizon, "the older reviews stamp still bounds the horizon")
	})

	// A fetch failure comes after origin resolves to GitHub, so it never
	// counts as a repository without reviews.
	missing := fmt.Errorf("gh api: %w", &exec.Error{Name: "gh", Err: exec.ErrNotFound})
	failures := map[string]error{
		"gh missing": missing,
		"auth":       fmt.Errorf("gh api: To get started with GitHub CLI: %w", ghExitError(t, 4)),
	}

	for name, fetchErr := range failures {
		t.Run(name, func(t *testing.T) {
			root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

			res, err := RefreshSources(context.Background(), RefreshRequest{
				Root: root, Mode: SourcesReviewsAndLocal, Fetch: staticFetch("", fetchErr),
			})
			require.NoError(t, err)
			require.NotEqual(t, SourceRefreshed, res.Reviews.State)

			record, _, horizon := readReviewRecord(t, dbPath)
			assert.Empty(t, record)
			assert.Equal(t, "1000", readEvidenceState(t, dbPath).reviewsAt)
			assert.Equal(t, int64(1000), horizon)
		})
	}

	t.Run("a GitHub origin clears the record", func(t *testing.T) {
		root, dbPath := sourceFixture(t, "")

		_, err := RefreshSources(context.Background(), RefreshRequest{Root: root, Mode: SourcesLocal})
		require.NoError(t, err)

		record, _, _ := readReviewRecord(t, dbPath)
		require.NotEmpty(t, record)

		setOrigin(t, root, "git@github.com:o/r.git")

		_, err = RefreshSources(context.Background(), RefreshRequest{Root: root, Mode: SourcesLocal})
		require.NoError(t, err)

		record, _, horizon := readReviewRecord(t, dbPath)
		assert.Empty(t, record, "the next fix write decides again")
		assert.Equal(t, int64(1000), horizon)
	})

	t.Run("a fix phase that writes nothing keeps the record", func(t *testing.T) {
		root, dbPath := sourceFixture(t, "")

		st, err := store.Open(dbPath)
		require.NoError(t, err)
		require.NoError(t, st.SetMeta(store.MetaNoReviewRepositoryAt, "1000"))
		require.NoError(t, st.Close())

		_, err = RefreshSources(context.Background(), RefreshRequest{
			Root: root, Mode: SourcesLocal, BeforePersist: func(string) error { return errRefused },
		})
		require.ErrorIs(t, err, errRefused)

		// A root without git history mines nothing into the same database.
		res, err := RefreshSources(context.Background(), RefreshRequest{
			Root: t.TempDir(), DBPath: dbPath, Mode: SourcesLocal,
		})
		require.NoError(t, err)
		require.Equal(t, CauseNoHistory, res.Fixes.Cause)

		record, fixesAt, horizon := readReviewRecord(t, dbPath)
		assert.Equal(t, "1000", record)
		assert.Equal(t, "1000", fixesAt)
		assert.Equal(t, int64(1000), horizon)
	})
}

func TestRefreshSourcesStopDuringFetchKeepsTheCorpus(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	// The fake blocks like a slow gh until the stop arrives.
	fetch := func(ctx context.Context, _, _ string, _ reviews.Options) ([]byte, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}

	go func() {
		<-started
		cancel()
	}()

	begin := time.Now()
	res, err := RefreshSources(ctx, RefreshRequest{Root: root, Mode: SourcesReviewsAndLocal, Fetch: fetch})
	require.NoError(t, err)

	assert.Less(t, time.Since(begin), 10*time.Second, "the stop returns without waiting for the fetch")
	assert.Equal(t, SourceRefreshed, res.Fixes.State, "the local phase committed before the fetch")
	assert.Equal(t, SourceStopped, res.Reviews.State)
	assert.Equal(t, CauseStopped, res.Reviews.Cause)
	require.ErrorIs(t, res.Reviews.Err, context.Canceled)
	assert.Equal(t, reviews.OutcomeStopped, res.Review.Outcome)

	assertReviewsKept(t, readEvidenceState(t, dbPath))
}

func TestRefreshSourcesStopAfterFetchNeverPublishes(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	ctx, cancel := context.WithCancel(context.Background())

	// The data arrives complete, but after the stop: it must not replace
	// the stored corpus.
	fetch := func(context.Context, string, string, reviews.Options) ([]byte, error) {
		cancel()

		return []byte(ghPageJSON), nil
	}

	res, err := RefreshSources(ctx, RefreshRequest{Root: root, Mode: SourcesReviewsAndLocal, Fetch: fetch})
	require.NoError(t, err)

	assert.Equal(t, SourceStopped, res.Reviews.State)
	assert.Equal(t, CauseStopped, res.Reviews.Cause)
	assert.False(t, res.Review.Fetched)
	assertReviewsKept(t, readEvidenceState(t, dbPath))
}

func TestRefreshSourcesStopDuringTheLocalPhaseSkipsGitHub(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	// The fix mine cannot stop part way, so a stop that arrives during it
	// is seen after it: a context that is already done models that.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RefreshSources(ctx, RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal,
		Fetch: func(context.Context, string, string, reviews.Options) ([]byte, error) {
			t.Fatal("a stop before the fetch must not start it")

			return nil, nil
		},
	})
	require.NoError(t, err)

	assert.Equal(t, SourceRefreshed, res.Fixes.State, "the local phase finishes and persists")
	assert.Equal(t, SourceStopped, res.Reviews.State)
	assert.Equal(t, CauseStopped, res.Reviews.Cause)
	assert.Equal(t, "stopped before the GitHub fetch", res.Reviews.Reason)

	got := readEvidenceState(t, dbPath)
	assertReviewsKept(t, got)
	assert.NotEqual(t, "1000", got.fixesAt)
}

// failWrites installs a trigger that makes every write to table fail.
// It stands in for a store error at the commit boundary.
func failWrites(t *testing.T, dbPath, table string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	for _, op := range []string{"INSERT", "DELETE"} {
		_, err := db.Exec(fmt.Sprintf(
			"CREATE TRIGGER fail_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'disk says no'); END",
			strings.ToLower(op), table, op, table))
		require.NoError(t, err)
	}
}

func TestRefreshSourcesReviewWriteFailure(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal, Fetch: staticFetch(ghPageJSON, nil),
		BeforePersist: func(source string) error {
			if source == SourceReviews {
				failWrites(t, dbPath, "lesson")
			}

			return nil
		},
	})
	require.Error(t, err, "a write failure is an error, as RefreshReviews has always returned")

	assert.Equal(t, SourceRefreshed, res.Fixes.State, "the local success stays visible")
	assert.Equal(t, SourceFailed, res.Reviews.State)
	assert.Equal(t, CauseStore, res.Reviews.Cause, "a write failure is not a fetch failure")
	assert.Contains(t, res.Reviews.Reason, "disk says no")
	assert.True(t, res.Review.Fetched, "the raw result still reports the fetch")

	got := readEvidenceState(t, dbPath)
	assertReviewsKept(t, got)
	assert.Equal(t, 1, got.findings[model.SourceFixConventional])
}

func TestRefreshSourcesFixWriteFailureSkipsReviews(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal,
		Fetch: func(context.Context, string, string, reviews.Options) ([]byte, error) {
			t.Fatal("a failed local write must stop the refresh before GitHub")

			return nil, nil
		},
		BeforePersist: func(source string) error {
			if source == SourceFixes {
				failWrites(t, dbPath, "finding")
			}

			return nil
		},
	})
	require.Error(t, err)

	assert.Equal(t, SourceFailed, res.Fixes.State)
	assert.Equal(t, CauseStore, res.Fixes.Cause)
	assert.Contains(t, res.Fixes.Reason, "disk says no")
	assert.Equal(t, SourceNotAttempted, res.Reviews.State)

	got := readEvidenceState(t, dbPath)
	assertReviewsKept(t, got)
	assert.Equal(t, "1000", got.fixesAt, "the rolled-back write keeps the old stamp")
}

func TestRefreshSourcesBeforePersistGuardsEveryWrite(t *testing.T) {
	root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

	var seen []string

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal, Fetch: staticFetch(ghPageJSON, nil),
		BeforePersist: func(source string) error {
			seen = append(seen, source)

			if source == SourceReviews {
				return context.Canceled
			}

			return nil
		},
	})
	require.ErrorIs(t, err, context.Canceled)

	assert.Equal(t, []string{SourceFixes, SourceReviews}, seen)
	assert.Equal(t, SourceRefreshed, res.Fixes.State)
	assert.Equal(t, SourceStopped, res.Reviews.State, "a context error from the hook is a stop")
	assert.Equal(t, CauseStopped, res.Reviews.Cause)
	assertReviewsKept(t, readEvidenceState(t, dbPath))

	// Any other hook error is a failure of that source.
	root, _ = sourceFixture(t, "git@github.com:o/r.git")
	conflict := errors.New("the database changed since the preview")

	res, err = RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesLocal,
		BeforePersist: func(string) error { return conflict },
	})
	require.ErrorIs(t, err, conflict)
	assert.Equal(t, SourceFailed, res.Fixes.State)
	assert.Equal(t, CauseRefused, res.Fixes.Cause)
}

// errRefused stands in for a caller's check that refuses the work.
var errRefused = errors.New("the workspace changed since the preview")

// removeDatabase deletes the database at dbPath with its SQLite side
// files, as a user who deletes .seamark/index.db does.
func removeDatabase(t *testing.T, dbPath string) {
	t.Helper()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Remove(dbPath + suffix)
		if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
	}
}

// TestRefreshSourcesRunsTheHookBeforeTheStoreOpens pins the order hook,
// then open. store.Open creates a missing database and migrates an
// older one, so a hook after the open would guard a changed database.
func TestRefreshSourcesRunsTheHookBeforeTheStoreOpens(t *testing.T) {
	noFetch := func(context.Context, string, string, reviews.Options) ([]byte, error) {
		t.Error("a refused fix phase must stop the refresh before GitHub")

		return nil, errors.New("unexpected fetch")
	}

	t.Run("mined fixes", func(t *testing.T) {
		root, _ := sourceFixture(t, "git@github.com:o/r.git")
		dbPath := filepath.Join(t.TempDir(), ".seamark", "index.db")

		var existed []bool

		res, err := RefreshSources(context.Background(), RefreshRequest{
			Root: root, DBPath: dbPath, Mode: SourcesReviewsAndLocal, Fetch: noFetch,
			BeforePersist: func(string) error {
				_, statErr := os.Stat(dbPath)
				existed = append(existed, statErr == nil)

				return errRefused
			},
		})
		require.ErrorIs(t, err, errRefused)

		assert.Equal(t, []bool{false}, existed, "the hook sees the database before the open")
		assert.Equal(t, SourceFailed, res.Fixes.State)
		assert.Equal(t, CauseRefused, res.Fixes.Cause)
		assert.Equal(t, SourceNotAttempted, res.Reviews.State)
		assert.NoDirExists(t, filepath.Dir(dbPath), "a refused open creates nothing")
	})

	t.Run("unmined fixes", func(t *testing.T) {
		// A directory without git gives an unmined result. Its store still
		// opens, so the hook still runs.
		root := t.TempDir()
		dbPath := filepath.Join(t.TempDir(), ".seamark", "index.db")

		var seen []string

		res, err := RefreshSources(context.Background(), RefreshRequest{
			Root: root, DBPath: dbPath, Mode: SourcesLocal,
			BeforePersist: func(source string) error {
				seen = append(seen, source)

				return errRefused
			},
		})
		require.ErrorIs(t, err, errRefused)

		assert.Equal(t, []string{SourceFixes}, seen)
		assert.Equal(t, CauseRefused, res.Fixes.Cause)
		assert.NoDirExists(t, filepath.Dir(dbPath), "a refused open creates nothing")

		// Without a hook the CLI behaviour stays: the store opens and the
		// source reads unavailable.
		res, err = RefreshSources(context.Background(), RefreshRequest{Root: root, DBPath: dbPath, Mode: SourcesLocal})
		require.NoError(t, err)

		assert.Equal(t, SourceUnavailable, res.Fixes.State)
		assert.Equal(t, CauseNoHistory, res.Fixes.Cause)
		assert.FileExists(t, dbPath)
	})

	t.Run("reviews", func(t *testing.T) {
		root, dbPath := sourceFixture(t, "git@github.com:o/r.git")

		// The database goes away during the fetch, and the hook refuses.
		fetch := func(context.Context, string, string, reviews.Options) ([]byte, error) {
			removeDatabase(t, dbPath)

			return []byte(ghPageJSON), nil
		}

		var existed []bool

		res, err := RefreshSources(context.Background(), RefreshRequest{
			Root: root, Mode: SourcesReviewsAndLocal, Fetch: fetch,
			BeforePersist: func(source string) error {
				if source != SourceReviews {
					return nil
				}

				_, statErr := os.Stat(dbPath)
				existed = append(existed, statErr == nil)

				return errRefused
			},
		})
		require.ErrorIs(t, err, errRefused)

		assert.Equal(t, []bool{false}, existed, "the hook sees the database before the open")
		assert.Equal(t, SourceRefreshed, res.Fixes.State)
		assert.Equal(t, SourceFailed, res.Reviews.State)
		assert.Equal(t, CauseRefused, res.Reviews.Cause)
		assert.NoFileExists(t, dbPath, "a refused review write creates no database")
	})
}

func TestSourceCauseNames(t *testing.T) {
	assert.Equal(t, "none", CauseNone.String())
	assert.Equal(t, "auth", CauseAuth.String())
	assert.Equal(t, "rate-limited", CauseRateLimited.String())
	assert.Equal(t, "store", CauseStore.String())
	assert.Equal(t, "refused", CauseRefused.String())
	assert.Equal(t, "SourceCause(99)", SourceCause(99).String())
}

func TestRefreshSourcesReportsTypedProgress(t *testing.T) {
	root, _ := sourceFixture(t, "git@github.com:o/r.git")

	var events []RefreshEvent

	_, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal, Fetch: staticFetch(ghPageJSON, nil),
		Progress: func(e RefreshEvent) { events = append(events, e) },
	})
	require.NoError(t, err)

	assert.Equal(t, []RefreshEvent{
		{Phase: PhaseMineFixes, Source: SourceFixes},
		{Phase: PhasePersist, Source: SourceFixes},
		{Phase: PhaseSourceDone, Source: SourceFixes, State: SourceRefreshed},
		{Phase: PhaseFetchReviews, Source: SourceReviews, Repository: "o/r"},
		{Phase: PhaseFetched, Source: SourceReviews, Repository: "o/r", Comments: 2, Kept: 2},
		{Phase: PhaseCluster, Source: SourceReviews, Repository: "o/r"},
		{Phase: PhasePersist, Source: SourceReviews},
		{Phase: PhaseSourceDone, Source: SourceReviews, State: SourceRefreshed},
	}, events)
}

func TestRefreshSourcesReasonIsOneSafeLine(t *testing.T) {
	root, _ := sourceFixture(t, "git@github.com:o/r.git")

	hostile := errors.New("gh api: \x1b]0;pwned\x07HTTP 500 GH_TOKEN=abc123\nsecond line")

	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, Mode: SourcesReviewsAndLocal, Fetch: staticFetch("", hostile),
	})
	require.NoError(t, err)

	reason := res.Reviews.Reason
	assert.NotContains(t, reason, "\x1b")
	assert.NotContains(t, reason, "\x07")
	assert.NotContains(t, reason, "\n")
	assert.NotContains(t, reason, "abc123")
	assert.Contains(t, reason, "[REDACTED]")
	require.ErrorIs(t, res.Reviews.Err, hostile, "the raw cause stays available for errors.Is")
}

func TestRefreshFixesReturnsTheMineCount(t *testing.T) {
	root, dbPath := sourceFixture(t, "")

	count, err := RefreshFixes(root, dbPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	count, err = RefreshFixes(t.TempDir(), filepath.Join(t.TempDir(), "index.db"), nil)
	require.NoError(t, err)
	assert.Zero(t, count, "an unmined source prints 0, as the CLI always has")
}

func TestSourceModeNames(t *testing.T) {
	assert.Equal(t, "local", SourcesLocal.String())
	assert.Equal(t, "reviews-and-local", SourcesReviewsAndLocal.String())
	assert.Equal(t, "SourceMode(7)", SourceMode(7).String())
}

// writeTree writes each file of files under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// calleeFQNs lists the FQNs that the symbol found by query calls.
func calleeFQNs(t *testing.T, st *store.Store, query string) map[string]string {
	t.Helper()

	callees, err := st.Callees(mustFind(t, st, query).ID)
	require.NoError(t, err)

	out := make(map[string]string, len(callees))
	for _, c := range callees {
		out[c.FQN] = c.Origin
	}

	return out
}

// TestGoTestCallerSkipsTestMethodsOfOtherDirectories proves the Go test
// visibility rule of the name-match tier. A _test.go method of another
// directory can never be the target, so it must not make a call ambiguous.
func TestGoTestCallerSkipsTestMethodsOfOtherDirectories(t *testing.T) {
	snapFiles := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"snap/snap.go": `package snap

type Snapshot struct{}

func (s *Snapshot) Err() error { return nil }
`,
		"snap/snap_test.go": `package snap

import "testing"

func TestErr(t *testing.T) {
	s := &Snapshot{}
	_ = s.Err()
}
`,
		"fake/fake_test.go": `package fake

type fakeCtx struct{}

func (fakeCtx) Err() error { return nil }
`,
	}

	t.Run("other directory test method is skipped", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, snapFiles)
		writeTree(t, root, map[string]string{
			// Root files share the directory ".", so the edge must exist.
			"a_test.go": `package m

import "testing"

func TestPing(t *testing.T) {
	var x pinger
	x.Ping()
}
`,
			"b_test.go": `package m

type pinger struct{}

func (pinger) Ping() {}
`,
			// The rule limits test methods only. A production method stays
			// visible to a test file of any directory.
			"other/other_test.go": `package other

import (
	"testing"

	"example.com/m/snap"
)

func TestOtherErr(t *testing.T) {
	s := &snap.Snapshot{}
	_ = s.Err()
}
`,
		})

		_, st := runIndex(t, root)

		callees := calleeFQNs(t, st, "snap.TestErr")
		assert.Equal(t, model.OriginUniqueName, callees["snap.Snapshot.Err"], "callees: %v", callees)

		fakeErr := mustFind(t, st, "fake.fakeCtx.Err")
		callers, err := st.Callers(fakeErr.ID)
		require.NoError(t, err)
		assert.Empty(t, callers, "a test method of another directory is never the target")

		assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "TestPing")["pinger.Ping"],
			"root test files count as one directory")

		otherCallees := calleeFQNs(t, st, "TestOtherErr")
		assert.Equal(t, model.OriginUniqueName, otherCallees["snap.Snapshot.Err"],
			"a test caller of another directory still resolves to the production method: %v", otherCallees)
	})

	t.Run("non-test file in a test directory skips test methods", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, snapFiles)
		writeTree(t, root, map[string]string{
			// helpers.go is test code by its directory, but Go builds it
			// without the _test.go files beside it. So the method of
			// double_test.go is never its target.
			"test/e2e/helpers.go": `package e2e

import "example.com/m/snap"

func CheckErr(s *snap.Snapshot) error {
	return s.Err()
}
`,
			"test/e2e/double_test.go": `package e2e

type double struct{}

func (double) Err() error { return nil }
`,
		})

		_, st := runIndex(t, root)

		callees := calleeFQNs(t, st, "CheckErr")
		assert.Equal(t, model.OriginUniqueName, callees["snap.Snapshot.Err"], "callees: %v", callees)
	})

	t.Run("same directory test double keeps the call ambiguous", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, snapFiles)
		writeTree(t, root, map[string]string{
			"snap/double_test.go": `package snap

type double struct{}

func (double) Err() error { return nil }
`,
		})

		_, st := runIndex(t, root)

		for fqn := range calleeFQNs(t, st, "snap.TestErr") {
			assert.NotContains(t, fqn, ".Err", "two visible Err methods give no edge")
		}
	})
}

// TestPythonTestCallerKeepsTestMethodsOfOtherDirectories proves that the
// Go visibility rule stays Go only. A Python test file can import a helper
// from another test directory, so that helper still makes a call ambiguous.
func TestPythonTestCallerKeepsTestMethodsOfOtherDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"tests/a/test_x.py": `def test_ping(obj):
    obj.ping()
`,
		"api/svc.py": `class Service:
    def ping(self):
        return 1
`,
		"tests/b/test_y.py": `class Helper:
    def ping(self):
        return 2
`,
	})

	_, st := runIndex(t, root)

	assert.Empty(t, calleeFQNs(t, st, "tests/a/test_x.test_ping"), "two ping methods give no edge")
}

// effectTags lists the effect tags of the symbol found by query.
func effectTags(t *testing.T, st *store.Store, query string) []string {
	t.Helper()

	effs, err := st.EffectsForSymbol(mustFind(t, st, query).ID)
	require.NoError(t, err)

	tags := make([]string, 0, len(effs))
	for _, e := range effs {
		tags = append(tags, e.Tag)
	}

	return tags
}

// TestGoCommonMethodNeedsPackageEvidence proves the evidence rule of the
// name-match tier. A common standard-library method name keeps its guess
// only when the calling file shares the method's directory or imports its
// package. A false guess would give the target's effects to the caller.
func TestGoCommonMethodNeedsPackageEvidence(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"pin/pin.go": `package pin

import "os"

type writer struct{}

func (writer) Replace(s string) { _ = os.WriteFile(s, nil, 0o600) }

func (writer) SetStatus() { _ = os.WriteFile("status", nil, 0o600) }

func files() writer { return writer{} }

func Commit() { files().Replace("x") }
`,
		"ui/ui.go": `package ui

import "strings"

func OneLine(s string) string {
	r := strings.NewReplacer("\n", " ")
	return r.Replace(s)
}
`,
		"cli/cli.go": `package cli

import "example.com/m/pin"

var _ = pin.Commit

func Write(w interface{ Replace(string) }) { w.Replace("x") }
`,
		"blank/b.go": `package blank

import _ "example.com/m/pin"

func Write(w interface{ Replace(string) }) { w.Replace("x") }
`,
		"led/led.go": `package led

type Ledger interface{ SetStatus() }

func Move(ls Ledger) { ls.SetStatus() }
`,
		"snap.go": `package m

type Snapshot struct{}

func (s *Snapshot) Err() error { return nil }
`,
		"sub/a.go": `package sub

import "context"

func A(ctx context.Context) error { return ctx.Err() }
`,
		"sub/b.go": `package sub

import "example.com/m"

func B(s *m.Snapshot) error { return s.Err() }
`,
		// An empty import path maps to the root key "" too. It is no
		// import of the module root, so it is no evidence.
		"sub/c.go": `package sub

import ""

func C(e interface{ Err() error }) error { return e.Err() }
`,
	})

	_, st := runIndex(t, root)

	assert.Empty(t, calleeFQNs(t, st, "ui.OneLine"), "a strings.Replacer call is no call into pin")
	assert.Empty(t, effectTags(t, st, "ui.OneLine"), "no false effect flows into the text helper")

	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "pin.Commit")["pin.writer.Replace"],
		"the method's own directory is evidence")
	assert.Equal(t, []string{"fs:write"}, effectTags(t, st, "pin.Commit"))

	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "cli.Write")["pin.writer.Replace"],
		"an import of the method's package is evidence")
	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "blank.Write")["pin.writer.Replace"],
		"a blank import is evidence too")

	// A name outside the common table keeps today's guess without evidence:
	// interface dispatch into another package is a real call.
	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "led.Move")["pin.writer.SetStatus"])
	assert.Equal(t, []string{"fs:write"}, effectTags(t, st, "led.Move"))

	// The root package key is "". Only an import of the module root counts.
	assert.Empty(t, calleeFQNs(t, st, "sub.A"), "a context call is no call into the root package")
	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "sub.B")["Snapshot.Err"],
		"an import of the module root is evidence")
	assert.Empty(t, calleeFQNs(t, st, "sub.C"), "an empty import path is no evidence")
}

// TestGoCommonMethodEvidenceIsPerFile proves that the package evidence of a
// common method name comes from the imports of the calling file. The
// receiver tier passes them on, so a promoted method keeps its guess. An
// import in another file of the same directory is no evidence. That rule
// loses some real calls on purpose, because a directory-wide rule restores
// many more false guesses.
func TestGoCommonMethodEvidenceIsPerFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"buf/buf.go": `package buf

import "os"

type Buffer struct{}

func (*Buffer) WriteString(s string) { _ = os.WriteFile(s, nil, 0o600) }

type Base struct{}

func (Base) Flush() { _ = os.WriteFile("flush", nil, 0o600) }
`,
		"emb/emb.go": `package emb

import "example.com/m/buf"

type T struct{ buf.Base }

func (t T) Done() { t.Flush() }
`,
		"pair/pair.go": `package pair

import "example.com/m/buf"

type Pair struct {
	buf.Base
	Data *buf.Buffer
}
`,
		// use.go imports nothing, so its calls have no package evidence.
		"pair/use.go": `package pair

func Fill(p *Pair) { p.Data.WriteString("x") }

func (p Pair) Stop() { p.Flush() }
`,
	})

	_, st := runIndex(t, root)

	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "emb.T.Done")["buf.Base.Flush"],
		"a promoted method keeps its guess through the imports of the calling file")
	assert.Equal(t, []string{"fs:write"}, effectTags(t, st, "emb.T.Done"))

	assert.Empty(t, calleeFQNs(t, st, "pair.Fill"), "the import of pair.go is no evidence for a field call in use.go")
	assert.Empty(t, calleeFQNs(t, st, "pair.Pair.Stop"), "the import of pair.go is no evidence for a promoted call in use.go")
}

// TestCommonMethodNameNeedsNoEvidenceOutsideGo proves that the evidence rule
// for common Go method names stays Go only. A Python or ECMA method with
// such a name keeps its guess from another directory without an import,
// because an import is weak evidence in those languages.
func TestCommonMethodNameNeedsNoEvidenceOutsideGo(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"lib/conn.py": `class Conn:
    def Close(self):
        return 1
`,
		"app/stop.py": `def stop(c):
    c.Close()
`,
		"web/sink.ts": `export class Sink {
	Flush(): number { return 1; }
}
`,
		"app/drain.ts": `export function drain(s: any) {
	s.Flush();
}
`,
	})

	_, st := runIndex(t, root)

	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "app/stop.stop")["lib/conn.Conn.Close"],
		"a Python method named Close keeps its guess")
	assert.Equal(t, model.OriginUniqueName, calleeFQNs(t, st, "app/drain.drain")["web/sink.Sink.Flush"],
		"an ECMA method named Flush keeps its guess")
}

// partnerFiles lists the co-change partners of file in the database at
// dbPath, keyed by path, at any lift.
func partnerFiles(t *testing.T, dbPath, file string) map[string]store.CoChangePartner {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	partners, err := st.CoChangePartners(file, 0, 100)
	require.NoError(t, err)

	byFile := make(map[string]store.CoChangePartner, len(partners))
	for _, p := range partners {
		byFile[p.File] = p
	}

	return byFile
}

// goneList returns the paths of the stored gone list of the database at
// dbPath. The list holds a hex key for each path, so the helper decodes
// the keys.
func goneList(t *testing.T, dbPath string) []string {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	stored, err := st.GetMeta(store.MetaGoneFiles)
	require.NoError(t, err)

	var keys []string
	require.NoError(t, json.Unmarshal([]byte(stored), &keys))

	paths := make([]string, 0, len(keys))

	for _, k := range keys {
		b, err := hex.DecodeString(k)
		require.NoError(t, err)

		paths = append(paths, string(b))
	}

	return paths
}

// hotFileNames lists the files that the orient hubs read from the
// database at dbPath.
func hotFileNames(t *testing.T, dbPath string) []string {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	hot, err := st.HotFiles(100)
	require.NoError(t, err)

	names := make([]string, 0, len(hot))
	for _, h := range hot {
		names = append(names, h.File)
	}

	return names
}

func TestIndexKeepsLivePartnersOfGoneFiles(t *testing.T) {
	root := t.TempDir()

	// Every partner changes with pkg/a.go in three commits. Each later
	// step removes some of them in a different way.
	partners := []string{
		"pkg/live.go",              // stays
		"pkg/unstaged.go",          // deleted, not staged
		"pkg/staged.go",            // deleted with git rm
		"pkg/committed.go",         // deleted in a commit
		"pkg/moved.go",             // renamed with git mv, not committed
		"pkg/cmoved.go",            // renamed in a commit
		"pkg/testdata/a.golden",    // under a skipped directory
		"pkg/testdata/gone.golden", // under a skipped directory, deleted, not staged
		"pkg/gen.go",               // generated, so not parsed
		"README.md",                // no parser
		"pkg/sparse.go",            // skip-worktree, missing from disk
		"AUTHORS.old",              // deleted in a commit, sorts before every partner
	}

	body := func(file string, v int) string {
		switch {
		case file == "pkg/gen.go":
			return fmt.Sprintf("// Code generated by hand. DO NOT EDIT.\n\npackage pkg\n\nvar Gen = %d\n", v)
		case strings.HasSuffix(file, ".go"):
			name := strings.TrimSuffix(filepath.Base(file), ".go")

			return fmt.Sprintf("package pkg\n\nfunc %s%d() {}\n", strings.ToUpper(name[:1])+name[1:], v)
		default:
			return fmt.Sprintf("version %d\n", v)
		}
	}

	gitIn(t, root, "init", "-b", "main")

	for v := 1; v <= 3; v++ {
		writeRepoFile(t, root, "pkg/a.go", fmt.Sprintf("package pkg\n\nfunc A%d() {}\n", v))

		for _, f := range partners {
			writeRepoFile(t, root, f, body(f, v))
		}

		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", fmt.Sprintf("change %d", v))
	}

	// Commits without pkg/a.go keep the lift of its pairs above one.
	for v := 1; v <= 3; v++ {
		writeRepoFile(t, root, "docs/notes.txt", fmt.Sprintf("notes %d\n", v))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", fmt.Sprintf("notes %d", v))
	}

	gitIn(t, root, "rm", "-q", "pkg/committed.go", "AUTHORS.old")
	gitIn(t, root, "mv", "pkg/cmoved.go", "pkg/crenamed.go")
	gitIn(t, root, "commit", "-q", "-m", "remove and rename")

	first, err := Run(Options{Root: root, Logf: t.Logf})
	require.NoError(t, err)
	require.True(t, first.HistoryMined)

	before := partnerFiles(t, first.DBPath, "pkg/a.go")
	require.Contains(t, before, "pkg/live.go")
	assert.NotContains(t, before, "pkg/committed.go", "a file deleted in a commit is no partner")
	assert.NotContains(t, before, "pkg/cmoved.go", "the old path of a committed rename is no partner")

	// Mining sorts the files of a commit, so a path that sorts first is
	// only ever the first file of its pairs.
	assert.NotContains(t, before, "AUTHORS.old", "a gone file that sorts first is no partner")
	assert.Equal(t, []string{"AUTHORS.old", "pkg/cmoved.go", "pkg/committed.go"}, goneList(t, first.DBPath))

	// The partners that stay in the workspace through every step, with
	// their run-1 numbers as seen from each other file.
	live := []string{"pkg/a.go", "pkg/live.go", "pkg/testdata/a.golden", "pkg/gen.go", "README.md", "pkg/sparse.go"}
	firstPartners := map[string]map[string]store.CoChangePartner{}

	for _, f := range append(slices.Clone(live), partners...) {
		firstPartners[f] = partnerFiles(t, first.DBPath, f)
	}

	// Now the working tree removes files in every uncommitted way.
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "unstaged.go")))
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "testdata", "gone.golden")))
	gitIn(t, root, "rm", "-q", "pkg/staged.go")
	gitIn(t, root, "mv", "pkg/moved.go", "pkg/renamed.go")
	gitIn(t, root, "update-index", "--skip-worktree", "pkg/sparse.go")
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "sparse.go")))

	second, err := Run(Options{Root: root, Logf: t.Logf})
	require.NoError(t, err)

	// The refresh that tools run mines history again and reuses the parse
	// cache, so no --force is needed for the gone list to change.
	assert.False(t, second.Skipped)
	assert.True(t, second.HistoryMined)
	assert.Less(t, second.FilesReparsed, second.FilesParsed)

	// The deletion rule ignores skipped directories, so a deleted golden
	// file is gone too, although the index never parses it.
	goneFiles := []string{
		"AUTHORS.old", "pkg/cmoved.go", "pkg/committed.go", "pkg/moved.go", "pkg/staged.go",
		"pkg/testdata/gone.golden", "pkg/unstaged.go",
	}

	assert.Equal(t, goneFiles, goneList(t, second.DBPath))

	after := partnerFiles(t, second.DBPath, "pkg/a.go")
	hubs := hotFileNames(t, second.DBPath)

	for _, gone := range goneFiles {
		assert.NotContains(t, after, gone, "%s is gone from the workspace", gone)
		assert.NotContains(t, hubs, gone, "the orient hubs leave out %s", gone)

		// A gone file keeps exactly its live partners, with the numbers
		// of run 1, because an agent that deletes it still needs them.
		want := map[string]store.CoChangePartner{}
		for _, f := range live {
			want[f] = firstPartners[gone][f]
		}

		assert.Equal(t, want, partnerFiles(t, second.DBPath, gone), "%s keeps its live partners", gone)
	}

	// Presence is a git question, not a parse question.
	for _, kept := range []string{"pkg/testdata/a.golden", "pkg/gen.go", "README.md", "pkg/sparse.go"} {
		assert.Contains(t, after, kept, "%s is still in the workspace", kept)
	}

	// The filter runs at read time, so the gone files still count in the
	// numbers of a surviving pair, and the pair count stays the same.
	assert.Equal(t, before["pkg/live.go"], after["pkg/live.go"])
	assert.Equal(t, first.Stats.CoChanges, second.Stats.CoChanges)

	// A full refresh gives the same result as the incremental one.
	third, err := Run(Options{Root: root, Force: true, Logf: t.Logf})
	require.NoError(t, err)
	assert.Equal(t, goneList(t, second.DBPath), goneList(t, third.DBPath))
	assert.Equal(t, second.Stats.CoChanges, third.Stats.CoChanges)

	for _, f := range append(slices.Clone(live), goneFiles...) {
		assert.Equal(t, partnerFiles(t, second.DBPath, f), partnerFiles(t, third.DBPath, f), "partners of %s", f)
	}

	assert.Equal(t, hubs, hotFileNames(t, third.DBPath))

	// A file that comes back stops being gone.
	gitIn(t, root, "checkout", "--", "pkg/unstaged.go")

	fourth, err := Run(Options{Root: root, Logf: t.Logf})
	require.NoError(t, err)
	assert.False(t, fourth.Skipped)
	assert.NotContains(t, goneList(t, fourth.DBPath), "pkg/unstaged.go")
	assert.Equal(t, before["pkg/unstaged.go"], partnerFiles(t, fourth.DBPath, "pkg/a.go")["pkg/unstaged.go"])
	assert.Equal(t, first.Stats.CoChanges, fourth.Stats.CoChanges)
}

// configGit runs git in root with a fixed identity and no global or
// system config.
func configGit(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// constantDiffScript writes an executable script that prints a constant
// line and touches marker when it runs. It stands in for an external diff
// program and a textconv driver.
func constantDiffScript(t *testing.T) (script, marker string) {
	t.Helper()

	dir := t.TempDir()
	script = filepath.Join(dir, "const.sh")
	marker = filepath.Join(dir, "ran")
	body := "#!/bin/sh\ntouch '" + marker + "'\necho constant\n"

	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	return script, marker
}

func TestWorkspaceStateIgnoresGitConfig(t *testing.T) {
	// The fingerprint decides whether the index is current. A config that
	// changes the diff or status text must not change it, or a second edit
	// of a dirty file or a new file can read as no change.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	script, marker := constantDiffScript(t)

	cases := []struct {
		name   string
		config [][2]string
		attrs  string
		env    [2]string
	}{
		{name: "diff.external", config: [][2]string{{"diff.external", script}}},
		{name: "GIT_EXTERNAL_DIFF", env: [2]string{"GIT_EXTERNAL_DIFF", script}},
		{
			name:   "textconv",
			config: [][2]string{{"diff.hide.textconv", script}},
			attrs:  "*.go diff=hide\n",
		},
		{name: "color.ui always", config: [][2]string{{"color.ui", "always"}}},
		{name: "status.showUntrackedFiles no", config: [][2]string{{"status.showUntrackedFiles", "no"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			tracked := filepath.Join(root, "main.go")
			untracked := filepath.Join(root, "new.go")
			stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

			require.NoError(t, os.WriteFile(tracked, []byte("package main\n"), 0o644))
			configGit(t, root, "init", "-q", "-b", "main")
			configGit(t, root, "add", "-A")
			configGit(t, root, "commit", "-q", "-m", "initial")

			// The three states: a dirty file, a second edit of it, and a
			// new untracked file next to it.
			states := []func(){
				func() {
					require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc one() {}\n"), 0o644))
					_ = os.Remove(untracked)
				},
				func() {
					require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc two() {}\n"), 0o644))
					_ = os.Remove(untracked)
				},
				func() {
					// The fingerprint holds the mtime of an untracked file, so
					// each write sets the same mtime.
					require.NoError(t, os.WriteFile(untracked, []byte("package main\n\nfunc three() {}\n"), 0o644))
					require.NoError(t, os.Chtimes(untracked, stamp, stamp))
				},
			}

			want := make([]string, len(states))

			for i, set := range states {
				set()

				want[i] = WorkspaceState(root)
				require.NotEmpty(t, want[i])
			}

			require.NotEqual(t, want[0], want[1], "a second edit changes the fingerprint")
			require.NotEqual(t, want[1], want[2], "a new file changes the fingerprint")

			for _, kv := range c.config {
				configGit(t, root, "config", kv[0], kv[1])
			}

			if c.attrs != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte(c.attrs), 0o644))
			}

			if c.env[0] != "" {
				t.Setenv(c.env[0], c.env[1])
			}

			_ = os.Remove(marker)

			for i, set := range states {
				set()
				assert.Equal(t, want[i], WorkspaceState(root), "state %d", i)
			}

			assert.NoFileExists(t, marker, "no external program runs")
		})
	}
}

func TestIndexSeesSecondEditUnderExternalDiff(t *testing.T) {
	// An external diff prints the same text for every edit. The fast path
	// must still see a second edit of a dirty file and index the new symbol.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	script, marker := constantDiffScript(t)
	root := t.TempDir()
	writeFixture(t, root)

	configGit(t, root, "init", "-q", "-b", "main")
	configGit(t, root, "add", "-A")
	configGit(t, root, "commit", "-q", "-m", "initial")
	configGit(t, root, "config", "diff.external", script)

	tracked := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc main() {}\n\nfunc Delta() {}\n"), 0o644))

	first, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, first.Skipped)

	require.NoError(t, os.WriteFile(tracked, []byte("package main\n\nfunc main() {}\n\nfunc Epsilon() {}\n"), 0o644))

	second, st := runIndex(t, root)
	assert.False(t, second.Skipped, "a second edit of a dirty file rebuilds")
	assert.Equal(t, "Epsilon", mustFind(t, st, "Epsilon").Name)
	assert.NoFileExists(t, marker, "no external program runs")
}

// gitIndexState is what a write to .git/index changes: the file identity,
// which a rename over the file replaces, the mtime, and the bytes.
type gitIndexState struct {
	info  os.FileInfo
	bytes []byte
}

// readGitIndex reads the gitIndexState of the repository at root.
func readGitIndex(t *testing.T, root string) gitIndexState {
	t.Helper()

	p := filepath.Join(root, ".git", "index")

	info, err := os.Stat(p)
	require.NoError(t, err)

	data, err := os.ReadFile(p)
	require.NoError(t, err)

	return gitIndexState{info: info, bytes: data}
}

// assertGitIndexKept fails when .git/index differs from before, or when
// a git process left .git/index.lock behind.
func assertGitIndexKept(t *testing.T, root string, before gitIndexState, msg string) {
	t.Helper()

	after := readGitIndex(t, root)

	assert.True(t, os.SameFile(before.info, after.info), "%s: .git/index has a new inode", msg)
	assert.Equal(t, before.info.ModTime(), after.info.ModTime(), "%s: .git/index has a new mtime", msg)
	assert.Equal(t, before.bytes, after.bytes, "%s: .git/index has new bytes", msg)
	assert.NoFileExists(t, filepath.Join(root, ".git", "index.lock"), msg)
}

// touchOlder gives the file at p an older mtime and keeps its content. A
// time in the past differs from the stat cache in .git/index, so the
// test needs no sleep to make git see a touch.
func touchOlder(t *testing.T, p string, age time.Duration) {
	t.Helper()

	at := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(p, at, at))
}

// clearGitIndexEnv removes the inherited variables that already stop git
// from writing .git/index, so a plain git command can show the write.
// t.Setenv restores them after the test.
func clearGitIndexEnv(t *testing.T) {
	t.Helper()

	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_OPTIONAL_LOCKS" || strings.HasPrefix(k, "GIT_CONFIG_") {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// gitIndexRepo builds a committed repository with a real change in
// main.go. A plain git status then settles the stat cache, so a later
// write comes only from a new touch.
func gitIndexRepo(t *testing.T) string {
	t.Helper()

	clearGitIndexEnv(t)

	root := t.TempDir()
	writeFixture(t, root)
	configGit(t, root, "init", "-q", "-b", "main")
	configGit(t, root, "add", "-A")
	configGit(t, root, "commit", "-q", "-m", "initial")

	main := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(main, []byte("package main\n\nfunc main() {}\n"), 0o644))
	touchOlder(t, main, 3*time.Hour)
	configGit(t, root, "status", "--porcelain")

	return root
}

func TestWorkspaceStateKeepsGitIndex(t *testing.T) {
	// A touch changes no content. The freshness check must read the
	// workspace without a rewrite of .git/index, because the rewrite takes
	// .git/index.lock and a concurrent git command of the user then fails.
	root := gitIndexRepo(t)
	touched := filepath.Join(root, "internal", "util", "util.go")

	fingerprint := WorkspaceState(root)
	require.NotEmpty(t, fingerprint)

	touchOlder(t, touched, 2*time.Hour)

	before := readGitIndex(t, root)

	assert.Equal(t, fingerprint, WorkspaceState(root), "a touch changes the fingerprint")
	assert.Equal(t, fingerprint, WorkspaceState(root), "a second call changes the fingerprint")
	assertGitIndexKept(t, root, before, "WorkspaceState")

	// A plain git status refreshes the stat cache. The fingerprint stays,
	// because only the output of git goes into it.
	configGit(t, root, "status", "--porcelain")
	assert.Equal(t, fingerprint, WorkspaceState(root), "a refreshed stat cache changes the fingerprint")

	// Controls: each plain command writes the index after a new touch, so
	// the fixture can show the write that WorkspaceState avoids.
	touchOlder(t, touched, 4*time.Hour)

	before = readGitIndex(t, root)

	configGit(t, root, "status", "--porcelain")
	require.NotEqual(t, before.bytes, readGitIndex(t, root).bytes, "a plain git status must write the index")

	touchOlder(t, touched, 5*time.Hour)

	before = readGitIndex(t, root)

	t.Setenv("GIT_OPTIONAL_LOCKS", "0")
	configGit(t, root, "status", "--porcelain")
	assertGitIndexKept(t, root, before, "git status with GIT_OPTIONAL_LOCKS=0")
	configGit(t, root, "diff", "HEAD")
	require.NotEqual(t, before.bytes, readGitIndex(t, root).bytes,
		"git diff HEAD must write the index although GIT_OPTIONAL_LOCKS=0")
}

func TestIndexRunKeepsGitIndex(t *testing.T) {
	// index.Run reaches WorkspaceState on its fast path and on a build.
	// Neither path may rewrite .git/index after a touch.
	t.Run("fast path", func(t *testing.T) {
		root := gitIndexRepo(t)

		first, err := Run(Options{Root: root})
		require.NoError(t, err)
		require.False(t, first.Skipped)

		touchOlder(t, filepath.Join(root, "internal", "util", "util.go"), 2*time.Hour)

		before := readGitIndex(t, root)

		second, err := Run(Options{Root: root})
		require.NoError(t, err)
		assert.True(t, second.Skipped, "a touch must keep the fast path")
		assertGitIndexKept(t, root, before, "the fast path")
	})

	t.Run("build", func(t *testing.T) {
		root := gitIndexRepo(t)

		touchOlder(t, filepath.Join(root, "internal", "util", "util.go"), 2*time.Hour)

		before := readGitIndex(t, root)

		sum, err := Run(Options{Root: root})
		require.NoError(t, err)
		assert.False(t, sum.Skipped)
		assertGitIndexKept(t, root, before, "the build")
	})
}
