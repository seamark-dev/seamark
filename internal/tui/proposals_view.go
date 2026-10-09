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
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the Lessons destination in the 1a layout: the
// sub-view line, the filter line, the list box and the detail box. The
// decision modals live in proposals_modal.go; the state and the key
// handling live in proposals.go, and the decision stages in
// previewflow.go. The box frames, the cursor and the filter line format
// live in listpane.go, because Pins shares them.

// Pane geometry of the 1a Lessons screen at 120 columns: a list box of
// 62 cells, one cell of gap, and a detail box of 57 cells.
const (
	propListW    = 62
	propDesignW  = 120
	propMinRule  = 16
	propLabelW   = 10 // the label column of the detail facts
	propSummaryN = 3  // the most lines of one finding summary
)

// subTabs renders " Proposals   Pins   Stats   Evidence   Run   p / m /
// s / e / u" with the write gate at the right. The active sub-view is in
// the accent color and bold. Run and u show only while runShown. A
// narrow line first takes shorter labels and gaps (lesTabRungs) to keep
// the key hint, then drops the hint. The hint names m, the one switch
// key that is not the first letter of its tab, and a narrow footer drops
// the switch keys first. The gate decides what apply does, so it must
// stay on the line at the minimum width.
func (s *lessons) subTabs(w int) string {
	t := s.st.theme

	right := s.props.gateLabel()

	// The hint and the gate label are both faint, so the hint keeps the
	// same three-cell gap before the gate as after the tabs. Else they
	// read as one phrase, for example "p / s / e distill.write off".
	gap := 0
	if right != "" {
		right += " "
		gap = 3
	}

	hint := "p / m / s / e"
	if s.runShown() {
		hint += " / u"
	}

	// A capturing sub-view takes the letters as text, so it shows no
	// hint.
	if !s.capturing() {
		for _, rung := range lesTabRungs {
			left := s.tabLine(rung) + "   " + t.Faint.Render(hint)
			if ui.Width(left)+gap+ui.Width(right) <= w {
				return ui.Spread(left, right, w)
			}
		}
	}

	var left string

	for _, rung := range lesTabRungs {
		left = s.tabLine(rung)
		if ui.Width(left)+gap+ui.Width(right) <= w {
			break
		}
	}

	if ui.Width(left)+ui.Width(right) > w {
		return ui.Truncate(left, w, t.Glyphs.Ellipsis)
	}

	return ui.Spread(left, right, w)
}

// lesTabRung is one step of the sub-view line: the labels of Proposals
// and Evidence, and the gap between two tabs.
type lesTabRung struct {
	proposals, evidence, gap string
}

// lesTabRungs are the sub-view lines from the widest to the narrowest.
// subTabs takes the first one that leaves three cells before the gate,
// first with the key hint and then without it. At 80 columns with the
// Run tab, the full labels keep the hint only with two-cell gaps (78
// cells with "distill.write on"). At 60 columns, the short labels with
// two-cell gaps leave room for the 27 cells of an unreadable gate. The
// short labels follow the abbreviated destination tabs, for example
// "Integ".
var lesTabRungs = []lesTabRung{
	{proposals: "Proposals", evidence: "Evidence", gap: "   "},
	{proposals: "Proposals", evidence: "Evidence", gap: "  "},
	{proposals: "Props", evidence: "Evid", gap: "   "},
	{proposals: "Props", evidence: "Evid", gap: "  "},
}

