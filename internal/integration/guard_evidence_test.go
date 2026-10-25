package integration

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// sameClientVersion reports whether evidence names the client version of
// a recording manifest. The evidence may put the client name before the
// version, as claudeRecordedVersion does.
func sameClientVersion(evidence, recorded string) bool {
	return recorded != "" && (evidence == recorded || strings.HasSuffix(evidence, " "+recorded))
}

// deniedPaths returns the pre-write paths that the recording shows
// blocked: a case with a payload of a guard tool, a deny record of that
// tool that wrote its reply, no post-edit record of the same call, and
// fixture directories that the run left unchanged. The context is child
// when the payload names an agent.
func (r guardRecording) deniedPaths(t *testing.T, client string) map[guardPath]bool {
	t.Helper()

	out := map[guardPath]bool{}

	for name, c := range r.cases {
		if !c.unchanged() || r.manifest.Client != client || c.Run != r.runs[name] {
			continue
		}

		for _, e := range r.events[c.Run] {
			tool := e.tool()
			if e.hook.Event != "PreToolUse" || !slices.Contains(guardTools[client], tool) {
				continue
			}

			deny, ok := r.guardRecordOf(name, e)
			if !ok || deny.Guard.Decision != lessoncheck.GuardDeny || !deny.Guard.ReplyWritten ||
				deny.Guard.Protocol != lessoncheck.GuardProtocolExit2 {
				continue
			}

			if _, ran := r.postEditOf(t, name, deny); ran {
				continue
			}

			context := lessoncheck.ContextRoot
			if nativeString(e.raw, "agent_id") != "" {
				context = lessoncheck.ContextChild
			}

			out[guardPath{client: client, tool: tool, context: context}] = true
		}
	}

	return out
}

// slowRecorded requires an observed protected slow call for the requested
// path. A write, an unjoined denial, or a timeout without proof stays unknown.
// With no path, it reports whether any path has that proof.
func (r guardRecording) slowRecorded(paths ...guardPath) bool {
	c, ok := r.cases[guardSlowGuard]
	if !ok || c.Phase != "slow" || !c.unchanged() || !slices.Contains(c.PreWrite, "slow 2") {
		return false
	}
	// The slow wrapper's completion line names no call. Require one
	// covered attempt so the line cannot describe a different denial.
	var attempts []timingEvent
	for _, e := range r.events[c.Run] {
		if e.hook.Event == "PreToolUse" && slices.Contains(guardTools[r.manifest.Client], e.tool()) {
			attempts = append(attempts, e)
		}
	}
	if len(attempts) != 1 {
		return false
	}
	e := attempts[0]
	deny, ok := r.guardRecordOf(guardSlowGuard, e)
	if !ok || deny.Guard.Decision != lessoncheck.GuardDeny || !deny.Guard.ReplyWritten ||
		deny.Guard.Protocol != lessoncheck.GuardProtocolExit2 {
		return false
	}
	for _, post := range ofKind(r.audit[c.Audit[0]:c.Audit[1]], lessoncheck.AuditPostEdit) {
		if post.MatchSHA == deny.MatchSHA {
			return false
		}
	}
	// A native successful post event also defeats proof if its audit
	// record was lost. Failed or permission-denied events can follow denial.
	for _, observed := range r.events[c.Run] {
		if observed.hook.Event == "PostToolUse" && observed.tool() == e.tool() {
			return false
		}
	}
	context := lessoncheck.ContextRoot
	if nativeString(e.raw, "agent_id") != "" {
		context = lessoncheck.ContextChild
	}
	p := guardPath{client: r.manifest.Client, tool: e.tool(), context: context}
	return len(paths) == 0 || len(paths) == 1 && paths[0] == p
}

// TestGuardEvidenceMatchesRecordings proves that GuardEvidence reads
// verified only for a pre-write path that a reviewed gate-5 recording
// covers (plan step P4.11, contract K10). Verified evidence needs the
// recording in testdata/<client>/guard, its client version, a surface
// that names the tool and the context, a blocked deny of the tool in
// that context, and the slow-guard case. One level lifts both
// native-deny-unverified and native-timeout-unverified, so it needs
// both cases. Without a recording, every pre-write path stays pending.
// The test reads the recordings in every run and never skips, so a skip
// of the replay never hides a claim.
func TestGuardEvidenceMatchesRecordings(t *testing.T) {
	for _, client := range slices.Sorted(maps.Keys(guardTools)) {
		rec, recorded := readGuardRecording(t, client)

		var denied map[guardPath]bool

		if recorded {
			denied = rec.deniedPaths(t, client)
		}

		for _, tool := range guardTools[client] {
			for _, context := range lessonContexts {
				label := client + " " + tool + " " + string(context)
				evidence := GuardEvidence(client, tool, context)
				require.NoError(t, evidence.Validate(), label)

				if evidence.Level != VerificationVerified {
					assert.Equal(t, VerificationPending, evidence.Level, label)
					assert.Contains(t, evidence.Surface, tool, label)
					assert.Contains(t, evidence.Surface, string(context), label)

					continue
				}

				require.True(t, recorded, "%s: verified evidence needs a reviewed gate-5 recording in testdata/%s/guard",
					label, client)
				assert.True(t, sameClientVersion(evidence.ClientVersion, rec.manifest.ClientVersion),
					"%s: the evidence names %q, the recording ran on %q", label, evidence.ClientVersion, rec.manifest.ClientVersion)
				assert.Contains(t, evidence.Surface, tool, label)
				assert.Contains(t, evidence.Surface, string(context), label)
				assert.True(t, denied[guardPath{client: client, tool: tool, context: context}],
					"%s: the recording shows no deny of this tool in this context that left the files unchanged", label)
				assert.True(t, rec.slowRecorded(guardPath{client: client, tool: tool, context: context}),
					"%s: the recording shows no protected slow call on this path, so native-timeout-unverified stays", label)
			}
		}
	}

	t.Run("each recorded entry names a pre-write path", func(t *testing.T) {
		for p := range guardRecorded {
			assert.Equal(t, VerificationVerified, GuardEvidence(p.client, p.tool, p.context).Level,
				"%s %s %s: the entry names no pre-write path, so it never reaches the report", p.client, p.tool, p.context)
		}
	})

	t.Run("post-edit evidence names no client, so it stays below verified", func(t *testing.T) {
		// postEditEvidence takes no client, tool, or context. A verified
		// level would then claim every client version at once. A reviewed
		// gate-5 recording verifies one client, so the function must first
		// take the client (contracts §5 row 38).
		assert.NotEqual(t, VerificationVerified, postEditEvidence().Level)
		require.NoError(t, postEditEvidence().Validate())
	})
}
