package tui

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// evEngineServices runs the evidence services of a real engine and
// scripts every other service. The other reads, for example Doctor,
// could run local tools; this test needs only the learning path.
type evEngineServices struct {
	*fakeServices
	e services.All
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

// screenEngine builds the production engine over ws with the given
// boundaries and the test clock, through the same seam as New. A screen
// test then drives the console over the real engine.
func screenEngine(ws workspace.Workspace, deps services.Deps) services.All {
	if deps.Now == nil {
		deps.Now = func() time.Time { return servicestest.Now }
	}

	return services.NewWithDeps(ws, "test", "/usr/local/bin/seamark", deps)
}

func TestEvidenceRefreshWithTheEngine(t *testing.T) {
	ws, marker := servicestest.Workspace(t, servicestest.Origin)
	fetch := &servicestest.Fetch{Raw: servicestest.Page}
	svc := evEngineServices{fakeServices: newFake(), e: screenEngine(ws, services.Deps{Fetch: fetch.Fetch})}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "e")

	les, ok := h.m.screens[destLessons].(*lessons)
	require.True(t, ok)
	require.Equal(t, lesEvidence, les.sub)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "╭─ Evidence sources ")
	assert.Contains(t, frame, "Refresh reviews + local fixes", "a ready index offers the refresh")

	before := servicestest.Tree(t, ws.Root)

	// The preview names the target that the fetch uses, and esc leaves
	// every file as it was.
	evPress(h, les, evRefreshReviews)
	assert.Contains(t, evPreviewText(t, les), "Repository o/r (remote origin)")

	h.press("esc")
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a preview and its cancel write nothing")
	assert.Empty(t, fetch.Called(), "a preview never fetches")

	evPress(h, les, evRefreshReviews)
	h.press("y")

	assert.Equal(t, []string{"o/r"}, fetch.Called(), "y runs the reviewed fetch once")
	assert.Equal(t, lesRun, les.sub)
	assert.False(t, h.m.st.busy())

	text := evRecordText(les.evid.run)
	assert.Contains(t, text, "✓ Evidence refreshed")
	assert.Contains(t, text, "✓ Local fixes refreshed")
	assert.Contains(t, text, "✓ GitHub reviews refreshed")

	_, err := os.Stat(marker)
	require.ErrorIs(t, err, fs.ErrNotExist, "an evidence refresh never runs the agent")

	h.press("e")
	assert.Contains(t, h.view(), "✓ refreshed just now", "the reloaded snapshot shows the new refresh")
}
