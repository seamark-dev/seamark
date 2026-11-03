package lessoncheck

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of ProjectOperation. Every repository lives
// under t.TempDir(), so no test reads or writes the checkout that runs
// it.

// projectTodoYAML holds one forbid pin on generated/ and one pattern pin
// on src. Both are check pins: these tests evaluate the snapshot, and the
// guard decision has its own tests.
const projectTodoYAML = `lesson_checks_version: 1
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
    mode: check
    check: frozen-assets
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
`

// claudeOp is a Claude operation with a complete path set.
func claudeOp(tool string, ops ...FileOp) Operation {
	return Operation{Client: "claude", Tool: tool, Ops: ops, PathsComplete: true}
}

// codexOp is a Codex apply_patch operation with a complete path set.
func codexOp(ops ...FileOp) Operation {
	return Operation{Client: "codex", Tool: "apply_patch", Ops: ops, PathsComplete: true}
}

// writeOp is a Claude Write of the full content.
func writeOp(path, content string) FileOp {
	return FileOp{Kind: OpWrite, Path: path, Content: &content}
}

// editOp is a Claude Edit of one replacement.
func editOp(path, old, replacement string, replaceAll bool) FileOp {
	return FileOp{Kind: OpEdit, Path: path, Edits: []TextEdit{{Old: old, New: replacement, ReplaceAll: replaceAll}}}
}

// multiEditOp is a Claude MultiEdit of the edits in order.
func multiEditOp(path string, edits ...TextEdit) FileOp {
	return FileOp{Kind: OpMultiEdit, Path: path, Edits: edits}
}

// addOp is a Codex Add File section.
func addOp(path, content string) FileOp {
	return FileOp{Kind: OpPatchAdd, Path: path, Content: &content}
}

// project projects an operation with the default limits. The result
// must be a valid proposed-edit snapshot.
func (g *gitRepo) project(op Operation) *Snapshot {
	g.t.Helper()

	return g.projectWith(DefaultLimits(), op)
}

// projectWith projects an operation with the given limits.
func (g *gitRepo) projectWith(lim Limits, op Operation) *Snapshot {
	g.t.Helper()

	snap, err := ProjectOperation(context.Background(), g.root, op, lim)
	require.NoError(g.t, err)
	requireValidProjection(g.t, snap, op)

	return snap
}

// requireValidProjection checks the invariants of every proposed-edit
// snapshot: the shared invariants of an operation snapshot with the
// pre-edit baseline without a pending digest, and the no-projection
// limitation of each unprojectable change.
func requireValidProjection(t *testing.T, snap *Snapshot, op Operation) {
	t.Helper()

	requireValidOperationSnapshot(t, snap, BoundaryProposedEdit, Baseline{Kind: BaselinePreEdit}, sortedUnique(operationPaths(op.Ops)))

	for _, ch := range snap.Changes {
		assert.Equal(t, ch.unprojectable != "", slices.Contains(ch.Limitations, LimitationNoProjection),
			"an unprojectable change carries the limitation no-projection: %s", ch.Path)
	}
}

// requireValidOperationSnapshot checks the invariants that the
// proposed-edit and the actual-edit snapshots share: the boundary, the
// baseline, the fixed operation selection, the sorted changes, the valid
// sides, and the digest of the fields.
func requireValidOperationSnapshot(t *testing.T, snap *Snapshot, boundary Boundary, baseline Baseline, paths []string) {
	t.Helper()

	require.Equal(t, SnapshotSchema, snap.Schema)
	require.Equal(t, boundary, snap.Boundary)
	require.Equal(t, baseline, snap.Baseline)
	require.Equal(t, SelectionOperation, snap.Selection.Kind)
	require.Equal(t, "the paths of one native edit", snap.Selection.Description)
	require.Equal(t, []string{"writes outside this operation"}, snap.Selection.Excludes)
	require.Equal(t, paths, snap.Selection.Paths, "the selection holds every path of the operation, sorted")

	require.NotNil(t, snap.Changes)
	require.True(t, slices.IsSortedFunc(snap.Changes, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.OldPath, b.OldPath))
	}), "changes are sorted by path and old path")

	for _, ch := range snap.Changes {
		requireValidSide(t, ch.Path, ch.Before)
		requireValidSide(t, ch.Path, ch.After)
		require.Contains(t, []ChangeKind{ChangeAdd, ChangeDelete, ChangeModify, ChangeMove}, ch.Kind, ch.Path)
	}

	require.Equal(t, snapshotDigest(snap), snap.SHA256)
}

// projectedText returns the projected after content of a change. The
// test fails when the after content is not available text.
func projectedText(t *testing.T, ch Change) string {
	t.Helper()

	require.Equal(t, ContentAvailable, ch.After.Content, "the after content of %s is projected", ch.Path)
	require.Equal(t, sha256Hex(string(ch.After.text)), ch.After.SHA256)

	return string(ch.After.text)
}

// resultOfRule returns the result of the pin with the given rule.
func resultOfRule(t *testing.T, results []Result, rule string) Result {
	t.Helper()

	for _, r := range results {
		if r.Rule == rule {
			return r
		}
	}

	require.Failf(t, "no result", "rule %q", rule)

	return Result{}
}

