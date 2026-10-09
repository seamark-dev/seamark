package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests of the shared state sections. The shell loads the learning
// and settings sources without registered work. A failed reload keeps
// the last good snapshot and marks it stale.

// loadSections runs the load commands of kinds and stores each result, as
// the shell does when the messages arrive.
func loadSections(t *testing.T, st *state, kinds ...sectionKind) {
	t.Helper()

	queue := []tea.Cmd{st.load(kinds...)}

	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]

		if c == nil {
			continue
		}

		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case loadedMsg:
			require.True(t, st.store(msg))
		}
	}
}

// oneRecordStats is a readable delivery log with one record.
func oneRecordStats() lessonstats.Snapshot {
	firings := []reviews.Firing{{
		TS: "2026-07-01T10:00:00Z", File: "api/a.py",
		Fired: []reviews.FiredLesson{{Region: "api", Symptom: "regenerate the client"}},
	}}

	return lessonstats.Snapshot{
		LogPath: "/work/repo/.seamark/lessons-audit.jsonl", LogExists: true, Records: len(firings),
		Summary: reviews.Summarize(firings, nil),
	}
}

// readyIndex is a ready index with semantic health.
func readyIndex() services.IndexView {
	return services.IndexView{
		Probe:  workspace.IndexProbe{State: workspace.IndexReady, Path: "/work/repo/.seamark/index.db", Schema: 6, Supported: 6},
		Status: &status.Status{Freshness: status.FreshCurrent, Symbols: 42},
	}
}

// TestStatsReloadFailureKeepsTheSnapshot proves finding 15 for the
// stats source: a failed reload keeps the good snapshot and marks it
// stale, and a first failed load shows its own error.
func TestStatsReloadFailureKeepsTheSnapshot(t *testing.T) {
	f := newFake()
	st := &state{svc: f, theme: ui.NewTheme(false)}

	// A first load has nothing to keep: the failure is the value.
	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	loadSections(t, st, secStats)
	assert.False(t, st.stats.stale())
	require.NoError(t, st.stats.err)
	require.Error(t, st.stats.value.LogErr)

	// A good read replaces a failed one.
	f.stats = oneRecordStats()
	loadSections(t, st, secStats)
	assert.False(t, st.stats.stale())
	assert.Equal(t, 1, st.stats.value.Records)

	// A failed reload keeps the good snapshot and marks it stale.
	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	loadSections(t, st, secStats)
	assert.True(t, st.stats.stale())
	require.EqualError(t, st.stats.err, "permission denied")
	assert.Equal(t, 1, st.stats.value.Records, "the good snapshot stays")
	require.NoError(t, st.stats.value.LogErr)

	// The next good read clears the stale mark.
	f.stats = oneRecordStats()
	loadSections(t, st, secStats)
	assert.False(t, st.stats.stale())
}

// TestStatsStaleNoteThroughTheShell proves finding 15 through the
// shell: after r, the Stats view shows the stale note, not the error
// empty state.
func TestStatsStaleNoteThroughTheShell(t *testing.T) {
	f := newFake()
	f.stats = oneRecordStats()

	h := newHarness(t, f, 120, 32)
	h.send(navigateMsg{dest: destLessons, intent: "stats"})

	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	h.press("r")

	frame := h.view()
	assert.Contains(t, frame, "stale: permission denied")
	assert.NotContains(t, frame, "Could not read the delivery log")
	assert.Empty(t, f.called(), "a refresh plans, previews and mutates nothing")
}

// TestIndexReloadFailureKeepsTheSnapshot proves finding 15 for the
// semantic health: a failed reload over a ready index keeps the good
// health and marks it stale; a new probe state replaces it.
func TestIndexReloadFailureKeepsTheSnapshot(t *testing.T) {
	f := newFake()
	st := &state{svc: f, theme: ui.NewTheme(false)}

	failed := readyIndex()
	failed.Status, failed.StatusErr = nil, errors.New("database is locked")

	// A first load has nothing to keep: the failure is the value.
	f.index = failed
	loadSections(t, st, secIndex)
	assert.False(t, st.index.stale())
	require.Error(t, st.index.value.StatusErr)

	f.index = readyIndex()
	loadSections(t, st, secIndex)
	assert.False(t, st.index.stale())

	f.index = failed
	loadSections(t, st, secIndex)
	assert.True(t, st.index.stale())
	require.NotNil(t, st.index.value.Status, "the good semantic health stays")

	// A new probe state is new information, so it replaces the value.
	f.index = services.IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}
	loadSections(t, st, secIndex)
	assert.False(t, st.index.stale())
	assert.Equal(t, workspace.IndexAbsent, st.index.value.Probe.State)
}

// TestIndexStaleNoteThroughTheShell proves that the Overview shows
// the stale note of the semantic health after a failed reload.
func TestIndexStaleNoteThroughTheShell(t *testing.T) {
	f := newFake()
	f.index = readyIndex()

	h := newHarness(t, f, 120, 32)

	failed := readyIndex()
	failed.Status, failed.StatusErr = nil, errors.New("database is locked")
	f.index = failed

	h.press("r")

	assert.True(t, h.m.st.index.stale())
	assert.Contains(t, h.view(), "stale: database is locked")
}

// readyLearning is a readable evidence snapshot of a ready index.
func readyLearning() learning.Snapshot {
	return learning.Snapshot{
		Probe:       workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
		FixFindings: 24, ReviewFindings: 118, Lessons: 12,
		ObservedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
}

func TestShellLoadsLearningAndSettingsWithoutWork(t *testing.T) {
	f := newFake()
	f.learning = readyLearning()
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

func TestSectionLoadingCoversTheNewSources(t *testing.T) {
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

// TestLearningReloadKeepsTheLastGoodCounts proves the stale rule
// of the evidence source: a failed count read over a ready index keeps
// the earlier counts and marks them stale, and a new index state
// replaces them.
func TestLearningReloadKeepsTheLastGoodCounts(t *testing.T) {
	f := newFake()
	f.learning = readyLearning()

	h := newHarness(t, f, 120, 40)
	st := h.m.st
	require.False(t, st.learning.stale())

	broken := readyLearning()
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

func TestSettingsReadErrorKeepsTheLastSnapshot(t *testing.T) {
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
