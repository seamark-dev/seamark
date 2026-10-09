package tui

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file renders Lessons › Pins in the 1a layout of Proposals. It
// draws the filter line, an optional state line, the list box and the
// detail box. Each value from the pin file, the ledger or a model gets
// sanitized here, at the leaf (oneLine, multiLine, errText). The file is
// user text and can hold escape bytes. The box frames and the filter
// line format come from listpane.go, so Pins and Proposals look alike.

// Column widths of the Pins list. The state column holds a label with
// one attention word, for example "! p12 unknown key". So the list never
// clips the relationship word at the reference size. A wider list gives
// the state column up to pinStateMaxW cells while the rule keeps
// pinRuleRoomW, so a label with two words shows whole. A label that
// still does not fit counts its other words (pinState.fit).
const (
	pinRegionW   = 12
	pinStateW    = 17
	pinStateMaxW = 30
	pinRuleRoomW = 24
)

// pinState is the relationship and attention label of one row: a glyph
// and words in one tone. The words carry the meaning without color, and
// the glyph keeps the tone visible in a monochrome terminal.
type pinState struct {
	glyph string
	style lipgloss.Style
	// lead holds the relationship words. flags holds the attention words,
	// the most urgent first, and sep joins them to lead.
	lead  string
	sep   string
	flags []string
}

// text returns every word of the label.
func (s pinState) text() string {
	if len(s.flags) == 0 {
		return s.lead
	}

	return s.lead + s.sep + strings.Join(s.flags, ", ")
}

// render returns the styled label with every word.
func (s pinState) render() string {
	return s.style.Render(s.glyph + " " + s.text())
}

// fit returns the styled label in at most w cells. A label that does not
// fit keeps its relationship words and its most urgent attention word,
// and counts the other words, for example "! p9 blocked +2". Cutting the
// words would hide "blocked", which tells why w and - are off. The
// detail status line shows every word.
func (s pinState) fit(w int, ell string) string {
	full := s.glyph + " " + s.text()
	if width(full) <= w || len(s.flags) == 0 {
		return s.style.Render(truncate(full, w, ell))
	}

	short := s.glyph + " " + s.lead + s.sep + s.flags[0]
	if more := len(s.flags) - 1; more > 0 {
		short += " +" + itoa(more)
	}

	if width(short) <= w {
		return s.style.Render(short)
	}

	if count := s.glyph + " " + s.lead + " +" + itoa(len(s.flags)); width(count) <= w {
		return s.style.Render(count)
	}

	return s.style.Render(truncate(full, w, ell))
}

// pinToneState builds a label in the glyph and the style of a tone.
func pinToneState(t *Theme, tone Tone, lead string) pinState {
	return pinState{glyph: t.ToneGlyph(tone), style: t.ToneStyle(tone), lead: lead}
}

// pinLabel returns the label of an installed pin: its relationship to
// the ledger (D-4), then the attention words of the entry. A linked pin
// leads with its proposal ID, because that ID is what the user looks up.
// "blocked" comes first, because it tells why w and - are off.
func (p *pinsView) pinLabel(in *maintain.Inspection, pin *maintain.Pin) pinState {
	t := p.st.theme

	if !pinLinksKnown(in) {
		return pinToneState(t, ToneUnknown, "link unknown")
	}

	var flags []string

	if pin.Entry.Block != "" {
		flags = append(flags, "blocked")
	}

	if len(pin.Entry.Unknown) > 0 {
		flags = append(flags, "unknown key")
	}

	if pin.Link.NoteChanged {
		flags = append(flags, "edited")
	}

	l := pin.Link

	var (
		tone Tone
		lead string
	)

	switch {
	case l.Link == pinfile.LinkLinked && len(l.Applied) != 1:
		// Classify never gives this; the guard keeps a malformed value
		// from reading as linked.
		return pinToneState(t, ToneUnknown, "link unknown")
	case l.Link == pinfile.LinkLinked:
		if len(flags) == 0 {
			return pinToneState(t, ToneSuccess, propID(l.Applied[0].ID)+" linked")
		}

		s := pinToneState(t, ToneAttention, propID(l.Applied[0].ID))
		s.sep, s.flags = " ", flags

		return s
	case l.Link == pinfile.LinkNoProposal:
		tone, lead = ToneUnknown, "no proposal"
	case l.Link == pinfile.LinkPendingMatch:
		tone, lead = ToneAttention, "pending"

		if len(l.Pending) == 1 {
			lead += " " + propID(l.Pending[0].ID)
		}
	case l.Link == pinfile.LinkAmbiguousFile:
		tone, lead = ToneAttention, "duplicate pin"
	case l.Link == pinfile.LinkAmbiguousLedger:
		tone, lead = ToneAttention, "ambiguous"
	case l.Link == pinfile.LinkAmbiguousFields:
		tone, lead = ToneAttention, "ambiguous row"
	default:
		return pinToneState(t, ToneUnknown, "link unknown")
	}

	if len(flags) > 0 {
		tone = ToneAttention
	}

	s := pinToneState(t, tone, lead)
	s.sep, s.flags = ", ", flags

	return s
}

// propState returns the label of a history row. A not-present row is a
// mismatch the user should see; a superseded row is plain history. A
// row whose region fields disagree reads "ambiguous row", because the
// pin that its apply wrote can be live under another key.
func (p *pinsView) propState(pr domain.Proposal) pinState {
	t := p.st.theme

	switch {
	case p.filter == pinSuperseded:
		return pinState{glyph: t.Glyphs.Hollow, style: t.Muted, lead: "superseded"}
	case pinRowAmbiguous(p.inspection(), pr):
		return pinToneState(t, ToneAttention, "ambiguous row")
	}

	return pinToneState(t, ToneAttention, "not present")
}

// pinRowAmbiguous reports whether the inspection reads the applied row
// pr as ambiguous, which pinfile.Classify does for a row whose stored
// region fields disagree.
func pinRowAmbiguous(in *maintain.Inspection, pr domain.Proposal) bool {
	return in != nil && in.Links != nil && in.Links.ByProposal[pr.ID] == pinfile.LinkAmbiguousFields
}

// view renders the filter line, the state line, a blank line and the
// boxes. The shell pads the last line, which keeps the blank row above
// the footer.
func (p *pinsView) view(w, h int) string {
	head := p.filterBar(w)
	if line := p.stateLine(w); line != "" {
		head = append(head, line)
	}

	boxH := max(4, h-len(head)-2)

	return strings.Join(head, "\n") + "\n\n" + p.body(w, boxH)
}

// filterBar renders " show [installed 6] not present 1  superseded 2
// region all     text any     1 selected". The active filter has
// brackets as well as the accent, so it stays visible without color. A
// count that the inspection cannot know shows "?", never zero. The line
// takes two rows when one row is too narrow.
func (p *pinsView) filterBar(w int) []string {
	t := p.st.theme
	in := p.inspection()

	states := filterChips(t, pinFilterCount, p.filter, func(f pinFilter) string {
		if in == nil {
			return f.label()
		}

		return f.label() + " " + pinCount(in, f)
	})

	rest := t.Faint.Render("region") + " " + p.fieldView(t, filterRegion, p.region, "all") + "     " +
		t.Faint.Render("text") + " " + p.fieldView(t, filterText, p.text, "any")

	if n := len(p.selected); n > 0 && p.filter == pinInstalled {
		rest += "     " + t.Marked.Render(itoa(n)+" selected")
	}

	return filterLines(t, t.Faint.Render("show")+" "+states, rest, w)
}

