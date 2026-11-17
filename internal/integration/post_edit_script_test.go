package integration

// Tests of the post-edit scenario of scripts/native-record.sh. They read
// the script text, or run its functions on the hook documents that the
// lesson-check setup writes, or run the whole script with stand-ins for
// seamark and the client CLI. So they need no model, no client CLI, and
// no login, and no test reaches the trust step of a real client.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// The setup lines of post_setup that the form tests run: the post-edit
// indirection entry, and the move of the owned Codex verify entry.
const (
	postEditInstrument = `guard_instrument PostToolUse "sh $INSTR/post-edit.sh" "$POST_TIMEOUT"`
	postEditMove       = `[ "$CLIENT" != codex ] || owned_last "$GUARD_DOC" PostToolUse "$POST_MARKER"`
)

// scriptValue returns the number that a line NAME=<number> of the script
// sets.
func scriptValue(t *testing.T, script, name string) int {
	t.Helper()

	prefix := name + "="
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(scriptLine(t, script, prefix), prefix)))
	require.NoError(t, err, prefix)

	return n
}

// heredocFunction returns the definition of a shell function that ends
// with a Python heredoc. The Python text can hold a closing brace at the
// start of a line, so the function ends at the heredoc end and the brace
// after it.
func heredocFunction(t *testing.T, script, name string) string {
	t.Helper()

	marker := "\n" + name + "() {\n"

	start := strings.Index(script, marker)
	require.GreaterOrEqual(t, start, 0, "the script defines %s", name)

	end := strings.Index(script[start:], "\nPY\n}\n")
	require.GreaterOrEqual(t, end, 0, "%s ends with a Python heredoc", name)

	return script[start+1 : start+end+len("\nPY\n}\n")]
}

// TestPostEditScenarioScriptMatchesTheTable proves that the script runs
// the post-edit cases of each client in the order and the phase of the
// replay table.
func TestPostEditScenarioScriptMatchesTheTable(t *testing.T) {
	script := nativeRecordScript(t)

	for _, tc := range []struct{ client, function string }{
		{ClaudeID, "record_claude_post_edit"},
		{CodexID, "record_codex_post_edit"},
	} {
		t.Run(tc.client, func(t *testing.T) {
			var got, want [][2]string

			for _, m := range guardRunLine.FindAllStringSubmatch(scriptFunction(t, script, tc.function), -1) {
				got = append(got, [2]string{m[1], m[2]})
			}

			for _, x := range postEditCases(tc.client) {
				want = append(want, [2]string{x.phase, x.name})
			}

			assert.Equal(t, want, got)
		})
	}
}

// TestPostEditScenarioWaitsAroundTheTimeout proves that the indirection
// entry gets the timeout of the owned verify entry of both clients, that
// the slow-post wait ends inside it, and that the post-timeout wait ends
// after it. The script and setup also name the same verify markers.
func TestPostEditScenarioWaitsAroundTheTimeout(t *testing.T) {
	script := nativeRecordScript(t)
	timeout := scriptValue(t, script, "POST_TIMEOUT")

	for _, specs := range [][]hooks.Spec{
		hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksCheck), hooks.CodexLessonCheckSpecs(hooks.LessonChecksCheck),
	} {
		i := slices.IndexFunc(specs, func(s hooks.Spec) bool { return s.Event == "PostToolUse" })
		require.GreaterOrEqual(t, i, 0, "the check mode installs a post-edit hook")
		assert.Equal(t, specs[i].Timeout, timeout, "the indirection entry gets the timeout of %s", specs[i].Marker)
	}

	assert.Equal(t, 12, scriptValue(t, script, "POST_IN_BUDGET_SECONDS"))
	assert.Equal(t, 20, scriptValue(t, script, "POST_SLOW_SECONDS"))
	assert.Less(t, scriptValue(t, script, "POST_IN_BUDGET_SECONDS"), timeout, "the in-budget wait ends before the timeout")
	assert.Greater(t, scriptValue(t, script, "POST_SLOW_SECONDS"), timeout, "the slow wait ends after the timeout")

	setup := scriptFunction(t, script, "post_setup")
	assert.Contains(t, setup, "\n    "+postEditInstrument+"\n")
	assert.Contains(t, setup, "\n    "+postEditMove+"\n")

	vars := scriptFunction(t, script, "post_vars")
	assert.Contains(t, vars, `POST_MARKER="`+hooks.LessonVerifyMarker+`"`)
	assert.Contains(t, vars, `POST_MARKER="`+hooks.CodexLessonVerifyMarker+`"`)
}

