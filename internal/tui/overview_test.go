package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	smodel "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovCap builds one capability inspection for a hand-made client.
func ovCap(c integration.Capability, supported bool, state integration.CapabilityState) integration.CapabilityInspection {
	return integration.CapabilityInspection{
		Capability:   c,
		Supported:    supported,
		State:        state,
		Verification: integration.VerificationEvidence{Level: integration.VerificationUnverified},
	}
}

// ovClaude is Claude Code with stale skills, no tool grants, and every
// other capability current.
func ovClaude() integration.Inspection {
	skills := ovCap(integration.CapabilitySkills, true, integration.StatePartial)
	skills.Detail = ".claude/skills: 1 stale"
	skills.Action = "re-run `seamark init --skills` to refresh the managed skills"

	grants := ovCap(integration.CapabilityToolGrants, true, integration.StateAbsent)
	grants.Action = "run `seamark init --client claude --approve-tools`"

	return integration.Inspection{
		ClientID: "claude",
		Name:     "Claude Code",
		Setup:    integration.SetupSupport{Hooks: true, GateHook: true, ResetHook: true, RegisterMCP: true, ApproveTools: true},
		GateMode: "warn",
		Capabilities: []integration.CapabilityInspection{
			skills,
			ovCap(integration.CapabilityMCPRegistration, true, integration.StateCurrent),
			grants,
			ovCap(integration.CapabilityEdits, true, integration.StateCurrent),
			ovCap(integration.CapabilityCommands, true, integration.StateCurrent),
			ovCap(integration.CapabilityResets, true, integration.StateCurrent),
		},
	}
}

// ovCodex is Codex with hooks installed, no MCP registration, a
// conflicting tool grant, pending native checks, and two findings.
func ovCodex() integration.Inspection {
	mcp := ovCap(integration.CapabilityMCPRegistration, true, integration.StateAbsent)
	mcp.Action = "run `seamark init --client codex` to register the seamark MCP server"

	grants := ovCap(integration.CapabilityToolGrants, true, integration.StateConflict)
	grants.Detail = "a foreign rule denies seamark tools"
	grants.Action = "remove the conflicting rule by hand"

	edits := ovCap(integration.CapabilityEdits, true, integration.StateCurrent)
	edits.Verification = integration.VerificationEvidence{
		Level: integration.VerificationVerified, ClientVersion: "codex-cli 0.154.0", Surface: "PreToolUse apply_patch",
	}

	commands := ovCap(integration.CapabilityCommands, true, integration.StateCurrent)
	commands.Verification = integration.VerificationEvidence{Level: integration.VerificationPending, Surface: "PreToolUse Bash"}

	resets := ovCap(integration.CapabilityResets, true, integration.StateAbsent)
	resets.Detail = "no reset hook is installed"

	return integration.Inspection{
		ClientID: "codex",
		Name:     "Codex",
		GateMode: "warn",
		Setup:    integration.SetupSupport{Hooks: true, GateHook: true, RegisterMCP: true, ApproveTools: true},
		Capabilities: []integration.CapabilityInspection{
			ovCap(integration.CapabilitySkills, true, integration.StateAbsent),
			mcp, grants, edits, commands, resets,
		},
		Findings: []integration.Finding{
			{
				Level: integration.FindingInfo, Path: ".codex/hooks.json",
				Reason: "Codex runs a project hook only after the user trusts it",
				Action: "open the project in Codex and review the hooks with /hooks",
			},
			{
				Level: integration.FindingWarning, Path: ".codex/config.toml",
				Reason: "hooks are turned off", Action: "set [features] hooks = true",
			},
		},
	}
}

