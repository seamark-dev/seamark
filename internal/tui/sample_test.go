package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the selection sample (contracts §4.4). A scripted
// services.Sample stands in for the engine, because the engine tests of
// services prove the selection itself. These tests prove what the
// console sends, how it labels the result, and that it never reads an
// unavailable sample as zero lessons.

// sampleReadAt is the read time of a scripted result.
var sampleReadAt = time.Date(2026, 10, 10, 12, 1, 0, 0, time.Local)

// sampleScripted answers every request with view, under the surface of
// the request, as the engine does.
func sampleScripted(view services.SampleView) func(services.SampleRequest) services.SampleView {
	return func(req services.SampleRequest) services.SampleView {
		v := view
		v.Surface = req.Surface

		return v
	}
}

// samplePinOpen opens Pins, drafts a new pin, opens its review, and
// opens the sample of the review with s.
func samplePinOpen(t *testing.T, f *fakeServices, w, hgt int) *harness {
	t.Helper()

	h := pinsOpen(t, f, w, hgt)
	authorFill(h)
	h.press("s")
	require.True(t, authorOf(h).review.showsPreview())

	h.press("s")
	require.NotNil(t, pinSample(h), "s opens the sample of the review")

	return h
}

// pinSample returns the open sample of the pin review, or nil.
func pinSample(h *harness) *sample {
	if r := authorOf(h).review; r != nil {
		return r.sample
	}

	return nil
}

// sampleText returns the title, the surface and the body of s on one
// line, at a body width of w cells. Below 72 cells the two sides stack,
// so the words of one side stay together.
func sampleText(s *sample, w int) string {
	o := s.content(w)

	return lesFlat(ansi.Strip(strings.Join(append([]string{o.Title, o.Right}, o.Body...), "\n")))
}

// sampleAddPath adds one path through the keys of the sample.
func sampleAddPath(h *harness, path string) {
	h.press("a")
	h.typeText(path)
	h.press("enter")
}

// samplePin returns a selected pin of rule and note on regions.
func samplePin(rule, note string, regions ...string) reviews.SurfacedPin {
	return reviews.SurfacedPin{Pin: reviews.PinForRegions(rule, note, regions)}
}

// sampleLesson returns a mined lesson of symptom in region.
func sampleLesson(region, symptom string, n int) domain.Lesson {
	return domain.Lesson{ClusterKey: region + "\x00" + symptom, Region: region, Symptom: symptom, Occurrences: n}
}

// sampleMarkerView holds a lesson for each marker. The review rewords one
// pin, drops one pin above the total, and adds one pin. It also mutes
// one mined lesson and lowers the threshold, so a one-off lesson comes
// in.
func sampleMarkerView() services.SampleView {
	keep := samplePin("keep-changelog-current", "Add a changelog line.")
	old := samplePin("old-rule", "Old guidance.")
	ruf := sampleLesson("a.go", "RUF001", 6)
	e702 := sampleLesson("api", "E702", 3)
	once := sampleLesson("api", "solitary finding", 1)

	return services.SampleView{
		Paths: []string{"a.go", "api/new.go"}, ReadAt: sampleReadAt, IndexedAt: sampleReadAt.Add(-21 * time.Minute),
		Current: report.Selection{
			Pins:     []reviews.SurfacedPin{keep, samplePin("wrap-errors", "Wrap errors.", "api"), old},
			Mined:    []domain.Lesson{ruf, e702},
			Excluded: []report.Exclusion{{Lesson: once, BelowThreshold: true}},
		},
		Candidate: report.Selection{
			Pins: []reviews.SurfacedPin{
				keep, samplePin("wrap-errors", "Wrap every error with its context.", "api"),
				samplePin("new-rule", "New guidance.", "api"),
			},
			Mined:     []domain.Lesson{ruf, once},
			OverTotal: []reviews.SurfacedPin{old},
			HeldBack:  1,
			Excluded:  []report.Exclusion{{Lesson: e702, Mutes: []reviews.MuteRule{{Rule: "E702"}}}},
		},
		CurrentThreshold: 2, CandidateThreshold: 1,
	}
}

