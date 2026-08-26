package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// seedStore builds a minimal index: one symbol in scripts/task.py plus a
// set of review lessons, enough to exercise lesson surfacing.
func seedStore(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	var err error
	st, err = store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	sym := model.Symbol{
		FQN: "scripts/task.run", Name: "run", Kind: model.KindFunction,
		File: "scripts/task.py", Span: model.Span{StartLine: 1, EndLine: 5},
	}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertSymbol(&sym)
	}))

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "scripts\x00RUF001", Region: "scripts", Reviewer: "coderabbit",
			Symptom: "RUF001", Occurrences: 6, LastTS: 100,
		},
		{
			ClusterKey: "scripts\x00once", Region: "scripts", Reviewer: "human",
			Symptom: "solitary finding", Occurrences: 1, LastTS: 50,
		},
	}, nil))

	return st, root
}

func TestWhySurfacesRegionLessons(t *testing.T) {
	st, root := seedStore(t)

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "scripts/task.py"))

	out := b.String()
	assert.Contains(t, out, "reviewers keep flagging")
	assert.Contains(t, out, "RUF001")
	assert.Contains(t, out, "×6")
	assert.NotContains(t, out, "solitary finding", "a single comment is below the recurrence threshold")
	assert.Contains(t, out, "expand lessons:scripts", "the raw-ledger ref is advertised")
}

func TestOrientSurfacesTopLessons(t *testing.T) {
	st, root := seedStore(t)

	var b strings.Builder
	require.NoError(t, Orient(&b, st, root))

	out := b.String()
	assert.Contains(t, out, "review lessons")          // the index summary line
	assert.Contains(t, out, "reviewers keep flagging") // the ranked section
	assert.Contains(t, out, "RUF001")
}

func TestFixDensityLine(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	commits := []struct{ title, body string }{
		{"fix: nil deref in loader", ""},
		{"add feature", ""},
		{"fix(api): wrong status code", ""},
		{"refactor helpers", ""},
		{"docs update", ""},
		{"extend config", ""},
		// Classified by its BODY at mining time; the density must agree.
		{"harden worker", "Fixes #12"},
	}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for i, c := range commits {
			kind := model.DecisionCommit
			if i == 5 {
				kind = model.DecisionRevert // reverts count as corrections too
			}

			err := tx.InsertDecision(&model.Decision{
				Kind: kind, Ref: fmt.Sprintf("sha%02d", i), TS: int64(1000 - i),
				Title: c.title, Body: c.body, Files: []string{"api/hot.go"},
			})
			if err != nil {
				return err
			}
		}

		return nil
	}))

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "api/hot.go"))

	assert.Contains(t, b.String(), "fix density  4 of the last 7 commits here were fixes",
		"two subject fixes, one revert, one body-only issue link")
}

func TestPinnedNoteSurvivesUntruncated(t *testing.T) {
	// A pin's note IS the guidance: the template promises it is "shown to
	// the agent verbatim", and on a real repo a hard 34-char cut reduced
	// a curated 10-finding pin to "Adding a fie…" — the agent never saw
	// the instruction. Lesson text must reach every surface whole.
	note := "Adding a field to a pooled struct? Reset it in Free() and " +
		"deep-copy it in clone(). Reviewers have flagged this ten times."
	lessons := []model.Lesson{
		{
			Region: "engine/resolve", Reviewer: "pinned",
			Symptom: "pooled-state-reset — " + note, Occurrences: 1 << 30,
		},
	}

	var b strings.Builder
	require.NoError(t, PrintLessonReminder(&b, "engine/resolve/context.go", lessons, 0))

	assert.Contains(t, b.String(), note, "the full note reaches the agent")
	assert.NotContains(t, b.String(), "…", "no ellipsis truncation on lesson text")
}

