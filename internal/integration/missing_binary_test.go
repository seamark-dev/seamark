package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// absentBinary is a hook binary path that no test machine holds.
const absentBinary = "/nonexistent/seamark"

// findingsAt keeps the findings of one path whose reason holds text.
func findingsAt(findings []Finding, path, text string) []Finding {
	return slices.DeleteFunc(slices.Clone(findings), func(f Finding) bool {
		return f.Path != path || !strings.Contains(f.Reason, text)
	})
}

// entryOf returns the inspection entry of one capability.
func entryOf(t *testing.T, insp Inspection, capability Capability) CapabilityInspection {
	t.Helper()

	entry, ok := insp.Entry(capability)
	require.True(t, ok, "no %s entry", capability)

	return entry
}

func TestHookBinaryMissingFollowsTheRuleOfIdentifyBinary(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good", "seamark")
	require.NoError(t, os.MkdirAll(filepath.Dir(good), 0o755))
	require.NoError(t, os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755))

	plain := filepath.Join(dir, "plain", "seamark")
	require.NoError(t, os.MkdirAll(filepath.Dir(plain), 0o755))
	require.NoError(t, os.WriteFile(plain, []byte("text"), 0o644))

	folder := filepath.Join(dir, "folder", "seamark")
	require.NoError(t, os.MkdirAll(folder, 0o755))

	type binaryCase struct{ name, binary, state, cause string }

	// A directory with an execute bit fails only the regular-file check.
	cases := []binaryCase{
		{"an absent absolute path", absentBinary, lessoncheck.BinaryMissing, binaryAbsent},
		{"a directory", folder, lessoncheck.BinaryMissing, binaryNotRegular},
		{"a relative command", "seamark", lessoncheck.BinaryUnknown, ""},
		{"a good file", good, "", ""},
	}

	// Windows has no execute bit, so a plain file can start there.
	if runtime.GOOS != "windows" {
		cases = append(cases, binaryCase{"a file without an execute bit", plain, lessoncheck.BinaryMissing, binaryNoExecBit})
	}

	// A directory without an execute bit fails both checks. Its cause
	// shows that the regular-file check comes before the execute-bit
	// check. The mode goes back before TempDir removes the folder.
	if runtime.GOOS != "windows" {
		bare := filepath.Join(dir, "bare", "seamark")
		require.NoError(t, os.MkdirAll(bare, 0o755))
		require.NoError(t, os.Chmod(bare, 0o644))
		t.Cleanup(func() { _ = os.Chmod(bare, 0o755) })

		cases = append(cases, binaryCase{"a directory without an execute bit", bare, lessoncheck.BinaryMissing, binaryNotRegular})
	}

	// A folder without permissions makes the stat fail with another error
	// than absence. The file can still exist, so the state is unknown. The
	// superuser reads every folder, so the case needs another user.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked", "seamark")
		require.NoError(t, os.MkdirAll(filepath.Dir(locked), 0o755))
		require.NoError(t, os.WriteFile(locked, []byte("#!/bin/sh\n"), 0o755))
		require.NoError(t, os.Chmod(filepath.Dir(locked), 0))
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(locked), 0o755) })

		cases = append(cases, binaryCase{"a file in a locked folder", locked, lessoncheck.BinaryUnknown, ""})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, state, cause := statHookBinary(tc.binary)
			assert.Equal(t, tc.state, state)
			assert.Equal(t, tc.cause, cause)
			assert.Equal(t, tc.cause, hookBinaryFault(tc.binary))
			assert.Equal(t, tc.state == lessoncheck.BinaryMissing, hookBinaryFault(tc.binary) != "")

			// identifyBinary reads the same state before it reads a version.
			if tc.state != "" {
				assert.Equal(t, tc.state, identifyBinary(tc.binary, BinaryIdentity{}).state)
			}
		})
	}
}

