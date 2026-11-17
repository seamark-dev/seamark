package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

// This file holds check --lessons. The policy path of check runs
// unchanged first. Then lessoncheck.Verify reads the default
// working-tree selection, whatever diff the policy read.
// report.NewCheckLessonsEnvelope combines both verdicts, and the command
// exits with the combined exit code. The text form leads with the
// combined line. The command decides no outcome itself.

// checkLessonsRun is the state of one check run when its policy path is
// done.
type checkLessonsRun struct {
	root        string // the workspace root of the policy path
	lessonsRoot string // the top level of the git work tree
	st          *store.Store
	diff        string // the diff that the policy read
	policyInput string // the source of diff: report.PolicyInputStdinDiff or report.PolicyInputGitDiffHead
	decision    *gate.Decision
	asJSON      bool
}

// runCheckLessons prints the policy verdict and the lesson verification
// of one check --lessons run. The text form is report.WriteCheckLessons:
// the combined line first, then the plain check text and the lesson
// report. The JSON form is only the check-lessons/1 document. The
// returned error makes Execute exit with the combined exit code.
func runCheckLessons(cmd *cobra.Command, run checkLessonsRun) error {
	out := cmd.OutOrStdout()
	changed := gate.ChangedPaths(run.diff)

	// The plain check text renders before the snapshot. The advisory
	// appends to the lessons audit under .seamark, and a write after the
	// snapshot read can make the result stale. The text waits in a
	// buffer, because the combined line comes first.
	var policyText bytes.Buffer

	if !run.asJSON {
		report.Decision(&policyText, run.decision)
		report.CheckCompanions(&policyText, run.st, run.root, changed)
		report.CheckAdvisory(&policyText, run.st, run.root, changed)
	}

	// SQLite keeps its WAL files next to an open index, and a read can
	// change them. Closing the index first removes them, so the snapshot
	// holds the same files as for lessons --verify. The caller closes
	// the store again, and a second close does nothing.
	_ = run.st.Close()

	// A zero Limits means the default limits. Verify sets the
	// verify-timeout deadline itself.
	v, err := lessoncheck.Verify(cmd.Context(), lessoncheck.VerifyRequest{
		Root:           run.lessonsRoot,
		Caller:         lessoncheck.CallerCLI,
		SeamarkVersion: version,
	})
	if err != nil {
		return err
	}

	env := report.NewCheckLessonsEnvelope(run.decision, v, run.policyInput, changed)

	if run.asJSON {
		// The encoding is compact, as plain check --json prints the
		// decision. The policy member then holds the same bytes.
		if err := json.NewEncoder(out).Encode(env); err != nil {
			return err
		}
	} else {
		report.WriteCheckLessons(out, env, policyText.String(), false)
	}

	// A failed audit record never changes the outcome or the exit code.
	// The rule of lessons --verify applies: on an exit-0 path one stderr
	// line names it, and a non-zero exit keeps its own line alone.
	if auditErr := v.AuditError(); auditErr != nil && env.Combined.ExitCode == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "seamark: lesson-check audit:", auditErr)
	}

	return checkLessonsExit(env.Combined, run.decision, v)
}

// checkLessonsExit returns the error that makes Execute exit with
// c.ExitCode. Exit 0 returns nil. A blocking policy returns its
// gate.Blocked error, so the policy line wins on stderr. Otherwise
// Combine and ExitError read the same summary state: exit 2 gets the
// violation error, and exit 1 gets the incomplete error.
func checkLessonsExit(c report.CombinedResult, d *gate.Decision, v *lessoncheck.Verification) error {
	if c.ExitCode == 0 {
		return nil
	}

	if err := blockedError(d); err != nil {
		return err
	}

	return v.ExitError()
}
