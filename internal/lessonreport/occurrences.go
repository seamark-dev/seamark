package lessonreport

import "github.com/seamark-dev/seamark/internal/reviews"

// This file reads the coverage counts that the audit alone gives. An
// occurrence count needs the capture archive, which this build does not
// read. So the occurrence fields of every pin reading stay at zero.

// CodeUnassessed is the unlinked code of feedback that awaits an
// assessment. This build has no assessment, so no reading carries it. The
// value stays, because the delivery/1 format names it.
const CodeUnassessed Code = "unassessed"

// analysis is the reading that every pin reading of one report shares:
// the coverage counts that the audit alone gives.
type analysis struct {
	coverage Coverage
}

// analyze reads the coverage counts of the audit.
func analyze(idx *index) *analysis {
	an := &analysis{}
	an.coverage.EventsWithoutPins = idx.eventsWithoutPins()
	an.coverage.UnknownLaneRecords = idx.unknownLaneRecords()

	return an
}

// eventsWithoutPins counts the v2 events that show an injected or a
// suppressed delivery and have no pin entry in any of their records. An
// evaluation event, a reset event, and a preparation event that returned
// nothing show no such delivery.
func (idx *index) eventsWithoutPins() int {
	n := 0

	for _, positions := range idx.byEvent {
		var delivered, pinned bool

		for _, i := range positions {
			f := idx.lines[i].Firing
			if f.SchemaVersion == 0 {
				continue
			}

			delivered = delivered || f.Delivery == reviews.DeliveryInjected || f.Delivery == reviews.DeliverySuppressedRepeat
			pinned = pinned || len(f.Pins) > 0
		}

		if delivered && !pinned {
			n++
		}
	}

	return n
}

// unknownLaneRecords counts the v2 records of the hook and the
// exploration surface that name no receiving context or an unknown lane.
func (idx *index) unknownLaneRecords() int {
	n := 0

	for _, l := range idx.lines {
		f := l.Firing
		if f.SchemaVersion == 0 || (f.Surface != "" && f.Surface != surfaceExplore) {
			continue
		}

		if f.ContextSHA == "" || f.Lane == laneUnknown {
			n++
		}
	}

	return n
}
