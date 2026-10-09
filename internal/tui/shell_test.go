package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// The tests of the shell chrome. A stub screen controls the key list,
// so the tests see the cramped terminal, the quit dialog, ctrl+c, the
// footer, the help overlay, the minimum size, and the leave guard.

// stubScreen is a stub destination with a scripted key list. The
// shell tests use it to control the footer and the help content, and
// to see which keys reach the active screen.
type stubScreen struct {
	bindings []key.Binding
	capture  bool
	seen     []string
}

func (s *stubScreen) title() string        { return "Stub" }
func (s *stubScreen) open(string) tea.Cmd  { return nil }
func (s *stubScreen) view(_, _ int) string { return "stub view" }
func (s *stubScreen) keys() []key.Binding  { return s.bindings }
func (s *stubScreen) capturing() bool      { return s.capture }

// update records every key message that reaches the screen.
func (s *stubScreen) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		s.seen = append(s.seen, k.String())
	}

	return nil
}

// stubKeys returns n bindings with distinct descriptions. Nine or
// more do not fit the footer next to the global hints, even at 120
// columns.
func stubKeys(n int) []key.Binding {
	letters := "abcdefghijklmnop"
	out := make([]key.Binding, 0, n)

	for i := range n {
		k := string(letters[i])
		out = append(out, key.NewBinding(key.WithKeys(k), key.WithHelp(k, "stub action "+itoa(i+1))))
	}

	return out
}

// assertWholeHints checks that the footer text holds only complete
// hints from bindings and the ellipsis that marks dropped hints.
func assertWholeHints(t *testing.T, footer string, bindings []key.Binding) {
	t.Helper()

	full := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		full[b.Help().Key+" "+b.Help().Desc] = true
	}

	for _, part := range strings.Split(strings.TrimSpace(footer), "  ") {
		part = strings.TrimSpace(part)
		if part == "" || part == "…" {
			continue
		}

		assert.True(t, full[part], "hint %q is cut mid-word in %q", part, footer)
	}
}

// stubConsole starts a console at w by h whose Overview is a stub screen.
func stubConsole(t *testing.T, w, h int, bindings []key.Binding) (*harness, *stubScreen) {
	t.Helper()

	hs := newHarness(t, newFake(), w, h)
	stub := &stubScreen{bindings: bindings}
	hs.m.screens[destOverview] = stub

	return hs, stub
}

// footerLine returns the last line of a frame: the key line.
func footerLine(frame string) string {
	lines := strings.Split(frame, "\n")

	return lines[len(lines)-1]
}

// pendingLedgerFake returns services with pending proposals whose
// previews and commits succeed.
func pendingLedgerFake() *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}

	return f
}

func TestCrampedTerminalDropsScreenKeys(t *testing.T) {
	f := pendingLedgerFake()
	h := newHarness(t, f, minWidth-10, minHeight-2)

	h.press("3", "space", "d", "y", "enter")
	assert.Equal(t, destOverview, h.m.active, "a destination key must not act while its view is hidden")
	assert.Empty(t, f.called(), "no hidden preview or confirmation may run")
	assert.Contains(t, h.view(), "More space needed")

	g := ovPopulated()
	h2 := newHarness(t, g, minWidth-10, minHeight-2)

	h2.press("b", "enter")
	assert.Empty(t, g.called(), "an index build must not start behind the resize message")

	h3, stub := stubConsole(t, minWidth-10, minHeight-2, stubKeys(2))
	stub.capture = true

	h3.press("a", "enter", "esc", "space", "x")
	assert.Empty(t, stub.seen, "a capturing screen gets no key while it is hidden")
	assert.False(t, h3.quit)
}

func TestCrampedTerminalQuitsFromACapturingScreen(t *testing.T) {
	f := pendingLedgerFake()
	h := lesOpen(t, f, 120, 32)

	h.press("space", "d")
	require.True(t, h.m.screens[destLessons].capturing(), "the dismiss preview captures keys")

	h.resize(minWidth-10, minHeight-2)
	h.press("q")
	assert.True(t, h.quit, "q quits although the hidden screen captures keys")
	assert.Equal(t, []string{"PreviewDecision:dismiss"}, f.called(), "quitting commits nothing")

	h2, stub := stubConsole(t, minWidth-10, minHeight-2, nil)
	stub.capture = true

	h2.press("ctrl+c")
	assert.True(t, h2.quit)
	assert.Empty(t, stub.seen)
}

