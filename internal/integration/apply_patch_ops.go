package integration

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// applyPatchOps returns the file operations of an apply_patch text, in
// patch order, for the lesson-check guard. The advisory edit hook reads
// the paths of the same operations (applyPatchPaths).
//
// The advisory edit hook is fail-open. Its replies must stay the same,
// byte for byte, although the guard now shares this decoder. So a change
// here must keep the paths and the error class of every patch. TestCodexEditsAgreeWithTheCodexParser
// checks those paths against the native parser.
//
// The result is complete or it is an error. A missing envelope or a
// patch without a hunk gives ErrMalformedEvent. A line that the known
// rules do not cover gives ErrUnsupportedGrammar.
//
// The function follows the Codex parser for every rule that decides
// which line is a header and what its path is. It matches a header
// after the trim that the parser applies, so "*** Update File: a.go "
// names a.go. A wrong path here means advice for a file that Codex does
// not touch, and no advice for the file that it does. The function
// checks the change lines less strictly than Codex does. Codex never
// applies a patch that it rejects, so advice for such a patch costs
// nothing.
//
// The operations:
//   - An add section gives OpPatchAdd. Its content is the "+" lines
//     without the "+", joined with "\n". A section without lines gives
//     an empty content, not a nil one.
//   - A delete section gives OpPatchDelete.
//   - An update section with a move and no other line gives
//     OpPatchMove: the native tool moves the file and keeps its content.
//   - Every other update section gives OpPatchUpdate. Any line below
//     the header, a move line excepted, sets Hunks, because the content
//     of a hunk has no verified projection yet.
//
// The function never runs the patch and never reads a file.
func applyPatchOps(patch string) ([]lessoncheck.FileOp, error) {
	lines, err := patchBody(patch)
	if err != nil {
		return nil, err
	}

	var (
		ops     []lessoncheck.FileOp
		added   []string // the lines of the open add section, without "+"
		hunk    string   // the marker of the open section, or "" before the first
		movable bool     // true directly after an update header
	)

	// flush sets the content of the open section when it is an add
	// section. The caller runs it before a new section and at the end.
	flush := func() {
		if n := len(ops); n > 0 && ops[n-1].Kind == lessoncheck.OpPatchAdd {
			content := strings.Join(added, "\n")
			ops[n-1].Content = &content
		}

		added = nil
	}

	for _, raw := range lines {
		// Codex trims both ends of a line outside an update section.
		// Inside one it trims the end only: a leading space marks a
		// context line, so an indented header there is file content.
		line := strings.TrimSpace(raw)
		if hunk == patchUpdateFile {
			line = strings.TrimRightFunc(raw, unicode.IsSpace)
		}

		kind, name, isHeader := patchHeader(line)

		switch {
		case isHeader && kind == patchMoveTo:
			// Codex reads a move only directly under its update header.
			if hunk != patchUpdateFile || !movable {
				return nil, fmt.Errorf("%w: %q outside an update header", ErrUnsupportedGrammar, patchMoveTo)
			}

			last := &ops[len(ops)-1]
			last.Kind, last.MoveTo, movable = lessoncheck.OpPatchMove, name, false
		case isHeader && kind == patchEndOfFile:
			if hunk != patchUpdateFile {
				return nil, fmt.Errorf("%w: %q outside an update hunk", ErrUnsupportedGrammar, patchEndOfFile)
			}

			markHunks(&ops[len(ops)-1])

			movable = false
		case isHeader:
			flush()

			ops = append(ops, patchSection(kind, name))
			hunk, movable = kind, kind == patchUpdateFile
		case strings.HasPrefix(line, patchEnvironment):
			// The line names an environment that is not the primary one.
			// No native recording shows that the event cwd belongs to that
			// environment, so no path of the patch can be resolved.
			return nil, fmt.Errorf("%w: the patch names an environment", ErrUnsupportedGrammar)
		case strings.HasPrefix(line, patchHeaderPrefix):
			// An unknown header can name a file.
			return nil, fmt.Errorf("%w: unknown header line", ErrUnsupportedGrammar)
		case !patchBodyLine(hunk, raw):
			return nil, fmt.Errorf("%w: unexpected line in the patch body", ErrUnsupportedGrammar)
		case hunk == patchAddFile:
			// patchBodyLine accepted the line, so it starts with "+".
			added, movable = append(added, raw[1:]), false
		default:
			markHunks(&ops[len(ops)-1])

			movable = false
		}
	}

	flush()

	if len(ops) == 0 {
		return nil, fmt.Errorf("%w: the patch holds no hunk", ErrMalformedEvent)
	}

	return ops, nil
}

// patchOpPaths returns the paths of the operations in patch order: each
// path, then its move target. patchHeader never gives an empty name, so
// an empty MoveTo means that the section has no move.
func patchOpPaths(ops []lessoncheck.FileOp) []string {
	var paths []string

	for _, op := range ops {
		paths = append(paths, op.Paths()...)
	}

	return paths
}

// patchSection returns the operation that a section header opens.
// patchHeader returns only the three file markers here, because the
// caller handles the move and end-of-file markers before.
func patchSection(kind, name string) lessoncheck.FileOp {
	switch kind {
	case patchAddFile:
		return lessoncheck.FileOp{Kind: lessoncheck.OpPatchAdd, Path: name}
	case patchDeleteFile:
		return lessoncheck.FileOp{Kind: lessoncheck.OpPatchDelete, Path: name}
	default:
		return lessoncheck.FileOp{Kind: lessoncheck.OpPatchUpdate, Path: name}
	}
}

// markHunks records that an update section carries a hunk. A move with
// a hunk is an update with a move target, because its content changes.
func markHunks(op *lessoncheck.FileOp) {
	op.Kind, op.Hunks = lessoncheck.OpPatchUpdate, true
}
