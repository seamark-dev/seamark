package proposals

// The decision side. A decision is two steps: a preview that writes
// nothing, and a commit that revalidates the preview and then writes.
// The pin file and the ledger are two stores, so a commit is not one
// transaction. The Result therefore reports each write separately, and
// nothing retries: a blind retry after a pin write duplicates pins.

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Action is the kind of decision a preview describes.
type Action int

const (
	// ActionApply turns pending proposals into pins.
	ActionApply Action = iota
	// ActionDismiss marks pending proposals dismissed. It writes only the
	// ledger, so a pin with the same rule and regions stays in the pin
	// file. A dismiss rejects a proposal, not an installed pin; a prune
	// of internal/maintain removes a pin.
	ActionDismiss
	// ActionReconcile records pending proposals as applied when their
	// pins already exist in the pin file. It writes only the ledger, so
	// a partial apply never needs a duplicate-producing retry.
	ActionReconcile
)

// String returns "apply", "dismiss" or "reconcile".
func (a Action) String() string {
	switch a {
	case ActionApply:
		return "apply"
	case ActionDismiss:
		return "dismiss"
	case ActionReconcile:
		return "reconcile"
	default:
		return "Action(" + strconv.Itoa(int(a)) + ")"
	}
}

var (
	// ErrNothingSelected reports an empty selection.
	ErrNothingSelected = errors.New("no pending proposal selected")
	// ErrStale reports that meaningful inputs changed after the
	// preview; the caller must build a new preview.
	ErrStale = errors.New("the ledger, the pin file, or the write gate changed since the preview")
	// ErrWriteDisabled reports an apply commit while distill.write is
	// off. The preview then offers the YAML block instead.
	ErrWriteDisabled = errors.New("distill.write is off")
	// ErrNeedsReconcile reports selected proposals whose pins already
	// exist in the pin file. A blind apply would duplicate them.
	ErrNeedsReconcile = errors.New("pins for the selected proposals already exist")
	// ErrNotPinned reports a reconcile selection with proposals whose
	// pins are not in the pin file. Reconcile records existing pins
	// only; a missing pin needs apply.
	ErrNotPinned = errors.New("no pin exists yet in " + LessonsFile + " for the selected proposals")
	// ErrFieldsDisagree reports an apply or reconcile selection with
	// proposals whose stored region fields disagree (pinfile.FieldsAgree).
	// Their apply writes a pin with another key than the key that every
	// reader links. So no reader links that pin to them, and no existing
	// pin is proven to be theirs. The preview refusal asks for a dismiss
	// or a fix by hand, because no decision of this package changes the
	// stored region fields.
	ErrFieldsDisagree = errors.New("the stored region fields of the selected proposals disagree")
	// ErrSharedKey reports an apply or reconcile selection with two or
	// more proposals that share one pin key. A *SharedKeyError carries
	// their IDs and matches this error.
	ErrSharedKey = errors.New("selected proposals share rule and regions")
	// ErrAppliedKey reports an apply or reconcile selection with a
	// proposal whose pin key an applied proposal already records. An
	// *AppliedKeyError carries the IDs and matches this error.
	ErrAppliedKey = errors.New("selected proposals have the rule and regions of an applied proposal")
)

// SharedKeyError reports the selected proposals that share one pin key.
// Apply and reconcile refuse them: two entries with one key are
// duplicate pins, and one entry that two applied proposals record is
// ambiguous. The link rules of internal/pinfile block both, so the
// console cannot change such a pin.
type SharedKeyError struct {
	// Groups holds the IDs of each shared key in ID order. The groups
	// are in the order of their lowest ID.
	Groups [][]int64
}

