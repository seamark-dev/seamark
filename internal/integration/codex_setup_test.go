package integration

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
)

// planCodex runs the Codex adapter alone.
func planCodex(t *testing.T, root string, req ClientSetup) ClientPlan {
	t.Helper()

	req.ClientID = CodexID

	plan, err := codexSetup{}.Plan(root, testBinary, req)
	require.NoError(t, err)
	assert.Empty(t, repeatedPathReasons(plan.Findings), "a finding names its path once")

	return plan
}

func TestCodexRegistrationWithoutGrants(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/config.toml", "# mine\nmodel = \"x\"\n")

	plan := planCodex(t, root, ClientSetup{RegisterMCP: true})

	require.Len(t, plan.Writes, 1)
	assert.Equal(t, "registered seamark mcp", plan.Writes[0].Detail)

	after := string(plan.Writes[0].After)
	assert.Contains(t, after, "# mine\nmodel = \"x\"\n", "every existing byte stays")
	assert.Contains(t, after, "command = \"seamark\"", "the shared file names the bare command")
	assert.NotContains(t, after, testBinary)
	assert.NotContains(t, after, "approval_mode", "registration does not imply approval")

	var parsed map[string]any

	_, err := toml.Decode(after, &parsed)
	require.NoError(t, err)

	// Trust stays the user's decision, and the plan says so.
	infos := findingReasons(plan, FindingInfo)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0], "setup never grants trust")
}

func TestCodexRegistrationAndGrantsShareOneWrite(t *testing.T) {
	plan := planCodex(t, t.TempDir(), ClientSetup{RegisterMCP: true, ApproveTools: true})

	require.Len(t, plan.Writes, 1, "one document, one write")
	assert.Equal(t, "registered seamark mcp; approved 5 tools: orient, why, change_set, check, expand", plan.Writes[0].Detail)
	assert.Empty(t, findingReasons(plan, FindingWarning))
}

