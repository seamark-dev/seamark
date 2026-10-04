package maintain

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Result is the outcome of a commit. The file and the ledger have
// separate outcomes, because no transaction spans both. The error
// fields hold file paths and store text, so a caller sanitizes them
// before display.
type Result struct {
	Action Action
	// FileWritten is true when the pin file holds the new bytes, or when
	// a failed write could not be checked. It never claims an untouched
	// file that was not observed.
	FileWritten bool
	FileErr     error
	// LedgerDone is true when every planned row holds its target values.
	LedgerDone bool
	LedgerErr  error
	// Restored is true only when the restore write succeeded.
	Restored   bool
	RestoreErr error
	// RestoreRefused says why no restore ran over the file of another
	// writer.
	RestoreRefused string
	// Observed holds the planned rows as read again after a failure;
	// ObservedErr is the failure of that read.
	Observed    []model.Proposal
	ObservedErr error
	// Moved lists the proposals that this commit changed, ascending.
	Moved []int64
	// Recovery is the session proof of a partial operation. It is set
	// when the commit neither restored the file nor completed the ledger,
	// and the session saw its own write land. A write that could not be
	// checked offers no recovery, because a prune recovery then rests on
	// a bare absence (D-13).
	Recovery *Recovery
	// Note is one plain sentence about the outcome. It names the step
	// that failed: the file write or the ledger update.
	Note string
}

// Partial reports a commit that changed the file or could not check it,
// did not complete the ledger, and did not restore the file.
func (r Result) Partial() bool {
	return r.FileWritten && !r.LedgerDone && !r.Restored
}

// Recovery is the in-memory proof that this session wrote the pin file
// of an operation whose ledger update did not complete. The session saw
// the written bytes in the file. It does not survive a restart: a later
// session sees only the mismatch.
type Recovery struct {
	Action  Action
	Request Request
	// Items are the reviewed items, in the order of the edits and the
	// ledger changes.
	Items []PlanItem
	// Written is the SHA-256 of the bytes that the session wrote.
	Written [32]byte
	// Before and After are the reviewed file contents. They hold file
	// data, so a caller sanitizes them before display.
	Before, After []byte
	// Edits and NewKeys are the reviewed file edits and their target keys.
	Edits   []pinfile.Edit
	NewKeys []reviews.PinKey
	// Retarget and Supersede are the ledger changes that did not complete.
	Retarget  []store.RegionChange
	Supersede []store.RowGuard
}

// The notes of a result. A note names the step that failed, because the
// console shows it as the plain outcome of the operation.
const (
	noteDone          = "the pin file and the ledger changed together"
	noteFileFailed    = "the pin file write failed; this operation changed nothing"
	noteRestored      = "the ledger update failed; the pin file holds the reviewed content again, and the ledger did not change"
	noteRestoreFailed = "reconciliation required: the ledger update failed, the pin file was not restored, and the ledger rows keep their earlier values"
	noteFileRestored  = "the pin file write reported a failure after the new bytes landed; the pin file holds the reviewed content again, and the ledger did not change"
	noteFileKept      = "reconciliation required: the pin file write reported a failure after the new bytes landed, the pin file was not restored, and the ledger rows keep their earlier values"
	noteFileUnread    = "reconciliation required: the pin file write reported a failure, and the file could not be read again; nothing was restored, the ledger did not change, and no recovery is offered"
	noteReread        = "reconciliation required: the ledger rows could not be read again, so nothing was restored"
	noteOtherWriter   = "the ledger was completed by another writer"
	noteOtherState    = "reconciliation required: the ledger rows changed in another way, so nothing was restored"
	noteRecovered     = "the ledger now matches the pin file"
	noteRecoverFailed = "the ledger update failed again; nothing changed, and the recovery stays available"
	refusedRestore    = "another writer changed the file; nothing restored"
)

// Commit applies a confirmed plan. It plans again from the current
// observations and refuses with ErrStale, before any write, unless the
// new plan is identical. It then replaces the pin file and updates the
// ledger in one guarded transaction. A non-nil error means that no write
// started. After the file write starts, every outcome is in the Result
// and the error is nil. Nothing is retried.
func Commit(deps Deps, p *Plan) (Result, error) {
	if p == nil {
		return Result{}, errors.New("maintain: no plan")
	}

	res := Result{Action: p.Action}

	if p.Recovery != nil {
		return res, errors.New("maintain: a recovery plan commits through CommitRecovery")
	}

	if err := confirmable(p); err != nil {
		return res, err
	}

	ledger := deps.ledger()
	if ledger == nil {
		return res, fmt.Errorf("%w: no ledger writer", ErrNoLedger)
	}

	q, err := NewPlan(deps, p.Request)
	if err != nil {
		return res, err
	}

	if reasons := differences(p, q); len(reasons) > 0 {
		return res, fmt.Errorf("%w: %s", ErrStale, strings.Join(reasons, "; "))
	}

	if err := confirmable(q); err != nil {
		return res, err
	}

	commit(deps, ledger, q, &res)

	return res, nil
}

