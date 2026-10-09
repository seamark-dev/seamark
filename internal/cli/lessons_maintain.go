package cli

// The maintenance commands `lessons --retarget` and `lessons --prune`.
// They plan and commit through internal/maintain with the pin file
// editor of internal/pinfile, the engine of the console's Pins view. So
// both surfaces change one entry in place, keep the hand-edited lines,
// and refuse the same entries. The CLI keeps its selection by proposal
// ID, its root resolution, and its output lines. This file maps the
// selected proposals to pin entries, and it maps the plan and the commit
// outcome to those lines.

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/render"
)

// The fixed reasons of a row that a run leaves as it is. The retarget
// texts are the earlier CLI lines, so a script that reads them still
// works.
const (
	leftNotFound   = "not found in lessons.yaml; left untouched"
	leftCurrent    = "regions already current"
	leftNoEvidence = "evidence aged out; nothing to recompute from (prune instead?)"
)

// runLessonsRetarget updates applied pins to the regions that today's
// inference computes from their living evidence. It is the upgrade path
// for pins distilled before region sets, or whose evidence moved. The
// region keys of the entry and the ledger row change together. A key in
// only one of them breaks liveness and coverage.
func runLessonsRetarget(cmd *cobra.Command, opts *options, raw string) error {
	return runLessonsMaintain(cmd, opts, raw, maintain.ActionRetarget)
}

// runLessonsPrune retires applied pins that restate another. It is not
// a dismissal: the entry that survives keeps the guidance. So the
// proposal becomes superseded, and the distiller still knows the theme.
// Editing lessons.yaml obeys the same gate as --apply.
func runLessonsPrune(cmd *cobra.Command, opts *options, raw string) error {
	return runLessonsMaintain(cmd, opts, raw, maintain.ActionPrune)
}

// runLessonsMaintain resolves the selection against the applied
// proposals and runs one maintenance action on their pins.
func runLessonsMaintain(cmd *cobra.Command, opts *options, raw string, action maintain.Action) error {
	st, probe, err := openIndexProbe(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return err
	}

	ps, err := resolveSelection(applied, raw, "applied")
	if err != nil {
		return err
	}

	// The root is the root that the index records, as for every other
	// lessons command. So a copied checkout still works on the original
	// files (A0 D15, open). The pin file writes go through the guarded
	// atomic write of the setup engine.
	deps := maintain.Deps{Root: probe.Root, Reg: integration.Builtin(), Reader: st, Probe: probe}

	return maintainPins(cmd.OutOrStdout(), deps, action, ps)
}

// maintainPins plans the action for the selected rows, prints the rows
// that it leaves as they are, and commits the rest. Only the rows whose
// entry is ready enter the commit. So a run changes what it can, and it
// names each row that it left with the reason. With distill.write off,
// it prints the change to make by hand instead.
func maintainPins(out io.Writer, deps maintain.Deps, action maintain.Action, ps []model.Proposal) error {
	sel, err := selectPins(deps, action, ps)
	if err != nil {
		return err
	}

	plan, err := sel.plan(deps)
	if err != nil {
		return err
	}

	for _, p := range ps {
		if reason, ok := sel.left[p.ID]; ok {
			fmt.Fprintf(out, "  p%-4d %s — %s\n", p.ID, render.Sanitize(p.Rule), reason)
		}
	}

	if plan == nil {
		if len(sel.hand) > 0 {
			printManual(out, sel.handChange())

			return nil
		}

		fmt.Fprintln(out, sel.nothingLine(action))

		return nil
	}

	// The gate decides only a change. A config that cannot be read is
	// an error then, never a default.
	if plan.GateErr != nil {
		return plan.GateErr
	}

	ready := sel.rowsOf(plan.Items)

	if !plan.Write {
		// Every entry is ready, so the editor accepts the file. A file
		// block is then a write refusal, for example a symbolic link.
		printManual(out, handChange{
			action: action, rows: ready, regions: regionsOf(plan.Items), diff: plan.Change.Diff,
			gate: plan.FileBlock,
		})

		return nil
	}

	if plan.FileBlock != "" {
		return fmt.Errorf("the pin file cannot be replaced: %s", render.Sanitize(plan.FileBlock))
	}

	res, err := maintain.Commit(deps, plan)
	if err != nil {
		return fmt.Errorf("%s aborted, nothing changed: %w", action, err)
	}

	if err := maintainFailure(res, proposalIDs(ready)); err != nil {
		return err
	}

	printDone(out, plan, ready, res)

	return nil
}

