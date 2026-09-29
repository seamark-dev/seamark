package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
)

// ovListW is the width of the Attention box at 120 columns. A wider
// terminal gives the extra columns to the Detail box, as in the 1a
// design; a narrower wide terminal keeps the same 8:7 split.
const ovListW = 64

// ovRowKind is the kind of one line of the Attention box.
type ovRowKind int

const (
	// ovRowText is a heading, a note, a client or doctor line, or a
	// blank line. The cursor skips it.
	ovRowText ovRowKind = iota
	// ovRowItem is a selectable attention item.
	ovRowItem
	// ovRowRule is the full-width rule above the client lines.
	ovRowRule
	// ovRowDoctor is the line of doctor counts. It renders at view time,
	// because it picks a shorter wording in a narrow box.
	ovRowDoctor
)

// ovRow is one line of the Attention box. item is the index of the
// attention item for an item row. text is the styled line of a text row;
// item rows render at view time, because the selection changes them.
type ovRow struct {
	kind ovRowKind
	text string
	item int
}

// view renders the Overview. The wide layout shows the cards above the
// Attention and Detail boxes; the narrow layout shows the one-line
// summary above one box, and enter shows the detail in its place.
func (o *overview) view(w, h int) string {
	wide := w >= wideWidth

	top, topH := o.summaryLine(w), 1
	if wide && h >= ovCardsMinBody {
		top, topH = o.cardsView(w), o.cardsHeight(w)
	}

	// One blank line under the summary and one above the footer, as in
	// the 1a design.
	boxH := max(3, h-topH-2)
	head := top + "\n\n"

	if o.freshEmpty() {
		return head + o.freshView(w, boxH)
	}

	items := o.attention()
	rows := o.rows(items)
	sel := o.sync(rows, items)

	if !wide {
		if o.detailOpen && sel >= 0 {
			return o.detailBox(&items[sel], w, max(3, h-1), true)
		}

		return head + o.listBox(rows, items, w, boxH, true, o.st.theme.Faint.Render("enter for detail"))
	}

	leftW := min(ovListW, w*8/15)
	listFocus := o.focus == ovFocusList
	left := o.listBox(rows, items, leftW, boxH, listFocus, "")

	var it *ovItem
	if sel >= 0 {
		it = &items[sel]
	}

	return head + hjoin(1, left, o.detailBox(it, w-leftW-1, boxH, !listFocus))
}

// rows lays out the attention items under their group headings, then
// the client lines and the doctor summary under a rule.
func (o *overview) rows(items []ovItem) []ovRow {
	t := o.st.theme

	var rows []ovRow

	note := func(text string) { rows = append(rows, ovRow{kind: ovRowText, text: text}) }

	for _, p := range o.pendingSources() {
		note(t.Accent.Render(t.spinnerFrame(o.st.tick)) + " " + t.Muted.Render(p+t.Glyphs.Ellipsis))
	}

	byGroup := [ovGroupCount][]int{}
	for i, it := range items {
		byGroup[it.group] = append(byGroup[it.group], i)
	}

	// The summary item needs no heading: its title names it, and a
	// heading would cost a list row in a short terminal.
	for _, i := range byGroup[ovSummary] {
		rows = append(rows, ovRow{kind: ovRowItem, item: i})
	}

	for g := ovActionable; g < ovGroupCount; g++ {
		idxs := byGroup[g]

		// An empty Action group is a result worth stating once every
		// source has loaded. R-4 keeps the other groups apart, and an
		// empty one says nothing, so it stays hidden.
		empty := len(idxs) == 0
		if empty && (g != ovActionable || !o.settled()) {
			continue
		}

		if len(rows) > 0 {
			note("")
		}

		style := t.Heading
		if g == ovActionable {
			style = t.Attention.Bold(true)
		}

		note(style.Render(strings.ToUpper(ovGroupNames[g])) + "  " + t.Faint.Render(itoa(len(idxs))))

		if empty {
			note("  " + t.Badge(ToneSuccess, "nothing actionable found"))
		}

		for _, i := range idxs {
			rows = append(rows, ovRow{kind: ovRowItem, item: i})
		}
	}

	if len(rows) > 0 {
		note("")
	}

	rows = append(rows, ovRow{kind: ovRowRule})
	note(t.Heading.Render("CLIENTS"))

	for _, l := range o.clientLines() {
		note(l)
	}

	note("")

	return append(rows, o.doctorRows()...)
}

