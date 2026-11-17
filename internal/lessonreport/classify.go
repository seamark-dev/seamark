package lessonreport

// This file holds the closed list of the action-level delivery classes.
// This build has no source for a class, so no reading gets one, and
// every class count of the report stays at zero. The values stay,
// because the delivery/1 format names them.

// The action-level classes.
const (
	ClassAtAction         Class = "at-action"
	ClassEarlierInContext Class = "earlier-in-context"
	ClassOtherLane        Class = "other-lane"
	ClassRegionNotMatched Class = "region-not-matched"
	ClassNotEmitted       Class = "not-emitted"
	ClassUnknown          Class = "unknown"
)

// PairMixed is the class of an occurrence whose actions disagree.
const PairMixed PairClass = "mixed"

// laneUnknown is the lane value of an audit record whose receiver lane is
// not known.
const laneUnknown = "unknown"

// emissionEmitted is the emission value of a hook record whose reminder
// the hook wrote. The position lists of scans.go read it, because a hook
// injection counts in its context only when the hook wrote the reminder.
const emissionEmitted = "emitted"

// actionRefLength is the length of a printed key or digest prefix.
const actionRefLength = 12

// shortRef returns the printed prefix of a key or digest.
func shortRef(key string) string {
	return key[:min(actionRefLength, len(key))]
}
