package maintain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// retargetFindings are the evidence shapes of the CLI retarget goldens.
// Findings 1 and 2 cover [scripts]; 3 and 4 cover [api, db]; 5 to 8 are
// spread over four top-level trees, so they cover the repository.
var retargetFindings = []model.Finding{
	{ID: 1, LessonKey: "k", Path: "scripts/a.py", PR: 1, Body: "x", Source: model.SourceReview},
	{ID: 2, LessonKey: "k", Path: "scripts/b.py", PR: 2, Body: "y", Source: model.SourceReview},
	{ID: 3, LessonKey: "k", Path: "api/h.go", PR: 3, Body: "z", Source: model.SourceReview},
	{ID: 4, LessonKey: "k", Path: "db/q.go", PR: 4, Body: "w", Source: model.SourceReview},
	{ID: 5, LessonKey: "k", Path: "a/x.go", PR: 5, Body: "a", Source: model.SourceReview},
	{ID: 6, LessonKey: "k", Path: "b/x.go", PR: 6, Body: "b", Source: model.SourceReview},
	{ID: 7, LessonKey: "k", Path: "c/x.go", PR: 7, Body: "c", Source: model.SourceReview},
	{ID: 8, LessonKey: "k", Path: "d/x.go", PR: 8, Body: "d", Source: model.SourceReview},
}

// retargetLessons is the pin file of the CLI retarget goldens. The p1 pin
// is hand-edited in the middle of the list: rule case, live note, an
// unknown key, and a user comment above it. keep-me has no ledger row.
const retargetLessons = `# Review-lesson tuning (see ` + "`seamark lessons --list`" + `).

pin:
  - rule: keep-me
    region: web
    note: A hand-written pin without a ledger row.
  # my own comment about the datasets pin
  - rule: Guard-Empty-Datasets
    region: '*'
    note: HAND EDITED live note that must survive.
    extra_field: unknown
  - rule: already-current
    region: scripts
    note: Already current.
  - rule: aged-out
    region: scripts
    note: Its evidence aged out.
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p5)
  - rule: two-areas
    region: api
    note: Two areas.
  - rule: scattered
    region: a
    note: Scattered evidence.

mute:
  - rule: noisy-rule
`

// retargetLedger is the ledger of retargetFixture before any change.
const retargetLedger = `p1 applied "guard-empty-datasets" region="" regions=[] triggers=[] checked=0 v0
p2 applied "already-current" region="scripts" regions=[] triggers=[] checked=0 v0
p3 applied "aged-out" region="scripts" regions=[] triggers=[] checked=0 v0
p4 applied "missing-pin" region="" regions=[] triggers=[] checked=0 v0
p5 applied "two-areas" region="api" regions=[] triggers=[] checked=0 v0
p6 applied "scattered" region="a" regions=[] triggers=[] checked=0 v0
`

// retargetFixture seeds the retarget goldens: p1 and p4 compute
// [scripts], p2 is current, p3 cites only an aged-out finding, p5 widens
// to [api, db], and p6 widens to the repository. p4 has no entry.
func retargetFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, retargetFindings)
	f.write(retargetLessons, 0o600)
	f.propose(
		model.Proposal{Signature: "s1", Rule: "guard-empty-datasets", Note: "Guard datasets for emptiness before reductions.",
			Members: []int64{1, 2}, Agent: "claude/v1", Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "already-current", Region: "scripts", Note: "Already current.",
			Members: []int64{1, 2}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s3", Rule: "aged-out", Region: "scripts", Note: "Its evidence aged out.",
			Members: []int64{99}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s4", Rule: "missing-pin", Note: "No entry carries this pin.",
			Members: []int64{1, 2}, Agent: "claude/v4", Status: model.ProposalApplied},
		model.Proposal{Signature: "s5", Rule: "two-areas", Region: "api", Note: "Two areas.",
			Members: []int64{3, 4}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s6", Rule: "scattered", Region: "a", Note: "Scattered evidence.",
			Members: []int64{5, 6, 7, 8}, Agent: "claude/v6", Status: model.ProposalApplied},
	)
	require.Equal(t, retargetLedger, f.ledger())

	return f
}

