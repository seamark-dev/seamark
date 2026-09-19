package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
)

// claudeLocalSettings is the second project hook source Claude Code
// reads. It is the user's personal file, so setup only inspects it for
// a seamark handler that would run twice; setup never writes it.
const claudeLocalSettings = ".claude/settings.local.json"

// claudeSetup plans Claude Code's native documents. The lifecycle hooks
// and the allow rules share .claude/settings.json, so one plan composes
// both into one write. The MCP registration lives in .mcp.json.
type claudeSetup struct{}

// Plan composes every requested edit and writes nothing.
func (claudeSetup) Plan(root, binary string, req ClientSetup) (ClientPlan, error) {
	var plan ClientPlan

	// The registration is planned first, because its server name spells
	// every tool rule. Its guard is appended after the settings guard, so
	// the documents narrate in the order a user expects: hooks first.
	var (
		mcpGuard FileGuard
		mcp      *approve.ClaudeMCPPlan
	)

	if req.RegisterMCP || req.ApproveTools {
		var (
			data []byte
			err  error
		)

		if mcpGuard, data, err = ReadGuarded(root, approve.MCPConfig); err != nil {
			return ClientPlan{}, err
		}

		if mcp, err = approve.PlanClaudeMCP(data, mcpGuard.Exists); err != nil {
			return ClientPlan{}, err
		}
	}

	if req.Hooks || req.ApproveTools {
		if err := planClaudeSettings(&plan, root, binary, req, mcp); err != nil {
			return ClientPlan{}, err
		}
	}

	if mcp != nil {
		plan.Reads = append(plan.Reads, mcpGuard)

		if req.RegisterMCP {
			if err := planClaudeRegistration(&plan, mcp); err != nil {
				return ClientPlan{}, err
			}
		}
	}

	return plan, nil
}

// planClaudeSettings merges the hooks and the allow rules into one
// snapshot of .claude/settings.json.
func planClaudeSettings(plan *ClientPlan, root, binary string, req ClientSetup, mcp *approve.ClaudeMCPPlan) error {
	guard, data, err := ReadGuarded(root, approve.ClaudeSettings)
	if err != nil {
		return err
	}

	plan.Reads = append(plan.Reads, guard)

	settings := map[string]any{}

	if guard.Exists {
		if settings, err = hooks.ParseSettings(data); err != nil {
			return fmt.Errorf("%w (fix or move it, then re-run)", err)
		}
	}

	var parts []string

	if req.Hooks {
		changed, err := planClaudeHooks(plan, root, binary, req.GateMode, settings)
		if err != nil {
			return err
		}

		if changed {
			parts = append(parts, "gate + lessons + context reset hooks")
		}
	}

	if req.ApproveTools {
		added, err := planClaudeGrants(plan, settings, mcp)
		if err != nil {
			return err
		}

		if added > 0 {
			parts = append(parts, fmt.Sprintf("%d allow rules", added))
		}
	}

	if len(parts) == 0 {
		plan.Kept = append(plan.Kept, FileKeep{Path: approve.ClaudeSettings, Detail: "nothing to add"})

		return nil
	}

	// The same encoding init always used, so a file that init wrote
	// before stays byte-stable when nothing in it changes.
	after, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	plan.Writes = append(plan.Writes, FileWrite{
		Path: approve.ClaudeSettings, After: append(after, '\n'), Detail: strings.Join(parts, "; "),
	})

	return nil
}

// planClaudeHooks merges the lifecycle hooks into the parsed settings
// and reports whether they changed. An empty gate mode keeps the
// installed mode, and warn on a first install: enforcement must never
// be added or removed without an explicit request.
func planClaudeHooks(plan *ClientPlan, root, binary, gateMode string, settings map[string]any) (bool, error) {
	if binary == "" {
		return false, errors.New("setup: the seamark binary path is empty")
	}

	previous := hooks.InstalledGateMode(settings)

	if gateMode == "" {
		gateMode = previous
	}

	if gateMode == "" {
		gateMode = hooks.ModeWarn
	}

	if previous == hooks.ModeEnforce && gateMode == hooks.ModeWarn {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.ClaudeSettings,
			Reason: "removes --enforce from the gate hook; the hook then follows .seamark/policy.yaml",
			Action: "run again with --gate-mode enforce to keep the flag",
		})
	}

	wanted := hooks.ClaudeSpecs(gateMode)
	specs, local := withoutDuplicateSource(plan, root, settings, wanted)

	changed, err := hooks.Merge(settings, binary, specs)
	if err != nil {
		return false, fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	reportCoverage(plan, settings, local, wanted)

	return changed, nil
}

