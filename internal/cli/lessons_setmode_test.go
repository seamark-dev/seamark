package cli

// Tests of lessons --set-mode (contracts K6 and §4, AC-27, AC-1). The
// command changes only the mode and check keys of one pin, refuses every
// K6 case with exit code 1 and no write, obeys distill.write, and calls
// no model.
//
// Two outputs change in later milestones, so these tests assert only the
// parts that every later form keeps (plan table (e), F8). Milestone P4
// replaces the fixed active line with the K10 join. Milestone P5 adds
// --delivery to the --pin text and to the K2 missing text.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// setModeYAMLHead is the top of every fixture: a tuning value and the
// spec example of a check configuration.
const setModeYAMLHead = `threshold: 3
lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
`

// setModeYAML holds the spec example pin without a mode or a check
// (spec L137-140) and the second example pin of contract K2.
const setModeYAML = setModeYAMLHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// setModeYAMLEnforced is setModeYAML after
// --set-mode enforce --pin 3215e7b483d1 --check frozen-assets (K6
// example).
const setModeYAMLEnforced = setModeYAMLHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// setModeYAMLAdvisory is setModeYAMLEnforced after
// --set-mode advisory --pin 3215e7b483d1: the check stays (K6 example).
const setModeYAMLAdvisory = setModeYAMLHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    check: frozen-assets
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// The pin-refs and the short versions of the spec example pin (K2
// vectors), and the short version of its check definition.
const (
	setModePinRef      = "3215e7b483d1"
	setModeSecondRef   = "ed5d4af5de3a"
	setModeShortPlain  = "e79201579c97"
	setModeShortEnf    = "ae4f17f75413"
	setModeShortForbid = "9ceb9cddb505"
)

// setModeWorkspace writes lessons as the pin file of a new root, sets
// the write gate, and installs the AC-1 sentinel after the config,
// because the sentinel adds its agent section to config.yaml. The root
// has no index, because --set-mode needs none.
func setModeWorkspace(t *testing.T, lessons string, gateOn bool) (string, *invokerSentinel) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	maintainWriteLessons(t, root, lessons, 0o600)

	if gateOn {
		maintainWriteConfig(t, root, maintainGateOn)
	}

	return root, newInvokerSentinel(t, root)
}

// setModeTree lists every path under root, so a test proves that the
// command creates no other file: no index, no hook, no backup.
func setModeTree(t *testing.T, root string) []string {
	t.Helper()

	var paths []string

	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		paths = append(paths, filepath.ToSlash(rel))

		return err
	}))

	return paths
}

// setModeBlockHead returns the first four lines of the K1 impact block
// of the spec example pin when it becomes enforced. The fifth line, the
// active state, changes in milestone P4.
func setModeBlockHead(t *testing.T) string {
	t.Helper()

	predicate, err := lessoncheck.Describe(reviews.CheckDef{Kind: string(lessoncheck.KindForbidPathChange)})
	require.NoError(t, err)

	return "pin " + setModePinRef + " frozen-assets: check frozen-assets (forbid-path-change/1, " +
		setModeShortForbid + "), mode enforce\n" +
		"  scope unchanged\n" +
		"  predicate: " + predicate + "\n" +
		"  version " + setModeShortPlain + " -> " + setModeShortEnf + "\n"
}

