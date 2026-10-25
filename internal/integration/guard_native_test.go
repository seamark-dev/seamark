package integration

// Offline replay of the gate-5 guard recordings (plan step P4.11).
// `make capture-native-record CLIENT=<client> CAPTURE_NATIVE_SCENARIO=guard`
// records them with the installed client and the operator's own login.
// A reviewer copies the reviewed sanitized copy into
// testdata/<client>/guard/. Each paid run of the scenario is one case,
// and guard-cases.jsonl names the case of each run with the state of the
// fixture directories before and after it. So these tests hold no
// session identifier.
//
// The tests decode the recorded hook payloads through the production
// guard decoders and read the lesson-check audit records of each run.
// For a denied case they check the blocking facts: the payload of the
// first attempt, a deny record that wrote its reply, no post-edit record
// of the same call, and fixture directories that the run left unchanged.
// They also check the capability reports of the phases against the
// contract K10 rules. They do not decide whether a deny text or a
// post-edit reply reached the model. That answer is what the recording
// measures, so the reviewer reads it from guard-context.jsonl and the
// transcripts (gate-5 checklist).
//
// The replay tests run only when SEAMARK_NATIVE_CLIENT names the client
// (make capture-native-check). Without a reviewed recording they skip as
// pending. A skip gives no evidence, so it never verifies a guard path.
// TestGuardEvidenceMatchesRecordings reads the same recordings in every
// test run (guard_evidence_test.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The files that the guard scenario adds to a recording, beside the hook
// payloads and the transcripts (scripts/capture-native.sh, guard_extras
// and guard_identities). guardIdentityFile joins each observed payload to
// its audit record. A recording without it gives no evidence.
const (
	guardCasesFile      = "guard-cases.jsonl"
	guardCapabilityFile = "guard-capability.jsonl"
	guardContextFile    = "guard-context.jsonl"
	guardHooksFile      = "guard-hooks.jsonl"
	guardAuditFile      = "lesson-check-audit.jsonl"
	guardIdentityFile   = "guard-identities.jsonl"
)

// The case names of the guard scenario. The script writes one name per
// paid run, so the names are the contract between the script and these
// tests.
const (
	guardEditViolation      = "edit-violation"
	guardWriteViolation     = "write-violation"
	guardMultiEditViolation = "multiedit-violation"
	guardSubagentViolation  = "subagent-violation"
	guardUnknownEdit        = "unknown-edit"
	guardN1AbsentEdit       = "n1-absent-edit"
	guardPermittedWrite     = "permitted-write"
	guardConfigError        = "config-error"
	guardCheckOnly          = "check-only"
	guardCheckInstalled     = "check-installed"
	guardFailureReach       = "failure-reach"
	guardReportOnlyWrapper  = "report-only-wrapper"
	guardSlowGuard          = "slow-guard"
	guardAddViolation       = "add-violation"
	guardUpdateViolation    = "update-violation"
	guardDeleteViolation    = "delete-violation"
	guardMoveViolation      = "move-violation"
	guardMultiFileViolation = "multi-file-violation"
	guardUpdateUnknown      = "update-unknown"
	guardUpdatePermitted    = "update-permitted"
)

// The cases of each client, in the order of the script.
var (
	claudeGuardCases = []string{
		guardEditViolation, guardWriteViolation, guardMultiEditViolation, guardSubagentViolation,
		guardUnknownEdit, guardN1AbsentEdit, guardPermittedWrite, guardConfigError, guardCheckOnly,
		guardCheckInstalled, guardFailureReach, guardReportOnlyWrapper, guardSlowGuard,
	}
	codexGuardCases = []string{
		guardAddViolation, guardUpdateViolation, guardDeleteViolation, guardMoveViolation,
		guardMultiFileViolation, guardSubagentViolation, guardUpdateUnknown, guardPermittedWrite,
		guardUpdatePermitted, guardConfigError, guardCheckOnly, guardReportOnlyWrapper, guardSlowGuard,
	}
)

// The phases of the guard scenario that remove the owned guard entry.
// The capability report of each reads every pre-write path as inactive.
var guardPhasesWithoutGuard = []string{"post-only", "wrapper", "slow"}

// codexAddAttributions are the attributions that a post-edit record of
// a native Add File section can have. The K8 projection joins the added
// lines with "\n" and adds no final newline. No recording shows yet
// whether the native applier writes one. With a final newline, the
// observed content differs from the projection. The replay accepts both
// and logs the answer, and the gate-5 checklist asks the reviewer to
// report it, because the contract is frozen.
var codexAddAttributions = []lessoncheck.Attribution{
	lessoncheck.AttributionProjectionMatch, lessoncheck.AttributionProjectionDiffers,
}

// guardState is the state of the fixture directories that the script
// notes before and after a run: the SHA-256 of every file, the text of
// every file under free/, and the git status lines.
type guardState struct {
	Files  map[string]string `json:"files"`
	Text   map[string]string `json:"text"`
	Status []string          `json:"status"`
}

