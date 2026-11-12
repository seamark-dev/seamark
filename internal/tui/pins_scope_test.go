package tui

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The manual scope edit of a linked pin in the console. The tests with a
// real repository drive the keys, the real planner, and the real commit.
// The tests with a fake script the states that a repository cannot reach
// on demand, such as a failed write.

// scopeChosenAt is the stamp of a chosen row in these tests. It reads
// 2026-08-21 in UTC.
const scopeChosenAt = int64(1_787_270_400)

// scopeChosenDate is scopeChosenAt as the console prints it.
const scopeChosenDate = "2026-08-21"

// scopeTyped is the region that the keys add to the fixture pin.
const scopeTyped = "internal/web"

// scopePlan returns the plan that maintain.PlanScope returns for the typed
// set to of p4. blocked holds the lines of a plan that y cannot confirm.
// The candidate file comes from the real pinfile planner.
func (fx maintFix) scopePlan(t *testing.T, o maintPlanOpts, blocked []string, to ...string) *maintain.Plan {
	t.Helper()

	pin := fx.pins[maintWrap]
	ref := pin.Entry.Ref()
	row := maintRows()[maintWrap]
	from := maintEntryRegions(pin.Entry)

	change, err := pinfile.Plan(fx.doc, []pinfile.Edit{{Ref: ref, Regions: to}}, nil)
	require.NoError(t, err)

	it := maintain.PlanItem{
		Ref: ref, Pin: pin, From: from, To: to, Rows: []domain.Proposal{row}, Status: maintain.ItemBlocked,
		Drafted: true, Broader: distill.Reaches(to, from), Narrower: distill.Reaches(from, to),
	}

	p := &maintain.Plan{
		Action: maintain.ActionScope, Request: maintain.Request{Action: maintain.ActionScope, Refs: []pinfile.EntryRef{ref}},
		Items: []maintain.PlanItem{it}, Change: change, Write: o.write, GateErr: o.gateErr, Blocked: blocked,
		Scoping: &maintain.ScopeRequest{At: scopeChosenAt},
	}

	if len(blocked) == 0 {
		p.Items[0].Status = maintain.ItemReady
		p.Retarget = []store.RegionChange{{
			Guard: store.NewRowGuard(row), Region: to[0], Regions: to, Chosen: scopeChosenAt,
		}}
		p.Confirmable = o.write && o.gateErr == nil
	}

	return p
}

// scopeFake returns a fake whose scope plan is p. commit scripts the
// commit, as in maintFakeOf.
func scopeFake(fx maintFix, p *maintain.Plan, commit func(*maintain.Plan) (maintain.Result, error)) *fakeServices {
	f := maintFakeOf(fx, nil, commit)
	f.planScope = func(maintain.ScopeRequest) (*maintain.Plan, error) { return p, nil }

	return f
}

// scopeType adds scopeTyped to the regions of the cursor pin.
func scopeType(h *harness) {
	h.press("i", "a")
	h.typeText(scopeTyped)
	h.press("enter")
}

// scopeOpen opens Lessons › Pins and the scope preview of the first pin.
func scopeOpen(t *testing.T, f *fakeServices) *harness {
	t.Helper()

	h := pinsOpen(t, f, 120, 60)

	scopeType(h)
	h.press("s")
	require.True(t, maintHostOf(h).open(), "the draft reaches the scope preview")

	return h
}

// scopeRepo returns a real repository whose first pin links p1. Before
// the console opens, edit can change its pin file or its settings.
func scopeRepo(t *testing.T, edit func(root string)) (*harness, services.All, string) {
	t.Helper()

	root := dbRootRepo(t, "repo")
	if edit != nil {
		edit(root)
	}

	h, svc := dbRootConsole(t, root, store.DefaultPath(root))
	require.Equal(t, "linked-rule", cursorPin(t, h).Entry.Rule)

	return h, svc, root
}

// scopeLedgerRow returns the applied row of the linked pin.
func scopeLedgerRow(t *testing.T, svc services.All) domain.Proposal {
	t.Helper()

	ledger, err := svc.Ledger()
	require.NoError(t, err)

	for _, row := range ledger.Applied {
		if row.Rule == "linked-rule" {
			return row
		}
	}

	require.FailNow(t, "the ledger holds no applied row of the pin")

	return domain.Proposal{}
}

