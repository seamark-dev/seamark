package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// checkLegacyDiff is the diff argument of the legacy MCP check goldens.
// Its first part equals `git diff HEAD` in checkLegacyFixture: it
// changes the body of store.Save, which reaches fs:write. Its second
// part adds store/doc.go, which holds no symbol. The working tree has
// that file untracked, so only the diff argument names it.
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

// checkLegacyFixture builds a repository whose check text holds every
// legacy part: an effect, two policy matches, an unindexed-file note, a
// companion from history, and a pinned lesson. The working tree holds
// the change of checkLegacyDiff. store/save.go is modified, and
// store/doc.go is untracked, so `git diff HEAD` leaves doc.go out.
//
// The CLI test package holds the same fixture. Test helpers cannot cross
// package boundaries, and one repository in both packages lets the two
// goldens be compared by eye.
func checkLegacyFixture(t *testing.T) string {
	t.Helper()

	// The server runs git itself. A global or system git configuration,
	// for example diff prefixes or colors, must not change the diff that
	// check reads.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

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

// TestCheckMCPGitDiffHeadIgnoresDiffConfig covers the git diff HEAD
// fallback of the MCP check tool and the companion hint, which reads git
// log. A repository config that changes the git text must not change the
// decision or the text. Without the fix, color.ui=always gave allow for a
// change that needs approval, and it dropped the "mostly Load" line.
func TestCheckMCPGitDiffHeadIgnoresDiffConfig(t *testing.T) {
	const decision = "verdict  require_approval (mode: warn)\n" +
		"effects  [fs:write]\n" +
		"  [require_approval] store-writes: the change reaches file writes"

	root := checkLegacyFixture(t)

	want, isErr := callCheck(t, root, "{}")
	require.False(t, isErr)
	require.Equal(t, decision, strings.SplitN(want, "\n\n", 2)[0])
	require.Contains(t, want, "    mostly Load\n")

	fixtureGit(t, root, "config", "color.ui", "always")

	got, isErr := callCheck(t, root, "{}")
	assert.False(t, isErr)
	assert.Equal(t, want, got)
}

// TestCheckLessonsMCPOptionAbsentKeepsText freezes the text of the MCP
// check tool as it was before the lesson_checks option existed, because
// a call without the option must keep its text. Three argument shapes
// are covered: an empty object and no arguments, which both fall back
// to `git diff HEAD`, and a diff argument. Only content[0].text is
// frozen. The tool schema in tools/list is not frozen, because it lists
// the option.
func TestCheckLessonsMCPOptionAbsentKeepsText(t *testing.T) {
	const (
		diffText = `verdict  require_approval (mode: warn)
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
		headText = `verdict  require_approval (mode: warn)
effects  [fs:write]
  [require_approval] store-writes: the change reaches file writes

history suggests also reviewing  (usually changes with the diff's files, absent from this diff)
  store/load.go                                      3 shared commits with store/save.go, lift 1.7
    mostly Load

advisory — recurring lessons for touched files (not part of the verdict)
- [pin · store] store-owner-only — Write store files with owner-only modes.
`
	)

	root := checkLegacyFixture(t)

	// json.Marshal escapes the diff's newlines, tabs, and quotes for
	// the one-line JSON-RPC frame.
	withDiff, err := json.Marshal(map[string]string{"diff": checkLegacyDiff})
	require.NoError(t, err)

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"check"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"check","arguments":`+string(withDiff)+`}}`,
	)

	for _, c := range []struct{ id, want string }{
		{id: "1", want: headText},
		{id: "2", want: headText},
		{id: "3", want: diffText},
	} {
		text, isErr := toolText(t, resps[c.id])
		assert.False(t, isErr, "request %s", c.id)
		assert.Equal(t, c.want, text, "request %s", c.id)
	}
}

// The tests of the check option lesson_checks. Every test builds a
// temporary repository with the lessons of lessonChecksYAML and runs
// the server under the invoker sentinel. Nothing reads the repository
// of this checkout.

// lessonChecksYAML arms two pins. frozen-assets forbids every change
// under generated/ (enforce), and no-todo forbids an added TODO( line
// under src/ (check). The third pin stays advisory. The CLI tests of
// lessons --verify use the same file.
const lessonChecksYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: no-todo
    region: src/
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: prefer-errors-is
    region: src/
    note: Compare errors with errors.Is.
`

// lessonChecksPolicy asks for approval of every change in the given
// mode. In warn mode it never blocks. In enforce mode it blocks every
// diff that names a file.
func lessonChecksPolicy(mode string) string {
	return "mode: " + mode + "\nrequire_approval:\n" +
		"  - id: any-change\n" +
		"    when: 'size(diff.files) > 0'\n" +
		"    message: the change touches files\n"
}

// lessonChecksFixture builds a committed repository under a new
// temporary directory: lessonChecksYAML, the policy in policyMode, a Go
// file under src/, an asset under generated/, and the sentinel. The
// server writes its index and audit files under .seamark. The
// .gitignore keeps them out of the working-tree snapshot, because a
// lesson result must not depend on them. It returns the git top level.
func lessonChecksFixture(t *testing.T, policyMode string) (root string, sentinel *invokerSentinel) {
	t.Helper()

	// The server and the fixture run git. A global or system git
	// configuration must not change what they see.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := t.TempDir()

	fixtureGit(t, dir, "init", "-q", "-b", "main")

	fixtureWrite(t, dir, ".gitignore",
		".seamark/*\n!.seamark/lessons.yaml\n!.seamark/policy.yaml\n!.seamark/config.yaml\n")
	fixtureWrite(t, dir, "go.mod", "module example.com/lessonchecks\n")
	fixtureWrite(t, dir, "generated/a.txt", "asset\n")
	fixtureWrite(t, dir, "src/handler.go", "package src\n\nfunc Handle() {}\n")
	fixtureWrite(t, dir, "docs/readme.md", "readme\n")
	fixtureWrite(t, dir, ".seamark/lessons.yaml", lessonChecksYAML)
	fixtureWrite(t, dir, ".seamark/policy.yaml", lessonChecksPolicy(policyMode))

	// The sentinel adds .seamark/config.yaml. The commit keeps it out
	// of the working-tree changes.
	sentinel = newInvokerSentinel(t, dir)

	fixtureGit(t, dir, "add", "-A")
	fixtureGit(t, dir, "commit", "-q", "-m", "Add the fixture")

	// The rerun line names the top level that git reports, which is dir
	// with its symbolic links resolved.
	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return root, sentinel
}

// fixtureGit runs git in dir with a fixed identity.
func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.Output()
	require.NoError(t, err, "git %v", args)

	return string(out)
}

// fixtureWrite writes one file under dir and creates its parents.
func fixtureWrite(t *testing.T, dir, rel, content string) {
	t.Helper()

	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// The working-tree edits of the tests. A comment in src/handler.go
// passes no-todo. A changed or a new file under generated/ is a change
// that frozen-assets forbids. A binary file under src/ leaves no-todo
// undecided. A TODO( line under src/ is a no-todo violation.
func editHandler(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "src/handler.go", "package src\n\n// Handle serves.\nfunc Handle() {}\n")
}

func editReadme(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "docs/readme.md", "readme\nmore\n")
}

func editAsset(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "generated/a.txt", "asset\nchanged\n")
}

func addAsset(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "generated/new.txt", "new asset\n")
}

func addBinary(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "src/logo.bin", "\x00\x01\x02PNG\x00")
}

func addTODO(t *testing.T, root string) {
	t.Helper()
	fixtureWrite(t, root, "src/handler.go", "package src\n\n// TODO(owner): split.\nfunc Handle() {}\n")
}

// callCheck runs one check call with the JSON arguments args on a new
// server at root. It returns the text and the isError flag.
func callCheck(t *testing.T, root, args string) (text string, isErr bool) {
	t.Helper()

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check","arguments":`+args+`}}`)

	return toolText(t, resps["1"])
}

