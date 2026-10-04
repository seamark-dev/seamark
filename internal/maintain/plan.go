package maintain

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ItemStatus is the state of one selected entry in a plan. The zero
// value is ItemBlocked, so an unset item never reads as ready.
type ItemStatus int

// The item states.
const (
	// ItemBlocked means that the entry cannot change; Reason says why.
	ItemBlocked ItemStatus = iota
	// ItemNoOp means that the change would leave the entry as it is.
	ItemNoOp
	// ItemReady means that the candidate file holds the change.
	ItemReady
)

// String returns the lower-case name of the state.
func (s ItemStatus) String() string {
	switch s {
	case ItemBlocked:
		return "blocked"
	case ItemNoOp:
		return "no-op"
	case ItemReady:
		return "ready"
	default:
		return "ItemStatus(" + strconv.Itoa(int(s)) + ")"
	}
}

// PlanItem is the plan of one selected entry.
type PlanItem struct {
	// Ref is the reviewed binding of the entry.
	Ref pinfile.EntryRef
	// Pin is the entry as the plan read it, with its link. A plan fills
	// only the scope and trigger readings; the other readings stay
	// unknown. Pin is zero when the entry changed since the review.
	Pin    Pin
	Status ItemStatus
	// Reason explains a blocked or no-op item. For a ready prune it says
	// whether a retained alternative is established. It can quote a
	// pinfile error with a rule or a path from the file, so a caller
	// sanitizes it before display.
	Reason string
	// From is the live region set; To is the computed set of a retarget.
	// Nil is repository-wide. To is meaningful only for a ready or a
	// no-op retarget. Both hold paths from the file or the model, so a
	// caller sanitizes them before display.
	From, To []string
	// Broader and RepoWide label a retarget that widens the scope.
	Broader, RepoWide bool
	// Facts explain what supports the computed scope. They hold file and
	// model data, so a caller sanitizes them before display.
	Facts []string
	// Kept lists the similar pins that stay in the file and stay linked
	// after a prune. Similar wording does not prove the same coverage.
	// Their entries hold file text, so a caller sanitizes it.
	Kept []Pin
	// Rows are the applied proposals linked to the entry. Their rule and
	// note come from the ledger, so a caller sanitizes them.
	Rows []model.Proposal
}

// Plan is a reviewed maintenance operation. It is complete even when it
// cannot be confirmed, so a preview can show the exact change.
type Plan struct {
	Action  Action
	Request Request
	Items   []PlanItem
	// Change is the verified candidate file of the ready items; nil when
	// no item is ready.
	Change *pinfile.Change
	// Retarget and Supersede are the guarded ledger changes of the ready
	// items, in item order.
	Retarget  []store.RegionChange
	Supersede []store.RowGuard
	// Write is distill.write at plan time; GateErr is its read failure.
	Write   bool
	GateErr error
	// FileBlock is the reason the pin file cannot be edited or replaced.
	// It can name a path, so a caller sanitizes it before display.
	FileBlock string
	// Confirmable is true when every item is ready, the gate is on, and
	// the file can be replaced. Only such a plan can be committed.
	Confirmable bool
	// Blocked lists the reasons that the plan cannot be confirmed. One
	// reason can quote FileBlock, so a caller sanitizes them.
	Blocked []string
	// Evidence binds a retarget to its inputs: the living findings, the
	// trigger fields, the computed regions, and the index run.
	Evidence [32]byte
	// File is the observation of the pin file that the plan read.
	File integration.FileGuard
	// Notes are plain statements for the preview.
	Notes     []string
	PlannedAt time.Time
	// Recovery is set on a plan from PlanRecovery. Such a plan completes
	// the ledger only and commits through CommitRecovery.
	Recovery *Recovery
}

// The fixed parts of the item reasons, so a test and a screen can rely
// on them. A reason can also quote a pinfile error, and that text holds
// file or ledger data.
const (
	reasonChanged     = "the pin changed since the list was read; refresh the Pins view"
	reasonNoEvidence  = "cannot recompute: every cited finding aged out of the index; the scope stays as it is"
	reasonCurrent     = "regions already current"
	reasonRetirement  = "no retained equivalent is established; this is a deliberate retirement"
	reasonLedgerFixes = "resolve it in .seamark/lessons.yaml or in the ledger first"
)

