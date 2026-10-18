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
	cmd.Cancel = func() error { return killGroup(cmd) }
}

// killGroup sends SIGKILL to the process group of a started client. With
// Setpgid, the group ID is the client's process ID, and a negative ID
// signals every process in the group. The group ID stays valid while a
// member lives, also after the client exits.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		// The group is gone: exec then reports the exit, not an error.
		return os.ErrProcessDone
	}

	return err
}
