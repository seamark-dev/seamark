package integration

// Tests of the advisor switch of scripts/native-record.sh. They run the
// shell functions claude_print and claude_advisor alone, with a stand-in
// claude program on PATH. So they need no model, no login, and no client
// CLI. The stand-in copies the behavior of Claude Code 2.1.314: it appends
// to the debug file, and it writes the advisor debug line only when
// CLAUDE_CODE_DISABLE_ADVISOR_TOOL is not 1. claude_print also runs
// claude_failure on the result. The stand-in prints no JSON, so that
// verdict is 0 and the expected output stays the same. These tests prove
// the script, not the client.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// advisorDebugLine is the debug line of Claude Code 2.1.314 when the
// advisor tool is on, as a native timing recording logged it.
const advisorDebugLine = "2026-11-03T12:22:30.000Z [DEBUG] [AdvisorTool] Server-side tool enabled with " +
	"claude-opus-5-5 as the advisor model"

// fakeClaude is the stand-in claude program. It prints the switch, its
// arguments, its working directory, and its standard input. It appends a
// debug log to the --debug-file path, as the real client does. The log
// holds the advisor line only when the switch is not 1, or when
// FAKE_CLAUDE_ADVISOR=on makes it ignore the switch. FAKE_CLAUDE_EXIT
// sets its exit status.
const fakeClaude = `#!/bin/sh
debug=
previous=
for a in "$@"; do
    [ "$previous" != --debug-file ] || debug=$a
    previous=$a
done
echo "switch=${CLAUDE_CODE_DISABLE_ADVISOR_TOOL:-}"
echo "args=$*"
echo "cwd=$(pwd -P)"
echo "prompt=$(cat)"
if [ -n "$debug" ]; then
    echo "2026-11-03T12:22:29.000Z [DEBUG] startup" >> "$debug"
    if [ "${CLAUDE_CODE_DISABLE_ADVISOR_TOOL:-}" != 1 ] || [ "${FAKE_CLAUDE_ADVISOR:-}" = on ]; then
        echo "$ADVISOR_LINE" >> "$debug"
    fi
fi
exit "${FAKE_CLAUDE_EXIT:-0}"
`

// advisorRecord is one line of advisor.jsonl.
type advisorRecord struct {
	Run          int    `json:"run"`
	Advisor      string `json:"advisor"`
	AdvisorModel string `json:"advisor_model"`
	Switch       string `json:"switch"`
}

// nativeRecordScript returns the text of scripts/native-record.sh.
func nativeRecordScript(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "native-record.sh"))
	require.NoError(t, err)

	return string(data)
}

// scriptFunction returns the definition of one shell function of the
// script, from its name line to its closing brace.
func scriptFunction(t *testing.T, script, name string) string {
	t.Helper()

	marker := "\n" + name + "() {\n"

	start := strings.Index(script, marker)
	require.GreaterOrEqual(t, start, 0, "the script defines %s", name)

	end := strings.Index(script[start+1:], "\n}\n")
	require.GreaterOrEqual(t, end, 0)

	return script[start+1 : start+1+end+3]
}

// scriptLine returns the first line of the script that starts with
// prefix, for example a helper function on one line.
func scriptLine(t *testing.T, script, prefix string) string {
	t.Helper()

	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line + "\n"
		}
	}

	require.FailNow(t, "the script has no line that starts with "+prefix)

	return ""
}

// requireTools skips the test when a tool of the shell functions is
// missing. A skip proves nothing.
func requireTools(t *testing.T, tools ...string) {
	t.Helper()

	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the test needs %s", tool)
		}
	}
}

// advisorRun is the scratch layout of one function run.
type advisorRun struct {
	repo, out, bin string
}

// newAdvisorRun creates a scratch repository directory, an output
// directory with one prompt, and a directory that holds the stand-in.
func newAdvisorRun(t *testing.T) advisorRun {
	t.Helper()

	// The stand-in prints its physical working directory, and the system
	// temporary directory can be a link.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	r := advisorRun{
		repo: filepath.Join(root, "repo"),
		out:  filepath.Join(root, "out"),
		bin:  filepath.Join(root, "bin"),
	}

	for _, dir := range []string{r.repo, r.out, r.bin} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}

	require.NoError(t, os.WriteFile(filepath.Join(r.bin, "claude"), []byte(fakeClaude), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(r.out, "prompt-1.txt"), []byte("reply with ok"), 0o600))

	return r
}

