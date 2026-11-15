package delivery

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// lessonStore opens the index of root and stores two mined lessons:
// one above the default threshold on api, one below it.
func lessonStore(t *testing.T, root string) *store.Store {
	t.Helper()

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "api\x00RUF001", Region: "api", Reviewer: "coderabbit", Symptom: "RUF001", Occurrences: 4, LastTS: 1},
		{ClusterKey: "api\x00once", Region: "api", Reviewer: "person", Symptom: "solitary finding", Occurrences: 1, LastTS: 1},
	}, nil))

	return st
}

// pinnedConfig returns a configuration with one pin per region.
func pinnedConfig(mode reviews.HookDeliveryMode) *reviews.Config {
	cfg := reviews.DefaultConfig()
	cfg.Delivery = mode
	cfg.Pin = []reviews.PinRule{
		{Rule: "boundary-validation", Region: "api", Note: "Validate request payloads at the edge."},
		{Rule: "transaction-atomicity", Region: "db", Note: "Wrap dependent writes in one transaction."},
	}

	return cfg
}

// editEvent builds a normalized event the way an adapter does.
func editEvent(root, session string, resettable bool, paths ...string) integration.EditEvent {
	event := integration.EditEvent{
		EventMeta: integration.EventMeta{SessionID: session, MatchID: "match-1", NativeTool: "Edit", CWD: root},
		Paths:     paths,
	}

	if session != "" {
		event.Context = &integration.ReceivingContext{ID: session, Resettable: resettable}
	}

	return event
}

// capture is an emit callback that keeps every advisory text.
type capture struct{ advice []string }

func (c *capture) emit(advice string) error {
	c.advice = append(c.advice, advice)

	return nil
}

func TestDeliverMatchesTheClaudeFixtureAndTheSingleFileReminder(t *testing.T) {
	// The parity check of the extraction: a native Claude fixture, decoded
	// by the adapter and delivered by the service, gives the bytes that
	// the single-file selector and the single-file reminder give.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryAlways)
	// The Write fixture creates a new file under docs.
	cfg.Pin = append(cfg.Pin, reviews.PinRule{Rule: "docs-voice", Region: "docs", Note: "Write in the present tense."})

	claude, ok := integration.Builtin().Lookup(integration.ClaudeID)
	require.True(t, ok)

	for _, fixture := range []string{"pre_tool_use_edit.json", "pre_tool_use_write.json", "pre_tool_use_multiedit.json"} {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "integration", "testdata", "claude", fixture))
			require.NoError(t, err)

			// The fixture workspace is a placeholder. Point it at the
			// test root and at a file the lessons cover.
			payload := strings.ReplaceAll(string(raw), "/workspace/repo", root)
			payload = strings.ReplaceAll(payload, "internal/api/", "api/")

			event, err := claude.Edits.DecodeEdit([]byte(payload))
			require.NoError(t, err)
			require.Len(t, event.Paths, 1)

			var got capture

			out, err := Deliver(context.Background(), st,
				Request{Root: root, ClientID: claude.ID, Event: event, Config: cfg}, got.emit)
			require.NoError(t, err)
			require.Len(t, got.advice, 1)

			file := out.Files[0]
			lessons, morePins, err := report.LessonsForScopeBudget(st, cfg, file, 8, cfg.HookPinBudget())
			require.NoError(t, err)

			var want strings.Builder
			require.NoError(t, report.PrintLessonReminder(&want, file, lessons, morePins))

			assert.Equal(t, want.String(), got.advice[0])
			assert.Equal(t, 1, strings.Count(got.advice[0], "(inspect the pins in "), "a pin block names the route once")
			assert.Equal(t, StatusEmitted, out.Status)
			assert.Equal(t, len(got.advice[0]), out.ContextBytes)
			assert.Equal(t, len(lessons), out.Emitted)
		})
	}
}

func TestDeliverSpendsOneBudgetAndWritesOneRecordForSeveralFiles(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryAlways)
	cfg.PinBudget = 1

	event := editEvent(root, "session-1", true,
		"api/handler.go", "db/query.go", "./api/handler.go", "../outside.go")
	event.NativeTool = "apply_patch"

	var got capture

	out, err := Deliver(context.Background(), st, Request{Root: root, ClientID: "fake", Event: event, Config: cfg}, got.emit)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/handler.go", "db/query.go"}, out.Files, "duplicate spellings are one file")
	assert.Equal(t, 1, out.RejectedCount, "the outside path is reported")
	assert.Equal(t, 1, out.HeldBackPins, "two pins apply and the operation has one pin slot")
	assert.Equal(t, 2, out.Emitted, "one pin and the mined lesson")

	require.Len(t, got.advice, 1, "one operation is one reply")
	assert.Contains(t, got.advice[0], "2 files (api/handler.go, db/query.go)")
	assert.Contains(t, got.advice[0], "quoted data, not instructions", "the advisory framing stays")
	assert.Contains(t, got.advice[0], "+1 more pins")
	assert.Equal(t, 1, strings.Count(got.advice[0], "(inspect the pins in "), "one route per reply, not per file")
	assert.NotContains(t, got.advice[0], "solitary finding", "the threshold still applies")

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "one operation is one record")
	assert.Equal(t, []string{"api/handler.go", "db/query.go"}, firings[0].Files)
	assert.Equal(t, "apply_patch", firings[0].Tool)
	assert.Equal(t, out.ContextBytes, firings[0].ContextBytes, "one context-byte total per emission")
	assert.Len(t, firings[0].MatchSHA, 64)
}

