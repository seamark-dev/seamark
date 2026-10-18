package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
)

// settingsDraftCodex opens Settings and drafts agent.cli: codex.
func settingsDraftCodex(t *testing.T, svc services.All) *harness {
	t.Helper()

	h := settingsOpen(t, svc, 120, 40)
	h.press("enter", "down", "space", "enter")
	require.Equal(t, 1, settingsScr(h).draftTotal())

	return h
}

func TestSettingsLeaveWithoutADraftAsksNothing(t *testing.T) {
	svc := settingsFake(t, settingsRich)
	h := settingsOpen(t, svc, 120, 40)

	h.press("3")
	assert.Equal(t, destLessons, h.m.active)
	assert.Empty(t, svc.called())
}

func TestSettingsLeaveGuardAsksKeepDiscardOrStay(t *testing.T) {
	const before = "distill:\n  write: false\n"

	svc := settingsFake(t, before)
	h := settingsDraftCodex(t, svc)
	scr := settingsScr(h)

	// A number key asks first and keeps the destination.
	h.press("3")
	require.Equal(t, destSettings, h.m.active)

	frame := h.view()
	assert.Contains(t, frame, "Leave with an unsaved draft?")
	assert.Contains(t, settingsBox(frame, "Leave with"), "Inference has 1 unsaved change: agent.cli.")
	assert.Contains(t, footerLine(frame), "k keep draft")
	assert.Contains(t, footerLine(frame), "d discard")
	assert.Contains(t, footerLine(frame), "esc stay here")
	assert.True(t, scr.capturing(), "the question captures the keys")

	// esc stays here with the draft.
	h.press("esc")
	assert.Equal(t, destSettings, h.m.active)
	assert.Equal(t, 1, scr.draftTotal())
	assert.NotContains(t, h.view(), "Leave with an unsaved draft?")

	// k keeps the draft and continues to the asked destination.
	h.press("3", "k")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, 1, scr.draftTotal(), "keep leaves the draft in the session")

	// The draft is still there on return, and the guard asks again.
	h.press("5")
	assert.NotEmpty(t, settingsLine(h.view(), "agent.cli", "codex", "● draft"))

	// enter equals keep.
	h.press("4", "enter")
	assert.Equal(t, destDoctor, h.m.active)

	// d discards every draft and continues.
	h.press("5", "1")
	require.Contains(t, h.view(), "Leave with an unsaved draft?")
	h.press("d")
	assert.Equal(t, destOverview, h.m.active)
	assert.Zero(t, scr.draftTotal())

	h.press("5", "2")
	assert.Equal(t, destIntegrations, h.m.active, "no draft, no question")

	assert.Equal(t, before, settingsBytes(t, svc), "nothing saves on a tab change")
	assert.Empty(t, svc.called(), "the guard never plans or saves")
}

func TestSettingsLeaveGuardHoldsANavigateIntent(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)
	les := h.m.screens[destLessons].(*lessons)

	h.send(navigateMsg{dest: destLessons, intent: "stats"})
	require.Equal(t, destSettings, h.m.active, "a link from another screen asks too")

	h.press("k")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesStats, les.sub, "the kept departure opens the intent it held")
}

func TestSettingsLeaveGuardDropsAnOpenPreview(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)
	scr := settingsScr(h)

	h.press("s")
	require.Contains(t, h.view(), "Save .seamark/config.yaml")

	// A link from elsewhere arrives while the preview is open.
	h.send(navigateMsg{dest: destOverview})
	assert.Equal(t, destSettings, h.m.active)
	assert.NotContains(t, h.view(), "Save .seamark/config.yaml", "the preview goes; nothing was written")
	assert.Contains(t, h.view(), "Leave with an unsaved draft?")

	h.press("k")
	assert.Equal(t, destOverview, h.m.active)
	assert.Equal(t, flowClosed, scr.flow.stage)
	assert.Equal(t, "", settingsBytes(t, svc))
	settingsNoWrites(t, svc)
}

func TestSettingsDiscardAsksFirst(t *testing.T) {
	const before = "agent:\n  cli: claude\n"

	svc := settingsFake(t, before)
	h := settingsDraftCodex(t, svc)
	scr := settingsScr(h)

	h.press("x")

	frame := h.view()
	assert.Contains(t, frame, "Discard the Inference draft?")
	assert.Contains(t, settingsBox(frame, "Discard the"), "It holds 1 unsaved change: agent.cli. The file does not change.")

	h.press("esc")
	assert.Equal(t, 1, scr.draftTotal(), "esc keeps the draft")

	h.press("x", "d")
	assert.Zero(t, scr.draftTotal())
	assert.Contains(t, h.view(), "Discarded the Inference draft. .seamark/config.yaml is unchanged.")
	assert.NotEmpty(t, settingsLine(h.view(), "agent.cli", "claude", "config.yaml"))

	// x without a draft only explains.
	h.press("x")
	assert.NotContains(t, h.view(), "Discard the Inference draft?")
	assert.Contains(t, h.view(), "Inference has no unsaved change.")

	assert.Equal(t, before, settingsBytes(t, svc))
	assert.Empty(t, svc.called())
}

func TestSettingsDraftsAreKeptPerGroup(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)
	scr := settingsScr(h)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	frame := h.view()
	assert.Contains(t, frame, "2 unsaved changes in 2 groups")
	assert.Equal(t, 1, scr.draftCount(settings.GroupInference))
	assert.Equal(t, 1, scr.draftCount(settings.GroupPinWrite))

	// s saves only the group under the cursor.
	h.press("s", "y")
	assert.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))
	assert.Equal(t, 1, scr.draftCount(settings.GroupInference), "the other group keeps its draft")
	assert.Zero(t, scr.draftCount(settings.GroupPinWrite))

	// A new snapshot drops an edit that the file now holds.
	settingsWrite(t, svc.root, "distill:\n  write: true\nagent:\n  cli: codex\n")
	h.press("r")
	assert.Zero(t, scr.draftTotal(), "an edit that the file holds already is no draft")
}

func TestSettingsSaveResultWaitsAfterLeaving(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsDraftCodex(t, svc)

	// The user leaves while the save runs; the save then fails.
	svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
		h.send(navigateMsg{dest: destOverview})
		require.Equal(t, destOverview, h.m.active, "a running save lets the user go without a question")

		err := errors.New("rename .seamark/config.yaml: permission denied")

		return settings.Result{Path: p.Snapshot.Path, Err: err}, err
	}

	h.press("s", "y")
	assert.Equal(t, destOverview, h.m.active)
	assert.False(t, h.m.st.busy(), "the result frees the slot on any screen")
	assert.Contains(t, h.view(), "Settings were not saved", "the notice row reports the failure")

	h.press("5")
	assert.Contains(t, h.view(), "Save failed", "the result waits in Settings")
	assert.Equal(t, 1, settingsScr(h).draftTotal(), "the draft stays")
	assert.Equal(t, "", settingsBytes(t, svc))
}
