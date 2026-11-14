package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/scaffold"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Gate hook modes — shared with the status surfaces via internal/hooks,
// which owns the marker spellings and the ownership rule.
const (
	gateModeWarn    = hooks.ModeWarn
	gateModeEnforce = hooks.ModeEnforce
)

func newInitCmd(opts *options) *cobra.Command {
	var (
		printOnly    bool
		gateMode     string
		skillsOpt    skillsFlag
		approveTools bool
		clients      []string
		// lessonChecks and lessonExploration are the two lesson-check
		// flags; empty keeps the installed choice.
		lessonChecks      string
		lessonExploration string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up seamark in this repository (config scaffolds + agent hooks)",
		Long: `Prepares a repository to use seamark:

  - scaffolds .seamark/policy.yaml, .seamark/lessons.yaml and
    .seamark/config.yaml (starters, never overwriting an existing file)
  - adds the .gitignore carve-outs that keep the index local but the
    policy-as-code files in review
  - wires Claude Code hooks into .claude/settings.json: the command gate
    on Bash, the review-lessons reminder on edits, and a PostCompact
    delivery reset — merged into any existing hooks, and safe to re-run
  - with --skills, installs the seamark agent skills (procedures for
    understanding, planning, and reviewing with the seamark tools) into
    .claude/skills and, when an .agents/ directory exists, .agents/skills;
    --skills=claude, --skills=codex, or --skills=all overrides the
    detection. A directory carrying seamark's ownership marker is
    refreshed on re-run; any other directory of the same name is left
    untouched. Without --skills nothing is installed and init prints
    how to install them.

A first init never blocks anything: it installs the gate hook in warn
mode, which reports verdicts and always lets the command through.
Enforcement is an explicit opt-in:

  seamark init --gate-mode enforce

That bakes --enforce into the hook: deny/require_approval verdicts exit 2
(blocking the agent's command), and so do the gate's own failures. The
gate cannot block a command when the client stops the hook at its
timeout (setup writes ` + strconv.Itoa(hooks.GateTimeout) + ` seconds) or cannot start the seamark binary.
A fresh init also scaffolds .seamark/policy.yaml with the matching mode;
an existing policy file is never modified.

Re-running init without --gate-mode keeps whatever mode is installed —
enforcement is never added or removed implicitly. Every run ends with a
"gate" line stating the effective behaviour, derived from the installed
hook and the policy file actually on disk.

--approve-tools configures the clients so the five seamark MCP tools run
without permission prompts. For Claude Code it merges exact allow rules
for the tools and the three seamark skills into .claude/settings.json; a
skill's own allowed-tools grant lasts one turn and, in the version tested
(2.1.257), applied only when the skill was invoked by name. For Codex it
appends to .codex/config.toml: the "seamark mcp" registration when none
exists and approval_mode = "approve" for exactly the five tools, never a
server-wide default. Existing values, comments, and explicit restrictive
settings stay; conflicts are reported, not replaced. The rules are spelled
with the server name .mcp.json registers, so a server added under another
name is approved under that name. Which clients: Claude Code unless
--skills=codex; Codex when --skills names it (codex or all) or, without an
explicit client, when a .codex/ directory exists; --skills=claude is
Claude Code only. One setup command:

  seamark init --skills --approve-tools

--client selects the agents to set up, by name, and may repeat:

  seamark init --client codex --skills --approve-tools
  seamark init --client claude --client codex

Each selected client gets what seamark supports for it: its hooks and
its MCP server registration. --skills and --approve-tools stay opt-in
and apply to the selected clients; a registration alone approves
nothing. A client that is not selected is never read or written, so a
broken file of another agent cannot stop the run. What a client does
not support yet is reported, not skipped silently. Without --client,
init behaves exactly as before. --client takes a bare --skills only:
--skills=codex and the other values belong to the form without --client.

--lesson-checks off|check|enforce installs, switches, or removes the
lesson-check hooks of the hooked clients: check records a baseline
before each covered edit and verifies the result after it; enforce also
denies a covered violation before the write. --lesson-exploration on|off
installs or removes the advice hook on a structured Read; Claude Code
has one, and a client without it gets a note. Without these
flags init keeps the installed choice and installs nothing new. Enforce
needs a valid .seamark/lessons.yaml and a client with a pre-write guard;
otherwise init stops before any write. The flags never change the gate
mode. To roll back, run:

  seamark init --lesson-checks off --lesson-exploration off

Use --print to preview every change without writing anything.`,
		// init takes no positional arguments. The one likely mistake,
		// "--skills codex", parses as the bare flag plus a stray word
		// because the flag has an optional value; name the = form instead
		// of cobra's "unknown command".
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && slices.Contains(skills.Modes, args[0]) {
				return fmt.Errorf("init: --skills takes its value with =, as in --skills=%s", args[0])
			}

			return cobra.NoArgs(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if gateMode != "" && gateMode != gateModeWarn && gateMode != gateModeEnforce {
				return fmt.Errorf("init: --gate-mode must be %s or %s, got %q",
					gateModeWarn, gateModeEnforce, gateMode)
			}

			if err := lessonCheckFlagError(cmd, lessonChecks, lessonExploration); err != nil {
				return err
			}

			if skillsOpt.mode != "" && !slices.Contains(skills.Modes, skillsOpt.mode) {
				return fmt.Errorf("init: --skills must be one of %s, got %q",
					strings.Join(skills.Modes, ", "), skillsOpt.mode)
			}

			// Two selection rules in one run have no single meaning, so the
			// combination is refused with the unambiguous spelling.
			if len(clients) > 0 && skillsOpt.mode != "" {
				return fmt.Errorf("init: --skills=%s cannot be combined with --client; %s",
					skillsOpt.mode, clientSkillsReplacement(skillsOpt.mode))
			}

			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return err
			}

			run := initRun{
				w: cmd.OutOrStdout(), root: root, bin: scaffold.BinaryPath(),
				gateMode: gateMode, printOnly: printOnly, approveTools: approveTools,
				lessonChecks: lessonChecks, lessonExploration: lessonExploration,
			}

			if len(clients) > 0 {
				return runInitClients(run, clients, skillsOpt.requested)
			}

			return runInitLegacy(run, skillsOpt.legacyMode())
		},
	}

	cmd.Flags().BoolVar(&printOnly, "print", false, "preview changes without writing")
	cmd.Flags().StringVar(&gateMode, "gate-mode", "",
		"gate hook mode: warn (report, never block) or enforce (blocking verdicts exit 2); "+
			"omitted keeps the installed mode (warn on first init)")
	cmd.Flags().Var(&skillsOpt, "skills",
		"install the seamark agent skills: auto (.claude/skills, plus .agents/skills when .agents/ exists), "+
			"claude, codex, or all; a bare --skills means auto, other values need the = form (--skills=codex); "+
			"with --client, a bare --skills installs for the selected clients")
	// A bare --skills is allowed; pflag then needs the = form for explicit
	// values, which the help text and README both state.
	cmd.Flags().Lookup("skills").NoOptDefVal = bareSkills
	cmd.Flags().BoolVar(&approveTools, "approve-tools", false,
		"let the seamark MCP tools run without prompts: Claude Code allow rules in .claude/settings.json, "+
			"Codex per-tool approvals in .codex/config.toml (additive; never removes a setting)")
	cmd.Flags().StringVar(&lessonChecks, "lesson-checks", "",
		"lesson-check hooks: off (remove them), check (baseline before and verification after each covered edit), "+
			"or enforce (also deny a covered violation before the write); omitted keeps the installed choice")
	cmd.Flags().StringVar(&lessonExploration, "lesson-exploration", "",
		"exploration advice hook on a structured Read: on or off; omitted keeps the installed choice")
	cmd.Flags().StringArrayVar(&clients, "client", nil,
		"set up this agent only ("+clientSupport(integration.Builtin())+"); repeat the flag for several. "+
			"Omitted keeps the detection init always used")

	return cmd
}