// checkArgs encodes the check arguments as one JSON object.
func checkArgs(t *testing.T, args map[string]any) string {
	t.Helper()

	data, err := json.Marshal(args)
	require.NoError(t, err)

	return string(data)
}

// lessonReport returns the lesson report of root as the server renders
// it. The test runs its own verification after the server call. The
// working tree is the same, and the text holds no time, so both reports
// are equal.
func lessonReport(t *testing.T, root string) string {
	t.Helper()

	v, err := lessoncheck.Verify(context.Background(), lessoncheck.VerifyRequest{
		Root:           root,
		Caller:         lessoncheck.CallerMCP,
		SeamarkVersion: "test",
	})
	require.NoError(t, err)

	var b strings.Builder
	lessoncheck.RenderText(&b, v)

	return b.String()
}

// mcpCombinedLine is the first line of the check text with
// lesson_checks. policy is the clause after "policy ". A clause that
// names a verdict that does not block holds a comma, so a semicolon
// ends it.
func mcpCombinedLine(state, policy, lessons string, exit int) string {
	sep := ", "
	if strings.Contains(policy, ",") {
		sep = "; "
	}

	return "combined: " + state + " (policy " + policy + sep + "lessons " + lessons +
		"; CLI exit " + strconv.Itoa(exit) + ")\n"
}

