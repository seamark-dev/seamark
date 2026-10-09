package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the index build modal. Enter closes a finished result
// and starts nothing, and esc still closes it. Enter never hides a
// running build, so the progress stays in view. A body taller than the
// area gives up its last lines, and the key hints stay visible.

// ovHelpKeys returns the help keys and texts of the Overview footer.
func ovHelpKeys(bindings []key.Binding) map[string]string {
	out := map[string]string{}

	for _, b := range bindings {
		out[b.Help().Key] = b.Help().Desc
	}

	return out
}

func TestOverviewBuildResultClosesWithEnter(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)

	h.press("b", "enter")
	require.Contains(t, h.view(), "Index built")
	assert.Contains(t, h.view(), "enter close   b build again", "the result modal names its keys")

	help := ovHelpKeys(ovScreen(h).keys())
	assert.Equal(t, "close", help["enter"], "the result labels enter as close, like the other results")
	assert.NotContains(t, help, "esc", "one close key is advertised")

	h.press("enter")

	frame := h.view()
	assert.NotContains(t, frame, "Index built")
	assert.Contains(t, frame, "ACTION")
	assert.Equal(t, []string{"BuildIndex"}, f.called(), "closing the result starts nothing")
}

func TestOverviewBuildResultStillClosesWithEsc(t *testing.T) {
	f := ovPopulated()
	f.buildIndex = func(bool, func(services.IndexEvent)) (*index.Summary, error) {
		return nil, errors.New("database is locked")
	}

	h := newHarness(t, f, 80, 24)

	h.press("b", "enter")
	require.Contains(t, h.view(), "Index build failed")
	assert.Contains(t, h.view(), "enter close")

	// The status line keeps the failure toast, so the panel state and
	// the list show that the result closed.
	h.press("esc")
	assert.False(t, ovScreen(h).showBuild)
	assert.Contains(t, h.view(), "ACTION")
	assert.Equal(t, []string{"BuildIndex"}, f.called(), "a failure never retries")
}

func TestOverviewRunningBuildIgnoresEnter(t *testing.T) {
	f := ovPopulated()

	var h *harness

	f.buildIndex = func(bool, func(services.IndexEvent)) (*index.Summary, error) {
		h.press("enter")
		assert.True(t, ovScreen(h).showBuild, "enter never hides a running build")
		assert.Empty(t, ovScreen(h).keys(), "a running build offers no key")

		return &index.Summary{}, nil
	}

	h = newHarness(t, f, 120, 40)
	h.press("b", "enter")

	assert.Contains(t, h.view(), "Index built")
	assert.Equal(t, []string{"BuildIndex"}, f.called())
}

// TestOverviewModalBoxClipsTheBodyAboveTheHints proves the clip rule of
// the build modal at the minimum size, in both glyph sets. The modal
// does not scroll, so the note above the hints is the only sign of the
// hidden lines.
func TestOverviewModalBoxClipsTheBodyAboveTheHints(t *testing.T) {
	body := make([]string, 0, 30)
	for i := range 30 {
		body = append(body, "body line "+strconv.Itoa(i+1))
	}

	for _, tc := range []struct {
		ascii bool
		note  string
	}{
		{ascii: false, note: "more below — enlarge the terminal"},
		{ascii: true, note: "more below - enlarge the terminal"},
	} {
		th := ui.NewTheme(tc.ascii)
		o := newOverview(&state{theme: th, width: ui.MinWidth, height: ui.MinHeight})
		h := ui.MinHeight - chromeHeight

		out := ansi.Strip(o.modalBox("Build the index?", "", body, "enter build   esc cancel", ui.MinWidth, h))
		lines := strings.Split(out, "\n")

		require.Len(t, lines, h, "ascii %v: the modal fills the area and no more", tc.ascii)

		for _, l := range lines {
			require.LessOrEqual(t, lipgloss.Width(l), ui.MinWidth, "ascii %v: line is too wide: %q", tc.ascii, l)
		}

		assert.Contains(t, out, "body line 1", "ascii %v: the body keeps its first lines", tc.ascii)
		assert.NotContains(t, out, "body line 30", "ascii %v: the body gives up its last lines", tc.ascii)
		assert.Contains(t, out, tc.note, "ascii %v: the clip note is missing", tc.ascii)

		// The note ends the body. A blank row and the rule follow, then
		// the hints and the bottom border.
		note, hints := lineIndex(lines, "more below"), lineIndex(lines, "enter build")
		assert.Equal(t, note+3, hints, "ascii %v: the note does not end the body", tc.ascii)
		assert.Equal(t, len(lines)-2, hints, "ascii %v: the hints are not the last row", tc.ascii)
		assert.Contains(t, lines[len(lines)-1], th.Glyphs.CornerBL, "ascii %v: the bottom border is clipped", tc.ascii)

		if tc.ascii {
			for _, r := range out {
				require.Less(t, r, rune(128), "the ASCII modal holds %q", r)
			}
		}
	}
}

// TestOverviewModalBoxShowsAShortBodyWhole proves the other side of the
// clip rule: a body that fits shows every line and no clip note.
func TestOverviewModalBoxShowsAShortBodyWhole(t *testing.T) {
	o := newOverview(&state{theme: ui.NewTheme(false), width: ui.MinWidth, height: ui.MinHeight})

	out := ansi.Strip(o.modalBox("Build the index?", "", []string{"one", "two", ""}, "enter build", ui.MinWidth, ui.MinHeight-chromeHeight))

	assert.Contains(t, out, "one")
	assert.Contains(t, out, "two")
	assert.Contains(t, out, "enter build")
	assert.NotContains(t, out, "more below")
	assert.Len(t, strings.Split(out, "\n"), 2+2+3, "the border, two body lines, and the blank row, rule and hints")
}
