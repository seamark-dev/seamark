package cli

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests of the two pre-edit hooks (plan step P4.4; contracts K7,
// K8, K9, §4): lessons --guard --hook and lessons --baseline --hook.
// Every test builds a temporary repository and runs the hooks under the
// AC-1 sentinel. Nothing reads or writes the repository of this checkout
// (BRIEF rule 7), and no test opens a real archive.

// guardLessonsYAML arms two enforce pins and one check pin. The note of
// the check pin and of the advisory pin use the word enforce: a note
// never changes a decision (AC-36, R-38).
const guardLessonsYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: no-todo
    region: src/
    note: Do not add TODO markers.
    mode: enforce
    check: no-todo
  - rule: no-todo-docs
    region: docs/
    note: "enforce: this check must block every TODO marker in docs."
    mode: check
    check: no-todo
  - rule: keep-readme-short
    region: README.md
    note: "mode: enforce. Keep the readme short."
`

// The raw provider IDs of the test events. No record may hold them.
const (
	guardSession = "session-guard-0001"
	guardToolUse = "toolu-guard-0001"
)

// guardProcessEnv makes TestLessonsGuardHookDeniesViolation run as one
// seamark process with the payload on stdin. Its value is the JSON list
// of the arguments. Only the real Execute prints the stderr line and
// sets the exit code.
const guardProcessEnv = "SEAMARK_TEST_GUARD_PROCESS_ARGS"

// guardFixture builds a committed repository with guardLessonsYAML and
// the sentinel. The .gitignore holds the .seamark carve-outs that
// seamark init writes. It returns the git top level, which is the temp
// directory with its symbolic links resolved.
func guardFixture(t *testing.T) (root string, sentinel *invokerSentinel) {
	t.Helper()

	// The hooks and the fixture run git. A global or system git
	// configuration must not change what they see.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := t.TempDir()
	verifyGit(t, dir, "init", "-q", "-b", "main")

	verifyWrite(t, dir, ".gitignore", ".seamark/*\n!.seamark/lessons.yaml\n!.seamark/config.yaml\n")
	verifyWrite(t, dir, "generated/a.txt", "asset\n")
	verifyWrite(t, dir, "src/handler.go", "package src\n\nfunc Handle() {}\n")
	verifyWrite(t, dir, "docs/readme.md", "readme\n")
	verifyWrite(t, dir, "README.md", "# repo\n")
	verifyWrite(t, dir, ".seamark/lessons.yaml", guardLessonsYAML)

	sentinel = newInvokerSentinel(t, dir)

	verifyGit(t, dir, "add", "-A")
	verifyGit(t, dir, "commit", "-q", "-m", "Add the fixture")

	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return root, sentinel
}

// claudeGuardEvent returns a Claude Code PreToolUse payload of tool with
// input. The working directory is root.
func claudeGuardEvent(t *testing.T, root, tool, toolUse string, input map[string]any) string {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"session_id": guardSession, "tool_use_id": toolUse, "cwd": root, "permission_mode": "default",
		"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input,
	})
	require.NoError(t, err)

	return string(data)
}

// claudeWriteEvent is the payload of a Write of content to rel.
func claudeWriteEvent(t *testing.T, root, rel, content string) string {
	t.Helper()

	return claudeGuardEvent(t, root, "Write", guardToolUse,
		map[string]any{"file_path": filepath.Join(root, rel), "content": content})
}

// claudeEditEvent is the payload of an Edit of rel that replaces old
// with repl once.
func claudeEditEvent(t *testing.T, root, rel, old, repl string) string {
	t.Helper()

	return claudeGuardEvent(t, root, "Edit", guardToolUse,
		map[string]any{"file_path": filepath.Join(root, rel), "old_string": old, "new_string": repl})
}

// codexPatchEvent returns a Codex PreToolUse payload of apply_patch with
// the patch body between the envelope lines. cwd is the working
// directory, against which Codex reports the relative patch paths.
func codexPatchEvent(t *testing.T, cwd, body string) string {
	t.Helper()

	data, err := json.Marshal(map[string]any{
		"session_id": guardSession, "tool_use_id": guardToolUse, "cwd": cwd, "turn_id": "turn-1",
		"hook_event_name": "PreToolUse", "tool_name": "apply_patch",
		"tool_input": map[string]any{"command": "*** Begin Patch\n" + body + "*** End Patch\n"},
	})
	require.NoError(t, err)

	return string(data)
}

// guardHook runs `seamark -C root lessons --guard --hook [args]` in the
// test process with payload on stdin.
func guardHook(t *testing.T, root, payload string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runIn(t, payload, append([]string{"-C", root, "lessons", "--guard", "--hook"}, args...)...)
}

// baselineHook runs `seamark -C root lessons --baseline --hook [args]`.
func baselineHook(t *testing.T, root, payload string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runIn(t, payload, append([]string{"-C", root, "lessons", "--baseline", "--hook"}, args...)...)
}

// requireDenied checks that err is the deny of the guard and returns the
// reasons. Execute adds the "seamark: " prefix and exits 2 for it.
func requireDenied(t *testing.T, err error) string {
	t.Helper()

	require.Error(t, err)
	require.ErrorIs(t, err, gate.ErrBlocked, "a deny exits 2")
	require.True(t, strings.HasPrefix(err.Error(), "blocked by policy: "), err.Error())

	return strings.TrimPrefix(err.Error(), "blocked by policy: ")
}

// checkRecords returns the records of the lesson-check audit stream of
// root, or nil when the stream does not exist.
func checkRecords(t *testing.T, root string) []lessoncheck.AuditRecord {
	t.Helper()

	data, err := os.ReadFile(checkStream(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(t, err)

	var recs []lessoncheck.AuditRecord

	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var rec lessoncheck.AuditRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)

		recs = append(recs, rec)
	}

	return recs
}

// lastRecord returns the newest record of the stream and requires its
// kind.
func lastRecord(t *testing.T, root, kind string) lessoncheck.AuditRecord {
	t.Helper()

	recs := checkRecords(t, root)
	require.NotEmpty(t, recs)

	rec := recs[len(recs)-1]
	require.Equal(t, kind, rec.Kind)
	require.Equal(t, lessoncheck.CallerHook, rec.Caller)

	if kind == lessoncheck.AuditGuard {
		require.NotNil(t, rec.Guard, "a guard record carries its decision")
	}

	return rec
}

// checkStream is the path of the lesson-check audit stream of root.
func checkStream(root string) string {
	return filepath.Join(root, ".seamark", "lesson-check-audit.jsonl")
}

// checksDir is the private pre-edit state directory of root.
func checksDir(root string) string {
	return filepath.Join(root, ".seamark", "checks")
}

// pendingNames returns the file names of the pending records of root.
func pendingNames(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(checksDir(root), "pending"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(t, err)

	var names []string

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}

	return names
}

// pendingOf reads the pending record of the test call of client. The key
// hashes over the identity root, which is the git top level here.
func pendingOf(t *testing.T, root, client string) *lessoncheck.Pending {
	t.Helper()

	p, err := lessoncheck.ReadPending(root, client, reviews.MatchDigest(root, guardSession, guardToolUse),
		lessoncheck.DefaultLimits())
	require.NoError(t, err)

	return p
}

// guardPins returns the pins of the fixture by rule.
func guardPins(t *testing.T, root string) map[string]lessoncheck.Pin {
	t.Helper()

	cfg, errs, err := lessoncheck.Load(root)
	require.NoError(t, err)
	require.Empty(t, errs)

	pins := map[string]lessoncheck.Pin{}
	for _, p := range cfg.Pins {
		pins[p.Rule.Rule] = p
	}

	return pins
}

// requireNoRawIDs fails when the stream of root holds a raw provider ID.
func requireNoRawIDs(t *testing.T, root string) {
	t.Helper()

	data, err := os.ReadFile(checkStream(root))
	require.NoError(t, err)

	assert.NotContains(t, string(data), guardSession, "a record holds digests, never a raw session ID")
	assert.NotContains(t, string(data), guardToolUse, "a record holds digests, never a raw tool-use ID")
}

// TestLessonsGuardHookDeniesViolation proves the deny path of contract
// K8 for the Claude Code edit tools: a forbidden path change and an
// added pattern line deny with the exact K8 texts, exit 2, and no
// pending record. The record holds the decision and the digests over
// the identity root, and no excerpt, note, or raw ID. The last subtest
// runs the real Execute in a child process for the stderr line and the
// exit code.
func TestLessonsGuardHookDeniesViolation(t *testing.T) {
	if encoded, ok := os.LookupEnv(guardProcessEnv); ok {
		runAsSeamark(encoded)
	}

	root, sentinel := guardFixture(t)
	pins := guardPins(t, root)
	frozen, todo := pins["frozen-assets"], pins["no-todo"]

	forbidHead := "lesson check frozen-assets (forbid-path-change/1) of pin " + frozen.Ref + ` "frozen-assets": `
	patternHead := "lesson check no-todo (no-added-pattern/1) of pin " + todo.Ref + ` "no-todo": `

	cases := []struct {
		name    string
		payload string
		pin     lessoncheck.Pin
		path    string
		reasons string
	}{
		{
			name:    "a Write of a frozen path",
			payload: claudeWriteEvent(t, root, "generated/a.txt", "asset\nchanged\n"),
			pin:     frozen,
			path:    "generated/a.txt",
			reasons: forbidHead + "Write would change generated/a.txt",
		},
		{
			name: "an Edit that adds a TODO line",
			payload: claudeEditEvent(t, root, "src/handler.go", "func Handle() {}",
				"// TODO(owner): split.\nfunc Handle() {}"),
			pin:     todo,
			path:    "src/handler.go",
			reasons: patternHead + "Edit adds a line matching the pattern at src/handler.go:3: // TODO(owner): split.",
		},
		{
			name:    "a Write of a new file with a TODO line",
			payload: claudeWriteEvent(t, root, "src/new.go", "package src\n\n// TODO(owner): fill in.\n"),
			pin:     todo,
			path:    "src/new.go",
			reasons: patternHead + "Write adds a line matching the pattern at src/new.go:3: // TODO(owner): fill in.",
		},
		{
			name:    "an Edit with an empty old_string that creates a file with a TODO line",
			payload: claudeEditEvent(t, root, "src/created.go", "", "package src\n\n// TODO(owner): fill in.\n"),
			pin:     todo,
			path:    "src/created.go",
			reasons: patternHead + "Edit adds a line matching the pattern at src/created.go:3: // TODO(owner): fill in.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := treeListing(t, filepath.Join(root, filepath.Dir(tc.path)))

			stdout, stderr, err := guardHook(t, root, tc.payload)
			assert.Equal(t, tc.reasons, requireDenied(t, err))
			assert.Empty(t, stdout, "the exit protocol writes nothing on stdout")
			assert.Empty(t, stderr, "Execute prints the reasons")

			assert.Equal(t, before, treeListing(t, filepath.Join(root, filepath.Dir(tc.path))), "the guard writes no file")
			assert.Empty(t, pendingNames(t, root), "a deny writes no pending record")

			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			d := rec.Guard

			assert.Equal(t, lessoncheck.GuardSchema, d.Schema)
			assert.Equal(t, lessoncheck.GuardDeny, d.Decision)
			assert.Equal(t, lessoncheck.GuardCauseViolation, d.Cause)
			assert.Equal(t, lessoncheck.GuardRequestedDeny, d.Requested)
			assert.Equal(t, lessoncheck.GuardProtocolExit2, d.Protocol)
			assert.True(t, d.ReplyWritten)
			assert.Equal(t, lessoncheck.GuardNativeUnverified, d.NativeBlocking, "no reviewed recording verifies a pre-write path yet")
			assert.True(t, d.PathsComplete)
			require.Len(t, d.Pins, 1, "only the enforce pin in scope is evaluated")
			assert.Equal(t, tc.pin.Identity, d.Pins[0].PinIdentity)
			assert.Equal(t, tc.pin.Version, d.Pins[0].PinVersion)
			assert.Equal(t, lessoncheck.OutcomeViolation, d.Pins[0].Outcome)
			assert.Equal(t, d.Pins, rec.Pins, "the top-level pins count the evaluated pins")

			assert.Equal(t, lessoncheck.BoundaryProposedEdit, rec.Boundary)
			assert.Equal(t, d.InputSHA256, rec.SnapshotSHA)
			assert.Equal(t, []string{tc.path}, rec.Paths)
			assert.Equal(t, integration.ClaudeID, rec.Client)
			assert.Equal(t, reviews.SessionDigest(root, guardSession), rec.SessionSHA)
			assert.Equal(t, reviews.MatchDigest(root, guardSession, guardToolUse), rec.MatchSHA)
			assert.Equal(t, reviews.ContextDigest(root, integration.ClaudeID, claudeReceiver(guardSession)), rec.ContextSHA)
			require.NotNil(t, rec.Emitted)
			assert.True(t, *rec.Emitted)
			assert.Empty(t, rec.Error)

			data, err := os.ReadFile(checkStream(root))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "TODO(", "a record holds no excerpt")
			assert.NotContains(t, string(data), "maintainer-managed", "a record holds no note")
			requireNoRawIDs(t, root)
		})
	}

	t.Run("Execute prints the reasons and exits 2", func(t *testing.T) {
		exe, err := os.Executable()
		require.NoError(t, err)

		encoded, err := json.Marshal([]string{"-C", root, "lessons", "--guard", "--hook"})
		require.NoError(t, err)

		cmd := exec.Command(exe, "-test.run=^TestLessonsGuardHookDeniesViolation$")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), guardProcessEnv+"="+string(encoded))
		cmd.Stdin = strings.NewReader(cases[0].payload)

		var stdout, stderr strings.Builder

		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		var exitErr *exec.ExitError

		require.ErrorAs(t, cmd.Run(), &exitErr)
		assert.Equal(t, 2, exitErr.ExitCode(), "both clients read exit 2 as a block")
		assert.Equal(t, []string{"seamark: blocked by policy: " + cases[0].reasons}, seamarkLines(stderr.String()))
		assert.NotContains(t, stdout.String(), "lesson check", "the reasons go to stderr only")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsGuardHookDeniesMultiFileWhole proves that the guard judges
// a native call as a whole (contract K8): one violating file of a
// multi-file patch, or one failing step of a MultiEdit, denies the whole
// call, and no path of it gets a pending record.
func TestLessonsGuardHookDeniesMultiFileWhole(t *testing.T) {
	root, sentinel := guardFixture(t)
	frozen := guardPins(t, root)["frozen-assets"]

	t.Run("one violating file of a Codex patch denies every file", func(t *testing.T) {
		payload := codexPatchEvent(t, root, "*** Add File: src/ok.go\n+package src\n"+
			"*** Delete File: generated/a.txt\n*** Add File: docs/new.md\n+page\n")

		_, _, err := guardHook(t, root, payload, "--client", "codex")
		assert.Equal(t, "lesson check frozen-assets (forbid-path-change/1) of pin "+frozen.Ref+
			` "frozen-assets": apply_patch would change generated/a.txt`, requireDenied(t, err))

		assert.FileExists(t, filepath.Join(root, "generated", "a.txt"))
		assert.NoFileExists(t, filepath.Join(root, "src", "ok.go"))
		assert.Empty(t, pendingNames(t, root), "the clean files of a denied call get no baseline either")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, []string{"docs/new.md", "generated/a.txt", "src/ok.go"}, rec.Paths)
		assert.Equal(t, lessoncheck.GuardCauseViolation, rec.Guard.Cause)
		assert.Equal(t, integration.CodexID, rec.Client)
	})

	t.Run("one failing step of a MultiEdit denies the whole call", func(t *testing.T) {
		payload := claudeGuardEvent(t, root, "MultiEdit", guardToolUse, map[string]any{
			"file_path": filepath.Join(root, "src", "handler.go"),
			"edits": []map[string]any{
				{"old_string": "package src", "new_string": "package src // clean"},
				{"old_string": "func Missing() {}", "new_string": "func Present() {}"},
			},
		})

		_, _, err := guardHook(t, root, payload)
		reasons := requireDenied(t, err)
		assert.Contains(t, reasons, "cannot evaluate src/handler.go: unprojectable-edit")
		assert.Contains(t, reasons, "old_string occurs 0 times")

		data, err := os.ReadFile(filepath.Join(root, "src", "handler.go"))
		require.NoError(t, err)
		assert.Equal(t, "package src\n\nfunc Handle() {}\n", string(data))
		assert.Empty(t, pendingNames(t, root))

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause)
		assert.Equal(t, []lessoncheck.Reason{lessoncheck.ReasonUnprojectableEdit}, rec.Guard.Reasons)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsGuardHookFailsClosed proves the fail-closed cases of
// contract K8 (AC-33): an unreadable request, an invalid or unreadable
// check set, a path that the guard cannot resolve, an edit that the
// projection cannot compute, a limit, a broken hook command, a missing
// work tree, and a panic each deny. No case writes a pending record.
func TestLessonsGuardHookFailsClosed(t *testing.T) {
	const (
		cannotRead  = "lesson check guard cannot read this request: "
		cannotWrite = "lesson check guard cannot read this Write request: "
		configHead  = "lesson checks cannot be established: "
		configTail  = "; ask the user to fix .seamark/lessons.yaml"
	)

	cases := []struct {
		name    string
		setup   func(t *testing.T, root string)
		payload func(t *testing.T, root string) string
		args    []string
		cause   string
		reason  lessoncheck.Reason
		check   func(t *testing.T, reasons string)
	}{
		{
			name:    "a payload that is not JSON",
			payload: func(*testing.T, string) string { return `{"tool_name": "Edit"` },
			cause:   lessoncheck.GuardCauseUnparseableInput,
			reason:  lessoncheck.ReasonUnparseableInput,
			check: func(t *testing.T, reasons string) {
				assert.True(t, strings.HasPrefix(reasons, cannotRead), reasons)
			},
		},
		{
			name: "a payload above the size cap",
			payload: func(t *testing.T, root string) string {
				return claudeWriteEvent(t, root, "README.md", strings.Repeat("x", integration.MaxHookPayload))
			},
			cause:  lessoncheck.GuardCauseUnparseableInput,
			reason: lessoncheck.ReasonUnparseableInput,
			check: func(t *testing.T, reasons string) {
				assert.True(t, strings.HasPrefix(reasons, cannotRead), reasons)
				assert.Contains(t, reasons, "payload exceeds")
			},
		},
		{
			name: "a request without tool_name",
			payload: func(t *testing.T, root string) string {
				return `{"session_id":"` + guardSession + `","tool_input":{"file_path":"` + filepath.Join(root, "README.md") + `"}}`
			},
			cause:  lessoncheck.GuardCauseUnparseableInput,
			reason: lessoncheck.ReasonUnparseableInput,
			check: func(t *testing.T, reasons string) {
				assert.Equal(t, cannotRead+"malformed hook event: the request names no tool_name", reasons)
			},
		},
		{
			name: "a Write without file_path",
			payload: func(t *testing.T, root string) string {
				return claudeGuardEvent(t, root, "Write", guardToolUse, map[string]any{"content": "x\n"})
			},
			cause:  lessoncheck.GuardCauseUnparseableInput,
			reason: lessoncheck.ReasonUnparseableInput,
			check: func(t *testing.T, reasons string) {
				assert.Equal(t, cannotWrite+"malformed hook event: the Write request names no file_path", reasons)
			},
		},
		{
			name: "a lessons.yaml that does not parse denies every edit",
			setup: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "README.md", "x\n") },
			cause:   lessoncheck.GuardCauseConfigurationError,
			reason:  lessoncheck.ReasonConfigurationError,
			check: func(t *testing.T, reasons string) {
				assert.True(t, strings.HasPrefix(reasons, configHead), reasons)
				assert.True(t, strings.HasSuffix(reasons, configTail), reasons)
			},
		},
		{
			name: "an invalid check definition denies with no fallback",
			setup: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml",
					strings.Replace(guardLessonsYAML, "kind: forbid-path-change/1", "kind: forbid-path-change/9", 1))
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "README.md", "x\n") },
			cause:   lessoncheck.GuardCauseConfigurationError,
			reason:  lessoncheck.ReasonConfigurationError,
			check: func(t *testing.T, reasons string) {
				assert.True(t, strings.HasPrefix(reasons, configHead), reasons)
				assert.Contains(t, reasons, "forbid-path-change/9")
				assert.True(t, strings.HasSuffix(reasons, configTail), reasons)
			},
		},
		{
			name: "an unreadable lessons.yaml denies",
			setup: func(t *testing.T, root string) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file without read permission")
				}

				path := filepath.Join(root, ".seamark", "lessons.yaml")
				require.NoError(t, os.Chmod(path, 0))
				t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "README.md", "x\n") },
			cause:   lessoncheck.GuardCauseConfigurationError,
			reason:  lessoncheck.ReasonConfigurationError,
			check: func(t *testing.T, reasons string) {
				// The deny text has a byte bound, and the cut falls inside the
				// message. A long temp folder fills the message with the root
				// path before the cause. So the start and the end always hold,
				// and the cause must show whenever the text was not cut.
				assert.True(t, strings.HasPrefix(reasons, configHead+"open "), reasons)
				assert.True(t, strings.HasSuffix(reasons, configTail), reasons)

				if !strings.HasSuffix(reasons, "…"+configTail) {
					assert.True(t, strings.HasSuffix(reasons, ": permission denied"+configTail), reasons)
				}
			},
		},
		{
			name: "a path through a loop of links arms every enforce pin",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink("loop-b", filepath.Join(root, "loop-a")))
				require.NoError(t, os.Symlink("loop-a", filepath.Join(root, "loop-b")))
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "loop-a/x.go", "x\n") },
			cause:   lessoncheck.GuardCauseUnknown,
			reason:  lessoncheck.ReasonIncompletePaths,
			check: func(t *testing.T, reasons string) {
				assert.Contains(t, reasons, "cannot evaluate a path that the guard cannot resolve: incomplete-paths")
			},
		},
		{
			name: "a link that leads out of the root arms every enforce pin",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "outside-link")))
			},
			payload: func(t *testing.T, root string) string {
				return claudeWriteEvent(t, root, "outside-link/x.go", "x\n")
			},
			cause:  lessoncheck.GuardCauseUnknown,
			reason: lessoncheck.ReasonIncompletePaths,
			check: func(t *testing.T, reasons string) {
				assert.Contains(t, reasons, "incomplete-paths")
			},
		},
		{
			name: "an Edit whose old_string does not occur",
			payload: func(t *testing.T, root string) string {
				return claudeEditEvent(t, root, "src/handler.go", "func Missing() {}", "func Present() {}")
			},
			cause:  lessoncheck.GuardCauseUnknown,
			reason: lessoncheck.ReasonUnprojectableEdit,
			check: func(t *testing.T, reasons string) {
				assert.Contains(t, reasons, "cannot evaluate src/handler.go: unprojectable-edit; old_string occurs 0 times")
			},
		},
		{
			name: "an Edit of an absent file with a non-empty old_string stays unprojectable",
			payload: func(t *testing.T, root string) string {
				return claudeEditEvent(t, root, "src/absent.go", "package", "package src\n")
			},
			cause:  lessoncheck.GuardCauseUnknown,
			reason: lessoncheck.ReasonUnprojectableEdit,
			check: func(t *testing.T, reasons string) {
				assert.Contains(t, reasons, "cannot evaluate src/absent.go: unprojectable-edit; the file does not exist")
			},
		},
		{
			name: "a current file above max-file-bytes in a pattern scope",
			setup: func(t *testing.T, root string) {
				verifyWrite(t, root, "src/big.txt", strings.Repeat("a\n", int(lessoncheck.DefaultLimits().MaxFileBytes)))
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "src/big.txt", "small\n") },
			cause:   lessoncheck.GuardCauseUnknown,
			reason:  lessoncheck.ReasonResourceLimit,
			check: func(t *testing.T, reasons string) {
				assert.Contains(t, reasons, "cannot evaluate src/big.txt: resource-limit")
			},
		},
		{
			name: "a panic denies with internal-error (contract K8, step 7)",
			setup: func(t *testing.T, _ string) {
				beforeGuardDecision = func() { panic("boom: secret payload text") }
				t.Cleanup(func() { beforeGuardDecision = nil })
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "README.md", "x\n") },
			cause:   lessoncheck.GuardCauseInternalError,
			check: func(t *testing.T, reasons string) {
				assert.Equal(t, "lesson check guard cannot decide this Write request: internal error", reasons)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, sentinel := guardFixture(t)

			if tc.setup != nil {
				tc.setup(t, root)
			}

			stdout, _, err := guardHook(t, root, tc.payload(t, root), tc.args...)
			tc.check(t, requireDenied(t, err))
			assert.Empty(t, stdout)
			assert.Empty(t, pendingNames(t, root), "a deny writes no pending record")

			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			assert.Equal(t, lessoncheck.GuardDeny, rec.Guard.Decision)
			assert.Equal(t, tc.cause, rec.Guard.Cause)
			assert.True(t, rec.Guard.ReplyWritten)
			assert.NotNil(t, rec.Guard.Pins, "pins encode as [], never null")

			if tc.reason != "" {
				assert.Contains(t, rec.Guard.Reasons, tc.reason)
			}

			data, err := os.ReadFile(checkStream(root))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "secret payload text", "a record keeps no panic value")
			assert.NotContains(t, string(data), "xxxxxxxx", "a record keeps no payload text")

			sentinel.requireNoCall(t)
		})
	}

	t.Run("an unknown hook client denies as the gate does", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "README.md", "x\n"), "--client", "nope")
		assert.Equal(t, `unknown hook client "nope" (known: claude, codex)`, requireDenied(t, err))

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardCauseInternalError, rec.Guard.Cause)
		assert.Equal(t, `unknown hook client "nope" (known: claude, codex)`, rec.Error)
		assert.Empty(t, rec.Client, "no client of the registry served the run")

		sentinel.requireNoCall(t)
	})

	t.Run("a directory outside every work tree denies", func(t *testing.T) {
		dir := t.TempDir()

		_, _, err := guardHook(t, dir, claudeWriteEvent(t, dir, "a.go", "x\n"))
		assert.Equal(t, "lesson check guard cannot decide this Write request: internal error", requireDenied(t, err))
		assert.NoDirExists(t, filepath.Join(dir, ".seamark"), "without a work tree the run writes nothing")
	})
}

// TestLessonsGuardHookAllowsWithEmptyReply proves the allow path of
// contract K8: exit 0 with nothing on stdout or stderr, a pending record
// for the paths that an executable pin scopes (contract K7), and a guard
// record without any capture opt-in. Every digest hashes over the K2
// identity root, also when the index records a linked root.
func TestLessonsGuardHookAllowsWithEmptyReply(t *testing.T) {
	root, sentinel := guardFixture(t)

	allow := func(t *testing.T, payload string, args ...string) lessoncheck.AuditRecord {
		t.Helper()

		stdout, stderr, err := guardHook(t, root, payload, args...)
		require.NoError(t, err)
		assert.Empty(t, stdout, "an allow never forces an allow")
		assert.Empty(t, stderr)

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardAllow, rec.Guard.Decision)
		assert.Equal(t, lessoncheck.GuardRequestedNone, rec.Guard.Requested)
		assert.False(t, rec.Guard.ReplyWritten, "an allow writes no reply")
		require.NotNil(t, rec.Emitted)
		assert.False(t, *rec.Emitted)

		return rec
	}

	t.Run("a path outside every executable scope stores nothing", func(t *testing.T) {
		rec := allow(t, claudeWriteEvent(t, root, "README.md", "# repo\n\nMore.\n"))
		assert.Empty(t, rec.Guard.Cause)
		assert.Empty(t, rec.Guard.Pins)
		assert.Equal(t, []string{"README.md"}, rec.Paths)
		assert.Empty(t, pendingNames(t, root), "no executable pin needs a baseline")
	})

	t.Run("a clean edit in an enforce scope stores its baseline", func(t *testing.T) {
		rec := allow(t, claudeEditEvent(t, root, "src/handler.go", "func Handle() {}", "func Handle() { _ = 1 }"))
		require.Len(t, rec.Guard.Pins, 1)
		assert.Equal(t, lessoncheck.OutcomePass, rec.Guard.Pins[0].Outcome)

		p := pendingOf(t, root, integration.ClaudeID)
		assert.Equal(t, "enforce", p.InstalledMode)
		assert.Equal(t, "Edit", p.Tool)
		assert.Equal(t, integration.ClaudeID, p.Client)
		assert.Equal(t, rec.SessionSHA, p.SessionSHA)
		assert.Equal(t, rec.MatchSHA, p.MatchSHA)
		assert.Equal(t, rec.ContextSHA, p.ContextSHA)
		assert.Equal(t, rec.ConfigSHA256, p.ConfigSHA256)
		assert.True(t, p.PathsComplete)
		require.Len(t, p.Paths, 1)
		assert.Equal(t, "src/handler.go", p.Paths[0].Path)
		assert.NotEmpty(t, p.Paths[0].ContentB64, "a pattern pin needs the before text")
		assert.NotEmpty(t, p.Paths[0].ProjectedAfterSHA256)
		require.NoError(t, lessoncheck.DeletePending(root, integration.ClaudeID, p.MatchSHA))
	})

	t.Run("a violation of a check pin allows and stores its baseline", func(t *testing.T) {
		rec := allow(t, claudeWriteEvent(t, root, "docs/readme.md", "readme\nTODO(owner): more\n"))
		assert.Empty(t, rec.Guard.Pins, "the guard evaluates enforce pins only")

		p := pendingOf(t, root, integration.ClaudeID)
		require.Len(t, p.Paths, 1)
		assert.Equal(t, "docs/readme.md", p.Paths[0].Path)
		require.NoError(t, lessoncheck.DeletePending(root, integration.ClaudeID, p.MatchSHA))
	})

	t.Run("a path outside the root allows with the cause outside-root", func(t *testing.T) {
		outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+".txt")

		rec := allow(t, claudeGuardEvent(t, root, "Write", guardToolUse,
			map[string]any{"file_path": outside, "content": "x\n"}))
		assert.Equal(t, lessoncheck.GuardCauseOutsideRoot, rec.Guard.Cause)
		assert.Empty(t, rec.Paths, "no record names the dropped path")
		assert.Empty(t, pendingNames(t, root))

		data, err := os.ReadFile(checkStream(root))
		require.NoError(t, err)
		assert.NotContains(t, string(data), filepath.Base(outside), "no audit field holds the dropped path")
	})

	t.Run("a tool outside the covered list allows with the cause tool-uncovered", func(t *testing.T) {
		rec := allow(t, claudeGuardEvent(t, root, "NotebookEdit", guardToolUse,
			map[string]any{"notebook_path": filepath.Join(root, "generated", "a.ipynb")}))
		assert.Equal(t, lessoncheck.GuardCauseToolUncovered, rec.Guard.Cause)
		assert.Equal(t, "NotebookEdit", rec.Tool)
		assert.Empty(t, rec.Paths)
	})

	t.Run("no record needs the capture opt-in, and no archive appears", func(t *testing.T) {
		assert.Len(t, checkRecords(t, root), 5, "one guard record per run")
		requireNoRawIDs(t, root)

		_, err := os.Lstat(filepath.Join(root, ".seamark", "private"))
		assert.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("every digest hashes over the identity root", func(t *testing.T) {
		_, err := run(t, "-C", root, "index")
		require.NoError(t, err)

		_, link := recordLinkedRoot(t, root)

		allow(t, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, reviews.SessionDigest(link, guardSession), rec.SessionSHA)
		assert.Equal(t, reviews.MatchDigest(link, guardSession, guardToolUse), rec.MatchSHA)
		assert.Equal(t, reviews.ContextDigest(link, integration.ClaudeID, claudeReceiver(guardSession)), rec.ContextSHA)
		assert.NotEqual(t, reviews.MatchDigest(root, guardSession, guardToolUse), rec.MatchSHA)

		p, err := lessoncheck.ReadPending(root, integration.ClaudeID, rec.MatchSHA, lessoncheck.DefaultLimits())
		require.NoError(t, err, "the key hashes over the identity root, the directory is under the git top level")
		assert.Equal(t, rec.SessionSHA, p.SessionSHA)
	})

	t.Run("an absent lessons.yaml arms no pin and allows", func(t *testing.T) {
		// Only an absent file means no checks (contract K8, step 1).
		require.NoError(t, os.Remove(filepath.Join(root, ".seamark", "lessons.yaml")))
		require.NoError(t, lessoncheck.DeletePending(root, integration.ClaudeID, lastRecord(t, root, lessoncheck.AuditGuard).MatchSHA))

		rec := allow(t, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		assert.Empty(t, rec.Guard.Pins)
		assert.Empty(t, rec.ConfigSHA256, "an absent file has no digest")
		assert.Empty(t, pendingNames(t, root), "no executable pin needs a baseline")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsGuardHookAuditFailureKeepsDecision proves AC-33 and §5 row
// 33: a failed audit append or pending write changes neither the reply
// nor the exit code, and the hook says nothing about it.
func TestLessonsGuardHookAuditFailureKeepsDecision(t *testing.T) {
	t.Run("an audit stream that cannot be written", func(t *testing.T) {
		// A directory at the lock path fails the open of the lock, so
		// every append fails.
		root, sentinel := guardFixture(t)
		require.NoError(t, os.MkdirAll(checkStream(root)+".lock", 0o755))

		stdout, stderr, err := guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		assert.Contains(t, requireDenied(t, err), "Write would change generated/a.txt")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr, "the hook says nothing about the audit")

		stdout, stderr, err = guardHook(t, root, claudeWriteEvent(t, root, "README.md", "x\n"))
		require.NoError(t, err, "an allow stays an allow")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		assert.NoFileExists(t, checkStream(root), "every append failed")

		sentinel.requireNoCall(t)
	})

	t.Run("a lock file that is a link", func(t *testing.T) {
		root, sentinel := guardFixture(t)
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o700))
		require.NoError(t, os.Symlink(target, checkStream(root)+".lock"))

		_, stderr, err := guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		requireDenied(t, err)
		assert.Empty(t, stderr)
		assert.NoFileExists(t, checkStream(root), "the writer refuses the link and writes nothing")

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data), "nothing goes through the link")

		sentinel.requireNoCall(t)
	})

	t.Run("a checks directory that is a link", func(t *testing.T) {
		root, sentinel := guardFixture(t)
		target := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o700))
		require.NoError(t, os.Symlink(target, checksDir(root)))

		stdout, stderr, err := guardHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
		require.NoError(t, err, "a failed pending write keeps the allow")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		entries, err := os.ReadDir(target)
		require.NoError(t, err)
		assert.Empty(t, entries, "nothing goes through the link")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardAllow, rec.Guard.Decision)
		assert.Equal(t, lessoncheck.ErrUnsafeChecksDir.Error(), rec.Error, "the record names the pending failure")

		_, _, err = guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		requireDenied(t, err)

		sentinel.requireNoCall(t)
	})
}

// TestLessonsGuardHookCheckModeAndNoteNeverDeny proves AC-36 and R-38:
// a check pin never denies, the word enforce in a note never arms a pin,
// and a prepare record of the same pin version and path never lets an
// enforce violation pass (F9).
func TestLessonsGuardHookCheckModeAndNoteNeverDeny(t *testing.T) {
	root, sentinel := guardFixture(t)
	frozen := guardPins(t, root)["frozen-assets"]

	t.Run("a violation of a check pin whose note says enforce allows", func(t *testing.T) {
		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nTODO(owner): x\n"))
		require.NoError(t, err)

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardAllow, rec.Guard.Decision)
		assert.Empty(t, rec.Guard.Pins)
	})

	t.Run("an advisory pin whose note says mode enforce allows", func(t *testing.T) {
		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "README.md", strings.Repeat("long line\n", 50)))
		require.NoError(t, err)
		assert.Empty(t, lastRecord(t, root, lessoncheck.AuditGuard).Guard.Pins)
	})

	t.Run("a prepare record never lets an enforce violation pass", func(t *testing.T) {
		_, err := run(t, "-C", root, "lessons", "--prepare", filepath.Join(root, "generated", "a.txt"))
		require.NoError(t, err)

		firings, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons-audit.jsonl"))
		require.NoError(t, err)
		require.Contains(t, string(firings), `"surface":"prepare"`)
		require.Contains(t, string(firings), frozen.Version, "the preparation recorded the same pin version")

		_, _, err = guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		assert.Contains(t, requireDenied(t, err), "Write would change generated/a.txt")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		require.Len(t, rec.Guard.Pins, 1)
		assert.Equal(t, frozen.Version, rec.Guard.Pins[0].PinVersion)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsGuardHookCodexApplyPatch proves the Codex rows of contract
// K8 and ruling A8: Add, Delete, and Move project; an Update with hunks
// in a pattern scope stays unknown and denies; a forbid pin decides on
// paths alone; a grammar that the decoder cannot read arms every enforce
// pin; relative paths start at the event directory.
func TestLessonsGuardHookCodexApplyPatch(t *testing.T) {
	root, sentinel := guardFixture(t)
	pins := guardPins(t, root)

	forbidHead := "lesson check frozen-assets (forbid-path-change/1) of pin " + pins["frozen-assets"].Ref + ` "frozen-assets": `
	patternHead := "lesson check no-todo (no-added-pattern/1) of pin " + pins["no-todo"].Ref + ` "no-todo": `

	denies := []struct {
		name    string
		cwd     string
		body    string
		cause   string
		reasons string
	}{
		{
			name:    "an Add with a TODO line",
			body:    "*** Add File: src/new.go\n+package src\n+// TODO(owner): x\n",
			cause:   lessoncheck.GuardCauseViolation,
			reasons: patternHead + "apply_patch adds a line matching the pattern at src/new.go:2: // TODO(owner): x",
		},
		{
			name:    "a Delete of a frozen path",
			body:    "*** Delete File: generated/a.txt\n",
			cause:   lessoncheck.GuardCauseViolation,
			reasons: forbidHead + "apply_patch would change generated/a.txt",
		},
		{
			name:    "a Move out of a frozen path",
			body:    "*** Update File: generated/a.txt\n*** Move to: docs/a.txt\n",
			cause:   lessoncheck.GuardCauseViolation,
			reasons: forbidHead + "apply_patch would change generated/a.txt -> docs/a.txt",
		},
		{
			name:  "an Update with hunks in a pattern scope",
			body:  "*** Update File: src/handler.go\n@@\n-func Handle() {}\n+func Handle() { _ = 1 }\n",
			cause: lessoncheck.GuardCauseUnknown,
			reasons: "lesson check no-todo (no-added-pattern/1) of pin " + pins["no-todo"].Ref +
				" cannot evaluate src/handler.go: unprojectable-edit; update hunks have no verified projection",
		},
		{
			name:    "a relative path starts at the event directory",
			cwd:     "src",
			body:    "*** Add File: nested.go\n+// TODO(owner): x\n",
			cause:   lessoncheck.GuardCauseViolation,
			reasons: patternHead + "apply_patch adds a line matching the pattern at src/nested.go:1: // TODO(owner): x",
		},
	}

	for _, tc := range denies {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := guardHook(t, root, codexPatchEvent(t, filepath.Join(root, tc.cwd), tc.body), "--client", "codex")
			assert.Equal(t, tc.reasons, requireDenied(t, err))

			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			assert.Equal(t, tc.cause, rec.Guard.Cause)
			assert.Equal(t, integration.CodexID, rec.Client)
			assert.Equal(t, "apply_patch", rec.Tool)
			assert.Equal(t, reviews.ContextDigest(root, integration.CodexID,
				integration.ReceivingContextOf(integration.CodexID, guardSession, "").ID), rec.ContextSHA)
			assert.Empty(t, pendingNames(t, root))
		})
	}

	t.Run("an environment line arms every enforce pin", func(t *testing.T) {
		payload := codexPatchEvent(t, root, "*** Environment ID: env-1\n*** Add File: README2.md\n+x\n")

		_, _, err := guardHook(t, root, payload, "--client", "codex")
		reasons := requireDenied(t, err)
		assert.Contains(t, reasons, "cannot evaluate a path that the guard cannot resolve: incomplete-paths")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause)
		assert.False(t, rec.Guard.PathsComplete)
		assert.Len(t, rec.Guard.Pins, 2, "every enforce pin applies")
		assert.Contains(t, rec.Error, "unsupported edit grammar")
	})

	t.Run("a Move from a frozen path to outside the root arms every enforce pin", func(t *testing.T) {
		// Dropping the move would allow the removal of a frozen file.
		payload := codexPatchEvent(t, root, "*** Update File: generated/a.txt\n*** Move to: ../moved.txt\n")

		_, _, err := guardHook(t, root, payload, "--client", "codex")
		assert.Contains(t, requireDenied(t, err), "cannot evaluate a path that the guard cannot resolve: incomplete-paths")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause)
		assert.Empty(t, rec.Paths, "no record names a path of the dropped move")
	})

	t.Run("an envelope without a hunk is unparseable", func(t *testing.T) {
		_, _, err := guardHook(t, root, codexPatchEvent(t, root, ""), "--client", "codex")
		assert.True(t, strings.HasPrefix(requireDenied(t, err), "lesson check guard cannot read this apply_patch request: "))
	})

	t.Run("an Update with hunks in a check scope allows without projection", func(t *testing.T) {
		payload := codexPatchEvent(t, root, "*** Update File: docs/readme.md\n@@\n-readme\n+readme TODO(x)\n")

		_, _, err := guardHook(t, root, payload, "--client", "codex")
		require.NoError(t, err)

		p := pendingOf(t, root, integration.CodexID)
		assert.Equal(t, []string{lessoncheck.LimitationNoProjection}, p.Limitations)
		require.Len(t, p.Paths, 1)
		assert.Empty(t, p.Paths[0].ProjectedAfterSHA256)
		require.NoError(t, lessoncheck.DeletePending(root, integration.CodexID, p.MatchSHA))
	})

	t.Run("a Move outside every scope allows", func(t *testing.T) {
		_, _, err := guardHook(t, root, codexPatchEvent(t, root, "*** Update File: README.md\n*** Move to: NOTES.md\n"),
			"--client", "codex")
		require.NoError(t, err)
		assert.Equal(t, []string{"NOTES.md", "README.md"}, lastRecord(t, root, lessoncheck.AuditGuard).Paths)
	})

	t.Run("an Add outside the root allows with the cause outside-root", func(t *testing.T) {
		_, _, err := guardHook(t, root, codexPatchEvent(t, root, "*** Add File: ../outside.txt\n+x\n"), "--client", "codex")
		require.NoError(t, err)
		assert.Equal(t, lessoncheck.GuardCauseOutsideRoot, lastRecord(t, root, lessoncheck.AuditGuard).Guard.Cause)
	})

	t.Run("a shell event is outside the covered list", func(t *testing.T) {
		payload := `{"session_id":"` + guardSession + `","tool_use_id":"` + guardToolUse + `","cwd":"` + root +
			`","hook_event_name":"PreToolUse","tool_name":"shell","tool_input":{"command":["rm","generated/a.txt"]}}`

		_, _, err := guardHook(t, root, payload, "--client", "codex")
		require.NoError(t, err)
		assert.Equal(t, lessoncheck.GuardCauseToolUncovered, lastRecord(t, root, lessoncheck.AuditGuard).Guard.Cause)
	})

	t.Run("no record holds patch text", func(t *testing.T) {
		data, err := os.ReadFile(checkStream(root))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "*** ")
		assert.NotContains(t, string(data), "TODO(")
		requireNoRawIDs(t, root)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsGuardHookNeverOpensArchive proves that the pre-edit hooks
// never open or create the capture archive: the K9 record needs no
// capture opt-in (spec L263). A run without an archive creates none,
// and the handler file imports neither the archive nor the capture
// package.
func TestLessonsGuardHookNeverOpensArchive(t *testing.T) {
	runs := func(t *testing.T, root string) {
		t.Helper()

		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
		requireDenied(t, err)

		_, _, err = guardHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
		require.NoError(t, err)

		_, _, err = baselineHook(t, root, claudeWriteEvent(t, root, "src/handler.go", "package src\n"))
		require.NoError(t, err)
	}

	t.Run("without an archive", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		runs(t, root)

		_, err := os.Lstat(filepath.Join(root, ".seamark", "private"))
		assert.ErrorIs(t, err, fs.ErrNotExist, "no hook creates the archive")
		assert.Len(t, checkRecords(t, root), 3, "the records need no capture opt-in")

		sentinel.requireNoCall(t)
	})

	t.Run("the handler file imports no archive or capture package", func(t *testing.T) {
		file, err := parser.ParseFile(token.NewFileSet(), "lessons_guard.go", nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)

			assert.NotEqual(t, "github.com/seamark-dev/seamark/internal/archive", path)
			assert.NotEqual(t, "github.com/seamark-dev/seamark/internal/capture", path)
		}
	})
}

