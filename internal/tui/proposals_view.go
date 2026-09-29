package tui

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// This file renders the Lessons destination in the 1a layout: the
// sub-view line, the filter line, the list box and the detail box. The
// decision modals live in les_modal.go; the state and the key handling
// live in lessons.go.

// Pane geometry of the 1a Lessons screen at 120 columns: a list box of
// 62 cells, one cell of gap, and a detail box of 57 cells.
const (
	propListW    = 62
	propDesignW  = 120
	propMinRule  = 16
	propLabelW   = 10 // the label column of the detail facts
	propSummaryN = 3  // the most lines of one finding summary
)

// subTabs renders " Proposals   Stats   p / s" with the write gate at
// the right. The active sub-view is in the accent color and bold.
func (s *lessons) subTabs(w int) string {
	t := s.st.theme

	tab := func(label string, on bool) string {
		if on {
			return t.TabActive.Render(label)
		}

		return t.Muted.Render(label)
	}

	left := " " + tab("Proposals", s.sub == lesProposals) + "   " + tab("Stats", s.sub == lesStats)

	if !s.capturing() {
		left += "   " + t.Faint.Render("p / s")
	}

	right := s.props.gateLabel()
	if right != "" {
		right += " "
	}

	if width(left)+width(right) > w {
		return truncate(left, w, t.Glyphs.Ellipsis)
	}

	return spread(left, right, w)
}

// gateLabel shows the distill.write gate, because it decides whether
// apply writes pins or only shows the YAML block. An unread gate shows
// nothing, and an unreadable gate says so.
func (p *propView) gateLabel() string {
	t := p.st.theme

	l := p.ledger()
	if l == nil {
		return ""
	}

	label := t.Faint.Render("distill.write") + " "

	switch {
	case l.WriteErr != nil:
		return label + t.Attention.Render(t.Glyphs.Warn+" unreadable")
	case l.Write:
		return label + t.Muted.Render("on")
	default:
		return label + t.Muted.Render("off")
	}
}

// view renders the filter line, a blank line and the boxes. The shell
// pads the last line, which keeps the blank row above the footer.
func (p *propView) view(w, h int) string {
	t := p.st.theme
	p.wide = w >= wideWidth

	head := p.filterBar(w)

	if sec := &p.st.ledger; sec.stale() {
		head = append(head, " "+t.Attention.Render(truncate(t.Glyphs.Warn+" stale: "+errText(sec.err)+
			". The list shows the last good snapshot.", w-1, t.Glyphs.Ellipsis)))
	}

	boxH := max(4, h-len(head)-2)

	return strings.Join(head, "\n") + "\n\n" + p.body(w, boxH)
}

// filterBar renders " state [pending 6] applied 1  dismissed 0     region
// all     1 selected". The active state has brackets as well as the
// accent, so it stays visible without color. It takes two lines when
// one line is too narrow.
func (p *propView) filterBar(w int) []string {
	t := p.st.theme
	l := p.ledger()

	var states strings.Builder

	for f := propPending; f < propStateCount; f++ {
		label := f.label()
		if l != nil {
			label += " " + itoa(propCount(l, f))
		}

		if f > propPending {
			// One space beside the brackets, two between plain labels.
			if f == p.filter || f-1 == p.filter {
				states.WriteString(" ")
			} else {
				states.WriteString("  ")
			}
		}

		if f == p.filter {
			states.WriteString(t.TabActive.Render("[" + label + "]"))
		} else {
			states.WriteString(t.Muted.Render(label))
		}
	}

	stateSeg := t.Faint.Render("state") + " " + states.String()

	var region string

	switch {
	case p.editing:
		region = p.input.view(t)
	case p.region == "":
		region = t.Muted.Render("all")
	default:
		region = t.Body.Render(oneLine(p.region))
	}

	rest := t.Faint.Render("region") + " " + region

	if n := len(p.selected); n > 0 && p.filter == propPending {
		rest += "     " + t.Marked.Render(itoa(n)+" selected")
	}

	if one := " " + stateSeg + "     " + rest; width(one) <= w {
		return []string{one}
	}

	return []string{
		" " + truncate(stateSeg, w-1, t.Glyphs.Ellipsis),
		" " + truncate(rest, w-1, t.Glyphs.Ellipsis),
	}
}

// propCount returns the number of proposals in one state.
func propCount(l *proposals.Ledger, f propState) int {
	switch f {
	case propApplied:
		return len(l.Applied)
	case propDismissed:
		return len(l.Dismissed)
	default:
		return len(l.Pending)
	}
}

