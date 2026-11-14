package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/skills"
)

// initClients runs `init --client …` through the shared path with a
// fixed binary, the way runInit does for the form without --client.
func initClients(t *testing.T, root string, printOnly, installSkills, approveTools bool, gateMode string, clients ...string) string {
	t.Helper()

	var b testWriter

	run := initRun{w: &b, root: root, bin: "/bin/seamark", gateMode: gateMode, printOnly: printOnly, approveTools: approveTools}
	require.NoError(t, runInitClients(run, clients, installSkills))

	return b.String()
}

// files lists every file under root, slash-separated.
func files(t *testing.T, root string) []string {
	t.Helper()

	var out []string

	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}

		return err
	}))

	return out
}

// mustRead reads one repository-relative file.
func mustRead(t *testing.T, root, rel string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)

	return data
}

// snapshot maps every file under root to its content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, rel := range files(t, root) {
		out[rel] = string(mustRead(t, root, rel))
	}

	return out
}

func TestInitClientCodexTouchesOnlyCommonAndCodexArtifacts(t *testing.T) {
	root := t.TempDir()

	// A broken file of an unselected client is irrelevant.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{ broken"), 0o644))

	preview := initClients(t, root, true, true, true, "", "codex")
	assert.Equal(t, []string{".claude/settings.json"}, files(t, root), "--print writes nothing")
	assert.Contains(t, preview, "would write .codex/config.toml (registered seamark mcp; approved 5 tools")
	assert.Contains(t, preview, "would write .codex/hooks.json (gate + lessons hooks)")
	assert.Contains(t, preview, "(nothing was written — --print)")

	out := initClients(t, root, false, true, true, "", "codex")
	assert.Contains(t, out, "  wrote   .codex/config.toml (registered seamark mcp; approved 5 tools: orient, why, change_set, check, expand)")
	assert.Contains(t, out, "  wrote  .agents/skills/seamark-plan-change")

	// The gate and lesson hooks, with the exact commands that Codex runs.
	// No reset hook: a Codex reset clears nothing, and each hook costs a
	// review.
	assert.Contains(t, out, "  wrote   .codex/hooks.json (gate + lessons hooks)")
	assert.Contains(t, out, "          PreToolUse Bash                /bin/seamark gate --hook --client codex")
	assert.Contains(t, out, "          PreToolUse apply_patch         /bin/seamark lessons --hook --client codex")
	assert.NotContains(t, out, "--hook-reset --client codex")

	// Native trust and the delivery limit are reported, not hidden. The
	// gate line describes the Codex hook: a first install never blocks.
	assert.Contains(t, out, "Codex runs a project hook only after the user reviews and trusts it")
	assert.Contains(t, out, "`hook_delivery: once-per-context` does not apply")
	assert.Contains(t, out, "setup never grants trust")
	assert.Contains(t, out, "gate    warn — verdicts are reported, nothing blocks")
	assert.NotContains(t, out, "no gate hook for the selected clients")
	assert.NotContains(t, out, "only a gate hook with --enforce blocks")

	assert.Equal(t, "{ broken", string(mustRead(t, root, ".claude/settings.json")), "the unselected client's file is untouched")
	assert.NoFileExists(t, filepath.Join(root, ".mcp.json"))
	assert.NoDirExists(t, filepath.Join(root, ".claude", "skills"))
	assert.FileExists(t, filepath.Join(root, ".seamark", "policy.yaml"))

	// A second run changes nothing.
	before := snapshot(t, root)
	again := initClients(t, root, false, true, true, "", "codex")
	assert.Contains(t, again, `kept    .codex/config.toml (seamark registered as "seamark"; 5/5 tools approved)`)
	assert.Contains(t, again, "kept    .codex/hooks.json (seamark hooks already wired)")
	assert.Contains(t, again, "kept    .agents/skills/seamark-plan-change (current)")
	assert.Equal(t, before, snapshot(t, root))
}

func TestInitClientRegistersWithoutGrants(t *testing.T) {
	root := t.TempDir()

	out := initClients(t, root, false, false, false, "", "codex", "claude")

	// Codex: the registration alone, no approval.
	codex := string(mustRead(t, root, ".codex/config.toml"))
	assert.Contains(t, codex, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")
	assert.NotContains(t, codex, "approval_mode", "registration does not imply approval")

	// Claude Code: hooks and the .mcp.json registration, no allow rule.
	assert.Contains(t, string(mustRead(t, root, ".mcp.json")), `"seamark"`)
	assert.NotContains(t, readSettings(t, root), "permissions", "no allow rule without --approve-tools")
	assert.Len(t, commands(t, readSettings(t, root)), 2, "gate and lessons hooks")

	// Registry order in the narration, whatever order the flags came in.
	assert.Less(t, strings.Index(out, ".claude/settings.json"), strings.Index(out, ".codex/config.toml"))
	assert.Contains(t, out, `  wrote   .mcp.json (registered seamark mcp as "seamark")`)
	assert.Contains(t, out, "skills  not installed — add --skills to install the seamark agent skills (.claude/skills, .agents/skills)")
	assert.Contains(t, out, "gate    warn — verdicts are reported")

	// Both clients run a gate hook, so the gate line covers both and no
	// client is named as ungated.
	assert.NotContains(t, out, "no command gate hook")
	assert.Contains(t, out, "/bin/seamark gate --hook")
	assert.Contains(t, out, "/bin/seamark gate --hook --client codex")

	enforcedRoot := t.TempDir()
	enforced := initClients(t, enforcedRoot, false, false, false, "enforce", "claude", "codex")
	assert.Contains(t, enforced, "gate    enforce — deny/require_approval verdicts exit 2 and block")
	assert.NotContains(t, enforced, "without --enforce", "--gate-mode enforce reaches every selected client's hook")
	assert.Contains(t, string(mustRead(t, enforcedRoot, ".codex/hooks.json")), "gate --enforce --hook --client codex")
	assert.Contains(t, string(mustRead(t, enforcedRoot, ".claude/settings.json")), "gate --enforce --hook")

	reversed := t.TempDir()
	assert.Equal(t, out, initClients(t, reversed, false, false, false, "", "claude", "codex", "claude"),
		"repeated and reordered --client flags give the same run")
}

func TestInitClientSkillsNoteTheMissingApprovals(t *testing.T) {
	root := t.TempDir()

	out := initClients(t, root, false, true, false, "", "claude", "codex")
	assert.Contains(t, out, "note    claude 0/8 rules; the client can prompt for the seamark tools")
	assert.Contains(t, out, "`seamark init --client claude --approve-tools` adds the approvals")
	assert.Contains(t, out, "note    codex registered as \"seamark\", 0/5 tools approved")

	approved := initClients(t, root, false, true, true, "", "claude", "codex")
	assert.NotContains(t, approved, "can prompt for the seamark tools")
	assert.Len(t, allowRules(t, root), 8)
}

func TestInitClientDoesNotInferCodexFromTheSharedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))

	// Without --client, a bare --skills detects .agents/. With it, the
	// selection alone decides.
	initClients(t, root, false, true, true, "", "claude")

	assert.DirExists(t, filepath.Join(root, ".claude", "skills", "seamark-plan-change"))
	assert.NoDirExists(t, filepath.Join(root, ".agents", "skills"))
	assert.NoFileExists(t, filepath.Join(root, ".codex", "config.toml"))
}

func TestInitClientSkillBytesEqualTheLegacyInstall(t *testing.T) {
	explicit, legacy := t.TempDir(), t.TempDir()

	initClients(t, explicit, false, true, false, "", "codex")

	var b testWriter
	require.NoError(t, runInit(&b, legacy, "/bin/seamark", "", false, skills.ModeCodex, false))

	var compared int

	for _, rel := range files(t, legacy) {
		if strings.HasPrefix(rel, ".agents/skills/") {
			assert.Equal(t, string(mustRead(t, legacy, rel)), string(mustRead(t, explicit, rel)), rel)

			compared++
		}
	}

	assert.Greater(t, compared, 3, "the comparison must cover the shipped files")
}