// TestPostEditScenarioIndirectionModes runs the indirection entry that
// post_edit_script writes, with no wait and a stand-in verify command
// that prints its arguments and its stdin and exits with VERIFY_STATUS.
// The off mode runs nothing. Each slow mode passes the payload and the
// reply through, exits with the status of the command, and logs a start
// line and a status line.
func TestPostEditScenarioIndirectionModes(t *testing.T) {
	requireTools(t, "sh", "cat", "sleep")

	instr := t.TempDir()
	verify := filepath.Join(t.TempDir(), "verify")
	require.NoError(t, os.WriteFile(verify, []byte("#!/bin/sh\nprintf 'args=%s\\n' \"$*\"\nprintf 'stdin=%s\\n' \"$(cat)\"\n"+
		"exit \"${VERIFY_STATUS:-0}\"\n"), 0o755))

	program := scriptFunction(t, nativeRecordScript(t), "post_edit_script") + "post_edit_script\n"
	setup := exec.Command("sh", "-eu", "-c", program)
	setup.Env = []string{"PATH=/usr/bin:/bin", "INSTR=" + instr, "BIN=" + verify, "POST_MARKER=" + hooks.CodexLessonVerifyMarker,
		"POST_IN_BUDGET_SECONDS=0", "POST_SLOW_SECONDS=0"}
	got, err := setup.CombinedOutput()
	require.NoError(t, err, string(got))

	mode, err := os.ReadFile(filepath.Join(instr, "post-edit-mode"))
	require.NoError(t, err)
	assert.Equal(t, "off\n", string(mode), "setup starts with the entry off")

	reply := "args=" + hooks.CodexLessonVerifyMarker + "\nstdin={\"tool_name\":\"apply_patch\"}\n"

	for _, tc := range []struct {
		mode, status, stdout, log string
	}{
		{"off", "0", "", ""},
		{postEditPhaseSlow, "0", reply, postEditSlowStart + "\n" + postEditSlowDone + "\n"},
		{postEditPhaseTimeout, "1", reply, postEditTimeoutStart + "\npost-timeout 1\n"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_ = os.Remove(filepath.Join(instr, "post-edit.log"))
			require.NoError(t, os.WriteFile(filepath.Join(instr, "post-edit-mode"), []byte(tc.mode+"\n"), 0o600))

			run := exec.Command("sh", filepath.Join(instr, "post-edit.sh"))
			run.Env = []string{"PATH=/usr/bin:/bin", "VERIFY_STATUS=" + tc.status}
			run.Stdin = strings.NewReader(`{"tool_name":"apply_patch"}`)

			out, err := run.Output()

			status := "0"

			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status = strconv.Itoa(exit.ExitCode())
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tc.status, status, "the entry exits with the status of the verify command")
			assert.Equal(t, tc.stdout, string(out), "the payload and the reply pass through")

			log, err := os.ReadFile(filepath.Join(instr, "post-edit.log"))
			if tc.log == "" {
				assert.ErrorIs(t, err, os.ErrNotExist, "the off mode logs nothing")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.log, string(log))
		})
	}
}

// postEditScratch is the scratch layout of one post-edit setup: the
// hook documents of `init --lesson-checks check` in root, the
// instrumentation directory, and a directory for the snapshots.
type postEditScratch struct {
	client, root, instr, snap, doc string
}

// newPostEditScratch installs the lesson hook entries of
// `init --client <client> --lesson-checks check` in a new root, and the
// lessons.yaml variant that post_restore copies.
func newPostEditScratch(t *testing.T, client string) postEditScratch {
	t.Helper()

	requireTools(t, "sh", "python3", "cp")

	s := postEditScratch{client: client, root: t.TempDir(), snap: t.TempDir()}
	s.instr = filepath.Join(s.root, ".native-instrumentation")
	s.doc = filepath.Join(s.root, ".claude", "settings.json")

	if client == CodexID {
		s.doc = filepath.Join(s.root, ".codex", "hooks.json")
	}

	installLessonHooks(t, s.root, client, testBinary, hooks.LessonChecksCheck, "")

	require.NoError(t, os.MkdirAll(filepath.Join(s.instr, "observed"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.instr, "lessons-check.yaml"), []byte("# check\n"), 0o644))

	return s
}

