package integration

// The lesson-check setup. `seamark init --lesson-checks off|check|enforce`
// and `--lesson-exploration off|on` install, switch, and remove the
// lesson-check and exploration hooks of a client. The entries go into
// the hook document that holds the lesson hook of the client, through
// the coordinator of every other setup write. This file holds what both
// client adapters share: the intent rules, the enforce preconditions,
// the composition, and the narration of the result.
//
// An empty intent preserves the installed choice, so a run without the
// flags installs nothing new. No rule here reads or changes the gate
// mode.

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
)

// lessonContexts are the context paths of every hook. Both built-in
// clients name a subagent in their edit events, so each hook has a root
// path and a child path.
var lessonContexts = []lessoncheck.PathContext{lessoncheck.PathContext(LaneRoot), lessoncheck.PathContext(LaneChild)}

// explorationNotice is the notice for --lesson-exploration on and a
// client without the structured Read path. The run does not fail,
// because preparation covers such a client.
const explorationNotice = "lesson exploration: nothing was installed for %s (no structured Read path); " +
	"preparation (seamark lessons --prepare) covers it"

// WithLessonChecks returns a copy of the setups with the lesson-check
// intent on each setup that installs hooks. The lesson-check hooks go
// into the hook document of the client, so a setup without hooks gets
// no intent, as it gets no gate mode. A run without --client installs
// hooks for Claude Code only, so only Claude Code gets the intent there.
// The gate mode stays as it is.
func WithLessonChecks(setups []ClientSetup, checks, exploration string) []ClientSetup {
	out := slices.Clone(setups)

	for i := range out {
		if out[i].Hooks {
			out[i].LessonChecks, out[i].LessonExploration = checks, exploration
		}
	}

	return out
}

// validLessonIntent checks the closed values of the two intents. The
// CLI checks its flags first with its own words. This check stops a
// caller that builds a ClientSetup itself.
func validLessonIntent(intent ClientSetup) error {
	switch intent.LessonChecks {
	case "", hooks.LessonChecksOff, hooks.LessonChecksCheck, hooks.LessonChecksEnforce:
	default:
		return fmt.Errorf("setup: client %q: lesson checks must be %s, %s, or %s, got %q", intent.ClientID,
			hooks.LessonChecksOff, hooks.LessonChecksCheck, hooks.LessonChecksEnforce, intent.LessonChecks)
	}

	switch intent.LessonExploration {
	case "", hooks.LessonExplorationOff, hooks.LessonExplorationOn:
	default:
		return fmt.Errorf("setup: client %q: lesson exploration must be %s or %s, got %q", intent.ClientID,
			hooks.LessonExplorationOff, hooks.LessonExplorationOn, intent.LessonExploration)
	}

	return nil
}

// validateLessonCheckOps checks the lesson-check setup operations of a
// descriptor. Each installed hook runs a handler that needs a decoder,
// and a hook without its decoder fails on every event. Enforce installs
// the post-edit verification beside the guard, so a guard needs the
// verification operation too.
func validateLessonCheckOps(c Client) error {
	ops := c.SetupOps

	switch {
	case (ops.LessonVerify || ops.LessonGuard || ops.LessonExploration) && !ops.Hooks:
		return fmt.Errorf("client %q: lesson-check hooks are declared without hook installation", c.ID)
	case ops.LessonVerify && (c.Guards == nil || c.Completions == nil):
		return fmt.Errorf("client %q: lesson verification is declared without guard and completion decoders", c.ID)
	case ops.LessonGuard && !ops.LessonVerify:
		return fmt.Errorf("client %q: a lesson guard is declared without lesson verification", c.ID)
	case ops.LessonExploration && c.Explores == nil:
		return fmt.Errorf("client %q: lesson exploration is declared without an explore decoder", c.ID)
	}

	return nil
}

