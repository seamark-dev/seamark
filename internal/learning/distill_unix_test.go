//go:build unix

package learning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// groupAgent configures an agent command whose wrapper starts a
// descendant, records its ID, and waits for it. It returns the file
// that receives the ID. The descendant holds the output pipe, so only a
// group kill ends the call before the pipe wait of the agent package
// expires.
func groupAgent(t *testing.T, ws workspace.Workspace) (pidFile string) {
	t.Helper()

	pidFile = filepath.Join(t.TempDir(), "descendant.pid")
	script := fmt.Sprintf(`cat >/dev/null; sleep 60 & echo $! > %[1]s.tmp && mv %[1]s.tmp %[1]s; wait`, pidFile)
	distWrite(t, ws, ".seamark/config.yaml", fmt.Sprintf("agent:\n  argv: [\"sh\", \"-c\", %q]\n", script))

	return pidFile
}

// stopAgentGroup starts run, waits until the agent of groupAgent starts
// its descendant, and then stops the run. It asserts that the run ends
// within 4 seconds and that the descendant ends too. It returns what run
// returned, so each test checks its own typed outcome.
func stopAgentGroup[T any](t *testing.T, pidFile string, run func(context.Context) (T, error)) (T, error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		got    T
		runErr error
	)

	done := make(chan struct{})

	go func() {
		got, runErr = run(ctx)
		close(done)
	}()

	var descendant int

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}

		descendant, err = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the wrapper starts its descendant")

	// A failed assertion must not leave the descendant running.
	t.Cleanup(func() { _ = syscall.Kill(descendant, syscall.SIGKILL) })

	stopped := time.Now()

	cancel()

	// Without the group kill, the call ends only when the pipe wait of
	// the agent package expires after 5 seconds.
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		require.FailNow(t, "the run waits for the pipes, so a descendant of the agent still runs")
	}

	assert.Less(t, time.Since(stopped), 4*time.Second)

	// The killed descendant is a zombie until init reaps it, and signal 0
	// still finds a zombie. So the check waits for ESRCH.
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(descendant, 0), syscall.ESRCH)
	}, 5*time.Second, 20*time.Millisecond, "the descendant %d outlives the stop", descendant)

	return got, runErr
}

// A stop must end the whole agent process group. A wrapper script, or a
// launcher that forwards only catchable signals, would otherwise keep
// its model call running after a stop or an exit.
func TestRunDistillStopEndsTheWholeAgentProcessGroup(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a"))
	pidFile := groupAgent(t, ws)

	p := distPlan(t, ws, DistillRequest{})

	// No fake: the run builds the invoker from the previewed command.
	x, err := stopAgentGroup(t, pidFile, func(ctx context.Context) (*distill.Execution, error) {
		return RunDistill(ctx, ws, integration.Builtin(), p, nil, nil)
	})

	require.NoError(t, err, "a stop is a typed result, not an error")
	require.NotNil(t, x)
	assert.Equal(t, distill.StopCancelled, x.Stop)
	assert.Equal(t, distill.GroupCancelled, x.Groups[0].Status)
}
