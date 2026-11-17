package maintain

import (
	"errors"
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The manual scope edit. A person chooses the regions of a linked pin in
// the console. One guarded operation writes the set to the entry and to
// its applied row, so the row and the entry keep one key and the link
// holds. A hand edit cannot do this, because it changes the key of the
// entry only.
//
// PlanScope runs the authoring planner, because the checks of a typed
// draft are the checks of a pin edit. It maps the result onto Plan.
// Commit then reuses the file write, the guarded ledger transaction,
// compensation, and Recovery. So PlanScope owns no write of its own.
//
// The computed scope decides no outcome here. A manual edit works when
// the computed scope is current, absent, unknown, or refused.

// reasonScopeKept is the reason of a draft that keeps the regions. Such a
// draft writes the pin file only and records no choice, so it never
// reaches the ledger through this plan.
const reasonScopeKept = "the draft keeps the regions of this pin, so the pin review saves it"

// The coverage sentences of a scope edit of a check or enforce pin. A set
// can lose some paths and gain others, so one sentence names both.
const (
	textCoverageLose = "Paths outside the new regions lose this check."
	textCoverageGain = "Paths in the new regions gain this check."
	textCoverageBoth = "Paths outside the new regions lose this check, and paths in the new regions gain it."
	textCoverageSame = "This check covers the same paths."
)

// ScopeRequest is one manual scope edit. Edit is the draft with all its
// fields, because one save is one operation.
type ScopeRequest struct {
	Edit AuthorRequest
	// At is the time of the choice in unix seconds. PlanScope fills it
	// once. Commit plans again from the same request, so the plan stays
	// identical and the stamp never reads as a later time.
	At int64
}

// ChangesRegions reports whether the draft req asks for other regions
// than old holds. It decides between the pin review and the scope
// preview. It compares the normalized regions in order, for two reasons.
// A retyped "api/" for a stored "api" is no change, so it records no
// choice. A new order is a change, because the pin file then changes. A
// draft whose scope has a blocking issue counts as a change, so the
// preview can name that issue.
func ChangesRegions(old reviews.PinRule, req AuthorRequest) bool {
	if !req.ScopeSet {
		return false
	}

	var c draftCheck

	set := c.scope(req)
	if Blocking(c.issues) {
		return true
	}

	next := reviews.PinForRegions("", "", set).AllRegions()

	return !slices.EqualFunc(old.AllRegions(), next, sameRegion)
}

// sameRegion reports whether two regions have one region form of the
// pin key, which reads "api/" and "api" as one region.
func sameRegion(a, b string) bool {
	return reviews.NewPinKey("", a, nil) == reviews.NewPinKey("", b, nil)
}

// PlanScope computes the manual scope edit of req from the current pin
// file, ledger, and write gate. It writes nothing. It returns an error
// only for an invalid request. Every blocker and every issue goes into
// Plan.Blocked, except the gate, which Plan.Write and Plan.GateErr hold.
// So gate off never hides a collision. The plan keeps the old and new
// regions and any candidate, so a blocked preview can show them.
func PlanScope(deps Deps, req ScopeRequest) (*Plan, error) {
	if req.Edit.Op != AuthorEdit || !req.Edit.ScopeSet {
		return nil, errors.New("maintain: a scope plan needs an edit that sets the scope")
	}

	if req.At == 0 {
		req.At = deps.now().Unix()
	}

	a, err := planAuthoring(deps, req.Edit, true)
	if err != nil {
		return nil, err
	}

	return a.scopePlan(req), nil
}

// scopePlan maps the authoring result onto a maintenance plan. Request
// names the entry, because a surface reads the changed pins from it.
func (a *authoring) scopePlan(req ScopeRequest) *Plan {
	ap := a.plan
	it := a.scopeItem()

	plan := &Plan{
		Action: ActionScope, Request: Request{Action: ActionScope, Refs: []pinfile.EntryRef{req.Edit.Ref}},
		Change: a.change, Impact: ap.Impact, Scoping: &req, Write: ap.Write, GateErr: ap.GateErr,
		Blocked: scopeBlocked(ap), File: ap.Guard, Notes: ap.Notes, PlannedAt: a.deps.now(),
	}

	if a.entry != nil && !ChangesRegions(ap.Old, a.req) {
		it.Status, it.Reason = ItemNoOp, reasonScopeKept
		plan.Blocked = append(plan.Blocked, reasonScopeKept)
	}

	switch {
	case it.Status == ItemNoOp:
	case len(plan.Blocked) > 0:
		it.Reason = plan.Blocked[0]
	case ap.ProposalGuard != nil:
		it.Status = ItemReady
		plan.Retarget = []store.RegionChange{regionChange(*ap.ProposalGuard, it.To, req.At)}
	}

	plan.Items = []PlanItem{it}
	plan.Confirmable = ap.Confirmable && it.Status == ItemReady

	return plan
}

// scopeItem returns the item of the edited entry. It holds the new set
// only while the draft holds a valid one.
func (a *authoring) scopeItem() PlanItem {
	ap := a.plan
	it := PlanItem{Ref: a.req.Ref, From: ap.Old.AllRegions()}

	if a.entry != nil {
		it.Pin = Pin{Entry: *a.entry, Link: a.link}
		it.Pin.Readings.unknown(reasonNotPreview)
		it.Pin.Readings.Exposure = ExposureReading{Reason: reasonNotPreview}
		it.Rows = slices.Clone(a.link.Applied)
	}

	if a.drafted {
		it.Drafted, it.Missing = true, ap.Missing
		it.To = ap.New.AllRegions()
		it.RepoWide = len(it.To) == 0
		it.Broader = distill.Reaches(it.To, it.From)
		it.Narrower = distill.Reaches(it.From, it.To)
	}

	return it
}

// regionChange returns the guarded ledger change of a scope edit. Only
// Region, Regions, and the stamp change, because RetargetApplied writes
// no other column.
func regionChange(guard store.RowGuard, to []string, at int64) store.RegionChange {
	region := ""
	if len(to) > 0 {
		region = to[0]
	}

	return store.RegionChange{Guard: guard, Region: region, Regions: slices.Clone(to), Chosen: at}
}

// scopeBlocked maps the blockers and the blocking issues of ap to one
// line each. The gate stays out, because the modal prints its own note.
// A file blocker takes routeScopeFile, because the hand route of a pin
// edit would change the regions and unlink this pin. FileBlock stays
// empty for the same reason.
func scopeBlocked(ap *AuthorPlan) []string {
	var out []string

	for _, b := range ap.Blockers {
		switch {
		case b.Kind == BlockGate:
			continue
		case b.Kind == BlockFile, b.Kind == BlockCheck && b.Route == routeByHand:
			b.Route = routeScopeFile
		}

		out = append(out, b.String())
	}

	for _, i := range ap.Issues {
		if !i.Info {
			out = append(out, fmt.Sprintf("%s: %s", i.Field, i.Text))
		}
	}

	return out
}

// Coverage returns the sentence that says how a scope edit of a check or
// enforce pin moves its check. A set can be broader and narrower at once,
// for example api to web, so that case names both moves.
func (it PlanItem) Coverage() string {
	switch {
	case it.Broader && it.Narrower:
		return textCoverageBoth
	case it.Narrower:
		return textCoverageLose
	case it.Broader:
		return textCoverageGain
	}

	return textCoverageSame
}
