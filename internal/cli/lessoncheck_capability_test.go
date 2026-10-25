package cli

// Tests of the K10 capability join in the CLI (plan step P4.8): the
// active lines of lessons --set-mode, the enforcement badges of lessons
// --prepare, and the AC-1 proof of doctor, status, and --set-mode. Every
// root is a temporary directory. The hook binary is a link named
// seamark to the test binary, so the report reads it as the running
// binary and never runs it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// capabilitySeamark returns a link named seamark to the running test
// binary. A hook command that names it is owned, because ownership
// reads the basename.
func capabilitySeamark(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(exe, link))

	return link
}

// capabilityInstall runs the hook setup of each client on root with the
// link as the hook binary. The setup runs in the test process on a
// temporary root only.
func capabilityInstall(t *testing.T, root string, setups ...integration.ClientSetup) {
	t.Helper()

	for i := range setups {
		setups[i].Hooks = true
	}

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
		Root: root, Binary: capabilitySeamark(t), Clients: setups,
	})
	require.NoError(t, err)

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)
}

// The K10 active lines of the spec example pin under Claude Code enforce
// hooks and no Codex hooks.
const (
	activeEnforceBlock  = "  active: requested; claude enforced-unverified, codex requested-inactive\n"
	activeEnforceClaude = "active: claude enforced-unverified (native-deny-unverified, native-timeout-unverified) · " +
		"pre-write Edit, Write, MultiEdit active-unverified\n"
	activeCodexInactive = "active: codex requested; inactive: not-installed\n"
)

// TestLessonsSetModeShowsActiveState proves K6 step 6 from milestone P4
// (§5 row 41): the active state is the K10 join of the file that the
// command wrote, per client, hook stage, and tool, in the impact block
// and in the active lines. A client that no handler covers reads
// "requested; inactive: <reasons>". The fixed text of milestone P1 is
// gone, and a root without handlers keeps its words.
func TestLessonsSetModeShowsActiveState(t *testing.T) {
	t.Run("claude enforce hooks, no codex hooks", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)
		capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})

		stdout, stderr := goldenRun(t, root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef,
			"--check", "frozen-assets")
		assert.Empty(t, stderr)
		assert.True(t, strings.HasPrefix(stdout, setModeBlockHead(t)+activeEnforceBlock), stdout)
		assert.Contains(t, stdout, "\nrequested mode: enforce\n"+activeEnforceClaude+activeCodexInactive)
		assert.NotContains(t, stdout, "\nactive: "+lessoncheck.ActiveRequested+"\n", "the P1 fixed line is gone")

		stdout, _ = goldenRun(t, root, "lessons", "--set-mode", "check", "--pin", setModePinRef)
		assert.Contains(t, stdout, "\nrequested mode: check\n"+
			"active: claude checked · post-edit active-unverified\n"+activeCodexInactive)
		assert.Contains(t, stdout, "  active: requested; claude checked, codex requested-inactive\n")

		stdout, _ = goldenRun(t, root, "lessons", "--set-mode", "advisory", "--pin", setModePinRef)
		assert.Contains(t, stdout, "\nrequested mode: advisory\nactive: "+lessoncheck.ActiveAdvisory+"\n")

		sentinel.requireNoCall(t)
	})

	t.Run("a root without handlers", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)

		stdout, _ := goldenRun(t, root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef,
			"--check", "frozen-assets")
		assert.True(t, strings.HasPrefix(stdout, setModeBlockHead(t)+"  active: requested; inactive: not-installed\n"), stdout)
		assert.Contains(t, stdout, "\nrequested mode: enforce\n"+
			"active: claude requested; inactive: not-installed\n"+activeCodexInactive)

		sentinel.requireNoCall(t)
	})

	t.Run("the write gate off joins the candidate too", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, false)
		capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})

		stdout, _, err := runErr(t, "-C", root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef,
			"--check", "frozen-assets")
		require.Error(t, err, "nothing is written")
		assert.True(t, strings.HasPrefix(stdout, setModeBlockHead(t)+activeEnforceBlock),
			"the impact block shows the K10 state of the candidate:\n%s", stdout)

		content, _ := maintainLessons(t, root)
		assert.Equal(t, setModeYAML, content)

		sentinel.requireNoCall(t)
	})

	t.Run("a check-only install for an enforce pin", func(t *testing.T) {
		root, _ := setModeWorkspace(t, setModeYAML, true)
		capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksCheck})

		stdout, _ := goldenRun(t, root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef,
			"--check", "frozen-assets")
		assert.Contains(t, stdout, "\nactive: claude requested; inactive: check-only-installed\n")
	})
}

