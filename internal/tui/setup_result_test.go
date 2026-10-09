package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/services"
)

// The tests in this file cover the groups of the setup result: problems
// that need attention, optional additions that the user did not choose,
// and the steps that remain in the client itself.

// setupResultWith applies the default Codex setup (MCP registration and
// hooks). The reinspection after the apply reports the given states.
func setupResultWith(t *testing.T, states map[integration.Capability]integration.CapabilityState) *setupWizard {
	t.Helper()

	f, _ := setupFake()
	f.applySetup = func(*services.SetupPreview) (integration.SetupResult, error) {
		next := integMatrix()
		codex := &next[1]

		for i := range codex.Capabilities {
			c := &codex.Capabilities[i]
			if s, ok := states[c.Capability]; ok {
				c.State = s
			}
		}

		codex.Findings = []integration.Finding{{
			Level: integration.FindingInfo, Path: ".codex/hooks.json",
			Reason: "Codex runs a project hook only after the user reviews and trusts it",
			Action: "open the project in Codex and review the hooks with /hooks",
		}}
		f.integrations = next

		return integration.SetupResult{Ops: []integration.OpResult{
			{Path: ".codex/config.toml", Status: integration.OpApplied, Consumers: []string{"codex"}},
		}}, nil
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter", "y")

	wz := setupScreen(h).wizard
	require.Equal(t, setupStepResult, wz.step)
	require.True(t, wz.result.inspected)
	require.Equal(t, []integration.ClientSetup{{ClientID: "codex", Hooks: true, RegisterMCP: true, CheckHookSources: true}},
		wz.setups)

	return wz
}

// setupSections splits flat result text at the given headings and
// returns the text under each heading that is present.
func setupSections(text string, headings ...string) map[string]string {
	type mark struct {
		heading string
		at      int
	}

	var marks []mark

	for _, hd := range headings {
		if i := strings.Index(text, hd); i >= 0 {
			marks = append(marks, mark{hd, i})
		}
	}

	out := map[string]string{}

	for _, m := range marks {
		end := len(text)

		for _, other := range marks {
			if other.at > m.at && other.at < end {
				end = other.at
			}
		}

		out[m.heading] = text[m.at+len(m.heading) : end]
	}

	return out
}

func TestSetupResultSeparatesAttentionOptionalAndNativeSteps(t *testing.T) {
	wz := setupResultWith(t, map[integration.Capability]integration.CapabilityState{
		integration.CapabilityMCPRegistration: integration.StateCurrent,
		integration.CapabilityEdits:           integration.StateCurrent,
		integration.CapabilityCommands:        integration.StatePartial, // requested, still partial
		integration.CapabilityToolGrants:      integration.StatePartial, // not requested, partial
		integration.CapabilitySkills:          integration.StateAbsent,  // not requested, absent
	})

	text := integFlat(strings.Join(wz.remaining(100), "\n"))
	parts := setupSections(text, "NEEDS ATTENTION", "NOT CONFIGURED (OPTIONAL)", "REMAINING NATIVE STEPS")
	require.Len(t, parts, 3, text)

	attention := parts["NEEDS ATTENTION"]
	assert.Contains(t, attention, "Command gate hook ! partial")
	assert.Contains(t, attention, "Tool grants ! partial · not part of this setup")
	assert.NotContains(t, attention, "Skills")

	optional := parts["NOT CONFIGURED (OPTIONAL)"]
	assert.Contains(t, optional, "You did not choose these additions")
	assert.Contains(t, optional, "Skills · not configured", "a declined addition has a neutral tone")
	assert.NotContains(t, optional, "Tool grants")

	native := parts["REMAINING NATIVE STEPS"]
	assert.Contains(t, native, "review the hooks with /hooks")
	assert.Contains(t, native, "Project trust")
	assert.Contains(t, native, "Native evidence")
	assert.Contains(t, native, "does not prove")
	assert.NotContains(t, native, "Skills")
	assert.NotContains(t, native, "Tool grants")

	assert.NotContains(t, text, "Every capability that setup can configure is current.",
		"the fallback appears only when no capability remains")
}

func TestSetupResultRequestedButAbsentNeedsAttention(t *testing.T) {
	wz := setupResultWith(t, map[integration.Capability]integration.CapabilityState{
		integration.CapabilityMCPRegistration: integration.StateAbsent,
		integration.CapabilityEdits:           integration.StateCurrent,
		integration.CapabilityCommands:        integration.StateCurrent,
		integration.CapabilityToolGrants:      integration.StateAbsent,
		integration.CapabilitySkills:          integration.StateAbsent,
	})

	parts := setupSections(integFlat(strings.Join(wz.remaining(100), "\n")), "NEEDS ATTENTION", "NOT CONFIGURED (OPTIONAL)")
	require.Len(t, parts, 2)

	assert.Contains(t, parts["NEEDS ATTENTION"], "MCP registration ! not configured",
		"the apply did not produce a requested capability")
	assert.Contains(t, parts["NOT CONFIGURED (OPTIONAL)"], "Tool grants · not configured")
	assert.Contains(t, parts["NOT CONFIGURED (OPTIONAL)"], "Skills · not configured")
}

func TestSetupResultFallbackOnlyWhenEverythingIsCurrent(t *testing.T) {
	wz := setupResultWith(t, map[integration.Capability]integration.CapabilityState{
		integration.CapabilityMCPRegistration: integration.StateCurrent,
		integration.CapabilityEdits:           integration.StateCurrent,
		integration.CapabilityCommands:        integration.StateCurrent,
		integration.CapabilityToolGrants:      integration.StateCurrent,
		integration.CapabilitySkills:          integration.StateCurrent,
	})

	text := integFlat(strings.Join(wz.remaining(100), "\n"))
	assert.Contains(t, text, "Every capability that setup can configure is current.")
	assert.NotContains(t, text, "NEEDS ATTENTION")
	assert.NotContains(t, text, "NOT CONFIGURED (OPTIONAL)")
	assert.Contains(t, text, "REMAINING NATIVE STEPS")
	assert.Contains(t, text, "review the hooks with /hooks")
}

func TestSetupCapRequestedMapsEveryCapability(t *testing.T) {
	hooks := []integration.ClientSetup{{ClientID: "codex", Hooks: true}}

	assert.True(t, setupCapRequested(hooks, integration.CapabilityEdits))
	assert.True(t, setupCapRequested(hooks, integration.CapabilityCommands))
	assert.True(t, setupCapRequested(hooks, integration.CapabilityResets))
	assert.False(t, setupCapRequested(hooks, integration.CapabilityMCPRegistration))
	assert.False(t, setupCapRequested(hooks, integration.CapabilitySkills))
	assert.False(t, setupCapRequested(hooks, integration.CapabilityToolGrants))

	all := []integration.ClientSetup{{ClientID: "codex", RegisterMCP: true, Skills: true, ApproveTools: true}}
	assert.True(t, setupCapRequested(all, integration.CapabilityMCPRegistration))
	assert.True(t, setupCapRequested(all, integration.CapabilitySkills))
	assert.True(t, setupCapRequested(all, integration.CapabilityToolGrants))
	assert.False(t, setupCapRequested(nil, integration.CapabilitySkills))
}