// listTitle names the list box after the state filter, for example
// "Pending proposals".
func (p *propView) listTitle() string {
	label := p.filter.label()

	return strings.ToUpper(label[:1]) + label[1:] + " proposals"
}

// propPaneTitle styles a box title: the focused pane in the title style,
// the other panes muted, as in the 1a design.
func propPaneTitle(t *Theme, title string, focused bool) string {
	if focused {
		return t.Title.Render(title)
	}

	return t.Muted.Render(title)
}

// body renders the loading, unavailable and empty states, or the list
// and detail boxes.
func (p *propView) body(w, h int) string {
	sec := &p.st.ledger

	if !sec.loaded {
		return p.unavailableBox(sec.err, w, h)
	}

	rows := p.rows()
	if len(rows) == 0 {
		return p.emptyBox(w, h)
	}

	if p.wide {
		listW := min(propListW, w*propListW/propDesignW)

		return hjoin(1, p.listBox(rows, listW, h, !p.focusDetail),
			p.detailBox(rows, w-listW-1, h, p.focusDetail))
	}

	if p.focusDetail {
		return p.detailBox(rows, w, h, true)
	}

	return p.listBox(rows, w, h, true)
}

// propRange renders the "3–12 of 40" position of a scrolled window.
// The ASCII glyph set gets a plain hyphen, because a view that renders
// outside the shell skips the frame-wide ASCII swap.
func propRange(t *Theme, from, to, n int) string {
	dash := "–"
	if t.ASCII {
		dash = "-"
	}

	return t.Faint.Render(itoa(from+1) + dash + itoa(to) + " of " + itoa(n))
}

// listBox renders the proposal list with its column header. The cursor
// row has the cursor glyph, and in the focused box a full-width
// selection background.
func (p *propView) listBox(rows []domain.Proposal, w, h int, focused bool) string {
	t := p.st.theme
	inner := boxInner(w)
	cols := p.columns(rows, inner)

	window := max(1, boxRows(h)-1)
	p.listH = window
	p.list.clamp(len(rows), window)

	lines := []string{cols.header(t)}
	end := min(len(rows), p.list.offset+window)

	for i := p.list.offset; i < end; i++ {
		pr := rows[i]
		cursor := i == p.list.cursor

		line := cols.row(t, pr, p.selected[pr.ID], cursor, p.tierOf(pr.ID))
		if cursor && focused {
			line = t.highlight(line, inner, bgSel)
		}

		lines = append(lines, line)
	}

	var foot string

	switch {
	case len(rows) > window:
		foot = propRange(t, p.list.offset, end, len(rows))
	case !p.wide:
		foot = t.Faint.Render("enter for detail")
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused, Lines: lines, Foot: foot,
		Title: propPaneTitle(t, p.listTitle(), focused), Right: t.Muted.Render(itoa(len(rows))),
	})
}

// propCols is the column layout of the proposal list: marker 2, check
// 4, ID 4, RULE, REGION 10, CITED 6 and TIER 4 in the 1a design. A zero
// width hides an optional column.
type propCols struct {
	check  bool
	idW    int
	ruleW  int
	regW   int
	citedW int
	tierW  int
}

// columns fits the list columns into w cells. Narrow boxes drop the
// optional columns, tier first, so the rule keeps a readable width;
// the detail box shows every dropped value.
func (p *propView) columns(rows []domain.Proposal, w int) propCols {
	c := propCols{check: p.filter == propPending, idW: 4, regW: 10, citedW: 6}

	for _, pr := range rows {
		c.idW = max(c.idW, width(propID(pr.ID))+2)
	}

	if l := p.ledger(); l != nil && l.HealthErr == nil && len(l.Health) > 0 {
		c.tierW = 4

		for _, pr := range rows {
			c.tierW = max(c.tierW, min(6, width(p.tierOf(pr.ID))))
		}
	}

	fixed := 2 + c.idW + c.regW + c.citedW + c.tierW
	if c.check {
		fixed += 4
	}

	c.ruleW = w - fixed

	for _, col := range []*int{&c.tierW, &c.citedW, &c.regW} {
		if c.ruleW >= propMinRule {
			break
		}

		c.ruleW += *col
		*col = 0
	}

	c.ruleW = max(4, c.ruleW)

	return c
}