// bareSkills is the value pflag hands to the flag for a bare --skills.
const bareSkills = "true"

// clientSupport lists each registered client with what seamark
// supports for it, from the descriptor, so the help text and the
// diagnostics make one support claim: "claude: skills, hooks, …".
func clientSupport(reg *integration.Registry) string {
	var parts []string

	for _, c := range reg.Clients() {
		parts = append(parts, c.ID+": "+c.DescribeSupport())
	}

	return strings.Join(parts, "; ")
}

// skillsFlag is the value of --skills. It records whether the flag was
// given bare or with an explicit mode. A plain string flag cannot: pflag
// sets a bare flag to its no-option default, so a bare --skills and
// --skills=auto would both read "auto". The difference matters with
// --client, which accepts only the bare form.
type skillsFlag struct {
	// requested is true when the flag was given in any form.
	requested bool
	// mode is the explicit value; empty for a bare flag.
	mode string
}

// Set records one occurrence of the flag. The flag reads like a boolean
// with optional modes: a bare --skills and --skills=true both ask for
// the skills with no mode named. An empty value and "false" ask for
// nothing, as an empty value always did, so a script that passes an
// unset variable installs nothing.
func (f *skillsFlag) Set(value string) error {
	switch value {
	case bareSkills:
		f.requested, f.mode = true, ""
	case "", "false":
		f.requested, f.mode = false, ""
	default:
		f.requested, f.mode = true, value
	}

	return nil
}

