package pinfile

// The analysis of one version of the file bytes. Parse uses it for the
// listing and the gate. Plan uses it again on the reviewed bytes and on
// each candidate, so a plan never trusts a field that a caller can change.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// pinFields are the keys that reviews.PinRule reads. Every other key in
// an item is unknown to the readers. TestPinFieldsAreTheKeysOfPinRule
// compares the list with the yaml tags of PinRule, so a new field fails
// the test until it joins the list.
var pinFields = []string{"rule", "region", "regions", "note"}

// provenanceRe finds the proposal ID at the end of a provenance comment,
// as distill.RenderPins writes it: "(seamark lessons --distill, p12)".
var provenanceRe = regexp.MustCompile(`, p(\d+)\)$`)

// analysis is one read of the file bytes.
type analysis struct {
	// lines and finalEOL are the LF text of the file. crlf is true when
	// the file uses CRLF line breaks; an append writes them back.
	lines    []string
	finalEOL bool
	crlf     bool
	// doc is the parsed document for edits. It is nil when the
	// structural gate of yamledit refuses the text.
	doc *yamledit.Doc
	// root is the top-level mapping of the first document, from doc or
	// from a plain decode; nil when there is no mapping.
	root *yaml.Node
	// pinKey and seq are the pin key and its sequence value. seq is nil
	// when the section is absent, null, or not a sequence.
	pinKey, seq *yaml.Node
	// stop is the first line after the pin section; 0 without doc.
	stop int
	// items holds every sequence item, empty items included.
	items []item
	// entries are the listed pins, in file order.
	entries []Entry

	decodeErr, readerErr error
	// listErr reports a listing that differs from the delivered pins.
	listErr error
	errLine int
	// gate is the structural reason that blocks every edit, or empty.
	gate string
}

// item is one pin sequence item with its lines.
type item struct {
	node *yaml.Node
	// first and last are the 1-based own lines. exact is true when they
	// come from yamledit.ItemSpan, so a splice can use them.
	first, last int
	exact       bool
}

// analyze reads raw as the pin file. It never fails: each problem goes
// into a field, so the caller decides what the problem blocks.
func analyze(raw []byte) *analysis {
	a := &analysis{}

	data, crlf, breaks := yamledit.NormalizeBreaks(raw)
	a.lines, a.finalEOL = yamledit.SplitLines(string(data))
	a.crlf = crlf

	// The readers parse the raw bytes, so the reader error is about them.
	cfg, err := reviews.ParseConfig(raw)
	if err != nil {
		a.readerErr = err
		a.errLine = yamledit.ErrorLine(err)
	}

	doc, issue := yamledit.Parse(data)
	if issue == nil {
		a.doc, a.root = doc, doc.Root
	} else {
		a.root, a.decodeErr = firstRoot(data)
	}

	// The gate names the corrected line of a syntax error.
	if a.decodeErr != nil {
		a.errLine = issue.Line
		if a.errLine == 0 {
			a.errLine = yamledit.ErrorLine(a.decodeErr)
		}
	}

	if a.root != nil {
		a.list()
	}

	a.gate = a.structuralGate(breaks, issue, crlf)
	a.listErr = a.compareListing(cfg)

	// A listing that the editor cannot trust must never guide a splice.
	// Each known shape that hides pins from the listing also fails the
	// gate, so this line only guards against a future gap.
	if a.listErr != nil && a.gate == "" {
		a.gate = a.listErr.Error()
	}

	return a
}

// compareListing compares the keys of the listing with the keys of the
// pins that the reader delivers, as two multisets. They differ when an
// alias pin value or a merge key gives the reader pins that the listing
// does not see. The listing then cannot prove that a pin is absent, so
// the difference is an error (D-5).
//
// A null item reads as a zero pin, and the listing skips it. A pin with
// the zero key therefore takes no part in the comparison on either side.
func (a *analysis) compareListing(cfg *reviews.Config) error {
	if cfg == nil || a.decodeErr != nil {
		return nil
	}

	zero := reviews.NewPinKey("", "", nil)
	count := map[reviews.PinKey]int{}

	for _, p := range cfg.Pin {
		count[reviews.NewPinKey(p.Rule, p.Region, p.Regions)]++
	}

	for _, e := range a.entries {
		count[e.Key]--
	}

	for key, n := range count {
		if n == 0 || key == zero {
			continue
		}

		if a.gate != "" {
			return fmt.Errorf("the listing differs from the delivered pins: %s", a.gate)
		}

		return errors.New("the listing differs from the delivered pins")
	}

	return nil
}

// firstRoot decodes the first YAML document and returns its top-level
// mapping. The readers read only the first document, so the listing
// does too.
func firstRoot(data []byte) (*yaml.Node, error) {
	var node yaml.Node

	err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&node)

	switch {
	case errors.Is(err, io.EOF):
		return nil, nil
	case err != nil:
		return nil, err
	case len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode:
		return nil, nil
	}

	return node.Content[0], nil
}

