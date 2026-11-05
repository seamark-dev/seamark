package integration

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
)

// codexHooksFile is the one Codex hook document that setup manages.
// Codex also reads inline [hooks] in .codex/config.toml. Setup only
// inspects that source, to find a seamark hook that would run twice.
const codexHooksFile = ".codex/hooks.json"

// codexSetup plans Codex's native configuration. The MCP registration
// and the per-tool approvals share .codex/config.toml, so one plan
// composes both into one write. The approve package holds the TOML
// algorithm. It appends and inserts, and it never rewrites a user's
// line. The gate and lesson hooks are in .codex/hooks.json, a document
// of their own.
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
// reuses it, because a second read of one path gives a second guard.
// The coordinator reports a changed file when two guards of one path
// differ. Every reader returns a non-nil value, so no caller checks for
// nil.
type codexConfigRead struct {
	err    error
	data   []byte
	exists bool
}

// hooksDisabledFindings warns when the project configuration turns
// Codex hooks off. Every installed hook is then inert. A reader who sees
// "installed" would expect reminders and gate verdicts that never come.
// The user-level configuration can carry the flag too. The inspection
// reads the project file only, and the reason says so. A missing file
// and a file that does not parse give no finding.
func (c *codexConfigRead) hooksDisabledFindings() []Finding {
	if !c.exists {
		return nil
	}

	disabled, err := approve.CodexHooksDisabled(c.data)
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

	// narrate prints the line that init has always printed for this
	// file. The line names the kept explicit settings, so they need no
	// finding.
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
// An empty gate mode keeps the installed mode, or selects warn on a
// first install. Setup never adds or removes enforcement without an
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
	if err != nil {
		return ownedReadError(guard, err)
	}

	plan.Reads = append(plan.Reads, guard)

	gateMode := cmp.Or(req.GateMode, codexInstalledGateMode(document), hooks.ModeWarn)
	specs := hooks.CodexSpecs(gateMode)
	gate := specs[0]

	// One read of config.toml serves the inline hooks and the feature
	// flags. The hook planner reuses the read of the registration
	// planner when that planner ran.
	if config == nil {
		config = readCodexConfig(plan, root)
	}

	inline := codexInlineCommands(plan, config)

	var managed []hooks.Spec

	for _, spec := range specs {
		var found []Finding

		sources := codexHookSources(&found, document, inline, spec)
		plan.Findings = append(plan.Findings, withoutManagedBackground(found, spec, sources)...)
		elsewhere := unmanagedSources(sources)

		// plan.GateHooks lists each gate hook that Codex runs from a
		// definition that setup does not manage, with its own mode. It
		// lists an uncertain source too and marks it. A gate line that
		// says "nothing blocks" while a wrapped gate blocks is the worse
		// error. A gate line that says "enforce" for an echo is wrong too.
		asked := ""

		if spec.Name == gate.Name {
			asked = req.GateMode

			for _, source := range elsewhere {
				plan.GateHooks = append(plan.GateHooks, sourceGateHook(source))
			}
		}

		if reportCodexHookSources(plan, elsewhere, hooks.Owned(document, spec), asked) {
			continue
		}

		managed = append(managed, spec)
	}

	// An owned gate command stays managed, so the merge rewrites it. The
	// narrator reports an --enforce flag that the rewrite removes.
	merged, err := hooks.Merge(document, binary, managed)
	if err != nil {
		return fmt.Errorf("%s: %w", codexHooksFile, err)
	}

	plan.Findings = append(plan.Findings, backgroundRemovalFindings(codexHooksFile, merged.Removed, true)...)

	// The plan lists the managed gate hook only when, after the merge,
	// Codex runs it for the shell tool. An existing entry keeps its
	// matcher. An entry whose matcher never fires for Bash gates nothing.
	if hooks.ManagedRuns(document, gate, binary, hooks.CodexMatcher) {
		plan.GateHooks = append(plan.GateHooks, GateHook{Path: codexHooksFile, Mode: gateMode, Managed: true})
	}

	// The lesson-check hooks go into hooks.json beside the lessons hook.
	// The enforce preconditions use the sources that this plan already
	// read. They check for hooks turned off in the project configuration.
	// They also check for a definition in either source that discards the
	// exit status of the guard, or that Codex runs in the background.
	// Codex trust stays unknown, so an installed guard is at most
	// active-unverified. A run that merges the pre-write hook removes the
	// background key from the owned entry, so that key does not count.
	merging := mergesLessonChecks(document, codexLessonHooks, req)

	lessons, err := planLessonChecks(document, codexHooksFile, root, binary, codexLessonHooks, req,
		codexLessonSources(document, inline, config, merging))
	if err != nil {
		return err
	}

	plan.Findings = append(plan.Findings, backgroundRemovalFindings(codexHooksFile, lessons.removed, true)...)

	for _, spec := range lessons.specs {
		sources := unmanagedSources(codexHookSources(&plan.Findings, document, inline, spec))

		if err := refuseSecondLessonHandler(CodexID, spec, sources, lessons.requested); err != nil {
			return err
		}

		// An uncertain wrapper does not prove coverage. Setup warns about
		// it and installs the managed handler, as ordinary hook setup does.
		reportCodexHookSources(plan, sources, true, "")
	}

	change := codexHooksChange{
		created: !guard.Exists, hooksChanged: merged.Changed,
		removedEnforce: merged.RemovesEnforce(hooks.CodexGateMarker(hooks.ModeEnforce)), lessons: lessons,
	}
	narrate := func(w io.Writer, status OpStatus) {
		narrateCodexHooks(w, binary, managed, change, status == OpPlanned)
	}

	// The adapter names the receiver of a Codex event, but no reset of a
	// Codex context is verified. The once-per-context mode therefore has
	// no effect there. Setup says so once, in the words that inspection
	// repeats.
	plan.Findings = append(plan.Findings, codexContextFinding)

	// Hooks turned off in the project configuration make every hook of
	// this run inert. The reader must learn that here, not from doctor.
	plan.Findings = append(plan.Findings, config.hooksDisabledFindings()...)

	// Codex records trust against the hash of a hook. So Codex skips a
	// new or changed hook until the user reviews it. The fact holds for a
	// kept hook too, and inspection repeats it in the same words.
	if len(managed) > 0 || len(lessons.specs) > 0 {
		plan.Findings = append(plan.Findings, codexTrustFinding)
	}

	if !change.changed() {
		plan.Kept = append(plan.Kept, FileKeep{Path: codexHooksFile, Detail: change.keptDetail(managed), Narrate: narrate})

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
		Detail: change.detail(managed), Narrate: narrate,
	})

	return nil
}