func TestPinBudgetAboveSurfaceCapStaysAccounted(t *testing.T) {
	// pin_budget: 20 must not let the surface's total cap (8) eat pins
	// silently — the excess has to land in the trimmed count so the
	// pointer line still tells the truth.
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	cfg := reviews.DefaultConfig()
	cfg.PinBudget = 20

	// Distinct topics: the ambient path collapses restated pins before
	// budgeting, and this test is about budget arithmetic, not dedup.
	for _, rule := range []string{
		"alpha-guard", "bravo-guard", "charlie-guard", "delta-guard", "echo-guard",
		"foxtrot-guard", "golf-guard", "hotel-guard", "india-guard", "juliet-guard",
	} {
		cfg.Pin = append(cfg.Pin, reviews.PinRule{Rule: rule, Region: "*", Note: "n"})
	}

	out, trimmed, err := LessonsForScopeBudget(st, cfg, "a/x.go", 8, cfg.HookPinBudget())
	require.NoError(t, err)
	assert.Len(t, out, 8)
	assert.Equal(t, 2, trimmed, "pins beyond the surface cap are counted, not silently dropped")
}

func TestReminderPointsAtBudgetedPins(t *testing.T) {
	lessons := []model.Lesson{
		{Region: "a", Reviewer: "pinned", Symptom: "r — most specific wins", Occurrences: 1 << 30},
	}

	var b strings.Builder
	require.NoError(t, PrintLessonReminder(&b, "a/x.go", lessons, 4))

	assert.Contains(t, b.String(), "+4 more pins for this area",
		"budgeted pins are pointed at, never silently dropped")
	assert.Contains(t, b.String(), "seamark lessons --file a/x.go")
}

func TestLessonSymptomSanitized(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Symptom text originates in untrusted comment bodies; a control
	// byte must not survive into rendered output.
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "k", Region: "x", Reviewer: "bot",
			Symptom: "bad\x1b[31mansi", Occurrences: 3, LastTS: 1,
		},
	}, nil))

	var b strings.Builder
	require.NoError(t, Orient(&b, st, root))

	assert.NotContains(t, b.String(), "\x1b", "escape sequences must be washed out")
}

func TestCapturedThemesDontRideTheMinedChannel(t *testing.T) {
	// An applied pin captures a theme; the mined lessons its citations
	// came from must stop surfacing — otherwise a theme whose pin lost
	// the injection budget still rides in through the mined channel, and
	// a shown pin gets said twice. Coverage demands the WHOLE cluster
	// cited and the pin live in lessons.yaml (both asserted below).
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "k-creds", Region: "scripts", Reviewer: "copilot",
			Symptom: "hard codes a postgres url including credentials", Occurrences: 2,
		},
		{
			ClusterKey: "k-ruff", Region: "scripts", Reviewer: "coderabbit",
			Symptom: "RUF003", Occurrences: 2,
		},
	}, []model.Finding{
		{ID: 1, LessonKey: "k-creds", Path: "scripts/db.py", Body: "creds", Source: model.SourceReview},
		{ID: 2, LessonKey: "k-creds", Path: "scripts/etl.py", Body: "creds again", Source: model.SourceReview},
		{ID: 3, LessonKey: "k-ruff", Path: "scripts/x.py", Body: "unicode dash", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-a", "scripts", 1, []model.Proposal{{
		Signature: "sig-a", Rule: "hardcoded-db-credentials", Region: "scripts",
		Note: "Read credentials from the environment.", Members: []int64{1, 2},
		Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	require.Len(t, saved, 1)

	// The pin as `--apply` writes it into lessons.yaml.
	pinned := reviews.DefaultConfig()
	pinned.Pin = []reviews.PinRule{{
		Rule: "hardcoded-db-credentials", Region: "scripts",
		Note: "Read credentials from the environment.",
	}}

	symptoms := func(cfg *reviews.Config) string {
		out, _, err := LessonsForScopeBudget(st, cfg, "scripts/foo.py", 8, 3)
		require.NoError(t, err)

		var b strings.Builder
		for _, l := range out {
			b.WriteString(l.Symptom)
			b.WriteByte('\n')
		}

		return b.String()
	}

	// Merely proposed: nothing is captured yet, both lessons surface.
	assert.Contains(t, symptoms(pinned), "postgres")
	assert.Contains(t, symptoms(pinned), "RUF003")

	// Applied with the pin live: the credentials cluster is captured —
	// its two findings are exactly the citations; the unrelated
	// cluster stays.
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)
	assert.NotContains(t, symptoms(pinned), "postgres", "a captured theme must not surface as a mined lesson")
	assert.Contains(t, symptoms(pinned), "RUF003", "uncaptured recurrence keeps surfacing")

	// Pin hand-removed from lessons.yaml (the distill.write:false prune
	// flow leaves the proposal applied): the lesson must resurface —
	// zero surfacings is worse than two.
	assert.Contains(t, symptoms(reviews.DefaultConfig()), "postgres",
		"a manually pruned pin resurfaces its source lesson")
}

