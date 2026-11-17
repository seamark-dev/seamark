package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// postEditReplyHeader is the first words of the post-edit reply after a
// call of the tool, as the findings text of lessoncheck/postedit.go
// writes them.
func postEditReplyHeader(tool string) string {
	return "seamark post-edit lesson checks after this " + tool + " call"
}

// claudePostEditReplies counts the lines of guard-context.jsonl that
// hold the PostToolUse reply of one Claude Code tool call: a
// hook_additional_context attachment of PostToolUse that names the call
// and holds the reply header of the tool. A hook_success record, a reply
// of PostToolUseFailure, and the reply of another call do not count.
func claudePostEditReplies(t *testing.T, lines [][]byte, id, tool string) int {
	t.Helper()

	n := 0

	for _, line := range lines {
		var rec struct {
			Type       string `json:"type"`
			Attachment struct {
				Type      string          `json:"type"`
				HookEvent string          `json:"hookEvent"`
				ToolUseID string          `json:"toolUseID"`
				Content   json.RawMessage `json:"content"`
			} `json:"attachment"`
		}
		require.NoError(t, json.Unmarshal(line, &rec), guardContextFile)

		a := rec.Attachment
		if rec.Type != "attachment" || a.Type != "hook_additional_context" || a.HookEvent != claudeEventPostTool ||
			id == "" || a.ToolUseID != id {
			continue
		}

		// The content of a context attachment is a list of texts. The
		// record of a success holds a single text, so it gives no list.
		var texts []string
		if json.Unmarshal(a.Content, &texts) != nil {
			continue
		}

		if slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, postEditReplyHeader(tool)) }) {
			n++
		}
	}

	return n
}

// postEditReplyRecords returns the number of guard-context.jsonl records
// that hold the post-edit reply of the call of a case. The script keeps
// only the records that the model did not write. A Codex reply is a hook
// developer record of the rollout that names the path of the case
// (postEditContext). A Claude Code reply is an attachment that names the
// tool call of the case (claudePostEditReplies).
func (r guardRecording) postEditReplyRecords(t *testing.T, x postEditCase) (int, error) {
	t.Helper()

	if r.manifest.Client == CodexID {
		roles := r.postEditContext(t, x.tool, x.path)

		return len(slices.DeleteFunc(roles, func(role string) bool { return role != "developer" })), nil
	}

	call, err := r.postEditCallOf(x)
	if err != nil {
		return 0, err
	}

	lines := timingLines(t, filepath.Join(r.dir, guardContextFile))

	return claudePostEditReplies(t, lines, nativeString(call.pre.raw, "tool_use_id"), x.tool), nil
}

// postEditSlowProof reports an error unless the recording shows an
// in-budget slow reply on one post-edit path. The case of the slow-post
// phase whose row names the tool and the context must pass the replay
// rule of a slow reply (postEditSlowReply): the indirection entry ran
// the verify command to the end, and the post-edit record of the call
// wrote its reply. Exactly one record of guard-context.jsonl must hold
// that reply. A missing case gives an error, so a path never takes the
// proof of another path.
func (r guardRecording) postEditSlowProof(t *testing.T, tool string, context lessoncheck.PathContext) error {
	t.Helper()

	for _, x := range postEditCases(r.manifest.Client) {
		if x.outcome != postEditOutcomeSlowReply || x.tool != tool || x.lane != string(context) {
			continue
		}

		if _, err := r.postEditSlowReply(x); err != nil {
			return fmt.Errorf("case %s: %w", x.name, err)
		}

		n, err := r.postEditReplyRecords(t, x)
		if err != nil {
			return fmt.Errorf("case %s: %w", x.name, err)
		}

		if n != 1 {
			return fmt.Errorf("case %s: %d records of %s hold the post-edit reply of the call, not one",
				x.name, n, guardContextFile)
		}

		return nil
	}

	return errors.New("no in-budget slow case of the scenario names this tool and context")
}

