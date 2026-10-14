package pinfiletest

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests prove the properties that the benchmarks rely on. They use
// small sizes, so the normal test run stays fast.

func TestLessonsReadsBackAsThePinsAndMutes(t *testing.T) {
	pins := Pins(25, "src", Note(1000))
	mutes := Mutes(6)
	text := Lessons(t, pins, mutes)

	cfg, err := reviews.ParseConfig([]byte(text))
	require.NoError(t, err)
	assert.Equal(t, pins, cfg.Pin, "the reader delivers every pin as generated")
	assert.Equal(t, mutes, cfg.Mute)

	doc := pinfile.Parse([]byte(text))
	require.False(t, doc.Unreadable(), "the pin file reads")
	require.Len(t, doc.Entries, len(pins))

	for i, e := range doc.Entries {
		assert.Empty(t, e.Block, "pin %d stays editable", i)
	}

	assert.Empty(t, doc.FileBlock, "the file stays editable")
	assert.Empty(t, Lessons(t, nil, nil), "empty lists leave both keys out")
}

func TestNoteHasTheSizeAndAnAuthoredForm(t *testing.T) {
	for _, n := range []int{1, 7, 1000, 10000} {
		note := Note(n)

		assert.Equal(t, n, utf8.RuneCountInString(note))
		require.NoError(t, reviews.CheckAuthoredText(reviews.TextPinNote, note), "size %d", n)
		assert.Equal(t, strings.TrimSpace(note), note, "size %d", n)
		assert.NotContains(t, note, " \n", "no line ends with a space")
	}

	assert.Contains(t, Note(1000), "\n", "a long note has lines")
	assert.Empty(t, Note(0))
}

func TestPinsNeitherRestateNorShareAKey(t *testing.T) {
	pins := Pins(60, "src", Note(500))
	keys := map[reviews.PinKey]bool{}

	surfaced := make([]reviews.SurfacedPin, len(pins))
	for i, p := range pins {
		require.NoError(t, reviews.CheckAuthoredText(reviews.TextPinRule, p.Rule))

		key := reviews.NewPinKey(p.Rule, p.Region, p.Regions)
		assert.False(t, keys[key], "pin %d repeats a key", i)
		keys[key] = true

		surfaced[i] = reviews.SurfacedPin{Pin: p}
	}

	kept, dropped := reviews.CollapseRestatedPins(surfaced)
	assert.Len(t, kept, len(pins), "the restatement check keeps every pin")
	assert.Empty(t, dropped)

	assert.Equal(t, Region("src", 0), pins[PinsPerRegion-1].Region)
	assert.Equal(t, Region("src", 1), pins[PinsPerRegion].Region, "a region holds PinsPerRegion pins")
}

func TestMutesHideNoMinedLessonAndNoPath(t *testing.T) {
	cfg := reviews.DefaultConfig()
	cfg.Mute = Mutes(20)

	lessons := Mined(3, 4, "src")
	require.Len(t, lessons, 12)

	for _, l := range lessons {
		assert.Empty(t, cfg.MuteMatches(l), "lesson %s", l.Symptom)
		assert.True(t, cfg.SurfacesMined(l), "lesson %s passes the default threshold", l.Symptom)
	}

	for _, p := range Paths(5, "src") {
		region := model.Lesson{Region: p, Symptom: "any", Occurrences: 3}
		assert.Empty(t, cfg.MuteMatches(region), "path %s", p)
	}
}

func TestPathsMeetTheRegionsOfThePinsAndLessons(t *testing.T) {
	paths := Paths(3, "src")

	assert.Equal(t, []string{"src/r000/" + Word(0) + ".go", "src/r001/" + Word(1) + ".go", "src/r002/" + Word(2) + ".go"},
		paths)
	assert.Equal(t, "src/r001", Mined(2, 1, "src")[1].Region)
}

func TestAppliedRowsLinkTheirPins(t *testing.T) {
	pins := Pins(5, "src", "")
	rows := Applied(pins, 3)

	require.Len(t, rows, 3)

	doc := pinfile.Parse([]byte(Lessons(t, pins, nil)))
	links := pinfile.Classify(doc, rows, nil, nil)

	for i, el := range links.Entries {
		want := pinfile.LinkNoProposal
		if i < len(rows) {
			want = pinfile.LinkLinked
		}

		assert.Equal(t, want, el.Link, "pin %d", i)
	}

	// A count above the pins adds rows whose pins the file does not hold.
	more := Applied(pins, 7)
	require.Len(t, more, 7)

	links = pinfile.Classify(doc, more, nil, nil)
	assert.Equal(t, pinfile.LinkNotPresent, links.ByProposal[more[6].ID])
	assert.Len(t, links.NotPresent, 2)
}

func TestWordsDiffer(t *testing.T) {
	seen := map[string]bool{}

	for i := range 5000 {
		w := Word(i)
		assert.False(t, seen[w], "word %d repeats %s", i, w)
		seen[w] = true
	}
}

func TestSweepVariesOneDimensionAtATime(t *testing.T) {
	var names []string

	for _, s := range Sweep(DimPins, DimNote) {
		names = append(names, s.Name())
		assert.Equal(t, Base.Mutes, s.Mutes, "a dimension outside the sweep stays at Base")
	}

	assert.Equal(t, []string{
		"pins=500/note=1000",
		"pins=10/note=1000", "pins=2000/note=1000", "pins=5000/note=1000",
		"pins=500/note=10000", "pins=500/note=100000",
	}, names, "Base first, then the other values of each dimension")
}
