package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
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
// preparation never touches them.
func requireNoDeliveryState(t *testing.T, root string) {
	t.Helper()

	for _, name := range []string{
		"lessons-hook-state.json", "lessons-hook-state.lock",
		"lessons-explore-state.json", "lessons-explore-state.lock",
	} {
		assert.NoFileExists(t, filepath.Join(root, ".seamark", name))
	}
}

// TestLessonsPrepareJSONAndText proves the two output forms of lessons
// --prepare: --json prints only the lesson-preparation/1 document, and
// the text is RenderPreparation of the same preparation in the full
// form. Each call records one firing record of the prepare surface. It
// also proves the mined status of each index state.
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

// TestLessonsPrepareRefusesEveryOtherMode proves the flag guards of
// --prepare. Every refusal happens before the dispatch, prints nothing
// on stdout, writes no file, and calls no model. So the prefix grant
// `seamark lessons --prepare *` reaches no command that writes. A new
// mode extends each text that lists the modes. So the test asserts such
// a text only by its head, a Contains for --prepare, and its tail.
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

// TestLessonsPrepareNeedsAPathInsideTheWorkspace proves the errors for
// the path set and their exit code 1 through the real Execute. A
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

// TestLessonsPrepareNeverInvokes proves that lessons --prepare calls no
// model: both output forms, a partial rejection, a repository without
// an index, and a lessons.yaml that does not decode run without an
// agent, a model CLI, or the network. The command writes no file except
// the firing records of the prepare surface: no index, no delivery
// state.
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
// directory or the repository root.
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