// String renders the value for pflag.
func (f *skillsFlag) String() string { return f.mode }

// Type is "bool" so the help prints the flag without a value
// placeholder: the bare form is the common one, and the usage text
// names the = form for the values.
func (*skillsFlag) Type() string { return "bool" }

// legacyMode returns the install mode for a run without --client: the
// explicit mode, auto for a bare flag, or empty when not requested.
func (f *skillsFlag) legacyMode() string {
	if f.requested && f.mode == "" {
		return skills.ModeAuto
	}

	return f.mode
}

// clientSkillsReplacement names the --client spelling of a --skills mode.
func clientSkillsReplacement(mode string) string {
	switch mode {
	case skills.ModeClaude, skills.ModeCodex:
		return fmt.Sprintf("use --client %s --skills", mode)
	case skills.ModeAll:
		return "use --client claude --client codex --skills"
	default:
		return "a bare --skills installs the skills for the selected clients"
	}
}

// initRun carries what every init form shares.
type initRun struct {
	w         io.Writer
	root, bin string
	// gateMode is the --gate-mode flag; empty keeps the installed mode.
	gateMode     string
	printOnly    bool
	approveTools bool
	// lessonChecks and lessonExploration are the lesson-check flags;
	// empty keeps the installed choice.
	lessonChecks      string
	lessonExploration string
}

// lessonCheckFlagError checks the values of the two lesson-check flags.
// An explicit empty value is an error too: the empty intent means "keep
// the installed choice", and a script that passes an unset variable
// must learn that its flag did nothing.
func lessonCheckFlagError(cmd *cobra.Command, checks, exploration string) error {
	switch {
	case cmd.Flags().Changed("lesson-checks") &&
		!slices.Contains([]string{hooks.LessonChecksOff, hooks.LessonChecksCheck, hooks.LessonChecksEnforce}, checks):
		return fmt.Errorf("--lesson-checks takes off, check, or enforce, got %q", checks)
	case cmd.Flags().Changed("lesson-exploration") &&
		!slices.Contains([]string{hooks.LessonExplorationOff, hooks.LessonExplorationOn}, exploration):
		return fmt.Errorf("--lesson-exploration takes off or on, got %q", exploration)
	}

	return nil
}

// runInit is an init run without --client and without a lesson-check
// flag. skillsMode is one of skills.Modes, or empty for "not
// requested".
func runInit(w io.Writer, root, bin, gateMode string, printOnly bool, skillsMode string, approveTools bool) error {
	return runInitLegacy(initRun{w: w, root: root, bin: bin, gateMode: gateMode, printOnly: printOnly, approveTools: approveTools},
		skillsMode)
}

// runInitLegacy is an init run without --client. The run is translated
// into the per-client intent it always had and then takes the same path
// as an explicit selection, so there is one implementation of setup.
// The lesson-check flags reach the clients that get hooks, and in this
// form that is Claude Code only.
func runInitLegacy(run initRun, skillsMode string) error {
	setups, err := integration.LegacySetups(run.root, skillsMode, run.approveTools, run.gateMode)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

	setups = integration.WithLessonChecks(setups, run.lessonChecks, run.lessonExploration)
	reg := integration.Builtin()

	return applyInit(run, reg, setups, legacyNotes{reg: reg, skillsRequested: skillsMode != ""})
}

// runInitClients is an init run with --client.
func runInitClients(run initRun, clients []string, installSkills bool) error {
	reg := integration.Builtin()

	setups, err := integration.ExplicitSetups(reg, clients, installSkills, run.approveTools, run.gateMode)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

	setups = integration.WithLessonChecks(setups, run.lessonChecks, run.lessonExploration)

	return applyInit(run, reg, setups, selectedNotes{reg: reg, skillsRequested: installSkills})
}

// initNotes prints the lines that depend on how the clients were
// selected. The setup itself does not: both forms plan and apply the
// same way.
type initNotes interface {
	// afterSetup prints the skills line or the missing-approval notes.
	afterSetup(run initRun, setups []integration.ClientSetup)
	// showInfo reports whether informational findings are printed.
	showInfo() bool
}

