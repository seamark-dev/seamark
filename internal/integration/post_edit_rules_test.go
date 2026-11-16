package integration

// Tests of the replay rules of the post-edit scenario on synthetic
// recordings. They give no native proof. Each expected run passes the
// replay of its case, and each change to it gives an error or a skip
// that names its reason.

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// postEditCaseRow returns the row of one case of a client.
func postEditCaseRow(t *testing.T, client, name string) postEditCase {
	t.Helper()

	i := slices.IndexFunc(postEditCases(client), func(x postEditCase) bool { return x.name == name })
	require.GreaterOrEqual(t, i, 0, "the table of %s has the case %s", client, name)

	return postEditCases(client)[i]
}

// postEditPayloads gives the payloads of the expected run of a case: the
// prompt, the call with its PostToolUse payload, and the stop. A child
// call comes between SubagentStart and SubagentStop. A Claude Code child
// call also comes inside a foreground Agent call, whose result reads
// completed after SubagentStop.
func postEditPayloads(client string, x postEditCase) []map[string]any {
	const session, agent, workspace = "session-a", "agent-1", "/workspace/native"

	var input map[string]any

	switch x.tool {
	case claudeToolEdit:
		input = map[string]any{"file_path": workspace + "/" + x.path, "old_string": "alpha", "new_string": "beta"}
	case claudeToolWrite:
		input = map[string]any{"file_path": workspace + "/" + x.path, "content": "slow\n"}
	default:
		input = map[string]any{"command": "*** Begin Patch\n*** Add File: " + x.path + "\n+slow\n*** End Patch\n"}
	}

	pre := map[string]any{"hook_event_name": "PreToolUse", "session_id": session, "cwd": workspace,
		"tool_name": x.tool, "tool_use_id": "call-1", "tool_input": input}
	post := maps.Clone(pre)
	post["hook_event_name"] = "PostToolUse"

	if x.lane == LaneRoot {
		return []map[string]any{
			{"hook_event_name": "UserPromptSubmit", "session_id": session}, pre, post,
			{"hook_event_name": "Stop", "session_id": session},
		}
	}

	pre["agent_id"], post["agent_id"] = agent, agent
	start := map[string]any{"hook_event_name": "SubagentStart", "session_id": session, "agent_id": agent}
	stop := map[string]any{"hook_event_name": "SubagentStop", "session_id": session, "agent_id": agent,
		"last_assistant_message": "frozen-sentinel"}

	if client == CodexID {
		return []map[string]any{
			{"hook_event_name": "UserPromptSubmit", "session_id": session}, start, pre, post, stop,
			{"hook_event_name": "Stop", "session_id": session},
		}
	}

	launch := map[string]any{"hook_event_name": "PreToolUse", "session_id": session, "tool_name": claudeToolAgent,
		"tool_use_id": "toolu_agent", "tool_input": map[string]any{"subagent_type": "editor", "run_in_background": false}}
	result := map[string]any{"hook_event_name": "PostToolUse", "session_id": session, "tool_name": claudeToolAgent,
		"tool_use_id": "toolu_agent", "tool_response": map[string]any{"status": agentStatusCompleted, "agentId": agent}}

	return []map[string]any{
		{"hook_event_name": "UserPromptSubmit", "session_id": session}, launch, start, pre, post, stop, result,
		{"hook_event_name": "Stop", "session_id": session},
	}
}

