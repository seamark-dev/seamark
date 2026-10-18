package tui

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The capture series. The scenes of the MVP to Phase 3 have a code of two
// digits and an optional variant letter, such as 70 or 70b. Each capture
// file owns a range of numbers, and the key help scenes add a letter to
// the scene that they extend, such as 12b. The Phase 4 scenes have a code
// of a series letter, a digit, and an optional variant letter, such as A4
// or A4b (P4-14). The letter names the series, and contracts §7 maps
// every code to its state. A frame name is the scene name and the size,
// so a code that two scenes share would put two states under one name.

// phase4SceneMap is the scene map of contracts §7 (C2): every Phase 4
// code and the state that its scenes show.
var phase4SceneMap = map[string]string{
	"A0": "empty new form",
	"A1": "filled multi-region multiline form",
	"A2": "unlinked edit",
	"A3": "linked edit with read-only rows",
	"A4": "create review and diff",
	"A5": "identity-changed review",
	"A6": "remove review",
	"A7": "gate-off and no-index blockers",
	"A8": "results (written, unknown, saved-refresh-failed)",
	"A9": "stale draft",
	"B0": "mute list (no-op, duplicate, overlap)",
	"B1": "record form and match text",
	"B2": "mute review",
	"C0": "delivery stored/default/effective",
	"C1": "number editor with overflow",
	"C2": "mode help with capabilities",
	"C3": "delivery review",
	"D0": "hook sample",
	"D1": "change-set sample",
	"D2": "unavailable states",
	"D3": "mode-only note",
}

// phase4Series returns the scene list of each Phase 4 series letter: A
// Pins authoring, B lesson mutes, C lesson delivery, D the selection
// sample.
func phase4Series() map[string][]phase4Scene {
	return map[string][]phase4Scene{
		"A": authorCaptureScenes(),
		"B": muteCaptureScenes(),
		"C": deliveryCaptureScenes(),
		"D": sampleCaptureScenes(),
	}
}

// TestCapturePhase4 writes the Phase 4 scenes of contracts §7 in both
// glyph sets at every capture size into SEAMARK_TUI_CAPTURE_DIR, for the
// maintainer visual review. It renders every list of phase4Series, and
// the series guard reads the same lists. So a list that the guard checks
// is always a list that the capture writes.
func TestCapturePhase4(t *testing.T) {
	series := phase4Series()

	for _, letter := range slices.Sorted(maps.Keys(series)) {
		capturePhase4(t, series[letter])
	}
}

// phase4Scene is one Phase 4 capture scene. open starts a console in a
// glyph set and at a size, and drives it into the state. want is a
// phrase that every frame of the scene shows, so a key that misses its
// state fails the capture instead of saving another screen.
type phase4Scene struct {
	name string
	want string
	open func(t *testing.T, ascii bool, w, hgt int) *harness
}

// phase4Frames returns the frame names of a scene without the size: the
// scene in the Unicode glyph set and its ASCII variant. Every Phase 4
// scene has both (contracts §7), so the variant is not a scene of its
// own.
func phase4Frames(sc phase4Scene) []string {
	return []string{sc.name, sc.name + "-ascii"}
}

// phase4OnPins returns the start of a scene on Lessons › Pins over the
// services of fake. The settings snapshot is the one of a workspace
// without config.yaml, as in TestCapturePins.
func phase4OnPins(fake func() *fakeServices, drive func(h *harness)) func(*testing.T, bool, int, int) *harness {
	return func(t *testing.T, ascii bool, w, hgt int) *harness {
		t.Helper()

		f := fake()
		f.settings = captureSettings(t)

		h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
		h.run(h.m.Init())
		h.send(teaSize(w, hgt))
		h.press("3", "m")
		drive(h)

		return h
	}
}

// phase4OnSettings returns the start of a scene on Settings over the
// real settings package. The workspace of captureRoot holds lessons as
// its lessons.yaml and no config.yaml; an empty lessons leaves the file
// absent, as lessonsFake does. setup changes the services before the
// console reads them, for example the client inspections.
func phase4OnSettings(lessons string, setup func(svc *settingsSvc),
	drive func(h *harness),
) func(*testing.T, bool, int, int) *harness {
	return func(t *testing.T, ascii bool, w, hgt int) *harness {
		t.Helper()

		root := captureRoot(t)
		if lessons != "" {
			lessonsWrite(t, root, lessons)
		}

		svc := settingsFakeAt(t, root, "")
		if setup != nil {
			setup(svc)
		}

		h := &harness{t: t, m: newModel(svc, ui.NewTheme(ascii))}
		h.run(h.m.Init())
		h.send(teaSize(w, hgt))
		h.press("5")
		drive(h)

		return h
	}
}

