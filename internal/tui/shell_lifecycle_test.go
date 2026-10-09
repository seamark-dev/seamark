package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of the session lifecycle. Run restores the terminal on
// every exit path. The exit cancels the stoppable work and waits for it
// within a bounded time.

// Terminal control sequences that Bubble Tea writes around a session.
const (
	termEnterAlt   = "\x1b[?1049h"
	termLeaveAlt   = "\x1b[?1049l"
	termShowCursor = "\x1b[?25h"
)

// syncBuffer is an output buffer that the renderer goroutine and the
// test goroutine can share without a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// failingReader returns a read error that is not io.EOF. Bubble
// Tea reports such an error and stops the session.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("terminal input closed unexpectedly")
}

// panicServices panics in the first-run check. Init runs that
// check as a command, so the panic happens inside a command goroutine.
type panicServices struct {
	*fakeServices
}

func (panicServices) Fresh() (bool, error) {
	panic("first-run check failed")
}

// runSession runs a console session and fails the test when the session
// does not end in time, so a lifecycle regression cannot hang the suite.
func runSession(t *testing.T, opts Options) error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- Run(opts) }()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the console session did not end")

		return nil
	}
}

// assertRestored checks that the output enters the alternate screen,
// then leaves it and shows the cursor again.
func assertRestored(t *testing.T, out string) {
	t.Helper()

	enter := strings.Index(out, termEnterAlt)
	require.GreaterOrEqual(t, enter, 0, "the session enters the alternate screen")

	assert.Greater(t, strings.LastIndex(out, termLeaveAlt), enter, "the session leaves the alternate screen")
	assert.Greater(t, strings.LastIndex(out, termShowCursor), enter, "the session shows the cursor again")
}

func TestShellRunRestoresTheTerminalOnQuit(t *testing.T) {
	f := newFake()

	var out syncBuffer

	// A plain reader, not a terminal file, proves that the console reads
	// keys from any io.Reader that the caller passes.
	err := runSession(t, Options{Services: f, In: strings.NewReader("q"), Out: &out})
	require.NoError(t, err)

	assertRestored(t, out.String())
	assert.Empty(t, f.called(), "opening and quitting the console writes nothing")
}

func TestShellRunRestoresTheTerminalAfterAPanic(t *testing.T) {
	var out syncBuffer

	err := runSession(t, Options{
		Services: panicServices{newFake()},
		In:       strings.NewReader(""),
		Out:      &out,
	})
	require.ErrorIs(t, err, tea.ErrProgramPanic)

	assertRestored(t, out.String())
}

func TestShellRunRestoresTheTerminalAfterAnInputError(t *testing.T) {
	var out syncBuffer

	err := runSession(t, Options{Services: newFake(), In: failingReader{}, Out: &out})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal input closed unexpectedly")

	assertRestored(t, out.String())
}

// sessionModel returns a model with a cancellable root context, as Run
// builds it, and the cancel function of that context.
func sessionModel(t *testing.T) (*model, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := newModel(newFake(), newTheme(false))
	m.st.ctx = ctx

	return m, cancel
}

// startWork starts fn as registered stoppable work in a goroutine, as
// Bubble Tea runs a command, and returns when fn has started.
func startWork(t *testing.T, m *model, fn func()) {
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

func TestShellExitCancelsAndWaitsForStoppableWork(t *testing.T) {
	m, cancel := sessionModel(t)
	ctx, stop := m.st.runContext()
	defer stop()

	var sawCancel error

	startWork(t, m, func() {
		<-ctx.Done()
		// A worker persists its last result before it returns.
		time.Sleep(20 * time.Millisecond)

		sawCancel = ctx.Err()
	})

	assert.True(t, m.stopWork(cancel, time.Now()), "the work ended within the budget")
	assert.ErrorIs(t, sawCancel, context.Canceled, "the root cancel reaches the work")
}

func TestShellExitWaitIsBounded(t *testing.T) {
	m, cancel := sessionModel(t)
	release := make(chan struct{})

	defer close(release)

	// This worker ignores its context, like a child that does not end.
	startWork(t, m, func() { <-release })

	now := time.Now()
	m.exitBy = now.Add(50 * time.Millisecond)

	start := time.Now()
	assert.False(t, m.stopWork(cancel, now), "the stuck work did not end")

	took := time.Since(start)
	assert.GreaterOrEqual(t, took, 40*time.Millisecond, "the console waits for the rest of the budget")
	assert.Less(t, took, 5*time.Second, "the wait stops at the budget")
}

func TestShellExitBudgetSharesTheDeadline(t *testing.T) {
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

func TestShellNoWorkStartsAfterTheExitWait(t *testing.T) {
	m, cancel := sessionModel(t)
	require.True(t, m.stopWork(cancel, time.Now()), "no work runs")

	ran := false
	msg := m.st.goWork(func() tea.Msg {
		ran = true

		return toastMsg{}
	})()

	assert.False(t, ran, "a command that starts during the exit does not run its work")
	assert.Nil(t, msg)
}

func TestShellWorkWithoutRootContextUsesTheBackground(t *testing.T) {
	st := &state{}

	ctx, cancel := st.runContext()
	require.NoError(t, ctx.Err())

	cancel()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, 7, st.goWork(func() tea.Msg { return 7 })(), "the zero state runs registered work")
}
