package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The fixture of the maintenance preview tests and captures. The pin
// file is real text, and the entries, links and candidate files come
// from the real pinfile reader and planner. So the previews show the
// diffs and line numbers that the engine would compute; only the scope
// advice and the item states are scripted.

// maintFixtureYAML is the pin file of the fixture: four linked pins,
// one of them with a hand-edited note, and one pin without a proposal.
const maintFixtureYAML = `# Lessons pinned for this repository.
pin:
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p4)
  - rule: wrap-sql-errors
    region: internal/store
    note: Wrap SQL errors with the query name.
  # distilled by claude/v2 from 3 findings (seamark lessons --distill, p12)
  - rule: close-db-handles
    regions: [internal/api, internal/db]
    note: Close database handles in the same function.
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p16)
  - rule: close-db-handle-on-error
    region: internal/db
    note: Close the handle on every error path, also when the query fails.
    owner: team-db
  # distilled by claude/v2 from 1 finding (seamark lessons --distill, p9)
  - rule: check-nil-errors
    region: internal/api
    note: Check every error that the store returns before you use the value, and wrap it with the call site.
  - rule: keep-changelog-current
    note: Add a changelog line to every user-visible change.
`

// The fixture entries by position in the pin file.
const (
	maintWrap = iota
	maintHandles
	maintOnError
	maintNilErrors
	maintChangelog
)

// maintRows are the applied proposals of the fixture. p9 records the
// note as distilled; the file holds a longer, hand-edited note. Each row
// cites two findings: 110+entry is living (maintFix.inspection), and
// 200+entry aged out. So the citation count matches the facts of the
// readings, "1/2 citations living", as the engines give them.
func maintRows() []domain.Proposal {
	created := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Unix()
	row := func(id int64, entry int, rule, region string, regions []string, note string) domain.Proposal {
		return domain.Proposal{
			ID: id, Signature: "sig-" + rule, Rule: rule, Region: region, Regions: regions, Note: note,
			Agent: "claude/v2", Status: domain.ProposalApplied, CreatedAt: created,
			Members: []int64{int64(110 + entry), int64(200 + entry)},
		}
	}

	return []domain.Proposal{
		row(4, maintWrap, "wrap-sql-errors", "internal/store", nil, "Wrap SQL errors with the query name."),
		row(12, maintHandles, "close-db-handles", "internal/api", []string{"internal/api", "internal/db"},
			"Close database handles in the same function."),
		row(16, maintOnError, "close-db-handle-on-error", "internal/db", nil,
			"Close the handle on every error path, also when the query fails."),
		row(9, maintNilErrors, "check-nil-errors", "internal/api", nil, "Check every error returned by the store."),
	}
}

// maintFix is the parsed fixture: the document, its links, and one Pin
// per entry, as the Pins inspection holds them.
type maintFix struct {
	doc   *pinfile.Document
	links *pinfile.Links
	pins  []maintain.Pin
}

// maintFixture parses the fixture pin file and links it to the rows.
func maintFixture(t *testing.T) maintFix {
	t.Helper()

	doc := pinfile.Parse([]byte(maintFixtureYAML))
	require.False(t, doc.Unreadable(), "the fixture pin file reads")
	require.Len(t, doc.Entries, 5)

	links := pinfile.Classify(doc, maintRows(), nil, nil)
	pins := make([]maintain.Pin, len(doc.Entries))

	for i, e := range doc.Entries {
		pins[i] = maintain.Pin{Entry: e, Link: links.Entries[i]}
	}

	require.Equal(t, pinfile.LinkLinked, pins[maintWrap].Link.Link)
	require.True(t, pins[maintNilErrors].Link.NoteChanged, "p9 has an edited live note")
	require.Equal(t, pinfile.LinkNoProposal, pins[maintChangelog].Link.Link)

	return maintFix{doc: doc, links: links, pins: pins}
}

