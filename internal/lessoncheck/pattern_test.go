package lessoncheck

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patternExampleJSON is the expected JSON result of no-added-pattern/1
// for one match and one binary path. Its pin digests are fixed
// placeholders, so the test copies them into the result.
const patternExampleJSON = `{
  "pin_ref": "77c0a1d2e3f4",
  "pin_identity": "77c0a1d2e3f4a5b6c7d8e9f00112233445566778899aabbccddeeff001122334",
  "pin_version": "c1d2e3f4a5b6c7d8e9f0112233445566778899aabbccddeeff00112233445566",
  "entry": 2,
  "rule": "no-todo",
  "mode": "check",
  "scope": ["src"],
  "check": {"id": "no-todo", "kind": "no-added-pattern/1", "version": "c044c91d812033b1da35c51ad301b6a3a06b09503b8e1126b392c1d643d7aa3b"},
  "outcome": "violation",
  "evidence": [{"path": "src/handler.go", "change": "modify", "line": 42, "excerpt": "// TODO(yuri): remove"}],
  "unknown_paths": [{"path": "src/logo.bin", "change": "add", "reason": "unsupported-content"}],
  "coverage": {"paths_in_scope": 2, "paths_evaluated": 1, "added_lines": 14}
}`

// numberedLines returns n lines "<prefix> 1" to "<prefix> n", each with
// its terminator.
func numberedLines(prefix string, n int) string {
	var b strings.Builder

	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}

	return b.String()
}

// unreadSide is a side with the same content digest as fileSide(content)
// whose content the producer did not read, as for an identical move.
func unreadSide(content string) Side {
	side := fileSide(content)
	side.Content = ContentNotRead
	side.text = nil

	return side
}

