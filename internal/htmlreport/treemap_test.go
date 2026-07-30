package htmlreport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overlaps reports whether two boxes share area. Touching edges are not
// an overlap — adjacent cells are exactly what a treemap is made of.
func overlaps(a, b Box) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W &&
		a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func assertNoOverlap(t *testing.T, boxes []Box) {
	t.Helper()

	const epsilon = 1e-6

	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			// Shrink both slightly so shared edges do not read as overlap.
			a, b := boxes[i], boxes[j]
			a.X, a.Y, a.W, a.H = a.X+epsilon, a.Y+epsilon, a.W-2*epsilon, a.H-2*epsilon
			b.X, b.Y, b.W, b.H = b.X+epsilon, b.Y+epsilon, b.W-2*epsilon, b.H-2*epsilon

			assert.Falsef(t, overlaps(a, b), "%s overlaps %s", boxes[i].Key, boxes[j].Key)
		}
	}
}

func TestSquarifyFillsTheRectangleWithoutOverlap(t *testing.T) {
	items := []weighted{{"a", 40}, {"b", 25}, {"c", 15}, {"d", 10}, {"e", 6}, {"f", 4}}

	boxes := squarify(items, 0, 0, mapWidth, mapHeight)
	require.Len(t, boxes, len(items), "every weighted item gets a box")
	assertNoOverlap(t, boxes)

	area := 0.0

	for _, b := range boxes {
		assert.Positive(t, b.W, b.Key)
		assert.Positive(t, b.H, b.Key)
		assert.GreaterOrEqual(t, b.X, 0.0, b.Key)
		assert.GreaterOrEqual(t, b.Y, 0.0, b.Key)
		assert.LessOrEqual(t, b.X+b.W, mapWidth+1e-6, b.Key)
		assert.LessOrEqual(t, b.Y+b.H, mapHeight+1e-6, b.Key)

		area += b.W * b.H
	}

	assert.InEpsilon(t, mapWidth*mapHeight, area, 0.02,
		"the boxes together cover the map")
}

func TestSquarifyAreaTracksWeight(t *testing.T) {
	// The whole point of a treemap: twice the weight, twice the area.
	boxes := squarify([]weighted{{"big", 60}, {"small", 30}}, 0, 0, 600, 400)
	require.Len(t, boxes, 2)

	got := map[string]float64{}
	for _, b := range boxes {
		got[b.Key] = b.W * b.H
	}

	assert.InEpsilon(t, 2.0, got["big"]/got["small"], 0.02)
}

func TestSquarifyIsDeterministic(t *testing.T) {
	items := []weighted{{"a", 9}, {"b", 9}, {"c", 3}, {"d", 1}}

	first := squarify(items, 0, 0, mapWidth, mapHeight)
	for range 5 {
		assert.Equal(t, first, squarify(items, 0, 0, mapWidth, mapHeight))
	}
}

func TestSquarifyDegenerateInput(t *testing.T) {
	assert.Empty(t, squarify(nil, 0, 0, 100, 100), "no items")
	assert.Empty(t, squarify([]weighted{{"a", 0}}, 0, 0, 100, 100), "no weight")
	assert.Empty(t, squarify([]weighted{{"a", 1}}, 0, 0, 0, 100), "no width")
	assert.Empty(t, squarify([]weighted{{"a", 1}}, 0, 0, 100, -5), "negative height")
}

func TestStripsGiveEveryCellRoomForALabel(t *testing.T) {
	// A weight distribution whose tail would be sub-pixel if drawn
	// strictly in proportion — exactly the slivers this layout exists
	// to avoid.
	items := []weighted{{"a", 500}, {"b", 40}, {"c", 3}, {"d", 1}, {"e", 1}}

	boxes, dropped := strips(items, 0, 0, 400, 60)
	require.NotEmpty(t, boxes)
	assertNoOverlap(t, boxes)

	for _, b := range boxes {
		assert.GreaterOrEqualf(t, b.W, minStrip-1e-6,
			"%s is too narrow to label (%.1fpx)", b.Key, b.W)
		assert.Equal(t, 60.0, b.H, "%s spans the box height", b.Key)
	}

	assert.Equal(t, len(items)-len(boxes), dropped,
		"what did not fit is reported, never silently missing")
}

