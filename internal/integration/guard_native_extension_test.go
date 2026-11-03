package integration

// The extension cases of the native guard scenario. They come after the
// thirteen first cases of each client in scripts/capture-native.sh:
// child denies, a child post-edit reply, and an in-budget slow deny per
// tool and lane. The table below gives the expected outcome of each
// case, so a later recording replays offline against it. A recording
// from before the extension holds none of these cases, and its replay
// skips them as pending.

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// guardPhaseSlowDeny is the phase of the in-budget slow cases. The
// indirection entry waits inside the hook timeout and then runs the
// guard, so a client that waits for its hook gets the deny.
const guardPhaseSlowDeny = "slow-deny"

// guardSlowDenyLine is the line that the indirection entry logs when the
// guard ran to the end of an in-budget wait and denied.
const guardSlowDenyLine = "slow-deny 2"

// guardOutcome is the expected outcome of an extension case.
type guardOutcome string

const (
	// guardOutcomeDeny: the guard denies the first attempt, and the run
	// changes no fixture file.
	guardOutcomeDeny guardOutcome = "deny"
	// guardOutcomeSlowDeny: the client waits for the slow indirection
	// entry, the guard denies, and the write does not happen.
	guardOutcomeSlowDeny guardOutcome = "slow-deny"
	// guardOutcomeReply: check pins allow the write, and the post-edit
	// hook reports the violation and writes its reply.
	guardOutcomeReply guardOutcome = "post-edit-reply"
)

// guardExtension is one row of the scenario table of the extension.
type guardExtension struct {
	name    string
	phase   string
	tool    string
	lane    string
	path    string
	outcome guardOutcome
}

// The extension cases of each client, in the order of the script.
var (
	claudeGuardExtensions = []guardExtension{
		{"subagent-edit-violation", "enforce", claudeToolEdit, LaneChild, "sentinel/edit.txt", guardOutcomeDeny},
		{"subagent-multiedit-violation", "enforce", claudeToolMultiEdit, LaneChild, "notes/multi.txt", guardOutcomeDeny},
		{"subagent-check-only", "check-pins", claudeToolWrite, LaneChild, "sentinel/child-checked.txt", guardOutcomeReply},
		{"slow-deny-write", guardPhaseSlowDeny, claudeToolWrite, LaneRoot, "sentinel/slow-deny.txt", guardOutcomeSlowDeny},
		{"slow-deny-edit", guardPhaseSlowDeny, claudeToolEdit, LaneRoot, "sentinel/edit.txt", guardOutcomeSlowDeny},
		{"slow-deny-multiedit", guardPhaseSlowDeny, claudeToolMultiEdit, LaneRoot, "notes/multi.txt", guardOutcomeSlowDeny},
		{"slow-deny-subagent-write", guardPhaseSlowDeny, claudeToolWrite, LaneChild, "sentinel/slow-child.txt", guardOutcomeSlowDeny},
		{"slow-deny-subagent-edit", guardPhaseSlowDeny, claudeToolEdit, LaneChild, "sentinel/edit.txt", guardOutcomeSlowDeny},
		{"slow-deny-subagent-multiedit", guardPhaseSlowDeny, claudeToolMultiEdit, LaneChild, "notes/multi.txt", guardOutcomeSlowDeny},
	}
	codexGuardExtensions = []guardExtension{
		{"subagent-check-only", "check-pins", codexApplyPatch, LaneChild, "sentinel/child-checked.txt", guardOutcomeReply},
		{"slow-deny-add", guardPhaseSlowDeny, codexApplyPatch, LaneRoot, "sentinel/slow-deny.txt", guardOutcomeSlowDeny},
		{"slow-deny-subagent-add", guardPhaseSlowDeny, codexApplyPatch, LaneChild, "sentinel/slow-child.txt", guardOutcomeSlowDeny},
	}
)

// guardExtensions returns the extension table of a client.
func guardExtensions(client string) []guardExtension {
	if client == CodexID {
		return codexGuardExtensions
	}

	return claudeGuardExtensions
}

// guardExtensionNames returns the case names of the extension of a
// client, in the order of the script.
func guardExtensionNames(client string) []string {
	var names []string

	for _, x := range guardExtensions(client) {
		names = append(names, x.name)
	}

	return names
}

