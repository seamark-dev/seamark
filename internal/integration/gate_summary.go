package integration

import (
	"slices"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// GateSummary groups the gate hooks of a workspace by what they do with
// a blocking verdict. init builds it from the hooks that a run leaves,
// and status builds it from the inspection of every client. So the two
// commands classify one state by one rule and differ only in their
// words. The labels are the caller's: document paths for init, client
// IDs for status. Each label appears once per group.
type GateSummary struct {
	// Enforce lists the hooks that carry --enforce. A verdict of theirs
	// blocks, whatever the policy file says.
	Enforce []string
	// Warn lists the hooks that follow the policy file. A verdict of
	// theirs blocks only when the policy file enforces.
	Warn []string
	// ReportOnly lists the hooks whose exit status cannot block: they
	// discard it, or the client runs them in the background. No verdict
	// of theirs blocks, whatever --enforce or the policy file says.
	ReportOnly []string
	// Background lists the report-only hooks that the client runs in the
	// background. They keep their exit status, but the client does not
	// wait for it. The words name that cause, so a hook that discards its
	// exit status keeps its text.
	Background []string
	// Possible lists the definitions that can run a gate hook, where the
	// reader cannot tell whether they do. What they do is unknown, not
	// nothing.
	Possible []string
	// FilteredPossible and UnfilteredPossible count the possible hooks of
	// a setup run with and without a Claude Code if field. One document
	// can hold both kinds, so the counts are per hook. The init gate line
	// picks its route from them. Status leaves them at zero.
	FilteredPossible   int
	UnfilteredPossible int
	// Unreadable lists the hook documents that cannot be read. What
	// their hooks do is unknown, not nothing.
	Unreadable []string
	// Inert lists the clients whose gate hook is present and never runs:
	// its binary cannot start, or its matcher never fires. Such a hook
	// blocks nothing, so the effect does not count it. Status names it
	// apart, because "no gate hook installed" would send the reader to a
	// setup run that does not repair it. Init leaves it empty.
	Inert []string
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
	// GateEffectReportOnly means every gate hook that runs is
	// report-only: it discards its exit status, or the client runs it in
	// the background. Verdicts are reported, and nothing blocks.
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
		g.Enforce = appendOnce(g.Enforce, label)
	case hooks.ModeWarn:
		g.Warn = appendOnce(g.Warn, label)
	case GateModeReportOnly:
		g.ReportOnly = appendOnce(g.ReportOnly, label)
	}
}

// AddPossible records one definition that may run a gate hook.
func (g *GateSummary) AddPossible(label string) {
	g.Possible = appendOnce(g.Possible, label)
}

// AddUnreadable records one hook document that cannot be read.
func (g *GateSummary) AddUnreadable(label string) {
	g.Unreadable = appendOnce(g.Unreadable, label)
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
		switch {
		case !g.addHook(hook.Path, hook.Mode, hook.Uncertain):
		case hook.Filtered:
			g.FilteredPossible++
		default:
			g.UnfilteredPossible++
		}

		if hook.Mode == GateModeReportOnly && hook.Background {
			g.Background = appendOnce(g.Background, hook.Path)
		}
	}

	return g
}

// ReportOnlyCause names why the report-only hooks of the labels block
// nothing, in the words of a gate line: "discards its exit status",
// "runs in the background", or both. A label without a recorded cause
// discards its exit status, as every report-only hook did before a
// background key counted.
func (g GateSummary) ReportOnlyCause(labels []string) string {
	background := 0

	for _, label := range labels {
		if slices.Contains(g.Background, label) {
			background++
		}
	}

	switch background {
	case 0:
		return "discards its exit status"
	case len(labels):
		return "runs in the background"
	default:
		return "discards its exit status or runs in the background"
	}
}

// addHook records one hook by its mode and its certainty. An uncertain
// hook in warn or enforce mode is possible: it may block. An uncertain
// report-only hook is report-only, not possible. The shell discards its
// exit status, or the client does not wait for it, so the reader knows
// that it never blocks.
// "Unknown" would say less than is known. The result is true when the
// hook is possible.
func (g *GateSummary) addHook(label, mode string, uncertain bool) bool {
	if uncertain && mode != GateModeReportOnly {
		g.AddPossible(label)

		return true
	}

	g.Add(label, mode)

	return false
}

// SummarizeInspections groups the gate hooks of every client that has a
// command gate by its client ID. A client whose hook document cannot be
// read is unreadable, with the detail of its commands entry. A client
// with a definition that may run a gate is possible, beside the mode of
// the definitions that certainly run one. A client whose gate entry is
// inert is listed as inert.
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

		if commands.Inert {
			g.Inert = appendOnce(g.Inert, insp.ClientID)
		}

		if insp.GateMode == GateModeReportOnly && insp.GateBackground {
			g.Background = appendOnce(g.Background, insp.ClientID)
		}

		if insp.PossibleGateMode != "" {
			g.addHook(insp.ClientID, insp.PossibleGateMode, true)
		}
	}

	return g
}
