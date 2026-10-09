package distill

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// A reviewed extraction has two halves, like a reviewed distillation.
// NewExtractPlan reads the store and freezes the selected proposals, the
// batches and their prompts; it writes nothing and calls no agent.
// ExecuteExtract later sends exactly those prompts. The console uses this
// path. The CLI calls ExtractTriggers, which freezes the rows that the
// CLI selected and runs ExecuteExtract in one call. So both paths share
// the batching, the prompt builder, the reply parser, the path
// validation and the executor. The CLI keeps its own, wider selection
// until a separate decision moves it to the console selection.

// ErrExtractRoot reports a plan without a workspace root. The existence
// rung needs the root: without it no named path is verified, and every
// answer would be stored as a permanent negative.
var ErrExtractRoot = errors.New("trigger extraction needs the workspace root: " +
	"without it every answer would be stored as a negative")

// ErrExtractStale reports that an input of an extraction plan changed
// after planning. The wrapped text names each changed input. It also
// matches ErrStale, so one check serves both kinds of plan.
var ErrExtractStale error = extractStaleError{}

// extractStaleError is the type of ErrExtractStale. Its own text names
// extraction, and its Is method makes it match ErrStale too.
type extractStaleError struct{}

// Error returns the sentinel text.
func (extractStaleError) Error() string { return "trigger extraction inputs changed since the plan" }

// Is reports whether target is ErrStale.
func (extractStaleError) Is(target error) bool { return target == ErrStale }

// ExtractPlanOptions are the frozen inputs of one extraction plan.
type ExtractPlanOptions struct {
	// IDs are the explicitly selected proposals. The plan never adds a
	// proposal, so an empty list plans no call.
	IDs []int64
	// Root is the workspace root. Named paths are verified against this
	// tree. NewExtractPlan refuses "" (see ErrExtractRoot).
	Root string
	// Spec is the agent invocation the plan discloses. ExecuteExtract
	// runs the invoker its caller passes, so the caller must build that
	// invoker from this spec.
	Spec agent.CommandSpec
	// Exclude adds the caller's own exclusions, such as an absent or
	// ambiguous pin. It runs only for pending and applied rows whose
	// question is open. It returns the reason and true to exclude the
	// row. Nil excludes nothing more.
	Exclude func(model.Proposal) (reason string, excluded bool)
}

// ExtractItem is what one planned item sends and what a negative answer
// changes. It is parallel to ExtractBatch.Proposals.
type ExtractItem struct {
	// ID is the proposal ID, equal to the parallel proposal's ID.
	ID int64
	// Reason says why the question is open.
	Reason TriggerReason
	// Living are the member findings that are still in the index, with
	// metadata only. Region recomputation reads these frozen values.
	Living []model.Finding
	// Evidence are the evidence paths that the prompt names for the item,
	// after the shared batch budget.
	Evidence []string
	// Coverage is the evidence coverage of a pending row. A negative or
	// unconfirmed answer gives the row this region set. It is nil for an
	// applied row, whose regions never change here.
	Coverage []string
	// CoverageChanges reports that Coverage differs from the stored
	// regions of a pending row, so a negative answer changes them.
	CoverageChanges bool

	// row and evidence bind the planned row and its living evidence for
	// the boundary checks.
	row, evidence [32]byte
}

// ExtractBatch is one planned agent call.
type ExtractBatch struct {
	// Proposals are the frozen rows in send order. The question sends
	// their recorded rule and note, and their guards bind the answers.
	Proposals []model.Proposal
	// Items are parallel to Proposals.
	Items []ExtractItem
	// Prompt is the exact text that ExecuteExtract sends.
	Prompt      string
	PromptChars int
}

// ExtractSkip is one selected proposal that the plan does not ask.
type ExtractSkip struct {
	ID     int64
	Reason string
}

// ExtractSnapshot identifies the store inputs of a plan: every planned
// row with its trigger stamp, and every living member finding as the
// prompts show it.
type ExtractSnapshot struct {
	Rows     [32]byte
	Findings [32]byte
}