// loadGuardExtension reads the reviewed guard recording of a client for
// the replay of its extension cases, or skips.
func loadGuardExtension(t *testing.T, client string, base []string) guardRecording {
	t.Helper()

	r := loadGuardRecording(t, client, base...)
	if _, ok := r.cases[guardExtensions(client)[0].name]; !ok {
		t.Skipf("pending: the guard recording of %s %s predates the extension cases; record again with the current "+
			"scripts/capture-native.sh", client, r.manifest.ClientVersion)
	}

	return r
}

// replayExtension runs the subtest of each extension case of a client.
func (r guardRecording) replayExtension(t *testing.T, client string) {
	for _, x := range guardExtensions(client) {
		t.Run(x.name, func(t *testing.T) { r.replayExtensionCase(t, client, x) })
	}
}

// replayExtensionCase checks one extension case against its row of the
// scenario table.
func (r guardRecording) replayExtensionCase(t *testing.T, client string, x guardExtension) {
	c := r.cases[x.name]
	require.Equal(t, x.phase, c.Phase, "the script ran the case in its phase")

	if x.tool == claudeToolMultiEdit && !r.has(x.name, "PreToolUse", x.tool) {
		require.NoError(t, r.noMultiEditTool(x),
			"case %s: no MultiEdit payload: the model did not follow the prompt, so record again", x.name)
		t.Skipf("Claude Code %s offered no MultiEdit tool in the %s lane; the MultiEdit path keeps no native evidence",
			r.manifest.ClientVersion, x.lane)
	}

	switch x.outcome {
	case guardOutcomeDeny:
		r.assertExtensionDeny(t, client, x)
	case guardOutcomeSlowDeny:
		r.assertExtensionSlowDeny(t, client, x)
	case guardOutcomeReply:
		r.assertExtensionReply(t, client, x)
	default:
		require.Failf(t, "unknown outcome", "%s", x.outcome)
	}
}

// guardNoMultiEdit is the reply word of a model that has no MultiEdit
// tool.
const guardNoMultiEdit = "no-multiedit"

// guardReplyWord matches a reply that the script keeps: one word.
var guardReplyWord = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// replyWord normalizes a reply as the script does for the reply of the
// parent. It trims white space, then periods, and sets lower case. Text
// that is not one word gives "".
func replyWord(s string) string {
	w := strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
	if !guardReplyWord.MatchString(w) {
		return ""
	}

	return w
}

// noMultiEditTool returns nil when a MultiEdit case without a MultiEdit
// payload shows a client without the tool. No write ran: no payload of a
// guarded tool, no line of the indirection entry, and no change to the
// fixture. Also, the model that held the tool list replied no-multiedit.
// In a child case that model is the subagent, not the parent.
func (r guardRecording) noMultiEditTool(x guardExtension) error {
	c := r.cases[x.name]

	for _, e := range r.events[r.runs[x.name]] {
		if e.hook.Event == "PreToolUse" && slices.Contains(claudeGuardTools, e.tool()) {
			return fmt.Errorf("a %s call ran instead", e.tool())
		}
	}

	if len(c.PreWrite) > 0 {
		return fmt.Errorf("the indirection entry logged %v", c.PreWrite)
	}

	if !c.unchanged() {
		return fmt.Errorf("the fixture changed: before %v, after %v", c.Before, c.After)
	}

	who, reply := "the parent", c.Reply

	if x.lane == LaneChild {
		var ok bool

		who = "the subagent"
		if reply, ok = r.childReply(x.name); !ok {
			return errors.New("no SubagentStop payload names the agent of SubagentStart")
		}
	}

	if reply != guardNoMultiEdit {
		return fmt.Errorf("%s replied %q, not %s", who, reply, guardNoMultiEdit)
	}

	return nil
}

