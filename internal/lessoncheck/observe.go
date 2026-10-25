package lessoncheck

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/seamark-dev/seamark/internal/history"
)

// This file holds ObserveAfter, the actual-edit producer of contract K9.
// It reads the state of each path of a pending record from disk after
// the native tool ran, and it compares that state with the before state
// of the record. So a result names what the edit changed, not what the
// working tree holds against HEAD. No step writes a file or runs a
// command that writes.

// pathVerdict tells how the observed after state of one pending path
// relates to its projected after state.
type pathVerdict int

// The verdicts of one path. The attribution of the snapshot takes the
// first verdict in this order that a path gives.
const (
	// verdictDiffers is an observed after state that differs from the
	// projection, for example because another writer changed the file.
	verdictDiffers pathVerdict = iota
	// verdictNone is a path without a projected digest to compare.
	verdictNone
	// verdictMatch is an observed after state equal to the projection.
	verdictMatch
)

// ObserveAfter returns the actual-edit snapshot of one native edit
// (contracts K4, K9). root is the top level of the git work tree.
// pending is the record of the call as ReadPending read it. A zero lim
// means DefaultLimits.
//
// The before side of each path comes from the record, with the content
// that the record keeps. The baseline is pre-edit, and pending_sha256 is
// the SHA-256 of the record bytes (§5 row 119). The after side comes
// from disk now, with the K4 worktree rules: Lstat, a symbolic link as a
// link, an open without following a link, and the byte limits. A path
// whose state did not change is not a change, so a failed tool that
// wrote nothing gives no change.
//
// The post-edit budget bounds the read. When it passes, the snapshot
// holds no change and names post-edit-budget in Limits, so every pin
// gives unknown/resource-limit. ObserveAfter returns an error only for a
// record that ReadPending did not return, for a path that is not
// repository-relative, for a root outside a git work tree, and for a
// canceled ctx (wrapping context.Canceled).
func ObserveAfter(ctx context.Context, root string, pending *Pending, lim Limits) (*Snapshot, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	if pending == nil || pending.fileSHA256 == "" {
		// The baseline must name the exact record bytes. A record that
		// was not read from its file has no such digest.
		return nil, errors.New("lesson checks: observe the edit: the pending record was not read from its file")
	}

	selected, err := selectionPaths(recordPaths(pending))
	if err != nil {
		// A record names paths below the root only. Any other path could
		// make the read leave the work tree.
		return nil, err
	}

	if !history.IsRepo(root) {
		return nil, fmt.Errorf("lesson checks: %s is not inside a git work tree", root)
	}

	ctx, cancel := context.WithTimeout(ctx, lim.PostEditBudget)
	defer cancel()

	snap := &Snapshot{
		Schema:   SnapshotSchema,
		Boundary: BoundaryActualEdit,
		Baseline: Baseline{Kind: BaselinePreEdit, PendingSHA: pending.fileSHA256},
		Selection: Selection{
			Kind:        SelectionOperation,
			Description: selectionOperationText,
			Paths:       selected,
			Excludes:    []string{selectionOperationExclude},
		},
		Changes:       []Change{},
		PathsComplete: pending.PathsComplete,
		bounds:        lim,
	}

	o := &observer{r: newTreeReader(ctx, root, lim), pending: pending}
	o.observeAll(snap)

	if o.r.stopped != nil {
		if !errors.Is(o.r.stopped, context.DeadlineExceeded) {
			return nil, fmt.Errorf("lesson checks: observe the edit: %w", o.r.stopped)
		}

		postEditTimedOut(snap, lim)
	}

	snap.SHA256 = snapshotDigest(snap)

	return snap, nil
}

// Attribution returns how the observed result of an actual-edit snapshot
// relates to the projection of the pre-edit record (contract K9). It is
// "" for a snapshot of another boundary and for a read that the
// post-edit budget ended, because then no comparison exists.
func (s *Snapshot) Attribution() Attribution {
	return s.attribution
}

// recordPaths returns every path of a record, the move sources
// included, in record order.
func recordPaths(p *Pending) []string {
	paths := make([]string, 0, len(p.Paths))

	for _, pp := range p.Paths {
		paths = append(paths, pp.Path)

		if pp.OldPath != "" {
			paths = append(paths, pp.OldPath)
		}
	}

	return paths
}

// postEditTimedOut turns a snapshot that the post-edit budget cut into
// one without changes. A partial observation must not decide any pin, so
// every checker then gives unknown/resource-limit with post-edit-budget.
func postEditTimedOut(snap *Snapshot, lim Limits) {
	snap.Changes = []Change{}
	snap.PathsComplete = false
	snap.Limits = []LimitHit{{Name: LimitPostEditBudget, Value: lim.PostEditBudget.Milliseconds()}}
	snap.identities = nil
	snap.attribution = ""
}