// Error names each group and the next step, for example "p1 and p2
// share rule and regions; apply one and dismiss the other". A reconcile
// is an apply without a pin write, so the same advice fits both.
func (e *SharedKeyError) Error() string {
	if len(e.Groups) == 0 {
		return ErrSharedKey.Error()
	}

	parts := make([]string, len(e.Groups))
	for i, g := range e.Groups {
		parts[i] = proseList(g) + " share rule and regions"
	}

	advice := "apply one and dismiss the other"

	switch {
	case len(e.Groups) > 1:
		advice = "apply one of each group and dismiss the others"
	case len(e.Groups[0]) > 2:
		advice = "apply one and dismiss the others"
	}

	return strings.Join(parts, "; ") + "; " + advice
}

// Is matches ErrSharedKey, so a caller can test the kind with errors.Is
// and read the IDs with errors.As.
func (e *SharedKeyError) Is(target error) bool {
	return target == ErrSharedKey
}

// sharedKeys returns a *SharedKeyError for the proposals of ps that
// share a pin key, or nil. ps must be in ID order, as a preview holds it.
func sharedKeys(ps []model.Proposal) error {
	byKey := make(map[reviews.PinKey][]int64, len(ps))

	var order []reviews.PinKey

	for _, p := range ps {
		key := pinKey(p)
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}

		byKey[key] = append(byKey[key], p.ID)
	}

	var groups [][]int64

	for _, key := range order {
		if ids := byKey[key]; len(ids) > 1 {
			groups = append(groups, ids)
		}
	}

	if len(groups) == 0 {
		return nil
	}

	return &SharedKeyError{Groups: groups}
}

// AppliedKeyError reports the selected pending proposals whose pin key
// an applied proposal already records. Pins blocks a pin that two
// applied proposals record, so an apply must never record a second
// applied row for one key. Apply and reconcile refuse such a row, also
// when the pin file does not hold the pin: a new pin with that key is
// blocked in the same way.
type AppliedKeyError struct {
	// Groups holds one group for each refused key. The groups are in the
	// order of their lowest pending ID.
	Groups []AppliedKeyGroup
}

// AppliedKeyGroup names the proposals of one refused pin key.
type AppliedKeyGroup struct {
	// Applied holds the IDs of the applied proposals with the key, in ID
	// order. An old ledger can hold more than one.
	Applied []int64
	// Pending holds the IDs of the selected pending proposals with the
	// key, in ID order.
	Pending []int64
}

// Error names the applied proposals and the pending proposals to
// dismiss, for example "p1 is already applied with this rule and
// regions; dismiss p3". With several keys, each group names the pending
// proposals whose rule and regions it means, because "this" is then
// unclear.
func (e *AppliedKeyError) Error() string {
	if len(e.Groups) == 0 {
		return ErrAppliedKey.Error()
	}

	parts := make([]string, len(e.Groups))
	for i, g := range e.Groups {
		verb := " is"
		if len(g.Applied) > 1 {
			verb = " are"
		}

		which := "this rule and regions"
		if len(e.Groups) > 1 {
			which = "the rule and regions of " + proseList(g.Pending)
		}

		parts[i] = proseList(g.Applied) + verb + " already applied with " + which
	}

	return strings.Join(parts, "; ") + "; dismiss " + proseList(e.Pending())
}

// Is matches ErrAppliedKey, so a caller can test the kind with
// errors.Is and read the IDs with errors.As.
func (e *AppliedKeyError) Is(target error) bool {
	return target == ErrAppliedKey
}

// Pending returns the IDs of the refused pending proposals of every
// group, in ID order. The caller dismisses them.
func (e *AppliedKeyError) Pending() []int64 {
	var out []int64
	for _, g := range e.Groups {
		out = append(out, g.Pending...)
	}

	slices.Sort(out)

	return out
}

// Applied returns the IDs of the applied proposals of every group, in
// ID order. A row has one key, so no ID is in two groups.
func (e *AppliedKeyError) Applied() []int64 {
	var out []int64
	for _, g := range e.Groups {
		out = append(out, g.Applied...)
	}

	slices.Sort(out)

	return out
}