// capturePhase4 writes the frames of scenes in both glyph sets at every
// capture size into SEAMARK_TUI_CAPTURE_DIR. Every frame must fit its
// terminal and show the phrase of its scene. The raw view must hold no
// control byte, because a capture file strips them, and an ASCII frame
// must hold ASCII only (D-24, R-14).
func capturePhase4(t *testing.T, scenes []phase4Scene) {
	t.Helper()

	dir := captureDir()

	for _, sc := range scenes {
		for i, name := range phase4Frames(sc) {
			ascii := i == 1

			for _, size := range captureSizes {
				w, hgt := size[0], size[1]

				h := sc.open(t, ascii, w, hgt)
				extAssertInert(t, h.m.View(), name, w, hgt)

				frame := h.view()
				assertFrame(t, frame, w, hgt)
				assert.Contains(t, frame, sc.want, "scene %s at %dx%d", name, w, hgt)

				if ascii {
					for _, r := range frame {
						require.Less(t, r, rune(128), "scene %s at %dx%d holds %q", name, w, hgt, r)
					}
				}

				saveCapture(t, dir, name+"-"+strconv.Itoa(w)+"x"+strconv.Itoa(hgt)+".txt", frame)
			}
		}
	}
}

// earlierCaptureNames returns the scene names of the MVP to Phase 3. A
// new capture list of that kind joins this function, so the guard below
// sees every name.
func earlierCaptureNames() []string {
	var names []string

	for _, list := range [][]captureScene{
		captureScenes(), captureEvidenceScenes(), distCaptureScenes(), linksCaptureScenes(),
		pinsCaptureScenes(), extCaptureScenes(), keysCaptureScenes(),
	} {
		for _, sc := range list {
			names = append(names, sc.name)
		}
	}

	for _, sc := range settingsCaptureScenes() {
		names = append(names, sc.name)
	}

	for _, sc := range maintScenes() {
		names = append(names, sc.name)
	}

	return names
}

// The forms of a scene name: a code, a dash, and lowercase words. The
// Phase 4 pattern captures the series letter and the digit.
var (
	earlierSceneName = regexp.MustCompile(`^\d{2}[a-z]?-[a-z0-9]+(-[a-z0-9]+)*$`)
	phase4SceneName  = regexp.MustCompile(`^([A-D])(\d)[a-z]?-[a-z0-9]+(-[a-z0-9]+)*$`)
)

// TestCaptureSeriesFollowTheSceneMap keeps the capture codes apart. An
// earlier scene keeps its two digits, and a Phase 4 scene uses the
// letter of its own series. No code and no frame name exists twice in
// any series. Every code of the §7 scene map has a scene, and no Phase 4
// scene uses a code outside the map. It writes nothing, so it runs
// without the capture directory.
func TestCaptureSeriesFollowTheSceneMap(t *testing.T) {
	codes := map[string]string{}
	frames := map[string]bool{}

	claim := func(name string) {
		code, _, _ := strings.Cut(name, "-")
		assert.Empty(t, codes[code], "scene %s reuses the code of scene %s", name, codes[code])
		codes[code] = name
	}

	frame := func(name string) {
		assert.False(t, frames[name], "the frame name %s exists twice", name)
		frames[name] = true
	}

	for _, name := range earlierCaptureNames() {
		assert.Regexp(t, earlierSceneName, name, "an earlier scene keeps a code of two digits")
		claim(name)
		frame(name)
	}

	covered := map[string]bool{}

	for letter, scenes := range phase4Series() {
		require.NotEmpty(t, scenes, "series %s has scenes", letter)

		for _, sc := range scenes {
			m := phase4SceneName.FindStringSubmatch(sc.name)
			require.NotNil(t, m, "scene %s has the form <letter><digit>[variant]-<words>", sc.name)

			assert.Equal(t, letter, m[1], "scene %s uses the letter of another series", sc.name)
			assert.Contains(t, phase4SceneMap, m[1]+m[2], "scene %s uses a code outside the §7 scene map", sc.name)
			assert.False(t, strings.HasSuffix(sc.name, "-ascii"), "scene %s: the capture adds the ASCII variant", sc.name)
			assert.NotEmpty(t, sc.want, "scene %s names the phrase of its state", sc.name)

			covered[m[1]+m[2]] = true

			claim(sc.name)

			for _, name := range phase4Frames(sc) {
				frame(name)
			}
		}
	}

	for code, state := range phase4SceneMap {
		assert.True(t, covered[code], "the §7 scene map lists %s (%s), and no scene shows it", code, state)
	}
}