// applyInit plans the whole run, applies it, and narrates. Everything
// is read and validated before the first write: a malformed or
// wrong-shaped file of a selected client aborts init with the tree
// untouched, never halfway through the scaffolds.
func applyInit(run initRun, reg *integration.Registry, setups []integration.ClientSetup, notes initNotes) error {
	w := run.w

	// The request and the gate mode of the run: the flag, else what is
	// installed, else warn. The policy scaffold and the gate line both
	// need the mode.
	req, gateMode := scaffold.Request(reg, run.root, run.bin, setups, run.gateMode)
	narrateScaffolds(req.Common)

	plan, err := integration.PlanSetup(reg, req)
	if err != nil {
		return err
	}

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{
		Preview:   run.printOnly,
		Observe:   func(op integration.OpResult) { narrateOp(w, op) },
		SkillsLog: w,
	})
	if err != nil {
		return err
	}

	notes.afterSetup(run, setups)
	printFindings(w, plan.Findings, notes.showInfo())

	if len(integration.GateHookClients(reg, setups)) == 0 {
		// The flag configures gate hooks. Without one it reaches only the
		// policy scaffold, and the user must learn that here.
		if run.gateMode != "" {
			fmt.Fprintf(w, "  note    --gate-mode %s configures no hook in this run: no selected client has a gate hook yet;\n"+
				"          only a new .seamark/policy.yaml carries the mode\n", run.gateMode)
		}

		printNoGateHook(w, run.root, gateMode)
	} else {
		printGateLine(w, run.root, gateMode, run.gateMode != "", plan.GateHooks, plan.UncheckedGateSources, writesPolicy(plan))

		// The gate line describes the clients with a gate hook. A selected
		// client without one must not look covered by it.
		for _, name := range integration.UngatedHookClients(reg, setups) {
			fmt.Fprintf(w, "  note    %s has no command gate hook yet: the gate line above does not cover its shell commands\n", name)
		}
	}

	fmt.Fprintf(w, "\nnext: `seamark index` to build the graph, "+
		"`seamark index --reviews` for reviews + fixes, "+
		"or `seamark index --fixes-only` for local fixes\n")

	if run.printOnly {
		fmt.Fprintf(w, "(nothing was written — --print)\n")
	}

	return nil
}

// narrateOp prints one finished operation. A planner's own narrator
// wins, because only the planner knows what it composed. The skill
// installer prints its own lines through the skills log, so a skill
// directory is narrated here only when it has no such line: after a
// failure.
func narrateOp(w io.Writer, op integration.OpResult) {
	switch op.Status {
	case integration.OpFailed:
		fmt.Fprintf(w, "  failed  %s (%s)\n", op.Path, render.Sanitize(fmt.Sprint(op.Err)))

		return
	case integration.OpNotAttempted:
		fmt.Fprintf(w, "  skipped %s (not attempted: an earlier write failed)\n", op.Path)

		return
	}

	if op.Kind == integration.OpSkill {
		return
	}

	if op.Narrate != nil {
		op.Narrate(w, op.Status)

		return
	}

	verb := "kept"

	switch {
	case op.Status == integration.OpPlanned && op.Created:
		verb = "would write"
	case op.Status == integration.OpPlanned:
		verb = "would update"
	case op.Status == integration.OpApplied && op.Created:
		verb = "wrote"
	case op.Status == integration.OpApplied:
		verb = "updated"
	}

	if op.Detail == "" {
		fmt.Fprintf(w, "  %-7s %s\n", verb, op.Path)

		return
	}

	fmt.Fprintf(w, "  %-7s %s (%s)\n", verb, op.Path, render.Sanitize(op.Detail))
}

// narrateScaffolds gives each scaffold document its init narration. The
// scaffold package holds no prose, so the console and init can describe
// the same writes in their own words. A written starter line names no
// detail; the .gitignore lines always name the carve-outs.
func narrateScaffolds(docs []integration.Document) {
	for i := range docs {
		doc := docs[i]

		docs[i].Narrate = func(w io.Writer, status integration.OpStatus) {
			switch {
			case status == integration.OpKept:
				fmt.Fprintf(w, "  kept    %s (%s)\n", doc.Path, doc.KeptDetail)
			case status == integration.OpPlanned && doc.CreateOnly:
				fmt.Fprintf(w, "  would write  %s\n", doc.Path)
			case status == integration.OpPlanned:
				fmt.Fprintf(w, "  would update %s (%s)\n", doc.Path, doc.Detail)
			case doc.CreateOnly:
				fmt.Fprintf(w, "  wrote  %s\n", doc.Path)
			default:
				fmt.Fprintf(w, "  updated %s (%s)\n", doc.Path, doc.Detail)
			}
		}
	}
}

