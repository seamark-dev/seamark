package settings

import (
	"testing"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile/pinfiletest"
)

// BenchmarkPhase4PlanSettings measures one PlanPatch call of a lessons
// group (make tui-measure): the splice, the lessons verifier, and the
// activation notes. The verifier compares the pin text and every other
// line of lessons.yaml, so the sizes grow the pin list and the mute
// list. Each sub-benchmark builds its own workspace before the timed
// loop. So a plain go test run builds no fixture.
func BenchmarkPhase4PlanSettings(b *testing.B) {
	ops := []struct {
		name string
		edit Edit
	}{
		{name: "mute-add", edit: add("F541", "vendor/")},
		{name: "threshold", edit: Edit{Field: Threshold, Int: 3}},
	}

	for _, s := range pinfiletest.Sweep(pinfiletest.DimPins, pinfiletest.DimMutes) {
		for _, op := range ops {
			b.Run(s.Name()+"/op="+op.name, func(b *testing.B) {
				text := pinfiletest.Lessons(b, pinfiletest.Pins(s.Pins, "src", ""), pinfiletest.Mutes(s.Mutes))
				snap := inspect(b, withLessons(b, text))
				reg := integration.Builtin()

				for b.Loop() {
					p, err := PlanPatch(snap, reg, []Edit{op.edit})
					if err != nil || p.Bytes.After == nil {
						b.Fatalf("the plan has no candidate: error %v", err)
					}
				}
			})
		}
	}
}
