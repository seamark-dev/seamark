package integration

// Tests of the ID pass of scripts/native-record-sanitize.py. They run the
// sanitizer with python3 on a synthetic Claude Code recording, so they
// need no model and no client CLI. The fixture holds each ID kind of
// Claude Code 2.1.314 in each place where a recording holds it: hook
// payloads, a session transcript with a compaction boundary and a
// task-notification prompt, subagent files, a subagent meta file, and one
// extra. The real IDs of the fixture are made up, but they have the
// shapes of real IDs.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The made-up real IDs of the sanitizer fixture. The agent values sort in
// the reverse order of their numbers, so a number that follows the sort
// order of the real IDs fails the test.
const (
	sanitizeSession    = "5d1c0e2a-7b3f-4c9d-8e1f-2a3b4c5d6e7f"
	sanitizeSession2   = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	sanitizePrompt     = "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	sanitizeAgentHook  = "af1e2d3c4b5a69788"
	sanitizeAgentLine  = "a0123456789abcdef"
	sanitizeAgentFile  = "a00ff00ff00ff00ff"
	sanitizeToolWrite  = "toolu_01AbCdEfGhIjKlMnOpQrStUv"
	sanitizeToolBash   = "toolu_01QwErTyUiOpAsDfGhJkLzXc"
	sanitizeToolAgent  = "toolu_01ZyXwVuTsRqPoNmLkJiHgFe"
	sanitizeMessage    = "msg_01AbCdEfGhIjKlMnOpQrStUv"
	sanitizeMessage2   = "msg_01ZyXwVuTsRqPoNmLkJiHgFe"
	sanitizeRequest    = "req_01AbCdEfGhIjKlMnOpQrStUv"
	sanitizeRequest2   = "req_01ZyXwVuTsRqPoNmLkJiHgFe"
	sanitizeDroppedReq = "req_01DroppedDroppedDroppedX"
	sanitizeRun        = "k3j9x0abc-q8w7e6r5"
	sanitizeTask       = "bk3x9q2zw"
	sanitizeRecords    = 18
)

// sanitizeRecord returns the made-up record UUID number i. The values
// fall as i grows, so a number that follows their sort order fails.
func sanitizeRecord(i int) string {
	return fmt.Sprintf("%08x-7a6b-4c5d-9e8f-%012x", 0xf0000000-i*0x01000000, 0xabcdef000000+i)
}

// The hook payloads of the fixture, in time order. The Stop payload
// lists a background shell task and the subagent.
var sanitizeHooks = []struct{ event, payload string }{
	{"UserPromptSubmit", `{"session_id":"{S1}","prompt_id":"{P1}","transcript_path":"{HOME}/.claude/projects/{SLUG}/{S1}.jsonl",` +
		`"cwd":"{REPO}","hook_event_name":"UserPromptSubmit","prompt":"create the file"}`},
	{"PreToolUse", `{"session_id":"{S1}","prompt_id":"{P1}","transcript_path":"{HOME}/.claude/projects/{SLUG}/{S1}.jsonl",` +
		`"cwd":"{REPO}","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"{REPO}/notes/a.txt",` +
		`"content":"a"},"tool_use_id":"{T1}"}`},
	{"PostToolUse", `{"session_id":"{S1}","prompt_id":"{P1}","hook_event_name":"PostToolUse","tool_name":"Bash",` +
		`"tool_use_id":"{T3}","tool_response":{"backgroundTaskId":"{B1}",` +
		`"outputFile":"/private/tmp/claude-501/{SLUG}/{S1}/tasks/{B1}.output"}}`},
	{"SubagentStart", `{"session_id":"{S1}","prompt_id":"{P1}","hook_event_name":"SubagentStart","agent_id":"{A1}",` +
		`"agent_type":"writer"}`},
	{"Stop", `{"session_id":"{S1}","hook_event_name":"Stop","background_tasks":[{"id":"{B1}","type":"local_bash"},` +
		`{"id":"{A1}","type":"subagent"}]}`},
}