// todoRepo is a repository with src/a.go and generated/a.txt committed.
func todoRepo(t *testing.T) *gitRepo {
	t.Helper()

	g := newGitRepo(t)
	g.write("src/a.go", "package a\n\nfunc A() {}\n")
	g.write("generated/a.txt", "frozen\n")
	g.commitAll("base")

	return g
}

func TestLessonCheckProjectClaudeEdit(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)

	t.Run("one occurrence is replaced once", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Edit", editOp("src/a.go", "func A() {}", "func A() {} // TODO(x)", false)))

		require.Len(t, snap.Changes, 1)
		ch := snap.Changes[0]

		assert.Equal(t, ChangeModify, ch.Kind)
		assert.Equal(t, "package a\n\nfunc A() {} // TODO(x)\n", projectedText(t, ch))
		assert.Equal(t, sha256Hex("package a\n\nfunc A() {}\n"), ch.Before.SHA256, "the before side comes from disk")
		assert.Equal(t, ContentAvailable, ch.Before.Content)
		assert.True(t, ch.ContentChanged)
		assert.False(t, ch.ModeChanged)
		assert.Equal(t, "100644", ch.After.Mode)
		assert.Empty(t, ch.Limitations)
		assert.True(t, snap.PathsComplete)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeModify, Line: 3, Excerpt: "func A() {} // TODO(x)"}}, res.Evidence)
	})

	t.Run("replace_all replaces every occurrence", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "x x x\n")
		snap := g.project(claudeOp("Edit", editOp("src/a.go", "x", "y", true)))

		assert.Equal(t, "y y y\n", projectedText(t, changeAt(t, snap, "src/a.go")))
	})

	t.Run("replace_all with one occurrence replaces it", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "x\n")
		snap := g.project(claudeOp("Edit", editOp("src/a.go", "x", "y", true)))

		assert.Equal(t, "y\n", projectedText(t, changeAt(t, snap, "src/a.go")))
	})

	rejected := []struct {
		name       string
		content    string
		old        string
		replaceAll bool
		note       string
	}{
		{"zero occurrences", "a\n", "zzz", false, "old_string occurs 0 times"},
		{"zero occurrences with replace_all", "a\n", "zzz", true, "old_string occurs 0 times"},
		{"two occurrences without replace_all", "x x\n", "x", false, "old_string occurs 2 times"},
		{"an empty old_string on an existing file", "a\n", "", false, "old_string is empty"},
		{"an empty old_string with replace_all", "a\n", "", true, "old_string is empty"},
		{"an empty old_string on an existing empty file", "", "", false, "old_string is empty"},
	}

	for _, tc := range rejected {
		t.Run("the native tool rejects "+tc.name+", so it is unprojectable-edit", func(t *testing.T) {
			g := newGitRepo(t)
			g.write("src/a.go", tc.content)
			snap := g.project(claudeOp("Edit", editOp("src/a.go", tc.old, "TODO(y)", tc.replaceAll)))

			ch := changeAt(t, snap, "src/a.go")
			assert.Equal(t, ChangeModify, ch.Kind)
			assert.Equal(t, tc.note, ch.unprojectable)
			assert.Equal(t, []string{LimitationNoProjection}, ch.Limitations)
			assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Content: ContentNotRead}, ch.After, "no partial after content")
			assert.True(t, ch.ContentChanged, "an unknown after content counts as changed")
			assert.True(t, snap.PathsComplete, "the path is known, only the content is not")

			res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
			assert.Equal(t, OutcomeUnknown, res.Outcome)
			assert.Equal(t, ReasonUnprojectableEdit, res.Reason)
		})
	}

	t.Run("an empty old_string on an absent file projects the add of a Write", func(t *testing.T) {
		const created = "package a\n\n// TODO(z)\n"

		for _, replaceAll := range []bool{false, true} {
			g := newGitRepo(t)
			g.write("README.md", "r\n")
			snap := g.project(claudeOp("Edit", editOp("src/new.go", "", created, replaceAll)))

			ch := changeAt(t, snap, "src/new.go")
			assert.Equal(t, ChangeAdd, ch.Kind, "replace_all %t", replaceAll)
			assert.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, ch.Before)
			assert.Equal(t, created, projectedText(t, ch), "the file holds new_string byte for byte")
			assert.Equal(t, "100644", ch.After.Mode)
			assert.Empty(t, ch.unprojectable)
			assert.Empty(t, ch.Limitations)
			assert.True(t, snap.PathsComplete)

			// The guard checks the add as it checks a Write of the same new
			// file, so both projections and both results must be equal.
			written := g.project(claudeOp("Write", writeOp("src/new.go", created)))
			assert.Equal(t, changeAt(t, written, "src/new.go"), ch)

			res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
			assert.Equal(t, OutcomeViolation, res.Outcome)
			assert.Equal(t, []Evidence{{Path: "src/new.go", Change: ChangeAdd, Line: 3, Excerpt: "// TODO(z)"}}, res.Evidence)
			assert.Equal(t, resultOfRule(t, Evaluate(cfg, written), "no-todo"), res)
		}
	})

	t.Run("an absent file with a non-empty old_string stays unprojectable-edit", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("README.md", "r\n")
		snap := g.project(claudeOp("Edit", editOp("src/new.go", "x", "TODO(z)", false)))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, ObjectAbsent, ch.Before.Type)
		assert.Equal(t, "the file does not exist", ch.unprojectable)
		assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Content: ContentNotRead}, ch.After, "no guessed after content")

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonUnprojectableEdit, res.Reason)
	})

	t.Run("forbid-path-change/1 sees every edited path as a change", func(t *testing.T) {
		g := todoRepo(t)

		for _, op := range []FileOp{
			editOp("generated/a.txt", "frozen", "thawed", false),
			editOp("generated/a.txt", "missing", "thawed", false),
			editOp("generated/a.txt", "frozen", "frozen", false),
		} {
			snap := g.project(claudeOp("Edit", op))

			res := resultOfRule(t, Evaluate(cfg, snap), "frozen-assets")
			assert.Equal(t, OutcomeViolation, res.Outcome, "old_string %q", op.Edits[0].Old)
		}
	})

	t.Run("an edit that keeps the content is a change without a content change", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Edit", editOp("src/a.go", "func A() {}", "func A() {}", false)))

		ch := changeAt(t, snap, "src/a.go")
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})

	t.Run("an executable file keeps its mode", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/run.sh", "echo a\n")
		require.NoError(t, os.Chmod(g.path("src/run.sh"), 0o755))

		ch := changeAt(t, g.project(claudeOp("Edit", editOp("src/run.sh", "a", "b", false))), "src/run.sh")
		assert.Equal(t, "100755", ch.Before.Mode)
		assert.Equal(t, "100755", ch.After.Mode)
		assert.False(t, ch.ModeChanged)
	})

	t.Run("binary current content keeps its own reason", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/logo.bin", "\x89PNG\x00TODO(")
		snap := g.project(claudeOp("Edit", editOp("src/logo.bin", "PNG", "TODO(", false)))

		ch := changeAt(t, snap, "src/logo.bin")
		assert.Equal(t, ContentBinary, ch.Before.Content)
		assert.Equal(t, "the current content is not readable text", ch.unprojectable)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonUnsupportedContent, res.Reason, "the before side is read first")
	})

	t.Run("an edit of a directory is unprojectable", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/dir/a.go", "a\n")

		ch := changeAt(t, g.project(claudeOp("Edit", editOp("src/dir", "a", "b", false))), "src/dir")
		assert.Equal(t, ObjectOther, ch.Before.Type)
		assert.Equal(t, "the path is not a regular file", ch.unprojectable)
	})

	t.Run("the JSON output carries no file content", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Edit", editOp("src/a.go", "func A() {}", "func SECRET() {}", false)))

		encoded, err := json.Marshal(snap)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "SECRET")
		assert.NotContains(t, string(encoded), "package a")
		assert.NotContains(t, string(encoded), "pending_sha256", "a projection has no pending record")
	})
}

