//go:build unix

package integration

// The tests in this file need unix. They create symbolic links, or
// they change the umask. os.Symlink needs special rights on
// Windows, so a link test in an untagged file fails there.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
)

// The umask is process-wide state, so this test must not run in
// parallel with another test of the package. None of them is parallel.
func TestANewFileHonoursTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	root := t.TempDir()

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: ClaudeID, RegisterMCP: true}},
	}), ApplyOptions{})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(root, ".mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a private umask yields a private file, the same as a plain write")
}

func TestSymlinksNeverRedirectAReadOrAWrite(t *testing.T) {
	outside := t.TempDir()
	writeRel(t, outside, "settings.json", "{}")
	writeRel(t, outside, "config.toml", "")

	t.Run("ancestor directory", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, ".claude")))

		_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{fullIntent(ClaudeID)}})
		require.ErrorContains(t, err, "symlink at .claude")
	})

	t.Run("destination file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(outside, "config.toml"), filepath.Join(root, ".codex", "config.toml")))

		_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{fullIntent(CodexID)}})
		require.ErrorContains(t, err, "symlink at .codex/config.toml")
	})

	t.Run("link created after the plan", func(t *testing.T) {
		root := t.TempDir()
		plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{{ClientID: CodexID, RegisterMCP: true}}})

		require.NoError(t, os.Symlink(outside, filepath.Join(root, ".codex")))

		_, err := ApplySetup(plan, ApplyOptions{})
		require.ErrorIs(t, err, ErrStalePlan)
		assert.Empty(t, readRel(t, outside, "config.toml"), "the write never follows the link")
	})
}

func TestACreateOnlyDocumentBelowALinkIsNeverCreated(t *testing.T) {
	// Setup refuses an absent starter below a linked directory, because
	// the create would go through the link.
	always := lessonsStarter()
	outside, linked := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(linked, ".seamark")))

	_, err := PlanSetup(Builtin(), SetupRequest{Root: linked, Common: []Document{always}})
	require.ErrorContains(t, err, "symlink at .seamark")

	// Setup keeps an existing starter below the same link, because it
	// observes a create-only document by its presence alone.
	writeRel(t, outside, "lessons.yaml", "theirs\n")

	kept := mustPlan(t, Builtin(), SetupRequest{Root: linked, Common: []Document{always}})
	assert.Empty(t, kept.Writes)
}

func TestLegacySetupsReadNoCodexPathWithoutApproveTools(t *testing.T) {
	// .codex is a link to itself, so stat fails on it. A plain init asks
	// nothing of Codex and must not stop on this path. A run with
	// --approve-tools calls stat on the path and reports the error.
	root := t.TempDir()
	require.NoError(t, os.Symlink(".codex", filepath.Join(root, ".codex")))

	setups, err := LegacySetups(root, "", false, "")
	require.NoError(t, err)
	require.Len(t, setups, 1)
	assert.Equal(t, ClaudeID, setups[0].ClientID)
	assert.False(t, setups[0].ApproveTools)

	_, err = LegacySetups(root, "", true, "")
	require.ErrorIs(t, err, syscall.ELOOP)
	assert.ErrorContains(t, err, filepath.Join(root, ".codex"), "the error names the Codex path")
}

func TestClaudeReadsAnInputThroughALinkAndNeverWritesThroughOne(t *testing.T) {
	outside := t.TempDir()
	writeRel(t, outside, "mcp.json", `{"mcpServers":{"sm":{"command":"/opt/seamark","args":["mcp"]}}}`)
	writeRel(t, outside, "empty.json", `{"mcpServers":{}}`)

	// Grants only: the plan takes the server name from .mcp.json and
	// never writes the file, so the read follows a link.
	root := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "mcp.json"), filepath.Join(root, ".mcp.json")))

	plan := planClaude(t, root, ClientSetup{ApproveTools: true})
	assert.True(t, approve.AllowSet(writeFor(t, plan, approve.ClaudeSettings))["mcp__sm__orient"])

	// The plan keeps the registration when the linked file holds it.
	full := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}})
	assert.Empty(t, full.Writes)

	// Setup refuses a registration that it would write through the link.
	linked := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "empty.json"), filepath.Join(linked, ".mcp.json")))

	_, err := PlanSetup(Builtin(), SetupRequest{Root: linked, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}})
	require.ErrorContains(t, err, ".mcp.json: symlink at .mcp.json")
	assert.Equal(t, `{"mcpServers":{}}`, readRel(t, outside, "empty.json"))
}

func TestCodexHooksRefuseALinkedDocument(t *testing.T) {
	// Setup writes no document through a link.
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(root, ".codex", "hooks.json")))

	_, err := codexSetup{}.Plan(root, testBinary, ClientSetup{ClientID: CodexID, Hooks: true})
	require.ErrorContains(t, err, ".codex/hooks.json: symlink at .codex/hooks.json")
}

// inspected returns one capability entry of an inspection.
func inspected(t *testing.T, insp Inspection, capability Capability) CapabilityInspection {
	t.Helper()

	entry, ok := insp.Entry(capability)
	require.True(t, ok, "%s", capability)

	return entry
}