// guardCase is one line of guard-cases.jsonl.
type guardCase struct {
	Run    int        `json:"run"`
	Case   string     `json:"case"`
	Phase  string     `json:"phase"`
	Before guardState `json:"before"`
	After  guardState `json:"after"`
	// Audit is the line range [from, to) of the run in
	// lesson-check-audit.jsonl.
	Audit [2]int `json:"audit"`
	// PreWrite holds the lines that the indirection entry logged in the
	// run: its mode and the exit status of the guard.
	PreWrite []string `json:"pre_write"`
	// Reply is the final reply of the model when it is one word. It is a
	// corroboration only.
	Reply string `json:"reply"`
}

// unchanged reports whether the run left every fixture file and the git
// status of the fixture directories as they were.
func (c guardCase) unchanged() bool {
	return maps.Equal(c.Before.Files, c.After.Files) && slices.Equal(c.Before.Status, c.After.Status)
}

// created reports whether the run added the file p.
func (c guardCase) created(p string) bool {
	_, before := c.Before.Files[p]
	_, after := c.After.Files[p]

	return !before && after
}

// guardHookResult is one line of guard-hooks.jsonl: the result of one
// guard, baseline, post-edit, or indirection command in the Claude Code
// debug log of a run. A line about a stopped hook has a note and no
// status.
type guardHookResult struct {
	Run    int    `json:"run"`
	Hook   string `json:"hook"`
	Event  string `json:"event"`
	Tool   string `json:"tool"`
	Status *int   `json:"status"`
	Note   string `json:"note"`
}

// guardCapability is one line of guard-capability.jsonl: the
// lesson-check report of `seamark status --json` at the start of a
// phase.
type guardCapability struct {
	Phase        string                        `json:"phase"`
	LessonChecks *lessoncheck.CapabilityReport `json:"lesson_checks"`
}

// guardRecording is one reviewed gate-5 guard recording. It embeds the
// timing view of a recording, so the sequence and path helpers of the
// timing replay serve it too.
type guardRecording struct {
	timingRecording

	cases      map[string]guardCase
	audit      []lessoncheck.AuditRecord
	hooks      []guardHookResult
	capability []guardCapability
	identities []guardIdentity
}

// guardIdentity binds one observed payload to the audit's original identity
// root. The collector records digests before sanitization replaces that root.
// It stores no raw root, session, agent, receiver, or tool-call ID.
type guardIdentity struct {
	File       string `json:"file"`
	Run        int    `json:"run"`
	Case       string `json:"case"`
	Client     string `json:"client"`
	Tool       string `json:"tool"`
	Lane       string `json:"lane"`
	SessionSHA string `json:"session_sha256"`
	MatchSHA   string `json:"match_sha256"`
	ContextSHA string `json:"context_sha256"`
}

// readGuardRecording reads the reviewed guard recording of a client. It
// returns false when no recording exists. It never skips, because the
// evidence test reads the recording in every test run.
func readGuardRecording(t *testing.T, client string) (guardRecording, bool) {
	t.Helper()

	dir := filepath.Join("testdata", client, "guard")

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return guardRecording{}, false
	}

	require.NoError(t, err)

	r := guardRecording{
		timingRecording: timingRecording{dir: dir, runs: map[string]int{}, events: map[int][]timingEvent{}},
		cases:           map[string]guardCase{},
	}
	require.NoError(t, json.Unmarshal(data, &r.manifest))
	require.Equal(t, client, r.manifest.Client)
	require.Subset(t, r.manifest.Extras,
		[]string{guardCasesFile, guardCapabilityFile, guardContextFile, guardHooksFile, guardAuditFile},
		"the guard scenario adds its cases, capability reports, deny and reply records, hook results, and audit records")

	for _, line := range timingLines(t, filepath.Join(dir, guardCasesFile)) {
		var c guardCase
		require.NoError(t, json.Unmarshal(line, &c))
		require.NotContains(t, r.cases, c.Case, "the case %s ran once", c.Case)

		r.cases[c.Case] = c
		r.runs[c.Case] = c.Run
	}

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

	readGuardLines(t, filepath.Join(dir, guardAuditFile), &r.audit)
	readGuardLines(t, filepath.Join(dir, guardHooksFile), &r.hooks)
	readGuardLines(t, filepath.Join(dir, guardCapabilityFile), &r.capability)
	// Older recordings have no exact event-to-audit join. Their evidence
	// stays unknown; the replay must not guess from tool names or order.
	if slices.Contains(r.manifest.Extras, guardIdentityFile) {
		readGuardLines(t, filepath.Join(dir, guardIdentityFile), &r.identities)
	}

	return r, true
}

// readGuardLines decodes each non-empty line of a JSONL file of the
// recording into one element of out. An empty file gives no element.
func readGuardLines[T any](t *testing.T, file string, out *[]T) {
	t.Helper()

	data, err := os.ReadFile(file)
	require.NoError(t, err)

	if len(data) == 0 {
		return
	}

	for _, line := range timingLines(t, file) {
		var v T
		require.NoError(t, json.Unmarshal(line, &v), file)

		*out = append(*out, v)
	}
}

