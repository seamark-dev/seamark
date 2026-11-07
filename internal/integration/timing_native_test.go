package integration

// Offline replay of the native timing recordings.
// `make native-record CLIENT=<client> NATIVE_RECORD_SCENARIO=timing`
// records them with the installed client and the operator's own login.
// A reviewer copies the reviewed sanitized copy into
// testdata/<client>/timing/. Each paid run of the scenario is one case,
// and timing-cases.jsonl names the case of each run, so these tests hold
// no session identifier.
//
// The tests decode the recorded hook payloads through the production
// decoders. For each case they check four facts: the hook sequence, the
// turn identity, the lane identity, and the match identity. They also
// check that the audit records of the recording name the turn and the
// lane. They do not decide whether a hook reply reached the model. That
// answer is what the recording measures, so the reviewer reads it from
// hook-context.jsonl and the transcripts.
//
// The tests run only when SEAMARK_NATIVE_CLIENT names the client
// (make native-replay). Without a reviewed recording they skip as
// pending. A skip gives no evidence, so it never verifies a timing item.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The files that the timing scenario adds to a recording, beside the
// hook payloads and the transcripts (scripts/native-record.sh,
// timing_extras).
const (
	timingCasesFile   = "timing-cases.jsonl"
	timingAuditFile   = "lessons-audit.jsonl"
	timingContextFile = "hook-context.jsonl"
)

// The case names of the timing scenario. The script writes one name per
// paid run, so the script and these tests must use the same names.
const (
	timingPriorResult           = "prior-result"
	timingNewModelStep          = "new-model-step"
	timingReadThenEdit          = "read-then-edit"
	timingSameCall              = "same-call"
	timingBatchAndProgram       = "batch-and-program"
	timingFailedRead            = "failed-read"
	timingSubagentRead          = "subagent-read"
	timingCompactReset          = "compact-reset"
	timingAfterCompact          = "after-compact"
	timingPriorPreparation      = "prior-preparation"
	timingPatchAfterPreparation = "patch-after-preparation"
	timingMultiFilePatch        = "multi-file-patch"
	timingSubagentPatch         = "subagent-patch"
)

// timingCompactInstructions is the text after /compact in the prompt of
// the compaction case. Claude Code passes it to PreCompact as
// custom_instructions.
const timingCompactInstructions = "Keep the summary to one short sentence and name no file path."

// timingRecording is one reviewed native timing recording.
type timingRecording struct {
	dir      string
	manifest nativeManifest
	runs     map[string]int        // case name → paid run
	events   map[int][]timingEvent // paid run → hook payloads in recorded order
}

// timingEvent is one recorded hook payload of a paid run.
type timingEvent struct {
	hook    nativeHook
	seq     int // position in the run, from 0
	payload []byte
	raw     map[string]any
}

// tool returns the tool name of the payload, or "".
func (e timingEvent) tool() string { return nativeString(e.raw, "tool_name") }

// timingAudit is the part of one audit record that the replay reads.
// The tags are the stable field names of the audit log. A generation is
// zero when the record has no key for it.
type timingAudit struct {
	Surface           string `json:"surface"`
	Delivery          string `json:"delivery"`
	SessionSHA        string `json:"session_sha256"`
	MatchSHA          string `json:"match_sha256"`
	ContextSHA        string `json:"context_sha256"`
	TurnSHA           string `json:"turn_sha256"`
	Lane              string `json:"lane"`
	Resettable        *bool  `json:"context_resettable"`
	RecordEvaluations bool   `json:"record_evaluations"`
	Read              *bool  `json:"read"`
	Schema            int    `json:"schema_version"`
	ContextGeneration uint64 `json:"context_generation"`
	ExploreGeneration uint64 `json:"explore_generation"`
}

// The delivery values of the audit records that the timing replay reads.
const (
	timingDeliveryInjected = "injected"
	timingDeliveryReset    = "reset"
)

// loadTimingRecording reads the reviewed timing recording of a client,
// or skips. The recording must hold each named case exactly once.
func loadTimingRecording(t *testing.T, client string, cases ...string) timingRecording {
	t.Helper()

	if os.Getenv(nativeClientEnv) != client {
		t.Skipf("the timing replay runs with %s=%s (make native-replay CLIENT=%s)",
			nativeClientEnv, client, client)
	}

	dir := filepath.Join("testdata", client, "timing")

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("pending: no reviewed native timing recording in %s; record one with "+
			"make native-record CLIENT=%s NATIVE_RECORD_SCENARIO=timing, review the sanitized copy, "+
			"and copy it there", dir, client)
	}

	require.NoError(t, err)

	r := timingRecording{dir: dir, runs: map[string]int{}, events: map[int][]timingEvent{}}
	require.NoError(t, json.Unmarshal(data, &r.manifest))
	require.Equal(t, client, r.manifest.Client)
	require.Subset(t, r.manifest.Extras, []string{timingCasesFile, timingAuditFile, timingContextFile},
		"the timing scenario adds its case list, its audit records, and the hook context")

	for _, line := range timingLines(t, filepath.Join(dir, timingCasesFile)) {
		var c struct {
			Run  int    `json:"run"`
			Case string `json:"case"`
		}

		require.NoError(t, json.Unmarshal(line, &c))
		require.NotContains(t, r.runs, c.Case, "the case %s ran once", c.Case)

		r.runs[c.Case] = c.Run
	}

	require.ElementsMatch(t, cases, slices.Collect(maps.Keys(r.runs)), "the recording holds every case of the scenario")

	r.readHookEvents(t)

	t.Logf("native timing recording of %s %s: %d cases", client, r.manifest.ClientVersion, len(r.runs))

	return r
}

// readHookEvents reads the hook payloads of the manifest into events,
// per paid run in recorded order. The timing and the guard loaders share
// it, so both replays number the payloads of a run alike.
func (r *timingRecording) readHookEvents(t *testing.T) {
	t.Helper()

	for _, h := range r.manifest.Hooks {
		// Run 0 holds payloads from before the first paid run, for
		// example of an interactive trust review. No case owns them.
		if h.Run == 0 {
			continue
		}

		payload, err := os.ReadFile(filepath.Join(r.dir, h.File))
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(payload, &raw), h.File)

		seq := len(r.events[h.Run])
		r.events[h.Run] = append(r.events[h.Run], timingEvent{hook: h, seq: seq, payload: payload, raw: raw})
	}
}

// timingLines returns the non-empty lines of a JSONL file.
func timingLines(t *testing.T, file string) [][]byte {
	t.Helper()

	data, err := os.ReadFile(file)
	require.NoError(t, err)

	var lines [][]byte

	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}

	return lines
}

