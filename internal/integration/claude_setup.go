package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		guard, data, err := ReadGuarded(root, approve.ClaudeSettings)
		if err != nil {
			return ClientPlan{}, err
		}

		plan.Reads = append(plan.Reads, guard)
		settings = map[string]any{}

		if guard.Exists {
			if settings, err = hooks.ParseSettings(data); err != nil {
				return ClientPlan{}, fmt.Errorf("%w (fix or move it, then re-run)", err)
			}
		}
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

// claudeNarration holds what the settings.json lines report. The
// adapter fills it while it plans, and the narrator reads it after the
// write, so the lines describe exactly what was composed.
type claudeNarration struct {
	binary string
	// hooks is true when the hooks were requested. specs lists the hooks
	// setup manages in this run, gateMode the mode they run in, and
	// previous the gate mode that was installed before.
	hooks        bool
	hooksChanged bool
	specs        []hooks.Spec
	gateMode     string
	previous     string
	// grants is the allow-rule plan; nil when none was requested.
	grants *approve.ClaudePlan
}

// changed reports whether the document is written.
func (n *claudeNarration) changed() bool {
	return n.hooksChanged || (n.grants != nil && len(n.grants.Missing) > 0)
}

// narrate prints the settings.json lines in the words init has always
// used: the hooks line with the exact hook commands, the note when
// enforcement leaves the hook, and the allow-rule lines.
func (n *claudeNarration) narrate(w io.Writer, status OpStatus) {
	preview := status == OpPlanned

	if n.hooks {
		switch {
		case !n.changed():
			fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", approve.ClaudeSettings)
		case n.hooksChanged:
			fmt.Fprintf(w, "  %s %s (gate + lessons + context reset hooks)\n", verb("updated", "would update", preview), approve.ClaudeSettings)
		default:
			fmt.Fprintf(w, "  %s %s (permissions; seamark hooks already wired)\n", verb("updated", "would update", preview), approve.ClaudeSettings)
		}

		// The exact hook commands: what runs on which tool must never
		// require opening settings.json to find out.
		for _, spec := range n.specs {
			where := spec.Event
			if spec.Matcher != "" {
				where += " " + spec.Matcher
			}

			fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(n.binary))
		}

		// The note states only what changed, the hook flag. Whether
		// anything still blocks is the effective-mode line's job: a kept
		// enforce policy blocks whatever the flag says.
		if n.previous == hooks.ModeEnforce && n.gateMode == hooks.ModeWarn {
			fmt.Fprintf(w, "  note    %s --enforce from the gate hook: the hook follows .seamark/policy.yaml\n"+
				"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n",
				verb("removed", "would remove", preview))
		}
	}

	if n.grants != nil {
		n.narrateGrants(w, preview)
	}
}

// narrateGrants lists every allow rule the run added: what a repository
// pre-approves must never require opening settings.json to find out.
// Explicit deny or ask entries are named as kept, like the Codex line
// does, so the user learns why a tool still prompts.
func (n *claudeNarration) narrateGrants(w io.Writer, preview bool) {
	kept := approve.KeptSuffix(n.grants.Conflicts)

	switch {
	case len(n.grants.Missing) == 0 && kept != "":
		// Nothing to add is not everything approved: the kept entries are
		// exactly the rules that still prompt.
		fmt.Fprintf(w, "  kept    %s permissions (nothing to add%s)\n", approve.ClaudeSettings, kept)
	case len(n.grants.Missing) == 0:
		fmt.Fprintf(w, "  kept    %s permissions (seamark tools and skills already approved)\n", approve.ClaudeSettings)
	default:
		fmt.Fprintf(w, "  %s %d Claude Code allow rules in %s (seamark MCP tools + skills%s)\n",
			verb("approved", "would approve", preview), len(n.grants.Missing), approve.ClaudeSettings, kept)

		for _, rule := range n.grants.Missing {
			fmt.Fprintf(w, "          %s\n", rule)
		}
	}
}

