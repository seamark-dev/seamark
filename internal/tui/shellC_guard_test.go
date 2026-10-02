package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellCGuardScreen is a stub destination with a leave guard. While
// hold is true, it refuses every departure, records the target, and
// shows a message, like a screen that asks about an unsaved draft.
type shellCGuardScreen struct {
	shellAScreen

	hold    bool
	refused []navigateMsg
}

func (s *shellCGuardScreen) leave(next destination, intent string) (tea.Cmd, bool) {
	if !s.hold {
		return nil, true
	}

	s.refused = append(s.refused, navigateMsg{dest: next, intent: intent})

	return toast(ToneAttention, "Keep or discard the draft first."), false
}

func TestShellCLeaveGuardKeepsTheDestination(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 32)
	guard := &shellCGuardScreen{hold: true}
	h.m.screens[destOverview] = guard
	les := h.m.screens[destLessons].(*lessons)

	h.press("2", "5")
	assert.Equal(t, destOverview, h.m.active, "a number key does not leave a guarded screen")
	assert.Contains(t, h.view(), "Keep or discard the draft first.", "the guard's command runs")

	h.send(navigateMsg{dest: destLessons, intent: "stats"})
	assert.Equal(t, destOverview, h.m.active, "a navigateMsg does not leave a guarded screen")
	assert.Equal(t, lesProposals, les.sub, "the refused destination does not open its intent")

	assert.Equal(t, []navigateMsg{
		{dest: destIntegrations}, {dest: destSettings}, {dest: destLessons, intent: "stats"},
	}, guard.refused, "the guard learns every target, so it can go there after its question")

	// An intent for the current destination is no departure.
	h.send(navigateMsg{dest: destOverview, intent: "build"})
	assert.Len(t, guard.refused, 3)

	// The help closes, so the question of the guard is visible.
	h.press("?")
	require.True(t, h.m.help)
	h.send(navigateMsg{dest: destDoctor})
	assert.False(t, h.m.help)
	assert.Equal(t, destOverview, h.m.active)

	guard.hold = false
	h.press("3")
	assert.Equal(t, destLessons, h.m.active, "a released guard lets the user go")
	assert.Empty(t, f.called())
}
