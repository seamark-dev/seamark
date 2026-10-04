package yamledit

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The tests from TestTokenEnd to TestErrorLineKeepsTheLinesOfOtherErrors
// moved here from internal/settings with the scanner. Their assertions
// are the same; only the names of the moved functions changed.

func TestTokenEnd(t *testing.T) {
	cases := []struct {
		line  string
		start int
		style yaml.Style
		want  string
		ok    bool
	}{
		{"  cli: claude", 7, 0, "claude", true},
		{"  cli: claude   # preset", 7, 0, "claude", true},
		{"  cli: a#b # c", 7, 0, "a#b", true},
		{"  cli: it's # my note", 7, 0, "it's", true},
		{`  cli: o"x  # note`, 7, 0, `o"x`, true},
		{"  cli: a\t# tab before the comment", 7, 0, "a", true},
		{`  cli: "cl\"aude" # q`, 7, yaml.DoubleQuotedStyle, `"cl\"aude"`, true},
		{`  cli: 'it''s' # q`, 7, yaml.SingleQuotedStyle, `'it''s'`, true},
		{`  cli: '# no' # q`, 7, yaml.SingleQuotedStyle, `'# no'`, true},
		{`  cli: "open`, 7, yaml.DoubleQuotedStyle, `"open`, false},
		{`  cli: 'open`, 7, yaml.SingleQuotedStyle, `'open`, false},
		{`  cli: !!str "x"`, 7, yaml.DoubleQuotedStyle, `!!str "x"`, false},
	}

	for _, c := range cases {
		end, ok := TokenEnd(c.line, c.start, c.style)
		assert.Equal(t, c.want, c.line[c.start:end], "line %q", c.line)
		assert.Equal(t, c.ok, ok, "line %q", c.line)
	}
}

func TestSkipProperties(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`!!str "claude"  # c`, `"claude"  # c`},
		{`&a !!str 'x'`, `'x'`},
		{`!<tag:yaml.org,2002:str> x`, `x`},
		{`!!seq[a]`, `[a]`},
		{`  plain`, `plain`},
		{`!!str`, ``},
		{`!!null # c`, `# c`},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, c.line[SkipProperties(c.line, 0):], "line %q", c.line)
	}
}

func TestFlowEnd(t *testing.T) {
	cases := []struct {
		text, want string
	}{
		{"exclude: [a, b]  # c\n", "exclude: [a, b]"},
		{"exclude: []\n", "exclude: []"},
		{"exclude: [it's]  # note\n", "exclude: [it's]"},
		{"exclude: [a 'b, 'c #d']  # note\n", "exclude: [a 'b, 'c #d']"},
		{"exclude: [\"x]\", y, ]  # c\n", "exclude: [\"x]\", y, ]"},
		{"exclude: !!seq [a]\n", "exclude: !!seq [a]"},
	}

	for _, c := range cases {
		doc := parsed(t, c.text)
		_, v, _ := doc.Section("exclude")

		end, ok := doc.FlowEnd(v)
		require.True(t, ok, "text %q", c.text)
		assert.Equal(t, c.want, doc.Lines[0][:end], "text %q", c.text)
	}
}

func TestCommentDetection(t *testing.T) {
	assert.Equal(t, 9, CommentStart("  a: 'x' # c", 0))
	assert.Equal(t, len("  a: '# no'"), CommentStart("  a: '# no'", 0), "a hash inside quotes is text")
	assert.Equal(t, len("  a: b#c"), CommentStart("  a: b#c", 0), "a hash needs white space before it")
	assert.Equal(t, 0, CommentStart("# top", 0))
	assert.Equal(t, 10, CommentStart("  a: it's # c", 0), "a quote inside a word opens no quotes")
	assert.Equal(t, 14, CommentStart("  a: [it's]   # c", 0), "a quote inside a flow item opens no quotes")
	assert.Equal(t, len("  a: ['x #']"), CommentStart("  a: ['x #']", 0), "a quote after [ opens quotes")

	assert.Equal(t, "   # aligned", TrailingComment("  exclude: [a]   # aligned", 11))
	assert.Equal(t, "", TrailingComment("  exclude: [a]", 11))
}

