package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// checkLessonsVerification returns a verification result with the
// summary state and the snapshot changes. The envelope reads no other
// field.
func checkLessonsVerification(state lessoncheck.SummaryState, changes ...lessoncheck.Change) *lessoncheck.Verification {
	return &lessoncheck.Verification{
		Schema:  lessoncheck.VerificationSchema,
		Summary: lessoncheck.Summary{State: state},
		Changes: changes,
	}
}

// TestCheckLessonsCombinedLine proves the combined line and the combined
// JSON of every verdict and mode. A verdict that is not allow and does
// not block is named with its mode, on both surfaces, while the state,
// the exit code, and the policy word stay those of lessoncheck.Combine.
// Before the fix a deny in warn mode read "policy pass".
func TestCheckLessonsCombinedLine(t *testing.T) {
	cases := []struct {
		verdict, mode string
		lessons       lessoncheck.SummaryState
		state         lessoncheck.CombinedState
		exit          int
		policy        string // the JSON policy value
		line          string // the line without the CLI exit clause
	}{
		{gate.VerdictAllow, "warn", lessoncheck.SummaryPassed, lessoncheck.CombinedPass, 0, "pass",
			"combined: pass (policy pass, lessons passed)"},
		{gate.VerdictDeny, "warn", lessoncheck.SummaryPassed, lessoncheck.CombinedPass, 0, "pass",
			"combined: pass (policy deny, not blocking in warn mode; lessons passed)"},
		{gate.VerdictDeny, "warn", lessoncheck.SummaryViolation, lessoncheck.CombinedFail, 2, "pass",
			"combined: fail (policy deny, not blocking in warn mode; lessons violation)"},
		{gate.VerdictApproval, "warn", lessoncheck.SummaryNoApplicableChecks, lessoncheck.CombinedPass, 0, "pass",
			"combined: pass (policy require_approval, not blocking in warn mode; lessons no-applicable-checks)"},
		{gate.VerdictApproval, "warn", lessoncheck.SummaryViolation, lessoncheck.CombinedFail, 2, "pass",
			"combined: fail (policy require_approval, not blocking in warn mode; lessons violation)"},
		{gate.VerdictApproval, "warn", lessoncheck.SummaryIncomplete, lessoncheck.CombinedIncomplete, 1, "pass",
			"combined: incomplete (policy require_approval, not blocking in warn mode; lessons incomplete)"},
		{gate.VerdictAllow, "enforce", lessoncheck.SummaryPassed, lessoncheck.CombinedPass, 0, "pass",
			"combined: pass (policy pass, lessons passed)"},
		{gate.VerdictDeny, "enforce", lessoncheck.SummaryPassed, lessoncheck.CombinedFail, 2, "blocked",
			"combined: fail (policy blocked, lessons passed)"},
		{gate.VerdictApproval, "enforce", lessoncheck.SummaryViolation, lessoncheck.CombinedFail, 2, "blocked",
			"combined: fail (policy blocked, lessons violation)"},
	}

	for _, c := range cases {
		t.Run(c.verdict+"/"+c.mode+"/"+string(c.lessons), func(t *testing.T) {
			d := &gate.Decision{Verdict: c.verdict, Mode: c.mode}
			env := NewCheckLessonsEnvelope(d, checkLessonsVerification(c.lessons), PolicyInputGitDiffHead, nil)

			assert.Equal(t, c.state, env.Combined.State)
			assert.Equal(t, c.exit, env.Combined.ExitCode)
			assert.Equal(t, c.policy, env.Combined.Policy, "policy says only whether the policy blocks")
			assert.Equal(t, c.verdict, env.Combined.PolicyVerdict)

			assert.Equal(t, c.line+"\n", combinedLine(env, false))

			withExit := strings.TrimSuffix(c.line, ")") + "; CLI exit " + strconv.Itoa(c.exit) + ")\n"
			assert.Equal(t, withExit, combinedLine(env, true))

			data, err := json.Marshal(env.Combined)
			require.NoError(t, err)
			assert.Contains(t, string(data), `"policy":"`+c.policy+`","policy_verdict":"`+c.verdict+`"`)
			assert.NotContains(t, string(data), "diff_paths_outside_snapshot", "git diff HEAD names no outside path")
		})
	}
}

// TestWriteCheckLessonsLeadsWithCombined proves the text layout with
// lesson verification: the combined line first, the note on diff paths
// outside the snapshot when there are any, a blank line, the policy
// text, a blank line, and the lesson report. The combined line is not
// repeated at the end. Before the change it was the last line.
func TestWriteCheckLessonsLeadsWithCombined(t *testing.T) {
	const policyText = "verdict  allow (mode: warn)\n"

	v := checkLessonsVerification(lessoncheck.SummaryPassed,
		lessoncheck.Change{Path: "src/a.go"})

	var report strings.Builder
	lessoncheck.RenderText(&report, v)

	d := &gate.Decision{Verdict: gate.VerdictAllow, Mode: "warn"}

	var b strings.Builder
	WriteCheckLessons(&b, NewCheckLessonsEnvelope(d, v, PolicyInputGitDiffHead, []string{"src/a.go"}), policyText, false)
	assert.Equal(t, "combined: pass (policy pass, lessons passed)\n\n"+policyText+"\n"+report.String(), b.String())

	b.Reset()
	WriteCheckLessons(&b, NewCheckLessonsEnvelope(d, v, PolicyInputStdinDiff, []string{"src/a.go", "src/b.go"}), policyText, true)
	assert.Equal(t, "combined: pass (policy pass, lessons passed; CLI exit 0)\n"+
		"note: the lesson checks read the working tree, not the supplied diff; 1 file of the diff is not in the verified snapshot (src/b.go)\n"+
		"\n"+policyText+"\n"+report.String(), b.String())
	assert.Equal(t, 1, strings.Count(b.String(), "combined: "))
}

