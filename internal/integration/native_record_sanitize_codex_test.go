package integration

// Tests of the Codex pass of scripts/native-record-sanitize.py. They run
// the sanitizer with python3 on a synthetic codex-cli 0.169.0 recording,
// so they need no model and no client CLI. The fixture holds each Codex
// ID kind in the places where a recording holds it: hook payloads, a root
// rollout and a worker rollout, and the extras. Its rollout file names
// hold a local wall time of CET, as Codex writes them. The real IDs and
// times of the fixture are made up, but they have the shapes of real
// ones.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The made-up real IDs of the Codex fixture. Threads and turns have the
// UUIDv7 shape, and a window has the UUIDv4 shape, as in codex-cli
// 0.169.0. The tokens have the lengths of real tokens.
const (
	codexRoot       = "019a7c10-1111-7a1b-8c2d-3e4f5a6b7c8d"
	codexWorker     = "019a7c10-2222-7b2c-9d3e-4f5a6b7c8d9e"
	codexTurnRoot   = "019a7c10-3333-7c3d-ae4f-5a6b7c8d9eaf"
	codexTurnWorker = "019a7c10-4444-7d4e-bf5a-6b7c8d9eafb0"
	codexWindowRoot = "6f1e2d3c-4b5a-4968-8776-655443322110"
	codexWindowWork = "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	codexExecSpawn  = "3c2b1a09-8f7e-4d6c-9b5a-493827161504"
	codexExecPatch  = "4d3c2b1a-0f9e-4e7d-8c6b-5a4938271615"
	codexExecWait   = "5e4d3c2b-1a0f-4f8e-9d7c-6b5a49382716"
	codexTools      = "019a7c10-5555-7e5f-80a1-b2c3d4e5f6a7"
	codexOutput     = "019a7c10-6666-7f60-91b2-c3d4e5f6a7b8"
	codexMessage    = "019a7c10-7777-7071-a2c3-d4e5f6a7b8c9"
	codexItem       = "019a7c10-8888-7182-b3d4-e5f6a7b8c9da"
	codexRevision   = "019a7c10-9999-7293-c4e5-f6a7b8c9daeb"
	codexMessageHex = "msg_0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f607182"
	codexCall       = "call_Zx9Yw8Vu7Ts6Rq5Po4Nm3Lk2Ji1Hg0Fe"
	codexCustomCall = "ctc_1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293"
	codexResponse   = "resp_2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4"
	codexReasoning  = "rs_3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5"
	codexProcess    = "73211"
)

// The made-up times of the Codex fixture: a Tuesday morning in November.
// Codex names each rollout by the local wall time of its thread start,
// and the fixture writes that time in CET, one hour ahead of UTC.
var (
	codexStart       = time.Date(2026, 11, 10, 9, 41, 7, 250e6, time.UTC)
	codexWorkerStart = time.Date(2026, 11, 10, 9, 41, 49, 690e6, time.UTC)
)

// codexShift is the offset that the sanitizer gives the fixture. It moves
// the first time, 09:41:07 UTC floored to the second, to 21:00:00 UTC on
// the same date.
const codexShift = 11*3600 + 18*60 + 53

// The synthetic thread values of the fixture.
const (
	codexThread1 = "10000000-0000-4000-8000-000000000001"
	codexThread2 = "10000000-0000-4000-8000-000000000002"
)

// codexRecordFixture is a synthetic Codex recording before it is written.
// A test can change its lines before write.
type codexRecordFixture struct {
	root, home, repo string
	hooks            []codexRecordHook
	rollout, worker  []string
	extras           map[string][]string
}

// codexRecordHook is one observed hook payload of the fixture.
type codexRecordHook struct {
	event, payload string
}

// codexRollout returns the real path of a rollout of the fixture below
// home: the local date directory and the local wall time, as Codex
// writes them.
func codexRollout(home string, start time.Time, thread string) string {
	local := start.In(time.FixedZone("CET", 3600))

	return filepath.Join(home, ".codex", "sessions", local.Format("2006"), local.Format("01"), local.Format("02"),
		"rollout-"+local.Format("2006-01-02T15-04-05")+"-"+thread+".jsonl")
}

