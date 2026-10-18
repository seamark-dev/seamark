package distill

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// A reviewed run has two halves. NewPlan reads the store and freezes the
// exact groups, evidence, and cleanup of one run; it writes nothing.
// Execute later runs exactly that plan. The split exists because a
// preview must describe the work that a confirmation authorizes: a
// second, independent computation could select other groups.

var (
	// ErrInvalidLimit reports a negative group limit. The legacy CLI
	// reads a negative limit as "all"; a reviewed plan must not guess.
	ErrInvalidLimit = errors.New("the group limit must be 0 (all groups) or positive")
	// ErrStale reports that an input of the plan changed after planning.
	// The wrapped text names each changed input. The caller must make a
	// new plan instead of running the old one.
	ErrStale = errors.New("distillation inputs changed since the plan")
)

// PlanOptions are the frozen inputs of one plan.
type PlanOptions struct {
	// Region restricts the corpus before grouping. It must already be
	// repository-relative, as NormalizeRegion returns it; "" means the
	// whole repository.
	Region string
	// Limit caps how many new groups the plan selects; 0 selects all.
	// NewPlan rejects a negative limit.
	Limit int
	// Pins are the patterns of .seamark/lessons.yaml as proposals, as
	// PinsFromConfig returns them.
	Pins []model.Proposal
	// Spec is the agent invocation the plan discloses. Execute runs the
	// invoker its caller passes, so the caller must build that invoker
	// from this spec.
	Spec agent.CommandSpec
	// Root is the workspace root. Trigger paths are verified against
	// this tree; "" drops them all.
	Root string
}

// PlannedGroup is one selected group with its members frozen. Execute
// sends exactly these findings, never a later version of the group.
type PlannedGroup struct {
	Group Group
	// Digest identifies the member findings as the prompt would show
	// them. A boundary check compares it with the stored rows.
	Digest [32]byte
}

// Snapshot identifies the store inputs a plan was made from. Revalidate
// recomputes each digest and names every one that differs.
type Snapshot struct {
	// Corpus covers the findings in scope, with every field that reaches
	// grouping or the prompt.
	Corpus [32]byte
	// Ledger covers every stored proposal: the cleanup set, the known
	// patterns for deduplication, and the coverage order all read it.
	Ledger [32]byte
	// Distilled covers the recorded group signatures, the skip set.
	Distilled [32]byte
	// Pins covers PlanOptions.Pins. The store holds no pins, so
	// Revalidate does not check it; the caller guards the pin file.
	Pins [32]byte
}

// Plan is one frozen distillation run: what it sends, in which order,
// what it skips, and which pending proposals it removes first.
type Plan struct {
	Options PlanOptions
	// Preflight is the disclosure of the selected groups. It is the
	// same value that the legacy OnPreflight callback receives.
	Preflight Preflight
	// Groups are the selected groups in execution order, parallel to
	// Preflight.Groups.
	Groups []PlannedGroup
	// Cleanup lists the pending proposals whose evidence group no longer
	// exists. Execute deletes exactly these rows before the first group.
	Cleanup []model.Proposal
	// Skipped lists the signatures already distilled, in coverage order.
	Skipped []string
	// Deferred lists the groups beyond the limit, in coverage order. An
	// entry carries identity and size only: Signature, Region, Findings,
	// and BodyCap. Its prompt is not built, so PromptChars and Evidence
	// stay empty.
	Deferred []GroupPlan
	// Total counts every group in scope, skipped and deferred included.
	Total int
	// Corpus counts the findings in scope before grouping. Findings
	// without a group are not sent, and this count makes that visible.
	Corpus    int
	Snapshot  Snapshot
	PlannedAt time.Time

	// known holds the patterns that deduplication and the primer read.
	// Execute works on a clone, so a plan never learns from a run.
	known *Known
}

// NewPlan groups, orders, and selects evidence exactly as a real run
// does after its stale-proposal cleanup. It models that cleanup instead
// of doing it: the pending rows to delete are listed in Plan.Cleanup and
// left out of the known patterns. NewPlan only reads the store, so a
// read-only store works. It never invokes an agent, prunes, or marks.
func NewPlan(st *store.Store, grouper Grouper, opts PlanOptions) (*Plan, error) {
	if opts.Limit < 0 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidLimit, opts.Limit)
	}

	p, _, err := buildPlan(st, grouper, opts, cleanupModel)

	return p, err
}