// TestCheckLessonsDiffPathsOutsideSnapshot proves the outside-path fact
// of a supplied diff. A path that a change names, as its path or as the
// source of a move, is in the snapshot. git diff HEAD never gets the
// fact. The note names three paths, counts the rest, and sanitizes each
// path. The JSON lists every path.
func TestCheckLessonsDiffPathsOutsideSnapshot(t *testing.T) {
	d := &gate.Decision{Verdict: gate.VerdictAllow, Mode: "warn"}
	v := checkLessonsVerification(lessoncheck.SummaryPassed,
		lessoncheck.Change{Path: "a.go"},
		lessoncheck.Change{Path: "new.go", OldPath: "old.go"})

	diff := []string{"a.go", "old.go", "new.go", "c.go", "d\x1b[31m.go", "e.go", "f.go"}

	env := NewCheckLessonsEnvelope(d, v, PolicyInputStdinDiff, diff)
	assert.Equal(t, []string{"c.go", "d\x1b[31m.go", "e.go", "f.go"}, env.Combined.DiffPathsOutsideSnapshot)
	assert.Equal(t, "note: the lesson checks read the working tree, not the supplied diff; "+
		"4 files of the diff are not in the verified snapshot (c.go, d[31m.go, e.go, and 1 more)\n",
		outsideNote(env.Combined.DiffPathsOutsideSnapshot))

	assert.Empty(t, NewCheckLessonsEnvelope(d, v, PolicyInputGitDiffHead, diff).Combined.DiffPathsOutsideSnapshot,
		"git diff HEAD reads the tree that the snapshot reads")
	assert.Empty(t, NewCheckLessonsEnvelope(d, v, PolicyInputStdinDiff, []string{"a.go", "new.go"}).Combined.DiffPathsOutsideSnapshot)
}

// TestCheckLessonsDiffPathsNeedAnExecutableCheck proves that the outside
// paths of a supplied diff need an executable check: with none, no check
// read the working tree, so the note and the JSON field would warn about
// nothing. Every other state keeps them, also no applicable check, where
// the supplied diff can hold the change that a check would apply to, and
// incomplete, which an invalid configuration gives.
func TestCheckLessonsDiffPathsNeedAnExecutableCheck(t *testing.T) {
	d := &gate.Decision{Verdict: gate.VerdictAllow, Mode: "warn"}
	diff := []string{"a.go", "src/b.go"}

	none := NewCheckLessonsEnvelope(d, checkLessonsVerification(lessoncheck.SummaryNoExecutableChecks,
		lessoncheck.Change{Path: "a.go"}), PolicyInputStdinDiff, diff)
	assert.Empty(t, none.Combined.DiffPathsOutsideSnapshot)

	data, err := json.Marshal(none)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "diff_paths_outside_snapshot")

	var b strings.Builder
	WriteCheckLessons(&b, none, "verdict  allow (mode: warn)\n", false)
	assert.NotContains(t, b.String(), "note: the lesson checks read")

	for _, state := range []lessoncheck.SummaryState{
		lessoncheck.SummaryNoApplicableChecks, lessoncheck.SummaryPassed,
		lessoncheck.SummaryIncomplete, lessoncheck.SummaryViolation,
	} {
		env := NewCheckLessonsEnvelope(d, checkLessonsVerification(state,
			lessoncheck.Change{Path: "a.go"}), PolicyInputStdinDiff, diff)
		assert.Equal(t, []string{"src/b.go"}, env.Combined.DiffPathsOutsideSnapshot, state)
	}
}

// TestLessonChecksNotRequested proves the line that ends a check without
// lesson verification. It names the count of executable pins that cover
// a changed path, says that an invalid or unreadable lessons.yaml leaves
// the count unknown, and gives "" when no executable pin covers a path,
// so the ordinary text keeps its bytes.
func TestLessonChecksNotRequested(t *testing.T) {
	const (
		route   = "; run with lesson_checks: true (CLI: seamark check --lessons)\n"
		invalid = "lesson checks: not requested — lessons.yaml is invalid, so whether an executable pin covers this diff is unknown" + route
	)

	const config = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Do not edit.
    mode: enforce
    check: frozen-assets
  - rule: frozen-docs
    region: docs/
    note: Do not edit.
    mode: check
    check: frozen-assets
  - rule: advisory-src
    region: src/
    note: Only advice.
    check: frozen-assets
`

	cases := []struct {
		name    string
		yaml    string // "" = no lessons.yaml
		changed []string
		want    string
	}{
		{name: "no lessons.yaml", changed: []string{"generated/a.txt"}},
		{name: "one covering pin", yaml: config, changed: []string{"generated/a.txt", "main.go"},
			want: "lesson checks: not requested — 1 executable pin covers this diff" + route},
		{name: "two covering pins", yaml: config, changed: []string{"generated/a.txt", "docs/x.md"},
			want: "lesson checks: not requested — 2 executable pins cover this diff" + route},
		{name: "an advisory pin only", yaml: config, changed: []string{"src/a.go"}},
		{name: "no covered path", yaml: config, changed: []string{"main.go"}},
		{name: "no changed path", yaml: "pin: [\n"},
		{name: "a file that does not decode", yaml: "pin: [\n", changed: []string{"main.go"}, want: invalid},
		{name: "an unsupported version", yaml: strings.Replace(config, "lesson_checks_version: 1", "lesson_checks_version: 9", 1),
			changed: []string{"main.go"}, want: invalid},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()

			if c.yaml != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(c.yaml), 0o644))
			}

			assert.Equal(t, c.want, LessonChecksNotRequested(root, c.changed))
		})
	}
}