// appliedKeys reads the applied proposals and returns the refusal of
// CheckAppliedKeys as an error, or nil. ps must be in ID order, with
// agreeing region fields.
func appliedKeys(ls Ledgerer, ps []model.Proposal) error {
	applied, err := ls.Proposals(model.ProposalApplied)
	if err != nil {
		return fmt.Errorf("read the applied proposals: %w", err)
	}

	// A nil *AppliedKeyError in an error value is not a nil error, so
	// the check returns an untyped nil here.
	if taken := CheckAppliedKeys(applied, ps); taken != nil {
		return taken
	}

	return nil
}

// CheckAppliedKeys returns an *AppliedKeyError for the proposals of ps
// whose pin key a row of applied records, or nil. ps must be in ID
// order, with agreeing region fields. It links an applied row by
// reviews.ProposalKey, because Pins counts the applied rows of a key
// with that key. It reads nothing, so the console can apply the rule of
// the preview to its ledger snapshot.
func CheckAppliedKeys(applied, ps []model.Proposal) *AppliedKeyError {
	byKey := make(map[reviews.PinKey][]int64, len(applied))
	for _, a := range applied {
		key := reviews.ProposalKey(a)
		byKey[key] = append(byKey[key], a.ID)
	}

	var groups []AppliedKeyGroup

	at := map[reviews.PinKey]int{}

	for _, p := range ps {
		key := pinKey(p)

		ids, ok := byKey[key]
		if !ok {
			continue
		}

		i, seen := at[key]
		if !seen {
			i = len(groups)
			at[key] = i
			groups = append(groups, AppliedKeyGroup{Applied: slices.Sorted(slices.Values(ids))})
		}

		groups[i].Pending = append(groups[i].Pending, p.ID)
	}

	if len(groups) == 0 {
		return nil
	}

	return &AppliedKeyError{Groups: groups}
}

// recheckAppliedKeys runs appliedKeys again at commit time. The
// fingerprints cover only the selected rows, so another writer can apply
// a proposal with a selected key after the preview. Such a refusal also
// matches ErrStale, because a new preview then names it.
func recheckAppliedKeys(ls Ledgerer, ps []model.Proposal) error {
	err := appliedKeys(ls, ps)
	if errors.Is(err, ErrAppliedKey) {
		return fmt.Errorf("%w: %w", ErrStale, err)
	}

	return err
}

// Fingerprint is the identity of one proposal at preview time. IDs can
// be reused after a prune, so the ID alone is not enough.
type Fingerprint struct {
	ID        int64
	Signature string
	Rule      string
	Note      string
	Regions   []string
}

// fingerprint captures the fields that decide what a pin says and
// where it applies. Regions uses RegionSet, the set RenderPins writes.
func fingerprint(p model.Proposal) Fingerprint {
	return Fingerprint{
		ID:        p.ID,
		Signature: p.Signature,
		Rule:      p.Rule,
		Note:      p.Note,
		Regions:   p.RegionSet(),
	}
}

// equal compares two fingerprints field by field. The region order
// counts, because the first region becomes the pin's `region` key.
func (f Fingerprint) equal(g Fingerprint) bool {
	return f.ID == g.ID && f.Signature == g.Signature && f.Rule == g.Rule &&
		f.Note == g.Note && slices.Equal(f.Regions, g.Regions)
}

// FileObservation records the pin file state at preview time.
type FileObservation struct {
	Exists bool
	SHA256 [32]byte
}

