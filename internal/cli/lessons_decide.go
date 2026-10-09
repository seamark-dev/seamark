package cli

// The decision commands `lessons --apply` and `lessons --dismiss`. They
// run through the decision flow of internal/proposals, the flow that the
// console uses, so both surfaces write the same pins and ledger states.
// The CLI keeps its own selection grammar, its root resolution, and its
// output lines; this file maps the shared previews and results to them.

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/store"
)

// runLessonsApply turns chosen proposals into pins. The pins land in
// .seamark/lessons.yaml only when the workspace opted in via
// distill.write. Otherwise, it prints the rendered block for manual
// insertion. In both modes, applying requires an explicit command that
// names the proposals.
func runLessonsApply(cmd *cobra.Command, opts *options, raw string) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ids, err := pendingSelection(st, raw)
	if err != nil {
		return err
	}

	pv, err := proposals.PreviewApply(st, root, ids)
	if err != nil {
		return err
	}

	// Another writer can decide every selected row between the two
	// reads. The preview then holds nothing to print or to write.
	if len(pv.Proposals) == 0 {
		return proposals.ErrNothingSelected
	}

	out := cmd.OutOrStdout()

	if !pv.Write {
		return printPinBlock(out, pv)
	}

	return applyPins(out, st, root, pv)
}

// runLessonsDismiss records a no on chosen proposals. The signature
// memory does the rest: the same evidence set is never re-proposed.
func runLessonsDismiss(cmd *cobra.Command, opts *options, raw string) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ids, err := pendingSelection(st, raw)
	if err != nil {
		return err
	}

	pv, err := proposals.PreviewDismiss(st, ids)
	if err != nil {
		return err
	}

	res, err := proposals.Commit(st, root, pv)
	if err != nil {
		return err
	}

	if err := dismissFailure(res); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "dismissed %d proposal(s) — remembered; the same evidence will not return\n",
		len(res.Moved))

	return nil
}

// dismissFailure maps the ledger outcome of a dismiss commit to the CLI
// error. Commit moves one row at a time, so a failure can leave some
// rows dismissed. The error then names both groups, because a rerun
// with the same IDs stops at the rows that already moved.
func dismissFailure(res proposals.Result) error {
	switch {
	case res.LedgerErr == nil:
		return nil
	case len(res.Moved) == 0:
		return res.LedgerErr
	default:
		return fmt.Errorf("dismissed %s, but not %s: %w", pidList(res.Moved), pidList(res.NotMoved), res.LedgerErr)
	}
}

// pendingSelection resolves the CLI selection against the pending
// proposals. It keeps the CLI's own errors for an unknown or decided ID
// and for an empty range. The shared flow reads the rows again by ID.
func pendingSelection(st *store.Store, raw string) ([]int64, error) {
	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return nil, err
	}

	ps, err := resolveSelection(pending, raw, "pending")
	if err != nil {
		return nil, err
	}

	return proposalIDs(ps), nil
}

// printPinBlock prints the gate-off answer: the pin block to paste. It
// leaves out the proposals whose pins the file already holds, because a
// paste of them would duplicate an entry. The gate-off apply never
// writes the ledger.
func printPinBlock(out io.Writer, pv *proposals.Preview) error {
	block := pv.YAML

	// The preview block holds every selected proposal. A display block
	// without the existing pins is needed only when there are some, so
	// the common case prints exactly the preview block.
	if len(pv.AlreadyPinned) > 0 {
		rest := slices.DeleteFunc(slices.Clone(pv.Proposals), func(p model.Proposal) bool {
			return slices.Contains(pv.AlreadyPinned, p.ID)
		})

		var err error

		if block, err = distill.RenderPins(rest); err != nil {
			return err
		}
	}

	if block == "" {
		fmt.Fprintf(out, ".seamark/lessons.yaml already holds the pins of %s — nothing to paste\n",
			pidList(pv.AlreadyPinned))
		fmt.Fprintln(out, "(proposals stay pending; enable distill.write to let apply record them as applied)")

		return nil
	}

	fmt.Fprintf(out, "distill.write is off in .seamark/config.yaml — paste under `pin:` in .seamark/lessons.yaml:\n\n%s\n", block)
	fmt.Fprintln(out, "(proposals stay pending; enable distill.write to let apply edit the file)")

	if len(pv.AlreadyPinned) > 0 {
		fmt.Fprintf(out, "(%s left out: .seamark/lessons.yaml already holds a pin with the same rule and regions)\n",
			pidList(pv.AlreadyPinned))
	}

	return nil
}

