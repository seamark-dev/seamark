// Package yamledit finds the source lines of YAML nodes, so that an
// editor can splice the text of a committed file instead of encoding it
// again. A re-encode of the whole document drops blank lines and moves
// comments, and the files are reviewed by people.
//
// The package reads only. Parse applies a structural gate and returns a
// Doc: the source lines plus the yaml.v3 node tree. The Doc methods give
// the line extent of a mapping key or a sequence item and the byte span
// of a value on its key line. The token scanners find where a scalar, a
// flow list, or a trailing comment ends. The encoders return scalar and
// flow list text that yaml.v3 reads back as the same strings.
//
// A problem that makes an edit unsafe is an Issue. An Issue names no file
// and no manual route, because each caller owns a different file. The
// settings editor (internal/settings) and the pin file editor use it.
package yamledit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Issue explains why an edit of the text is unsafe. It names no file, so
// each caller adds its own file name and manual route.
type Issue struct {
	// Line is the 1-based line to open, or 0 when it is not known.
	Line int
	// Reason is one sentence without a final period.
	Reason string
}

// Error names the reason and the line.
func (i *Issue) Error() string {
	if i.Line > 0 {
		return fmt.Sprintf("%s (line %d)", i.Reason, i.Line)
	}

	return i.Reason
}

// Doc is one parsed YAML document with its source lines. The fields are
// read-only: a caller clones Lines before a splice, because the other
// fields describe the lines as they were parsed.
type Doc struct {
	// Lines holds the source lines without their line breaks.
	Lines []string
	// FinalEOL is true when the last line ends with a line break.
	FinalEOL bool
	// Root is the top-level mapping, or nil for an empty document.
	Root *yaml.Node
	// Limit is the 1-based line where the document content stops: a
	// "..." end marker, or the line after the last line. New sections
	// go before it, because text after the marker is not read.
	Limit int
	// valueLines holds the lines after the first line of a multi-line
	// quoted or block scalar. Such a line can start with "#" and still
	// be value text, so the comment rules skip it.
	valueLines map[int]bool
	// bom is true when the first line starts with a byte order mark.
	// yaml.v3 does not count it as a column.
	bom bool
}

// docEndRe matches a document end marker at the start of a line.
var docEndRe = regexp.MustCompile(`^\.{3}(\s|$)`)

// yamlLineRe finds the line number in a yaml.v3 error message.
var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlSyntaxRe splits a yaml.v3 syntax error into its optional line
// number (group 1) and its problem text (group 2).
var yamlSyntaxRe = regexp.MustCompile(`yaml: (?:line (\d+): )?([^\n]*)`)

// yamlParserProblems are the problem texts of the yaml.v3 parser. The
// fail function of yaml.v3 adds 1 to the 0-based mark of a scanner
// error only. So the line of a parser error is 0-based, and yaml.v3
// leaves out a line 0. The mark is the start of the construct that
// holds the defect, or the token where the parser stopped. The texts
// come from parserc.go in gopkg.in/yaml.v3 v3.0.1. Scanner texts that
// also start with "did not find expected" are not in this list.
var yamlParserProblems = []string{
	"did not find expected <stream-start>",
	"did not find expected <document start>",
	"did not find expected node content",
	"did not find expected key",
	"did not find expected '-' indicator",
	"did not find expected ',' or ']'",
	"did not find expected ',' or '}'",
	"found undefined tag handle",
	"found duplicate %YAML directive",
	"found duplicate %TAG directive",
	"found incompatible YAML document",
}

// yamlTabProblem marks the scanner errors about a tab in indentation.
// Their mark is the start of the scalar before the tab, so the tab is
// on that line or on a later line.
const yamlTabProblem = "tab character"

// SplitLines splits LF text into lines and records the final break.
func SplitLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}

	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}

	return lines, false
}

// JoinLines is the inverse of SplitLines.
func JoinLines(lines []string, finalEOL bool) []byte {
	if len(lines) == 0 {
		return nil
	}

	text := strings.Join(lines, "\n")
	if finalEOL {
		text += "\n"
	}

	return []byte(text)
}