// childReply returns the last message of the first subagent of a case,
// as replyWord gives it. It reads the SubagentStop payload of the agent
// that the first SubagentStart names. The parent gets only that message,
// and it can reword it when it relays it. So the reply of the parent
// does not decide a child case.
func (r guardRecording) childReply(name string) (string, bool) {
	agent := ""

	for _, e := range r.events[r.runs[name]] {
		switch {
		case e.hook.Event == "SubagentStart" && agent == "":
			agent = nativeString(e.raw, "agent_id")
		case e.hook.Event == "SubagentStop" && agent != "" && nativeString(e.raw, "agent_id") == agent:
			return replyWord(nativeString(e.raw, "last_assistant_message")), true
		}
	}

	return "", false
}

// extensionCall returns the pre-write payload of an extension case. A
// child case must show SubagentStart before it, and the payload must
// name the agent of SubagentStart. A root case names no agent.
func (r guardRecording) extensionCall(t *testing.T, x guardExtension, rec lessoncheck.AuditRecord) timingEvent {
	t.Helper()

	if x.lane == LaneRoot {
		e := r.sequence(t, x.name, "UserPromptSubmit", "PreToolUse:"+x.tool)[1]
		assert.Empty(t, nativeString(e.raw, "agent_id"), "case %s: the parent made the call", x.name)

		return e
	}

	steps := r.sequence(t, x.name, "UserPromptSubmit", "SubagentStart", "PreToolUse:"+x.tool)
	assertChild(t, r.manifest.Client, steps[1], steps[2], rec)

	return steps[2]
}

// assertExtensionOp checks the decoded operation of the call: its path
// and, for MultiEdit, both steps in one call.
func (r guardRecording) assertExtensionOp(t *testing.T, client string, x guardExtension, e timingEvent) {
	t.Helper()

	op := guardOp(t, client, e)
	assert.Equal(t, []string{x.path}, r.opPaths(op), "case %s: the call names its fixture file", x.name)

	if x.tool == claudeToolMultiEdit {
		require.Len(t, op.Ops, 1)
		assert.Equal(t, lessoncheck.OpMultiEdit, op.Ops[0].Kind)
		assert.Len(t, op.Ops[0].Edits, 2, "case %s: one call holds the permitted and the violating step", x.name)
	}
}

// assertExtensionDeny checks a child deny: the blocking facts of
// assertDenied, the child lane, and the guard command status 2.
func (r guardRecording) assertExtensionDeny(t *testing.T, client string, x guardExtension) {
	t.Helper()

	e, deny := r.assertDenied(t, x.name, x.tool)
	assert.Equal(t, e.hook.File, r.extensionCall(t, x, deny).hook.File, "case %s: the denied call is the first call", x.name)
	r.assertExtensionOp(t, client, x, e)
	assert.Equal(t, lessoncheck.GuardCauseViolation, deny.Guard.Cause, x.name)
	r.assertHookStatus(t, x.name, "guard", 2)
}

// assertExtensionSlowDeny checks an in-budget slow deny. The case is
// observed only when the client waited for the indirection entry, so the
// guard ran to the end and denied, and the write did not happen.
func (r guardRecording) assertExtensionSlowDeny(t *testing.T, client string, x guardExtension) {
	t.Helper()

	c := r.cases[x.name]
	require.True(t, slowDenyObserved(c),
		"case %s: not observed: the indirection entry logged %v instead of one guard deny, or the write happened: "+
			"before %v, after %v", x.name, c.PreWrite, c.Before, c.After)

	e, deny := r.assertDenied(t, x.name, x.tool)
	assert.Equal(t, e.hook.File, r.extensionCall(t, x, deny).hook.File, "case %s: the denied call is the first call", x.name)
	r.assertExtensionOp(t, client, x, e)
	assert.Equal(t, lessoncheck.GuardCauseViolation, deny.Guard.Cause, x.name)
	assert.False(t, r.has(x.name, "PostToolUse", x.tool),
		"case %s: a PostToolUse event of the tool shows that the tool ran after the slow deny", x.name)
	r.assertHookStatus(t, x.name, "pre-write", 2)
}