func TestInitClientKeepsTheInstalledGateMode(t *testing.T) {
	root := t.TempDir()

	initClients(t, root, false, false, false, gateModeEnforce, "claude")
	assert.Equal(t, gateModeEnforce, installedGateMode(readSettings(t, root)))

	// No --gate-mode keeps the installed mode, and the line says enforce.
	out := initClients(t, root, false, false, false, "", "claude")
	assert.Equal(t, gateModeEnforce, installedGateMode(readSettings(t, root)))
	assert.Contains(t, out, "gate    enforce")

	// A Codex-only run reads no Claude Code file and changes none. With
	// the policy file gone and no --gate-mode, the new scaffold shows
	// which mode the run resolved: enforce would mean it read the
	// unselected client's hook.
	before := string(mustRead(t, root, ".claude/settings.json"))
	require.NoError(t, os.Remove(filepath.Join(root, ".seamark", "policy.yaml")))

	initClients(t, root, false, false, false, "", "codex")
	assert.Equal(t, before, string(mustRead(t, root, ".claude/settings.json")))
	assert.Equal(t, starterPolicyFor(gateModeWarn), string(mustRead(t, root, ".seamark/policy.yaml")))
}

func TestInitClientNamesAManagedHookThatKeepsWarn(t *testing.T) {
	// Claude Code was set up with enforce. A later run adds Codex with
	// no --gate-mode: the run's mode is enforce, the new Codex hook is
	// warn, and the gate line must not read as covering Codex.
	root := t.TempDir()
	initClients(t, root, false, false, false, gateModeEnforce, "claude")

	out := initClients(t, root, false, false, false, "", "claude", "codex")
	assert.Contains(t, out, "gate    enforce — deny/require_approval verdicts exit 2 and block")
	assert.Contains(t, out, "  note    .codex/hooks.json runs its gate hook without --enforce: that hook follows .seamark/policy.yaml")
	assert.Contains(t, string(mustRead(t, root, ".codex/hooks.json")), "gate --hook --client codex\"")
	assert.Contains(t, string(mustRead(t, root, ".claude/settings.json")), "gate --enforce --hook")

	// The reverse order: Codex enforces, Claude Code is added without a
	// mode. Enforce wins for the run, and the Claude Code hook is named.
	reversed := t.TempDir()
	initClients(t, reversed, false, false, false, gateModeEnforce, "codex")

	out = initClients(t, reversed, false, false, false, "", "codex", "claude")
	assert.Contains(t, out, "gate    enforce")
	assert.Contains(t, out, "  note    .claude/settings.json runs its gate hook without --enforce")

	// An explicit mode reaches every selected hook, and the note goes.
	out = initClients(t, reversed, false, false, false, gateModeEnforce, "codex", "claude")
	assert.NotContains(t, out, "without --enforce")
}

func TestInitClientReportsAnUnmanagedWarnGateUnderEnforce(t *testing.T) {
	// A gate hook that setup does not manage runs without --enforce,
	// certainly. A run without a mode installs no second gate hook. A
	// run that asks for enforce installs the managed enforce hook beside
	// it. A run that leaves nothing able to block must not look like one
	// that honored the request. Both runs name the retained hook and what
	// it does.
	unmanaged := "env /opt/bin/seamark gate --hook --client codex"
	workspace := func(t *testing.T) string {
		t.Helper()

		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(starterPolicyFor(gateModeWarn)), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+unmanaged+`"}]}]}}`), 0o644))

		return root
	}

	root := workspace(t)
	out := initClients(t, root, false, false, false, "", "codex")

	assert.Contains(t, out, "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)")
	assert.Contains(t, out, "installed no second handler")
	assert.NotContains(t, out, "gate    enforce")
	assert.NotContains(t, string(mustRead(t, root, ".codex/hooks.json")), `"command": "/bin/seamark gate`, "no second gate handler")

	// The retained hook, run as Codex runs it: a deny is reported and
	// nothing blocks, as the summary says.
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`
	stdout, _, err := runIn(t, payload, "-C", root, "gate", "--hook", "--client", "codex")
	require.NoError(t, err)
	assert.Contains(t, stdout, "deny")
	assert.Contains(t, stdout, "mode: warn")

	// The reported defect: --gate-mode enforce installed nothing here, and
	// the run ended with no hook able to block.
	root = workspace(t)
	out = initClients(t, root, false, false, false, gateModeEnforce, "codex")

	assert.Contains(t, out, "gate    enforce — deny/require_approval verdicts exit 2 and block")
	assert.Contains(t, out, "  note    .codex/hooks.json runs a gate hook without --enforce that setup does not manage: --gate-mode enforce")
	assert.Contains(t, out, "  note    .codex/hooks.json: already runs the seamark gate hook: `"+unmanaged+"`, in warn mode; "+
		"the run asked for enforce mode, so setup installed the managed handler too, and the hook runs twice")

	after := string(mustRead(t, root, ".codex/hooks.json"))
	assert.Contains(t, after, unmanaged, "setup edits no hook it does not manage")
	assert.Contains(t, after, "gate --enforce --hook --client codex\"", "the managed enforce hook is installed beside it")
}

func TestInitClientGateLineReadsWhatAWrappedGateDoes(t *testing.T) {
	// Wrapped gates that setup does not manage. The gate line must say
	// what each one does when Codex runs it.
	codexHooks := func(t *testing.T, command string) string {
		t.Helper()

		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+command+`"}]}]}}`), 0o644))

		return root
	}

	discarding := "/opt/bin/seamark gate --enforce --hook --client codex || true"
	reportOnly := "  gate    report-only — the gate hook in .codex/hooks.json discards its exit status"

	// "|| true" discards the exit status, so no verdict blocks.
	root := codexHooks(t, discarding)

	out := initClients(t, root, false, false, false, "", "codex")
	assert.Contains(t, out, reportOnly)
	assert.NotContains(t, out, "gate    enforce")
	assert.NotContains(t, out, "exit 2 and block")
	assert.NotContains(t, out, "opt in: --gate-mode enforce", "no flag reaches a hook that setup does not manage")
	assert.Contains(t, out, "  note    .codex/hooks.json: has `"+discarding+"`, "+
		"which discards the exit status of the seamark gate hook, so no verdict blocks")

	// The reported defect: an enforcing policy file does not make that
	// hook block either, and the hook has --enforce.
	t.Run("a kept enforcing policy", func(t *testing.T) {
		root := codexHooks(t, discarding)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(starterPolicyFor(gateModeEnforce)), 0o644))

		out := initClients(t, root, false, false, false, "", "codex")
		assert.Contains(t, out, reportOnly)
		assert.NotContains(t, out, "exit 2 and block")
		assert.NotContains(t, out, "without --enforce")
		assert.NotContains(t, out, "reached no gate hook")
		assert.Equal(t, gateModeEnforce, policyModeOf(t, root))
	})

	// A run that asks for enforce gets a hook that enforces: the managed
	// hook goes in beside the one that discards its exit status, and the
	// findings name both facts.
	t.Run("a new enforcing policy", func(t *testing.T) {
		root := codexHooks(t, discarding)

		out := initClients(t, root, false, false, false, gateModeEnforce, "codex")
		assert.Contains(t, out, "gate    enforce — deny/require_approval verdicts exit 2 and block")
		assert.NotContains(t, out, reportOnly)
		assert.Contains(t, out, "  note    .codex/hooks.json: already runs the seamark gate hook: `"+discarding+"`, which discards its exit status; "+
			"the run asked for enforce mode, so setup installed the managed handler too, and the hook runs twice")
		assert.Contains(t, out, "  note    .codex/hooks.json: has `"+discarding+"`, which discards the exit status of the seamark gate hook")
		assert.Contains(t, string(mustRead(t, root, ".codex/hooks.json")), "/seamark gate --enforce --hook --client codex\"")
		assert.Equal(t, gateModeEnforce, policyModeOf(t, root))
	})

	// The same wrapper without --enforce follows no policy either.
	root = codexHooks(t, "/opt/bin/seamark gate --hook --client codex || true")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(starterPolicyFor(gateModeEnforce)), 0o644))

	out = initClients(t, root, false, false, false, "", "codex")
	assert.Contains(t, out, reportOnly)
	assert.NotContains(t, out, "exit 2 and block")

	// A long option before -c, and an exec prefix in front of the shell:
	// the wrapper enforces, and setup adds no second gate hook beside it.
	for _, command := range []string{
		"bash --norc -c '/opt/bin/seamark gate --enforce --hook --client codex'",
		"/usr/bin/env bash --norc -c '/opt/bin/seamark gate --enforce --hook --client codex'",
		"{ cd /repo; /opt/bin/seamark gate --enforce --hook --client codex; }",
	} {
		root := codexHooks(t, command)

		out := initClients(t, root, false, false, false, "", "codex")
		assert.Contains(t, out, "  gate    enforce — .codex/hooks.json runs its own gate hook with --enforce", command)
		assert.NotContains(t, out, "nothing blocks", command)
		assert.NotContains(t, out, "discards the exit status", command)
		assert.Equal(t, 1, strings.Count(string(mustRead(t, root, ".codex/hooks.json")), " gate "), "one gate hook: %s", command)
	}
}

