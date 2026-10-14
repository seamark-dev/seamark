package yamledit

// Key lookup, line extents, and the line predicates. The node tree of
// yaml.v3 has a start position for each node but no end position, so
// an extent ends where the next sibling starts.

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Section returns the key node, the value node, and the pair index of a
// top-level section. The nodes are nil when the section is absent.
func (d *Doc) Section(name string) (key, value *yaml.Node, idx int) {
	if d.Root == nil {
		return nil, nil, -1
	}

	return Lookup(d.Root, name)
}

// Lookup returns the key and value nodes of name in mapping m, and the
// index of the key in m.Content. The nodes are nil when name is absent.
func Lookup(m *yaml.Node, name string) (key, value *yaml.Node, idx int) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, -1
	}

	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1], i
		}
	}

	return nil, nil, -1
}

// Child returns the key and value nodes of one field in a section.
func (d *Doc) Child(sectionName, key string) (k, v *yaml.Node) {
	_, value, _ := d.Section(sectionName)
	k, v, _ = Lookup(value, key)

	return k, v
}

// SectionEnd returns the last line of the top-level section at pair
// index idx.
func (d *Doc) SectionEnd(idx int) int {
	return d.Extent(d.Root, idx, d.Limit)
}

// ChildEnd returns the last line of one field inside a section.
func (d *Doc) ChildEnd(sectionName, key string) int {
	_, value, sidx := d.Section(sectionName)
	_, _, cidx := Lookup(value, key)

	return d.Extent(value, cidx, d.SectionEnd(sidx)+1)
}

// Extent returns the last line (1-based, inclusive) of the entry whose
// key is at pair index i of mapping m. stop is the first line after the
// parent's extent. The entry runs to the line before the next key.
// Trailing blank lines, and comments no deeper than the key, belong to
// the next key or to the document foot, so they are left out.
func (d *Doc) Extent(m *yaml.Node, i, stop int) int {
	key := m.Content[i]

	end := stop - 1
	if i+2 < len(m.Content) {
		end = m.Content[i+2].Line - 1
	}

	indent := key.Column - 1

	for end > key.Line {
		l := d.Lines[end-1]

		if IsBlank(l) || (d.Comment(end) && IndentOf(l) <= indent) {
			end--

			continue
		}

		break
	}

	return end
}

// Comment reports whether line n holds only a comment. A line of a
// multi-line value is value text, even when it starts with "#".
func (d *Doc) Comment(n int) bool {
	return IsComment(d.Lines[n-1]) && !d.valueLines[n]
}

// ContentLines lists the lines in [from, to] that hold YAML content:
// neither blank nor a comment.
func (d *Doc) ContentLines(from, to int) []int {
	var out []int

	for n := from; n <= to; n++ {
		if !IsBlank(d.Lines[n-1]) && !d.Comment(n) {
			out = append(out, n)
		}
	}

	return out
}

// HasComment reports whether any line in [from, to] is a comment line.
// CommentIn also finds a comment after the value text of a line.
func (d *Doc) HasComment(from, to int) bool {
	for n := from; n <= to; n++ {
		if d.Comment(n) {
			return true
		}
	}

	return false
}

// CommentIn reports whether any line in [from, to] holds a comment: a
// comment line, or a comment after the value text of the line. An editor
// that deletes or replaces these lines would drop such a comment.
func (d *Doc) CommentIn(from, to int) bool {
	for n := from; n <= to; n++ {
		if d.commentAt(n) < len(d.Lines[n-1]) {
			return true
		}
	}

	return false
}

// LineComment returns the comment of line n from its "#" to the line
// end, or an empty string. It finds the comments that CommentIn finds. A
// verifier compares these texts before and after a splice, because a
// splice can change the value on a line and keep its comment.
func (d *Doc) LineComment(n int) string {
	return d.Lines[n-1][d.commentAt(n):]
}

// commentAt returns the byte offset where the comment of line n starts,
// or the line length. The scan starts after the value text of the line.
// A scan from column 0 reads a quote inside plain text as an opening
// quote, and a "#" inside a multi-line quoted value as a comment.
func (d *Doc) commentAt(n int) int {
	line := d.Lines[n-1]

	if end, ok := d.textEnds[n]; ok {
		return CommentStart(line, end)
	}

	if d.Comment(n) {
		return IndentOf(line)
	}

	// No scalar starts on this line, and no quoted or block scalar goes
	// through it. The line thus holds the rest of a multi-line plain
	// scalar or only indicators, such as a dash. A quote there is text.
	// So the first "#" after white space starts the comment.
	return CommentStart(line, plainEnd(line, IndentOf(line), ""))
}