// renderRow renders one row at w cells. The selected item row gets the
// cursor glyph, a bold title and, in the focused pane, the selection
// background over the full width.
func (o *overview) renderRow(r ovRow, items []ovItem, selected, focused bool, w int) string {
	t := o.st.theme

	switch r.kind {
	case ovRowRule:
		return t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, w))
	case ovRowDoctor:
		return o.doctorCounts(w)
	case ovRowText:
		return r.text
	}

	it := items[r.item]

	marker, title := "  ", t.Body.Render(oneLine(it.title))
	if selected {
		marker, title = t.Key.Render(t.Glyphs.Cursor)+" ", t.Title.Render(oneLine(it.title))
	}

	left := marker + o.itemGlyph(it) + " " + title

	// The hint gives way to the title: a row that cannot hold both keeps
	// the whole title, and the detail names the next step anyway.
	line := left
	if hint := o.rowHint(it); hint != "" && width(left)+width(hint)+2 <= w {
		line = spread(left, hint+" ", w)
	}

	if selected && focused {
		return t.highlight(line, w, bgSel)
	}

	return line
}

// itemState returns the glyph and the style of an item: the glyph of
// its group, in the failure color when the item is broken. A neutral
// glyph is dim, as the optional "·" of the 1a design.
func (o *overview) itemState(it ovItem) (glyph string, style lipgloss.Style) {
	t := o.st.theme

	tone := ovGroupTones[it.group]

	switch {
	case it.group == ovSummary:
		tone = it.tone
	case it.tone == ToneFailure:
		tone = ToneFailure
	}

	style = t.ToneStyle(tone)
	if tone == ToneNeutral {
		style = t.Muted
	}

	return t.ToneGlyph(tone), style
}

// itemGlyph renders the state glyph of an item.
func (o *overview) itemGlyph(it ovItem) string {
	glyph, style := o.itemState(it)

	return style.Render(glyph)
}

// rowHint names where enter leads, for the rows of the Action and
// Optional groups: "b build" or the destination with an arrow.
func (o *overview) rowHint(it ovItem) string {
	t := o.st.theme

	if it.group != ovActionable && it.group != ovOptional {
		return ""
	}

	switch it.act.kind {
	case ovActBuild:
		return t.Faint.Render("b build")
	case ovActNavigate:
		return t.Faint.Render(ovDestName(it.act.dest) + " " + t.Glyphs.Arrow)
	}

	return ""
}

// ovDestName returns the tab name of a destination.
func ovDestName(d destination) string {
	switch d {
	case destIntegrations:
		return "Integrations"
	case destLessons:
		return "Lessons"
	case destDoctor:
		return "Doctor"
	default:
		return "Overview"
	}
}

// listBox renders the Attention box. A list longer than the box scrolls
// with the cursor, and the bottom border shows the visible range.
func (o *overview) listBox(rows []ovRow, items []ovItem, w, h int, focused bool, foot string) string {
	t := o.st.theme
	inner, n := boxInner(w), boxRows(h)
	o.listH = n

	selectable := false

	for _, r := range rows {
		if r.kind == ovRowItem {
			selectable = true

			break
		}
	}

	off := 0
	if selectable {
		off = o.listOffset(rows, n)
	}

	end := min(len(rows), off+n)
	lines := make([]string, 0, end-off)

	for i := off; i < end; i++ {
		lines = append(lines, o.renderRow(rows[i], items, selectable && i == o.list.cursor, focused, inner))
	}

	if len(rows) > n {
		pos := o.position(off+1, end, len(rows))
		if foot != "" {
			pos += t.Faint.Render(" " + t.Glyphs.Sep + " ")
		}

		foot = pos + foot
	}

	right := ""
	if count, ok := o.attentionCount(); ok {
		right = t.Muted.Render(itoa(count))
	}

	return t.box(boxOpts{W: w, H: h, Title: t.Title.Render("Attention"), Right: right, Foot: foot, Focus: focused, Lines: lines})
}