func TestCrampedTerminalShowsCompactHelp(t *testing.T) {
	w, hgt := minWidth-10, minHeight-2
	h := newHarness(t, newFake(), w, hgt)

	h.press("?")

	frame := h.view()
	assertFrame(t, frame, w, hgt)
	assert.Contains(t, frame, "Keys at this size")
	assert.Contains(t, frame, "close help")
	assert.NotContains(t, frame, "More space needed")

	h.press("?")
	assert.Contains(t, h.view(), "More space needed", "? toggles the compact help")

	h.press("?", "esc")
	assert.Contains(t, h.view(), "More space needed", "esc closes the compact help")
	assert.False(t, h.quit)
}

func TestCrampedTerminalShowsTheQuitQuestion(t *testing.T) {
	w, hgt := minWidth-10, minHeight-2
	h := newHarness(t, newFake(), w, hgt)
	require.True(t, h.m.st.begin("Building the index"))

	h.press("q")

	frame := h.view()
	assertFrame(t, frame, w, hgt)
	assert.Contains(t, frame, "Exit after the running operation?")
	assert.Contains(t, lesFlat(frame), "Building the index is running")
	assert.Contains(t, frame, "keep working")

	h.press("esc")
	assert.False(t, h.m.quitConfirm)
	assert.Contains(t, h.view(), "More space needed")

	h.press("q", "enter")
	assert.False(t, h.quit, "the console waits for the running work")
	assert.True(t, h.m.quitAfter)

	h.send(mutationDoneMsg{})
	assert.True(t, h.quit)
}

func TestShellQuitDialogClosesWhenTheMutationEnds(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	require.True(t, h.m.st.begin("Building the index"))

	h.press("q")
	require.Contains(t, h.view(), "Exit after the running operation?")

	h.send(mutationDoneMsg{})
	assert.False(t, h.quit)
	assert.False(t, h.m.quitConfirm, "the question no longer applies")
	assert.NotContains(t, h.view(), "Exit after the running operation?")
	assert.NotContains(t, h.view(), " is running", "no dialog names an empty operation")

	require.True(t, h.m.st.begin("Applying the setup"))
	h.send(mutationDoneMsg{})
	assert.False(t, h.quit, "a later mutation must not exit the console")
}

func TestShellQuitDialogKeepsActionLabelsWhole(t *testing.T) {
	for _, w := range []int{minWidth, 120} {
		h := newHarness(t, newFake(), w, 32)
		require.True(t, h.m.st.begin("Building the index"))

		h.press("q")

		frame := h.view()
		assertFrame(t, frame, w, 32)

		// The action row inside the modal holds both labels whole. The
		// footer repeats them, so the check looks at the boxed row.
		assert.Contains(t, frame, "│ enter exit after completion   esc keep working", "width %d: no label wraps inside itself", w)
		assert.Contains(t, frame, "╰", "width %d: the bottom border shows", w)
	}
}

func TestShellQuitDialogQuitsAtOnceWhenIdle(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	h.m.quitConfirm = true

	h.press("enter")
	assert.True(t, h.quit, "with no running work, yes means quit now")
	assert.False(t, h.m.quitAfter, "no latch outlives the question")
}

func TestShellQuitDialogCancelClearsTheLatch(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)
	require.True(t, h.m.st.begin("Building the index"))

	h.press("q", "enter")
	require.True(t, h.m.quitAfter)

	h.press("q", "esc")
	assert.False(t, h.m.quitAfter, "keep working cancels the exit")

	h.send(mutationDoneMsg{})
	assert.False(t, h.quit)
}

