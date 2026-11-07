package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold the firing records of version 2 that the
// lessons hooks write through the CLI.

// hookRepo returns an indexed Git repository with one mined lesson and
// one pin on a.go. The hooks then have something to say about a.go.
func hookRepo(t *testing.T) string {
	t.Helper()

	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	seedLesson(t, root, "a.go", "RUF001", 4)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("pin:\n  - {rule: keep-main-small, region: a.go, note: Move the work into helpers.}\n"), 0o644))

	return root
}

// recordEvaluationsOn turns on the recording switch for the rest of the
// test, as the native recording script does for its paid runs. The hooks
// run in this process, so they read the switch from its environment.
func recordEvaluationsOn(t *testing.T) {
	t.Helper()

	t.Setenv(recordEvaluationsEnv, "1")
}

// editPayload is a native Claude PreToolUse payload for an Edit of file.
func editPayload(file, session, toolUse, prompt string) string {
	return `{"session_id":"` + session + `","tool_use_id":"` + toolUse + `","prompt_id":"` + prompt +
		`","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"` + file + `"}}`
}

// resetPayload is a native Claude lifecycle payload of event for the
// root context of session. The reset hook acts only on PostCompact.
func resetPayload(session, event string) string {
	return `{"session_id":"` + session + `","prompt_id":"prompt-2","hook_event_name":"` + event + `"}`
}

// recordLinkedRoot makes the index of root record its repository root
// through a symbolic link to the git top level. The identity root is
// then the link, and every digest over the git top level differs. The
// index command writes the meta, so callers run it first.
func recordLinkedRoot(t *testing.T, root string) (top, link string) {
	t.Helper()

	top = gitRoot(t, root)
	link = filepath.Join(t.TempDir(), "repo-link")
	require.NoError(t, os.Symlink(top, link))

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, link))
	require.NoError(t, st.Close())

	return top, link
}

// stateContexts counts the context entries of a delivery state file.
// The edit state and the exploration state share the "contexts" key.
func stateContexts(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var state struct {
		Contexts map[string]json.RawMessage `json:"contexts"`
	}

	require.NoError(t, json.Unmarshal(data, &state))

	return len(state.Contexts)
}

// claudeReceiver is the receiver ID that the Claude adapter derives for
// the root context of session.
func claudeReceiver(session string) string {
	return integration.ReceivingContextOf(integration.ClaudeID, session, "").ID
}

func TestLessonsHookWritesAuditV2(t *testing.T) {
	root := hookRepo(t)
	idRoot := gitRoot(t, root)

	hook := func(file, toolUse string) string {
		t.Helper()

		out, _, err := runIn(t, editPayload(filepath.Join(root, file), "session-1", toolUse, "prompt-1"),
			"-C", root, "lessons", "--hook")
		require.NoError(t, err)

		return out
	}

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	// Without the recording switch: a match writes the v2 injection
	// record, and an edit without a match writes nothing.
	assert.Contains(t, hook("a.go", "toolu-1"), "keep-main-small")
	assert.Empty(t, hook("nowhere.go", "toolu-2"))

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)

	rec := firings[0]
	assert.Equal(t, reviews.DeliveryInjected, rec.Delivery)
	assert.Equal(t, "a.go", rec.File)
	assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), rec.EventID)
	assert.Equal(t, "emitted", rec.Emission)
	assert.Equal(t, "complete", rec.Evaluation)
	assert.Equal(t, cfg.GuidanceDigest(), rec.GuidanceSHA)
	assert.Equal(t, "ok", rec.GuidanceStatus)
	assert.Equal(t, reviews.SessionDigest(idRoot, "session-1"), rec.SessionSHA)
	assert.Equal(t, reviews.MatchDigest(idRoot, "session-1", "toolu-1"), rec.MatchSHA)
	assert.Equal(t, reviews.ContextDigest(idRoot, integration.ClaudeID, claudeReceiver("session-1")), rec.ContextSHA)
	assert.Equal(t, reviews.TurnDigest(idRoot, integration.ClaudeID, "session-1", "prompt-1"), rec.TurnSHA)
	assert.Equal(t, integration.LaneRoot, rec.Lane)
	require.NotNil(t, rec.ContextResettable)
	assert.True(t, *rec.ContextResettable)
	assert.False(t, rec.RecordEvaluations)
	require.Len(t, rec.Pins, 1)
	assert.Equal(t, reviews.PinInjected, rec.Pins[0].Delivery)
	assert.Equal(t, cfg.PinVersion(cfg.Pin[0]), rec.Pins[0].Version)

	// With the recording switch: the edit without a match writes its
	// evaluation, and every record says that the switch was on.
	recordEvaluationsOn(t)

	assert.Empty(t, hook("nowhere.go", "toolu-3"))
	assert.Contains(t, hook("a.go", "toolu-4"), "keep-main-small")

	firings, err = reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 3)

	evaluation, injected := firings[1], firings[2]
	assert.Equal(t, reviews.DeliveryEvaluation, evaluation.Delivery)
	assert.Equal(t, "nowhere.go", evaluation.File)
	assert.Equal(t, "not-needed", evaluation.Emission)
	assert.True(t, evaluation.RecordEvaluations)
	assert.Equal(t, reviews.MatchDigest(idRoot, "session-1", "toolu-3"), evaluation.MatchSHA)
	assert.Equal(t, reviews.DeliveryInjected, injected.Delivery)
	assert.True(t, injected.RecordEvaluations)

	// An invalid lessons.yaml: the defaults still advise, and the record
	// names the guidance invalid without a digest.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte("pin: [\n"), 0o644))
	assert.Contains(t, hook("a.go", "toolu-5"), "RUF001")

	firings, err = reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 4)
	assert.Equal(t, "invalid", firings[3].GuidanceStatus)
	assert.Empty(t, firings[3].GuidanceSHA)
	assert.Empty(t, firings[3].Pins)
}

