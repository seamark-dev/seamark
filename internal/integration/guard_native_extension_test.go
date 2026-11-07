package integration

// The extension cases of the native guard scenario. They come after the
// thirteen first cases of each client in scripts/native-record.sh:
// child denies, a child post-edit reply, an in-budget slow deny per tool
// and lane, and for Claude Code a write that fails during its execution.
// The table below gives the expected outcome of each case, so a later
// recording replays offline against it. A recording from before the
// extension holds none of these cases, and its replay skips them as
// pending.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
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
	// guardOutcomeFailure: no pre-write hook runs, the write fails during
	// its execution, and the post-edit hook replies to PostToolUseFailure.
	guardOutcomeFailure guardOutcome = "post-edit-failure"
)

// guardExecutionFailure is the case whose Write fails during its
// execution, in the read-only directory that guard_fixture makes.
const guardExecutionFailure = "execution-failure"

// guardExtension is one row of the scenario table of the extension. The
// shape is the launch shape that the prompt of a Claude Code child case
// sets. It is empty for a root case and for each Codex case.
type guardExtension struct {
	name    string
	phase   string
	tool    string
	lane    string
	path    string
	outcome guardOutcome
	shape   string
}

// The extension cases of each client, in the order of the script.
var (
	claudeGuardExtensions = []guardExtension{
		{"subagent-edit-violation", "enforce", claudeToolEdit, LaneChild, "sentinel/edit.txt", guardOutcomeDeny, launchForeground},
		{"subagent-multiedit-violation", "enforce", claudeToolMultiEdit, LaneChild, "notes/multi.txt", guardOutcomeDeny, launchForeground},
		{"subagent-check-only", "check-pins", claudeToolWrite, LaneChild, "sentinel/child-checked.txt", guardOutcomeReply, launchBackground},
		{"slow-deny-write", guardPhaseSlowDeny, claudeToolWrite, LaneRoot, "sentinel/slow-deny.txt", guardOutcomeSlowDeny, ""},
		{"slow-deny-edit", guardPhaseSlowDeny, claudeToolEdit, LaneRoot, "sentinel/edit.txt", guardOutcomeSlowDeny, ""},
		{"slow-deny-multiedit", guardPhaseSlowDeny, claudeToolMultiEdit, LaneRoot, "notes/multi.txt", guardOutcomeSlowDeny, ""},
		{"slow-deny-subagent-write", guardPhaseSlowDeny, claudeToolWrite, LaneChild, "sentinel/slow-child.txt", guardOutcomeSlowDeny, launchBackground},
		{"slow-deny-subagent-edit", guardPhaseSlowDeny, claudeToolEdit, LaneChild, "sentinel/edit.txt", guardOutcomeSlowDeny, launchBackground},
		{"slow-deny-subagent-multiedit", guardPhaseSlowDeny, claudeToolMultiEdit, LaneChild, "notes/multi.txt", guardOutcomeSlowDeny, launchBackground},
		{guardExecutionFailure, "post-only", claudeToolWrite, LaneRoot, "sentinel/locked/new.txt", guardOutcomeFailure, ""},
	}
	codexGuardExtensions = []guardExtension{
		{"subagent-check-only", "check-pins", codexApplyPatch, LaneChild, "sentinel/child-checked.txt", guardOutcomeReply, ""},
		{"slow-deny-add", guardPhaseSlowDeny, codexApplyPatch, LaneRoot, "sentinel/slow-deny.txt", guardOutcomeSlowDeny, ""},
		{"slow-deny-subagent-add", guardPhaseSlowDeny, codexApplyPatch, LaneChild, "sentinel/slow-child.txt", guardOutcomeSlowDeny, ""},
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
			"scripts/native-record.sh", client, r.manifest.ClientVersion)
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

	// The shape is logged before a skip, because a skipped MultiEdit case
	// gives no proof, but its shape still tells the reviewer what ran.
	if x.shape != "" {
		requested, observed, err := r.agentLaunch(x.name)
		t.Logf("case %s: launch shape requested %q, observed %q, error %v", x.name, requested, observed, err)
	}

	if x.tool == claudeToolMultiEdit && !r.has(x.name, "PreToolUse", x.tool) {
		require.NoError(t, r.noMultiEditTool(x),
			"case %s: no MultiEdit payload: the model did not follow the prompt, so record again", x.name)
		t.Skipf("Claude Code %s offered no MultiEdit tool in the %s lane; the MultiEdit path keeps no native evidence",
			r.manifest.ClientVersion, x.lane)
	}

	if x.shape != "" {
		assert.NoError(t, r.launchShape(x.name, x.shape), "case %s", x.name)
	}

	switch x.outcome {
	case guardOutcomeDeny:
		r.assertExtensionDeny(t, client, x)
	case guardOutcomeSlowDeny:
		r.assertExtensionSlowDeny(t, client, x)
	case guardOutcomeReply:
		r.assertExtensionReply(t, client, x)
	case guardOutcomeFailure:
		r.assertExtensionFailure(t, x)
	default:
		require.Failf(t, "unknown outcome", "%s", x.outcome)
	}
}

