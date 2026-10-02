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
)

// Review fix: the console stopped only the direct agent process. A
// wrapper script, or a launcher that forwards only catchable signals,
// then kept its model call running after a stop or an exit.
func TestRunDistillStopEndsTheWholeAgentProcessGroup(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a"))
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")

	// The wrapper starts a descendant, records its ID, and waits for it.
	// The descendant holds the output pipe, so only a group kill ends
	// the call before the pipe wait of the agent package expires.
	script := fmt.Sprintf(`cat >/dev/null; sleep 60 & echo $! > %[1]s.tmp && mv %[1]s.tmp %[1]s; wait`, pidFile)
	distWrite(t, ws, ".seamark/config.yaml", fmt.Sprintf("agent:\n  argv: [\"sh\", \"-c\", %q]\n", script))

	p := distPlan(t, ws, DistillRequest{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		x   *distill.Execution
		err error
	}

	done := make(chan result, 1)

	go func() {
		// No fake: the run builds the invoker from the previewed command.
		x, err := RunDistill(ctx, ws, integration.Builtin(), p, nil, nil)
		done <- result{x: x, err: err}
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
	var res result

	select {
	case res = <-done:
	case <-time.After(4 * time.Second):
		require.FailNow(t, "the run waits for the pipes, so a descendant of the agent still runs")
	}

	assert.Less(t, time.Since(stopped), 4*time.Second)
	require.NoError(t, res.err, "a stop is a typed result, not an error")
	require.NotNil(t, res.x)
	assert.Equal(t, distill.StopCancelled, res.x.Stop)
	assert.Equal(t, distill.GroupCancelled, res.x.Groups[0].Status)

	// The killed descendant is a zombie until init reaps it, and signal 0
	// still finds a zombie. So the check waits for ESRCH.
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(descendant, 0), syscall.ESRCH)
	}, 5*time.Second, 20*time.Millisecond, "the descendant %d outlives the stop", descendant)
}