// confirmable returns why a plan cannot be committed, or nil.
func confirmable(p *Plan) error {
	switch {
	case p.Confirmable:
		return nil
	case p.GateErr == nil && !p.Write:
		return ErrWriteDisabled
	}

	return ErrNotConfirmable
}

// commit writes the file and then the ledger of a revalidated plan.
func commit(deps Deps, ledger LedgerWriter, q *Plan, res *Result) {
	written := sha256.Sum256(q.Change.After)

	if err := deps.files().Replace(deps.Reg, deps.Root, q.File, q.Change.After); err != nil {
		if errors.Is(err, pinfile.ErrStale) {
			err = fmt.Errorf("%w: %w", ErrStale, err)
		}

		res.FileErr = err

		// A failed write is observed, never assumed: the result says what
		// the file holds now.
		holds, rerr := fileHolds(deps.Root, written)
		res.FileWritten = holds || rerr != nil

		from := fromFile

		if rerr != nil {
			from = fromUnread
			res.FileErr = errors.Join(err, fmt.Errorf("the file could not be read after the failure: %w", rerr))
		}

		if !res.FileWritten {
			res.Note = noteFileFailed

			return
		}

		compensate(deps, ledger, q, written, from, res)

		return
	}

	res.FileWritten = true

	if err := writeLedger(ledger, q); err != nil {
		res.LedgerErr = err
		compensate(deps, ledger, q, written, fromLedger, res)

		return
	}

	res.LedgerDone = true
	res.Moved = plannedIDs(q)
	res.Note = noteDone
}

// writeLedger runs the guarded ledger transaction of a plan.
func writeLedger(ledger LedgerWriter, q *Plan) error {
	if q.Action == ActionPrune {
		return ledger.SupersedeExact(q.Supersede)
	}

	return ledger.RetargetApplied(q.Retarget)
}

// fileHolds reports whether the pin file under root holds the bytes
// with the SHA-256 written.
func fileHolds(root string, written [32]byte) (bool, error) {
	guard, data, err := integration.ReadInput(root, pinfile.File)
	if err != nil {
		return false, err
	}

	return guard.Exists && sha256.Sum256(data) == written, nil
}

// origin is the failed step that starts a compensation. It selects the
// note, because the note must name the step that really failed. It also
// decides the recovery offer, because only an observed write proves that
// this session changed the file.
type origin int

const (
	// fromLedger means that the file write succeeded and the ledger
	// update failed.
	fromLedger origin = iota
	// fromFile means that the file write reported a failure, but the
	// file holds the new bytes.
	fromFile
	// fromUnread means that the file write reported a failure, and the
	// file could not be read again.
	fromUnread
)

// restored returns the note of a compensation that restored the file.
// A restore writes only over the written bytes, so after it every origin
// knows that the new bytes landed.
func (o origin) restored() string {
	if o == fromLedger {
		return noteRestored
	}

	return noteFileRestored
}

// kept returns the note of a compensation whose restore did not run or
// failed.
func (o origin) kept() string {
	switch o {
	case fromLedger:
		return noteRestoreFailed
	case fromFile:
		return noteFileKept
	}

	return noteFileUnread
}

// compensate handles a file that changed, or could not be checked, while
// the ledger did not change. It reads the planned rows again. When every
// row still holds its planned values, it restores the old bytes, but only
// over the bytes that this commit wrote. When every row already holds its
// target, another writer completed the ledger. Any other state needs
// reconciliation.
func compensate(deps Deps, ledger LedgerWriter, q *Plan, written [32]byte, from origin, res *Result) {
	// A prune recovery proves only that a key is absent. That proof is
	// valid only when this session saw its own write land (D-13).
	offer := func() {
		if from != fromUnread {
			res.Recovery = newRecovery(q, written)
		}
	}

	observed, err := ledger.ProposalsWithIDs(plannedIDs(q))
	if err != nil {
		res.ObservedErr = err
		res.Note = noteReread
		offer()

		return
	}

	res.Observed = observed

	switch rowsHold(q, observed) {
	case holdPlanned:
		err := deps.files().Restore(deps.Reg, deps.Root, written, q.Change.Before)
		if err == nil {
			res.Restored = true
			res.Note = from.restored()

			return
		}

		res.RestoreErr = err
		if errors.Is(err, pinfile.ErrStale) {
			res.RestoreRefused = refusedRestore
		}

		res.Note = from.kept()
		offer()
	case holdTarget:
		res.LedgerDone = true
		res.Note = noteOtherWriter
	default:
		res.Note = noteOtherState
		offer()
	}
}

// rowHold is the state of the planned rows after a failure.
type rowHold int

const (
	// holdOther means that the rows hold neither state, or one is gone.
	holdOther rowHold = iota
	// holdPlanned means that every row holds its planned values.
	holdPlanned
	// holdTarget means that every row holds its target values.
	holdTarget
)

