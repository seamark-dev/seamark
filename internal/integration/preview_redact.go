package integration

// The preview redaction layer. A native client file also holds the
// settings of other servers, and those settings often carry
// credentials. The shared redact patterns miss common shapes, for
// example "X-API-Key" or a quoted TOML key. So this layer reads the
// structure of a JSON or TOML file and hides string values by their key
// and by the table that holds them. It is for display only: apply
// writes the planned bytes, never the preview. It stays local to the
// preview, because wider shared patterns would also change the audit
// log and the mined review text.

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/redact"
)

// redactedValue replaces a hidden value. It is the marker of the shared
// redact package, so every hidden value reads the same.
const redactedValue = "[REDACTED]"

// credentialWords are the key segments that name a credential. A
// segment matches with or without a plural "s".
var credentialWords = map[string]bool{
	"key": true, "token": true, "secret": true, "password": true, "passwd": true,
	"credential": true, "auth": true, "authorization": true, "cookie": true,
	"session": true, "pat": true,
}

// credentialSuffixes also match at the end of a segment, because
// "apikey" or "accesstoken" join the words without a separator. The
// short words are not in this list: "oauth" or "compat" name no
// credential.
var credentialSuffixes = []string{"key", "token", "secret", "password", "passwd"}

// credentialTables hold environment variables or HTTP headers. Every
// string value in them is hidden, whatever its key, because a value
// such as a bare token has no name that marks it. The names are
// compared without case, "_" and "-".
var credentialTables = map[string]bool{
	"env": true, "environment": true, "headers": true, "httpheaders": true, "envhttpheaders": true,
}

// serverTables hold one entry for each MCP server in the client files.
var serverTables = map[string]bool{"mcpServers": true, "mcp_servers": true, "servers": true}

// headerFlags take an HTTP header as their next argument, for example
// `--header "X-API-Key: sk-..."`.
var headerFlags = map[string]bool{"--header": true, "-H": true}

// authorizationValue matches an Authorization header inside a string
// with any scheme, for example "Authorization: Basic dXNl". The shared
// patterns cover only the Bearer scheme.
var authorizationValue = regexp.MustCompile(`(?i)(\bauthorization\s*[:=]\s*)(?:[a-z][a-z0-9._~+/-]*\s+)?[^\s"',;]+`)

// maskRange is a byte range to hide: in the file for the scanner, and
// in one line after lineMasks splits the ranges by line.
type maskRange struct {
	start, end int
}

// diffSide is one side of a preview diff: the file lines and the ranges
// to hide on each line. The ranges come from the whole file, because a
// hunk often starts below the table that makes a value secret.
type diffSide struct {
	lines []string
	hide  [][]maskRange
}

// newDiffSide splits content into diff lines and finds the ranges to
// hide. Only JSON and TOML files are scanned: those are the native
// client files. Other files keep the shared redaction only, so the
// Seamark documents stay readable for review.
func newDiffSide(rel string, content []byte) diffSide {
	side := diffSide{lines: diffLines(content)}

	if dialect, ok := structuredDialect(rel); ok {
		side.hide = lineMasks(string(content), scanSecrets(string(content), dialect))
	}

	return side
}

// hideAt returns the ranges to hide on line i. The missing-newline
// marker line is not in the file, so it has no ranges.
func (d diffSide) hideAt(i int) []maskRange {
	if i < len(d.hide) {
		return d.hide[i]
	}

	return nil
}

// fileDialect is the syntax the scanner reads.
type fileDialect int

const (
	dialectJSON fileDialect = iota
	dialectTOML
)

// structuredDialect returns the dialect of a native client file, by
// its extension.
func structuredDialect(rel string) (fileDialect, bool) {
	switch strings.ToLower(path.Ext(rel)) {
	case ".json", ".jsonc":
		return dialectJSON, true
	case ".toml":
		return dialectTOML, true
	default:
		return 0, false
	}
}

// scrubLine applies the shared redaction to one diff line. A native
// client file also gets the Authorization rule, because a header can
// sit in an argument list, where no key names it.
func scrubLine(rel, text string) string {
	if _, ok := structuredDialect(rel); ok {
		text = authorizationValue.ReplaceAllString(text, "${1}"+redactedValue)
	}

	return redact.Secrets(text)
}

