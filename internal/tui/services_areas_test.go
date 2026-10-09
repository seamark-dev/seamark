package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
)

// The tests in this file drive each command worker through the one
// Services area that it takes. The fake goes in as that area only, so
// the tests prove that the worker needs no other area.

// workerMsgs reads ch until the worker closes it. Every worker closes
// its channel after the end message, so the read ends by itself.
func workerMsgs(ch <-chan tea.Msg) []tea.Msg {
	var out []tea.Msg

	for msg := range ch {
		out = append(out, msg)
	}

	return out
}

func TestOvRunBuildTakesTheSetupArea(t *testing.T) {
	f := newFake()
	sum := &index.Summary{}
	f.buildIndex = func(force bool, progress func(IndexEvent)) (*index.Summary, error) {
		assert.True(t, force)
		progress(IndexEvent{Phase: "scan", Done: 1, Total: 2})

		return sum, nil
	}

	var area Setup = f

	ch := make(chan tea.Msg, 4)
	ovRunBuild(area, 7, true, ch)

	msgs := workerMsgs(ch)
	require.Len(t, msgs, 2)
	assert.Equal(t, ovBuildEventMsg{id: 7, ev: IndexEvent{Phase: "scan", Done: 1, Total: 2}}, msgs[0])
	assert.Equal(t, ovBuildDoneMsg{id: 7, summary: sum}, msgs[1])
	assert.Equal(t, []string{"BuildIndex"}, f.called())

	// A panic becomes a failed result, so the mutation slot frees.
	f.buildIndex = func(bool, func(IndexEvent)) (*index.Summary, error) { panic("kaboom") }
	ch = make(chan tea.Msg, 4)
	ovRunBuild(area, 8, false, ch)

	msgs = workerMsgs(ch)
	require.Len(t, msgs, 1, "a panic ends the build with one result")

	done, ok := msgs[0].(ovBuildDoneMsg)
	require.True(t, ok)
	assert.Equal(t, 8, done.id)
	assert.Nil(t, done.summary)
	require.EqualError(t, done.err, "the index build stopped on an internal error: kaboom")
}

func TestEvWorkTakesTheLearningArea(t *testing.T) {
	f := newFake()
	plan := &learning.Plan{Mode: index.SourcesLocal}
	f.runEvidence = func(_ context.Context, p *learning.Plan, progress func(index.RefreshEvent)) (learning.Outcome, error) {
		assert.Same(t, plan, p)
		progress(index.RefreshEvent{Phase: index.PhaseMineFixes})

		return learning.Outcome{}, nil
	}

	var area Learning = f

	ch := make(chan tea.Msg, 4)
	evWork(context.Background(), area, plan, 3, ch)

	msgs := workerMsgs(ch)
	require.Len(t, msgs, 2)
	assert.Equal(t, evEventMsg{id: 3, ev: index.RefreshEvent{Phase: index.PhaseMineFixes}}, msgs[0])

	done, ok := msgs[1].(evDoneMsg)
	require.True(t, ok)
	assert.Equal(t, 3, done.id)
	require.NoError(t, done.err)
	assert.Equal(t, index.SourceNotAttempted, done.out.Fixes.State, "a result without source states reads as not attempted")
	assert.Equal(t, index.SourceNotAttempted, done.out.Reviews.State)
	assert.Equal(t, []string{"RunEvidence"}, f.called())

	// A panic becomes an error result with unknown counts.
	f.runEvidence = func(context.Context, *learning.Plan, func(index.RefreshEvent)) (learning.Outcome, error) {
		panic("kaboom")
	}
	ch = make(chan tea.Msg, 4)
	evWork(context.Background(), area, plan, 4, ch)

	msgs = workerMsgs(ch)
	require.Len(t, msgs, 1, "a panic ends the refresh with one result")

	done, ok = msgs[0].(evDoneMsg)
	require.True(t, ok)
	require.EqualError(t, done.err, "the evidence refresh stopped on an internal error: kaboom")
	assert.Equal(t, done.err, done.out.After.CountsErr, "the counts of an unready index read as unknown")
}

