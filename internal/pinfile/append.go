package pinfile

// The append. An apply adds new pin entries at the head of the pin list,
// right after the pin: line, where the CLI always put them. The new
// lines go in as one run, so every existing byte stays as the user wrote
// it. The planner then proves the candidate from its bytes, as it does
// for a retarget or a prune, before a caller can write it.

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// newFileHeader is the text that the first apply of a workspace starts
// the pin file with. It tells a reader which command reads the file.
const newFileHeader = "# Review-lesson tuning (see `seamark lessons --list`).\n"

// renderIndent is the indentation of every line that distill.RenderPins
// writes. The planner moves the lines to the indentation of the list.
const renderIndent = "  "

// refusal is an append that the planner refuses. Its text is the plain
// reason with the manual route, without the sentinel text. The CLI then
// prints the same sentence as before the append moved here. It matches
// its sentinel, so a caller can test the kind with errors.Is.
type refusal struct {
	text string
	kind error
}

// Error returns the reason.
func (r *refusal) Error() string {
	return r.text
}

// Unwrap returns the sentinel: ErrUnsupported or ErrUnverified.
func (r *refusal) Unwrap() error {
	return r.kind
}

// addition is the rendered block of the new entries.
type addition struct {
	// lines are the block lines, each indented by two spaces.
	lines []string
	// items are the decoded entries with every key. pins are the entries
	// as the lessons reader delivers them.
	items []map[string]any
	pins  []reviews.PinRule
}

// PlanAppend computes the candidate file that adds the entries of block
// at the head of the pin list of d. block is the text of
// distill.RenderPins: entries with their provenance comments, each line
// indented by two spaces. PlanAppend is pure and writes nothing.
//
// The new lines go right after the pin: line, at the indentation of the
// list items. Without a pin section, a new section goes at the end of
// the document content. A file that d observed as absent starts with a
// header comment. A CRLF file keeps its CRLF line breaks.
//
// It refuses a pin section that is not a plain block list. It also
// refuses a file that the editor cannot parse or that the reader
// rejects. It returns the candidate only after verifyAppend proves it.
// A link or a file that this user cannot write does not stop it;
// PrepareAppend refuses the write.
func PlanAppend(d *Document, block string) ([]byte, error) {
	if d == nil {
		return nil, errors.New("pinfile: no document")
	}

	add, err := parseBlock(block)
	if err != nil {
		return nil, err
	}

	start := d.Bytes
	if !d.Guard.Exists && len(start) == 0 {
		start = []byte(newFileHeader)
	}

	a := analyze(start)

	// The structural gate puts the reason of a failed parse first, so
	// a.gate names it when a.doc is nil.
	switch {
	case a.doc == nil:
		return nil, cannotTake(a.gate)
	case a.readerErr != nil:
		return nil, cannotTake("the lessons reader rejects the file: " + a.readerErr.Error())
	}

	var (
		pos, indent int
		ins         []string
	)

	if a.pinKey == nil {
		pos, indent, ins = a.newSection()
	} else if pos, indent, err = a.listHead(); err != nil {
		return nil, err
	}

	ins = append(ins, add.shifted(indent)...)

	// A new section at the end of the text ends with a line break, as
	// every line of the block does.
	finalEOL := a.finalEOL || (a.pinKey == nil && pos == len(a.lines))
	cand := yamledit.JoinLines(slices.Insert(slices.Clone(a.lines), pos, ins...), finalEOL)

	if a.crlf {
		cand = bytes.ReplaceAll(cand, []byte("\n"), []byte("\r\n"))
	}

	if err := a.verifyAppend(start, cand, add, pos, ins, finalEOL); err != nil {
		return nil, err
	}

	return cand, nil
}

// cannotTake refuses a file that the planner cannot extend with proof.
func cannotTake(reason string) error {
	return &refusal{
		text: "lessons.yaml cannot take the new pins safely: " + reason + "; add the entries by hand",
		kind: ErrUnsupported,
	}
}

// unverified refuses a candidate that fails a check of verifyAppend.
func unverified(reason string) error {
	return &refusal{text: "refusing to write: " + reason, kind: ErrUnverified}
}

// parseBlock reads the rendered block as the pin list of a file of its
// own. The decoded entries are the values that the candidate must show
// at the head of its list.
func parseBlock(block string) (*addition, error) {
	lines, _ := yamledit.SplitLines(block)
	if len(lines) == 0 {
		return nil, errors.New("pinfile: no pin entries to add")
	}

	for _, l := range lines {
		if !strings.HasPrefix(l, renderIndent) {
			return nil, errors.New("pinfile: a line of the pin block is not indented by two spaces")
		}
	}

	src := []byte("pin:\n" + strings.Join(lines, "\n") + "\n")
	x := analyze(src)

	if x.doc == nil || x.readerErr != nil || x.seq == nil {
		return nil, errors.New("pinfile: the pin block does not load as a pin list")
	}

	items, err := x.itemValues()
	if err != nil {
		return nil, err
	}

	cfg, err := reviews.ParseConfig(src)
	if err != nil {
		return nil, err
	}

	return &addition{lines: lines, items: items, pins: cfg.Pin}, nil
}

