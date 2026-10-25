package lessoncheck

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of the evaluation budget (review finding
// R-1). The guard and the post-edit budget must bound the evaluation and
// the edit projection, and the end of a budget must fail closed. No test
// here depends on a wall-clock limit: a context that has already ended,
// a canceled context, or a counting context stands in for the time.

// repeatedLines is the worst input of the quadratic diff: n equal lines.
func repeatedLines(n int) string {
	return strings.Repeat("}\n", n)
}

// endedBudget returns a context whose deadline has passed.
func endedBudget(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancel)

	return ctx
}

// canceledContext returns a canceled context.
func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

// repeatedLinesSnapshot is a proposed edit that adds one marker to a
// 20000-line file of equal lines. It is the input of the review probe:
// without a budget the diff takes tens of seconds.
func repeatedLinesSnapshot() *Snapshot {
	before := repeatedLines(DefaultLimits().MaxDiffLines)
	after := repeatedLines(DefaultLimits().MaxDiffLines/2-1) + "TODO(x)\n" + repeatedLines(DefaultLimits().MaxDiffLines/2)

	return snapshotOf(BoundaryProposedEdit, modifyChange("src/a.go", fileSide(before), fileSide(after)))
}

// countingContext reports no error for its first free calls of Err and
// DeadlineExceeded after that. It lets a test end a budget at an exact
// step of a loop that checks the context once per step.
type countingContext struct {
	context.Context

	calls atomic.Int64
	free  int64
}

func (c *countingContext) Err() error {
	if c.calls.Add(1) > c.free {
		return context.DeadlineExceeded
	}

	return nil
}

func TestLessonCheckEvaluateWithinBudget(t *testing.T) {
	cfg := parseValid(t, guardYAML)
	snap := snapshotOf(BoundaryProposedEdit,
		modifyChange("src/a.go", fileSide("a\n"), fileSide("a\nTODO(x)\n")),
		modifyChange("docs/a.md", fileSide("a\n"), fileSide("a\nTODO(x)\n")))

	t.Run("a budget that does not end gives the results of Evaluate", func(t *testing.T) {
		want := Evaluate(cfg, snap)
		require.Len(t, want, 3)

		live, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		assert.Equal(t, want, EvaluateWithin(live, LimitGuardBudget, cfg, snap))
		assert.Equal(t, want, EvaluateWithin(nil, LimitGuardBudget, cfg, snap), "a nil context is unbounded") //nolint:staticcheck // the nil context is the documented unbounded value
	})

	t.Run("an ended deadline makes every pin unknown with the named limit", func(t *testing.T) {
		for _, limit := range []string{LimitGuardBudget, LimitPostEditBudget} {
			results := EvaluateWithin(endedBudget(t), limit, cfg, snap)
			require.Len(t, results, 3, "check pins stay in the list, advisory pins stay out")

			for _, r := range results {
				assert.Equal(t, OutcomeUnknown, r.Outcome, r.Rule)
				assert.Equal(t, ReasonResourceLimit, r.Reason, r.Rule)
				assert.Equal(t, limit, r.Limit, r.Rule)
				assert.NotEmpty(t, r.Check.Version, "the result still names its check")
				assert.NotEmpty(t, r.PinRef, "the result still names its pin")
			}
		}
	})

	t.Run("a cancellation makes every pin unknown/interrupted", func(t *testing.T) {
		for _, r := range EvaluateWithin(canceledContext(t), LimitGuardBudget, cfg, snap) {
			assert.Equal(t, OutcomeUnknown, r.Outcome)
			assert.Equal(t, ReasonInterrupted, r.Reason)
			assert.Empty(t, r.Limit)
		}
	})
}

