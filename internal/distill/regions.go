package distill

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// Region inference for proposals (RFC-002 §1). commonDir — the deepest
// directory shared by every citation — collapses to repo-wide the
// moment evidence spans top-level trees, and measured on a real repo
// that put 54% of applied pins at `*`: every one of them taxing the
// injection budget of every edit. Three mechanical causes, each with a
// counter here:
//
//   - test and doc paths drag the common prefix to the root even when
//     every code path agrees (a fix's test out-churns it; a README
//     citation has no directory in common with anything) — those paths
//     don't vote;
//   - one theme legitimately living in TWO places (api AND db) has no
//     single-prefix answer — a small set of regions does;
//   - findings are not independent evidence — six comments on one pull
//     request must not outvote two independent events — so EVENTS vote,
//     with the same collapse rule the recurrence bar uses.

const (
	// maxRegions bounds a proposal's region set: past three, the pin is
	// repo-wide in all but name.
	maxRegions = 3
	// maxRegionDepth bounds how deep a region reaches. Directory depth
	// beyond three is spurious precision for guidance — and the deeper
	// the region, the sooner a refactor orphans it.
	maxRegionDepth = 3
	// coverTarget is the share of voting events a region set must
	// explain. 80% tolerates one outlier citation without widening to
	// repo-wide.
	coverTarget = 0.8
	// voteQuorum is the share of ALL events that must be able to vote.
	// When most events cite only tests, docs, or root files (a
	// docs-drift theme), the few code paths left are not a mandate —
	// the honest region is repo-wide.
	voteQuorum = 0.5
)

// coverageRegions computes a proposal's region set from its cited
// evidence: a small set of directories (≤ maxRegions, depth ≤
// maxRegionDepth) covering at least coverTarget of the voting events,
// chosen greedily — most new events first. Greedy is not guaranteed
// minimal in general, but under this need/cap geometry no input has
// been found where it misses a cover an exhaustive search would find,
// and it stays linear in candidates where exhaustion is combinatorial.
// Nil means repo-wide — rendered `*`, exactly as before.
func coverageRegions(cited []model.Finding) []string {
	votes, total := eventVotes(cited)

	if len(votes) == 0 || len(votes) < int(math.Ceil(voteQuorum*float64(total))) {
		return nil
	}

	// Candidate tally: how many events each directory covers, and how
	// many distinct voting paths back it — the tiebreak that keeps a
	// single-event proposal from landing on an arbitrary citation.
	events := map[string]int{}
	paths := map[string]int{}

	for _, dirs := range votes {
		for d := range dirs {
			events[d]++
			paths[d] += dirs[d]
		}
	}

	candidates := make([]string, 0, len(events))
	for d := range events {
		candidates = append(candidates, d)
	}

	sort.Strings(candidates)

	need := int(math.Ceil(coverTarget * float64(len(votes))))
	covered := map[string]bool{}

	var out []string

	for len(out) < maxRegions && len(covered) < need {
		best, bestEvents, bestPaths := "", 0, 0

		for _, d := range candidates {
			fresh := 0

			for key, dirs := range votes {
				if !covered[key] && dirs[d] > 0 {
					fresh++
				}
			}

			// Most new events; then most backing paths; then the deeper
			// region; the sorted candidate walk settles exact ties.
			if fresh > bestEvents ||
				(fresh == bestEvents && fresh > 0 && paths[d] > bestPaths) ||
				(fresh == bestEvents && fresh > 0 && paths[d] == bestPaths &&
					regionSegs(d) > regionSegs(best)) {
				best, bestEvents, bestPaths = d, fresh, paths[d]
			}
		}

		if bestEvents == 0 {
			break
		}

		out = append(out, best)

		for key, dirs := range votes {
			if dirs[best] > 0 {
				covered[key] = true
			}
		}
	}

	// The set must actually explain the evidence: three regions that
	// still miss the target mean the theme is genuinely scattered, and
	// repo-wide is the honest answer.
	if len(covered) < need {
		return nil
	}

	return out
}

// eventVotes collapses citations into events (the CountEvents key:
// findings sharing a pull request are one event) and returns each
// voting event's candidate directories with per-path backing counts,
// plus the total event count — abstainers included — for the quorum.
//
// Test paths vote only when an event has no code path at all — the
// same rule fixFiles applies to a fix's primary file: a test usually
// marks where a mistake was CAUGHT, not where it lives, but a lesson
// whose entire evidence is test files is legitimately about the tests
// (no-op assertions, unbounded fixtures), and stripping its region
// would spray it repo-wide. Docs and root-level files never vote.
func eventVotes(cited []model.Finding) (votes map[string]map[string]int, total int) {
	votes = map[string]map[string]int{}

	all := map[string]bool{}
	testDirs := map[string]map[string]int{}
	hasCode := map[string]bool{}

	for _, f := range cited {
		key := fmt.Sprintf("id:%d", f.ID)
		if f.PR > 0 {
			key = fmt.Sprintf("pr:%d", f.PR)
		}

		all[key] = true

		fPaths := f.Paths
		if len(fPaths) == 0 {
			fPaths = []string{f.Path}
		}

		for _, p := range fPaths {
			if model.IsDocPath(p) {
				continue
			}

			// Code evidence is recorded BEFORE the root-level filter: a
			// root file (main.go) has no directory to vote for, but its
			// presence means the event is about code — its tests must
			// not inherit the vote.
			if !model.IsTestPath(p) {
				hasCode[key] = true
			}

			if !strings.Contains(p, "/") {
				continue
			}

			target := votes
			if model.IsTestPath(p) {
				target = testDirs
			}

			for _, d := range regionAncestors(path.Dir(p)) {
				if target[key] == nil {
					target[key] = map[string]int{}
				}

				target[key][d]++
			}
		}
	}

	for key, dirs := range testDirs {
		if votes[key] == nil && !hasCode[key] {
			votes[key] = dirs
		}
	}

	return votes, len(all)
}

// regionAncestors lists a directory's prefixes up to maxRegionDepth:
// "a/b/c/d" → a, a/b, a/b/c.
func regionAncestors(dir string) []string {
	if dir == "." || dir == "" {
		return nil
	}

	segs := strings.Split(dir, "/")
	if len(segs) > maxRegionDepth {
		segs = segs[:maxRegionDepth]
	}

	out := make([]string, 0, len(segs))

	for i := range segs {
		out = append(out, strings.Join(segs[:i+1], "/"))
	}

	return out
}

// regionSegs is a region's depth in path segments ("" is 0).
func regionSegs(region string) int {
	if region == "" {
		return 0
	}

	return strings.Count(region, "/") + 1
}