func TestPartialCitationDoesNotCoverACluster(t *testing.T) {
	// Citing one member proves origin, not that the pin subsumes the
	// cluster: one comment can flag two mistakes, and a recurrence
	// arriving after the pin was applied re-opens the lesson.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "k", Region: "api", Reviewer: "human",
			Symptom: "wrap engine context", Occurrences: 3,
		},
	}, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "api/a.go", Body: "one", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "api/b.go", Body: "two", Source: model.SourceReview},
		{ID: 3, LessonKey: "k", Path: "api/c.go", Body: "post-pin recurrence", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-c", "api", 1, []model.Proposal{{
		Signature: "sig-c", Rule: "wrap-engine-context", Region: "api",
		Note: "Wrap the engine context at every call site.", Members: []int64{1, 2},
		Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{{Rule: "wrap-engine-context", Region: "api", Note: "n"}}

	out, _, err := LessonsForScopeBudget(st, cfg, "api/z.go", 8, 0)
	require.NoError(t, err)

	var b strings.Builder
	for _, l := range out {
		b.WriteString(l.Symptom)
		b.WriteByte('\n')
	}

	assert.Contains(t, b.String(), "wrap engine context",
		"a cluster with an uncited finding is not fully captured and keeps surfacing")
}

func TestDismissedProposalsDontSuppressMinedLessons(t *testing.T) {
	// A dismissal rejects the distilled guidance, not the raw
	// recurrence — mute is the tool for hiding that.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "k", Region: "api", Reviewer: "human",
			Symptom: "wrap engine context", Occurrences: 3,
		},
	}, []model.Finding{
		{ID: 9, LessonKey: "k", Path: "api/a.go", Body: "wrap it", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-b", "api", 1, []model.Proposal{{
		Signature: "sig-b", Rule: "wrap-engine-context", Region: "api",
		Note: "Wrap the engine context at every call site.", Members: []int64{9},
		Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalDismissed)
	require.NoError(t, err)

	out, _, err := LessonsForScopeBudget(st, reviews.DefaultConfig(), "api/b.go", 8, 3)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Contains(t, out[0].Symptom, "wrap engine context")
}

