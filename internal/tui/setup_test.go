package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// setupPlanFor returns a scripted preview for the Codex requests of a
// test: two writes with a diff, one kept shared file, one skill to
// install, one foreign skill, a trust finding and one gate hook.
func setupPlanFor(setups []integration.ClientSetup) *SetupPreview {
	plan := &integration.SetupPlan{
		Root: "/work/repo",
		Reads: []integration.FileGuard{
			{Path: ".seamark/policy.yaml", Exists: true},
			{Path: ".codex/config.toml", Exists: true},
			{Path: ".codex/hooks.json"},
		},
		Writes: []integration.FileWrite{
			{Path: ".codex/config.toml", Consumers: []string{"codex"}, Detail: "register the seamark MCP server"},
			{Path: ".codex/hooks.json", Consumers: []string{"codex"}, Detail: "gate and lessons hooks"},
		},
		Kept: []integration.FileKeep{{Path: ".seamark/policy.yaml", Detail: "already present"}},
		Skills: []skills.Entry{
			{Name: "seamark-plan-change", Rel: ".agents/skills/seamark-plan-change", State: skills.Absent},
			{Name: "seamark-review-change", Rel: ".agents/skills/seamark-review-change", State: skills.Foreign,
				Reason: "no seamark marker"},
		},
		Destinations: []integration.SkillDestination{{Dir: ".agents/skills", Consumers: []string{"codex"}}},
		GateHooks:    []integration.GateHook{{ClientID: "codex", Path: ".codex/hooks.json", Mode: "warn", Managed: true}},
		Findings: []integration.Finding{{
			Level: integration.FindingInfo, Path: ".codex/config.toml",
			Reason: "Codex reads this file only in a project the user trusts; setup never grants trust",
		}},
	}

	return &SetupPreview{
		Setups: setups,
		Plan:   plan,
		Writes: []integration.WritePreview{
			{
				Path: ".codex/config.toml", Added: 2, Removed: 1, Consumers: []string{"codex"},
				Diff: "--- a/.codex/config.toml\n+++ b/.codex/config.toml\n@@ -1,2 +1,3 @@\n [features]\n" +
					"-old = 1\n+[mcp_servers.seamark]\n+command = \"seamark\"\n",
			},
			{
				Path: ".codex/hooks.json", Created: true, Added: 1, Consumers: []string{"codex"},
				Diff: "--- a/.codex/hooks.json\n+++ b/.codex/hooks.json\n@@ -0,0 +1 @@\n+{\"hooks\": {}}\n",
			},
		},
		GateMode:  "warn",
		PlannedAt: time.Date(2026, 9, 29, 14, 3, 22, 0, time.UTC),
	}
}

// setupFake returns services with the matrix inspection and a planner
// that records every request it receives.
func setupFake() (*fakeServices, *[][]integration.ClientSetup) {
	f := newFake()
	f.integrations = integMatrix()

	var requests [][]integration.ClientSetup

	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		requests = append(requests, setups)

		return setupPlanFor(setups), nil
	}

	return f, &requests
}

// setupOpen opens the wizard for Codex through the Integrations list.
func setupOpen(t *testing.T, f *fakeServices, w, hgt int) *harness {
	t.Helper()

	h := newHarness(t, f, w, hgt)
	h.press("2")
	h.send(navigateMsg{dest: destIntegrations, intent: "setup:codex"})

	return h
}

func setupScreen(h *harness) *integrations {
	return h.m.screens[destIntegrations].(*integrations)
}

// setupBoxText returns the words inside the columns [from, to) of every
// frame line, without the box borders. A box wraps its prose, and a box
// beside it would mix its words into the lines, so a test reads one box
// at a time.
func setupBoxText(frame string, from, to int) string {
	borders := strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ")

	var b strings.Builder

	for _, line := range strings.Split(frame, "\n") {
		r := []rune(line)
		if from >= len(r) {
			continue
		}

		b.WriteString(borders.Replace(string(r[from:min(to, len(r))])) + " ")
	}

	return integFlat(b.String())
}

// setupLineWith returns the first frame line that holds every part, or
// "" when no line does.
func setupLineWith(frame string, parts ...string) string {
	for _, line := range strings.Split(frame, "\n") {
		all := true

		for _, p := range parts {
			if !strings.Contains(line, p) {
				all = false

				break
			}
		}

		if all {
			return line
		}
	}

	return ""
}