// pinCount renders the row count of filter f, or "?" when the
// inspection cannot know it.
func pinCount(in *maintain.Inspection, f pinFilter) string {
	switch f {
	case pinNotPresent:
		if in.Links == nil || in.Links.NotPresentErr != nil {
			return "?"
		}

		return itoa(len(in.Links.NotPresent))
	case pinSuperseded:
		if in.LedgerErr != nil {
			return "?"
		}

		return itoa(len(in.Superseded))
	default:
		// An unreadable file with no listed entry is not an empty file.
		if in.DocErr != nil || (in.Doc.Unreadable() && len(in.Pins) == 0) {
			return "?"
		}

		return itoa(len(in.Pins))
	}
}

// stateLine returns one line about a source that limits the view. The
// source is an unreadable pin file, unknown relationships, a pin file
// that cannot be edited, or failed readings. The detail pane holds the
// full text, so this line may be cut at a narrow width.
func (p *pinsView) stateLine(w int) string {
	t := p.st.theme
	in := p.inspection()

	if in == nil || in.DocErr != nil {
		return ""
	}

	var line string

	switch {
	case in.Doc.Unreadable():
		line = t.Attention.Render(t.Glyphs.Warn + " " + pinUnreadableText(in.Doc) + ". Read-only: no action is offered.")
	case pinsForeignRoot(p.st) != "":
		line = t.Attention.Render(t.Glyphs.Warn + " The index records another repository: " +
			oneLine(pinsForeignRoot(p.st)) + ". Retarget and prune are off.")
	case in.LinkErr != nil:
		line = t.Unknown.Render(t.Glyphs.Unknown + " Relationships are unknown: " + pinReason(in.LinkErr.Error()) + ".")
	case in.Links == nil:
		line = t.Unknown.Render(t.Glyphs.Unknown + " Relationships are unknown: the ledger was not read.")
	case in.Doc.FileBlock != "":
		line = t.Attention.Render(t.Glyphs.Warn + " The pin file cannot be edited: " + diag(in.Doc.FileBlock) + ".")
	case in.ReadingsErr != nil:
		line = t.Unknown.Render(t.Glyphs.Unknown + " Some readings are unknown: " + pinReason(in.ReadingsErr.Error()) + ".")
	default:
		return ""
	}

	return " " + truncate(line, w-1, t.Glyphs.Ellipsis)
}

// pinUnreadableText describes a pin file that gives no reliable listing,
// with its line when the reader knows it (D-5).
func pinUnreadableText(d *pinfile.Document) string {
	var err error

	switch {
	case d == nil:
		return pinfile.File + " could not be read"
	case d.DecodeErr != nil:
		err = d.DecodeErr
	case d.ReaderErr != nil:
		err = d.ReaderErr
	default:
		err = d.ListErr
	}

	text := pinfile.File + " could not be read"
	if d.ErrLine > 0 {
		text += " (line " + itoa(d.ErrLine) + ")"
	}

	if err != nil {
		text += ": " + errText(err)
	}

	return text
}

// body renders the loading, unavailable and empty states, or the list
// and detail boxes.
func (p *pinsView) body(w, h int) string {
	in := p.inspection()
	if in == nil {
		return p.loadingBox(w, h)
	}

	rows := p.rows()
	if len(rows) == 0 {
		return p.emptyBox(in, w, h)
	}

	// Pins has more columns than Proposals, so a wide terminal gives its
	// list two fifths of the width. The state column can then show a
	// label with two attention words.
	listW := max(min(propListW, w*propListW/propDesignW), w*2/5)

	return p.split(rows, w, h, listW, p.listBox, p.detailBox)
}

// listTitle names the list box after the filter.
func (p *pinsView) listTitle() string {
	switch p.filter {
	case pinNotPresent:
		return "Applied, pin not present"
	case pinSuperseded:
		return "Superseded proposals"
	default:
		return "Installed pins"
	}
}

// pinCols is the column layout of the list: marker 2, check 4 (only
// where marking works), number, RULE, REGIONS and STATE. A zero region
// width hides that column; the detail pane shows every value.
type pinCols struct {
	check  bool
	numW   int
	ruleW  int
	regW   int
	stateW int
}

// columns fits the list columns into w cells. A narrow box drops the
// region column first, so the rule keeps a readable width; the state
// column always stays, because it carries the relationship.
func (p *pinsView) columns(in *maintain.Inspection, rows []pinRow, w int) pinCols {
	c := pinCols{check: p.actionable(), numW: 4, regW: pinRegionW, stateW: pinStateW}

	label := 0

	for _, r := range rows {
		c.numW = max(c.numW, width(pinRowNum(r))+2)

		if r.pin != nil {
			label = max(label, width(p.pinLabel(in, r.pin).render())+1)
		}
	}

	fixed := 2 + c.numW + c.regW
	if c.check {
		fixed += 4
	}

	// The state column grows only while the rule keeps its room.
	if wide := min(pinStateMaxW, label); wide > c.stateW && w-fixed-wide >= pinRuleRoomW {
		c.stateW = wide
	}

	fixed += c.stateW
	c.ruleW = w - fixed

	if c.ruleW < propMinRule {
		c.ruleW += c.regW
		c.regW = 0
	}

	c.ruleW = max(4, c.ruleW)

	return c
}

// pinRowNum is the number column: the position of a pin in the file, or
// the pN form of a proposal row.
func pinRowNum(r pinRow) string {
	if r.pin != nil {
		return itoa(r.n + 1)
	}

	return propID(r.prop.ID)
}

// cells returns the row cells in column order.
func (c pinCols) cells(marker, check, num, rule, region, state string) []colCell {
	out := []colCell{cell(marker, 2)}

	if c.check {
		out = append(out, cell(check, 4))
	}

	out = append(out, cell(num, c.numW), cell(rule, c.ruleW))

	if c.regW > 0 {
		out = append(out, cell(region, c.regW))
	}

	return append(out, cell(state, c.stateW))
}

// listBox renders the list with its column header. The cursor row has
// the cursor glyph, and in the focused box a full-width selection
// background (listPane.renderList). The rows use integRow, so a clipped
// cell keeps a gap before the next column.
func (p *pinsView) listBox(rows []pinRow, w, h int, focused bool) string {
	t := p.st.theme
	in := p.inspection()
	c := p.columns(in, rows, boxInner(w))

	num := "#"
	if p.filter != pinInstalled {
		num = "ID"
	}

	header := t.Faint.Render(integRow(t, c.cells("", "", num, "RULE", "REGIONS", "STATE")...))

	return p.renderList(t, rows, w, h, focused, p.listTitle(), header, func(r pinRow, cursor bool) string {
		return p.listRow(in, c, r, cursor)
	})
}