// run runs program after the form and phase functions of the script,
// with the variables that post_vars and record_mode set. guard_capability
// is a stand-in that keeps a copy of the hook document that seamark
// status would read. It returns the output and the exit status.
func (s postEditScratch) run(t *testing.T, program string) (output string, status int) {
	t.Helper()

	script := nativeRecordScript(t)

	var functions strings.Builder

	for _, name := range []string{
		"guard_instrument", "owned_last", "owned_doc", "owned_keys", "post_doc", "post_keys", "post_restore", "post_phase",
	} {
		functions.WriteString(scriptFunction(t, script, name))
	}

	python, err := exec.LookPath("python3")
	require.NoError(t, err)

	matcher, marker := "Edit|Write|MultiEdit", hooks.LessonVerifyMarker
	if s.client == CodexID {
		matcher, marker = "apply_patch", hooks.CodexLessonVerifyMarker
	}

	stub := "guard_capability() { cp \"$GUARD_DOC\" \"$SNAP/capability-$1.json\"; }\n"
	cmd := exec.Command("sh", "-eu", "-c", stub+functions.String()+program)
	cmd.Env = []string{
		"PATH=" + filepath.Dir(python) + ":/usr/bin:/bin",
		"CLIENT=" + s.client, "REPO=" + s.root, "INSTR=" + s.instr, "OBS=" + filepath.Join(s.instr, "observed"),
		"GUARD_DOC=" + s.doc, "GUARD_MATCHER=" + matcher, "POST_MARKER=" + marker, "SNAP=" + s.snap,
		"POST_TIMEOUT=" + strconv.Itoa(scriptValue(t, script, "POST_TIMEOUT")),
	}

	out, err := cmd.CombinedOutput()
	output = string(out)

	var exit *exec.ExitError
	if err != nil {
		require.ErrorAs(t, err, &exit, output)

		return output, exit.ExitCode()
	}

	return output, 0
}

// setUp is the program of the form setup in post_setup: the indirection
// entry, the move of the owned Codex verify entry, and the kept copy.
func (s postEditScratch) setUp() string {
	return postEditInstrument + "\n" + postEditMove + "\ncp \"$GUARD_DOC\" \"$INSTR/hooks-check.json\"\n"
}

// postToolUseGroups returns the PostToolUse groups of a hook document.
func postToolUseGroups(t *testing.T, path string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	return doc.Hooks["PostToolUse"]
}

// entriesEnding returns the trust keys of the entries of a form whose
// command ends with the marker.
func entriesEnding(f hookForm, marker string) []string {
	var keys []string

	for key, e := range f.entries {
		command, _ := e.Handler["command"].(string)
		if strings.HasSuffix(command, " "+marker) {
			keys = append(keys, key)
		}
	}

	slices.Sort(keys)

	return keys
}