// TestLessonsHookRecordsHeldBackPins proves the pin list of an edit-hook
// record through the CLI. Only report and delivery tests checked it
// before, so a held-back pin recorded as injected passed every CLI
// test.
func TestLessonsHookRecordsHeldBackPins(t *testing.T) {
	root := hookRepo(t)

	// The repository-wide pin restates the first pin, and a pin budget
	// of 1 holds back the two other pins on a.go.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte("pin_budget: 1\npin:\n"+
		"  - {rule: boundary-validation, region: a.go, note: Validate request payloads at the edge.}\n"+
		"  - {rule: boundary-validation, region: '*', note: Validate request payloads at the edge.}\n"+
		"  - {rule: idempotent-retries, region: a.go, note: Make retried requests idempotent.}\n"+
		"  - {rule: transaction-atomicity, region: a.go, note: Wrap dependent writes in one transaction.}\n"), 0o644))

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 4)

	out, _, err := runIn(t, editPayload(filepath.Join(root, "a.go"), "session-1", "toolu-1", "prompt-1"),
		"-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "boundary-validation")
	assert.Contains(t, out, "+3 more pins", "the reminder points at the held-back pins")

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)
	assert.Equal(t, reviews.DeliveryInjected, firings[0].Delivery)

	pin := func(p reviews.PinRule, delivery reviews.PinDelivery, cause string) reviews.FiringPin {
		return reviews.FiringPin{
			Identity: reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions)),
			Version:  cfg.PinVersion(p), Delivery: delivery, Cause: cause,
		}
	}

	assert.Equal(t, []reviews.FiringPin{
		pin(cfg.Pin[0], reviews.PinInjected, ""),
		pin(cfg.Pin[1], reviews.PinHeldBack, reviews.CauseRestated),
		pin(cfg.Pin[2], reviews.PinHeldBack, reviews.CauseOverPinCap),
		pin(cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverPinCap),
	}, firings[0].Pins, "a held-back pin keeps its cause and is never recorded as injected")
}

// TestLessonsHookOverflowReachesTheNextEdit runs the overflow through the
// native Claude payloads. The root context moves to the next pin, and a
// child keeps the plain selection, because no reset reaches it.
func TestLessonsHookOverflowReachesTheNextEdit(t *testing.T) {
	root := hookRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"hook_delivery: once-per-context\npin_budget: 3\npin:\n"+
			"  - {rule: boundary-validation, region: a.go, note: Validate request payloads at the edge.}\n"+
			"  - {rule: idempotent-retries, region: a.go, note: Make retried requests idempotent.}\n"+
			"  - {rule: transaction-atomicity, region: a.go, note: Wrap dependent writes in one transaction.}\n"+
			"  - {rule: structured-logging, region: a.go, note: Log fields and never format strings.}\n"), 0o644))

	const last = "structured-logging"

	first := []string{"boundary-validation", "idempotent-retries", "transaction-atomicity"}

	edit := func(toolUse string) string {
		t.Helper()

		out, _, err := runIn(t, editPayload(filepath.Join(root, "a.go"), "session-1", toolUse, "prompt-1"),
			"-C", root, "lessons", "--hook")
		require.NoError(t, err)

		return out
	}

	assertFirstPins := func(out string) {
		t.Helper()

		for _, rule := range first {
			assert.Contains(t, out, rule)
		}

		assert.NotContains(t, out, last)
		assert.Contains(t, out, "+1 more pins")
	}

	assertFirstPins(edit("toolu-1"))

	out := edit("toolu-2")
	assert.Contains(t, out, last, "the next edit reaches the pin above the cap")
	assert.NotContains(t, out, "more pins")
	assert.NotContains(t, out, first[0])

	// The child has its own receiver and no reset, so it repeats the first pins.
	child := strings.Replace(editPayload(filepath.Join(root, "a.go"), "session-1", "toolu-3", "prompt-1"),
		`"prompt_id"`, `"agent_id":"agent-7","prompt_id"`, 1)

	for range 2 {
		out, _, err := runIn(t, child, "-C", root, "lessons", "--hook")
		require.NoError(t, err)
		assertFirstPins(out)
	}

	_, _, err := runIn(t, resetPayload("session-1", "PostCompact"), "-C", root, "lessons", "--hook-reset")
	require.NoError(t, err)

	assertFirstPins(edit("toolu-4"))
}

