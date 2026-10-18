package reviews

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollapseRestatedPinsListsTheDroppedPins(t *testing.T) {
	// A selection sample names each restatement, so the dropped pins
	// come back in input order. The count of CollapseRestated is their
	// length.
	lint := SurfacedPin{Pin: PinRule{Rule: "lint-before-commit",
		Note: "Run every configured linter and fix findings before committing work."}}
	parity := SurfacedPin{Pin: PinRule{Rule: "train-serve-parity",
		Note: "Serving must compute features exactly as the historical materialization does."}}
	restated := SurfacedPin{Pin: PinRule{Rule: "run-linters-before-commit",
		Note: "Run every linter the repository configures and fix the findings before committing."}}
	code := SurfacedPin{Pin: PinRule{Rule: "RUF001"}}
	sameCode := SurfacedPin{Pin: PinRule{Rule: "ruf001"}, Rank: 2}

	pins := []SurfacedPin{lint, parity, restated, code, sameCode}

	orig := make([]SurfacedPin, len(pins))
	copy(orig, pins)

	kept, dropped := CollapseRestatedPins(pins)

	assert.Equal(t, []SurfacedPin{lint, parity, code}, kept)
	assert.Equal(t, []SurfacedPin{restated, sameCode}, dropped)
	assert.Equal(t, orig, pins, "the input slice is not storage for the result")

	keptCount, count := CollapseRestated(pins)
	assert.Equal(t, kept, keptCount, "both forms keep the same pins")
	assert.Equal(t, len(dropped), count, "the count is the length of the dropped list")
}

func TestCollapseRestatedPinsKeepsShortInput(t *testing.T) {
	// Fewer than two pins cannot restate each other. The input comes
	// back as is and nothing is dropped.
	for _, pins := range [][]SurfacedPin{nil, {{Pin: PinRule{Rule: "only-pin"}}}} {
		kept, dropped := CollapseRestatedPins(pins)

		assert.Equal(t, pins, kept)
		assert.Nil(t, dropped)
	}

	// Distinct pins drop nothing, so the dropped list stays nil.
	kept, dropped := CollapseRestatedPins([]SurfacedPin{
		{Pin: PinRule{Rule: "E501"}},
		{Pin: PinRule{Rule: "E702"}},
	})
	require.Len(t, kept, 2)
	assert.Nil(t, dropped)
}
