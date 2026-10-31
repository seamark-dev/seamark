//go:build unix

package learning

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
)

// A stop must end the whole agent process group of an extraction, as it
// does for a distillation. A wrapper script, or a launcher that forwards
// only catchable signals, would otherwise keep its model call running.
func TestRunExtractStopEndsTheWholeAgentProcessGroup(t *testing.T) {
	ws, _ := extWorkspace(t)
	rows := extSeed(t, ws, extPending("open", "api", 1))
	pidFile := groupAgent(t, ws)

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Empty(t, p.Blockers)

	// No fake: the run builds the invoker from the previewed command.
	x, err := stopAgentGroup(t, pidFile, func(ctx context.Context) (*distill.ExtractExecution, error) {
		return RunExtract(ctx, ws, integration.Builtin(), p, nil, nil)
	})

	require.NoError(t, err, "a stop is a typed result, not an error")
	require.NotNil(t, x)
	assert.Equal(t, distill.StopCancelled, x.Stop)
	assert.Equal(t, distill.BatchCancelled, x.Batches[0].Status, "a stopped call is cancelled, never failed")
	assert.Equal(t, extIDs(rows...), x.Cancelled)
	assert.False(t, extStamped(t, ws, rows[0].ID), "a cancelled item is not stamped")
}
