package integration

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// sameClientVersion reports whether evidence names the client version of
// a recording manifest. The evidence may put the client name before the
// version, as claudeEvidenceVersion does.
func sameClientVersion(evidence, recorded string) bool {
	return recorded != "" && (evidence == recorded || strings.HasSuffix(evidence, " "+recorded))
}

// blockedDeny reports whether a guard record shows a block: a deny that
// wrote its reply with the exit protocol. The deny check and the slow
// check share it, so both accept the same proof.
func blockedDeny(rec lessoncheck.AuditRecord) bool {
	return rec.Guard.Decision == lessoncheck.GuardDeny && rec.Guard.ReplyWritten &&
		rec.Guard.Protocol == lessoncheck.GuardProtocolExit2
}

// eventContext returns the context path of a recorded payload: child
// when the payload names an agent, root otherwise.
func eventContext(e timingEvent) lessoncheck.PathContext {
	if nativeString(e.raw, "agent_id") != "" {
		return lessoncheck.ContextChild
	}

	return lessoncheck.ContextRoot
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
			if !ok || !blockedDeny(deny) {
				continue
			}

			if _, ran := r.postEditOf(t, name, deny); ran {
				continue
			}

			out[guardPath{client: client, tool: tool, context: eventContext(e)}] = true
		}
	}

	return out
}

// slowRecorded reports whether a slow case of the given kind shows a
// protected slow call on the given path. slowPastTimeout reads the
// slow-guard case, and slowInBudget reads each in-budget slow case. A
// write, a deny without a joined record, or a stopped hook gives false,
// so the evidence stays unknown. With no path, it reports whether any
// path has that proof. Two or more paths give false, because one case
// proves one path.
func (r guardRecording) slowRecorded(kind slowProof, paths ...guardPath) bool {
	if len(paths) > 1 {
		return false
	}

	for name, c := range r.cases {
		if !slowCaseOf(kind, name, c) {
			continue
		}

		if p, ok := r.protectedSlowPath(name); ok && (len(paths) == 0 || paths[0] == p) {
			return true
		}
	}

	return false
}

// slowCaseOf reports whether a case is a slow case of the kind and shows
// its outcome. The slow-guard case must log the deny of the guard after
// its wait past the timeout. An in-budget case must log exactly one deny
// and leave the fixture unchanged (slowDenyObserved).
func slowCaseOf(kind slowProof, name string, c guardCase) bool {
	switch kind {
	case slowPastTimeout:
		return name == guardSlowGuard && c.Phase == guardSlowPhase && c.unchanged() && slices.Contains(c.PreWrite, "slow 2")
	case slowInBudget:
		return c.Phase == guardPhaseSlowDeny && slowDenyObserved(c)
	default:
		return false
	}
}

// protectedSlowPath returns the path of the one protected call of a slow
// case: a blocked deny that joins its payload, no post-edit record of the
// call, and no native PostToolUse event of the tool.
func (r guardRecording) protectedSlowPath(name string) (guardPath, bool) {
	c := r.cases[name]

	// The completion line of the slow wrapper names no call. So the case
	// must hold exactly one covered attempt, or the line can describe
	// another deny.
	var attempts []timingEvent

	for _, e := range r.events[c.Run] {
		if e.hook.Event == "PreToolUse" && slices.Contains(guardTools[r.manifest.Client], e.tool()) {
			attempts = append(attempts, e)
		}
	}

	if len(attempts) != 1 {
		return guardPath{}, false
	}

	e := attempts[0]

	deny, ok := r.guardRecordOf(name, e)
	if !ok || !blockedDeny(deny) {
		return guardPath{}, false
	}

	if c.Audit[0] < 0 || c.Audit[1] > len(r.audit) || c.Audit[0] > c.Audit[1] {
		return guardPath{}, false
	}

	for _, post := range ofKind(r.audit[c.Audit[0]:c.Audit[1]], lessoncheck.AuditPostEdit) {
		if post.MatchSHA == deny.MatchSHA {
			return guardPath{}, false
		}
	}

	// A native PostToolUse event of the tool shows that the tool ran,
	// also when its audit record is lost, so it cancels the proof. A
	// failure or permission-denied event can follow a deny, so it does not.
	for _, observed := range r.events[c.Run] {
		if observed.hook.Event == "PostToolUse" && observed.tool() == e.tool() {
			return guardPath{}, false
		}
	}

	return guardPath{client: r.manifest.Client, tool: e.tool(), context: eventContext(e)}, true
}