// Preview describes one pending decision exactly. It is not an
// authorization: Commit rejects it when an input changed.
type Preview struct {
	Action Action
	// Proposals holds the eligible selected proposals in ID order.
	Proposals []model.Proposal
	// Prints holds their fingerprints, parallel to Proposals.
	Prints []Fingerprint
	// Ineligible holds requested IDs that are no longer pending.
	Ineligible []int64
	// Write is the distill.write gate at preview time (apply only).
	Write bool
	// YAML is the pin block that apply inserts at the head of the
	// `pin:` list.
	YAML string
	// Target is the pin file path relative to the root.
	Target string
	// Lessons is the pin file observation (apply only).
	Lessons FileObservation
	// LessonsErr holds the pin file guard failure of an apply preview
	// taken with the gate off, for example a symbolic link. That
	// preview writes nothing, so the failure does not stop the YAML
	// block; Lessons is then zero and Commit refuses the preview.
	LessonsErr error
	// AlreadyPinned holds selected IDs whose pin already exists. No
	// applied proposal records such a pin, because the preview refuses
	// that case first (ErrAppliedKey).
	AlreadyPinned []int64
}

// ids returns the eligible IDs in preview order.
func (pv *Preview) ids() []int64 {
	out := make([]int64, len(pv.Prints))
	for i, f := range pv.Prints {
		out[i] = f.ID
	}

	return out
}

// Ledgerer is the ledger access a decision needs. *store.Store
// implements it; tests inject failures through it.
type Ledgerer interface {
	ProposalsByIDs(ids []int64) ([]model.Proposal, error)
	// Proposals returns the rows of one status. Apply and reconcile read
	// the applied rows, because Pins blocks a pin key that more than one
	// applied row records.
	Proposals(status string) ([]model.Proposal, error)
	SetProposalStatus(ids []int64, to string) (int, error)
}

// PreviewApply validates the selection and describes the pin change.
// It writes nothing. It refuses three kinds of selection, with the gate
// on or off, because each gives a pin that Pins blocks at once. The
// first kind holds a proposal whose region fields disagree: no reader
// links the pin that its apply writes. The second holds a proposal whose
// pin key an applied proposal records (ErrAppliedKey). The third holds
// proposals that share one pin key (ErrSharedKey).
//
// The applied key check runs before the shared key check. Its advice
// (dismiss) is final, and the apply of one row of a shared group meets
// it again. It also runs before the pin file read. So the pin of an
// applied proposal never reaches AlreadyPinned, which the CLI records
// as applied without a write.
func PreviewApply(ls Ledgerer, root string, ids []int64) (*Preview, error) {
	pv, err := previewSelection(ls, ActionApply, ids)
	if err != nil {
		return nil, err
	}

	if odd := disagreeing(pv.Proposals); len(odd) > 0 {
		return nil, fmt.Errorf("%w: %s; resolve them by hand or dismiss them", ErrFieldsDisagree, idList(odd))
	}

	if err := appliedKeys(ls, pv.Proposals); err != nil {
		return nil, err
	}

	if err := sharedKeys(pv.Proposals); err != nil {
		return nil, err
	}

	if err := observePins(pv, root); err != nil {
		return nil, err
	}

	// The block is what the CLI prints with the gate off and what the
	// commit inserts with the gate on: one renderer for both. Every
	// selected proposal has its own key, so the block holds one entry
	// for each of them.
	pv.YAML, err = distill.RenderPins(pv.Proposals)
	if err != nil {
		return nil, err
	}

	return pv, nil
}

// PreviewDismiss validates the selection and describes the ledger
// change. It writes nothing.
func PreviewDismiss(ls Ledgerer, ids []int64) (*Preview, error) {
	return previewSelection(ls, ActionDismiss, ids)
}

