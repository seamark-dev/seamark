package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lesSubTabs returns the sub-view line of the Lessons frame.
func lesSubTabs(h *harness) string {
	return strings.TrimRight(strings.Split(h.view(), "\n")[4], " ")
}

func TestLessonsEvidenceSubView(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	h.press("e")
	require.Equal(t, lesEvidence, les.sub)
	assert.True(t, strings.HasPrefix(lesSubTabs(h), " Proposals   Pins   Stats   Evidence   p / m / s / e "), lesSubTabs(h))

	lines := strings.Split(h.view(), "\n")
	assert.True(t, strings.HasPrefix(lines[7], "╭─ Evidence sources "), "the box starts on the row of the other boxes")
	assert.Contains(t, h.view(), "seamark index --fixes-only")
	assert.Contains(t, footerLine(h.view()), "p proposals   m pins   s stats")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	h.press("s")
	assert.Equal(t, lesStats, les.sub)

	h.press("e", "p")
	assert.Equal(t, lesProposals, les.sub)

	h.send(navigateMsg{dest: destLessons, intent: "evidence"})
	assert.Equal(t, lesEvidence, les.sub)
	assert.Empty(t, f.called(), "the Evidence view reads and writes nothing")
}

func TestLessonsRunSubViewStaysHiddenUntilARun(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	h.press("u")
	assert.Equal(t, lesProposals, les.sub, "u does nothing before the first run")
	assert.NotContains(t, lesSubTabs(h), "Run")

	h.send(navigateMsg{dest: destLessons, intent: "run"})
	assert.Equal(t, lesProposals, les.sub, "the run intent keeps the view before the first run")

	h.m.st.lastRunKind = runEvidence
	assert.True(t, strings.HasPrefix(lesSubTabs(h), " Proposals   Pins   Stats   Evidence   Run   p / m / s / e / u "), lesSubTabs(h))

	h.press("u")
	require.Equal(t, lesRun, les.sub)
	assert.Contains(t, h.view(), "╭─ Run ")
	assert.False(t, les.capturing(), "a run view never captures keys, so navigation stays free")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	// At the minimum width the key hint drops before the write gate.
	assert.True(t, strings.HasSuffix(lesSubTabs(h), "distill.write on"), lesSubTabs(h))
	assert.NotContains(t, lesSubTabs(h), "p / m")

	h.press("2")
	h.send(navigateMsg{dest: destLessons, intent: "run"})
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesRun, les.sub)
	assert.Empty(t, f.called())
}

func TestLessonsDistillIntentOpensTheRunSubView(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	h.send(navigateMsg{dest: destLessons, intent: "distill"})
	require.Equal(t, lesRun, les.sub, "the scope step lives in the Run sub-view")
	assert.Contains(t, lesSubTabs(h), "Run   p / m / s / e / u")
	assert.Contains(t, h.view(), "Distillation preparation")
}

func TestLessonsProposalsRunIntent(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	h.press("s")
	h.send(navigateMsg{dest: destLessons, intent: "proposals:run"})
	assert.Equal(t, lesProposals, les.sub)
	assert.Nil(t, les.props.run, "no run result means no run filter")

	at := time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)
	require.Nil(t, les.props.showRun([]int64{2, 3}, "run 14:05", at))
	require.NotNil(t, les.props.run)
	assert.Equal(t, map[int64]bool{2: true, 3: true}, les.props.run.ids)
	assert.Equal(t, "run 14:05", les.props.run.label)
	assert.Equal(t, at, les.props.run.at)
}

func TestLessonsSwitchKeysWaitWhileTheViewCaptures(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	h.m.st.lastRunKind = runDistill

	h.press("/")
	h.typeText("eu")
	assert.Equal(t, lesProposals, les.sub, "the region input receives e and u")
	assert.Equal(t, "eu", les.props.input.Value())
	assert.NotContains(t, lesSubTabs(h), "p / m", "the switch hint hides while the view captures")
}