func TestLessonCheckNoAddedPattern(t *testing.T) {
	cfg := onePin(t, KindNoAddedPattern, `TODO\(`, "src")

	t.Run("the full JSON result of a match beside a binary path", func(t *testing.T) {
		example := parseValid(t, contractYAML+`  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
`)
		before := numberedLines("line", 41)
		after := before + "// TODO(yuri): remove\n" + numberedLines("added", 13)
		snap := snapshotOf(BoundaryWorkingTree,
			modifyChange("src/handler.go", fileSide(before), fileSide(after)),
			addChange("src/logo.bin", fileSide("\x89PNG\x00\x01")),
		)

		results := Evaluate(example, snap)
		require.Len(t, results, 2)

		res := results[1]
		res.PinRef = "77c0a1d2e3f4"
		res.PinIdentity = "77c0a1d2e3f4a5b6c7d8e9f00112233445566778899aabbccddeeff001122334"
		res.PinVersion = "c1d2e3f4a5b6c7d8e9f0112233445566778899aabbccddeeff00112233445566"

		encoded, err := json.Marshal(res)
		require.NoError(t, err)
		assert.JSONEq(t, patternExampleJSON, string(encoded))
	})

	text := fileSide("a\nTODO(old)\n")
	exec := fileSide("a\nTODO(old)\n")
	exec.Mode = "100755"

	identical := moveChange("src/a.go", "src/b.go", unreadSide("TODO(old)\n"), fileSide("TODO(old)\n"))
	identical.Move = MoveIdenticalContent

	cases := []struct {
		name     string
		change   Change
		outcome  Outcome
		evidence []Evidence
		coverage Coverage
	}{
		{
			name:     "a new file adds every line",
			change:   addChange("src/a.go", fileSide("a\nTODO(a)\n")),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/a.go", Change: ChangeAdd, Line: 2, Excerpt: "TODO(a)"}},
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 2},
		},
		{
			name:     "a clean new file passes",
			change:   addChange("src/a.go", fileSide("a\nb")),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 2},
		},
		{
			name:     "a modify adds a match",
			change:   modifyChange("src/a.go", fileSide("a\nb\n"), fileSide("a\nb\nTODO(new)\n")),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/a.go", Change: ChangeModify, Line: 3, Excerpt: "TODO(new)"}},
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 1},
		},
		{
			name:     "an unchanged matching line does not violate",
			change:   modifyChange("src/a.go", text, fileSide("a\nTODO(old)\nclean\n")),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 1},
		},
		{
			name:     "a removed match does not violate",
			change:   modifyChange("src/a.go", text, fileSide("a\n")),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1},
		},
		{
			name:     "a deletion does not violate",
			change:   deleteChange("src/a.go", text),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1},
		},
		{
			name:     "a mode change alone adds no line",
			change:   modifyChange("src/a.go", text, exec),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1},
		},
		{
			name:     "an identical move inside the scope adds no line and reads no content",
			change:   identical,
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 2, PathsEvaluated: 2},
		},
		{
			name:     "an edited move inside the scope adds its diff",
			change:   moveChange("src/a.go", "src/b.go", text, fileSide("a\nTODO(old)\nTODO(new)\n")),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/b.go", OldPath: "src/a.go", Change: ChangeMove, Line: 3, Excerpt: "TODO(new)"}},
			coverage: Coverage{PathsInScope: 2, PathsEvaluated: 2, AddedLines: 1},
		},
		{
			name:     "a move into the scope adds the whole content",
			change:   moveChange("lib/a.go", "src/a.go", text, text),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/a.go", OldPath: "lib/a.go", Change: ChangeMove, Line: 2, Excerpt: "TODO(old)"}},
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 2},
		},
		{
			name:     "a move out of the scope is a deletion from it",
			change:   moveChange("src/a.go", "lib/a.go", text, fileSide("TODO(new)\n")),
			outcome:  OutcomePass,
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1},
		},
		{
			name:     "a link target is one line",
			change:   addChange("src/link", linkSide("TODO(x)")),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/link", Change: ChangeAdd, Line: 1, Excerpt: "TODO(x)"}},
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 1},
		},
		{
			name:     "a type change compares the file lines with the link target",
			change:   modifyChange("src/a.go", fileSide("a\n"), linkSide("TODO(x)")),
			outcome:  OutcomeViolation,
			evidence: []Evidence{{Path: "src/a.go", Change: ChangeModify, Line: 1, Excerpt: "TODO(x)"}},
			coverage: Coverage{PathsInScope: 1, PathsEvaluated: 1, AddedLines: 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree, tc.change))

			assert.Equal(t, tc.outcome, res.Outcome)
			assert.Empty(t, res.Reason)
			assert.Equal(t, tc.evidence, res.Evidence)
			assert.Empty(t, res.UnknownPaths)
			assert.Equal(t, tc.coverage, res.Coverage)
		})
	}

	labelled := fileSide("TODO(x)\x00")
	labelled.Content = ContentAvailable

	unknowns := []struct {
		name     string
		boundary Boundary
		change   Change
		reason   Reason
	}{
		{"binary after content", BoundaryWorkingTree, addChange("src/a.bin", fileSide("TODO(x)\x00")), ReasonUnsupportedContent},
		{"binary before content", BoundaryWorkingTree, modifyChange("src/a.go", fileSide("\x00"), fileSide("TODO(x)\n")), ReasonUnsupportedContent},
		{"invalid UTF-8", BoundaryWorkingTree, addChange("src/a.go", fileSide("TODO(\xff)\n")), ReasonUnsupportedContent},
		{"binary content labelled as text", BoundaryWorkingTree, addChange("src/a.go", labelled), ReasonUnsupportedContent},
		{"a gitlink", BoundaryWorkingTree, addChange("src/sub", Side{Type: ObjectGitlink, Mode: "160000", Content: ContentNotRead}), ReasonUnsupportedContent},
		{"another type", BoundaryWorkingTree, addChange("src/fifo", Side{Type: ObjectOther, Content: ContentNotRead}), ReasonUnsupportedContent},
		{"unreadable after content", BoundaryWorkingTree, addChange("src/a.go", stateSide(ContentUnreadable, 3)), ReasonContentUnavailable},
		{"after content not read", BoundaryWorkingTree, addChange("src/a.go", stateSide(ContentNotRead, 3)), ReasonContentUnavailable},
		{"an unreadable HEAD blob", BoundaryWorkingTree, modifyChange("src/a.go", stateSide(ContentUnreadable, 3), fileSide("x\n")), ReasonMissingBaseline},
		{"an absent baseline after an edit", BoundaryActualEdit, modifyChange("src/a.go", absentSide(), fileSide("x\n")), ReasonMissingBaseline},
		{"an unreadable pre-edit file before an edit", BoundaryProposedEdit, modifyChange("src/a.go", stateSide(ContentUnreadable, 3), fileSide("x\n")), ReasonContentUnavailable},
	}

	for _, tc := range unknowns {
		t.Run("unknown: "+tc.name, func(t *testing.T) {
			res := onlyResult(t, cfg, snapshotOf(tc.boundary, tc.change))

			assert.Equal(t, OutcomeUnknown, res.Outcome)
			assert.Equal(t, tc.reason, res.Reason)
			assert.Empty(t, res.Limit)
			assert.Empty(t, res.Evidence)
			assert.Equal(t, []Evidence{{Path: tc.change.Path, OldPath: tc.change.OldPath, Change: tc.change.Kind, Reason: tc.reason}}, res.UnknownPaths)
			assert.Equal(t, Coverage{PathsInScope: 1}, res.Coverage)
		})
	}

	t.Run("a violation keeps the unknown paths", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree,
			addChange("src/b.bin", fileSide("\x00")),
			addChange("src/a.go", fileSide("TODO(x)\n")))

		res := onlyResult(t, cfg, snap)

		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Empty(t, res.Reason)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeAdd, Line: 1, Excerpt: "TODO(x)"}}, res.Evidence)
		assert.Equal(t, []Evidence{{Path: "src/b.bin", Change: ChangeAdd, Reason: ReasonUnsupportedContent}}, res.UnknownPaths)
		assert.Equal(t, Coverage{PathsInScope: 2, PathsEvaluated: 1, AddedLines: 1}, res.Coverage)
	})

	t.Run("not-applicable and unknown without a change in scope", func(t *testing.T) {
		outside := snapshotOf(BoundaryWorkingTree, addChange("lib/a.go", fileSide("TODO(x)\n")), addChange("srcx/a.go", fileSide("TODO(x)\n")))

		res := onlyResult(t, cfg, outside)
		assert.Equal(t, OutcomeNotApplicable, res.Outcome)

		res = onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree))
		assert.Equal(t, OutcomeNotApplicable, res.Outcome)

		outside.PathsComplete = false
		res = onlyResult(t, cfg, outside)
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonIncompletePaths, res.Reason)
	})

	t.Run("an inline flag works", func(t *testing.T) {
		folded := onePin(t, KindNoAddedPattern, `(?i)todo\(`, "src")
		res := onlyResult(t, folded, snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide("Todo(x)\n"))))

		assert.Equal(t, OutcomeViolation, res.Outcome)
	})
}

