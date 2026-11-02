package cli

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// These tests hold the refusals of a chosen row. The CLI has no preview,
// so an inferred scope never replaces a set that a person chose here.

// The fixed text of a refused chosen row, as a script reads it.
const (
	chosenRetargetLine = "a person chose these regions, so a computed scope does not replace them here; " +
		"review it in seamark tui, Lessons › Pins, with w"
	chosenFormRoute = "to edit the regions that a person chose, use seamark tui, Lessons › Pins, with i"
)

// chosenAt is the stamp of a chosen row in these tests.
const chosenAt = 1_790_000_000

// maintainExec runs one statement on the index of root.
func maintainExec(t *testing.T, root, query string, args ...any) {
	t.Helper()

	db, err := sql.Open("sqlite", store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	_, err = db.Exec(query, args...)
	require.NoError(t, err)
}

// maintainChoose records that a person chose the regions of row id.
func maintainChoose(t *testing.T, root string, id int) {
	t.Helper()

	maintainExec(t, root, `UPDATE proposal SET scope_chosen_at = ? WHERE id = ?`, chosenAt, id)
}

func TestLessonsRetargetRefusesAChosenRow(t *testing.T) {
	t.Run("a computed scope that differs is refused", func(t *testing.T) {
		root := retargetGoldenWorkspace(t, retargetGoldenLessons)
		maintainWriteConfig(t, root, maintainGateOn)
		maintainChoose(t, root, 1)

		for _, args := range [][]string{
			{"lessons", "--retarget", "p1"},
			{"lessons", "--retarget", "p1", "--confirm-checks"},
		} {
			stdout, stderr := goldenRun(t, root, args...)
			assert.Equal(t, "  p1    guard-empty-datasets — "+chosenRetargetLine+"\nno pins retargeted\n", stdout)
			assert.Empty(t, stderr)
		}

		content, _ := maintainLessons(t, root)
		assert.Equal(t, retargetGoldenLessons, content)
		assert.Equal(t, retargetGoldenLedger, maintainLedger(t, root, 0))
	})

	t.Run("the other rows of the run continue", func(t *testing.T) {
		root := retargetGoldenWorkspace(t, retargetGoldenLessons)
		maintainWriteConfig(t, root, maintainGateOn)
		maintainChoose(t, root, 1)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1,p5")
		assert.Equal(t, "  p1    guard-empty-datasets — "+chosenRetargetLine+"\n"+
			"  p5    two-areas → api, db\n"+
			"retargeted 1 pin(s) — review the lessons.yaml diff and commit it\n", stdout)
		assert.Empty(t, stderr)
	})

	t.Run("a current row keeps its line and its last line", func(t *testing.T) {
		root := retargetGoldenWorkspace(t, retargetGoldenLessons)
		maintainWriteConfig(t, root, maintainGateOn)
		maintainChoose(t, root, 2)
		maintainChoose(t, root, 3)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p2,p3")
		assert.Equal(t, retargetGoldenPlanLines+"nothing to retarget\n", stdout)
		assert.Empty(t, stderr)
	})
}

// The console reads n/a on w in these states, so the line keeps the reason
// that stops w and names i. Neither gate setting gives a hand change: a
// hand edit of a chosen row would let a computed scope replace the choice.
func TestLessonsRetargetOfAChosenRowNamesTheKeyThatWorks(t *testing.T) {
	const anchor = "  - rule: already-current\n"

	cases := []struct {
		name    string
		lessons string
		mode    os.FileMode
		reason  string
	}{
		{
			name: "another pin owns the computed key",
			lessons: strings.Replace(retargetGoldenLessons, anchor,
				"  - rule: guard-empty-datasets\n    region: scripts\n    note: A hand-written twin.\n"+anchor, 1),
			mode:   0o600,
			reason: "the target key is already in use: another pin already carries the key guard-empty-datasets @ scripts",
		},
		{
			name:    "the file uses CRLF line breaks",
			lessons: strings.ReplaceAll(retargetGoldenLessons, "\n", "\r\n"),
			mode:    0o600,
			reason:  "the pin file cannot be changed: the file uses CRLF line breaks",
		},
		{
			name:    "the file cannot be replaced",
			lessons: retargetGoldenLessons,
			mode:    0o444,
			reason:  "the pin file cannot be changed: the file is read-only (-r--r--r--); seamark does not replace it",
		},
	}

	for _, c := range cases {
		for _, gate := range []string{maintainGateOn, "distill:\n  write: false\n"} {
			t.Run(c.name+" "+gate, func(t *testing.T) {
				root := retargetGoldenWorkspace(t, c.lessons)
				maintainWriteLessons(t, root, c.lessons, c.mode)
				maintainWriteConfig(t, root, gate)
				maintainChoose(t, root, 1)

				stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1")
				assert.Equal(t, "  p1    guard-empty-datasets — "+c.reason+"; "+chosenFormRoute+"\nno pins retargeted\n", stdout)
				assert.Empty(t, stderr)

				content, _ := maintainLessons(t, root)
				assert.Equal(t, c.lessons, content)
				assert.Equal(t, retargetGoldenLedger, maintainLedger(t, root, 0))
			})
		}
	}
}

func TestExtractSummaryNamesTheConsoleForAChosenRow(t *testing.T) {
	const plain = "applied pins with new triggers show their current delivery scopes in " +
		"`lessons --proposals`; `--retarget` applies them"

	assert.Equal(t, plain, appliedTriggersHint(0), "a run with no chosen row keeps its bytes")
	assert.Contains(t, appliedTriggersHint(1), "`seamark tui`, Lessons › Pins")
}
