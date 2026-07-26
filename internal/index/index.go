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

	root, err := ResolveRoot(opts.Root)
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

	g := buildGraph(results, readGoModules(root, files))

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

// ResolveRoot widens to the git toplevel when inside a repository.
func ResolveRoot(root string) (string, error) {
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

// goModule maps one go.mod to its repo location.
type goModule struct {
	dir  string // repo-relative directory, "" for the root
	path string // module path declared in go.mod
}

// readGoModules parses every go.mod in the workspace. Monorepos nest Go
// modules (a Go ingestor inside a Python repo): imports must resolve
// against their OWN module, or every cross-package call in a nested
// module silently loses its edges. Longest module path first so nested
// modules shadow their parents.
func readGoModules(root string, files []string) []goModule {
	var mods []goModule

	for _, f := range files {
		if path.Base(f) != "go.mod" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}

		m := moduleRe.FindSubmatch(data)
		if m == nil {
			continue
		}

		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}

		mods = append(mods, goModule{dir: dir, path: string(m[1])})
	}

	sort.Slice(mods, func(i, j int) bool { return len(mods[i].path) > len(mods[j].path) })

	return mods
}

// graph is the in-memory staging area between parsing and storage.
type graph struct {
	results []*parse.FileResult
	modules []goModule

	// All keys below are scoped per language family (scopedKey): a Go
	// package directory and an ES module can share the same repo-relative
	// key ("web/api" the directory vs web/api.ts the file) without being
	// the same namespace — unscoped keys let a TS import resolve into Go
	// symbols with high confidence.
	packages map[string]*model.Symbol // scoped package key -> package symbol
	// declared symbols by (scoped package key, name).
	decls map[string]map[string][]*model.Symbol
	// methods by (family, bare name), for lowest-confidence resolution.
	// Family, not dialect: TS/TSX/JS share one namespace, and judging
	// uniqueness per dialect would fabricate edges a repo-wide duplicate
	// should suppress.
	methodsByName map[string]map[string][]*model.Symbol
}

// scopedKey namespaces a package key by language family.
func scopedKey(family, key string) string { return family + "\x00" + key }

func buildGraph(results []*parse.FileResult, modules []goModule) *graph {
	g := &graph{
		results:       results,
		modules:       modules,
		packages:      map[string]*model.Symbol{},
		decls:         map[string]map[string][]*model.Symbol{},
		methodsByName: map[string]map[string][]*model.Symbol{},
	}
	// A Python package's __init__.py and a sibling module can claim the
	// same key ("api/__init__.py" and "api.py" → "api"). Python gives the
	// package precedence and the module becomes unimportable; mirror that
	// by keeping the shadowed file's symbols out of the resolution maps.
	// (They are still stored as symbols — the code exists.)
	pyInitKeys := map[string]bool{}

	for _, res := range results {
		if res.Language == "python" && strings.HasSuffix(res.Path, "__init__.py") {
			pyInitKeys[res.Package] = true
		}
	}

	shadowed := func(res *parse.FileResult) bool {
		return res.Language == "python" &&
			!strings.HasSuffix(res.Path, "__init__.py") && pyInitKeys[res.Package]
	}

	for _, res := range results {
		fam := parse.LanguageFamily(res.Language)
		g.packageSymbol(fam, res.Package)

		for i := range res.Symbols {
			if shadowed(res) {
				continue
			}

			sym := &res.Symbols[i].Symbol
			sk := scopedKey(fam, res.Package)
			byName := g.decls[sk]

			if byName == nil {
				byName = map[string][]*model.Symbol{}
				g.decls[sk] = byName
			}

			byName[sym.Name] = append(byName[sym.Name], sym)

			if sym.Kind == model.KindMethod {
				methods := g.methodsByName[fam]
				if methods == nil {
					methods = map[string][]*model.Symbol{}
					g.methodsByName[fam] = methods
				}

				methods[sym.Name] = append(methods[sym.Name], sym)
			}
		}
		// Pre-create package symbols for import targets so insertion order
		// is deterministic and edges always have both endpoints.
		for _, imp := range res.Imports {
			if key, ok := g.importKeyOK(imp); ok {
				g.packageSymbol(fam, key)
			}
		}
	}
	return g
}

// importKeyOK returns the package key for an import; false for relative
// specifiers that escaped the repo, which must not materialize junk
// package nodes (their raw dotted/relative text is not a key).
func (g *graph) importKeyOK(imp parse.Import) (string, bool) {
	if imp.Resolved == "" && strings.HasPrefix(imp.Path, ".") {
		return "", false
	}

	return g.importKey(imp), true
}

// importKey maps an import to a package-symbol key: the extractor's own
// resolution when it has one (JS/TS relative specifiers), repo-relative
// translation for Go module paths, the raw path for external imports.
func (g *graph) importKey(imp parse.Import) string {
	if imp.Resolved != "" {
		return imp.Resolved
	}

	// Modules are sorted longest-path-first, so a nested module shadows
	// its parent for imports under its prefix.
	for _, m := range g.modules {
		if imp.Path == m.path {
			return m.dir
		}

		if rel, ok := strings.CutPrefix(imp.Path, m.path+"/"); ok {
			return path.Join(m.dir, rel)
		}
	}

	return imp.Path
}

