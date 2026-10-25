package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests in this file hold lessons --explore --hook (contract L2)
// through the CLI. AC-26: exploration never touches the edit hook state.
// BRIEF rule 15: the exploration hook never denies and runs no check.

// readPayload is a native Claude Code PostToolUse payload of the Read
// tool for file. The decoder never reads tool_response, so the payload
// carries content that a shell parser could take for a path.
func readPayload(file, session, toolUse string) string {
	return `{"session_id":"` + session + `","tool_use_id":"` + toolUse + `","prompt_id":"prompt-1",` +
		`"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"` + file + `"},` +
		`"tool_response":{"type":"text","file":{"filePath":"elsewhere.go","content":"package main"}}}`
}

// failedReadPayload is the PostToolUseFailure payload of a Read that
// failed, for example of a file that does not exist yet.
func failedReadPayload(file, session string) string {
	return `{"session_id":"` + session + `","tool_use_id":"toolu-fail","hook_event_name":"PostToolUseFailure",` +
		`"tool_name":"Read","tool_input":{"file_path":"` + file + `"},"error":"File does not exist."}`
}

// exploreHook runs `seamark -C root lessons --explore --hook [args]` in
// the test process with payload on stdin.
func exploreHook(t *testing.T, root, payload string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runIn(t, payload, append([]string{"-C", root, "lessons", "--explore", "--hook"}, args...)...)
}

// exploreReply decodes the one native reply of an exploration hook.
func exploreReply(t *testing.T, stdout string) (event, text string) {
	t.Helper()

	var reply struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}

	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&reply), "the reply has only the advisory fields: %s", stdout)
	assert.False(t, decoder.More(), "one JSON value")

	return reply.HookSpecificOutput.HookEventName, reply.HookSpecificOutput.AdditionalContext
}

// exploreRepo is hookRepo with once-per-context delivery for the edit
// hook, so the edit hook keeps a state file that exploration must leave
// alone.
func exploreRepo(t *testing.T) string {
	t.Helper()

	root := hookRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("hook_delivery: once-per-context\n"+
			"pin:\n  - {rule: keep-main-small, region: a.go, note: Move the work into helpers.}\n"), 0o644))

	return root
}

// editStateFile and exploreStateFile are the two delivery state files
// of root.
func editStateFile(root string) string {
	return filepath.Join(root, ".seamark", "lessons-hook-state.json")
}

func exploreStateFile(root string) string {
	return filepath.Join(root, ".seamark", "lessons-explore-state.json")
}

