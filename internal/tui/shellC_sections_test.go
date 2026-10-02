package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// shellCLearning is a readable evidence snapshot of a ready index.
func shellCLearning() learning.Snapshot {
	return learning.Snapshot{
		Probe:       workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
		FixFindings: 24, ReviewFindings: 118, Lessons: 12,
		ObservedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
}

func TestShellCLoadsLearningAndSettingsWithoutWork(t *testing.T) {
	f := newFake()
	f.learning = shellCLearning()
	f.settings = fakeSettingsSnapshot(t, "distill:\n  write: true\n")

	h := newHarness(t, f, 120, 40)
	st := h.m.st

	require.True(t, st.learning.loaded)
	assert.Equal(t, 118, st.learning.value.ReviewFindings)
	require.True(t, st.settings.loaded)
	require.NotNil(t, st.settings.value)

	write, ok := st.settings.value.Field(settings.DistillWrite)
	require.True(t, ok)
	assert.Equal(t, "on", write.Effective)

	assert.Equal(t, 1, f.readCount("Learning"))
	assert.Equal(t, 1, f.readCount("Settings"))
	assert.False(t, st.loading())
	assert.False(t, h.m.latestSnapshot().Before(st.learning.at), "the footer clock covers the new sources")
	assert.False(t, h.m.latestSnapshot().Before(st.settings.at))

	// A view refresh reloads both and still plans or runs nothing.
	h.press("r")
	assert.Equal(t, 2, f.readCount("Learning"))
	assert.Equal(t, 2, f.readCount("Settings"))

	h.press("3", "e", "5")
	assert.Empty(t, f.called(), "loading and browsing never plan, fetch, invoke, or write")

	for _, name := range []string{"PlanEvidence:reviews-and-local", "RunEvidence", "PlanDistill", "RunDistill",
		"CleanupPending", "PlanSettings", "SaveSettings"} {
		assert.Zero(t, f.callCount(name), name)
	}
}

func TestShellCSectionLoadingCoversTheNewSources(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	st := h.m.st

	st.learning.loading = true
	assert.True(t, st.loading(), "a learning load keeps the activity ticker")

	st.learning.loading = false
	st.settings.loading = true
	assert.True(t, st.loading(), "a settings load keeps the activity ticker")

	assert.Contains(t, allSections, secLearning)
	assert.Contains(t, allSections, secSettings)
}

// TestShellCLearningReloadKeepsTheLastGoodCounts proves the stale rule
// of the evidence source: a failed count read over a ready index keeps
// the earlier counts and marks them stale, and a new index state
// replaces them.
func TestShellCLearningReloadKeepsTheLastGoodCounts(t *testing.T) {
	f := newFake()
	f.learning = shellCLearning()

	h := newHarness(t, f, 120, 40)
	st := h.m.st
	require.False(t, st.learning.stale())

	broken := shellCLearning()
	broken.FixFindings, broken.ReviewFindings = 0, 0
	broken.CountsErr = errors.New("database is locked")
	f.learning = broken

	h.press("r")
	assert.True(t, st.learning.stale(), "a failed read over a ready index reads stale")
	assert.Equal(t, 118, st.learning.value.ReviewFindings, "the earlier counts stay, never zero")

	gone := learning.Snapshot{
		Probe:     workspace.IndexProbe{State: workspace.IndexAbsent, Root: "/work/repo"},
		CountsErr: errors.New("no index database"),
	}
	f.learning = gone

	h.press("r")
	assert.False(t, st.learning.stale(), "a new index state is new information")
	assert.Equal(t, workspace.IndexAbsent, st.learning.value.Probe.State)
	require.Error(t, st.learning.value.CountsErr, "the counts read unknown")
}

func TestShellCSettingsReadErrorKeepsTheLastSnapshot(t *testing.T) {
	f := newFake()
	f.settings = fakeSettingsSnapshot(t, "")

	h := newHarness(t, f, 120, 40)
	st := h.m.st
	good := st.settings.value
	require.NotNil(t, good)

	f.settings, f.settingsErr = nil, errors.New("settings: workspace root is empty")

	h.press("r")
	assert.True(t, st.settings.stale())
	assert.Same(t, good, st.settings.value, "the earlier snapshot stays visible")
}
