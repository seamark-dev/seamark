package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/skills"
)

// The three approval-target tests below moved here with the rule, from
// the init tests, when init without --client became a translation into
// per-client intent. They freeze the detection the legacy form always
// used.

func TestLegacyApprovalTargetsFollowSkillsModeOrDetectCodex(t *testing.T) {
	root := t.TempDir()

	claude, codex, err := legacyApprovalTargets(root, "")
	require.NoError(t, err)
	assert.True(t, claude)
	assert.False(t, codex, "no .codex/ directory, no Codex configuration")

	require.NoError(t, os.Mkdir(filepath.Join(root, ".codex"), 0o755))
	claude, codex, err = legacyApprovalTargets(root, "")
	require.NoError(t, err)
	assert.True(t, claude)
	assert.True(t, codex)

	for mode, want := range map[string][2]bool{
		skills.ModeClaude: {true, false},
		skills.ModeCodex:  {false, true},
		skills.ModeAll:    {true, true},
	} {
		claude, codex, err = legacyApprovalTargets(root, mode)
		require.NoError(t, err, mode)
		assert.Equal(t, want, [2]bool{claude, codex}, mode)
	}

}

func TestLegacyApprovalTargetsUseOneRuleWithAndWithoutSkills(t *testing.T) {
	// .codex/ without .agents/: the documented one-liner must configure
	// Codex, whichever way the skills target was detected.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".codex"), 0o755))

	for _, mode := range []string{"", skills.ModeAuto} {
		claude, codex, err := legacyApprovalTargets(root, mode)
		require.NoError(t, err, mode)
		assert.True(t, claude, mode)
		assert.True(t, codex, mode)
	}

	// .agents/ without .codex/: skills go to Codex, approvals do not,
	// unless --skills names Codex.
	agents := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(agents, ".agents"), 0o755))

	_, codex, err := legacyApprovalTargets(agents, skills.ModeAuto)
	require.NoError(t, err)
	assert.False(t, codex, "auto detection never creates .codex/")

	_, codex, err = legacyApprovalTargets(agents, skills.ModeAll)
	require.NoError(t, err)
	assert.True(t, codex)

	claude, codex, err := legacyApprovalTargets(root, skills.ModeClaude)
	require.NoError(t, err)
	assert.True(t, claude)
	assert.False(t, codex, "an explicit client narrows the set even with .codex/ present")
}

func TestLegacyApprovalTargetsReportAnUnreadableCodexDirectory(t *testing.T) {
	// A .codex/ that cannot be read is not the same as no .codex/: the
	// error surfaces before init writes anything.
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}

	root := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	_, _, err := legacyApprovalTargets(root, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrPermission)
}

func TestLegacySetupsKeepTheGranularIntent(t *testing.T) {
	claude := func(skills, grants bool) ClientSetup {
		return ClientSetup{ClientID: ClaudeID, Hooks: true, Skills: skills, ApproveTools: grants}
	}
	codex := func(skills, grants bool) ClientSetup {
		return ClientSetup{ClientID: CodexID, Skills: skills, RegisterMCP: grants, ApproveTools: grants}
	}

	cases := []struct {
		name                string
		mode                string
		approve             bool
		agentsDir, codexDir bool
		want                []ClientSetup
	}{
		{name: "plain init", want: []ClientSetup{claude(false, false)}},
		{name: "approve-tools, no .codex", approve: true, want: []ClientSetup{claude(false, true)}},
		{name: "approve-tools with .codex", approve: true, codexDir: true,
			want: []ClientSetup{claude(false, true), codex(false, true)}},
		{name: ".codex alone asks nothing of Codex", codexDir: true, want: []ClientSetup{claude(false, false)}},
		{name: "bare skills", mode: skills.ModeAuto, want: []ClientSetup{claude(true, false)}},
		{name: "bare skills with .agents", mode: skills.ModeAuto, agentsDir: true,
			want: []ClientSetup{claude(true, false), codex(true, false)}},
		// The skills target is detected by .agents/, the approvals target
		// by .codex/: each by the place its own artifact lives.
		{name: "bare skills, approve, .agents only", mode: skills.ModeAuto, approve: true, agentsDir: true,
			want: []ClientSetup{claude(true, true), codex(true, false)}},
		{name: "bare skills, approve, .codex only", mode: skills.ModeAuto, approve: true, codexDir: true,
			want: []ClientSetup{claude(true, true), codex(false, true)}},
		{name: "skills=claude ignores both directories", mode: skills.ModeClaude, approve: true, agentsDir: true, codexDir: true,
			want: []ClientSetup{claude(true, true)}},
		// Codex-only skills still install the Claude Code hooks, and the
		// grants then go to Codex alone.
		{name: "skills=codex", mode: skills.ModeCodex, want: []ClientSetup{claude(false, false), codex(true, false)}},
		{name: "skills=codex with approve", mode: skills.ModeCodex, approve: true,
			want: []ClientSetup{claude(false, false), codex(true, true)}},
		{name: "skills=all with approve", mode: skills.ModeAll, approve: true,
			want: []ClientSetup{claude(true, true), codex(true, true)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			if tc.agentsDir {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".agents"), 0o755))
			}

			if tc.codexDir {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".codex"), 0o755))
			}

			got, err := LegacySetups(root, tc.mode, tc.approve, "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			// Claude Code never gets an MCP registration from this form, and
			// the gate mode reaches the one client that has a gate hook.
			withMode, err := LegacySetups(root, tc.mode, tc.approve, "enforce")
			require.NoError(t, err)
			assert.Equal(t, "enforce", withMode[0].GateMode)
			assert.False(t, withMode[0].RegisterMCP)
		})
	}

	_, err := LegacySetups(t.TempDir(), "everything", false, "")
	require.ErrorContains(t, err, "unknown install mode")
}

