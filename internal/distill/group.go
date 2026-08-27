// Package distill turns the raw review findings behind lessons into
// candidate groups for LLM distillation (RFC-001 §5.4 Tier 2). Exact
// clustering cannot merge ten differently-worded findings about one
// mistake; a reader of the raw material can — this package prepares
// that reader's batches.
//
// The pipeline is built around one economic invariant: an unchanged
// evidence set is never distilled twice. Findings carry stable ids
// (GitHub comment ids), a group's Signature hashes its member ids, and
// the store remembers which signatures were already processed. A new
// finding changes its group's signature — exactly that group is re-read,
// nothing else.
//
// Grouping is a Grouper: the contract (deterministic output, stable
// signatures) is the architecture, the strategy is replaceable. The
// built-in lexical grouper connects findings that share salient tokens
// — identifiers survive in finding bodies, and "reset"/"pooled"/"Free"
// recurring across files is precisely the trace a semantic pattern
// leaves — then buckets the remainder by directory. An embedding-based
// grouper can replace it without touching the pipeline or the stored
// state.
package distill

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// Group is one distillation batch: findings that plausibly share a
// theme (or at least a neighborhood), with a stable identity.
type Group struct {
	// Region is the deepest directory common to every member, "" when
	// the members span top-level trees (a repo-wide theme batch).
	Region string
	// Findings are the members, ordered by id.
	Findings []model.Finding
	// Signature identifies the evidence set: the hash of the member
	// ids. Same members — same signature, regardless of ordering or of
	// anything outside the group.
	Signature string
	// Area marks a directory bucket: thematically unconnected findings
	// batched so distillation still covers them. Membership in an area
	// group only means "same directory", so consumers that treat group
	// membership as "same mistake" (the outcome loop) must skip these.
	Area bool
}

// Grouper buckets findings into candidate groups. Implementations must
// be deterministic: the same findings yield the same groups with the
// same signatures, in the same order.
type Grouper interface {
	Group(findings []model.Finding) []Group
}

// lexicalGrouper is the built-in strategy: theme groups from shared
// salient tokens, area groups from directories, singletons dropped.
type lexicalGrouper struct {
	// strongShared joins findings without a component-size constraint.
	strongShared int
	// weakShared admits wording bridges, but only while the resulting
	// component stays small. This preserves cross-wording recall without
	// letting a chain of generic two-token overlaps swallow the corpus.
	weakShared       int
	maxWeakComponent int
	// maxDocFrac drops tokens present in more than this fraction of
	// findings: a word that appears everywhere connects nothing.
	maxDocFrac float64
	// maxGroup caps a group's size; oversized theme components are
	// split by directory, oversized area buckets by id order.
	maxGroup int
}

// NewLexicalGrouper returns the default token-overlap Grouper.
//
// Three shared tokens form an unconstrained strong edge. Two-token edges are
// useful wording bridges (the pooled-state corpus needs them), but may only
// build a small component. This two-tier rule prevents the weak transitive
// chains observed in large public repositories without giving up the recall
// that motivated lexical grouping.
func NewLexicalGrouper() Grouper {
	return &lexicalGrouper{
		strongShared: 3, weakShared: 2, maxWeakComponent: 12,
		maxDocFrac: 0.10, maxGroup: 40,
	}
}

