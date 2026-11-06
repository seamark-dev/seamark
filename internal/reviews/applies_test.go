package reviews

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPinRuleAppliesToEqualsHookRule tests that the exported AppliesTo is
// the region rule of the edit hook. It gives the same result as
// appliesTo for every pin shape and scope, and it agrees with the pins
// that pinsFor selects. The delivery report needs this, because it must
// not apply a second region rule.
func TestPinRuleAppliesToEqualsHookRule(t *testing.T) {
	scopes := []string{"", "api", "api/x.go", "api/v1/y.go", "apix", "db/z.go", "docs"}

	cases := []struct {
		name string
		pin  PinRule
		// want holds the result for each scope, in the order of scopes.
		want []bool
	}{
		{"repo-wide by empty region", PinRule{Rule: "r"}, []bool{true, true, true, true, true, true, true}},
		{"repo-wide by star", PinRule{Rule: "r", Region: "*"}, []bool{true, true, true, true, true, true, true}},
		{"repo-wide by a star set", PinRule{Rule: "r", Regions: []string{"*"}}, []bool{true, true, true, true, true, true, true}},
		{"file region", PinRule{Rule: "r", Region: "api/x.go"}, []bool{true, true, true, false, false, false, false}},
		{"directory region", PinRule{Rule: "r", Region: "api"}, []bool{true, true, true, true, false, false, false}},
		{"directory with a trailing slash", PinRule{Rule: "r", Region: "api/"}, []bool{true, true, true, true, false, false, false}},
		{"nested directory region", PinRule{Rule: "r", Region: "api/v1"}, []bool{true, true, false, true, false, false, false}},
		{"region set", PinRule{Rule: "r", Regions: []string{"api", "db"}}, []bool{true, true, true, true, false, true, false}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Pin: []PinRule{tc.pin}}

			for i, scope := range scopes {
				got := tc.pin.AppliesTo(scope)

				assert.Equal(t, tc.want[i], got, "scope %q", scope)
				assert.Equal(t, tc.pin.appliesTo(scope), got, "scope %q equals appliesTo", scope)
				assert.Equal(t, got, len(cfg.pinsFor(scope)) == 1, "scope %q equals the pinsFor selection", scope)
			}
		})
	}
}