// linkTo writes body to a file outside the repository and links rel to
// it. The parent directory of rel is a real directory.
func linkTo(t *testing.T, root, rel, body string) (target string) {
	t.Helper()

	target = filepath.Join(t.TempDir(), filepath.Base(rel))
	require.NoError(t, os.WriteFile(target, []byte(body), 0o644))

	link := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))

	return target
}

func TestALinkedRegistrationReadsAlikeInSetupAndInspection(t *testing.T) {
	// .mcp.json is an input. Setup reads it through a link and refuses
	// only a write through one. Inspection reads it by the same rule, so
	// init, doctor, and status call a linked registration current.
	root := t.TempDir()
	linkTo(t, root, approve.MCPConfig, `{"mcpServers":{"seamark":{"command":"seamark","args":["mcp"]}}}`)

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, RegisterMCP: true, ApproveTools: true},
	}})

	i := slices.IndexFunc(plan.Kept, func(k FileKeep) bool { return k.Path == approve.MCPConfig })
	require.GreaterOrEqual(t, i, 0, "init keeps the linked registration")
	assert.Equal(t, `seamark registered as "seamark"`, plan.Kept[i].Detail)

	_, err := ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)

	insp := claudeSetup{}.Inspect(root)
	registration := inspected(t, insp, CapabilityMCPRegistration)
	assert.Equal(t, StateCurrent, registration.State, registration.Detail)
	assert.Equal(t, `registered in .mcp.json as "seamark"`, registration.Detail)
	assert.Empty(t, registration.Action)

	grants := inspected(t, insp, CapabilityToolGrants)
	assert.Equal(t, StateCurrent, grants.State, grants.Detail)
	assert.Equal(t, approve.StateCurrent, approve.InspectClaude(root).State(), "status reads the registration alike")
}

func TestALinkedRegistrationWithoutSeamarkNamesAFixThatClearsIt(t *testing.T) {
	// Setup never writes through a link, so a re-run of init cannot
	// register seamark in a linked .mcp.json. The action names the fixes
	// that work, and never a re-run alone.
	for name, tc := range map[string]struct{ body, stop, change string }{
		"another server": {
			`{"mcpServers":{"other":{"command":"other-server"}}}`, ".mcp.json: symlink at .mcp.json",
			"register seamark in the file it points to",
		},
		"a broken file": {
			"{ broken", ".mcp.json: invalid character",
			"fix the file it points to and register seamark there",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			linkTo(t, root, approve.MCPConfig, tc.body)

			_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
				{ClientID: ClaudeID, RegisterMCP: true},
			}})
			require.ErrorContains(t, err, tc.stop, "a re-run of init does not clear the state")

			registration := inspected(t, claudeSetup{}.Inspect(root), CapabilityMCPRegistration)
			assert.NotEqual(t, StateCurrent, registration.State)
			assert.Equal(t, "setup never writes through the symlink at .mcp.json: "+tc.change+
				", or replace the link with the real file and run `seamark init --client claude`", registration.Action)
		})
	}

	// A link to a missing file registers nothing, and the same fix applies.
	root := t.TempDir()
	target := linkTo(t, root, approve.MCPConfig, "")
	require.NoError(t, os.Remove(target))

	registration := inspected(t, claudeSetup{}.Inspect(root), CapabilityMCPRegistration)
	assert.Equal(t, StateAbsent, registration.State)
	assert.Contains(t, registration.Action, "symlink at .mcp.json")
}

func TestALinkedOwnedDocumentIsRefusedByEveryReader(t *testing.T) {
	// Setup owns these documents, so the plan refuses to read or write one
	// through a link. Every inspection reader of the same data refuses the
	// link too, and each entry names the fix that clears it: a re-run alone
	// does not.
	settings := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` +
		testBinary + ` gate --enforce --hook"}]}]},"permissions":{"allow":["mcp__seamark__orient"]}}`
	config := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"
	hookDoc := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` +
		testBinary + ` gate --enforce --hook --client codex"}]}]}}`

	cases := []struct {
		name, rel, body string
		intent          ClientSetup
		setup           SetupAdapter
		entries         []Capability
	}{
		{"Claude Code settings", approve.ClaudeSettings, settings,
			ClientSetup{ClientID: ClaudeID, Hooks: true, ApproveTools: true}, claudeSetup{},
			[]Capability{CapabilityEdits, CapabilityCommands, CapabilityResets, CapabilityToolGrants}},
		{"Codex configuration", approve.CodexConfig, config,
			ClientSetup{ClientID: CodexID, RegisterMCP: true, ApproveTools: true}, codexSetup{},
			[]Capability{CapabilityMCPRegistration, CapabilityToolGrants}},
		{"Codex hooks", codexHooksFile, hookDoc,
			ClientSetup{ClientID: CodexID, Hooks: true}, codexSetup{},
			[]Capability{CapabilityEdits, CapabilityCommands}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			linkTo(t, root, tc.rel, tc.body)

			_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{tc.intent}})
			require.ErrorContains(t, err, "symlink at "+tc.rel)

			insp := tc.setup.Inspect(root)

			for _, capability := range tc.entries {
				entry := inspected(t, insp, capability)
				assert.Equal(t, StateUnreadable, entry.State, "%s: %s", capability, entry.Detail)
				assert.Equal(t, "replace the symlink at "+tc.rel+" with the real file or directory, then re-run setup; "+
					"setup never writes "+tc.rel+" through a link", entry.Action, capability)
			}

			assert.Empty(t, insp.ManagedGateMode, "a refused document has no managed hook")
			assert.Empty(t, tc.setup.ManagedGateMode(root))
		})
	}

	// The approval record that status prints refuses the settings link,
	// as the plan does.
	root := t.TempDir()
	linkTo(t, root, approve.ClaudeSettings, settings)
	assert.Equal(t, approve.StateUnreadable, approve.InspectClaude(root).State())
	assert.Contains(t, approve.InspectClaude(root).Err, "symlink at .claude/settings.json")
}