// TestLessonsGuardHookCallsNoInvoker proves AC-1 for the guard: every
// path of the handler (allow, deny, unparseable, configuration error,
// and the Codex decoder) runs without a model or network call.
func TestLessonsGuardHookCallsNoInvoker(t *testing.T) {
	root, sentinel := guardFixture(t)

	_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))
	require.NoError(t, err)

	_, _, err = guardHook(t, root, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"))
	requireDenied(t, err)

	_, _, err = guardHook(t, root, "{")
	requireDenied(t, err)

	_, _, err = guardHook(t, root, codexPatchEvent(t, root, "*** Delete File: generated/a.txt\n"), "--client", "codex")
	requireDenied(t, err)

	verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")

	_, _, err = guardHook(t, root, claudeWriteEvent(t, root, "README.md", "x\n"))
	requireDenied(t, err)

	assert.Len(t, checkRecords(t, root), 5)
	sentinel.requireNoCall(t)
}

// TestLessonsGuardFlagRules proves the §4.1 refusals of --guard and
// --baseline, their exclusivity (§4.2), and the final --client text,
// asserted whole (table (e)). A refused call writes nothing. The
// positive controls show that the routed calls reach the pre-edit
// handlers and not the advisory edit hook.
func TestLessonsGuardFlagRules(t *testing.T) {
	root, sentinel := guardFixture(t)

	// Every guard below runs before a path is resolved, so a relative
	// path is enough and keeps the subtest names short.
	const file = "src/handler.go"

	refusals := []struct {
		args []string
		want string
	}{
		{[]string{"--guard"}, "--guard needs --hook: it reads one native pre-edit event from stdin"},
		{[]string{"--baseline"}, "--baseline needs --hook: it reads one native pre-edit event from stdin"},
		{[]string{"--guard", "--client", "codex"}, "--guard needs --hook: it reads one native pre-edit event from stdin"},
		{[]string{"--guard", "--baseline", "--hook"}, "--guard does not combine with --baseline"},
		{[]string{"--baseline", "--guard"}, "--guard does not combine with --baseline"},
		{[]string{"--file", file, "--guard", "--hook"}, "--guard does not combine with --file"},
		{[]string{"--region", "src", "--guard", "--hook"}, "--guard does not combine with --region"},
		{[]string{"--list", "--baseline", "--hook"}, "--baseline does not combine with --list"},
		{[]string{"--stats", "--guard", "--hook"}, "--guard does not combine with --stats"},
		{[]string{"--explore", "--guard", "--hook"}, "--guard does not combine with --explore"},
		{[]string{"--hook-reset", "--baseline", "--hook"}, "--baseline does not combine with --hook-reset"},
		{[]string{"--apply", "p1", "--guard", "--hook"}, "--guard does not combine with --apply"},
		{[]string{"--list", "--client", "claude"}, "--client applies to --hook, --hook-reset, --guard, --baseline, --explore, " +
			"and --verify --hook only; the agent that --distill runs is agent.cli in .seamark/config.yaml"},
	}

	for _, tc := range refusals {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			stdout, _, err := runIn(t, claudeWriteEvent(t, root, "generated/a.txt", "x\n"),
				append([]string{"-C", root, "lessons"}, tc.args...)...)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.NotErrorIs(t, err, gate.ErrBlocked, "a usage error exits 1")
			assert.Empty(t, stdout)
		})
	}

	// The guards of other modes run first. Their texts can grow in later
	// steps, so these rows assert only that the call is refused with a
	// mode conflict that names both flags.
	for _, args := range [][]string{
		{"--verify", "--guard"},
		{"--prepare", file, "--baseline"},
		{"--set-mode", "check", "--pin", "x", "--guard"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := runIn(t, "", append([]string{"-C", root, "lessons"}, args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not combine with")
			assert.Contains(t, err.Error(), "--"+args[0][2:])
		})
	}

	t.Run("positional arguments and --json are refused", func(t *testing.T) {
		_, _, err := runIn(t, "", "-C", root, "lessons", "--guard", "--hook", "extra")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), `unexpected argument "extra"`), err.Error())

		_, _, err = runIn(t, "", "-C", root, "lessons", "--baseline", "--hook", "--json")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "--json applies to --verify"), err.Error())
	})

	t.Run("a refused call writes nothing", func(t *testing.T) {
		assert.NoFileExists(t, checkStream(root))
		assert.NoDirExists(t, checksDir(root))
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-audit.jsonl"))
	})

	t.Run("the routed calls reach the pre-edit handlers, never the edit hook", func(t *testing.T) {
		clean := claudeEditEvent(t, root, "src/handler.go", "func Handle() {}", "func Handle() { _ = 1 }")

		stdout, _, err := guardHook(t, root, clean, "--client", "claude")
		require.NoError(t, err)
		assert.Empty(t, stdout, "the edit hook would print the reminder of the pin in scope")
		assert.Equal(t, lessoncheck.AuditGuard, lastRecord(t, root, lessoncheck.AuditGuard).Kind)

		stdout, _, err = baselineHook(t, root, clean)
		require.NoError(t, err)
		assert.Empty(t, stdout)
		assert.Equal(t, lessoncheck.AuditBaseline, lastRecord(t, root, lessoncheck.AuditBaseline).Kind)

		_, _, err = guardHook(t, root, codexPatchEvent(t, root, "*** Add File: NOTES.md\n+x\n"), "--client", "codex")
		require.NoError(t, err)

		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-audit.jsonl"), "no firing record: no advice ran")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsBaselineHookNeverDenies proves the check-only row of
// contract K8: lessons --baseline --hook exits 0 with nothing on stdout
// or stderr for every request. It stores the pending baseline with the
// installed mode check (contract K7). A run that stores none names the
// reason in its baseline record.
func TestLessonsBaselineHookNeverDenies(t *testing.T) {
	quiet := func(t *testing.T, root, payload string, args ...string) lessoncheck.AuditRecord {
		t.Helper()

		stdout, stderr, err := baselineHook(t, root, payload, args...)
		require.NoError(t, err, "the baseline handler never denies")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		return lastRecord(t, root, lessoncheck.AuditBaseline)
	}

	t.Run("violations store their baselines", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		for _, tc := range []struct {
			client, payload, path string
		}{
			{integration.ClaudeID, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"), "generated/a.txt"},
			{integration.ClaudeID, claudeEditEvent(t, root, "src/handler.go", "func Handle() {}", "// TODO(x): y\nfunc Handle() {}"), "src/handler.go"},
			{integration.CodexID, codexPatchEvent(t, root, "*** Delete File: generated/a.txt\n"), "generated/a.txt"},
		} {
			rec := quiet(t, root, tc.payload, "--client", tc.client)
			assert.Empty(t, rec.Error)
			assert.Equal(t, lessoncheck.BoundaryProposedEdit, rec.Boundary)
			assert.NotEmpty(t, rec.SnapshotSHA)
			assert.Equal(t, []string{tc.path}, rec.Paths)
			assert.Empty(t, rec.Pins, "the baseline handler evaluates nothing")
			assert.Nil(t, rec.Guard)

			p := pendingOf(t, root, tc.client)
			assert.Equal(t, "check", p.InstalledMode)
			require.Len(t, p.Paths, 1)
			assert.Equal(t, tc.path, p.Paths[0].Path)
			assert.Equal(t, rec.MatchSHA, p.MatchSHA)
			require.NoError(t, lessoncheck.DeletePending(root, tc.client, p.MatchSHA))
		}

		requireNoRawIDs(t, root)
		sentinel.requireNoCall(t)
	})

	t.Run("a path outside every executable scope needs no baseline", func(t *testing.T) {
		root, sentinel := guardFixture(t)

		rec := quiet(t, root, claudeWriteEvent(t, root, "README.md", "x\n"))
		assert.Empty(t, rec.Error)
		assert.Empty(t, pendingNames(t, root))

		sentinel.requireNoCall(t)
	})

	t.Run("each pre-edit run sweeps an expired baseline", func(t *testing.T) {
		// A crash or a cancelled tool leaves a record that no post-edit
		// run consumes (contract K7, crash recovery).
		root, sentinel := guardFixture(t)
		old := time.Now().Add(-2 * lessoncheck.DefaultLimits().PendingMaxAge)

		for _, hook := range []func(*testing.T, string, string, ...string) (string, string, error){baselineHook, guardHook} {
			quiet(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nmore\n"))

			names := pendingNames(t, root)
			require.Len(t, names, 1)

			path := filepath.Join(checksDir(root), "pending", names[0])
			require.NoError(t, os.Chtimes(path, old, old))

			_, _, err := hook(t, root, claudeWriteEvent(t, root, "README.md", "x\n"))
			require.NoError(t, err)
			assert.Empty(t, pendingNames(t, root), "the run removed the expired record")

			recs := checkRecords(t, root)
			discarded := recs[len(recs)-1]
			assert.Equal(t, lessoncheck.AuditBaselineDiscarded, discarded.Kind, "the sweep runs after the record of the run")
			assert.Equal(t, lessoncheck.DiscardExpired, discarded.Discarded)
			assert.Equal(t, reviews.MatchDigest(root, guardSession, guardToolUse), discarded.MatchSHA)
		}

		sentinel.requireNoCall(t)
	})

	failures := []struct {
		name    string
		setup   func(t *testing.T, root string)
		payload func(t *testing.T, root string) string
		args    []string
		err     string
	}{
		{
			name:    "a payload that is not JSON",
			payload: func(*testing.T, string) string { return "{" },
			err:     "malformed hook event",
		},
		{
			name: "a tool outside the covered list",
			payload: func(t *testing.T, root string) string {
				return claudeGuardEvent(t, root, "NotebookEdit", guardToolUse, map[string]any{"notebook_path": "x.ipynb"})
			},
			err: "hook event not applicable",
		},
		{
			name: "an unknown hook client",
			payload: func(t *testing.T, root string) string {
				return claudeWriteEvent(t, root, "generated/a.txt", "x\n")
			},
			args: []string{"--client", "nope"},
			err:  `unknown hook client "nope" (known: claude, codex)`,
		},
		{
			name:    "a lessons.yaml that does not parse",
			setup:   func(t *testing.T, root string) { verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n") },
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "generated/a.txt", "x\n") },
			err:     "lessons config",
		},
		{
			name: "an invalid check definition",
			setup: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml",
					strings.Replace(guardLessonsYAML, "kind: forbid-path-change/1", "kind: forbid-path-change/9", 1))
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "generated/a.txt", "x\n") },
			err:     "forbid-path-change/9",
		},
		{
			name: "a checks directory that is a link",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o700))
				require.NoError(t, os.Symlink(t.TempDir(), checksDir(root)))
			},
			payload: func(t *testing.T, root string) string { return claudeWriteEvent(t, root, "generated/a.txt", "x\n") },
			err:     lessoncheck.ErrUnsafeChecksDir.Error(),
		},
		{
			name: "an event without a tool-use ID",
			payload: func(t *testing.T, root string) string {
				return claudeGuardEvent(t, root, "Write", "",
					map[string]any{"file_path": filepath.Join(root, "generated", "a.txt"), "content": "x\n"})
			},
			err: errNoMatchID.Error(),
		},
	}

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			root, sentinel := guardFixture(t)

			if tc.setup != nil {
				tc.setup(t, root)
			}

			rec := quiet(t, root, tc.payload(t, root), tc.args...)
			assert.Contains(t, rec.Error, tc.err, "the record names the reason")
			assert.Empty(t, pendingNames(t, root), "a failure stores no baseline")

			sentinel.requireNoCall(t)
		})
	}
}

