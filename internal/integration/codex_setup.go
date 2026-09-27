package integration

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
)

// codexHooksFile is the one hook representation that setup manages for
// Codex. Codex also reads inline [hooks] in .codex/config.toml; setup
// only inspects that source for a seamark hook that would run twice.
const codexHooksFile = ".codex/hooks.json"

// codexSetup plans Codex's native configuration. The MCP registration
// and the per-tool approvals share .codex/config.toml, so one plan
// composes both into one write. The TOML algorithm stays in the approve
// package: it appends and inserts, and it never rewrites a user's line.
// The gate and lesson hooks live in .codex/hooks.json, a document of
// their own.
type codexSetup struct{}

// Plan composes every requested edit and writes nothing. The
// registration names the bare command, because config.toml is committed
// and shared between machines. The hooks name the binary path, as the
// Claude Code hooks do: a hook runs outside a login shell.
func (codexSetup) Plan(root, binary string, req ClientSetup) (ClientPlan, error) {
	var (
		plan   ClientPlan
		config *codexConfigRead
	)

	if req.RegisterMCP || req.ApproveTools {
		read, err := planCodexConfig(&plan, root, req)
		if err != nil {
			return ClientPlan{}, err
		}

		config = &read
	}

	if req.Hooks {
		if err := planCodexHooks(&plan, root, binary, req, config); err != nil {
			return ClientPlan{}, err
		}
	}

	return plan, nil
}

// codexConfigRead is config.toml as one plan read it. The hook planner
// reuses it: a second read of one path gives a second guard, and the
// coordinator takes two different guards for a file that changed.
type codexConfigRead struct {
	data   []byte
	exists bool
}

// planCodexConfig plans the registration and the approvals.
func planCodexConfig(plan *ClientPlan, root string, req ClientSetup) (codexConfigRead, error) {
	guard, data, err := ReadGuarded(root, approve.CodexConfig)
	if err != nil {
		return codexConfigRead{}, err
	}

	read := codexConfigRead{data: data, exists: guard.Exists}

	config, err := approve.PlanCodexData(data, guard.Exists,
		approve.CodexOptions{Register: req.RegisterMCP, Approve: req.ApproveTools})
	if err != nil {
		return codexConfigRead{}, err
	}

	plan.Reads = append(plan.Reads, guard)

	// The line init has always printed for this file. It names the
	// explicit settings that were kept, so they need no finding.
	narrate := func(w io.Writer, status OpStatus) { approve.NarrateCodex(w, config, status == OpPlanned) }

	if config.Changed() {
		plan.Writes = append(plan.Writes, FileWrite{
			Path: approve.CodexConfig, After: config.Document(data), Detail: codexWriteDetail(config), Narrate: narrate,
		})

		// Codex reads the project layer only after the user trusts the
		// project. Setup owns the file, never the trust decision.
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingInfo,
			Path:   approve.CodexConfig,
			Reason: "Codex reads this file only in a project the user trusts; setup never grants trust",
			Action: "open the project in Codex and accept its trust prompt",
		})
	} else {
		plan.Kept = append(plan.Kept, FileKeep{Path: approve.CodexConfig, Detail: codexKeptDetail(config), Narrate: narrate})
	}

	// A tool table needs the server table it belongs to. Without the
	// registration intent the approvals have nowhere to attach.
	if req.ApproveTools && !req.RegisterMCP && !config.Registered {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.CodexConfig,
			Reason: "seamark mcp is not registered, so no tool approval was added",
			Action: "register the server, then approve the tools",
		})
	}

	return read, nil
}