// loadGuardRecording reads the reviewed guard recording of a client for
// its replay, or skips. The recording must hold each named case exactly
// once.
func loadGuardRecording(t *testing.T, client string, cases ...string) guardRecording {
	t.Helper()

	if os.Getenv(nativeClientEnv) != client {
		t.Skipf("the guard replay runs with %s=%s (make capture-native-check CLIENT=%s)",
			nativeClientEnv, client, client)
	}

	r, ok := readGuardRecording(t, client)
	if !ok {
		t.Skipf("pending: no reviewed gate-5 guard recording in %s; record one with "+
			"make capture-native-record CLIENT=%s CAPTURE_NATIVE_SCENARIO=guard, review the sanitized copy, "+
			"and copy it there", filepath.Join("testdata", client, "guard"), client)
	}

	require.ElementsMatch(t, cases, slices.Collect(maps.Keys(r.cases)), "the recording holds every case of the scenario")
	// Without identity rows, no payload joins its audit record, so every
	// assertion on a call would fail with a message that hides the cause.
	require.Contains(t, r.manifest.Extras, guardIdentityFile,
		"the recording has no %s: record again with the current scripts/capture-native.sh", guardIdentityFile)
	t.Logf("gate-5 guard recording of %s %s: %d cases", client, r.manifest.ClientVersion, len(r.cases))

	return r
}

// records returns the lesson-check audit records of the run of a case.
func (r guardRecording) records(t *testing.T, name string) []lessoncheck.AuditRecord {
	t.Helper()

	c := r.cases[name]
	require.LessOrEqual(t, c.Audit[0], c.Audit[1], "case %s: the audit range", name)
	require.LessOrEqual(t, c.Audit[1], len(r.audit), "case %s: the audit range lies in the audit records", name)

	return r.audit[c.Audit[0]:c.Audit[1]]
}

// ofKind returns the records of one kind, in recorded order.
func ofKind(records []lessoncheck.AuditRecord, kind string) []lessoncheck.AuditRecord {
	var out []lessoncheck.AuditRecord

	for _, rec := range records {
		if rec.Kind == kind {
			out = append(out, rec)
		}
	}

	return out
}

// guardRecord returns the guard record of the first observed call of a
// tool. It never substitutes another call's record when that join fails.
func (r guardRecording) guardRecord(t *testing.T, name, tool string) (lessoncheck.AuditRecord, bool) {
	t.Helper()

	for _, e := range r.events[r.runs[name]] {
		if e.hook.Event == "PreToolUse" && e.tool() == tool {
			return r.guardRecordOf(name, e)
		}
	}

	return lessoncheck.AuditRecord{}, false
}

// guardRecordOf requires one metadata row and one audit record for this
// event, case, client, session, call, and receiving context. Ambiguity is unknown.
func (r guardRecording) guardRecordOf(name string, e timingEvent) (lessoncheck.AuditRecord, bool) {
	c, exists := r.cases[name]
	client := r.manifest.Client
	receiver := ReceivingContextOf(client, nativeString(e.raw, "session_id"), nativeString(e.raw, "agent_id"))
	validRange := exists && c.Audit[0] >= 0 && c.Audit[0] <= c.Audit[1] && c.Audit[1] <= len(r.audit)
	if !validRange || c.Run <= 0 || r.runs[name] != c.Run || e.hook.Run != c.Run || e.hook.File == "" ||
		e.hook.Event != "PreToolUse" || receiver == nil || nativeString(e.raw, "tool_use_id") == "" {
		return lessoncheck.AuditRecord{}, false
	}
	var identity guardIdentity
	count := 0
	for _, id := range r.identities {
		if id.File == e.hook.File {
			identity, count = id, count+1
		}
	}
	if count != 1 || identity.Run != c.Run || identity.Case != name || identity.Client != client ||
		identity.Tool != e.tool() || identity.Lane != receiver.Lane ||
		!guardDigest(identity.SessionSHA) || !guardDigest(identity.MatchSHA) || !guardDigest(identity.ContextSHA) {
		return lessoncheck.AuditRecord{}, false
	}
	// A repeated call identity cannot identify one observed attempt.
	for _, id := range r.identities {
		if id.File != identity.File && id.Client == client && id.MatchSHA == identity.MatchSHA {
			return lessoncheck.AuditRecord{}, false
		}
	}
	var found lessoncheck.AuditRecord
	count = 0
	for _, rec := range ofKind(r.audit[c.Audit[0]:c.Audit[1]], lessoncheck.AuditGuard) {
		if rec.Client == client && rec.Tool == identity.Tool && rec.SessionSHA == identity.SessionSHA &&
			rec.MatchSHA == identity.MatchSHA && rec.ContextSHA == identity.ContextSHA && rec.Guard != nil {
			found, count = rec, count+1
		}
	}
	return found, count == 1
}

// guardDigest accepts only the lowercase hex SHA-256 form that the
// collector writes.
func guardDigest(s string) bool {
	raw, err := hex.DecodeString(s)

	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == s
}

