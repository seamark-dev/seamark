package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/report"
)

// checkLegacyDiff is the piped diff of the legacy check goldens. Its
// first part equals `git diff HEAD` in checkLegacyFixture: it changes
// the body of store.Save, which reaches fs:write. Its second part adds
// store/doc.go, which holds no symbol. The working tree has that file
// untracked, so only a piped diff names it.
const checkLegacyDiff = `diff --git a/store/save.go b/store/save.go
index 62ae309..f4485a5 100644
--- a/store/save.go
+++ b/store/save.go
@@ -5,5 +5,6 @@ import "os"
 // Save writes the payload.
 func Save(path string, data []byte) error {
 	// The path is trusted.
+	// Owner-only: the payload is private.
 	return os.WriteFile(path, data, 0o600)
 }
diff --git a/store/doc.go b/store/doc.go
new file mode 100644
index 0000000..d839dd2
--- /dev/null
+++ b/store/doc.go
@@ -0,0 +1,2 @@
+// Package store keeps payloads on disk.
+package store
`

// checkLegacyFixture builds a repository whose plain check output holds
// every legacy part: an effect, two policy matches, an unindexed-file
// note, a companion from history, and a pinned lesson. The working tree
// holds the change of checkLegacyDiff. store/save.go is modified, and
// store/doc.go is untracked, so `git diff HEAD` leaves doc.go out.
func checkLegacyFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		// Fixed identities and dates keep every object ID stable.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	commit := func(subject string) {
		git("add", "-A")
		git("commit", "-q", "-m", subject)
	}

	save := func(comment string) string {
		return "package store\n\nimport \"os\"\n\n// Save writes the payload.\n" +
			"func Save(path string, data []byte) error {\n" + comment +
			"\treturn os.WriteFile(path, data, 0o600)\n}\n"
	}

	load := func(comment string) string {
		return "package store\n\nimport \"os\"\n\n// Load reads the payload.\n" +
			"func Load(path string) ([]byte, error) {\n" + comment +
			"\treturn os.ReadFile(path)\n}\n"
	}

	git("init", "-q", "-b", "main")

	write("go.mod", "module example.com/legacy\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("store/save.go", save(""))
	write("store/load.go", load(""))
	write("docs/notes.md", "notes\n")
	write(".seamark/policy.yaml", "mode: warn\nrequire_approval:\n"+
		"  - id: store-writes\n"+
		"    when: 'diff.effects.contains(\"fs:write\")'\n"+
		"    message: the change reaches file writes\n"+
		"  - id: unindexed-blindspot\n"+
		"    when: 'diff.unindexed_files > 0'\n"+
		"    message: changed files outside index coverage\n")
	write(".seamark/lessons.yaml", "pin:\n  - rule: store-owner-only\n    region: store/\n"+
		"    note: Write store files with owner-only modes.\n")
	commit("Add the store")

	// Save and Load change together in three commits and the notes alone
	// in two. Then load.go is a companion of save.go with a lift above one.
	write("store/save.go", save("\t// Callers own the path.\n"))
	write("store/load.go", load("\t// Callers own the path.\n"))
	commit("Document path ownership")

	write("store/save.go", save("\t// The path is trusted.\n"))
	write("store/load.go", load("\t// The path is trusted.\n"))
	commit("State the path trust")

	write("docs/notes.md", "notes\nmore\n")
	commit("Extend the notes")

	write("docs/notes.md", "notes\nmore\nlast\n")
	commit("Finish the notes")

	write("store/save.go", save("\t// The path is trusted.\n\t// Owner-only: the payload is private.\n"))
	write("store/doc.go", "// Package store keeps payloads on disk.\npackage store\n")

	return root
}