func TestSampleSendsOnlyTheReviewBytes(t *testing.T) {
	t.Run("pin review", func(t *testing.T) {
		f := authorFake(nil)
		f.sample = sampleScripted(sampleMarkerView())
		h := samplePinOpen(t, f, 120, 40)
		plan := authorOf(h).review.preview

		sampleAddPath(h, "internal/api")
		h.press("space")

		reqs := f.sampleRequests()
		require.Len(t, reqs, 3, "the open, the new path and the new surface each read once")
		assert.Equal(t, 3, f.callCount("Sample"))

		for _, req := range reqs {
			assert.Equal(t, plan.Before, req.Before, "the saved side is the text that the review read")
			assert.Equal(t, plan.After, req.After, "the review side is the candidate of the review")
		}

		assert.Empty(t, reqs[0].Paths, "no path by default")
		assert.Equal(t, services.SampleHook, reqs[0].Surface)
		assert.Equal(t, []string{"internal/api"}, reqs[1].Paths)
		assert.Equal(t, services.SampleChangeSet, reqs[2].Surface, "space switches the surface")
		assert.NotContains(t, f.called(), "CommitAuthor:create", "a sample never commits")
	})

	t.Run("delivery review", func(t *testing.T) {
		f := lessonsSnapFake(t, "threshold: 4\n")
		f.sample = sampleScripted(sampleMarkerView())
		h := lessonsOpen(t, f, "group:delivery", 120, 40)
		h.press("d", "s", "s")

		scr := settingsScr(h)
		require.NotNil(t, scr.flow.sample, "s opens the sample of a lessons review")

		reqs := f.sampleRequests()
		require.Len(t, reqs, 1)
		assert.Equal(t, scr.flow.preview.Bytes.Before, reqs[0].Before)
		assert.Equal(t, scr.flow.preview.Bytes.After, reqs[0].After)
		assert.NotContains(t, f.called(), "SaveSettings", "a sample never saves")
	})

	t.Run("mute review", func(t *testing.T) {
		h, svc := muteEditorOpen(t, 120, 40)
		svc.sample = sampleScripted(sampleMarkerView())
		h.press("a")
		h.typeText("W1")
		h.press("enter", "s")
		require.True(t, settingsScr(h).flow.showsPreview(), "s in the mute editor opens its review")

		h.press("s")

		scr := settingsScr(h)
		require.NotNil(t, scr.flow.sample, "s opens the sample of the mute review")
		assert.False(t, scr.flow.sample.modeOnly, "a mute review changes no mode")

		reqs := svc.sampleRequests()
		require.Len(t, reqs, 1)
		assert.Equal(t, scr.flow.preview.Bytes.Before, reqs[0].Before)
		assert.Equal(t, scr.flow.preview.Bytes.After, reqs[0].After)
		assert.NotContains(t, svc.called(), "SaveSettings", "a sample never saves")
	})

	t.Run("a plan without a candidate", func(t *testing.T) {
		f := authorFake(func(p *maintain.AuthorPlan) { p.After = nil })
		h := pinsOpen(t, f, 120, 40)
		authorFill(h)
		h.press("s", "s")

		assert.Nil(t, pinSample(h), "a plan without a candidate has nothing to compare")
		assert.Zero(t, f.callCount("Sample"))
		assert.NotContains(t, authorReviewFlat(t, h), "s sample")

		page, ok := pinsOf(h).contextHelp()
		require.True(t, ok)
		assert.NotContains(t, page.text, sampleOpenHelp, "the help names s only where s opens a sample")
	})

	t.Run("a no-op review", func(t *testing.T) {
		f := lessonsSnapFake(t, "threshold: 4\n")
		planner := f.planSettings
		f.planSettings = func(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
			p, err := planner(snap, edits)
			if p != nil {
				p.NoOp = true
			}

			return p, err
		}

		h := lessonsOpen(t, f, "group:delivery", 120, 40)
		h.press("d", "s", "s")
		assert.Nil(t, settingsScr(h).flow.sample, "a no-op review has one text, so nothing to compare")
		assert.Zero(t, f.callCount("Sample"))
	})

	t.Run("a config.yaml review", func(t *testing.T) {
		svc := settingsFake(t, "")
		h := settingsOpen(t, svc, 120, 40)
		h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
		h.press("enter", "down", "space")
		h.typeText("180")
		h.press("enter", "esc", "s")
		require.True(t, settingsScr(h).flow.showsPreview())

		h.press("s")
		assert.Nil(t, settingsScr(h).flow.sample, "a sample selects from lessons.yaml only")
		assert.Zero(t, svc.callCount("Sample"))
		assert.NotContains(t, footerLine(h.view()), "s sample")
	})
}

// sampleUnavailableTexts are copies of the texts of services.Sample, one
// per unavailable state. The engine test TestEngineSampleStates owns
// these texts. The console tests and the captures script them.
var sampleUnavailableTexts = map[string]string{
	"absent index":  "Sample unavailable: no readable index. Without an index the hook still delivers pins only; this sample does not model that.",
	"unreadable":    "Sample unavailable: the index is unreadable.",
	"newer schema":  "Sample unavailable: the index is newer: a newer seamark wrote its schema, and this seamark cannot read it.",
	"older schema":  "Sample unavailable: the index is older: its schema needs the migration of an index build, and a sample never builds the index.",
	"not indexed":   "Sample unavailable: the index is not indexed: it holds decisions but no mined evidence.",
	"foreign root":  "Sample unavailable: the index records another repository root (/elsewhere), not this workspace (/work/repo).",
	"no paths":      "Sample unavailable: no paths. Add one or more paths; no paths never means the whole repository.",
	"saved text":    "Sample unavailable: the saved configuration cannot be read: hook_delivery must be \"always\".",
	"review text":   "Sample unavailable: the configuration of this review cannot be read: yaml: line 1.",
	"no usable":     "Sample unavailable: no usable path: the hook rejects every path.",
	"evidence read": "Sample unavailable: the evidence read failed: database is locked.",
	"selection":     "Sample unavailable: the selection failed: report: the lesson evidence was not read for this config.",
}

