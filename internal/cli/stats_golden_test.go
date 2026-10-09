package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// These golden tests pin the full output of `lessons --stats`. The
// command reads its data through lessonstats.Gather, the reader of the
// console's Stats view. The goldens prove that this shared reader keeps
// the text that the command printed with its own reads before.

// statsExposure is the time of the first delivered pin firing. The
// outcome loop starts each exposure clock here, so the evidence and the
// commits sit on fixed sides of it.
var statsExposure = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

// statsPins are the pins of the golden fixture. The order is the
// proposal ID order: p1 is the first entry.
var statsPins = []reviews.PinRule{
	{Rule: "legacy-honor-cancel", Region: "svc/legacy", Note: "Propagate cancellation."},
	{Rule: "auth-token-check", Region: "svc/auth", Note: "Validate the token first."},
	{Rule: "metrics-bound-buffers", Region: "svc/metrics", Note: "Cap the exporter buffers."},
	{Rule: "billing-round-cents", Region: "svc/billing", Note: "Round half to even."},
}

// statsMined are the mined lessons of the golden fixture. pooledLesson
// fires on every surface; closeRows never fires.
var (
	pooledLesson = model.Lesson{
		ClusterKey: "api\x00reset pooled state", Region: "api", Symptom: "reset pooled state",
		Reviewer: "person", Occurrences: 4, LastTS: 100,
	}
	closeRows = model.Lesson{
		ClusterKey: "db\x00close rows", Region: "db", Symptom: "close rows",
		Reviewer: "person", Occurrences: 3, LastTS: 90,
	}
)

// statsWorkspace builds an index without a tree-sitter run, as
// maintainWorkspace does. It seeds every source that `lessons --stats`
// reads: mined lessons, findings, commits, applied proposals and pins.
// It writes no firing log; each test writes its own.
func statsWorkspace(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	pre := statsExposure.Add(-time.Hour).Unix()
	post := statsExposure.Add(time.Hour).Unix()

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	require.NoError(t, st.SetMeta(store.MetaIndexedAt, "1"))
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))

	// One cited finding for each pin. f4 recurs in the cluster of the
	// billing citation after the exposure, so that pin is not landing.
	// The bodies share no words, so no lexical group joins two pins.
	require.NoError(t, st.ReplaceLessons([]model.Lesson{pooledLesson, closeRows}, []model.Finding{
		{ID: 1, LessonKey: "svc/legacy\x00cancel", Path: "svc/legacy/adapter.go", PR: 1,
			Body: "adapter swallows cancellation", CreatedAt: pre, Source: model.SourceReview},
		{ID: 2, LessonKey: "svc/auth\x00token", Path: "svc/auth/handler.go", PR: 2,
			Body: "validate session token", CreatedAt: pre, Source: model.SourceReview},
		{ID: 3, LessonKey: "svc/metrics\x00buffer", Path: "svc/metrics/exporter.go", PR: 3,
			Body: "exporter buffer grows unbounded", CreatedAt: pre, Source: model.SourceReview},
		{ID: 4, LessonKey: "svc/billing\x00rounding", Path: "svc/billing/invoice.go", PR: 4,
			Body: "invoice rounding drops cents", CreatedAt: pre, Source: model.SourceReview},
		{ID: 5, LessonKey: "svc/billing\x00rounding", Path: "svc/billing/invoice.go", PR: 5,
			Body: "refund rounding loses cents again", CreatedAt: post, Source: model.SourceReview},
	}))

	// svc/auth has enough commits on both sides for a working verdict.
	// svc/metrics has one commit after the exposure, which is too few.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		commit := func(ref string, ts int64, file string) error {
			return tx.InsertDecision(&model.Decision{
				Kind: model.DecisionCommit, Ref: ref, TS: ts, Files: []string{file},
			})
		}

		for i := range 3 {
			if err := commit(fmt.Sprintf("auth-pre-%d", i), pre-int64(i), "svc/auth/handler.go"); err != nil {
				return err
			}
		}

		for i := range 6 {
			if err := commit(fmt.Sprintf("auth-post-%d", i), post+int64(i), "svc/auth/handler.go"); err != nil {
				return err
			}
		}

		return commit("metrics-post", post, "svc/metrics/exporter.go")
	}))

	// The evidence must be mined after the exposure. Otherwise every
	// pin without a recurrence reads "evidence not mined since exposure".
	minedAt := fmt.Sprint(statsExposure.Add(2 * time.Hour).Unix())
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, minedAt))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, minedAt))

	var yaml strings.Builder

	yaml.WriteString("pin:\n")

	for i, pin := range statsPins {
		require.NoError(t, st.InsertProposal(&model.Proposal{
			Signature: pin.Rule, Rule: pin.Rule, Region: pin.Region, Note: pin.Note,
			Members: []int64{int64(i + 1)}, Agent: "claude/v2", Status: model.ProposalApplied,
		}))

		fmt.Fprintf(&yaml, "  - rule: %s\n    region: %s\n    note: %s\n", pin.Rule, pin.Region, pin.Note)
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(yaml.String()), 0o644))

	return root
}

