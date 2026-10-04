package integration

import (
	"fmt"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Inspect runs every client's offline inspection and completes each
// view with what only the registry knows: the display name, the
// declared capabilities, and the state of the shared skill
// destinations. The result follows registry order and covers exactly
// ConfiguredCapabilities per client, so init, doctor, and status read
// one account of support and setup. Nothing is written, no client
// binary runs, and no login starts.
//
// A shared skill directory is inspected once and reported to every
// consumer with the same words, so no client can claim a state that
// another contradicts.
func (r *Registry) Inspect(root string) []Inspection {
	dests := SkillDestinations(r.ordered)
	states := skills.InspectTargets(root, SkillTargets(dests))

	byDir := make(map[string]skills.ClientState, len(dests))
	for i, d := range dests {
		byDir[d.Dir] = states[i]
	}

	out := make([]Inspection, 0, len(r.ordered))

	for _, c := range r.ordered {
		insp := Inspection{ClientID: c.ID}

		if c.Setup != nil {
			insp = c.Setup.Inspect(root)
		}

		insp.ClientID, insp.Name, insp.Declared, insp.Setup = c.ID, c.Name, c.Declared(), c.SetupOps
		insp.Capabilities = completeCapabilities(c, insp.Capabilities, dests, byDir)

		out = append(out, insp)
	}

	return out
}

// InspectSkills reports the skill directories of the registry's
// clients, one record per destination, labelled by every consumer. It
// is the list init, doctor, and status print, and the source of each
// client's skills entry in Inspect, so the two views cannot disagree.
func (r *Registry) InspectSkills(root string) []skills.ClientState {
	return skills.InspectTargets(root, SkillTargets(SkillDestinations(r.ordered)))
}

// completeCapabilities returns the entries in ConfiguredCapabilities
// order. The adapter's entry is kept where it exists; a missing entry
// is synthesized as absent with the descriptor's support. The skills
// entry always comes from the destination states, because the
// directory belongs to the registry, not to one adapter; an adapter
// entry for it contributes only its trust and verification.
func completeCapabilities(c Client, reported []CapabilityInspection, dests []SkillDestination, byDir map[string]skills.ClientState) []CapabilityInspection {
	out := make([]CapabilityInspection, 0, len(ConfiguredCapabilities))

	for _, capability := range ConfiguredCapabilities {
		entry := CapabilityInspection{Capability: capability, Supported: c.Supports(capability)}

		if i := slices.IndexFunc(reported, func(e CapabilityInspection) bool { return e.Capability == capability }); i >= 0 {
			entry = reported[i]
		}

		if capability == CapabilitySkills {
			entry = skillsEntry(c, entry, dests, byDir)
		}

		out = append(out, entry)
	}

	return out
}

// skillsEntry classifies the client's skill directories from the
// destination states. The worst directory decides the state: an
// unreadable directory first, then a managed copy that a refresh
// repairs, then a directory under a shipped name that seamark does not
// own, which init never replaces. The detail names each directory with
// its state and, for a shared directory, the other consumers.
func skillsEntry(c Client, evidence CapabilityInspection, dests []SkillDestination, byDir map[string]skills.ClientState) CapabilityInspection {
	entry := CapabilityInspection{
		Capability: CapabilitySkills, Supported: len(c.SkillDirs) > 0,
		Trust: evidence.Trust, Verification: evidence.Verification,
	}

	var details []string

	for _, dir := range c.SkillDirs {
		state, ok := byDir[dir]
		if !ok {
			continue
		}

		// Describe labels the record with its client; the directory is
		// the label the client's own entry needs.
		labelled := state
		labelled.Client = dir
		detail := labelled.Describe()

		if i := slices.IndexFunc(dests, func(d SkillDestination) bool { return d.Dir == dir }); i >= 0 {
			if others := slices.DeleteFunc(slices.Clone(dests[i].Consumers), func(id string) bool { return id == c.ID }); len(others) > 0 {
				detail += " (shared with " + strings.Join(others, ", ") + ")"
			}
		}

		details = append(details, detail)
		entry.State = worseSkillState(entry.State, skillState(state))
	}

	entry.Detail = strings.Join(details, " · ")
	entry.Action = skillsAction(entry.State)

	return entry
}

// skillState maps one directory record to a capability state.
func skillState(s skills.ClientState) CapabilityState {
	switch {
	case s.Err != "":
		return StateUnreadable
	case s.NeedsRefresh():
		return StatePartial
	case s.Foreign > 0:
		return StateConflict
	case s.Installed():
		return StateCurrent
	default:
		return StateAbsent
	}
}

// skillStateRank orders skill states from nothing to worst, so the
// worst directory of a client decides its entry.
var skillStateRank = map[CapabilityState]int{
	StateAbsent: 0, StateCurrent: 1, StateConflict: 2, StatePartial: 3, StateUnreadable: 4,
}

// worseSkillState returns the worse of two skill states.
func worseSkillState(a, b CapabilityState) CapabilityState {
	if skillStateRank[b] > skillStateRank[a] {
		return b
	}

	return a
}

// skillsAction is the corrective command for a skills state. A foreign
// directory outranks "not installed" in wording: `seamark init --skills`
// never replaces it, so the action must say what to do first.
func skillsAction(state CapabilityState) string {
	switch state {
	case StateUnreadable:
		return "make the skill directory readable, then re-run `seamark init --skills`"
	case StatePartial:
		return "re-run `seamark init --skills` to refresh the managed skills"
	case StateConflict:
		return "a directory under a seamark skill name is not seamark's; rename or remove it, " +
			"then run `seamark init --skills` to install the shipped skill"
	case StateAbsent:
		return "run `seamark init --skills` to add the seamark agent skills"
	default:
		return ""
	}
}

// inspectGrants converts an approval record into the tool-grants
// inspection. Claude Code and Codex share the record, so they share
// this conversion, and both clients classify a grant state the same way.
// The detail drops the record's client label: the consumer adds it.
func inspectGrants(record approve.ClientApproval, evidence VerificationEvidence) CapabilityInspection {
	entry := CapabilityInspection{
		Capability:   CapabilityToolGrants,
		Supported:    true,
		Verification: evidence,
		Detail:       render.Sanitize(strings.TrimPrefix(record.Describe(), record.Client+" ")),
	}

	switch record.State() {
	case approve.StateUnreadable:
		entry.State = StateUnreadable
		entry.Action = "fix the file, then re-run `seamark init --approve-tools`"
	case approve.StateConflicting:
		entry.State = StateConflict
		entry.Action = "seamark leaves explicit settings alone; edit the file by hand if the seamark tools should be approved"
	case approve.StatePartial:
		entry.State = StatePartial
		entry.Action = "re-run `seamark init --approve-tools` to add the missing entries"
	case approve.StateCurrent:
		entry.State = StateCurrent
	default:
		entry.State = StateAbsent
		entry.Action = "run `seamark init --approve-tools` so the seamark tools run without prompts; " +
			"project configuration only — user or managed policy can still prompt"
	}

	return entry
}

// registrationAction is the corrective command for an MCP registration
// state. Registration is part of explicit client setup, so the command
// names the client.
func registrationAction(clientID string, state CapabilityState) string {
	switch state {
	case StateUnreadable:
		return "fix the file, then re-run `seamark init --client " + clientID + "`"
	case StateConflict:
		return "seamark never replaces a foreign registration; edit the file by hand"
	case StateAbsent:
		return "run `seamark init --client " + clientID + "` to register the seamark MCP server"
	default:
		return ""
	}
}

// hookSource is one definition of a seamark hook in one of the client's
// sources, with the evidence classification needs. The evidence is the
// tools the client runs it for and whether the shell certainly runs the
// hook. It is also whether setup manages the definition, and for a gate
// hook its mode. Classification and narration follow from these fields;
// no consumer reads them back from prose.
type hookSource struct {
	// path is the document that holds the definition, for narration.
	path    string
	command string
	// tools lists the spec's tools the client runs the definition for:
	// a "command"-typed entry under a matcher that fires. Empty when the
	// client never runs it for the spec's tools.
	tools []string
	// certain is true when hooks.SeamarkHookUse answers HookRuns: the
	// shell executes the seamark hook each time. It is false for
	// HookMayRun, for example when another program gets the seamark
	// command as arguments.
	certain bool
	// managed is true for the definition setup installs and updates.
	managed bool
	// mode is the gate mode the definition bakes in; "" for other hooks.
	mode string
}

// describeCommand renders a hook command for a detail or a finding. A
// wrapper can carry a credential ("hook-runner --token=… seamark gate
// --hook"), and the text reaches terminals, status JSON, and the MCP
// status resource, so secret-shaped values are scrubbed before control
// sequences are stripped.
func describeCommand(command string) string {
	return render.Sanitize(redact.Secrets(command))
}

// firingTools lists the spec's tools an entry runs for under the
// client's matcher rule. An event without a matcher fires for every
// entry, which the empty tool stands for.
func firingTools(spec hooks.Spec, matcher string, fires hooks.MatcherRule) []string {
	var tools []string

	for _, tool := range spec.Tools() {
		if tool == "" || fires(matcher, tool) {
			tools = append(tools, tool)
		}
	}

	return tools
}

// markerMode reads the gate mode of a command from every marker of the
// spec that the command runs, in the given way (owned, or wrapped).
// Enforce wins: a wrapper that runs the warn command and then the
// enforce command blocks, whatever it ran first. A marker of another
// hook gives "".
func markerMode(spec hooks.Spec, runs func(marker string) bool) string {
	mode := ""

	for _, marker := range spec.Markers() {
		if !runs(marker) {
			continue
		}

		switch hooks.GateMarkerMode(marker) {
		case hooks.ModeEnforce:
			return hooks.ModeEnforce
		case hooks.ModeWarn:
			mode = hooks.ModeWarn
		}
	}

	return mode
}

// wrappedGateMode reads the gate mode of a command that setup does not
// own, from one reading of the command for the spec's markers. asked
// is the mode of the gate markers that the command runs, and mode is
// what the command does with them. A gate verdict blocks only by exit
// status 2, so a marker counts only when the shell can pass that status
// to the client. When no run of the gate can pass it, the hook still
// runs and reports, and mode is GateModeReportOnly. A hook other than
// the gate gives "" for both.
func wrappedGateMode(spec hooks.Spec, reading hooks.HookReading) (mode, asked string) {
	asked = markerMode(spec, func(marker string) bool { return reading.UseFor(marker) != hooks.HookNotRun })
	mode = markerMode(spec, reading.StatusPassesFor)

	if mode == "" && asked != "" {
		mode = GateModeReportOnly
	}

	return mode, asked
}

// discardsStatusFinding names a definition that runs the gate and
// discards its exit status, for example with "|| true", "&", or "| cat".
// mode and asked come from wrappedGateMode. The finding is a warning
// whatever the definition asked for. With --enforce, the user asked for
// blocking and gets none. Without it, an enforcing policy file blocks
// nothing either, and doctor prints warnings only, so an information
// finding would leave the user to believe that the policy is active.
// Setup never edits the definition, so the action says what to change.
func discardsStatusFinding(path, command, mode, asked string) Finding {
	finding := Finding{
		Level: FindingWarning,
		Path:  path,
		Reason: fmt.Sprintf("has `%s`, which discards the exit status of the seamark gate hook, "+
			"so no verdict blocks, whatever --enforce or .seamark/policy.yaml says", describeCommand(command)),
		Action: "run the seamark gate as the last command of that definition, in the foreground, " +
			"so its exit status 2 reaches the client",
	}

	// The command can also run a warn gate whose status passes. That run
	// still blocks under an enforcing policy file.
	if mode == hooks.ModeWarn {
		finding.Reason = fmt.Sprintf("has `%s`, which discards the exit status of the seamark gate hook with --enforce, "+
			"so a verdict blocks only when .seamark/policy.yaml enforces", describeCommand(command))
	}

	return finding
}

// documentSources lists every definition of the spec's hook in one hook
// document. The owned commands are such definitions, and setup manages
// them when the document is the managed one. The wrapped commands are
// such definitions too, and setup never manages them. Each source keeps
// the tools the client runs it for. A definition the client never runs
// for the spec's tools has another type, or a matcher that never fires.
// It is named in an info finding and has no tools, so it counts for
// nothing.
func documentSources(findings *[]Finding, document map[string]any, fires hooks.MatcherRule, path string, spec hooks.Spec, managed bool) []hookSource {
	var sources []hookSource

	hookMap, _ := document["hooks"].(map[string]any)
	eventHooks, _ := hookMap[spec.Event].([]any)

	hooks.ForEachCommand(eventHooks, func(matcher string, h map[string]any, cmd string) {
		source := hookSource{path: path, command: cmd}
		// asked is the gate mode of the markers that the command runs. A
		// wrapper that discards the exit status gets another mode.
		asked := ""

		// An owned command needs no parse: ownership is a text rule. Any
		// other command is read once, and the reading answers every
		// question about it.
		if hooks.OwnedBySeamark(cmd, spec.Markers()) {
			source.certain, source.managed = true, managed
			source.mode = markerMode(spec, func(marker string) bool { return hooks.OwnedBySeamark(cmd, []string{marker}) })
			asked = source.mode
		} else if reading := hooks.ReadHook(cmd, spec.Markers()); reading.Use() != hooks.HookNotRun {
			source.certain = reading.Use() == hooks.HookRuns
			source.mode, asked = wrappedGateMode(spec, reading)
		} else {
			return
		}

		if hookType, _ := h["type"].(string); hookType == "command" {
			source.tools = firingTools(spec, matcher, fires)
		}

		if len(source.tools) == 0 {
			*findings = append(*findings, neverFiresFinding(path, cmd, spec.Event, matcher, spec))

			return
		}

		// A definition that never fires gates nothing, so only a running
		// one gets the finding about its exit status.
		if source.mode != asked {
			*findings = append(*findings, discardsStatusFinding(path, cmd, source.mode, asked))
		}

		sources = append(sources, source)
	})

	return sources
}

// managedSources keeps the sources setup manages.
func managedSources(sources []hookSource) []hookSource {
	var out []hookSource

	for _, s := range sources {
		if s.managed {
			out = append(out, s)
		}
	}

	return out
}

// unmanagedSources keeps the sources setup does not manage.
func unmanagedSources(sources []hookSource) []hookSource {
	var out []hookSource

	for _, s := range sources {
		if !s.managed {
			out = append(out, s)
		}
	}

	return out
}

// coveredTools unions the tools of the sources that pass keep, in the
// spec's order, so a partial report names them reproducibly.
func coveredTools(spec hooks.Spec, sources []hookSource, keep func(hookSource) bool) []string {
	var tools []string

	for _, tool := range spec.Tools() {
		for _, s := range sources {
			if keep(s) && slices.Contains(s.tools, tool) {
				tools = append(tools, tool)

				break
			}
		}
	}

	return tools
}

// hookMode returns the gate mode of the sources that pass keep. It is
// enforce when any of them enforces. It is warn when any of them
// follows the policy file. It is GateModeReportOnly when each of them
// discards its exit status, and "" without such a source. One enforcing
// definition blocks whatever the others say. A warn definition blocks
// when the policy file enforces.
//
// The callers keep the certain sources apart from the uncertain ones. A
// source that only can run the gate must not count as one that does. An
// "echo" with the gate command as its arguments blocks nothing. It must
// not count as nothing either, because a wrapper with the same words
// blocks. So the uncertain sources get a mode of their own, and every
// consumer names it as possible.
func hookMode(sources []hookSource, keep func(hookSource) bool) string {
	mode := ""

	for _, s := range sources {
		if s.mode == "" || !keep(s) {
			continue
		}

		switch s.mode {
		case hooks.ModeEnforce:
			return hooks.ModeEnforce
		case hooks.ModeWarn:
			mode = hooks.ModeWarn
		default:
			if mode == "" {
				mode = s.mode
			}
		}
	}

	return mode
}

// certainSource keeps the sources that certainly run the hook.
func certainSource(s hookSource) bool { return s.certain }

// uncertainSource keeps the sources that only can run the hook.
func uncertainSource(s hookSource) bool { return !s.certain }

// anySource keeps every source.
func anySource(hookSource) bool { return true }

// missingTools lists the spec's tools that no certain source covers.
func missingTools(spec hooks.Spec, sources []hookSource) []string {
	certain := coveredTools(spec, sources, certainSource)

	return slices.DeleteFunc(slices.Clone(spec.Tools()), func(tool string) bool { return slices.Contains(certain, tool) })
}

// classifyHook fills one hook entry from its sources. The tools that
// certain sources cover decide the state. All of the spec's tools make
// the hook current, and some make it partial. The action then names the
// matcher and the document that holds the hook, because setup never
// rewrites the matcher of an existing entry. Without a certain source,
// an uncertain one makes the hook partial, and nothing makes it absent.
// The detail names the mode of a gate hook and the source of a hook
// setup does not manage. The mode is that of the certain sources, or of
// the uncertain ones when only those exist and the detail says "may".
func classifyHook(entry *CapabilityInspection, spec hooks.Spec, sources []hookSource) {
	certain := coveredTools(spec, sources, certainSource)
	possible := coveredTools(spec, sources, uncertainSource)
	name := spec.Name + " hook"

	mode := hookMode(sources, certainSource)
	if mode == "" {
		mode = hookMode(sources, uncertainSource)
	}

	if mode != "" {
		name += " (" + mode + ")"
	}

	managed := slices.ContainsFunc(sources, func(s hookSource) bool { return s.managed && s.certain })

	switch {
	case len(certain) == len(spec.Tools()):
		entry.State = StateCurrent
		entry.Detail = name + " installed"

		if !managed {
			entry.Detail = fmt.Sprintf("%s runs from %s (not managed by setup)", name, sourcePaths(sources, true))
		}
	case len(certain) > 0:
		entry.State = StatePartial
		entry.Detail = fmt.Sprintf("%s runs for %s only, not for %s",
			name, strings.Join(certain, ", "), strings.Join(missingTools(spec, sources), ", "))
		entry.Action = fmt.Sprintf("set the hook's matcher to %q in %s; setup does not change the matcher of an existing hook",
			spec.Matcher, sourcePaths(sources, true))
	case len(possible) > 0:
		first := slices.IndexFunc(sources, func(s hookSource) bool { return !s.certain })
		entry.State = StatePartial
		entry.Detail = fmt.Sprintf("%s may run from %s: `%s`", name, sources[first].path, describeCommand(sources[first].command))
		entry.Action = "run the seamark hook directly in that definition, or remove it and re-run setup, so setup can tell"
	default:
		entry.State = StateAbsent
	}
}

// sourcePaths lists the distinct paths of the sources, certain ones
// only when asked.
func sourcePaths(sources []hookSource, certainOnly bool) string {
	var paths []string

	for _, s := range sources {
		if (certainOnly && !s.certain) || slices.Contains(paths, s.path) {
			continue
		}

		paths = append(paths, s.path)
	}

	return strings.Join(paths, ", ")
}

// sourceFindings names each definition setup does not manage, beside
// the managed one when both run: the client runs every matching
// definition, so a second one delivers each lesson twice and evaluates
// each command twice. An uncertain definition is a warning, because
// setup cannot tell whether the hook runs twice; the reader can. The
// reason never starts with the path: every consumer prefixes it.
func sourceFindings(findings *[]Finding, spec hooks.Spec, sources []hookSource) {
	managed := managedSources(sources)
	managedTools := coveredTools(spec, managed, certainSource)
	managedMode := hookMode(managed, anySource)

	for _, s := range unmanagedSources(sources) {
		command := describeCommand(s.command)
		twice := slices.DeleteFunc(slices.Clone(s.tools), func(tool string) bool { return !slices.Contains(managedTools, tool) })

		switch {
		case !s.certain:
			reason := fmt.Sprintf("has `%s`, which can run the seamark %s hook; setup cannot tell", command, spec.Name)
			if len(twice) > 0 {
				reason += ", and when it does, the hook runs twice"
			}

			*findings = append(*findings, Finding{
				Level:  FindingWarning,
				Path:   s.path,
				Reason: reason,
				Action: "run the seamark hook directly in that definition, or remove it, so setup can tell",
			})
		case len(twice) > 0:
			reason := fmt.Sprintf("also runs `%s` for %s; the managed %s hook runs too, so the hook runs twice",
				command, strings.Join(toolNames(twice), ", "), spec.Name)

			// The other definition can carry another gate mode. Both run,
			// so a local --enforce still blocks under a managed warn hook.
			if s.mode != "" && s.mode != managedMode {
				reason += fmt.Sprintf("; that definition runs in %s mode, and both apply", s.mode)
			}

			*findings = append(*findings, Finding{
				Level:  FindingWarning,
				Path:   s.path,
				Reason: reason,
				Action: "remove one of the two definitions",
			})
		default:
			*findings = append(*findings, Finding{
				Level:  FindingInfo,
				Path:   s.path,
				Reason: fmt.Sprintf("runs the seamark %s hook as `%s`; setup does not manage that definition", spec.Name, command),
			})
		}
	}
}

// toolNames names tools for a finding; the empty tool stands for an
// event without a matcher.
func toolNames(tools []string) []string {
	out := make([]string, 0, len(tools))

	for _, tool := range tools {
		out = append(out, toolName(tool))
	}

	return out
}

// neverFiresFinding names a definition the client never runs for the
// spec's tools. Setup and inspection share it, so both say it alike.
// Path names the document that holds the definition. The reason does
// not repeat it, because every consumer prints the path first.
func neverFiresFinding(path, command, event, matcher string, spec hooks.Spec) Finding {
	return Finding{
		Level: FindingInfo,
		Path:  path,
		Reason: fmt.Sprintf("has `%s` under %s %q, which never runs for %s %s; it is not a handler of that hook",
			describeCommand(command), event, render.Sanitize(matcher), spec.Event, strings.Join(toolNames(spec.Tools()), ", ")),
	}
}

// unreadableHooks marks every hook entry unreadable with one reason: the
// document could not be read or parsed, so nothing about the hooks is
// known, and "unreadable" must not read as "not installed".
func unreadableHooks(entries []*CapabilityInspection, path string, err error) {
	for _, entry := range entries {
		entry.State = StateUnreadable
		entry.Detail = unreadableDetail(err)
		entry.Action = "fix or move " + path + ", then re-run setup"
	}
}

// linkActions gives each unreadable entry of a linked document the
// action that can clear it. documents maps a capability to the owned
// document that holds it. Setup never writes an owned document through
// a symbolic link, so "re-run setup" alone cannot clear that state.
func linkActions(root string, entries []CapabilityInspection, documents map[Capability]string) {
	for i := range entries {
		path, ok := documents[entries[i].Capability]
		if !ok || entries[i].State != StateUnreadable {
			continue
		}

		// A failed check keeps the generic action, which still names the
		// document.
		if link, err := skills.SymlinkIn(root, path); err == nil && link != "" {
			entries[i].Action = fmt.Sprintf("replace the symlink at %s with the real file or directory, then re-run setup; "+
				"setup never writes %s through a link", link, path)
		}
	}
}

// restoreHooks gives each absent hook entry an action that installs the
// missing hook in this workspace. Doctor prints the action for a client
// that runs only some of its hooks. capabilities names the hooks that
// setup installs.
func restoreHooks(entries []CapabilityInspection, action string, capabilities ...Capability) {
	for i := range entries {
		if entries[i].State == StateAbsent && slices.Contains(capabilities, entries[i].Capability) {
			entries[i].Action = action
		}
	}
}

// unreadableDetail renders a read or parse failure the way the skills
// and approval records do: the word, then the sanitized reason.
func unreadableDetail(err error) string {
	return "unreadable (" + render.Sanitize(err.Error()) + ")"
}

// Entry returns the inspection of one capability, and false when the
// view holds none for it.
func (i Inspection) Entry(capability Capability) (CapabilityInspection, bool) {
	for _, entry := range i.Capabilities {
		if entry.Capability == capability {
			return entry, true
		}
	}

	return CapabilityInspection{}, false
}

// Describe renders the entry in a few words for narration: the
// adapter's detail when it has one, else the state in the vocabulary of
// the capability ("not installed", "not registered", "not configured").
// init, doctor, and status all print it, so the three never phrase a
// state differently.
func (c CapabilityInspection) Describe() string {
	if c.Detail != "" {
		return c.Detail
	}

	if !c.Supported {
		return "not supported"
	}

	if c.State != StateAbsent {
		return c.State.String()
	}

	switch c.Capability {
	case CapabilityMCPRegistration:
		return "not registered"
	case CapabilityToolGrants:
		return "not configured"
	default:
		return "not installed"
	}
}

// DescribeHooks renders the lifecycle hooks of the client in the words
// doctor always used: "gate (warn) + lessons hooks installed",
// "lessons hook installed, gate hook missing", "no hooks installed".
// A hook is missing only when Setup says setup installs it; a
// capability the client does not declare is never missing. A partial
// hook carries its detail, because "installed" would overstate it.
func (i Inspection) DescribeHooks() string {
	edits, _ := i.Entry(CapabilityEdits)
	commands, _ := i.Entry(CapabilityCommands)
	resets, _ := i.Entry(CapabilityResets)

	if !edits.Supported && !commands.Supported {
		return "no hook support"
	}

	for _, entry := range []CapabilityInspection{edits, commands} {
		if entry.State == StateUnreadable {
			return "hooks unreadable (" + entry.Detail + ")"
		}
	}

	gate := commands.Supported && commands.State != StateAbsent
	lessons := edits.Supported && edits.State != StateAbsent

	var line string

	switch {
	case !gate && !lessons:
		return "no hooks installed"
	case gate && lessons:
		line = fmt.Sprintf("gate (%s) + lessons hooks installed", i.gateWord(commands))
	case gate:
		line = fmt.Sprintf("gate hook installed (%s)", i.gateWord(commands))

		if i.Setup.Hooks && edits.Supported {
			line += ", lessons hook missing"
		}
	default:
		line = "lessons hook installed"

		if i.Setup.GateHook {
			line += ", gate hook missing"
		}
	}

	var qualifiers []string

	for _, entry := range []CapabilityInspection{commands, edits} {
		if entry.State == StatePartial {
			qualifiers = append(qualifiers, entry.Detail)
		}
	}

	if lessons && i.Setup.ResetHook && resets.State == StateAbsent {
		qualifiers = append(qualifiers, "context reset hook missing")
	}

	if len(qualifiers) > 0 {
		line += "; " + strings.Join(qualifiers, "; ")
	}

	return line
}

// gateWord is the mode word of the gate line: the installed mode, or
// "mode unknown" when a gate hook is present without a readable mode.
func (i Inspection) gateWord(commands CapabilityInspection) string {
	if i.GateMode != "" {
		return i.GateMode
	}

	if commands.State == StatePartial {
		return "mode unknown"
	}

	return hooks.ModeWarn
}

// Describe renders the native evidence for narration: what was checked
// and on which version. An unverified level says so, because a reader
// must not take an installed hook for a checked one; an unknown level
// renders empty.
func (e VerificationEvidence) Describe() string {
	scope := ""
	if e.Surface != "" {
		scope = " (" + e.Surface + ")"
	}

	switch e.Level {
	case VerificationVerified:
		return "natively verified on " + e.ClientVersion + scope
	case VerificationPending:
		return "native check pending" + scope
	case VerificationUnverified:
		return "not natively verified" + scope
	default:
		return ""
	}
}

// DescribeVerification renders the native evidence of the configured
// capabilities, grouped by evidence, so a reader learns in one line
// which installed surfaces a recorded native check covers. Absent and
// unreadable entries are left out: there is nothing to have verified.
func (i Inspection) DescribeVerification() string {
	var (
		order  []string
		groups = map[string][]string{}
	)

	for _, entry := range i.Capabilities {
		if !entry.Supported || entry.State == StateAbsent || entry.State == StateUnreadable {
			continue
		}

		text := entry.Verification.Describe()
		if text == "" {
			continue
		}

		if _, seen := groups[text]; !seen {
			order = append(order, text)
		}

		groups[text] = append(groups[text], string(entry.Capability))
	}

	parts := make([]string, 0, len(order))

	for _, text := range order {
		parts = append(parts, strings.Join(groups[text], ", ")+": "+text)
	}

	return strings.Join(parts, "; ")
}

// DescribeSupport names what the descriptor declares, for help text and
// diagnostics: the setup operations and the lifecycle surfaces, in a
// fixed order. Help, init, doctor, and status derive their support
// claims from it, so they agree.
func (c Client) DescribeSupport() string {
	var parts []string

	if len(c.SkillDirs) > 0 {
		parts = append(parts, "skills")
	}

	if c.SetupOps.Hooks {
		parts = append(parts, "hooks")
	}

	if c.SetupOps.GateHook {
		parts = append(parts, "command gate")
	}

	if c.SetupOps.RegisterMCP {
		parts = append(parts, "MCP registration")
	}

	if c.SetupOps.ApproveTools {
		parts = append(parts, "tool grants")
	}

	if c.Invocation != nil {
		parts = append(parts, "inference")
	}

	if len(parts) == 0 {
		return "nothing yet"
	}

	return strings.Join(parts, ", ")
}