// The policy clauses of the combined line in the fixture.
// lessonChecksPolicy asks for approval of every diff that names a file,
// so a warn-mode call over a change does not pass the policy, and the
// clause says so.
const (
	mcpClauseApprovalWarn = "require_approval, not blocking in warn mode"
	mcpClauseAllow        = "pass"
	mcpClauseBlocked      = "blocked"
)

// mcpNotRequestedOnePin is the line that ends the check text without
// lesson_checks when one executable pin covers a changed path.
const mcpNotRequestedOnePin = "lesson checks: not requested — 1 executable pin covers this diff; " +
	"run with lesson_checks: true (CLI: seamark check --lessons)\n"

// withoutNotRequested returns the check text without lesson_checks minus
// its last line on lesson checks that did not run. That text is the body
// of a call with lesson_checks.
func withoutNotRequested(t *testing.T, text string, want bool) string {
	t.Helper()

	if !want {
		assert.NotContains(t, text, "lesson checks:", "no executable pin covers the diff")

		return text
	}

	require.True(t, strings.HasSuffix(text, "\n"+mcpNotRequestedOnePin), "the text names the pin: %s", text)

	return strings.TrimSuffix(text, "\n"+mcpNotRequestedOnePin)
}

// TestCheckLessonsMCPOption proves the lesson_checks option of the MCP
// check tool. False and absent give the same text, which ends with the
// line on lesson checks that did not run when an executable pin covers
// a changed path. True gives the combined line with the CLI exit code,
// a blank line, that text without the line, a blank line, and the
// lesson report. Every verdict stays in band. The lesson checks read
// the working tree whatever diff the policy reads. Malformed arguments
// and a root outside git are refused with their usage and root texts.
func TestCheckLessonsMCPOption(t *testing.T) {
	t.Run("the schema declares the option", func(t *testing.T) {
		root := writeFixture(t)

		resps := serve(t, root, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		require.Nil(t, resps["1"].Error)

		// The schemas of the other tools have other shapes, so only the
		// check schema is decoded field by field.
		var list struct {
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		require.NoError(t, json.Unmarshal(resps["1"].Result, &list))

		var schema json.RawMessage

		for _, tool := range list.Tools {
			if tool.Name == "check" {
				schema = tool.InputSchema
			}
		}

		require.NotNil(t, schema, "tools/list holds check")

		var check struct {
			Properties map[string]map[string]string `json:"properties"`
			Required   []string                     `json:"required"`
		}
		require.NoError(t, json.Unmarshal(schema, &check))

		assert.Equal(t, map[string]string{
			"type":        "boolean",
			"description": "also verify the working tree against executable lesson checks (lesson-verification/1); default false",
		}, check.Properties["lesson_checks"])
		assert.Contains(t, check.Properties, "diff")
		assert.Empty(t, check.Required, "both arguments stay optional")
	})

	cases := []struct {
		name    string
		policy  string // the policy mode
		edit    func(t *testing.T, root string)
		state   string // the combined state
		clause  string // the policy clause of the combined line
		lessons string // the lesson summary state
		exit    int    // the exit code of check --lessons
		// notRequested is true when the text without lesson_checks ends
		// with the line on lesson checks that did not run.
		notRequested bool
	}{
		{
			name: "lessons passed", policy: "warn", edit: editHandler,
			state: "pass", clause: mcpClauseApprovalWarn, lessons: "passed", exit: 0, notRequested: true,
		},
		{
			name: "no applicable checks", policy: "warn", edit: editReadme,
			state: "pass", clause: mcpClauseApprovalWarn, lessons: "no-applicable-checks", exit: 0,
		},
		{
			// git diff HEAD skips the untracked binary, so the policy reads
			// no file and allows.
			name: "lessons incomplete", policy: "warn", edit: addBinary,
			state: "incomplete", clause: mcpClauseAllow, lessons: "incomplete", exit: 1,
		},
		{
			name: "lesson violation", policy: "warn", edit: editAsset,
			state: "fail", clause: mcpClauseApprovalWarn, lessons: "violation", exit: 2, notRequested: true,
		},
		{
			name: "policy blocks, lessons incomplete", policy: "enforce",
			edit: func(t *testing.T, root string) {
				editHandler(t, root)
				addBinary(t, root)
			},
			state: "fail", clause: mcpClauseBlocked, lessons: "incomplete", exit: 2, notRequested: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := lessonChecksFixture(t, c.policy)
			c.edit(t, root)

			legacy, isErr := callCheck(t, root, `{}`)
			require.False(t, isErr, legacy)

			// False is the default: the text stays byte-identical.
			off, isErr := callCheck(t, root, `{"lesson_checks":false}`)
			assert.False(t, isErr)
			assert.Equal(t, legacy, off)

			got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
			assert.False(t, isErr, "a violation and a block stay in band: %s", got)

			lessons := lessonReport(t, root)
			require.True(t, strings.HasPrefix(lessons, "lesson checks: "+c.lessons+"\n"), lessons)

			body := withoutNotRequested(t, legacy, c.notRequested)
			want := mcpCombinedLine(c.state, c.clause, c.lessons, c.exit) + "\n" + body + "\n" + lessons
			assert.Equal(t, want, got)

			sentinel.requireNoCall(t)
		})
	}

	t.Run("a diff argument never narrows the lesson selection", func(t *testing.T) {
		root, sentinel := lessonChecksFixture(t, "warn")
		editHandler(t, root)
		addAsset(t, root)

		// git diff HEAD skips the untracked asset, so the diff argument
		// names src/handler.go only.
		diff := fixtureGit(t, root, "diff", "HEAD")
		require.Contains(t, diff, "src/handler.go")
		require.NotContains(t, diff, "generated/new.txt")

		legacy, isErr := callCheck(t, root, checkArgs(t, map[string]any{"diff": diff}))
		require.False(t, isErr, legacy)

		off, isErr := callCheck(t, root, checkArgs(t, map[string]any{"diff": diff, "lesson_checks": false}))
		assert.False(t, isErr)
		assert.Equal(t, legacy, off)

		got, isErr := callCheck(t, root, checkArgs(t, map[string]any{"diff": diff, "lesson_checks": true}))
		assert.False(t, isErr)

		lessons := lessonReport(t, root)
		assert.Contains(t, lessons, "add generated/new.txt", "the untracked file outside the diff is evaluated")

		// Every path of the diff is in the snapshot, so no note follows
		// the combined line.
		body := withoutNotRequested(t, legacy, true)
		assert.Equal(t, mcpCombinedLine("fail", mcpClauseApprovalWarn, "violation", 2)+"\n"+body+"\n"+lessons, got)

		sentinel.requireNoCall(t)
	})

	t.Run("the text names the own audit files", func(t *testing.T) {
		root, sentinel := lessonChecksFixture(t, "warn")
		editHandler(t, root)

		got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
		assert.False(t, isErr, got)
		assert.Contains(t, got, "; seamark's own lesson-check audit files\n")

		sentinel.requireNoCall(t)
	})

	t.Run("malformed arguments get the usage text", func(t *testing.T) {
		root := writeFixture(t)

		for _, args := range []string{`{"lesson_checks":"yes"}`, `{"lesson_checks":1}`, `{"diff":42}`, `[]`} {
			text, isErr := callCheck(t, root, args)
			assert.True(t, isErr, args)
			assert.Equal(t, `check takes {"diff": "<unified diff>", "lesson_checks": true|false} (both optional)`, text, args)
		}
	})

	t.Run("a root outside git is refused before the policy path", func(t *testing.T) {
		dir := t.TempDir()

		text, isErr := callCheck(t, dir, checkArgs(t, map[string]any{"diff": checkLegacyDiff, "lesson_checks": true}))
		assert.True(t, isErr)
		assert.Equal(t, "lesson checks: "+dir+" is not inside a git work tree", text)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "the refusal builds no index and writes no audit record")
	})
}