func TestSetupHappyPathAppliesTheReviewedPlan(t *testing.T) {
	f, requests := setupFake()

	var applied *SetupPreview

	f.applySetup = func(p *SetupPreview) (integration.SetupResult, error) {
		applied = p

		// The reinspection after apply sees the new files.
		next := integMatrix()
		for i := range next[1].Capabilities {
			if next[1].Capabilities[i].Capability != integration.CapabilityToolGrants {
				next[1].Capabilities[i].State = integration.StateCurrent
			}
		}

		next[1].Capabilities[2].Action = "run `seamark init --client codex --approve-tools` to add the tool grants"
		next[1].Findings = []integration.Finding{{
			Level: integration.FindingInfo, Path: ".codex/hooks.json",
			Reason: "Codex runs a project hook only after the user reviews and trusts it",
			Action: "open the project in Codex and review the hooks with /hooks",
		}}
		f.integrations = next

		return integration.SetupResult{
			Ops: []integration.OpResult{
				{Path: ".seamark/policy.yaml", Status: integration.OpKept, Detail: "already present"},
				{Path: ".codex/config.toml", Status: integration.OpApplied, Consumers: []string{"codex"}},
				{Path: ".codex/hooks.json", Status: integration.OpApplied, Created: true, Consumers: []string{"codex"}},
			},
			Skills: []integration.OpResult{
				{Kind: integration.OpSkill, Path: ".agents/skills/seamark-plan-change", Status: integration.OpApplied, Created: true},
			},
		}, nil
	}

	h := setupOpen(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, " Set up Codex")
	assert.Contains(t, frame, "✓ Client ── ● Choose ── ○ Review ── ○ Apply")
	assert.Contains(t, frame, "╭─ Choose additions ─")
	assert.NotEmpty(t, setupLineWith(frame, "› [✓] MCP registration", "add", "not registered"))
	assert.NotEmpty(t, setupLineWith(frame, "[✓] Lifecycle hooks", "add", "no hooks installed"))
	assert.NotEmpty(t, setupLineWith(frame, "[ ] Managed skills", "no change"))
	assert.NotEmpty(t, setupLineWith(frame, "[ ] Tool grants", "no change"))
	assert.Contains(t, frame, "2 selected · 0 kept")
	assert.Contains(t, setupBoxText(frame, 0, 60), "it never uninstalls or revokes", "the note on kept rows")
	assert.Contains(t, frame, "╭─ MCP registration ─", "the right box names the focused row")
	assert.NotEmpty(t, setupLineWith(frame, "After", "+ MCP registration"))

	// Opt in to the skills, then review the plan.
	h.press("down", "down", "space")
	frame = h.view()
	assert.NotEmpty(t, setupLineWith(frame, "[✓] Managed skills", "add"))
	assert.NotEmpty(t, setupLineWith(frame, "Dir", ".agents/skills"), "the skills row names its directory")

	h.press("enter")
	require.Len(t, *requests, 1)
	assert.Equal(t, []integration.ClientSetup{{
		ClientID: "codex", Skills: true, Hooks: true, RegisterMCP: true, CheckHookSources: true,
	}}, (*requests)[0])

	frame = h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "● Review")
	assert.Contains(t, frame, "╭─ Plan ─")
	assert.Contains(t, frame, "2 files · 1 skill")
	assert.NotEmpty(t, setupLineWith(frame, "› M .codex/config.toml", "+2 −1"))
	assert.NotEmpty(t, setupLineWith(frame, "A .codex/hooks.json", "+1"))
	assert.NotEmpty(t, setupLineWith(frame, "A ", "seamark-plan-change", "new"))
	assert.NotEmpty(t, setupLineWith(frame, "= .seamark/policy.yaml", "kept"), "a kept path stays visible")
	assert.NotEmpty(t, setupLineWith(frame, "! ", "review-change", "not managed"))

	flat := setupBoxText(frame, 0, 42)
	assert.Contains(t, flat, "WILL")
	assert.Contains(t, flat, "~ register the seamark MCP server")
	assert.Contains(t, flat, "+ gate and lessons hooks")
	assert.Contains(t, flat, "+ install the seamark-plan-change skill")
	assert.Contains(t, flat, "WON'T")
	assert.Contains(t, flat, "· touch tool grants")
	assert.Contains(t, flat, "· touch the configuration of Claude Code")
	assert.Contains(t, flat, "· change agent.cli or agent.argv")
	assert.Contains(t, flat, "GATE AFTER APPLY")
	assert.Contains(t, flat, "warn .codex/hooks.json · managed")
	assert.Contains(t, flat, "setup never grants trust")
	assert.Contains(t, flat, "Nothing is written until you press y.")

	// The diff box shows the selected write with its line numbers.
	assert.Contains(t, frame, "╭─ .codex/config.toml ─")
	assert.Contains(t, frame, "modified")
	assert.Contains(t, frame, "@ @@ -1,2 +1,3 @@")
	assert.Contains(t, frame, "  1   1   [features]")
	assert.Contains(t, frame, "  2     − old = 1")
	assert.Contains(t, frame, "      2 + [mcp_servers.seamark]")
	assert.Contains(t, frame, "      3 + command = \"seamark\"")
	assert.Contains(t, frame, "1–7 of 7")
	assert.Contains(t, frame, "Used by Codex · register the seamark MCP server")

	// The kept shared file has no diff.
	h.press("down", "down", "down")
	assert.Contains(t, h.view(), "shared Seamark file")
	assert.NotContains(t, f.called(), "ApplySetup", "moving through the review writes nothing")

	// The review is the confirmation: y applies directly.
	h.press("y")
	require.Equal(t, []string{"PlanSetup", "ApplySetup"}, f.called())
	require.NotNil(t, applied)
	assert.Equal(t, (*requests)[0], applied.Setups, "apply takes exactly the reviewed plan")
	assert.False(t, h.m.st.busy(), "the result frees the mutation slot")

	frame = h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "✓ Configuration updated")
	assert.Contains(t, frame, "✓ done")
	assert.Contains(t, frame, "OPERATIONS")
	assert.NotEmpty(t, setupLineWith(frame, "✓ file", ".codex/hooks.json", "created"))
	assert.NotEmpty(t, setupLineWith(frame, "✓ file", ".codex/config.toml", "updated"))
	assert.NotEmpty(t, setupLineWith(frame, "· file", ".seamark/policy.yaml", "kept"))
	assert.NotEmpty(t, setupLineWith(frame, "✓ skill", "seamark-plan-change", "created"))
	assert.Contains(t, frame, "✓ Client ── ✓ Choose ── ✓ Review ── ✓ Apply")

	res := setupScreen(h).wizard
	require.True(t, res.result.inspected, "the reinspection arrives after apply")

	// The declined tool grants are optional, not a native step.
	parts := setupSections(integFlat(strings.Join(res.remaining(90), "\n")), "NOT CONFIGURED (OPTIONAL)", "REMAINING NATIVE STEPS")
	require.Len(t, parts, 2)

	optional := parts["NOT CONFIGURED (OPTIONAL)"]
	assert.Contains(t, optional, "Tool grants · not configured")
	assert.Contains(t, optional, "seamark init --client codex --approve-tools")

	native := parts["REMAINING NATIVE STEPS"]
	assert.NotContains(t, native, "Tool grants")
	assert.Contains(t, native, "review the hooks with /hooks")
	assert.Contains(t, native, "Project trust")
	assert.Contains(t, native, "Native evidence")
	assert.Contains(t, native, "does not prove")

	cmd, caveat := setupCLIRoute(applied.Setups)
	assert.Equal(t, "seamark init --client codex --skills", cmd)
	assert.Contains(t, caveat, "plans the same additions")

	h.press("enter")
	assert.Nil(t, setupScreen(h).wizard)
	assert.Contains(t, h.view(), "Clients")
}

