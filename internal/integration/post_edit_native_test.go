package integration

// Offline replay of the native post-edit recordings.
// `make native-record CLIENT=<client> NATIVE_RECORD_SCENARIO=post-edit`
// records them with the installed client and the operator's own login.
// A reviewer copies the reviewed sanitized copy into
// testdata/<client>/post-edit/. The scenario runs its cases through the
// guard functions of the script, so the copy holds the files of a guard
// copy, and the guard reader decodes it.
//
// For each case the tests check what the recording shows about the
// post-edit hook: the payloads of the call, the records that join the
// call through its identity row, and the lines of the post-edit
// indirection entry. They do not decide whether a reply reached the
// model, or whether the client waited for a slow hook before its next
// model step. The reviewer reads those answers from guard-context.jsonl
// and the transcripts.
//
// The replay tests run only when SEAMARK_NATIVE_CLIENT names the client
// (make native-replay). Without a reviewed recording they skip as
// pending. A skip gives no evidence, so it never verifies a post-edit
// path. TestPostEditEvidenceMatchesRecordings reads the same recordings
// in every test run (post_edit_evidence_test.go).

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// postEditScenario names the scenario and its testdata directory.
const postEditScenario = "post-edit"

// The phases of the post-edit scenario (scripts/native-record.sh,
// post_phase). The check phase runs with the hook document of setup. The
// slow phases remove the owned verify entry of PostToolUse and let the
// indirection entry run the verify command after a wait.
const (
	postEditPhaseCheck   = "check"
	postEditPhaseSlow    = "slow-post"
	postEditPhaseTimeout = "post-timeout"
)

// The lines that the post-edit indirection entry logs in a run: one line
// before its wait, and one line with the exit status of the verify
// command after it. The verify command exits 0 when it wrote its reply.
const (
	postEditSlowStart    = postEditPhaseSlow + " start"
	postEditSlowDone     = postEditPhaseSlow + " 0"
	postEditTimeoutStart = postEditPhaseTimeout + " start"
	postEditTimeoutDone  = postEditPhaseTimeout + " 0"
)

// postEditHook is the name of the post-edit indirection command in
// guard-hooks.jsonl (scripts/native-record.sh, guard_extras).
const postEditHook = "post-edit"

// postEditOutcome is the expected outcome of a post-edit case.
type postEditOutcome string

const (
	// postEditOutcomeReply: the owned verify entry checks the write after
	// the tool ran, and its post-edit record wrote a reply.
	postEditOutcomeReply postEditOutcome = "reply"
	// postEditOutcomeSlowReply: the indirection entry waits inside the
	// post-edit timeout, then runs the verify command to the end, and the
	// post-edit record wrote a reply.
	postEditOutcomeSlowReply postEditOutcome = "slow-reply"
	// postEditOutcomeTimeout: the indirection entry waits past the
	// post-edit timeout. The client either stops it before the verify
	// command, or lets it run to the end. Both are valid answers.
	postEditOutcomeTimeout postEditOutcome = "timeout"
	// postEditOutcomeFailure: the patch fails during its execution, after
	// the pre-write hook. The client sends a PostToolUse payload or none.
	postEditOutcomeFailure postEditOutcome = "failure"
)

// postEditCase is one row of the scenario table. The shape is the launch
// shape that the prompt of a Claude Code child case sets. It is empty
// for a root case and for each Codex case.
type postEditCase struct {
	name    string
	phase   string
	tool    string
	lane    string
	path    string
	outcome postEditOutcome
	shape   string
}

