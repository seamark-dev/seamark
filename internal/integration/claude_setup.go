package integration

import (
	"cmp"
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
// reads. It is the user's personal file. init only inspects it for a
// duplicate seamark handler, which runs twice, and for disableAllHooks,
// which can turn the hooks off or on again. init never writes it.
const claudeLocalSettings = ".claude/settings.local.json"

// claudeSetup plans Claude Code's native documents. The lifecycle hooks
// and the allow rules share .claude/settings.json, so one plan composes
// both into one write. The MCP registration lives in .mcp.json.
type claudeSetup struct{}

// Plan composes every requested edit and writes nothing. The reads keep
// the historical order of init: the settings, then the hooks merge, then
// .mcp.json, then the allow rules. So a repository with two broken files
// reports the same one first.
func (claudeSetup) Plan(root, binary string, req ClientSetup) (ClientPlan, error) {
	var (
		plan      ClientPlan
		settings  map[string]any
		narration = &claudeNarration{binary: binary}
	)

	if req.Hooks || req.ApproveTools {
		guard, parsed, err := readClaudeSettings(root)
		if err != nil {
			return ClientPlan{}, ownedReadError(guard, err)
		}

		plan.Reads = append(plan.Reads, guard)
		settings = parsed
	}

	if req.Hooks {
		if err := planClaudeHooks(&plan, narration, root, req, settings); err != nil {
			return ClientPlan{}, err
		}
	}

	// Plan reads the registration before the grants, because every tool
	// rule contains its server name.
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

// claudeDisableAllHooks is the Claude Code settings key that turns off
// every hook of the project and local settings files.
const claudeDisableAllHooks = "disableAllHooks"

// claudeLocalState says how a run read .claude/settings.local.json. A nil
// document cannot tell a file that the run did not read from an absent
// file, so the readers of disableAllHooks need this state too.
type claudeLocalState int

const (
	// claudeLocalRead means that the run read the file or found no file.
	// A nil document then holds no key.
	claudeLocalRead claudeLocalState = iota
	// claudeLocalUnread means that the run did not read the file.
	claudeLocalUnread
	// claudeLocalUnreadable means that the run could not read or parse
	// the file.
	claudeLocalUnreadable
)

// claudeLocalStateOf gives the state of a local file that the run read
// with readLocalHooks, or did not read.
func claudeLocalStateOf(read bool, err error) claudeLocalState {
	switch {
	case !read:
		return claudeLocalUnread
	case err != nil:
		return claudeLocalUnreadable
	default:
		return claudeLocalRead
	}
}

// claudeHooksDisabled reports whether disableAllHooks turns off the
// project hooks of Claude Code, and names the file that decides it.
// Claude Code merges the local file after the shared file, so a boolean
// in the local file wins: a local false turns the hooks on again. Only a
// boolean true turns the hooks off, because Claude Code tests the merged
// value strictly. A value of another type in the local file does not
// count as set, so the shared file decides. That reading never claims
// enforcement while the hooks can be off. A local file that the run did
// not read or could not read leaves the shared file alone to decide.
func claudeHooksDisabled(settings, local map[string]any, state claudeLocalState) (path string, disabled bool) {
	if state == claudeLocalRead {
		if value, ok := local[claudeDisableAllHooks].(bool); ok {
			if !value {
				return "", false
			}

			return claudeLocalSettings, true
		}
	}

	if value, ok := settings[claudeDisableAllHooks].(bool); ok && value {
		return approve.ClaudeSettings, true
	}

	return "", false
}

// claudeHooksDisabledFindings warns when disableAllHooks turns the
// project hooks off. Every installed seamark hook is then inert. A
// reader who sees "installed" would expect reminders and gate verdicts
// that never come. Claude Code also reads user-level, --settings, and
// managed settings, and the reason says that setup does not read them.
// When the local file was not read or could not be read, the reason
// says so too, because a local false there turns the hooks on again.
func claudeHooksDisabledFindings(settings, local map[string]any, state claudeLocalState) []Finding {
	path, disabled := claudeHooksDisabled(settings, local, state)
	if !disabled {
		return nil
	}

	reason := "disableAllHooks: true turns off the project hooks of Claude Code, so the installed seamark hooks never run " +
		"(user-level, --settings, and managed settings are not read here)"

	switch state {
	case claudeLocalUnread:
		reason += "; this run did not read " + claudeLocalSettings + ", where disableAllHooks: false turns the hooks on again"
	case claudeLocalUnreadable:
		reason += "; " + claudeLocalSettings + " could not be read, and disableAllHooks: false there turns the hooks on again"
	case claudeLocalRead:
	}

	return []Finding{{
		Level:  FindingWarning,
		Path:   path,
		Reason: reason,
		Action: "remove disableAllHooks, or set it to false, to run the hooks",
	}}
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
		parts = append(parts, hooksDetail(narration.specs))
	}

	if narration.lessons.changed() {
		parts = append(parts, narration.lessons.detail())
	}

	if narration.grants != nil && len(narration.grants.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d allow rules", len(narration.grants.Missing)))
	}

	// init always used this encoding. So a file that init wrote before
	// keeps its bytes when nothing in it changes.
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
// An empty gate mode keeps the installed mode, or gives warn on a first
// install. Setup never adds or removes enforcement without an explicit
// request. The narrator reports a removed --enforce flag.
// hooks.InstalledGateMode reads the installed mode for Plan, Inspect,
// and ManagedGateMode, so a re-run keeps the mode that doctor reports.
func planClaudeHooks(plan *ClientPlan, narration *claudeNarration, root string, req ClientSetup, settings map[string]any) error {
	if narration.binary == "" {
		return errors.New("setup: the seamark binary path is empty")
	}

	gateMode := cmp.Or(req.GateMode, hooks.InstalledGateMode(settings), hooks.ModeWarn)

	// Every hook goes into the shared settings, whatever another source
	// runs. The team commits the shared file. So it must be complete and
	// must not depend on the personal file of the person who ran setup.
	specs := hooks.ClaudeSpecs(gateMode)

	merged, err := hooks.Merge(settings, narration.binary, specs)
	if err != nil {
		return fmt.Errorf("%s: %w", approve.ClaudeSettings, err)
	}

	// The plan lists the gate hooks that the client runs after this plan.
	// The managed one counts when the merge left it under a matcher that
	// fires for Bash. A hook in the local file counts when the run checks
	// the other hook sources of the client (CheckHookSources). The local
	// hook keeps its own mode, and setup never changes it.
	gate := specs[0]

	if hooks.ManagedRuns(settings, gate, narration.binary, hooks.ClaudeMatcher) {
		plan.GateHooks = append(plan.GateHooks, GateHook{Path: approve.ClaudeSettings, Mode: gateMode, Managed: true})
	}

	// After the merge the shared file holds the managed hooks. Every other
	// definition of a hook is the same evidence that inspection reads. It
	// can be a wrapper in the shared file or any definition in the
	// personal file. Setup reports it with its tools and its mode, and
	// never edits it. readsClaudeLocalHooks says when a run reads the
	// local file.
	var local map[string]any
	var localErr error

	readsLocal := readsClaudeLocalHooks(req, settings)
	if readsLocal {
		local, localErr = readLocalHooks(plan, root)
	}

	localState := claudeLocalStateOf(readsLocal, localErr)

	for i, spec := range specs {
		sources := documentSources(&plan.Findings, settings, hooks.ClaudeMatcher, approve.ClaudeSettings, spec, true)

		if req.CheckHookSources {
			sources = append(sources, documentSources(&plan.Findings, local, hooks.ClaudeMatcher, claudeLocalSettings, spec, false)...)
		}

		// The plan lists the gate hooks that the client runs from
		// definitions setup does not manage, each with its own mode. It
		// lists an uncertain one too, and marks it. A gate line that says
		// "nothing blocks" while a wrapped gate blocks is the worse error.
		// One that says "enforce" for an echo is wrong too.
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
	narration.hooksChanged, narration.specs = merged.Changed, specs
	narration.removedEnforce = merged.RemovesEnforce(hooks.GateMarker(hooks.ModeEnforce))

	// Hooks turned off by disableAllHooks make every hook of this run
	// inert. The reader must learn that here, not from doctor.
	plan.Findings = append(plan.Findings, claudeHooksDisabledFindings(settings, local, localState)...)

	// The lesson-check hooks go into the shared file beside the lessons
	// hook. The enforce preconditions use the definitions that this plan
	// already read, so the decision and the guards describe one file state.
	lessons, err := planLessonChecks(settings, approve.ClaudeSettings, root, narration.binary, claudeLessonHooks, req,
		claudeLessonSources(settings, local, localState))
	if err != nil {
		return err
	}

	narration.lessons = lessons

	return checkClaudeLessonHandlers(plan, settings, local, lessons.specs, req.LessonChecks != "")
}

// readsClaudeLocalHooks reports whether a run reads the local file. An
// explicit setup reads it to report duplicate hooks. Explicit enforce
// reads it, because a local wrapper must not bypass the preflight
// refusal. Check mode reads it, because the narrator states the
// capability of the pre-write path, and a local wrapper or an unreadable
// file changes that capability. A run that keeps an installed
// lesson-check mode reads it, because the rule of one handler per lesson
// check holds for a kept handler too. Plain init in a repository without
// a lesson-check hook does not read it.
func readsClaudeLocalHooks(req ClientSetup, settings map[string]any) bool {
	if req.CheckHookSources {
		return true
	}

	mode := resolveLessonChecks(settings, claudeLessonHooks.specs(hooks.LessonChecksEnforce), req.LessonChecks)

	return mode == hooks.LessonChecksCheck || mode == hooks.LessonChecksEnforce
}

// checkClaudeLessonHandlers applies the rule of one handler per lesson
// check to the hooks that the plan merged. Claude Code runs every
// matching definition of both settings files, so a second handler runs
// each check twice. Only a run that installs or keeps a lesson-check
// hook gets here with a spec, so a repository without one is not
// refused for a definition of its own.
//
// Claude Code has several edit tools, and setup keeps a matcher that the
// user narrowed. A definition for a tool that the managed handler does
// not run for repeats no check, so it gets the finding alone.
//
// The exploration hook delivers advice and evaluates no check. So it
// keeps the rule of the other advisory hooks: a finding and no refusal.
// specs comes after the merge, so the shared file holds the managed
// handlers. requested is true when the run names a lesson-check mode.
func checkClaudeLessonHandlers(plan *ClientPlan, settings, local map[string]any, specs []hooks.Spec, requested bool) error {
	for _, spec := range specs {
		sources := claudeHookSources(&plan.Findings, settings, local, spec)

		if spec.Marker != claudeExplorationSpec.Marker {
			if err := refuseSecondLessonHandler(ClaudeID, spec, runsBesideManaged(spec, sources), requested); err != nil {
				return err
			}
		}

		sourceFindings(&plan.Findings, spec, sources)
	}

	return nil
}

// claudeHookSources lists every definition of the spec's hook in both
// settings files. Setup manages the shared file only.
func claudeHookSources(findings *[]Finding, settings, local map[string]any, spec hooks.Spec) []hookSource {
	sources := documentSources(findings, settings, hooks.ClaudeMatcher, approve.ClaudeSettings, spec, true)

	return append(sources, documentSources(findings, local, hooks.ClaudeMatcher, claudeLocalSettings, spec, false)...)
}

// runsBesideManaged keeps the definitions that setup does not manage and
// that run for a tool of the managed handler.
func runsBesideManaged(spec hooks.Spec, sources []hookSource) []hookSource {
	managedTools := coveredTools(spec, sources, managedSource)

	return slices.DeleteFunc(unmanagedSources(sources), func(s hookSource) bool {
		return len(sharedTools(s.tools, managedTools)) == 0
	})
}

// claudeLessonSources reads the state of the Claude Code hook sources
// that the enforce preconditions need. Plan and inspection both use it,
// so they agree on when the guard stays inactive. A definition in either
// settings file that discards the exit status of the pre-write hook
// keeps the guard inactive. A local file that cannot be read makes the
// source state unknown. A nil local document holds no definition.
// disableAllHooks keeps every path inactive, by the rule of
// claudeHooksDisabled.
func claudeLessonSources(settings, local map[string]any, localState claudeLocalState) lessonSourceState {
	_, disabled := claudeHooksDisabled(settings, local, localState)
	state := lessonSourceState{sourceUnreadable: localState == claudeLocalUnreadable, hooksDisabled: disabled}

	if reportOnlyIn(settings, claudeLessonHooks) {
		state.reportOnly = append(state.reportOnly, approve.ClaudeSettings)
	}

	if reportOnlyIn(local, claudeLessonHooks) {
		state.reportOnly = append(state.reportOnly, claudeLocalSettings)
	}

	return state
}

// readLocalHooks reads the user's local settings file, the second hook
// source Claude Code runs. Setup never edits it and never lets it change
// what the shared file gets. Setup reports a handler that both files
// run, and it refuses a second lesson-check handler. The plan guards the
// file, also when it is absent, because a hook added to it after the
// plan would make the report wrong. Setup reports a local file that it
// cannot read and continues, because setup does not own that file.
func readLocalHooks(plan *ClientPlan, root string) (map[string]any, error) {
	skipped := func(err error) (map[string]any, error) {
		plan.Findings = append(plan.Findings, uncheckedSourceFinding(claudeLocalSettings, "duplicate seamark hooks", err))

		return nil, err
	}

	// An input: the read may follow a link, for example into a dotfiles
	// directory.
	guard, data, err := ReadInput(root, claudeLocalSettings)
	if err != nil {
		return skipped(err)
	}

	plan.Reads = append(plan.Reads, guard)

	if !guard.Exists {
		return nil, nil
	}

	local, err := hooks.ParseDocument(data)
	if err != nil {
		return skipped(err)
	}

	return local, nil
}

// reportMissingCoverage names each tool that no source runs the handler
// for after the merge. It uses the same evidence that inspection reads,
// so a wrapper or a personal definition that covers a tool covers it.
// Setup never rewrites the matcher of an existing entry, because a
// narrow matcher can be the user's choice. So it cannot always complete
// the coverage, and then it must say so instead of reporting a complete
// install. sourceFindings reports a tool that only an uncertain
// definition covers.
func reportMissingCoverage(plan *ClientPlan, spec hooks.Spec, sources []hookSource) {
	missing := toolNames(missingTools(spec, coveredTools(spec, sources, anySource)))
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

// planClaudeGrants appends the missing allow rules to the parsed
// settings. An explicit deny or ask entry is not an error: the rest of
// the setup is still safe, and the narrator names the kept entries.
func planClaudeGrants(narration *claudeNarration, settings map[string]any, mcp *approve.ClaudeMCPPlan) error {
	// A conflicting .mcp.json registers nothing, so the rules use the
	// conventional name. approve.Registration.ServerName uses the same rule.
	grants, err := approve.PlanClaude(settings, cmp.Or(mcp.Server, approve.ClaudeServer))
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

// claudeHookEvidence is the evidence of the Claude Code entries without
// a native recording. The lesson capability report copies it from the
// resets entry, so both readings of the hooks use this value.
var claudeHookEvidence = VerificationEvidence{Level: VerificationUnverified}

// Inspect reports the native documents of Claude Code, offline: the
// registration, the grants, the lifecycle hooks, and the lesson-check
// hooks. No native recording verifies these entries, so each capability
// entry stays unverified.
func (claudeSetup) Inspect(root string) Inspection {
	evidence := claudeHookEvidence

	hookEntries, gate, findings, lessons := inspectClaudeHooks(root, evidence)

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
		LessonHooks:       lessons,
	}
}

// InspectLessonHooks reads the two settings files with the reader of
// Inspect and skips the other documents. The capture inspection can
// start Git for a finding, and the lesson capability report reads no
// finding.
func (claudeSetup) InspectLessonHooks(root string) Inspection {
	entries, gate, _, lessons := inspectClaudeHooks(root, claudeHookEvidence)

	return Inspection{ClientID: ClaudeID, Capabilities: entries, HookDocumentError: gate.err, LessonHooks: lessons}
}

// gateInspection is what an inspection learns about the gate hooks of
// one client. mode is the mode of the definitions that certainly run
// the gate. possible is the mode of the definitions that may run it.
// managed is the mode of the managed hook. err is the reason when the
// managed document cannot be read. It is sanitized, because it reaches
// terminals and MCP clients as it is.
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
//
// lessons is the reading of the lesson-check and exploration hooks for
// the capability report, from the same two reads. It takes the
// hook-source state from claudeLessonSources, as the plan does, and it
// adds no finding. An unreadable settings file gives nil lessons,
// because gate.err already names it. A file without such a hook also
// gives nil.
//
// The disableAllHooks finding comes once, when a hook entry is not
// absent. A repository without seamark hooks gets no finding for a
// setting of its own.
func inspectClaudeHooks(root string, evidence VerificationEvidence) (
	entries []CapabilityInspection, gate gateInspection, findings []Finding, lessons *LessonHookInspection,
) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: evidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: evidence}
	resets := CapabilityInspection{Capability: CapabilityResets, Supported: true, Verification: evidence}

	// all follows the order of ClaudeSpecs: the gate hook first, then the
	// lessons hook, then the context reset.
	all := []*CapabilityInspection{&commands, &edits, &resets}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	_, settings, err := readClaudeSettings(root)
	if err != nil {
		unreadableHooks(all, approve.ClaudeSettings, err)

		return collect(), gateInspection{err: render.Sanitize(err.Error())}, nil, nil
	}

	// The local file is an input: a file that cannot be read is
	// reported and does not make the shared file's hooks unknown.
	var scratch ClientPlan

	local, localErr := readLocalHooks(&scratch, root)
	findings = scratch.Findings

	for i, spec := range hooks.ClaudeSpecs(hooks.ModeWarn) {
		sources := claudeHookSources(&findings, settings, local, spec)

		classifyHook(all[i], spec, sources)
		sourceFindings(&findings, spec, sources)

		if i == 0 {
			gate.mode, gate.possible = hookMode(sources, certainSource), hookMode(sources, uncertainSource)
		}
	}

	// Inspection always reads the local file, so only a read failure
	// leaves the shared file alone to decide disableAllHooks.
	localState := claudeLocalStateOf(true, localErr)

	gate.managed = hooks.InstalledGateMode(settings)
	lessons = lessonHookReading(claudeLessonSources(settings, local, localState).inspection(settings, claudeLessonHooks))

	installed := lessons != nil || slices.ContainsFunc(all, func(c *CapabilityInspection) bool {
		return c.State != StateAbsent
	})

	if installed {
		findings = append(findings, claudeHooksDisabledFindings(settings, local, localState)...)
	}

	return collect(), gate, findings, lessons
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
// an input, so the read follows a link, as the plan's read does. stops
// is true when the explicit setup of Claude Code stops at the file. The
// plan then cannot read the file, or it must write through a link.
func inspectClaudeRegistration(root string, evidence VerificationEvidence) (entry CapabilityInspection, stops bool) {
	entry = CapabilityInspection{Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence}

	guard, data, err := ReadInput(root, approve.MCPConfig)
	if err != nil {
		classifyRegistration(&entry, ClaudeID, approve.MCPConfig, err, false, "", nil)

		return entry, true
	}

	// A plan error leaves no plan, so the error alone decides the state.
	if mcp, err := approve.PlanClaudeMCP(data, guard.Exists); err != nil {
		classifyRegistration(&entry, ClaudeID, approve.MCPConfig, err, false, "", nil)
	} else {
		classifyRegistration(&entry, ClaudeID, approve.MCPConfig, nil, mcp.Registered, mcp.Server, mcp.Conflicts)
	}

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
