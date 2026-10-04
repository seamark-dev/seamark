package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
)

// Review fix (Phase C, trial e2e-maintenance frames 18 to 20): the
// Settings texts of distill.write named only the proposal preview. The
// Pins retarget and prune read the same gate (D-14), and the user
// reaches this screen with o from a retarget preview. Every text on
// that path must name the three previews.
func TestSettingsPinWriteTextsNameRetargetAndPrune(t *testing.T) {
	fx := maintFixture(t)
	svc := settingsFake(t, "distill:\n  write: false\n")
	svc.ledger = lesLedger()
	svc.pins = fx.inspection()
	svc.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
		return fx.maintNarrow(t, maintPlanOpts{cursor: true}), nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "m", "w")
	require.True(t, maintHasKey(h, "o"), "the gate is off, so the retarget preview offers o")

	h.press("o")
	require.Equal(t, destSettings, h.m.active)

	var frames []string

	// The detail of the field: what it controls, the choices and when a
	// save takes effect.
	frame := h.view()
	frames = append(frames, frame)

	detail := settingsBox(frame, "distill.write")
	for _, want := range []string{
		"Lets a reviewed apply, retarget or prune write pins.",
		"off previews show the change only",
		"on y in a preview writes the pins",
		"Saving on writes no pin; a new apply, retarget or prune preview then offers the write.",
		"TAKES EFFECT At the next apply, retarget or prune preview. Saving writes no pin; a new preview then " +
			"offers the write.",
	} {
		assert.Contains(t, detail, want)
	}

	// The save preview: WON'T at the top, TAKES EFFECT at the end.
	h.press("enter", "down", "space", "enter", "s")

	frame = h.view()
	frames = append(frames, frame)
	assert.Contains(t, settingsBox(frame, "Save"),
		"write a pin: a new apply, retarget or prune preview offers the write")

	h.press("end")

	frame = h.view()
	frames = append(frames, frame)

	save := settingsBox(frame, "Save")
	assert.Contains(t, save, "TAKES EFFECT distill.write: at the next apply, retarget or prune preview.")
	assert.Contains(t, save, "Saving writes no pin; only a new preview offers the write.")

	// The toast of the save.
	h.press("y")
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	frame = h.view()
	frames = append(frames, frame)
	assert.Contains(t, frame, "Configuration updated: distill.write. It takes effect at the next apply, "+
		"retarget or prune preview.")

	for i, f := range frames {
		assert.NotContains(t, f, "proposal preview", "frame %d names only the proposal preview", i)
		assert.NotContains(t, f, "proposal actions", "frame %d names only the proposal actions", i)
	}
}