// shifted returns the block lines with indent spaces in place of their
// two-space indentation. A uniform shift keeps the YAML meaning of every
// line, also inside a block scalar; verifyAppend proves the result.
func (b *addition) shifted(indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(b.lines))

	for i, l := range b.lines {
		out[i] = pad + strings.TrimPrefix(l, renderIndent)
	}

	return out
}

// listHead returns the place of the new entries in an existing pin
// section. The place is the 0-based index of the line after the pin:
// line, and the indentation is the dash column of the list. An empty
// section takes items two spaces deeper than its key. A flow list, a
// value, a tag, or an anchor has no such place, so the planner refuses
// them.
func (a *analysis) listHead() (pos, indent int, err error) {
	_, v, _ := yamledit.Lookup(a.root, "pin")
	plain := v.Style&yaml.TaggedStyle == 0 && v.Anchor == ""

	switch {
	case v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0:
		return 0, 0, &refusal{
			text: "lessons.yaml has a flow-style pin section; add the entries by hand",
			kind: ErrUnsupported,
		}
	case plain && yamledit.IsNull(v) && v.Value == "":
		// The column is 1-based, so Column + 1 is two spaces deeper.
		return a.pinKey.Line, a.pinKey.Column + 1, nil
	case plain && v.Kind == yaml.SequenceNode:
		// yaml.v3 puts a sequence without properties at its first dash.
		return a.pinKey.Line, v.Column - 1, nil
	}

	return 0, 0, &refusal{
		text: "lessons.yaml has a pin section that is not a plain block list; add the entries by hand",
		kind: ErrUnsupported,
	}
}

// newSection returns the place, the item indentation, and the first
// lines of a new pin section. It goes at the end of the document
// content, after a blank line, at the column of the top-level keys. Text
// after a "..." end marker is not read, so the section goes before it.
func (a *analysis) newSection() (pos, indent int, lines []string) {
	col := 0
	if a.root != nil && len(a.root.Content) > 0 {
		col = a.root.Content[0].Column - 1
	}

	pos = a.doc.Limit - 1

	if pos > 0 && !yamledit.IsBlank(a.lines[pos-1]) {
		lines = append(lines, "")
	}

	return pos, col + len(renderIndent), append(lines, strings.Repeat(" ", col)+"pin:")
}

// verifyAppend proves the candidate cand of an append on start, the
// reviewed text, from its bytes:
//
//  1. the editor parses it, and the lessons reader loads it;
//  2. it keeps the line breaks of the reviewed text;
//  3. every top-level key and value except the pin list is equal and in
//     place;
//  4. the pin items are the new entries and then every reviewed item,
//     in order, with duplicates and unknown keys;
//  5. the reader delivers the new pins and then every reviewed pin;
//  6. the reviewed lines stay byte-identical and in order around the one
//     run of new lines ins at the 0-based index pos.
func (a *analysis) verifyAppend(start, cand []byte, add *addition, pos int, ins []string, finalEOL bool) error {
	b := analyze(cand)

	switch {
	case b.doc == nil:
		return unverified("edited lessons.yaml would not parse: " + b.gate)
	case b.readerErr != nil:
		return unverified("edited lessons.yaml would not parse: " + b.readerErr.Error())
	case b.crlf != a.crlf:
		return unverified("the edit would change the line breaks of lessons.yaml")
	case !sameSettings(a.root, b.root):
		return unverified("the edit would change a setting outside the pin list")
	}

	reviewed, err := a.itemValues()
	if err != nil {
		return err
	}

	got, err := b.itemValues()
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(slices.Concat(add.items, reviewed), got) {
		return unverified("the pin list would differ from the new entries followed by the earlier entries")
	}

	before, err := reviews.ParseConfig(start)
	if err != nil {
		return err
	}

	after, err := reviews.ParseConfig(cand)
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(slices.Concat(add.pins, before.Pin), after.Pin) {
		return unverified("the delivered pins would differ from the new pins followed by the earlier pins")
	}

	if !slices.Equal(slices.Insert(slices.Clone(a.lines), pos, ins...), b.lines) || b.finalEOL != finalEOL {
		return unverified("the edit would change a line outside the new entries")
	}

	return nil
}

// sameSettings reports whether both top-level mappings hold the same
// keys with the same values in the same order, apart from the pin key.
// A nil mapping holds no keys, so a new file compares as empty.
func sameSettings(x, y *yaml.Node) bool {
	return slices.EqualFunc(otherPairs(x), otherPairs(y), func(p, q []*yaml.Node) bool {
		return yamledit.NodeEqual(p[0], q[0]) && yamledit.NodeEqual(p[1], q[1])
	})
}

// otherPairs returns the key and value nodes of each pair of m, except
// the pin pair.
func otherPairs(m *yaml.Node) [][]*yaml.Node {
	if m == nil {
		return nil
	}

	var out [][]*yaml.Node

	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != "pin" {
			out = append(out, m.Content[i:i+2])
		}
	}

	return out
}
