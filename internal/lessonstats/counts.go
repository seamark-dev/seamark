package lessonstats

import (
	"fmt"
	"sort"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// DeliveryDays is the number of days that Snapshot.Days covers. The
// console shows windows of 7, 14 and 30 days, so 30 covers them all.
const DeliveryDays = 30

// TopRegionCount is the number of regions that LessonCounts.TopRegions
// keeps. The console shows one bar for each of them.
const TopRegionCount = 5

// LessonCounts is the active lesson set by source. A mined lesson is
// active when it clears the surfacing bar: the recurrence threshold and
// the mute rules of lessons.yaml. Pins always surface.
type LessonCounts struct {
	// Mined is the number of active mined lessons.
	Mined int
	// Inactive is the number of mined lessons below the threshold or
	// muted. They stay in the index but no surface shows them.
	Inactive int
	// Pins is the number of pin rules in lessons.yaml.
	Pins int
	// TopRegions holds the regions with the most active mined lessons,
	// at most TopRegionCount, most lessons first. Ties break on the
	// region, so a refresh keeps the order.
	TopRegions []RegionCount
}

// Active returns the number of lessons that the surfaces can show.
func (c LessonCounts) Active() int {
	return c.Mined + c.Pins
}

// RegionCount is the number of active mined lessons in one region. The
// region is the file or directory that the lesson names, unchanged.
type RegionCount struct {
	Region  string
	Lessons int
}

// DayCount holds the delivered records of one local day.
type DayCount struct {
	// Day is the local midnight that starts the day.
	Day time.Time
	// Delivered counts the delivered records on every surface.
	Delivered int
	// Hook counts the delivered edit-hook records, a subset of
	// Delivered.
	Hook int
}

// lessonCounts counts the active lessons. indexed is false without an
// index. A failed source returns an error and no counts, because a
// failed read must never render as zero.
func lessonCounts(indexed bool, mined []model.Lesson, minedErr error,
	cfg *reviews.Config, cfgErr error,
) (*LessonCounts, error) {
	switch {
	case !indexed:
		return nil, fmt.Errorf("%w: lesson counts need the mined lessons", ErrNoIndex)
	case cfgErr != nil:
		return nil, cfgErr
	case minedErr != nil:
		return nil, minedErr
	}

	counts := &LessonCounts{Pins: len(cfg.Pin)}
	byRegion := map[string]int{}

	for _, l := range mined {
		// SurfacesMined is the rule that every surface applies, so the
		// count matches what an agent can receive.
		if !cfg.SurfacesMined(l) {
			counts.Inactive++

			continue
		}

		counts.Mined++
		byRegion[l.Region]++
	}

	counts.TopRegions = topRegions(byRegion, TopRegionCount)

	return counts, nil
}

// topRegions returns the n regions with the most lessons.
func topRegions(byRegion map[string]int, n int) []RegionCount {
	out := make([]RegionCount, 0, len(byRegion))

	for region, count := range byRegion {
		out = append(out, RegionCount{Region: region, Lessons: count})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Lessons != out[j].Lessons {
			return out[i].Lessons > out[j].Lessons
		}

		return out[i].Region < out[j].Region
	})

	if len(out) > n {
		out = out[:n]
	}

	return out
}

// DailyCounts counts the delivered records of each local day for the
// DeliveryDays days that end on the day of now. It applies the rule of
// reviews.Summarize: a record is delivered when it has no delivery
// status or when its status is "injected". A suppressed repeat is not a
// delivery. undated counts delivered records whose time does not parse.
func DailyCounts(firings []reviews.Firing, now time.Time) (days []DayCount, undated int) {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	days = make([]DayCount, DeliveryDays)
	index := make(map[string]int, DeliveryDays)

	for i := range days {
		// AddDate keeps local midnight across a daylight saving change,
		// where a fixed 24-hour step would not.
		days[i].Day = today.AddDate(0, 0, i-(DeliveryDays-1))
		index[days[i].Day.Format(time.DateOnly)] = i
	}

	for _, f := range firings {
		if !f.Delivered() {
			continue
		}

		at, err := time.Parse(time.RFC3339, f.TS)
		if err != nil {
			undated++

			continue
		}

		i, ok := index[at.In(loc).Format(time.DateOnly)]
		if !ok {
			// The record is older than the window, or its clock ran ahead.
			continue
		}

		days[i].Delivered++

		if f.Surface == "" || f.Surface == "hook" {
			days[i].Hook++
		}
	}

	return days, undated
}
