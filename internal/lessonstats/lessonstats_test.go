package lessonstats

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

var (
	// pooled fires through every kind of record.
	pooled = reviews.FiredLesson{Region: "api", Symptom: "reset pooled state"}
	// quiet is mined and eligible, but no record names it.
	quiet = model.Lesson{
		ClusterKey: "db\x00close rows", Region: "db", Symptom: "close rows",
		Reviewer: "person", Occurrences: 3, LastTS: 90,
	}
)

// mixedFirings covers the legacy and the current record shapes.
func mixedFirings() []reviews.Firing {
	hook := func(client string, status reviews.DeliveryStatus, bytes int) reviews.Firing {
		f := reviews.Firing{
			TS: "2026-09-01T10:00:00Z", File: "api/a.py", Tool: "Edit", Delivery: status,
			SessionSHA: "session", Generation: 1, ContextBytes: bytes,
			Fired: []reviews.FiredLesson{pooled}, Client: client, ContextSHA: client,
		}
		if client != "" {
			f.Mechanism = "pre-tool-use-context"
		}

		return f
	}

	return []reviews.Firing{
		// A legacy record: no delivery status and no client.
		{TS: "2026-08-01T10:00:00Z", File: "api/a.py", Tool: "Edit", Fired: []reviews.FiredLesson{pooled}},
		// Instrumented, but older than the client attribution.
		hook("", reviews.DeliveryInjected, 100),
		hook("claude", reviews.DeliveryInjected, 200),
		hook("claude", reviews.DeliverySuppressedRepeat, 0),
		hook("codex", reviews.DeliveryInjected, 300),
		// Other surfaces are delivered exposure, never hook reminders.
		{TS: "2026-09-02T10:00:00Z", Surface: "change_set", Files: []string{"api/a.py", "db/b.py"},
			Fired: []reviews.FiredLesson{pooled}},
		{TS: "2026-09-03T10:00:00Z", Surface: "check", Files: []string{"api/a.py"},
			Fired: []reviews.FiredLesson{pooled}},
	}
}

// writeLog writes the firings as the hook would, plus any raw lines.
func writeLog(t *testing.T, root string, firings []reviews.Firing, raw ...string) {
	t.Helper()

	var buf []byte

	for _, f := range firings {
		line, err := json.Marshal(f)
		require.NoError(t, err)

		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	for _, line := range raw {
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(reviews.AuditLogPath(root), buf, 0o644))
}

// openStore opens an index outside root, seeded with mined lessons.
func openStore(t *testing.T, lessons ...model.Lesson) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons(lessons, nil))

	return st
}

func TestGatherKeepsLogStatesDistinct(t *testing.T) {
	st := openStore(t)

	t.Run("no log", func(t *testing.T) {
		root := t.TempDir()

		snap := Gather(st, root)
		assert.Equal(t, filepath.Join(root, ".seamark", "lessons-audit.jsonl"), snap.LogPath)
		assert.False(t, snap.LogExists)
		assert.Zero(t, snap.Records)
		require.NoError(t, snap.LogErr)
		assert.True(t, snap.Eligible)
	})

	t.Run("empty log", func(t *testing.T) {
		root := t.TempDir()
		writeLog(t, root, nil)

		snap := Gather(st, root)
		assert.True(t, snap.LogExists)
		assert.Zero(t, snap.Records)
		assert.Zero(t, snap.Skipped)
		require.NoError(t, snap.LogErr)
	})

	t.Run("only unreadable lines", func(t *testing.T) {
		root := t.TempDir()
		writeLog(t, root, nil, "not json", "{torn")

		snap := Gather(st, root)
		assert.True(t, snap.LogExists)
		assert.Zero(t, snap.Records)
		assert.Equal(t, 2, snap.Skipped, "corrupt lines are not an empty log")
		require.NoError(t, snap.LogErr)
	})

	t.Run("read error", func(t *testing.T) {
		root := t.TempDir()

		// A directory at the log path makes the read fail.
		require.NoError(t, os.MkdirAll(reviews.AuditLogPath(root), 0o755))

		snap := Gather(st, root)
		require.Error(t, snap.LogErr)
		assert.Contains(t, snap.LogErr.Error(), "failed to read firing log")
		assert.Zero(t, snap.Records)
		assert.Equal(t, reviews.Summary{}, snap.Summary, "a failed read never renders as counts")
		assert.False(t, snap.Eligible)
		require.Error(t, snap.EligibleErr)
		require.Error(t, snap.OutcomeErr)
	})
}