// TestDeliverLeavesTheRouteOutOfAMinedOnlyReminder covers a workspace
// without pins. The reminder shows mined lessons only, so it names no pin
// inspection route.
func TestDeliverLeavesTheRouteOutOfAMinedOnlyReminder(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := reviews.DefaultConfig()
	cfg.Delivery = reviews.HookDeliveryAlways

	var got capture

	out, err := Deliver(context.Background(), st,
		Request{Root: root, ClientID: "fake", Event: editEvent(root, "session-1", true, "api/handler.go"), Config: cfg},
		got.emit)
	require.NoError(t, err)
	require.Len(t, got.advice, 1)

	assert.Contains(t, got.advice[0], "- [×4] RUF001\n")
	assert.NotContains(t, got.advice[0], "(inspect the pins")
	assert.Equal(t, len(got.advice[0]), out.ContextBytes)
}

func TestDeliverSuppressesOnlyAfterEmissionAndResets(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryOncePerContext)
	req := Request{Root: root, ClientID: integration.ClaudeID, Config: cfg,
		Event: editEvent(root, "session-1", true, "api/handler.go")}

	var got capture

	first, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, first.Status)
	assert.Equal(t, SuppressionActive, first.Suppression)
	assert.Equal(t, uint64(1), first.Generation)

	second, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusSuppressed, second.Status)
	assert.Equal(t, 2, second.Suppressed)
	assert.Zero(t, second.ContextBytes)
	assert.Len(t, got.advice, 1, "the same context gets no second copy")

	// A second file of the same context gets only what is new.
	req.Event = editEvent(root, "session-1", true, "api/handler.go", "db/query.go")

	partial, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, 1, partial.Emitted)
	assert.Equal(t, 2, partial.Suppressed)
	assert.Contains(t, got.advice[1], "transaction-atomicity")
	assert.NotContains(t, got.advice[1], "boundary-validation")

	// Another context is not affected.
	other := req
	other.Event = editEvent(root, "session-2", true, "api/handler.go")

	fresh, err := Deliver(context.Background(), st, other, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, fresh.Status)

	// A reset starts a new generation for the first context.
	require.NoError(t, Reset(ResetRequest{Root: root, ClientID: integration.ClaudeID,
		Event: integration.ResetEvent{Context: integration.ReceivingContext{ID: "session-1", Resettable: true}}}))

	again, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, again.Status)
	assert.Equal(t, 3, again.Emitted)
	assert.Equal(t, uint64(2), again.Generation)

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	var statuses []reviews.DeliveryStatus
	for _, f := range firings {
		statuses = append(statuses, f.Delivery)

		if f.Delivery == reviews.DeliverySuppressedRepeat {
			assert.Zero(t, f.ContextBytes, "a suppressed match carries no bytes")
		}
	}

	assert.Equal(t, []reviews.DeliveryStatus{
		reviews.DeliveryInjected,         // first
		reviews.DeliverySuppressedRepeat, // second
		reviews.DeliveryInjected,         // partial: the new pin
		reviews.DeliverySuppressedRepeat, // partial: the known lessons
		reviews.DeliveryInjected,         // other context
		reviews.DeliveryInjected,         // after the reset
	}, statuses)
}

func TestDeliverSeparatesReceivingContextsAndClients(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryOncePerContext)

	// deliver runs one event and returns its status.
	deliver := func(clientID string, event integration.EditEvent) Outcome {
		t.Helper()

		out, err := Deliver(context.Background(), st,
			Request{Root: root, ClientID: clientID, Mechanism: "fake-stdout", Event: event, Config: cfg},
			func(string) error { return nil })
		require.NoError(t, err)

		return out
	}

	parent := editEvent(root, "session-1", true, "api/handler.go")

	assert.Equal(t, StatusEmitted, deliver("claude", parent).Status)
	assert.Equal(t, StatusSuppressed, deliver("claude", parent).Status)

	// A subagent with its own identity shares the parent session id. The
	// parent's emission must not hide the subagent's first reminder.
	child := editEvent(root, "session-1", true, "api/handler.go")
	child.Context = &integration.ReceivingContext{ID: "session-1/agent-7", Resettable: true}

	assert.Equal(t, StatusEmitted, deliver("claude", child).Status, "the child is another receiver")
	assert.Equal(t, StatusSuppressed, deliver("claude", child).Status)

	// A subagent without an identity reports the parent session only.
	// The adapter then gives no context, and delivery repeats.
	anonymous := editEvent(root, "session-1", true, "api/handler.go")
	anonymous.Context = nil

	for range 2 {
		out := deliver("claude", anonymous)
		assert.Equal(t, StatusEmitted, out.Status)
		assert.Equal(t, SuppressionNoContext, out.Suppression)
	}

	// Another client reports the identical session string.
	assert.Equal(t, StatusEmitted, deliver("fake", parent).Status, "one session string, two clients, two receivers")
	assert.Equal(t, StatusSuppressed, deliver("fake", parent).Status)

	// A reset reaches one receiver of one client.
	require.NoError(t, Reset(ResetRequest{Root: root, ClientID: "claude",
		Event: integration.ResetEvent{Context: *parent.Context}}))

	again := deliver("claude", parent)
	assert.Equal(t, StatusEmitted, again.Status)
	assert.Equal(t, uint64(2), again.Generation)
	assert.Equal(t, StatusSuppressed, deliver("claude", child).Status, "the child keeps its generation")
	assert.Equal(t, StatusSuppressed, deliver("fake", parent).Status, "the other client keeps its generation")

	// The log tells the receivers apart, and the session digest keeps
	// its old meaning: equal for every record of the session string.
	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	receivers := map[string]bool{}
	sessions := map[string]bool{}

	for _, f := range firings {
		sessions[f.SessionSHA] = true

		if f.ContextSHA != "" {
			receivers[f.Client+" "+f.ContextSHA] = true
		}

		assert.Equal(t, "fake-stdout", f.Mechanism)
		assert.Contains(t, []string{"claude", "fake"}, f.Client)
	}

	assert.Len(t, sessions, 1)
	assert.Len(t, receivers, 3, "parent, child, and the other client's parent")
}

