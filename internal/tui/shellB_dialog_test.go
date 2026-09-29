package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellBLongBody is a dialog body that cannot fit at the minimum size:
// blocks with blank lines between them, like the help overlay.
func shellBLongBody() string {
	blocks := make([]string, 0, 12)

	for i := range 12 {
		blocks = append(blocks, "Block "+itoa(i+1)+" line one\nBlock "+itoa(i+1)+" line two")
	}

	return strings.Join(blocks, "\n\n\n")
}

// shellBDialog renders a dialog with the long body in plain text.
func shellBDialog(t *Theme, w, h int) string {
	return ansi.Strip(t.dialog("Long dialog title", shellBLongBody(),
		t.actions(t.action("y", "Apply", true), t.action("esc", "Back", false)), w, h))
}

// TestShellBDialogKeepsTitleAndActionsAtSmallHeights proves finding 3/9:
// a dialog taller than the area keeps its title, its action row and its
// bottom border, and says that the body is clipped.
func TestShellBDialogKeepsTitleAndActionsAtSmallHeights(t *testing.T) {
	th := newTheme(false)
	innerW := minWidth - 2

	for _, h := range []int{minHeight - chromeHeight, 12, 9, 7, 6} {
		out := shellBDialog(th, innerW, h)
		lines := strings.Split(out, "\n")

		require.LessOrEqual(t, len(lines), h, "height %d: the dialog is taller than the area", h)

		for _, l := range lines {
			require.LessOrEqual(t, lipgloss.Width(l), innerW, "height %d: line is too wide: %q", h, l)
		}

		assert.Contains(t, out, "Long dialog title", "height %d: the title is hidden", h)
		assert.Contains(t, out, "y Apply", "height %d: the action row is hidden", h)
		assert.Contains(t, out, "esc Back", "height %d: the action row is hidden", h)
		assert.Contains(t, out, "╰", "height %d: the bottom border is clipped", h)
		assert.Contains(t, out, "more below — enlarge the terminal", "height %d: the clip note is missing", h)

		// The note sits directly above the action row.
		note, acts := shellBLineIndex(lines, "more below"), shellBLineIndex(lines, "y Apply")
		assert.Equal(t, note+1, acts, "height %d: the note is not directly above the actions", h)
	}
}

// TestShellBDialogCompactsBeforeItClips proves the order of the steps:
// padding and blank lines go before any body line.
func TestShellBDialogCompactsBeforeItClips(t *testing.T) {
	th := newTheme(false)
	body := "one\n\n\n\ntwo\n\nthree"
	acts := th.action("enter", "OK", true)

	// Full layout: 2 border + 2 padding + title + gap + 7 body + gap + actions.
	full := ansi.Strip(th.dialog("Title", body, acts, 58, 30))
	assert.Equal(t, 15, shellBBoxHeight(full))

	// Without padding and with one blank line per run the box has 11
	// rows, so the whole body stays and no note appears.
	compact := ansi.Strip(th.dialog("Title", body, acts, 58, 11))
	assert.Equal(t, 11, shellBBoxHeight(compact))
	assert.Contains(t, compact, "three")
	assert.NotContains(t, compact, "more below")

	// One row less removes the last blank line; the upper one stays.
	partial := strings.Split(ansi.Strip(th.dialog("Title", body, acts, 58, 10)), "\n")
	assert.Equal(t, 10, shellBBoxHeight(strings.Join(partial, "\n")))

	one := shellBLineIndex(partial, "one")
	assert.Empty(t, strings.Trim(partial[one+1], " │"), "the blank line under the lead text stays")
	assert.Equal(t, shellBLineIndex(partial, "two")+1, shellBLineIndex(partial, "three"))

	// Without any blank body line the box has 9 rows.
	tight := ansi.Strip(th.dialog("Title", body, acts, 58, 9))
	assert.Equal(t, 9, shellBBoxHeight(tight))
	assert.Contains(t, tight, "three")
	assert.NotContains(t, tight, "more below")

	// One row less clips the body: two body lines, the note, the actions.
	clipped := ansi.Strip(th.dialog("Title", body, acts, 58, 8))
	assert.Equal(t, 8, shellBBoxHeight(clipped))
	assert.Contains(t, clipped, "two")
	assert.NotContains(t, clipped, "three")
	assert.Contains(t, clipped, "more below")
	assert.Contains(t, clipped, "enter OK")
}

// TestShellBDialogWidthStaysInsideTheArea proves the width cap: the box
// is never wider than the area and its text keeps a positive width.
func TestShellBDialogWidthStaysInsideTheArea(t *testing.T) {
	th := newTheme(false)

	for _, w := range []int{4, 10, 20, 40, 52, 58, 80, 118, 200} {
		assert.LessOrEqual(t, dialogWidth(w), w, "width %d", w)
		assert.GreaterOrEqual(t, dialogInner(w), 1, "width %d", w)

		out := ansi.Strip(th.dialog("A title that is long enough to be cut", "body text", th.action("esc", "Close", true), w, 12))
		for _, l := range strings.Split(out, "\n") {
			require.LessOrEqual(t, lipgloss.Width(l), max(w, dialogMinBox), "width %d: %q", w, l)
		}
	}

	assert.Equal(t, 48, dialogWidth(58), "the minimum-size box keeps its old width")
	assert.Equal(t, dialogMaxBox, dialogWidth(200))
}

// TestShellBDialogNoteIsASCIIInTheASCIITheme proves that the clip note
// uses the ASCII dash in the ASCII theme.
func TestShellBDialogNoteIsASCIIInTheASCIITheme(t *testing.T) {
	out := shellBDialog(newTheme(true), minWidth-2, 9)

	assert.Contains(t, out, "more below - enlarge the terminal")

	for _, r := range out {
		require.Less(t, r, rune(128), "ASCII dialog holds %q", r)
	}
}

// TestShellBOverlaysFitAtTheMinimumSize proves finding 3/9 through the
// shell: at 60x20 the first-run guide and the quit dialog keep their
// actions and their bottom border.
func TestShellBOverlaysFitAtTheMinimumSize(t *testing.T) {
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

// TestShellBMinimumSizeIs60x20 proves the raised minimum height.
func TestShellBMinimumSizeIs60x20(t *testing.T) {
	h := newHarness(t, newFake(), 60, 19)
	assert.Contains(t, h.view(), "More space needed")

	h.resize(59, 20)
	assert.Contains(t, h.view(), "More space needed")

	h.resize(60, 20)
	assert.NotContains(t, h.view(), "More space needed")
}

// shellBLineIndex returns the index of the first line that holds s.
func shellBLineIndex(lines []string, s string) int {
	for i, l := range lines {
		if strings.Contains(l, s) {
			return i
		}
	}

	return -1
}

// shellBBoxHeight counts the rows from the top border to the bottom
// border of a rendered dialog.
func shellBBoxHeight(out string) int {
	lines := strings.Split(out, "\n")

	return shellBLineIndex(lines, "╰") - shellBLineIndex(lines, "╭") + 1
}
