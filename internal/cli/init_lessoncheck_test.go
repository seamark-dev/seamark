package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The tests of `seamark init --lesson-checks` and
// `--lesson-exploration`. Every root is a temporary directory: a test
// never installs a hook in the checkout.

// initLessonRun runs init with the lesson-check flags through the shared
// path with a temporary executable. Without a client it is the form without
// --client.
func initLessonRun(t *testing.T, root, binary string, run initRun, clients ...string) (string, error) {
	t.Helper()

	var b testWriter

	run.w, run.root, run.bin = &b, root, binary

	var err error

	if len(clients) > 0 {
		err = runInitClients(run, clients, false)
	} else {
		err = runInitLegacy(run, "")
	}

	return b.String(), err
}

// initLessons is initLessonRun with only the two lesson-check flags.
func initLessons(t *testing.T, root, binary, checks, exploration string, clients ...string) string {
	t.Helper()

	out, err := initLessonRun(t, root, binary, initRun{lessonChecks: checks, lessonExploration: exploration}, clients...)
	require.NoError(t, err)

	return out
}

// lessonMode reads the installed lesson-check mode of one client.
func lessonMode(t *testing.T, root, clientID string) string {
	t.Helper()

	if clientID == integration.CodexID {
		document, err := hooks.ParseDocument(mustRead(t, root, ".codex/hooks.json"))
		require.NoError(t, err)

		return hooks.InstalledLessonChecks(document, hooks.CodexLessonCheckSpecs(hooks.LessonChecksEnforce))
	}

	return hooks.InstalledLessonChecks(readSettings(t, root), hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksEnforce))
}