// TestTimingRecordingReadsHookEvents checks the payload reader that the
// timing and the guard replays share, on a synthetic recording. The
// payload file of run 0 does not exist, so a read of it fails the test.
func TestTimingRecordingReadsHookEvents(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o755))

	payloads := map[string]string{
		"hooks/002-UserPromptSubmit.json": `{"session_id":"s1"}`,
		"hooks/003-PreToolUse.json":       `{"session_id":"s1","tool_name":"Write"}`,
		"hooks/004-PreToolUse.json":       `{"session_id":"s2","tool_name":"Edit"}`,
	}

	for name, payload := range payloads {
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(payload), 0o600))
	}

	r := timingRecording{dir: dir, events: map[int][]timingEvent{}}
	r.manifest.Hooks = []nativeHook{
		{File: "hooks/001-SessionStart.json", Run: 0, Event: "SessionStart"},
		{File: "hooks/002-UserPromptSubmit.json", Run: 1, Event: "UserPromptSubmit"},
		{File: "hooks/003-PreToolUse.json", Run: 1, Event: "PreToolUse"},
		{File: "hooks/004-PreToolUse.json", Run: 2, Event: "PreToolUse"},
	}

	r.readHookEvents(t)

	require.Len(t, r.events, 2, "run 0 has no case, so the reader skips it")
	require.Len(t, r.events[1], 2)
	require.Len(t, r.events[2], 1)

	assert.Equal(t, []int{0, 1}, []int{r.events[1][0].seq, r.events[1][1].seq}, "a run numbers its payloads from 0")
	assert.Equal(t, 0, r.events[2][0].seq, "each run starts its own numbering")
	assert.Equal(t, "Write", r.events[1][1].tool())
	assert.Equal(t, payloads["hooks/003-PreToolUse.json"], string(r.events[1][1].payload))
	assert.Equal(t, r.manifest.Hooks[3], r.events[2][0].hook)
}

// sequence returns the payloads of one case that match the steps in
// order. A step is an event name, or an event name and a tool name
// joined by a colon. Other payloads can come between two steps, because
// a client also runs lifecycle hooks of its own.
func (r timingRecording) sequence(t *testing.T, name string, steps ...string) []timingEvent {
	t.Helper()

	events := r.events[r.runs[name]]
	matched := make([]timingEvent, 0, len(steps))
	next := 0

	for _, step := range steps {
		event, tool, _ := strings.Cut(step, ":")

		for next < len(events) {
			e := events[next]
			next++

			if e.hook.Event == event && (tool == "" || e.tool() == tool) {
				matched = append(matched, e)

				break
			}
		}
	}

	require.Len(t, matched, len(steps), "case %s (run %d): the hooks ran in the order [%s], want the steps [%s] in this order",
		name, r.runs[name], timingNames(events), strings.Join(steps, ", "))

	return matched
}

// before returns the payloads of a case that come before one of its
// payloads.
func (r timingRecording) before(name string, e timingEvent) []timingEvent {
	return r.events[r.runs[name]][:e.seq]
}

// The launch shapes of a Claude Code Agent call. A foreground agent ends
// before the Agent call returns. A background agent works after the call
// returns, and its result comes as a later prompt. A default request sets
// no run_in_background, so the shape depends on the default of the build.
const (
	launchForeground = "foreground"
	launchBackground = "background"
	launchDefault    = "default"
)

// The tool_response status of a PostToolUse Agent payload for each shape.
const (
	agentStatusCompleted     = "completed"
	agentStatusAsyncLaunched = "async_launched"
)

// agentLaunch reads the launch shape of the first Agent call of a case.
// The request comes from run_in_background of the PreToolUse Agent
// payload. The observed shape comes from the PostToolUse Agent payload of
// the same call: async_launched before the SubagentStop of the agent is
// background, and completed after it is foreground. Any other pair is an
// error, because it fits neither shape.
func (r timingRecording) agentLaunch(name string) (requested, observed string, err error) {
	events := r.events[r.runs[name]]

	pre := slices.IndexFunc(events, func(e timingEvent) bool {
		return e.hook.Event == claudeEventPreToolUse && e.tool() == claudeToolAgent
	})
	if pre < 0 {
		return "", "", errors.New("no PreToolUse Agent payload: the model made no Agent call; record the case again")
	}

	input, _ := events[pre].raw["tool_input"].(map[string]any)

	switch value, set := input["run_in_background"]; {
	case !set:
		requested = launchDefault
	case value == true:
		requested = launchBackground
	case value == false:
		requested = launchForeground
	default:
		return "", "", fmt.Errorf("run_in_background of the Agent call is %v, not a boolean", value)
	}

	call := nativeString(events[pre].raw, "tool_use_id")
	post := slices.IndexFunc(events, func(e timingEvent) bool {
		return e.seq > pre && e.hook.Event == "PostToolUse" && e.tool() == claudeToolAgent &&
			nativeString(e.raw, "tool_use_id") == call
	})
	if post < 0 {
		return requested, "", errors.New("no PostToolUse Agent payload names the Agent call")
	}

	response, _ := events[post].raw["tool_response"].(map[string]any)
	status, agent := nativeString(response, "status"), nativeString(response, "agentId")

	stop := slices.IndexFunc(events, func(e timingEvent) bool {
		return e.seq > pre && e.hook.Event == "SubagentStop" && (agent == "" || nativeString(e.raw, "agent_id") == agent)
	})
	if stop < 0 {
		return requested, "", errors.New("no SubagentStop payload names the agent of the Agent call")
	}

	switch {
	case status == agentStatusAsyncLaunched && post < stop:
		return requested, launchBackground, nil
	case status == agentStatusCompleted && stop < post:
		return requested, launchForeground, nil
	}

	order := "after"
	if stop < post {
		order = "before"
	}

	return requested, "", fmt.Errorf("the Agent call returned the status %q, and SubagentStop came %s it; "+
		"that fits no launch shape", status, order)
}

// launchShape checks the first Agent call of a case against the shape of
// the case. A request in another shape, also a request without the field,
// asks for a new recording, because the model did not follow the prompt.
// A matching request that ran in the other shape is a finding about the
// client, and a new recording does not change it. The agent of the call
// result must be the agent that SubagentStart announced.
func (r timingRecording) launchShape(name, shape string) error {
	requested, observed, err := r.agentLaunch(name)
	if err != nil {
		return err
	}

	if requested != shape {
		return fmt.Errorf("the model did not set the launch shape (the request reads %s, the case needs %s); "+
			"record the case again", requested, shape)
	}

	if observed != shape {
		return fmt.Errorf("Claude Code %s ran a %s launch as %s", r.manifest.ClientVersion, requested, observed)
	}

	var start, result string

	for _, e := range r.events[r.runs[name]] {
		switch {
		case e.hook.Event == "SubagentStart" && start == "":
			start = nativeString(e.raw, "agent_id")
		case e.hook.Event == "PostToolUse" && e.tool() == claudeToolAgent && result == "":
			response, _ := e.raw["tool_response"].(map[string]any)
			result = nativeString(response, "agentId")
		}
	}

	if start == "" || result != start {
		return errors.New("the Agent call result names another agent than SubagentStart, or no agent")
	}

	return nil
}

// timingNames prints the event and tool of each payload, for a failure
// message.
func timingNames(events []timingEvent) string {
	names := make([]string, 0, len(events))

	for _, e := range events {
		name := e.hook.Event
		if tool := e.tool(); tool != "" {
			name += ":" + tool
		}

		names = append(names, name)
	}

	return strings.Join(names, ", ")
}

// rel returns a decoded path relative to the placeholder workspace. The
// event directory resolves a relative path first.
func (r timingRecording) rel(cwd, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(cwd, p)
	}

	return strings.TrimPrefix(path.Clean(p), r.manifest.Placeholders.Workspace+"/")
}

// rels returns the decoded paths of an edit relative to the placeholder
// workspace.
func (r timingRecording) rels(e EditEvent) []string {
	out := make([]string, 0, len(e.Paths))

	for _, p := range e.Paths {
		out = append(out, r.rel(e.CWD, p))
	}

	return out
}