// sampleNoIndex answers as the engine does in a workspace without an
// index: a request without a path gets the no-paths reason, and every
// other request gets the no-index reason. Neither has a list.
func sampleNoIndex(req services.SampleRequest) services.SampleView {
	text := sampleUnavailableTexts["absent index"]
	if len(req.Paths) == 0 {
		text = sampleUnavailableTexts["no paths"]
	}

	return services.SampleView{Surface: req.Surface, Unavailable: text}
}

func TestSampleUnavailableNeverShowsZero(t *testing.T) {
	// This test proves the display rule: each text shows as it is, with
	// no list and no count.
	zeroWords := []string{
		sampleSavedTitle, sampleReviewTitle, "held back", "trimmed", "No lesson selected", "FILES", "index built",
		"index build time",
	}

	for name, text := range sampleUnavailableTexts {
		t.Run(name, func(t *testing.T) {
			f := authorFake(nil)
			f.sample = sampleScripted(services.SampleView{Unavailable: text})
			h := samplePinOpen(t, f, 120, 40)
			sampleAddPath(h, "a.go")

			got := sampleText(pinSample(h), 60)
			assert.Contains(t, got, lesFlat(text))

			for _, word := range zeroWords {
				assert.NotContains(t, got, word, "an unavailable sample shows no list and no count")
			}

			assert.Contains(t, lesFlat(h.view()), "Sample unavailable", "the frame shows the reason")
		})
	}

	t.Run("an unscripted engine", func(t *testing.T) {
		h := samplePinOpen(t, authorFake(nil), 120, 40)
		got := sampleText(pinSample(h), 60)
		assert.Contains(t, got, "Sample unavailable: no scripted sample.")
		assert.NotContains(t, got, "held back")
	})

	t.Run("rejected paths name their reason", func(t *testing.T) {
		f := authorFake(nil)
		f.sample = sampleScripted(services.SampleView{
			Unavailable: sampleUnavailableTexts["no usable"],
			Rejected:    []delivery.RejectedPath{{Path: "../outside.go", Reason: delivery.RejectOutside}},
		})
		h := samplePinOpen(t, f, 120, 40)
		assert.Contains(t, sampleText(pinSample(h), 60), "../outside.go · rejected: outside the workspace")
	})

	t.Run("an empty selection is a computed result", func(t *testing.T) {
		f := authorFake(nil)
		f.sample = sampleScripted(services.SampleView{Paths: []string{"a.go"}, ReadAt: sampleReadAt})
		h := samplePinOpen(t, f, 120, 40)
		sampleAddPath(h, "a.go")

		got := sampleText(pinSample(h), 60)
		assert.NotContains(t, got, "Sample unavailable")
		assert.Equal(t, 2, strings.Count(got, "No lesson selected for these paths."), "each side says it in words")
		assert.Equal(t, 2, strings.Count(got, "pins held back (restatements and caps): 0"))
	})
}

func TestSampleMarkersAreText(t *testing.T) {
	f := authorFake(nil)
	f.sample = sampleScripted(sampleMarkerView())
	h := samplePinOpen(t, f, 120, 40)
	sampleAddPath(h, "a.go")

	got := sampleText(pinSample(h), 60)
	saved, review, ok := strings.Cut(got, sampleReviewTitle)
	require.True(t, ok, "the review side has its title")
	require.Contains(t, saved, sampleSavedTitle)

	sep := " " + h.m.st.theme.Glyphs.Sep + " "

	assert.Contains(t, saved, "Add a changelog line. kept")
	assert.Contains(t, saved, "Wrap errors. kept, wording changed")
	assert.Contains(t, saved, "Old guidance. removed"+sep+"above the total cap", "a held-back pin names its list")
	assert.Contains(t, saved, "RUF001 kept")
	assert.Contains(t, saved, "E702 removed"+sep+"muted by rule E702", "an excluded lesson names every reason")
	assert.Contains(t, saved, "solitary finding below threshold 2", "the saved side lists its own exclusion")

	assert.Contains(t, review, "Add a changelog line. kept")
	assert.Contains(t, review, "Wrap every error with its context. kept, wording changed")
	assert.Contains(t, review, "New guidance. added")
	assert.Contains(t, review, "solitary finding added")
	assert.NotContains(t, review, "added"+sep, "an added lesson shows its earlier reason under the saved exclusions")
	assert.Contains(t, review, "E702 muted by rule E702", "the review side lists its own exclusion")
	assert.NotContains(t, review, "removed", "the review side names what it adds, not what it drops")

	// The tags of the hook: pin for a pin, ×N for a mined lesson.
	assert.Contains(t, saved, "pin"+sep+"*"+sep+"keep-changelog-current")
	assert.Contains(t, saved, "×6"+sep+"a.go"+sep+"RUF001")

	// The markers are words, so the ASCII frame keeps them.
	h.m.st.theme = ui.NewTheme(true)
	frame := lesFlat(h.view())
	assert.Contains(t, frame, "kept, wording changed")
	assert.Contains(t, frame, "x6 / a.go / RUF001")
}