// ovShared is a client that declares skills and edits only, so its
// other capabilities are unsupported.
func ovShared() integration.Inspection {
	return integration.Inspection{
		ClientID: "shared",
		Name:     "Shared Agent",
		Setup:    integration.SetupSupport{Hooks: true},
		Capabilities: []integration.CapabilityInspection{
			ovCap(integration.CapabilitySkills, true, integration.StateCurrent),
			ovCap(integration.CapabilityMCPRegistration, false, integration.StateAbsent),
			ovCap(integration.CapabilityToolGrants, false, integration.StateAbsent),
			ovCap(integration.CapabilityEdits, true, integration.StateAbsent),
			ovCap(integration.CapabilityCommands, false, integration.StateAbsent),
			ovCap(integration.CapabilityResets, false, integration.StateAbsent),
		},
	}
}

// ovDoctorReport has every doctor state. The approvals warning overlaps
// the Codex tool-grant conflict, and the git warning overlaps nothing.
func ovDoctorReport() *doctor.Report {
	return &doctor.Report{
		Checks: []doctor.Check{
			{Name: "binary", State: doctor.StateOK, Detail: "seamark test (darwin/arm64)"},
			{Name: "git", State: doctor.StateWarn, Detail: "root is not inside a git repository", Fix: "run seamark inside a git repository"},
			{Name: "policy", State: doctor.StateFail, Detail: "policy.yaml: unknown key deny_all", Fix: "fix .seamark/policy.yaml"},
			{Name: "gh", State: doctor.StateInfo, Detail: "gh not found on PATH", Fix: "install gh for review mining"},
			{Name: "approvals", State: doctor.StateWarn, Detail: "codex conflicting rule", Fix: "remove the conflicting rule"},
		},
		Warns: 2,
		Fails: 1,
	}
}

// ovReadyIndex is a ready, current index with semantic health and a
// compiled policy.
func ovReadyIndex(root, db string) services.IndexView {
	return services.IndexView{
		Probe: workspace.IndexProbe{
			State: workspace.IndexReady, Path: db, Schema: 6, Supported: 6, Root: root,
			IndexedAt: time.Now().Add(-2 * time.Hour).Unix(),
		},
		Status: &status.Status{
			IndexedAt:      time.Now().Add(-2 * time.Hour).Unix(),
			Freshness:      status.FreshCurrent,
			Coverage:       status.Coverage{Known: true, FilesSeen: 1240, FilesParsed: 1200, FilesSkipped: 40},
			Symbols:        12345,
			Edges:          45678,
			History:        store.HistoryWindow{Decisions: 321},
			Lessons:        12,
			ReviewsMinedAt: time.Now().Add(-72 * time.Hour).Unix(),
			GatePolicyMode: "warn",
			DistillClient:  "claude",
		},
		Policy: services.PolicySummary{Mode: "warn", Deny: 2, Approval: 3},
	}
}

// ovPopulated is a workspace with a ready index, three clients, every
// doctor state, and three pending proposals.
func ovPopulated() *fakeServices {
	f := newFake()
	f.doctor = ovDoctorReport()
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.integrations = []integration.Inspection{ovClaude(), ovCodex(), ovShared()}
	f.ledger = &proposals.Ledger{Pending: []smodel.Proposal{
		{ID: 1, Rule: "a", Status: "proposed"}, {ID: 2, Rule: "b", Status: "proposed"}, {ID: 3, Rule: "c", Status: "proposed"},
	}}

	return f
}

// ovScreen returns the Overview screen of a harness.
func ovScreen(h *harness) *overview {
	o, ok := h.m.screens[destOverview].(*overview)
	if !ok {
		panic("the Overview screen is not an *overview")
	}

	return o
}

// ovRowTexts returns every row of the Attention box without styling,
// rendered at w cells.
func ovRowTexts(o *overview, w int) []string {
	items := o.attention()
	rows := o.rows(items)
	out := make([]string, len(rows))

	for i, r := range rows {
		out[i] = ansi.Strip(o.renderRow(r, items, false, false, w))
	}

	return out
}

// ovDetail renders the whole detail of one item at w cells as one block.
func ovDetail(o *overview, it ovItem, w int) string {
	return strings.Join(o.detailLines(it, w), "\n")
}