// transcript maps a recorded transcript path below the placeholder home
// onto the recording directory. The sanitizer keeps the layout below the
// client's home directory and drops that directory's name.
func (r timingRecording) transcript(t *testing.T, hint string) string {
	t.Helper()

	prefix := r.manifest.Placeholders.Home + "/"
	require.True(t, strings.HasPrefix(hint, prefix), "the transcript %q lies below the placeholder home", hint)

	rel := strings.TrimPrefix(hint, prefix)
	for _, home := range []string{".claude/", ".codex/"} {
		rel = strings.TrimPrefix(rel, home)
	}

	return filepath.Join(r.dir, filepath.FromSlash(rel))
}

// audit reads the v2 audit records of the recording. The hook,
// exploration, and preparation surfaces write v2 records. A record of
// another version holds none of the identity fields that the replay
// reads, so the reader leaves it out.
func (r timingRecording) audit(t *testing.T) []timingAudit {
	t.Helper()

	var records []timingAudit

	for _, line := range timingLines(t, filepath.Join(r.dir, timingAuditFile)) {
		var a timingAudit
		require.NoError(t, json.Unmarshal(line, &a))

		if a.Schema == 2 {
			records = append(records, a)
		}
	}

	require.NotEmpty(t, records, "the lesson hooks of the recording wrote v2 audit records")

	return records
}

// assertPreparationsHaveNoReceiver checks the preparation records of the
// audit: at least one exists, and none names a receiver. A preparation
// command returns its text to whoever ran it, so its record can never
// name the receiving context.
func assertPreparationsHaveNoReceiver(t *testing.T, records []timingAudit) {
	t.Helper()

	var prepared int

	for _, a := range records {
		if a.Surface != "prepare" {
			continue
		}

		prepared++

		assert.Empty(t, a.SessionSHA+a.MatchSHA+a.ContextSHA+a.TurnSHA+a.Lane,
			"a preparation record names no session, call, context, turn, or lane")
	}

	assert.Positive(t, prepared, "the preparation command wrote its record")
}

// claudeTimingCalls maps the ID of each tool call in a Claude Code
// transcript to the ID of the assistant message that holds it. Claude
// Code writes each content block of a message as a record of its own
// with the message ID, so two calls of one response share that ID.
func claudeTimingCalls(t *testing.T, file string) map[string]string {
	t.Helper()

	calls := map[string]string{}

	for _, line := range timingLines(t, file) {
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}

		require.NoError(t, json.Unmarshal(line, &rec))

		if rec.Type != "assistant" {
			continue
		}

		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}

		// The content of an assistant message is a list of blocks. Any
		// other shape holds no tool call.
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			continue
		}

		for _, b := range blocks {
			if b.Type == "tool_use" {
				calls[b.ID] = rec.Message.ID
			}
		}
	}

	return calls
}

// claudeCall checks the match identity of a root event: its tool_use_id
// names a tool call of the session transcript. It returns the ID of the
// assistant message that holds the call.
func (r timingRecording) claudeCall(t *testing.T, e timingEvent, meta EventMeta) string {
	t.Helper()

	file := r.transcript(t, nativeString(e.raw, "transcript_path"))
	message, ok := claudeTimingCalls(t, file)[meta.MatchID]
	require.True(t, ok, "%s: tool_use_id %s names a tool call of the session transcript", e.hook.File, meta.MatchID)

	return message
}

// claudeTimingEdit decodes a recorded edit payload with the production
// decoder. A Claude Code edit names one file.
func claudeTimingEdit(t *testing.T, e timingEvent) EditEvent {
	t.Helper()

	event, err := claudeEdits{}.DecodeEdit(e.payload)
	require.NoError(t, err, e.hook.File)
	require.Len(t, event.Paths, 1, e.hook.File)

	return event
}

// claudeTimingExplore decodes a recorded Read payload with the
// production decoder.
func claudeTimingExplore(t *testing.T, e timingEvent) ExploreEvent {
	t.Helper()

	event, err := claudeExplores{}.DecodeExplore(e.payload)
	require.NoError(t, err, e.hook.File)

	return event
}

// assertClaudeRoot checks the lane and turn identity of a root event: a
// resettable root receiver and a non-empty turn (prompt_id).
func assertClaudeRoot(t *testing.T, name string, meta EventMeta) {
	t.Helper()

	require.NotNil(t, meta.Context, "%s: the event names its receiver", name)
	assert.Equal(t, LaneRoot, meta.Context.Lane, "%s: the session itself receives the reply", name)
	assert.True(t, meta.Context.Resettable, "%s: PostCompact resets the root context", name)
	assert.NotEmpty(t, meta.Turn, "%s: the event names its turn", name)
	assert.NotEmpty(t, meta.MatchID, "%s: the event names its tool call", name)
}

// assertCodexLane checks the lane and turn identity of a Codex event.
// No Codex context is resettable, because no native run verifies an
// automatic compaction or a nested subagent (see ReceivingContextOf).
func assertCodexLane(t *testing.T, name, lane string, meta EventMeta) {
	t.Helper()

	require.NotNil(t, meta.Context, "%s: the event names its receiver", name)
	assert.Equal(t, lane, meta.Context.Lane, name)
	assert.False(t, meta.Context.Resettable, "%s: no reset of a Codex context is verified", name)
	assert.NotEmpty(t, meta.Turn, "%s: the event names its turn (turn_id)", name)
	assert.NotEmpty(t, meta.MatchID, "%s: the event names its tool call", name)
}

