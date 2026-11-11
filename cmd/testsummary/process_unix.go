//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// terminationSignals are the signals that interrupt a run. The child is
// owned: the runner forwards the request to the child's process group and
// waits, then escalates.
func terminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// configureProcessGroup puts the child in its own process group so the
// terminal's SIGINT reaches only this runner, and so termination can reap
// the descendants the child started without touching another invocation.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, sig os.Signal) {
	if cmd.Process == nil {
		return
	}
	number, ok := sig.(syscall.Signal)
	if !ok {
		number = syscall.SIGTERM
	}
	// Ignore errors: a group that already exited needs no signal, and
	// ESRCH is not a runner failure.
	_ = syscall.Kill(-cmd.Process.Pid, number)
}

func killGroup(cmd *exec.Cmd) {
	signalGroup(cmd, syscall.SIGKILL)
}

// groupAlive reports whether any process remains in the owned group. The
// group id is the leader's pid, and the group exists while it has members.
func groupAlive(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalNumber(sig os.Signal) int {
	if number, ok := sig.(syscall.Signal); ok {
		return int(number)
	}
	return int(syscall.SIGTERM)
}

func signalName(number int) string {
	return syscall.Signal(number).String()
}

func signaledExitCode(err *exec.ExitError) (int, bool) {
	status, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return 128 + int(status.Signal()), true
}
