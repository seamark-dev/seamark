package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the pin form, the discard question, and the stages
// of the pin review (pins_author.go). Every text from the pin file, the
// ledger, the user, or an error passes through a sanitizer here, at the
// leaf: ui.OneLine, ui.MultiLine, ui.ErrText or pinEscaped. The
// authoring engine returns such text raw (A4 hand-off).
//
// The guidance lines use the renderer of the delivery engine
// (reviews.SurfacedPin.Lesson), so they show the delivered wording. They
// keep the line breaks of a note, and the delivery surfaces join them
// (pinGuidanceLabel). The form and the review show each character
// that the text check refuses as a U+XXXX escape (pinEscaped), so a
// hidden character stays visible and the user can find it.

// pinFormLabelW is the label column of the form: "Regions" and the
// other field names fit with a gap of three cells.
const pinFormLabelW = 10

// pinNotePreview is the number of note lines that an unfocused note
// row shows. The guidance and the review show the whole note.
const pinNotePreview = 4

// pinGuidancePreview is the number of guidance lines that the form
// shows. The review shows every line and scrolls.
const pinGuidancePreview = 8

// pinReplaceNote is the replacement statement of the File row (P4-17).
const pinReplaceNote = "The save replaces the file: it keeps the permission bits, not an access control list, " +
	"extended attributes, or another owner."

// formView renders the context line, a blank line and the form box. The
// box leaves the last row blank above the footer, as the other
// sub-views do.
func (p *pinsView) formView(w, h int) string {
	t := p.st.theme
	f := p.author.form
	boxH := max(4, h-3)
	inner := ui.BoxInner(w)

	body, keep := p.formLines(inner, ui.BoxRows(boxH))
	tail := append([]string{integRule(t, inner)}, p.formTail(inner)...)

	o := ui.BoxOpts{W: w, H: boxH, Focus: true, Title: integPaneTitle(t, p.formTitle(), true), Right: p.formBadge()}

	return strings.Join([]string{p.formContext(w), "", t.ScrollBox(o, body, tail, &f.sc, keep, true)}, "\n")
}

// formTitle names the form: a new pin, or the pin that the draft edits.
func (p *pinsView) formTitle() string {
	d := p.author.form.draft
	if d.op == maintain.AuthorCreate {
		return "New pin"
	}

	if where := p.pinWhere(d.ref); where != "" && d.orphan == "" {
		return "Edit " + where
	}

	return "Edit pin"
}

// formBadge labels the state of the draft in the box border. The words
// carry the state without color.
func (p *pinsView) formBadge() string {
	t := p.st.theme
	d := p.author.form.draft

	switch {
	case d.orphan != "":
		return t.Badge(ui.ToneFailure, "orphaned draft")
	case d.changed:
		return t.Badge(ui.ToneAttention, "entry changed")
	}

	return t.Attention.Render("draft")
}

// formContext renders the line above the box: what the form drafts and
// that nothing is written yet.
func (p *pinsView) formContext(w int) string {
	t := p.st.theme
	sep := " " + t.Glyphs.Sep + " "

	line := " " + t.Title.Render(p.formTitle()) + t.Faint.Render(sep+pinfile.File+sep+
		"nothing is written before the review and its y")

	return ui.Truncate(line, w, t.Glyphs.Ellipsis)
}

