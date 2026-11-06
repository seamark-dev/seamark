package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// deliveryFile includes directories, so a new state directory or lock
// cannot hide behind an unchanged database. No sidecars are excluded.
type deliveryFile struct {
	Data    string
	Mode    fs.FileMode
	ModTime time.Time
}

func deliveryTree(t *testing.T, root string) map[string]deliveryFile {
	t.Helper()

	files := map[string]deliveryFile{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		file := deliveryFile{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			file.Data = string(data)
		}

		files[rel] = file

		return nil
	})
	require.NoError(t, err)

	return files
}

// deliverySQL changes only temporary fixtures. Rollback journal mode
// makes whole-tree comparisons strict without SQLite's empty sidecars.
func deliverySQL(t *testing.T, path string, statements ...string) *sql.DB {
	t.Helper()

	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for _, statement := range statements {
		_, err := db.Exec(statement)
		require.NoError(t, err)
	}

	return db
}

func deliveryJSON(t *testing.T, report lessonreport.Report) string {
	t.Helper()

	data, err := json.Marshal(report)
	require.NoError(t, err)

	return string(data)
}

// noProvider returns an agent invoker that fails the test on any call. A
// delivery read must work without a configured model client.
func noProvider(t *testing.T) *servicestest.Agent {
	t.Helper()

	return &servicestest.Agent{Fn: func(context.Context, string) (string, error) {
		assert.Fail(t, "no provider may run here")

		return "", errors.New("no provider is configured")
	}}
}

// deliverySources writes one evaluation record to the firing log and one
// verify record to the check stream of ws. The session and the match are
// digests of raw values that must never leave the engine.
func deliverySources(t *testing.T, ws workspace.Workspace, identityRoot string) *reviews.Config {
	t.Helper()

	cfg, err := reviews.ParseConfig([]byte(maintPins))
	require.NoError(t, err)

	pathsComplete := true
	firing := reviews.Firing{
		TS: servicestest.Now.Format(time.RFC3339), SchemaVersion: reviews.AuditSchemaVersion,
		EventID: strings.Repeat("a", 32), EventSeq: 1, EventRecords: 1, Delivery: reviews.DeliveryEvaluation,
		Evaluation: "complete", PathsComplete: &pathsComplete, GuidanceSHA: cfg.GuidanceDigest(),
		File: "outside.go", Client: integration.ClaudeID, Lane: "root",
		SessionSHA: reviews.SessionDigest(identityRoot, "PRIVATE-native-session"),
		MatchSHA:   reviews.MatchDigest(identityRoot, "PRIVATE-native-session", "PRIVATE-tool-use"),
	}
	data, err := json.Marshal(firing)
	require.NoError(t, err)
	servicestest.Write(t, ws, ".seamark/lessons-audit.jsonl", string(data)+"\n")

	check := lessoncheck.AuditRecord{Schema: "lesson-check-audit/1", Kind: lessoncheck.AuditVerify}
	data, err = json.Marshal(check)
	require.NoError(t, err)
	servicestest.Write(t, ws, ".seamark/lesson-check-audit.jsonl", string(data)+"\n")

	return cfg
}

// assertNoArchiveRead checks the fixed archive and capture states. The
// report reads no capture archive, so no workspace changes them.
func assertNoArchiveRead(t *testing.T, report lessonreport.Report) {
	t.Helper()
	assert.Equal(t, lessonreport.StateAbsent, report.AsOf.Archive.State)
	assert.Equal(t, lessonreport.ArchiveNotReadError, report.AsOf.Archive.Error)
	assert.Equal(t, lessonreport.CaptureUnavailable, report.Capture.State)
}

