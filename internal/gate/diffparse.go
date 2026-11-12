package gate

import (
	"regexp"
	"strconv"
	"strings"
)

// hunkRe reads a hunk header: the old count, the new start, and the new
// count. Git leaves out a count of 1.
var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parsedDiff is what the shared parser reads from a unified diff.
type parsedDiff struct {
	// paths are the changed files in first-appearance order.
	paths []string
	// lines are the changed new-side lines of each path. A path with no
	// hunk, for example a binary or a mode-only change, has no lines.
	lines map[string][]uint32
}

// parseDiff reads a unified diff. EvalDiff and ChangedPaths both use
// it, so an advisory surface never disagrees with the verdict about the
// changed files.
//
// Inside a hunk, the old and new counts tell which lines are body
// lines. A body line is never a header, so an added "++ x" line does not
// register x. Only added and removed lines count as changes. The
// context lines of a hunk do not, because they are not part of the
// change.
func parseDiff(diffText string) parsedDiff {
	out := parsedDiff{lines: map[string][]uint32{}}

	// register adds a path even before, or without, any hunk. Binary,
	// mode-only, rename-only, and empty-file changes have no hunk, yet
	// they change state that the gate must see.
	register := func(p string) {
		if p == "" {
			return
		}

		if _, ok := out.lines[p]; !ok {
			out.lines[p] = nil
			out.paths = append(out.paths, p)
		}
	}

	file := ""
	oldFile := ""

	// gitRecord tells that the current record starts with a diff --git
	// line. gitHeader tells that no "---" line and no hunk came after it
	// yet. Git writes one "---" line before the hunks of a record, so a
	// later "---" line starts a plain record.
	gitRecord, gitHeader := false, false

	var h hunkWalker

	lines := strings.Split(diffText, "\n")

	for i, line := range lines {
		if h.active() {
			if !h.recordStartsAt(lines[i:]) && h.step(line) {
				continue
			}

			// A line that is not a body line, or the start of a new
			// record, ends the hunk early. The header rules below then
			// read it.
			h.end()
		}

		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, oldFile = "", "" // new record
			gitRecord, gitHeader = true, true

			if p := gitHeaderPath(line); p != "" {
				file = p
				register(p)
			}
		case strings.HasPrefix(line, "rename to "):
			// The rename line is more reliable than the `diff --git`
			// split for paths with spaces, so it sets the record's path.
			if p := diffHeaderPath(strings.TrimPrefix(line, "rename to "), ""); p != "" {
				file = p
				register(p)
			}
		case strings.HasPrefix(line, "rename from "):
			// A rename removes its old path, so the path enters the diff
			// files like a whole-file deletion. Check then shows the
			// partners and lessons of that path. The path gets no changed
			// line, so its reach stays unknown. The line leaves file
			// unchanged, because every hunk line belongs to the new path.
			register(diffHeaderPath(strings.TrimPrefix(line, "rename from "), ""))
		case strings.HasPrefix(line, "--- "):
			gitRecord = gitRecord && gitHeader
			gitHeader = false
			oldFile = diffHeaderPath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			file = diffHeaderPath(line[4:], "b/")

			// A whole-file deletion has `+++ /dev/null`. The old path is
			// what vanished, and it must enter diff.files, because the
			// deletion of an effectful file must never look like an
			// empty diff.
			if file == "" && oldFile != "" {
				file = oldFile
			}

			register(file)
		default:
			// A hunk with no file still starts the walker, so that its
			// body lines are never read as headers.
			if m := hunkRe.FindStringSubmatch(line); m != nil {
				gitHeader = false

				h.start(m, gitRecord, func(n uint32) {
					if file != "" {
						out.lines[file] = append(out.lines[file], n)
					}
				})
			}
		}
	}

	h.end()

	return out
}

// hunkWalker reads the body lines of one hunk and records the changed
// new-side lines.
type hunkWalker struct {
	oldLeft, newLeft int    // body lines that the header still announces
	next             uint32 // the new-side number of the next line
	lastText         uint32 // the last new-side line with text in this hunk, or 0
	cutOpen          bool   // a run of removed lines with no added line yet
	cutText          bool   // the run has a removed line with text
	cutIndented      bool   // the first removed line with text is indented
	beforeFirst      bool   // the hunk lies before new-side line 1
	git              bool   // the record starts with a diff --git line
	record           func(uint32)
}

// start begins a hunk from the matches of hunkRe. git tells that the
// record starts with a diff --git line.
func (h *hunkWalker) start(m []string, git bool, record func(uint32)) {
	h.oldLeft = hunkCount(m[1])
	h.newLeft = hunkCount(m[3])
	newStart, _ := strconv.Atoi(m[2])

	// With a new count of 0, git gives the line before the cut as the
	// start. The next new-side line is then the line after it.
	h.next = uint32(newStart)
	if h.newLeft == 0 {
		h.next++
	}

	// Git writes a new start of 0 only for a cut before line 1. Then the
	// new side can have no line at all, for example after the deletion
	// of a whole file.
	h.beforeFirst = newStart == 0

	h.lastText = 0
	h.cutOpen = false
	h.git = git
	h.record = record
}