func TestDeliverEmitsAgainWhenTheLessonTextChanges(t *testing.T) {
	// The delivered set keys a pin by its version. A reworded note is
	// another version, so the same context gets it once more.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryOncePerContext)
	req := Request{Root: root, ClientID: integration.ClaudeID, Config: cfg,
		Event: editEvent(root, "session-1", true, "api/handler.go")}

	var got capture

	_, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)

	cfg.Pin[0].Note = "Validate request payloads at the edge, and reject unknown fields."

	revised, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)

	assert.Equal(t, 1, revised.Emitted, "only the revised pin is new to the context")
	assert.Equal(t, 1, revised.Suppressed)
	assert.Contains(t, got.advice[1], "reject unknown fields")
	assert.NotContains(t, got.advice[1], "RUF001")
}

func TestLegacyClientIsTheClaudeRegistryID(t *testing.T) {
	// The session-keyed entry points of the reviews package keep the
	// name of the client they always served.
	assert.Equal(t, integration.ClaudeID, reviews.LegacyClientID)
}

func TestDeliverRepeatsInsideAClaudeSubagentAndNeverResetsTheParent(t *testing.T) {
	// End to end with the native fixtures: the parent session and a
	// subagent that reports the same session id.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryOncePerContext)

	claude, ok := integration.Builtin().Lookup(integration.ClaudeID)
	require.True(t, ok)

	fixture := func(name string) []byte {
		t.Helper()

		raw, err := os.ReadFile(filepath.Join("..", "integration", "testdata", "claude", name))
		require.NoError(t, err)

		payload := strings.ReplaceAll(string(raw), "/workspace/repo", root)

		return []byte(strings.ReplaceAll(payload, "internal/api/", "api/"))
	}

	deliver := func(name string) Outcome {
		t.Helper()

		event, err := claude.Edits.DecodeEdit(fixture(name))
		require.NoError(t, err)

		out, err := Deliver(context.Background(), st, Request{Root: root, ClientID: claude.ID,
			Mechanism: claude.Edits.AdviceMechanism(), Event: event, Config: cfg}, func(string) error { return nil })
		require.NoError(t, err)

		return out
	}

	reset := func(name string) {
		t.Helper()

		event, err := claude.Resets.DecodeReset(fixture(name))
		require.NoError(t, err)
		require.NoError(t, Reset(ResetRequest{Root: root, ClientID: claude.ID, Event: event}))
	}

	assert.Equal(t, StatusEmitted, deliver("pre_tool_use_edit.json").Status)
	assert.Equal(t, StatusSuppressed, deliver("pre_tool_use_edit.json").Status)

	// The parent already has the lesson. The subagent does not, and it
	// has no reset event, so it gets the advice on every edit.
	for range 2 {
		out := deliver("pre_tool_use_edit_subagent.json")
		assert.Equal(t, StatusEmitted, out.Status)
		assert.Equal(t, SuppressionNotResettable, out.Suppression)
	}

	// A reset that names the subagent must not reach the parent.
	reset("post_compact_subagent.json")
	assert.Equal(t, StatusSuppressed, deliver("pre_tool_use_edit.json").Status)

	reset("post_compact.json")
	assert.Equal(t, StatusEmitted, deliver("pre_tool_use_edit.json").Status)

	// The log holds one session digest and two context digests.
	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	sessions, contexts := map[string]bool{}, map[string]bool{}
	for _, f := range firings {
		sessions[f.SessionSHA] = true
		contexts[f.ContextSHA] = true
	}

	assert.Len(t, sessions, 1)
	assert.Len(t, contexts, 2)
	assert.NotContains(t, contexts, "")
}

func TestDeliverGivesEquivalentEventsOfTwoClientsTheSameAdvice(t *testing.T) {
	// One file, edited through Claude Code and through a Codex patch. The
	// clients differ in the payload only, so the advice must be equal.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryAlways)
	reg := integration.Builtin()

	advise := func(clientID string, payload []byte) (string, Outcome) {
		t.Helper()

		client, ok := reg.Lookup(clientID)
		require.True(t, ok)

		event, err := client.Edits.DecodeEdit(payload)
		require.NoError(t, err)

		var got capture

		out, err := Deliver(context.Background(), st, Request{Root: root, ClientID: client.ID,
			Mechanism: client.Edits.AdviceMechanism(), Event: event, Config: cfg}, got.emit)
		require.NoError(t, err)
		require.Len(t, got.advice, 1)

		return got.advice[0], out
	}

	fixture := func(dir, name string) []byte {
		t.Helper()

		raw, err := os.ReadFile(filepath.Join("..", "integration", "testdata", dir, name))
		require.NoError(t, err)

		// The fixture workspace is a placeholder. Point it at the test
		// root and at the region the lessons cover, also in a nested cwd.
		payload := strings.ReplaceAll(string(raw), "/workspace/repo", root)

		return []byte(strings.ReplaceAll(payload, "internal/api", "api"))
	}

	claudeAdvice, _ := advise(integration.ClaudeID, fixture("claude", "pre_tool_use_edit.json"))
	codexAdvice, codexOut := advise(integration.CodexID, fixture("codex", "pre_tool_use_apply_patch_update.json"))

	assert.Equal(t, claudeAdvice, codexAdvice)
	assert.Equal(t, SuppressionOff, codexOut.Suppression)

	// A patch that names the file twice is the same single-file event.
	repeated, out := advise(integration.CodexID, fixture("codex", "pre_tool_use_apply_patch_repeated.json"))
	assert.Equal(t, claudeAdvice, repeated)
	assert.Equal(t, []string{"api/handler.go"}, out.Files)

	// A nested event directory, end to end. This fixture keeps its own
	// depth: "../../docs" leaves the workspace from a shallower directory.
	raw, err := os.ReadFile(filepath.Join("..", "integration", "testdata", "codex", "pre_tool_use_apply_patch_nested_cwd.json"))
	require.NoError(t, err)

	cfg.Pin = append(cfg.Pin, reviews.PinRule{Rule: "docs-voice", Region: "docs", Note: "Write in the present tense."})

	_, nested := advise(integration.CodexID, []byte(strings.ReplaceAll(string(raw), "/workspace/repo", root)))
	assert.Equal(t, []string{"internal/api/handler.go", "docs/from-nested.md"}, nested.Files)

	// The Codex adapter names the receiver, but no reset of a Codex
	// context is verified. Under once-per-context the advice repeats, the
	// outcome says why, and no state appears.
	cfg.Delivery = reviews.HookDeliveryOncePerContext

	for range 2 {
		_, out := advise(integration.CodexID, fixture("codex", "pre_tool_use_apply_patch_update.json"))
		assert.Equal(t, StatusEmitted, out.Status)
		assert.Equal(t, SuppressionNotResettable, out.Suppression)
	}

	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.json"))

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	last := firings[len(firings)-1]
	assert.Equal(t, integration.CodexID, last.Client)
	assert.Equal(t, "apply_patch", last.Tool)
	assert.Len(t, last.SessionSHA, 64)
	assert.Len(t, last.ContextSHA, 64, "the log names the lane that the client reported")
}

