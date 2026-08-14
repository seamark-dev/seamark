package distill

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

const (
	// scopePartnerRank bounds how many co-change partners per
	// evidence file, ordered by shared commits. Rank 1 is the common
	// case. Five tolerates churn-heavy neighbors above the real pair.
	scopePartnerRank = 5

	// scopeMinTogether and scopeMinLift set the minimum evidence for
	// one partner edge. A pair with fewer than 5 shared commits is not
	// a pattern. A pair with lift below 3 usually shows only general
	// churn: large mechanical commits and frequently changed files
	// connect many unrelated pairs. Both thresholds come from the
	// 2026-08 corpus audit: the true trigger pair had lift 6.6, and
	// the noise floor was near 2.
	scopeMinTogether = 5
	scopeMinLift     = 3.0
)

// ScopeAdvisory says that a pin's delivery regions likely miss the
// trigger site. Two signals must agree on the same place: the note
// names a repo path outside the regions, and the co-change
// history of a cited evidence file points at it too. One signal
// alone is not enough.
type ScopeAdvisory struct {
	// NotePath is the path the note names, outside the regions. It
	// exists in the working tree — a missing path is never a target.
	NotePath string

	// Partner is the co-change partner file that agrees with
	// NotePath: equal to it, or under it when NotePath is a
	// directory. Co-change names files; notes often name directories.
	Partner string

	// Evidence is the cited finding file whose partner agreed.
	Evidence string

	// Together counts the shared commits on the Evidence–Partner
	// edge, so the printed advisory can show its strength.
	Together int

	// Suggested is the pin's current region set with the trigger
	// region appended. Regions the trigger region contains leave
	// first: delivery regions form a union, so a contained region
	// adds nothing and wastes a cap slot. Survivors keep their order
	// — order only feeds the legacy region: field; pin identity
	// sorts regions. Nil when the result would still pass
	// maxRegions: the user decides what to remove.
	Suggested []string
}

// Line renders the advisory sentence
func (a ScopeAdvisory) Line() string {
	target := a.Partner
	if target == a.NotePath {
		target = "it"
	}

	s := fmt.Sprintf(
		"delivery may miss the trigger: the note names %s (outside the regions) and evidence %s co-changes with %s (%d shared commits)",
		a.NotePath, a.Evidence, target, a.Together,
	)

	if len(a.Suggested) > 0 {
		s += fmt.Sprintf(" — consider regions: [%s]", strings.Join(a.Suggested, ", "))
	}

	return s
}

// AuditScope runs the trigger-scope check for one pin. regions
// must be the pin's live region set; nil means repo-wide, which
// already delivers everywhere, so the check reports nothing. When
// several partner edges agree, the one with the most shared commits
// wins. ok is false when the signals do not agree — the common case.
func AuditScope(st *store.Store, root, note string, regions []string, cited []model.Finding) (ScopeAdvisory, bool, error) {
	if len(regions) == 0 {
		return ScopeAdvisory{}, false, nil
	}

	notePaths := extractPathsFromNote(note, root, regions)
	if len(notePaths) == 0 {
		return ScopeAdvisory{}, false, nil
	}

	var best ScopeAdvisory
	seen := make(map[string]struct{})

	for _, f := range cited {
		fPaths := f.Paths
		if len(f.Paths) == 0 {
			fPaths = []string{f.Path}
		}

		for _, evidencePath := range fPaths {
			if _, exists := seen[evidencePath]; exists {
				continue
			}

			seen[evidencePath] = struct{}{}

			partners, err := st.CoChangePartners(evidencePath, scopeMinLift, scopePartnerRank)
			if err != nil {
				return ScopeAdvisory{}, false, fmt.Errorf(
					"scope audit: co-change partners for %q: %w",
					evidencePath,
					err,
				)
			}

			for _, partner := range partners {
				if partner.Together < scopeMinTogether {
					continue
				}

				if model.IsTestPath(partner.File) ||
					model.IsDocPath(partner.File) ||
					isInsideRegions(partner.File, regions) {
					continue
				}

				var matchingNotePath string

				for _, notePath := range notePaths {
					if partner.File == notePath ||
						strings.HasPrefix(partner.File, notePath+"/") {
						matchingNotePath = notePath
						break
					}
				}

				if matchingNotePath == "" {
					continue
				}

				if best.Partner != "" && partner.Together <= best.Together {
					continue
				}

				best = ScopeAdvisory{
					NotePath: matchingNotePath,
					Partner:  partner.File,
					Evidence: evidencePath,
					Together: partner.Together,
				}
			}
		}
	}

	if best.Partner == "" {
		return ScopeAdvisory{}, false, nil
	}

	best.Suggested = widenRegions(regions, triggerRegion(best.Partner))

	return best, true, nil
}

