// Package outcome implements the passive loop: for each
// applied pin that has fired at least once, it checks whether the
// pin's mistake recurred after agents started seeing it. Everything is
// recomputed on every call from the firing log, the finding table, and
// mined history; nothing is stored.
package outcome

import (
	"fmt"
	"time"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Verdict is the derived outcome for a single applied pin.
type Verdict int

const (
	// VerdictUntested means there is not enough evidence to judge the pin. See
	// Reason for which evidence is missing.
	VerdictUntested Verdict = iota
	// VerdictWorking means the pin fired, the region saw enough commits,
	// and the mistake did not recur. Validation only — not a signal
	// to remove the pin.
	VerdictWorking
	// VerdictNotLanding means the pin fired and the mistake recurred
	// anyway. The one verdict that calls for action.
	VerdictNotLanding
)

func (v Verdict) String() string {
	switch v {
	case VerdictWorking:
		return "working"
	case VerdictNotLanding:
		return "not landing"
	default:
		return "untested"
	}
}

// Reason says why a verdict is untested, so Line() can name the
// missing evidence in the rendered sentence.
type Reason int

const (
	// ReasonMeasured means all checks passed; the verdict is working or not landing.
	ReasonMeasured Reason = iota
	// ReasonNeverFired means there is no firing under the pin's current identity.
	ReasonNeverFired
	// ReasonLowActivity means fewer than MinExposureActivity region-commits exist since exposure.
	ReasonLowActivity
	// ReasonStaleEvidence means findings were not mined since exposure.
	ReasonStaleEvidence
	// ReasonDeadCitations means all cited findings aged out of the mining window.
	ReasonDeadCitations
)

// MinExposureActivity is the minimum number of post-exposure commits
// in the pin's regions before a "working" claim is allowed. Below it
// the verdict is untested: nothing touched the region, so the absence
// of the mistake proves nothing.
const MinExposureActivity = 5

// Reading is the derived exposure and verdict for a single proposal.
type Reading struct {
	ProposalID  int64
	Exposed     bool      // at least one firing under the pin's current identity
	First       time.Time // exposure start: the first firing; zero when !Exposed
	Firings     int       // delivered firing records naming the pin
	Matches     int       // delivered plus suppressed records naming the pin
	PreEvents   int       // mistake events before exposure (the pin's evidence)
	PreCommits  int       // commits in the pin's regions before exposure
	PostEvents  int       // mistake events since exposure — the recurrence count
	PostCommits int       // commits in the pin's regions since exposure
	Verdict     Verdict
	Reason      Reason // set when Verdict is untested
}

// Line renders the verdict sentence. All surfaces (ledger, --stats,
// HTML report) print this one string, so they never phrase the same
// reading differently. The output is fixed words and numbers only, so
// callers do not need to sanitize it.
func (r Reading) Line() string {
	exposure := r.exposureText()

	switch {
	case !r.Exposed:
		return "untested — never fired"
	case r.Verdict == VerdictNotLanding:
		return fmt.Sprintf("not landing — recurred %d× since exposure (%s)",
			r.PostEvents, exposure)
	case r.Verdict == VerdictUntested:
		switch r.Reason {
		case ReasonDeadCitations:
			return fmt.Sprintf("untested — citations aged out of the mining window (%s)", exposure)
		case ReasonStaleEvidence:
			return fmt.Sprintf("untested — evidence not mined since exposure (%s)", exposure)
		default:
			return fmt.Sprintf("untested — %s since exposure (%s)",
				regionCommits(r.PostCommits), exposure)
		}
	default:
		return fmt.Sprintf("working — flagged %d× in ~%s before exposure; "+
			"%d× in %d since (%s)",
			r.PreEvents, regionCommits(r.PreCommits), r.PostEvents, r.PostCommits, exposure)
	}
}

func (r Reading) exposureText() string {
	if r.Matches > r.Firings {
		return fmt.Sprintf("delivered %d×; matched %d×", r.Firings, r.Matches)
	}

	return fmt.Sprintf("fired %d×", r.Firings)
}

// regionCommits formats a commit count with its unit, singular for 1.
func regionCommits(n int) string {
	if n == 1 {
		return "1 region-commit"
	}

	return fmt.Sprintf("%d region-commits", n)
}

// Gather computes a Reading for every applied proposal whose pin is
// still present in lessons.yaml and cites at least one finding. The
// result is keyed by proposal id; proposals that cannot be measured
// (pruned pin, no citations) are absent, and callers print nothing
// for them.
func Gather(st *store.Store, cfg *reviews.Config, applied []model.Proposal,
	firings []reviews.Firing,
) (map[int64]Reading, error) {
	if len(applied) == 0 {
		return nil, nil
	}

	all, err := st.AllFindings()
	if err != nil {
		return nil, fmt.Errorf("failed to load findings: %w", err)
	}

	// Groups are not persisted anywhere, so re-derive them once here;
	// the same run serves every pin's fix-side join below.
	groups := distill.NewLexicalGrouper().Group(all)
	links := indexFindingLinks(all, groups)
	exposure := reviews.FirstFirings(firings)

	out := make(map[int64]Reading, len(applied))

	minedThrough := st.EvidenceHorizon()

	for _, p := range applied {
		if len(p.Members) == 0 {
			continue
		}

		pin, ok := cfg.FindPin(reviews.NewPinKey(p.Rule, p.Region, p.Regions))
		if !ok {
			continue
		}

		// Identity, note, and regions come from the live yaml pin, not
		// the proposal: the yaml wording is what the firing log saw,
		// and a hand-edit or retarget moves the measurement with it.
		exp, exposed := exposure[reviews.PinIdentity(pin)]
		var r Reading

		if !exposed {
			r = assess(exp, false, nil, 0, 0, 0)
		} else {
			first := exp.First.Unix()

			// first+1 keeps the two clocks consistent: assess files a
			// finding at CreatedAt == first under "before", so a commit
			// at ts == first must count as "before" too.
			pre, err := st.CountCommitsTouching(pin.AllRegions(), 0, first+1)
			if err != nil {
				return nil, fmt.Errorf("failed to count pre-exposure commits for %s: %w", pin, err)
			}

			post, err := st.CountCommitsTouching(pin.AllRegions(), first+1, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to count post-exposure commits for %s: %w", pin, err)
			}

			r = assess(exp, true, linked(p.Members, links), pre, post, minedThrough)
		}

		r.ProposalID = p.ID
		out[p.ID] = r

	}

	return out, nil
}

// findingLinks indexes the two one-hop relationships used by outcome
// measurement. all retains store order so linked returns byte-for-byte the same
// ordering as the finding corpus after selecting through either index.
type findingLinks struct {
	all               []model.Finding
	byID              map[int64]model.Finding
	byKey             map[string][]int64
	groupsByFindingID map[int64][]int64
}

func indexFindingLinks(all []model.Finding, groups []distill.Group) findingLinks {
	links := findingLinks{
		all:               all,
		byID:              make(map[int64]model.Finding, len(all)),
		byKey:             make(map[string][]int64),
		groupsByFindingID: make(map[int64][]int64),
	}

	for _, f := range all {
		links.byID[f.ID] = f
		if f.LessonKey != "" {
			links.byKey[f.LessonKey] = append(links.byKey[f.LessonKey], f.ID)
		}
	}

	for _, group := range groups {
		// Area groups bucket findings by location rather than shared mistake.
		if group.Area {
			continue
		}

		var fixes []int64
		for _, f := range group.Findings {
			if f.LessonKey == "" {
				fixes = append(fixes, f.ID)
			}
		}

		for _, f := range group.Findings {
			links.groupsByFindingID[f.ID] = append(links.groupsByFindingID[f.ID], fixes...)
		}
	}

	return links
}

// linked returns the findings tied to a pin's mistake, through two
// joins: review findings that share a cluster key with a cited
// finding, and fix findings that share a lexical group with one (fix
// findings have no cluster key, so the group is their only link).
// Both joins reach one hop from the citations; there is no transitive
// chaining. The result has no duplicates and keeps the id order of
// the findings table.
func linked(members []int64, links findingLinks) []model.Finding {
	want := make(map[int64]bool, len(members))

	for _, id := range members {
		want[id] = true

		if f, ok := links.byID[id]; ok && f.LessonKey != "" {
			for _, linkedID := range links.byKey[f.LessonKey] {
				want[linkedID] = true
			}
		}

		for _, linkedID := range links.groupsByFindingID[id] {
			want[linkedID] = true
		}
	}

	var out []model.Finding

	for _, f := range links.all {
		if want[f.ID] {
			out = append(out, f)
		}
	}

	return out
}

// assess combines exposure, linked findings, and commit activity into
// one Reading. minedThrough is the timestamp the finding corpus is
// current through (store.EvidenceHorizon).
func assess(exp reviews.Exposure, exposed bool, linked []model.Finding,
	preCommits, postCommits int, minedThrough int64,
) Reading {
	if !exposed {
		return Reading{Verdict: VerdictUntested, Reason: ReasonNeverFired}
	}

	cutoff := exp.First.Unix()
	var pre, post []model.Finding

	for _, f := range linked {
		// <= : a finding at the exposure instant counts as "before".
		// Blaming the pin requires the reminder to have preceded the
		// mistake.
		if f.CreatedAt <= cutoff {
			pre = append(pre, f)
			continue
		}

		post = append(post, f)
	}

	r := Reading{
		Exposed:     true,
		First:       exp.First,
		Firings:     exp.Count,
		Matches:     exp.Matches,
		PreEvents:   model.CountEvents(pre),
		PreCommits:  preCommits,
		PostEvents:  model.CountEvents(post),
		PostCommits: postCommits,
	}

	// The order matters. A recorded recurrence is direct evidence and
	// wins over every missing-evidence check. The remaining cases ask
	// whether the ABSENCE of a recurrence means anything: not without
	// living citations, not if mining never ran after exposure, and
	// not in a region nothing touched.
	switch {
	case r.PostEvents > 0:
		r.Verdict = VerdictNotLanding
	case r.PreEvents == 0:
		r.Verdict, r.Reason = VerdictUntested, ReasonDeadCitations
	case minedThrough <= cutoff:
		r.Verdict, r.Reason = VerdictUntested, ReasonStaleEvidence
	case r.PostCommits < MinExposureActivity:
		r.Verdict, r.Reason = VerdictUntested, ReasonLowActivity
	default:
		r.Verdict = VerdictWorking
	}

	return r
}
