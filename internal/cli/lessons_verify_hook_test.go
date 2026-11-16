package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests of the post-edit hook, lessons --verify --hook. Every test
// builds a temporary repository with guardFixture and runs the hooks
// under the invoker sentinel, which records each start of a model
// client. Nothing reads or writes the repository of this checkout.

// The payloads that give the shape of the completion events: a native
// Claude Code recording and a synthetic Codex fixture. Both name another
// tool, so each helper keeps the shape and puts the edit tool, its
// input, and the test IDs in.
var (
	claudeCompletionFixture = filepath.Join("..", "integration", "testdata", "claude", "native", "hooks", "004-PostToolUseFailure.json")
	codexCompletionFixture  = filepath.Join("..", "integration", "testdata", "codex", "post_tool_use_nonzero_exit.json")
)

// The tool outcomes of a Claude Code completion event.
const (
	claudeSucceeded   = "succeeded"
	claudeFailed      = "failed"
	claudeInterrupted = "interrupted"
)

// fixturePayload reads one payload fixture as a JSON object.
func fixturePayload(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))

	return payload
}

// claudeCompletionEvent returns a Claude Code completion payload of the
// call toolUse of tool with input. A succeeded call is PostToolUse; a
// failed or interrupted call is PostToolUseFailure with is_interrupt.
func claudeCompletionEvent(t *testing.T, root, outcome, tool, toolUse string, input map[string]any) string {
	t.Helper()

	payload := fixturePayload(t, claudeCompletionFixture)
	payload["session_id"], payload["tool_use_id"], payload["cwd"] = guardSession, toolUse, root
	payload["tool_name"], payload["tool_input"] = tool, input

	switch outcome {
	case claudeSucceeded:
		payload["hook_event_name"] = "PostToolUse"
		payload["tool_response"] = map[string]any{"success": true}

		delete(payload, "error")
		delete(payload, "is_interrupt")
	case claudeFailed, claudeInterrupted:
		payload["hook_event_name"] = "PostToolUseFailure"
		payload["is_interrupt"] = outcome == claudeInterrupted
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	return string(data)
}

// claudeWriteDone is the PostToolUse payload of the test Write of rel.
func claudeWriteDone(t *testing.T, root, rel, content string) string {
	t.Helper()

	return claudeCompletionEvent(t, root, claudeSucceeded, "Write", guardToolUse,
		map[string]any{"file_path": filepath.Join(root, rel), "content": content})
}

// codexCompletionEvent returns a Codex PostToolUse payload of apply_patch
// with the patch body and the exit code in tool_response.
func codexCompletionEvent(t *testing.T, cwd, body string, exitCode int) string {
	t.Helper()

	payload := fixturePayload(t, codexCompletionFixture)
	payload["session_id"], payload["tool_use_id"], payload["cwd"] = guardSession, guardToolUse, cwd
	payload["tool_name"] = "apply_patch"
	payload["tool_input"] = map[string]any{"command": "*** Begin Patch\n" + body + "*** End Patch\n"}
	payload["tool_response"] = map[string]any{"exit_code": exitCode, "output": "patch output"}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	return string(data)
}

// verifyHook runs `seamark -C root lessons --verify --hook [args]` in the
// test process with payload on stdin. The hook always exits 0.
func verifyHook(t *testing.T, root, payload string, args ...string) (stdout, stderr string) {
	t.Helper()

	stdout, stderr, err := runIn(t, payload, append([]string{"-C", root, "lessons", "--verify", "--hook"}, args...)...)
	require.NoError(t, err, "the post-edit hook always exits 0")

	return stdout, stderr
}

// claudeFindings decodes the Claude Code reply of the post-edit hook and
// returns its event name and its text.
func claudeFindings(t *testing.T, stdout string) (event, text string) {
	t.Helper()

	return exploreReply(t, stdout)
}

// requireReportOnly checks that a findings text never claims that the
// edit was stopped: the tool already ran.
func requireReportOnly(t *testing.T, text string) {
	t.Helper()

	assert.Contains(t, text, "post-edit")

	for _, word := range []string{"blocked", "prevented", "denied", "permissionDecision"} {
		assert.NotContains(t, strings.ToLower(text), strings.ToLower(word))
	}
}

// pinRefOf returns the pin-ref of the fixture pin with the rule.
func pinRefOf(t *testing.T, root, rule string) string {
	t.Helper()

	return guardPins(t, root)[rule].Ref
}

// TestLessonsVerifyHookReportsActualDelta proves that the post-edit hook
// checks the actual delta of a completed edit. It compares the state
// before the edit with the state after it, not the working tree with
// HEAD. A TODO marker that the working tree held before the edit gives
// no finding. A marker that the edit adds gives one finding with its
// line, the file stays as written, and the record holds the outcome
// without an excerpt.
func TestLessonsVerifyHookReportsActualDelta(t *testing.T) {
	root, sentinel := guardFixture(t)

	t.Run("an older marker is not a finding of this edit", func(t *testing.T) {
		verifyWrite(t, root, "src/handler.go", "package src\n\n// TODO(old): keep\nfunc Handle() {}\n")

		input := map[string]any{"file_path": filepath.Join(root, "src", "handler.go"), "old_string": "func Handle() {}", "new_string": "func Handle() { _ = 1 }"}

		_, _, err := guardHook(t, root, claudeGuardEvent(t, root, "Edit", guardToolUse, input))
		require.NoError(t, err, "the edit adds no marker, so the guard allows it")
		require.Len(t, pendingNames(t, root), 1)

		verifyWrite(t, root, "src/handler.go", "package src\n\n// TODO(old): keep\nfunc Handle() { _ = 1 }\n")

		stdout, stderr := verifyHook(t, root, claudeCompletionEvent(t, root, claudeSucceeded, "Edit", guardToolUse, input))
		assert.Empty(t, stdout, "a pass says nothing")
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, lessoncheck.BoundaryActualEdit, rec.Boundary)
		assert.Equal(t, lessoncheck.SummaryPassed, rec.State)
		assert.Equal(t, lessoncheck.AttributionProjectionMatch, rec.Attribution)
		assert.Equal(t, lessoncheck.ToolSucceeded, rec.ToolOutcome)
		assert.Equal(t, []string{"src/handler.go"}, rec.Paths)
		assert.NotEmpty(t, rec.SnapshotSHA)
		require.NotNil(t, rec.Emitted)
		assert.False(t, *rec.Emitted)
		require.Len(t, rec.Pins, 1, "only the enforce pin on src applies")
		assert.Equal(t, lessoncheck.OutcomePass, rec.Pins[0].Outcome)
		assert.Equal(t, reviews.MatchDigest(root, guardSession, guardToolUse), rec.MatchSHA)
		assert.Equal(t, reviews.ContextDigest(root, integration.ClaudeID, claudeReceiver(guardSession)), rec.ContextSHA,
			"the context digest comes from the pending record")

		assert.Empty(t, pendingNames(t, root), "the hook deletes the record it used")

		_, err = verifyRun(t, root)
		require.ErrorIs(t, err, lessoncheck.ErrViolation, "the working tree against HEAD holds the older marker")
	})

	t.Run("an added marker is a finding with its line", func(t *testing.T) {
		content := "readme\nTODO(docs): fill in\n"

		_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", content))
		require.NoError(t, err)

		verifyWrite(t, root, "docs/readme.md", content)

		stdout, stderr := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", content))
		assert.Empty(t, stderr)

		event, text := claudeFindings(t, stdout)
		assert.Equal(t, "PostToolUse", event)

		ref := pinRefOf(t, root, "no-todo-docs")
		assert.Contains(t, text, "seamark post-edit lesson checks after this Write call: violation (1 evaluated pin: 1 violation, 0 unknown; snapshot ")
		assert.Contains(t, text, "- violation: pin "+ref+", check no-todo (no-added-pattern/1): docs/readme.md:2 adds a line that matches the pattern")
		assert.Contains(t, text, "The files stay as written; repair the violation in a later edit.")
		assert.Contains(t, text, "Check again: seamark lessons --verify --file "+filepath.Join(root, "docs", "readme.md"))
		assert.NotContains(t, text, "fill in", "the reply holds no excerpt")
		requireReportOnly(t, text)

		data, err := os.ReadFile(filepath.Join(root, "docs", "readme.md"))
		require.NoError(t, err)
		assert.Equal(t, content, string(data), "the file stays as written")

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, lessoncheck.SummaryViolation, rec.State)
		require.NotNil(t, rec.Emitted)
		assert.True(t, *rec.Emitted)
		require.Len(t, rec.Pins, 1)
		assert.Equal(t, lessoncheck.ModeCheck, rec.Pins[0].Mode)
		assert.Equal(t, []int{2}, rec.Pins[0].EvidenceLines)

		stream, err := os.ReadFile(checkStream(root))
		require.NoError(t, err)
		assert.NotContains(t, string(stream), "fill in", "the record holds no content")
		requireNoRawIDs(t, root)
		assert.NoFileExists(t, reviews.AuditLogPath(root), "the post-edit hook writes no firing record")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookFailedAndInterrupted proves the post-edit check
// of a failed or interrupted tool. The hook checks the state on disk
// anyway, names the tool outcome, and answers with the event of the
// failure. A failed tool that wrote nothing gives no finding. Codex
// reports a failed patch by a non-zero exit code in PostToolUse. The
// findings go to stdout as the PostToolUse additionalContext reply with
// exit 0, and a pass writes nothing.
func TestLessonsVerifyHookFailedAndInterrupted(t *testing.T) {
	root, sentinel := guardFixture(t)
	content := "readme\nTODO(partial): x\n"

	for _, outcome := range []string{claudeFailed, claudeInterrupted} {
		t.Run(outcome, func(t *testing.T) {
			_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", content))
			require.NoError(t, err)

			// The tool wrote a part of the content before it stopped.
			verifyWrite(t, root, "docs/readme.md", content)

			input := map[string]any{"file_path": filepath.Join(root, "docs", "readme.md"), "content": content}
			stdout, _ := verifyHook(t, root, claudeCompletionEvent(t, root, outcome, "Write", guardToolUse, input))

			event, text := claudeFindings(t, stdout)
			assert.Equal(t, "PostToolUseFailure", event)

			verb := map[string]string{claudeFailed: "failed", claudeInterrupted: "was interrupted"}[outcome]
			assert.Contains(t, text, "The Write tool "+verb+", so the checks ran on the files as they are now.")
			assert.Contains(t, text, "docs/readme.md:2 adds a line that matches the pattern")
			requireReportOnly(t, text)

			rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
			assert.Equal(t, lessoncheck.ToolOutcome(outcome), rec.ToolOutcome)
			assert.Equal(t, lessoncheck.SummaryViolation, rec.State)

			verifyWrite(t, root, "docs/readme.md", "readme\n")
		})
	}

	t.Run("a failed tool that wrote nothing gives no finding", func(t *testing.T) {
		_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", content))
		require.NoError(t, err)

		input := map[string]any{"file_path": filepath.Join(root, "docs", "readme.md"), "content": content}
		stdout, stderr := verifyHook(t, root, claudeCompletionEvent(t, root, claudeFailed, "Write", guardToolUse, input))
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, lessoncheck.ToolFailed, rec.ToolOutcome)
		assert.Equal(t, lessoncheck.SummaryNoApplicableChecks, rec.State)
		assert.Equal(t, lessoncheck.AttributionProjectionDiffers, rec.Attribution, "the projected write did not happen")
	})

	t.Run("a failed Codex patch reports in the PostToolUse context", func(t *testing.T) {
		body := "*** Add File: docs/new.md\n+TODO(codex): y\n"

		_, _, err := baselineHook(t, root, codexPatchEvent(t, root, body), "--client", "codex")
		require.NoError(t, err)

		verifyWrite(t, root, "docs/new.md", "TODO(codex): y\n")

		stdout, stderr := verifyHook(t, root, codexCompletionEvent(t, root, body, 1), "--client", "codex")
		assert.Empty(t, stderr, "Codex never shows stderr of a PostToolUse hook with exit 0 to the model")

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, integration.CodexID, rec.Client)
		assert.Equal(t, lessoncheck.ToolFailed, rec.ToolOutcome)
		assert.Equal(t, []string{"docs/new.md"}, rec.Paths)
		require.Len(t, rec.SnapshotSHA, 64)

		text := "seamark post-edit lesson checks after this apply_patch call: violation " +
			"(1 evaluated pin: 1 violation, 0 unknown; snapshot " + rec.SnapshotSHA[:12] + ")\n" +
			"- violation: pin " + pinRefOf(t, root, "no-todo-docs") + ", check no-todo (no-added-pattern/1): " +
			"docs/new.md:1 adds a line that matches the pattern\n" +
			"The apply_patch tool failed, so the checks ran on the files as they are now.\n" +
			"The files stay as written; repair the violation in a later edit. " +
			"Check again: seamark lessons --verify --file " + filepath.Join(root, "docs", "new.md") + "\n"
		assert.Equal(t, `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"`+
			strings.ReplaceAll(text, "\n", `\n`)+`"}}`+"\n", stdout, "the exact JSON reply of one finding")
		requireReportOnly(t, text)
	})

	t.Run("a Codex patch without a finding writes nothing", func(t *testing.T) {
		body := "*** Add File: docs/clean.md\n+clean\n"

		_, _, err := baselineHook(t, root, codexPatchEvent(t, root, body), "--client", "codex")
		require.NoError(t, err)

		verifyWrite(t, root, "docs/clean.md", "clean\n")

		stdout, stderr := verifyHook(t, root, codexCompletionEvent(t, root, body, 0), "--client", "codex")
		assert.Empty(t, stdout, "a pass gives no JSON reply")
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, integration.CodexID, rec.Client)
		assert.Equal(t, lessoncheck.SummaryPassed, rec.State)
		assert.Equal(t, lessoncheck.AttributionProjectionMatch, rec.Attribution)
		require.NotNil(t, rec.Emitted)
		assert.False(t, *rec.Emitted, "the hook wrote no reply")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookMissingBaseline proves the rule for a call
// without a usable pending record: each executable pin in scope of the
// event paths is unknown/missing-baseline, the attribution is
// no-baseline, and the findings name the rerun with absolute paths. An
// expired record is discarded by the sweep and never serves as a pass. A
// guard deny leaves no record, so the post-edit record of the same call
// joins the guard record by its match digest.
func TestLessonsVerifyHookMissingBaseline(t *testing.T) {
	requireMissing := func(t *testing.T, root, stdout string, rules ...string) lessoncheck.AuditRecord {
		t.Helper()

		_, text := claudeFindings(t, stdout)

		for _, rule := range rules {
			assert.Contains(t, text, "- unknown/missing-baseline: pin "+pinRefOf(t, root, rule)+", check ")
		}

		assert.Contains(t, text, "No pre-edit baseline exists for this call, so the checks cannot tell what this edit changed.")
		requireReportOnly(t, text)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, lessoncheck.AttributionNoBaseline, rec.Attribution)
		assert.Equal(t, lessoncheck.SummaryIncomplete, rec.State)
		assert.Empty(t, rec.SnapshotSHA)
		require.Len(t, rec.Pins, len(rules))

		for _, pin := range rec.Pins {
			assert.Equal(t, lessoncheck.OutcomeUnknown, pin.Outcome)
			assert.Equal(t, lessoncheck.ReasonMissingBaseline, pin.Reason)
		}

		return rec
	}

	t.Run("no record", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\n"))
		rec := requireMissing(t, root, stdout, "no-todo-docs")
		assert.Empty(t, rec.ContextSHA, "a completion event names no receiver")
		assert.Equal(t, []string{"docs/readme.md"}, rec.Paths)

		_, text := claudeFindings(t, stdout)
		assert.Contains(t, text, "Check again: seamark lessons --verify --file "+filepath.Join(root, "docs", "readme.md"))

		sentinel.requireNoCall(t)
	})

	t.Run("after a guard deny", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		requireDenied(t, err)
		assert.Empty(t, pendingNames(t, root), "a deny writes no record")

		guard := lastRecord(t, root, lessoncheck.AuditGuard)

		// The client ran the tool anyway: the edit is on disk.
		verifyWrite(t, root, "generated/a.txt", "changed\n")

		stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "generated/a.txt", "changed\n"))
		rec := requireMissing(t, root, stdout, "frozen-assets")
		assert.Equal(t, guard.MatchSHA, rec.MatchSHA, "the records of one call join on the match digest")

		sentinel.requireNoCall(t)
	})

	t.Run("an expired record is discarded and never used", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
		require.NoError(t, err)

		names := pendingNames(t, root)
		require.Len(t, names, 1)

		old := time.Now().Add(-2 * time.Hour)
		require.NoError(t, os.Chtimes(filepath.Join(checksDir(root), "pending", names[0]), old, old))

		verifyWrite(t, root, "docs/readme.md", "readme\nmore\n")

		stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\nmore\n"))
		requireMissing(t, root, stdout, "no-todo-docs")

		recs := checkRecords(t, root)
		require.GreaterOrEqual(t, len(recs), 2)
		assert.Equal(t, lessoncheck.AuditBaselineDiscarded, recs[len(recs)-2].Kind)
		assert.Equal(t, lessoncheck.DiscardExpired, recs[len(recs)-2].Discarded)
		assert.Empty(t, pendingNames(t, root))

		sentinel.requireNoCall(t)
	})

	t.Run("an unsafe checks directory gives no baseline", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o700))
		require.NoError(t, os.Symlink(t.TempDir(), checksDir(root)))

		stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\n"))
		rec := requireMissing(t, root, stdout, "no-todo-docs")
		assert.Contains(t, rec.Error, "lesson checks: .seamark/checks is not a real directory")

		sentinel.requireNoCall(t)
	})

	t.Run("an event that does not decode leaves every pin unknown", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		stdout, _ := verifyHook(t, root, "{")

		_, text := claudeFindings(t, stdout)
		assert.Contains(t, text, "Check again: seamark lessons --verify\n", "no path is known")

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		require.Len(t, rec.Pins, 3, "the scope of the edit is not known, so every executable pin applies")

		for _, pin := range rec.Pins {
			assert.Equal(t, lessoncheck.ReasonUnparseableInput, pin.Reason)
		}

		assert.False(t, rec.PathsComplete)
		assert.Equal(t, lessoncheck.ToolUnknown, rec.ToolOutcome)

		sentinel.requireNoCall(t)
	})

	t.Run("another tool or client says nothing", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
		require.NoError(t, err)

		before := pendingNames(t, root)

		// The native recording itself: a Bash tool after the same session.
		native, err := os.ReadFile(claudeCompletionFixture)
		require.NoError(t, err)

		stdout, stderr := verifyHook(t, root, string(native))
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Empty(t, rec.Pins)
		assert.Contains(t, rec.Error, "not one of the covered edit tools")
		assert.Equal(t, before, pendingNames(t, root), "an uncovered event keeps the baseline of another call")

		// An uncovered tool with the tool-use ID of the pending call, as a
		// hand-edited matcher can send it, keeps the baseline too.
		stdout, _ = verifyHook(t, root, claudeCompletionEvent(t, root, claudeSucceeded, "Bash", guardToolUse,
			map[string]any{"command": "ls"}))
		assert.Empty(t, stdout)
		assert.Equal(t, before, pendingNames(t, root), "only a covered completion consumes the baseline")

		stdout, stderr = verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\n"), "--client", "nosuch")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Contains(t, lastRecord(t, root, lessoncheck.AuditPostEdit).Error, `unknown hook client "nosuch"`)

		sentinel.requireNoCall(t)
	})
}