func TestNativeRecordClaudeTimingRecording(t *testing.T) {
	r := loadTimingRecording(t, ClaudeID, timingPriorResult, timingNewModelStep, timingCompactReset, timingAfterCompact,
		timingReadThenEdit, timingSameCall, timingBatchAndProgram, timingFailedRead, timingSubagentRead)

	t.Run("a prior result, then a new model step", func(t *testing.T) {
		read := r.sequence(t, timingPriorResult, "UserPromptSubmit", "PostToolUse:Read")[1]
		explore := claudeTimingExplore(t, read)
		assert.True(t, explore.Read)
		assert.Equal(t, "timing/prior.txt", r.rel(explore.CWD, explore.Paths[0]))
		assertClaudeRoot(t, timingPriorResult, explore.EventMeta)
		r.claudeCall(t, read, explore.EventMeta)

		edit := r.sequence(t, timingNewModelStep, "UserPromptSubmit", "PreToolUse:Edit")[1]
		event := claudeTimingEdit(t, edit)
		assert.Equal(t, []string{"timing/prior.txt"}, r.rels(event))
		assertClaudeRoot(t, timingNewModelStep, event.EventMeta)
		r.claudeCall(t, edit, event.EventMeta)

		// The resumed run continues the session with a new prompt. So the
		// read result reached the session one turn before the edit.
		assert.Equal(t, explore.SessionID, event.SessionID, "the resumed run continues the session")
		assert.Equal(t, explore.Context.ID, event.Context.ID, "one receiver gets the read result and makes the edit")
		assert.NotEqual(t, explore.Turn, event.Turn, "the edit belongs to a later turn than the read")
	})

	t.Run("a read, then an edit in the same turn", func(t *testing.T) {
		steps := r.sequence(t, timingReadThenEdit, "UserPromptSubmit", "PostToolUse:Read", "PreToolUse:Edit")
		read := claudeTimingExplore(t, steps[1])
		edit := claudeTimingEdit(t, steps[2])

		assert.True(t, read.Read)
		assert.Equal(t, "timing/intra.txt", r.rel(read.CWD, read.Paths[0]))
		assert.Equal(t, []string{"timing/intra.txt"}, r.rels(edit))
		assertClaudeRoot(t, timingReadThenEdit, read.EventMeta)
		assertClaudeRoot(t, timingReadThenEdit, edit.EventMeta)
		assert.Equal(t, read.Context.ID, edit.Context.ID)
		assert.Equal(t, read.Turn, edit.Turn, "one prompt: the read and the edit share the turn")

		// Two assistant messages mean that a model request came after the
		// read result and before the edit, inside one turn.
		assert.NotEqual(t, r.claudeCall(t, steps[1], read.EventMeta), r.claudeCall(t, steps[2], edit.EventMeta),
			"the model constructed the edit in a later response than the read")
	})

	t.Run("a reminder on its own call", func(t *testing.T) {
		write := r.sequence(t, timingSameCall, "UserPromptSubmit", "PreToolUse:Write")[1]
		event := claudeTimingEdit(t, write)
		assert.Equal(t, []string{"timing/same.txt"}, r.rels(event))
		assertClaudeRoot(t, timingSameCall, event.EventMeta)
		r.claudeCall(t, write, event.EventMeta)

		// The hook of the write is the only delivery path of the case,
		// because no read came before the write in the new session.
		for _, e := range r.before(timingSameCall, write) {
			assert.NotEqual(t, claudeReadTool, e.tool(), "%s: no read before the write", e.hook.File)
		}
	})

	t.Run("a batch of writes and a program that prepares and writes", func(t *testing.T) {
		steps := r.sequence(t, timingBatchAndProgram, "UserPromptSubmit", "PreToolUse:Write", "PreToolUse:Write", "PreToolUse:Bash")
		first := claudeTimingEdit(t, steps[1])
		second := claudeTimingEdit(t, steps[2])

		assert.ElementsMatch(t, []string{"timing/batch-a.txt", "timing/batch-b.txt"},
			append(r.rels(first), r.rels(second)...))
		assertClaudeRoot(t, timingBatchAndProgram, first.EventMeta)
		assertClaudeRoot(t, timingBatchAndProgram, second.EventMeta)
		assert.Equal(t, first.Turn, second.Turn, "one prompt: both writes share the turn")

		// One response holds both calls. So the hook of the first write
		// ran after the model had constructed the second one.
		assert.Equal(t, r.claudeCall(t, steps[1], first.EventMeta), r.claudeCall(t, steps[2], second.EventMeta),
			"one response constructed both writes; if not, the model did not batch them, so record the case again")

		input, _ := steps[3].raw["tool_input"].(map[string]any)
		command := nativeString(input, "command")
		assert.Contains(t, command, "lessons --prepare timing/program.txt", "the program prepares the file")
		assert.Contains(t, command, "> timing/program.txt", "the same program writes the file")

		// The shell writes the file, so no edit hook sees it.
		for _, e := range r.events[r.runs[timingBatchAndProgram]] {
			if e.hook.Event == claudeEventPreToolUse && slices.Contains([]string{"Edit", "Write", "MultiEdit"}, e.tool()) {
				assert.NotContains(t, r.rels(claudeTimingEdit(t, e)), "timing/program.txt", e.hook.File)
			}
		}
	})

	t.Run("a failed read", func(t *testing.T) {
		read := r.sequence(t, timingFailedRead, "UserPromptSubmit", "PostToolUseFailure:Read")[1]
		event := claudeTimingExplore(t, read)
		assert.False(t, event.Read, "PostToolUseFailure reports a read that failed")
		assert.Equal(t, "timing/missing.txt", r.rel(event.CWD, event.Paths[0]))
		assertClaudeRoot(t, timingFailedRead, event.EventMeta)
		r.claudeCall(t, read, event.EventMeta)
	})

	t.Run("a subagent read", func(t *testing.T) {
		steps := r.sequence(t, timingSubagentRead, "UserPromptSubmit", "SubagentStart", "PostToolUse:Read")
		session := nativeString(steps[0].raw, "session_id")
		agent := nativeString(steps[1].raw, "agent_id")
		require.NotEmpty(t, agent, "SubagentStart names the agent")

		event := claudeTimingExplore(t, steps[2])
		assert.True(t, event.Read)
		assert.Equal(t, "timing/child.txt", r.rel(event.CWD, event.Paths[0]))
		assert.Equal(t, session, event.SessionID, "a subagent event keeps the session of its parent")
		require.Equal(t, ReceivingContextOf(ClaudeID, session, agent), event.Context,
			"the read names the child lane of the agent that SubagentStart announced")
		assert.Equal(t, LaneChild, event.Context.Lane)
		assert.False(t, event.Context.Resettable, "no verified reset reaches a subagent")
		assert.NotEmpty(t, event.Turn)

		// The call is in the transcript of the subagent, not in the
		// transcript of its parent: the lane that the payload names holds
		// the call.
		main := r.transcript(t, nativeString(steps[2].raw, "transcript_path"))
		child := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-"+agent+".jsonl")
		assert.Contains(t, claudeTimingCalls(t, child), event.MatchID)
		assert.NotContains(t, claudeTimingCalls(t, main), event.MatchID)

		// The prompt asks for a background agent.
		assert.NoError(t, r.launchShape(timingSubagentRead, launchBackground), timingSubagentRead)
	})

	t.Run("the audit records name turns and lanes", func(t *testing.T) {
		records := r.audit(t)

		var childReads, failedReads int

		for _, a := range records {
			if (a.Surface != "" && a.Surface != "explore") || a.Delivery == "reset" {
				continue
			}

			assert.NotEmpty(t, a.SessionSHA, "a hook record names its session")
			assert.NotEmpty(t, a.MatchSHA, "a hook record names its call")
			assert.NotEmpty(t, a.ContextSHA, "a hook record names its receiver")
			assert.NotEmpty(t, a.TurnSHA, "a hook record names its turn")
			assert.Contains(t, []string{LaneRoot, LaneChild}, a.Lane)
			assert.True(t, a.RecordEvaluations, "the recording script set the recording switch for the paid runs")

			if a.Surface == "explore" && a.Lane == LaneChild {
				childReads++
			}

			if a.Surface == "explore" && a.Read != nil && !*a.Read {
				failedReads++
			}
		}

		assert.Positive(t, childReads, "the subagent read wrote a child-lane record")
		assert.Positive(t, failedReads, "the failed read wrote a record with read: false")
		assertPreparationsHaveNoReceiver(t, records)

		assert.True(t, priorTurn(records), "an exploration injection precedes an edit of the same root context in a later turn")
	})

	t.Run("a compaction resets the root context", func(t *testing.T) {
		name := timingCompactReset
		steps := r.sequence(t, name, claudeEventSessionStart, claudeEventPreCompact, claudeEventPostCompact)
		assert.Equal(t, "resume", nativeString(steps[0].raw, "source"), "the run resumes the session of %s", timingNewModelStep)

		for _, e := range r.events[r.runs[name]] {
			assert.NotEqual(t, claudeEventPreToolUse, e.hook.Event, "%s: the compaction run calls no tool", e.hook.File)

			if e.hook.Event == claudeEventSessionStart {
				t.Logf("case %s: %s has the source %q", name, e.hook.File, nativeString(e.raw, "source"))
			}
		}

		pre, post := steps[1], steps[2]
		assert.Equal(t, "manual", nativeString(pre.raw, "trigger"), "/compact is a manual compaction")
		assert.Equal(t, timingCompactInstructions, strings.TrimSpace(nativeString(pre.raw, "custom_instructions")))

		// The payload of Claude Code 2.1.300 had prompt_id, and a new
		// recording does not change the client. So a missing turn is a
		// finding about the decoder, not a reason to record again.
		require.NotEmpty(t, nativeString(post.raw, "prompt_id"),
			"Claude Code %s sent PostCompact without prompt_id; the reset decoder needs a turn", r.manifest.ClientVersion)

		reset, err := claudeResets{}.DecodeReset(post.payload)
		require.NoError(t, err, post.hook.File)
		assert.Equal(t, LaneRoot, reset.Context.Lane, "the compaction resets the root context")
		assert.True(t, reset.Context.Resettable)

		edit := claudeTimingEdit(t, r.sequence(t, timingNewModelStep, "UserPromptSubmit", "PreToolUse:Edit")[1])
		assert.Equal(t, edit.SessionID, reset.SessionID, "the compaction resumes the session of the edit")
		assert.NotEmpty(t, reset.Turn)
		assert.NotEqual(t, edit.Turn, reset.Turn, "the compaction belongs to a later turn than the edit")
		assert.NotEmpty(t, nativeString(post.raw, "compact_summary"), "PostCompact carries the summary")

		assert.NoError(t, compactReset(r.audit(t)))
	})

	t.Run("advice returns after the compaction", func(t *testing.T) {
		name := timingAfterCompact
		events := r.events[r.runs[name]]

		// Claude Code answers some calls before any hook, for example an
		// Edit whose old text is not in the file. The replay cannot tell
		// that answer from a model that made no call, so it names both.
		for _, step := range []struct{ event, tool string }{
			{claudeEventPostTool, claudeReadTool}, {claudeEventPreToolUse, claudeToolEdit},
		} {
			require.True(t, slices.ContainsFunc(events, func(e timingEvent) bool {
				return e.hook.Event == step.event && e.tool() == step.tool
			}), "case %s: no %s %s payload: the model made no call, or Claude Code answered the call before any hook "+
				"(an Edit whose text beta is not in the file, or a Read stub before the hooks); record the case again",
				name, step.event, step.tool)
		}

		steps := r.sequence(t, name, claudeEventSessionStart, "UserPromptSubmit", "PostToolUse:Read", "PreToolUse:Edit")
		t.Logf("case %s: %s has the source %q", name, steps[0].hook.File, nativeString(steps[0].raw, "source"))

		// A second compaction writes a second reset record, so the advice
		// after it no longer shows the effect of the first reset.
		for _, e := range events {
			assert.NotContains(t, []string{claudeEventPreCompact, claudeEventPostCompact}, e.hook.Event,
				"%s: the run compacts again", e.hook.File)
		}

		post := r.sequence(t, timingCompactReset, claudeEventPostCompact)[0]
		reset, err := claudeResets{}.DecodeReset(post.payload)
		require.NoError(t, err, post.hook.File)

		edit := claudeTimingEdit(t, r.sequence(t, timingNewModelStep, "UserPromptSubmit", "PreToolUse:Edit")[1])
		assertClaudeRoot(t, timingNewModelStep, edit.EventMeta)

		read := claudeTimingExplore(t, steps[2])
		assert.True(t, read.Read)
		assert.Equal(t, "timing/prior.txt", r.rel(read.CWD, read.Paths[0]))
		assertClaudeRoot(t, name, read.EventMeta)
		assert.Equal(t, reset.SessionID, read.SessionID, "the run continues the compacted session")
		assert.Equal(t, edit.SessionID, read.SessionID, "the compacted session holds the earlier edit")
		assert.Equal(t, edit.Context.ID, read.Context.ID, "the compacted context reads the file again")
		assert.NotEqual(t, reset.Turn, read.Turn, "the read belongs to a later turn than the compaction")
		r.claudeCall(t, steps[2], read.EventMeta)

		again := claudeTimingEdit(t, steps[3])
		assert.Equal(t, []string{"timing/prior.txt"}, r.rels(again))
		assertClaudeRoot(t, name, again.EventMeta)
		assert.Equal(t, read.SessionID, again.SessionID)
		assert.Equal(t, read.Context.ID, again.Context.ID, "one receiver reads the file and edits it")
		assert.Equal(t, read.Turn, again.Turn, "one prompt: the read and the edit share the turn")
		r.claudeCall(t, steps[3], again.EventMeta)

		assert.NoError(t, adviceAfterReset(r.audit(t)))
	})
}

