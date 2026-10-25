package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold the write rules of firing record version
// 2 for the edit hook and the reset hook (contract L3, surface "").

// Fixed event IDs keep the records of one test apart without a clock.
const (
	eventOne   = "0123456789abcdef0123456789abcdef"
	eventTwo   = "fedcba9876543210fedcba9876543210"
	eventThree = "00000000000000000000000000000003"
)

// writeConfig writes lessons.yaml under root and reads it back as the
// CLI does, so the firing log names the guidance ok.
func writeConfig(t *testing.T, root, text string) *reviews.Config {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(text), 0o644))

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	return cfg
}

// claudeEvent builds an edit event as the Claude adapter does: the
// receiver comes from ReceivingContextOf, so it carries its lane.
func claudeEvent(root, session, agent, match, turn string, paths ...string) integration.EditEvent {
	return integration.EditEvent{
		EventMeta: integration.EventMeta{
			SessionID: session, MatchID: match, NativeTool: "Edit", CWD: root, Turn: turn,
			Context: integration.ReceivingContextOf(integration.ClaudeID, session, agent),
		},
		Paths: paths,
	}
}

// receiverOf is the receiver ID of the root context of a Claude session.
func receiverOf(session string) string {
	return integration.ReceivingContextOf(integration.ClaudeID, session, "").ID
}

// claudeRequest is an edit request of the Claude client for event.
func claudeRequest(root string, cfg *reviews.Config, event integration.EditEvent) Request {
	return Request{
		Root: root, ClientID: integration.ClaudeID, Mechanism: "pre-tool-use-context",
		Event: event, Config: cfg,
	}
}

// pinOf is the firing-log pin of the pin rule p under cfg.
func pinOf(cfg *reviews.Config, p reviews.PinRule, delivery reviews.PinDelivery, cause string) reviews.FiringPin {
	return reviews.FiringPin{
		Identity: reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions)),
		Version:  cfg.PinVersion(p), Delivery: delivery, Cause: cause,
	}
}

// firings reads every record of the firing log under root.
func firings(t *testing.T, root string) []reviews.Firing {
	t.Helper()

	records, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	return records
}

// symptoms returns the symptoms of a fired list, in order.
func symptoms(fired []reviews.FiredLesson) []string {
	out := make([]string, len(fired))
	for i, f := range fired {
		out[i] = f.Symptom
	}

	return out
}

