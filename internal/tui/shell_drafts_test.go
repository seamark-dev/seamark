package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The tests of the shell mechanisms that the Phase 4 forms plug into
// (contracts §4.1). They cover the quit question per set of draft
// holders, the help of an open layer, and the editing keys of the
// harness. No Phase 4 form exists yet, so stub screens stand in for the
// holders.

// draftStub is a stub destination that holds drafts of files.
type draftStub struct {
	stubScreen

	files     []string
	discarded bool
}

func (s *draftStub) drafts() []string {
	if s.discarded {
		return nil
	}

	return s.files
}

func (s *draftStub) discardDrafts() { s.discarded = true }

// settingsLessonsDraft puts a draft of a lessons.yaml group into the
// Settings screen of h. That group has no editor yet.
func settingsLessonsDraft(h *harness) {
	settingsScr(h).edits[settings.Threshold] = settings.Edit{Field: settings.Threshold, Int: 3}
}

func TestQuitNamesEachFileWhoseDraftIsLost(t *testing.T) {
	const lessonsOnly = "Quitting discards every unsaved draft of .seamark/lessons.yaml. The file does not change."

	cases := []struct {
		name  string
		setup func(t *testing.T) (*harness, *draftStub)
		body  string
	}{
		{
			name: "a holder with a lessons.yaml draft",
			setup: func(t *testing.T) (*harness, *draftStub) {
				h := newHarness(t, newFake(), 120, 40)
				stub := &draftStub{files: []string{pinfile.File}}
				h.m.screens[destLessons] = stub

				return h, stub
			},
			body: lessonsOnly,
		},
		{
			name: "a Settings draft of a lessons.yaml group",
			setup: func(t *testing.T) (*harness, *draftStub) {
				h := settingsOpen(t, settingsFake(t, ""), 120, 40)
				settingsLessonsDraft(h)

				return h, nil
			},
			body: lessonsOnly,
		},
		{
			name: "a config.yaml Settings draft and a lessons.yaml holder",
			setup: func(t *testing.T) (*harness, *draftStub) {
				h := settingsDraftCodex(t, settingsFake(t, ""))
				stub := &draftStub{files: []string{pinfile.File}}
				h.m.screens[destLessons] = stub

				return h, stub
			},
			body: "Quitting discards every unsaved draft of .seamark/config.yaml and .seamark/lessons.yaml. The " +
				"files do not change.",
		},
		{
			name: "both kinds of Settings draft",
			setup: func(t *testing.T) (*harness, *draftStub) {
				h := settingsDraftCodex(t, settingsFake(t, ""))
				settingsLessonsDraft(h)

				return h, nil
			},
			body: "Quitting discards every unsaved draft of .seamark/config.yaml and .seamark/lessons.yaml. The " +
				"files do not change.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, stub := tc.setup(t)

			h.press("q")
			require.True(t, h.m.quitConfirm, "a draft asks first")
			assert.False(t, h.quit)

			title, body, _ := h.m.quitCopy(false)
			assert.Equal(t, "Discard the unsaved drafts and quit?", title)
			assert.Equal(t, tc.body, body)

			_, compact, _ := h.m.quitCopy(true)
			assert.Equal(t, tc.body, compact, "the body fits the compact screen as it is")

			for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}, {40, 10}} {
				h.resize(size[0], size[1])

				frame := h.view()
				assertFrame(t, frame, size[0], size[1])
				assert.Contains(t, lesFlat(frame), "Discard the unsaved drafts and quit?", "size %v", size)

				// The question itself: the modal, or the whole compact
				// screen. The screen behind the modal is not part of it.
				dialog := frame
				if !h.m.cramped() {
					dialog = h.m.quitView(size[0], size[1]-1)
				}

				if !strings.Contains(tc.body, settingsFile) {
					assert.NotContains(t, dialog, "config.yaml", "size %v: no config.yaml draft is lost", size)
				}
			}

			h.resize(120, 40)

			// esc keeps every draft; enter discards them and quits.
			h.press("esc")
			assert.False(t, h.quit)
			assert.NotEmpty(t, h.m.draftFiles())

			h.press("q", "enter")
			assert.True(t, h.quit)
			assert.Empty(t, h.m.draftFiles(), "the exit drops every draft")

			if stub != nil {
				assert.True(t, stub.discarded)
			}
		})
	}
}