// lessonCheckIntent applies the lesson-check support of the descriptor
// to one intent, before the hook support does. An enforce request for a
// client without a pre-write guard fails, because setup never
// substitutes check-only behavior. Setup skips a check request for a
// client without the hooks and adds a finding, as for any other
// unsupported operation. An exploration request for a client without
// the structured Read path gets the notice and installs nothing.
func lessonCheckIntent(c Client, intent ClientSetup) (ClientSetup, []Finding, error) {
	var findings []Finding

	switch {
	case intent.LessonChecks == hooks.LessonChecksEnforce && !c.SetupOps.LessonGuard:
		return ClientSetup{}, nil, unsupportedEnforce(c.ID)
	case intent.LessonChecks != "" && !c.SetupOps.LessonVerify:
		// An off request for such a client has nothing to remove.
		if intent.LessonChecks == hooks.LessonChecksCheck {
			findings = append(findings, skippedFinding(c, "lesson checks are"))
		}

		intent.LessonChecks = ""
	}

	if intent.LessonExploration != "" && !c.SetupOps.LessonExploration {
		// The notice is a warning, so the form without --client prints it
		// too. That form hides the informational findings.
		if intent.LessonExploration == hooks.LessonExplorationOn {
			findings = append(findings, Finding{Level: FindingWarning, Reason: fmt.Sprintf(explorationNotice, c.ID)})
		}

		intent.LessonExploration = ""
	}

	return intent, findings, nil
}

// unsupportedEnforce is the refusal for a client without a pre-write
// guard. It names the client, then the write tools and the contexts
// that have no guard. Such a client has no guard in any context, so the
// message names every context. The tools are the guard tools that the
// registry knows for the client, sorted. A client without such a list
// gets the words "its edit tools", because the coordinator knows no
// other tool list.
func unsupportedEnforce(clientID string) error {
	tools := slices.Sorted(slices.Values(guardTools[clientID]))

	named := "its edit tools"
	if len(tools) > 0 {
		named = strings.Join(tools, ", ")
	}

	contexts := make([]string, 0, len(lessonContexts))
	for _, context := range lessonContexts {
		contexts = append(contexts, string(context))
	}

	return fmt.Errorf("lesson checks: enforce is not supported for %s (no pre-write guard for %s in %s); "+
		"choose --lesson-checks check for check-only behavior or omit the client",
		clientID, named, strings.Join(contexts, " and "))
}

// lessonHookClient is what the lesson-check planner knows about one
// client's hooks.
type lessonHookClient struct {
	id string
	// specs returns the lesson-check hooks of a mode: the pre-write hook
	// first, then the post-edit hooks.
	specs func(mode string) []hooks.Spec
	// exploration is the exploration hook, or nil for a client without
	// the structured Read path.
	exploration *hooks.Spec
	// fires is the matcher rule of the client.
	fires hooks.MatcherRule
	// exec is the execution rule of the client. It tells a pre-write
	// handler in the background and a filtered handler apart.
	exec hooks.ExecutionRule
	// trustReview is true for a client that runs a project hook only
	// after the user reviews it. Seamark cannot read that trust record.
	trustReview bool
	// uncovered lists the writer tools that no lesson-check hook covers.
	// The report names them with reason tool-uncovered, so it never
	// claims coverage that it does not have.
	uncovered []string
}

// preWrite returns the pre-write hook of the client. The enforce mode
// lists every lesson-check hook, and the pre-write hook comes first.
func (c lessonHookClient) preWrite() hooks.Spec {
	return c.specs(hooks.LessonChecksEnforce)[0]
}

// allSpecs returns every lesson-check spec of the client, then its
// exploration spec when it has one. The enforce specs list the baseline
// marker too, so they find the hooks of both modes.
func (c lessonHookClient) allSpecs() []hooks.Spec {
	specs := c.specs(hooks.LessonChecksEnforce)
	if c.exploration != nil {
		specs = append(specs, *c.exploration)
	}

	return specs
}