// The lines of the main session transcript. The first record copies a
// model request, so the sanitizer drops its request ID, and that ID takes
// no number. Line 8 has a UUID toolUseID. Line 10 is a compaction
// boundary, and line 11 has a submitUuid that names no record.
var sanitizeTranscript = []string{
	`{"type":"api-request","sessionId":"{S1}","requestId":"{R0}","timestamp":"2026-11-07T10:00:00.000Z","body":"system prompt"}`,
	`{"parentUuid":null,"uuid":"{U1}","sessionId":"{S1}","promptId":"{P1}","type":"user",` +
		`"timestamp":"2026-11-07T10:00:01.000Z","message":{"role":"user","content":"create the file"}}`,
	`{"parentUuid":"{U1}","uuid":"{U2}","sessionId":"{S1}","requestId":"{R1}","type":"assistant",` +
		`"message":{"id":"{M1}","role":"assistant","content":[{"type":"tool_use","id":"{T1}","name":"Write",` +
		`"input":{"file_path":"{REPO}/notes/a.txt","content":"a"}}]},"wireToolInputs":{"{T1}":{"file_path":"{REPO}/notes/a.txt"}}}`,
	`{"parentUuid":"{U2}","uuid":"{U3}","sessionId":"{S1}","type":"user","sourceToolAssistantUUID":"{U2}",` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"{T1}","content":"File created"}]}}`,
	`{"parentUuid":"{U3}","uuid":"{U4}","sessionId":"{S1}","requestId":"{R2}","type":"assistant",` +
		`"message":{"id":"{M2}","role":"assistant","content":[{"type":"tool_use","id":"{T2}","name":"Agent",` +
		`"input":{"subagent_type":"writer","prompt":"create the child file"}}]}}`,
	`{"parentUuid":"{U4}","uuid":"{U5}","sessionId":"{S1}","type":"user","toolUseResult":{"status":"completed","agentId":"{A1}"},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"{T2}","content":[{"type":"text","text":"done\nagentId: {A1}"}]}]}}`,
	`{"parentUuid":"{U5}","uuid":"{U6}","sessionId":"{S1}","type":"user","toolUseResult":{"status":"async_launched","agentId":"{A2}"},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"{T2}","content":"Async agent launched. agentId: {A2}"}]}}`,
	`{"parentUuid":"{U6}","uuid":"{U7}","sessionId":"{S1}","type":"user","origin":{"kind":"task-notification",` +
		`"producer":"session-task","runId":"{RUN}"},"message":{"role":"user","content":"<task-notification>\n<task-id>{B1}</task-id>\n` +
		`<tool-use-id>{T3}</tool-use-id>\n<output-file>/private/tmp/claude-501/{SLUG}/{S1}/tasks/{B1}.output</output-file>\n` +
		`<status>completed</status>\n</task-notification>"}}`,
	`{"parentUuid":"{U7}","uuid":"{U8}","sessionId":"{S1}","type":"system","subtype":"informational","toolUseID":"{U9}"}`,
	`{"parentUuid":"{U8}","uuid":"{U10}","sessionId":"{S1}","type":"attachment","toolUseID":"{T1}",` +
		`"attachment":{"type":"hook_success","content":"generated context","toolUseID":"{T1}"}}`,
	`{"parentUuid":null,"logicalParentUuid":"{U10}","uuid":"{U11}","sessionId":"{S1}","type":"system","subtype":"compact_boundary",` +
		`"compactMetadata":{"trigger":"manual","preservedSegment":{"headUuid":"{U12}","anchorUuid":"{U11}","tailUuid":"{U10}"}}}`,
	`{"parentUuid":"{U11}","uuid":"{U13}","sessionId":"{S1}","type":"system","subtype":"local_command","submitUuid":"{U14}",` +
		`"content":"Unknown command"}`,
}

// sanitizeFixture is the scratch layout of one sanitizer fixture.
type sanitizeFixture struct {
	root, record, out, idMap string
}

// sanitizeReplacer fills the tokens of the fixture text.
func sanitizeReplacer(root string) *strings.Replacer {
	repo := filepath.Join(root, "repo")

	pairs := []string{
		"{S1}", sanitizeSession, "{S2}", sanitizeSession2, "{P1}", sanitizePrompt,
		"{A1}", sanitizeAgentHook, "{A2}", sanitizeAgentLine, "{A3}", sanitizeAgentFile,
		"{T1}", sanitizeToolWrite, "{T2}", sanitizeToolAgent, "{T3}", sanitizeToolBash,
		"{M1}", sanitizeMessage, "{M2}", sanitizeMessage2,
		"{R0}", sanitizeDroppedReq, "{R1}", sanitizeRequest, "{R2}", sanitizeRequest2,
		"{RUN}", sanitizeRun, "{B1}", sanitizeTask,
		"{REPO}", repo, "{HOME}", filepath.Join(root, "operator-q7"), "{SLUG}", claudeProjectSlug(repo),
	}

	for i := 1; i <= sanitizeRecords; i++ {
		pairs = append(pairs, fmt.Sprintf("{U%d}", i), sanitizeRecord(i))
	}

	return strings.NewReplacer(pairs...)
}

// claudeProjectSlug returns the project directory name that Claude Code
// derives from a path, as the sanitizer does.
func claudeProjectSlug(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}

		return '-'
	}, p)
}