// run runs a shell program after the fail helper and the named functions
// of the script, with the stand-in first on PATH. The program then prints
// the FAILED counter, which the summary of the script reads. It returns
// the combined output.
func (r advisorRun) run(t *testing.T, program string, env []string, functions ...string) (string, error) {
	t.Helper()

	script := nativeRecordScript(t)

	var defs strings.Builder
	defs.WriteString("FAILED=0\n")
	defs.WriteString(scriptLine(t, script, "fail()"))

	for _, name := range functions {
		defs.WriteString(scriptFunction(t, script, name))
	}

	cmd := exec.Command("sh", "-eu", "-c", defs.String()+program+"\necho \"failed=$FAILED\"\n")
	cmd.Env = append(os.Environ(),
		"PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"REPO="+r.repo, "OUT="+r.out, "ADVISOR_LINE="+advisorDebugLine,
		// The parent environment must not decide the switch.
		"CLAUDE_CODE_DISABLE_ADVISOR_TOOL=")
	cmd.Env = append(cmd.Env, env...)

	got, err := cmd.CombinedOutput()

	return string(got), err
}

// advisorRecords decodes advisor.jsonl of the output directory.
func (r advisorRun) advisorRecords(t *testing.T) []advisorRecord {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(r.out, "advisor.jsonl"))
	require.NoError(t, err)

	var records []advisorRecord

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec advisorRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)

		records = append(records, rec)
	}

	return records
}

// TestNativeRecordClaudePrintTurnsAdvisorOff proves that claude_print
// starts claude with CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1 in the scratch
// repository, and records the advisor state of the run after it.
func TestNativeRecordClaudePrintTurnsAdvisorOff(t *testing.T) {
	requireTools(t, "sh", "sed", "grep", "head", "python3")

	r := newAdvisorRun(t)
	debug := filepath.Join(r.out, "run-1-debug.log")

	got, err := r.run(t, `claude_print 1 --model haiku --debug-file "$OUT/run-1-debug.log"`, nil,
		"claude_print", "claude_advisor", "claude_failure")
	require.NoError(t, err, got)
	assert.Equal(t, "  advisor  run 1: off\nfailed=0\n", got)

	result, err := os.ReadFile(filepath.Join(r.out, "run-1.json"))
	require.NoError(t, err)
	assert.Equal(t, "switch=1\nargs=-p --model haiku --debug-file "+debug+"\ncwd="+r.repo+"\nprompt=reply with ok\n",
		string(result))

	assert.Equal(t, []advisorRecord{{Run: 1, Advisor: "off", Switch: "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"}},
		r.advisorRecords(t))
}

// TestNativeRecordClaudePrintRecordsAFailedRun proves that a failed run
// keeps the exit status of claude and still records its advisor state.
func TestNativeRecordClaudePrintRecordsAFailedRun(t *testing.T) {
	requireTools(t, "sh", "sed", "grep", "head", "python3")

	r := newAdvisorRun(t)

	program := `if claude_print 1 --debug-file "$OUT/run-1-debug.log"; then echo status=0; else echo "status=$?"; fi`
	got, err := r.run(t, program, []string{"FAKE_CLAUDE_EXIT=3"}, "claude_print", "claude_advisor", "claude_failure")
	require.NoError(t, err, got)
	assert.Equal(t, "  advisor  run 1: off\nstatus=3\nfailed=0\n", got)

	assert.Equal(t, []advisorRecord{{Run: 1, Advisor: "off", Switch: "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"}},
		r.advisorRecords(t))
}