// mcpPreparations serves one `seamark mcp` session with the global
// flags under the 2025-06-18 protocol with one change_set call for each
// path set. It returns the lesson_preparation of each call, in call
// order.
func mcpPreparations(t *testing.T, flags []string, calls ...[]string) []delivery.Preparation {
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

	for line := range strings.SplitSeq(runMCP(t, flags, requests...), "\n") {
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

// TestLessonsPrepareMatchesChangeSet proves the parity of the two
// preparation surfaces: lessons --prepare and MCP change_set return one
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

		served := mcpPreparations(t, []string{"-C", root}, calls...)

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

// manyPins holds pins on distinct themes. Similar rule names or notes
// would collapse as restatements in the hook and the preparation.
var manyPins = []struct{ rule, note string }{
	{"close-bodies", "Close response bodies on every path."},
	{"outbound-deadlines", "Wrap context deadlines around outbound calls."},
	{"bounded-retries", "Bound retries with jittered backoff."},
	{"unicode-compare", "Normalize user text to NFC before compare."},
	{"page-size", "Cap page size at the API edge."},
	{"token-scope", "Check token scope before the handler body."},
	{"idempotent-posts", "Make POST handlers idempotent with request keys."},
	{"metric-names", "Name metrics with the service prefix."},
	{"lock-order", "Take locks in table order to avoid deadlocks."},
	{"early-rollback", "Roll back the transaction on any early return."},
	{"down-migrations", "Ship each schema migration with a down step."},
	{"cache-expiry", "Expire cache entries; never cache forever."},
	{"trace-headers", "Propagate trace headers to downstream calls."},
	{"tenant-quotas", "Enforce per-tenant quotas in middleware."},
}

// manyPinsRepo builds an indexed repository with n distinct pins in
// region api, at most len(manyPins). header goes above the pin
// list, so a test can set the budget keys. It returns the root and the
// pin rules in file order.
func manyPinsRepo(t *testing.T, n int, header string) (root string, rules []string) {
	t.Helper()

	root = writeFixture(t)
	verifyWrite(t, root, "api/handler.go", "package api\n\nfunc Handle() {}\n")

	var b strings.Builder

	b.WriteString(header)
	b.WriteString("pin:\n")

	for _, p := range manyPins[:n] {
		rules = append(rules, p.rule)
		b.WriteString("  - {rule: " + p.rule + ", region: api, note: \"" + p.note + "\"}\n")
	}

	verifyWrite(t, root, ".seamark/lessons.yaml", b.String())
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	return root, rules
}

// pinLines returns the rules of the "- [pin] <rule> — ..." lines of a
// lessons --file view, in print order.
func pinLines(view string) []string {
	var rules []string

	for _, line := range strings.Split(view, "\n") {
		rest, ok := strings.CutPrefix(line, "- [pin] ")
		if !ok {
			continue
		}

		rule, _, _ := strings.Cut(rest, " ")
		rules = append(rules, rule)
	}

	return rules
}

// TestLessonsFileListsEveryLesson holds that the --file view applies no
// lesson cap. The other routes name it as the view that shows what they
// leave out, so it must list every pin in scope.
func TestLessonsFileListsEveryLesson(t *testing.T) {
	root, rules := manyPinsRepo(t, 14, "")

	view, err := run(t, "-C", root, "lessons", "--file", "api/handler.go")
	require.NoError(t, err)

	assert.Equal(t, rules, pinLines(view), "each pin once, in rank order")
	assert.NotContains(t, view, "more", "the view lists everything, so it needs no pointer line")
	assert.Contains(t, view, "review lessons for api/handler.go")
	assert.Contains(t, view, "seamark lessons --region api")
}

// TestLessonsFileShowsWhatTheRoutesLeaveOut follows the routes of the
// preparation and the edit hook. Each pin that they leave out must
// appear in the --file view that they name.
func TestLessonsFileShowsWhatTheRoutesLeaveOut(t *testing.T) {
	root, rules := manyPinsRepo(t, 12, "change_budget: 1\npin_budget: 3\n")

	// A relative --prepare path starts at the current directory, so the
	// test passes an absolute one.
	p := prepareJSON(t, root, filepath.Join(root, "api", "handler.go"))
	require.NotEmpty(t, p.Omitted)

	for _, o := range p.Omitted {
		path, ok := strings.CutPrefix(o.Inspect, "seamark lessons --file ")
		require.True(t, ok, "inspect route: %s", o.Inspect)

		view, err := run(t, "-C", root, "lessons", "--file", path)
		require.NoError(t, err)
		assert.Contains(t, pinLines(view), o.Rule, "%s names a view that shows %s", o.Inspect, o.Rule)
	}

	payload := `{"tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "api", "handler.go") + `"}}`

	out, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)

	var hook struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &hook))
	assert.Contains(t, hook.HookSpecificOutput.AdditionalContext, "(+9 more pins for this area: ")

	view, err := run(t, "-C", root, "lessons", "--file", "api/handler.go")
	require.NoError(t, err)

	shown := pinLines(view)
	for _, rule := range rules[3:] {
		assert.NotContains(t, hook.HookSpecificOutput.AdditionalContext, rule+" ", "the hook holds %s back", rule)
		assert.Contains(t, shown, rule, "the view that the hook names shows %s", rule)
	}
}

// TestLessonsPrepareSaysWhenTheIndexIsStale proves the mined freshness of
// lessons --prepare. The command refreshes no index, so after an edit of
// a tracked file the index no longer matches the workspace: the document
// says stale beside mined_status ok, and the text says it in one line. A
// current index has no key, so an ordinary document keeps its bytes. The
// index records this checkout, so the document names no mined_root.
func TestLessonsPrepareSaysWhenTheIndexIsStale(t *testing.T) {
	root := prepareRepo(t, true)
	main := filepath.Join(root, "a.go")
	const line = "  the mined lessons come from an index that does not match the workspace now " +
		"(`seamark index` refreshes it)\n"

	current, err := prepareRun(t, root, main, "--json")
	require.NoError(t, err)
	assert.NotContains(t, current, "mined_freshness")
	assert.NotContains(t, current, "mined_root")

	text, err := prepareRun(t, root, main)
	require.NoError(t, err)
	assert.NotContains(t, text, line)

	verifyWrite(t, root, "a.go", "package main\n\nfunc main() {}\n\nfunc later() {}\n")

	p := prepareJSON(t, root, main)
	assert.Equal(t, delivery.MinedOK, p.Guidance.MinedStatus, "the existing value keeps its meaning")
	assert.Equal(t, delivery.MinedStale, p.Guidance.MinedFreshness)
	assert.Equal(t, indexhealth.StaleWorkspaceChanged, p.Guidance.MinedStaleReason)
	assert.Empty(t, p.Guidance.MinedRoot)

	text, err = prepareRun(t, root, main)
	require.NoError(t, err)
	assert.Contains(t, text, line)
}