// scopeNoRetarget asserts that a stage of this action names no retarget,
// in any case, because each route keeps its own words.
func scopeNoRetarget(t *testing.T, text string) {
	t.Helper()

	assert.NotContains(t, strings.ToLower(text), "retarget")
}

func TestPinScopeEditPreviewAndSave(t *testing.T) {
	h, svc, root := scopeRepo(t, nil)

	h.press("i", "a")
	h.typeText("web")
	h.press("enter", "s")

	text := maintContent(t, h)
	scopeNoRetarget(t, text)

	for _, want := range []string{
		"Edit regions", "broader", "to the regions that you chose", "Broader:",
		"No file yet: web. A pin can name a future file", "LEDGER p1 api › api, web",
		"only its stored regions and the scope choice change", "WILL", "WON'T", "DIFF",
		"Edit the regions of 1 pin · broader · y writes lessons.yaml and the ledger", "y save the regions",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "the cursor row cannot change")

	h.press("y")

	text = maintContent(t, h)
	scopeNoRetarget(t, text)
	assert.Contains(t, text, "Regions saved")
	assert.Contains(t, text, "1 pin edited")

	data, err := os.ReadFile(filepath.Join(root, pinfile.File))
	require.NoError(t, err)
	assert.Contains(t, string(data), "regions: [api, web]")

	row := scopeLedgerRow(t, svc)
	assert.Equal(t, []string{"api", "web"}, row.RegionSet())
	assert.Positive(t, row.ScopeChosen, "the row records that a person chose the set")

	h.press("enter")
	assert.False(t, maintHostOf(h).open())

	// The draft landed, so the reload reports no lost draft, and the pin
	// stays linked.
	assert.Empty(t, authorOf(h).edits)
	assert.Empty(t, authorOf(h).orphans)
	assert.NotContains(t, h.m.toast.text, "lost its pin")
	assert.True(t, pinLinked(cursorPin(t, h)), "the link holds")
	assert.Contains(t, pinsDetail(t, h), "A person chose these regions on ")
}

// A new order is a region change, so it takes the preview and not the
// pin review.
func TestPinScopeEditOfANewOrderTakesThePreview(t *testing.T) {
	fx := maintFixture(t)
	f := scopeFake(fx, fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/db", "internal/api"), nil)
	h := pinsOpen(t, f, 120, 60)

	pinsCursorTo(h, maintHandles+1)
	authorDraftRegions(h, "internal/db", "internal/api")
	h.press("s")

	require.True(t, maintHostOf(h).open())
	assert.Equal(t, []string{"PlanScope"}, f.called())
}

// A pin with no region reads as repository-wide in the form, and a person
// chooses these regions there. The way back is a repository-wide choice.
func TestPinScopeEditOfARepositoryWidePin(t *testing.T) {
	t.Run("a pin without a region takes regions", func(t *testing.T) {
		f := authorFake(nil)
		in := pinsInspection()
		in.Pins[0].Entry.Region = ""
		f.pins = in

		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 1)
		h.press("i")

		d := authorOf(h).form.draft
		require.Equal(t, maintain.ScopeRepoWide, d.scope)

		d.scope, d.regions = maintain.ScopeRegions, []string{"api"}
		h.press("s")

		assert.True(t, maintHostOf(h).open())
		assert.Equal(t, []string{"PlanScope"}, f.called())
	})

	t.Run("a repository-wide choice warns", func(t *testing.T) {
		h, svc, _ := scopeRepo(t, nil)

		h.press("i")

		d := authorOf(h).form.draft
		d.scope, d.regions = maintain.ScopeRepoWide, nil
		h.press("s")

		text := maintContent(t, h)
		assert.Contains(t, text, "Repository-wide: the pin reaches every path")
		assert.Contains(t, text, "Edit the regions of 1 pin · repository-wide")

		h.press("y")
		assert.Empty(t, scopeLedgerRow(t, svc).RegionSet())
		assert.Positive(t, scopeLedgerRow(t, svc).ScopeChosen)
	})
}

// A draft that keeps the regions takes the pin review: a note edit, and a
// retyped spelling of the same region. A stamp there would record a
// choice that nobody made.
func TestPinScopeKeptRegionsTakeThePinReview(t *testing.T) {
	cases := []struct {
		name string
		edit func(d *pinDraft)
	}{
		{"a note edit", func(d *pinDraft) { d.note.SetValue("A new note.") }},
		{"a retyped spelling", func(d *pinDraft) { d.regions = []string{"internal/store/"} }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			h := pinsOpen(t, f, 120, 40)

			pinsCursorTo(h, 1)
			h.press("i")
			c.edit(authorOf(h).form.draft)
			h.press("s")

			assert.False(t, maintHostOf(h).open())
			assert.Equal(t, []string{"PlanAuthor:edit"}, f.called(), "this draft has no ledger change")
		})
	}
}