// codexInlineHooksPath names the inline [hooks] of config.toml in a
// finding and in a source list. One constant keeps both names equal.
const codexInlineHooksPath = approve.CodexConfig + " [hooks]"

// codexLessonSources reads the state of the Codex hook sources that the
// enforce preconditions need. It uses the reads of the hook plan, so it
// sees the same files as the plan. An unreadable configuration gives
// unknown source state. Setup still permits the install, and the
// capability report keeps the read failure. merging is true for a plan
// that merges the pre-write hook, by the rule of claudeLessonSources.
func codexLessonSources(document map[string]any, inline []approve.InlineHook, config *codexConfigRead,
	merging bool,
) lessonSourceState {
	state := lessonSourceState{}

	if reportOnlyIn(document, codexLessonHooks, !merging) {
		state.reportOnly = append(state.reportOnly, codexHooksFile)
	}

	if reportOnlyInline(inline, codexLessonHooks) {
		state.reportOnly = append(state.reportOnly, codexInlineHooksPath)
	}

	state.hooksDisabled = len(config.hooksDisabledFindings()) > 0
	state.sourceUnreadable = config.err != nil

	return state
}

// codexInstalledGateMode reads the mode of the managed gate hook from a
// parsed hooks.json. It is the mode of the owned command under a matcher
// that fires for the shell tool. The mode is enforce when any such
// command enforces, and "" when none runs. The spec of either mode finds
// the gate hook of both modes. Plan, Inspect, and ManagedGateMode read
// the mode here, so they cannot disagree.
func codexInstalledGateMode(document map[string]any) string {
	return hooks.EffectiveGateMode(document, hooks.CodexSpecs(hooks.ModeWarn)[0], hooks.CodexMatcher)
}

