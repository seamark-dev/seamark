package integration

// Tests of the Codex trust keys of the guard scenario in
// scripts/capture-native.sh. Codex keys the trust of a project hook by
// its position (event, group, handler) and compares a hash of the entry
// content at that key. A moved entry reads as modified, and Codex does
// not run it without a new review. The tests run the setup and phase
// functions of the script on the hook document that init writes. Then
// they check the hook document of each Codex case against the enforce
// form that the operator reviews.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// codexGuardMarker is the marker of the owned Codex guard, as guard_vars
// sets it.
const codexGuardMarker = "lessons --guard --hook --client codex"

// The phases whose Codex cases run without the owned guard entry.
var codexPhasesWithoutGuard = []string{"wrapper", "slow", guardPhaseSlowDeny}

// trustEntry is one hook entry of a hook document: the fields of its group
// (for example the matcher) and the handler itself. Codex hashes both.
type trustEntry struct {
	Group   map[string]any
	Handler map[string]any
}

// hookForm holds the entries of one hook document by trust key, and the
// number of its PreToolUse groups.
type hookForm struct {
	entries map[string]trustEntry
	groups  int
}

// readHookForm reads a hook document and keys each entry by event, group
// index, and handler index, as Codex keys its trust.
func readHookForm(t *testing.T, path string) hookForm {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	form := hookForm{entries: map[string]trustEntry{}, groups: len(doc.Hooks["PreToolUse"])}

	for event, groups := range doc.Hooks {
		for g, group := range groups {
			fields := map[string]any{}

			for k, v := range group {
				if k != "hooks" {
					fields[k] = v
				}
			}

			handlers, _ := group["hooks"].([]any)
			for h, handler := range handlers {
				m, ok := handler.(map[string]any)
				require.True(t, ok, "%s group %d holds a handler object", event, g)

				form.entries[fmt.Sprintf("%s:%d:%d", event, g, h)] = trustEntry{Group: fields, Handler: m}
			}
		}
	}

	return form
}

// ownedGuardKeys returns the trust keys of the owned guard entries.
func (f hookForm) ownedGuardKeys() []string {
	var keys []string

	for key, e := range f.entries {
		command, _ := e.Handler["command"].(string)
		if strings.HasSuffix(command, " "+codexGuardMarker) {
			keys = append(keys, key)
		}
	}

	return keys
}

