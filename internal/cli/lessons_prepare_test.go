package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests of lessons --prepare. Every test builds a temporary
// repository. Nothing reads the repository of this checkout.

// prepareLessonsYAML holds three pins. keep-main-small covers a.go,
// frozen-assets is an enforce pin with a check on generated/, and
// prefer-errors-is stays advisory on src/.
const prepareLessonsYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: keep-main-small
    region: a.go
    note: Move the work into helpers.
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: prefer-errors-is
    region: src/
    note: Compare errors with errors.Is.
`

// prepareRepo builds a committed repository with prepareLessonsYAML.
// With indexed, it also runs `seamark index` and stores one mined lesson
// (RUF001) on a.go. Without it, the repository has no index database.
func prepareRepo(t *testing.T, indexed bool) string {
	t.Helper()

	// gitify and the commands run git. A global or system git
	// configuration must not change what they see.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := writeFixture(t)
	verifyWrite(t, root, "generated/a.txt", "asset\n")
	verifyWrite(t, root, "src/handler.go", "package src\n\nfunc Handle() {}\n")
	verifyWrite(t, root, "docs/readme.md", "readme\n")
	verifyWrite(t, root, ".seamark/lessons.yaml", prepareLessonsYAML)
	gitify(t, root)

	if indexed {
		_, err := run(t, "-C", root, "index")
		require.NoError(t, err)

		seedLesson(t, root, "a.go", "RUF001", 4)
	}

	return root
}

// prepareRun runs `seamark -C root lessons --prepare <args>` in the test
// process.
func prepareRun(t *testing.T, root string, args ...string) (stdout string, err error) {
	t.Helper()

	stdout, _, err = runIn(t, "", append([]string{"-C", root, "lessons", "--prepare"}, args...)...)

	return stdout, err
}

// prepareJSON runs lessons --prepare with --json and decodes stdout as
// exactly one lesson-preparation/1 document.
func prepareJSON(t *testing.T, root string, args ...string) delivery.Preparation {
	t.Helper()

	stdout, err := prepareRun(t, root, append(args, "--json")...)
	require.NoError(t, err)

	dec := json.NewDecoder(strings.NewReader(stdout))

	var p delivery.Preparation
	require.NoError(t, dec.Decode(&p), "stdout: %s", stdout)

	var extra json.RawMessage
	require.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout holds one JSON document only")

	return p
}

// prepareRefuse runs `seamark -C root lessons <args>` and requires a
// refusal: an error that exits 1 and an empty stdout. It returns the
// error text without the `seamark: ` prefix that Execute adds.
func prepareRefuse(t *testing.T, root string, args ...string) string {
	t.Helper()

	stdout, _, err := runIn(t, "", append([]string{"-C", root, "lessons"}, args...)...)
	require.Error(t, err, "args %v", args)
	assert.NotErrorIs(t, err, lessoncheck.ErrViolation, "a refusal exits 1, args %v", args)
	assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1, args %v", args)
	assert.Empty(t, stdout, "a refusal prints nothing on stdout, args %v", args)

	return err.Error()
}

// preparedPaths returns the resolved paths of p in request order.
func preparedPaths(p delivery.Preparation) []string {
	paths := make([]string, 0, len(p.Request.Paths))

	for _, sp := range p.Request.Paths {
		paths = append(paths, sp.Path)
	}

	return paths
}

// preparedRules returns the rules of the selected pins of p.
func preparedRules(p delivery.Preparation) []string {
	rules := make([]string, 0, len(p.Pins))

	for _, pin := range p.Pins {
		rules = append(rules, pin.Rule)
	}

	return rules
}

// prepareFirings returns the firing records of root and requires that
// each one comes from the prepare surface.
func prepareFirings(t *testing.T, root string) []reviews.Firing {
	t.Helper()

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	for _, f := range firings {
		assert.Equal(t, string(delivery.SurfacePrepare), f.Surface)
	}

	return firings
}

// prepareFiles is treeListing without the directories and without the
// -wal and -shm files of the index. A read-only open of the index, also
// the one of workspace.Probe, lets SQLite create these two files. They
// are no write of seamark, and index.db itself stays byte for byte.
// A new file still shows as a new path, so a directory entry adds
// nothing.
func prepareFiles(t *testing.T, root string) map[string]string {
	t.Helper()

	listing := treeListing(t, root)
	db := filepath.Join(root, ".seamark", "index.db")

	for path, entry := range listing {
		if strings.HasPrefix(entry, "d") || path == db+"-wal" || path == db+"-shm" {
			delete(listing, path)
		}
	}

	return listing
}

// requireNoDeliveryState fails when the edit hook state, the
// exploration state, or one of their lock files exists under root. A
// preparation never touches them (contract L1 Behavior 6, AC-26).
func requireNoDeliveryState(t *testing.T, root string) {
	t.Helper()

	for _, name := range []string{
		"lessons-hook-state.json", "lessons-hook-state.lock",
		"lessons-explore-state.json", "lessons-explore-state.lock",
	} {
		assert.NoFileExists(t, filepath.Join(root, ".seamark", name))
	}
}

// TestLessonsPrepareJSONAndText proves the two output forms of contract
// L1: --json prints only the lesson-preparation/1 document, and the text
// is RenderPreparation of the same preparation in the full form. Each
// call records one firing record of the prepare surface. It also proves
// the mined status of each index state (L1 Behavior 5).
func TestLessonsPrepareJSONAndText(t *testing.T) {
	root := prepareRepo(t, true)
	main := filepath.Join(root, "a.go")
	asset := filepath.Join(root, "generated", "new.txt")

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	doc := prepareJSON(t, root, main, asset)

	t.Run("the document", func(t *testing.T) {
		assert.Equal(t, delivery.PreparationSchema, doc.Schema)
		assert.Equal(t, version, doc.SeamarkVersion)
		assert.Equal(t, delivery.SurfacePrepare, doc.Surface)
		assert.Equal(t, delivery.Receiver{Lane: "unknown", Reason: "no-receiver-identity"}, doc.Receiver)

		assert.Equal(t, []string{"a.go", "generated/new.txt"}, preparedPaths(doc))
		assert.Equal(t, "file", doc.Request.Paths[0].State)
		assert.Equal(t, "absent", doc.Request.Paths[1].State, "a new path does not need to exist")
		assert.Zero(t, doc.Request.RejectedCount)

		assert.Equal(t, "ok", doc.Guidance.Status)
		assert.Equal(t, cfg.GuidanceDigest(), doc.Guidance.GuidanceSHA256)
		assert.Equal(t, delivery.MinedOK, doc.Guidance.MinedStatus)
		assert.Equal(t, "valid", doc.Guidance.ChecksStatus)

		require.ElementsMatch(t, []string{"keep-main-small", "frozen-assets"}, preparedRules(doc))

		for _, pin := range doc.Pins {
			i := pin.Entry - 1
			require.True(t, i >= 0 && i < len(cfg.Pin), "entry %d", pin.Entry)

			key := reviews.NewPinKey(cfg.Pin[i].Rule, cfg.Pin[i].Region, cfg.Pin[i].Regions)
			assert.Equal(t, cfg.Pin[i].Rule, pin.Rule)
			assert.Equal(t, cfg.PinVersion(cfg.Pin[i]), pin.PinVersion, "pin %s", pin.Rule)
			assert.Equal(t, cfg.ShortPinRef(key), pin.PinRef, "pin %s", pin.Rule)
			assert.Equal(t, reviews.PinIdentityDigest(key), pin.PinIdentity, "pin %s", pin.Rule)
		}

		require.Len(t, doc.Lessons, 1)
		assert.Equal(t, "RUF001", doc.Lessons[0].Symptom)

		require.Len(t, doc.Checks, 1)
		assert.Equal(t, "enforce", doc.Checks[0].Mode)
		assert.True(t, doc.Checks[0].Runs)
		assert.Equal(t, "frozen-assets", doc.Checks[0].Check.ID)
		assert.Equal(t, []string{"generated/new.txt"}, doc.Checks[0].Paths)

		assert.Equal(t, []string{"paths-added", "guidance-changed", "receiver-changed"}, doc.Refresh)
		assert.Equal(t, delivery.PreparationNotice, doc.Notice)
		assert.NotEmpty(t, doc.EventID, "the call records its event")
	})

	t.Run("the firing record", func(t *testing.T) {
		firings := prepareFirings(t, root)
		require.Len(t, firings, 1)

		rec := firings[0]
		assert.Equal(t, doc.EventID, rec.EventID)
		assert.Equal(t, doc.Request.ScopeSHA256, rec.ScopeSHA)
		assert.Equal(t, []string{"a.go", "generated/new.txt"}, rec.Files)
		assert.Equal(t, "returned", rec.Emission)
		assert.Empty(t, rec.SessionSHA, "a preparation has no receiver identity")
		assert.Empty(t, rec.ContextSHA, "a preparation has no receiver identity")
	})

	t.Run("the text is the full form of the same preparation", func(t *testing.T) {
		text, err := prepareRun(t, root, main, asset)
		require.NoError(t, err)

		// The text holds no time and no event ID, so a second call
		// renders the same bytes as the first document.
		var want bytes.Buffer
		delivery.RenderPreparation(&want, &doc, delivery.FormFull)
		assert.Equal(t, want.String(), text)

		assert.True(t, strings.HasPrefix(text,
			"lesson preparation (lesson-preparation/1) for 2 path(s) — receiver unknown\n"), "text:\n%s", text)
		assert.Contains(t, text, delivery.PreparationNotice)

		firings := prepareFirings(t, root)
		require.Len(t, firings, 2, "each call records one event")
		assert.NotEqual(t, firings[0].EventID, firings[1].EventID)
	})

	t.Run("without an index the pins still apply", func(t *testing.T) {
		bare := prepareRepo(t, false)

		p := prepareJSON(t, bare, filepath.Join(bare, "a.go"))
		assert.Equal(t, delivery.MinedNoIndex, p.Guidance.MinedStatus)
		assert.Equal(t, []string{"keep-main-small"}, preparedRules(p))
		assert.Empty(t, p.Lessons)
		assert.NoFileExists(t, filepath.Join(bare, ".seamark", "index.db"), "a preparation creates no index")
	})

	t.Run("an index that this binary cannot read", func(t *testing.T) {
		bare := prepareRepo(t, false)
		db := filepath.Join(bare, ".seamark", "index.db")
		verifyWrite(t, bare, ".seamark/index.db", "not a database\n")

		p := prepareJSON(t, bare, filepath.Join(bare, "a.go"))
		assert.Equal(t, delivery.MinedUnavailable, p.Guidance.MinedStatus)
		assert.Equal(t, []string{"keep-main-small"}, preparedRules(p))

		data, err := os.ReadFile(db)
		require.NoError(t, err)
		assert.Equal(t, "not a database\n", string(data), "a preparation never repairs or migrates the index")
	})

	t.Run("a lessons.yaml that does not decode", func(t *testing.T) {
		bare := prepareRepo(t, false)
		verifyWrite(t, bare, ".seamark/lessons.yaml", "pin: [\n")

		p := prepareJSON(t, bare, filepath.Join(bare, "a.go"))
		assert.Equal(t, "invalid", p.Guidance.Status)
		assert.Empty(t, p.Guidance.GuidanceSHA256)
		assert.Equal(t, "invalid", p.Guidance.ChecksStatus)
		assert.Empty(t, p.Pins)
	})
}

// TestLessonsPrepareRefusesEveryOtherMode proves the guards of contracts
// §4.1 and §4.2 for --prepare. Every refusal happens before the dispatch,
// prints nothing on stdout, writes no file, and calls no model. So the
// prefix grant `seamark lessons --prepare *` reaches no command that
// writes. The texts that a later milestone extends are asserted by the
// plan rule of table (e): the head, one Contains per mode of milestone
// P3, and the tail.
func TestLessonsPrepareRefusesEveryOtherMode(t *testing.T) {
	root := prepareRepo(t, true)
	sentinel := newInvokerSentinel(t, root)

	before := treeListing(t, root)

	t.Run("each other mode and decision flag", func(t *testing.T) {
		for _, c := range []struct {
			args []string
			flag string
		}{
			{[]string{"--file", "a.go"}, "--file"},
			{[]string{"--list"}, "--list"},
			{[]string{"--region", "src"}, "--region"},
			{[]string{"--stats"}, "--stats"},
			{[]string{"--distill"}, "--distill"},
			{[]string{"--proposals"}, "--proposals"},
			{[]string{"--verify"}, "--verify"},
			{[]string{"--set-mode", "check", "--pin", "0123456789ab"}, "--set-mode"},
			{[]string{"--apply", "p1"}, "--apply"},
			{[]string{"--dismiss", "p1"}, "--dismiss"},
			{[]string{"--prune", "p1"}, "--prune"},
			{[]string{"--retarget", "p1"}, "--retarget"},
			{[]string{"--extract-triggers"}, "--extract-triggers"},
			{[]string{"--hook"}, "--hook"},
			{[]string{"--hook-reset"}, "--hook-reset"},
			// The first other mode in the help order names the conflict.
			{[]string{"--stats", "--list"}, "--list"},
			{[]string{"--set-mode", "check", "--pin", "0123456789ab", "--verify"}, "--verify"},
			{[]string{"--apply", "p1", "--dismiss", "p2"}, "--apply"},
			// Two --file values name --file, not the --verify rule.
			{[]string{"--file", "a.go", "--file", "src/handler.go"}, "--file"},
		} {
			got := prepareRefuse(t, root, append([]string{"--prepare", "a.go"}, c.args...)...)
			assert.Equal(t, "--prepare does not combine with "+c.flag, got, "args %v", c.args)

			// The flag order of the call does not matter.
			got = prepareRefuse(t, root, append(c.args, "--prepare", "a.go")...)
			assert.Equal(t, "--prepare does not combine with "+c.flag, got, "args %v first", c.args)
		}
	})

	t.Run("each modifier flag keeps its own refusal", func(t *testing.T) {
		for _, c := range []struct {
			args []string
			flag string
		}{
			{[]string{"--confirm-checks"}, "--confirm-checks"},
			{[]string{"--dry-run"}, "--dry-run"},
			{[]string{"--pin", "0123456789ab"}, "--pin"},
			{[]string{"--check", "frozen-assets"}, "--check"},
			{[]string{"--client", "codex"}, "--client"},
		} {
			got := prepareRefuse(t, root, append([]string{"--prepare", "a.go"}, c.args...)...)
			assert.True(t, strings.HasPrefix(got, c.flag+" "), "args %v: got %q", c.args, got)
		}
	})

	t.Run("--json names --prepare", func(t *testing.T) {
		for _, args := range [][]string{{"--json"}, {"--json", "--list"}, {"--json", "--file", "a.go"}} {
			got := prepareRefuse(t, root, args...)
			assert.True(t, strings.HasPrefix(got, "--json applies to --verify"), "got %q", got)
			assert.Contains(t, got, "--prepare")
			assert.True(t, strings.HasSuffix(got, " only"), "got %q", got)
		}
	})

	t.Run("the default error names --prepare", func(t *testing.T) {
		got := prepareRefuse(t, root)
		assert.True(t, strings.HasPrefix(got, "provide --file <path>, --list, --stats, --distill, --proposals, --verify"),
			"got %q", got)
		assert.Contains(t, got, ", --prepare,")
		assert.True(t, strings.HasSuffix(got, ", --apply, --dismiss, or --hook"), "got %q", got)
	})

	t.Run("a path without --prepare stays an unexpected argument", func(t *testing.T) {
		got := prepareRefuse(t, root, "a.go")
		assert.True(t, strings.HasPrefix(got, `unexpected argument "a.go"`), "got %q", got)

		got = prepareRefuse(t, root, "--verify", "a.go")
		assert.True(t, strings.HasPrefix(got, `unexpected argument "a.go"`), "got %q", got)
	})

	assert.Equal(t, before, treeListing(t, root), "a refusal writes no file, not even a firing record")
	requireNoDeliveryState(t, root)

	sentinel.requireNoCall(t)
}

// TestLessonsPrepareNeedsAPathInsideTheWorkspace proves the L1 errors
// for the path set and their exit code 1 through the real Execute. A
// partial rejection, a path without pins, and a new path still prepare
// with exit 0.
func TestLessonsPrepareNeedsAPathInsideTheWorkspace(t *testing.T) {
	root := prepareRepo(t, true)

	// The preparation cuts a long input path, and a long temp folder makes
	// a temp path too long to show in full. A relative path stays short. It
	// starts at the process directory: the package directory in this process
	// and root in a child process. Neither puts the file inside root.
	outside := filepath.Join("..", "x.go")

	before := prepareFiles(t, root)

	t.Run("no path", func(t *testing.T) {
		const want = "--prepare needs at least one path: seamark lessons --prepare <path>..."

		assert.Equal(t, want, prepareRefuse(t, root, "--prepare"))
		assert.Equal(t, want, prepareRefuse(t, root, "--prepare", "--json"))

		stdout, stderr, code := seamarkProcess(t, root, "-C", root, "lessons", "--prepare")
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout)
		assert.Equal(t, []string{"seamark: " + want}, seamarkLines(stderr))
	})

	t.Run("no path inside the workspace", func(t *testing.T) {
		want := `no path of --prepare is inside the workspace: 3 rejected (first "` + outside +
			`": outside the workspace)`

		assert.Equal(t, want, prepareRefuse(t, root, "--prepare", outside, "", root))
		assert.Equal(t, want, prepareRefuse(t, root, "--prepare", outside, "", root, "--json"))

		assert.Equal(t, `no path of --prepare is inside the workspace: 1 rejected (first "": malformed path)`,
			prepareRefuse(t, root, "--prepare", ""))

		stdout, stderr, code := seamarkProcess(t, root, "-C", root, "lessons", "--prepare", outside, "", root, "--json")
		assert.Equal(t, 1, code, "a preparation never exits 2")
		assert.Empty(t, stdout, "no JSON on a refusal")
		assert.Equal(t, []string{"seamark: " + want}, seamarkLines(stderr))
	})

	assert.Equal(t, before, prepareFiles(t, root), "a refused preparation writes no file")

	t.Run("a partial rejection prepares the paths inside", func(t *testing.T) {
		p := prepareJSON(t, root, outside, filepath.Join(root, "a.go"))
		assert.Equal(t, []string{"a.go"}, preparedPaths(p))
		assert.Equal(t, 1, p.Request.RejectedCount)
		require.Len(t, p.Request.Rejected, 1)
		assert.Equal(t, delivery.RejectedInput{Path: outside, Reason: "outside the workspace"}, p.Request.Rejected[0])

		text, err := prepareRun(t, root, outside, filepath.Join(root, "a.go"))
		require.NoError(t, err)
		assert.Contains(t, text, outside, "the text lists the rejected path")

		_, _, code := seamarkProcess(t, root, "-C", root, "lessons", "--prepare", outside, filepath.Join(root, "a.go"))
		assert.Equal(t, 0, code)
	})

	t.Run("a path without pins and a new path prepare too", func(t *testing.T) {
		p := prepareJSON(t, root, filepath.Join(root, "docs", "readme.md"))
		assert.Equal(t, []string{"docs/readme.md"}, preparedPaths(p))
		assert.Empty(t, p.Pins)
		assert.Empty(t, p.Checks)

		p = prepareJSON(t, root, filepath.Join(root, "generated", "next", "b.txt"))
		require.Len(t, p.Request.Paths, 1)
		assert.Equal(t, "absent", p.Request.Paths[0].State)
		assert.Equal(t, []string{"frozen-assets"}, preparedRules(p))
	})
}

