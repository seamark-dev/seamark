package lessonreport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompactSummary keeps the capture reading separate and preserves
// every fixed word, including the qualification of negative observations.
func TestCompactSummary(t *testing.T) {
	p := PinReading{
		PinRef: "123456789abc", PinIdentity: strings.Repeat("a", 64), PinVersion: "version-a",
		Occurrences: OccurrenceCounts{Occurrences: 3, ByClass: map[PairClass]int{
			PairClass(ClassUnknown): 1, PairClass(ClassAtAction): 2,
		}},
		Timing:   map[PairTiming]int{PairTiming(TimingUnknown): 1, PairTiming(TimingSameCallOnly): 2},
		Delivery: DeliveryCounts{Sessions: 5}, Observation: Observation{NoLinkedCorrection: 2, Incomplete: 1},
	}
	r := &Report{Capture: CaptureState{State: CaptureAvailable}, Pins: []PinReading{p}}
	command := "seamark lessons --delivery --pin 123456789abc"
	// counts is the line of p up to its expansion command.
	counts := "lesson delivery: 3 assessed correction occurrence(s) (at-action 2, unknown 1); timing same-call-only 2, unknown 1; 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — "

	t.Run("template", func(t *testing.T) {
		line, ok := Compact(r, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, counts+command, line)
	})

	// The line names no version, so its counts tell which reading Compact
	// chose.
	t.Run("asked-version", func(t *testing.T) {
		clone := *r
		other := PinReading{PinRef: "abcdef123456", PinIdentity: strings.Repeat("b", 64), PinVersion: "version-b"}
		clone.Pins = []PinReading{p, other}
		clone.Historical = []PinReading{{PinRef: other.PinRef, PinIdentity: other.PinIdentity, PinVersion: p.PinVersion}}

		line, ok := Compact(&clone, other.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "lesson delivery: no assessed correction linked; 0 delivered session(s) — seamark lessons --delivery --pin abcdef123456", line)

		line, ok = Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, counts+command, line, "a config entry takes precedence over a historical version")
	})

	t.Run("unavailable-and-unknown", func(t *testing.T) {
		for _, tc := range []struct {
			report  *Report
			version string
		}{
			{nil, p.PinVersion},
			{&Report{Capture: CaptureState{State: CaptureUnavailable}, Pins: r.Pins}, p.PinVersion},
			{r, "unknown"}, {r, ""},
		} {
			line, ok := Compact(tc.report, tc.version)
			assert.False(t, ok)
			assert.Empty(t, line)
		}
	})

	t.Run("neither", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{{PinRef: p.PinRef, PinVersion: p.PinVersion}}
		line, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "lesson delivery: no assessed correction linked; 0 delivered session(s) — "+command, line)
	})

	t.Run("sessions-with-no-occurrences", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{{PinRef: p.PinRef, PinVersion: p.PinVersion, Delivery: DeliveryCounts{Sessions: 2}, Observation: Observation{Incomplete: 2}}}
		line, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "lesson delivery: 0 assessed correction occurrence(s); 2 delivered session(s): 0 no linked correction observed (qualified), 2 incomplete — "+command, line)
	})

	t.Run("no-timing-values", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{p}
		clone.Pins[0].Timing = nil
		line, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "lesson delivery: 3 assessed correction occurrence(s) (at-action 2, unknown 1); 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — "+command, line)
	})

	t.Run("all-classes-and-timings", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{p}
		clone.Pins[0].Occurrences = OccurrenceCounts{Occurrences: 7, ByClass: map[PairClass]int{
			PairMixed: 1, PairClass(ClassUnknown): 1, PairClass(ClassNotEmitted): 1,
			PairClass(ClassRegionNotMatched): 1, PairClass(ClassOtherLane): 1,
			PairClass(ClassEarlierInContext): 1, PairClass(ClassAtAction): 1,
		}}
		clone.Pins[0].Timing = map[PairTiming]int{PairTimingMixed: 1, PairTiming(TimingUnknown): 2, PairTiming(TimingSameCallOnly): 3, PairTiming(TimingBeforeConstruction): 1}
		line, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "lesson delivery: 7 assessed correction occurrence(s) (at-action 1, earlier-in-context 1, other-lane 1, region-not-matched 1, not-emitted 1, unknown 1, mixed 1); timing before-construction 1, same-call-only 3, unknown 2, mixed 1; 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — "+command, line)
		assert.Greater(t, len([]rune(line)), 200, "the complete template must not be truncated")
	})

	t.Run("historical-refs", func(t *testing.T) {
		clone := *r
		old := p
		old.PinRef = ""
		old.PinVersion = "old"
		clone.Historical = []PinReading{old}
		line, ok := Compact(&clone, old.PinVersion)
		require.True(t, ok)
		assert.Equal(t, counts+command, line, "a current identity retains its current pin ref")

		clone.Pins = nil
		line, ok = Compact(&clone, old.PinVersion)
		require.True(t, ok)
		assert.Equal(t, counts+"seamark lessons --delivery --pin aaaaaaaaaaaa", line)
		assert.NotContains(t, line, old.PinIdentity, "no raw identity is exposed")
	})
}