// codexISO writes a time of the fixture with milliseconds, as Codex does.
func codexISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// newCodexRecordFixture returns the synthetic Codex recording. The worker
// thread first appears in the text of the spawn result. The first walk
// gives it the thread kind from a later rollout name and agent_id, so
// the second walk never counts it as an item.
func newCodexRecordFixture(t *testing.T) *codexRecordFixture {
	t.Helper()

	root := t.TempDir()
	f := &codexRecordFixture{root: root, home: filepath.Join(root, "operator-q7"), repo: filepath.Join(root, "repo")}

	rootPath := codexRollout(f.home, codexStart, codexRoot)
	workerPath := codexRollout(f.home, codexWorkerStart, codexWorker)

	fill := strings.NewReplacer(
		"{R}", codexRoot, "{W}", codexWorker, "{TR}", codexTurnRoot, "{TW}", codexTurnWorker,
		"{WR}", codexWindowRoot, "{WW}", codexWindowWork, "{E1}", codexExecSpawn, "{E2}", codexExecPatch,
		"{E3}", codexExecWait, "{AT}", codexTools, "{CTCO}", codexOutput, "{MSGU}", codexMessage,
		"{ITEM}", codexItem, "{REV}", codexRevision, "{MSGH}", codexMessageHex, "{CALL}", codexCall,
		"{CTC}", codexCustomCall, "{RESP}", codexResponse, "{RS}", codexReasoning, "{PID}", codexProcess,
		"{REPO}", f.repo, "{ROOT}", rootPath, "{WORKER}", workerPath,
		"{T4}", codexISO(codexStart.Add(4*time.Hour+30*time.Minute)), "{T0}", codexISO(codexStart), "{T1}", codexISO(codexStart.Add(614*time.Millisecond)),
		"{T2}", codexISO(codexStart.Add(2*time.Second)), "{T3}", codexISO(codexStart.Add(9*time.Second)),
		"{TW0}", codexISO(codexWorkerStart), "{TW1}", codexISO(codexWorkerStart.Add(3*time.Second)),
		"{S}", fmt.Sprint(codexStart.Unix()+1), "{S9}", fmt.Sprint(codexStart.Unix()+9),
		"{MS}", fmt.Sprint(codexStart.UnixMilli()+1058), "{MS1}", fmt.Sprint(codexStart.UnixMilli()+1059),
		"{CT}", fmt.Sprintf("%d.864644", codexStart.Unix()+2),
		"{TS}", codexStart.Add(13*time.Second).Format("2006-01-02T15:04:05")+".123456Z",
	).Replace

	lines := func(in ...string) []string {
		out := make([]string, 0, len(in))
		for _, line := range in {
			out = append(out, fill(line))
		}

		return out
	}

	common := `"transcript_path":"{ROOT}","cwd":"{REPO}","model":"gpt-5.6-luna"`
	f.hooks = []codexRecordHook{
		{"SessionStart", `{"session_id":"{R}",` + common + `,"hook_event_name":"SessionStart","source":"startup"}`},
		{"UserPromptSubmit", `{"session_id":"{R}","turn_id":"{TR}",` + common + `,"hook_event_name":"UserPromptSubmit","prompt":"go"}`},
		{"PreToolUse", `{"session_id":"{R}","turn_id":"{TR}",` + common + `,"hook_event_name":"PreToolUse",` +
			`"tool_name":"spawn_agent","tool_input":{"agent_type":"worker","message":"add a file"},"tool_use_id":"exec-{E1}"}`},
		{"PostToolUse", `{"session_id":"{R}","turn_id":"{TR}",` + common + `,"hook_event_name":"PostToolUse",` +
			`"tool_name":"spawn_agent","tool_response":"{\"agent_id\":\"{W}\",\"nickname\":\"Volta\"}","tool_use_id":"exec-{E1}"}`},
		{"SubagentStart", `{"session_id":"{R}","turn_id":"{TW}","transcript_path":"{WORKER}","cwd":"{REPO}",` +
			`"hook_event_name":"SubagentStart","agent_id":"{W}","agent_type":"worker"}`},
		{"PreToolUse", `{"session_id":"{R}","turn_id":"{TW}","agent_id":"{W}","agent_type":"worker",` +
			`"transcript_path":"{WORKER}","cwd":"{REPO}","hook_event_name":"PreToolUse","tool_name":"apply_patch",` +
			`"tool_input":{"command":"*** Begin Patch\n*** Add File: child.txt\n+child\n*** End Patch"},"tool_use_id":"exec-{E2}"}`},
		{"PreToolUse", `{"session_id":"{R}","turn_id":"{TR}",` + common + `,"hook_event_name":"PreToolUse",` +
			`"tool_name":"multi_agent_v1wait_agent","tool_input":{"targets":["{W}"],"timeout_ms":3600000},"tool_use_id":"exec-{E3}"}`},
		{"Stop", `{"session_id":"{R}","turn_id":"{TR}",` + common + `,"hook_event_name":"Stop","last_assistant_message":"done"}`},
	}

	for i := range f.hooks {
		f.hooks[i].payload = fill(f.hooks[i].payload)
	}

	f.rollout = lines(
		`{"timestamp":"{T1}","type":"session_meta","payload":{"session_id":"{R}","id":"{R}","timestamp":"{T0}",`+
			`"cwd":"{REPO}","creator_account_id":"acct-operator","base_instructions":{"text":"system"},`+
			`"cli_version":"0.169.0","context_window":{"window_id":"{WR}"}}}`,
		`{"timestamp":"{T1}","type":"response_item","payload":{"type":"additional_tools","id":"at_{AT}","role":"developer",`+
			`"tools":[{"type":"namespace","name":"mcp__cua_repl","description":"UI automation through cua_repl",`+
			`"tools":[{"type":"function","name":"js"}]}]}}`,
		`{"timestamp":"{T1}","type":"response_item","payload":{"type":"message","id":"msg_{MSGU}","role":"user",`+
			`"content":[{"type":"input_text","text":"<environment_context>\n  <cwd>{REPO}</cwd>\n  `+
			`<current_date>2026-11-10</current_date>\n  <timezone>CET</timezone>\n</environment_context>"}],`+
			`"internal_chat_message_metadata_passthrough":{"turn_id":"{TR}","create_time":{CT}}},`+
			`"metadata":{"retained_source":{"id":{"message_id":"msg_{MSGU}","turn_id":"{TR}"},"revision":"retained_{REV}"}}}`,
		`{"timestamp":"{T2}","type":"event_msg","payload":{"type":"task_started","turn_id":"{TR}","started_at":{S}}}`,
		`{"timestamp":"{T2}","type":"response_item","payload":{"type":"custom_tool_call","id":"{CTC}","call_id":"{CALL}",`+
			`"name":"exec","input":"await tools.multi_agent_v1__wait_agent({targets: [\"{W}\"]});"}}`,
		`{"timestamp":"{T2}","type":"event_msg","payload":{"type":"item_completed","thread_id":"{R}","turn_id":"{TR}",`+
			`"item":{"type":"CommandExecution","id":"exec-{E1}","process_id":"{PID}","status":"completed"},`+
			`"started_at_ms":{MS},"completed_at_ms":{MS1}}}`,
		`{"timestamp":"{T2}","type":"event_msg","payload":{"type":"item_completed","thread_id":"{R}","turn_id":"{TR}",`+
			`"item":{"type":"CollabAgentToolCall","id":"exec-{E3}","sender_thread_id":"{R}","receiver_thread_ids":["{W}"],`+
			`"agents_states":{"{W}":{"status":"completed"}}}}}`,
		`{"timestamp":"{T2}","type":"event_msg","payload":{"type":"item_completed","thread_id":"{R}","turn_id":"{TR}",`+
			`"item":{"type":"UserMessage","id":"{ITEM}"}}}`,
		`{"timestamp":"{T3}","type":"response_item","payload":{"type":"custom_tool_call_output","id":"ctco_{CTCO}",`+
			`"call_id":"{CALL}","output":[{"type":"input_text","text":"ok"}]}}`,
		`{"timestamp":"{T3}","type":"response_item","payload":{"type":"message","id":"{MSGH}","role":"assistant",`+
			`"content":[{"type":"output_text","text":"done"}]}}`,
		`{"timestamp":"{T3}","type":"response_item","payload":{"type":"reasoning","id":"{RS}","encrypted_content":"secret"}}`,
		`{"timestamp":"{T3}","type":"token_usage_record","payload":{"thread_id":"{R}","turn_id":"{TR}","session_id":"{R}",`+
			`"response_id":"{RESP}"}}`,
		`{"timestamp":"{T3}","type":"event_msg","payload":{"type":"task_complete","turn_id":"{TR}","started_at":{S},`+
			`"completed_at":{S9},"duration_ms":7842}}`,
		`{"timestamp":"{T4}","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text",`+
			`"text":"<environment_context>\n  <current_date>2026-11-10</current_date>\n</environment_context>"}]}}`,
	)

	f.worker = lines(
		`{"timestamp":"{TW0}","type":"session_meta","payload":{"session_id":"{R}","id":"{W}","parent_thread_id":"{R}",`+
			`"timestamp":"{TW0}","source":{"subagent":{"thread_spawn":{"parent_thread_id":"{R}","agent_nickname":"Volta"}}},`+
			`"context_window":{"window_id":"{WW}"}}}`,
		`{"timestamp":"{TW1}","type":"event_msg","payload":{"type":"item_completed","thread_id":"{W}","turn_id":"{TW}",`+
			`"item":{"type":"FileChange","id":"exec-{E2}"}}}`,
	)

	f.extras = map[string][]string{
		"exec-events.jsonl": lines(`{"type":"thread.started","thread_id":"{R}"}`),
		"guard-context.jsonl": lines(`{"timestamp":"{T3}","type":"response_item","payload":{"type":"custom_tool_call_output",` +
			`"id":"ctco_{CTCO}","call_id":"{CALL}","output":"blocked"}}`),
		"lesson-check-audit.jsonl": lines(`{"ts":"{TS}","surface":"hook","session_sha256":"` + strings.Repeat("ab", 32) + `"}`),
	}

	return f
}

