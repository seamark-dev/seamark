package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold preparation: the selection, the exact pin
// versions, the checks list, the omissions, the firing record, and the
// two text forms.

// preparedAt is the fixed preparation time of the tests.
var preparedAt = time.Date(2026, 10, 21, 10, 15, 2, 0, time.UTC)

// prepareEventID is the fixed event ID of the test preparations. The
// reference document of TestPreparationJSONMatchesTheReferenceDocument
// carries it.
const prepareEventID = "53cdb75d6777b20e3d59e77cecd39b31"

// prepareExampleConfig is the .seamark/lessons.yaml that the reference
// preparation reads: one enforce pin with the frozen-assets check and
// one advisory pin.
const prepareExampleConfig = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// prepareTwoPins pins one rule on api and one on db.
const prepareTwoPins = `pin:
  - rule: boundary-validation
    region: api
    note: Validate request payloads at the edge.
  - rule: transaction-atomicity
    region: db
    note: Wrap dependent writes in one transaction.
`

// prepareRequest is a CLI preparation of paths under root with fixed
// time and event ID.
func prepareRequest(root string, cfg *reviews.Config, paths ...string) PrepareRequest {
	return PrepareRequest{
		Root: root, CWD: root, Paths: paths, Surface: SurfacePrepare, Config: cfg,
		Now:            func() time.Time { return preparedAt },
		EventID:        func() string { return prepareEventID },
		SeamarkVersion: "v0.8.0-31-g96e14ef",
	}
}

// prepareMemoryStore is an empty in-memory index, as the CLI opens it
// when the workspace has no index.
func prepareMemoryStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	return st
}

// mustPrepare runs Prepare, requires success, and records the result as
// a caller does after a successful answer.
func mustPrepare(t *testing.T, st *store.Store, req PrepareRequest) *Preparation {
	t.Helper()

	p, err := Prepare(context.Background(), st, req)
	require.NoError(t, err)

	p.Record()

	return p
}

// preparationText renders p in form.
func preparationText(p *Preparation, form string) string {
	var b strings.Builder

	RenderPreparation(&b, p, form)

	return b.String()
}

// writeWorkspaceFile creates one file under root.
func writeWorkspaceFile(t *testing.T, root, rel string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
}

// pinKeyOf is the pin key of a pin rule.
func pinKeyOf(p reviews.PinRule) reviews.PinKey {
	return reviews.NewPinKey(p.Rule, p.Region, p.Regions)
}

// rawAuditLines reads every line of the firing log under root as a
// JSON object, so a test can assert that a key is absent.
func rawAuditLines(t *testing.T, root string) []map[string]any {
	t.Helper()

	f, err := os.Open(reviews.AuditLogPath(root))
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	defer func() { _ = f.Close() }()

	var lines []map[string]any

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line))

		lines = append(lines, line)
	}

	require.NoError(t, scanner.Err())

	return lines
}

func TestPrepareReturnsExactVersionsForOneManyAndNewPaths(t *testing.T) {
	root := workspace(t)
	cfg := writeConfig(t, root, prepareTwoPins)
	st := prepareMemoryStore(t)
	api, db := cfg.Pin[0], cfg.Pin[1]

	exact := func(p reviews.PinRule, entry int, paths ...string) PreparedPin {
		key := pinKeyOf(p)

		return PreparedPin{
			PinRef: cfg.ShortPinRef(key), PinIdentity: reviews.PinIdentityDigest(key), PinVersion: cfg.PinVersion(p),
			Entry: entry, Rule: p.Rule, Note: p.Note, Scope: p.AllRegions(), Mode: "advisory", Paths: paths,
		}
	}

	t.Run("one path", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go"))

		assert.Equal(t, []PreparedPin{exact(api, 1, "api/handler.go")}, p.Pins)
		assert.Equal(t, []string{"api/handler.go"}, p.Request.Requested)
		assert.Equal(t, []ScopePath{{Path: "api/handler.go", State: "file", Pins: 1, Checks: 0}}, p.Request.Paths)
		assert.Equal(t, 64, len(p.Pins[0].PinVersion), "the version is the full digest")
	})

	t.Run("many paths keep the change_set order and one budget", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg, "db/query.go", "api/handler.go", "a.go"))

		require.Len(t, p.Pins, 2)
		assert.ElementsMatch(t, []PreparedPin{exact(api, 1, "api/handler.go"), exact(db, 2, "db/query.go")}, p.Pins)

		// The pins come in the order that change_set prints them.
		lessons, _, err := report.LessonsForFiles(st, cfg, []string{"db/query.go", "api/handler.go", "a.go"},
			cfg.ChangeSetBudget())
		require.NoError(t, err)
		require.Len(t, lessons, 2)

		for i, l := range lessons {
			assert.Equal(t, p.Pins[i].Rule+" — "+p.Pins[i].Note, l.Symptom)
		}

		assert.Equal(t, ScopePath{Path: "a.go", State: "file"}, p.Request.Paths[2], "no pin applies to a.go")
	})

	t.Run("a new path and other spellings of one path", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg,
			"api/new_endpoint.go", "./api/handler.go", filepath.Join(root, "api", "handler.go"), "api"))

		assert.Equal(t, []PreparedPin{exact(api, 1, "api/new_endpoint.go", "api/handler.go", "api")}, p.Pins)
		assert.Equal(t, []ScopePath{
			{Path: "api/new_endpoint.go", State: "absent", Pins: 1},
			{Path: "api/handler.go", State: "file", Pins: 1},
			{Path: "api", State: "directory", Pins: 1},
		}, p.Request.Paths, "a path resolves once, and a new path is absent")
		assert.Len(t, p.Request.Requested, 4, "the request keeps every input")
	})

	t.Run("a path below a file is not absent", func(t *testing.T) {
		// Lstat fails with ENOTDIR: no file can be created there, and the
		// error is not proof of absence.
		p := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go/x"))

		assert.Equal(t, []ScopePath{{Path: "api/handler.go/x", State: "other", Pins: 1}}, p.Request.Paths)
	})

	t.Run("a repeated entry keeps one pin with its first entry", func(t *testing.T) {
		repeated := writeConfig(t, root, prepareTwoPins+"  - rule: boundary-validation\n    region: api\n"+
			"    note: Validate request payloads at the edge.\n")
		p := mustPrepare(t, st, prepareRequest(root, repeated, "api/handler.go"))

		require.Len(t, p.Pins, 1)
		assert.Equal(t, 1, p.Pins[0].Entry)
		assert.Equal(t, 2, p.Request.Paths[0].Pins, "the path count holds both entries")
	})
}

