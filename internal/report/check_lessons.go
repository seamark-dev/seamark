package report

import (
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// This file holds the check-lessons/1 envelope of
// `check --lessons --json`. The type lives in report and not in cli,
// because the MCP server imports report and gate but not cli, and both
// build the envelope. report holds the type only: it runs no check and
// decides no exit code. lessoncheck.Combine decides the combined state
// and the exit code.

// CheckLessonsSchema is the format name of the envelope.
const CheckLessonsSchema = "check-lessons/1"

// The closed list of policy inputs. A policy input names the source of
// the diff that the policy read. The lesson checks always read the
// working tree, whatever the source.
const (
	// PolicyInputStdinDiff is a diff that the caller gave to the command.
	PolicyInputStdinDiff = "stdin-diff"
	// PolicyInputGitDiffHead is the output of `git diff HEAD`.
	PolicyInputGitDiffHead = "git-diff-head"
)

// CheckLessonsEnvelope is the check-lessons/1 document: the policy
// decision, the lesson verification, and their combined verdict.
type CheckLessonsEnvelope struct {
	Schema   string                    `json:"schema"` // CheckLessonsSchema
	Policy   *gate.Decision            `json:"policy"` // the exact object that plain `check --json` prints
	Lessons  *lessoncheck.Verification `json:"lessons"`
	Combined CombinedResult            `json:"combined"`
}

// CombinedResult is the combined verdict of the envelope. State,
// ExitCode, and Policy come from lessoncheck.Combine, so the CLI and the
// MCP server print the same values.
type CombinedResult struct {
	State       lessoncheck.CombinedState `json:"state"`        // pass, fail, or incomplete
	ExitCode    int                       `json:"exit_code"`    // 0, 1, or 2: the CLI exits with it
	Policy      string                    `json:"policy"`       // pass or blocked
	Lessons     lessoncheck.SummaryState  `json:"lessons"`      // the summary state of Lessons
	PolicyInput string                    `json:"policy_input"` // PolicyInputStdinDiff or PolicyInputGitDiffHead
}

// NewCheckLessonsEnvelope builds the envelope of one check --lessons
// run. It fills Combined from lessoncheck.Combine with the summary state
// and policy.Blocking(), so no caller derives the state, the exit code,
// or the policy value itself. policy and lessons must not be nil.
func NewCheckLessonsEnvelope(policy *gate.Decision, lessons *lessoncheck.Verification, policyInput string) CheckLessonsEnvelope {
	combined := lessoncheck.Combine(lessons.Summary.State, policy.Blocking())

	return CheckLessonsEnvelope{
		Schema:  CheckLessonsSchema,
		Policy:  policy,
		Lessons: lessons,
		Combined: CombinedResult{
			State:       combined.State,
			ExitCode:    combined.ExitCode,
			Policy:      combined.Policy,
			Lessons:     lessons.Summary.State,
			PolicyInput: policyInput,
		},
	}
}