// postEditOf returns the post-edit record of the call that a record
// names, by its match digest, or false. Both records come from one
// recording, so their digests share one identity root (contract K9).
func (r guardRecording) postEditOf(t *testing.T, name string, call lessoncheck.AuditRecord) (lessoncheck.AuditRecord, bool) {
	t.Helper()

	for _, rec := range ofKind(r.records(t, name), lessoncheck.AuditPostEdit) {
		if call.MatchSHA != "" && rec.MatchSHA == call.MatchSHA {
			return rec, true
		}
	}

	return lessoncheck.AuditRecord{}, false
}

// has reports whether the run of a case holds a payload of the event,
// and of the tool when tool is not empty.
func (r guardRecording) has(name, event, tool string) bool {
	return slices.ContainsFunc(r.events[r.runs[name]], func(e timingEvent) bool {
		return e.hook.Event == event && (tool == "" || e.tool() == tool)
	})
}

// opPaths returns the paths of every file operation relative to the
// placeholder workspace, with the destination of a move after its
// source.
func (r guardRecording) opPaths(op lessoncheck.Operation) []string {
	var out []string

	for _, fop := range op.Ops {
		out = append(out, r.rel(op.CWD, fop.Path))

		if fop.MoveTo != "" {
			out = append(out, r.rel(op.CWD, fop.MoveTo))
		}
	}

	return out
}

// guardOp decodes a recorded pre-write payload with the production
// guard decoder of the client.
func guardOp(t *testing.T, client string, e timingEvent) lessoncheck.Operation {
	t.Helper()

	var (
		op  lessoncheck.Operation
		err error
	)

	switch client {
	case ClaudeID:
		op, err = claudeGuards{}.DecodeGuard(e.payload)
	case CodexID:
		op, err = codexGuards{}.DecodeGuard(e.payload)
	default:
		require.Failf(t, "unknown client", "%s", client)
	}

	require.NoError(t, err, e.hook.File)

	return op
}

// assertDenied checks a case whose first attempt the guard denies: the
// payload of the tool, a deny record that wrote its reply with the exit
// protocol, no post-edit record of the same call, and fixture
// directories that the run left unchanged. It returns the payload and
// the deny record.
func (r guardRecording) assertDenied(t *testing.T, name, tool string) (timingEvent, lessoncheck.AuditRecord) {
	t.Helper()

	e := r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+tool)[1]

	deny, ok := r.guardRecord(t, name, tool)
	require.True(t, ok, "case %s: the guard wrote a record of the %s call", name, tool)
	assert.Equal(t, lessoncheck.GuardDeny, deny.Guard.Decision, "case %s: the guard denied the call", name)
	assert.True(t, deny.Guard.ReplyWritten, "case %s: the guard wrote its deny reply", name)
	assert.Equal(t, lessoncheck.GuardProtocolExit2, deny.Guard.Protocol, name)

	_, ran := r.postEditOf(t, name, deny)
	assert.False(t, ran, "case %s: a post-edit record names the denied call, so the client ran the tool after the deny", name)

	c := r.cases[name]
	assert.True(t, c.unchanged(), "case %s: the denied call changed the fixture: before %v, after %v",
		name, c.Before, c.After)

	return e, deny
}

// assertGuardStatus checks the exit status of the guard command in the
// Claude Code debug log of a case. A recording without any hook result
// (a Codex recording, or a debug log in another format) skips the check
// with a note, because the payloads and the audit hold the other facts.
func (r guardRecording) assertGuardStatus(t *testing.T, name string, status int) {
	t.Helper()

	if len(r.hooks) == 0 {
		t.Logf("case %s: the recording holds no hook result; read run-%d-debug.log for the exit status", name, r.runs[name])

		return
	}

	assert.True(t, slices.ContainsFunc(r.hooks, func(h guardHookResult) bool {
		return h.Run == r.runs[name] && h.Hook == "guard" && h.Status != nil && *h.Status == status
	}), "case %s: the debug log shows the guard command with exit status %d", name, status)
}

// assertChild checks that a payload names the child lane of the agent
// that SubagentStart announced, and that the deny record names its
// receiver.
func assertChild(t *testing.T, client string, start, e timingEvent, deny lessoncheck.AuditRecord) {
	t.Helper()

	session := nativeString(e.raw, "session_id")
	agent := nativeString(start.raw, "agent_id")
	require.NotEmpty(t, agent, "SubagentStart names the agent")
	assert.Equal(t, agent, nativeString(e.raw, "agent_id"), "the pre-write payload names the agent")

	receiver := ReceivingContextOf(client, session, agent)
	require.NotNil(t, receiver)
	assert.Equal(t, LaneChild, receiver.Lane, "the subagent write is in the child lane")
	assert.NotEmpty(t, deny.ContextSHA, "the deny record names its receiver")
}