// reasonNotPreview is the reason of every reading that a plan does not
// compute. A preview reads only the scope and the trigger state.
const reasonNotPreview = "not read for a preview"

// NewPlan computes the operation of req from the current pin file,
// ledger, write gate, and evidence. It writes nothing. Every selected
// entry gets an item: ready, no-op, or blocked with a reason. A plan is
// confirmable only when every item is ready, so it never acts on a
// different subset.
func NewPlan(deps Deps, req Request) (*Plan, error) {
	if err := checkRequest(req); err != nil {
		return nil, err
	}

	if deps.Root == "" {
		return nil, errors.New("maintain: workspace root is empty")
	}

	doc, err := pinfile.Read(deps.Root)
	if err != nil {
		return nil, err
	}

	rows, err := readLedger(deps.Reader)
	if err != nil {
		return nil, err
	}

	links := pinfile.Classify(doc, rows.applied, rows.pending, rows.superseded)

	pl := &planner{deps: deps, doc: doc, rows: rows, pins: make([]Pin, len(doc.Entries))}
	for i := range pl.pins {
		pl.pins[i] = Pin{Entry: doc.Entries[i], Link: links.Entries[i]}
		pl.pins[i].Readings.unknown(reasonNotPreview)
		pl.pins[i].Readings.Exposure = ExposureReading{Reason: reasonNotPreview}
	}

	pl.plan = &Plan{
		Action: req.Action, Request: req, File: doc.Guard, FileBlock: doc.FileBlock, PlannedAt: deps.now(),
	}
	pl.plan.Write, pl.plan.GateErr = readGate(deps.Root)

	if err := pl.items(); err != nil {
		return nil, err
	}

	if err := pl.edit(); err != nil {
		return nil, err
	}

	pl.ledger()
	pl.kept()
	pl.finish()

	return pl.plan, nil
}

// checkRequest refuses a request that names no action or no entry. An
// empty selection is never read as every pin.
func checkRequest(req Request) error {
	switch {
	case req.Action != ActionRetarget && req.Action != ActionPrune:
		return fmt.Errorf("maintain: unknown action %s", req.Action)
	case len(req.Refs) == 0:
		return ErrNothingSelected
	case req.Cursor && len(req.Refs) != 1:
		return errors.New("maintain: a cursor request names exactly one entry")
	}

	return nil
}

// readGate reads distill.write strictly: a config that cannot be read
// blocks the plan and never reads as a default.
func readGate(root string) (bool, error) {
	cfg, err := distill.LoadConfig(root)
	if err != nil {
		return false, err
	}

	return cfg.Distill.Write, nil
}

// planner holds the state of one NewPlan call.
type planner struct {
	deps Deps
	doc  *pinfile.Document
	rows ledgerRows
	pins []Pin
	plan *Plan

	// at holds the index in pins of each item, or -1.
	at []int
	// edits and edited pair each requested file edit with its item.
	edits  []pinfile.Edit
	edited []int
	// evidence holds the digest records of the retarget items.
	evidence []evidenceRecord
}

// evidenceRecord is the input of one scope advice. The plan digest binds
// these values, so a commit refuses when the advice could differ.
type evidenceRecord struct {
	Index    int
	ID       int64
	Living   []int64
	Triggers []string
	Checked  int64
	Version  int
	Kind     distill.ScopeKind
	Regions  []string
	Facts    []distill.TriggerFact
}