// ovFind returns the attention item with the key.
func ovFind(t *testing.T, o *overview, key string) ovItem {
	t.Helper()

	for _, it := range o.attention() {
		if it.key == key {
			return it
		}
	}

	require.Failf(t, "attention item missing", "no item with key %q", key)

	return ovItem{}
}

// ovSelect moves the cursor down until the selected item has the key.
func ovSelect(t testing.TB, h *harness, key string) {
	t.Helper()

	o := ovScreen(h)
	_ = h.view()

	for range 100 {
		if o.selKey == key {
			return
		}

		h.press("down")
	}

	require.Failf(t, "item not reachable", "the cursor never reached %q", key)
}

// ovIndexOf returns the position of the first row that contains s.
func ovIndexOf(rows []string, s string) int {
	for i, r := range rows {
		if strings.Contains(r, s) {
			return i
		}
	}

	return -1
}

func TestOverviewGroupsAttentionInOrder(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	rows := ovRowTexts(o, 60)

	headings := []string{"ACTION", "OPTIONAL", "UNSUPPORTED", "UNKNOWN", "CLIENTS", "DOCTOR"}
	last := -1

	for _, heading := range headings {
		i := ovIndexOf(rows, heading)
		require.Greater(t, i, last, "%s must follow the previous group", heading)

		last = i
	}

	at := func(s string) int { return ovIndexOf(rows, s) }
	actionAt, optionalAt, unsupportedAt := at("ACTION  6"), at("OPTIONAL  4"), at("UNSUPPORTED  5")
	unknownAt, clientsAt := at("UNKNOWN"), at("CLIENTS")

	between := func(s string, lo, hi int) {
		t.Helper()

		i := at(s)
		assert.True(t, i > lo && i < hi, "%q is row %d, want a row between %d and %d", s, i, lo, hi)
	}

	// Broken states come first in Action.
	assert.Equal(t, actionAt+1, at("policy: policy.yaml"))
	between("git: root", actionAt, optionalAt)
	between("Claude Code · skills partial", actionAt, optionalAt)
	between("Codex · tool grants conflict", actionAt, optionalAt)
	between("Codex · hooks are turned off", actionAt, optionalAt)
	between("3 proposals await a decision", actionAt, optionalAt)
	assert.Contains(t, rows[at("3 proposals await a decision")], "Lessons ›", "an action row names where enter leads")

	between("Codex · MCP not registered", optionalAt, unsupportedAt)
	between("Claude Code · tool grants not configured", optionalAt, unsupportedAt)
	between("Shared Agent · lessons hook not installed", optionalAt, unsupportedAt)

	between("Shared Agent · MCP not supported", unsupportedAt, unknownAt)
	between("Codex · context reset hook not installed by setup", unsupportedAt, unknownAt)
	assert.Contains(t, rows[at("context reset hook not installed by setup")], "◌", "unsupported rows use the ◌ glyph")

	between("Claude Code · trust not verifiable by Seamark", unknownAt, clientsAt)
	between("Codex · trust not verifiable by Seamark", unknownAt, clientsAt)
	between("Codex · compatibility evidence incomplete", unknownAt, clientsAt)
	assert.Equal(t, -1, at("Claude Code · compatibility evidence"),
		"unverified evidence is a recorded level, not unknown evidence")

	// The approvals warning overlaps the Codex tool-grant item, so it
	// appears once. The full doctor list moved to the Doctor tab.
	assert.Equal(t, -1, at("approvals: codex"))
	assert.Equal(t, -1, at("DOCTOR CHECKS"))
	assert.Equal(t, -1, at("seamark test (darwin/arm64)"), "an ok check stays in the Doctor tab")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Attention")
	assert.Contains(t, frame, "Detail")
	assert.Contains(t, frame, "Doctor check: policy", "the first action item opens in the detail pane")
	assert.Contains(t, frame, "fix .seamark/policy.yaml")
	assert.Empty(t, f.called(), "browsing must not plan, preview, or mutate")
}

