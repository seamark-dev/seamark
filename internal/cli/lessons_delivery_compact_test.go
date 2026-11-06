package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// compactFixture extends the delivery fixture with a measured finding
// pin and another applied pin. All command reads stay in temporary roots.
func compactFixture(t *testing.T, identityRoot string) *deliveryFixture {
	t.Helper()

	f := newDeliveryFixture(t, identityRoot)
	st, err := store.Open(store.DefaultPath(f.root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaIndexedAt, "1"))
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, f.identityRoot))
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "api\x00handles", Path: "api/handler.go", PR: 1, Body: "Close handles on failure.",
			CreatedAt: deliveryNow.Add(-time.Hour).Unix(), Source: model.SourceReview},
		{ID: 2, LessonKey: "api\x00handles", Path: "api/handler.go", PR: 2, Body: "Close handles on failure again.",
			CreatedAt: deliveryNow.Add(time.Hour).Unix(), Source: model.SourceReview},
	}))
	for i, pin := range f.cfg.Pin {
		p := model.Proposal{Signature: pin.Rule, Rule: pin.Rule, Region: pin.Region, Note: pin.Note,
			Status: model.ProposalApplied, Members: []int64{1}}
		require.NoError(t, storetest.InsertProposal(st, &p))
		require.Equal(t, int64(i+1), p.ID)
	}
	for _, status := range []string{model.ProposalProposed, model.ProposalDismissed} {
		p := model.Proposal{Signature: status, Rule: f.cfg.Pin[0].Rule, Region: "api", Note: "Undecided row.", Status: status}
		require.NoError(t, storetest.InsertProposal(st, &p))
	}
	require.NoError(t, st.Close())
	deliverySQL(t, store.DefaultPath(f.root), "PRAGMA journal_mode = DELETE")

	// The old firing unit supplies recurrence exposure independently of
	// the versioned delivery record of the delivery fixture.
	lesson := (reviews.SurfacedPin{Pin: f.cfg.Pin[0]}).Lesson()
	legacy, err := json.Marshal(reviews.Firing{TS: deliveryNow.Format(time.RFC3339), File: "api/handler.go",
		Fired: []reviews.FiredLesson{{Region: lesson.Region, Symptom: lesson.Symptom}}})
	require.NoError(t, err)
	path := reviews.AuditLogPath(f.root)
	audit, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(audit, append(legacy, '\n')...), 0o644))

	return f
}