func TestQuitKeepsTheSettingsWordsForConfigDraftsOnly(t *testing.T) {
	h := settingsDraftCodex(t, settingsFake(t, ""))

	// An empty holder adds no file, so the Settings words stay.
	h.m.screens[destLessons] = &draftStub{}

	h.press("q")

	title, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Discard the unsaved settings draft and quit?", title)
	assert.Equal(t, "Inference has 1 unsaved change: agent.cli. Quitting discards every unsaved change; "+
		".seamark/config.yaml does not change.", body)

	// Without any draft, q quits at once.
	h2 := newHarness(t, newFake(), 120, 40)
	h2.m.screens[destLessons] = &draftStub{}
	h2.press("q")
	assert.True(t, h2.quit)
	assert.False(t, h2.m.quitConfirm)
}

func TestQuitDuringWorkNamesTheLessonsDraft(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	stub := &draftStub{files: []string{pinfile.File}}
	h.m.screens[destLessons] = stub
	claimDistillRun(t, h)

	h.press("q")
	require.True(t, h.m.quitConfirm)

	title, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Stop and exit?", title)
	assert.True(t, strings.HasSuffix(body, " The exit also discards every unsaved draft of .seamark/lessons.yaml."),
		"body: %s", body)
	assert.NotContains(t, body, "settings draft")

	_, compact, _ := h.m.quitCopy(true)
	assert.NotContains(t, compact, "lessons.yaml", "the compact screen has no room for the drafts")

	// The drafts go only when the console really exits.
	h.press("enter")
	assert.False(t, stub.discarded, "the exit waits for the work")

	h.send(mutationDoneMsg{})
	assert.True(t, h.quit)
	assert.True(t, stub.discarded)
}

func TestQuitAfterWorkAsksAgainForALaterDraft(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	stub := &draftStub{}
	h.m.screens[destLessons] = stub
	require.True(t, h.m.st.begin("Refreshing evidence"))

	h.press("q", "enter")
	require.True(t, h.m.quitAfter, "the yes waits for the work")
	require.False(t, h.m.quitConfirm)

	// Drafting works while the work runs, so a draft can start after the
	// yes. The question did not name it.
	stub.files = []string{pinfile.File}

	h.send(mutationDoneMsg{})
	assert.False(t, h.quit, "the exit never drops a draft that the question did not name")
	assert.False(t, stub.discarded)
	assert.True(t, h.m.quitConfirm, "the console asks the draft question instead")
	assert.False(t, h.m.quitAfter)

	title, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Discard the unsaved drafts and quit?", title)
	assert.Equal(t, "Quitting discards every unsaved draft of .seamark/lessons.yaml. The file does not change.", body)

	h.press("enter")
	assert.True(t, h.quit)
	assert.True(t, stub.discarded)
}

func TestQuitQuestionWithoutDraftsAsksPlainly(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	stub := &draftStub{files: []string{pinfile.File}}
	h.m.screens[destLessons] = stub

	h.press("q")
	require.True(t, h.m.quitConfirm)

	// A reload can drop a draft while the question is open, for example
	// when the file now holds the drafted value.
	stub.files = nil

	title, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Quit Seamark?", title)
	assert.Equal(t, "No unsaved draft remains. Quitting changes no file.", body)

	_, compact, _ := h.m.quitCopy(true)
	assert.Equal(t, body, compact)

	for _, size := range [][2]int{{120, 40}, {40, 10}} {
		h.resize(size[0], size[1])
		assert.NotContains(t, lesFlat(h.view()), "draft of .", "size %v", size)
	}

	h.resize(120, 40)
	h.press("enter")
	assert.True(t, h.quit)
}

// helpStub is a stub destination with the help of an open layer.
type helpStub struct {
	stubScreen

	page helpPage
	ok   bool
}

func (s *helpStub) contextHelp() (helpPage, bool) { return s.page, s.ok }