// keys returns the sorted trust keys of the form.
func (f hookForm) keys() []string {
	keys := make([]string, 0, len(f.entries))
	for key := range f.entries {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// codexGuardScratch is the scratch layout of one Codex guard setup.
type codexGuardScratch struct {
	root, instr, snap string
}

// newCodexGuardScratch installs the lesson hook entries of
// `init --client codex --lesson-checks enforce` in a new root. It also
// writes the lessons.yaml variants that guard_restore and guard_phase
// copy. Their content does not matter here.
func newCodexGuardScratch(t *testing.T) codexGuardScratch {
	t.Helper()

	requireTools(t, "sh", "python3", "cp")

	s := codexGuardScratch{root: t.TempDir(), snap: t.TempDir()}
	s.instr = filepath.Join(s.root, ".native-instrumentation")

	installLessonHooks(t, s.root, CodexID, testBinary, hooks.LessonChecksEnforce, "")

	require.NoError(t, os.MkdirAll(filepath.Join(s.instr, "observed"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, ".seamark"), 0o755))

	for _, name := range []string{"lessons-enforce.yaml", "lessons-check.yaml", "lessons-broken.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(s.instr, name), []byte("# "+name+"\n"), 0o644))
	}

	return s
}

// run runs program after the guard functions of the script, with the
// variables that guard_vars and record_mode set for Codex. guard_capability
// is a stand-in that keeps a copy of the hook document that seamark
// status would read. It returns the output and the exit status.
func (s codexGuardScratch) run(t *testing.T, program string) (output string, status int) {
	t.Helper()

	script := captureNativeScript(t)

	var functions strings.Builder

	for _, name := range []string{"guard_instrument", "guard_last", "guard_doc", "guard_keys", "guard_restore", "guard_phase"} {
		functions.WriteString(scriptFunction(t, script, name))
	}

	python, err := exec.LookPath("python3")
	require.NoError(t, err)

	stub := "guard_capability() { cp \"$GUARD_DOC\" \"$SNAP/capability-$1.json\"; }\n"
	cmd := exec.Command("sh", "-eu", "-c", stub+functions.String()+program)
	cmd.Env = []string{
		"PATH=" + filepath.Dir(python) + ":/usr/bin:/bin",
		"CLIENT=codex", "REPO=" + s.root, "INSTR=" + s.instr, "OBS=" + filepath.Join(s.instr, "observed"),
		"GUARD_DOC=" + filepath.Join(s.root, ".codex", "hooks.json"), "GUARD_MARKER=" + codexGuardMarker,
		"GUARD_MATCHER=apply_patch", "SNAP=" + s.snap, "BIN=/nonexistent/seamark",
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

// codexGuardCaseRuns returns the phase and the case of each guard_run
// call of record_codex_guard, in the order of the script.
func codexGuardCaseRuns(t *testing.T) [][2]string {
	t.Helper()

	var runs [][2]string

	for _, m := range guardRunLine.FindAllStringSubmatch(scriptFunction(t, captureNativeScript(t), "record_codex_guard"), -1) {
		runs = append(runs, [2]string{m[1], m[2]})
	}

	require.Len(t, runs, len(codexGuardCases)+len(codexGuardExtensions), "record_codex_guard runs every Codex case")

	return runs
}

// TestGuardScenarioCodexCasesKeepTheTrustKeys runs the Codex setup of the
// script, then the phase of each Codex case in the order of
// record_codex_guard. The hook document of each case must hold every
// entry at the trust key and with the content of the enforce form. The
// enforce form is the form that the operator reviews, so Codex then
// treats every entry of every case as trusted.
func TestGuardScenarioCodexCasesKeepTheTrustKeys(t *testing.T) {
	s := newCodexGuardScratch(t)
	runs := codexGuardCaseRuns(t)

	var program strings.Builder

	program.WriteString("guard_instrument\nguard_last\ncp \"$GUARD_DOC\" \"$INSTR/hooks-enforce.json\"\nguard_keys\n")

	for i, r := range runs {
		fmt.Fprintf(&program, "guard_phase %s\ncp \"$GUARD_DOC\" \"$SNAP/case-%02d.json\"\n", r[0], i)
	}

	out, status := s.run(t, program.String())
	require.Equal(t, 0, status, out)

	enforce := readHookForm(t, filepath.Join(s.instr, "hooks-enforce.json"))
	guardKey := fmt.Sprintf("PreToolUse:%d:0", enforce.groups-1)

	require.Equal(t, []string{guardKey}, enforce.ownedGuardKeys(), "the owned guard is the last PreToolUse entry")
	assert.NotContains(t, enforce.entries, fmt.Sprintf("PreToolUse:%d:1", enforce.groups-1),
		"the owned guard is alone in its group")

	enforceBytes, err := os.ReadFile(filepath.Join(s.instr, "hooks-enforce.json"))
	require.NoError(t, err)

	for i, r := range runs {
		phase, name := r[0], r[1]

		t.Run(fmt.Sprintf("%02d %s %s", i+1, phase, name), func(t *testing.T) {
			path := filepath.Join(s.snap, fmt.Sprintf("case-%02d.json", i))
			form := readHookForm(t, path)

			for key, e := range form.entries {
				want, ok := enforce.entries[key]
				if assert.True(t, ok, "%s is a trust key of the enforce form", key) {
					assert.Equal(t, want, e, "%s keeps the content of the enforce form", key)
				}
			}

			if slices.Contains(codexPhasesWithoutGuard, phase) {
				want := slices.DeleteFunc(enforce.keys(), func(k string) bool { return k == guardKey })
				assert.Equal(t, want, form.keys(), "only the owned guard entry is absent")

				return
			}

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(enforceBytes), string(data), "the case runs with the enforce form itself")
		})
	}

	t.Run("the inline wrapper changes only the command of the owned guard", func(t *testing.T) {
		// No Codex case runs with this form. Only seamark status reads it.
		inline := readHookForm(t, filepath.Join(s.snap, "capability-wrapper-inline.json"))
		require.Equal(t, enforce.keys(), inline.keys())

		for key, e := range inline.entries {
			if key != guardKey {
				assert.Equal(t, enforce.entries[key], e, key)
			}
		}

		assert.Equal(t, enforce.entries[guardKey].Handler["command"].(string)+" || true",
			inline.entries[guardKey].Handler["command"])
	})
}

// TestGuardScenarioCodexGuardMoveKeepsEveryEntry proves that guard_last
// changes only the position of the owned guard. Every entry keeps its
// group fields and its handler, and the capability report of seamark
// status stays the same, so the enforce cases measure the same guard.
func TestGuardScenarioCodexGuardMoveKeepsEveryEntry(t *testing.T) {
	s := newCodexGuardScratch(t)
	doc := filepath.Join(s.root, ".codex", "hooks.json")

	out, status := s.run(t, "guard_instrument\ncp \"$GUARD_DOC\" \"$SNAP/before.json\"\n")
	require.Equal(t, 0, status, out)

	report := func() string {
		data, err := json.Marshal(LessonCapability([]Inspection{inspectionOf(t, s.root, CodexID)}, s.root,
			BinaryIdentity{Path: testBinary}))
		require.NoError(t, err)

		return string(data)
	}

	before := report()

	out, status = s.run(t, "guard_last\n")
	require.Equal(t, 0, status, out)

	assert.Equal(t, before, report(), "the move keeps the capability report")

	values := func(f hookForm) []string {
		var all []string

		for _, e := range f.entries {
			data, err := json.Marshal(e)
			require.NoError(t, err)

			all = append(all, string(data))
		}

		slices.Sort(all)

		return all
	}

	old, moved := readHookForm(t, filepath.Join(s.snap, "before.json")), readHookForm(t, doc)
	assert.Equal(t, values(old), values(moved), "the move keeps every entry with its content")
	assert.Equal(t, []string{"PreToolUse:2:0"}, old.ownedGuardKeys(), "init writes the owned guard as the third PreToolUse group")
	assert.Equal(t, []string{fmt.Sprintf("PreToolUse:%d:0", moved.groups-1)}, moved.ownedGuardKeys())
}

// TestGuardScenarioCodexKeysRefuseTheOldLayout runs guard_keys on the
// layout of an older setup, with the owned guard before the observer and
// the indirection entry. Its no-guard form moves both entries, so the
// check fails and names the moved keys. guard_begin then starts no paid
// turn.
func TestGuardScenarioCodexKeysRefuseTheOldLayout(t *testing.T) {
	s := newCodexGuardScratch(t)

	out, status := s.run(t, "guard_instrument\ncp \"$GUARD_DOC\" \"$INSTR/hooks-enforce.json\"\nguard_keys\n")
	require.Equal(t, 1, status, out)

	assert.Contains(t, out, "the owned guard entry is at PreToolUse:2:0, not alone at PreToolUse:4:0")
	assert.Contains(t, out, "PreToolUse:2:0 holds other content than in the enforce form")
	assert.Contains(t, out, "PreToolUse:3:0 holds other content than in the enforce form")
}