// pinSelection binds the selected rows to the entries of the pin file.
type pinSelection struct {
	action maintain.Action
	doc    *pinfile.Document
	// rows lists the rows of each requested entry, by entry index. Two
	// rows share an entry only when the entry is ambiguous.
	rows map[int][]model.Proposal
	// refs are the requested entries, in the ID order of their first row.
	refs []pinfile.EntryRef
	// left holds the reason of each row that the run leaves, by ID.
	left map[int64]string
	// blocked is true when a row is left for a reason other than current
	// regions or aged-out evidence.
	blocked bool
	// hand holds the items that only the file-level gate of the pin file
	// editor blocks while distill.write is off. That gate blocks every
	// edit of the file, so no item is ready then. gate is its reason.
	hand []maintain.PlanItem
	gate string
}

// selectPins reads the pin file and finds the entry of each selected
// row by the link rules of the console (pinfile.Classify). A row without
// an entry is left with its link reason. A file that gives no reliable
// listing is an error, because no pin is then known to be absent.
func selectPins(deps maintain.Deps, action maintain.Action, ps []model.Proposal) (*pinSelection, error) {
	doc, err := pinfile.Read(deps.Root)
	if err != nil {
		return nil, err
	}

	if doc.Unreadable() {
		return nil, unreadablePins(action, doc)
	}

	applied, err := deps.Reader.Proposals(model.ProposalApplied)
	if err != nil {
		return nil, err
	}

	// Classify reads the ledger rows in ID order, as the plan does, so
	// the first linked row of an entry is the same row. The link of an
	// applied row does not depend on the pending or superseded rows.
	slices.SortFunc(applied, func(a, b model.Proposal) int { return cmp.Compare(a.ID, b.ID) })
	links := pinfile.Classify(doc, applied, nil, nil)

	sel := &pinSelection{action: action, doc: doc, rows: map[int][]model.Proposal{}, left: map[int64]string{}}

	for _, p := range ps {
		i := slices.IndexFunc(links.Entries, func(el pinfile.EntryLink) bool {
			return slices.ContainsFunc(el.Applied, func(a model.Proposal) bool { return a.ID == p.ID })
		})

		if i < 0 {
			sel.leave(p.ID, leftReason(links.ByProposal[p.ID]))

			continue
		}

		index := doc.Entries[i].Index
		if _, ok := sel.rows[index]; !ok {
			sel.refs = append(sel.refs, doc.Entries[i].Ref())
		}

		sel.rows[index] = append(sel.rows[index], p)
	}

	return sel, nil
}

// unreadablePins explains a pin file that gives no reliable listing. A
// syntax or reader error keeps the earlier CLI text.
func unreadablePins(action maintain.Action, doc *pinfile.Document) error {
	verb := "pruning"
	if action == maintain.ActionRetarget {
		verb = "retargeting"
	}

	switch {
	case doc.DecodeErr != nil:
		return fmt.Errorf("lessons.yaml does not parse; fix it before %s: %w", verb, doc.DecodeErr)
	case doc.ReaderErr != nil:
		return fmt.Errorf("lessons.yaml does not parse; fix it before %s: %w", verb, doc.ReaderErr)
	}

	return fmt.Errorf("lessons.yaml cannot be listed reliably; fix it before %s: %w", verb, doc.ListErr)
}

// leftReason returns the line of a selected row without an entry.
func leftReason(link pinfile.Link) string {
	if link == pinfile.LinkNotPresent {
		return leftNotFound
	}

	return link.Reason() + "; left untouched"
}

// leave records the reason of a row that the run leaves as it is.
func (s *pinSelection) leave(id int64, reason string) {
	s.left[id] = reason
	s.blocked = s.blocked || (reason != leftCurrent && reason != leftNoEvidence)
}

