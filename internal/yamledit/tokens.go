package yamledit

// Single-line token scanners. yaml.v3 gives the start of a node but not
// its end, so a splice scans the line for the end of the token and for
// a trailing comment after it.

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// flowStops are the flow indicators. A plain scalar inside a flow
// collection ends before any of them.
const flowStops = ",[]{}"

// TokenEnd returns the byte offset after the scalar token that starts
// at start, in block context. A quoted token ends at its closing quote;
// ok is false when that quote is not on the line. A plain token ends
// before a comment or at the line end, without trailing white space.
func TokenEnd(line string, start int, style yaml.Style) (end int, ok bool) {
	return scalarEnd(line, start, style, "")
}

// scalarEnd is TokenEnd with the characters that also end a plain
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

// SkipProperties returns the byte offset after the white space and the
// node properties (tags and anchors) at i. yaml.v3 puts a node column
// at its first property, but the splice needs the value text. A
// property ends at white space or at a flow indicator; a verbatim tag
// ("!<...>") ends at its ">".
func SkipProperties(line string, i int) int {
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

// FlowEnd returns the byte offset after the "]" of a flow list that
// sits on one line. yaml.v3 gives the start of each item, so only the
// last item needs a scan, and a quote or a "#" inside an item is text.
func (d *Doc) FlowEnd(v *yaml.Node) (int, bool) {
	line := d.Lines[v.Line-1]

	pos := SkipProperties(line, d.Offset(v.Line, v.Column))
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

		start := SkipProperties(line, d.Offset(last.Line, last.Column))
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

// CommentStart returns the byte offset of a trailing comment that starts
// at or after from, or the line length. A "#" starts a comment only
// outside quotes and after white space. A quote character opens quotes
// only where a token can start: at from, after white space, or after
// "[", "{", or ",". Inside a plain word, such as it's, it is text.
// Inside single quotes, YAML writes a quote as two quote characters, so
// such a pair does not close the value.
func CommentStart(line string, from int) int {
	var quote byte

	for i := from; i < len(line); i++ {
		c := line[i]

		switch {
		case quote == '"' && c == '\\':
			i++
		case quote == '\'' && c == '\'' && i+1 < len(line) && line[i+1] == '\'':
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

// TrailingComment returns the comment of a line after from, with the
// white space before it, or an empty string. The original spacing
// stays, because aligned comments are part of the reviewed layout.
func TrailingComment(line string, from int) string {
	i := CommentStart(line, from)
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