func TestRunInitLessonChecksInstallsAndPreserves(t *testing.T) {
	binary := capabilitySeamark(t)
	root := t.TempDir()

	// A fresh init installs nothing new, and says nothing of it.
	out := initLessons(t, root, binary, "", "")
	assert.Equal(t, hooks.LessonChecksOff, lessonMode(t, root, integration.ClaudeID))
	assert.NotContains(t, out, "lesson checks")
	assert.NotContains(t, out, "lesson-check")
	assert.NotContains(t, out, "--explore")
	assert.Len(t, commands(t, readSettings(t, root)), 2, "the gate and the lessons hook only")

	out = initLessons(t, root, binary, hooks.LessonChecksCheck, "")
	assert.Equal(t, hooks.LessonChecksCheck, lessonMode(t, root, integration.ClaudeID))
	assert.Contains(t, out, "  updated .claude/settings.json (lesson-check hooks)\n")
	assert.Contains(t, out, "          PreToolUse Edit|Write|MultiEdit "+binary+" lessons --baseline --hook\n")
	assert.Contains(t, out, "          PostToolUseFailure Edit|Write|MultiEdit "+binary+" lessons --verify --hook\n")
	// The reviewed Claude Code recording shows that the client waits for
	// a slow pre-write hook of Edit and Write. So the baseline paths of
	// these tools read active, with the timeout limit of the client.
	assert.Contains(t, out, "  lesson checks  claude check — "+
		"pre-write Edit active (limit: the client lets the write run after its 10 s hook timeout); "+
		"pre-write Write active (limit: the client lets the write run after its 10 s hook timeout); "+
		"pre-write MultiEdit active-unverified; post-edit Edit active-unverified; post-edit Write active-unverified; "+
		"post-edit MultiEdit active-unverified\n")
	assert.Contains(t, out, "  gate    warn — verdicts are reported, nothing blocks")
	assert.Contains(t, out, uncheckedLocalNote, "the lesson rule reads the personal file, the gate line still leaves it out")
	assert.Equal(t, gateModeWarn, installedGateMode(readSettings(t, root)), "a lesson-check flag never changes the gate")

	// An omitted flag keeps the installed choice and prints no line.
	before := snapshot(t, root)
	out = initLessons(t, root, binary, "", "")
	assert.Equal(t, before, snapshot(t, root))
	assert.Contains(t, out, "  kept    .claude/settings.json (seamark hooks already wired)\n")
	assert.Contains(t, out, "          PreToolUse Edit|Write|MultiEdit "+binary+" lessons --baseline --hook\n")
	assert.NotContains(t, out, "  lesson checks  ")

	// The gate mode and the lesson-check mode stay independent.
	_, err := initLessonRun(t, root, binary, initRun{gateMode: gateModeEnforce})
	require.NoError(t, err)
	assert.Equal(t, hooks.LessonChecksCheck, lessonMode(t, root, integration.ClaudeID))

	out = initLessons(t, root, binary, hooks.LessonChecksEnforce, "", integration.ClaudeID)
	assert.Equal(t, hooks.LessonChecksEnforce, lessonMode(t, root, integration.ClaudeID))
	assert.Equal(t, gateModeEnforce, installedGateMode(readSettings(t, root)))
	assert.Contains(t, out, "  lesson checks  claude enforce — pre-write Edit active (limit: the client lets the write run "+
		"after its 10 s hook timeout); pre-write Write active (limit: ")
	assert.Contains(t, out, "pre-write MultiEdit active-unverified (native-deny-unverified, ")
	assert.Contains(t, out, "  gate    enforce — ")

	_, err = initLessonRun(t, root, binary, initRun{gateMode: gateModeWarn})
	require.NoError(t, err)
	assert.Equal(t, hooks.LessonChecksEnforce, lessonMode(t, root, integration.ClaudeID))
	assert.Equal(t, gateModeWarn, installedGateMode(readSettings(t, root)))

	// A preview writes nothing and still names the result.
	before = snapshot(t, root)
	out, err = initLessonRun(t, root, binary, initRun{lessonChecks: hooks.LessonChecksCheck, printOnly: true})
	require.NoError(t, err)
	assert.Equal(t, before, snapshot(t, root))
	assert.Contains(t, out, "  would update .claude/settings.json (lesson-check hooks)\n")
	assert.Contains(t, out, "  lesson checks  claude check — ")

	// Codex gets its own hooks in hooks.json, and setup never grants
	// trust: the line names the unknown trust. The reviewed Codex
	// recording verifies the native evidence of apply_patch, so the line
	// also names the timeout limit of the client.
	root = t.TempDir()
	out = initLessons(t, root, binary, hooks.LessonChecksCheck, "", integration.CodexID)
	assert.Equal(t, hooks.LessonChecksCheck, lessonMode(t, root, integration.CodexID))
	assert.Contains(t, out, "          PreToolUse apply_patch         "+binary+" lessons --baseline --hook --client codex\n")
	assert.Contains(t, out, "          PostToolUse apply_patch        "+binary+" lessons --verify --hook --client codex\n")
	assert.Contains(t, out, "setup never grants trust")
	assert.Contains(t, out, "  lesson checks  codex check — pre-write apply_patch active-unverified (trust-unknown) "+
		"(limit: the client lets the write run after its 10 s hook timeout); "+
		"post-edit apply_patch active-unverified (trust-unknown)\n")

	// The form without --client installs hooks for Claude Code only, so
	// it never touches the Codex hooks.
	codexHooks := string(mustRead(t, root, ".codex/hooks.json"))
	initLessons(t, root, binary, hooks.LessonChecksOff, "")
	assert.Equal(t, codexHooks, string(mustRead(t, root, ".codex/hooks.json")))
}

