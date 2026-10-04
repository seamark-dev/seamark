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

// Plan composes every requested edit and writes nothing. The order of
// the reads is init's historical order, so a repository with two broken
// files reports the same one first: the settings, then the hooks merge,
// then .mcp.json, then the allow rules.
func (claudeSetup) Plan(root, binary string, req ClientSetup) (ClientPlan, error) {
	var (
		plan      ClientPlan
		settings  map[string]any
		narration = &claudeNarration{binary: binary}
	)

	if req.Hooks || req.ApproveTools {
		guard, parsed, err := readClaudeSettings(root)

		switch {
		case err != nil && guard.Exists:
			return ClientPlan{}, fmt.Errorf("%w (fix or move it, then re-run)", err)
		case err != nil:
			return ClientPlan{}, err
		}

		plan.Reads = append(plan.Reads, guard)
		settings = parsed
	}

	if req.Hooks {
		if err := planClaudeHooks(&plan, narration, root, req, settings); err != nil {
			return ClientPlan{}, err
		}
	}

	// The registration is planned before the grants, because its server
	// name spells every tool rule.
	var mcp *approve.ClaudeMCPPlan

	if req.RegisterMCP || req.ApproveTools {
		// An input, not an owned document: a run that only takes the server
		// name from it may read it through a link, as init always did. The
		// coordinator refuses the write when the registration would go
		// through one.
		guard, data, err := ReadInput(root, approve.MCPConfig)
		if err != nil {
			return ClientPlan{}, err
		}

		if mcp, err = approve.PlanClaudeMCP(data, guard.Exists); err != nil {
			return ClientPlan{}, err
		}

		plan.Reads = append(plan.Reads, guard)
	}

	if req.ApproveTools {
		if err := planClaudeGrants(narration, settings, mcp); err != nil {
			return ClientPlan{}, err
		}
	}

	if settings != nil {
		if err := finishClaudeSettings(&plan, narration, settings); err != nil {
			return ClientPlan{}, err
		}
	}

	if req.RegisterMCP {
		if err := planClaudeRegistration(&plan, mcp); err != nil {
			return ClientPlan{}, err
		}
	}

	return plan, nil
}

// finishClaudeSettings records the settings.json write, or the keep.
func finishClaudeSettings(plan *ClientPlan, narration *claudeNarration, settings map[string]any) error {
	if !narration.changed() {
		plan.Kept = append(plan.Kept, FileKeep{
			Path: approve.ClaudeSettings, Detail: "nothing to add", Narrate: narration.narrate,
		})

		return nil
	}

	var parts []string

	if narration.hooksChanged {
		parts = append(parts, "gate + lessons + context reset hooks")
	}

	if narration.grants != nil && len(narration.grants.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d allow rules", len(narration.grants.Missing)))
	}

	// The same encoding init always used, so a file that init wrote
	// before stays byte-stable when nothing in it changes.
	after, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	plan.Writes = append(plan.Writes, FileWrite{
		Path: approve.ClaudeSettings, After: append(after, '\n'),
		Detail: strings.Join(parts, "; "), Narrate: narration.narrate,
	})

	return nil
}

