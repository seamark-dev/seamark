package lessonreport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the plain walks over the audit that the classifier used
// before the position lists, and the tests that compare both. The walks
// stay as the reference: a report from the position lists must equal the
// report from the walks, field for field and byte for byte.

// linearScans answers each positionScans question with a walk over the
// keyed lookups of the index from their start.
type linearScans struct {
	idx *index
}

func newLinearScans(idx *index) positionScans {
	return linearScans{idx: idx}
}

func (s linearScans) latestPreparation(version string, before int) int {
	idx := s.idx
	found := -1

	for _, i := range idx.byVersion[version] {
		if i >= before {
			break
		}

		f := idx.lines[i].Firing
		if (f.Surface == surfaceChangeSet || f.Surface == surfacePrepare) &&
			deliveriesOf(f, version)[reviews.PinReturned] {
			found = i
		}
	}

	return found
}

func (s linearScans) earlierInjections(session, version string, before int) []int {
	idx := s.idx

	var out []int

	for _, i := range idx.bySession[session] {
		if i >= before {
			break
		}

		f := idx.lines[i].Firing
		if f.SchemaVersion > 0 && receiverSurface(f.Surface) && injects(f, version) &&
			!idx.isDeniedInjection(f) {
			out = append(out, i)
		}
	}

	return out
}

func (s linearScans) laneInjections(session, ctx, version string, before int) []int {
	var out []int

	for _, i := range s.earlierInjections(session, version, before) {
		if ctx != "" && s.idx.lines[i].Firing.ContextSHA == ctx {
			out = append(out, i)
		}
	}

	return out
}

func (s linearScans) latestContextInjection(ctx, version string, generation uint64, before int) int {
	idx := s.idx
	found := -1

	for _, i := range idx.byContext[ctx] {
		if i >= before {
			break
		}

		f := idx.lines[i].Firing
		if f.Surface == "" && f.Generation == generation && f.Emission == emissionEmitted &&
			injects(f, version) && !idx.isDeniedInjection(f) {
			found = i
		}
	}

	return found
}

func (s linearScans) resetBetween(ctx string, from, to int) bool {
	for _, i := range s.idx.byContext[ctx] {
		if i >= to {
			break
		}

		if i > from && s.idx.lines[i].Firing.Delivery == reviews.DeliveryReset {
			return true
		}
	}

	return false
}

// requireSameAsLinear builds the report again with the plain walks and
// requires an equal Report, equal JSON, and equal text.
func requireSameAsLinear(t testing.TB, in Inputs, ref string, got Report) {
	t.Helper()

	want, err := buildWith(in, ref, newLinearScans)
	require.NoError(t, err)
	require.Equal(t, want, got)

	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)

	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	require.Equal(t, string(wantJSON), string(gotJSON))

	var wantText, gotText bytes.Buffer

	require.NoError(t, RenderText(&wantText, want))
	require.NoError(t, RenderText(&gotText, got))
	require.Equal(t, wantText.String(), gotText.String())
}

// The keys of the scan audits. Each list holds the empty key, because an
// empty session, context, or version has its own rule in every scan.
var (
	scanSessions = []string{"", "s1", "s2"}
	scanContexts = []string{"", "c1", "c2", "c3"}
	scanVersions = []string{"", "v1", "v2", "v3"}
	scanMatches  = []string{"", "m-ok", "m-denied"}
	scanSurfaces = []string{"", surfaceExplore, surfaceChangeSet, surfacePrepare}
	scanPinKinds = []reviews.PinDelivery{
		reviews.PinInjected, reviews.PinSuppressedRepeat, reviews.PinHeldBack, reviews.PinReturned,
	}
)

// scanIndex returns the index of a list of records. The guard denied the
// call "m-denied".
func scanIndex(records []reviews.Firing) *index {
	in := Inputs{
		Audit: auditLines(records...),
		CheckAudit: lessoncheck.AuditPrefix{Records: []lessoncheck.AuditRecord{{
			Kind: lessoncheck.AuditGuard, MatchSHA: "m-denied",
			Guard: &lessoncheck.GuardDecision{Decision: lessoncheck.GuardDeny},
		}}},
	}

	return newIndex(in, StateRead, StateRead)
}

