//go:build unix

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// killGroupOnCancel makes the client the leader of a new process group.
// On cancellation it sends SIGKILL to that whole group. A launcher that
// forwards only catchable signals, or a wrapper script, then cannot
// leave a descendant running.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// With Setpgid, the group ID is the client's process ID. A
		// negative ID sends the signal to every process in the group.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The group is gone: exec then reports the exit, not an error.
			return os.ErrProcessDone
		}

		return err
	}
}