// withoutDuplicateSource adjusts the hooks for the handlers that the
// user's local settings file already runs. Claude Code runs the
// matching hooks of every source, so a second copy would deliver each
// lesson twice and evaluate each command twice.
//
// Coverage is per tool. A hook that the shared file already holds stays
// in the list, so Merge still updates its binary path and its gate
// mode. Otherwise setup adds the hook only for the tools the local file
// does not cover, and it adds nothing when the local file covers all of
// them. The local file is never edited. A local file that cannot be
// read is reported and does not stop setup, because setup does not own
// that file.
func withoutDuplicateSource(plan *ClientPlan, root string, settings map[string]any, specs []hooks.Spec) (kept []hooks.Spec, local map[string]any) {
	skipped := func(reason string) ([]hooks.Spec, map[string]any) {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   claudeLocalSettings,
			Reason: "not checked for duplicate seamark hooks: " + render.Sanitize(reason),
			Action: "fix the file, then run the command again",
		})

		return specs, nil
	}

	guard, data, err := ReadGuarded(root, claudeLocalSettings)
	if err != nil {
		return skipped(err.Error())
	}

	// Guarded although never written, and guarded when absent too. A
	// local file that appears after the plan, or gains a hook, would make
	// the planned hooks a duplicate.
	plan.Reads = append(plan.Reads, guard)

	if !guard.Exists {
		return specs, nil
	}

	if local, err = hooks.ParseDocument(data); err != nil {
		return skipped(err.Error())
	}

	for _, spec := range specs {
		covered, running := hooks.Covered(local, spec, hooks.ClaudeMatcher)

		if len(covered) == 0 || hooks.Installed(settings, spec) {
			kept = append(kept, spec)

			continue
		}

		var uncovered []string

		for _, tool := range spec.Tools() {
			if !slices.Contains(covered, tool) {
				uncovered = append(uncovered, tool)
			}
		}

		finding := Finding{
			Level:  FindingWarning,
			Path:   claudeLocalSettings,
			Reason: fmt.Sprintf("already runs `%s`; setup does not add a second copy to %s", render.Sanitize(running), approve.ClaudeSettings),
			Action: "remove the hook from the local file to let setup manage it",
		}

		if len(uncovered) > 0 {
			spec.Matcher = strings.Join(uncovered, "|")
			kept = append(kept, spec)

			finding.Reason = fmt.Sprintf("already runs `%s` for %s; setup adds the hook to %s for %s only",
				render.Sanitize(running), strings.Join(covered, ", "), approve.ClaudeSettings, strings.Join(uncovered, ", "))
		}

		plan.Findings = append(plan.Findings, finding)
	}

	return kept, local
}

// reportCoverage names, after the merge, each tool that two sources run
// the same handler for, and each tool that no source runs it for. Setup
// never rewrites the matcher of an existing entry: a narrow matcher can
// be the user's choice. So it cannot always complete the coverage, and
// then it must say so instead of reporting a complete install.
func reportCoverage(plan *ClientPlan, settings, local map[string]any, specs []hooks.Spec) {
	for _, spec := range specs {
		shared, _ := hooks.Covered(settings, spec, hooks.ClaudeMatcher)
		personal, running := hooks.Covered(local, spec, hooks.ClaudeMatcher)

		var twice, missing []string

		for _, tool := range spec.Tools() {
			switch inShared, inLocal := slices.Contains(shared, tool), slices.Contains(personal, tool); {
			case inShared && inLocal:
				twice = append(twice, toolName(tool))
			case !inShared && !inLocal:
				missing = append(missing, toolName(tool))
			}
		}

		if len(twice) > 0 {
			plan.Findings = append(plan.Findings, Finding{
				Level: FindingWarning,
				Path:  claudeLocalSettings,
				Reason: fmt.Sprintf("runs `%s`, and %s runs the same handler, so it runs twice for %s",
					render.Sanitize(running), approve.ClaudeSettings, strings.Join(twice, ", ")),
				Action: "remove the hook from one of the two files",
			})
		}

		if len(missing) > 0 {
			plan.Findings = append(plan.Findings, Finding{
				Level: FindingWarning,
				Path:  approve.ClaudeSettings,
				Reason: fmt.Sprintf("no hook runs `seamark %s` for %s; setup does not change the matcher of an existing hook",
					spec.Marker, strings.Join(missing, ", ")),
				Action: fmt.Sprintf("set the hook's matcher to %q", spec.Matcher),
			})
		}
	}
}

