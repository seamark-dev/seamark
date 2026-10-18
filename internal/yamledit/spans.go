package yamledit

// Spans of block sequence items and of mapping keys. An editor of a list
// of entries, such as the pin list of lessons.yaml, deletes whole items
// and replaces single values. It needs the exact lines of an item and
// the exact bytes of a value, because every other byte must stay.

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ItemSpan returns the first and the last line (1-based, inclusive) of
// item i of the block sequence seq. stop is the first line after the
// extent of the entry that holds seq, for example SectionEnd(idx) + 1.
//
// The first line is the dash line. yaml.v3 puts a mapping item at its
// first key, which is on a later line when the dash stands alone. The
// scan therefore goes back from that line to the dash at the column of
// the sequence.
//
// The last line is the last line that the item owns: content, a line of
// a multi-line value, or a comment deeper than the dash. Trailing blank
// lines, and comments at or left of the dash column, belong to the next
// item, the list foot, or the next key, so they are left out.
//
// ok is false when the text does not show the expected layout. Then seq
// is not a block sequence, i is out of range, or stop is before the
// item. Or a dash is not the first character of its line at the column
// of the sequence, as in the compact nested list "- - a".
func (d *Doc) ItemSpan(seq *yaml.Node, i, stop int) (first, last int, ok bool) {
	if seq == nil || seq.Kind != yaml.SequenceNode || seq.Style&yaml.FlowStyle != 0 || i < 0 || i >= len(seq.Content) {
		return 0, 0, false
	}

	col, ok := d.dashColumn(seq)
	if !ok {
		return 0, 0, false
	}

	if first, ok = d.dashLine(seq.Content[i], col); !ok {
		return 0, 0, false
	}

	last = min(stop-1, len(d.Lines))

	if i+1 < len(seq.Content) {
		next, ok := d.dashLine(seq.Content[i+1], col)
		if !ok {
			return 0, 0, false
		}

		last = next - 1
	}

	if last < first {
		return 0, 0, false
	}

	for last > first {
		l := d.Lines[last-1]

		if IsBlank(l) || (d.Comment(last) && IndentOf(l) <= col) {
			last--

			continue
		}

		break
	}

	return first, last, true
}

// dashColumn returns the 0-based column of the dashes of the block
// sequence seq. yaml.v3 puts a sequence without properties at its first
// dash. A tag or an anchor moves the node column to the property, so the
// first content line after the property gives the dash column then.
func (d *Doc) dashColumn(seq *yaml.Node) (int, bool) {
	if seq.Style&yaml.TaggedStyle == 0 && seq.Anchor == "" {
		return seq.Column - 1, true
	}

	// Only a comment can follow the properties on their own line: a dash
	// there is not valid YAML for a block sequence.
	line := d.Lines[seq.Line-1]
	if rest := SkipProperties(line, d.Offset(seq.Line, seq.Column)); rest < len(line) && line[rest] != '#' {
		return 0, false
	}

	for n := seq.Line + 1; n <= len(d.Lines); n++ {
		l := d.Lines[n-1]

		if IsBlank(l) || d.Comment(n) {
			continue
		}

		at := LeadingSpaces(l)
		if !DashAt(l, at) {
			return 0, false
		}

		return at, true
	}

	return 0, false
}

// dashLine returns the line of the dash that opens item, at column col.
// The scan starts at the item line. That line holds the dash, or the
// first key when the dash stands alone on an earlier line. Between the
// dash and the first key, only blank lines and comments can stand.
func (d *Doc) dashLine(item *yaml.Node, col int) (int, bool) {
	for n := item.Line; n >= 1; n-- {
		l := d.Lines[n-1]
		at := LeadingSpaces(l)

		if DashAt(l, at) {
			switch {
			case at == col:
				return n, true
			case n == item.Line && at > col:
				// A deeper dash on the item line opens a nested list in
				// the item. The item's own dash stands alone above it.
				continue
			default:
				return 0, false
			}
		}

		if n != item.Line && !IsBlank(l) && !d.Comment(n) {
			return 0, false
		}
	}

	return 0, false
}

// DashAt reports whether line l holds a sequence entry dash at byte at:
// a "-" followed by white space or by the line end. "---" and "-1" are
// not dashes.
func DashAt(l string, at int) bool {
	return at < len(l) && l[at] == '-' && (at+1 == len(l) || isSpace(l[at+1]))
}

// Span is one key of a block mapping with the text of its value.
type Span struct {
	// Key and Value are the nodes of the pair. Index is the index of Key
	// in the Content of the mapping, as Extent expects it.
	Key, Value *yaml.Node
	Index      int
	// First is the key line. Last is the last line of the pair, as
	// Extent returns it: the lines of a multi-line value and the comments
	// deeper than the key are part of the pair.
	First, Last int
	// Inline is true when the whole value sits on the key line, so that a
	// splice can replace it there. Such a value is a scalar token, an
	// empty value, or a flow list that closes on that line. No content
	// line follows inside the pair. A block scalar, a block collection,
	// an alias, and a flow mapping are never inline.
	Inline bool
	// Start and End are byte offsets on the key line, set when Inline is
	// true. Start is the value node column: its first property, its text,
	// or the place after the colon for an empty value. End follows the
	// last character of the value, so a trailing comment starts after it.
	Start, End int
}

// KeySpan locates the first key name in the block mapping m and the text
// of its value. stop is the first line after the extent of m, for
// example the last line of an ItemSpan + 1. ok is false when m is not a
// block mapping or when name is absent.
func (d *Doc) KeySpan(m *yaml.Node, name string, stop int) (Span, bool) {
	if m == nil || m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 {
		return Span{}, false
	}

	k, v, idx := Lookup(m, name)
	if k == nil {
		return Span{}, false
	}

	s := Span{Key: k, Value: v, Index: idx, First: k.Line, Last: d.Extent(m, idx, stop)}
	s.Start, s.End, s.Inline = d.inline(k, v, s.Last)

	return s, true
}

// inline returns the byte span of a value that sits on its key line. A
// content line after the key line inside the pair means that the value
// continues there, as a multi-line plain scalar does.
func (d *Doc) inline(k, v *yaml.Node, last int) (start, end int, ok bool) {
	if v.Line != k.Line || len(d.ContentLines(k.Line+1, last)) > 0 {
		return 0, 0, false
	}

	line := d.Lines[k.Line-1]
	start = d.Offset(v.Line, v.Column)
	token := SkipProperties(line, start)

	switch {
	case v.Kind == yaml.ScalarNode && v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return 0, 0, false
	case v.Kind == yaml.ScalarNode && (token == len(line) || line[token] == '#'):
		// An empty value has no token. Its properties, such as a bare
		// "!!null" tag, are its whole text.
		return start, start + len(strings.TrimRight(line[start:token], " \t")), true
	case v.Kind == yaml.ScalarNode:
		end, ok = TokenEnd(line, token, v.Style)

		return start, end, ok
	case v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0:
		end, ok = d.FlowEnd(v)

		return start, end, ok
	}

	return 0, 0, false
}

// ReplaceValue returns the key line of s with text in place of the value.
// The key, the spacing before the value, and the trailing comment with
// its alignment stay. A property (a tag or an anchor) goes with the old
// value, because the new text is complete. An empty value gets one space
// after the colon. ok is false when the value is not inline.
func (d *Doc) ReplaceValue(s Span, text string) (line string, ok bool) {
	if !s.Inline {
		return "", false
	}

	old := d.Lines[s.First-1]

	head := old[:s.Start]
	if head == "" || !isSpace(head[len(head)-1]) {
		head += " "
	}

	return head + text + old[s.End:], true
}