// cleanupMode selects how planning treats stale pending proposals.
type cleanupMode int

const (
	// cleanupModel lists the stale rows and plans as if they were gone.
	// It writes nothing. NewPlan and the dry run of Run use it.
	cleanupModel cleanupMode = iota
	// cleanupPrune deletes the stale rows before the ledger is read, at
	// the same point as the legacy real run. Both modes then select the
	// same groups.
	cleanupPrune
)

// buildPlan is the shared planning path. It also returns the number of
// rows that cleanupPrune deleted.
func buildPlan(st *store.Store, grouper Grouper, opts PlanOptions, mode cleanupMode) (*Plan, int, error) {
	findings, err := scopedFindings(st, opts.Region)
	if err != nil {
		return nil, 0, err
	}

	groups := grouper.Group(findings)

	live, err := liveSignatures(st, opts.Region, findings, groups)
	if err != nil {
		return nil, 0, err
	}

	var (
		cleanup []model.Proposal
		pruned  int
	)

	switch mode {
	case cleanupPrune:
		if pruned, err = st.PruneStaleProposals(live); err != nil {
			return nil, 0, err
		}
	case cleanupModel:
		if cleanup, err = st.PendingProposalsOutside(live); err != nil {
			return nil, 0, err
		}
	}

	done, err := st.DistilledSignatures()
	if err != nil {
		return nil, 0, err
	}

	ledger, err := readLedger(st)
	if err != nil {
		return nil, 0, err
	}

	known, cited := knownFrom(opts.Pins, ledger, cleanup)

	// Read the least-covered evidence first. Nothing is filtered — a
	// budgeted run simply spends its calls where no pattern has been
	// drawn yet, instead of in whatever order the grouper emitted.
	orderByCoverage(groups, cited)

	p := &Plan{
		Options: opts,
		Cleanup: cleanup,
		Total:   len(groups),
		Corpus:  len(findings),
		known:   known,
	}

	// Selection first, execution after: the split is what makes an
	// honest preflight possible — the disclosure names exactly the
	// groups that Execute would send.
	for _, g := range groups {
		switch {
		case done[g.Signature]:
			p.Skipped = append(p.Skipped, g.Signature)
		case opts.Limit > 0 && len(p.Groups) >= opts.Limit:
			p.Deferred = append(p.Deferred, GroupPlan{
				Signature: g.Signature, Region: g.Region,
				Findings: len(g.Findings), BodyCap: promptBodyCapFor(len(g.Findings)),
			})
		default:
			p.Groups = append(p.Groups, PlannedGroup{Group: g, Digest: findingsDigest(g.Findings)})
		}
	}

	p.Preflight = preflightFor(opts.Spec.Argv, p.Groups, known)
	p.Snapshot = Snapshot{
		Corpus:    findingsDigest(findings),
		Ledger:    ledgerDigest(ledger),
		Distilled: signaturesDigest(done),
		Pins:      pinsDigest(opts.Pins),
	}
	p.PlannedAt = time.Now()

	return p, pruned, nil
}

// Revalidate reads the store again and compares every snapshot digest
// except Pins, which the store does not hold. It returns an error that
// wraps ErrStale and names each changed input. A read failure returns
// that error unwrapped, because it says nothing about staleness.
func (p *Plan) Revalidate(st *store.Store) error {
	if p == nil {
		return errors.New("distill: no plan to revalidate")
	}

	findings, err := scopedFindings(st, p.Options.Region)
	if err != nil {
		return err
	}

	ledger, err := readLedger(st)
	if err != nil {
		return err
	}

	done, err := st.DistilledSignatures()
	if err != nil {
		return err
	}

	var reasons []string

	if findingsDigest(findings) != p.Snapshot.Corpus {
		reasons = append(reasons, "the findings in scope changed")
	}

	if ledgerDigest(ledger) != p.Snapshot.Ledger {
		reasons = append(reasons, "the proposal ledger changed")
	}

	if signaturesDigest(done) != p.Snapshot.Distilled {
		reasons = append(reasons, "the distilled signatures changed")
	}

	if len(reasons) > 0 {
		return fmt.Errorf("%w: %s", ErrStale, strings.Join(reasons, "; "))
	}

	return nil
}