// listOffset returns the first visible row for a window of n rows. The
// heading above the cursor stays in view, so a selected row never
// appears without its group name. On the first item the window starts
// at the top, and on the last item it ends at the bottom, so the notes
// above and the client and doctor lines below stay reachable.
func (o *overview) listOffset(rows []ovRow, n int) int {
	o.list.clamp(len(rows), n)

	first, last := -1, -1

	for i, r := range rows {
		if r.kind == ovRowItem {
			if first < 0 {
				first = i
			}

			last = i
		}
	}

	switch o.list.cursor {
	case first:
		o.list.offset = 0
	case last:
		o.list.offset = max(o.list.offset, len(rows)-n)
	}

	top := o.list.cursor
	for top > 0 && rows[top-1].kind != ovRowItem && o.list.cursor-top < 2 && o.list.cursor-top+1 < n {
		top--
	}

	o.list.offset = max(0, min(o.list.offset, top))

	return o.list.offset
}

// position renders the "first–last of total" note of a scrolled box.
func (o *overview) position(first, last, total int) string {
	t := o.st.theme

	dash := "–"
	if t.ASCII {
		dash = "-"
	}

	return t.Faint.Render(itoa(first) + dash + itoa(last) + " of " + itoa(total))
}

// clientLines renders one line per client: the configuration glyph, the
// name, and what Seamark configured, from the typed inspection.
func (o *overview) clientLines() []string {
	t := o.st.theme
	sec := &o.st.integrations

	if !sec.loaded {
		if sec.err != nil {
			return []string{"  " + t.Unknown.Render("clients unknown: "+errText(sec.err))}
		}

		return []string{"  " + o.reading("the integrations")}
	}

	if len(sec.value) == 0 {
		return []string{"  " + t.Muted.Render("no registered clients")}
	}

	nameW := 0
	for _, insp := range sec.value {
		nameW = max(nameW, width(oneLine(ovClientName(insp))))
	}

	lines := make([]string, 0, len(sec.value)+1)

	for _, insp := range sec.value {
		glyph := t.Faint.Render(t.Glyphs.Hollow)
		if tone, _ := ovClientState(insp); tone != ToneNeutral {
			glyph = t.ToneStyle(tone).Render(t.ToneGlyph(tone))
		}

		lines = append(lines, "  "+glyph+" "+t.Body.Render(padRight(oneLine(ovClientName(insp)), nameW))+"  "+
			o.clientCaps(insp))
	}

	if sec.stale() {
		lines = append(lines, "  "+o.staleNote(sec.err))
	}

	return lines
}

// ovCapWords are the short capability words of the client lines, in the
// order of the 1a design.
var ovCapWords = []struct {
	capability integration.Capability
	word       string
}{
	{integration.CapabilityCommands, "gate"},
	{integration.CapabilityEdits, "lessons"},
	{integration.CapabilityMCPRegistration, "MCP"},
	{integration.CapabilitySkills, "skills"},
	{integration.CapabilityToolGrants, "grants"},
}

// clientCaps lists what Seamark configured for a client: "gate warn ·
// lessons · MCP · skills · grants". A capability that is not current
// adds its state in the attention color; an absent one is left out,
// because its own attention item reports it.
func (o *overview) clientCaps(insp integration.Inspection) string {
	t := o.st.theme

	if ovNothingConfigured(insp) {
		return t.Muted.Render("not set up")
	}

	parts := make([]string, 0, len(ovCapWords))

	for _, cw := range ovCapWords {
		c, ok := insp.Entry(cw.capability)
		if !ok || !c.Supported || c.State == integration.StateAbsent {
			continue
		}

		switch {
		case c.State == integration.StateCurrent && cw.capability == integration.CapabilityCommands:
			parts = append(parts, t.Muted.Render("gate "+oneLine(ovGateWord(insp, c))))
		case c.State == integration.StateCurrent:
			parts = append(parts, t.Muted.Render(cw.word))
		case c.State == integration.StateUnreadable:
			parts = append(parts, t.Failure.Render(cw.word+" "+c.State.String()))
		default:
			parts = append(parts, t.Attention.Render(cw.word+" "+c.State.String()))
		}
	}

	return strings.Join(parts, t.Muted.Render(" "+t.Glyphs.Sep+" "))
}

// ovGateWord is the mode of an installed gate hook. It follows the rule
// of Inspection.DescribeHooks: the installed mode, "mode unknown" for a
// partial hook without a readable mode, and warn otherwise, so the
// client line and `seamark doctor` agree.
func ovGateWord(insp integration.Inspection, commands integration.CapabilityInspection) string {
	switch {
	case insp.GateMode != "":
		return insp.GateMode
	case commands.State == integration.StatePartial:
		return "mode unknown"
	default:
		return "warn"
	}
}