// planCodexHooks merges the gate hook and the lesson hook into
// .codex/hooks.json. It keeps every other hook in the file. It never
// grants trust: Codex runs a project hook only after the user reviews
// that hook in Codex.
//
// An empty gate mode keeps the installed mode, and warn on a first
// install: enforcement must never be added or removed without an
// explicit request. The narrator reports a removed --enforce flag.
//
// One handler per hook is the rule. Codex runs every matching handler
// of every source, and Codex delivery never suppresses a repeat. A
// second handler therefore doubles the advice, the log records, and the
// budget, and it evaluates each command twice. When a command that
// setup does not own already runs the hook, setup installs no handler
// beside it and says where the hook runs.
func planCodexHooks(plan *ClientPlan, root, binary string, req ClientSetup, config *codexConfigRead) error {
	if binary == "" {
		return errors.New("setup: the seamark binary path is empty")
	}

	guard, data, err := ReadGuarded(root, codexHooksFile)
	if err != nil {
		return err
	}

	plan.Reads = append(plan.Reads, guard)

	document := map[string]any{}

	if guard.Exists {
		if document, err = hooks.ParseDocumentExact(data); err != nil {
			return fmt.Errorf("%s: %w (fix or move it, then re-run)", codexHooksFile, err)
		}
	}

	gateMode := req.GateMode
	previous := codexInstalledGateMode(document)

	if gateMode == "" {
		gateMode = previous
	}

	if gateMode == "" {
		gateMode = hooks.ModeWarn
	}

	specs := hooks.CodexSpecs(gateMode)
	gate := specs[0]

	// One read of config.toml serves the inline hooks and the feature
	// flags; the registration planner's read is reused when it ran.
	if config == nil {
		config = readCodexConfig(plan, root)
	}

	inline := codexInlineCommands(plan, root, config)

	var managed []hooks.Spec

	for _, spec := range specs {
		elsewhere := unmanagedSources(codexHookSources(&plan.Findings, document, inline, spec))

		// The gate hooks Codex runs from a definition setup does not manage,
		// each with its own mode. An uncertain source counts too: a gate
		// line that says "nothing blocks" while a wrapped gate blocks is the
		// worse error.
		if spec.Name == gate.Name {
			for _, source := range elsewhere {
				plan.GateHooks = append(plan.GateHooks, GateHook{Path: source.path, Mode: source.mode})
			}
		}

		if reportCodexHookSources(plan, elsewhere, hooks.Owned(document, spec)) {
			continue
		}

		managed = append(managed, spec)
	}

	changed, err := hooks.Merge(document, binary, managed)
	if err != nil {
		return fmt.Errorf("%s: %w", codexHooksFile, err)
	}

	// The managed gate hook, when the merge left it where Codex runs it
	// for the shell tool: an existing entry keeps its matcher, and one
	// that never fires for Bash gates nothing.
	if hooks.ManagedRuns(document, gate, binary, hooks.CodexMatcher) {
		plan.GateHooks = append(plan.GateHooks, GateHook{Path: codexHooksFile, Mode: gateMode, Managed: true})
	}

	change := codexHooksChange{changed: changed, created: !guard.Exists, gateMode: gateMode, previous: previous}
	narrate := func(w io.Writer, status OpStatus) {
		narrateCodexHooks(w, binary, managed, change, status == OpPlanned)
	}

	// The adapter reads no receiver from a Codex event yet. The event
	// names one, but the reset of a subagent is unverified. The
	// once-per-context mode therefore has no effect there. Say so once,
	// at setup, in the words inspection repeats.
	plan.Findings = append(plan.Findings, codexContextFinding)

	// Hooks turned off in the project configuration make every hook of
	// this run inert; the reader must learn that here, not from doctor.
	if config != nil && config.exists {
		plan.Findings = append(plan.Findings, codexHooksDisabledFinding(config.data)...)
	}

	// Codex records trust against the hash of a hook, so a new or changed
	// hook is skipped until the user reviews it. The fact holds for a
	// kept hook too; inspection repeats it in the same words.
	if len(managed) > 0 {
		plan.Findings = append(plan.Findings, codexTrustFinding)
	}

	if !changed {
		detail := "seamark hooks already wired"
		if len(managed) == 0 {
			detail = "no hook installed: the seamark hooks run from definitions that setup does not manage"
		}

		plan.Kept = append(plan.Kept, FileKeep{Path: codexHooksFile, Detail: detail, Narrate: narrate})

		return nil
	}

	// The exact encoder keeps the user's numbers and command text as
	// written. Codex records trust against a hook, and a user reviews the
	// file, so setup must not change what it does not own.
	after, err := hooks.FormatDocumentExact(document)
	if err != nil {
		return fmt.Errorf("%s: %w", codexHooksFile, err)
	}

	plan.Writes = append(plan.Writes, FileWrite{
		Path: codexHooksFile, After: after,
		Detail: codexHooksDetail(managed), Narrate: narrate,
	})

	return nil
}