// assertExtensionReply checks a child post-edit reply: the guard allows
// the write under check pins, and the post-edit record of the same call
// reports the violation and writes its reply. Whether the reply reached
// the child model request is the reviewer's reading of the subagent
// transcript.
func (r guardRecording) assertExtensionReply(t *testing.T, client string, x guardExtension) {
	t.Helper()

	r.sequence(t, x.name, "UserPromptSubmit", "SubagentStart", "PreToolUse:"+x.tool, "PostToolUse:"+x.tool)

	allow, ok := r.guardRecord(t, x.name, x.tool)
	require.True(t, ok, "case %s: the guard wrote a record of the call", x.name)
	assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision, "case %s: check-mode findings cannot deny", x.name)

	r.assertExtensionOp(t, client, x, r.extensionCall(t, x, allow))

	attributions := []lessoncheck.Attribution{lessoncheck.AttributionProjectionMatch}
	if client == CodexID {
		attributions = codexAddAttributions
	}

	post := r.assertExecuted(t, x.name, allow, x.path, lessoncheck.SummaryViolation, attributions...)
	assertEmitted(t, x.name, post)
}

// slowDenyObserved reports whether an in-budget slow case shows its
// outcome. The indirection entry must log exactly one guard deny, so the
// client waited for the whole hook. The fixture must stay unchanged, so
// the write did not happen. A client that stops the hook early logs
// nothing, and a second call logs a second line.
func slowDenyObserved(c guardCase) bool {
	return slices.Equal(c.PreWrite, []string{guardSlowDenyLine}) && c.unchanged()
}

// TestSlowDenyObserved checks the observation rule of the in-budget
// slow cases on synthetic case lines. They give no native proof.
func TestSlowDenyObserved(t *testing.T) {
	same := guardState{Files: map[string]string{"sentinel/edit.txt": "a"}}
	written := guardState{Files: map[string]string{"sentinel/edit.txt": "a", "sentinel/slow-deny.txt": "b"},
		Status: []string{"?? sentinel/slow-deny.txt"}}

	for _, tc := range []struct {
		name     string
		preWrite []string
		after    guardState
		want     bool
	}{
		{"the client waited and the guard denied", []string{guardSlowDenyLine}, same, true},
		{"the client stopped the hook early", nil, same, false},
		{"the guard allowed", []string{"slow-deny 0"}, same, false},
		{"the slow-guard line is another mode", []string{"slow 2"}, same, false},
		{"two calls ran the guard", []string{guardSlowDenyLine, guardSlowDenyLine}, same, false},
		{"the write happened after the deny", []string{guardSlowDenyLine}, written, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := guardCase{Phase: guardPhaseSlowDeny, Before: same, After: tc.after, PreWrite: tc.preWrite}
			assert.Equal(t, tc.want, slowDenyObserved(c))
		})
	}
}

// guardExtensionRow returns the row of one extension case of a client.
func guardExtensionRow(t *testing.T, client, name string) guardExtension {
	t.Helper()

	i := slices.IndexFunc(guardExtensions(client), func(x guardExtension) bool { return x.name == name })
	require.GreaterOrEqual(t, i, 0, "the table of %s has the case %s", client, name)

	return guardExtensions(client)[i]
}

// multiEditRecording builds a synthetic Claude Code recording of one
// MultiEdit case in run 1, with the reply of the parent and the hook
// payloads in recorded order. The fixture stays unchanged, and the
// indirection entry logs nothing.
func multiEditRecording(x guardExtension, reply string, payloads []map[string]any) guardRecording {
	r := guardRecording{
		timingRecording: timingRecording{runs: map[string]int{x.name: 1}, events: map[int][]timingEvent{}},
		cases:           map[string]guardCase{},
	}
	r.manifest.Client = ClaudeID
	r.manifest.ClientVersion = "synthetic"

	state := guardState{Files: map[string]string{"notes/multi.txt": "a"}}
	r.cases[x.name] = guardCase{Run: 1, Case: x.name, Phase: x.phase, Before: state, After: state, Reply: reply}

	for i, raw := range payloads {
		event, _ := raw["hook_event_name"].(string)
		hook := nativeHook{File: fmt.Sprintf("hooks/%03d-%s.json", i+1, event), Run: 1, Event: event}
		r.events[1] = append(r.events[1], timingEvent{hook: hook, seq: i, raw: raw})
	}

	return r
}

