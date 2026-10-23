package maintain

// Tests of the lesson-check keys in the maintenance writers (contract
// K1, writer table, AC-27). A retarget, a prune, and a manual edit
// never set and never clear the mode or the check of a pin,
// and a manual create writes an advisory pin. Every candidate passes the
// strict check tier of pinfile.Prepare, so each test also proves that
// the writer does not refuse a valid file with checks.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// checkWritersLessons holds the check definitions of the spec example
// and four pins. guard-scripts and two-areas are linked to p1 and p2,
// so a retarget can move them. ruff-unused-import is linked to p3, and
// it is the only pin bound to frozen-assets. The hand-written pin has no
// ledger row. The comment on the mode line must stay after every write.
const checkWritersLessons = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: guard-scripts
    region: '*'
    note: Guard the scripts.
    mode: enforce
    check: no-todo
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p2)
  - rule: two-areas
    region: api
    note: Two areas.
    mode: check     # keep this comment
    check: no-todo
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p3)
  - rule: ruff-unused-import
    region: scripts
    note: Remove unused imports in scripts.
    mode: enforce
    check: frozen-assets
  - rule: hand-written
    region: web
    note: A hand-written pin without a ledger row.
    check: no-todo
mute:
  - rule: noisy-rule
`

// checkWritersFixture seeds checkWritersLessons with the evidence of the
// retarget goldens: p1 computes [scripts], and p2 widens to [api, db].
func checkWritersFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, retargetFindings)
	f.write(checkWritersLessons, 0o600)
	f.propose(
		model.Proposal{Signature: "s1", Rule: "guard-scripts", Note: "Guard the scripts.",
			Members: []int64{1, 2}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "two-areas", Region: "api", Note: "Two areas.",
			Members: []int64{3, 4}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s3", Rule: "ruff-unused-import", Region: "scripts",
			Note: "Remove unused imports in scripts.", Members: []int64{1, 2}, Agent: "claude/v2",
			Status: model.ProposalApplied},
	)

	return f
}

// pinChecks returns "rule mode=<mode> check=<check>" for each pin of
// text, in file order. The mode is the normalized mode of lessoncheck,
// so a pin without a mode key reads advisory. It requires that text
// passes the strict check tier. Without that rule, a writer that refuses
// its candidate looks like a writer that drops the keys.
func pinChecks(t *testing.T, text string) []string {
	t.Helper()

	cfg, errs, err := lessoncheck.Parse([]byte(text))
	require.NoError(t, err)
	require.Empty(t, errs)

	out := make([]string, 0, len(cfg.Pins))
	for _, p := range cfg.Pins {
		out = append(out, fmt.Sprintf("%s mode=%s check=%s", p.Rule.Rule, p.Mode, p.CheckID))
	}

	return out
}

// checkWritersPins is pinChecks of checkWritersLessons.
var checkWritersPins = []string{
	"guard-scripts mode=enforce check=no-todo",
	"two-areas mode=check check=no-todo",
	"ruff-unused-import mode=enforce check=frozen-assets",
	"hand-written mode=advisory check=no-todo",
}

// TestRetargetKeepsModeAndCheck proves the K1 writer row "Retarget". A
// retarget changes only the region keys of each entry. The mode, the
// check, the comment on the mode line, and the checks map stay byte for
// byte, also for an executable pin.
func TestRetargetKeepsModeAndCheck(t *testing.T) {
	f := checkWritersFixture(t)
	require.Equal(t, checkWritersPins, pinChecks(t, checkWritersLessons))

	p := f.plan(f.deps(), f.request(ActionRetarget, "guard-scripts", "two-areas"))
	require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
	require.Len(t, p.Items, 2)
	assert.Equal(t, []string{"scripts"}, p.Items[0].To)
	assert.Equal(t, []string{"api", "db"}, p.Items[1].To)

	res, err := Commit(f.deps(), p)
	require.NoError(t, err)
	require.NoError(t, res.FileErr)
	assert.True(t, res.FileWritten)
	assert.True(t, res.LedgerDone)

	want := strings.Replace(checkWritersLessons,
		"  - rule: guard-scripts\n    region: '*'\n",
		"  - rule: guard-scripts\n    region: scripts\n", 1)
	want = strings.Replace(want,
		"  - rule: two-areas\n    region: api\n",
		"  - rule: two-areas\n    region: api\n    regions: [api, db]\n", 1)
	assert.Equal(t, want, f.lessons())
	assert.Equal(t, checkWritersPins, pinChecks(t, f.lessons()))
}

// TestPruneKeepsChecksMap proves the K1 writer row "Prune". A prune
// removes only the entry and its provenance comment. The checks map
// stays byte for byte. This includes the definition that no pin binds
// after the prune, because the strict tier accepts an unused check. A
// ledger failure restores the original bytes through pinWriter.Restore
// (the row "Compensation").
func TestPruneKeepsChecksMap(t *testing.T) {
	pruned := strings.Replace(checkWritersLessons, `  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p3)
  - rule: ruff-unused-import
    region: scripts
    note: Remove unused imports in scripts.
    mode: enforce
    check: frozen-assets