// NormalizeBreaks converts CRLF text to LF for editing. Mixed line
// breaks block the edit, because a splice could not keep both forms.
func NormalizeBreaks(raw []byte) (data []byte, crlf bool, issue *Issue) {
	pairs := bytes.Count(raw, []byte("\r\n"))
	if pairs == 0 {
		if i := bytes.IndexByte(raw, '\r'); i >= 0 {
			return raw, false, &Issue{Reason: "the file uses bare CR line breaks", Line: bytes.Count(raw[:i], []byte("\n")) + 1}
		}

		return raw, false, nil
	}

	if pairs != bytes.Count(raw, []byte("\n")) || pairs != bytes.Count(raw, []byte("\r")) {
		return raw, false, &Issue{Reason: "the file mixes CRLF and LF line breaks"}
	}

	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), true, nil
}

// Parse parses LF text and applies the structural gate. A problem that
// makes every edit unsafe returns an Issue with the line to open. The
// gate accepts one document whose top level is a block mapping or empty.
func Parse(data []byte) (*Doc, *Issue) {
	// The splice rules index the lines by the line numbers of yaml.v3.
	// Text that yaml.v3 counts differently stops here, before any index.
	if issue := textIssue(data); issue != nil {
		return nil, issue
	}

	lines, finalEOL := SplitLines(string(data))
	doc := &Doc{
		Lines:      lines,
		FinalEOL:   finalEOL,
		Limit:      len(lines) + 1,
		valueLines: map[int]bool{},
		bom:        len(lines) > 0 && strings.HasPrefix(lines[0], byteOrderMark),
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))

	var node yaml.Node

	err := dec.Decode(&node)
	if errors.Is(err, io.EOF) {
		return doc, nil
	}

	if err != nil {
		line := errorLine(err, lines)

		return nil, &Issue{Reason: "the file is not valid YAML: " + syntaxReason(err, line), Line: line}
	}

	// The readers decode the first document only. A second document is
	// either ignored in silence or a hidden syntax error, so it blocks.
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		line := next.Line
		if err != nil {
			line = errorLine(err, lines)
		}

		return nil, &Issue{Reason: "the file holds more than one YAML document", Line: line}
	}

	body := node.Content[0]

	// "---" alone decodes to an empty null: no mapping yet.
	if body.Kind == yaml.ScalarNode && body.Tag == "!!null" && body.Value == "" {
		doc.Limit = markerLimit(lines, 1)

		return doc, nil
	}

	if issue := checkRoot(body); issue != nil {
		return nil, issue
	}

	if issue := lineRange(body, len(lines)); issue != nil {
		return nil, issue
	}

	doc.Root = body
	doc.Limit = markerLimit(lines, body.Line)

	if issue := doc.markValues(body, 0); issue != nil {
		return nil, issue
	}

	return doc, nil
}

// byteOrderMark is the UTF-8 byte order mark.
const byteOrderMark = "\ufeff"

// textIssue refuses text whose lines yaml.v3 counts differently from
// SplitLines. yaml.v3 also reads UTF-16 and counts CR, NEL (U+0085),
// LS (U+2028), and PS (U+2029) as line breaks; SplitLines knows only
// UTF-8 and LF. CRLF text arrives here already converted to LF.
func textIssue(data []byte) *Issue {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])

		switch {
		case r == utf8.RuneError && size == 1:
			return &Issue{Reason: "the file is not UTF-8 text", Line: lineAt(data, i)}
		case r == '\r', r == '\u0085', r == '\u2028', r == '\u2029':
			return &Issue{
				Reason: fmt.Sprintf("the file holds the line break U+%04X; the editor counts LF line breaks only", r),
				Line:   lineAt(data, i),
			}
		}

		i += size
	}

	return nil
}

// lineAt returns the 1-based line of byte offset i.
func lineAt(data []byte, i int) int {
	return bytes.Count(data[:i], []byte("\n")) + 1
}

// lineRange refuses a node whose line is outside the text. It is the
// last guard before the line indexes: a count that differs from the
// one of yaml.v3 must stop the edit, never panic.
func lineRange(n *yaml.Node, count int) *Issue {
	if n.Line < 1 || n.Line > count {
		return &Issue{Reason: "the YAML parser and the editor count the lines of the file differently"}
	}

	for _, c := range n.Content {
		if issue := lineRange(c, count); issue != nil {
			return issue
		}
	}

	return nil
}