func TestRunInitLessonChecksRefusesUnsupportedEnforce(t *testing.T) {
	binary := capabilitySeamark(t)
	// A client without a pre-write guard: the refusal names the client,
	// the tools, and the contexts, before any write.
	codex, _ := integration.Builtin().Lookup(integration.CodexID)
	codex.SetupOps.LessonGuard = false

	reg, err := integration.NewRegistry(codex)
	require.NoError(t, err)

	setups, err := integration.ExplicitSetups(reg, []string{integration.CodexID}, false, false, "")
	require.NoError(t, err)

	root := t.TempDir()

	var b testWriter

	refused := initRun{w: &b, root: root, bin: binary}
	err = applyInit(refused, reg, integration.WithLessonChecks(setups, hooks.LessonChecksEnforce, ""), selectedNotes{reg: reg})
	require.Error(t, err)
	assert.Equal(t, "lesson checks: enforce is not supported for codex (no pre-write guard for apply_patch in root and child); "+
		"choose --lesson-checks check for check-only behavior or omit the client", err.Error())
	assert.Empty(t, files(t, root), "nothing was written, not even a scaffold")
	assert.Empty(t, b.String())

	// A known-bad state refuses with its reason, and setup never
	// substitutes check-only behavior.
	root = t.TempDir()
	require.NoError(t, inspecttest.Named("disabled").Write(root))

	before := snapshot(t, root)
	_, err = initLessonRun(t, root, binary, initRun{lessonChecks: hooks.LessonChecksEnforce}, integration.CodexID)
	require.Error(t, err)
	assert.Equal(t, "lesson checks: enforce for codex would stay inactive (hooks-disabled); .codex/config.toml: "+
		"set [features] hooks = true, or remove the key, to run the hooks; then rerun", err.Error())
	assert.Equal(t, before, snapshot(t, root))

	// Claude Code disableAllHooks in either project settings file refuses
	// the same way.
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json"} {
		root = t.TempDir()
		require.NoError(t, inspecttest.WriteFile(root, rel, `{"disableAllHooks": true}`))

		before = snapshot(t, root)
		_, err = initLessonRun(t, root, binary, initRun{lessonChecks: hooks.LessonChecksEnforce}, integration.ClaudeID)
		require.Error(t, err, rel)
		assert.Equal(t, "lesson checks: enforce for claude would stay inactive (hooks-disabled); "+rel+
			": remove disableAllHooks, or set it to false, to run the hooks; then rerun", err.Error(), rel)
		assert.Equal(t, before, snapshot(t, root), rel)
	}

	// A configuration that does not validate refuses through the real
	// command too.
	root = writeFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte("lesson_checks_version: 9\n"), 0o644))

	before = snapshot(t, root)
	_, err = run(t, "-C", root, "init", "--lesson-checks", "enforce")
	require.Error(t, err)
	assert.Equal(t, "lesson checks: .seamark/lessons.yaml does not validate: "+
		"lesson_checks_version 9 is not supported; this seamark reads up to 1; fix it before enabling enforce", err.Error())
	assert.Equal(t, before, snapshot(t, root))
}

func TestRunInitLessonChecksFlagValues(t *testing.T) {
	binary := capabilitySeamark(t)
	root := writeFixture(t)
	before := snapshot(t, root)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--lesson-checks", "strict"}, `--lesson-checks takes off, check, or enforce, got "strict"`},
		{[]string{"--lesson-checks="}, `--lesson-checks takes off, check, or enforce, got ""`},
		{[]string{"--lesson-checks", "Enforce"}, `--lesson-checks takes off, check, or enforce, got "Enforce"`},
		{[]string{"--lesson-exploration", "yes"}, `--lesson-exploration takes off or on, got "yes"`},
		{[]string{"--lesson-exploration="}, `--lesson-exploration takes off or on, got ""`},
	} {
		_, err := run(t, append([]string{"-C", root, "init"}, tc.args...)...)
		require.Error(t, err, tc.args)
		assert.Equal(t, tc.want, err.Error(), tc.args)
	}

	assert.Equal(t, before, snapshot(t, root), "a value error writes nothing")

	for _, args := range [][]string{
		{"--lesson-checks", "off"}, {"--lesson-checks", "check"}, {"--lesson-checks", "enforce"},
		{"--lesson-exploration", "off"}, {"--lesson-exploration", "on"},
	} {
		_, err := run(t, append([]string{"-C", root, "init", "--print"}, args...)...)
		require.NoError(t, err, args)
	}

	assert.Equal(t, before, snapshot(t, root), "--print writes nothing")

	// Exploration for a client without the structured Read path: no
	// failure, the client is named, and nothing is installed for it.
	root = t.TempDir()
	out := initLessons(t, root, binary, "", hooks.LessonExplorationOn, integration.ClaudeID, integration.CodexID)
	assert.Contains(t, out, "  note    lesson exploration: nothing was installed for codex (no structured Read path); "+
		"preparation (seamark lessons --prepare) covers it\n")
	assert.NotContains(t, out, "nothing was installed for claude")
	assert.NotContains(t, string(mustRead(t, root, ".codex/hooks.json")), "--explore")
	assert.Contains(t, string(mustRead(t, root, ".claude/settings.json")), ""+binary+" lessons --explore --hook")
	assert.Contains(t, out, "  lesson checks  claude off — no pre-write or post-edit hook installed; exploration Read active-unverified\n")
	assert.NotContains(t, out, "  lesson checks  codex", "the notice is the only line for a client that gets nothing")
}

