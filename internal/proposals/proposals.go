// Package proposals is the service layer for the proposal decision
// ledger. It has a read-only ledger view, region and text filtering,
// evidence health, and previewed decisions with revalidation.
//
// The CLI and the console share only the evidence helpers: Health,
// AppliedOutcomes and RegionSetsEqual. Both surfaces therefore judge
// proposal health, lesson outcomes and region sets by one set of rules.
//
// Only the console uses the ledger view (Load, MatchesRegion,
// MatchesRegionSet, MatchesText) and the decision flow (PreviewApply,
// PreviewDismiss, PreviewReconcile, Commit).
//
// The CLI commands `lessons --apply` and `lessons --dismiss`
// keep their own path in internal/cli, so their output stays unchanged.
// That path renders and writes pins through the same distill functions.
// Unlike Commit, it does not refuse proposals whose pins already exist.
package proposals

import (
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// LessonsFile is the repository-relative path of the pin file.
const LessonsFile = ".seamark/lessons.yaml"

// Ledger is a read-only snapshot of the decision ledger. Each derived
// source keeps its own error, so one failed reader does not hide the
// proposals.
type Ledger struct {
	// Pending, Applied and Dismissed hold the proposals by state, in
	// ascending ID order.
	Pending, Applied, Dismissed []model.Proposal
	// Superseded holds the proposals whose pins a prune removed, in
	// ascending ID order. They are read-only history: no decision can
	// change them, so Health does not cover them.
	Superseded []model.Proposal
	// Health holds evidence-health advisories by proposal ID.
	Health map[int64]report.ProposalHealth
	// HealthErr holds the advisory failure; Health is then empty.
	HealthErr error
	// Evidence holds the full cited findings by finding ID. A cited ID
	// that is missing aged out of the index.
	Evidence map[int64]model.Finding
	// EvidenceErr holds the evidence read failure.
	EvidenceErr error
	// Write is the current distill.write gate.
	Write bool
	// WriteErr holds the config read failure; Write is then false.
	WriteErr error
	// ObservedAt is the time of the snapshot.
	ObservedAt time.Time
}

// Load reads the ledger and its derived sources. It writes nothing.
// It fails only when the proposal rows cannot be read.
func Load(st *store.Store, root string, now time.Time) (*Ledger, error) {
	ls := &Ledger{ObservedAt: now}

	// The store lists newest first. The console orders by ID, because
	// the spec asks for a deterministic order that a refresh keeps.
	for _, state := range []struct {
		status string
		into   *[]model.Proposal
	}{
		{model.ProposalProposed, &ls.Pending},
		{model.ProposalApplied, &ls.Applied},
		{model.ProposalDismissed, &ls.Dismissed},
		{model.ProposalSuperseded, &ls.Superseded},
	} {
		ps, err := st.Proposals(state.status)
		if err != nil {
			return nil, err
		}

		sortByID(ps)
		*state.into = ps
	}

	// The advisories cover the same rows as `lessons --proposals`:
	// dismissed and superseded rows carry no decision that an advisory
	// can change.
	ls.Health, ls.HealthErr = Health(st, root, append(append([]model.Proposal{}, ls.Pending...), ls.Applied...))
	if ls.HealthErr != nil {
		ls.Health = nil
	}

	var cited []int64

	// Superseded rows keep their evidence, so the history view can show
	// why the pin existed.
	for _, group := range [][]model.Proposal{ls.Pending, ls.Applied, ls.Dismissed, ls.Superseded} {
		for _, p := range group {
			cited = append(cited, p.Members...)
		}
	}

	ls.Evidence, ls.EvidenceErr = st.FindingsByIDs(cited)

	cfg, err := distill.LoadConfig(root)
	if err != nil {
		ls.WriteErr = err
	} else {
		ls.Write = cfg.Distill.Write
	}

	return ls, nil
}

// MatchesRegion reports whether p applies to the repository-relative
// path scope filter. It matches on path-component boundaries in both
// directions. A repo-wide proposal matches every filter, and an empty
// filter matches every proposal.
func MatchesRegion(p model.Proposal, filter string) bool {
	return MatchesRegionSet(p.RegionSet(), filter)
}

// MatchesRegionSet reports whether a region set applies to the
// repository-relative path scope filter. It is MatchesRegion for a set
// that is not a proposal, such as the regions of a pin. It matches on
// path-component boundaries in both directions. An empty filter matches
// every set.
//
// set must be an effective set: model.Proposal.RegionSet or
// reviews.PinRule.AllRegions. A nil or empty set is repo-wide and
// matches every filter. Only those readers apply the delivery rules for
// a "*" or a blank entry, so raw pin entries can give a wrong answer.
func MatchesRegionSet(set []string, filter string) bool {
	filter = strings.Trim(strings.TrimSpace(filter), "/")
	if filter == "" || len(set) == 0 {
		return true
	}

	// Both directions: a filter inside a region selects the set that
	// covers it, and a region inside the filter is part of it.
	for _, r := range set {
		r = strings.Trim(strings.TrimSpace(r), "/")

		if reviews.RegionMatches(filter, r) || reviews.RegionMatches(r, filter) {
			return true
		}
	}

	return false
}

// MatchesText reports whether the text query q occurs in rule or in
// note. The match ignores case, and the query ignores surrounding
// spaces, because a typed search often carries a stray space. A blank
// query matches everything. The caller passes the text it shows, for
// example the live rule and note of a pin.
func MatchesText(rule, note, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}

	return strings.Contains(strings.ToLower(rule), q) || strings.Contains(strings.ToLower(note), q)
}

// sortByID orders proposals by ascending ID in place.
func sortByID(ps []model.Proposal) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
}