// packageSymbol lazily creates the grouping symbol for a package key
// within one language family. The repo root package is named "." to keep
// FQNs non-empty.
func (g *graph) packageSymbol(family, key string) *model.Symbol {
	sk := scopedKey(family, key)
	if sym, ok := g.packages[sk]; ok {
		return sym
	}

	fqn := key
	if fqn == "" {
		fqn = "."
	}

	sym := &model.Symbol{FQN: fqn, Name: path.Base(fqn), Kind: model.KindPackage}
	g.packages[sk] = sym

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
		pkg := g.packages[scopedKey(parse.LanguageFamily(res.Language), res.Package)]

		for i := range res.Symbols {
			sym := &res.Symbols[i].Symbol
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}

			// DEFINES edges tie each symbol to its package/module node —
			// and disambiguate same-FQN package nodes across language
			// families (a Go dir and an ES module can share a key).
			if pkg != nil {
				err := tx.InsertEdge(model.Edge{
					Src: pkg.ID, Dst: sym.ID,
					Kind: model.EdgeDefines, Origin: model.OriginParse,
				})
				if err != nil {
					return err
				}
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
	fam := parse.LanguageFamily(res.Language)
	src := g.packages[scopedKey(fam, res.Package)]

	for _, imp := range res.Imports {
		key, ok := g.importKeyOK(imp)
		if !ok {
			continue
		}

		dst := g.packages[scopedKey(fam, key)]

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
//  1. bare identifier    → named import target module,     (qualified)
//     that is a named      else function in the same       (same-package)
//     import               package/module
//  2. selector whose     → function in the imported repo   (qualified)
//     qualifier is an      package/module
//     import alias or
//     namespace import
//  3. self/cls/this      → the method of the caller's      (same-class)
//     method call          own class, when declared there
//  4. any other          → the single same-family repo     (unique-name)
//     selector call        symbol with that method name
//
// Known limitation, accepted for M1: resolution is purely syntactic, so a
// local variable shadowing an import alias (or sharing a method name) can
// produce a wrong edge. The origin column declares the derivation exactly
// so consumers can weigh it; proper scope tracking is future work.
func (g *graph) writeCallEdges(tx *store.Tx, res *parse.FileResult, decl *parse.SymbolDecl) error {
	fam := parse.LanguageFamily(res.Language)

	imports := map[string]string{} // qualifier local name -> scoped package key
	named := map[string]string{}   // bare imported name -> scoped package key

	for _, imp := range res.Imports {
		rawKey, ok := g.importKeyOK(imp)
		if !ok {
			continue
		}

		key := scopedKey(fam, rawKey)
		if imp.LocalName != "" {
			imports[imp.LocalName] = key
		}

		for _, n := range imp.Named {
			named[n] = key
		}
	}

	// A function can reach the same destination through several call
	// sites resolved by different tiers; keep the best origin per target
	// so a same-class resolution is never reported as a name-match guess.
	best := map[int64]string{}

	for _, call := range decl.Calls {
		var dst *model.Symbol
		origin := ""

		switch {
		case !call.Selector:
			// Bare identifier: a named import wins over the local scope
			// (that is what the import means); otherwise a function in the
			// same package/module. Selector calls must not land here —
			// x.db.Close() is a method call on a value, not a reference
			// into the package scope.
			if pkgKey, ok := named[call.Name]; ok {
				dst = pickUnique(g.decls[pkgKey][call.Name], model.KindFunction)
				origin = model.OriginQualified
			} else {
				dst = pickUnique(g.decls[scopedKey(fam, res.Package)][call.Name], model.KindFunction)
				origin = model.OriginSamePackage
			}
		case call.Receiver && decl.Symbol.Kind == model.KindMethod:
			// The receiver shadows any same-named import, so this tier
			// comes first. The caller's FQN is <pkg>.<Class>.<name>; a
			// sibling method shares everything but the last segment.
			want := strings.TrimSuffix(decl.Symbol.FQN, "."+decl.Symbol.Name) + "." + call.Name

			for _, c := range g.decls[scopedKey(fam, res.Package)][call.Name] {
				if c.Kind == model.KindMethod && c.FQN == want {
					dst = c
					origin = model.OriginSameClass

					break
				}
			}

			// Miss (e.g. an inherited method): the guess tier may catch it.
			if dst == nil {
				dst, origin = g.uniqueMethod(fam, call.Name)
			}
		case call.Qualifier != "":
			if pkgKey, ok := imports[call.Qualifier]; ok {
				// A known import qualifier that does not resolve is a call
				// into an external module — never fall through to a guess.
				dst = pickUnique(g.decls[pkgKey][call.Name], model.KindFunction)
				origin = model.OriginQualified
			} else {
				dst, origin = g.uniqueMethod(fam, call.Name)
			}
		default:
			// Complex operand (x.db.Close()): only the guess tier applies.
			dst, origin = g.uniqueMethod(fam, call.Name)
		}

		if dst == nil || dst.ID == decl.Symbol.ID {
			continue
		}

		if cur, seen := best[dst.ID]; !seen || originRank(origin) > originRank(cur) {
			best[dst.ID] = origin
		}
	}

	for dstID, origin := range best {
		err := tx.InsertEdge(model.Edge{
			Src: decl.Symbol.ID, Dst: dstID,
			Kind: model.EdgeCalls, Origin: origin,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// originRank orders derivations by confidence for edge dedup: any resolved
// tier beats the name-match guess.
func originRank(origin string) int {
	if origin == model.OriginUniqueName {
		return 1
	}

	return 2
}

// uniqueMethod is the lowest-confidence tier: the single method in the
// language family carrying the called name, if exactly one exists.
func (g *graph) uniqueMethod(fam, name string) (dst *model.Symbol, origin string) {
	if candidates := g.methodsByName[fam][name]; len(candidates) == 1 {
		return candidates[0], model.OriginUniqueName
	}

	return nil, ""
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
