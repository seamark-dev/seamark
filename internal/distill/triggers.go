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

// Trigger paths: where a mistake is MADE, distinct from
// where its evidence lives. The distiller names them; the harness
// verifies them in three rungs — syntax, existence in the working
// tree, co-change confirmation. Only a confirmed trigger may widen
// delivery regions: an unverified name must not tax every edit under
// a wrong directory.

// maxTriggerPaths bounds how many trigger paths one proposal stores.
// Past three the pin has no specific trigger — it is repo-wide in all
// but name, the same reasoning as maxRegions.
const maxTriggerPaths = 3

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
	Region   string // its delivery region; "" for root-level or vanished paths
	Together int    // strongest confirming co-change edge; 0 = unconfirmed
	Widened  bool   // true when it changed the recomputed region set
	// Covered is true when the trigger region already sits inside the
	// set — confirmed and delivered, nothing to do. A confirmed fact
	// with neither Covered nor Widened was BLOCKED (region cap, or no
	// expressible region); surfaces must say so, not stay silent.
	Covered bool
}

// BlockedLine renders the confirmed-but-undeliverable sentence every
// surface prints — the plan, the ledger, and the HTML report must not
// phrase the same fact differently. Empty for facts that widened, are
// covered, or are unconfirmed.
func (f TriggerFact) BlockedLine() string {
	if f.Together == 0 || f.Widened || f.Covered {
		return ""
	}

	return fmt.Sprintf("trigger %s — confirmed by co-change (%d shared commits) but not "+
		"deliverable: the region set is full or the path has no region; "+
		"widen regions in .seamark/lessons.yaml by hand", f.Path, f.Together)
}

// RecomputeRegions returns the regions today's inference assigns a
// proposal: evidence coverage, widened by each stored trigger path
// that co-change history confirms. Every reader of "regions now" —
// distillation, the ledger, --retarget — must use this one function,
// or a retarget would silently narrow a widened delivery. Repo-wide
// coverage (nil) stays repo-wide: it already reaches every trigger,
// and widening it would NARROW delivery. root locates the working
// tree, which decides whether a trigger is a directory or a file.
func RecomputeRegions(st *store.Store, root string, p model.Proposal, living []model.Finding,
) ([]string, []TriggerFact, error) {
	regions := CoverageRegions(living)
	facts := make([]TriggerFact, 0, len(p.TriggerPaths))

	for _, tp := range p.TriggerPaths {
		f := TriggerFact{Path: tp, Region: triggerRegion(root, tp)}

		together, err := confirmTrigger(st, tp, living)
		if err != nil {
			return nil, nil, err
		}

		f.Together = together

		if together > 0 && f.Region != "" {
			switch {
			case len(regions) == 0 || isInsideRegions(f.Region, regions):
				f.Covered = true
			default:
				if widened := widenRegions(regions, f.Region); widened != nil {
					regions = widened
					f.Widened = true
				}
			}
		}

		facts = append(facts, f)
	}

	return regions, facts, nil
}

// confirmTrigger is the history rung: the strongest co-change edge
// between the cited evidence and the trigger path — the together
// count of the best partner that is the path itself or lives under
// it. Zero means history cannot confirm the trigger. Test and doc
// partners never confirm: they are not delivery targets.
func confirmTrigger(st *store.Store, trigger string, cited []model.Finding) (int, error) {
	best := 0
	seen := make(map[string]struct{})

	for _, f := range cited {
		fPaths := f.Paths
		if len(fPaths) == 0 {
			fPaths = []string{f.Path}
		}

		for _, evidencePath := range fPaths {
			if _, dup := seen[evidencePath]; dup {
				continue
			}

			seen[evidencePath] = struct{}{}

			partners, err := st.CoChangePartners(evidencePath, scopeMinLift, scopePartnerRank)
			if err != nil {
				return 0, err
			}

			for _, partner := range partners {
				if partner.Together < scopeMinTogether || partner.Together <= best {
					continue
				}

				if model.IsTestPath(partner.File) || model.IsDocPath(partner.File) {
					continue
				}

				if partner.File == trigger || strings.HasPrefix(partner.File, trigger+"/") {
					best = partner.Together
				}
			}
		}
	}

	return best, nil
}
