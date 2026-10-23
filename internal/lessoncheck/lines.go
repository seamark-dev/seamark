package lessoncheck

import (
	"bytes"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

// This file holds the frozen line rules of no-added-pattern/1 (contract
// K3). The text test, the line split, and the line diff are part of the
// checker version: a change of any of them is a new kind version.

// textState applies the text test to raw content. Content with a NUL
// byte is binary, and content that is not valid UTF-8 is not text. The
// producers and the checker use this one function, so they agree.
func textState(content []byte) ContentState {
	if bytes.IndexByte(content, 0) >= 0 {
		return ContentBinary
	}

	if !utf8.Valid(content) {
		return ContentInvalidUTF8
	}

	return ContentAvailable
}

// splitLines splits text content into lines without their terminators.
// The terminator is "\n", and one "\r" directly before it is part of the
// terminator (§5 row 15). A lone "\r" stays in the line. A last segment
// without "\n" is a line. Content that ends in "\n" has no empty last
// line, and empty content has zero lines.
func splitLines(content []byte) []string {
	var lines []string

	for len(content) > 0 {
		end := bytes.IndexByte(content, '\n')
		if end < 0 {
			lines = append(lines, string(content))

			break
		}

		lines = append(lines, string(bytes.TrimSuffix(content[:end], []byte("\r"))))
		content = content[end+1:]
	}

	return lines
}

// addedLine is one line that a change adds. number is its 1-based line
// number in the after content.
type addedLine struct {
	number int
	text   string
}

// everyLine returns every line of the after content as added. A new file
// and a move into the scope add their whole content (spec L150).
func everyLine(after []string) []addedLine {
	added := make([]addedLine, 0, len(after))

	for i, text := range after {
		added = append(added, addedLine{number: i + 1, text: text})
	}

	return added
}

// diffLines returns the lines that the after content adds to the before
// content. The added lines are the after lines of the insert and replace
// opcodes. The matcher runs without autojunk: with autojunk, difflib
// ignores a line that occurs often in a long file, so an unchanged
// frequent line can read as added (contract K3, §6 A4).
func diffLines(before, after []string) []addedLine {
	var added []addedLine

	for _, op := range difflib.NewMatcherWithJunk(before, after, false, nil).GetOpCodes() {
		if op.Tag != 'i' && op.Tag != 'r' {
			continue
		}

		for j := op.J1; j < op.J2; j++ {
			added = append(added, addedLine{number: j + 1, text: after[j]})
		}
	}

	return added
}