// childMultiEditPayloads gives the payloads of a child MultiEdit case
// with no MultiEdit call, in the order of a background subagent. The
// parent turn stops before the subagent works. The subagent reads the
// file and stops with its reply. Then the reply reaches the parent as a
// new prompt. Each extra payload comes after the read of the subagent.
func childMultiEditPayloads(childReply string, extra ...map[string]any) []map[string]any {
	child := func(event, tool string) map[string]any {
		return map[string]any{"hook_event_name": event, "tool_name": tool, "agent_id": "agent-1"}
	}

	payloads := []map[string]any{
		{"hook_event_name": "UserPromptSubmit"},
		{"hook_event_name": "PreToolUse", "tool_name": "Agent"},
		{"hook_event_name": "PostToolUse", "tool_name": "Agent"},
		{"hook_event_name": "SubagentStart", "agent_id": "agent-1"},
		child("PreToolUse", "Read"),
		child("PostToolUse", "Read"),
	}
	payloads = append(payloads, extra...)

	return append(payloads,
		map[string]any{"hook_event_name": "Stop"},
		map[string]any{"hook_event_name": "SubagentStop", "agent_id": "agent-1", "last_assistant_message": childReply},
		map[string]any{"hook_event_name": "UserPromptSubmit"},
		map[string]any{"hook_event_name": "Stop"},
	)
}