// Group implements Grouper.
func (g *lexicalGrouper) Group(findings []model.Finding) []Group {
	if len(findings) == 0 {
		return nil
	}

	// Sort by id first: every later step iterates slices, so this one
	// sort makes the whole pipeline order-insensitive to input.
	sorted := make([]model.Finding, len(findings))
	copy(sorted, findings)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	tokens := make([]map[string]bool, len(sorted))
	df := map[string]int{}

	for i := range sorted {
		tokens[i] = salientTokens(&sorted[i])
		for tok := range tokens[i] {
			df[tok]++
		}
	}

	// Drop ubiquitous tokens: in a resolver engine every finding says
	// "resolve"; keeping it would chain the whole repo into one blob.
	// The floor keeps the filter dormant on small sets, where a token
	// carried by most findings IS the theme, not noise.
	cutoff := max(int(g.maxDocFrac*float64(len(sorted))), 8)

	for i := range tokens {
		for tok := range tokens[i] {
			if df[tok] > cutoff {
				delete(tokens[i], tok)
			}
		}
	}

	// Strong edges establish the semantic cores first. Weak edges then
	// attach wording bridges only while the combined component remains
	// reviewable; a long A~B~C~… chain must not become one theme merely
	// because every neighboring pair shares two generic words.
	parent := make([]int, len(sorted))
	sizes := make([]int, len(sorted))
	for i := range parent {
		parent[i] = i
		sizes[i] = 1
	}

	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}

		return x
	}

	union := func(i, j, cap int) {
		a, b := find(i), find(j)
		if a == b || cap > 0 && sizes[a]+sizes[b] > cap {
			return
		}

		parent[b] = a
		sizes[a] += sizes[b]
	}

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sharedAtLeast(tokens[i], tokens[j], g.strongShared) {
				union(i, j, 0)
			}
		}
	}

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if weakBridgeAllowed(&sorted[i], &sorted[j]) &&
				sharedAtLeast(tokens[i], tokens[j], g.weakShared) {
				union(i, j, g.maxWeakComponent)
			}
		}
	}

	members := map[int][]model.Finding{} // component root -> findings
	for i := range sorted {
		root := find(i)
		members[root] = append(members[root], sorted[i])
	}

	var themes []Group

	byDir := map[string][]model.Finding{} // leftovers for area grouping

	roots := make([]int, 0, len(members))
	for root := range members {
		roots = append(roots, root)
	}

	sort.Ints(roots)

	for _, root := range roots {
		fs := members[root]
		if len(fs) < 2 {
			// A component of one has no recurrence to distill; it waits
			// in its directory bucket for company.
			byDir[path.Dir(fs[0].Path)] = append(byDir[path.Dir(fs[0].Path)], fs[0])

			continue
		}

		themes = append(themes, g.splitOversized(fs)...)
	}

	out := themes
	out = append(out, g.areaGroups(byDir)...)

	// Present strongest evidence first; signature ties are impossible.
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Findings) != len(out[j].Findings) {
			return len(out[i].Findings) > len(out[j].Findings)
		}

		return out[i].Signature < out[j].Signature
	})

	return out
}

// weakBridgeAllowed reserves two-token transitivity for review prose, where
// terse differently-worded comments need it. Fix findings carry a commit
// message, changed functions, and a patch excerpt; accepting a weak edge there
// mostly connects their shared mining envelope. Fix-to-fix and mixed-source
// recurrence therefore need the strong threshold.
func weakBridgeAllowed(a, b *model.Finding) bool {
	return sourceLabel(a.Source) == model.SourceReview &&
		sourceLabel(b.Source) == model.SourceReview
}

// splitOversized turns one theme component into groups within the size
// cap. The component is already thematically coherent, so any cut is
// fine for reading — but NOT for the signature economics: cutting by
// position means one new low-id member shifts every boundary, every
// slice signature churns, and the whole component is re-read and
// re-billed (measured: two different runs each minted a proposal from
// the same evidence subset that way). Members are therefore bucketed by
// a hash of their id — a new finding perturbs exactly its own bucket.
func (g *lexicalGrouper) splitOversized(fs []model.Finding) []Group {
	var out []Group

	for _, part := range g.bounded(fs) {
		out = append(out, makeGroup(part))
	}

	return out
}

// areaGroups buckets thematically unconnected findings by directory —
// full coverage even where token overlap saw nothing. Directories of
// one are dropped: a singleton has no recurrence to distill, and its
// signature will change the moment a neighbor arrives. Oversized
// directories split through the same stable bucketing as theme
// components — a hot directory grows constantly, which is exactly when
// positional cuts would churn every signature it has.
func (g *lexicalGrouper) areaGroups(byDir map[string][]model.Finding) []Group {
	var out []Group

	for _, dir := range sortedKeys(byDir) {
		if len(byDir[dir]) < 2 {
			continue
		}

		for _, part := range g.bounded(byDir[dir]) {
			grp := makeGroup(part)
			grp.Area = true

			out = append(out, grp)
		}
	}

	return out
}

