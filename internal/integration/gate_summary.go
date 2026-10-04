package integration

import (
	"slices"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// GateSummary groups the gate hooks of a workspace by what they do with
// a blocking verdict. init builds it from the hooks a run leaves, and
// status from the inspection of every client, so the two commands
// classify one state by one rule and differ only in their words. The
// labels are the caller's: document paths for init, client IDs for
// status. Each label appears once per group.
type GateSummary struct {
	// Enforce lists the hooks that carry --enforce. A verdict of theirs
	// blocks, whatever the policy file says.
	Enforce []string
	// Warn lists the hooks that follow the policy file. A verdict of
	// theirs blocks only when the policy file enforces.
	Warn []string
	// ReportOnly lists the hooks that discard their exit status. No
	// verdict of theirs blocks, whatever --enforce or the policy file
	// says.
	ReportOnly []string
	// Possible lists the definitions that can run a gate hook, where the
	// reader cannot tell whether they do. What they do is unknown, not
	// nothing.
	Possible []string
	// Unreadable lists the hook documents that cannot be read. What
	// their hooks do is unknown, not nothing.
	Unreadable []string
}

// GateEffect is what the gate hooks of a workspace do with a blocking
// verdict, from strongest to weakest. One enforcing hook blocks
// whatever the others do, and one warn hook follows the policy file
// whatever a report-only hook beside it does. An unreadable hook
// document and a definition that may run a gate outrank a report-only
// hook. Either can hold an enforcing gate, so "nothing blocks" is not
// known.
type GateEffect int

// The gate effects, in order of strength.
const (
	// GateEffectNone means no gate hook runs.
	GateEffectNone GateEffect = iota
	// GateEffectReportOnly means every gate hook that runs discards its
	// exit status: verdicts are reported, nothing blocks.
	GateEffectReportOnly
	// GateEffectUnknown means no gate hook is known to block, and a hook
	// document cannot be read or a definition may run a gate, so one may.
	GateEffectUnknown
	// GateEffectFollowsPolicy means a warn hook runs: the policy file
	// decides whether a verdict blocks.
	GateEffectFollowsPolicy
	// GateEffectBlocks means an enforcing hook runs: blocking verdicts
	// exit 2.
	GateEffectBlocks
)

// Add records one hook that runs, by its mode. A mode other than
// enforce, warn, and report-only records nothing.
func (g *GateSummary) Add(label, mode string) {
	switch mode {
	case hooks.ModeEnforce:
		g.Enforce = addLabel(g.Enforce, label)
	case hooks.ModeWarn:
		g.Warn = addLabel(g.Warn, label)
	case GateModeReportOnly:
		g.ReportOnly = addLabel(g.ReportOnly, label)
	}
}

// AddPossible records one definition that may run a gate hook.
func (g *GateSummary) AddPossible(label string) {
	g.Possible = addLabel(g.Possible, label)
}

// AddUnreadable records one hook document that cannot be read.
func (g *GateSummary) AddUnreadable(label string) {
	g.Unreadable = addLabel(g.Unreadable, label)
}

// addLabel appends a label once.
func addLabel(labels []string, label string) []string {
	if slices.Contains(labels, label) {
		return labels
	}

	return append(labels, label)
}

// Effect returns the strongest effect among the hooks.
func (g GateSummary) Effect() GateEffect {
	switch {
	case len(g.Enforce) > 0:
		return GateEffectBlocks
	case len(g.Warn) > 0:
		return GateEffectFollowsPolicy
	case len(g.Unreadable) > 0 || len(g.Possible) > 0:
		return GateEffectUnknown
	case len(g.ReportOnly) > 0:
		return GateEffectReportOnly
	default:
		return GateEffectNone
	}
}

// SummarizeGateHooks groups the gate hooks of a setup run by their
// document path. A hook that may run is possible when it could block.
func SummarizeGateHooks(gateHooks []GateHook) GateSummary {
	var g GateSummary

	for _, hook := range gateHooks {
		g.addHook(hook.Path, hook.Mode, hook.Uncertain)
	}

	return g
}

// addHook records one hook by its mode and its certainty. An uncertain
// hook in warn or enforce mode is possible: it may block. An uncertain
// report-only hook is report-only, not possible: its exit status never
// reaches the client, so the reader knows that it never blocks, and
// "unknown" would say less than is known.
func (g *GateSummary) addHook(label, mode string, uncertain bool) {
	if uncertain && mode != GateModeReportOnly {
		g.AddPossible(label)

		return
	}

	g.Add(label, mode)
}

// SummarizeInspections groups the gate hooks of every client that has a
// command gate by its client ID. A client whose hook document cannot be
// read is unreadable, with the detail of its commands entry. A client
// with a definition that may run a gate is possible, beside the mode of
// the definitions that certainly run one.
func SummarizeInspections(inspections []Inspection) GateSummary {
	var g GateSummary

	for _, insp := range inspections {
		commands, ok := insp.Entry(CapabilityCommands)
		if !ok || !commands.Supported {
			continue
		}

		if commands.State == StateUnreadable {
			g.AddUnreadable(insp.ClientID + ": " + commands.Detail)

			continue
		}

		g.Add(insp.ClientID, insp.GateMode)

		if insp.PossibleGateMode != "" {
			g.addHook(insp.ClientID, insp.PossibleGateMode, true)
		}
	}

	return g
}
