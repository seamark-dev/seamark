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

	guard, document, err := readCodexHooks(root)

	switch {
	case err != nil && guard.Exists:
		return fmt.Errorf("%w (fix or move it, then re-run)", err)
	case err != nil:
		return err
	}

	plan.Reads = append(plan.Reads, guard)

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
		// each with its own mode. An uncertain source is listed too, marked
		// as such. A gate line that says "nothing blocks" while a wrapped
		// gate blocks is the worse error. One that says "enforce" for an
		// echo is wrong too.
		asked := ""

		if spec.Name == gate.Name {
			asked = req.GateMode

			for _, source := range elsewhere {
				plan.GateHooks = append(plan.GateHooks, GateHook{Path: source.path, Mode: source.mode, Uncertain: !source.certain})
			}
		}

		if reportCodexHookSources(plan, elsewhere, hooks.Owned(document, spec), asked) {
			continue
		}

		managed = append(managed, spec)
	}

	// An owned gate command stays managed, so the merge rewrites it, and
	// the narrator reports a removed --enforce from the rewrite.
	merged, err := hooks.Merge(document, binary, managed)
	if err != nil {
		return fmt.Errorf("%s: %w", codexHooksFile, err)
	}

	// The managed gate hook, when the merge left it where Codex runs it
	// for the shell tool. An existing entry keeps its matcher, and one
	// that never fires for Bash gates nothing.
	if hooks.ManagedRuns(document, gate, binary, hooks.CodexMatcher) {
		plan.GateHooks = append(plan.GateHooks, GateHook{Path: codexHooksFile, Mode: gateMode, Managed: true})
	}

	change := codexHooksChange{
		changed: merged.Changed, created: !guard.Exists,
		removedEnforce: merged.RemovesEnforce(hooks.CodexGateMarker(hooks.ModeEnforce)),
	}
	changed := merged.Changed
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
// parsed hooks.json. It is the mode of the owned command under a matcher
// that fires for the shell tool, enforce when any of them enforces, or
// "" when none runs. The spec of either mode finds the hook of both.
// Plan, Inspect, and ManagedGateMode read the mode here, so they cannot
// disagree.
func codexInstalledGateMode(document map[string]any) string {
	return hooks.EffectiveGateMode(document, hooks.CodexSpecs(hooks.ModeWarn)[0], hooks.CodexMatcher)
}