// timeoutOutcomeOf reads the timeout outcome of the client from the
// slow-guard case. The guard waits past the hook timeout there.
//
//   - waits: the case shows a protected slow call (slowRecorded).
//   - fail-open: the guard logged nothing, one covered attempt ran, the
//     fixture changed, and a native PostToolUse event of the tool shows
//     that the tool ran.
//   - fail-closed: the guard logged nothing, one covered attempt ran,
//     the fixture stayed unchanged, and no PostToolUse event of the tool
//     exists.
//
// Any other case gives false, so the outcome stays unknown.
func (r guardRecording) timeoutOutcomeOf() (timeoutOutcome, bool) {
	if r.slowRecorded(slowPastTimeout) {
		return timeoutWaits, true
	}

	c, ok := r.cases[guardSlowGuard]
	if !ok || c.Phase != guardSlowPhase || len(c.PreWrite) > 0 {
		return 0, false
	}

	var attempts []timingEvent

	for _, e := range r.events[c.Run] {
		if e.hook.Event == "PreToolUse" && slices.Contains(guardTools[r.manifest.Client], e.tool()) {
			attempts = append(attempts, e)
		}
	}

	if len(attempts) != 1 {
		return 0, false
	}

	ran := slices.ContainsFunc(r.events[c.Run], func(e timingEvent) bool {
		return e.hook.Event == "PostToolUse" && e.tool() == attempts[0].tool()
	})

	switch {
	case ran && !c.unchanged():
		return timeoutFailOpen, true
	case !ran && c.unchanged():
		return timeoutFailClosed, true
	default:
		return 0, false
	}
}

// preWriteTimeout returns the timeout in seconds of the pre-write hook
// that setup installs for the client.
func preWriteTimeout(t *testing.T, client string) int {
	t.Helper()

	specs := map[string]func(string) []hooks.Spec{
		ClaudeID: hooks.ClaudeLessonCheckSpecs,
		CodexID:  hooks.CodexLessonCheckSpecs,
	}[client]
	require.NotNil(t, specs, "client %s has lesson-check hook specs", client)

	return specs(hooks.LessonChecksEnforce)[0].Timeout
}

// withSyntheticFailOpen replaces the recorded maps for one test. Each
// given path reads verified by an in-budget slow proof, and the client
// version fails open at the 10 s hook timeout. The entries test the
// narration only and give no native proof.
func withSyntheticFailOpen(t *testing.T, paths ...guardPath) {
	t.Helper()

	savedPaths, savedTimeouts := guardRecorded, timeoutRecorded
	t.Cleanup(func() { guardRecorded, timeoutRecorded = savedPaths, savedTimeouts })

	guardRecorded = map[guardPath]guardProof{}
	timeoutRecorded = map[clientVersion]timeoutDisposition{}

	for _, p := range paths {
		guardRecorded[p] = guardProof{
			recordedEvidence: recordedEvidence{clientVersion: "synthetic-fixture", surface: "fixture " + p.tool},
			slow:             slowInBudget,
		}
		timeoutRecorded[clientVersion{client: p.client, version: "synthetic-fixture"}] = timeoutDisposition{
			outcome: timeoutFailOpen, seconds: 10,
		}
	}
}