// postEditTimeoutOutcomeOf reads the post-edit timeout outcome of the
// client from the post-timeout case. The indirection entry waits past
// the hook timeout there.
//
//   - drops: the case passes the replay rule (postEditTimeout), the
//     indirection entry logged only its start, and no record holds the
//     reply.
//   - waits: the indirection entry logged the exit status 0 of the verify
//     command, and exactly one record holds the reply.
//
// Any other case gives an error, so the outcome stays unknown.
func (r guardRecording) postEditTimeoutOutcomeOf(t *testing.T) (postEditTimeoutOutcome, error) {
	t.Helper()

	i := slices.IndexFunc(postEditCases(r.manifest.Client), func(x postEditCase) bool {
		return x.outcome == postEditOutcomeTimeout
	})
	if i < 0 {
		return 0, errors.New("the scenario of the client has no post-timeout case")
	}

	x := postEditCases(r.manifest.Client)[i]

	if _, err := r.postEditTimeout(x); err != nil {
		return 0, fmt.Errorf("case %s: %w", x.name, err)
	}

	n, err := r.postEditReplyRecords(t, x)
	if err != nil {
		return 0, fmt.Errorf("case %s: %w", x.name, err)
	}

	stopped := slices.Equal(r.cases[x.name].PostEdit, []string{postEditTimeoutStart})

	switch {
	case stopped && n == 0:
		return postEditTimeoutDrops, nil
	case !stopped && n == 1:
		return postEditTimeoutWaits, nil
	default:
		return 0, fmt.Errorf("case %s: the hook stopped: %t, but %d records hold the reply", x.name, stopped, n)
	}
}

// postEditTimeoutNote reports whether the Claude Code debug log of the
// post-timeout case names the stop of the indirection entry after the
// given number of seconds. The line of a stopped hook has a note and no
// status. A Codex recording has no debug log, so it gives true.
func (r guardRecording) postEditTimeoutNote(seconds int) bool {
	if r.manifest.Client != ClaudeID {
		return true
	}

	run := 0

	for _, x := range postEditCases(ClaudeID) {
		if x.outcome == postEditOutcomeTimeout {
			run = r.runs[x.name]
		}
	}

	stop := fmt.Sprintf("timed out after %d000ms", seconds)

	return slices.ContainsFunc(r.hooks, func(h guardHookResult) bool {
		return run > 0 && h.Run == run && h.Hook == postEditHook && h.Status == nil && strings.Contains(h.Note, stop)
	})
}

// postEditHookTimeout returns the timeout in seconds of each post-edit
// hook that setup installs for the client. All post-edit hooks of a
// client share one timeout, so a second value fails the test.
func postEditHookTimeout(t *testing.T, client string) int {
	t.Helper()

	specs := map[string]func(string) []hooks.Spec{
		ClaudeID: hooks.ClaudeLessonCheckSpecs,
		CodexID:  hooks.CodexLessonCheckSpecs,
	}[client]
	require.NotNil(t, specs, "client %s has lesson-check hook specs", client)

	posts := specs(hooks.LessonChecksCheck)[1:]
	require.NotEmpty(t, posts, "client %s has a post-edit hook", client)

	for _, spec := range posts {
		require.Equal(t, posts[0].Timeout, spec.Timeout, "%s: the post-edit hooks share one timeout", client)
	}

	return posts[0].Timeout
}