// codexHookSources lists every definition of the spec's hook that Codex
// runs for the spec's tools. The definitions are the owned and wrapped
// commands of hooks.json, and the inline [hooks] commands of
// config.toml. The command text alone is not coverage. A gate command
// under PostToolUse, or under a matcher that only fires for
// apply_patch, gates no shell command. If setup counted it as a gate,
// the shell would stay ungated. An info finding names such a
// definition, and the list leaves it out. An inline entry in a layout
// the reader does not know has no matcher or type to check. It counts
// as a source that can run the hook for every tool: a false "runs"
// leaves the user without any hook.
func codexHookSources(findings *[]Finding, document map[string]any, inline []approve.InlineHook, spec hooks.Spec) []hookSource {
	sources := documentSources(findings, document, codexHookRules, codexHooksFile, spec, true)

	for _, entry := range inline {
		reading := hooks.ReadHook(entry.Command, spec.Markers())
		if reading.Use() == hooks.HookNotRun {
			continue
		}

		if entry.Event != spec.Event ||
			(entry.Known && (entry.Type != "command" || !hooks.Fires(spec, entry.Matcher, hooks.CodexMatcher))) {
			*findings = append(*findings, neverFiresFinding(codexInlineHooksPath, entry.Command, entry.Event, entry.Matcher, spec))

			continue
		}

		mode, asked := wrappedGateMode(spec, reading)
		if mode != asked {
			*findings = append(*findings, discardsStatusFinding(codexInlineHooksPath, entry.Command, mode))
		}

		source := hookSource{
			path: codexInlineHooksPath, command: entry.Command, certain: entry.Known && reading.Use() == hooks.HookRuns,
			mode: mode,
		}

		if entry.Known {
			source.tools = firingTools(spec, entry.Matcher, hooks.CodexMatcher)
		} else {
			source.tools = spec.Tools()
		}

		// The inline reader keeps only async of the execution fields, the
		// one field that Codex reads.
		var execution hooks.Execution
		if entry.Async {
			execution.Background = "async"
		}

		source.applyExecution(spec, execution)
		*findings = append(*findings, source.executionFindings(spec, CodexID)...)

		sources = append(sources, source)
	}

	return sources
}

// withoutManagedBackground removes the background finding of each
// managed source from findings. The Codex plan reads its sources before
// the merge, because they decide which specs the merge gets. An owned
// entry keeps its spec in the merge, so the merge removes the key. The
// removal finding then replaces the warning, which asks for this run.
func withoutManagedBackground(findings []Finding, spec hooks.Spec, sources []hookSource) []Finding {
	for _, s := range sources {
		if !s.managed || s.background == "" {
			continue
		}

		stale := backgroundFinding(s, spec, CodexID)
		findings = slices.DeleteFunc(findings, func(f Finding) bool { return f == stale })
	}

	return findings
}

