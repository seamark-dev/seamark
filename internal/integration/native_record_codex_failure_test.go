package integration

// Tests of the Codex turn verdict of scripts/native-record.sh. They run
// the shell functions codex_failure and codex_verdict alone on event
// streams in the shape of `codex exec --json`. So they need no model, no
// login, and no client CLI. These tests prove the script, not the client.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexLimitMessage is the usage-limit text of Codex 0.164.0, as a native
// guard recording got it in an error event and a turn.failed event.
const codexLimitMessage = "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), " +
	"visit https://chatgpt.com/settings/usage to purchase more credits or try again at Nov 8th, 2026 11:02 PM."

// codexLimitEvents is the event stream of that recorded run: the turn
// starts, then an error event and a turn.failed event carry the limit.
const codexLimitEvents = `{"type":"thread.started","thread_id":"thread-1"}
{"type":"turn.started"}
{"type":"error","message":"` + codexLimitMessage + `"}
{"type":"turn.failed","error":{"message":"` + codexLimitMessage + `"}}
`

// codexBackslashEvents is the event stream of a failed turn whose message
// holds backslash escapes and "\c", as a Windows path can. The JSON text
// escapes each backslash.
const codexBackslashEvents = `{"type":"thread.started","thread_id":"thread-4"}
{"type":"turn.failed","error":{"message":"path C:\\new\\table and \\c end"}}
`

// codexDoneEvents is the event stream of a turn that completed.
const codexDoneEvents = `{"type":"thread.started","thread_id":"thread-2"}
{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}
`

// codexFailedEvents is the event stream of a turn that failed for another
// reason. The message holds a line break and a tab, which the reason must
// not keep, because the FAIL line is one terminal line.
const codexFailedEvents = `{"type":"thread.started","thread_id":"thread-3"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"stream disconnected\nbefore completion:\tconnection reset"}}
`

// recordScript runs a shell program after the counters, the one-line
// helpers, and the named functions of the script, in a scratch output
// directory. The client is codex, and the scenario is timing, so a stop
// restores no guard setup. It returns the combined output and the exit
// status of sh. A program can set CLIENT or SCENARIO again to change that.
func recordScript(t *testing.T, program string, functions ...string) (output string, code int) {
	t.Helper()
	requireTools(t, "sh", "python3")

	script := nativeRecordScript(t)

	var defs strings.Builder
	defs.WriteString("FAILED=0\nOK=0\nBLOCKED=0\nMODE=record\nCLIENT=codex\nSCENARIO=timing\n")

	for _, prefix := range []string{"ok()", "fail()", "blocked()", "note()"} {
		defs.WriteString(scriptLine(t, script, prefix))
	}

	for _, name := range functions {
		defs.WriteString(scriptFunction(t, script, name))
	}

	cmd := exec.Command("sh", "-eu", "-c", defs.String()+program)
	cmd.Dir = t.TempDir()

	got, err := cmd.CombinedOutput()
	if err == nil {
		return string(got), 0
	}

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, string(got))

	return string(got), exitErr.ExitCode()
}

// writeEvents writes one event stream into a scratch file and returns its
// path.
func writeEvents(t *testing.T, events string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(events), 0o600))

	return path
}

// TestNativeRecordCodexFailureReadsEvents proves that codex_failure
// takes the failure reason from the event stream: a usage limit gives
// verdict 2, another turn.failed gives verdict 1 with its message on one
// line, and a completed turn gives verdict 0 with no message.
func TestNativeRecordCodexFailureReadsEvents(t *testing.T) {
	cases := []struct {
		name, events, reason string
		verdict              string
	}{
		{"usage limit", codexLimitEvents, codexLimitMessage, "2"},
		{"other failed turn", codexFailedEvents, "stream disconnected before completion: connection reset", "1"},
		{"completed turn", codexDoneEvents, "", "0"},
		{"account limit in a failed turn only", `{"type":"turn.failed","error":{"message":"Your account limit is reached."}}` + "\n",
			"Your account limit is reached.", "2"},
		{"rate limit is a plain failure", `{"type":"turn.failed","error":{"message":"Rate limit reached, retry later."}}` + "\n",
			"Rate limit reached, retry later.", "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEvents(t, tc.events)

			got, code := recordScript(t,
				"verdict=0\nreason=$(codex_failure '"+path+"') || verdict=$?\nprintf 'verdict=%s\\nreason=%s\\n' \"$verdict\" \"$reason\"\n",
				"codex_failure")
			require.Equal(t, 0, code, got)

			assert.Equal(t, "verdict="+tc.verdict+"\nreason="+tc.reason+"\n", got)
		})
	}
}

// TestNativeRecordCodexVerdictStopsAtLimit proves that codex_verdict
// stops the record call at a usage limit, also when codex exits non-zero
// with an empty stderr. The FAIL line names the limit message as given,
// the summary exits 1, and the next turn never starts.
func TestNativeRecordCodexVerdictStopsAtLimit(t *testing.T) {
	cases := []struct{ name, events, message string }{
		{"recorded limit", codexLimitEvents, codexLimitMessage},
		{"limit message with backslashes",
			`{"type":"turn.failed","error":{"message":"Your usage limit is reached at C:\\c"}}` + "\n",
			`Your usage limit is reached at C:\c`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := writeEvents(t, tc.events)
			stderr := writeEvents(t, "")

			got, code := recordScript(t,
				"codex_verdict 'run 1 (add-violation)' 1 '"+events+"' '"+stderr+"' || true\necho 'next turn starts'\n",
				"summary", "codex_failure", "codex_verdict", "limit_stop")

			assert.Equal(t, 1, code, got)
			assert.Contains(t, got, "  FAIL     run 1 (add-violation) stopped at a Codex usage or account limit: "+
				tc.message+"\n  note     no later run starts;")
			assert.Contains(t, got, "result: FAIL")
			assert.NotContains(t, got, "next turn starts")
		})
	}
}