func TestOverviewClassifiesCapabilitiesFromTypedData(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)
	o := ovScreen(h)

	skills := ovFind(t, o, "cap:claude:skills")
	assert.Equal(t, ovActionable, skills.group)
	assert.Equal(t, "partial", skills.badge)
	assert.Equal(t, "skills:claude", skills.act.intent)
	assert.Equal(t, destIntegrations, skills.act.dest)

	grants := ovFind(t, o, "cap:codex:tool-grants")
	assert.Equal(t, ovActionable, grants.group)
	assert.Equal(t, "client:codex", grants.act.intent, "a conflict needs the user, not setup")

	mcp := ovFind(t, o, "cap:codex:mcp-registration")
	assert.Equal(t, ovOptional, mcp.group)
	assert.Equal(t, "setup:codex", mcp.act.intent)

	resets := ovFind(t, o, "cap:codex:resets")
	assert.Equal(t, ovUnsupported, resets.group)
	assert.Equal(t, ui.ToneUnsupported, resets.tone)
	assert.Equal(t, ovActNone, resets.act.kind)

	unsupported := ovFind(t, o, "cap:shared:commands")
	assert.Equal(t, ovUnsupported, unsupported.group)
	assert.Equal(t, "not supported", unsupported.badge)

	trust := ovFind(t, o, "trust:codex")
	assert.Equal(t, ovUnknown, trust.group)
	assert.Equal(t, ui.ToneUnknown, trust.tone, "unknown evidence is never green")
	assert.Contains(t, strings.Join(trust.body, " "), "review the hooks with /hooks",
		"informational findings explain the trust limit")

	evidence := ovFind(t, o, "evidence:codex")
	assert.Contains(t, evidence.body[0], "gate hook (check pending)")
	assert.NotContains(t, evidence.body[0], "lessons hook", "verified evidence is not listed as missing")

	finding := ovFind(t, o, "finding:codex:1:.codex/config.toml")
	assert.Equal(t, ovActionable, finding.group)
	assert.Equal(t, "warning", finding.badge)
	assert.Equal(t, "set [features] hooks = true", finding.fix)

	props := ovFind(t, o, "proposals")
	assert.Equal(t, destLessons, props.act.dest)
	assert.Equal(t, "proposals", props.act.intent)
}

func TestOverviewCardsShowIndexNavigateLearnGuard(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	for _, want := range []string{
		"INDEX", "✓ current", "1,200/1,240 files parsed", "no parse errors", "indexed 2h ago",
		"NAVIGATE", "12,345 symbols", "45,678 edges", "321 history decisions",
		"LEARN", "3 pending", "12 lessons", "reviews mined 3d ago", "distill agent claude",
		"GUARD", "mode warn", "gate on Claude Code, Codex", "2 deny · 3 approval rules", "policy compiles",
	} {
		assert.Contains(t, frame, want)
	}

	assert.NotContains(t, frame, "…", "large counts switch to a shorter wording instead of a cut")
	assert.NotEmpty(t, ovScreen(h).tabCount(), "the tab count is known once every source loaded")
}

func TestOverviewAbsentIndexOffersBuild(t *testing.T) {
	f := newFake()
	f.doctor = &doctor.Report{Checks: []doctor.Check{
		{Name: "index", State: doctor.StateInfo, Detail: "no index database yet", Fix: "run `seamark index` to build it"},
	}}
	f.index = services.IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent, Path: f.dbPath}}
	f.integrations = []integration.Inspection{ovClaude()}
	f.ledgerErr = services.ErrNoLedger

	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "○ no index")
	assert.Contains(t, frame, "No index yet")
	assert.Contains(t, frame, "b build the index", "the detail offers the build key")
	assert.Contains(t, frame, "seamark index")
	assert.Contains(t, frame, "proposals unknown: no")
	assert.Contains(t, frame, "readable index")
	assert.NotContains(t, frame, "none pending", "a failed ledger read never renders as zero")
	assert.Contains(t, frame, "Claude Code", "integration inspection works without an index")

	h.press("enter")
	assert.Contains(t, h.view(), "Build the index?")
	assert.True(t, ovScreen(h).capturing())

	h.press("esc")
	assert.NotContains(t, h.view(), "Build the index?")
	assert.Empty(t, f.called())
}