func TestDeliverWritesTheV2WriteRules(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
		"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n")
	event := claudeEvent(root, "session-1", "", "toolu-1", "prompt-1", "api/handler.go")

	// First edit: the pin and the mined lesson are injected.
	req := claudeRequest(root, cfg, event)
	req.EventID = eventOne

	var got capture

	out, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	require.Equal(t, StatusEmitted, out.Status)

	records := firings(t, root)
	require.Len(t, records, 1)

	first := records[0]
	assert.Equal(t, reviews.DeliveryInjected, first.Delivery)
	assert.Equal(t, []string{"boundary-validation — Validate request payloads at the edge.", "RUF001"},
		symptoms(first.Fired))
	assert.Equal(t, "api/handler.go", first.File)
	assert.Equal(t, "Edit", first.Tool)
	assert.Equal(t, out.ContextBytes, first.ContextBytes)
	assert.Equal(t, uint64(1), first.Generation)
	assert.Equal(t, integration.ClaudeID, first.Client)
	assert.Equal(t, "pre-tool-use-context", first.Mechanism)
	assert.Equal(t, reviews.AuditSchemaVersion, first.SchemaVersion)
	assert.Equal(t, eventOne, first.EventID)
	assert.Equal(t, 1, first.EventSeq)
	assert.Equal(t, 1, first.EventRecords)
	assert.Equal(t, "emitted", first.Emission)
	assert.Equal(t, "complete", first.Evaluation)
	require.NotNil(t, first.PathsComplete)
	assert.True(t, *first.PathsComplete)
	assert.Equal(t, cfg.GuidanceDigest(), first.GuidanceSHA)
	assert.Equal(t, report.GuidanceOK, first.GuidanceStatus)
	assert.Equal(t, reviews.TurnDigest(root, integration.ClaudeID, "session-1", "prompt-1"), first.TurnSHA)
	assert.Equal(t, integration.LaneRoot, first.Lane)
	require.NotNil(t, first.ContextResettable)
	assert.True(t, *first.ContextResettable)
	assert.False(t, first.CaptureOptIn, "the request has no capture opt-in")
	assert.Equal(t, []reviews.FiringPin{pinOf(cfg, cfg.Pin[0], reviews.PinInjected, "")}, first.Pins,
		"the mined lesson is in fired only")

	// A new pin: it is injected, and the delivered lessons are suppressed.
	// One event writes both records with one event ID.
	cfg = writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
		"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n"+
		"  - {rule: idempotent-retries, region: api, note: Make retried requests idempotent.}\n")
	req = claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu-2", "prompt-1",
		"api/handler.go", "/etc/hosts"))
	req.EventID = eventTwo

	out, err = Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	require.Equal(t, StatusEmitted, out.Status)

	records = firings(t, root)
	require.Len(t, records, 3)

	injected, suppressed := records[1], records[2]
	assert.Equal(t, reviews.DeliveryInjected, injected.Delivery)
	assert.Equal(t, []string{"idempotent-retries — Make retried requests idempotent."}, symptoms(injected.Fired))
	assert.Equal(t, reviews.DeliverySuppressedRepeat, suppressed.Delivery)
	assert.Equal(t, []string{"boundary-validation — Validate request payloads at the edge.", "RUF001"},
		symptoms(suppressed.Fired))
	assert.Zero(t, suppressed.ContextBytes)

	for i, rec := range []reviews.Firing{injected, suppressed} {
		assert.Equal(t, eventTwo, rec.EventID, "one event, one ID")
		assert.Equal(t, i+1, rec.EventSeq)
		assert.Equal(t, 2, rec.EventRecords)
		assert.Equal(t, "emitted", rec.Emission, "the event emitted advice")
		assert.Equal(t, cfg.GuidanceDigest(), rec.GuidanceSHA)
		require.NotNil(t, rec.PathsComplete)
		assert.False(t, *rec.PathsComplete, "the outside path is rejected")
		assert.Equal(t, 1, rec.RejectedPaths)
		assert.Equal(t, injected.MatchSHA, rec.MatchSHA)
	}

	assert.Equal(t, []reviews.FiringPin{
		pinOf(cfg, cfg.Pin[1], reviews.PinInjected, ""),
		pinOf(cfg, cfg.Pin[0], reviews.PinSuppressedRepeat, ""),
	}, injected.Pins, "the delivery orders the pins")
	assert.Empty(t, suppressed.Pins, "only the first record carries the pins")

	// Every match is suppressed: one record, and no emission was needed.
	req = claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu-3", "prompt-2", "api/handler.go"))
	req.EventID = eventThree

	out, err = Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	require.Equal(t, StatusSuppressed, out.Status)

	records = firings(t, root)
	require.Len(t, records, 4)

	last := records[3]
	assert.Equal(t, reviews.DeliverySuppressedRepeat, last.Delivery)
	assert.Equal(t, "not-needed", last.Emission)
	assert.Equal(t, 1, last.EventRecords)
	assert.Equal(t, uint64(1), last.Generation)
	assert.Equal(t, []reviews.FiringPin{
		pinOf(cfg, cfg.Pin[0], reviews.PinSuppressedRepeat, ""),
		pinOf(cfg, cfg.Pin[1], reviews.PinSuppressedRepeat, ""),
	}, last.Pins)

	// The log holds digests only, never a raw provider identity.
	raw, err := os.ReadFile(reviews.AuditLogPath(root))
	require.NoError(t, err)

	for _, id := range []string{"session-1", "toolu-1", "prompt-1", "prompt-2"} {
		assert.NotContains(t, string(raw), id)
	}
}

