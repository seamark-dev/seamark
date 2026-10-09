package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// integClaudeSetup and integCodexSetup mirror the built-in descriptors,
// because the registry fills Setup in every real inspection.
var (
	integClaudeSetup = integration.SetupSupport{Hooks: true, GateHook: true, ResetHook: true, RegisterMCP: true, ApproveTools: true}
	integCodexSetup  = integration.SetupSupport{Hooks: true, GateHook: true, RegisterMCP: true, ApproveTools: true}
)

// integCap builds one supported capability entry.
func integCap(c integration.Capability, s integration.CapabilityState, detail, action string) integration.CapabilityInspection {
	return integration.CapabilityInspection{Capability: c, Supported: true, State: s, Detail: detail, Action: action}
}

// integMatrix is a Claude inspection that covers every configuration
// state, unknown and pending trust, and every evidence level, plus a
// Codex inspection with nothing configured.
func integMatrix() []integration.Inspection {
	skills := integCap(integration.CapabilitySkills, integration.StatePartial,
		".claude/skills: 3 current, 1 stale", "re-run `seamark init --skills` to refresh the managed skills")
	skills.Verification = integration.VerificationEvidence{Level: integration.VerificationUnverified}

	mcp := integCap(integration.CapabilityMCPRegistration, integration.StateCurrent, "registered as seamark", "")
	mcp.Trust = integration.TrustPending
	mcp.Verification = integration.VerificationEvidence{Level: integration.VerificationPending, Surface: "project .mcp.json"}

	grants := integCap(integration.CapabilityToolGrants, integration.StateConflict,
		"a deny rule blocks mcp__seamark__why", "remove the deny rule for mcp__seamark__why")

	edits := integCap(integration.CapabilityEdits, integration.StateCurrent, "", "")
	edits.Verification = integration.VerificationEvidence{
		Level: integration.VerificationVerified, ClientVersion: "claude 2.1.0", Surface: "PreToolUse Edit",
	}

	commands := integCap(integration.CapabilityCommands, integration.StateCurrent, "", "")

	resets := integCap(integration.CapabilityResets, integration.StateUnreadable,
		"unreadable (invalid character '}' in .claude/settings.local.json)",
		"fix or move .claude/settings.local.json, then re-run setup")

	claude := integration.Inspection{
		ClientID: "claude", Name: "Claude Code", Setup: integClaudeSetup,
		Capabilities: []integration.CapabilityInspection{skills, mcp, grants, edits, commands, resets},
		GateMode:     "enforce", ManagedGateMode: "warn",
		Findings: []integration.Finding{{
			Level: integration.FindingWarning, Path: ".claude/settings.local.json",
			Reason: "a second source runs the seamark gate hook", Action: "remove the duplicate hook from .claude/settings.local.json",
		}},
	}

	codex := integration.Inspection{ClientID: "codex", Name: "Codex", Setup: integCodexSetup}

	for _, c := range integration.ConfiguredCapabilities {
		entry := integCap(c, integration.StateAbsent, "", "")
		if c == integration.CapabilitySkills {
			entry.Verification = integration.VerificationEvidence{
				Level: integration.VerificationVerified, ClientVersion: "codex-cli 0.152.1", Surface: ".agents/skills",
			}
		}

		codex.Capabilities = append(codex.Capabilities, entry)
	}

	return []integration.Inspection{claude, codex}
}

// integHealthy is a Claude inspection with every capability current,
// unknown trust and no native check, as a fresh Claude setup reports.
func integHealthy() integration.Inspection {
	insp := integration.Inspection{
		ClientID: "claude", Name: "Claude Code", Setup: integClaudeSetup, GateMode: "warn", ManagedGateMode: "warn",
	}

	details := map[integration.Capability]string{
		integration.CapabilitySkills:          ".claude/skills 3/3 current",
		integration.CapabilityMCPRegistration: `registered in .mcp.json as "seamark"`,
		integration.CapabilityToolGrants:      "8/8 rules",
		integration.CapabilityEdits:           "lessons hook installed",
		integration.CapabilityCommands:        "gate hook (warn) installed",
		integration.CapabilityResets:          "context reset hook installed",
	}

	for _, c := range integration.ConfiguredCapabilities {
		entry := integCap(c, integration.StateCurrent, details[c], "")
		entry.Verification = integration.VerificationEvidence{Level: integration.VerificationUnverified}
		insp.Capabilities = append(insp.Capabilities, entry)
	}

	return insp
}

