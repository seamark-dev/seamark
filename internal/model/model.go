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
	"fmt"
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

// Lesson is a cluster of recurring review feedback (M6): the same kind
// of comment landing on the same region across pull requests. It is the
// anti-repeat signal — "reviewers keep flagging X here" — surfaced to an
// agent before it makes the mistake a fourth time.
type Lesson struct {
	ID          int64
	ClusterKey  string // stable identity of (region, symptom); the upsert key
	Region      string // file or directory the feedback lands in
	Reviewer    string // coderabbit | copilot | bot | human | mixed
	Symptom     string // a rule code (RUF001) or a normalized message
	Fix         string // extracted suggestion, when the comment carried one
	Occurrences int    // how many comments fall in this cluster
	LastTS      int64  // most recent occurrence, unix seconds
	ExampleURL  string // a representative comment, for provenance
	// Annotation is surface-time display text (a confidence tag like
	// "weak evidence: 1 event"), never part of the lesson's identity:
	// the firing log records Symptom, and an annotation that changes
	// with age must not split one lesson into many statistical rows.
	Annotation string
}

// Finding is one raw review comment behind a lesson — the full material
// the 80-char fingerprint was distilled from. Lessons answer "what
// keeps happening"; findings keep the evidence, so deeper passes
// (distillation, provenance display) work from what reviewers actually
// wrote rather than from lossy summaries.
type Finding struct {
	ID        int64  // stable across mines: GitHub comment id, or sha-derived for fixes
	LessonKey string // ClusterKey of the lesson this comment fed; "" for fix findings
	// Path is the finding's semantic home: the commented file for a
	// review finding, the most-changed non-test code file for a fix.
	Path string
	// Paths is a fix commit's full code footprint (primary first, by
	// churn, capped) — what region inference votes with, so a fix whose
	// test out-churned it still points at the code it corrected. Nil
	// for review findings: their single Path IS the location.
	Paths     []string
	PR        int    // pull-request number, 0 when unknown
	Reviewer  string // coderabbit | copilot | bot | human
	Body      string // boilerplate-stripped text, capped
	URL       string // provenance link (comment or commit), "" when none
	CreatedAt int64  // unix seconds
	// Source is the provider and its derivation: review, revert, or
	// fix:conventional / fix:issue-link / fix:subject — every finding
	// declares how it was mined, like every edge declares how it was
	// resolved.
	Source string
}

// Finding sources.
const (
	SourceReview          = "review"
	SourceRevert          = "revert"
	SourceFixConventional = "fix:conventional"
	SourceFixIssueLink    = "fix:issue-link"
	SourceFixSubject      = "fix:subject"
	// SourceFixBranch marks a fix whose only declaration is its branch
	// name: a merge from fix/… whose commits carried no fix-shaped
	// message of their own. The finding is the merge's whole diff.
	SourceFixBranch = "fix:branch"
)

// fixMinedSources are the sources one pass of fix mining produces —
// every fix tier plus reverts, which the same provider classifies. They
// share a replace lifecycle, so a new tier must be listed here to be
// swapped rather than accumulate. Deliberately an explicit list and not
// "everything that is not a review": a future provider on its own
// cadence (CI transitions, say) must not have its findings wiped by a
// fix mine.
var fixMinedSources = []string{
	SourceFixConventional, SourceFixIssueLink, SourceFixSubject,
	SourceFixBranch, SourceRevert,
}

// FixMinedSources returns the sources replaced together with fix mining.
func FixMinedSources() []string {
	return append([]string(nil), fixMinedSources...)
}

// Proposal lifecycle states. Dismissed and superseded are both "not in
// the pin file", but they mean opposite things about the pattern:
// dismissed rejects the guidance, superseded keeps it and drops a
// redundant wording of it. Conflating them would let pruning a
// duplicate silently suppress a theme the user still wants.
const (
	ProposalProposed   = "proposed"
	ProposalApplied    = "applied"
	ProposalDismissed  = "dismissed"
	ProposalSuperseded = "superseded"
)

// Proposal is one distilled pattern awaiting a human decision: a
// pin-shaped lesson synthesized by the configured agent from a group of
// findings (the members it cited). Proposals follow the plan/apply
// contract — the distiller may only ever PROPOSE; a pin reaches
// .seamark/lessons.yaml exclusively through an explicit apply.
type Proposal struct {
	ID        int64
	Signature string // the evidence group that produced it
	Rule      string // short kebab-case pin label
	// Region is the first (deepest-coverage) region of Regions, kept as
	// a single value for display and for readers that predate region
	// sets ("" = repo-wide).
	Region string
	// Regions is the evidence-coverage region set (at most 3 directories
	// covering ≥80% of the cited events; see distill.coverageRegions).
	// Empty means "derive from Region" — pre-set rows and repo-wide
	// proposals both land there.
	Regions   []string
	Note      string  // the guidance, pin-ready
	Members   []int64 // finding ids the agent cited — verified to exist in the group
	Agent     string  // provenance: adapter name + prompt version
	Status    string  // proposed | applied | dismissed
	CreatedAt int64
}

// RegionSet returns the effective region set: Regions when present,
// else the single Region, else nil — which means repo-wide. A "*" (or
// empty) entry anywhere means the whole set is repo-wide: matching
// code compares literal paths, and an unnormalized wildcard would
// match nothing instead of everything.
func (p Proposal) RegionSet() []string {
	var out []string

	for _, r := range p.Regions {
		if r == "" || r == "*" {
			return nil
		}

		out = append(out, r)
	}

	if len(out) > 0 {
		return out
	}

	if p.Region != "" && p.Region != "*" {
		return []string{p.Region}
	}

	return nil
}

// IsDocPath reports whether a file is documentation — never the
// semantic home of a code change. Shared by fix mining (a doc file
// must not be elected a fix's primary file) and region inference (doc
// paths don't vote; a README citation must not drag a region to
// repo-wide).
func IsDocPath(p string) bool {
	return strings.HasSuffix(p, ".md") ||
		strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "rfc/")
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

// CountEvents collapses findings into distinct events: findings
// sharing a pull request are one event (the review comment and the fix
// commit that answered it), while findings without a pr number are
// independent. The distiller's recurrence bar, region inference, and
// confidence all count evidence this way — one definition, or two
// surfaces disagree about what recurred.
func CountEvents(findings []Finding) int {
	events := map[string]bool{}

	for _, f := range findings {
		key := fmt.Sprintf("id:%d", f.ID)
		if f.PR > 0 {
			key = fmt.Sprintf("pr:%d", f.PR)
		}

		events[key] = true
	}

	return len(events)
}