// TestLessonsHookNeverInvokes proves that the record paths of the edit
// hook and the reset hook call no model. An injected edit, a suppressed
// repeat, an edit without a match, and a compaction reset run with the
// recording switch on, without an agent, a model CLI, or the network.
// The record checks prove that each write path ran.
func TestLessonsHookNeverInvokes(t *testing.T) {
	// exploreRepo sets once-per-context delivery, so a repeat edit of the
	// same file in the same context is suppressed. The sentinel adds to
	// config.yaml only, so it keeps that lessons.yaml.
	root := exploreRepo(t)
	sentinel := newInvokerSentinel(t, root)
	recordEvaluationsOn(t)

	for _, c := range []struct {
		file, toolUse string
	}{
		{"a.go", "toolu-1"},
		{"a.go", "toolu-2"},
		{"nowhere.go", "toolu-3"},
	} {
		_, _, err := runIn(t, editPayload(filepath.Join(root, c.file), "session-1", c.toolUse, "prompt-1"),
			"-C", root, "lessons", "--hook")
		require.NoError(t, err, "edit of %s", c.file)
	}

	_, _, err := runIn(t, `{"session_id":"session-1","prompt_id":"prompt-2","hook_event_name":"PostCompact"}`,
		"-C", root, "lessons", "--hook-reset")
	require.NoError(t, err)

	sentinel.requireNoCall(t)

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	got := make([]reviews.DeliveryStatus, 0, len(firings))
	for _, rec := range firings {
		got = append(got, rec.Delivery)
	}

	assert.Equal(t, []reviews.DeliveryStatus{
		reviews.DeliveryInjected,
		reviews.DeliverySuppressedRepeat,
		reviews.DeliveryEvaluation,
		reviews.DeliveryReset,
	}, got, "one record per hook run with the recording switch on")
}

func TestLessonsHookResetJoinsEditRecords(t *testing.T) {
	// The reset hook hashes over the identity root of the edit hook. The
	// index here records a root through a symbolic link, so the git top
	// level gives other digests.
	root := hookRepo(t)
	top, link := recordLinkedRoot(t, root)

	reset := func() {
		t.Helper()

		_, _, err := runIn(t, resetPayload("session-1", "PostCompact"), "-C", root, "lessons", "--hook-reset")
		require.NoError(t, err)
	}

	out, _, err := runIn(t, editPayload(filepath.Join(root, "a.go"), "session-1", "toolu-1", "prompt-1"),
		"-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "keep-main-small")

	// Without the recording switch the reset writes no record.
	reset()

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)

	recordEvaluationsOn(t)
	reset()

	firings, err = reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 2, "the reset record joins the edit record in one log")

	edit, rec := firings[0], firings[1]
	assert.Equal(t, reviews.DeliveryReset, rec.Delivery)
	assert.True(t, rec.RecordEvaluations)

	assert.Equal(t, reviews.SessionDigest(link, "session-1"), edit.SessionSHA, "the edit hashes over the meta root")
	assert.Equal(t, edit.SessionSHA, rec.SessionSHA, "the reset joins the edit by session")
	assert.Equal(t, edit.ContextSHA, rec.ContextSHA, "the reset joins the edit by receiver")
	assert.NotEqual(t, reviews.SessionDigest(top, "session-1"), rec.SessionSHA,
		"the git top level is not the identity root here")
	assert.Equal(t, reviews.TurnDigest(link, integration.ClaudeID, "session-1", "prompt-2"), rec.TurnSHA)
	assert.Equal(t, integration.LaneRoot, rec.Lane)
}