// statsFired returns the firing-log entries for lessons, as the hook
// writes them.
func statsFired(lessons ...model.Lesson) []reviews.FiredLesson {
	out := make([]reviews.FiredLesson, len(lessons))

	for i, l := range lessons {
		out[i] = reviews.FiredLesson{Region: l.Region, Symptom: l.Symptom}
	}

	return out
}

// statsPinLesson returns pin i as the surfaces render it.
func statsPinLesson(i int) model.Lesson {
	return reviews.SurfacedPin{Pin: statsPins[i]}.Lesson()
}

// statsWriteLog writes the firings to the firing log of root.
func statsWriteLog(t *testing.T, root string, firings []reviews.Firing) {
	t.Helper()

	var buf strings.Builder

	for _, f := range firings {
		line, err := json.Marshal(f)
		require.NoError(t, err)

		buf.Write(line)
		buf.WriteByte('\n')
	}

	require.NoError(t, os.WriteFile(reviews.AuditLogPath(root), []byte(buf.String()), 0o644))
}

// statsTS returns the firing time of a record days after the exposure.
func statsTS(days int) string {
	return statsExposure.AddDate(0, 0, days).Format(time.RFC3339)
}

// statsWriteGoldenLog writes the firing log of the golden fixture. Its
// ranked rows differ in counts or times, so their order never depends
// on a tie-break. auth, metrics and billing fire; legacy never fires.
func statsWriteGoldenLog(t *testing.T, root string) {
	t.Helper()

	auth, metrics, billing := statsPinLesson(1), statsPinLesson(2), statsPinLesson(3)

	const mechanism = "pre-tool-use-context"

	statsWriteLog(t, root, []reviews.Firing{
		// A legacy record: no delivery status and no client.
		{TS: statsTS(-31), File: "api/a.py", Tool: "Edit", Fired: statsFired(pooledLesson)},
		// The first pin delivery starts every exposure clock.
		{TS: statsTS(0), File: "svc/auth/handler.go", Tool: "Edit", Delivery: reviews.DeliveryInjected,
			SessionSHA: "s1", ContextSHA: "c1", Generation: 1, ContextBytes: 200,
			Client: "claude", Mechanism: mechanism,
			Fired: statsFired(pooledLesson, auth, metrics, billing)},
		// The same context gets auth again: a repeated injection.
		{TS: statsTS(1), File: "svc/auth/handler.go", Tool: "Edit", Delivery: reviews.DeliveryInjected,
			SessionSHA: "s1", ContextSHA: "c1", Generation: 1, ContextBytes: 150,
			Client: "claude", Mechanism: mechanism, Fired: statsFired(auth)},
		// A suppressed repeat is a match, never a delivery.
		{TS: statsTS(2), File: "api/a.py", Tool: "Edit", Delivery: reviews.DeliverySuppressedRepeat,
			SessionSHA: "s1", ContextSHA: "c1", Generation: 1,
			Client: "claude", Mechanism: mechanism, Fired: statsFired(pooledLesson)},
		{TS: statsTS(2), File: "api/b.py", Tool: "Write", Delivery: reviews.DeliveryInjected,
			SessionSHA: "s2", ContextSHA: "c2", Generation: 1, ContextBytes: 300,
			Client: "codex", Mechanism: mechanism, Fired: statsFired(pooledLesson)},
		// Other surfaces are delivered exposure, never hook reminders.
		{TS: statsTS(3), Surface: "change_set", Files: []string{"api/a.py", "db/b.py"},
			Fired: statsFired(pooledLesson, metrics)},
		{TS: statsTS(4), Surface: "check", Files: []string{"api/a.py"}, Fired: statsFired(pooledLesson)},
	})
}

func TestLessonsStatsGolden(t *testing.T) {
	root := statsWorkspace(t)

	statsWriteGoldenLog(t, root)

	stdout, stderr, err := runErr(t, "-C", root, "lessons", "--stats")
	require.NoError(t, err)
	assert.Empty(t, stderr)

	// Every section prints. The ranked rows have distinct counts or
	// times, so their order does not depend on a tie-break. Inside one
	// verdict, the outcomes list the newest proposal first: p3 before p1.
	assert.Equal(t, `lesson firings — 4 hook reminders, 1 change_set, 1 check — across 4 files

hook delivery — instrumented: 3 injected (1 repeated), 1 suppressed; context: 650 bytes
  claude via pre-tool-use-context: 2 injected (1 repeated), 1 suppressed; context: 350 bytes
  codex via pre-tool-use-context: 1 injected (0 repeated), 0 suppressed; context: 300 bytes

most surfaced
  ×5    delivered / ×6    matched  api                                      last 2026-09-05  reset pooled state
  ×2    svc/metrics                              last 2026-09-04  metrics-bound-buffers — Cap the exporter buffers.
  ×2    svc/auth                                 last 2026-09-02  auth-token-check — Validate the token first.
  ×1    svc/billing                              last 2026-09-01  billing-round-cents — Round half to even.

never fired — 1 lessons in regions no edit has touched (decay candidates)
  db                                       close rows

pin outcomes — 4 measured: 1 working, 1 not landing, 2 untested
  p4    billing-round-cents                not landing — recurred 1× since exposure (fired 1×)
  p2    auth-token-check                   working — flagged 1× in ~3 region-commits before exposure; 0× in 6 since (fired 2×)
  p3    metrics-bound-buffers              untested — 1 region-commit since exposure (fired 2×)
  p1    legacy-honor-cancel                untested — never fired
`, stdout)
}

