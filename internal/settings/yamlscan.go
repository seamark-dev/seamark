package settings

// The YAML analysis. The node tree of yaml.v3 finds keys and their
// lines; it never produces the written bytes. A re-encode of the whole
// document drops blank lines and moves comments. The file is committed
// and reviewed, so edits splice the source text instead (patch.go).

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

// Section names of the owned configuration keys. One reader owns each
// section: agent, distill, and index (which also reads reviews).
const (
	sectionAgent   = "agent"
	sectionDistill = "distill"
	sectionIndex   = "index"
	sectionReviews = "reviews"
)

// ownedSections lists the sections that the readers decode. A duplicate
// key, an alias, or a merge key in them changes what a reader sees. The
// analysis therefore looks inside these sections only.
var ownedSections = []string{sectionAgent, sectionDistill, sectionIndex, sectionReviews}

// document is one parsed configuration file with its source lines.
type document struct {
	// lines holds the source lines without their line breaks.
	lines []string
	// finalEOL is true when the last line ends with a line break.
	finalEOL bool
	// root is the top-level mapping, or nil for an empty document.
	root *yaml.Node
	// limit is the 1-based line where the document content stops: a
	// "..." end marker, or the line after the last line. New sections
	// go before it, because text after the marker is not read.
	limit int
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

// splitLines splits LF text into lines and records the final break.
func splitLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}

	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}

	return lines, false
}

// joinLines is the inverse of splitLines.
func joinLines(lines []string, finalEOL bool) []byte {
	if len(lines) == 0 {
		return nil
	}

	text := strings.Join(lines, "\n")
	if finalEOL {
		text += "\n"
	}

	return []byte(text)
}

// parseDocument parses LF text and applies the structural gate. A
// problem that makes every edit unsafe returns a BlockedError with the
// line to open.
func parseDocument(data []byte) (*document, *BlockedError) {
	// The splice rules index the lines by the line numbers of yaml.v3.
	// Text that yaml.v3 counts differently stops here, before any index.
	if blocked := textIssue(data); blocked != nil {
		return nil, blocked
	}

	lines, finalEOL := splitLines(string(data))
	doc := &document{
		lines:      lines,
		finalEOL:   finalEOL,
		limit:      len(lines) + 1,
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

		return nil, &BlockedError{Reason: "the file is not valid YAML: " + syntaxReason(err, line), Line: line}
	}

	// The readers decode the first document only. A second document is
	// either ignored in silence or a hidden syntax error, so it blocks.
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		line := next.Line
		if err != nil {
			line = errorLine(err, lines)
		}

		return nil, &BlockedError{Reason: "the file holds more than one YAML document", Line: line}
	}

	body := node.Content[0]

	// "---" alone decodes to an empty null: no mapping yet.
	if body.Kind == yaml.ScalarNode && body.Tag == "!!null" && body.Value == "" {
		doc.limit = markerLimit(lines, 1)

		return doc, nil
	}

	if blocked := checkRoot(body); blocked != nil {
		return nil, blocked
	}

	if blocked := lineRange(body, len(lines)); blocked != nil {
		return nil, blocked
	}

	doc.root = body
	doc.limit = markerLimit(lines, body.Line)

	if blocked := doc.markValues(body, 0); blocked != nil {
		return nil, blocked
	}

	return doc, nil
}

// byteOrderMark is the UTF-8 byte order mark.
const byteOrderMark = "\ufeff"