// TestLessonsBaselineHookCallsNoInvoker proves AC-1 for the baseline
// handler: the decode, the projection, and the pending write of both
// clients run without a model or network call.
func TestLessonsBaselineHookCallsNoInvoker(t *testing.T) {
	root, sentinel := guardFixture(t)

	_, _, err := baselineHook(t, root, claudeWriteEvent(t, root, "docs/readme.md", "readme\nTODO(x): y\n"))
	require.NoError(t, err)
	assert.Equal(t, "check", pendingOf(t, root, integration.ClaudeID).InstalledMode)

	_, _, err = baselineHook(t, root, codexPatchEvent(t, root, "*** Add File: src/new.go\n+package src\n"), "--client", "codex")
	require.NoError(t, err)
	assert.Equal(t, "check", pendingOf(t, root, integration.CodexID).InstalledMode)

	assert.Len(t, pendingNames(t, root), 2)
	sentinel.requireNoCall(t)
}

// TestLessonsHookNeverDeniesUnderEnforce proves BRIEF rule 15 and §6 A7:
// the advisory edit hook never runs the guard. With an enforce pin in
// scope and a violating edit, lessons --hook exits 0 with advice only,
// creates nothing under .seamark/checks, and writes no K9 record. An
// invalid lessons.yaml keeps it fail-open too.
func TestLessonsHookNeverDeniesUnderEnforce(t *testing.T) {
	root, sentinel := guardFixture(t)

	stdout, _, err := runIn(t, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"), "-C", root, "lessons", "--hook")
	require.NoError(t, err, "the edit hook fails open and never denies")
	assert.Contains(t, stdout, "additionalContext", "the edit hook gives advice")
	assert.Contains(t, stdout, "frozen-assets", "the advice names the pin in scope")
	assert.NotContains(t, stdout, "permissionDecision", "the advice never asks for a decision")

	verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")

	_, _, err = runIn(t, claudeWriteEvent(t, root, "generated/a.txt", "changed\n"), "-C", root, "lessons", "--hook")
	require.NoError(t, err, "an invalid lessons.yaml keeps the edit hook fail-open")

	assert.NoDirExists(t, checksDir(root), "the edit hook stores no pre-edit state")
	assert.NoFileExists(t, checkStream(root), "the edit hook writes no lesson-check record")

	sentinel.requireNoCall(t)
}

// A deletion or move removes the source entry, including a final link.
func TestLessonsGuardHookPatchSourceEntry(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, patch string
		denied                      bool
		paths                       []string
	}{
		{"delete protected link", "generated/link.txt", "../free/target.txt", "*** Delete File: generated/link.txt\n", true, []string{"generated/link.txt"}},
		{"move protected link", "generated/link.txt", "../free/target.txt", "*** Update File: generated/link.txt\n*** Move to: free/moved.txt\n", true, []string{"free/moved.txt", "generated/link.txt"}},
		{"move protected link with hunks", "generated/link.txt", "../free/target.txt", "*** Update File: generated/link.txt\n*** Move to: free/moved.txt\n@@\n-keep\n+clean\n", true, []string{"free/moved.txt", "generated/link.txt"}},
		{"delete free link to protected file", "free/link.txt", "../generated/a.txt", "*** Delete File: free/link.txt\n", false, []string{"free/link.txt"}},
		{"move free link to protected file", "free/link.txt", "../generated/a.txt", "*** Update File: free/link.txt\n*** Move to: free/moved.txt\n", false, []string{"free/link.txt", "free/moved.txt"}},
		{"move pattern-scoped link", "src/link.txt", "../free/target.txt", "*** Update File: src/link.txt\n*** Move to: src/moved.txt\n", true, []string{"src/link.txt", "src/moved.txt"}},
		{"delete dangling protected link", "generated/link.txt", "../free/absent.txt", "*** Delete File: generated/link.txt\n", true, []string{"generated/link.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, sentinel := guardFixture(t)
			verifyWrite(t, root, "free/target.txt", "keep\n")
			require.NoError(t, os.Symlink(tc.target, filepath.Join(root, tc.source)))
			_, _, err := guardHook(t, root, codexPatchEvent(t, root, tc.patch), "--client", "codex")
			if tc.denied {
				require.ErrorIs(t, err, gate.ErrBlocked)
			} else {
				require.NoError(t, err)
			}
			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			assert.Equal(t, tc.paths, rec.Paths)
			assert.True(t, rec.PathsComplete)
			if strings.HasPrefix(tc.source, "src/") {
				assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause)
				assert.Equal(t, []lessoncheck.Reason{lessoncheck.ReasonUnprojectableEdit}, rec.Guard.Reasons)
			}
			sentinel.requireNoCall(t)
		})
	}
}

