//go:build !unix

package agent

import "os/exec"

// killGroupOnCancel keeps the default of exec on platforms without
// process groups: cancellation kills the client only. These platforms
// are not supported release targets today.
func killGroupOnCancel(*exec.Cmd) {}