// TestLessonsPrepareSaysWhenAnotherVersionBuiltTheIndex proves that
// lessons --prepare, which refreshes no index, calls an index of another
// seamark version stale in an unchanged workspace: the document says
// stale with the reason other-version, and the text names the rebuild.
// After an index run the document has neither key again.
func TestLessonsPrepareSaysWhenAnotherVersionBuiltTheIndex(t *testing.T) {
	root := prepareRepo(t, true)
	main := filepath.Join(root, "a.go")
	const line = "  the mined lessons come from an index that was built by another seamark version " +
		"(`seamark index` rebuilds it)\n"

	ageParseCache(t, store.DefaultPath(root))

	p := prepareJSON(t, root, main)
	assert.Equal(t, delivery.MinedOK, p.Guidance.MinedStatus)
	assert.Equal(t, delivery.MinedStale, p.Guidance.MinedFreshness)
	assert.Equal(t, indexhealth.StaleOtherVersion, p.Guidance.MinedStaleReason)

	text, err := prepareRun(t, root, main)
	require.NoError(t, err)
	assert.Contains(t, text, line)

	_, err = run(t, "-C", root, "index")
	require.NoError(t, err)

	current, err := prepareRun(t, root, main, "--json")
	require.NoError(t, err)
	assert.NotContains(t, current, "mined_freshness")
	assert.NotContains(t, current, "mined_stale_reason")

	text, err = prepareRun(t, root, main)
	require.NoError(t, err)
	assert.NotContains(t, text, line)
}

// prepareOriginLessons is the lessons.yaml that prepareCopy gives the
// origin after the copy. Its one pin covers a.go under another rule, so
// a preparation that reads the origin's file shows origin-only.
const prepareOriginLessons = `pin:
  - rule: origin-only
    region: a.go
    note: The pin of the origin.
`

// prepareCopy builds an indexed repository with prepareRepo and copies
// it with `cp -R`. The copy keeps the index, so that index still records
// the origin. The origin then gets prepareOriginLessons, so the two
// checkouts have different pins. It returns the origin by its resolved
// path, as the index records it, and the copy.
func prepareCopy(t *testing.T) (origin, copied string) {
	t.Helper()

	root := prepareRepo(t, true)
	copied = rootWriteCopy(t, root)

	origin, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	verifyWrite(t, origin, ".seamark/lessons.yaml", prepareOriginLessons)

	return origin, copied
}