// write writes the recording and its record.json. The transcripts come
// in the order of their real paths, as the record step sorts them.
func (f *codexRecordFixture) write(t *testing.T) sanitizeFixture {
	t.Helper()

	observed := filepath.Join(f.root, "observed")

	for i, h := range f.hooks {
		name := fmt.Sprintf("p%d", i+1)
		writeLines(t, filepath.Join(observed, name+".json"), h.payload)
		writeLines(t, filepath.Join(observed, name+".meta.json"),
			fmt.Sprintf(`{"event":%q,"start_ns":%d,"process_cwd":%q}`, h.event, 1000+i, f.repo))
	}

	rootPath := codexRollout(f.home, codexStart, codexRoot)
	workerPath := codexRollout(f.home, codexWorkerStart, codexWorker)
	writeLines(t, rootPath, f.rollout...)
	writeLines(t, workerPath, f.worker...)

	extras := map[string]string{}
	for name, lines := range f.extras {
		extras[name] = filepath.Join(f.root, "extras", name)
		writeLines(t, extras[name], lines...)
	}

	return writeSanitizeRecord(t, f.root, map[string]any{
		"client": CodexID, "client_version": "0.169.0", "repo": f.repo, "home": f.home,
		"seamark": filepath.Join(f.root, "bin", "seamark"), "instrumentation": filepath.Join(f.repo, ".native-instrumentation"),
		"observed": observed, "runs": []int{1000}, "transcripts": []string{rootPath, workerPath}, "extras": extras,
	})
}

