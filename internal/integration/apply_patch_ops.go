package integration

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// applyPatchOps returns the file operations of an apply_patch text, in
// patch order, for the lesson-check guard (contract K8). It reads the
// text by the rules of applyPatchPaths and through the same helpers
// (patchBody, patchHeader, patchBodyLine). So the paths of the
// operations, each path followed by its move target, equal the result
// of applyPatchPaths, and the errors are of the same kind
// (TestApplyPatchOpsKeepThePathOracle).
//
// applyPatchPaths stays a separate function. The advisory edit hook
// calls it, and that hook must keep its result byte for byte (§6 A7).
//
// The operations:
//   - An add section gives OpPatchAdd. Its content is the "+" lines
//     without the "+", joined with "\n" (contract K8). A section without
//     lines gives an empty content, not a nil one.
//   - A delete section gives OpPatchDelete.
//   - An update section with a move and no other line gives
//     OpPatchMove: the native tool moves the file and keeps its content.
//   - Every other update section gives OpPatchUpdate. Any line below
//     the header, a move line excepted, sets Hunks, because the content
//     of a hunk has no verified projection yet (§6 A8, plan step P4.12).
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
		// The trim rules are the rules of applyPatchPaths: both ends
		// outside an update section, the end only inside one.
		line := strings.TrimSpace(raw)
		if hunk == patchUpdateFile {
			line = strings.TrimRightFunc(raw, unicode.IsSpace)
		}

		kind, name, isHeader := patchHeader(line)

		switch {
		case isHeader && kind == patchMoveTo:
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
			// The environment root is not known, so no path of the patch
			// can be resolved (applyPatchPaths has the same rule).
			return nil, fmt.Errorf("%w: the patch names an environment", ErrUnsupportedGrammar)
		case strings.HasPrefix(line, patchHeaderPrefix):
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