func TestSplitCitationsAcrossPinsDoNotCover(t *testing.T) {
	// Two live pins each citing half a cluster: the union covers it,
	// but neither pin carries the theme — the lesson must keep
	// surfacing.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{
			ClusterKey: "k", Region: "api", Reviewer: "human",
			Symptom: "wrap engine context", Occurrences: 2,
		},
	}, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "api/a.go", Body: "one", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "api/b.go", Body: "two", Source: model.SourceReview},
	}))

	first, err := st.SaveDistilledGroup("sig-x", "api", 1, []model.Proposal{{
		Signature: "sig-x", Rule: "alpha-theme", Region: "api",
		Note: "First unrelated theme.", Members: []int64{1}, Status: model.ProposalProposed,
	}})
	require.NoError(t, err)

	second, err := st.SaveDistilledGroup("sig-y", "api", 1, []model.Proposal{{
		Signature: "sig-y", Rule: "beta-theme", Region: "api",
		Note: "Second unrelated theme.", Members: []int64{2}, Status: model.ProposalProposed,
	}})
	require.NoError(t, err)

	_, err = st.SetProposalStatus([]int64{first[0].ID, second[0].ID}, model.ProposalApplied)
	require.NoError(t, err)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "alpha-theme", Region: "api", Note: "n"},
		{Rule: "beta-theme", Region: "api", Note: "n"},
	}

	out, _, err := LessonsForScopeBudget(st, cfg, "api/z.go", 8, 0)
	require.NoError(t, err)

	var b strings.Builder
	for _, l := range out {
		b.WriteString(l.Symptom)
		b.WriteByte('\n')
	}

	assert.Contains(t, b.String(), "wrap engine context",
		"a cluster split across two pins' citations is not captured by either")
}

func TestWeakEvidencePinsRankLastAndGetTagged(t *testing.T) {
	// Confidence at the surface: a single-event pin (the v1-era shape)
	// must not out-rank a hand-written pin for a budget slot, and the
	// ambient surface tags it so the agent knows what it is leaning on.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// One PR, two comments: one event — weak under current rules.
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "api/a.go", PR: 9, Body: "b", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "api/b.go", PR: 9, Body: "b", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-w", "api", 1, []model.Proposal{{
		Signature: "sig-w", Rule: "single-event-guidance", Region: "api",
		Note: "Distilled from one review exchange.", Members: []int64{1, 2},
		Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		// File order puts the weak distilled pin FIRST; rank must
		// reorder so the hand-written pin wins the single slot.
		{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
		{Rule: "hand-written-guard", Region: "api", Note: "A human wrote this deliberately."},
	}

	out, trimmed, err := LessonsForScopeBudget(st, cfg, "api/z.go", 8, 1)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, 1, trimmed)
	assert.Contains(t, out[0].Symptom, "hand-written-guard",
		"a hand-written pin outranks weak distilled evidence for the slot")

	// Unbudgeted (why): every pin shows, the weak one carries its facts
	// in the display-only annotation — never in Symptom, whose text is
	// the firing log's identity and must not age.
	out, _, err = LessonsForScopeBudget(st, cfg, "api/z.go", 8, 0)
	require.NoError(t, err)

	var all strings.Builder

	for _, l := range out {
		all.WriteString(l.Symptom)
		all.WriteString(" | ")
		all.WriteString(l.Annotation)
		all.WriteByte('\n')

		assert.NotContains(t, l.Symptom, "event(s)",
			"aging facts must never leak into the lesson's identity")
	}

	assert.Contains(t, all.String(), "weak: 1 event(s)",
		"deliberate views print the tier with its facts")
}

func TestLessonsForFilesUnionsAndBudgets(t *testing.T) {
	// The multi-file surface: one repo-wide pin applying to every file
	// appears once, per-region pins join in, and the change budget caps
	// the union with an honest count.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Distinct topics: the union collapses restatements globally, and
	// this test is about the merge, not dedup.
	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "secrets-hygiene", Region: "*", Note: "Applies to every file exactly once."},
		{Rule: "boundary-validation", Region: "api", Note: "Validate request payloads at the edge."},
		{Rule: "transaction-atomicity", Region: "db", Note: "Wrap dependent writes in one transaction."},
	}

	lessons, trimmed, err := LessonsForFiles(st, cfg, []string{"api/a.go", "db/b.go"}, 6)
	require.NoError(t, err)
	assert.Zero(t, trimmed)

	var all strings.Builder
	for _, l := range lessons {
		all.WriteString(l.Symptom)
		all.WriteByte('\n')
	}

	assert.Len(t, lessons, 3, "the repo-wide pin is one line, not one per file")
	assert.Contains(t, all.String(), "boundary-validation")
	assert.Contains(t, all.String(), "transaction-atomicity")

	capped, trimmed, err := LessonsForFiles(st, cfg, []string{"api/a.go", "db/b.go"}, 2)
	require.NoError(t, err)
	assert.Len(t, capped, 2)
	assert.Equal(t, 1, trimmed, "the held-back lesson is counted, never hidden")
}