// cells returns the row cells in column order.
func (c propCols) cells(marker, check, id, rule, region, cited, tier string) []colCell {
	out := []colCell{cell(marker, 2)}

	if c.check {
		out = append(out, cell(check, 4))
	}

	out = append(out, cell(id, c.idW), cell(rule, c.ruleW))

	for _, opt := range []colCell{cell(region, c.regW), cell(cited, c.citedW), cell(tier, c.tierW)} {
		if opt.w > 0 {
			out = append(out, opt)
		}
	}

	return out
}

// header renders the faint column titles.
func (c propCols) header(t *Theme) string {
	return t.Faint.Render(t.cols(c.cells("", "", "ID", "RULE", "REGION", "CITED", "TIER")...))
}

// row renders one proposal. The cursor row has the cursor glyph and a
// bold rule; a marked row has the check glyph in the accent color.
func (c propCols) row(t *Theme, pr domain.Proposal, marked, cursor bool, tier string) string {
	marker := ""
	rule := t.Body

	if cursor {
		marker = t.Key.Render(t.Glyphs.Cursor)
		rule = t.Title
	}

	check := t.Faint.Render(t.Glyphs.Uncheck)
	if marked {
		check = t.Marked.Render(t.Glyphs.Check)
	}

	return t.cols(c.cells(marker, check, t.Muted.Render(propID(pr.ID)), rule.Render(oneLine(pr.Rule)),
		t.Muted.Render(propRegionLabel(pr)), t.Body.Render("  "+itoa(len(pr.Members))), t.Muted.Render(tier))...)
}

// propID renders a proposal ID in the ledger's pN form.
func propID(id int64) string {
	return "p" + strconv.FormatInt(id, 10)
}

// propRegionLabel is the short region label of a list row. A set of
// several regions shows its size; the detail box lists them.
func propRegionLabel(pr domain.Proposal) string {
	set := pr.RegionSet()

	switch len(set) {
	case 0:
		return "repo-wide"
	case 1:
		return oneLine(set[0])
	default:
		return itoa(len(set)) + " regions"
	}
}

// tierOf returns the evidence tier of a proposal, or "".
func (p *propView) tierOf(id int64) string {
	l := p.ledger()
	if l == nil {
		return ""
	}

	return oneLine(l.Health[id].Tier)
}

// detailBox renders the proposal under the cursor. The body scrolls
// inside the box, and a different proposal starts at the top.
func (p *propView) detailBox(rows []domain.Proposal, w, h int, focused bool) string {
	t := p.st.theme
	cur, _ := p.current(rows)
	body := p.detailLines(cur, boxInner(w))

	if p.detailID != cur.ID {
		p.detail.off = 0
		p.detailID = cur.ID
	}

	window := boxRows(h)
	p.detail.page = window
	p.detail.last = max(0, len(body)-window)
	p.detail.by(0)

	sep := " " + t.Glyphs.Sep + " "

	var foot string

	switch {
	case p.detail.off < p.detail.last:
		foot = t.Faint.Render("↓ more" + sep + "pgdn")
	case p.detail.off > 0:
		foot = t.Faint.Render("↑ more" + sep + "pgup")
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused, Foot: foot,
		Title: propPaneTitle(t, "Proposal "+propID(cur.ID), focused),
		Lines: body[p.detail.off:min(len(body), p.detail.off+window)],
	})
}

// propDash returns the dash of the theme's glyph set.
func propDash(t *Theme) string {
	if t.ASCII {
		return " - "
	}

	return " — "
}