// assertExecuted checks a case whose call the client ran: the post-edit
// record of the call names the outcome, one of the attributions, and the
// state, and the run created the file p.
func (r guardRecording) assertExecuted(t *testing.T, name string, call lessoncheck.AuditRecord, p string,
	state lessoncheck.SummaryState, attributions ...lessoncheck.Attribution,
) lessoncheck.AuditRecord {
	t.Helper()

	post, ok := r.postEditOf(t, name, call)
	require.True(t, ok, "case %s: a post-edit record names the call", name)
	assert.Equal(t, lessoncheck.ToolSucceeded, post.ToolOutcome, name)
	assert.Contains(t, attributions, post.Attribution, name)
	assert.Equal(t, state, post.State, name)
	assert.True(t, r.cases[name].created(p), "case %s: the call created %s", name, p)
	t.Logf("case %s: post-edit attribution %s", name, post.Attribution)

	return post
}

// assertEmitted checks that a post-edit record wrote its reply.
func assertEmitted(t *testing.T, name string, post lessoncheck.AuditRecord) {
	t.Helper()

	require.NotNil(t, post.Emitted, name)
	assert.True(t, *post.Emitted, "case %s: the post-edit hook wrote its findings", name)
}

// assertWrapper checks the report-only wrapper case: the indirection
// entry ran the guard, which denied, and discarded its exit status. The
// client then ran the tool, so a post-edit record of the same call
// exists without a baseline (contract K9, "executed after deny").
func (r guardRecording) assertWrapper(t *testing.T, tool, p string) {
	t.Helper()

	name := guardReportOnlyWrapper
	r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+tool)

	assert.Equal(t, []string{"report-only 2"}, r.cases[name].PreWrite, "the wrapper ran the guard once and it denied")

	deny, ok := r.guardRecord(t, name, tool)
	require.True(t, ok, "the guard of the wrapper wrote a record")
	assert.Equal(t, lessoncheck.GuardDeny, deny.Guard.Decision)

	r.assertExecuted(t, name, deny, p, lessoncheck.SummaryIncomplete, lessoncheck.AttributionNoBaseline)
}

// assertSlow checks the slow-guard case and logs the native timeout
// rule that it shows. The client either waited for the hook, so the
// guard ran and denied, or it stopped the hook at the timeout. Both are
// valid answers, so the test checks only that the facts agree.
func (r guardRecording) assertSlow(t *testing.T, tool, p string) {
	t.Helper()

	name := guardSlowGuard
	r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+tool)

	c := r.cases[name]
	waited := slices.Contains(c.PreWrite, "slow 2")

	if waited {
		assert.False(t, c.created(p), "the client waited for the slow guard, which denied, so the file must not exist")

		_, ok := r.guardRecord(t, name, tool)
		assert.True(t, ok, "the slow guard wrote its record")
	}

	t.Logf("native timeout of %s %s: hook waited to the end: %t; the tool created %s: %t",
		r.manifest.Client, r.manifest.ClientVersion, waited, p, c.created(p))
}

// assertCapability checks the capability report of each phase against
// the contract K10 rules. No pin reads enforced unless every pre-write
// path of the client is active with verified evidence.
func (r guardRecording) assertCapability(t *testing.T, client string) {
	t.Helper()

	require.NotEmpty(t, r.capability, "the recording holds the capability report of each phase")

	for _, phase := range r.capability {
		require.NotNil(t, phase.LessonChecks, "phase %s: status --json held a lesson_checks report", phase.Phase)

		c, ok := phase.LessonChecks.Client(client)
		require.True(t, ok, "phase %s: the report names %s", phase.Phase, client)

		var preWrite []lessoncheck.PathCapability

		for _, p := range c.Paths {
			if p.HookStage == lessoncheck.StagePreWrite {
				preWrite = append(preWrite, p)
			}
		}

		require.NotEmpty(t, preWrite, phase.Phase)

		enforced := !slices.ContainsFunc(preWrite, func(p lessoncheck.PathCapability) bool {
			return p.State != lessoncheck.PathActive || p.Verification.Level != lessoncheck.LevelVerified
		})

		for _, pin := range phase.LessonChecks.Pins {
			for _, pc := range pin.Clients {
				if pc.Client == client && pc.Badge == lessoncheck.BadgeEnforced {
					assert.True(t, enforced, "phase %s: pin %s reads enforced without verified pre-write paths", phase.Phase, pin.PinRef)
				}
			}
		}

		for _, p := range preWrite {
			label := phase.Phase + " " + p.Tool + " " + string(p.Context)

			switch {
			case phase.Phase == "enforce" && p.Verification.Level != lessoncheck.LevelVerified:
				assert.NotEqual(t, lessoncheck.PathActive, p.State, label)
				assert.Subset(t, p.Reasons, []lessoncheck.CapReason{
					lessoncheck.CapNativeDenyUnverified, lessoncheck.CapNativeTimeoutUnverified,
				}, label)
			case phase.Phase == "wrapper-inline":
				assert.Equal(t, lessoncheck.PathInactive, p.State, label)
				assert.Contains(t, p.Reasons, lessoncheck.CapReportOnlyWrapper, label)
			case phase.Phase == "broken-config":
				assert.Equal(t, lessoncheck.PathInactive, p.State, label)
				assert.Contains(t, p.Reasons, lessoncheck.CapConfigurationError, label)
			case slices.Contains(guardPhasesWithoutGuard, phase.Phase):
				assert.Equal(t, lessoncheck.PathInactive, p.State, label)
				assert.Contains(t, p.Reasons, lessoncheck.CapNotInstalled, label)
			}
		}

		if phase.Phase == "check-installed" {
			assert.Equal(t, lessoncheck.InstalledCheck, c.Installed, "init --lesson-checks check installed the baseline hook")
		}
	}
}

