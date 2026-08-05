// Package confidence computes a deterministic evidence-health tier for
// distilled proposals (RFC-002 §6) — a leaf package, because both the
// distiller's surfaces and the report layer must reach one answer and
// neither may import the other.
package confidence

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

// The tier is computed from data already in the store — never stored,
// never model-scored. The inputs decay on their own (findings age out
// of the mining window, cited files get deleted), so a pin's tier
// honestly weakens as its evidence does, with nothing to migrate.
// Presentation is tiered, never numeric: a 0.73 invites false
// precision, while "weak: 1 event, evidence gone" states exactly what
// a reader can check.

// Tier is a proposal's evidence-health class.
type Tier int

// The tiers, weakest first so ranking can compare numerically.
const (
	TierWeak Tier = iota
	TierFair
	TierStrong
)

// String renders the tier the way every surface spells it.
func (t Tier) String() string {
	switch t {
	case TierStrong:
		return "strong"
	case TierWeak:
		return "weak"
	default:
		return "fair"
	}
}

// The thresholds, calibrated against the two corpora that motivated
// confidence (RFC-002 §6): the 34 single-event pins distilled before
// the recurrence rule must score weak, and a multi-source recent
// pattern must score strong.
const (
	strongMinEvents  = 3
	strongMinSources = 2
	strongMaxAge     = 180 * 24 * time.Hour
	weakMaxAge       = 540 * 24 * time.Hour
)

// Facts are the measured inputs behind a tier — printed next to it, so
// the tier is an argument, not a verdict.
type Facts struct {
	// Cited is how many findings the proposal cited when distilled;
	// Living how many still exist in the store (findings age out with
	// the mining window).
	Cited, Living int
	// Events is the distinct-event count over the LIVING citations,
	// under the same collapse rule as the recurrence bar.
	Events int
	// Sources are the distinct evidence kinds among living citations,
	// fix tiers folded ("review", "fix"), sorted.
	Sources []string
	// NewestAge is the age of the newest living citation; zero when
	// nothing lives. Meaningful only when AgeKnown.
	NewestAge time.Duration
	// AgeKnown is false when no living citation carries a usable
	// timestamp — recency is then unknown, which is not the same as
	// "brand new".
	AgeKnown bool
	// DeadPaths counts living citations whose file no longer exists in
	// the workspace — guidance about deleted code.
	DeadPaths int
}

// Line renders the facts for a parenthetical: "2 events, review+fix,
// newest 14d" (plus decay notes when they apply).
func (f Facts) Line() string {
	if f.Living == 0 {
		return "evidence aged out of the store"
	}

	age := "age unknown"
	if f.AgeKnown {
		age = fmt.Sprintf("newest %dd", int(f.NewestAge.Hours()/24))
	}

	parts := []string{
		fmt.Sprintf("%d event(s)", f.Events),
		strings.Join(f.Sources, "+"),
		age,
	}

	if f.DeadPaths > 0 {
		parts = append(parts, fmt.Sprintf("%d cited file(s) deleted", f.DeadPaths))
	}

	if f.Living < f.Cited {
		parts = append(parts, fmt.Sprintf("%d/%d citations living", f.Living, f.Cited))
	}

	return strings.Join(parts, ", ")
}

// Assess computes a proposal's tier and the facts behind it. byID is
// the current finding table (AllFindings keyed by id); root locates the
// workspace for path liveness; now anchors ages so tests are exact.
func Assess(p model.Proposal, byID map[int64]model.Finding, root string, now time.Time) (Tier, Facts) {
	var living []model.Finding

	for _, id := range p.Members {
		if f, ok := byID[id]; ok {
			living = append(living, f)
		}
	}

	facts := Facts{Cited: len(p.Members), Living: len(living)}

	if len(living) == 0 {
		return TierWeak, facts
	}

	facts.Events = model.CountEvents(living)

	kinds := map[string]bool{}

	var newest int64

	for _, f := range living {
		kind := f.Source
		if kind == "" {
			kind = model.SourceReview // pre-migration rows read as review
		}

		if strings.HasPrefix(kind, "fix") || kind == model.SourceRevert {
			kind = "fix"
		}

		kinds[kind] = true

		if f.CreatedAt > newest {
			newest = f.CreatedAt
		}

		// The primary path is the citation's semantic home; only a
		// confirmed absence means the guidance points at deleted code —
		// a permission or I/O error says nothing about the file.
		if root != "" {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.Path))); errors.Is(err, fs.ErrNotExist) {
				facts.DeadPaths++
			}
		}
	}

	for kind := range kinds {
		facts.Sources = append(facts.Sources, kind)
	}

	sort.Strings(facts.Sources)

	if newest > 0 {
		facts.NewestAge = now.Sub(time.Unix(newest, 0))
		facts.AgeKnown = true
	}

	switch {
	case facts.Events < 2,
		facts.DeadPaths == facts.Living,
		facts.AgeKnown && facts.NewestAge > weakMaxAge:
		return TierWeak, facts
	case facts.Events >= strongMinEvents &&
		len(facts.Sources) >= strongMinSources &&
		// Unknown recency must not read as recent: strong demands a
		// KNOWN fresh timestamp.
		facts.AgeKnown && facts.NewestAge <= strongMaxAge &&
		facts.DeadPaths == 0:
		return TierStrong, facts
	default:
		return TierFair, facts
	}
}