func TestInitClientGateLineNamesAGateThatMayRunAsPossible(t *testing.T) {
	// The reported defect: an unknown program with the gate command as
	// its arguments made the gate line say "enforce", as if the wrapper
	// certainly ran the gate. The reader cannot tell what the program
	// does, so the line names the managed hook and a note names the
	// definition that may also run a gate.
	printed := "/opt/bin/hook-runner /opt/bin/seamark gate --enforce --hook"
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+printed+`"}]}]}}`), 0o644))

	out := initClients(t, root, false, false, false, "", "claude")
	assert.Contains(t, out, "  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing (opt in: --gate-mode enforce)")
	assert.Contains(t, out, "  note    .claude/settings.json holds a definition that may also run a gate hook; setup cannot tell what it does")
	assert.Contains(t, out, "  note    .claude/settings.json: has `"+printed+"`, which can run the seamark gate hook; setup cannot tell")
	assert.NotContains(t, out, "runs its own gate hook with --enforce")
	assert.NotContains(t, out, "gate    enforce")
	assert.NotContains(t, out, "nothing blocks", "the wrapper may block")
}

func TestInitClientGateLineDoesNotSayNothingBlocksBesideAFilteredGate(t *testing.T) {
	// A filtered enforcing gate in the personal file blocks each call that
	// its filter matches, such as a force push. Setup cannot tell which
	// calls those are, so the gate line limits its claim to the gate hooks
	// that certainly run, and the note names the filtered definition.
	root := t.TempDir()
	initClients(t, root, false, false, false, "", "claude")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"/opt/bin/seamark gate --enforce --hook","if":"Bash(git push*)"}]}]}}`), 0o644))

	out := initClients(t, root, false, false, false, "", "claude")
	assert.NotContains(t, out, "nothing blocks")
	assert.Contains(t, out, "  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing"+
		" (opt in: --gate-mode enforce)\n"+
		"  note    .claude/settings.local.json holds a definition that may also run a gate hook; setup cannot tell what it does\n")
}

// policyModeOf reads the mode of the policy file in root.
func policyModeOf(t *testing.T, root string) string {
	t.Helper()

	mode, err := policyFileMode(root, "")
	require.NoError(t, err)

	return mode
}

// hooksOnlySetup is a test-only adapter that declares lifecycle hooks
// and no command gate, the shape of a client whose gate is not built.
type hooksOnlySetup struct{}

func (hooksOnlySetup) Inspect(string) integration.Inspection {
	return integration.Inspection{ClientID: "hooksonly"}
}

// ManagedGateMode is empty: the adapter installs no gate hook.
func (hooksOnlySetup) ManagedGateMode(string) string { return "" }

func (hooksOnlySetup) Plan(string, string, integration.ClientSetup) (integration.ClientPlan, error) {
	return integration.ClientPlan{}, nil
}

func TestInitClientGateModeWithoutAGateHookSaysSo(t *testing.T) {
	reg, err := integration.NewRegistry(integration.Client{
		ID: "hooksonly", Name: "Hooks Only", Setup: hooksOnlySetup{}, SetupOps: integration.SetupSupport{Hooks: true},
	})
	require.NoError(t, err)

	setups, err := integration.ExplicitSetups(reg, []string{"hooksonly"}, false, false, gateModeEnforce)
	require.NoError(t, err)

	var b testWriter

	run := initRun{w: &b, root: t.TempDir(), bin: "/bin/seamark", gateMode: gateModeEnforce}
	require.NoError(t, applyInit(run, reg, setups, selectedNotes{reg: reg}))

	out := b.String()
	assert.Contains(t, out, "note    --gate-mode enforce configures no hook in this run")
	assert.Contains(t, out, "gate    no gate hook for the selected clients")
	assert.NotContains(t, out, "gate    enforce")

	// Beside a gated client, the ungated one is named.
	reg, err = integration.NewRegistry(append(integration.Builtin().Clients(), integration.Client{
		ID: "hooksonly", Name: "Hooks Only", Setup: hooksOnlySetup{}, SetupOps: integration.SetupSupport{Hooks: true},
	})...)
	require.NoError(t, err)

	setups, err = integration.ExplicitSetups(reg, []string{"hooksonly", "codex"}, false, false, "")
	require.NoError(t, err)

	b = testWriter{}
	run = initRun{w: &b, root: t.TempDir(), bin: "/bin/seamark"}
	require.NoError(t, applyInit(run, reg, setups, selectedNotes{reg: reg}))

	out = b.String()
	assert.Contains(t, out, "gate    warn — verdicts are reported")
	assert.Contains(t, out, "  note    Hooks Only has no command gate hook yet: the gate line above does not cover its shell commands")
}

func TestInitClientSharedDestinationIsWrittenOnce(t *testing.T) {
	// A third client that reads .agents/skills beside Codex, registered
	// without a change to either built-in client.
	reg, err := integration.NewRegistry(append(integration.Builtin().Clients(),
		integration.Client{ID: "third", Name: "Third Agent", SkillDirs: []string{skills.AgentsDir}})...)
	require.NoError(t, err)

	setups, err := integration.ExplicitSetups(reg, []string{"third", "codex"}, true, false, "")
	require.NoError(t, err)

	root := t.TempDir()

	var b testWriter

	run := initRun{w: &b, root: root, bin: "/bin/seamark"}
	require.NoError(t, applyInit(run, reg, setups, selectedNotes{reg: reg, skillsRequested: true}))

	out := b.String()
	assert.Equal(t, 1, strings.Count(out, "wrote  .agents/skills/seamark-plan-change"), "one destination, one write")
	assert.Contains(t, out, "note    Third Agent: lifecycle hooks are not supported by this integration; skipped")
	assert.Contains(t, out, "note    Third Agent: MCP registration is not supported by this integration; skipped")
}

func TestInitClientFlagValidation(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "init", "--client", "gemini")
	require.ErrorContains(t, err, `unknown client "gemini" (known: claude, codex)`)
	assert.NoDirExists(t, filepath.Join(root, ".seamark"), "an unknown client fails before any write")

	for mode, replacement := range map[string]string{
		skills.ModeCodex:  "use --client codex --skills",
		skills.ModeClaude: "use --client claude --skills",
		skills.ModeAll:    "use --client claude --client codex --skills",
		skills.ModeAuto:   "a bare --skills installs the skills for the selected clients",
	} {
		_, err = run(t, "-C", root, "init", "--client", "codex", "--skills="+mode)
		require.ErrorContains(t, err, "--skills="+mode+" cannot be combined with --client", mode)
		assert.Contains(t, err.Error(), replacement)
	}

	assert.NoDirExists(t, filepath.Join(root, ".codex"))

	// An empty value asks for nothing, as it always did, so it is not a
	// valued form either. "true" and "false" read like any boolean flag.
	for _, arg := range []string{"--skills=", "--skills=false"} {
		out, err := run(t, "-C", root, "init", "--client", "codex", arg, "--print")
		require.NoError(t, err, arg)
		assert.NotContains(t, out, "would write  .agents/skills", arg)
	}

	out, err := run(t, "-C", root, "init", "--client", "codex", "--skills=true", "--print")
	require.NoError(t, err)
	assert.Contains(t, out, "would write  .agents/skills/seamark-plan-change")

	// The bare form is the one --client takes.
	out, err = run(t, "-C", root, "init", "--client", "codex", "--skills", "--print")
	require.NoError(t, err)
	assert.Contains(t, out, "would write  .agents/skills/seamark-plan-change")

	// Without --client, a bare --skills still means auto.
	out, err = run(t, "-C", root, "init", "--skills", "--print")
	require.NoError(t, err)
	assert.Contains(t, out, "would write  .claude/skills/seamark-plan-change")
	assert.NotContains(t, out, ".agents/skills")
}

func TestInitWithoutClientKeepsItsHistoricalOutput(t *testing.T) {
	// The informational findings are new text. A run without --client
	// plans the same Codex write, and it must still print what it always
	// printed: no trust note, no partial-support note.
	root := t.TempDir()

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, skills.ModeCodex, true))

	out := b.String()
	assert.Contains(t, out, "  wrote   .codex/config.toml (registered seamark mcp; approved 5 tools")
	assert.NotContains(t, out, "setup never grants trust")
	assert.NotContains(t, out, "not supported by this integration")

	// The same plan under --client prints the note.
	assert.Contains(t, initClients(t, t.TempDir(), false, true, true, "", "codex"), "setup never grants trust")
}

func TestInitSkillsEmptyValueInstallsNothing(t *testing.T) {
	// A script that passes an unset variable, --skills=$MODE, must not
	// start to install the skills.
	root := writeFixture(t)

	out, err := run(t, "-C", root, "init", "--skills=", "--print")
	require.NoError(t, err)
	assert.NotContains(t, out, "would write  .claude/skills")
	assert.Contains(t, out, "skills  not installed")
}

func TestInitWithoutClientIgnoresThePersonalSettingsFile(t *testing.T) {
	// .claude/settings.local.json is personal and usually not committed.
	// The shared settings that the team commits must hold every hook,
	// whoever ran init.
	// The gate line then cannot state the effect of a local gate hook, so
	// it says that the run did not check that file.
	root := t.TempDir()
	local := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --enforce --hook"}]}]}}`
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(local), 0o644))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

	assert.Equal(t, gateModeWarn, installedGateMode(readSettings(t, root)), "the gate hook is in the shared file")
	assert.Len(t, commands(t, readSettings(t, root)), 2)
	assert.Equal(t, local, string(mustRead(t, root, ".claude/settings.local.json")), "the personal file is preserved")
	assert.Contains(t, b.String(), "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)\n"+
		uncheckedLocalNote)
}