func TestNewPlanRetargetComputesTheReviewedChange(t *testing.T) {
	f := retargetFixture(t)

	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))

	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
	assert.Empty(t, p.Blocked)
	assert.True(t, p.Write)
	require.NoError(t, p.GateErr)
	assert.Empty(t, p.FileBlock)
	assert.Equal(t, fixtureNow, p.PlannedAt)
	assert.NotEqual(t, [32]byte{}, p.Evidence)
	assert.True(t, p.File.Exists)

	require.Len(t, p.Items, 1)
	it := p.Items[0]
	assert.Equal(t, ItemReady, it.Status)
	assert.Empty(t, it.Reason)
	assert.Nil(t, it.From, "the live pin is repository-wide")
	assert.Equal(t, []string{"scripts"}, it.To)
	assert.False(t, it.Broader)
	assert.False(t, it.RepoWide)
	assert.Equal(t, []string{"no confirmed trigger: the scope is the coverage of 2 living finding(s)"}, it.Facts)
	require.Len(t, it.Rows, 1)
	assert.Equal(t, int64(1), it.Rows[0].ID)
	assert.True(t, it.Pin.Link.NoteChanged, "the live note differs from the recorded note")
	assert.True(t, it.Pin.Readings.Scope.Known)
	assert.Equal(t, distill.ScopeChanged, it.Pin.Readings.Scope.Advice.Kind)
	assert.True(t, it.Pin.Readings.Trigger.Known)
	assert.Equal(t, distill.TriggerOpen, it.Pin.Readings.Trigger.State)
	assert.False(t, it.Pin.Readings.Outcome.Known, "a preview reads no outcome")
	assert.Equal(t, reasonNotPreview, it.Pin.Readings.Outcome.Reason)

	require.NotNil(t, p.Change)
	assert.Equal(t, retargetLessons, string(p.Change.Before))
	assert.Contains(t, p.Change.Diff, "-    region: '*'")
	assert.Contains(t, p.Change.Diff, "+    region: scripts")
	assert.Equal(t, 1, p.Change.Added)
	assert.Equal(t, 1, p.Change.Removed)

	guard := store.NewRowGuard(it.Rows[0])
	assert.Equal(t, []store.RegionChange{{Guard: guard, Region: "scripts", Regions: []string{"scripts"}}}, p.Retarget)
	assert.Empty(t, p.Supersede)

	assert.Equal(t, []string{
		"only the region keys of each entry change; the rule, the note, the comments, and the position stay",
		"the proposal keeps its recorded note; only its stored regions change",
		"delivery history restarts under the new pin identity; the outcome reads untested until the pin fires again",
		"scope advice reads the index built 2025-09-27 19:06 UTC",
		"the diff hides text that looks like a secret; the file gets the exact bytes",
	}, p.Notes)

	// A plan writes nothing.
	assert.Equal(t, retargetLessons, f.lessons())
	assert.Equal(t, retargetLedger, f.ledger())
}