func TestPrepareListsEveryExecutablePinRegardlessOfBudget(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "generated/a.txt")
	writeWorkspaceFile(t, root, "internal/parse/strict.go")

	cfg := writeConfig(t, root, `lesson_checks_version: 1
change_budget: 1
checks:
  frozen:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated
    note: Keep generated files frozen.
    mode: enforce
    check: frozen
  - rule: no-generated-todo
    region: generated
    note: Add no TODO markers to generated files.
    mode: check
    check: no-todo
  - rule: todo-advice
    region: generated
    note: Prefer issue links over inline markers.
    check: no-todo
  - rule: parser-todo
    region: internal/parse
    note: Track parser work in issues.
    mode: check
    check: no-todo
  - rule: plain-advice
    region: generated
    note: Regenerate with the build script.
`)
	st := prepareMemoryStore(t)

	p := mustPrepare(t, st, prepareRequest(root, cfg, "generated/a.txt"))

	require.Len(t, p.Pins, 1, "the change budget holds one pin")
	assert.Len(t, p.Omitted, 3, "the budget leaves three applicable pins out")

	summary := func(entry int, mode string, runs bool, paths ...string) CheckSummary {
		pin := cfg.Pin[entry-1]
		def := cfg.Checks[pin.Check]
		predicate, err := lessoncheck.Describe(def)
		require.NoError(t, err)

		return CheckSummary{
			PinRef: cfg.ShortPinRef(pinKeyOf(pin)), PinVersion: cfg.PinVersion(pin), Mode: mode, Runs: runs,
			Check: lessoncheck.CheckRef{
				ID: pin.Check, Kind: lessoncheck.Kind(def.Kind), Version: reviews.CheckVersion(def),
			},
			Predicate: predicate, Paths: paths,
		}
	}

	assert.Equal(t, []CheckSummary{
		summary(1, "enforce", true, "generated/a.txt"),
		summary(2, "check", true, "generated/a.txt"),
		summary(3, "advisory", false, "generated/a.txt"),
	}, p.Checks, "every pin with a check in scope, in entry order, with the advisory one not run")
	assert.Equal(t, []ScopePath{{Path: "generated/a.txt", State: "file", Pins: 4, Checks: 2}}, p.Request.Paths)
	assert.Equal(t, "valid", p.Guidance.ChecksStatus)

	encoded, err := json.Marshal(p.Checks)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "enforcement", "without a capability report no check has an enforcement field")

	t.Run("a directory above a check scope gets the pin and its check", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg, "internal"))

		require.Len(t, p.Pins, 1)
		assert.Equal(t, "parser-todo", p.Pins[0].Rule, "the pin region rule works in both directions")
		assert.Equal(t, []CheckSummary{summary(4, "check", true, "internal")}, p.Checks,
			"the checks list uses the pin region rule")
		assert.Equal(t, []ScopePath{{Path: "internal", State: "directory", Pins: 1, Checks: 1}}, p.Request.Paths)
	})

	t.Run("the check scope holds the paths below it", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg, "internal/parse/strict.go", "internal/parse/new.go"))

		assert.Equal(t, []CheckSummary{
			summary(4, "check", true, "internal/parse/strict.go", "internal/parse/new.go"),
		}, p.Checks)
	})

	t.Run("an edit hook budget never limits the list", func(t *testing.T) {
		text := preparationText(p, FormSupplement)

		assert.Contains(t, text, "checks (every check in scope; no budget applies)")
		assert.Equal(t, 3, strings.Count(text, "· check frozen")+strings.Count(text, "· check no-todo"))
		assert.Contains(t, text, "does not run (advisory)")
	})
}

// TestPrepareListsTheCheckOfEveryListedPin proves that a listed pin
// with a check mode also gets its check summary, whatever the
// granularity of the requested path. A directory above the scope of an
// enforce pin gets the pin by the pin region rule, so the checks list
// and the check count of the path use that rule too.
func TestPrepareListsTheCheckOfEveryListedPin(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "internal/parse/strict.go")
	writeWorkspaceFile(t, root, "docs/readme.md")

	cfg := writeConfig(t, root, `lesson_checks_version: 1
checks:
  frozen-parser:
    kind: forbid-path-change/1
pin:
  - rule: frozen-parser
    region: internal/parse
    note: Change the parser only in a release.
    mode: enforce
    check: frozen-parser
`)
	pin := cfg.Pin[0]
	def := cfg.Checks[pin.Check]
	predicate, err := lessoncheck.Describe(def)
	require.NoError(t, err)

	p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "internal", "docs/readme.md"))

	require.Len(t, p.Pins, 1)
	assert.Equal(t, "enforce", p.Pins[0].Mode)
	assert.Equal(t, []string{"internal"}, p.Pins[0].Paths, "the pin region rule works in both directions")

	assert.Equal(t, []CheckSummary{{
		PinRef: cfg.ShortPinRef(pinKeyOf(pin)), PinVersion: cfg.PinVersion(pin), Mode: "enforce", Runs: true,
		Check: lessoncheck.CheckRef{
			ID: pin.Check, Kind: lessoncheck.Kind(def.Kind), Version: reviews.CheckVersion(def),
		},
		Predicate: predicate, Paths: []string{"internal"},
	}}, p.Checks, "the listed enforce pin carries its check")
	assert.Equal(t, []ScopePath{
		{Path: "internal", State: "directory", Pins: 1, Checks: 1},
		{Path: "docs/readme.md", State: "file", Pins: 0, Checks: 0},
	}, p.Request.Paths, "the check count of a path uses the rule of its pin count")

	// Each listed pin and its check summary name one pin and one path set.
	for _, listed := range p.Pins {
		var found bool

		for _, c := range p.Checks {
			if c.PinRef == listed.PinRef {
				found = true

				assert.Equal(t, listed.PinVersion, c.PinVersion)
				assert.Equal(t, listed.Paths, c.Paths)
			}
		}

		assert.True(t, found, "pin %s has no check summary", listed.PinRef)
	}

	text := preparationText(p, FormSupplement)
	assert.Contains(t, text, "checks (every check in scope; no budget applies)")
	assert.Contains(t, text, "· enforce · runs · check frozen-parser (forbid-path-change/1")
}

