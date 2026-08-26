package distill

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// Trigger paths: where a mistake is MADE, distinct from where its
// evidence may also be repaired. The distiller names them; the harness
// verifies syntax and existence, then requires either a direct citation
// or co-change confirmation. Verified triggers become the delivery
// regions; evidence coverage remains the conservative fallback.

// maxTriggerPaths bounds both the model reply and the eventual delivery
// union. It matches maxRegions, so every valid answer can remain
// expressible without silently discarding a trigger.
const maxTriggerPaths = model.MaxTriggerPaths

// cleanTriggerPaths is the syntax rung: trim wrapping, drop absolute
// and parent-escaping paths, normalize, dedupe, cap. Pure — existence
// and history run in later rungs.
func cleanTriggerPaths(paths []string) []string {
	var out []string

	seen := make(map[string]struct{}, len(paths))

	for _, p := range paths {
		p = trimNotePath(strings.TrimSpace(p))

		if p == "" ||
			path.IsAbs(p) ||
			filepath.IsAbs(filepath.FromSlash(p)) ||
			strings.Contains(p, "..") {
			continue
		}

		p = path.Clean(p)

		if _, dup := seen[p]; dup {
			continue
		}

		seen[p] = struct{}{}
		out = append(out, p)

		if len(out) == maxTriggerPaths {
			break
		}
	}

	return out
}

// validateTriggerPaths is the existence rung: keep the paths present
// in the working tree. An empty root drops everything — without a
// tree to check against, no name is verified, and unverified names
// must not reach storage.
func validateTriggerPaths(root string, paths []string) []string {
	if root == "" {
		return nil
	}

	var out []string

	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			out = append(out, p)
		}
	}

	return out
}

// TriggerFact reports one stored trigger path's state under today's
// history, for the plan and ledger surfaces.
type TriggerFact struct {
	Path     string // the stored trigger path
	Region   string // its exact delivery region; "" for vanished paths
	Together int    // strongest confirming co-change edge; 0 = unconfirmed
	Direct   bool   // true when the trigger is itself cited evidence
	Selected bool   // true when the recomputed delivery set reaches it
}

// BlockedLine renders the confirmed-but-undeliverable sentence every
// surface prints — the plan, the ledger, and the HTML report must not
// phrase the same fact differently. Empty for selected or unconfirmed
// facts.
func (f TriggerFact) BlockedLine() string {
	if f.Selected || (!f.Direct && f.Together == 0) {
		return ""
	}

	confirmation := "directly cited by the evidence"
	if !f.Direct {
		confirmation = fmt.Sprintf("confirmed by co-change (%d shared commits)", f.Together)
	}

	return fmt.Sprintf("trigger %s — %s but not "+
		"deliverable: the region set is full or the path is absent from the working tree; "+
		"review the trigger or edit regions in .seamark/lessons.yaml by hand", f.Path, confirmation)
}

// RecomputeRegions returns the regions today's inference assigns a
// proposal. Verified trigger paths are the most precise delivery
// surface: a trigger is accepted when it is directly cited evidence or
// when history confirms it co-changes with that evidence. If none are
// accepted, evidence coverage is the conservative fallback. Every
// reader of "regions now" — distillation, the ledger, --retarget — must
// use this one function. root locates the working tree and rejects
// vanished trigger paths.
func RecomputeRegions(st *store.Store, root string, p model.Proposal, living []model.Finding,
) ([]string, []TriggerFact, error) {
	coverage := CoverageRegions(living)
	facts := make([]TriggerFact, 0, len(p.TriggerPaths))
	selected := make([]string, 0, min(len(p.TriggerPaths), maxRegions))

	// Directly cited triggers need no historical inference. Fetch
	// partners only when at least one surviving trigger needs it; one
	// fetch then serves all such paths.
	var partners map[string][]store.CoChangePartner
	needsHistory := false

	for _, tp := range p.TriggerPaths {
		if !triggerDirectlyCited(tp, living) {
			needsHistory = true

			break
		}
	}

	if needsHistory {
		var err error

		if partners, err = evidencePartners(st, living); err != nil {
			return nil, nil, err
		}
	}

	for _, tp := range p.TriggerPaths {
		f := TriggerFact{
			Path: tp, Region: triggerRegion(root, tp),
			Direct: triggerDirectlyCited(tp, living),
		}

		if !f.Direct {
			f.Together = confirmTrigger(partners, tp)
		}

		if (f.Direct || f.Together > 0) && f.Region != "" {
			var selectedOK bool

			selected, selectedOK = addDeliveryRegion(selected, f.Region)
			f.Selected = selectedOK
		}

		facts = append(facts, f)
	}

	if len(selected) > 0 {
		return selected, facts, nil
	}

	return coverage, facts, nil
}