func TestChangeSetCarriesLessons(t *testing.T) {
	// The moment-of-change surface: an agent calling change_set before
	// a multi-file edit gets the memory that used to live only in the
	// per-file hook.
	st, root := seedStore(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: guard-empty-datasets\n    region: scripts\n"+
			"    note: Guard datasets before reductions.\n",
	), 0o644))

	var b strings.Builder
	require.NoError(t, ChangeSet(&b, st, root, []string{"scripts/task.py"}))

	out := b.String()
	assert.Contains(t, out, "lessons for this change")
	assert.Contains(t, out, "[pin · scripts] guard-empty-datasets")
	assert.Contains(t, out, "RUF001", "mined recurrence rides along")
}

func TestStrongPinFromLaterFileBeatsWeakFromFirst(t *testing.T) {
	// Rank must survive the multi-file merge: a weak pin surfaced by
	// the FIRST file must not hold a budget slot a hand-written
	// (strong) pin from a LATER file deserves.
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// One PR, two comments: one event — weak.
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "api/a.go", PR: 9, Body: "b", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "api/b.go", PR: 9, Body: "b", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-w", "api", 1, []model.Proposal{{
		Signature: "sig-w", Rule: "single-event-guidance", Region: "api",
		Note: "Distilled from one review exchange.", Members: []int64{1, 2},
		Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
		{Rule: "transaction-atomicity", Region: "db", Note: "Wrap dependent writes in one transaction."},
	}

	// api file first, db file second; budget 1.
	lessons, trimmed, err := LessonsForFiles(st, cfg, []string{"api/x.go", "db/y.go"}, 1)
	require.NoError(t, err)
	require.Len(t, lessons, 1)
	assert.Equal(t, 1, trimmed, "the held-back pin is counted")
	assert.Contains(t, lessons[0].Symptom, "transaction-atomicity",
		"the hand-written pin from the later file wins the slot over weak evidence")
}

func TestProposalLedgerRendersOutcome(t *testing.T) {
	applied := []model.Proposal{
		{ID: 16, Rule: "pooled-state-reset", Region: "engine"},
		{ID: 47, Rule: "leak-exception-to-client", Region: "svc/api"},
		{ID: 9, Rule: "cap-per-request-query", Region: "web"},
	}

	health := map[int64]ProposalHealth{
		16: {Tier: "strong", Outcome: "working — flagged 10× in ~200 region-commits " +
			"before exposure; 0× in 84 since (fired 41×)"},
		47: {Tier: "strong", Escalate: true,
			Outcome: "not landing — recurred 2× since exposure (fired 12×)"},
		// p9 carries no reading: an unmeasured pin must render nothing.
	}

	var sb strings.Builder
	PrintProposalLedger(&sb, nil, applied, nil, nil, health)
	out := sb.String()

	assert.Contains(t, out,
		"working — flagged 10× in ~200 region-commits before exposure; 0× in 84 since (fired 41×)")
	assert.Contains(t, out, "not landing — recurred 2× since exposure (fired 12×)")

	// The escalation hint names exactly the not-landing set.
	assert.Contains(t, out, "not landing — p47 fires but the mistake recurs")
	assert.NotContains(t, out, "p16 fire")

	// Two measured pins, two sentences — p9 has no reading, so no line.
	assert.Equal(t, 2, strings.Count(out, "(fired"))

	// No escalating pins — no hint block at all.
	sb.Reset()
	PrintProposalLedger(&sb, nil, applied[:1], nil, nil,
		map[int64]ProposalHealth{16: {Tier: "strong"}})
	assert.NotContains(t, sb.String(), "escalation is yours")
}