func TestDeliveryReadsWithoutWriting(t *testing.T) {
	t.Run("bound-context-and-ledger-read", func(t *testing.T) {
		ws, linked := maintWorkspace(t, false)
		cfg := deliverySources(t, ws, ws.Root)
		require.NoError(t, deliverySQL(t, ws.DBPath, "PRAGMA journal_mode = DELETE").Close())

		agent := noProvider(t)
		fetch := &servicestest.Fetch{Err: errors.New("network is unavailable")}
		svc := NewWithDeps(ws, "v9.8.7", "/unused/seamark", Deps{
			Invoke: agent, Fetch: fetch.Fetch, Now: func() time.Time { return servicestest.Now },
		})
		other := t.TempDir()
		before, otherBefore := deliveryTree(t, ws.Root), deliveryTree(t, other)
		req := lessonreport.Request{
			Root: other, SeamarkVersion: "caller-version",
			Now:     func() time.Time { t.Error("caller clock must not run"); return time.Time{} },
			Applied: []model.Proposal{{ID: 999, Rule: linked.Rule, Region: linked.Region, Status: model.ProposalApplied}},
			PinRef:  cfg.ShortPinRef(reviews.ProposalKey(linked)),
		}
		got, err := svc.Delivery(req)
		require.NoError(t, err)
		require.NotNil(t, got.Expanded)
		assert.Equal(t, "v9.8.7", got.SeamarkVersion)
		assert.Equal(t, servicestest.Now.Format(time.RFC3339), got.AsOf.GeneratedAt)
		require.Len(t, got.Pins, 2)
		assert.Empty(t, got.Pins[0].NextSteps)
		assert.Equal(t, 1, got.Checks.Operations["verify"])
		assert.Equal(t, 1, got.Totals.EvaluationRecords)
		assert.Zero(t, got.Checks.GuardAttempts.Prevented)
		assertNoArchiveRead(t, got)

		want, err := lessonreport.Gather(context.Background(), lessonreport.Request{
			Root: ws.Root, SeamarkVersion: "v9.8.7", PinRef: req.PinRef,
			Now: func() time.Time { return servicestest.Now }, Applied: []model.Proposal{linked},
		})
		require.NoError(t, err)
		assert.Equal(t, want, got)

		req.Limits.MaxAuditBytes = 1
		limited, err := svc.Delivery(req)
		require.NoError(t, err)
		assert.Equal(t, lessonreport.StateOverLimit, limited.AsOf.Audit.State)
		assert.Equal(t, lessonreport.StateOverLimit, limited.AsOf.CheckAudit.State)
		assert.EqualValues(t, 1, limited.AsOf.Audit.Bytes)
		assert.EqualValues(t, 1, limited.AsOf.CheckAudit.Bytes)
		require.NotNil(t, limited.Expanded, "the selected reference is preserved under a read bound")
		assert.Equal(t, got.Expanded.PinRef, limited.Expanded.PinRef)
		assert.Equal(t, before, deliveryTree(t, ws.Root))
		assert.Equal(t, otherBefore, deliveryTree(t, other))
		assert.Zero(t, agent.Count())
		assert.Empty(t, fetch.Called())

		encoded := deliveryJSON(t, got)
		for _, raw := range []string{"PRIVATE-native-session", "PRIVATE-tool-use"} {
			assert.NotContains(t, encoded, raw, "only opaque, shortened refs may leave the engine")
		}
		assert.NotContains(t, encoded, "null")
	})

	t.Run("independent-sources-without-a-ledger", testDeliveryWithoutLedger)
	t.Run("moved-root", testDeliveryMovedRoot)
	t.Run("plain-workspace", testDeliveryPlainWorkspace)
	t.Run("selection-errors", testDeliverySelectionErrors)
}

func testDeliveryWithoutLedger(t *testing.T) {
	for _, state := range []string{"absent", "unreadable", "broken-ledger"} {
		t.Run(state, func(t *testing.T) {
			ws, _ := maintWorkspace(t, false)
			deliverySources(t, ws, ws.Root)

			switch state {
			case "absent":
				require.NoError(t, os.Remove(ws.DBPath))
			case "unreadable":
				require.NoError(t, os.WriteFile(ws.DBPath, []byte("not a database"), 0o600))
			case "broken-ledger":
				require.NoError(t, deliverySQL(t, ws.DBPath, "PRAGMA journal_mode = DELETE", "DROP TABLE proposal").Close())
			}

			agent := noProvider(t)
			e := engNew(ws, Deps{Invoke: agent})
			before := deliveryTree(t, ws.Root)
			// A read must remain callable while a mutation owns the lock.
			e.mu.Lock()
			defer e.mu.Unlock()

			got, err := e.Delivery(lessonreport.Request{Applied: []model.Proposal{{
				ID: 999, Rule: "close-db-handles", Region: "api", Status: model.ProposalApplied,
			}}})
			require.NoError(t, err)
			assertNoArchiveRead(t, got)
			assert.Equal(t, lessonreport.StateRead, got.AsOf.Audit.State)
			assert.Equal(t, lessonreport.StateRead, got.AsOf.CheckAudit.State)
			assert.Equal(t, lessonreport.ConfigOK, got.AsOf.Config.State)
			require.NotEmpty(t, got.Pins)
			assert.Equal(t, 1, got.Totals.EvaluationRecords)
			assert.Equal(t, before, deliveryTree(t, ws.Root))
			assert.Zero(t, agent.Count())
		})
	}
}