// TestCheckLessonsAbsentKeepsPlainJSON freezes the stdout of plain
// `check` and `check --json` as it was before `--lessons` existed,
// because a call without the flag must keep its bytes. Both diff
// sources are covered: a piped diff and the `git diff HEAD` fallback.
// The two sources give different bytes, so each golden proves that its
// branch of readDiff ran. Stderr is not frozen, because the index
// refresh line depends on the run order.
func TestCheckLessonsAbsentKeepsPlainJSON(t *testing.T) {
	const (
		pipedHuman = `verdict  require_approval (mode: warn)
effects  [fs:write]
  [require_approval] store-writes: the change reaches file writes
  [require_approval] unindexed-blindspot: changed files outside index coverage
  note: 1 of 2 changed files have changes outside any indexed symbol (store/doc.go) — their effect reach is unknown, not clean

history suggests also reviewing  (usually changes with the diff's files, absent from this diff)
  store/load.go                                      3 shared commits with store/save.go, lift 1.7
    mostly Load

advisory — recurring lessons for touched files (not part of the verdict)
- [pin · store] store-owner-only — Write store files with owner-only modes.
`
		pipedJSON = `{"verdict":"require_approval","mode":"warn","effects":["fs:write"],` +
			`"matches":[{"rule":"store-writes","verdict":"require_approval","message":"the change reaches file writes"},` +
			`{"rule":"unindexed-blindspot","verdict":"require_approval","message":"changed files outside index coverage"}],` +
			`"policy_sha256":"a1074762ae4a826cfee3f077ec13c41ddd014ab5088247a9a05ceb0a7cec1926",` +
			`"notes":["1 of 2 changed files have changes outside any indexed symbol (store/doc.go) — their effect reach is unknown, not clean"]}` +
			"\n"
		headHuman = `verdict  require_approval (mode: warn)
effects  [fs:write]
  [require_approval] store-writes: the change reaches file writes

history suggests also reviewing  (usually changes with the diff's files, absent from this diff)
  store/load.go                                      3 shared commits with store/save.go, lift 1.7
    mostly Load

advisory — recurring lessons for touched files (not part of the verdict)
- [pin · store] store-owner-only — Write store files with owner-only modes.
`
		headJSON = `{"verdict":"require_approval","mode":"warn","effects":["fs:write"],` +
			`"matches":[{"rule":"store-writes","verdict":"require_approval","message":"the change reaches file writes"}],` +
			`"policy_sha256":"a1074762ae4a826cfee3f077ec13c41ddd014ab5088247a9a05ceb0a7cec1926"}` +
			"\n"
	)

	root := checkLegacyFixture(t)

	for _, c := range []struct {
		name  string
		stdin string // "" makes check fall back to `git diff HEAD`
		args  []string
		want  string
	}{
		{name: "piped human", stdin: checkLegacyDiff, want: pipedHuman},
		{name: "piped json", stdin: checkLegacyDiff, args: []string{"--json"}, want: pipedJSON},
		{name: "git diff HEAD human", want: headHuman},
		{name: "git diff HEAD json", args: []string{"--json"}, want: headJSON},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, _, err := runIn(t, c.stdin, append([]string{"-C", root, "check"}, c.args...)...)
			require.NoError(t, err, "a warn-mode policy never blocks")
			assert.Equal(t, c.want, stdout)
		})
	}
}

// The tests of check --lessons. Every test builds a temporary
// repository with the lessons of verifyLessonsYAML and a policy, and
// runs the command under the invoker sentinel. Nothing reads the
// repository of this checkout.

// checkLessonsPolicy asks for approval of every change. In warn mode it
// never blocks. With --enforce it blocks every diff that names a file.
const checkLessonsPolicy = "mode: warn\nrequire_approval:\n" +
	"  - id: any-change\n" +
	"    when: 'size(diff.files) > 0'\n" +
	"    message: the change touches files\n"

// The stderr lines of Execute for check --lessons in the fixture.
const (
	checkLessonsBlockedLine = "seamark: blocked by policy: the change touches files"
	checkLessonsRefreshLine = "seamark: index refreshed"
)