func TestSampleNeverMarksAChangeSetRepeatFromItsAbsence(t *testing.T) {
	x := samplePin("wrap-errors", "Wrap errors.")
	y := samplePin("wrap-errors", "Wrap every error.")

	marks := func(rows []sampleRow) []sampleMark {
		var out []sampleMark
		for _, r := range rows {
			out = append(out, r.mark)
		}

		return out
	}

	// The saved file holds x twice, and change_set lists it once. The
	// review rewords the first entry, so the review lists y and then x.
	v := &services.SampleView{
		Surface:   services.SampleChangeSet,
		Current:   report.Selection{Pins: []reviews.SurfacedPin{x}},
		Candidate: report.Selection{Pins: []reviews.SurfacedPin{y, x}},
	}

	saved, review := sampleSides(v)
	assert.Equal(t, []sampleMark{sampleReworded}, marks(saved))
	assert.Equal(t, []sampleMark{sampleReworded, sampleMarkNone}, marks(review),
		"the saved side lists x, so the second x can be its change_set repeat: no marker")

	// The review drops both entries: no list of the review holds x.
	v.Candidate = report.Selection{}
	saved, _ = sampleSides(v)
	assert.Equal(t, []sampleMark{sampleRemoved}, marks(saved))

	// The hook lists every applicable pin, so a pin in no list is gone.
	v.Surface = services.SampleHook
	v.Current = report.Selection{Pins: []reviews.SurfacedPin{x, x}}
	v.Candidate = report.Selection{Pins: []reviews.SurfacedPin{y}}
	saved, review = sampleSides(v)
	assert.Equal(t, []sampleMark{sampleReworded, sampleRemoved}, marks(saved))
	assert.Equal(t, []sampleMark{sampleReworded}, marks(review))

	// A held-back list names its reason on each surface.
	v.Candidate = report.Selection{Restated: []reviews.SurfacedPin{x}}
	saved, _ = sampleSides(v)
	assert.Equal(t, "restates another pin", saved[0].why)

	v.Surface = services.SampleChangeSet
	v.Current = report.Selection{Pins: []reviews.SurfacedPin{x}}
	v.Candidate = report.Selection{OverTotal: []reviews.SurfacedPin{x}}
	saved, _ = sampleSides(v)
	assert.Equal(t, sampleRemoved, saved[0].mark)
	assert.Equal(t, "above the change_set budget", saved[0].why)

	// A mined lesson in no list of the other side has no typed fate.
	ruf := sampleLesson("a.go", "RUF001", 6)
	v.Current = report.Selection{Mined: []domain.Lesson{ruf}}
	v.Candidate = report.Selection{}
	saved, _ = sampleSides(v)
	assert.Equal(t, []sampleMark{sampleMarkNone}, marks(saved))

	v.Candidate = report.Selection{MinedOverTotal: []domain.Lesson{ruf}}
	saved, _ = sampleSides(v)
	assert.Equal(t, []sampleMark{sampleRemoved}, marks(saved))
	assert.Equal(t, "above the change_set budget", saved[0].why)
}

func TestSampleHeldReasonBelongsToThePin(t *testing.T) {
	// a, b and c share the rule and the regions, so they share a pin key.
	// Only the note tells them apart.
	a := samplePin("wrap-errors", "Wrap an error with its context.")
	b := samplePin("wrap-errors", "Return a wrapped cause from every failing call.")
	c := samplePin("wrap-errors", "Name the failing call in the error.")

	cases := []struct {
		name      string
		surface   services.SampleSurface
		candidate report.Selection
		why       string
	}{
		{
			// The hook keeps every entry, so a review that lowers the pin
			// cap puts a above the cap and b in Restated.
			name: "the list of the entry on the hook", surface: services.SampleHook,
			candidate: report.Selection{Restated: []reviews.SurfacedPin{b}, OverPinCap: []reviews.SurfacedPin{a}},
			why:       "above the pin cap",
		},
		{
			name: "the list of the entry on change_set", surface: services.SampleChangeSet,
			candidate: report.Selection{Restated: []reviews.SurfacedPin{b}, OverTotal: []reviews.SurfacedPin{a}},
			why:       "above the change_set budget",
		},
		{
			// The review rewords a, and the one pin of that key is above
			// the cap.
			name: "the one held pin of a reworded entry", surface: services.SampleHook,
			candidate: report.Selection{OverPinCap: []reviews.SurfacedPin{c}},
			why:       "above the pin cap",
		},
		{
			name: "two held pins of the key name no reason", surface: services.SampleHook,
			candidate: report.Selection{Restated: []reviews.SurfacedPin{b}, OverPinCap: []reviews.SurfacedPin{c}},
		},
		{
			// lessons.yaml repeats a. The lists do not say which copy is
			// the entry of the saved side.
			name: "two held copies of the entry name no reason", surface: services.SampleHook,
			candidate: report.Selection{Restated: []reviews.SurfacedPin{a}, OverPinCap: []reviews.SurfacedPin{a}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &services.SampleView{
				Surface:   tc.surface,
				Current:   report.Selection{Pins: []reviews.SurfacedPin{a}, Restated: []reviews.SurfacedPin{b}},
				Candidate: tc.candidate,
			}

			saved, _ := sampleSides(v)
			require.Len(t, saved, 1)
			assert.Equal(t, sampleRemoved, saved[0].mark, "the review selects no pin of this key")
			assert.Equal(t, tc.why, saved[0].why, "a reason comes only from the list that holds this pin")
		})
	}
}