// TestLessonsVerifyHookRepairRerun proves the repair loop of the
// post-edit check. A violation stays written, and the next edit, which
// repairs it, gets its own post-edit check against its own baseline.
// The repair passes, the hook says nothing, and lessons --verify then
// passes too.
func TestLessonsVerifyHookRepairRerun(t *testing.T) {
	root, sentinel := guardFixture(t)
	path := filepath.Join(root, "docs", "readme.md")

	edit := func(toolUse, old, repl string) {
		t.Helper()

		input := map[string]any{"file_path": path, "old_string": old, "new_string": repl}

		_, _, err := baselineHook(t, root, claudeGuardEvent(t, root, "Edit", toolUse, input))
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		verifyWrite(t, root, "docs/readme.md", strings.Replace(string(data), old, repl, 1))
	}

	done := func(toolUse, old, repl string) string {
		t.Helper()

		input := map[string]any{"file_path": path, "old_string": old, "new_string": repl}
		stdout, _ := verifyHook(t, root, claudeCompletionEvent(t, root, claudeSucceeded, "Edit", toolUse, input))

		return stdout
	}

	edit("toolu-first", "readme\n", "readme\nTODO(fix): later\n")

	_, text := claudeFindings(t, done("toolu-first", "readme\n", "readme\nTODO(fix): later\n"))
	assert.Contains(t, text, "docs/readme.md:2 adds a line that matches the pattern")
	assert.Contains(t, text, "repair the violation in a later edit")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "readme\nTODO(fix): later\n", string(data), "the hook never reverts the edit")

	edit("toolu-repair", "TODO(fix): later\n", "Fixed later.\n")
	assert.Empty(t, done("toolu-repair", "TODO(fix): later\n", "Fixed later.\n"), "the repair passes, so the hook says nothing")

	rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
	assert.Equal(t, lessoncheck.SummaryPassed, rec.State)
	assert.Equal(t, reviews.MatchDigest(root, guardSession, "toolu-repair"), rec.MatchSHA)
	assert.Empty(t, pendingNames(t, root))

	_, err = verifyRun(t, root, "--file", path)
	require.NoError(t, err, "the working tree passes after the repair")

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookProjectionDiffers proves the attribution of an
// observed result that differs from the projection: another writer
// changed the file between the two hooks. The check runs on the observed
// delta, and the findings say that the delta can hold that change.
func TestLessonsVerifyHookProjectionDiffers(t *testing.T) {
	root, sentinel := guardFixture(t)

	_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
	require.NoError(t, err)

	// Another writer, not the projected Write, leaves this content.
	verifyWrite(t, root, "docs/readme.md", "readme\nTODO(other): writer\n")

	stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\nmore\n"))

	_, text := claudeFindings(t, stdout)
	assert.Contains(t, text, "The observed result differs from the proposed edit, so the change can include a change by another writer.")
	assert.Contains(t, text, "docs/readme.md:2 adds a line that matches the pattern")
	requireReportOnly(t, text)

	rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
	assert.Equal(t, lessoncheck.AttributionProjectionDiffers, rec.Attribution)
	assert.Equal(t, lessoncheck.SummaryViolation, rec.State)

	t.Run("a matching result has no such note", func(t *testing.T) {
		_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "TODO(same): x\n"))
		require.NoError(t, err)

		verifyWrite(t, root, "docs/readme.md", "TODO(same): x\n")

		stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "TODO(same): x\n"))

		_, text := claudeFindings(t, stdout)
		assert.NotContains(t, text, "another writer")
		assert.Equal(t, lessoncheck.AttributionProjectionMatch, lastRecord(t, root, lessoncheck.AuditPostEdit).Attribution)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookCodexAddMatchesNativeBytes proves that a Codex