// TestCheckLessonsMCPNeverInvokes proves that the check option calls no
// model: the git fallback, a diff argument, and the option off start no
// agent, no model CLI, and no network client.
func TestCheckLessonsMCPNeverInvokes(t *testing.T) {
	root, sentinel := lessonChecksFixture(t, "warn")
	editAsset(t, root)
	addAsset(t, root)
	addTODO(t, root)
	addBinary(t, root)

	for _, args := range []string{
		`{"lesson_checks":true}`,
		checkArgs(t, map[string]any{"diff": checkLegacyDiff, "lesson_checks": true}),
		`{"lesson_checks":false}`,
		`{}`,
	} {
		text, isErr := callCheck(t, root, args)
		assert.False(t, isErr, "%s: %s", args, text)
		assert.NotEmpty(t, text)
	}

	sentinel.requireNoCall(t)
}

// This part holds the invoker sentinel of the MCP server. The CLI tests
// hold the same sentinel, because test helpers cannot cross package
// boundaries. The sentinel has three parts:
//   - agent.argv in .seamark/config.yaml names a stub script;
//   - stubs named claude, codex, and gh come first on PATH;
//   - the proxy variables name a closed local port.
//
// Each stub appends its name to one marker file and exits with
// sentinelExit. git stays on PATH, because the server reads the
// repository through git. The proxy part is no proof inside the test
// process, because Go caches the proxy settings after the first read.
// It catches a child process that reaches the network.

