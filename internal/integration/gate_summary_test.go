package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/hooks"
)

func TestGateSummaryGroupsHooksAndRanksTheEffect(t *testing.T) {
	var g GateSummary
	assert.Equal(t, GateEffectNone, g.Effect())

	// An unknown mode records nothing; a label appears once per group.
	g.Add("a", "")
	g.Add("a", hooks.ModeWarn)
	g.Add("a", hooks.ModeWarn)
	assert.Equal(t, GateSummary{Warn: []string{"a"}}, g)
	assert.Equal(t, GateEffectFollowsPolicy, g.Effect())

	// The strongest effect wins, in the order status and init print.
	g.Add("b", GateModeReportOnly)
	assert.Equal(t, GateEffectFollowsPolicy, g.Effect(), "a warn hook outranks a report-only one")

	g.Add("c", hooks.ModeEnforce)
	assert.Equal(t, GateEffectBlocks, g.Effect())

	// An unreadable document ranks below a hook that blocks or follows
	// the policy, and above a report-only hook: the document can hold an
	// enforcing gate, so "nothing blocks" is not known.
	var unknown GateSummary
	unknown.AddUnreadable("d: unreadable")
	unknown.AddUnreadable("d: unreadable")
	assert.Equal(t, []string{"d: unreadable"}, unknown.Unreadable)
	assert.Equal(t, GateEffectUnknown, unknown.Effect())

	unknown.Add("e", GateModeReportOnly)
	assert.Equal(t, GateEffectUnknown, unknown.Effect())

	unknown.Add("f", hooks.ModeWarn)
	assert.Equal(t, GateEffectFollowsPolicy, unknown.Effect())

	// A definition that may run a gate ranks like an unreadable document:
	// it can hold an enforcing gate, and it is no running hook.
	var possible GateSummary
	possible.AddPossible("g")
	possible.AddPossible("g")
	assert.Equal(t, []string{"g"}, possible.Possible)
	assert.Equal(t, GateEffectUnknown, possible.Effect())

	possible.Add("h", GateModeReportOnly)
	assert.Equal(t, GateEffectUnknown, possible.Effect())

	possible.Add("i", hooks.ModeEnforce)
	assert.Equal(t, GateEffectBlocks, possible.Effect())
}

func TestSummarizeGateHooksGroupsByPath(t *testing.T) {
	g := SummarizeGateHooks([]GateHook{
		{Path: ".claude/settings.json", Mode: hooks.ModeWarn, Managed: true},
		{Path: ".claude/settings.local.json", Mode: hooks.ModeEnforce},
		{Path: ".claude/settings.local.json", Mode: hooks.ModeEnforce},
		{Path: ".codex/hooks.json", Mode: GateModeReportOnly},
		// An uncertain hook is possible, whatever its mode says.
		{Path: ".codex/config.toml [hooks]", Mode: hooks.ModeEnforce, Uncertain: true},
	})

	assert.Equal(t, GateSummary{
		Enforce: []string{".claude/settings.local.json"}, Warn: []string{".claude/settings.json"},
		ReportOnly: []string{".codex/hooks.json"}, Possible: []string{".codex/config.toml [hooks]"},
	}, g)
	assert.Equal(t, GateEffectBlocks, g.Effect())

	// The reported defect: an uncertain enforcing hook alone read as one
	// that enforces.
	g = SummarizeGateHooks([]GateHook{{Path: ".claude/settings.json", Mode: hooks.ModeEnforce, Uncertain: true}})
	assert.Equal(t, GateSummary{Possible: []string{".claude/settings.json"}}, g)
	assert.Equal(t, GateEffectUnknown, g.Effect())

	// An uncertain hook that discards its exit status never blocks, so it
	// is report-only, not unknown: a gate in the background, for example.
	g = SummarizeGateHooks([]GateHook{{Path: ".claude/settings.json", Mode: GateModeReportOnly, Uncertain: true}})
	assert.Equal(t, GateSummary{ReportOnly: []string{".claude/settings.json"}}, g)
	assert.Equal(t, GateEffectReportOnly, g.Effect())
}

func TestSummarizeInspectionsReadsOnlyClientsWithAGate(t *testing.T) {
	entry := func(state CapabilityState, detail string) CapabilityInspection {
		return CapabilityInspection{Capability: CapabilityCommands, Supported: true, State: state, Detail: detail}
	}

	g := SummarizeInspections([]Inspection{
		{ClientID: "claude", GateMode: hooks.ModeWarn, Capabilities: []CapabilityInspection{entry(StateCurrent, "")}},
		{ClientID: "codex", GateMode: hooks.ModeEnforce, Capabilities: []CapabilityInspection{entry(StateUnreadable, "unreadable: boom")}},
		// No gate capability at all: the client is left out, whatever its mode says.
		{ClientID: "fakeagent", GateMode: hooks.ModeEnforce, Capabilities: []CapabilityInspection{{Capability: CapabilityCommands}}},
		{ClientID: "none", GateMode: hooks.ModeEnforce},
	})

	assert.Equal(t, GateSummary{Warn: []string{"claude"}, Unreadable: []string{"codex: unreadable: boom"}}, g)
	assert.Equal(t, GateEffectFollowsPolicy, g.Effect())

	// A possible gate mode makes the client possible, beside the mode of
	// the definitions that certainly run.
	g = SummarizeInspections([]Inspection{
		{ClientID: "claude", PossibleGateMode: hooks.ModeEnforce, Capabilities: []CapabilityInspection{entry(StatePartial, "")}},
		{ClientID: "codex", GateMode: GateModeReportOnly, PossibleGateMode: hooks.ModeWarn, Capabilities: []CapabilityInspection{entry(StateCurrent, "")}},
	})

	assert.Equal(t, GateSummary{ReportOnly: []string{"codex"}, Possible: []string{"claude", "codex"}}, g)
	assert.Equal(t, GateEffectUnknown, g.Effect())

	// A possible report-only gate never blocks, so it is report-only.
	g = SummarizeInspections([]Inspection{
		{ClientID: "claude", PossibleGateMode: GateModeReportOnly, Capabilities: []CapabilityInspection{entry(StatePartial, "")}},
	})

	assert.Equal(t, GateSummary{ReportOnly: []string{"claude"}}, g)
	assert.Equal(t, GateEffectReportOnly, g.Effect())
}
