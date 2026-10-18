package distill

import (
	"errors"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The scope advice of an applied proposal: the region set that a
// retarget writes, as a typed value. The retarget planner of
// internal/maintain reads it for `seamark lessons --retarget` and for the
// console's Pins view, so both surfaces act on one recommendation. The
// Pins readings show the same advice before any retarget.

// ScopeKind classifies the scope that today's inference computes for a
// proposal against the scope that the proposal records.
type ScopeKind int

const (
	// ScopeNoEvidence means that no cited finding is living, so nothing
	// can be recomputed. The recorded scope stays: missing evidence is
	// never a reason to widen a pin. It is the zero value, so an unset
	// advice never reads as a change.
	ScopeNoEvidence ScopeKind = iota
	// ScopeCurrent means that the computed set equals the recorded set.
	ScopeCurrent
	// ScopeChanged means that the computed set differs from the recorded
	// set. A retarget can apply it.
	ScopeChanged
)

// String returns a stable lower-case phrase for the kind.
func (k ScopeKind) String() string {
	switch k {
	case ScopeNoEvidence:
		return "cannot recompute: no living evidence"
	case ScopeCurrent:
		return "regions already current"
	case ScopeChanged:
		return "a different scope is computed"
	default:
		return "ScopeKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// ScopeAdvice is the computed scope of one proposal with its reasons.
type ScopeAdvice struct {
	Kind ScopeKind
	// Regions is the computed set in the order of the region engine. Nil
	// means repository-wide. It is nil for ScopeNoEvidence too; RepoWide
	// tells the two cases apart.
	Regions []string
	// Facts are the trigger facts of RecomputeRegions: which stored
	// trigger paths the evidence or the history confirms, and which of
	// them the region set delivers.
	Facts []TriggerFact
	// RepoWide is true when the computed set is repository-wide.
	RepoWide bool
	// Broader is true for a change that reaches a path the recorded set
	// does not reach. A preview shows it as a warning.
	Broader bool
	// Narrower is true for a change that no longer reaches a path the
	// recorded set reaches.
	Narrower bool
}

// errScopeNoIndex reports a call without a store. The co-change history
// that confirms triggers lives in the index, so no advice exists without it.
var errScopeNoIndex = errors.New("distill: scope advice needs the index")

// RecommendScope computes the scope that a retarget writes for p. No
// living finding gives ScopeNoEvidence. Otherwise RecomputeRegions gives
// the set. A set equal to p.RegionSet() under the pin-key equivalence
// gives ScopeCurrent. living holds the cited findings that are still in
// the index. It writes nothing; it reads the store (co-change history)
// and the working tree under root.
func RecommendScope(st *store.Store, root string, p model.Proposal, living []model.Finding) (ScopeAdvice, error) {
	if len(living) == 0 {
		return ScopeAdvice{Kind: ScopeNoEvidence}, nil
	}

	if st == nil {
		return ScopeAdvice{}, errScopeNoIndex
	}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	if err != nil {
		return ScopeAdvice{}, err
	}

	advice := ScopeAdvice{Kind: ScopeCurrent, Regions: regions, Facts: facts, RepoWide: len(regions) == 0}
	current := p.RegionSet()

	// The health advisory of internal/proposals compares with the same
	// pin-key equivalence. That package imports this one, so the rule is
	// spelled here too.
	if reviews.NewPinKey("x", "", regions) == reviews.NewPinKey("x", "", current) {
		return advice, nil
	}

	advice.Kind = ScopeChanged
	advice.Broader = reaches(regions, current)
	advice.Narrower = reaches(current, regions)

	return advice, nil
}

// reaches reports whether set next delivers to a path that set cur does
// not. An empty set is repository-wide: it reaches every path, and no
// set reaches beyond it.
func reaches(next, cur []string) bool {
	if len(cur) == 0 {
		return false
	}

	if len(next) == 0 {
		return true
	}

	for _, n := range next {
		n = strings.TrimSuffix(n, "/")
		covered := false

		for _, c := range cur {
			if reviews.RegionMatches(strings.TrimSuffix(c, "/"), n) {
				covered = true

				break
			}
		}

		if !covered {
			return true
		}
	}

	return false
}