// writeLines writes lines into a new file, one per line.
func writeLines(t *testing.T, file string, lines ...string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// newSanitizeFixture writes the synthetic Claude Code recording. The
// home directory has a name of its own, because the sanitizer denies the
// home name as a user name.
func newSanitizeFixture(t *testing.T) sanitizeFixture {
	t.Helper()

	root := t.TempDir()
	fill := sanitizeReplacer(root).Replace
	home := filepath.Join(root, "operator-q7")
	project := filepath.Join(home, ".claude", "projects", claudeProjectSlug(filepath.Join(root, "repo")))
	observed := filepath.Join(root, "observed")

	for i, h := range sanitizeHooks {
		name := fmt.Sprintf("p%d", i+1)
		writeLines(t, filepath.Join(observed, name+".json"), fill(h.payload))

		meta := fmt.Sprintf(`{"event":%q,"start_ns":%d,"process_cwd":%q}`, h.event, 1000+i, filepath.Join(root, "repo"))
		writeLines(t, filepath.Join(observed, name+".meta.json"), meta)
	}

	main := filepath.Join(project, sanitizeSession+".jsonl")
	sub := filepath.Join(project, sanitizeSession, "subagents")
	files := map[string][]string{
		main: sanitizeTranscript,
		filepath.Join(sub, "agent-"+sanitizeAgentHook+".jsonl"): {`{"parentUuid":null,"uuid":"{U15}","isSidechain":true,` +
			`"agentId":"{A1}","sessionId":"{S1}","type":"user","timestamp":"2026-11-07T10:00:05.000Z","message":{"role":"user","content":"go"}}`},
		filepath.Join(sub, "agent-"+sanitizeAgentHook+".meta.json"): {`{"agentType":"writer","toolUseId":"{T2}"}`},
		filepath.Join(sub, "agent-"+sanitizeAgentLine+".jsonl"): {`{"uuid":"{U16}","agentId":"{A2}","sessionId":"{S1}",` +
			`"type":"user","timestamp":"2026-11-07T10:00:09.000Z","message":{"role":"user","content":"go"}}`},
		filepath.Join(sub, "agent-"+sanitizeAgentFile+".jsonl"): {`{"uuid":"{U17}","agentId":"{A3}","sessionId":"{S1}",` +
			`"type":"user","timestamp":"2026-11-07T10:00:00.500Z","message":{"role":"user","content":"go"}}`},
		filepath.Join(project, sanitizeSession2+".jsonl"): {`{"uuid":"{U18}","sessionId":"{S2}","type":"user",` +
			`"timestamp":"2026-11-07T11:00:00.000Z","message":{"role":"user","content":"another session"}}`},
	}

	var transcripts []string

	for file, lines := range files {
		filled := make([]string, 0, len(lines))
		for _, line := range lines {
			filled = append(filled, fill(line))
		}

		writeLines(t, file, filled...)
		transcripts = append(transcripts, file)
	}

	// The script sorts the transcripts by their real paths.
	slices.Sort(transcripts)

	extra := filepath.Join(root, "extras", "guard-context.jsonl")
	writeLines(t, extra, fill(`{"parentUuid":"{U2}","uuid":"{U3}","sessionId":"{S1}","type":"attachment",`+
		`"attachment":{"type":"hook_success","toolUseID":"{T1}","content":"seamark post-edit lesson checks after this Write call"}}`))

	record := map[string]any{
		"client": ClaudeID, "client_version": "2.1.314", "repo": filepath.Join(root, "repo"), "home": home,
		"seamark": filepath.Join(root, "bin", "seamark"), "instrumentation": filepath.Join(root, "repo", ".native-instrumentation"),
		"observed": observed, "runs": []int{1000}, "transcripts": transcripts,
		"extras": map[string]string{"guard-context.jsonl": extra},
	}

	return writeSanitizeRecord(t, root, record)
}

// writeSanitizeRecord writes record.json into root and returns the layout.
func writeSanitizeRecord(t *testing.T, root string, record map[string]any) sanitizeFixture {
	t.Helper()

	data, err := json.Marshal(record)
	require.NoError(t, err)

	f := sanitizeFixture{
		root: root, record: filepath.Join(root, "record.json"),
		out: filepath.Join(root, "out"), idMap: filepath.Join(root, "id-map.json"),
	}
	require.NoError(t, os.WriteFile(f.record, data, 0o600))

	return f
}

// sanitize runs the sanitizer on the fixture into out, with the ID map.
// It returns the combined output and the exit error.
func (f sanitizeFixture) sanitize(t *testing.T, out string) (string, error) {
	t.Helper()

	script := filepath.Join("..", "..", "scripts", "native-record-sanitize.py")
	got, err := exec.Command("python3", "-I", script, "--id-map", f.idMap, f.record, out).CombinedOutput()

	return string(got), err
}

// outputFiles returns the relative name and the content of each file
// below dir.
func outputFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	files := map[string]string{}

	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)

		return err
	}))

	return files
}