// triggerRegion is the delivery region for a trigger path: its
// directory, capped at maxRegionDepth. Empty for root-level paths —
// no region can express them.
func triggerRegion(p string) string {
	ancestors := regionAncestors(path.Dir(p))
	if len(ancestors) == 0 {
		return ""
	}

	return ancestors[len(ancestors)-1]
}

// widenRegions unions the trigger region into a region set. Regions
// the trigger region contains leave first: delivery regions form a
// union, so a contained region adds nothing and wastes a cap slot.
// Survivors keep their order. Nil when the trigger is empty or the
// result would still pass maxRegions — the user decides what to
// remove.
func widenRegions(regions []string, trigger string) []string {
	if trigger == "" {
		return nil
	}

	kept := make([]string, 0, len(regions)+1)

	for _, r := range regions {
		if r == trigger || strings.HasPrefix(r, trigger+"/") {
			continue
		}

		kept = append(kept, r)
	}

	if len(kept) >= maxRegions {
		return nil
	}

	return append(kept, trigger)
}

// AuditScopes runs the trigger-scope check for a set of proposals.
// Pending rows use their stored note and regions. Applied rows use
// the live yaml pin's note — a reworded note is what fires — and
// rows whose pin is pruned or re-scoped by hand are skipped. Only
// living findings feed the check. Keyed by proposal ID; unflagged
// proposals are absent.
func AuditScopes(st *store.Store, cfg *reviews.Config, root string,
	ps []model.Proposal, meta map[int64]model.Finding,
) (map[int64]ScopeAdvisory, error) {
	out := make(map[int64]ScopeAdvisory, len(ps))
	if len(ps) == 0 {
		return out, nil
	}

	for _, p := range ps {
		var living []model.Finding

		for _, id := range p.Members {
			if f, ok := meta[id]; ok {
				living = append(living, f)
			}
		}

		if len(living) == 0 {
			continue
		}

		note, regions := p.Note, p.RegionSet()

		switch p.Status {
		case model.ProposalProposed:
		// Stored note and regions: they are what --apply installs.
		case model.ProposalApplied:
			pin, live := cfg.FindPin(reviews.NewPinKey(p.Rule, p.Region, p.Regions))
			if !live {
				// Pruned or re-scoped by hand: the decided delivery
				// no longer exists under this key.
				continue
			}

			// The live pin wins: a reworded note is what fires.
			note, regions = pin.Note, pin.AllRegions()
		default:
			continue
		}

		scope, flaagged, err := AuditScope(st, root, note, regions, living)
		if err != nil {
			return nil, fmt.Errorf("scope audit for proposal %d: %w", p.ID, err)
		}

		if flaagged {
			out[p.ID] = scope
		}
	}

	return out, nil
}

func extractPathsFromNote(note, root string, regions []string) []string {
	var res []string
	seen := make(map[string]struct{})

	for token := range strings.FieldsSeq(note) {
		if !strings.Contains(token, "/") {
			continue
		}

		token = trimNotePath(token)

		if token == "" ||
			path.IsAbs(token) ||
			filepath.IsAbs(filepath.FromSlash(token)) ||
			strings.Contains(token, "..") {
			continue
		}

		filePath := path.Clean(token)

		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(filePath))); err != nil {
			continue
		}

		if isInsideRegions(filePath, regions) {
			continue
		}

		if _, exists := seen[filePath]; exists {
			continue
		}

		seen[filePath] = struct{}{}
		res = append(res, filePath)
	}

	return res
}

func isInsideRegions(filePath string, regions []string) bool {
	for _, region := range regions {
		region = strings.TrimRight(region, "/")

		if region == "" || region == "*" || filePath == region ||
			strings.HasPrefix(filePath, region+"/") {
			return true
		}
	}

	return false
}

func trimNotePath(token string) string {
	token = strings.TrimRight(token, ".,;:")
	token = strings.Trim(token, "`'\"()")
	token = strings.TrimRight(token, ".,;:")
	token = strings.TrimSuffix(token, "/*")
	return strings.TrimSuffix(token, "/")
}