func TestClaudeHookWithAMissingBinaryWarnsAndCountsForNothing(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", `{"hooks":{
  "PreToolUse":[`+hookEntry("Bash", absentBinary+" gate --enforce --hook")+`,`+
		hookEntry("Edit|Write|MultiEdit", absentBinary+" lessons --hook")+`],
  "PostCompact":[{"hooks":[{"type":"command","command":"`+absentBinary+` lessons --hook-reset"}]}]}}`)

	insp := claudeSetup{}.Inspect(root)

	gate := findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark gate hook")
	require.Len(t, gate, 1)
	assert.Equal(t, FindingWarning, gate[0].Level)
	assert.Contains(t, gate[0].Reason, "has `"+absentBinary+" gate --enforce --hook`, whose binary "+absentBinary+" is missing")
	assert.Contains(t, gate[0].Reason, "no verdict blocks")
	assert.Equal(t, "run `seamark init --client claude`, which rewrites the hook command to the running seamark binary",
		gate[0].Action)

	lessons := findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark lessons hook")
	require.Len(t, lessons, 1)
	assert.NotContains(t, lessons[0].Reason, "verdict", "only the gate blocks a verdict")
	assert.Len(t, findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark context reset hook"), 1)

	// The definitions count for no tool and no mode, so no gate mode is
	// claimed, and each hook is partial with the repair of its binary.
	assert.Empty(t, insp.GateMode)
	assert.Equal(t, hooks.ModeEnforce, insp.ManagedGateMode, "the marker mode that setup keeps")

	for _, capability := range []Capability{CapabilityCommands, CapabilityEdits, CapabilityResets} {
		entry := entryOf(t, insp, capability)
		assert.Equal(t, StatePartial, entry.State, capability)
		assert.Contains(t, entry.Detail, "cannot start from .claude/settings.json: "+absentBinary+" is missing", capability)
		assert.Contains(t, entry.Action, "run `seamark init --client claude`", capability)
		assert.True(t, entry.Inert, capability)
	}

	// No hook runs, so the hooks line calls none installed.
	assert.Equal(t, "no hook runs; gate hook cannot start from .claude/settings.json: "+absentBinary+" is missing; "+
		"lessons hook cannot start from .claude/settings.json: "+absentBinary+" is missing", insp.DescribeHooks())
}

func TestClaudeMissingBinaryActionFollowsTheRouteThatRuns(t *testing.T) {
	// A .mcp.json that the explicit setup cannot read stops that run, so
	// the repair names the plain form.
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", bashHook(absentBinary+" gate --hook"))
	writeRel(t, root, ".mcp.json", `{not json`)

	insp := claudeSetup{}.Inspect(root)

	gate := findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark gate hook")
	require.Len(t, gate, 1)
	assert.True(t, strings.HasPrefix(gate[0].Action, "run `seamark init` (the plain form never reads .mcp.json"), gate[0].Action)
	assert.Equal(t, gate[0].Action, entryOf(t, insp, CapabilityCommands).Action)
}

func TestCodexMissingBinaryActionFollowsTheRouteThatRuns(t *testing.T) {
	// Codex setup stops at a config.toml that it cannot read, so the
	// repair fixes that file first. The finding names .codex/hooks.json,
	// so the route names config.toml.
	root := t.TempDir()
	writeRel(t, root, codexHooksFile, bashHook(absentBinary+" gate --hook --client codex"))
	writeRel(t, root, ".codex/config.toml", "[mcp_servers\n")

	insp := codexSetup{}.Inspect(root)

	route := "fix .codex/config.toml, then re-run `seamark init --client codex`, which rewrites the hook command " +
		"to the running seamark binary; then open the project in Codex and review the hooks with /hooks, " +
		"because the new command changes the trust hash"

	gate := findingsAt(insp.Findings, codexHooksFile, "cannot start the seamark gate hook")
	require.Len(t, gate, 1)
	assert.Equal(t, route, gate[0].Action)
	assert.Equal(t, route, entryOf(t, insp, CapabilityCommands).Action)

	// The route runs: the setup run stops at config.toml until the file
	// parses, and then it rewrites the command to the running binary.
	explicit, err := ExplicitSetups(Builtin(), []string{CodexID}, false, false, "")
	require.NoError(t, err)

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: explicit})
	require.ErrorContains(t, err, ".codex/config.toml")

	writeRel(t, root, ".codex/config.toml", "")

	_, err = ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: explicit}), ApplyOptions{})
	require.NoError(t, err)

	assert.Equal(t, StateCurrent, entryOf(t, codexSetup{}.Inspect(root), CapabilityCommands).State)

	written, err := os.ReadFile(filepath.Join(root, codexHooksFile))
	require.NoError(t, err)
	assert.Contains(t, string(written), testBinary+" gate --hook --client codex")
	assert.NotContains(t, string(written), absentBinary)
}

func TestCodexMissingHookActionNamesAnUnreadableConfig(t *testing.T) {
	// Only the lessons hook is installed, and setup stops at config.toml.
	// The absent gate entry names config.toml, the file that stops setup.
	root := t.TempDir()
	writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+hookEntry("apply_patch", testBinary+" lessons --hook --client codex")+`]}}`)
	writeRel(t, root, ".codex/config.toml", "[mcp_servers\n")

	commands := entryOf(t, codexSetup{}.Inspect(root), CapabilityCommands)
	require.Equal(t, StateAbsent, commands.State)
	assert.Equal(t, "fix .codex/config.toml, then re-run `seamark init --client codex`", commands.Action)
}

func TestCodexMissingHookActionKeepsTheInstallRouteForAReadableConfig(t *testing.T) {
	// A config.toml that parses stops no setup run, so the absent gate
	// entry keeps the install route. Only an unreadable file changes it.
	root := t.TempDir()
	writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[`+hookEntry("apply_patch", testBinary+" lessons --hook --client codex")+`]}}`)
	writeRel(t, root, ".codex/config.toml", "[mcp_servers]\n")

	insp := codexSetup{}.Inspect(root)
	require.NotEqual(t, StateUnreadable, entryOf(t, insp, CapabilityMCPRegistration).State)

	commands := entryOf(t, insp, CapabilityCommands)
	require.Equal(t, StateAbsent, commands.State)
	assert.Equal(t, "run `seamark init --client codex` to install the missing hook", commands.Action)

	// The route runs: the setup run installs the gate hook.
	explicit, err := ExplicitSetups(Builtin(), []string{CodexID}, false, false, "")
	require.NoError(t, err)

	_, err = ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: explicit}), ApplyOptions{})
	require.NoError(t, err)

	assert.Equal(t, StateCurrent, entryOf(t, codexSetup{}.Inspect(root), CapabilityCommands).State)
}

