package lessonreport

import "github.com/seamark-dev/seamark/internal/reviews"

// index holds the keyed lookups of the audit prefix (read-only).
// newIndex builds them in one pass over the lines, so no pin and
// no episode rescans the log. Every list holds positions into
// lines, in file order, because the file order of the append-only log
// orders the records. Times never do.
//
// The lookups by context and turn serve the action classes and the
// decision timing, which Build does not derive yet. Build reads the
// lookups by version, session, and event, plus the denied-call set.
type index struct {
	lines []reviews.AuditLine

	byMatch   map[string][]int // match_sha256 → positions
	byContext map[string][]int // context_sha256 → positions, in file order
	bySession map[string][]int // session_sha256 → positions
	byEvent   map[string][]int // event_id → positions
	byVersion map[string][]int // pin version → positions of v2 records whose pins name it
	byTurn    map[string][]int // turn_sha256 → positions

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
}

// newIndex builds the lookups and the totals. It reads the prefix lines
// only when usable is true: a source that is unreadable or over its limit
// gives no counts, because a partial count would read like a result. The
// denied set comes from the lesson-check records, which are usable on their own
// state.
func newIndex(in Inputs, usable bool) *index {
	idx := &index{
		byMatch:   map[string][]int{},
		byContext: map[string][]int{},
		bySession: map[string][]int{},
		byEvent:   map[string][]int{},
		byVersion: map[string][]int{},
		byTurn:    map[string][]int{},
		denied:    map[string]bool{},
		totals:    Totals{DeliveredRecords: map[string]int{}},
	}

	if in.CheckAuditError == "" && !in.CheckAudit.OverLimit {
		for _, r := range in.CheckAudit.Records {
			if guardDenied(r) {
				idx.denied[r.MatchSHA] = true
			}
		}
	}

	if !usable {
		return idx
	}

	idx.lines = in.Audit.Lines

	firings := make([]reviews.Firing, len(idx.lines))
	legacy := make([]reviews.Firing, 0, in.Audit.Legacy)
	preparations := map[[3]string]bool{}

	for i, l := range idx.lines {
		f := l.Firing
		firings[i] = f

		if f.SchemaVersion == 0 {
			legacy = append(legacy, f)
		}

		put(idx.byMatch, f.MatchSHA, i)
		put(idx.byContext, f.ContextSHA, i)
		put(idx.bySession, f.SessionSHA, i)
		put(idx.byEvent, f.EventID, i)
		put(idx.byTurn, f.TurnSHA, i)

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
	for surface, n := range summary.BySurface {
		idx.totals.DeliveredRecords[surface] = n
	}

	idx.totals.SuppressedHookRecords = summary.SuppressedHookFirings
	idx.totals.Events = len(idx.byEvent)
	idx.totals.DistinctPreparations = len(preparations)
	idx.legacy = reviews.FirstFirings(legacy)

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

// deliveryCounts counts the audit records of one pin version. The version's records come from the lookup by version, so an
// older version of the same identity never adds to this one. One record
// counts once per delivery value, also when two config entries share the
// version. A delivered session needs an injected record of the hook or
// the exploration surface with a session digest, and not on a denied
// call. The v1 records name only the identity, so they count apart and on
// the current version only; see legacyIdentity.
func (idx *index) deliveryCounts(p pinVersion) DeliveryCounts {
	out := DeliveryCounts{Records: map[string]int{}}
	sessions := map[string]bool{}

	for _, i := range idx.byVersion[p.version] {
		f := idx.lines[i].Firing
		out.Records[surfaceName(f.Surface)]++

		var injected, suppressed, held, failed, returned bool

		for _, pin := range f.Pins {
			if pin.Version != p.version {
				continue
			}

			switch pin.Delivery {
			case reviews.PinInjected:
				injected = true
			case reviews.PinSuppressedRepeat:
				suppressed = true
			case reviews.PinHeldBack:
				held = true
			case reviews.PinEmitFailed:
				failed = true
			case reviews.PinReturned:
				returned = true
			}
		}

		out.Injected += boolInt(injected)
		out.SuppressedRepeat += boolInt(suppressed)
		out.HeldBack += boolInt(held)
		out.EmitFailed += boolInt(failed)
		out.Returned += boolInt(returned)

		opensSession := injected && f.SessionSHA != "" &&
			(f.Surface == "" || f.Surface == surfaceExplore) && !idx.isDeniedInjection(f)
		if opensSession {
			sessions[f.SessionSHA] = true
		}
	}

	out.Sessions = len(sessions)
	if identity, ok := idx.legacyIdentity(p); ok {
		out.LegacyIdentityRecords = idx.legacy[identity].Matches
	}

	return out
}

// legacyIdentity is the canonical identity under which the v1 records
// name a current pin. It renders the configured entry the way every
// surface does (reviews.PinIdentity), note included, because the v1 hook
// wrote the rendered symptom, "rule — note", into Fired. A v1 record
// carries no version, so its identity count belongs to the current
// reading alone. A historical version has no config entry and reads 0 by
// rule, not as a measured zero. Duplicate entries of one identity each
// show the count, as they each show every other count.
func (idx *index) legacyIdentity(p pinVersion) (reviews.FiredLesson, bool) {
	if p.configured == nil {
		return reviews.FiredLesson{}, false
	}

	return reviews.PinIdentity(*p.configured), true
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