func TestDeliverRecordsTheOwnInjectionOfAnAction(t *testing.T) {
	// AC-9, emission side: each action gets its own injection record,
	// joined by the digest of its tool use and its turn. The digests hash
	// over the identity root, also when it differs from the root.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryAlways)

	identity := t.TempDir()

	actions := []struct {
		match, turn string
	}{
		{"toolu-a", "prompt-1"},
		{"toolu-b", "prompt-1"},
		{"toolu-c", "prompt-2"},
		{"", ""}, // an event without an action identity
	}

	for _, a := range actions {
		req := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", a.match, a.turn, "api/handler.go"))
		req.IdentityRoot = identity

		var got capture

		out, err := Deliver(context.Background(), st, req, got.emit)
		require.NoError(t, err)
		require.Equal(t, StatusEmitted, out.Status)
	}

	assert.NoFileExists(t, reviews.AuditLogPath(root), "the log lives under the identity root")

	records := firings(t, identity)
	require.Len(t, records, len(actions))

	ids := map[string]bool{}

	for i, a := range actions {
		rec := records[i]
		assert.Equal(t, reviews.DeliveryInjected, rec.Delivery)
		assert.Equal(t, reviews.SessionDigest(identity, "session-1"), rec.SessionSHA)
		assert.Equal(t, reviews.ContextDigest(identity, integration.ClaudeID, receiverOf("session-1")), rec.ContextSHA)
		assert.NotEqual(t, reviews.SessionDigest(root, "session-1"), rec.SessionSHA, "never the workspace root")

		ids[rec.EventID] = true

		if a.match == "" {
			assert.Empty(t, rec.MatchSHA, "a missing action identity stays unknown")
			assert.Empty(t, rec.TurnSHA, "a missing turn stays unknown")

			continue
		}

		assert.Equal(t, reviews.MatchDigest(identity, "session-1", a.match), rec.MatchSHA)
		assert.Equal(t, reviews.TurnDigest(identity, integration.ClaudeID, "session-1", a.turn), rec.TurnSHA)
	}

	assert.Len(t, ids, len(actions), "each action is its own event")
	assert.NotEqual(t, records[0].MatchSHA, records[1].MatchSHA, "two actions of one turn stay apart")
	assert.Equal(t, records[0].TurnSHA, records[1].TurnSHA, "two actions of one turn share the turn")
	assert.NotEqual(t, records[1].TurnSHA, records[2].TurnSHA)
}

func TestDeliverWritesNewRecordKindsOnlyUnderCaptureOptIn(t *testing.T) {
	failed := errors.New("stdout closed")

	cases := []struct {
		name       string
		setup      func(t *testing.T, root string, st *store.Store) (*store.Store, integration.EditEvent, Emit)
		wantErr    bool
		emission   string
		evaluation string
		complete   bool
		rejected   int
	}{
		{
			name: "no lesson matches",
			setup: func(_ *testing.T, root string, st *store.Store) (*store.Store, integration.EditEvent, Emit) {
				return st, claudeEvent(root, "s", "", "m", "p", "db/query.go"), mustNotEmit
			},
			emission: "not-needed", evaluation: "complete", complete: true,
		},
		{
			name: "every input path rejected",
			setup: func(_ *testing.T, root string, st *store.Store) (*store.Store, integration.EditEvent, Emit) {
				return st, claudeEvent(root, "s", "", "m", "p", "/etc/hosts", "bad\x00path"), mustNotEmit
			},
			emission: "not-needed", evaluation: "complete", complete: false, rejected: 2,
		},
		{
			name: "the selection fails",
			setup: func(t *testing.T, root string, _ *store.Store) (*store.Store, integration.EditEvent, Emit) {
				closed, err := store.OpenMemory()
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				return closed, claudeEvent(root, "s", "", "m", "p", "api/handler.go"), mustNotEmit
			},
			wantErr: true, emission: "not-needed", evaluation: "failed", complete: true,
		},
		{
			name: "the reply fails",
			setup: func(_ *testing.T, root string, st *store.Store) (*store.Store, integration.EditEvent, Emit) {
				return st, claudeEvent(root, "s", "", "m", "p", "api/handler.go"),
					func(string) error { return failed }
			},
			wantErr: true, emission: "failed", evaluation: "complete", complete: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, optIn := range []bool{false, true} {
				root := workspace(t)
				cfg := writeConfig(t, root, "pin:\n"+
					"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n")
				st, event, emit := tc.setup(t, root, lessonStore(t, root))

				req := claudeRequest(root, cfg, event)
				req.RecordEvaluations = optIn

				_, err := Deliver(context.Background(), st, req, emit)
				if tc.wantErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}

				if !optIn {
					assert.NoFileExists(t, reviews.AuditLogPath(root),
						"without capture opt-in the log gets no evaluation record")

					continue
				}

				records := firings(t, root)
				require.Len(t, records, 1)

				rec := records[0]
				assert.Equal(t, reviews.DeliveryEvaluation, rec.Delivery)
				assert.NotNil(t, rec.Fired)
				assert.Empty(t, rec.Fired, "an evaluation lists no lesson as fired")
				assert.Equal(t, tc.emission, rec.Emission)
				assert.Equal(t, tc.evaluation, rec.Evaluation)
				require.NotNil(t, rec.PathsComplete)
				assert.Equal(t, tc.complete, *rec.PathsComplete)
				assert.Equal(t, tc.rejected, rec.RejectedPaths)
				assert.True(t, rec.CaptureOptIn)
				assert.Equal(t, reviews.SessionDigest(root, "s"), rec.SessionSHA)
				assert.Equal(t, reviews.MatchDigest(root, "s", "m"), rec.MatchSHA)
				assert.Equal(t, reviews.TurnDigest(root, integration.ClaudeID, "s", "p"), rec.TurnSHA)
				assert.Equal(t, integration.LaneRoot, rec.Lane)

				if tc.emission == "failed" {
					assert.Equal(t, []reviews.FiringPin{pinOf(cfg, cfg.Pin[0], reviews.PinEmitFailed, "")}, rec.Pins,
						"the agent did not get the selected pin")
				} else {
					assert.Empty(t, rec.Pins)
				}

				// The summary readers skip the record, so the counts stay.
				summary := reviews.Summarize(records, nil)
				assert.Zero(t, summary.Total)
			}
		})
	}

	t.Run("an injection names the opt-in, and no path means no record", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)

		req := claudeRequest(root, pinnedConfig(reviews.HookDeliveryAlways),
			claudeEvent(root, "s", "", "m", "p"))
		req.RecordEvaluations = true

		_, err := Deliver(context.Background(), st, req, mustNotEmit)
		require.NoError(t, err)
		assert.NoFileExists(t, reviews.AuditLogPath(root), "an event without a path evaluates nothing")

		var got capture

		req.Event = claudeEvent(root, "s", "", "m", "p", "api/handler.go")

		_, err = Deliver(context.Background(), st, req, got.emit)
		require.NoError(t, err)

		records := firings(t, root)
		require.Len(t, records, 1)
		assert.True(t, records[0].CaptureOptIn)
		assert.Equal(t, reviews.DeliveryInjected, records[0].Delivery)
	})

	t.Run("a failed reply after a partial lease names both deliveries", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
			"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n")

		req := claudeRequest(root, cfg, claudeEvent(root, "s", "", "m1", "p", "api/handler.go"))
		req.RecordEvaluations = true

		var got capture

		_, err := Deliver(context.Background(), st, req, got.emit)
		require.NoError(t, err)

		cfg = writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
			"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n"+
			"  - {rule: idempotent-retries, region: api, note: Make retried requests idempotent.}\n")
		req = claudeRequest(root, cfg, claudeEvent(root, "s", "", "m2", "p", "api/handler.go"))
		req.RecordEvaluations = true

		_, err = Deliver(context.Background(), st, req, func(string) error { return failed })
		require.ErrorIs(t, err, failed)

		records := firings(t, root)
		require.Len(t, records, 2)

		rec := records[1]
		assert.Equal(t, reviews.DeliveryEvaluation, rec.Delivery)
		assert.Equal(t, "failed", rec.Emission)
		assert.Equal(t, uint64(1), rec.Generation)
		assert.Equal(t, []reviews.FiringPin{
			pinOf(cfg, cfg.Pin[0], reviews.PinSuppressedRepeat, ""),
			pinOf(cfg, cfg.Pin[1], reviews.PinEmitFailed, ""),
		}, rec.Pins)

		// The failed reply marked nothing delivered.
		_, err = Deliver(context.Background(), st, req, got.emit)
		require.NoError(t, err)
		assert.Contains(t, got.advice[len(got.advice)-1], "idempotent-retries")
	})
}

