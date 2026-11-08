package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The tests of the doctor row "checks". Every root is a temporary
// workspace. The hook binary is a link named seamark to the test
// binary, so the report identifies it as the running binary without
// running it.

// lessonEnforceYAML holds one enforce pin with a forbid check.
const lessonEnforceYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    mode: enforce
    check: frozen
`

// lessonCheckYAML holds one check pin with a forbid check.
const lessonCheckYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    mode: check
    check: frozen
`

// selfSeamark returns a link named seamark to the running test binary.
// A hook command that names it is owned, and the report reads it as the
// running binary.
func selfSeamark(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(exe, link))

	return link
}

// installLessonChecks runs the lesson-check setup of each client on root.
func installLessonChecks(t *testing.T, root, binary string, setups ...integration.ClientSetup) {
	t.Helper()

	for i := range setups {
		setups[i].Hooks = true
	}

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{Root: root, Binary: binary, Clients: setups})
	require.NoError(t, err)

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)
}

// writeLessons writes .seamark/lessons.yaml under root.
func writeLessons(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(text), 0o600))
}

// checksRow runs doctor over the built-in clients and returns the row
// "checks", and false when doctor prints none.
func checksRow(t *testing.T, root, dbPath string) (Check, bool) {
	t.Helper()

	c, ok := byName(run(integration.Builtin(), root, dbPath, "test"))["checks"]

	return c, ok
}

// TestDoctorLessonChecksRow proves the lesson-check row of doctor: it
// is absent without lesson-check hooks and keys, so doctor keeps its
// bytes; it names the installed mode of each client, the pin counts,
// and the configuration state; it warns on a requested pin that no
// handler covers and on an invalid configuration, and fails only when a
// guard is installed and the configuration does not validate. A newer
// lesson_checks_version reads invalid with the note that advisory pins
// still deliver.
func TestDoctorLessonChecksRow(t *testing.T) {
	stubAgent(t)

	seamark := selfSeamark(t)

	t.Run("absent without lesson-check hooks and keys", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		installLessonChecks(t, root, seamark, integration.ClientSetup{ClientID: integration.ClaudeID})

		_, ok := checksRow(t, root, dbPath)
		assert.False(t, ok, "a plain setup prints no checks row")

		writeLessons(t, root, "pin:\n  - rule: plain\n    region: lib/\n    mode: advisory\n")

		_, ok = checksRow(t, root, dbPath)
		assert.False(t, ok, "advisory pins print no checks row")

		writeLessons(t, root, "pin: [\n")

		_, ok = checksRow(t, root, dbPath)
		assert.False(t, ok, "a file that does not decode alone prints no checks row")
	})

	t.Run("a requested pin that no handler covers warns and names the install", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, lessonEnforceYAML)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateWarn, row.State)

		// The reviewed Claude Code recording verifies the Edit and Write
		// paths, so the row names the timeout limit of that client.
		assert.Equal(t, "claude enforce, codex off · 1 enforce, 0 check, 0 advisory pin(s) · config valid · "+
			"claude pre-write limit: the client lets the write run after its 10 s hook timeout · "+
			"exploration inactive for codex", row.Detail)
		assert.Contains(t, row.Fix, "run `seamark init --client codex --lesson-checks enforce` to install the handlers")
	})

	t.Run("both clients covered is ok", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, lessonEnforceYAML)
		installLessonChecks(t, root, seamark,
			integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce},
			integration.ClientSetup{ClientID: integration.CodexID, LessonChecks: hooks.LessonChecksEnforce})

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateOK, row.State, "enforced-unverified is consistent: %s", row.Fix)
		assert.Equal(t, "claude enforce, codex enforce · 1 enforce, 0 check, 0 advisory pin(s) · config valid · "+
			"claude pre-write limit: the client lets the write run after its 10 s hook timeout · "+
			"exploration inactive for codex", row.Detail)
		assert.Empty(t, row.Fix)
	})

	t.Run("an installed handler without checks prints the row", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonExploration: hooks.LessonExplorationOn,
		})

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok, "an exploration hook prints the row")
		assert.Equal(t, StateOK, row.State)
		assert.Equal(t, "claude off, codex off · 0 enforce, 0 check, 0 advisory pin(s) · config valid · "+
			"exploration inactive for codex", row.Detail)
	})

	t.Run("an invalid configuration warns without a guard and fails with one", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, "lesson_checks_version: 1\nchecks:\n  bad:\n    kind: nope/1\npin:\n"+
			"  - rule: r\n    region: a/\n    mode: check\n    check: bad\n")

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateWarn, row.State)
		assert.Contains(t, row.Detail, "config invalid")
		assert.Contains(t, row.Fix, "fix .seamark/lessons.yaml; `seamark lessons --verify` lists every error")

		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksCheck,
		})

		row, _ = checksRow(t, root, dbPath)
		assert.Equal(t, StateWarn, row.State, "a baseline is no guard")

		// Setup refuses enforce on an invalid file, so the guard goes in
		// first and the file breaks after it.
		guarded, guardedDB := fixtureRoot(t)
		installLessonChecks(t, guarded, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		writeLessons(t, guarded, "pin: [\n")

		row, ok = checksRow(t, guarded, guardedDB)
		require.True(t, ok)
		assert.Equal(t, StateFail, row.State, "an armed guard denies every covered edit")
		assert.Contains(t, row.Fix, "until then the installed guard denies every covered edit")
	})

	t.Run("a newer lesson_checks_version reads invalid with the advisory note", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, "lesson_checks_version: 2\npin:\n  - rule: kept\n    region: a/\n")

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok, "a semantic error prints the row also without a checks map")
		assert.Equal(t, StateWarn, row.State)
		assert.Equal(t, "claude off, codex off · 0 enforce, 0 check, 1 advisory pin(s) · config invalid "+
			"(lesson_checks_version 2 is not supported; advisory pins still deliver) · exploration inactive for codex", row.Detail)

		// Setup refuses enforce on such a file, so the guard goes in first.
		writeLessons(t, root, lessonEnforceYAML)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		writeLessons(t, root, "lesson_checks_version: 2\npin:\n  - rule: kept\n    region: a/\n")

		row, _ = checksRow(t, root, dbPath)
		assert.Equal(t, StateFail, row.State)
	})

	t.Run("another hook binary warns and names the rerun", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)

		other := filepath.Join(t.TempDir(), "seamark")
		require.NoError(t, os.WriteFile(other, []byte("#!/bin/sh\nexit 0\n"), 0o755))

		installLessonChecks(t, root, other, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksCheck,
		})

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateWarn, row.State)
		assert.Contains(t, row.Fix, "run `seamark init --client claude` with the current binary, so the hooks name it")
	})
}