// priorTurn reports whether the audit records hold the input of the
// prior-turn test of the delivery report: an exploration injection, then
// an edit record of the same root context in another turn. A reset record
// has the surface of the edit hook too, so it does not count as an edit.
func priorTurn(records []timingAudit) bool {
	for i, d := range records {
		if d.Surface != "explore" || d.Delivery != timingDeliveryInjected || d.Lane != LaneRoot {
			continue
		}

		for _, a := range records[i+1:] {
			if a.Surface == "" && a.Delivery != timingDeliveryReset && a.Lane == LaneRoot &&
				a.SessionSHA == d.SessionSHA && a.ContextSHA == d.ContextSHA && a.TurnSHA != d.TurnSHA {
				return true
			}
		}
	}

	return false
}

// compactReset returns nil when the audit records, in file order, show
// one reset of the compacted root context. The recording holds exactly
// one reset record. It names the resettable root lane, carries the
// recording switch, and names no call. Before it, the same session and
// context have an exploration injection and an edit record in other
// turns. The reset starts a new generation of both kinds of delivery
// state, so each of its generations is above the generation of the last
// record of that kind. The last record counts, because a later case can
// add advice to the context on both sides of the reset.
func compactReset(records []timingAudit) error {
	at, err := resetRecord(records)
	if err != nil {
		return err
	}

	reset := records[at]

	switch {
	case reset.Lane != LaneRoot || reset.Resettable == nil || !*reset.Resettable:
		return errors.New("the reset record does not name the resettable root lane")
	case !reset.RecordEvaluations:
		return errors.New("the reset record lacks the recording switch")
	case reset.MatchSHA != "":
		return errors.New("the reset record names a call")
	case reset.SessionSHA == "" || reset.ContextSHA == "" || reset.TurnSHA == "":
		return errors.New("the reset record names no session, context, or turn")
	}

	last := func(match func(timingAudit) bool) (timingAudit, bool) {
		for i := at - 1; i >= 0; i-- {
			a := records[i]
			if a.Lane == LaneRoot && a.SessionSHA == reset.SessionSHA && a.ContextSHA == reset.ContextSHA && match(a) {
				return a, true
			}
		}

		return timingAudit{}, false
	}

	explore, found := last(func(a timingAudit) bool { return a.Surface == "explore" && a.Delivery == timingDeliveryInjected })
	if !found {
		return errors.New("no exploration injection of the reset context comes before the reset")
	}

	edit, found := last(func(a timingAudit) bool { return a.Surface == "" && a.Delivery != timingDeliveryReset })
	if !found {
		return errors.New("no edit record of the reset context comes before the reset")
	}

	switch {
	case explore.TurnSHA == reset.TurnSHA || edit.TurnSHA == reset.TurnSHA:
		return errors.New("the reset shares a turn with the advice before it")
	case reset.ContextGeneration <= edit.ContextGeneration:
		return fmt.Errorf("the reset edit generation %d is not above %d", reset.ContextGeneration, edit.ContextGeneration)
	case reset.ExploreGeneration <= explore.ExploreGeneration:
		return fmt.Errorf("the reset exploration generation %d is not above %d", reset.ExploreGeneration, explore.ExploreGeneration)
	}

	return nil
}