// RowsHoldPlanned compares the planned ledger rows of p, as read again,
// with their planned and their target values. planned is true when each
// planned row still holds the values that the plan read. target is true
// when each planned row holds the values that the commit writes. A row
// that observed lacks holds neither. The check uses the store guard
// comparison, so a caller that describes the rows agrees with the
// guarded transaction. A plan without a ledger change gives true for
// both, as every check over no rows does; a ready item always carries a
// ledger change.
func RowsHoldPlanned(p *Plan, observed []model.Proposal) (planned, target bool) {
	byID := make(map[int64]model.Proposal, len(observed))
	for _, row := range observed {
		byID[row.ID] = row
	}

	planned, target = true, true

	for _, g := range guardPairs(p) {
		row, ok := byID[g[0].ID]

		planned = planned && ok && g[0].Matches(row)
		target = target && ok && g[1].Matches(row)
	}

	return planned, target
}

// rowsHold classifies the observed rows of a plan. The planned values
// come first, because only then may the commit restore the file.
func rowsHold(q *Plan, observed []model.Proposal) rowHold {
	planned, target := RowsHoldPlanned(q, observed)

	switch {
	case planned:
		return holdPlanned
	case target:
		return holdTarget
	}

	return holdOther
}

// guardPairs returns, per ledger change, the guard of the planned row and
// a guard that holds the target values.
func guardPairs(q *Plan) [][2]store.RowGuard {
	var out [][2]store.RowGuard

	for _, g := range q.Supersede {
		done := g
		done.Status = model.ProposalSuperseded
		out = append(out, [2]store.RowGuard{g, done})
	}

	for _, c := range q.Retarget {
		done := c.Guard
		done.Region, done.Regions = c.Region, slices.Clone(c.Regions)
		out = append(out, [2]store.RowGuard{c.Guard, done})
	}

	return out
}

// plannedIDs returns the IDs of the ledger changes, ascending.
func plannedIDs(q *Plan) []int64 {
	var ids []int64

	for _, g := range guardPairs(q) {
		ids = append(ids, g[0].ID)
	}

	slices.Sort(ids)

	return ids
}

// newRecovery records the session proof of a plan whose file write
// landed.
func newRecovery(q *Plan, written [32]byte) *Recovery {
	return &Recovery{
		Action: q.Action, Request: q.Request, Items: slices.Clone(q.Items), Written: written,
		Before: bytes.Clone(q.Change.Before), After: bytes.Clone(q.Change.After),
		Edits: slices.Clone(q.Change.Edits), NewKeys: slices.Clone(q.Change.NewKeys),
		Retarget: slices.Clone(q.Retarget), Supersede: slices.Clone(q.Supersede),
	}
}

// differences names what changed between a reviewed plan and the plan
// from the current observations. An empty result means identical.
func differences(p, q *Plan) []string {
	var reasons []string

	add := func(changed bool, reason string) {
		if changed {
			reasons = append(reasons, reason)
		}
	}

	add(p.File.Exists != q.File.Exists || p.File.SHA256 != q.File.SHA256, "the pin file changed")
	add(!sameChange(p.Change, q.Change), "the planned file content changed")
	add(p.Write != q.Write || (p.GateErr == nil) != (q.GateErr == nil), "distill.write changed")
	add(p.FileBlock != q.FileBlock, "the pin file can no longer be changed")
	add(!reflect.DeepEqual(p.Retarget, q.Retarget) || !reflect.DeepEqual(p.Supersede, q.Supersede),
		"the ledger rows changed")
	add(p.Evidence != q.Evidence, "the evidence or the index changed")

	if len(p.Items) != len(q.Items) {
		return append(reasons, "the selection changed")
	}

	for i := range p.Items {
		add(!sameItem(p.Items[i], q.Items[i]), fmt.Sprintf("the plan of entry %d changed", p.Items[i].Ref.Index+1))
	}

	return reasons
}

// sameChange compares two candidate files and their edits.
func sameChange(a, b *pinfile.Change) bool {
	if a == nil || b == nil {
		return a == b
	}

	return bytes.Equal(a.Before, b.Before) && bytes.Equal(a.After, b.After) &&
		reflect.DeepEqual(a.Edits, b.Edits) && reflect.DeepEqual(a.NewKeys, b.NewKeys)
}

// sameItem compares the reviewed facts of two items: the entry, its
// rows, the state, the scope, and the retained alternatives.
func sameItem(a, b PlanItem) bool {
	return a.Ref == b.Ref && a.Status == b.Status && a.Reason == b.Reason &&
		slices.Equal(a.From, b.From) && slices.Equal(a.To, b.To) &&
		a.Broader == b.Broader && a.RepoWide == b.RepoWide && slices.Equal(a.Facts, b.Facts) &&
		reflect.DeepEqual(a.Rows, b.Rows) && a.Pin.Link.NoteChanged == b.Pin.Link.NoteChanged &&
		slices.EqualFunc(a.Kept, b.Kept, func(x, y Pin) bool {
			return x.Entry.Ref() == y.Entry.Ref() && reflect.DeepEqual(x.Link.Applied, y.Link.Applied)
		})
}
