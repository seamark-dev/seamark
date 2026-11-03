package lessonreport

import (
	"maps"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// index holds the keyed lookups of the audit prefix (read-only).
// newIndex builds them in one pass over the lines, so no pin and no
// episode rescans the log. Every list holds positions into lines, in file
// order, because the file order of the append-only log orders the
// records. Times never do.
//
// byVersion serves the delivery counts and the windows. byEvent serves
// the coverage counts and the totals. byMatch, bySession, and byContext
// serve the action classes and the decision timing. These need a capture
// archive, so this build fills the three lookups but does not read them.
type index struct {
	lines []reviews.AuditLine

	byMatch   map[string][]int // match_sha256 → positions
	byContext map[string][]int // context_sha256 → positions
	bySession map[string][]int // session_sha256 → positions
	byEvent   map[string][]int // event_id → positions
	byVersion map[string][]int // pin version → positions of v2 records whose pins name it

	// legacy holds the exposure of each pin identity in the v1 records.
	// reviews.FirstFirings is the one reader that canonicalizes a
	// recorded lesson. Its Matches field counts the delivered and the
	// suppressed records that name the identity.
	legacy map[reviews.FiredLesson]reviews.Exposure

	// denied holds the match digests of the guard denies in the
	// lesson-check stream. An injection on such a call opens no delivered
	// session.
	denied map[string]bool
	// deniedInjections counts the injected hook records on denied calls.
	deniedInjections int

	totals Totals

	// scans answers the classifier's questions that look back from one
	// position. newIndex fills it with the position lists of this index.
	// The classifier needs a capture archive, so this build fills the
	// lists but does not read them.
	scans positionScans
}

// newIndex builds the lookups and the totals. It reads the prefix lines
// only when auditState is StateRead. A source that is unreadable or over
// its limit gives no counts, because a partial count would read like a
// result. The denied set comes from the lesson-check records, and only
// when checkState is StateRead. The check reading uses the same test, so
// both agree on whether the check stream is usable.
func newIndex(in Inputs, auditState, checkState string) *index {
	idx := &index{
		byMatch:   map[string][]int{},
		byContext: map[string][]int{},
		bySession: map[string][]int{},
		byEvent:   map[string][]int{},
		byVersion: map[string][]int{},
		denied:    map[string]bool{},
		totals:    Totals{DeliveredRecords: map[string]int{}},
	}

	if checkState == StateRead {
		for _, r := range in.CheckAudit.Records {
			if isGuardDeny(r) && r.MatchSHA != "" {
				idx.denied[r.MatchSHA] = true
			}
		}
	}

	if auditState != StateRead {
		idx.scans = newPositionIndex(idx)

		return idx
	}

	idx.lines = in.Audit.Lines

	firings := make([]reviews.Firing, len(idx.lines))
	legacy := make([]reviews.Firing, 0, in.Audit.Legacy)
	preparations := map[[3]string]bool{}

	for i := range idx.lines {
		f := idx.lines[i].Firing
		firings[i] = f

		if f.SchemaVersion == 0 {
			legacy = append(legacy, f)
		}

		put(idx.byMatch, f.MatchSHA, i)
		put(idx.byContext, f.ContextSHA, i)
		put(idx.bySession, f.SessionSHA, i)
		put(idx.byEvent, f.EventID, i)

		// A record lists a version once, also when duplicate pin entries
		// name it twice.
		for _, p := range f.Pins {
			if p.Version == "" {
				continue
			}

			if list := idx.byVersion[p.Version]; len(list) == 0 || list[len(list)-1] != i {
				idx.byVersion[p.Version] = append(list, i)
			}
		}

		switch f.Delivery {
		case reviews.DeliveryEvaluation:
			idx.totals.EvaluationRecords++
		case reviews.DeliveryReset:
			idx.totals.ResetRecords++
		}

		if f.SchemaVersion > 0 && (f.Surface == surfaceChangeSet || f.Surface == surfacePrepare) {
			preparations[[3]string{f.Surface, f.ScopeSHA, f.GuidanceSHA}] = true
		}

		if idx.isDeniedInjection(f) && injects(f, "") {
			idx.deniedInjections++
		}
	}

	summary := reviews.Summarize(firings, nil)
	maps.Copy(idx.totals.DeliveredRecords, summary.BySurface)

	idx.totals.SuppressedHookRecords = summary.SuppressedHookFirings
	idx.totals.Events = len(idx.byEvent)
	idx.totals.DistinctPreparations = len(preparations)
	idx.legacy = reviews.FirstFirings(legacy)
	idx.scans = newPositionIndex(idx)

	return idx
}

// put appends position i to the list of key. An empty key names nothing.
func put(m map[string][]int, key string, i int) {
	if key != "" {
		m[key] = append(m[key], i)
	}
}

// surfaceName is the report name of a record's surface.
func surfaceName(surface string) string {
	if surface == "" {
		return surfaceHook
	}

	return surface
}

// receiverSurface reports whether the records of a surface name a
// receiver: the hook surface, stored as "", or the exploration surface. A
// preparation record names no receiver, so it opens no session and has no
// lane.
func receiverSurface(surface string) bool {
	return surface == "" || surface == surfaceExplore
}

// isDeniedInjection reports whether a hook record belongs to a call that
// the guard denied: the match digests are equal.
func (idx *index) isDeniedInjection(f reviews.Firing) bool {
	return f.Surface == "" && f.MatchSHA != "" && idx.denied[f.MatchSHA]
}

// injects reports whether a record's pins inject a version. An empty
// version asks about any pin.
func injects(f reviews.Firing, version string) bool {
	for _, p := range f.Pins {
		if p.Delivery == reviews.PinInjected && (version == "" || p.Version == version) {
			return true
		}
	}

	return false
}

// deliveriesOf returns the set of delivery values that a record gives the
// pin version.
func deliveriesOf(f reviews.Firing, version string) map[reviews.PinDelivery]bool {
	out := map[reviews.PinDelivery]bool{}

	for _, p := range f.Pins {
		if p.Version == version {
			out[p.Delivery] = true
		}
	}

	return out
}

// opensSession reports whether a record opens a delivered session of a pin
// version. It needs an injection of the version from the hook or the
// exploration surface, a session digest, and a call that the guard did not
// deny. The delivery counts and the windows both use this test, so they
// count the same sessions.
func (idx *index) opensSession(f reviews.Firing, version string) bool {
	return f.SessionSHA != "" && receiverSurface(f.Surface) && injects(f, version) && !idx.isDeniedInjection(f)
}

// deliveryCounts counts the audit records of one pin version. The
// version's records come from the lookup by version, so an older version
// of the same identity never adds to this one. One record counts once per
// delivery value, also when two config entries share the version. The v1
// records name only the identity, so they count separately and on the
// current version only. They hold no pin list, so they never enter the
// split of the held-back causes.
func (idx *index) deliveryCounts(p pinVersion) DeliveryCounts {
	out := DeliveryCounts{Records: map[string]int{}}
	sessions := map[string]bool{}

	for _, i := range idx.byVersion[p.version] {
		f := idx.lines[i].Firing
		out.Records[surfaceName(f.Surface)]++

		d := deliveriesOf(f, p.version)
		out.Injected += boolInt(d[reviews.PinInjected])
		out.SuppressedRepeat += boolInt(d[reviews.PinSuppressedRepeat])
		out.HeldBack += boolInt(d[reviews.PinHeldBack])

		// causeOf gives one cause per record, so the split stays equal to
		// the held-back count when a record names the version twice.
		if d[reviews.PinHeldBack] {
			out.addHeldBackCause(surfaceName(f.Surface), causeOf(f, p.version))
		}

		out.EmitFailed += boolInt(d[reviews.PinEmitFailed])
		out.Returned += boolInt(d[reviews.PinReturned])

		if idx.opensSession(f, p.version) {
			sessions[f.SessionSHA] = true
		}
	}

	out.Sessions = len(sessions)

	// The v1 hook wrote the rendered symptom, "rule — note", into Fired.
	// So the lookup renders the configured entry the way every surface
	// does (reviews.PinIdentity), note included. A v1 record carries no
	// version, so only a current reading shows the count. A historical
	// version has no config entry and reads 0 by rule, not as a measured
	// zero. Duplicate entries of one identity each show the count, as they
	// show every other count.
	if p.configured != nil {
		out.LegacyIdentityRecords = idx.legacy[reviews.PinIdentity(*p.configured)].Matches
	}

	return out
}

// addHeldBackCause counts one held-back record under its surface and cause.
// A cause outside the closed list counts under HeldBackCauseUnknown, so
// the split stays equal to the held-back count.
func (d *DeliveryCounts) addHeldBackCause(surface, cause string) {
	if !reviews.KnownHeldBackCause(cause) {
		cause = HeldBackCauseUnknown
	}

	if d.HeldBackCauses == nil {
		d.HeldBackCauses = map[string]map[string]int{}
	}

	if d.HeldBackCauses[surface] == nil {
		d.HeldBackCauses[surface] = map[string]int{}
	}

	d.HeldBackCauses[surface][cause]++
}

// causeEmitFailed is the cause that causeOf gives a failed emission. It is
// no held-back cause, so the split counts it under HeldBackCauseUnknown.
const causeEmitFailed = "emit-failed"

// causeOf returns the cause of the held-back or failed delivery of the
// pin version. The writer sorts held-back entries before failed entries,
// so a held-back entry of the version decides when the record has both.
func causeOf(f reviews.Firing, version string) string {
	for _, p := range f.Pins {
		if p.Version != version {
			continue
		}

		switch p.Delivery {
		case reviews.PinHeldBack:
			return p.Cause
		case reviews.PinEmitFailed:
			return causeEmitFailed
		}
	}

	return ""
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
		if f.SchemaVersion == 0 || !receiverSurface(f.Surface) {
			continue
		}

		if f.ContextSHA == "" || f.Lane == laneUnknown {
			n++
		}
	}

	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