// sentinelExit is the exit code of every stub. No seamark path exits
// with it, so a stub that runs makes the surface fail visibly.
const sentinelExit = 97

// invokerSentinel records every start of an agent or of a tool that can
// call a model.
type invokerSentinel struct {
	marker string // each stub appends its name here
}

// newInvokerSentinel installs the sentinel for root and for the rest of
// the test. It adds an agent section to .seamark/config.yaml and keeps
// the other content. The test must not run in parallel, because the
// sentinel sets environment variables.
func newInvokerSentinel(t *testing.T, root string) *invokerSentinel {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the sentinel stubs are shell scripts")
	}

	// The stubs and the marker live outside root, so they never appear
	// as untracked files in a working-tree snapshot of root.
	dir := t.TempDir()
	s := &invokerSentinel{marker: filepath.Join(dir, "calls.txt")}

	agentStub := filepath.Join(dir, "agent.sh")
	s.writeStub(t, agentStub, "agent.argv")
	sentinelAgentConfig(t, root, agentStub)

	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))

	for _, name := range []string{"claude", "codex", "gh"} {
		s.writeStub(t, filepath.Join(bin, name), name)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	proxy := closedLocalURL(t)
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy)
	}

	// A NO_PROXY entry would let a request pass the proxy.
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}

	return s
}

// requireNoCall fails the test when any stub ran.
func (s *invokerSentinel) requireNoCall(t *testing.T) {
	t.Helper()

	data, err := os.ReadFile(s.marker)
	if errors.Is(err, os.ErrNotExist) {
		return
	}

	require.NoError(t, err)
	require.Empty(t, string(data), "a model or network stub ran; the marker names it")
}