func TestCodexHookWithAMissingBinaryWarnsAndNamesTheReview(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/hooks.json", `{"hooks":{"PreToolUse":[`+
		hookEntry("Bash", absentBinary+" gate --hook --client codex")+`,`+
		hookEntry("apply_patch", absentBinary+" lessons --hook --client codex")+`]}}`)

	insp := codexSetup{}.Inspect(root)

	gate := findingsAt(insp.Findings, ".codex/hooks.json", "cannot start the seamark gate hook")
	require.Len(t, gate, 1)
	assert.Equal(t, FindingWarning, gate[0].Level)
	assert.Contains(t, gate[0].Action, "run `seamark init --client codex`, which rewrites the hook command")
	assert.Contains(t, gate[0].Action, "review the hooks with /hooks, because the new command changes the trust hash")
	assert.Len(t, findingsAt(insp.Findings, ".codex/hooks.json", "cannot start the seamark lessons hook"), 1)

	assert.Empty(t, insp.GateMode)

	for _, capability := range []Capability{CapabilityCommands, CapabilityEdits} {
		entry := entryOf(t, insp, capability)
		assert.Equal(t, StatePartial, entry.State, capability)
		assert.Contains(t, entry.Detail, "cannot start from .codex/hooks.json", capability)
		assert.Equal(t, gate[0].Action, entry.Action, capability)
	}
}