// applyPins commits a gate-on apply. A proposal whose pin is already in
// the pin file gets no second entry: a ledger-only reconcile records it
// as applied. PreviewApply refuses a pin that an applied proposal
// records, so such a pin has no applied row, for example a hand-written
// entry. The other proposals are written and recorded. The write
// comes first, so the first output line stays the "applied" line. ls is
// the ledger; tests inject a failing one through it.
func applyPins(out io.Writer, ls proposals.Ledgerer, root string, pv *proposals.Preview) error {
	pinned := pv.AlreadyPinned

	if rest := withoutIDs(proposalIDs(pv.Proposals), pinned); len(rest) > 0 {
		if err := writePins(out, ls, root, pv, rest); err != nil {
			return err
		}
	}

	if len(pinned) == 0 {
		return nil
	}

	rpv, err := proposals.PreviewReconcile(ls, root, pinned)
	if err != nil {
		return err
	}

	res, err := proposals.Commit(ls, root, rpv)
	if err != nil {
		return err
	}

	if res.LedgerErr != nil {
		return fmt.Errorf("%w — run `seamark lessons --apply %s` again to record them as applied",
			res.LedgerErr, pidArg(res.NotMoved))
	}

	fmt.Fprintf(out, "recorded %d proposal(s) as applied without a new pin — .seamark/lessons.yaml "+
		"already holds a pin with the same rule and regions\n", len(res.Moved))

	for _, p := range rpv.Proposals {
		fmt.Fprintf(out, "  p%-4d %s\n", p.ID, p.Rule)
	}

	return nil
}

// writePins writes and records the proposals of ids, none of which has
// a pin in the file yet, and prints the "applied" lines.
func writePins(out io.Writer, ls proposals.Ledgerer, root string, pv *proposals.Preview, ids []int64) error {
	// The reconcile of the existing pins runs after this write. A failure
	// here skips it, so the rerun advice must name those proposals too.
	pinned := pv.AlreadyPinned

	// Commit refuses an apply preview with existing pins, so the rest
	// needs its own preview. Nothing was written since the first one.
	if len(ids) < len(pv.Proposals) {
		var err error

		if pv, err = proposals.PreviewApply(ls, root, ids); err != nil {
			return err
		}
	}

	res, err := proposals.Commit(ls, root, pv)
	if err != nil {
		return err
	}

	if err := applyFailure(res, pinned); err != nil {
		return err
	}

	// The count is the ledger count, as before the shared flow. A row
	// that another writer decided during the commit stays listed, but is
	// not counted.
	fmt.Fprintf(out, "applied %d pin(s) to .seamark/lessons.yaml — review the diff and commit it\n", len(res.Moved))

	for _, p := range pv.Proposals {
		fmt.Fprintf(out, "  p%-4d %s\n", p.ID, p.Rule)
	}

	return nil
}

// applyFailure maps the write outcome of an apply commit to the CLI
// error. A pin write that changed nothing keeps the error text of the
// pin writer. A ledger failure after the pin write names the rerun that
// records the existing pins, because a rerun never writes a second pin.
// pinned holds the selected IDs whose pins were in the file before the
// write. They are still pending, so the rerun names them with the rest.
func applyFailure(res proposals.Result, pinned []int64) error {
	switch {
	case res.FileErr != nil && !res.FileWritten:
		return res.FileErr
	case res.FileErr != nil:
		return fmt.Errorf("%w — .seamark/lessons.yaml may have changed; check it with git diff before you apply again",
			res.FileErr)
	case res.LedgerErr != nil:
		return fmt.Errorf("the pins are in .seamark/lessons.yaml, but the ledger update failed: %w — run "+
			"`seamark lessons --apply %s` again to record them as applied without a second pin",
			res.LedgerErr, pidArg(slices.Sorted(slices.Values(slices.Concat(res.NotMoved, pinned)))))
	}

	return nil
}

// proposalIDs returns the IDs of ps in their order.
func proposalIDs(ps []model.Proposal) []int64 {
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}

	return ids
}

// withoutIDs returns the IDs of all that are not in drop, in order.
func withoutIDs(all, drop []int64) []int64 {
	var out []int64

	for _, id := range all {
		if !slices.Contains(drop, id) {
			out = append(out, id)
		}
	}

	return out
}

// pidList renders IDs in the ledger's pN form for prose: "p1, p2".
func pidList(ids []int64) string {
	return strings.Join(pids(ids), ", ")
}

// pidArg renders IDs as one --apply argument: "p1,p2".
func pidArg(ids []int64) string {
	return strings.Join(pids(ids), ",")
}

// pids renders each ID in the ledger's pN form.
func pids(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "p" + strconv.FormatInt(id, 10)
	}

	return out
}
