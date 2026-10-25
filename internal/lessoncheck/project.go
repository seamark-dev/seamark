package lessoncheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/history"
)

// This file holds ProjectOperation, the proposed-edit producer of
// contract K8. It reads the current state of each operation path with
// the worktree rules of K4 and computes the state that the native tool
// would write. The projection is complete or it marks the content as
// unprojectable (§6 A8). No step writes a file or runs a command that
// writes.

// The fixed selection texts of an operation snapshot (contract K4).
const (
	selectionOperationText    = "the paths of one native edit"
	selectionOperationExclude = "writes outside this operation"
)

// The details of an unprojectable change. The guard adds the detail to
// its deny text, so the agent sees why the guard cannot evaluate the
// edit. No detail tells the agent how to avoid the guard (R-38).
const (
	noteAbsent      = "the file does not exist"
	noteEmptyOld    = "old_string is empty"
	noteNotFile     = "the path is not a regular file"
	noteNotText     = "the current content is not readable text"
	noteLink        = "the path is a symbolic link"
	noteThroughLink = "a parent directory is a symbolic link"
	noteHunks       = "update hunks have no verified projection"
	noteSamePath    = "the move target is the source path"
	noteRepeated    = "the edit names the path more than once"
)

// noteOccurs is the detail of an Edit whose old_string the native tool
// rejects: it occurs 0 times, or more than once without replace_all
// (§5 row 34). Contract K8 gives the text.
const noteOccurs = "old_string occurs %d times"

// ProjectOperation returns the proposed-edit snapshot of one native
// edit. root is the top level of the git work tree. The paths of op are
// repository-relative after the CLI resolves parent links (contract K8).
// Delete and move sources name the final entry, including a symbolic link.
// Other endpoints name the resolved target. A zero lim means DefaultLimits.
//
// The before side of each path comes from disk with the K4 rules: Lstat,
// a symbolic link as a link, an open without following a link, and the
// byte limits. So the baseline is pre-edit with an empty pending_sha256
// (§5 row 119). Every path of the operation is a change, whatever its
// projected content (spec L168).
//
// The guard budget bounds the projection. When it passes, the snapshot
// holds no change and names guard-budget in Limits, so every pin gives
// unknown/resource-limit. ProjectOperation returns an error only for an
// operation that no decoder can return, failed repository discovery, or
// a canceled ctx (wrapping context.Canceled).
func ProjectOperation(ctx context.Context, root string, op Operation, lim Limits) (*Snapshot, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	if err := checkOperation(op); err != nil {
		return nil, err
	}

	selected, err := selectionPaths(operationPaths(op.Ops))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, lim.GuardBudget)
	defer cancel()

	snap := &Snapshot{
		Schema:   SnapshotSchema,
		Boundary: BoundaryProposedEdit,
		Baseline: Baseline{Kind: BaselinePreEdit},
		Selection: Selection{
			Kind:        SelectionOperation,
			Description: selectionOperationText,
			Paths:       selected,
			Excludes:    []string{selectionOperationExclude},
		},
		Changes:       []Change{},
		PathsComplete: op.PathsComplete,
		bounds:        lim,
	}

	// Repository discovery shares the projection deadline and output bound.
	// An expired context starts no external work.
	var repo history.GitOutput
	err = ctx.Err()
	if err == nil {
		repo, err = history.RunGit(ctx, history.GitCall{
			Dir: root, Args: []string{"rev-parse", "--is-inside-work-tree"}, MaxStdout: lim.MaxGitOutputBytes,
		})
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		guardTimedOut(snap, lim)
		snap.SHA256 = snapshotDigest(snap)
		return snap, nil
	case err != nil:
		return nil, fmt.Errorf("lesson checks: establish the git work tree: %w", err)
	case repo.ExitCode != 0 || strings.TrimSpace(string(repo.Stdout)) != "true":
		return nil, fmt.Errorf("lesson checks: %s is not inside a git work tree", root)
	}

	p := &projector{r: newTreeReader(ctx, root, lim), repeated: repeatedPaths(op.Ops)}
	p.projectAll(snap, op.Ops)

	if p.r.stopped != nil {
		if !errors.Is(p.r.stopped, context.DeadlineExceeded) {
			return nil, fmt.Errorf("lesson checks: project the edit: %w", p.r.stopped)
		}

		guardTimedOut(snap, lim)
	}

	snap.SHA256 = snapshotDigest(snap)

	return snap, nil
}

