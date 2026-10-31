package integration

import (
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

// guardRecorded lists the pre-write paths that a reviewed native guard
// recording verifies. A reviewer adds an entry only after a review of
// the recording in testdata/<client>/guard. The recording must show a
// deny of the tool in the context and the slow-guard case. One verified
// level removes both reasons native-deny-unverified and
// native-timeout-unverified, so it needs proof of both.
// TestGuardEvidenceMatchesRecordings checks each entry against the
// recording. The map is empty, because no reviewed recording exists yet.
var guardRecorded = map[guardPath]recordedEvidence{}

// GuardEvidence returns the native evidence that a deny of the guard
// stops the tool, for one client, tool, and context path. The guard
// copies its level into the native_blocking field of its decision, and
// the capability report shows it per pre-write path.
//
// The evidence is static, because inspection runs no client binary. A
// path in guardRecorded is verified, with the client version and the
// surface of its reviewed recording. Every other pre-write path is
// pending. The pending evidence names no client version, because the
// recording sets the version it ran against.
//
// A tool or a context without a pre-write path gives the zero value,
// which means unknown. Both built-in clients name a subagent in their
// edit events, so every context of lessonContexts has a path of its own.
func GuardEvidence(clientID, tool string, context lessoncheck.PathContext) VerificationEvidence {
	if !slices.Contains(guardTools[clientID], tool) || !slices.Contains(lessonContexts, context) {
		return VerificationEvidence{}
	}

	if r, ok := guardRecorded[guardPath{client: clientID, tool: tool, context: context}]; ok {
		return mustVerifiedEvidence(r.clientVersion, r.surface)
	}

	return VerificationEvidence{
		Level:   VerificationPending,
		Surface: "PreToolUse " + tool + " exit 2 in the " + string(context) + " context",
	}
}