// codexReals returns every made-up real ID of the fixture, also the UUID
// inside each prefixed token.
func codexReals() []string {
	return []string{
		codexRoot, codexWorker, codexTurnRoot, codexTurnWorker, codexWindowRoot, codexWindowWork,
		codexExecSpawn, codexExecPatch, codexExecWait, codexTools, codexOutput, codexMessage, codexItem,
		codexRevision, codexMessageHex, codexCall, codexCustomCall, codexResponse, codexReasoning,
	}
}

// codexExpected gives each real ID of the fixture its kind and its
// synthetic value. The numbers follow the first use: the hook payloads in
// time order, then the root rollout, then the worker rollout, then the
// extras. Member names, rollout names, and prefixes come first; every
// other UUID is an item, numbered in the second walk.
func codexExpected() []idMapEntry {
	pad := func(prefix, real string, n int) string {
		return prefix + fmt.Sprintf("%0*d", len(real)-len(prefix), n)
	}

	return []idMapEntry{
		{"thread", codexRoot, codexThread1},
		{"thread", codexWorker, codexThread2},
		{"turn", codexTurnRoot, "20000000-0000-4000-8000-000000000001"},
		{"turn", codexTurnWorker, "20000000-0000-4000-8000-000000000002"},
		{"item", codexRevision, "30000000-0000-4000-8000-000000000001"},
		{"item", codexItem, "30000000-0000-4000-8000-000000000002"},
		{"window", codexWindowRoot, "40000000-0000-4000-8000-000000000001"},
		{"window", codexWindowWork, "40000000-0000-4000-8000-000000000002"},
		{"exec", codexExecSpawn, "50000000-0000-4000-8000-000000000001"},
		{"exec", codexExecPatch, "50000000-0000-4000-8000-000000000002"},
		{"exec", codexExecWait, "50000000-0000-4000-8000-000000000003"},
		{"call", codexCall, pad("call_", codexCall, 1)},
		{"ctc", codexCustomCall, pad("ctc_", codexCustomCall, 1)},
		{"ctco", codexOutput, "70000000-0000-4000-8000-000000000001"},
		{"resp", codexResponse, pad("resp_", codexResponse, 1)},
		{"rs", codexReasoning, pad("rs_", codexReasoning, 1)},
		{"msg", codexMessage, "80000000-0000-4000-8000-000000000001"},
		{"msg", codexMessageHex, pad("msg_", codexMessageHex, 2)},
		{"at", codexTools, "60000000-0000-4000-8000-000000000001"},
		{"process", codexProcess, "90001"},
	}
}