// TestPostEditScenarioCodexFormsKeepTheTrustKeys runs the Codex form
// setup of post_setup, then the phase of each Codex case in the order of
// the table. The check form holds the owned verify entry alone in the
// last PostToolUse group and the indirection entry with the timeout of
// the verify entry. Each case runs with every entry at the trust key and
// with the content of the check form, so one review of the check form
// covers every case.
func TestPostEditScenarioCodexFormsKeepTheTrustKeys(t *testing.T) {
	s := newPostEditScratch(t, CodexID)

	var program strings.Builder

	program.WriteString(s.setUp() + "post_keys\n")

	for i, x := range codexPostEditCases {
		fmt.Fprintf(&program, "post_phase %s\ncp \"$GUARD_DOC\" \"$SNAP/case-%02d.json\"\n"+
			"cp \"$INSTR/post-edit-mode\" \"$SNAP/mode-%02d.txt\"\n", x.phase, i, i)
	}

	out, status := s.run(t, program.String())
	require.Equal(t, 0, status, out)

	checkPath := filepath.Join(s.instr, "hooks-check.json")
	check := readHookForm(t, checkPath)
	groups := postToolUseGroups(t, checkPath)
	verifyKey := fmt.Sprintf("PostToolUse:%d:0", len(groups)-1)

	require.Equal(t, []string{verifyKey}, entriesEnding(check, hooks.CodexLessonVerifyMarker),
		"the owned verify entry is the last PostToolUse entry")
	assert.NotContains(t, check.entries, fmt.Sprintf("PostToolUse:%d:1", len(groups)-1), "the verify entry is alone in its group")
	assert.Len(t, entriesEnding(check, hooks.CodexLessonBaselineMarker), 1, "the baseline entry stays")

	indirection := fmt.Sprintf("PostToolUse:%d:0", len(groups)-2)
	if assert.Contains(t, check.entries, indirection) {
		e := check.entries[indirection]
		assert.Equal(t, "sh "+s.instr+"/post-edit.sh", e.Handler["command"], "the indirection entry comes before the verify entry")
		assert.InDelta(t, 15, e.Handler["timeout"], 0, "the indirection entry has the timeout of the verify entry")
		assert.Equal(t, "apply_patch", e.Group["matcher"])
	}

	checkBytes, err := os.ReadFile(checkPath)
	require.NoError(t, err)

	for i, x := range codexPostEditCases {
		t.Run(fmt.Sprintf("%02d %s %s", i+1, x.phase, x.name), func(t *testing.T) {
			path := filepath.Join(s.snap, fmt.Sprintf("case-%02d.json", i))
			form := readHookForm(t, path)

			for key, e := range form.entries {
				want, ok := check.entries[key]
				if assert.True(t, ok, "%s is a trust key of the check form", key) {
					assert.Equal(t, want, e, "%s keeps the content of the check form", key)
				}
			}

			mode, err := os.ReadFile(filepath.Join(s.snap, fmt.Sprintf("mode-%02d.txt", i)))
			require.NoError(t, err)

			if x.phase == postEditPhaseCheck {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, string(checkBytes), string(data), "the case runs with the check form itself")
				assert.Equal(t, "off\n", string(mode))

				return
			}

			want := slices.DeleteFunc(check.keys(), func(k string) bool { return k == verifyKey })
			assert.Equal(t, want, form.keys(), "only the owned verify entry is absent")
			assert.Equal(t, x.phase+"\n", string(mode), "the phase sets the mode of the indirection entry")
		})
	}

	for _, phase := range []string{postEditPhaseCheck, postEditPhaseSlow, postEditPhaseTimeout} {
		assert.FileExists(t, filepath.Join(s.snap, "capability-"+phase+".json"), "each phase appends its capability report")
	}
}

// TestPostEditScenarioClaudeFormDropsOnlyTheVerifyEntry proves that the
// Claude Code no-verify form loses only the owned verify entry of
// PostToolUse. The verify entry of PostToolUseFailure and the baseline
// stay. Claude Code keys no trust by position, so the test compares the
// entries by content.
func TestPostEditScenarioClaudeFormDropsOnlyTheVerifyEntry(t *testing.T) {
	s := newPostEditScratch(t, ClaudeID)

	out, status := s.run(t, s.setUp()+"post_doc no-verify \"$SNAP/no-verify.json\"\n")
	require.Equal(t, 0, status, out)

	values := func(path string) []string {
		var all []string

		for key, e := range readHookForm(t, path).entries {
			event, _, _ := strings.Cut(key, ":")
			data, err := json.Marshal(e)
			require.NoError(t, err)

			all = append(all, event+" "+string(data))
		}

		slices.Sort(all)

		return all
	}

	check := values(filepath.Join(s.instr, "hooks-check.json"))
	form := values(filepath.Join(s.snap, "no-verify.json"))

	var dropped []string

	for _, v := range check {
		if i := slices.Index(form, v); i >= 0 {
			form = slices.Delete(form, i, i+1)
		} else {
			dropped = append(dropped, v)
		}
	}

	assert.Empty(t, form, "the no-verify form adds no entry")
	require.Len(t, dropped, 1, "the no-verify form drops one entry")
	assert.True(t, strings.HasPrefix(dropped[0], "PostToolUse "), dropped[0])
	assert.Contains(t, dropped[0], " "+hooks.LessonVerifyMarker+`"`)
	assert.Len(t, entriesEnding(readHookForm(t, filepath.Join(s.snap, "no-verify.json")), hooks.LessonVerifyMarker), 1,
		"the verify entry of PostToolUseFailure stays")
}