func TestLessonsGuardHookOversizeArming(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		denied       bool
	}{
		{"check", strings.ReplaceAll(guardLessonsYAML, "mode: enforce", "mode: check"), false},
		{"advisory", strings.ReplaceAll(guardLessonsYAML, "mode: enforce", "mode: advisory"), false},
		{"absent config", "", false},
		{"enforce", guardLessonsYAML, true},
		{"invalid yaml", "pin: [\n", true},
		{"invalid check without enforce", strings.ReplaceAll(strings.ReplaceAll(guardLessonsYAML, "mode: enforce", "mode: check"), "kind: forbid-path-change/1", "kind: forbid-path-change/9"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, sentinel := guardFixture(t)
			if tc.config == "" {
				require.NoError(t, os.Remove(filepath.Join(root, ".seamark", "lessons.yaml")))
			} else {
				verifyWrite(t, root, ".seamark/lessons.yaml", tc.config)
			}
			payload := claudeWriteEvent(t, root, "README.md", strings.Repeat("x", integration.MaxHookPayload))
			_, _, err := guardHook(t, root, payload)
			if tc.denied {
				require.ErrorIs(t, err, gate.ErrBlocked)
			} else {
				require.NoError(t, err)
			}
			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			assert.Equal(t, tc.denied, rec.Guard.Decision == lessoncheck.GuardDeny)
			assert.Empty(t, rec.Paths, "an unreadable request contributes no raw paths")
			assert.Empty(t, pendingNames(t, root))
			sentinel.requireNoCall(t)
		})
	}
}

