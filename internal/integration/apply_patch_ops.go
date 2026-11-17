package integration

import (
	"fmt"
	"slices"
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
// checks those paths against the native parser. A rule that only the
// guard and the post-edit check need goes into guardPatchOps.
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
//     without the "+", each ended by "\n". The Codex applier writes a
//     final newline, and the projection must hold the bytes on disk. A
//     section without lines gives an empty content, not a nil one. A
//     section with an added line that still ends with a carriage return
//     keeps a nil content (addContent), so the guard projects no bytes.
//   - A delete section gives OpPatchDelete.
//   - An update section with a move and no other line gives
//     OpPatchMove, so the guard checks both paths. codex-cli 0.164.0
//     sent such a section to the PreToolUse hook, and the guard denied
//     it. codex-cli 0.169.0 refuses it in its own patch check before any
//     hook, because the section has no hunk. No recording shows that the
//     native tool applies such a section.
//   - Every other update section gives OpPatchUpdate. Any line below
//     the header, a move line excepted, sets Hunks. When every such line
//     follows the verified chunk grammar (patchChunks), the section also
//     gets its Chunks, so the guard can project its content. Another form
//     keeps Chunks nil and adds no error, because the paths of the
//     section are complete either way.
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
		changes []string // the raw lines of the open update section below its header and move
		hunk    string   // the marker of the open section, or "" before the first
		movable bool     // true directly after an update header
	)

	// flush sets the content of the open section when it is an add
	// section, and the chunks when it is an update section with hunks.
	// The caller runs it before a new section and at the end. An empty
	// add section stays "", because Codex then writes an empty file.
	flush := func() {
		if n := len(ops); n > 0 {
			switch last := &ops[n-1]; {
			case last.Kind == lessoncheck.OpPatchAdd:
				last.Content = addContent(added)
			case last.Hunks:
				last.Chunks = patchChunks(changes)
			}
		}

		added, changes = nil, nil
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

			changes, movable = append(changes, raw), false
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

			changes, movable = append(changes, raw), false
		}
	}

	flush()

	if len(ops) == 0 {
		return nil, fmt.Errorf("%w: the patch holds no hunk", ErrMalformedEvent)
	}

	return ops, nil
}

// patchPathRemoved holds the characters that the Codex applier removes
// from a header path when it writes the file: a tab and a carriage
// return. codex-cli 0.169.0 does this in Add, Update, Delete, and Move
// to paths. Offline runs of that applier keep the other C0 and C1
// controls, DEL, and the Unicode space characters, so those paths stay
// as decoded.
const patchPathRemoved = "\t\r"

// guardPatchOps returns the file operations of an apply_patch text for
// the lesson-check guard and the post-edit check. It applies the rules
// of applyPatchOps and adds one rule: a header path that holds a
// character of patchPathRemoved gives ErrUnsupportedGrammar.
//
// The rule exists because the applier writes another path than the
// decoder reads. "sen\rtinel/x.txt" writes sentinel/x.txt, but the
// decoded path lies outside a sentinel/ pin scope, so the guard would
// allow the edit. With the error, the path set is not complete. An armed
// guard then denies, and the post-edit check reports every pin as
// unknown. The decoder does not copy the removal, because a later
// applier can change it and the guard must not guess a path.
//
// A tab or a carriage return at the end of a header line never reaches
// this check: the line trim removes it, as in Codex. The advisory edit
// hook keeps applyPatchOps, so its replies do not change. The error text
// names no path, because the guard writes it into its audit record.
func guardPatchOps(patch string) ([]lessoncheck.FileOp, error) {
	ops, err := applyPatchOps(patch)
	if err != nil {
		return nil, err
	}

	for _, op := range ops {
		for _, path := range op.Paths() {
			if strings.ContainsAny(path, patchPathRemoved) {
				return nil, fmt.Errorf("%w: a header path holds a tab or a carriage return", ErrUnsupportedGrammar)
			}
		}
	}

	return ops, nil
}

