package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold exploration delivery and its firing
// records. Exploration never reads or writes the edit hook state.

// exploreMechanism is the reply path of the Claude explore adapter.
const exploreMechanism = "post-tool-use-context"

// apiPin pins one rule on api, the region of the mined lessons of
// lessonStore.
const apiPin = "pin:\n  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n"

// readEvent builds a structured read event as the Claude adapter decodes
// it: the receiver comes from ReceivingContextOf, so it has its lane.
func readEvent(root, session, agent, match, path string, read bool) integration.ExploreEvent {
	return integration.ExploreEvent{
		EventMeta: integration.EventMeta{
			SessionID: session, MatchID: match, NativeTool: "Read", CWD: root, Turn: "prompt-1",
			Context: integration.ReceivingContextOf(integration.ClaudeID, session, agent),
		},
		Paths: []string{path},
		Read:  read,
	}
}

// exploreRequest is an exploration request of the Claude client.
func exploreRequest(root string, cfg *reviews.Config, event integration.ExploreEvent) ExploreRequest {
	return ExploreRequest{
		Root: root, ClientID: integration.ClaudeID, Mechanism: exploreMechanism, Event: event, Config: cfg,
	}
}

// eventIDOf returns an EventID function with a fixed ID.
func eventIDOf(id string) func() string {
	return func() string { return id }
}

// explore runs Explore with a fresh capture and returns the outcome and
// the advice texts.
func explore(t *testing.T, st *store.Store, req ExploreRequest) (out ExploreOutcome, advice []string) {
	t.Helper()

	var got capture

	out, err := Explore(context.Background(), st, req, got.emit)
	require.NoError(t, err)

	return out, got.advice
}

// editStatePath and exploreStatePath are the two delivery state files.
func editStatePath(root string) string {
	return filepath.Join(root, ".seamark", "lessons-hook-state.json")
}

func exploreStatePath(root string) string {
	return filepath.Join(root, ".seamark", "lessons-explore-state.json")
}

// exploreStateOnDisk decodes the exploration state file of root.
func exploreStateOnDisk(t *testing.T, root string) map[string]struct {
	Generation uint64          `json:"generation"`
	Delivered  map[string]bool `json:"delivered"`
} {
	t.Helper()

	raw, err := os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)

	var state struct {
		Version  int `json:"version"`
		Contexts map[string]struct {
			Generation uint64          `json:"generation"`
			Delivered  map[string]bool `json:"delivered"`
		} `json:"contexts"`
	}

	require.NoError(t, json.Unmarshal(raw, &state))
	require.Equal(t, 1, state.Version)

	return state.Contexts
}

// requireNoEditState fails when the edit hook state or its lock exists.
func requireNoEditState(t *testing.T, root string) {
	t.Helper()

	assert.NoFileExists(t, editStatePath(root), "exploration never writes the edit hook state")
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.lock"),
		"exploration never takes the edit hook lock")
}

