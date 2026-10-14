package report

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile/pinfiletest"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// BenchmarkPhase4LessonsForFilesBudget measures one hook selection: the
// evidence read and the hook fold. The Phase 4 limits compare it before
// and after the typed selection, so the name carries the phase. The
// benchmark uses only the production wrapper, so it also compiles
// against the code before the evidence split (make tui-measure runs it
// on both).
//
// The first three sub-benchmarks use the seeded fixture alone. The
// sized ones (pinfiletest.Sweep) add mined lessons, pins, mutes, applied
// rows, and paths at the sizes of the Phase 4 fixtures. Each one builds
// its own store before the timed loop, so a plain go test run builds no
// fixture.
func BenchmarkPhase4LessonsForFilesBudget(b *testing.B) {
	// goldenConfig starts from the default config. A struct literal has
	// threshold 0 and would select every mined lesson.
	cfg := goldenConfig(distinctPins("scripts", 10)...)

	cases := []struct {
		name  string
		files []string
		// applied adds one applied proposal for the first pin. Then the
		// read builds the confidence assessor, as on a real index.
		applied bool
	}{
		{name: "1 file", files: []string{"scripts/task.py"}},
		{name: "3 files", files: []string{"scripts/task.py", "scripts/sub/a.py", "web/b.ts"}},
		{name: "3 files with an applied row", files: []string{"scripts/task.py", "scripts/sub/a.py", "web/b.ts"}, applied: true},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			st, _ := seedStore(b)

			if tc.applied {
				pin := cfg.Pin[0]
				require.NoError(b, storetest.InsertProposal(st, &model.Proposal{
					Signature: "sig-bench", Rule: pin.Rule, Region: pin.Region, Note: pin.Note,
					Status: model.ProposalApplied,
				}))
			}

			budget := HookBudget(cfg)

			for b.Loop() {
				lessons, _, err := LessonsForFilesBudget(st, cfg, tc.files, budget)
				if err != nil || len(lessons) == 0 {
					b.Fatalf("selection failed: %d lessons, error %v", len(lessons), err)
				}
			}
		})
	}

	sizes := pinfiletest.Sweep(pinfiletest.DimPins, pinfiletest.DimMutes, pinfiletest.DimPaths, pinfiletest.DimApplied)
	for _, s := range sizes {
		b.Run(s.Name(), func(b *testing.B) {
			st := selectionScale(b, s)
			pins := pinfiletest.Pins(s.Pins, "scripts", "")

			for _, row := range pinfiletest.Applied(pins, s.Applied) {
				require.NoError(b, storetest.InsertProposal(st, &row))
			}

			sized := goldenConfig(pins...)
			sized.Mute = pinfiletest.Mutes(s.Mutes)
			files := pinfiletest.Paths(s.Paths, "scripts")
			budget := HookBudget(sized)

			for b.Loop() {
				lessons, _, err := LessonsForFilesBudget(st, sized, files, budget)
				if err != nil || len(lessons) == 0 {
					b.Fatalf("selection failed: %d lessons, error %v", len(lessons), err)
				}
			}
		})
	}
}

// selectionScale returns the seeded store with generated mined lessons
// in place of the two seeded ones. Each region slot that a path of s can
// enter holds ten. So each path reads ten mined lessons, and every mute
// is checked against them.
func selectionScale(b *testing.B, s pinfiletest.Size) *store.Store {
	b.Helper()

	st, _ := seedStore(b)
	require.NoError(b, st.ReplaceLessons(pinfiletest.Mined(s.Paths, 10, "scripts"), nil))

	return st
}
