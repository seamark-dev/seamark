package settings

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/integration"
)

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
		end, ok := tokenEnd(c.line, c.start, c.style)
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
		assert.Equal(t, c.want, c.line[skipProperties(c.line, 0):], "line %q", c.line)
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
		_, v, _ := doc.section("exclude")

		end, ok := doc.flowEnd(v)
		require.True(t, ok, "text %q", c.text)
		assert.Equal(t, c.want, doc.lines[0][:end], "text %q", c.text)
	}
}

func TestCommentDetection(t *testing.T) {
	assert.Equal(t, 9, commentStart("  a: 'x' # c", 0))
	assert.Equal(t, len("  a: '# no'"), commentStart("  a: '# no'", 0), "a hash inside quotes is text")
	assert.Equal(t, len("  a: b#c"), commentStart("  a: b#c", 0), "a hash needs white space before it")
	assert.Equal(t, 0, commentStart("# top", 0))
	assert.Equal(t, 10, commentStart("  a: it's # c", 0), "a quote inside a word opens no quotes")
	assert.Equal(t, 14, commentStart("  a: [it's]   # c", 0), "a quote inside a flow item opens no quotes")
	assert.Equal(t, len("  a: ['x #']"), commentStart("  a: ['x #']", 0), "a quote after [ opens quotes")

	assert.Equal(t, "   # aligned", trailingComment("  exclude: [a]   # aligned", 11))
	assert.Equal(t, "", trailingComment("  exclude: [a]", 11))
}

func TestByteOffsetCountsCharacters(t *testing.T) {
	assert.Equal(t, 3, byteOffset("é: 1", 2), "é is one column and two bytes")
	assert.Equal(t, 3, byteOffset("abc", 9), "the offset stops at the line end")
}

func TestNormalizeBreaks(t *testing.T) {
	data, crlf, blocked := normalizeBreaks([]byte("a: 1\n"))
	assert.Equal(t, "a: 1\n", string(data))
	assert.False(t, crlf)
	assert.Nil(t, blocked)

	data, crlf, blocked = normalizeBreaks([]byte("a: 1\r\nb: 2\r\n"))
	assert.Equal(t, "a: 1\nb: 2\n", string(data))
	assert.True(t, crlf)
	assert.Nil(t, blocked)

	_, _, blocked = normalizeBreaks([]byte("a: 1\r\nb: 2\n"))
	require.NotNil(t, blocked)
	assert.Contains(t, blocked.Reason, "mixes")
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
		blocked := textIssue([]byte(c.text))
		require.NotNil(t, blocked, "text %q", c.text)
		assert.Contains(t, blocked.Reason, c.reason)
		assert.Equal(t, c.line, blocked.Line)
	}
}

func TestLineRangeStopsANodeOutsideTheText(t *testing.T) {
	inside := &yaml.Node{Line: 1, Content: []*yaml.Node{{Line: 2}}}
	assert.Nil(t, lineRange(inside, 2))

	outside := &yaml.Node{Line: 1, Content: []*yaml.Node{{Line: 1}, {Line: 3}}}
	blocked := lineRange(outside, 2)
	require.NotNil(t, blocked)
	assert.Contains(t, blocked.Reason, "count the lines of the file differently")

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

	for n := 1; n <= len(doc.lines); n++ {
		if doc.comment(n) {
			comments = append(comments, n)
		}
	}

	assert.Equal(t, []int{7, 13}, comments, "only the real comment lines")
}

func TestAValueThatTheScanCannotFollowBlocks(t *testing.T) {
	doc := &document{lines: []string{"a: x"}, valueLines: map[int]bool{}}

	// The node claims a quoted value, but the text holds none.
	node := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Line: 1, Column: 4}

	blocked := doc.markScalar(node, 0)
	require.NotNil(t, blocked)
	assert.Contains(t, blocked.Reason, "cannot find where a multi-line value ends")
	assert.Equal(t, 1, blocked.Line)

	unclosed := &document{lines: []string{`a: "x`, "  y"}, valueLines: map[int]bool{}}
	require.NotNil(t, unclosed.markScalar(node, 0), "a quote without its closing quote")

	literal := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.LiteralStyle, Line: 1, Column: 4}
	require.NotNil(t, doc.markScalar(literal, 0), "a block scalar without its header")
}