// The cases of each client, in the order of the script. Every Claude
// Code child prompt sets the foreground shape, so the parent waits for
// the slow hook of the child call inside its Agent call.
var (
	claudePostEditCases = []postEditCase{
		{"post-edit-edit", postEditPhaseCheck, claudeToolEdit, LaneRoot, "sentinel/edit.txt", postEditOutcomeReply, ""},
		{"post-edit-subagent-edit", postEditPhaseCheck, claudeToolEdit, LaneChild, "sentinel/edit.txt",
			postEditOutcomeReply, launchForeground},
		{"slow-post-write", postEditPhaseSlow, claudeToolWrite, LaneRoot, "sentinel/slow-post.txt", postEditOutcomeSlowReply, ""},
		{"slow-post-edit", postEditPhaseSlow, claudeToolEdit, LaneRoot, "sentinel/edit.txt", postEditOutcomeSlowReply, ""},
		{"slow-post-subagent-write", postEditPhaseSlow, claudeToolWrite, LaneChild, "sentinel/slow-post-child.txt",
			postEditOutcomeSlowReply, launchForeground},
		{"slow-post-subagent-edit", postEditPhaseSlow, claudeToolEdit, LaneChild, "sentinel/edit.txt",
			postEditOutcomeSlowReply, launchForeground},
		{"post-timeout-write", postEditPhaseTimeout, claudeToolWrite, LaneRoot, "sentinel/post-timeout.txt",
			postEditOutcomeTimeout, ""},
	}
	codexPostEditCases = []postEditCase{
		{guardExecutionFailure, postEditPhaseCheck, codexApplyPatch, LaneRoot, "sentinel/locked/new.txt",
			postEditOutcomeFailure, ""},
		{"slow-post-add", postEditPhaseSlow, codexApplyPatch, LaneRoot, "sentinel/slow-post.txt", postEditOutcomeSlowReply, ""},
		{"slow-post-subagent-add", postEditPhaseSlow, codexApplyPatch, LaneChild, "sentinel/slow-post-child.txt",
			postEditOutcomeSlowReply, ""},
		{"post-timeout-add", postEditPhaseTimeout, codexApplyPatch, LaneRoot, "sentinel/post-timeout.txt",
			postEditOutcomeTimeout, ""},
	}
)

// postEditCases returns the scenario table of a client.
func postEditCases(client string) []postEditCase {
	if client == CodexID {
		return codexPostEditCases
	}

	return claudePostEditCases
}

// postEditNames returns the case names of a client, in the order of the
// script.
func postEditNames(client string) []string {
	var names []string

	for _, x := range postEditCases(client) {
		names = append(names, x.name)
	}

	return names
}

// loadPostEditRecording reads the reviewed post-edit recording of a
// client for its replay, or skips. The recording must hold exactly the
// cases of the client table, and the identity rows that join each call
// to its records.
func loadPostEditRecording(t *testing.T, client string) guardRecording {
	t.Helper()

	if os.Getenv(nativeClientEnv) != client {
		t.Skipf("the post-edit replay runs with %s=%s (make native-replay CLIENT=%s)",
			nativeClientEnv, client, client)
	}

	r, ok := readCaseRecording(t, client, postEditScenario)
	if !ok {
		t.Skipf("pending: no reviewed native post-edit recording in %s; record one with "+
			"make native-record CLIENT=%s NATIVE_RECORD_SCENARIO=%s, review the sanitized copy, and copy it there",
			filepath.Join("testdata", client, postEditScenario), client, postEditScenario)
	}

	require.ElementsMatch(t, postEditNames(client), slices.Collect(maps.Keys(r.cases)),
		"the recording holds every case of the scenario and no other case")
	require.Contains(t, r.manifest.Extras, guardIdentityFile,
		"the recording has no %s: record again with the current scripts/native-record.sh", guardIdentityFile)
	t.Logf("native post-edit recording of %s %s: %d cases", client, r.manifest.ClientVersion, len(r.cases))

	return r
}

func TestNativeRecordClaudePostEditRecording(t *testing.T) {
	loadPostEditRecording(t, ClaudeID).replayPostEdit(t)
}

func TestNativeRecordCodexPostEditRecording(t *testing.T) {
	loadPostEditRecording(t, CodexID).replayPostEdit(t)
}

// replayPostEdit checks the capability report of each phase, then runs
// the subtest of each case of the client.
func (r guardRecording) replayPostEdit(t *testing.T) {
	client := r.manifest.Client

	t.Run("the capability report claims no enforcement without evidence", func(t *testing.T) {
		r.assertCapability(t, client)
	})

	for _, x := range postEditCases(client) {
		t.Run(x.name, func(t *testing.T) { r.replayPostEditCase(t, x) })
	}
}

