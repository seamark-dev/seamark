package lessonreport

import (
	"sort"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// positionScans answers the questions of the classifier that look back
// from one audit position. Each answer reads only records before that
// position, in file order. The interface exists so that a test can
// compare the index with a plain walk over the audit.
type positionScans interface {
	// latestPreparation returns the position of the newest change_set or
	// prepare record before a position that returned the pin version, or -1.
	latestPreparation(version string, pos int) int
	// earlierInjections returns the positions of the injections of the pin
	// version in a session before a position, in file order. The list holds
	// only hook and exploration records of version 2 on calls that the guard
	// did not deny. The caller must not change the list.
	earlierInjections(session, version string, pos int) []int
	// laneInjections is earlierInjections limited to the records of one
	// receiving context. An empty context gives no record.
	laneInjections(session, ctx, version string, pos int) []int
	// latestContextInjection returns the position of the newest emitted hook
	// injection of the pin version in a context and generation before a
	// position, or -1. A call that the guard denied never counts.
	latestContextInjection(ctx, version string, generation uint64, pos int) int
	// resetBetween reports whether the context got a reset record after
	// position from and before position to.
	resetBetween(ctx string, from, to int) bool
}

// positionIndex is the positionScans of Build. Each list holds positions
// in file order, so a binary search on a position finds the records
// before it. A list holds the records that the plain walk accepted, under
// the keys that the walk compared, and no looser key. Build drops the
// lists when it returns, so no list outlives one report.
type positionIndex struct {
	// byPreparation holds, per pin version, the change_set and prepare
	// records that returned the version.
	byPreparation map[string][]int
	// bySessionVersion holds, per session and pin version, the version 2
	// injections of the hook and the exploration surface on calls that the
	// guard did not deny.
	bySessionVersion map[sessionVersion][]int
	// byLaneVersion holds the records of bySessionVersion per session,
	// receiving context, and pin version.
	byLaneVersion map[laneVersion][]int
	// byContextVersion holds, per context, pin version, and generation,
	// the emitted hook injections on calls that the guard did not deny.
	byContextVersion map[contextVersion][]int
	// byReset holds the reset records per context.
	byReset map[string][]int
}

// sessionVersion keys a list by session digest and pin version.
type sessionVersion struct{ session, version string }

// laneVersion keys a list by session digest, context digest, and pin
// version. The plain walk compared both digests, so the key holds both.
type laneVersion struct{ session, ctx, version string }

// contextVersion keys a list by context digest, pin version, and context
// generation. The plain walk compared these three and not the session.
type contextVersion struct {
	ctx, version string
	generation   uint64
}

// newPositionIndex builds the position lists of the audit prefix. It
// reads each record once. Each list entry is one record under one key, and
// a record has few pins, so the memory stays linear in records.
func newPositionIndex(idx *index) *positionIndex {
	p := &positionIndex{
		byPreparation:    map[string][]int{},
		bySessionVersion: map[sessionVersion][]int{},
		byLaneVersion:    map[laneVersion][]int{},
		byContextVersion: map[contextVersion][]int{},
		byReset:          map[string][]int{},
	}

	for i := range idx.lines {
		f := idx.lines[i].Firing
		denied := idx.isDeniedInjection(f)

		// The plain walks read the records through the session, context, and
		// version lookups. Those lookups hold no empty session or context
		// key, so no list here holds one either. The empty version key is
		// on purpose, see below. A denied call adds only to the guard reading. The
		// context walk read every schema version, so its list does too.
		inSession := f.SchemaVersion > 0 && receiverSurface(f.Surface) && f.SessionSHA != "" && !denied
		inContext := f.Surface == "" && f.ContextSHA != "" && f.Emission == emissionEmitted && !denied
		preparation := f.Surface == surfaceChangeSet || f.Surface == surfacePrepare

		for _, pin := range f.Pins {
			switch {
			case pin.Delivery == reviews.PinInjected:
				// injects reads an empty version as a question about any pin,
				// so each injection also goes under the empty version.
				p.addInjection(f, i, "", inSession, inContext)
				p.addInjection(f, i, pin.Version, inSession, inContext)
			case preparation && pin.Version != "" && pin.Delivery == reviews.PinReturned:
				appendOnce(p.byPreparation, pin.Version, i)
			}
		}

		if f.Delivery == reviews.DeliveryReset && f.ContextSHA != "" {
			appendOnce(p.byReset, f.ContextSHA, i)
		}
	}

	return p
}

// addInjection adds the record at position i to the injection lists of
// a pin version that the record qualifies for.
func (p *positionIndex) addInjection(f reviews.Firing, i int, version string, inSession, inContext bool) {
	if inSession {
		appendOnce(p.bySessionVersion, sessionVersion{f.SessionSHA, version}, i)

		if f.ContextSHA != "" {
			appendOnce(p.byLaneVersion, laneVersion{f.SessionSHA, f.ContextSHA, version}, i)
		}
	}

	if inContext {
		appendOnce(p.byContextVersion, contextVersion{f.ContextSHA, version, f.Generation}, i)
	}
}

// appendOnce appends position i to the list of key, unless the list
// already ends with it. Two pin entries of one record then give one entry.
func appendOnce[K comparable](m map[K][]int, key K, i int) {
	if list := m[key]; len(list) == 0 || list[len(list)-1] != i {
		m[key] = append(list, i)
	}
}

// entriesBefore returns the entries of a list that lie before a position.
// The result shares the array of the list and has no spare capacity, so an
// append by a caller never writes into the list.
func entriesBefore(list []int, pos int) []int {
	n := sort.SearchInts(list, pos)
	if n == 0 {
		return nil
	}

	return list[:n:n]
}

// latestPreparation, earlierInjections, laneInjections, and
// latestContextInjection take the matching list up to the position. The
// last entry of that part is the newest record, as in the plain walk.
func (p *positionIndex) latestPreparation(version string, pos int) int {
	if list := entriesBefore(p.byPreparation[version], pos); len(list) > 0 {
		return list[len(list)-1]
	}

	return -1
}

func (p *positionIndex) earlierInjections(session, version string, pos int) []int {
	return entriesBefore(p.bySessionVersion[sessionVersion{session, version}], pos)
}

func (p *positionIndex) laneInjections(session, ctx, version string, pos int) []int {
	return entriesBefore(p.byLaneVersion[laneVersion{session, ctx, version}], pos)
}

func (p *positionIndex) latestContextInjection(ctx, version string, generation uint64, pos int) int {
	if list := entriesBefore(p.byContextVersion[contextVersion{ctx, version, generation}], pos); len(list) > 0 {
		return list[len(list)-1]
	}

	return -1
}

func (p *positionIndex) resetBetween(ctx string, from, to int) bool {
	// Only the first reset after from can lie before to, because the list
	// is in file order.
	list := p.byReset[ctx]
	n := sort.SearchInts(list, from+1)

	return n < len(list) && list[n] < to
}