// applyMasks replaces the hidden ranges of one line. The ranges must be
// sorted and must not overlap.
func applyMasks(line string, masks []maskRange) string {
	if len(masks) == 0 {
		return line
	}

	var b strings.Builder

	prev := 0

	for _, m := range masks {
		b.WriteString(line[prev:m.start])
		b.WriteString(maskText(line[m.start:m.end]))
		prev = m.end
	}

	b.WriteString(line[prev:])

	return b.String()
}

// maskText returns the replacement of one hidden range. A string that
// starts and ends on the line keeps its quotes, so the line still reads
// as JSON or TOML.
func maskText(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[:1] + redactedValue + s[:1]
	}

	return redactedValue
}

// mergeMasks joins the ranges of the same line from both sides of the
// diff. An unchanged line shows once, so it must hide what either side
// hides.
func mergeMasks(a, b []maskRange) []maskRange {
	if len(a) == 0 {
		return b
	}

	if len(b) == 0 {
		return a
	}

	all := slices.Concat(a, b)
	slices.SortFunc(all, func(x, y maskRange) int { return x.start - y.start })

	out := all[:1]

	for _, m := range all[1:] {
		last := &out[len(out)-1]
		if m.start <= last.end {
			last.end = max(last.end, m.end)

			continue
		}

		out = append(out, m)
	}

	return out
}

// lineMasks splits the file ranges by line. Each line gets the part of
// every range that falls on it, relative to the line start and without
// the newline. The result is parallel to the lines of diffLines.
func lineMasks(text string, masks []maskRange) [][]maskRange {
	var out [][]maskRange

	k := 0

	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}

		var line []maskRange

		// A range that ends on this line is done; a range that goes on
		// stays for the next line.
		for k < len(masks) && masks[k].end <= start {
			k++
		}

		for i := k; i < len(masks) && masks[i].start < end; i++ {
			if lo, hi := max(masks[i].start, start), min(masks[i].end, end); lo < hi {
				line = append(line, maskRange{lo - start, hi - start})
			}
		}

		out = append(out, line)
		start = end + 1
	}

	return out
}

// scanFrame is one open container of the scan: an object, a TOML
// table, or an array.
type scanFrame struct {
	// array is true for an array; objects and tables have keys.
	array bool
	// path holds the keys from the file root to the container.
	path []string
	// key holds the last key read in an object. A dotted TOML key has
	// more than one segment.
	key []string
	// expectKey is true when the object waits for its next key.
	expectKey bool
	// flag is true when the last string in an array is an option that
	// takes a credential or a header as its next argument.
	flag bool
}

// secretScanner finds the string values to hide in a JSON or TOML
// file. It is a tolerant lexer, not a parser. It never fails, and an
// unknown byte only moves it forward, so a malformed or stale file
// still gets every string checked. It keeps its own stack instead of
// recursion, so deep nesting cannot exhaust the call stack.
type secretScanner struct {
	text  string
	toml  bool
	pos   int
	stack []scanFrame
	masks []maskRange
}

// scanSecrets returns the byte ranges of the string values to hide, in
// file order.
func scanSecrets(text string, dialect fileDialect) []maskRange {
	s := &secretScanner{text: text, toml: dialect == dialectTOML}

	// A TOML file is a table whose keys start the lines. A JSON file
	// holds one value, which the scan reads as an array element.
	s.stack = []scanFrame{{array: !s.toml, expectKey: s.toml}}

	for s.pos < len(s.text) {
		s.step()
	}

	return s.masks
}

// step reads one token and moves the scan forward by at least one byte.
func (s *secretScanner) step() {
	if s.skipComment() {
		return
	}

	top := &s.stack[len(s.stack)-1]
	c := s.text[s.pos]

	switch {
	case c == '\n':
		// A TOML statement ends at the end of its line.
		if s.toml && len(s.stack) == 1 {
			top.expectKey = true
		}

		s.pos++
	case c == ' ' || c == '\t' || c == '\r':
		s.pos++
	case c == '[' && s.toml && len(s.stack) == 1 && top.expectKey:
		s.readHeader(top)
	case c == '{' || c == '[':
		top.flag = false
		s.stack = append(s.stack, scanFrame{array: c == '[', path: s.valuePath(), expectKey: c == '{'})
		s.pos++
	case c == '}' || c == ']':
		// The root frame stays: a stray bracket must not end the scan.
		if len(s.stack) > 1 {
			s.stack = s.stack[:len(s.stack)-1]
		}

		s.pos++
	case c == ',':
		if !top.array {
			top.expectKey = true
		}

		s.pos++
	case c == ':' || c == '=':
		s.pos++
	case !top.array && top.expectKey:
		s.readKey(top)
	case c == '"' || c == '\'':
		s.readValue(top)
	default:
		top.flag = false
		s.skipScalar()
	}
}