// assertExtensionFailure checks the write that fails during its
// execution (see executionFailure) and logs the error text of the client
// and the reply. The reply is a corroboration only: the reviewer reads the
// reach of the post-edit reply from guard-context.jsonl.
func (r guardRecording) assertExtensionFailure(t *testing.T, x guardExtension) {
	t.Helper()

	require.NoError(t, r.executionFailure(x), "case %s", x.name)

	for _, e := range r.events[r.runs[x.name]] {
		if e.hook.Event == claudeEventToolFailure && e.tool() == x.tool {
			t.Logf("case %s: the PostToolUseFailure error of Claude Code %s: %q", x.name, r.manifest.ClientVersion,
				nativeString(e.raw, "error"))
		}
	}

	t.Logf("case %s: the reply names %q", x.name, r.cases[x.name].Reply)
}

// executionFailure returns nil when the run of a case shows a write that
// fails during its execution. The run has one PreToolUse payload of the
// tool and then a PostToolUseFailure payload of the same call in the root
// lane, with a permission error. No guard runs in the phase. One post-edit
// record of the call reports the failed tool without a baseline as
// incomplete and replies. The fixture stays unchanged. The debug log shows
// the verify command on PostToolUseFailure with exit status 0.
//
// A run without the PreToolUse payload, a retried call, or a successful
// write asks for a new recording. The client checks a new file only for
// its absence before the hooks, so no valid run skips this path, and a
// successful write means that the directory was not read-only.
func (r guardRecording) executionFailure(x guardExtension) error {
	run := r.runs[x.name]
	events := r.events[run]

	var pre, failure []timingEvent

	for _, e := range events {
		switch {
		case e.tool() != x.tool:
		case e.hook.Event == claudeEventPreToolUse:
			pre = append(pre, e)
		case e.hook.Event == claudeEventToolFailure:
			failure = append(failure, e)
		case e.hook.Event == "PostToolUse":
			return fmt.Errorf("a PostToolUse %s payload: the write succeeded, so %s was not read-only; record the case again",
				x.tool, path.Dir(x.path))
		}
	}

	switch {
	case len(pre) == 0:
		return fmt.Errorf("no PreToolUse %s payload: the model made no call, or the client refused it before any hook; "+
			"record the case again", x.tool)
	case len(pre) > 1:
		return fmt.Errorf("%d PreToolUse %s payloads: the model retried the call; record the case again", len(pre), x.tool)
	case len(failure) == 0:
		return fmt.Errorf("Claude Code %s sent no PostToolUseFailure for the failed %s", r.manifest.ClientVersion, x.tool)
	case !slices.ContainsFunc(events[:pre[0].seq], func(e timingEvent) bool { return e.hook.Event == "UserPromptSubmit" }):
		return errors.New("no UserPromptSubmit payload comes before the call")
	case failure[0].seq < pre[0].seq:
		return errors.New("PostToolUseFailure comes before PreToolUse")
	}

	call := nativeString(pre[0].raw, "tool_use_id")
	if call == "" || nativeString(failure[0].raw, "tool_use_id") != call {
		return errors.New("PostToolUseFailure names another tool call than PreToolUse")
	}

	if interrupt, set := failure[0].raw["is_interrupt"]; set && interrupt != false {
		return fmt.Errorf("PostToolUseFailure has is_interrupt %v: the call stopped, it did not fail", interrupt)
	}

	if nativeString(pre[0].raw, "agent_id") != "" || nativeString(failure[0].raw, "agent_id") != "" {
		return errors.New("a payload names an agent, but the parent makes the call")
	}

	text := strings.ToLower(nativeString(failure[0].raw, "error"))
	if !strings.Contains(text, "eacces") && !strings.Contains(text, "permission denied") {
		return errors.New("the PostToolUseFailure error names no EACCES and no permission denied")
	}

	op, err := claudeGuards{}.DecodeGuard(pre[0].payload)
	if err != nil {
		return fmt.Errorf("the PreToolUse payload does not decode: %w", err)
	}

	if paths := r.opPaths(op); !slices.Equal(paths, []string{x.path}) {
		return fmt.Errorf("the call names %v, not %s", paths, x.path)
	}

	return r.failureRecords(x, pre[0])
}

