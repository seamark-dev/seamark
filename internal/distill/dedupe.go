package distill

import (
	"sort"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/wording"
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
// Two checks run, both deterministic and free, in order of strength:
//
//  1. Evidence identity. Group signatures churn as the corpus grows
//     (slice boundaries move), so two different groups can cite the
//     same finding subset — measured: two applied pins with
//     byte-identical member lists whose wordings shared almost nothing
//     ("run-linters-before-commit" / "lint-clean-before-push"). EQUAL
//     citation sets are one pattern, whatever the words say. Equality
//     only, never containment: one finding can flag two mistakes, so a
//     subset is legitimate distinct evidence — and a wrong suppression
//     is persistent (the group is marked distilled), while a missed
//     duplicate stays visible and prunable.
//  2. Wording (internal/wording): no agent call is needed to see that
//     two short rules say the same thing.

// entry is one known pattern: its comparable wording, the regions it
// governs (nil = repo-wide), and the evidence it cited (empty for
// config pins).
type entry struct {
	topic     wording.Topic
	regions   []string
	signature string  // evidence group that produced it; "" for config pins
	members   []int64 // sorted; nil when the pattern cites no findings
}

// Known is the set of patterns already captured: every pin in
// lessons.yaml (hand-written ones included) and every proposal already
// decided or pending. A distilled pattern matching one of these is not
// news — including one the user dismissed, since a dismissal is a
// decision the distiller must not relitigate.
type Known struct {
	entries []entry
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
	k.entries = append(k.entries, entry{
		topic:     wording.New(p.Rule, p.Note),
		regions:   p.RegionSet(),
		signature: p.Signature,
		members:   sortedIDs(p.Members),
	})
}

// Labels lists the rule names already captured for an area, newest
// first, at most limit of them. They are fed to the distiller so it can
// skip what is known and spend the call looking for something else —
// labels only, because the notes would cost more tokens than the
// duplicates they prevent.
func (k *Known) Labels(region string, limit int) []string {
	var out []string

	seen := map[string]bool{}

	for _, e := range k.entries {
		if len(out) >= limit {
			break
		}

		label := e.topic.Label()
		if label == "" || seen[label] || !governs(e.regions, region) {
			continue
		}

		seen[label] = true
		out = append(out, label)
	}

	return out
}

// governs reports whether a pattern pinned at pinRegions bears on work
// in groupRegion. Repo-wide pins (nil) always do; otherwise any of the
// pin's regions may contain or be contained by the group's — a pin on
// api/services is worth knowing about when distilling api, and vice
// versa. A cross-tree group (no common region) sees everything, which
// the caller's limit bounds.
func governs(pinRegions []string, groupRegion string) bool {
	if len(pinRegions) == 0 || groupRegion == "" {
		return true
	}

	for _, r := range pinRegions {
		if reviews.RegionMatches(r, groupRegion) ||
			reviews.RegionMatches(groupRegion, r) {
			return true
		}
	}

	return false
}

// Restated returns the label of the known pattern p duplicates, and
// whether it duplicates one at all. Evidence identity is checked
// before wording: it is the stronger claim, and it catches the pairs
// wording cannot.
//
// Identity deliberately skips patterns from p's own reply batch (same
// signature): one finding can flag two mistakes, so a model reading a
// group may honestly cite the same members for distinct patterns — the
// wording check still guards that batch against padding. Across
// batches the same citations mean the corpus was re-carved and the
// theme re-derived, which is exactly the duplication to stop.
func (k *Known) Restated(p model.Proposal) (string, bool) {
	if mine := sortedIDs(p.Members); mine != nil {
		for _, e := range k.entries {
			if sameBatch(p.Signature, e.signature) {
				continue
			}

			if evidenceEqual(mine, e.members) {
				return e.topic.Label(), true
			}
		}
	}

	t := wording.New(p.Rule, p.Note)

	for _, e := range k.entries {
		if t.Restates(e.topic) {
			return e.topic.Label(), true
		}
	}

	return "", false
}

// sameBatch reports whether two signatures name the same reply batch.
// Empty signatures never match: a pattern with no batch has no
// batch-mates.
func sameBatch(a, b string) bool {
	return a != "" && a == b
}

// sortedIDs returns a sorted copy of ids, nil when there are none —
// the comparable form evidenceEqual expects.
func sortedIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}

	out := make([]int64, len(ids))
	copy(out, ids)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

// evidenceEqual reports whether two citation sets are identical. Two
// patterns drawn from exactly the same evidence in different batches
// are one pattern re-derived — the measured duplication shape. Strict
// containment deliberately does NOT count: a subset can be a different
// mistake the same findings also flag, and suppressing it would mark
// its group distilled with the lesson unextracted. Both slices must be
// sorted; either being empty never matches — a config pin cites
// nothing and identity says nothing about it.
func evidenceEqual(a, b []int64) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
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

	topics := make([]wording.Topic, len(ps))
	sets := make([][]int64, len(ps))

	for i, p := range ps {
		topics[i] = wording.New(p.Rule, p.Note)
		sets[i] = sortedIDs(p.Members)
	}

	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			// Same rules as Restated: equal evidence is conclusive even
			// when the wordings share nothing — except inside one reply
			// batch, where shared citations are legitimate.
			rederived := !sameBatch(ps[i].Signature, ps[j].Signature) &&
				evidenceEqual(sets[i], sets[j])

			if topics[i].Restates(topics[j]) || rederived {
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