// TestLessonsExploreHookNeverTouchesEditState proves AC-26 through the
// CLI: exploration never reads or writes the edit hook state, so an
// edit of a pin right after a read of its file still gets the reminder.
// It fails when exploration marks the edit state delivered.
func TestLessonsExploreHookNeverTouchesEditState(t *testing.T) {
	root := exploreRepo(t)
	file := filepath.Join(root, "a.go")

	edit := func(session, toolUse string) string {
		t.Helper()

		out, _, err := runIn(t, editPayload(file, session, toolUse, "prompt-1"), "-C", root, "lessons", "--hook")
		require.NoError(t, err)

		return out
	}

	read := func(payload string) string {
		t.Helper()

		out, _, err := exploreHook(t, root, payload)
		require.NoError(t, err)

		return out
	}

	// Before any edit: two reads of one receiver. The first advises, the
	// second is suppressed by the exploration state alone.
	_, text := exploreReply(t, read(readPayload(file, "session-1", "toolu-r1")))
	assert.Contains(t, text, "keep-main-small")
	assert.Empty(t, read(readPayload(file, "session-1", "toolu-r2")), "the exploration lease dedupes")
	assert.FileExists(t, exploreStateFile(root))

	assert.NoFileExists(t, editStateFile(root), "exploration never creates the edit hook state")
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.lock"),
		"exploration never takes the edit hook lock")

	// The edit of the same pin right after the reads still emits.
	assert.Contains(t, edit("session-1", "toolu-e1"), "keep-main-small",
		"an edit right after exploration still gets its reminder")
	require.FileExists(t, editStateFile(root))

	before, err := os.ReadFile(editStateFile(root))
	require.NoError(t, err)

	// N exploration events of every kind: a repeated read, new root
	// receivers, a subagent, and a failed read. None changes a byte of
	// the edit hook state.
	child := strings.Replace(readPayload(file, "session-1", "toolu-c1"), `"prompt_id"`, `"agent_id":"agent-7","prompt_id"`, 1)

	for _, payload := range []string{
		readPayload(file, "session-1", "toolu-r3"),
		readPayload(file, "session-2", "toolu-r4"),
		readPayload(file, "session-3", "toolu-r5"),
		child,
		child,
		failedReadPayload(filepath.Join(root, "new.go"), "session-4"),
	} {
		read(payload)
	}

	after, err := os.ReadFile(editStateFile(root))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "exploration never writes deliveryContextState.Delivered")

	// A receiver that read the file and never edited it still gets the
	// edit reminder. The edit state still suppresses its own repeat.
	assert.Contains(t, edit("session-2", "toolu-e2"), "keep-main-small",
		"a read in session-2 marks nothing delivered for the edit hook")
	assert.Empty(t, edit("session-1", "toolu-e3"), "the edit hook keeps its own suppression")

	// The firing log names the two surfaces apart.
	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	surfaces := map[string]int{}
	for _, f := range firings {
		surfaces[f.Surface+"/"+string(f.Delivery)]++
	}

	assert.Equal(t, 2, surfaces["/"+string(reviews.DeliveryInjected)], "two edit reminders")
	assert.Equal(t, 1, surfaces["/"+string(reviews.DeliverySuppressedRepeat)], "one suppressed edit")
	assert.Equal(t, 5, surfaces[reviews.ExploreSurface+"/"+string(reviews.DeliveryInjected)],
		"session-1, session-2, session-3, the subagent twice")
	assert.Zero(t, surfaces[reviews.ExploreSurface+"/"+string(reviews.DeliverySuppressedRepeat)],
		"exploration writes no suppressed-repeat record")
}

// TestLessonsExploreHookIgnoresShellAndOtherTools proves that only a
// structured Read names a file. A shell command, a search, a listing,
// an edit, and a Read under another event write nothing, also with the
// recording switch on, which would record an evaluation of a decoded
// event.
func TestLessonsExploreHookIgnoresShellAndOtherTools(t *testing.T) {
	root := hookRepo(t)
	recordEvaluationsOn(t)

	file := filepath.Join(root, "a.go")
	read := readPayload(file, "session-1", "toolu-1")

	for name, payload := range map[string]string{
		"a shell cat": `{"session_id":"session-1","hook_event_name":"PostToolUse","tool_name":"Bash",` +
			`"tool_input":{"command":"cat ` + file + `"},"tool_response":{"stdout":"` + file + `"}}`,
		"a failed shell command": `{"session_id":"session-1","hook_event_name":"PostToolUseFailure","tool_name":"Bash",` +
			`"tool_input":{"command":"cat ` + file + `"}}`,
		"a search": `{"session_id":"session-1","hook_event_name":"PostToolUse","tool_name":"Grep",` +
			`"tool_input":{"pattern":"main","path":"` + file + `"}}`,
		"a listing": `{"session_id":"session-1","hook_event_name":"PostToolUse","tool_name":"Glob",` +
			`"tool_input":{"pattern":"a.go","path":"` + root + `"}}`,
		"an edit":            strings.Replace(read, `"tool_name":"Read"`, `"tool_name":"Edit"`, 1),
		"an MCP read tool":   strings.Replace(read, `"tool_name":"Read"`, `"tool_name":"mcp__files__Read"`, 1),
		"a read before use":  strings.Replace(read, `"PostToolUse"`, `"PreToolUse"`, 1),
		"a prompt":           `{"session_id":"session-1","hook_event_name":"UserPromptSubmit","prompt":"read ` + file + `"}`,
		"an edit hook event": editPayload(file, "session-1", "toolu-2", "prompt-1"),
	} {
		out, stderr, err := exploreHook(t, root, payload)
		require.NoError(t, err, name)
		assert.Empty(t, out, name)
		assert.Empty(t, stderr, name)
	}

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	assert.Empty(t, firings, "no record: the hook decoded no read event")
	assert.NoFileExists(t, exploreStateFile(root))

	// The structured Read of the same file advises and records.
	out, _, err := exploreHook(t, root, read)
	require.NoError(t, err)

	event, text := exploreReply(t, out)
	assert.Equal(t, "PostToolUse", event)
	assert.Contains(t, text, "a.go, which you just read")
	assert.NotContains(t, text, "elsewhere.go", "tool_response is never read")

	firings, err = reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)
	assert.Equal(t, reviews.ExploreSurface, firings[0].Surface)
	assert.Equal(t, "Read", firings[0].Tool)
	assert.Equal(t, "a.go", firings[0].File)
	assert.True(t, firings[0].CaptureOptIn)
}

