package integration

// Tests of the Claude run verdict of scripts/native-record.sh. They run
// the shell functions claude_failure and claude_print alone, and both
// Claude runners with a stand-in claude program. The stand-in prints a
// result in the shape of `claude -p --output-format json`. So the tests
// need no model, no login, and no client CLI. These tests prove the
// script, not the client.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeLimitText is the limit text of Claude Code 2.1.310, as a native
// guard recording got it in each of its 13 error results. The time zone
// is UTC in place of the zone of the recording account. The limit match
// reads only the start of the text, so the zone does not change a verdict.
const claudeLimitText = "You've hit your weekly limit · resets Oct 29 at 10am (UTC)"

// claudeTransientText is the transient 429 text of Claude Code 2.1.319.
// The client marks it as a transient error, not as a usage limit.
const claudeTransientText = "API Error: Server is temporarily limiting requests (not your usage limit) · retry"

// claudeServerText is a server error that is not a limit.
const claudeServerText = "API Error: 500 Internal server error"

// claudePausedText is the fixed text of Claude Code 2.1.319 when the
// usage of the whole organization is paused. The client marks it as a
// usage limit, but its prefix list does not hold it.
const claudePausedText = "Usage is paused for your organization. For help, please contact support."

// claudeResult returns one result object of `claude -p --output-format
// json` with the given error flag and result text.
func claudeResult(t *testing.T, isError bool, text string) string {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"type": "result", "is_error": isError, "result": text,
		"session_id": "session-1", "total_cost_usd": 0,
	})
	require.NoError(t, err)

	return string(data)
}

// writeScratch writes text into a new scratch file and returns its path.
func writeScratch(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

// TestNativeRecordClaudeFailureReadsResult proves the verdict of
// claude_failure. Each limit prefix, the limit pattern, and the
// organization pause text of Claude Code 2.1.319 give verdict 2. Other
// errors give 1, and runs without an error result give 0.
func TestNativeRecordClaudeFailureReadsResult(t *testing.T) {
	limits := []string{
		"You've hit your session limit",
		"You've reached your Fable limit.",
		"You're out of usage credits",
		"Your org is out of usage · add funds to continue",
		"Your org is out of usage · contact your admin",
		"Your seat type doesn't include usage credits",
		"Your seat type doesn't include usage",
		"Your usage allocation has been disabled by your admin",
		"Your group's usage limit is set to $0",
		"Fable 5 requires usage credits",
		"You're out of extra usage",
		"Your seat type doesn't include extra usage",
		"Opus 5.5 requires usage credits.",
		"Haiku requires usage credits. Add credits to continue.",
		"Opus " + strings.Repeat("a", 40) + " requires usage credits.",
		claudePausedText,
		claudeLimitText,
	}

	type verdictCase struct {
		name, file, verdict, reason string
	}

	var cases []verdictCase

	for _, text := range limits {
		cases = append(cases, verdictCase{"limit " + text, claudeResult(t, true, text), "2", text})
	}

	// The limit pattern matches only at the start, needs its final period,
	// takes at most 40 characters of model name, and takes no middle dot.
	nearPattern := []string{
		"API Error: Opus 5.5 requires usage credits.",
		"Opus 5.5 requires usage credits",
		"Opus " + strings.Repeat("a", 41) + " requires usage credits.",
		"Opus 5·5 requires usage credits.",
	}

	for _, text := range nearPattern {
		cases = append(cases, verdictCase{"near the pattern " + text, claudeResult(t, true, text), "1", text})
	}

	cases = append(cases, []verdictCase{
		{"a line break in a limit prefix", claudeResult(t, true, "You've hit\nyour weekly limit"), "2",
			"You've hit your weekly limit"},
		{"the transient 429 text", claudeResult(t, true, claudeTransientText), "1", claudeTransientText},
		{"a server error", claudeResult(t, true, claudeServerText), "1", claudeServerText},
		{"a rate limit", claudeResult(t, true, "API Error: Rate limit reached, retry later"), "1",
			"API Error: Rate limit reached, retry later"},
		{"limit text after an API error prefix", claudeResult(t, true, "API Error: "+claudeLimitText), "1",
			"API Error: " + claudeLimitText},
		{"a usage warning", claudeResult(t, true, "You've used 90% of your weekly limit"), "1",
			"You've used 90% of your weekly limit"},
		{"a close-to-limit warning", claudeResult(t, true, "You're close to your weekly limit"), "1",
			"You're close to your weekly limit"},
		{"a tab and an escape", claudeResult(t, true, "Error:\x1b\tboom"), "1", "Error: boom"},
		{"an error with no result", `{"type":"result","is_error":true}`, "1", ""},
		{"a model reply with limit text", claudeResult(t, false, claudeLimitText), "0", ""},
		{"no is_error field", `{"type":"result","result":"` + claudeLimitText + `"}`, "0", ""},
		{"is_error as a string", `{"type":"result","is_error":"true","result":"` + claudeLimitText + `"}`, "0", ""},
		{"not JSON", "Error: not logged in\n", "0", ""},
		{"a JSON array", `["` + claudeLimitText + `"]`, "0", ""},
	}...)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeScratch(t, "run-1.json", tc.file)

			got, code := recordScript(t,
				"verdict=0\nreason=$(claude_failure '"+path+"') || verdict=$?\nprintf 'verdict=%s\\nreason=%s\\n' \"$verdict\" \"$reason\"\n",
				"claude_failure")
			require.Equal(t, 0, code, got)

			assert.Equal(t, "verdict="+tc.verdict+"\nreason="+tc.reason+"\n", got)
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		got, code := recordScript(t,
			"verdict=0\nreason=$(claude_failure \"$PWD/missing.json\") || verdict=$?\nprintf 'verdict=%s\\nreason=%s\\n' \"$verdict\" \"$reason\"\n",
			"claude_failure")
		require.Equal(t, 0, code, got)

		assert.Equal(t, "verdict=0\nreason=\n", got)
	})
}