// inspection returns the Pins snapshot of the fixture, with the readings
// that an indexed database computes for the linked pins. Their scope
// agrees with the fixture plans. So Pins offers w for the pins that a
// plan retargets, and a capture shows the same scope in the Pins detail
// and in the preview.
func (fx maintFix) inspection() *maintain.Inspection {
	// The clone keeps the readings out of fx.pins, because a plan item
	// carries only the readings that the plan computes.
	pins := slices.Clone(fx.pins)

	for _, r := range []struct {
		entry  int
		path   string
		advice distill.ScopeAdvice
	}{
		{maintWrap, "internal/store/sql/query.go", distill.ScopeAdvice{
			Kind: distill.ScopeChanged, Regions: []string{"internal/store/sql"}, Narrower: true,
		}},
		{maintHandles, "internal/api/users.go", distill.ScopeAdvice{
			Kind: distill.ScopeChanged, Regions: []string{"internal"}, Broader: true,
		}},
		{maintOnError, "internal/db/conn.go", distill.ScopeAdvice{
			Kind: distill.ScopeCurrent, Regions: []string{"internal/db"},
		}},
		{maintNilErrors, "internal/api/handlers/user.go", distill.ScopeAdvice{
			Kind: distill.ScopeChanged, Regions: []string{"internal/api/handlers"}, Narrower: true,
		}},
	} {
		living := []domain.Finding{pinsFinding(int64(110+r.entry), 40+r.entry, r.path)}

		pins[r.entry].Readings = pinsKnown(confidence.TierFair, living)
		pins[r.entry].Readings.Scope.Advice = r.advice
	}

	return &maintain.Inspection{
		Doc: fx.doc, Links: fx.links, Pins: pins,
		Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
		At:    time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
	}
}

// maintSpec scripts one selected entry of a fixture plan.
type maintSpec struct {
	entry  int
	status maintain.ItemStatus
	reason string
	// to is the computed region set of a retarget; nil is
	// repository-wide.
	to                          []string
	broader, repoWide, narrower bool
	facts                       []string
	// kept are the entries that stay as retained alternatives of a
	// prune.
	kept []int
}

// maintPlanOpts are the plan-wide facts of a fixture plan.
type maintPlanOpts struct {
	write   bool
	gateErr error
	block   string
	cursor  bool
}

// plan builds the plan that maintain.NewPlan would return for specs.
// The candidate file comes from the real pinfile planner, so the diff
// and its counts are the engine's.
func (fx maintFix) plan(t *testing.T, a maintain.Action, o maintPlanOpts, specs ...maintSpec) *maintain.Plan {
	t.Helper()

	p := &maintain.Plan{
		Action: a, Request: maintain.Request{Action: a, Cursor: o.cursor}, Write: o.write, GateErr: o.gateErr,
		FileBlock: o.block, PlannedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
	}

	var edits []pinfile.Edit

	for _, s := range specs {
		pin := fx.pins[s.entry]
		pin.Readings.Scope.Advice.Narrower = s.narrower
		ref := pin.Entry.Ref()

		it := maintain.PlanItem{
			Ref: ref, Pin: pin, Status: s.status, Reason: s.reason, From: maintEntryRegions(pin.Entry),
			Broader: s.broader, RepoWide: s.repoWide, Facts: s.facts,
		}

		if s.status != maintain.ItemBlocked {
			it.Rows = slices.Clone(pin.Link.Applied)
		}

		if a == maintain.ActionRetarget && s.status != maintain.ItemBlocked {
			it.To = s.to
		}

		for _, k := range s.kept {
			it.Kept = append(it.Kept, fx.pins[k])
		}

		p.Request.Refs = append(p.Request.Refs, ref)
		p.Items = append(p.Items, it)

		if s.status != maintain.ItemReady {
			continue
		}

		row := it.Rows[0]

		if a == maintain.ActionPrune {
			edits = append(edits, pinfile.Edit{Ref: ref, Delete: true, Provenance: []int64{row.ID}})
			p.Supersede = append(p.Supersede, store.NewRowGuard(row))

			continue
		}

		edits = append(edits, pinfile.Edit{Ref: ref, Regions: s.to})

		region := ""
		if len(s.to) > 0 {
			region = s.to[0]
		}

		p.Retarget = append(p.Retarget, store.RegionChange{
			Guard: store.NewRowGuard(row), Region: region, Regions: slices.Clone(s.to),
		})
	}

	if len(edits) > 0 {
		change, err := pinfile.Plan(fx.doc, edits, nil)
		require.NoError(t, err)

		p.Change = change
	}

	p.Confirmable = o.write && o.gateErr == nil && o.block == "" && maintNotReady(p.Items) == 0 && p.Change != nil

	return p
}