// Add File projects the bytes that Codex writes, final newline included.
// A file without that newline differs, so the compare is exact.
func TestLessonsVerifyHookCodexAddMatchesNativeBytes(t *testing.T) {
	root, sentinel := guardFixture(t)
	body := "*** Add File: src/gen.go\n+package gen\n"

	for _, tc := range []struct {
		written     string
		attribution lessoncheck.Attribution
	}{
		{"package gen\n", lessoncheck.AttributionProjectionMatch},
		{"package gen", lessoncheck.AttributionProjectionDiffers},
	} {
		_, _, err := guardHook(t, root, codexPatchEvent(t, root, body), "--client", "codex")
		require.NoError(t, err, "the add holds no marker, so the guard allows it")

		verifyWrite(t, root, "src/gen.go", tc.written)

		stdout, stderr := verifyHook(t, root, codexCompletionEvent(t, root, body, 0), "--client", "codex")
		assert.Empty(t, stdout, "a pass gives no JSON reply")
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, integration.CodexID, rec.Client)
		assert.Equal(t, lessoncheck.SummaryPassed, rec.State)
		assert.Equal(t, tc.attribution, rec.Attribution, "written %q", tc.written)
	}

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookCodexControlPathIsIncomplete proves the post-edit
// rule for a Codex header path with a tab or a carriage return. Codex
// writes the path without the character, so the hook cannot place the
// edit. Every executable pin is unknown/incomplete-paths, also with the
// written file on disk. The named path is outside every scope, so a
// check of that path alone would report nothing.
func TestLessonsVerifyHookCodexControlPathIsIncomplete(t *testing.T) {
	root, sentinel := guardFixture(t)

	for _, body := range []string{
		"*** Add File: gen\rerated/b.txt\n+x\n",
		"*** Update File: README.md\n*** Move to: gen\terated/b.txt\n",
	} {
		_, _, err := baselineHook(t, root, codexPatchEvent(t, root, body), "--client", "codex")
		require.NoError(t, err)
		assert.Empty(t, pendingNames(t, root), "the baseline handler stores nothing for a refused path")

		// The file that codex-cli 0.169.0 writes for either patch.
		verifyWrite(t, root, "generated/b.txt", "x\n")

		stdout, stderr := verifyHook(t, root, codexCompletionEvent(t, root, body, 0), "--client", "codex")
		assert.Empty(t, stderr)

		event, text := claudeFindings(t, stdout)
		assert.Equal(t, "PostToolUse", event)

		for _, rule := range []string{"frozen-assets", "no-todo", "no-todo-docs"} {
			assert.Contains(t, text, "- unknown/incomplete-paths: pin "+pinRefOf(t, root, rule)+", check ")
		}

		assert.Contains(t, text, "Check again: seamark lessons --verify\n", "no path is known")
		requireReportOnly(t, text)

		rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
		assert.Equal(t, integration.CodexID, rec.Client)
		assert.Equal(t, lessoncheck.ToolSucceeded, rec.ToolOutcome)
		assert.False(t, rec.PathsComplete)
		assert.Empty(t, rec.Paths, "no record names the path of the refused patch")
		assert.Contains(t, rec.Error, "a header path holds a tab or a carriage return")
		require.Len(t, rec.Pins, 3, "the scope of the edit is not known, so every executable pin applies")

		for _, pin := range rec.Pins {
			assert.Equal(t, lessoncheck.OutcomeUnknown, pin.Outcome)
			assert.Equal(t, lessoncheck.ReasonIncompletePaths, pin.Reason)
		}

		require.NoError(t, os.Remove(filepath.Join(root, "generated", "b.txt")))
	}

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookCallsNoInvoker proves that the post-edit hook
// starts no model client and makes no network call. The runs cover a
// pass, a violation, a missing baseline, an event that does not decode,
// a Codex event, an unknown client, and an invalid lessons.yaml.
func TestLessonsVerifyHookCallsNoInvoker(t *testing.T) {
	root, sentinel := guardFixture(t)

	_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
	require.NoError(t, err)
	verifyWrite(t, root, "docs/readme.md", "readme\nmore\n")
	verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "readme\nmore\n"))

	_, _, err = baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "TODO(x): y\n"))
	require.NoError(t, err)
	verifyWrite(t, root, "docs/readme.md", "TODO(x): y\n")
	verifyHook(t, root, claudeWriteDone(t, root, "docs/readme.md", "TODO(x): y\n"))

	verifyHook(t, root, claudeWriteDone(t, root, "src/handler.go", "package src\n"))
	verifyHook(t, root, "{")
	verifyHook(t, root, codexCompletionEvent(t, root, "*** Add File: docs/c.md\n+c\n", 0), "--client", "codex")
	verifyHook(t, root, claudeWriteDone(t, root, "README.md", "x\n"), "--client", "nosuch")

	verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")

	stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "README.md", "x\n"))
	_, text := claudeFindings(t, stdout)
	assert.Contains(t, text, "- unknown/configuration-error: lesson checks cannot be established: ")

	posts := 0

	for _, rec := range checkRecords(t, root) {
		if rec.Kind == lessoncheck.AuditPostEdit {
			posts++
		}
	}

	assert.Equal(t, 7, posts, "each run appends one post-edit record")
	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookRefusesGrantForms proves that the skill grants