// The lesson-check hooks of the built-in clients.
var (
	claudeExplorationSpec = hooks.ClaudeExplorationSpec()
	claudeLessonHooks     = lessonHookClient{
		id: ClaudeID, specs: hooks.ClaudeLessonCheckSpecs, exploration: &claudeExplorationSpec, fires: hooks.ClaudeMatcher,
		exec: hooks.ClaudeExecution, uncovered: []string{"NotebookEdit", claudeToolBash},
	}
	codexLessonHooks = lessonHookClient{
		id: CodexID, specs: hooks.CodexLessonCheckSpecs, fires: hooks.CodexMatcher, exec: hooks.CodexExecution,
		trustReview: true, uncovered: []string{"shell"},
	}
)

// mergesLessonChecks reports whether a plan merges the pre-write hook of
// the client: the run installs or keeps check or enforce mode. Such a
// plan removes a background key from the owned pre-write entry before
// it writes.
func mergesLessonChecks(document map[string]any, client lessonHookClient, req ClientSetup) bool {
	mode := resolveLessonChecks(document, client.specs(hooks.LessonChecksEnforce), req.LessonChecks)

	return mode == hooks.LessonChecksCheck || mode == hooks.LessonChecksEnforce
}

// lessonSourceState is what one plan reads about the hook sources of a
// client. The enforce preconditions read it. The adapter fills it from
// the reads of its own plan, so the decision and the file guards of the
// plan describe one file state.
type lessonSourceState struct {
	// trust is the project trust of the hook document. No built-in
	// adapter reads a trust record, so it stays unknown for both.
	trust TrustState
	// disabled holds the finding of a project configuration that turns
	// every hook of the client off, or nothing. The refusal prints its
	// file and its action, so the user sees the step without the plan.
	disabled []Finding
	// reportOnly lists each definition of the pre-write hook that
	// discards the exit status of the hook, or that the client runs in
	// the background, the managed document first. No deny of such a
	// definition blocks. The refusal names each one with its step.
	reportOnly []ReportOnlyDefinition
	// sourceUnreadable is true when a supplemental hook source cannot be
	// read or parsed, so its effect is unknown. Setup still installs the
	// hooks, but the capability report cannot establish coverage.
	sourceUnreadable bool
}

// hooksDisabled reports whether the project configuration turns every
// hook of the client off.
func (s lessonSourceState) hooksDisabled() bool {
	return len(s.disabled) > 0
}

// inspection returns the lesson-hook reading of one hook document with
// this source state. The plan summary and both inspections build the
// reading here, so they cannot disagree on the source state.
func (s lessonSourceState) inspection(document map[string]any, client lessonHookClient) LessonHookInspection {
	return LessonHookInspection{
		Entries: lessonHookEntries(document, client), ReportOnly: s.reportOnly,
		HooksDisabled: s.hooksDisabled(), SourceUnreadable: s.sourceUnreadable,
	}
}

// blockers returns the known-bad states, sorted. Under enforce, each one
// keeps the guard inactive, so setup refuses before any write. Each
// reason appears at most once, so a sort is enough.
func (s lessonSourceState) blockers() []lessoncheck.CapReason {
	var reasons []lessoncheck.CapReason

	if s.trust == TrustPending {
		reasons = append(reasons, lessoncheck.CapUntrusted)
	}

	if s.hooksDisabled() {
		reasons = append(reasons, lessoncheck.CapHooksDisabled)
	}

	if len(s.reportOnly) > 0 {
		reasons = append(reasons, lessoncheck.CapReportOnlyWrapper)
	}

	slices.Sort(reasons)

	return reasons
}

// enforceRefusal is the refusal of an enforce request for a client in a
// known-bad state, or nil. The message lists the sorted reasons, then
// the step for each reason in the same order. A refused plan prints no
// finding, so the refusal is the only text the user sees. A step for
// every reason lets one pass of edits reach a state where enforce runs.
func enforceRefusal(clientID string, state lessonSourceState) error {
	blockers := state.blockers()
	if len(blockers) == 0 {
		return nil
	}

	var steps []string

	for _, reason := range blockers {
		switch reason {
		case lessoncheck.CapHooksDisabled:
			for _, finding := range state.disabled {
				steps = append(steps, finding.Path+": "+finding.Action)
			}
		case lessoncheck.CapReportOnlyWrapper:
			steps = append(steps, ReportOnlySteps(clientID, state.reportOnly)...)
		case lessoncheck.CapUntrusted:
			steps = append(steps, LessonTrustStep(clientID))
		}
	}

	return fmt.Errorf("lesson checks: enforce for %s would stay inactive (%s); %s; then rerun",
		clientID, lessoncheck.JoinCapReasons(blockers), strings.Join(steps, "; "))
}