// textIssue refuses text whose lines yaml.v3 counts differently from
// splitLines. yaml.v3 also reads UTF-16 and counts CR, NEL (U+0085),
// LS (U+2028), and PS (U+2029) as line breaks; splitLines knows only
// UTF-8 and LF. CRLF text arrives here already converted to LF.
func textIssue(data []byte) *BlockedError {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])

		switch {
		case r == utf8.RuneError && size == 1:
			return &BlockedError{Reason: "the file is not UTF-8 text", Line: lineAt(data, i)}
		case r == '\r', r == '\u0085', r == '\u2028', r == '\u2029':
			return &BlockedError{
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
func lineRange(n *yaml.Node, count int) *BlockedError {
	if n.Line < 1 || n.Line > count {
		return &BlockedError{Reason: "the YAML parser and the editor count the lines of the file differently"}
	}

	for _, c := range n.Content {
		if blocked := lineRange(c, count); blocked != nil {
			return blocked
		}
	}

	return nil
}

// offset converts the 1-based yaml.v3 column col on line n into a byte
// offset. yaml.v3 counts characters, and it does not count a byte order
// mark at the start of the file.
func (d *document) offset(n, col int) int {
	line, skip := d.lines[n-1], 0
	if n == 1 && d.bom {
		skip = len(byteOrderMark)
	}

	return skip + byteOffset(line[skip:], col-1)
}

// markValues records the lines inside the multi-line quoted and block
// scalars under n. indent is the indentation of the collection that
// holds n; a block scalar measures its content against it.
func (d *document) markValues(n *yaml.Node, indent int) *BlockedError {
	switch n.Kind {
	case yaml.ScalarNode:
		return d.markScalar(n, indent)
	case yaml.MappingNode, yaml.SequenceNode:
		inner := d.blockIndent(n)

		for _, c := range n.Content {
			if blocked := d.markValues(c, inner); blocked != nil {
				return blocked
			}
		}
	}

	return nil
}

// blockIndent returns the indentation of a block collection: the column
// of its keys or dashes. A tag or an anchor moves the node column, so
// the line of the first child gives the indentation then.
func (d *document) blockIndent(n *yaml.Node) int {
	if (n.Style&yaml.TaggedStyle == 0 && n.Anchor == "") || len(n.Content) == 0 {
		return n.Column - 1
	}

	first := n.Content[0]
	if first.Line != n.Line {
		return leadingSpaces(d.lines[first.Line-1])
	}

	return first.Column - 1
}

// markScalar records the lines after the first line of one quoted or
// block scalar. A scalar that the scan cannot follow blocks the edit:
// the comment rules would be unsafe on its lines.
func (d *document) markScalar(n *yaml.Node, indent int) *BlockedError {
	var quote byte

	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		quote = '"'
	case n.Style&yaml.SingleQuotedStyle != 0:
		quote = '\''
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0:
		return nil
	}

	lost := &BlockedError{Reason: "the editor cannot find where a multi-line value ends", Line: n.Line}

	line, off, ok := d.valueStart(n)
	if !ok {
		return lost
	}

	var last int

	switch c := d.lines[line-1][off]; {
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
func (d *document) valueStart(n *yaml.Node) (line, off int, ok bool) {
	line, off = n.Line, d.offset(n.Line, n.Column)

	for line <= len(d.lines) {
		text := d.lines[line-1]

		off = skipProperties(text, off)
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
func (d *document) closeQuote(line, off int, quote byte) (int, bool) {
	text := d.lines[line-1]
	if off >= len(text) || text[off] != quote {
		return 0, false
	}

	from := off + 1

	for ; line <= len(d.lines); line, from = line+1, 0 {
		if _, ok := scanQuote(d.lines[line-1], from, quote); ok {
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
func (d *document) blockEnd(line, off, indent int) int {
	header, content := d.lines[line-1], 0

	for i := off + 1; i < len(header) && i <= off+2; i++ {
		if c := header[i]; c >= '1' && c <= '9' {
			content = indent + int(c-'0')
		}
	}

	if content == 0 {
		content = max(indent+1, 1)

		for l := line + 1; l <= len(d.lines); l++ {
			content = max(content, leadingSpaces(d.lines[l-1]))

			if !isBlank(d.lines[l-1]) {
				break
			}
		}
	}

	last := line

	for l := line + 1; l <= len(d.lines); l++ {
		text := d.lines[l-1]

		if isBlank(text) {
			continue
		}

		if leadingSpaces(text) < content {
			break
		}

		last = l
	}

	return last
}

// checkRoot applies the rules for the top-level mapping. A merge key
// or a duplicate key at the top level changes what every reader sees.
// Flow style has no line per key to splice.
func checkRoot(body *yaml.Node) *BlockedError {
	switch {
	case body.Kind != yaml.MappingNode:
		return &BlockedError{Reason: "the top level of the file is not a mapping", Line: body.Line}
	case body.Style&yaml.FlowStyle != 0:
		return &BlockedError{Reason: "the top level uses flow style { }", Line: body.Line}
	case body.Anchor != "":
		return &BlockedError{Reason: "the top level carries an anchor (&" + body.Anchor + ")", Line: body.Line}
	}

	seen := map[string]int{}

	for i := 0; i < len(body.Content); i += 2 {
		key := body.Content[i]

		switch {
		case key.Kind != yaml.ScalarNode:
			return &BlockedError{Reason: "the top level holds a complex key", Line: key.Line}
		case key.Tag == "!!merge":
			return &BlockedError{Reason: "the top level uses a merge key (<<)", Line: key.Line}
		}

		if first, dup := seen[key.Value]; dup {
			return &BlockedError{
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

// yamlErrorLine returns the 1-based line that a yaml.v3 error names,
// or 0. It corrects the 0-based line of a parser error, so the route
// does not name the line above the defect.
func yamlErrorLine(err error) int {
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
	n := yamlErrorLine(err)

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

// syntaxReason renders a yaml.v3 syntax error for a BlockedError. The
// reason names the corrected line, so it agrees with BlockedError.Line.
// The readers keep the text of yaml.v3, because the commands print it.
func syntaxReason(err error, line int) string {
	text := err.Error()

	m := yamlSyntaxRe.FindStringSubmatchIndex(text)
	if len(m) < 4 || m[2] < 0 || line == 0 {
		return text
	}

	return text[:m[2]] + strconv.Itoa(line) + text[m[3]:]
}

// section returns the key node, the value node, and the pair index of a
// top-level section. The nodes are nil when the section is absent.
func (d *document) section(name string) (key, value *yaml.Node, idx int) {
	if d.root == nil {
		return nil, nil, -1
	}

	return lookup(d.root, name)
}

// lookup returns the key and value nodes of name in mapping m.
func lookup(m *yaml.Node, name string) (key, value *yaml.Node, idx int) {
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

// child returns the key and value nodes of one field in a section.
func (d *document) child(sectionName, key string) (k, v *yaml.Node) {
	_, value, _ := d.section(sectionName)
	k, v, _ = lookup(value, key)

	return k, v
}

// sectionEnd returns the last line of a top-level section.
func (d *document) sectionEnd(idx int) int {
	return d.extent(d.root, idx, d.limit)
}

// childEnd returns the last line of one field inside a section.
func (d *document) childEnd(sectionName, key string) int {
	_, value, sidx := d.section(sectionName)
	_, _, cidx := lookup(value, key)

	return d.extent(value, cidx, d.sectionEnd(sidx)+1)
}

// extent returns the last line (1-based, inclusive) of the entry whose
// key is at pair index i of mapping m. stop is the first line after the
// parent's extent. The entry runs to the line before the next key.
// Trailing blank lines, and comments no deeper than the key, belong to
// the next key or to the document foot, so they are left out.
func (d *document) extent(m *yaml.Node, i, stop int) int {
	key := m.Content[i]

	end := stop - 1
	if i+2 < len(m.Content) {
		end = m.Content[i+2].Line - 1
	}

	indent := key.Column - 1

	for end > key.Line {
		l := d.lines[end-1]

		if isBlank(l) || (d.comment(end) && indentOf(l) <= indent) {
			end--

			continue
		}

		break
	}

	return end
}

// comment reports whether line n holds only a comment. A line of a
// multi-line value is value text, even when it starts with "#".
func (d *document) comment(n int) bool {
	return isComment(d.lines[n-1]) && !d.valueLines[n]
}

// contentLines lists the lines in [from, to] that hold YAML content:
// neither blank nor a comment.
func (d *document) contentLines(from, to int) []int {
	var out []int

	for n := from; n <= to; n++ {
		if !isBlank(d.lines[n-1]) && !d.comment(n) {
			out = append(out, n)
		}
	}

	return out
}

// hasComment reports whether any line in [from, to] is a comment line.
func (d *document) hasComment(from, to int) bool {
	for n := from; n <= to; n++ {
		if d.comment(n) {
			return true
		}
	}

	return false
}

// sectionIssue explains why one owned section cannot be edited, or
// returns nil. An alias, an anchor, or a merge key makes the effect of
// an edit ambiguous: an edit would also change another place, or a
// merged value would stay in effect. A duplicate key hides one of the
// two values. Flow style has no line per key to splice.
func (d *document) sectionIssue(name string) *BlockedError {
	key, value, _ := d.section(name)
	if key == nil {
		return nil
	}

	if key.Anchor != "" {
		return &BlockedError{Reason: fmt.Sprintf("the %s section key carries an anchor", name), Line: key.Line}
	}

	if blocked := walkOwned(name, value); blocked != nil {
		return blocked
	}

	switch {
	case value.Kind == yaml.MappingNode && value.Style&yaml.FlowStyle != 0:
		return &BlockedError{Reason: fmt.Sprintf("the %s section uses flow style { }", name), Line: value.Line}
	case value.Kind != yaml.MappingNode && !isNull(value):
		return &BlockedError{Reason: fmt.Sprintf("the %s section is not a mapping", name), Line: value.Line}
	}

	return nil
}

// walkOwned finds the first alias, anchor, merge key, or duplicate key
// in the subtree of an owned section.
func walkOwned(section string, n *yaml.Node) *BlockedError {
	switch {
	case n.Kind == yaml.AliasNode:
		return &BlockedError{Reason: fmt.Sprintf("the %s section uses an alias (*%s)", section, n.Value), Line: n.Line}
	case n.Anchor != "":
		return &BlockedError{Reason: fmt.Sprintf("the %s section uses an anchor (&%s)", section, n.Anchor), Line: n.Line}
	}

	if n.Kind == yaml.MappingNode {
		seen := map[string]int{}

		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]

			if key.Tag == "!!merge" {
				return &BlockedError{Reason: fmt.Sprintf("the %s section uses a merge key (<<)", section), Line: key.Line}
			}

			if first, dup := seen[key.Value]; dup {
				return &BlockedError{
					Reason: fmt.Sprintf("the %s section repeats the key %q (first at line %d)", section, key.Value, first),
					Line:   key.Line,
				}
			}

			seen[key.Value] = key.Line
		}
	}

	for _, c := range n.Content {
		if blocked := walkOwned(section, c); blocked != nil {
			return blocked
		}
	}

	return nil
}

// isNull reports whether a node is a YAML null ("", "~", or "null").
func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// isBlank reports whether a line holds only white space.
func isBlank(l string) bool {
	return strings.TrimSpace(l) == ""
}

// isComment reports whether a line holds only a comment.
func isComment(l string) bool {
	return strings.HasPrefix(strings.TrimSpace(l), "#")
}

// indentOf returns the number of leading white-space characters.
func indentOf(l string) int {
	return len(l) - len(strings.TrimLeft(l, " \t"))
}

// leadingSpaces returns the number of leading spaces. YAML indentation
// is spaces only; a tab is never part of it.
func leadingSpaces(l string) int {
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

// flowStops are the flow indicators. A plain scalar inside a flow
// collection ends before any of them.
const flowStops = ",[]{}"

// tokenEnd returns the byte offset after the scalar token that starts
// at start, in block context. A quoted token ends at its closing quote;
// ok is false when that quote is not on the line. A plain token ends
// before a comment or at the line end, without trailing white space.
func tokenEnd(line string, start int, style yaml.Style) (end int, ok bool) {
	return scalarEnd(line, start, style, "")
}

// scalarEnd is tokenEnd with the characters that also end a plain
// token: none in block context, the flow indicators in flow context.
func scalarEnd(line string, start int, style yaml.Style, stops string) (int, bool) {
	switch {
	case style&yaml.DoubleQuotedStyle != 0:
		return quotedEnd(line, start, '"')
	case style&yaml.SingleQuotedStyle != 0:
		return quotedEnd(line, start, '\'')
	}

	return plainEnd(line, start, stops), true
}

// quotedEnd returns the byte offset after the closing quote of a token
// that opens with quote at start.
func quotedEnd(line string, start int, quote byte) (int, bool) {
	if start >= len(line) || line[start] != quote {
		return len(line), false
	}

	return scanQuote(line, start+1, quote)
}

// scanQuote returns the byte offset after the closing quote at or after
// from. In double quotes a backslash escapes the next character; in
// single quotes two quote characters are one quote character.
func scanQuote(line string, from int, quote byte) (int, bool) {
	for i := from; i < len(line); i++ {
		switch {
		case quote == '"' && line[i] == '\\':
			i++
		case line[i] != quote:
		case quote == '\'' && i+1 < len(line) && line[i+1] == '\'':
			i++
		default:
			return i + 1, true
		}
	}

	return len(line), false
}

// plainEnd returns the byte offset after a plain token that starts at
// start. A plain scalar cannot hold " #", so the first "#" after white
// space ends it. A quote character inside it is text, not a quote.
func plainEnd(line string, start int, stops string) int {
	end := len(line)

	for i := start; i < len(line); i++ {
		if strings.IndexByte(stops, line[i]) >= 0 || (line[i] == '#' && i > 0 && isSpace(line[i-1])) {
			end = i

			break
		}
	}

	return start + len(strings.TrimRight(line[start:end], " \t"))
}

// isSpace reports a YAML white-space character.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t'
}

// skipProperties returns the byte offset after the white space and the
// node properties (tags and anchors) at i. yaml.v3 puts a node column
// at its first property, but the splice needs the value text. A
// property ends at white space or at a flow indicator; a verbatim tag
// ("!<...>") ends at its ">".
func skipProperties(line string, i int) int {
	for {
		for i < len(line) && isSpace(line[i]) {
			i++
		}

		if i >= len(line) || (line[i] != '!' && line[i] != '&') {
			return i
		}

		if strings.HasPrefix(line[i:], "!<") {
			j := strings.IndexByte(line[i:], '>')
			if j < 0 {
				return len(line)
			}

			i += j + 1

			continue
		}

		for i < len(line) && !isSpace(line[i]) && strings.IndexByte(flowStops, line[i]) < 0 {
			i++
		}
	}
}

// flowEnd returns the byte offset after the "]" of a flow list that
// sits on one line. yaml.v3 gives the start of each item, so only the
// last item needs a scan, and a quote or a "#" inside an item is text.
func (d *document) flowEnd(v *yaml.Node) (int, bool) {
	line := d.lines[v.Line-1]

	pos := skipProperties(line, d.offset(v.Line, v.Column))
	if pos >= len(line) || line[pos] != '[' {
		return 0, false
	}

	pos++

	if n := len(v.Content); n > 0 {
		last := v.Content[n-1]
		if last.Kind != yaml.ScalarNode || last.Line != v.Line {
			return 0, false
		}

		var ok bool

		start := skipProperties(line, d.offset(last.Line, last.Column))
		if pos, ok = scalarEnd(line, start, last.Style, flowStops); !ok {
			return 0, false
		}
	}

	for pos < len(line) && (isSpace(line[pos]) || line[pos] == ',') {
		pos++
	}

	if pos >= len(line) || line[pos] != ']' {
		return 0, false
	}

	return pos + 1, true
}

// commentStart returns the byte offset of a trailing comment that starts
// at or after from, or the line length. A "#" starts a comment only
// outside quotes and after white space. A quote character opens quotes
// only where a token can start: at from, after white space, or after
// "[", "{", or ",". Inside a plain word, such as it's, it is text.
func commentStart(line string, from int) int {
	var quote byte

	for i := from; i < len(line); i++ {
		c := line[i]

		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			if i == from || isSpace(line[i-1]) || strings.IndexByte("[{,", line[i-1]) >= 0 {
				quote = c
			}
		case c == '#' && (i == 0 || isSpace(line[i-1])):
			return i
		}
	}

	return len(line)
}

// trailingComment returns the comment of a line after from, with the
// white space before it, or an empty string. The original spacing
// stays, because aligned comments are part of the reviewed layout.
func trailingComment(line string, from int) string {
	i := commentStart(line, from)
	if i == len(line) {
		return ""
	}

	j := i
	for j > from && (line[j-1] == ' ' || line[j-1] == '\t') {
		j--
	}

	if j == i {
		return " " + line[i:]
	}

	return line[j:]
}