func TestOverviewBuildShowsProgressThenResult(t *testing.T) {
	f := ovPopulated()

	var h *harness

	var midBuild string

	f.buildIndex = func(force bool, progress func(services.IndexEvent)) (*index.Summary, error) {
		assert.False(t, force)

		// The build runs inside a command, so the model is idle here:
		// the modal shows the first phase, and a second b is refused.
		h.press("b")
		midBuild = h.view()

		progress(services.IndexEvent{Phase: "scan", Done: 10, Total: 10})

		for i := 1; i <= 10; i++ {
			progress(services.IndexEvent{Phase: "parse", Done: i, Total: 10})
		}

		progress(services.IndexEvent{Log: "warn: parse a.go: unexpected \x1b[31mtoken"})
		progress(services.IndexEvent{Phase: "history", Done: 0, Total: 0})
		progress(services.IndexEvent{Phase: "history", Done: 1, Total: 1})
		progress(services.IndexEvent{Phase: "write", Done: 0, Total: 0})
		progress(services.IndexEvent{Phase: "write", Done: 1, Total: 1})

		// The build wrote a new index; the refresh after it must show it.
		idx := ovReadyIndex(f.root, f.dbPath)
		idx.Status.Symbols = 50
		f.index = idx

		return &index.Summary{
			DBPath: f.dbPath, FilesSeen: 10, FilesParsed: 9, FilesReparsed: 4, FilesSkipped: 1, ParseErrors: 1,
			HistoryMined: true, Stats: store.Stats{Symbols: 50, Edges: 70, Decisions: 4}, Duration: time.Second,
		}, nil
	}

	h = newHarness(t, f, 120, 40)

	h.press("b")

	dialog := h.view()
	assertFrame(t, dialog, 120, 40)
	assert.Contains(t, dialog, "Build the index?")
	assert.Contains(t, dialog, f.dbPath)
	assert.Contains(t, dialog, "No review fetch, no network call, no inference.")
	assert.Contains(t, dialog, "cannot be cancelled")
	assert.Contains(t, dialog, "f rebuild from scratch")
	assert.Contains(t, dialog, "enter build")

	h.press("enter")

	assert.Contains(t, midBuild, "Building the index")
	assert.Contains(t, midBuild, "This build cannot be cancelled safely. Press q to exit after it")
	assert.Contains(t, midBuild, "Open the database")
	assert.Contains(t, midBuild, "Another operation is running: Building the index")

	assert.Equal(t, []string{"BuildIndex"}, f.called(), "a second b while running must not start a second build")
	assert.False(t, h.m.st.busy(), "the build frees the mutation slot")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Index built")
	assert.Contains(t, frame, "10 seen")
	assert.Contains(t, frame, "9 parsed")
	assert.Contains(t, frame, "4 reparsed")
	assert.Contains(t, frame, "1 skipped")
	assert.Contains(t, frame, "warn: parse a.go")
	assert.Contains(t, frame, "enter close")
	assert.NotContains(t, h.m.View(), "\x1b[31m", "log lines render inert")

	h.press("esc")
	assert.NotContains(t, h.view(), "Index built")
	assert.Contains(t, h.view(), "50 symbols", "the index section reloads after the build")
}

func TestOverviewQuitDuringBuildExitsAfterCompletion(t *testing.T) {
	f := ovPopulated()

	var h *harness

	f.buildIndex = func(_ bool, progress func(services.IndexEvent)) (*index.Summary, error) {
		assert.False(t, ovScreen(h).capturing(), "the progress modal lets q reach the shell")

		h.press("q")
		assert.Contains(t, h.view(), "Exit after the running operation?")

		h.press("enter")
		assert.False(t, h.quit, "q never interrupts a running build")

		progress(services.IndexEvent{Phase: "scan", Done: 1, Total: 1})

		return &index.Summary{}, nil
	}

	h = newHarness(t, f, 120, 40)
	h.press("b", "enter")

	assert.True(t, h.quit, "the console exits once the build finishes")
	assert.Equal(t, []string{"BuildIndex"}, f.called())
}