// TestLessonsHookResetUsesTheIdentityRoot proves that the reset hook
// resets the state at the identity root. The edit hook and the
// exploration hook key their state under that root. The index here
// records a root through a symbolic link. A reset at the git top level
// then writes a second state key and leaves the advice hidden.
func TestLessonsHookResetUsesTheIdentityRoot(t *testing.T) {
	root := exploreRepo(t)
	top, link := recordLinkedRoot(t, root)
	require.NotEqual(t, top, link, "the identity root differs from the git top level")

	file := filepath.Join(root, "a.go")

	read := func(toolUse string) string {
		t.Helper()

		out, _, err := exploreHook(t, root, readPayload(file, "session-1", toolUse))
		require.NoError(t, err)

		return out
	}

	edit := func(toolUse string) string {
		t.Helper()

		out, _, err := runIn(t, editPayload(file, "session-1", toolUse, "prompt-1"), "-C", root, "lessons", "--hook")
		require.NoError(t, err)

		return out
	}

	assert.Contains(t, read("toolu-r1"), "keep-main-small", "read 1 advises")
	assert.Empty(t, read("toolu-r2"), "read 2 is suppressed")
	assert.Contains(t, edit("toolu-e1"), "keep-main-small", "edit 1 advises")
	assert.Empty(t, edit("toolu-e2"), "edit 2 is suppressed")

	_, _, err := runIn(t, resetPayload("session-1", "PostCompact"), "-C", root, "lessons", "--hook-reset")
	require.NoError(t, err)

	assert.Contains(t, read("toolu-r3"), "keep-main-small", "the reset reaches the exploration state of the hook")
	assert.Contains(t, edit("toolu-e3"), "keep-main-small", "the reset reaches the edit state of the hook")

	// The reset found the entry of the hooks. It wrote no second key for
	// the same receiver.
	assert.Equal(t, 1, stateContexts(t, exploreStateFile(root)), "one exploration context key")
	assert.Equal(t, 1, stateContexts(t, editStateFile(root)), "one edit context key")
}

// TestLessonsHookResetIgnoresOtherEvents proves that --hook-reset acts
// only on PostCompact, the event that setup installs it for. Before the
// decoder checked hook_event_name, a stray SessionStart payload with a
// session id reset the edit state and the exploration state of that
// session.
func TestLessonsHookResetIgnoresOtherEvents(t *testing.T) {
	root := exploreRepo(t)
	recordEvaluationsOn(t)

	file := filepath.Join(root, "a.go")

	read := func(toolUse string) string {
		t.Helper()

		out, _, err := exploreHook(t, root, readPayload(file, "session-1", toolUse))
		require.NoError(t, err)

		return out
	}

	edit := func(toolUse string) string {
		t.Helper()

		out, _, err := runIn(t, editPayload(file, "session-1", toolUse, "prompt-1"), "-C", root, "lessons", "--hook")
		require.NoError(t, err)

		return out
	}

	resets := func() int {
		t.Helper()

		firings, err := reviews.ReadFirings(root)
		require.NoError(t, err)

		n := 0
		for _, f := range firings {
			if f.Delivery == reviews.DeliveryReset {
				n++
			}
		}

		return n
	}

	assert.Contains(t, read("toolu-r1"), "keep-main-small", "read 1 advises")
	assert.Contains(t, edit("toolu-e1"), "keep-main-small", "edit 1 advises")

	exploreBefore, err := os.ReadFile(exploreStateFile(root))
	require.NoError(t, err)

	editBefore, err := os.ReadFile(editStateFile(root))
	require.NoError(t, err)

	// Each payload names the session. Only the event name is wrong.
	for _, payload := range []string{
		`{"session_id":"session-1","hook_event_name":"SessionStart","source":"compact"}`,
		resetPayload("session-1", "PreCompact"),
		`{"session_id":"session-1","prompt_id":"prompt-2"}`,
	} {
		out, stderr, err := runIn(t, payload, "-C", root, "lessons", "--hook-reset")
		require.NoError(t, err, "the reset hook never fails: %s", payload)
		assert.Empty(t, out, payload)
		assert.Empty(t, stderr, payload)
	}

	exploreAfter, err := os.ReadFile(exploreStateFile(root))
	require.NoError(t, err)

	editAfter, err := os.ReadFile(editStateFile(root))
	require.NoError(t, err)

	assert.Equal(t, string(exploreBefore), string(exploreAfter), "no other event resets the exploration state")
	assert.Equal(t, string(editBefore), string(editAfter), "no other event resets the edit state")
	assert.Zero(t, resets(), "no other event writes a reset record")
	assert.Empty(t, read("toolu-r2"), "read 2 stays suppressed")
	assert.Empty(t, edit("toolu-e2"), "edit 2 stays suppressed")

	// Positive control: PostCompact resets both states, so the absence
	// checks above can fail.
	_, _, err = runIn(t, resetPayload("session-1", "PostCompact"), "-C", root, "lessons", "--hook-reset")
	require.NoError(t, err)

	assert.Equal(t, 1, resets(), "PostCompact writes one reset record")
	assert.Contains(t, read("toolu-r3"), "keep-main-small", "PostCompact resets the exploration state")
	assert.Contains(t, edit("toolu-e3"), "keep-main-small", "PostCompact resets the edit state")
}