// replayPostEditCase checks one case against its row of the scenario
// table, and logs the facts that the reviewer reads with the transcripts.
func (r guardRecording) replayPostEditCase(t *testing.T, x postEditCase) {
	c := r.cases[x.name]
	require.Equal(t, x.phase, c.Phase, "the script ran the case in its phase")

	if x.shape != "" {
		requested, observed, err := r.agentLaunch(x.name)
		t.Logf("case %s: launch shape requested %q, observed %q, error %v", x.name, requested, observed, err)
		require.NoError(t, r.launchShape(x.name, x.shape), "case %s", x.name)
	}

	var (
		note string
		skip bool
		err  error
	)

	switch x.outcome {
	case postEditOutcomeReply:
		note, err = r.postEditReply(x)
	case postEditOutcomeSlowReply:
		note, err = r.postEditSlowReply(x)
	case postEditOutcomeTimeout:
		note, err = r.postEditTimeout(x)
	case postEditOutcomeFailure:
		note, skip, err = r.postEditFailure(x)
	default:
		err = fmt.Errorf("unknown outcome %s", x.outcome)
	}

	require.NoError(t, err, "case %s", x.name)

	if skip {
		t.Skip(note)
	}

	t.Log(note)

	if x.lane == LaneChild && r.manifest.Client == ClaudeID {
		reply, ok := r.childReply(x.name)
		t.Logf("case %s: the subagent replies %q (found %t), the parent %q", x.name, reply, ok, c.Reply)
	} else {
		t.Logf("case %s: the reply names %q", x.name, c.Reply)
	}

	if r.manifest.Client == CodexID {
		r.logCodexPostEditContext(t, x.name, x.path)
	}
}

// errNoPreWrite is the error of a run without the PreToolUse payload of
// the tool. The failure case reads it as a client refusal or a missing
// call, and skips.
var errNoPreWrite = errors.New("no PreToolUse payload of the tool")

// postEditCall is the call of a post-edit case: its pre-write payload,
// its PostToolUse payload when the client sent one, its identity row,
// and the baseline and post-edit records that name it.
type postEditCall struct {
	pre, post timingEvent
	hasPost   bool
	identity  guardIdentity
	baselines []lessoncheck.AuditRecord
	posts     []lessoncheck.AuditRecord
}

// postEditCallOf finds the call of a case. The run holds exactly one
// PreToolUse payload of the tool, after UserPromptSubmit. A child call
// comes after SubagentStart and names its agent; a root call names no
// agent. At most one PostToolUse payload of the tool exists, after the
// PreToolUse payload, with the same tool call and agent. The records join
// the call only through its one identity row, so a missing or ambiguous
// join gives an error and never a guess.
func (r guardRecording) postEditCallOf(x postEditCase) (postEditCall, error) {
	var (
		call      postEditCall
		pre, post []timingEvent
	)

	c, ok := r.cases[x.name]
	if !ok || c.Run <= 0 || r.runs[x.name] != c.Run {
		return call, fmt.Errorf("the recording holds no run of the case %s", x.name)
	}

	events := r.events[c.Run]

	for _, e := range events {
		switch {
		case e.tool() != x.tool:
		case e.hook.Event == "PreToolUse":
			pre = append(pre, e)
		case e.hook.Event == "PostToolUse":
			post = append(post, e)
		}
	}

	switch {
	case len(pre) == 0:
		return call, fmt.Errorf("%w %s: the model made no call, or the client refused it before any hook",
			errNoPreWrite, x.tool)
	case len(pre) > 1:
		return call, fmt.Errorf("%d PreToolUse %s payloads: the model retried the call; record the case again",
			len(pre), x.tool)
	case len(post) > 1:
		return call, fmt.Errorf("%d PostToolUse %s payloads, not at most one", len(post), x.tool)
	}

	call.pre = pre[0]
	before := events[:call.pre.seq]

	if !slices.ContainsFunc(before, func(e timingEvent) bool { return e.hook.Event == "UserPromptSubmit" }) {
		return call, errors.New("no UserPromptSubmit payload comes before the call")
	}

	agent := nativeString(call.pre.raw, "agent_id")

	if x.lane == LaneChild {
		start := slices.IndexFunc(before, func(e timingEvent) bool { return e.hook.Event == "SubagentStart" })
		if start < 0 || agent == "" || nativeString(before[start].raw, "agent_id") != agent {
			return call, errors.New("the call names no agent of a SubagentStart payload before it")
		}
	} else if agent != "" {
		return call, errors.New("the call names an agent, but the parent makes the call")
	}

	id := nativeString(call.pre.raw, "tool_use_id")
	if id == "" {
		return call, errors.New("the PreToolUse payload names no tool call")
	}

	if len(post) == 1 {
		call.post, call.hasPost = post[0], true

		switch {
		case call.post.seq < call.pre.seq:
			return call, errors.New("PostToolUse comes before PreToolUse")
		case nativeString(call.post.raw, "tool_use_id") != id:
			return call, errors.New("PostToolUse names another tool call than PreToolUse")
		case nativeString(call.post.raw, "agent_id") != agent:
			return call, errors.New("PostToolUse names another agent than PreToolUse")
		}
	}

	return r.postEditRecords(x, c, call)
}