func TestCaptureNativeClaudeGuardRecording(t *testing.T) {
	r := loadGuardRecording(t, ClaudeID, claudeGuardCases...)

	t.Run("the capability report claims no enforcement without evidence", func(t *testing.T) {
		r.assertCapability(t, ClaudeID)
	})

	for _, tc := range []struct{ name, tool, path string }{
		{guardEditViolation, claudeToolEdit, "sentinel/edit.txt"},
		{guardWriteViolation, claudeToolWrite, "sentinel/written.txt"},
	} {
		t.Run("a first "+tc.tool+" in the sentinel scope is denied before it writes", func(t *testing.T) {
			e, deny := r.assertDenied(t, tc.name, tc.tool)
			op := guardOp(t, ClaudeID, e)
			assert.Equal(t, []string{tc.path}, r.opPaths(op))
			assert.Equal(t, lessoncheck.GuardCauseViolation, deny.Guard.Cause)
			assert.Equal(t, LaneRoot, claudeContext(op.SessionID, nativeString(e.raw, "agent_id")).Lane)
			r.assertGuardStatus(t, tc.name, 2)
		})
	}

	t.Run("a MultiEdit with one violating step writes nothing", func(t *testing.T) {
		name := guardMultiEditViolation
		if !r.has(name, "PreToolUse", claudeToolMultiEdit) {
			require.Equal(t, "no-multiedit", r.cases[name].Reply,
				"no MultiEdit payload and no no-multiedit reply: the model did not follow the prompt, so record the case again")
			t.Skipf("Claude Code %s offered no MultiEdit tool; the MultiEdit path keeps no native evidence",
				r.manifest.ClientVersion)
		}

		e, deny := r.assertDenied(t, name, claudeToolMultiEdit)
		op := guardOp(t, ClaudeID, e)
		require.Len(t, op.Ops, 1)
		assert.Equal(t, lessoncheck.OpMultiEdit, op.Ops[0].Kind)
		assert.Len(t, op.Ops[0].Edits, 2, "one call holds the permitted and the violating step")
		assert.Equal(t, []string{"notes/multi.txt"}, r.opPaths(op))
		assert.Equal(t, lessoncheck.GuardCauseViolation, deny.Guard.Cause)
		r.assertGuardStatus(t, name, 2)
	})

	t.Run("a subagent Write is denied in the child lane", func(t *testing.T) {
		name := guardSubagentViolation
		steps := r.sequence(t, name, "UserPromptSubmit", "SubagentStart", "PreToolUse:"+claudeToolWrite)

		e, deny := r.assertDenied(t, name, claudeToolWrite)
		assert.Equal(t, []string{"sentinel/child.txt"}, r.opPaths(guardOp(t, ClaudeID, e)))
		assertChild(t, ClaudeID, steps[1], e, deny)
		r.assertGuardStatus(t, name, 2)
	})

	t.Run("an Edit that the guard cannot project is denied", func(t *testing.T) {
		name := guardUnknownEdit
		if !r.has(name, "PreToolUse", claudeToolEdit) {
			t.Skipf("case %s: no PreToolUse Edit payload; the client refused the call before the hook, "+
				"or the model made no call (gate-5 checklist)", name)
		}

		_, deny := r.assertDenied(t, name, claudeToolEdit)
		assert.Equal(t, lessoncheck.GuardCauseUnknown, deny.Guard.Cause)
		assert.Contains(t, deny.Guard.Reasons, lessoncheck.ReasonUnprojectableEdit)
	})

	t.Run("one Edit on an absent file outside every pin (N1)", func(t *testing.T) {
		name := guardN1AbsentEdit
		if !r.has(name, "PreToolUse", claudeToolEdit) {
			t.Skipf("case %s: no PreToolUse Edit payload; the client refused the call before the hook, "+
				"or the model made no call (gate-5 checklist)", name)
		}

		e := r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+claudeToolEdit)[1]
		op := guardOp(t, ClaudeID, e)
		require.Len(t, op.Ops, 1)
		require.Len(t, op.Ops[0].Edits, 1)
		assert.Empty(t, op.Ops[0].Edits[0].Old, "the Edit has an empty old_string")
		assert.Equal(t, []string{"free/n1.txt"}, r.opPaths(op))

		allow, ok := r.guardRecord(t, name, claudeToolEdit)
		require.True(t, ok, "the guard wrote a record")
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision, "no enforce pin scopes free/")

		// P0 review ruling N1: the K8 Edit row stays unprojectable-edit
		// until this case shows that one Edit creates the absent file.
		assert.False(t, r.cases[name].created("free/n1.txt"),
			"N1: one Edit created an absent file on Claude Code %s; the K8 Edit row must now project an add "+
				"with new_string (P0 review N1), and this assertion changes with it", r.manifest.ClientVersion)
	})

	t.Run("a permitted Write completes and its post-edit check passes", func(t *testing.T) {
		name := guardPermittedWrite
		r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+claudeToolWrite)

		allow, ok := r.guardRecord(t, name, claudeToolWrite)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision)
		assert.NotEmpty(t, allow.Guard.Pins, "the pattern pin of notes/ was evaluated")

		r.assertExecuted(t, name, allow, "notes/permitted.txt", lessoncheck.SummaryPassed,
			lessoncheck.AttributionProjectionMatch)
	})

	t.Run("an invalid check set denies a write outside every pin", func(t *testing.T) {
		_, deny := r.assertDenied(t, guardConfigError, claudeToolWrite)
		assert.Equal(t, lessoncheck.GuardCauseConfigurationError, deny.Guard.Cause)
		r.assertGuardStatus(t, guardConfigError, 2)
	})

	t.Run("check pins never block, and the post-edit hook reports the violation", func(t *testing.T) {
		name := guardCheckOnly
		r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+claudeToolWrite, "PostToolUse:"+claudeToolWrite)

		allow, ok := r.guardRecord(t, name, claudeToolWrite)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision, "check-mode findings cannot deny")

		post := r.assertExecuted(t, name, allow, "sentinel/checked.txt", lessoncheck.SummaryViolation,
			lessoncheck.AttributionProjectionMatch)
		assertEmitted(t, name, post)
	})

	t.Run("an installed check mode stores a baseline and never blocks", func(t *testing.T) {
		name := guardCheckInstalled
		r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+claudeToolWrite, "PostToolUse:"+claudeToolWrite)

		assert.Empty(t, ofKind(r.records(t, name), lessoncheck.AuditGuard), "init --lesson-checks check installs no guard")

		baselines := ofKind(r.records(t, name), lessoncheck.AuditBaseline)
		require.NotEmpty(t, baselines, "the baseline hook wrote its record")
		assert.Empty(t, baselines[0].Error, "the baseline hook stored the pending baseline")

		post := r.assertExecuted(t, name, baselines[0], "sentinel/baseline.txt", lessoncheck.SummaryViolation,
			lessoncheck.AttributionProjectionMatch)
		assertEmitted(t, name, post)
	})

	t.Run("a failed Edit without a baseline gets a PostToolUseFailure reply", func(t *testing.T) {
		name := guardFailureReach
		if !r.has(name, "PreToolUse", claudeToolEdit) {
			t.Skipf("case %s: no PreToolUse Edit payload; the model made no Edit call (gate-5 checklist)", name)
		}

		if !r.has(name, "PostToolUseFailure", claudeToolEdit) {
			t.Skipf("case %s: Claude Code %s sent no PostToolUseFailure for the failed Edit; the failure reply stays "+
				"unverified (contracts §5 row 38)", name, r.manifest.ClientVersion)
		}

		assert.Empty(t, ofKind(r.records(t, name), lessoncheck.AuditGuard), "the phase has no pre-write hook")

		posts := ofKind(r.records(t, name), lessoncheck.AuditPostEdit)
		require.NotEmpty(t, posts)
		assert.Equal(t, lessoncheck.ToolFailed, posts[0].ToolOutcome)
		assert.Equal(t, lessoncheck.AttributionNoBaseline, posts[0].Attribution)
		assert.Equal(t, lessoncheck.SummaryIncomplete, posts[0].State)
		assertEmitted(t, name, posts[0])
		assert.True(t, r.cases[name].unchanged(), "the failed Edit changed nothing")
	})

	t.Run("a report-only wrapper lets the denied Write run", func(t *testing.T) {
		r.assertWrapper(t, claudeToolWrite, "sentinel/wrapped.txt")
	})

	t.Run("a guard slower than its timeout", func(t *testing.T) {
		r.assertSlow(t, claudeToolWrite, "sentinel/slow.txt")
	})
}