// list finds the pin items and decodes each mapping item into an Entry.
func (a *analysis) list() {
	key, value, idx := yamledit.Lookup(a.root, "pin")
	if key == nil {
		return
	}

	a.pinKey = key

	if value.Kind != yaml.SequenceNode {
		return
	}

	a.seq = value

	if a.doc != nil {
		a.stop = a.doc.SectionEnd(idx) + 1
	}

	for i, n := range value.Content {
		it := item{node: n}
		it.first, it.last, it.exact = a.span(i)
		a.items = append(a.items, it)
	}

	for i := range a.items {
		if m := mapping(a.items[i].node); m != nil {
			a.entries = append(a.entries, a.entry(i, m))
		}
	}
}

// mapping returns the mapping that an item reads as, through an alias,
// or nil for an empty item or an item that is not a mapping.
func mapping(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}

	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}

	return n
}

// span returns the own lines of item i. Without the parsed document, or
// in a flow sequence, the lines of the decoded values stand in.
func (a *analysis) span(i int) (first, last int, exact bool) {
	n := a.seq.Content[i]

	if a.doc != nil {
		if first, last, ok := a.doc.ItemSpan(a.seq, i, a.stop); ok {
			return first, last, true
		}
	}

	first, last = n.Line, yamledit.LastLine(n)
	last = min(max(last, first), len(a.lines))

	return min(first, last), last, false
}

// entry decodes item i, which reads as the mapping m.
func (a *analysis) entry(i int, m *yaml.Node) Entry {
	it := a.items[i]

	var pin reviews.PinRule

	// A type error keeps the fields that decode, so the listing still
	// shows them. The reader error explains the rest.
	decodeErr := it.node.Decode(&pin)

	e := Entry{
		Index: i, Line: it.first, EndLine: it.last,
		Rule: pin.Rule, Region: pin.Region, Regions: pin.Regions, Note: pin.Note,
		Key:           reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions),
		Unknown:       unknownKeys(m),
		ProvenanceIDs: a.provenanceIDs(i),
	}

	if it.first >= 1 && it.last <= len(a.lines) {
		e.Raw = strings.Join(a.lines[it.first-1:it.last], "\n")
	}

	e.Block = a.entryBlock(it, decodeErr)

	return e
}

// unknownKeys lists the keys of m that no reader uses. A merge key is
// not listed: it is not a field, and the gate blocks it.
func unknownKeys(m *yaml.Node) []string {
	var out []string

	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]

		if k.Tag == "!!merge" || slices.Contains(pinFields, k.Value) {
			continue
		}

		out = append(out, k.Value)
	}

	return out
}

// comment reports whether line n holds only a comment. The parsed
// document knows the lines of multi-line values; the plain test does not.
func (a *analysis) comment(n int) bool {
	if a.doc != nil {
		return a.doc.Comment(n)
	}

	return yamledit.IsComment(a.lines[n-1])
}

// provenanceLines returns the provenance comment lines directly above
// item i with their proposal IDs, from the top down. The run stops at
// the first line that is not a provenance comment. It never reaches
// into the previous item, because a comment deeper than the dash after
// an item is that item's own line.
func (a *analysis) provenanceLines(i int) (lines []int, ids []int64) {
	floor := a.pinKey.Line
	if i > 0 {
		floor = a.items[i-1].last
	}

	for n := a.items[i].first - 1; n > floor; n-- {
		if !a.comment(n) {
			break
		}

		id, ok := provenanceID(a.lines[n-1])
		if !ok {
			break
		}

		lines = append([]int{n}, lines...)
		ids = append([]int64{id}, ids...)
	}

	return lines, ids
}

// provenanceIDs returns the IDs of the provenance comments above item i.
func (a *analysis) provenanceIDs(i int) []int64 {
	_, ids := a.provenanceLines(i)

	return ids
}

// provenanceID reads the proposal ID of a provenance comment line. The
// whole digit run must be the canonical decimal ID, so "p12" never reads
// as p1 and "p012" reads as no ID.
func provenanceID(line string) (int64, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "#") || !strings.Contains(text, reviews.ProvenanceMarker) {
		return 0, false
	}

	m := provenanceRe.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}

	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || strconv.FormatInt(id, 10) != m[1] {
		return 0, false
	}

	return id, true
}

// issueText formats a block reason with its line, as yamledit does.
func issueText(reason string, line int) string {
	return (&yamledit.Issue{Reason: reason, Line: line}).Error()
}

// structuralGate returns the first reason from the bytes alone that
// makes every edit unsafe, or an empty string. A link and a read-only
// mode are not in it: they block the write, not the plan.
func (a *analysis) structuralGate(breaks, issue *yamledit.Issue, crlf bool) string {
	switch {
	case breaks != nil:
		return breaks.Error()
	case issue != nil:
		return issue.Error()
	case a.readerErr != nil:
		return issueText("the lessons reader rejects the file", a.errLine)
	case crlf:
		return "the file uses CRLF line breaks"
	}

	if reason := a.pinGate(); reason != "" {
		return reason
	}

	if n := tabLine(a.lines); n > 0 {
		return issueText("the file indents a line with a tab", n)
	}

	return ""
}

