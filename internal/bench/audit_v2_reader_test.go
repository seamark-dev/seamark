package bench

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// This file is outside both go:embed lists of the bench fingerprint
// (fingerprint.go, workflow_fingerprint.go). A change here therefore
// moves no experiment fingerprint, and committed results stay pooled.

// TestMatchingTreatmentFiringsReadsAuditV2 is the M6 test of the P0
// review (A11, contract L3 "Legacy readers of v2 records"). A trial
// runs with the recording switch off and installs no exploration. Its
// log thus holds v2 records of the edit hook and the preparation
// surfaces only, and the fingerprinted reader must accept them without
// an error.
func TestMatchingTreatmentFiringsReadsAuditV2(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeLessons(dir, schemaSyncLessonYAML))

	cfg, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)

	pin := cfg.Pin[0]
	expected := reviews.PinIdentity(pin)
	treatment := []model.Lesson{{Region: expected.Region, Symptom: expected.Symptom}}
	other := []model.Lesson{{Region: "server", Symptom: "another lesson"}}
	firing := reviews.FiringPin{
		Identity: reviews.PinIdentityDigest(reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions)),
		Version:  cfg.PinVersion(pin),
	}

	resettable := true

	// edit is a hook event of the treatment client with the recording
	// switch off, the only state a trial has.
	edit := func(file, match string, records []reviews.EventRecord, emission string,
		delivery reviews.PinDelivery,
	) reviews.AuditEvent {
		p := firing
		p.Delivery = delivery

		return reviews.AuditEvent{
			Root: dir, Tool: "Edit", Files: []string{file}, ClientID: "claude",
			Mechanism: "pre-tool-use-context", SessionID: "session-1", MatchID: match, Turn: "prompt-1",
			ReceiverID: "session-1", Lane: "root", Resettable: &resettable,
			Records: records, Pins: []reviews.FiringPin{p},
			Emission: emission, Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: cfg.GuidanceDigest(), GuidanceStatus: "ok", Generation: 1,
		}
	}

	preparation := func(surface string) reviews.AuditEvent {
		p := firing
		p.Delivery = reviews.PinReturned

		return reviews.AuditEvent{
			Root: dir, Surface: surface, Files: []string{"server/schema.py"},
			Records: []reviews.EventRecord{{Lessons: treatment}}, Pins: []reviews.FiringPin{p},
			Emission: "returned", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: cfg.GuidanceDigest(), GuidanceStatus: "ok",
		}
	}

	events := []reviews.AuditEvent{
		preparation("change_set"),
		edit("server/schema.py", "tool-use-1", []reviews.EventRecord{
			{Status: reviews.DeliveryInjected, Lessons: treatment, ContextBytes: 400},
		}, "emitted", reviews.PinInjected),
		// Two records of one event: the treatment injected, another
		// lesson suppressed.
		edit("server/presenters.py", "tool-use-2", []reviews.EventRecord{
			{Status: reviews.DeliveryInjected, Lessons: treatment, ContextBytes: 420},
			{Status: reviews.DeliverySuppressedRepeat, Lessons: other},
		}, "emitted", reviews.PinInjected),
		edit("server/schema.py", "tool-use-3", []reviews.EventRecord{
			{Status: reviews.DeliverySuppressedRepeat, Lessons: treatment},
		}, "not-needed", reviews.PinSuppressedRepeat),
		preparation("check"),
	}

	for _, ev := range events {
		_, err := reviews.RecordEvent(ev)
		require.NoError(t, err)
	}

	firings, err := reviews.ReadFirings(dir)
	require.NoError(t, err)
	require.Len(t, firings, 6)

	for _, f := range firings {
		require.Equal(t, reviews.AuditSchemaVersion, f.SchemaVersion, "every record is v2")
		require.False(t, f.RecordEvaluations, "a trial runs with the recording switch off")
	}

	intensity, err := matchingTreatmentFirings(dir, pin)
	require.NoError(t, err)

	// A check or change_set record is ambient activity, not treatment.
	assert.Equal(t, hookIntensity{
		Matches: 3, Injections: 2, Repeated: 1, Suppressed: 1, ContextBytes: 820, AuditRows: 6,
	}, intensity)

	// The records above come from RecordEvent. The production writers
	// must give the reader the same kinds when a trial runs them.
	t.Run("the hook, reset, and check writers with the recording switch off", writersFeedTheReader)
}

// writersFeedTheReader runs the edit hook, the reset hook, and the check
// advisory of this release with the recording switch off, as a trial
// does, and feeds their log to the fingerprinted reader. A failed reply,
// an edit without a match, and a reset then write nothing that the
// reader refuses.
func writersFeedTheReader(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeLessons(dir, "hook_delivery: once-per-context\n"+schemaSyncLessonYAML))

	for _, file := range []string{"server/schema.py", "server/presenters.py", "web/app.ts"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("x\n"), 0o644))
	}

	cfg, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)

	st, err := store.OpenMemory()
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	receiver := integration.ReceivingContextOf(integration.ClaudeID, "session-1", "")
	sent := func(string) error { return nil }

	edit := func(file, match string, emit delivery.Emit) {
		t.Helper()

		_, _ = delivery.Deliver(context.Background(), st, delivery.Request{
			Root: dir, ClientID: integration.ClaudeID, Mechanism: "pre-tool-use-context", Config: cfg,
			Event: integration.EditEvent{
				EventMeta: integration.EventMeta{
					SessionID: "session-1", MatchID: match, NativeTool: "Edit", CWD: dir,
					Turn: "prompt-1", Context: receiver,
				},
				Paths: []string{file},
			},
		}, emit)
	}

	edit("server/schema.py", "tool-use-1", func(string) error { return errors.New("stdout closed") })
	edit("server/schema.py", "tool-use-2", sent)
	edit("server/presenters.py", "tool-use-3", sent)
	edit("web/app.ts", "tool-use-4", sent)
	require.NoError(t, delivery.Reset(delivery.ResetRequest{
		Root: dir, ClientID: integration.ClaudeID,
		Event: integration.ResetEvent{Context: *receiver, SessionID: "session-1", Turn: "prompt-2"},
	}))
	edit("server/schema.py", "tool-use-5", sent)
	report.CheckAdvisory(io.Discard, st, dir, []string{"server/schema.py"})

	firings, err := reviews.ReadFirings(dir)
	require.NoError(t, err)
	require.Len(t, firings, 4, "two injections, one suppression, and the check")

	contextBytes := 0

	for _, f := range firings {
		require.Equal(t, reviews.AuditSchemaVersion, f.SchemaVersion)
		require.False(t, f.RecordEvaluations)
		require.NotEqual(t, reviews.DeliveryEvaluation, f.Delivery)
		require.NotEqual(t, reviews.DeliveryReset, f.Delivery)

		contextBytes += f.ContextBytes
	}

	intensity, err := matchingTreatmentFirings(dir, cfg.Pin[0])
	require.NoError(t, err)

	// The reset starts generation 2, so the second injection is not a
	// repeat. The failed reply left its pin eligible.
	assert.Equal(t, hookIntensity{
		Matches: 3, Injections: 2, Repeated: 0, Suppressed: 1, ContextBytes: contextBytes, AuditRows: 4,
	}, intensity)
}
