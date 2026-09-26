package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
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
(blocking the agent's command) and the gate's own failures fail closed.
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
				w: cmd.OutOrStdout(), root: root, bin: seamarkPath(),
				gateMode: gateMode, printOnly: printOnly, approveTools: approveTools,
			}

			if len(clients) > 0 {
				return runInitClients(run, clients, skillsOpt.requested)
			}

			return runInit(run.w, run.root, run.bin, gateMode, printOnly, skillsOpt.legacyMode(), approveTools)
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
	cmd.Flags().StringArrayVar(&clients, "client", nil,
		"set up this agent only ("+strings.Join(integration.Builtin().IDs(), ", ")+"); repeat the flag for several. "+
			"Omitted keeps the detection init always used")

	return cmd
}

// bareSkills is the value pflag hands to the flag for a bare --skills.
const bareSkills = "true"

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

// seamarkPath resolves the absolute path of the running binary, so the
// hooks invoke this exact install regardless of the caller's PATH. Falls
// back to the bare name if resolution fails.
func seamarkPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "seamark"
	}

	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	return stableInstallPath(exe)
}

// stableInstallPath maps a versioned Homebrew Cellar path to the keg's
// stable opt symlink. Homebrew deletes the old Cellar directory on every
// upgrade, so hooks that keep it break. The opt symlink survives
// upgrades. Every other path returns unchanged.
func stableInstallPath(path string) string {
	prefix, rest, found := strings.Cut(path, "/Cellar/seamark/")
	if !found {
		return path
	}

	// The remainder must be exactly <version>/bin/seamark. A different
	// layout is not a Homebrew keg of this binary.
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "bin" || parts[2] != "seamark" {
		return path
	}

	// Substitute only when the opt symlink really exists, so a broken
	// or partial install keeps the path that is known to work.
	opt := filepath.Join(prefix, "opt", "seamark", "bin", "seamark")
	if _, err := os.Stat(opt); err != nil {
		return path
	}

	return opt
}

// initRun carries what every init form shares.
type initRun struct {
	w         io.Writer
	root, bin string
	// gateMode is the --gate-mode flag; empty keeps the installed mode.
	gateMode     string
	printOnly    bool
	approveTools bool
}

// runInit is an init run without --client. skillsMode is one of
// skills.Modes, or empty for "not requested". The run is translated
// into the per-client intent it always had and then takes the same path
// as an explicit selection, so there is one implementation of setup.
func runInit(w io.Writer, root, bin, gateMode string, printOnly bool, skillsMode string, approveTools bool) error {
	setups, err := integration.LegacySetups(root, skillsMode, approveTools, gateMode)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

	run := initRun{w: w, root: root, bin: bin, gateMode: gateMode, printOnly: printOnly, approveTools: approveTools}

	return applyInit(run, integration.Builtin(), setups, legacyNotes{skillsRequested: skillsMode != ""})
}