// mustNotEmit is an emit callback for an event with nothing to say.
func mustNotEmit(string) error {
	return errors.New("nothing to say, so the reply must not be written")
}

func TestDeliverNamesHeldBackPinsWithTheirCause(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "pin_budget: 1\npin:\n"+
		"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n"+
		"  - {rule: boundary-validation, region: '*', note: Validate request payloads at the edge.}\n"+
		"  - {rule: idempotent-retries, region: api, note: Make retried requests idempotent.}\n"+
		"  - {rule: transaction-atomicity, region: api, note: Wrap dependent writes in one transaction.}\n")

	var got capture

	out, err := Deliver(context.Background(), st,
		claudeRequest(root, cfg, claudeEvent(root, "s", "", "m", "p", "api/handler.go")), got.emit)
	require.NoError(t, err)
	assert.Equal(t, 3, out.HeldBackPins)
	require.Len(t, got.advice, 1)
	assert.Contains(t, got.advice[0], "+3 more pins", "the reminder points at the held-back pins")

	records := firings(t, root)
	require.Len(t, records, 1)

	// The collapse drops the repository-wide restatement of entry 1, and
	// the pin cap holds back the rest. The mined lesson has no pin.
	assert.Equal(t, []reviews.FiringPin{
		pinOf(cfg, cfg.Pin[0], reviews.PinInjected, ""),
		pinOf(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseRestated),
		pinOf(cfg, cfg.Pin[2], reviews.PinHeldBack, reviews.CauseOverPinCap),
		pinOf(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverPinCap),
	}, records[0].Pins)
	assert.Equal(t, []string{"boundary-validation — Validate request payloads at the edge.", "RUF001"},
		symptoms(records[0].Fired), "held-back pins stay out of the fired list")
}