func TestByteOffsetCountsCharacters(t *testing.T) {
	assert.Equal(t, 3, byteOffset("é: 1", 2), "é is one column and two bytes")
	assert.Equal(t, 3, byteOffset("abc", 9), "the offset stops at the line end")
}

func TestNormalizeBreaks(t *testing.T) {
	data, crlf, issue := NormalizeBreaks([]byte("a: 1\n"))
	assert.Equal(t, "a: 1\n", string(data))
	assert.False(t, crlf)
	assert.Nil(t, issue)

	data, crlf, issue = NormalizeBreaks([]byte("a: 1\r\nb: 2\r\n"))
	assert.Equal(t, "a: 1\nb: 2\n", string(data))
	assert.True(t, crlf)
	assert.Nil(t, issue)

	_, _, issue = NormalizeBreaks([]byte("a: 1\r\nb: 2\n"))
	require.NotNil(t, issue)
	assert.Contains(t, issue.Reason, "mixes")
}

func TestTextGateMatchesTheLineCountOfYAML(t *testing.T) {
	require.Nil(t, textIssue([]byte("a: 1\n# ü é\n")))

	cases := []struct {
		text, reason string
		line         int
	}{
		{"a: 1\nb: 2\u2028\n", "U+2028", 2},
		{"a: 1\n\n# \u2029\n", "U+2029", 3},
		{"\u0085", "U+0085", 1},
		{"a: 1\rb: 2\n", "U+000D", 1},
		{"a: 1\nb: \xc3\n", "not UTF-8 text", 2},
	}

	for _, c := range cases {
		issue := textIssue([]byte(c.text))
		require.NotNil(t, issue, "text %q", c.text)
		assert.Contains(t, issue.Reason, c.reason)
		assert.Equal(t, c.line, issue.Line)
	}
}

func TestLineRangeStopsANodeOutsideTheText(t *testing.T) {
	inside := &yaml.Node{Line: 1, Content: []*yaml.Node{{Line: 2}}}
	assert.Nil(t, lineRange(inside, 2))

	outside := &yaml.Node{Line: 1, Content: []*yaml.Node{{Line: 1}, {Line: 3}}}
	issue := lineRange(outside, 2)
	require.NotNil(t, issue)
	assert.Contains(t, issue.Reason, "count the lines of the file differently")

	assert.NotNil(t, lineRange(&yaml.Node{Line: 0}, 2), "a line before the text is outside too")
}

func TestValueLinesAreNotComments(t *testing.T) {
	doc := parsed(t, `a: "x
  # q1"
b: |
  # b1

  # b2
# real 1
c: 'y
# q2'
d:
  - |2
      # d1
  # real 2
e: !!str
  "z
  # q3"
f: >-
 # f1
`)

	var comments []int

	for n := 1; n <= len(doc.Lines); n++ {
		if doc.Comment(n) {
			comments = append(comments, n)
		}
	}

	assert.Equal(t, []int{7, 13}, comments, "only the real comment lines")
}

func TestAValueThatTheScanCannotFollowBlocks(t *testing.T) {
	doc := &Doc{Lines: []string{"a: x"}, valueLines: map[int]bool{}}

	// The node claims a quoted value, but the text holds none.
	node := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Line: 1, Column: 4}

	issue := doc.markScalar(node, 0)
	require.NotNil(t, issue)
	assert.Contains(t, issue.Reason, "cannot find where a multi-line value ends")
	assert.Equal(t, 1, issue.Line)

	unclosed := &Doc{Lines: []string{`a: "x`, "  y"}, valueLines: map[int]bool{}}
	require.NotNil(t, unclosed.markScalar(node, 0), "a quote without its closing quote")

	literal := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.LiteralStyle, Line: 1, Column: 4}
	require.NotNil(t, doc.markScalar(literal, 0), "a block scalar without its header")
}

// padding returns n top-level keys, one per line, so that a defect
// after them is on line n+1. yaml.v3 hides its 0-based parser lines on
// lines 1 and 2 only.
func padding(n int) string {
	var b strings.Builder

	for i := range n {
		fmt.Fprintf(&b, "k%d: %d\n", i, i)
	}

	return b.String()
}