// LessonTrustStep is the step for a client whose lesson-check hooks
// wait for the user's trust review. Seamark cannot grant that trust, so
// the step asks the user. The enforce refusal and doctor print it.
func LessonTrustStep(clientID string) string {
	return "review and trust the lesson-check hooks in " + clientID
}

// ReportOnlySteps returns one step for each report-only definition. The
// enforce refusal and doctor both print these steps, so one state gets
// one text. A record equal to an earlier one in each field gives no
// second step, because one edit removes both copies. The removals keep
// the record order, and the init step of the managed entry comes last.
// Init refuses while another definition runs the hook beside the managed
// handler, so the init step runs only after each removal.
func ReportOnlySteps(clientID string, defs []ReportOnlyDefinition) []string {
	var (
		removals, inits []string
		seen            []ReportOnlyDefinition
	)

	for _, def := range defs {
		if slices.Contains(seen, def) {
			continue
		}

		seen = append(seen, def)

		if def.rerunsInit() {
			inits = append(inits, def.Step(clientID))
		} else {
			removals = append(removals, def.Step(clientID))
		}
	}

	return slices.Concat(removals, inits)
}

// rerunsInit reports whether the step of the definition is a run of
// init. Only the managed entry in the background gets that step, because
// init removes the background key from the entry that it owns.
func (d ReportOnlyDefinition) rerunsInit() bool {
	return d.Managed && d.Background != ""
}

// Step returns the step that makes the pre-write hook block again. It
// removes the definition, because a definition that keeps the hook
// beside a managed handler meets the refusal of a second handler. The
// managed entry is the exception: a run of init removes its background
// key. The command passes through describeCommand, so no secret and no
// control character reaches the terminal.
func (d ReportOnlyDefinition) Step(clientID string) string {
	const hook = "the lesson-check pre-write hook"

	switch {
	case d.rerunsInit():
		return fmt.Sprintf("%s: run `seamark init --client %s`, which removes %#q from %s", d.Path, clientID, d.Background, hook)
	case d.Background != "":
		return fmt.Sprintf("%s: remove the definition %#q, which has `%s: true`, so the client runs %s in the background",
			d.Path, describeCommand(d.Command), d.Background, hook)
	default:
		return fmt.Sprintf("%s: remove the definition %#q, which discards the exit status of %s",
			d.Path, describeCommand(d.Command), hook)
	}
}

// refuseSecondLessonHandler applies the rule of one handler per lesson
// check to definitions that setup does not manage. A definition that
// certainly runs the hook refuses the plan, before any write. A
// definition that only can run the hook does not prove a second run, so
// the caller warns about it and keeps the managed handler. The two
// adapters share the rule, so one state gets one answer on both clients.
// Claude Code runs a local copy with the managed handler key and equal
// fields once, so the Claude adapter takes such a copy out before it
// calls this rule. Codex runs every copy. requested is true when the run
// names a lesson-check mode.
func refuseSecondLessonHandler(clientID string, spec hooks.Spec, sources []hookSource, requested bool) error {
	for _, source := range sources {
		if source.certain {
			return duplicateLessonHandler(clientID, spec, source, requested)
		}
	}

	return nil
}