// TestLessonsPrepareNeverInvokes proves AC-1 for lessons --prepare: both
// output forms, a partial rejection, a repository without an index, and
// a lessons.yaml that does not decode run without an agent, a model
// CLI, or the network. The command writes no file except the firing
// records of the prepare surface: no index, no delivery state.
func TestLessonsPrepareNeverInvokes(t *testing.T) {
	root := prepareRepo(t, true)
	sentinel := newInvokerSentinel(t, root)

	// A model call would need an agent: agent.argv names the stub. A
	// default agent would start claude or codex, which are stubs too.
	config, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(config), "argv:")

	outside := filepath.Join(t.TempDir(), "x.go")
	main := filepath.Join(root, "a.go")
	audit := reviews.AuditLogPath(root)

	before := prepareFiles(t, root)

	for _, args := range [][]string{
		{main},
		{main, "--json"},
		{main, outside},
		{filepath.Join(root, "generated", "a.txt"), filepath.Join(root, "src", "new.go"), "--json"},
	} {
		_, err := prepareRun(t, root, args...)
		require.NoError(t, err, "args %v", args)
	}

	firings := prepareFirings(t, root)
	assert.Len(t, firings, 4, "each call with a selected lesson records one event")

	verifyWrite(t, root, ".seamark/lessons.yaml", "pin: [\n")

	_, err = prepareRun(t, root, main)
	require.NoError(t, err, "an invalid lessons.yaml still prepares")

	after := prepareFiles(t, root)

	// The firing log and the rewritten lessons.yaml are the only changes.
	for _, path := range []string{audit, filepath.Join(root, ".seamark", "lessons.yaml")} {
		delete(before, path)
		delete(after, path)
	}

	assert.Equal(t, before, after, "lessons --prepare writes no file except its firing records")
	requireNoDeliveryState(t, root)

	t.Run("without an index", func(t *testing.T) {
		bare := prepareRepo(t, false)
		bareSentinel := newInvokerSentinel(t, bare)

		bareBefore := prepareFiles(t, bare)

		_, err := prepareRun(t, bare, filepath.Join(bare, "a.go"), "--json")
		require.NoError(t, err)

		bareAfter := prepareFiles(t, bare)
		delete(bareAfter, reviews.AuditLogPath(bare))
		assert.Equal(t, bareBefore, bareAfter, "no index and no state file appear")
		requireNoDeliveryState(t, bare)

		bareSentinel.requireNoCall(t)
	})

	sentinel.requireNoCall(t)
}