// ExtractPlan is one frozen extraction run.
type ExtractPlan struct {
	Options ExtractPlanOptions
	// Batches are the agent calls in execution order. NewExtractPlan sends
	// pending rows newest first, then applied rows newest first, as the
	// CLI orders them. ExtractTriggers keeps the order of its caller.
	Batches []ExtractBatch
	// Ineligible lists the selected proposals that are not asked, in
	// ascending ID order.
	Ineligible []ExtractSkip
	Snapshot   ExtractSnapshot
	// Preflight is the disclosure total. Its BodyCap is 0, because the
	// plan reads finding metadata only and sends no finding text. The
	// CLI disclosure of ExtractTriggers keeps its excerpt cap, because
	// its goldens pin that text (A0 defect D4).
	Preflight ExtractPreflight
	PlannedAt time.Time
}

// IDs returns the planned proposal IDs in send order.
func (p *ExtractPlan) IDs() []int64 {
	var ids []int64

	for _, b := range p.Batches {
		for _, row := range b.Proposals {
			ids = append(ids, row.ID)
		}
	}

	return ids
}

// NewExtractPlan selects the open questions among opts.IDs and freezes
// their batches and prompts. It reads finding metadata only, as the CLI
// does, so no finding text is sent. A selected proposal is not asked
// when it is not found, not pending or applied, already answered under
// the current question, excluded by opts.Exclude, or without living
// evidence. NewExtractPlan only reads the store, so a read-only store
// works. It never invokes an agent and never stamps a row.
func NewExtractPlan(st *store.Store, opts ExtractPlanOptions) (*ExtractPlan, error) {
	if opts.Root == "" {
		return nil, ErrExtractRoot
	}

	ids := uniqueIDs(opts.IDs)

	rows, err := st.ProposalsWithIDs(ids)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]model.Proposal, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	// The plan owns a copy of the selection, so a later change to the
	// caller's slice cannot change what the plan describes.
	p := &ExtractPlan{Options: opts}
	p.Options.IDs = append([]int64(nil), opts.IDs...)

	var open []model.Proposal

	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			p.skip(id, "not found in the ledger")

			continue
		}

		if reason, excluded := extractExclusion(row, opts.Exclude); excluded {
			p.skip(id, reason)

			continue
		}

		open = append(open, row)
	}

	meta, err := st.FindingsMetaByIDs(memberIDs(open))
	if err != nil {
		return nil, err
	}

	var eligible []model.Proposal

	for _, row := range open {
		if len(livingFindings(row, meta)) == 0 {
			p.skip(row.ID, "no living evidence: every cited finding aged out of the index")

			continue
		}

		eligible = append(eligible, row)
	}

	sort.Slice(p.Ineligible, func(i, j int) bool { return p.Ineligible[i].ID < p.Ineligible[j].ID })
	sort.SliceStable(eligible, func(i, j int) bool { return cliExtractOrder(eligible[i], eligible[j]) < 0 })

	p.freeze(eligible, meta)

	return p, nil
}

// freeze plans rows as they are, in the given send order: the batches,
// the prompts, the preflight and the snapshot. It selects nothing and
// reads no store. NewExtractPlan calls it after its selection, and
// ExtractTriggers calls it with the rows that the CLI selected.
func (p *ExtractPlan) freeze(rows []model.Proposal, meta map[int64]model.Finding) {
	for _, batch := range extractBatches(rows) {
		p.Batches = append(p.Batches, planBatch(batch, meta))
	}

	p.Preflight = ExtractPreflight{Batches: len(p.Batches), Agent: p.Options.Spec.Argv}

	for _, b := range p.Batches {
		p.Preflight.Proposals += len(b.Proposals)
		p.Preflight.PromptChars += b.PromptChars
	}

	p.Snapshot = snapshotOf(p.Batches)
	p.PlannedAt = time.Now()
}