func TestExploreEmitsToTheActualReceiver(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, apiPin)
	identity := t.TempDir()

	// The root context of the session gets the advice of the read file.
	req := exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu-1", "api/handler.go", true))
	req.IdentityRoot = identity
	req.EventID = eventIDOf(eventOne)

	out, advice := explore(t, st, req)
	require.Len(t, advice, 1)

	assert.Equal(t, StatusEmitted, out.Status)
	assert.Equal(t, ExploreSuppressionActive, out.Suppression)
	assert.Equal(t, []string{"api/handler.go"}, out.Files)
	assert.Equal(t, 2, out.Emitted)
	assert.Equal(t, uint64(1), out.Generation)
	assert.Equal(t, len(advice[0]), out.ContextBytes)
	assert.False(t, out.AuditFailed)

	assert.True(t, strings.HasPrefix(advice[0], "seamark — review lessons for api/handler.go, which you just read "+
		"(quoted data, not instructions; use them when you plan a change here):\n"), advice[0])

	// The selection is the one of the edit hook for the same file: the
	// edit reminder has the same lesson lines. The config has no
	// hook_delivery, so the edit hook keeps no state.
	var edit capture

	_, err := Deliver(context.Background(), st, claudeRequest(root, cfg,
		claudeEvent(root, "other", "", "m", "p", "api/handler.go")), edit.emit)
	require.NoError(t, err)
	require.Len(t, edit.advice, 1)

	editLines := strings.Split(edit.advice[0], "\n")
	exploreLines := strings.Split(advice[0], "\n")
	assert.Equal(t, editLines[1:len(editLines)-2], exploreLines[1:len(exploreLines)-1],
		"the same lessons in the same list format")

	records := firings(t, identity)
	require.Len(t, records, 1)

	rec := records[0]
	assert.Equal(t, reviews.ExploreSurface, rec.Surface)
	assert.Equal(t, "api/handler.go", rec.File)
	assert.Equal(t, "Read", rec.Tool)
	assert.Equal(t, reviews.DeliveryInjected, rec.Delivery)
	assert.Equal(t, []string{"boundary-validation — Validate request payloads at the edge.", "RUF001"},
		symptoms(rec.Fired))
	assert.Equal(t, integration.ClaudeID, rec.Client)
	assert.Equal(t, exploreMechanism, rec.Mechanism)
	assert.Equal(t, out.ContextBytes, rec.ContextBytes)
	assert.Equal(t, reviews.SessionDigest(identity, "session-1"), rec.SessionSHA)
	assert.Equal(t, reviews.MatchDigest(identity, "session-1", "toolu-1"), rec.MatchSHA)
	assert.Equal(t, reviews.ContextDigest(identity, integration.ClaudeID, receiverOf("session-1")), rec.ContextSHA)
	assert.Equal(t, reviews.TurnDigest(identity, integration.ClaudeID, "session-1", "prompt-1"), rec.TurnSHA)
	assert.Equal(t, integration.LaneRoot, rec.Lane)
	require.NotNil(t, rec.ContextResettable)
	assert.True(t, *rec.ContextResettable)
	assert.Equal(t, uint64(1), rec.ExploreGeneration)
	assert.Zero(t, rec.Generation, "context_generation belongs to the edit hook lease")
	require.NotNil(t, rec.Read)
	assert.True(t, *rec.Read)
	assert.Equal(t, "emitted", rec.Emission)
	assert.Equal(t, "complete", rec.Evaluation)
	require.NotNil(t, rec.PathsComplete)
	assert.True(t, *rec.PathsComplete)
	assert.Equal(t, cfg.GuidanceDigest(), rec.GuidanceSHA)
	assert.Equal(t, "ok", rec.GuidanceStatus)
	assert.False(t, rec.RecordEvaluations)
	assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion)
	assert.Equal(t, eventOne, rec.EventID)
	assert.Equal(t, 1, rec.EventSeq)
	assert.Equal(t, 1, rec.EventRecords)
	assert.Empty(t, rec.ScopeSHA)
	assert.Equal(t, []reviews.FiringPin{pinOf(cfg, cfg.Pin[0], reviews.PinInjected, "")}, rec.Pins)

	// A subagent of the same session is another receiver: it gets the
	// advice, although the root context already has it.
	child := exploreRequest(root, cfg, readEvent(root, "session-1", "agent-7", "toolu-2", "api/handler.go", true))
	child.IdentityRoot = identity

	out, advice = explore(t, st, child)
	require.Len(t, advice, 1)
	assert.Equal(t, ExploreSuppressionNotResettable, out.Suppression)
	assert.Zero(t, out.Generation)

	childContext := integration.ReceivingContextOf(integration.ClaudeID, "session-1", "agent-7")

	records = firings(t, identity)
	require.Len(t, records, 2)
	assert.Equal(t, integration.LaneChild, records[1].Lane)
	assert.Equal(t, reviews.ContextDigest(identity, integration.ClaudeID, childContext.ID), records[1].ContextSHA)
	require.NotNil(t, records[1].ContextResettable)
	assert.False(t, *records[1].ContextResettable)
	assert.Zero(t, records[1].ExploreGeneration, "no lease, no generation")

	// The state holds the root context only, keyed under Root.
	contexts := exploreStateOnDisk(t, root)
	require.Len(t, contexts, 1)
	assert.Contains(t, contexts, reviews.ContextDigest(root, integration.ClaudeID, receiverOf("session-1")))

	requireNoEditState(t, root)

	// The hook budget holds pins back as for the edit hook, and the
	// record names each held-back pin with its cause.
	budgeted := workspace(t)
	st = lessonStore(t, budgeted)
	cfg = writeConfig(t, budgeted, "pin_budget: 1\n"+apiPin+
		"  - {rule: idempotent-handlers, region: api, note: Make every handler safe to retry.}\n")

	out, advice = explore(t, st,
		exploreRequest(budgeted, cfg, readEvent(budgeted, "session-1", "", "toolu", "api/handler.go", true)))
	require.Len(t, advice, 1)
	assert.Equal(t, 1, out.HeldBackPins)
	assert.Contains(t, advice[0], "boundary-validation")
	assert.NotContains(t, advice[0], "idempotent-handlers")

	records = firings(t, budgeted)
	require.Len(t, records, 1)
	assert.Equal(t, []reviews.FiringPin{
		pinOf(cfg, cfg.Pin[0], reviews.PinInjected, ""),
		pinOf(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseOverPinCap),
	}, records[0].Pins)
}