// duplicateLessonHandler returns the refusal for a second lesson handler
// in a source that setup does not manage. Setup refuses because two
// handlers would run the same check twice, or, for a Claude Code copy
// with other fields, because setup cannot tell which fields apply. A run
// that requests a lesson change asks setup to manage the handler, so the
// advice is to remove the other definition. A run that requests none
// keeps a handler that setup already manages, so the advice says that
// setup owns the handler.
func duplicateLessonHandler(clientID string, spec hooks.Spec, source hookSource, requested bool) error {
	where := fmt.Sprintf("lesson checks: %s %s %s already runs a handler from %s: %#q",
		clientID, spec.Event, strings.Join(source.tools, ", "), source.path, describeCommand(source.command))
	if requested {
		return fmt.Errorf("%s; setup refuses a second handler; remove that definition to let setup manage lesson checks", where)
	}

	return fmt.Errorf("%s; setup already manages this handler, so remove that definition", where)
}

// lessonConfig is the result of lessoncheck.Load for one plan. errs
// holds a read or decode error too, so the refusal and the summary read
// one list.
type lessonConfig struct {
	cfg  *lessoncheck.Config
	errs []lessoncheck.ConfigError
}

// configRefusal is the refusal of an enforce request for a
// configuration that does not validate, or nil. The message names the
// first error, which is the read error when the file does not decode.
func configRefusal(config lessonConfig) error {
	if len(config.errs) == 0 {
		return nil
	}

	return fmt.Errorf("lesson checks: .seamark/lessons.yaml does not validate: %s; fix it before enabling enforce",
		config.errs[0].Message)
}

// lessonChange is what the lesson-check part of a plan does to one hook
// document. The narrator reads it after the write, so the lines
// describe exactly what the plan composed.
type lessonChange struct {
	clientID string
	// requested is true when the run named a lesson-check or exploration
	// intent. Only then does the narration print the lesson checks line.
	requested bool
	// specs lists the hooks that the plan merged, for the command lines.
	specs []hooks.Spec
	// commands are the merged commands with their retained matchers.
	commands []lessonHookCommand
	// checksChanged and explorationChanged are true when a merge changed
	// the document.
	checksChanged, explorationChanged bool
	// checksRemoved and explorationRemoved count the hooks that the plan
	// removed.
	checksRemoved, explorationRemoved int
	// removed lists the background keys that the merge removed from the
	// owned pre-write entry. The adapter reports each one.
	removed []hooks.BackgroundRemoval
	// mode is the installed lesson-check mode after the plan.
	mode string
	// summary names the paths that are not active after the plan.
	summary string
}

type lessonHookCommand struct {
	event, matcher, command string
}

// changed reports whether the plan changes the document.
func (c lessonChange) changed() bool {
	return c.checksChanged || c.explorationChanged || c.checksRemoved > 0 || c.explorationRemoved > 0
}

// detail says what the plan does to the lesson-check entries, in the
// words of a file line.
func (c lessonChange) detail() string {
	var parts []string

	if c.checksChanged {
		parts = append(parts, "lesson-check hooks")
	}

	if c.explorationChanged {
		parts = append(parts, "exploration hook")
	}

	if c.checksRemoved > 0 {
		parts = append(parts, fmt.Sprintf("removed %d lesson-check hooks", c.checksRemoved))
	}

	if c.explorationRemoved > 0 {
		parts = append(parts, "removed the exploration hook")
	}

	return strings.Join(parts, "; ")
}

// narrateCommands prints the command of each merged hook, in the form
// of the other hook lines. So the user sees what runs on which tool
// without opening the document. The adapter's narrator prints the file
// line, and its detail names this change.
func (c lessonChange) narrateCommands(w io.Writer) {
	for _, command := range c.commands {
		fmt.Fprintf(w, "          %-30s %s\n", render.Sanitize(command.event+" "+command.matcher), render.Sanitize(command.command))
	}
}

// narrateLine prints the lesson checks line of the client, only when
// the run named a lesson-check or exploration intent.
func (c lessonChange) narrateLine(w io.Writer) {
	if c.requested {
		fmt.Fprintf(w, "  lesson checks  %s %s — %s\n", c.clientID, c.mode, c.summary)
	}
}