// listRow renders one row. The cursor row has the cursor glyph and a
// bold rule; a marked pin has the check glyph in the accent color.
func (p *pinsView) listRow(in *maintain.Inspection, c pinCols, r pinRow, cursor bool) string {
	t := p.st.theme

	marker := ""
	style := t.Body

	if cursor {
		marker = t.Key.Render(t.Glyphs.Cursor)
		style = t.Title
	}

	var (
		rule, region string
		state        pinState
		marked       bool
	)

	if r.pin != nil {
		rule = r.pin.Entry.Rule
		region = pinRegionLabel(pinEntryRegions(r.pin.Entry))
		state = p.pinLabel(in, r.pin)
		marked = p.selected.has(r.pin.Entry.Ref())
	} else {
		rule = r.prop.Rule
		region = pinRegionLabel(r.prop.RegionSet())
		state = p.propState(r.prop)
	}

	check := t.Faint.Render(t.Glyphs.Uncheck)
	if marked {
		check = t.Marked.Render(t.Glyphs.Check)
	}

	ruleText := style.Render(oneLine(rule))
	if strings.TrimSpace(rule) == "" {
		ruleText = t.Faint.Render("(no rule)")
	}

	region = pinPathCell(region, max(1, c.regW-1), t.Glyphs.Ellipsis)

	return integRow(t, c.cells(marker, check, t.Muted.Render(pinRowNum(r)), ruleText, t.Muted.Render(region),
		state.fit(c.stateW, t.Glyphs.Ellipsis))...)
}

// pinPathCell fits a region into w cells. A long path keeps its last
// whole components, for example "…/store", because the end of a path is
// its most specific part. A path without a fitting component is cut
// from the left.
func pinPathCell(s string, w int, ell string) string {
	if width(s) <= w {
		return s
	}

	for i := range len(s) {
		if s[i] == '/' && width(ell)+width(s[i:]) <= w {
			return ell + s[i:]
		}
	}

	return truncateLeft(s, w, ell)
}

// pinRegionLabel is the short region label of a list row. A set of
// several regions shows its size; the detail pane lists them.
func pinRegionLabel(set []string) string {
	switch len(set) {
	case 0:
		return "repo-wide"
	case 1:
		return oneLine(set[0])
	default:
		return itoa(len(set)) + " regions"
	}
}

// detailBox renders the row under the cursor: a pin or a proposal of
// the history filters. The body scrolls inside the box, and another row
// starts at the top (listPane.renderDetail).
func (p *pinsView) detailBox(rows []pinRow, w, h int, focused bool) string {
	t := p.st.theme
	cur, _ := p.cursorRow(rows)
	inner := boxInner(w)

	if cur.pin != nil {
		return p.renderDetail(t, cur, "Pin "+itoa(cur.n+1), p.pinDetail(p.inspection(), cur, inner), w, h, focused)
	}

	return p.renderDetail(t, cur, "Proposal "+propID(cur.prop.ID), p.propDetail(cur.prop, inner), w, h, focused)
}

// titleLines renders a rule in the title style with a faint tag after
// it, or on its own line when the tag does not fit.
func (p *pinsView) titleLines(rule, tag string, w int) []string {
	t := p.st.theme

	text := oneLine(rule)
	if strings.TrimSpace(text) == "" {
		text = "(no rule)"
	}

	out := propLines(t.Title, text, w)
	if last := len(out) - 1; width(out[last])+2+width(tag) <= w {
		out[last] += "  " + t.Faint.Render(tag)
	} else {
		out = append(out, t.Faint.Render(tag))
	}

	return out
}

// pinNoteText sanitizes a note for display and drops its trailing line
// breaks. A YAML literal block ("note: |") ends with a line break, and
// the detail would show it as an empty row after the note.
func pinNoteText(note string) string {
	return strings.TrimRight(multiLine(note), "\n")
}

// pinFact renders a muted label column and value lines that the caller
// styled and wrapped to the value width.
func pinFact(t *Theme, label string, values []string) []string {
	if len(values) == 0 {
		return nil
	}

	return propIndent(t.Muted.Render(padRight(label, propLabelW)), values)
}

// pinValueW returns the width of a value beside the label column.
func pinValueW(w int) int {
	return max(1, w-propLabelW)
}

// regionValues renders a region set as accent lines, or "repo-wide".
func (p *pinsView) regionValues(set []string, w int) []string {
	t := p.st.theme

	if len(set) == 0 {
		return []string{t.Muted.Render("repo-wide")}
	}

	out := make([]string, 0, len(set))
	for _, r := range set {
		out = append(out, t.Accent.Render(truncate(oneLine(r), w, t.Glyphs.Ellipsis)))
	}

	return out
}

// pinDetail renders an installed pin. It shows the rule and its state,
// the live note, the file facts, the related proposals, the readings,
// similar pins, and the maintenance routes.
func (p *pinsView) pinDetail(in *maintain.Inspection, r pinRow, w int) []string {
	t := p.st.theme
	pin := r.pin
	e := pin.Entry

	out := p.titleLines(e.Rule, "pin "+itoa(r.n+1)+" of "+itoa(len(in.Pins)), w)

	parts := []string{p.pinLabel(in, pin).render()}

	if p.actionable() {
		if p.selected.has(e.Ref()) {
			parts = append(parts, t.Marked.Render(t.Glyphs.Check+" selected"))
		} else {
			parts = append(parts, t.Faint.Render(t.Glyphs.Uncheck+" not selected"))
		}
	}

	if c := pin.Readings.Confidence; c.Known && pinLinksKnown(in) && pinLinked(pin) {
		parts = append(parts, t.Muted.Render("tier")+" "+t.Body.Render(c.Tier.String()))
	}

	out = append(out, strings.Join(parts, "   "), "", t.Heading.Render("NOTE")+"  "+t.Faint.Render("live, from the file"))

	if strings.TrimSpace(e.Note) == "" {
		out = append(out, t.Muted.Render("No note is set."))
	} else {
		out = append(out, propLines(t.Body, pinNoteText(e.Note), w)...)
	}

	out = append(out, "")
	out = append(out, p.entryFacts(e, w)...)
	out = append(out, "")
	out = append(out, p.linkLines(in, pin, w)...)
	out = append(out, "")
	out = append(out, p.readingLines(in, pin, w)...)

	if sim := p.similarLines(in, pin, w); len(sim) > 0 {
		out = append(out, "")
		out = append(out, sim...)
	}

	out = append(out, "")

	return append(out, p.maintenanceLines(in, pin, w)...)
}