func TestDistRunWorkTakesTheLearningArea(t *testing.T) {
	f := newFake()
	exec := &distill.Execution{}
	p := &learning.DistillPreview{}
	f.runDistill = func(_ context.Context, got *learning.DistillPreview, progress func(distill.Event)) (*distill.Execution, error) {
		assert.Same(t, p, got)
		progress(distill.Event{Kind: distill.EventGroupStarted, Total: 1})

		return exec, nil
	}

	var area Learning = f

	rec := &distRecord{}
	ch := make(chan tea.Msg, 4)
	distRunWork(context.Background(), area, rec, p, ch)

	msgs := workerMsgs(ch)
	require.Len(t, msgs, 2)

	ev, ok := msgs[0].(distEventMsg)
	require.True(t, ok)
	assert.Same(t, rec, ev.rec)
	assert.Equal(t, distill.EventGroupStarted, ev.ev.Kind)

	done, ok := msgs[1].(distDoneMsg)
	require.True(t, ok)
	assert.Same(t, rec, done.rec)
	assert.Same(t, exec, done.exec)
	require.NoError(t, done.err)
	assert.False(t, done.panicked)
	assert.Equal(t, []string{"RunDistill"}, f.called())

	// A panic becomes a failed end that marks the panic.
	f.runDistill = func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
		panic("kaboom")
	}
	ch = make(chan tea.Msg, 4)
	distRunWork(context.Background(), area, rec, p, ch)

	msgs = workerMsgs(ch)
	require.Len(t, msgs, 1, "a panic ends the run with one end message")

	done, ok = msgs[0].(distDoneMsg)
	require.True(t, ok)
	assert.Nil(t, done.exec)
	assert.True(t, done.panicked)
	require.EqualError(t, done.err, "panic: kaboom")
}

func TestExtRunWorkTakesTheLearningArea(t *testing.T) {
	f := newFake()
	exec := &distill.ExtractExecution{}
	p := &learning.ExtractPreview{}
	f.runExtract = func(_ context.Context, got *learning.ExtractPreview,
		progress func(distill.ExtractEvent),
	) (*distill.ExtractExecution, error) {
		assert.Same(t, p, got)
		progress(distill.ExtractEvent{Kind: distill.ExtractBatchStarted, Total: 1})

		return exec, nil
	}

	var area Learning = f

	rec := &extRecord{}
	ch := make(chan tea.Msg, 4)
	extRunWork(context.Background(), area, rec, p, ch)

	msgs := workerMsgs(ch)
	require.Len(t, msgs, 2)

	ev, ok := msgs[0].(extEventMsg)
	require.True(t, ok)
	assert.Same(t, rec, ev.rec)
	assert.Equal(t, distill.ExtractBatchStarted, ev.ev.Kind)

	done, ok := msgs[1].(extDoneMsg)
	require.True(t, ok)
	assert.Same(t, rec, done.rec)
	assert.Same(t, exec, done.exec)
	require.NoError(t, done.err)
	assert.False(t, done.panicked)
	assert.Equal(t, []string{"RunExtract"}, f.called())

	// A panic becomes a failed end that marks the panic.
	f.runExtract = func(context.Context, *learning.ExtractPreview,
		func(distill.ExtractEvent),
	) (*distill.ExtractExecution, error) {
		panic("kaboom")
	}
	ch = make(chan tea.Msg, 4)
	extRunWork(context.Background(), area, rec, p, ch)

	msgs = workerMsgs(ch)
	require.Len(t, msgs, 1, "a panic ends the run with one end message")

	done, ok = msgs[0].(extDoneMsg)
	require.True(t, ok)
	assert.Nil(t, done.exec)
	assert.True(t, done.panicked)
	require.EqualError(t, done.err, "panic: kaboom")
}