// codexLines decodes each line of a JSONL output file. Numbers keep their
// text, so a test can compare a shifted time digit by digit.
func codexLines(t *testing.T, file string) []map[string]any {
	t.Helper()

	var out []map[string]any

	for _, line := range timingLines(t, file) {
		d := json.NewDecoder(strings.NewReader(string(line)))
		d.UseNumber()

		var v map[string]any
		require.NoError(t, d.Decode(&v), file)

		out = append(out, v)
	}

	return out
}

// codexPayload returns the payload member of a decoded rollout line.
func codexPayload(line map[string]any) map[string]any {
	p, _ := line["payload"].(map[string]any)

	return p
}

// codexNumericZone finds an ISO time with a numeric zone. The fixture
// writes every time with Z, so a numeric zone in a copy is a local
// offset that the pass let through.
var codexNumericZone = regexp.MustCompile(`T\d{2}:\d{2}:\d{2}(?:\.\d+)?[+-]\d{2}:\d{2}`)

// The output names of the fixture rollouts: the shifted UTC start of each
// thread, in a directory of its UTC date, with the synthetic thread.
const (
	codexRootCopy   = "sessions/2026/11/10/rollout-2026-11-10T21-00-00-" + codexThread1 + ".jsonl"
	codexWorkerCopy = "sessions/2026/11/10/rollout-2026-11-10T21-00-42-" + codexThread2 + ".jsonl"
)