// postEditRecording builds a synthetic recording of one case in run 1
// from the payloads. One identity row names the PreToolUse payload. One
// baseline record and one post-edit record name its call. The fixture
// and the indirection lines follow the outcome of the case, and the
// Claude Code debug log shows the hook command on PostToolUse with exit
// status 0. The context file is empty, so the Codex replay logs no
// record.
func postEditRecording(t *testing.T, client string, x postEditCase, payloads []map[string]any) guardRecording {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, guardContextFile), nil, 0o600))

	before := guardState{Files: map[string]string{"sentinel/edit.txt": "a"}}
	after := guardState{Files: map[string]string{"sentinel/edit.txt": "a", x.path: "b"}}

	var lines []string

	switch x.outcome {
	case postEditOutcomeSlowReply:
		lines = []string{postEditSlowStart, postEditSlowDone}
	case postEditOutcomeTimeout:
		lines = []string{postEditTimeoutStart, postEditTimeoutDone}
	case postEditOutcomeFailure:
		after = before
	case postEditOutcomeReply:
	}

	r := guardRecording{
		timingRecording: timingRecording{dir: dir, runs: map[string]int{x.name: 1}, events: map[int][]timingEvent{}},
		cases: map[string]guardCase{x.name: {Run: 1, Case: x.name, Phase: x.phase, Before: before, After: after,
			Audit: [2]int{0, 2}, PostEdit: lines}},
	}
	r.manifest.Client, r.manifest.ClientVersion = client, "synthetic"
	r.manifest.Placeholders.Workspace = "/workspace/native"
	r.events[1] = syntheticEvents(t, 1, payloads)

	file := ""

	for _, e := range r.events[1] {
		if e.hook.Event == "PreToolUse" && e.tool() == x.tool {
			file = e.hook.File
		}
	}

	digest := func(b string) string { return strings.Repeat(b, 32) }
	emitted, status, outcome := true, 0, lessoncheck.ToolSucceeded

	if x.outcome == postEditOutcomeFailure {
		outcome = lessoncheck.ToolFailed
	}

	r.audit = []lessoncheck.AuditRecord{
		{Kind: lessoncheck.AuditBaseline, MatchSHA: digest("ab")},
		{Kind: lessoncheck.AuditPostEdit, MatchSHA: digest("ab"), ToolOutcome: outcome,
			State: lessoncheck.SummaryViolation, Attribution: lessoncheck.AttributionProjectionMatch, Emitted: &emitted},
	}
	r.identities = []guardIdentity{{File: file, Run: 1, Case: x.name, Client: client, Tool: x.tool, Lane: x.lane,
		SessionSHA: digest("cd"), MatchSHA: digest("ab"), ContextSHA: digest("ef")}}

	hook := "verify"
	if x.phase != postEditPhaseCheck {
		hook = postEditHook
	}

	if client == ClaudeID {
		r.hooks = []guardHookResult{{Run: 1, Hook: hook, Event: "PostToolUse", Tool: x.tool, Status: &status, MS: 12034}}
	}

	return r
}

// TestPostEditReplayPassesTheExpectedRuns replays the expected run of
// every case of both tables.
func TestPostEditReplayPassesTheExpectedRuns(t *testing.T) {
	for _, client := range []string{ClaudeID, CodexID} {
		for _, x := range postEditCases(client) {
			t.Run(client+" "+x.name, func(t *testing.T) {
				r := postEditRecording(t, client, x, postEditPayloads(client, x))
				r.replayPostEditCase(t, x)
			})
		}
	}
}

// postEditChange changes the payloads or the recording of an expected run.
type postEditChange struct {
	name     string
	payloads func([]map[string]any) []map[string]any
	record   func(r *guardRecording)
	want     string
}

// setPayload sets one key of one payload.
func setPayload(i int, key string, value any) func([]map[string]any) []map[string]any {
	return func(p []map[string]any) []map[string]any {
		p[i][key] = value

		return p
	}
}

// dropEvent removes the payloads of one event and tool.
func dropEvent(event, tool string) func([]map[string]any) []map[string]any {
	return func(p []map[string]any) []map[string]any {
		return slices.DeleteFunc(p, func(m map[string]any) bool {
			return m["hook_event_name"] == event && m["tool_name"] == tool
		})
	}
}