// TestLessonsPrepareResolvesPathsFromTheProcessDirectory proves that a
// relative path starts at the process directory, not at the -C
// directory or the repository root (contract L1, §1.3).
func TestLessonsPrepareResolvesPathsFromTheProcessDirectory(t *testing.T) {
	root := prepareRepo(t, true)

	t.Run("from a subdirectory", func(t *testing.T) {
		t.Chdir(filepath.Join(root, "src"))

		p := prepareJSON(t, root, "handler.go", "../a.go", "./new.go")
		assert.Equal(t, []string{"src/handler.go", "a.go", "src/new.go"}, preparedPaths(p))
		assert.Equal(t, "absent", p.Request.Paths[2].State)
		assert.Equal(t, []string{"handler.go", "../a.go", "./new.go"}, p.Request.Requested,
			"the document keeps the paths as given")
	})

	t.Run("-C does not anchor a relative path", func(t *testing.T) {
		t.Chdir(root)

		// -C names a subdirectory. The workspace widens to the git top
		// level, and a.go still starts at the process directory.
		p := prepareJSON(t, filepath.Join(root, "src"), "a.go")
		assert.Equal(t, []string{"a.go"}, preparedPaths(p))
	})

	t.Run("outside the repository", func(t *testing.T) {
		t.Chdir(t.TempDir())

		got := prepareRefuse(t, root, "--prepare", "a.go")
		assert.Equal(t, `no path of --prepare is inside the workspace: 1 rejected (first "a.go": outside the workspace)`, got)
	})

	t.Run("the process directory of a real process", func(t *testing.T) {
		stdout, _, code := seamarkProcess(t, filepath.Join(root, "src"),
			"-C", root, "lessons", "--prepare", "handler.go", "--json")
		require.Equal(t, 0, code)

		var p delivery.Preparation
		require.NoError(t, json.Unmarshal([]byte(stdout), &p), "stdout: %s", stdout)
		assert.Equal(t, []string{"src/handler.go"}, preparedPaths(p))
		assert.Equal(t, []string{"prefer-errors-is"}, preparedRules(p))
	})
}