// postEditRecords adds the identity row and the records of a call. The
// identity row must be the only row of the payload file and match the
// run, case, client, tool, and lane, with digests of the collector form.
// No other row may repeat its call identity.
func (r guardRecording) postEditRecords(x postEditCase, c guardCase, call postEditCall) (postEditCall, error) {
	client := r.manifest.Client

	receiver := ReceivingContextOf(client, nativeString(call.pre.raw, "session_id"), nativeString(call.pre.raw, "agent_id"))
	if receiver == nil || receiver.Lane != x.lane {
		return call, fmt.Errorf("the call is not in the %s lane", x.lane)
	}

	var rows []guardIdentity

	for _, id := range r.identities {
		if id.File == call.pre.hook.File {
			rows = append(rows, id)
		}
	}

	if len(rows) != 1 {
		return call, fmt.Errorf("%d identity rows name %s, not one", len(rows), call.pre.hook.File)
	}

	call.identity = rows[0]
	id := call.identity

	if id.Run != c.Run || id.Case != x.name || id.Client != client || id.Tool != x.tool || id.Lane != x.lane ||
		!guardDigest(id.SessionSHA) || !guardDigest(id.MatchSHA) || !guardDigest(id.ContextSHA) {
		return call, errors.New("the identity row does not match the run, case, client, tool, and lane of the call")
	}

	for _, other := range r.identities {
		if other.File != id.File && other.Client == client && other.MatchSHA == id.MatchSHA {
			return call, errors.New("another identity row repeats the call identity")
		}
	}

	if c.Audit[0] < 0 || c.Audit[0] > c.Audit[1] || c.Audit[1] > len(r.audit) {
		return call, fmt.Errorf("the audit range %v lies outside the %d audit records", c.Audit, len(r.audit))
	}

	// ofKind returns a new slice, so the removal leaves the audit records
	// of the recording as they are.
	records := r.audit[c.Audit[0]:c.Audit[1]]
	otherCall := func(rec lessoncheck.AuditRecord) bool { return rec.MatchSHA != id.MatchSHA }

	call.baselines = slices.DeleteFunc(ofKind(records, lessoncheck.AuditBaseline), otherCall)
	call.posts = slices.DeleteFunc(ofKind(records, lessoncheck.AuditPostEdit), otherCall)

	return call, nil
}

// postEditWrote reports an error unless the post-edit record of a
// successful call under check pins reads succeeded, violation, and
// projection-match, and wrote its reply.
func postEditWrote(post lessoncheck.AuditRecord) error {
	if post.ToolOutcome != lessoncheck.ToolSucceeded || post.State != lessoncheck.SummaryViolation ||
		post.Attribution != lessoncheck.AttributionProjectionMatch || post.Emitted == nil || !*post.Emitted {
		return fmt.Errorf("the post-edit record reads %s, %s, %s; want a succeeded tool, a violation, "+
			"projection-match, and a written reply", post.ToolOutcome, post.State, post.Attribution)
	}

	return nil
}