// TestPostEditEvidenceMatchesRecordings proves that PostEditEvidence
// reads verified only for a post-edit path that a reviewed native
// post-edit recording covers. Verified evidence needs the recording in
// testdata/<client>/post-edit, its client version, a surface that names
// PostToolUse, the tool, and the context, and an in-budget slow reply of
// the same path. The reviewed post-edit timeout outcome of the client
// version must exist, and a client that drops the findings at its
// timeout must give the limit. Without a recording, every post-edit path
// stays pending. The test reads the recordings in every run and never
// skips, so a skip of the replay never hides a claim.
func TestPostEditEvidenceMatchesRecordings(t *testing.T) {
	for _, client := range slices.Sorted(maps.Keys(guardTools)) {
		rec, recorded := readCaseRecording(t, client, postEditScenario)

		for _, tool := range guardTools[client] {
			for _, context := range lessonContexts {
				label := client + " " + tool + " " + string(context)
				evidence := PostEditEvidence(client, tool, context)
				require.NoError(t, evidence.Validate(), label)

				if evidence.Level != VerificationVerified {
					assert.Equal(t, VerificationPending, evidence.Level, label)
					assert.Contains(t, evidence.Surface, tool, label)
					assert.Contains(t, evidence.Surface, string(context), label)

					continue
				}

				require.True(t, recorded,
					"%s: verified evidence needs a reviewed native post-edit recording in testdata/%s/post-edit",
					label, client)
				assert.True(t, sameClientVersion(evidence.ClientVersion, rec.manifest.ClientVersion),
					"%s: the evidence names %q, the recording ran on %q", label, evidence.ClientVersion, rec.manifest.ClientVersion)
				assert.Contains(t, evidence.Surface, "PostToolUse "+tool, label)
				assert.Contains(t, evidence.Surface, string(context), label)
				assert.NoError(t, rec.postEditSlowProof(t, tool, context),
					"%s: the recording shows no in-budget slow reply on this path", label)

				// An in-budget proof leaves open what the client does past
				// the timeout. The reviewed outcome of the version gives the
				// limit, so the limit cannot go missing for lack of a fact.
				outcome, ok := postEditTimeoutRecorded[clientVersion{client: client, version: evidence.ClientVersion}]
				require.True(t, ok, "%s: an in-budget proof needs the post-edit timeout outcome of its client version", label)

				if outcome.outcome == postEditTimeoutDrops {
					assert.Equal(t, fmt.Sprintf("the client drops the post-edit findings after its %d s hook timeout; "+
						"the edit stays", outcome.seconds), evidence.Limit,
						"%s: a verified path of a client that drops the findings names its limit", label)
				} else {
					assert.Empty(t, evidence.Limit, label)
				}
			}
		}
	}

	t.Run("each post-edit timeout outcome matches the post-timeout case of its recording", func(t *testing.T) {
		for key, d := range postEditTimeoutRecorded {
			label := key.client + " " + key.version

			rec, recorded := readCaseRecording(t, key.client, postEditScenario)
			require.True(t, recorded,
				"%s: a post-edit timeout outcome needs a reviewed native post-edit recording in testdata/%s/post-edit",
				label, key.client)
			assert.True(t, sameClientVersion(key.version, rec.manifest.ClientVersion),
				"%s: the outcome names %q, the recording ran on %q", key.client, key.version, rec.manifest.ClientVersion)

			got, err := rec.postEditTimeoutOutcomeOf(t)
			require.NoError(t, err, "%s: the post-timeout case shows no clear outcome", label)
			assert.Equal(t, d.outcome, got, label)
			assert.Equal(t, postEditHookTimeout(t, key.client), d.seconds,
				"%s: the limit names the timeout of the installed post-edit hook", label)
			assert.True(t, rec.postEditTimeoutNote(d.seconds),
				"%s: the debug log names no stop of the post-edit hook after %d s", label, d.seconds)
		}
	})

	t.Run("each recorded entry names a post-edit path", func(t *testing.T) {
		for p := range postEditRecorded {
			assert.Equal(t, VerificationVerified, PostEditEvidence(p.client, p.tool, p.context).Level,
				"%s %s %s: the entry names no post-edit path, so it never reaches the report", p.client, p.tool, p.context)
		}
	})
}

// TestPostEditToolsMatchTheInstalledMatchers ties the tools that
// PostEditEvidence accepts to the matcher of each installed post-edit
// hook, in both modes. The capability report asks PostEditEvidence about
// each matcher tool, so a tool outside guardTools would read unknown.
func TestPostEditToolsMatchTheInstalledMatchers(t *testing.T) {
	for client, specs := range map[string]func(string) []hooks.Spec{
		ClaudeID: hooks.ClaudeLessonCheckSpecs,
		CodexID:  hooks.CodexLessonCheckSpecs,
	} {
		for _, mode := range []string{hooks.LessonChecksEnforce, hooks.LessonChecksCheck} {
			for _, spec := range specs(mode)[1:] {
				assert.Equal(t, guardTools[client], spec.Tools(),
					"%s %s %s: the post-edit hook fires on exactly the tools of the pre-write hook", client, mode, spec.Event)
			}
		}
	}
}