func TestExploreDedupesByReceiverGenerationVersionAndSurface(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	// hook_delivery configures the edit hook only. Exploration dedupes
	// a resettable receiver also under the default "always".
	cfg := writeConfig(t, root, "hook_delivery: always\n"+apiPin)

	read := func(session string) (ExploreOutcome, []string) {
		t.Helper()

		return explore(t, st, exploreRequest(root, cfg, readEvent(root, session, "", "toolu", "api/handler.go", true)))
	}

	out, advice := read("session-1")
	require.Len(t, advice, 1)
	assert.Equal(t, 2, out.Emitted)

	pinItem := reviews.ExploreItemDigest(reviews.ExploreSurface, reviews.ExploreItemPin, cfg.PinVersion(cfg.Pin[0]))
	minedItem := reviews.ExploreItemDigest(reviews.ExploreSurface, reviews.ExploreItemLesson, "api\x00RUF001")
	key := reviews.ContextDigest(root, integration.ClaudeID, receiverOf("session-1"))

	contexts := exploreStateOnDisk(t, root)
	assert.Equal(t, map[string]bool{pinItem: true, minedItem: true}, contexts[key].Delivered,
		"the state keys each item by surface, kind, and pin version or cluster")

	// The same receiver in the same generation: nothing to emit.
	out, advice = read("session-1")
	assert.Empty(t, advice)
	assert.Equal(t, StatusSuppressed, out.Status)
	assert.Equal(t, 2, out.Suppressed)
	assert.Equal(t, uint64(1), out.Generation)

	// Another receiver gets the advice.
	_, advice = read("session-2")
	assert.Len(t, advice, 1)

	// A reset starts a new generation, and the advice comes again.
	rootContext := *integration.ReceivingContextOf(integration.ClaudeID, "session-1", "")
	require.NoError(t, Reset(ResetRequest{
		Root: root, ClientID: integration.ClaudeID, Event: integration.ResetEvent{Context: rootContext},
	}))

	out, advice = read("session-1")
	require.Len(t, advice, 1)
	assert.Equal(t, uint64(2), out.Generation)
	assert.Equal(t, 2, out.Emitted)

	// A new pin version reaches the receiver again. The mined lesson
	// keeps its key, so it stays suppressed.
	cfg = writeConfig(t, root, "hook_delivery: always\n"+
		"pin:\n  - {rule: boundary-validation, region: api, note: Validate every payload at the edge.}\n")

	out, advice = read("session-1")
	require.Len(t, advice, 1)
	assert.Contains(t, advice[0], "Validate every payload at the edge.")
	assert.NotContains(t, advice[0], "RUF001")
	assert.Equal(t, 1, out.Emitted)
	assert.Equal(t, 1, out.Suppressed)

	// The surfaces stay apart: an edit hook delivery in once-per-context
	// mode still gets the pin after the reads, and it changes nothing in
	// the exploration state.
	requireNoEditState(t, root)

	before, err := os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)

	editCfg := writeConfig(t, root, "hook_delivery: once-per-context\n"+
		"pin:\n  - {rule: boundary-validation, region: api, note: Validate every payload at the edge.}\n")

	var edit capture

	_, err = Deliver(context.Background(), st,
		claudeRequest(root, editCfg, claudeEvent(root, "session-1", "", "m", "p", "api/handler.go")), edit.emit)
	require.NoError(t, err)
	require.Len(t, edit.advice, 1)
	assert.Contains(t, edit.advice[0], "Validate every payload at the edge.")
	assert.FileExists(t, editStatePath(root))

	after, err := os.ReadFile(exploreStatePath(root))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the edit hook never writes the exploration state")

	// The edit delivery does not suppress exploration, and exploration
	// does not consult the edit state: the next read of a new receiver
	// still emits.
	_, advice = read("session-3")
	assert.Len(t, advice, 1)

	// Each emitting read wrote one injected record, also the read that
	// emitted one item and suppressed one: an explore event has no
	// second, suppressed-repeat record.
	var explored []reviews.DeliveryStatus

	for _, rec := range firings(t, root) {
		if rec.Surface == reviews.ExploreSurface {
			explored = append(explored, rec.Delivery)
			assert.Equal(t, 1, rec.EventRecords)
		}
	}

	assert.Equal(t, []reviews.DeliveryStatus{
		reviews.DeliveryInjected, reviews.DeliveryInjected, reviews.DeliveryInjected,
		reviews.DeliveryInjected, reviews.DeliveryInjected,
	}, explored, "session-1, session-2, after the reset, the new version, session-3")
}

