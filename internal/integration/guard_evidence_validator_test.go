package integration

// Tests of the validator behind TestGuardEvidenceMatchesRecordings.
// The recordings here are synthetic. They test the validator only and give
// no native proof. Every rejection case starts from a recording that the
// validator accepts, so a rejection cannot come from an incomplete fixture.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// synthRoot is the identity root of the synthetic audit digests.
const synthRoot = "/synthetic/root"

// synthCall is one pre-write attempt of a synthetic case.
type synthCall struct {
	tool  string
	agent string // names a child call when set
	call  string // the tool-call ID

	decision string // the guard decision; empty writes no guard record
	reply    bool   // the guard wrote its deny reply
	noExit2  bool   // the record names another protocol
	post     bool   // a post-edit record names the call
	postHook bool   // a PostToolUse payload of the tool exists
	noJoin   bool   // the recording has no identity row for the call
}

// synthDeny is a call that the guard denied with a written reply.
func synthDeny(tool, agent, call string) synthCall {
	return synthCall{tool: tool, agent: agent, call: call, decision: lessoncheck.GuardDeny, reply: true}
}

// synthRecording builds a recording of one case. The case runs once, holds
// the given calls, and leaves the fixture directories unchanged.
func synthRecording(name, phase string, calls ...synthCall) guardRecording {
	const session = "session-a"

	r := guardRecording{
		timingRecording: timingRecording{
			manifest: nativeManifest{Client: ClaudeID},
			runs:     map[string]int{name: 1},
			events:   map[int][]timingEvent{},
		},
	}
	files := map[string]string{"sentinel/a.txt": "hash"}
	c := guardCase{
		Run: 1, Case: name, Phase: phase,
		Before: guardState{Files: files}, After: guardState{Files: files},
	}

	for i, call := range calls {
		file := fmt.Sprintf("hooks/%03d-PreToolUse.json", i+1)
		raw := map[string]any{"tool_name": call.tool, "session_id": session, "tool_use_id": call.call}
		if call.agent != "" {
			raw["agent_id"] = call.agent
		}

		r.events[1] = append(r.events[1], timingEvent{
			hook: nativeHook{File: file, Run: 1, Event: "PreToolUse"}, seq: i, raw: raw,
		})

		receiver := ReceivingContextOf(ClaudeID, session, call.agent)
		id := guardIdentity{
			File: file, Run: 1, Case: name, Client: ClaudeID, Tool: call.tool, Lane: receiver.Lane,
			SessionSHA: reviews.SessionDigest(synthRoot, session),
			MatchSHA:   reviews.MatchDigest(synthRoot, session, call.call),
			ContextSHA: reviews.ContextDigest(synthRoot, ClaudeID, receiver.ID),
		}

		if !call.noJoin {
			r.identities = append(r.identities, id)
		}

		if call.decision != "" {
			protocol := lessoncheck.GuardProtocolExit2
			if call.noExit2 {
				protocol = "other"
			}

			r.audit = append(r.audit, lessoncheck.AuditRecord{
				Kind: lessoncheck.AuditGuard, Client: ClaudeID, Tool: call.tool,
				SessionSHA: id.SessionSHA, MatchSHA: id.MatchSHA, ContextSHA: id.ContextSHA,
				Guard: &lessoncheck.GuardDecision{Decision: call.decision, ReplyWritten: call.reply, Protocol: protocol},
			})
		}

		if call.post {
			r.audit = append(r.audit, lessoncheck.AuditRecord{
				Kind: lessoncheck.AuditPostEdit, Client: ClaudeID, Tool: call.tool,
				SessionSHA: id.SessionSHA, MatchSHA: id.MatchSHA, ContextSHA: id.ContextSHA,
			})
		}

		if call.postHook {
			r.events[1] = append(r.events[1], timingEvent{
				hook: nativeHook{Run: 1, Event: "PostToolUse"}, raw: map[string]any{"tool_name": call.tool},
			})
		}
	}

	c.Audit = [2]int{0, len(r.audit)}
	if phase == guardSlowPhase {
		c.PreWrite = []string{"slow 2"}
	}

	r.cases = map[string]guardCase{name: c}

	return r
}