func TestRunInitLessonChecksOffIsTheRollback(t *testing.T) {
	binary := capabilitySeamark(t)
	root := t.TempDir()
	clients := []string{integration.ClaudeID, integration.CodexID}

	initLessons(t, root, binary, "", "", clients...)

	before := snapshot(t, root)

	out := initLessons(t, root, binary, hooks.LessonChecksEnforce, hooks.LessonExplorationOn, clients...)
	assert.Equal(t, hooks.LessonChecksEnforce, lessonMode(t, root, integration.ClaudeID))
	assert.Equal(t, hooks.LessonChecksEnforce, lessonMode(t, root, integration.CodexID))
	assert.Contains(t, out, "  lesson checks  claude enforce — ")
	assert.Contains(t, out, "  lesson checks  codex enforce — ")

	out = initLessons(t, root, binary, hooks.LessonChecksOff, hooks.LessonExplorationOff, clients...)
	assert.Contains(t, out, "  updated .claude/settings.json (removed 3 lesson-check hooks; removed the exploration hook)\n")
	assert.Contains(t, out, "  updated .codex/hooks.json (removed 2 lesson-check hooks)\n")
	assert.Contains(t, out, "  lesson checks  claude off — no pre-write or post-edit hook installed\n")
	assert.Contains(t, out, "  lesson checks  codex off — no pre-write or post-edit hook installed\n")

	// Every file, also the gate, lessons, and reset entries, has the bytes
	// it had before the first lesson-check request.
	assert.Equal(t, before, snapshot(t, root))

	assert.Equal(t, hooks.LessonChecksOff, lessonMode(t, root, integration.ClaudeID))
	assert.Equal(t, hooks.LessonChecksOff, lessonMode(t, root, integration.CodexID))

	// A second rollback has nothing to remove.
	out = initLessons(t, root, binary, hooks.LessonChecksOff, hooks.LessonExplorationOff, clients...)
	assert.Equal(t, before, snapshot(t, root))
	assert.Contains(t, out, "  kept    .claude/settings.json (seamark hooks already wired)\n")
}

func TestRunInitLessonChecksCallsNoInvoker(t *testing.T) {
	// Setup of the lesson-check and exploration hooks starts no agent, no
	// model, and no network request.
	root := writeFixture(t)
	sentinel := newInvokerSentinel(t, root)

	_, err := run(t, "-C", root, "init", "--lesson-checks", "enforce")
	require.NoError(t, err)

	_, err = run(t, "-C", root, "init", "--lesson-exploration", "on")
	require.NoError(t, err)

	_, err = run(t, "-C", root, "init", "--client", "codex", "--lesson-checks", "check", "--lesson-exploration", "on")
	require.NoError(t, err)

	sentinel.requireNoCall(t)
	assert.Contains(t, string(mustRead(t, root, ".claude/settings.json")), "lessons --guard --hook")
	assert.Contains(t, string(mustRead(t, root, ".claude/settings.json")), "lessons --explore --hook")
	assert.Contains(t, string(mustRead(t, root, ".codex/hooks.json")), "lessons --baseline --hook --client codex")
}