// checkOperation refuses an operation that no decoder returns. Such an
// operation is a programming error. The check stays, because a guess
// about a malformed operation could read as an exact projection.
func checkOperation(op Operation) error {
	for i, fop := range op.Ops {
		var problem string

		switch fop.Kind {
		case OpWrite, OpPatchAdd:
			if fop.Content == nil {
				problem = "has no content"
			}
		case OpEdit:
			if len(fop.Edits) != 1 {
				problem = "needs exactly one text edit"
			}
		case OpMultiEdit:
			if len(fop.Edits) == 0 {
				problem = "has no text edit"
			}
		case OpPatchMove:
			if fop.MoveTo == "" {
				problem = "has no move target"
			}
		case OpPatchDelete, OpPatchUpdate:
		default:
			problem = "has an unknown kind"
		}

		if problem == "" && fop.MoveTo != "" && fop.Kind != OpPatchMove && fop.Kind != OpPatchUpdate {
			problem = "has a move target that its kind does not take"
		}

		if problem != "" {
			return fmt.Errorf("lesson checks: file operation %d (%q) %s", i+1, fop.Kind, problem)
		}
	}

	return nil
}

// operationPaths returns every path of the operations, move targets
// included, in operation order.
func operationPaths(ops []FileOp) []string {
	var out []string

	for _, fop := range ops {
		out = append(out, fop.paths()...)
	}

	return out
}

// repeatedPaths returns the paths that more than one operation names.
// The projection reads each path from disk once, and a later operation
// on the same path would see the state of the earlier one. So such a
// path has no exact projection.
func repeatedPaths(ops []FileOp) map[string]bool {
	seen := map[string]bool{}
	repeated := map[string]bool{}

	for _, fop := range ops {
		for _, p := range fop.paths() {
			if seen[p] {
				repeated[p] = true
			}

			seen[p] = true
		}
	}

	return repeated
}

// guardTimedOut turns a snapshot that the guard budget cut into one
// without changes. A partial projection must not decide any pin, so
// every checker then gives unknown/resource-limit with guard-budget
// (contract K8, step 2).
func guardTimedOut(snap *Snapshot, lim Limits) {
	snap.Changes = []Change{}
	snap.PathsComplete = false
	snap.Limits = []LimitHit{{Name: LimitGuardBudget, Value: lim.GuardBudget.Milliseconds()}}
	snap.identities = nil
}

// projector holds the state of one projection.
type projector struct {
	// r reads the before side of each path. It applies the K4 worktree
	// rules and the byte limits, and it keeps the limit hits.
	r *treeReader
	// repeated holds the paths that more than one operation names.
	repeated map[string]bool
	// incomplete is true when the projection found a path whose real
	// location it cannot name, such as a path through a link.
	incomplete bool
}

// projectAll projects every operation into snap. Operations above
// max-snapshot-paths are not projected, and the path set is then
// incomplete, because no cut projection may read as complete (K3).
func (p *projector) projectAll(snap *Snapshot, ops []FileOp) {
	if maxOps := p.r.lim.MaxSnapshotPaths; len(ops) > maxOps {
		p.r.hit(LimitMaxSnapshotPaths, int64(maxOps), len(ops)-maxOps)
		ops = ops[:maxOps]
		snap.PathsComplete = false
	}

	for _, fop := range ops {
		if p.r.done() {
			return
		}

		snap.Changes = append(snap.Changes, p.project(fop)...)
	}

	if p.incomplete {
		snap.PathsComplete = false
	}

	sort.SliceStable(snap.Changes, func(i, j int) bool {
		if snap.Changes[i].Path != snap.Changes[j].Path {
			return snap.Changes[i].Path < snap.Changes[j].Path
		}

		return snap.Changes[i].OldPath < snap.Changes[j].OldPath
	})

	snap.Limits = p.r.limitHits()
	snap.identities = p.r.identityList()
}

// project returns the changes of one file operation.
func (p *projector) project(fop FileOp) []Change {
	switch fop.Kind {
	case OpWrite, OpPatchAdd:
		return []Change{p.write(fop)}
	case OpEdit, OpMultiEdit:
		return []Change{p.edit(fop)}
	case OpPatchDelete:
		return []Change{p.remove(fop)}
	default:
		return p.update(fop)
	}
}