// checkLessonsFixture builds a committed repository under a new
// temporary directory: verifyLessonsYAML, checkLessonsPolicy, a Go
// file under src/, an asset under generated/, and the agent section of
// sentinel. Check writes its index and audit files under .seamark. The
// .gitignore keeps them out of the working-tree snapshot, because a
// lesson result must not depend on them. It returns the git top level;
// the caller owns the sentinel environment and its end check.
func checkLessonsFixture(t *testing.T, sentinel *invokerSentinel) (root string) {
	t.Helper()

	if sentinel == nil {
		t.Skip("the sentinel stubs are shell scripts")
	}

	dir := t.TempDir()

	verifyGit(t, dir, "init", "-q", "-b", "main")

	verifyWrite(t, dir, ".gitignore",
		".seamark/*\n!.seamark/lessons.yaml\n!.seamark/policy.yaml\n!.seamark/config.yaml\n")
	verifyWrite(t, dir, "go.mod", "module example.com/checklessons\n")
	verifyWrite(t, dir, "generated/a.txt", "asset\n")
	verifyWrite(t, dir, "src/handler.go", "package src\n\nfunc Handle() {}\n")
	verifyWrite(t, dir, "docs/readme.md", "readme\n")
	verifyWrite(t, dir, ".seamark/lessons.yaml", verifyLessonsYAML)
	verifyWrite(t, dir, ".seamark/policy.yaml", checkLessonsPolicy)

	// The sentinel adds .seamark/config.yaml. The commit keeps it out
	// of the working-tree changes.
	sentinel.install(t, dir)

	verifyGit(t, dir, "add", "-A")
	verifyGit(t, dir, "commit", "-q", "-m", "Add the fixture")

	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return root
}

// The working-tree edits of the check --lessons tests. A comment in
// src/handler.go passes no-todo. A new file under generated/ is an
// untracked change that frozen-assets forbids.
func checkLessonsEditHandler(t *testing.T, root string) {
	t.Helper()
	verifyWrite(t, root, "src/handler.go", "package src\n\n// Handle serves.\nfunc Handle() {}\n")
}

func checkLessonsAddAsset(t *testing.T, root string) {
	t.Helper()
	verifyWrite(t, root, "generated/new.txt", "new asset\n")
}

// checkExitLines returns the stderr lines of Execute without the index
// refresh line, which depends on the run order.
func checkExitLines(stderr string) []string {
	var lines []string

	for _, line := range seamarkLines(stderr) {
		if line != checkLessonsRefreshLine {
			lines = append(lines, line)
		}
	}

	return lines
}

// checkLessonsEnvelope decodes stdout as exactly one check-lessons/1
// document. The policy and lessons members stay raw, so a test can
// compare them with the documents of plain check and lessons --verify.
type checkLessonsEnvelope struct {
	Schema   string                `json:"schema"`
	Policy   json.RawMessage       `json:"policy"`
	Lessons  json.RawMessage       `json:"lessons"`
	Combined report.CombinedResult `json:"combined"`
}

func decodeCheckLessons(t *testing.T, stdout string) checkLessonsEnvelope {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(stdout))

	var env checkLessonsEnvelope
	require.NoError(t, dec.Decode(&env), "stdout: %s", stdout)

	var extra json.RawMessage
	require.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout holds one JSON document only")

	return env
}

// withoutCheckedAt decodes a lesson-verification/1 document without its
// checked_at member. Two runs differ only there.
func withoutCheckedAt(t *testing.T, doc []byte) map[string]any {
	t.Helper()

	var m map[string]any
	require.NoError(t, json.Unmarshal(doc, &m), "document: %s", doc)
	require.Contains(t, m, "checked_at")

	delete(m, "checked_at")

	return m
}

// combinedLine is the last line of the text form of check --lessons.
func combinedLine(state, policy, lessons string) string {
	return "combined: " + state + " (policy " + policy + ", lessons " + lessons + ")\n"
}

