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

	reportClaudeWrappers(plan, settings, specs)

	if req.CheckHookSources {
		local := readLocalHooks(plan, root)

		if mode := hooks.EffectiveGateMode(local, gate, hooks.ClaudeMatcher); mode != "" {
			plan.GateHooks = append(plan.GateHooks, GateHook{Path: claudeLocalSettings, Mode: mode})
		}

		reportCoverage(plan, settings, local, specs)
	}

	narration.hooks, narration.hooksChanged = true, changed
	narration.specs, narration.gateMode, narration.previous = specs, gateMode, previous

	return nil
}

// reportClaudeWrappers names each command in the shared settings that
// runs a seamark hook through a wrapper, a shell condition, or a
// redirect. Setup does not own such a command: it keeps the command as
// it is, and it still installs the managed hook, because the shared file
// always gets every hook. The handler then runs twice, and the user
// decides which copy stays. A wrapped gate hook keeps its own mode, so
// the gate line of the run must know it.
func reportClaudeWrappers(plan *ClientPlan, settings map[string]any, specs []hooks.Spec) {
	for i, spec := range specs {
		for _, wrapped := range hooks.Wrapped(settings, spec) {
			effect := "the seamark hook runs twice"
			if !wrapped.Certain {
				effect = "when that command runs the seamark hook, the hook runs twice"
			}

			plan.Findings = append(plan.Findings, Finding{
				Level: FindingWarning,
				Path:  approve.ClaudeSettings,
				Reason: fmt.Sprintf("%s also has `%s`, which setup does not manage and did not change; %s",
					spec.Event, render.Sanitize(wrapped.Command), effect),
				Action: "remove one of the two hooks",
			})

			// The gate spec is the first one, by the order of ClaudeSpecs. An
			// uncertain wrapper counts too: a gate line that says "nothing
			// blocks" while a wrapped gate blocks is the worse error. A
			// wrapper that Claude Code never runs for Bash gates nothing.
			if i != 0 || wrapped.Type != "command" || !hooks.Fires(spec, wrapped.Matcher, hooks.ClaudeMatcher) {
				continue
			}

			mode := hooks.ModeWarn
			if hooks.SeamarkHookUse(wrapped.Command, []string{hooks.GateMarker(hooks.ModeEnforce)}) != hooks.HookNotRun {
				mode = hooks.ModeEnforce
			}

			plan.GateHooks = append(plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: mode})
		}
	}
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

// reportCoverage names, after the merge, each tool that two sources run
// the same handler for, and each tool that no source runs it for.
// Claude Code runs the matching hooks of every source, so a handler in
// both files delivers each lesson twice and evaluates each command
// twice; the user removes the personal copy. Setup never rewrites the
// matcher of an existing entry, because a narrow matcher can be the
// user's choice. So it cannot always complete the coverage, and then it
// must say so instead of reporting a complete install.
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
			reason := fmt.Sprintf("runs `%s`, and %s runs the same handler, so it runs twice for %s",
				render.Sanitize(running), approve.ClaudeSettings, strings.Join(twice, ", "))

			// The local copy can carry another gate mode. Both hooks run, so
			// a local --enforce still blocks under a shared warn hook. The
			// plan's GateHooks carry that to the caller's gate summary.
			if !strings.HasSuffix(running, " "+spec.Marker) {
				reason += "; the local copy runs in another gate mode, and both apply"
			}

			plan.Findings = append(plan.Findings, Finding{
				Level:  FindingWarning,
				Path:   claudeLocalSettings,
				Reason: reason,
				Action: "remove the hook from the local file; the shared file is the one setup manages",
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

	return Inspection{
		ClientID: ClaudeID,
		Capabilities: []CapabilityInspection{
			inspectClaudeRegistration(root, evidence),
			inspectGrants(approve.InspectClaude(root), evidence),
		},
		GateMode: installedClaudeGateMode(root),
	}
}

// installedClaudeGateMode reads the gate hook mode under the same path
// rules as setup: a linked or unreadable settings file reports no mode
// here, and the setup plan then reports the error itself.
func installedClaudeGateMode(root string) string {
	guard, data, err := ReadGuarded(root, approve.ClaudeSettings)
	if err != nil || !guard.Exists {
		return ""
	}

	settings, err := hooks.ParseSettings(data)
	if err != nil {
		return ""
	}

	return hooks.InstalledGateMode(settings)
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