// propLines wraps text to w cells and styles each line.
func propLines(style lipgloss.Style, text string, w int) []string {
	lines := strings.Split(wrap(text, max(1, w)), "\n")
	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

// propNote renders a tone glyph and wrapped text with a hanging indent.
// The glyph keeps the tone visible without color.
func propNote(t *Theme, tone Tone, text string, w int) []string {
	style := t.ToneStyle(tone)
	lines := strings.Split(wrap(text, max(1, w-2)), "\n")

	for i, l := range lines {
		prefix := "  "
		if i == 0 {
			prefix = style.Render(t.ToneGlyph(tone)) + " "
		}

		lines[i] = prefix + style.Render(l)
	}

	return lines
}

// propIndent prefixes the first line with lead and the other lines
// with spaces of the same width.
func propIndent(lead string, lines []string) []string {
	pad := strings.Repeat(" ", width(lead))
	out := make([]string, len(lines))

	for i, l := range lines {
		if i == 0 {
			out[i] = lead + l
		} else {
			out[i] = pad + l
		}
	}

	return out
}

// propKV renders aligned label and value rows as lines.
func propKV(t *Theme, rows [][2]string, w int) []string {
	return strings.Split(t.kv(rows, w), "\n")
}

// detailLines renders the full proposal: rule and state, the note,
// the regions and provenance, the cited evidence, the advisories and
// the available decisions.
func (p *propView) detailLines(pr domain.Proposal, w int) []string {
	t := p.st.theme
	l := p.ledger()

	out := p.headLines(pr, w)
	out = append(out, "", t.Heading.Render("RULE"))

	if strings.TrimSpace(pr.Note) == "" {
		out = append(out, t.Muted.Render("No note is recorded."))
	} else {
		out = append(out, propLines(t.Body, multiLine(pr.Note), w)...)
	}

	out = append(out, "")
	out = append(out, p.factLines(pr, w)...)
	out = append(out, "")
	out = append(out, p.evidenceLines(pr, l, w)...)

	if adv := p.advisoryLines(pr, l, w); len(adv) > 0 {
		out = append(out, "")
		out = append(out, adv...)
	}

	out = append(out, "")

	return append(out, p.decisionLines(pr, l, w)...)
}

// headLines renders the rule with the ID and the status line. Only
// real values appear: a missing tier leaves no placeholder.
func (p *propView) headLines(pr domain.Proposal, w int) []string {
	t := p.st.theme
	id := propID(pr.ID)

	out := propLines(t.Title, oneLine(pr.Rule), w)
	if last := len(out) - 1; width(out[last])+2+width(id) <= w {
		out[last] += "  " + t.Faint.Render(id)
	} else {
		out = append(out, t.Faint.Render(id))
	}

	g := t.Glyphs

	var parts []string

	switch p.filter {
	case propApplied:
		parts = append(parts, t.Success.Render(g.Dot+" applied"))
	case propDismissed:
		parts = append(parts, t.Muted.Render(g.Dot+" dismissed"))
	default:
		parts = append(parts, t.Attention.Render(g.Dot+" pending"))

		if p.selected[pr.ID] {
			parts = append(parts, t.Marked.Render(g.Check+" selected"))
		} else {
			parts = append(parts, t.Faint.Render(g.Uncheck+" not selected"))
		}
	}

	if tier := p.tierOf(pr.ID); tier != "" {
		parts = append(parts, t.Muted.Render("tier")+" "+t.Body.Render(tier))
	}

	return append(out, strings.Join(parts, "   "))
}

// factLines renders the regions, the trigger paths and the provenance
// in a label column. Paths are clipped, never wrapped.
func (p *propView) factLines(pr domain.Proposal, w int) []string {
	t := p.st.theme

	row := func(label, value string) string {
		return t.cols(cell(t.Muted.Render(label), propLabelW), cell(value, max(1, w-propLabelW)))
	}

	paths := func(label string, set []string) []string {
		out := make([]string, len(set))

		for i, r := range set {
			if i > 0 {
				label = ""
			}

			out[i] = row(label, t.Accent.Render(oneLine(r)))
		}

		return out
	}

	var out []string

	if set := pr.RegionSet(); len(set) == 0 {
		out = append(out, row("Regions", t.Muted.Render("repo-wide")))
	} else {
		out = append(out, paths("Regions", set)...)
	}

	out = append(out, paths("Triggers", pr.TriggerPaths)...)

	agent := t.Muted.Render("unknown")
	if pr.Agent != "" {
		agent = t.Body.Render(oneLine(pr.Agent))
	}

	if pr.CreatedAt > 0 {
		agent += t.Faint.Render(" " + t.Glyphs.Sep + " created " + time.Unix(pr.CreatedAt, 0).Format(time.DateOnly))
	}

	return append(out, row("Agent", agent))
}

// evidenceLines renders every cited finding. A finding that aged out of
// the index is labelled unavailable, never silently omitted.
func (p *propView) evidenceLines(pr domain.Proposal, l *proposals.Ledger, w int) []string {
	t := p.st.theme
	head := t.Heading.Render("EVIDENCE")

	if len(pr.Members) == 0 {
		return append([]string{head}, propLines(t.Muted, "No cited findings are recorded for this proposal.", w)...)
	}

	meta := propPlural(len(pr.Members), "cited finding", "cited findings")
	if age, ok := p.newestAge(pr, l); ok {
		meta += " " + t.Glyphs.Sep + " newest " + age
	}

	out := []string{head + "  " + t.Faint.Render(meta)}

	if l != nil && l.EvidenceErr != nil {
		ids := make([]string, len(pr.Members))
		for i, id := range pr.Members {
			ids[i] = strconv.FormatInt(id, 10)
		}

		out = append(out, propNote(t, ToneFailure, "The cited evidence could not be read: "+errText(l.EvidenceErr), w)...)

		return append(out, propLines(t.Muted, "Cited finding IDs: "+strings.Join(ids, ", "), w)...)
	}

	for i, id := range pr.Members {
		if i > 0 {
			out = append(out, "")
		}

		lead := t.Faint.Render(padRight(itoa(i+1), 2)) + " "
		inner := max(1, w-width(lead))

		f, ok := domain.Finding{}, false
		if l != nil {
			f, ok = l.Evidence[id]
		}

		if !ok {
			out = append(out, propIndent(lead, propNote(t, ToneUnknown,
				"finding "+strconv.FormatInt(id, 10)+": unavailable"+propDash(t)+"aged out of the index", inner))...)

			continue
		}

		out = append(out, propIndent(lead, p.findingLines(f, inner))...)
	}

	return out
}

// newestAge returns the age of the newest available cited finding. The
// ledger's read time is the reference, so the age matches the snapshot.
func (p *propView) newestAge(pr domain.Proposal, l *proposals.Ledger) (string, bool) {
	if l == nil {
		return "", false
	}

	var newest int64

	for _, id := range pr.Members {
		if f, ok := l.Evidence[id]; ok && f.CreatedAt > newest {
			newest = f.CreatedAt
		}
	}

	if newest == 0 {
		return "", false
	}

	ref := l.ObservedAt
	if ref.IsZero() {
		ref = time.Now()
	}

	return propAge(ref.Sub(time.Unix(newest, 0))), true
}

// propAge renders a duration as "5m", "6h" or "16d".
func propAge(d time.Duration) string {
	d = max(0, d)

	switch {
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return itoa(int(d.Hours())) + "h"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}

// findingLines renders one cited finding: a plain summary of the
// reviewer's text in bold, then the pull request, the reviewer and the
// path. The summary drops markdown and emoji, so its width is exact.
func (p *propView) findingLines(f domain.Finding, w int) []string {
	t := p.st.theme

	var out []string

	if summary := oneLine(lesSummary(multiLine(f.Body))); summary == "" {
		out = append(out, t.Muted.Render("No text is recorded for this finding."))
	} else {
		lines := clipWithEllipsis(strings.Split(wrap(summary, w), "\n"), propSummaryN, w, t.Glyphs.Ellipsis)
		for _, l := range lines {
			out = append(out, t.Title.Render(l))
		}
	}

	var meta []string

	if f.PR > 0 {
		meta = append(meta, "PR #"+itoa(f.PR))
	}

	if f.Reviewer != "" {
		meta = append(meta, oneLine(f.Reviewer))
	}

	if f.PR == 0 && f.Source != "" {
		meta = append(meta, oneLine(f.Source))
	}

	path := f.Path
	if path == "" && len(f.Paths) > 0 {
		path = f.Paths[0]
	}

	sep := " " + t.Glyphs.Sep + " "
	line := t.Muted.Render(strings.Join(meta, sep))

	if path != "" {
		if len(f.Paths) > 1 {
			path += " +" + itoa(len(f.Paths)-1)
		}

		if len(meta) > 0 {
			line += t.Muted.Render(sep)
		}

		line += t.Accent.Render(oneLine(path))
	}

	if line != "" {
		out = append(out, truncate(line, w, t.Glyphs.Ellipsis))
	}

	return out
}

// advisoryLines renders the evidence-health advisories that the CLI
// ledger prints. The outcome is a derived check, so its label says so.
func (p *propView) advisoryLines(pr domain.Proposal, l *proposals.Ledger, w int) []string {
	t := p.st.theme

	if l == nil {
		return nil
	}

	head := t.Heading.Render("ADVISORIES")

	if l.HealthErr != nil {
		return append([]string{head}, propNote(t, ToneAttention, "Advisories are unavailable: "+errText(l.HealthErr), w)...)
	}

	h, ok := l.Health[pr.ID]
	if !ok {
		if p.filter == propDismissed {
			return append([]string{head}, propLines(t.Muted, "Advisories cover pending and applied proposals only.", w)...)
		}

		return append([]string{head}, propLines(t.Muted, "No advisories were computed for this proposal.", w)...)
	}

	var rows [][2]string

	if h.Tier != "" {
		v := oneLine(h.Tier)
		if h.Facts != "" {
			v += " " + t.Glyphs.Sep + " " + oneLine(h.Facts)
		}

		rows = append(rows, [2]string{"Evidence", v})
	}

	if h.Era != "" {
		rows = append(rows, [2]string{"Era", oneLine(h.Era)})
	}

	if h.Pruned {
		rows = append(rows, [2]string{"Pin", "not in " + proposals.LessonsFile +
			": removed by hand, or the YAML block was never pasted"})
	}

	if h.Retarget != "" {
		rows = append(rows, [2]string{"Regions now", "today's evidence suggests " + oneLine(h.Retarget)})
	}

	if h.Scope != "" {
		rows = append(rows, [2]string{"Scope", oneLine(h.Scope)})
	}

	if h.Blocked != "" {
		rows = append(rows, [2]string{"Blocked", oneLine(h.Blocked)})
	}

	if h.Outcome != "" {
		rows = append(rows, [2]string{"Outcome", oneLine(h.Outcome) +
			"\n(derived recurrence check, not proof of effectiveness)"})
	}

	if len(rows) == 0 {
		return append([]string{head}, propLines(t.Muted, "No advisory applies to this proposal.", w)...)
	}

	out := append([]string{head}, propKV(t, rows, w)...)

	if h.Escalate {
		out = append(out, propNote(t, ToneAttention, "The pin fires and the mistake still recurs.", w)...)
	}

	return out
}

// decisionLines explains what the user can decide about this proposal,
// and why read-only rows have no actions.
func (p *propView) decisionLines(pr domain.Proposal, l *proposals.Ledger, w int) []string {
	t := p.st.theme
	out := []string{t.Heading.Render("DECISION")}

	switch p.filter {
	case propApplied:
		out = append(out, propLines(t.Muted, "Applied proposals are read-only here, and the console has no undo. "+
			"Pin edits, pruning and retargeting stay on the CLI:", w)...)

		return append(out, t.command("seamark lessons --help"))
	case propDismissed:
		return append(out, propLines(t.Muted, "Dismissed proposals are read-only, and the console has no undo. "+
			"The decision is remembered: the same evidence does not come back.", w)...)
	}

	apply := "a previews an apply"
	if propGateOff(l) {
		apply = "a shows the YAML block"
	}

	out = append(out, propLines(t.Body, apply+" and d previews a dismissal of the selection, or of this "+
		"proposal when nothing is selected. Nothing changes before you confirm with y.", w)...)

	switch {
	case l == nil:
	case l.WriteErr != nil:
		out = append(out, propNote(t, ToneAttention, "The write gate could not be read: "+errText(l.WriteErr), w)...)
	case l.Write:
		out = append(out, propLines(t.Muted, "distill.write is on: apply writes pins to "+proposals.LessonsFile+".", w)...)
	default:
		out = append(out, propLines(t.Muted, "distill.write is off: a shows the YAML block to copy and changes nothing.", w)...)
	}

	if p.pinned[pr.ID] {
		out = append(out, propNote(t, ToneAttention, "A pin for "+propID(pr.ID)+" already exists in "+
			proposals.LessonsFile+". Press c to record it as applied (ledger only); the pin file stays unchanged.", w)...)
	}

	return out
}

// propGateOff reports a write gate that is known to be off. An unread
// or failed gate is not off: the preview and the commit check it again.
func propGateOff(l *proposals.Ledger) bool {
	return l != nil && l.WriteErr == nil && !l.Write
}

// unavailableBox explains a ledger that never loaded. A missing index
// is the common case, so it names the build route.
func (p *propView) unavailableBox(err error, w, h int) string {
	t := p.st.theme
	inner := min(boxInner(w), 72)

	var m propMessage

	switch {
	case err == nil:
		m = propMessage{
			head: []string{t.Accent.Render(t.spinnerFrame(p.st.tick)) + " " + t.Title.Render("Reading the proposal ledger"+t.Glyphs.Ellipsis)},
			body: "Proposals, their evidence and their advisories load from the index. Nothing is written.",
		}
	case errors.Is(err, ErrNoLedger):
		m = propMessage{
			head: []string{t.Attention.Render(t.Glyphs.Warn) + " " + t.Title.Render("No index yet")},
			body: "Proposals live in the index database, and this workspace has no readable index. " +
				"Build it from Overview (press 1, then b), or run this command in a shell:",
			next: append([]string{t.command("seamark index")}, propLines(t.Faint, errText(err), inner)...),
		}
	default:
		m = propMessage{
			head: []string{t.Failure.Render(t.Glyphs.Fail) + " " + t.Title.Render("The proposal ledger could not be read")},
			body: errText(err) + "\n\nThe other views stay usable. Press r to read the ledger again.",
		}
	}

	return p.messageBox(t.Title.Render("Proposals"), "", m, w, h)
}

// emptyBox explains an empty list: no proposals at all, no pending
// proposal, an empty state, or a region filter that hides every row.
func (p *propView) emptyBox(w, h int) string {
	t := p.st.theme
	label := p.filter.label()

	l := p.ledger()
	if l == nil {
		// A service may return no ledger and no error; that is an empty
		// ledger, not an unknown one.
		l = &proposals.Ledger{}
	}

	total := len(l.Pending) + len(l.Applied) + len(l.Dismissed)
	g := t.Glyphs

	var m propMessage

	switch {
	case total == 0:
		agent := "the configured agent"
		if a := p.distillAgent(); a != "" {
			agent += " (" + a + ")"
		}

		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No proposals yet")},
			body: "Proposals come from review evidence in two CLI steps. The first command fetches review comments " +
				"through gh. The second invokes " + agent + " to distill the evidence into proposals. " +
				"Nothing starts from this message; run the commands in a shell when you choose to.",
			next: []string{t.command("seamark index --reviews"), t.command("seamark lessons --distill")},
		}
	case p.filter == propPending && len(l.Pending) == 0:
		m = propMessage{
			head: []string{t.Success.Render(g.OK) + " " + t.Title.Render("No pending proposals")},
			body: "Every distilled proposal has been applied or dismissed.",
			next: p.morePending(),
		}
	case len(p.stateRows()) == 0:
		var others []string

		for f := propPending; f < propStateCount; f++ {
			if f != p.filter {
				others = append(others, itoa(propCount(l, f))+" "+f.label())
			}
		}

		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No "+label+" proposals")},
			body: "The ledger holds " + strings.Join(others, " and ") + " proposals. Press f to show another state.",
		}
	default:
		n := len(p.stateRows())

		m = propMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No "+label+" proposals match region "+oneLine(p.region))},
			body: propPlural(n, label+" proposal is", label+" proposals are") + " outside this region. " +
				"Press / and submit an empty filter to show every region.",
		}
	}

	return p.messageBox(t.Title.Render(p.listTitle()), t.Muted.Render("0"), m, w, h)
}