func TestPrepareNamesOmissionsWithInspectCommands(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my repo")
	writeWorkspaceFile(t, root, "api/handler.go")
	writeWorkspaceFile(t, root, "plain/a.go")

	cfg := writeConfig(t, root, `change_budget: 2
pin:
  - rule: boundary-validation
    region: api/handler.go
    note: Validate request payloads at the edge.
  - rule: boundary-validation
    region: api
    note: Validate request payloads at the edge.
  - rule: retry-idempotency
    region: api
    note: Every retried call must be idempotent.
  - rule: cache-invalidation
    region: api
    note: Drop cached rows after each write.
`)
	st := lessonStore(t, root)

	p := mustPrepare(t, st, prepareRequest(root, cfg, "plain/a.go", "api/handler.go"))

	inspect := "seamark lessons --file '" + filepath.Join(root, "api", "handler.go") + "'"
	restated, overTotal := cfg.Pin[1], cfg.Pin[3]

	assert.Equal(t, []string{"boundary-validation", "retry-idempotency"},
		[]string{p.Pins[0].Rule, p.Pins[1].Rule}, "the deepest pin first, then file order")
	assert.Equal(t, []Omission{
		{
			Kind: "pin", PinRef: cfg.ShortPinRef(pinKeyOf(restated)), PinVersion: cfg.PinVersion(restated),
			Rule: "boundary-validation", Reason: "restated", Inspect: inspect,
		},
		{
			Kind: "pin", PinRef: cfg.ShortPinRef(pinKeyOf(overTotal)), PinVersion: cfg.PinVersion(overTotal),
			Rule: "cache-invalidation", Reason: "over-total", Inspect: inspect,
		},
		{Kind: "lesson", ClusterKey: "api\x00RUF001", Rule: "RUF001", Reason: "over-total", Inspect: inspect},
	}, p.Omitted, "restated, then over-total pins, then over-total mined lessons")
	assert.Empty(t, p.Lessons, "the pins fill the budget")

	t.Run("a plain path stays unquoted", func(t *testing.T) {
		assert.Equal(t, "seamark lessons --file /work/api/x.go", inspectCommand("/work", "api/x.go"))
		assert.Equal(t, `seamark lessons --file '/work/it'\''s.go'`, inspectCommand("/work", "it's.go"))
	})

	t.Run("both text forms name the omissions and their commands", func(t *testing.T) {
		for _, form := range []string{FormFull, FormSupplement} {
			text := preparationText(p, form)

			assert.Contains(t, text, "\nomitted\n", form)
			assert.Contains(t, text, "· restated · boundary-validation: "+inspect+"\n", form)
			assert.Contains(t, text, "· over-total · cache-invalidation: "+inspect+"\n", form)
			assert.Contains(t, text, "- lesson api · over-total · RUF001: "+inspect+"\n", form)
			assert.NotContains(t, text, "more:", form)
		}
	})

	t.Run("the supplement never repeats the lesson text", func(t *testing.T) {
		text := preparationText(p, FormSupplement)

		assert.NotContains(t, text, "Validate request payloads at the edge.")
		assert.NotContains(t, text, "Every retried call must be idempotent.")
		assert.Contains(t, preparationText(p, FormFull), "Every retried call must be idempotent.")
	})
}

// pinTopics holds one distinct word for each pin of manyPins. Rules with
// shared words can restate each other, and a restated pin has another
// omission reason.
var pinTopics = []string{
	"apple", "bridge", "candle", "desert", "engine", "forest", "garden",
	"harbor", "island", "jacket", "kettle", "ladder", "marble", "needle",
	"orange", "pepper", "quartz", "ribbon", "saddle", "tunnel", "violin",
}

// manyPins returns a configuration with change_budget 1 and n pins in the
// api region. Each pin has its own topic, so no pin restates another.
func manyPins(n int) string {
	var b strings.Builder

	b.WriteString("change_budget: 1\npin:\n")

	for _, topic := range pinTopics[:n] {
		fmt.Fprintf(&b, "  - rule: %s\n    region: api\n    note: %s.\n", topic, topic)
	}

	return b.String()
}

// omissionLines returns the omission lines of a text form.
func omissionLines(text string) []string {
	_, section, _ := strings.Cut(text, "\nomitted\n")

	var lines []string

	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "- pin ") || strings.HasPrefix(line, "- lesson ") {
			lines = append(lines, line)
		}
	}

	return lines
}

func TestPrepareCapsTheOmittedTextAndKeepsTheFullResult(t *testing.T) {
	t.Run("many pins print five lines and a count", func(t *testing.T) {
		root := workspace(t)
		writeWorkspaceFile(t, root, "api/handler.go")

		cfg := writeConfig(t, root, manyPins(21))
		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

		require.Len(t, p.Omitted, 20, "the structured result keeps every omission")

		for _, o := range p.Omitted {
			assert.Equal(t, reviews.CauseOverTotal, o.Reason, o.Rule)
		}

		for _, form := range []string{FormFull, FormSupplement} {
			text := preparationText(p, form)
			lines := omissionLines(text)

			require.Len(t, lines, maxOmittedShown, form)

			for i, line := range lines {
				assert.Contains(t, line, " · "+p.Omitted[i].Rule+": ", "%s keeps document order", form)
			}

			assert.Contains(t, text, "(+15 more: `seamark lessons --file <path>` shows a file's full view)\n", form)
			assert.NotContains(t, text, " · "+p.Omitted[maxOmittedShown].Rule+": ", form)
		}
	})

	t.Run("one omission over the cap prints five lines and one more", func(t *testing.T) {
		// Six omissions are the smallest list that the cap shortens. One
		// line too many or a missing count line shows only at this size.
		root := workspace(t)
		writeWorkspaceFile(t, root, "api/handler.go")

		cfg := writeConfig(t, root, manyPins(7))
		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

		require.Len(t, p.Omitted, maxOmittedShown+1)

		for _, form := range []string{FormFull, FormSupplement} {
			text := preparationText(p, form)

			assert.Len(t, omissionLines(text), maxOmittedShown, form)
			assert.Contains(t, text, "(+1 more: `seamark lessons --file <path>` shows a file's full view)\n", form)
			assert.NotContains(t, text, " · "+p.Omitted[maxOmittedShown].Rule+": ", form)
		}
	})

	t.Run("the cap counts pins and mined lessons together", func(t *testing.T) {
		root := workspace(t)
		writeWorkspaceFile(t, root, "api/handler.go")

		cfg := writeConfig(t, root, manyPins(7))
		p := mustPrepare(t, lessonStore(t, root), prepareRequest(root, cfg, "api/handler.go"))

		require.Len(t, p.Omitted, 7)
		assert.Equal(t, omittedPin, p.Omitted[5].Kind)
		assert.Equal(t, "lesson", p.Omitted[6].Kind, "the mined lesson comes after the pins")

		for _, form := range []string{FormFull, FormSupplement} {
			text := preparationText(p, form)

			for _, line := range omissionLines(text) {
				assert.True(t, strings.HasPrefix(line, "- pin "), "%s shows pins first: %s", form, line)
			}

			assert.Contains(t, text, "(+2 more: ", form)
			assert.NotContains(t, text, "RUF001", form)
		}
	})
}