// helpStubConsole starts a console whose first destination is a
// capturing helpStub.
func helpStubConsole(t *testing.T, w, hgt int, ok bool) (*harness, *helpStub) {
	t.Helper()

	h := newHarness(t, newFake(), w, hgt)
	stub := &helpStub{ok: ok, page: helpPage{
		title: "Stub form",
		text:  []string{"The form explains itself here.", "A second paragraph \x1b[31mwith an escape."},
		keys: []key.Binding{
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "review")),
			key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "discard")),
		},
	}}
	stub.capture = true
	h.m.screens[destOverview] = stub

	return h, stub
}

func TestContextHelpOpensForALayerWithoutAFocusedField(t *testing.T) {
	h, stub := helpStubConsole(t, 120, 40, true)

	h.press("?")
	require.True(t, h.m.help)
	require.NotNil(t, h.m.page)
	assert.Empty(t, stub.seen, "the ? opens the help and does not reach the layer")

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	flat := lesFlat(frame)
	for _, want := range []string{
		"Stub form", "? or esc to close", "The form explains itself here.", "A second paragraph [31mwith an escape.",
		"KEYS", "s review", "x discard",
	} {
		assert.Contains(t, flat, want)
	}

	assert.NotContains(t, flat, "GLOBAL", "the layer help replaces the key list of the console")
	assert.NotContains(t, h.m.View(), "\x1b[31m", "the help text goes through the sanitizer")
	assert.Contains(t, footerLine(frame), "esc close")

	// esc returns to the layer; it captures the keys again.
	h.press("esc")
	assert.False(t, h.m.help)
	assert.Nil(t, h.m.page)

	h.press("x", "q")
	assert.Equal(t, []string{"x", "q"}, stub.seen)
	assert.False(t, h.quit, "the layer still captures q")

	// The help of the console still opens with ? after the layer closes.
	stub.capture = false

	h.press("?")
	assert.True(t, h.m.help)
	assert.Nil(t, h.m.page)
	assert.Contains(t, h.view(), "GLOBAL")
}

func TestContextHelpLeavesQuestionMarkToAFocusedField(t *testing.T) {
	h, stub := helpStubConsole(t, 120, 40, false)

	h.press("?")
	assert.False(t, h.m.help, "a text field has focus, so ? is a character")
	assert.Equal(t, []string{"?"}, stub.seen)

	// A screen without the method keeps the old behaviour.
	plain, own := stubConsole(t, 120, 40, stubKeys(3))
	own.capture = true

	plain.press("?")
	assert.False(t, plain.m.help)
	assert.Equal(t, []string{"?"}, own.seen)

	// Today no Lessons sub-view has a help of its own.
	_, ok := hookLessons(plain).contextHelp()
	assert.False(t, ok)
}

func TestContextHelpScrollsAtTheMinimumSize(t *testing.T) {
	h, stub := helpStubConsole(t, 60, 20, true)
	stub.page.text = []string{strings.Repeat("A long paragraph that wraps. ", 40)}

	h.press("?")

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	assert.Regexp(t, `\d+–\d+ of \d+`, frame, "a long help shows its position")
	assert.Contains(t, footerLine(frame), "↑↓ scroll")

	h.press("G")
	assert.Contains(t, lesFlat(h.view()), "x discard", "the end of the help shows the keys")
}

func TestHarnessNamesTheEditingKeys(t *testing.T) {
	for name, want := range map[string]tea.KeyMsg{
		"ctrl+j":    {Type: tea.KeyCtrlJ},
		"alt+enter": {Type: tea.KeyEnter, Alt: true},
		"delete":    {Type: tea.KeyDelete},
		"backspace": {Type: tea.KeyBackspace},
	} {
		got := keyMsg(name)
		assert.Equal(t, want, got)
		assert.Equal(t, name, got.String(), "the name matches the key string of Bubble Tea")
	}

	// A paste is one message with every rune; no binding matches it, so
	// a pasted q never quits. The stub does not capture, so the global
	// keys of the shell see the paste first.
	h, stub := stubConsole(t, 120, 40, stubKeys(3))

	h.paste("q")
	h.paste("q\nline two")
	assert.Equal(t, []string{"[q]", "[q\nline two]"}, stub.seen)
	assert.False(t, h.quit)
	assert.False(t, h.m.quitConfirm)
}