// current reads the current state of one path: the worktree record and
// the before side. A path through a link reads as an unreadable file,
// and a symbolic link reads as a link with its target as content.
func (p *projector) current(rel string) (*worktreeFile, Side) {
	f := p.r.load(rel)
	side, _ := f.side("")

	return f, side
}

// write projects a Claude Write or a Codex Add File section: the after
// side is the full content. Both tools create the file when it is
// absent and replace its content when it exists.
func (p *projector) write(fop FileOp) Change {
	f, before := p.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeModify, Before: before, After: p.content(*fop.Content, keptMode(before))}
	if before.Type == ObjectAbsent {
		ch.Kind = ChangeAdd
	}

	if before.Type == ObjectOther {
		// The native tool cannot write over a directory or a device.
		unprojectable(&ch, noteNotFile)
	}

	p.target(&ch, f, fop)
	setProjectedFlags(&ch)

	return ch
}

// edit projects a Claude Edit or MultiEdit with the rules of contract
// K8. Each step counts old_string in the running content. A failing
// step makes the whole call unprojectable, because the native call is
// atomic.
//
// An absent file is unprojectable, also with an empty old_string, until
// a native sentinel recording shows that one Edit creates the file
// (ruling N1, plan step P4.11).
func (p *projector) edit(fop FileOp) Change {
	f, before := p.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeModify, Before: before, After: Side{Type: ObjectFile, Mode: keptMode(before)}}

	switch {
	case before.Type == ObjectAbsent:
		ch.Kind = ChangeAdd

		unprojectable(&ch, noteAbsent)
	case before.Type != ObjectFile:
		unprojectable(&ch, noteNotFile)
	case before.Content != ContentAvailable:
		// The before side keeps its own state, such as binary or
		// too-large. The pattern checker reads it first, so that state
		// names the reason.
		unprojectable(&ch, noteNotText)
	default:
		after, tooLarge, note, err := applyEdits(p.r.ctx, string(before.text), fop, p.r.lim.MaxFileBytes)

		switch {
		case err != nil:
			// The budget ended inside the edit steps. The reader keeps the
			// context error, and ProjectOperation then discards the whole
			// projection, so this change needs no content.
			p.r.done()
		case note != "":
			unprojectable(&ch, note)
		case tooLarge > 0:
			ch.After = Side{Type: ObjectFile, Mode: ch.After.Mode, Size: tooLarge, Content: ContentTooLarge}
			p.r.hit(LimitMaxFileBytes, p.r.lim.MaxFileBytes, 1)
		default:
			ch.After = p.content(after, ch.After.Mode)
		}
	}

	p.target(&ch, f, fop)
	setProjectedFlags(&ch)

	return ch
}

// applyEdits applies the text edits of one Edit or MultiEdit to the
// current content. It returns the new content, or the projected size
// when that size passes maxBytes, or the detail of the first failing
// step. The size check runs before each replacement, because one
// replace_all can grow the content far above the payload size.
//
// A MultiEdit can hold thousands of steps, and each step scans the whole
// content. So ctx is checked before each step, and its error is the
// error result: the guard budget must bound the projection.
func applyEdits(ctx context.Context, content string, fop FileOp, maxBytes int64) (after string, tooLarge int64, note string, err error) {
	for i, e := range fop.Edits {
		if err := ctx.Err(); err != nil {
			return "", 0, "", err
		}

		step := ""
		if fop.Kind == OpMultiEdit {
			step = fmt.Sprintf("edit %d: ", i+1)
		}

		// strings.Count gives len+1 for an empty string, so the empty
		// old_string has its own rule.
		if e.Old == "" {
			return "", 0, step + noteEmptyOld, nil
		}

		n := strings.Count(content, e.Old)
		if n == 0 || (n > 1 && !e.ReplaceAll) {
			return "", 0, step + fmt.Sprintf(noteOccurs, n), nil
		}

		count := 1
		if e.ReplaceAll {
			count = n
		}

		size := int64(len(content)) + int64(count)*(int64(len(e.New))-int64(len(e.Old)))
		if size > maxBytes {
			return "", size, "", nil
		}

		content = strings.Replace(content, e.Old, e.New, count)
	}

	return content, 0, "", nil
}