// morePending returns the facts and the commands under "No pending
// proposals": the active lesson count and the last review mining when
// they are known, then the CLI steps that propose more.
func (p *propView) morePending() []string {
	t := p.st.theme

	var facts []string

	if sec := &p.st.stats; sec.loaded && sec.value.Lessons != nil {
		facts = append(facts, propPlural(sec.value.Lessons.Active(), "lesson", "lessons")+" active")
	}

	if sec := &p.st.index; sec.loaded && sec.value.Status != nil && sec.value.Status.ReviewsMinedAt > 0 {
		facts = append(facts, "reviews mined "+ageSince(sec.value.Status.ReviewsMinedAt))
	}

	var out []string

	if len(facts) > 0 {
		out = append(out, t.Muted.Render(strings.Join(facts, " "+t.Glyphs.Sep+" ")), "")
	}

	return append(out,
		t.Faint.Render("run ")+t.command("seamark index --reviews")+t.Faint.Render(" to fetch new review findings,"),
		t.Faint.Render("then ")+t.command("seamark lessons --distill")+t.Faint.Render(" to propose more"))
}

// distillAgent returns the resolved distill command when the semantic
// health knows it, else "".
func (p *propView) distillAgent() string {
	sec := &p.st.index
	if !sec.loaded || sec.value.Status == nil {
		return ""
	}

	return diag(sec.value.Status.DistillAgent)
}