// resetRecord returns the position of the one reset record in the audit
// records. compactReset and adviceAfterReset both find the reset record
// here, so the two rules always read the same record.
func resetRecord(records []timingAudit) (int, error) {
	at := -1

	for i, a := range records {
		if a.Delivery != timingDeliveryReset {
			continue
		}

		if at >= 0 {
			return -1, errors.New("the recording holds more than one reset record")
		}

		at = i
	}

	if at < 0 {
		return -1, errors.New("the recording holds no reset record")
	}

	return at, nil
}

// adviceAfterReset returns nil when the audit records, in file order,
// show that advice reaches the compacted root context again after the
// reset. The reset starts a new generation of both kinds of delivery
// state. So the next read and the next edit of a file whose advice the
// context got before the reset get that advice again. Only the records
// after the reset that name its session, its context, and the root lane
// count. The first exploration record and the first edit record among
// them must be injections in the generation of the reset, with a call,
// in one turn after the reset. A suppressed repeat or an evaluation there
// is a finding about the reset. A new recording cannot change it, because
// the model does not control the delivery state.
func adviceAfterReset(records []timingAudit) error {
	at, err := resetRecord(records)
	if err != nil {
		return err
	}

	reset := records[at]

	var after []timingAudit

	for _, a := range records[at+1:] {
		if a.Lane == LaneRoot && a.SessionSHA == reset.SessionSHA && a.ContextSHA == reset.ContextSHA {
			after = append(after, a)
		}
	}

	if len(after) == 0 {
		return errors.New("no record of the reset context comes after the reset")
	}

	first := func(match func(timingAudit) bool) (timingAudit, bool) {
		i := slices.IndexFunc(after, match)
		if i < 0 {
			return timingAudit{}, false
		}

		return after[i], true
	}

	explore, found := first(func(a timingAudit) bool { return a.Surface == "explore" })
	if !found {
		return errors.New("no exploration record of the reset context comes after the reset")
	}

	edit, found := first(func(a timingAudit) bool { return a.Surface == "" && a.Delivery != timingDeliveryReset })
	if !found {
		return errors.New("no edit record of the reset context comes after the reset")
	}

	const finding = "the reset did not give the advice back to the compacted context"

	switch {
	case explore.Delivery != timingDeliveryInjected:
		return fmt.Errorf("%s: the first exploration record after the reset reads %q", finding, explore.Delivery)
	case edit.Delivery != timingDeliveryInjected:
		return fmt.Errorf("%s: the first edit record after the reset reads %q", finding, edit.Delivery)
	case explore.ExploreGeneration != reset.ExploreGeneration:
		return fmt.Errorf("the exploration injection after the reset has the generation %d, not %d",
			explore.ExploreGeneration, reset.ExploreGeneration)
	case edit.ContextGeneration != reset.ContextGeneration:
		return fmt.Errorf("the edit injection after the reset has the generation %d, not %d",
			edit.ContextGeneration, reset.ContextGeneration)
	case explore.Read == nil || !*explore.Read:
		return errors.New("the exploration injection after the reset reports no successful read")
	case explore.MatchSHA == "" || edit.MatchSHA == "":
		return errors.New("an injection after the reset names no call")
	case explore.TurnSHA == "" || explore.TurnSHA == reset.TurnSHA || edit.TurnSHA == reset.TurnSHA:
		return errors.New("an injection after the reset names no turn, or the turn of the reset")
	case explore.TurnSHA != edit.TurnSHA:
		return errors.New("the read and the edit after the reset belong to two turns, not to one prompt")
	}

	return nil
}

func TestNativeRecordCodexTimingRecording(t *testing.T) {
	r := loadTimingRecording(t, CodexID, timingPriorPreparation, timingPatchAfterPreparation, timingSameCall,
		timingMultiFilePatch, timingSubagentPatch)
	calls := r.codexCalls(t)

	t.Run("a prior preparation, then a patch in a later turn", func(t *testing.T) {
		shell := r.sequence(t, timingPriorPreparation, "UserPromptSubmit", "PreToolUse:Bash")[1]
		input, _ := shell.raw["tool_input"].(map[string]any)
		assert.Contains(t, nativeString(input, "command"), "lessons --prepare timing/prior.txt")
		assert.Empty(t, nativeString(shell.raw, "agent_id"), "the parent runs the preparation")
		require.NotEmpty(t, nativeString(shell.raw, "turn_id"))

		patch := r.sequence(t, timingPatchAfterPreparation, "UserPromptSubmit", "PreToolUse:apply_patch")[1]
		event := codexTimingEdit(t, patch)
		assert.Equal(t, []string{"timing/prior.txt"}, r.rels(event))
		assertCodexLane(t, timingPatchAfterPreparation, LaneRoot, event.EventMeta)
		assert.Contains(t, calls, event.MatchID, "tool_use_id names a tool call of a rollout")

		// The resumed run continues the thread with a new turn. So the
		// preparation output reached the thread one turn before the patch.
		assert.Equal(t, nativeString(shell.raw, "session_id"), event.SessionID, "the resumed run continues the thread")
		assert.NotEqual(t, nativeString(shell.raw, "turn_id"), event.Turn, "the patch belongs to a later turn")
	})

	t.Run("a reminder on its own call", func(t *testing.T) {
		patch := r.sequence(t, timingSameCall, "UserPromptSubmit", "PreToolUse:apply_patch")[1]
		event := codexTimingEdit(t, patch)
		assert.Equal(t, []string{"timing/same.txt"}, r.rels(event))
		assertCodexLane(t, timingSameCall, LaneRoot, event.EventMeta)
		assert.Contains(t, calls, event.MatchID, "tool_use_id names a tool call of a rollout")
	})

	t.Run("one patch for two files", func(t *testing.T) {
		patch := r.sequence(t, timingMultiFilePatch, "UserPromptSubmit", "PreToolUse:apply_patch")[1]
		event := codexTimingEdit(t, patch)

		// One call names both files, so one hook reply covers both edits
		// after the model constructed them together.
		assert.ElementsMatch(t, []string{"timing/multi-a.txt", "timing/multi-b.txt"}, r.rels(event),
			"one patch adds both files; if not, record the case again")
		assertCodexLane(t, timingMultiFilePatch, LaneRoot, event.EventMeta)
		assert.Contains(t, calls, event.MatchID, "tool_use_id names a tool call of a rollout")
	})

	t.Run("a subagent patch", func(t *testing.T) {
		steps := r.sequence(t, timingSubagentPatch, "UserPromptSubmit", "SubagentStart", "PreToolUse:apply_patch")
		session := nativeString(steps[0].raw, "session_id")
		agent := nativeString(steps[1].raw, "agent_id")
		require.NotEmpty(t, agent, "SubagentStart names the agent")

		event := codexTimingEdit(t, steps[2])
		assert.Equal(t, []string{"timing/child-patch.txt"}, r.rels(event))
		assert.Equal(t, session, event.SessionID, "a subagent event keeps the session of its parent")
		require.Equal(t, ReceivingContextOf(CodexID, session, agent), event.Context,
			"the patch names the child lane of the agent that SubagentStart announced")
		assertCodexLane(t, timingSubagentPatch, LaneChild, event.EventMeta)
		assert.Contains(t, calls, event.MatchID, "tool_use_id names a tool call of a rollout")
	})

	t.Run("the audit records name turns and lanes", func(t *testing.T) {
		records := r.audit(t)

		var children int

		for _, a := range records {
			if a.Surface != "" {
				continue
			}

			assert.NotEmpty(t, a.SessionSHA, "a hook record names its session")
			assert.NotEmpty(t, a.MatchSHA, "a hook record names its call")
			assert.NotEmpty(t, a.ContextSHA, "a hook record names its lane")
			assert.NotEmpty(t, a.TurnSHA, "a hook record names its turn")
			assert.Contains(t, []string{LaneRoot, LaneChild}, a.Lane)

			if assert.NotNil(t, a.Resettable, "a hook record states the reset policy") {
				assert.False(t, *a.Resettable, "no Codex context is resettable")
			}

			if a.Lane == LaneChild {
				children++
			}
		}

		assert.Positive(t, children, "the subagent patch wrote a child-lane record")
		assertPreparationsHaveNoReceiver(t, records)
	})
}