// TestNoMultiEditToolReadsTheModelThatHeldTheTools checks the skip rule
// of the MultiEdit cases on synthetic recordings. They give no native
// proof. In a child case the subagent holds the tool list, so its own
// reply decides. The parent only relays that reply and can reword it.
func TestNoMultiEditToolReadsTheModelThatHeldTheTools(t *testing.T) {
	root := []map[string]any{
		{"hook_event_name": "UserPromptSubmit"},
		{"hook_event_name": "PreToolUse", "tool_name": "Read"},
		{"hook_event_name": "PostToolUse", "tool_name": "Read"},
		{"hook_event_name": "Stop"},
	}
	otherAgent := childMultiEditPayloads(guardNoMultiEdit)
	otherAgent[len(otherAgent)-3] = map[string]any{
		"hook_event_name": "SubagentStop", "agent_id": "agent-2", "last_assistant_message": guardNoMultiEdit,
	}
	childEdit := map[string]any{"hook_event_name": "PreToolUse", "tool_name": claudeToolEdit, "agent_id": "agent-1"}

	for _, tc := range []struct {
		name     string
		row      string
		reply    string
		payloads []map[string]any
		preWrite []string
		written  bool
		ok       bool
	}{
		{"the child has no tool and the parent rewords its reply", "subagent-multiedit-violation", "",
			childMultiEditPayloads(guardNoMultiEdit), nil, false, true},
		{"the child reply gives its word without spaces, capitals, or a period", "slow-deny-subagent-multiedit", "",
			childMultiEditPayloads(" No-MultiEdit. "), nil, false, true},
		{"the parent relays the word that the child did not reply", "subagent-multiedit-violation", guardNoMultiEdit,
			childMultiEditPayloads("done"), nil, false, false},
		{"the child replied a sentence", "subagent-multiedit-violation", guardNoMultiEdit,
			childMultiEditPayloads("I have no MultiEdit tool."), nil, false, false},
		{"only another agent stopped", "subagent-multiedit-violation", guardNoMultiEdit,
			otherAgent, nil, false, false},
		{"the child made an Edit call", "subagent-multiedit-violation", guardNoMultiEdit,
			childMultiEditPayloads(guardNoMultiEdit, childEdit), nil, false, false},
		{"the child changed the fixture", "subagent-multiedit-violation", guardNoMultiEdit,
			childMultiEditPayloads(guardNoMultiEdit), nil, true, false},
		{"the indirection entry ran the guard", "slow-deny-subagent-multiedit", guardNoMultiEdit,
			childMultiEditPayloads(guardNoMultiEdit), []string{guardSlowDenyLine}, false, false},
		{"a root case reads the reply of the parent", "slow-deny-multiedit", guardNoMultiEdit,
			root, nil, false, true},
		{"a root case with another reply", "slow-deny-multiedit", "done",
			root, nil, false, false},
		{"a root case that changed the fixture", "slow-deny-multiedit", guardNoMultiEdit,
			root, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := guardExtensionRow(t, ClaudeID, tc.row)
			r := multiEditRecording(x, tc.reply, tc.payloads)

			c := r.cases[x.name]
			c.PreWrite = tc.preWrite
			if tc.written {
				c.After = guardState{Files: map[string]string{"notes/multi.txt": "b"}}
			}
			r.cases[x.name] = c

			err := r.noMultiEditTool(x)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestGuardExtensionSkipsAChildWithoutMultiEdit replays the child
// MultiEdit cases of a client without the tool. The subagent replies
// no-multiedit, and the parent gives no one-word reply, as the one
// child guard run of Claude Code 2.1.314 did. Each case must skip with
// its reason and not ask for a new recording.
func TestGuardExtensionSkipsAChildWithoutMultiEdit(t *testing.T) {
	for _, name := range []string{"subagent-multiedit-violation", "slow-deny-subagent-multiedit"} {
		x := guardExtensionRow(t, ClaudeID, name)
		r := multiEditRecording(x, "", childMultiEditPayloads(guardNoMultiEdit))

		var replay *testing.T

		t.Run(name, func(t *testing.T) {
			replay = t
			r.replayExtensionCase(t, ClaudeID, x)
		})

		assert.True(t, replay.Skipped(), "case %s: the replay skips and checks no deny", name)
	}
}

// assertHookStatus checks the exit status of one hook command in the
// Claude Code debug log of a case. A recording without any hook result
// (a Codex recording) skips the check with a note, as assertGuardStatus
// does.
func (r guardRecording) assertHookStatus(t *testing.T, name, hook string, status int) {
	t.Helper()

	if len(r.hooks) == 0 {
		t.Logf("case %s: the recording holds no hook result; read run-%d-debug.log for the exit status", name, r.runs[name])

		return
	}

	assert.True(t, slices.ContainsFunc(r.hooks, func(h guardHookResult) bool {
		return h.Run == r.runs[name] && h.Hook == hook && h.Status != nil && *h.Status == status
	}), "case %s: the debug log shows the %s command with exit status %d", name, hook, status)
}

func TestCaptureNativeClaudeGuardExtension(t *testing.T) {
	loadGuardExtension(t, ClaudeID, claudeGuardCases).replayExtension(t, ClaudeID)
}

func TestCaptureNativeCodexGuardExtension(t *testing.T) {
	loadGuardExtension(t, CodexID, codexGuardCases).replayExtension(t, CodexID)
}

// guardRunLine matches one guard_run call of the script: its phase and
// its case.
var guardRunLine = regexp.MustCompile(`(?m)^\s*guard_run (\S+) (\S+) "`)

// TestGuardScenarioScriptMatchesTheTable proves that the script runs the
// first cases of each client in their old order, then the extension
// cases in the order and phase of the table. So an earlier recording
// keeps its run numbers, and a new recording replays against the table.
func TestGuardScenarioScriptMatchesTheTable(t *testing.T) {
	script := captureNativeScript(t)

	for _, tc := range []struct {
		client, function string
		base             []string
	}{
		{ClaudeID, "record_claude_guard", claudeGuardCases},
		{CodexID, "record_codex_guard", codexGuardCases},
	} {
		t.Run(tc.client, func(t *testing.T) {
			var names, phases []string

			for _, m := range guardRunLine.FindAllStringSubmatch(scriptFunction(t, script, tc.function), -1) {
				phases = append(phases, m[1])
				names = append(names, m[2])
			}

			assert.Equal(t, slices.Concat(tc.base, guardExtensionNames(tc.client)), names)

			for i, x := range guardExtensions(tc.client) {
				j := len(tc.base) + i
				if assert.Less(t, j, len(phases)) {
					assert.Equal(t, x.phase, phases[j], "case %s", x.name)
				}
			}
		})
	}
}

// TestGuardScenarioSlowDenyWaitsInsideTheTimeout proves that the wait of
// the slow-deny mode stays below the timeout of the indirection entry,
// and that the slow wait stays above it.
func TestGuardScenarioSlowDenyWaitsInsideTheTimeout(t *testing.T) {
	script := captureNativeScript(t)

	value := func(prefix string) int {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(scriptLine(t, script, prefix), prefix)))
		require.NoError(t, err, prefix)

		return n
	}

	timeout := regexp.MustCompile(`"command": prewrite, "timeout": (\d+)\}`).FindStringSubmatch(script)
	require.Len(t, timeout, 2, "the script gives the indirection entry a timeout")

	hook, err := strconv.Atoi(timeout[1])
	require.NoError(t, err)

	assert.Equal(t, 7, value("GUARD_IN_BUDGET_SECONDS="))
	assert.Less(t, value("GUARD_IN_BUDGET_SECONDS="), hook, "the in-budget wait ends before the hook timeout")
	assert.Greater(t, value("GUARD_SLOW_SECONDS="), hook, "the slow wait ends after the hook timeout")
}

// TestGuardScenarioIndirectionModes runs the indirection entry that
// guard_prewrite_script writes, with a stand-in guard that exits 2 and
// no wait. Each mode logs its own line, so the replay can tell the
// in-budget deny from the slow and the report-only runs.
func TestGuardScenarioIndirectionModes(t *testing.T) {
	requireTools(t, "sh", "cat", "sleep")

	script := captureNativeScript(t)
	instr := t.TempDir()
	guard := filepath.Join(instr, "guard")

	program := scriptFunction(t, script, "guard_prewrite_script") +
		"printf '#!/bin/sh\\nexit 2\\n' > \"$BIN\"\nchmod +x \"$BIN\"\nguard_prewrite_script\n"
	setup := exec.Command("sh", "-eu", "-c", program)
	setup.Env = []string{"PATH=/usr/bin:/bin", "INSTR=" + instr, "BIN=" + guard, "GUARD_MARKER=lessons --guard --hook",
		"GUARD_SLOW_SECONDS=0", "GUARD_IN_BUDGET_SECONDS=0"}
	got, err := setup.CombinedOutput()
	require.NoError(t, err, string(got))

	for _, tc := range []struct {
		mode   string
		status int
		log    string
	}{
		{"off", 0, ""},
		{"report-only", 0, "report-only 2"},
		{"slow", 2, "slow 2"},
		{guardPhaseSlowDeny, 2, guardSlowDenyLine},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			run := exec.Command("sh", "-c", "rm -f \"$INSTR/pre-write.log\"\n"+
				"printf '%s\\n' \"$MODE\" > \"$INSTR/pre-write-mode\"\n"+
				"status=0\nsh \"$INSTR/pre-write.sh\" || status=$?\n"+
				"echo \"status=$status\"\ncat \"$INSTR/pre-write.log\" 2>/dev/null || true\n")
			run.Env = []string{"PATH=/usr/bin:/bin", "INSTR=" + instr, "MODE=" + tc.mode}
			out, err := run.CombinedOutput()
			require.NoError(t, err, string(out))

			want := "status=" + strconv.Itoa(tc.status) + "\n"
			if tc.log != "" {
				want += tc.log + "\n"
			}

			assert.Equal(t, want, string(out))
		})
	}
}

// TestGuardScenarioSlowDenyPhase runs guard_phase with stand-ins for the
// hook document and the capability report. The slow-deny phase removes
// the owned guard entry and sets the slow-deny mode, as the slow phase
// does with its own mode.
func TestGuardScenarioSlowDenyPhase(t *testing.T) {
	requireTools(t, "sh", "cat")

	script := captureNativeScript(t)
	instr := t.TempDir()

	stubs := "guard_restore() { echo restore; }\nguard_doc() { echo \"doc $1\"; }\nguard_capability() { echo \"capability $1\"; }\n"
	program := stubs + scriptFunction(t, script, "guard_phase") + "guard_phase slow-deny\nguard_phase slow-deny\n" +
		"cat \"$INSTR/pre-write-mode\"\n"

	cmd := exec.Command("sh", "-eu", "-c", program)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "INSTR=" + instr}
	got, err := cmd.CombinedOutput()
	require.NoError(t, err, string(got))

	assert.Equal(t, "restore\ndoc no-guard\ncapability slow-deny\nslow-deny\n", string(got),
		"the second call in the same phase changes nothing")
}
