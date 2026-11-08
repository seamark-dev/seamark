package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// The Codex gate commands of the test binary in the Claude Code form.
var (
	claudeFormEnforce = testBinary + " " + hooks.GateMarker(hooks.ModeEnforce)
	claudeFormWarn    = testBinary + " " + hooks.GateMarker(hooks.ModeWarn)
)

// codexHooksWith writes a hooks.json with the managed lessons hook and
// the given PreToolUse entries.
func codexHooksWith(t *testing.T, root string, entries ...string) {
	t.Helper()

	entries = append(entries, hookEntry("apply_patch", testBinary+" "+hooks.CodexLessonsMarker))
	writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+strings.Join(entries, ",")+`]}}`)
}

// gateCommands lists the gate commands of the planned hooks.json.
func gateCommands(t *testing.T, plan ClientPlan) []string {
	t.Helper()

	var out []string

	for _, cmd := range commandsIn(writeFor(t, plan, codexHooksFile), "PreToolUse") {
		if strings.Contains(cmd, " gate ") {
			out = append(out, cmd)
		}
	}

	return out
}

func TestCodexInspectionNamesAGateInTheClaudeCodeForm(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormEnforce))

	insp := codexSetup{}.Inspect(root)

	assert.Equal(t, hooks.ModeEnforce, insp.GateMode)
	assert.Equal(t, hooks.ModeEnforce, insp.ManagedGateMode)
	assert.Equal(t, hooks.ModeEnforce, codexSetup{}.ManagedGateMode(root))
	assert.Equal(t, "gate (enforce) + lessons hooks installed", insp.DescribeHooks())

	found := findingsAt(insp.Findings, codexHooksFile, "in the Claude Code form")
	require.Len(t, found, 1)
	assert.Equal(t, FindingWarning, found[0].Level)
	assert.Contains(t, found[0].Reason, "has `"+claudeFormEnforce+"`")
	assert.Contains(t, found[0].Reason, "Codex runs it beside the managed gate hook, in enforce mode")
	assert.Equal(t, "run `seamark init --client codex`, which replaces that handler with the managed gate hook; "+
		"then open the project in Codex and review the hooks with /hooks", found[0].Action)
}

func TestCodexInspectionReadsAGateOnlyInTheClaudeCodeForm(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", claudeFormEnforce))

	insp := codexSetup{}.Inspect(root)

	commands := entryOf(t, insp, CapabilityCommands)
	assert.Equal(t, StateCurrent, commands.State)
	assert.Equal(t, hooks.ModeEnforce, insp.GateMode)

	found := findingsAt(insp.Findings, codexHooksFile, "in the Claude Code form")
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Reason, "Codex runs it in enforce mode")
	assert.NotContains(t, found[0].Reason, "beside")
}

func TestCodexInspectionSaysBesideOnlyForACodexFormHandlerThatRuns(t *testing.T) {
	// A Codex-form handler that Codex cannot run is no managed gate hook
	// that the Claude Code form runs beside.
	cases := map[string]string{
		"a Codex form whose binary is missing": hookEntry("Bash", absentBinary+" "+hooks.CodexGateMarker(hooks.ModeWarn)),
	}

	for name, codexForm := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			codexHooksWith(t, root, codexForm, hookEntry("Bash", claudeFormWarn))

			found := findingsAt(codexSetup{}.Inspect(root).Findings, codexHooksFile, "in the Claude Code form")
			require.Len(t, found, 1)
			assert.Contains(t, found[0].Reason, "Codex runs it in warn mode")
			assert.NotContains(t, found[0].Reason, "beside")
		})
	}
}

func TestCodexInspectionOfTheClaudeCodeFormFollowsTheRouteThatRuns(t *testing.T) {
	// Codex setup stops at a config.toml that it cannot read, so the
	// replacement of the handler fixes that file first.
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", claudeFormWarn))
	writeRel(t, root, ".codex/config.toml", "[mcp_servers\n")

	found := findingsAt(codexSetup{}.Inspect(root).Findings, codexHooksFile, "in the Claude Code form")
	require.Len(t, found, 1)
	assert.True(t, strings.HasPrefix(found[0].Action,
		"fix the file, then re-run `seamark init --client codex`, which replaces that handler"), found[0].Action)
}

func TestCodexWrappedGateInTheClaudeCodeFormIsAnUnmanagedSource(t *testing.T) {
	root := t.TempDir()
	wrapped := "sh -c '" + claudeFormEnforce + "'"
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", wrapped))

	insp := codexSetup{}.Inspect(root)
	assert.Equal(t, hooks.ModeEnforce, insp.GateMode)
	assert.Equal(t, hooks.ModeWarn, insp.ManagedGateMode)
	assert.Len(t, findingsAt(insp.Findings, codexHooksFile, "runs twice"), 1)

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	assert.Contains(t, plan.GateHooks, GateHook{Path: codexHooksFile, Mode: hooks.ModeEnforce})
	assert.Empty(t, findingsWith(plan.Findings, "setup removed"))

	// Setup never edits a definition that it does not own.
	for _, w := range plan.Writes {
		assert.Contains(t, string(w.After), wrapped)
	}
}