// Another recorded root reads as an unknown link, so a draft takes the
// pin review. That review keeps y off and names the root route.
func TestPinScopeForeignRootTakesThePinReview(t *testing.T) {
	f := authorFake(nil)
	in := pinsInspection()
	in.Index.Root = "/work/original"
	f.pins = in

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, 1)
	scopeType(h)
	h.press("s")

	assert.False(t, maintHostOf(h).open())
	assert.Equal(t, []string{"PlanAuthor:edit"}, f.called())
}

func TestPinScopePreviewShowsItsNotes(t *testing.T) {
	const (
		identity = "Earlier deliveries stay recorded under the earlier wording and scope."
		ledger   = "The proposal ledger records the new regions and that a person chose them."
	)

	for _, write := range []bool{true, false} {
		h, _, _ := scopeRepo(t, func(root string) {
			if !write {
				path := filepath.Join(root, ".seamark", "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("distill:\n  write: false\n"), 0o644))
			}
		})

		h.press("i", "a")
		h.typeText("web")
		h.press("enter", "s")

		text := maintContent(t, h)
		assert.Contains(t, text, "EFFECTS", "write %v", write)
		assert.Contains(t, text, identity, "write %v", write)
		assert.Contains(t, text, ledger, "write %v", write)
		assert.Equal(t, write, maintHasKey(h, "y"))
	}
}

// A draft can change the note beside the regions, and the preview then adds
// the text about the 'edited' mark. That text keeps the words of this route
// before y, also with y off, and in the result.
func TestPinScopePreviewOfANoteChangeKeepsTheRouteWords(t *testing.T) {
	for _, write := range []bool{true, false} {
		h, _, root := scopeRepo(t, func(root string) {
			if !write {
				path := filepath.Join(root, ".seamark", "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("distill:\n  write: false\n"), 0o644))
			}
		})

		authorDraftRegions(h, "api", "web")
		authorOf(h).form.draft.note.SetValue("A new note.")
		h.press("s")

		text := maintContent(t, h)
		assert.Contains(t, text, "Pins shows the difference as 'edited'.", "write %v", write)
		scopeNoRetarget(t, text)
		assert.Equal(t, write, maintHasKey(h, "y"))

		if !write {
			continue
		}

		h.press("y")

		scopeNoRetarget(t, maintContent(t, h))

		data, err := os.ReadFile(filepath.Join(root, pinfile.File))
		require.NoError(t, err)
		assert.Contains(t, string(data), "A new note.")
		assert.Contains(t, string(data), "regions: [api, web]")
	}
}

func TestPinScopePreviewShowsEveryBlocker(t *testing.T) {
	t.Run("the gate note hides no collision", func(t *testing.T) {
		h, _, root := scopeRepo(t, func(root string) {
			pins := filepath.Join(root, pinfile.File)
			data, err := os.ReadFile(pins)
			require.NoError(t, err)

			extra := "  - rule: linked-rule\n    region: web\n    note: Another pin of this rule.\n"
			require.NoError(t, os.WriteFile(pins, append(data, extra...), 0o644))

			cfg := filepath.Join(root, ".seamark", "config.yaml")
			require.NoError(t, os.WriteFile(cfg, []byte("distill:\n  write: false\n"), 0o644))
		})
		require.NotEmpty(t, root)

		// The form replaces the region, so the new set is the set of the
		// other pin.
		authorDraftRegions(h, "web")
		h.press("s")

		text := maintContent(t, h)
		assert.Contains(t, text, "distill.write is off, so this preview changes nothing.")
		assert.Contains(t, text, "after a save, press i in Pins for a new preview.")
		assert.Contains(t, text, "choose other regions, or decide the proposal in Proposals")
		assert.Contains(t, text, "api")
		assert.Contains(t, text, "web", "the preview keeps the new regions of a valid draft")
		assert.NotContains(t, text, "revise the selection")
		assert.NotContains(t, text, "cursor row")
		assert.False(t, maintHasKey(h, "y"))
		scopeNoRetarget(t, text)
	})

	t.Run("a blocked candidate keeps its impact and its coverage", func(t *testing.T) {
		fx := maintFixture(t)
		note := "Wrap SQL errors with the query name."
		p := fx.scopePlan(t, maintPlanOpts{write: true}, []string{"a file blocker; a route"}, "internal/web")
		p.Impact = checkImpactOf(t, checkImpactWrapYAML("wrap-sql-errors", "internal/store", note),
			checkImpactWrapYAML("wrap-sql-errors", "internal/web", note))

		h := scopeOpen(t, scopeFake(fx, p, nil))

		text := maintContent(t, h)
		assert.Contains(t, text, "A file blocker; a route.")
		assert.Equal(t, 1, strings.Count(text, "CHECK IMPACT"), "the block shows once")
		assert.Contains(t, text, "Paths outside the new regions lose this check, and paths in the new regions gain it.")
		assert.Contains(t, text, "y is off: this draft cannot be written")
		assert.Contains(t, text, "DIFF")
		assert.NotContains(t, text, "The diff holds the ready pins only.")
	})

	t.Run("a blocking issue keeps the old regions", func(t *testing.T) {
		h, _, _ := scopeRepo(t, nil)

		authorDraftRegions(h, "../outside")
		h.press("s")

		text := maintContent(t, h)
		assert.Contains(t, text, "Regions api", "the old regions show")
		assert.Contains(t, text, "Regions: invalid region:", "the issue names its row")
		assert.NotContains(t, text, "keeps its recorded rule", "no note changes")
		assert.False(t, maintHasKey(h, "y"))
	})
}

// authorDraftRegions opens the form of the cursor pin with regions as its
// typed set.
func authorDraftRegions(h *harness, regions ...string) {
	h.press("i")

	authorOf(h).form.draft.regions = regions
}

func TestPinScopeEditCancelKeepsTheDraft(t *testing.T) {
	fx := maintFixture(t)
	f := scopeFake(fx, fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped), nil)
	h := scopeOpen(t, f)

	h.press("esc")
	assert.False(t, maintHostOf(h).open())
	assert.Nil(t, authorOf(h).form, "the form closed for the preview")
	assert.Empty(t, maintMutations(f))
	require.Len(t, authorOf(h).edits, 1, "the draft stays in the session")

	h.press("i")
	require.NotNil(t, authorOf(h).form)
	assert.Equal(t, []string{"internal/store", scopeTyped}, authorOf(h).form.draft.regions)
}

func TestPinScopeEditGateRoutesToSettings(t *testing.T) {
	fx := maintFixture(t)
	f := scopeFake(fx, fx.scopePlan(t, maintPlanOpts{}, nil, "internal/store", scopeTyped), nil)
	h := scopeOpen(t, f)

	text := maintContent(t, h)
	assert.Contains(t, text, "Press o for Settings › Pin writing; after a save, press i in Pins for a new preview.")
	assert.False(t, maintHasKey(h, "y"))
	require.True(t, maintHasKey(h, "o"))

	h.press("o")
	assert.Equal(t, destSettings, h.m.active)
	assert.False(t, maintHostOf(h).open())
	assert.Len(t, authorOf(h).edits, 1, "the draft stays for i")
}

func TestPinScopeFailedWriteNamesTheDraftRoute(t *testing.T) {
	cases := []struct {
		name string
		res  maintain.Result
		want string
	}{
		{
			"the file write fails", maintain.Result{Action: maintain.ActionScope, FileErr: errors.New("disk full")},
			"Fix the cause named under File error, then press i in Pins for a new preview.",
		},
		{
			"the ledger write fails",
			maintain.Result{Action: maintain.ActionScope, FileWritten: true, Restored: true, LedgerErr: errors.New("locked")},
			"Fix the cause named under Ledger error, then press i in Pins for a new preview.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := maintFixture(t)
			p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
			f := scopeFake(fx, p, func(*maintain.Plan) (maintain.Result, error) { return c.res, nil })
			h := scopeOpen(t, f)

			h.press("y")

			text := maintContent(t, h)
			scopeNoRetarget(t, text)
			assert.Contains(t, text, c.want)
			assert.NotContains(t, text, "press w")

			h.press("enter")
			assert.False(t, maintHostOf(h).open(), "enter closes the result")

			h.press("i")
			require.NotNil(t, authorOf(h).form, "i opens the draft")
			assert.Equal(t, []string{"internal/store", scopeTyped}, authorOf(h).form.draft.regions)
		})
	}
}