// doctorRows renders the doctor summary: the check count, then one
// line of counts per state. The Doctor tab lists every check.
func (o *overview) doctorRows() []ovRow {
	t := o.st.theme
	sec := &o.st.doctor
	head := t.Heading.Render("DOCTOR")

	text := func(s string) ovRow { return ovRow{kind: ovRowText, text: s} }

	switch {
	case !sec.loaded:
		return []ovRow{text(head), text("  " + t.Accent.Render(t.spinnerFrame(o.st.tick)) + " " +
			t.Muted.Render("running the doctor checks"+t.Glyphs.Ellipsis))}
	case sec.value == nil:
		return []ovRow{text(head), text("  " + t.Unknown.Render("doctor report unavailable"))}
	}

	rows := []ovRow{
		text(head + "  " + t.Faint.Render(ovPlural(len(sec.value.Checks), "check", "checks"))),
		{kind: ovRowDoctor},
	}

	if sec.stale() {
		rows = append(rows, text("  "+o.staleNote(sec.err)))
	}

	return rows
}

// doctorCounts renders the counts of the doctor report per state and
// the key of the Doctor tab: "✓ 13 ok   · 2 info   0 failing · 4 for
// all checks". A narrow box gets tighter gaps, then a shorter key label.
func (o *overview) doctorCounts(w int) string {
	t := o.st.theme
	ok, info, warn, fail := ovDoctorCounts(o.st.doctor.value)

	parts := []string{
		t.Success.Render(t.Glyphs.OK + " " + itoa(ok) + " ok"),
		t.Info.Render(t.Glyphs.Info + " " + itoa(info) + " info"),
	}

	if warn > 0 {
		parts = append(parts, t.Attention.Render(t.Glyphs.Warn+" "+itoa(warn)+" warn"))
	}

	if fail > 0 {
		parts = append(parts, t.Failure.Render(t.Glyphs.Fail+" "+itoa(fail)+" failing"))
	} else {
		parts = append(parts, t.Faint.Render("0 failing"))
	}

	sep := t.Faint.Render(" " + t.Glyphs.Sep + " ")
	key := t.Key.Render("4") + " "

	return ovFit(w,
		"  "+strings.Join(parts, "   ")+sep+key+t.Muted.Render("for all checks"),
		"  "+strings.Join(parts, "  ")+sep+key+t.Muted.Render("for all checks"),
		"  "+strings.Join(parts, "  ")+sep+key+t.Muted.Render("all checks"))
}

// detailBox renders the Detail box of one item, or of none. A detail
// longer than the box scrolls, and the bottom border shows the range.
func (o *overview) detailBox(it *ovItem, w, h int, focused bool) string {
	t := o.st.theme
	inner, n := boxInner(w), boxRows(h)

	title := t.Muted.Render("Detail")
	if focused {
		title = t.Title.Render("Detail")
	}

	var lines []string

	if it == nil {
		text := "Items appear here as the sources load."
		if len(o.pendingSources()) == 0 && o.st.doctor.loaded {
			text = "No item to show."
		}

		lines = strings.Split(t.Muted.Render(wrap(text, inner)), "\n")
	} else {
		lines = o.detailLines(*it, inner)
	}

	o.detailOff = max(0, min(o.detailOff, len(lines)-n))
	end := min(len(lines), o.detailOff+n)

	foot := ""
	if len(lines) > n {
		foot = o.position(o.detailOff+1, end, len(lines))
	}

	return t.box(boxOpts{W: w, H: h, Title: title, Foot: foot, Focus: focused, Lines: lines[o.detailOff:end]})
}