// skipComment steps over a comment and reports whether there was one.
// TOML comments start with "#". JSON comments are not standard, but
// some clients accept "//" and "/* */", so the scan steps over them.
func (s *secretScanner) skipComment() bool {
	rest := s.text[s.pos:]

	switch {
	case s.toml && rest[0] == '#', !s.toml && strings.HasPrefix(rest, "//"):
		s.skipLine()
	case !s.toml && strings.HasPrefix(rest, "/*"):
		end := strings.Index(rest[2:], "*/")
		if end < 0 {
			s.pos = len(s.text)
		} else {
			s.pos += end + 4
		}
	default:
		return false
	}

	return true
}

// skipLine moves the scan to the newline at the end of the line. The
// newline stays, because it ends a TOML statement.
func (s *secretScanner) skipLine() {
	if end := strings.IndexByte(s.text[s.pos:], '\n'); end >= 0 {
		s.pos += end
	} else {
		s.pos = len(s.text)
	}
}

// skipScalar steps over a number, a boolean, a date, or an unknown
// byte. It stops at the next byte with a meaning to the scan.
func (s *secretScanner) skipScalar() {
	s.pos++

	for s.pos < len(s.text) && !strings.ContainsRune(" \t\r\n,:=[]{}\"'#/", rune(s.text[s.pos])) {
		s.pos++
	}
}

// readHeader reads a TOML [table] or [[table]] header. Its keys become
// the path of the values that follow. An array of tables uses the same
// path for each element, because the rules do not depend on the index.
func (s *secretScanner) readHeader(top *scanFrame) {
	s.pos++

	if s.peek() == '[' {
		s.pos++
	}

	top.path, top.key, top.expectKey = s.readKeySegments(), nil, false
	s.skipLine()
}

// readKey reads the next key of an object or table.
func (s *secretScanner) readKey(top *scanFrame) {
	segs := s.readKeySegments()
	if len(segs) == 0 {
		// Not a key: step over the byte, so the scan moves on.
		s.pos++

		return
	}

	top.key, top.expectKey = segs, false
}

// readKeySegments reads one key: a JSON string, or a TOML key with
// bare, quoted, or dotted segments.
func (s *secretScanner) readKeySegments() []string {
	var segs []string

	for {
		s.skipInlineSpace()

		seg, ok := s.readKeySegment()
		if !ok {
			return segs
		}

		segs = append(segs, seg)

		s.skipInlineSpace()

		if !s.toml || s.peek() != '.' {
			return segs
		}

		s.pos++
	}
}

// readKeySegment reads one quoted or bare key segment.
func (s *secretScanner) readKeySegment() (string, bool) {
	start := s.pos

	switch c := s.peek(); {
	case c == '"' || c == '\'':
		s.skipString()

		return unquoteKey(s.text[start:s.pos]), true
	case isBareKeyByte(c):
		for s.pos < len(s.text) && isBareKeyByte(s.text[s.pos]) {
			s.pos++
		}

		return s.text[start:s.pos], true
	default:
		return "", false
	}
}

// readValue reads a string value and records its range when the value
// must stay hidden.
func (s *secretScanner) readValue(top *scanFrame) {
	start := s.pos
	s.skipString()

	hide := hiddenPath(s.valuePath())

	if top.array {
		hide = hide || top.flag
		top.flag = takesCredential(unquoteKey(s.text[start:s.pos]))
	}

	if hide {
		s.masks = append(s.masks, maskRange{start, s.pos})
	}
}

