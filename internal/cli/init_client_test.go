package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
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
	assert.Contains(t, preview, "would write .codex/hooks.json (lessons hook)")
	assert.Contains(t, preview, "(nothing was written — --print)")

	out := initClients(t, root, false, true, true, "", "codex")
	assert.Contains(t, out, "  wrote   .codex/config.toml (registered seamark mcp; approved 5 tools: orient, why, change_set, check, expand)")
	assert.Contains(t, out, "  wrote  .agents/skills/seamark-plan-change")

	// The lesson hook, with the exact command that Codex runs. No reset
	// hook: a Codex reset clears nothing, and each hook costs a review.
	assert.Contains(t, out, "  wrote   .codex/hooks.json (lessons hook)")
	assert.Contains(t, out, "          PreToolUse apply_patch         /bin/seamark lessons --hook --client codex")
	assert.NotContains(t, out, "--hook-reset --client codex")

	// Native trust and the delivery limit are reported, not hidden. Codex
	// gets no gate hook yet, so the run prints no gate mode for it.
	assert.Contains(t, out, "Codex runs a project hook only after the user reviews and trusts it")
	assert.Contains(t, out, "`hook_delivery: once-per-context` does not apply")
	assert.Contains(t, out, "setup never grants trust")
	assert.Contains(t, out, "gate    no gate hook for the selected clients")
	assert.NotContains(t, out, "only the Claude hook enforces")

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

	// The gate line describes Claude Code only, and the run says so: a
	// Codex shell command is not gated, whatever the mode reads.
	ungated := "  note    Codex has no command gate hook yet: the gate line above does not cover its shell commands"
	assert.Contains(t, out, ungated)
	assert.Less(t, strings.Index(out, "gate    warn"), strings.Index(out, ungated))

	enforced := initClients(t, t.TempDir(), false, false, false, "enforce", "claude", "codex")
	assert.Contains(t, enforced, "gate    enforce")
	assert.Contains(t, enforced, ungated, "an enforce line must never read as covering Codex")

	claudeOnly := initClients(t, t.TempDir(), false, false, false, "", "claude")
	assert.NotContains(t, claudeOnly, "no command gate hook", "every selected client is gated")

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

func TestInitClientGateModeWithoutAGateHookSaysSo(t *testing.T) {
	out := initClients(t, t.TempDir(), false, false, false, gateModeEnforce, "codex")

	assert.Contains(t, out, "note    --gate-mode enforce configures no hook in this run")
	assert.Contains(t, out, "gate    no gate hook for the selected clients")
	assert.NotContains(t, out, "gate    enforce")
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
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --enforce --hook"}]}]}}`), 0o644))

	var b testWriter
	require.NoError(t, runInit(&b, root, "/bin/seamark", "", false, "", false))

	assert.Equal(t, gateModeWarn, installedGateMode(readSettings(t, root)), "the gate hook is in the shared file")
	assert.Len(t, commands(t, readSettings(t, root)), 2)
	assert.NotContains(t, b.String(), "settings.local.json")
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

// writeLocalGate puts an enforcing seamark gate hook into the user's
// personal Claude Code settings.
func writeLocalGate(t *testing.T, root string) string {
	t.Helper()

	const local = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --enforce --hook"}]}]}}`

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(local), 0o644))

	return local
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
	assert.Contains(t, out, "gate    enforce — deny/require_approval verdicts exit 2 and block; gate failures fail closed")
}

func TestInitClientGateSummaryIgnoresALocalWarnHook(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bin/seamark gate --hook"}]}]}}`), 0o644))

	out := initClients(t, root, false, false, false, "", "claude")
	assert.Contains(t, out, "gate    warn — verdicts are reported, nothing blocks")
}