// TestLessonsPrepareShowsEnforcement proves the L1 enforcement field
// from milestone P4: each check summary carries the K10 badge of every
// client, in the JSON and in the text, from the capability report of
// the workspace. Preparation computes no badge.
func TestLessonsPrepareShowsEnforcement(t *testing.T) {
	root := prepareRepo(t, false)
	asset := filepath.Join(root, "generated", "new.txt")

	inactive := []lessoncheck.CapReason{lessoncheck.CapNotInstalled}

	doc := prepareJSON(t, root, asset)
	require.Len(t, doc.Checks, 1)
	assert.Equal(t, []lessoncheck.PinClient{
		{Client: "claude", Badge: lessoncheck.BadgeRequestedInactive, Reasons: inactive},
		{Client: "codex", Badge: lessoncheck.BadgeRequestedInactive, Reasons: inactive},
	}, doc.Checks[0].Enforcement, "no handler is installed")

	capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})

	doc = prepareJSON(t, root, asset)
	require.Len(t, doc.Checks, 1)
	assert.Equal(t, []lessoncheck.PinClient{
		{Client: "claude", Badge: lessoncheck.BadgeEnforcedUnverified,
			Reasons: []lessoncheck.CapReason{lessoncheck.CapNativeDenyUnverified, lessoncheck.CapNativeTimeoutUnverified}},
		{Client: "codex", Badge: lessoncheck.BadgeRequestedInactive, Reasons: inactive},
	}, doc.Checks[0].Enforcement)

	cfg, errs := lessoncheck.LoadForCapability(root)
	report := integration.LessonCapabilityOf(integration.Builtin().Inspect(root), cfg, errs, integration.SelfIdentity(version))
	pin, ok := report.Pin(doc.Checks[0].PinRef)
	require.True(t, ok)
	assert.Equal(t, pin.Clients, doc.Checks[0].Enforcement, "the badges are the report's")

	text, err := prepareRun(t, root, asset)
	require.NoError(t, err)
	assert.Contains(t, text, "  enforcement claude enforced-unverified (native-deny-unverified, native-timeout-unverified) · "+
		"codex requested-inactive (not-installed)\n")

	t.Run("an advisory pin without a check has no summary", func(t *testing.T) {
		readme := prepareJSON(t, root, filepath.Join(root, "src", "handler.go"))
		assert.Empty(t, readme.Checks)
	})
}

// TestLessonCheckHealthSurfacesCallNoInvoker proves AC-1 for the K10
// surfaces of the CLI: doctor, status, status --json, and --set-mode
// read the capability report offline. The invoker sentinel records no
// call.
func TestLessonCheckHealthSurfacesCallNoInvoker(t *testing.T) {
	root := prepareRepo(t, true)
	maintainWriteConfig(t, root, maintainGateOn)

	sentinel := newInvokerSentinel(t, root)

	capabilityInstall(t, root,
		integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce},
		integration.ClientSetup{ClientID: integration.CodexID, LessonChecks: hooks.LessonChecksCheck})

	doctorOut, _, _ := runErr(t, "-C", root, "doctor")
	assert.Contains(t, doctorOut, "checks", "the doctor row prints")
	assert.Contains(t, doctorOut, "claude enforce, codex check")

	statusOut, err := run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, statusOut, "lesson checks  claude  enforce · pre-write Edit, Write, MultiEdit active-unverified")
	assert.Contains(t, statusOut, "               codex   check · pre-write apply_patch active-unverified")

	statusJSON, err := run(t, "-C", root, "status", "--json")
	require.NoError(t, err)

	var doc struct {
		LessonChecks *lessoncheck.CapabilityReport `json:"lesson_checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(statusJSON), &doc))
	require.NotNil(t, doc.LessonChecks)
	assert.Equal(t, lessoncheck.CapabilitySchema, doc.LessonChecks.Schema)

	cfg, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)

	parsed, _, err := lessoncheck.Parse(cfg)
	require.NoError(t, err)

	frozen := parsed.Pins[1].Ref

	setMode, err := run(t, "-C", root, "lessons", "--set-mode", "check", "--pin", frozen)
	require.NoError(t, err)
	assert.Contains(t, setMode, "active: claude checked · post-edit active-unverified\n")
	assert.Contains(t, setMode, "active: codex checked (trust-unknown) · post-edit active-unverified\n")

	prepared := prepareJSON(t, root, filepath.Join(root, "generated", "a.txt"))
	require.Len(t, prepared.Checks, 1)
	assert.NotEmpty(t, prepared.Checks[0].Enforcement)
	assert.Equal(t, delivery.SurfacePrepare, prepared.Surface)

	sentinel.requireNoCall(t)
}