func TestNewPlanRetargetNamesEveryUnchangedEntry(t *testing.T) {
	f := retargetFixture(t)

	p := f.plan(f.deps(), f.request(ActionRetarget,
		"Guard-Empty-Datasets", "already-current", "aged-out", "keep-me", "two-areas", "scattered"))

	assert.False(t, p.Confirmable, "a mixed selection never acts on a subset")
	assert.Equal(t, []string{"3 of 6 selected pin(s) cannot change; revise the selection"}, p.Blocked)

	want := []struct {
		status ItemStatus
		reason string
		to     []string
	}{
		{ItemReady, "", []string{"scripts"}},
		{ItemNoOp, reasonCurrent, []string{"scripts"}},
		{ItemBlocked, reasonNoEvidence, nil},
		{ItemBlocked, "no linked proposal; the console changes only proposal-linked pins (edit .seamark/lessons.yaml by hand)", nil},
		{ItemReady, "", []string{"api", "db"}},
		{ItemReady, "", nil},
	}

	require.Len(t, p.Items, len(want))

	for i, w := range want {
		assert.Equal(t, w.status, p.Items[i].Status, "item %d", i)
		assert.Equal(t, w.reason, p.Items[i].Reason, "item %d", i)
		assert.Equal(t, w.to, p.Items[i].To, "item %d", i)
	}

	assert.True(t, p.Items[4].Broader, "two-areas widens")
	assert.True(t, p.Items[5].Broader, "scattered widens")
	assert.True(t, p.Items[5].RepoWide, "scattered becomes repository-wide")
	assert.Equal(t, []string{"no confirmed trigger: the scope is the coverage of 4 living finding(s); " +
		"they are too scattered for a region, so the scope is repository-wide"}, p.Items[5].Facts)

	// The ready part is still computed, so the preview shows its change.
	require.NotNil(t, p.Change)
	assert.Len(t, p.Change.Edits, 3)
	assert.Len(t, p.Retarget, 3)

	_, err := Commit(f.deps(), p)
	require.ErrorIs(t, err, ErrNotConfirmable)
	assert.Equal(t, retargetLessons, f.lessons())
	assert.Equal(t, retargetLedger, f.ledger())
}

func TestNewPlanRetargetNeedsAReadyIndex(t *testing.T) {
	f := retargetFixture(t)

	for _, state := range []workspace.IndexState{workspace.IndexNotIndexed, workspace.IndexOlder} {
		deps := f.deps()
		deps.Probe.State = state

		p := f.plan(deps, f.request(ActionRetarget, "Guard-Empty-Datasets"))
		require.Len(t, p.Items, 1)
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Equal(t, "unknown: scope advice needs an indexed database (the index is "+state.String()+"); "+
			"build it with `seamark index`", p.Items[0].Reason)
		assert.False(t, p.Confirmable)
		assert.Nil(t, p.Change)
	}

	// A prune needs no scope advice, so it plans without an index run.
	deps := f.deps()
	deps.Probe.State = workspace.IndexNotIndexed

	p := f.plan(deps, f.request(ActionPrune, "Guard-Empty-Datasets"))
	assert.True(t, p.Confirmable, "blocked: %v", p.Blocked)
}

func TestNewPlanRetargetRefusesCollisions(t *testing.T) {
	t.Run("another applied proposal records the target key", func(t *testing.T) {
		f := retargetFixture(t)
		f.propose(model.Proposal{Signature: "s7", Rule: "guard-empty-datasets", Region: "scripts", Note: "Other.",
			Members: []int64{1}, Status: model.ProposalApplied})

		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Contains(t, p.Items[0].Reason, "an applied proposal already records the key guard-empty-datasets @ scripts")
		assert.False(t, p.Confirmable)
	})

	t.Run("another live pin carries the target key", func(t *testing.T) {
		f := retargetFixture(t)
		f.write(replaceOnce(t, retargetLessons, "  - rule: already-current\n",
			"  - rule: guard-empty-datasets\n    region: scripts\n    note: A hand-written twin.\n"+
				"  - rule: already-current\n"), 0o600)

		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Contains(t, p.Items[0].Reason, "another pin already carries the key guard-empty-datasets @ scripts")
	})

	t.Run("an applied row records the target as its raw key only", func(t *testing.T) {
		f := retargetFixture(t)

		// The region fields of p7 disagree. Its proposal key is two-areas @
		// db, and its raw stored key is two-areas @ api, db. An older reader
		// links a pin by the raw key, so that key counts as taken too.
		f.propose(model.Proposal{Signature: "s7", Rule: "two-areas", Region: "api", Regions: []string{"db"},
			Note: "Other.", Members: []int64{3}, Status: model.ProposalApplied})

		p := f.plan(f.deps(), f.request(ActionRetarget, "two-areas"))
		require.Len(t, p.Items, 1)
		assert.Equal(t, ItemBlocked, p.Items[0].Status)
		assert.Contains(t, p.Items[0].Reason, "an applied proposal already records the key two-areas @ api, db")
		assert.False(t, p.Confirmable)
	})

	t.Run("two selected pins target one key", func(t *testing.T) {
		f := newFixture(t, retargetFindings)
		f.write(`pin:
  - rule: twin-rule
    region: web
    note: First.
  - rule: twin-rule
    region: lib
    note: Second.
`, 0o644)
		f.propose(
			model.Proposal{Signature: "a", Rule: "twin-rule", Region: "web", Note: "First.", Members: []int64{1, 2},
				Status: model.ProposalApplied},
			model.Proposal{Signature: "b", Rule: "twin-rule", Region: "lib", Note: "Second.", Members: []int64{1, 2},
				Status: model.ProposalApplied},
		)

		p := f.plan(f.deps(), f.request(ActionRetarget, "twin-rule@web", "twin-rule@lib"))
		require.Len(t, p.Items, 2)

		for _, it := range p.Items {
			assert.Equal(t, ItemBlocked, it.Status)
			assert.Contains(t, it.Reason, "another selected entry targets the key twin-rule @ scripts")
		}

		assert.Nil(t, p.Change)
		assert.False(t, p.Confirmable)
	})
}