func TestSampleExclusionNamesEveryReason(t *testing.T) {
	ex := report.Exclusion{
		Lesson: sampleLesson("api", "E702", 1), Covered: true, BelowThreshold: true,
		Mutes: []reviews.MuteRule{{Rule: "E702", Region: "api"}, {Region: "api/"}},
	}

	assert.Equal(t, "muted by rule E702 · region api; muted by region api/; below threshold 3; covered by an applied pin",
		sampleExclusionWords(ex, 3))
	assert.Empty(t, sampleExclusionWords(report.Exclusion{}, 3), "no typed reason, no word")
}

func TestSampleHeldBackLabels(t *testing.T) {
	view := sampleMarkerView()
	view.Current.HeldBack = 2
	view.Candidate.HeldBack = 3

	f := authorFake(nil)
	f.sample = sampleScripted(view)
	h := samplePinOpen(t, f, 120, 40)
	sampleAddPath(h, "a.go")

	hook := sampleText(pinSample(h), 60)
	assert.Contains(t, hook, "pins held back (restatements and caps): 2")
	assert.Contains(t, hook, "pins held back (restatements and caps): 3")

	h.press("space")

	changeSet := sampleText(pinSample(h), 60)
	assert.Contains(t, changeSet, "lessons trimmed (restatements and the change_set budget): 2")
	assert.Contains(t, changeSet, "lessons trimmed (restatements and the change_set budget): 3")
	assert.Contains(t, changeSet, "change-set selection", "the border names the surface")

	for _, text := range []string{hook, changeSet} {
		assert.NotContains(t, text, "over budget", "a held-back count includes restatements")
	}
}

func TestSampleModeOnlyNote(t *testing.T) {
	// The cursor starts on threshold; hook_delivery is three rows down.
	modeKeys := []string{"down", "down", "down", "enter", "down", "down", "space", "enter"}

	openWith := func(t *testing.T, script func(services.SampleRequest) services.SampleView, keys ...string) *harness {
		t.Helper()

		f := lessonsSnapFake(t, "threshold: 4\n")
		f.sample = script
		h := lessonsOpen(t, f, "group:delivery", 120, 40)
		h.press(keys...)
		h.press("s", "s")
		require.NotNil(t, settingsScr(h).flow.sample)

		return h
	}

	open := func(t *testing.T, keys ...string) string {
		t.Helper()

		return sampleText(settingsScr(openWith(t, sampleScripted(sampleMarkerView()), keys...)).flow.sample, 60)
	}

	mode := open(t, modeKeys...)
	assert.Contains(t, mode, sampleModeNote, "a mode-only review says what the sample cannot show")

	// The engine answers a request without a path, and a request without
	// an index, with a reason only. The note belongs to the review, so it
	// shows in these states too.
	h := openWith(t, sampleNoIndex, modeKeys...)
	atOpen := sampleText(settingsScr(h).flow.sample, 60)
	assert.Contains(t, atOpen, "Sample unavailable: no paths.")
	assert.Contains(t, atOpen, sampleModeNote, "the note shows at open, before any path")

	sampleAddPath(h, "a.go")
	noIndex := sampleText(settingsScr(h).flow.sample, 60)
	assert.Contains(t, noIndex, "Sample unavailable: no readable index.")
	assert.Contains(t, noIndex, sampleModeNote, "the note shows next to an unavailable reason")

	threshold := open(t, "d")
	assert.NotContains(t, threshold, "hook_delivery changes repetition")

	h = openWith(t, sampleNoIndex, "d")
	assert.NotContains(t, sampleText(settingsScr(h).flow.sample, 60), "hook_delivery changes repetition",
		"a threshold review shows no note in an unavailable state either")

	f := authorFake(nil)
	f.sample = sampleScripted(sampleMarkerView())
	assert.NotContains(t, sampleText(pinSample(samplePinOpen(t, f, 120, 40)), 60), "hook_delivery changes repetition")
}