// mcpPreparations serves one `seamark mcp` session in root under the
// 2025-06-18 protocol with one change_set call for each path set. It
// returns the lesson_preparation of each call, in call order.
func mcpPreparations(t *testing.T, root string, calls ...[]string) []delivery.Preparation {
	t.Helper()

	requests := []string{`{"jsonrpc":"2.0","id":0,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test"}}}`}

	for i, files := range calls {
		params, err := json.Marshal(map[string]any{"name": "change_set", "arguments": map[string]any{"files": files}})
		require.NoError(t, err)

		requests = append(requests,
			`{"jsonrpc":"2.0","id":`+strconv.Itoa(i+1)+`,"method":"tools/call","params":`+string(params)+`}`)
	}

	byID := map[int]delivery.Preparation{}

	for line := range strings.SplitSeq(runMCP(t, root, requests...), "\n") {
		// runMCP appends the server log to the protocol frames. Only a
		// frame starts with a brace.
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var resp struct {
			ID     int `json:"id"`
			Result struct {
				IsError           bool `json:"isError"`
				StructuredContent struct {
					LessonPreparation *delivery.Preparation `json:"lesson_preparation"`
				} `json:"structuredContent"`
			} `json:"result"`
		}

		require.NoError(t, json.Unmarshal([]byte(line), &resp), "frame: %s", line)

		if resp.ID == 0 {
			continue
		}

		require.False(t, resp.Result.IsError, "frame: %s", line)
		require.NotNil(t, resp.Result.StructuredContent.LessonPreparation, "frame: %s", line)

		byID[resp.ID] = *resp.Result.StructuredContent.LessonPreparation
	}

	out := make([]delivery.Preparation, len(calls))

	for i := range calls {
		p, ok := byID[i+1]
		require.True(t, ok, "change_set call %d has a result", i+1)

		out[i] = p
	}

	return out
}