func TestExploreNeverSuppressesWithoutAResettableContext(t *testing.T) {
	codexContext := integration.ReceivingContextOf(integration.CodexID, "session-1", "")
	require.NotNil(t, codexContext)
	require.False(t, codexContext.Resettable)

	cases := []struct {
		name    string
		context *integration.ReceivingContext
		want    ExploreSuppression
	}{
		{"a subagent", integration.ReceivingContextOf(integration.ClaudeID, "session-1", "agent-7"), ExploreSuppressionNotResettable},
		{"no session", integration.ReceivingContextOf(integration.ClaudeID, "", ""), ExploreSuppressionNoContext},
		{"a receiver without an ID", &integration.ReceivingContext{Resettable: true}, ExploreSuppressionNoContext},
		{"a receiver that no reset reaches", codexContext, ExploreSuppressionNotResettable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := workspace(t)
			st := lessonStore(t, root)
			cfg := writeConfig(t, root, apiPin)

			event := readEvent(root, "session-1", "", "toolu", "api/handler.go", true)
			event.Context = tc.context

			for range 2 {
				out, advice := explore(t, st, exploreRequest(root, cfg, event))
				assert.Len(t, advice, 1, "every read emits")
				assert.Equal(t, tc.want, out.Suppression)
				assert.Zero(t, out.Generation)
			}

			assert.NoFileExists(t, exploreStatePath(root), "no lease, no state")
			requireNoEditState(t, root)
		})
	}

	t.Run("a state that this binary cannot read", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, apiPin)

		newer := []byte(`{"version":2,"contexts":{}}` + "\n")
		require.NoError(t, os.WriteFile(exploreStatePath(root), newer, 0o600))

		req := exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu", "api/handler.go", true))

		for range 2 {
			out, advice := explore(t, st, req)
			assert.Len(t, advice, 1)
			assert.Equal(t, ExploreSuppressionStateUnavailable, out.Suppression)
		}

		raw, err := os.ReadFile(exploreStatePath(root))
		require.NoError(t, err)
		assert.Equal(t, newer, raw, "the reader never overwrites a state that it cannot read")
		requireNoEditState(t, root)
	})

	t.Run("a busy lock", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, apiPin)

		held, err := reviews.BeginExploreDelivery(root,
			reviews.DeliveryContext{ClientID: integration.ClaudeID, ReceiverID: "other"},
			[]reviews.ExploreItem{{Kind: reviews.ExploreItemPin, ID: "v"}})
		require.NoError(t, err)

		defer func() { _ = held.Close() }()

		out, advice := explore(t, st,
			exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu", "api/handler.go", true)))
		assert.Len(t, advice, 1)
		assert.Equal(t, ExploreSuppressionStateUnavailable, out.Suppression)
	})
}