// triggerDirectlyCited recognizes a trigger that points at a cited
// production path or its immediate parent directory. Arbitrary ancestors are
// not direct evidence: allowing one nested citation to bless a top-level
// directory would give an uncorroborated model answer too much delivery.
// Test and documentation paths cannot establish direct delivery.
func triggerDirectlyCited(trigger string, cited []model.Finding) bool {
	if model.IsTestPath(trigger) || model.IsDocPath(trigger) {
		return false
	}

	for _, f := range cited {
		for _, evidencePath := range relevantFindingPaths(f) {
			if model.IsTestPath(evidencePath) || model.IsDocPath(evidencePath) {
				continue
			}

			if evidencePath == trigger || path.Dir(evidencePath) == trigger {
				return true
			}
		}
	}

	return false
}

// addDeliveryRegion adds one exact trigger scope to a bounded region
// union. A parent replaces children; a child already reached by a
// parent is selected without spending another slot.
func addDeliveryRegion(regions []string, candidate string) ([]string, bool) {
	if candidate == "" {
		return regions, false
	}

	for _, region := range regions {
		if candidate == region || strings.HasPrefix(candidate, region+"/") {
			return regions, true
		}
	}

	kept := make([]string, 0, len(regions)+1)

	for _, region := range regions {
		if strings.HasPrefix(region, candidate+"/") {
			continue
		}

		kept = append(kept, region)
	}

	if len(kept) >= maxRegions {
		return regions, false
	}

	return append(kept, candidate), true
}

// evidencePartners fetches each unique evidence file's co-change
// partners, once. The result feeds every trigger confirmation of one
// proposal.
func evidencePartners(st *store.Store, cited []model.Finding) (map[string][]store.CoChangePartner, error) {
	out := map[string][]store.CoChangePartner{}

	for _, f := range cited {
		fPaths := f.Paths
		if len(fPaths) == 0 {
			fPaths = []string{f.Path}
		}

		for _, evidencePath := range fPaths {
			if _, done := out[evidencePath]; done {
				continue
			}

			partners, err := st.CoChangePartners(evidencePath, scopeMinLift, scopePartnerRank)
			if err != nil {
				return nil, err
			}

			out[evidencePath] = partners
		}
	}

	return out, nil
}

// confirmTrigger is the history rung: the strongest co-change edge
// between the cited evidence and the trigger path — the together
// count of the best partner that is the path itself or whose immediate
// parent is the named directory. Zero means history cannot confirm the
// trigger. Test and doc
// partners never confirm: they are not delivery targets. Pure over
// the fetched partners; a max, so map order cannot change the answer.
func confirmTrigger(partners map[string][]store.CoChangePartner, trigger string) int {
	best := 0

	for _, ps := range partners {
		for _, partner := range ps {
			if partner.Together < scopeMinTogether || partner.Together <= best {
				continue
			}

			if model.IsTestPath(partner.File) || model.IsDocPath(partner.File) {
				continue
			}

			if partner.File == trigger || path.Dir(partner.File) == trigger {
				best = partner.Together
			}
		}
	}

	return best
}