func TestCodexKeepsExplicitRestrictionsAndReportsThem(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/config.toml", `[mcp_servers.seamark]
command = "seamark"
args = ["mcp"]
disabled_tools = ["expand"]

[mcp_servers.seamark.tools.why]
approval_mode = "prompt"
`)

	plan := planCodex(t, root, ClientSetup{RegisterMCP: true, ApproveTools: true})

	require.Len(t, plan.Writes, 1, "a restricted tool does not stop the safe part of the setup")
	assert.Equal(t, "approved 3 tools: orient, change_set, check", plan.Writes[0].Detail)
	assert.Contains(t, string(plan.Writes[0].After), "approval_mode = \"prompt\"", "the restriction stays")

	// The kept settings are named on the file's own line, in init's words.
	assert.Empty(t, findingReasons(plan, FindingWarning))

	line := narrated(t, plan, approve.CodexConfig, OpApplied)
	assert.Contains(t, line, "  updated .codex/config.toml (approved 3 tools: orient, change_set, check; kept explicit settings: ")
	assert.Contains(t, line, "disabled_tools lists expand")
	assert.Contains(t, line, `tools.why.approval_mode = "prompt"`)
	assert.Contains(t, narrated(t, plan, approve.CodexConfig, OpPlanned), "  would update .codex/config.toml")
}

func TestCodexForeignRegistrationIsKept(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/config.toml", "[mcp_servers.seamark]\ncommand = \"other-tool\"\n")

	plan := planCodex(t, root, ClientSetup{RegisterMCP: true, ApproveTools: true})

	assert.Empty(t, plan.Writes, "a foreign registration is never replaced")
	assert.Equal(t, map[string]string{approve.CodexConfig: "seamark not registered"}, keptDetails(plan))
	assert.Contains(t, narrated(t, plan, approve.CodexConfig, OpKept),
		"  kept    .codex/config.toml (seamark not registered; kept explicit settings: mcp_servers.seamark runs another command")
}

func TestCodexGrantsAloneNeedARegistration(t *testing.T) {
	plan := planCodex(t, t.TempDir(), ClientSetup{ApproveTools: true})

	assert.Empty(t, plan.Writes)

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "not registered, so no tool approval was added")
}

func TestCodexPlanWithoutAnIntentReadsNothing(t *testing.T) {
	plan := planCodex(t, t.TempDir(), ClientSetup{Skills: true})

	assert.Empty(t, plan.Reads)
	assert.Empty(t, plan.Writes)
}

func TestCodexInspection(t *testing.T) {
	state := func(root string) map[Capability]CapabilityState {
		out := map[Capability]CapabilityState{}

		for _, entry := range (codexSetup{}).Inspect(root).Capabilities {
			require.NoError(t, entry.Validate())
			assert.Equal(t, TrustUnknown, entry.Trust, "trust cannot be read offline")

			switch entry.Capability {
			case CapabilityMCPRegistration, CapabilityToolGrants, CapabilityCommands, CapabilityResets:
				assert.Equal(t, VerificationPending, entry.Verification.Level,
					"%s: the native check is defined and has not run", entry.Capability)
			default:
				assert.Equal(t, VerificationVerified, entry.Verification.Level,
					"%s: the compatibility record holds a native run", entry.Capability)
			}

			if entry.Capability == CapabilitySkills {
				// Evidence only: the registry fills the directory state.
				continue
			}

			out[entry.Capability] = entry.State
		}

		return out
	}

	// The adapter reports its native documents and the three hooks; the
	// reset hook is absent by design and says why.
	absent := map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateAbsent, CapabilityToolGrants: StateAbsent,
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
	}

	root := t.TempDir()
	assert.Equal(t, absent, state(root))

	resets, ok := codexSetup{}.Inspect(root).Entry(CapabilityResets)
	require.True(t, ok)
	assert.Contains(t, resets.Detail, "no reset hook is installed", "absent by design carries its reason")

	apply := func(intent ClientSetup) {
		intent.ClientID = CodexID

		_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{intent}}), ApplyOptions{})
		require.NoError(t, err)
	}

	apply(ClientSetup{RegisterMCP: true})
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateCurrent, CapabilityToolGrants: StatePartial,
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
	}, state(root), "a registration alone approves nothing")

	apply(ClientSetup{RegisterMCP: true, ApproveTools: true})
	assert.Equal(t, StateCurrent, state(root)[CapabilityToolGrants])

	conflict := t.TempDir()
	writeRel(t, conflict, ".codex/config.toml", "[mcp_servers.seamark]\ncommand = \"other-tool\"\n")
	assert.Equal(t, StateConflict, state(conflict)[CapabilityMCPRegistration])

	broken := t.TempDir()
	writeRel(t, broken, ".codex/config.toml", "[mcp_servers\n")
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateUnreadable, CapabilityToolGrants: StateUnreadable,
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
	}, state(broken), "a broken config.toml says nothing about hooks.json")
}

func TestCodexParseErrorNamesTheHookDocumentOnce(t *testing.T) {
	// The reader names .codex/hooks.json in a parse error, as the Claude
	// Code parser names its file, so every reason about a hook document
	// reads alike. The plan adds its advice and no second name.
	root := t.TempDir()
	writeRel(t, root, ".codex/hooks.json", "{not json")

	insp := codexSetup{}.Inspect(root)
	assert.True(t, strings.HasPrefix(insp.HookDocumentError, ".codex/hooks.json: "), insp.HookDocumentError)
	assert.Equal(t, 1, strings.Count(insp.HookDocumentError, ".codex/hooks.json"))

	commands, ok := insp.Entry(CapabilityCommands)
	require.True(t, ok)
	assert.Equal(t, StateUnreadable, commands.State)
	assert.Equal(t, "unreadable ("+insp.HookDocumentError+")", commands.Detail)

	_, err := codexSetup{}.Plan(root, testBinary, ClientSetup{ClientID: CodexID, Hooks: true})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), ".codex/hooks.json: "), err.Error())
	assert.Equal(t, 1, strings.Count(err.Error(), ".codex/hooks.json"))
	assert.Contains(t, err.Error(), "fix or move it, then re-run")
}