// TestRenderPreparationSanitizesEveryOmissionValue proves that the
// renderer itself removes control runes from each value of an omission
// line. Prepare already sanitizes the rule, so this test sets the
// omissions directly. A decoded document reaches the renderer the same way.
func TestRenderPreparationSanitizesEveryOmissionValue(t *testing.T) {
	root := workspace(t)
	writeWorkspaceFile(t, root, "api/handler.go")

	cfg := writeConfig(t, root, manyPins(1))
	p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

	// An escape sequence, a one-byte CSI, and a bidirectional override:
	// a terminal acts on each one instead of showing it.
	const unsafe = "\x1b[31m\u009b\u202e"

	p.Omitted = []Omission{
		{
			Kind: omittedPin, PinRef: "ref" + unsafe, PinVersion: "abc",
			Rule: "rule" + unsafe, Reason: "reason" + unsafe, Inspect: "inspect" + unsafe,
		},
		{
			Kind: omittedLesson, ClusterKey: "api" + unsafe + "\x00symptom",
			Rule: "symptom" + unsafe, Reason: reviews.CauseOverTotal, Inspect: "inspect",
		},
	}

	for _, form := range []string{FormFull, FormSupplement} {
		text := preparationText(p, form)

		assert.Contains(t, text, "- pin ref[31m · version abc · reason[31m · rule[31m: inspect[31m\n", form)
		assert.Contains(t, text, "- lesson api[31m · over-total · symptom[31m: inspect\n", form)
		assert.NotContains(t, text, "\x1b", form)
	}
}

func TestPrepareRefreshesWhenScopeOrDefinitionsChange(t *testing.T) {
	root := workspace(t)
	st := prepareMemoryStore(t)
	cfg := writeConfig(t, root, prepareTwoPins)

	first := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go"))
	again := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go"))

	t.Run("identical calls return identical metadata", func(t *testing.T) {
		assert.Equal(t, first.Pins, again.Pins)
		assert.Equal(t, first.Checks, again.Checks)
		assert.Equal(t, first.Request.ScopeSHA256, again.Request.ScopeSHA256)
		assert.Equal(t, first.Guidance.GuidanceSHA256, again.Guidance.GuidanceSHA256)
		assert.Equal(t, []string{"paths-added", "guidance-changed", "receiver-changed"}, first.Refresh)
		assert.Equal(t, Receiver{Lane: "unknown", Reason: "no-receiver-identity"}, first.Receiver)
	})

	t.Run("a new path changes the scope, and the order does not", func(t *testing.T) {
		grown := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go", "db/query.go"))
		reordered := mustPrepare(t, st, prepareRequest(root, cfg, "db/query.go", "./api/handler.go"))

		assert.NotEqual(t, first.Request.ScopeSHA256, grown.Request.ScopeSHA256)
		assert.Equal(t, grown.Request.ScopeSHA256, reordered.Request.ScopeSHA256)
		assert.Equal(t, first.Guidance.GuidanceSHA256, grown.Guidance.GuidanceSHA256)
	})

	t.Run("a cosmetic edit keeps the guidance", func(t *testing.T) {
		cosmetic := writeConfig(t, root, `pin:
  - region: api/
    rule: Boundary-Validation
    note: "  Validate request payloads at the edge.  "
    mode: advisory
  - {rule: transaction-atomicity, region: db, note: Wrap dependent writes in one transaction.}
`)
		p := mustPrepare(t, st, prepareRequest(root, cosmetic, "api/handler.go"))

		assert.Equal(t, first.Guidance.GuidanceSHA256, p.Guidance.GuidanceSHA256)
		assert.Equal(t, first.Pins[0].PinVersion, p.Pins[0].PinVersion)
	})

	t.Run("a new note, mode, or check changes the guidance and the version", func(t *testing.T) {
		noted := writeConfig(t, root, strings.Replace(prepareTwoPins, "at the edge.", "at every edge.", 1))
		renoted := mustPrepare(t, st, prepareRequest(root, noted, "api/handler.go"))

		assert.NotEqual(t, first.Guidance.GuidanceSHA256, renoted.Guidance.GuidanceSHA256)
		assert.NotEqual(t, first.Pins[0].PinVersion, renoted.Pins[0].PinVersion)
		assert.Equal(t, first.Pins[0].PinRef, renoted.Pins[0].PinRef, "the identity stays")

		checked := writeConfig(t, root, "lesson_checks_version: 1\nchecks:\n  frozen:\n    kind: forbid-path-change/1\n"+
			strings.Replace(prepareTwoPins, "at the edge.\n", "at the edge.\n    mode: check\n    check: frozen\n", 1))
		armed := mustPrepare(t, st, prepareRequest(root, checked, "api/handler.go"))

		assert.NotEqual(t, first.Guidance.GuidanceSHA256, armed.Guidance.GuidanceSHA256)
		assert.NotEqual(t, first.Pins[0].PinVersion, armed.Pins[0].PinVersion)
		assert.Equal(t, "check", armed.Pins[0].Mode)
		require.Len(t, armed.Checks, 1)
		assert.Equal(t, armed.Pins[0].PinVersion, armed.Checks[0].PinVersion, "one version on both lists")
	})

	t.Run("the refresh line names the current guidance", func(t *testing.T) {
		text := preparationText(first, FormFull)

		assert.Contains(t, text, "prepare again when the file set grows, when .seamark/lessons.yaml changes (guidance "+
			first.Guidance.GuidanceSHA256[:12]+"), or when another agent or subagent will construct the change\n")
	})
}

