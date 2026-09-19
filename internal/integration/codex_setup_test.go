package integration

import (
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

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "disabled_tools lists expand")
	assert.Contains(t, warnings[0], `tools.why.approval_mode = "prompt"`)
}

func TestCodexForeignRegistrationIsKept(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/config.toml", "[mcp_servers.seamark]\ncommand = \"other-tool\"\n")

	plan := planCodex(t, root, ClientSetup{RegisterMCP: true, ApproveTools: true})

	assert.Empty(t, plan.Writes, "a foreign registration is never replaced")
	assert.Equal(t, []FileKeep{{Path: approve.CodexConfig, Detail: "seamark not registered"}}, plan.Kept)

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "runs another command")
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
			assert.Equal(t, VerificationPending, entry.Verification.Level,
				"the native check is defined and has not run")

			out[entry.Capability] = entry.State
		}

		return out
	}

	root := t.TempDir()
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateAbsent, CapabilityToolGrants: StateAbsent,
	}, state(root))

	apply := func(intent ClientSetup) {
		intent.ClientID = CodexID

		_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{intent}}), ApplyOptions{})
		require.NoError(t, err)
	}

	apply(ClientSetup{RegisterMCP: true})
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateCurrent, CapabilityToolGrants: StatePartial,
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
	}, state(broken))
}