// Review fix: the route named the line above the defect, because
// yaml.v3 prints a 0-based line for a parser error.
func TestSyntaxErrorsNameTheDefectLine(t *testing.T) {
	cases := []struct {
		name, yaml string
		line       int
	}{
		{"unclosed flow list", padding(18) + "broken: [unclosed, list\n", 19},
		{"unclosed flow list before a blank line", padding(2) + "c: [x, y\n\nd: 1\n", 3},
		{"unclosed flow map", padding(6) + "g: {x: 1\nh: 7\ni: 8\n", 7},
		{"bad indentation of a key", padding(1) + "b:\n  c: 1\n d: 2\n", 4},
		{"bad indentation after a nested map", padding(3) + "d:\n  e: 1\n  f: 2\n j: 6\n", 7},
		// yaml.v3 names the first line of the nested block, not the
		// line of the defect. The route then opens that block.
		{"bad indentation inside a nested block", "index:\n  exclude:\n    - a\n   bad: 1\n", 2},
		{"tab in the indentation", padding(3) + "\tbad: tab\n", 4},
		{"tab in the indentation after blank lines", padding(1) + "b:\n  c: 1\n\n\n\td: 2\n", 6},
		{"tab in a block scalar", padding(1) + "b: |\n  x\n\t y\n", 4},
		{"unterminated quote", padding(2) + "c: 'unterminated\nd: 1\n", 3},
		{"undefined tag handle", padding(3) + "d: !x!y 1\n", 4},
		{"missing colon", padding(3) + "junk\ne: 5\n", 4},
		{"parser error at the end of the text", padding(3) + "d: [\n", 4},
		{"parser error on the first line without a line break", "agent: [", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, issue := Parse([]byte(c.yaml))
			require.NotNil(t, issue)

			assert.Equal(t, c.line, issue.Line)
			assert.Contains(t, issue.Reason, "the file is not valid YAML: yaml: ")
			assert.NotRegexp(t, `line (\d+)`, strings.ReplaceAll(issue.Reason, fmt.Sprintf("line %d:", c.line), ""),
				"the reason names the same line as the route")
		})
	}
}

func TestErrorLineKeepsTheLinesOfOtherErrors(t *testing.T) {
	// A reader type error names a 1-based node line, so it stays.
	err := errors.New("index config: yaml: unmarshal errors:\n  line 12: cannot unmarshal !!str `30` into int")
	assert.Equal(t, 12, ErrorLine(err))

	// A scanner error prints a 1-based line, so it stays.
	err = errors.New("agent config: yaml: line 3: did not find expected alphabetic or numeric character")
	assert.Equal(t, 3, ErrorLine(err))

	// A parser error prints a 0-based line, so it moves one line down.
	err = errors.New("agent config: yaml: line 3: did not find expected key")
	assert.Equal(t, 4, ErrorLine(err))

	assert.Equal(t, 0, ErrorLine(errors.New("permission denied")))
}

// parsed parses text that must pass the structural gate.
func parsed(t *testing.T, text string) *Doc {
	t.Helper()

	doc, issue := Parse([]byte(text))
	require.Nil(t, issue)

	return doc
}

// The tests below are new with the package. internal/settings covered
// these rules only through its Inspect results.

func TestIssueErrorNamesNoFile(t *testing.T) {
	assert.Equal(t, "duplicate key (line 3)", (&Issue{Reason: "duplicate key", Line: 3}).Error())
	assert.Equal(t, "no line", (&Issue{Reason: "no line"}).Error())
}

func TestStructuralGate(t *testing.T) {
	cases := []struct {
		name, text, reason string
		line               int
	}{
		{"second document", "a: 1\n---\nb: 2\n", "more than one YAML document", 2},
		{"top-level list", "- a\n- b\n", "the top level of the file is not a mapping", 1},
		{"top-level scalar", "just text\n", "the top level of the file is not a mapping", 1},
		{"flow mapping", "{a: 1}\n", "the top level uses flow style { }", 1},
		{"anchored top level", "&top\na: 1\n", "the top level carries an anchor (&top)", 1},
		{"complex key", "? [a, b]\n: 1\n", "the top level holds a complex key", 1},
		{"merge key", "base: &b\n  x: 1\n<<: *b\n", "the top level uses a merge key (<<)", 3},
		{"duplicate key", "pin:\n  - rule: a\nthreshold: 2\npin: []\n", `duplicate key "pin" (first at line 1)`, 4},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, issue := Parse([]byte(c.text))
			require.NotNil(t, issue)
			assert.Nil(t, doc)
			assert.Contains(t, issue.Reason, c.reason)
			assert.Equal(t, c.line, issue.Line)
		})
	}
}