// dropOwnedHooks removes the owned hook entries that carry one of the
// markers from the Claude Code settings of root. It leaves every other
// entry, so the file stays valid JSON.
func dropOwnedHooks(t *testing.T, root string, markers ...string) {
	t.Helper()

	path := filepath.Join(root, ".claude", "settings.json")

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Positive(t, hooks.RemoveOwned(doc, markers, ""), "the markers name no installed entry")

	data, err = json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// TestDoctorLessonChecksPartialFix proves that the fix of a partial
// install repairs it. Setup repairs a half stage pair only when the
// request names a lesson-check mode, so a plain `init --client` leaves
// the install partial and doctor repeats the advice.
func TestDoctorLessonChecksPartialFix(t *testing.T) {
	stubAgent(t)

	seamark := selfSeamark(t)

	t.Run("a missing guard entry advises the wanted mode", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, lessonEnforceYAML)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		dropOwnedHooks(t, root, hooks.LessonGuardMarker)

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateWarn, row.State)
		assert.Contains(t, row.Detail, "claude partial")
		assert.Contains(t, row.Fix, "run `seamark init --client claude --lesson-checks enforce` to restore the missing lesson-check hook")
		assert.NotContains(t, row.Fix, "`seamark init --client claude`", "a plain init leaves the install partial")

		// The advised command repairs the install, and the advice ends.
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})

		row, ok = checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.NotContains(t, row.Detail, "partial")
		assert.NotContains(t, row.Fix, "restore the missing lesson-check hook")
	})

	t.Run("a missing post-edit entry advises the recorded mode", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksCheck,
		})
		dropOwnedHooks(t, root, hooks.LessonVerifyMarker)

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Contains(t, row.Detail, "claude partial")
		assert.Contains(t, row.Fix, "run `seamark init --client claude --lesson-checks check` to restore the missing lesson-check hook",
			"the surviving baseline entry records the mode, and no pin wants another")
	})

	t.Run("no recorded and no wanted mode names both choices", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		dropOwnedHooks(t, root, hooks.LessonGuardMarker)

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Contains(t, row.Fix, "--lesson-checks check` or `seamark init --client claude --lesson-checks enforce`")
	})
}