// printFindings prints what the plan found and no narrator line shows.
// A warning is always printed. An informational finding is printed for
// an explicit selection only: a run without --client keeps the output
// it always had.
func printFindings(w io.Writer, findings []integration.Finding, showInfo bool) {
	for _, f := range findings {
		if f.Level == integration.FindingInfo && !showInfo {
			continue
		}

		reason := render.Sanitize(f.Reason)
		if f.Path != "" {
			reason = f.Path + ": " + reason
		}

		fmt.Fprintf(w, "  note    %s\n", reason)

		if f.Action != "" {
			fmt.Fprintf(w, "          %s\n", render.Sanitize(f.Action))
		}
	}
}

// printGateLine reports the EFFECTIVE blocking behaviour, derived from
// the hook AND the policy file actually on disk — not from the flag
// alone: a kept policy.yaml can carry a different mode than the hook,
// and claiming "nothing blocks" while a kept `mode: enforce` still
// blocks would repeat the exact trust bug this command exists to
// prevent.
//
// The same rule covers a gate hook that setup does not manage: one in
// another source, such as the user's local settings file, or a wrapped
// command in the shared file. Setup leaves that hook as it is, so it
// still runs: when it enforces, the run blocks whatever the mode of the
// hook setup manages. A definition that may run a gate can block too,
// so a warn line beside one claims only that the gate hooks that
// certainly run block nothing.
//
// A run can also leave a source unchecked: init without --client never
// reads the user's local settings file for gate hooks. The line then
// names that file and the command that checks it, because a hook there
// can block or stop blocking without this run knowing.
//
// requested is true when the user typed --gate-mode. Without the flag,
// the mode of the run comes from the installed hooks. A line that says
// where the flag went would then name a flag that the user never gave.
//
// policyNew is true when this run writes .seamark/policy.yaml. The line
// then calls the file new, not kept: the file carries the mode of this
// run, not an earlier choice of the user.
func printGateLine(w io.Writer, root, gateMode string, requested bool, gateHooks []integration.GateHook, unchecked []string,
	policyNew bool,
) {
	policyMode, policyErr := policyFileMode(root, gateMode)

	policyWord := "kept"
	if policyNew {
		policyWord = "new"
	}

	// The summary comes from the hooks the run leaves, never from the
	// mode it asked for: a hook setup does not manage keeps its own mode,
	// and the managed hook is left out when such a hook already runs.
	// The groups and the effect come from the rule status uses too.
	summary := integration.SummarizeGateHooks(gateHooks)

	var (
		managed        *integration.GateHook // the first managed hook that runs
		managedEnforce bool
		managedOff     bool     // a managed hook sits in a client whose hooks are off
		offEnforce     bool     // a managed hook that is off carries --enforce
		enforcing      []string // the unmanaged hooks that enforce
	)

	for i, hook := range gateHooks {
		// A hook that the client does not run describes nothing that
		// happens now. The off line and the note below name it. They give
		// its mode only for a managed hook, because setup knows what that
		// hook does once the hooks run. The sentences about the run still
		// must not deny that hook.
		if hook.HooksOff {
			if hook.Managed {
				managedOff = true
				offEnforce = offEnforce || hook.Mode == gateModeEnforce
			}

			continue
		}

		if hook.Managed && managed == nil {
			managed = &gateHooks[i]
		}

		// A hook that may run enforces nothing for certain. The summary
		// lists it as possible, and a note below names it.
		switch {
		case hook.Mode != gateModeEnforce || hook.Uncertain:
		case hook.Managed:
			managedEnforce = true
		case !slices.Contains(enforcing, hook.Path):
			enforcing = append(enforcing, hook.Path)
		}
	}

	// What the managed hook does beside an unmanaged enforcing one. Setup
	// can install a managed hook in a client whose hooks are off. That hook
	// exists, so the line says that it does not run, not that it is absent.
	beside, besideSentence := "; setup installed no gate hook of its own beside it", "Setup installed no gate hook of its own beside it"

	switch {
	case managed != nil:
		beside, besideSentence = ", although the hook setup manages is in warn mode", "The hook setup manages is in warn mode"
	case managedOff:
		beside, besideSentence = "; the hook setup manages does not run while the hooks are off",
			"The hook setup manages does not run while the hooks are off"
	}

	reach := enforceReach(requested, offEnforce)

	switch effect := summary.Effect(); {
	case effect == integration.GateEffectNone && len(summary.HooksOff) > 0:
		// Every gate hook of the run sits in a client whose hooks are off.
		// A mode line would claim verdicts that never come, so the line
		// says what is true now and what each managed hook does later.
		fmt.Fprintf(w, "  gate    off — no gate hook runs while the hooks are off (see the note above), so no shell command gets a verdict\n")

		for _, line := range offManagedLines(gateHooks, false) {
			fmt.Fprintf(w, "          %s\n", line)
		}

		if policyErr != nil {
			fmt.Fprintf(w, "          note: .seamark/policy.yaml also failed to load (%v)\n", policyErr)
		}
	case effect == integration.GateEffectNone:
		// A gated client whose gate hook never fires: an owned entry under
		// another matcher, which setup does not rewrite. The coverage
		// finding names it.
		if policyErr != nil {
			fmt.Fprintf(w, "  gate    no gate hook runs for shell commands; .seamark/policy.yaml failed to load (%v)\n", policyErr)
		} else {
			fmt.Fprintf(w, "  gate    no gate hook runs for shell commands — .seamark/policy.yaml (mode: %s) governs\n"+
				"          plain `seamark gate` and `seamark check` runs\n", policyMode)
		}
	case effect == integration.GateEffectUnknown:
		// No hook of the run certainly runs a gate, and a definition may.
		// The plan never holds an unreadable document, so the possible
		// definitions are the whole reason.
		fmt.Fprintf(w, "  gate    unknown — %s holds a definition that may run a gate hook, and setup cannot tell what\n"+
			"          it does; %s\n", strings.Join(summary.Possible, ", "), possibleRoute(summary))

		if policyErr != nil {
			fmt.Fprintf(w, "          note: .seamark/policy.yaml also failed to load (%v)\n", policyErr)
		}
	case effect == integration.GateEffectBlocks && !managedEnforce && policyErr != nil:
		fmt.Fprintf(w, "  gate    enforce — %s runs its own gate hook with --enforce, and .seamark/policy.yaml\n"+
			"          failed to load (%v); that hook fails closed: EVERY hooked command blocks until the\n"+
			"          policy is fixed. %s\n", strings.Join(enforcing, ", "), policyErr, besideSentence)
	case effect == integration.GateEffectBlocks && !managedEnforce:
		fmt.Fprintf(w, "  gate    enforce — %s runs its own gate hook with --enforce: deny/require_approval\n"+
			"          verdicts exit 2 and block%s. Setup never\n"+
			"          edits a gate hook it does not manage; remove that hook to stop blocking\n", strings.Join(enforcing, ", "), beside)

		if policyMode == gateModeEnforce {
			fmt.Fprintf(w, "          note: the %s .seamark/policy.yaml also sets mode: enforce, so every gate hook blocks\n"+
				"          too; both must change to stop blocking\n", policyWord)
		}
	case effect == integration.GateEffectReportOnly:
		// Every gate hook of the run discards its exit status or runs in
		// the background. No mode and no policy makes it block, so the line
		// must not name one. The fix names the cause of each hook.
		fix := "run the gate last in that hook"
		if len(summary.Background) > 0 {
			fix = "remove the background key from that hook"
			if len(summary.Background) < len(summary.ReportOnly) {
				fix = "run the gate last and in the foreground in that hook"
			}
		}

		fmt.Fprintf(w, "  gate    report-only — the gate hook in %s %s: verdicts\n"+
			"          are reported, nothing blocks, whatever --enforce or .seamark/policy.yaml says. Setup never\n"+
			"          edits a gate hook it does not manage; %s, or remove it and re-run\n",
			strings.Join(summary.ReportOnly, ", "), summary.ReportOnlyCause(summary.ReportOnly), fix)

		if policyErr != nil {
			fmt.Fprintf(w, "          note: .seamark/policy.yaml also failed to load (%v)\n", policyErr)
		}
	case policyErr != nil && managedEnforce:
		fmt.Fprintf(w, "  gate    enforce — but .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook fails closed: EVERY hooked command blocks until the policy is fixed\n", policyErr)
	case policyErr != nil:
		fmt.Fprintf(w, "  gate    warn — .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook reports the error and fails open\n", policyErr)
	case managedEnforce:
		// The gate's own failures exit 2 only while the gate process runs.
		// The line names the two cases where it never returns, because
		// then the client's own rule decides and the gate cannot block.
		fmt.Fprintf(w, "  gate    enforce — deny/require_approval verdicts exit 2 and block; so do the gate's own failures.\n"+
			"          The gate cannot block a command when the client stops the hook at its timeout (setup\n"+
			"          writes %d s) or cannot start the seamark binary\n", hooks.GateTimeout)

		if policyMode != gateModeEnforce {
			fmt.Fprintf(w, "          note: the %s .seamark/policy.yaml says mode: %s — it still governs plain\n"+
				"          `seamark gate` and `seamark check` runs; only a gate hook with --enforce blocks\n", policyWord, policyMode)
		}
	case policyMode == gateModeEnforce:
		fmt.Fprintf(w, "  gate    enforce — the %s .seamark/policy.yaml sets mode: enforce and the hook\n"+
			"          follows it: deny/require_approval verdicts exit 2 and block; edit policy.yaml\n"+
			"          to stop blocking\n", policyWord)
	case gateMode == gateModeEnforce && len(summary.Possible) > 0:
		// A definition that may run a gate can block, for example a
		// filtered one that enforces. So the line limits its claim to the
		// gate hooks that certainly run. The note below names the
		// definition.
		line := "  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing"
		if reach != "" {
			line += ";\n          " + reach
		}

		fmt.Fprintln(w, line)
	case gateMode == gateModeEnforce:
		// The mode of the run is enforce, and no gate hook that runs
		// carries it. The notes below name each hook and why.
		line := "  gate    warn — verdicts are reported, nothing blocks"
		if reach != "" {
			line += "; " + reach
		}

		fmt.Fprintln(w, line)
	case len(summary.Possible) > 0:
		fmt.Fprintf(w, "  gate    warn — verdicts are reported; the gate hooks that certainly run block nothing"+
			" (opt in: --gate-mode enforce)\n")
	default:
		fmt.Fprintf(w, "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)\n")
	}

	// The note follows every gate line, also a blocking one: a local copy
	// of the managed handler in the background makes the managed gate
	// report-only. The path is an adapter constant, so it needs no scrub.
	if len(unchecked) > 0 {
		fmt.Fprintf(w, "          this run does not check %s for gate hooks, so this line leaves\n"+
			"          them out; `seamark doctor` checks that file\n", strings.Join(unchecked, ", "))
	}

	// The gate line above describes the hooks that run. A hook that the
	// client does not run must not look covered by it. The paths are
	// adapter constants, so they need no scrub.
	if len(summary.HooksOff) > 0 && summary.Effect() > integration.GateEffectNone {
		fmt.Fprintf(w, "  note    the gate hook in %s does not run while the hooks are off (see the note above);"+
			" the gate line covers the other hooks\n", strings.Join(summary.HooksOff, ", "))

		// The gate line leaves out the managed hook that is off, so the
		// note states its mode. Another hook runs, so each line names the
		// path of the hook that is off.
		for _, line := range offManagedLines(gateHooks, true) {
			fmt.Fprintf(w, "          %s\n", line)
		}
	}

	// The unknown line above already names the possible definitions.
	if len(summary.Possible) > 0 && summary.Effect() > integration.GateEffectUnknown {
		fmt.Fprintf(w, "  note    %s holds a definition that may also run a gate hook; setup cannot tell what it does\n",
			strings.Join(summary.Possible, ", "))
	}

	printGateModeNotes(w, gateMode, gateHooks)
}