// observer holds the state of one observation.
type observer struct {
	// r reads the after side of each path. It applies the K4 worktree
	// rules and the byte limits, and it keeps the limit hits.
	r       *treeReader
	pending *Pending
}

// observeAll observes every path of the record into snap and sets the
// attribution of the snapshot.
func (o *observer) observeAll(snap *Snapshot) {
	noProjection := slices.Contains(o.pending.Limitations, LimitationNoProjection)
	verdicts := make([]pathVerdict, 0, len(o.pending.Paths))

	for _, pp := range o.pending.Paths {
		if o.r.done() {
			return
		}

		changes, verdict := o.observe(pp)
		snap.Changes = append(snap.Changes, changes...)
		verdicts = append(verdicts, verdict)
	}

	sort.SliceStable(snap.Changes, func(i, j int) bool {
		if snap.Changes[i].Path != snap.Changes[j].Path {
			return snap.Changes[i].Path < snap.Changes[j].Path
		}

		return snap.Changes[i].OldPath < snap.Changes[j].OldPath
	})

	snap.Limits = o.r.limitHits()
	snap.identities = o.r.identityList()
	snap.attribution = attributionOf(verdicts, noProjection)
}

// attributionOf returns the attribution of a record from the verdicts
// of its paths. An observed difference comes first, because it is a
// fact about the edit and the findings must name it. A record without a
// projection comes next, because then no match is known.
func attributionOf(verdicts []pathVerdict, noProjection bool) Attribution {
	switch {
	case slices.Contains(verdicts, verdictDiffers):
		return AttributionProjectionDiffers
	case noProjection || slices.Contains(verdicts, verdictNone):
		return AttributionNoProjection
	default:
		return AttributionProjectionMatch
	}
}

// observe returns the observed changes of one pending path and the
// verdict of the path. A move has two ends: the record names the target
// as Path and the source as OldPath, and its before side is the source.
func (o *observer) observe(pp PendingPath) ([]Change, pathVerdict) {
	before := o.before(pp)

	if pp.OldPath == "" {
		f, after := o.current(pp.Path)
		verdict := verdictOf(pp, after, true)

		ch, changed := observedChange(pp.Path, before, f, after)
		if !changed {
			return nil, verdict
		}

		return []Change{markVerdict(ch, verdict)}, verdict
	}

	src, srcAfter := o.current(pp.OldPath)
	dst, dstAfter := o.current(pp.Path)
	gone := src.id.state == pathAbsent
	verdict := verdictOf(pp, dstAfter, gone)

	switch {
	case gone && dstAfter.Type != ObjectAbsent:
		// The source is gone and the target exists: the move happened.
		// The pairing comes from the projection of the same call.
		ch := Change{Path: pp.Path, OldPath: pp.OldPath, Kind: ChangeMove, Before: before, After: dstAfter, Move: MoveProjection}
		setObservedFlags(&ch)
		undecidedEnd(&ch, dst)

		return []Change{markVerdict(ch, verdict)}, verdict
	case gone:
		// Both ends are absent: the edit removed the source, or the
		// source was absent before the edit too.
		ch, changed := observedChange(pp.OldPath, before, src, srcAfter)
		if !changed {
			return nil, verdict
		}

		return []Change{markVerdict(ch, verdict)}, verdict
	}

	// The source still exists, so no move happened. Each end is its own
	// change: the source against its before side, and a new target.
	var changes []Change

	if ch, changed := observedChange(pp.OldPath, before, src, srcAfter); changed {
		changes = append(changes, markVerdict(ch, verdict))
	}

	if ch, changed := observedChange(pp.Path, Side{Type: ObjectAbsent, Content: ContentNotRead}, dst, dstAfter); changed {
		changes = append(changes, markVerdict(ch, verdict))
	}

	return changes, verdict
}

// current reads the observed state of one path: the worktree record and
// the after side.
func (o *observer) current(rel string) (*worktreeFile, Side) {
	f := o.r.load(rel)
	side, _ := f.side("")

	return f, side
}

