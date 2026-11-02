package lessoncheck

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of the own-state rule: a default
// verification leaves out the untracked audit files that it writes
// itself. Every repository lives under t.TempDir(), so no test reads or
// writes the checkout that runs it.

// ownStateExclude is the fixed text that names the audit files. The test
// holds the words and not the producer constant, so a change of the
// words fails here.
const ownStateExclude = "seamark's own lesson-check audit files"

// forbidEverythingYAML holds one repository-wide forbid pin. Any change
// that the snapshot holds is a violation, so the pin shows exactly
// which paths the snapshot reads.
const forbidEverythingYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen-everything
    region: "*"
    note: Nothing may change.
    mode: check
    check: frozen
`

// ownStateRepo is a committed repository with the forbid-everything pin
// where only lessons.yaml is ignored. No ignore rule hides the audit
// files, so only the own-state rule can leave them out.
func ownStateRepo(t *testing.T) *gitRepo {
	t.Helper()

	g := newGitRepo(t)
	g.write(".git/info/exclude", ".seamark/lessons.yaml\n")
	g.write("src/a.go", "package a\n")
	g.commitAll("base")
	writeLessons(t, g.root, forbidEverythingYAML)

	return g
}

// verifiedEvidencePaths returns the paths of the evidence of the only result.
func verifiedEvidencePaths(t *testing.T, v *Verification) []string {
	t.Helper()

	require.Len(t, v.Results, 1)

	var paths []string

	for _, e := range v.Results[0].Evidence {
		paths = append(paths, e.Path)
	}

	return paths
}

func TestLessonCheckVerifyLeavesOutOwnAudit(t *testing.T) {
	t.Run("a second default verification equals the first", func(t *testing.T) {
		g := ownStateRepo(t)

		first := g.verify()
		second := g.verify()
		third := g.verify()

		assert.Equal(t, SummaryNoApplicableChecks, first.Summary.State)
		assert.Equal(t, first.Summary.State, second.Summary.State, "the audit record of the first run is no change")
		assert.Equal(t, first.Summary.State, third.Summary.State)
		assert.Equal(t, first.Binding.SnapshotSHA256, second.Binding.SnapshotSHA256)
		assert.Equal(t, first.Changes, second.Changes)
		assert.Empty(t, second.Changes)

		_, err := os.Stat(g.path(".seamark/lesson-check-audit.jsonl"))
		require.NoError(t, err, "the first run wrote its record, so the file is present")
	})

	t.Run("the rotated log and the lock are left out too", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl.1", "{}\n")
		g.write(".seamark/lesson-check-audit.jsonl.lock", "")

		v := g.verify()

		assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
		assert.Empty(t, v.Changes)
	})

	t.Run("an earlier result stays fresh after its own record", func(t *testing.T) {
		g := ownStateRepo(t)

		v := g.verify()
		require.Equal(t, FreshnessFresh, v.Freshness)

		fresh, err := Revalidate(context.Background(), g.root, v)
		require.NoError(t, err)
		assert.Equal(t, FreshnessFresh, fresh, "the write of the record is no input of the result")
	})

	t.Run("only the exact audit paths are left out", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl", "{}\n")
		g.write(".seamark/lesson-check-audit.jsonl.bak", "{}\n")
		g.write(".seamark/lesson-check-audit.jsonl.2", "{}\n")
		g.write(".seamark/other.txt", "x\n")
		g.write("nested/.seamark/lesson-check-audit.jsonl", "{}\n")

		v := g.verify()

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{
			".seamark/lesson-check-audit.jsonl.2",
			".seamark/lesson-check-audit.jsonl.bak",
			".seamark/other.txt",
			"nested/.seamark/lesson-check-audit.jsonl",
		}, verifiedEvidencePaths(t, v))
	})

	t.Run("a tracked audit file still counts", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl", "{}\n")
		g.commitAll("track the audit file")

		// The first run appends a record, so the tracked file is modified.
		g.verify()
		v := g.verify()

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{".seamark/lesson-check-audit.jsonl"}, verifiedEvidencePaths(t, v))
		assert.Equal(t, ChangeModify, v.Results[0].Evidence[0].Change)
	})

	t.Run("a path that the user names still counts", func(t *testing.T) {
		g := ownStateRepo(t)
		g.verify()

		v := g.verify(".seamark/lesson-check-audit.jsonl")

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{".seamark/lesson-check-audit.jsonl"}, verifiedEvidencePaths(t, v))
		assert.Equal(t, ChangeAdd, v.Results[0].Evidence[0].Change)
	})
}

// TestLessonCheckNamesOwnAudit proves that a reader sees the own-state
// exclusion. A selection that can drop the audit files names them on
// every run. A selection that cannot drop them keeps its list. Only the
// words change, so the selected files and the verdict stay the same.
func TestLessonCheckNamesOwnAudit(t *testing.T) {
	t.Run("a default run with the three files names them and reads no change", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl", "{}\n")
		g.write(".seamark/lesson-check-audit.jsonl.1", "{}\n")
		g.write(".seamark/lesson-check-audit.jsonl.lock", "")

		v := g.verify()

		assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
		assert.Empty(t, v.Changes)
		assert.Equal(t, wantDefaultExcludes, v.Selection.Excludes)
		assert.Equal(t, 1, strings.Count(renderText(v), ownStateExclude))
	})

	t.Run("the list and the digest do not depend on the files", func(t *testing.T) {
		g := ownStateRepo(t)

		before := g.snapshot()
		assert.NoFileExists(t, g.path(".seamark/lesson-check-audit.jsonl"))

		g.verify()
		require.FileExists(t, g.path(".seamark/lesson-check-audit.jsonl"))

		after := g.snapshot()

		assert.Equal(t, wantDefaultExcludes, before.Selection.Excludes)
		assert.Equal(t, wantDefaultExcludes, after.Selection.Excludes)
		assert.Equal(t, before.SHA256, after.SHA256)
	})

	t.Run("a tracked audit file keeps its change beside the text", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl", "{}\n")
		g.commitAll("track the audit file")

		// The first run appends a record, so the tracked file is modified.
		g.verify()
		v := g.verify()

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{".seamark/lesson-check-audit.jsonl"}, verifiedEvidencePaths(t, v))
		assert.Equal(t, wantDefaultExcludes, v.Selection.Excludes)
	})

	t.Run("a path that the user names keeps the explicit list", func(t *testing.T) {
		g := ownStateRepo(t)
		g.write(".seamark/lesson-check-audit.jsonl.lock", "")

		v := g.verify(".seamark/lesson-check-audit.jsonl.lock")

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{".seamark/lesson-check-audit.jsonl.lock"}, verifiedEvidencePaths(t, v))
		assert.Equal(t, wantExplicitExcludes, v.Selection.Excludes)
		assert.NotContains(t, renderText(v), ownStateExclude)
	})
}

func TestLessonCheckWithoutUntracked(t *testing.T) {
	drop := func(path string) bool { return path == "drop.txt" }

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nothing to drop keeps the bytes", "? keep.txt\x001 .M N... 100644 100644 100644 a b src/a.go\x00", "? keep.txt\x001 .M N... 100644 100644 100644 a b src/a.go\x00"},
		{"an untracked record of the path goes", "? drop.txt\x00? keep.txt\x00", "? keep.txt\x00"},
		{"a tracked record of the path stays", "1 .M N... 100644 100644 100644 a b drop.txt\x00", "1 .M N... 100644 100644 100644 a b drop.txt\x00"},
		{"an ignored record of the path stays", "! drop.txt\x00", "! drop.txt\x00"},
		{"a rename source that looks like a record stays", "2 R. N... 100644 100644 100644 a b R100 new.txt\x00? drop.txt\x00", "2 R. N... 100644 100644 100644 a b R100 new.txt\x00? drop.txt\x00"},
		{"only the whole path matches", "? drop.txt.bak\x00? sub/drop.txt\x00", "? drop.txt.bak\x00? sub/drop.txt\x00"},
		{"empty output stays empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(withoutUntracked([]byte(tc.in), drop)))
		})
	}
}