// tabLine renders the tabs of one rung in the tab order Proposals, Pins,
// Stats, Evidence, Run.
func (s *lessons) tabLine(r lesTabRung) string {
	t := s.st.theme

	tab := func(label string, on bool) string {
		if on {
			return t.TabActive.Render(label)
		}

		return t.Muted.Render(label)
	}

	line := " " + tab(r.proposals, s.sub == lesProposals) + r.gap + tab("Pins", s.sub == lesPins) +
		r.gap + tab("Stats", s.sub == lesStats) + r.gap + tab(r.evidence, s.sub == lesEvidence)

	if s.runShown() {
		line += r.gap + tab("Run", s.sub == lesRun)
	}

	return line
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

	head := p.filterBar(w)

	if sec := &p.st.ledger; sec.stale() {
		head = append(head, " "+t.Attention.Render(ui.Truncate(t.Glyphs.Warn+" stale: "+ui.ErrText(sec.err)+
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

	states := filterChips(t, propStateCount, p.filter, func(f propState) string {
		if l == nil {
			return f.label()
		}

		return f.label() + " " + strconv.Itoa(propCount(l, f))
	})

	rest := t.Faint.Render("region") + " " + p.fieldView(t, filterRegion, p.region, "all")

	// The run filter shows as its own chip, so a list limited to one run
	// never looks like the whole pending list.
	if p.run != nil {
		rest += "     " + p.runChip()
	}

	if n := len(p.selected); n > 0 && p.filter == propPending {
		rest += "     " + t.Marked.Render(strconv.Itoa(n)+" selected")
	}

	return filterLines(t, t.Faint.Render("state")+" "+states, rest, w)
}

// propCount returns the number of proposals in one state.
func propCount(l *proposals.Ledger, f propState) int {
	return len(propStateRows(l, f))
}

// listTitle names the list box after the state filter, for example
// "Pending proposals".
func (p *propView) listTitle() string {
	label := p.filter.label()

	return strings.ToUpper(label[:1]) + label[1:] + " proposals"
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

	return p.split(rows, w, h, min(propListW, w*propListW/propDesignW), p.listBox, p.detailBox)
}

// listBox renders the proposal list with its column header. The cursor
// row has the cursor glyph, and in the focused box a full-width
// selection background (listPane.renderList).
func (p *propView) listBox(rows []domain.Proposal, w, h int, focused bool) string {
	t := p.st.theme
	cols := p.columns(rows, ui.BoxInner(w))

	return p.renderList(t, rows, w, h, focused, p.listTitle(), cols.header(t),
		func(pr domain.Proposal, cursor bool) string {
			return cols.row(t, pr, p.selected[pr.ID], cursor, p.tierOf(pr.ID))
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
		c.idW = max(c.idW, ui.Width(propID(pr.ID))+2)
	}

	if l := p.ledger(); l != nil && l.HealthErr == nil && len(l.Health) > 0 {
		c.tierW = 4

		for _, pr := range rows {
			c.tierW = max(c.tierW, min(6, ui.Width(p.tierOf(pr.ID))))
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
func (c propCols) cells(marker, check, id, rule, region, cited, tier string) []ui.ColCell {
	out := []ui.ColCell{ui.Cell(marker, 2)}

	if c.check {
		out = append(out, ui.Cell(check, 4))
	}

	out = append(out, ui.Cell(id, c.idW), ui.Cell(rule, c.ruleW))

	for _, opt := range []ui.ColCell{ui.Cell(region, c.regW), ui.Cell(cited, c.citedW), ui.Cell(tier, c.tierW)} {
		if opt.W > 0 {
			out = append(out, opt)
		}
	}

	return out
}

// header renders the faint column titles.
func (c propCols) header(t *ui.Theme) string {
	return t.Faint.Render(t.Cols(c.cells("", "", "ID", "RULE", "REGION", "CITED", "TIER")...))
}

// row renders one proposal. The cursor row has the cursor glyph and a
// bold rule; a marked row has the check glyph in the accent color.
func (c propCols) row(t *ui.Theme, pr domain.Proposal, marked, cursor bool, tier string) string {
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

	// The rule and the region stop one cell short of their columns, so a
	// full cell keeps a gap before the next column. A long region keeps
	// its last path components, as in the Pins list, because a cut from
	// the right makes every "internal/..." region look the same.
	ell := t.Glyphs.Ellipsis
	ruleText := ui.Truncate(ui.OneLine(pr.Rule), max(1, c.ruleW-1), ell)
	region := pinPathCell(propRegionLabel(pr), max(1, c.regW-1), ell)

	return t.Cols(c.cells(marker, check, t.Muted.Render(propID(pr.ID)), rule.Render(ruleText),
		t.Muted.Render(region), t.Body.Render("  "+strconv.Itoa(len(pr.Members))), t.Muted.Render(tier))...)
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
		return ui.OneLine(set[0])
	default:
		return strconv.Itoa(len(set)) + " regions"
	}
}

// tierOf returns the evidence tier of a proposal, or "".
func (p *propView) tierOf(id int64) string {
	l := p.ledger()
	if l == nil {
		return ""
	}

	return ui.OneLine(l.Health[id].Tier)
}

// detailBox renders the proposal under the cursor. The body scrolls
// inside the box, and a different proposal starts at the top
// (listPane.renderDetail).
func (p *propView) detailBox(rows []domain.Proposal, w, h int, focused bool) string {
	cur, _ := p.cursorRow(rows)

	return p.renderDetail(p.st.theme, cur, "Proposal "+propID(cur.ID), p.detailLines(cur, ui.BoxInner(w)), w, h, focused)
}

// propDash returns the dash of the theme's glyph set.
func propDash(t *ui.Theme) string {
	if t.ASCII {
		return " - "
	}

	return " — "
}

// propLines wraps text to w cells and styles each line.
func propLines(style lipgloss.Style, text string, w int) []string {
	lines := strings.Split(ui.Wrap(text, max(1, w)), "\n")
	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

// propNote renders a tone glyph and wrapped text with a hanging indent.
// The glyph keeps the tone visible without color.
func propNote(t *ui.Theme, tone ui.Tone, text string, w int) []string {
	style := t.ToneStyle(tone)
	lines := strings.Split(ui.Wrap(text, max(1, w-2)), "\n")

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
	pad := strings.Repeat(" ", ui.Width(lead))
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
func propKV(t *ui.Theme, rows [][2]string, w int) []string {
	return strings.Split(kv(t, rows, w), "\n")
}

// detailLines renders the full proposal: rule and state, the note,
// the regions and provenance, the cited evidence, the advisories and
// the available decisions.
func (p *propView) detailLines(pr domain.Proposal, w int) []string {
	t := p.st.theme
	l := p.ledger()

	// The head names the rule; this section holds the note, so its
	// heading says NOTE, as the Pins detail does.
	out := p.headLines(pr, w)
	out = append(out, "", t.Heading.Render("NOTE"))

	if strings.TrimSpace(pr.Note) == "" {
		out = append(out, t.Muted.Render("No note is recorded."))
	} else {
		out = append(out, propLines(t.Body, ui.MultiLine(pr.Note), w)...)
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

	out := propLines(t.Title, ui.OneLine(pr.Rule), w)
	if last := len(out) - 1; ui.Width(out[last])+2+ui.Width(id) <= w {
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
	case propSuperseded:
		// The hollow glyph matches the superseded rows of Pins, so one
		// history state looks the same in both sub-views.
		parts = append(parts, t.Muted.Render(g.Hollow+" superseded"))
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
		return t.Cols(ui.Cell(t.Muted.Render(label), propLabelW), ui.Cell(value, max(1, w-propLabelW)))
	}

	paths := func(label string, set []string) []string {
		out := make([]string, len(set))

		for i, r := range set {
			if i > 0 {
				label = ""
			}

			out[i] = row(label, t.Accent.Render(ui.OneLine(r)))
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
		agent = t.Body.Render(ui.OneLine(pr.Agent))
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

		out = append(out, propNote(t, ui.ToneFailure, "The cited evidence could not be read: "+ui.ErrText(l.EvidenceErr), w)...)

		return append(out, propLines(t.Muted, "Cited finding IDs: "+strings.Join(ids, ", "), w)...)
	}

	for i, id := range pr.Members {
		if i > 0 {
			out = append(out, "")
		}

		lead := t.Faint.Render(ui.PadRight(strconv.Itoa(i+1), 2)) + " "
		inner := max(1, w-ui.Width(lead))

		f, ok := domain.Finding{}, false
		if l != nil {
			f, ok = l.Evidence[id]
		}

		if !ok {
			out = append(out, propIndent(lead, propNote(t, ui.ToneUnknown,
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
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

// findingLines renders one cited finding: a plain summary of the
// reviewer's text in bold, then the pull request, the reviewer and the
// path. The summary drops markdown and emoji, so its width is exact.
func (p *propView) findingLines(f domain.Finding, w int) []string {
	t := p.st.theme

	var out []string

	if summary := ui.OneLine(lesSummary(ui.MultiLine(f.Body))); summary == "" {
		out = append(out, t.Muted.Render("No text is recorded for this finding."))
	} else {
		lines := ui.ClipWithEllipsis(strings.Split(ui.Wrap(summary, w), "\n"), propSummaryN, w, t.Glyphs.Ellipsis)
		for _, l := range lines {
			out = append(out, t.Title.Render(l))
		}
	}

	var meta []string

	if f.PR > 0 {
		meta = append(meta, "PR #"+strconv.Itoa(f.PR))
	}

	if f.Reviewer != "" {
		meta = append(meta, ui.OneLine(f.Reviewer))
	}

	if f.PR == 0 && f.Source != "" {
		meta = append(meta, ui.OneLine(f.Source))
	}

	path := f.Path
	if path == "" && len(f.Paths) > 0 {
		path = f.Paths[0]
	}

	sep := " " + t.Glyphs.Sep + " "
	line := t.Muted.Render(strings.Join(meta, sep))

	if path != "" {
		if len(f.Paths) > 1 {
			path += " +" + strconv.Itoa(len(f.Paths)-1)
		}

		if len(meta) > 0 {
			line += t.Muted.Render(sep)
		}

		line += t.Accent.Render(ui.OneLine(path))
	}

	if line != "" {
		out = append(out, ui.Truncate(line, w, t.Glyphs.Ellipsis))
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
		return append([]string{head}, propNote(t, ui.ToneAttention, "Advisories are unavailable: "+ui.ErrText(l.HealthErr), w)...)
	}

	h, ok := l.Health[pr.ID]
	if !ok {
		if p.filter == propDismissed || p.filter == propSuperseded {
			return append([]string{head}, propLines(t.Muted, "Advisories cover pending and applied proposals only.", w)...)
		}

		return append([]string{head}, propLines(t.Muted, "No advisories were computed for this proposal.", w)...)
	}

	var rows [][2]string

	if h.Tier != "" {
		v := ui.OneLine(h.Tier)
		if h.Facts != "" {
			v += " " + t.Glyphs.Sep + " " + ui.OneLine(h.Facts)
		}

		rows = append(rows, [2]string{"Evidence", v})
	}

	if h.Era != "" {
		rows = append(rows, [2]string{"Era", ui.OneLine(h.Era)})
	}

	if h.Pruned {
		rows = append(rows, [2]string{"Pin", "not in " + proposals.LessonsFile +
			": removed by hand, or the YAML block was never pasted"})
	}

	if h.Retarget != "" {
		rows = append(rows, [2]string{"Regions now", "today's evidence suggests " + ui.OneLine(h.Retarget)})
	}

	if h.Scope != "" {
		rows = append(rows, [2]string{"Scope", ui.OneLine(h.Scope)})
	}

	if h.Blocked != "" {
		rows = append(rows, [2]string{"Blocked", ui.OneLine(h.Blocked)})
	}

	if h.Outcome != "" {
		rows = append(rows, [2]string{"Outcome", ui.OneLine(h.Outcome) +
			"\n(derived recurrence check, not proof of effectiveness)"})
	}

	if len(rows) == 0 {
		return append([]string{head}, propLines(t.Muted, "No advisory applies to this proposal.", w)...)
	}

	out := append([]string{head}, propKV(t, rows, w)...)

	if h.Escalate {
		out = append(out, propNote(t, ui.ToneAttention, "The pin fires and the mistake still recurs.", w)...)
	}

	return out
}

// decisionLines explains what the user can decide about this proposal,
// and why read-only rows have no actions. An applied row points to
// Lessons › Pins, where its installed pin can be retargeted or pruned
// after a preview.
func (p *propView) decisionLines(pr domain.Proposal, l *proposals.Ledger, w int) []string {
	t := p.st.theme
	out := []string{t.Heading.Render("DECISION")}

	switch p.filter {
	case propApplied:
		out = append(out, propLines(t.Muted, "Applied proposals are read-only here, and the console has no undo. "+
			appliedPinText(p.st, pr.ID), w)...)

		return append(out, p.extractLines(pr, l, w)...)
	case propDismissed:
		return append(out, propLines(t.Muted, "Dismissed proposals are read-only, and the console has no undo. "+
			"The decision is remembered: the same evidence does not come back.", w)...)
	case propSuperseded:
		return append(out, propLines(t.Muted, "Superseded: its pin was pruned. Read-only; not restorable here.", w)...)
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
		out = append(out, propNote(t, ui.ToneAttention, "The write gate could not be read: "+ui.ErrText(l.WriteErr), w)...)
	case l.Write:
		out = append(out, propLines(t.Muted, "distill.write is on: apply writes pins to "+proposals.LessonsFile+".", w)...)
	default:
		out = append(out, propLines(t.Muted, "distill.write is off: a shows the YAML block to copy and changes nothing.", w)...)
	}

	// The applied key note comes first: the preview refuses that row before
	// it looks for a pin, so the pin note would offer a c that is n/a. The
	// note checks the targets, because a and c act on the marks when there
	// are any. So it explains each n/a of a and c in the help.
	ids, implicit := p.targets()

	which := "the selection"
	if implicit {
		which = propID(pr.ID)
	}

	switch taken := p.appliedKeyRefusal(ids); {
	case taken != nil:
		out = append(out, propNote(t, ui.ToneAttention, "a and c do not work for "+which+": Pins blocks a pin "+
			"that two applied proposals record. "+appliedKeyText(taken, ids), w)...)
	case p.appliedKeyRefusal([]int64{pr.ID}) != nil:
		// The marks do not hold this row, so a and c do not act on it.
		// The pin note must still not offer c for a row that the record
		// refuses.
	case p.hasPin(pr.ID):
		out = append(out, propNote(t, ui.ToneAttention, "A pin for "+propID(pr.ID)+" already exists in "+
			proposals.LessonsFile+". Press c to record it as applied (ledger only); the pin file stays unchanged.", w)...)
	}

	return append(out, p.extractLines(pr, l, w)...)
}

// extractLines tells whether t can ask the trigger question of the
// proposal, and why not when it cannot. A closed question shows its
// reason, so a missing t in the footer has an explanation. Marked rows
// replace the cursor row as the targets of t, so the line then speaks
// of the marked rows, as the a and d line does.
func (p *propView) extractLines(pr domain.Proposal, l *proposals.Ledger, w int) []string {
	t := p.st.theme

	if p.extractMarked() {
		targets := p.extractTargets()

		if !p.canExtract() && len(targets) > 0 {
			_, why := extQuestion(p.st, l, targets[0])

			return propLines(t.Muted, "Trigger question: no selected proposal has an open question; "+
				propID(targets[0].ID)+": "+why+".", w)
		}

		return propLines(t.Body, "t asks the agent about the "+propPlural(len(targets), "selected proposal",
			"selected proposals")+" (trigger extraction). The review lists the ones that it does not ask; "+
			"nothing is sent before y.", w)
	}

	ok, why := extQuestion(p.st, l, pr)
	if !ok {
		return propLines(t.Muted, "Trigger question: "+why+".", w)
	}

	return propLines(t.Body, "t asks the agent where its mistake occurs (trigger extraction). The review shows "+
		"the batches and the usage first; nothing is sent before y.", w)
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
	inner := min(ui.BoxInner(w), 72)

	var m paneMessage

	switch {
	case err == nil:
		m = paneMessage{
			head: []string{t.Accent.Render(t.SpinnerFrame(p.st.tick)) + " " + t.Title.Render("Reading the proposal ledger"+t.Glyphs.Ellipsis)},
			body: "Proposals, their evidence and their advisories load from the index. Nothing is written.",
		}
	case errors.Is(err, ErrNoLedger):
		m = paneMessage{
			head: []string{t.Attention.Render(t.Glyphs.Warn) + " " + t.Title.Render("No index yet")},
			body: "Proposals live in the index database, and this workspace has no readable index. " +
				"Build it from Overview (press 1, then b), or run this command in a shell:",
			next: append([]string{commandText(t, "seamark index")}, propLines(t.Faint, ui.ErrText(err), inner)...),
		}
	default:
		m = paneMessage{
			head: []string{t.Failure.Render(t.Glyphs.Fail) + " " + t.Title.Render("The proposal ledger could not be read")},
			body: ui.ErrText(err) + "\n\nThe other views stay usable. Press r to read the ledger again.",
		}
	}

	return messageBox(t, t.Title.Render("Proposals"), "", m, w, h)
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

	total := len(l.Pending) + len(l.Applied) + len(l.Dismissed) + len(l.Superseded)
	g := t.Glyphs

	var m paneMessage

	switch {
	case p.run != nil:
		// The run filter comes first: its proposals may be decided or
		// still loading, and "no proposals yet" would be false.
		m = p.runMessage(l)
	case total == 0:
		agent := "the configured agent"
		if a := p.distillAgent(); a != "" {
			agent += " (" + a + ")"
		}

		m = paneMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No proposals yet")},
			body: "Proposals come from review evidence in two CLI steps. The first command fetches review comments " +
				"through gh. The second invokes " + agent + " to distill the evidence into proposals. " +
				"Nothing starts from this message; run the commands in a shell when you choose to.",
			next: []string{commandText(t, "seamark index --reviews"), commandText(t, "seamark lessons --distill")},
		}
	case p.filter == propPending && len(l.Pending) == 0:
		m = paneMessage{
			head: []string{t.Success.Render(g.OK) + " " + t.Title.Render("No pending proposals")},
			body: "Every distilled proposal has been applied or dismissed.",
			next: p.morePending(),
		}
	case len(p.stateRows()) == 0:
		var others []string

		for f := propPending; f < propStateCount; f++ {
			if f != p.filter {
				others = append(others, strconv.Itoa(propCount(l, f))+" "+f.label())
			}
		}

		m = paneMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No "+label+" proposals")},
			body: "The ledger holds " + propJoin(others) + " proposals. Press f to show another state.",
		}
	default:
		n := len(p.stateRows())

		m = paneMessage{
			head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No "+label+" proposals match region "+ui.OneLine(p.region))},
			body: propPlural(n, label+" proposal is", label+" proposals are") + " outside this region. " +
				"Press / and submit an empty filter to show every region.",
		}
	}

	return messageBox(t, t.Title.Render(p.listTitle()), t.Muted.Render("0"), m, w, h)
}

// propJoin joins words as a list for a sentence: "a", "a and b", or
// "a, b and c".
func propJoin(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
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
		t.Faint.Render("run ")+commandText(t, "seamark index --reviews")+t.Faint.Render(" to fetch new review findings,"),
		t.Faint.Render("then ")+commandText(t, "seamark lessons --distill")+t.Faint.Render(" to propose more"))
}

// distillAgent returns the resolved distill command when the semantic
// health knows it, else "".
func (p *propView) distillAgent() string {
	sec := &p.st.index
	if !sec.loaded || sec.value.Status == nil {
		return ""
	}

	return ui.Diag(sec.value.Status.DistillAgent)
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
// folds only note lines. The pin file editor blocks a rule or a region
// that spans several lines, and it reads a provenance comment as one
// line. The folded block must parse to the same value as the source;
// else the source lines stay.
func propFoldPins(block string, w int) propPinBlock {
	src := strings.Split(strings.TrimRight(block, "\n"), "\n")
	out := propFoldLines(src, w)

	if out.folded && !propSameYAML(strings.Join(out.lines, "\n"), block) {
		out = propPinBlock{lines: src, cont: make([]bool, len(src))}

		for _, line := range src {
			out.overflow = out.overflow || ui.Width(line) > w
		}
	}

	return out
}

// propFoldLines folds each over-wide note line and keeps every other
// line. It does not check the result; propFoldPins does.
func propFoldLines(src []string, w int) propPinBlock {
	var out propPinBlock

	for _, line := range src {
		if ui.Width(line) <= w {
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
		if ui.Width(cur)+1+ui.Width(word) <= w {
			cur += " " + word

			continue
		}

		out = append(out, cur)
		cur = cont + word
	}

	out = append(out, cur)

	for _, l := range out {
		if ui.Width(l) > w {
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