// compactExpected returns the delivery lines that the commands add and
// the outcome line of the first applied pin.
func compactExpected(t *testing.T, f *deliveryFixture) (lines map[int64]string, outcomeLine string) {
	t.Helper()

	st, err := store.OpenReadOnly(store.DefaultPath(f.root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	snap := lessonstats.Gather(st, f.identityRoot)
	require.NoError(t, snap.Err())
	req := compactDeliveryRequest(&options{workspace: f.root})
	req.Applied = snap.Applied
	r, err := lessonreport.Gather(context.Background(), req)
	require.NoError(t, err)

	return report.CaptureDelivery(snap.Applied, &r), snap.Readings[1].Line()
}

// assertCompactLegacyWrites compares every file, including the index
// main. The legacy writable opener may change index journaling and its
// parent mtime; no added delivery source or state may change.
func assertCompactLegacyWrites(t *testing.T, before, after map[string]deliveryFile) {
	t.Helper()

	require.Len(t, after, len(before), "no durable file may appear")
	for path, old := range before {
		current, ok := after[path]
		require.True(t, ok, path)
		if assert.ObjectsAreEqual(old, current) {
			continue
		}

		switch path {
		case ".seamark/index.db":
			assert.Equal(t, old.Mode, current.Mode)
			require.Len(t, current.Data, len(old.Data))
			require.Greater(t, len(old.Data), 100)
			assert.Equal(t, "\x01\x01", old.Data[18:20], "fixture starts in DELETE journal mode")
			assert.Equal(t, "\x02\x02", current.Data[18:20], "legacy opener changes the SQLite journal header to WAL")
			t.Logf("inherited writable index open changed %s: bytes equal=%t, mode equal=%t, mtime equal=%t",
				path, old.Data == current.Data, old.Mode == current.Mode, old.ModTime.Equal(current.ModTime))
		case ".seamark", ".git", ".git/index":
			assert.Equal(t, old.Mode, current.Mode, path)
			assert.Equal(t, old.Data, current.Data, path)
			t.Logf("inherited index sidecar lifecycle or git status refresh changed %s mtime only", path)
		default:
			assert.Equal(t, old, current, path)
		}
	}
}

// TestLessonsDeliveryProposalsLine runs lessons --proposals. This build
// reads no capture archive, so the ledger adds no delivery line and
// keeps the outcome line of the applied pin.
func TestLessonsDeliveryProposalsLine(t *testing.T) {
	f := compactFixture(t, "")
	sentinel := newInvokerSentinel(t, f.root)
	lines, outcomeLine := compactExpected(t, f)
	assert.Empty(t, lines)
	require.Equal(t, "not landing — recurred 1× since exposure (fired 1×)", outcomeLine)
	before := deliveryTree(t, f.root)
	out, _, err := runIn(t, "", "-C", f.root, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "        "+outcomeLine+"\n")
	assert.NotContains(t, out, "lesson delivery:")
	for _, raw := range f.raw {
		assert.NotContains(t, out, raw)
	}
	assertCompactLegacyWrites(t, before, deliveryTree(t, f.root))
	sentinel.requireNoCall(t)
}

// TestLessonsDeliveryStatsLine runs lessons --stats. The outcome lines
// keep their bytes, and no delivery line follows them.
func TestLessonsDeliveryStatsLine(t *testing.T) {
	f := compactFixture(t, "")
	sentinel := newInvokerSentinel(t, f.root)
	lines, outcomeLine := compactExpected(t, f)
	assert.Empty(t, lines)
	before := deliveryTree(t, f.root)
	out, _, err := runIn(t, "", "-C", f.root, "lessons", "--stats")
	require.NoError(t, err)
	assert.Contains(t, out, outcomeLine+"\n")
	assert.Contains(t, out, "pin outcomes — 2 measured: 0 working, 1 not landing, 1 untested")
	assert.NotContains(t, out, "lesson delivery:")
	assertCompactLegacyWrites(t, before, deliveryTree(t, f.root))
	sentinel.requireNoCall(t)
}

// TestLessonsDeliveryReportCardCallsNoModel writes the HTML report. The
// cards keep their outcome text, get no delivery paragraph, and escape
// the pin text.
func TestLessonsDeliveryReportCardCallsNoModel(t *testing.T) {
	f := compactFixture(t, "")
	sentinel := newInvokerSentinel(t, f.root)
	lines, outcomeLine := compactExpected(t, f)
	assert.Empty(t, lines)
	verifyWrite(t, f.root, ".seamark/checks/state.json", "untouched-state\n")
	verifyWrite(t, f.root, ".seamark/lessons-hook-state.json", "untouched-state\n")
	verifyWrite(t, f.root, ".seamark/lessons-explore-state.json", "untouched-state\n")
	// Pin text is allowed, but it must not become executable markup.
	deliverySQL(t, store.DefaultPath(f.root), `UPDATE proposal SET note = '<script>alert("pin")</script>' WHERE id = 1`)
	before := deliveryTree(t, f.root)
	out, _, err := runIn(t, "", "-C", f.root, "report", "--out", "-")
	require.NoError(t, err)
	assert.Contains(t, out, outcomeLine+"</p>")
	assert.Contains(t, out, "&lt;script&gt;alert(&#34;pin&#34;)&lt;/script&gt;")
	assert.NotContains(t, out, `<script>alert("pin")</script>`)
	assert.NotContains(t, out, `<p class="health delivery">`)
	for _, raw := range f.raw {
		assert.NotContains(t, out, raw)
	}
	assertCompactLegacyWrites(t, before, deliveryTree(t, f.root))
	sentinel.requireNoCall(t)
}

func TestLessonsDeliveryCompactReadOnly(t *testing.T) {
	f := compactFixture(t, "")
	sentinel := newInvokerSentinel(t, f.root)
	st, err := store.OpenReadOnly(store.DefaultPath(f.root))
	require.NoError(t, err)
	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	health, err := proposals.Health(st, f.root, applied)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	before := deliveryTree(t, f.root)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	got := withDelivery(health, cmd, &options{workspace: f.root}, applied)
	assert.Equal(t, health, got, "without a delivery line the ledger health stays as it is")
	assert.Empty(t, got[1].Delivery)
	assert.Empty(t, got[2].Delivery)
	assert.Equal(t, before, deliveryTree(t, f.root), "the added multi-pin reading writes nothing, including the index main")

	// A failed workspace resolution must not accidentally read CWD or
	// the old identity root.
	req := compactDeliveryRequest(&options{workspace: filepath.Join(f.root, "absent")})
	assert.Empty(t, req.Root)
	assert.Empty(t, captureDelivery(cmd, &options{workspace: filepath.Join(f.root, "absent")}, applied))
	ws, err := workspace.Resolve(f.root, "")
	require.NoError(t, err)
	req = compactDeliveryRequest(&options{workspace: f.root})
	assert.Equal(t, ws.Root, req.Root)
	assert.Equal(t, version, req.SeamarkVersion)
	assert.NotNil(t, req.Now)
	sentinel.requireNoCall(t)
}

func TestLessonsDeliveryCompactLegacyOpenWrites(t *testing.T) {
	f := compactFixture(t, "")
	sentinel := newInvokerSentinel(t, f.root)
	before := deliveryTree(t, f.root)
	st, _, err := openIndex(&options{workspace: f.root})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	after := deliveryTree(t, f.root)
	require.NotEqual(t, before[".seamark/index.db"].Data, after[".seamark/index.db"].Data)
	assertCompactLegacyWrites(t, before, after)
	sentinel.requireNoCall(t)
}

func TestLessonsDeliveryCompactMovedRoot(t *testing.T) {
	original := newDeliveryFixture(t, "")
	f := compactFixture(t, original.root)
	sentinel := newInvokerSentinel(t, f.root)
	// The old checkout holds the same identity but a different semantic
	// version. Its legacy outcome stays independent of the delivery reading.
	oldPins := strings.Replace(deliveryPins, "Close the handle", "Previously close the handle", 1)
	oldConfig, err := reviews.ParseConfig([]byte(oldPins))
	require.NoError(t, err)
	require.NotEqual(t, f.cfg.PinVersion(f.cfg.Pin[0]), oldConfig.PinVersion(oldConfig.Pin[0]))
	verifyWrite(t, original.root, ".seamark/lessons.yaml", oldPins)
	_, legacyOutcome := compactExpected(t, f)
	require.Equal(t, "untested — never fired", legacyOutcome)
	before := deliveryTree(t, original.root)
	for _, args := range [][]string{{"lessons", "--proposals"}, {"lessons", "--stats"}, {"report", "--out", "-"}} {
		// Read from a nested directory with an explicit database: the new
		// source root must still widen to the git top level.
		nested := filepath.Join(f.root, "api")
		require.NoError(t, os.MkdirAll(nested, 0o755))
		out, _, err := runIn(t, "", append([]string{"-C", nested, "--db", store.DefaultPath(f.root)}, args...)...)
		require.NoError(t, err)
		assert.Contains(t, out, legacyOutcome)
		assert.NotContains(t, out, "lesson delivery:")
		assertCompactLegacyWrites(t, before, deliveryTree(t, original.root))
	}
	sentinel.requireNoCall(t)
}