// TestLessonsExploreHookFailsOpen proves the L2 error rules: every
// internal failure exits 0, and only a failed reply write is the hook's
// own failure.
func TestLessonsExploreHookFailsOpen(t *testing.T) {
	t.Run("input that the hook cannot use", func(t *testing.T) {
		root := hookRepo(t)
		outside := filepath.Join(t.TempDir(), "x.go")

		for name, payload := range map[string]string{
			"empty stdin":            "",
			"invalid JSON":           "{",
			"a Read without a path":  `{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{}}`,
			"a file outside":         readPayload(outside, "s", "t"),
			"a payload above 1 MiB":  readPayload(filepath.Join(root, "a.go"), strings.Repeat("s", 1<<20), "t"),
			"a JSON array of events": "[" + readPayload(filepath.Join(root, "a.go"), "s", "t") + "]",
		} {
			out, _, err := exploreHook(t, root, payload)
			require.NoError(t, err, name)
			assert.Empty(t, out, name)
		}

		// An unknown client is a broken hook command: it says so and
		// still exits 0.
		out, stderr, err := exploreHook(t, root, readPayload(filepath.Join(root, "a.go"), "s", "t"), "--client", "nope")
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Contains(t, stderr, `unknown hook client "nope"`)

		assert.NoFileExists(t, exploreStateFile(root))
	})

	t.Run("no index: the pins still apply and no database appears", func(t *testing.T) {
		root := writeFixture(t)
		gitify(t, root)
		verifyWrite(t, root, ".seamark/lessons.yaml",
			"pin:\n  - {rule: keep-main-small, region: a.go, note: Move the work into helpers.}\n")

		out, _, err := exploreHook(t, root, readPayload(filepath.Join(root, "a.go"), "s", "t"))
		require.NoError(t, err)

		_, text := exploreReply(t, out)
		assert.Contains(t, text, "keep-main-small")
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "index.db"))
	})

	t.Run("an invalid lessons.yaml falls back to the defaults", func(t *testing.T) {
		root := hookRepo(t)
		verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")
		recordEvaluationsOn(t)

		out, _, err := exploreHook(t, root, readPayload(filepath.Join(root, "a.go"), "s", "t"))
		require.NoError(t, err)

		_, text := exploreReply(t, out)
		assert.Contains(t, text, "RUF001", "the mined lesson still applies")

		firings, err := reviews.ReadFirings(root)
		require.NoError(t, err)
		require.Len(t, firings, 1)
		assert.Equal(t, "invalid", firings[0].GuidanceStatus)
		assert.Empty(t, firings[0].GuidanceSHA)
	})

	t.Run("an unreadable state emits every time and stays as it is", func(t *testing.T) {
		root := hookRepo(t)
		newer := `{"version":2,"contexts":{}}` + "\n"
		verifyWrite(t, root, ".seamark/lessons-explore-state.json", newer)

		for range 2 {
			out, _, err := exploreHook(t, root, readPayload(filepath.Join(root, "a.go"), "s", "t"))
			require.NoError(t, err)

			_, text := exploreReply(t, out)
			assert.Contains(t, text, "keep-main-small", "no suppression without a readable state")
		}

		raw, err := os.ReadFile(exploreStateFile(root))
		require.NoError(t, err)
		assert.Equal(t, newer, string(raw))
	})

	t.Run("a failed log append never changes the reply", func(t *testing.T) {
		root := hookRepo(t)
		require.NoError(t, os.Mkdir(reviews.AuditLogPath(root), 0o755))

		out, _, err := exploreHook(t, root, readPayload(filepath.Join(root, "a.go"), "s", "t"))
		require.NoError(t, err)

		_, text := exploreReply(t, out)
		assert.Contains(t, text, "keep-main-small")
	})

	t.Run("a failed reply write is the hook's own failure", func(t *testing.T) {
		root := hookRepo(t)
		payload := readPayload(filepath.Join(root, "a.go"), "s", "t")

		cmd := New()
		cmd.SetIn(strings.NewReader(payload))
		cmd.SetOut(failingWriter{})
		cmd.SetErr(new(strings.Builder))
		cmd.SetArgs([]string{"-C", root, "lessons", "--explore", "--hook"})

		err := cmd.Execute()
		require.Error(t, err)
		assert.NotErrorIs(t, err, gate.ErrBlocked, "exit 1, never a deny")

		firings, readErr := reviews.ReadFirings(root)
		require.NoError(t, readErr)
		assert.Empty(t, firings, "the agent never got the advice, so nothing is injected")

		// Nothing was marked delivered: the next read advises again.
		out, _, err := exploreHook(t, root, payload)
		require.NoError(t, err)

		_, text := exploreReply(t, out)
		assert.Contains(t, text, "keep-main-small")
	})
}