// PreviewReconcile validates that every selected pending proposal
// already has its pin and describes the ledger-only transition to
// applied. It writes nothing. It refuses a proposal whose region fields
// disagree, as Pins and extraction do: the pin check reads the rendered
// key, and every other reader links the row by another key. It also
// refuses a proposal whose pin key an applied proposal records
// (ErrAppliedKey) and proposals that share one pin key (ErrSharedKey),
// as apply does: two applied proposals that record one pin make Pins
// block it. The applied key check runs before the pin check, so a pin
// of an applied proposal never reads as the pin of the selected one.
func PreviewReconcile(ls Ledgerer, root string, ids []int64) (*Preview, error) {
	pv, err := previewSelection(ls, ActionReconcile, ids)
	if err != nil {
		return nil, err
	}

	if odd := disagreeing(pv.Proposals); len(odd) > 0 {
		return nil, fmt.Errorf("%w: %s; resolve them by hand or dismiss them", ErrFieldsDisagree, idList(odd))
	}

	if err := appliedKeys(ls, pv.Proposals); err != nil {
		return nil, err
	}

	if err := sharedKeys(pv.Proposals); err != nil {
		return nil, err
	}

	// The gate does not guard a ledger-only change. The preview still
	// records it, so the caller can show the complete state.
	if err := observePins(pv, root); err != nil {
		return nil, err
	}

	if missing := without(pv.ids(), pv.AlreadyPinned); len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s; use apply for them", ErrNotPinned, idList(missing))
	}

	return pv, nil
}

// previewSelection reads the selection back from the ledger. Only
// pending rows come back, so every other requested ID is reported as
// ineligible instead of silently dropped.
func previewSelection(ls Ledgerer, action Action, ids []int64) (*Preview, error) {
	if len(ids) == 0 {
		return nil, ErrNothingSelected
	}

	want := slices.Clone(ids)
	slices.Sort(want)
	want = slices.Compact(want)

	ps, err := ls.ProposalsByIDs(want)
	if err != nil {
		return nil, fmt.Errorf("read the selected proposals: %w", err)
	}

	sortByID(ps)

	pv := &Preview{Action: action, Proposals: ps, Prints: make([]Fingerprint, len(ps))}

	for i, p := range ps {
		pv.Prints[i] = fingerprint(p)
	}

	pv.Ineligible = without(want, pv.ids())

	return pv, nil
}

// observePins records the write gate, the pin file state, and the
// selected proposals whose pins already exist. A config read failure
// is returned as the reader's error: defaults would misstate the gate
// or hide existing pins.
//
// A pin file guard failure stops every preview except an apply with
// the gate off. That preview only offers the YAML block for a manual
// paste, as the CLI does, so the failure goes into LessonsErr instead.
// A reconcile preview keeps the refusal, because its commit writes the
// ledger on the evidence of the pin file.
func observePins(pv *Preview, root string) error {
	cfg, err := distill.LoadConfig(root)
	if err != nil {
		return err
	}

	pv.Write = cfg.Distill.Write
	pv.Target = LessonsFile

	if pv.Lessons, err = observeLessons(root); err != nil {
		if pv.Action != ActionApply || pv.Write {
			return err
		}

		pv.LessonsErr = err
	}

	pv.AlreadyPinned, err = alreadyPinned(root, pv.Proposals)

	return err
}

// observeLessons hashes the pin file through the shared setup guard.
// The guard refuses a symbolic link at any path component, because a
// committed link must never redirect a pin write out of the repository.
func observeLessons(root string) (FileObservation, error) {
	guard, _, err := integration.ReadGuarded(root, LessonsFile)
	if err != nil {
		return FileObservation{}, err
	}

	return FileObservation{Exists: guard.Exists, SHA256: guard.SHA256}, nil
}

// alreadyPinned returns the IDs of the proposals whose pin the pin file
// already carries. A malformed pin file is an error, never defaults:
// defaults have no pins and would allow a duplicate.
func alreadyPinned(root string, ps []model.Proposal) ([]int64, error) {
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		return nil, err
	}

	var out []int64

	for _, p := range ps {
		if _, ok := cfg.FindPin(pinKey(p)); ok {
			out = append(out, p.ID)
		}
	}

	return out, nil
}

// pinKey is the identity of the pin that RenderPins writes for p
// (pinfile.RenderedKey). It can differ from reviews.ProposalKey only when
// the region fields disagree, and apply and reconcile refuse such a row.
func pinKey(p model.Proposal) reviews.PinKey {
	return pinfile.RenderedKey(p)
}