func TestInitWithoutClientNamesTheUncheckedFileWithoutReadingIt(t *testing.T) {
	// The note comes from the plan, not from a read: a broken or an
	// unreadable personal file gives the same note and no warning.
	cases := map[string]os.FileMode{"broken": 0o644}
	if os.Geteuid() != 0 {
		cases["unreadable"] = 0o000
	}

	for name, mode := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".claude", "settings.local.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte("{bad"), mode))

			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

			var b testWriter
			require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

			assert.Contains(t, b.String(), uncheckedLocalNote)
			assert.NotContains(t, b.String(), "not checked for duplicate seamark hooks")
		})
	}
}

func TestPrintGateLineNamesTheUncheckedFileUnderEveryGateLine(t *testing.T) {
	// A local hook can change what any gate line claims, also a blocking
	// one. So every gate line gets the note. The note comes after the line
	// and its own notes, and before the notes on other definitions. A run
	// that checks every file prints the same lines without the note.
	const (
		shared = ".claude/settings.json"
		local  = ".claude/settings.local.json"
		broken = "mode: [broken\n"
	)

	managedWarn := integration.GateHook{Path: shared, Mode: gateModeWarn, Managed: true}
	managedEnforce := integration.GateHook{Path: shared, Mode: gateModeEnforce, Managed: true}
	localEnforce := integration.GateHook{Path: local, Mode: gateModeEnforce}
	possible := integration.GateHook{Path: shared, Mode: gateModeEnforce, Uncertain: true}
	reportOnly := integration.GateHook{Path: shared, Mode: integration.GateModeReportOnly}

	for name, tc := range map[string]struct {
		gateMode string
		hooks    []integration.GateHook
		policy   string // the body of .seamark/policy.yaml; empty means no file
		last     string // a part of the line just before the note
		next     string // the start of the line just after the note; empty means no line
	}{
		"no gate hook": {gateModeWarn, nil, "", "plain `seamark gate` and `seamark check` runs", ""},
		"no gate hook and a broken policy": {
			gateModeWarn, nil, broken, "no gate hook runs for shell commands; .seamark/policy.yaml failed to load (", "",
		},
		"unknown and a broken policy": {
			gateModeWarn, []integration.GateHook{possible}, broken, "note: .seamark/policy.yaml also failed to load (", "",
		},
		"a local enforcing hook": {
			gateModeWarn, []integration.GateHook{managedWarn, localEnforce}, "",
			"edits a gate hook it does not manage; remove that hook to stop blocking", "",
		},
		"a local enforcing hook and a broken policy": {
			gateModeWarn, []integration.GateHook{managedWarn, localEnforce}, broken,
			"policy is fixed. The hook setup manages is in warn mode", "",
		},
		"report-only": {
			gateModeWarn, []integration.GateHook{reportOnly}, "", "run the gate last in that hook, or remove it and re-run", "",
		},
		"enforce and a broken policy": {
			gateModeEnforce, []integration.GateHook{managedEnforce}, broken,
			"the hook fails closed: EVERY hooked command blocks until the policy is fixed", "",
		},
		"warn and a broken policy": {
			gateModeWarn, []integration.GateHook{managedWarn}, broken, "the hook reports the error and fails open", "",
		},
		"enforce and a kept warn policy": {
			gateModeEnforce, []integration.GateHook{managedEnforce}, "mode: warn\n",
			"`seamark gate` and `seamark check` runs; only a gate hook with --enforce blocks", "",
		},
		"warn and a kept enforcing policy": {
			gateModeWarn, []integration.GateHook{managedWarn}, "mode: enforce\n", "          to stop blocking", "",
		},
		"enforce that reaches no gate hook": {
			gateModeEnforce, []integration.GateHook{managedWarn}, "mode: warn\n",
			"--gate-mode enforce reached no gate hook of this run", "  note    .claude/settings.json runs its gate hook without --enforce",
		},
		"warn beside a possible definition": {
			gateModeWarn, []integration.GateHook{managedWarn, possible}, "", "(opt in: --gate-mode enforce)",
			"  note    .claude/settings.json holds a definition that may also run a gate hook",
		},
		"enforce that reaches no gate hook, beside a possible definition": {
			gateModeEnforce, []integration.GateHook{managedWarn, possible}, "mode: warn\n",
			"--gate-mode enforce reached no gate hook of this run",
			"  note    .claude/settings.json holds a definition that may also run a gate hook",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if tc.policy != "" {
				require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", tc.policy))
			}

			// A run writes the policy file only when none exists. Each case
			// models a run where the user typed the mode of the case.
			var plain, explicit testWriter
			printGateLine(&plain, root, tc.gateMode, true, tc.hooks, []string{local}, tc.policy == "")
			printGateLine(&explicit, root, tc.gateMode, true, tc.hooks, nil, tc.policy == "")

			out := plain.String()

			// A definition that may run a gate can block, so no line beside
			// one says that nothing blocks.
			if slices.Contains(tc.hooks, possible) {
				assert.NotContains(t, out, "nothing blocks")
			}

			at := strings.Index(out, uncheckedLocalNote)
			require.GreaterOrEqual(t, at, 0, "the gate line has the note:\n%s", out)

			head := strings.TrimSuffix(out[:at], "\n")
			assert.Contains(t, head[strings.LastIndex(head, "\n")+1:], tc.last, "the line before the note:\n%s", out)

			if after := out[at+len(uncheckedLocalNote):]; tc.next == "" {
				assert.Empty(t, after, "the note ends the gate line")
			} else {
				assert.True(t, strings.HasPrefix(after, tc.next), "the line after the note:\n%s", out)
			}

			assert.Equal(t, strings.Replace(out, uncheckedLocalNote, "", 1), explicit.String(),
				"a run that checks every file prints the same lines without the note")
		})
	}
}