// requireScansEqual asks both paths every question for every key and every
// position from before the first record to after the last.
func requireScansEqual(t *testing.T, idx *index) {
	t.Helper()

	got, want := idx.scans, newLinearScans(idx)
	n := len(idx.lines)

	for before := -1; before <= n+1; before++ {
		for _, v := range scanVersions {
			require.Equal(t, want.latestPreparation(v, before), got.latestPreparation(v, before),
				"latestPreparation %q before %d", v, before)

			for _, s := range scanSessions {
				require.Equal(t, want.earlierInjections(s, v, before), got.earlierInjections(s, v, before),
					"earlierInjections %q %q before %d", s, v, before)

				for _, c := range scanContexts {
					require.Equal(t, want.laneInjections(s, c, v, before), got.laneInjections(s, c, v, before),
						"laneInjections %q %q %q before %d", s, c, v, before)
				}
			}

			for _, c := range scanContexts {
				for g := range uint64(4) {
					require.Equal(t, want.latestContextInjection(c, v, g, before), got.latestContextInjection(c, v, g, before),
						"latestContextInjection %q %q %d before %d", c, v, g, before)
				}
			}
		}

		for _, c := range scanContexts {
			for from := -1; from <= n; from++ {
				require.Equal(t, want.resetBetween(c, from, before), got.resetBetween(c, from, before),
					"resetBetween %q from %d to %d", c, from, before)
			}
		}
	}
}

// injectedHook is an emitted v2 hook record that injects the versions.
func injectedHook(session, ctx string, generation uint64, versions ...string) reviews.Firing {
	f := reviews.Firing{
		SchemaVersion: 2, SessionSHA: session, ContextSHA: ctx, Generation: generation,
		Emission: emissionEmitted, MatchSHA: "m-ok", Delivery: reviews.DeliveryInjected,
	}

	for _, v := range versions {
		f.Pins = append(f.Pins, reviews.FiringPin{Version: v, Delivery: reviews.PinInjected})
	}

	return f
}

// TestPositionScansEdgeCases proves both paths equal on a hand-built audit
// that holds each edge case, and states the answer of each case.
func TestPositionScansEdgeCases(t *testing.T) {
	denied := injectedHook("s1", "c1", 1, "v1")
	denied.MatchSHA = "m-denied"

	noLane := injectedHook("s1", "", 1, "v1")
	reset := reviews.Firing{SchemaVersion: 2, ContextSHA: "c1", Delivery: reviews.DeliveryReset}
	prepared := reviews.Firing{SchemaVersion: 2, Surface: surfacePrepare, Pins: []reviews.FiringPin{
		{Version: "v1", Delivery: reviews.PinReturned},
	}}

	records := []reviews.Firing{
		prepared,                                // 0: preparation that returned v1
		injectedHook("s1", "c1", 1, "v1"),       // 1: lane c1 of session s1
		injectedHook("s1", "c2", 1, "v1"),       // 2: a second lane of the same session
		injectedHook("s2", "c1", 1, "v1"),       // 3: the same lane digest in a second session
		noLane,                                  // 4: a record with no lane
		denied,                                  // 5: a denied call
		reset,                                   // 6: a reset of lane c1
		injectedHook("s1", "c1", 1, "v1", "v1"), // 7: two pin entries of one version
		injectedHook("s1", "c1", 2, "v2"),       // 8: a second generation and version
		reset,                                   // 9: a reset at the position of an action
	}

	idx := scanIndex(records)
	requireScansEqual(t, idx)

	s := idx.scans

	t.Run("empty list", func(t *testing.T) {
		assert.Nil(t, s.earlierInjections("s1", "v3", 10))
		assert.Equal(t, -1, s.latestPreparation("v3", 10))
		assert.False(t, s.resetBetween("c3", -1, 10))
	})

	t.Run("position before the first entry", func(t *testing.T) {
		assert.Nil(t, s.earlierInjections("s1", "v1", 1))
		assert.Equal(t, -1, s.latestPreparation("v1", 0))
		assert.Equal(t, -1, s.latestContextInjection("c1", "v1", 1, 1))
	})

	t.Run("equal positions stay out", func(t *testing.T) {
		assert.Equal(t, []int{1, 2, 4}, s.earlierInjections("s1", "v1", 7))
		assert.Equal(t, []int{1, 2, 4, 7}, s.earlierInjections("s1", "v1", 8))
		assert.False(t, s.resetBetween("c1", 6, 7))
		assert.False(t, s.resetBetween("c1", 5, 6))
	})

	t.Run("one version in two sessions and lanes", func(t *testing.T) {
		assert.Equal(t, []int{1, 7}, s.laneInjections("s1", "c1", "v1", 10))
		assert.Equal(t, []int{2}, s.laneInjections("s1", "c2", "v1", 10))
		assert.Equal(t, []int{3}, s.laneInjections("s2", "c1", "v1", 10))
		assert.Equal(t, 7, s.latestContextInjection("c1", "v1", 1, 10))
	})

	t.Run("record with no lane", func(t *testing.T) {
		assert.Nil(t, s.laneInjections("s1", "", "v1", 10))
		assert.Equal(t, -1, s.latestContextInjection("", "v1", 1, 10))
	})

	t.Run("reset at the position of an action", func(t *testing.T) {
		assert.False(t, s.resetBetween("c1", 8, 9))
		assert.True(t, s.resetBetween("c1", 8, 10))
		assert.True(t, s.resetBetween("c1", 1, 7))
	})

	t.Run("an empty version asks about any injected pin", func(t *testing.T) {
		assert.Equal(t, []int{1, 2, 4, 7, 8}, s.earlierInjections("s1", "", 10))
	})
}