// disagreeing returns the IDs of the proposals whose stored region
// fields disagree (pinfile.FieldsAgree), in the order of ps.
func disagreeing(ps []model.Proposal) []int64 {
	var out []int64

	for _, p := range ps {
		if !pinfile.FieldsAgree(p) {
			out = append(out, p.ID)
		}
	}

	return out
}

// Result reports what a commit changed. File and ledger outcomes stay
// separate, because the two writes are not one transaction.
type Result struct {
	Action Action
	// FileWritten is true when the pin file changed.
	FileWritten bool
	// FileErr holds the pin file failure.
	FileErr error
	// Moved holds the IDs whose ledger state changed.
	Moved []int64
	// NotMoved holds the selected IDs whose ledger state did not change.
	NotMoved []int64
	// LedgerErr holds the ledger failure.
	LedgerErr error
}

// Partial reports a pin file write without a complete ledger update:
// the state that needs manual reconciliation.
func (r Result) Partial() bool {
	return r.FileWritten && (r.LedgerErr != nil || len(r.NotMoved) > 0)
}

// Commit revalidates the preview and applies it. It never retries. A
// non-nil error means a revalidation refusal and no change; once a
// write starts, the Result fields report every outcome instead.
func Commit(ls Ledgerer, root string, pv *Preview) (Result, error) {
	if pv == nil || len(pv.Prints) == 0 {
		return Result{}, ErrNothingSelected
	}

	res := Result{Action: pv.Action}

	switch pv.Action {
	case ActionApply:
		return commitApply(ls, root, pv)
	case ActionDismiss:
		if _, err := revalidate(ls, pv); err != nil {
			return res, err
		}

		moveLedger(ls, pv.ids(), model.ProposalDismissed, &res)

		return res, nil
	case ActionReconcile:
		return commitReconcile(ls, root, pv)
	default:
		return res, fmt.Errorf("unknown decision action %s", pv.Action)
	}
}

// commitApply writes the pins and then records them as applied. The
// order is the CLI order: a failed status write leaves a visible pin
// in the file diff, while the other order could mark a pin applied
// that never reached the file. The pin write goes through
// internal/pinfile, as every pin edit of the console does.
func commitApply(ls Ledgerer, root string, pv *Preview) (Result, error) {
	res := Result{Action: ActionApply}

	// A preview taken with the gate off offered the YAML block only.
	// It never described a file write, so it cannot authorize one.
	if !pv.Write {
		return res, ErrWriteDisabled
	}

	// Without a pin file observation, the stale check has no baseline.
	// PreviewApply never makes such a gate-on preview, but a caller can.
	if pv.LessonsErr != nil {
		return res, fmt.Errorf("the preview could not observe %s: %w", LessonsFile, pv.LessonsErr)
	}

	ps, err := revalidate(ls, pv)
	if err != nil {
		return res, err
	}

	// The fingerprint holds RegionSet, not the raw fields. A raw Region
	// can change without a change of RegionSet, so the check runs again.
	if odd := disagreeing(ps); len(odd) > 0 {
		return res, fmt.Errorf("%w: the region fields of %s changed after the preview: %w",
			ErrStale, idList(odd), ErrFieldsDisagree)
	}

	if err := recheckAppliedKeys(ls, ps); err != nil {
		return res, err
	}

	// The fingerprint holds the rule and the region set, so a preview of
	// PreviewApply passes this check again. The check guards a preview
	// that a caller built in another way.
	if err := sharedKeys(ps); err != nil {
		return res, err
	}

	cfg, err := distill.LoadConfig(root)
	if err != nil {
		return res, err
	}

	if !cfg.Distill.Write {
		return res, fmt.Errorf("%w: distill.write was turned off after the preview", ErrStale)
	}

	now, err := observeLessons(root)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrStale, err)
	}

	if now != pv.Lessons {
		return res, fmt.Errorf("%w: %s changed after the preview", ErrStale, LessonsFile)
	}

	// The fingerprint omits the provenance comment inputs (agent and
	// member count). Comparing the rendered block proves that the file
	// receives exactly the previewed entries. The write moves the lines
	// to the indentation of the list; pinfile proves that the move keeps
	// every value.
	block, err := distill.RenderPins(ps)
	if err != nil {
		return res, err
	}

	if block != pv.YAML {
		return res, fmt.Errorf("%w: the pin block differs from the preview", ErrStale)
	}

	pinned, err := alreadyPinned(root, ps)
	if err != nil {
		return res, err
	}

	if len(pinned) > 0 {
		return res, fmt.Errorf("%w: %s; reconcile records them without a second pin",
			ErrNeedsReconcile, idList(pinned))
	}

	// Writing starts here. From now on, every outcome goes into res and
	// the error stays nil. A stale refusal of the guarded write is the one
	// exception: that write changed nothing, so the caller previews again.
	ids := pv.ids()

	err = appendPins(root, pv.Lessons, block)

	switch {
	case errors.Is(err, pinfile.ErrStale):
		return res, fmt.Errorf("%w: %s changed after the preview", ErrStale, LessonsFile)
	case err != nil:
		res.FileErr = err
		res.NotMoved = ids
		res.FileWritten = fileChanged(root, pv.Lessons, &res)

		return res, nil
	}

	res.FileWritten = true
	moveLedger(ls, ids, model.ProposalApplied, &res)

	return res, nil
}