// entryFacts renders the regions, the file location, the unknown keys
// and the provenance comment of an entry in the label column.
func (p *pinsView) entryFacts(e pinfile.Entry, w int) []string {
	t := p.st.theme
	vw := pinValueW(w)

	out := pinFact(t, "Regions", p.regionValues(pinEntryRegions(e), vw))

	source := pinfile.File
	if e.Line > 0 {
		source += ":" + itoa(e.Line)
	}

	out = append(out, pinFact(t, "Source", []string{t.Muted.Render(truncateLeft(source, vw, t.Glyphs.Ellipsis))})...)

	for i, k := range e.Unknown {
		label := ""
		if i == 0 {
			label = "Keys"
		}

		out = append(out, pinFact(t, label, propLines(t.Attention, "unknown key "+oneLine(k)+" (no reader uses it)", vw))...)
	}

	if len(e.ProvenanceIDs) > 0 {
		out = append(out, pinFact(t, "Comment", propLines(t.Muted,
			"a seamark comment above it names "+propIDList(e.ProvenanceIDs), vw))...)
	}

	return out
}

// linkLines renders the relationship of a pin to the ledger and every
// matching row, so an ambiguous pin shows all of its matches. A linked
// pin whose live note changed shows the recorded note too.
func (p *pinsView) linkLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	t := p.st.theme
	out := []string{t.Heading.Render("PROPOSALS")}

	if !pinLinksKnown(in) {
		out = append(out, propNote(t, ToneUnknown, pinLinkUnknownText(in), w)...)

		// The reader keeps the matching rows of an entry for display,
		// but none of them is a proven link while a source is missing.
		if rows := pinMatches(pin.Link); len(rows) > 0 {
			out = append(out, propLines(t.Muted, "Rows with this rule and these regions; no link is proven:", w)...)

			for _, pr := range rows {
				out = append(out, p.proposalRow(pr, w))
			}
		}

		return out
	}

	l := pin.Link
	out = append(out, propLines(t.Muted, pinLinkSentence(l), w)...)

	if l.Link == pinfile.LinkAmbiguousFile {
		out = append(out, p.duplicateLines(in, pin, w)...)
	}

	for _, pr := range pinMatches(l) {
		out = append(out, p.proposalRow(pr, w))
	}

	if l.Link == pinfile.LinkLinked && l.NoteChanged {
		id := propID(l.Applied[0].ID)

		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, "The live note differs from the note that "+id+
			" recorded. Maintenance keeps the live note; an extraction asks about the recorded note.", w)...)
		out = append(out, "", t.Heading.Render("RECORDED NOTE")+"  "+t.Faint.Render(id))

		if note := l.Applied[0].Note; strings.TrimSpace(note) == "" {
			out = append(out, t.Muted.Render("No note was recorded."))
		} else {
			out = append(out, propLines(t.Muted, pinNoteText(note), w)...)
		}
	}

	return out
}

// duplicateLines names every other entry with the key of pin, for
// example "also pin 7 · .seamark/lessons.yaml:57". The route asks the
// user to fix the duplicates by hand, so each one shows its position
// (D-4: an ambiguous entry lists every match).
func (p *pinsView) duplicateLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	t := p.st.theme
	sep := " " + t.Glyphs.Sep + " "

	var out []string

	for i := range in.Pins {
		e := in.Pins[i].Entry
		if e.Index == pin.Entry.Index || e.Key != pin.Entry.Key {
			continue
		}

		where := pinfile.File
		if e.Line > 0 {
			where += ":" + itoa(e.Line)
		}

		out = append(out, truncate(t.Muted.Render("also pin "+itoa(i+1)+sep)+t.Body.Render(where), w, t.Glyphs.Ellipsis))
	}

	return out
}

// pinMatches returns every ledger row that matches an entry: the
// applied rows, then the pending rows, then the superseded history.
func pinMatches(l pinfile.EntryLink) []domain.Proposal {
	out := slices.Clone(l.Applied)
	out = append(out, l.Pending...)

	return append(out, l.Superseded...)
}

// pinLinkSentence explains a link in one plain sentence. The words say
// what the console can prove, never who wrote a pin.
func pinLinkSentence(l pinfile.EntryLink) string {
	switch l.Link {
	case pinfile.LinkLinked:
		return "Linked to one applied proposal by rule and regions."
	case pinfile.LinkNoProposal:
		return "No applied or pending proposal has this rule and these regions. That does not prove a person wrote the pin."
	case pinfile.LinkPendingMatch:
		return "A pending proposal has this rule and these regions, but no applied one."
	case pinfile.LinkAmbiguousFile:
		return "Ambiguous: more than one entry of the pin file has this rule and these regions."
	case pinfile.LinkAmbiguousLedger:
		return "Ambiguous: more than one applied proposal has this rule and these regions."
	case pinfile.LinkAmbiguousFields:
		return "Ambiguous: the stored region fields of the matching proposal disagree, so no link is proven."
	default:
		return "The link is unknown: " + l.Link.Reason() + "."
	}
}

// pinLinkUnknownText explains why no relationship is known.
func pinLinkUnknownText(in *maintain.Inspection) string {
	switch {
	case in.DocErr != nil:
		return "Relationships are unknown: the pin file could not be read."
	case in.LinkErr != nil:
		return "Relationships are unknown: " + pinReason(in.LinkErr.Error()) + "."
	default:
		return "Relationships are unknown: the ledger was not read."
	}
}

// pinReason makes a reason of the maintenance readers safe for a
// sentence. The readers start an unknown reason with "unknown: ", and a
// sentence that already says "unknown" would repeat the word.
func pinReason(reason string) string {
	return strings.TrimPrefix(diag(reason), "unknown: ")
}

// proposalRow renders one related ledger row: its ID, its state, and
// the agent and the date that created it.
func (p *pinsView) proposalRow(pr domain.Proposal, w int) string {
	t := p.st.theme

	line := t.Muted.Render(padRight(propID(pr.ID), 5)) + " " + pinStatusWord(t, pr.Status)
	if meta := p.agentMeta(pr); meta != "" {
		line += "   " + t.Faint.Render(meta)
	}

	return truncate(line, w, t.Glyphs.Ellipsis)
}

// agentLine renders the agent and the creation date of a proposal, or
// "unknown" when the ledger records neither.
func (p *pinsView) agentLine(pr domain.Proposal, w int) string {
	t := p.st.theme

	meta := p.agentMeta(pr)
	if meta == "" {
		return t.Muted.Render("unknown")
	}

	return truncate(t.Body.Render(meta), w, t.Glyphs.Ellipsis)
}

// agentMeta joins the agent and the creation date of a proposal.
func (p *pinsView) agentMeta(pr domain.Proposal) string {
	var meta []string

	if pr.Agent != "" {
		meta = append(meta, oneLine(pr.Agent))
	}

	if pr.CreatedAt > 0 {
		meta = append(meta, "created "+time.Unix(pr.CreatedAt, 0).UTC().Format(time.DateOnly))
	}

	return strings.Join(meta, " "+p.st.theme.Glyphs.Sep+" ")
}

// pinStatusWord renders a ledger status as a badge with its word.
func pinStatusWord(t *Theme, status string) string {
	g := t.Glyphs

	switch status {
	case domain.ProposalApplied:
		return t.Success.Render(g.Dot + " applied")
	case domain.ProposalProposed:
		return t.Attention.Render(g.Dot + " pending")
	case domain.ProposalSuperseded:
		return t.Muted.Render(g.Hollow + " superseded")
	case domain.ProposalDismissed:
		return t.Muted.Render(g.Dot + " dismissed")
	default:
		return t.Muted.Render(g.Dot + " " + oneLine(status))
	}
}

