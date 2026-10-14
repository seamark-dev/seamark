package tui

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/pinfile/pinfiletest"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// The Phase 4 benchmarks of the console screens (make tui-measure). They
// drive the console through the harness at the reference size, 120×40,
// over generated fixtures of pinfiletest. Each sub-benchmark builds its
// fixture before the timed loop, so a plain go test run builds nothing.
// A frame is h.m.View(), the text that Bubble Tea draws, without the
// escape removal of h.view().

// pinsScale returns fake services whose Pins read gives the inspection
// of a generated pin file of size s. The real reader and classifier
// build it (maintInspectionOf), with s.Applied applied rows. Pin 0 holds
// a note of s.Note runes, and the cursor starts on it, so the detail
// pane wraps the long note.
func pinsScale(b *testing.B, s pinfiletest.Size) *fakeServices {
	b.Helper()

	pins := pinfiletest.Pins(s.Pins, "src", pinfiletest.Note(s.Note))
	applied := pinfiletest.Applied(pins, s.Applied)

	f := newFake()
	f.ledger = &proposals.Ledger{Applied: applied, Write: true}
	f.pins = maintInspectionOf(b, []byte(pinfiletest.Lessons(b, pins, nil)), applied, nil)

	return f
}

// BenchmarkPhase4Pins measures Lessons › Pins. step=load is one read of
// the pins with one open edit draft of pin 0: the refresh, the services
// read, the rebuilt list, and the rebind of the draft. step=view is one
// frame without a draft.
func BenchmarkPhase4Pins(b *testing.B) {
	for _, s := range pinfiletest.Sweep(pinfiletest.DimPins, pinfiletest.DimApplied, pinfiletest.DimNote) {
		b.Run(s.Name(), func(b *testing.B) {
			f := pinsScale(b, s)

			b.Run("step=load", func(b *testing.B) {
				h := pinsOpen(b, f, 120, 40)

				// One edit draft changes the long note of pin 0. A changed
				// draft is unsaved work, so every reload rebinds it. The
				// rebind builds the note area again from the entry
				// (pinDraft.follow), so the note size reaches the load.
				pinsCursorTo(h, 1)
				h.press("i", "down", "enter", "end")
				h.typeText(" x")
				h.press("esc")

				d := authorOf(h).form.draft
				require.True(b, d.dirty(), "the draft holds a changed note")

				for b.Loop() {
					authorReload(h, f, f.pins)
				}

				require.Same(b, d, authorOf(h).edits[d.ref], "every reload rebinds the draft of pin 0")
			})

			b.Run("step=view", func(b *testing.B) {
				h := pinsOpen(b, f, 120, 40)

				for b.Loop() {
					if h.m.View() == "" {
						b.Fatal("the frame is empty")
					}
				}
			})
		})
	}
}

// BenchmarkPhase4Settings measures one frame of the lessons Settings
// groups with long mute lists. layer=group is the lesson mutes group
// with its detail; layer=editor is the open mute editor with one row per
// entry. The workspace holds a real lessons.yaml with the pins of Base,
// so the settings snapshot is a real inspection (lessonsSnapFake).
func BenchmarkPhase4Settings(b *testing.B) {
	for _, s := range pinfiletest.Sweep(pinfiletest.DimMutes) {
		b.Run(s.Name(), func(b *testing.B) {
			text := pinfiletest.Lessons(b, pinfiletest.Pins(s.Pins, "src", ""), pinfiletest.Mutes(s.Mutes))

			for _, layer := range []string{"group", "editor"} {
				b.Run("layer="+layer, func(b *testing.B) {
					h := lessonsOpen(b, lessonsSnapFake(b, text), "group:mutes", 120, 40)

					if layer == "editor" {
						h.press("enter")
						require.True(b, settingsScr(h).muteEditing(), "enter opens the mute editor")
					}

					for b.Loop() {
						if h.m.View() == "" {
							b.Fatal("the frame is empty")
						}
					}
				})
			}
		})
	}
}
