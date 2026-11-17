package lessoncheck

// This file holds the operation model of the guard: what a guard decoder
// returns for one pending native edit. The integration decoders build an
// Operation, the CLI guard handler normalizes its paths, and
// ProjectOperation turns it into a proposed-edit snapshot. The types
// live here, because lessoncheck cannot import integration.

// OpKind names what one file operation of a native edit does.
type OpKind string

// The closed list of operation kinds.
const (
	// OpWrite is a Claude Write: the full new content of one file.
	OpWrite OpKind = "write"
	// OpEdit is a Claude Edit: one text replacement in one file.
	OpEdit OpKind = "edit"
	// OpMultiEdit is a Claude MultiEdit: replacements in order on one
	// file. The native call is atomic, so one failing step fails all.
	OpMultiEdit OpKind = "multi-edit"
	// OpPatchAdd is a Codex apply_patch Add File section.
	OpPatchAdd OpKind = "patch-add"
	// OpPatchDelete is a Codex apply_patch Delete File section.
	OpPatchDelete OpKind = "patch-delete"
	// OpPatchUpdate is a Codex apply_patch Update File section with
	// hunks, with or without a Move to line.
	OpPatchUpdate OpKind = "patch-update"
	// OpPatchMove is a Codex apply_patch Update File section with a Move
	// to line and no hunk: a move with unchanged content.
	OpPatchMove OpKind = "patch-move"
)

// Operation is one pending native edit, as a guard decoder reads it. A
// decoder returns a complete Operation or an error, never a part of the
// edit, because a check on a part of the edit can miss a violation.
//
// SessionID and MatchID are raw provider IDs. They never go into a
// record: the handlers hash them first, because a record holds digests
// and codes, not raw IDs.
type Operation struct {
	Client    string
	Tool      string   // native tool name
	Ops       []FileOp // complete, or the decoder returned an error
	SessionID string   // raw; hashed before persisting
	MatchID   string   // raw tool_use_id; hashed before persisting
	CWD       string
	Receiver  string // receiving context id or ""
	// PathsComplete is false when delivery.NormalizePaths rejected a path
	// that may lie inside the worktree. The guard then cannot tell which
	// pin scopes the edit touches. Dropped lists the paths that are
	// lexically outside the root. No record holds them.
	PathsComplete bool
	Dropped       []string
}

// FileOp is one file operation of a native edit.
//
// Path and MoveTo are repository-relative and slash-separated at their
// real location. The CLI handler resolves them with
// delivery.NormalizePaths, and ProjectOperation never normalizes,
// because lessoncheck cannot import delivery.
type FileOp struct {
	Kind   OpKind
	Path   string
	MoveTo string // patch-move, and patch-update with a Move to line
	// Content is the full content of a write, and for a patch-add the
	// bytes that the applier writes. It is nil for every other kind. A
	// patch-add with nil Content stays unprojectable, because the
	// decoder found an added line whose written bytes are not known.
	Content *string
	Edits   []TextEdit // edit (one), multi-edit (many, in order)
	// Hunks is true for a patch-update that carries hunks.
	Hunks bool
	// Chunks holds the hunks of a patch-update in patch order, when the
	// decoder read every line of the section with the verified chunk
	// grammar. It is nil for any other section. An update with Hunks and
	// nil Chunks stays unprojectable, because the applier rules for its
	// form are not known.
	Chunks []PatchChunk
}

// PatchChunk is one hunk of a Codex Update section: an optional @@
// anchor line and the change lines below it.
type PatchChunk struct {
	// Anchor is the text of an "@@ <text>" line without its trailing
	// whitespace. It is "" for a chunk without an anchor. The grammar
	// gives no empty anchor, because "@@ " alone trims to "@@".
	Anchor string
	Lines  []PatchLine
	// EndOfFile is true when "*** End of File" closes the chunk. The
	// chunk then matches only at the end of the file.
	EndOfFile bool
}

// PatchLine is one change line of a chunk. Text is the raw line
// without its first byte, with no trim, because the applier writes an
// added line as it stands in the patch.
type PatchLine struct {
	Kind PatchLineKind
	Text string
}

// PatchLineKind is the first byte of a change line.
type PatchLineKind byte

// The closed list of change line kinds.
const (
	PatchContext PatchLineKind = ' ' // a line that stays
	PatchDelete  PatchLineKind = '-' // a line that goes
	PatchAdd     PatchLineKind = '+' // a line that comes
)

// TextEdit is one text replacement of a Claude Edit or MultiEdit.
type TextEdit struct {
	Old, New   string
	ReplaceAll bool
}

// Paths returns the paths of a file operation: the path, and the move
// target when the operation moves the file. The client adapters call
// it, so one rule lists the paths of an operation.
func (op FileOp) Paths() []string {
	if op.MoveTo == "" {
		return []string{op.Path}
	}

	return []string{op.Path, op.MoveTo}
}

// writesContent reports whether the operation writes file content. A
// native tool writes content through a symbolic link, so such an
// operation on a link has no exact projection.
func (op FileOp) writesContent() bool {
	return op.Kind != OpPatchDelete
}