func TestGatherMatchesTheExistingAggregation(t *testing.T) {
	root := t.TempDir()
	firings := mixedFirings()
	writeLog(t, root, firings)

	mined := []model.Lesson{
		{ClusterKey: "api\x00reset pooled state", Region: "api", Symptom: "reset pooled state",
			Reviewer: "person", Occurrences: 4, LastTS: 100},
		quiet,
	}
	st := openStore(t, mined...)

	snap := Gather(st, root)
	require.NoError(t, snap.LogErr)
	require.NoError(t, snap.EligibleErr)
	require.NoError(t, snap.OutcomeErr)
	assert.True(t, snap.LogExists)
	assert.Equal(t, len(firings), snap.Records)
	assert.True(t, snap.Eligible)

	// The CLI computes exactly this for `lessons --stats`.
	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	stored, err := st.AllLessons(0)
	require.NoError(t, err)

	want := reviews.Summarize(firings, cfg.Surface(stored, ""))
	got := snap.Summary

	assert.ElementsMatch(t, want.Ranked, got.Ranked)
	want.Ranked, got.Ranked = nil, nil
	assert.Equal(t, want, got)

	// The legacy semantics survive: the unattributed row holds the
	// instrumented record without a client, and the legacy record is
	// delivered but not instrumented.
	require.Len(t, snap.Summary.HookByClient, 3)
	assert.Equal(t, reviews.HookAttribution{Injected: 1, ContextBytes: 100}, snap.Summary.HookByClient[2])
	assert.Equal(t, 4, snap.Summary.BySurface["hook"])
	assert.Equal(t, 3, snap.Summary.InstrumentedHookFirings)
	assert.Equal(t, 1, snap.Summary.SuppressedHookFirings)
	assert.Equal(t, 1, snap.Summary.BySurface["change_set"])
	assert.Equal(t, 1, snap.Summary.BySurface["check"])

	require.Len(t, snap.Summary.NeverFired, 1)
	assert.Equal(t, quiet.Symptom, snap.Summary.NeverFired[0].Symptom)
}

func TestGatherWithoutAnIndexKeepsTheTotals(t *testing.T) {
	root := t.TempDir()
	firings := mixedFirings()
	writeLog(t, root, firings)

	snap := Gather(nil, root)
	require.NoError(t, snap.LogErr)
	assert.Equal(t, len(firings), snap.Records)
	assert.False(t, snap.Eligible)
	require.ErrorIs(t, snap.EligibleErr, ErrNoIndex)
	assert.Contains(t, snap.EligibleErr.Error(), "eligibility needs the mined lessons")
	require.ErrorIs(t, snap.OutcomeErr, ErrNoIndex)
	assert.Empty(t, snap.Applied)

	want := reviews.Summarize(firings, nil)
	assert.Equal(t, want.Total, snap.Summary.Total)
	assert.Equal(t, want.BySurface, snap.Summary.BySurface)
	assert.Equal(t, want.HookByClient, snap.Summary.HookByClient)
	assert.Empty(t, snap.Summary.NeverFired)
}