// remove projects a Codex Delete File section: the path becomes absent.
// A deleted link is the link itself, because the native tool removes
// the link and not its target.
func (p *projector) remove(fop FileOp) Change {
	f, before := p.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeDelete, Before: before, After: Side{Type: ObjectAbsent, Content: ContentNotRead}}

	p.target(&ch, f, fop)
	setProjectedFlags(&ch)

	return ch
}

// update projects a Codex Update File section. Without hunks it is a
// move with unchanged content. With hunks the paths are complete, and
// the content stays unprojectable until a verified applier exists (§6
// A8, plan step P4.12).
//
// A move onto an existing file replaces that file. The projection then
// gives a delete of the source and a modify of the target, because the
// lines that the target gains come from the target's own before state.
func (p *projector) update(fop FileOp) []Change {
	src, before := p.current(fop.Path)

	after := before
	after.Mode = keptMode(before)

	if fop.MoveTo == "" || fop.MoveTo == fop.Path {
		ch := Change{Path: fop.Path, Kind: ChangeModify, Before: before, After: after}
		if before.Type == ObjectAbsent {
			ch.Kind = ChangeAdd
		}

		if fop.MoveTo != "" {
			// The native tool writes the target and then removes the
			// source, so the result of a move onto itself is not known.
			unprojectable(&ch, noteSamePath)
		}

		p.updateContent(&ch, fop, before)
		p.target(&ch, src, fop)
		setProjectedFlags(&ch)

		return []Change{ch}
	}

	dst, existing := p.current(fop.MoveTo)

	if existing.Type == ObjectAbsent {
		after.Mode = modeFile

		ch := Change{Path: fop.MoveTo, OldPath: fop.Path, Kind: ChangeMove, Before: before, After: after, Move: MoveProjection}
		p.updateContent(&ch, fop, before)

		if p.source(&ch, src) {
			ch.Limitations = withLimitation(ch.Limitations, LimitationPathThroughLink)
		}

		p.target(&ch, dst, fop)
		setProjectedFlags(&ch)

		return []Change{ch}
	}

	gone := Change{Path: fop.Path, Kind: ChangeDelete, Before: before, After: Side{Type: ObjectAbsent, Content: ContentNotRead}}

	after.Mode = keptMode(existing)

	replaced := Change{Path: fop.MoveTo, Kind: ChangeModify, Before: existing, After: after}
	p.updateContent(&replaced, fop, before)

	// The content of the target comes from the source, so the link rules
	// of the source apply to the target change. The deletion of the
	// source itself is exact.
	if p.source(&replaced, src) {
		gone.Limitations = withLimitation(gone.Limitations, LimitationPathThroughLink)
	}

	p.target(&replaced, dst, fop)
	setProjectedFlags(&gone)
	setProjectedFlags(&replaced)

	return []Change{gone, replaced}
}

// updateContent sets the after content of an update: the source
// content for a move without hunks, unprojectable for a section with
// hunks or with an absent source.
func (p *projector) updateContent(ch *Change, fop FileOp, before Side) {
	switch {
	case fop.Hunks:
		unprojectable(ch, noteHunks)
	case before.Type == ObjectAbsent:
		// The native tool refuses to update or move an absent file.
		unprojectable(ch, noteAbsent)
	case before.Type == ObjectOther:
		unprojectable(ch, noteNotFile)
	}
}

// source applies the link rules to the source of a move. ch is the
// change that receives the source content. The native tool reads the
// source content through a link, and the projection never follows one,
// so the moved content is unprojectable. It returns true for a source
// below a link, so the caller marks the change that holds the source
// path.
func (p *projector) source(ch *Change, f *worktreeFile) bool {
	below := f.id.state == pathThroughLink

	switch {
	case below:
		p.incomplete = true

		linkUnprojectable(ch, noteThroughLink)
	case f.id.state == pathPresent && f.typ == ObjectSymlink:
		linkUnprojectable(ch, noteLink)
	}

	if p.repeated[f.id.path] {
		unprojectable(ch, noteRepeated)
	}

	return below
}

