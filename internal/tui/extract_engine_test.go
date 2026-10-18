package tui

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
)

// TestExtractRecordRunsTheRealEngine drives the record over the real
// engine with a fake invoker. The review plans through the shared
// service and runs no agent; y sends exactly the reviewed question, and
// the stored answer closes the question for the next preview.
func TestExtractRecordRunsTheRealEngine(t *testing.T) {
	ws, marker := servicestest.Workspace(t, "", "api")
	servicestest.Write(t, ws, "api/f21.go", "package api\n")

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	row := domain.Proposal{
		Signature: "sig-ext", Rule: "close-db-handles", Region: "api", Note: "Close the handle.",
		Members: []int64{21, 22}, Status: domain.ProposalProposed,
	}
	require.NoError(t, storetest.InsertProposal(st, &row))
	require.NoError(t, st.Close())

	var prompts []string

	agent := &servicestest.Agent{Fn: func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)

		return fmt.Sprintf(`{"triggers": [{"id": %d, "trigger_paths": ["api/f21.go"]}]}`, row.ID), nil
	}}
	e := screenEngine(ws, services.Deps{Invoke: agent})

	h := newHarness(t, e, 120, 40)
	req := learning.ExtractRequest{IDs: []int64{row.ID}}
	h.send(openExtractMsg{req: req})

	rec := extRecOf(t, h)
	require.Equal(t, stepPreview, rec.step)
	require.True(t, rec.runnable(), "the review is ready: %v", rec.prev.err)
	assert.Contains(t, h.view(), "Sends 1 proposal in 1 batch · 0 ineligible")
	assert.Zero(t, agent.Count(), "the review never invokes the agent")
	assert.NoFileExists(t, marker, "the configured command never runs")

	h.press("y")

	require.Equal(t, stepResult, rec.step)
	assert.Equal(t, 1, agent.Count())
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], fmt.Sprintf("id=%d", row.ID))
	assert.Equal(t, runExtract, h.m.st.lastRunKind)
	assert.False(t, h.m.st.busy())

	flat := extTallFlat(h)
	assert.Contains(t, flat, "Trigger extraction completed")
	assert.Contains(t, flat, "Stored "+propID(row.ID))
	assert.Contains(t, flat, "named for 1 proposal, valid for 1, stored for 1")
	assert.NoFileExists(t, marker)

	// The stored answer closes the question, so a new preview asks
	// nothing and offers no start.
	h.send(openExtractMsg{req: req})

	next := extRecOf(t, h)
	require.NotSame(t, rec, next)
	require.Equal(t, stepPreview, next.step)
	assert.False(t, next.runnable())
	assert.Contains(t, extTallFlat(h), "already answered with trigger paths")
	assert.Equal(t, 1, agent.Count())
}