func TestOverviewBuildSkippedReportsAlreadyCurrent(t *testing.T) {
	f := ovPopulated()
	f.buildIndex = func(force bool, _ func(services.IndexEvent)) (*index.Summary, error) {
		assert.True(t, force, "f starts a rebuild from scratch")

		return &index.Summary{Skipped: true, Stats: store.Stats{Symbols: 7, Edges: 8}}, nil
	}

	h := newHarness(t, f, 120, 40)
	h.press("b", "f")

	frame := h.view()
	assert.Contains(t, frame, "Index already current")
	assert.Contains(t, frame, "Nothing was rebuilt")
	assert.NotContains(t, frame, " seen ", "a skipped build reports no file counts")
	assert.Equal(t, []string{"BuildIndex"}, f.called())
}

func TestOverviewBuildFailureShowsError(t *testing.T) {
	f := ovPopulated()
	f.buildIndex = func(_ bool, progress func(services.IndexEvent)) (*index.Summary, error) {
		progress(services.IndexEvent{Phase: "scan", Done: 3, Total: 3})
		progress(services.IndexEvent{Phase: "parse", Done: 1, Total: 3})

		return nil, errors.New("store: begin rebuild: database is locked")
	}

	h := newHarness(t, f, 120, 40)
	h.press("b", "enter")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Index build failed")
	assert.Contains(t, frame, "store: begin rebuild: database is locked")
	assert.Contains(t, frame, "Refresh view (r) shows what changed; nothing retries")
	assert.Contains(t, frame, "failed")
	assert.False(t, h.m.st.busy())
	assert.Equal(t, []string{"BuildIndex"}, f.called(), "a failure never retries")

	// Other destinations stay usable after the failure.
	h.press("2")
	assert.Equal(t, destIntegrations, h.m.active)
}

func TestOverviewRefusesBuildWhileAnotherMutationRuns(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)

	require.True(t, h.m.st.begin("Applying setup"))
	h.press("b")

	frame := h.view()
	assert.Contains(t, frame, "Another operation is running: Applying setup")
	assert.NotContains(t, frame, "Build the index?")
	assert.Empty(t, f.called())
}

func TestOverviewRefreshViewNeverBuilds(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)

	h.press("r")
	assert.Empty(t, f.called(), "refresh view reads; it never builds")

	// Inside the confirmation, r is not a shortcut.
	h.press("b", "r")
	assert.Contains(t, h.view(), "Build the index?")

	h.press("esc")
	assert.Empty(t, f.called(), "cancelling the confirmation builds nothing")
}

func TestOverviewEnterOnMCPItemOpensSetup(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)

	ovSelect(t, h, "cap:codex:mcp-registration")

	frame := h.view()
	assert.Contains(t, frame, "Codex · MCP not registered")
	assert.Contains(t, frame, "enter set up Codex")

	h.press("enter")
	assert.Equal(t, destIntegrations, h.m.active)
}

func TestOverviewNarrowLayoutOpensDetail(t *testing.T) {
	h := newHarness(t, ovPopulated(), 80, 24)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "Attention")
	assert.Contains(t, frame, "enter for detail")
	assert.NotContains(t, frame, "NAVIGATE", "the narrow layout collapses the cards into one line")
	assert.NotContains(t, frame, "Detail", "the narrow layout shows one box")

	h.press("down")
	key := ovScreen(h).selKey

	h.press("enter")

	detail := h.view()
	assertFrame(t, detail, 80, 24)
	assert.Contains(t, detail, "Detail")
	assert.Contains(t, detail, "NEXT")
	assert.Contains(t, detail, "esc back")

	h.resize(82, 24)
	assert.Contains(t, h.view(), "NEXT", "resize keeps the open detail")

	h.press("esc")
	assert.Contains(t, h.view(), "Attention")
	assert.Equal(t, key, ovScreen(h).selKey, "back keeps the cursor on the same item")
}