// TestPostEditScenarioCodexKeysRefuseALayoutWithoutTheMove runs post_keys
// on a check form whose owned verify entry stays in the first PostToolUse
// group. Its no-verify form moves the observer and the indirection entry,
// so the check fails and names the moved keys.
func TestPostEditScenarioCodexKeysRefuseALayoutWithoutTheMove(t *testing.T) {
	s := newPostEditScratch(t, CodexID)

	out, status := s.run(t, postEditInstrument+"\ncp \"$GUARD_DOC\" \"$INSTR/hooks-check.json\"\npost_keys\n")
	require.Equal(t, 1, status, out)

	assert.Contains(t, out, "the owned verify entry is at PostToolUse:0:0, not alone at PostToolUse:2:0")
	assert.Contains(t, out, "PostToolUse:0:0 holds other content than in the check form")
	assert.Contains(t, out, "PostToolUse:2:0 of the check form is missing")
}

// TestPostEditScenarioPhaseRefusesAnUnknownPhase proves that post_phase
// stops at a phase name that the scenario does not know, before it
// changes the hook document.
func TestPostEditScenarioPhaseRefusesAnUnknownPhase(t *testing.T) {
	s := newPostEditScratch(t, CodexID)

	out, status := s.run(t, s.setUp()+"post_phase slow\n")
	assert.Equal(t, 2, status, out)
	assert.Contains(t, out, `post_phase: unknown phase "slow"`)
	assert.NoFileExists(t, filepath.Join(s.snap, "capability-slow.json"))
}

// TestPostEditScenarioLimitStopRestoresTheCheckSetup runs limit_stop in
// each slow phase, on the form functions of the script. The stop leaves
// the hook document that setup kept, the indirection entry off, and the
// check lessons.yaml. So a reused repository keeps the form that the
// operator trusted, and no later run starts.
func TestPostEditScenarioLimitStopRestoresTheCheckSetup(t *testing.T) {
	script := nativeRecordScript(t)

	var stop strings.Builder

	stop.WriteString("FAILED=0\nOK=0\nBLOCKED=0\nMODE=record\nSCENARIO=post-edit\n")

	for _, prefix := range []string{"ok()", "fail()", "blocked()", "note()"} {
		stop.WriteString(scriptLine(t, script, prefix))
	}

	stop.WriteString(scriptFunction(t, script, "summary") + scriptFunction(t, script, "limit_stop"))

	for _, client := range []string{ClaudeID, CodexID} {
		for _, phase := range []string{postEditPhaseSlow, postEditPhaseTimeout} {
			t.Run(client+" "+phase, func(t *testing.T) {
				s := newPostEditScratch(t, client)

				program := s.setUp() + "post_phase " + phase + "\n" +
					"cp \"$GUARD_DOC\" \"$SNAP/phase.json\"\ncp \"$INSTR/post-edit-mode\" \"$SNAP/phase-mode.txt\"\n" +
					"printf '# changed\\n' > \"$REPO/.seamark/lessons.yaml\"\n" + stop.String() +
					"fail 'run 3 (case) stopped at a usage or account limit'\nlimit_stop\necho 'next run starts'\n"

				out, status := s.run(t, program)
				assert.Equal(t, 1, status, out)
				assert.Contains(t, out, "no later run starts")
				assert.Contains(t, out, "result: FAIL")
				assert.NotContains(t, out, "next run starts")

				read := func(path string) string {
					data, err := os.ReadFile(path)
					require.NoError(t, err)

					return string(data)
				}

				check := read(filepath.Join(s.instr, "hooks-check.json"))
				require.NotEqual(t, check, read(filepath.Join(s.snap, "phase.json")), "the phase changed the hook document")
				require.Equal(t, phase+"\n", read(filepath.Join(s.snap, "phase-mode.txt")))

				assert.Equal(t, check, read(s.doc), "the stop restores the hook document that setup kept")
				assert.Equal(t, "off\n", read(filepath.Join(s.instr, "post-edit-mode")), "the stop sets the indirection entry off")
				assert.Equal(t, "# check\n", read(filepath.Join(s.root, ".seamark", "lessons.yaml")),
					"the stop restores the check lessons.yaml")
			})
		}
	}
}