// planClaudeHooks merges the lifecycle hooks into the parsed settings.
// An empty gate mode keeps the installed mode, and warn on a first
// install: enforcement must never be added or removed without an
// explicit request. The narrator reports a removed --enforce flag.
// hooks.InstalledGateMode reads the installed mode for Plan, Inspect,
// and ManagedGateMode, so a re-run keeps the mode that doctor reports.
func planClaudeHooks(plan *ClientPlan, narration *claudeNarration, root string, req ClientSetup, settings map[string]any) error {
	if narration.binary == "" {
		return errors.New("setup: the seamark binary path is empty")
	}

	gateMode := req.GateMode
	previous := hooks.InstalledGateMode(settings)

	if gateMode == "" {
		gateMode = previous
	}

	if gateMode == "" {
		gateMode = hooks.ModeWarn
	}

	// Every hook goes into the shared settings, whatever another source
	// runs. The shared file is the one the team commits, so it must be
	// complete and must not depend on the personal file of the person who
	// ran setup.
	specs := hooks.ClaudeSpecs(gateMode)

	merged, err := hooks.Merge(settings, narration.binary, specs)
	if err != nil {
		return fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	// The gate hooks the client runs after this plan: the managed one,
	// when the merge left it under a matcher that fires for Bash, and one
	// in the local file when the run looked there. The local hook is
	// reported with its own mode, never changed.
	gate := specs[0]

	if hooks.ManagedRuns(settings, gate, narration.binary, hooks.ClaudeMatcher) {
		plan.GateHooks = append(plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: gateMode, Managed: true})
	}

	// After the merge the shared file holds the managed hooks. Every
	// other definition of a hook, wrapped in the shared file or anywhere
	// in the personal file, is the same evidence inspection reads: setup
	// reports it with its tools and its mode, and never edits it. The
	// local file is read only when the run looks at every hook source.
	var local map[string]any

	if req.CheckHookSources {
		local = readLocalHooks(plan, root)
	}

	for i, spec := range specs {
		sources := documentSources(&plan.Findings, settings, hooks.ClaudeMatcher, approve.ClaudeSettings, spec, true)

		if req.CheckHookSources {
			sources = append(sources, documentSources(&plan.Findings, local, hooks.ClaudeMatcher, claudeLocalSettings, spec, false)...)
		}

		// The gate hooks the client runs from definitions setup does not
		// manage, each with its own mode. An uncertain one is listed too,
		// marked as such. A gate line that says "nothing blocks" while a
		// wrapped gate blocks is the worse error. One that says "enforce"
		// for an echo is wrong too.
		if i == 0 {
			for _, source := range unmanagedSources(sources) {
				plan.GateHooks = append(plan.GateHooks, GateHook{Path: source.path, Mode: source.mode, Uncertain: !source.certain})
			}
		}

		sourceFindings(&plan.Findings, spec, sources)

		if req.CheckHookSources {
			reportMissingCoverage(plan, spec, sources)
		}
	}

	// Merge rewrites every owned gate command, also one under a matcher
	// that never fires. The installed mode counts only a command that
	// fires, so a plain re-run can remove a flag that the user wrote. The
	// narrator reports the removal that Merge made.
	narration.hooks, narration.hooksChanged = true, merged.Changed
	narration.specs, narration.removedEnforce = specs, merged.RemovesEnforce(hooks.GateMarker(hooks.ModeEnforce))

	return nil
}

// readLocalHooks reads the user's local settings file, the second hook
// source Claude Code runs. Setup never edits it and never lets it change
// what the shared file gets; it only reports a handler that both files
// run. The file is guarded, and guarded when absent too: a hook added to
// it after the plan would make the report wrong. A local file that
// cannot be read is reported and does not stop setup, because setup does
// not own that file.
func readLocalHooks(plan *ClientPlan, root string) map[string]any {
	skipped := func(err error) map[string]any {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   claudeLocalSettings,
			Reason: "not checked for duplicate seamark hooks: " + render.Sanitize(readReason(claudeLocalSettings, err)),
			Action: "fix the file, then run the command again",
		})

		return nil
	}

	// An input: the read may follow a link, for example into a dotfiles
	// directory.
	guard, data, err := ReadInput(root, claudeLocalSettings)
	if err != nil {
		return skipped(err)
	}

	plan.Reads = append(plan.Reads, guard)

	if !guard.Exists {
		return nil
	}

	local, err := hooks.ParseDocument(data)
	if err != nil {
		return skipped(err)
	}

	return local
}

