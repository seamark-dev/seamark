package lessoncheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/history"
)

// This file holds ProjectOperation, the proposed-edit producer. It reads
// the current state of each operation path with the worktree rules and
// computes the state that the native tool would write. The projection is
// complete, or it marks the content as unprojectable. No step writes a
// file or runs a command that writes.

// The fixed selection texts of an operation snapshot.
const (
	selectionOperationText    = "the paths of one native edit"
	selectionOperationExclude = "writes outside this operation"
)

// operationSnapshot returns an operation snapshot without changes,
// before any read. ProjectOperation and ObserveAfter both call it,
// because both snapshots cover the paths of one native edit.
func operationSnapshot(boundary Boundary, baseline Baseline, selected []string, complete bool, lim Limits) *Snapshot {
	return &Snapshot{
		Schema:   SnapshotSchema,
		Boundary: boundary,
		Baseline: baseline,
		Selection: Selection{
			Kind:        SelectionOperation,
			Description: selectionOperationText,
			Paths:       selected,
			Excludes:    []string{selectionOperationExclude},
		},
		Changes:       []Change{},
		PathsComplete: complete,
		bounds:        lim,
	}
}

// cutByBudget clears the changes of an operation snapshot that a hook
// budget cut. A partial read must not decide any pin, so every checker
// then gives unknown/resource-limit with the limit that hit names.
func cutByBudget(snap *Snapshot, hit LimitHit) {
	snap.Changes = []Change{}
	snap.PathsComplete = false
	snap.Limits = []LimitHit{hit}
	snap.identities = nil
	snap.attribution = ""
}

// The details of an unprojectable change. The guard adds the detail to
// its deny text, so the agent sees why the guard cannot evaluate the
// edit. No detail tells the agent how to avoid the guard, because the
// text explains a deny and must not help the agent skip a check.
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
// rejects: it occurs 0 times, or more than once without replace_all.
const noteOccurs = "old_string occurs %d times"

// ProjectOperation returns the proposed-edit snapshot of one native
// edit. root is the top level of the git work tree. The paths of op are
// repository-relative after the CLI resolves parent links. Delete and
// move sources name the final entry, including a symbolic link. Other
// endpoints name the resolved target. A zero lim means DefaultLimits.
//
// The before side of each path comes from disk with the worktree rules:
// Lstat, a symbolic link as a link, an open without following a link,
// and the byte limits. So the baseline is pre-edit with an empty
// pending_sha256. Every path of the operation is a change, whatever its
// projected content, because forbid-path-change/1 decides on paths alone.
//
// The guard budget bounds the projection. When the budget ends, the
// snapshot holds no change and names guard-budget in Limits, so every
// pin gives unknown/resource-limit. ProjectOperation returns an error
// only for an operation that no decoder can return, failed repository
// discovery, or a canceled ctx (wrapping context.Canceled).
func ProjectOperation(ctx context.Context, root string, op Operation, lim Limits) (*Snapshot, error) {
	lim = lim.orDefault()

	if err := checkOperation(op); err != nil {
		return nil, err
	}

	selected, err := selectionPaths(operationPaths(op.Ops))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, lim.GuardBudget)
	defer cancel()

	snap := operationSnapshot(BoundaryProposedEdit, Baseline{Kind: BaselinePreEdit}, selected, op.PathsComplete, lim)
	cut := LimitHit{Name: LimitGuardBudget, Value: lim.GuardBudget.Milliseconds()}

	// Repository discovery shares the projection deadline and output
	// bound. os/exec checks the context before it starts git, so an
	// expired context starts no external work.
	repo, err := history.RunGit(ctx, history.GitCall{
		Dir: root, Args: []string{"rev-parse", "--is-inside-work-tree"}, MaxStdout: lim.MaxGitOutputBytes,
	})

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cutByBudget(snap, cut)
	case err != nil:
		return nil, fmt.Errorf("lesson checks: establish the git work tree: %w", err)
	case repo.ExitCode != 0 || strings.TrimSpace(string(repo.Stdout)) != "true":
		return nil, fmt.Errorf("lesson checks: %s is not inside a git work tree", root)
	default:
		p := &projector{r: newTreeReader(ctx, root, lim), repeated: repeatedPaths(op.Ops)}
		p.projectAll(snap, op.Ops)

		if stopped := p.r.stopped; stopped != nil {
			if !errors.Is(stopped, context.DeadlineExceeded) {
				return nil, fmt.Errorf("lesson checks: project the edit: %w", stopped)
			}

			cutByBudget(snap, cut)
		}
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
		out = append(out, fop.Paths()...)
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
		for _, p := range fop.Paths() {
			if seen[p] {
				repeated[p] = true
			}

			seen[p] = true
		}
	}

	return repeated
}