// readPinFile reads the pin file for appendPins. It is a variable, so a
// test can change the file between this read and the guarded write.
var readPinFile = pinfile.Read

// appendPins adds the pin block at the head of the pin list, where an
// apply always put it (pinfile.Placement zero value). It writes
// through internal/pinfile, the guarded write of Pins. That write
// refuses a link and a file that this user cannot write. It keeps the
// file mode and replaces the file by rename. The write binds to the
// previewed file state; another state returns pinfile.ErrStale and
// writes nothing.
func appendPins(root string, before FileObservation, block string) error {
	d, err := readPinFile(root)
	if err != nil {
		return err
	}

	if d.Guard.Exists != before.Exists || d.Guard.SHA256 != before.SHA256 {
		return fmt.Errorf("%w: the file changed after the preview", pinfile.ErrStale)
	}

	after, err := pinfile.PlanAppend(d, block, pinfile.Placement{})
	if err != nil {
		return err
	}

	// The write names no client, so the registry only satisfies the setup
	// engine. The built-in registry is the one that the console passes.
	p, err := pinfile.PrepareAppend(integration.Builtin(), root, d.Guard, after)
	if err != nil {
		return err
	}

	return p.Apply()
}

// fileChanged reports whether the pin file differs from the preview
// after a failed pin write. The guarded write replaces the file by
// rename, so its own failure leaves the old bytes. The check still runs,
// because the file is not verified until it is read again. When the
// file cannot be read again, it reports true and adds the read error:
// the console must never claim an untouched file it did not verify.
func fileChanged(root string, before FileObservation, res *Result) bool {
	after, err := observeLessons(root)
	if err != nil {
		res.FileErr = errors.Join(res.FileErr,
			fmt.Errorf("cannot read %s again to verify it: %w", LessonsFile, err))

		return true
	}

	return after != before
}