func TestDeliverKeepsAdviceEligibleWhenTheReplyFails(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	req := Request{Root: root, ClientID: integration.ClaudeID,
		Config: pinnedConfig(reviews.HookDeliveryOncePerContext),
		Event:  editEvent(root, "session-1", true, "api/handler.go")}

	broken := errors.New("stdout closed")

	out, err := Deliver(context.Background(), st, req, func(string) error { return broken })

	var emitErr *EmitError
	require.ErrorAs(t, err, &emitErr, "the caller can tell a failed reply from a failed lookup")
	require.ErrorIs(t, err, broken)
	assert.Equal(t, broken.Error(), err.Error(), "the hook reports the write error unchanged")
	assert.NotEqual(t, StatusEmitted, out.Status)

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	assert.Empty(t, firings, "a reply that never arrived is not recorded as injected")

	var got capture

	retry, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, retry.Status, "the failed emission marked nothing delivered")
	assert.Equal(t, 2, retry.Emitted)
}

func TestDeliverRepeatsWithoutAReliableReceivingContext(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := pinnedConfig(reviews.HookDeliveryOncePerContext)

	cases := []struct {
		name  string
		event integration.EditEvent
		want  Suppression
	}{
		{"no context", editEvent(root, "", false, "api/handler.go"), SuppressionNoContext},
		{"context without a reset", editEvent(root, "session-1", false, "api/handler.go"), SuppressionNotResettable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got capture

			for range 2 {
				out, err := Deliver(context.Background(), st,
					Request{Root: root, ClientID: "fake", Event: tc.event, Config: cfg}, got.emit)
				require.NoError(t, err)
				assert.Equal(t, StatusEmitted, out.Status)
				assert.Equal(t, tc.want, out.Suppression, "the outcome names the fallback")
				assert.Zero(t, out.Generation)
			}

			assert.Len(t, got.advice, 2, "uncertain identity means repeated delivery")
			assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.json"),
				"no suppression means no state")
		})
	}

	// A session id without a context still joins the audit records.
	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.NotEmpty(t, firings)
	assert.Len(t, firings[len(firings)-1].SessionSHA, 64)
}

func TestDeliverFailsOpenWhenTheStateIsLocked(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	req := Request{Root: root, ClientID: integration.ClaudeID,
		Config: pinnedConfig(reviews.HookDeliveryOncePerContext),
		Event:  editEvent(root, "session-1", true, "api/handler.go")}

	// Another hook process holds the lease.
	held, err := reviews.BeginHookDelivery(root, "another-session", []model.Lesson{{Region: "x", Symptom: "y"}})
	require.NoError(t, err)

	var got capture

	out, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, held.Close())
	require.NoError(t, err)

	assert.Equal(t, StatusEmitted, out.Status, "a busy lock never hides advice")
	assert.Equal(t, SuppressionStateUnavailable, out.Suppression)
	assert.Len(t, got.advice, 1)
}

func TestDeliverReportsAFailedCommitAndStillSucceeds(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	root := workspace(t)
	st := lessonStore(t, root)
	req := Request{Root: root, ClientID: integration.ClaudeID,
		Config: pinnedConfig(reviews.HookDeliveryOncePerContext),
		Event:  editEvent(root, "session-1", true, "api/handler.go")}

	dir := filepath.Join(root, ".seamark")
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// The reply succeeds, and then the state directory refuses writes.
	out, err := Deliver(context.Background(), st, req, func(string) error {
		return os.Chmod(dir, 0o500)
	})
	require.NoError(t, err, "the advice already reached the client")
	assert.Equal(t, StatusEmitted, out.Status)
	assert.Equal(t, SuppressionCommitFailed, out.Suppression)
	assert.True(t, out.AuditFailed, "the log append failed too, and the result does not change")

	require.NoError(t, os.Chmod(dir, 0o755))

	var got capture

	retry, err := Deliver(context.Background(), st, req, got.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, retry.Status, "a failed state write allows a later delivery")
}

