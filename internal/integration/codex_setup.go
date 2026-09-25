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
// The lesson hooks live in .codex/hooks.json, a document of their own.
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
		if err := planCodexHooks(&plan, root, binary, config); err != nil {
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

// planCodexHooks merges the lesson hook into .codex/hooks.json. It
// keeps every other hook in the file. It never grants trust: Codex runs
// a project hook only after the user reviews that hook in Codex.
//
// One handler per hook is the rule. Codex runs every matching handler
// of every source, and Codex delivery never suppresses a repeat. A
// second handler therefore doubles the advice, the log records, and the
// budget. When a command that setup does not own already runs the hook,
// setup installs no handler beside it and says where the hook runs.
func planCodexHooks(plan *ClientPlan, root, binary string, config *codexConfigRead) error {
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

	inline := codexInlineCommands(plan, root, config)

	var managed []hooks.Spec

	for _, spec := range hooks.CodexSpecs() {
		if codexHookRunsElsewhere(plan, document, inline, spec) {
			continue
		}

		managed = append(managed, spec)
	}

	changed, err := hooks.Merge(document, binary, managed)
	if err != nil {
		return fmt.Errorf("%s: %w", codexHooksFile, err)
	}

	narrate := func(w io.Writer, status OpStatus) {
		narrateCodexHooks(w, binary, managed, codexHooksChange{changed: changed, created: !guard.Exists}, status == OpPlanned)
	}

	// The adapter reads no receiver from a Codex event yet. The event
	// names one, but the reset of a subagent is unverified. The
	// once-per-context mode therefore has no effect there. Say so once,
	// at setup.
	plan.Findings = append(plan.Findings, Finding{
		Level: FindingInfo,
		Path:  codexHooksFile,
		Reason: "the Codex adapter reads no receiving context yet (the reset of a subagent is unverified), " +
			"so `hook_delivery: once-per-context` does not apply: reminders repeat, within the hook budget",
	})

	if !changed {
		detail := "seamark hooks already wired"
		if len(managed) == 0 {
			detail = "no hook installed: the seamark hook runs from a definition that setup does not manage"
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
		Detail: "lessons hook", Narrate: narrate,
	})

	// Codex records trust against the hash of a hook, so a new or changed
	// hook is skipped until the user reviews it.
	plan.Findings = append(plan.Findings, Finding{
		Level:  FindingInfo,
		Path:   codexHooksFile,
		Reason: "Codex runs a project hook only after the user reviews and trusts it; a changed hook needs a new review; setup never grants trust",
		Action: "open the project in Codex and review the hooks with /hooks",
	})

	return nil
}

// codexHookRunsElsewhere reports whether setup must leave the hook of
// the spec alone, and it records why. The unmanaged definitions are a
// wrapped command in hooks.json and a seamark hook command inline in
// config.toml: setup edits neither.
//
// Only a definition that certainly runs the hook stops the install. A
// false "runs" leaves the user without any hook, and a false "does not
// run" gives one extra reminder. A command that only can run the hook,
// such as an unknown program with the seamark command as its arguments,
// therefore gets a warning, and setup installs the managed handler.
//
// A managed handler that already exists stays managed. Setup keeps it
// current, because a removed handler is a bigger change than the user
// asked for, and it warns that the hook runs twice.
func codexHookRunsElsewhere(plan *ClientPlan, document map[string]any, inline []string, spec hooks.Spec) bool {
	type source struct {
		path, command string
		certain       bool
	}

	var elsewhere []source

	for _, wrapped := range hooks.Wrapped(document, spec) {
		elsewhere = append(elsewhere, source{codexHooksFile, wrapped.Command, wrapped.Certain})
	}

	for _, command := range inline {
		if use := hooks.SeamarkHookUse(command, spec.Markers()); use != hooks.HookNotRun {
			elsewhere = append(elsewhere, source{approve.CodexConfig + " [hooks]", command, use == hooks.HookRuns})
		}
	}

	owned := hooks.Owned(document, spec)
	omit := false

	for _, s := range elsewhere {
		command := render.Sanitize(s.command)

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

// narrateCodexHooks prints the hooks.json lines in the form of the
// Claude Code lines: the file line, then each managed hook command. What
// runs on which tool must never require opening the file to find out.
func narrateCodexHooks(w io.Writer, binary string, specs []hooks.Spec, change codexHooksChange, preview bool) {
	switch {
	case len(specs) == 0:
		fmt.Fprintf(w, "  kept    %s (no hook installed: the seamark hook runs from a definition that setup does not manage)\n", codexHooksFile)
	case !change.changed:
		fmt.Fprintf(w, "  kept    %s (seamark hooks already wired)\n", codexHooksFile)
	case change.created:
		fmt.Fprintf(w, "  %s %s (lessons hook)\n", verb("wrote  ", "would write", preview), codexHooksFile)
	default:
		fmt.Fprintf(w, "  %s %s (lessons hook)\n", verb("updated", "would update", preview), codexHooksFile)
	}

	for _, spec := range specs {
		where := spec.Event
		if spec.Matcher != "" {
			where += " " + spec.Matcher
		}

		fmt.Fprintf(w, "          %-30s %s\n", where, spec.Command(binary))
	}
}

// codexHooksChange says what the plan does to hooks.json.
type codexHooksChange struct {
	changed bool // the plan writes the file
	created bool // the file does not exist yet
}

// codexInlineCommands returns the hook commands of the second project
// hook source, inline [hooks] in .codex/config.toml. Codex merges both
// sources. The registration plan can already hold the file. Otherwise
// the file is an input here, so the read may follow a link. A file that
// the function cannot read gives a warning and does not stop setup.
func codexInlineCommands(plan *ClientPlan, root string, config *codexConfigRead) []string {
	skipped := func(reason string) []string {
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

	commands, present, err := approve.CodexInlineHooks(config.data)
	if err != nil {
		return skipped(err.Error())
	}

	if present && !slices.ContainsFunc(commands, func(command string) bool {
		return slices.ContainsFunc(hooks.CodexSpecs(), func(spec hooks.Spec) bool {
			return hooks.SeamarkHookUse(command, spec.Markers()) != hooks.HookNotRun
		})
	}) {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingInfo,
			Path:   approve.CodexConfig,
			Reason: "inline [hooks] are present; Codex merges them with " + codexHooksFile + " and warns at startup",
		})
	}

	return commands
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

// Inspect reports the registration and the grants, offline. Both are
// pending verification: the native check for the generated file is
// defined and has not run against this version of the setup.
func (codexSetup) Inspect(root string) Inspection {
	evidence := VerificationEvidence{Level: VerificationPending, Surface: "project " + approve.CodexConfig}
	record := approve.InspectCodex(root)

	registration := CapabilityInspection{
		Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence,
	}

	switch {
	case record.Err != "":
		registration.State, registration.Detail = StateUnreadable, render.Sanitize(record.Err)
	case record.Registered != "":
		registration.State = StateCurrent
		registration.Detail = fmt.Sprintf("registered as %q", render.Sanitize(record.Registered))
	case len(record.Conflicts) > 0:
		// Without a registration every conflict is about the registration:
		// the name is taken, or the layout cannot take the table.
		registration.State = StateConflict
		registration.Detail = render.Sanitize(strings.Join(record.Conflicts, "; "))
	default:
		registration.State = StateAbsent
	}

	return Inspection{
		ClientID:     CodexID,
		Capabilities: []CapabilityInspection{registration, inspectGrants(record, evidence)},
	}
}
