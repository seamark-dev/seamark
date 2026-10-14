package services

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/pinfile/pinfiletest"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
)

// sampleScale builds the workspace of one size on a real index. Each
// region slot that a path enters holds ten mined lessons. lessons.yaml
// holds the pins and mutes of s, and the ledger holds s.Applied applied
// rows. The request is a mute review: its candidate adds one mute to the
// saved text. The caller sets the surface.
func sampleScale(b *testing.B, s pinfiletest.Size) (*engine, SampleRequest) {
	b.Helper()

	ws, _ := servicestest.Workspace(b, "")
	pins := pinfiletest.Pins(s.Pins, "api", "")
	mutes := pinfiletest.Mutes(s.Mutes)

	st, err := store.Open(ws.DBPath)
	require.NoError(b, err)
	require.NoError(b, st.ReplaceLessons(pinfiletest.Mined(s.Paths, 10, "api"), nil))

	for _, row := range pinfiletest.Applied(pins, s.Applied) {
		require.NoError(b, storetest.InsertProposal(st, &row))
	}

	require.NoError(b, st.Close())

	saved := pinfiletest.Lessons(b, pins, mutes)
	review := pinfiletest.Lessons(b, pins, append(mutes, reviews.MuteRule{Rule: "F541"}))
	servicestest.Write(b, ws, pinfile.File, saved)

	return engNew(ws, Deps{}), SampleRequest{
		Paths: pinfiletest.Paths(s.Paths, "api"), Before: []byte(saved), After: []byte(review),
	}
}

// BenchmarkPhase4Sample measures one selection sample of a mute review
// on the real engine (make tui-measure). A sample opens the index and
// reads lessons.yaml from disk to compare it with the saved text. It
// parses both lessons texts, names the paths by the rule of the
// surface, and checks each path on disk. Then it reads the evidence
// once and folds it twice. The two parses grow with the pin count, so
// they take a large part of a row with many pins. Each size builds its
// own indexed workspace before the timed loops, so a plain go test run
// builds no fixture.
func BenchmarkPhase4Sample(b *testing.B) {
	surfaces := []struct {
		name    string
		surface SampleSurface
	}{
		{name: "hook", surface: SampleHook},
		{name: "change-set", surface: SampleChangeSet},
	}

	sizes := pinfiletest.Sweep(pinfiletest.DimPins, pinfiletest.DimMutes, pinfiletest.DimPaths, pinfiletest.DimApplied)
	for _, s := range sizes {
		b.Run(s.Name(), func(b *testing.B) {
			e, req := sampleScale(b, s)

			for _, sf := range surfaces {
				b.Run("surface="+sf.name, func(b *testing.B) {
					req.Surface = sf.surface

					for b.Loop() {
						view := e.Sample(req)
						if view.Unavailable != "" || len(view.Current.Lessons) == 0 {
							b.Fatalf("the sample selected nothing: %q", view.Unavailable)
						}
					}
				})
			}
		})
	}
}