// TestCheckLessonsExitTable proves the exit table of check --lessons
// through the real Execute, in a child process: 0 when the policy does
// not block and the lessons exit 0; 1 when the policy does not block
// and the lessons are incomplete; 2 when the policy blocks or a lesson
// check reports a violation; 1 for an error before the evaluation. A
// blocking policy keeps its stderr line. The JSON exit code equals the
// process exit code. The text form is the plain check text, a blank
// line, the lessons --verify text, and the combined line.
//
// The parent owns the sentinel environment, and its cleanup runs after
// every parallel child. A stub call in any child fails this table, but
// the failure names no child: every case appends to the same protected
// marker. The extra cases stay serial, because they cover the paths
// before the evaluation.
func TestCheckLessonsExitTable(t *testing.T) {
	sentinel := newInvokerEnvironmentForTable(t)
	if sentinel != nil {
		t.Cleanup(func() { sentinel.requireNoCall(t) })
	}

	cases := []struct {
		name    string
		edit    func(t *testing.T, root string)
		enforce bool
		code    int
		stderr  string // the Execute line; "" = none
		is      error  // the sentinel of the in-process error; nil = no error
		state   string // the combined state
		policy  string // the combined policy value
		lessons string // the lesson summary state
	}{
		{
			name: "policy warns, lessons passed",
			edit: checkLessonsEditHandler,
			code: 0, state: "pass", policy: "pass", lessons: "passed",
		},
		{
			name: "policy warns, no applicable checks",
			edit: func(t *testing.T, root string) { verifyWrite(t, root, "docs/readme.md", "readme\nmore\n") },
			code: 0, state: "pass", policy: "pass", lessons: "no-applicable-checks",
		},
		{
			name:   "policy warns, lessons incomplete",
			edit:   verifyAddBinary,
			code:   1,
			stderr: verifyUnknownLine,
			is:     lessoncheck.ErrIncomplete,
			state:  "incomplete", policy: "pass", lessons: "incomplete",
		},
		{
			name:   "policy warns, lesson violation",
			edit:   verifyEditAsset,
			code:   2,
			stderr: verifyViolationLine,
			is:     lessoncheck.ErrViolation,
			state:  "fail", policy: "pass", lessons: "violation",
		},
		{
			name:    "policy blocks, lessons passed",
			edit:    checkLessonsEditHandler,
			enforce: true,
			code:    2,
			stderr:  checkLessonsBlockedLine,
			is:      gate.ErrBlocked,
			state:   "fail", policy: "blocked", lessons: "passed",
		},
		{
			name: "policy blocks, lessons incomplete",
			edit: func(t *testing.T, root string) {
				checkLessonsEditHandler(t, root)
				verifyAddBinary(t, root)
			},
			enforce: true,
			code:    2,
			stderr:  checkLessonsBlockedLine,
			is:      gate.ErrBlocked,
			state:   "fail", policy: "blocked", lessons: "incomplete",
		},
		{
			name:    "policy blocks, lesson violation",
			edit:    verifyEditAsset,
			enforce: true,
			code:    2,
			stderr:  checkLessonsBlockedLine,
			is:      gate.ErrBlocked,
			state:   "fail", policy: "blocked", lessons: "violation",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := checkLessonsFixture(t, sentinel)
			c.edit(t, root)

			args := []string{"-C", root, "check", "--lessons"}
			plainArgs := []string{"-C", root, "check"}

			if c.enforce {
				args = append(args, "--enforce")
				plainArgs = append(plainArgs, "--enforce")
			}

			// The child process gets the real exit code and stderr line.
			// It enters through TestLessonsVerifyExitCodes, which runs
			// Execute with any arguments (seamarkProcess).
			stdout, stderr, code := seamarkProcess(t, root, args...)
			assert.Equal(t, c.code, code, "stdout: %s\nstderr: %s", stdout, stderr)

			if c.stderr == "" {
				assert.Empty(t, checkExitLines(stderr))
			} else {
				assert.Equal(t, []string{c.stderr}, checkExitLines(stderr), "one Execute line; the policy line wins")
			}

			want := combinedLine(c.state, c.policy, c.lessons)
			assert.True(t, strings.HasSuffix(stdout, want), "stdout ends with %q: %s", want, stdout)

			jsonOut, jsonErr, jsonCode := seamarkProcess(t, root, append(args, "--json")...)
			assert.Equal(t, c.code, jsonCode, "stderr: %s", jsonErr)
			assert.Equal(t, checkExitLines(stderr), checkExitLines(jsonErr))

			env := decodeCheckLessons(t, jsonOut)
			assert.Equal(t, report.CombinedResult{
				State:       lessoncheck.CombinedState(c.state),
				ExitCode:    jsonCode,
				Policy:      c.policy,
				Lessons:     lessoncheck.SummaryState(c.lessons),
				PolicyInput: report.PolicyInputGitDiffHead,
			}, env.Combined, "the process exits with combined.exit_code")

			// The text form: plain check, a blank line, the lesson
			// report of lessons --verify, and the combined line.
			plain, _, plainErr := runIn(t, "", plainArgs...)
			if c.enforce {
				require.ErrorIs(t, plainErr, gate.ErrBlocked)
			} else {
				require.NoError(t, plainErr)
			}

			lessonText, _, _ := runIn(t, "", "-C", root, "lessons", "--verify")

			got, _, err := runIn(t, "", args...)
			assert.Equal(t, plain+"\n"+lessonText+want, got)

			// The in-process run returns the error that Execute maps.
			if c.is == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, c.is)
			}

			if c.code != 2 {
				assert.NotErrorIs(t, err, lessoncheck.ErrViolation)
				assert.NotErrorIs(t, err, gate.ErrBlocked)
			}
		})
	}

	t.Run("a policy error comes before the evaluation", func(t *testing.T) {
		root := checkLessonsFixture(t, sentinel)
		verifyEditAsset(t, root)
		verifyWrite(t, root, ".seamark/policy.yaml", "mode: sometimes\n")

		for _, extra := range [][]string{nil, {"--json"}} {
			args := append([]string{"-C", root, "check", "--lessons"}, extra...)

			stdout, stderr, code := seamarkProcess(t, root, args...)
			assert.Equal(t, 1, code, "stderr: %s", stderr)
			assert.Empty(t, stdout, "no report and no JSON on an error")

			lines := checkExitLines(stderr)
			require.Len(t, lines, 1)
			assert.Contains(t, lines[0], "mode must be warn or enforce")
		}
	})

	t.Run("a directory outside git is refused before the policy path", func(t *testing.T) {
		dir := t.TempDir()

		for _, extra := range [][]string{nil, {"--json"}} {
			args := append([]string{"-C", dir, "check", "--lessons"}, extra...)

			stdout, stderr, code := seamarkProcess(t, dir, args...)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout)
			assert.Equal(t, []string{"seamark: lesson checks: " + dir + " is not inside a git work tree"}, seamarkLines(stderr))
		}

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "the refusal builds no index and writes no audit record")
	})
}

