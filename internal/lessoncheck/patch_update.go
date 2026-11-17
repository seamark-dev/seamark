package lessoncheck

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file holds the content projection of a Codex Update section: the
// bytes that the Codex apply_patch applier writes when it applies the
// chunks of one section to the current file. Each rule here copies a
// behavior of the codex-cli 0.169.0 applier that a case of the content
// oracle shows (internal/integration/testdata/codex/apply_patch_oracle.json).
// The projection is exact or it gives a detail, because a wrong
// projection could hide a violation.

// seekCheckWork is the number of line comparisons between two checks of
// the guard context in a seek. A near-match pattern over a large file
// can need billions of comparisons, so the seek must stop when the
// budget ends. One check per comparison would slow every normal seek.
const seekCheckWork = 1 << 16

// insertion is one added line and the index of the file line before
// which it goes. An index equal to the line count puts it at the end.
type insertion struct {
	at   int
	text string
}

// applyChunks returns the content of a file after the chunks of one
// Update section, or the projected size when that size passes maxBytes,
// or the detail of the first rule that has no exact projection. It
// returns the error of ctx when the guard budget ends during a seek.
//
// The steps follow the applier:
//   - The file splits into lines that end with "\n" or "\r\n". Only the
//     last line can have no line end. A lone "\r" gives a detail, because
//     the applier reads it as a line end in a way that no case pins.
//   - New lines take the line end of the first line, or "\n".
//   - A last line without a line end gets that line end first.
//   - Each chunk moves a cursor that starts at line 0. An anchor moves
//     the cursor below the first matching line.
//   - A chunk without context and deleted lines adds its lines at the
//     end of the file.
//   - Any other chunk matches its context and deleted lines from the
//     cursor (see seeker.seek). A context line keeps the file line. An
//     added line goes before the first line of its run of added and
//     deleted lines.
//   - Insertions at one index keep the chunk order, then the line order.
func applyChunks(ctx context.Context, content []byte, chunks []PatchChunk, maxBytes int64) (after []byte, tooLarge int64, note string, err error) {
	lines, ends, ok := patchFileLines(content)
	if !ok {
		return nil, 0, noteLoneCR, nil
	}

	eol := "\n"
	if len(ends) > 0 && ends[0] != "" {
		eol = ends[0]
	}

	if n := len(ends); n > 0 && ends[n-1] == "" {
		ends[n-1] = eol
	}

	s := seeker{ctx: ctx, lines: lines}

	var inserts []insertion

	deleted := make([]bool, len(lines))
	cursor := 0

	for _, chunk := range chunks {
		if chunk.Anchor != "" {
			at, found, err := s.seek([]string{chunk.Anchor}, cursor, false)
			if err != nil {
				return nil, 0, "", err
			}

			if !found {
				return nil, 0, noteNoMatch, nil
			}

			cursor = at + 1
		}

		elems := slices.Clone(chunk.Lines)

		old := oldSide(elems)
		if len(old) == 0 {
			// The applier puts a pure insertion at the end of the file, also
			// below an anchor and with the end-of-file marker.
			for _, e := range elems {
				inserts = append(inserts, insertion{at: len(lines), text: e.Text})
			}

			continue
		}

		at, found, err := s.seek(old, cursor, chunk.EndOfFile)
		if err == nil && !found && old[len(old)-1] == "" {
			elems = withoutTrailingEmpty(elems)
			old = oldSide(elems)
			at, found, err = s.seek(old, cursor, chunk.EndOfFile)
		}

		if err != nil {
			return nil, 0, "", err
		}

		if !found {
			return nil, 0, noteNoMatch, nil
		}

		inserts = place(elems, at, deleted, inserts)
		cursor = at + len(old)
	}

	// The insertions come in chunk order and line order, so a stable sort
	// by index keeps that order at one index.
	slices.SortStableFunc(inserts, func(a, b insertion) int { return cmp.Compare(a.at, b.at) })

	size := int64(0)

	for i, line := range lines {
		if !deleted[i] {
			size += int64(len(line) + len(ends[i]))
		}
	}

	for _, in := range inserts {
		size += int64(len(in.text) + len(eol))
	}

	if size > maxBytes {
		return nil, size, "", nil
	}

	out := make([]byte, 0, size)
	next := 0

	for i := 0; i <= len(lines); i++ {
		for ; next < len(inserts) && inserts[next].at == i; next++ {
			out = append(out, inserts[next].text...)
			out = append(out, eol...)
		}

		if i < len(lines) && !deleted[i] {
			out = append(out, lines[i]...)
			out = append(out, ends[i]...)
		}
	}

	return out, 0, "", nil
}