func TestPrintGateLineNamesTheGateModeFlagOnlyWhenTyped(t *testing.T) {
	// Without --gate-mode, the enforce mode of the run comes from an
	// installed hook. A line that says where the flag went would then name
	// a flag that the user never gave. So only a typed flag gets the
	// clause, and the lines after the gate line stay the same.
	const shared = ".claude/settings.json"

	managedWarn := integration.GateHook{Path: shared, Mode: gateModeWarn, Managed: true}
	possible := integration.GateHook{Path: shared, Mode: gateModeEnforce, Uncertain: true}

	for name, tc := range map[string]struct {
		hooks []integration.GateHook
		plain string // the gate line when the user typed no --gate-mode
		typed string // the gate line when the user typed --gate-mode enforce
	}{
		"no gate hook carries --enforce": {
			[]integration.GateHook{managedWarn},
			"  gate    warn — verdicts are reported, nothing blocks\n",
			"  gate    warn — verdicts are reported, nothing blocks; --gate-mode enforce reached no gate hook of this run\n",
		},
		"beside a possible definition": {
			[]integration.GateHook{managedWarn, possible},
			"  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing\n",
			"  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing;\n" +
				"          --gate-mode enforce reached no gate hook of this run\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A kept warn policy keeps the line off the policy branch, so the
			// mode of the run alone picks the line.
			root := t.TempDir()
			require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", "mode: warn\n"))

			var plain, typed testWriter
			printGateLine(&plain, root, gateModeEnforce, false, tc.hooks, nil, false)
			printGateLine(&typed, root, gateModeEnforce, true, tc.hooks, nil, false)

			require.True(t, strings.HasPrefix(plain.String(), tc.plain), "the plain gate line:\n%s", plain.String())
			require.True(t, strings.HasPrefix(typed.String(), tc.typed), "the typed gate line:\n%s", typed.String())
			assert.NotContains(t, plain.String(), "--gate-mode enforce reached")

			assert.Equal(t, strings.TrimPrefix(typed.String(), tc.typed), strings.TrimPrefix(plain.String(), tc.plain),
				"only the gate line differs")
		})
	}
}

func TestInitKeepsAScaffoldItCannotRead(t *testing.T) {
	// A starter file is never clobbered, so init never looks inside one:
	// a directory with the file's name is "already present", as before.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "lessons.yaml"), 0o755))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

	assert.Contains(t, b.String(), "  kept    .seamark/lessons.yaml (already present)")
	assert.FileExists(t, filepath.Join(root, ".seamark", "policy.yaml"))
}

func TestInitNamesWhatAFailedWriteLeftUndone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only files, so the write cannot fail")
	}

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n"), 0o444))

	var b testWriter

	err := runInit(&b, root, "/bin/seamark", "", false, skills.ModeClaude, false)
	require.ErrorContains(t, err, ".gitignore")

	out := b.String()
	assert.Contains(t, out, "  wrote  .seamark/config.yaml", "what landed")
	assert.Contains(t, out, "  failed  .gitignore")
	assert.Contains(t, out, "  skipped .claude/settings.json (not attempted: an earlier write failed)")
	assert.Contains(t, out, "  skipped .claude/skills/seamark-plan-change (not attempted")
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
}

func TestInitNamesTheFilterRouteForAFilteredManagedGate(t *testing.T) {
	// A filter on the managed gate makes the gate line unknown. The
	// handler already runs the gate directly, so the route removes the
	// filter, in the words that doctor prints.
	root := t.TempDir()
	initClients(t, root, false, false, false, gateModeEnforce, "claude")

	path := filepath.Join(root, ".claude", "settings.json")
	data := mustRead(t, root, ".claude/settings.json")
	command := `"command": "/bin/seamark gate --enforce --hook"`
	require.Contains(t, string(data), command)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), command, command+`, "if": "Bash(git push*)"`, 1)), 0o644))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

	// The run has no --client, so the note on the unchecked personal file
	// follows the route.
	assert.Contains(t, b.String(), "  gate    unknown — .claude/settings.json holds a definition that may run a gate hook, and setup cannot tell what\n"+
		"          it does; "+integration.FilteredHookRoute+"\n"+uncheckedLocalNote)
	assert.NotContains(t, b.String(), "run the seamark gate directly")
}

func TestPrintGateLinePicksTheRouteOfEachPossibleDefinition(t *testing.T) {
	// One document can hold a filtered and an unfiltered definition, so
	// the route follows the hooks, not the documents.
	const unknown = "  gate    unknown — .claude/settings.json holds a definition that may run a gate hook, and setup cannot tell what\n"

	filtered := integration.GateHook{Path: ".claude/settings.json", Mode: gateModeEnforce, Managed: true, Uncertain: true, Filtered: true}
	wrapped := integration.GateHook{Path: ".claude/settings.json", Mode: gateModeEnforce, Uncertain: true}

	for name, tc := range map[string]struct {
		hooks []integration.GateHook
		want  string
	}{
		"filtered":   {[]integration.GateHook{filtered}, "          it does; " + integration.FilteredHookRoute + "\n"},
		"unfiltered": {[]integration.GateHook{wrapped}, "          it does; " + integration.UncertainHookRoute + "\n"},
		"both kinds": {[]integration.GateHook{filtered, wrapped}, "          it does; for a definition with an `if` field, " +
			integration.FilteredHookRoute + ";\n          for another, " + integration.UncertainHookRoute + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			var b testWriter
			printGateLine(&b, t.TempDir(), gateModeWarn, false, tc.hooks, nil, true)

			assert.Contains(t, b.String(), unknown+tc.want)
			assert.NotContains(t, b.String(), "run the seamark gate directly")
		})
	}
}

func TestPrintGateLineCallsAPolicyFileKeptOnlyWhenItExistedBefore(t *testing.T) {
	// The gate line runs after the writes, so the policy file is on disk
	// in both cases. A file that this run wrote carries the mode of the
	// run, so the line calls it new. Only an earlier file is kept.
	const shared, local = ".claude/settings.json", ".claude/settings.local.json"

	managedWarn := integration.GateHook{Path: shared, Mode: gateModeWarn, Managed: true}
	managedEnforce := integration.GateHook{Path: shared, Mode: gateModeEnforce, Managed: true}
	localEnforce := integration.GateHook{Path: local, Mode: gateModeEnforce}

	for name, tc := range map[string]struct {
		gateMode string
		hooks    []integration.GateHook
		policy   string
		text     string // the line text after "the new" or "the kept"
	}{
		"the hook follows an enforcing policy": {
			gateModeEnforce, []integration.GateHook{managedWarn}, "mode: enforce\n",
			" .seamark/policy.yaml sets mode: enforce and the hook\n",
		},
		"an enforcing policy beside a local enforcing hook": {
			gateModeEnforce, []integration.GateHook{managedWarn, localEnforce}, "mode: enforce\n",
			" .seamark/policy.yaml also sets mode: enforce, so every gate hook blocks\n",
		},
		"a warn policy beside an enforcing managed hook": {
			gateModeEnforce, []integration.GateHook{managedEnforce}, "mode: warn\n",
			" .seamark/policy.yaml says mode: warn — it still governs plain\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", tc.policy))

			var written, kept testWriter
			printGateLine(&written, root, tc.gateMode, true, tc.hooks, nil, true)
			printGateLine(&kept, root, tc.gateMode, true, tc.hooks, nil, false)

			assert.Contains(t, written.String(), "the new"+tc.text)
			assert.NotContains(t, written.String(), "the kept")
			assert.Contains(t, kept.String(), "the kept"+tc.text)
			assert.NotContains(t, kept.String(), "the new")
		})
	}
}

