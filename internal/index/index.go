// Package index orchestrates a full indexing pass: walk the workspace,
// parse supported files, resolve call references into edges, mine git
// history, and write everything into the store atomically.
package index

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/parse"
	"github.com/seamark-dev/seamark/internal/store"
)

// Options configures one indexing run.
type Options struct {
	// Root is the workspace directory. When inside a git repository it is
	// widened to the repository toplevel so paths are repo-relative.
	Root string
	// DBPath overrides the index location (default: <root>/.seamark/index.db).
	DBPath string
	// History tunes the git mining pass.
	History history.Options
	// Logf receives progress and warnings; nil discards them.
	Logf func(format string, args ...any)
}

// Summary reports what a run produced.
type Summary struct {
	Root         string
	DBPath       string
	FilesSeen    int // files listed in the workspace
	FilesParsed  int // files a language extractor handled
	ParseErrors  int
	HistoryMined bool
	// HistorySkipNote says why the history layer is absent when
	// HistoryMined is false: "not a git repository" and "mining failed"
	// are different situations and must not be conflated in output.
	HistorySkipNote string
	Stats           store.Stats
	Duration        time.Duration
}

// Run executes a full indexing pass.
func Run(opts Options) (*Summary, error) {
	start := time.Now()
	logf := opts.Logf

	if logf == nil {
		logf = func(string, ...any) {}
	}

	root, err := resolveRoot(opts.Root)
	if err != nil {
		return nil, err
	}

	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	files, err := listFiles(root)
	if err != nil {
		return nil, err
	}

	registry, err := parse.NewRegistry()
	if err != nil {
		return nil, err
	}

	defer registry.Close()

	sum := &Summary{Root: root, DBPath: dbPath, FilesSeen: len(files)}

	var results []*parse.FileResult

	for _, rel := range files {
		ex := registry.ForPath(rel)
		if ex == nil {
			continue
		}

		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			logf("warn: read %s: %v", rel, err)
			sum.ParseErrors++
			continue
		}

		res, err := ex.Extract(rel, src)
		if err != nil {
			logf("warn: parse %s: %v", rel, err)
			sum.ParseErrors++
			continue
		}

		results = append(results, res)
		sum.FilesParsed++
	}

	// History is best-effort: a non-git workspace still gets a structure graph.
	var decisions []model.Decision
	var pairs []model.CoChange

	if history.IsRepo(root) {
		decisions, pairs, err = history.Mine(root, opts.History)
		if err != nil {
			logf("warn: history mining failed: %v", err)

			sum.HistorySkipNote = "history mining failed, see warnings"
		} else {
			sum.HistoryMined = true
		}
	} else {
		logf("note: %s is not a git repository; skipping history layer", root)

		sum.HistorySkipNote = "not a git repository"
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }() // read results are already committed

	g := buildGraph(results, moduleName(root))

	err = st.Rebuild(func(tx *store.Tx) error {
		if err := g.write(tx); err != nil {
			return err
		}

		for i := range decisions {
			if err := tx.InsertDecision(&decisions[i]); err != nil {
				return err
			}
		}

		for _, p := range pairs {
			if err := tx.InsertCoChange(p); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	for k, v := range map[string]string{
		"repo_root":  root,
		"indexed_at": fmt.Sprint(time.Now().Unix()),
	} {
		if err := st.SetMeta(k, v); err != nil {
			return nil, err
		}
	}

	if sum.Stats, err = st.Stats(); err != nil {
		return nil, err
	}

	sum.Duration = time.Since(start)

	return sum, nil
}

// resolveRoot widens to the git toplevel when inside a repository.
func resolveRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = abs

	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	return abs, nil
}

// skipDirs are never worth parsing: dependencies, outputs, fixtures, and
// seamark's own state.
var skipDirs = map[string]bool{
	".git": true, ".seamark": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "testdata": true, ".venv": true,
}

// listFiles returns repo-relative candidate paths. Inside git it trusts
// `git ls-files` (tracked + unignored untracked) so .gitignore is honored;
// otherwise it walks the tree with conservative skips.
func listFiles(root string) ([]string, error) {
	if history.IsRepo(root) {
		cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		cmd.Dir = root

		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("index: git ls-files: %w", err)
		}

		var files []string

		for f := range strings.SplitSeq(string(out), "\x00") {
			if f != "" && !underSkippedDir(f) {
				files = append(files, f)
			}
		}

		return files, nil
	}

	var files []string

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if skipDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".")) {
				if p != root {
					return filepath.SkipDir
				}
			}

			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		files = append(files, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index: walk %s: %w", root, err)
	}

	return files, nil
}

func underSkippedDir(rel string) bool {
	for seg := range strings.SplitSeq(path.Dir(rel), "/") {
		if skipDirs[seg] {
			return true
		}
	}

	return false
}

var moduleRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// moduleName reads the Go module path from go.mod, "" when absent. It lets
// the resolver translate absolute import paths into repo-relative packages.
func moduleName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}

	if m := moduleRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}

	return ""
}

// graph is the in-memory staging area between parsing and storage.
type graph struct {
	results    []*parse.FileResult
	modulePath string

	packages map[string]*model.Symbol // package key -> package symbol
	// declared symbols by (package, name); values point into results.
	decls map[string]map[string][]*model.Symbol
	// methods by bare name across the repo, for lowest-confidence resolution.
	methodsByName map[string][]*model.Symbol
}