func TestSetupBackFromReviewKeepsTheDraft(t *testing.T) {
	f, requests := setupFake()
	h := setupOpen(t, f, 120, 32)

	h.press("down", "down", "space", "up", "up", "space", "enter")
	require.Len(t, *requests, 1)
	assert.False(t, (*requests)[0][0].RegisterMCP)
	assert.True(t, (*requests)[0][0].Skills)

	h.press("n")

	frame := h.view()
	assert.Contains(t, frame, "Choose additions")
	assert.Contains(t, frame, "[ ] MCP registration")
	assert.Contains(t, frame, "[✓] Managed skills")

	h.resize(70, 24)
	frame = h.view()
	assertFrame(t, frame, 70, 24)
	assert.Contains(t, frame, "[✓] Managed skills", "a resize keeps the choices")
	assert.NotContains(t, f.called(), "ApplySetup")
}

func TestSetupCancelAtReviewMakesNoChange(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)

	h.press("enter")
	require.Equal(t, setupStepPreview, setupScreen(h).wizard.step)
	assert.Contains(t, h.view(), "esc cancel")

	h.press("esc")
	assert.Nil(t, setupScreen(h).wizard, "esc cancels the whole setup")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "cancel before apply never applies")
}

func TestSetupLockedRowsAreNeverRequested(t *testing.T) {
	f, requests := setupFake()

	insps := integMatrix()
	codex := &insps[1]
	codex.Capabilities[1].State = integration.StateCurrent // MCP registration
	codex.Capabilities[3].State = integration.StatePartial // edit lessons hook
	codex.Capabilities[4].State = integration.StateCurrent // command gate hook
	f.integrations = insps

	h := setupOpen(t, f, 120, 32)

	frame := h.view()
	assert.NotEmpty(t, setupLineWith(frame, "[=] MCP registration", "kept", "already configured"))
	assert.NotEmpty(t, setupLineWith(frame, "[✓] Lifecycle hooks", "update"))
	assert.Contains(t, frame, "1 selected · 1 kept")

	// A locked row does not toggle; it explains why.
	h.press("space")
	assert.Contains(t, h.view(), "is already configured. Setup keeps it")

	h.press("enter")
	require.Len(t, *requests, 1)
	assert.Equal(t, []integration.ClientSetup{{ClientID: "codex", Hooks: true, CheckHookSources: true}}, (*requests)[0])
}