// never reach a hook entry point. The grants
// `Bash(seamark lessons --verify --json*)` and
// `Bash(seamark lessons --verify --file *)` match a call with --hook
// appended, and the grants `--file *` and `--region *` match a plain
// --hook or --hook-reset call. Each such call exits 1 with its refusal
// text before any handler runs: it reads and deletes no pending record
// and writes no record or state. The grant
// `Bash(seamark lessons --verify)` has no wildcard, so it never matches
// `--verify --hook`.
func TestLessonsVerifyHookRefusesGrantForms(t *testing.T) {
	root, sentinel := guardFixture(t)
	file := filepath.Join(root, "docs", "readme.md")

	_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nTODO(x): y\n"))
	require.NoError(t, err)
	verifyWrite(t, root, "docs/readme.md", "readme\nTODO(x): y\n")

	names := pendingNames(t, root)
	require.Len(t, names, 1)

	record, err := os.ReadFile(filepath.Join(checksDir(root), "pending", names[0]))
	require.NoError(t, err)

	streamBefore, err := os.ReadFile(checkStream(root))
	require.NoError(t, err)

	done := claudeWriteDone(t, root, "docs/readme.md", "readme\nTODO(x): y\n")
	edit := claudeWriteEvent(t, root, "docs/readme.md", "readme\nTODO(x): y\n")
	reset := resetPayload(guardSession, "PostCompact")

	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{done, []string{"--verify", "--json", "--hook"}, "--verify --hook takes no --file or --json"},
		{done, []string{"--verify", "--file", file, "--hook"}, "--verify --hook takes no --file or --json"},
		{done, []string{"--verify", "--json", "--file", file, "--hook"}, "--verify --hook takes no --file or --json"},
		{edit, []string{"--file", file, "--hook"}, "--hook does not combine with --file"},
		{edit, []string{"--region", "docs", "--hook"}, "--hook does not combine with --region"},
		{edit, []string{"--list", "--hook"}, "--hook does not combine with --list"},
		{edit, []string{"--stats", "--hook"}, "--hook does not combine with --stats"},
		{reset, []string{"--file", file, "--hook-reset"}, "--hook-reset does not combine with --file"},
		{reset, []string{"--region", ".", "--hook-reset"}, "--hook-reset does not combine with --region"},
	} {
		stdout, _, err := runIn(t, c.stdin, append([]string{"-C", root, "lessons"}, c.args...)...)
		require.Error(t, err, "args %v", c.args)
		assert.Equal(t, c.want, err.Error(), "args %v", c.args)
		assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1")
		assert.NotErrorIs(t, err, lessoncheck.ErrViolation, "a refusal exits 1")
		assert.Empty(t, stdout, "args %v", c.args)

		data, err := os.ReadFile(filepath.Join(checksDir(root), "pending", names[0]))
		require.NoError(t, err, "args %v: the pending record stays", c.args)
		assert.Equal(t, record, data, "args %v", c.args)

		stream, err := os.ReadFile(checkStream(root))
		require.NoError(t, err)
		assert.Equal(t, streamBefore, stream, "args %v: no record", c.args)

		assert.NoFileExists(t, reviews.AuditLogPath(root), "args %v: no firing record", c.args)
		assert.NoFileExists(t, editStateFile(root), "args %v: no delivery state", c.args)
		assert.NoFileExists(t, exploreStateFile(root), "args %v: no exploration state", c.args)
	}

	// The same payloads without the granted flag reach the hooks. So the
	// absence checks above can fail.
	stdout, _ := verifyHook(t, root, done)
	_, text := claudeFindings(t, stdout)
	assert.Contains(t, text, "violation")
	assert.Empty(t, pendingNames(t, root), "the post-edit hook reads and deletes the record")
	assert.Equal(t, lessoncheck.AuditPostEdit, lastRecord(t, root, lessoncheck.AuditPostEdit).Kind)

	stdout, _, err = runIn(t, edit, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, stdout, "additionalContext", "the plain edit hook still gives advice")
	assert.FileExists(t, reviews.AuditLogPath(root))

	sentinel.requireNoCall(t)
}