func TestEmptyDocumentsParse(t *testing.T) {
	for _, text := range []string{"", "# only a comment\n", "---\n", "\n\n"} {
		doc := parsed(t, text)
		assert.Nil(t, doc.Root, "text %q", text)
	}

	doc := parsed(t, "a: 1\n...\n# after the marker\n")
	assert.Equal(t, 2, doc.Limit, "the end marker stops the content")

	doc = parsed(t, "a: 1\nb: 2")
	assert.Equal(t, 3, doc.Limit)
	assert.False(t, doc.FinalEOL)
	assert.Equal(t, "a: 1\nb: 2", string(JoinLines(doc.Lines, doc.FinalEOL)))
}

func TestSplitAndJoinKeepTheFinalBreak(t *testing.T) {
	for _, text := range []string{"", "a", "a\n", "a\n\n", "a\nb\n", "\n"} {
		lines, eol := SplitLines(text)
		assert.Equal(t, text, string(JoinLines(lines, eol)), "text %q", text)
	}
}

func TestOffsetSkipsTheByteOrderMark(t *testing.T) {
	doc := parsed(t, "\ufeffa: x\nb: y\n")

	_, v, _ := doc.Section("a")
	assert.Equal(t, "x", doc.Lines[0][doc.Offset(v.Line, v.Column):])

	_, v, _ = doc.Section("b")
	assert.Equal(t, "y", doc.Lines[1][doc.Offset(v.Line, v.Column):])
}

func TestSubtreeIssueFindsEveryAmbiguousConstruct(t *testing.T) {
	cases := []struct {
		name, text, reason string
		line               int
	}{
		{"alias", "base: &b x\npin:\n  - rule: *b\n", "the pin section uses an alias (*b)", 3},
		{"anchor", "pin:\n  - rule: a\n    region: &r api\n", "the pin section uses an anchor (&r)", 3},
		{"anchored item", "pin:\n  - &p\n    rule: a\n", "the pin section uses an anchor (&p)", 2},
		{"merge key", "base: &b\n  rule: a\npin:\n  - <<: *b\n    note: x\n", "the pin section uses a merge key (<<)", 4},
		{"duplicate key", "pin:\n  - rule: a\n    rule: b\n", `the pin section repeats the key "rule" (first at line 2)`, 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parsed(t, c.text)
			_, v, _ := doc.Section("pin")

			issue := SubtreeIssue("pin", v)
			require.NotNil(t, issue)
			assert.Equal(t, c.reason, issue.Reason)
			assert.Equal(t, c.line, issue.Line)
		})
	}

	doc := parsed(t, "pin:\n  - rule: a\n    regions: [api, db]\n  - rule: a\n")
	_, v, _ := doc.Section("pin")
	assert.Nil(t, SubtreeIssue("pin", v), "equal keys in two items are not duplicates")
}

func TestNodeEqualIgnoresPositionsAndComments(t *testing.T) {
	a := parsed(t, "x:   [a, b]  # one\n")
	b := parsed(t, "# head\n\nx: [a, b]\n")
	c := parsed(t, "x: [a, c]\n")
	d := parsed(t, "x:\n  - a\n  - b\n")

	assert.True(t, NodeEqual(a.Root, b.Root))
	assert.False(t, NodeEqual(a.Root, c.Root), "a value differs")
	assert.False(t, NodeEqual(a.Root, d.Root), "the style differs")
}

func TestLinePredicates(t *testing.T) {
	assert.True(t, IsBlank(" \t"))
	assert.False(t, IsBlank(" a"))
	assert.True(t, IsComment("   # c"))
	assert.False(t, IsComment("a: 1 # c"))
	assert.Equal(t, 3, IndentOf(" \t x"))
	assert.Equal(t, 1, LeadingSpaces(" \t x"), "a tab is not indentation")
	assert.True(t, IsNull(nil))
	assert.True(t, IsNull(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}))
	assert.False(t, IsNull(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}))
}