// TestGuardEvidenceMatchesRecordings proves that GuardEvidence reads
// verified only for a pre-write path that a reviewed native guard
// recording covers. Verified evidence needs the recording in
// testdata/<client>/guard, its client version, a surface that names the
// tool and the context, a blocked deny of the tool in that context, and
// a slow proof of the same path: the slow-guard case, or an in-budget
// slow case. One verified level removes both reasons
// native-deny-unverified and native-timeout-unverified, so it needs both
// cases. A verified path of a client that lets the write run after its
// timeout must name that limit. Without a recording, every pre-write
// path stays pending. The test reads the recordings in every run and
// never skips, so a skip of the replay never hides a claim.
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

				require.True(t, recorded, "%s: verified evidence needs a reviewed native guard recording in testdata/%s/guard",
					label, client)
				assert.True(t, sameClientVersion(evidence.ClientVersion, rec.manifest.ClientVersion),
					"%s: the evidence names %q, the recording ran on %q", label, evidence.ClientVersion, rec.manifest.ClientVersion)
				assert.Contains(t, evidence.Surface, tool, label)
				assert.Contains(t, evidence.Surface, string(context), label)
				assert.True(t, denied[guardPath{client: client, tool: tool, context: context}],
					"%s: the recording shows no deny of this tool in this context that left the files unchanged", label)
				proof := guardRecorded[guardPath{client: client, tool: tool, context: context}]
				require.Contains(t, []slowProof{slowPastTimeout, slowInBudget}, proof.slow,
					"%s: the entry names the kind of its slow proof", label)
				assert.True(t, rec.slowRecorded(proof.slow, guardPath{client: client, tool: tool, context: context}),
					"%s: the recording shows no protected slow call of this kind on this path, so "+
						"native-timeout-unverified stays", label)

				// An in-budget proof leaves open what the client does past the
				// timeout. The reviewed outcome of the version gives the limit,
				// so the limit cannot go missing for lack of a fact.
				if proof.slow == slowInBudget {
					assert.Contains(t, timeoutRecorded, clientVersion{client: client, version: evidence.ClientVersion},
						"%s: an in-budget proof needs the timeout outcome of its client version", label)
				}

				// A client that lets the write run after its timeout can only
				// give an in-budget proof. Its verified path carries the limit,
				// so each surface that shows the claim also shows the gap.
				outcome := timeoutRecorded[clientVersion{client: client, version: evidence.ClientVersion}]
				if outcome.outcome == timeoutFailOpen {
					assert.Equal(t, slowInBudget, proof.slow,
						"%s: a client that fails open at its timeout gives an in-budget slow proof only", label)
					assert.Contains(t, evidence.Limit, fmt.Sprintf("after its %d s hook timeout", outcome.seconds),
						"%s: a verified path of a fail-open client names its limit", label)
				}
			}
		}
	}

	t.Run("each timeout outcome matches the slow-guard case of its recording", func(t *testing.T) {
		for key, d := range timeoutRecorded {
			rec, recorded := readGuardRecording(t, key.client)
			require.True(t, recorded, "%s %s: a timeout outcome needs a reviewed native guard recording in testdata/%s/guard",
				key.client, key.version, key.client)
			assert.True(t, sameClientVersion(key.version, rec.manifest.ClientVersion),
				"%s: the outcome names %q, the recording ran on %q", key.client, key.version, rec.manifest.ClientVersion)

			got, ok := rec.timeoutOutcomeOf()
			require.True(t, ok, "%s %s: the slow-guard case shows no clear timeout outcome", key.client, key.version)
			assert.Equal(t, d.outcome, got, "%s %s", key.client, key.version)
			assert.Equal(t, preWriteTimeout(t, key.client), d.seconds,
				"%s %s: the limit names the timeout of the installed pre-write hook", key.client, key.version)
		}
	})

	t.Run("each recorded entry names a pre-write path", func(t *testing.T) {
		for p := range guardRecorded {
			assert.Equal(t, VerificationVerified, GuardEvidence(p.client, p.tool, p.context).Level,
				"%s %s %s: the entry names no pre-write path, so it never reaches the report", p.client, p.tool, p.context)
		}
	})
}

// TestGuardToolsMatchTheInstalledMatchers ties the tools that the guard
// decoder reads to the matcher of the installed pre-write hook, in both
// modes. The guard and the baseline handler both use that decoder. The
// capability report asks GuardEvidence about each matcher tool. So a
// tool in only one list fires a hook that the decoder refuses, or it
// reads unknown in the report, and no other test fails.
func TestGuardToolsMatchTheInstalledMatchers(t *testing.T) {
	for client, specs := range map[string]func(string) []hooks.Spec{
		ClaudeID: hooks.ClaudeLessonCheckSpecs,
		CodexID:  hooks.CodexLessonCheckSpecs,
	} {
		for _, mode := range []string{hooks.LessonChecksEnforce, hooks.LessonChecksCheck} {
			assert.Equal(t, specs(mode)[0].Tools(), guardTools[client],
				"%s %s: the pre-write hook fires on exactly the tools that the guard decoder reads", client, mode)
		}
	}
}