`, "", 1)
	require.NotEqual(t, checkWritersLessons, pruned, "the golden anchor must occur")

	t.Run("the prune writes", func(t *testing.T) {
		f := checkWritersFixture(t)

		p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)

		res, err := Commit(f.deps(), p)
		require.NoError(t, err)
		require.NoError(t, res.FileErr)
		assert.True(t, res.LedgerDone)

		assert.Equal(t, pruned, f.lessons())
		assert.Contains(t, f.lessons(), "checks:\n  frozen-assets:\n    kind: forbid-path-change/1\n",
			"the unused definition stays")
		assert.Equal(t, []string{checkWritersPins[0], checkWritersPins[1], checkWritersPins[3]},
			pinChecks(t, f.lessons()))
	})

	t.Run("a ledger failure restores the checks map", func(t *testing.T) {
		f := checkWritersFixture(t)
		p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import"))

		deps := f.deps()
		deps.Ledger = &ledgerSeam{Store: f.st, err: errors.New("injected: database is locked")}

		res, err := Commit(deps, p)
		require.NoError(t, err)
		assert.True(t, res.FileWritten)
		assert.False(t, res.LedgerDone)
		assert.True(t, res.Restored)
		require.NoError(t, res.RestoreErr)

		assert.Equal(t, checkWritersLessons, f.lessons())
		assert.Equal(t, checkWritersPins, pinChecks(t, f.lessons()))
	})
}

// checksAuthorLessons holds two executable pins without a ledger row,
// one with a region set, and settings around the list.
const checksAuthorLessons = `threshold: 3
lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  # a person wrote this one
  - rule: Manual-Rule
    region: web
    regions: [web, docs]
    note: Keep web and docs tidy.
    mode: enforce
    check: frozen-assets
  - rule: todo-free
    region: web
    note: Do not add TODO markers.
    mode: check     # keep this comment
    check: no-todo
mute:
  - rule: noisy