// TestLessonsPrepareInACopiedCheckout covers a checkout whose index
// records another root: a copy before its first index run, and a moved
// checkout. The preparation describes the -C checkout. A relative path
// and an absolute path of the copy both resolve, the pins come from the
// lessons.yaml of the copy, and the mined lessons come from the copied
// index, which the document names in mined_root. The origin stays byte
// for byte, also its firing log.
func TestLessonsPrepareInACopiedCheckout(t *testing.T) {
	origin, copied := prepareCopy(t)
	before := prepareFiles(t, origin)

	cfg, err := reviews.LoadConfig(copied)
	require.NoError(t, err)

	t.Chdir(copied)

	t.Run("a relative path", func(t *testing.T) {
		p := prepareJSON(t, copied, "a.go")

		assert.Equal(t, []string{"a.go"}, preparedPaths(p))
		assert.Zero(t, p.Request.RejectedCount)
		assert.Equal(t, []string{"keep-main-small"}, preparedRules(p), "the pins of the copy, not of the origin")
		assert.Equal(t, cfg.GuidanceDigest(), p.Guidance.GuidanceSHA256, "the guidance is the lessons.yaml of the copy")

		assert.Equal(t, delivery.MinedOK, p.Guidance.MinedStatus)
		assert.Equal(t, origin, p.Guidance.MinedRoot, "the document names the root that the index records")
		require.Len(t, p.Lessons, 1, "the mined lessons come from the copied index")
		assert.Equal(t, "RUF001", p.Lessons[0].Symptom)
	})

	t.Run("an absolute path", func(t *testing.T) {
		p := prepareJSON(t, copied, filepath.Join(copied, "src", "handler.go"))

		assert.Equal(t, []string{"src/handler.go"}, preparedPaths(p))
		assert.Equal(t, []string{"prefer-errors-is"}, preparedRules(p))
		assert.Equal(t, origin, p.Guidance.MinedRoot)
	})

	t.Run("the text names the recorded root", func(t *testing.T) {
		text, err := prepareRun(t, copied, "a.go")
		require.NoError(t, err)

		assert.Contains(t, text, "  the mined lessons come from an index that records "+origin+"\n")
		assert.Contains(t, text, "keep-main-small")
		assert.NotContains(t, text, "origin-only")
	})

	t.Run("a path in the origin is outside the workspace", func(t *testing.T) {
		abs := filepath.Join(origin, "a.go")

		got := prepareRefuse(t, copied, "--prepare", abs)
		assert.Equal(t, `no path of --prepare is inside the workspace: 1 rejected (first "`+abs+`": outside the workspace)`, got)
	})

	t.Run("the firing records stay in the copy", func(t *testing.T) {
		assert.Len(t, prepareFirings(t, copied), 3, "one record for each preparation with a selection")

		firings, err := reviews.ReadFirings(origin)
		require.NoError(t, err)
		assert.Empty(t, firings)
		assert.Equal(t, before, prepareFiles(t, origin), "a preparation in the copy writes nothing in the origin")
	})

	t.Run("a moved checkout", func(t *testing.T) {
		root := prepareRepo(t, true)

		recorded, err := filepath.EvalSymlinks(root)
		require.NoError(t, err)

		moved := filepath.Join(rootWriteDir(t), "moved")
		require.NoError(t, os.Rename(recorded, moved))
		t.Chdir(moved)

		p := prepareJSON(t, moved, "a.go")
		assert.Equal(t, []string{"a.go"}, preparedPaths(p))
		assert.Equal(t, []string{"keep-main-small"}, preparedRules(p))
		assert.Equal(t, recorded, p.Guidance.MinedRoot, "the document names the root that no longer exists")
		assert.NoDirExists(t, recorded, "a preparation never creates the recorded root")
	})
}

// TestLessonsPrepareMatchesChangeSetInACopy proves that the two
// preparation surfaces agree in a copy whose index records the origin:
// MCP change_set and lessons --prepare describe the copy and read its
// lessons.yaml. Only the surface, the time, and the event ID differ.
func TestLessonsPrepareMatchesChangeSetInACopy(t *testing.T) {
	t.Run("the database of the origin", func(t *testing.T) {
		// The MCP refresh never rebuilds this file and skips, because the
		// index matches the copy. So both surfaces read the index of the
		// origin and name it in mined_root.
		origin, copied := prepareCopy(t)
		db := []string{"--db", store.DefaultPath(origin)}
		paths := []string{filepath.Join(copied, "a.go"), filepath.Join(copied, "generated", "b.txt")}

		served := mcpPreparations(t, append([]string{"-C", copied}, db...), paths)[0]
		prepared := prepareJSON(t, copied, append(paths, db...)...)

		assert.Equal(t, origin, prepared.Guidance.MinedRoot)
		assert.ElementsMatch(t, []string{"keep-main-small", "frozen-assets"}, preparedRules(prepared))
		require.Len(t, prepared.Lessons, 1)

		served.Surface, served.PreparedAt, served.EventID = prepared.Surface, prepared.PreparedAt, prepared.EventID
		assert.Equal(t, prepared, served)
	})

	t.Run("the default database of the copy", func(t *testing.T) {
		// The MCP refresh rebuilds a copied default index, so the index
		// records the copy after the first change_set call. The command
		// refreshes no index, so before that call it names the origin.
		origin, copied := prepareCopy(t)
		paths := []string{filepath.Join(copied, "a.go")}

		first := prepareJSON(t, copied, paths...)
		assert.Equal(t, origin, first.Guidance.MinedRoot)

		served := mcpPreparations(t, []string{"-C", copied}, paths)[0]
		assert.Empty(t, served.Guidance.MinedRoot, "the server rebuilt the index for the copy")
		assert.Equal(t, first.Request, served.Request)
		assert.Equal(t, first.Pins, served.Pins)
		assert.Equal(t, first.Guidance.GuidanceSHA256, served.Guidance.GuidanceSHA256)

		prepared := prepareJSON(t, copied, paths...)
		served.Surface, served.PreparedAt, served.EventID = prepared.Surface, prepared.PreparedAt, prepared.EventID
		assert.Equal(t, prepared, served)
	})
}