// plan plans the requested entries until every entry of the plan is
// ready. Each round leaves the entries that cannot change and plans the
// rest again, because a commit needs a plan with only ready entries. It
// returns nil when no entry is ready. An entry that only the file-level
// gate blocks while distill.write is off goes to hand, not to left.
func (s *pinSelection) plan(deps maintain.Deps) (*maintain.Plan, error) {
	refs := s.refs

	for len(refs) > 0 {
		plan, err := maintain.NewPlan(deps, maintain.Request{Action: s.action, Refs: refs})
		if err != nil {
			return nil, err
		}

		// The rows were bound to the file that selectPins read. Another
		// writer can change it before the plan reads it again.
		if plan.File.Exists != s.doc.Guard.Exists || plan.File.SHA256 != s.doc.Guard.SHA256 {
			return nil, fmt.Errorf("%s aborted, nothing changed: %w", s.action, pinfile.ErrStale)
		}

		var ready []pinfile.EntryRef

		for _, it := range plan.Items {
			switch {
			case it.Status == maintain.ItemReady:
				ready = append(ready, it.Ref)
			case byHand(plan, it):
				s.hand, s.gate = append(s.hand, it), plan.FileBlock
			default:
				for _, p := range s.rows[it.Ref.Index] {
					s.leave(p.ID, itemReason(it))
				}
			}
		}

		if len(ready) == len(refs) {
			return plan, nil
		}

		refs = ready
	}

	return nil, nil
}

// itemReason returns the line of an entry that cannot change. The scope
// outcomes keep the earlier CLI texts. Other reasons come from the plan
// and can quote file or ledger text, so they are sanitized.
func itemReason(it maintain.PlanItem) string {
	scope := it.Pin.Readings.Scope

	switch {
	case it.Status == maintain.ItemNoOp:
		return leftCurrent
	case scope.Known && scope.Advice.Kind == distill.ScopeNoEvidence:
		return leftNoEvidence
	}

	return render.Sanitize(it.Reason)
}

// byHand reports whether only the file-level gate of the pin file editor
// blocks the item while distill.write is off. The CLI then writes
// nothing, so a hand edit of the entry is still possible. pinfile refuses
// every edit of such a file with ErrUnsupported and the gate text.
// maintain keeps that text as the reason of the item.
func byHand(plan *maintain.Plan, it maintain.PlanItem) bool {
	return !plan.Write && plan.GateErr == nil && plan.FileBlock != "" && it.Status == maintain.ItemBlocked &&
		it.Reason == fmt.Sprintf("%s: %s", pinfile.ErrUnsupported, plan.FileBlock)
}

// rowsOf returns the selected rows of the entries of items, in ID order.
func (s *pinSelection) rowsOf(items []maintain.PlanItem) []model.Proposal {
	var rows []model.Proposal

	for _, it := range items {
		rows = append(rows, s.rows[it.Ref.Index]...)
	}

	slices.SortFunc(rows, func(a, b model.Proposal) int { return cmp.Compare(a.ID, b.ID) })

	return rows
}

// regionsOf returns the computed region set of each item, by the ID of
// its linked row. A linked entry has exactly one applied row. The set of
// a prune item is nil.
func regionsOf(items []maintain.PlanItem) map[int64][]string {
	to := map[int64][]string{}

	for _, it := range items {
		for _, p := range it.Rows {
			to[p.ID] = it.To
		}
	}

	return to
}

// handChange returns the gate-off answer for the items that only the
// file-level gate blocks. The editor computes no candidate for such a
// file, so the answer has no diff.
func (s *pinSelection) handChange() handChange {
	return handChange{
		action: s.action, rows: s.rowsOf(s.hand), regions: regionsOf(s.hand), gate: s.gate,
	}
}

// nothingLine is the last line of a run without a ready entry. A
// retarget that only met current or aged-out rows has nothing to do;
// any other left row is a change that did not happen.
func (s *pinSelection) nothingLine(action maintain.Action) string {
	switch {
	case action == maintain.ActionPrune:
		return "no pins pruned"
	case s.blocked:
		return "no pins retargeted"
	}

	return "nothing to retarget"
}

// handChange is the gate-off answer: the rows to change by hand and, for
// a retarget, their new regions by proposal ID. diff is the display diff
// of the plan, or empty. gate is the reason that the command refuses the
// file with distill.write on, or empty.
type handChange struct {
	action  maintain.Action
	rows    []model.Proposal
	regions map[int64][]string
	diff    string
	gate    string
}

