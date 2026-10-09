package tui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// These tests cover the review fix of the quit with an unsaved Settings
// draft. Before the fix, q and ctrl+c quit at once, and the drafts of
// every group were lost without a question.

func TestSettingsQuitWithADraftAsksFirst(t *testing.T) {
	const before = "agent:\n  cli: claude\n"

	svc := settingsFake(t, before)
	h := settingsDraftCodex(t, svc)
	scr := settingsScr(h)

	h.press("q")
	require.True(t, h.m.quitConfirm, "q asks first")
	assert.False(t, h.quit)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "Discard the unsaved settings draft and quit?", "size %v", size)

		footer := footerLine(frame)
		assert.Contains(t, footer, "enter discard and quit", "size %v", size)
		assert.Contains(t, footer, "ctrl+c quit", "size %v: the second ctrl+c answers yes", size)
		assert.NotContains(t, footer, " q quit", "size %v: q does nothing in the question", size)
	}

	h.resize(120, 40)

	_, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Inference has 1 unsaved change: agent.cli. Quitting discards every unsaved change; "+
		".seamark/config.yaml does not change.", body)
	assert.Contains(t, lesFlat(h.view()), "Inference has 1 unsaved change: agent.cli.")

	// A repeated q keeps the question, and esc stays with the draft.
	h.press("q")
	assert.True(t, h.m.quitConfirm)
	assert.False(t, h.quit)

	h.press("esc")
	assert.False(t, h.m.quitConfirm)
	assert.False(t, h.quit)
	assert.Equal(t, 1, scr.draftTotal(), "esc keeps the draft")
	assert.Equal(t, destSettings, h.m.active)

	// y answers yes, like enter.
	h.press("q", "y")
	assert.True(t, h.quit)
	assert.Equal(t, before, settingsBytes(t, svc), "the quit writes nothing")
	settingsNoWrites(t, svc)
}

func TestSettingsQuitQuestionAnswersEnterAndCtrlC(t *testing.T) {
	cases := map[string][]string{
		"enter":         {"q", "enter"},
		"ctrl+c twice":  {"ctrl+c", "ctrl+c"},
		"ctrl+c then y": {"ctrl+c", "y"},
	}

	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			h := settingsDraftCodex(t, settingsFake(t, ""))

			h.press(keys[0])
			require.True(t, h.m.quitConfirm, "an idle ctrl+c asks too")
			assert.False(t, h.quit)

			h.press(keys[1])
			assert.True(t, h.quit)
		})
	}
}

func TestSettingsQuitWithoutADraftAsksNothing(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)

	// The discarded draft leaves nothing to lose.
	h.press("x", "d")
	require.Zero(t, settingsScr(h).draftTotal())

	h.press("q")
	assert.False(t, h.m.quitConfirm)
	assert.True(t, h.quit, "no draft, no question")
}

func TestSettingsQuitAsksFromEveryDestinationAndEditor(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)

	// A kept draft stays for the session, so a quit from another
	// destination asks too, and the question names every group.
	h.press("down", "down", "enter", "down", "space", "enter")
	require.Len(t, settingsScr(h).draftGroups(), 2)

	h.press("3", "k")
	require.Equal(t, destLessons, h.m.active)

	h.press("q")
	require.True(t, h.m.quitConfirm)
	assert.Contains(t, h.view(), "Discard the unsaved settings draft and quit?")

	_, body, _ := h.m.quitCopy(false)
	assert.Contains(t, body, "2 groups have unsaved changes: Inference (agent.cli), Pin writing (distill.write).")

	h.press("esc")
	assert.Equal(t, destLessons, h.m.active)

	// The save preview captures q, and ctrl+c still reaches the shell.
	h.press("5", "s")
	require.True(t, settingsScr(h).capturing())

	h.press("q")
	assert.False(t, h.m.quitConfirm, "the preview takes q")

	h.press("ctrl+c")
	require.True(t, h.m.quitConfirm)
	assert.Contains(t, h.view(), "Discard the unsaved settings draft and quit?")

	h.press("esc")
	assert.True(t, settingsScr(h).capturing(), "esc returns to the preview")
	assert.False(t, h.quit)
	settingsNoWrites(t, svc)
}

func TestSettingsQuitRunningWorkKeepsPrecedence(t *testing.T) {
	h := settingsDraftCodex(t, settingsFake(t, ""))
	ctx := claimDistillRun(t, h)

	h.press("q")
	require.True(t, h.m.quitConfirm)

	frame := h.view()
	assert.Contains(t, frame, "Stop and exit?")
	assert.NotContains(t, frame, "Discard the unsaved settings draft and quit?")

	_, body, _ := h.m.quitCopy(false)
	assert.Contains(t, body, "The exit also discards the unsaved settings draft.")

	h.press("enter")
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.False(t, h.quit, "the console waits for the work")

	h.send(mutationDoneMsg{})
	assert.True(t, h.quit, "the confirmed exit asks nothing more")

	// Without a draft, the question of the work keeps its old text.
	h2 := newHarness(t, newFake(), 120, 32)
	claimDistillRun(t, h2)

	_, body, _ = h2.m.quitCopy(false)
	assert.NotContains(t, body, "settings draft")
}

// The running-work question ends with the work, even with a draft. The
// result of the work shows, and a later quit asks the draft question.
func TestSettingsQuitAsksAgainAfterTheWorkEnds(t *testing.T) {
	h := settingsDraftCodex(t, settingsFake(t, ""))
	ctx := claimDistillRun(t, h)

	h.press("q")
	require.True(t, h.m.quitConfirm)
	require.Contains(t, h.view(), "Stop and exit?")

	h.send(mutationDoneMsg{})
	assert.False(t, h.m.quitConfirm, "the question of the work closes with the work")
	assert.False(t, h.quit, "nobody confirmed the exit")
	assert.NoError(t, ctx.Err(), "the work ended on its own")
	assert.Equal(t, 1, settingsScr(h).draftTotal(), "the draft stays")

	h.press("q")
	require.True(t, h.m.quitConfirm)

	frame := h.view()
	assert.Contains(t, frame, "Discard the unsaved settings draft and quit?")
	assert.NotContains(t, frame, "Stop and exit?")

	h.press("esc")
	assert.False(t, h.quit)
}

func TestSettingsQuitQuestionFitsTheCrampedScreen(t *testing.T) {
	h := settingsDraftCodex(t, settingsFake(t, ""))

	w, hgt := ui.MinWidth-10, ui.MinHeight-2
	h.resize(w, hgt)
	h.press("q")
	require.True(t, h.m.quitConfirm)

	frame := h.view()
	assertFrame(t, frame, w, hgt)
	assert.Contains(t, lesFlat(frame), "Quitting discards the unsaved settings draft;")
	assert.Contains(t, frame, "discard and quit")

	h.press("enter")
	assert.True(t, h.quit)
}