// TestLessonsSetMode proves the K6 success path (AC-27): the command
// writes only the mode and check keys of the pin, prints the impact
// block, the K6 success line, the requested mode, the active state, and
// the supported paths, and exits 0. An unchanged request exits 0
// without a write, also with the gate off.
func TestLessonsSetMode(t *testing.T) {
	t.Run("enforce binds a check and writes both keys", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)
		tree := setModeTree(t, root)

		stdout, stderr := goldenRun(t, root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef,
			"--check", "frozen-assets")
		assert.Empty(t, stderr)

		assert.True(t, strings.HasPrefix(stdout, setModeBlockHead(t)), stdout)
		assert.Contains(t, stdout, "\npin "+setModePinRef+": mode advisory -> enforce, check none -> frozen-assets; "+
			"version "+setModeShortEnf+"\n")
		assert.Contains(t, stdout, "\nrequested mode: enforce\n")
		assert.Contains(t, stdout, "requested; inactive: ", "the active line keeps this part in every milestone")
		assert.Contains(t, stdout, "seamark lessons --verify")
		assert.Contains(t, stdout, "seamark check --lessons")

		content, mode := maintainLessons(t, root)
		assert.Equal(t, setModeYAMLEnforced, content, "no top-level key, no checks entry, no other pin changes")
		assert.Equal(t, os.FileMode(0o600), mode, "the write keeps the mode bits")
		assert.Equal(t, tree, setModeTree(t, root), "no index, no hook, no other file")

		sentinel.requireNoCall(t)
	})

	t.Run("advisory removes the mode and keeps the check", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAMLEnforced, true)

		after, err := reviews.ParseConfig([]byte(setModeYAMLAdvisory))
		require.NoError(t, err)

		stdout, stderr := goldenRun(t, root, "lessons", "--set-mode", "advisory", "--pin", setModePinRef)
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "\npin "+setModePinRef+": mode enforce -> advisory, check frozen-assets -> "+
			"frozen-assets; version "+after.PinVersion(after.Pin[0])[:12]+"\n")
		assert.Contains(t, stdout, "\nrequested mode: advisory\n")

		content, _ := maintainLessons(t, root)
		assert.Equal(t, setModeYAMLAdvisory, content)

		sentinel.requireNoCall(t)
	})

	t.Run("check binds a check to an advisory pin of the other entry", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)

		stdout, stderr := goldenRun(t, root, "lessons", "--set-mode", "check", "--pin", " ED5D4AF5 ",
			"--check", "no-todo")
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "pin "+setModeSecondRef+": mode advisory -> check, check none -> no-todo; ")

		content, _ := maintainLessons(t, root)
		assert.Equal(t, strings.Replace(setModeYAML, "    note: Reject unknown fields in the parser.\n",
			"    note: Reject unknown fields in the parser.\n    mode: check\n    check: no-todo\n", 1), content)

		sentinel.requireNoCall(t)
	})

	t.Run("an unchanged request exits 0 without a write", func(t *testing.T) {
		for _, gateOn := range []bool{true, false} {
			root, sentinel := setModeWorkspace(t, setModeYAMLEnforced, gateOn)

			stdout, stderr := goldenRun(t, root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef)
			assert.Empty(t, stderr)
			assert.Equal(t, "pin "+setModePinRef+" already has mode enforce and check frozen-assets; "+
				"nothing to change\n", stdout, "gate on: %t", gateOn)

			content, _ := maintainLessons(t, root)
			assert.Equal(t, setModeYAMLEnforced, content)

			sentinel.requireNoCall(t)
		}
	})
}