// TestCheckLessonsJSONEnvelope proves the check-lessons/1 document: the
// policy member is the exact object of plain check --json, the lessons
// member is the lessons --verify document, and combined comes from
// lessoncheck.Combine. The lesson checks read the working tree, so an
// untracked file in scope is evaluated although the piped diff omits
// it.
func TestCheckLessonsJSONEnvelope(t *testing.T) {
	sentinel := newInvokerEnvironment(t)
	root := checkLessonsFixture(t, sentinel)
	checkLessonsEditHandler(t, root)
	checkLessonsAddAsset(t, root)

	// git diff HEAD skips the untracked file, so the piped diff names
	// src/handler.go only.
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = root
	diff, err := cmd.Output()
	require.NoError(t, err)
	require.Contains(t, string(diff), "src/handler.go")
	require.NotContains(t, string(diff), "generated/new.txt")

	verifyJSON, _, err := runIn(t, "", "-C", root, "lessons", "--verify", "--json")
	require.ErrorIs(t, err, lessoncheck.ErrViolation)

	cases := []struct {
		name     string
		stdin    string
		enforce  bool
		combined report.CombinedResult
		is       error
	}{
		{
			name:  "piped diff",
			stdin: string(diff),
			combined: report.CombinedResult{State: lessoncheck.CombinedFail, ExitCode: 2, Policy: "pass",
				Lessons: lessoncheck.SummaryViolation, PolicyInput: report.PolicyInputStdinDiff},
			is: lessoncheck.ErrViolation,
		},
		{
			name: "git diff HEAD",
			combined: report.CombinedResult{State: lessoncheck.CombinedFail, ExitCode: 2, Policy: "pass",
				Lessons: lessoncheck.SummaryViolation, PolicyInput: report.PolicyInputGitDiffHead},
			is: lessoncheck.ErrViolation,
		},
		{
			name:    "piped diff under --enforce",
			stdin:   string(diff),
			enforce: true,
			combined: report.CombinedResult{State: lessoncheck.CombinedFail, ExitCode: 2, Policy: "blocked",
				Lessons: lessoncheck.SummaryViolation, PolicyInput: report.PolicyInputStdinDiff},
			is: gate.ErrBlocked,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var extra []string
			if c.enforce {
				extra = []string{"--enforce"}
			}

			plainJSON, _, _ := runIn(t, c.stdin, append([]string{"-C", root, "check", "--json"}, extra...)...)

			stdout, _, err := runIn(t, c.stdin, append([]string{"-C", root, "check", "--lessons", "--json"}, extra...)...)
			require.ErrorIs(t, err, c.is)

			assert.True(t, strings.HasPrefix(stdout, `{"schema":"check-lessons/1",`), "schema is the first key: %s", stdout)
			assert.True(t, strings.HasSuffix(stdout, "}\n"))

			env := decodeCheckLessons(t, stdout)
			assert.Equal(t, report.CheckLessonsSchema, env.Schema)
			assert.Equal(t, c.combined, env.Combined)

			// The policy member holds the bytes of plain check --json.
			assert.JSONEq(t, plainJSON, string(env.Policy))
			assert.Contains(t, stdout, `"policy":`+strings.TrimSuffix(plainJSON, "\n")+`,"lessons":`)

			// The lessons member is the lessons --verify document.
			assert.Equal(t, withoutCheckedAt(t, []byte(verifyJSON)), withoutCheckedAt(t, env.Lessons))

			var lessons lessoncheck.Verification
			require.NoError(t, json.Unmarshal(env.Lessons, &lessons))
			assert.Equal(t, lessoncheck.CallerCLI, lessons.Caller)
			assert.Equal(t, version, lessons.SeamarkVersion)
			assert.Equal(t, lessoncheck.BoundaryWorkingTree, lessons.Boundary)
			assert.Equal(t, lessoncheck.SelectionDefault, lessons.Selection.Kind, "the piped diff never narrows the selection")

			violation := resultOfRule(t, lessons, "frozen-assets")
			assert.Equal(t, lessoncheck.OutcomeViolation, violation.Outcome)
			require.NotEmpty(t, violation.Evidence)
			assert.Equal(t, "generated/new.txt", violation.Evidence[0].Path, "the untracked file outside the diff is evaluated")
			assert.Equal(t, lessoncheck.ChangeAdd, violation.Evidence[0].Change)

			assert.Equal(t, lessoncheck.OutcomePass, resultOfRule(t, lessons, "no-todo").Outcome)
		})
	}

	sentinel.requireNoCall(t)
}

