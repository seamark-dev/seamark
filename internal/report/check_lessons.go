package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds the check-lessons/1 envelope of
// `check --lessons --json` and the text form of check with and without
// lesson verification. The type lives in report and not in cli,
// because the MCP server imports report and gate but not cli, and both
// build the envelope and print the text. report runs no check and
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
	State    lessoncheck.CombinedState `json:"state"`     // pass, fail, or incomplete
	ExitCode int                       `json:"exit_code"` // 0, 1, or 2: the CLI exits with it
	// Policy says only whether the policy blocks: pass or blocked. A deny
	// in warn mode is pass here, so PolicyVerdict names the verdict.
	Policy        string                   `json:"policy"`
	PolicyVerdict string                   `json:"policy_verdict"` // the verdict of Policy: allow, deny, or require_approval
	Lessons       lessoncheck.SummaryState `json:"lessons"`        // the summary state of Lessons
	PolicyInput   string                   `json:"policy_input"`   // PolicyInputStdinDiff or PolicyInputGitDiffHead
	// DiffPathsOutsideSnapshot lists the changed paths of a supplied diff
	// that the verified snapshot does not hold, in diff order. The lesson
	// checks read the working tree, so they did not verify these paths.
	// It is empty for git diff HEAD.
	DiffPathsOutsideSnapshot []string `json:"diff_paths_outside_snapshot,omitempty"`
}

// NewCheckLessonsEnvelope builds the envelope of one check --lessons
// run. It fills Combined from lessoncheck.Combine with the summary state
// and policy.Blocking(), so no caller derives the state, the exit code,
// or the policy value itself. diffPaths are the changed paths of the
// diff that the policy read (gate.ChangedPaths). policy and lessons must
// not be nil.
func NewCheckLessonsEnvelope(policy *gate.Decision, lessons *lessoncheck.Verification, policyInput string, diffPaths []string) CheckLessonsEnvelope {
	combined := lessoncheck.Combine(lessons.Summary.State, policy.Blocking())

	// Only a supplied diff can name a path that the working tree does not
	// change. git diff HEAD reads the same tree as the snapshot.
	var outside []string
	if policyInput == PolicyInputStdinDiff {
		outside = pathsOutsideSnapshot(diffPaths, lessons.Changes)
	}

	return CheckLessonsEnvelope{
		Schema:  CheckLessonsSchema,
		Policy:  policy,
		Lessons: lessons,
		Combined: CombinedResult{
			State:                    combined.State,
			ExitCode:                 combined.ExitCode,
			Policy:                   combined.Policy,
			PolicyVerdict:            policy.Verdict,
			Lessons:                  lessons.Summary.State,
			PolicyInput:              policyInput,
			DiffPathsOutsideSnapshot: outside,
		},
	}
}

// pathsOutsideSnapshot returns the paths that no change of the snapshot
// names, as its path or as the source of a move, in the order of paths.
func pathsOutsideSnapshot(paths []string, changes []lessoncheck.Change) []string {
	held := make(map[string]bool, 2*len(changes))

	for _, ch := range changes {
		held[ch.Path] = true

		if ch.OldPath != "" {
			held[ch.OldPath] = true
		}
	}

	var outside []string

	for _, p := range paths {
		if !held[p] {
			outside = append(outside, p)
		}
	}

	return outside
}

// WriteCheckLessons writes the text form of one check run with lesson
// verification: the combined line first, the note on diff paths that
// the lesson checks did not read, a blank line, the policy text, a blank
// line, and the lesson report. The combined verdict leads, because a
// reader who stops early must not read only the policy verdict. withExit
// adds the CLI exit code, for the MCP server, which has no exit code of
// its own.
func WriteCheckLessons(w io.Writer, env CheckLessonsEnvelope, policyText string, withExit bool) {
	fmt.Fprint(w, combinedLine(env, withExit))

	if outside := env.Combined.DiffPathsOutsideSnapshot; len(outside) > 0 {
		fmt.Fprint(w, outsideNote(outside))
	}

	fmt.Fprintln(w)
	fmt.Fprint(w, policyText)
	fmt.Fprintln(w)
	lessoncheck.RenderText(w, env.Lessons)
}

// combinedLine returns the combined line. A verdict that is not allow and
// does not block is named with its mode, so a deny in warn mode never
// reads as a policy pass. That clause holds a comma, so a semicolon
// then separates it from the lessons clause.
func combinedLine(env CheckLessonsEnvelope, withExit bool) string {
	c := env.Combined
	policy, sep := "policy "+c.Policy, ", "

	if !env.Policy.Blocking() && c.PolicyVerdict != gate.VerdictAllow {
		policy = fmt.Sprintf("policy %s, not blocking in %s mode",
			render.Sanitize(c.PolicyVerdict), render.Sanitize(env.Policy.Mode))
		sep = "; "
	}

	line := fmt.Sprintf("combined: %s (%s%slessons %s", c.State, policy, sep, c.Lessons)
	if withExit {
		line += fmt.Sprintf("; CLI exit %d", c.ExitCode)
	}

	return line + ")\n"
}

// maxNotePaths bounds the paths that one note names. The JSON field
// holds every path.
const maxNotePaths = 3

// outsideNote says that the lesson checks did not read some files of the
// supplied diff. It names the first maxNotePaths and counts the rest.
func outsideNote(paths []string) string {
	shown := paths
	if len(shown) > maxNotePaths {
		shown = shown[:maxNotePaths]
	}

	names := make([]string, len(shown))
	for i, p := range shown {
		names[i] = render.Sanitize(p)
	}

	list := strings.Join(names, ", ")
	if more := len(paths) - len(shown); more > 0 {
		list += fmt.Sprintf(", and %d more", more)
	}

	count := "1 file of the diff is"
	if len(paths) != 1 {
		count = fmt.Sprintf("%d files of the diff are", len(paths))
	}

	return fmt.Sprintf("note: the lesson checks read the working tree, not the supplied diff; %s not in the verified snapshot (%s)\n",
		count, list)
}

// lessonChecksRun is the route to the verification that a check without
// it did not run, in the words of the MCP option and of the CLI flag.
const lessonChecksRun = "; run with lesson_checks: true (CLI: seamark check --lessons)\n"

// LessonChecksNotRequested returns the line that ends a check text
// without lesson verification when at least one pin of mode check or
// enforce covers a changed path, else "". Without it the advisory lists
// such a pin, and nothing says that its check exists and did not run.
// An invalid or unreadable lessons.yaml gives a line too, because no
// count is known then, and silence would read as none. root holds the
// .seamark directory, as for CheckAdvisory.
func LessonChecksNotRequested(root string, changed []string) string {
	if len(changed) == 0 {
		return ""
	}

	cfg, errs, err := lessoncheck.Load(root)
	if err != nil || len(errs) > 0 {
		return "lesson checks: not requested — lessons.yaml is invalid, so whether an executable pin covers this diff is unknown" +
			lessonChecksRun
	}

	switch n := cfg.ExecutablePinsCovering(changed); n {
	case 0:
		return ""
	case 1:
		return "lesson checks: not requested — 1 executable pin covers this diff" + lessonChecksRun
	default:
		return fmt.Sprintf("lesson checks: not requested — %d executable pins cover this diff", n) + lessonChecksRun
	}
}