// propMessage is a centered message: a styled head, a plain body that
// wraps, and styled next-step lines.
type propMessage struct {
	head []string
	body string
	next []string
}

// messageBox renders a full-width box with a centered message. When the
// box is too short, the body gives up lines first, so the head and the
// next action stay visible.
func (p *propView) messageBox(title, right string, m propMessage, w, h int) string {
	t := p.st.theme
	inner := boxInner(w)
	rows := boxRows(h)
	textW := min(inner, 72)

	var body []string
	if m.body != "" {
		body = strings.Split(wrap(m.body, textW), "\n")
	}

	gap := 1
	if len(m.head)+len(body)+len(m.next)+2 > rows {
		gap = 0
	}

	if room := rows - len(m.head) - len(m.next) - 2*gap; len(body) > room {
		body = clipWithEllipsis(body, max(0, room), textW, t.Glyphs.Ellipsis)
	}

	lines := joinLineBlocks(gap, m.head, t.mutedLines(body), m.next)
	if len(lines) > rows {
		lines = lines[:rows]
	}

	out := make([]string, 0, rows)
	for range max(0, (rows-len(lines))*2/5) {
		out = append(out, "")
	}

	for _, l := range lines {
		out = append(out, strings.Repeat(" ", max(0, (inner-width(l))/2))+l)
	}

	return t.box(boxOpts{W: w, H: h, Title: title, Right: right, Focus: true, Lines: out})
}

