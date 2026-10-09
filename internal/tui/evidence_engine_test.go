package tui

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
)

// evEngineServices runs the evidence services of a real engine and
// scripts every other service. The other reads, for example Doctor,
// could run local tools; this test needs only the learning path.
type evEngineServices struct {
	*fakeServices
	e *engine
}

// Learning reads the evidence snapshot of the engine.
func (s evEngineServices) Learning() learning.Snapshot {
	s.count("Learning")

	return s.e.Learning()
}

// PlanEvidence records the plan call and plans with the engine.
func (s evEngineServices) PlanEvidence(mode index.SourceMode) (*learning.Plan, error) {
	s.record("PlanEvidence:" + mode.String())

	return s.e.PlanEvidence(mode)
}

// RunEvidence records the run call and runs the plan with the engine.
func (s evEngineServices) RunEvidence(ctx context.Context, p *learning.Plan,
	progress func(index.RefreshEvent),
) (learning.Outcome, error) {
	s.record("RunEvidence")

	return s.e.RunEvidence(ctx, p, progress)
}

func TestEvidenceRefreshWithTheEngine(t *testing.T) {
	ws, marker := engWorkspace(t, engOrigin)
	fetch := &engFetch{raw: engPage}
	svc := evEngineServices{fakeServices: newFake(), e: engNew(ws, engineDeps{fetch: fetch.fetch})}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "e")

	les, ok := h.m.screens[destLessons].(*lessons)
	require.True(t, ok)
	require.Equal(t, lesEvidence, les.sub)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "╭─ Evidence sources ")
	assert.Contains(t, frame, "Refresh reviews + local fixes", "a ready index offers the refresh")

	before := engTree(t, ws.Root)

	// The preview names the target that the fetch uses, and esc leaves
	// every file as it was.
	evPress(h, les, evRefreshReviews)
	assert.Contains(t, evPreviewText(t, les), "Repository o/r (remote origin)")

	h.press("esc")
	assert.Equal(t, before, engTree(t, ws.Root), "a preview and its cancel write nothing")
	assert.Empty(t, fetch.called(), "a preview never fetches")

	evPress(h, les, evRefreshReviews)
	h.press("y")

	assert.Equal(t, []string{"o/r"}, fetch.called(), "y runs the reviewed fetch once")
	assert.Equal(t, lesRun, les.sub)
	assert.False(t, h.m.st.busy())
	engUnlocked(t, svc.e)

	text := evRecordText(les.evid.run)
	assert.Contains(t, text, "✓ Evidence refreshed")
	assert.Contains(t, text, "✓ Local fixes refreshed")
	assert.Contains(t, text, "✓ GitHub reviews refreshed")

	_, err := os.Stat(marker)
	require.ErrorIs(t, err, fs.ErrNotExist, "an evidence refresh never runs the agent")

	h.press("e")
	assert.Contains(t, h.view(), "✓ refreshed just now", "the reloaded snapshot shows the new refresh")
}