// Offset converts the 1-based yaml.v3 column col on line n into a byte
// offset. yaml.v3 counts characters, and it does not count a byte order
// mark at the start of the file.
func (d *Doc) Offset(n, col int) int {
	line, skip := d.Lines[n-1], 0
	if n == 1 && d.bom {
		skip = len(byteOrderMark)
	}

	return skip + byteOffset(line[skip:], col-1)
}

// markValues records the lines inside the multi-line quoted and block
// scalars under n. indent is the indentation of the collection that
// holds n; a block scalar measures its content against it.
func (d *Doc) markValues(n *yaml.Node, indent int) *Issue {
	switch n.Kind {
	case yaml.ScalarNode:
		return d.markScalar(n, indent)
	case yaml.MappingNode, yaml.SequenceNode:
		inner := d.BlockIndent(n)

		for _, c := range n.Content {
			if issue := d.markValues(c, inner); issue != nil {
				return issue
			}
		}
	}

	return nil
}

// BlockIndent returns the indentation of a block collection: the column
// of its keys or dashes. A tag or an anchor moves the node column, so
// the line of the first child gives the indentation then.
func (d *Doc) BlockIndent(n *yaml.Node) int {
	if (n.Style&yaml.TaggedStyle == 0 && n.Anchor == "") || len(n.Content) == 0 {
		return n.Column - 1
	}

	first := n.Content[0]
	if first.Line != n.Line {
		return LeadingSpaces(d.Lines[first.Line-1])
	}

	return first.Column - 1
}

// markScalar records the lines after the first line of one quoted or
// block scalar. A scalar that the scan cannot follow blocks the edit:
// the comment rules would be unsafe on its lines.
func (d *Doc) markScalar(n *yaml.Node, indent int) *Issue {
	var quote byte

	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		quote = '"'
	case n.Style&yaml.SingleQuotedStyle != 0:
		quote = '\''
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0:
		return nil
	}

	lost := &Issue{Reason: "the editor cannot find where a multi-line value ends", Line: n.Line}

	line, off, ok := d.valueStart(n)
	if !ok {
		return lost
	}

	var last int

	switch c := d.Lines[line-1][off]; {
	case quote != 0:
		if last, ok = d.closeQuote(line, off, quote); !ok {
			return lost
		}
	case c != '|' && c != '>':
		return lost
	default:
		last = d.blockEnd(line, off, indent)
	}

	for l := line + 1; l <= last; l++ {
		d.valueLines[l] = true
	}

	return nil
}

// valueStart returns the line and the byte offset where the text of a
// scalar starts. yaml.v3 puts the node column at its first property (a
// tag or an anchor), and the text can follow on a later line.
func (d *Doc) valueStart(n *yaml.Node) (line, off int, ok bool) {
	line, off = n.Line, d.Offset(n.Line, n.Column)

	for line <= len(d.Lines) {
		text := d.Lines[line-1]

		off = SkipProperties(text, off)
		if off < len(text) && text[off] != '#' {
			return line, off, true
		}

		line, off = line+1, 0
	}

	return 0, 0, false
}

// closeQuote returns the line of the quote that closes the quoted
// scalar that opens at byte off of line. A "#" inside the quotes is
// text, on any line.
func (d *Doc) closeQuote(line, off int, quote byte) (int, bool) {
	text := d.Lines[line-1]
	if off >= len(text) || text[off] != quote {
		return 0, false
	}

	from := off + 1

	for ; line <= len(d.Lines); line, from = line+1, 0 {
		if _, ok := scanQuote(d.Lines[line-1], from, quote); ok {
			return line, true
		}
	}

	return 0, false
}

// blockEnd returns the last content line of the block scalar whose
// header ("|" or ">") is at byte off of line. The rules follow yaml.v3:
// an explicit indentation indicator adds to the indentation of the
// holding collection; else the first lines set it, one more at least.
// The scalar ends before the first line with less indentation.
func (d *Doc) blockEnd(line, off, indent int) int {
	header, content := d.Lines[line-1], 0

	for i := off + 1; i < len(header) && i <= off+2; i++ {
		if c := header[i]; c >= '1' && c <= '9' {
			content = indent + int(c-'0')
		}
	}

	if content == 0 {
		content = max(indent+1, 1)

		for l := line + 1; l <= len(d.Lines); l++ {
			content = max(content, LeadingSpaces(d.Lines[l-1]))

			if !IsBlank(d.Lines[l-1]) {
				break
			}
		}
	}

	last := line

	for l := line + 1; l <= len(d.Lines); l++ {
		text := d.Lines[l-1]

		if IsBlank(text) {
			continue
		}

		if LeadingSpaces(text) < content {
			break
		}

		last = l
	}

	return last
}