func TestGatherReportsAMalformedLessonsConfig(t *testing.T) {
	root := t.TempDir()
	firings := mixedFirings()
	writeLog(t, root, firings)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("hook_delivery: sometimes\n"), 0o644))

	snap := Gather(openStore(t, quiet), root)
	require.NoError(t, snap.LogErr)
	assert.False(t, snap.Eligible)
	require.ErrorIs(t, snap.EligibleErr, reviews.ErrLessonsConfig, "the reader's error, never defaults")
	require.ErrorIs(t, snap.OutcomeErr, reviews.ErrLessonsConfig)
	assert.Equal(t, reviews.Summarize(firings, nil).Total, snap.Summary.Total, "the totals stay usable")
	assert.Empty(t, snap.Summary.NeverFired, "no eligibility without the config")
}

func TestGatherRanksTiesStably(t *testing.T) {
	root := t.TempDir()

	// Four lessons with equal counts and timestamps: only the tie-break
	// orders them.
	lessons := []reviews.FiredLesson{
		{Region: "web", Symptom: "b"},
		{Region: "api", Symptom: "z"},
		{Region: "web", Symptom: "a"},
		{Region: "api", Symptom: "c"},
	}
	writeLog(t, root, []reviews.Firing{{TS: "2026-09-01T10:00:00Z", File: "x.go", Fired: lessons}})

	want := []reviews.FiredLesson{
		{Region: "api", Symptom: "c"},
		{Region: "api", Symptom: "z"},
		{Region: "web", Symptom: "a"},
		{Region: "web", Symptom: "b"},
	}

	for range 20 {
		snap := Gather(nil, root)
		require.Len(t, snap.Summary.Ranked, 4)

		var got []reviews.FiredLesson
		for _, r := range snap.Summary.Ranked {
			got = append(got, reviews.FiredLesson{Region: r.Region, Symptom: r.Symptom})
		}

		require.Equal(t, want, got)
	}
}

func TestGatherListsAppliedProposalsByID(t *testing.T) {
	root := t.TempDir()
	st := openStore(t)

	for _, rule := range []string{"first", "second", "third"} {
		p := model.Proposal{
			Signature: rule, Rule: rule, Region: "api", Note: "n",
			Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalApplied,
		}
		require.NoError(t, st.InsertProposal(&p))
	}

	snap := Gather(st, root)
	require.NoError(t, snap.OutcomeErr)
	require.Len(t, snap.Applied, 3)
	assert.Less(t, snap.Applied[0].ID, snap.Applied[1].ID)
	assert.Less(t, snap.Applied[1].ID, snap.Applied[2].ID)
}

func TestGatherNeverRecordsDelivery(t *testing.T) {
	st := openStore(t, quiet)

	// An absent log stays absent, and no .seamark directory appears.
	root := t.TempDir()
	_ = Gather(st, root)

	_, err := os.Stat(filepath.Join(root, ".seamark"))
	assert.True(t, os.IsNotExist(err), "viewing stats creates nothing")

	// An existing log keeps its bytes.
	root = t.TempDir()
	writeLog(t, root, mixedFirings())

	before, err := os.ReadFile(reviews.AuditLogPath(root))
	require.NoError(t, err)

	_ = Gather(st, root)
	_ = Gather(nil, root)

	after, err := os.ReadFile(reviews.AuditLogPath(root))
	require.NoError(t, err)
	assert.Equal(t, before, after, "viewing stats is not delivery")
}