// before returns the before side of a pending path. The text comes from
// the content of the record, and it must hash to the digest of the
// side. A side without usable text reads as not-read, so a pattern pin
// gives missing-baseline and never reads an empty before state. The
// record keeps no text when no pattern pin scoped the path, and a
// corrupt record must never stand in for a baseline.
func (o *observer) before(pp PendingPath) Side {
	side := pp.Before
	side.text = nil

	switch side.Content {
	case ContentAvailable:
		data, err := base64.StdEncoding.DecodeString(pp.ContentB64)
		sum := sha256.Sum256(data)

		if err != nil || int64(len(data)) != side.Size || hex.EncodeToString(sum[:]) != side.SHA256 {
			side.Content = ContentNotRead

			return side
		}

		side.text = data
	case ContentTooLarge:
		// The writer drops content above max-baseline-bytes (contract K7).
		// A side above max-file-bytes keeps the reason of its size.
		if slices.Contains(o.pending.Limitations, LimitationBaselineTooLarge) && side.Size <= o.r.lim.MaxFileBytes {
			side.limit = LimitMaxBaselineBytes
			o.r.hit(LimitMaxBaselineBytes, o.r.lim.MaxBaselineBytes, 1)
		}
	}

	return side
}

// verdictOf compares the observed after side of a path with its
// projection. A projected delete has no digest, so an absent path
// matches it. ends is false when a move left its source in place.
func verdictOf(pp PendingPath, after Side, ends bool) pathVerdict {
	if pp.ProjectedAfterSHA256 == "" {
		if after.Type == ObjectAbsent && pp.OldPath == "" {
			return verdictMatch
		}

		return verdictNone
	}

	if ends && after.Type != ObjectAbsent && after.SHA256 == pp.ProjectedAfterSHA256 {
		return verdictMatch
	}

	return verdictDiffers
}

// markVerdict adds the change limitation of a verdict (contract K9):
// projection-differs for an observed difference, and no-projection for
// a path without a projected digest to compare.
func markVerdict(ch Change, verdict pathVerdict) Change {
	switch verdict {
	case verdictDiffers:
		ch.Limitations = withLimitation(ch.Limitations, LimitationProjectionDiffers)
	case verdictNone:
		ch.Limitations = withLimitation(ch.Limitations, LimitationNoProjection)
	case verdictMatch:
	}

	return ch
}

// observedChange returns the change of one path from its before side
// and its observed state. changed is false when the path did not change.
// A path that the observer cannot see, below a link or after a failed
// Lstat, is an unobserved change: forbid-path-change/1 then gives
// unknown and never a violation for it (K3).
func observedChange(rel string, before Side, f *worktreeFile, after Side) (ch Change, changed bool) {
	ch = Change{Path: rel, Kind: ChangeModify, Before: before, After: after}

	switch {
	case f.id.state == pathThroughLink || f.id.state == pathUnknown:
		if before.Type == ObjectAbsent {
			ch.Kind = ChangeAdd
		}

		setObservedFlags(&ch)
		undecidedEnd(&ch, f)

		return ch, true
	case before.Type == ObjectAbsent && after.Type == ObjectAbsent:
		return Change{}, false
	case before.Type == ObjectAbsent:
		ch.Kind = ChangeAdd
	case after.Type == ObjectAbsent:
		ch.Kind = ChangeDelete
	}

	setObservedFlags(&ch)

	if ch.Kind != ChangeModify {
		return ch, true
	}

	known := before.SHA256 != "" && after.SHA256 != ""

	switch {
	case ch.ModeChanged || ch.TypeChanged:
		return ch, true
	case known:
		return ch, ch.ContentChanged
	case before.Size != after.Size:
		// A size difference is an observed content difference.
		return ch, true
	}

	// The observer cannot compare the content, for example of a file
	// above max-file-bytes. The change stays undecided.
	ch.unobserved = true

	return ch, true
}

// undecidedEnd marks a change whose observed end the observer cannot
// see. A path below a link also gets the limitation path-through-link.
func undecidedEnd(ch *Change, f *worktreeFile) {
	switch f.id.state {
	case pathThroughLink:
		ch.unobserved = true
		ch.Limitations = withLimitation(ch.Limitations, LimitationPathThroughLink)
	case pathUnknown:
		ch.unobserved = true
	}
}

// setObservedFlags sets the change flags from the two sides. A side
// without a known digest counts as changed content, because the
// observer cannot show that the content stays the same.
func setObservedFlags(ch *Change) {
	known := ch.Before.SHA256 != "" && ch.After.SHA256 != ""
	ch.ContentChanged = !known || ch.Before.SHA256 != ch.After.SHA256

	present := ch.Before.Type != ObjectAbsent && ch.After.Type != ObjectAbsent
	ch.ModeChanged = present && ch.Before.Mode != "" && ch.After.Mode != "" && ch.Before.Mode != ch.After.Mode
	ch.TypeChanged = present && ch.Before.Type != ch.After.Type
}