// codexInstalledGateMode reads the mode of the managed gate hook from a
// parsed hooks.json: the mode of the owned command under a matcher that
// fires for the shell tool, enforce when any of them enforces, or ""
// when none runs. The spec of either mode finds the hook of both.
func codexInstalledGateMode(document map[string]any) string {
	return hooks.EffectiveGateMode(document, hooks.CodexSpecs(hooks.ModeWarn)[0], hooks.CodexMatcher)
}

// codexHookSources lists every definition of the spec's hook that Codex
// runs for the spec's tools: the owned and wrapped commands of
// hooks.json, and the inline [hooks] commands of config.toml. The
// command text alone is not coverage: a gate command under PostToolUse,
// or under a matcher that only fires for apply_patch, gates no shell
// command, and a setup that took it for one would leave the shell
// ungated. Such a definition is named in an info finding and not
// listed. An inline entry in a layout the reader does not know has no
// matcher or type to check, so it counts as a source that can run the
// hook for every tool: a false "runs" leaves the user without any hook.
func codexHookSources(findings *[]Finding, document map[string]any, inline []approve.InlineHook, spec hooks.Spec) []hookSource {
	sources := documentSources(findings, document, hooks.CodexMatcher, codexHooksFile, spec, true)

	for _, entry := range inline {
		use := hooks.SeamarkHookUse(entry.Command, spec.Markers())
		if use == hooks.HookNotRun {
			continue
		}

		path := approve.CodexConfig + " [hooks]"

		if entry.Event != spec.Event ||
			(entry.Known && (entry.Type != "command" || !hooks.Fires(spec, entry.Matcher, hooks.CodexMatcher))) {
			*findings = append(*findings, neverFiresFinding(path, entry.Command, entry.Event, entry.Matcher, spec))

			continue
		}

		source := hookSource{
			path: path, command: entry.Command, certain: entry.Known && use == hooks.HookRuns,
			mode: markerMode(spec, func(marker string) bool {
				return hooks.SeamarkHookUse(entry.Command, []string{marker}) != hooks.HookNotRun
			}),
		}

		if entry.Known {
			source.tools = firingTools(spec, entry.Matcher, hooks.CodexMatcher)
		} else {
			source.tools = spec.Tools()
		}

		sources = append(sources, source)
	}

	return sources
}

// reportCodexHookSources records each unmanaged source of one hook and
// reports whether setup must leave that hook alone. owned says whether
// the managed handler already exists.
//
// Only a definition that certainly runs the hook stops the install. A
// false "runs" leaves the user without any hook, and a false "does not
// run" gives one extra reminder or one extra evaluation. A command that
// only can run the hook, such as an unknown program with the seamark
// command as its arguments, therefore gets a warning, and setup installs
// the managed handler.
//
// A managed handler that already exists stays managed. Setup keeps it
// current, because a removed handler is a bigger change than the user
// asked for, and it warns that the hook runs twice.
func reportCodexHookSources(plan *ClientPlan, sources []hookSource, owned bool) bool {
	omit := false

	for _, s := range sources {
		command := describeCommand(s.command)

		finding := Finding{Level: FindingWarning, Path: codexHooksFile, Action: "remove one of the two definitions"}

		switch {
		case !s.certain:
			finding.Reason = fmt.Sprintf("%s has `%s`, which can run the seamark hook; setup cannot tell, so it installed "+
				"the managed handler; when that command runs the hook, the hook runs twice", s.path, command)
		case owned:
			finding.Reason = fmt.Sprintf("%s also runs the seamark hook: `%s`; the managed handler in %s exists too, "+
				"so the hook runs twice", s.path, command, codexHooksFile)
		default:
			omit = true
			finding.Reason = fmt.Sprintf("%s already runs the seamark hook: `%s`; setup does not manage that definition, "+
				"so it installed no second handler", s.path, command)
			finding.Action = "to let setup manage the hook, remove that definition and run the command again"
		}

		plan.Findings = append(plan.Findings, finding)
	}

	return omit
}