func TestLessonCheckProjectMultiEditAtomic(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)

	t.Run("each edit applies to the running content", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "alpha\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "alpha", New: "beta"},
			TextEdit{Old: "beta", New: "gamma gamma"},
			TextEdit{Old: "gamma", New: "delta", ReplaceAll: true},
		)))

		assert.Equal(t, "delta delta\n", projectedText(t, changeAt(t, snap, "src/a.go")))
	})

	t.Run("a failing later step makes the whole call unprojectable", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "alpha\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "alpha", New: "TODO(a)"},
			TextEdit{Old: "missing", New: "b"},
		)))

		ch := changeAt(t, snap, "src/a.go")
		assert.Equal(t, "edit 2: old_string occurs 0 times", ch.unprojectable)
		assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Content: ContentNotRead}, ch.After,
			"the first step leaves no partial content, because the native call is atomic")

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonUnprojectableEdit, res.Reason, "never a violation from a partial projection")
	})

	t.Run("a step that the first step made ambiguous fails", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "a b\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "a", New: "b"},
			TextEdit{Old: "b", New: "c"},
		)))

		assert.Equal(t, "edit 2: old_string occurs 2 times", changeAt(t, snap, "src/a.go").unprojectable)
	})

	t.Run("an empty old_string in a later step fails", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "a\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "a", New: "b"},
			TextEdit{Old: "", New: "c"},
		)))

		assert.Equal(t, "edit 2: old_string is empty", changeAt(t, snap, "src/a.go").unprojectable)
	})

	t.Run("a first empty old_string on an absent file creates the content of the later steps", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("README.md", "r\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/new.go",
			TextEdit{Old: "", New: "package a\n\nvar x, y = 1, 1\n"},
			TextEdit{Old: "package a", New: "package b"},
			TextEdit{Old: "1", New: "2", ReplaceAll: true},
		)))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, ObjectAbsent, ch.Before.Type)
		assert.Equal(t, "package b\n\nvar x, y = 2, 2\n", projectedText(t, ch))
		assert.Equal(t, "100644", ch.After.Mode)
		assert.Empty(t, ch.Limitations)
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})

	t.Run("a failing later step after the create step makes the whole call unprojectable", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			later TextEdit
			note  string
		}{
			{"a missing old_string", TextEdit{Old: "missing", New: "b"}, "edit 2: old_string occurs 0 times"},
			{"an empty old_string", TextEdit{Old: "", New: "b"}, "edit 2: old_string is empty"},
		} {
			g := newGitRepo(t)
			g.write("README.md", "r\n")
			snap := g.project(claudeOp("MultiEdit", multiEditOp("src/new.go",
				TextEdit{Old: "", New: "TODO(a)\n"},
				tc.later,
			)))

			ch := changeAt(t, snap, "src/new.go")
			assert.Equal(t, ChangeAdd, ch.Kind, tc.name)
			assert.Equal(t, tc.note, ch.unprojectable, tc.name)
			assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Content: ContentNotRead}, ch.After,
				"%s: the create step leaves no partial content, because the native call is atomic", tc.name)
			assert.Equal(t, ReasonUnprojectableEdit, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Reason, tc.name)
		}
	})

	t.Run("an absent file stays unprojectable unless the first step has an empty old_string", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("README.md", "r\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/new.go",
			TextEdit{Old: "package a", New: "package b"},
			TextEdit{Old: "", New: "package a\n"},
		)))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, "the file does not exist", ch.unprojectable)
		assert.Equal(t, ReasonUnprojectableEdit, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Reason)
	})

	t.Run("a first empty old_string on an existing file fails", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "a\n")
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "", New: "b\n"},
			TextEdit{Old: "a", New: "c"},
		)))

		assert.Equal(t, "edit 1: old_string is empty", changeAt(t, snap, "src/a.go").unprojectable)
	})

	t.Run("a clean multi-edit passes", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("MultiEdit", multiEditOp("src/a.go",
			TextEdit{Old: "package a", New: "package b"},
			TextEdit{Old: "func A() {}", New: "func B() {}"},
		)))

		assert.Equal(t, "package b\n\nfunc B() {}\n", projectedText(t, changeAt(t, snap, "src/a.go")))
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})
}

