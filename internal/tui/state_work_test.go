package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of the registered work helpers: beginWork for a short write
// that cannot stop, and startRun for stoppable work of a run kind.

// workMsg is the result message of a test worker.
type workMsg struct{ n int }

func TestBeginWorkClaimsTheSlotAndRegistersTheWork(t *testing.T) {
	st := &state{}
	ran := 0

	cmd, ok := st.beginWork("Removing 1 pin", func() tea.Msg {
		ran++

		return workMsg{n: ran}
	})
	require.True(t, ok)
	require.NotNil(t, cmd)
	assert.Equal(t, "Removing 1 pin", st.mutation)
	assert.Nil(t, st.run, "a short write is not stoppable")
	assert.Zero(t, ran, "the work runs in the command, never in Update")

	again, ok := st.beginWork("Retargeting 1 pin", func() tea.Msg { return nil })
	assert.False(t, ok, "the slot holds one mutation")
	assert.Nil(t, again)
	assert.Equal(t, "Removing 1 pin", st.mutation)

	assert.Equal(t, workMsg{n: 1}, cmd())
	assert.True(t, st.work.wait(time.Second), "the finished work left the worker set")
}

func TestBeginWorkMakesTheExitWaitForTheWrite(t *testing.T) {
	st := &state{}
	started, release := make(chan struct{}), make(chan struct{})

	cmd, ok := st.beginWork("Removing 1 pin", func() tea.Msg {
		close(started)
		<-release

		return workMsg{n: 1}
	})
	require.True(t, ok)

	done := make(chan tea.Msg, 1)

	go func() { done <- cmd() }()

	// fn runs only after the command registered it, so the exit wait
	// sees the running write.
	<-started
	assert.False(t, st.work.wait(20*time.Millisecond), "the exit waits for a running write")

	close(release)
	assert.Equal(t, workMsg{n: 1}, <-done)

	// After the exit wait began, no new write starts.
	late, ok := (&state{work: workerSet{closed: true}}).beginWork("Removing 1 pin", func() tea.Msg {
		t.Fatal("no work may start during an exit")

		return nil
	})
	require.True(t, ok)
	assert.Nil(t, late())
}

func TestStartRunRecordsTheKindAndStopsThroughTheNote(t *testing.T) {
	st := &state{}

	var got context.Context

	cmd, ok := st.startRun(runExtract, extractRunName, runNote{verb: extractRunVerb, total: 3},
		func(ctx context.Context) tea.Msg {
			got = ctx

			return workMsg{n: 2}
		})
	require.True(t, ok)
	require.NotNil(t, cmd)
	assert.Equal(t, runExtract, st.lastRunKind)
	assert.Equal(t, extractRunName, st.mutation)
	require.True(t, st.stoppable(), "the note carries the stop of the run context")
	assert.False(t, st.run.started.IsZero())

	require.True(t, st.requestStop())
	assert.Equal(t, workMsg{n: 2}, cmd())
	require.Error(t, got.Err(), "the stop cancelled the context of the work")

	// A second run is refused, and the refused run keeps the first note.
	other, ok := st.startRun(runDistill, "Distilling proposals", runNote{verb: "Distilling"},
		func(context.Context) tea.Msg {
			t.Fatal("a refused run never starts")

			return nil
		})
	assert.False(t, ok)
	assert.Nil(t, other)
	assert.Equal(t, runExtract, st.lastRunKind, "a refused run keeps the kind of the running one")
}

func TestStartRunEndsTheContextWithTheWork(t *testing.T) {
	st := &state{}

	var got context.Context

	cmd, ok := st.startRun(runExtract, extractRunName, runNote{verb: extractRunVerb},
		func(ctx context.Context) tea.Msg {
			got = ctx
			assert.NoError(t, ctx.Err(), "the context is live while the work runs")

			return nil
		})
	require.True(t, ok)

	cmd()
	require.ErrorIs(t, got.Err(), context.Canceled, "the context ends when the work returns")
}

func TestExtractionRunNamesItselfInTheShell(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)

	_, ok := h.m.st.startRun(runExtract, extractRunName, runNote{
		verb: extractRunVerb, detail: "3 proposals", done: 1, total: 2,
		home: navigateMsg{dest: destLessons, intent: "run"},
	}, func(context.Context) tea.Msg { return nil })
	require.True(t, ok)

	it := ovFind(t, ovScreen(h), "learn:run")
	assert.Equal(t, "Trigger extraction running · 1 of 2 done", it.title)

	notice := strings.Split(h.view(), "\n")[3]
	assert.Contains(t, notice, "Extracting triggers · 1 of 2 done · 3 proposals")
	assert.Contains(t, notice, "Lessons › Run")

	h.press("q")
	assert.Contains(t, lesFlat(h.view()), "Stop and exit?")
	assert.Contains(t, lesFlat(h.view()), "Extracting trigger information is running.")

	h.press("esc")
	h.send(mutationDoneMsg{})
	assert.True(t, ovlLessons(h).runShown(), "Lessons offers the Run sub-view after an extraction")
}