// codexTimingEdit decodes a recorded apply_patch payload with the
// production decoder.
func codexTimingEdit(t *testing.T, e timingEvent) EditEvent {
	t.Helper()

	event, err := codexEdits{}.DecodeEdit(e.payload)
	require.NoError(t, err, e.hook.File)

	return event
}

// codexCalls collects the tool-call IDs of every rollout of the
// recording: the call_id of a tool-call response item and the ID of a
// completed item. The set holds both forms, because the hook
// tool_use_id of a shell call can name its completed item. The recording
// shows which form an apply_patch call takes.
func (r timingRecording) codexCalls(t *testing.T) map[string]bool {
	t.Helper()

	calls := map[string]bool{}

	for _, name := range r.manifest.Transcripts {
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		for _, line := range timingLines(t, filepath.Join(r.dir, filepath.FromSlash(name))) {
			var rec map[string]any
			require.NoError(t, json.Unmarshal(line, &rec), name)

			payload, _ := rec["payload"].(map[string]any)
			kind := nativeString(payload, "type")

			switch {
			case rec["type"] == "response_item" && strings.HasSuffix(kind, "_call"):
				calls[nativeString(payload, "call_id")] = true
			case rec["type"] == "event_msg" && kind == "item_completed":
				item, _ := payload["item"].(map[string]any)
				calls[nativeString(item, "id")] = true
			}
		}
	}

	delete(calls, "")

	return calls
}

// launchPayloads gives the payloads of a Claude Code child case in one
// launch shape. A foreground call returns completed after SubagentStop. A
// background call returns async_launched first, and the result of the
// agent comes as a later prompt. A nil request leaves run_in_background
// out of the Agent call.
func launchPayloads(request any, status string, background bool) []map[string]any {
	input := map[string]any{"subagent_type": "writer", "prompt": "create the file"}
	if request != nil {
		input["run_in_background"] = request
	}

	pre := map[string]any{"hook_event_name": "PreToolUse", "tool_name": claudeToolAgent, "tool_use_id": "toolu_1",
		"tool_input": input}
	post := map[string]any{"hook_event_name": "PostToolUse", "tool_name": claudeToolAgent, "tool_use_id": "toolu_1",
		"tool_response": map[string]any{"status": status, "agentId": "a1"}}
	start := map[string]any{"hook_event_name": "SubagentStart", "agent_id": "a1"}
	stop := map[string]any{"hook_event_name": "SubagentStop", "agent_id": "a1"}
	prompt := map[string]any{"hook_event_name": "UserPromptSubmit"}

	if background {
		return []map[string]any{prompt, pre, start, post, {"hook_event_name": "Stop"}, stop, prompt}
	}

	return []map[string]any{prompt, pre, start, stop, post, {"hook_event_name": "Stop"}}
}

// launchRecording builds a synthetic timing recording of one case in run 1.
func launchRecording(t *testing.T, payloads []map[string]any) timingRecording {
	t.Helper()

	r := timingRecording{runs: map[string]int{"child": 1}, events: map[int][]timingEvent{}}
	r.manifest.ClientVersion = "synthetic"
	r.events[1] = syntheticEvents(t, 1, payloads)

	return r
}

// TestAgentLaunchReadsTheShape checks the launch shape reader and its
// check on synthetic recordings. They give no native proof.
func TestAgentLaunchReadsTheShape(t *testing.T) {
	for _, tc := range []struct {
		name               string
		payloads           []map[string]any
		requested, observe string
		shape              string
		err                string
	}{
		{"a foreground launch", launchPayloads(false, agentStatusCompleted, false),
			launchForeground, launchForeground, launchForeground, ""},
		{"a background launch", launchPayloads(true, agentStatusAsyncLaunched, true),
			launchBackground, launchBackground, launchBackground, ""},
		{"a request without the field", launchPayloads(nil, agentStatusAsyncLaunched, true),
			launchDefault, launchBackground, launchBackground, "record the case again"},
		{"a foreground request that ran in the background", launchPayloads(false, agentStatusAsyncLaunched, true),
			launchForeground, launchBackground, launchForeground, "Claude Code synthetic ran a foreground launch as background"},
		{"a background request that ran in the foreground", launchPayloads(true, agentStatusCompleted, false),
			launchBackground, launchForeground, launchBackground, "Claude Code synthetic ran a background launch as foreground"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := launchRecording(t, tc.payloads)

			requested, observed, err := r.agentLaunch("child")
			require.NoError(t, err)
			assert.Equal(t, tc.requested, requested)
			assert.Equal(t, tc.observe, observed)

			err = r.launchShape("child", tc.shape)
			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
			}
		})
	}

	t.Run("a status that disagrees with the order", func(t *testing.T) {
		for _, payloads := range [][]map[string]any{
			launchPayloads(true, agentStatusCompleted, true),
			launchPayloads(false, agentStatusAsyncLaunched, false),
		} {
			_, _, err := launchRecording(t, payloads).agentLaunch("child")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "fits no launch shape")
		}
	})

	t.Run("a call result that names another agent", func(t *testing.T) {
		payloads := launchPayloads(false, agentStatusCompleted, false)
		payloads[2]["agent_id"] = "a2"

		err := launchRecording(t, payloads).launchShape("child", launchForeground)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "another agent than SubagentStart")
	})

	t.Run("no Agent call", func(t *testing.T) {
		_, _, err := launchRecording(t, []map[string]any{{"hook_event_name": "UserPromptSubmit"}}).agentLaunch("child")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "record the case again")
	})
}

