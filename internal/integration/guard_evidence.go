package integration

import (
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// guardTools lists, per client, the tools of its pre-write guard path.
// The guard decoders read the same lists, so a tool has a pre-write path
// exactly when the guard decodes it.
var guardTools = map[string][]string{
	ClaudeID: claudeGuardTools,
	CodexID:  codexGuardTools,
}

// recordedEvidence names the reviewed native recording that verifies one
// path or item: the client version that the recording ran against and
// the surface that it exercised.
type recordedEvidence struct {
	clientVersion string
	surface       string
}

// guardPath is the key of one pre-write path: client, tool, and context.
type guardPath struct {
	client  string
	tool    string
	context lessoncheck.PathContext
}

// slowProof names the case that proves that the client waits for a slow
// guard and honors its deny. A deny alone does not show what the client
// does with a guard that takes long.
type slowProof int

const (
	// slowPastTimeout is the slow-guard case. The guard runs past the hook
	// timeout, and the client still waits for it and honors the deny.
	slowPastTimeout slowProof = iota + 1
	// slowInBudget is an in-budget slow case. The guard is slow but ends
	// inside the hook timeout, and the client waits for it and honors the
	// deny. A client that stops its hook at the timeout can still give
	// this proof.
	slowInBudget
)

// guardProof is the reviewed evidence of one pre-write path: the
// recording and the kind of its slow proof.
type guardProof struct {
	recordedEvidence
	slow slowProof
}

// claudeEvidenceVersion is the client version of the reviewed Claude
// Code recordings in testdata/claude/guard and testdata/claude/timing.
// Both recordings ran on this version, so the guard, timeout, and timing
// entries of Claude Code name it. The post-edit recording ran on a later
// version, so its entries name claudePostEditEvidenceVersion.
const claudeEvidenceVersion = "Claude Code 2.1.319"

// codexEvidenceVersion is the client version of the reviewed Codex
// recordings in testdata/codex/guard, testdata/codex/timing, and
// testdata/codex/post-edit. All three recordings ran on this version,
// so the guard, timeout, timing, and post-edit entries of Codex name it.
// timeoutLimit and postEditTimeoutLimit find the timeout outcomes by
// this exact string, so the proofs and the timeout keys share it.
const codexEvidenceVersion = "codex-cli 0.169.0"

// guardRecorded lists the pre-write paths that a reviewed native guard
// recording verifies. A reviewer adds an entry only after a review of
// the recording in testdata/<client>/guard. The recording must show a
// deny of the tool in the context and a slow proof of the same path.
// One verified level removes both reasons native-deny-unverified and
// native-timeout-unverified, so it needs proof of both. No path takes
// the proof of another path.
// TestGuardEvidenceMatchesRecordings checks each entry against the
// recording.
//
// The Claude Code recording shows a blocked deny of Write and Edit in
// the root and the child context. Each of these paths also has an
// in-budget slow deny: a slow pre-write hook ends inside the 10 s hook
// timeout, and the client waits for it and honors the deny. Claude
// Code stops a slower hook at the timeout and lets the write run
// (timeoutRecorded), so no Claude Code path has a slowPastTimeout
// proof. MultiEdit has no entry, because the client offers no
// MultiEdit tool. The model's replies in the 2.1.319 recording say so,
// and TestNativeClaudeOffersNoMultiEditTool shows it on 2.1.325.
//
// The Codex recording shows a blocked deny of apply_patch in the root
// and the child context, and an in-budget slow deny of each. Codex also
// stops a slower hook at the timeout and lets the patch run, so its
// paths have no slowPastTimeout proof either.
var guardRecorded = map[guardPath]guardProof{
	{client: ClaudeID, tool: claudeToolEdit, context: lessoncheck.ContextRoot}: inBudgetProof(
		claudeEvidenceVersion, claudeToolEdit, lessoncheck.ContextRoot),
	{client: ClaudeID, tool: claudeToolEdit, context: lessoncheck.ContextChild}: inBudgetProof(
		claudeEvidenceVersion, claudeToolEdit, lessoncheck.ContextChild),
	{client: ClaudeID, tool: claudeToolWrite, context: lessoncheck.ContextRoot}: inBudgetProof(
		claudeEvidenceVersion, claudeToolWrite, lessoncheck.ContextRoot),
	{client: ClaudeID, tool: claudeToolWrite, context: lessoncheck.ContextChild}: inBudgetProof(
		claudeEvidenceVersion, claudeToolWrite, lessoncheck.ContextChild),
	{client: CodexID, tool: codexApplyPatch, context: lessoncheck.ContextRoot}: inBudgetProof(
		codexEvidenceVersion, codexApplyPatch, lessoncheck.ContextRoot),
	{client: CodexID, tool: codexApplyPatch, context: lessoncheck.ContextChild}: inBudgetProof(
		codexEvidenceVersion, codexApplyPatch, lessoncheck.ContextChild),
}

// inBudgetProof returns the proof of one pre-write path with an
// in-budget slow deny, recorded on the given client version. The
// surface is the surface of the pending evidence of the path, so a
// review changes the level and the version of a path, not the surface
// that it names.
func inBudgetProof(version, tool string, context lessoncheck.PathContext) guardProof {
	return guardProof{
		recordedEvidence: recordedEvidence{clientVersion: version, surface: guardSurface(tool, context)},
		slow:             slowInBudget,
	}
}

// guardSurface names the native surface of one pre-write path: the
// PreToolUse event of the tool, its exit 2 deny, and the context.
func guardSurface(tool string, context lessoncheck.PathContext) string {
	return "PreToolUse " + tool + " exit 2 in the " + string(context) + " context"
}

// timeoutOutcome is what a client does when its pre-write hook runs past
// the hook timeout.
type timeoutOutcome int

const (
	// timeoutWaits: the client waits for the hook and honors its exit
	// status.
	timeoutWaits timeoutOutcome = iota + 1
	// timeoutFailOpen: the client stops the hook and lets the write run.
	timeoutFailOpen
	// timeoutFailClosed: the client stops the hook and blocks the write.
	timeoutFailClosed
)

// clientVersion is the key of a fact about one version of one client.
type clientVersion struct {
	client  string
	version string
}

// timeoutDisposition is the reviewed timeout outcome of one client
// version, from the slow-guard case of its guard recording. seconds is
// the hook timeout that the recording ran with.
type timeoutDisposition struct {
	outcome timeoutOutcome
	seconds int
}

// timeoutRecorded lists the timeout outcome per client version. It is a
// fact of the client hook runner, separate from the proof of each path.
// It does not verify a path. It only gives the limit text of a verified
// path. The key version uses the words of recordedEvidence, so a path
// entry finds the fact of its own version.
// TestGuardEvidenceMatchesRecordings checks each entry against the
// recording.
//
// In the slow-guard case of each recording, the client stops the
// pre-write hook after 10 s and the write runs. Claude Code shows this
// with its own timeout message. Codex shows it only by the 10 s gap
// between PreToolUse and PostToolUse, a guard that never logged, and
// the file that the patch added. So each verified path of both clients
// names that limit.
var timeoutRecorded = map[clientVersion]timeoutDisposition{
	{client: ClaudeID, version: claudeEvidenceVersion}: {outcome: timeoutFailOpen, seconds: 10},
	{client: CodexID, version: codexEvidenceVersion}:   {outcome: timeoutFailOpen, seconds: 10},
}

// timeoutLimit returns the limit text of a verified path of the client
// version: what happens to a write when the guard runs past the hook
// timeout. A client that waits or blocks gives "", because the deny then
// holds in every case.
func timeoutLimit(clientID, version string) string {
	d, ok := timeoutRecorded[clientVersion{client: clientID, version: version}]
	if !ok || d.outcome != timeoutFailOpen {
		return ""
	}

	return fmt.Sprintf("the client lets the write run after its %d s hook timeout", d.seconds)
}

// GuardEvidence returns the native evidence that a deny of the guard
// stops the tool, for one client, tool, and context path. The guard
// copies its level into the native_blocking field of its decision, and
// the capability report shows it per pre-write path.
//
// The evidence is static, because inspection runs no client binary. A
// path in guardRecorded is verified, with the client version and the
// surface of its reviewed recording. When that client version lets the
// write run after the hook timeout, the evidence carries that limit, so
// every surface that shows the path enforced also names the limit.
// Every other pre-write path is pending. The pending evidence names no
// client version, because the recording sets the version it ran
// against.
//
// A tool or a context without a pre-write path gives the zero value,
// which means unknown. Both built-in clients name a subagent in their
// edit events, so every context of lessonContexts has a path of its own.
func GuardEvidence(clientID, tool string, context lessoncheck.PathContext) VerificationEvidence {
	if !slices.Contains(guardTools[clientID], tool) || !slices.Contains(lessonContexts, context) {
		return VerificationEvidence{}
	}

	if r, ok := guardRecorded[guardPath{client: clientID, tool: tool, context: context}]; ok {
		e := mustVerifiedEvidence(r.clientVersion, r.surface)
		e.Limit = timeoutLimit(clientID, r.clientVersion)

		return e
	}

	return VerificationEvidence{Level: VerificationPending, Surface: guardSurface(tool, context)}
}