// commitReconcile records pending proposals as applied when their pins
// are still in the pin file, their region fields still agree, no applied
// proposal records their pin keys, and no two of them share a pin key.
// It writes only the ledger.
func commitReconcile(ls Ledgerer, root string, pv *Preview) (Result, error) {
	res := Result{Action: ActionReconcile}

	ps, err := revalidate(ls, pv)
	if err != nil {
		return res, err
	}

	// The fingerprint holds RegionSet, not the raw fields. A raw Region
	// can change without a change of RegionSet, so the check runs again.
	if odd := disagreeing(ps); len(odd) > 0 {
		return res, fmt.Errorf("%w: the region fields of %s changed after the preview: %w",
			ErrStale, idList(odd), ErrFieldsDisagree)
	}

	if err := recheckAppliedKeys(ls, ps); err != nil {
		return res, err
	}

	// A preview of PreviewReconcile passes this check again, because the
	// fingerprint holds the rule and the region set. The check guards a
	// preview that a caller built in another way, as in commitApply.
	if err := sharedKeys(ps); err != nil {
		return res, err
	}

	pinned, err := alreadyPinned(root, ps)
	if err != nil {
		return res, err
	}

	if missing := without(pv.ids(), pinned); len(missing) > 0 {
		return res, fmt.Errorf("%w: the pins of %s left %s after the preview",
			ErrStale, idList(missing), LessonsFile)
	}

	moveLedger(ls, pv.ids(), model.ProposalApplied, &res)

	return res, nil
}

// revalidate reads the previewed proposals again. Every one must still
// be pending with an identical fingerprint: a reused ID or a changed
// note or region set would make the commit act on unseen content. It
// returns the current rows in preview order.
func revalidate(ls Ledgerer, pv *Preview) ([]model.Proposal, error) {
	rows, err := ls.ProposalsByIDs(pv.ids())
	if err != nil {
		return nil, fmt.Errorf("read the selected proposals: %w", err)
	}

	byID := make(map[int64]model.Proposal, len(rows))
	for _, p := range rows {
		byID[p.ID] = p
	}

	var gone, changed []int64

	ps := make([]model.Proposal, 0, len(pv.Prints))

	for _, want := range pv.Prints {
		p, ok := byID[want.ID]

		switch {
		case !ok:
			gone = append(gone, want.ID)
		case !fingerprint(p).equal(want):
			changed = append(changed, want.ID)
		default:
			ps = append(ps, p)
		}
	}

	var reasons []string

	if len(gone) > 0 {
		reasons = append(reasons, idList(gone)+" no longer pending")
	}

	if len(changed) > 0 {
		reasons = append(reasons, idList(changed)+" changed")
	}

	if len(reasons) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrStale, strings.Join(reasons, "; "))
	}

	return ps, nil
}

// moveLedger moves each ID from pending to state `to` with its own
// statement and records the exact outcome. One statement per ID tells
// which rows moved. A batch count cannot name the rows that another
// writer decided after the revalidation read. It stops at the first
// error and never retries; the remaining IDs are reported as not moved.
func moveLedger(ls Ledgerer, ids []int64, to string, res *Result) {
	for i, id := range ids {
		n, err := ls.SetProposalStatus([]int64{id}, to)
		if err != nil {
			res.LedgerErr = fmt.Errorf("record p%d as %s: %w", id, to, err)
			res.NotMoved = append(res.NotMoved, ids[i:]...)

			return
		}

		if n == 1 {
			res.Moved = append(res.Moved, id)
		} else {
			// Another writer decided or removed the row after the
			// revalidation read.
			res.NotMoved = append(res.NotMoved, id)
		}
	}
}

// without returns the IDs of all that are not in drop, in the order of
// all.
func without(all, drop []int64) []int64 {
	var out []int64

	for _, id := range all {
		if !slices.Contains(drop, id) {
			out = append(out, id)
		}
	}

	return out
}

// idList renders IDs in the ledger's pN form, for error messages.
func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "p" + strconv.FormatInt(id, 10)
	}

	return strings.Join(parts, ", ")
}

// proseList renders two or more IDs as prose: "p1 and p2", or "p1, p2
// and p5". The refusal of a shared key reads as a sentence, so the last
// two IDs join with "and".
func proseList(ids []int64) string {
	if len(ids) < 2 {
		return idList(ids)
	}

	last := len(ids) - 1

	return idList(ids[:last]) + " and " + idList(ids[last:])
}