// TestPostEditScenarioCaseLineHoldsThePostEditLines runs guard_run with
// stand-ins for the phase, the fixture, and the client run. The stand-in
// client run logs the lines of a slow hook. The post-edit case line holds
// only the lines of its own run, and a guard case line holds no
// post_edit key, so it keeps the form of the reviewed guard copies.
func TestPostEditScenarioCaseLineHoldsThePostEditLines(t *testing.T) {
	requireTools(t, "sh", "python3", "wc", "tr")

	script := nativeRecordScript(t)
	python, err := exec.LookPath("python3")
	require.NoError(t, err)

	for _, tc := range []struct {
		scenario string
		want     []string
	}{
		{postEditScenario, []string{postEditSlowStart, postEditSlowDone}},
		{"guard", nil},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			out, instr := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(out, "runs.txt"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(instr, "post-edit.log"), []byte(postEditTimeoutStart+"\n"), 0o600))

			stubs := "post_phase() { GUARD_PHASE=$1; }\nguard_phase() { GUARD_PHASE=$1; }\n" +
				"guard_reset() { :; }\nguard_fixture() { :; }\n" +
				"guard_state() { printf '{\"files\": {}, \"text\": {}, \"status\": []}' > \"$1\"; }\n" +
				"guard_claude() { printf '%s\\n' '" + postEditSlowStart + "' '" + postEditSlowDone + "' >> \"$INSTR/post-edit.log\"; " +
				"printf 'frozen-sentinel' > \"$OUT/reply-$RUN.txt\"; }\n"
			program := stubs + scriptFunction(t, script, "guard_lines") + heredocFunction(t, script, "guard_run") +
				"guard_run slow-post slow-post-write \"the prompt\"\n"

			cmd := exec.Command("sh", "-eu", "-c", program)
			cmd.Env = []string{"PATH=" + filepath.Dir(python) + ":/usr/bin:/bin", "CLIENT=claude", "SCENARIO=" + tc.scenario,
				"OUT=" + out, "INSTR=" + instr, "REPO=" + t.TempDir(), "AUDIT_LINES0=0"}
			got, err := cmd.CombinedOutput()
			require.NoError(t, err, string(got))

			lines := timingLines(t, filepath.Join(out, guardCasesFile))
			require.Len(t, lines, 1)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(lines[0], &raw))

			var c guardCase
			require.NoError(t, json.Unmarshal(lines[0], &c))

			assert.Equal(t, postEditPhaseSlow, c.Phase)
			assert.Equal(t, "frozen-sentinel", c.Reply)
			assert.Empty(t, c.PreWrite)

			if tc.want == nil {
				assert.NotContains(t, raw, "post_edit", "a guard case line has no post_edit key")

				return
			}

			assert.Equal(t, tc.want, c.PostEdit, "the case line holds only the lines of its own run")
		})
	}
}

// dryRun is the scratch layout of one run of the whole script with
// stand-ins: a seamark that answers version, init, index, and status,
// and a client CLI that answers only --version. Each stand-in logs its
// arguments, so a test can prove that no client call but --version
// happened.
type dryRun struct {
	dir, bin, out, calls string
}