// A stale result gives enter a new preview, which reads the draft again.
func TestPinScopeStaleResultPreviewsAgain(t *testing.T) {
	fx := maintFixture(t)
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	f := scopeFake(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		return fakeMaintResult(q), maintain.ErrStale
	})
	h := scopeOpen(t, f)

	h.press("y")
	assert.Contains(t, maintContent(t, h), "Something changed since the preview")

	h.press("enter")
	assert.Equal(t, []string{"PlanScope", "CommitMaintenance:scope", "PlanScope"}, f.called())
}

// y waits while another operation runs. The preview says so, and no
// commit starts.
func TestPinScopePreviewWaitsForAnotherOperation(t *testing.T) {
	fx := maintFixture(t)
	f := scopeFake(fx, fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped), nil)
	h := scopeOpen(t, f)

	h.m.st.mutation = "another writer"
	h.press("y")

	text := maintContent(t, h)
	assert.Contains(t, text, "y waits: another operation is running")
	assert.Empty(t, maintMutations(f))
}

func TestPinScopePartialResultOffersTheCompletion(t *testing.T) {
	fx := maintFixture(t)
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	f := scopeFake(fx, p, maintPartial(fx, true))
	f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) { return fx.recoveryPlan(r, true), nil }
	h := scopeOpen(t, f)

	h.press("y")

	text := maintContent(t, h)
	scopeNoRetarget(t, text)
	assert.Contains(t, text, "Regions written, ledger incomplete")
	assert.Contains(t, text, "Do not edit the regions again")
	assert.Contains(t, text, "Recovery available: c previews the ledger-only completion")

	h.press("c")

	text = maintContent(t, h)
	scopeNoRetarget(t, text)
	assert.Contains(t, text, "The pin file still holds the regions that this session wrote.")
	assert.Contains(t, text, "The file holds the reviewed entry with the new regions.")

	t.Run("without a proof the text names the reconcile route", func(t *testing.T) {
		route := maintManualRoute(maintain.ActionScope, "p4")
		scopeNoRetarget(t, route)
		assert.Contains(t, route, "then press i in Pins for a new preview")
	})
}