// recovery returns the session proof that a partial commit of p offers.
func (fx maintFix) recovery(p *maintain.Plan) *maintain.Recovery {
	return &maintain.Recovery{
		Action: p.Action, Request: p.Request, Items: slices.Clone(p.Items),
		Before: p.Change.Before, After: p.Change.After, Edits: p.Change.Edits, NewKeys: p.Change.NewKeys,
		Retarget: p.Retarget, Supersede: p.Supersede,
	}
}

// recoveryPlan returns the plan that maintain.PlanRecovery would return
// for r: every item proven when proven is set, else the first item
// blocked with the engine reason.
func (fx maintFix) recoveryPlan(r *maintain.Recovery, proven bool) *maintain.Plan {
	p := &maintain.Plan{
		Action: r.Action, Request: r.Request, Items: slices.Clone(r.Items), Retarget: r.Retarget,
		Supersede: r.Supersede, Write: true, Recovery: r, PlannedAt: time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC),
	}

	for i := range p.Items {
		p.Items[i].Status, p.Items[i].Reason = maintain.ItemReady, ""
	}

	if !proven {
		p.Items[0].Status = maintain.ItemBlocked
		p.Items[0].Reason = "the retargeted pin is not in the file with the reviewed content"
	}

	p.Confirmable = proven

	return p
}

// maintFakeOf returns a fake whose Pins read is the fixture and whose
// plan read returns p. commit scripts the commit; nil returns a success
// that moves every planned row.
func maintFakeOf(fx maintFix, p *maintain.Plan, commit func(*maintain.Plan) (maintain.Result, error)) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.pins = fx.inspection()
	// A scope preview needs an indexed database (contracts D-16), so the
	// header shows one, as it does when the console offers w.
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) { return p, nil }

	f.commitMaintenance = commit
	if commit == nil {
		f.commitMaintenance = func(q *maintain.Plan) (maintain.Result, error) {
			return maintain.Result{
				Action: q.Action, FileWritten: true, LedgerDone: true, Moved: maintLedgerIDs(q),
				Note: "the pin file and the ledger changed together",
			}, nil
		}
	}

	return f
}

// maintPartial is a partial commit in a shape that maintain.Commit
// returns, with every planned row read again at its planned values.
// With offer, the file write landed, the ledger update failed, and the
// restore failed, so the result carries a session recovery. Without it,
// the write reported a failure and the file could not be read again: the
// engine then never reaches the ledger step and offers no recovery,
// because no read saw the write land (D-13).
func maintPartial(fx maintFix, offer bool) func(*maintain.Plan) (maintain.Result, error) {
	return func(q *maintain.Plan) (maintain.Result, error) {
		res := maintain.Result{Action: q.Action, FileWritten: true}

		for _, it := range q.Items {
			if it.Status == maintain.ItemReady {
				res.Observed = append(res.Observed, it.Rows[0])
			}
		}

		if offer {
			res.LedgerErr = errors.New("store: database is locked")
			res.RestoreErr = errors.New("integration: .seamark/lessons.yaml: read-only file system")
			res.Recovery = fx.recovery(q)
			res.Note = "reconciliation required: the ledger update failed, the pin file was not restored, and the " +
				"ledger rows keep their earlier values"

			return res, nil
		}

		res.FileErr = errors.Join(errors.New("integration: .seamark/lessons.yaml: rename: disk full"),
			errors.New("the file could not be read after the failure: permission denied"))
		res.RestoreErr = errors.New("integration: .seamark/lessons.yaml: permission denied")
		res.Note = "reconciliation required: the pin file write reported a failure, and the file could not be read " +
			"again; nothing was restored, the ledger did not change, and no recovery is offered"

		return res, nil
	}
}

// maintScopeFacts are the facts of the computed scope of p4, as the
// engine words them (maintain factLines).
func maintScopeFacts() []string {
	return []string{
		"trigger internal/store/sql/query.go is cited evidence; it delivers to internal/store/sql",
		"trigger internal/store/cache.go is not confirmed by the evidence or the history; it does not set the scope",
	}
}

// maintNarrow is the single retarget of p4 to a narrower region.
func (fx maintFix) maintNarrow(t *testing.T, o maintPlanOpts) *maintain.Plan {
	t.Helper()

	return fx.plan(t, maintain.ActionRetarget, o, maintSpec{
		entry: maintWrap, status: maintain.ItemReady, to: []string{"internal/store/sql"}, narrower: true,
		facts: maintScopeFacts(),
	})
}

