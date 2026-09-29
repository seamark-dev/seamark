package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// shellBLoad runs the load commands of kinds and stores each result, as
// the shell does when the messages arrive.
func shellBLoad(t *testing.T, st *state, kinds ...sectionKind) {
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

// shellBStats is a readable delivery log with one record.
func shellBStats() lessonstats.Snapshot {
	firings := []reviews.Firing{{
		TS: "2026-07-01T10:00:00Z", File: "api/a.py",
		Fired: []reviews.FiredLesson{{Region: "api", Symptom: "regenerate the client"}},
	}}

	return lessonstats.Snapshot{
		LogPath: "/work/repo/.seamark/lessons-audit.jsonl", LogExists: true, Records: len(firings),
		Summary: reviews.Summarize(firings, nil),
	}
}

// shellBReadyIndex is a ready index with semantic health.
func shellBReadyIndex() IndexView {
	return IndexView{
		Probe:  workspace.IndexProbe{State: workspace.IndexReady, Path: "/work/repo/.seamark/index.db", Schema: 6, Supported: 6},
		Status: &status.Status{Freshness: status.FreshCurrent, Symbols: 42},
	}
}

// TestShellBStatsReloadFailureKeepsTheSnapshot proves finding 15 for the
// stats source: a failed reload keeps the good snapshot and marks it
// stale, and a first failed load shows its own error.
func TestShellBStatsReloadFailureKeepsTheSnapshot(t *testing.T) {
	f := newFake()
	st := &state{svc: f, theme: newTheme(false)}

	// A first load has nothing to keep: the failure is the value.
	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	shellBLoad(t, st, secStats)
	assert.False(t, st.stats.stale())
	require.NoError(t, st.stats.err)
	require.Error(t, st.stats.value.LogErr)

	// A good read replaces a failed one.
	f.stats = shellBStats()
	shellBLoad(t, st, secStats)
	assert.False(t, st.stats.stale())
	assert.Equal(t, 1, st.stats.value.Records)

	// A failed reload keeps the good snapshot and marks it stale.
	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	shellBLoad(t, st, secStats)
	assert.True(t, st.stats.stale())
	require.EqualError(t, st.stats.err, "permission denied")
	assert.Equal(t, 1, st.stats.value.Records, "the good snapshot stays")
	require.NoError(t, st.stats.value.LogErr)

	// The next good read clears the stale mark.
	f.stats = shellBStats()
	shellBLoad(t, st, secStats)
	assert.False(t, st.stats.stale())
}

// TestShellBStatsStaleNoteThroughTheShell proves finding 15 through the
// shell: after r, the Stats view shows the stale note, not the error
// empty state.
func TestShellBStatsStaleNoteThroughTheShell(t *testing.T) {
	f := newFake()
	f.stats = shellBStats()

	h := newHarness(t, f, 120, 32)
	h.send(navigateMsg{dest: destLessons, intent: "stats"})

	f.stats = lessonstats.Snapshot{LogErr: errors.New("permission denied")}
	h.press("r")

	frame := h.view()
	assert.Contains(t, frame, "stale: permission denied")
	assert.NotContains(t, frame, "Could not read the delivery log")
	assert.Empty(t, f.called(), "a refresh plans, previews and mutates nothing")
}

// TestShellBIndexReloadFailureKeepsTheSnapshot proves finding 15 for the
// semantic health: a failed reload over a ready index keeps the good
// health and marks it stale; a new probe state replaces it.
func TestShellBIndexReloadFailureKeepsTheSnapshot(t *testing.T) {
	f := newFake()
	st := &state{svc: f, theme: newTheme(false)}

	failed := shellBReadyIndex()
	failed.Status, failed.StatusErr = nil, errors.New("database is locked")

	// A first load has nothing to keep: the failure is the value.
	f.index = failed
	shellBLoad(t, st, secIndex)
	assert.False(t, st.index.stale())
	require.Error(t, st.index.value.StatusErr)

	f.index = shellBReadyIndex()
	shellBLoad(t, st, secIndex)
	assert.False(t, st.index.stale())

	f.index = failed
	shellBLoad(t, st, secIndex)
	assert.True(t, st.index.stale())
	require.NotNil(t, st.index.value.Status, "the good semantic health stays")

	// A new probe state is new information, so it replaces the value.
	f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}
	shellBLoad(t, st, secIndex)
	assert.False(t, st.index.stale())
	assert.Equal(t, workspace.IndexAbsent, st.index.value.Probe.State)
}

// TestShellBIndexStaleNoteThroughTheShell proves that the Overview shows
// the stale note of the semantic health after a failed reload.
func TestShellBIndexStaleNoteThroughTheShell(t *testing.T) {
	f := newFake()
	f.index = shellBReadyIndex()

	h := newHarness(t, f, 120, 32)

	failed := shellBReadyIndex()
	failed.Status, failed.StatusErr = nil, errors.New("database is locked")
	f.index = failed

	h.press("r")

	assert.True(t, h.m.st.index.stale())
	assert.Contains(t, h.view(), "stale: database is locked")
}