// failureRecords checks the audit records, the fixture, and the hook
// result of the failed write in executionFailure.
func (r guardRecording) failureRecords(x guardExtension, pre timingEvent) error {
	c := r.cases[x.name]
	if c.Audit[0] < 0 || c.Audit[0] > c.Audit[1] || c.Audit[1] > len(r.audit) {
		return fmt.Errorf("the audit range %v lies outside the %d audit records", c.Audit, len(r.audit))
	}

	records := r.audit[c.Audit[0]:c.Audit[1]]
	if guards := ofKind(records, lessoncheck.AuditGuard); len(guards) > 0 {
		return fmt.Errorf("%d guard records: the phase has no pre-write hook", len(guards))
	}

	posts := ofKind(records, lessoncheck.AuditPostEdit)
	if len(posts) != 1 {
		return fmt.Errorf("%d post-edit records, not one", len(posts))
	}

	post := posts[0]
	if post.ToolOutcome != lessoncheck.ToolFailed || post.Attribution != lessoncheck.AttributionNoBaseline ||
		post.State != lessoncheck.SummaryIncomplete || post.Emitted == nil || !*post.Emitted {
		return fmt.Errorf("the post-edit record reads %s, %s, %s; want a failed tool without a baseline, incomplete, "+
			"and a written reply", post.ToolOutcome, post.Attribution, post.State)
	}

	var rows []guardIdentity

	for _, id := range r.identities {
		if id.File == pre.hook.File {
			rows = append(rows, id)
		}
	}

	if len(rows) != 1 || !guardDigest(rows[0].MatchSHA) || rows[0].MatchSHA != post.MatchSHA {
		return errors.New("the post-edit record does not join the identity row of the PreToolUse payload")
	}

	if !c.unchanged() || c.created(x.path) {
		return fmt.Errorf("the failed write changed the fixture: before %v, after %v", c.Before, c.After)
	}

	if !slices.ContainsFunc(r.hooks, func(h guardHookResult) bool {
		return h.Run == c.Run && h.Hook == "verify" && h.Event == claudeEventToolFailure &&
			h.Status != nil && *h.Status == 0
	}) {
		return errors.New("the debug log shows no verify command on PostToolUseFailure with exit status 0")
	}

	return nil
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
// transcript. For Codex, the replay logs the rollout records that hold
// the PostToolUse context of the call.
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

	if client == CodexID {
		r.logCodexPostEditContext(t, x.name, x.path)
	}
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

func TestNativeRecordClaudeGuardExtension(t *testing.T) {
	loadGuardExtension(t, ClaudeID, claudeGuardCases).replayExtension(t, ClaudeID)
}

func TestNativeRecordCodexGuardExtension(t *testing.T) {
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
	script := nativeRecordScript(t)

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
	script := nativeRecordScript(t)

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

	script := nativeRecordScript(t)
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

	script := nativeRecordScript(t)
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

// syntheticEvents gives each payload of a synthetic run its hook entry,
// its position, its decoded form, and its bytes, as readHookEvents does.
func syntheticEvents(t *testing.T, run int, payloads []map[string]any) []timingEvent {
	t.Helper()

	events := make([]timingEvent, 0, len(payloads))

	for i, raw := range payloads {
		data, err := json.Marshal(raw)
		require.NoError(t, err)

		event, _ := raw["hook_event_name"].(string)
		hook := nativeHook{File: fmt.Sprintf("hooks/%03d-%s.json", i+1, event), Run: run, Event: event}
		events = append(events, timingEvent{hook: hook, seq: i, payload: data, raw: raw})
	}

	return events
}

// failurePayloads gives the payloads of the expected execution-failure
// run: the prompt, the Write call, its failure with the permission error
// of the temporary file, and the stop.
func failurePayloads() []map[string]any {
	return []map[string]any{
		{"hook_event_name": "UserPromptSubmit", "session_id": "s1"},
		{"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/workspace/native", "tool_name": claudeToolWrite,
			"tool_use_id": "toolu_1", "tool_input": map[string]any{"file_path": "/workspace/native/sentinel/locked/new.txt",
				"content": "locked\n"}},
		{"hook_event_name": "PostToolUseFailure", "session_id": "s1", "tool_name": claudeToolWrite, "tool_use_id": "toolu_1",
			"is_interrupt": false, "error": "EACCES: permission denied, open '/workspace/native/sentinel/locked/new.txt.tmp.1.abc'"},
		{"hook_event_name": "Stop", "session_id": "s1"},
	}
}

// failureRecording builds a synthetic Claude Code recording of the
// execution-failure case in run 1 from the payloads. One post-edit record
// joins the identity row of the PreToolUse payload, and the debug log
// shows the verify command on PostToolUseFailure.
func failureRecording(t *testing.T, payloads []map[string]any) (guardRecording, guardExtension) {
	t.Helper()

	x := guardExtensionRow(t, ClaudeID, guardExecutionFailure)
	state := guardState{Files: map[string]string{"sentinel/edit.txt": "a"}}

	r := guardRecording{
		timingRecording: timingRecording{runs: map[string]int{x.name: 1}, events: map[int][]timingEvent{}},
		cases: map[string]guardCase{x.name: {Run: 1, Case: x.name, Phase: x.phase, Before: state, After: state,
			Audit: [2]int{0, 1}}},
	}
	r.manifest.Client, r.manifest.ClientVersion = ClaudeID, "synthetic"
	r.manifest.Placeholders.Workspace = "/workspace/native"
	r.events[1] = syntheticEvents(t, 1, payloads)

	match, emitted, status := strings.Repeat("ab", 32), true, 0
	r.audit = []lessoncheck.AuditRecord{{
		Kind: lessoncheck.AuditPostEdit, MatchSHA: match, ToolOutcome: lessoncheck.ToolFailed,
		Attribution: lessoncheck.AttributionNoBaseline, State: lessoncheck.SummaryIncomplete, Emitted: &emitted,
	}}
	r.identities = []guardIdentity{{File: "hooks/002-PreToolUse.json", Run: 1, Case: x.name, Client: ClaudeID,
		Tool: claudeToolWrite, Lane: LaneRoot, MatchSHA: match}}
	r.hooks = []guardHookResult{{Run: 1, Hook: "verify", Event: claudeEventToolFailure, Tool: claudeToolWrite, Status: &status}}

	return r, x
}

// TestExecutionFailureReadsTheFailedWrite checks the rule of the
// execution-failure case on synthetic recordings. They give no native
// proof. Each change to the expected run must give an error that names
// its reason.
func TestExecutionFailureReadsTheFailedWrite(t *testing.T) {
	t.Run("the expected run passes the replay", func(t *testing.T) {
		r, x := failureRecording(t, failurePayloads())
		require.NoError(t, r.executionFailure(x))
		r.replayExtensionCase(t, ClaudeID, x)
	})

	set := func(i int, key string, value any) func([]map[string]any) []map[string]any {
		return func(p []map[string]any) []map[string]any {
			p[i][key] = value

			return p
		}
	}

	for _, tc := range []struct {
		name     string
		payloads func([]map[string]any) []map[string]any
		record   func(r *guardRecording)
		want     string
	}{
		{name: "the write succeeded", payloads: set(2, "hook_event_name", "PostToolUse"), want: "record the case again"},
		{name: "no PostToolUseFailure", payloads: func(p []map[string]any) []map[string]any {
			return slices.Delete(p, 2, 3)
		}, want: "sent no PostToolUseFailure"},
		{name: "no PreToolUse", payloads: func(p []map[string]any) []map[string]any {
			return slices.Delete(p, 1, 2)
		}, want: "no PreToolUse Write payload"},
		{name: "a second PreToolUse", payloads: func(p []map[string]any) []map[string]any {
			return slices.Insert(p, 2, maps.Clone(p[1]))
		}, want: "retried the call"},
		{name: "another tool call", payloads: set(2, "tool_use_id", "toolu_2"), want: "another tool call"},
		{name: "an error without a permission error", payloads: set(2, "error", "ENOSPC: no space left on device"),
			want: "no EACCES"},
		{name: "an interrupt", payloads: set(2, "is_interrupt", true), want: "is_interrupt"},
		{name: "an agent", payloads: set(2, "agent_id", "a1"), want: "names an agent"},
		{name: "another path", payloads: set(1, "tool_input", map[string]any{"file_path": "/workspace/native/sentinel/other.txt",
			"content": "locked\n"}), want: "not sentinel/locked/new.txt"},
		{name: "a guard record", record: func(r *guardRecording) {
			r.audit = append(r.audit, lessoncheck.AuditRecord{Kind: lessoncheck.AuditGuard})
			r.cases[guardExecutionFailure] = withAudit(r.cases[guardExecutionFailure], 0, 2)
		}, want: "1 guard records"},
		{name: "another outcome", record: func(r *guardRecording) { r.audit[0].ToolOutcome = lessoncheck.ToolSucceeded },
			want: "want a failed tool"},
		{name: "another attribution", record: func(r *guardRecording) {
			r.audit[0].Attribution = lessoncheck.AttributionProjectionDiffers
		}, want: "want a failed tool"},
		{name: "no identity join", record: func(r *guardRecording) { r.audit[0].MatchSHA = strings.Repeat("cd", 32) },
			want: "does not join"},
		{name: "a changed fixture", record: func(r *guardRecording) {
			c := r.cases[guardExecutionFailure]
			c.After = guardState{Files: map[string]string{"sentinel/edit.txt": "a", "sentinel/locked/new.txt": "b"}}
			r.cases[guardExecutionFailure] = c
		}, want: "changed the fixture"},
		{name: "no verify result", record: func(r *guardRecording) { r.hooks[0].Event = claudeEventPostTool },
			want: "no verify command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payloads := failurePayloads()
			if tc.payloads != nil {
				payloads = tc.payloads(payloads)
			}

			r, x := failureRecording(t, payloads)
			if tc.record != nil {
				tc.record(&r)
			}

			err := r.executionFailure(x)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// withAudit returns a case line with another audit range.
func withAudit(c guardCase, from, to int) guardCase {
	c.Audit = [2]int{from, to}

	return c
}

// TestGuardScenarioFixtureOfTheFailureCase runs guard_fixture,
// guard_state, and guard_reset of the script in a scratch Git repository.
// The fixture step adds an empty read-only directory that leaves the
// state unchanged. The reset removes it, also when a write left a file in
// it, which only the chmod of guard_reset makes possible.
func TestGuardScenarioFixtureOfTheFailureCase(t *testing.T) {
	requireTools(t, "sh", "git", "python3", "chmod", "mkdir")

	script := nativeRecordScript(t)
	repo, out := t.TempDir(), t.TempDir()
	locked := filepath.Join(repo, "sentinel", "locked")

	// A directory without write permission blocks the removal of the
	// temporary directory, so the test gives it back its permission.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))

		return string(out)
	}

	git("init", "-q", "-b", "main")
	git("config", "user.name", "native")
	git("config", "user.email", "native@example.invalid")

	for _, f := range []string{"sentinel/edit.txt", "notes/failure.txt", "free/oracle.txt"} {
		writeLines(t, filepath.Join(repo, filepath.FromSlash(f)), "alpha")
	}

	git("add", "sentinel", "notes", "free")
	git("commit", "-q", "-m", "guard fixture")

	run := func(program string) {
		defs := scriptFunction(t, script, "guard_reset") + scriptFunction(t, script, "guard_fixture") +
			scriptFunction(t, script, "guard_state")
		cmd := exec.Command("sh", "-eu", "-c", defs+program)
		cmd.Env = append(os.Environ(), "REPO="+repo, "OUT="+out)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	run("guard_state \"$OUT/before.json\"\nguard_fixture execution-failure\nguard_state \"$OUT/after.json\"\n")

	info, err := os.Stat(locked)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o555), info.Mode().Perm(), "the directory is read-only")

	entries, err := os.ReadDir(locked)
	require.NoError(t, err)
	assert.Empty(t, entries, "the directory holds no file")

	before, err := os.ReadFile(filepath.Join(out, "before.json"))
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(out, "after.json"))
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after), "an empty directory changes no file hash and no git status line")

	if os.Geteuid() != 0 {
		assert.Error(t, os.WriteFile(filepath.Join(locked, "new.txt"), []byte("locked\n"), 0o600),
			"a file creation in the read-only directory fails")
	}

	run("guard_reset\n")
	assert.NoDirExists(t, locked, "the reset removes the empty directory")
	assert.Empty(t, git("status", "--porcelain", "--untracked-files=all"))

	// A write that reached the directory leaves a file inside it.
	require.NoError(t, os.MkdirAll(locked, 0o755))
	writeLines(t, filepath.Join(locked, "x"), "written")
	require.NoError(t, os.Chmod(locked, 0o555))

	if os.Geteuid() != 0 {
		// git clean alone cannot remove the file, so the chmod is needed.
		out, err := exec.Command("git", "-C", repo, "clean", "-fdq", "--", "sentinel").CombinedOutput()
		assert.Error(t, err, string(out))
		assert.FileExists(t, filepath.Join(locked, "x"))
	}

	run("guard_reset\n")
	assert.NoDirExists(t, locked, "the reset removes the directory and its file")
	assert.Empty(t, git("status", "--porcelain", "--untracked-files=all"))
}