func TestLessonCheckProjectWriteAddAndModify(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)

	t.Run("a write of a new file is an add of every line", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Write", writeOp("src/new/b.go", "package b\n// TODO(b)\n")))

		require.Len(t, snap.Changes, 1)
		ch := snap.Changes[0]

		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, ch.Before)
		assert.Equal(t, "package b\n// TODO(b)\n", projectedText(t, ch))
		assert.Equal(t, "100644", ch.After.Mode)
		assert.True(t, ch.ContentChanged)
		assert.Equal(t, []string{"src/new/b.go"}, snap.Selection.Paths)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Equal(t, 2, res.Evidence[0].Line)
	})

	t.Run("a write of an existing file is a modify against the disk content", func(t *testing.T) {
		g := todoRepo(t)
		// The worktree differs from HEAD: the before side is the disk.
		g.write("src/a.go", "package a\n// TODO(old)\n")
		snap := g.project(claudeOp("Write", writeOp("src/a.go", "package a\n// TODO(old)\nclean\n")))

		ch := changeAt(t, snap, "src/a.go")
		assert.Equal(t, ChangeModify, ch.Kind)
		assert.Equal(t, sha256Hex("package a\n// TODO(old)\n"), ch.Before.SHA256)
		assert.Empty(t, ch.Before.Oid, "the before side never comes from git")
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome,
			"a line that the disk already holds is not added")
	})

	t.Run("a write of the same content is still a change", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Write", writeOp("generated/a.txt", "frozen\n")))

		ch := changeAt(t, snap, "generated/a.txt")
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, snap), "frozen-assets").Outcome)
	})

	t.Run("a write keeps the executable bit", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/run.sh", "echo a\n")
		require.NoError(t, os.Chmod(g.path("src/run.sh"), 0o755))

		ch := changeAt(t, g.project(claudeOp("Write", writeOp("src/run.sh", "echo b\n"))), "src/run.sh")
		assert.Equal(t, "100755", ch.After.Mode)
		assert.False(t, ch.ModeChanged)
	})

	t.Run("binary written content gives unsupported-content", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(claudeOp("Write", writeOp("src/logo.bin", "\x00TODO(")))

		ch := changeAt(t, snap, "src/logo.bin")
		assert.Equal(t, ContentBinary, ch.After.Content)
		assert.NotEmpty(t, ch.After.SHA256)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, ReasonUnsupportedContent, res.Reason)
	})

	t.Run("a write over a directory is unprojectable", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/dir/a.go", "a\n")

		ch := changeAt(t, g.project(claudeOp("Write", writeOp("src/dir", "x\n"))), "src/dir")
		assert.Equal(t, "the path is not a regular file", ch.unprojectable)
	})

	t.Run("a path below a file reads as absent", func(t *testing.T) {
		g := todoRepo(t)
		ch := changeAt(t, g.project(claudeOp("Write", writeOp("src/a.go/x", "x\n"))), "src/a.go/x")

		assert.Equal(t, ChangeAdd, ch.Kind)
	})

	t.Run("equal operations give equal digests", func(t *testing.T) {
		g := todoRepo(t)
		op := claudeOp("Write", writeOp("src/a.go", "package a\n"))

		first := g.project(op)
		again := g.project(op)
		assert.Equal(t, first.SHA256, again.SHA256)

		other := g.project(claudeOp("Write", writeOp("src/a.go", "package b\n")))
		assert.NotEqual(t, first.SHA256, other.SHA256, "the projected content is part of the digest")
	})
}