// formLines renders the lead notes, the rows with their verdicts, and
// the guidance. It returns the line range of the cursor row, so a short
// box keeps it visible.
func (p *pinsView) formLines(w, rows int) (lines []string, keep [2]int) {
	t := p.st.theme
	f := p.author.form
	d := f.draft
	vw := max(1, w-2-pinFormLabelW)
	issues := d.issues()

	out := formRows{t: t, labelW: pinFormLabelW, w: w, cursor: f.row, lines: p.formLead(w), keep: [2]int{-1, -1}}
	row := out.add

	row(pinRowRule, "Rule", p.ruleRow(vw), pinRowNotes(t, d.ruleLock, d.rule.Notice(), issues, maintain.FieldRule, -1, vw))
	row(pinRowNote, "Note", p.noteRow(vw, rows), pinRowNotes(t, d.noteLock, d.note.Notice(), issues,
		maintain.FieldNote, -1, vw))
	row(pinRowRepoWide, "Scope", []string{radio(t, d.scope == maintain.ScopeRepoWide, "repository-wide")}, nil)
	row(pinRowRegions, "", []string{radio(t, d.scope == maintain.ScopeRegions, "these regions")},
		pinRowNotes(t, d.scopeLock, "", issues, maintain.FieldScope, -1, vw))

	for i, region := range d.regions {
		label := ""
		if i == 0 {
			label = "Regions"
		}

		style := t.Body
		if d.scopeLock != "" {
			style = t.Muted
		}

		value := propLines(style, pinEscapedLine(region), vw)
		notice := ""

		if f.field == pinFieldRegion && f.regionAt == i {
			value, notice = pinInputLines(f.region.View(t), vw), f.region.Notice()
		}

		row(pinRowFirstRegion+i, label, value, pinRowNotes(t, "", notice, issues, maintain.FieldRegions, i, vw))
	}

	label := ""
	if len(d.regions) == 0 {
		label = "Regions"
	}

	add, notice := []string{t.Faint.Render("+ add a region")}, ""
	if f.field == pinFieldRegion && f.regionAt < 0 {
		add, notice = pinInputLines(f.region.View(t), vw), f.region.Notice()
	}

	notes := pinRowNotes(t, "", notice, issues, maintain.FieldRegions, -1, vw)
	row(f.addRow(), label, add, append(notes, propLines(t.Muted, pinRegionsHelp, vw)...))

	lines = out.lines
	lines = append(lines, "", t.Heading.Render("GUIDANCE")+"  "+t.Faint.Render(pinGuidanceLabel))
	lines = append(lines, ui.ClipWithEllipsis(propLines(t.Body, p.formGuidance(), w), pinGuidancePreview, w,
		t.Glyphs.Ellipsis)...)

	return lines, out.keep
}

// formLead returns the notes above the rows: an orphaned draft, a
// changed entry, an unknown relationship, and ignored marks.
func (p *pinsView) formLead(w int) []string {
	t := p.st.theme
	d := p.author.form.draft

	var out []string

	note := func(tone ui.Tone, text string) {
		out = append(out, propNote(t, tone, text, w)...)
	}

	switch {
	case d.orphan != "":
		note(ui.ToneFailure, "This draft lost its pin: "+ui.OneLine(d.orphan)+". It keeps its text: c copies it and x "+
			"discards it. It opens no review.")
	case d.changed:
		note(ui.ToneAttention, "Entry changed since the draft started. The review shows the current value next to "+
			"the draft for each edited field.")
	}

	if d.op == maintain.AuthorEdit && d.unknown && d.orphan == "" {
		note(ui.ToneUnknown, ovSentenceEnd(pinUnknownNote))
	}

	if len(p.selected) > 0 {
		marks := pinMarksNote
		if d.op == maintain.AuthorCreate {
			marks = "Marks are ignored: a new pin goes after the last pin, and the marks stay."
		}

		out = append(out, propLines(t.Muted, marks, w)...)
	}

	if len(out) > 0 {
		out = append(out, "")
	}

	return out
}

// ruleRow renders the value lines of the Rule row.
func (p *pinsView) ruleRow(w int) []string {
	t := p.st.theme
	f := p.author.form
	d := f.draft

	switch {
	case f.field == pinFieldRule:
		return pinInputLines(d.rule.View(t), w)
	case d.ruleValue() == "":
		return []string{t.Faint.Render("(empty)")}
	case d.ruleLock != "":
		return propLines(t.Muted, pinEscapedLine(d.ruleValue()), w)
	}

	return propLines(t.Body, pinEscapedLine(d.ruleValue()), w)
}

// noteRow renders the value lines of the Note row. The focused note is
// the text area; an unfocused note shows its first lines.
func (p *pinsView) noteRow(w, rows int) []string {
	t := p.st.theme
	f := p.author.form
	d := f.draft

	switch {
	case f.field == pinFieldNote:
		return strings.Split(d.note.View(t, w, max(3, min(12, rows/3))), "\n")
	case d.noteValue() == "":
		return []string{t.Faint.Render("(empty)")}
	}

	style := t.Body
	if d.noteLock != "" {
		style = t.Muted
	}

	text := pinEscaped(d.noteValue())

	lines := propLines(style, text, w)
	if len(lines) <= pinNotePreview {
		return lines
	}

	more := len(lines) - pinNotePreview

	return append(lines[:pinNotePreview], t.Faint.Render(t.Glyphs.Ellipsis+" "+propPlural(more, "more line",
		"more lines")+"; enter shows them"))
}