// writeStub writes one stub script. It appends name to the marker and
// exits with sentinelExit.
func (s *invokerSentinel) writeStub(t *testing.T, path, name string) {
	t.Helper()

	body := "#!/bin/sh\necho " + shellQuote(name) + " >> " + shellQuote(s.marker) + "\n" +
		"exit " + strconv.Itoa(sentinelExit) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sentinelAgentConfig appends an agent section with argv = [sh, stub]
// to .seamark/config.yaml. The fixtures of this file write no
// config.yaml before the sentinel, so no second agent key can exist.
func sentinelAgentConfig(t *testing.T, root, stub string) {
	t.Helper()

	path := filepath.Join(root, ".seamark", "config.yaml")

	data, err := os.ReadFile(path)
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
	}

	content := string(data)
	require.NotContains(t, content, "agent:", "the sentinel owns the agent section of config.yaml")

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	content += "agent:\n  argv: [\"sh\", " + strconv.Quote(stub) + "]\n"

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// closedLocalURL returns an http URL of a local port that no process
// listens on. A connection to it fails at once.
func closedLocalURL(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return "http://" + addr
}

// lateChange puts a git wrapper first on PATH for the rest of the test.
// A verification reads the status twice: once for the snapshot, and once
// to revalidate it. The wrapper adds an untracked file under src/ just
// before the second read, so an input changes after the read and before
// the publication. The hook for that moment is private to lessoncheck,
// and this wrapper needs no change of the server.
func lateChange(t *testing.T, root string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a shell script")
	}

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	count := shellQuote(filepath.Join(dir, "count"))
	late := shellQuote(filepath.Join(root, "src", "late.go"))

	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"*--ignored=no*)\n" +
		"  n=$(cat " + count + " 2>/dev/null || echo 0)\n" +
		"  n=$((n + 1))\n" +
		"  echo \"$n\" > " + count + "\n" +
		"  if [ \"$n\" = 2 ]; then echo 'package src' > " + late + "; fi\n" +
		"  ;;\n" +
		"esac\n" +
		"exec " + shellQuote(gitPath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))

	// macOS can scan a new executable on its first run. This run takes
	// that time before a verification starts its deadline.
	out, err := exec.Command(filepath.Join(dir, "git"), "--version").CombinedOutput()
	require.NoError(t, err, "%s", out)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCheckLessonsMCPStaleResult proves that a stale verification stays
// in band. The agent must read the stale line and the exit code that the
// CLI gives, because a tool error would hide the report. A stale result
// publishes no pass, and a violation that the run observed stays.
func TestCheckLessonsMCPStaleResult(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(t *testing.T, root string)
		frozen  string // the outcome of the frozen-assets row
		state   string
		lessons string
		exit    int
	}{
		{
			name:   "a pass reads unknown",
			edit:   editHandler,
			frozen: "unknown/stale-snapshot",
			state:  "incomplete", lessons: "incomplete", exit: 1,
		},
		{
			name:   "an observed violation stays",
			edit:   editAsset,
			frozen: "violation",
			state:  "fail", lessons: "violation", exit: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := lessonChecksFixture(t, "warn")
			c.edit(t, root)
			lateChange(t, root)

			got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
			assert.False(t, isErr, "a stale result stays in band: %s", got)

			assert.Contains(t, got, "lesson checks: "+c.lessons+"\n")
			assert.Contains(t, got, ", stale\n")
			assert.Contains(t, got, "\n  stale: the inputs changed after the read")
			assert.True(t, strings.HasPrefix(got, mcpCombinedLine(c.state, mcpClauseApprovalWarn, c.lessons, c.exit)), got)

			assert.Regexp(t, `(?m)^  `+c.frozen+`  pin [0-9a-f]+ frozen-assets `, got)
			assert.Regexp(t, `(?m)^  unknown/stale-snapshot  pin [0-9a-f]+ no-todo `, got)

			sentinel.requireNoCall(t)
		})
	}
}

// commitMCPPolicy replaces the committed policy of root, so the policy
// file is no change of the working tree.
func commitMCPPolicy(t *testing.T, root, policy string) {
	t.Helper()

	fixtureWrite(t, root, ".seamark/policy.yaml", policy)
	fixtureGit(t, root, "add", ".seamark/policy.yaml")
	fixtureGit(t, root, "commit", "-q", "-m", "Change the policy")
}

// mcpDenyPolicy denies every diff that names a file, in the given mode.
func mcpDenyPolicy(mode string) string {
	return "mode: " + mode + "\ndeny:\n" +
		"  - id: no-change\n" +
		"    when: 'size(diff.files) > 0'\n" +
		"    message: the change touches files\n"
}