func TestProposalLedgerRendersScopeAdvisory(t *testing.T) {
	scope := "delivery may miss the trigger: the note names api/schemas.py (outside the regions) " +
		"and evidence web/src/api/schema.ts co-changes with it (38 shared commits) " +
		"— consider regions: [web/src/api, api]"

	pending := []model.Proposal{{ID: 71, Rule: "regenerate-web-schema", Region: "web/src/api", Note: "n"}}
	applied := []model.Proposal{{ID: 5, Rule: "quiet-one", Region: "api"}}

	var sb strings.Builder

	PrintProposalLedger(&sb, pending, applied, nil, nil, map[int64]ProposalHealth{
		71: {Tier: "strong", Scope: scope},
		5:  {Tier: "strong"},
	})
	out := sb.String()

	assert.Contains(t, out, scope)
	assert.Equal(t, 1, strings.Count(out, "delivery may miss the trigger"),
		"unflagged pins render no scope line")

	// The tail block names the flagged set once, after the lists, so a
	// long ledger cannot bury the advisory.
	assert.Contains(t, out, "trigger scope: p71")
	assert.Contains(t, out, "applied pins change through `--retarget`")
	assert.Less(t, strings.Index(out, "applied — these are pins"),
		strings.Index(out, "trigger scope:"), "the tail follows the lists")

	// A pruned pin names its state instead of advising.
	sb.Reset()
	PrintProposalLedger(&sb, nil, applied, nil, nil, map[int64]ProposalHealth{
		5: {Tier: "strong", Pruned: true},
	})
	assert.Contains(t, sb.String(), "not in .seamark/lessons.yaml")

	// A blocked confirmed trigger renders its own line — no drift and
	// no advisory would otherwise mention it.
	sb.Reset()
	PrintProposalLedger(&sb, nil, applied, nil, nil, map[int64]ProposalHealth{
		5: {Tier: "strong", Blocked: "trigger api/schemas.py — confirmed by co-change (38 shared commits) but not deliverable"},
	})
	assert.Contains(t, sb.String(), "but not deliverable")

	// Region lines group together: the advisory follows "regions now:".
	sb.Reset()
	PrintProposalLedger(&sb, nil, applied, nil, nil, map[int64]ProposalHealth{
		5: {Tier: "strong", Retarget: "api, db", Scope: scope},
	})
	out = sb.String()

	require.Contains(t, out, "regions now: api, db")
	assert.Less(t, strings.Index(out, "regions now:"), strings.Index(out, "delivery may miss"),
		"scope renders after the retarget line")
	assert.Contains(t, out, "trigger scope: p5")

	// No flagged pins — no tail at all.
	sb.Reset()
	PrintProposalLedger(&sb, nil, applied, nil, nil, map[int64]ProposalHealth{
		5: {Tier: "strong"},
	})
	assert.NotContains(t, sb.String(), "trigger scope:")
}

func TestDistillPlanShowsScopeAdvisories(t *testing.T) {
	pending := []model.Proposal{
		{ID: 71, Rule: "regenerate-web-schema", Region: "web/src/api",
			Note: "Edit api/schemas.py first.", Members: []int64{1, 2}},
		{ID: 72, Rule: "quiet-one", Region: "api", Note: "n", Members: []int64{3}},
	}

	confirmed := "trigger api/schemas.py — directly cited by the evidence; delivery targets api/schemas.py"
	unconfirmed := "trigger cmd/gen.go — named by the distiller, not confirmed by history; consider regions after apply"

	var sb strings.Builder

	PrintDistillPlan(&sb, DistillSummary{GroupsTotal: 1, GroupsRead: 1}, pending,
		map[int64][]string{71: {confirmed, unconfirmed}}, []string{"p71"})
	out := sb.String()

	assert.Contains(t, out, confirmed, "a precise proposal announces what happened")
	assert.Contains(t, out, unconfirmed)
	assert.Contains(t, out, "trigger scope: p71")
	assert.Equal(t, 1, strings.Count(out, "directly cited by the evidence"),
		"the unflagged proposal renders no annotation lines")
	assert.Contains(t, out, "review the trigger and proposed delivery regions")

	// No annotations — the plan prints exactly as before.
	sb.Reset()
	PrintDistillPlan(&sb, DistillSummary{GroupsTotal: 1, GroupsRead: 1}, pending, nil, nil)
	assert.NotContains(t, sb.String(), "trigger")
}