// setPatch gives every apply_patch payload a new tool input with the
// patch text. The PostToolUse payload is a shallow copy of the PreToolUse
// payload, so a new map keeps the two payloads apart.
func setPatch(text string) func([]map[string]any) []map[string]any {
	return func(p []map[string]any) []map[string]any {
		for _, m := range p {
			if m["tool_name"] == codexApplyPatch {
				m["tool_input"] = map[string]any{"command": text}
			}
		}

		return p
	}
}

// changedRun builds the expected run of a case with one change.
func changedRun(t *testing.T, client string, x postEditCase, change postEditChange) guardRecording {
	t.Helper()

	payloads := postEditPayloads(client, x)
	if change.payloads != nil {
		payloads = change.payloads(payloads)
	}

	r := postEditRecording(t, client, x, payloads)
	if change.record != nil {
		change.record(&r)
	}

	return r
}

// setCase changes the case line of the only case of a recording.
func setCase(r *guardRecording, fn func(c *guardCase)) {
	for name, c := range r.cases {
		fn(&c)
		r.cases[name] = c
	}
}

// TestPostEditReplyRefusesOtherRuns checks the reply rule on a root Edit
// of Claude Code.
func TestPostEditReplyRefusesOtherRuns(t *testing.T) {
	x := postEditCaseRow(t, ClaudeID, "post-edit-edit")

	for _, change := range []postEditChange{
		{name: "the indirection entry logged a line", want: "logged [slow-post start] in the check phase",
			record: func(r *guardRecording) { setCase(r, func(c *guardCase) { c.PostEdit = []string{postEditSlowStart} }) }},
		{name: "two post-edit records", want: "2 post-edit records", record: func(r *guardRecording) {
			r.audit = append(r.audit, r.audit[1])
			setCase(r, func(c *guardCase) { c.Audit = [2]int{0, 3} })
		}},
		{name: "another attribution", want: "want a succeeded tool", record: func(r *guardRecording) {
			r.audit[1].Attribution = lessoncheck.AttributionNoBaseline
		}},
		{name: "no reply", want: "want a succeeded tool", record: func(r *guardRecording) {
			r.audit[1].Emitted = nil
		}},
		{name: "no baseline", want: "0 baseline records", record: func(r *guardRecording) {
			r.audit[0].MatchSHA = strings.Repeat("99", 32)
		}},
		{name: "no identity row", want: "0 identity rows", record: func(r *guardRecording) { r.identities = nil }},
		{name: "a repeated call identity", want: "repeats the call identity", record: func(r *guardRecording) {
			r.identities = append(r.identities, guardIdentity{File: "hooks/009-PreToolUse.json", Client: ClaudeID,
				MatchSHA: r.identities[0].MatchSHA})
		}},
		{name: "an identity row of another lane", want: "does not match", record: func(r *guardRecording) {
			r.identities[0].Lane = LaneChild
		}},
		{name: "an unchanged file", want: "did not change sentinel/edit.txt", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.After = c.Before })
		}},
		{name: "no verify result", want: "no verify command", record: func(r *guardRecording) { r.hooks = nil }},
		{name: "no PostToolUse", want: "the post-edit hook did not run", payloads: dropEvent("PostToolUse", claudeToolEdit)},
		{name: "a retried call", want: "retried the call", payloads: func(p []map[string]any) []map[string]any {
			return slices.Insert(p, 2, maps.Clone(p[1]))
		}},
		{name: "an agent in a root call", want: "names an agent", payloads: setPayload(1, "agent_id", "agent-9")},
		{name: "another tool call", want: "another tool call", payloads: setPayload(2, "tool_use_id", "call-2")},
		{name: "no prompt before the call", want: "no UserPromptSubmit", payloads: func(p []map[string]any) []map[string]any {
			return p[1:]
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			_, err := changedRun(t, ClaudeID, x, change).postEditReply(x)
			require.Error(t, err)
			assert.Contains(t, err.Error(), change.want)
		})
	}
}