// TestPositionScansMatchLinear proves both paths equal on random audits
// over small key sets, so keys and positions collide often.
func TestPositionScansMatchLinear(t *testing.T) {
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // a fixed seed keeps the test deterministic.
		records := make([]reviews.Firing, 20+rng.IntN(40))

		for i := range records {
			records[i] = randomScanRecord(rng)
		}

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			requireScansEqual(t, scanIndex(records))
		})
	}
}

// randomScanRecord draws one record from the scan keys.
func randomScanRecord(rng *rand.Rand) reviews.Firing {
	pick := func(list []string) string { return list[rng.IntN(len(list))] }

	f := reviews.Firing{
		SchemaVersion: 2 * rng.IntN(2),
		Surface:       pick(scanSurfaces),
		SessionSHA:    pick(scanSessions),
		ContextSHA:    pick(scanContexts),
		MatchSHA:      pick(scanMatches),
		Generation:    uint64(rng.IntN(3)),
	}

	if rng.IntN(3) > 0 {
		f.Emission = emissionEmitted
	}

	if rng.IntN(6) == 0 {
		f.Delivery = reviews.DeliveryReset
	}

	for range rng.IntN(4) {
		f.Pins = append(f.Pins, reviews.FiringPin{
			Version: pick(scanVersions), Delivery: scanPinKinds[rng.IntN(len(scanPinKinds))],
		})
	}

	return f
}

// TestBuildMatchesLinearOnBenchShapes proves an equal report from both
// paths on the two bench shapes at small sizes with several seeds. It
// covers the overview and the expansion of pin A.
func TestBuildMatchesLinearOnBenchShapes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 24 synthetic logs")
	}

	for _, records := range []int{300, 1_000, 3_000} {
		for _, shape := range benchShapes {
			for _, seed := range []uint64{benchSeed, 1, 2, 3} {
				t.Run(fmt.Sprintf("records=%d/shape=%s/seed=%d", records, shape.name, seed), func(t *testing.T) {
					in, _ := newSeededBenchInputs(t, records, shape, seed)
					ref := in.Config.ShortPinRef(reviews.NewPinKey("keep-parser-strict", "internal/parse", nil))

					for _, pinRef := range []string{"", ref} {
						got, err := Build(in, pinRef)
						require.NoError(t, err)
						requireSameAsLinear(t, in, pinRef, got)
					}
				})
			}
		}
	}
}
