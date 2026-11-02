package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests of lessons --verify (plan step P1.9; contracts K4, K5, §1.3,
// §4). Every test builds a temporary repository and runs the command
// under the AC-1 sentinel. Nothing reads the repository of this
// checkout.

// verifyLessonsYAML arms two pins. frozen-assets forbids every change
// under generated/ (enforce), and no-todo forbids an added TODO( line
// under src/ (check). The third pin stays advisory.
const verifyLessonsYAML = `lesson_checks_version: 1
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

// The exact stderr lines of contracts K5 for the fixture.
const (
	verifyViolationLine = "seamark: lesson check violation: 1 of 2 evaluated pins (see the report)"
	verifyUnknownLine   = "seamark: lesson checks incomplete: 1 unknown; rerun seamark lessons --verify"
)

// verifyFixture builds a committed repository under dir with
// verifyLessonsYAML, the sentinel, and a .gitignore that ignores
// generated/*.log. The .gitignore also holds the .seamark carve-outs
// that seamark init writes. Since milestone P4, every verification
// appends a record to .seamark/lesson-check-audit.jsonl (contract K5),
// and a later run must not see that file as a change. It returns the
// git top level, which is dir with its symbolic links resolved, because
// the rerun commands name that path.
func verifyFixture(t *testing.T, dir string) (root string, sentinel *invokerSentinel) {
	t.Helper()

	// The command and the fixture run git. A global or system git
	// configuration must not change what they see.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	verifyGit(t, dir, "init", "-q", "-b", "main")

	verifyWrite(t, dir, ".gitignore",
		"generated/*.log\n.seamark/*\n!.seamark/lessons.yaml\n!.seamark/config.yaml\n")
	verifyWrite(t, dir, "generated/a.txt", "asset\n")
	verifyWrite(t, dir, "src/handler.go", "package src\n\nfunc Handle() {}\n")
	verifyWrite(t, dir, "docs/readme.md", "readme\n")
	verifyWrite(t, dir, ".seamark/lessons.yaml", verifyLessonsYAML)

	// The sentinel adds .seamark/config.yaml. The commit keeps it out
	// of the working-tree changes.
	sentinel = newInvokerSentinel(t, dir)

	verifyGit(t, dir, "add", "-A")
	verifyGit(t, dir, "commit", "-q", "-m", "Add the fixture")

	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return root, sentinel
}

// verifyGit runs git in dir with a fixed identity.
func verifyGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// verifyWrite writes one file under dir and creates its parents.
func verifyWrite(t *testing.T, dir, rel, content string) {
	t.Helper()

	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// The working-tree edits of the tests.
func verifyEditAsset(t *testing.T, root string) {
	t.Helper()
	verifyWrite(t, root, "generated/a.txt", "asset\nchanged\n")
}

func verifyAddTODO(t *testing.T, root string) {
	t.Helper()
	verifyWrite(t, root, "src/handler.go", "package src\n\n// TODO(owner): split.\nfunc Handle() {}\n")
}

func verifyAddBinary(t *testing.T, root string) {
	t.Helper()
	verifyWrite(t, root, "src/logo.bin", "\x00\x01\x02PNG\x00")
}

// verifyRun runs `seamark -C root lessons --verify <args>` in the test
// process.
func verifyRun(t *testing.T, root string, args ...string) (stdout string, err error) {
	t.Helper()

	stdout, _, err = runIn(t, "", append([]string{"-C", root, "lessons", "--verify"}, args...)...)

	return stdout, err
}

// verifyDocument decodes stdout as exactly one lesson-verification/1
// document.
func verifyDocument(t *testing.T, stdout string) lessoncheck.Verification {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(stdout))

	var v lessoncheck.Verification
	require.NoError(t, dec.Decode(&v), "stdout: %s", stdout)

	var extra json.RawMessage
	require.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout holds one JSON document only")

	return v
}

// treeListing returns every path under root outside .git with its size
// and modification time. Two equal listings show that a command wrote
// no file.
func treeListing(t *testing.T, root string) map[string]string {
	t.Helper()

	listing := map[string]string{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		listing[p] = fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())

		return nil
	})
	require.NoError(t, err)

	return listing
}

// verifyProcessEnv makes TestLessonsVerifyExitCodes run as one seamark
// process. Its value is the JSON list of the arguments. The real Execute
// then maps the error to the exit code and prints the stderr line,
// which no in-process run can show.
const verifyProcessEnv = "SEAMARK_TEST_VERIFY_PROCESS_ARGS"

// runAsSeamark runs Execute with the arguments of encoded and exits with
// its code. It never returns.
func runAsSeamark(encoded string) {
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		fmt.Fprintln(os.Stderr, "bad "+verifyProcessEnv+":", err)
		os.Exit(3)
	}

	os.Args = append([]string{"seamark"}, args...)
	os.Exit(Execute())
}

// seamarkProcess runs this test binary as `seamark <args>` in dir and
// returns its output and exit code. The child inherits the environment,
// so the sentinel of the test also holds in the child.
func seamarkProcess(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	encoded, err := json.Marshal(args)
	require.NoError(t, err)

	var out, errOut bytes.Buffer

	cmd := exec.Command(exe, "-test.run=^TestLessonsVerifyExitCodes$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), verifyProcessEnv+"="+string(encoded))
	cmd.Stdout, cmd.Stderr = &out, &errOut

	err = cmd.Run()

	var exitErr *exec.ExitError

	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		require.NoError(t, err)
	}

	return out.String(), errOut.String(), code
}

// seamarkLines returns the stderr lines that Execute prints. A build
// with coverage can add other lines, so the test compares only these.
func seamarkLines(stderr string) []string {
	var lines []string

	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "seamark: ") {
			lines = append(lines, line)
		}
	}

	return lines
}

// TestLessonsVerifyExitCodes proves the exit codes of contracts K5 and
// §4.2 through the real Execute, in a child process: 0 for passed,
// no-applicable-checks, and no-executable-checks; 2 for a violation,
// also with an unknown result; 1 for an unknown result, a tier-1 and a
// tier-2 configuration error (ruling P1-b), a root error, and a --file
// error. It also checks the stderr line of each case and the error that
// the in-process run returns.
func TestLessonsVerifyExitCodes(t *testing.T) {
	if encoded, ok := os.LookupEnv(verifyProcessEnv); ok {
		runAsSeamark(encoded)
	}

	const syntaxYAML = "pin: [\n"

	_, syntaxErr := reviews.ParseConfig([]byte(syntaxYAML))
	require.Error(t, syntaxErr)

	outside := filepath.Join(t.TempDir(), "outside.txt")

	cases := []struct {
		name   string
		edit   func(t *testing.T, root string)
		args   []string
		code   int
		state  string // the summary state of the first stdout line; "" = no report
		stderr string // the Execute line; "" = none
		is     error  // the sentinel of the in-process error; nil with code 1 = any other error
		report string // a part of the text report
	}{
		{
			name: "passed",
			edit: func(t *testing.T, root string) {
				verifyWrite(t, root, "src/handler.go", "package src\n\n// Handle serves.\nfunc Handle() {}\n")
			},
			code:  0,
			state: "passed",
		},
		{
			name:  "no applicable checks",
			edit:  func(t *testing.T, root string) { verifyWrite(t, root, "docs/readme.md", "readme\nmore\n") },
			code:  0,
			state: "no-applicable-checks",
		},
		{
			name: "no executable checks",
			edit: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml",
					"pin:\n  - rule: prefer-errors-is\n    region: src/\n    note: Compare errors with errors.Is.\n")
				verifyEditAsset(t, root)
			},
			code:  0,
			state: "no-executable-checks",
		},
		{
			name:   "violation",
			edit:   verifyEditAsset,
			code:   2,
			state:  "violation",
			stderr: verifyViolationLine,
			is:     lessoncheck.ErrViolation,
			report: "violation  pin ",
		},
		{
			name: "violation with an unknown result",
			edit: func(t *testing.T, root string) {
				verifyEditAsset(t, root)
				verifyAddBinary(t, root)
			},
			code:   2,
			state:  "violation",
			stderr: verifyViolationLine,
			is:     lessoncheck.ErrViolation,
			report: "unknown/unsupported-content",
		},
		{
			name:   "unknown result",
			edit:   verifyAddBinary,
			code:   1,
			state:  "incomplete",
			stderr: verifyUnknownLine,
			is:     lessoncheck.ErrIncomplete,
			report: "unknown/unsupported-content",
		},
		{
			name: "tier-1 configuration error",
			edit: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml", "lesson_checks_version: -1\n")
			},
			code:   1,
			state:  "incomplete",
			stderr: "seamark: lesson checks incomplete: lessons config: lesson_checks_version must be 1, got -1; rerun seamark lessons --verify",
			is:     lessoncheck.ErrIncomplete,
			report: "config: invalid; no check ran\n  - lessons config: lesson_checks_version must be 1, got -1\n",
		},
		{
			name:   "tier-1 YAML syntax error",
			edit:   func(t *testing.T, root string) { verifyWrite(t, root, ".seamark/lessons.yaml", syntaxYAML) },
			code:   1,
			state:  "incomplete",
			stderr: "seamark: " + render.Sanitize("lesson checks incomplete: "+syntaxErr.Error()+"; rerun seamark lessons --verify"),
			is:     lessoncheck.ErrIncomplete,
			report: "config: invalid; no check ran\n",
		},
		{
			// The pattern does not compile, so no checker runs, and the
			// change under generated/ is no violation: every executable pin
			// is unknown/configuration-error.
			name: "tier-2 configuration error",
			edit: func(t *testing.T, root string) {
				verifyWrite(t, root, ".seamark/lessons.yaml", strings.Replace(verifyLessonsYAML, `'TODO\('`, `'TODO('`, 1))
				verifyEditAsset(t, root)
			},
			code:  1,
			state: "incomplete",
			stderr: "seamark: lesson checks incomplete: lessons config: checks.no-todo: pattern does not compile: " +
				"error parsing regexp: missing closing ): `TODO(`; rerun seamark lessons --verify",
			is:     lessoncheck.ErrIncomplete,
			report: "unknown/configuration-error  pin ",
		},
		{
			name:   "--file outside the repository",
			args:   []string{"--file", outside},
			code:   1,
			stderr: "seamark: --file \"" + outside + "\" is outside the repository",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := verifyFixture(t, t.TempDir())

			if c.edit != nil {
				c.edit(t, root)
			}

			args := append([]string{"-C", root, "lessons", "--verify"}, c.args...)

			stdout, stderr, code := seamarkProcess(t, root, args...)
			assert.Equal(t, c.code, code, "stdout: %s\nstderr: %s", stdout, stderr)

			if c.stderr == "" {
				assert.Empty(t, seamarkLines(stderr))
			} else {
				assert.Equal(t, []string{c.stderr}, seamarkLines(stderr))
			}

			if c.state == "" {
				assert.Empty(t, stdout, "a usage or root error prints no report")
			} else {
				assert.True(t, strings.HasPrefix(stdout, "lesson checks: "+c.state+"\n"), "stdout: %s", stdout)
			}

			assert.Contains(t, stdout, c.report)

			// The in-process run returns the error that Execute maps.
			_, _, err := runIn(t, "", args...)

			switch {
			case c.code == 0:
				require.NoError(t, err)
			case c.is != nil:
				require.ErrorIs(t, err, c.is)
			default:
				require.Error(t, err)
			}

			if c.code != 2 {
				assert.NotErrorIs(t, err, lessoncheck.ErrViolation)
			}

			assert.NotErrorIs(t, err, gate.ErrBlocked, "a lesson check is no policy block")

			sentinel.requireNoCall(t)
		})
	}

	t.Run("a directory outside git is a root error", func(t *testing.T) {
		dir := t.TempDir()

		stdout, stderr, code := seamarkProcess(t, dir, "-C", dir, "lessons", "--verify", "--json")
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout)
		assert.Equal(t, []string{"seamark: lesson checks: " + dir + " is not inside a git work tree"}, seamarkLines(stderr))
	})
}

// TestLessonsVerifyJSON proves the --json output (contract K5): stdout
// holds only the lesson-verification/1 document that Verify returns,
// also for a configuration error, and nothing on a usage or root error.
func TestLessonsVerifyJSON(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	verifyEditAsset(t, root)
	verifyAddTODO(t, root)
	verifyWrite(t, root, "src/new.go", "package src\n")

	t.Run("the document of Verify", func(t *testing.T) {
		stdout, err := verifyRun(t, root, "--json")
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.True(t, strings.HasPrefix(stdout, "{\n  \"schema\": \"lesson-verification/1\",\n"), "two-space indentation: %s", stdout)

		got := verifyDocument(t, stdout)
		assert.Equal(t, lessoncheck.VerificationSchema, got.Schema)
		assert.Equal(t, lessoncheck.CallerCLI, got.Caller)
		assert.Equal(t, version, got.SeamarkVersion)
		assert.Equal(t, lessoncheck.BoundaryWorkingTree, got.Boundary)
		assert.Equal(t, lessoncheck.FreshnessFresh, got.Freshness)
		assert.Equal(t, lessoncheck.SummaryViolation, got.Summary.State)
		assert.Equal(t, "seamark lessons --verify", got.Rerun)
		assert.Len(t, got.Results, 2)
		assert.Len(t, got.Listings, 1)
		assert.Len(t, got.Changes, 3, "the JSON output names every change; the text output does not")

		// The CLI prints the document of Verify unchanged. Only the time
		// of the check differs between two runs.
		want, err := lessoncheck.Verify(t.Context(), lessoncheck.VerifyRequest{
			Root: root, Caller: lessoncheck.CallerCLI, SeamarkVersion: version,
		})
		require.NoError(t, err)

		want.CheckedAt = got.CheckedAt

		wantJSON, err := json.Marshal(want)
		require.NoError(t, err)
		assert.JSONEq(t, string(wantJSON), stdout)
	})

	t.Run("the text output holds no JSON and no change list", func(t *testing.T) {
		stdout, err := verifyRun(t, root)
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.True(t, strings.HasPrefix(stdout, "lesson checks: violation\n"))
		assert.NotContains(t, stdout, "{")
		assert.NotContains(t, stdout, "src/new.go", "a change outside every finding stays out of the text")
	})

	t.Run("explicit paths", func(t *testing.T) {
		asset := filepath.Join(root, "generated", "a.txt")

		stdout, err := verifyRun(t, root, "--json", "--file", asset)
		require.ErrorIs(t, err, lessoncheck.ErrViolation)

		got := verifyDocument(t, stdout)
		assert.Equal(t, lessoncheck.SelectionExplicitPaths, got.Selection.Kind)
		assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths)
		assert.Equal(t, "seamark lessons --verify --file "+asset, got.Rerun, "the rerun names the absolute path (§1.3)")
	})

	t.Run("a configuration error still prints the document", func(t *testing.T) {
		original, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
		require.NoError(t, err)

		verifyWrite(t, root, ".seamark/lessons.yaml", "lesson_checks_version: -1\n")

		t.Cleanup(func() { verifyWrite(t, root, ".seamark/lessons.yaml", string(original)) })

		stdout, err := verifyRun(t, root, "--json")
		require.ErrorIs(t, err, lessoncheck.ErrIncomplete)
		assert.Contains(t, stdout, "\"results\": [],")
		assert.Contains(t, stdout, "\"listings\": [],")

		got := verifyDocument(t, stdout)
		assert.False(t, got.Config.Valid)
		require.Len(t, got.Config.Errors, 1)
		assert.Equal(t, lessoncheck.CodeDecodeError, got.Config.Errors[0].Code)
		assert.Equal(t, lessoncheck.SummaryIncomplete, got.Summary.State)
	})

	t.Run("no executable checks exit 0 with the document", func(t *testing.T) {
		clean, cleanSentinel := verifyFixture(t, t.TempDir())
		verifyWrite(t, clean, ".seamark/lessons.yaml", "pin:\n  - rule: r\n    region: src/\n    note: n\n")

		stdout, err := verifyRun(t, clean, "--json")
		require.NoError(t, err)
		assert.Equal(t, lessoncheck.SummaryNoExecutableChecks, verifyDocument(t, stdout).Summary.State)

		cleanSentinel.requireNoCall(t)
	})

	t.Run("a usage or root error prints no JSON", func(t *testing.T) {
		stdout, err := verifyRun(t, root, "--json", "--file", filepath.Join(t.TempDir(), "x"))
		require.Error(t, err)
		assert.Empty(t, stdout)

		stdout, err = verifyRun(t, root, "--json", "--file", ".")
		require.Error(t, err)
		assert.Empty(t, stdout)

		outside := t.TempDir()

		stdout, err = verifyRun(t, outside, "--json")
		require.EqualError(t, err, "lesson checks: "+outside+" is not inside a git work tree")
		assert.Empty(t, stdout)

		// The root comes first: a --file value needs the root to be read.
		stdout, err = verifyRun(t, outside, "--json", "--file", "x.txt")
		require.EqualError(t, err, "lesson checks: "+outside+" is not inside a git work tree")
		assert.Empty(t, stdout)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyFileRules proves the --file rules of contracts K4 and
// §1.3: an absolute or relative value inside the repository selects its
// path, several values select the sorted set, an ignored path named by
// --file is checked, and a value outside the repository or naming the
// whole repository is refused with the exact text. Without --verify,
// --file keeps one value.
func TestLessonsVerifyFileRules(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	verifyEditAsset(t, root)
	verifyAddTODO(t, root)

	selection := func(t *testing.T, args ...string) lessoncheck.Verification {
		t.Helper()

		stdout, _ := verifyRun(t, root, append([]string{"--json"}, args...)...)

		return verifyDocument(t, stdout)
	}

	t.Run("an absolute path", func(t *testing.T) {
		got := selection(t, "--file", filepath.Join(root, "src", "handler.go"))
		assert.Equal(t, []string{"src/handler.go"}, got.Selection.Paths)
		assert.Equal(t, lessoncheck.SummaryViolation, got.Summary.State, "the added TODO( line")
	})

	t.Run("a relative path starts at the process directory", func(t *testing.T) {
		t.Chdir(root)

		got := selection(t, "--file", "generated/a.txt")
		assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths)
	})

	t.Run("several values give the sorted set without duplicates", func(t *testing.T) {
		got := selection(t,
			"--file", filepath.Join(root, "src", "handler.go"),
			"--file", filepath.Join(root, "generated"),
			"--file", filepath.Join(root, "src", "handler.go")+"/",
			"--file", " "+filepath.Join(root, "docs")+" ")
		assert.Equal(t, []string{"docs", "generated", "src/handler.go"}, got.Selection.Paths)
	})

	t.Run("an ignored path named by --file is checked", func(t *testing.T) {
		verifyWrite(t, root, "generated/run.log", "log\n")

		t.Cleanup(func() { require.NoError(t, os.Remove(filepath.Join(root, "generated", "run.log"))) })

		got := selection(t, "--file", filepath.Join(root, "generated", "run.log"))
		require.Len(t, got.Results, 2)
		assert.Equal(t, lessoncheck.OutcomeViolation, got.Results[0].Outcome)
		require.Len(t, got.Results[0].Evidence, 1)
		assert.Equal(t, "generated/run.log", got.Results[0].Evidence[0].Path)
	})

	t.Run("a deleted path", func(t *testing.T) {
		t.Chdir(root)

		require.NoError(t, os.Remove(filepath.Join(root, "generated", "a.txt")))

		t.Cleanup(func() { verifyEditAsset(t, root) })

		got := selection(t, "--file", "generated/a.txt")
		require.Len(t, got.Results[0].Evidence, 1)
		assert.Equal(t, "delete", string(got.Results[0].Evidence[0].Change))
	})

	t.Run("a rerun path is absolute and quoted", func(t *testing.T) {
		verifyWrite(t, root, "generated/a b.txt", "new\n")

		t.Cleanup(func() { require.NoError(t, os.Remove(filepath.Join(root, "generated", "a b.txt"))) })

		got := selection(t, "--file", filepath.Join(root, "generated", "a b.txt"))
		assert.Equal(t, "seamark lessons --verify --file '"+filepath.Join(root, "generated", "a b.txt")+"'", got.Rerun)
	})

	t.Run("a value outside the repository", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "x.txt")

		stdout, err := verifyRun(t, root, "--file", outside)
		require.EqualError(t, err, "--file \""+outside+"\" is outside the repository")
		assert.Empty(t, stdout)

		t.Chdir(filepath.Join(root, "src"))

		_, err = verifyRun(t, root, "--file", "../../x.txt")
		require.EqualError(t, err, `--file "../../x.txt" is outside the repository`)
	})

	t.Run("a value that names the whole repository", func(t *testing.T) {
		t.Chdir(root)

		for _, value := range []string{".", "./", "", "  ", root, root + "/"} {
			stdout, err := verifyRun(t, root, "--file", value)
			require.EqualError(t, err, "--file \""+value+"\" names the whole repository; omit --file for the default selection")
			assert.Empty(t, stdout)
		}
	})

	t.Run("without --verify, --file keeps one value", func(t *testing.T) {
		stdout, _, err := runIn(t, "", "-C", root, "lessons", "--file", "a.go", "--file", "b.go")
		require.EqualError(t, err, "--file takes one path except with --verify")
		assert.Empty(t, stdout)

		// One value still reaches the lookup, which needs an index.
		_, _, err = runIn(t, "", "-C", root, "lessons", "--file", "src/handler.go")
		require.ErrorIs(t, err, errNoIndex)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyFileFromSubdirectory proves that a relative --file
// value starts at the process directory (contracts K4): run from src/,
// handler.go selects src/handler.go. The test does not resolve the
// temporary directory. On macOS the process directory then reads
// /var/... while the git top level reads /private/var/..., the shape a
// symbolic link above a real repository gives.
func TestLessonsVerifyFileFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	root, sentinel := verifyFixture(t, dir)

	verifyAddTODO(t, root)
	verifyEditAsset(t, root)

	t.Chdir(filepath.Join(dir, "src"))

	run := func(t *testing.T, args ...string) (lessoncheck.Verification, error) {
		t.Helper()

		stdout, _, err := runIn(t, "", append([]string{"lessons", "--verify", "--json"}, args...)...)

		return verifyDocument(t, stdout), err
	}

	t.Run("a file of the directory", func(t *testing.T) {
		got, err := run(t, "--file", "handler.go")
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.Equal(t, []string{"src/handler.go"}, got.Selection.Paths)
		assert.Equal(t, "seamark lessons --verify --file "+filepath.Join(root, "src", "handler.go"), got.Rerun)
	})

	t.Run("a parent path", func(t *testing.T) {
		got, err := run(t, "--file", "../generated/a.txt")
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths)
	})

	t.Run("the directory itself", func(t *testing.T) {
		got, err := run(t, "--file", ".")
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.Equal(t, []string{"src"}, got.Selection.Paths, "from src/, . is src, not the whole repository")
	})

	t.Run("the default selection from a subdirectory", func(t *testing.T) {
		got, err := run(t)
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.Equal(t, lessoncheck.SelectionDefault, got.Selection.Kind)
		assert.Equal(t, 2, got.Summary.Changes)
	})

	sentinel.requireNoCall(t)
}

// verifyGenPin adds a pin on gen/ to verifyLessonsYAML. The fixture of
// TestLessonsVerifyFileThroughALinkedRoot commits gen as a link to
// generated, so the pin scopes a path below an in-repo link.
const verifyGenPin = `  - rule: frozen-gen
    region: gen/
    note: The gen link is frozen too.
    mode: enforce
    check: frozen-assets
`

// TestLessonsVerifyFileThroughALinkedRoot proves contracts §1.3 and K4
// when the process directory reaches the repository through a link. The
// git top level then differs from the process directory. Only that root
// alias is matched: a deleted path still compares, a link below the root
// is never followed, and a linked parent keeps the limitation
// path-through-link. A relative value starts at the resolved process
// directory (P1 fix round F-A). The absolute values that name the alias
// still test the root alias match. The test builds its own alias, so it
// also runs on Linux, where no /var link exists.
func TestLessonsVerifyFileThroughALinkedRoot(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	require.NoError(t, os.Symlink("generated", filepath.Join(root, "gen")))
	verifyWrite(t, root, ".seamark/lessons.yaml", verifyLessonsYAML+verifyGenPin)
	verifyGit(t, root, "add", "-A")
	verifyGit(t, root, "commit", "-q", "-m", "Add the gen link")

	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(root, alias))

	t.Chdir(alias)

	run := func(t *testing.T, args ...string) (lessoncheck.Verification, error) {
		t.Helper()

		stdout, _, err := runIn(t, "", append([]string{"lessons", "--verify", "--json"}, args...)...)
		require.NotEmpty(t, stdout, "args %v: no report, error: %v", args, err)

		return verifyDocument(t, stdout), err
	}

	// result returns the result of the pin with the rule name.
	result := func(t *testing.T, v lessoncheck.Verification, rule string) lessoncheck.Result {
		t.Helper()

		for _, r := range v.Results {
			if r.Rule == rule {
				return r
			}
		}

		require.Failf(t, "no result", "rule %s in %+v", rule, v.Results)

		return lessoncheck.Result{}
	}

	asset := filepath.Join(root, "generated", "a.txt")

	// restore writes the committed asset back, so the next subtest starts
	// from a clean tree.
	restore := func(t *testing.T) {
		t.Cleanup(func() { verifyWrite(t, root, "generated/a.txt", "asset\n") })
	}

	t.Run("a deleted file", func(t *testing.T) {
		require.NoError(t, os.Remove(asset))
		restore(t)

		for _, value := range []string{"generated/a.txt", filepath.Join(alias, "generated", "a.txt")} {
			got, err := run(t, "--file", value)
			require.ErrorIs(t, err, lessoncheck.ErrViolation, "value %q", value)
			assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths, "value %q", value)
			assert.Equal(t, "seamark lessons --verify --file "+asset, got.Rerun, "the rerun names the git top level")

			r := result(t, got, "frozen-assets")
			require.Len(t, r.Evidence, 1, "value %q", value)
			assert.Equal(t, "delete", string(r.Evidence[0].Change))
		}
	})

	t.Run("a deleted directory", func(t *testing.T) {
		require.NoError(t, os.RemoveAll(filepath.Join(root, "generated")))
		restore(t)

		for _, value := range []string{"generated/a.txt", "src/../generated/a.txt"} {
			got, err := run(t, "--file", value)
			require.ErrorIs(t, err, lessoncheck.ErrViolation, "value %q", value)
			assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths, "value %q", value)
		}
	})

	t.Run("a link that the value names is not followed", func(t *testing.T) {
		verifyWrite(t, root, "src/TODO(x)", "package src\n")
		require.NoError(t, os.Symlink("TODO(x)", filepath.Join(root, "src", "alias.go")))

		t.Cleanup(func() {
			require.NoError(t, os.Remove(filepath.Join(root, "src", "alias.go")))
			require.NoError(t, os.Remove(filepath.Join(root, "src", "TODO(x)")))
		})

		got, err := run(t, "--file", "src/alias.go")
		require.ErrorIs(t, err, lessoncheck.ErrViolation, "the link target string holds TODO(")
		assert.Equal(t, []string{"src/alias.go"}, got.Selection.Paths, "never the link target src/TODO(x)")
		assert.Equal(t, lessoncheck.OutcomeViolation, result(t, got, "no-todo").Outcome)
	})

	t.Run("a path below an in-repo link keeps the link", func(t *testing.T) {
		got, err := run(t, "--file", "gen/a.txt")
		require.ErrorIs(t, err, lessoncheck.ErrIncomplete)
		assert.Equal(t, []string{"gen/a.txt"}, got.Selection.Paths, "never the link target generated/a.txt")
		require.Len(t, got.Changes, 1)
		assert.Equal(t, []string{lessoncheck.LimitationPathThroughLink}, got.Changes[0].Limitations)

		// The tree is clean, and the producer does not look through the
		// link, so it observes no difference: the forbid pin reads unknown,
		// never violation (review fixes snapshot-git:F5, checkers:K3-F2).
		frozen := result(t, got, "frozen-gen")
		assert.Equal(t, lessoncheck.OutcomeUnknown, frozen.Outcome)
		assert.Equal(t, lessoncheck.ReasonContentUnavailable, frozen.Reason)
	})

	t.Run("the linked root names the whole repository", func(t *testing.T) {
		for _, value := range []string{".", alias, alias + "/"} {
			stdout, _, err := runIn(t, "", "lessons", "--verify", "--file", value)
			require.EqualError(t, err, "--file \""+value+"\" names the whole repository; omit --file for the default selection")
			assert.Empty(t, stdout)
		}
	})

	t.Run("a value outside the repository stays outside", func(t *testing.T) {
		into := filepath.Join(t.TempDir(), "into")
		require.NoError(t, os.Symlink(filepath.Join(root, "src"), into))

		for _, value := range []string{"../x.txt", filepath.Join(t.TempDir(), "x.txt"), filepath.Join(into, "handler.go")} {
			stdout, _, err := runIn(t, "", "lessons", "--verify", "--file", value)
			require.EqualError(t, err, "--file \""+value+"\" is outside the repository")
			assert.Empty(t, stdout)
		}
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyFileFromALinkedProcessDirectory proves fix item F-A
// of the P1 review (contracts K4 Addition of the P1 fix round). The CLI
// resolves the links of the process directory before the inside check,
// as capture resolves the hook cwd (correction-learning F6). So a link
// from outside the repository into it gives paths inside the repository.
// A link in the tree that leads outside gives the outside error. The
// value itself stays lexical, so a link that it names is never followed.
func TestLessonsVerifyFileFromALinkedProcessDirectory(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	verifyAddTODO(t, root)

	proj := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.Symlink(filepath.Join(root, "src"), proj))

	run := func(t *testing.T, args ...string) (lessoncheck.Verification, error) {
		t.Helper()

		stdout, _, err := runIn(t, "", append([]string{"lessons", "--verify", "--json"}, args...)...)
		require.NotEmpty(t, stdout, "args %v: no report, error: %v", args, err)

		return verifyDocument(t, stdout), err
	}

	// refused requires the outside error of contracts K4 and no stdout.
	// It names the root with -C. Without -C, a process directory outside
	// the repository fails earlier, at the root step.
	refused := func(t *testing.T, value string) {
		t.Helper()

		stdout, _, err := runIn(t, "", "-C", root, "lessons", "--verify", "--file", value)
		require.EqualError(t, err, "--file \""+value+"\" is outside the repository")
		assert.Empty(t, stdout)
	}

	// link adds an untracked link in the tree and removes it after the
	// subtest. The removal runs after t.Chdir restores the directory,
	// because cleanups run in reverse order.
	link := func(t *testing.T, target, rel string) string {
		t.Helper()

		p := filepath.Join(root, rel)
		require.NoError(t, os.Symlink(target, p))

		t.Cleanup(func() { require.NoError(t, os.Remove(p)) })

		return p
	}

	t.Run("a file of a directory that an outside link leads into", func(t *testing.T) {
		t.Chdir(proj)

		got, err := run(t, "--file", "handler.go")
		require.ErrorIs(t, err, lessoncheck.ErrViolation, "the added TODO( line")
		assert.Equal(t, []string{"src/handler.go"}, got.Selection.Paths)
		assert.Equal(t, "seamark lessons --verify --file "+filepath.Join(root, "src", "handler.go"), got.Rerun)
	})

	t.Run("a parent path and the directory itself start at the link target", func(t *testing.T) {
		t.Chdir(proj)

		got, err := run(t, "--file", "../generated/a.txt", "--file", ".")
		require.ErrorIs(t, err, lessoncheck.ErrViolation)
		assert.Equal(t, []string{"generated/a.txt", "src"}, got.Selection.Paths)
	})

	t.Run("a missing file keeps its name", func(t *testing.T) {
		t.Chdir(proj)

		got, err := run(t, "--file", "gone.go")
		require.NoError(t, err)
		assert.Equal(t, []string{"src/gone.go"}, got.Selection.Paths)
	})

	t.Run("an absolute value through the outside link stays outside", func(t *testing.T) {
		// Only the process directory is resolved. A value that names the
		// link is a path outside root (§1.3: never followed).
		t.Chdir(proj)

		refused(t, filepath.Join(proj, "handler.go"))
	})

	t.Run("an in-tree link to a directory of the tree gives the target name", func(t *testing.T) {
		// The process directory is the link target, so a relative value
		// names a path below generated/. A value that names gen/ itself
		// keeps the link (TestLessonsVerifyFileThroughALinkedRoot).
		gen := link(t, "generated", "gen")

		t.Chdir(gen)

		got, err := run(t, "--file", "a.txt")
		require.NoError(t, err, "generated/a.txt is unchanged")
		assert.Equal(t, []string{"generated/a.txt"}, got.Selection.Paths)
	})

	t.Run("an in-tree link that leads outside stays refused", func(t *testing.T) {
		out := link(t, t.TempDir(), "outlink")

		t.Chdir(out)

		refused(t, "x.txt")
	})

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyFlagConflicts proves the guards of contracts §4.2 for
// milestone P1. --verify combines with no other mode, and --json and a
// second --file need --verify. The texts that later milestones extend
// are asserted by the table (e) rule: the head, each mode of milestone
// P1, and the tail. The --hook cases use only forms that §4.1 keeps
// refusing after milestone P4.
func TestLessonsVerifyFlagConflicts(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	verifyEditAsset(t, root)

	before := treeListing(t, root)

	refuse := func(t *testing.T, args ...string) string {
		t.Helper()

		stdout, _, err := runIn(t, "", append([]string{"-C", root, "lessons"}, args...)...)
		require.Error(t, err, "args %v", args)
		assert.NotErrorIs(t, err, lessoncheck.ErrViolation, "a refusal exits 1")
		assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1")
		assert.Empty(t, stdout, "a refusal prints nothing on stdout")

		return err.Error()
	}

	t.Run("each other mode", func(t *testing.T) {
		for _, c := range []struct {
			args []string
			flag string
		}{
			{[]string{"--list"}, "--list"},
			{[]string{"--region", "src"}, "--region"},
			{[]string{"--stats"}, "--stats"},
			{[]string{"--distill"}, "--distill"},
			{[]string{"--proposals"}, "--proposals"},
			{[]string{"--apply", "p1"}, "--apply"},
			{[]string{"--dismiss", "p1"}, "--dismiss"},
			{[]string{"--prune", "p1"}, "--prune"},
			{[]string{"--retarget", "p1"}, "--retarget"},
			{[]string{"--extract-triggers"}, "--extract-triggers"},
			{[]string{"--hook-reset"}, "--hook-reset"},
			// The first other mode in the help order names the conflict.
			{[]string{"--stats", "--list"}, "--list"},
			{[]string{"--apply", "p1", "--dismiss", "p2"}, "--apply"},
		} {
			got := refuse(t, append([]string{"--verify"}, c.args...)...)
			assert.Equal(t, "--verify does not combine with "+c.flag, got, "args %v", c.args)
		}
	})

	t.Run("--hook", func(t *testing.T) {
		for _, args := range [][]string{
			{"--verify", "--json", "--hook"},
			{"--verify", "--file", filepath.Join(root, "generated", "a.txt"), "--hook"},
		} {
			got := refuse(t, args...)
			assert.True(t, strings.HasPrefix(got, "--verify "), "got %q", got)
		}
	})

	t.Run("positional arguments", func(t *testing.T) {
		got := refuse(t, "--verify", "generated/a.txt")
		assert.True(t, strings.HasPrefix(got, `unexpected argument "generated/a.txt"`), "got %q", got)
	})

	t.Run("--json without --verify", func(t *testing.T) {
		for _, args := range [][]string{{"--json"}, {"--json", "--list"}, {"--json", "--file", "a.go"}} {
			got := refuse(t, args...)
			assert.True(t, strings.HasPrefix(got, "--json applies to --verify"), "got %q", got)
			assert.True(t, strings.HasSuffix(got, " only"), "got %q", got)
		}
	})

	t.Run("a second --file without --verify", func(t *testing.T) {
		got := refuse(t, "--file", "a.go", "--file", "b.go")
		assert.Equal(t, "--file takes one path except with --verify", got)

		got = refuse(t, "--list", "--file", "a.go", "--file", "b.go")
		assert.Equal(t, "--file takes one path except with --verify", got)
	})

	t.Run("the default error names --verify", func(t *testing.T) {
		got := refuse(t)
		assert.True(t, strings.HasPrefix(got, "provide --file <path>, --list, --stats, --distill, --proposals, --verify"), "got %q", got)
		assert.True(t, strings.HasSuffix(got, ", --apply, --dismiss, or --hook"), "got %q", got)
	})

	t.Run("the older guards keep their texts", func(t *testing.T) {
		assert.Equal(t, "--apply, --dismiss, --prune and --retarget are different decisions — run them one at a time",
			refuse(t, "--apply", "p1", "--dismiss", "p2"))
		assert.Equal(t, "--dry-run only applies to --distill and --extract-triggers", refuse(t, "--verify", "--dry-run"))
	})

	assert.Equal(t, before, treeListing(t, root), "a refusal writes no file")

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyNeverInvokes proves AC-1 for lessons --verify: every
// outcome, both output forms, explicit paths, and a configuration error
// run without an agent, a model CLI, or the network. The command also
// writes no index and no state. Its only write is the record of the
// lesson-check audit stream (contract K5, milestone P4).
func TestLessonsVerifyNeverInvokes(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	// A model call would need an agent: agent.argv names the stub. A
	// default agent would start claude or codex, which are stubs too.
	config, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(config), "argv:")

	verifyEditAsset(t, root)
	verifyAddTODO(t, root)
	verifyAddBinary(t, root)

	before := treeListing(t, root)

	for _, args := range [][]string{
		nil,
		{"--json"},
		{"--file", filepath.Join(root, "generated", "a.txt")},
		{"--json", "--file", filepath.Join(root, "src")},
	} {
		_, err := verifyRun(t, root, args...)
		require.ErrorIs(t, err, lessoncheck.ErrViolation, "args %v", args)
	}

	verifyWrite(t, root, ".seamark/lessons.yaml", strings.Replace(verifyLessonsYAML, `'TODO\('`, `'TODO('`, 1))

	_, err = verifyRun(t, root)
	require.ErrorIs(t, err, lessoncheck.ErrIncomplete)

	after := treeListing(t, root)
	delete(before, filepath.Join(root, ".seamark", "lessons.yaml"))
	delete(after, filepath.Join(root, ".seamark", "lessons.yaml"))

	// Since milestone P4, each run appends one record to the lesson-check
	// audit stream (contract K5). The stream, its lock, and the entry of
	// .seamark are the only writes.
	assert.FileExists(t, filepath.Join(root, ".seamark", "lesson-check-audit.jsonl"))

	for _, rel := range []string{".seamark", ".seamark/lesson-check-audit.jsonl", ".seamark/lesson-check-audit.jsonl.lock"} {
		delete(before, filepath.Join(root, filepath.FromSlash(rel)))
		delete(after, filepath.Join(root, filepath.FromSlash(rel)))
	}

	assert.Equal(t, before, after, "lessons --verify writes no file except the lesson-check audit stream")

	sentinel.requireNoCall(t)
}

// TestLessonsVerifyWritesAuditRecord proves the milestone P4 additions
// of contract K5 through the CLI: each run appends one verify record to
// .seamark/lesson-check-audit.jsonl with the caller cli, and a failed
// record gives one stderr line on an exit-0 path only. The failure
// never changes the exit code, and the report keeps its own line.
func TestLessonsVerifyWritesAuditRecord(t *testing.T) {
	root, sentinel := verifyFixture(t, t.TempDir())

	verifyWrite(t, root, "src/handler.go", "package src\n\n// Handle serves.\nfunc Handle() {}\n")

	audit := filepath.Join(root, ".seamark", "lesson-check-audit.jsonl")

	t.Run("one verify record per run", func(t *testing.T) {
		_, err := verifyRun(t, root)
		require.NoError(t, err)

		data, err := os.ReadFile(audit)
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		require.Len(t, lines, 1)

		var rec lessoncheck.AuditRecord
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))

		assert.Equal(t, lessoncheck.AuditSchema, rec.Schema)
		assert.Equal(t, lessoncheck.AuditVerify, rec.Kind)
		assert.Equal(t, lessoncheck.CallerCLI, rec.Caller)
		assert.Equal(t, lessoncheck.BoundaryWorkingTree, rec.Boundary)
		assert.Equal(t, lessoncheck.SummaryPassed, rec.State)
		assert.Equal(t, []string{"src/handler.go"}, rec.Paths)
		assert.True(t, rec.PathsComplete)
		require.Len(t, rec.Pins, 2)
		assert.NotContains(t, string(data), "Handle serves", "a record holds no file content")

		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-audit.jsonl"), "the firing log gets nothing")
	})

	t.Run("a failed record on an exit-0 path prints one line", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))
		require.NoError(t, os.Remove(audit))
		require.NoError(t, os.Symlink(target, audit))

		want := []string{"seamark: lesson-check audit: unsafe path: " + audit + " is a symbolic link"}

		for _, args := range [][]string{nil, {"--json"}} {
			stdout, stderr, code := seamarkProcess(t, root, append([]string{"-C", root, "lessons", "--verify"}, args...)...)
			assert.Equal(t, 0, code, "args %v: stdout: %s\nstderr: %s", args, stdout, stderr)
			assert.Equal(t, want, seamarkLines(stderr), "args %v", args)
			assert.NotContains(t, stdout, "lesson-check audit", "the line goes to stderr only")
		}

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data), "nothing goes through the link")
	})

	t.Run("a failed record on a non-zero exit keeps the K5 line alone", func(t *testing.T) {
		verifyEditAsset(t, root)

		_, stderr, code := seamarkProcess(t, root, "-C", root, "lessons", "--verify")
		assert.Equal(t, 2, code)
		assert.Equal(t, []string{verifyViolationLine}, seamarkLines(stderr))
	})

	sentinel.requireNoCall(t)
}

// lateChange puts a git wrapper first on PATH for the rest of the test.
// Every verification reads the status twice: once for the snapshot, and
// once to revalidate it. The wrapper adds an untracked file under src/
// just before the second read, so an input changes after the read and
// before the publication. The hook for that moment is private to
// lessoncheck, and this wrapper needs no change of the command. It returns
// a function that restores the state, so a test can run a second command.
func lateChange(t *testing.T, root string) (reset func()) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a shell script")
	}

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	late := filepath.Join(root, "src", "late.go")

	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"*--ignored=no*)\n" +
		"  n=$(cat " + shellQuote(count) + " 2>/dev/null || echo 0)\n" +
		"  n=$((n + 1))\n" +
		"  echo \"$n\" > " + shellQuote(count) + "\n" +
		"  if [ \"$n\" = 2 ]; then echo 'package src' > " + shellQuote(late) + "; fi\n" +
		"  ;;\n" +
		"esac\n" +
		"exec " + shellQuote(gitPath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))

	// macOS can scan a new executable on its first run. This run takes
	// that time before a verification starts its deadline.
	out, err := exec.Command(filepath.Join(dir, "git"), "--version").CombinedOutput()
	require.NoError(t, err, "%s", out)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() {
		require.FileExists(t, late, "the wrapper changed an input")
		require.NoError(t, os.Remove(count))
		require.NoError(t, os.Remove(late))
	}
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// outcomeWords lists the outcome of each result in the words of the text
// report. A reason exists only for an unknown result.
func outcomeWords(results []lessoncheck.Result) []string {
	words := make([]string, 0, len(results))

	for _, r := range results {
		word := string(r.Outcome)
		if r.Reason != "" {
			word += "/" + string(r.Reason)
		}

		words = append(words, word)
	}

	return words
}

// The stale cases of the commands: what each one exits with and what its
// JSON reads when an input changes after the read. A stale result never
// publishes a pass, and a violation that the run observed stays.
var staleCases = []struct {
	name     string
	edit     func(t *testing.T, root string)
	code     int
	stderr   string
	outcomes []string
	state    lessoncheck.SummaryState
	combined string
}{
	{
		name: "a pass reads unknown",
		edit: checkLessonsEditHandler, code: 1, stderr: "seamark: lesson checks incomplete: 2 unknown; rerun seamark lessons --verify",
		outcomes: []string{"unknown/stale-snapshot", "unknown/stale-snapshot"},
		state:    lessoncheck.SummaryIncomplete, combined: "incomplete",
	},
	{
		name: "an observed violation stays",
		edit: verifyEditAsset, code: 2, stderr: verifyViolationLine,
		outcomes: []string{"violation", "unknown/stale-snapshot"},
		state:    lessoncheck.SummaryViolation, combined: "fail",
	},
}

// TestLessonsVerifyStaleResult proves the text, the JSON, and the exit
// code of a stale result through the real Execute and a real change of
// the working tree.
func TestLessonsVerifyStaleResult(t *testing.T) {
	for _, c := range staleCases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := verifyFixture(t, t.TempDir())
			c.edit(t, root)

			reset := lateChange(t, root)

			stdout, stderr, code := seamarkProcess(t, root, "-C", root, "lessons", "--verify")
			assert.Equal(t, c.code, code, "stdout: %s\nstderr: %s", stdout, stderr)
			assert.Equal(t, []string{c.stderr}, seamarkLines(stderr))
			assert.Contains(t, stdout, ", stale\n")
			assert.Contains(t, stdout, "\n  stale: the inputs changed after the read")

			reset()

			jsonOut, _, jsonCode := seamarkProcess(t, root, "-C", root, "lessons", "--verify", "--json")
			assert.Equal(t, c.code, jsonCode)

			v := verifyDocument(t, jsonOut)
			assert.Equal(t, lessoncheck.FreshnessStale, v.Freshness)
			assert.Equal(t, c.state, v.Summary.State)
			assert.Equal(t, c.outcomes, outcomeWords(v.Results))
			assert.NotEmpty(t, v.Rerun, "a state that exits non-zero names its rerun")

			sentinel.requireNoCall(t)
		})
	}
}
