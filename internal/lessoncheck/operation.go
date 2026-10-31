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
	// Content is the full content of a write, and the "+" lines of a
	// patch-add joined with "\n". It is nil for every other kind.
	Content *string
	Edits   []TextEdit // edit (one), multi-edit (many, in order)
	// Hunks is true for a patch-update that carries hunks. The content
	// of such an update stays unprojectable until a verified applier
	// exists, because a wrong projection could hide a violation.
	Hunks bool
}

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