// offManagedLines say what each managed gate hook that is off does once
// its client runs the hooks again. One such hook gets one line without
// its path, unless named is true. Several get one line each with the
// path, because the modes of the clients can differ. One sentence would
// give the mode of one hook to all of them. The note beside a gate line
// sets named, because another hook runs there. "The gate hook setup
// manages" could then read as a hook that runs. The result is empty when
// no managed hook is off. The paths are adapter constants, so they need
// no scrub.
func offManagedLines(gateHooks []integration.GateHook, named bool) []string {
	var off []integration.GateHook

	for _, hook := range gateHooks {
		if hook.HooksOff && hook.Managed {
			off = append(off, hook)
		}
	}

	if len(off) == 1 && !named {
		return []string{"the gate hook setup manages " + offManagedEffect(off[0].Mode)}
	}

	lines := make([]string, 0, len(off))
	for _, hook := range off {
		lines = append(lines, "the managed gate hook in "+hook.Path+" "+offManagedEffect(hook.Mode))
	}

	return lines
}

// offManagedEffect says what a managed gate hook in this mode does once
// its client runs the hooks again.
func offManagedEffect(mode string) string {
	switch mode {
	case gateModeEnforce:
		return "carries --enforce: its deny/require_approval verdicts block once the hooks run"
	case integration.GateModeReportOnly:
		return "stays report-only once the hooks run"
	default:
		return "follows .seamark/policy.yaml once the hooks run"
	}
}

