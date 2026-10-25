package yamledit

// Line inserts for new keys. The settings editor (internal/settings)
// and the check-definition editor (internal/maintain) add top-level keys
// and section children with these functions. One insert rule for both
// editors keeps the layout of a new key the same in every file.
// The functions return the new text. A caller parses the text again
// and verifies it, because an insert does not check the result.

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// IndentLines prefixes each line with n spaces.
func IndentLines(lines []string, n int) []string {
	out := make([]string, 0, len(lines))

	for _, l := range lines {
		out = append(out, strings.Repeat(" ", n)+l)
	}

	return out
}

// InsertChild adds child lines at the end of an existing top-level
// section. The lines get the indentation of the other children, or two
// spaces more than the section key. A null token on the section line
// ("agent: ~") goes away, because a mapping follows now. The caller
// makes sure that the section exists and is a block mapping or null.
func (d *Doc) InsertChild(section string, child []string) []byte {
	sk, sv, idx := d.Section(section)
	lines := slices.Clone(d.Lines)

	indent := sk.Column - 1 + 2
	if sv.Kind == yaml.MappingNode && len(sv.Content) > 0 {
		indent = sv.Content[0].Column - 1
	}

	// A null token ("~", "null", or a bare "!!null" tag) holds no quote,
	// so the comment search can start at the token itself. A line with
	// no token and no tag stays as it is.
	if IsNull(sv) && sv.Line == sk.Line && (sv.Value != "" || sv.Style&yaml.TaggedStyle != 0) {
		line := lines[sk.Line-1]
		start := d.Offset(sk.Line, sv.Column)
		lines[sk.Line-1] = strings.TrimRight(line[:start], " \t") + TrailingComment(line, start)
	}

	end := d.SectionEnd(idx)
	lines = slices.Insert(lines, end, IndentLines(child, indent)...)

	return JoinLines(lines, d.FinalEOL)
}

// AppendSection adds a new section with its child lines at the end of
// the document content.
func (d *Doc) AppendSection(section string, child []string) []byte {
	return d.AppendTopLevel(append([]string{section + ":"}, IndentLines(child, 2)...))
}

// AppendTopLevel adds new top-level lines at the end of the document
// content, after a blank line (SectionStart). The indentation follows the
// existing top-level keys. Text after a "..." end marker is not read, so
// the lines go before the marker.
func (d *Doc) AppendTopLevel(block []string) []byte {
	lines := slices.Clone(d.Lines)
	pos, indent, lead := d.SectionStart()

	lead = append(lead, IndentLines(block, indent)...)

	finalEOL := d.FinalEOL || pos == len(lines)
	lines = slices.Insert(lines, pos, lead...)

	return JoinLines(lines, finalEOL)
}