// TestNativeRecordCodexVerdictFailsAndPasses proves that codex_verdict
// prints the turn.failed message as the reason and lets the call go on,
// fails a turn that exits 0 with turn.failed, keeps the stderr line when
// the stream has no message, and passes a completed turn.
func TestNativeRecordCodexVerdictFailsAndPasses(t *testing.T) {
	cases := []struct {
		name, events, stderr, status, want string
	}{
		{"failed turn with a message", codexFailedEvents, "", "1",
			"  FAIL     run 2 (case) failed: stream disconnected before completion: connection reset\nreturned=1\nfailed=1\n"},
		{"exit 0 with turn.failed", codexFailedEvents, "", "0",
			"  FAIL     run 2 (case) failed: stream disconnected before completion: connection reset\nreturned=1\nfailed=1\n"},
		{"no event message keeps stderr", "", "first\nError: not logged in\n", "1",
			"  FAIL     run 2 (case) failed: Error: not logged in\nreturned=1\nfailed=1\n"},
		{"no message at all", "", "", "7",
			"  FAIL     run 2 (case) failed: codex exited with status 7 and printed no message\nreturned=1\nfailed=1\n"},
		{"failed turn with backslashes", codexBackslashEvents, "", "1",
			"  FAIL     run 2 (case) failed: path C:\\new\\table and \\c end\nreturned=1\nfailed=1\n"},
		{"stderr with backslashes", "", "first\nError: C:\\temp\\c\n", "1",
			"  FAIL     run 2 (case) failed: Error: C:\\temp\\c\nreturned=1\nfailed=1\n"},
		{"completed turn", codexDoneEvents, "", "0", "returned=0\nfailed=0\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := writeEvents(t, tc.events)
			stderr := writeEvents(t, tc.stderr)

			got, code := recordScript(t,
				"returned=0\ncodex_verdict 'run 2 (case)' "+tc.status+" '"+events+"' '"+stderr+"' || returned=$?\n"+
					"printf 'returned=%s\\nfailed=%s\\n' \"$returned\" \"$FAILED\"\n",
				"summary", "codex_failure", "codex_verdict", "limit_stop")
			require.Equal(t, 0, code, got)

			assert.Equal(t, tc.want, got)
		})
	}
}

// TestNativeRecordCodexRunnersStopAtLimit proves that each Codex turn
// runner of the script judges its turn with codex_verdict. A stand-in
// codex prints the recorded limit stream, writes nothing to stderr, and
// exits 1. Each runner then stops the call before the next turn. The
// guard scenario first restores the enforce setup, and the post-edit
// scenario its check setup. A stub records each restore.
func TestNativeRecordCodexRunnersStopAtLimit(t *testing.T) {
	cases := []struct {
		name, scenario, call string
		functions            []string
	}{
		{"timing", "timing", "timing_codex add-case new 'add a file'", []string{"timing_codex"}},
		{"guard", "guard", "gcase=add-violation\nguard_codex", []string{"guard_codex"}},
		{"post-edit", "post-edit", "gcase=slow-post-subagent-add\nguard_codex", []string{"guard_codex"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin, out, repo := filepath.Join(root, "bin"), filepath.Join(root, "out"), filepath.Join(root, "repo")

			for _, dir := range []string{bin, out, repo} {
				require.NoError(t, os.MkdirAll(dir, 0o755))
			}

			events := writeEvents(t, codexLimitEvents)
			fake := "#!/bin/sh\ncat > /dev/null\ncat '" + events + "'\nexit 1\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte(fake), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(out, "runs.txt"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(out, "prompt-1.txt"), []byte("add a file"), 0o600))

			// The stubs replace the setup steps that the stop does not need.
			program := "PATH='" + bin + "':$PATH\nOUT='" + out + "'\nREPO='" + repo + "'\nBIN='" + filepath.Join(bin, "seamark") + "'\n" +
				"SCENARIO=" + tc.scenario + "\nRUN=1\nmodel=m\nSESSION=\n" +
				"timing_start() { RUN=1; }\nguard_restore() { echo 'guard setup restored'; }\n" +
				"post_restore() { echo 'post-edit setup restored'; }\n" +
				tc.call + "\necho 'next turn starts'\n"

			got, code := recordScript(t, program, append([]string{"summary", "codex_failure", "codex_verdict", "limit_stop"}, tc.functions...)...)

			assert.Equal(t, 1, code, got)
			assert.Contains(t, got, "stopped at a Codex usage or account limit: "+codexLimitMessage+"\n")
			assert.Contains(t, got, "result: FAIL")
			assert.NotContains(t, got, "next turn starts")
			assert.Equal(t, tc.scenario == "guard", strings.Contains(got, "guard setup restored"), got)
			assert.Equal(t, tc.scenario == "post-edit", strings.Contains(got, "post-edit setup restored"), got)
		})
	}
}