func TestSetupHooksRowNamesTheMissingHooks(t *testing.T) {
	f, _ := setupFake()

	claude := integHealthy()
	claude.Capabilities[5].State = integration.StateAbsent // context reset hook
	claude.Capabilities[5].Detail = ""
	f.integrations = []integration.Inspection{claude, integMatrix()[1]}

	h := newHarness(t, f, 120, 40)
	h.press("2")
	h.send(navigateMsg{dest: destIntegrations, intent: "setup:claude"})
	h.press("down")

	frame := h.view()
	assert.NotEmpty(t, setupLineWith(frame, "[✓] Lifecycle hooks", "update", "+ context reset hook"))
	assert.NotEmpty(t, setupLineWith(frame, "Now", "gate (warn) + lessons hooks installed"))
	assert.NotEmpty(t, setupLineWith(frame, "After", "+ context reset hook"))
	assert.Contains(t, frame, "1 selected · 3 kept")
}

func TestSetupUnsupportedRowExplainsWhy(t *testing.T) {
	f, requests := setupFake()
	f.clients = inspecttest.Registry().Clients()
	f.integrations = append(integMatrix(), integration.Inspection{
		ClientID: inspecttest.SharedID, Name: "Shared Agent",
		Capabilities: []integration.CapabilityInspection{{
			Capability: integration.CapabilityEdits, Supported: true, State: integration.StateCurrent,
			Trust: integration.TrustPending,
		}},
	})

	h := newHarness(t, f, 120, 32)
	h.press("2")
	h.send(navigateMsg{dest: destIntegrations, intent: "setup:" + inspecttest.SharedID})

	frame := h.view()
	assert.NotEmpty(t, setupLineWith(frame, "[-] MCP registration", "n/a", "not supported by"))
	assert.Contains(t, frame, "[ ] Managed skills")
	assert.Contains(t, setupBoxText(frame, 61, 120), "Shared Agent does not support this addition")

	h.press("space")
	assert.Contains(t, h.view(), "MCP registration is not supported by Shared Agent.")

	// Nothing is selected, so continuing plans nothing and says why.
	h.press("enter")
	assert.Empty(t, *requests)
	assert.Contains(t, h.view(), "! Select at least one addition to review a plan.")

	h.press("down", "down", "space")
	assert.NotContains(t, h.view(), "Select at least one addition", "a toggle clears the hint")

	h.press("enter")
	require.Len(t, *requests, 1)
	assert.Equal(t, []integration.ClientSetup{{ClientID: inspecttest.SharedID, Skills: true, CheckHookSources: true}},
		(*requests)[0])
}

func TestSetupSkillRefreshPlansSkillsOnly(t *testing.T) {
	f, requests := setupFake()
	h := newHarness(t, f, 120, 32)
	h.press("2", "down", "u")

	require.Len(t, *requests, 1)
	assert.Equal(t, []integration.ClientSetup{{ClientID: "codex", Skills: true, CheckHookSources: true}}, (*requests)[0])
	assert.Contains(t, h.view(), "Refresh managed skills · Codex")

	h.send(navigateMsg{dest: destIntegrations, intent: "skills:claude"})
	require.Len(t, *requests, 2)
	assert.Equal(t, "claude", (*requests)[1][0].ClientID)
	assert.True(t, (*requests)[1][0].Skills)
	assert.False(t, (*requests)[1][0].Hooks)
}