func testDeliveryMovedRoot(t *testing.T) {
	indexed, linked := maintWorkspace(t, false)
	require.NoError(t, deliverySQL(t, indexed.DBPath, "PRAGMA journal_mode = DELETE").Close())
	moved := archiveWorkspace(t)
	moved.DBPath = indexed.DBPath
	servicestest.Write(t, moved, ".seamark/lessons.yaml", maintPins)
	deliverySources(t, moved, indexed.Root)
	// The original checkout has different sources; using the identity
	// root for filesystem reads must not silently pass this fixture.
	servicestest.Write(t, indexed, ".seamark/lessons.yaml", "pin:\n  - rule: wrong-checkout\n")
	before, indexedBefore := deliveryTree(t, moved.Root), deliveryTree(t, indexed.Root)
	agent := noProvider(t)
	got, err := engNew(moved, Deps{Invoke: agent}).Delivery(lessonreport.Request{})
	require.NoError(t, err)
	require.Len(t, got.Pins, 2)
	assert.Equal(t, "close-db-handles", got.Pins[0].Rule)
	assert.Equal(t, lessonreport.StateRead, got.AsOf.Audit.State)
	assert.Equal(t, 1, got.Totals.EvaluationRecords, "the firing log of the moved checkout is read")
	assertNoArchiveRead(t, got)

	want, err := lessonreport.Gather(context.Background(), lessonreport.Request{
		Root: moved.Root, SeamarkVersion: "test", Applied: []model.Proposal{linked},
		Now: func() time.Time { return servicestest.Now },
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, before, deliveryTree(t, moved.Root))
	assert.Equal(t, indexedBefore, deliveryTree(t, indexed.Root))
	assert.Zero(t, agent.Count())
}

// testDeliveryPlainWorkspace reads a committed workspace without an index
// and without Seamark files. Every source reads absent, and the read
// creates no directory.
func testDeliveryPlainWorkspace(t *testing.T) {
	ws := archiveWorkspace(t)
	agent := noProvider(t)
	before := deliveryTree(t, ws.Root)
	got, err := engNew(ws, Deps{Invoke: agent}).Delivery(lessonreport.Request{})
	require.NoError(t, err)
	assertNoArchiveRead(t, got)
	assert.Equal(t, lessonreport.StateAbsent, got.AsOf.Audit.State)
	assert.Equal(t, lessonreport.StateAbsent, got.AsOf.CheckAudit.State)
	assert.Equal(t, lessonreport.StateAbsent, got.AsOf.Config.State)
	assert.Empty(t, got.Pins)
	assert.NotContains(t, deliveryJSON(t, got), "null")
	assert.Equal(t, before, deliveryTree(t, ws.Root))
	assert.Zero(t, agent.Count())
	assert.NoDirExists(t, filepath.Join(ws.Root, ".seamark"))
}

func testDeliverySelectionErrors(t *testing.T) {
	ws := archiveWorkspace(t)
	servicestest.Write(t, ws, ".seamark/lessons.yaml", maintPins)
	agent := noProvider(t)
	e := engNew(ws, Deps{Invoke: agent})
	before := deliveryTree(t, ws.Root)
	for _, ref := range []string{"not-a-pin", "cccccccccccc"} {
		report, err := e.Delivery(lessonreport.Request{PinRef: ref})
		var pinErr *reviews.PinRefError
		require.True(t, errors.As(err, &pinErr))
		require.ErrorIs(t, err, reviews.ErrPinRef)
		assert.Empty(t, report.Schema, "selection errors are forwarded, not fabricated source reports")
		assert.Equal(t, "test", report.SeamarkVersion)
	}

	assert.Equal(t, before, deliveryTree(t, ws.Root))

	// Two distinct identities share a short prefix. This is a real
	// configuration ambiguity, not duplicate entries of one identity.
	const collisions = "pin:\n  - rule: collision-25364\n  - rule: collision-76880\n"

	servicestest.Write(t, ws, ".seamark/lessons.yaml", collisions)
	cfg, err := reviews.ParseConfig([]byte(collisions))
	require.NoError(t, err)
	first := reviews.PinIdentityDigest(reviews.NewPinKey(cfg.Pin[0].Rule, "", nil))
	second := reviews.PinIdentityDigest(reviews.NewPinKey(cfg.Pin[1].Rule, "", nil))
	require.Equal(t, first[:8], second[:8], "fixture rules must share a prefix")
	_, err = e.Delivery(lessonreport.Request{PinRef: first[:8]})
	var pinErr *reviews.PinRefError
	require.True(t, errors.As(err, &pinErr))
	assert.Equal(t, reviews.PinRefAmbiguousPrefix, pinErr.Kind)
	assert.Zero(t, agent.Count())
}