func TestLessonCheckProjectPatchOps(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)

	t.Run("an add section creates or replaces a file", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(addOp("src/b.go", "package b"), addOp("src/a.go", "package a")))

		assert.Equal(t, []string{"modify src/a.go", "add src/b.go"}, changeList(snap))
		assert.Equal(t, "package b", projectedText(t, changeAt(t, snap, "src/b.go")))
	})

	t.Run("a delete section removes the file", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchDelete, Path: "generated/a.txt"}))

		ch := changeAt(t, snap, "generated/a.txt")
		assert.Equal(t, ChangeDelete, ch.Kind)
		assert.Equal(t, sha256Hex("frozen\n"), ch.Before.SHA256)
		assert.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, ch.After)
		assert.Empty(t, ch.Limitations, "a deletion is exact")
		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, snap), "frozen-assets").Outcome)
	})

	t.Run("a delete of an absent file stays a deletion", func(t *testing.T) {
		g := todoRepo(t)
		ch := changeAt(t, g.project(codexOp(FileOp{Kind: OpPatchDelete, Path: "src/gone.go"})), "src/gone.go")

		assert.Equal(t, ChangeDelete, ch.Kind)
		assert.Equal(t, ObjectAbsent, ch.Before.Type)
	})

	t.Run("a move without hunks keeps the content", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("docs/a.go", "// TODO(old)\n")
		require.NoError(t, os.Chmod(g.path("docs/a.go"), 0o755))

		snap := g.project(codexOp(FileOp{Kind: OpPatchMove, Path: "docs/a.go", MoveTo: "src/a.go"}))

		require.Len(t, snap.Changes, 1)
		ch := snap.Changes[0]

		assert.Equal(t, ChangeMove, ch.Kind)
		assert.Equal(t, "docs/a.go", ch.OldPath)
		assert.Equal(t, "src/a.go", ch.Path)
		assert.Equal(t, MoveProjection, ch.Move)
		assert.Equal(t, "// TODO(old)\n", projectedText(t, ch))
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, "100644", ch.After.Mode, "the native tool writes a new file without the executable bit")
		assert.True(t, ch.ModeChanged)
		assert.Equal(t, []string{"docs/a.go", "src/a.go"}, snap.Selection.Paths)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeViolation, res.Outcome, "a move into the scope adds every line")
	})

	t.Run("a move onto an existing file is a deletion and a modify", func(t *testing.T) {
		g := todoRepo(t)
		g.write("docs/b.go", "package a\n// TODO(moved)\n")

		snap := g.project(codexOp(FileOp{Kind: OpPatchMove, Path: "docs/b.go", MoveTo: "src/a.go"}))

		assert.Equal(t, []string{"delete docs/b.go", "modify src/a.go"}, changeList(snap))

		target := changeAt(t, snap, "src/a.go")
		assert.Equal(t, sha256Hex("package a\n\nfunc A() {}\n"), target.Before.SHA256, "the before side is the replaced file")
		assert.Equal(t, "package a\n// TODO(moved)\n", projectedText(t, target))

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeViolation, res.Outcome, "the target gains the source lines")
	})

	t.Run("an update with hunks has complete paths and unprojectable content", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchUpdate, Path: "src/a.go", Hunks: true}))

		ch := changeAt(t, snap, "src/a.go")
		assert.Equal(t, ChangeModify, ch.Kind)
		assert.Equal(t, "update hunks have no verified projection", ch.unprojectable)
		assert.True(t, snap.PathsComplete)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonUnprojectableEdit, res.Reason)
	})

	t.Run("an update decides forbid-path-change/1 on paths alone", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchUpdate, Path: "generated/a.txt", Hunks: true}))

		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, snap), "frozen-assets").Outcome)
		assert.Equal(t, OutcomeNotApplicable, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome,
			"an update outside every pattern scope needs no content")
	})

	t.Run("an update with hunks and a move is an unprojectable move", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchUpdate, Path: "src/a.go", MoveTo: "src/b.go", Hunks: true}))

		ch := changeAt(t, snap, "src/b.go")
		assert.Equal(t, ChangeMove, ch.Kind)
		assert.Equal(t, "src/a.go", ch.OldPath)
		assert.Equal(t, "update hunks have no verified projection", ch.unprojectable)
	})

	t.Run("an update of an absent file is an unprojectable add", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchUpdate, Path: "src/gone.go", Hunks: true}))

		ch := changeAt(t, snap, "src/gone.go")
		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, ReasonUnprojectableEdit, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Reason)
	})

	t.Run("a move of an absent source or onto itself is unprojectable", func(t *testing.T) {
		g := todoRepo(t)

		absent := changeAt(t, g.project(codexOp(FileOp{Kind: OpPatchMove, Path: "src/gone.go", MoveTo: "src/b.go"})), "src/b.go")
		assert.Equal(t, "the file does not exist", absent.unprojectable)

		self := changeAt(t, g.project(codexOp(FileOp{Kind: OpPatchMove, Path: "src/a.go", MoveTo: "src/a.go"})), "src/a.go")
		assert.Equal(t, "the move target is the source path", self.unprojectable)
	})

	t.Run("a multi-file patch projects every section", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(
			addOp("src/z.go", "package z\n"),
			FileOp{Kind: OpPatchDelete, Path: "generated/a.txt"},
			FileOp{Kind: OpPatchUpdate, Path: "src/a.go", Hunks: true},
		))

		assert.Equal(t, []string{"delete generated/a.txt", "modify src/a.go", "add src/z.go"}, changeList(snap))
		assert.True(t, snap.PathsComplete)
	})

	t.Run("a path that two sections name is unprojectable", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.project(codexOp(
			addOp("src/b.go", "package b\n"),
			FileOp{Kind: OpPatchMove, Path: "src/a.go", MoveTo: "src/b.go"},
		))

		for _, ch := range snap.Changes {
			if ch.Path == "src/b.go" {
				assert.Equal(t, "the edit names the path more than once", ch.unprojectable)
			}
		}
	})
}