// planLessonChecks applies the lesson-check intent of one client to its
// parsed hook document and writes nothing. The document must be the
// one that holds the lesson hook of the client, and path names it.
//
// An enforce request first checks the preconditions: the configuration
// must validate, and no known-bad state may keep the guard inactive.
// Either failure returns an error before the plan has a write.
//
// The merge of the resolved mode comes first. The pre-write hook lists
// the other mode's marker as legacy, so the merge rewrites an entry of
// the other mode in place. Then RemoveOwned removes a remaining entry of
// the other mode's marker under another event. Off removes every
// lesson-check marker of the client, and no other entry.
func planLessonChecks(document map[string]any, path, root, binary string, client lessonHookClient, req ClientSetup,
	state lessonSourceState,
) (lessonChange, error) {
	change := lessonChange{clientID: client.id, requested: req.LessonChecks != "" || req.LessonExploration != ""}
	all := client.specs(hooks.LessonChecksEnforce)

	var config lessonConfig

	if change.requested {
		var err error

		config.cfg, config.errs, err = lessoncheck.Load(root)
		if err != nil {
			// Load gives no semantic error beside a read or decode error. The
			// capability report reads such an error as a decode-error entry.
			config.errs = []lessoncheck.ConfigError{{
				Code: lessoncheck.CodeDecodeError, Subject: "lessons.yaml", Message: err.Error(),
			}}
		}
	}

	if req.LessonChecks == hooks.LessonChecksEnforce {
		if err := configRefusal(config); err != nil {
			return lessonChange{}, err
		}

		if err := enforceRefusal(client.id, state); err != nil {
			return lessonChange{}, err
		}
	}

	switch mode := resolveLessonChecks(document, all, req.LessonChecks); mode {
	case hooks.LessonChecksCheck, hooks.LessonChecksEnforce:
		specs := client.specs(mode)

		merged, err := hooks.Merge(document, binary, specs)
		if err != nil {
			return lessonChange{}, fmt.Errorf("%s: %w", path, err)
		}

		removed := hooks.RemoveOwned(document, specs[0].Legacy, binary)
		change.specs = append(change.specs, specs...)
		change.checksChanged = merged.Changed || removed > 0
		change.removed = merged.Removed
	case hooks.LessonChecksOff:
		change.checksRemoved = hooks.RemoveOwned(document, hooks.LessonCheckMarkers(all), binary)
	}

	if client.exploration != nil {
		if err := planExploration(&change, document, binary, *client.exploration, req.LessonExploration); err != nil {
			return lessonChange{}, fmt.Errorf("%s: %w", path, err)
		}
	}

	change.mode = hooks.InstalledLessonChecks(document, all)
	change.summary = lessonSummary(document, binary, client, change.mode, config, state)
	hookMap, _ := document["hooks"].(map[string]any)

	for _, spec := range change.specs {
		entries, _ := hookMap[spec.Event].([]any)

		hooks.ForEachCommand(entries, func(matcher string, _ map[string]any, command string) {
			if hooks.OwnedBySeamark(command, spec.Markers()) {
				change.commands = append(change.commands, lessonHookCommand{spec.Event, matcher, command})
			}
		})
	}

	return change, nil
}

// resolveLessonChecks returns the mode the plan installs: the request,
// else the installed mode, which the plan merges again so the commands
// name the current binary. An installed mode of off gives "", so a run
// without the flag adds and removes nothing. A partial install keeps
// the mode of its pre-write hook, and the merge restores the missing
// post-edit hook. A partial install without a pre-write hook gives "":
// its mode is unknown, so the plan leaves it as it is.
func resolveLessonChecks(document map[string]any, all []hooks.Spec, requested string) string {
	if requested != "" {
		return requested
	}

	switch installed := hooks.InstalledLessonChecks(document, all); installed {
	case hooks.LessonChecksCheck, hooks.LessonChecksEnforce:
		return installed
	case hooks.LessonChecksPartial:
		// The pre-write hook alone reads as its own mode.
		if pre := hooks.InstalledLessonChecks(document, all[:1]); pre != hooks.LessonChecksOff {
			return pre
		}
	}

	return ""
}

