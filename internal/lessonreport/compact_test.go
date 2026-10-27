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

	t.Run("template", func(t *testing.T) {
		summary, ok := Compact(r, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, CompactSummary{
			PinVersion: p.PinVersion, Command: command,
			Line: "capture delivery: 3 assessed correction occurrence(s) (at-action 2, unknown 1); timing same-call-only 2, unknown 1; 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — " + command,
		}, summary)
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
			summary, ok := Compact(tc.report, tc.version)
			assert.False(t, ok)
			assert.Equal(t, CompactSummary{}, summary)
		}
	})

	t.Run("neither", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{{PinRef: p.PinRef, PinVersion: p.PinVersion}}
		summary, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "capture delivery: no assessed correction linked; 0 delivered session(s) — "+command, summary.Line)
	})

	t.Run("sessions-with-no-occurrences", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{{PinRef: p.PinRef, PinVersion: p.PinVersion, Delivery: DeliveryCounts{Sessions: 2}, Observation: Observation{Incomplete: 2}}}
		summary, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "capture delivery: 0 assessed correction occurrence(s); 2 delivered session(s): 0 no linked correction observed (qualified), 2 incomplete — "+command, summary.Line)
	})

	t.Run("no-timing-values", func(t *testing.T) {
		clone := *r
		clone.Pins = []PinReading{p}
		clone.Pins[0].Timing = nil
		summary, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "capture delivery: 3 assessed correction occurrence(s) (at-action 2, unknown 1); 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — "+command, summary.Line)
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
		summary, ok := Compact(&clone, p.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "capture delivery: 7 assessed correction occurrence(s) (at-action 1, earlier-in-context 1, other-lane 1, region-not-matched 1, not-emitted 1, unknown 1, mixed 1); timing before-construction 1, same-call-only 3, unknown 2, mixed 1; 5 delivered session(s): 2 no linked correction observed (qualified), 1 incomplete — "+command, summary.Line)
		assert.Greater(t, len([]rune(summary.Line)), 200, "the complete template must not be truncated")
	})

	t.Run("historical-refs", func(t *testing.T) {
		clone := *r
		old := p
		old.PinRef = ""
		old.PinVersion = "old"
		clone.Historical = []PinReading{old}
		summary, ok := Compact(&clone, old.PinVersion)
		require.True(t, ok)
		assert.Equal(t, command, summary.Command, "a current identity retains its current pin ref")

		clone.Pins = nil
		summary, ok = Compact(&clone, old.PinVersion)
		require.True(t, ok)
		assert.Equal(t, "seamark lessons --delivery --pin aaaaaaaaaaaa", summary.Command)
		assert.NotContains(t, summary.Line, old.PinIdentity, "no raw identity is exposed")
	})
}
