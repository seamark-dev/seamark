package integration

import (
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// claudePostEditEvidenceVersion is the client version of the reviewed
// Claude Code recording in testdata/claude/post-edit. The guard and
// timing recordings ran on an earlier version (claudeEvidenceVersion),
// so the post-edit entries of Claude Code name this version instead.
// postEditTimeoutLimit finds the timeout outcome by this exact string,
// so the post-edit entries and the timeout key share it.
const claudePostEditEvidenceVersion = "Claude Code 2.1.325"

// postEditPath is the key of one post-edit path: client, tool, and
// context.
type postEditPath struct {
	client  string
	tool    string
	context lessoncheck.PathContext
}

// postEditRecorded lists the post-edit paths that a reviewed native
// post-edit recording verifies. A reviewer adds an entry only after a
// review of the recording in testdata/<client>/post-edit. The
// recording must show an in-budget slow reply of the tool in the
// context: a post-edit hook that waits inside the hook timeout, runs
// the verify command to the end, and writes its reply. The review reads
// from the transcript that the reply reaches the model before its next
// step. No path takes the proof of another path.
// TestPostEditEvidenceMatchesRecordings checks each entry against the
// recording.
//
// Each entry covers the PostToolUse event only. The Claude Code
// recording holds no failed tool, so the PostToolUseFailure reply keeps
// only the proof of the 2.1.319 guard recording, and no recording has a
// slow hook on that event.
//
// The Claude Code recording shows an in-budget slow reply of Write and
// Edit in the root and the child context. It also shows the plain reply
// of the owned verify entry for an Edit in both contexts. MultiEdit has
// no entry, because the client offers no MultiEdit tool.
//
// The Codex recording shows an in-budget slow reply of apply_patch in
// the root and the child context. Its paths keep trust-unknown, so they
// read verified evidence but never the state active.
var postEditRecorded = map[postEditPath]recordedEvidence{
	{client: ClaudeID, tool: claudeToolEdit, context: lessoncheck.ContextRoot}: postEditProof(
		claudePostEditEvidenceVersion, claudeToolEdit, lessoncheck.ContextRoot),
	{client: ClaudeID, tool: claudeToolEdit, context: lessoncheck.ContextChild}: postEditProof(
		claudePostEditEvidenceVersion, claudeToolEdit, lessoncheck.ContextChild),
	{client: ClaudeID, tool: claudeToolWrite, context: lessoncheck.ContextRoot}: postEditProof(
		claudePostEditEvidenceVersion, claudeToolWrite, lessoncheck.ContextRoot),
	{client: ClaudeID, tool: claudeToolWrite, context: lessoncheck.ContextChild}: postEditProof(
		claudePostEditEvidenceVersion, claudeToolWrite, lessoncheck.ContextChild),
	{client: CodexID, tool: codexApplyPatch, context: lessoncheck.ContextRoot}: postEditProof(
		codexEvidenceVersion, codexApplyPatch, lessoncheck.ContextRoot),
	{client: CodexID, tool: codexApplyPatch, context: lessoncheck.ContextChild}: postEditProof(
		codexEvidenceVersion, codexApplyPatch, lessoncheck.ContextChild),
}

// postEditProof returns the proof of one post-edit path, recorded on
// the given client version. The surface is the surface of the pending
// evidence of the path, so a review changes the level and the version
// of a path, not the surface that it names.
func postEditProof(version, tool string, context lessoncheck.PathContext) recordedEvidence {
	return recordedEvidence{clientVersion: version, surface: postEditSurface(tool, context)}
}

// postEditSurface names the native surface of one post-edit path: the
// PostToolUse event of the tool, its additionalContext reply, and the
// context.
func postEditSurface(tool string, context lessoncheck.PathContext) string {
	return "PostToolUse " + tool + " additionalContext in the " + string(context) + " context"
}

// postEditTimeoutOutcome is what a client does when its post-edit hook
// runs past the hook timeout. The edit already happened, so the hook
// can lose its reply but cannot stop the edit.
type postEditTimeoutOutcome int

const (
	// postEditTimeoutWaits: the client lets the hook run to the end, and
	// the reply reaches the model.
	postEditTimeoutWaits postEditTimeoutOutcome = iota + 1
	// postEditTimeoutDrops: the client stops the hook at the timeout and
	// drops its reply. The edit stays.
	postEditTimeoutDrops
)

// postEditTimeout is the reviewed post-edit timeout outcome of one
// client version, from the post-timeout case of its post-edit
// recording. seconds is the hook timeout that the recording ran with.
type postEditTimeout struct {
	outcome postEditTimeoutOutcome
	seconds int
}

// postEditTimeoutRecorded lists the post-edit timeout outcome per client
// version. It is a fact of the client hook runner, separate from the
// proof of each path. It does not verify a path. It only gives the limit
// text of a verified path. The key version uses the words of
// recordedEvidence, so a path entry finds the fact of its own version.
// TestPostEditEvidenceMatchesRecordings checks each entry against the
// recording.
//
// In the post-timeout case of each recording, the client stops the
// post-edit hook after 15 s, before the verify command, and the model
// gets no findings and no notice. The edit stays. Claude Code shows the
// stop with its own timeout message. Codex shows it only by the 15 s gap
// before the call output and the missing reply. The case runs on
// PostToolUse, so no entry makes a claim about PostToolUseFailure.
var postEditTimeoutRecorded = map[clientVersion]postEditTimeout{
	{client: ClaudeID, version: claudePostEditEvidenceVersion}: {outcome: postEditTimeoutDrops, seconds: 15},
	{client: CodexID, version: codexEvidenceVersion}:           {outcome: postEditTimeoutDrops, seconds: 15},
}

// postEditTimeoutLimit returns the limit text of a verified post-edit
// path of the client version: what happens to the findings when the
// hook runs past its timeout. A client that waits gives "", because the
// reply then arrives in every case.
func postEditTimeoutLimit(clientID, version string) string {
	d, ok := postEditTimeoutRecorded[clientVersion{client: clientID, version: version}]
	if !ok || d.outcome != postEditTimeoutDrops {
		return ""
	}

	return fmt.Sprintf("the client drops the post-edit findings after its %d s hook timeout; the edit stays", d.seconds)
}

// PostEditEvidence returns the native evidence that the post-edit reply
// reaches the model, for one client, tool, and context path. The
// capability report shows it per post-edit path. It is the post-edit
// analog of GuardEvidence.
//
// The evidence is static, because inspection runs no client binary. A
// path in postEditRecorded is verified, with the client version and the
// surface of its reviewed recording. When that client version drops
// the findings after the hook timeout, the evidence carries that limit,
// so every surface that shows the path active also names the limit.
// Every other post-edit path is pending. The pending evidence names no
// client version, because the recording sets the version it ran
// against.
//
// The post-edit hooks share the matcher of the pre-write hook, so the
// tools of guardTools have a post-edit path. A tool or a context
// without a post-edit path gives the zero value, which means unknown.
func PostEditEvidence(clientID, tool string, context lessoncheck.PathContext) VerificationEvidence {
	if !slices.Contains(guardTools[clientID], tool) || !slices.Contains(lessonContexts, context) {
		return VerificationEvidence{}
	}

	if r, ok := postEditRecorded[postEditPath{client: clientID, tool: tool, context: context}]; ok {
		e := mustVerifiedEvidence(r.clientVersion, r.surface)
		e.Limit = postEditTimeoutLimit(clientID, r.clientVersion)

		return e
	}

	return VerificationEvidence{Level: VerificationPending, Surface: postEditSurface(tool, context)}
}