func TestDeliverStaysSilentWhenNothingApplies(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)

	muted := reviews.DefaultConfig()
	muted.Mute = []reviews.MuteRule{{Rule: "RUF001"}}

	cases := []struct {
		name  string
		cfg   *reviews.Config
		paths []string
	}{
		{"no lesson for the file", nil, []string{"db/query.go"}},
		{"muted lesson", muted, []string{"api/handler.go"}},
		{"no paths", nil, nil},
		{"only outside paths", pinnedConfig(reviews.HookDeliveryAlways), []string{"/etc/hosts", "../x.go"}},
		{"malformed path", pinnedConfig(reviews.HookDeliveryAlways), []string{"bad\x00path"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Deliver(context.Background(), st,
				Request{Root: root, ClientID: "fake", Event: editEvent(root, "s", true, tc.paths...), Config: tc.cfg},
				func(string) error {
					require.Fail(t, "nothing to say, so the reply must not be written")

					return nil
				})
			require.NoError(t, err)
			assert.Equal(t, StatusSilent, out.Status)
			assert.Zero(t, out.ContextBytes)
		})
	}

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	assert.Empty(t, firings, "silence is not a firing")
}

// overflowPins lists eight pins on api that do not restate each other,
// in fold order. They share a rank, so only the file order ranks them.
var overflowPins = []string{
	"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n",
	"  - {rule: idempotent-retries, region: api, note: Make retried requests idempotent.}\n",
	"  - {rule: transaction-atomicity, region: api, note: Wrap dependent writes in one transaction.}\n",
	"  - {rule: structured-logging, region: api, note: Log fields and never format strings.}\n",
	"  - {rule: cache-invalidation, region: api, note: Purge entries after every write.}\n",
	"  - {rule: lock-ordering, region: api, note: Take mutexes by ascending address.}\n",
	"  - {rule: metric-naming, region: api, note: Prefix counters with their subsystem.}\n",
	"  - {rule: error-wrapping, region: api, note: Keep the cause inside returned failures.}\n",
}

// overflowConfig writes the first count pins of overflowPins with
// pin_budget 3 under once-per-context delivery.
func overflowConfig(t *testing.T, root string, count int) *reviews.Config {
	t.Helper()

	return writeConfig(t, root, "hook_delivery: once-per-context\npin_budget: 3\npin:\n"+
		strings.Join(overflowPins[:count], ""))
}

// emptyStore opens an index with no mined lesson, so only the pins of the
// configuration can fill the advice.
func emptyStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	return st
}

// overflowRun holds the fixed values of one receiver's matching edits.
type overflowRun struct {
	st   *store.Store
	root string
	cfg  *reviews.Config
}

// edit delivers one matching edit of the Claude root context of session-1
// and returns the outcome and the advice. The advice is empty when the
// event emits nothing.
func (r overflowRun) edit(t *testing.T) (out Outcome, advice string) {
	t.Helper()

	var got capture

	out, err := Deliver(context.Background(), r.st,
		claudeRequest(r.root, r.cfg, claudeEvent(r.root, "session-1", "", "match", "prompt", "api/handler.go")), got.emit)
	require.NoError(t, err)

	return out, strings.Join(got.advice, "")
}

// reset starts a new generation of the Claude root context of session-1.
func (r overflowRun) reset(t *testing.T) {
	t.Helper()

	require.NoError(t, Reset(ResetRequest{Root: r.root, ClientID: integration.ClaudeID, Event: integration.ResetEvent{
		Context: *integration.ReceivingContextOf(integration.ClaudeID, "session-1", ""),
	}}))
}

// pinsOfLastEvent returns the pins of the last event in the firing log.
// Only the first record of an event carries them. The log orders them by
// delivery, so a test compares them as a set.
func pinsOfLastEvent(t *testing.T, root string) []reviews.FiringPin {
	t.Helper()

	records := firings(t, root)
	require.NotEmpty(t, records)

	for i := len(records) - 1; i >= 0; i-- {
		if len(records[i].Pins) > 0 {
			return records[i].Pins
		}
	}

	require.Fail(t, "no record carries pins")

	return nil
}

// assertSends proves that advice names the rules of sent in that order
// and names no other rule of the pins.
func assertSends(t *testing.T, advice string, pins []reviews.PinRule, sent ...int) {
	t.Helper()

	position := -1

	for _, i := range sent {
		at := strings.Index(advice, pins[i].Rule)
		require.Greater(t, at, position, "the eligible pins keep the fold order: %s", pins[i].Rule)
		position = at
	}

	for i, pin := range pins {
		if !slices.Contains(sent, i) {
			assert.NotContains(t, advice, pin.Rule)
		}
	}
}