// items binds each reference and applies the checks that need no file
// splice. It collects the file edits of the entries that pass.
func (pl *planner) items() error {
	var meta map[int64]model.Finding

	if pl.plan.Action == ActionRetarget && pl.deps.Probe.State == workspace.IndexReady {
		var rows []model.Proposal

		for _, p := range pl.pins {
			if p.Link.Link == pinfile.LinkLinked && slices.Contains(pl.plan.Request.Refs, p.Entry.Ref()) {
				rows = append(rows, p.Link.Applied[0])
			}
		}

		var err error
		if meta, err = pl.deps.Reader.FindingsMetaByIDs(members(rows)); err != nil {
			return fmt.Errorf("maintain: read the cited findings: %w", err)
		}
	}

	pl.plan.Items = make([]PlanItem, len(pl.plan.Request.Refs))
	pl.at = make([]int, len(pl.plan.Request.Refs))

	for n, ref := range pl.plan.Request.Refs {
		it := &pl.plan.Items[n]
		it.Ref = ref
		pl.at[n] = slices.IndexFunc(pl.doc.Entries, func(e pinfile.Entry) bool { return e.Index == ref.Index })

		if pl.at[n] >= 0 && pl.doc.Entries[pl.at[n]].Ref() != ref {
			pl.at[n] = -1
		}

		if pl.at[n] < 0 {
			it.Reason = reasonChanged

			continue
		}

		it.Pin = pl.pins[pl.at[n]]
		it.From = pinRule(it.Pin.Entry).AllRegions()

		if reason := pl.blocked(it.Pin); reason != "" {
			it.Reason = reason

			continue
		}

		row := it.Pin.Link.Applied[0]
		it.Rows = slices.Clone(it.Pin.Link.Applied)

		open, state := distill.TriggerQuestionOpen(row)
		it.Pin.Readings.Trigger = TriggerReading{Known: true, Open: open, State: state}

		if pl.plan.Action == ActionPrune {
			pl.add(n, pinfile.Edit{Ref: ref, Delete: true, Provenance: []int64{row.ID}})

			continue
		}

		if err := pl.scope(n, row, meta); err != nil {
			return err
		}
	}

	return nil
}

// blocked returns the reason that an entry cannot be maintained, or an
// empty string. Only one live entry linked to one applied row with
// agreeing region fields can change, and only when its layout is safe.
func (pl *planner) blocked(p Pin) string {
	link := p.Link.Link

	switch {
	case pl.doc.Unreadable():
		reason := link.Reason() + "; fix .seamark/lessons.yaml first"
		if pl.doc.ErrLine > 0 {
			reason = fmt.Sprintf("%s; fix .seamark/lessons.yaml first (line %d)", link.Reason(), pl.doc.ErrLine)
		}

		return reason
	case link == pinfile.LinkNoProposal:
		return link.Reason() + "; the console changes only proposal-linked pins (edit .seamark/lessons.yaml by hand)"
	case link == pinfile.LinkPendingMatch:
		return link.Reason() + " (Proposals › c)"
	case link != pinfile.LinkLinked:
		return link.Reason() + "; " + reasonLedgerFixes
	case p.Entry.Block != "":
		return "cannot edit this entry safely: " + p.Entry.Block + "; edit .seamark/lessons.yaml by hand"
	}

	return ""
}

// scope computes the advice of one retarget item and adds its edit when
// the advice is a change.
func (pl *planner) scope(n int, row model.Proposal, meta map[int64]model.Finding) error {
	it := &pl.plan.Items[n]

	if pl.deps.Probe.State != workspace.IndexReady {
		it.Reason = noIndexReason(pl.deps.Probe)

		return nil
	}

	living := livingOf(row, meta)

	advice, err := distill.RecommendScope(pl.deps.Reader, pl.deps.Root, row, living)
	if err != nil {
		return fmt.Errorf("maintain: compute the scope of p%d: %w", row.ID, err)
	}

	pl.evidence = append(pl.evidence, evidenceRecord{
		Index: it.Ref.Index, ID: row.ID, Living: findingIDs(living), Triggers: row.TriggerPaths,
		Checked: row.TriggerChecked, Version: row.TriggerPromptVersion,
		Kind: advice.Kind, Regions: advice.Regions, Facts: advice.Facts,
	})

	it.Pin.Readings.Scope = ScopeReading{Known: true, Advice: advice}

	for _, f := range advice.Facts {
		if f.BlockedLine() != "" {
			it.Pin.Readings.Trigger.Blocked = append(it.Pin.Readings.Trigger.Blocked, f)
		}
	}

	switch advice.Kind {
	case distill.ScopeNoEvidence:
		it.Reason = reasonNoEvidence
	case distill.ScopeCurrent:
		it.Status, it.Reason, it.To = ItemNoOp, reasonCurrent, advice.Regions
	default:
		it.To, it.Broader, it.RepoWide = advice.Regions, advice.Broader, advice.RepoWide
		it.Facts = factLines(advice, len(living))
		pl.add(n, pinfile.Edit{Ref: it.Ref, Regions: advice.Regions})
	}

	return nil
}

