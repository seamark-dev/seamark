package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// writePins writes one pin per region into .seamark/lessons.yaml, each
// with its own rule.
func writePins(t *testing.T, root string, regions ...string) {
	t.Helper()

	var b strings.Builder

	b.WriteString("pin:\n")

	for i, region := range regions {
		fmt.Fprintf(&b, "  - rule: guard-%02d\n    region: %q\n    note: Pin %d.\n", i, region, i)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(b.String()), 0o644))
}

// minedLessons returns n mined lessons in the scripts area that pass
// the default threshold, with the highest count first.
func minedLessons(n int) []model.Lesson {
	out := make([]model.Lesson, n)

	for i := range out {
		symptom := fmt.Sprintf("finding-%03d", i)
		out[i] = model.Lesson{
			ClusterKey: "scripts\x00" + symptom, Region: "scripts", Reviewer: "coderabbit",
			Symptom: symptom, Occurrences: 3 + n - i, LastTS: int64(100 + i),
		}
	}

	return out
}

// lessonSection returns the lesson section of a report: the header line
// and every line after it up to the next blank line.
func lessonSection(t *testing.T, out, header string) []string {
	t.Helper()

	_, rest, ok := strings.Cut(out, "\n"+header+"\n")
	require.True(t, ok, "the report has the header %q:\n%s", header, out)

	section, _, _ := strings.Cut(rest, "\n\n")

	return strings.Split(strings.TrimSuffix(section, "\n"), "\n")
}

// TestWhyCountsTheLessonsItLeavesOut uses the repro shape: 7 pins (the
// file, its directory twice, and four repository-wide ones) and 2 mined
// lessons apply to one file.
func TestWhyCountsTheLessonsItLeavesOut(t *testing.T) {
	st, root := seedStore(t)
	require.NoError(t, st.ReplaceLessons(minedLessons(2), nil))
	writePins(t, root, "scripts/task.py", "scripts", "scripts", "*", "*", "*", "*")

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	all, err := LessonsForScope(st, cfg, "scripts/task.py", 0)
	require.NoError(t, err)
	require.Len(t, all, 9)

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "scripts/task.py"))

	// The 6 lesson lines keep their bytes: the cap is a prefix cut of the
	// full list. The count line names the full view of the file by an
	// absolute path, so it runs from any directory.
	var shown strings.Builder
	printLessons(&shown, all[:6])

	want := strings.Split(strings.TrimSuffix(shown.String(), "\n"), "\n")
	want = append(want, "  +3 more: `seamark lessons --file "+filepath.Join(root, "scripts", "task.py")+"` shows this file's full view")

	lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
	require.Len(t, lines, 8)
	assert.Equal(t, want, lines[:7])
}

func TestWhyCountLineScrubsTheRoot(t *testing.T) {
	// The count line names an absolute path, and the repository root can
	// hold a control byte. The terminal must not get it raw.
	st, _ := seedStore(t)
	require.NoError(t, st.ReplaceLessons(minedLessons(2), nil))

	root := filepath.Join(t.TempDir(), "esc") + "\x1b[31mroot"
	writePins(t, root, "scripts/task.py", "scripts", "scripts", "*", "*", "*", "*")

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "scripts/task.py"))

	assert.Contains(t, b.String(), "\n  +3 more: `seamark lessons --file ")
	assert.NotContains(t, b.String(), "\x1b")
}

func TestWhyPrintsNoCountLineForSixLessons(t *testing.T) {
	st, root := seedStore(t)
	require.NoError(t, st.ReplaceLessons(minedLessons(2), nil))
	writePins(t, root, "scripts", "scripts", "*", "*")

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "scripts/task.py"))

	assert.Contains(t, b.String(), "guard-03")
	assert.NotContains(t, b.String(), "more:")
}

func TestInspectCommandQuotesAnAbsolutePath(t *testing.T) {
	assert.Equal(t, "seamark lessons --file /work/api/x.go", InspectCommand("/work", "api/x.go"))
	assert.Equal(t, `seamark lessons --file '/work dir/a b.go'`, InspectCommand("/work dir", "a b.go"))
	assert.Equal(t, `seamark lessons --file '/work/it'\''s.go'`, InspectCommand("/work", "it's.go"))

	// A relative root resolves against the working directory, so another
	// directory still names the same file.
	wd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, "seamark lessons --file "+filepath.Join(wd, "repo", "x.go"), InspectCommand("repo", "x.go"))
}