func TestPrepareWorksWithoutAnIndexAndWithAnInvalidConfig(t *testing.T) {
	t.Run("no index selects pins only", func(t *testing.T) {
		root := workspace(t)
		cfg := writeConfig(t, root, prepareTwoPins)
		req := prepareRequest(root, cfg, "api/handler.go")
		req.MinedStatus = MinedNoIndex

		p := mustPrepare(t, prepareMemoryStore(t), req)

		assert.Equal(t, "no-index", p.Guidance.MinedStatus)
		assert.Len(t, p.Pins, 1)
		assert.Contains(t, preparationText(p, FormFull), "no index exists, so the selection holds pins only")

		req.MinedStatus = MinedUnavailable
		p = mustPrepare(t, prepareMemoryStore(t), req)
		assert.Equal(t, "unavailable", p.Guidance.MinedStatus)
		assert.Len(t, p.Pins, 1)

		req.MinedStatus = "partial"
		_, err := Prepare(context.Background(), prepareMemoryStore(t), req)
		require.Error(t, err, "the mined status list is closed")
	})

	t.Run("a file that does not decode falls back to the defaults", func(t *testing.T) {
		root := workspace(t)
		st := lessonStore(t, root)
		path := filepath.Join(root, ".seamark", "lessons.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("pin: [\n"), 0o644))

		_, loadErr := reviews.LoadConfig(root)
		require.Error(t, loadErr)

		given := prepareRequest(root, nil, "api/handler.go")
		given.ConfigErr = loadErr

		// Without a config and an error, Prepare reads lessons.yaml itself.
		for name, req := range map[string]PrepareRequest{
			"given": given, "loaded": prepareRequest(root, nil, "api/handler.go"),
		} {
			p := mustPrepare(t, st, req)

			assert.Equal(t, "invalid", p.Guidance.Status, name)
			assert.Empty(t, p.Guidance.GuidanceSHA256, name)
			assert.Empty(t, p.Pins, name)
			assert.Equal(t, "invalid", p.Guidance.ChecksStatus, name)
			assert.Equal(t, strings.TrimPrefix(loadErr.Error(), "lessons config: "), p.Guidance.ChecksError, name)
			require.Len(t, p.Lessons, 1, "the default threshold still selects mined lessons")

			text := preparationText(p, FormFull)
			assert.Contains(t, text, ".seamark/lessons.yaml does not read or decode, so the defaults apply", name)
			assert.Contains(t, text, "(guidance invalid)", name)
		}
	})

	t.Run("prepare reads a valid file itself", func(t *testing.T) {
		root := workspace(t)
		writeConfig(t, root, prepareTwoPins)

		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, nil, "api/handler.go"))

		assert.Equal(t, "ok", p.Guidance.Status)
		assert.Len(t, p.Pins, 1)
	})

	t.Run("a semantic error keeps the pins and lists the executable pins", func(t *testing.T) {
		root := workspace(t)
		cfg := writeConfig(t, root, `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO('
pin:
  - rule: frozen-assets
    region: api
    note: Changes to these files require a maintainer-managed update.
  - rule: no-todo
    region: api
    note: Do not add TODO markers.
    mode: check
    check: no-todos
`)
		errs := lessoncheck.Validate(cfg)
		require.Len(t, errs, 2)

		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

		assert.Equal(t, "ok", p.Guidance.Status)
		assert.NotEmpty(t, p.Guidance.GuidanceSHA256)
		assert.Len(t, p.Pins, 2, "an advisory reader keeps the pins")
		assert.Equal(t, "invalid", p.Guidance.ChecksStatus)
		assert.Equal(t, errs[0].Message, p.Guidance.ChecksError)
		assert.Equal(t, []CheckSummary{{
			PinRef: cfg.ShortPinRef(pinKeyOf(cfg.Pin[1])), PinVersion: cfg.PinVersion(cfg.Pin[1]),
			Mode: "check", Runs: true, Check: lessoncheck.CheckRef{ID: "no-todos"}, Paths: []string{"api/handler.go"},
		}}, p.Checks, "the executable pin stays listed with its mode")
		assert.Contains(t, preparationText(p, FormFull), "checks are invalid: "+errs[0].Message)
		assert.Contains(t, preparationText(p, FormFull), "check no-todos (not defined)")
	})

	t.Run("an unknown mode is invalid on the pin and advisory on the check", func(t *testing.T) {
		root := workspace(t)
		cfg := writeConfig(t, root, `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen-api
    region: api
    note: Keep the api frozen.
    mode: strict
    check: frozen
`)
		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

		require.Len(t, p.Pins, 1)
		assert.Equal(t, "invalid", p.Pins[0].Mode)
		require.Len(t, p.Checks, 1)
		assert.Equal(t, "advisory", p.Checks[0].Mode)
		assert.False(t, p.Checks[0].Runs, "an unknown mode never arms a check")
	})

	t.Run("an executable pin without a check stays listed", func(t *testing.T) {
		root := workspace(t)
		cfg := writeConfig(t, root, "lesson_checks_version: 1\npin:\n"+
			"  - rule: frozen-api\n    region: api\n    note: Keep the api frozen.\n    mode: enforce\n")
		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, "api/handler.go"))

		assert.Equal(t, "invalid", p.Guidance.ChecksStatus)
		require.Len(t, p.Checks, 1)
		assert.Equal(t, lessoncheck.CheckRef{}, p.Checks[0].Check)
		assert.True(t, p.Checks[0].Runs)
		assert.Contains(t, preparationText(p, FormFull), "· enforce · runs · check (none bound)\n")
	})

	t.Run("no lessons.yaml is absent", func(t *testing.T) {
		root := workspace(t)

		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, nil, "api/handler.go"))

		assert.Equal(t, "absent", p.Guidance.Status)
		assert.Equal(t, reviews.DefaultConfig().GuidanceDigest(), p.Guidance.GuidanceSHA256)
		assert.Equal(t, "none", p.Guidance.ChecksStatus)
		assert.Contains(t, preparationText(p, FormFull), "no .seamark/lessons.yaml exists, so no pin applies")
	})

	t.Run("no path inside the workspace selects nothing", func(t *testing.T) {
		root := workspace(t)
		cfg := writeConfig(t, root, prepareTwoPins)

		// The preparation cuts a long input path, and a long temp folder
		// makes a temp path too long to show in full. A relative path stays
		// short. It starts at root, so it names a file beside the workspace.
		outside := filepath.Join("..", "x.go")

		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(root, cfg, outside, ""))

		assert.Empty(t, p.Request.Paths)
		assert.Equal(t, 2, p.Request.RejectedCount)
		assert.Equal(t, []RejectedInput{
			{Path: outside, Reason: "outside the workspace"},
			{Path: "", Reason: "malformed path"},
		}, p.Request.Rejected)
		assert.Empty(t, p.EventID, "nothing selected, nothing recorded")
		assert.Empty(t, rawAuditLines(t, root))

		for _, form := range []string{FormFull, FormSupplement} {
			text := preparationText(p, form)
			assert.Contains(t, text, "for 0 path(s) — receiver unknown", form)
			assert.Contains(t, text, "no requested path is inside the workspace, so nothing is selected", form)
			assert.Contains(t, text, "rejected 2 inputs (first 2 listed)", form)
		}
	})

	t.Run("a store and a preparation surface are required", func(t *testing.T) {
		root := workspace(t)

		_, err := Prepare(context.Background(), nil, prepareRequest(root, nil, "a.go"))
		require.Error(t, err)

		req := prepareRequest(root, nil, "a.go")
		req.Surface = SurfaceExplore
		_, err = Prepare(context.Background(), prepareMemoryStore(t), req)
		require.Error(t, err, "exploration never returns a preparation")
	})
}