// integFlat joins wrapped prose into one line, so a test can match a
// sentence whatever the pane width.
func integFlat(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// integDetailText renders the detail lines of one inspection as plain
// text.
func integDetailText(h *harness, insp integration.Inspection, w int) string {
	s := h.m.screens[destIntegrations].(*integrations)

	return ansi.Strip(strings.Join(s.detailLines(insp, w), "\n"))
}

func TestIntegrationsLoadingState(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 32)

	h.m.st.integrations = section[[]integration.Inspection]{loading: true}
	h.press("2")

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	assert.Contains(t, frame, "Reading integrations")
	assert.Contains(t, frame, "Nothing is written")
}

func TestIntegrationsListAndDetailSeparateTheDimensions(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 120, 40)
	h.press("2")

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	// The Clients box counts current capabilities, never "connected".
	assert.Contains(t, frame, "╭─ Clients ─")
	assert.Contains(t, frame, "› Claude Code")
	assert.Contains(t, frame, "! 3/6")
	assert.Contains(t, frame, "gate enforce · 1 partial")
	assert.Contains(t, frame, "1 conflict · 1 unreadable")
	assert.Contains(t, frame, "· not set up")
	assert.Contains(t, frame, "5 not configured")
	assert.NotContains(t, frame, "Connected")
	assert.NotContains(t, frame, "Working")

	// The table keeps configuration, trust and evidence apart.
	assert.Contains(t, frame, "CAPABILITIES")

	for _, column := range []string{"Capability", "Config", "Where", "Trust", "Native"} {
		assert.Contains(t, frame, column)
	}

	assert.Contains(t, frame, "! partial")
	assert.Contains(t, frame, "! conflict")
	assert.Contains(t, frame, "! unreadable")
	assert.Contains(t, frame, "? unknown")
	assert.Contains(t, frame, "· pending")
	assert.Contains(t, frame, "✓ verified")
	assert.Contains(t, frame, "· unverified")

	// Unknown trust is never rendered as success.
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "Edit lessons hook") {
			assert.Contains(t, line, "✓ current")
			assert.Contains(t, line, "? unknown")
			assert.NotContains(t, line, "✓ unknown")
		}
	}

	require.Empty(t, f.called(), "browsing must not plan or mutate")
}

func TestIntegrationsBoxesFollowTheDesignWidths(t *testing.T) {
	f := newFake()
	codex := integMatrix()[1]
	for i := range codex.Capabilities {
		if codex.Capabilities[i].Capability != integration.CapabilityResets {
			codex.Capabilities[i].State = integration.StateCurrent
		}
	}

	f.integrations = []integration.Inspection{integHealthy(), codex}

	h := newHarness(t, f, 120, 40)
	h.press("2")

	lines := strings.Split(h.view(), "\n")
	require.GreaterOrEqual(t, len(lines), 6)

	// Row 5 holds the top borders: Clients is 34 cells wide, then one gap.
	top := lines[4]
	assert.True(t, strings.HasPrefix(top, "╭─ Clients "), top)
	assert.Equal(t, "╮", string([]rune(top)[33]), "the Clients box is 34 cells wide")
	assert.Equal(t, "╭", string([]rune(top)[35]), "the detail box starts after a one-cell gap")
	assert.Equal(t, "╮", string([]rune(top)[119]), "the detail box takes the rest of the width")

	frame := h.view()
	assert.Contains(t, frame, "✓ 6/6")
	assert.Contains(t, frame, "gate warn · lessons · MCP")
	assert.Contains(t, frame, "✓ 5/5 · 1 n/a", "the reset hook of Codex does not apply")
	assert.Contains(t, frame, "8/8 rules", "the Where column shows the typed detail")
	assert.Contains(t, frame, "No configured capability is natively verified.")
	assert.Contains(t, frame, "Project trust is unknown for every configured capability")
	assert.Contains(t, frame, "✓ No findings.")
	assert.NotContains(t, frame, "NEXT", "a client with every capability current has no next step")
}