// TestLessonsSetModeRefusals proves the K6 message table and the §4
// flag rules. Every refusal exits 1, prints nothing on stdout, and
// leaves the pin file as it was (AC-27). A text that a later milestone
// extends is asserted by its kept head and tail only (§4.2 subset
// rule, F8).
func TestLessonsSetModeRefusals(t *testing.T) {
	invalidOther := strings.Replace(setModeYAML, "    note: Reject unknown fields in the parser.\n",
		"    note: Reject unknown fields in the parser.\n    mode: check\n", 1)

	cases := []struct {
		name    string
		lessons string
		args    []string
		want    string
	}{
		{"no --pin", setModeYAML, []string{"--set-mode", "check"}, "--set-mode needs --pin <pin-ref>"},
		{"an empty --pin", setModeYAML, []string{"--set-mode", "check", "--pin", " "}, "--set-mode needs --pin <pin-ref>"},
		{
			"a bad mode", setModeYAML, []string{"--set-mode", "strict", "--pin", setModePinRef},
			`--set-mode takes advisory, check, or enforce, got "strict"`,
		},
		{
			"an empty mode", setModeYAML, []string{"--set-mode", "", "--pin", setModePinRef},
			`--set-mode takes advisory, check, or enforce, got ""`,
		},
		{"--check without --set-mode", setModeYAML, []string{"--check", "frozen-assets"}, "--check applies to --set-mode only"},
		{
			"no check for enforce", setModeYAML, []string{"--set-mode", "enforce", "--pin", setModePinRef},
			"--set-mode enforce needs --check <check-id>: pin " + setModePinRef + " has no check",
		},
		{
			"an unknown check", setModeYAML,
			[]string{"--set-mode", "check", "--pin", setModePinRef, "--check", "no-such"},
			`check "no-such" is not defined under checks in .seamark/lessons.yaml; define it by hand, then rerun`,
		},
		{
			"an invalid candidate", invalidOther,
			[]string{"--set-mode", "enforce", "--pin", setModePinRef, "--check", "frozen-assets"},
			"lessons --set-mode: the result would not validate: pin 2 (keep-parser-strict @ internal/parse): " +
				"mode check requires check: <check-id> (and 0 more); nothing was written",
		},
		{
			"a malformed pin-ref", setModeYAML, []string{"--set-mode", "check", "--pin", "3215-e7b4"},
			`pin reference "3215-e7b4": give at least 8 hex characters from the pin column of ` +
				"`seamark lessons --verify`",
		},
		{
			"an empty --check", setModeYAML,
			[]string{"--set-mode", "check", "--pin", setModePinRef, "--check", ""},
			"--check needs a check ID: seamark lessons --set-mode <mode> --pin <pin-ref> --check <check-id>",
		},
		{
			"another mode", setModeYAML, []string{"--set-mode", "check", "--pin", setModePinRef, "--verify"},
			"--set-mode does not combine with --verify",
		},
		{
			"a decision flag", setModeYAML, []string{"--set-mode", "check", "--pin", setModePinRef, "--apply", "p1"},
			"--set-mode does not combine with --apply",
		},
		{
			"a lookup file", setModeYAML, []string{"--set-mode", "check", "--pin", setModePinRef, "--file", "a.go"},
			"--set-mode does not combine with --file",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := setModeWorkspace(t, c.lessons, true)

			out, _, err := runErr(t, append([]string{"-C", root, "lessons"}, c.args...)...)
			require.Error(t, err)
			assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1, not 2")
			assert.NotErrorIs(t, err, lessoncheck.ErrViolation, "a refusal exits 1, not 2")
			assert.Equal(t, c.want, err.Error())
			assert.Empty(t, out)

			content, _ := maintainLessons(t, root)
			assert.Equal(t, c.lessons, content, "nothing is written")

			sentinel.requireNoCall(t)
		})
	}

	t.Run("--pin applies to --set-mode only", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)

		for _, args := range [][]string{
			{"lessons", "--pin", setModePinRef},
			{"lessons", "--list", "--pin", setModePinRef},
			{"lessons", "--verify", "--pin", setModePinRef},
		} {
			out, _, err := runErr(t, append([]string{"-C", root}, args...)...)
			require.Error(t, err, "%v", args)
			assert.True(t, strings.HasPrefix(err.Error(), "--pin applies to --set-mode"), "%v: %s", args, err)
			assert.True(t, strings.HasSuffix(err.Error(), " only"), "%v: %s", args, err)
			assert.Empty(t, out, "%v", args)
		}

		content, _ := maintainLessons(t, root)
		assert.Equal(t, setModeYAML, content)

		sentinel.requireNoCall(t)
	})

	t.Run("a missing pin-ref keeps the K2 head", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)

		out, _, err := runErr(t, "-C", root, "lessons", "--set-mode", "check", "--pin", "00000000")
		require.Error(t, err)
		assert.ErrorIs(t, err, reviews.ErrPinRef)
		assert.True(t, strings.HasPrefix(err.Error(), "pin reference 00000000 names no pin in .seamark/lessons.yaml; "+
			"`seamark lessons --verify` lists the current references"), err.Error())
		assert.Empty(t, out)

		content, _ := maintainLessons(t, root)
		assert.Equal(t, setModeYAML, content)

		sentinel.requireNoCall(t)
	})

	t.Run("a positional argument is refused", func(t *testing.T) {
		root, sentinel := setModeWorkspace(t, setModeYAML, true)

		_, _, err := runErr(t, "-C", root, "lessons", "--set-mode", "check", "--pin", setModePinRef, "extra")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), `unexpected argument "extra"`), err.Error())

		content, _ := maintainLessons(t, root)
		assert.Equal(t, setModeYAML, content)

		sentinel.requireNoCall(t)
	})
}