// skipString steps over a string that starts at the scan position. A
// TOML file also has multiline strings in triple quotes. A single-line
// string without its closing quote ends at the end of the line.
func (s *secretScanner) skipString() {
	quote := s.text[s.pos]
	escapes := quote == '"' || !s.toml
	delim := string(quote)

	if s.toml && strings.HasPrefix(s.text[s.pos:], strings.Repeat(delim, 3)) {
		delim = strings.Repeat(delim, 3)
	}

	s.pos += len(delim)

	for s.pos < len(s.text) {
		c := s.text[s.pos]

		switch {
		case c == '\\' && escapes:
			s.pos += 2
		case c == '\n' && len(delim) == 1:
			return
		case strings.HasPrefix(s.text[s.pos:], delim):
			s.pos += len(delim)

			// TOML allows up to two more quotes before the closing
			// delimiter of a multiline string; they belong to the value.
			for extra := 0; len(delim) == 3 && extra < 2 && s.peek() == quote; extra++ {
				s.pos++
			}

			return
		default:
			s.pos++
		}
	}

	s.pos = min(s.pos, len(s.text))
}

// skipInlineSpace steps over spaces and tabs, but not over a newline.
func (s *secretScanner) skipInlineSpace() {
	for s.pos < len(s.text) && (s.text[s.pos] == ' ' || s.text[s.pos] == '\t') {
		s.pos++
	}
}

// peek returns the byte at the scan position, or 0 at the end.
func (s *secretScanner) peek() byte {
	if s.pos < len(s.text) {
		return s.text[s.pos]
	}

	return 0
}

// valuePath returns the key path of the next value in the innermost
// container. An array element has the path of the array.
func (s *secretScanner) valuePath() []string {
	top := s.stack[len(s.stack)-1]
	if top.array {
		return top.path
	}

	return slices.Concat(top.path, top.key)
}

// isBareKeyByte reports whether c can be part of a bare TOML key.
func isBareKeyByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// unquoteKey returns the text of a quoted key or string. An escape that
// Go does not know keeps the raw text: the name is only matched, never
// written.
func unquoteKey(quoted string) string {
	if len(quoted) < 2 || (quoted[0] != '"' && quoted[0] != '\'') {
		return quoted
	}

	if quoted[0] == '"' {
		if text, err := strconv.Unquote(quoted); err == nil {
			return text
		}
	}

	return strings.Trim(quoted, `"'`)
}

// hiddenPath reports whether the string value at a key path must stay
// hidden: its own key names a credential, or a credential table holds
// it. The tables of Seamark's own server entry stay readable, so the
// user can review that entry. A credential name there still hides.
func hiddenPath(keys []string) bool {
	if len(keys) > 0 && credentialName(keys[len(keys)-1]) {
		return true
	}

	own := false

	for i, key := range keys {
		if i > 0 && serverTables[keys[i-1]] && (key == approve.ClaudeServer || key == approve.CodexServer) {
			own = true
		}

		if !own && credentialTables[foldName(key)] {
			return true
		}
	}

	return false
}

// takesCredential reports whether an argument is an option whose next
// argument is a credential or a header, for example "--api-key" or
// "--header". The option with an "=" value is left to the shared
// redaction, which already reads that shape.
func takesCredential(arg string) bool {
	if headerFlags[arg] {
		return true
	}

	name := strings.TrimLeft(arg, "-")

	return name != arg && name != "" && !strings.ContainsAny(name, "= ") && credentialName(name)
}

// credentialName reports whether a key names a credential. The key is
// split at every byte that is not a letter or digit and at each
// lower-to-upper case change, so "X-API-Key", "OPENAI_KEY" and
// "accessToken" all match.
func credentialName(key string) bool {
	for _, seg := range keySegments(key) {
		word := strings.TrimSuffix(seg, "s")
		if credentialWords[seg] || credentialWords[word] {
			return true
		}

		for _, suffix := range credentialSuffixes {
			if strings.HasSuffix(seg, suffix) || strings.HasSuffix(word, suffix) {
				return true
			}
		}
	}

	return false
}

// keySegments splits a key into lower-case words.
func keySegments(key string) []string {
	var (
		segs []string
		cur  []rune
		prev rune
	)

	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}

	for _, r := range key {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			flush()

			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}

		prev = r
	}

	flush()

	return segs
}

// foldName lowers a table name and drops "_" and "-", so "http_headers"
// and "httpHeaders" compare equal.
func foldName(name string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
}
