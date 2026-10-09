package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// TestCapturePins writes the Pins scenes (70 to 79) at every capture
// size into SEAMARK_TUI_CAPTURE_DIR, for the maintainer visual review.
// It checks that every frame fits its terminal. It also checks the raw
// frame for control bytes, because a capture file strips them, and
// every ASCII scene for non-ASCII runes (D-24).
func TestCapturePins(t *testing.T) {
	dir := captureDir()
	snap := captureSettings(t)

	for _, sc := range pinsCaptureScenes() {
		for _, size := range captureSizes {
			w, h := size[0], size[1]

			f := sc.fake()
			f.settings = snap

			hs := &harness{t: t, m: newModel(f, ui.NewTheme(sc.ascii))}
			hs.run(hs.m.Init())
			hs.send(teaSize(w, h))
			hs.press("3", "m")
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

// pinsCaptureScenes lists the Pins scenes. They show the populated list
// and the details of a linked, an edited and an ambiguous pin. They also
// show a label with every attention word, both history filters, the
// comparison, the filters, the malformed file, the empty states, and
// ASCII variants. Each scene starts on Lessons › Pins.
func pinsCaptureScenes() []captureScene {
	over := func(in func() *maintain.Inspection) func() *fakeServices {
		return func() *fakeServices {
			f := pinsFake()
			f.pins = in()

			return f
		}
	}

	// detail shows the detail of row n: beside the list in the wide
	// layout with the focus on it, alone in the compact layout. It pages
	// down at least pages times and then until the anchor text shows, so
	// every size shows the same part of the detail.
	detail := func(n, pages int, anchor string) func(h *harness) {
		return func(h *harness) {
			pinsCursorTo(h, n)

			if h.m.st.width >= ui.WideWidth {
				h.press("tab")
			} else {
				h.press("enter")
			}

			// The detail records its page size when it renders, as in the
			// real loop, so each page key follows a render.
			for i := range 20 {
				if i >= pages && strings.Contains(h.view(), anchor) {
					return
				}

				h.view()
				h.press("pgdown")
			}
		}
	}

	// keys presses named keys and single runes, and types any other
	// text into the focused input.
	keys := func(ks ...string) func(h *harness) {
		named := map[string]bool{"enter": true, "down": true, "space": true, "esc": true, "tab": true}

		return func(h *harness) {
			for _, k := range ks {
				if len(k) > 1 && !named[k] {
					h.typeText(k)

					continue
				}

				h.press(k)
			}
		}
	}

	none := func(*harness) {}

	syntax := func() *maintain.Inspection {
		in := fakePins()
		in.Doc.Guard.Exists = true
		in.Doc.Bytes = []byte("pin:\n  - rule: a\n    note: b: c\n")
		in.Doc.DecodeErr = errors.New("yaml: line 3: mapping values are not allowed in this context")
		in.Doc.ErrLine = 3
		in.LinkErr = pinfile.ErrUnreadable
		in.Links = &pinfile.Links{NotPresentErr: pinfile.ErrUnreadable}

		return in
	}

	// flags gives p9 every attention word, so the list shows how a long
	// label fits its column.
	flags := func() *maintain.Inspection {
		in := pinsInspection()
		in.Pins[1].Entry.Unknown = []string{"regoin"}
		in.Pins[1].Entry.Block = "a multi-line rule value"

		return in
	}

	// notLanding gives p9 an outcome that is not landing (AC-3). The
	// reading is advice, so the keys stay those of a working pin.
	notLanding := func() *maintain.Inspection {
		in := pinsInspection()
		in.Pins[1].Readings.Outcome = maintain.OutcomeReading{Known: true, Reading: outcome.Reading{
			Exposed: true, Firings: 4, Matches: 4, PreEvents: 3, PreCommits: 10, PostEvents: 2, PostCommits: 6,
			Verdict: outcome.VerdictNotLanding,
		}}
		in.Pins[1].Readings.Exposure = maintain.ExposureReading{
			Known: true, Fired: true, Exposure: reviews.Exposure{Count: 4, Matches: 4},
		}

		return in
	}

	// foreign is the inspection of a copied checkout whose index records
	// the original repository.
	foreign := func() *maintain.Inspection {
		in := pinsInspection()
		in.Index.Root = "/work/original"

		return in
	}

	return []captureScene{
		{name: "70-pins-list", fake: pinsFake, drive: none},
		{name: "70b-pins-list-ascii", fake: over(pinsASCIIInspection), ascii: true, drive: none},
		{name: "70c-pins-list-every-flag", fake: over(flags), drive: none},
		// Pin 1 has current regions, an answered trigger question and no
		// similar pin. So w, t and v are unavailable: the footer and the
		// help list them as n/a, and w shows its reason.
		{name: "70d-pins-help", fake: pinsFake, drive: keys("?")},
		{name: "70e-pins-help-ascii", fake: over(pinsASCIIInspection), ascii: true, drive: keys("?")},
		{name: "70f-pins-unavailable-retarget", fake: pinsFake, drive: keys("w")},
		{name: "71-pins-linked-detail", fake: pinsFake, drive: detail(1, 0, "")},
		{name: "71b-pins-linked-readings", fake: pinsFake, drive: detail(1, 1, "Outcome")},
		{name: "72-pins-note-changed", fake: pinsFake, drive: detail(2, 0, "")},
		{name: "72b-pins-note-changed-scope", fake: pinsFake, drive: detail(2, 1, "broader:")},
		{name: "72c-pins-retarget-collision", fake: over(pinsLiveCollision), drive: detail(2, 2, "No retarget")},
		{name: "72d-pins-not-landing", fake: over(notLanding), drive: detail(2, 1, "not a reason to prune")},
		{name: "73-pins-ambiguous", fake: pinsFake, drive: detail(9, 0, "")},
		{name: "73c-pins-duplicate", fake: pinsFake, drive: detail(7, 0, "")},
		{name: "73b-pins-blocked-hostile", fake: pinsFake, drive: detail(10, 0, "")},
		{name: "74-pins-not-present", fake: pinsFake, drive: keys("f")},
		{name: "75-pins-superseded", fake: pinsFake, drive: keys("f", "f")},
		{name: "76-pins-compare", fake: pinsFake, drive: keys("down", "down", "v")},
		{name: "76b-pins-compare-ascii", fake: over(pinsASCIIInspection), ascii: true, drive: keys("down", "down", "v")},
		{name: "77-pins-filtered", fake: pinsFake, drive: keys("/", "internal/db", "enter", "z", "close", "enter", "down", "space")},
		{name: "77c-pins-filter-clears-selection", fake: pinsFake, drive: keys("down", "space", "/", "internal/db", "enter")},
		{name: "77b-pins-search-input", fake: pinsFake, drive: keys("z", "database")},
		{name: "78-pins-malformed", fake: over(pinsMalformed), drive: none},
		{name: "78b-pins-syntax-error", fake: over(syntax), drive: none},
		{name: "78c-pins-malformed-detail", fake: over(pinsMalformed), drive: detail(1, 0, "")},
		{name: "78d-pins-foreign-root", fake: over(foreign), drive: detail(2, 2, "seamark index")},
		{name: "79-pins-no-file", fake: over(fakePins), drive: none},
		{name: "79b-pins-no-ledger", fake: over(pinsNoLedger), drive: none},
		{name: "79c-pins-no-ledger-missing", fake: over(pinsNoLedger), drive: keys("f")},
		{name: "79d-pins-every-pin-present", fake: over(fakePins), drive: keys("f")},
	}
}

// TestCapturePinsScenesAreNamed keeps the scene list in its range:
// numbers 70 to 79 with letter variants, and no name twice. It writes
// nothing, so it runs without the capture directory.
func TestCapturePinsScenesAreNamed(t *testing.T) {
	seen := map[string]bool{}

	for _, sc := range pinsCaptureScenes() {
		require.False(t, seen[sc.name], "scene %s is listed twice", sc.name)
		seen[sc.name] = true

		n, err := strconv.Atoi(sc.name[:2])
		require.NoError(t, err, sc.name)
		assert.GreaterOrEqual(t, n, 70, sc.name)
		assert.LessOrEqual(t, n, 79, sc.name)
		assert.True(t, strings.Contains(sc.name, "pins"), sc.name)
	}
}