func TestClaudeLocalHookWithAMissingBinaryGetsAHandEdit(t *testing.T) {
	root := t.TempDir()
	applyHooks(t, root, ClaudeID)
	writeRel(t, root, claudeLocalSettings, bashHook(absentBinary+" gate --enforce --hook"))

	insp := claudeSetup{}.Inspect(root)

	local := findingsAt(insp.Findings, claudeLocalSettings, "cannot start the seamark gate hook")
	require.Len(t, local, 1)
	assert.Equal(t, "fix the binary path in .claude/settings.local.json, or remove that definition", local[0].Action)

	// The local definition counts for no mode and runs beside nothing. It
	// gets no information finding about a definition that runs, either.
	assert.Equal(t, hooks.ModeWarn, insp.GateMode)
	assert.Empty(t, findingsAt(insp.Findings, claudeLocalSettings, "runs twice"))
	assert.Empty(t, findingsAt(insp.Findings, claudeLocalSettings, "setup does not manage that definition"))
	assert.Equal(t, StateCurrent, entryOf(t, insp, CapabilityCommands).State)

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Len(t, findingsAt(plan.Findings, claudeLocalSettings, "cannot start the seamark gate hook"), 1)
	assert.Empty(t, findingsAt(plan.Findings, claudeLocalSettings, "runs twice"))

	for _, hook := range plan.GateHooks {
		assert.NotEqual(t, claudeLocalSettings, hook.Path, "a hook that cannot start is no gate hook")
	}
}

func TestClaudeEqualHooksWithAMissingBinaryAreNoCopy(t *testing.T) {
	// A definition that cannot start runs for no tool. So an equal local
	// definition is no copy that Claude Code runs once, and only the two
	// binary warnings print.
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", bashHook(absentBinary+" gate --hook"))
	writeRel(t, root, claudeLocalSettings, bashHook(absentBinary+" gate --hook"))

	insp := claudeSetup{}.Inspect(root)

	assert.Len(t, findingsWith(insp.Findings, "cannot start the seamark gate hook"), 2)
	assert.Empty(t, findingsWith(insp.Findings, "Claude Code runs"))
}

func TestClaudeHookFollowsAWorkingDefinitionBesideABrokenOne(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", bashHook(absentBinary+" gate --hook"))
	writeRel(t, root, claudeLocalSettings, bashHook(testBinary+" gate --enforce --hook"))

	insp := claudeSetup{}.Inspect(root)

	commands := entryOf(t, insp, CapabilityCommands)
	assert.Equal(t, StateCurrent, commands.State)
	assert.Contains(t, commands.Detail, "runs from .claude/settings.local.json (not managed by setup)")
	assert.Equal(t, hooks.ModeEnforce, insp.GateMode)
	assert.Len(t, findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark gate hook"), 1)
}

func TestCodexSetupRewritesAMissingBinaryWithoutAWarning(t *testing.T) {
	// The plan rebuilds the finding of each managed source and removes it.
	// Each cause must give the same finding, or a stale warning stays.
	for name, binary := range brokenBinaries(t) {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, ".codex/hooks.json", bashHook(binary+" gate --hook --client codex"))

			plan := planCodex(t, root, ClientSetup{Hooks: true})

			assert.Empty(t, findingsWith(plan.Findings, "cannot start"), "the merge repairs the managed command")
			require.Len(t, plan.Writes, 1)
			assert.Contains(t, string(plan.Writes[0].After), testBinary+" gate --hook --client codex")
			assert.NotContains(t, string(plan.Writes[0].After), binary)
			assert.Contains(t, plan.GateHooks, GateHook{Path: codexHooksFile, Mode: hooks.ModeWarn, Managed: true})
		})
	}
}

// brokenBinaries returns one hook binary path for each cause that stops
// the client: an absent path, a directory, and a file with no execute
// bit. Windows has no execute bit, so it gets no such file.
func brokenBinaries(t *testing.T) map[string]string {
	t.Helper()

	dir := t.TempDir()

	folder := filepath.Join(dir, "folder", "seamark")
	require.NoError(t, os.MkdirAll(folder, 0o755))

	binaries := map[string]string{"absent": absentBinary, "directory": folder}

	if runtime.GOOS != "windows" {
		plain := filepath.Join(dir, "plain", "seamark")
		require.NoError(t, os.MkdirAll(filepath.Dir(plain), 0o755))
		require.NoError(t, os.WriteFile(plain, []byte("text"), 0o644))

		binaries["no execute bit"] = plain
	}

	return binaries
}