// TestDoctorLessonChecksUnreadableDocument proves the row of an
// unreadable hook document. The mode off would claim that no hook is
// installed, and an init cannot repair a file that does not parse. So
// the detail says unreadable, and the fix names the file.
func TestDoctorLessonChecksUnreadableDocument(t *testing.T) {
	stubAgent(t)

	seamark := selfSeamark(t)

	root, dbPath := fixtureRoot(t)
	writeLessons(t, root, lessonEnforceYAML)
	installLessonChecks(t, root, seamark, integration.ClientSetup{
		ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{ not json"), 0o600))

	row, ok := checksRow(t, root, dbPath)
	require.True(t, ok)
	assert.Equal(t, StateWarn, row.State)
	assert.Contains(t, row.Detail, "claude unreadable (.claude/settings.json: ")
	assert.NotContains(t, row.Detail, "claude off", "an unreadable file is not an absent hook")
	assert.Contains(t, row.Detail, "codex off", "a client without a document keeps the mode off")

	assert.Contains(t, row.Fix, "fix or remove .claude/settings.json")
	assert.NotContains(t, row.Fix, "--client claude", "no init repairs a file that does not parse")
	assert.Contains(t, row.Fix, "--client codex --lesson-checks enforce", "the other client keeps its install advice")

	t.Run("a supplemental source that is unreadable qualifies an installed mode", func(t *testing.T) {
		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, lessonEnforceYAML)
		installLessonChecks(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte("{ not json"), 0o600))

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Contains(t, row.Detail, "claude enforce (a hook source is unreadable)")
	})
}

// oldRelease is a release below lessoncheck.MinHandlerRelease.
const oldRelease = "v0.8.0"

// releaseSeamark builds a small program that names release the way the
// Makefile names a release, and returns its path. The version of a hook
// binary comes from its Go build information, and the reader of that
// information is private to package integration. So a real binary is the
// only way to reach this state from here. The build needs no module and
// no network.
func releaseSeamark(t *testing.T, release string) string {
	t.Helper()

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/oldseamark\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))

	binary := filepath.Join(t.TempDir(), "seamark")

	cmd := exec.Command("go", "build", "-o", binary,
		"-ldflags=-X github.com/seamark-dev/seamark/internal/cli.version="+release, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	return binary
}

// TestDoctorLessonChecksOlderBinary proves what the row does with a hook
// binary below the first release with a handler. The row warns and names
// the rerun for every binary that is not the running one. So those two
// facts cannot show the reading of the version, and a partial install
// shows it. An older binary runs no handler, so a check pin stays
// requested and the repair names its mode. A binary at the first handler
// release serves that pin, so the repair names both modes. The status
// block and the console name the older release, and this row does not.
func TestDoctorLessonChecksOlderBinary(t *testing.T) {
	older := releaseSeamark(t, oldRelease)
	first := releaseSeamark(t, lessoncheck.MinHandlerRelease)

	stubAgent(t)

	t.Run("the row warns and names the rerun", func(t *testing.T) {
		// The repository holds no pin, so the row has no other cause to warn.
		root, dbPath := fixtureRoot(t)
		installLessonChecks(t, root, older, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)
		assert.Equal(t, StateWarn, row.State, "the hooks of this binary run no handler")
		assert.Contains(t, row.Fix, "run `seamark init --client claude` with the current binary, so the hooks name it")
	})

	// partialRow returns the row of a repository with one check pin after
	// the guard entry of an enforce install goes. The post-edit entry
	// stays and can serve a check pin, so the version of the hook binary
	// alone decides whether the pin is served.
	partialRow := func(t *testing.T, binary string) Check {
		t.Helper()

		root, dbPath := fixtureRoot(t)
		writeLessons(t, root, lessonCheckYAML)
		installLessonChecks(t, root, binary, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce,
		})
		dropOwnedHooks(t, root, hooks.LessonGuardMarker)

		row, ok := checksRow(t, root, dbPath)
		require.True(t, ok)

		return row
	}

	t.Run("an older binary leaves the pin requested", func(t *testing.T) {
		assert.Contains(t, partialRow(t, older).Fix,
			"run `seamark init --client claude --lesson-checks check` to restore the missing lesson-check hook")
	})

	t.Run("a binary at the first handler release serves the pin", func(t *testing.T) {
		assert.Contains(t, partialRow(t, first).Fix,
			"run `seamark init --client claude --lesson-checks check` or `seamark init --client claude --lesson-checks enforce` "+
				"to restore the missing lesson-check hook")
	})
}

// TestDoctorLessonChecksNamesTheLimit proves that the row names the
// limit of a verified pre-write path, so the row never reads as full
// enforcement.
func TestDoctorLessonChecksNamesTheLimit(t *testing.T) {
	report := lessoncheck.CapabilityReport{Config: lessoncheck.ConfigReport{Valid: true}, Clients: []lessoncheck.ClientCapability{limitClient()}}

	got := lessonCheckDetail(report, &lessoncheck.Config{}, nil)
	assert.Contains(t, got, "claude pre-write limit: the client lets the write run after its 10 s hook timeout")
}

// limitClient is a client whose pre-write paths read active with a
// synthetic verified limit. It tests the narration only and gives no
// native proof.
func limitClient() lessoncheck.ClientCapability {
	v := lessoncheck.CapVerification{
		Level: lessoncheck.LevelVerified, ClientVersion: "synthetic-fixture", Surface: "fixture",
		Limit: "the client lets the write run after its 10 s hook timeout",
	}

	return lessoncheck.ClientCapability{Client: "claude", Installed: lessoncheck.InstalledEnforce, Paths: []lessoncheck.PathCapability{
		{Tool: "Write", HookStage: lessoncheck.StagePreWrite, Context: lessoncheck.ContextRoot, State: lessoncheck.PathActive, Verification: v},
		{Tool: "Write", HookStage: lessoncheck.StagePreWrite, Context: lessoncheck.ContextChild, State: lessoncheck.PathActive, Verification: v},
	}}
}