// The Ledger error row prints the error of the real store. A fake scripts
// that text, so this test uses the real engine and the real keys.
func TestPinScopeLedgerFailureKeepsTheRouteWords(t *testing.T) {
	h, svc, root := scopeRepo(t, nil)
	row := scopeLedgerRow(t, svc)

	path := filepath.Join(root, pinfile.File)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	db, err := sql.Open("sqlite", store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	_, err = db.Exec(`CREATE TRIGGER fail_region BEFORE UPDATE OF region ON proposal
BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`)
	require.NoError(t, err)

	h.press("i", "a")
	h.typeText("web")
	h.press("enter", "s", "y")

	text := maintContent(t, h)
	assert.Contains(t, text, "Ledger error")
	assert.Contains(t, text, "scope edit p1")
	scopeNoRetarget(t, text)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the file is restored")
	assert.Equal(t, row, scopeLedgerRow(t, svc), "the ledger does not change")

	h.press("enter", "i")
	require.NotNil(t, authorOf(h).form, "i opens the draft")
	assert.Equal(t, []string{"api", "web"}, authorOf(h).form.draft.regions)
}

func TestMaintScopePreviewShowsCheckImpact(t *testing.T) {
	fx := maintFixture(t)
	note := "Wrap SQL errors with the query name."
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	p.Impact = checkImpactOf(t, checkImpactWrapYAML("wrap-sql-errors", "internal/store", note),
		checkImpactWrapYAML("wrap-sql-errors", "internal/store", note+" Name it."))

	h := scopeOpen(t, scopeFake(fx, p, nil))

	text := maintContent(t, h)
	assertImpactBeforeConfirm(t, text, p.Impact, "y save the regions")
	assert.Contains(t, text, "Paths in the new regions gain this check.")
	assert.Less(t, strings.Index(text, "CHECK IMPACT"), strings.Index(text, "Paths in the new regions"))
}

// The lead names a choice of a person. Evidence stands behind no typed
// set, so the lead never reads as support.
func TestPinScopePreviewClaimsNoEvidence(t *testing.T) {
	for _, write := range []bool{true, false} {
		fx := maintFixture(t)
		p := fx.scopePlan(t, maintPlanOpts{write: write}, nil, "internal/store", scopeTyped)
		h := scopeOpen(t, scopeFake(fx, p, nil))

		lead := lesFlat(strings.Join(maintHostOf(h).modal.planLead(100), "\n"))
		assert.NotContains(t, strings.ToLower(lead), "evidence", "write %v", write)
		assert.NotContains(t, strings.ToLower(lead), "support", "write %v", write)
	}
}

// chooseRow marks the applied row of entry as chosen in the inspection.
func chooseRow(in *maintain.Inspection, entry int) {
	applied := []domain.Proposal{in.Pins[entry].Link.Applied[0]}
	applied[0].ScopeChosen = scopeChosenAt
	in.Pins[entry].Link.Applied = applied
}

func TestPinRetargetOfAChosenRowNamesTheChoice(t *testing.T) {
	fx := maintFixture(t)

	var committed *maintain.Plan

	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	p.Items[0].Rows = []domain.Proposal{maintRows()[maintWrap]}
	p.Items[0].Rows[0].ScopeChosen = scopeChosenAt

	f := maintFakeOf(fx, p, nil)
	in := fx.inspection()
	chooseRow(in, maintWrap)
	f.pins = in

	// The planner refuses a chosen row unless the console sets the flag, so
	// this test fails without it.
	f.planMaintenance = func(req maintain.Request) (*maintain.Plan, error) {
		if !req.ReplaceChosen {
			blocked := *p
			blocked.Confirmable = false

			return &blocked, nil
		}

		return p, nil
	}
	commit := f.commitMaintenance
	f.commitMaintenance = func(q *maintain.Plan) (maintain.Result, error) {
		committed = q

		return commit(q)
	}

	h := pinsOpen(t, f, 120, 60)

	detail := pinsDetail(t, h)
	assert.Contains(t, detail, "suggests a change")
	assert.Contains(t, detail, "A person chose these regions on "+scopeChosenDate+".")

	h.press("w")

	text := maintContent(t, h)
	assert.Contains(t, text, "This retarget replaces the regions that a person chose on "+scopeChosenDate+".")
	assert.Less(t, strings.Index(text, "replaces the regions"), strings.LastIndex(text, "y retarget"))

	h.press("y")
	require.NotNil(t, committed, "y commits the reviewed plan")
	assert.Zero(t, committed.Retarget[0].Chosen, "the computed set clears the choice")
}

func TestPinChosenRowOfferingNoRetargetRoutesToTheForm(t *testing.T) {
	fx := maintFixture(t)
	f := maintFakeOf(fx, nil, nil)
	in := fx.inspection()
	chooseRow(in, maintOnError)
	f.pins = in

	h := pinsOpen(t, f, 120, 60)
	pinsCursorTo(h, maintOnError+1)

	assert.Contains(t, authorUnavailable(h), "w")

	p := pinsOf(h)
	assert.Equal(t, pinRouteForm, p.retargetNext(cursorPin(t, h)))

	detail := pinsDetail(t, h)
	assert.Contains(t, detail, "A person chose these regions on "+scopeChosenDate+".")
	assert.Contains(t, detail, pinsFormSentence)
	assert.NotContains(t, detail, "suggests a change", "a current scope has no suggestion")
}

// A scope edit of a chosen row replaces a choice with a new choice, so the
// line of a computed retarget has no place in its preview.
func TestPinScopeSecondEditOfAChosenRowKeepsItsWords(t *testing.T) {
	h, svc, _ := scopeRepo(t, nil)

	h.press("i", "a")
	h.typeText("web")
	h.press("enter", "s", "y", "enter")
	require.Positive(t, scopeLedgerRow(t, svc).ScopeChosen)

	authorDraftRegions(h, "api")
	h.press("s")

	text := maintContent(t, h)
	assert.Contains(t, text, "Edit regions")
	assert.NotContains(t, text, "This retarget replaces")
	scopeNoRetarget(t, text)
}

// A row without a stamp has no choice to replace, so its retarget preview
// names no person and no date.
func TestPinRetargetOfARowWithoutAChoiceNamesNoChoice(t *testing.T) {
	fx := maintFixture(t)

	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	p.Items[0].Rows = []domain.Proposal{maintRows()[maintWrap]}

	h := pinsOpen(t, maintFakeOf(fx, p, nil), 120, 60)
	h.press("w")

	text := maintContent(t, h)
	assert.Contains(t, text, "y retarget")
	assert.NotContains(t, text, "a person chose")
}

// A blocked draft has no candidate file, so the DIFF section keeps its place
// and says why, in the words of this route.
func TestPinScopePreviewWithoutACandidateSaysWhy(t *testing.T) {
	h, _, _ := scopeRepo(t, nil)

	authorDraftRegions(h, "../outside")
	h.press("s")

	text := maintContent(t, h)
	assert.Contains(t, text, "No file change: the draft has items to fix, or a blocker stops the plan.")
	scopeNoRetarget(t, text)
}

// The pin of a scope edit can carry a note that a person edited after
// distillation. The comparison must say what this route does to that note.
func TestPinScopePreviewOfAnEditedNoteKeepsTheRouteWords(t *testing.T) {
	h, _, _ := scopeRepo(t, func(root string) {
		path := filepath.Join(root, pinfile.File)
		data, err := os.ReadFile(path)
		require.NoError(t, err)

		edited := strings.Replace(string(data), "Linked note here.", "A note that a person edited.", 1)
		require.NoError(t, os.WriteFile(path, []byte(edited), 0o644))
	})

	authorDraftRegions(h, "api", "web")
	h.press("s")

	text := maintContent(t, h)
	assert.Contains(t, text, "LIVE NOTE")
	assert.Contains(t, text, "The note was edited after distillation. The recorded note of p1 stays as history, "+
		"and the live note changes only when the draft changes it.")
	scopeNoRetarget(t, text)
}

// A refused commit writes nothing, and the draft stays. So the route is the
// key that opens the draft, not a new selection.
func TestPinScopeRefusedCommitNamesTheDraftRoute(t *testing.T) {
	fx := maintFixture(t)
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	f := scopeFake(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		return fakeMaintResult(q), maintain.ErrNotConfirmable
	})
	h := scopeOpen(t, f)

	h.press("y")

	text := maintContent(t, h)
	assert.Contains(t, text, "The preview cannot be confirmed, so nothing was written. Press i in Pins to open the draft again.")
	assert.NotContains(t, text, "Revise the selection and preview again.")
	scopeNoRetarget(t, text)
}