// TestLessonsSetModeWriteGateOff proves §5 row 28: with distill.write
// off, the command prints the checker impact, refuses with the K6 text
// and the lines to set by hand, exits 1, and writes nothing.
func TestLessonsSetModeWriteGateOff(t *testing.T) {
	const head = "distill.write is off in .seamark/config.yaml — nothing was written; set these lines on pin "

	for _, c := range []struct {
		name    string
		lessons string
		args    []string
		want    string
		block   bool
	}{
		{
			"enforce with a check", setModeYAML,
			[]string{"--set-mode", "enforce", "--pin", setModePinRef, "--check", "frozen-assets"},
			head + setModePinRef + " by hand:\n  mode: enforce\n  check: frozen-assets", true,
		},
		{
			"advisory", setModeYAMLEnforced, []string{"--set-mode", "advisory", "--pin", setModePinRef},
			head + setModePinRef + " by hand:\n  mode: advisory", true,
		},
		{
			"a check binding of an advisory pin", setModeYAML,
			[]string{"--set-mode", "advisory", "--pin", setModePinRef, "--check", "frozen-assets"},
			head + setModePinRef + " by hand:\n  check: frozen-assets", false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, sentinel := setModeWorkspace(t, c.lessons, false)

			out, _, err := runErr(t, append([]string{"-C", root, "lessons"}, c.args...)...)
			require.Error(t, err)
			assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1, not 2")
			assert.Equal(t, c.want, err.Error())

			if c.block {
				assert.True(t, strings.HasPrefix(out, "pin "+setModePinRef+" frozen-assets: check frozen-assets"), out)
			} else {
				assert.Empty(t, out, "an advisory pin has no impact block")
			}

			content, _ := maintainLessons(t, root)
			assert.Equal(t, c.lessons, content, "nothing is written")

			sentinel.requireNoCall(t)
		})
	}
}

// TestLessonsSetModeNeverInvokes proves AC-1 for --set-mode: a write,
// an unchanged request, a gate-off answer, and a refusal start no agent
// CLI, no claude, codex, or gh, and create no index.
func TestLessonsSetModeNeverInvokes(t *testing.T) {
	root, sentinel := setModeWorkspace(t, setModeYAML, true)
	set := []string{"-C", root, "lessons", "--set-mode", "enforce", "--pin", setModePinRef, "--check", "frozen-assets"}

	_, _, err := runErr(t, set...)
	require.NoError(t, err, "the write")

	_, _, err = runErr(t, set...)
	require.NoError(t, err, "the unchanged request")

	_, _, err = runErr(t, "-C", root, "lessons", "--set-mode", "check", "--pin", setModePinRef, "--check", "no-such")
	require.Error(t, err, "a refusal")

	// The gate turns off in place, so the agent section of the sentinel
	// stays in config.yaml.
	configPath := filepath.Join(root, ".seamark", "config.yaml")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "write: true\n")
	require.NoError(t, os.WriteFile(configPath,
		[]byte(strings.Replace(string(config), "write: true\n", "write: false\n", 1)), 0o644))

	_, _, err = runErr(t, "-C", root, "lessons", "--set-mode", "advisory", "--pin", setModePinRef)
	require.Error(t, err, "the gate-off answer")

	_, statErr := os.Stat(filepath.Join(root, ".seamark", "index.db"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "the command opens no index")

	content, _ := maintainLessons(t, root)
	assert.Equal(t, setModeYAMLEnforced, content)

	sentinel.requireNoCall(t)
}