// shortHookBudgets runs the hooks of the test with a small guard budget
// and a small post-edit budget. A test that must pass the budget waits
// for twice its length, so it needs no upper time limit: a run that
// ignores the budget only gets slower, and the assertions then fail.
func shortHookBudgets(t *testing.T) (wait time.Duration) {
	t.Helper()

	const budget = 200 * time.Millisecond

	hookLimits = func() lessoncheck.Limits {
		lim := lessoncheck.DefaultLimits()
		lim.GuardBudget, lim.PostEditBudget = budget, budget

		return lim
	}

	t.Cleanup(func() { hookLimits = lessoncheck.DefaultLimits })

	return 2 * budget
}

// commitRepeatedLines commits src/repeated.txt with n equal lines. A
// later edit of it makes the worst input of the quadratic line diff
// (review finding R-1).
func commitRepeatedLines(t *testing.T, root string, n int) {
	t.Helper()

	verifyWrite(t, root, "src/repeated.txt", strings.Repeat("}\n", n))
	verifyGit(t, root, "add", "-A")
	verifyGit(t, root, "commit", "-q", "-m", "Add repeated lines")
}

// TestLessonsGuardHookBudgetBoundsEvaluation proves that the guard
// budget bounds the evaluation, and not only the projection (review
// finding R-1, contract K8, step 2). The diff of 20000 equal lines took
// 19 s before the fix. The hook now waits past the budget, and the
// decision must come from the ended budget: an enforce pin denies with
// resource-limit and never finds the marker, and a check pin stays
// advisory.
func TestLessonsGuardHookBudgetBoundsEvaluation(t *testing.T) {
	lines := lessoncheck.DefaultLimits().MaxDiffLines
	edited := strings.Repeat("}\n", lines/2-1) + "TODO(x)\n" + strings.Repeat("}\n", lines/2)

	t.Run("an enforce pin denies with resource-limit", func(t *testing.T) {
		root, sentinel := guardFixture(t)
		commitRepeatedLines(t, root, lines)

		wait := shortHookBudgets(t)
		beforeGuardDecision = func() { time.Sleep(wait) }

		t.Cleanup(func() { beforeGuardDecision = nil })

		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "src/repeated.txt", edited))
		assert.Contains(t, requireDenied(t, err), "resource-limit")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardDeny, rec.Guard.Decision)
		assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause, "the cut evaluation is unknown, never a violation")
		assert.Contains(t, rec.Guard.Reasons, lessoncheck.ReasonResourceLimit)
		assert.Empty(t, pendingNames(t, root), "a deny writes no pending record")
		sentinel.requireNoCall(t)
	})

	t.Run("a check pin stays advisory", func(t *testing.T) {
		root, sentinel := guardFixture(t)
		verifyWrite(t, root, ".seamark/lessons.yaml", strings.ReplaceAll(guardLessonsYAML, "mode: enforce", "mode: check"))
		commitRepeatedLines(t, root, lines)

		wait := shortHookBudgets(t)
		beforeGuardDecision = func() { time.Sleep(wait) }

		t.Cleanup(func() { beforeGuardDecision = nil })

		_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "src/repeated.txt", edited))
		require.NoError(t, err, "a check pin never denies, also when its evaluation is cut")

		rec := lastRecord(t, root, lessoncheck.AuditGuard)
		assert.Equal(t, lessoncheck.GuardAllow, rec.Guard.Decision)
		sentinel.requireNoCall(t)
	})
}

