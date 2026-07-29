package distill

import (
	"regexp"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Deduplication of PROPOSALS, distinct from the deduplication of
// findings. Signature memory stops seamark re-reading the same evidence
// group, but a repo-wide mistake appears in several groups by
// definition — each one independently re-deriving "guard empty
// collections" under a fresh name. Measured on a real repo: 65 applied
// pins carried 50 distinct themes, so 23% of the pin file restated
// something already pinned, and the edit hook's injection budget was
// spending its slots on near-identical guidance.
//
// The check is deterministic and free: no agent call is needed to see
// that two short rules say the same thing. Thresholds were calibrated
// against that repo's real pins — every hand-identified duplicate
// cluster is caught, and no distinct pin is merged.

const (
	ruleOverlap = 0.40 // rule labels alone are conclusive at this overlap
	noteOverlap = 0.30 // …or the guidance text is
	// Weaker agreement in BOTH counts as duplication: differently-named
	// rules whose notes rhyme (leaky-error-payloads vs
	// no-internal-details-in-client-errors).
	ruleWeak = 0.25
	noteWeak = 0.18
)

var wordRe = regexp.MustCompile(`[a-z]+`)

// stopWords carry no topic: they inflate overlap between unrelated
// rules and hide it between related ones.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "when": true, "never": true,
	"always": true, "must": true, "not": true, "dont": true, "are": true, "this": true,
	"that": true, "from": true, "into": true, "before": true, "after": true, "use": true,
	"using": true, "set": true, "get": true, "make": true, "sure": true, "keep": true,
	"only": true, "same": true, "its": true, "their": true, "them": true, "they": true,
	"you": true, "your": true, "our": true, "any": true, "all": true, "each": true,
	"every": true, "than": true, "then": true, "instead": true, "rather": true,
}

// words tokenizes text into topic words of at least minLen length, with
// plurals folded: "error" and "errors" are one topic, and rules about
// the same mistake routinely disagree on number ("leaky-error-payloads"
// vs "no-internal-details-in-client-errors" shared nothing until this).
func words(s string, minLen int) map[string]bool {
	out := map[string]bool{}

	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = w[:len(w)-1]
		}

		if len(w) > minLen && !stopWords[w] {
			out[w] = true
		}
	}

	return out
}

// jaccard is the overlap of two token sets, 0 when either is empty.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	shared := 0

	for w := range a {
		if b[w] {
			shared++
		}
	}

	return float64(shared) / float64(len(a)+len(b)-shared)
}

// topic is the comparable form of one pattern: its label and guidance
// reduced to topic words, plus the region it governs.
type topic struct {
	label  string
	region string
	rule   map[string]bool
	note   map[string]bool
}

func newTopic(rule, note string) topic {
	return topic{
		// The label is echoed back — into an agent prompt and onto a
		// terminal — so it is normalized to the same kebab slug the
		// distiller's own rules get. Pins are hand-written in committed
		// config, which on a cloned or contributed-to repo is no more
		// trusted than a review comment: unnormalized, a multiline rule
		// would inject lines into the prompt's instruction block and a
		// long one would blow the primer budget the label cap exists to
		// bound. cleanRule strips everything but [a-z0-9-] and caps the
		// length, so both are structurally impossible.
		label: cleanRule(rule),
		// Matching still reads the raw text: words() lowercases and
		// drops punctuation itself, so normalization changes nothing
		// about which patterns are judged to restate each other.
		rule: words(strings.ReplaceAll(rule, "-", " "), 2),
		note: words(note, 3),
	}
}

// minNoteWords is how much guidance text a note needs before its
// overlap means anything: two four-word notes sharing one incidental
// word score 0.3 on pure arithmetic and say nothing to a reader.
const minNoteWords = 4

// restates reports whether two patterns say the same thing.
func (t topic) restates(o topic) bool {
	r := jaccard(t.rule, o.rule)
	if r >= ruleOverlap {
		return true
	}

	if len(t.note) < minNoteWords || len(o.note) < minNoteWords {
		return false // too little prose to judge; the labels already disagreed
	}

	n := jaccard(t.note, o.note)

	return n >= noteOverlap || (r >= ruleWeak && n >= noteWeak)
}

// Known is the set of patterns already captured: every pin in
// lessons.yaml (hand-written ones included) and every proposal already
// decided or pending. A distilled pattern matching one of these is not
// news — including one the user dismissed, since a dismissal is a
// decision the distiller must not relitigate.
type Known struct {
	topics []topic
}

// NewKnown builds the set. Pins arrive as Proposals carrying just Rule
// and Note, so config pins and stored proposals share one shape.
func NewKnown(patterns ...[]model.Proposal) *Known {
	k := &Known{}

	for _, group := range patterns {
		for _, p := range group {
			k.Add(p)
		}
	}

	return k
}

// Add records a pattern as known — used as a run proceeds, so two
// groups in the same pass cannot both propose the same thing.
func (k *Known) Add(p model.Proposal) {
	t := newTopic(p.Rule, p.Note)
	t.region = p.Region
	k.topics = append(k.topics, t)
}

// Labels lists the rule names already captured for an area, newest
// first, at most limit of them. They are fed to the distiller so it can
// skip what is known and spend the call looking for something else —
// labels only, because the notes would cost more tokens than the
// duplicates they prevent.
func (k *Known) Labels(region string, limit int) []string {
	var out []string

	seen := map[string]bool{}

	for _, t := range k.topics {
		if len(out) >= limit {
			break
		}

		if t.label == "" || seen[t.label] || !governs(t.region, region) {
			continue
		}

		seen[t.label] = true
		out = append(out, t.label)
	}

	return out
}

// governs reports whether a pattern pinned at pinRegion bears on work
// in groupRegion. Repo-wide pins always do; otherwise either may
// contain the other — a pin on api/services is worth knowing about
// when distilling api, and vice versa. A cross-tree group (no common
// region) sees everything, which the caller's limit bounds.
func governs(pinRegion, groupRegion string) bool {
	if pinRegion == "" || pinRegion == "*" || groupRegion == "" {
		return true
	}

	return reviews.RegionMatches(pinRegion, groupRegion) ||
		reviews.RegionMatches(groupRegion, pinRegion)
}

// Restated returns the label of the known pattern p duplicates, and
// whether it duplicates one at all.
func (k *Known) Restated(p model.Proposal) (string, bool) {
	t := newTopic(p.Rule, p.Note)

	for _, known := range k.topics {
		if t.restates(known) {
			return known.label, true
		}
	}

	return "", false
}

// Clusters groups patterns that restate each other, largest first;
// singletons are omitted. It is the audit of an existing pin file: what
// is already there in several wordings, for a human to prune (seamark
// never edits lessons.yaml unasked).
func Clusters(ps []model.Proposal) [][]model.Proposal {
	parent := make([]int, len(ps))
	for i := range parent {
		parent[i] = i
	}

	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}

		return x
	}

	topics := make([]topic, len(ps))
	for i, p := range ps {
		topics[i] = newTopic(p.Rule, p.Note)
	}

	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			if topics[i].restates(topics[j]) {
				parent[find(i)] = find(j)
			}
		}
	}

	groups := map[int][]model.Proposal{}
	for i, p := range ps {
		root := find(i)
		groups[root] = append(groups[root], p)
	}

	// Deterministic order: biggest cluster first, ties by lowest id.
	var out [][]model.Proposal

	for i := range ps {
		if g := groups[find(i)]; len(g) > 1 && find(i) == i {
			out = append(out, g)
		}
	}

	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) ||
				(len(out[j]) == len(out[i]) && out[j][0].ID < out[i][0].ID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}

	return out
}