func TestResetWritesAResetRecordUnderCaptureOptIn(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\n")
	identity := t.TempDir()

	reset := func(receiving integration.ReceivingContext, client string, optIn bool) ResetRequest {
		return ResetRequest{
			Root: root, IdentityRoot: identity, ClientID: client, RecordEvaluations: optIn,
			Event: integration.ResetEvent{Context: receiving, SessionID: "session-1", Turn: "prompt-9"},
		}
	}

	rootContext := *integration.ReceivingContextOf(integration.ClaudeID, "session-1", "")

	// No state yet: the reset changes nothing, and the record still says
	// that the context lost its earlier advice.
	require.NoError(t, Reset(reset(rootContext, integration.ClaudeID, true)))
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.json"), "a reset creates no state")

	records := firings(t, identity)
	require.Len(t, records, 1)
	assert.Zero(t, records[0].Generation, "no generation was reset")

	req := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu-1", "prompt-1", "api/handler.go"))
	req.IdentityRoot = identity
	req.RecordEvaluations = true

	var got capture

	_, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)

	// Without opt-in, a reset still resets and writes no record.
	require.NoError(t, Reset(reset(rootContext, integration.ClaudeID, false)))
	require.Len(t, firings(t, identity), 2)

	reset3 := reset(rootContext, integration.ClaudeID, true)
	reset3.EventID = eventThree
	require.NoError(t, Reset(reset3))

	records = firings(t, identity)
	require.Len(t, records, 3)

	edit, rec := records[1], records[2]
	assert.Equal(t, reviews.DeliveryReset, rec.Delivery)
	assert.NotNil(t, rec.Fired)
	assert.Empty(t, rec.Fired)
	assert.Equal(t, uint64(3), rec.Generation, "two resets after the first lease")
	assert.Equal(t, edit.SessionSHA, rec.SessionSHA, "the reset joins the edit records")
	assert.Equal(t, edit.ContextSHA, rec.ContextSHA)
	assert.Equal(t, reviews.ContextDigest(identity, integration.ClaudeID, rootContext.ID), rec.ContextSHA)
	assert.Equal(t, reviews.TurnDigest(identity, integration.ClaudeID, "session-1", "prompt-9"), rec.TurnSHA)
	assert.Equal(t, integration.ClaudeID, rec.Client)
	assert.Equal(t, integration.LaneRoot, rec.Lane)
	require.NotNil(t, rec.ContextResettable)
	assert.True(t, *rec.ContextResettable)
	assert.True(t, rec.CaptureOptIn)
	assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion)
	assert.Equal(t, eventThree, rec.EventID)
	assert.Equal(t, 1, rec.EventRecords)
	assert.Empty(t, rec.File)
	assert.Empty(t, rec.Files)
	assert.Empty(t, rec.Tool)
	assert.Empty(t, rec.Mechanism)
	assert.Empty(t, rec.MatchSHA)
	assert.Empty(t, rec.Emission)
	assert.Empty(t, rec.Evaluation)
	assert.Nil(t, rec.PathsComplete)
	assert.Empty(t, rec.Pins)

	// The next edit gets the new generation, so a reader joins them.
	_, err = Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)

	records = firings(t, identity)
	require.Len(t, records, 4)
	assert.Equal(t, rec.Generation, records[3].Generation)
	assert.Equal(t, reviews.DeliveryInjected, records[3].Delivery, "the reset restored the advice")

	// A context that no reset reaches, or a request without a client,
	// writes nothing even with opt-in.
	child := *integration.ReceivingContextOf(integration.ClaudeID, "session-1", "agent-7")
	require.NoError(t, Reset(reset(child, integration.ClaudeID, true)))
	require.NoError(t, Reset(reset(rootContext, "", true)))
	require.NoError(t, Reset(reset(integration.ReceivingContext{Resettable: true}, integration.ClaudeID, true)))
	assert.Len(t, firings(t, identity), 4)

	// The reset record is not a delivery for the summary readers.
	assert.Equal(t, 2, reviews.Summarize(firings(t, identity), nil).Total)

	raw, err := os.ReadFile(reviews.AuditLogPath(identity))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), "prompt-9"), "the raw turn never persists")
}
