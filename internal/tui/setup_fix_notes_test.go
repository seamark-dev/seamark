package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupWideReviewNotesScrollWithTab proves that the plan notes of the
// wide review stay reachable when they do not fit: tab moves the
// movement keys to the notes, and tab again gives them back to the files.
func TestSetupWideReviewNotesScrollWithTab(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 22)
	h.press("enter")

	wz := setupScreen(h).wizard
	require.NotNil(t, wz)
	require.True(t, wz.previewWide())

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

	cursor := wz.paths.cursor
	h.press("tab", "down")
	assert.False(t, wz.notesFocus)
	assert.NotEqual(t, cursor, wz.paths.cursor, "the files take the keys again")
}