func TestSampleLabelsItsSnapshotAndFiles(t *testing.T) {
	view := sampleMarkerView()
	view.NoFile = []string{"api/new.go"}
	view.Unindexed = []string{"api/new.go"}
	view.Stale = true

	f := authorFake(nil)
	f.sample = sampleScripted(view)
	h := samplePinOpen(t, f, 120, 40)
	sampleAddPath(h, "a.go")

	sep := " " + h.m.st.theme.Glyphs.Sep + " "
	got := sampleText(pinSample(h), 60)

	assert.Contains(t, got, "evidence read 12:01, index built 11:40")
	assert.Contains(t, got, "FILES a.go api/new.go"+sep+"no file yet"+sep+"not in the index")
	assert.Contains(t, got, lesFlat(sampleStaleNote), "a saved side that is not the disk says so")
	assert.NotContains(t, got, "hook_delivery changes repetition", "a pin review never changes the mode")

	// An index of another day shows its date, and an unknown build time
	// says so in words.
	day := sampleReadAt.Add(-30 * time.Hour)
	assert.Equal(t, "evidence read 12:01, index built "+day.Format("2006-01-02 15:04"),
		sampleSnapshot(&services.SampleView{ReadAt: sampleReadAt, IndexedAt: day}))
	assert.Equal(t, "evidence read 12:01, index build time unknown", sampleSnapshot(&services.SampleView{ReadAt: sampleReadAt}))
}

func TestSampleRecomputesOnReloads(t *testing.T) {
	reads := 0

	f := authorFake(nil)
	f.sample = func(req services.SampleRequest) services.SampleView {
		v := sampleMarkerView()
		v.Surface = req.Surface
		v.ReadAt = sampleReadAt.Add(time.Duration(reads) * time.Minute)
		reads++

		return v
	}

	h := samplePinOpen(t, f, 120, 40)
	sampleAddPath(h, "a.go")
	require.Equal(t, 2, f.callCount("Sample"))

	for i, kind := range []sectionKind{secIndex, secLedger, secPins} {
		h.send(refreshMsg{kinds: []sectionKind{kind}})
		assert.Equal(t, 3+i, f.callCount("Sample"), "a load of %v reads the sample again", kind)
	}

	assert.Contains(t, sampleText(pinSample(h), 60), "evidence read 12:05", "the label names the newest read")
	assert.Equal(t, []string{"a.go"}, f.sampleRequests()[4].Paths, "a reload keeps the rows")

	h.send(refreshMsg{kinds: []sectionKind{secStats, secDoctor, secSettings}})
	assert.Equal(t, 5, f.callCount("Sample"), "other sources do not feed the sample")

	// A failed load keeps its old snapshot, so the evidence is the same.
	gen := h.m.st.gens[secLedger].start()
	h.send(loadedMsg{kind: secLedger, gen: gen, err: errors.New("database is locked"), at: time.Now()})
	assert.Equal(t, 5, f.callCount("Sample"))

	t.Run("settings", func(t *testing.T) {
		sf := lessonsSnapFake(t, "threshold: 4\n")
		sf.sample = sampleScripted(sampleMarkerView())
		sh := lessonsOpen(t, sf, "group:delivery", 120, 40)
		sh.press("d", "s", "s")
		require.Equal(t, 1, sf.callCount("Sample"))

		sh.send(refreshMsg{kinds: []sectionKind{secIndex}})
		assert.Equal(t, 2, sf.callCount("Sample"), "the sample of a settings review reads again too")
	})
}

func TestSampleCapturesEveryKey(t *testing.T) {
	f := authorFake(nil)
	f.sample = sampleScripted(sampleMarkerView())
	h := samplePinOpen(t, f, 120, 40)
	require.True(t, hookLessons(h).capturing(), "the sample captures for its whole life")
	assert.Contains(t, footerLine(h.view()), "ctrl+c quit")
	assert.Contains(t, footerLine(h.view()), "space surface", "the footer names the keys of the sample")
	assert.Contains(t, h.view(), "Selection sample", "the sample takes the place of the review modal")

	// No global key, no Lessons key, no Pins key and no review key acts.
	for _, k := range []string{"1", "2", "3", "4", "5", "q", "r", "p", "m", "s", "e", "u", "y", "n", "i", "d", "o",
		"w", "t", "v", "f", "/", "c", "tab"} {
		h.press(k)
		require.Equal(t, destLessons, h.m.active, "key %q", k)
		require.Equal(t, lesPins, hookLessons(h).sub, "key %q", k)
		require.NotNil(t, pinSample(h), "key %q leaves the sample open", k)
		require.False(t, h.quit, "key %q", k)
		require.False(t, h.m.quitConfirm, "key %q", k)
	}

	assert.Equal(t, 1, f.callCount("Sample"), "no key above reads again")
	assert.Zero(t, f.callCount("CommitAuthor:create"), "y never commits under the sample")

	// ? opens the help of the sample while no field has focus.
	h.press("?")
	require.True(t, h.m.help)
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Selection sample", h.m.page.title)
	h.press("esc")
	require.NotNil(t, pinSample(h), "the help returns to the sample")

	// The path input takes every character, ? and digits included.
	h.press("a")
	h.typeText("1q?r")
	assert.False(t, h.m.help, "? is a typed character in the path input")
	h.press("enter")
	assert.Equal(t, []string{"1q?r"}, pinSample(h).req.Paths)

	// ctrl+c reaches the shell: the draft asks the quit question.
	h.press("ctrl+c")
	assert.True(t, h.m.quitConfirm)
	h.press("esc")

	// esc returns to the review that opened the sample, as it was.
	h.press("esc")
	assert.Nil(t, pinSample(h))
	require.NotNil(t, authorOf(h).review)
	assert.True(t, authorOf(h).review.showsPreview(), "the review stays under the sample")
	assert.Zero(t, f.callCount("PlanAuthor:create")-1, "the review was not planned again")
}