// enforceReach says where a typed --gate-mode enforce went, for a run
// whose gate hooks that run carry no --enforce. It is empty when the
// user typed no --gate-mode, because the line must not name a flag that
// the user never gave. A managed hook that is off can carry the flag.
// The sentence then limits its claim to the hooks that run, and a note
// names the hook that is off.
func enforceReach(requested, offEnforce bool) string {
	switch {
	case !requested:
		return ""
	case offEnforce:
		return "--gate-mode enforce reached no gate hook that runs"
	default:
		return "--gate-mode enforce reached no gate hook of this run"
	}
}

// possibleRoute names the route for the definitions that may run a gate
// hook, in the words that doctor prints for each one. A filtered handler
// already runs the gate directly, so it gets the route that removes the
// filter. A run with both kinds names both routes.
func possibleRoute(summary integration.GateSummary) string {
	switch {
	case summary.FilteredPossible == 0:
		return integration.UncertainHookRoute
	case summary.UnfilteredPossible == 0:
		return integration.FilteredHookRoute
	default:
		return "for a definition with an `if` field, " + integration.FilteredHookRoute + ";\n" +
			"          for another, " + integration.UncertainHookRoute
	}
}

// printGateModeNotes names each gate hook that runs without --enforce
// in a run whose mode is enforce, and why. A managed hook of another
// selected client keeps its installed mode when no mode is requested. A
// hook setup does not manage never gets the flag: setup edits no such
// hook, and it installs no managed hook beside one that certainly runs.
// The gate line above must not read as covering either. A warn run
// needs no note: an enforcing hook it leaves is the gate line itself.
func printGateModeNotes(w io.Writer, gateMode string, gateHooks []integration.GateHook) {
	if gateMode != gateModeEnforce {
		return
	}

	for _, hook := range gateHooks {
		// The note above names a hook that may run; its mode is not known
		// to apply. A hook that the client does not run has its own note.
		switch {
		case hook.Mode != gateModeWarn || hook.Uncertain || hook.HooksOff:
		case hook.Managed:
			fmt.Fprintf(w, "  note    %s runs its gate hook without --enforce: that hook follows .seamark/policy.yaml\n"+
				"          instead; re-run with --gate-mode enforce to bake the flag into every selected client's gate hook\n",
				hook.Path)
		default:
			fmt.Fprintf(w, "  note    %s runs a gate hook without --enforce that setup does not manage: --gate-mode enforce\n"+
				"          does not reach it, and that hook follows .seamark/policy.yaml; edit it, or remove it and re-run\n"+
				"          to let setup manage the gate\n", hook.Path)
		}
	}
}