// place applies the change lines of one matched chunk. at is the index
// of the file line that the first context or deleted line matched. It
// marks the deleted lines and returns inserts with the added lines.
func place(elems []PatchLine, at int, deleted []bool, inserts []insertion) []insertion {
	pos, run := at, -1

	for _, e := range elems {
		switch e.Kind {
		case PatchContext:
			pos, run = pos+1, -1
		case PatchDelete:
			if run < 0 {
				run = pos
			}

			deleted[pos] = true
			pos++
		case PatchAdd:
			if run < 0 {
				run = pos
			}

			inserts = append(inserts, insertion{at: run, text: e.Text})
		}
	}

	return inserts
}

// patchFileLines splits text content into lines and their line ends:
// "\n", "\r\n", or "" for a last line without one. A final line end
// adds no empty line, and empty content has no line. ok is false when
// the content holds a "\r" that no "\n" follows.
//
// Each line is a substring of one copy of the content, so the split
// copies the content once.
func patchFileLines(content []byte) (lines, ends []string, ok bool) {
	for i, b := range content {
		if b == '\r' && (i+1 == len(content) || content[i+1] != '\n') {
			return nil, nil, false
		}
	}

	text := string(content)

	for text != "" {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines, ends = append(lines, text), append(ends, "")

			break
		}

		line, end := text[:i], "\n"
		if strings.HasSuffix(line, "\r") {
			line, end = line[:len(line)-1], "\r\n"
		}

		lines, ends = append(lines, line), append(ends, end)
		text = text[i+1:]
	}

	return lines, ends, true
}

// oldSide returns the texts of the context and deleted lines: the lines
// that the chunk expects in the file.
func oldSide(elems []PatchLine) []string {
	var old []string

	for _, e := range elems {
		if e.Kind != PatchAdd {
			old = append(old, e.Text)
		}
	}

	return old
}

// withoutTrailingEmpty returns the change lines without the last line
// of the old side, and without the last line of the new side when that
// line is empty. The applier retries a chunk this way when it fails and
// its old side ends with an empty line. A context line that leaves one
// side only becomes an added or a deleted line. The caller makes sure
// that the last line of the old side is empty.
func withoutTrailingEmpty(elems []PatchLine) []PatchLine {
	out := slices.Clone(elems)
	gone := make([]bool, len(out))

	if i := lastIndex(out, PatchDelete); i >= 0 {
		// The last old line leaves the old side.
		if out[i].Kind == PatchContext {
			out[i].Kind = PatchAdd
		} else {
			gone[i] = true
		}
	}

	if i := lastIndex(out, PatchAdd); i >= 0 && !gone[i] && out[i].Text == "" {
		// The last new line leaves the new side.
		if out[i].Kind == PatchContext {
			out[i].Kind = PatchDelete
		} else {
			gone[i] = true
		}
	}

	kept := out[:0]

	for i, e := range out {
		if !gone[i] {
			kept = append(kept, e)
		}
	}

	return kept
}

// lastIndex returns the index of the last line of one side, or -1. A
// side holds the context lines and the lines of its own kind: the old
// side holds the deleted lines, the new side the added lines. A line
// that already left a side has the kind of the other side.
func lastIndex(elems []PatchLine, kind PatchLineKind) int {
	for i := len(elems) - 1; i >= 0; i-- {
		if elems[i].Kind == PatchContext || elems[i].Kind == kind {
			return i
		}
	}

	return -1
}

// seeker finds a sequence of lines in the current content with the
// match passes of the applier. It counts the comparisons and checks the
// guard context, so the seek stops when the budget ends.
type seeker struct {
	ctx   context.Context
	lines []string
	work  int
}