// TestPostEditSlowReplyRefusesOtherRuns checks the slow rule on a Claude
// Code child Write.
func TestPostEditSlowReplyRefusesOtherRuns(t *testing.T) {
	x := postEditCaseRow(t, ClaudeID, "slow-post-subagent-write")

	for _, change := range []postEditChange{
		{name: "the client stopped the hook", want: "not observed", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.PostEdit = []string{postEditSlowStart} })
		}},
		{name: "the hook ran twice", want: "not observed", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) {
				c.PostEdit = []string{postEditSlowStart, postEditSlowDone, postEditSlowStart, postEditSlowDone}
			})
		}},
		{name: "the owned verify entry ran too", want: "2 post-edit records", record: func(r *guardRecording) {
			r.audit = append(r.audit, r.audit[1])
			setCase(r, func(c *guardCase) { c.Audit = [2]int{0, 3} })
		}},
		{name: "no post-edit result", want: "no post-edit command", record: func(r *guardRecording) {
			r.hooks[0].Hook = "verify"
		}},
		{name: "a child call of another agent", want: "names no agent of a SubagentStart",
			payloads: setPayload(3, "agent_id", "agent-9")},
		{name: "a child call without an agent", want: "names no agent of a SubagentStart",
			payloads: func(p []map[string]any) []map[string]any {
				delete(p[3], "agent_id")
				delete(p[4], "agent_id")

				return p
			}},
		{name: "a PostToolUse of another agent", want: "another agent", payloads: setPayload(4, "agent_id", "agent-9")},
	} {
		t.Run(change.name, func(t *testing.T) {
			_, err := changedRun(t, ClaudeID, x, change).postEditSlowReply(x)
			require.Error(t, err)
			assert.Contains(t, err.Error(), change.want)
		})
	}
}

// TestPostEditTimeoutReadsBothAnswers checks the timeout rule on the root
// patch of Codex. A stopped hook and a hook that ran to the end both
// pass, and each gives its own note.
func TestPostEditTimeoutReadsBothAnswers(t *testing.T) {
	x := postEditCaseRow(t, CodexID, "post-timeout-add")

	stopped := func(r *guardRecording) {
		setCase(r, func(c *guardCase) { c.PostEdit = []string{postEditTimeoutStart} })
		r.audit = r.audit[:1]
		setCase(r, func(c *guardCase) { c.Audit = [2]int{0, 1} })
	}

	note, err := changedRun(t, CodexID, x, postEditChange{record: stopped}).postEditTimeout(x)
	require.NoError(t, err)
	assert.Equal(t, "native post-edit timeout of codex synthetic: the client stopped the hook before the verify command: true", note)

	note, err = changedRun(t, CodexID, x, postEditChange{}).postEditTimeout(x)
	require.NoError(t, err)
	assert.Contains(t, note, "the client stopped the hook before the verify command: false")

	for _, change := range []postEditChange{
		{name: "a record without an exit status", want: "logged no exit status", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.PostEdit = []string{postEditTimeoutStart} })
		}},
		{name: "no start line", want: "not a start", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.PostEdit = nil })
		}},
		{name: "a failed verify command", want: "not a start", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.PostEdit = []string{postEditTimeoutStart, "post-timeout 1"} })
		}},
		{name: "no new file", want: "did not create sentinel/post-timeout.txt", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) { c.After = c.Before })
		}},
		{name: "no PostToolUse", want: "the post-edit hook did not run", payloads: dropEvent("PostToolUse", codexApplyPatch)},
	} {
		t.Run(change.name, func(t *testing.T) {
			_, err := changedRun(t, CodexID, x, change).postEditTimeout(x)
			require.Error(t, err)
			assert.Contains(t, err.Error(), change.want)
		})
	}
}