// resultOfRule returns the one result of the pin with the given rule.
func resultOfRule(t *testing.T, v lessoncheck.Verification, rule string) lessoncheck.Result {
	t.Helper()

	for _, r := range v.Results {
		if r.Rule == rule {
			return r
		}
	}

	require.Failf(t, "no result", "rule %s", rule)

	return lessoncheck.Result{}
}

// TestCheckLessonsNamesOwnAudit proves that check --lessons shows the
// own-state exclusion. It runs the default selection, so its text and
// its lessons member name the audit files.
func TestCheckLessonsNamesOwnAudit(t *testing.T) {
	sentinel := newInvokerEnvironment(t)
	root := checkLessonsFixture(t, sentinel)
	checkLessonsEditHandler(t, root)

	stdout, _, err := runIn(t, "", "-C", root, "check", "--lessons")
	require.NoError(t, err)
	assert.Contains(t, stdout, "; "+ownAuditExclude+"\n")

	stdout, _, err = runIn(t, "", "-C", root, "check", "--lessons", "--json")
	require.NoError(t, err)

	var lessons lessoncheck.Verification
	require.NoError(t, json.Unmarshal(decodeCheckLessons(t, stdout).Lessons, &lessons))
	assert.Contains(t, lessons.Selection.Excludes, ownAuditExclude)

	sentinel.requireNoCall(t)
}