// add records a file edit for item n.
func (pl *planner) add(n int, e pinfile.Edit) {
	pl.edits = append(pl.edits, e)
	pl.edited = append(pl.edited, n)
}

// edit plans the file edits. An edit that the pin file planner refuses
// blocks its item, or makes it a no-op; the others become ready.
func (pl *planner) edit() error {
	if len(pl.edits) == 0 {
		return nil
	}

	change, err := pinfile.Plan(pl.doc, pl.edits, pl.otherApplied())

	refused := pinfile.EditErrors(err)
	if err != nil && len(refused) == 0 {
		return fmt.Errorf("maintain: plan the pin file: %w", err)
	}

	for k, n := range pl.edited {
		it := &pl.plan.Items[n]
		it.Status = ItemReady

		for _, ee := range refused {
			if ee.Ref != pl.edits[k].Ref {
				continue
			}

			it.Status, it.Reason = ItemBlocked, ee.Error()
			if errors.Is(ee, pinfile.ErrNoOp) {
				it.Status = ItemNoOp
			}
		}
	}

	pl.plan.Change = change

	return nil
}

// otherApplied returns the keys of the applied rows that no selected
// entry links (see appliedKeys).
func (pl *planner) otherApplied() []reviews.PinKey {
	selected := map[int64]bool{}

	for _, it := range pl.plan.Items {
		for _, p := range it.Rows {
			selected[p.ID] = true
		}
	}

	return appliedKeys(pl.rows.applied, selected)
}

// ledger builds the guarded ledger changes of the ready items and the
// evidence digest of a retarget.
func (pl *planner) ledger() {
	for _, it := range pl.plan.Items {
		if it.Status != ItemReady {
			continue
		}

		guard := store.NewRowGuard(it.Rows[0])

		if pl.plan.Action == ActionPrune {
			pl.plan.Supersede = append(pl.plan.Supersede, guard)

			continue
		}

		region := ""
		if len(it.To) > 0 {
			region = it.To[0]
		}

		pl.plan.Retarget = append(pl.plan.Retarget, store.RegionChange{
			Guard: guard, Region: region, Regions: slices.Clone(it.To),
		})
	}

	if pl.plan.Action == ActionRetarget {
		pl.plan.Evidence = evidenceDigest(pl.evidence, pl.deps.Probe)
	}
}

// kept lists the retained alternatives of each ready prune: the cluster
// partners that stay in the candidate file and stay linked.
func (pl *planner) kept() {
	if pl.plan.Action != ActionPrune {
		return
	}

	clusters(pl.pins)

	for n := range pl.plan.Items {
		it := &pl.plan.Items[n]
		if it.Status != ItemReady {
			continue
		}

		for _, j := range pl.pins[pl.at[n]].Readings.Similar {
			if !slices.Contains(pl.at, j) {
				it.Kept = append(it.Kept, pl.pins[j])
			}
		}

		it.Reason = reasonRetirement
		if len(it.Kept) > 0 {
			it.Reason = fmt.Sprintf("similar wording stays in %d linked pin(s); similar wording does not prove "+
				"the same coverage", len(it.Kept))
		}
	}
}