func TestSampleRowsEditThePaths(t *testing.T) {
	f := authorFake(nil)
	f.sample = sampleScripted(sampleMarkerView())
	h := samplePinOpen(t, f, 120, 40)
	s := pinSample(h)

	sampleAddPath(h, "a.go")
	sampleAddPath(h, "api")
	assert.Equal(t, []string{"a.go", "api"}, s.req.Paths)
	assert.Equal(t, 1, s.row, "the cursor follows the new path")

	// enter edits the cursor path; an empty value removes the row.
	h.press("enter", "backspace", "backspace", "backspace")
	h.typeText("web")
	h.press("enter")
	assert.Equal(t, []string{"a.go", "web"}, s.req.Paths)

	h.press("up", "enter", "backspace", "backspace", "backspace", "backspace", "esc")
	assert.Equal(t, []string{"web"}, s.req.Paths, "esc keeps the typed value, and an empty one goes")

	// An edit that keeps the value reads nothing again.
	reads := f.callCount("Sample")
	h.press("enter", "enter")
	assert.Equal(t, reads, f.callCount("Sample"), "the same rows need no new read")

	// x removes the cursor path; on the add row it does nothing.
	h.press("down", "x")
	assert.Equal(t, []string{"web"}, s.req.Paths, "x on the add row removes nothing")
	h.press("up", "x")
	assert.Empty(t, s.req.Paths)
	h.press("x")
	assert.Empty(t, s.req.Paths)

	// A paste of two lines is refused whole, with a notice.
	h.press("a")
	h.paste("a.go\napi")
	assert.Contains(t, sampleText(s, 60), "This field takes one line")
	h.press("esc")
	assert.Empty(t, s.req.Paths, "the refused paste added nothing")

	reqs := f.sampleRequests()
	assert.Equal(t, []string{"web"}, reqs[len(reqs)-2].Paths)
	assert.Empty(t, reqs[len(reqs)-1].Paths, "each kept change reads again")
}

func TestSampleShowsOnlyTheNewestReadOfItsRows(t *testing.T) {
	f := newFake()
	f.sample = func(req services.SampleRequest) services.SampleView {
		v := sampleMarkerView()
		v.Surface, v.Paths = req.Surface, req.Paths

		return v
	}

	st := &state{svc: f, theme: ui.NewTheme(false)}
	s, first := newSample(st, []byte("pin: []\n"), []byte("threshold: 3\n"), false)
	s.done(first().(sampledMsg))

	// A changed row starts a read. Until it arrives, the result of the
	// old rows must not show under the new rows.
	s.edit(0)
	s.input.Set("a.go")
	second := s.fieldKey(keyMsg("enter"))
	require.NotNil(t, second)
	assert.Contains(t, sampleText(s, 60), "Reading the evidence", "a result of other rows stays hidden")
	assert.NotContains(t, sampleText(s, 60), sampleSavedTitle)

	// A second change starts a newer read. The older read arrives last
	// and must not replace the newer result.
	third, _ := s.key(keyMsg("space"))
	require.NotNil(t, third)

	s.done(third().(sampledMsg))
	s.done(second().(sampledMsg))

	assert.Equal(t, services.SampleChangeSet, s.view.Surface, "the newest read stays")
	assert.Contains(t, sampleText(s, 60), "lessons trimmed", "the newest read shows")
}

func TestSampleKeepsTheCursorRowInView(t *testing.T) {
	f := authorFake(nil)
	f.sample = sampleScripted(sampleMarkerView())
	h := &harness{t: t, m: newModel(f, ui.NewTheme(true))}
	h.run(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 60, Height: 20})
	h.press("3", "m")
	authorFill(h)
	h.press("s", "s")
	require.NotNil(t, pinSample(h))

	for _, p := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "b1", "b2", "last/path.go"} {
		sampleAddPath(h, p)
	}

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	assert.Contains(t, frame, "> "+"         last/path.go", "the cursor row of the last path shows")
	assert.Contains(t, frame, "esc back", "the key that leaves the sample shows at the minimum size")

	h.press("home")
	assert.Contains(t, h.view(), "Local selection", "home scrolls back to the top")

	h.press("down")
	assert.Contains(t, h.view(), "a2", "a cursor move shows the cursor row again")

	for _, r := range h.view() {
		require.Less(t, r, rune(128), "the ASCII frame holds ASCII only")
	}
}