func TestLessonCheckProjectBoundsAndDeadline(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)

	small := DefaultLimits()
	small.MaxFileBytes = 16

	t.Run("a before side above max-file-bytes gives resource-limit", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/big.go", strings.Repeat("a", 17))
		snap := g.projectWith(small, claudeOp("Edit", editOp("src/big.go", "a", "b", true)))

		ch := changeAt(t, snap, "src/big.go")
		assert.Equal(t, ContentTooLarge, ch.Before.Content)
		assert.Equal(t, []LimitHit{{Name: LimitMaxFileBytes, Value: 16, Paths: 1}}, snap.Limits)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxFileBytes, res.Limit)
	})

	t.Run("a written content above max-file-bytes gives resource-limit", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.projectWith(small, claudeOp("Write", writeOp("src/new.go", strings.Repeat("b", 17))))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Size: 17, Content: ContentTooLarge}, ch.After)
		assert.Equal(t, LimitMaxFileBytes, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Limit)
	})

	t.Run("a created content above max-file-bytes gives resource-limit", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.projectWith(small, claudeOp("Edit", editOp("src/new.go", "", strings.Repeat("b", 17), false)))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, ChangeAdd, ch.Kind)
		assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Size: 17, Content: ContentTooLarge}, ch.After)
		assert.Equal(t, []LimitHit{{Name: LimitMaxFileBytes, Value: 16, Paths: 1}}, snap.Limits)
		assert.Equal(t, LimitMaxFileBytes, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Limit)
	})

	t.Run("a create step above max-file-bytes stops before a later step shrinks it", func(t *testing.T) {
		g := todoRepo(t)
		snap := g.projectWith(small, claudeOp("MultiEdit", multiEditOp("src/new.go",
			TextEdit{Old: "", New: strings.Repeat("b", 17)},
			TextEdit{Old: strings.Repeat("b", 17), New: "b"},
		)))

		ch := changeAt(t, snap, "src/new.go")
		assert.Equal(t, Side{Type: ObjectFile, Mode: "100644", Size: 17, Content: ContentTooLarge}, ch.After,
			"the size check runs before each step, as for a replacement")
		assert.Equal(t, LimitMaxFileBytes, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Limit)
	})

	t.Run("replace_all growth stops before the content is built", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("src/a.go", "aaaa")
		snap := g.projectWith(small, claudeOp("Edit", editOp("src/a.go", "a", "bbbbb", true)))

		ch := changeAt(t, snap, "src/a.go")
		assert.Equal(t, ContentTooLarge, ch.After.Content)
		assert.Equal(t, int64(20), ch.After.Size, "the size is the projected size")
		assert.Nil(t, ch.After.text)
		assert.Equal(t, LimitMaxFileBytes, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Limit)
	})

	t.Run("max-snapshot-bytes bounds all content of the snapshot", func(t *testing.T) {
		lim := DefaultLimits()
		lim.MaxSnapshotBytes = 10

		g := todoRepo(t)
		snap := g.projectWith(lim, claudeOp("MultiEdit", multiEditOp("src/a.go", TextEdit{Old: "func A", New: "func B"})))

		ch := changeAt(t, snap, "src/a.go")
		assert.Equal(t, ContentTooLarge, ch.Before.Content)
		assert.Equal(t, LimitMaxSnapshotBytes, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Limit)
	})

	t.Run("max-snapshot-paths cuts the operations and the path set", func(t *testing.T) {
		lim := DefaultLimits()
		lim.MaxSnapshotPaths = 2

		g := todoRepo(t)
		snap := g.projectWith(lim, codexOp(addOp("src/a1.go", "a"), addOp("src/a2.go", "a"), addOp("generated/z.txt", "z")))

		assert.Equal(t, []string{"add src/a1.go", "add src/a2.go"}, changeList(snap))
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitMaxSnapshotPaths, Value: 2, Paths: 1}}, snap.Limits)

		res := resultOfRule(t, Evaluate(cfg, snap), "frozen-assets")
		assert.Equal(t, OutcomeUnknown, res.Outcome, "a cut path never reads as not-applicable")
		assert.Equal(t, LimitMaxSnapshotPaths, res.Limit)
	})

	t.Run("the guard budget gives unknown/resource-limit for every pin", func(t *testing.T) {
		lim := DefaultLimits()
		lim.GuardBudget = time.Nanosecond

		g := todoRepo(t)
		op := claudeOp("Write", writeOp("src/a.go", "clean\n"))
		snap, err := ProjectOperation(context.Background(), g.root, op, lim)
		require.NoError(t, err, "a deadline is a result, not an error")
		requireValidProjection(t, snap, op)

		assert.Empty(t, snap.Changes)
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitGuardBudget, Value: 0}}, snap.Limits)

		for _, res := range Evaluate(cfg, snap) {
			assert.Equal(t, OutcomeUnknown, res.Outcome, res.Rule)
			assert.Equal(t, ReasonResourceLimit, res.Reason, res.Rule)
			assert.Equal(t, LimitGuardBudget, res.Limit, res.Rule)
		}
	})

	t.Run("a deadline of the caller also reads as the guard budget", func(t *testing.T) {
		g := todoRepo(t)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		snap, err := ProjectOperation(ctx, g.root, claudeOp("Write", writeOp("src/a.go", "x")), DefaultLimits())
		require.NoError(t, err)
		assert.Equal(t, LimitGuardBudget, snap.Limits[0].Name)
	})

	t.Run("a canceled context is an error", func(t *testing.T) {
		g := todoRepo(t)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := ProjectOperation(ctx, g.root, claudeOp("Write", writeOp("src/a.go", "x")), DefaultLimits())
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("an operation that no decoder returns is an error", func(t *testing.T) {
		g := todoRepo(t)

		for name, op := range map[string]FileOp{
			"absolute path":      writeOp("/etc/passwd", "x"),
			"parent path":        writeOp("../x", "x"),
			"empty path":         writeOp("", "x"),
			"write, no content":  {Kind: OpWrite, Path: "src/a.go"},
			"add, no content":    {Kind: OpPatchAdd, Path: "src/a.go"},
			"edit, two edits":    {Kind: OpEdit, Path: "src/a.go", Edits: []TextEdit{{Old: "a"}, {Old: "b"}}},
			"multi-edit, none":   {Kind: OpMultiEdit, Path: "src/a.go"},
			"move, no target":    {Kind: OpPatchMove, Path: "src/a.go"},
			"write with a move":  {Kind: OpWrite, Path: "src/a.go", MoveTo: "src/b.go", Content: new(string)},
			"unknown kind":       {Kind: "chmod", Path: "src/a.go"},
			"move to the parent": {Kind: OpPatchMove, Path: "src/a.go", MoveTo: "../b.go"},
		} {
			_, err := ProjectOperation(context.Background(), g.root, codexOp(op), DefaultLimits())
			assert.Error(t, err, name)
		}
	})

	t.Run("a root outside a git work tree is an error", func(t *testing.T) {
		isolateGit(t)

		_, err := ProjectOperation(context.Background(), t.TempDir(), claudeOp("Write", writeOp("a.go", "x")), DefaultLimits())
		require.ErrorContains(t, err, "is not inside a git work tree")
	})

	t.Run("an incomplete operation gives an incomplete path set", func(t *testing.T) {
		g := todoRepo(t)

		op := claudeOp("Write", writeOp("src/a.go", "x"))
		op.PathsComplete = false

		snap := g.project(op)
		assert.False(t, snap.PathsComplete)
		assert.Empty(t, snap.Limits)
	})
}

func TestLessonCheckProjectLinksAsLinks(t *testing.T) {
	skipWithoutSymlinks(t)

	cfg := parseValid(t, projectTodoYAML)

	// linkRepo holds src/link.go, a link to a file outside the root whose
	// content would violate the pattern pin if a read followed the link.
	linkRepo := func(t *testing.T) (*gitRepo, string) {
		t.Helper()

		outside := t.TempDir() + "/outside.go"
		require.NoError(t, os.WriteFile(outside, []byte("// TODO(outside)\n"), 0o644))

		g := todoRepo(t)
		g.symlink(outside, "src/link.go")

		return g, outside
	}

	t.Run("a deleted link is the link itself", func(t *testing.T) {
		g, outside := linkRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchDelete, Path: "src/link.go"}))

		ch := changeAt(t, snap, "src/link.go")
		assert.Equal(t, ChangeDelete, ch.Kind)
		assert.Equal(t, ObjectSymlink, ch.Before.Type)
		assert.Equal(t, "120000", ch.Before.Mode)
		assert.Equal(t, outside, string(ch.Before.text), "the content of a link is its target")
		assert.Equal(t, sha256Hex(outside), ch.Before.SHA256)
		assert.Empty(t, ch.Limitations)
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})

	for _, tc := range []struct {
		tool string
		op   FileOp
	}{
		{"Write", writeOp("src/link.go", "clean\n")},
		{"Edit", editOp("src/link.go", "TODO", "DONE", false)},
		{"MultiEdit", multiEditOp("src/link.go", TextEdit{Old: "TODO", New: "DONE"})},
		{"apply_patch", addOp("src/link.go", "clean\n")},
		{"apply_patch", FileOp{Kind: OpPatchUpdate, Path: "src/link.go", Hunks: true}},
	} {
		t.Run("a "+string(tc.op.Kind)+" through a link is unprojectable and the path set is incomplete", func(t *testing.T) {
			g, _ := linkRepo(t)
			snap := g.project(Operation{Client: "x", Tool: tc.tool, Ops: []FileOp{tc.op}, PathsComplete: true})

			ch := changeAt(t, snap, "src/link.go")
			assert.Equal(t, ObjectSymlink, ch.Before.Type, "Lstat reads the link as a link")
			assert.Equal(t, "the path is a symbolic link", ch.unprojectable)
			assert.False(t, snap.PathsComplete, "the native tool writes the link target, a path outside the snapshot")

			res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
			assert.Equal(t, OutcomeUnknown, res.Outcome)
			assert.NotEqual(t, OutcomeViolation, res.Outcome, "the target content is never read")
		})
	}

	t.Run("a moved link has unprojectable content and complete paths", func(t *testing.T) {
		g, _ := linkRepo(t)
		snap := g.project(codexOp(FileOp{Kind: OpPatchMove, Path: "src/link.go", MoveTo: "src/moved.go"}))

		ch := changeAt(t, snap, "src/moved.go")
		assert.Equal(t, "the path is a symbolic link", ch.unprojectable)
		assert.True(t, snap.PathsComplete, "the move removes the link itself and writes the target path")
	})

	t.Run("a path below a linked directory is never read", func(t *testing.T) {
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(outside+"/a.go", []byte("// TODO(outside)\n"), 0o644))

		g := todoRepo(t)
		g.symlink(outside, "src/dir")

		for _, op := range []FileOp{
			editOp("src/dir/a.go", "TODO", "DONE", false),
			writeOp("src/dir/a.go", "clean\n"),
			{Kind: OpPatchDelete, Path: "src/dir/a.go"},
		} {
			snap := g.project(codexOp(op))

			ch := changeAt(t, snap, "src/dir/a.go")
			assert.Equal(t, Side{Type: ObjectFile, Content: ContentUnreadable}, ch.Before, string(op.Kind))
			assert.Equal(t, []string{LimitationPathThroughLink}, withoutNoProjection(ch.Limitations), string(op.Kind))
			assert.False(t, snap.PathsComplete, string(op.Kind))

			if op.Kind != OpPatchDelete {
				assert.Equal(t, "a parent directory is a symbolic link", ch.unprojectable, string(op.Kind))
			}

			res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
			assert.NotEqual(t, OutcomeViolation, res.Outcome, string(op.Kind))
			assert.NotEqual(t, OutcomePass, res.Outcome, string(op.Kind))
		}
	})

	t.Run("the before digest of a link is the digest of its target string", func(t *testing.T) {
		g, outside := linkRepo(t)
		snap := g.project(claudeOp("Edit", editOp("src/link.go", "x", "y", false)))

		before := changeAt(t, snap, "src/link.go").Before
		assert.Equal(t, sha256Hex(outside), before.SHA256)
		assert.NotEqual(t, sha256Hex("// TODO(outside)\n"), before.SHA256, "the projection never reads the target file")
	})
}