func TestLessonCheckDecideGuardBudgetOnRepeatedLines(t *testing.T) {
	op := claudeOp("Write", writeOp("src/a.go", "unused: the snapshot holds the content\n"))
	snap := repeatedLinesSnapshot()

	t.Run("an ended budget denies an enforce pin with resource-limit", func(t *testing.T) {
		decision, texts := DecideGuard(GuardInput{
			Config: parseValid(t, guardYAML), Operation: op, Snapshot: snap, Context: endedBudget(t),
		})

		assert.Equal(t, GuardDeny, decision.Decision)
		assert.Equal(t, GuardCauseUnknown, decision.Cause, "the cut evaluation never finds the marker, so it is not a violation")
		assert.Equal(t, []Reason{ReasonResourceLimit}, decision.Reasons)
		require.Len(t, decision.Pins, 1)
		assert.Equal(t, OutcomeUnknown, decision.Pins[0].Outcome)
		assert.Equal(t, ReasonResourceLimit, decision.Pins[0].Reason)
		require.Len(t, texts, 1)
		assert.Contains(t, texts[0], "resource-limit")
	})

	t.Run("an ended budget stays advisory for a check pin", func(t *testing.T) {
		cfg := parseValid(t, strings.ReplaceAll(guardYAML, "mode: enforce", "mode: check"))

		decision, texts := DecideGuard(GuardInput{Config: cfg, Operation: op, Snapshot: snap, Context: endedBudget(t)})

		assert.Equal(t, GuardAllow, decision.Decision)
		assert.Empty(t, texts)
	})

	t.Run("a budget that does not end keeps the violation", func(t *testing.T) {
		// The diff of a short repeated file is fast, so the test needs no
		// budget to stay short. It proves that a live context changes no
		// result.
		short := snapshotOf(BoundaryProposedEdit, modifyChange("src/a.go",
			fileSide(repeatedLines(300)), fileSide(repeatedLines(150)+"TODO(x)\n"+repeatedLines(150))))
		live, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		bounded, _ := DecideGuard(GuardInput{Config: parseValid(t, guardYAML), Operation: op, Snapshot: short, Context: live})
		unbounded, _ := DecideGuard(GuardInput{Config: parseValid(t, guardYAML), Operation: op, Snapshot: short})

		assert.Equal(t, GuardCauseViolation, bounded.Cause)
		assert.Equal(t, unbounded, bounded)
	})
}

func TestLessonCheckPostEditBudgetOnRepeatedLines(t *testing.T) {
	cfg := parseValid(t, guardYAML)

	// The record names the path, so no result reads as a missing baseline.
	snap := repeatedLinesSnapshot()
	snap.Selection.Paths = []string{"src/a.go"}

	out := EvaluatePostEdit(PostEditInput{
		Config: cfg, Snapshot: snap, Paths: []string{"src/a.go"}, PathsComplete: true,
		Root: "/work/repo", Tool: "Write", Outcome: ToolSucceeded, Context: endedBudget(t),
	})

	require.Len(t, out.Results, 1, "only the pin whose scope holds the path applies")
	assert.Equal(t, OutcomeUnknown, out.Results[0].Outcome)
	assert.Equal(t, ReasonResourceLimit, out.Results[0].Reason)
	assert.Equal(t, LimitPostEditBudget, out.Results[0].Limit)
	assert.Equal(t, SummaryIncomplete, out.State, "a cut evaluation never reads as a pass")
	assert.NotEmpty(t, out.Findings)
}