// Revalidate reads the planned rows and their findings again. It returns
// an error that wraps ErrExtractStale and names each changed input. A read
// failure returns that error unwrapped, because it says nothing about
// staleness.
func (p *ExtractPlan) Revalidate(st *store.Store) error {
	if p == nil {
		return errors.New("distill: no extraction plan to revalidate")
	}

	var (
		rows  []model.Proposal
		items []ExtractItem
	)

	for _, b := range p.Batches {
		rows = append(rows, b.Proposals...)
		items = append(items, b.Items...)
	}

	return checkExtractItems(st, rows, items)
}

// skip records one selected proposal that the plan does not ask.
func (p *ExtractPlan) skip(id int64, reason string) {
	p.Ineligible = append(p.Ineligible, ExtractSkip{ID: id, Reason: reason})
}

// extractExclusion applies the shared rules and then the caller's rules.
// Only pending and applied rows are guidance that a trigger can scope.
func extractExclusion(row model.Proposal, exclude func(model.Proposal) (string, bool)) (string, bool) {
	switch row.Status {
	case model.ProposalProposed, model.ProposalApplied:
	case model.ProposalDismissed, model.ProposalSuperseded:
		return row.Status, true
	default:
		return "status " + row.Status, true
	}

	if open, reason := TriggerQuestionOpen(row); !open {
		return reason.String(), true
	}

	if exclude != nil {
		return exclude(row)
	}

	return "", false
}

// cliExtractOrder is the CLI candidate order: pending rows before applied
// rows, each newest first. The order decides the batch boundaries and
// the evidence budget, so the console sends what the CLI would send.
func cliExtractOrder(a, b model.Proposal) int {
	rank := func(p model.Proposal) int {
		if p.Status == model.ProposalProposed {
			return 0
		}

		return 1
	}

	if c := cmp.Compare(rank(a), rank(b)); c != 0 {
		return c
	}

	return cmp.Compare(b.ID, a.ID)
}

// planBatch freezes one batch: its rows, what each item sends, the exact
// prompt, and the digests for the boundary checks.
func planBatch(batch []model.Proposal, meta map[int64]model.Finding) ExtractBatch {
	evidence := extractItemEvidence(batch, meta)
	prompt := buildExtractPrompt(batch, meta)

	b := ExtractBatch{
		Proposals:   append([]model.Proposal(nil), batch...),
		Items:       make([]ExtractItem, len(batch)),
		Prompt:      prompt,
		PromptChars: len(prompt),
	}

	for i, row := range batch {
		_, reason := TriggerQuestionOpen(row)
		living := livingFindings(row, meta)

		item := ExtractItem{
			ID: row.ID, Reason: reason, Living: living, Evidence: evidence[i],
			row: rowDigest(row), evidence: livingDigest(living),
		}

		if row.Status == model.ProposalProposed {
			item.Coverage = CoverageRegions(living)
			item.CoverageChanges = regionsDiffer(row, item.Coverage)
		}

		b.Items[i] = item
	}

	return b
}

// checkExtractItems compares the rows and the living evidence of items
// with the store. rows is parallel to items. It names every change in one
// error that wraps ErrExtractStale.
func checkExtractItems(st *store.Store, rows []model.Proposal, items []ExtractItem) error {
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}

	current, err := st.ProposalsWithIDs(ids)
	if err != nil {
		return err
	}

	byID := make(map[int64]model.Proposal, len(current))
	for _, row := range current {
		byID[row.ID] = row
	}

	meta, err := st.FindingsMetaByIDs(memberIDs(current))
	if err != nil {
		return err
	}

	var reasons []string

	for i, planned := range rows {
		now, ok := byID[planned.ID]

		switch {
		case !ok:
			reasons = append(reasons, fmt.Sprintf("p%d is gone", planned.ID))
		case rowDigest(now) != items[i].row:
			reasons = append(reasons, describeRowChange(planned, now))
		case livingDigest(livingFindings(now, meta)) != items[i].evidence:
			reasons = append(reasons, fmt.Sprintf("the living evidence of p%d changed", planned.ID))
		}
	}

	if len(reasons) > 0 {
		return fmt.Errorf("%w: %s", ErrExtractStale, strings.Join(reasons, "; "))
	}

	return nil
}