// TestNativeRecordSanitizeReplacesCodexIDs proves the Codex pass on the
// synthetic recording: no real ID, local time, or tool list remains,
// each real ID has one synthetic value numbered by first use, the replay
// joins hold, every time moves by one offset into the night window from
// 21:00 to 08:00 UTC, and a second run gives the same bytes.
func TestNativeRecordSanitizeReplacesCodexIDs(t *testing.T) {
	requireTools(t, "python3")

	f := newCodexRecordFixture(t).write(t)

	got, err := f.sanitize(t, f.out)
	require.NoError(t, err, got)

	files := outputFiles(t, f.out)

	t.Run("no real ID, local time, home path, or tool list remains", func(t *testing.T) {
		for name, data := range files {
			for _, real := range codexReals() {
				assert.NotContains(t, data, real, "%s holds a real ID", name)
				assert.NotContains(t, name, real, "the file name %s holds a real ID", name)
			}

			for _, gone := range []string{"T10-41-", "/Users/", "<timezone>CET", "cua_repl", "acct-operator", "secret"} {
				assert.NotContains(t, data+name, gone, name)
			}

			assert.False(t, codexNumericZone.MatchString(data+name), "%s holds a time with a numeric zone", name)
		}
	})

	t.Run("each real ID has one synthetic value numbered by first use", func(t *testing.T) {
		want := map[string]idMapEntry{}
		for _, e := range codexExpected() {
			want[e.Real] = e
		}

		assert.Equal(t, want, readIDMap(t, f.idMap))

		manifest := readJSON(t, filepath.Join(f.out, "manifest.json"))
		assert.Equal(t, map[string]any{
			"thread": 2.0, "turn": 2.0, "item": 2.0, "window": 2.0, "exec": 3.0, "call": 1.0, "ctc": 1.0, "ctco": 1.0,
			"resp": 1.0, "rs": 1.0, "msg": 2.0, "at": 1.0, "process": 1.0,
		}, manifest["synthetic_ids"])
		assert.Equal(t, true, manifest["times_shifted"])
		assert.Equal(t, []any{codexRootCopy, codexWorkerCopy}, manifest["transcripts"], "sorted synthetic paths")
	})

	t.Run("the replay joins hold", func(t *testing.T) {
		require.Contains(t, files, codexRootCopy)
		require.Contains(t, files, codexWorkerCopy)

		items := func(name string) map[string]bool {
			ids := map[string]bool{}
			for _, line := range codexLines(t, filepath.Join(f.out, name)) {
				if item, ok := codexPayload(line)["item"].(map[string]any); ok {
					ids[nativeString(item, "id")] = true
				}
			}

			return ids
		}

		meta := func(name string) map[string]any {
			return codexPayload(codexLines(t, filepath.Join(f.out, name))[0])
		}

		for i := 1; i <= 8; i++ {
			matches, err := filepath.Glob(filepath.Join(f.out, "hooks", fmt.Sprintf("%03d-*.json", i)))
			require.NoError(t, err)
			require.Len(t, matches, 1)

			payload := readJSON(t, matches[0])
			transcript := strings.TrimPrefix(nativeString(payload, "transcript_path"), "/home/native/.codex/")
			require.Contains(t, files, transcript, "%s: transcript_path names a rollout of the copy", matches[0])
			assert.Equal(t, codexThread1, nativeString(payload, "session_id"), matches[0])

			if call := nativeString(payload, "tool_use_id"); call != "" {
				assert.True(t, items(transcript)[call], "%s: tool_use_id names an item of its rollout", matches[0])
			}

			if agent := nativeString(payload, "agent_id"); agent != "" {
				assert.Equal(t, codexThread2, agent)
				assert.Equal(t, agent, meta(transcript)["id"], "agent_id names the worker thread")
			}
		}

		assert.Equal(t, codexThread1, meta(codexRootCopy)["id"], "session_id names the root session_meta")

		worker := meta(codexWorkerCopy)
		assert.Equal(t, codexThread1, worker["session_id"])
		assert.Equal(t, codexThread1, worker["parent_thread_id"])
		assert.Contains(t, files["hooks/004-PostToolUse.json"], codexThread2, "the spawn result names the worker")
		assert.Contains(t, files["hooks/007-PreToolUse.json"], `"`+codexThread2+`"`, "the wait targets the worker")

		item := codexPayload(codexLines(t, filepath.Join(f.out, codexRootCopy))[5])["item"].(map[string]any)
		assert.Equal(t, "90001", item["process_id"], "the process ID keeps its string type")
	})

	t.Run("the tool list of additional_tools is masked", func(t *testing.T) {
		tools := codexPayload(codexLines(t, filepath.Join(f.out, codexRootCopy))[1])
		assert.Equal(t, map[string]any{
			"type": "additional_tools", "id": "at_60000000-0000-4000-8000-000000000001", "role": "developer",
			"tools": "<sanitized>",
		}, tools)
	})

	t.Run("every time moves by one offset into the night window", func(t *testing.T) {
		shift := func(t time.Time) time.Time { return t.Add(codexShift * time.Second) }
		rollout := codexLines(t, filepath.Join(f.out, codexRootCopy))

		assert.Equal(t, "2026-11-10T21:00:00.250Z", codexPayload(rollout[0])["timestamp"],
			"the first time lands at 21:00:00 UTC on its own date and keeps its fraction")
		assert.Equal(t, codexISO(shift(codexStart.Add(614*time.Millisecond))), rollout[0]["timestamp"])
		assert.Equal(t, json.Number(fmt.Sprint(codexStart.Unix()+1+codexShift)), codexPayload(rollout[3])["started_at"])
		assert.Equal(t, json.Number(fmt.Sprint(codexStart.UnixMilli()+1058+codexShift*1000)),
			codexPayload(rollout[5])["started_at_ms"])
		assert.Equal(t, json.Number(fmt.Sprintf("%d.864644", codexStart.Unix()+2+codexShift)),
			codexPayload(rollout[2])["internal_chat_message_metadata_passthrough"].(map[string]any)["create_time"],
			"a float time keeps its digits")
		assert.Equal(t, json.Number(fmt.Sprint(codexStart.Unix()+9+codexShift)), codexPayload(rollout[12])["completed_at"])
		assert.Equal(t, json.Number("7842"), codexPayload(rollout[12])["duration_ms"], "a duration stays")

		audit := codexLines(t, filepath.Join(f.out, "lesson-check-audit.jsonl"))
		assert.Equal(t, shift(codexStart.Add(13*time.Second)).Format("2006-01-02T15:04:05")+".123456Z", audit[0]["ts"])

		text := codexPayload(rollout[2])["content"].([]any)[0].(map[string]any)["text"]
		assert.Contains(t, text, "<current_date>2026-11-10</current_date>", "the UTC date of the shifted record time")
		assert.Contains(t, text, "<timezone>Etc/UTC</timezone>")

		// The later record lands after midnight UTC, so its tag names the next day.
		assert.Equal(t, "2026-11-11T01:30:00.250Z", rollout[13]["timestamp"])
		later := codexPayload(rollout[13])["content"].([]any)[0].(map[string]any)["text"]
		assert.Contains(t, later, "<current_date>2026-11-11</current_date>")

		worker := codexLines(t, filepath.Join(f.out, codexWorkerCopy))
		assert.Equal(t, codexISO(shift(codexWorkerStart)), codexPayload(worker[0])["timestamp"])

		hook := readJSON(t, filepath.Join(f.out, "hooks", "005-SubagentStart.json"))
		assert.Equal(t, "/home/native/.codex/"+codexWorkerCopy, hook["transcript_path"], "the transcript_path holds the UTC name")
	})

	t.Run("a second run gives the same bytes", func(t *testing.T) {
		again := filepath.Join(f.root, "again")

		got, err := f.sanitize(t, again)
		require.NoError(t, err, got)
		assert.Equal(t, files, outputFiles(t, again))
	})
}

