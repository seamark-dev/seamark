package lessoncheck

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

// This file holds the frozen line rules of no-added-pattern/1. The text
// test, the line split, and the line diff are part of the checker
// version: a change of any of them is a new kind version.

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
// terminator, so a file with CRLF line ends gives the same lines. A lone
// "\r" stays in the line. A last segment without "\n" is a line. Content
// that ends in "\n" has no empty last line, and empty content has zero
// lines (nil).
//
// splitLines copies the content into one string, and each line is a
// substring of it. So the split copies the content once, not once per
// line. The trade-off: a kept line keeps the whole content in memory.
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}

	text := string(content)
	lines := make([]string, 0, lineCount(content))

	for text != "" {
		end := strings.IndexByte(text, '\n')
		if end < 0 {
			lines = append(lines, text)

			break
		}

		lines = append(lines, strings.TrimSuffix(text[:end], "\r"))
		text = text[end+1:]
	}

	return lines
}

// lineCount returns the number of lines that splitLines gives, without
// the split: one per "\n", and one more for a last segment without
// "\n". The max-diff-lines test uses it, so a side above the limit
// costs no split.
func lineCount(content []byte) int {
	n := bytes.Count(content, []byte{'\n'})

	if len(content) > 0 && content[len(content)-1] != '\n' {
		n++
	}

	return n
}

// addedLine is one line that a change adds. number is its 1-based line
// number in the after content.
type addedLine struct {
	number int
	text   string
}

// everyLine returns every line of the after content as added. A new file
// and a move into the scope add their whole content, because the scope
// had none of it before.
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
// frequent line can read as added.
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