// newDryRun writes the stand-ins. The stand-in init copies the hook
// document that the lesson-check setup of the client writes in check
// mode. The stand-in index writes the repository root into an index
// database, as guard_identities reads it.
func newDryRun(t *testing.T, client string) dryRun {
	t.Helper()

	requireTools(t, "sh", "git", "python3", "cp")

	d := dryRun{dir: t.TempDir()}
	d.bin = filepath.Join(d.dir, "bin", "seamark")
	d.out = filepath.Join(d.dir, "out")
	d.calls = filepath.Join(d.dir, "calls")

	template := filepath.Join(d.dir, "template")
	installLessonHooks(t, template, client, testBinary, hooks.LessonChecksCheck, "")

	doc := ".claude/settings.json"
	if client == CodexID {
		doc = ".codex/hooks.json"
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(d.bin), 0o755))
	require.NoError(t, os.MkdirAll(d.calls, 0o755))

	seamark := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + d.calls + "/seamark.txt'\n" +
		"[ \"$1\" != version ] || { echo 'seamark v0.0.0-standin'; exit 0; }\n" +
		"[ \"$1\" = -C ] || exit 1\n" +
		"case \"$3 $7\" in\n" +
		"    'init check') mkdir -p \"$2/$(dirname '" + doc + "')\" && cp '" + filepath.Join(template, doc) + "' \"$2/" + doc + "\" ;;\n" +
		"    'index '*) python3 -I -c 'import sqlite3, sys; db = sqlite3.connect(sys.argv[1]); " +
		"db.execute(\"CREATE TABLE meta (key TEXT, value TEXT)\"); " +
		"db.execute(\"INSERT INTO meta VALUES (?, ?)\", (\"repo_root\", sys.argv[2])); db.commit()' " +
		"\"$2/.seamark/index.db\" \"$2\" ;;\n" +
		"    'status '*) echo '{}' ;;\n" +
		"    *) exit 1 ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(d.bin, []byte(seamark), 0o755))

	cli := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + d.calls + "/client.txt'\n" +
		"[ \"$1\" = --version ] || exit 1\necho '" + client + " 0.0.0-standin'\n"
	require.NoError(t, os.WriteFile(filepath.Join(d.dir, "bin", client), []byte(cli), 0o755))

	return d
}

// run runs the record mode of the script for the client with the
// stand-ins first on PATH, a scratch home, and the extra environment. It
// returns the output and the exit status.
func (d dryRun) run(t *testing.T, client string, env ...string) (output string, status int) {
	t.Helper()

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "native-record.sh"))
	require.NoError(t, err)

	python, err := exec.LookPath("python3")
	require.NoError(t, err)

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	// The sanitizer refuses a copy that holds the base name of the home
	// directory, so the name must not occur in a manifest.
	home := filepath.Join(d.dir, "qoperator")
	require.NoError(t, os.MkdirAll(home, 0o755))

	cmd := exec.Command("sh", script, "record", client, d.bin)
	cmd.Env = append([]string{
		"PATH=" + strings.Join([]string{filepath.Dir(d.bin), filepath.Dir(python), filepath.Dir(gitPath), "/usr/bin", "/bin"}, ":"),
		"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		"NATIVE_RECORD_OUT=" + d.out, "SEAMARK_ALLOW_DIRTY=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	}, env...)

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	if err != nil {
		require.ErrorAs(t, err, &exit, string(out))

		return string(out), exit.ExitCode()
	}

	return string(out), 0
}