// detailLines renders the title, the status line, the prose, the
// sections and the next step of an item, each line at most w cells.
func (o *overview) detailLines(it ovItem, w int) []string {
	t := o.st.theme

	head := it.head
	if head == "" {
		head = it.title
	}

	lines := ovStyled(t.Title.Render, wrap(oneLine(head), w))
	lines = append(lines, o.statusLines(it, w)...)

	for _, p := range it.body {
		if p = diag(p); p != "" {
			lines = append(lines, "")
			lines = append(lines, ovStyled(t.Body.Render, wrap(p, w))...)
		}
	}

	for _, s := range it.sections {
		if text := diag(s.text); text != "" {
			lines = append(lines, "", t.Heading.Render(strings.ToUpper(s.title)))
			lines = append(lines, ovStyled(t.Body.Render, wrap(text, w))...)
		}
	}

	if it.summary {
		lines = append(lines, "")
		lines = append(lines, strings.Split(o.summaryDetail(w), "\n")...)
	}

	lines = append(lines, "", t.Heading.Render("NEXT"))

	return append(lines, o.nextLines(it, w)...)
}

// ovStyled renders each line of text with the style function, so every
// line keeps its own style when the caller splits the block.
func ovStyled(render func(...string) string, text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = render(l)
	}

	return lines
}

// statusLines renders the status of an item: the group glyph and word,
// then the underlying state and the source in the faint style. The
// source is left out when the title already names it.
func (o *overview) statusLines(it ovItem, w int) []string {
	t := o.st.theme

	word := strings.ToLower(ovGroupNames[it.group])
	if it.group == ovSummary {
		word = oneLine(it.badge)
	}

	glyph, style := o.itemState(it)
	badge := style.Render(glyph + " " + word)

	var meta []string
	if it.badge != "" && it.badge != word {
		meta = append(meta, it.badge)
	}

	if it.source != "" && it.source != it.head {
		meta = append(meta, it.source)
	}

	if len(meta) == 0 {
		return []string{badge}
	}

	pad := width(badge) + 2
	text := wrap(oneLine(strings.Join(meta, " "+t.Glyphs.Sep+" ")), max(10, w-pad))
	out := ovStyled(t.Faint.Render, text)

	out[0] = badge + "  " + out[0]
	for i := 1; i < len(out); i++ {
		out[i] = strings.Repeat(" ", pad) + out[i]
	}

	return out
}

// nextLines renders the next step: the in-app key when a workflow
// exists, then the corrective text as selectable text that the console
// never runs, else a statement that nothing is needed.
func (o *overview) nextLines(it ovItem, w int) []string {
	t := o.st.theme

	var lines []string

	switch it.act.kind {
	case ovActBuild:
		lines = append(lines, t.Key.Render("b")+" "+t.Body.Render(it.act.label))
	case ovActNavigate:
		lines = append(lines, t.Key.Render("enter")+" "+t.Body.Render(it.act.label))
	}

	fix := diag(it.fix)

	switch {
	case fix == "":
	case it.act.kind != ovActNone && strings.HasPrefix(fix, "seamark ") &&
		width(lines[0])+width(" · same as "+fix) <= w:
		// A plain seamark command does what the key does, so it fits
		// on the key line as the CLI route.
		lines[0] += t.Faint.Render(" "+t.Glyphs.Sep+" same as ") + t.command(fix)
	default:
		intro := "Seamark shows this and never runs it:"
		if it.act.kind != ovActNone {
			intro = "Or by hand:"
		}

		lines = append(lines, ovStyled(t.Faint.Render, wrap(intro, w))...)

		for _, l := range strings.Split(wrap(fix, max(10, w-2)), "\n") {
			lines = append(lines, "  "+t.command(l))
		}
	}

	if len(lines) == 0 {
		lines = append(lines, t.Success.Render("No action needed."))
	}

	return lines
}

// freshView renders the empty first-run state with its next actions,
// inside the Attention box.
func (o *overview) freshView(w, h int) string {
	t := o.st.theme
	inner, n := boxInner(w), boxRows(h)

	body := "Seamark found no index and no agent integration in this workspace. " +
		"Nothing has been written: opening the console only reads."

	if r := o.st.doctor.value; r != nil && r.Fails+r.Warns > 0 {
		body += " The doctor checks report " + ovPlural(r.Fails+r.Warns, "problem", "problems") + "; 4 shows them."
	}

	next := strings.Join([]string{
		t.action("s    ", "Set up an integration", true),
		t.action("b    ", "Build the index", false),
		t.action("enter", "Show the attention list", false),
	}, "\n")

	lines := strings.Split(t.empty(ToneNeutral, "Nothing is set up here yet", body, next, inner, n), "\n")

	return t.box(boxOpts{W: w, H: h, Title: t.Title.Render("Attention"), Focus: true, Lines: lines})
}