// TestLessonsExploreHookWritesNothingForCodex proves that a client
// without a structured read adapter writes nothing (contract L2): no
// reply, no record, no state, also with the recording switch on.
func TestLessonsExploreHookWritesNothingForCodex(t *testing.T) {
	root := hookRepo(t)
	recordEvaluationsOn(t)

	codex, ok := integration.Builtin().Lookup(integration.CodexID)
	require.True(t, ok)
	require.Nil(t, codex.Explores, "Codex has no structured read adapter")

	before := treeListing(t, root)

	for _, payload := range []string{
		readPayload(filepath.Join(root, "a.go"), "session-1", "toolu-1"),
		`{"session_id":"session-1","turn_id":"turn-1","hook_event_name":"PostToolUse","tool_name":"shell",` +
			`"tool_input":{"command":"cat a.go"}}`,
		"",
	} {
		out, stderr, err := exploreHook(t, root, payload, "--client", "codex")
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Empty(t, stderr)
	}

	assert.Equal(t, before, treeListing(t, root), "the hook wrote no file")
}

// TestLessonsExploreHookNeverInvokes proves AC-1 for the exploration
// hook: an advice, a suppressed repeat, a failed read, a read without a
// match, and a read with the recording switch on run without an agent,
// a model CLI, or the network.
func TestLessonsExploreHookNeverInvokes(t *testing.T) {
	root := exploreRepo(t)
	sentinel := newInvokerSentinel(t, root)
	recordEvaluationsOn(t)

	file := filepath.Join(root, "a.go")

	for _, payload := range []string{
		readPayload(file, "session-1", "toolu-1"),
		readPayload(file, "session-1", "toolu-2"),
		failedReadPayload(filepath.Join(root, "new.go"), "session-1"),
		readPayload(filepath.Join(root, "go.mod"), "session-1", "toolu-3"),
		strings.Replace(readPayload(file, "session-1", "toolu-4"), `"prompt_id"`, `"agent_id":"agent-1","prompt_id"`, 1),
	} {
		_, _, err := exploreHook(t, root, payload)
		require.NoError(t, err)
	}

	sentinel.requireNoCall(t)

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	assert.Len(t, firings, 5, "one record per read with the recording switch on")
}