func TestPrepareNeverTouchesDeliveryState(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\n"+prepareTwoPins)
	statePath := filepath.Join(root, ".seamark", "lessons-hook-state.json")
	explorePath := filepath.Join(root, ".seamark", "lessons-explore-state.json")
	session := "session-prepare"

	deliver := func(session string) Outcome {
		t.Helper()

		event := claudeEvent(root, session, "", "toolu_"+session, "prompt-1", "api/handler.go")
		out, err := Deliver(context.Background(), st, claudeRequest(root, cfg, event), (&capture{}).emit)
		require.NoError(t, err)

		return out
	}

	prepareAll := func() {
		t.Helper()

		for _, surface := range []Surface{SurfacePrepare, SurfaceChangeSet} {
			req := prepareRequest(root, cfg, "api/handler.go", "db/query.go")
			req.Surface = surface
			req.EventID = nil

			p := mustPrepare(t, st, req)
			require.NotEmpty(t, p.Pins)
		}
	}

	t.Run("a preparation before the first edit marks nothing delivered", func(t *testing.T) {
		prepareAll()

		assert.NoFileExists(t, statePath, "preparation creates no edit state")
		assert.NoFileExists(t, explorePath, "preparation creates no exploration state")
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.lock"))
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-explore-state.lock"))

		assert.Equal(t, StatusEmitted, deliver(session).Status, "the first edit still gets its reminder")
	})

	// Both state files hold an entry now. The exploration entry comes
	// from the exploration lease, as the exploration hook writes it.
	dc := reviews.DeliveryContext{ClientID: integration.ClaudeID, ReceiverID: receiverOf(session)}
	lease, err := reviews.BeginExploreDelivery(root, dc, []reviews.ExploreItem{
		{Kind: reviews.ExploreItemPin, ID: cfg.PinVersion(cfg.Pin[0])},
	})
	require.NoError(t, err)
	require.NoError(t, lease.Commit())
	require.NoError(t, lease.Close())

	editBefore, err := os.ReadFile(statePath)
	require.NoError(t, err)
	exploreBefore, err := os.ReadFile(explorePath)
	require.NoError(t, err)

	listing := func() []string {
		entries, err := os.ReadDir(filepath.Join(root, ".seamark"))
		require.NoError(t, err)

		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}

		return names
	}
	namesBefore := listing()

	t.Run("a preparation after an edit keeps both states byte for byte", func(t *testing.T) {
		prepareAll()
		prepareAll()

		editAfter, err := os.ReadFile(statePath)
		require.NoError(t, err)
		exploreAfter, err := os.ReadFile(explorePath)
		require.NoError(t, err)

		assert.Equal(t, editBefore, editAfter, "the edit state is never read for a write")
		assert.Equal(t, exploreBefore, exploreAfter)
		assert.Equal(t, namesBefore, listing(), "preparation adds no state or lock file")

		out := deliver(session)
		assert.Equal(t, StatusSuppressed, out.Status, "preparation does not reset the edit context")
		assert.Equal(t, uint64(1), out.Generation)
	})
}