// reportCodexHookSources records each unmanaged source of one hook and
// reports whether setup must not install the managed handler of that
// hook. owned says whether the managed handler already exists. asked is
// the gate mode the run asks for, and "" for no request or for another
// hook.
//
// Only a definition that certainly runs the hook stops the install. A
// false "runs" leaves the user without any hook, and a false "does not
// run" gives one extra reminder or one extra evaluation. A command that
// only can run the hook therefore gets a warning, and setup installs the
// managed handler. An example is an unknown program with the seamark
// command as its arguments.
//
// A run that asks for a gate mode gets a gate hook of that mode. A
// certain definition that delivers that mode, or a stronger one, makes
// the managed hook needless. One that discards its exit status, or that
// Codex runs in the background, delivers no mode, and one in warn mode
// does not deliver enforce. Setup then
// installs the managed hook beside it and warns that the hook runs
// twice. The user asked for blocking. A run that leaves nothing able to
// block must not look like one that honored the request.
//
// A managed handler that already exists stays managed. Setup keeps it
// current, because a removed handler is a bigger change than the user
// asked for. Setup also warns that the hook runs twice.
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
			finding.Reason = fmt.Sprintf("also runs the seamark hook: %#q; %s exists too, so the hook runs twice", command, managed)
		case !delivered:
			finding.Reason = fmt.Sprintf("already runs the seamark gate hook: `%s`, %s; the run asked for %s mode, "+
				"so setup installed %s too, and the hook runs twice", command, describeGateMode(s), asked, managed)
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
// for a finding. A report-only hook names its cause: a discarded exit
// status, or a run in the background.
func describeGateMode(s hookSource) string {
	switch {
	case s.mode == GateModeReportOnly && s.background != "":
		return "which runs in the background"
	case s.mode == GateModeReportOnly:
		return "which discards its exit status"
	default:
		return "in " + s.mode + " mode"
	}
}

