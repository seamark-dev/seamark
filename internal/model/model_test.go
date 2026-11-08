package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOriginConfidenceOrdersTiers proves the display order of the edge
// origins. A resolved tier ranks above a name match, and an unknown origin
// ranks below every known tier.
func TestOriginConfidenceOrdersTiers(t *testing.T) {
	cases := []struct {
		origin string
		want   int
	}{
		{OriginQualified, 4},
		{OriginSameClass, 3},
		{OriginSamePackage, 2},
		{OriginUniqueName, 1},
		{OriginParse, 0},
		{"", 0},
		{"something-else", 0},
	}

	for _, c := range cases {
		t.Run(c.origin, func(t *testing.T) {
			assert.Equal(t, c.want, OriginConfidence(c.origin))
		})
	}
}