// codexHookSources lists every definition of the spec's hook that Codex
// runs for the spec's tools. The definitions are the owned and wrapped
// commands of hooks.json, and the inline [hooks] commands of
// config.toml. The command text alone is not coverage. A gate command
// under PostToolUse, or under a matcher that only fires for
// apply_patch, gates no shell command. A setup that took it for one
// would leave the shell ungated. Such a definition is named in an info
// finding and not listed. An inline entry in a layout the reader does
// not know has no matcher or type to check. It counts as a source that
// can run the hook for every tool: a false "runs" leaves the user
// without any hook.
func codexHookSources(findings *[]Finding, document map[string]any, inline []approve.InlineHook, spec hooks.Spec) []hookSource {
	sources := documentSources(findings, document, hooks.CodexMatcher, codexHooksFile, spec, true)

	for _, entry := range inline {
		reading := hooks.ReadHook(entry.Command, spec.Markers())
		if reading.Use() == hooks.HookNotRun {
			continue
		}

		path := approve.CodexConfig + " [hooks]"

		if entry.Event != spec.Event ||
			(entry.Known && (entry.Type != "command" || !hooks.Fires(spec, entry.Matcher, hooks.CodexMatcher))) {
			*findings = append(*findings, neverFiresFinding(path, entry.Command, entry.Event, entry.Matcher, spec))

			continue
		}

		mode, asked := wrappedGateMode(spec, reading)
		if mode != asked {
			*findings = append(*findings, discardsStatusFinding(path, entry.Command, mode, asked))
		}

		source := hookSource{
			path: path, command: entry.Command, certain: entry.Known && reading.Use() == hooks.HookRuns, mode: mode,
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
// the managed handler already exists. asked is the gate mode the run
// asks for, and "" for no request or for another hook.
//
// Only a definition that certainly runs the hook stops the install. A
// false "runs" leaves the user without any hook, and a false "does not
// run" gives one extra reminder or one extra evaluation. A command that
// only can run the hook, such as an unknown program with the seamark
// command as its arguments, therefore gets a warning, and setup installs
// the managed handler.
//
// A run that asks for a gate mode gets a gate hook of that mode. A
// certain definition that delivers that mode, or a stronger one, makes
// the managed hook needless. One that discards its exit status delivers
// no mode, and one in warn mode does not deliver enforce. Setup then
// installs the managed hook beside it and warns that the hook runs
// twice. The user asked for blocking. A run that leaves nothing able to
// block must not look like one that honored the request.
//
// A managed handler that already exists stays managed. Setup keeps it
// current, because a removed handler is a bigger change than the user
// asked for, and it warns that the hook runs twice.
//
// Each finding names the document that holds the definition. Every
// consumer prints that path first, so the reason does not repeat it.
func reportCodexHookSources(plan *ClientPlan, sources []hookSource, owned bool, asked string) bool {
	certain := slices.ContainsFunc(sources, certainSource)
	delivered := asked == "" || slices.ContainsFunc(sources, func(s hookSource) bool {
		return s.certain && deliversGateMode(s.mode, asked)
	})

	// A certain definition that setup does not own stops the install,
	// unless the run asks for a mode that no such definition delivers.
	omit := !owned && certain && delivered

	for _, s := range sources {
		command := describeCommand(s.command)

		// The managed handler is in hooks.json. A definition in another
		// document makes the finding about two files, so the reason then
		// names hooks.json too.
		managed := "the managed handler"
		if s.path != codexHooksFile {
			managed += " in " + codexHooksFile
		}

		finding := Finding{Level: FindingWarning, Path: s.path, Action: "remove one of the two definitions"}

		switch {
		case !s.certain && omit:
			finding.Reason = fmt.Sprintf("has `%s`, which can run the seamark hook; setup cannot tell, and it installed "+
				"no handler of its own, because another definition already runs the hook", command)
			finding.Action = "to let setup manage the hook, remove both definitions and run the command again"
		case !s.certain:
			finding.Reason = fmt.Sprintf("has `%s`, which can run the seamark hook; setup cannot tell, so it installed "+
				"%s; when that command runs the hook, the hook runs twice", command, managed)
		case owned:
			finding.Reason = fmt.Sprintf("also runs the seamark hook: `%s`; %s exists too, so the hook runs twice", command, managed)
		case !delivered:
			finding.Reason = fmt.Sprintf("already runs the seamark gate hook: `%s`, %s; the run asked for %s mode, "+
				"so setup installed %s too, and the hook runs twice", command, describeGateMode(s.mode), asked, managed)
			finding.Action = "remove that definition to let setup manage the gate, or remove the managed handler to keep yours"
		default:
			finding.Reason = fmt.Sprintf("already runs the seamark hook: `%s`; setup does not manage that definition, "+
				"so it installed no second handler", command)
			finding.Action = "to let setup manage the hook, remove that definition and run the command again"
		}

		plan.Findings = append(plan.Findings, finding)
	}

	return omit
}

// deliversGateMode reports whether a gate hook in the mode delivers the
// mode a run asks for. Enforce delivers every request: it blocks more
// than warn, never less. A report-only hook delivers none: no verdict
// of it blocks.
func deliversGateMode(mode, asked string) bool {
	return mode == asked || mode == hooks.ModeEnforce
}

// describeGateMode names what a wrapped gate hook does with a verdict,
// for a finding.
func describeGateMode(mode string) string {
	if mode == GateModeReportOnly {
		return "which discards its exit status"
	}

	return "in " + mode + " mode"
}

// codexInlineCommands returns the hooks of the second project hook
// source, inline [hooks] in .codex/config.toml, with their identity.
// Codex merges both sources. The registration plan can already hold the
// file. Otherwise the file is an input here, so the read may follow a
// link. A file that the function cannot read gives a warning and does
// not stop setup.
func codexInlineCommands(plan *ClientPlan, root string, config *codexConfigRead) []approve.InlineHook {
	skipped := func(err error) []approve.InlineHook {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.CodexConfig,
			Reason: "not checked for inline seamark hooks: " + render.Sanitize(readReason(approve.CodexConfig, err)),
			Action: "fix the file, then run the command again",
		})

		return nil
	}

	if config == nil {
		guard, data, err := ReadInput(root, approve.CodexConfig)
		if err != nil {
			return skipped(err)
		}

		plan.Reads = append(plan.Reads, guard)
		config = &codexConfigRead{data: data, exists: guard.Exists}
	}

	if !config.exists {
		return nil
	}

	entries, present, err := approve.CodexInlineHookEntries(config.data)
	if err != nil {
		return skipped(err)
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

	hookEntries, gate, findings := inspectCodexHooks(root)
	capabilities := append([]CapabilityInspection{skillsEvidence, registration, inspectGrants(record, evidence)}, hookEntries...)

	// Setup owns both documents. Inspection still reads the inline hooks
	// of config.toml through a link, as the plan does when it registers
	// nothing. Codex runs those hooks, so inspection reports them.
	linkActions(root, capabilities, map[Capability]string{
		CapabilityMCPRegistration: approve.CodexConfig, CapabilityToolGrants: approve.CodexConfig,
		CapabilityEdits: codexHooksFile, CapabilityCommands: codexHooksFile,
	})

	// Every Codex setup that installs hooks also plans the registration,
	// so it stops at a config.toml that the plan cannot read. The fix of
	// that file then comes first.
	restore := "run `seamark init --client codex` to install the missing hook"
	if i := slices.IndexFunc(capabilities, func(e CapabilityInspection) bool {
		return e.Capability == CapabilityMCPRegistration && e.State == StateUnreadable
	}); i >= 0 {
		restore = capabilities[i].Action
	}

	restoreHooks(capabilities, restore, CapabilityEdits, CapabilityCommands)

	return Inspection{
		ClientID:          CodexID,
		Capabilities:      capabilities,
		GateMode:          gate.mode,
		PossibleGateMode:  gate.possible,
		ManagedGateMode:   gate.managed,
		HookDocumentError: gate.err,
		Findings:          findings,
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
// setup plans with. The evidence is every definition of each hook with
// the tools Codex runs it for. It reports the gate mode of the
// definitions that run, the mode of the ones that may run, and the mode
// of the managed hook alone. It also reports the limitations the reader
// must know. Those are trust that seamark cannot read, and the receiving
// context the adapter does not identify. Hooks turned off in the project
// configuration and a definition Codex never runs are limitations too.
func inspectCodexHooks(root string) (entries []CapabilityInspection, gate gateInspection, findings []Finding) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: codexEditsEvidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: codexCommandsEvidence}
	resets := CapabilityInspection{
		Capability: CapabilityResets, Supported: true, Verification: codexResetsEvidence, Detail: codexResetDetail,
	}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	_, document, err := readCodexHooks(root)
	if err != nil {
		unreadableHooks([]*CapabilityInspection{&edits, &commands}, codexHooksFile, err)

		return collect(), gateInspection{err: render.Sanitize(err.Error())}, nil
	}

	// The scratch plan collects the findings of the shared input readers.
	var scratch ClientPlan

	config := readCodexConfig(&scratch, root)
	inline := codexInlineCommands(&scratch, root, config)
	findings = scratch.Findings

	specs := hooks.CodexSpecs(hooks.ModeWarn)
	gateSpec, lessons := specs[0], specs[1]

	gateSources := codexHookSources(&findings, document, inline, gateSpec)
	classifyHook(&commands, gateSpec, gateSources)
	sourceFindings(&findings, gateSpec, gateSources)

	lessonSources := codexHookSources(&findings, document, inline, lessons)
	classifyHook(&edits, lessons, lessonSources)
	sourceFindings(&findings, lessons, lessonSources)

	gate.mode, gate.possible = hookMode(gateSources, certainSource), hookMode(gateSources, uncertainSource)
	gate.managed = codexInstalledGateMode(document)

	if edits.State != StateAbsent || commands.State != StateAbsent {
		findings = append(findings, codexTrustFinding, codexContextFinding)

		if config != nil && config.exists {
			findings = append(findings, codexHooksDisabledFinding(config.data)...)
		}
	}

	return collect(), gate, findings
}

// ManagedGateMode reads the mode of the managed gate hook from
// .codex/hooks.json alone, by the rule that Plan and Inspect use. A file
// that cannot be read gives "": the plan then reports the error.
func (codexSetup) ManagedGateMode(root string) string {
	_, document, err := readCodexHooks(root)
	if err != nil {
		return ""
	}

	return codexInstalledGateMode(document)
}

// readCodexHooks reads and parses .codex/hooks.json, by the rule of
// readOwnedDocument. Codex records trust against the hook text, so the
// exact parser keeps the user's numbers and command text as written. A
// parse error names the file, as the Claude Code parser does, so every
// reason about a hook document reads alike.
func readCodexHooks(root string) (FileGuard, map[string]any, error) {
	return readOwnedDocument(root, codexHooksFile, func(data []byte) (map[string]any, error) {
		document, err := hooks.ParseDocumentExact(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", codexHooksFile, err)
		}

		return document, nil
	})
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