// replaceOnce replaces the one occurrence of old in s.
func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()

	i := indexOnce(t, s, old)

	return s[:i] + repl + s[i+len(old):]
}

// indexOnce returns the index of the one occurrence of sub in s.
func indexOnce(t *testing.T, s, sub string) int {
	t.Helper()

	first := -1

	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			require.Equal(t, -1, first, "%q occurs more than once", sub)

			first = i
		}
	}

	require.GreaterOrEqual(t, first, 0, "%q does not occur", sub)

	return first
}

func TestNewPlanGateOffShowsTheChangeButNeverWrites(t *testing.T) {
	for _, action := range []Action{ActionRetarget, ActionPrune} {
		t.Run(action.String(), func(t *testing.T) {
			f := retargetFixture(t)
			f.gate(false)

			req := f.request(action, "Guard-Empty-Datasets")
			p := f.plan(f.deps(), req)

			assert.False(t, p.Write)
			require.NoError(t, p.GateErr)
			assert.False(t, p.Confirmable)
			assert.Equal(t, []string{"distill.write is off: this preview changes nothing; turn on " +
				"Settings › Pin writing and preview again"}, p.Blocked)
			assert.Equal(t, ItemReady, p.Items[0].Status)
			require.NotNil(t, p.Change, "the preview still shows the exact change")
			assert.NotEmpty(t, p.Change.Diff)

			// The preview also shows the exact ledger effect (D-14).
			require.Len(t, p.Items[0].Rows, 1)
			guard := store.NewRowGuard(p.Items[0].Rows[0])

			switch action {
			case ActionRetarget:
				assert.Equal(t, []store.RegionChange{{Guard: guard, Region: "scripts", Regions: []string{"scripts"}}},
					p.Retarget)
				assert.Empty(t, p.Supersede)
			case ActionPrune:
				assert.Equal(t, []store.RowGuard{guard}, p.Supersede)
				assert.Empty(t, p.Retarget)
			}

			_, err := Commit(f.deps(), p)
			require.ErrorIs(t, err, ErrWriteDisabled)

			// Turning the gate on does not authorize the old preview.
			f.gate(true)

			_, err = Commit(f.deps(), p)
			require.ErrorIs(t, err, ErrWriteDisabled)
			assert.Equal(t, retargetLessons, f.lessons())
			assert.Equal(t, retargetLedger, f.ledger())

			assert.True(t, f.plan(f.deps(), req).Confirmable, "a fresh preview is confirmable")
		})
	}
}

func TestNewPlanReadsTheGateStrictly(t *testing.T) {
	f := retargetFixture(t)
	f.config("distill: [\n")

	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
	require.Error(t, p.GateErr)
	assert.False(t, p.Write)
	assert.False(t, p.Confirmable)
	assert.Equal(t, []string{"distill.write cannot be read; fix .seamark/config.yaml first"}, p.Blocked)

	_, err := Commit(f.deps(), p)
	require.ErrorIs(t, err, ErrNotConfirmable)
	assert.Equal(t, retargetLessons, f.lessons())
}