// reportMissingCoverage names, after the merge, each tool that no
// source certainly runs the handler for, from the same evidence
// inspection reads: a wrapper or a personal definition that covers a
// tool covers it. Setup never rewrites the matcher of an existing
// entry, because a narrow matcher can be the user's choice. So it
// cannot always complete the coverage, and then it must say so instead
// of reporting a complete install. A tool that only an uncertain
// definition covers is reported by sourceFindings, not here.
func reportMissingCoverage(plan *ClientPlan, spec hooks.Spec, sources []hookSource) {
	covered := coveredTools(spec, sources, func(hookSource) bool { return true })

	var missing []string

	for _, tool := range spec.Tools() {
		if !slices.Contains(covered, tool) {
			missing = append(missing, toolName(tool))
		}
	}

	if len(missing) == 0 {
		return
	}

	plan.Findings = append(plan.Findings, Finding{
		Level: FindingWarning,
		Path:  approve.ClaudeSettings,
		Reason: fmt.Sprintf("no hook runs `seamark %s` for %s; setup does not change the matcher of an existing hook",
			spec.Marker, strings.Join(missing, ", ")),
		Action: fmt.Sprintf("set the hook's matcher to %q", spec.Matcher),
	})
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
// settings. An explicit deny or ask entry is not an error: the rest of
// the setup is still safe, and the narrator names the kept entries.
func planClaudeGrants(narration *claudeNarration, settings map[string]any, mcp *approve.ClaudeMCPPlan) error {
	// A conflicting .mcp.json registers nothing, so the rules fall back to
	// the conventional name, the same as approve.Registration.ServerName.
	server := mcp.Server
	if server == "" {
		server = approve.ClaudeServer
	}

	grants, err := approve.PlanClaude(settings, server)
	if err != nil {
		return err
	}

	if err := approve.MergeAllow(settings, grants); err != nil {
		return fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	narration.grants = grants

	return nil
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

	hookEntries, gate, findings := inspectClaudeHooks(root, evidence)

	// The skills entry carries evidence only; the registry fills the
	// directory state.
	skillsEvidence := CapabilityInspection{Capability: CapabilitySkills, Supported: true, Verification: evidence}

	registration, stops := inspectClaudeRegistration(root, evidence)
	capabilities := append([]CapabilityInspection{
		skillsEvidence,
		registration,
		inspectGrants(approve.InspectClaude(root), evidence),
	}, hookEntries...)

	// The hooks and the grants share .claude/settings.json, a document
	// that setup owns.
	linkActions(root, capabilities, map[Capability]string{
		CapabilityToolGrants: approve.ClaudeSettings, CapabilityEdits: approve.ClaudeSettings,
		CapabilityCommands: approve.ClaudeSettings, CapabilityResets: approve.ClaudeSettings,
	})

	// The explicit setup also plans .mcp.json, so it can stop there. The
	// plain form of init never reads .mcp.json, so it installs a missing
	// hook in that state.
	restore := "run `seamark init --client claude` to install the missing hook"
	if stops {
		restore = "run `seamark init` to install the missing hook: the plain form never reads .mcp.json, " +
			"where `seamark init --client claude` stops"
	}

	restoreHooks(capabilities, restore, CapabilityEdits, CapabilityCommands, CapabilityResets)

	return Inspection{
		ClientID:          ClaudeID,
		Capabilities:      capabilities,
		GateMode:          gate.mode,
		PossibleGateMode:  gate.possible,
		ManagedGateMode:   gate.managed,
		HookDocumentError: gate.err,
		Findings:          findings,
	}
}

// gateInspection is what an inspection learns about the gate hooks of
// one client. mode is the mode of the definitions that certainly run
// the gate, and possible the mode of the ones that may run it. managed
// is the mode of the managed hook. err is the reason when the managed
// document cannot be read, sanitized: it reaches terminals and MCP
// clients as it is.
type gateInspection struct {
	mode, possible, managed, err string
}

// inspectClaudeHooks classifies the lifecycle hooks from the shared
// settings and the personal local file, from one kind of evidence for
// both. The evidence is every definition of each hook, owned or
// wrapped, with the tools the client runs it for. Setup manages the
// shared file only. A definition in the local file is a running hook
// that setup does not manage, and a handler in both files is reported
// as running twice. Claude Code records no per-hook trust that seamark
// could read, so trust stays unknown and no finding names it.
func inspectClaudeHooks(root string, evidence VerificationEvidence) (entries []CapabilityInspection, gate gateInspection, findings []Finding) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: evidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: evidence}
	resets := CapabilityInspection{Capability: CapabilityResets, Supported: true, Verification: evidence}

	// The order of ClaudeSpecs: the gate hook first, then the lessons
	// hook, then the context reset.
	all := []*CapabilityInspection{&commands, &edits, &resets}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	_, settings, err := readClaudeSettings(root)
	if err != nil {
		unreadableHooks(all, approve.ClaudeSettings, err)

		return collect(), gateInspection{err: render.Sanitize(err.Error())}, nil
	}

	// The local file is an input: a file that cannot be read is
	// reported and does not make the shared file's hooks unknown.
	var scratch ClientPlan

	local := readLocalHooks(&scratch, root)
	findings = scratch.Findings

	for i, spec := range hooks.ClaudeSpecs(hooks.ModeWarn) {
		sources := documentSources(&findings, settings, hooks.ClaudeMatcher, approve.ClaudeSettings, spec, true)
		sources = append(sources, documentSources(&findings, local, hooks.ClaudeMatcher, claudeLocalSettings, spec, false)...)

		classifyHook(all[i], spec, sources)
		sourceFindings(&findings, spec, sources)

		if i == 0 {
			gate.mode, gate.possible = hookMode(sources, certainSource), hookMode(sources, uncertainSource)
		}
	}

	gate.managed = hooks.InstalledGateMode(settings)

	return collect(), gate, findings
}