func TestDeliverMovesToTheNextPinAfterTheBudget(t *testing.T) {
	// Four pins of equal rank, budget three, and a resettable receiver.
	root := workspace(t)
	cfg := overflowConfig(t, root, 4)
	run := overflowRun{st: emptyStore(t), root: root, cfg: cfg}
	a, b, c, d := cfg.Pin[0], cfg.Pin[1], cfg.Pin[2], cfg.Pin[3]

	// The first three pins fill the budget, and the pointer names the rest.
	first, advice := run.edit(t)
	assert.Equal(t, StatusEmitted, first.Status)
	assert.Equal(t, SuppressionActive, first.Suppression)
	assert.Equal(t, 3, first.Emitted)
	assert.Zero(t, first.Suppressed)
	assert.Equal(t, 1, first.HeldBackPins)
	assertSends(t, advice, cfg.Pin, 0, 1, 2)
	assert.Contains(t, advice, "+1 more pins")
	assert.ElementsMatch(t, []reviews.FiringPin{
		pinOf(cfg, a, reviews.PinInjected, ""),
		pinOf(cfg, b, reviews.PinInjected, ""),
		pinOf(cfg, c, reviews.PinInjected, ""),
		pinOf(cfg, d, reviews.PinHeldBack, reviews.CauseOverPinCap),
	}, pinsOfLastEvent(t, root))

	// The delivered pins leave before the budget, so the last pin goes out.
	second, advice := run.edit(t)
	assert.Equal(t, StatusEmitted, second.Status)
	assert.Equal(t, 1, second.Emitted)
	assert.Equal(t, 3, second.Suppressed)
	assert.Zero(t, second.HeldBackPins, "a delivered pin is not a held-back pin")
	assertSends(t, advice, cfg.Pin, 3)
	assert.NotContains(t, advice, "more pins")
	assert.ElementsMatch(t, []reviews.FiringPin{
		pinOf(cfg, a, reviews.PinSuppressedRepeat, ""),
		pinOf(cfg, b, reviews.PinSuppressedRepeat, ""),
		pinOf(cfg, c, reviews.PinSuppressedRepeat, ""),
		pinOf(cfg, d, reviews.PinInjected, ""),
	}, pinsOfLastEvent(t, root))

	records := firings(t, root)
	require.Len(t, records, 3)
	assert.Equal(t, []string{"structured-logging — Log fields and never format strings."}, symptoms(records[1].Fired))
	assert.Equal(t, reviews.DeliverySuppressedRepeat, records[2].Delivery)
	assert.Len(t, records[2].Fired, 3, "the second record names the delivered pins")

	// The receiver now has every pin.
	third, advice := run.edit(t)
	assert.Equal(t, StatusSuppressed, third.Status)
	assert.Equal(t, 4, third.Suppressed)
	assert.Empty(t, advice)

	// A reset restores the order from the top of the pins.
	run.reset(t)

	fourth, advice := run.edit(t)
	assert.Equal(t, uint64(2), fourth.Generation)
	assertSends(t, advice, cfg.Pin, 0, 1, 2)
	assert.Contains(t, advice, "+1 more pins")

	_, advice = run.edit(t)
	assertSends(t, advice, cfg.Pin, 3)
}

func TestDeliverKeepsARestatementCollapsedAfterDelivery(t *testing.T) {
	root := workspace(t)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin_budget: 1\npin:\n"+
		"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n"+
		"  - {rule: boundary-validation, region: '*', note: Validate request payloads at the edge.}\n"+
		"  - {rule: idempotent-retries, region: api, note: Make retried requests idempotent.}\n"+
		"  - {rule: transaction-atomicity, region: api, note: Wrap dependent writes in one transaction.}\n")
	run := overflowRun{st: emptyStore(t), root: root, cfg: cfg}
	kept, restated, next, last := cfg.Pin[0], cfg.Pin[1], cfg.Pin[2], cfg.Pin[3]
	collapsed := pinOf(cfg, restated, reviews.PinHeldBack, reviews.CauseRestated)

	_, advice := run.edit(t)
	assert.Contains(t, advice, "+3 more pins")

	// The restatement never takes the room of the pin that it restates.
	out, advice := run.edit(t)
	assert.Equal(t, 2, out.HeldBackPins, "the restatement and the last pin")
	assert.Contains(t, advice, next.Rule)
	assert.Contains(t, advice, "+2 more pins")
	assert.ElementsMatch(t, []reviews.FiringPin{
		pinOf(cfg, kept, reviews.PinSuppressedRepeat, ""),
		collapsed,
		pinOf(cfg, next, reviews.PinInjected, ""),
		pinOf(cfg, last, reviews.PinHeldBack, reviews.CauseOverPinCap),
	}, pinsOfLastEvent(t, root))

	out, advice = run.edit(t)
	assert.Equal(t, 1, out.HeldBackPins, "the restatement stays held back")
	assert.Contains(t, advice, last.Rule)
	assert.Contains(t, advice, "+1 more pins")

	// Nothing is new, so the restatement stays out and no text goes out.
	out, advice = run.edit(t)
	assert.Equal(t, StatusSuppressed, out.Status)
	assert.Empty(t, advice)
	assert.ElementsMatch(t, []reviews.FiringPin{
		pinOf(cfg, kept, reviews.PinSuppressedRepeat, ""),
		collapsed,
		pinOf(cfg, next, reviews.PinSuppressedRepeat, ""),
		pinOf(cfg, last, reviews.PinSuppressedRepeat, ""),
	}, pinsOfLastEvent(t, root))
}

