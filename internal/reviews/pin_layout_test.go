package reviews

// Tests of the region layout that the pin writers share.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPinForRegionsWritesTheSharedRegionLayout(t *testing.T) {
	cases := []struct {
		name string
		set  []string
		want PinRule
	}{
		{"repository-wide", nil, PinRule{Rule: "r", Region: "*", Note: "n"}},
		{"empty set", []string{}, PinRule{Rule: "r", Region: "*", Note: "n"}},
		{"one region", []string{"api"}, PinRule{Rule: "r", Region: "api", Note: "n"}},
		{"a set keeps its order", []string{"db", "api"}, PinRule{Rule: "r", Region: "db", Regions: []string{"db", "api"}, Note: "n"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin := PinForRegions("r", "n", tc.set)

			assert.Equal(t, tc.want, pin)
			assert.Equal(t, NewPinKey("r", "", tc.set), NewPinKey(pin.Rule, pin.Region, pin.Regions),
				"the layout keeps the key of the set")
		})
	}
}