func TestSetupStalePreviewBlocksApply(t *testing.T) {
	f, _ := setupFake()
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		p := setupPlanFor(setups)
		p.Writes[0].Stale = true

		return p, nil
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	frame := h.view()
	assert.Contains(t, setupBoxText(frame, 0, 42), "! Files changed since planning. Press p to plan again.")
	assert.NotEmpty(t, setupLineWith(frame, "! .codex/config.toml", "stale"))
	assert.Contains(t, frame, "This file changed since planning")
	assert.Contains(t, frame, "p plan again")
	assert.NotContains(t, frame, "y apply", "a stale plan offers no apply key")

	h.press("y")
	assert.Equal(t, setupStepPreview, setupScreen(h).wizard.step, "a stale plan cannot be applied")
	assert.Contains(t, h.view(), "Apply is blocked.")
	assert.Equal(t, []string{"PlanSetup"}, f.called())

	h.press("p")
	assert.Equal(t, []string{"PlanSetup", "PlanSetup"}, f.called())
}

func TestSetupPreviewWithoutAPlanOffersNoApply(t *testing.T) {
	f, _ := setupFake()
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		return &SetupPreview{Setups: setups}, nil
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "No plan")
	assert.Contains(t, setupBoxText(frame, 0, 120), "Nothing was written.")

	h.press("down", "enter", "y")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "a preview without a plan cannot be applied")
}

func TestSetupPlanErrorShowsTheFullReason(t *testing.T) {
	f, _ := setupFake()
	f.planSetup = func([]integration.ClientSetup) (*SetupPreview, error) {
		return nil, fmt.Errorf("%w: .codex/config.toml and the MCP planner plan different content", integration.ErrPlanConflict)
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	frame := setupBoxText(h.view(), 0, 120)
	assertFrame(t, h.view(), 120, 32)
	assert.Contains(t, frame, "The plan has conflicting changes")
	assert.Contains(t, frame, "plan different content")
	assert.Contains(t, frame, "Nothing was written")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "a failed plan cannot be applied")

	h.press("n")
	assert.Equal(t, setupStepChoices, setupScreen(h).wizard.step)
}

func TestSetupStaleApplyWritesNothingAndPlansAgain(t *testing.T) {
	f, _ := setupFake()
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		return integration.SetupResult{Ops: []integration.OpResult{
			{Path: ".codex/config.toml", Status: integration.OpNotAttempted},
			{Path: ".codex/hooks.json", Status: integration.OpNotAttempted},
		}}, fmt.Errorf("%w: .codex/config.toml changed since it was planned; run the command again", integration.ErrStalePlan)
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter", "y")

	frame := h.view()
	assert.Contains(t, frame, "Files changed since the preview. Nothing was written.")
	assert.NotContains(t, frame, "Configuration updated")
	assert.NotEmpty(t, setupLineWith(frame, "! file", ".codex/config.toml", "not attempted"))
	assert.False(t, h.m.st.busy())

	h.press("p")
	assert.Equal(t, []string{"PlanSetup", "ApplySetup", "PlanSetup"}, f.called())
	assert.Equal(t, setupStepPreview, setupScreen(h).wizard.step)
}

func TestSetupFailureListsFailedAndNotAttempted(t *testing.T) {
	f, _ := setupFake()
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		cause := errors.New("the file is read-only")

		return integration.SetupResult{
			Ops: []integration.OpResult{
				{Path: ".codex/config.toml", Status: integration.OpApplied},
				{Path: ".codex/hooks.json", Status: integration.OpFailed, Err: cause},
			},
			Skills: []integration.OpResult{
				{Kind: integration.OpSkill, Path: ".agents/skills/seamark-plan-change", Status: integration.OpNotAttempted},
			},
		}, fmt.Errorf(".codex/hooks.json: %w", cause)
	}

	h := setupOpen(t, f, 120, 40)
	h.press("enter", "y")

	body := setupScreen(h).wizard
	require.Equal(t, setupStepResult, body.step)

	frame := h.view()
	assert.Contains(t, frame, "! Setup stopped")
	assert.Contains(t, frame, "! stopped")
	assert.NotContains(t, frame, "Configuration updated")
	assert.Contains(t, setupBoxText(frame, 0, 120), "does not roll back")
	assert.Contains(t, frame, "● Apply", "a failed apply does not mark the last step done")

	table := ansi.Strip(setupOpTable(ui.NewTheme(false), f.clients, body.result.res.Ops, 90) + "\n" +
		setupOpTable(ui.NewTheme(false), f.clients, body.result.res.Skills, 90))
	assert.NotEmpty(t, setupLineWith(table, "✓ file", ".codex/config.toml", "updated"))
	assert.NotEmpty(t, setupLineWith(table, "! file", ".codex/hooks.json", "failed"))
	assert.Contains(t, table, "the file is read-only")
	assert.NotEmpty(t, setupLineWith(table, "! skill", "seamark-plan-change", "not attempted"))
	assert.Equal(t, []string{"PlanSetup", "ApplySetup"}, f.called(), "a failure never retries")
}