func TestIntegrationsDetailScrollsToEverySection(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 120, 32)
	h.press("2", "tab")

	body := integFlat(integDetailText(h, f.integrations[0], 81))

	for _, want := range []string{
		"Supports skills", "CAPABILITIES",
		"natively verified on claude 2.1.0 (PreToolUse Edit)", "native check pending (project .mcp.json)",
		"GATE", "Effective mode enforce from every gate hook that runs", "Managed hook warn",
		"FINDINGS", "! warning", "a second source runs the seamark gate hook", "› remove the duplicate hook",
		"SHARED RESOURCES", ".claude/skills Claude Code", ".agents/skills Codex", "shared by every client",
		"NEXT", "Skills .claude/skills: 3 current, 1 stale", "› re-run `seamark init --skills`",
		"› remove the deny rule for mcp__seamark__why", "fix or move .claude/settings.local.json",
	} {
		assert.Contains(t, body, want)
	}

	// The detail box scrolls with the movement keys while it has focus,
	// and its bottom border shows the visible range.
	before := h.view()
	assert.Contains(t, before, "1–")

	h.press("pgdown")
	assert.NotEqual(t, before, h.view())
	require.Empty(t, f.called())
}

func TestIntegrationsNarrowLayoutOpensDetailAndReturns(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 80, 24)
	h.press("2", "down")

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "Clients")
	assert.NotContains(t, frame, "CAPABILITIES")
	assert.Contains(t, frame, "enter details")

	h.press("enter")

	frame = h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ Codex ")
	assert.Contains(t, frame, "CAPABILITIES")
	assert.Contains(t, frame, "esc clients")

	h.press("esc")
	assert.Contains(t, h.view(), "Clients")
	assert.Equal(t, 1, h.m.screens[destIntegrations].(*integrations).list.Cursor, "the cursor survives the detail")

	h.resize(120, 40)
	assertFrame(t, h.view(), 120, 40)
	assert.Equal(t, 1, h.m.screens[destIntegrations].(*integrations).list.Cursor, "the cursor survives a resize")
	require.Empty(t, f.called())
}

func TestIntegrationsFramesFitEverySize(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h := newHarness(t, f, size[0], size[1])
		h.press("2")
		assertFrame(t, h.view(), size[0], size[1])

		h.press("enter")
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "Claude Code", "size %v", size)
	}

	require.Empty(t, f.called())
}

func TestIntegrationsNarrowTableMovesTheDetailUnderTheRow(t *testing.T) {
	f := newFake()
	f.integrations = []integration.Inspection{integHealthy()}

	h := newHarness(t, f, 60, 20)
	text := integDetailText(h, f.integrations[0], 56)

	assert.NotContains(t, text, "Where", "a narrow table has no Where column")
	assert.Contains(t, text, "  8/8 rules", "the detail moves under its row")

	for _, line := range strings.Split(text, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 56, "line %q", line)
	}
}

func TestIntegrationsSharedResourcesNameConsumers(t *testing.T) {
	f := newFake()
	f.clients = inspecttest.Registry().Clients()

	shared := strings.Join(integShared(ui.NewTheme(false), f.clients, 80), "\n")

	assert.Contains(t, shared, ".claude/skills")
	assert.Contains(t, shared, "Claude Code")
	assert.Contains(t, shared, ".agents/skills")
	assert.Contains(t, shared, "Codex, Shared Agent")
}

func TestIntegrationsOpenIntents(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 120, 32)
	s := h.m.screens[destIntegrations].(*integrations)

	h.send(navigateMsg{dest: destIntegrations, intent: "client:codex"})
	assert.Equal(t, 1, s.list.Cursor)
	assert.Nil(t, s.wizard)

	h.send(navigateMsg{dest: destIntegrations, intent: "setup"})
	require.NotNil(t, s.wizard)
	assert.Equal(t, setupStepClient, s.wizard.step)
	assert.Contains(t, h.view(), "Choose a client")

	h.send(navigateMsg{dest: destIntegrations, intent: "setup:claude"})
	require.NotNil(t, s.wizard)
	assert.Equal(t, setupStepChoices, s.wizard.step)
	assert.Equal(t, "claude", s.wizard.client.ID)

	h.send(navigateMsg{dest: destIntegrations, intent: "setup:nope"})
	assert.Equal(t, setupStepClient, s.wizard.step)
	assert.Contains(t, h.view(), "Unknown client: nope")

	h.press("esc")
	assert.Nil(t, s.wizard)
	require.Empty(t, f.called(), "opening setup plans nothing until the user continues")
}