// pinRowNotes returns the notes under one row. They are the reason of a
// read-only row, the notice of its input, and the draft check verdicts
// of field and index. A normalization line is information, so it
// takes the muted style; every other verdict blocks the draft.
func pinRowNotes(t *ui.Theme, lock, notice string, issues []maintain.FieldIssue, field maintain.AuthorField,
	index, w int,
) []string {
	var out []string

	if lock != "" {
		out = append(out, propLines(t.Muted, "Read-only: "+ui.OneLine(lock)+".", w)...)
	}

	if notice != "" {
		out = append(out, propNote(t, ui.ToneAttention, ui.OneLine(notice), w)...)
	}

	for _, is := range issues {
		if is.Field != field || is.Index != index {
			continue
		}

		if is.Info {
			out = append(out, propLines(t.Muted, ovSentenceEnd(pinEscapedLine(is.Text)), w)...)
		} else {
			out = append(out, propNote(t, ui.ToneAttention, ovSentenceEnd(pinEscapedLine(is.Text)), w)...)
		}
	}

	return out
}

// pinInputLines wraps the view of a focused one-line input, so a long
// value and its cursor glyph stay in the row.
func pinInputLines(view string, w int) []string {
	return strings.Split(ansi.Hardwrap(view, max(1, w), true), "\n")
}

// formGuidance renders the draft as the delivery engine shows it. An
// unset scope names itself, because the engine would read it as
// repository-wide.
func (p *pinsView) formGuidance() string {
	d := p.author.form.draft

	var set []string

	if d.scope == maintain.ScopeRegions {
		for _, raw := range d.regions {
			// The planner stores the checked value, so the guidance shows
			// it. A refused region shows as typed; its row names the fix.
			if in, err := reviews.CheckPinRegion(raw); err == nil {
				raw = in.Value
			}

			set = append(set, raw)
		}
	}

	pin := reviews.PinForRegions(d.ruleValue(), d.noteValue(), set)
	text := pinGuidance(p.st.theme, pin)

	if d.scope == maintain.ScopeUnset {
		text = pinGuidanceWith(p.st.theme, pin, "scope not chosen")
	}

	return text
}

// pinGuidanceLabel names what the GUIDANCE lines show. The form keeps
// the line breaks of a note, but the delivery surfaces print a pin on
// one line, so the label does not claim the delivered text. The heading
// line is not wrapped, so it stays below the 56 cells of the box at 60x20.
const pinGuidanceLabel = "the rendered pin; delivered on one line"

// pinGuidance renders a pin in the form of a hook line: "pin · regions ·
// rule — note". The text comes from reviews.SurfacedPin.Lesson, the one
// renderer of every delivery surface, and keeps its escapes visible. It
// keeps the line breaks of a note; the hook prints the same text on one
// line.
func pinGuidance(t *ui.Theme, pin reviews.PinRule) string {
	return pinGuidanceWith(t, pin, "")
}

// pinGuidanceWith renders a pin with region in place of its regions, or
// with its own regions when region is "". The ASCII glyph set gets an
// ASCII dash, so its frames hold only ASCII.
func pinGuidanceWith(t *ui.Theme, pin reviews.PinRule, region string) string {
	l := reviews.SurfacedPin{Pin: pin}.Lesson()
	sep := " " + t.Glyphs.Sep + " "

	if region == "" {
		region = l.Region
	}

	symptom := l.Symptom
	if t.ASCII {
		symptom = strings.Replace(symptom, " — ", " "+t.Glyphs.Dash+" ", 1)
	}

	return pinEscaped("pin" + sep + region + sep + symptom)
}