func TestAMissingHookActionClearsTheStateBehindALinkedDocument(t *testing.T) {
	// The explicit setup of Claude Code plans .mcp.json, and it stops at a
	// link that it must write. The action names the plain form of init,
	// which never reads .mcp.json, and that run restores the hook.
	root := t.TempDir()
	linkTo(t, root, approve.MCPConfig, `{"mcpServers":{}}`)
	writeRel(t, root, approve.ClaudeSettings, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
		`{"type":"command","command":"`+testBinary+` gate --hook"}]}]}}`)

	edits := inspected(t, claudeSetup{}.Inspect(root), CapabilityEdits)
	require.Equal(t, StateAbsent, edits.State)
	assert.Equal(t, "run `seamark init` to install the missing hook: the plain form never reads .mcp.json, "+
		"where `seamark init --client claude` stops", edits.Action)

	explicit, err := ExplicitSetups(Builtin(), []string{ClaudeID}, false, false, "")
	require.NoError(t, err)

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: explicit})
	require.ErrorContains(t, err, "symlink at .mcp.json")

	legacy, err := LegacySetups(root, "", false, "")
	require.NoError(t, err)

	_, err = ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: legacy}), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, StateCurrent, inspected(t, claudeSetup{}.Inspect(root), CapabilityEdits).State)

	// Every Codex setup that installs hooks plans .codex/config.toml.
	// The action of the missing hook is then the fix of the link.
	root = t.TempDir()
	linkTo(t, root, approve.CodexConfig, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")
	writeRel(t, root, codexHooksFile, `{"hooks":{"PreToolUse":[{"matcher":"apply_patch","hooks":[`+
		`{"type":"command","command":"`+testBinary+` lessons --hook --client codex"}]}]}}`)

	commands := inspected(t, codexSetup{}.Inspect(root), CapabilityCommands)
	require.Equal(t, StateAbsent, commands.State)
	assert.Equal(t, inspected(t, codexSetup{}.Inspect(root), CapabilityMCPRegistration).Action, commands.Action)
	assert.Contains(t, commands.Action, "replace the symlink at .codex/config.toml")

	explicit, err = ExplicitSetups(Builtin(), []string{CodexID}, false, false, "")
	require.NoError(t, err)

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: explicit})
	require.ErrorContains(t, err, "symlink at .codex/config.toml")
}

func TestInspectSanitizesTheHookDocumentError(t *testing.T) {
	// The reason of an unreadable hook document reaches terminals, the
	// status JSON, and the MCP status resource as it is. An operating
	// system error names the absolute path, and a path can hold a
	// terminal escape, so the inspection sanitizes the reason where it
	// sets it. The file is unreadable by its mode, which root ignores.
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}

	for _, tc := range []struct {
		rel     string
		inspect func(root string) Inspection
	}{
		{approve.ClaudeSettings, claudeSetup{}.Inspect},
		{".codex/hooks.json", codexSetup{}.Inspect},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			// The linter reads a literal with the escape as a path with a
			// separator, so the name is built from a variable.
			escape := "\x1b[2J"
			root := filepath.Join(t.TempDir(), "repo"+escape)
			writeRel(t, root, tc.rel, `{}`)

			abs := filepath.Join(root, filepath.FromSlash(tc.rel))
			require.NoError(t, os.Chmod(abs, 0o000))
			t.Cleanup(func() { _ = os.Chmod(abs, 0o644) })

			insp := tc.inspect(root)
			assert.Contains(t, insp.HookDocumentError, "permission denied")
			assert.True(t, strings.HasPrefix(insp.HookDocumentError, tc.rel+": "), insp.HookDocumentError)
			assert.Equal(t, 1, strings.Count(insp.HookDocumentError, tc.rel), "the document is named once")
			assert.NotContains(t, insp.HookDocumentError, "\x1b")

			commands, ok := insp.Entry(CapabilityCommands)
			require.True(t, ok)
			assert.Equal(t, StateUnreadable, commands.State)
			assert.Equal(t, "unreadable ("+insp.HookDocumentError+")", commands.Detail)
		})
	}
}