// codexModelText returns a change of the fixture that adds a model
// message with the text to the root rollout, 9 seconds after the start.
// A test puts a value there that no member name marks.
func codexModelText(t *testing.T, text string) func(f *codexRecordFixture) {
	t.Helper()

	return func(f *codexRecordFixture) {
		line, err := json.Marshal(map[string]any{"timestamp": codexISO(codexStart.Add(9 * time.Second)),
			"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": text}}}})
		require.NoError(t, err)

		f.rollout = append(f.rollout, string(line))
	}
}

// TestNativeRecordSanitizeEndsTheCodexWindowAt0800UTC proves the end of
// the night window. The first whole second of the fixture lands at
// 21:00:00 UTC, so a time 11 hours after it lands at 08:00:00 UTC of the
// next day. The sanitizer keeps a time one millisecond before that edge.
// It refuses a time at the edge, because no single offset then keeps
// every time in the window.
func TestNativeRecordSanitizeEndsTheCodexWindowAt0800UTC(t *testing.T) {
	requireTools(t, "python3")

	edge := codexStart.Truncate(time.Second).Add(11 * time.Hour)

	t.Run("a time one millisecond before 08:00 UTC stays in the window", func(t *testing.T) {
		fixture := newCodexRecordFixture(t)
		codexModelText(t, "late "+codexISO(edge.Add(-time.Millisecond)))(fixture)
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.NoError(t, err, got)
		assert.Contains(t, outputFiles(t, f.out)[codexRootCopy], "late 2026-11-11T07:59:59.999Z")
	})

	t.Run("a time at 08:00 UTC is refused", func(t *testing.T) {
		fixture := newCodexRecordFixture(t)
		codexModelText(t, "late "+codexISO(edge))(fixture)
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.Error(t, err, got)
		assert.Contains(t, got, "the recorded times span more than the window from 21:00 to 08:00 UTC")
	})
}