// TestCheckLessonsNeverInvokes proves that check --lessons calls no
// model: the text and JSON forms, a piped diff, and --enforce start no
// agent, no model CLI, and no network client.
func TestCheckLessonsNeverInvokes(t *testing.T) {
	sentinel := newInvokerEnvironment(t)
	root := checkLessonsFixture(t, sentinel)
	verifyEditAsset(t, root)
	checkLessonsAddAsset(t, root)
	verifyAddTODO(t, root)

	for _, c := range []struct {
		stdin string
		args  []string
	}{
		{},
		{args: []string{"--json"}},
		{stdin: checkLegacyDiff},
		{stdin: checkLegacyDiff, args: []string{"--json", "--enforce"}},
		{args: []string{"--enforce"}},
	} {
		stdout, _, err := runIn(t, c.stdin, append([]string{"-C", root, "check", "--lessons"}, c.args...)...)
		require.Error(t, err, "the fixture holds a violation")
		assert.NotEmpty(t, stdout)
	}

	sentinel.requireNoCall(t)
}

// TestCheckLessonsStaleResult proves that check --lessons keeps the
// stale result of the verification. The policy does not block here, so
// the combined state and the exit code come from the lessons alone.
func TestCheckLessonsStaleResult(t *testing.T) {
	for _, c := range staleCases {
		t.Run(c.name, func(t *testing.T) {
			sentinel := newInvokerEnvironment(t)
			root := checkLessonsFixture(t, sentinel)
			c.edit(t, root)

			reset := lateChange(t, root)

			stdout, stderr, code := seamarkProcess(t, root, "-C", root, "check", "--lessons")
			assert.Equal(t, c.code, code, "stdout: %s\nstderr: %s", stdout, stderr)
			assert.Equal(t, []string{c.stderr}, checkExitLines(stderr))
			assert.Contains(t, stdout, "\n  stale: the inputs changed after the read")
			assert.True(t, strings.HasSuffix(stdout, combinedLine(c.combined, "pass", string(c.state))), stdout)

			reset()

			jsonOut, _, jsonCode := seamarkProcess(t, root, "-C", root, "check", "--lessons", "--json")
			assert.Equal(t, c.code, jsonCode)

			env := decodeCheckLessons(t, jsonOut)
			assert.Equal(t, c.code, env.Combined.ExitCode)
			assert.Equal(t, c.state, env.Combined.Lessons)

			var v lessoncheck.Verification
			require.NoError(t, json.Unmarshal(env.Lessons, &v))
			assert.Equal(t, lessoncheck.FreshnessStale, v.Freshness)
			assert.Equal(t, c.outcomes, outcomeWords(v.Results))

			sentinel.requireNoCall(t)
		})
	}
}