// propPinBlock is the display form of a pin block. It never replaces
// Preview.YAML, because Commit compares that exact string to detect a
// stale preview.
type propPinBlock struct {
	lines []string
	// cont marks the lines that continue a folded note, parallel to
	// lines. They carry no YAML key, so the view does not color one.
	cont []bool
	// folded is true when at least one note continues on more lines.
	folded bool
	// overflow is true when at least one line stays wider than the
	// pane. The pane then needs sideways scrolling for that line.
	overflow bool
}

// propFoldPins fits a rendered pin block into w cells. A pane clips a
// wider line, and a terminal selection then copies a cut note. It
// folds only note lines: RemovePins and the pin readers read the rule,
// region and comment lines one line at a time. The folded block must
// parse to the same value as the source; else the source lines stay.
func propFoldPins(block string, w int) propPinBlock {
	src := strings.Split(strings.TrimRight(block, "\n"), "\n")
	out := propFoldLines(src, w)

	if out.folded && !propSameYAML(strings.Join(out.lines, "\n"), block) {
		out = propPinBlock{lines: src, cont: make([]bool, len(src))}

		for _, line := range src {
			out.overflow = out.overflow || width(line) > w
		}
	}

	return out
}

// propFoldLines folds each over-wide note line and keeps every other
// line. It does not check the result; propFoldPins does.
func propFoldLines(src []string, w int) propPinBlock {
	var out propPinBlock

	for _, line := range src {
		if width(line) <= w {
			out.lines = append(out.lines, line)
			out.cont = append(out.cont, false)

			continue
		}

		if parts, ok := propFoldNote(line, w); ok {
			for i, part := range parts {
				out.lines = append(out.lines, part)
				out.cont = append(out.cont, i > 0)
			}

			out.folded = true

			continue
		}

		out.lines = append(out.lines, line)
		out.cont = append(out.cont, false)
		out.overflow = true
	}

	return out
}