// sanitizeReals returns every made-up real ID of the fixture.
func sanitizeReals() []string {
	reals := []string{
		sanitizeSession, sanitizeSession2, sanitizePrompt, sanitizeAgentHook, sanitizeAgentLine, sanitizeAgentFile,
		sanitizeToolWrite, sanitizeToolBash, sanitizeToolAgent, sanitizeMessage, sanitizeMessage2,
		sanitizeRequest, sanitizeRequest2, sanitizeDroppedReq, sanitizeRun, sanitizeTask,
	}

	for i := 1; i <= sanitizeRecords; i++ {
		reals = append(reals, sanitizeRecord(i))
	}

	return reals
}

// idMapEntry is one entry of the ID map file.
type idMapEntry struct {
	Kind      string `json:"kind"`
	Real      string `json:"real"`
	Synthetic string `json:"synthetic"`
}

// readIDMap decodes the ID map file into kind and synthetic value by real ID.
func readIDMap(t *testing.T, file string) map[string]idMapEntry {
	t.Helper()

	data, err := os.ReadFile(file)
	require.NoError(t, err)

	var doc struct {
		IDs []idMapEntry `json:"ids"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	out := map[string]idMapEntry{}
	for _, e := range doc.IDs {
		require.NotContains(t, out, e.Real, "one entry per real ID")
		out[e.Real] = e
	}

	return out
}

// readJSON decodes one output file.
func readJSON(t *testing.T, file string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(file)
	require.NoError(t, err)

	var v map[string]any
	require.NoError(t, json.Unmarshal(data, &v), file)

	return v
}

// sanitizeExpected gives each real ID of the fixture its kind and its
// synthetic value. The numbers follow the first use: the hook payloads in
// time order, then the session of run 1 with its main transcript and its
// subagent files, then the session without a hook payload. The agent of
// the parent transcript line comes before the agent of the earliest
// subagent file. The record numbers follow the record UUIDs in that
// order. The dropped request ID takes no number.
func sanitizeExpected() map[string]idMapEntry {
	want := map[string]idMapEntry{}

	add := func(kind, real, synthetic string) {
		want[real] = idMapEntry{Kind: kind, Real: real, Synthetic: synthetic}
	}

	add("session", sanitizeSession, "10000000-0000-4000-8000-000000000001")
	add("session", sanitizeSession2, "10000000-0000-4000-8000-000000000002")
	add("prompt", sanitizePrompt, "20000000-0000-4000-8000-000000000001")
	add("agent", sanitizeAgentHook, "a0000000000000001")
	add("agent", sanitizeAgentLine, "a0000000000000002")
	add("agent", sanitizeAgentFile, "a0000000000000003")
	add("tool_use", sanitizeToolWrite, "toolu_01NativeToolUse000000001")
	add("tool_use", sanitizeToolBash, "toolu_01NativeToolUse000000002")
	add("tool_use", sanitizeToolAgent, "toolu_01NativeToolUse000000003")
	add("message", sanitizeMessage, "msg_01NativeMessage000000001")
	add("message", sanitizeMessage2, "msg_01NativeMessage000000002")
	add("request", sanitizeRequest, "req_01NativeRequest000000001")
	add("request", sanitizeRequest2, "req_01NativeRequest000000002")
	add("run", sanitizeRun, "native-run-0000001")
	add("task", sanitizeTask, "b00000001")

	for i := 1; i <= sanitizeRecords; i++ {
		add("record", sanitizeRecord(i), fmt.Sprintf("30000000-0000-4000-8000-%012d", i))
	}

	return want
}

// TestNativeRecordSanitizeReplacesClaudeIDs proves the ID pass on the
// synthetic Claude Code recording: no real ID remains, each real ID has
// one synthetic value numbered by first use, the value is the same in
// every place, the timing join holds, and a second run gives the same
// bytes.
func TestNativeRecordSanitizeReplacesClaudeIDs(t *testing.T) {
	requireTools(t, "python3")

	f := newSanitizeFixture(t)

	got, err := f.sanitize(t, f.out)
	require.NoError(t, err, got)

	files := outputFiles(t, f.out)
	want := sanitizeExpected()

	t.Run("no real ID remains in a file or a file name", func(t *testing.T) {
		for name, data := range files {
			for _, real := range sanitizeReals() {
				assert.NotContains(t, name, real)
				assert.NotContains(t, data, real, name)
			}
		}
	})

	t.Run("each real ID has one synthetic value, numbered by first use", func(t *testing.T) {
		got := readIDMap(t, f.idMap)
		delete(want, sanitizeDroppedReq)
		assert.Equal(t, want, got)
		assert.NotContains(t, got, sanitizeDroppedReq, "a value that the sanitizer drops takes no number")

		manifest := readJSON(t, filepath.Join(f.out, "manifest.json"))
		assert.Equal(t, map[string]any{
			"session": 2.0, "prompt": 1.0, "agent": 3.0, "tool_use": 3.0, "record": 18.0,
			"message": 2.0, "request": 2.0, "run": 1.0, "task": 1.0,
		}, manifest["synthetic_ids"])
	})

	session := want[sanitizeSession].Synthetic
	project := "projects/-workspace-native/"

	t.Run("one synthetic value in every place", func(t *testing.T) {
		pre := readJSON(t, filepath.Join(f.out, "hooks", "002-PreToolUse.json"))
		assert.Equal(t, want[sanitizeToolWrite].Synthetic, pre["tool_use_id"], "the payload field")
		assert.Equal(t, session, pre["session_id"])
		assert.Equal(t, "/home/native/.claude/"+project+session+".jsonl", pre["transcript_path"], "the transcript path")

		post := readJSON(t, filepath.Join(f.out, "hooks", "003-PostToolUse.json"))
		response, _ := post["tool_response"].(map[string]any)
		assert.Equal(t, "b00000001", response["backgroundTaskId"])
		assert.Equal(t, "/tmp/claude-tasks/-workspace-native/"+session+"/tasks/b00000001.output", response["outputFile"])

		stop := readJSON(t, filepath.Join(f.out, "hooks", "005-Stop.json"))
		assert.Equal(t, []any{
			map[string]any{"id": "b00000001", "type": "local_bash"},
			map[string]any{"id": "a0000000000000001", "type": "subagent"},
		}, stop["background_tasks"])

		transcript := files[project+session+".jsonl"]
		assert.Contains(t, transcript, "<task-id>b00000001</task-id>", "the task-notification tags")
		assert.Contains(t, transcript, "<tool-use-id>"+want[sanitizeToolBash].Synthetic+"</tool-use-id>")
		assert.Contains(t, transcript, "<output-file>/tmp/claude-tasks/-workspace-native/"+session+"/tasks/b00000001.output")
		assert.Contains(t, transcript, `"wireToolInputs":{"`+want[sanitizeToolWrite].Synthetic+`":`, "the object key")
		assert.Contains(t, transcript, `"runId":"native-run-0000001"`)
		assert.Contains(t, transcript, `"submitUuid":"`+want[sanitizeRecord(14)].Synthetic+`"`)
		assert.Contains(t, transcript, `"headUuid":"`+want[sanitizeRecord(12)].Synthetic+`"`)
		assert.Contains(t, transcript, "agentId: a0000000000000002", "the Agent tool result text")

		sub := project + session + "/subagents/agent-"
		for _, name := range []string{"a0000000000000001.jsonl", "a0000000000000001.meta.json",
			"a0000000000000002.jsonl", "a0000000000000003.jsonl"} {
			assert.Contains(t, files, sub+name, "the subagent file name")
		}

		assert.Equal(t, want[sanitizeToolAgent].Synthetic,
			readJSON(t, filepath.Join(f.out, filepath.FromSlash(sub+"a0000000000000001.meta.json")))["toolUseId"],
			"the meta toolUseId")

		extra := files["guard-context.jsonl"]
		assert.Contains(t, extra, `"toolUseID":"`+want[sanitizeToolWrite].Synthetic+`"`, "the extra")
		assert.Contains(t, extra, `"sessionId":"`+session+`"`)

		manifest := readJSON(t, filepath.Join(f.out, "manifest.json"))
		transcripts, _ := manifest["transcripts"].([]any)
		assert.Equal(t, []any{
			project + session + ".jsonl",
			project + session + "/subagents/agent-a0000000000000001.jsonl",
			project + session + "/subagents/agent-a0000000000000001.meta.json",
			project + session + "/subagents/agent-a0000000000000002.jsonl",
			project + session + "/subagents/agent-a0000000000000003.jsonl",
			project + want[sanitizeSession2].Synthetic + ".jsonl",
		}, transcripts, "the manifest lists the synthetic paths, sorted")
	})

	t.Run("the timing join holds", func(t *testing.T) {
		r := timingRecording{dir: f.out}
		require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &r.manifest))

		pre := readJSON(t, filepath.Join(f.out, "hooks", "002-PreToolUse.json"))
		calls := claudeTimingCalls(t, r.transcript(t, nativeString(pre, "transcript_path")))
		assert.Equal(t, want[sanitizeMessage].Synthetic, calls[nativeString(pre, "tool_use_id")],
			"the payload tool_use_id names a tool_use block of the transcript that transcript_path names")
	})

	t.Run("a second run gives the same bytes", func(t *testing.T) {
		first, err := os.ReadFile(f.idMap)
		require.NoError(t, err)

		again := filepath.Join(f.root, "again")
		got, err := f.sanitize(t, again)
		require.NoError(t, err, got)

		assert.Equal(t, files, outputFiles(t, again))

		second, err := os.ReadFile(f.idMap)
		require.NoError(t, err)
		assert.Equal(t, string(first), string(second))
	})

	t.Run("the map must lie outside the copy", func(t *testing.T) {
		script := filepath.Join("..", "..", "scripts", "native-record-sanitize.py")
		inside := filepath.Join(f.root, "inside")

		got, err := exec.Command("python3", "-I", script, "--id-map", filepath.Join(inside, "id-map.json"),
			f.record, inside).CombinedOutput()
		require.Error(t, err)
		assert.Contains(t, string(got), "the ID map must lie outside")
		assert.NoDirExists(t, inside, "the sanitizer writes nothing")
	})
}

// TestNativeRecordSanitizeRefusesAnUnmappedID proves the residual check.
// A UUID that no place of the map names refuses the copy. The message
// names the file and the kind, but never the value, also when the value
// is part of a file name.
func TestNativeRecordSanitizeRefusesAnUnmappedID(t *testing.T) {
	requireTools(t, "python3")

	const unmapped = "4f0c9e8d-7b6a-4d5c-8b4a-3f2e1d0c9b8a"

	t.Run("a UUID inside a longer string", func(t *testing.T) {
		f := newSanitizeFixture(t)
		payload := filepath.Join(f.root, "observed", "p1.json")
		writeLines(t, payload, sanitizeReplacer(f.root).Replace(
			`{"session_id":"{S1}","prompt_id":"{P1}","hook_event_name":"UserPromptSubmit","prompt":"see run `+unmapped+` again"}`))

		got, err := f.sanitize(t, f.out)
		require.Error(t, err)
		assert.Contains(t, got, "hooks/001-UserPromptSubmit.json: holds an unmapped UUID ID")
		assert.NotContains(t, got, unmapped)
	})

	t.Run("a UUID only in an output file name", func(t *testing.T) {
		f := newSanitizeFixture(t)

		var record map[string]any
		data, err := os.ReadFile(f.record)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &record))

		extra := filepath.Join(f.root, "extras", "notes.jsonl")
		writeLines(t, extra, `{"note":"no ID here"}`)
		record["extras"] = map[string]string{"notes-" + unmapped + ".jsonl": extra}
		f = writeSanitizeRecord(t, f.root, record)

		got, err := f.sanitize(t, f.out)
		require.Error(t, err)
		assert.Contains(t, got, "notes-<id>.jsonl: holds an unmapped UUID ID")
		assert.NotContains(t, got, unmapped)
	})
}

// TestNativeRecordPassesTheIDMapForClaudeOnly proves that the record step
// asks for the ID map of a Claude Code recording only. The map lies
// beside the raw recording, outside the sanitized copy, because it holds
// the real IDs.
func TestNativeRecordPassesTheIDMapForClaudeOnly(t *testing.T) {
	// The record step holds a heredoc with a closing brace at the line
	// start, so scriptFunction cannot cut record_mode, and the test reads
	// the whole script.
	script := nativeRecordScript(t)

	assert.Equal(t, 1, strings.Count(script, "\n    id_map=\n    [ \"$CLIENT\" != claude ] || id_map=$OUT/id-map.json\n"))
	assert.Equal(t, 1, strings.Count(script,
		` "$SCRIPTS/native-record-sanitize.py" ${id_map:+--id-map "$id_map"} "$OUT/record.json" "$OUT/sanitized"`))
	assert.Equal(t, 1, strings.Count(script, "native-record-sanitize.py"), "the script runs the sanitizer once")
}

// TestNativeRecordSanitizeKeepsCodexIDs proves that the ID pass skips a
// Codex recording. A Codex thread ID is a UUID. It stays in every file,
// no map file exists, the manifest has no count key, and the transcripts
// keep the order of record.json.
func TestNativeRecordSanitizeKeepsCodexIDs(t *testing.T) {
	requireTools(t, "python3")

	const thread = "019a0b1c-2d3e-7f40-8a5b-6c7d8e9f0a1b"

	root := t.TempDir()
	home := filepath.Join(root, "operator-q7")
	repo := filepath.Join(root, "repo")
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "11", "07")
	earlier := filepath.Join(sessions, "rollout-2026-11-07T10-00-00-"+thread+".jsonl")
	later := filepath.Join(sessions, "rollout-2026-11-07T11-00-00-"+thread+"-b.jsonl")
	observed := filepath.Join(root, "observed")

	writeLines(t, earlier, `{"type":"session_meta","payload":{"id":"`+thread+`","cwd":"`+repo+`"}}`)
	writeLines(t, later, `{"type":"session_meta","payload":{"id":"`+thread+`"}}`)
	writeLines(t, filepath.Join(observed, "p1.json"), `{"session_id":"`+thread+`","turn_id":"`+thread+`","hook_event_name":"Stop"}`)
	writeLines(t, filepath.Join(observed, "p1.meta.json"), `{"event":"Stop","start_ns":1000,"process_cwd":"`+repo+`"}`)

	f := writeSanitizeRecord(t, root, map[string]any{
		"client": CodexID, "client_version": "0.164.0", "repo": repo, "home": home,
		"seamark": filepath.Join(root, "bin", "seamark"), "instrumentation": filepath.Join(repo, ".native-instrumentation"),
		"observed": observed, "runs": []int{1000}, "transcripts": []string{later, earlier}, "extras": map[string]string{},
	})

	got, err := f.sanitize(t, f.out)
	require.NoError(t, err, got)

	for name, data := range outputFiles(t, f.out) {
		if name != "manifest.json" {
			assert.Contains(t, data, thread, "%s keeps the thread ID", name)
		}
	}

	assert.NoFileExists(t, f.idMap, "a Codex run writes no map file")

	manifest := readJSON(t, filepath.Join(f.out, "manifest.json"))
	assert.NotContains(t, manifest, "synthetic_ids")
	assert.Equal(t, []any{
		"sessions/2026/11/07/rollout-2026-11-07T11-00-00-" + thread + "-b.jsonl",
		"sessions/2026/11/07/rollout-2026-11-07T10-00-00-" + thread + ".jsonl",
	}, manifest["transcripts"], "the transcripts keep the order of record.json")
}