func TestExploreRecordsEvaluationsOnlyUnderTheRecordingSwitch(t *testing.T) {
	failed := errors.New("stdout closed")

	type setup struct {
		st    *store.Store
		event integration.ExploreEvent
		emit  Emit
	}

	cases := []struct {
		name       string
		setup      func(t *testing.T, root string, st *store.Store, cfg *reviews.Config) setup
		wantErr    bool
		emission   string
		evaluation string
		complete   bool
		rejected   int
		pins       []reviews.PinDelivery
	}{
		{
			name: "no lesson matches",
			setup: func(_ *testing.T, root string, st *store.Store, _ *reviews.Config) setup {
				return setup{st, readEvent(root, "s", "", "m", "db/query.go", true), mustNotEmit}
			},
			emission: "not-needed", evaluation: "complete", complete: true,
		},
		{
			name: "the path is rejected",
			setup: func(_ *testing.T, root string, st *store.Store, _ *reviews.Config) setup {
				return setup{st, readEvent(root, "s", "", "m", "/etc/hosts", true), mustNotEmit}
			},
			emission: "not-needed", evaluation: "complete", complete: false, rejected: 1,
		},
		{
			name: "the selection fails",
			setup: func(t *testing.T, root string, _ *store.Store, _ *reviews.Config) setup {
				closed, err := store.OpenMemory()
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				return setup{closed, readEvent(root, "s", "", "m", "api/handler.go", true), mustNotEmit}
			},
			wantErr: true, emission: "not-needed", evaluation: "failed", complete: true,
		},
		{
			name: "every item was delivered before",
			setup: func(t *testing.T, root string, st *store.Store, cfg *reviews.Config) setup {
				event := readEvent(root, "s", "", "m", "api/handler.go", true)
				// The first read emits, and the test log keeps only the
				// record of the second read.
				_, advice := explore(t, st, exploreRequest(root, cfg, event))
				require.Len(t, advice, 1)
				require.NoError(t, os.Remove(reviews.AuditLogPath(root)))

				return setup{st, event, mustNotEmit}
			},
			emission: "not-needed", evaluation: "complete", complete: true,
			pins: []reviews.PinDelivery{reviews.PinSuppressedRepeat},
		},
		{
			name: "the reply fails",
			setup: func(_ *testing.T, root string, st *store.Store, _ *reviews.Config) setup {
				return setup{st, readEvent(root, "s", "", "m", "api/handler.go", true),
					func(string) error { return failed }}
			},
			wantErr: true, emission: "failed", evaluation: "complete", complete: true,
			pins: []reviews.PinDelivery{reviews.PinEmitFailed},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, recording := range []bool{false, true} {
				root := workspace(t)
				st := lessonStore(t, root)
				cfg := writeConfig(t, root, apiPin)
				s := tc.setup(t, root, st, cfg)

				req := exploreRequest(root, cfg, s.event)
				req.RecordEvaluations = recording

				out, err := Explore(context.Background(), s.st, req, s.emit)
				if tc.wantErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}

				assert.NotEqual(t, StatusEmitted, out.Status)

				records := firings(t, root)
				if !recording {
					assert.Empty(t, records, "with the recording switch off, nothing emitted means no record")
					continue
				}

				require.Len(t, records, 1)

				rec := records[0]
				assert.Equal(t, reviews.DeliveryEvaluation, rec.Delivery)
				assert.Equal(t, reviews.ExploreSurface, rec.Surface)
				assert.NotNil(t, rec.Fired)
				assert.Empty(t, rec.Fired)
				assert.Equal(t, tc.emission, rec.Emission)
				assert.Equal(t, tc.evaluation, rec.Evaluation)
				require.NotNil(t, rec.PathsComplete)
				assert.Equal(t, tc.complete, *rec.PathsComplete)
				assert.Equal(t, tc.rejected, rec.RejectedPaths)
				assert.True(t, rec.RecordEvaluations)
				require.NotNil(t, rec.Read)
				assert.True(t, *rec.Read)
				assert.Equal(t, exploreMechanism, rec.Mechanism)
				assert.Equal(t, 1, rec.EventRecords)

				var deliveries []reviews.PinDelivery
				for _, p := range rec.Pins {
					deliveries = append(deliveries, p.Delivery)
				}

				assert.Equal(t, tc.pins, deliveries)
			}
		})
	}

	t.Run("a failed reply marks nothing delivered", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, apiPin)
		req := exploreRequest(root, cfg, readEvent(root, "s", "", "m", "api/handler.go", true))

		_, err := Explore(context.Background(), st, req, func(string) error { return failed })

		var emitErr *EmitError
		require.ErrorAs(t, err, &emitErr)
		assert.ErrorIs(t, err, failed)

		_, advice := explore(t, st, req)
		assert.Len(t, advice, 1, "the advice stays eligible")
	})

	t.Run("an injection is recorded with the recording switch on or off", func(t *testing.T) {
		for _, recording := range []bool{false, true} {
			root := workspace(t)
			st := lessonStore(t, root)
			cfg := writeConfig(t, root, apiPin)

			req := exploreRequest(root, cfg, readEvent(root, "s", "", "m", "api/handler.go", true))
			req.RecordEvaluations = recording

			_, advice := explore(t, st, req)
			require.Len(t, advice, 1)

			records := firings(t, root)
			require.Len(t, records, 1)
			assert.Equal(t, reviews.DeliveryInjected, records[0].Delivery)
			assert.Equal(t, recording, records[0].RecordEvaluations)
		}
	})

	t.Run("an event without exactly one path writes nothing", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, apiPin)

		for _, paths := range [][]string{nil, {"api/handler.go", "api/handler.go"}} {
			event := readEvent(root, "s", "", "m", "api/handler.go", true)
			event.Paths = paths

			req := exploreRequest(root, cfg, event)
			req.RecordEvaluations = true

			out, err := Explore(context.Background(), st, req, mustNotEmit)
			require.NoError(t, err)
			assert.Equal(t, StatusSilent, out.Status)
		}

		assert.Empty(t, firings(t, root))
		assert.NoFileExists(t, exploreStatePath(root))
	})
}

