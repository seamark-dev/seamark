package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds lessons --set-mode (contracts K6 and §4). The command
// checks its flags, plans the edit through maintain.PlanSetMode, and
// writes it through maintain.CommitSetMode. The engine owns every rule
// of the edit, so the console form of milestone P4 refuses the same
// requests with the same texts.

// setModeConflicts lists the lessons modes and decision flags that
// --set-mode refuses, in the order of the help text. The first flag
// that the call sets names the conflict (contracts §4.2). Each modifier
// flag, for example --client, keeps its own guard and is not here.
var setModeConflicts = []string{
	"file", "list", "region", "stats", "distill", "proposals", "verify", "apply",
	"dismiss", "prune", "retarget", "extract-triggers", "hook", "hook-reset",
}

// setModePaths is the line that names the explicit verification paths
// of an executable pin (K6 step 6, plan row P1-g). They run a check
// whatever hooks are installed. The active lines name the hook paths.
const setModePaths = "supported paths: seamark lessons --verify, seamark check --lessons, " +
	"and the MCP check tool with lesson_checks: true"

// setModeFlags are the flag values of one lessons --set-mode call.
type setModeFlags struct {
	mode, pin, check string
}

// setModeFlagError checks the flags that --set-mode adds before any
// dispatch. --pin and --check apply to --set-mode only, and --set-mode
// combines with no other mode. The --pin text is the milestone P1 form
// of contracts §4.1: milestone P5 adds --delivery to it.
func setModeFlagError(cmd *cobra.Command, f setModeFlags) error {
	setMode := cmd.Flags().Changed("set-mode")
	checkSet := cmd.Flags().Changed("check")

	switch {
	case checkSet && !setMode:
		return errors.New("--check applies to --set-mode only")
	case cmd.Flags().Changed("pin") && !setMode:
		return errors.New("--pin applies to --set-mode only")
	case !setMode:
		return nil
	}

	for _, name := range setModeConflicts {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--set-mode does not combine with --%s", name)
		}
	}

	if strings.TrimSpace(f.pin) == "" {
		return errors.New("--set-mode needs --pin <pin-ref>")
	}

	if _, err := maintain.ParseMode(f.mode); err != nil {
		return err
	}

	// An empty ID means "keep the check" to the engine. A set flag
	// counts also when it is empty, so the call never keeps the old
	// check without notice.
	if checkSet && f.check == "" {
		return errors.New("--check needs a check ID: seamark lessons --set-mode <mode> --pin <pin-ref> --check <check-id>")
	}

	return nil
}