// codexInlineCommands returns the hooks of the second project hook
// source, inline [hooks] in .codex/config.toml, with their identity.
// Codex merges both sources. config is the read of the file that the
// caller made, and it is never nil. A file that does not parse sets
// config.err, so the lesson sources report the failure without a second
// parse. A read failure in config.err, or a file that does not parse,
// gives a warning and does not stop setup.
func codexInlineCommands(plan *ClientPlan, config *codexConfigRead) []approve.InlineHook {
	skipped := func(err error) []approve.InlineHook {
		plan.Findings = append(plan.Findings, uncheckedSourceFinding(approve.CodexConfig, "inline seamark hooks", err))

		return nil
	}

	if !config.exists {
		if config.err != nil {
			return skipped(config.err)
		}

		return nil
	}

	entries, present, err := approve.CodexInlineHookEntries(config.data)
	if err != nil {
		config.err = err
		return skipped(err)
	}

	// The specs of either mode carry the markers of both. One reading
	// with the markers of every spec parses each command once. Its use is
	// the strongest use of any marker, so it equals one reading per spec.
	var markers []string
	for _, spec := range hooks.CodexSpecs(hooks.ModeWarn) {
		markers = append(markers, spec.Markers()...)
	}

	if present && !slices.ContainsFunc(entries, func(entry approve.InlineHook) bool {
		return hooks.SeamarkHookUse(entry.Command, markers) != hooks.HookNotRun
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
// verification. Their native check for the generated file is defined
// and has not run against this version of setup.
func (codexSetup) Inspect(root string) Inspection {
	evidence := VerificationEvidence{Level: VerificationPending, Surface: "project " + approve.CodexConfig}
	record := approve.InspectCodex(root)

	registration := CapabilityInspection{
		Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence,
	}

	// The record keeps its read error as text, so the text becomes the
	// error again.
	var readErr error
	if record.Err != "" {
		readErr = errors.New(record.Err)
	}

	classifyRegistration(&registration, CodexID, approve.CodexConfig, readErr, record.Registered != "", record.Registered,
		record.Conflicts)

	// The skills entry carries evidence only; the registry fills the
	// directory state. The native trial of 2026-09-03 loaded the skills
	// from .agents/skills on codex-cli 0.152.1.
	skillsEvidence := CapabilityInspection{Capability: CapabilitySkills, Supported: true, Verification: codexSkillsEvidence}

	// One read of each hook source serves every hook inspector below. So
	// the hook inspectors see the same state of each file.
	reads := readCodexHookSources(root)
	hookEntries, gate, findings := classifyCodexHooks(reads)
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
		GateBackground:    gate.background,
		PossibleGateMode:  gate.possible,
		ManagedGateMode:   gate.managed,
		HookDocumentError: gate.err,
		Findings:          findings,
		LessonHooks:       inspectCodexLessonHooks(reads),
	}
}

// InspectLessonHooks reads the hook sources with the reader of Inspect
// and skips the registration and the grants, because the lesson
// capability report reads neither.
func (codexSetup) InspectLessonHooks(root string) Inspection {
	reads := readCodexHookSources(root)
	entries, gate, _ := classifyCodexHooks(reads)

	return Inspection{
		ClientID: CodexID, Capabilities: entries, HookDocumentError: gate.err, LessonHooks: inspectCodexLessonHooks(reads),
	}
}

// codexHookReads holds .codex/hooks.json and .codex/config.toml as one
// inspection read them. Inspect passes one value to each inspector, so
// the inspectors cannot disagree about a file that changes between
// reads. When hooks.json cannot be read, err holds the error and the
// other fields stay empty. The hook inspector then reports that error
// only, and the lesson-hook reading gives nil.
type codexHookReads struct {
	err      error
	document map[string]any
	config   *codexConfigRead
	inline   []approve.InlineHook
	// findings holds the findings of the input readers, for example a
	// config.toml that does not parse. Only the hook inspection reports
	// them, so each one appears once.
	findings []Finding
}

// readCodexHookSources reads both hook sources for an inspection. It
// skips config.toml when hooks.json cannot be read, because no inspector
// uses the inline hooks without the document.
func readCodexHookSources(root string) codexHookReads {
	_, document, err := readCodexHooks(root)
	if err != nil {
		return codexHookReads{err: err}
	}

	// The scratch plan collects the findings of the shared input readers.
	// An inspection writes nothing, so its guards stay unused.
	var scratch ClientPlan

	config := readCodexConfig(&scratch, root)
	inline := codexInlineCommands(&scratch, config)

	return codexHookReads{document: document, config: config, inline: inline, findings: scratch.Findings}
}

// inspectCodexLessonHooks reads the lesson-check hooks of
// .codex/hooks.json for the capability report. It also reads the two
// source states that the enforce preconditions check. One is a
// definition of the pre-write hook in hooks.json or in the inline
// [hooks] that discards its exit status, or that Codex runs in the
// background. The other is [features] hooks = false in the
// project configuration. The reading adds no finding, so the existing
// doctor and status lines keep their bytes. An unreadable document
// gives nil, because HookDocumentError already names it. A document
// without such a hook also gives nil.
func inspectCodexLessonHooks(reads codexHookReads) *LessonHookInspection {
	if reads.err != nil {
		return nil
	}

	state := codexLessonSources(reads.document, reads.inline, reads.config, false)

	return lessonHookReading(state.inspection(reads.document, codexLessonHooks))
}

// These values are the native evidence of the Codex adapter. A verified
// level names the tested version and surface. A pending level names the
// surface whose check has not run. The native acceptance run of
// 2026-09-21 observed the edit hook: real apply_patch envelopes, one
// lesson per patch, and context that the model sees. The gate hook and
// the reset decoder have no native run yet.
var (
	codexSkillsEvidence   = mustVerifiedEvidence("codex-cli 0.152.1", ".agents/skills")
	codexEditsEvidence    = mustVerifiedEvidence("codex-cli 0.154.0", "PreToolUse apply_patch")
	codexCommandsEvidence = VerificationEvidence{Level: VerificationPending, Surface: "PreToolUse Bash"}
	codexResetsEvidence   = VerificationEvidence{Level: VerificationPending, Surface: "PostCompact"}
)

// codexResetDetail explains the absent reset hook, so narration does
// not call it missing: setup installs none on purpose.
const codexResetDetail = "no reset hook is installed: reminders repeat, so a reset has nothing to clear " +
	"(the reset of a subagent is unverified)"

// classifyCodexHooks classifies the lifecycle hooks from .codex/hooks.json
// and the inline [hooks] of .codex/config.toml, from the same evidence
// setup plans with. The evidence is every definition of each hook with
// the tools Codex runs it for. It reports the gate mode of the
// definitions that run and the mode of the ones that may run. It also
// reports the mode of the managed hook alone. It reports the limitations
// the reader must know. Those are trust that seamark cannot read, and
// the receiving context that no verified reset reaches. Hooks turned off
// in the project configuration and a definition Codex never runs are
// limitations too.
func classifyCodexHooks(reads codexHookReads) (entries []CapabilityInspection, gate gateInspection, findings []Finding) {
	edits := CapabilityInspection{Capability: CapabilityEdits, Supported: true, Verification: codexEditsEvidence}
	commands := CapabilityInspection{Capability: CapabilityCommands, Supported: true, Verification: codexCommandsEvidence}
	resets := CapabilityInspection{
		Capability: CapabilityResets, Supported: true, Verification: codexResetsEvidence, Detail: codexResetDetail,
	}

	collect := func() []CapabilityInspection { return []CapabilityInspection{edits, commands, resets} }

	if reads.err != nil {
		unreadableHooks([]*CapabilityInspection{&edits, &commands}, codexHooksFile, reads.err)

		return collect(), gateInspection{err: render.Sanitize(reads.err.Error())}, nil
	}

	findings = reads.findings

	specs := hooks.CodexSpecs(hooks.ModeWarn)
	gateSpec, lessons := specs[0], specs[1]

	gateSources := codexHookSources(&findings, reads.document, reads.inline, gateSpec)
	classifyHook(&commands, gateSpec, gateSources)
	sourceFindings(&findings, gateSpec, gateSources)

	lessonSources := codexHookSources(&findings, reads.document, reads.inline, lessons)
	classifyHook(&edits, lessons, lessonSources)
	sourceFindings(&findings, lessons, lessonSources)

	gate.mode, gate.possible = hookMode(gateSources, certainSource), hookMode(gateSources, uncertainSource)
	gate.background = backgroundReportOnly(gateSources)

	// gate.managed is the marker mode of the owned gate, also when the
	// owned entry runs in the background. It is the mode that setup keeps,
	// and the next setup run removes the background key.
	gate.managed = codexInstalledGateMode(reads.document)

	// The lesson-check hooks are read without codexHookSources. So they
	// get their findings about background keys here, and no other
	// finding, so the other doctor lines keep their bytes.
	for _, spec := range codexLessonHooks.allSpecs() {
		var scratch []Finding

		for _, s := range codexHookSources(&scratch, reads.document, reads.inline, spec) {
			findings = append(findings, s.executionFindings(spec, CodexID)...)
		}
	}

	if edits.State != StateAbsent || commands.State != StateAbsent {
		findings = append(findings, codexTrustFinding, codexContextFinding)
		findings = append(findings, reads.config.hooksDisabledFindings()...)
	}

	return collect(), gate, findings
}

// ManagedGateMode reads the mode of the managed gate hook from
// .codex/hooks.json alone, by the rule that Plan and Inspect use. An
// unreadable file gives "", and the plan then reports the error.
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
	return readOwnedDocument(root, codexHooksFile, exactParser(codexHooksFile))
}

// readCodexConfig reads .codex/config.toml for inline hooks and feature
// flags. It keeps a read failure, so the inline reader and the
// capability report give the same state without a second read.
func readCodexConfig(plan *ClientPlan, root string) *codexConfigRead {
	guard, data, err := ReadInput(root, approve.CodexConfig)
	if err != nil {
		return &codexConfigRead{err: err}
	}

	plan.Reads = append(plan.Reads, guard)

	return &codexConfigRead{data: data, exists: guard.Exists}
}

// These findings state the limitations of every installed Codex hook.
// Setup prints them when it installs the hooks. Inspection repeats
// them, so doctor and status say what init said.
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
		Reason: "the Codex adapter names the receiving context, but no reset of a Codex context is verified, " +
			"so `hook_delivery: once-per-context` does not apply: reminders repeat, within the hook budget",
	}
)