func TestInitClientCallsThePolicyFileThatItWritesNew(t *testing.T) {
	// No policy file exists. An idle managed gate hook and a running warn
	// gate hook in the local file make the gate line name the policy file.
	// The run writes that file, so the line must not call it kept. A
	// second run finds the file, so it is kept then.
	for name, printOnly := range map[string]bool{"a run that writes": false, "a run with --print": true} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeHookDocument(t, root, ".claude/settings.json", "Shell", "/bin/seamark gate --hook")
			writeHookDocument(t, root, ".claude/settings.local.json", "Bash", "/bin/seamark gate --hook")

			out := initClients(t, root, printOnly, false, false, gateModeEnforce, "claude")

			if printOnly {
				assert.Contains(t, out, "  would write  .seamark/policy.yaml\n")
				assert.NoFileExists(t, filepath.Join(root, ".seamark", "policy.yaml"))
			} else {
				assert.Contains(t, out, "  wrote  .seamark/policy.yaml\n")
			}

			assert.Contains(t, out, "  gate    enforce — the new .seamark/policy.yaml sets mode: enforce and the hook\n")
			assert.NotContains(t, out, "the kept .seamark/policy.yaml")
		})
	}

	root := t.TempDir()
	writeHookDocument(t, root, ".claude/settings.json", "Shell", "/bin/seamark gate --hook")
	writeHookDocument(t, root, ".claude/settings.local.json", "Bash", "/bin/seamark gate --hook")
	initClients(t, root, false, false, false, gateModeEnforce, "claude")

	out := initClients(t, root, false, false, false, gateModeEnforce, "claude")
	assert.Contains(t, out, "  kept    .seamark/policy.yaml (already present)\n")
	assert.Contains(t, out, "  gate    enforce — the kept .seamark/policy.yaml sets mode: enforce and the hook\n")
}

// writeLocalGate puts an enforcing seamark gate hook into the user's
// personal Claude Code settings.
func writeLocalGate(t *testing.T, root string) string {
	t.Helper()

	const local = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --enforce --hook"}]}]}}`

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(local), 0o644))

	return local
}

// writeHookDocument writes a hook document with one PreToolUse command
// under the matcher. Claude Code and Codex share the shape.
func writeHookDocument(t *testing.T, root, rel, matcher, command string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"`+matcher+`","hooks":[{"type":"command","command":"`+command+`"}]}]}}`), 0o644))
}

func TestInitClientGateSummaryIncludesALocalEnforcingHook(t *testing.T) {
	root := t.TempDir()
	local := writeLocalGate(t, root)

	// The user asks for warn. The shared hook gets warn, and the personal
	// hook still enforces: a force push is still denied. The summary must
	// not say that nothing blocks.
	out := initClients(t, root, false, false, false, gateModeWarn, "claude")

	assert.Equal(t, gateModeWarn, installedGateMode(readSettings(t, root)))
	assert.Contains(t, out, "gate    enforce — .claude/settings.local.json runs its own gate hook with --enforce")
	assert.Contains(t, out, "although the hook setup manages is in warn mode")
	assert.NotContains(t, out, "nothing blocks")
	assert.NotContains(t, out, "this run does not check", "an explicit run checks the personal file")
	assert.Equal(t, local, string(mustRead(t, root, ".claude/settings.local.json")), "the personal file is preserved")

	// A kept enforcing policy is named too, because both then block.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(starterPolicyFor(gateModeEnforce)), 0o644))

	out = initClients(t, root, false, false, false, "", "claude")
	assert.Contains(t, out, "gate    enforce — .claude/settings.local.json runs its own gate hook")
	assert.Contains(t, out, "the kept .seamark/policy.yaml also sets mode: enforce")

	// A policy that cannot load: the enforcing hook fails closed.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: [broken\n"), 0o644))

	out = initClients(t, root, false, false, false, "", "claude")
	assert.Contains(t, out, "that hook fails closed: EVERY hooked command blocks")
	assert.NotContains(t, out, "fails open")

	// An enforcing managed hook already says enforce; the line is as before.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(starterPolicyFor(gateModeEnforce)), 0o644))

	out = initClients(t, root, false, false, false, gateModeEnforce, "claude")
	assert.Contains(t, out, "gate    enforce — deny/require_approval verdicts exit 2 and block; so do the gate's own failures.\n")
}

// The enforce gate line names the two cases that return no exit 2. A
// client that stops the hook at its timeout, or a binary that cannot
// start, never runs the gate code, so the gate cannot block.
func TestInitGateLineNamesWhatEnforceCannotBlock(t *testing.T) {
	want := "  gate    enforce — deny/require_approval verdicts exit 2 and block; so do the gate's own failures.\n" +
		"          The gate cannot block a command when the client stops the hook at its timeout (setup\n" +
		"          writes 15 s) or cannot start the seamark binary\n"

	for _, client := range []string{"claude", "codex"} {
		out := initClients(t, t.TempDir(), false, false, false, gateModeEnforce, client)
		assert.Contains(t, out, want, client)
		assert.NotContains(t, out, "gate failures fail closed", client)
	}
}

// The init help states the same limit as the gate line. The test pins
// the whole paragraph, because a short substring lets the help claim
// what the client does after a timeout, and no recording shows that.
func TestInitHelpNamesWhatEnforceCannotBlock(t *testing.T) {
	want := "That bakes --enforce into the hook: deny/require_approval verdicts exit 2\n" +
		"(blocking the agent's command), and so do the gate's own failures. The\n" +
		"gate cannot block a command when the client stops the hook at its\n" +
		"timeout (setup writes 15 seconds) or cannot start the seamark binary.\n"

	long := newInitCmd(&options{}).Long

	assert.Contains(t, long, want)
	assert.NotContains(t, long, "fail closed")
}

func TestInitClientGateSummaryIgnoresALocalWarnHook(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --hook"}]}]}}`), 0o644))

	out := initClients(t, root, false, false, false, "", "claude")
	assert.Contains(t, out, "gate    warn — verdicts are reported, nothing blocks")
}

// TestInitClientFollowsTheInspectionMatrix runs the explicit init form
// over the shared fixture matrix: every limitation the inspection
// reports for a built-in client, init reports too, in its own output,
// and a credential in a hook command never reaches it.
func TestInitClientFollowsTheInspectionMatrix(t *testing.T) {
	for _, f := range inspecttest.Fixtures() {
		if _, builtin := integration.Builtin().Lookup(f.Client); !builtin {
			continue
		}

		t.Run(f.Name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, f.Write(root))

			// A native document the inspection cannot read stops init before
			// any write; the error names the file, as the inspection does.
			if slices.Contains(slices.Collect(maps.Values(f.States)), integration.StateUnreadable) {
				var b testWriter
				run := initRun{w: &b, root: root, bin: "/bin/seamark", printOnly: true}
				err := runInitClients(run, []string{f.Client}, false)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "fix or move it")

				return
			}

			out := initClients(t, root, true, false, false, "", f.Client)

			// A plain re-run keeps the mode of the managed gate hook.
			if f.ManagedGateMode != "" {
				assert.NotContains(t, out, "remove --enforce")
			}

			if f.ManagedGateMode == "enforce" {
				assert.Contains(t, out, "/bin/seamark gate --enforce --hook")
			}

			expected := f.Findings
			if f.InitFindings != nil {
				expected = f.InitFindings
			}

			for _, reason := range expected {
				assert.Contains(t, out, reason)
			}

			for _, secret := range f.Absent {
				assert.NotContains(t, out, secret)
			}

			if f.Skills != "" {
				assert.Contains(t, out, f.Skills)
			}
		})
	}
}

func TestInitClientWarnsWhenCodexHooksAreTurnedOff(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, inspecttest.Named("disabled").Write(root))

	// Setup would install hooks that never run: the run says so, as a
	// warning, before the gate line promises verdicts.
	out := initClients(t, root, false, false, false, "", integration.CodexID)
	assert.Contains(t, out, "note    .codex/config.toml: [features] hooks = false turns every Codex hook off")
	assert.Contains(t, out, "set [features] hooks = true")

	// The gate line claims no verdict for a hook that never runs. It says
	// what the managed hook does once the hooks run.
	assert.Contains(t, out, "  gate    off — no gate hook runs while the hooks are off (see the note above), "+
		"so no shell command gets a verdict\n"+
		"          the gate hook setup manages follows .seamark/policy.yaml once the hooks run\n")
	assert.NotContains(t, out, "verdicts are reported")
}