// TestNativeRecordSanitizeRefusesACodexResidual proves that the Codex
// residual check refuses a copy that keeps a value the pass cannot
// replace. Each refusal names the file and the kind, never the value.
func TestNativeRecordSanitizeRefusesACodexResidual(t *testing.T) {
	requireTools(t, "python3")

	const token = "fc_9z8y7x6w5v4u3t2s1r0q"
	digits := fmt.Sprint(codexStart.Unix() + 5)

	model := func(text string) func(f *codexRecordFixture) {
		return codexModelText(t, text)
	}

	for _, tc := range []struct {
		name, want, value string
		change            func(f *codexRecordFixture)
	}{
		{"a function call ID", "holds an unmapped fc_ ID", token, model("call " + token)},
		{"epoch digits in a text", "holds epoch digits of a recorded time", digits, model("started at " + digits)},
		{"a time without a zone", "holds a timestamp without a time zone", "2026-11-10T09:41:20", model("at 2026-11-10T09:41:20")},
		{"a home path", "holds a home path", "/Users/someone", model("see /Users/someone/notes")},
		{"a file name time in a text", "holds an unshifted file name time", "2026-11-10T10-41-07",
			model("see rollout-2026-11-10T10-41-07.jsonl")},
		{"an epoch number far from the recorded times", "holds an unshifted epoch time", "", func(f *codexRecordFixture) {
			f.rollout = append(f.rollout, fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count",`+
				`"limits":{"resets_at":%d}}}`, codexISO(codexStart.Add(9*time.Second)), codexStart.Unix()+3*86400))
		}},
		{"a time far from the others", "the recorded times span more than the window", "",
			model("late " + codexISO(codexStart.Add(12*time.Hour)))},
		{"a rollout name without its session_meta time", "is not its session_meta start time in a time zone", "",
			func(f *codexRecordFixture) {
				f.rollout[0] = strings.Replace(f.rollout[0], codexISO(codexStart), "2026-11-10T09:30:00.000Z", 1)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCodexRecordFixture(t)
			tc.change(fixture)
			f := fixture.write(t)

			got, err := f.sanitize(t, f.out)
			require.Error(t, err, got)
			assert.Contains(t, got, tc.want)

			if tc.value != "" {
				assert.NotContains(t, got, tc.value, "the message holds no value")
			}

			assert.NotContains(t, got, codexRoot, "the message holds no real ID")
			assert.NotContains(t, got, "T10-41-", "the message holds no local name time")
		})
	}

	t.Run("the refusal names the rollout without its ID and time", func(t *testing.T) {
		fixture := newCodexRecordFixture(t)
		model("call " + token)(fixture)
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.Error(t, err, got)
		assert.Contains(t, got, "sessions/2026/11/10/rollout-<time>-<id>.jsonl: holds an unmapped fc_ ID")
	})
}