// postEditChanged reports an error unless the run wrote the path of the
// case: a Write or an Add creates the file, and an Edit changes it.
func (r guardRecording) postEditChanged(x postEditCase) error {
	c := r.cases[x.name]

	if x.tool == claudeToolEdit {
		before, had := c.Before.Files[x.path]
		after, has := c.After.Files[x.path]

		if !had || !has || before == after {
			return fmt.Errorf("the Edit did not change %s", x.path)
		}

		return nil
	}

	if !c.created(x.path) {
		return fmt.Errorf("the call did not create %s", x.path)
	}

	return nil
}

// postEditHookResult returns the result line of a hook command on
// PostToolUse in the Claude Code debug log of a case. A Codex recording
// has no debug log, so it gives no result and no error.
func (r guardRecording) postEditHookResult(x postEditCase, hook string) (guardHookResult, error) {
	if r.manifest.Client != ClaudeID {
		return guardHookResult{}, nil
	}

	run := r.runs[x.name]

	for _, h := range r.hooks {
		if h.Run == run && h.Hook == hook && h.Event == "PostToolUse" && h.Status != nil && *h.Status == 0 {
			return h, nil
		}
	}

	return guardHookResult{}, fmt.Errorf("the debug log shows no %s command on PostToolUse with exit status 0", hook)
}

// postEditDone checks the call of a case whose verify command ran to the
// end: one baseline record and one post-edit record of the call, the
// facts of postEditWrote, and the write of the path. One post-edit record
// shows that one entry ran the verify command for the call.
func (r guardRecording) postEditDone(x postEditCase, call postEditCall) error {
	switch {
	case !call.hasPost:
		return fmt.Errorf("no PostToolUse %s payload: the post-edit hook did not run", x.tool)
	case len(call.baselines) != 1:
		return fmt.Errorf("%d baseline records of the call, not one", len(call.baselines))
	case len(call.posts) != 1:
		return fmt.Errorf("%d post-edit records of the call, not one", len(call.posts))
	}

	if err := postEditWrote(call.posts[0]); err != nil {
		return err
	}

	return r.postEditChanged(x)
}

// postEditReply checks a case of the check phase: the owned verify entry
// checked the call, and the indirection entry logged nothing.
func (r guardRecording) postEditReply(x postEditCase) (string, error) {
	call, err := r.postEditCallOf(x)
	if err != nil {
		return "", err
	}

	if lines := r.cases[x.name].PostEdit; len(lines) > 0 {
		return "", fmt.Errorf("the indirection entry logged %v in the check phase", lines)
	}

	if err := r.postEditDone(x, call); err != nil {
		return "", err
	}

	if _, err := r.postEditHookResult(x, "verify"); err != nil {
		return "", err
	}

	return fmt.Sprintf("case %s: the owned verify entry wrote its reply for the %s call in the %s lane",
		x.name, x.tool, x.lane), nil
}

// postEditSlowReply checks a case of the slow-post phase. The indirection
// entry logged its start and the exit status 0 of the verify command, so
// the client let the hook run to the end. The owned verify entry is
// absent in the phase, so the one post-edit record comes from the
// indirection entry.
func (r guardRecording) postEditSlowReply(x postEditCase) (string, error) {
	call, err := r.postEditCallOf(x)
	if err != nil {
		return "", err
	}

	if lines := r.cases[x.name].PostEdit; !slices.Equal(lines, []string{postEditSlowStart, postEditSlowDone}) {
		return "", fmt.Errorf("not observed: the indirection entry logged %v, not one start and one exit status 0; "+
			"the client stopped the hook, or the hook ran more than once", lines)
	}

	if err := r.postEditDone(x, call); err != nil {
		return "", err
	}

	h, err := r.postEditHookResult(x, postEditHook)
	if err != nil {
		return "", err
	}

	took := "no debug log"
	if r.manifest.Client == ClaudeID {
		took = fmt.Sprintf("the debug log gives %d ms", h.MS)
	}

	return fmt.Sprintf("case %s: the slow hook ran to the end for the %s call in the %s lane (%s)",
		x.name, x.tool, x.lane, took), nil
}