// addedCount counts the added lines of the opcodes of a matcher, with
// the opcode loop of diffLines.
func addedCount(m *difflib.SequenceMatcher) int {
	n := 0

	for _, op := range m.GetOpCodes() {
		if op.Tag == 'i' || op.Tag == 'r' {
			n += op.J2 - op.J1
		}
	}

	return n
}

func TestLessonCheckDiffDisablesAutojunk(t *testing.T) {
	// A long file whose one line occurs often: autojunk treats a line
	// that fills more than 1% of a 200-line or longer file as junk.
	before := strings.Split(strings.TrimSuffix(strings.Repeat("x\n", 250), "\n"), "\n")
	after := append(append(append([]string{}, before[:125]...), "y"), before[125:]...)

	require.Equal(t, []addedLine{{number: 126, text: "y"}}, diffLines(before, after))

	// The forbidden matcher reads the frequent line as added, so this
	// test fails if diffLines ever uses NewMatcher.
	require.Greater(t, addedCount(difflib.NewMatcher(before, after)), 1)
	require.Equal(t, 1, addedCount(difflib.NewMatcherWithJunk(before, after, false, nil)))

	cfg := onePin(t, KindNoAddedPattern, `^x$`, "src")
	snap := snapshotOf(BoundaryWorkingTree, modifyChange("src/a.txt",
		fileSide(strings.Join(before, "\n")+"\n"), fileSide(strings.Join(after, "\n")+"\n")))

	res := onlyResult(t, cfg, snap)

	assert.Equal(t, OutcomePass, res.Outcome, "an unchanged frequent line is not an added line")
	assert.Equal(t, 1, res.Coverage.AddedLines)
}