// codexHooksDetail names the managed hooks in init's words, from the
// specs that were merged: "gate + lessons hooks", or the one that was.
func codexHooksDetail(specs []hooks.Spec) string {
	names := make([]string, 0, len(specs))

	for _, spec := range specs {
		names = append(names, spec.Name)
	}

	if len(names) == 1 {
		return names[0] + " hook"
	}

	return strings.Join(names, " + ") + " hooks"
}

// narrateCodexHooks prints the hooks.json lines in the form of the
// Claude Code lines: the file line, then each managed hook command, then
// the note when enforcement leaves the gate hook. What runs on which
// tool must never require opening the file to find out.
func narrateCodexHooks(w io.Writer, binary string, specs []hooks.Spec, change codexHooksChange, preview bool) {
	switch {
	case len(specs) == 0:
		fmt.Fprintf(w, "  kept    %s (no hook installed: the seamark hooks run from definitions that setup does not manage)\n", codexHooksFile)
	case !change.changed:
		fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", codexHooksFile)
	case change.created:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("wrote  ", "would write", preview), codexHooksFile, codexHooksDetail(specs))
	default:
		fmt.Fprintf(w, "  %s %s (%s)\n", verb("updated", "would update", preview), codexHooksFile, codexHooksDetail(specs))
	}

	for _, spec := range specs {
		where := spec.Event
		if spec.Matcher != "" {
			where += " " + spec.Matcher
		}

		fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(binary))
	}

	// The note states only what changed, the hook flag. Whether anything
	// still blocks is the effective-mode line's job: a kept enforce
	// policy blocks whatever the flag says.
	if change.previous == hooks.ModeEnforce && change.gateMode == hooks.ModeWarn {
		fmt.Fprintf(w, "  note    %s --enforce from the Codex gate hook: the hook follows .seamark/policy.yaml\n"+
			"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n",
			verb("removed", "would remove", preview))
	}
}

// codexHooksChange says what the plan does to hooks.json.
type codexHooksChange struct {
	changed bool // the plan writes the file
	created bool // the file does not exist yet
	// gateMode is the mode the managed gate hook runs in after the
	// plan, and previous the mode it ran in before.
	gateMode, previous string
}

// codexInlineCommands returns the hooks of the second project hook
// source, inline [hooks] in .codex/config.toml, with their identity.
// Codex merges both sources. The registration plan can already hold the
// file. Otherwise the file is an input here, so the read may follow a
// link. A file that the function cannot read gives a warning and does
// not stop setup.
func codexInlineCommands(plan *ClientPlan, root string, config *codexConfigRead) []approve.InlineHook {
	skipped := func(reason string) []approve.InlineHook {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.CodexConfig,
			Reason: "not checked for inline seamark hooks: " + render.Sanitize(reason),
			Action: "fix the file, then run the command again",
		})

		return nil
	}

	if config == nil {
		guard, data, err := ReadInput(root, approve.CodexConfig)
		if err != nil {
			return skipped(err.Error())
		}

		plan.Reads = append(plan.Reads, guard)
		config = &codexConfigRead{data: data, exists: guard.Exists}
	}

	if !config.exists {
		return nil
	}

	entries, present, err := approve.CodexInlineHookEntries(config.data)
	if err != nil {
		return skipped(err.Error())
	}

	// The specs of either mode carry the markers of both.
	if present && !slices.ContainsFunc(entries, func(entry approve.InlineHook) bool {
		return slices.ContainsFunc(hooks.CodexSpecs(hooks.ModeWarn), func(spec hooks.Spec) bool {
			return hooks.SeamarkHookUse(entry.Command, spec.Markers()) != hooks.HookNotRun
		})
	}) {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingInfo,
			Path:   approve.CodexConfig,
			Reason: "inline [hooks] are present; Codex merges them with " + codexHooksFile + " and warns at startup",
		})
	}

	return entries
}

// codexWriteDetail says what the write adds, in init's words.
func codexWriteDetail(config *approve.CodexPlan) string {
	var parts []string

	if config.Register {
		parts = append(parts, "registered seamark mcp")
	}

	if len(config.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("approved %d tools: %s", len(config.Missing), strings.Join(config.Missing, ", ")))
	}

	return strings.Join(parts, "; ")
}