// checkRoot applies the rules for the top-level mapping. A merge key
// or a duplicate key at the top level changes what every reader sees.
// Flow style has no line per key to splice.
func checkRoot(body *yaml.Node) *Issue {
	switch {
	case body.Kind != yaml.MappingNode:
		return &Issue{Reason: "the top level of the file is not a mapping", Line: body.Line}
	case body.Style&yaml.FlowStyle != 0:
		return &Issue{Reason: "the top level uses flow style { }", Line: body.Line}
	case body.Anchor != "":
		return &Issue{Reason: "the top level carries an anchor (&" + body.Anchor + ")", Line: body.Line}
	}

	seen := map[string]int{}

	for i := 0; i < len(body.Content); i += 2 {
		key := body.Content[i]

		switch {
		case key.Kind != yaml.ScalarNode:
			return &Issue{Reason: "the top level holds a complex key", Line: key.Line}
		case key.Tag == "!!merge":
			return &Issue{Reason: "the top level uses a merge key (<<)", Line: key.Line}
		}

		if first, dup := seen[key.Value]; dup {
			return &Issue{
				Reason: fmt.Sprintf("duplicate key %q (first at line %d)", key.Value, first),
				Line:   key.Line,
			}
		}

		seen[key.Value] = key.Line
	}

	return nil
}

// markerLimit returns the line of the first "..." end marker after
// line from, or the line after the last line.
func markerLimit(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if docEndRe.MatchString(lines[i]) {
			return i + 1
		}
	}

	return len(lines) + 1
}

// ErrorLine returns the 1-based line that a yaml.v3 error names, or 0.
// It corrects the 0-based line of a parser error, so the route does not
// name the line above the defect.
func ErrorLine(err error) int {
	text := err.Error()

	if m := yamlSyntaxRe.FindStringSubmatch(text); len(m) == 3 && parserProblem(m[2]) {
		// yaml.v3 leaves out a 0-based line 0, so no number means line 1.
		n, _ := strconv.Atoi(m[1])

		return n + 1
	}

	m := yamlLineRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}

	n, _ := strconv.Atoi(m[1])

	return n
}

// parserProblem reports whether problem is a yaml.v3 parser error.
func parserProblem(problem string) bool {
	for _, p := range yamlParserProblems {
		if strings.HasPrefix(problem, p) {
			return true
		}
	}

	return false
}

// errorLine finds the line to open for a yaml.v3 parse error in lines.
// A parser error at the end of the text names the line after the last
// line, so the last line is the line to open. A tab error names the
// scalar before the tab, and some tab errors name no line. The first
// indentation with a tab from that line on is then the line to open.
func errorLine(err error, lines []string) int {
	n := ErrorLine(err)

	if n > len(lines) {
		return len(lines)
	}

	if n == 0 || strings.Contains(err.Error(), yamlTabProblem) {
		if tab := tabLine(lines, max(n, 1)); tab > 0 {
			return tab
		}
	}

	return n
}

// tabLine returns the first 1-based line at or after from whose
// indentation holds a tab, or 0.
func tabLine(lines []string, from int) int {
	for i := from - 1; i < len(lines); i++ {
		l := lines[i]

		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if strings.Contains(indent, "\t") {
			return i + 1
		}
	}

	return 0
}

// syntaxReason renders a yaml.v3 syntax error for an Issue. The reason
// names the corrected line, so it agrees with Issue.Line. The readers
// keep the text of yaml.v3, because the commands print it.
func syntaxReason(err error, line int) string {
	text := err.Error()

	m := yamlSyntaxRe.FindStringSubmatchIndex(text)
	if len(m) < 4 || m[2] < 0 || line == 0 {
		return text
	}

	return text[:m[2]] + strconv.Itoa(line) + text[m[3]:]
}