func TestCodexSetupKeepsOneGateHandler(t *testing.T) {
	foreign := `{"matcher":"Bash","hooks":[{"type":"command","command":"my-linter"},` +
		`{"type":"command","command":"` + claudeFormEnforce + `"}]}`

	cases := map[string]struct {
		entries  []string
		mode     string
		gates    []string
		removed  int
		replaced int
	}{
		"both forms under Bash": {
			[]string{hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormWarn)},
			hooks.ModeWarn, []string{codexWarnGate}, 1, 0,
		},
		"only the Claude Code form": {
			[]string{hookEntry("Bash", claudeFormWarn)},
			hooks.ModeWarn, []string{codexWarnGate}, 0, 1,
		},
		"the Codex form never fires": {
			[]string{hookEntry("ash", codexWarnGate), hookEntry("Bash", claudeFormWarn)},
			hooks.ModeWarn, []string{codexWarnGate, codexWarnGate}, 0, 1,
		},
		"a warn and an enforce handler in the Claude Code form": {
			[]string{hookEntry("Bash", claudeFormWarn), hookEntry("Bash", claudeFormEnforce)},
			hooks.ModeEnforce, []string{codexEnforceGate}, 1, 1,
		},
		"a handler beside a foreign hook": {
			[]string{hookEntry("Bash", codexWarnGate), foreign},
			hooks.ModeEnforce, []string{codexEnforceGate}, 1, 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			codexHooksWith(t, root, tc.entries...)

			plan := planCodex(t, root, ClientSetup{Hooks: true})

			assert.Equal(t, tc.gates, gateCommands(t, plan))
			assert.Contains(t, plan.GateHooks, GateHook{Path: codexHooksFile, Mode: tc.mode, Managed: true})
			assert.Len(t, findingsWith(plan.Findings, "setup removed"), tc.removed)
			assert.Len(t, findingsWith(plan.Findings, "setup replaced"), tc.replaced)
			assert.Empty(t, findingsWith(plan.Findings, "Codex runs it"), "the plan prints its own findings")

			for _, f := range findingsWith(plan.Findings, "in the Claude Code form") {
				assert.Equal(t, FindingWarning, f.Level)
				assert.Equal(t, codexHooksFile, f.Path)
				assert.Equal(t, "open the project in Codex and review the hooks with /hooks", f.Action)
			}

			// The ash entry keeps its matcher: setup never changes one.
			if name == "the Codex form never fires" {
				assert.Equal(t, []any{"ash", "Bash", "apply_patch"}, matchersOf(writeFor(t, plan, codexHooksFile), "PreToolUse"))
			}

			if name == "a handler beside a foreign hook" {
				assert.Contains(t, commandsIn(writeFor(t, plan, codexHooksFile), "PreToolUse"), "my-linter")
			}
		})
	}
}

func TestCodexSetupNamesTheTrustReviewOfAReplacedHandler(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", claudeFormWarn))

	plan := planCodex(t, root, ClientSetup{Hooks: true})

	replaced := findingsWith(plan.Findings, "setup replaced")
	require.Len(t, replaced, 1)
	assert.Contains(t, replaced[0].Reason, "`"+claudeFormWarn+"`, the seamark gate hook in the Claude Code form, with `"+
		codexWarnGate+"`")
	assert.Contains(t, replaced[0].Reason, "the change alters the Codex trust hash, so Codex asks you to review that hook again")
}

func TestCodexSetupKeepsTheEnforcementOfAClaudeCodeFormHandler(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormEnforce))

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	assert.Equal(t, []string{codexEnforceGate}, gateCommands(t, plan))
	assert.Contains(t, plan.GateHooks, GateHook{Path: codexHooksFile, Mode: hooks.ModeEnforce, Managed: true})
	assert.NotContains(t, narrated(t, plan, codexHooksFile, OpApplied), "removed --enforce")

	warn := planCodex(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeWarn})
	assert.Equal(t, []string{codexWarnGate}, gateCommands(t, warn))
	assert.Contains(t, warn.GateHooks, GateHook{Path: codexHooksFile, Mode: hooks.ModeWarn, Managed: true})
	assert.Contains(t, narrated(t, warn, codexHooksFile, OpApplied), "  note    removed --enforce from the Codex gate hook")
}

func TestCodexSetupRemovesAWarnHandlerWithoutAnEnforceNote(t *testing.T) {
	// The removed Claude Code form handler ran in warn mode, so a warn run
	// takes no enforcement away.
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormWarn))

	plan := planCodex(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeWarn})

	assert.Equal(t, []string{codexWarnGate}, gateCommands(t, plan))
	assert.Len(t, findingsWith(plan.Findings, "setup removed"), 1)
	assert.NotContains(t, narrated(t, plan, codexHooksFile, OpApplied), "removed --enforce")
}

func TestCodexSetupAfterTheCleanupStaysByteForByte(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormEnforce))

	applyHooks(t, root, CodexID)
	before := readRel(t, root, codexHooksFile)
	assert.NotContains(t, before, claudeFormEnforce+`"`)

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, plan.Writes)
	assert.Empty(t, findingsWith(plan.Findings, "in the Claude Code form"))

	applyHooks(t, root, CodexID)
	assert.Equal(t, before, readRel(t, root, codexHooksFile))
}