// seek returns the first index at or after start where pattern matches
// the file lines. An empty pattern matches at start. With eof, the only
// index that counts is the one that puts the pattern at the end of the
// file, and only when it is at or after start.
//
// The applier tries four passes, each over every index, and the first
// match wins: exact, without trailing whitespace, without leading and
// trailing whitespace, and trimmed with normalized punctuation and
// spaces. So an exact match later in the file wins over a fuzzy match
// before it.
func (s *seeker) seek(pattern []string, start int, eof bool) (at int, found bool, err error) {
	n, p := len(s.lines), len(pattern)
	if p == 0 {
		return start, true, nil
	}

	first, last := start, n-p
	if eof {
		first = n - p
	}

	if p > n || first < start || first > last {
		return 0, false, nil
	}

	for _, pass := range matchPasses {
		pat := make([]string, p)
		for i, line := range pattern {
			pat[i] = matchForm(pass, line)
		}

		for at = first; at <= last; at++ {
			if s.work += p; s.work >= seekCheckWork {
				s.work = 0

				if ctxErr := s.ctx.Err(); ctxErr != nil {
					return 0, false, ctxErr
				}
			}

			if s.matchesAt(pass, pat, at) {
				return at, true, nil
			}
		}
	}

	return 0, false, nil
}

// matchesAt reports whether every pattern line, in the form of one
// pass, equals the file line at the same offset from at in that form.
// The file side is compared without a new string, so a seek over a
// large file allocates nothing per line.
func (s *seeker) matchesAt(pass int, pat []string, at int) bool {
	for i, want := range pat {
		line := s.lines[at+i]

		var same bool

		switch pass {
		case passExact:
			same = line == want
		case passTrimEnd:
			same = strings.TrimRightFunc(line, unicode.IsSpace) == want
		case passTrim:
			same = strings.TrimFunc(line, unicode.IsSpace) == want
		default:
			same = normalizedEqual(strings.TrimFunc(line, unicode.IsSpace), want)
		}

		if !same {
			return false
		}
	}

	return true
}

// The match passes of a seek, in the order of the applier.
const (
	passExact = iota
	passTrimEnd
	passTrim
	passNormalize
)

// matchPasses lists the passes for a range loop.
var matchPasses = [...]int{passExact, passTrimEnd, passTrim, passNormalize}

// matchForm returns a pattern line in the form of one pass. Whitespace
// is the Unicode White_Space property: unicode.IsSpace in Go and
// char::is_whitespace in Rust give the same set.
func matchForm(pass int, line string) string {
	switch pass {
	case passExact:
		return line
	case passTrimEnd:
		return strings.TrimRightFunc(line, unicode.IsSpace)
	case passTrim:
		return strings.TrimFunc(line, unicode.IsSpace)
	default:
		return strings.Map(normalizedRune, strings.TrimFunc(line, unicode.IsSpace))
	}
}

// normalizedEqual reports whether line, with each rune normalized,
// equals want, a pattern line in the normalized form.
func normalizedEqual(line, want string) bool {
	j := 0

	for _, r := range line {
		var buf [utf8.UTFMax]byte

		k := utf8.EncodeRune(buf[:], normalizedRune(r))
		if j+k > len(want) || want[j:j+k] != string(buf[:k]) {
			return false
		}

		j += k
	}

	return j == len(want)
}

// normalizedRune maps the punctuation and spaces that the applier
// treats as ASCII in its last pass. The oracle pins every character of
// this table, and one unmapped neighbor of each class, on codex-cli
// 0.169.0. Every other rune stays.
func normalizedRune(r rune) rune {
	switch {
	case r >= '\u2010' && r <= '\u2015', r == '\u2212':
		return '-' // hyphens, dashes, and the minus sign
	case r >= '\u2018' && r <= '\u201b':
		return '\'' // single quotation marks
	case r >= '\u201c' && r <= '\u201f':
		return '"' // double quotation marks
	case r == '\u00a0', r >= '\u2002' && r <= '\u200a', r == '\u202f', r == '\u205f', r == '\u3000':
		return ' ' // no-break and fixed-width spaces
	default:
		return r
	}
}