func TestLessonCheckEvidenceBounds(t *testing.T) {
	cfg := onePin(t, KindNoAddedPattern, `TODO\(`, "src")
	many := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide(numberedLines("TODO(n)", 13))))

	t.Run("the default keeps ten entries and counts the rest", func(t *testing.T) {
		res := onlyResult(t, cfg, many)

		assert.Equal(t, OutcomeViolation, res.Outcome)
		require.Len(t, res.Evidence, 10)
		assert.Equal(t, 3, res.EvidenceOmitted)
		assert.Equal(t, 1, res.Evidence[0].Line)
		assert.Equal(t, 10, res.Evidence[9].Line)
		assert.Equal(t, 13, res.Coverage.AddedLines, "the cut changes the display only")
	})

	t.Run("an injected bound", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, many.Changes...)
		snap.bounds = DefaultLimits()
		snap.bounds.MaxEvidencePerResult = 2

		res := onlyResult(t, cfg, snap)

		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Len(t, res.Evidence, 2)
		assert.Equal(t, 11, res.EvidenceOmitted)
	})

	t.Run("forbid-path-change/1 evidence is bounded too", func(t *testing.T) {
		var changes []Change

		for i := range 12 {
			changes = append(changes, addChange(fmt.Sprintf("src/f%02d.go", i), fileSide("x\n")))
		}

		res := onlyResult(t, onePin(t, KindForbidPathChange, "", "src"), snapshotOf(BoundaryWorkingTree, changes...))

		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Len(t, res.Evidence, 10)
		assert.Equal(t, 2, res.EvidenceOmitted)
		assert.Equal(t, Coverage{PathsInScope: 12, PathsEvaluated: 12}, res.Coverage)
	})

	excerptOf := func(t *testing.T, line string, maxRunes int) string {
		t.Helper()

		snap := snapshotOf(BoundaryWorkingTree, addChange("src/a.go", fileSide(line+"\n")))
		snap.bounds = DefaultLimits()
		snap.bounds.MaxExcerptRunes = maxRunes

		res := onlyResult(t, cfg, snap)
		require.Len(t, res.Evidence, 1)

		return res.Evidence[0].Excerpt
	}

	t.Run("an excerpt is redacted", func(t *testing.T) {
		excerpt := excerptOf(t, `TODO( password = "hunter2" )`, 160)

		assert.NotContains(t, excerpt, "hunter2")
		assert.Contains(t, excerpt, "[REDACTED]")
	})

	t.Run("an excerpt holds no control character", func(t *testing.T) {
		excerpt := excerptOf(t, "\x1b[31mTODO(x)\x07", 160)

		assert.Equal(t, "[31mTODO(x)", excerpt)
	})

	t.Run("an excerpt is cut at the rune limit", func(t *testing.T) {
		line := "TODO(" + strings.Repeat("é", 300) + ")"

		excerpt := excerptOf(t, line, 160)
		assert.Equal(t, 160, utf8.RuneCountInString(excerpt))
		assert.True(t, strings.HasSuffix(excerpt, "…"))

		assert.Equal(t, "TODO(éééé…", excerptOf(t, line, 10))
	})

	t.Run("the rule is sanitized and cut for display", func(t *testing.T) {
		long := onePin(t, KindForbidPathChange, "", "src")
		long.Pins[0].Rule.Rule = "bad\x1bname-" + strings.Repeat("é", 100)

		res := onlyResult(t, long, snapshotOf(BoundaryWorkingTree))

		assert.Equal(t, 80, utf8.RuneCountInString(res.Rule))
		assert.True(t, strings.HasPrefix(res.Rule, "badname-é"))
		assert.True(t, strings.HasSuffix(res.Rule, "…"))
	})
}
