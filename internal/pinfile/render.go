package pinfile

// The text of authored pins. A person writes a pin in the console, so
// the text can hold any line breaks, quotes, and "#" lines. The renderer
// here proves each form before it returns it. A note that a later edit
// cannot splice, or that reads back as other text, gets one
// double-quoted line instead.

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// noteIndent is how much deeper than its key a line of a block note
// starts. distill.RenderPins writes the same indentation.
const noteIndent = 2

// marshalIndent is the indentation of yaml.v3 for the lines of a block
// scalar under a top-level key.
const marshalIndent = "    "

// literalHeader is the header of a literal block without a final line
// break. EncodeNote writes no other header. "|" and "|+" hold a final
// line break, which the console refuses in a note, and a "|+" block
// takes the blank lines after it. "|4-" does not parse inside a
// sequence item. Such notes take double quotes.
const literalHeader = "|-"

// probeHead is the start of the one-item probe file of EncodeNote.
// probeCol is the column of its note key. It is 4, as in the layout of
// distill.RenderPins.
const (
	probeHead = "pin:\n  - rule: probe\n"
	probeCol  = 4
)

// EncodeNote returns the YAML text of a note value. lines[0] is the text
// after "note: " on the key line. Each later line is one line of a
// literal block without its indentation; an empty string is an empty
// line. The caller indents each nonempty later line two spaces deeper
// than the note key (noteBlock).
//
// A note with a line break can get the literal form "|-". yaml.v3
// chooses that form, as for distill.RenderPins. EncodeNote then builds
// a one-item probe file with the form. It accepts the form only when the
// probe passes the editing gate and decodes to the note byte for byte.
// The probe entry must also own every probe line. For example, the probe
// refuses a line that starts with a tab, because the gate then blocks
// every later edit of the file. Every other note gets one line from
// yamledit.BlockScalar, with escapes in double quotes.
//
// The probe is not the file, so the caller still verifies the decoded
// candidate.
func EncodeNote(note string) ([]string, error) {
	if strings.Contains(note, "\n") {
		if lines, ok := literalNote(note); ok {
			return lines, nil
		}
	}

	text, err := yamledit.BlockScalar(note)
	if err != nil {
		return nil, fail(ErrUnsupported, "the note has no safe YAML text")
	}

	return []string{text}, nil
}

// literalNote returns the note in the "|-" form, or false. yaml.v3
// chooses the form. It quotes text that a literal block cannot hold,
// such as a line with trailing spaces. The probe then proves the form.
func literalNote(note string) ([]string, bool) {
	out, err := yaml.Marshal(map[string]string{"note": note})
	if err != nil {
		return nil, false
	}

	raw, _ := yamledit.SplitLines(string(out))
	if len(raw) < 2 || raw[0] != "note: "+literalHeader {
		return nil, false
	}

	lines := []string{literalHeader}

	for _, l := range raw[1:] {
		body, ok := strings.CutPrefix(l, marshalIndent)
		if !ok && l != "" {
			return nil, false
		}

		lines = append(lines, body)
	}

	probe := append([]string{strings.Repeat(" ", probeCol) + "note: " + lines[0]}, noteBlock(probeCol, lines)...)
	a := analyze([]byte(probeHead + strings.Join(probe, "\n") + "\n"))

	if a.gate != "" || len(a.entries) != 1 || a.entries[0].Note != note {
		return nil, false
	}

	// YAML reads a line of only a tab or a Unicode space as note text. The
	// line rules of the editor read it as blank. As the last line, the
	// entry would not own it, so a later edit leaves it behind or fails.
	// The probe entry must therefore own every probe line.
	if a.entries[0].EndLine != len(a.lines) {
		return nil, false
	}

	return lines, true
}

// noteBlock returns the later lines of the encoded note enc, with the
// indentation for a note key at column col. Each nonempty line starts
// two spaces deeper than the key. An empty line stays empty, so an edit
// adds no line of only spaces. RenderPin pads empty lines for
// parseBlock, as distill.RenderPins does, so a created note can hold
// such lines.
func noteBlock(col int, enc []string) []string {
	pad := strings.Repeat(" ", col+noteIndent)
	out := make([]string, 0, len(enc))

	for _, l := range enc[1:] {
		if l == "" {
			out = append(out, "")

			continue
		}

		out = append(out, pad+l)
	}

	return out
}

// RenderPin renders one pin as an entry for PlanAppend. Each line starts
// with two spaces. No provenance comment comes first, because no
// proposal stands behind a pin that a person writes. The region keys
// follow reviews.PinForRegions, as in distill.RenderPins. RenderPin
// refuses the region sets that a rewrite refuses (validRegions), so a
// create and an edit of one pin follow one region rule.
//
// The pin gets its own encoders on purpose. distill.RenderPins marshals
// with yaml.v3. Its bytes stay as they are, because the CLI apply
// goldens pin them. But yaml.v3 can write a note as a "|4-" or "|+"
// block, which later edits refuse or which loses line breaks. RenderPin
// writes the values with the yamledit encoders and the note with
// EncodeNote, which proves its form first.
//
// RenderPin reads its block back and refuses a block that does not
// decode to exactly the pin.
func RenderPin(p reviews.PinRule) (string, error) {
	set := draftRegions(p)
	if err := validRegions(set); err != nil {
		return "", err
	}

	pin := reviews.PinForRegions(p.Rule, p.Note, set)

	rule, err := yamledit.BlockScalar(pin.Rule)
	if err != nil {
		return "", noText("the rule")
	}

	region, err := yamledit.BlockScalar(pin.Region)
	if err != nil {
		return "", noText("the region")
	}

	lines := []string{"- rule: " + rule, "  region: " + region}

	if len(pin.Regions) > 0 {
		set, err := yamledit.FlowList(pin.Regions)
		if err != nil {
			return "", noText("the regions")
		}

		lines = append(lines, "  regions: "+set)
	}

	note, err := EncodeNote(pin.Note)
	if err != nil {
		return "", err
	}

	lines = append(lines, "  note: "+note[0])
	lines = append(lines, noteBlock(len("  "), note)...)

	// parseBlock reads only lines with the render indentation. An empty
	// line of a block note therefore holds those two spaces too, as in
	// the block of distill.RenderPins.
	var b strings.Builder

	for _, l := range lines {
		b.WriteString(renderIndent + l + "\n")
	}

	block := b.String()

	add, err := parseBlock(block)
	if err != nil || len(add.pins) != 1 || !reflect.DeepEqual(add.pins[0], pin) {
		return "", fail(ErrUnverified, "the rendered pin does not read back as the pin")
	}

	return block, nil
}

// draftRegions returns the region set of p in the order of the draft:
// Region, then Regions. A Region of "" or "*" adds no region. A Region
// equal to the first item of Regions counts once, because
// reviews.PinForRegions writes that layout. The set is not normalized,
// so validRegions refuses a repeated or empty region, as for a rewrite.
func draftRegions(p reviews.PinRule) []string {
	if p.Region == "" || p.Region == "*" || (len(p.Regions) > 0 && p.Regions[0] == p.Region) {
		return p.Regions
	}

	return append([]string{p.Region}, p.Regions...)
}

// noText refuses a value that no encoder can write as YAML text that
// reads back the same, for example text that is not UTF-8. what names
// the value, such as "the rule" or "a region".
func noText(what string) error {
	return fail(ErrUnsupported, what+" has no safe single-line YAML text")
}