func TestDeliverRestoresOverflowEligibility(t *testing.T) {
	t.Run("a failed reply keeps the tried pins eligible", func(t *testing.T) {
		root := workspace(t)
		cfg := overflowConfig(t, root, 4)
		run := overflowRun{st: emptyStore(t), root: root, cfg: cfg}

		// Run the first edit, so the failed event is the one of the last pin.
		_, advice := run.edit(t)
		assertSends(t, advice, cfg.Pin, 0, 1, 2)

		broken := errors.New("stdout closed")

		// A failed reply is an evaluation record, so it needs the recording
		// switch.
		req := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "m", "p", "api/handler.go"))
		req.RecordEvaluations = true

		_, err := Deliver(context.Background(), run.st, req, func(string) error { return broken })
		require.ErrorIs(t, err, broken)
		assert.ElementsMatch(t, []reviews.FiringPin{
			pinOf(cfg, cfg.Pin[0], reviews.PinSuppressedRepeat, ""),
			pinOf(cfg, cfg.Pin[1], reviews.PinSuppressedRepeat, ""),
			pinOf(cfg, cfg.Pin[2], reviews.PinSuppressedRepeat, ""),
			pinOf(cfg, cfg.Pin[3], reviews.PinEmitFailed, ""),
		}, pinsOfLastEvent(t, root))

		_, advice = run.edit(t)
		assertSends(t, advice, cfg.Pin, 3)
	})

	t.Run("a reset empties the delivered set", func(t *testing.T) {
		root := workspace(t)
		cfg := overflowConfig(t, root, 4)
		run := overflowRun{st: emptyStore(t), root: root, cfg: cfg}

		_, _ = run.edit(t)
		run.reset(t)

		_, advice := run.edit(t)
		assertSends(t, advice, cfg.Pin, 0, 1, 2)
		assert.Contains(t, advice, "+1 more pins")
	})

	t.Run("a new version is eligible at its rank", func(t *testing.T) {
		root := workspace(t)
		cfg := overflowConfig(t, root, 4)
		st := emptyStore(t)
		run := overflowRun{st: st, root: root, cfg: cfg}

		_, _ = run.edit(t)
		_, _ = run.edit(t)

		// A new note gives the second pin a new version. The rest stay delivered.
		changed := writeConfig(t, root, "hook_delivery: once-per-context\npin_budget: 3\npin:\n"+
			overflowPins[0]+
			"  - {rule: idempotent-retries, region: api, note: Retry only with an idempotency key.}\n"+
			strings.Join(overflowPins[2:4], ""))

		out, advice := overflowRun{st: st, root: root, cfg: changed}.edit(t)
		assert.Equal(t, 1, out.Emitted)
		assert.Equal(t, 3, out.Suppressed)
		assert.Contains(t, advice, "Retry only with an idempotency key.")
		assert.NotContains(t, advice, "more pins")
		assert.NotContains(t, advice, "boundary-validation")
	})
}

func TestDeliverKeepsThePlainSelectionForEveryOtherReceiver(t *testing.T) {
	statePath := func(root string) string { return filepath.Join(root, ".seamark", "lessons-hook-state.json") }

	cases := []struct {
		name  string
		first string
		event func(root string) integration.EditEvent
		// prepare fills the state and returns a function that ends the case.
		prepare func(t *testing.T, run overflowRun) func()
		want    Suppression
		// hasState says that the case keeps a state file, which must not change.
		hasState bool
	}{
		{
			name:  "the configuration asks for repeats",
			first: "hook_delivery: always\n",
			event: func(root string) integration.EditEvent {
				return claudeEvent(root, "session-1", "", "m", "p", "api/handler.go")
			},
			want: SuppressionOff,
		},
		{
			name:  "no receiving context",
			first: "hook_delivery: once-per-context\n",
			event: func(root string) integration.EditEvent { return editEvent(root, "", false, "api/handler.go") },
			want:  SuppressionNoContext,
		},
		{
			name:  "a child lane has no reset",
			first: "hook_delivery: once-per-context\n",
			event: func(root string) integration.EditEvent {
				return claudeEvent(root, "session-1", "agent-7", "m", "p", "api/handler.go")
			},
			want: SuppressionNotResettable,
		},
		{
			name:  "a busy lock",
			first: "hook_delivery: once-per-context\n",
			event: func(root string) integration.EditEvent {
				return claudeEvent(root, "session-1", "", "m", "p", "api/handler.go")
			},
			prepare: func(t *testing.T, run overflowRun) func() {
				t.Helper()

				// The state names the first pins, and the lease of another
				// session hides that set from this event.
				_, _ = run.edit(t)

				held, err := reviews.BeginHookDelivery(run.root, "another-session", nil)
				require.NoError(t, err)

				return func() { require.NoError(t, held.Close()) }
			},
			want: SuppressionStateUnavailable, hasState: true,
		},
		{
			name:  "a damaged state file",
			first: "hook_delivery: once-per-context\n",
			event: func(root string) integration.EditEvent {
				return claudeEvent(root, "session-1", "", "m", "p", "api/handler.go")
			},
			prepare: func(t *testing.T, run overflowRun) func() {
				t.Helper()

				require.NoError(t, os.WriteFile(statePath(run.root), []byte("{not json"), 0o600))

				return func() {}
			},
			want: SuppressionStateUnavailable, hasState: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := workspace(t)
			cfg := writeConfig(t, root, tc.first+"pin_budget: 3\npin:\n"+strings.Join(overflowPins[:4], ""))
			st := lessonStore(t, root)
			run := overflowRun{st: st, root: root, cfg: cfg}

			finish := func() {}
			if tc.prepare != nil {
				finish = tc.prepare(t, run)
			}

			defer finish()

			// The expected bytes come from the fold, which no state reaches.
			plain, err := hookSelection(st, cfg, []string{"api/handler.go"})
			require.NoError(t, err)

			var want strings.Builder
			require.NoError(t, report.PrintEditReminder(&want, []string{"api/handler.go"}, plain.Lessons, plain.HeldBack))

			var before []byte
			if tc.hasState {
				before, err = os.ReadFile(statePath(root))
				require.NoError(t, err)
			}

			request := claudeRequest(root, cfg, tc.event(root))

			for range 2 {
				var got capture

				out, err := Deliver(context.Background(), st, request, got.emit)
				require.NoError(t, err)

				assert.Equal(t, StatusEmitted, out.Status)
				assert.Equal(t, tc.want, out.Suppression)
				assert.Equal(t, 1, out.HeldBackPins)
				assert.Zero(t, out.Suppressed)
				assert.Zero(t, out.Generation)
				require.Len(t, got.advice, 1)
				assert.Equal(t, want.String(), got.advice[0], "the reminder keeps its bytes on every edit")
				assert.ElementsMatch(t, []reviews.FiringPin{
					pinOf(cfg, cfg.Pin[0], reviews.PinInjected, ""),
					pinOf(cfg, cfg.Pin[1], reviews.PinInjected, ""),
					pinOf(cfg, cfg.Pin[2], reviews.PinInjected, ""),
					pinOf(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverPinCap),
				}, pinsOfLastEvent(t, root))
			}

			if !tc.hasState {
				assert.NoFileExists(t, statePath(root), "no lease means no state")

				return
			}

			after, err := os.ReadFile(statePath(root))
			require.NoError(t, err)
			assert.Equal(t, before, after, "the event changes no state")
		})
	}
}

