package lessonreport

import "github.com/seamark-dev/seamark/internal/reviews"

// This file holds the reading that the pin readings of one report share:
// the coverage counts that the audit alone gives and the observation
// windows. An occurrence count needs the capture archive, which this
// build does not read. So the occurrence fields of every pin reading stay
// at zero.

// CodeUnassessed is the unlinked code of feedback that awaits an
// assessment. This build has no assessment, so no reading carries it. The
// value stays, because the delivery/1 format names it.
const CodeUnassessed Code = "unassessed"

// versionKey names one pin version.
type versionKey struct{ identity, version string }

// analysis is the reading that every pin reading of one report shares:
// the coverage counts that the audit alone gives and the window reading
// of each pin version.
type analysis struct {
	coverage Coverage
	windows  *windowBuilder
}

// analyze reads the coverage counts of the audit and prepares the window
// reading.
func analyze(idx *index) *analysis {
	an := &analysis{}
	an.coverage.EventsWithoutPins = idx.eventsWithoutPins()
	an.coverage.UnknownLaneRecords = idx.unknownLaneRecords()

	an.windows = newWindowBuilder(idx)

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

// fill copies the window reading of a version into a pin reading.
// Duplicate config entries of one version each get their own copy.
func (an *analysis) fill(p *PinReading, key versionKey) {
	p.Observation = an.windows.of(key).observation.copied()
}

func copyMap[K comparable](m map[K]int) map[K]int {
	out := make(map[K]int, len(m))
	for k, v := range m {
		out[k] = v
	}

	return out
}

// copied returns the observation with its own maps, so two readings never
// share one.
func (o Observation) copied() Observation {
	o.IncompleteByReason = copyMap(o.IncompleteByReason)

	timing := make(map[Timing]Partition, len(o.ByTiming))
	for k, v := range o.ByTiming {
		timing[k] = v
	}

	o.ByTiming = timing

	return o
}

// sessions returns the session windows of the versions of one identity, in
// the order of the versions.
func (an *analysis) sessions(identity string, versions []string) []SessionWindow {
	out := []SessionWindow{}

	for _, v := range versions {
		out = append(out, an.windows.of(versionKey{identity, v}).sessions...)
	}

	return out
}
