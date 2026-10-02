//go:build unix

package tui

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shellAStartRun runs a console session in the background and returns
// the channel of its result. The input ends at once, and Bubble Tea
// keeps the session open after the end of its input. So only a quit
// from outside the keys ends the session.
func shellAStartRun(t *testing.T, out *shellABuffer) <-chan error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- Run(Options{Services: newFake(), In: strings.NewReader(""), Out: out}) }()

	// The first frame proves that the session runs, and that Run
	// registered its signals before the program started.
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), shellAEnterAlt)
	}, 10*time.Second, 10*time.Millisecond, "the session starts")

	return done
}

// shellAWait returns the result of a session, or fails the test when
// the session does not end in time.
func shellAWait(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the console session did not end")

		return nil
	}
}

// Review fix: the agent runs in its own process group, so a closed
// terminal no longer reaches it. The console must turn the hangup into
// its controlled exit, which cancels the work.
func TestShellARunQuitsOnAHangup(t *testing.T) {
	// The test process listens for SIGHUP too. A missing registration
	// then fails the test and does not end the test binary.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	defer signal.Stop(guard)

	var out shellABuffer

	done := shellAStartRun(t, &out)
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))

	require.NoError(t, shellAWait(t, done), "a hangup is a controlled quit, not an error")
	shellARestored(t, out.String())
}