func TestLessonCheckDiffLinesWithin(t *testing.T) {
	before := []string{"a", "b", "c"}
	after := []string{"a", "x", "b", "c", "y"}

	t.Run("a live budget gives the lines of diffLines", func(t *testing.T) {
		live, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		got, cause := diffLinesWithin(budget{ctx: live, limit: LimitGuardBudget}, before, after)
		assert.Empty(t, cause.reason)
		assert.Equal(t, diffLines(before, after), got)
		assert.NotEmpty(t, got)

		got, cause = diffLinesWithin(budget{}, before, after)
		assert.Empty(t, cause.reason, "the zero budget is unbounded")
		assert.Equal(t, diffLines(before, after), got)
	})

	t.Run("an ended budget starts no diff", func(t *testing.T) {
		replaceDiff(t, func([]string, []string) []addedLine {
			t.Error("the diff started after the budget ended")

			return nil
		})

		got, cause := diffLinesWithin(budget{ctx: endedBudget(t), limit: LimitGuardBudget}, before, after)
		assert.Nil(t, got)
		assert.Equal(t, reasonHit{reason: ReasonResourceLimit, limit: LimitGuardBudget}, cause)
	})

	t.Run("the end of the budget stops the wait for a running diff", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})

		replaceDiff(t, func([]string, []string) []addedLine {
			close(started)
			<-release

			return nil
		})
		t.Cleanup(func() { close(release) })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			<-started
			cancel()
		}()

		got, cause := diffLinesWithin(budget{ctx: ctx, limit: LimitGuardBudget}, before, after)
		assert.Nil(t, got)
		assert.Equal(t, reasonHit{reason: ReasonInterrupted}, cause)
	})

	t.Run("a panic of the diff reaches the caller", func(t *testing.T) {
		replaceDiff(t, func([]string, []string) []addedLine { panic("diff failed") })

		live, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		assert.PanicsWithValue(t, "diff failed", func() {
			diffLinesWithin(budget{ctx: live, limit: LimitGuardBudget}, before, after)
		})
	})
}

// replaceDiff swaps the diff of diffLinesWithin until the test ends.
func replaceDiff(t *testing.T, fn func([]string, []string) []addedLine) {
	t.Helper()

	old := runDiff
	runDiff = fn

	t.Cleanup(func() { runDiff = old })
}

func TestLessonCheckApplyEditsBudget(t *testing.T) {
	steps := FileOp{Kind: OpMultiEdit, Path: "src/a.go", Edits: []TextEdit{
		{Old: "a", New: "b", ReplaceAll: true}, {Old: "b", New: "c", ReplaceAll: true}, {Old: "c", New: "d", ReplaceAll: true},
	}}

	t.Run("a live context applies every step", func(t *testing.T) {
		after, tooLarge, note, err := applyEdits(context.Background(), "aaa", steps, 1<<20)
		require.NoError(t, err)
		assert.Equal(t, "ddd", after)
		assert.Zero(t, tooLarge)
		assert.Empty(t, note)
	})

	t.Run("an ended context stops before the first step", func(t *testing.T) {
		after, _, note, err := applyEdits(endedBudget(t), "aaa", steps, 1<<20)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Empty(t, after, "a cut edit has no content")
		assert.Empty(t, note, "the end of the budget is not a failed step")
	})

	t.Run("the context ends between two steps", func(t *testing.T) {
		ctx := &countingContext{Context: context.Background(), free: 2}

		_, _, _, err := applyEdits(ctx, "aaa", steps, 1<<20)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.EqualValues(t, 3, ctx.calls.Load(), "the loop checks once per step and stops at the first end")
	})
}

func TestLessonCheckProjectEditBudget(t *testing.T) {
	g := todoRepo(t)
	fop := FileOp{Kind: OpMultiEdit, Path: "src/a.go", Edits: []TextEdit{
		{Old: "package a", New: "package b"}, {Old: "package b", New: "package c"},
	}}

	// Count the context checks that the projection makes before the
	// first edit step, so the test ends the budget exactly there and
	// does not depend on how many checks the file read makes.
	probe := &countingContext{Context: context.Background(), free: 1 << 30}
	p := &projector{r: newTreeReader(probe, g.root, DefaultLimits())}
	p.edit(fop)
	require.NoError(t, p.r.stopped)

	checks := probe.calls.Load()
	require.Greater(t, checks, int64(2), "the read and the two steps each check the context")

	ctx := &countingContext{Context: context.Background(), free: checks - 2}
	p = &projector{r: newTreeReader(ctx, g.root, DefaultLimits())}
	p.edit(fop)

	require.ErrorIs(t, p.r.stopped, context.DeadlineExceeded, "the end inside the edit steps stops the projection")
}