// projector holds the state of one projection.
type projector struct {
	// r reads the before side of each path. It applies the worktree rules
	// and the byte limits, and it keeps the limit hits.
	r *treeReader
	// repeated holds the paths that more than one operation names.
	repeated map[string]bool
	// incomplete is true when the projection found a path whose real
	// location it cannot name, such as a path through a link.
	incomplete bool
}

// projectAll projects every operation into snap. It skips the operations
// above max-snapshot-paths and marks the path set incomplete, because no
// cut projection may read as complete.
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

	sortChanges(snap.Changes)

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

// write projects a Claude Write or a Codex Add File section: the after
// side is the full content. Both tools create the file when it is
// absent and replace its content when it exists.
func (p *projector) write(fop FileOp) Change {
	f, before := p.r.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeModify, Before: before, After: p.content([]byte(*fop.Content), keptMode(before))}
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

// edit projects a Claude Edit or MultiEdit. Each step counts old_string
// in the running content. A failing step makes the whole call
// unprojectable, because the native call is atomic.
//
// An absent file gets an add when the first step has an empty
// old_string. A native Edit run on Claude Code 2.1.314 shows that the
// client creates the absent file with new_string. No native run shows a
// MultiEdit that creates a file, so its first step follows the Edit
// result by analogy. Any other edit of an absent file stays
// unprojectable, because no content exists that can hold its old_string.
func (p *projector) edit(fop FileOp) Change {
	f, before := p.r.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeModify, Before: before, After: Side{Type: ObjectFile, Mode: keptMode(before)}}

	absent := before.Type == ObjectAbsent
	if absent {
		ch.Kind = ChangeAdd
	}

	switch {
	case absent && fop.Edits[0].Old != "":
		unprojectable(&ch, noteAbsent)
	case !absent && before.Type != ObjectFile:
		unprojectable(&ch, noteNotFile)
	case !absent && before.Content != ContentAvailable:
		// The before side keeps its own state, such as binary or
		// too-large. The pattern checker reads it first, so that state
		// names the reason.
		unprojectable(&ch, noteNotText)
	default:
		// An absent file has no before text, so the create step gives
		// the first content.
		after, tooLarge, note, err := applyEdits(p.r.ctx, before.text, fop, absent, p.r.lim.MaxFileBytes)

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
// absent tells that the file does not exist. A first step with an empty
// old_string then creates the file with new_string, and the later steps
// apply to that content. In every other step an empty old_string fails.
//
// A MultiEdit can hold thousands of steps, and each step scans the whole
// content. So the function checks ctx before each step and returns its
// error, because the guard budget must bound the projection.
//
// The content stays in bytes, because a string copy of a file up to
// max-file-bytes costs memory on each guard call. bytes.Replace always
// returns a new slice, so the result never shares memory with content.
func applyEdits(ctx context.Context, content []byte, fop FileOp, absent bool, maxBytes int64) (after []byte, tooLarge int64, note string, err error) {
	for i, e := range fop.Edits {
		if err := ctx.Err(); err != nil {
			return nil, 0, "", err
		}

		step := ""
		if fop.Kind == OpMultiEdit {
			step = fmt.Sprintf("edit %d: ", i+1)
		}

		if absent && i == 0 && e.Old == "" {
			if size := int64(len(e.New)); size > maxBytes {
				return nil, size, "", nil
			}

			content = []byte(e.New)

			continue
		}

		// bytes.Count gives len+1 for an empty separator, so the empty
		// old_string has its own rule.
		if e.Old == "" {
			return nil, 0, step + noteEmptyOld, nil
		}

		old := []byte(e.Old)

		n := bytes.Count(content, old)
		if n == 0 || (n > 1 && !e.ReplaceAll) {
			return nil, 0, step + fmt.Sprintf(noteOccurs, n), nil
		}

		count := 1
		if e.ReplaceAll {
			count = n
		}

		size := int64(len(content)) + int64(count)*(int64(len(e.New))-int64(len(e.Old)))
		if size > maxBytes {
			return nil, size, "", nil
		}

		content = bytes.Replace(content, old, []byte(e.New), count)
	}

	return content, 0, "", nil
}

// remove projects a Codex Delete File section: the path becomes absent.
// A deleted link is the link itself, because the native tool removes
// the link and not its target.
func (p *projector) remove(fop FileOp) Change {
	f, before := p.r.current(fop.Path)

	ch := Change{Path: fop.Path, Kind: ChangeDelete, Before: before, After: Side{Type: ObjectAbsent, Content: ContentNotRead}}

	p.target(&ch, f, fop)
	setProjectedFlags(&ch)

	return ch
}

// update projects a Codex Update File section. Without hunks it is a
// move with unchanged content. With hunks the paths are complete, and
// the content stays unprojectable, because no verified hunk applier
// exists yet.
//
// A move onto an existing file replaces that file. The projection then
// gives a delete of the source and a modify of the target. It uses a
// modify, because the lines that the target gains come from the target's
// own before state.
func (p *projector) update(fop FileOp) []Change {
	src, before := p.r.current(fop.Path)

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

	dst, existing := p.r.current(fop.MoveTo)

	if existing.Type == ObjectAbsent {
		after.Mode = modeFile

		ch := Change{Path: fop.MoveTo, OldPath: fop.Path, Kind: ChangeMove, Before: before, After: after, Move: MoveProjection}
		p.updateContent(&ch, fop, before)
		p.source(&ch, &ch, src)
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
	// source itself is exact, but it holds the source path.
	p.source(&replaced, &gone, src)
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

// source applies the link rules to the source of a move. content is the
// change that receives the source content, and holder is the change that
// holds the source path. The native tool reads the source content
// through a link, and the projection never follows one, so the moved
// content is unprojectable. A source below a link also marks holder,
// because the real location of the source path is unknown.
func (p *projector) source(content, holder *Change, f *worktreeFile) {
	switch {
	case f.id.state == pathThroughLink:
		p.throughLink(holder)
		linkUnprojectable(content, noteThroughLink)
	case f.id.state == pathPresent && f.typ == ObjectSymlink:
		linkUnprojectable(content, noteLink)
	}

	if p.repeated[f.id.path] {
		unprojectable(content, noteRepeated)
	}
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
// incomplete.
func (p *projector) throughLink(ch *Change) {
	p.incomplete = true
	ch.Limitations = withLimitation(ch.Limitations, LimitationPathThroughLink)
}

// content returns the after side of projected content. The byte limits
// apply as to a read: max-file-bytes per side and max-snapshot-bytes
// for all content that the snapshot holds. The reader applies both
// limits and the content rule, so a projected side and a read side
// follow the same rules.
func (p *projector) content(data []byte, mode string) Side {
	side := Side{Type: ObjectFile, Mode: mode, Size: int64(len(data))}

	if p.r.tooLarge(side.Size) {
		side.Content = ContentTooLarge

		return side
	}

	p.r.budget -= side.Size
	side.SHA256, side.Content, side.text = contentOf(data)

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

// unprojectable marks the after content of a change as unknown. It keeps
// the first detail, because that detail names the first cause that the
// projection found. The change limitation no-projection makes the mark
// visible in the JSON output, and the pending record maps it.
func unprojectable(ch *Change, note string) {
	if ch.unprojectable == "" {
		ch.unprojectable = note
	}

	// A deletion keeps its absent after side: the path is gone whether
	// the native tool succeeds or refuses.
	if ch.Kind != ChangeDelete {
		ch.After = Side{Type: ObjectFile, Mode: keptMode(ch.After), Content: ContentNotRead}
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

// setProjectedFlags sets the change flags from the two sides with the
// rule of setChangeFlags. An unprojectable after side counts as changed
// content, because the projection cannot show that the content stays
// the same. A move with a projected after side copies the before side,
// so its content stays the same.
func setProjectedFlags(ch *Change) {
	setChangeFlags(ch)

	if ch.unprojectable != "" {
		ch.ContentChanged = true
	}

	if ch.Kind == ChangeMove && ch.unprojectable == "" && ch.After.Content == ch.Before.Content && ch.After.SHA256 == ch.Before.SHA256 {
		ch.ContentChanged = false
	}
}