func TestPrintOutcomesOrdersActionableFirst(t *testing.T) {
	applied := []model.Proposal{
		{ID: 9, Rule: "cap-per-request-query", Region: "web"},
		{ID: 16, Rule: "pooled-state-reset", Region: "engine"},
		{ID: 47, Rule: "leak-exception-to-client", Region: "svc/api"},
	}

	readings := map[int64]outcome.Reading{
		9: {ProposalID: 9, Exposed: true, Firings: 5, PostCommits: 3,
			Verdict: outcome.VerdictUntested, Reason: outcome.ReasonLowActivity},
		16: {ProposalID: 16, Exposed: true, Firings: 41, PreEvents: 10,
			PreCommits: 200, PostCommits: 84, Verdict: outcome.VerdictWorking},
		47: {ProposalID: 47, Exposed: true, Firings: 12, PostEvents: 2,
			Verdict: outcome.VerdictNotLanding},
	}

	var sb strings.Builder
	PrintOutcomes(&sb, applied, readings)
	out := sb.String()

	// The aggregate line adds up: measured = working + not landing + untested.
	assert.Contains(t, out, "pin outcomes — 3 measured: 1 working, 1 not landing, 1 untested")

	// Actionable first: not landing, then working, then untested —
	// regardless of proposal order.
	notLanding := strings.Index(out, "leak-exception-to-client")
	working := strings.Index(out, "pooled-state-reset")
	untested := strings.Index(out, "cap-per-request-query")
	require.NotEqual(t, -1, notLanding)
	require.NotEqual(t, -1, working)
	require.NotEqual(t, -1, untested)
	assert.Less(t, notLanding, working)
	assert.Less(t, working, untested)

	// Each row carries its falsifiable sentence.
	assert.Contains(t, out, "not landing — recurred 2× since exposure (fired 12×)")
	assert.Contains(t, out,
		"working — flagged 10× in ~200 region-commits before exposure; 0× in 84 since (fired 41×)")
	assert.Contains(t, out, "untested — 3 region-commits since exposure (fired 5×)")

	// Nothing measured — nothing printed, not an empty header.
	sb.Reset()
	PrintOutcomes(&sb, applied, nil)
	assert.Zero(t, sb.Len())
}

func TestPrintFiringSummaryReportsSuppressionOnlyHistory(t *testing.T) {
	var out strings.Builder
	PrintFiringSummary(&out, reviews.Summary{SuppressedHookFirings: 3})

	assert.Contains(t, out.String(), "no lesson firings delivered")
	assert.Contains(t, out.String(),
		"hook delivery — instrumented: 0 injected (0 repeated), 3 suppressed")
	assert.NotContains(t, out.String(), "no lesson firings recorded yet")
}

func TestPrintFiringSummaryShowsPerLessonMatchesWhenDeliveryIsSuppressed(t *testing.T) {
	var out strings.Builder
	PrintFiringSummary(&out, reviews.Summary{
		Total:     1,
		BySurface: map[string]int{"hook": 1},
		Ranked: []reviews.Fired{{
			Region: "api", Symptom: "sync generated client", Count: 1, Matches: 4,
			LastTS: "2026-08-10T12:00:00Z",
		}},
		InstrumentedHookFirings: 1,
		SuppressedHookFirings:   3,
	})

	assert.Contains(t, out.String(), "×1    delivered / ×4    matched")
}