// TestLessonsExploreHookNeverDenies proves BRIEF rule 15 for the
// exploration hook: an enforce pin with an executable check in scope of
// the read file gives advice only. The reply has no decision field, the
// hook exits 0, and it touches no check state. A usage error exits 1.
func TestLessonsExploreHookNeverDenies(t *testing.T) {
	root := prepareRepo(t, true)
	// The working tree breaks the enforce pin, so a check would fail.
	verifyEditAsset(t, root)

	asset := filepath.Join(root, "generated", "a.txt")

	for _, payload := range []string{
		readPayload(asset, "session-1", "toolu-1"),
		failedReadPayload(filepath.Join(root, "generated", "new.txt"), "session-2"),
	} {
		out, stderr, err := exploreHook(t, root, payload)
		require.NoError(t, err, "the hook exits 0")
		assert.Empty(t, stderr)

		// exploreReply refuses any field beside the advisory context, so
		// a decision, permissionDecision, or continue field fails here.
		event, text := exploreReply(t, out)
		assert.Contains(t, []string{"PostToolUse", "PostToolUseFailure"}, event)
		assert.Contains(t, text, "frozen-assets")
		assert.NotContains(t, out, "deny")
		assert.NotContains(t, out, "block")
	}

	assert.NoDirExists(t, filepath.Join(root, ".seamark", "checks"), "no guard or check state")

	t.Run("usage errors exit 1 and never deny", func(t *testing.T) {
		for _, c := range []struct {
			args []string
			want string
		}{
			{[]string{"--explore"}, "--explore needs --hook: it reads one native read event from stdin"},
			{[]string{"--explore", "--client", "claude"}, "--explore needs --hook: it reads one native read event from stdin"},
			{[]string{"--prepare", "a.go", "--explore"}, "--prepare does not combine with --explore"},
			{[]string{"--verify", "--explore"}, "--verify does not combine with --explore"},
		} {
			stdout, _, err := runIn(t, readPayload(asset, "s", "t"), append([]string{"-C", root, "lessons"}, c.args...)...)
			require.Error(t, err, "args %v", c.args)
			assert.Equal(t, c.want, err.Error(), "args %v", c.args)
			assert.NotErrorIs(t, err, gate.ErrBlocked)
			assert.NotErrorIs(t, err, lessoncheck.ErrViolation)
			assert.Empty(t, stdout)
		}

		// The --client text grows with each hook mode (contracts §4.2
		// subset rule): the head, each mode of this milestone, the tail.
		_, _, err := runIn(t, "", "-C", root, "lessons", "--list", "--client", "claude")
		require.Error(t, err)

		text := err.Error()
		assert.True(t, strings.HasPrefix(text, "--client applies to --hook, --hook-reset, "), text)
		assert.Contains(t, text, "--explore")
		assert.True(t, strings.HasSuffix(text,
			" only; the agent that --distill runs is agent.cli in .seamark/config.yaml"), text)
	})
}

// TestLessonsExploreHookRefusesGrantForms proves the exclusivity rule of
// contracts §4.2 for --explore. The skills grant `seamark lessons --file *`
// and `seamark lessons --region *` without a prompt, so these forms must
// not reach the exploration hook (triage item P3.12-2). Each form exits 1
// before the dispatch and writes no state and no firing record. The test
// fails when --explore accepts another mode or a decision flag.
func TestLessonsExploreHookRefusesGrantForms(t *testing.T) {
	root := exploreRepo(t)
	payload := readPayload(filepath.Join(root, "a.go"), "session-1", "toolu-1")

	writes := []string{
		exploreStateFile(root),
		filepath.Join(root, ".seamark", "lessons-explore-state.lock"),
		reviews.AuditLogPath(root),
	}

	for _, path := range writes {
		require.NoFileExists(t, path, "the fixture starts without an exploration write")
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--file", "a.go", "--explore", "--hook"}, "--explore does not combine with --file"},
		{[]string{"--region", ".", "--explore", "--hook"}, "--explore does not combine with --region"},
		{[]string{"--list", "--explore", "--hook"}, "--explore does not combine with --list"},
		{[]string{"--stats", "--explore", "--hook"}, "--explore does not combine with --stats"},
		{[]string{"--explore", "--hook", "--apply", "p1"}, "--explore does not combine with --apply"},
		{[]string{"--explore", "--hook", "--hook-reset"}, "--explore does not combine with --hook-reset"},
	} {
		stdout, _, err := runIn(t, payload, append([]string{"-C", root, "lessons"}, c.args...)...)
		require.Error(t, err, "args %v", c.args)
		assert.Equal(t, c.want, err.Error(), "args %v", c.args)
		assert.Empty(t, stdout, "args %v", c.args)

		for _, path := range writes {
			assert.NoFileExists(t, path, "args %v", c.args)
		}
	}

	// The same payload without the other flag writes the state and the
	// record. So the absence checks above can fail.
	stdout, _, err := exploreHook(t, root, payload)
	require.NoError(t, err)
	assert.Contains(t, stdout, "keep-main-small")
	assert.FileExists(t, exploreStateFile(root))
	assert.FileExists(t, reviews.AuditLogPath(root))
}