func TestExtentsLeaveHeadCommentsToTheNextKey(t *testing.T) {
	doc, blocked := parseDocument([]byte(starterConfig(t)))
	require.Nil(t, blocked)

	_, _, idx := doc.section(sectionIndex)
	assert.Equal(t, 15, doc.sectionEnd(idx), "the index section ends at the commented example item")
	assert.Equal(t, 8, doc.childEnd(sectionIndex, "generated"), "the exclude head comment is not part of generated")
	assert.Equal(t, 15, doc.childEnd(sectionIndex, "exclude"))
}

func TestBlockedErrorNamesTheManualRoute(t *testing.T) {
	err := &BlockedError{Reason: "duplicate key", Line: 3}

	assert.Equal(t, "duplicate key (line 3); edit .seamark/config.yaml by hand", err.Error())
	assert.ErrorIs(t, err, ErrBlocked)
	assert.Equal(t, "no line; edit .seamark/config.yaml by hand", (&BlockedError{Reason: "no line"}).Error())
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
			_, blocked := parseDocument([]byte(c.yaml))
			require.NotNil(t, blocked)

			assert.Equal(t, c.line, blocked.Line)
			assert.Contains(t, blocked.Reason, "the file is not valid YAML: yaml: ")
			assert.NotRegexp(t, `line (\d+)`, strings.ReplaceAll(blocked.Reason, fmt.Sprintf("line %d:", c.line), ""),
				"the reason names the same line as the route")
		})
	}
}

func TestErrorLineKeepsTheLinesOfOtherErrors(t *testing.T) {
	// A reader type error names a 1-based node line, so it stays.
	err := errors.New("index config: yaml: unmarshal errors:\n  line 12: cannot unmarshal !!str `30` into int")
	assert.Equal(t, 12, yamlErrorLine(err))

	// A scanner error prints a 1-based line, so it stays.
	err = errors.New("agent config: yaml: line 3: did not find expected alphabetic or numeric character")
	assert.Equal(t, 3, yamlErrorLine(err))

	// A parser error prints a 0-based line, so it moves one line down.
	err = errors.New("agent config: yaml: line 3: did not find expected key")
	assert.Equal(t, 4, yamlErrorLine(err))

	assert.Equal(t, 0, yamlErrorLine(errors.New("permission denied")))
}

// parsed parses text that must pass the structural gate.
func parsed(t *testing.T, text string) *document {
	t.Helper()

	doc, blocked := parseDocument([]byte(text))
	require.Nil(t, blocked)

	return doc
}

func TestVerifyRefusesAChangeOutsideTheEditedSection(t *testing.T) {
	err := sameOtherSections(parsed(t, "a: 1\nagent:\n  cli: x\n"), parsed(t, "a: 2\nagent:\n  cli: y\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the a section changed")

	err = sameOtherSections(parsed(t, "agent:\n  cli: x\na: 1\n"), parsed(t, "a: 1\nagent:\n  cli: x\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the agent section moved")

	err = sameOtherSections(parsed(t, "a: 1\n"), parsed(t, "a: 1\nb: 2\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the other top-level keys changed")

	require.NoError(t, sameOtherSections(parsed(t, "a: &x 1\n"), parsed(t, "a: &x 1\nagent:\n  cli: x\n"), sectionAgent))
}

func TestVerifyRefusesAnUnintendedChangeInTheSection(t *testing.T) {
	before := parsed(t, "agent:\n  cli: claude\n  model: opus\n")

	err := intendedSection(before, parsed(t, "agent:\n  cli: codex\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, String: "codex"}})
	require.Error(t, err, "an unknown nested key disappeared")

	require.NoError(t, intendedSection(before, parsed(t, "agent:\n  cli: codex\n  model: opus\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, String: "codex"}}))

	require.NoError(t, intendedSection(parsed(t, "agent:\n  cli: x\n"), parsed(t, "a: 1\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, Reset: true}}), "an empty section equals no section")
}

func TestVerifyRefusesAResultThatAReaderRejects(t *testing.T) {
	// The draft check never lets this pattern through; verify is the net
	// under it, so it is called here with the bytes directly.
	root := withConfig(t, "index:\n  generated: true\n")
	snap := inspect(t, root)

	edits := []Edit{{Field: IndexExclude, List: []string{"a/**/b"}}}
	after := []byte("index:\n  generated: true\n  exclude:\n    - a/**/b\n")

	_, err := verify(snap, integration.Builtin(), GroupIndexing, edits, after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to write")
	assert.Contains(t, err.Error(), "the index reader would reject the result")
}
