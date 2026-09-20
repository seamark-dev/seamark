package delivery

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
	assert.NotContains(t, got.advice[0], "solitary finding", "the threshold still applies")

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "one operation is one record")
	assert.Equal(t, []string{"api/handler.go", "db/query.go"}, firings[0].Files)
	assert.Equal(t, "apply_patch", firings[0].Tool)
	assert.Equal(t, out.ContextBytes, firings[0].ContextBytes, "one context-byte total per emission")
	assert.Len(t, firings[0].MatchSHA, 64)
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

	for _, receiver := range []integration.ReceivingContext{
		{},
		{ID: "session-1"},
		{ID: "session-1", Resettable: true},
	} {
		require.NoError(t, Reset(ResetRequest{Root: root, ClientID: "fake",
			Event: integration.ResetEvent{Context: receiver}}))
	}

	assert.NoDirExists(t, filepath.Join(root, ".seamark"),
		"a reset never creates state that delivery has not used")
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
