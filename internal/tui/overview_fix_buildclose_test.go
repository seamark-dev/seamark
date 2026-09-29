package tui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
)

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
	f.buildIndex = func(bool, func(IndexEvent)) (*index.Summary, error) {
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

	f.buildIndex = func(bool, func(IndexEvent)) (*index.Summary, error) {
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