func TestLessonsStatsGoldenWithoutDelivery(t *testing.T) {
	t.Run("no firing log", func(t *testing.T) {
		root := statsWorkspace(t)

		stdout, stderr, err := runErr(t, "-C", root, "lessons", "--stats")
		require.NoError(t, err)
		assert.Empty(t, stderr)

		// No pin fired, so every applied pin is untested. The newest
		// proposal still comes first.
		assert.Equal(t, `no lesson firings recorded yet — the edit hook logs each time it reminds an agent (wire it with `+"`seamark init`"+`)

pin outcomes — 4 measured: 0 working, 0 not landing, 4 untested
  p4    billing-round-cents                untested — never fired
  p3    metrics-bound-buffers              untested — never fired
  p2    auth-token-check                   untested — never fired
  p1    legacy-honor-cancel                untested — never fired
`, stdout)
	})

	t.Run("only suppressed matches", func(t *testing.T) {
		root := statsWorkspace(t)

		statsWriteLog(t, root, []reviews.Firing{
			{TS: statsTS(0), File: "api/a.py", Tool: "Edit", Delivery: reviews.DeliverySuppressedRepeat,
				SessionSHA: "s1", ContextSHA: "c1", Generation: 1,
				Client: "claude", Mechanism: "pre-tool-use-context", Fired: statsFired(pooledLesson)},
		})

		stdout, stderr, err := runErr(t, "-C", root, "lessons", "--stats")
		require.NoError(t, err)
		assert.Empty(t, stderr)

		assert.Equal(t, `no lesson firings delivered — recorded hook matches were suppressed
hook delivery — instrumented: 0 injected (0 repeated), 1 suppressed; context: 0 bytes
  claude via pre-tool-use-context: 0 injected (0 repeated), 1 suppressed; context: 0 bytes


pin outcomes — 4 measured: 0 working, 0 not landing, 4 untested
  p4    billing-round-cents                untested — never fired
  p3    metrics-bound-buffers              untested — never fired
  p2    auth-token-check                   untested — never fired
  p1    legacy-honor-cancel                untested — never fired
`, stdout)
	})
}

func TestLessonsStatsReportsTheFirstFailedSource(t *testing.T) {
	// A directory at the log path makes the log read fail.
	breakLog := func(t *testing.T, root string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(reviews.AuditLogPath(root), 0o755))
	}

	breakConfig := func(t *testing.T, root string) {
		t.Helper()

		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
			[]byte("hook_delivery: sometimes\n"), 0o644))
	}

	t.Run("unreadable log", func(t *testing.T) {
		root := statsWorkspace(t)
		breakLog(t, root)

		stdout, err := run(t, "-C", root, "lessons", "--stats")
		require.Error(t, err)
		assert.Empty(t, stdout, "a failed read prints no partial counts")
		assert.Equal(t, "failed to read firing log: read <log>: is a directory",
			strings.ReplaceAll(err.Error(), reviews.AuditLogPath(root), "<log>"))
	})

	t.Run("malformed config and unreadable log", func(t *testing.T) {
		root := statsWorkspace(t)
		breakLog(t, root)
		breakConfig(t, root)

		// The config is read before the log, so its error wins.
		stdout, err := run(t, "-C", root, "lessons", "--stats")
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)
		assert.Empty(t, stdout)
		assert.True(t, strings.HasPrefix(err.Error(), "failed to load lessons config: "), err.Error())
	})
}

func TestLessonsStatsOrdersTiedRowsByRegionThenSymptom(t *testing.T) {
	root := statsWorkspace(t)

	// Four lessons with equal counts and times. Before Gather, the
	// command printed such rows in map order, so the order could change
	// from one run to the next.
	statsWriteLog(t, root, []reviews.Firing{{TS: statsTS(0), File: "x.go", Fired: []reviews.FiredLesson{
		{Region: "web", Symptom: "b"},
		{Region: "api", Symptom: "z"},
		{Region: "web", Symptom: "a"},
		{Region: "api", Symptom: "c"},
	}}})

	var want strings.Builder

	want.WriteString("most surfaced\n")

	for _, row := range [][2]string{{"api", "c"}, {"api", "z"}, {"web", "a"}, {"web", "b"}} {
		fmt.Fprintf(&want, "  ×1    %-40s last 2026-09-01  %s\n", row[0], row[1])
	}

	for range 10 {
		stdout, err := run(t, "-C", root, "lessons", "--stats")
		require.NoError(t, err)
		require.Contains(t, stdout, want.String())
	}
}