// TestNativeRecordClaudeFailureKeepsTheCodexLineRule proves that the
// Claude and the Codex reader clean a text with the same one-line rule,
// because both texts go to the same terminal lines.
func TestNativeRecordClaudeFailureKeepsTheCodexLineRule(t *testing.T) {
	script := nativeRecordScript(t)

	oneLine := func(name string) string {
		body := scriptFunction(t, script, name)

		start := strings.Index(body, "\ndef one_line(")
		require.GreaterOrEqual(t, start, 0, "%s defines one_line", name)

		end := strings.Index(body[start+1:], "\n\n")
		require.GreaterOrEqual(t, end, 0)

		return body[start+1 : start+1+end]
	}

	assert.Equal(t, oneLine("codex_failure"), oneLine("claude_failure"))
}

// claudeRunner is one Claude runner of the script with the program that
// calls it.
type claudeRunner struct {
	name, scenario, call, label string
	functions                   []string
}

// claudeRunners are the timing and the guard runner.
var claudeRunners = []claudeRunner{
	{"timing", "timing", "timing_claude add-case new 'add a file'", "run 1 (add-case)", []string{"timing_claude"}},
	{"guard", "guard", "gcase=add-violation\nguard_claude", "run 1 (add-violation)", []string{"guard_claude"}},
}

// fakeClaudeResult is the stand-in claude program of the runner tests. It
// writes a debug log without the advisor line, so the advisor reads off.
// It prints FAKE_RESULT to stdout and FAKE_STDERR to stderr, and exits
// with FAKE_EXIT.
const fakeClaudeResult = `#!/bin/sh
previous=
for a in "$@"; do
    [ "$previous" != --debug-file ] || echo "2026-11-08T10:00:00.000Z [DEBUG] startup" >> "$a"
    previous=$a
done
cat > /dev/null
cat "$FAKE_RESULT"
cat "$FAKE_STDERR" >&2
exit "$FAKE_EXIT"
`

// runClaudeRunner runs one runner with the stand-in. The stand-in prints
// the result text and the stderr text, and exits with status. The program
// prints "next run starts" after the runner. It returns the combined
// output, the exit status of sh, and the output directory.
func runClaudeRunner(t *testing.T, runner claudeRunner, result, stderr, status string) (output string, code int, out string) {
	t.Helper()

	root := t.TempDir()
	bin, repo := filepath.Join(root, "bin"), filepath.Join(root, "repo")
	out = filepath.Join(root, "out")

	for _, dir := range []string{bin, out, repo} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}

	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeResult), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "prompt-1.txt"), []byte("add a file"), 0o600))

	resultPath := writeScratch(t, "result.json", result)
	stderrPath := writeScratch(t, "stderr.txt", stderr)

	// The stubs replace the setup steps that the runners do not need here.
	program := "PATH='" + bin + "':$PATH\nOUT='" + out + "'\nREPO='" + repo + "'\nCLIENT=claude\n" +
		"SCENARIO=" + runner.scenario + "\nRUN=1\nmodel=m\nagents='{}'\nSESSION=\n" +
		"FAKE_RESULT='" + resultPath + "'\nFAKE_STDERR='" + stderrPath + "'\nFAKE_EXIT=" + status + "\n" +
		"export FAKE_RESULT FAKE_STDERR FAKE_EXIT\n" +
		"timing_start() { RUN=1; }\nguard_restore() { echo 'guard setup restored'; }\n" +
		runner.call + "\necho 'next run starts'\n"

	functions := append([]string{"summary", "claude_print", "claude_advisor", "claude_failure", "limit_stop"},
		runner.functions...)

	output, code = recordScript(t, program, functions...)

	return output, code, out
}

