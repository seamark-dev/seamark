package lessoncheck

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forbidExampleJSON is the expected JSON result of the specYAML pin on
// one modified file. Its digests are the fixed digests of specYAML. The
// test compares the whole result, because scripts read the JSON form.
const forbidExampleJSON = `{
  "pin_ref": "3215e7b483d1",
  "pin_identity": "3215e7b483d134443a8204970767f1a0057d4726f235e946b6c0c27abd8cd055",
  "pin_version": "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e",
  "entry": 1,
  "rule": "frozen-assets",
  "mode": "enforce",
  "scope": ["generated"],
  "check": {"id": "frozen-assets", "kind": "forbid-path-change/1", "version": "9ceb9cddb5053035c0d9779ffab326503647b5295cb3f43791d5fe1e9be9cf7a"},
  "outcome": "violation",
  "evidence": [{"path": "generated/a.txt", "change": "modify"}],
  "coverage": {"paths_in_scope": 1, "paths_evaluated": 1}
}`

func TestLessonCheckForbidPathChange(t *testing.T) {
	spec := parseValid(t, specYAML)

	t.Run("the full JSON result of a modified file", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, modifyChange("generated/a.txt", fileSide("a\n"), fileSide("b\n")))

		encoded, err := json.Marshal(onlyResult(t, spec, snap))
		require.NoError(t, err)
		assert.JSONEq(t, forbidExampleJSON, string(encoded))
	})

	text := fileSide("a\n")
	exec := fileSide("a\n")
	exec.Mode = "100755"

	cases := []struct {
		name     string
		change   Change
		evidence Evidence
		inScope  int
	}{
		{"add", addChange("generated/new.txt", fileSide("n\n")), Evidence{Path: "generated/new.txt", Change: ChangeAdd}, 1},
		{"content change", modifyChange("generated/a.txt", text, fileSide("b\n")), Evidence{Path: "generated/a.txt", Change: ChangeModify}, 1},
		{"deletion", deleteChange("generated/a.txt", text), Evidence{Path: "generated/a.txt", Change: ChangeDelete}, 1},
		{"mode change", modifyChange("generated/a.txt", text, exec), Evidence{Path: "generated/a.txt", Change: ChangeModify}, 1},
		{"type change", modifyChange("generated/a.txt", text, linkSide("a\n")), Evidence{Path: "generated/a.txt", Change: ChangeModify}, 1},
		{"binary content", modifyChange("generated/logo.bin", fileSide("\x00a"), fileSide("\x00b")), Evidence{Path: "generated/logo.bin", Change: ChangeModify}, 1},
		{"gitlink", addChange("generated/sub", Side{Type: ObjectGitlink, Mode: "160000", Content: ContentNotRead}), Evidence{Path: "generated/sub", Change: ChangeAdd}, 1},
		{"move out of scope", moveChange("generated/a.txt", "docs/a.txt", text, text), Evidence{Path: "docs/a.txt", OldPath: "generated/a.txt", Change: ChangeMove}, 1},
		{"move into scope", moveChange("docs/a.txt", "generated/a.txt", text, text), Evidence{Path: "generated/a.txt", OldPath: "docs/a.txt", Change: ChangeMove}, 1},
		{"move inside scope", moveChange("generated/a.txt", "generated/b.txt", text, text), Evidence{Path: "generated/b.txt", OldPath: "generated/a.txt", Change: ChangeMove}, 2},
		{"the region path itself", addChange("generated", fileSide("g\n")), Evidence{Path: "generated", Change: ChangeAdd}, 1},
	}

	for _, tc := range cases {
		t.Run("violation: "+tc.name, func(t *testing.T) {
			res := onlyResult(t, spec, snapshotOf(BoundaryWorkingTree, tc.change))

			assert.Equal(t, OutcomeViolation, res.Outcome)
			assert.Empty(t, res.Reason)
			assert.Equal(t, []Evidence{tc.evidence}, res.Evidence)
			assert.Empty(t, res.UnknownPaths)
			assert.Equal(t, Coverage{PathsInScope: tc.inScope, PathsEvaluated: tc.inScope}, res.Coverage)
		})
	}

	t.Run("not-applicable: changes outside the scope", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree,
			addChange("generated-other/a.txt", text),
			addChange("generatedx", text),
			modifyChange("src/generated/a.txt", text, fileSide("b\n")),
			moveChange("docs/a.txt", "docs/b.txt", text, text),
		)

		res := onlyResult(t, spec, snap)

		assert.Equal(t, OutcomeNotApplicable, res.Outcome, "a region is a path prefix, never a name prefix")
		assert.Empty(t, res.Evidence)
		assert.Equal(t, Coverage{}, res.Coverage)
	})

	t.Run("not-applicable: an unchanged path named by --file", func(t *testing.T) {
		// A staged change that the worktree reverts is dropped, so the
		// explicit path has no change.
		snap := snapshotOf(BoundaryWorkingTree)
		snap.Selection = Selection{Kind: SelectionExplicitPaths, Paths: []string{"generated/a.txt"}, Excludes: []string{}}

		res := onlyResult(t, spec, snap)

		assert.Equal(t, OutcomeNotApplicable, res.Outcome, "an unchanged explicit path is never a pass")
	})

	t.Run("unknown: an incomplete path set without a change in scope", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, addChange("docs/a.txt", text))
		snap.PathsComplete = false

		res := onlyResult(t, spec, snap)

		assert.Equal(t, OutcomeUnknown, res.Outcome, "a path set that is not complete proves nothing")
		assert.Equal(t, ReasonIncompletePaths, res.Reason)
		assert.Empty(t, res.Limit)
	})

	// A --file path below a link is a change without an observed
	// difference. Its reason joins the other causes, and reasonPrecedence
	// picks one.
	t.Run("unknown: an unobserved change takes its place in the reason precedence", func(t *testing.T) {
		linked := addChange("generated/x.txt", Side{Type: ObjectFile, Content: ContentUnreadable})
		linked.Limitations = []string{LimitationPathThroughLink}
		linked.unobserved = true

		snap := snapshotOf(BoundaryWorkingTree, linked)
		res := onlyResult(t, spec, snap)

		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonContentUnavailable, res.Reason)
		assert.Equal(t, []Evidence{{Path: "generated/x.txt", Change: ChangeAdd, Reason: ReasonContentUnavailable}}, res.UnknownPaths)
		assert.Equal(t, Coverage{PathsInScope: 1}, res.Coverage)

		snap.PathsComplete = false
		res = onlyResult(t, spec, snap)
		assert.Equal(t, ReasonIncompletePaths, res.Reason, "incomplete-paths comes before content-unavailable")

		snap.Limits = []LimitHit{{Name: LimitMaxSnapshotPaths, Value: 1, Paths: 1}}
		res = onlyResult(t, spec, snap)
		assert.Equal(t, ReasonContentUnavailable, res.Reason, "content-unavailable comes before resource-limit")
		assert.Empty(t, res.Limit)
	})

	t.Run("violation: an incomplete path set with a change in scope", func(t *testing.T) {
		snap := snapshotOf(BoundaryWorkingTree, addChange("generated/a.txt", text))
		snap.PathsComplete = false

		res := onlyResult(t, spec, snap)

		assert.Equal(t, OutcomeViolation, res.Outcome)
	})

	t.Run("each boundary uses the same predicate", func(t *testing.T) {
		for _, boundary := range []Boundary{BoundaryProposedEdit, BoundaryActualEdit, BoundaryWorkingTree} {
			res := onlyResult(t, spec, snapshotOf(boundary, modifyChange("generated/a.txt", text, fileSide("b\n"))))
			assert.Equal(t, OutcomeViolation, res.Outcome, boundary)
		}
	})

	t.Run("several regions and a repository-wide scope", func(t *testing.T) {
		several := onePin(t, KindForbidPathChange, "", "assets", "generated", "vendor/lib")
		snap := snapshotOf(BoundaryWorkingTree, deleteChange("vendor/lib/x.go", text), addChange("vendor/other.go", text))

		res := onlyResult(t, several, snap)

		assert.Equal(t, []string{"assets", "generated", "vendor/lib"}, res.Scope)
		assert.Equal(t, OutcomeViolation, res.Outcome)
		assert.Equal(t, []Evidence{{Path: "vendor/lib/x.go", Change: ChangeDelete}}, res.Evidence)

		repoWide := onePin(t, KindForbidPathChange, "", "")
		res = onlyResult(t, repoWide, snapshotOf(BoundaryWorkingTree, addChange("README.md", text)))

		assert.Equal(t, []string{}, res.Scope)
		assert.Equal(t, OutcomeViolation, res.Outcome, "a repository-wide pin covers every path")

		res = onlyResult(t, repoWide, snapshotOf(BoundaryWorkingTree))
		assert.Equal(t, OutcomeNotApplicable, res.Outcome)
	})
}