// TestNativeRecordClaudePrintIgnoresAnEarlierRun proves that the files
// of an earlier call with the same run number stay out of the new run.
// The client appends to the debug file. So an old advisor line would make
// the new run read on, although the switch kept the tool off.
func TestNativeRecordClaudePrintIgnoresAnEarlierRun(t *testing.T) {
	requireTools(t, "sh", "sed", "grep", "head", "rm", "python3")

	r := newAdvisorRun(t)
	debug := filepath.Join(r.out, "run-1-debug.log")
	stderr := filepath.Join(r.out, "run-1-stderr.txt")

	require.NoError(t, os.WriteFile(debug, []byte("2026-11-02T09:00:00.000Z [DEBUG] startup\n"+advisorDebugLine+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.out, "run-1.json"), []byte(`{"session_id": "OLD-RUN-SESSION"}`), 0o600))
	require.NoError(t, os.WriteFile(stderr, []byte("OLD-RUN-STDERR\n"), 0o600))

	got, err := r.run(t, `claude_print 1 --debug-file "$OUT/run-1-debug.log"`, nil, "claude_print", "claude_advisor", "claude_failure")
	require.NoError(t, err, got)
	assert.Equal(t, "  advisor  run 1: off\nfailed=0\n", got)

	log, err := os.ReadFile(debug)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-03T12:22:29.000Z [DEBUG] startup\n", string(log), "the debug log holds the new run only")

	result, err := os.ReadFile(filepath.Join(r.out, "run-1.json"))
	require.NoError(t, err)
	// A marker that no path holds, because the stand-in prints its arguments
	// and its directory, and a temporary path can hold any short word.
	assert.NotContains(t, string(result), "OLD-RUN-SESSION")

	errText, err := os.ReadFile(stderr)
	require.NoError(t, err)
	assert.Empty(t, string(errText))

	assert.Equal(t, []advisorRecord{{Run: 1, Advisor: "off", Switch: "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"}},
		r.advisorRecords(t))
}

// TestNativeRecordClaudePrintFailsAnAdvisorRun proves that a run with
// the advisor on prints FAIL and counts it for the summary. The run keeps
// its own exit status and its advisor record.
func TestNativeRecordClaudePrintFailsAnAdvisorRun(t *testing.T) {
	requireTools(t, "sh", "sed", "grep", "head", "rm", "python3")

	r := newAdvisorRun(t)

	program := `if claude_print 1 --debug-file "$OUT/run-1-debug.log"; then echo status=0; else echo "status=$?"; fi`
	got, err := r.run(t, program, []string{"FAKE_CLAUDE_ADVISOR=on"}, "claude_print", "claude_advisor", "claude_failure")
	require.NoError(t, err, got)
	assert.Equal(t, "  advisor  run 1: on, with the advisor model claude-opus-5-5\n"+
		"  FAIL     run 1 used the advisor tool; no recording run may use it\nstatus=0\nfailed=1\n", got)

	assert.Equal(t, []advisorRecord{{
		Run: 1, Advisor: "on", AdvisorModel: "claude-opus-5-5", Switch: "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1",
	}}, r.advisorRecords(t))
}

// TestNativeRecordClaudeAdvisorState proves the reading of one debug
// log: the advisor line reads on with its model, a log without it reads
// off, and a missing or empty log reads unknown. On and unknown print
// FAIL and count it, because only off proves that the run used no advisor.
func TestNativeRecordClaudeAdvisorState(t *testing.T) {
	requireTools(t, "sh", "sed", "grep", "head")

	const unknown = "  advisor  run 1: unknown\n" +
		"  FAIL     run 1 has no advisor state, because its debug log is missing or empty\nfailed=1\n"

	for _, tc := range []struct {
		name    string
		log     string
		missing bool // writes no debug log
		want    advisorRecord
		output  string
	}{
		{
			name: "the advisor line reads on with its model and fails",
			log:  "2026-11-03T12:22:29.000Z [DEBUG] startup\n" + advisorDebugLine + "\n",
			want: advisorRecord{Run: 1, Advisor: "on", AdvisorModel: "claude-opus-5-5"},
			output: "  advisor  run 1: on, with the advisor model claude-opus-5-5\n" +
				"  FAIL     run 1 used the advisor tool; no recording run may use it\nfailed=1\n",
		},
		{
			name:   "a log without the advisor line reads off",
			log:    "2026-11-03T12:22:29.000Z [DEBUG] startup\n",
			want:   advisorRecord{Run: 1, Advisor: "off"},
			output: "  advisor  run 1: off\nfailed=0\n",
		},
		{
			name:   "an empty log reads unknown and fails",
			want:   advisorRecord{Run: 1, Advisor: "unknown"},
			output: unknown,
		},
		{
			name:    "a missing log reads unknown and fails",
			missing: true,
			want:    advisorRecord{Run: 1, Advisor: "unknown"},
			output:  unknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAdvisorRun(t)

			if !tc.missing {
				require.NoError(t, os.WriteFile(filepath.Join(r.out, "run-1-debug.log"), []byte(tc.log), 0o600))
			}

			got, err := r.run(t, "claude_advisor 1", nil, "claude_advisor")
			require.NoError(t, err, got)
			assert.Equal(t, tc.output, got)

			tc.want.Switch = "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"
			assert.Equal(t, []advisorRecord{tc.want}, r.advisorRecords(t))
		})
	}
}