// TestNativeRecordClaudeRunnersStopAtLimit proves that both Claude
// runners stop the record call at the recorded limit result, whatever
// the exit status of claude. The limit run keeps its advisor line and
// its advisor record, because the stop comes after the advisor check.
// The guard runner first restores the enforce setup, which a stub
// records.
func TestNativeRecordClaudeRunnersStopAtLimit(t *testing.T) {
	for _, runner := range claudeRunners {
		for _, status := range []string{"0", "1"} {
			t.Run(runner.name+" exit "+status, func(t *testing.T) {
				got, code, out := runClaudeRunner(t, runner, claudeResult(t, true, claudeLimitText), "", status)

				advisor, err := os.ReadFile(filepath.Join(out, "advisor.jsonl"))
				require.NoError(t, err, got)
				assert.Contains(t, string(advisor), `{"run": 1, "advisor": "off"`)

				assert.Equal(t, 1, code, got)
				assert.Contains(t, got, "  advisor  run 1: off\n")
				assert.Contains(t, got, "  FAIL     run 1 stopped at a Claude Code usage or account limit: "+claudeLimitText+"\n")
				assert.Contains(t, got, "no later run starts")
				assert.Contains(t, got, "result: FAIL")
				assert.NotContains(t, got, "next run starts")
				assert.NotContains(t, got, "finished (session")
				assert.NotContains(t, got, "  cost")
				assert.Equal(t, runner.scenario == "guard", strings.Contains(got, "guard setup restored"), got)
			})
		}
	}
}

// TestNativeRecordClaudeRunnersGoOnAfterOtherError proves that an error
// result without a limit, such as the transient 429 text, prints FAIL
// and lets the call go on to the next run.
func TestNativeRecordClaudeRunnersGoOnAfterOtherError(t *testing.T) {
	for _, runner := range claudeRunners {
		for _, text := range []string{claudeServerText, claudeTransientText} {
			for _, status := range []string{"0", "1"} {
				t.Run(runner.name+" "+text+" exit "+status, func(t *testing.T) {
					got, code, out := runClaudeRunner(t, runner, claudeResult(t, true, text), "", status)

					want := "  FAIL     " + runner.label + " failed: " + text + "\n"
					if status == "0" {
						want = "  FAIL     " + runner.label + " reported an error; see " + filepath.Join(out, "run-1.json") + "\n"
					}

					assert.Equal(t, 0, code, got)
					assert.Contains(t, got, want)
					assert.Contains(t, got, "next run starts")
					assert.NotContains(t, got, "stopped at")
					assert.NotContains(t, got, "finished (session")
				})
			}
		}
	}
}

// TestNativeRecordClaudeRunnersNameTheFailure proves the reason of a
// failed run: the error result text first, then the last non-empty
// stderr line on one clean line, then the exit status.
func TestNativeRecordClaudeRunnersNameTheFailure(t *testing.T) {
	cases := []struct {
		name, result, stderr, status, reason string
	}{
		{"result text before stderr", claudeResult(t, true, claudeServerText), "Error: other\n", "1", claudeServerText},
		{"last non-empty stderr line", "", "first\nError: not logged in\n\n", "1", "Error: not logged in"},
		{"no message at all", "", "", "7", "claude exited with status 7 and printed no message"},
		{"a stderr line with an escape and a tab", "", "Error:\x1b\tboom\n", "1", "Error: boom"},
	}

	for _, runner := range claudeRunners {
		for _, tc := range cases {
			t.Run(runner.name+" "+tc.name, func(t *testing.T) {
				got, code, _ := runClaudeRunner(t, runner, tc.result, tc.stderr, tc.status)

				assert.Equal(t, 0, code, got)
				assert.Contains(t, got, "  FAIL     "+runner.label+" failed: "+tc.reason+"\n")
				assert.Contains(t, got, "next run starts")
			})
		}
	}
}