// printNoGateHook is the gate line of a run that installs no gate hook,
// because no selected client supports one yet. The line must not
// describe a hook that does not exist; the policy file still governs the
// plain commands.
func printNoGateHook(w io.Writer, root, gateMode string) {
	policyMode, err := policyFileMode(root, gateMode)
	if err != nil {
		fmt.Fprintf(w, "  gate    no gate hook for the selected clients; .seamark/policy.yaml failed to load (%v)\n", err)

		return
	}

	fmt.Fprintf(w, "  gate    no gate hook for the selected clients — .seamark/policy.yaml (mode: %s) governs\n"+
		"          plain `seamark gate` and `seamark check` runs\n", policyMode)
}

// writesPolicy reports whether the plan writes .seamark/policy.yaml. The
// plan keeps an existing starter file, so a write means that the file is
// new in this run. A preview plans the same write.
func writesPolicy(plan *integration.SetupPlan) bool {
	return slices.ContainsFunc(plan.Writes, func(fw integration.FileWrite) bool {
		return fw.Path == ".seamark/policy.yaml"
	})
}

// policyFileMode reads the mode of .seamark/policy.yaml through the real
// loader. An absent file resolves to fallback: the scaffold that init is
// writing (or would write, under --print) carries exactly that mode.
func policyFileMode(root, fallback string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, ".seamark", "policy.yaml")); os.IsNotExist(err) {
		return fallback, nil
	}

	policy, err := gate.LoadPolicy(root)
	if err != nil {
		return "", err
	}

	return policy.Mode, nil
}