// maintWide retargets p12 to a broader set and p9 to the whole
// repository.
func (fx maintFix) maintWide(t *testing.T) *maintain.Plan {
	t.Helper()

	return fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true},
		maintSpec{
			entry: maintHandles, status: maintain.ItemReady, to: []string{"internal"}, broader: true,
			facts: []string{"no confirmed trigger: the scope is the coverage of 3 living finding(s)"},
		},
		maintSpec{
			entry: maintNilErrors, status: maintain.ItemReady, broader: true, repoWide: true,
			facts: []string{"no confirmed trigger: the scope is the coverage of 4 living finding(s); they are too " +
				"scattered for a region, so the scope is repository-wide"},
		})
}

// maintMixed selects a ready, a no-op and a blocked pin.
func (fx maintFix) maintMixed(t *testing.T) *maintain.Plan {
	t.Helper()

	return fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true},
		maintSpec{entry: maintWrap, status: maintain.ItemReady, to: []string{"internal/store/sql"},
			facts: maintScopeFacts()},
		maintSpec{entry: maintHandles, status: maintain.ItemNoOp, reason: "regions already current",
			to: []string{"internal/api", "internal/db"}},
		maintSpec{entry: maintChangelog, reason: "no linked proposal; the console changes only proposal-linked " +
			"pins (edit .seamark/lessons.yaml by hand)"})
}

// maintPrune prunes p16; kept names the retained alternative.
func (fx maintFix) maintPrune(t *testing.T, kept bool) *maintain.Plan {
	t.Helper()

	s := maintSpec{entry: maintOnError, status: maintain.ItemReady,
		reason: "no retained equivalent is established; this is a deliberate retirement"}
	if kept {
		s.kept = []int{maintHandles}
		s.reason = "similar wording stays in 1 linked pin(s); similar wording does not prove the same coverage"
	}

	return fx.plan(t, maintain.ActionPrune, maintPlanOpts{write: true, cursor: true}, s)
}

// maintHostOf returns the maintenance host of the Pins sub-view of h.
func maintHostOf(h *harness) *maintHost {
	return &hookLessons(h).pins.host
}

// maintOpen opens Lessons › Pins and the preview of req at w by hgt.
func maintOpen(t *testing.T, f *fakeServices, req maintain.Request, w, hgt int) *harness {
	t.Helper()

	h := lesOpen(t, f, w, hgt)
	h.press("m")
	h.send(openMaintMsg{req: req})

	return h
}

// maintFooter returns the footer row of the frame.
func maintFooter(h *harness) string {
	lines := strings.Split(h.view(), "\n")

	return lines[len(lines)-1]
}

// maintMutations returns the recorded maintenance calls that write.
func maintMutations(f *fakeServices) []string {
	var out []string

	for _, c := range f.called() {
		if strings.HasPrefix(c, "Commit") {
			out = append(out, c)
		}
	}

	return out
}

// maintContent returns the whole current stage on one line: the border
// labels, every body line, the scope summary and the actions. A frame
// shows only the window of a long preview, so content checks read the
// stage itself.
func maintContent(t *testing.T, h *harness) string {
	t.Helper()

	mod := maintHostOf(h).modal
	require.NotNil(t, mod, "no maintenance preview is open")

	v := mod.content(modalBodyWidth(modalMinW, h.m.st.width))

	parts := append([]string{v.title, v.right}, v.body...)
	parts = append(parts, v.tail...)
	parts = append(parts, h.m.st.theme.hints(v.actions))

	return lesFlat(ansi.Strip(strings.Join(parts, "\n")))
}

// maintShown returns the modal as the frame shows it, at the size that
// the app gives it, on one line and without its side borders. So a
// check sees only the visible window, and it can span a wrapped line.
func maintShown(h *harness) string {
	box := ansi.Strip(maintHostOf(h).modal.modal(h.m.st.width, h.m.st.height-1))

	return lesFlat(strings.ReplaceAll(box, "│", " "))
}

// maintHasKey reports an enabled footer key k on the Lessons screen.
func maintHasKey(h *harness, k string) bool {
	for _, b := range hookLessons(h).keys() {
		if b.Enabled() && b.Help().Key == k {
			return true
		}
	}

	return false
}