func TestCtrlCLeavesTheFirstRunAndHelp(t *testing.T) {
	f := newFake()
	f.fresh = true

	h := newHarness(t, f, 120, 32)
	require.Contains(t, h.view(), "Set up seamark in this repository")

	h.press("ctrl+c")
	assert.True(t, h.quit)

	// The review of the Create config step waits for y; ctrl+c still
	// leaves, and nothing is applied.
	f2 := newFake()
	f2.fresh = true
	f2.planSetup = func([]integration.ClientSetup) (*SetupPreview, error) { return frDocsPreview(), nil }

	h2 := newHarness(t, f2, 120, 32)
	h2.press("enter")
	require.Contains(t, h2.view(), "Nothing is written until you press y")

	h2.press("ctrl+c")
	assert.True(t, h2.quit)
	assert.Equal(t, []string{"PlanSetup"}, f2.called(), "quitting applies nothing")

	h3 := newHarness(t, newFake(), 120, 32)
	h3.press("?", "ctrl+c")
	assert.True(t, h3.quit)
}

func TestShellFooterKeepsGlobalHints(t *testing.T) {
	bindings := stubKeys(9)

	for _, w := range []int{minWidth, 80, 120} {
		h, _ := stubConsole(t, w, 32, bindings)

		frame := h.view()
		assertFrame(t, frame, w, 32)

		footer := footerLine(frame)
		assert.Contains(t, footer, "r refresh", "width %d", w)
		assert.Contains(t, footer, "? help", "width %d", w)
		assert.Contains(t, footer, "q quit", "width %d", w)
		assert.Regexp(t, `\d\d:\d\d:\d\d $`, footer, "width %d: the snapshot clock sits at the far right", w)

		local, _, found := strings.Cut(footer, "r refresh")
		require.True(t, found)
		assertWholeHints(t, local, bindings)

		// At the minimum width the global hints and the clock leave too
		// little room for one stub hint; "? help" still leads to it.
		if w >= 80 {
			assert.Contains(t, footer, "a stub action 1", "width %d keeps the first local hint", w)
			assert.Contains(t, footer, "…", "width %d marks the dropped hints", w)
		}
	}
}

func TestShellFooterOnProposalsKeepsHelpAndQuit(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 24}, {minWidth, minHeight}} {
		h := lesOpen(t, pendingLedgerFake(), size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])

		footer := footerLine(frame)
		assert.Contains(t, footer, "? help", "size %v", size)
		assert.Contains(t, footer, "q quit", "size %v", size)
	}
}

func TestShellFooterDropsWholeHintsInOverlays(t *testing.T) {
	bindings := stubKeys(9)
	h, stub := stubConsole(t, minWidth, 32, bindings)
	stub.capture = true

	footer := footerLine(h.view())
	assert.NotContains(t, footer, "? help", "a capturing screen receives ? itself")
	assert.NotContains(t, footer, " q quit", "a capturing screen takes q as text or ignores it")
	assert.Contains(t, footer, "ctrl+c quit", "the interrupt key that always quits stays visible")
	assert.Contains(t, footer, "…")

	local, _, found := strings.Cut(footer, "ctrl+c quit")
	require.True(t, found)
	assertWholeHints(t, local, bindings)
}

func TestShellHelpScrollsAndKeepsItsFrame(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
		w, hgt := size[0], size[1]
		h, _ := stubConsole(t, w, hgt, stubKeys(10))

		h.press("?")

		frame := h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "STUB", "size %v: the group of the active screen shows first", size)
		assert.Contains(t, frame, "? or esc to close", "size %v: the close hint in the border stays visible", size)
		assert.Contains(t, frame, "╰", "size %v: the bottom border stays visible", size)
		assert.Contains(t, frame, " of ", "size %v: a position note shows", size)
		assert.NotContains(t, frame, "seamark lessons --stats", "size %v: the body scrolls", size)
		assert.Contains(t, footerLine(frame), "scroll")

		h.press("down")
		assert.Equal(t, 1, h.m.helpOff)

		h.press("pgdown")
		assert.Greater(t, h.m.helpOff, 1)

		h.press("end")

		frame = h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "seamark lessons", "size %v: end shows the plain commands", size)
		assert.Contains(t, frame, "--stats", "size %v", size)
		assert.Contains(t, frame, "? or esc to close", "size %v", size)
		assert.Contains(t, frame, "╰", "size %v", size)
		assert.NotContains(t, frame, "STUB", "size %v", size)

		last := h.m.helpOff
		h.press("down")
		assert.Equal(t, last, h.m.helpOff, "size %v: the offset stops at the last row", size)

		h.press("home")
		assert.Equal(t, 0, h.m.helpOff)
		assert.Contains(t, h.view(), "STUB")

		h.press("esc")
		assert.False(t, h.m.help)
		assert.NotContains(t, h.view(), "? or esc to close")
	}
}