// TestLessonsFileInACopiedCheckout follows the routes in a copy whose
// index records the origin. Each omission of a preparation names a
// --file view of the copy, and that view shows the pin from the
// lessons.yaml of the copy. A relative path reads the copy too. why in
// the copy describes the origin, so it names a path of the origin, and
// that view still shows the pins of the origin.
func TestLessonsFileInACopiedCheckout(t *testing.T) {
	root, rules := manyPinsRepo(t, 12, "change_budget: 1\n")
	copied := rootWriteCopy(t, root)

	origin, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	// One more pin in the origin makes a view of the origin differ from
	// a view of the copy.
	pins, err := os.ReadFile(filepath.Join(origin, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	verifyWrite(t, origin, ".seamark/lessons.yaml",
		string(pins)+"  - {rule: origin-only, region: api, note: \"The pin of the origin.\"}\n")

	t.Run("each omission names a view of the copy", func(t *testing.T) {
		p := prepareJSON(t, copied, filepath.Join(copied, "api", "handler.go"))
		require.Equal(t, origin, p.Guidance.MinedRoot)
		require.Len(t, p.Omitted, len(rules)-1)

		for _, o := range p.Omitted {
			path, ok := strings.CutPrefix(o.Inspect, "seamark lessons --file ")
			require.True(t, ok, "inspect route: %s", o.Inspect)
			assert.Equal(t, filepath.Join(copied, "api", "handler.go"), path)

			view, err := run(t, "-C", copied, "lessons", "--file", path)
			require.NoError(t, err)
			assert.Contains(t, pinLines(view), o.Rule, "%s names a view that shows %s", o.Inspect, o.Rule)
			assert.NotContains(t, pinLines(view), "origin-only", "the view reads the lessons.yaml of the copy")
		}
	})

	t.Run("a relative path reads the copy", func(t *testing.T) {
		view, err := run(t, "-C", copied, "lessons", "--file", "api/handler.go")
		require.NoError(t, err)
		assert.Equal(t, rules, pinLines(view))
		assert.Contains(t, view, "review lessons for api/handler.go")
	})

	t.Run("the route of why names the origin", func(t *testing.T) {
		out, err := run(t, "-C", copied, "why", "api/handler.go")
		require.NoError(t, err)

		_, rest, ok := strings.Cut(out, "`seamark lessons --file ")
		require.True(t, ok, "why names the full view: %s", out)

		path, _, _ := strings.Cut(rest, "`")
		assert.Equal(t, filepath.Join(origin, "api", "handler.go"), path)

		view, err := run(t, "-C", copied, "lessons", "--file", path)
		require.NoError(t, err)
		assert.Equal(t, append(slices.Clone(rules), "origin-only"), pinLines(view), "the view that why names shows the pins that why counts")
	})
}