func TestIntegrationsIntentBeforeFirstSnapshot(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 120, 32)
	s := h.m.screens[destIntegrations].(*integrations)

	// Drop the snapshot so the intent arrives first.
	h.m.st.integrations = section[[]integration.Inspection]{}
	s.open("client:codex")
	assert.Equal(t, "codex", s.pendingClient)

	h.send(h.m.st.load(secIntegrations)())
	assert.Equal(t, 1, s.list.Cursor)
	assert.Empty(t, s.pendingClient)
}

func TestIntegrationsRenderControlSequencesInert(t *testing.T) {
	f := newFake()
	insps := integMatrix()
	insps[0].Name = "Claude \x1b[31mCode"
	insps[0].Capabilities[0].Detail = "stale \x1b[2Jcopy"
	insps[0].Capabilities[0].Action = "run \x1b]0;title\x07 now"
	insps[0].Findings[0].Reason = "evil \x1b[31mreason"
	f.integrations = insps

	h := newHarness(t, f, 120, 32)
	h.press("2")

	s := h.m.screens[destIntegrations].(*integrations)
	body := strings.Join(s.detailLines(f.integrations[0], 80), "\n")

	assert.NotContains(t, h.m.View(), "\x1b")
	assert.NotContains(t, body, "\x1b")
	assert.NotContains(t, body, "\x07")
}

func TestIntegrationsASCIIThemeAvoidsUnicodeGlyphs(t *testing.T) {
	f := newFake()
	f.integrations = integMatrix()

	h := newHarness(t, f, 120, 32)
	h.m.st.theme = ui.NewTheme(true)
	h.press("2")

	frame := h.view()
	assertFrame(t, frame, 120, 32)

	for _, r := range frame {
		require.Less(t, r, rune(128), "the ASCII theme renders %q:\n%s", r, frame)
	}

	assert.Contains(t, frame, "+ current")
	assert.Contains(t, frame, "! conflict")
}

func TestIntegrationsCountIgnoresResetAbsentByDesign(t *testing.T) {
	insp := integration.Inspection{ClientID: "codex", Setup: integCodexSetup}

	for _, c := range integration.ConfiguredCapabilities {
		state := integration.StateCurrent
		if c == integration.CapabilityResets {
			state = integration.StateAbsent
		}

		insp.Capabilities = append(insp.Capabilities, integCap(c, state, "", ""))
	}

	tally := integCount(insp)
	assert.Equal(t, 5, tally.expected)
	assert.Equal(t, 1, tally.na)
	assert.Equal(t, "✓ 5/5 · 1 n/a", ansi.Strip(integBadge(ui.NewTheme(false), tally)))
	assert.Equal(t, []string{"lessons", "MCP", "skills", "grants"}, integSummaryParts(insp, tally))

	// The reset row of the table says why it does not apply.
	reset := insp.Capabilities[len(insp.Capabilities)-1]
	assert.Equal(t, "· n/a", ansi.Strip(integConfigCell(ui.NewTheme(false), insp, reset)))
	assert.Equal(t, "not installed by setup", integWhere(insp, reset, "Codex"))
}

func TestIntegrationsUnsupportedCapabilityIsNotApplicable(t *testing.T) {
	th := ui.NewTheme(false)
	insp := integration.Inspection{ClientID: "shared", Name: "Shared Agent"}
	c := integration.CapabilityInspection{Capability: integration.CapabilityToolGrants}

	assert.Equal(t, "◌ n/a", ansi.Strip(integConfigCell(th, insp, c)))
	assert.Equal(t, "·", ansi.Strip(integTrustCell(th, c)))
	assert.Equal(t, "·", ansi.Strip(integNativeCell(th, c)))
	assert.Equal(t, "not supported by Shared Agent", integWhere(insp, c, "Shared Agent"))
}