// requireCaseInsensitive skips the test when the file system of root
// treats names that differ only by case as one name. The case-alias
// tests have no meaning on a case-sensitive file system, where such
// names are different files.
func requireCaseInsensitive(t *testing.T, root string) {
	t.Helper()

	if _, err := os.Lstat(filepath.Join(root, "GENERATED")); err != nil {
		t.Skip("the file system of the fixture is case-sensitive")
	}
}

// TestLessonsGuardHookCaseAliases checks R-2: a client path that
// differs from the on-disk spelling by case reaches the same file, so
// it needs the same decision and the same audited path as the on-disk
// spelling.
func TestLessonsGuardHookCaseAliases(t *testing.T) {
	root0, _ := guardFixture(t)
	requireCaseInsensitive(t, root0)

	for _, tc := range []struct {
		name    string
		client  string
		payload func(t *testing.T, root string) string
		denied  bool
		paths   []string
	}{
		{"write to an aliased directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "GENERATED/a.txt", "x\n")
		}, true, []string{"generated/a.txt"}},
		{"write to an aliased file", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "generated/A.TXT", "x\n")
		}, true, []string{"generated/a.txt"}},
		{"new file in an aliased directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "GENERATED/New.txt", "x\n")
		}, true, []string{"generated/New.txt"}},
		{"new file below a new directory in an aliased directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "Generated/Sub/New.txt", "x\n")
		}, true, []string{"generated/Sub/New.txt"}},
		{"pattern edit in an aliased directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeEditEvent(t, root, "SRC/Handler.go", "func Handle() {}", "// TODO(x): y\nfunc Handle() {}")
		}, true, []string{"src/handler.go"}},
		{"aliased parent step", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "DOCS/../GENERATED/a.txt", "x\n")
		}, true, []string{"generated/a.txt"}},
		{"aliased repository root", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeGuardEvent(t, root, "Write", guardToolUse, map[string]any{
				"file_path": strings.ToUpper(root) + "/generated/a.txt", "content": "x\n",
			})
		}, true, []string{"generated/a.txt"}},
		{"aliased working directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeGuardEvent(t, strings.ToUpper(root), "Write", guardToolUse, map[string]any{
				"file_path": "GENERATED/a.txt", "content": "x\n",
			})
		}, true, []string{"generated/a.txt"}},
		{"free path in an aliased directory", integration.ClaudeID, func(t *testing.T, root string) string {
			return claudeWriteEvent(t, root, "DOCS/README.md", "x\n")
		}, false, []string{"docs/readme.md"}},
		{"patch delete", integration.CodexID, func(t *testing.T, root string) string {
			return codexPatchEvent(t, root, "*** Delete File: GENERATED/a.txt\n")
		}, true, []string{"generated/a.txt"}},
		{"patch move source", integration.CodexID, func(t *testing.T, root string) string {
			return codexPatchEvent(t, root, "*** Update File: GENERATED/A.txt\n*** Move to: free/moved.txt\n")
		}, true, []string{"free/moved.txt", "generated/a.txt"}},
		{"patch move destination", integration.CodexID, func(t *testing.T, root string) string {
			return codexPatchEvent(t, root, "*** Update File: DOCS/readme.md\n*** Move to: GENERATED/moved.txt\n")
		}, true, []string{"docs/readme.md", "generated/moved.txt"}},
		{"patch add", integration.CodexID, func(t *testing.T, root string) string {
			return codexPatchEvent(t, root, "*** Add File: Generated/b.txt\n+x\n")
		}, true, []string{"generated/b.txt"}},
		{"patch update with a hunk", integration.CodexID, func(t *testing.T, root string) string {
			return codexPatchEvent(t, root, "*** Update File: GENERATED/a.txt\n@@\n-asset\n+x\n")
		}, true, []string{"generated/a.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, sentinel := guardFixture(t)

			_, _, err := guardHook(t, root, tc.payload(t, root), "--client", tc.client)
			if tc.denied {
				require.ErrorIs(t, err, gate.ErrBlocked)
			} else {
				require.NoError(t, err)
			}

			rec := lastRecord(t, root, lessoncheck.AuditGuard)
			assert.Equal(t, tc.paths, rec.Paths, "the audit names the on-disk spelling")
			assert.True(t, rec.PathsComplete)

			if tc.denied {
				assert.Equal(t, lessoncheck.GuardCauseViolation, rec.Guard.Cause, "the pin decides, not a fail-closed rule")
			}

			sentinel.requireNoCall(t)
		})
	}
}