// The notice row and the quit question show this name while the commit runs.
func TestPinScopeCommitNamesItsWork(t *testing.T) {
	fx := maintFixture(t)
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	h := scopeOpen(t, scopeFake(fx, p, nil))

	mod := maintHostOf(h).modal

	commit := mod.commit()
	require.Equal(t, flowCommitting, mod.stage)
	assert.Equal(t, "Writing the regions of 1 pin", h.m.st.mutation)
	assert.Contains(t, h.view(), "Writing the regions of 1 pin")

	h.run(commit)
	assert.Empty(t, h.m.st.mutation)
}

// A region that the user typed but did not confirm belongs to the draft. A
// reload that locks the rows must count it as an edit, so the note says that
// it went away. The fake changes the stored regions and keeps the binding,
// so the reload takes the branch of an unchanged entry.
func TestPinDraftReloadThatLocksTheRegionsReportsTheTypedText(t *testing.T) {
	f := authorFake(nil)
	f.pins = pinsNoLedger()
	h := pinsOpen(t, f, 120, 40)

	pinsCursorTo(h, 1)
	h.press("i", "a")
	h.typeText("internal/typed")

	form := authorOf(h).form
	require.Equal(t, pinFieldRegion, form.field)

	locked := pinsInspection()
	ref := locked.Pins[0].Entry.Ref()
	locked.Pins[0].Entry.Region = "internal/store\u0085"
	locked.Doc.Entries[0] = locked.Pins[0].Entry
	require.Equal(t, ref, locked.Pins[0].Entry.Ref(), "the reload keeps the binding")

	authorReload(h, f, locked)

	assert.Contains(t, h.m.toast.text, `The scope edit of "wrap-sql-errors" went away`)
	assert.Contains(t, form.draft.scopeLock, "linked to proposal p4")
	assert.Equal(t, pinFieldNone, form.field, "a read-only row takes no typing")
}