// guardSlowPhase is the phase of the slow-guard case in guard-cases.jsonl.
const guardSlowPhase = "slow"

func synthPath(tool string, context lessoncheck.PathContext) guardPath {
	return guardPath{client: ClaudeID, tool: tool, context: context}
}

// mutateCase changes the only case of a recording.
func (r *guardRecording) mutateCase(change func(*guardCase)) {
	for name, c := range r.cases {
		change(&c)
		r.cases[name] = c
	}
}

// These synthetic recordings test the validator. They provide no native proof.
func TestGuardEvidenceValidatorAcceptsBoundDeny(t *testing.T) {
	root, child := synthDeny("Write", "", "call-root"), synthDeny("Write", "agent-1", "call-child")

	t.Run("a root deny certifies the root path only", func(t *testing.T) {
		got := synthRecording("one", "enforce", root).deniedPaths(t, ClaudeID)
		assert.Equal(t, map[guardPath]bool{synthPath("Write", lessoncheck.ContextRoot): true}, got)
	})
	t.Run("a bound child deny certifies the child path only", func(t *testing.T) {
		got := synthRecording("one", "enforce", child).deniedPaths(t, ClaudeID)
		assert.Equal(t, map[guardPath]bool{synthPath("Write", lessoncheck.ContextChild): true}, got)
	})
	t.Run("two bound denies certify two paths", func(t *testing.T) {
		got := synthRecording("one", "enforce", root, child).deniedPaths(t, ClaudeID)
		assert.Equal(t, map[guardPath]bool{
			synthPath("Write", lessoncheck.ContextRoot):  true,
			synthPath("Write", lessoncheck.ContextChild): true,
		}, got)
	})
}