// pinGate applies the rules for the pin section. Each refused shape
// either has no line per item to splice, or makes one edit change
// another place: an alias repeats an item, and a tag changes a type.
func (a *analysis) pinGate() string {
	if a.pinKey == nil {
		return ""
	}

	_, value, _ := yamledit.Lookup(a.root, "pin")

	if n := taggedNode(a.pinKey); n != nil || a.pinKey.Anchor != "" {
		return issueText("the pin key carries a tag or an anchor", a.pinKey.Line)
	}

	if issue := yamledit.SubtreeIssue("pin", value); issue != nil {
		return issue.Error()
	}

	if n := taggedNode(value); n != nil {
		return issueText(fmt.Sprintf("the pin section uses a tag (%s)", n.Tag), n.Line)
	}

	if yamledit.IsNull(value) {
		return ""
	}

	if value.Kind != yaml.SequenceNode || value.Style&yaml.FlowStyle != 0 {
		return issueText("the pin section is not a block list", value.Line)
	}

	for _, it := range a.items {
		switch {
		case yamledit.IsNull(it.node):
			return issueText("the pin list holds an empty entry", it.node.Line)
		case !it.exact:
			return issueText("the editor cannot find the lines of a pin entry", it.node.Line)
		}
	}

	return ""
}

// taggedNode returns the first node in the subtree of n with an explicit
// tag, or nil.
func taggedNode(n *yaml.Node) *yaml.Node {
	if n.Style&yaml.TaggedStyle != 0 {
		return n
	}

	for _, c := range n.Content {
		if t := taggedNode(c); t != nil {
			return t
		}
	}

	return nil
}

// tabLine returns the first 1-based line whose indentation holds a tab,
// or 0. The splice rules measure indentation in spaces. yaml.v3 already
// refuses a tab in the indentation of a key or an item, so this check
// mostly finds a tab at the start of a line of a multi-line value. The
// edit is then refused although it could be safe: such text is rare, and
// the user can still edit the file by hand. A blank line is skipped,
// because its white space has no meaning.
func tabLine(lines []string) int {
	for i, l := range lines {
		if !yamledit.IsBlank(l) && yamledit.IndentOf(l) != yamledit.LeadingSpaces(l) {
			return i + 1
		}
	}

	return 0
}

// entryBlock returns the reason one entry cannot be edited, or empty.
// The file-level gate covers the shapes that it detects; this check adds
// the shapes of a single entry.
func (a *analysis) entryBlock(it item, decodeErr error) string {
	n := it.node

	switch {
	case decodeErr != nil:
		return issueText("the entry does not load as a pin", n.Line)
	case n.Kind == yaml.AliasNode:
		return issueText("the entry is an alias", n.Line)
	case n.Style&yaml.FlowStyle != 0:
		return issueText("the entry uses flow style { }", n.Line)
	case a.doc == nil:
		// The file gate already blocks every edit and names the reason.
		return ""
	case !it.exact:
		return issueText("the editor cannot find the lines of the entry", n.Line)
	}

	stop := it.last + 1

	for _, name := range []string{"rule", "region"} {
		s, ok := a.doc.KeySpan(n, name, stop)

		switch {
		case !ok:
		case s.Value.Kind != yaml.ScalarNode:
			return issueText(fmt.Sprintf("the %s value is not text", name), s.First)
		case !s.Inline:
			return issueText(fmt.Sprintf("the %s value spans several lines", name), s.First)
		}
	}

	if s, ok := a.doc.KeySpan(n, "regions", stop); ok {
		return a.regionsBlock(s)
	}

	return ""
}

// regionsBlock checks the regions value of an entry. A flow list must
// sit on its key line. Each block list item must be one text token on
// its dash line, so a retarget can replace it in place.
func (a *analysis) regionsBlock(s yamledit.Span) string {
	v := s.Value

	switch {
	case yamledit.IsNull(v) || (v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0):
		if !s.Inline {
			return issueText("the regions list spans several lines", s.First)
		}
	case v.Kind == yaml.SequenceNode:
		for j, it := range v.Content {
			first, last, ok := a.doc.ItemSpan(v, j, s.Last+1)
			if !ok || first != last || it.Kind != yaml.ScalarNode || yamledit.IsNull(it) || it.Line != first {
				return issueText("a regions item is not one text value on its own line", it.Line)
			}

			if _, _, ok := a.token(it); !ok {
				return issueText("a regions item is not one text value on its own line", it.Line)
			}
		}
	default:
		return issueText("the regions value is not a list", s.First)
	}

	return ""
}

// token returns the byte span of the scalar n on its line. The gate
// refuses tags and anchors in the pin section, so the span starts at
// the node column.
func (a *analysis) token(n *yaml.Node) (start, end int, ok bool) {
	line := a.lines[n.Line-1]
	start = a.doc.Offset(n.Line, n.Column)

	end, ok = yamledit.TokenEnd(line, yamledit.SkipProperties(line, start), n.Style)

	return start, end, ok
}