// ManagedGateMode reads the mode of the managed gate hook from
// .claude/settings.json alone, by the rule that Plan and Inspect use. A
// file that cannot be read gives "": the plan then reports the error.
func (claudeSetup) ManagedGateMode(root string) string {
	_, settings, err := readClaudeSettings(root)
	if err != nil {
		return ""
	}

	return hooks.InstalledGateMode(settings)
}

// readClaudeSettings reads and parses .claude/settings.json, by the
// rule of readOwnedDocument.
func readClaudeSettings(root string) (FileGuard, map[string]any, error) {
	return readOwnedDocument(root, approve.ClaudeSettings, hooks.ParseSettings)
}

// inspectClaudeRegistration classifies .mcp.json with the planner's own
// rules and reader, so inspection and setup cannot disagree. The file is
// an input: the read follows a link, as the plan's read does. stops is
// true when the explicit setup of Claude Code stops at the file. The
// plan then cannot read the file, or it must write through a link.
func inspectClaudeRegistration(root string, evidence VerificationEvidence) (entry CapabilityInspection, stops bool) {
	entry = CapabilityInspection{Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence}

	guard, data, err := ReadInput(root, approve.MCPConfig)
	if err != nil {
		entry.State, entry.Detail = StateUnreadable, unreadableDetail(err)
		entry.Action = registrationAction(ClaudeID, StateUnreadable)

		return entry, true
	}

	mcp, err := approve.PlanClaudeMCP(data, guard.Exists)

	switch {
	case err != nil:
		entry.State, entry.Detail = StateUnreadable, unreadableDetail(err)
	case mcp.Registered:
		entry.State = StateCurrent
		entry.Detail = fmt.Sprintf("registered in %s as %q", approve.MCPConfig, render.Sanitize(mcp.Server))
	case len(mcp.Conflicts) > 0:
		entry.State, entry.Detail = StateConflict, render.Sanitize(strings.Join(mcp.Conflicts, "; "))
	default:
		entry.State = StateAbsent
	}

	entry.Action = registrationAction(ClaudeID, entry.State)
	linked := guard.Linked != "" && (entry.State == StateAbsent || entry.State == StateUnreadable)

	if linked {
		entry.Action = linkedRegistrationAction(guard.Linked, entry.State)
	}

	return entry, linked || entry.State == StateUnreadable
}

// linkedRegistrationAction is the corrective action for a linked
// .mcp.json that does not register seamark. A re-run of init cannot
// clear the state, because setup never writes through a link. A file
// that the plan cannot parse needs a fix before the registration.
func linkedRegistrationAction(link string, state CapabilityState) string {
	change := "register seamark in the file it points to"
	if state == StateUnreadable {
		change = "fix the file it points to and register seamark there"
	}

	return "setup never writes through the symlink at " + link + ": " + change +
		", or replace the link with the real file and run `seamark init --client claude`"
}