// compactRecords gives the audit records of the expected compaction: an
// exploration injection, an edit injection in the next turn, and the
// reset record in a later turn, all of one root context.
func compactRecords() []timingAudit {
	yes := true

	return []timingAudit{
		{Surface: "explore", Delivery: timingDeliveryInjected, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t1",
			MatchSHA: "m1", Lane: LaneRoot, Resettable: &yes, RecordEvaluations: true, ExploreGeneration: 1},
		{Delivery: timingDeliveryInjected, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t2", MatchSHA: "m2",
			Lane: LaneRoot, Resettable: &yes, RecordEvaluations: true, ContextGeneration: 1},
		{Delivery: timingDeliveryReset, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t3", Lane: LaneRoot,
			Resettable: &yes, RecordEvaluations: true, ContextGeneration: 2, ExploreGeneration: 2},
	}
}

// TestPriorTurnSkipsResetRecords checks the prior-turn rule on synthetic
// records. A reset record has the surface of the edit hook, but it is no
// edit. They give no native proof.
func TestPriorTurnSkipsResetRecords(t *testing.T) {
	records := compactRecords()

	assert.False(t, priorTurn([]timingAudit{records[0], records[2]}), "a reset record alone is no edit in a later turn")
	assert.True(t, priorTurn(records[:2]), "an edit in a later turn counts")
	assert.False(t, priorTurn([]timingAudit{records[1], records[0]}), "the edit must come after the injection")
}

// TestCompactResetReadsTheResetRecord checks the reset rule of the
// compaction case on synthetic records. They give no native proof. Each
// change to the expected records must give an error.
func TestCompactResetReadsTheResetRecord(t *testing.T) {
	require.NoError(t, compactReset(compactRecords()))

	later := compactRecords()
	later = append(later, timingAudit{Delivery: timingDeliveryInjected, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t4",
		Lane: LaneRoot, ContextGeneration: 2})
	assert.NoError(t, compactReset(later), "advice after the reset does not count")

	for _, tc := range []struct {
		name   string
		change func([]timingAudit) []timingAudit
	}{
		{"no reset record", func(r []timingAudit) []timingAudit { return r[:2] }},
		{"two reset records", func(r []timingAudit) []timingAudit { return append(r, r[2]) }},
		{"a reset record that names a call", func(r []timingAudit) []timingAudit { r[2].MatchSHA = "m3"; return r }},
		{"a reset record without the switch", func(r []timingAudit) []timingAudit { r[2].RecordEvaluations = false; return r }},
		{"a child reset", func(r []timingAudit) []timingAudit { r[2].Lane = LaneChild; return r }},
		{"no injection before the reset", func(r []timingAudit) []timingAudit { return r[1:] }},
		{"no edit before the reset", func(r []timingAudit) []timingAudit { return []timingAudit{r[0], r[2]} }},
		{"another session", func(r []timingAudit) []timingAudit { r[2].SessionSHA = "s2"; return r }},
		{"another context", func(r []timingAudit) []timingAudit { r[2].ContextSHA = "c2"; return r }},
		{"an edit generation that is not above", func(r []timingAudit) []timingAudit { r[2].ContextGeneration = 1; return r }},
		{"an exploration generation that is not above", func(r []timingAudit) []timingAudit {
			r[2].ExploreGeneration = 1
			return r
		}},
		{"the turn of the injection", func(r []timingAudit) []timingAudit { r[2].TurnSHA = "t1"; return r }},
		{"the turn of the edit", func(r []timingAudit) []timingAudit { r[2].TurnSHA = "t2"; return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, compactReset(tc.change(compactRecords())))
		})
	}
}

// afterResetRecords gives the audit records of the expected compaction,
// then the records of the read and the edit after it: an exploration
// injection and an edit injection of the reset context, in one later
// turn and in the generation of the reset.
func afterResetRecords() []timingAudit {
	yes := true

	return append(compactRecords(),
		timingAudit{Surface: "explore", Delivery: timingDeliveryInjected, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t4",
			MatchSHA: "m4", Lane: LaneRoot, Resettable: &yes, RecordEvaluations: true, Read: &yes, ExploreGeneration: 2},
		timingAudit{Delivery: timingDeliveryInjected, SessionSHA: "s", ContextSHA: "c", TurnSHA: "t4", MatchSHA: "m5",
			Lane: LaneRoot, Resettable: &yes, RecordEvaluations: true, ContextGeneration: 2},
	)
}

// TestAdviceAfterResetReadsTheReturnOfAdvice checks the rule of the case
// after the compaction on synthetic records. They give no native proof.
// Only the first record of each kind after the reset counts, so a later
// repeat or an earlier record of another context changes nothing. Each
// other change to the expected records must give an error.
func TestAdviceAfterResetReadsTheReturnOfAdvice(t *testing.T) {
	require.NoError(t, adviceAfterReset(afterResetRecords()))

	repeat := afterResetRecords()
	repeat = append(repeat, timingAudit{Delivery: "suppressed-repeat", SessionSHA: "s", ContextSHA: "c", TurnSHA: "t5",
		MatchSHA: "m6", Lane: LaneRoot, ContextGeneration: 2})
	assert.NoError(t, adviceAfterReset(repeat), "a repeat after the first edit injection does not count")

	other := afterResetRecords()
	other = slices.Insert(other, 3, timingAudit{Surface: "explore", Delivery: timingDeliveryInjected, SessionSHA: "s",
		ContextSHA: "c2", TurnSHA: "t4", MatchSHA: "m7", Lane: LaneRoot, ExploreGeneration: 1})
	assert.NoError(t, adviceAfterReset(other), "a record of another context does not count")

	const finding = "the reset did not give the advice back to the compacted context"

	for _, tc := range []struct {
		name   string
		change func([]timingAudit) []timingAudit
		want   string
	}{
		{name: "no record after the reset", change: func(r []timingAudit) []timingAudit { return r[:3] }},
		{name: "only records of another context after the reset", change: func(r []timingAudit) []timingAudit {
			r[3].ContextSHA, r[4].ContextSHA = "c2", "c2"
			return r
		}},
		{name: "the exploration record reads evaluation", change: func(r []timingAudit) []timingAudit {
			r[3].Delivery = "evaluation"
			return r
		}, want: finding},
		{name: "no exploration record", change: func(r []timingAudit) []timingAudit { return slices.Delete(r, 3, 4) }},
		{name: "no edit record", change: func(r []timingAudit) []timingAudit { return r[:4] }},
		{name: "the edit reads suppressed-repeat", change: func(r []timingAudit) []timingAudit {
			r[4].Delivery = "suppressed-repeat"
			return r
		}, want: finding},
		{name: "an edit generation of 1", change: func(r []timingAudit) []timingAudit { r[4].ContextGeneration = 1; return r }},
		{name: "an exploration generation of 1", change: func(r []timingAudit) []timingAudit {
			r[3].ExploreGeneration = 1
			return r
		}},
		{name: "the exploration turn of the reset", change: func(r []timingAudit) []timingAudit { r[3].TurnSHA = "t3"; return r }},
		{name: "the edit turn of the reset", change: func(r []timingAudit) []timingAudit { r[4].TurnSHA = "t3"; return r }},
		{name: "the two records in two turns", change: func(r []timingAudit) []timingAudit { r[4].TurnSHA = "t5"; return r }},
		{name: "a failed read", change: func(r []timingAudit) []timingAudit {
			no := false
			r[3].Read = &no
			return r
		}},
		{name: "an edit without a match digest", change: func(r []timingAudit) []timingAudit { r[4].MatchSHA = ""; return r }},
		{name: "two reset records", change: func(r []timingAudit) []timingAudit { return append(r, r[2]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := adviceAfterReset(tc.change(afterResetRecords()))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