func TestStripsFillTheBoxExactly(t *testing.T) {
	boxes, dropped := strips([]weighted{{"a", 3}, {"b", 2}, {"c", 1}}, 10, 20, 600, 80)
	require.Len(t, boxes, 3)
	assert.Zero(t, dropped)

	assert.Equal(t, 10.0, boxes[0].X, "the first strip starts at the box edge")

	last := boxes[len(boxes)-1]
	assert.InDelta(t, 610.0, last.X+last.W, 1e-6, "the last strip closes the box")
}

func TestStripsSwitchAxisWithTheBoxShape(t *testing.T) {
	wide, _ := strips([]weighted{{"a", 1}, {"b", 1}}, 0, 0, 400, 100)
	require.Len(t, wide, 2)
	assert.Equal(t, wide[0].Y, wide[1].Y, "a wide box is split into columns")

	tall, _ := strips([]weighted{{"a", 1}, {"b", 1}}, 0, 0, 100, 400)
	require.Len(t, tall, 2)
	assert.Equal(t, tall[0].X, tall[1].X, "a tall box is split into rows")
}

func TestStripsDropsWhatCannotBeDrawn(t *testing.T) {
	// Ten files in a box only wide enough for two legible strips.
	items := make([]weighted, 10)
	for i := range items {
		items[i] = weighted{fmt.Sprintf("f%d", i), float64(10 - i)}
	}

	boxes, dropped := strips(items, 0, 0, 2*minStrip, 40)
	assert.Len(t, boxes, 2)
	assert.Equal(t, 8, dropped)

	boxes, dropped = strips(items, 0, 0, 0, 40)
	assert.Empty(t, boxes, "a box with no width draws nothing")
	assert.Equal(t, 10, dropped)

	// Narrower than a single legible strip: draw nothing rather than one
	// unreadable sliver.
	boxes, dropped = strips(items, 0, 0, minStrip-1, 40)
	assert.Empty(t, boxes)
	assert.Equal(t, 10, dropped)
}

func TestAllocateRaisesSmallSharesToTheFloor(t *testing.T) {
	// One item dominates; without a floor the other two would be drawn
	// a few pixels wide.
	items := []weighted{{"a", 500}, {"b", 3}, {"c", 1}}
	sizes := allocate(items, 400)

	total := 0.0

	for i, size := range sizes {
		assert.GreaterOrEqualf(t, size, minStrip-1e-9,
			"%s is below the legibility floor", items[i].key)

		total += size
	}

	assert.InDelta(t, 400.0, total, 1e-9, "the shares consume the span exactly")
	assert.Greater(t, sizes[0], sizes[1], "the dominant item still gets the most room")
}

func TestByWeightIsStableForTiedWeights(t *testing.T) {
	items := []weighted{{"b", 5}, {"a", 5}, {"c", 9}}
	byWeight(items)

	assert.Equal(t, []weighted{{"c", 9}, {"a", 5}, {"b", 5}}, items,
		"ties break by key, so the same index draws the same map")
}

func TestInsetLeavesRoomForTheDirectoryLabel(t *testing.T) {
	x, y, w, h := inset(Box{"dir", 100, 200, 300, 150})

	assert.Equal(t, 104.0, x)
	assert.Equal(t, 217.0, y, "the files start below the directory label")
	assert.Equal(t, 292.0, w)
	assert.Equal(t, 129.0, h)

	// A box smaller than its own padding must not produce negative sizes.
	_, _, w, h = inset(Box{"tiny", 0, 0, 2, 2})
	assert.Positive(t, w)
	assert.Positive(t, h)
}