// TestLessonsPrepareMatchesChangeSet proves the AC-25 parity of contract
// L1 (plan step P3.8): lessons --prepare and MCP change_set return one
// preparation for one file, for many files, and for new paths. Only the
// surface, the time, and the event ID differ. A rewrite of lessons.yaml
// moves the guidance digest and the pin version on both surfaces alike.
//
// The paths are absolute, because the CLI resolves a relative path from
// the process directory and MCP from the workspace root.
func TestLessonsPrepareMatchesChangeSet(t *testing.T) {
	root := prepareRepo(t, true)
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	cases := []struct {
		name  string
		paths []string
	}{
		{name: "one file", paths: []string{abs("a.go")}},
		{name: "many files", paths: []string{abs("a.go"), abs("generated/a.txt"), abs("src/handler.go")}},
		{name: "new paths", paths: []string{abs("src/new.go"), abs("generated/b.txt")}},
	}

	// compare prepares every case on both surfaces and returns the MCP
	// preparations.
	compare := func(t *testing.T) []delivery.Preparation {
		t.Helper()

		calls := make([][]string, len(cases))
		for i, c := range cases {
			calls[i] = c.paths
		}

		served := mcpPreparations(t, root, calls...)

		for i, c := range cases {
			prepared := prepareJSON(t, root, c.paths...)
			got := served[i]

			assert.Equal(t, delivery.SurfacePrepare, prepared.Surface, c.name)
			assert.Equal(t, delivery.SurfaceChangeSet, got.Surface, c.name)
			assert.NotEmpty(t, prepared.EventID, c.name)
			assert.NotEmpty(t, got.EventID, c.name)
			assert.NotEqual(t, prepared.EventID, got.EventID, "%s: each surface records its own event", c.name)

			// Every other field is equal: the paths, the guidance, the
			// pins with their versions, the mined lessons, the checks,
			// and the omissions.
			got.Surface, got.PreparedAt, got.EventID = prepared.Surface, prepared.PreparedAt, prepared.EventID
			assert.Equal(t, prepared, got, c.name)
		}

		return served
	}

	before := compare(t)

	// The mined lesson of a.go is in both documents, so the index refresh
	// of the server kept the index and the parity covers mined lessons too.
	require.Len(t, before[0].Lessons, 1)
	assert.Equal(t, "RUF001", before[0].Lessons[0].Symptom)
	assert.Equal(t, delivery.MinedOK, before[0].Guidance.MinedStatus)
	assert.Equal(t, []string{"keep-main-small"}, preparedRules(before[0]))
	require.Len(t, before[1].Checks, 1)
	assert.Equal(t, "frozen-assets", before[1].Checks[0].Check.ID)
	assert.Equal(t, "absent", before[2].Request.Paths[0].State)

	verifyWrite(t, root, ".seamark/lessons.yaml",
		strings.Replace(prepareLessonsYAML, "Move the work into helpers.", "Keep main short; move the work into helpers.", 1))

	after := compare(t)

	assert.NotEqual(t, before[0].Guidance.GuidanceSHA256, after[0].Guidance.GuidanceSHA256,
		"a changed definition moves the guidance digest")
	require.Len(t, after[0].Pins, 1)
	assert.Equal(t, before[0].Pins[0].PinIdentity, after[0].Pins[0].PinIdentity, "the pin keeps its identity")
	assert.NotEqual(t, before[0].Pins[0].PinVersion, after[0].Pins[0].PinVersion, "the pin gets a new version")
}