// target applies the link rules and the repeated-path rule to the path
// that an operation writes or deletes. A native tool writes content
// through a link, so the real target lies outside the snapshot: the
// content is unprojectable and the path set is incomplete. A deleted
// link is exact, because the native tool removes the link itself.
func (p *projector) target(ch *Change, f *worktreeFile, fop FileOp) {
	switch {
	case f.id.state == pathThroughLink:
		p.throughLink(ch)

		if fop.writesContent() {
			linkUnprojectable(ch, noteThroughLink)
		}
	case f.id.state == pathPresent && f.typ == ObjectSymlink && fop.writesContent():
		p.incomplete = true

		linkUnprojectable(ch, noteLink)
	}

	if p.repeated[f.id.path] {
		unprojectable(ch, noteRepeated)
	}
}

// throughLink marks a change at a path below a symbolic link. The real
// location of the path is unknown to the projection, so the path set is
// incomplete (§5 row 17).
func (p *projector) throughLink(ch *Change) {
	p.incomplete = true
	ch.Limitations = withLimitation(ch.Limitations, LimitationPathThroughLink)
}

// content returns the after side of projected text. The byte limits
// apply as to a read: max-file-bytes per side and max-snapshot-bytes
// for all content that the snapshot holds.
func (p *projector) content(text, mode string) Side {
	side := Side{Type: ObjectFile, Mode: mode, Size: int64(len(text))}

	switch {
	case side.Size > p.r.lim.MaxFileBytes:
		side.Content = ContentTooLarge
		p.r.hit(LimitMaxFileBytes, p.r.lim.MaxFileBytes, 1)

		return side
	case side.Size > p.r.budget:
		side.Content = ContentTooLarge
		p.r.hit(LimitMaxSnapshotBytes, p.r.lim.MaxSnapshotBytes, 1)

		return side
	}

	p.r.budget -= side.Size

	data := []byte(text)
	sum := sha256.Sum256(data)
	side.SHA256 = hex.EncodeToString(sum[:])
	side.Content = textState(data)

	if side.Content == ContentAvailable {
		side.text = data
	}

	return side
}

// keptMode returns the mode that a native write gives a path: the mode
// of an existing regular file, else the mode of a new file. Both
// clients create a file without the executable bit.
func keptMode(before Side) string {
	if before.Type == ObjectFile && before.Mode != "" {
		return before.Mode
	}

	return modeFile
}

// unprojectable marks the after content of a change as unknown. The
// first detail wins, because it names the first cause that the
// projection found. The change limitation no-projection makes the mark
// visible in the JSON output, and the pending record maps it (K7).
func unprojectable(ch *Change, note string) {
	if ch.unprojectable == "" {
		ch.unprojectable = note
	}

	// A deletion keeps its absent after side: the path is gone whether
	// the native tool succeeds or refuses.
	if ch.Kind != ChangeDelete {
		mode := ch.After.Mode
		if mode == "" || ch.After.Type != ObjectFile {
			mode = modeFile
		}

		ch.After = Side{Type: ObjectFile, Mode: mode, Content: ContentNotRead}
	}

	ch.Limitations = withLimitation(ch.Limitations, LimitationNoProjection)
}

// linkUnprojectable marks the after content of a change as unknown
// because of a symbolic link. The link detail replaces an earlier
// detail, because a link is the first cause: the native tool acts on
// another path than the one that the projection read.
func linkUnprojectable(ch *Change, note string) {
	ch.unprojectable = ""

	unprojectable(ch, note)
}

// setProjectedFlags sets the change flags from the two sides. A side
// without a known digest counts as changed content, because the
// projection cannot show that the content stays the same. A move with
// a projected after side keeps its content by construction.
func setProjectedFlags(ch *Change) {
	known := ch.unprojectable == "" && ch.Before.SHA256 != "" && ch.After.SHA256 != ""
	ch.ContentChanged = !known || ch.Before.SHA256 != ch.After.SHA256

	if ch.Kind == ChangeMove && ch.unprojectable == "" && ch.After.Content == ch.Before.Content && ch.After.SHA256 == ch.Before.SHA256 {
		ch.ContentChanged = false
	}

	present := ch.Before.Type != ObjectAbsent && ch.After.Type != ObjectAbsent
	ch.ModeChanged = present && ch.Before.Mode != "" && ch.After.Mode != "" && ch.Before.Mode != ch.After.Mode
	ch.TypeChanged = present && ch.Before.Type != ch.After.Type
}

// withLimitation adds a change limitation once and keeps the list
// sorted.
func withLimitation(list []string, limitation string) []string {
	for _, l := range list {
		if l == limitation {
			return list
		}
	}

	list = append(list, limitation)
	sort.Strings(list)

	return list
}