// TestLessonsGuardHookCaseAliasUnreadableDirectory checks that the
// guard denies when it cannot read the directory that settles the
// on-disk spelling of an aliased name.
func TestLessonsGuardHookCaseAliasUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	root, sentinel := guardFixture(t)
	requireCaseInsensitive(t, root)

	locked := filepath.Join(root, "generated")
	require.NoError(t, os.Chmod(locked, 0o311))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, _, err := guardHook(t, root, claudeWriteEvent(t, root, "GENERATED/a.txt", "x\n"))
	require.ErrorIs(t, err, gate.ErrBlocked)

	rec := lastRecord(t, root, lessoncheck.AuditGuard)
	assert.False(t, rec.PathsComplete, "an unsettled spelling is an incomplete path set")
	assert.Equal(t, lessoncheck.GuardCauseUnknown, rec.Guard.Cause)
	assert.Contains(t, rec.Guard.Reasons, lessoncheck.ReasonIncompletePaths)
	sentinel.requireNoCall(t)
}

// TestLessonsBaselineHookCaseAliases checks that the baseline handler
// stores the baseline of the on-disk path, so the post-edit run of the
// same event finds it.
func TestLessonsBaselineHookCaseAliases(t *testing.T) {
	root, sentinel := guardFixture(t)
	requireCaseInsensitive(t, root)

	_, _, err := baselineHook(t, root, claudeEditEvent(t, root, "SRC/Handler.go", "func Handle() {}", "// TODO(x): y\nfunc Handle() {}"))
	require.NoError(t, err)

	rec := lastRecord(t, root, lessoncheck.AuditBaseline)
	assert.Equal(t, []string{"src/handler.go"}, rec.Paths)

	p := pendingOf(t, root, integration.ClaudeID)
	require.Len(t, p.Paths, 1)
	assert.Equal(t, "src/handler.go", p.Paths[0].Path)
	sentinel.requireNoCall(t)
}