// printManual prints the gate-off answer: what to change by hand. A
// retarget shows the exact region lines in the diff of the plan. So a
// hand edit keeps the live note, the rule spelling, and the comments.
// The diff hides text that looks like a secret. A file that the editor
// refuses as a whole has no diff.
func printManual(out io.Writer, h handChange) {
	if h.action == maintain.ActionPrune {
		fmt.Fprintf(out, "distill.write is off in .seamark/config.yaml — remove these pins from "+
			".seamark/lessons.yaml by hand:\n\n")
		printRows(out, h.action, h.rows, h.regions)
		fmt.Fprintf(out, "\n(the proposals stay applied; %s)\n",
			h.tail("enable distill.write to let prune edit the file"))

		return
	}

	fmt.Fprintf(out, "distill.write is off in .seamark/config.yaml — change the regions of these pins in "+
		".seamark/lessons.yaml by hand:\n\n")
	printRows(out, h.action, h.rows, h.regions)

	if h.diff != "" {
		fmt.Fprintf(out, "\n%s\n", strings.TrimRight(h.diff, "\n"))
	}

	fmt.Fprintf(out, "\n(nothing was changed; %s)\n",
		h.tail("enable distill.write to let retarget edit the file and the ledger together"))
}

// tail returns the end of the last line of the answer. When the command
// refuses the file also with distill.write on, it names that refusal,
// because then the hint to turn the gate on is false.
func (h handChange) tail(hint string) string {
	if h.gate == "" {
		return hint
	}

	return fmt.Sprintf("with distill.write on, %s still refuses this file: %s",
		h.action, render.Sanitize(h.gate))
}

// printDone prints the outcome of a completed commit.
func printDone(out io.Writer, plan *maintain.Plan, ready []model.Proposal, res maintain.Result) {
	if plan.Action == maintain.ActionPrune {
		fmt.Fprintf(out, "pruned %d pin(s) from .seamark/lessons.yaml, %d proposal(s) marked superseded — "+
			"review the diff and commit it\n", len(plan.Items), len(res.Moved))
		printRows(out, plan.Action, ready, nil)

		return
	}

	printRows(out, plan.Action, ready, regionsOf(plan.Items))
	fmt.Fprintf(out, "retargeted %d pin(s) — review the lessons.yaml diff and commit it\n", len(plan.Items))
}

// printRows prints one line per row: the rule, and for a retarget the
// new regions from to, by proposal ID. The rule comes from the ledger,
// so it is sanitized.
func printRows(out io.Writer, action maintain.Action, rows []model.Proposal, to map[int64][]string) {
	for _, p := range rows {
		if action == maintain.ActionPrune {
			fmt.Fprintf(out, "  p%-4d %s\n", p.ID, render.Sanitize(p.Rule))

			continue
		}

		regions := "*"
		if set := to[p.ID]; len(set) > 0 {
			regions = strings.Join(set, ", ")
		}

		fmt.Fprintf(out, "  p%-4d %s → %s\n", p.ID, render.Sanitize(p.Rule), render.Sanitize(regions))
	}
}

// maintainFailure maps the outcome of a commit to the CLI error. ids are
// the proposals of the commit. The CLI has no session, so it cannot keep
// the recovery proof of the console (maintain.Recovery). A partial
// result therefore names the state and the way back: undo the file
// change, and run the command again from a consistent state.
func maintainFailure(res maintain.Result, ids []int64) error {
	switch {
	case res.LedgerDone:
		return nil
	case !res.FileWritten:
		return fmt.Errorf("%s aborted, nothing changed: %w", res.Action, res.FileErr)
	case res.Restored:
		return fmt.Errorf("%s aborted, lessons.yaml restored: %w", res.Action, failureCause(res))
	}

	return fmt.Errorf("%s incomplete: %s: %w — undo the change to .seamark/lessons.yaml "+
		"(`git diff .seamark/lessons.yaml` shows it) and run `seamark lessons --%s %s` again",
		res.Action, res.Note, failureCause(res), res.Action, pidArg(ids))
}

// failureCause joins the failures of a commit into one line: the failed
// write first, then a failed restore or a failed read of the rows.
func failureCause(res maintain.Result) error {
	cause := res.LedgerErr
	if cause == nil {
		cause = res.FileErr
	}

	if cause == nil {
		cause = errors.New("unknown failure")
	}

	if res.RestoreErr != nil {
		cause = fmt.Errorf("%w; the restore failed: %w", cause, res.RestoreErr)
	}

	if res.ObservedErr != nil {
		cause = fmt.Errorf("%w; the rows could not be read again: %w", cause, res.ObservedErr)
	}

	return cause
}