// verb picks the preview form of a narration verb.
func verb(applied, preview string, isPreview bool) string {
	if isPreview {
		return preview
	}

	return applied
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

	changed, err := hooks.Merge(settings, narration.binary, specs)
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
		// manage, each with its own mode. An uncertain one counts too: a
		// gate line that says "nothing blocks" while a wrapped gate blocks
		// is the worse error.
		if i == 0 {
			for _, source := range unmanagedSources(sources) {
				plan.GateHooks = append(plan.GateHooks, GateHook{Path: source.path, Mode: source.mode})
			}
		}

		sourceFindings(&plan.Findings, spec, sources)

		if req.CheckHookSources {
			reportMissingCoverage(plan, spec, sources)
		}
	}

	narration.hooks, narration.hooksChanged = true, changed
	narration.specs, narration.gateMode, narration.previous = specs, gateMode, previous

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
	skipped := func(reason string) map[string]any {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   claudeLocalSettings,
			Reason: "not checked for duplicate seamark hooks: " + render.Sanitize(reason),
			Action: "fix the file, then run the command again",
		})

		return nil
	}

	// An input: the read may follow a link, for example into a dotfiles
	// directory.
	guard, data, err := ReadInput(root, claudeLocalSettings)
	if err != nil {
		return skipped(err.Error())
	}

	plan.Reads = append(plan.Reads, guard)

	if !guard.Exists {
		return nil
	}

	local, err := hooks.ParseDocument(data)
	if err != nil {
		return skipped(err.Error())
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

	hookEntries, gateMode, managedGateMode, findings := inspectClaudeHooks(root, evidence)

	// The skills entry carries evidence only; the registry fills the
	// directory state.
	skillsEvidence := CapabilityInspection{Capability: CapabilitySkills, Supported: true, Verification: evidence}

	return Inspection{
		ClientID: ClaudeID,
		Capabilities: append([]CapabilityInspection{
			skillsEvidence,
			inspectClaudeRegistration(root, evidence),
			inspectGrants(approve.InspectClaude(root), evidence),
		}, hookEntries...),
		GateMode:        gateMode,
		ManagedGateMode: managedGateMode,
		Findings:        findings,
	}
}

// inspectClaudeHooks classifies the lifecycle hooks from the shared
// settings and the personal local file, from one kind of evidence for
// both: every definition of each hook, owned or wrapped, with the tools
// the client runs it for. Setup manages the shared file only; a
// definition in the local file is a running hook that setup does not
// manage, and a handler in both files is reported as running twice.
// Claude Code records no per-hook trust that seamark could read, so
// trust stays unknown and no finding names it.
func inspectClaudeHooks(root string, evidence VerificationEvidence) (entries []CapabilityInspection, gateMode, managedGateMode string, findings []Finding) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: evidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: evidence}
	resets := CapabilityInspection{Capability: CapabilityResets, Supported: true, Verification: evidence}

	// The order of ClaudeSpecs: the gate hook first, then the lessons
	// hook, then the context reset.
	all := []*CapabilityInspection{&commands, &edits, &resets}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	guard, data, err := ReadGuarded(root, approve.ClaudeSettings)
	if err != nil {
		unreadableHooks(all, approve.ClaudeSettings, err)

		return collect(), "", "", nil
	}

	settings := map[string]any{}

	if guard.Exists {
		if settings, err = hooks.ParseSettings(data); err != nil {
			unreadableHooks(all, approve.ClaudeSettings, err)

			return collect(), "", "", nil
		}
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
			gateMode = hookMode(sources)
			managedGateMode = hookMode(managedSources(sources))
		}
	}

	return collect(), gateMode, managedGateMode, findings
}

// inspectClaudeRegistration classifies .mcp.json with the planner's own
// rules, so inspection and setup cannot disagree about a conflict.
func inspectClaudeRegistration(root string, evidence VerificationEvidence) CapabilityInspection {
	entry := CapabilityInspection{Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence}

	guard, data, err := ReadGuarded(root, approve.MCPConfig)
	if err != nil {
		entry.State, entry.Detail = StateUnreadable, unreadableDetail(err)
		entry.Action = registrationAction(ClaudeID, StateUnreadable)

		return entry
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

	return entry
}