// checkGroup is the cheap store check at one group boundary. It fails
// with ErrStale when another writer recorded the group or when a member
// finding changed. skip is true in the first case: the group's evidence
// was read by someone else, so the group needs no further work.
func checkGroup(st *store.Store, pg PlannedGroup) (skip bool, err error) {
	done, err := st.DistilledSignatures()
	if err != nil {
		return false, err
	}

	sig := pg.Group.Signature
	if done[sig] {
		return true, fmt.Errorf("%w: group %s was distilled by another writer", ErrStale, sig)
	}

	ids := make([]int64, len(pg.Group.Findings))
	for i, f := range pg.Group.Findings {
		ids[i] = f.ID
	}

	rows, err := st.FindingsByIDs(ids)
	if err != nil {
		return false, err
	}

	current := make([]model.Finding, 0, len(ids))

	for _, id := range ids {
		f, ok := rows[id]
		if !ok {
			return false, fmt.Errorf("%w: finding %d of group %s is gone", ErrStale, id, sig)
		}

		current = append(current, f)
	}

	if findingsDigest(current) != pg.Digest {
		return false, fmt.Errorf("%w: the findings of group %s changed", ErrStale, sig)
	}

	return false, nil
}

// scopedFindings reads the corpus and keeps the findings in the region.
// The filter runs before grouping, so unrelated findings cannot change
// token frequencies, bridge components, or take a budgeted call.
func scopedFindings(st *store.Store, region string) ([]model.Finding, error) {
	findings, err := st.AllFindings()
	if err != nil {
		return nil, err
	}

	if region == "" {
		return findings, nil
	}

	regional := findings[:0]

	for _, finding := range findings {
		if withinRegion(region, finding.Path) {
			regional = append(regional, finding)
		}
	}

	return regional, nil
}

// liveSignatures returns the signatures whose pending proposals stay.
// Every current group is live, the distilled ones included.
func liveSignatures(st *store.Store, region string, findings []model.Finding, groups []Group,
) (map[string]bool, error) {
	live := make(map[string]bool, len(groups))
	for _, g := range groups {
		live[g.Signature] = true
	}

	if region == "" {
		return live, nil
	}

	// A scoped corpus says nothing about proposals elsewhere. Preserve
	// their signatures so pruning cannot mistake "outside this run" for
	// "its evidence disappeared". Proposals inside the scope still face
	// the current regional group set and are pruned normally when stale.
	corpus := make(map[int64]struct{}, len(findings))
	for _, finding := range findings {
		corpus[finding.ID] = struct{}{}
	}

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return nil, err
	}

	for _, proposal := range pending {
		if !proposalMembersInCorpus(proposal, corpus) {
			live[proposal.Signature] = true
		}
	}

	return live, nil
}

// ledgerStatuses is the order in which the known patterns are built.
// The order decides which labels fill the bounded primer first, so it
// must not change.
var ledgerStatuses = []string{
	model.ProposalProposed, model.ProposalApplied,
	model.ProposalDismissed, model.ProposalSuperseded,
}

// readLedger reads every stored proposal in ledgerStatuses order, each
// status newest first.
func readLedger(st *store.Store) ([]model.Proposal, error) {
	var ledger []model.Proposal

	for _, status := range ledgerStatuses {
		stored, err := st.Proposals(status)
		if err != nil {
			return nil, err
		}

		ledger = append(ledger, stored...)
	}

	return ledger, nil
}

