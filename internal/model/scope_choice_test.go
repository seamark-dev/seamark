package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestScopeChoiceDateReadsUTC proves that one stamp reads as one date on
// every machine. Each stamp sits near midnight UTC, so the local date of a
// zone on that side of UTC differs from the UTC date.
func TestScopeChoiceDateReadsUTC(t *testing.T) {
	local := time.Local

	t.Cleanup(func() { time.Local = local })

	cases := []struct {
		name string
		zone *time.Location
		at   time.Time
		want string
	}{
		{"east of UTC", time.FixedZone("east", 9*60*60), time.Date(2026, 8, 21, 23, 30, 0, 0, time.UTC), "2026-08-21"},
		{"west of UTC", time.FixedZone("west", -9*60*60), time.Date(2026, 8, 21, 0, 30, 0, 0, time.UTC), "2026-08-21"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			time.Local = tc.zone

			assert.Equal(t, tc.want, ScopeChoiceDate(tc.at.Unix()))
		})
	}
}