// addContent returns the bytes that the Codex applier writes for the
// added lines of one add section, each line without its "+". It returns
// nil when an added line still ends with a carriage return.
//
// patchBody removes one carriage return at each line end, and the
// applier removes two (oracle cases 97 and 98). So "+x\r\r" gives the
// decoded text "x\r", and the applier writes "x\n". A content of
// "x\r\n" would let a line-end pattern such as "x$" judge other bytes
// than the file gets. The decoder does not copy the removal of the
// second carriage return, because a later applier can change it and the
// guard must not guess content. A single carriage return at a line end
// gives the same bytes in both, so that content stays as decoded.
func addContent(added []string) *string {
	if slices.ContainsFunc(added, func(line string) bool { return strings.HasSuffix(line, "\r") }) {
		return nil
	}

	content := ""
	if len(added) > 0 {
		content = strings.Join(added, "\n") + "\n"
	}

	return &content
}

// patchChunks reads the chunks of one update section from its raw lines
// below the header and the move line. It returns nil when a line breaks
// the chunk grammar that the content oracle verifies:
//
//   - The first chunk may start without an "@@" line. Every later chunk
//     starts with one.
//   - "@@" alone, after a right trim, opens a chunk without an anchor.
//     "@@ <text>" opens a chunk whose anchor is the right-trimmed text:
//     leading spaces stay and trailing spaces go, as in the applier.
//   - A line that starts with " ", "-", or "+" is a context, deleted, or
//     added line. Its text is the rest of the raw line, with no trim.
//   - An empty raw line is an empty context line.
//   - "*** End of File", after a right trim, closes a chunk that holds a
//     line. Only an "@@" line may follow it.
//   - A chunk holds at least one line.
//   - No change line ends with a carriage return. patchBody removes one
//     carriage return at each line end, and the applier removes two
//     (oracle cases 96 and 99), so the decoded text has one carriage
//     return more than the applier writes.
//
// The applier accepts some other forms, such as "*** End of File" as
// the first line of a section. A nil result keeps such a section
// unprojectable, so the guard never guesses its content.
func patchChunks(lines []string) []lessoncheck.PatchChunk {
	var chunks []lessoncheck.PatchChunk

	for i := 0; i < len(lines); {
		var chunk lessoncheck.PatchChunk

		switch line := strings.TrimRightFunc(lines[i], unicode.IsSpace); {
		case line == "@@":
			i++
		case strings.HasPrefix(line, "@@ "):
			chunk.Anchor = line[len("@@ "):]
			i++
		case len(chunks) > 0:
			return nil
		}

		i = chunkLines(lines, i, &chunk)
		if len(chunk.Lines) == 0 || i < 0 {
			return nil
		}

		chunks = append(chunks, chunk)
	}

	return chunks
}

// chunkLines reads the change lines of one chunk from lines[i:] into
// chunk. It returns the index of the first line after the chunk, or -1
// when an end-of-file line closes a chunk without lines or a change line
// ends with a carriage return.
func chunkLines(lines []string, i int, chunk *lessoncheck.PatchChunk) int {
	for ; i < len(lines); i++ {
		raw := lines[i]

		switch {
		case strings.TrimRightFunc(raw, unicode.IsSpace) == patchEndOfFile:
			if len(chunk.Lines) == 0 {
				return -1
			}

			chunk.EndOfFile = true

			return i + 1
		case strings.HasSuffix(raw, "\r"):
			return -1
		case raw == "":
			chunk.Lines = append(chunk.Lines, lessoncheck.PatchLine{Kind: lessoncheck.PatchContext})
		case strings.ContainsRune("+- ", rune(raw[0])):
			chunk.Lines = append(chunk.Lines, lessoncheck.PatchLine{Kind: lessoncheck.PatchLineKind(raw[0]), Text: raw[1:]})
		default:
			return i
		}
	}

	return i
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
