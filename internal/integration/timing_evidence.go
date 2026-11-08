package integration

import (
	"slices"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// payloadIdentities lists, per client and context path, the identity
// and timing items that the hook payloads of the client can give. An
// item that a payload cannot give is unsupported, so the delivery report
// never attributes it.
//
//   - match-identity needs the tool-use ID of the call (tool_use_id).
//   - lane-identity needs the receiving lane (session_id, agent_id).
//   - turn-boundary and model-boundary need the turn of the event
//     (Claude prompt_id, Codex turn_id). A payload without a turn gives
//     neither.
//
// Claude Code sends all three on the tool events of the session and of
// a subagent (native recording, Claude Code 2.1.300; payloads in
// testdata/claude/native). The Codex child lane is unsupported for the
// call and the lane, because no native run verifies a nested subagent or
// the child identity of a Codex hook (see ReceivingContextOf).
// context-reset is not here, because the reset evidence of the
// inspection gives it.
var payloadIdentities = map[string]map[lessoncheck.PathContext][]lessoncheck.AttributionItem{
	ClaudeID: {
		lessoncheck.ContextRoot: {
			lessoncheck.ItemMatchIdentity, lessoncheck.ItemLaneIdentity, lessoncheck.ItemTurnBoundary, lessoncheck.ItemModelBoundary,
		},
		lessoncheck.ContextChild: {
			lessoncheck.ItemMatchIdentity, lessoncheck.ItemLaneIdentity, lessoncheck.ItemTurnBoundary, lessoncheck.ItemModelBoundary,
		},
	},
	CodexID: {
		lessoncheck.ContextRoot: {
			lessoncheck.ItemMatchIdentity, lessoncheck.ItemLaneIdentity, lessoncheck.ItemTurnBoundary, lessoncheck.ItemModelBoundary,
		},
		lessoncheck.ContextChild: {lessoncheck.ItemTurnBoundary, lessoncheck.ItemModelBoundary},
	},
}

// timingPath is the key of one attribution: client, item, and context.
type timingPath struct {
	client  string
	item    lessoncheck.AttributionItem
	context lessoncheck.PathContext
}

// timingRecorded lists the attributions that a reviewed native timing
// recording verifies. A reviewer adds an entry only after a review of
// the recording in testdata/<client>/timing.
// TestTimingEvidenceMatchesRecordings checks each entry against the
// cases of the recording.
//
// The Claude Code recording verifies the call and the lane in the root
// and the child context, and the turn and the model step in the root
// context. Its only child case is a subagent read, which shows no turn
// or model step of the child, so those two child items stay pending.
// Codex has no entry, because no reviewed Codex recording exists yet.
var timingRecorded = map[timingPath]recordedEvidence{
	{client: ClaudeID, item: lessoncheck.ItemMatchIdentity, context: lessoncheck.ContextRoot}: claudeTimingProof(
		lessoncheck.ItemMatchIdentity, lessoncheck.ContextRoot),
	{client: ClaudeID, item: lessoncheck.ItemMatchIdentity, context: lessoncheck.ContextChild}: claudeTimingProof(
		lessoncheck.ItemMatchIdentity, lessoncheck.ContextChild),
	{client: ClaudeID, item: lessoncheck.ItemLaneIdentity, context: lessoncheck.ContextRoot}: claudeTimingProof(
		lessoncheck.ItemLaneIdentity, lessoncheck.ContextRoot),
	{client: ClaudeID, item: lessoncheck.ItemLaneIdentity, context: lessoncheck.ContextChild}: claudeTimingProof(
		lessoncheck.ItemLaneIdentity, lessoncheck.ContextChild),
	{client: ClaudeID, item: lessoncheck.ItemTurnBoundary, context: lessoncheck.ContextRoot}: claudeTimingProof(
		lessoncheck.ItemTurnBoundary, lessoncheck.ContextRoot),
	{client: ClaudeID, item: lessoncheck.ItemModelBoundary, context: lessoncheck.ContextRoot}: claudeTimingProof(
		lessoncheck.ItemModelBoundary, lessoncheck.ContextRoot),
}

// claudeTimingProof returns the proof of one Claude Code attribution.
// The surface is the surface of the pending evidence of the item, so a
// review changes the level and the version of an item, not the surface
// that it names.
func claudeTimingProof(item lessoncheck.AttributionItem, context lessoncheck.PathContext) recordedEvidence {
	return recordedEvidence{clientVersion: claudeEvidenceVersion, surface: timingSurface(item, context)}
}

// timingSurface names the native surface of one attribution: the timing
// recording, the item, and the context.
func timingSurface(item lessoncheck.AttributionItem, context lessoncheck.PathContext) string {
	return "native timing recording, " + string(item) + " in the " + string(context) + " context"
}

// TimingEvidence returns the native evidence of one identity or timing
// item for one client and context path. It is the timing analog of
// GuardEvidence.
//
// The evidence is static, because inspection runs no client binary. An
// item in timingRecorded is verified, with the client version and the
// surface of its reviewed recording. Every other item that the payloads
// can give is pending. The pending evidence names no client version,
// because the recording sets the version it ran against.
//
// An item that the payloads cannot give, context-reset, and an unknown
// client give the zero value, which means unknown.
func TimingEvidence(clientID string, item lessoncheck.AttributionItem, context lessoncheck.PathContext) VerificationEvidence {
	if !slices.Contains(payloadIdentities[clientID][context], item) {
		return VerificationEvidence{}
	}

	if r, ok := timingRecorded[timingPath{client: clientID, item: item, context: context}]; ok {
		return mustVerifiedEvidence(r.clientVersion, r.surface)
	}

	return VerificationEvidence{Level: VerificationPending, Surface: timingSurface(item, context)}
}

// attributionsOf returns the attributions of one client: one per item
// and context path, items first. context-reset copies the evidence of
// the resets capability, and its state is unsupported for a context
// that no verified reset reaches. The other items read TimingEvidence,
// and their state is unsupported when the payloads cannot give the
// item. Else the state is verified only with verified evidence. A
// client without an entry in payloadIdentities gives nil.
func attributionsOf(insp Inspection) []lessoncheck.CapAttribution {
	contexts, ok := payloadIdentities[insp.ClientID]
	if !ok {
		return nil
	}

	resets, _ := insp.Entry(CapabilityResets)

	var out []lessoncheck.CapAttribution

	for _, item := range lessoncheck.AttributionItems {
		for _, context := range lessonContexts {
			a := lessoncheck.CapAttribution{Item: item, Context: context}

			supported := slices.Contains(contexts[context], item)
			evidence := TimingEvidence(insp.ClientID, item, context)

			if item == lessoncheck.ItemContextReset {
				supported, evidence = resetReaches(insp.ClientID, context), resets.Verification
			}

			a.Verification = capVerification(evidence)

			switch {
			case !supported:
				a.State = lessoncheck.AttrUnsupported
			case evidence.Level == VerificationVerified:
				a.State = lessoncheck.AttrVerified
			default:
				a.State = lessoncheck.AttrUnverified
			}

			out = append(out, a)
		}
	}

	return out
}

// resetReaches reports whether a reset event of the client reaches the
// context path, by the one receiver rule (laneResettable). A child
// context and every Codex context are not resettable. A path context
// uses the lane words, so the context converts to a lane.
func resetReaches(clientID string, context lessoncheck.PathContext) bool {
	return laneResettable(clientID, string(context))
}