func TestDeliverSendsANewMinedLessonWhenEveryPinIsDelivered(t *testing.T) {
	// Eight pins fill the total cap, so the first cut leaves the mined
	// lesson out. A second cut that reads only Mined never finds it.
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin_budget: 8\npin:\n"+strings.Join(overflowPins, ""))
	run := overflowRun{st: st, root: root, cfg: cfg}

	first, advice := run.edit(t)
	assert.Equal(t, 8, first.Emitted)
	assert.NotContains(t, advice, "RUF001")

	second, advice := run.edit(t)
	assert.Equal(t, StatusEmitted, second.Status)
	assert.Equal(t, SuppressionActive, second.Suppression)
	assert.Equal(t, 1, second.Emitted)
	assert.Equal(t, 8, second.Suppressed)
	assert.Contains(t, advice, "RUF001")
	assert.NotContains(t, advice, "more pins")

	third, advice := run.edit(t)
	assert.Equal(t, StatusSuppressed, third.Status)
	assert.Equal(t, 9, third.Suppressed)
	assert.Empty(t, advice)
}

func TestDeliverOpensNoLeaseWhenNothingApplies(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\n")

	out, err := Deliver(context.Background(), st,
		claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "m", "p", "db/query.go")), mustNotEmit)
	require.NoError(t, err)

	assert.Equal(t, StatusSilent, out.Status)
	assert.Equal(t, SuppressionOff, out.Suppression)
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.json"))
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.lock"))
}

func TestDeliverAdvisesOnANewFileByItsPath(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)

	var got capture

	out, err := Deliver(context.Background(), st, Request{Root: root, ClientID: "fake",
		Event: editEvent(root, "", false, "api/not_written_yet.go")}, got.emit)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/not_written_yet.go"}, out.Files)
	require.Len(t, got.advice, 1)
	assert.Contains(t, got.advice[0], "RUF001", "a new file gets the lessons of its region")
}

func TestDeliverReadsPinsThroughAnEmptyIndex(t *testing.T) {
	// A clone that was never indexed has lessons.yaml and no database.
	root := workspace(t)

	st, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	var got capture

	out, err := Deliver(context.Background(), st, Request{Root: root, ClientID: "fake",
		Event: editEvent(root, "", false, "api/handler.go"), Config: pinnedConfig(reviews.HookDeliveryAlways)}, got.emit)
	require.NoError(t, err)

	assert.Equal(t, 1, out.Emitted)
	assert.Contains(t, got.advice[0], "boundary-validation")
	assert.NoFileExists(t, store.DefaultPath(root), "delivery never creates an index")
}

func TestDeliverStopsOnACancelledContext(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Deliver(ctx, st, Request{Root: root, ClientID: "fake",
		Event: editEvent(root, "", false, "api/handler.go")},
		func(string) error {
			require.Fail(t, "a cancelled delivery writes no reply")

			return nil
		})
	require.ErrorIs(t, err, context.Canceled)
}

func TestDeliverReportsALookupFailureAsAPlainError(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	require.NoError(t, st.Close())

	_, err := Deliver(context.Background(), st, Request{Root: root, ClientID: "fake",
		Event: editEvent(root, "", false, "api/handler.go")}, func(string) error { return nil })
	require.Error(t, err)

	var emitErr *EmitError
	assert.NotErrorAs(t, err, &emitErr, "the hook keeps a lookup failure quiet")
}

func TestResetIgnoresAnEventWithoutAResettableContext(t *testing.T) {
	root := workspace(t)

	for _, receiving := range []integration.ReceivingContext{
		{},
		{ID: "session-1"},
		{ID: "session-1", Resettable: true},
	} {
		require.NoError(t, Reset(ResetRequest{Root: root, ClientID: "fake",
			Event: integration.ResetEvent{Context: receiving}}))
	}

	assert.NoDirExists(t, filepath.Join(root, ".seamark"),
		"a reset never creates state that delivery has not used")

	// With state on disk, an event that names no resettable context, or
	// a request without a client, must leave every entry as it is.
	st := lessonStore(t, root)
	req := Request{Root: root, ClientID: "fake", Config: pinnedConfig(reviews.HookDeliveryOncePerContext),
		Event: editEvent(root, "session-1", true, "api/handler.go")}

	_, err := Deliver(context.Background(), st, req, func(string) error { return nil })
	require.NoError(t, err)

	path := filepath.Join(root, ".seamark", "lessons-hook-state.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, reset := range []ResetRequest{
		{Root: root, ClientID: "fake", Event: integration.ResetEvent{Context: integration.ReceivingContext{Resettable: true}}},
		{Root: root, ClientID: "fake", Event: integration.ResetEvent{Context: integration.ReceivingContext{ID: "session-1"}}},
		{Root: root, Event: integration.ResetEvent{Context: integration.ReceivingContext{ID: "session-1", Resettable: true}}},
		{Root: root},
	} {
		require.NoError(t, Reset(reset))
	}

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a missing identity resets no unrelated context")
}

func TestPackageStartsNoInference(t *testing.T) {
	// Lesson lookup and emission make no model call. The package must
	// not even import the packages that can start one.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, imp := range file.Imports {
			for _, banned := range []string{"/internal/agent", "/internal/distill", "os/exec", "net/http"} {
				assert.NotContains(t, imp.Path.Value, banned, "%s imports %s", name, imp.Path.Value)
			}
		}
	}
}