// pinEscapeRunes replaces each character that the text check refuses
// (pinRefusedRune) or that the sanitizer drops with its U+XXXX escape.
// So a bidi mark or a separator, which the sanitizer keeps, stays
// visible too. Line breaks and tabs stay, so a diff keeps its layout.
func pinEscapeRunes(s string) string {
	var b strings.Builder

	for _, r := range s {
		c := string(r)

		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case pinRefusedRune(r) || ui.MultiLine(c) != c:
			fmt.Fprintf(&b, "%U", r)
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// pinEscaped makes text safe for display with the escapes of
// pinEscapeRunes. ui.MultiLine then keeps the line breaks.
func pinEscaped(s string) string {
	return ui.MultiLine(pinEscapeRunes(s))
}

// pinEscapedLine is pinEscaped on one line.
func pinEscapedLine(s string) string {
	return ui.OneLine(pinEscaped(s))
}

// pinEscapedSet escapes each region of set, for maintRegionText.
func pinEscapedSet(set []string) []string {
	out := make([]string, len(set))
	for i, r := range set {
		out[i] = pinEscapedLine(r)
	}

	return out
}

// formTail is the state of the draft under the rule: what s does next,
// or why the draft cannot be reviewed.
func (p *pinsView) formTail(w int) []string {
	t := p.st.theme
	d := p.author.form.draft
	sep := " " + t.Glyphs.Sep + " "

	// maintain.Blocking owns the rule "an issue blocks unless Info", so
	// the count asks it for each issue.
	blocking := 0

	for _, is := range d.issues() {
		if maintain.Blocking([]maintain.FieldIssue{is}) {
			blocking++
		}
	}

	switch {
	case d.orphan != "":
		return propNote(t, ui.ToneFailure, "Orphaned draft"+sep+"c copies it"+sep+"x discards it", w)
	case d.op == maintain.AuthorEdit && !d.dirty():
		return propLines(t.Muted, "No change yet"+sep+"edit a row, then s reviews it", w)
	case blocking > 0:
		return propNote(t, ui.ToneAttention, propPlural(blocking, "item", "items")+" to fix"+sep+
			"s still shows the review; y stays off", w)
	}

	return propLines(t.Body, "Draft complete"+sep+"s reviews the change"+sep+"nothing is written before y", w)
}

// promptContent is the question before x discards a draft.
func (p *pinsView) promptContent(w int) ui.ModalOpts {
	t := p.st.theme
	d := p.author.form.draft

	what := "the new pin"
	if d.op == maintain.AuthorEdit {
		what = "the edit of " + p.quoteRule(d.base.Rule)
	}

	return ui.ModalOpts{
		Title: t.Attention.Render("Discard this draft?"),
		Body:  propLines(t.Body, "The draft of "+what+" goes away. "+pinfile.File+" does not change.", w),
		Actions: []key.Binding{
			ui.WithHelp(authorKeys.DiscardYes, "d", "discard the draft"), ui.WithHelp(ui.Nav.Back, "esc", "keep it"),
		},
	}
}

// content builds the modal of the current stage of a review in w cells.
func (r *pinReview) content(w int) ui.ModalOpts {
	t := r.st.theme

	switch r.stage {
	case flowPreparing:
		return flowWaitModal(r.st, "Preparing the pin review", "Seamark reads "+pinfile.File+", the ledger and the "+
			"write gate. Nothing is written.", true, w)
	case flowCommitting:
		return flowWaitModal(r.st, r.st.mutation, "Seamark checks the review again, then replaces "+pinfile.File+
			". The ledger does not change. The result appears here.", false, w)
	case flowResult:
		return r.resultView(w)
	}

	if r.err != nil {
		hint := "Fix the cause named above, then " + r.reopen() + "."

		return previewFailedModal(t, "The pin review", r.err, ovSentence(hint), "Nothing was changed.",
			r.previewActions(false), w)
	}

	return r.planView(w)
}

// planView renders a plan: the lead, the blockers, the draft verdicts,
// the facts, the guidance, the history, the effects and the exact diff.
func (r *pinReview) planView(w int) ui.ModalOpts {
	body := r.planLead(w)
	body = append(body, r.blockerLines(w)...)
	body = append(body, r.issueLines(w)...)
	body = append(body, r.factLines(w)...)
	body = append(body, r.guidanceLines(w)...)
	body = append(body, r.changedLines(w)...)
	body = append(body, r.historyLines(w)...)
	body = append(body, r.noteLines(w)...)
	body = append(body, r.diffLines(w)...)

	return ui.ModalOpts{
		Title: r.planTitle(), Right: r.planRight(), Body: body, Tail: r.planTail(w), Actions: r.previewActions(false),
	}
}

// pinOpTitle names an operation in a title.
func pinOpTitle(op maintain.AuthorOp) string {
	switch op {
	case maintain.AuthorRemove:
		return "Remove pin"
	case maintain.AuthorEdit:
		return "Edit pin"
	default:
		return "New pin"
	}
}

// planTitle names the operation and the rule. A removal takes the
// failure style, as a prune does, because it removes guidance.
func (r *pinReview) planTitle() string {
	t := r.st.theme
	p := r.preview
	op := p.Request.Op

	verb := t.TabActive.Render(pinOpTitle(op))
	if op == maintain.AuthorRemove {
		verb = t.Failure.Bold(true).Render(pinOpTitle(op))
	}

	rule := p.New.Rule
	if op == maintain.AuthorRemove || rule == "" {
		rule = p.Old.Rule
	}

	if rule = pinEscapedLine(rule); rule == "" {
		return verb
	}

	return verb + " " + t.Body.Render(ui.Truncate(rule, pinNoteRuleW, t.Glyphs.Ellipsis))
}

// planRight labels the plan in the border: "preview only" while y is
// off.
func (r *pinReview) planRight() string {
	t := r.st.theme
	if !r.preview.Confirmable {
		return t.Badge(ui.ToneAttention, "preview only")
	}

	return t.Muted.Render(pinfile.File)
}

// planLead is the first paragraph: what y does, or that y is off.
func (r *pinReview) planLead(w int) []string {
	t := r.st.theme
	p := r.preview

	var lead string

	switch {
	case p.NoOp:
		lead = "The draft changes nothing, so y is off. Nothing was changed."
	case !p.Confirmable && p.After == nil:
		lead = "This review has no change to confirm, so y is off. Nothing was changed."
	case !p.Confirmable:
		lead = "This review shows the exact change, but it cannot be confirmed, so y is off. Nothing was changed."
	default:
		lead = r.writeLead() + " distill.write is on. Nothing changes before you press y."
	}

	out := propLines(t.Body, lead, w)

	if r.marks {
		out = append(out, propLines(t.Muted, pinMarksNote, w)...)
	}

	if p.Confirmable && r.st.busy() {
		out = append(out, "")
		out = append(out, propNote(t, ui.ToneAttention, r.st.mutation+" is running. y is off until it ends.", w)...)
	}

	return out
}

// writeLead says what a confirmed plan writes.
func (r *pinReview) writeLead() string {
	where := r.where
	if where == "" {
		where = "the pin"
	}

	switch r.preview.Request.Op {
	case maintain.AuthorRemove:
		return "Seamark removes " + where + " from " + pinfile.File + "."
	case maintain.AuthorEdit:
		return "Seamark changes " + where + " in place in " + pinfile.File + "."
	default:
		return "Seamark adds this pin after the last pin of " + pinfile.File + "."
	}
}

// blockerLines lists every reason that blocks the write, each with its
// route. The blocker that o routes names the key too.
func (r *pinReview) blockerLines(w int) []string {
	t := r.st.theme
	p := r.preview

	var out []string

	_, _, words, _ := r.route()
	routed := r.routed()

	for i, b := range p.Blockers {
		text := pinSentence(pinEscapedLine(b.String()))
		if i == routed {
			text += " Press o to open " + words + "."
		}

		out = append(out, "")
		out = append(out, propNote(t, ui.ToneAttention, text, w)...)
	}

	return out
}

// issueLines lists the verdicts of the draft check, each with its field.
// A normalization line is information, so it takes the muted style.
func (r *pinReview) issueLines(w int) []string {
	t := r.st.theme
	p := r.preview

	if len(p.Issues) == 0 {
		return nil
	}

	out := []string{"", t.Heading.Render("DRAFT CHECK")}

	for _, is := range p.Issues {
		text := is.Field.String() + ": " + pinEscapedLine(is.Text)
		if is.Index >= 0 {
			text = is.Field.String() + " (row " + strconv.Itoa(is.Index+1) + "): " + pinEscapedLine(is.Text)
		}

		if is.Info {
			out = append(out, propLines(t.Muted, text, w)...)
		} else {
			out = append(out, propNote(t, ui.ToneAttention, text, w)...)
		}
	}

	return out
}

// factLines names the operation, the file and its replacement, the
// entry, and the relationship.
func (r *pinReview) factLines(w int) []string {
	t := r.st.theme
	p := r.preview

	entry := "new, after the last pin"
	if p.Request.Op != maintain.AuthorCreate {
		entry = r.where
		if entry == "" {
			entry = "the reviewed entry"
		}
	}

	rows := [][2]string{
		{"Operation", p.Request.Op.String() + " a pin"},
		{"File", pinfile.File + ". " + pinReplaceNote},
		{"Entry", entry},
	}

	if p.Request.Op != maintain.AuthorCreate {
		rows = append(rows, [2]string{"Link", r.linkText()})
	}

	return append([]string{""}, propKV(t, rows, w)...)
}

// linkText names the relationship of the entry to the ledger.
func (r *pinReview) linkText() string {
	p := r.preview

	switch {
	case !p.LinksKnown:
		return "unknown: no readable ledger of this workspace"
	case p.Link == pinfile.LinkLinked && p.Proposal != nil:
		return "linked to " + propID(p.Proposal.ID)
	case p.Link == pinfile.LinkNoProposal:
		return "no proposal"
	}

	return ui.OneLine(p.Link.Reason())
}

// guidanceLines shows the guidance before and after, each with its
// marker, so the change reads without color.
func (r *pinReview) guidanceLines(w int) []string {
	t := r.st.theme
	p := r.preview
	op := p.Request.Op

	out := []string{"", t.Heading.Render("GUIDANCE") + "  " + t.Faint.Render(pinGuidanceLabel)}

	line := func(sign string, tone ui.Tone, pin reviews.PinRule) {
		out = append(out, propIndent(t.ToneStyle(tone).Render(sign)+" ", propLines(t.Body, pinGuidance(t, pin), w-2))...)
	}

	if op != maintain.AuthorCreate {
		line(t.Glyphs.Del, ui.ToneFailure, p.Old)
	}

	switch {
	case op == maintain.AuthorRemove:
	case p.New.Rule == "" && p.New.Note == "":
		out = append(out, propLines(t.Muted, "No new guidance: the draft has items to fix.", w)...)
	default:
		line(t.Glyphs.Add, ui.ToneSuccess, p.New)
	}

	if len(p.Missing) > 0 {
		out = append(out, propLines(t.Muted, "No file yet: "+maintRegionText(pinEscapedSet(p.Missing))+". A pin can name a future "+
			"file; it applies once the file exists.", w)...)
	}

	return out
}

// changedLines shows the current value next to the draft for each field
// that the draft edits, after a rebind by key alone (contracts §4.2).
func (r *pinReview) changedLines(w int) []string {
	t := r.st.theme
	p := r.preview

	if r.draft == nil || !r.draft.changed {
		return nil
	}

	req := p.Request
	out := []string{"", t.Heading.Render("ENTRY CHANGED") + "  " + t.Faint.Render("since the draft started")}

	pair := func(label, now, draft string) {
		out = append(out, integLabeled(t, label, maintLabelW, "now: "+now, t.Muted, w)...)
		out = append(out, integLabeled(t, "", maintLabelW, "draft: "+draft, t.Body, w)...)
	}

	if req.RuleSet {
		pair("Rule", pinEscapedLine(p.Old.Rule), pinEscapedLine(req.Rule))
	}

	if req.NoteSet {
		pair("Note", pinEscaped(p.Old.Note), pinEscaped(req.Note))
	}

	if req.ScopeSet {
		draft := maintRegionText(pinEscapedSet(req.Regions))
		if req.Scope == maintain.ScopeRepoWide {
			draft = deltaRepoWide
		}

		pair("Scope", maintRegionText(pinEscapedSet(p.Old.AllRegions())), draft)
	}

	if !req.RuleSet && !req.NoteSet && !req.ScopeSet {
		out = append(out, propLines(t.Muted, "The draft edits no field, so every field shows its current value.", w)...)
	}

	return out
}

// historyLines lists the superseded and dismissed rows of the key. They
// are history: no row is the source of an authored pin.
func (r *pinReview) historyLines(w int) []string {
	t := r.st.theme
	p := r.preview

	if len(p.History) == 0 {
		return nil
	}

	out := []string{"", t.Heading.Render("HISTORY") + "  " + t.Faint.Render("not the source of this pin")}

	for _, pr := range p.History {
		line := t.Muted.Render(ui.PadRight(propID(pr.ID), 5)) + " " + pinStatusWord(t, pr.Status) + "  " +
			t.Body.Render(pinEscapedLine(pr.Rule))
		out = append(out, ui.Truncate(line, w, t.Glyphs.Ellipsis))
	}

	return append(out, propLines(t.Muted, "Earlier proposals of this rule and scope stay as they are. The review "+
		"never adopts one of them.", w)...)
}

// noteLines shows the statements of the plan: the identity texts, the
// texts of a linked pin, and the ledger effect (contracts §5).
func (r *pinReview) noteLines(w int) []string {
	t := r.st.theme

	if len(r.preview.Notes) == 0 {
		return nil
	}

	out := []string{"", t.Heading.Render("EFFECTS")}

	for _, n := range r.preview.Notes {
		out = append(out, propLines(t.Body, ovSentenceEnd(pinEscapedLine(n)), w)...)
	}

	return out
}

// diffLines renders the exact file change with its line counts.
func (r *pinReview) diffLines(w int) []string {
	t := r.st.theme
	p := r.preview

	if p.After == nil {
		return append([]string{"", t.Heading.Render("DIFF")},
			propLines(t.Muted, "No file change: the draft has items to fix, or a blocker stops the plan.", w)...)
	}

	head := t.Heading.Render("DIFF") + "  " + maintCounts(t, p.Added, p.Removed)
	if !p.Confirmable {
		head += "  " + t.Faint.Render("preview only")
	}

	// The diff holds the bytes of the entry, so a refused character gets
	// its escape there too. The escape keeps the lines and the tabs.
	return append([]string{"", head}, setupDiffLines(t, pinEscapeRunes(p.Diff), w)...)
}

// planTail is the summary under the rule: the operation and what y
// does, or why y is off. The minimum layout keeps it while the body
// scrolls.
func (r *pinReview) planTail(w int) []string {
	t := r.st.theme
	p := r.preview
	sep := " " + t.Glyphs.Sep + " "
	op := pinOpTitle(p.Request.Op)

	switch {
	case p.Confirmable && r.st.busy():
		return propNote(t, ui.ToneAttention, op+sep+"y waits: another operation is running", w)
	case p.Confirmable:
		return propLines(t.Body, op+sep+"y writes "+pinfile.File+sep+"the ledger stays", w)
	}

	return propNote(t, ui.ToneAttention, op+sep+"y is off: "+r.offReason(), w)
}

// offReason names the first reason that y is off, in short words.
func (r *pinReview) offReason() string {
	p := r.preview

	switch {
	case p.NoOp:
		return "the draft changes nothing"
	case maintain.Blocking(p.Issues):
		return "the draft has items to fix"
	case len(p.Blockers) > 0:
		return pinBlockWords(p.Blockers[0], p.GateErr, r.draft == nil)
	case !p.LinksKnown:
		return "the relationship is unknown"
	}

	return "the review cannot be confirmed"
}

// pinBlockWords names a blocker in a few words by its kind. removal is
// true for a review without a draft, so its entry blocker names the
// read of Pins instead.
func pinBlockWords(b maintain.AuthorBlocker, gateErr error, removal bool) string {
	switch b.Kind {
	case maintain.BlockEntry:
		if removal {
			return "the pin changed since Pins read it"
		}

		return "the pin changed since the draft started"
	case maintain.BlockRelation:
		return "the link to the ledger forbids it"
	case maintain.BlockOwner:
		return "another pin or proposal has this rule and scope"
	case maintain.BlockLedger:
		return "no readable index"
	case maintain.BlockForeignRoot:
		return "the index records another repository"
	case maintain.BlockGate:
		if gateErr != nil {
			return "distill.write cannot be read"
		}

		return "distill.write is off"
	}

	return "the pin file cannot be changed"
}

// pinSentence ends a blocker text with a full stop. It keeps the case of
// the first word, because a blocker can start with a key such as
// distill.write.
func pinSentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}

	return s
}