// active reports whether the header still announces body lines.
func (h *hunkWalker) active() bool {
	return h.oldLeft > 0 || h.newLeft > 0
}

// recordStartsAt reports whether rest starts with the "---", "+++" and
// hunk header lines of a new file record. Git never writes a hunk header
// inside a hunk, so these lines show that a hand-made count is too large.
// Without this check, the next file drops out of the diff.
//
// In a git record, both counts at 1 are an exception. Git can end a hunk
// with a removed "-- x" line and an added "++ y" line. Git starts each
// next record with a diff --git line, so the two lines stay body lines.
//
// A plain diff has no such start line. There, one context line too many
// on both sides is the usual count error, so the three lines start a new
// record. If they are body lines after all, the next hunk counts for a
// path that the index does not know. That path gets the unknown-reach
// note, so the diff does not read as clean.
func (h *hunkWalker) recordStartsAt(rest []string) bool {
	if len(rest) < 3 || (h.git && h.oldLeft == 1 && h.newLeft == 1) {
		return false
	}

	return strings.HasPrefix(rest[0], "--- ") && strings.HasPrefix(rest[1], "+++ ") &&
		hunkRe.MatchString(rest[2])
}

// step reads one line. It returns false when the line is not a body
// line of this hunk. The hunk ends when both counts reach 0.
func (h *hunkWalker) step(line string) bool {
	switch {
	case strings.HasPrefix(line, "+") && h.newLeft > 0:
		// Removed lines directly before an added line are a replacement.
		// The removed lines can be the end of the symbol above, for
		// example a Python function, which has no closing line. So the
		// lines above the run count too.
		if h.cutOpen {
			h.recordAbove()
		}

		h.cutOpen = false
		h.record(h.next)
		h.advance(line[1:])
		h.newLeft--
	case strings.HasPrefix(line, "-") && h.oldLeft > 0:
		h.noteCutLine(line[1:])
		h.oldLeft--
	case (line == "" || line[0] == ' ') && h.oldLeft > 0 && h.newLeft > 0:
		// Git writes an empty context line as "" when
		// diff.suppressBlankEmpty is set.
		h.closeCut()
		h.advance(strings.TrimPrefix(line, " "))
		h.oldLeft--
		h.newLeft--
	case strings.HasPrefix(line, `\`):
		// "\ No newline at end of file" belongs to the line before it.
		// It counts on neither side and keeps a run of removed lines open.
	default:
		return false
	}

	if !h.active() {
		h.end()
	}

	return true
}

// end closes the hunk and records an open cut.
func (h *hunkWalker) end() {
	h.closeCut()
	h.oldLeft, h.newLeft = 0, 0
}

// advance moves past one new-side line with the given text. A line with
// text becomes the line that recordAbove can use.
func (h *hunkWalker) advance(text string) {
	if strings.TrimSpace(text) != "" {
		h.lastText = h.next
	}

	h.next++
}

// noteCutLine adds one removed line with the given text to the current
// run. A blank removed line shows no indentation, so the first removed
// line with text decides whether the run is indented.
func (h *hunkWalker) noteCutLine(text string) {
	if !h.cutOpen {
		h.cutOpen, h.cutText, h.cutIndented = true, false, false
	}

	if !h.cutText && strings.TrimSpace(text) != "" {
		h.cutText = true
		h.cutIndented = text[0] == ' ' || text[0] == '\t'
	}
}

// recordAbove records the new-side lines above a run of removed lines.
// The line directly above always counts.
//
// That line can be a blank line outside the symbol that lost the lines.
// The span of a Python function ends at its last statement, not at the
// blank lines below it. An indented run continues the block above it, so
// it also records the last line with text above it in the hunk.
//
// A run that starts at column 0 does not look past a blank line. Such a
// run starts a new top-level item, so the item above lost nothing. For
// example, a comment edit above c must not touch b above the blank line.
func (h *hunkWalker) recordAbove() {
	if h.cutIndented && h.lastText > 0 && h.lastText+1 < h.next {
		h.record(h.lastText)
	}

	if h.next > 1 {
		h.record(h.next - 1)
	}
}

// closeCut records the new-side lines on both sides of a pure deletion.
// Either neighbour can belong to the symbol that lost the lines, for
// example at the end of a Python function, which has no closing line.
func (h *hunkWalker) closeCut() {
	if !h.cutOpen {
		return
	}

	h.cutOpen = false

	// A cut before line 1 records nothing. Line 1 can be a symbol of a
	// file that a saved diff deletes but the tree still holds. That
	// symbol must not hide the unknown reach of the deletion.
	if h.beforeFirst {
		return
	}

	h.recordAbove()
	h.record(h.next)
}

// hunkCount reads a hunk count. Git leaves out a count of 1.
func hunkCount(s string) int {
	if s == "" {
		return 1
	}

	n, _ := strconv.Atoi(s)

	return n
}