func TestShellHelpShowsThreeColumnsWhenWide(t *testing.T) {
	h, _ := stubConsole(t, 120, 40, stubKeys(3))

	h.press("?")

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	// GLOBAL, MOVE and the screen group share one header row, as in the
	// 1a help modal.
	assert.Regexp(t, `│ GLOBAL +MOVE +STUB +│`, frame)
	assert.Contains(t, frame, "Keys")
	assert.NotContains(t, footerLine(frame), "scroll", "the whole help fits at 120x40")
	assert.Contains(t, frame, "Every key that writes a file asks first.")

	// The modal covers the middle only: the chrome stays visible behind
	// it, so the user still sees where the help came from.
	assert.Contains(t, frame, "▶┊◀ seamark")
	assert.Contains(t, frame, "1 Stub")
}

func TestShellHelpReachesPlainCommandsOnLessons(t *testing.T) {
	f := pendingLedgerFake()
	h := lesOpen(t, f, 80, 24)

	h.press("?", "end")

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "PLAIN COMMANDS")
	assert.Contains(t, frame, "seamark lessons --proposals")
	assert.Contains(t, frame, "? or esc to close")
	assert.Contains(t, frame, "╰")

	h.press("?")
	assert.False(t, h.m.help)
	assert.Empty(t, f.called(), "reading the help calls no service")
}

func TestShellHelpOffsetSurvivesAResize(t *testing.T) {
	h, _ := stubConsole(t, 120, 50, stubKeys(4))

	h.press("?")
	require.Equal(t, 0, h.m.helpOff)

	h.resize(minWidth, minHeight)
	h.press("end")
	require.Positive(t, h.m.helpOff)

	h.resize(120, 50)
	assertFrame(t, h.view(), 120, 50)
	assert.Contains(t, h.view(), "STUB", "a larger window shows the whole help")

	h.press("up")
	assert.Equal(t, 0, h.m.helpOff, "the offset is clamped to the new size before it moves")
}

func TestShellFirstRunKeepsStepColumns(t *testing.T) {
	f := newFake()
	f.fresh = true
	f.agentsOnPath = []string{"claude", "codex"}

	// col returns the display column where s starts in line.
	col := func(line, s string) int {
		before, _, found := strings.Cut(line, s)
		require.True(t, found, "%q holds %q", line, s)

		return lipgloss.Width(before)
	}

	for _, w := range []int{minWidth, 80, 120} {
		h := newHarness(t, f, w, 32)
		frame := h.view()
		assertFrame(t, frame, w, 32)

		lines := strings.Split(frame, "\n")
		steps := []string{"1  Create config", "2  Build the index", "3  Connect agents"}
		descs := []string{".seamark/", "local parse", "found on PATH"}

		var labelCols, descCols []int

		for i, step := range steps {
			row := lines[lineIndex(lines, step)]
			labelCols = append(labelCols, col(row, step))
			descCols = append(descCols, col(row, descs[i]))
		}

		assert.Equal(t, labelCols[0], labelCols[1], "width %d: the step labels share a column", w)
		assert.Equal(t, labelCols[0], labelCols[2], "width %d", w)
		assert.Equal(t, descCols[0], descCols[1], "width %d: the descriptions share a column", w)
		assert.Equal(t, descCols[0], descCols[2], "width %d", w)
		assert.Contains(t, frame, "enter start", "width %d: the key row stays visible", w)
	}
}