func TestSnapshotErrNamesTheFirstFailedSource(t *testing.T) {
	// breakLog puts a directory at the log path, so the log read fails.
	breakLog := func(t *testing.T, root string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(reviews.AuditLogPath(root), 0o755))
	}

	breakConfig := func(t *testing.T, root string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
			[]byte("hook_delivery: sometimes\n"), 0o644))
	}

	t.Run("every source reads", func(t *testing.T) {
		root := t.TempDir()
		writeLog(t, root, mixedFirings())

		require.NoError(t, Gather(openStore(t, quiet), root).Err())
	})

	t.Run("the config comes before the log", func(t *testing.T) {
		root := t.TempDir()
		breakLog(t, root)
		breakConfig(t, root)

		snap := Gather(openStore(t, quiet), root)
		require.Error(t, snap.LogErr)
		require.ErrorIs(t, snap.Err(), reviews.ErrLessonsConfig)
		assert.Contains(t, snap.Err().Error(), "failed to load lessons config")
	})

	t.Run("the applied proposals come before the config and the log", func(t *testing.T) {
		root := t.TempDir()
		breakLog(t, root)
		breakConfig(t, root)

		path := filepath.Join(t.TempDir(), "index.db")
		st, err := store.Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { _ = st.Close() })

		// Without the proposal table, only the ledger read fails.
		db, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		_, err = db.Exec(`DROP TABLE proposal`)
		require.NoError(t, err)
		require.NoError(t, db.Close())

		snap := Gather(st, root)
		require.Error(t, snap.Err())
		assert.Contains(t, snap.Err().Error(), "failed to fetch applied proposals")
	})

	t.Run("the mined lessons come first", func(t *testing.T) {
		root := t.TempDir()
		breakLog(t, root)
		breakConfig(t, root)

		// A closed store fails every read: the mined lessons, the ledger
		// and the outcomes.
		st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
		require.NoError(t, err)
		require.NoError(t, st.Close())

		snap := Gather(st, root)
		require.Error(t, snap.Err())
		assert.Contains(t, snap.Err().Error(), "failed to fetch mined lessons")
	})

	t.Run("the outcomes need an index", func(t *testing.T) {
		root := t.TempDir()
		writeLog(t, root, mixedFirings())

		require.ErrorIs(t, Gather(nil, root).Err(), ErrNoIndex)
	})
}

func TestGatherMeasuresTheNewestPinFirst(t *testing.T) {
	root := t.TempDir()

	pins := []reviews.PinRule{
		{Rule: "older-rule", Region: "api", Note: "Older."},
		{Rule: "newer-rule", Region: "db", Note: "Newer."},
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: older-rule\n    region: api\n    note: Older.\n"+
			"  - rule: newer-rule\n    region: db\n    note: Newer.\n"), 0o644))

	// Both pins fired, so the measurement counts the commits of each.
	var fired []reviews.FiredLesson

	for _, pin := range pins {
		l := reviews.SurfacedPin{Pin: pin}.Lesson()
		fired = append(fired, reviews.FiredLesson{Region: l.Region, Symptom: l.Symptom})
	}

	writeLog(t, root, []reviews.Firing{{TS: "2026-09-01T10:00:00Z", File: "api/a.py", Fired: fired}})

	path := filepath.Join(t.TempDir(), "index.db")
	st, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	for _, pin := range pins {
		require.NoError(t, st.InsertProposal(&model.Proposal{
			Signature: pin.Rule, Rule: pin.Rule, Region: pin.Region, Note: pin.Note,
			Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalApplied,
		}))
	}

	// Without the commit files, the first commit count fails.
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE decision_file`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// `lessons --stats` measured in the store order, newest first, so
	// its error named the newer pin.
	snap := Gather(st, root)
	require.Error(t, snap.OutcomeErr)
	assert.Contains(t, snap.OutcomeErr.Error(), "failed to count pre-exposure commits for {newer-rule db")
	assert.Equal(t, snap.OutcomeErr, snap.Err())

	// The snapshot still lists the proposals by ascending ID.
	require.Len(t, snap.Applied, 2)
	assert.Equal(t, "older-rule", snap.Applied[0].Rule)
}

func TestNewestFirstSortsACopy(t *testing.T) {
	applied := []model.Proposal{{ID: 1}, {ID: 3}, {ID: 2}}

	got := NewestFirst(applied)
	require.Len(t, got, 3)
	assert.Equal(t, []int64{3, 2, 1}, []int64{got[0].ID, got[1].ID, got[2].ID})
	assert.Equal(t, []int64{1, 3, 2}, []int64{applied[0].ID, applied[1].ID, applied[2].ID},
		"the snapshot keeps its own order")
}