// codexKeptDetail says why the file needs nothing.
func codexKeptDetail(config *approve.CodexPlan) string {
	if !config.Registered {
		return "seamark not registered"
	}

	return fmt.Sprintf("seamark registered as %q; %d/%d tools approved",
		render.Sanitize(config.Server), len(config.Approved), len(approve.Tools))
}

// Inspect reports the registration, the grants, and the installed gate
// hook mode, offline. The registration and the grants are pending
// verification: the native check for the generated file is defined and
// has not run against this version of the setup.
func (codexSetup) Inspect(root string) Inspection {
	evidence := VerificationEvidence{Level: VerificationPending, Surface: "project " + approve.CodexConfig}
	record := approve.InspectCodex(root)

	registration := CapabilityInspection{
		Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence,
	}

	switch {
	case record.Err != "":
		registration.State, registration.Detail = StateUnreadable, unreadableDetail(errors.New(record.Err))
	case record.Registered != "":
		registration.State = StateCurrent
		registration.Detail = fmt.Sprintf("registered in %s as %q", approve.CodexConfig, render.Sanitize(record.Registered))
	case len(record.Conflicts) > 0:
		// Without a registration every conflict is about the registration:
		// the name is taken, or the layout cannot take the table.
		registration.State = StateConflict
		registration.Detail = render.Sanitize(strings.Join(record.Conflicts, "; "))
	default:
		registration.State = StateAbsent
	}

	registration.Action = registrationAction(CodexID, registration.State)

	// The skills entry carries evidence only; the registry fills the
	// directory state. The 2026-09-03 trial loaded the skills from
	// .agents/skills on codex-cli 0.152.1 (compatibility record).
	skillsEvidence := CapabilityInspection{Capability: CapabilitySkills, Supported: true, Verification: codexSkillsEvidence}

	hookEntries, gateMode, managedGateMode, findings := inspectCodexHooks(root)

	return Inspection{
		ClientID:        CodexID,
		Capabilities:    append([]CapabilityInspection{skillsEvidence, registration, inspectGrants(record, evidence)}, hookEntries...),
		GateMode:        gateMode,
		ManagedGateMode: managedGateMode,
		Findings:        findings,
	}
}

// The native evidence of the Codex adapter, from the compatibility
// record. A verified level names the tested version and surface; a
// pending one names the surface whose check has not run. The edit hook
// was observed in the 2026-09-21 native acceptance run: real apply_patch
// envelopes, one lesson per patch, model-visible context. The gate hook
// and the reset decoder have no native run yet.
var (
	codexSkillsEvidence   = mustVerifiedEvidence("codex-cli 0.152.1", ".agents/skills")
	codexEditsEvidence    = mustVerifiedEvidence("codex-cli 0.154.0", "PreToolUse apply_patch")
	codexCommandsEvidence = VerificationEvidence{Level: VerificationPending, Surface: "PreToolUse Bash"}
	codexResetsEvidence   = VerificationEvidence{Level: VerificationPending, Surface: "PostCompact"}
)

// mustVerifiedEvidence builds verified evidence from constants; the
// contract tests validate every entry, so a blank scope is a
// programming error.
func mustVerifiedEvidence(clientVersion, surface string) VerificationEvidence {
	e, err := VerifiedEvidence(clientVersion, surface)
	if err != nil {
		panic("integration: " + err.Error())
	}

	return e
}

// codexResetDetail explains the absent reset hook, so narration does
// not call it missing: setup installs none on purpose.
const codexResetDetail = "no reset hook is installed: reminders repeat, so a reset has nothing to clear " +
	"(the reset of a subagent is unverified)"

