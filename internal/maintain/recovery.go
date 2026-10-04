package maintain

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The texts of a recovery plan.
const (
	noteRecovery       = "completes the ledger update of the earlier operation; the pin file stays as it is"
	reasonUnproven     = "the reviewed result is no longer proven; reconcile .seamark/lessons.yaml and the ledger by hand"
	reasonFileUnread   = "the pin file cannot be read, so the reviewed result cannot be proven"
	reasonTargetGone   = "the retargeted pin is not in the file with the reviewed content"
	reasonOldKeyBack   = "a pin with the earlier regions is in the file again"
	reasonPrunedBack   = "a pin with the pruned key is in the file again"
	reasonKeyTaken     = "another applied proposal now records the target key"
	reasonRowChanged   = "the proposal row changed since the operation"
	reasonRecoveryRows = "the planned ledger rows changed since the operation"
)

// PlanRecovery plans the ledger-only completion of a partial operation.
// It is confirmable only while a fresh observation proves the reviewed
// result: the pin file reads without error; for a retarget, every target
// entry is present with exactly the reviewed content, every earlier key
// is absent, and no other applied row records a target key; for a prune,
// no pruned key is present; and every ledger row still holds its planned
// values. It writes nothing.
func PlanRecovery(deps Deps, r *Recovery) (*Plan, error) {
	if r == nil {
		return nil, errors.New("maintain: no recovery")
	}

	if deps.Root == "" {
		return nil, errors.New("maintain: workspace root is empty")
	}

	if deps.Reader == nil {
		return nil, fmt.Errorf("%w: no readable index", ErrNoLedger)
	}

	doc, err := pinfile.Read(deps.Root)
	if err != nil {
		return nil, err
	}

	p := &Plan{
		Action: r.Action, Request: r.Request, Items: slices.Clone(r.Items), File: doc.Guard,
		Retarget: slices.Clone(r.Retarget), Supersede: slices.Clone(r.Supersede),
		PlannedAt: deps.now(), Recovery: r, Notes: []string{noteRecovery},
	}
	p.Write, p.GateErr = readGate(deps.Root)

	reasons := fileProof(doc, r)

	keys, err := keyProof(deps.Reader, r)
	if err != nil {
		return nil, err
	}

	rows, err := rowProof(deps.Reader, p)
	if err != nil {
		return nil, err
	}

	for i := range p.Items {
		p.Items[i].Status, p.Items[i].Reason = ItemReady, ""

		if why := slices.Concat(reasons[i], keys[i], rows[i]); len(why) > 0 {
			p.Items[i].Status, p.Items[i].Reason = ItemBlocked, strings.Join(why, "; ")
		}
	}

	switch {
	case p.GateErr != nil:
		p.Blocked = append(p.Blocked, "distill.write cannot be read; fix .seamark/config.yaml first")
	case !p.Write:
		p.Blocked = append(p.Blocked, "distill.write is off: turn on Settings › Pin writing and preview again")
	}

	if slices.ContainsFunc(p.Items, func(it PlanItem) bool { return it.Status != ItemReady }) {
		p.Blocked = append(p.Blocked, reasonUnproven)
	}

	p.Confirmable = len(p.Blocked) == 0 && len(p.Items) == len(r.Edits) && len(p.Items) > 0

	return p, nil
}

// fileProof checks the pin file against the reviewed result. It returns
// the failed checks per item, in the order of the edits.
func fileProof(doc *pinfile.Document, r *Recovery) [][]string {
	reasons := make([][]string, len(r.Items))

	if len(r.Edits) != len(r.Items) || len(r.NewKeys) != len(r.Edits) {
		for i := range reasons {
			reasons[i] = []string{reasonUnproven}
		}

		return reasons
	}

	// An unreliable listing proves no absence: an alias pin value can
	// deliver a pruned pin that no listed entry shows.
	if doc.Unreadable() {
		for i := range reasons {
			reasons[i] = []string{reasonFileUnread}
		}

		return reasons
	}

	after := pinfile.Parse(r.After)

	for i, e := range r.Edits {
		if hasKey(doc, e.Ref.Key) {
			reason := reasonOldKeyBack
			if e.Delete {
				reason = reasonPrunedBack
			}

			reasons[i] = append(reasons[i], reason)
		}

		if e.Delete {
			continue
		}

		want := withKey(after, r.NewKeys[i])
		got := withKey(doc, r.NewKeys[i])

		if len(want) != 1 || len(got) != 1 || want[0].Raw != got[0].Raw {
			reasons[i] = append(reasons[i], reasonTargetGone)
		}
	}

	return reasons
}