func buildGraph(results []*parse.FileResult, modulePath string) *graph {
	g := &graph{
		results:       results,
		modulePath:    modulePath,
		packages:      map[string]*model.Symbol{},
		decls:         map[string]map[string][]*model.Symbol{},
		methodsByName: map[string][]*model.Symbol{},
	}
	for _, res := range results {
		g.packageSymbol(res.Package)

		for i := range res.Symbols {
			sym := &res.Symbols[i].Symbol
			byName := g.decls[res.Package]

			if byName == nil {
				byName = map[string][]*model.Symbol{}
				g.decls[res.Package] = byName
			}

			byName[sym.Name] = append(byName[sym.Name], sym)

			if sym.Kind == model.KindMethod {
				g.methodsByName[sym.Name] = append(g.methodsByName[sym.Name], sym)
			}
		}
		// Pre-create package symbols for import targets so insertion order
		// is deterministic and edges always have both endpoints.
		for _, imp := range res.Imports {
			g.packageSymbol(g.importKey(imp.Path))
		}
	}
	return g
}

// importKey maps an import path to a package-symbol key: repo-relative for
// internal packages, the import path itself for external ones.
func (g *graph) importKey(importPath string) string {
	if g.modulePath == "" {
		return importPath
	}

	if importPath == g.modulePath {
		return ""
	}

	if rel, ok := strings.CutPrefix(importPath, g.modulePath+"/"); ok {
		return rel
	}

	return importPath
}

// packageSymbol lazily creates the grouping symbol for a package key.
// The repo root package is named "." to keep FQNs non-empty.
func (g *graph) packageSymbol(key string) *model.Symbol {
	if sym, ok := g.packages[key]; ok {
		return sym
	}

	fqn := key
	if fqn == "" {
		fqn = "."
	}

	sym := &model.Symbol{FQN: fqn, Name: path.Base(fqn), Kind: model.KindPackage}
	g.packages[key] = sym

	return sym
}

// write inserts symbols, then resolves references into edges. Symbols must
// be inserted first so edge endpoints have IDs.
func (g *graph) write(tx *store.Tx) error {
	for _, key := range sortedKeys(g.packages) {
		if err := tx.InsertSymbol(g.packages[key]); err != nil {
			return err
		}
	}

	for _, res := range g.results {
		for i := range res.Symbols {
			if err := tx.InsertSymbol(&res.Symbols[i].Symbol); err != nil {
				return err
			}
		}
	}

	for _, res := range g.results {
		if err := g.writeImportEdges(tx, res); err != nil {
			return err
		}

		for i := range res.Symbols {
			if err := g.writeCallEdges(tx, res, &res.Symbols[i]); err != nil {
				return err
			}
		}
	}

	return nil
}

func (g *graph) writeImportEdges(tx *store.Tx, res *parse.FileResult) error {
	src := g.packages[res.Package]

	for _, imp := range res.Imports {
		dst := g.packages[g.importKey(imp.Path)]

		if src == nil || dst == nil || src == dst {
			continue
		}

		err := tx.InsertEdge(model.Edge{
			Src: src.ID, Dst: dst.ID,
			Kind: model.EdgeImports, Origin: model.OriginParse,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// writeCallEdges resolves the call references of one declaration, in
// descending confidence order (RFC §3: every edge declares its derivation):
//
//  1. bare identifier   → function in the same package    (same-package)
//  2. selector whose    → function in the imported repo   (qualified)
//     qualifier is an     package
//     import alias
//  3. any other         → the single repo symbol with     (unique-name)
//     selector call       that method name, if unique
//
// Known limitation, accepted for M1: resolution is purely syntactic, so a
// local variable shadowing an import alias (or sharing a method name) can
// produce a wrong edge. The origin column declares the derivation exactly
// so consumers can weigh it; proper scope tracking is future work.
func (g *graph) writeCallEdges(tx *store.Tx, res *parse.FileResult, decl *parse.SymbolDecl) error {
	imports := map[string]string{} // local name -> package key

	for _, imp := range res.Imports {
		if imp.LocalName != "" {
			imports[imp.LocalName] = g.importKey(imp.Path)
		}
	}

	for _, call := range decl.Calls {
		var dst *model.Symbol
		origin := ""

		if !call.Selector {
			// Bare identifier: only ever a same-package function. Selector
			// calls must not land here — x.db.Close() is a method call on a
			// value, not a reference into the package scope.
			dst = pickUnique(g.decls[res.Package][call.Name], model.KindFunction)
			origin = model.OriginSamePackage
		} else if pkgKey, ok := imports[call.Qualifier]; ok {
			dst = pickUnique(g.decls[pkgKey][call.Name], model.KindFunction)
			origin = model.OriginQualified
		} else if candidates := g.methodsByName[call.Name]; len(candidates) == 1 {
			dst = candidates[0]
			origin = model.OriginUniqueName
		}

		if dst == nil || dst == &decl.Symbol {
			continue
		}

		err := tx.InsertEdge(model.Edge{
			Src: decl.Symbol.ID, Dst: dst.ID,
			Kind: model.EdgeCalls, Origin: origin,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// pickUnique returns the sole symbol of the wanted kind, nil otherwise.
// Ambiguity (build-tag variants, redeclarations) yields no edge rather than
// a guessed one.
func pickUnique(candidates []*model.Symbol, kind model.SymbolKind) *model.Symbol {
	var found *model.Symbol
	for _, c := range candidates {
		if c.Kind != kind {
			continue
		}
		if found != nil {
			return nil
		}
		found = c
	}
	return found
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
