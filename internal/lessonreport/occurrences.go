package lessonreport

import "maps"

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
	an.coverage.DeniedCallInjections = idx.deniedInjections

	an.windows = newWindowBuilder(idx)

	return an
}

// fill copies the window reading of a version into a pin reading.
// Duplicate config entries of one version each get their own copy.
func (an *analysis) fill(p *PinReading, key versionKey) {
	p.Observation = an.windows.of(key).observation.copied()
}

// copyMap returns a copy of a map. The copy is never nil, also for a nil
// map, so it encodes as {} and not as null. maps.Clone keeps a nil map nil,
// so it does not fit here.
func copyMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	maps.Copy(out, m)

	return out
}

// copied returns the observation with its own maps, so two readings never
// share one.
func (o Observation) copied() Observation {
	o.IncompleteByReason = copyMap(o.IncompleteByReason)
	o.ByTiming = copyMap(o.ByTiming)

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