func TestInitClientWarnsWhenClaudeHooksAreTurnedOff(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))

	// A gate mode is no lesson-check mode, so the run installs and warns.
	// The gate line claims no verdict and no block for a hook that never
	// runs.
	out := initClients(t, root, false, false, false, "enforce", integration.ClaudeID)
	assert.Contains(t, out, "note    .claude/settings.json: disableAllHooks: true turns off the project hooks of Claude Code, "+
		"so the installed seamark hooks never run")
	assert.Contains(t, out, "remove disableAllHooks, or set it to false")
	assert.Equal(t, 1, strings.Count(out, "disableAllHooks: true turns off"))
	assert.Contains(t, out, "  gate    off — no gate hook runs while the hooks are off (see the note above), "+
		"so no shell command gets a verdict\n"+
		"          the gate hook setup manages carries --enforce: its deny/require_approval verdicts block once the hooks run\n")
	assert.NotContains(t, out, "exit 2 and block")
	assert.NotContains(t, out, "verdicts are reported")
}

func TestInitGateLineLeavesOutAClientWithItsHooksOff(t *testing.T) {
	// Claude Code runs no hook, and Codex does. The gate line describes
	// the Codex hook, and a note names the hook that does not run.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))

	out := initClients(t, root, false, false, false, "", integration.ClaudeID, integration.CodexID)
	assert.Contains(t, out, "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)\n")
	assert.Contains(t, out, "  note    the gate hook in .claude/settings.json does not run while the hooks are off "+
		"(see the note above); the gate line covers the other hooks\n")
}

func TestInitGateModeNotesSkipAHookThatIsOff(t *testing.T) {
	// The local hook names a binary that the tests treat as present. A
	// missing binary would stop the hook before the note, and the test
	// would then prove nothing.
	const note = "  note    .claude/settings.local.json runs a gate hook without --enforce"

	local := inspecttest.PreToolUseDocument("Bash", inspecttest.Binary+" gate --hook")

	// With the hooks on, the local warn hook gets the note.
	on := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(on, ".claude/settings.local.json", local))

	out := initClients(t, on, false, false, false, gateModeEnforce, integration.ClaudeID)
	require.Contains(t, out, note, "the fixture reaches the note")

	// A local warn gate hook that never runs needs no note about the
	// enforce flag: the off line covers it.
	off := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(off, ".claude/settings.json", `{"disableAllHooks": true}`))
	require.NoError(t, inspecttest.WriteFile(off, ".claude/settings.local.json", local))

	out = initClients(t, off, false, false, false, gateModeEnforce, integration.ClaudeID)
	assert.Contains(t, out, "  gate    off — no gate hook runs while the hooks are off")
	assert.NotContains(t, out, "runs a gate hook without --enforce")
}

func TestInitGateLineIgnoresAnEnforceHookThatIsOff(t *testing.T) {
	// Claude Code turns its hooks off, so its managed enforce hook runs
	// for no command. The Codex warn hook runs, and the policy says warn.
	// So nothing blocks, and the line must not say enforce.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))
	require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", starterPolicyFor(gateModeWarn)))

	initClients(t, root, false, false, false, gateModeEnforce, integration.ClaudeID)
	initClients(t, root, false, false, false, "", integration.CodexID)

	// The plain run keeps the mode of each client.
	out := initClients(t, root, false, false, false, "", integration.ClaudeID, integration.CodexID)
	assert.Contains(t, out, "          PreToolUse Bash                /bin/seamark gate --enforce --hook\n")
	assert.Contains(t, out, "          PreToolUse Bash                /bin/seamark gate --hook --client codex\n")
	assert.NotContains(t, out, "gate    enforce")
	assert.NotContains(t, out, "exit 2 and block")

	// The user typed no --gate-mode, so the line names no such flag. The
	// Claude Code hook carries --enforce, so no line may deny it. The note
	// states its mode, as the off line does when every hook is off.
	assert.Contains(t, out, "  gate    warn — verdicts are reported, nothing blocks\n"+
		"  note    the gate hook in .claude/settings.json does not run while the hooks are off "+
		"(see the note above); the gate line covers the other hooks\n"+
		"          the managed gate hook in .claude/settings.json carries --enforce: "+
		"its deny/require_approval verdicts block once the hooks run\n"+
		"  note    .codex/hooks.json runs its gate hook without --enforce: that hook follows .seamark/policy.yaml\n")
	assert.NotContains(t, out, "reached no gate hook")
}

func TestInitGateLineNamesTheManagedHookThatIsOffBesideAnUnmanagedEnforcingHook(t *testing.T) {
	// Codex runs a gate hook with --enforce that setup does not manage, so
	// setup installs no Codex gate hook beside it. Setup does install the
	// Claude Code gate hook, but the hooks there are off. So the line says
	// that this hook does not run, not that setup installed none.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))
	require.NoError(t, inspecttest.WriteFile(root, ".codex/config.toml", "[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n"+
		"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\n"+
		"command = \"/opt/bin/seamark gate --enforce --hook --client codex\"\n"))

	out := initClients(t, root, false, false, false, "", integration.ClaudeID, integration.CodexID)
	assert.Contains(t, out, "          PreToolUse Bash                /bin/seamark gate --hook\n")
	assert.Contains(t, out, "  gate    enforce — .codex/config.toml [hooks] runs its own gate hook with --enforce: deny/require_approval\n"+
		"          verdicts exit 2 and block; the hook setup manages does not run while the hooks are off. Setup never\n"+
		"          edits a gate hook it does not manage; remove that hook to stop blocking\n"+
		"  note    the gate hook in .claude/settings.json does not run while the hooks are off "+
		"(see the note above); the gate line covers the other hooks\n"+
		"          the managed gate hook in .claude/settings.json follows .seamark/policy.yaml once the hooks run\n")
	assert.NotContains(t, out, "installed no gate hook of its own")
}

