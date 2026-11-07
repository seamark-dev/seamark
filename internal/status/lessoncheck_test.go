package status

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests of the lesson checks block of status and of the
// lesson_checks key of status --json. Every root is a temporary
// workspace. The hook binary is a link named seamark to the test
// binary, so the report reads it as the running binary.

// statusLessonsYAML is the example: one enforce pin and four advisory
// pins.
const statusLessonsYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    mode: enforce
    check: frozen-assets
  - rule: a
    region: a/
  - rule: b
    region: b/
  - rule: c
    region: c/
  - rule: d
    region: d/
`

// statusSeamark returns a link named seamark to the running test binary.
func statusSeamark(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(exe, link))

	return link
}

// statusInstall runs the hook setup of one client on root.
func statusInstall(t *testing.T, root, binary string, setup integration.ClientSetup) {
	t.Helper()

	setup.Hooks = true

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
		Root: root, Binary: binary, Clients: []integration.ClientSetup{setup},
	})
	require.NoError(t, err)

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)
}

// statusText gathers status of the seeded workspace over the built-in
// clients as the running binary and returns it with its text and JSON.
func statusText(t *testing.T, st *store.Store, root string) (s *Status, text string, doc map[string]any) {
	t.Helper()

	s, err := gatherAs(integration.Builtin(), st, root, integration.SelfIdentity(""))
	require.NoError(t, err)

	var b bytes.Buffer
	Print(&b, s)

	data, err := json.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &doc))

	return s, b.String(), doc
}

// TestStatusLessonChecksBlockAndJSON proves the lesson checks block and
// the lesson_checks key of status --json. A repository without
// lesson-check hooks and keys keeps its text and JSON. The block
// follows the clients block, names each client with its installed mode
// and path states, reads exploration inactive for a client without the
// structured Read path, and ends with the pins line. The clients[] JSON
// gains no key.
func TestStatusLessonChecksBlockAndJSON(t *testing.T) {
	seamark := statusSeamark(t)

	t.Run("absent without lesson-check hooks and keys", func(t *testing.T) {
		st, root := seededStore(t)
		statusInstall(t, root, seamark, integration.ClientSetup{ClientID: integration.ClaudeID})

		s, text, doc := statusText(t, st, root)
		assert.Nil(t, s.LessonChecks)
		assert.NotContains(t, text, "lesson checks")
		assert.NotContains(t, doc, "lesson_checks")
	})

	t.Run("the lesson checks block", func(t *testing.T) {
		st, root := seededStore(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(statusLessonsYAML), 0o600))
		statusInstall(t, root, seamark, integration.ClientSetup{
			ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce, LessonExploration: hooks.LessonExplorationOn,
		})

		s, text, doc := statusText(t, st, root)
		require.NotNil(t, s.LessonChecks)

		block := "lesson checks  claude  enforce · pre-write Edit, Write, MultiEdit active-unverified · " +
			"post-edit active-unverified · exploration active-unverified\n" +
			"               codex   off · exploration inactive (no structured Read path)\n" +
			"               pins    1 enforce (requested-inactive on codex), 0 check, 4 advisory · config valid\n"
		assert.Contains(t, text, block)

		clients := strings.Index(text, "clients ")
		lessons := strings.Index(text, "lesson checks ")
		require.GreaterOrEqual(t, clients, 0, text)
		assert.Greater(t, lessons, clients, "the block follows the clients block")

		report, ok := doc["lesson_checks"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.CapabilitySchema, report["schema"])
		assert.Contains(t, report, "uncovered_writers")

		for _, c := range doc["clients"].([]any) {
			for key := range c.(map[string]any) {
				assert.NotContains(t, strings.ToLower(key), "lesson", "clients[] keeps its keys")
			}
		}

		codex, ok := s.LessonChecks.Client(integration.CodexID)
		require.True(t, ok)
		assert.False(t, codex.HasStage(lessoncheck.StageExploration))

		for _, p := range codex.Paths {
			if p.HookStage == lessoncheck.StageExploration {
				assert.Equal(t, lessoncheck.PathUnsupported, p.State, "the human word is inactive, the state is unsupported")
			}
		}
	})

	t.Run("a newer lesson_checks_version on the pins line", func(t *testing.T) {
		st, root := seededStore(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
			[]byte("lesson_checks_version: 2\npin:\n  - rule: kept\n    region: a/\n"), 0o600))

		_, text, doc := statusText(t, st, root)
		assert.Contains(t, text, "               pins    0 enforce, 0 check, 1 advisory · config invalid "+
			"(lesson_checks_version 2 is not supported; advisory pins still deliver)\n")

		report := doc["lesson_checks"].(map[string]any)
		assert.Equal(t, false, report["config"].(map[string]any)["valid"])
	})
}

// TestStatusLessonChecksUnreadableDocument proves that the block tells
// an unreadable hook document from a document without a hook. The text
// uses the words of doctor. The JSON keeps the closed installed mode and
// its keys, so a reader of the report sees no new value, and the path
// reason stays the machine-readable fact.
func TestStatusLessonChecksUnreadableDocument(t *testing.T) {
	seamark := statusSeamark(t)

	const pins = "               codex   off · exploration inactive (no structured Read path)\n"

	offLine := "lesson checks  claude  off · exploration inactive (not-installed)\n" + pins

	settings := func(root string) string { return filepath.Join(root, ".claude", "settings.json") }
	local := func(root string) string { return filepath.Join(root, ".claude", "settings.local.json") }
	write := func(t *testing.T, path, text string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}

	seed := func(t *testing.T) (*store.Store, string) {
		t.Helper()

		st, root := seededStore(t)
		write(t, filepath.Join(root, ".seamark", "lessons.yaml"), statusLessonsYAML)

		return st, root
	}

	enforce := integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce}

	t.Run("an absent document reads off", func(t *testing.T) {
		st, root := seed(t)
		require.NoFileExists(t, settings(root))

		_, text, _ := statusText(t, st, root)
		assert.Contains(t, text, offLine)
	})

	t.Run("a valid document without a lesson-check hook reads off", func(t *testing.T) {
		st, root := seed(t)
		statusInstall(t, root, seamark, integration.ClientSetup{ClientID: integration.ClaudeID})

		_, text, _ := statusText(t, st, root)
		assert.Contains(t, text, offLine)
	})

	t.Run("an installed document reads its mode", func(t *testing.T) {
		st, root := seed(t)
		statusInstall(t, root, seamark, enforce)

		_, text, _ := statusText(t, st, root)
		assert.Contains(t, text, "lesson checks  claude  enforce · pre-write Edit, Write, MultiEdit active-unverified · ")
		assert.NotContains(t, text, "unreadable")
	})

	t.Run("an unreadable document reads unreadable", func(t *testing.T) {
		st, root := seed(t)
		statusInstall(t, root, seamark, enforce)
		write(t, settings(root), "{ not json")

		s, text, doc := statusText(t, st, root)

		reason := s.Clients[0].HookDocumentError
		require.NotEmpty(t, reason)
		assert.True(t, strings.HasPrefix(reason, ".claude/settings.json: "), reason)

		assert.Contains(t, text, "lesson checks  claude  unreadable ("+reason+") · "+
			"pre-write Edit, Write, MultiEdit inactive (document-unreadable) · post-edit inactive (document-unreadable) · "+
			"exploration inactive (document-unreadable)\n"+pins)
		assert.NotContains(t, text, "lesson checks  claude  off")

		claude, ok := s.LessonChecks.Client(integration.ClaudeID)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.InstalledOff, claude.Installed, "the enum keeps its closed set")

		report := doc["lesson_checks"].(map[string]any)
		first := report["clients"].([]any)[0].(map[string]any)
		assert.Equal(t, "off", first["installed"], "the JSON report keeps the value")
		assert.ElementsMatch(t, []string{"client", "installed", "paths", "attributions"}, mapKeys(first), "the JSON report keeps its keys")
	})

	t.Run("an unreadable supplemental source qualifies the installed mode", func(t *testing.T) {
		st, root := seed(t)
		statusInstall(t, root, seamark, enforce)
		write(t, local(root), "{ not json")

		_, text, _ := statusText(t, st, root)
		assert.Contains(t, text, "lesson checks  claude  enforce (a hook source is unreadable) · pre-write ")
	})

	t.Run("an unreadable supplemental source beside no handler keeps off", func(t *testing.T) {
		st, root := seed(t)
		write(t, local(root), "{ not json")

		_, text, _ := statusText(t, st, root)
		assert.Contains(t, text, offLine, "no path reports the source, and doctor reads the same")
	})
}

// mapKeys lists the keys of a JSON object.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	return keys
}

// oldRelease is a release below lessoncheck.MinHandlerRelease.
const oldRelease = "v0.8.0"

// oldSeamark builds a small program that names oldRelease the way the
// Makefile names a release, and returns its path. The version of a hook
// binary comes from its Go build information, and the reader of that
// information is private to package integration. So a real binary is the
// only way to reach this state from here. The build needs no module and
// no network.
func oldSeamark(t *testing.T) string {
	t.Helper()

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/oldseamark\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))

	binary := filepath.Join(t.TempDir(), "seamark")

	cmd := exec.Command("go", "build", "-o", binary,
		"-ldflags=-X github.com/seamark-dev/seamark/internal/cli.version="+oldRelease, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	return binary
}

// TestStatusLessonChecksOlderBinary proves that the block reports a hook
// binary below the first release with a handler. Such a binary has no
// lesson-check command, so the installed hooks do nothing. The mode alone
// reads enforce, so only the path states tell the reader.
func TestStatusLessonChecksOlderBinary(t *testing.T) {
	binary := oldSeamark(t)

	st, root := seededStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(statusLessonsYAML), 0o600))
	statusInstall(t, root, binary, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})

	s, text, _ := statusText(t, st, root)

	assert.Contains(t, text, "lesson checks  claude  enforce · pre-write Edit, Write, MultiEdit inactive "+
		"(capability-version-old, native-deny-unverified, native-timeout-unverified, other-binary) · "+
		"post-edit inactive (capability-version-old, other-binary) · ")
	assert.Contains(t, text, "1 enforce (requested-inactive on claude, codex)", "an inactive path serves no enforce pin")

	require.NotNil(t, s.LessonChecks)

	claude, ok := s.LessonChecks.Client(integration.ClaudeID)
	require.True(t, ok)
	require.NotEmpty(t, claude.Paths)

	for _, p := range claude.Paths {
		if p.HookStage == lessoncheck.StagePreWrite {
			assert.Equal(t, oldRelease, p.BinaryVersion, "the report names the version that it read")
		}
	}
}

// TestStatusLessonChecksNamesTheLimit proves that the client line names
// the limit of its verified pre-write paths.
func TestStatusLessonChecksNamesTheLimit(t *testing.T) {
	got := lessonClientText(limitClient(), "")
	assert.Contains(t, got, "pre-write Write active (limit: the client lets the write run after its 10 s hook timeout)")
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