func TestExplicitSetupsScopeEveryOperationToTheSelection(t *testing.T) {
	reg, err := NewRegistry(append(Builtin().Clients(), thirdClient())...)
	require.NoError(t, err)

	// Repeated IDs collapse, and the order of the flags does not matter.
	got, err := ExplicitSetups(reg, []string{thirdID, CodexID, thirdID}, true, false, "warn")
	require.NoError(t, err)
	assert.Equal(t, []ClientSetup{
		{ClientID: CodexID, Skills: true, Hooks: true, RegisterMCP: true, GateMode: "warn", CheckHookSources: true},
		{ClientID: thirdID, Skills: true, Hooks: true, RegisterMCP: true, GateMode: "warn", CheckHookSources: true},
	}, got, "registry order; hooks and registration always, skills and grants opt-in")

	reversed, err := ExplicitSetups(reg, []string{CodexID, thirdID}, true, false, "warn")
	require.NoError(t, err)
	assert.Equal(t, got, reversed)

	// The grants stay an opt-in: a registration alone approves nothing.
	plain, err := ExplicitSetups(reg, []string{ClaudeID}, false, false, "")
	require.NoError(t, err)
	assert.Equal(t, []ClientSetup{{ClientID: ClaudeID, Hooks: true, RegisterMCP: true, CheckHookSources: true}}, plain)

	_, err = ExplicitSetups(reg, []string{CodexID, "gemini"}, true, true, "")
	require.ErrorIs(t, err, ErrUnknownClient)
	assert.Contains(t, err.Error(), "known: claude, codex, fakeagent")

	_, err = ExplicitSetups(reg, nil, true, true, "")
	require.ErrorContains(t, err, "no client selected")
}

func TestASharedSkillDirectoryDoesNotSelectItsOtherConsumer(t *testing.T) {
	// .agents/skills is shared storage, not proof that Codex is set up.
	// An explicit selection of the third client installs the skills there
	// and asks nothing of Codex.
	reg, err := NewRegistry(append(Builtin().Clients(), thirdClient())...)
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))

	setups, err := ExplicitSetups(reg, []string{thirdID}, true, false, "")
	require.NoError(t, err)

	plan := mustPlan(t, reg, SetupRequest{Root: root, Binary: testBinary, Clients: setups})
	assert.Equal(t, []SkillDestination{{Dir: ".agents/skills", Consumers: []string{thirdID}}}, plan.Destinations)

	for _, guard := range plan.Reads {
		assert.NotContains(t, guard.Path, ".codex", "Codex was not selected, so its file is never read")
	}
}

func TestGateModeComesFromTheSelectedHookClients(t *testing.T) {
	reg := Builtin()
	root := t.TempDir()

	_, err := ApplySetup(mustPlan(t, reg, SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, GateMode: "enforce"},
	}}), ApplyOptions{})
	require.NoError(t, err)

	both, err := ExplicitSetups(reg, []string{ClaudeID, CodexID}, false, false, "")
	require.NoError(t, err)
	assert.Equal(t, []string{ClaudeID}, GateHookClients(reg, both), "Codex installs lesson hooks and no gate hook yet")
	assert.Equal(t, "enforce", InstalledGateMode(reg, root, GateHookClients(reg, both)))

	// A Codex-only selection reads no Claude Code file, so it sees no mode.
	codexOnly, err := ExplicitSetups(reg, []string{CodexID}, false, false, "")
	require.NoError(t, err)
	assert.Empty(t, GateHookClients(reg, codexOnly))
	assert.Empty(t, InstalledGateMode(reg, root, GateHookClients(reg, codexOnly)))

	// A settings file that cannot be read reports no mode; the plan then
	// reports the error itself.
	writeRel(t, root, ".claude/settings.json", "{ broken")
	assert.Empty(t, InstalledGateMode(reg, root, []string{ClaudeID}))
}