func TestNewPlanRefusesInvalidRequests(t *testing.T) {
	f := retargetFixture(t)
	ref := f.ref("Guard-Empty-Datasets", "")

	cases := []struct {
		name string
		deps func() Deps
		req  Request
		want error
		text string
	}{
		{name: "no action", deps: f.deps, req: Request{Refs: []pinfile.EntryRef{ref}}, text: "maintain: unknown action Action(0)"},
		{name: "no entry", deps: f.deps, req: Request{Action: ActionPrune}, want: ErrNothingSelected},
		{
			name: "a cursor names one entry", deps: f.deps,
			req:  Request{Action: ActionPrune, Refs: []pinfile.EntryRef{ref, ref}, Cursor: true},
			text: "maintain: a cursor request names exactly one entry",
		},
		{
			name: "no root", req: Request{Action: ActionPrune, Refs: []pinfile.EntryRef{ref}},
			deps: func() Deps { d := f.deps(); d.Root = ""; return d },
			text: "maintain: workspace root is empty",
		},
		{
			name: "no ledger", req: Request{Action: ActionPrune, Refs: []pinfile.EntryRef{ref}},
			deps: func() Deps { d := f.deps(); d.Reader = nil; return d },
			want: ErrNoLedger,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPlan(tc.deps(), tc.req)
			require.Error(t, err)
			assert.Nil(t, p)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				assert.EqualError(t, err, tc.text)
			}
		})
	}
}

func TestNewPlanBindsTheReviewedEntry(t *testing.T) {
	f := retargetFixture(t)
	req := f.request(ActionRetarget, "Guard-Empty-Datasets")

	// Another writer edits the reviewed entry after the list was read.
	f.write(replaceOnce(t, retargetLessons, "HAND EDITED", "EDITED AGAIN"), 0o600)

	p := f.plan(f.deps(), req)
	require.Len(t, p.Items, 1)
	assert.Equal(t, ItemBlocked, p.Items[0].Status)
	assert.Equal(t, reasonChanged, p.Items[0].Reason)
	assert.Equal(t, Pin{}, p.Items[0].Pin, "the plan never shows another entry as the reviewed one")
	assert.False(t, p.Confirmable)
}

func TestNewPlanNamesTheCursorRow(t *testing.T) {
	f := retargetFixture(t)

	req := f.request(ActionPrune, "Guard-Empty-Datasets")
	req.Cursor = true

	p := f.plan(f.deps(), req)
	assert.Equal(t, "acts on the cursor row; no row is marked", p.Notes[0])
	assert.Equal(t, []string{
		"acts on the cursor row; no row is marked",
		"the linked proposal moves from applied to superseded; findings, signatures, and the firing log stay",
		"a superseded proposal is history; the console does not restore it",
		"the diff hides text that looks like a secret; the file gets the exact bytes",
	}, p.Notes)
}

