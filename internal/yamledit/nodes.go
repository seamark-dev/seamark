package yamledit

// Key lookup, line extents, and the line predicates. The node tree of
// yaml.v3 has a start position for each node but no end position, so
// an extent ends where the next sibling starts.

import (
	"fmt"
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
func (d *Doc) HasComment(from, to int) bool {
	for n := from; n <= to; n++ {
		if d.Comment(n) {
			return true
		}
	}

	return false
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
