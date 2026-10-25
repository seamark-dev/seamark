package lessoncheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the helpers that the checker tests share, and the
// tests of the registry and of Evaluate itself. The tests build the
// snapshots by hand, because the producers of milestone P1 (git) and
// milestone P4 (projection, observation) are not under test here.

// fileSide is a regular file side with the given content. The content
// state comes from the text test, as a producer sets it.
func fileSide(content string) Side {
	sum := sha256.Sum256([]byte(content))

	return Side{
		Type:    ObjectFile,
		Mode:    "100644",
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(content)),
		Content: textState([]byte(content)),
		text:    []byte(content),
	}
}

// linkSide is a symlink side whose content is the link target.
func linkSide(target string) Side {
	side := fileSide(target)
	side.Type = ObjectSymlink
	side.Mode = "120000"

	return side
}

// stateSide is a file side of the given size whose content the producer
// did not read as text.
func stateSide(state ContentState, size int64) Side {
	return Side{Type: ObjectFile, Mode: "100644", Size: size, Content: state}
}

// absentSide is the side of a path that does not exist.
func absentSide() Side {
	return Side{Type: ObjectAbsent, Content: ContentNotRead}
}

// addChange is an added path.
func addChange(path string, after Side) Change {
	return Change{Path: path, Kind: ChangeAdd, Before: absentSide(), After: after, ContentChanged: true}
}

// deleteChange is a deleted path.
func deleteChange(path string, before Side) Change {
	return Change{Path: path, Kind: ChangeDelete, Before: before, After: absentSide(), ContentChanged: true}
}

// modifyChange is a changed path. The flags compare the two sides, as a
// producer sets them.
func modifyChange(path string, before, after Side) Change {
	return Change{
		Path:           path,
		Kind:           ChangeModify,
		Before:         before,
		After:          after,
		ContentChanged: before.SHA256 != after.SHA256,
		ModeChanged:    before.Mode != after.Mode,
		TypeChanged:    before.Type != after.Type,
	}
}

// moveChange is a move from oldPath to path.
func moveChange(oldPath, path string, before, after Side) Change {
	ch := modifyChange(path, before, after)
	ch.OldPath = oldPath
	ch.Kind = ChangeMove
	ch.Move = MoveGitRename

	return ch
}

// snapshotOf is a complete snapshot of the given boundary.
func snapshotOf(boundary Boundary, changes ...Change) *Snapshot {
	return &Snapshot{
		Schema:        SnapshotSchema,
		Boundary:      boundary,
		Baseline:      Baseline{Kind: BaselineHead, Commit: "96e14ef0a1b2c3d4e5f60718293a4b5c6d7e8f90"},
		Selection:     Selection{Kind: SelectionDefault, Excludes: []string{}},
		Changes:       append([]Change{}, changes...),
		PathsComplete: true,
	}
}

// parseValid parses a lessons.yaml text that must be valid.
func parseValid(t *testing.T, text string) *Config {
	t.Helper()

	cfg, errs, err := Parse([]byte(text))
	require.NoError(t, err)
	require.Empty(t, errs)

	return cfg
}

// onePin is a valid config with one executable pin "rule-check" (mode
// check) bound to one definition of the given kind. pattern is "" for
// forbid-path-change/1. region "" makes the pin repository-wide.
func onePin(t *testing.T, kind Kind, pattern, region string, regions ...string) *Config {
	t.Helper()

	var b strings.Builder

	b.WriteString("lesson_checks_version: 1\nchecks:\n  rule-check:\n    kind: " + string(kind) + "\n")

	if pattern != "" {
		b.WriteString("    pattern: '" + strings.ReplaceAll(pattern, "'", "''") + "'\n")
	}

	b.WriteString("pin:\n  - rule: rule-check\n    region: " + strconv.Quote(region) + "\n")

	if len(regions) > 0 {
		b.WriteString("    regions: [" + strings.Join(regions, ", ") + "]\n")
	}

	b.WriteString("    note: A test pin.\n    mode: check\n    check: rule-check\n")

	return parseValid(t, b.String())
}

// onlyResult evaluates a config with one executable pin.
func onlyResult(t *testing.T, cfg *Config, snap *Snapshot) Result {
	t.Helper()

	results := Evaluate(cfg, snap)
	require.Len(t, results, 1)

	return results[0]
}

// mixedYAML holds one pin of each kind of state, in file order: a forbid
// pin, a pattern pin, an advisory pin without a check, and an advisory
// pin with a check.
const mixedYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen
    region: generated
    note: Do not edit generated files.
    mode: enforce
    check: frozen
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: prose
    region: src
    note: An advisory pin without a check.
  - rule: later
    region: src
    note: An advisory pin with a check.
    check: no-todo