// finish decides whether the plan can be confirmed and writes the notes.
func (pl *planner) finish() {
	p := pl.plan

	switch {
	case p.GateErr != nil:
		p.Blocked = append(p.Blocked, "distill.write cannot be read; fix .seamark/config.yaml first")
	case !p.Write:
		p.Blocked = append(p.Blocked, "distill.write is off: this preview changes nothing; turn on "+
			"Settings › Pin writing and preview again")
	}

	if p.FileBlock != "" {
		p.Blocked = append(p.Blocked, "the pin file cannot be changed: "+p.FileBlock)
	}

	ready := 0

	for _, it := range p.Items {
		if it.Status == ItemReady {
			ready++
		}
	}

	if ready < len(p.Items) {
		p.Blocked = append(p.Blocked, fmt.Sprintf("%d of %d selected pin(s) cannot change; revise the selection",
			len(p.Items)-ready, len(p.Items)))
	}

	p.Confirmable = len(p.Blocked) == 0 && p.Change != nil && len(p.Change.Edits) == len(p.Items)
	p.Notes = pl.notes()
}

// notes returns the plain statements of the preview.
func (pl *planner) notes() []string {
	var notes []string

	if pl.plan.Request.Cursor {
		notes = append(notes, "acts on the cursor row; no row is marked")
	}

	switch pl.plan.Action {
	case ActionRetarget:
		notes = append(notes,
			"only the region keys of each entry change; the rule, the note, the comments, and the position stay",
			"the proposal keeps its recorded note; only its stored regions change",
			"delivery history restarts under the new pin identity; the outcome reads untested until the pin fires again")

		if at := pl.deps.Probe.IndexedAt; at > 0 {
			notes = append(notes, "scope advice reads the index built "+
				time.Unix(at, 0).UTC().Format("2006-01-02 15:04 UTC"))
		}
	case ActionPrune:
		notes = append(notes,
			"the linked proposal moves from applied to superseded; findings, signatures, and the firing log stay",
			"a superseded proposal is history; the console does not restore it")
	}

	return append(notes, "the diff hides text that looks like a secret; the file gets the exact bytes")
}

// evidenceDigest hashes the inputs of every scope advice of a plan and
// the index run that the advice read.
func evidenceDigest(records []evidenceRecord, probe workspace.IndexProbe) [32]byte {
	data, err := json.Marshal(struct {
		Records   []evidenceRecord
		State     string
		IndexedAt int64
	}{records, probe.State.String(), probe.IndexedAt})
	if err != nil {
		// The records hold only strings, numbers, and booleans, so the
		// encoder cannot fail. A panic keeps a broken digest visible.
		panic("maintain: encode the evidence digest: " + err.Error())
	}

	return sha256.Sum256(data)
}

// findingIDs returns the IDs of findings in ascending order.
func findingIDs(fs []model.Finding) []int64 {
	ids := make([]int64, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}

	slices.Sort(ids)

	return ids
}

// factLines explains what supports a computed scope: each stored trigger
// path and its state, then the evidence coverage when no trigger sets
// the scope.
func factLines(advice distill.ScopeAdvice, living int) []string {
	var (
		lines    []string
		selected bool
	)

	for _, f := range advice.Facts {
		switch {
		case f.Selected && f.Direct:
			selected = true
			lines = append(lines, fmt.Sprintf("trigger %s is cited evidence; it delivers to %s", f.Path, f.Region))
		case f.Selected:
			selected = true
			lines = append(lines, fmt.Sprintf("trigger %s changed together with the evidence in %d commit(s); "+
				"it delivers to %s", f.Path, f.Together, f.Region))
		case f.BlockedLine() != "":
			lines = append(lines, f.BlockedLine())
		default:
			lines = append(lines, fmt.Sprintf("trigger %s is not confirmed by the evidence or the history; "+
				"it does not set the scope", f.Path))
		}
	}

	if !selected {
		line := fmt.Sprintf("no confirmed trigger: the scope is the coverage of %d living finding(s)", living)
		if advice.RepoWide {
			line += "; they are too scattered for a region, so the scope is repository-wide"
		}

		lines = append(lines, line)
	}

	return lines
}
