package proposals

import (
	"fmt"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Health re-judges proposals under TODAY'S rules (RFC-002 §7):
// confidence tier over living evidence, the prompt era when the row
// predates the recurrence rules, and the regions current inference
// would assign. Purely derived — recomputed on every ask, stored
// nowhere, acted on only by explicit commands. It moved from
// internal/cli, so `seamark lessons --proposals` and the console show
// the same advisories. The error texts stay as the CLI printed them.
func Health(st *store.Store, root string, ps []model.Proposal) (map[int64]report.ProposalHealth, error) {
	if len(ps) == 0 {
		return nil, nil
	}

	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		return nil, fmt.Errorf("lessons proposalHealth: reading .seamark/lessons.yaml: %w", err)
	}

	var cited []int64

	for _, p := range ps {
		cited = append(cited, p.Members...)
	}

	meta, err := st.FindingsMetaByIDs(cited)
	if err != nil {
		return nil, err
	}

	advisories, err := distill.AuditScopes(st, cfg, root, ps, meta)
	if err != nil {
		return nil, fmt.Errorf("lessons proposalHealth: auditing scopes: %w", err)
	}

	now := time.Now()
	out := make(map[int64]report.ProposalHealth, len(ps))
	var appliedPs []model.Proposal

	for _, p := range ps {
		tier, facts := confidence.Assess(p, meta, root, now)

		h := report.ProposalHealth{Tier: tier.String(), Facts: facts.Line()}

		if adv, ok := advisories[p.ID]; ok {
			h.Scope = adv.Line()
		}

		if strings.HasSuffix(p.Agent, "/v1") {
			h.Era = "distilled under prompt v1, before the same-PR-counts-once rule"
		}

		// The liveness rule every applied-pin surface uses: a row whose
		// pin left lessons.yaml delivers nothing, so region advice is
		// suppressed and the state is named instead.
		if p.Status == model.ProposalApplied {
			if _, live := cfg.FindPin(reviews.ProposalKey(p)); !live {
				h.Pruned = true
			}
		}

		var living []model.Finding

		for _, id := range p.Members {
			if f, ok := meta[id]; ok {
				living = append(living, f)
			}
		}

		if len(living) > 0 && !h.Pruned {
			// One recompute for every "regions now" reader: verified
			// trigger scopes when available, evidence coverage otherwise.
			recomputed, facts, err := distill.RecomputeRegions(st, root, p, living)
			if err != nil {
				return nil, err
			}

			if !RegionSetsEqual(recomputed, p.RegionSet()) {
				h.Retarget = strings.Join(recomputed, ", ")
				if h.Retarget == "" {
					h.Retarget = "*"
				}
			}

			// A blocked trigger produces no drift and no advisory —
			// without this line, a confirmed delivery miss is
			// invisible everywhere after the extraction summary.
			for _, f := range facts {
				if line := f.BlockedLine(); line != "" {
					if h.Blocked != "" {
						h.Blocked += "; "
					}

					h.Blocked += line
				}
			}
		}

		if p.Status == model.ProposalApplied {
			appliedPs = append(appliedPs, p)
		}

		out[p.ID] = h
	}

	// Passive-loop verdicts for the applied subset. Pins
	// Gather cannot measure get no Outcome, and the printer renders
	// nothing for them.
	if len(appliedPs) > 0 {
		readings, err := appliedOutcomes(st, root, appliedPs)
		if err != nil {
			return nil, err
		}

		for id, r := range readings {
			h := out[id]
			h.Outcome = r.Line()
			h.Escalate = r.Verdict == outcome.VerdictNotLanding
			out[id] = h
		}
	}

	return out, nil
}

// RegionSetsEqual compares two region sets order-insensitively, the
// same equivalence the pin identity uses. The health advisory and the
// CLI retarget share it, so both call the same sets equal.
func RegionSetsEqual(a, b []string) bool {
	return reviews.NewPinKey("x", "", a) == reviews.NewPinKey("x", "", b)
}

// appliedOutcomes loads the lessons config and the firing log and
// measures the applied pins with MeasureOutcomes. Health is its only
// caller and needs only the readings, so it returns no config and no
// firings. A malformed config is an error; it never falls back to
// defaults, because defaults would judge firings against rules the
// agent never received. It moved from internal/cli, and the error
// texts stay as the CLI printed them.
func appliedOutcomes(st *store.Store, root string, applied []model.Proposal) (
	map[int64]outcome.Reading, error,
) {
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		return nil, fmt.Errorf("failed to load lessons config: %w", err)
	}

	firings, err := reviews.ReadFirings(root)
	if err != nil {
		return nil, fmt.Errorf("failed to read firing log: %w", err)
	}

	return MeasureOutcomes(st, cfg, applied, firings)
}

// MeasureOutcomes measures the applied pins against a lessons config
// and firing records that the caller already read. Health uses it
// through appliedOutcomes. lessonstats.Gather reads the firing log once
// for all of its sections, so its delivery counts and its outcomes come
// from the same records. The error text stays as the CLI printed it.
func MeasureOutcomes(st *store.Store, cfg *reviews.Config, applied []model.Proposal,
	firings []reviews.Firing,
) (map[int64]outcome.Reading, error) {
	readings, err := outcome.Gather(st, cfg, applied, firings)
	if err != nil {
		return nil, fmt.Errorf("failed to gather applied proposal outcomes: %w", err)
	}

	return readings, nil
}
