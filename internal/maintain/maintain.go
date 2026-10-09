// Package maintain inspects the installed pins and maintains the
// proposal-linked ones for the console and the CLI. A retarget changes
// the regions of a pin to the scope that today's evidence computes. A
// prune removes a pin and marks its proposal superseded. The CLI commits
// right after it plans, and it keeps no Recovery, because it has no
// session.
//
// Two stores hold the state, and no transaction spans both. The pin file
// .seamark/lessons.yaml is the authority for the installed guidance. The
// proposal ledger in the index database is the authority for the
// recorded decisions. This package relates the two and never repairs one
// from an assumption about the other.
//
// Inspect reads both and computes the readings once per snapshot. It
// writes nothing, takes no lock, and needs no store: without a ledger the
// pins stay listed and every relationship reads as unknown.
//
// NewPlan computes the complete candidate file, the ledger changes, and
// the reason for every entry that cannot change. It writes nothing. Commit
// plans again from the current file, rows, write gate, and evidence, and
// refuses with ErrStale unless the result is identical. It then replaces
// the file through the guarded atomic write of the setup engine, and
// updates the ledger in one short guarded transaction.
//
// A failure between the two writes is reported, not hidden. Commit puts
// the old file back only while the file still holds the bytes that it
// wrote and every ledger row still holds its planned values. Otherwise it
// leaves the file as it is and reports what it observed. It offers a
// Recovery only when it saw its own write land. A recovery completes the
// ledger update only while a fresh observation still proves the reviewed
// result. The proof lives in memory: after a restart, a bare absence of a
// pin never supersedes a proposal.
//
// No journal records a partial operation, and no general undo exists.
// So a quit or a restart loses the Recovery. A later session sees only
// the mismatch, through the link rules of pinfile.Classify. After a
// retarget, the new entry links no applied proposal, and the applied
// proposal reads as not present. After a prune, the applied proposal
// reads as not present. Nothing repairs the mismatch automatically,
// because the mismatch alone does not prove which writer made it. A
// repeat of the operation finds no linked pin, and NewPlan blocks it.
// So a hand edit reconciles the two stores.
//
// Many display fields hold text from the pin file, the ledger, or a
// model. Such text can contain terminal control bytes, so a caller
// sanitizes it before display. These fields are PlanItem.Reason, From,
// To, Facts, Kept, and Rows; Plan.Blocked and FileBlock; the Reason of
// every reading; the error fields of Result; and Recovery.Before and
// After. Only the texts in the const blocks of this package are fixed.
package maintain

import (
	"errors"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The errors of the plan and commit boundary. A commit that returns one
// of them wrote nothing.
var (
	// ErrNothingSelected reports a request without an entry.
	ErrNothingSelected = errors.New("no pin selected")
	// ErrNoLedger reports a proposal ledger that cannot be read. Pins then
	// stay listed, but no link to a proposal is known.
	ErrNoLedger = errors.New("unknown: the proposal ledger could not be read")
	// ErrStale reports that the pin file, the ledger rows, the write gate,
	// or the evidence changed since the preview.
	ErrStale = errors.New("the pin file, the ledger, the write gate, or the evidence changed since the preview")
	// ErrWriteDisabled reports a plan that was made while distill.write
	// was off. Such a plan never authorizes a write.
	ErrWriteDisabled = errors.New("distill.write is off")
	// ErrNotConfirmable reports a plan with a blocked or no-op entry, a
	// gate that cannot be read, or a file that cannot be replaced.
	ErrNotConfirmable = errors.New("the preview cannot be confirmed; revise the selection")
)

// Action is a maintenance operation. The zero value is not an action, so
// an unset request never changes a pin.
type Action int

// The operations.
const (
	// ActionRetarget changes the regions of linked pins to the computed
	// scope and updates the region fields of their proposals. The
	// proposals stay applied, and their recorded notes stay, because the
	// guidance stays installed. The pin identity holds the regions. So
	// the delivery history in the firing log restarts under the new
	// identity, and a plan note says so.
	ActionRetarget Action = iota + 1
	// ActionPrune removes linked pins and moves their proposals from
	// applied to superseded. A prune is a deliberate retirement of
	// installed guidance. It differs from a dismissal, which decides a
	// pending proposal. It also differs from the removal of stale pending
	// proposals in a distillation run, which never touches a pin.
	ActionPrune
)

// String returns the lower-case name of the action.
func (a Action) String() string {
	switch a {
	case ActionRetarget:
		return "retarget"
	case ActionPrune:
		return "prune"
	default:
		return "Action(" + strconv.Itoa(int(a)) + ")"
	}
}

// Request names the entries that the user selected. A plan binds to each
// entry as the Pins view read it, never to a list position alone.
type Request struct {
	Action Action
	// Refs are the reviewed entries, in selection order.
	Refs []pinfile.EntryRef
	// Cursor is true when no entry was marked and the request names the
	// cursor row. The preview then says so.
	Cursor bool
}

// LedgerWriter is the ledger boundary of a commit. *store.Store
// implements it. Tests replace it to inject a failure after the file
// write.
type LedgerWriter interface {
	ProposalsWithIDs(ids []int64) ([]model.Proposal, error)
	RetargetApplied(changes []store.RegionChange) error
	SupersedeExact(guards []store.RowGuard) error
}

// FileWriter is the pin file boundary of a commit. The default writes
// through pinfile.Prepare, Apply, and Restore. Tests replace it to inject
// a failure or another writer.
type FileWriter interface {
	// Replace writes after over the file that expect observed. An error
	// that wraps pinfile.ErrStale means that the file changed first.
	Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error
	// Restore writes original back, but only while the file holds the
	// bytes with the SHA-256 written.
	Restore(reg *integration.Registry, root string, written [32]byte, original []byte) error
}

// Deps are the inputs and the boundaries of NewPlan and Commit.
type Deps struct {
	// Root is the effective repository root: the root that the index
	// records, else the workspace root.
	Root string
	// Reg is the client registry of the setup engine.
	Reg *integration.Registry
	// Reader reads the ledger and the evidence. NewPlan accepts a
	// read-only store. Commit plans again through it.
	Reader *store.Store
	// Ledger writes the ledger. Nil uses Reader, which must then be
	// writable.
	Ledger LedgerWriter
	// Files writes the pin file. Nil uses the guarded pinfile write.
	Files FileWriter
	// Probe is a fresh observation of the index. A retarget needs a
	// ready index, because the scope advice reads its history.
	Probe workspace.IndexProbe
	// Now reads the clock. Nil reads the system clock.
	Now func() time.Time
}

// ledger returns the ledger boundary of d. A nil Reader gives a nil
// boundary only when no Ledger is set.
func (d Deps) ledger() LedgerWriter {
	if d.Ledger != nil {
		return d.Ledger
	}

	if d.Reader == nil {
		return nil
	}

	return d.Reader
}

// files returns the file boundary of d.
func (d Deps) files() FileWriter {
	if d.Files != nil {
		return d.Files
	}

	return pinWriter{}
}

// now reads the clock of d.
func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}

	return time.Now()
}

// pinWriter is the production FileWriter: the guarded atomic write of
// the pin file through integration Route A.
type pinWriter struct{}

// Replace prepares and applies the guarded write.
func (pinWriter) Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error {
	p, err := pinfile.Prepare(reg, root, expect, after)
	if err != nil {
		return err
	}

	return p.Apply()
}

// Restore delegates to pinfile.Restore.
func (pinWriter) Restore(reg *integration.Registry, root string, written [32]byte, original []byte) error {
	return pinfile.Restore(reg, root, written, original)
}