// TestPostEditFailureReadsTheFailedPatch checks the failure rule of the
// Codex patch into the read-only directory.
func TestPostEditFailureReadsTheFailedPatch(t *testing.T) {
	x := postEditCaseRow(t, CodexID, guardExecutionFailure)

	note, skip, err := changedRun(t, CodexID, x, postEditChange{}).postEditFailure(x)
	require.NoError(t, err)
	assert.False(t, skip)
	assert.Equal(t, "case execution-failure: codex synthetic ran the post-edit hook after the failed patch; the record "+
		"reads the outcome failed, the attribution projection-match, the state violation, a written reply true "+
		"(1 baseline records)", note)

	note, skip, err = changedRun(t, CodexID, x, postEditChange{payloads: dropEvent("PreToolUse", codexApplyPatch)}).
		postEditFailure(x)
	require.NoError(t, err)
	assert.True(t, skip, "a run without the call gives no evidence")
	assert.Contains(t, note, "the client refused the patch before any hook, or the model made no call")

	note, skip, err = changedRun(t, CodexID, x, postEditChange{payloads: dropEvent("PostToolUse", codexApplyPatch)}).
		postEditFailure(x)
	require.NoError(t, err)
	assert.True(t, skip)
	assert.Equal(t, "case execution-failure: codex synthetic sent no PostToolUse after the failed patch; a hook after a "+
		"failed patch stays unsupported on that version", note)

	// A tool response without an exit status gives the outcome unknown.
	// The note logs it, because only a succeeded tool contradicts the
	// fixture.
	unknown := func(r *guardRecording) { r.audit[1].ToolOutcome = lessoncheck.ToolUnknown }
	note, skip, err = changedRun(t, CodexID, x, postEditChange{record: unknown}).postEditFailure(x)
	require.NoError(t, err)
	assert.False(t, skip)
	assert.Contains(t, note, "the record reads the outcome unknown,")

	elsewhere := setPatch("*** Begin Patch\n*** Add File: elsewhere.txt\n+slow\n*** End Patch\n")

	for _, change := range []postEditChange{
		{name: "a succeeded tool", want: "reads the outcome succeeded, but the patch did not create sentinel/locked/new.txt",
			record: func(r *guardRecording) { r.audit[1].ToolOutcome = lessoncheck.ToolSucceeded }},
		{name: "a succeeded patch at another path", want: "the call names [elsewhere.txt], not sentinel/locked/new.txt",
			payloads: elsewhere, record: func(r *guardRecording) { r.audit[1].ToolOutcome = lessoncheck.ToolSucceeded }},
		{name: "a patch at another path without PostToolUse", want: "the call names [elsewhere.txt]",
			payloads: func(p []map[string]any) []map[string]any {
				return dropEvent("PostToolUse", codexApplyPatch)(elsewhere(p))
			}},
		{name: "a patch at a second path", want: "the call names [sentinel/locked/new.txt free/other.txt]",
			payloads: setPatch("*** Begin Patch\n*** Add File: sentinel/locked/new.txt\n+slow\n" +
				"*** Add File: free/other.txt\n+slow\n*** End Patch\n")},
		{name: "a payload without a patch", want: "the PreToolUse payload does not decode",
			payloads: setPayload(1, "tool_input", map[string]any{})},
		{name: "the patch wrote the file", want: "sentinel/locked was not read-only", record: func(r *guardRecording) {
			setCase(r, func(c *guardCase) {
				c.After = guardState{Files: map[string]string{"sentinel/edit.txt": "a", x.path: "b"}}
			})
		}},
		{name: "no post-edit record", want: "0 post-edit records", record: func(r *guardRecording) {
			r.audit = r.audit[:1]
			setCase(r, func(c *guardCase) { c.Audit = [2]int{0, 1} })
		}},
		{name: "a retried patch", want: "retried the call", payloads: func(p []map[string]any) []map[string]any {
			return slices.Insert(p, 2, maps.Clone(p[1]))
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			_, _, err := changedRun(t, CodexID, x, change).postEditFailure(x)
			require.Error(t, err)
			assert.Contains(t, err.Error(), change.want)
		})
	}
}