`

// checksAuthorFixture is a workspace with checksAuthorLessons and the
// gate on. The directory web exists, docs does not.
func checksAuthorFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, nil, "web/index.html")
	f.write(checksAuthorLessons, 0o644)

	return f
}

// TestAuthorCreateIsAdvisory proves the K1 writer row "Manual create"
// and AC-27. pinfile.RenderPin writes the rule, the regions, and the
// note only, so a new pin is advisory. The executable pins and the
// checks map stay byte for byte.
func TestAuthorCreateIsAdvisory(t *testing.T) {
	f := checksAuthorFixture(t)

	p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance.", "web"))
	require.True(t, p.Confirmable, "blocked: %v issues: %v", blockerLines(p), p.Issues)

	res, err := CommitAuthor(f.deps(), p)
	require.NoError(t, err)
	require.NoError(t, res.Err)
	assert.Equal(t, Written, res.Outcome)

	assert.Equal(t, strings.Replace(checksAuthorLessons, "mute:\n",
		"  - rule: new-rule\n    region: web\n    note: Guidance.\n"+"mute:\n", 1), f.lessons())
	assert.Equal(t, []string{
		"Manual-Rule mode=enforce check=frozen-assets",
		"todo-free mode=check check=no-todo",
		"new-rule mode=advisory check=",
	}, pinChecks(t, f.lessons()))

	d, err := pinfile.Read(f.root)
	require.NoError(t, err)

	created := d.Entries[len(d.Entries)-1]
	assert.Empty(t, created.Mode, "no mode key: the pin is advisory")
	assert.Empty(t, created.Check, "no check key")
}

// TestAuthorEditKeepsModeAndCheck proves the K1 writer row "Manual
// edit" of milestone P1. The form sets the rule, the note, or the scope,
// and the planner routes them to a Rewrite without a mode and a check.
// So the entry keeps both keys, and the comment on the mode line stays.
func TestAuthorEditKeepsModeAndCheck(t *testing.T) {
	cases := []struct {
		name, rule string
		req        func(ref pinfile.EntryRef) AuthorRequest
		old, new   string
	}{
		{
			name: "the note", rule: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, "Keep web and docs clean.") },
			old: "    note: Keep web and docs tidy.\n", new: "    note: Keep web and docs clean.\n",
		},
		{
			name: "the rule", rule: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Rule: "renamed-rule", RuleSet: true}
			},
			old: "  - rule: Manual-Rule\n", new: "  - rule: renamed-rule\n",
		},
		{
			name: "the scope", rule: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"./web/"}, ScopeSet: true}
			},
			old: "    region: web\n    regions: [web, docs]\n", new: "    region: web\n",
		},
		{
			name: "repository-wide", rule: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRepoWide, ScopeSet: true}
			},
			old: "    region: web\n    regions: [web, docs]\n", new: "    region: '*'\n",
		},
		{
			name: "the note above a commented mode", rule: "todo-free",
			req: func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, "Never add TODO markers.") },
			old: "    note: Do not add TODO markers.\n", new: "    note: Never add TODO markers.\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := checksAuthorFixture(t)

			p := f.authorPlan(f.deps(), tc.req(f.ref(tc.rule, "")))
			require.True(t, p.Confirmable, "blocked: %v issues: %v", blockerLines(p), p.Issues)

			res, err := CommitAuthor(f.deps(), p)
			require.NoError(t, err)
			require.NoError(t, res.Err)
			assert.Equal(t, Written, res.Outcome)

			want := strings.Replace(checksAuthorLessons, tc.old, tc.new, 1)
			require.NotEqual(t, checksAuthorLessons, want, "the golden anchor must occur")
			assert.Equal(t, want, f.lessons())

			// The edit keeps the mode and the check of both pins. A rule
			// edit renames the first one, so the rule column follows it.
			rule := "Manual-Rule"
			if strings.Contains(tc.new, "renamed-rule") {
				rule = "renamed-rule"
			}

			assert.Equal(t, []string{
				rule + " mode=enforce check=frozen-assets",
				"todo-free mode=check check=no-todo",
			}, pinChecks(t, f.lessons()))
		})
	}
}

// impactHead returns the expected first line and version line of the
// impact block of the pin at entry n: the pin-ref and the rule before
// the edit, the check, the mode, and both pin versions. The parts come
// from reviews, whose vectors the reviews tests fix.
func impactHead(t *testing.T, before, after []byte, n int, check, mode string) (head, version string) {
	t.Helper()

	b, err := reviews.ParseConfig(before)
	require.NoError(t, err)

	a, err := reviews.ParseConfig(after)
	require.NoError(t, err)

	old, next := b.Pin[n-1], a.Pin[n-1]
	ref := b.ShortPinRef(reviews.NewPinKey(old.Rule, old.Region, old.Regions))
	def := a.Checks[next.Check]

	head = fmt.Sprintf("pin %s %s: check %s (%s, %s), mode %s",
		ref, old.Rule, check, def.Kind, reviews.CheckVersion(def)[:12], mode)
	version = fmt.Sprintf("  version %s -> %s", b.PinVersion(old)[:12], a.PinVersion(next)[:12])

	return head, version
}

// forbidPredicate and todoPredicate are the Describe texts of the two
// definitions of the fixtures.
const (
	forbidPredicate = "  predicate: violation if any change touches a file in scope: an add, a content change, " +
		"a deletion, a file mode or type change, or either end of a move; content is not read"
	todoPredicate = "  predicate: violation if a line that a change adds in scope matches the Go regular " +
		"expression 'TODO\\('; a new file adds every line; binary or unreadable content gives unknown"
)

// TestRetargetShowsCheckImpact proves the K1 impact of a retarget
// (writer table, "Retarget"). The plan carries one impact block per
// executable pin whose regions move, before any write, also while the
// gate is off. An advisory pin and a prune get no block, so every
// existing retarget and prune preview keeps its bytes.
func TestRetargetShowsCheckImpact(t *testing.T) {
	t.Run("executable pins", func(t *testing.T) {
		f := checkWritersFixture(t)

		p := f.plan(f.deps(), f.request(ActionRetarget, "guard-scripts", "two-areas"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
		require.NotNil(t, p.Change)
		require.Len(t, p.Impact, 2)

		head1, version1 := impactHead(t, p.Change.Before, p.Change.After, 1, "no-todo", "enforce")
		head2, version2 := impactHead(t, p.Change.Before, p.Change.After, 2, "no-todo", "check")

		assert.Equal(t, []string{
			head1,
			"  scope repository-wide -> scripts",
			todoPredicate,
			version1,
			"  active: requested; inactive: not-installed",
			head2,
			"  scope api -> api, db",
			todoPredicate,
			version2,
			"  active: requested; inactive: not-installed",
		}, lessoncheck.ImpactLines(p.Impact))

		assert.Equal(t, checkWritersLessons, f.lessons(), "a plan writes nothing")
	})

	t.Run("the gate-off preview carries the block", func(t *testing.T) {
		f := checkWritersFixture(t)
		f.gate(false)

		p := f.plan(f.deps(), f.request(ActionRetarget, "two-areas"))
		assert.False(t, p.Confirmable)
		require.Len(t, p.Impact, 1)
		assert.Equal(t, 2, p.Impact[0].Entry)
		assert.Equal(t, lessoncheck.ModeCheck, p.Impact[0].Mode)
	})

	// A pin that does not pair with the pin list must never read as
	// advisory. The editor blocks the known causes, such as an empty
	// item, so a hand-built change proves the error.
	t.Run("an edit that does not pair is an error", func(t *testing.T) {
		doc := pinfile.Parse([]byte(checkWritersLessons))
		ref := doc.Entries[1].Ref()

		_, err := checkImpact(doc, &pinfile.Change{
			Before: []byte(checkWritersLessons), After: []byte(checkWritersLessons),
			Edits:   []pinfile.Edit{{Ref: ref, Regions: []string{"api"}}},
			NewKeys: []reviews.PinKey{reviews.NewPinKey("another-rule", "api", nil)},
		})
		require.Error(t, err)

		impacts, err := checkImpact(doc, &pinfile.Change{
			Before: []byte(checkWritersLessons), After: []byte(checkWritersLessons),
			Edits: []pinfile.Edit{{Ref: ref, Regions: []string{"api"}}}, NewKeys: []reviews.PinKey{ref.Key},
		})
		require.NoError(t, err)
		require.Len(t, impacts, 1)
		assert.Equal(t, 2, impacts[0].Entry)
	})

	t.Run("an advisory pin gets no block", func(t *testing.T) {
		f := retargetFixture(t)

		p := f.plan(f.deps(), f.request(ActionRetarget, "Guard-Empty-Datasets", "two-areas"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
		assert.Empty(t, p.Impact)
	})

	t.Run("a prune gets no block", func(t *testing.T) {
		f := checkWritersFixture(t)

		p := f.plan(f.deps(), f.request(ActionPrune, "ruff-unused-import"))
		require.True(t, p.Confirmable, "blocked: %v", p.Blocked)
		assert.Empty(t, p.Impact)
	})
}