func TestOverviewWideLayoutSwitchesFocus(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)
	o := ovScreen(h)

	_ = h.view()
	key := o.selKey

	h.press("tab")
	assert.Equal(t, ovFocusDetail, o.focus)

	h.press("down")
	assert.Equal(t, key, o.selKey, "down scrolls the focused detail, not the list")

	h.press("esc")
	assert.Equal(t, ovFocusList, o.focus)

	h.press("down")
	assert.NotEqual(t, key, o.selKey)

	moved := o.selKey

	h.press("r")
	assert.Equal(t, moved, o.selKey, "refresh keeps the selected item")
}

func TestOverviewKeepsSelectionAcrossRefresh(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)

	ovSelect(t, h, "cap:codex:mcp-registration")

	// A refresh removes an item above the cursor.
	f.doctor = &doctor.Report{Checks: f.doctor.Checks[:1]}
	h.press("r")

	assert.Equal(t, "cap:codex:mcp-registration", ovScreen(h).selKey)
	assert.Contains(t, h.view(), "Codex · MCP not registered")
}

func TestOverviewFreshWorkspaceOffersSetupAndBuild(t *testing.T) {
	f := newFake()
	f.index = services.IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent, Path: f.dbPath}}
	f.integrations = []integration.Inspection{
		{ClientID: "claude", Name: "Claude Code", Capabilities: []integration.CapabilityInspection{
			ovCap(integration.CapabilitySkills, true, integration.StateAbsent),
			ovCap(integration.CapabilityMCPRegistration, true, integration.StateAbsent),
		}},
	}
	f.ledgerErr = services.ErrNoLedger

	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Nothing is set up here yet")
	assert.Contains(t, frame, "Nothing has been written")
	assert.Contains(t, frame, "Set up an integration")
	assert.Contains(t, frame, "Build the index")

	h.press("b")
	assert.Contains(t, h.view(), "Build the index?")
	h.press("esc")

	h.press("enter")
	assert.Contains(t, h.view(), "OPTIONAL", "enter shows the full list")
	assert.Contains(t, h.view(), "s set up", "the footer offers setup while nothing is set up")

	h.press("s")
	assert.Equal(t, destIntegrations, h.m.active, "s returns to guided setup")
	assert.NotContains(t, f.called(), "BuildIndex")
}

func TestOverviewLoadingStateBeforeFirstLoad(t *testing.T) {
	f := ovPopulated()
	m := newModel(f, ui.NewTheme(false))

	// Start the loads without running them, so every source is loading.
	_ = m.st.load(allSections...)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	frame := ansi.Strip(m.View())
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "reading the index")
	assert.Contains(t, frame, "inspecting the integrations")
	assert.Contains(t, frame, "running the doctor checks")
	assert.NotContains(t, frame, "0 pending")
	assert.NotContains(t, frame, "0 symbols")

	o, ok := m.screens[destOverview].(*overview)
	require.True(t, ok)
	assert.Empty(t, o.tabCount(), "the tab count stays hidden while the sources load")
	assert.Empty(t, f.called())
}

func TestOverviewLedgerFailureIsUnknownThenStale(t *testing.T) {
	f := ovPopulated()
	f.ledger, f.ledgerErr = nil, errors.New("database is locked")

	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assert.Contains(t, frame, "? unknown", "the LEARN card states an unknown count")
	assert.Contains(t, frame, "Pending proposal count unknown")
	assert.NotContains(t, frame, "none pending")
	assert.Equal(t, ovUnknown, ovFind(t, ovScreen(h), "proposals").group)

	f.ledger, f.ledgerErr = &proposals.Ledger{Pending: []smodel.Proposal{{ID: 9}, {ID: 10}}}, nil
	h.press("r")
	assert.Contains(t, h.view(), "2 pending")

	f.ledgerErr = errors.New("disk I/O error")
	h.press("r")

	frame = h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "2 pending", "a failed reload keeps the last snapshot")
	assert.Contains(t, frame, "stale: disk I/O error")
}

