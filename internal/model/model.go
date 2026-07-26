// Package model holds the shared data types of the seamark graph.
//
// The graph captures three layers (RFC-001 §5.1):
//
//   - structure: Symbol and Edge rows, parsed from source with tree-sitter
//   - history:   CoChange pairs and Decision rows, mined from git
//   - effects:   Effect tags propagated along call edges (later milestone)
//
// Every value here traces to a parse, a commit, or a policy file — never to
// generated prose.
package model

import (
	"path"
	"strings"
)

// SymbolKind classifies a Symbol row.
type SymbolKind string

// The symbol kinds extractors produce.
const (
	KindFunction SymbolKind = "function"
	KindMethod   SymbolKind = "method"
	KindType     SymbolKind = "type"
	KindConst    SymbolKind = "const"
	KindVar      SymbolKind = "var"
	// KindPackage represents a package/module grouping node. Internal
	// packages use their repo-relative directory as FQN; external ones use
	// the import path as written.
	KindPackage SymbolKind = "package"
)

// EdgeKind classifies a structural edge between two symbols.
type EdgeKind string

// The edge kinds of the structure graph (RFC-001 §5.1).
const (
	EdgeCalls      EdgeKind = "CALLS"
	EdgeImports    EdgeKind = "IMPORTS"
	EdgeImplements EdgeKind = "IMPLEMENTS"
	EdgeDefines    EdgeKind = "DEFINES"
)

// Edge origins record how an edge was derived, so downstream consumers can
// filter by confidence.
const (
	// OriginSamePackage: bare-identifier call resolved within the package.
	OriginSamePackage = "same-package"
	// OriginSameClass: self/cls/this method call resolved to a method of
	// the caller's own class.
	OriginSameClass = "same-class"
	// OriginQualified: qualified call resolved through the file's imports.
	OriginQualified = "qualified"
	// OriginUniqueName: method call resolved because exactly one symbol in
	// the repo carries that name. Lowest confidence.
	OriginUniqueName = "unique-name"
	// OriginParse: edge read directly off the syntax tree (imports).
	OriginParse = "parse"
)

// Span is a half-open source range in 1-based lines and 0-based columns.
type Span struct {
	StartLine uint32
	StartCol  uint32
	EndLine   uint32
	EndCol    uint32
}

// Symbol is one node of the structure graph.
type Symbol struct {
	ID      int64
	FQN     string // e.g. "internal/store.Store.UpsertSymbols"
	Name    string // last FQN segment, e.g. "UpsertSymbols"
	Kind    SymbolKind
	File    string // repo-relative path; empty for external packages
	Span    Span
	Sig     string // declaration signature text, single line
	DocHash string // sha256 hex of the doc comment, "" if none
}

// Edge is one directed structural edge.
type Edge struct {
	Src    int64
	Dst    int64
	Kind   EdgeKind
	Origin string
}

// CoChange records that two files changed together in history.
// Lift = P(a,b) / (P(a)·P(b)) over the mined commit window: >1 means the
// pair co-occurs more than chance, and the further above 1 the stronger the
// empirical coupling.
type CoChange struct {
	FileA    string // canonical order: FileA < FileB
	FileB    string
	Together int // commits touching both
	Total    int // commits in the mined window
	Lift     float64
}

// DecisionKind classifies where a Decision row came from.
type DecisionKind string

// The decision sources the history miner records.
const (
	DecisionCommit DecisionKind = "commit"
	DecisionRevert DecisionKind = "revert"
	DecisionPR     DecisionKind = "pr"
	DecisionADR    DecisionKind = "adr"
)

// Decision is one unit of "why": a commit, PR, revert, or ADR.
type Decision struct {
	ID     int64
	Kind   DecisionKind
	Ref    string // commit SHA, PR number, ADR path
	TS     int64  // unix seconds
	Author string
	Title  string
	Body   string
	Files  []string // repo-relative files this decision touched
}

// IsTestPath reports whether a file is test code, by each language's
// naming convention. Shared by resolution (test doubles must not win
// unique-name matches) and reporting (orientation shows the production
// surface, not test helpers).
func IsTestPath(p string) bool {
	base := path.Base(p)

	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_"),
		strings.HasSuffix(base, "_test.py"),
		strings.Contains(base, ".test."),
		strings.Contains(base, ".spec."):
		return true
	}

	for seg := range strings.SplitSeq(path.Dir(p), "/") {
		if seg == "tests" || seg == "test" || seg == "__tests__" {
			return true
		}
	}

	return false
}