func TestOrientCountsHiddenLessonsPerKind(t *testing.T) {
	t.Run("the repro shape lists pins only and counts both kinds", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons(minedLessons(2), nil))
		writePins(t, root, "scripts/task.py", "scripts", "scripts", "*", "*", "*", "*")

		var b strings.Builder
		require.NoError(t, Orient(&b, st, root))

		lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
		require.Len(t, lines, 7)
		assert.Equal(t, "  +3 more: 1 pin (.seamark/lessons.yaml lists every pin), "+
			"2 mined lessons (`seamark lessons --list`)", lines[6])
		assert.NotContains(t, b.String(), "reviewers keep flagging")
	})

	t.Run("six pins print no count line", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons(nil, nil))
		writePins(t, root, "a", "b", "c", "d", "e", "f")

		var b strings.Builder
		require.NoError(t, Orient(&b, st, root))

		lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
		assert.Len(t, lines, 6)
		assert.NotContains(t, b.String(), "more")
	})

	t.Run("three pins and five mined lessons", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons(minedLessons(5), nil))
		writePins(t, root, "a", "b", "c")

		var b strings.Builder
		require.NoError(t, Orient(&b, st, root))

		lines := lessonSection(t, b.String(),
			"pinned lessons, then what reviewers keep flagging  (pins from .seamark/lessons.yaml first)")
		require.Len(t, lines, 7)
		assert.Equal(t, "  +2 more mined lessons: `seamark lessons --list` shows every mined lesson", lines[6])
	})

	t.Run("a full read says or more", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons(minedLessons(orientMinedRead+1), nil))

		var b strings.Builder
		require.NoError(t, Orient(&b, st, root))

		lines := lessonSection(t, b.String(), "reviewers keep flagging  (recurring feedback — avoid the repeat)")
		require.Len(t, lines, 7)
		assert.Equal(t, "  +194 or more mined lessons: `seamark lessons --list` shows every mined lesson", lines[6])
	})
}

func TestOrientMoreLineTakesTheSingularNoun(t *testing.T) {
	assert.Equal(t, "+1 more pin: .seamark/lessons.yaml lists every pin", orientMoreLine(1, 0, false))
	assert.Equal(t, "+1 more mined lesson: `seamark lessons --list` shows every mined lesson", orientMoreLine(0, 1, false))
	assert.Equal(t, "+2 more: 1 pin (.seamark/lessons.yaml lists every pin), 1 mined lesson (`seamark lessons --list`)",
		orientMoreLine(1, 1, false))
	assert.Equal(t, "+3 or more: 2 pins (.seamark/lessons.yaml lists every pin), 1 or more mined lessons (`seamark lessons --list`)",
		orientMoreLine(2, 1, true))
	assert.Empty(t, orientMoreLine(0, 0, true))
}

func TestWhyNamesTheRawViewOnlyWhenItHasLessons(t *testing.T) {
	const pointer = "  raw findings incl. one-offs: expand lessons:scripts (MCP) or `seamark lessons --region scripts`"

	t.Run("pins only and an empty ledger", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons(nil, nil))
		writePins(t, root, "scripts")

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "scripts/task.py"))

		lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
		assert.Len(t, lines, 1)
		assert.NotContains(t, b.String(), "raw findings")
	})

	t.Run("pins and a one-off below the threshold", func(t *testing.T) {
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons([]model.Lesson{{
			ClusterKey: "scripts\x00once", Region: "scripts", Reviewer: "person",
			Symptom: "solitary finding", Occurrences: 1, LastTS: 50,
		}}, nil))
		writePins(t, root, "scripts")

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "scripts/task.py"))

		lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
		assert.Equal(t, pointer, lines[len(lines)-1], "the view holds the one-off")
	})

	t.Run("pins and a lesson in a subdirectory", func(t *testing.T) {
		// The view of the directory lists the lessons of its
		// subdirectories too. A view of the file alone would not.
		st, root := seedStore(t)
		require.NoError(t, st.ReplaceLessons([]model.Lesson{{
			ClusterKey: "scripts/sub\x00once", Region: "scripts/sub", Reviewer: "person",
			Symptom: "nested finding", Occurrences: 1, LastTS: 50,
		}}, nil))
		writePins(t, root, "scripts")

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "scripts/task.py"))

		lines := lessonSection(t, b.String(), "pinned lessons  (from .seamark/lessons.yaml)")
		assert.Equal(t, pointer, lines[len(lines)-1], "the view holds the nested lesson")
	})
}

func TestWhyHeaderNamesWhatItLists(t *testing.T) {
	t.Run("mined only", func(t *testing.T) {
		st, root := seedStore(t)

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "scripts/task.py"))

		assert.Contains(t, b.String(), "\nreviewers keep flagging  (recurring feedback — avoid the repeat)\n")
		assert.NotContains(t, b.String(), "across pull requests")
	})

	t.Run("pins and mined lessons", func(t *testing.T) {
		st, root := seedStore(t)
		writePins(t, root, "scripts")

		var b strings.Builder
		require.NoError(t, Why(&b, st, root, "scripts/task.py"))

		lines := lessonSection(t, b.String(),
			"pinned lessons, then what reviewers keep flagging  (pins from .seamark/lessons.yaml first)")
		assert.Len(t, lines, 3, "the pin, the mined lesson, and the pointer")
		assert.NotContains(t, b.String(), "across pull requests")
	})
}
