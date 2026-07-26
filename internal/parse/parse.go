// Package parse turns source files into language-neutral symbol and
// reference facts. One Extractor per language; the indexer stays free of
// language knowledge.
//
// Extraction is deliberately syntactic: call references are recorded as
// (qualifier, name) pairs and resolved to symbols later by the indexer with
// declared confidence (model.Origin*). Type-accurate resolution is a future
// refinement, not a prerequisite — the graph records how each edge was
// derived so consumers can filter.
package parse

import (
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/model"
)

// Import is one import clause of a file.
type Import struct {
	Path string // as written, e.g. "github.com/spf13/cobra" or "./api/client"
	// LocalName is the identifier that qualifies member calls through this
	// import: the Go package alias, or a JS/TS namespace import (`* as ns`).
	// "" when the import contributes no qualifier.
	LocalName string
	// Named lists identifiers imported un-aliased into the file's scope
	// (`import {foo} from …`). Bare calls to these resolve into the
	// imported module. Aliased imports are omitted — their local names
	// cannot be looked up in the target module. Unused by Go.
	Named []string
	// Resolved is the repo-relative package/module key this import points
	// at, when the extractor can resolve it by itself (JS/TS relative
	// specifiers). "" means the indexer decides (Go module paths) or the
	// import is external.
	Resolved string
}

// CallRef is an unresolved call site inside a declaration.
type CallRef struct {
	// Qualifier is the identifier the call is selected from: "pkg" in
	// pkg.F(), "w" in w.Run(). Empty for bare calls and for selector calls
	// whose operand is a complex expression (x.db.Close(), f().Close()).
	Qualifier string
	Name      string // called identifier / method name
	Line      uint32 // 1-based
	// Selector distinguishes x.F() from F(). Bare calls resolve against
	// package-level functions; selector calls must never be, even when
	// Qualifier is empty — the operand is a value, not the package scope.
	Selector bool
	// Receiver marks a call through the enclosing method's own receiver —
	// Python's first parameter, Go's named receiver, JS/TS's lexical
	// `this`. Set by the extractor from the declaration, never inferred
	// from spelling: a local variable named "self" must not qualify.
	Receiver bool
}

// SymbolDecl is a declared symbol plus the call references inside its body.
type SymbolDecl struct {
	Symbol model.Symbol
	Calls  []CallRef
}

// FileResult is everything extracted from one file.
type FileResult struct {
	Path     string // repo-relative
	Language string
	Package  string // repo-relative package/module key (directory for Go)
	Symbols  []SymbolDecl
	Imports  []Import
}

// Extractor parses one language. Implementations are not safe for
// concurrent use; the indexer runs one goroutine per Extractor instance.
type Extractor interface {
	Language() string
	Extensions() []string // with leading dot, e.g. ".go"
	// Extract parses src of the file at repo-relative relPath.
	Extract(relPath string, src []byte) (*FileResult, error)
	Close()
}

// LanguageFamily groups languages that share one symbol namespace: the
// three ECMA dialects (JS, TS, TSX) import each other freely and must be
// resolved as a single family; every other language is its own.
func LanguageFamily(lang string) string {
	switch EcmaDialect(lang) {
	case EcmaJS, EcmaTS, EcmaTSX:
		return "ecma"
	default:
		return lang
	}
}

// FamilyForPath returns the language family for a file path by extension,
// "" when unsupported. Kept in sync with the extractors' Extensions() —
// static so read-only surfaces can classify paths without instantiating
// parsers.
func FamilyForPath(p string) string {
	switch filepath.Ext(p) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return "ecma"
	case ".py":
		return "python"
	default:
		return ""
	}
}

// Registry maps file extensions to extractors.
type Registry struct {
	byExt      map[string]Extractor
	extractors []Extractor
}

// NewRegistry constructs all built-in extractors.
func NewRegistry() (*Registry, error) {
	r := &Registry{byExt: map[string]Extractor{}}

	builders := []func() (Extractor, error){
		func() (Extractor, error) { return NewGoExtractor() },
		func() (Extractor, error) { return NewEcmaExtractor(EcmaJS) },
		func() (Extractor, error) { return NewEcmaExtractor(EcmaTS) },
		func() (Extractor, error) { return NewEcmaExtractor(EcmaTSX) },
		func() (Extractor, error) { return NewPyExtractor() },
	}
	for _, build := range builders {
		ex, err := build()
		if err != nil {
			r.Close()
			return nil, err
		}

		r.extractors = append(r.extractors, ex)

		for _, ext := range ex.Extensions() {
			r.byExt[ext] = ex
		}
	}

	return r, nil
}

// ForPath returns the extractor handling path, or nil when unsupported.
func (r *Registry) ForPath(path string) Extractor {
	return r.byExt[filepath.Ext(path)]
}

// Close releases parser resources.
func (r *Registry) Close() {
	for _, ex := range r.extractors {
		ex.Close()
	}
}