// bounded cuts an oversized set into id-hash buckets of at most
// maxGroup members. The bucket count is a power of two, so it only
// changes when the set crosses a doubling boundary — growth between
// boundaries touches one bucket, and today's re-bill-everything is the
// new worst case, paid logarithmically rarely instead of per finding.
func (g *lexicalGrouper) bounded(fs []model.Finding) [][]model.Finding {
	if len(fs) <= g.maxGroup {
		return [][]model.Finding{fs}
	}

	k := 1
	for k*g.maxGroup < len(fs) {
		k <<= 1
	}

	buckets := make([][]model.Finding, k)

	for _, f := range fs {
		i := idBucket(f.ID, k)
		buckets[i] = append(buckets[i], f)
	}

	// A singleton bucket would be dropped downstream as "no
	// recurrence" — but its member HAS company in this set, and a
	// finding with company must never be lost to an accident of
	// hashing. Merge it into the lowest-indexed other occupied bucket.
	for i, b := range buckets {
		if len(b) != 1 {
			continue
		}

		for j := range buckets {
			if j != i && len(buckets[j]) > 0 {
				buckets[j] = append(buckets[j], b[0])
				buckets[i] = nil

				break
			}
		}
	}

	var out [][]model.Finding

	for _, b := range buckets {
		if len(b) == 0 {
			continue
		}

		// Hash skew can overfill a bucket; the positional split stays
		// confined to that one bucket.
		out = append(out, slices(b, g.maxGroup)...)
	}

	return out
}

// idBucket hashes a finding id into one of k buckets. FNV-1a keeps the
// spread uniform for both id shapes in the corpus: sequential GitHub
// comment ids and sha-derived fix ids.
func idBucket(id int64, k int) int {
	h := fnv.New64a()

	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], uint64(id))
	_, _ = h.Write(buf[:])

	return int(h.Sum64() % uint64(k))
}

// makeGroup assembles a Group: members by id, common region, signature.
func makeGroup(fs []model.Finding) Group {
	sort.Slice(fs, func(i, j int) bool { return fs[i].ID < fs[j].ID })

	h := sha256.New()
	for _, f := range fs {
		fmt.Fprintf(h, "%d,", f.ID)
	}

	return Group{
		Region:    commonDir(fs),
		Findings:  fs,
		Signature: hex.EncodeToString(h.Sum(nil))[:16],
	}
}

// commonDir is the deepest directory shared by every member's path, ""
// when only the repo root is (reviews has a sibling for lesson merging;
// here root-common is fine — a group is a batch, not a surfaced region).
func commonDir(fs []model.Finding) string {
	segs := strings.Split(path.Dir(fs[0].Path), "/")

	for _, f := range fs[1:] {
		other := strings.Split(path.Dir(f.Path), "/")

		if len(other) < len(segs) {
			segs = segs[:len(other)]
		}

		for i := range segs {
			if segs[i] != other[i] {
				segs = segs[:i]
				break
			}
		}
	}

	common := path.Join(segs...)
	if common == "." {
		return ""
	}

	return common
}

var tokenRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_]{3,}`)

// stopTokens are words too common in review prose to signal a theme.
var stopTokens = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "have": true,
	"will": true, "must": true, "should": true, "would": true, "could": true,
	"when": true, "then": true, "than": true, "here": true, "there": true,
	"please": true, "consider": true, "avoid": true, "instead": true,
	"function": true, "method": true, "value": true, "return": true,
	"file": true, "line": true, "code": true, "change": true, "same": true,
	"also": true, "only": true, "into": true, "after": true, "before": true,
	"suggestion": true, "shell": true,
}

// tokensPerFinding caps how much one finding contributes: a giant body
// must not become a hub that chains unrelated groups together.
const tokensPerFinding = 40

// salientTokens extracts the theme-bearing vocabulary of one finding:
// lowercased words and identifiers of length ≥4, stopwords out, capped
// in text order. Identifiers are the strongest signal — the same
// backticked field name recurring across files is a semantic link no
// prose paraphrase can hide.
func salientTokens(f *model.Finding) map[string]bool {
	out := map[string]bool{}

	for _, tok := range tokenRe.FindAllString(groupingText(f), -1) {
		tok = strings.ToLower(tok)

		if stopTokens[tok] {
			continue
		}

		out[tok] = true
		if len(out) >= tokensPerFinding {
			break
		}
	}

	return out
}

// groupingText removes the storage envelope added by fix mining before token
// comparison. The full body still reaches the distillation prompt; only the
// cheap lexical candidate search ignores generic provenance labels and
// trailers that otherwise connect unrelated fixes.
func groupingText(f *model.Finding) string {
	if !isFixFinding(f.Source) {
		return f.Body
	}

	var primary, detail strings.Builder
	inDetail := false

	for line := range strings.Lines(f.Body) {
		line = strings.TrimSpace(line)

		switch {
		case line == "", line == "---------":
			continue
		case strings.HasPrefix(line, "fix commit "):
			continue
		case strings.HasPrefix(line, "subject:"):
			line = strings.TrimSpace(strings.TrimPrefix(line, "subject:"))
		case strings.HasPrefix(line, "Co-authored-by:"),
			strings.HasPrefix(line, "Signed-off-by:"):
			continue
		case strings.HasPrefix(line, "functions:"):
			inDetail = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "functions:"))
		case line == "patch:":
			inDetail = true
			continue
		case inDetail && (strings.HasPrefix(line, "@@") ||
			strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---")):
			continue
		}

		if line == "" {
			continue
		}

		if inDetail {
			detail.WriteString(line)
			detail.WriteByte('\n')
		} else {
			primary.WriteString(line)
			primary.WriteByte('\n')
		}
	}

	// A real commit message is the highest-signal summary. Patch/function
	// detail is the fallback for anonymous messages such as "fix: PR
	// review", not extra vocabulary that every already-descriptive fix
	// needs to contribute to lexical grouping.
	if semanticTokenCount(primary.String()) >= 4 {
		return primary.String()
	}

	primary.WriteString(detail.String())

	return primary.String()
}

func isFixFinding(source string) bool {
	return strings.HasPrefix(source, "fix:") || source == model.SourceRevert
}

func semanticTokenCount(text string) int {
	seen := map[string]bool{}

	for _, token := range tokenRe.FindAllString(text, -1) {
		token = strings.ToLower(token)
		if !stopTokens[token] {
			seen[token] = true
		}
	}

	return len(seen)
}

// sharedAtLeast reports whether two token sets intersect in at least n
// entries, without materializing the intersection.
func sharedAtLeast(a, b map[string]bool, n int) bool {
	if len(b) < len(a) {
		a, b = b, a
	}

	seen := 0

	for tok := range a {
		if b[tok] {
			seen++
			if seen >= n {
				return true
			}
		}
	}

	return false
}

// slices cuts fs into chunks of at most size, preserving order. A
// trailing chunk of one is rebalanced by borrowing from its neighbor
// (…39+2 instead of …40+1): downstream drops single-member groups as
// "no recurrence", and a finding that HAD company must never be lost to
// an accident of arithmetic.
func slices(fs []model.Finding, size int) [][]model.Finding {
	if len(fs) == 0 {
		return nil
	}

	starts := []int{}
	for at := 0; at < len(fs); at += size {
		starts = append(starts, at)
	}

	if last := starts[len(starts)-1]; len(starts) > 1 && len(fs)-last == 1 {
		starts[len(starts)-1]--
	}

	var out [][]model.Finding

	for i, at := range starts {
		end := len(fs)
		if i+1 < len(starts) {
			end = starts[i+1]
		}

		out = append(out, fs[at:end])
	}

	return out
}

func sortedKeys(m map[string][]model.Finding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
