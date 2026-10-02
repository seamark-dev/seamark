package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellCSession returns a model with a cancellable root context, as Run
// builds it, and the cancel function of that context.
func shellCSession(t *testing.T) (*model, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := newModel(newFake(), newTheme(false))
	m.st.ctx = ctx

	return m, cancel
}

// shellCWork starts fn as registered stoppable work in a goroutine, as
// Bubble Tea runs a command, and returns when fn has started.
func shellCWork(t *testing.T, m *model, fn func()) {
	t.Helper()

	started := make(chan struct{})
	cmd := m.st.goWork(func() tea.Msg {
		close(started)
		fn()

		return nil
	})

	go cmd()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the worker did not start")
	}
}

func TestShellCExitCancelsAndWaitsForStoppableWork(t *testing.T) {
	m, cancel := shellCSession(t)
	ctx, stop := m.st.runContext()
	defer stop()

	var sawCancel error

	shellCWork(t, m, func() {
		<-ctx.Done()
		// A worker persists its last result before it returns.
		time.Sleep(20 * time.Millisecond)

		sawCancel = ctx.Err()
	})

	assert.True(t, m.stopWork(cancel, time.Now()), "the work ended within the budget")
	assert.ErrorIs(t, sawCancel, context.Canceled, "the root cancel reaches the work")
}

func TestShellCExitWaitIsBounded(t *testing.T) {
	m, cancel := shellCSession(t)
	release := make(chan struct{})

	defer close(release)

	// This worker ignores its context, like a child that does not end.
	shellCWork(t, m, func() { <-release })

	now := time.Now()
	m.exitBy = now.Add(50 * time.Millisecond)

	start := time.Now()
	assert.False(t, m.stopWork(cancel, now), "the stuck work did not end")

	took := time.Since(start)
	assert.GreaterOrEqual(t, took, 40*time.Millisecond, "the console waits for the rest of the budget")
	assert.Less(t, took, 5*time.Second, "the wait stops at the budget")
}

func TestShellCExitBudgetSharesTheDeadline(t *testing.T) {
	m := newModel(newFake(), newTheme(false))
	now := time.Now()

	assert.Equal(t, exitWait, m.exitBudget(now), "a plain exit gets the whole budget")

	m.exitBy = now.Add(3 * time.Second)
	assert.Equal(t, 3*time.Second, m.exitBudget(now), "a stop-and-exit spends one budget")

	m.exitBy = now.Add(-time.Second)
	assert.Zero(t, m.exitBudget(now), "a spent budget waits no more")

	m.exitBy = now.Add(time.Hour)
	assert.Equal(t, exitWait, m.exitBudget(now), "the budget never grows past exitWait")
}

func TestShellCNoWorkStartsAfterTheExitWait(t *testing.T) {
	m, cancel := shellCSession(t)
	require.True(t, m.stopWork(cancel, time.Now()), "no work runs")

	ran := false
	msg := m.st.goWork(func() tea.Msg {
		ran = true

		return toastMsg{}
	})()

	assert.False(t, ran, "a command that starts during the exit does not run its work")
	assert.Nil(t, msg)
}

func TestShellCWorkWithoutRootContextUsesTheBackground(t *testing.T) {
	st := &state{}

	ctx, cancel := st.runContext()
	require.NoError(t, ctx.Err())

	cancel()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, 7, st.goWork(func() tea.Msg { return 7 })(), "the zero state runs registered work")
}
