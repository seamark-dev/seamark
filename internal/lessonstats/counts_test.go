package lessonstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// countsLesson builds a mined lesson with a unique cluster key.
func countsLesson(region, symptom string, occurrences int) model.Lesson {
	return model.Lesson{
		ClusterKey: region + "\x00" + symptom, Region: region, Symptom: symptom,
		Reviewer: "person", Occurrences: occurrences, LastTS: 10,
	}
}

// countsConfig writes a lessons.yaml with two pins and a mute rule for
// the db region.
func countsConfig(t *testing.T, root string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"mute:\n  - region: db\npin:\n  - rule: first-pin\n    region: api\n    note: a\n"+
			"  - rule: second-pin\n    region: '*'\n    note: b\n"), 0o644))
}

func TestGatherCountsActiveLessonsBySource(t *testing.T) {
	root := t.TempDir()
	countsConfig(t, root)

	st := openStore(t,
		countsLesson("api", "one", 3),
		countsLesson("api", "two", 2),
		countsLesson("api", "rare", 1), // below the default threshold
		countsLesson("db", "muted", 9), // muted by region
		countsLesson("web", "three", 4),
		countsLesson("web", "four", 2),
		countsLesson("cli", "five", 2),
		countsLesson("docs", "six", 2),
		countsLesson("zeta", "seven", 2),
		countsLesson("alpha", "eight", 2),
	)

	snap := gatherAt(st, root, time.Now())
	require.NoError(t, snap.LessonsErr)
	require.NotNil(t, snap.Lessons)

	c := *snap.Lessons
	assert.Equal(t, 8, c.Mined)
	assert.Equal(t, 2, c.Inactive, "the rare and the muted lesson do not surface")
	assert.Equal(t, 2, c.Pins)
	assert.Equal(t, 10, c.Active())

	// Most lessons first; ties break on the region; at most five rows.
	assert.Equal(t, []RegionCount{
		{Region: "api", Lessons: 2}, {Region: "web", Lessons: 2},
		{Region: "alpha", Lessons: 1}, {Region: "cli", Lessons: 1}, {Region: "docs", Lessons: 1},
	}, c.TopRegions)
}

func TestGatherKeepsLessonCountsWhenTheLogFails(t *testing.T) {
	root := t.TempDir()

	// A directory at the log path makes the read fail.
	require.NoError(t, os.MkdirAll(reviews.AuditLogPath(root), 0o755))

	snap := gatherAt(openStore(t, countsLesson("api", "one", 3)), root, time.Now())
	require.Error(t, snap.LogErr)
	require.NoError(t, snap.LessonsErr)
	require.NotNil(t, snap.Lessons, "the lesson counts do not need the delivery log")
	assert.Equal(t, 1, snap.Lessons.Mined)
	assert.Nil(t, snap.Days, "an unread log has no day counts")
}

func TestGatherReportsWhyLessonCountsAreMissing(t *testing.T) {
	root := t.TempDir()

	snap := gatherAt(nil, root, time.Now())
	assert.Nil(t, snap.Lessons)
	require.ErrorIs(t, snap.LessonsErr, ErrNoIndex)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("hook_delivery: sometimes\n"), 0o644))

	snap = gatherAt(openStore(t, countsLesson("api", "one", 3)), root, time.Now())
	assert.Nil(t, snap.Lessons, "a malformed config is never read as defaults")
	require.ErrorIs(t, snap.LessonsErr, reviews.ErrLessonsConfig)
}

func TestDailyCountsPlacesDeliveredRecordsByLocalDay(t *testing.T) {
	// UTC+2 moves a 23:30 UTC record to the next local day.
	loc := time.FixedZone("UTC+2", 2*60*60)
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, loc)

	firings := []reviews.Firing{
		{TS: "2026-09-29T08:00:00Z", Fired: []reviews.FiredLesson{pooled}},                                             // legacy hook
		{TS: "2026-09-29T09:00:00Z", Delivery: reviews.DeliveryInjected, Fired: []reviews.FiredLesson{pooled}},         // hook
		{TS: "2026-09-29T10:00:00Z", Delivery: reviews.DeliverySuppressedRepeat, Fired: []reviews.FiredLesson{pooled}}, // not delivered
		{TS: "2026-09-29T11:00:00Z", Delivery: "future-status", Fired: []reviews.FiredLesson{pooled}},                  // unknown
		{TS: "2026-09-28T23:30:00Z", Surface: "check", Fired: []reviews.FiredLesson{pooled}},                           // local Sep 29
		{TS: "2026-08-31T10:00:00Z", Surface: "change_set", Fired: []reviews.FiredLesson{pooled}},                      // first day
		{TS: "2026-08-30T10:00:00Z", Fired: []reviews.FiredLesson{pooled}},                                             // too old
		{TS: "2026-10-02T10:00:00Z", Fired: []reviews.FiredLesson{pooled}},                                             // ahead
		{TS: "yesterday", Fired: []reviews.FiredLesson{pooled}},                                                        // undated
	}

	days, undated := DailyCounts(firings, now)
	require.Len(t, days, DeliveryDays)
	assert.Equal(t, 1, undated)

	assert.Equal(t, time.Date(2026, 9, 29, 0, 0, 0, 0, loc), days[DeliveryDays-1].Day)
	assert.Equal(t, time.Date(2026, 8, 31, 0, 0, 0, 0, loc), days[0].Day)

	assert.Equal(t, DayCount{Day: days[DeliveryDays-1].Day, Delivered: 3, Hook: 2}, days[DeliveryDays-1])
	assert.Equal(t, DayCount{Day: days[0].Day, Delivered: 1, Hook: 0}, days[0])

	total := 0
	for _, d := range days {
		total += d.Delivered
	}

	assert.Equal(t, 4, total, "suppressed, unknown, old and future records stay out")
}

func TestGatherFillsTheDayCounts(t *testing.T) {
	root := t.TempDir()
	firings := mixedFirings()
	writeLog(t, root, firings)

	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	snap := gatherAt(nil, root, now)
	require.NoError(t, snap.LogErr)
	require.Len(t, snap.Days, DeliveryDays)

	want, undated := DailyCounts(firings, now)
	assert.Equal(t, want, snap.Days)
	assert.Equal(t, undated, snap.Undated)

	// Sep 1 holds the four hook records, of which three are delivered;
	// Sep 2 and Sep 3 hold one other surface each.
	assert.Equal(t, 3, snap.Days[DeliveryDays-3].Delivered)
	assert.Equal(t, 3, snap.Days[DeliveryDays-3].Hook)
	assert.Equal(t, 1, snap.Days[DeliveryDays-2].Delivered)
	assert.Equal(t, 0, snap.Days[DeliveryDays-2].Hook)
	assert.Equal(t, 1, snap.Days[DeliveryDays-1].Delivered)
}
