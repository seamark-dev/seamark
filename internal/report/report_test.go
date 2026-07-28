package report

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// seedStore builds a minimal index: one symbol in scripts/task.py plus a
// set of review lessons, enough to exercise lesson surfacing.
func seedStore(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	var err error
	st, err = store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	sym := model.Symbol{
		FQN: "scripts/task.run", Name: "run", Kind: model.KindFunction,
		File: "scripts/task.py", Span: model.Span{StartLine: 1, EndLine: 5},
	}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertSymbol(&sym)
	}))

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "scripts\x00RUF001", Region: "scripts", Reviewer: "coderabbit",
			Symptom: "RUF001", Occurrences: 6, LastTS: 100},
		{ClusterKey: "scripts\x00once", Region: "scripts", Reviewer: "human",
			Symptom: "solitary finding", Occurrences: 1, LastTS: 50},
	}, nil))

	return st, root
}

func TestWhySurfacesRegionLessons(t *testing.T) {
	st, root := seedStore(t)

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "scripts/task.py"))

	out := b.String()
	assert.Contains(t, out, "reviewers keep flagging")
	assert.Contains(t, out, "RUF001")
	assert.Contains(t, out, "×6")
	assert.NotContains(t, out, "solitary finding", "a single comment is below the recurrence threshold")
	assert.Contains(t, out, "expand lessons:scripts", "the raw-ledger ref is advertised")
}

func TestOrientSurfacesTopLessons(t *testing.T) {
	st, root := seedStore(t)

	var b strings.Builder
	require.NoError(t, Orient(&b, st, root))

	out := b.String()
	assert.Contains(t, out, "review lessons")          // the index summary line
	assert.Contains(t, out, "reviewers keep flagging") // the ranked section
	assert.Contains(t, out, "RUF001")
}

func TestPinnedNoteSurvivesUntruncated(t *testing.T) {
	// A pin's note IS the guidance: the template promises it is "shown to
	// the agent verbatim", and on a real repo a hard 34-char cut reduced
	// a curated 10-finding pin to "Adding a fie…" — the agent never saw
	// the instruction. Lesson text must reach every surface whole.
	note := "Adding a field to a pooled struct? Reset it in Free() and " +
		"deep-copy it in clone(). Reviewers have flagged this ten times."
	lessons := []model.Lesson{
		{Region: "engine/resolve", Reviewer: "pinned",
			Symptom: "pooled-state-reset — " + note, Occurrences: 1 << 30},
	}

	var b strings.Builder
	require.NoError(t, PrintLessonReminder(&b, "engine/resolve/context.go", lessons, 0))

	assert.Contains(t, b.String(), note, "the full note reaches the agent")
	assert.NotContains(t, b.String(), "…", "no ellipsis truncation on lesson text")
}

func TestPinBudgetAboveSurfaceCapStaysAccounted(t *testing.T) {
	// pin_budget: 20 must not let the surface's total cap (8) eat pins
	// silently — the excess has to land in the trimmed count so the
	// pointer line still tells the truth.
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	cfg := reviews.DefaultConfig()
	cfg.PinBudget = 20

	for i := 0; i < 10; i++ {
		cfg.Pin = append(cfg.Pin, reviews.PinRule{
			Rule: fmt.Sprintf("pin-%02d", i), Region: "*", Note: "n"})
	}

	out, trimmed, err := LessonsForScopeBudget(st, cfg, "a/x.go", 8, cfg.HookPinBudget())
	require.NoError(t, err)
	assert.Len(t, out, 8)
	assert.Equal(t, 2, trimmed, "pins beyond the surface cap are counted, not silently dropped")
}

func TestReminderPointsAtBudgetedPins(t *testing.T) {
	lessons := []model.Lesson{
		{Region: "a", Reviewer: "pinned", Symptom: "r — most specific wins", Occurrences: 1 << 30},
	}

	var b strings.Builder
	require.NoError(t, PrintLessonReminder(&b, "a/x.go", lessons, 4))

	assert.Contains(t, b.String(), "+4 more pins for this area",
		"budgeted pins are pointed at, never silently dropped")
	assert.Contains(t, b.String(), "seamark lessons --file a/x.go")
}

func TestLessonSymptomSanitized(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Symptom text originates in untrusted comment bodies; a control
	// byte must not survive into rendered output.
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "k", Region: "x", Reviewer: "bot",
			Symptom: "bad\x1b[31mansi", Occurrences: 3, LastTS: 1},
	}, nil))

	var b strings.Builder
	require.NoError(t, Orient(&b, st, root))

	assert.NotContains(t, b.String(), "\x1b", "escape sequences must be washed out")
}