// knownFrom collects everything already captured: the workspace's pins
// plus every stored proposal, whatever its status, except the rows the
// cleanup removes. A dismissed proposal counts — the user said no, and
// re-proposing it under a new name would relitigate a decision.
// It also returns the findings those proposals cited — the exact
// evidence a pattern has already been drawn from, which is what makes
// coverage ordering deterministic rather than a guess.
func knownFrom(pins, ledger, cleanup []model.Proposal) (known *Known, cited map[int64]bool) {
	removed := make(map[int64]bool, len(cleanup))
	for _, p := range cleanup {
		removed[p.ID] = true
	}

	known = NewKnown(pins)
	cited = map[int64]bool{}

	for _, p := range ledger {
		if removed[p.ID] {
			continue
		}

		known.Add(p)

		for _, id := range p.Members {
			cited[id] = true
		}
	}

	return known, cited
}

// preflightFor builds the disclosure of the selected groups. Each prompt
// size is exact for the known patterns at plan time; later groups of a
// run can grow by the labels that earlier groups add.
func preflightFor(argv []string, groups []PlannedGroup, known *Known) Preflight {
	pf := Preflight{Agent: argv}

	for _, pg := range groups {
		g := pg.Group
		bodyCap := promptBodyCapFor(len(g.Findings))
		chars := len(buildPrompt(g, known.Labels(g.Region, maxKnownLabels)))
		plan := GroupPlan{
			Signature: g.Signature,
			Region:    g.Region, Findings: len(g.Findings), PromptChars: chars,
			BodyCap: bodyCap,
		}

		for _, finding := range g.Findings {
			paths, _ := promptFindingEvidence(finding, bodyCap)
			plan.Evidence = append(plan.Evidence, FindingPlan{
				ID: finding.ID, Source: sourceLabel(finding.Source),
				PR: finding.PR, Path: finding.Path, Paths: paths,
			})
		}

		pf.Groups = append(pf.Groups, plan)
		pf.PromptChars += chars
		pf.Findings += len(g.Findings)
		pf.BodyCap = max(pf.BodyCap, bodyCap)
	}

	return pf
}

// findingsDigest hashes the findings in their given order. It covers the
// fields that reach grouping or the prompt, so an edited body under the
// same ID changes the digest although the group signature stays.
func findingsDigest(findings []model.Finding) [32]byte {
	h := sha256.New()

	for _, f := range findings {
		digestRecord(h, struct {
			ID       int64
			Path     string
			Paths    []string
			PR       int
			Source   string
			Reviewer string
			Body     string
		}{f.ID, f.Path, f.Paths, f.PR, f.Source, f.Reviewer, f.Body})
	}

	return digestSum(h)
}

// ledgerDigest hashes the proposal rows in their given order, with every
// field that the cleanup, deduplication, or coverage order reads.
func ledgerDigest(ledger []model.Proposal) [32]byte {
	h := sha256.New()

	for _, p := range ledger {
		digestRecord(h, struct {
			ID        int64
			Status    string
			Signature string
			Rule      string
			Note      string
			Region    string
			Regions   []string
			Members   []int64
		}{p.ID, p.Status, p.Signature, p.Rule, p.Note, p.Region, p.Regions, p.Members})
	}

	return digestSum(h)
}

// signaturesDigest hashes a signature set in sorted order.
func signaturesDigest(done map[string]bool) [32]byte {
	sigs := make([]string, 0, len(done))
	for sig := range done {
		sigs = append(sigs, sig)
	}

	sort.Strings(sigs)

	h := sha256.New()
	digestRecord(h, sigs)

	return digestSum(h)
}

// pinsDigest hashes pins in their given order, with the fields that
// deduplication and the primer read.
func pinsDigest(pins []model.Proposal) [32]byte {
	h := sha256.New()

	for _, p := range pins {
		digestRecord(h, struct {
			Rule    string
			Note    string
			Region  string
			Regions []string
		}{p.Rule, p.Note, p.Region, p.Regions})
	}

	return digestSum(h)
}

// digestRecord writes one record as a JSON line. JSON quotes every
// string, so two different records never produce the same bytes.
func digestRecord(h hash.Hash, record any) {
	// Encoding plain structs of strings, numbers, and slices cannot
	// fail, and a hash write never fails.
	data, _ := json.Marshal(record)
	_, _ = h.Write(append(data, '\n'))
}

// digestSum returns the SHA-256 sum as an array.
func digestSum(h hash.Hash) [32]byte {
	var sum [32]byte

	copy(sum[:], h.Sum(nil))

	return sum
}