func TestGuardEvidenceValidatorRejectsUnboundDeny(t *testing.T) {
	root, child := synthDeny("Write", "", "call-root"), synthDeny("Write", "agent-1", "call-child")
	rootPath, childPath := synthPath("Write", lessoncheck.ContextRoot), synthPath("Write", lessoncheck.ContextChild)

	for _, tc := range []struct {
		name  string
		build func() guardRecording
		want  map[guardPath]bool // the paths that stay certified
	}{
		{"a child call without a deny record cannot borrow the root deny", func() guardRecording {
			return synthRecording("one", "enforce", root, synthCall{tool: "Write", agent: "agent-1", call: "call-child"})
		}, map[guardPath]bool{rootPath: true}},
		{"a child call without an identity row stays unknown", func() guardRecording {
			c := child
			c.noJoin = true

			return synthRecording("one", "enforce", root, c)
		}, map[guardPath]bool{rootPath: true}},
		{"a root call without an identity row stays unknown", func() guardRecording {
			c := root
			c.noJoin = true

			return synthRecording("one", "enforce", c, child)
		}, map[guardPath]bool{childPath: true}},
		{"a recording without identity rows stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", root, child)
			r.identities = nil

			return r
		}, nil},
		{"one tool-call ID on two events identifies no attempt", func() guardRecording {
			return synthRecording("one", "enforce", root, synthDeny("Write", "", "call-root"))
		}, nil},
		{"an allow is no deny", func() guardRecording {
			return synthRecording("one", "enforce", synthCall{tool: "Write", call: "c", decision: lessoncheck.GuardAllow})
		}, nil},
		{"a deny without its written reply is no block", func() guardRecording {
			return synthRecording("one", "enforce", synthCall{tool: "Write", call: "c", decision: lessoncheck.GuardDeny})
		}, nil},
		{"a deny of another protocol is no block", func() guardRecording {
			c := synthDeny("Write", "", "c")
			c.noExit2 = true

			return synthRecording("one", "enforce", c)
		}, nil},
		{"a post-edit record of the call shows that the tool ran", func() guardRecording {
			c := synthDeny("Write", "", "c")
			c.post = true

			return synthRecording("one", "enforce", c)
		}, nil},
		{"a run that changed the fixture shows no block", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.mutateCase(func(c *guardCase) {
				c.After = guardState{Files: map[string]string{"sentinel/a.txt": "hash", "sentinel/b.txt": "new"}}
			})

			return r
		}, nil},
		{"a pre-write tool of another client is no evidence", func() guardRecording {
			return synthRecording("one", "enforce", synthDeny("apply_patch", "", "c"))
		}, nil},
		{"a join to the audit record of another context stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", child)
			r.audit[0].ContextSHA = reviews.ContextDigest(synthRoot, ClaudeID, ReceivingContextOf(ClaudeID, "session-a", "").ID)

			return r
		}, nil},
		{"an identity row with a malformed digest stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.identities[0].MatchSHA = strings.ToUpper(r.identities[0].MatchSHA)

			return r
		}, nil},
		{"an identity row of another case stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.identities[0].Case = "two"

			return r
		}, nil},
		{"an identity row of the other lane stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.identities[0].Lane = LaneChild

			return r
		}, nil},
		{"two audit records of one call stay ambiguous", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.audit = append(r.audit, r.audit[0])
			r.mutateCase(func(c *guardCase) { c.Audit = [2]int{0, 2} })

			return r
		}, nil},
		{"an audit range outside the stream stays unknown", func() guardRecording {
			r := synthRecording("one", "enforce", root)
			r.mutateCase(func(c *guardCase) { c.Audit = [2]int{0, 9} })

			return r
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.build().deniedPaths(t, ClaudeID)
			if len(tc.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGuardEvidenceValidatorSlowRecording(t *testing.T) {
	deny := synthDeny("Write", "", "call-slow")
	rootWrite := synthPath("Write", lessoncheck.ContextRoot)

	t.Run("a protected slow call qualifies its own path only", func(t *testing.T) {
		r := synthRecording(guardSlowGuard, guardSlowPhase, deny)
		assert.True(t, r.slowRecorded(), "any path")
		assert.True(t, r.slowRecorded(rootWrite))
		assert.False(t, r.slowRecorded(synthPath("Write", lessoncheck.ContextChild)), "another lane")
		assert.False(t, r.slowRecorded(synthPath("Edit", lessoncheck.ContextRoot)), "another tool")
		assert.False(t, r.slowRecorded(rootWrite, synthPath("Edit", lessoncheck.ContextRoot)), "two paths")
	})
	t.Run("a protected child call qualifies the child path", func(t *testing.T) {
		r := synthRecording(guardSlowGuard, guardSlowPhase, synthDeny("Write", "agent-1", "call-slow"))
		assert.True(t, r.slowRecorded(synthPath("Write", lessoncheck.ContextChild)))
		assert.False(t, r.slowRecorded(rootWrite))
	})

	for _, tc := range []struct {
		name  string
		build func() guardRecording
	}{
		{"a slow call that wrote gives no protection (fail-open)", func() guardRecording {
			r := synthRecording(guardSlowGuard, guardSlowPhase, deny)
			r.mutateCase(func(c *guardCase) {
				c.After = guardState{Files: map[string]string{"sentinel/a.txt": "hash", "sentinel/slow.txt": "created"}}
			})

			return r
		}},
		{"a slow call that left only a git status change gives no protection", func() guardRecording {
			r := synthRecording(guardSlowGuard, guardSlowPhase, deny)
			r.mutateCase(func(c *guardCase) { c.After.Status = []string{"?? sentinel/slow.txt"} })

			return r
		}},
		{"a hook that the client stopped is not a protected disposition", func() guardRecording {
			r := synthRecording(guardSlowGuard, guardSlowPhase, synthCall{tool: "Write", call: "call-slow"})
			r.mutateCase(func(c *guardCase) { c.PreWrite = nil })

			return r
		}},
		{"a wait without the deny exit status stays unknown", func() guardRecording {
			r := synthRecording(guardSlowGuard, guardSlowPhase, deny)
			r.mutateCase(func(c *guardCase) { c.PreWrite = []string{"slow 0"} })

			return r
		}},
		{"another phase is no slow case", func() guardRecording {
			r := synthRecording(guardSlowGuard, "enforce", deny)
			r.mutateCase(func(c *guardCase) { c.PreWrite = []string{"slow 2"} })

			return r
		}},
		{"a recording without identity rows stays unknown", func() guardRecording {
			r := synthRecording(guardSlowGuard, guardSlowPhase, deny)
			r.identities = nil

			return r
		}},
		{"two covered attempts leave the wrapper line without a call", func() guardRecording {
			return synthRecording(guardSlowGuard, guardSlowPhase, deny, synthDeny("Write", "", "call-other"))
		}},
		{"an allow is no protection", func() guardRecording {
			return synthRecording(guardSlowGuard, guardSlowPhase,
				synthCall{tool: "Write", call: "c", decision: lessoncheck.GuardAllow})
		}},
		{"a deny without its written reply is no protection", func() guardRecording {
			return synthRecording(guardSlowGuard, guardSlowPhase,
				synthCall{tool: "Write", call: "c", decision: lessoncheck.GuardDeny})
		}},
		{"a post-edit record of the call shows that the tool ran", func() guardRecording {
			c := deny
			c.post = true

			return synthRecording(guardSlowGuard, guardSlowPhase, c)
		}},
		{"a native PostToolUse of the tool defeats the proof", func() guardRecording {
			c := deny
			c.postHook = true

			return synthRecording(guardSlowGuard, guardSlowPhase, c)
		}},
		{"a case under another name is not the slow case", func() guardRecording {
			return synthRecording("other", guardSlowPhase, deny)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.build()
			assert.False(t, r.slowRecorded(), "any path")
			assert.False(t, r.slowRecorded(rootWrite), "root Write")
		})
	}
}

// TestGuardIdentityCollectorMatchesProduction runs the guard_identities
// step of scripts/capture-native.sh over a synthetic observer directory. It
// proves that the collector writes the digests of the production
// derivations (contract K2) and no raw ID, and that it refuses a fixture
// whose roots disagree. The step needs python3, git, and its sqlite3
// module; without them the test skips, and a skip proves nothing.
func TestGuardIdentityCollectorMatchesProduction(t *testing.T) {
	for _, tool := range []string{"python3", "git", "sh", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the collector test needs %s", tool)
		}
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-native.sh"))
	require.NoError(t, err)

	script := string(data)

	const marker = "\nguard_identities() {\n"

	start := strings.Index(script, marker)
	require.GreaterOrEqual(t, start, 0, "the script defines guard_identities")

	end := strings.Index(script[start+1:], "\n}\n")
	require.GreaterOrEqual(t, end, 0)

	function := script[start+1 : start+1+end+3]

	// Run the shell function alone, as the script does after guard_extras.
	run := func(t *testing.T, out string) (string, error) {
		t.Helper()

		cmd := exec.Command("sh", "-eu", "-c", function+"\nguard_identities")
		cmd.Env = append(os.Environ(), "OUT="+out)
		got, err := cmd.CombinedOutput()

		return string(got), err
	}

	type fixture struct {
		out, repo string
	}

	// build writes one fixture: a git repository, an index database whose
	// meta names metaRoot, and two PreToolUse payloads in an observer
	// directory (a root Write and a child Write), and two other events.
	build := func(t *testing.T, metaRoot func(repo string) string) fixture {
		t.Helper()

		out := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(out, "repo"), 0o755))

		// The collector accepts only a canonical root, and the system
		// temporary directory can be a link.
		repo, err := filepath.EvalSymlinks(filepath.Join(out, "repo"))
		require.NoError(t, err)

		git := exec.Command("git", "-C", repo, "init", "-q")
		got, err := git.CombinedOutput()
		require.NoError(t, err, string(got))

		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".seamark"), 0o755))
		sql := exec.Command("python3", "-I", "-c", `
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)")
db.execute("INSERT INTO meta VALUES ('repo_root', ?)", (sys.argv[2],))
db.commit()
`, filepath.Join(repo, ".seamark", "index.db"), metaRoot(repo))
		got, err = sql.CombinedOutput()
		require.NoError(t, err, string(got))

		observed := filepath.Join(out, "observed")
		require.NoError(t, os.MkdirAll(observed, 0o755))

		write := func(name, event string, start int64, payload map[string]any) {
			data, err := json.Marshal(payload)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(observed, name+".json"), data, 0o600))

			meta, err := json.Marshal(map[string]any{"event": event, "start_ns": start})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(observed, name+".meta.json"), meta, 0o600))
		}

		write("001-SessionStart", "SessionStart", 100, map[string]any{"session_id": "raw-session"})
		write("002-PreToolUse", "PreToolUse", 200, map[string]any{
			"session_id": "raw-session", "tool_use_id": "raw-call-root", "tool_name": "Write",
		})
		write("003-PreToolUse", "PreToolUse", 300, map[string]any{
			"session_id": "raw-session", "agent_id": "raw-agent", "tool_use_id": "raw-call-child", "tool_name": "Write",
		})
		write("004-PreToolUse", "PreToolUse", 400, map[string]any{
			"session_id": "raw-session", "tool_use_id": "raw-call-bash", "tool_name": "Bash",
		})

		record, err := json.Marshal(map[string]any{
			"client": ClaudeID, "repo": repo, "observed": observed, "runs": []int64{150},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(out, "record.json"), record, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(out, "guard-cases.jsonl"),
			[]byte(`{"run":1,"case":"edit-violation"}`+"\n"), 0o600))

		return fixture{out: out, repo: repo}
	}

	t.Run("it writes the production digests and no raw ID", func(t *testing.T) {
		f := build(t, func(repo string) string { return repo })

		got, err := run(t, f.out)
		require.NoError(t, err, got)

		data, err := os.ReadFile(filepath.Join(f.out, "guard-identities.jsonl"))
		require.NoError(t, err)

		var rows []guardIdentity

		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var row guardIdentity
			require.NoError(t, json.Unmarshal([]byte(line), &row))

			rows = append(rows, row)
		}

		root, child := ReceivingContextOf(ClaudeID, "raw-session", ""), ReceivingContextOf(ClaudeID, "raw-session", "raw-agent")
		require.Equal(t, []guardIdentity{
			{
				File: "hooks/002-PreToolUse.json", Run: 1, Case: "edit-violation", Client: ClaudeID, Tool: "Write",
				Lane:       LaneRoot,
				SessionSHA: reviews.SessionDigest(f.repo, "raw-session"),
				MatchSHA:   reviews.MatchDigest(f.repo, "raw-session", "raw-call-root"),
				ContextSHA: reviews.ContextDigest(f.repo, ClaudeID, root.ID),
			},
			{
				File: "hooks/003-PreToolUse.json", Run: 1, Case: "edit-violation", Client: ClaudeID, Tool: "Write",
				Lane:       LaneChild,
				SessionSHA: reviews.SessionDigest(f.repo, "raw-session"),
				MatchSHA:   reviews.MatchDigest(f.repo, "raw-session", "raw-call-child"),
				ContextSHA: reviews.ContextDigest(f.repo, ClaudeID, child.ID),
			},
		}, rows, "the Bash call and the SessionStart payload get no row")

		for _, raw := range []string{"raw-session", "raw-agent", "raw-call", f.repo} {
			assert.NotContains(t, string(data), raw, "the file stores no raw ID or root")
		}

		record, err := os.ReadFile(filepath.Join(f.out, "record.json"))
		require.NoError(t, err)
		assert.Contains(t, string(record), "guard-identities.jsonl", "the sanitizer takes the file from the record extras")
	})

	t.Run("it refuses an index root that differs from the git root", func(t *testing.T) {
		f := build(t, func(string) string { return "/elsewhere" })

		got, err := run(t, f.out)
		require.Error(t, err)
		assert.Contains(t, got, "canonical fixture git/index root")
		assert.NoFileExists(t, filepath.Join(f.out, "guard-identities.jsonl"), "no partial promotion")
	})
}
