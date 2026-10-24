package integration

// Offline replay of the gate-3 timing recordings (plan step P3.14).
// `make capture-native-record CLIENT=<client> CAPTURE_NATIVE_SCENARIO=timing`
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
// hook-context.jsonl and the transcripts (gate-3 checklist).
//
// The tests run only when SEAMARK_NATIVE_CLIENT names the client (make
// capture-native-check). Without a reviewed recording they skip as
// pending. A skip gives no evidence, so it never verifies a timing item.

import (
	"bytes"
	"encoding/json"
	"errors"
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
// hook payloads and the transcripts (scripts/capture-native.sh,
// timing_extras).
const (
	timingCasesFile   = "timing-cases.jsonl"
	timingAuditFile   = "lessons-audit.jsonl"
	timingContextFile = "hook-context.jsonl"
)

// The case names of the timing scenario. The script writes one name per
// paid run, so the names are the contract between the script and these
// tests.
const (
	timingPriorResult           = "prior-result"
	timingNewModelStep          = "new-model-step"
	timingReadThenEdit          = "read-then-edit"
	timingSameCall              = "same-call"
	timingBatchAndProgram       = "batch-and-program"
	timingFailedRead            = "failed-read"
	timingSubagentRead          = "subagent-read"
	timingPriorPreparation      = "prior-preparation"
	timingPatchAfterPreparation = "patch-after-preparation"
	timingMultiFilePatch        = "multi-file-patch"
	timingSubagentPatch         = "subagent-patch"
)

// timingRecording is one reviewed gate-3 timing recording.
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

// timingAudit is the part of one audit record (contract L3) that the
// replay reads. The tags are the frozen field names of the log.
type timingAudit struct {
	Surface      string `json:"surface"`
	Delivery     string `json:"delivery"`
	SessionSHA   string `json:"session_sha256"`
	MatchSHA     string `json:"match_sha256"`
	ContextSHA   string `json:"context_sha256"`
	TurnSHA      string `json:"turn_sha256"`
	Lane         string `json:"lane"`
	Resettable   *bool  `json:"context_resettable"`
	CaptureOptIn bool   `json:"capture_opt_in"`
	Read         *bool  `json:"read"`
	Schema       int    `json:"schema_version"`
}

// loadTimingRecording reads the reviewed timing recording of a client,
// or skips. The recording must hold each named case exactly once.
func loadTimingRecording(t *testing.T, client string, cases ...string) timingRecording {
	t.Helper()

	if os.Getenv(nativeClientEnv) != client {
		t.Skipf("the timing replay runs with %s=%s (make capture-native-check CLIENT=%s)",
			nativeClientEnv, client, client)
	}

	dir := filepath.Join("testdata", client, "timing")

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("pending: no reviewed gate-3 timing recording in %s; record one with "+
			"make capture-native-record CLIENT=%s CAPTURE_NATIVE_SCENARIO=timing, review the sanitized copy, "+
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

	for _, h := range r.manifest.Hooks {
		// Run 0 holds payloads from before the first paid run, for
		// example of an interactive trust review. No case owns them.
		if h.Run == 0 {
			continue
		}

		payload, err := os.ReadFile(filepath.Join(dir, h.File))
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(payload, &raw), h.File)

		seq := len(r.events[h.Run])
		r.events[h.Run] = append(r.events[h.Run], timingEvent{hook: h, seq: seq, payload: payload, raw: raw})
	}

	t.Logf("gate-3 timing recording of %s %s: %d cases", client, r.manifest.ClientVersion, len(r.runs))

	return r
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

// audit reads the v2 audit records of the recording (contract L3). The
// hook, exploration, and preparation surfaces write v2 records. A record
// of another version holds none of the identity fields that the replay
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
// name the receiving context (contract L1, P0 review ruling N2).
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
// No Codex context is resettable (contract L3, Codex row).
func assertCodexLane(t *testing.T, name, lane string, meta EventMeta) {
	t.Helper()

	require.NotNil(t, meta.Context, "%s: the event names its receiver", name)
	assert.Equal(t, lane, meta.Context.Lane, name)
	assert.False(t, meta.Context.Resettable, "%s: no reset of a Codex context is verified", name)
	assert.NotEmpty(t, meta.Turn, "%s: the event names its turn (turn_id)", name)
	assert.NotEmpty(t, meta.MatchID, "%s: the event names its tool call", name)
}

func TestCaptureNativeClaudeTimingRecording(t *testing.T) {
	r := loadTimingRecording(t, ClaudeID, timingPriorResult, timingNewModelStep, timingReadThenEdit,
		timingSameCall, timingBatchAndProgram, timingFailedRead, timingSubagentRead)

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

		// The own hook is the only delivery path of the case: no read
		// came before the write in the new session.
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
		// ran after the model had constructed the second one (AC-24).
		assert.Equal(t, r.claudeCall(t, steps[1], first.EventMeta), r.claudeCall(t, steps[2], second.EventMeta),
			"one response constructed both writes; if not, the model did not batch them, so record the case again")

		input, _ := steps[3].raw["tool_input"].(map[string]any)
		command := nativeString(input, "command")
		assert.Contains(t, command, "lessons --prepare timing/program.txt", "the program prepares the file")
		assert.Contains(t, command, "> timing/program.txt", "the same program writes the file")

		// The shell writes the file, so no edit hook sees it.
		for _, e := range r.events[r.runs[timingBatchAndProgram]] {
			if e.hook.Event == claudePreToolUse && slices.Contains([]string{"Edit", "Write", "MultiEdit"}, e.tool()) {
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
			assert.True(t, a.CaptureOptIn, "the recording script set the recording switch for the paid runs")

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

		// The input of the prior-turn test of the delivery report: an
		// exploration injection, then an edit record of the same root
		// context in another turn.
		var priorTurn bool

		for i, d := range records {
			if d.Surface != "explore" || d.Delivery != "injected" || d.Lane != LaneRoot {
				continue
			}

			for _, a := range records[i+1:] {
				if a.Surface == "" && a.Lane == LaneRoot && a.SessionSHA == d.SessionSHA &&
					a.ContextSHA == d.ContextSHA && a.TurnSHA != d.TurnSHA {
					priorTurn = true
				}
			}
		}

		assert.True(t, priorTurn, "an exploration injection precedes an edit of the same root context in a later turn")
	})
}

func TestCaptureNativeCodexTimingRecording(t *testing.T) {
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
		// after the model constructed them together (AC-24).
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
			assert.NotEmpty(t, a.ContextSHA, "a hook record names its lane (§5 row 96)")
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
// completed item. The hook tool_use_id of a shell call equals the ID of
// its completed item (TestCaptureNativeCodexHookToolResults), and the
// recording shows which form an apply_patch call takes.
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