func TestPrepareRecordsAReturnedEventWithoutReceiverIdentity(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "change_budget: 2\n"+prepareTwoPins+`  - rule: retry-idempotency
    region: api
    note: Every retried call must be idempotent.
  - rule: cache-invalidation
    region: api
    note: Drop cached rows after each write.
`)
	outside := filepath.Join(t.TempDir(), "outside.go")

	p := mustPrepare(t, st, prepareRequest(root, cfg, "api/handler.go", outside))
	lines := rawAuditLines(t, root)
	require.Len(t, lines, 1, "one record per call")

	line := lines[0]
	assert.Equal(t, prepareEventID, p.EventID)

	t.Run("the record names the event, the scope, and every matched pin", func(t *testing.T) {
		records := firings(t, root)
		require.Len(t, records, 1)

		rec := records[0]
		assert.Equal(t, "prepare", rec.Surface)
		assert.Equal(t, "api/handler.go", rec.File, "the record holds the resolved path, not the raw input")
		assert.Equal(t, []string{
			"boundary-validation — Validate request payloads at the edge.",
			"retry-idempotency — Every retried call must be idempotent.",
		}, symptoms(rec.Fired))
		assert.Empty(t, rec.Delivery, "a returned record has no record delivery")
		assert.Equal(t, 2, rec.SchemaVersion)
		assert.Equal(t, prepareEventID, rec.EventID)
		assert.Equal(t, 1, rec.EventSeq)
		assert.Equal(t, 1, rec.EventRecords)
		assert.Equal(t, "returned", rec.Emission)
		assert.Equal(t, "complete", rec.Evaluation)
		require.NotNil(t, rec.PathsComplete)
		assert.False(t, *rec.PathsComplete, "one input was rejected")
		assert.Equal(t, 1, rec.RejectedPaths)
		assert.Equal(t, p.Guidance.GuidanceSHA256, rec.GuidanceSHA)
		assert.Equal(t, "ok", rec.GuidanceStatus)
		assert.Equal(t, p.Request.ScopeSHA256, rec.ScopeSHA)
		assert.Equal(t, preparedAt.Format(time.RFC3339), rec.TS)
		assert.Equal(t, []reviews.FiringPin{
			pinOf(cfg, cfg.Pin[0], reviews.PinReturned, ""),
			pinOf(cfg, cfg.Pin[2], reviews.PinReturned, ""),
			pinOf(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverTotal),
		}, rec.Pins)
	})

	t.Run("the record has no receiver identity", func(t *testing.T) {
		for _, key := range []string{
			"session_sha256", "match_sha256", "context_sha256", "turn_sha256", "lane", "client", "mechanism",
			"context_resettable", "record_evaluations", "context_generation", "explore_generation", "read",
			"context_bytes", "tool", "delivery",
		} {
			assert.NotContains(t, line, key)
		}

		assert.Equal(t, Receiver{Lane: "unknown", Reason: "no-receiver-identity"}, p.Receiver)
	})

	t.Run("change_set records under its own surface with a random event ID", func(t *testing.T) {
		req := prepareRequest(root, cfg, "api/handler.go", "db/query.go")
		req.Surface = SurfaceChangeSet
		req.EventID = nil

		p := mustPrepare(t, st, req)
		records := firings(t, root)
		require.Len(t, records, 2)

		rec := records[1]
		assert.Equal(t, "change_set", rec.Surface)
		assert.Equal(t, []string{"api/handler.go", "db/query.go"}, rec.Files)
		assert.Regexp(t, "^[0-9a-f]{32}$", p.EventID)
		assert.Equal(t, p.EventID, rec.EventID)
		require.NotNil(t, rec.PathsComplete)
		assert.True(t, *rec.PathsComplete)
	})

	t.Run("nothing selected writes nothing", func(t *testing.T) {
		p := mustPrepare(t, st, prepareRequest(root, cfg, "a.go"))

		assert.Empty(t, p.EventID)
		assert.Len(t, firings(t, root), 2)
	})

	t.Run("a failed append leaves the event ID empty", func(t *testing.T) {
		blocked := workspace(t)
		blockedCfg := writeConfig(t, blocked, prepareTwoPins)
		require.NoError(t, os.Mkdir(reviews.AuditLogPath(blocked), 0o755))

		p := mustPrepare(t, prepareMemoryStore(t), prepareRequest(blocked, blockedCfg, "api/handler.go"))

		assert.Len(t, p.Pins, 1, "the preparation does not depend on the log")
		assert.Empty(t, p.EventID)
	})

	t.Run("a cancelled call records nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := Prepare(ctx, st, prepareRequest(root, cfg, "api/handler.go"))
		require.ErrorIs(t, err, context.Canceled)
		assert.Len(t, firings(t, root), 2)
	})
}

// TestPrepareHandsItsRecordToTheCaller proves that Prepare appends no
// firing record itself. The caller writes the record after the rest of
// its answer succeeded, so a failed answer leaves no returned record.
func TestPrepareHandsItsRecordToTheCaller(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, prepareTwoPins)

	p, err := Prepare(context.Background(), st, prepareRequest(root, cfg, "api/handler.go"))
	require.NoError(t, err)

	require.Len(t, p.Pins, 1, "the preparation selected a pin")
	assert.Empty(t, p.EventID, "no record names the preparation yet")
	assert.Empty(t, rawAuditLines(t, root), "Prepare alone writes no record")

	t.Run("Record appends the one record and names it", func(t *testing.T) {
		p.Record()

		assert.Equal(t, prepareEventID, p.EventID)

		records := firings(t, root)
		require.Len(t, records, 1)
		assert.Equal(t, prepareEventID, records[0].EventID)
		assert.Equal(t, "prepare", records[0].Surface)
		assert.Equal(t, "returned", records[0].Emission)
	})

	t.Run("a second Record writes nothing", func(t *testing.T) {
		p.Record()

		assert.Equal(t, prepareEventID, p.EventID)
		assert.Len(t, firings(t, root), 1, "one call gives at most one record")
	})

	t.Run("a preparation without a lesson has no record", func(t *testing.T) {
		p, err := Prepare(context.Background(), st, prepareRequest(root, cfg, "a.go"))
		require.NoError(t, err)

		p.Record()

		assert.Empty(t, p.EventID)
		assert.Len(t, firings(t, root), 1)
	})
}