// TestLessonCheckHookDigestsJoinFiringLog proves the identity root join
// of one tool call across three hooks: the guard record, the post-edit
// record, and the version 2 firing record of lessons --hook share
// match_sha256, session_sha256, and context_sha256. The index meta
// records the root through a symbolic link, so every digest hashes over
// the link and differs from a digest over the git top level.
func TestLessonCheckHookDigestsJoinFiringLog(t *testing.T) {
	root := hookRepo(t)
	verifyWrite(t, root, ".seamark/lessons.yaml", `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: keep-main-small
    region: a.go
    note: Move the work into helpers.
    mode: enforce
    check: no-todo
`)

	top, link := recordLinkedRoot(t, root)
	sentinel := newInvokerSentinel(t, root)

	input := map[string]any{"file_path": filepath.Join(top, "a.go"), "old_string": "func helper() {}", "new_string": "func helper() { _ = 1 }"}
	pre := claudeGuardEvent(t, top, "Edit", guardToolUse, input)

	_, _, err := guardHook(t, top, pre)
	require.NoError(t, err, "the edit adds no marker")

	advice, _, err := runIn(t, pre, "-C", top, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, advice, "keep-main-small")

	data, err := os.ReadFile(filepath.Join(top, "a.go"))
	require.NoError(t, err)
	verifyWrite(t, top, "a.go", strings.Replace(string(data), "func helper() {}", "func helper() { _ = 1 }", 1))

	verifyHook(t, top, claudeCompletionEvent(t, top, claudeSucceeded, "Edit", guardToolUse, input))

	post := lastRecord(t, top, lessoncheck.AuditPostEdit)

	var guard lessoncheck.AuditRecord

	for _, rec := range checkRecords(t, top) {
		if rec.Kind == lessoncheck.AuditGuard {
			guard = rec
		}
	}

	require.NotNil(t, guard.Guard, "the guard wrote its record")

	firings, err := reviews.ReadFirings(top)
	require.NoError(t, err)
	require.NotEmpty(t, firings)

	firing := firings[len(firings)-1]

	want := struct{ session, match, context string }{
		reviews.SessionDigest(link, guardSession),
		reviews.MatchDigest(link, guardSession, guardToolUse),
		reviews.ContextDigest(link, integration.ClaudeID, claudeReceiver(guardSession)),
	}

	for name, got := range map[string]struct{ session, match, context string }{
		"guard":     {guard.SessionSHA, guard.MatchSHA, guard.ContextSHA},
		"post-edit": {post.SessionSHA, post.MatchSHA, post.ContextSHA},
		"firing":    {firing.SessionSHA, firing.MatchSHA, firing.ContextSHA},
	} {
		assert.Equal(t, want, got, "%s record", name)
	}

	assert.NotEqual(t, reviews.MatchDigest(top, guardSession, guardToolUse), want.match,
		"the identity root is the link, so a digest over the git top level differs")
	assert.Equal(t, lessoncheck.AttributionProjectionMatch, post.Attribution, "the post-edit hook found the baseline")
	assert.Equal(t, lessoncheck.SummaryPassed, post.State)

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyHookBudgetBoundsEvaluation proves that the post-edit
// budget bounds the evaluation, and not only the observation. The diff
// of 20000 equal lines took 19 s before the fix. The hook now waits
// past the budget, and the result must be an unknown pin with
// resource-limit: a cut evaluation never reads as a pass or as a
// finding that no check made.
func TestLessonsVerifyHookBudgetBoundsEvaluation(t *testing.T) {
	root, sentinel := guardFixture(t)
	lines := lessoncheck.DefaultLimits().MaxDiffLines
	edited := strings.Repeat("}\n", lines/2-1) + "TODO(x)\n" + strings.Repeat("}\n", lines/2)

	commitRepeatedLines(t, root, lines)

	_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "src/repeated.txt", edited))
	require.NoError(t, err)
	require.Len(t, pendingNames(t, root), 1)

	verifyWrite(t, root, "src/repeated.txt", edited)

	wait := shortHookBudgets(t)
	beforePostEditEvaluate = func() { time.Sleep(wait) }

	t.Cleanup(func() { beforePostEditEvaluate = nil })

	stdout, _ := verifyHook(t, root, claudeWriteDone(t, root, "src/repeated.txt", edited))

	_, text := claudeFindings(t, stdout)
	assert.Contains(t, text, "incomplete")
	assert.Contains(t, text, "unknown/resource-limit")

	rec := lastRecord(t, root, lessoncheck.AuditPostEdit)
	assert.Equal(t, lessoncheck.SummaryIncomplete, rec.State)
	require.Len(t, rec.Pins, 1, "only the enforce pin on src applies")
	assert.Equal(t, lessoncheck.OutcomeUnknown, rec.Pins[0].Outcome)
	assert.Equal(t, lessoncheck.ReasonResourceLimit, rec.Pins[0].Reason)
	sentinel.requireNoCall(t)
}
