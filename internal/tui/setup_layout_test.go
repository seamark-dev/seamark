package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests in this file cover the setup wizard in short terminals:
// every client stays visible or is announced, and the explanation of
// the selected addition stays reachable.

func TestSetupClientStepShowsEveryClientInAShortTerminal(t *testing.T) {
	f, _ := setupFake()
	f.clients = inspecttest.Registry().Clients()
	f.integrations = append(integMatrix(), integration.Inspection{ClientID: inspecttest.SharedID, Name: "Shared Agent"})

	for _, size := range [][2]int{{ui.MinWidth, ui.MinHeight}, {60, 20}} {
		h := newHarness(t, f, size[0], size[1])
		h.press("2")
		h.send(navigateMsg{dest: destIntegrations, intent: "setup"})

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "Choose a client")
		assert.Contains(t, frame, "Claude Code", "size %v", size)
		assert.Contains(t, frame, "Codex", "size %v", size)
		assert.Contains(t, frame, "Shared Agent", "size %v", size)
	}
}

func TestIntegrationClientListAnnouncesHiddenClients(t *testing.T) {
	th := ui.NewTheme(false)
	items := []integItem{
		{name: "Claude Code", badge: "· not set up", summary: []string{"6 not configured"}},
		{name: "Codex", badge: "· not set up", summary: []string{"5 not configured"}},
		{name: "Shared Agent", badge: "· not set up", summary: []string{"1 not configured"}},
	}

	var l ui.ListState

	render := func(h int) string {
		return ansi.Strip(strings.Join(integRenderClients(th, items, &l, 40, h, true), "\n"))
	}

	out := render(4)
	assert.Contains(t, out, "Claude Code")
	assert.NotContains(t, out, "Codex")
	assert.Contains(t, out, "1–1 of 3", "a hidden client is announced")
	assert.LessOrEqual(t, setupLineCount(out), 4)

	l.Cursor = 2
	out = render(4)
	assert.Contains(t, out, "Shared Agent", "the cursor item stays visible")
	assert.Contains(t, out, "3–3 of 3")

	// A list that fits shows no note.
	l = ui.ListState{}
	out = render(8)
	assert.Contains(t, out, "Shared Agent")
	assert.NotContains(t, out, "of 3")
}

func TestSetupChooseDetailsReachableInCompactLayout(t *testing.T) {
	f, requests := setupFake()
	h := setupOpen(t, f, 60, 20)
	wz := setupScreen(h).wizard

	// Tool grants carry the native trust note that the spec requires.
	h.press("down", "down", "down")

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	assert.Contains(t, frame, "press i to read the full text", "a cut explanation names the details key")
	assert.Contains(t, ansi.Strip(h.m.st.theme.Hints(wz.keys())), "i details", "the step lists the details key")

	// The footer gives way to the global keys at this width; the help
	// lists every key of the step.
	h.press("?")
	rows, _ := h.m.helpLayout(60, 20-chromeHeight)
	assert.Contains(t, ansi.Strip(strings.Join(rows, "\n")), "details")

	h.press("esc", "i")
	require.True(t, wz.aboutOpen)

	frame = h.view()
	assertFrame(t, frame, 60, 20)
	assert.Contains(t, frame, "Details · Tool grants")
	assert.Contains(t, setupBoxText(frame, 0, 60), "Seamark never grants native trust")
	assert.Contains(t, ansi.Strip(h.m.st.theme.Hints(wz.keys())), "esc choices")

	// The details box scrolls to the notes at the end.
	h.press("end")
	assert.Contains(t, setupBoxText(h.view(), 0, 60), "Nothing is written before you confirm the plan")

	// Space and enter do nothing in the details pane.
	h.press("space", "enter")
	assert.Empty(t, *requests)

	h.press("esc")
	assert.False(t, wz.aboutOpen)
	assert.Contains(t, h.view(), "[ ] Tool grants")
	assert.Same(t, wz, setupScreen(h).wizard, "esc closes the details, not the wizard")
	assert.Equal(t, setupStepChoices, wz.step)
	assert.Equal(t, 3, wz.rows.Cursor, "the cursor survives the details pane")

	// The details key toggles the pane.
	h.press("i", "i")
	assert.False(t, wz.aboutOpen)
	require.Empty(t, f.called())
}

func TestSetupChooseDetailsFitWhenThereIsRoom(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 40)

	h.press("down", "down", "down")
	assert.Contains(t, h.view(), "i details", "a wide footer lists the details key")

	frame := setupBoxText(h.view(), 61, 120)
	assert.Contains(t, frame, "Seamark never grants native trust")
	assert.NotContains(t, frame, "press i to read the full text", "a whole explanation needs no note")
}

func TestSetupClipKeepsWholeBlocksAndNamesTheKey(t *testing.T) {
	th := ui.NewTheme(false)
	block := []string{"one", "two", "three", "four"}

	assert.Equal(t, block, setupClip(th, block, 20, 4))
	assert.Empty(t, setupClip(th, block, 20, 0))

	clipped := ansi.Strip(strings.Join(setupClip(th, block, 40, 3), "\n"))
	assert.Equal(t, "one\ntwo\n… press i to read the full text", clipped)
	assert.Equal(t, []string{"one", "two", "three", "four"}, block, "the clip leaves its input as it is")
}

// setupLineCount returns the number of lines of a rendered block.
func setupLineCount(s string) int {
	if s == "" {
		return 0
	}

	return strings.Count(s, "\n") + 1
}

// TestSetupWideReviewNotesScrollWithTab proves that the plan notes of the
// wide review stay reachable when they do not fit: tab moves the
// movement keys to the notes, and tab again gives them back to the files.
func TestSetupWideReviewNotesScrollWithTab(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 22)
	h.press("enter")

	wz := setupScreen(h).wizard
	require.NotNil(t, wz)
	require.True(t, wz.st.wide())

	frame := h.view()
	assertFrame(t, frame, 120, 22)

	if !assert.Contains(t, frame, "tab to scroll") {
		return
	}

	h.press("tab")
	assert.True(t, wz.notesFocus)
	assert.Contains(t, h.view(), "tab files")

	before := wz.planOff
	h.press("down", "down")
	assert.Greater(t, wz.planOff, before, "the notes scroll while they have the keys")

	cursor := wz.paths.Cursor
	h.press("tab", "down")
	assert.False(t, wz.notesFocus)
	assert.NotEqual(t, cursor, wz.paths.Cursor, "the files take the keys again")
}