func TestNewPlanBlocksEntriesWithoutOneLink(t *testing.T) {
	f := newFixture(t, retargetFindings)
	f.write(`pin:
  - rule: dup
    region: db
    note: First.
  - rule: dup
    region: db
    note: Second.
  - rule: two-rows
    region: lib
    note: Two rows.
  - rule: pending-one
    region: api
    note: Pending.
  - rule: mixed-fields
    region: web
    note: Mixed.
  - rule: flow-entry
    region: web
    regions:
      - web
      - lib
    note: |
      A note.
`, 0o644)
	f.propose(
		model.Proposal{Signature: "a", Rule: "dup", Region: "db", Members: []int64{1}, Status: model.ProposalApplied},
		model.Proposal{Signature: "b", Rule: "two-rows", Region: "lib", Members: []int64{1}, Status: model.ProposalApplied},
		model.Proposal{Signature: "c", Rule: "two-rows", Region: "lib", Members: []int64{2}, Status: model.ProposalApplied},
		model.Proposal{Signature: "d", Rule: "pending-one", Region: "api", Members: []int64{1}, Status: model.ProposalProposed},
		model.Proposal{Signature: "e", Rule: "mixed-fields", Region: "lib", Regions: []string{"web"}, Members: []int64{1},
			Status: model.ProposalApplied},
	)

	p := f.plan(f.deps(), f.request(ActionPrune, "dup", "two-rows", "pending-one", "mixed-fields"))
	require.Len(t, p.Items, 4)

	assert.Equal(t, "ambiguous: duplicate pins; "+reasonLedgerFixes, p.Items[0].Reason)
	assert.Equal(t, "ambiguous: several applied proposals; "+reasonLedgerFixes, p.Items[1].Reason)
	assert.Equal(t, "matches a pending proposal; record it as applied first (Proposals › c)", p.Items[2].Reason)
	assert.Equal(t, "ambiguous: stored region fields disagree; "+reasonLedgerFixes, p.Items[3].Reason)

	for _, it := range p.Items {
		assert.Equal(t, ItemBlocked, it.Status)
		assert.Empty(t, it.Rows, "a blocked entry plans no ledger change")
	}

	assert.Nil(t, p.Change)
	assert.Empty(t, p.Supersede)
	assert.False(t, p.Confirmable)
}

func TestNewPlanBlocksAnEntryThatCannotBeEditedSafely(t *testing.T) {
	f := newFixture(t, retargetFindings)
	f.write("pin:\n  - {rule: flow-item, region: web, note: Flow.}\n", 0o644)
	f.propose(model.Proposal{Signature: "a", Rule: "flow-item", Region: "web", Members: []int64{1},
		Status: model.ProposalApplied})

	p := f.plan(f.deps(), f.request(ActionPrune, "flow-item"))
	require.Len(t, p.Items, 1)
	assert.Equal(t, ItemBlocked, p.Items[0].Status)
	assert.Contains(t, p.Items[0].Reason, "cannot edit this entry safely: ")
	assert.Contains(t, p.Items[0].Reason, "; edit .seamark/lessons.yaml by hand")
}

func TestNewPlanKeepsFileTextInAReasonRaw(t *testing.T) {
	f := newFixture(t, retargetFindings)

	// A hand-edited rule holds terminal control bytes, and a twin entry
	// already carries the computed target key.
	f.write("pin:\n"+
		"  - rule: \"guard\\e]0;pwned\\a\"\n    region: '*'\n    note: Guard.\n"+
		"  - rule: \"guard\\e]0;pwned\\a\"\n    region: scripts\n    note: A hand-written twin.\n", 0o644)
	f.propose(model.Proposal{Signature: "s1", Rule: "guard\x1b]0;pwned\a", Note: "Guard.", Members: []int64{1, 2},
		Status: model.ProposalApplied})

	p := f.plan(f.deps(), f.request(ActionRetarget, "guard\x1b]0;pwned\a"))
	require.Len(t, p.Items, 1)
	assert.Equal(t, ItemBlocked, p.Items[0].Status)

	// The reason quotes the rule byte for byte. The package doc names
	// Reason as text that the caller sanitizes; this pins that contract.
	assert.Contains(t, p.Items[0].Reason, "another pin already carries the key guard\x1b]0;pwned\a @ scripts")
	assert.Contains(t, p.Items[0].Reason, "\x1b")
}

func TestNewPlanBlocksAnUnreadablePinFile(t *testing.T) {
	cases := []struct {
		name, tail, want string
	}{
		{
			name: "a value error without a line", tail: "hook_delivery: sometimes\n",
			want: "pin file unreadable; fix .seamark/lessons.yaml first",
		},
		{
			name: "a type error with its line", tail: "threshold: lots\n",
			want: "pin file unreadable; fix .seamark/lessons.yaml first (line 28)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := retargetFixture(t)
			req := f.request(ActionPrune, "Guard-Empty-Datasets")
			f.write(retargetLessons+tc.tail, 0o600)

			p := f.plan(f.deps(), req)
			require.Len(t, p.Items, 1)
			assert.Equal(t, ItemBlocked, p.Items[0].Status)
			assert.Equal(t, tc.want, p.Items[0].Reason)
			assert.False(t, p.Confirmable)
		})
	}
}