func TestSetupBusySlotStartsNoApply(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	require.True(t, h.m.st.begin("Building the index"))
	h.press("y")

	assert.Equal(t, []string{"PlanSetup"}, f.called())
	assert.Equal(t, setupStepPreview, setupScreen(h).wizard.step)
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
}

func TestSetupNarrowReviewOpensTheDiffAndKeepsItOnResize(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 80, 24)

	assertFrame(t, h.view(), 80, 24)

	h.press("enter", "down")
	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ Plan ─")
	assert.NotContains(t, frame, "@@", "the narrow review shows one box at a time")
	assert.Contains(t, frame, "enter diff")

	h.press("enter")
	frame = h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ .codex/hooks.json ─")
	assert.Contains(t, frame, "      1 + {\"hooks\": {}}")
	assert.Contains(t, frame, "esc files")

	h.resize(120, 32)
	frame = h.view()
	assertFrame(t, frame, 120, 32)
	assert.Equal(t, 1, setupScreen(h).wizard.paths.Cursor, "a resize keeps the selected path")
	assert.Contains(t, frame, "{\"hooks\": {}}")
	assert.Contains(t, frame, "╭─ Plan ─", "the wide review shows both boxes")

	h.resize(80, 24)
	h.press("esc")
	assert.Contains(t, h.view(), "╭─ Plan ─", "esc closes the diff first")
	assert.NotNil(t, setupScreen(h).wizard)
}

func TestSetupFramesFitEverySize(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		f, _ := setupFake()
		f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
			return integration.SetupResult{Ops: []integration.OpResult{
				{Path: ".codex/config.toml", Status: integration.OpApplied, Consumers: []string{"codex"}},
			}}, nil
		}

		h := setupOpen(t, f, size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])

		h.press("enter")
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "Plan", "size %v", size)

		h.press("enter")
		assertFrame(t, h.view(), size[0], size[1])

		h.press("y")
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "Configuration updated", "size %v", size)
	}
}

func TestSetupPlanNotesScrollInANarrowTerminal(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 60, 20)
	h.press("enter")

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	assert.Contains(t, frame, "… notes 1–", "hidden notes are announced")
	assert.Contains(t, frame, "· pgup/pgdn")
	assert.Contains(t, frame, "Nothing is written until you press y.", "the apply line always shows")

	h.press("pgdown")
	assert.NotEqual(t, frame, h.view(), "pgdn scrolls the plan notes")
	assert.Equal(t, []string{"PlanSetup"}, f.called())
}