// TestPostEditEvidenceReadsEachPath checks the reading rule of
// PostEditEvidence on the shipped registry: a recorded path is verified
// with the limit of its client version, a covered path without an entry
// is pending, and a tool or a context without a post-edit path is
// unknown.
func TestPostEditEvidenceReadsEachPath(t *testing.T) {
	write := PostEditEvidence(ClaudeID, claudeToolWrite, lessoncheck.ContextChild)
	assert.Equal(t, VerificationEvidence{
		Level: VerificationVerified, ClientVersion: claudePostEditEvidenceVersion,
		Surface: "PostToolUse Write additionalContext in the child context",
		Limit:   "the client drops the post-edit findings after its 15 s hook timeout; the edit stays",
	}, write)

	patch := PostEditEvidence(CodexID, codexApplyPatch, lessoncheck.ContextRoot)
	assert.Equal(t, VerificationVerified, patch.Level)
	assert.Equal(t, codexEvidenceVersion, patch.ClientVersion)
	assert.Equal(t, write.Limit, patch.Limit, "both clients drop the findings at the same timeout")

	assert.Equal(t, VerificationEvidence{
		Level: VerificationPending, Surface: "PostToolUse MultiEdit additionalContext in the root context",
	}, PostEditEvidence(ClaudeID, claudeToolMultiEdit, lessoncheck.ContextRoot),
		"MultiEdit has no entry, because Claude Code offers no MultiEdit tool")

	assert.Equal(t, VerificationEvidence{}, PostEditEvidence(ClaudeID, claudeReadTool, lessoncheck.ContextRoot),
		"Read has no post-edit path")
	assert.Equal(t, VerificationEvidence{}, PostEditEvidence(CodexID, claudeToolWrite, lessoncheck.ContextRoot),
		"a Claude Code tool has no Codex post-edit path")
	assert.Equal(t, VerificationEvidence{}, PostEditEvidence(ClaudeID, claudeToolWrite, "nested"),
		"a context outside lessonContexts has no path")
	assert.Equal(t, VerificationEvidence{}, PostEditEvidence("other", claudeToolWrite, lessoncheck.ContextRoot))

	t.Run("a client version that waits for the hook gives no limit", func(t *testing.T) {
		saved := postEditTimeoutRecorded
		t.Cleanup(func() { postEditTimeoutRecorded = saved })

		postEditTimeoutRecorded = map[clientVersion]postEditTimeout{
			{client: ClaudeID, version: claudePostEditEvidenceVersion}: {outcome: postEditTimeoutWaits, seconds: 15},
		}

		e := PostEditEvidence(ClaudeID, claudeToolEdit, lessoncheck.ContextRoot)
		assert.Equal(t, VerificationVerified, e.Level)
		assert.Empty(t, e.Limit)
		assert.Empty(t, PostEditEvidence(CodexID, codexApplyPatch, lessoncheck.ContextRoot).Limit,
			"a client version without an outcome gives no limit")
	})
}

// claudeContextLine returns one synthetic guard-context.jsonl line of
// Claude Code: an attachment of the given type and event for one tool
// call, with the given content.
func claudeContextLine(t *testing.T, kind, event, id string, content any) []byte {
	t.Helper()

	data, err := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{
		"type": kind, "hookEvent": event, "toolUseID": id, "content": content,
	}})
	require.NoError(t, err)

	return data
}

// TestClaudePostEditRepliesCountsTheReplyOfTheCall checks the reader of
// the Claude Code post-edit reply on synthetic records. It gives no
// native proof.
func TestClaudePostEditRepliesCountsTheReplyOfTheCall(t *testing.T) {
	reply := []string{postEditReplyHeader(claudeToolWrite) + ": violation (1 evaluated pin)\n"}
	blob, err := json.Marshal(map[string]any{"type": "api-request-blob", "message": reply[0]})
	require.NoError(t, err)

	lines := [][]byte{
		claudeContextLine(t, "hook_success", claudeEventPostTool, "toolu_a", "Success"),
		claudeContextLine(t, "hook_additional_context", claudeEventPostTool, "toolu_a", reply),
		blob,
		claudeContextLine(t, "hook_additional_context", claudeEventPostTool, "toolu_b", reply),
		claudeContextLine(t, "hook_additional_context", claudeEventToolFailure, "toolu_c", reply),
		claudeContextLine(t, "hook_additional_context", claudeEventPostTool, "toolu_d",
			[]string{postEditReplyHeader(claudeToolEdit) + ": violation\n"}),
	}

	assert.Equal(t, 1, claudePostEditReplies(t, lines, "toolu_a", claudeToolWrite),
		"the success record and the request blob do not count")
	assert.Equal(t, 1, claudePostEditReplies(t, lines, "toolu_b", claudeToolWrite))
	assert.Zero(t, claudePostEditReplies(t, lines, "toolu_c", claudeToolWrite), "a PostToolUseFailure reply does not count")
	assert.Zero(t, claudePostEditReplies(t, lines, "toolu_d", claudeToolWrite), "the header names another tool")
	assert.Zero(t, claudePostEditReplies(t, lines, "toolu_e", claudeToolWrite), "no record names the call")
	assert.Zero(t, claudePostEditReplies(t, lines, "", claudeToolWrite), "a call without an ID matches nothing")
}