func TestSampleHostileTextIsInert(t *testing.T) {
	const hostile = "\x1b]0;pwned\x07\x1b[2J\u009b31m\u202e"

	pin := samplePin("rule-"+hostile, "Note "+hostile+" text.", "api"+hostile)
	mined := sampleLesson("api"+hostile, "E702"+hostile, 3)
	view := services.SampleView{
		Paths: []string{"a" + hostile}, NoFile: []string{"a" + hostile}, Unindexed: []string{"a" + hostile},
		Rejected: []delivery.RejectedPath{{Path: "../" + hostile, Reason: delivery.RejectOutside}},
		ReadAt:   sampleReadAt,
		Current:  report.Selection{Pins: []reviews.SurfacedPin{pin}, Mined: []domain.Lesson{mined}},
		Candidate: report.Selection{
			OverTotal: []reviews.SurfacedPin{pin},
			Excluded: []report.Exclusion{{
				Lesson: mined, BelowThreshold: true, Mutes: []reviews.MuteRule{{Rule: "E702" + hostile, Region: hostile}},
			}},
		},
		CurrentThreshold: 2, CandidateThreshold: 5,
	}

	for _, ascii := range []bool{false, true} {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			f := authorFake(nil)
			f.sample = sampleScripted(view)
			h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
			h.run(h.m.Init())
			h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			h.press("3", "m")
			authorFill(h)
			h.press("s", "s")
			require.NotNil(t, pinSample(h))

			h.press("a")
			h.paste("p" + hostile)
			h.press("enter")

			check := func(scene string) {
				raw := h.m.View()
				for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u202e"} {
					assert.NotContains(t, raw, bad, "%s at %v", scene, size)
				}

				assertFrame(t, h.view(), size[0], size[1])

				if ascii {
					for _, r := range h.view() {
						require.Less(t, r, rune(128), "ASCII frame of %s at %v", scene, size)
					}
				}
			}

			check("sample")
			h.press("end")
			check("sample end")

			assert.Contains(t, sampleText(pinSample(h), 60), "U+202E", "a hidden character shows as its escape")

			f.sample = sampleScripted(services.SampleView{Unavailable: "Sample unavailable: " + hostile})
			h.press("space")
			check("unavailable")
		}
	}
}

func TestSampleClosesWithItsReview(t *testing.T) {
	t.Run("pin writing changed", func(t *testing.T) {
		h := samplePinOpen(t, authorFake(nil), 120, 40)
		h.send(settingsSavedMsg{group: settings.GroupPinWrite, at: time.Now()})

		assert.Nil(t, authorOf(h).review, "the review and its sample read the old gate")
		assert.NotNil(t, authorOf(h).newDraft, "the draft stays")
	})

	t.Run("another settings group", func(t *testing.T) {
		h := samplePinOpen(t, authorFake(nil), 120, 40)
		h.send(settingsSavedMsg{group: settings.GroupInference, at: time.Now()})

		assert.NotNil(t, pinSample(h), "the sample does not read config.yaml")
	})

	t.Run("a late read of a closed sample", func(t *testing.T) {
		f := authorFake(nil)
		h := samplePinOpen(t, f, 120, 40)
		closed := pinSample(h)
		h.press("esc", "s")

		next := pinSample(h)
		require.NotNil(t, next)
		require.NotSame(t, closed, next)

		h.send(sampledMsg{owner: closed, seq: closed.seq.latest(), view: services.SampleView{Unavailable: "Sample unavailable: a stale read."}})
		assert.NotContains(t, sampleText(next, 60), "a stale read", "a read of a closed sample changes nothing")
	})
}

func TestSampleContextHelpOfSettings(t *testing.T) {
	f := lessonsSnapFake(t, "threshold: 4\n")
	f.sample = sampleScripted(sampleMarkerView())
	h := lessonsOpen(t, f, "group:delivery", 120, 40)
	h.press("d", "s")

	page, ok := settingsScr(h).contextHelp()
	require.True(t, ok)
	assert.Contains(t, keyNames(page.keys), "s", "the review of lessons.yaml names the sample")
	assert.Contains(t, page.text, sampleOpenHelp, "the page says what s does, as the pin review help does")
	assert.Contains(t, footerLine(h.view()), "s sample")

	h.press("s")
	assert.Contains(t, h.view(), "Selection sample", "the sample takes the place of the review modal")
	assert.Contains(t, footerLine(h.view()), "space surface", "the footer names the keys of the sample")

	h.press("?")
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Selection sample", h.m.page.title)

	h.press("esc", "esc")
	assert.Nil(t, settingsScr(h).flow.sample, "esc returns to the review")
	assert.True(t, settingsScr(h).flow.showsPreview())
	assert.Contains(t, footerLine(h.view()), "y save", "the review keys are back")

	// A late read of the closed sample never reaches the next one.
	closed := &sample{}
	h.press("s")
	next := settingsScr(h).flow.sample
	require.NotNil(t, next)

	h.send(sampledMsg{owner: closed, seq: next.seq.latest(), view: services.SampleView{Unavailable: "Sample unavailable: a stale read."}})
	assert.NotContains(t, sampleText(next, 60), "a stale read", "a read of another sample changes nothing")
}

// keyNames returns the key labels of the enabled bindings.
func keyNames(bindings []key.Binding) []string {
	var out []string

	for _, b := range bindings {
		if b.Enabled() {
			out = append(out, b.Help().Key)
		}
	}

	return out
}