// readingLines renders the derived readings of a pin (D-23): evidence,
// living citations, scope advice, trigger state, outcome and exposure.
// Each one says "unknown" with its reason when its input is missing,
// because missing evidence is not a negative result. Nothing here
// chooses or runs an action. The heading avoids the word "selected",
// because in this view that word names the space marks.
func (p *pinsView) readingLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	t := p.st.theme
	r := pin.Readings
	vw := pinValueW(w)

	out := []string{t.Heading.Render("READINGS") + "  " + t.Faint.Render("derived advice; it chooses no action")}

	if !pinLinksKnown(in) || !pinLinked(pin) {
		text := "Evidence, scope, trigger and outcome readings stay unknown while the relationships are unknown."
		if pinLinksKnown(in) {
			text = "Evidence, scope, trigger and outcome readings need one linked proposal, and this pin has " +
				pinLinkNoun(pin.Link.Link) + "."
		}

		out = append(out, propLines(t.Muted, text, w)...)

		return append(out, pinFact(t, "Exposure", p.exposureValues(r.Exposure, vw))...)
	}

	out = append(out, pinFact(t, "Evidence", p.evidenceValues(r.Confidence, vw))...)
	out = append(out, pinFact(t, "Citations", p.citationValues(pin, r.Confidence, vw))...)
	out = append(out, pinFact(t, "Scope", p.scopeValues(pin, r.Scope, vw))...)
	out = append(out, pinFact(t, "Triggers", p.triggerValues(pin, r.Trigger, vw))...)
	out = append(out, pinFact(t, "Outcome", p.outcomeValues(r.Outcome, vw))...)

	return append(out, pinFact(t, "Exposure", p.exposureValues(r.Exposure, vw))...)
}

// pinLinkNoun names a link that is not a single linked proposal.
func pinLinkNoun(l pinfile.Link) string {
	switch l {
	case pinfile.LinkNoProposal:
		return "no linked proposal"
	case pinfile.LinkPendingMatch:
		return "only a pending match"
	default:
		return "an ambiguous link"
	}
}

// unknownValues renders the reason of an unknown reading.
func (p *pinsView) unknownValues(reason string, w int) []string {
	t := p.st.theme

	if reason == "" {
		reason = "unknown"
	}

	// The readers start the reason with "unknown:" or "no reading:", so
	// it keeps its own words.
	return propIndent(t.Unknown.Render(t.Glyphs.Unknown)+" ", propLines(t.Muted, diag(reason), max(1, w-2)))
}

// evidenceValues renders the evidence tier and its facts.
func (p *pinsView) evidenceValues(c maintain.ConfidenceReading, w int) []string {
	t := p.st.theme

	if !c.Known {
		return p.unknownValues(c.Reason, w)
	}

	return propLines(t.Body, c.Tier.String()+" "+t.Glyphs.Sep+" "+oneLine(c.Facts.Line()), w)
}

// citationValues renders the citations of the linked proposal: each
// living finding, then each cited finding that left the index. A gone
// finding gets its own row, so the user can see which citation aged out
// and not only a count. The bodies are not read, so the source, the
// date, the reviewer and the path identify a living finding.
func (p *pinsView) citationValues(pin *maintain.Pin, c maintain.ConfidenceReading, w int) []string {
	t := p.st.theme

	if !c.Known {
		return nil
	}

	members := pin.Link.Applied[0].Members
	out := []string{t.Body.Render(itoa(len(c.Living)) + " living of " + itoa(len(members)) + " cited")}

	// The comparison reads the whole rows, so a cut at the pane width
	// never makes two different rows look the same.
	texts := make([]string, len(c.Living))
	for i, f := range c.Living {
		texts[i] = ansi.Strip(p.citationLine(f, false, pinUncutW))
	}

	for i, f := range c.Living {
		// Two fix findings in one file on one day read the same, so such
		// rows also show the finding ID.
		same := 0

		for _, other := range texts {
			if other == texts[i] {
				same++
			}
		}

		out = append(out, p.citationLine(f, same > 1, w))
	}

	for _, id := range members {
		if !slices.ContainsFunc(c.Living, func(f domain.Finding) bool { return f.ID == id }) {
			out = append(out, propNote(t, ToneUnknown, "finding "+strconv.FormatInt(id, 10)+
				": no longer in the index (aged out, reverted or removed)", w)...)
		}
	}

	return out
}

// pinUncutW is a width that no citation row reaches, so a row renders
// whole.
const pinUncutW = 1 << 16

// citationLine renders one living finding: the pull request or the
// source, the reviewer, the creation date, and the path with the count
// of its other paths, as the Proposals evidence shows them. withID adds
// the finding ID for a row that would otherwise repeat another row.
func (p *pinsView) citationLine(f domain.Finding, withID bool, w int) string {
	t := p.st.theme
	sep := " " + t.Glyphs.Sep + " "

	var meta []string

	if f.PR > 0 {
		meta = append(meta, "PR #"+itoa(f.PR))
	} else if f.Source != "" {
		meta = append(meta, oneLine(f.Source))
	}

	if f.Reviewer != "" {
		meta = append(meta, oneLine(f.Reviewer))
	}

	if f.CreatedAt > 0 {
		meta = append(meta, time.Unix(f.CreatedAt, 0).UTC().Format(time.DateOnly))
	}

	if withID {
		meta = append(meta, "finding "+strconv.FormatInt(f.ID, 10))
	}

	line := t.Muted.Render(strings.Join(meta, sep))

	path := f.Path
	if path == "" && len(f.Paths) > 0 {
		path = f.Paths[0]
	}

	if path != "" {
		if len(f.Paths) > 1 {
			path += " +" + itoa(len(f.Paths)-1)
		}

		if len(meta) > 0 {
			line += t.Muted.Render(sep)
		}

		line += t.Accent.Render(oneLine(path))
	}

	return truncate(line, w, t.Glyphs.Ellipsis)
}

// scopeValues renders the scope advice: current, a change from the live
// set to the computed set with explicit rows, or no recompute. A broader
// or repository-wide change carries a warning, because it delivers the
// pin to more paths.
func (p *pinsView) scopeValues(pin *maintain.Pin, s maintain.ScopeReading, w int) []string {
	t := p.st.theme

	if !s.Known {
		return p.unknownValues(s.Reason, w)
	}

	a := s.Advice

	switch a.Kind {
	case distill.ScopeCurrent:
		return []string{t.Success.Render(t.Glyphs.OK+" current") + t.Muted.Render(" "+t.Glyphs.Sep+" regions already current")}
	case distill.ScopeNoEvidence:
		return append([]string{t.Unknown.Render(t.Glyphs.Unknown + " cannot recompute")},
			propLines(t.Muted, "No cited finding is living. The current regions stay; missing evidence never widens a pin.", w)...)
	}

	out := []string{t.Attention.Render(t.Glyphs.Warn + " would change")}
	out = append(out, t.regionDelta(pinEntryRegions(pin.Entry), a.Regions, w)...)

	if a.RepoWide {
		out = append(out, propNote(t, ToneAttention, "repository-wide: the pin would reach every path", w)...)
	} else if a.Broader {
		out = append(out, propNote(t, ToneAttention, "broader: reaches paths that the current regions do not", w)...)
	}

	if a.Narrower {
		out = append(out, propLines(t.Muted, "narrower: drops paths that the current regions reach", w)...)
	}

	return out
}

