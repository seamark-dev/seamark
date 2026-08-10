package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// runJudgeCommand runs a bounded hidden check. A normal non-zero exit or
// timeout is a failed verdict; inability to start or supervise the command is
// benchmark infrastructure failure.
func runJudgeCommand(dir, name string, args ...string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cacheRoot, err := os.MkdirTemp("", "seamark-bench-judge-cache-")
	if err != nil {
		return false, fmt.Errorf("create benchmark judge cache: %w", err)
	}
	defer func() { _ = os.RemoveAll(cacheRoot) }()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(
		agentEnvironment(dir),
		// Python timestamp bytecode can stay valid after a same-size source
		// rewrite within one second. A fresh per-command cache directory,
		// together with disabled bytecode writes, makes hidden judges read the
		// current source instead of a stale fixture __pycache__.
		"PYTHONPYCACHEPREFIX="+filepath.Join(cacheRoot, "python"),
		"PYTHONDONTWRITEBYTECODE=1",
	)

	cmd.WaitDelay = processWaitDelay

	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return false, nil
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}

		return false, fmt.Errorf("run benchmark judge: %w\n%s", err, out)
	}

	return true, nil
}