// The console opens this preview after a stale result under --db. The plan of
// the engine can be confirmable there, so only the console keeps y off, and
// the tail names the reason.
func TestPinScopePreviewUnderAnotherDatabaseNamesTheRoot(t *testing.T) {
	fx := maintFixture(t)
	p := fx.scopePlan(t, maintPlanOpts{write: true}, nil, "internal/store", scopeTyped)
	f := scopeFake(fx, p, func(q *maintain.Plan) (maintain.Result, error) {
		return fakeMaintResult(q), maintain.ErrStale
	})
	h := scopeOpen(t, f)

	h.press("y")

	f.database = &workspace.DatabaseFacts{Kind: store.DatabaseOther, ForeignRoot: "/work/original"}
	h.send(refreshMsg{kinds: []sectionKind{secPins}})
	h.press("enter")

	assert.False(t, maintHasKey(h, "y"))
	assert.Contains(t, maintContent(t, h), "y is off: the index records another repository")

	h.press("y")
	assert.Equal(t, 1, f.callCount("CommitMaintenance:scope"), "y writes nothing")
}

// A repository-wide pin that takes regions is the main path of a manual
// scope edit, so the real engine must label the plan as narrower.
func TestPinScopeEditOfARepositoryWidePinReadsNarrower(t *testing.T) {
	h, _, _ := scopeRepo(t, func(root string) {
		path := filepath.Join(root, pinfile.File)
		data, err := os.ReadFile(path)
		require.NoError(t, err)

		wide := strings.Replace(string(data), "rule: linked-rule\n    region: api\n", "rule: linked-rule\n    region: '*'\n", 1)
		require.NoError(t, os.WriteFile(path, []byte(wide), 0o644))

		db, err := sql.Open("sqlite", store.DefaultPath(root))
		require.NoError(t, err)

		defer func() { _ = db.Close() }()

		_, err = db.Exec(`UPDATE proposal SET region = '' WHERE rule = 'linked-rule'`)
		require.NoError(t, err)
	})

	h.press("i")

	d := authorOf(h).form.draft
	require.Equal(t, maintain.ScopeRepoWide, d.scope)

	d.scope, d.regions = maintain.ScopeRegions, []string{"api"}
	h.press("s")

	assert.Equal(t, "narrower", ansi.Strip(maintHostOf(h).modal.planRight()))

	text := maintContent(t, h)
	assert.Contains(t, text, maintNarrowerText)
	assert.NotContains(t, text, "Broader:")
	scopeNoRetarget(t, text)
}