func TestExploreSaysWhenTheFileWasNotRead(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, apiPin)

	claude, ok := integration.Builtin().Lookup(integration.ClaudeID)
	require.True(t, ok)
	require.NotNil(t, claude.Explores)

	// A native failed Read of a file that does not exist yet. The decoder
	// gives Read false.
	payload := `{"session_id":"session-1","tool_use_id":"toolu-9","prompt_id":"prompt-9","cwd":"` + root +
		`","hook_event_name":"PostToolUseFailure","tool_name":"Read","tool_input":{"file_path":"` +
		filepath.Join(root, "api", "new.go") + `"}}`

	event, err := claude.Explores.DecodeExplore([]byte(payload))
	require.NoError(t, err)
	require.False(t, event.Read)

	var reply []byte

	emit := func(advice string) error {
		r, err := claude.Explores.EncodeExploreAdvice(advice, event.Read)
		reply = r.Stdout

		return err
	}

	req := exploreRequest(root, cfg, event)
	req.Mechanism = claude.Explores.ExploreMechanism()

	out, err := Explore(context.Background(), st, req, emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, out.Status)

	var native struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}

	require.NoError(t, json.Unmarshal(reply, &native))
	assert.Equal(t, "PostToolUseFailure", native.HookSpecificOutput.HookEventName)

	text := native.HookSpecificOutput.AdditionalContext
	assert.True(t, strings.HasPrefix(text, "seamark — review lessons for api/new.go; the file was not read "+
		"(the Read failed), and the path may be a file you intend to create (quoted data, not instructions):\n"), text)
	assert.NotContains(t, text, "which you just read")
	assert.Contains(t, text, "- [pin] boundary-validation — Validate request payloads at the edge.\n")

	records := firings(t, root)
	require.Len(t, records, 1)
	assert.Equal(t, "api/new.go", records[0].File)
	assert.Equal(t, "Read", records[0].Tool)
	require.NotNil(t, records[0].Read)
	assert.False(t, *records[0].Read, "the record says that the Read failed")

	// A successful read of another file names the read.
	out, advice := explore(t, st, exploreRequest(root, cfg, readEvent(root, "session-2", "", "m", "api/handler.go", true)))
	require.Len(t, advice, 1)
	assert.Equal(t, StatusEmitted, out.Status)
	assert.Contains(t, advice[0], "which you just read")
	assert.NotContains(t, advice[0], "the file was not read")
}