func TestCaptureNativeCodexGuardRecording(t *testing.T) {
	r := loadGuardRecording(t, CodexID, codexGuardCases...)

	t.Run("the capability report claims no enforcement without evidence", func(t *testing.T) {
		r.assertCapability(t, CodexID)
	})

	for _, tc := range []struct {
		name  string
		kinds []lessoncheck.OpKind
		paths []string
	}{
		{guardAddViolation, []lessoncheck.OpKind{lessoncheck.OpPatchAdd}, []string{"sentinel/added.txt"}},
		{guardUpdateViolation, []lessoncheck.OpKind{lessoncheck.OpPatchUpdate}, []string{"sentinel/update.txt"}},
		{guardDeleteViolation, []lessoncheck.OpKind{lessoncheck.OpPatchDelete}, []string{"sentinel/delete.txt"}},
		{guardMoveViolation, []lessoncheck.OpKind{lessoncheck.OpPatchMove}, []string{"sentinel/move.txt", "free/moved.txt"}},
		{guardMultiFileViolation, []lessoncheck.OpKind{lessoncheck.OpPatchAdd, lessoncheck.OpPatchAdd},
			[]string{"notes/multi-ok.txt", "sentinel/multi.txt"}},
	} {
		t.Run("the patch of "+tc.name+" is denied before it writes", func(t *testing.T) {
			e, deny := r.assertDenied(t, tc.name, codexApplyPatch)
			op := guardOp(t, CodexID, e)

			// An update with a Move to line and hunks moves the file too,
			// so it counts as a move here.
			var kinds []lessoncheck.OpKind

			for _, fop := range op.Ops {
				kind := fop.Kind
				if kind == lessoncheck.OpPatchUpdate && fop.MoveTo != "" {
					kind = lessoncheck.OpPatchMove
				}

				kinds = append(kinds, kind)
			}

			assert.Equal(t, tc.kinds, kinds, "the patch sections; if the model wrote another patch, record the case again")
			assert.ElementsMatch(t, tc.paths, r.opPaths(op))
			assert.Equal(t, lessoncheck.GuardCauseViolation, deny.Guard.Cause)
			assert.Empty(t, nativeString(e.raw, "agent_id"), "the parent wrote the patch")
		})
	}

	t.Run("a subagent patch is denied in the child lane", func(t *testing.T) {
		name := guardSubagentViolation
		steps := r.sequence(t, name, "UserPromptSubmit", "SubagentStart", "PreToolUse:"+codexApplyPatch)

		e, deny := r.assertDenied(t, name, codexApplyPatch)
		assert.Equal(t, []string{"sentinel/child.txt"}, r.opPaths(guardOp(t, CodexID, e)))
		assertChild(t, CodexID, steps[1], e, deny)
	})

	t.Run("an update in the pattern scope is denied as unknown (A8)", func(t *testing.T) {
		e, deny := r.assertDenied(t, guardUpdateUnknown, codexApplyPatch)
		op := guardOp(t, CodexID, e)
		require.Len(t, op.Ops, 1)
		assert.Equal(t, lessoncheck.OpPatchUpdate, op.Ops[0].Kind)
		assert.True(t, op.Ops[0].Hunks, "the update carries hunks")
		assert.Equal(t, lessoncheck.GuardCauseUnknown, deny.Guard.Cause)
		assert.Contains(t, deny.Guard.Reasons, lessoncheck.ReasonUnprojectableEdit)
	})

	t.Run("a permitted patch completes and its post-edit check passes", func(t *testing.T) {
		name := guardPermittedWrite
		r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+codexApplyPatch)

		allow, ok := r.guardRecord(t, name, codexApplyPatch)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision)
		assert.NotEmpty(t, allow.Guard.Pins, "the pattern pin of notes/ was evaluated")

		r.assertExecuted(t, name, allow, "notes/permitted.txt", lessoncheck.SummaryPassed,
			codexAddAttributions...)
	})

	t.Run("an update outside every pin is the native content case of A8", func(t *testing.T) {
		name := guardUpdatePermitted
		e := r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+codexApplyPatch)[1]
		op := guardOp(t, CodexID, e)
		require.Len(t, op.Ops, 1)
		assert.Equal(t, lessoncheck.OpPatchUpdate, op.Ops[0].Kind)
		assert.Equal(t, []string{"free/oracle.txt"}, r.opPaths(op))

		allow, ok := r.guardRecord(t, name, codexApplyPatch)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision, "no enforce pin scopes free/")

		c := r.cases[name]
		require.NotEqual(t, c.Before.Text["free/oracle.txt"], c.After.Text["free/oracle.txt"], "the native applier changed the file")

		// The patch, the content before, and the content after are one
		// native content case for the update applier (plan step P4.12).
		input, _ := e.raw["tool_input"].(map[string]any)
		t.Logf("A8 content case of codex-cli %s:\npatch: %q\nbefore: %q\nafter: %q", r.manifest.ClientVersion,
			nativeString(input, "command"), c.Before.Text["free/oracle.txt"], c.After.Text["free/oracle.txt"])
	})

	t.Run("an invalid check set denies a patch outside every pin", func(t *testing.T) {
		_, deny := r.assertDenied(t, guardConfigError, codexApplyPatch)
		assert.Equal(t, lessoncheck.GuardCauseConfigurationError, deny.Guard.Cause)
	})

	t.Run("check pins never block, and the post-edit hook reports the violation", func(t *testing.T) {
		name := guardCheckOnly
		r.sequence(t, name, "UserPromptSubmit", "PreToolUse:"+codexApplyPatch, "PostToolUse:"+codexApplyPatch)

		allow, ok := r.guardRecord(t, name, codexApplyPatch)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.GuardAllow, allow.Guard.Decision, "check-mode findings cannot deny")

		post := r.assertExecuted(t, name, allow, "sentinel/checked.txt", lessoncheck.SummaryViolation,
			codexAddAttributions...)
		assertEmitted(t, name, post)
	})

	t.Run("a report-only wrapper lets the denied patch run", func(t *testing.T) {
		r.assertWrapper(t, codexApplyPatch, "sentinel/wrapped.txt")
	})

	t.Run("a guard slower than its timeout", func(t *testing.T) {
		r.assertSlow(t, codexApplyPatch, "sentinel/slow.txt")
	})
}