// triggerValues renders the trigger question state of the linked
// proposal, its stored trigger paths, and the confirmed triggers that
// the region set does not deliver.
func (p *pinsView) triggerValues(pin *maintain.Pin, tr maintain.TriggerReading, w int) []string {
	t := p.st.theme

	if !tr.Known {
		return p.unknownValues(tr.Reason, w)
	}

	// An open question is plain advice; an answered one is a fact.
	word, style := "answered", t.Body
	if tr.Open {
		word, style = "open", t.Muted
	}

	out := propLines(style, word+" "+t.Glyphs.Sep+" "+tr.State.String(), w)

	for _, path := range pin.Link.Applied[0].TriggerPaths {
		out = append(out, t.Accent.Render(truncate(oneLine(path), w, t.Glyphs.Ellipsis)))
	}

	// The hand edit is a route only while the console offers no retarget
	// and the scope is not current. Else w makes the change, or no change
	// is due.
	s := pin.Readings.Scope
	route := p.retargetBlock(pin) != "" && (!s.Known || s.Advice.Kind != distill.ScopeCurrent)

	for _, f := range tr.Blocked {
		out = append(out, propNote(t, ToneAttention, oneLine(pinTriggerFact(f, route)), w)...)
	}

	return out
}

// pinTriggerFact words a confirmed trigger that sets no region, from the
// fields of distill.TriggerFact. The shared BlockedLine always ends with
// a hand edit route, and the CLI keeps that text (D-1). The console
// offers w for the same change, so its own words name the hand edit
// only when route is set.
func pinTriggerFact(f distill.TriggerFact, route bool) string {
	why := "directly cited by the evidence"
	if !f.Direct {
		why = "confirmed by co-change (" + itoa(f.Together) + " shared commits)"
	}

	text := "trigger " + f.Path + " is " + why + ", but it sets no region: "
	if f.Region == "" {
		text += "the path is not in the working tree"
	} else {
		text += "the region set is full"
	}

	if route {
		text += "; edit the regions in " + pinfile.File + " by hand"
	}

	return text
}

// outcomeValues renders the passive outcome reading with its window and
// reason. The label says that the reading is derived: "working" is not
// a reason to remove a pin, and "not landing" is not proof of cause.
func (p *pinsView) outcomeValues(o maintain.OutcomeReading, w int) []string {
	t := p.st.theme

	if !o.Known {
		return p.unknownValues(o.Reason, w)
	}

	tone := ToneUnknown

	switch o.Reading.Verdict {
	case outcome.VerdictWorking:
		tone = ToneSuccess
	case outcome.VerdictNotLanding:
		tone = ToneAttention
	}

	out := propNote(t, tone, oneLine(o.Reading.Line()), w)

	return append(out, propLines(t.Faint, "(derived recurrence check; not proof of cause, and not a reason to prune)", w)...)
}

// exposureValues renders the delivery history of the live pin identity.
// That identity covers the rule, the note and the regions, so a retarget
// restarts the history although the wording stays the same.
func (p *pinsView) exposureValues(x maintain.ExposureReading, w int) []string {
	t := p.st.theme

	if !x.Known {
		return p.unknownValues(x.Reason, w)
	}

	if !x.Fired {
		return propLines(t.Muted, "never delivered under its current rule, note and regions", w)
	}

	sep := " " + t.Glyphs.Sep + " "
	text := "delivered " + itoa(x.Exposure.Count) + "×"

	if !x.Exposure.First.IsZero() {
		text += sep + "first " + x.Exposure.First.UTC().Format(time.DateOnly)
	}

	if x.Exposure.Matches > x.Exposure.Count {
		text += sep + "matched " + itoa(x.Exposure.Matches) + "×"
	}

	return propLines(t.Body, text, w)
}

// similarLines lists the pins of the same redundancy cluster. The list
// is advice: it selects nothing, and v opens a read-only comparison.
func (p *pinsView) similarLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	t := p.st.theme
	sim := pin.Readings.Similar

	if len(sim) == 0 {
		return nil
	}

	out := []string{t.Heading.Render("SIMILAR") + "  " + t.Faint.Render(propPlural(len(sim), "pin", "pins"))}

	for _, i := range sim {
		if i < 0 || i >= len(in.Pins) {
			continue
		}

		other := in.Pins[i].Entry
		lead := t.Muted.Render(padRight("pin "+itoa(i+1), 8))
		rest := t.Body.Render(oneLine(other.Rule)) + t.Faint.Render("  "+pinRegionLabel(pinEntryRegions(other)))

		out = append(out, truncate(lead+rest, w, t.Glyphs.Ellipsis))
	}

	return append(out, propLines(t.Muted, "v compares them side by side. Similar wording does not mean identical "+
		"coverage, and a similar pin is no reason to prune.", w)...)
}

// pinPendingMatchText names the route for a pin that only pending
// proposals match. The record refuses two proposals with one pin key,
// because two applied proposals that record one pin make Pins block it.
// So with several matches, the text asks for one record and dismissals.
func pinPendingMatchText(pending []domain.Proposal) string {
	ids := propIDList(pinProposalIDs(pending))
	tail := " The pin stays read-only here until one applied proposal links it."

	if len(pending) < 2 {
		return "Record " + ids + " as applied first: press p, select it, then press c." + tail
	}

	return "Record one of " + ids + " as applied first, and dismiss the others: press p, select one and " +
		"press c, then select the others and press d." + tail
}