// resultView reports what a commit did to the file. The title never
// claims a change that did not happen, and every result says that
// nothing is retried (P4-18).
func (r *pinReview) resultView(w int) ui.ModalOpts {
	t := r.st.theme
	o := r.out

	if o.err != nil {
		return r.refusalView(w)
	}

	title, tone, lead, tail := r.outcome()

	body := propNote(t, tone, lead, w)
	body = append(body, "", t.Heading.Render("WHAT HAPPENED"))
	body = append(body, propKV(t, r.resultFacts(), w)...)

	// The write landed, but the reload of the pins failed. The row says
	// so; the commit never runs again (P4-18).
	body = append(body, o.check.row(t, w)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return ui.ModalOpts{
		Title: t.ToneStyle(tone).Render(title), Body: body, Tail: propNote(t, tone, tail, w), Actions: r.resultActions(),
	}
}

// outcome titles a commit outcome and states what holds now and the
// next step.
func (r *pinReview) outcome() (title string, tone ui.Tone, lead, tail string) {
	t := r.st.theme
	res := r.out.res
	op := r.out.plan.Request.Op
	sep := " " + t.Glyphs.Sep + " "
	again := "The draft stays; press enter for a fresh review."

	if r.draft == nil {
		again = "Press enter to review the removal again."
	}

	switch res.Outcome {
	case maintain.Written:
		title, lead = "Pin written", "Added the new pin after the last pin of "+pinfile.File+"."

		switch op {
		case maintain.AuthorEdit:
			title, lead = "Pin edited", "Changed "+r.whereOr("the pin")+" in place in "+pinfile.File+"."
		case maintain.AuthorRemove:
			title, lead = "Pin removed", "Removed "+r.whereOr("the pin")+" from "+pinfile.File+"."
		}

		return title, ui.ToneSuccess, lead + " The ledger did not change. Review the diff and commit it.",
			"File written" + sep + "ledger unchanged"
	case maintain.Unknown:
		return "Result unknown", ui.ToneAttention, unknownOutcomeText(pinfile.File) + " The write failed, and " +
				"reading the file again failed too, so Seamark cannot say what it holds. " + again,
			"Result unknown" + sep + "enter reviews again"
	}

	return "Not written", ui.ToneFailure, "The write failed, and " + pinfile.File + " still holds its earlier " +
		"bytes. " + again, "File unchanged" + sep + "enter reviews again"
}

// whereOr returns the entry of the review, or fallback.
func (r *pinReview) whereOr(fallback string) string {
	if r.where == "" {
		return fallback
	}

	return r.where
}

// resultFacts lists the file, the error and the ledger on separate rows.
func (r *pinReview) resultFacts() [][2]string {
	res := r.out.res

	var rows [][2]string

	switch res.Outcome {
	case maintain.Written:
		rows = append(rows, [2]string{"Pin file", "written: " + pinfile.File})
	case maintain.Unknown:
		rows = append(rows, [2]string{"Pin file", "unknown: check " + pinfile.File})
	default:
		rows = append(rows, [2]string{"Pin file", "not changed"})
	}

	if res.Err != nil {
		label := "Error"
		if res.Outcome == maintain.Written {
			label = "Write error"
		}

		rows = append(rows, [2]string{label, maintErrText(res.Err)})
	}

	return append(rows, [2]string{"Ledger", "no change: pin authoring never writes the proposal ledger"})
}

// refusalView reports a commit that the engine refused before the
// write. A stale review lists what changed, and enter reviews again.
func (r *pinReview) refusalView(w int) ui.ModalOpts {
	t := r.st.theme
	err := r.out.err
	sep := " " + t.Glyphs.Sep + " "

	body := propNote(t, ui.ToneAttention, maintRefusal(t, err, "review", ""), w)

	if diffs := pinStale(err); len(diffs) > 0 {
		body = append(body, "", t.Heading.Render("WHAT CHANGED"))

		for _, d := range diffs {
			body = append(body, propIndent(t.Faint.Render(t.Glyphs.Sep)+" ", propLines(t.Body, pinEscapedLine(d), w-2))...)
		}
	} else {
		body = append(body, "")
		body = append(body, propLines(t.Muted, "Details: "+ui.ErrText(err), w)...)
	}

	next := "Nothing was written"

	if r.reviewAgain() {
		body = append(body, "")
		body = append(body, propLines(t.Body, ovSentence(r.againText()), w)...)
		next += sep + "enter reviews again"
	}

	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return ui.ModalOpts{
		Title: t.Attention.Render("Not written"), Body: body, Actions: r.resultActions(),
		Tail: propNote(t, ui.ToneAttention, next, w),
	}
}

// againText says what enter does after a refusal.
func (r *pinReview) againText() string {
	if r.draft == nil {
		return "press enter to review the removal again from the current state."
	}

	return "the draft stays. Press enter for a new review from the current state."
}