// TestNativeRecordClaudeRunsGoThroughClaudePrint proves that the script
// starts claude in print mode only inside claude_print, so no paid run
// misses the advisor switch. Comments and print lines (echo, or
// printf '%s\n') only name the command.
func TestNativeRecordClaudeRunsGoThroughClaudePrint(t *testing.T) {
	script := nativeRecordScript(t)
	body := scriptFunction(t, script, "claude_print")

	invocation := regexp.MustCompile(`(^|[\s(&;|])claude\s+(-p|--print)\b`)

	var outside []string

	for _, line := range strings.Split(strings.Replace(script, body, "", 1), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "echo ") ||
			strings.HasPrefix(trimmed, `printf '%s\n' `) {
			continue
		}

		if invocation.MatchString(line) {
			outside = append(outside, trimmed)
		}
	}

	assert.Empty(t, outside, "each print-mode claude run goes through claude_print")
	assert.Equal(t, 1, strings.Count(body, "claude -p"), "claude_print starts claude once")
	assert.Contains(t, body, `CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1 claude -p "$@"`)
	assert.GreaterOrEqual(t, strings.Count(script, `claude_print "$`), 2,
		"the timing and guard scenarios call claude_print")
}

// recordStep returns the Python program of the record.json step of the
// script, the heredoc that collects the transcripts and the extras.
func recordStep(t *testing.T) string {
	t.Helper()

	script := nativeRecordScript(t)

	start := strings.Index(script, "\nclient, version, repo, seamark, instr, observed, out, session = sys.argv[1:9]\n")
	require.GreaterOrEqual(t, start, 0, "the script has the record.json step")

	heredoc := strings.LastIndex(script[:start], "<<'PY'\n")
	require.GreaterOrEqual(t, heredoc, 0)

	end := strings.Index(script[start:], "\nPY\n")
	require.GreaterOrEqual(t, end, 0)

	return script[heredoc+len("<<'PY'\n") : start+end+1]
}

// TestNativeRecordListsAdvisorState runs the record.json step of
// the script. It proves that advisor.jsonl becomes an extra of the
// sanitized copy when a Claude run wrote it, and only then.
func TestNativeRecordListsAdvisorState(t *testing.T) {
	requireTools(t, "python3")

	step := recordStep(t)

	for _, written := range []bool{true, false} {
		root := t.TempDir()
		out := filepath.Join(root, "out")
		observed := filepath.Join(root, "observed")

		require.NoError(t, os.MkdirAll(out, 0o755))
		require.NoError(t, os.MkdirAll(observed, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(out, "runs.txt"), []byte("100\n"), 0o600))

		if written {
			require.NoError(t, os.WriteFile(filepath.Join(out, "advisor.jsonl"),
				[]byte(`{"run": 1, "advisor": "off"}`+"\n"), 0o600))
		}

		cmd := exec.Command("python3", "-I", "-", ClaudeID, "2.1.314 (Claude Code)", filepath.Join(root, "repo"),
			filepath.Join(root, "seamark"), filepath.Join(root, "instr"), observed, out, "")
		cmd.Stdin = strings.NewReader(step)
		cmd.Env = append(os.Environ(), "HOME="+root, "CLAUDE_CONFIG_DIR="+filepath.Join(root, ".claude"))

		got, err := cmd.CombinedOutput()
		require.NoError(t, err, string(got))

		data, err := os.ReadFile(filepath.Join(out, "record.json"))
		require.NoError(t, err)

		var record struct {
			Extras map[string]string `json:"extras"`
		}
		require.NoError(t, json.Unmarshal(data, &record))

		if written {
			assert.Equal(t, map[string]string{"advisor.jsonl": filepath.Join(out, "advisor.jsonl")}, record.Extras)
		} else {
			assert.Empty(t, record.Extras, "no advisor.jsonl, no extra")
		}
	}
}