// The lists say what y writes. A draft that changes another field writes that
// field too, so the list that says the live note stays must change.
func TestPinScopeEffectsFollowTheFieldsOfTheDraft(t *testing.T) {
	const (
		other    = "write the other fields of the draft"
		keepNote = "change the rule, the live note, the comments or the position of the pin"
		keep     = "change the rule, the comments or the position of the pin"
	)

	// open previews a draft that sets the fields of edit.
	open := func(t *testing.T, opts maintPlanOpts, edit maintain.AuthorRequest) string {
		t.Helper()

		fx := maintFixture(t)
		p := fx.scopePlan(t, opts, nil, "internal/store", scopeTyped)
		p.Scoping = &maintain.ScopeRequest{Edit: edit, At: scopeChosenAt}

		return maintContent(t, scopeOpen(t, scopeFake(fx, p, nil)))
	}

	fields := map[string]maintain.AuthorRequest{
		"the note":  {NoteSet: true},
		"the mode":  {ModeSet: true},
		"the check": {CheckSet: true},
	}

	for name, edit := range fields {
		t.Run(name, func(t *testing.T) {
			text := open(t, maintPlanOpts{write: true}, edit)
			assert.Contains(t, text, other)
			assert.Contains(t, text, keep)
			assert.NotContains(t, text, "the live note")
		})

		t.Run(name+" with y off", func(t *testing.T) {
			text := open(t, maintPlanOpts{}, edit)
			assert.NotContains(t, text, "WILL")
			assert.NotContains(t, text, "WON'T")
			assert.NotContains(t, text, other)
		})
	}

	t.Run("a draft of the regions alone", func(t *testing.T) {
		text := open(t, maintPlanOpts{write: true}, maintain.AuthorRequest{})
		assert.Contains(t, text, keepNote)
		assert.NotContains(t, text, other)
	})

	t.Run("the real engine reads the draft", func(t *testing.T) {
		h, _, _ := scopeRepo(t, nil)

		authorDraftRegions(h, "api", "web")
		authorOf(h).form.draft.note.SetValue("A new note.")
		h.press("s")

		text := maintContent(t, h)
		assert.Contains(t, text, other)
		assert.Contains(t, text, keep)
	})
}
