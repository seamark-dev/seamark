package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/settings"
)

// pinWriteScope is the scope of distill.write that every text of the
// gate names (spec R-5, contracts §4.3). The test spells it out, so a
// change of the wording fails here and in the engine together.
const pinWriteScope = "apply, retarget, prune, and new pins, pin edits, and pin removals in Lessons › Pins"

// Review fix (Phase C, trial e2e-maintenance frames 18 to 20): the
// Settings texts of distill.write named only the proposal preview. The
// Pins retarget and prune read the same gate (D-14), and the user
// reaches this screen with o from a retarget preview. Phase 4 (R-5)
// adds the pin authoring of Lessons › Pins to the gate. So every text
// on that path names the whole scope, and none names only a part.
func TestSettingsPinWriteTextsNameTheWholeScope(t *testing.T) {
	require.Equal(t, pinWriteScope, settings.PinWriteScope, "the engine owns the one phrase")

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
		"Lets a reviewed preview write pins: " + pinWriteScope + ".",
		"off previews show the change only",
		"on y in a preview writes the pins",
		"Saving on writes no pin; a new preview then offers the write: " + pinWriteScope + ".",
		"TAKES EFFECT At the next pin-write preview: " + pinWriteScope + ". Saving writes no pin; a new " +
			"preview then offers the write.",
	} {
		assert.Contains(t, detail, want)
	}

	// The save preview: WON'T at the top, TAKES EFFECT at the end.
	h.press("enter", "down", "space", "enter", "s")

	frame = h.view()
	frames = append(frames, frame)
	assert.Contains(t, settingsBox(frame, "Save"), "write a pin: a new preview offers the write ("+pinWriteScope+")")

	h.press("end")

	frame = h.view()
	frames = append(frames, frame)

	save := settingsBox(frame, "Save")
	assert.Contains(t, save, "TAKES EFFECT distill.write: at the next pin-write preview: "+pinWriteScope+".")
	assert.Contains(t, save, "Saving writes no pin; only a new preview offers the write.")

	// The toast of the save.
	h.press("y")
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	frames = append(frames, h.view())
	assert.Equal(t, "Configuration updated: distill.write. It takes effect at the next pin-write preview: "+
		pinWriteScope+".", h.m.toast.text)

	for i, f := range frames {
		flat := lesFlat(f)
		assert.NotContains(t, flat, "proposal preview", "frame %d names only the proposal preview", i)
		assert.NotContains(t, flat, "proposal actions", "frame %d names only the proposal actions", i)
		assert.NotContains(t, flat, "retarget or prune", "frame %d names a part of the scope", i)
	}
}

// Review fix B3-CR-2: the Overview fact of pin writing named only the
// apply. Every pin write reads the gate, so the fact uses the words of
// the Settings choices, which name no single action.
func TestOverviewPinWritingFactNamesNoSingleAction(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"distill:\n  write: true\n", "Pin writing on · " + settingsPinWriteOnNote},
		{"distill:\n  write: false\n", "Pin writing off · " + settingsPinWriteOffNote},
	} {
		f := ovPopulated()
		f.settings = fakeSettingsSnapshot(t, c.file)

		// The summary item holds every fact in the compact layout.
		h := newHarness(t, f, 80, 24)
		ovOpenSummary(t, h)

		flat := ovFlat(ovReadDetail(t, h, 80, 24))
		assert.Contains(t, flat, c.want)
		assert.NotContains(t, flat, "apply writes pins", "the fact names a part of the scope")
		assert.NotContains(t, flat, "apply shows the YAML block", "the fact names a part of the scope")
	}
}