func TestNewPlanComputesTheChangeOfALinkedFile(t *testing.T) {
	f := retargetFixture(t)

	// The pin file is a symbolic link to a file outside .seamark.
	target := filepath.Join(f.root, "real-lessons.yaml")
	require.NoError(t, os.Rename(f.pinPath(), target))
	require.NoError(t, os.Symlink(target, f.pinPath()))

	p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets"))
	assert.Equal(t, ItemReady, p.Items[0].Status)
	require.NotNil(t, p.Change, "the preview shows the exact change")
	assert.Contains(t, p.FileBlock, "symlink at .seamark/lessons.yaml")
	assert.False(t, p.Confirmable)
	assert.Contains(t, p.Blocked, "the pin file cannot be changed: "+p.FileBlock)

	_, err := Commit(f.deps(), p)
	require.ErrorIs(t, err, ErrNotConfirmable)
}

func TestNewPlanWorksOnAReadOnlyStore(t *testing.T) {
	f := retargetFixture(t)

	ro, err := store.OpenReadOnly(store.DefaultPath(f.root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })

	deps := f.deps()
	deps.Reader = ro

	p := f.plan(deps, f.request(ActionRetarget, "Guard-Empty-Datasets", "two-areas"))
	assert.True(t, p.Confirmable, "blocked: %v", p.Blocked)
	assert.Equal(t, f.plan(f.deps(), p.Request).Evidence, p.Evidence, "both readers see the same evidence")
}

func TestFactLinesExplainTheScope(t *testing.T) {
	cases := []struct {
		name   string
		advice distill.ScopeAdvice
		living int
		want   []string
	}{
		{
			name:   "coverage",
			advice: distill.ScopeAdvice{Regions: []string{"scripts"}},
			living: 2,
			want:   []string{"no confirmed trigger: the scope is the coverage of 2 living finding(s)"},
		},
		{
			name: "direct and co-changed triggers",
			advice: distill.ScopeAdvice{Facts: []distill.TriggerFact{
				{Path: "api/a.go", Region: "api/a.go", Direct: true, Selected: true},
				{Path: "db/q.go", Region: "db/q.go", Together: 7, Selected: true},
			}},
			want: []string{
				"trigger api/a.go is cited evidence; it delivers to api/a.go",
				"trigger db/q.go changed together with the evidence in 7 commit(s); it delivers to db/q.go",
			},
		},
		{
			name: "unconfirmed and blocked triggers",
			advice: distill.ScopeAdvice{RepoWide: true, Facts: []distill.TriggerFact{
				{Path: "x/y.go"},
				{Path: "z/w.go", Region: "z/w.go", Direct: true},
			}},
			living: 4,
			want: []string{
				"trigger x/y.go is not confirmed by the evidence or the history; it does not set the scope",
				distill.TriggerFact{Path: "z/w.go", Region: "z/w.go", Direct: true}.BlockedLine(),
				"no confirmed trigger: the scope is the coverage of 4 living finding(s); " +
					"they are too scattered for a region, so the scope is repository-wide",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, factLines(tc.advice, tc.living))
		})
	}
}

func TestEnumStrings(t *testing.T) {
	assert.Equal(t, "retarget", ActionRetarget.String())
	assert.Equal(t, "prune", ActionPrune.String())
	assert.Equal(t, "Action(0)", Action(0).String())
	assert.Equal(t, "blocked", ItemBlocked.String())
	assert.Equal(t, "no-op", ItemNoOp.String())
	assert.Equal(t, "ready", ItemReady.String())
	assert.Equal(t, "ItemStatus(9)", ItemStatus(9).String())
	assert.Equal(t, ItemBlocked, PlanItem{}.Status, "an unset item is never ready")
}