// maintenanceLines names the keys that act on this pin, or why none
// does and the next valid route. Nothing changes before a preview and
// its y.
func (p *pinsView) maintenanceLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	t := p.st.theme
	out := []string{t.Heading.Render("MAINTENANCE")}

	note := func(tone Tone, text string) {
		out = append(out, propNote(t, tone, text, w)...)
	}

	l := pin.Link

	switch {
	case in.Doc.Unreadable():
		note(ToneAttention, "Read-only: "+pinUnreadableText(in.Doc)+". Fix the file and press r. No action is "+
			"offered until it reads, and no pin is shown as removed.")

		return out
	case !pinLinksKnown(in):
		note(ToneUnknown, pinLinkUnknownText(in))

		if in.Index.State != workspace.IndexReady {
			out = append(out, propLines(t.Muted, "Build the index from Overview (press 1, then b), or run this "+
				"command in a shell:", w)...)
			out = append(out, t.command("seamark index"))
		}

		return out
	case l.Link == pinfile.LinkNoProposal:
		out = append(out, propLines(t.Muted, "Read-only here: the console changes only pins with one linked "+
			"proposal. Edit "+pinfile.File+" by hand to change this pin.", w)...)

		return out
	case l.Link == pinfile.LinkPendingMatch:
		note(ToneAttention, pinPendingMatchText(l.Pending))

		return out
	case l.Link == pinfile.LinkAmbiguousFile:
		note(ToneAttention, "Blocked: remove or change the duplicate entries in "+pinfile.File+
			" by hand. Maintenance and extraction stay off until one entry remains.")

		return out
	case l.Link == pinfile.LinkAmbiguousLedger:
		note(ToneAttention, "Blocked: "+propIDList(pinProposalIDs(l.Applied))+" all record this pin, so no "+
			"action can choose one. Edit the pin by hand in "+pinfile.File+".")

		return out
	case l.Link != pinfile.LinkLinked:
		note(ToneAttention, "Blocked: "+l.Link.Reason()+". Edit the pin by hand in "+pinfile.File+".")

		return out
	case pinsForeignRoot(p.st) != "":
		out = append(out, p.foreignLines(w)...)

		return append(out, p.extractLines(pin, w)...)
	case in.Doc.FileBlock != "":
		note(ToneAttention, "Retarget and prune are off: the pin file cannot be edited ("+diag(in.Doc.FileBlock)+").")

		return append(out, p.extractLines(pin, w)...)
	case pin.Entry.Block != "":
		note(ToneAttention, "Retarget and prune are off: this entry cannot be edited safely ("+
			diag(pin.Entry.Block)+"). Edit it by hand in "+pinfile.File+".")

		return append(out, p.extractLines(pin, w)...)
	}

	// The keys that act on this pin come first. Each key that this pin
	// does not offer gets a muted line with its reason, so the footer and
	// the detail agree.
	noRetarget, noExtract := p.retargetBlock(pin), pinExtractBlock(pin)

	// A sentence never starts with the bare key "-", which reads as a
	// list bullet.
	keys := "The - key previews a prune."
	if noRetarget == "" {
		keys = "w previews a retarget to the computed regions. " + keys
	}

	if noExtract == "" {
		keys += " t prepares a trigger extraction, which writes no pin."
	}

	out = append(out, propLines(t.Body, keys+" The keys act on the selected pins, or on this pin when nothing "+
		"is selected. Nothing changes before you confirm with y.", w)...)

	if noRetarget != "" {
		out = append(out, propLines(t.Muted, "No retarget: "+noRetarget+".", w)...)
	}

	if noExtract != "" {
		out = append(out, p.extractLines(pin, w)...)
	}

	return append(out, p.gateLines(w)...)
}

// foreignLines explains why retarget and prune are off when the index
// records another repository. It names both pin files, because a commit
// would change the pin file of the recorded root and the ledger of this
// workspace. A new index in this checkout records this root.
func (p *pinsView) foreignLines(w int) []string {
	t := p.st.theme
	root := pinsForeignRoot(p.st)

	out := propNote(t, ToneAttention, "Retarget and prune are off: the index of this workspace records another "+
		"repository, "+oneLine(root)+". A change would write "+oneLine(filepath.Join(root, pinfile.File))+
		" and the ledger of this workspace, not "+oneLine(filepath.Join(p.st.svc.Root(), pinfile.File))+".", w)

	out = append(out, propLines(t.Muted, "Build the index of this checkout from Overview (press 1, then b), or "+
		"run this command here, then press r:", w)...)

	return append(out, t.command("seamark index"))
}

// extractLines states whether t asks about the trigger question of pin.
// An extraction writes trigger fields in the ledger and never the pin
// file, so it stays on when a file block stops retarget and prune.
func (p *pinsView) extractLines(pin *maintain.Pin, w int) []string {
	t := p.st.theme

	if why := pinExtractBlock(pin); why != "" {
		return propLines(t.Muted, "No trigger extraction: "+why+".", w)
	}

	return propLines(t.Body, "t still prepares a trigger extraction, which writes no pin.", w)
}

// gateLines states the distill.write gate as the ledger snapshot reads
// it. The preview reads the gate again, so a stale line changes nothing.
func (p *pinsView) gateLines(w int) []string {
	t := p.st.theme

	if !p.st.ledger.loaded || p.st.ledger.value == nil {
		return nil
	}

	l := p.st.ledger.value

	switch {
	case l.WriteErr != nil:
		return propNote(t, ToneAttention, "The write gate could not be read: "+errText(l.WriteErr), w)
	case l.Write:
		return propLines(t.Muted, "distill.write is on: a confirmed retarget or prune writes "+pinfile.File+".", w)
	default:
		return propLines(t.Muted, "distill.write is off: a preview shows the change and writes nothing. "+
			"Settings › Pin writing (5) turns it on.", w)
	}
}

// pinProposalIDs returns the IDs of ps.
func pinProposalIDs(ps []domain.Proposal) []int64 {
	ids := make([]int64, len(ps))
	for i, pr := range ps {
		ids[i] = pr.ID
	}

	return ids
}

// propDetail renders a history row: the recorded proposal of a missing
// pin, or a superseded proposal. Both are read-only.
func (p *pinsView) propDetail(pr domain.Proposal, w int) []string {
	t := p.st.theme
	vw := pinValueW(w)

	out := p.titleLines(pr.Rule, propID(pr.ID), w)

	status := p.propState(pr).render()
	if p.filter == pinNotPresent {
		status += "   " + pinStatusWord(t, pr.Status) + t.Faint.Render(" in the ledger")
	}

	out = append(out, status, "", t.Heading.Render("NOTE")+"  "+t.Faint.Render("recorded with the proposal"))

	if strings.TrimSpace(pr.Note) == "" {
		out = append(out, t.Muted.Render("No note was recorded."))
	} else {
		out = append(out, propLines(t.Body, pinNoteText(pr.Note), w)...)
	}

	out = append(out, "")
	out = append(out, pinFact(t, "Regions", p.regionValues(pr.RegionSet(), vw))...)

	for i, path := range pr.TriggerPaths {
		label := ""
		if i == 0 {
			label = "Triggers"
		}

		out = append(out, pinFact(t, label, []string{t.Accent.Render(truncate(oneLine(path), vw, t.Glyphs.Ellipsis))})...)
	}

	out = append(out, pinFact(t, "Agent", []string{p.agentLine(pr, vw)})...)
	out = append(out, "")

	if p.filter == pinSuperseded {
		out = append(out, t.Heading.Render("HISTORY"))
		out = append(out, propLines(t.Muted, "Superseded: its pin was pruned. Read-only; not restorable here. "+
			"Its findings and signatures stay in the index.", w)...)

		return out
	}

	// A row whose fields disagree has no proven absence. The text must
	// not ask for the regions above, because that pin can already exist.
	if pinRowAmbiguous(p.inspection(), pr) {
		out = append(out, t.Heading.Render("AMBIGUOUS"))
		out = append(out, propLines(t.Body, "The stored region fields of "+propID(pr.ID)+" disagree. No entry of "+
			pinfile.File+" has its rule with the stored regions, but its apply writes the regions above. So "+
			"Seamark cannot prove that its pin is gone, and it does not supersede the proposal.", w)...)
		out = append(out, "")

		return append(out, propLines(t.Muted, "This row is read-only here. Resolve it by hand in "+pinfile.File+
			" or in the ledger, then press r.", w)...)
	}

	out = append(out, t.Heading.Render("MISMATCH"))
	out = append(out, propLines(t.Body, "The ledger records "+propID(pr.ID)+" as applied, but no entry of "+
		pinfile.File+" has its rule and regions. Seamark does not supersede the proposal or write the pin back.", w)...)
	out = append(out, "")

	out = append(out, propLines(t.Muted, "A hand edit may have changed or removed the pin. A changed pin shows "+
		"under installed as \"no proposal\". This row is read-only here.", w)...)
	out = append(out, "")

	// Bare absence does not prove who removed the pin, so the console
	// offers no repair. The text names both manual outcomes instead.
	return append(out, propLines(t.Muted, "To restore the pin, add the rule, the note and the regions above to "+
		pinfile.File+" by hand, then press r. If the pin was meant to go, the row stays here as a known "+
		"mismatch; the console offers no repair for it.", w)...)
}

