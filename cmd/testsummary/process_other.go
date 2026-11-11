//go:build !unix

package main

import (
	"os"
	"os/exec"
)

// Non-Unix platforms have no process groups to own. The runner still
// reports interruption as a failure, never as a pass.

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func configureProcessGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig os.Signal) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killGroup(cmd *exec.Cmd) {
	signalGroup(cmd, os.Interrupt)
}

func groupAlive(cmd *exec.Cmd) bool {
	return false
}

func signalNumber(sig os.Signal) int {
	return 1
}

func signalName(number int) string {
	return "interrupt"
}

func signaledExitCode(err *exec.ExitError) (int, bool) {
	return 0, false
}