// TestCheckLessonsMCPNamesANonBlockingVerdict proves that the combined
// line of the MCP check names a deny or require_approval verdict that
// warn mode does not block. Before the fix it read "policy pass" under
// "verdict  deny (mode: warn)". The combined state and the CLI exit
// code stay: warn mode never blocks. Enforce mode keeps "policy
// blocked".
func TestCheckLessonsMCPNamesANonBlockingVerdict(t *testing.T) {
	cases := []struct {
		name   string
		policy string // "" keeps the require_approval policy of the fixture
		mode   string // the fixture policy mode
		edit   func(t *testing.T, root string)
		line   string
	}{
		{
			name: "deny in warn mode, lessons passed", policy: mcpDenyPolicy("warn"), mode: "warn", edit: editHandler,
			line: "combined: pass (policy deny, not blocking in warn mode; lessons passed; CLI exit 0)\n",
		},
		{
			name: "deny in warn mode, lesson violation", policy: mcpDenyPolicy("warn"), mode: "warn", edit: editAsset,
			line: "combined: fail (policy deny, not blocking in warn mode; lessons violation; CLI exit 2)\n",
		},
		{
			name: "require_approval in warn mode, lessons passed", mode: "warn", edit: editHandler,
			line: "combined: pass (policy require_approval, not blocking in warn mode; lessons passed; CLI exit 0)\n",
		},
		{
			name: "require_approval in warn mode, lesson violation", mode: "warn", edit: editAsset,
			line: "combined: fail (policy require_approval, not blocking in warn mode; lessons violation; CLI exit 2)\n",
		},
		{
			name: "deny in enforce mode", policy: mcpDenyPolicy("enforce"), mode: "enforce", edit: editHandler,
			line: "combined: fail (policy blocked, lessons passed; CLI exit 2)\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := lessonChecksFixture(t, c.mode)
			if c.policy != "" {
				commitMCPPolicy(t, root, c.policy)
			}

			c.edit(t, root)

			got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
			assert.False(t, isErr, got)
			assert.True(t, strings.HasPrefix(got, c.line), "the text starts with %q: %s", c.line, got)
			assert.Equal(t, 1, strings.Count(got, "combined: "), "the combined line is not repeated")

			sentinel.requireNoCall(t)
		})
	}
}

// TestCheckMCPNotesAFailedAudit proves that the MCP check names a
// failed audit append in its text, one note per record. Before the fix
// only the server log named a lost policy audit record, and nothing
// named a lost lesson-check audit record. The verdict does not change,
// and nothing goes through the link.
func TestCheckMCPNotesAFailedAudit(t *testing.T) {
	link := func(t *testing.T, path string) (target string) {
		t.Helper()

		target = filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))
		require.NoError(t, os.Symlink(target, path))

		return target
	}

	kept := func(t *testing.T, target string) {
		t.Helper()

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data), "nothing goes through the link")
	}

	t.Run("the policy audit record", func(t *testing.T) {
		root, sentinel := lessonChecksFixture(t, "warn")
		editHandler(t, root)

		audit := filepath.Join(root, ".seamark", "audit.jsonl")
		target := link(t, audit)

		note := "note: the audit record of this verdict was not written (gate: audit: " + audit +
			" is a symlink — refusing to follow it)\n"

		plain, isErr := callCheck(t, root, `{}`)
		assert.False(t, isErr, plain)
		assert.True(t, strings.HasPrefix(plain, "verdict  require_approval (mode: warn)\n"), plain)
		assert.True(t, strings.HasSuffix(plain, "\n\n"+note+mcpNotRequestedOnePin), "the note precedes the last line: %s", plain)

		got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
		assert.False(t, isErr, got)
		assert.True(t, strings.HasPrefix(got, mcpCombinedLine("pass", mcpClauseApprovalWarn, "passed", 0)), got)
		assert.True(t, strings.HasSuffix(got, "\n\n"+note), "the note ends the text: %s", got)
		assert.Equal(t, 1, strings.Count(got, "note: the audit record"))

		kept(t, target)
		sentinel.requireNoCall(t)
	})

	t.Run("the lesson-check audit record", func(t *testing.T) {
		root, sentinel := lessonChecksFixture(t, "warn")
		editHandler(t, root)

		audit := filepath.Join(root, ".seamark", "lesson-check-audit.jsonl")
		target := link(t, audit)

		got, isErr := callCheck(t, root, `{"lesson_checks":true}`)
		assert.False(t, isErr, got)
		assert.True(t, strings.HasPrefix(got, mcpCombinedLine("pass", mcpClauseApprovalWarn, "passed", 0)), got)
		assert.True(t, strings.HasSuffix(got,
			"\n\nnote: the lesson-check audit record was not written (unsafe path: "+audit+" is a symbolic link)\n"), got)
		assert.NotContains(t, got, "note: the audit record of this verdict")

		kept(t, target)
		sentinel.requireNoCall(t)
	})
}