// planExploration applies the exploration intent. On merges the hook,
// off removes it, and an empty intent merges it again only when it is
// installed, so the command names the current binary.
func planExploration(change *lessonChange, document map[string]any, binary string, spec hooks.Spec, requested string) error {
	switch {
	case requested == hooks.LessonExplorationOff:
		change.explorationRemoved = hooks.RemoveOwned(document, spec.Markers(), binary)

		return nil
	case requested == hooks.LessonExplorationOn, requested == "" && hooks.Owned(document, spec):
	default:
		return nil
	}

	merged, err := hooks.Merge(document, binary, []hooks.Spec{spec})
	if err != nil {
		return err
	}

	change.specs = append(change.specs, spec)
	change.explorationChanged = merged.Changed

	return nil
}

// lessonPath is one hook path of the narration line.
type lessonPath struct {
	stage   lessoncheck.HookStage
	tool    string
	context lessoncheck.PathContext
	state   lessoncheck.PathState
	reasons []lessoncheck.CapReason
	limit   string // the limit of a verified claim; "" for none
}

// String renders the path as "<hook_stage> <tool> <state> (<reasons>)",
// then the limit in its own parentheses. A path without a reason and a
// limit has no parentheses.
func (p lessonPath) String() string {
	text := fmt.Sprintf("%s %s %s", p.stage, p.tool, p.state)

	if p.context != "" {
		text = fmt.Sprintf("%s %s %s %s", p.stage, p.tool, p.context, p.state)
	}

	if len(p.reasons) > 0 {
		text += " (" + lessoncheck.JoinCapReasons(p.reasons) + ")"
	}

	if p.limit != "" {
		text += " (" + lessoncheck.LimitText([]string{p.limit}) + ")"
	}

	return text
}

// lessonSummary names each hook path that is not active after the plan,
// and each active path with a limit, joined with "; ". A limit narrows
// the claim of an active path, so the line must not hide it. It is the setup-time reading of the facts that the
// plan holds: the installed entries, the configuration, the hook
// sources, and the static native evidence. Equal root and child paths
// share one line. Unequal paths name their context, because one context
// cannot establish the other's coverage. Off collapses its
// not-installed paths into one phrase, so the line does not repeat
// "inactive (not-installed)" for every tool.
func lessonSummary(document map[string]any, binary string, client lessonHookClient, mode string, config lessonConfig,
	state lessonSourceState,
) string {
	r := pathReader{
		client: client, self: BinaryIdentity{Path: binary}, trust: state.trust,
		binaries: map[string]binaryReading{},
		lessons:  state.inspection(document, client),
	}

	report := lessoncheck.CapabilityReportOf(config.cfg, config.errs, r.paths(), nil)
	capability, _ := report.Client(client.id)

	var items []string
	if mode == hooks.LessonChecksOff {
		items = append(items, "no pre-write or post-edit hook installed")
	}

	explorationOwned := client.exploration != nil && hooks.Owned(document, *client.exploration)

	for _, path := range capability.Paths {
		switch {
		case path.State == lessoncheck.PathActive && path.Verification.Limit == "":
			continue
		case path.HookStage == lessoncheck.StageExploration:
			// The exploration hook is optional, so the line names its paths
			// only when the hook is installed.
			if !explorationOwned {
				continue
			}
		case mode == hooks.LessonChecksOff:
			// The phrase before the loop already names these paths.
			continue
		}

		reading := lessonPath{
			stage: path.HookStage, tool: path.Tool, state: path.State, reasons: path.Reasons, limit: path.Verification.Limit,
		}
		if slices.ContainsFunc(capability.Paths, func(other lessoncheck.PathCapability) bool {
			return other.HookStage == path.HookStage && other.Tool == path.Tool &&
				(other.State != path.State || !slices.Equal(other.Reasons, path.Reasons) ||
					other.Verification.Limit != path.Verification.Limit)
		}) {
			reading.context = path.Context
		}

		item := reading.String()
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}

	if len(items) == 0 {
		return "every path active"
	}

	return strings.Join(items, "; ")
}