// toolName names a tool for a finding. The empty tool stands for an
// event without a matcher.
func toolName(tool string) string {
	if tool == "" {
		return "the event"
	}

	return tool
}

// planClaudeGrants appends the missing allow rules to the parsed
// settings and returns how many it added. An explicit deny or ask entry
// is a finding, not an error: the rest of the setup is still safe.
func planClaudeGrants(plan *ClientPlan, settings map[string]any, mcp *approve.ClaudeMCPPlan) (int, error) {
	// A conflicting .mcp.json registers nothing, so the rules fall back to
	// the conventional name, the same as approve.Registration.ServerName.
	server := mcp.Server
	if server == "" {
		server = approve.ClaudeServer
	}

	grants, err := approve.PlanClaude(settings, server)
	if err != nil {
		return 0, err
	}

	if err := approve.MergeAllow(settings, grants); err != nil {
		return 0, fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	if len(grants.Conflicts) > 0 {
		plan.Findings = append(plan.Findings, Finding{
			Level: FindingWarning,
			Path:  approve.ClaudeSettings,
			Reason: fmt.Sprintf("explicit settings keep %d seamark rule(s) from being approved: %s",
				grants.Conflicting(), render.Sanitize(strings.Join(grants.Conflicts, "; "))),
			Action: "edit the file by hand if those tools must run without prompts",
		})
	}

	return len(grants.Missing), nil
}

// planClaudeRegistration adds the .mcp.json write, or records why the
// file stays as it is.
func planClaudeRegistration(plan *ClientPlan, mcp *approve.ClaudeMCPPlan) error {
	switch {
	case mcp.Register:
		after, err := mcp.Document()
		if err != nil {
			return err
		}

		plan.Writes = append(plan.Writes, FileWrite{
			Path: approve.MCPConfig, After: after,
			Detail: fmt.Sprintf("registered seamark mcp as %q", mcp.Server),
		})
	case mcp.Registered:
		plan.Kept = append(plan.Kept, FileKeep{
			Path: approve.MCPConfig, Detail: fmt.Sprintf("seamark registered as %q", render.Sanitize(mcp.Server)),
		})
	default:
		plan.Kept = append(plan.Kept, FileKeep{Path: approve.MCPConfig, Detail: "seamark not registered"})
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.MCPConfig,
			Reason: render.Sanitize(strings.Join(mcp.Conflicts, "; ")),
			Action: "rename or remove that entry, or register seamark under another name",
		})
	}

	return nil
}

// Inspect reports the registration and the grants, offline. The
// lifecycle capabilities join when their codecs are declared.
func (claudeSetup) Inspect(root string) Inspection {
	evidence := VerificationEvidence{Level: VerificationUnverified}

	return Inspection{
		ClientID: ClaudeID,
		Capabilities: []CapabilityInspection{
			inspectClaudeRegistration(root, evidence),
			inspectGrants(approve.InspectClaude(root), evidence),
		},
	}
}

// inspectClaudeRegistration classifies .mcp.json with the planner's own
// rules, so inspection and setup cannot disagree about a conflict.
func inspectClaudeRegistration(root string, evidence VerificationEvidence) CapabilityInspection {
	entry := CapabilityInspection{Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence}

	guard, data, err := ReadGuarded(root, approve.MCPConfig)
	if err != nil {
		entry.State, entry.Detail = StateUnreadable, render.Sanitize(err.Error())

		return entry
	}

	mcp, err := approve.PlanClaudeMCP(data, guard.Exists)

	switch {
	case err != nil:
		entry.State, entry.Detail = StateUnreadable, render.Sanitize(err.Error())
	case mcp.Registered:
		entry.State, entry.Detail = StateCurrent, fmt.Sprintf("registered as %q", render.Sanitize(mcp.Server))
	case len(mcp.Conflicts) > 0:
		entry.State, entry.Detail = StateConflict, render.Sanitize(strings.Join(mcp.Conflicts, "; "))
	default:
		entry.State = StateAbsent
	}

	return entry
}
