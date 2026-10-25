package integration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardEncodeDenyUsesExitProtocol checks the K8 deny reply of every
// built-in guard: exit 2 with the reasons on stderr, the protocol of
// the command gate (§6 A7). The reply holds no JSON permissionDecision
// on stdout, because no native run exercised that form. Each reason is
// sanitized, because it travels to the agent and the terminal.
func TestGuardEncodeDenyUsesExitProtocol(t *testing.T) {
	builtins := Builtin().Clients()
	require.NotEmpty(t, builtins)

	guarded := 0

	for _, c := range builtins {
		if c.Guards == nil {
			continue
		}

		guarded++

		t.Run(c.ID, func(t *testing.T) {
			reply := c.Guards.EncodeDeny([]string{
				"lesson check frozen-assets (forbid-path-change/1) of pin 3215e7b483d1: Edit would change a.go",
				"lesson check no-todo\x1b[31m (no-added-pattern/1)\n of pin ed5d4af5de3a: adds a line",
				"  ",
			})

			assert.Equal(t, hookBlockExit, reply.ExitCode)
			assert.Equal(t, 2, reply.ExitCode, "both clients read exit 2 as a block")
			assert.Empty(t, reply.Stdout, "no permissionDecision form and no other stdout")
			assert.Equal(t,
				"lesson check frozen-assets (forbid-path-change/1) of pin 3215e7b483d1: Edit would change a.go; "+
					"lesson check no-todo[31m (no-added-pattern/1) of pin ed5d4af5de3a: adds a line",
				string(reply.Stderr), "sanitized reasons joined like the gate's rule messages, blank ones dropped")

			// A deny without reasons still blocks.
			empty := c.Guards.EncodeDeny(nil)
			assert.Equal(t, hookBlockExit, empty.ExitCode)
			assert.Empty(t, empty.Stdout)
			assert.Empty(t, empty.Stderr)

			// The guard and the command gate block in one form.
			assert.Equal(t, exitProtocolGate{}.EncodeFailure(errors.New("x")).ExitCode, reply.ExitCode)
		})
	}

	assert.Equal(t, 2, guarded, "both built-in clients have a guard")
}