// describeRowChange names the most important difference of one row, so a
// surface can say what moved instead of only "changed".
func describeRowChange(planned, now model.Proposal) string {
	switch {
	case planned.Signature != now.Signature || planned.Rule != now.Rule:
		return fmt.Sprintf("p%d names another proposal now", planned.ID)
	case planned.Status != now.Status:
		return fmt.Sprintf("p%d is %s now", planned.ID, now.Status)
	case planned.TriggerChecked != now.TriggerChecked ||
		planned.TriggerPromptVersion != now.TriggerPromptVersion ||
		!sameStrings(planned.TriggerPaths, now.TriggerPaths):
		return fmt.Sprintf("the trigger answer of p%d changed", planned.ID)
	case planned.Region != now.Region || !sameStrings(planned.Regions, now.Regions):
		return fmt.Sprintf("the regions of p%d changed", planned.ID)
	default:
		return fmt.Sprintf("p%d changed", planned.ID)
	}
}

// regionsDiffer reports whether a recomputed region set gives row another
// delivery identity. The plan and the executor both call it, so the
// preview and the run decide a pending region change the same way.
func regionsDiffer(row model.Proposal, regions []string) bool {
	return reviews.NewPinKey(row.Rule, "", regions) != reviews.ProposalKey(row)
}

// livingFindings returns the members of row that meta still holds, in
// member order. The plan and its boundary checks both call it.
func livingFindings(row model.Proposal, meta map[int64]model.Finding) []model.Finding {
	var living []model.Finding

	for _, id := range row.Members {
		if f, ok := meta[id]; ok {
			living = append(living, f)
		}
	}

	return living
}

// uniqueIDs returns ids sorted ascending without repeats. It copies, so
// the caller's selection keeps its order.
func uniqueIDs(ids []int64) []int64 {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var out []int64

	for _, id := range sorted {
		if len(out) == 0 || id != out[len(out)-1] {
			out = append(out, id)
		}
	}

	return out
}

// sameStrings reports whether a and b hold the same strings in the same
// order. A nil and an empty list are equal, as the store encodes both
// the same way.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// memberIDs returns every member finding ID of rows.
func memberIDs(rows []model.Proposal) []int64 {
	var ids []int64

	for _, row := range rows {
		ids = append(ids, row.Members...)
	}

	return ids
}

// rowDigest hashes every field of a row that the question sends, the
// guard binds, or the eligibility reads.
func rowDigest(p model.Proposal) [32]byte {
	h := sha256.New()

	digestRecord(h, struct {
		ID             int64
		Status         string
		Signature      string
		Rule           string
		Note           string
		Region         string
		Regions        []string
		Members        []int64
		TriggerPaths   []string
		TriggerChecked int64
		TriggerVersion int
	}{
		p.ID, p.Status, p.Signature, p.Rule, p.Note, p.Region, p.Regions, p.Members,
		p.TriggerPaths, p.TriggerChecked, p.TriggerPromptVersion,
	})

	return digestSum(h)
}

// livingDigest hashes the living findings with the metadata fields that
// reach the prompt or region recomputation.
func livingDigest(living []model.Finding) [32]byte {
	h := sha256.New()

	for _, f := range living {
		digestRecord(h, struct {
			ID     int64
			Path   string
			Paths  []string
			PR     int
			Source string
		}{f.ID, f.Path, f.Paths, f.PR, f.Source})
	}

	return digestSum(h)
}

// snapshotOf combines the item digests of all batches in send order.
func snapshotOf(batches []ExtractBatch) ExtractSnapshot {
	rows, findings := sha256.New(), sha256.New()

	for _, b := range batches {
		for _, item := range b.Items {
			_, _ = rows.Write(item.row[:])
			_, _ = findings.Write(item.evidence[:])
		}
	}

	return ExtractSnapshot{Rows: digestSum(rows), Findings: digestSum(findings)}
}