// keyProof checks that no applied row outside the recovery records a
// retarget key. A row can become applied after the partial operation,
// for example a pending twin of the written pin. A completion would then
// give two applied proposals one key. It uses the plan-time rule of
// appliedKeys and returns the failed checks per item.
func keyProof(reader *store.Store, r *Recovery) ([][]string, error) {
	reasons := make([][]string, len(r.Items))

	// fileProof already refuses a proof whose keys and items disagree.
	if r.Action != ActionRetarget || len(r.NewKeys) != len(r.Items) {
		return reasons, nil
	}

	rows, err := readLedger(reader)
	if err != nil {
		return nil, err
	}

	selected := map[int64]bool{}
	for _, c := range r.Retarget {
		selected[c.Guard.ID] = true
	}

	taken := appliedKeys(rows.applied, selected)

	for i, key := range r.NewKeys {
		if slices.Contains(taken, key) {
			reasons[i] = append(reasons[i], reasonKeyTaken)
		}
	}

	return reasons, nil
}

// rowProof reads the planned rows again and checks that each still holds
// its planned values. It returns the failed checks per item.
func rowProof(reader *store.Store, p *Plan) ([][]string, error) {
	reasons := make([][]string, len(p.Items))
	pairs := guardPairs(p)

	if len(pairs) != len(p.Items) {
		for i := range reasons {
			reasons[i] = []string{reasonRecoveryRows}
		}

		return reasons, nil
	}

	observed, err := reader.ProposalsWithIDs(plannedIDs(p))
	if err != nil {
		return nil, fmt.Errorf("maintain: read the planned rows: %w", err)
	}

	for i, g := range pairs {
		k := slices.IndexFunc(observed, func(row model.Proposal) bool { return row.ID == g[0].ID })

		if k < 0 || !g[0].Matches(observed[k]) {
			reasons[i] = append(reasons[i], fmt.Sprintf("p%d: %s", g[0].ID, reasonRowChanged))
		}
	}

	return reasons, nil
}

// hasKey reports whether a live entry has key.
func hasKey(d *pinfile.Document, key reviews.PinKey) bool {
	return len(withKey(d, key)) > 0
}

// withKey returns the live entries with key.
func withKey(d *pinfile.Document, key reviews.PinKey) []pinfile.Entry {
	var out []pinfile.Entry

	for _, e := range d.Entries {
		if e.Key == key {
			out = append(out, e)
		}
	}

	return out
}

// CommitRecovery completes the ledger update of a recovery plan. It
// plans the recovery again and refuses with ErrStale unless the proof
// still holds and the observations are identical. It never writes the
// pin file. A failed ledger write keeps the recovery available.
func CommitRecovery(deps Deps, p *Plan) (Result, error) {
	if p == nil || p.Recovery == nil {
		return Result{}, errors.New("maintain: not a recovery plan")
	}

	res := Result{Action: p.Action}

	if err := confirmable(p); err != nil {
		return res, err
	}

	ledger := deps.ledger()
	if ledger == nil {
		return res, fmt.Errorf("%w: no ledger writer", ErrNoLedger)
	}

	q, err := PlanRecovery(deps, p.Recovery)
	if err != nil {
		return res, err
	}

	if reasons := recoveryDifferences(p, q); len(reasons) > 0 {
		return res, fmt.Errorf("%w: %s", ErrStale, strings.Join(reasons, "; "))
	}

	if err := writeLedger(ledger, q); err != nil {
		res.LedgerErr = err
		res.Recovery = p.Recovery
		res.Note = noteRecoverFailed
		res.Observed, res.ObservedErr = ledger.ProposalsWithIDs(plannedIDs(q))

		return res, nil
	}

	res.LedgerDone = true
	res.Moved = plannedIDs(q)
	res.Note = noteRecovered

	return res, nil
}

// recoveryDifferences names what changed between a reviewed recovery
// plan and the plan from the current observations.
func recoveryDifferences(p, q *Plan) []string {
	var reasons []string

	if p.File.Exists != q.File.Exists || p.File.SHA256 != q.File.SHA256 {
		reasons = append(reasons, "the pin file changed")
	}

	if p.Write != q.Write || (p.GateErr == nil) != (q.GateErr == nil) {
		reasons = append(reasons, "distill.write changed")
	}

	if !q.Confirmable {
		reasons = append(reasons, reasonUnproven)
	}

	if !reflect.DeepEqual(p.Retarget, q.Retarget) || !reflect.DeepEqual(p.Supersede, q.Supersede) {
		reasons = append(reasons, reasonRecoveryRows)
	}

	return reasons
}