// SubtreeIssue finds the first alias, anchor, merge key, or duplicate
// key in the subtree of n. Each of them makes the effect of an edit
// ambiguous: an edit would also change another place, a merged value
// would stay in effect, or a reader would see only one of two values.
// section names the subtree in the reason.
func SubtreeIssue(section string, n *yaml.Node) *Issue {
	switch {
	case n.Kind == yaml.AliasNode:
		return &Issue{Reason: fmt.Sprintf("the %s section uses an alias (*%s)", section, n.Value), Line: n.Line}
	case n.Anchor != "":
		return &Issue{Reason: fmt.Sprintf("the %s section uses an anchor (&%s)", section, n.Anchor), Line: n.Line}
	}

	if n.Kind == yaml.MappingNode {
		seen := map[string]int{}

		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]

			if key.Tag == "!!merge" {
				return &Issue{Reason: fmt.Sprintf("the %s section uses a merge key (<<)", section), Line: key.Line}
			}

			if first, dup := seen[key.Value]; dup {
				return &Issue{
					Reason: fmt.Sprintf("the %s section repeats the key %q (first at line %d)", section, key.Value, first),
					Line:   key.Line,
				}
			}

			seen[key.Value] = key.Line
		}
	}

	for _, c := range n.Content {
		if issue := SubtreeIssue(section, c); issue != nil {
			return issue
		}
	}

	return nil
}

// NodeEqual compares two node trees by value, tag, style, and anchors.
// It ignores positions and comments: a splice can move a comment from
// one node to its neighbour without a change of any value.
func NodeEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || a.Anchor != b.Anchor ||
		a.Style != b.Style || len(a.Content) != len(b.Content) {
		return false
	}

	for i := range a.Content {
		if !NodeEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}

	return true
}

// LastLine returns the last start line of any node in the subtree of n.
// A multi-line scalar can end below this line, so a caller that needs
// the last text line scans on from it.
func LastLine(n *yaml.Node) int {
	last := n.Line

	for _, c := range n.Content {
		last = max(last, LastLine(c))
	}

	return last
}

// IsNull reports whether a node is a YAML null ("", "~", or "null").
func IsNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// IsBlank reports whether a line holds only white space.
func IsBlank(l string) bool {
	return strings.TrimSpace(l) == ""
}

// IsComment reports whether a line holds only a comment. It does not
// know the lines of multi-line values; Doc.Comment does.
func IsComment(l string) bool {
	return strings.HasPrefix(strings.TrimSpace(l), "#")
}

// IndentOf returns the number of leading white-space characters.
func IndentOf(l string) int {
	return len(l) - len(strings.TrimLeft(l, " \t"))
}

// LeadingSpaces returns the number of leading spaces. YAML indentation
// is spaces only; a tab is never part of it.
func LeadingSpaces(l string) int {
	return len(l) - len(strings.TrimLeft(l, " "))
}

// byteOffset converts a 0-based rune column into a byte offset in the
// line. yaml.v3 counts columns in characters, and a line can hold
// multi-byte text before the value.
func byteOffset(line string, col int) int {
	off := 0

	for i := 0; i < col && off < len(line); i++ {
		_, size := utf8.DecodeRuneInString(line[off:])
		off += size
	}

	return off
}

// SectionStart returns where a new top-level key goes: before the
// 0-based line pos (the end of the document content, before a "..."
// marker), at the column col of the existing top-level keys. lead holds
// one blank line when the line above pos is not blank, so the new key
// reads as its own block. The settings and the pin editors append their
// sections at this one place.
func (d *Doc) SectionStart() (pos, col int, lead []string) {
	if d.Root != nil && len(d.Root.Content) > 0 {
		col = d.Root.Content[0].Column - 1
	}

	pos = d.Limit - 1

	if pos > 0 && !IsBlank(d.Lines[pos-1]) {
		lead = []string{""}
	}

	return pos, col, lead
}

// PairsExcept returns the key and value nodes of each pair of the
// mapping m whose key is not in skip, in order. at is the number of such
// pairs before the first skipped key, or -1 when m has no skipped key.
// An editor that changes some keys compares the other pairs of the
// reviewed and the candidate document with SamePairs.
func PairsExcept(m *yaml.Node, skip ...string) (pairs [][2]*yaml.Node, at int) {
	at = -1

	if m == nil {
		return nil, at
	}

	for i := 0; i+1 < len(m.Content); i += 2 {
		if slices.Contains(skip, m.Content[i].Value) {
			if at < 0 {
				at = len(pairs)
			}

			continue
		}

		pairs = append(pairs, [2]*yaml.Node{m.Content[i], m.Content[i+1]})
	}

	return pairs, at
}

// SamePairs reports whether two lists of pairs hold equal keys and
// values in the same order, by NodeEqual. When they differ, key names
// the key of the first pair that differs; it is empty when the counts
// differ.
func SamePairs(x, y [][2]*yaml.Node) (key string, same bool) {
	if len(x) != len(y) {
		return "", false
	}

	for i := range x {
		if !NodeEqual(x[i][0], y[i][0]) || !NodeEqual(x[i][1], y[i][1]) {
			return x[i][0].Value, false
		}
	}

	return "", true
}