func TestClaudeHookBinaryWarningNamesTheCause(t *testing.T) {
	binaries := brokenBinaries(t)

	cases := map[string]string{"directory": binaryNotRegular, "no execute bit": binaryNoExecBit}

	for name, cause := range cases {
		binary, ok := binaries[name]
		if !ok {
			continue
		}

		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, ".claude/settings.json", bashHook(binary+" gate --hook"))

			insp := claudeSetup{}.Inspect(root)

			gate := findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark gate hook")
			require.Len(t, gate, 1)
			assert.Contains(t, gate[0].Reason, "whose binary "+binary+" "+cause+", so the client cannot start the seamark gate hook, and no verdict blocks")
			assert.NotContains(t, gate[0].Reason, binaryAbsent)

			// The setup run rewrites the command to the running binary, so
			// it clears every cause, and the action does not depend on it.
			assert.Equal(t, "run `seamark init --client claude`, which rewrites the hook command to the running seamark binary",
				gate[0].Action)

			commands := entryOf(t, insp, CapabilityCommands)
			assert.Equal(t, StatePartial, commands.State)
			assert.Contains(t, commands.Detail, "gate hook cannot start from .claude/settings.json: "+binary+" "+cause)
			assert.NotContains(t, commands.Detail, binaryAbsent)

			// Each cause stops the client, so the entry is inert. The hooks
			// line then does not call the hook installed, and it keeps the cause.
			assert.True(t, commands.Inert, "the client never starts the hook")
			assert.Equal(t, "no hook runs; gate hook cannot start from .claude/settings.json: "+binary+" "+cause,
				insp.DescribeHooks())
		})
	}
}

func TestClaudeLocalHookWithAnUnrunnableBinaryGetsAHandEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no execute bit")
	}

	root := t.TempDir()
	applyHooks(t, root, ClaudeID)
	writeRel(t, root, claudeLocalSettings, bashHook(brokenBinaries(t)["no execute bit"]+" gate --hook"))

	insp := claudeSetup{}.Inspect(root)

	local := findingsAt(insp.Findings, claudeLocalSettings, "cannot start the seamark gate hook")
	require.Len(t, local, 1)
	assert.Contains(t, local[0].Reason, binaryNoExecBit)
	assert.Equal(t, "fix the binary path in .claude/settings.local.json, or remove that definition", local[0].Action)
}

func TestHookBinaryWarningScrubsTheBinaryPath(t *testing.T) {
	// The settings file holds the binary path, so the path is untrusted
	// text. status --json writes the detail and the reason with no second
	// scrub, so the inspection must remove the escape character itself.
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", bashHook(`'/nonexistent/esc\u001b[2J/seamark' gate --hook`))

	insp := claudeSetup{}.Inspect(root)

	gate := findingsAt(insp.Findings, ".claude/settings.json", "cannot start the seamark gate hook")
	require.Len(t, gate, 1)
	assert.Contains(t, gate[0].Reason, "whose binary /nonexistent/esc[2J/seamark is missing")
	assert.NotContains(t, gate[0].Reason, "\x1b")

	commands := entryOf(t, insp, CapabilityCommands)
	assert.Contains(t, commands.Detail, "gate hook cannot start from .claude/settings.json: /nonexistent/esc[2J/seamark is missing")
	assert.NotContains(t, commands.Detail, "\x1b")
}

func TestRelativeHookCommandGetsNoBinaryCheck(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", bashHook("seamark gate --hook"))

	insp := claudeSetup{}.Inspect(root)

	assert.Empty(t, findingsWith(insp.Findings, "cannot start"))
	assert.Equal(t, hooks.ModeWarn, insp.GateMode)
}

func TestSetupNeverChecksTheBinaryItRunsAs(t *testing.T) {
	// The plan writes its own binary into the managed commands, so that
	// binary never gets the warning, whatever the disk holds.
	root := t.TempDir()

	claude, err := claudeSetup{}.Plan(root, absentBinary, ClientSetup{ClientID: ClaudeID, Hooks: true, CheckHookSources: true})
	require.NoError(t, err)
	assert.Empty(t, findingsWith(claude.Findings, "cannot start"))
	assert.Contains(t, claude.GateHooks, GateHook{Path: ".claude/settings.json", Mode: hooks.ModeWarn, Managed: true})

	codex, err := codexSetup{}.Plan(root, absentBinary, ClientSetup{ClientID: CodexID, Hooks: true})
	require.NoError(t, err)
	assert.Empty(t, findingsWith(codex.Findings, "cannot start"))
}