func TestOverviewProgressShowsParseCountAndSkippedPhases(t *testing.T) {
	st := &state{svc: newFake(), theme: ui.NewTheme(false), width: 120, height: 40}
	o := newOverview(st)

	o.build = newOvBuild(1, false, time.Now())
	o.showBuild = true

	require.Nil(t, o.update(ovBuildEventMsg{id: 2, ev: services.IndexEvent{Phase: "scan", Done: 1, Total: 1}}),
		"an event of another build is ignored")

	o.update(ovBuildEventMsg{id: 1, ev: services.IndexEvent{Phase: "scan", Done: 1240, Total: 1240}})
	o.update(ovBuildEventMsg{id: 1, ev: services.IndexEvent{Phase: "parse", Done: 512, Total: 1240}})

	block, ok := o.modal(120, 39)
	require.True(t, ok, "a running build shows its progress as a modal")

	frame := ansi.Strip(block)
	assert.Contains(t, frame, "1,240 files")
	assert.Contains(t, frame, "512 / 1,240")
	assert.NotContains(t, frame, "%", "progress never invents a percentage")
	assert.Empty(t, o.keys(), "a running build offers no key")

	// A workspace outside git sends no history events.
	o.update(ovBuildEventMsg{id: 1, ev: services.IndexEvent{Phase: "write", Done: 0, Total: 0}})
	assert.Equal(t, phaseSkipped, o.build.phases[ovPhaseIndex("history")])
	assert.Equal(t, phaseDone, o.build.phases[ovPhaseIndex("parse")], "a later phase closes the earlier one")

	o.build.finish(nil, errors.New("boom"), time.Now())
	assert.Equal(t, phaseFailed, o.build.phases[ovPhaseIndex("write")])
}

func TestOverviewControlSequencesRenderInert(t *testing.T) {
	f := ovPopulated()
	f.doctor = &doctor.Report{Checks: []doctor.Check{
		{Name: "git", State: doctor.StateFail, Detail: "evil \x1b[31mred\x1b]0;title\x07 detail", Fix: "run \x1b[2Jclear"},
	}, Fails: 1}
	f.index.Policy = services.PolicySummary{Err: errors.New("policy \x1b[2Jbroken")}
	f.index.Status.DistillClient = "claude\x1b]0;x\x07"

	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		h := newHarness(t, f, size[0], size[1])

		raw := h.m.View()
		assert.NotContains(t, raw, "\x1b[31m")
		assert.NotContains(t, raw, "\x1b[2J")
		assert.NotContains(t, raw, "\x07")
		assert.Contains(t, h.view(), "evil [31mred")
	}
}

func TestOverviewASCIIThemeUsesASCIIGlyphs(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)
	h.m.st.theme = ui.NewTheme(true)

	o := ovScreen(h)

	pure := func(what, frame string) {
		t.Helper()

		for _, r := range ansi.Strip(frame) {
			require.Less(t, r, rune(128), "the ASCII theme renders %q in the %s:\n%s", r, what, ansi.Strip(frame))
		}
	}

	pure("wide view", o.view(120, 35))

	banner, _ := o.banner(120)
	pure("banner", banner)

	h.resize(80, 24)
	pure("narrow view", o.view(80, 19))

	h.press("enter")
	pure("narrow detail", o.view(80, 19))
	h.press("esc")

	h.press("b")

	block, ok := o.modal(80, 23)
	require.True(t, ok)
	pure("confirm modal", block)

	h.press("enter")

	block, ok = o.modal(80, 23)
	require.True(t, ok)
	pure("result modal", block)
}