// TestCheckMCPNamesLessonChecksNotRequested proves the last line of the
// MCP check without lesson_checks: when an executable pin covers a
// changed path, the text says that its check exists and did not run,
// and names the option. Before the fix the advisory listed the pin and
// nothing more. No covering pin keeps the text without the line, and an
// invalid lessons.yaml says so instead of a false "none".
func TestCheckMCPNamesLessonChecksNotRequested(t *testing.T) {
	const invalid = "lesson checks: not requested — lessons.yaml is invalid, so whether an executable pin " +
		"covers this diff is unknown; run with lesson_checks: true (CLI: seamark check --lessons)\n"

	cases := []struct {
		name string
		edit func(t *testing.T, root string)
		want string // the last line; "" = no line
	}{
		{name: "one covering pin", edit: editHandler, want: mcpNotRequestedOnePin},
		{name: "no covering pin", edit: editReadme},
		{name: "an invalid lessons.yaml", edit: func(t *testing.T, root string) {
			editHandler(t, root)
			fixtureWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")
		}, want: invalid},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := lessonChecksFixture(t, "warn")
			c.edit(t, root)

			got, isErr := callCheck(t, root, `{}`)
			assert.False(t, isErr, got)

			if c.want == "" {
				assert.NotContains(t, got, "lesson checks:")
			} else {
				assert.True(t, strings.HasSuffix(got, "\n\n"+c.want), "the text ends with a blank line and %q: %s", c.want, got)
				assert.Equal(t, 1, strings.Count(got, "lesson checks:"), "one line")
			}

			sentinel.requireNoCall(t)
		})
	}
}

// TestCheckLessonsMCPNamesDiffPathsOutsideSnapshot proves the note on a
// diff argument that the lesson checks did not read. A diff over a clean
// tree passed with no word that the checks read the working tree. The
// note is the second line, after the combined line.
func TestCheckLessonsMCPNamesDiffPathsOutsideSnapshot(t *testing.T) {
	const todoDiff = `diff --git a/src/todo.go b/src/todo.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/src/todo.go
@@ -0,0 +1,3 @@
+package src
+
+// TODO(owner): split.
`

	root, sentinel := lessonChecksFixture(t, "warn")

	got, isErr := callCheck(t, root, checkArgs(t, map[string]any{"diff": todoDiff, "lesson_checks": true}))
	assert.False(t, isErr, got)

	lines := strings.SplitN(got, "\n", 4)
	require.Len(t, lines, 4)
	assert.Equal(t, strings.TrimSuffix(mcpCombinedLine("pass", mcpClauseApprovalWarn, "no-applicable-checks", 0), "\n"), lines[0])
	assert.Equal(t, "note: the lesson checks read the working tree, not the supplied diff; "+
		"1 file of the diff is not in the verified snapshot (src/todo.go)", lines[1])
	assert.Empty(t, lines[2], "a blank line follows the head")

	// Without a diff argument the policy reads git diff HEAD, the tree
	// that the checks read, so no note follows.
	head, isErr := callCheck(t, root, `{"lesson_checks":true}`)
	assert.False(t, isErr, head)
	assert.NotContains(t, head, "note: the lesson checks read")

	sentinel.requireNoCall(t)
}
