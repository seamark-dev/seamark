package distill

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// DefaultGroupLimit is the number of new evidence groups one run sends
// when the operator names no limit. Each group is one agent call, so the
// default bounds what a run costs. The CLI flag and the console share it.
const DefaultGroupLimit = 10

// ErrRegionEscapes reports a region outside the workspace root. Such a
// region matches no finding, so accepting it would plan an empty run
// that looks like "nothing new" instead of a mistyped path.
var ErrRegionEscapes = errors.New("the region is outside the workspace")

// NormalizeRegion turns operator input into the repository-relative
// region form that PlanOptions.Region expects. A relative input is
// relative to root, not to the process directory, because the console
// can start anywhere. An absolute input must be inside root. The empty
// input, ".", "./", and root itself all mean the whole repository and
// return "". The result uses slashes and has no trailing slash.
//
// The CLI keeps its own lenient toRepoRel: its output must not change.
func NormalizeRegion(root, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil
	}

	if filepath.IsAbs(input) {
		rel, ok := regionInside(root, input)
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrRegionEscapes, input)
		}

		input = rel
	}

	clean := path.Clean(filepath.ToSlash(input))

	switch {
	case clean == ".":
		return "", nil
	case clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean):
		return "", fmt.Errorf("%w: %s", ErrRegionEscapes, input)
	}

	return clean, nil
}

// regionInside returns the root-relative form of an absolute path when
// the path is inside root. It retries with resolved symbolic links,
// because a root such as /var/... can also appear as /private/var/...
func regionInside(root, abs string) (string, bool) {
	roots := []string{filepath.Clean(root)}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, resolved)
	}

	targets := []string{filepath.Clean(abs)}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		targets = append(targets, resolved)
	}

	for _, r := range roots {
		for _, t := range targets {
			rel, err := filepath.Rel(r, t)
			if err != nil {
				continue
			}

			rel = filepath.ToSlash(rel)
			if rel != ".." && !strings.HasPrefix(rel, "../") {
				return rel, true
			}
		}
	}

	return "", false
}

// PinsFromConfig converts the pins of .seamark/lessons.yaml into the
// proposal shape that deduplication reads. The CLI and the console both
// call it, so a pin counts as known in the same way on both surfaces.
func PinsFromConfig(cfg *reviews.Config) []model.Proposal {
	if cfg == nil {
		return []model.Proposal{}
	}

	pins := make([]model.Proposal, 0, len(cfg.Pin))
	for _, p := range cfg.Pin {
		// AllRegions resolves the union once at the boundary, so a
		// hand-written pin carrying both keys governs everywhere it says.
		pins = append(pins, model.Proposal{
			Rule: p.Rule, Note: p.Note, Region: p.Region, Regions: p.AllRegions(),
		})
	}

	return pins
}
