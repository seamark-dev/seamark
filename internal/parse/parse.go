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
	Path      string // as written, e.g. "github.com/spf13/cobra"
	LocalName string // alias or last path segment; "" for blank/dot imports
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

// Registry maps file extensions to extractors.
type Registry struct {
	byExt      map[string]Extractor
	extractors []Extractor
}

// NewRegistry constructs all built-in extractors.
func NewRegistry() (*Registry, error) {
	goEx, err := NewGoExtractor()
	if err != nil {
		return nil, err
	}

	r := &Registry{byExt: map[string]Extractor{}}

	for _, ex := range []Extractor{goEx} {
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