// TestPostEditTimeoutOutcomeReadsTheCase checks the reading of the
// post-edit timeout outcome on synthetic recordings of the post-timeout
// case of each client. It gives no native proof.
func TestPostEditTimeoutOutcomeReadsTheCase(t *testing.T) {
	stopped := func(t *testing.T, client string, reply bool) guardRecording {
		t.Helper()

		x := postEditCaseRow(t, client, "post-timeout-"+map[string]string{ClaudeID: "write", CodexID: "add"}[client])
		r := postEditRecording(t, client, x, postEditPayloads(client, x))

		c := r.cases[x.name]
		c.PostEdit, c.Audit = []string{postEditTimeoutStart}, [2]int{0, 1}
		r.cases[x.name] = c
		r.audit = r.audit[:1]

		if client == ClaudeID {
			r.hooks = []guardHookResult{{Run: 1, Hook: postEditHook,
				Note: "[DEBUG] Hooks: PostToolUse:Write [sh post-edit.sh] timed out after 15000ms (15035ms)"}}
		}

		if reply {
			writeSyntheticReply(t, r, client, x)
		}

		return r
	}

	for _, client := range []string{ClaudeID, CodexID} {
		t.Run(client, func(t *testing.T) {
			got, err := stopped(t, client, false).postEditTimeoutOutcomeOf(t)
			require.NoError(t, err)
			assert.Equal(t, postEditTimeoutDrops, got, "the hook stopped, and no record holds the reply")
			assert.True(t, stopped(t, client, false).postEditTimeoutNote(15))

			_, err = stopped(t, client, true).postEditTimeoutOutcomeOf(t)
			require.Error(t, err, "a stopped hook cannot give a reply")

			x := postEditCaseRow(t, client, "post-timeout-"+map[string]string{ClaudeID: "write", CodexID: "add"}[client])
			waited := postEditRecording(t, client, x, postEditPayloads(client, x))
			writeSyntheticReply(t, waited, client, x)

			got, err = waited.postEditTimeoutOutcomeOf(t)
			require.NoError(t, err)
			assert.Equal(t, postEditTimeoutWaits, got, "the hook ran to the end, and one record holds the reply")

			_, err = postEditRecording(t, client, x, postEditPayloads(client, x)).postEditTimeoutOutcomeOf(t)
			require.Error(t, err, "a hook that ran to the end must leave its reply")
		})
	}

	t.Run("the Claude Code note must name the stop after the timeout", func(t *testing.T) {
		r := stopped(t, ClaudeID, false)
		assert.False(t, r.postEditTimeoutNote(10), "the note names 15000 ms, not 10000 ms")

		r.hooks = nil
		assert.False(t, r.postEditTimeoutNote(15), "no note")
	})
}

// writeSyntheticReply writes the guard-context.jsonl of a synthetic
// recording with one record of the post-edit reply of the case: a hook
// developer record that names the path for Codex, an attachment that
// names the tool call for Claude Code.
func writeSyntheticReply(t *testing.T, r guardRecording, client string, x postEditCase) {
	t.Helper()

	text := postEditReplyHeader(x.tool) + ": violation\n- violation: pin a, check c (forbid-path-change/1): add " +
		x.path + "\n"

	var line []byte

	if client == CodexID {
		data, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{
			"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}},
		}})
		require.NoError(t, err)

		line = data
	} else {
		line = claudeContextLine(t, "hook_additional_context", claudeEventPostTool, "call-1", []string{text})
	}

	require.NoError(t, os.WriteFile(filepath.Join(r.dir, guardContextFile), append(line, '\n'), 0o600))
}

// TestPostEditSlowProofNeedsTheCaseOfThePath checks the slow proof on
// synthetic recordings. A slow case with its reply proves its own path.
// It does not prove another path, and it does not prove its own path
// without the reply record. It gives no native proof.
func TestPostEditSlowProofNeedsTheCaseOfThePath(t *testing.T) {
	for _, client := range []string{ClaudeID, CodexID} {
		t.Run(client, func(t *testing.T) {
			name := map[string]string{ClaudeID: "slow-post-write", CodexID: "slow-post-add"}[client]
			x := postEditCaseRow(t, client, name)

			r := postEditRecording(t, client, x, postEditPayloads(client, x))
			require.Error(t, r.postEditSlowProof(t, x.tool, lessoncheck.ContextRoot), "the reply record is missing")

			writeSyntheticReply(t, r, client, x)
			require.NoError(t, r.postEditSlowProof(t, x.tool, lessoncheck.ContextRoot))
			require.Error(t, r.postEditSlowProof(t, x.tool, lessoncheck.ContextChild),
				"the recording holds no slow case of the child context")
		})
	}
}