// propFoldNote splits one "note: value" line into lines of at most w
// cells. YAML folds a line break inside a plain or quoted flow scalar
// into one space. So each break replaces exactly one space, and each
// continuation line starts at the key indent plus two. It reports
// false for a block scalar, whose line breaks are content, and for a
// word that does not fit.
func propFoldNote(line string, w int) ([]string, bool) {
	body := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(body)]

	value, ok := strings.CutPrefix(body, "note: ")
	if !ok || value == "" || strings.ContainsAny(value[:1], "|>") {
		return nil, false
	}

	words := propSplitSingleSpaces(value)
	if len(words) < 2 {
		return nil, false
	}

	cont := indent + "  "
	cur := indent + "note: " + words[0]

	var out []string

	for _, word := range words[1:] {
		if width(cur)+1+width(word) <= w {
			cur += " " + word

			continue
		}

		out = append(out, cur)
		cur = cont + word
	}

	out = append(out, cur)

	for _, l := range out {
		if width(l) > w {
			return nil, false
		}
	}

	return out, true
}

// propSplitSingleSpaces splits s at each space that has a non-space on
// both sides. A run of spaces never splits: YAML trims the spaces at a
// line break, so a break there would lose spaces from the note.
func propSplitSingleSpaces(s string) []string {
	var out []string

	start := 0

	// A space is one byte in UTF-8, so byte indexes are safe here.
	for i := 1; i < len(s)-1; i++ {
		if s[i] == ' ' && s[i-1] != ' ' && s[i+1] != ' ' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}

	return append(out, s[start:])
}

// propSameYAML reports whether two pin blocks parse to the same value
// under a "pin:" key, as they do in lessons.yaml. Comments do not
// count. A block that does not parse never matches.
func propSameYAML(a, b string) bool {
	var x, y any

	if yaml.Unmarshal([]byte("pin:\n"+a), &x) != nil || yaml.Unmarshal([]byte("pin:\n"+b), &y) != nil {
		return false
	}

	return reflect.DeepEqual(x, y)
}
