package lessonreport

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestBuildCountsEventsWithoutPins proves the count of events that show a
// delivery and carry no pin entry. The count leaves out evaluation, reset,
// and preparation events, version 1 records, and events with pins.
func TestBuildCountsEventsWithoutPins(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)

	suppressed := v2("e2", "s1", "m2")
	suppressed.Delivery = reviews.DeliverySuppressedRepeat

	// An event of two records with no pin entry in either record.
	twoFirst := v2("two-records", "s1", "m3")
	twoFirst.EventRecords = 2
	twoSecond := v2("two-records", "s1", "m3")
	twoSecond.EventSeq, twoSecond.EventRecords = 2, 2
	twoSecond.Delivery = reviews.DeliverySuppressedRepeat

	// An event of two records whose first record names a pin.
	pinnedFirst := v2("with-pins", "s1", "m4", pin(id, ver, reviews.PinInjected))
	pinnedFirst.EventRecords = 2
	pinnedSecond := v2("with-pins", "s1", "m4")
	pinnedSecond.EventSeq, pinnedSecond.EventRecords = 2, 2
	pinnedSecond.Delivery = reviews.DeliverySuppressedRepeat

	evaluation := v2("e5", "s1", "m5")
	evaluation.Delivery = reviews.DeliveryEvaluation

	reset := v2("e6", "s1", "")
	reset.Delivery, reset.MatchSHA = reviews.DeliveryReset, ""

	audit := auditLines(
		// Counted: an injected event and a suppressed one, both without pins.
		v2("e1", "s1", "m1"),
		suppressed,
		// Counted once: the event of two records without pins.
		twoFirst,
		twoSecond,
		// Not counted: the rest.
		pinnedFirst,
		pinnedSecond,
		evaluation,
		reset,
		reviews.Firing{SchemaVersion: 2, Surface: surfaceChangeSet, EventID: "prep-empty", EventSeq: 1, EventRecords: 1, Emission: "returned"},
		reviews.Firing{SchemaVersion: 2, Surface: surfacePrepare, EventID: "prep-empty-2", EventSeq: 1, EventRecords: 1, Emission: "returned"},
		reviews.Firing{Delivery: reviews.DeliveryInjected, Fired: []reviews.FiredLesson{{Region: "generated", Symptom: "frozen-assets"}}},
	)

	assert.Equal(t, 3, mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "").Coverage.EventsWithoutPins)

	t.Run("an unreadable audit counts none", func(t *testing.T) {
		in := Inputs{Audit: audit, Config: cfg, Now: fixedNow, AuditError: "boom"}

		assert.Equal(t, 0, mustBuild(t, in, "").Coverage.EventsWithoutPins)
	})
}

// TestBuildCountsUnknownLaneRecords proves the count of hook and
// exploration records that name no receiving context or an unknown lane.
// A preparation record and a version 1 record are not counted.
func TestBuildCountsUnknownLaneRecords(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)

	known := v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected))
	known.Lane = "root"

	noContext := v2("e2", "s2", "m2", pin(id, ver, reviews.PinInjected))
	noContext.ContextSHA = ""

	unknownLane := v2("e3", "s3", "m3", pin(id, ver, reviews.PinInjected))
	unknownLane.Surface, unknownLane.Lane = surfaceExplore, laneUnknown

	prepare := reviews.Firing{
		SchemaVersion: 2, Surface: surfacePrepare, EventID: "e4", EventSeq: 1, EventRecords: 1, Emission: "returned",
	}
	legacy := reviews.Firing{Delivery: reviews.DeliveryInjected, Fired: []reviews.FiredLesson{{Region: "generated", Symptom: "frozen-assets"}}}

	audit := auditLines(known, noContext, unknownLane, prepare, legacy)

	assert.Equal(t, 2, mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "").Coverage.UnknownLaneRecords)
}