// loadingBox shows the first read of the pins.
func (p *pinsView) loadingBox(w, h int) string {
	t := p.st.theme

	return t.messageBox(t.Title.Render("Installed pins"), "", propMessage{
		head: []string{t.Accent.Render(t.spinnerFrame(p.st.tick)) + " " + t.Title.Render("Reading the pin file"+t.Glyphs.Ellipsis)},
		body: "Seamark reads " + pinfile.File + " and relates it to the proposal ledger. Nothing is written.",
	}, w, h)
}

// emptyBox explains an empty list. The cause is a file that could not
// be read, no pin file, an empty pin list, a filter that hides every
// row, or an empty history.
func (p *pinsView) emptyBox(in *maintain.Inspection, w, h int) string {
	t := p.st.theme
	g := t.Glyphs
	title := t.Title.Render(p.listTitle())
	count := t.Muted.Render("0")

	routes := []string{t.Key.Render("p") + t.Muted.Render(" open Proposals   ") + t.Key.Render("e") +
		t.Muted.Render(" open Evidence")}

	var m propMessage

	switch {
	case p.filter == pinInstalled && in.DocErr != nil:
		m = propMessage{
			head: []string{t.Failure.Render(g.Fail) + " " + t.Title.Render("The pin file could not be read")},
			body: errText(in.DocErr) + "\n\nNo pin is listed, and none is shown as removed. The other views " +
				"stay usable. Press r to read the file again.",
		}
		count = t.Muted.Render("?")
	case p.filter == pinInstalled && in.Doc.Unreadable() && len(in.Pins) == 0:
		m = propMessage{
			head: []string{t.Attention.Render(g.Warn) + " " + t.Title.Render("The pin file could not be read")},
			body: pinUnreadableText(in.Doc) + ".\n\nSeamark lists no pin, because it could not read the list. " +
				"That is not an empty list: no pin is shown as removed. Fix the file, then press r.",
		}
		count = t.Muted.Render("?")
	case p.filter == pinInstalled && len(in.Pins) == 0 && !in.Doc.Guard.Exists && len(in.Doc.Bytes) == 0:
		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No pin file yet")},
			body: pinfile.File + " does not exist, so no pin is installed. Pins come from applied proposals: " +
				"review proposals in Proposals, or collect evidence in Evidence first.",
			next: routes,
		}
	case p.filter == pinInstalled && len(in.Pins) == 0:
		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No pins in "+pinfile.File)},
			body: "The file has no pin entry. Apply a proposal in Proposals to add one.",
			next: routes,
		}
	case p.filter == pinNotPresent && (in.Links == nil || in.Links.NotPresentErr != nil):
		reason := pinLinkUnknownText(in)
		if in.Links != nil {
			reason = errText(in.Links.NotPresentErr) + "."
		}

		m = propMessage{
			head: []string{t.Unknown.Render(g.Unknown) + " " + t.Title.Render("Unknown which pins are missing")},
			body: reason + " Seamark never reads an error as a missing pin.",
		}
		p.unknownRoute(in, &m)
		count = t.Muted.Render("?")
	case p.filter == pinSuperseded && in.LedgerErr != nil:
		m = propMessage{
			head: []string{t.Unknown.Render(g.Unknown) + " " + t.Title.Render("The history is unknown")},
			body: "The superseded proposals could not be read: " + pinReason(in.LedgerErr.Error()) + ".",
		}
		p.unknownRoute(in, &m)
		count = t.Muted.Render("?")
	case p.region != "" || p.text != "":
		m = p.filterMessage()
	case p.filter == pinNotPresent:
		m = propMessage{
			head: []string{t.Success.Render(g.OK) + " " + t.Title.Render("Every applied proposal has its pin")},
			body: "Each applied proposal matches one entry of " + pinfile.File + " by rule and regions.",
		}
	default:
		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No superseded proposals")},
			body: "A confirmed prune moves the proposals of the removed pins here. The history is read-only.",
		}
	}

	return t.messageBox(title, count, m, w, h)
}

// unknownRoute adds the next valid route to the message of a history
// filter that a read error hides. A missing index has the build route; an
// unreadable pin file needs a fix; any other error may pass on a new
// read. The installed pins stay listed, so f leads back to them. The
// routes go in the next lines, because a short box clips the body first.
func (p *pinsView) unknownRoute(in *maintain.Inspection, m *propMessage) {
	t := p.st.theme
	key := func(k string) string { return t.Key.Render(k) }

	switch {
	case in.Index.State != workspace.IndexReady:
		m.next = append(m.next,
			t.Muted.Render("Press ")+key("1")+t.Muted.Render(", then ")+key("b")+t.Muted.Render(" to build the index, or run:"),
			t.command("seamark index"))
	case in.DocErr != nil || in.Doc.Unreadable():
		m.next = append(m.next, t.Muted.Render("Fix "+pinfile.File+", then press ")+key("r")+t.Muted.Render("."))
	default:
		m.next = append(m.next, t.Muted.Render("Press ")+key("r")+t.Muted.Render(" to read the ledger again."))
	}

	if len(in.Pins) > 0 {
		m.next = append(m.next, "", key("f")+t.Muted.Render(" cycles back to the installed pins, which stay listed"))
	}
}

// filterMessage explains a filter that hides every row.
func (p *pinsView) filterMessage() propMessage {
	t := p.st.theme

	var what []string

	if p.region != "" {
		what = append(what, "region "+oneLine(p.region))
	}

	if p.text != "" {
		what = append(what, `text "`+oneLine(p.text)+`"`)
	}

	noun := "pins"
	if p.filter != pinInstalled {
		noun = p.filter.label() + " proposals"
	}

	return propMessage{
		head: []string{t.Muted.Render(t.Glyphs.Info) + " " + t.Title.Render("No "+noun+" match "+strings.Join(what, " and "))},
		body: "Press / or z and submit an empty value to show every row.",
	}
}