// lines returns the logged calls of one stand-in, or none.
func (d dryRun) lines(t *testing.T, name string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(d.calls, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// assertOnlyVersionCalls checks that the client stand-in got --version
// calls and no other call.
func (d dryRun) assertOnlyVersionCalls(t *testing.T) {
	t.Helper()

	calls := d.lines(t, "client.txt")
	require.NotEmpty(t, calls, "the script reads the client version")

	for _, c := range calls {
		assert.Equal(t, "--version", c, "no client call but --version")
	}
}

// TestPostEditScenarioDryRunStopsAtTheCodexTrustStep runs the whole
// script for Codex with NATIVE_RECORD_RUNS=0 and stand-ins. Setup builds
// the scratch repository, its key check passes, and the call stops at
// the trust step before any paid run, with the next command for the
// operator.
func TestPostEditScenarioDryRunStopsAtTheCodexTrustStep(t *testing.T) {
	d := newDryRun(t, CodexID)

	out, status := d.run(t, CodexID, "NATIVE_RECORD_SCENARIO=post-edit", "NATIVE_RECORD_RUNS=0")
	require.Equal(t, 3, status, out)

	repo := filepath.Join(d.out, "repo")
	physical, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)

	assert.Contains(t, out, "  ok       every Codex hook form keeps the trust keys and the content of the check form\n")
	assert.Contains(t, out, "  blocked  no paid run: the new Codex hooks need your trust review first\n")
	assert.Contains(t, out, "NATIVE_RECORD_REPO="+physical+" make native-record CLIENT=codex NATIVE_RECORD_SCENARIO=post-edit\n")
	assert.NotContains(t, out, "paid runs (")
	assert.NoFileExists(t, filepath.Join(d.out, "runs.txt"), "the call stops before the run list")

	d.assertOnlyVersionCalls(t)

	var subcommands []string

	for _, c := range d.lines(t, "seamark.txt") {
		fields := strings.Fields(c)
		if fields[0] == "-C" {
			fields = fields[2:]
		}

		subcommands = append(subcommands, strings.Join(fields, " "))
	}

	assert.Equal(t, []string{"version", "init --client codex --lesson-checks check", "index"}, subcommands)

	instr := filepath.Join(physical, ".native-instrumentation")
	for _, f := range []string{"scenario-post-edit", "hooks-check.json", "post-edit.sh", "lessons-check.yaml"} {
		assert.FileExists(t, filepath.Join(instr, f))
	}

	kept, err := os.ReadFile(filepath.Join(instr, "hooks-check.json"))
	require.NoError(t, err)
	doc, err := os.ReadFile(filepath.Join(physical, ".codex", "hooks.json"))
	require.NoError(t, err)
	assert.Equal(t, string(kept), string(doc), "the repository holds the check form that the operator reviews")

	groups := postToolUseGroups(t, filepath.Join(physical, ".codex", "hooks.json"))
	require.NotEmpty(t, groups)
	assert.Contains(t, fmt.Sprint(groups[len(groups)-1]), hooks.CodexLessonVerifyMarker, "the verify entry is last")

	lessons, err := os.ReadFile(filepath.Join(physical, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(lessons), "mode: check")
	assert.NotContains(t, string(lessons), "mode: enforce")
}

// TestPostEditScenarioDryRunCollectsNothingForClaude runs the whole
// script for Claude Code with NATIVE_RECORD_RUNS=0 and stand-ins. Setup
// builds the scratch repository, no paid run starts, and the collection
// writes an empty sanitized copy. The call ends blocked, because no hook
// payload exists.
func TestPostEditScenarioDryRunCollectsNothingForClaude(t *testing.T) {
	d := newDryRun(t, ClaudeID)

	out, status := d.run(t, ClaudeID, "NATIVE_RECORD_SCENARIO=post-edit", "NATIVE_RECORD_RUNS=0")
	require.Equal(t, 3, status, out)

	assert.Contains(t, out, "  note     NATIVE_RECORD_RUNS=0: no paid run; collecting what the repository already holds\n")
	assert.Contains(t, out, "  ok       sanitized copy in ")
	assert.Contains(t, out, "  blocked  no claude hook ran in ")
	assert.NotContains(t, out, "FAIL")

	d.assertOnlyVersionCalls(t)

	manifest, err := os.ReadFile(filepath.Join(d.out, "sanitized", "manifest.json"))
	require.NoError(t, err)

	var m nativeManifest
	require.NoError(t, json.Unmarshal(manifest, &m))
	assert.Equal(t, ClaudeID, m.Client)
	assert.Empty(t, m.Hooks)
	assert.Subset(t, m.Extras, []string{guardCasesFile, guardCapabilityFile, guardContextFile, guardHooksFile,
		guardAuditFile, guardIdentityFile}, "the copy holds the files of a guard copy")
}

// TestNativeRecordRefusesAnUnknownScenario proves that an unknown
// scenario name stops the script before any setup, with the list of the
// known names.
func TestNativeRecordRefusesAnUnknownScenario(t *testing.T) {
	d := newDryRun(t, CodexID)

	out, status := d.run(t, CodexID, "NATIVE_RECORD_SCENARIO=post-edits")
	require.Equal(t, 2, status, out)

	assert.Contains(t, out, `unknown scenario "post-edits" (known: timing, guard, post-edit)`)
	assert.Empty(t, d.lines(t, "seamark.txt"), "no setup ran")
	assert.Empty(t, d.lines(t, "client.txt"), "no client call ran")
	assert.NoDirExists(t, d.out)
}