// TestRunInitLessonChecksNarratesLocalHookSources proves that
// `init --lesson-checks` reads the personal hook file in both modes,
// with and without --client. A local file that does not parse makes the
// path inactive, so the init line must not say active-unverified while
// status says inactive. A wrapper that discards the exit status of the
// pre-write hook is a second handler, so setup refuses it.
func TestRunInitLessonChecksNarratesLocalHookSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	binary := capabilitySeamark(t)

	const wrapper = `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[` +
		`{"type":"command","command":"/x/seamark lessons --baseline --hook || true"}]}]}}`

	for name, tc := range map[string]struct{ local, reason string }{
		"local file that does not parse": {"{bad", "document-unreadable"},
		"local report-only wrapper":      {wrapper, "report-only-wrapper"},
	} {
		for _, clients := range [][]string{{integration.ClaudeID}, nil} {
			for _, mode := range []string{hooks.LessonChecksCheck, hooks.LessonChecksEnforce} {
				// Enforce refuses a report-only wrapper for its own reason
				// before this check runs, so only the check mode reaches it.
				if mode == hooks.LessonChecksEnforce && tc.reason == "report-only-wrapper" {
					continue
				}

				t.Run(fmt.Sprintf("%s clients=%v mode=%s", name, clients, mode), func(t *testing.T) {
					root := t.TempDir()
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(tc.local), 0o644))

					out, err := initLessonRun(t, root, binary, initRun{lessonChecks: mode}, clients...)
					if tc.reason == "report-only-wrapper" {
						require.ErrorContains(t, err, "setup refuses a second handler")

						return
					}

					require.NoError(t, err)

					report := integration.LessonCapability(integration.Builtin().Inspect(root), root, integration.SelfIdentity(""))
					claude, ok := report.Client(integration.ClaudeID)
					require.True(t, ok)

					status := claude.StageText(lessoncheck.StagePreWrite)
					require.Contains(t, status, "inactive ("+tc.reason, "status reads the same local file")

					assert.NotContains(t, out, "pre-write Edit active-unverified", "init must not claim coverage that status denies")
					assert.Contains(t, out, "pre-write Edit inactive ("+tc.reason, "init narrates what status reports")
				})
			}
		}
	}

	t.Run("plain init without a lesson-check mode never reads the local file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte("{bad"), 0o644))

		_, err := initLessonRun(t, root, binary, initRun{})
		require.NoError(t, err, "a broken personal file is none of a plain init's business")
	})
}

func TestRunInitAcceptsAVerbatimLocalGuardCopy(t *testing.T) {
	// Claude Code runs a handler that both settings files define with
	// equal fields once. So plain init keeps working beside such a copy.
	binary := capabilitySeamark(t)
	root := t.TempDir()

	initLessons(t, root, binary, hooks.LessonChecksEnforce, "", integration.ClaudeID)

	var guard map[string]any

	entries, _ := readSettings(t, root)["hooks"].(map[string]any)["PreToolUse"].([]any)
	hooks.ForEachCommand(entries, func(_ string, h map[string]any, command string) {
		if hooks.OwnedBySeamark(command, []string{hooks.LessonGuardMarker}) {
			guard = h
		}
	})
	require.NotNil(t, guard)

	local, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "Edit|Write|MultiEdit", "hooks": []any{guard}},
	}}})
	require.NoError(t, err)
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.local.json", string(local)))

	before := snapshot(t, root)

	_, err = initLessonRun(t, root, binary, initRun{})
	require.NoError(t, err, "plain init accepts the copy")
	assert.Equal(t, hooks.LessonChecksEnforce, lessonMode(t, root, integration.ClaudeID))
	assert.Equal(t, before, snapshot(t, root), "the run keeps every file")
}