func TestASCIIShellFramesUseNoUnicode(t *testing.T) {
	asciiOnly := func(t *testing.T, frame string) {
		t.Helper()

		for _, r := range frame {
			require.Less(t, r, rune(0x80), "frame holds %q:\n%s", r, frame)
		}
	}

	start := func(w, hgt int) *harness {
		hs := &harness{t: t, m: newModel(newFake(), newTheme(true))}
		hs.run(hs.m.Init())
		hs.resize(w, hgt)

		return hs
	}

	h := start(minWidth-10, minHeight-2)
	asciiOnly(t, h.view())

	h.press("?")
	asciiOnly(t, h.view())

	h.press("?")
	require.True(t, h.m.st.begin("Building the index"))
	h.press("q")
	asciiOnly(t, h.view())

	h2 := start(80, 24)
	h2.m.screens[destOverview] = &stubScreen{bindings: stubKeys(10)}
	h2.press("?")
	asciiOnly(t, h2.view())

	// The first-run guide and the Doctor view use the glyph set too.
	f := newFake()
	f.fresh = true
	f.agentsOnPath = []string{"claude"}
	f.planSetup = func([]integration.ClientSetup) (*SetupPreview, error) { return frDocsPreview(), nil }

	h3 := &harness{t: t, m: newModel(f, newTheme(true))}
	h3.run(h3.m.Init())
	h3.resize(120, 40)
	asciiOnly(t, h3.view())

	h3.press("enter")
	asciiOnly(t, h3.view())

	g := newFake()
	g.doctor = docReport()

	h4 := &harness{t: t, m: newModel(g, newTheme(true))}
	h4.run(h4.m.Init())
	h4.resize(120, 40)
	h4.press("4")
	asciiOnly(t, h4.view())

	h4.resize(80, 24)
	h4.press("enter")
	asciiOnly(t, h4.view())
}

// TestShellOverlaysFitAtTheMinimumSize proves finding 3/9 through the
// shell: at 60x20 the first-run guide and the quit dialog keep their
// actions and their bottom border.
func TestShellOverlaysFitAtTheMinimumSize(t *testing.T) {
	f := newFake()
	f.fresh = true
	f.agentsOnPath = []string{"claude", "codex"}

	h := newHarness(t, f, minWidth, minHeight)
	frame := h.view()
	assertFrame(t, frame, minWidth, minHeight)
	assert.Contains(t, frame, "Set up seamark in this repository")
	assert.Contains(t, frame, "v read-only view")
	assert.Contains(t, frame, "3  Connect agents")
	assert.Contains(t, frame, "╰", "the first-run box keeps its bottom border")

	h.press("v")
	require.True(t, h.m.st.begin("Building the index"))
	h.press("q")

	frame = h.view()
	assertFrame(t, frame, minWidth, minHeight)
	assert.Contains(t, frame, "Exit after the running operation?")
	assert.Contains(t, frame, "│ enter exit after completion   esc keep working")
	assert.Contains(t, frame, "╰", "the quit dialog keeps its bottom border")
}

// TestShellMinimumSizeIs60x20 proves the raised minimum height.
func TestShellMinimumSizeIs60x20(t *testing.T) {
	h := newHarness(t, newFake(), 60, 19)
	assert.Contains(t, h.view(), "More space needed")

	h.resize(59, 20)
	assert.Contains(t, h.view(), "More space needed")

	h.resize(60, 20)
	assert.NotContains(t, h.view(), "More space needed")
}

// lineIndex returns the index of the first line that holds s.
func lineIndex(lines []string, s string) int {
	for i, l := range lines {
		if strings.Contains(l, s) {
			return i
		}
	}

	return -1
}

// guardScreen is a stub destination with a leave guard. While
// hold is true, it refuses every departure, records the target, and
// shows a message, like a screen that asks about an unsaved draft.
type guardScreen struct {
	stubScreen

	hold    bool
	refused []navigateMsg
}

func (s *guardScreen) leave(next destination, intent string) (tea.Cmd, bool) {
	if !s.hold {
		return nil, true
	}

	s.refused = append(s.refused, navigateMsg{dest: next, intent: intent})

	return toast(ToneAttention, "Keep or discard the draft first."), false
}

func TestShellLeaveGuardKeepsTheDestination(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 32)
	guard := &guardScreen{hold: true}
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