// postEditTimeout checks a case of the post-timeout phase. The write
// happened, because a post-edit hook cannot block it. The indirection
// entry logged its start, and then either nothing or the exit status 0
// of the verify command. Without the second line the client stopped the
// hook, and no post-edit record of the call exists. With it the hook ran
// to the end, and one post-edit record exists.
func (r guardRecording) postEditTimeout(x postEditCase) (string, error) {
	call, err := r.postEditCallOf(x)
	if err != nil {
		return "", err
	}

	if !call.hasPost {
		return "", fmt.Errorf("no PostToolUse %s payload: the post-edit hook did not run", x.tool)
	}

	if err := r.postEditChanged(x); err != nil {
		return "", err
	}

	lines := r.cases[x.name].PostEdit
	stopped := slices.Equal(lines, []string{postEditTimeoutStart})

	switch {
	case stopped && len(call.posts) > 0:
		return "", fmt.Errorf("%d post-edit records of the call, but the indirection entry logged no exit status",
			len(call.posts))
	case stopped:
	case slices.Equal(lines, []string{postEditTimeoutStart, postEditTimeoutDone}):
		if err := r.postEditDone(x, call); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("the indirection entry logged %v, not a start with or without one exit status 0", lines)
	}

	return fmt.Sprintf("native post-edit timeout of %s %s: the client stopped the hook before the verify command: %t",
		r.manifest.Client, r.manifest.ClientVersion, stopped), nil
}

// postEditFailure checks the patch that fails during its execution. A
// run without the PreToolUse payload gives a skip: the client refused the
// patch before any hook, or the model made no call. Otherwise the patch
// must name only the path of the case and must not create its file. A
// run without the PostToolUse payload gives a skip that names the client
// version, because that client runs no hook after the failed patch.
// Otherwise one post-edit record names the call and does not read a
// succeeded tool, and the note logs what it reads.
//
// The state of the fixture covers only sentinel/, notes/, and free/. So
// only the decoded path shows that the patch targeted the read-only
// directory, and a patch that succeeds at another path must not pass.
// Only the Codex table has a failure case, so the Codex decoder reads the
// payload.
func (r guardRecording) postEditFailure(x postEditCase) (note string, skip bool, err error) {
	call, err := r.postEditCallOf(x)
	if errors.Is(err, errNoPreWrite) {
		return fmt.Sprintf("case %s: no PreToolUse %s payload; the client refused the patch before any hook, "+
			"or the model made no call", x.name, x.tool), true, nil
	}

	if err != nil {
		return "", false, err
	}

	op, err := codexGuards{}.DecodeGuard(call.pre.payload)
	if err != nil {
		return "", false, fmt.Errorf("the PreToolUse payload does not decode: %w", err)
	}

	if paths := r.opPaths(op); !slices.Equal(paths, []string{x.path}) {
		return "", false, fmt.Errorf("the call names %v, not %s; record the case again", paths, x.path)
	}

	c := r.cases[x.name]
	if c.created(x.path) || !c.unchanged() {
		return "", false, fmt.Errorf("the patch changed the fixture, so %s was not read-only; record the case again: "+
			"before %v, after %v", path.Dir(x.path), c.Before, c.After)
	}

	if !call.hasPost {
		return fmt.Sprintf("case %s: %s %s sent no PostToolUse after the failed patch; a hook after a failed patch "+
			"stays unsupported on that version", x.name, r.manifest.Client, r.manifest.ClientVersion), true, nil
	}

	if len(call.posts) != 1 {
		return "", false, fmt.Errorf("%d post-edit records of the call, not one", len(call.posts))
	}

	// The patch created no file at its only path, so a succeeded tool
	// contradicts the fixture. A reviewer must then read the payload, and
	// the note must not log the record as the hook after a failed patch.
	post := call.posts[0]
	if post.ToolOutcome == lessoncheck.ToolSucceeded {
		return "", false, fmt.Errorf("the post-edit record reads the outcome %s, but the patch did not create %s",
			post.ToolOutcome, x.path)
	}

	emitted := post.Emitted != nil && *post.Emitted

	return fmt.Sprintf("case %s: %s %s ran the post-edit hook after the failed patch; the record reads the outcome %s, "+
		"the attribution %s, the state %s, a written reply %t (%d baseline records)", x.name, r.manifest.Client,
		r.manifest.ClientVersion, post.ToolOutcome, post.Attribution, post.State, emitted, len(call.baselines)), false, nil
}