// inspectCodexHooks classifies the lifecycle hooks from .codex/hooks.json
// and the inline [hooks] of .codex/config.toml, from the same evidence
// setup plans with: every definition of each hook with the tools Codex
// runs it for. It reports the gate mode of every definition that runs,
// the mode of the managed hook alone, and the limitations the reader
// must know: trust that seamark cannot read, the receiving context the
// adapter does not identify, hooks turned off in the project
// configuration, and a definition Codex never runs.
func inspectCodexHooks(root string) (entries []CapabilityInspection, gateMode, managedGateMode string, findings []Finding) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: codexEditsEvidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: codexCommandsEvidence}
	resets := CapabilityInspection{
		Capability: CapabilityResets, Supported: true, Verification: codexResetsEvidence, Detail: codexResetDetail,
	}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	guard, data, err := ReadGuarded(root, codexHooksFile)
	if err != nil {
		unreadableHooks([]*CapabilityInspection{&edits, &commands}, codexHooksFile, err)

		return collect(), "", "", nil
	}

	document := map[string]any{}

	if guard.Exists {
		if document, err = hooks.ParseDocumentExact(data); err != nil {
			unreadableHooks([]*CapabilityInspection{&edits, &commands}, codexHooksFile, err)

			return collect(), "", "", nil
		}
	}

	// The scratch plan collects the findings of the shared input readers.
	var scratch ClientPlan

	config := readCodexConfig(&scratch, root)
	inline := codexInlineCommands(&scratch, root, config)
	findings = scratch.Findings

	specs := hooks.CodexSpecs(hooks.ModeWarn)
	gate, lessons := specs[0], specs[1]

	gateSources := codexHookSources(&findings, document, inline, gate)
	classifyHook(&commands, gate, gateSources)
	sourceFindings(&findings, gate, gateSources)

	lessonSources := codexHookSources(&findings, document, inline, lessons)
	classifyHook(&edits, lessons, lessonSources)
	sourceFindings(&findings, lessons, lessonSources)

	gateMode = hookMode(gateSources)
	managedGateMode = hookMode(managedSources(gateSources))

	if edits.State != StateAbsent || commands.State != StateAbsent {
		findings = append(findings, codexTrustFinding, codexContextFinding)

		if config != nil && config.exists {
			findings = append(findings, codexHooksDisabledFinding(config.data)...)
		}
	}

	return collect(), gateMode, managedGateMode, findings
}

// readCodexConfig reads .codex/config.toml as an input, for the inline
// hooks and the feature flags. A file that cannot be read is reported
// by the inline reader, which then reads it again and names the reason.
func readCodexConfig(plan *ClientPlan, root string) *codexConfigRead {
	guard, data, err := ReadInput(root, approve.CodexConfig)
	if err != nil {
		return nil
	}

	plan.Reads = append(plan.Reads, guard)

	return &codexConfigRead{data: data, exists: guard.Exists}
}

// The limitations every installed Codex hook carries. Setup prints the
// same facts when it installs the hooks; inspection repeats them, so
// doctor and status say what init said.
var (
	codexTrustFinding = Finding{
		Level: FindingInfo,
		Path:  codexHooksFile,
		Reason: "Codex runs a project hook only after the user reviews and trusts it, and a changed hook needs a new review; " +
			"setup never grants trust, and seamark cannot read the trust record, so trust stays unverified here",
		Action: "open the project in Codex and review the hooks with /hooks",
	}
	codexContextFinding = Finding{
		Level: FindingInfo,
		Path:  codexHooksFile,
		Reason: "the Codex adapter reads no receiving context yet (the reset of a subagent is unverified), " +
			"so `hook_delivery: once-per-context` does not apply: reminders repeat, within the hook budget",
	}
)

// codexHooksDisabledFinding warns when the project configuration turns
// Codex hooks off: every installed hook is then inert, and a reader who
// sees "installed" would expect reminders and gate verdicts that never
// come. The user-level configuration can carry the flag too; the
// inspection reads the project file only and says so.
func codexHooksDisabledFinding(data []byte) []Finding {
	disabled, err := approve.CodexHooksDisabled(data)
	if err != nil || !disabled {
		return nil
	}

	return []Finding{{
		Level: FindingWarning,
		Path:  approve.CodexConfig,
		Reason: "[features] hooks = false turns every Codex hook off in this project, so the installed seamark hooks never run " +
			"(the user-level configuration is not read here)",
		Action: "set [features] hooks = true, or remove the key, to run the hooks",
	}}
}
