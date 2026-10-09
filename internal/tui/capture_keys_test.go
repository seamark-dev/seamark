package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// TestCaptureKeys writes the help scenes of the key discoverability
// change at every capture size into SEAMARK_TUI_CAPTURE_DIR, for the
// maintainer visual review. It checks that every frame fits its
// terminal. It also checks the raw frame for control bytes, because a
// capture file strips them, and every ASCII scene for non-ASCII runes
// (D-24).
//
// Each scene adds a letter to the earlier scene that it extends, so the
// help shows beside the state that it describes. The Pins help scenes
// stay in the Pins file (70d, 70e).
func TestCaptureKeys(t *testing.T) {
	dir := captureDir()
	snap := captureSettings(t)

	for _, sc := range keysCaptureScenes() {
		for _, size := range captureSizes {
			w, h := size[0], size[1]

			f := sc.fake()
			f.settings = snap

			hs := &harness{t: t, m: newModel(f, ui.NewTheme(sc.ascii))}
			hs.run(hs.m.Init())
			hs.send(teaSize(w, h))
			sc.drive(hs)

			frame := hs.view()
			assertFrame(t, frame, w, h)

			for _, b := range []string{"\x1b]", "\x07", "\u009b", "\x1b[2J", "\x1b[31m"} {
				assert.NotContains(t, hs.m.View(), b, "scene %s at %dx%d holds the control bytes %q", sc.name, w, h, b)
			}

			if sc.ascii {
				for _, r := range frame {
					require.Less(t, r, rune(128), "scene %s at %dx%d holds %q", sc.name, w, h, r)
				}
			}

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}

// keysCaptureScenes lists the help scenes. 12b is the help of the
// Proposals list of scene 12, before a mark. The help lists every
// Proposals action and marks x and c n/a (user decision of 2026-10-04:
// help only). It has no esc row, because no run filter is on (a second
// user decision of 2026-10-04). The footer under the help is the help
// footer, so the Proposals footer of scene 12 stays as in Phase 2.
func keysCaptureScenes() []captureScene {
	proposalsList := func() *fakeServices {
		f := newFake()
		f.ledger = lesLedger()

		return f
	}

	// toLast opens the help and scrolls down one row at a time until the
	// last row of the screen group is in view. A compact help shows the
	// n/a rows only after a scroll, and the scene must show them at every
	// size. One row per step stops with that row at the bottom, so the
	// window also shows the n/a label and the keys that work above it.
	toLast := func(last string) func(h *harness) {
		return func(h *harness) {
			h.press("3", "?")

			for range 60 {
				if strings.Contains(h.view(), last) {
					return
				}

				h.press("down")
			}
		}
	}

	return []captureScene{
		{name: "12b-proposals-help", fake: proposalsList, drive: toLast("record applied")},
	}
}