// TestPreparationJSONMatchesTheReferenceDocument proves the
// lesson-preparation/1 document of one fixed preparation and its two
// text forms. The preparation reads prepareExampleConfig as
// .seamark/lessons.yaml with a fixed time and event ID. So the reference
// document want can fix the value of every field.
func TestPreparationJSONMatchesTheReferenceDocument(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "generated/a.txt")

	cfg := writeConfig(t, root, prepareExampleConfig)
	st := prepareMemoryStore(t)

	p := mustPrepare(t, st, prepareRequest(root, cfg, "generated/a.txt", "internal/parse/strict.go"))

	got, err := json.Marshal(p)
	require.NoError(t, err)

	// want is the reference document. Describe owns the predicate
	// wording, so want takes the predicate from Describe and not from a
	// fixed text. want lists the pins in delivery order, as Selection.Pins
	// holds them: the deeper region internal/parse ranks before entry 1.
	predicate, err := lessoncheck.Describe(cfg.Checks["frozen-assets"])
	require.NoError(t, err)

	want := `{
  "schema": "lesson-preparation/1",
  "seamark_version": "v0.8.0-31-g96e14ef",
  "prepared_at": "2026-10-21T10:15:02Z",
  "surface": "prepare",
  "receiver": {"lane": "unknown", "reason": "no-receiver-identity"},
  "request": {
    "requested": ["generated/a.txt", "internal/parse/strict.go"],
    "paths": [
      {"path": "generated/a.txt", "state": "file", "pins": 1, "checks": 1},
      {"path": "internal/parse/strict.go", "state": "absent", "pins": 1, "checks": 0}
    ],
    "rejected_count": 0,
    "scope_sha256": "99e15a982fd909d6d06808aa14fb4f94118a3ca1ea25949bc768889c445aafff"
  },
  "guidance": {"status": "ok", "guidance_sha256": "bdeadde9766c9bca721cbdeea2af6a929d344f35e394847a06468dd59545d4e3", "budget": 6, "mined_status": "ok", "checks_status": "valid"},
  "pins": [
    {"pin_ref": "ed5d4af5de3a", "pin_identity": "ed5d4af5de3a230a089f19fb7de4c2b04a512cbec6dfb023eabc944d93a82488", "pin_version": "f128e3e12b9d12825e8f7a4396ebb003b517d1e162955edb04bf147a5aa89897", "entry": 2, "rule": "keep-parser-strict", "note": "Reject unknown fields in the parser.", "scope": ["internal/parse"], "mode": "advisory", "paths": ["internal/parse/strict.go"]},
    {"pin_ref": "3215e7b483d1", "pin_identity": "3215e7b483d134443a8204970767f1a0057d4726f235e946b6c0c27abd8cd055", "pin_version": "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e", "entry": 1, "rule": "frozen-assets", "note": "Changes to these files require a maintainer-managed update.", "scope": ["generated"], "mode": "enforce", "paths": ["generated/a.txt"]}
  ],
  "lessons": [],
  "checks": [
    {"pin_ref": "3215e7b483d1", "pin_version": "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e", "mode": "enforce", "runs": true, "check": {"id": "frozen-assets", "kind": "forbid-path-change/1", "version": "9ceb9cddb5053035c0d9779ffab326503647b5295cb3f43791d5fe1e9be9cf7a"}, "predicate": ` +
		jsonString(t, predicate) + `, "paths": ["generated/a.txt"]}
  ],
  "omitted": [],
  "refresh": ["paths-added", "guidance-changed", "receiver-changed"],
  "notice": "Preparation is advisory: it authorizes no write, it does not show that a model read it, and every covered edit still meets its active checks. Plan with it, then construct the change in a later step, not in the same generated program.",
  "event_id": "53cdb75d6777b20e3d59e77cecd39b31"
}`

	assert.JSONEq(t, want, string(got))

	t.Run("the scope digest is the compose vector", func(t *testing.T) {
		assert.Equal(t, "99e15a982fd909d6d06808aa14fb4f94118a3ca1ea25949bc768889c445aafff",
			scopeDigest([]string{"internal/parse/strict.go", "generated/a.txt", "generated/a.txt"}))
		assert.Equal(t, reviews.ComposeDigest("prep-scope/1", "0"), scopeDigest(nil))
	})

	t.Run("the full text", func(t *testing.T) {
		assert.Equal(t, `lesson preparation (lesson-preparation/1) for 2 path(s) — receiver unknown
guidance bdeadde9766c · change budget 6 · mined lessons ok · checks valid

paths
  generated/a.txt (file) · 1 pin · 1 check
  internal/parse/strict.go (absent) · 1 pin · 0 checks

pins
- pin ed5d4af5de3a · version f128e3e12b9d · advisory · entry 2 · keep-parser-strict — Reject unknown fields in the parser.
  scope internal/parse · paths internal/parse/strict.go
- pin 3215e7b483d1 · version ae4f17f75413 · enforce · entry 1 · frozen-assets — Changes to these files require a maintainer-managed update.
  scope generated · paths generated/a.txt

checks (every check in scope; no budget applies)
- pin 3215e7b483d1 · version ae4f17f75413 · enforce · runs · check frozen-assets (forbid-path-change/1, version 9ceb9cddb505)
  `+predicate+`
  paths generated/a.txt

prepare again when the file set grows, when .seamark/lessons.yaml changes (guidance bdeadde9766c), or when another agent or subagent will construct the change
`+PreparationNotice+"\n", preparationText(p, FormFull))
	})

	t.Run("the supplement text", func(t *testing.T) {
		assert.Equal(t, `lesson preparation (lesson-preparation/1) for 2 path(s) — receiver unknown
guidance bdeadde9766c · change budget 6 · mined lessons ok · checks valid

pins
- pin ed5d4af5de3a · version f128e3e12b9d · advisory · entry 2 · keep-parser-strict
- pin 3215e7b483d1 · version ae4f17f75413 · enforce · entry 1 · frozen-assets

checks (every check in scope; no budget applies)
- pin 3215e7b483d1 · version ae4f17f75413 · enforce · runs · check frozen-assets (forbid-path-change/1, version 9ceb9cddb505)
  `+predicate+`
  paths generated/a.txt

prepare again when the file set grows, when .seamark/lessons.yaml changes (guidance bdeadde9766c), or when another agent or subagent will construct the change
`+PreparationNotice+"\n", preparationText(p, FormSupplement))
	})

	t.Run("a supplement without anything to add is empty", func(t *testing.T) {
		bare := mustPrepare(t, st, prepareRequest(root, cfg, "docs/readme.md"))

		assert.Empty(t, bare.Pins)
		assert.Empty(t, preparationText(bare, FormSupplement))
		assert.NotEmpty(t, preparationText(bare, FormFull))
		assert.Empty(t, preparationText(p, "html"), "the form list is closed")
	})

	t.Run("every list encodes as an array", func(t *testing.T) {
		bare := mustPrepare(t, st, prepareRequest(root, cfg))

		encoded, err := json.Marshal(bare)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "null")
	})
}

// jsonString encodes s as a JSON string literal.
func jsonString(t *testing.T, s string) string {
	t.Helper()

	encoded, err := json.Marshal(s)
	require.NoError(t, err)

	return string(encoded)
}

// TestEnforcementTextNamesTheLimit proves that the enforcement line of a
// prepared check names the limit of an enforced badge.
func TestEnforcementTextNamesTheLimit(t *testing.T) {
	got := enforcementText([]lessoncheck.PinClient{
		{Client: "claude", Badge: lessoncheck.BadgeEnforced, Limits: []string{"the client lets the write run after its 10 s hook timeout"}},
		{Client: "codex", Badge: lessoncheck.BadgeRequestedInactive, Reasons: []lessoncheck.CapReason{lessoncheck.CapNotInstalled}},
	})
	assert.Equal(t, "claude enforced (limit: the client lets the write run after its 10 s hook timeout) · "+
		"codex requested-inactive (not-installed)", got)
}