func TestSetupDiffKeepsSignsAndInertContent(t *testing.T) {
	th := ui.NewTheme(false)

	diff := setupDiff(th, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old \x1b[31mred\n+new \x1b]0;t\x07line\n same\n", 30)
	assert.NotContains(t, diff, "\x1b[31m")
	assert.NotContains(t, diff, "\x07")
	assert.NotContains(t, diff, "+++", "the file header lines are dropped")

	for _, line := range strings.Split(diff, "\n") {
		runes := []rune(line)
		require.Greater(t, len(runes), 8, "line %q", line)
		assert.Contains(t, "+−@ ", string(runes[8]), "every row keeps its sign after the gutter: %q", line)
	}

	long := strings.Split(setupDiff(th, "@@ -0,0 +1 @@\n+"+strings.Repeat("abcdefghij", 5)+"\n", 24), "\n")
	require.Greater(t, len(long), 3)

	for _, line := range long[1:] {
		assert.Equal(t, "+", string([]rune(line)[8]), "a wrapped row keeps its sign: %q", line)
		assert.LessOrEqual(t, ansi.StringWidth(line), 24)
	}

	assert.True(t, strings.HasPrefix(long[1], "      1 + "), "the first row carries the new line number")
	assert.True(t, strings.HasPrefix(long[2], "        + "), "a continuation row has an empty gutter")
}

func TestSetupParseDiffNumbersEveryHunk(t *testing.T) {
	rows := setupParseDiff("--- a/x\n+++ b/x\n@@ -14,3 +14,4 @@\n a\n-b\n+c\n+d\n e\n@@ -40,0 +41 @@\n+z\n")

	require.Len(t, rows, 8)
	assert.Equal(t, setupDiffRow{sign: ' ', old: 14, new: 14, text: "a"}, rows[1])
	assert.Equal(t, setupDiffRow{sign: '-', old: 15, text: "b"}, rows[2])
	assert.Equal(t, setupDiffRow{sign: '+', new: 15, text: "c"}, rows[3])
	assert.Equal(t, setupDiffRow{sign: '+', new: 16, text: "d"}, rows[4])
	assert.Equal(t, setupDiffRow{sign: ' ', old: 16, new: 17, text: "e"}, rows[5])
	assert.Equal(t, setupDiffRow{sign: '+', new: 41, text: "z"}, rows[7], "an empty old range names the line before it")
}

func TestSetupControlSequencesInPlanRenderInert(t *testing.T) {
	f, _ := setupFake()
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		p := setupPlanFor(setups)
		p.Plan.Writes[0].Detail = "adds \x1b[2J registration"
		p.Plan.Findings[0].Reason = "evil \x1b[31m finding"
		p.Plan.Kept[0].Detail = "kept \x1b]0;x\x07"
		p.Writes[0].Diff = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n+evil \x1b[2J line\n"

		return p, nil
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	for range setupEntries(setupScreen(h).wizard.preview) {
		assert.NotContains(t, h.m.View(), "\x1b")
		h.press("down")
	}

	wz := setupScreen(h).wizard
	body, tail := wz.planNotes(setupEntries(wz.preview), 80)
	assert.NotContains(t, strings.Join(append(body, tail...), "\n"), "\x1b")
}

func TestSetupClientStepKeepsChoicesPerClient(t *testing.T) {
	f, requests := setupFake()
	h := newHarness(t, f, 120, 32)
	h.press("2")
	h.send(navigateMsg{dest: destIntegrations, intent: "setup"})

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "╭─ Choose a client ─")
	assert.Contains(t, frame, "● Client")
	assert.Contains(t, frame, "Claude Code")
	assert.Contains(t, frame, "not set up")

	// Codex: uncheck MCP registration, then go back and pick Claude.
	h.press("down", "enter", "space")
	assert.Contains(t, h.view(), "[ ] MCP registration")

	h.press("esc", "up", "enter")
	assert.Equal(t, "claude", setupScreen(h).wizard.client.ID)
	assert.Contains(t, h.view(), "[=] MCP registration", "Claude has MCP registration current")

	h.press("esc", "down", "enter")
	assert.Contains(t, h.view(), "[✓] MCP registration", "a new client starts from its defaults")

	h.press("esc", "esc")
	assert.Nil(t, setupScreen(h).wizard)
	assert.Empty(t, *requests)
}

func TestSetupStaleAfterAWriteNeverClaimsNothingWritten(t *testing.T) {
	f, _ := setupFake()
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		stale := fmt.Errorf("%w: .codex/hooks.json changed since it was planned; run the command again",
			integration.ErrStalePlan)

		return integration.SetupResult{Ops: []integration.OpResult{
			{Path: ".codex/config.toml", Status: integration.OpApplied},
			{Path: ".codex/hooks.json", Status: integration.OpFailed, Err: stale},
		}}, fmt.Errorf(".codex/hooks.json: %w", stale)
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter", "y")

	frame := setupBoxText(h.view(), 0, 120)
	assert.Contains(t, frame, "Setup stopped")
	assert.NotContains(t, frame, "Nothing was written")
	assert.Contains(t, frame, "does not roll back")
}

// setupOpTable renders the operation log as one block of w cells.
func setupOpTable(th *ui.Theme, clients []integration.Client, ops []integration.OpResult, w int) string {
	return strings.Join(setupOpLines(th, clients, ops, w), "\n")
}

// setupDiff renders a unified diff as one block of w cells.
func setupDiff(th *ui.Theme, diff string, w int) string {
	return strings.Join(setupDiffLines(th, diff, w), "\n")
}

// The tests below prove that a plan or apply result reaches only the
// wizard that asked for it. Every new wizard counts seq from zero, so
// seq alone cannot tell two wizards apart.

// setupHold sends one key press and returns its commands without
// running them. A test runs them later to deliver a late result.
func setupHold(h *harness, k string) tea.Cmd {
	h.t.Helper()

	_, cmd := h.m.Update(keyMsg(k))

	return cmd
}

// setupCloseWithHeldPlan opens a Codex wizard, checks the managed
// skills, starts its plan without running it, and closes the wizard.
// It returns the held plan command and the closed wizard.
func setupCloseWithHeldPlan(t *testing.T, h *harness) (tea.Cmd, *setupWizard) {
	t.Helper()

	old := setupScreen(h).wizard
	require.NotNil(t, old)

	h.press("down", "down", "space")
	held := setupHold(h, "enter")
	require.True(t, old.preparing)

	h.press("esc", "esc")
	require.Nil(t, setupScreen(h).wizard)

	return held, old
}

func TestSetupLatePlanOfClosedWizardArrivesAfterTheNewPlan(t *testing.T) {
	for _, next := range []string{"claude", "codex"} {
		t.Run(next, func(t *testing.T) {
			f, requests := setupFake()

			var applied *SetupPreview

			f.applySetup = func(p *SetupPreview) (integration.SetupResult, error) {
				applied = p

				return integration.SetupResult{}, nil
			}

			h := setupOpen(t, f, 120, 32)
			held, old := setupCloseWithHeldPlan(t, h)

			h.send(navigateMsg{dest: destIntegrations, intent: "setup:" + next})
			wz := setupScreen(h).wizard
			require.NotSame(t, old, wz)

			// The new wizard plans first; the closed wizard's plan
			// arrives last.
			h.press("enter")
			require.NotNil(t, wz.preview)

			reviewed := wz.preview

			h.run(held)
			require.Len(t, *requests, 2)
			require.NotEqual(t, (*requests)[0], (*requests)[1], "the two wizards asked for different plans")

			assert.Same(t, reviewed, wz.preview, "the late plan of a closed wizard is dropped")
			assert.Equal(t, (*requests)[0], wz.preview.Setups)
			assert.Contains(t, h.view(), "Set up "+wz.client.Name)

			h.press("y")
			require.NotNil(t, applied)
			assert.Same(t, reviewed, applied, "apply takes the plan that the new wizard reviewed")
			assert.Equal(t, next, applied.Setups[0].ClientID)
		})
	}
}

func TestSetupLatePlanOfClosedWizardArrivesBeforeTheNewPlan(t *testing.T) {
	for _, next := range []string{"claude", "codex"} {
		t.Run(next, func(t *testing.T) {
			f, requests := setupFake()
			h := setupOpen(t, f, 120, 32)
			heldOld, _ := setupCloseWithHeldPlan(t, h)

			h.send(navigateMsg{dest: destIntegrations, intent: "setup:" + next})
			wz := setupScreen(h).wizard

			heldNew := setupHold(h, "enter")
			require.True(t, wz.preparing)

			// The closed wizard's plan arrives while the new one plans.
			h.run(heldOld)
			assert.True(t, wz.preparing, "a foreign plan does not end the planning")
			assert.Nil(t, wz.preview)
			assert.Contains(t, h.view(), "Planning the setup")

			h.run(heldNew)
			require.Len(t, *requests, 2)
			require.NotNil(t, wz.preview)
			assert.False(t, wz.preparing)
			assert.Equal(t, (*requests)[1], wz.preview.Setups, "the preview is the new wizard's own plan")
			assert.Equal(t, next, wz.preview.Setups[0].ClientID)
		})
	}
}

func TestSetupApplyResultOfAnotherWizardIsDropped(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	wz := setupScreen(h).wizard
	require.Equal(t, setupStepPreview, wz.step)

	wz.finished(setupAppliedMsg{owner: newSetupWizard(h.m.st, f.clients), seq: wz.seq.last}, 0)
	assert.Equal(t, setupStepPreview, wz.step)
	assert.Nil(t, wz.result)

	wz.finished(setupAppliedMsg{owner: wz, seq: wz.seq.last}, 0)
	assert.Equal(t, setupStepResult, wz.step, "the wizard's own result arrives")
}
