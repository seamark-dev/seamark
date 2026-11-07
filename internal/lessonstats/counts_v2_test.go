package lessonstats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// countsV2Events are the v2 events of one day and the day before: an
// edit-hook injection, an exploration injection, and a check record.
// When withNewKinds is true, the zero-match evaluation and the reset of
// a writer with the recording switch on follow the injection.
func countsV2Events(root string, now time.Time, withNewKinds bool) []reviews.AuditEvent {
	lesson := []model.Lesson{{Region: "api", Symptom: "first-pin — a"}}
	resettable := true

	edit := reviews.AuditEvent{
		Root: root, Tool: "Edit", Files: []string{"api/a.go"}, ClientID: "claude",
		Mechanism: "pre-tool-use-context", SessionID: "session-1", MatchID: "tool-use-1",
		ReceiverID: "session-1", Lane: "root", Resettable: &resettable,
		Records:  []reviews.EventRecord{{Status: reviews.DeliveryInjected, Lessons: lesson, ContextBytes: 40}},
		Emission: "emitted", Evaluation: "complete", PathsComplete: true, Generation: 1, Now: now,
	}

	explore := edit
	explore.Surface = "explore"
	explore.Tool = "Read"
	explore.Mechanism = "post-tool-use-context"
	explore.MatchID = "tool-use-2"

	check := reviews.AuditEvent{
		Root: root, Surface: "check", Files: []string{"api/a.go"},
		Records:  []reviews.EventRecord{{Lessons: lesson}},
		Emission: "returned", Evaluation: "complete", PathsComplete: true, Now: now.AddDate(0, 0, -1),
	}

	if !withNewKinds {
		return []reviews.AuditEvent{edit, explore, check}
	}

	evaluation := edit
	evaluation.Files = []string{"docs/readme.md"}
	evaluation.MatchID = "tool-use-3"
	evaluation.Records = []reviews.EventRecord{{Status: reviews.DeliveryEvaluation}}
	evaluation.Emission = "not-needed"
	evaluation.CaptureOptIn = true

	reset := reviews.AuditEvent{
		Root: root, ClientID: "claude", SessionID: "session-1", ReceiverID: "session-1",
		Lane: "root", Resettable: &resettable,
		Records:    []reviews.EventRecord{{Status: reviews.DeliveryReset}},
		Generation: 2, ExploreGeneration: 2, CaptureOptIn: true, Now: now,
	}

	return []reviews.AuditEvent{edit, evaluation, reset, explore, check}
}

// TestDailyCountsSkipEvaluationAndResetRecords feeds a v2 log to
// DailyCounts (contract L3, Legacy readers of v2 records). Evaluation
// and reset records are not deliveries, so they change no day count.
// An exploration injection is a delivery but not a hook delivery.
func TestDailyCountsSkipEvaluationAndResetRecords(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)

	daysOf := func(withNewKinds bool) []DayCount {
		root := t.TempDir()

		for _, ev := range countsV2Events(root, now, withNewKinds) {
			_, err := reviews.RecordEvent(ev)
			require.NoError(t, err)
		}

		firings, err := reviews.ReadFirings(root)
		require.NoError(t, err)

		for _, f := range firings {
			require.Equal(t, reviews.AuditSchemaVersion, f.SchemaVersion, "every record is v2")
		}

		days, undated := DailyCounts(firings, now)
		require.Zero(t, undated)

		return days
	}

	days := daysOf(true)
	require.Len(t, days, DeliveryDays)

	assert.Equal(t, DayCount{Day: days[DeliveryDays-1].Day, Delivered: 2, Hook: 1}, days[DeliveryDays-1],
		"the edit and the exploration injections; the evaluation and the reset are not deliveries")
	assert.Equal(t, DayCount{Day: days[DeliveryDays-2].Day, Delivered: 1, Hook: 0}, days[DeliveryDays-2],
		"the check record of the day before")

	assert.Equal(t, daysOf(false), days, "the new record kinds change no day count")
}
