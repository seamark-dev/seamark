package distill

import (
	"errors"
	"fmt"
	"os"
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
// can start anywhere. An absolute input must be inside root, also when
// it reaches root through a link above root. The empty input, ".", "./",
// and root itself all mean the whole repository and return "". The
// result uses slashes and has no trailing slash.
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
// the path is inside root. The comparison is lexical first. Then only an
// alias of root itself counts, for example /var/x for /private/var/x.
// The function never resolves the named path. A deleted path must still
// compare, and a link below root must stay visible as a link.
func regionInside(root, abs string) (string, bool) {
	abs = filepath.Clean(abs)

	roots := []string{filepath.Clean(root)}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, resolved)
	}

	for _, r := range roots {
		if rel, ok := relInside(r, abs); ok {
			return rel, true
		}
	}

	return aliasInside(root, abs)
}

// relInside returns the lexical form of abs relative to dir when abs is
// dir or below it.
func relInside(dir, abs string) (string, bool) {
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return "", false
	}

	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}

	return rel, true
}

// aliasInside finds the ancestor of abs that is the same directory as
// root, and returns the lexical rest of abs below it. The walk starts at
// the file system root. Thus it meets the alias of root before any path
// below root, and it follows no link below root. os.SameFile also
// matches an alias that differs only in letter case, because git prints
// the top level in the letter case of the disk. The walk ends with abs
// itself, so a link that names root still means the whole repository.
func aliasInside(root, abs string) (string, bool) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", false
	}

	var chain []string

	for p := abs; ; p = filepath.Dir(p) {
		chain = append(chain, p)

		if filepath.Dir(p) == p {
			break
		}
	}

	for i := len(chain) - 1; i >= 0; i-- {
		info, err := os.Stat(chain[i])
		if err != nil {
			// A missing or unreadable ancestor hides every path below
			// it, so no later ancestor can be root.
			return "", false
		}

		if os.SameFile(info, rootInfo) {
			return relInside(chain[i], abs)
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