`

// mixedSnapshot changes both scopes of mixedYAML, out of producer order.
func mixedSnapshot() *Snapshot {
	return snapshotOf(BoundaryWorkingTree,
		modifyChange("src/b.go", fileSide("a\n"), fileSide("a\n// TODO(x)\n")),
		addChange("generated/z.txt", fileSide("z\n")),
		deleteChange("generated/a.txt", fileSide("a\n")),
		addChange("src/a.go", stateSide(ContentBinary, 3)),
	)
}

func TestLessonCheckUnknownReasonPrecedence(t *testing.T) {
	t.Run("the order is the K3 list", func(t *testing.T) {
		assert.Equal(t, []Reason{
			"configuration-error", "unparseable-input", "incomplete-paths", "missing-baseline",
			"unprojectable-edit", "content-unavailable", "unsupported-content", "resource-limit",
			"interrupted", "stale-snapshot",
		}, reasonPrecedence)
	})

	t.Run("an earlier reason wins over every later reason", func(t *testing.T) {
		for i, first := range reasonPrecedence {
			for _, later := range reasonPrecedence[i+1:] {
				reason, _ := pickReason([]reasonHit{{reason: later}, {reason: first}})
				assert.Equal(t, first, reason, "%s against %s", first, later)
			}
		}
	})

	t.Run("resource-limit keeps the limit of its first cause", func(t *testing.T) {
		reason, limit := pickReason([]reasonHit{
			{reason: ReasonStaleSnapshot},
			{reason: ReasonResourceLimit, limit: LimitMaxFileBytes},
			{reason: ReasonResourceLimit, limit: LimitMaxDiffLines},
		})

		assert.Equal(t, ReasonResourceLimit, reason)
		assert.Equal(t, LimitMaxFileBytes, limit)

		reason, limit = pickReason([]reasonHit{
			{reason: ReasonResourceLimit, limit: LimitMaxFileBytes},
			{reason: ReasonUnsupportedContent},
		})

		assert.Equal(t, ReasonUnsupportedContent, reason)
		assert.Empty(t, limit, "a reason other than resource-limit names no limit")
	})

	t.Run("evaluate picks by precedence and lists every unknown path", func(t *testing.T) {
		cfg := onePin(t, KindNoAddedPattern, `TODO\(`, "src")
		snap := snapshotOf(BoundaryWorkingTree,
			addChange("src/c.bin", stateSide(ContentBinary, 4)),
			addChange("src/a.go", stateSide(ContentUnreadable, 4)),
			modifyChange("src/b.go", stateSide(ContentUnreadable, 4), fileSide("b\n")),
			addChange("src/d.big", stateSide(ContentTooLarge, 5*mebibyte)),
		)

		res := onlyResult(t, cfg, snap)

		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonMissingBaseline, res.Reason)
		assert.Empty(t, res.Limit)
		assert.Equal(t, []Evidence{
			{Path: "src/a.go", Change: ChangeAdd, Reason: ReasonContentUnavailable},
			{Path: "src/b.go", Change: ChangeModify, Reason: ReasonMissingBaseline},
			{Path: "src/c.bin", Change: ChangeAdd, Reason: ReasonUnsupportedContent},
			{Path: "src/d.big", Change: ChangeAdd, Reason: ReasonResourceLimit, limit: LimitMaxFileBytes},
		}, res.UnknownPaths)
		assert.Equal(t, Coverage{PathsInScope: 4, PathsEvaluated: 0}, res.Coverage)
	})

	t.Run("an incomplete path set beats a missing baseline", func(t *testing.T) {
		cfg := onePin(t, KindNoAddedPattern, `TODO\(`, "src")
		snap := snapshotOf(BoundaryWorkingTree,
			modifyChange("src/b.go", stateSide(ContentUnreadable, 4), fileSide("b\n")))
		snap.PathsComplete = false

		res := onlyResult(t, cfg, snap)

		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonIncompletePaths, res.Reason)
	})

	t.Run("an invalid config runs no checker", func(t *testing.T) {
		// invalidYAML (config_test.go) has an advisory pin 1 and a check
		// pin 2 whose check ID names no definition.
		cfg, errs, err := Parse([]byte(invalidYAML))
		require.NoError(t, err)
		require.Len(t, errs, 2)

		violating := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide("TODO(x)\n")))

		for name, snap := range map[string]*Snapshot{"with a snapshot": violating, "without a snapshot": nil} {
			results := Evaluate(cfg, snap)
			require.Len(t, results, 1, name)

			res := results[0]
			assert.Equal(t, 2, res.Entry, name)
			assert.Equal(t, ModeCheck, res.Mode, name)
			assert.Equal(t, "no-todo", res.Rule, name)
			assert.Equal(t, []string{"src"}, res.Scope, name)
			assert.Equal(t, OutcomeUnknown, res.Outcome, name)
			assert.Equal(t, ReasonConfigurationError, res.Reason, name)
			assert.Equal(t, CheckRef{ID: "no-todos"}, res.Check, "an undefined check has no kind and no version")
			assert.Equal(t, Coverage{}, res.Coverage, name)
			assert.Empty(t, res.Evidence, name)
		}
	})

	t.Run("an invalid config keeps the written definition in the check reference", func(t *testing.T) {
		// The definition is valid on its own; the missing version makes
		// the file invalid (version-missing).
		cfg, errs, err := Parse([]byte(`checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: enforce
    check: no-todo
`))
		require.NoError(t, err)
		require.Len(t, errs, 1)
		require.Equal(t, CodeVersionMissing, errs[0].Code)

		res := onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree))

		assert.Equal(t, ReasonConfigurationError, res.Reason)
		assert.Equal(t, CheckRef{ID: "no-todo", Kind: KindNoAddedPattern, Version: noTodoVersion}, res.Check)
	})

	t.Run("a valid config without its compiled check fails closed", func(t *testing.T) {
		cfg := onePin(t, KindNoAddedPattern, `TODO\(`, "src")
		cfg.Checks = map[string]Check{}

		res := onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree))

		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonConfigurationError, res.Reason)

		cfg = onePin(t, KindNoAddedPattern, `TODO\(`, "src")
		check := cfg.Checks["rule-check"]
		check.Pattern = nil
		cfg.Checks["rule-check"] = check

		res = onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide("x\n"))))

		assert.Equal(t, ReasonConfigurationError, res.Reason, "a pattern check without a pattern never passes")
	})
}

func TestLessonCheckResourceLimits(t *testing.T) {
	pattern := onePin(t, KindNoAddedPattern, `TODO\(`, "src")
	forbid := onePin(t, KindForbidPathChange, "", "src")

	bounded := func(snap *Snapshot, edit func(*Limits)) *Snapshot {
		lim := DefaultLimits()
		edit(&lim)
		snap.bounds = lim

		return snap
	}

	t.Run("max-diff-lines stops one comparison", func(t *testing.T) {
		long := snapshotOf(BoundaryWorkingTree, modifyChange("src/a.go", fileSide("1\n2\n3\n4\n"), fileSide("1\n2\n3\n4\nTODO(x)\n")))
		res := onlyResult(t, pattern, bounded(long, func(l *Limits) { l.MaxDiffLines = 4 }))

		assert.Equal(t, OutcomeUnknown, res.Outcome, "a cut comparison is never a pass")
		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxDiffLines, res.Limit)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeModify, Reason: ReasonResourceLimit, limit: LimitMaxDiffLines}},
			res.UnknownPaths, "the unknown path names its limit for Verification.Limits (fix F-D)")

		res = onlyResult(t, pattern, bounded(long, func(l *Limits) { l.MaxDiffLines = 5 }))
		assert.Equal(t, OutcomeViolation, res.Outcome, "a side of exactly the limit is compared")
	})

	t.Run("an added file needs no comparison", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide("1\n2\n3\nTODO(x)\n")))
		res := onlyResult(t, pattern, bounded(snap, func(l *Limits) { l.MaxDiffLines = 1 }))

		assert.Equal(t, OutcomeViolation, res.Outcome)
	})

	t.Run("a too-large side names the byte limit that stopped it", func(t *testing.T) {
		big := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", stateSide(ContentTooLarge, 4*mebibyte+1)))
		res := onlyResult(t, pattern, big)

		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxFileBytes, res.Limit)

		total := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", stateSide(ContentTooLarge, 10)))
		res = onlyResult(t, pattern, total)

		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxSnapshotBytes, res.Limit, "a small side is cut by the snapshot total")

		res = onlyResult(t, forbid, big)
		assert.Equal(t, OutcomeViolation, res.Outcome, "forbid-path-change/1 reads no content")
	})

	t.Run("a path-set limit makes both checkers unknown", func(t *testing.T) {
		cut := func() *Snapshot {
			snap := snapshotOf(BoundaryWorkingTree, addChange("lib/a.go", fileSide("x\n")))
			snap.PathsComplete = false
			snap.Limits = []LimitHit{{Name: LimitMaxFileBytes, Value: 4 * mebibyte, Paths: 1}, {Name: LimitMaxSnapshotPaths, Value: 5000, Paths: 3}}

			return snap
		}

		for name, cfg := range map[string]*Config{"forbid": forbid, "pattern": pattern} {
			res := onlyResult(t, cfg, cut())

			assert.Equal(t, OutcomeUnknown, res.Outcome, name)
			assert.Equal(t, ReasonResourceLimit, res.Reason, name)
			assert.Equal(t, LimitMaxSnapshotPaths, res.Limit, "%s: a content limit never explains a cut path set", name)
		}
	})

	t.Run("an incomplete path set without a path-set limit is incomplete-paths", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree)
		snap.PathsComplete = false
		snap.Limits = []LimitHit{{Name: LimitMaxFileBytes, Value: 4 * mebibyte, Paths: 1}}

		for name, cfg := range map[string]*Config{"forbid": forbid, "pattern": pattern} {
			res := onlyResult(t, cfg, snap)

			assert.Equal(t, ReasonIncompletePaths, res.Reason, name)
			assert.Empty(t, res.Limit, name)
		}
	})

	t.Run("a clean change in a cut path set is never a pass", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide("clean\n")))
		snap.PathsComplete = false
		snap.Limits = []LimitHit{{Name: LimitMaxSnapshotPaths, Value: 5000, Paths: 1}}

		res := onlyResult(t, pattern, snap)

		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxSnapshotPaths, res.Limit)
		assert.Equal(t, Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 1}, res.Coverage)
	})

	t.Run("an observed violation stays a violation", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree,
			addChange("src/a.go", fileSide("TODO(x)\n")),
			addChange("src/b.go", stateSide(ContentTooLarge, 5*mebibyte)))
		snap.PathsComplete = false
		snap.Limits = []LimitHit{{Name: LimitMaxSnapshotPaths, Value: 5000, Paths: 1}}

		for name, cfg := range map[string]*Config{"forbid": forbid, "pattern": pattern} {
			res := onlyResult(t, cfg, snap)

			assert.Equal(t, OutcomeViolation, res.Outcome, name)
			assert.Empty(t, res.Reason, "%s: a violation carries no reason", name)
			assert.Empty(t, res.Limit, name)
		}

		res := onlyResult(t, pattern, snap)
		assert.Equal(t, []Evidence{{Path: "src/b.go", Change: ChangeAdd, Reason: ReasonResourceLimit, limit: LimitMaxFileBytes}}, res.UnknownPaths)
	})
}

// pureFiles are the files that implement Evaluate. They must not import
// a package that does I/O or reads the clock.
var pureFiles = []string{"registry.go", "forbid.go", "pattern.go", "lines.go", "snapshot.go"}

// impureImports are the standard packages that do I/O, read the clock,
// or give random values.
var impureImports = []string{"os", "os/exec", "io", "io/fs", "io/ioutil", "net", "net/http", "time", "math/rand", "math/rand/v2", "crypto/rand", "syscall", "path/filepath", "runtime"}

func TestLessonCheckEvaluateIsPure(t *testing.T) {
	t.Run("the evaluation files import nothing that does I/O", func(t *testing.T) {
		for _, name := range pureFiles {
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			require.NoError(t, err)

			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				require.NoError(t, err)
				assert.NotContains(t, impureImports, path, "%s imports %s", name, path)
			}
		}
	})

	t.Run("equal inputs give equal results and the inputs stay unchanged", func(t *testing.T) {
		cfg := parseValid(t, mixedYAML)
		snap := mixedSnapshot()

		first := Evaluate(cfg, snap)

		for range 20 {
			again := Evaluate(cfg, snap)
			require.Equal(t, first, again)
		}

		assert.Equal(t, parseValid(t, mixedYAML), cfg, "Evaluate changes no config")
		assert.Equal(t, mixedSnapshot(), snap, "Evaluate changes no snapshot")

		firstJSON, err := json.Marshal(first)
		require.NoError(t, err)

		againJSON, err := json.Marshal(Evaluate(cfg, snap))
		require.NoError(t, err)
		assert.Equal(t, string(firstJSON), string(againJSON))
	})

	t.Run("the producer order does not change the result", func(t *testing.T) {
		cfg := parseValid(t, mixedYAML)
		snap := mixedSnapshot()
		reversed := mixedSnapshot()

		for i, j := 0, len(reversed.Changes)-1; i < j; i, j = i+1, j-1 {
			reversed.Changes[i], reversed.Changes[j] = reversed.Changes[j], reversed.Changes[i]
		}

		assert.Equal(t, Evaluate(cfg, snap), Evaluate(cfg, reversed))
	})

	t.Run("every executable pin in file order and no advisory pin", func(t *testing.T) {
		cfg := parseValid(t, mixedYAML)
		results := Evaluate(cfg, mixedSnapshot())

		require.Len(t, results, 2)
		assert.Equal(t, 1, results[0].Entry)
		assert.Equal(t, ModeEnforce, results[0].Mode)
		assert.Equal(t, OutcomeViolation, results[0].Outcome)
		assert.Equal(t, []Evidence{
			{Path: "generated/a.txt", Change: ChangeDelete},
			{Path: "generated/z.txt", Change: ChangeAdd},
		}, results[0].Evidence, "evidence is sorted by path")
		assert.Equal(t, 2, results[1].Entry)
		assert.Equal(t, ModeCheck, results[1].Mode)
		assert.Equal(t, OutcomeViolation, results[1].Outcome)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeAdd, Reason: ReasonUnsupportedContent}}, results[1].UnknownPaths)
	})

	t.Run("no executable pin gives an empty list", func(t *testing.T) {
		cfg := parseValid(t, "pin:\n  - rule: prose\n    region: src\n    note: Advisory only.\n")
		results := Evaluate(cfg, mixedSnapshot())

		require.NotNil(t, results)
		assert.Empty(t, results)

		encoded, err := json.Marshal(results)
		require.NoError(t, err)
		assert.Equal(t, "[]", string(encoded))
	})

	t.Run("a result does not share its scope with the config", func(t *testing.T) {
		cfg := parseValid(t, mixedYAML)
		results := Evaluate(cfg, mixedSnapshot())

		results[0].Scope[0] = "changed"
		assert.Equal(t, []string{"generated"}, cfg.Pins[0].Scope)

		repoWide := onlyResult(t, onePin(t, KindForbidPathChange, "", ""), snapshotOf(BoundaryWorkingTree))
		require.NotNil(t, repoWide.Scope)

		encoded, err := json.Marshal(repoWide)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"scope":[]`, "a repository-wide scope encodes as []")
	})
}

func TestLessonCheckDescribe(t *testing.T) {
	t.Run("the registry holds the two kinds, sorted", func(t *testing.T) {
		assert.Equal(t, []Kind{KindForbidPathChange, KindNoAddedPattern}, Kinds())
	})

	t.Run("Validate accepts every registry kind", func(t *testing.T) {
		for _, kind := range Kinds() {
			def := reviews.CheckDef{Kind: string(kind)}
			if kind == KindNoAddedPattern {
				def.Pattern = "x"
			}

			cfg := &reviews.Config{LessonChecksVersion: 1, Checks: map[string]reviews.CheckDef{"c": def}}
			assert.Empty(t, Validate(cfg), kind)

			_, err := Describe(def)
			assert.NoError(t, err, kind)
		}
	})

	t.Run("each predicate as text", func(t *testing.T) {
		text, err := Describe(reviews.CheckDef{Kind: string(KindForbidPathChange)})
		require.NoError(t, err)
		assert.Equal(t, "violation if any change touches a file in scope: an add, a content change, "+
			"a deletion, a file mode or type change, or either end of a move; content is not read", text)

		text, err = Describe(reviews.CheckDef{Kind: string(KindNoAddedPattern), Pattern: `TODO\(`})
		require.NoError(t, err)
		assert.Equal(t, `violation if a line that a change adds in scope matches the Go regular expression 'TODO\('; `+
			"a new file adds every line; binary or unreadable content gives unknown", text)
	})

	t.Run("a control character in the pattern is not printed", func(t *testing.T) {
		text, err := Describe(reviews.CheckDef{Kind: string(KindNoAddedPattern), Pattern: "a\x1b[31mb"})
		require.NoError(t, err)
		assert.NotContains(t, text, "\x1b")
		assert.Contains(t, text, "'a[31mb'")
	})

	t.Run("a definition that no checker can run is an error", func(t *testing.T) {
		for name, def := range map[string]reviews.CheckDef{
			"unknown kind":  {Kind: "no-added-pattern/2", Pattern: "x"},
			"empty kind":    {},
			"empty pattern": {Kind: string(KindNoAddedPattern)},
		} {
			text, err := Describe(def)
			require.Error(t, err, name)
			assert.Empty(t, text, name)
		}

		_, err := Describe(reviews.CheckDef{Kind: "no-added-pattern/2"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"no-added-pattern/2"`)
	})
}