// discardsPreWriteStatus reports whether one definition that setup does
// not own runs the pre-write hook of the spec and discards its exit
// status. A deny blocks only by exit status 2, so such a definition
// never blocks. An owned command is one shell word and a marker, so
// only another definition can discard the status. The reading answers
// for every marker of the spec. Each marker of a pre-write spec is a
// guard or a baseline marker, so no other marker can change the answer.
func discardsPreWriteStatus(spec hooks.Spec, cmd string) bool {
	if hooks.OwnedBySeamark(cmd, spec.Markers()) {
		return false
	}

	reading := hooks.ReadHook(cmd, spec.Markers())

	return reading.Use() != hooks.HookNotRun && !reading.StatusPasses()
}

// runsPreWrite reports whether a command runs the pre-write hook of the
// spec: an owned command, or another one that can run it.
func runsPreWrite(spec hooks.Spec, cmd string) bool {
	return hooks.OwnedBySeamark(cmd, spec.Markers()) || hooks.ReadHook(cmd, spec.Markers()).Use() != hooks.HookNotRun
}

// reportOnlyIn returns each definition of the client's pre-write hook
// in a parsed hook document that discards its exit status, or that the
// client runs in the background, in document order. Only a "command"
// hook under a matcher that fires runs, so only such a definition
// counts. managed is true for the document that setup manages, and an
// owned command there is a managed record. merging is true for a plan
// that merges the pre-write hook. That plan removes the background key
// from the owned entry of the managed document, so the key does not
// count there.
func reportOnlyIn(document map[string]any, client lessonHookClient, path string, managed, merging bool,
) []ReportOnlyDefinition {
	spec, fires := client.preWrite(), client.fires
	hookMap, _ := document["hooks"].(map[string]any)
	eventHooks, _ := hookMap[spec.Event].([]any)

	var defs []ReportOnlyDefinition

	hooks.ForEachCommand(eventHooks, func(matcher string, h map[string]any, cmd string) {
		if hookType, _ := h["type"].(string); hookType != "command" || !hooks.Fires(spec, matcher, fires) {
			return
		}

		owned := managed && hooks.OwnedBySeamark(cmd, spec.Markers())
		if def, ok := reportOnlyDefinition(spec, path, cmd, client.exec(h).Background, owned); ok &&
			(!owned || !merging || def.Background == "") {
			defs = append(defs, def)
		}
	})

	return defs
}

// reportOnlyInline is reportOnlyIn for the inline [hooks] of the Codex
// configuration. An entry in a layout that the reader does not know has
// no matcher or type to check, so it counts as one that runs. Setup
// never manages an inline entry, so an owned command there counts too,
// and its step is a removal.
func reportOnlyInline(entries []approve.InlineHook, client lessonHookClient) []ReportOnlyDefinition {
	spec, fires := client.preWrite(), client.fires

	var defs []ReportOnlyDefinition

	for _, entry := range entries {
		if entry.Event != spec.Event || (entry.Known && (entry.Type != "command" || !hooks.Fires(spec, entry.Matcher, fires))) {
			continue
		}

		background := ""
		if entry.Async {
			background = "async"
		}

		if def, ok := reportOnlyDefinition(spec, codexInlineHooksPath, entry.Command, background, false); ok {
			defs = append(defs, def)
		}
	}

	return defs
}

// reportOnlyDefinition reads one definition that runs under a firing
// matcher. It returns the record and true when no deny of the
// definition blocks. The discard cause wins over the background cause,
// because removing the key alone leaves the status discarded.
func reportOnlyDefinition(spec hooks.Spec, path, cmd, background string, managed bool) (ReportOnlyDefinition, bool) {
	def := ReportOnlyDefinition{Path: path, Command: cmd, Managed: managed}

	switch {
	case discardsPreWriteStatus(spec, cmd):
		return def, true
	case background != "" && runsPreWrite(spec, cmd):
		def.Background = background

		return def, true
	default:
		return ReportOnlyDefinition{}, false
	}
}