// runInitClients is an init run with --client.
func runInitClients(run initRun, clients []string, installSkills bool) error {
	reg := integration.Builtin()

	setups, err := integration.ExplicitSetups(reg, clients, installSkills, run.approveTools, run.gateMode)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

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

	// The gate mode of the run: the flag, else what is installed, else
	// warn. The policy scaffold and the gate line both need it.
	hookClients := integration.GateHookClients(reg, setups)
	gateMode := resolveGateMode(integration.InstalledGateMode(reg, run.root, hookClients), run.gateMode)

	plan, err := integration.PlanSetup(reg, integration.SetupRequest{
		Root: run.root, Binary: run.bin, Clients: setups, Common: commonDocuments(gateMode),
	})
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

	if len(hookClients) == 0 {
		// The flag configures gate hooks. Without one it reaches only the
		// policy scaffold, and the user must learn that here.
		if run.gateMode != "" {
			fmt.Fprintf(w, "  note    --gate-mode %s configures no hook in this run: no selected client has a gate hook yet;\n"+
				"          only a new .seamark/policy.yaml carries the mode\n", run.gateMode)
		}

		printNoGateHook(w, run.root, gateMode)
	} else {
		printGateLine(w, run.root, gateMode, plan.GateHooks)

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

// commonDocuments returns the client-independent files of an init run:
// the three .seamark scaffolds and the .gitignore carve-outs. The setup
// coordinator guards and writes them with the client documents, so one
// preflight covers every write.
func commonDocuments(gateMode string) []integration.Document {
	var docs []integration.Document

	for _, f := range []struct {
		rel, body string
	}{
		{".seamark/policy.yaml", starterPolicyFor(gateMode)},
		{".seamark/lessons.yaml", starterLessons},
		{".seamark/config.yaml", starterConfig},
	} {
		docs = append(docs, integration.Document{
			Path:       f.rel,
			CreateOnly: true,
			Detail:     "starter",
			KeptDetail: "already present",
			// A scaffold is a starter. An existing file is never clobbered.
			Compose: func(existing []byte, exists bool) ([]byte, error) {
				if exists {
					return existing, nil
				}

				return []byte(f.body), nil
			},
			Narrate: func(w io.Writer, status integration.OpStatus) {
				switch status {
				case integration.OpKept:
					fmt.Fprintf(w, "  kept    %s (already present)\n", f.rel)
				case integration.OpPlanned:
					fmt.Fprintf(w, "  would write  %s\n", f.rel)
				default:
					fmt.Fprintf(w, "  wrote  %s\n", f.rel)
				}
			},
		})
	}

	return append(docs, integration.Document{
		Path:       ".gitignore",
		Detail:     "seamark carve-outs",
		KeptDetail: "seamark carve-outs already present",
		Compose:    composeGitignore,
		Narrate: func(w io.Writer, status integration.OpStatus) {
			switch status {
			case integration.OpKept:
				fmt.Fprintf(w, "  kept    .gitignore (seamark carve-outs already present)\n")
			case integration.OpPlanned:
				fmt.Fprintf(w, "  would update .gitignore (seamark carve-outs)\n")
			default:
				fmt.Fprintf(w, "  updated .gitignore (seamark carve-outs)\n")
			}
		},
	})
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
// hook setup manages.
func printGateLine(w io.Writer, root, gateMode string, gateHooks []integration.GateHook) {
	policyMode, policyErr := policyFileMode(root, gateMode)

	// The summary comes from the hooks the run leaves, never from the
	// mode it asked for: a hook setup does not manage keeps its own mode,
	// and the managed hook is left out when such a hook already runs.
	var (
		managed        *integration.GateHook
		managedEnforce bool
		enforcing      []string // the unmanaged hooks that enforce
	)

	for i, hook := range gateHooks {
		if hook.Managed && managed == nil {
			managed = &gateHooks[i]
		}

		switch {
		case hook.Mode != gateModeEnforce:
		case hook.Managed:
			managedEnforce = true
		case !slices.Contains(enforcing, hook.Path):
			enforcing = append(enforcing, hook.Path)
		}
	}

	// What the managed hook does beside an unmanaged enforcing one.
	beside, besideSentence := "; setup installed no gate hook of its own beside it", "Setup installed no gate hook of its own beside it"
	if managed != nil {
		beside, besideSentence = ", although the hook setup manages is in warn mode", "The hook setup manages is in warn mode"
	}

	switch {
	case len(gateHooks) == 0:
		// A gated client whose gate hook never fires: an owned entry under
		// another matcher, which setup does not rewrite. The coverage
		// finding names it.
		if policyErr != nil {
			fmt.Fprintf(w, "  gate    no gate hook runs for shell commands; .seamark/policy.yaml failed to load (%v)\n", policyErr)
		} else {
			fmt.Fprintf(w, "  gate    no gate hook runs for shell commands — .seamark/policy.yaml (mode: %s) governs\n"+
				"          plain `seamark gate` and `seamark check` runs\n", policyMode)
		}
	case !managedEnforce && len(enforcing) > 0 && policyErr != nil:
		fmt.Fprintf(w, "  gate    enforce — %s runs its own gate hook with --enforce, and .seamark/policy.yaml\n"+
			"          failed to load (%v); that hook fails closed: EVERY hooked command blocks until the\n"+
			"          policy is fixed. %s\n", strings.Join(enforcing, ", "), policyErr, besideSentence)
	case !managedEnforce && len(enforcing) > 0:
		fmt.Fprintf(w, "  gate    enforce — %s runs its own gate hook with --enforce: deny/require_approval\n"+
			"          verdicts exit 2 and block%s. Setup never\n"+
			"          edits a gate hook it does not manage; remove that hook to stop blocking\n", strings.Join(enforcing, ", "), beside)

		if policyMode == gateModeEnforce {
			fmt.Fprintf(w, "          note: the kept .seamark/policy.yaml also sets mode: enforce, so every gate hook blocks\n"+
				"          too; both must change to stop blocking\n")
		}
	case policyErr != nil && managedEnforce:
		fmt.Fprintf(w, "  gate    enforce — but .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook fails closed: EVERY hooked command blocks until the policy is fixed\n", policyErr)
	case policyErr != nil:
		fmt.Fprintf(w, "  gate    warn — .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook reports the error and fails open\n", policyErr)
	case managedEnforce:
		fmt.Fprintf(w, "  gate    enforce — deny/require_approval verdicts exit 2 and block; gate failures fail closed\n")

		if policyMode != gateModeEnforce {
			fmt.Fprintf(w, "          note: the kept .seamark/policy.yaml says mode: %s — it still governs plain\n"+
				"          `seamark gate` and `seamark check` runs; only a gate hook with --enforce blocks\n", policyMode)
		}
	case policyMode == gateModeEnforce:
		fmt.Fprintf(w, "  gate    enforce — the kept .seamark/policy.yaml sets mode: enforce and the hook\n"+
			"          follows it: deny/require_approval verdicts exit 2 and block; edit policy.yaml\n"+
			"          to stop blocking\n")
	case gateMode == gateModeEnforce:
		// The run asked for enforce and no hook of the run carries it. The
		// notes below name each hook and why.
		fmt.Fprintf(w, "  gate    warn — verdicts are reported, nothing blocks; --gate-mode enforce reached no gate hook of this run\n")
	default:
		fmt.Fprintf(w, "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)\n")
	}

	printGateModeNotes(w, gateMode, gateHooks)
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
		switch {
		case hook.Mode != gateModeWarn:
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

// resolveGateMode turns the --gate-mode flag into the mode of the run.
// An empty flag keeps what a previous init installed (warn on the first
// run): enforcement must never be added — or removed — implicitly.
func resolveGateMode(installed, requested string) string {
	if requested != "" {
		return requested
	}

	if installed != "" {
		return installed
	}

	return gateModeWarn
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

// gitignoreBlock is appended verbatim when no carve-outs are present; a
// .gitignore initialized by an older seamark grows just the lines it is
// missing.
const gitignoreBlock = `
# Seamark: the index and audit log stay local; the policy-as-code
# overlays belong in review.
.seamark/*
!.seamark/policy.yaml
!.seamark/effects.yaml
!.seamark/lessons.yaml
!.seamark/config.yaml
`

// carveoutLines returns the pattern lines of gitignoreBlock (comments and
// blanks dropped) — the unit ensureGitignore checks and repairs, so a new
// overlay file added to the block reaches already-initialized repos too.
func carveoutLines() []string {
	var lines []string

	for _, ln := range strings.Split(gitignoreBlock, "\n") {
		if ln != "" && !strings.HasPrefix(ln, "#") {
			lines = append(lines, ln)
		}
	}

	return lines
}

// composeGitignore returns .gitignore with the seamark carve-outs. A
// file that holds them all returns unchanged. No carve-outs at all gets
// the commented block; an older block grows just its missing lines.
// Appending at the end keeps every `!` re-include after `.seamark/*`,
// the order gitignore precedence needs.
func composeGitignore(existing []byte, _ bool) ([]byte, error) {
	present := map[string]bool{}
	for _, ln := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(ln)] = true
	}

	lines := carveoutLines()

	var missing []string

	for _, ln := range lines {
		if !present[ln] {
			missing = append(missing, ln)
		}
	}

	if len(missing) == 0 {
		return existing, nil
	}

	block := gitignoreBlock
	if len(missing) < len(lines) {
		block = "\n" + strings.Join(missing, "\n") + "\n"
	}

	return append(slices.Clone(existing), block...), nil
}