// withoutNoProjection drops no-projection from a limitation list, so a
// test can compare the other limitations.
func withoutNoProjection(list []string) []string {
	var out []string

	for _, l := range list {
		if l != LimitationNoProjection {
			out = append(out, l)
		}
	}

	return out
}

// TestLessonCheckProjectRepositoryDiscoveryDeadline proves that the
// projection deadline bounds repository discovery. The test does not
// assert that the git shim starts. Under CPU load the budget can end
// before the shell runs, and then the projection correctly starts no git.
// Both outcomes end at the deadline, so the assertions hold in both cases
// and do not depend on scheduler timing.
func TestLessonCheckProjectRepositoryDiscoveryDeadline(t *testing.T) {
	const (
		budget   = 300 * time.Millisecond
		shimWait = 30 // seconds; far above any bound the test accepts
		// slack is the longest accepted run time. It is shorter than
		// shimWait, so a wait for the shim always fails the test.
		slack = 10 * time.Second
	)

	g := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "git-started")
	// exec replaces the shell, so cancellation leaves no sleeping child.
	gitShim(t, `if [ "$1" = "rev-parse" ]; then
  : > `+shellQuote(marker)+`
  exec /bin/sleep `+strconv.Itoa(shimWait)+`
fi`)
	lim := DefaultLimits()
	lim.GuardBudget = budget
	start := time.Now()
	snap, err := ProjectOperation(context.Background(), g.root, claudeOp("Write", writeOp("x.txt", "hello\n")), lim)
	elapsed := time.Since(start)
	require.NoError(t, err)

	_, markerErr := os.Stat(marker)
	t.Logf("discovery budget=%s elapsed=%s git-started=%t", budget, elapsed, markerErr == nil)
	require.GreaterOrEqual(t, elapsed, budget, "discovery ends at the deadline, not earlier")
	require.Less(t, elapsed, slack, "discovery does not wait for slow git")
	require.Empty(t, snap.Changes)
	require.False(t, snap.PathsComplete)
	require.Equal(t, []LimitHit{{Name: LimitGuardBudget, Value: budget.Milliseconds()}}, snap.Limits)
}