// runLessonsSetMode runs lessons --set-mode. It changes the mode and the
// check keys of one pin and nothing else. The root is the git top level
// of the -C directory, the root of the lessons.yaml that lessons
// --verify reads, so the pin-ref of its pin column names a pin of the
// same file. The command needs no index, because it writes no ledger
// row.
//
// The exit code is 0 for a write and for an unchanged request, and 1
// for every refusal (§5 row 26). With distill.write off, the command
// prints the checker impact and returns the lines to set by hand as its
// refusal (§5 row 28).
func runLessonsSetMode(cmd *cobra.Command, opts *options, f setModeFlags) error {
	ws, err := workspace.Locate(opts.workspace, "")
	if err != nil {
		return err
	}

	deps := maintain.Deps{Root: ws.Root, Reg: integration.Builtin()}

	plan, err := maintain.PlanSetMode(deps, maintain.SetModeRequest{
		PinRef: f.pin, Mode: lessoncheck.Mode(f.mode), CheckID: f.check,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	if plan.Change == nil {
		fmt.Fprintf(out, "pin %s already has mode %s and check %s; nothing to change\n",
			plan.Before.Ref, modeText(plan.Before), checkText(plan.Before.CheckID))

		return nil
	}

	// The gate decides only a change. A config that cannot be read is
	// an error then, never a default.
	if plan.GateErr != nil {
		return plan.GateErr
	}

	// The impact block prints on both routes, so the join comes first.
	joinActiveState(plan, deps.Reg, ws.Root)

	if !plan.Write {
		printImpact(out, plan.Impact)

		return setModeByHand(plan)
	}

	if err := maintain.CommitSetMode(deps, plan); err != nil {
		return err
	}

	printSetMode(out, plan)

	return nil
}

// joinActiveState replaces the fixed active text of milestone P1 with
// the K10 join (K6 step 6, §5 row 41). The report reads the candidate
// bytes of the plan, so it shows the mode that the pin requests after
// the edit, also when the write gate is off and nothing is written. The
// pin-ref is a prefix of the pin identity, so it names the same pin
// before and after the edit. The impact block gets the one-line form,
// and the active lines get one line per client. A candidate that does
// not decode keeps the fixed text, because the plan validated it and
// such a state is not reachable.
//
// SetModePlan.Active keeps its type of display lines (K-4). The CLI
// fills it, because the engine has no binary identity.
func joinActiveState(plan *maintain.SetModePlan, reg *integration.Registry, root string) {
	cfg, errs, err := lessoncheck.Parse(plan.Change.After)
	if err != nil {
		return
	}

	report := integration.LessonCapabilityOf(reg.Inspect(root), cfg, errs, integration.SelfIdentity(version))
	plan.Active = report.ActiveLines(plan.After.Ref)

	for i := range plan.Impact {
		if plan.Impact[i].PinRef == plan.Before.Ref {
			plan.Impact[i].Active = report.ActiveText(plan.After.Ref)
		}
	}
}

// printSetMode prints the output of a written edit (K6 step 6): the
// checker impact, the K6 success line, the requested mode, the active
// state, and the supported paths of an executable pin.
func printSetMode(out io.Writer, plan *maintain.SetModePlan) {
	printImpact(out, plan.Impact)

	fmt.Fprintf(out, "pin %s: mode %s -> %s, check %s -> %s; version %s\n",
		plan.Before.Ref, modeText(plan.Before), plan.After.Mode,
		checkText(plan.Before.CheckID), checkText(plan.After.CheckID), shortVersion(plan.After.Version))
	fmt.Fprintf(out, "requested mode: %s\n", plan.After.Mode)

	for _, line := range plan.Active {
		fmt.Fprintln(out, line)
	}

	if plan.After.Mode != lessoncheck.ModeAdvisory {
		fmt.Fprintln(out, setModePaths)
	}
}

// setModeByHand returns the K6 refusal of the write gate: the lines to
// set on the pin by hand. It names only the keys that the edit changes.
// "mode: advisory" stands for the removal of the mode key, because an
// explicit advisory mode reads the same and gives the same pin version.
func setModeByHand(plan *maintain.SetModePlan) error {
	var lines []string

	if modeText(plan.Before) != string(plan.After.Mode) {
		lines = append(lines, "  mode: "+string(plan.After.Mode))
	}

	if plan.After.CheckID != plan.Before.CheckID {
		lines = append(lines, "  check: "+render.Sanitize(plan.After.CheckID))
	}

	return fmt.Errorf("distill.write is off in .seamark/config.yaml — nothing was written; "+
		"set these lines on pin %s by hand:\n%s", plan.Before.Ref, strings.Join(lines, "\n"))
}

// modeText returns the mode of a pin for display. An unknown mode text
// normalizes to advisory, so the raw text is shown instead: the person
// then sees the value that the edit replaces.
func modeText(p lessoncheck.Pin) string {
	if p.Rule.Mode != "" && p.Rule.Mode != string(p.Mode) {
		return fmt.Sprintf("%q", p.Rule.Mode)
	}

	return string(p.Mode)
}

// checkText returns a check ID for display, or "none".
func checkText(id string) string {
	if id == "" {
		return "none"
	}

	return render.Sanitize(id)
}

// shortVersionHex is the length of a version in the K6 success line,
// the 12 hex characters of the K1 impact block.
const shortVersionHex = 12

// shortVersion returns the short form of a pin version.
func shortVersion(v string) string {
	if len(v) > shortVersionHex {
		return v[:shortVersionHex]
	}

	return v
}