func TestGateLineWithAManagedHookThatIsOff(t *testing.T) {
	// One client turns its hooks off, and another client runs a gate hook.
	// The gate line describes the hook that runs. It must not deny the
	// managed hook that is off, and it names --gate-mode only when the user
	// typed it.
	const (
		shared = ".claude/settings.json"
		codex  = ".codex/hooks.json"
		inline = ".codex/config.toml [hooks]"
		broken = "mode: [broken\n"
		offFor = "  note    the gate hook in .claude/settings.json does not run while the hooks are off " +
			"(see the note above); the gate line covers the other hooks\n"
		offEnforce = "          the managed gate hook in .claude/settings.json carries --enforce: " +
			"its deny/require_approval verdicts block once the hooks run\n"
		offWarn = "          the managed gate hook in .claude/settings.json follows .seamark/policy.yaml once the hooks run\n"
	)

	sharedEnforceOff := integration.GateHook{Path: shared, Mode: gateModeEnforce, Managed: true, HooksOff: true}
	sharedWarnOff := integration.GateHook{Path: shared, Mode: gateModeWarn, Managed: true, HooksOff: true}
	codexWarn := integration.GateHook{Path: codex, Mode: gateModeWarn, Managed: true}
	inlineWarn := integration.GateHook{Path: inline, Mode: gateModeWarn}
	inlineEnforce := integration.GateHook{Path: inline, Mode: gateModeEnforce}
	possible := integration.GateHook{Path: inline, Mode: gateModeEnforce, Uncertain: true}

	for name, tc := range map[string]struct {
		gateMode  string
		requested bool // the user typed --gate-mode
		hooks     []integration.GateHook
		policy    string // the body of .seamark/policy.yaml; empty means no file
		want      string // the whole output; %v stands for the error of a broken policy
	}{
		"an installed enforce mode": {
			gateModeEnforce, false, []integration.GateHook{sharedEnforceOff, codexWarn}, "mode: warn\n",
			"  gate    warn — verdicts are reported, nothing blocks\n" + offFor + offEnforce +
				"  note    .codex/hooks.json runs its gate hook without --enforce: that hook follows .seamark/policy.yaml\n" +
				"          instead; re-run with --gate-mode enforce to bake the flag into every selected client's gate hook\n",
		},
		"a typed enforce mode": {
			gateModeEnforce, true, []integration.GateHook{sharedEnforceOff, inlineWarn}, "mode: warn\n",
			"  gate    warn — verdicts are reported, nothing blocks; --gate-mode enforce reached no gate hook that runs\n" +
				offFor + offEnforce +
				"  note    .codex/config.toml [hooks] runs a gate hook without --enforce that setup does not manage: --gate-mode enforce\n" +
				"          does not reach it, and that hook follows .seamark/policy.yaml; edit it, or remove it and re-run\n" +
				"          to let setup manage the gate\n",
		},
		"a typed enforce mode beside a possible definition": {
			gateModeEnforce, true, []integration.GateHook{sharedEnforceOff, codexWarn, possible}, "mode: warn\n",
			"  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing;\n" +
				"          --gate-mode enforce reached no gate hook that runs\n" + offFor + offEnforce +
				"  note    .codex/config.toml [hooks] holds a definition that may also run a gate hook; setup cannot tell what it does\n" +
				"  note    .codex/hooks.json runs its gate hook without --enforce: that hook follows .seamark/policy.yaml\n" +
				"          instead; re-run with --gate-mode enforce to bake the flag into every selected client's gate hook\n",
		},
		"an unmanaged enforcing hook": {
			gateModeWarn, false, []integration.GateHook{sharedWarnOff, inlineEnforce}, "",
			"  gate    enforce — .codex/config.toml [hooks] runs its own gate hook with --enforce: deny/require_approval\n" +
				"          verdicts exit 2 and block; the hook setup manages does not run while the hooks are off. Setup never\n" +
				"          edits a gate hook it does not manage; remove that hook to stop blocking\n" + offFor + offWarn,
		},
		"an unmanaged enforcing hook and a broken policy": {
			gateModeWarn, false, []integration.GateHook{sharedWarnOff, inlineEnforce}, broken,
			"  gate    enforce — .codex/config.toml [hooks] runs its own gate hook with --enforce, and .seamark/policy.yaml\n" +
				"          failed to load (%v); that hook fails closed: EVERY hooked command blocks until the\n" +
				"          policy is fixed. The hook setup manages does not run while the hooks are off\n" + offFor + offWarn,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if tc.policy != "" {
				require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", tc.policy))
			}

			want := tc.want

			_, policyErr := policyFileMode(root, tc.gateMode)
			require.Equal(t, tc.policy == broken, policyErr != nil, "only the broken policy fails to load")

			if policyErr != nil {
				want = strings.Replace(want, "%v", policyErr.Error(), 1)
			}

			var b testWriter
			printGateLine(&b, root, tc.gateMode, tc.requested, tc.hooks, nil, tc.policy == "")
			assert.Equal(t, want, b.String())
		})
	}
}

func TestInitOffLineNamesTheModeOfEachManagedHook(t *testing.T) {
	// Both clients turn their hooks off, and their managed gate hooks
	// differ in mode. One sentence would give the mode of one hook to
	// both, so the line names each hook with its own mode.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))
	require.NoError(t, inspecttest.WriteFile(root, ".codex/config.toml", "[features]\nhooks = false\n"))

	initClients(t, root, false, false, false, gateModeEnforce, integration.ClaudeID)
	initClients(t, root, false, false, false, "", integration.CodexID)

	out := initClients(t, root, false, false, false, "", integration.ClaudeID, integration.CodexID)
	assert.Contains(t, out, "  gate    off — no gate hook runs while the hooks are off (see the note above), "+
		"so no shell command gets a verdict\n"+
		"          the managed gate hook in .claude/settings.json carries --enforce: "+
		"its deny/require_approval verdicts block once the hooks run\n"+
		"          the managed gate hook in .codex/hooks.json follows .seamark/policy.yaml once the hooks run\n")
	assert.NotContains(t, out, "the gate hook setup manages")
}

func TestGateOffLineCases(t *testing.T) {
	// Every gate hook of these runs is off, so the off line and its own
	// lines are the whole output. No line may describe a running hook.
	const (
		shared   = ".claude/settings.json"
		local    = ".claude/settings.local.json"
		config   = ".codex/config.toml"
		broken   = "mode: [broken\n"
		offLine  = "  gate    off — no gate hook runs while the hooks are off (see the note above), so no shell command gets a verdict\n"
		enforces = "          the gate hook setup manages carries --enforce: its deny/require_approval verdicts block once the hooks run\n"
	)

	managedEnforce := integration.GateHook{Path: shared, Mode: gateModeEnforce, Managed: true, HooksOff: true}

	for name, tc := range map[string]struct {
		gateMode string
		hooks    []integration.GateHook
		policy   string // the body of .seamark/policy.yaml; empty means no file
		want     string // the lines after the off line
	}{
		"a managed enforce hook": {gateModeEnforce, []integration.GateHook{managedEnforce}, "", enforces},
		"a managed report-only hook": {
			gateModeWarn,
			[]integration.GateHook{{Path: shared, Mode: integration.GateModeReportOnly, Managed: true, HooksOff: true}},
			"", "          the gate hook setup manages stays report-only once the hooks run\n",
		},
		"a managed warn hook": {
			gateModeWarn, []integration.GateHook{{Path: shared, Mode: gateModeWarn, Managed: true, HooksOff: true}},
			"", "          the gate hook setup manages follows .seamark/policy.yaml once the hooks run\n",
		},
		"an unmanaged hook alone": {
			gateModeWarn, []integration.GateHook{{Path: config, Mode: gateModeEnforce, HooksOff: true}}, "", "",
		},
		"a local warn hook in an enforce run": {
			gateModeEnforce, []integration.GateHook{managedEnforce, {Path: local, Mode: gateModeWarn, HooksOff: true}},
			"", enforces,
		},
		"two managed hooks with different modes": {
			gateModeEnforce,
			[]integration.GateHook{managedEnforce, {Path: ".codex/hooks.json", Mode: gateModeWarn, Managed: true, HooksOff: true}},
			"",
			"          the managed gate hook in .claude/settings.json carries --enforce: " +
				"its deny/require_approval verdicts block once the hooks run\n" +
				"          the managed gate hook in .codex/hooks.json follows .seamark/policy.yaml once the hooks run\n",
		},
		"a broken policy": {gateModeEnforce, []integration.GateHook{managedEnforce}, broken, enforces},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if tc.policy != "" {
				require.NoError(t, inspecttest.WriteFile(root, ".seamark/policy.yaml", tc.policy))
			}

			// A broken policy adds its note after the managed hook lines.
			want := offLine + tc.want

			_, policyErr := policyFileMode(root, tc.gateMode)
			require.Equal(t, tc.policy == broken, policyErr != nil, "only the broken policy fails to load")

			if policyErr != nil {
				want += "          note: .seamark/policy.yaml also failed to load (" + policyErr.Error() + ")\n"
			}

			var b testWriter
			printGateLine(&b, root, tc.gateMode, false, tc.hooks, nil, tc.policy == "")
			assert.Equal(t, want, b.String())
		})
	}
}

func TestPlainInitGateLineWithTheHooksOff(t *testing.T) {
	// The plain form does not read the local file, where a false can turn
	// the hooks on again. "while the hooks are off" stays true then, and
	// the unchecked-source lines still follow.
	root := t.TempDir()
	require.NoError(t, inspecttest.WriteFile(root, ".claude/settings.json", `{"disableAllHooks": true}`))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", gateModeEnforce, false, "", false))

	out := b.String()
	assert.Contains(t, out, "this run did not read .claude/settings.local.json")
	assert.Contains(t, out, "  gate    off — no gate hook runs while the hooks are off (see the note above), "+
		"so no shell command gets a verdict\n"+
		"          the gate hook setup manages carries --enforce: its deny/require_approval verdicts block once the hooks run\n"+
		"          this run does not check .claude/settings.local.json for gate hooks")
	assert.NotContains(t, out, "exit 2 and block")
}
