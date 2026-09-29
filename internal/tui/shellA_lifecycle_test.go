package tui

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Terminal control sequences that Bubble Tea writes around a session.
const (
	shellAEnterAlt   = "\x1b[?1049h"
	shellALeaveAlt   = "\x1b[?1049l"
	shellAShowCursor = "\x1b[?25h"
)

// shellABuffer is an output buffer that the renderer goroutine and the
// test goroutine can share without a data race.
type shellABuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *shellABuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *shellABuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// shellAFailingReader returns a read error that is not io.EOF. Bubble
// Tea reports such an error and stops the session.
type shellAFailingReader struct{}

func (shellAFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("terminal input closed unexpectedly")
}

// shellAPanicServices panics in the first-run check. Init runs that
// check as a command, so the panic happens inside a command goroutine.
type shellAPanicServices struct {
	*fakeServices
}

func (shellAPanicServices) Fresh() (bool, error) {
	panic("first-run check failed")
}

// shellARun runs a console session and fails the test when the session
// does not end in time, so a lifecycle regression cannot hang the suite.
func shellARun(t *testing.T, opts Options) error {
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

// shellARestored checks that the output enters the alternate screen,
// then leaves it and shows the cursor again.
func shellARestored(t *testing.T, out string) {
	t.Helper()

	enter := strings.Index(out, shellAEnterAlt)
	require.GreaterOrEqual(t, enter, 0, "the session enters the alternate screen")

	assert.Greater(t, strings.LastIndex(out, shellALeaveAlt), enter, "the session leaves the alternate screen")
	assert.Greater(t, strings.LastIndex(out, shellAShowCursor), enter, "the session shows the cursor again")
}

func TestShellARunRestoresTheTerminalOnQuit(t *testing.T) {
	f := newFake()

	var out shellABuffer

	// A plain reader, not a terminal file, proves that the console reads
	// keys from any io.Reader that the caller passes.
	err := shellARun(t, Options{Services: f, In: strings.NewReader("q"), Out: &out})
	require.NoError(t, err)

	shellARestored(t, out.String())
	assert.Empty(t, f.called(), "opening and quitting the console writes nothing")
}

func TestShellARunRestoresTheTerminalAfterAPanic(t *testing.T) {
	var out shellABuffer

	err := shellARun(t, Options{
		Services: shellAPanicServices{newFake()},
		In:       strings.NewReader(""),
		Out:      &out,
	})
	require.ErrorIs(t, err, tea.ErrProgramPanic)

	shellARestored(t, out.String())
}

func TestShellARunRestoresTheTerminalAfterAnInputError(t *testing.T) {
	var out shellABuffer

	err := shellARun(t, Options{Services: newFake(), In: shellAFailingReader{}, Out: &out})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal input closed unexpectedly")

	shellARestored(t, out.String())
}