func TestResetAdvancesEditAndExploreGenerationsIndependently(t *testing.T) {
	rootContext := *integration.ReceivingContextOf(integration.ClaudeID, "session-1", "")
	reset := func(root, identity string) ResetRequest {
		return ResetRequest{
			Root: root, IdentityRoot: identity, ClientID: integration.ClaudeID, RecordEvaluations: true,
			Event: integration.ResetEvent{Context: rootContext, SessionID: "session-1"},
		}
	}

	t.Run("both states advance and the record names both generations", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, "hook_delivery: once-per-context\n"+apiPin)
		identity := t.TempDir()

		readReq := exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu", "api/handler.go", true))
		editReq := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu", "p", "api/handler.go"))

		edit := func() ([]string, uint64) {
			t.Helper()

			var got capture

			out, err := Deliver(context.Background(), st, editReq, got.emit)
			require.NoError(t, err)

			return got.advice, out.Generation
		}

		out, advice := explore(t, st, readReq)
		require.Len(t, advice, 1)
		assert.Equal(t, uint64(1), out.Generation)

		editAdvice, editGeneration := edit()
		require.Len(t, editAdvice, 1, "an edit right after the read still gets its reminder")
		assert.Equal(t, uint64(1), editGeneration)

		_, advice = explore(t, st, readReq)
		assert.Empty(t, advice)

		editAdvice, _ = edit()
		assert.Empty(t, editAdvice)

		require.NoError(t, Reset(reset(root, identity)))

		records := firings(t, identity)
		require.Len(t, records, 1)
		assert.Equal(t, reviews.DeliveryReset, records[0].Delivery)
		assert.Equal(t, uint64(2), records[0].Generation)
		assert.Equal(t, uint64(2), records[0].ExploreGeneration)

		out, advice = explore(t, st, readReq)
		require.Len(t, advice, 1)
		assert.Equal(t, uint64(2), out.Generation)

		editAdvice, editGeneration = edit()
		require.Len(t, editAdvice, 1)
		assert.Equal(t, uint64(2), editGeneration)

		// A second reset after more reads advances both again, each from
		// its own value.
		require.NoError(t, Reset(reset(root, identity)))

		records = firings(t, identity)
		assert.Equal(t, uint64(3), records[len(records)-1].Generation)
		assert.Equal(t, uint64(3), records[len(records)-1].ExploreGeneration)
	})

	t.Run("only exploration state", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, apiPin)
		identity := t.TempDir()

		_, advice := explore(t, st,
			exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu", "api/handler.go", true)))
		require.Len(t, advice, 1)

		require.NoError(t, Reset(reset(root, identity)))

		requireNoEditState(t, root)

		records := firings(t, identity)
		require.Len(t, records, 1)
		assert.Zero(t, records[0].Generation, "no edit state, no edit generation")
		assert.Equal(t, uint64(2), records[0].ExploreGeneration)
	})

	t.Run("only edit state", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		cfg := writeConfig(t, root, "hook_delivery: once-per-context\n"+apiPin)
		identity := t.TempDir()

		var got capture

		_, err := Deliver(context.Background(), st,
			claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu", "p", "api/handler.go")), got.emit)
		require.NoError(t, err)

		require.NoError(t, Reset(reset(root, identity)))

		assert.NoFileExists(t, exploreStatePath(root), "a reset creates no exploration state")
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-explore-state.lock"))

		records := firings(t, identity)
		require.Len(t, records, 1)
		assert.Equal(t, uint64(2), records[0].Generation)
		assert.Zero(t, records[0].ExploreGeneration)
	})

	t.Run("a failed reset of one state still resets the other", func(t *testing.T) {
		for _, broken := range []string{"lessons-hook-state.json", "lessons-explore-state.json"} {
			root := workspace(t)
			st := lessonStore(t, root)
			cfg := writeConfig(t, root, "hook_delivery: once-per-context\n"+apiPin)
			identity := t.TempDir()

			readReq := exploreRequest(root, cfg, readEvent(root, "session-1", "", "toolu", "api/handler.go", true))
			editReq := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "toolu", "p", "api/handler.go"))

			var got capture

			_, advice := explore(t, st, readReq)
			require.Len(t, advice, 1)

			_, err := Deliver(context.Background(), st, editReq, got.emit)
			require.NoError(t, err)

			// A newer version that this binary refuses to read or write.
			newer := []byte(`{"version":99}` + "\n")
			require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", broken), newer, 0o600))

			assert.Error(t, Reset(reset(root, identity)), "%s: the state error is returned", broken)

			records := firings(t, identity)
			require.Len(t, records, 1, broken)

			if broken == "lessons-hook-state.json" {
				assert.Zero(t, records[0].Generation)
				assert.Equal(t, uint64(2), records[0].ExploreGeneration, "exploration still resets")
			} else {
				assert.Equal(t, uint64(2), records[0].Generation, "the edit state still resets")
				assert.Zero(t, records[0].ExploreGeneration)
			}

			raw, err := os.ReadFile(filepath.Join(root, ".seamark", broken))
			require.NoError(t, err)
			assert.Equal(t, newer, raw, "%s: never overwritten", broken)
		}
	})
}
