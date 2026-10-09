package tui

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the domain widgets: the rows and blocks whose name,
// parameters or fields carry a seamark concept, such as a preview
// effect, a run phase, a region delta, a setup step or the brand. They
// draw with the primitives of package ui. They stay here, because ui
// holds only what names no seamark concept (the ui package doc).

// sectionBlock renders a heading over a body with consistent spacing.
func sectionBlock(t *ui.Theme, title, body string) string {
	if body == "" {
		return t.Heading.Render(strings.ToUpper(title))
	}

	return t.Heading.Render(strings.ToUpper(title)) + "\n" + body
}

// kv renders aligned label and value rows. Values wrap under their own
// column, so long paths stay readable.
func kv(t *ui.Theme, rows [][2]string, w int) string {
	labelW := 0

	for _, r := range rows {
		labelW = max(labelW, ui.Width(r[0]))
	}

	valueW := max(10, w-labelW-2)

	var b strings.Builder

	for i, r := range rows {
		lines := strings.Split(ui.Wrap(r[1], valueW), "\n")

		for j, line := range lines {
			label := ""
			if j == 0 {
				label = r[0]
			}

			b.WriteString(t.Muted.Render(ui.PadRight(label, labelW)) + "  " + line)

			if j < len(lines)-1 {
				b.WriteString("\n")
			}
		}

		if i < len(rows)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// dialogMoreNote tells that the body of a build modal is clipped. The
// modal does not scroll, so a larger terminal is the only way to show
// the rest.
func dialogMoreNote(t *ui.Theme) string {
	return "more below " + t.Glyphs.Dash + " enlarge the terminal"
}

// commandText renders a selectable CLI command or path. It is display
// text only: the console never runs a command taken from diagnostics.
func commandText(t *ui.Theme, s string) string {
	return t.Code.Render(ui.OneLine(s))
}

// radio renders one choice of a single-choice list, for example
// "(●) codex". The glyph shows the choice without color.
func radio(t *ui.Theme, on bool, label string) string {
	if on {
		return t.Accent.Render(t.Glyphs.RadioOn) + " " + t.Body.Render(ui.OneLine(label))
	}

	return t.Muted.Render(t.Glyphs.RadioOff) + " " + t.Muted.Render(ui.OneLine(label))
}

// effect is one WILL line of a preview: its glyph, the glyph tone, and
// the text.
type effect struct {
	glyph string
	tone  ui.Tone
	text  string
}

// effectLines renders the WILL and WON'T sections of a preview. A WON'T
// line is as explicit as a WILL line, because a reader must see what
// stays.
func effectLines(t *ui.Theme, will []effect, wont []string, w int) []string {
	var out []string

	if len(will) > 0 {
		out = append(out, "", t.Heading.Render("WILL"))

		for _, e := range will {
			out = append(out, propIndent(t.ToneStyle(e.tone).Render(e.glyph)+" ",
				propLines(t.Body, e.text, w-2))...)
		}
	}

	if len(wont) > 0 {
		out = append(out, "", t.Heading.Render("WON'T"))

		for _, text := range wont {
			out = append(out, propIndent(t.Faint.Render(t.Glyphs.Sep)+" ", propLines(t.Muted, text, w-2))...)
		}
	}

	return out
}

// arrowRow renders "label  old › new". The old value uses the muted
// style, and the new value uses the plain style. A row that does not
// fit wraps in one style, because no style may cross a line end. The
// caller sanitizes old and next, because they can hold file text.
func arrowRow(t *ui.Theme, label, old, next string, labelW, w int) []string {
	arrow := " " + t.Glyphs.Arrow + " "

	if labelW+ui.Width(old+arrow+next) <= w {
		return []string{t.Muted.Render(ui.PadRight(label, labelW)) + t.Muted.Render(old) + t.Faint.Render(arrow) +
			t.Body.Render(next)}
	}

	return integLabeled(t, label, labelW, old+arrow+next, t.Body, w)
}

// pairMinW is the narrowest width that shows a pair as two columns. Each
// column then keeps at least 34 cells, which holds a short note line;
// a narrower column would break most words.
const pairMinW = 72

// pairGap is the divider between the two columns of a pair.
const pairGap = 3

// pairWidth returns the width that each side of a pair has at w cells:
// the left column when two columns fit, else the whole width, because
// the sides then stack. A caller wraps its lines to this width.
func pairWidth(w int) int {
	if w >= pairMinW {
		return (w - pairGap) / 2
	}

	return w
}

// pair renders two blocks for a comparison, for example the live pin
// and its recorded proposal. At pairMinW cells and wider the blocks
// stand side by side under their heads, with a vertical rule between
// them. A narrower width stacks them with a blank row between, because
// two narrow columns would break most words. lh and rh are the styled
// heads; l and r are styled lines that the caller sanitized and wrapped
// to pairWidth(w). pair clips a longer line, so no column overflows.
func pair(t *ui.Theme, lh, rh string, l, r []string, w int) []string {
	ell := t.Glyphs.Ellipsis

	if w < pairMinW {
		out := []string{ui.Truncate(lh, w, ell)}

		for _, line := range l {
			out = append(out, ui.Truncate(line, w, ell))
		}

		out = append(out, "", ui.Truncate(rh, w, ell))

		for _, line := range r {
			out = append(out, ui.Truncate(line, w, ell))
		}

		return out
	}

	lw := pairWidth(w)
	rw := w - pairGap - lw
	div := t.Rule.Render(" " + t.Glyphs.Vertical + " ")

	left := append([]string{lh}, l...)
	right := append([]string{rh}, r...)
	out := make([]string, 0, max(len(left), len(right)))

	for i := range max(len(left), len(right)) {
		var a, b string

		if i < len(left) {
			a = left[i]
		}

		if i < len(right) {
			b = right[i]
		}

		out = append(out, ui.PadRight(ui.Truncate(a, lw, ell), lw)+div+ui.Truncate(b, rw, ell))
	}

	return out
}

// The words of a region delta. Each row carries one, so the change
// reads without color and without the sign glyph.
const (
	deltaAdded   = "added"
	deltaRemoved = "removed"
	deltaKept    = "kept"
	// deltaRepoWide names an empty region set, which delivers a pin to
	// every path.
	deltaRepoWide = "repository-wide"
)

// deltaRow is one row of a region delta: the sign, the region, and the
// word. An empty region stands for the repository-wide set.
type deltaRow struct {
	sign, region, word string
	tone               ui.Tone
}

// regionDelta renders the change from the region set before to the set
// after as explicit rows, for example:
//
//	− internal/store       removed
//	+ internal/store/sql   added
//	  internal/api         kept
//
// Removed rows come first, then added rows, then kept rows. A kept row
// has no glyph, like a context line of a diff: the OK and Info glyphs
// read as "+" and "-" in ASCII and would turn a kept region into a
// change. An empty set is repository-wide. A long region wraps after a
// '/', and every row of it keeps its sign; a continuation row indents
// its text. regionDelta sanitizes the regions, because they come from
// the pin file or from a model.
func regionDelta(t *ui.Theme, before, after []string, w int) []string {
	g := t.Glyphs
	was, now := deltaSet(before), deltaSet(after)

	var rows []deltaRow

	for _, r := range was {
		if !slices.Contains(now, r) {
			rows = append(rows, deltaRow{sign: g.Del, region: r, word: deltaRemoved, tone: ui.ToneFailure})
		}
	}

	for _, r := range now {
		if !slices.Contains(was, r) {
			rows = append(rows, deltaRow{sign: g.Add, region: r, word: deltaAdded, tone: ui.ToneSuccess})
		}
	}

	for _, r := range was {
		if slices.Contains(now, r) {
			rows = append(rows, deltaRow{sign: " ", region: r, word: deltaKept, tone: ui.ToneNeutral})
		}
	}

	// The region column takes the longest region that fits beside the
	// sign and the longest word, so the words share one column.
	wordW := ui.Width(deltaRemoved)
	room := max(1, w-2-3-wordW)

	regW := 0
	for _, r := range rows {
		regW = max(regW, ui.Width(deltaLabel(r.region)))
	}

	regW = min(regW, room)

	out := make([]string, 0, len(rows))

	for _, r := range rows {
		out = append(out, deltaLines(t, r, regW)...)
	}

	return out
}

// deltaIndent is the extra indent of a continuation row of a wrapped
// region. Without it, a continuation reads as one more region of the
// delta.
const deltaIndent = 2

// deltaLines renders one delta row. A region wider than regW wraps
// after a '/' (deltaWrap). The continuation rows repeat the sign, so a
// removed path reads as removed on every row. They indent the text by
// deltaIndent and carry no word, so only the first row reads as a
// region.
func deltaLines(t *ui.Theme, r deltaRow, regW int) []string {
	sign := t.ToneStyle(r.tone).Render(r.sign)

	// Only an added region reads in the body style: it is what the
	// change delivers. The word of a kept row stays faint.
	text, word := t.Muted, t.ToneStyle(r.tone)
	if r.tone == ui.ToneSuccess && r.region != "" {
		text = t.Body
	}

	if r.tone == ui.ToneNeutral {
		word = t.Faint
	}

	segs := deltaWrap(deltaLabel(r.region), regW, max(1, regW-deltaIndent))
	out := make([]string, 0, len(segs))
	indent := strings.Repeat(" ", deltaIndent)

	for i, seg := range segs {
		if i == 0 {
			out = append(out, sign+" "+text.Render(ui.PadRight(seg, regW))+"   "+word.Render(r.word))

			continue
		}

		out = append(out, sign+" "+indent+text.Render(seg))
	}

	return out
}

// deltaWrap breaks a region into rows of at most first cells, then rest
// cells. It breaks after a '/' or a space, so each row ends at a path
// boundary and no row starts with a fragment of a directory name. Only a
// single part that is wider than its row breaks at the cell limit,
// because no row may overflow the column.
func deltaWrap(region string, first, rest int) []string {
	if ui.Width(region) <= first {
		return []string{region}
	}

	var (
		rows []string
		cur  string
	)

	limit := first

	for _, part := range deltaParts(region) {
		if cur != "" && ui.Width(cur)+ui.Width(part) > limit {
			rows = append(rows, cur)
			cur, limit = "", rest
		}

		for cur == "" && ui.Width(part) > limit {
			head := ui.Truncate(part, limit, "")
			if head == "" {
				// A wide character does not fit a one-cell row; it
				// takes the row alone, so the loop always moves on.
				_, size := utf8.DecodeRuneInString(part)
				head = part[:size]
			}

			rows = append(rows, head)
			part, limit = part[len(head):], rest
		}

		cur += part
	}

	if cur != "" {
		rows = append(rows, cur)
	}

	return rows
}

// deltaParts splits a region after each '/' and each space, so the
// parts joined again give the region. Both separators are ASCII, so a
// byte scan never cuts a multi-byte character.
func deltaParts(region string) []string {
	var parts []string

	start := 0

	for i := range len(region) {
		if region[i] == '/' || region[i] == ' ' {
			parts = append(parts, region[start:i+1])
			start = i + 1
		}
	}

	if start < len(region) {
		parts = append(parts, region[start:])
	}

	return parts
}

// deltaSet returns the sanitized regions of set in their order, without
// repeats and without empty entries. An empty set gives the one empty
// region that stands for the repository-wide set, so a move to or from
// it shows as a change.
func deltaSet(set []string) []string {
	out := make([]string, 0, len(set))

	for _, r := range set {
		if r = ui.OneLine(r); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}

	if len(out) == 0 {
		return []string{""}
	}

	return out
}

// deltaLabel returns the shown text of a delta region.
func deltaLabel(region string) string {
	if region == "" {
		return deltaRepoWide
	}

	return region
}

// phaseState is the progress of one step of a long operation.
type phaseState int

const (
	phasePending phaseState = iota
	phaseActive
	phaseDone
	phaseSkipped
	phaseFailed
	phaseStopped
)

// phaseRow is one step line: the label, a note such as a count, and
// the state.
type phaseRow struct {
	label string
	note  string
	state phaseState
}

// phaseRows renders each step with its state glyph, for example
// "✓ Scan files        1,204 files". The labels share one column. The
// skipped, failed and stopped states also show a word, because failed
// and stopped share the glyph "!" and must differ without color.
func phaseRows(t *ui.Theme, rows []phaseRow, tick, w int) []string {
	g := t.Glyphs

	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, ui.Width(r.label))
	}

	lines := make([]string, 0, len(rows))

	for _, r := range rows {
		var glyph, word string

		style := t.Body

		switch r.state {
		case phaseActive:
			glyph = t.Accent.Render(t.SpinnerFrame(tick))
		case phaseDone:
			glyph = t.Success.Render(g.OK)
		case phaseSkipped:
			glyph, style, word = t.Faint.Render(g.Info), t.Faint, "skipped"
		case phaseFailed:
			glyph, word = t.Failure.Render(g.Fail), "failed"
		case phaseStopped:
			glyph, word = t.Attention.Render(g.Warn), "stopped"
		default:
			glyph, style = t.Faint.Render(g.Info), t.Muted
		}

		lines = append(lines, ui.Truncate(glyph+" "+style.Render(ui.PadRight(r.label, labelW))+"  "+
			t.Muted.Render(joinNote(word, r.note)), w, g.Ellipsis))
	}

	return lines
}

// joinNote joins two notes of one line with a comma. An empty note
// adds nothing.
func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + ", " + b
	}
}

// activityLines renders the last keep diagnostic lines, each redacted,
// sanitized and cut to w cells. A long run keeps its detail area this
// small, so raw process output never takes over the screen. keep <= 0
// keeps every line.
func activityLines(t *ui.Theme, lines []string, keep, w int) []string {
	if keep > 0 && len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, t.Muted.Render(ui.Truncate(ui.Diag(l), w, t.Glyphs.Ellipsis)))
	}

	return out
}

// paneMessage is a centered message: a styled head, a plain body that
// wraps, and styled next-step lines.
type paneMessage struct {
	head []string
	body string
	next []string
}

// messageBox renders a full-width box with a centered message. When the
// box is too short, the body gives up lines first, so the head and the
// next action stay visible. The empty states of Proposals and Pins use
// it, so both sub-views read alike.
func messageBox(t *ui.Theme, title, right string, m paneMessage, w, h int) string {
	inner := ui.BoxInner(w)
	rows := ui.BoxRows(h)
	textW := min(inner, 72)

	var body []string
	if m.body != "" {
		body = strings.Split(ui.Wrap(m.body, textW), "\n")
	}

	gap := 1
	if len(m.head)+len(body)+len(m.next)+2 > rows {
		gap = 0
	}

	if room := rows - len(m.head) - len(m.next) - 2*gap; len(body) > room {
		body = ui.ClipWithEllipsis(body, max(0, room), textW, t.Glyphs.Ellipsis)
	}

	lines := ui.JoinLineBlocks(gap, m.head, t.MutedLines(body), m.next)
	if len(lines) > rows {
		lines = lines[:rows]
	}

	out := make([]string, 0, rows)
	for range max(0, (rows-len(lines))*2/5) {
		out = append(out, "")
	}

	for _, l := range lines {
		out = append(out, strings.Repeat(" ", max(0, (inner-ui.Width(l))/2))+l)
	}

	return t.Box(ui.BoxOpts{W: w, H: h, Title: title, Right: right, Focus: true, Lines: out})
}

// stepper renders "✓ Client ── ● Choose ── ○ Review ── ○ Apply": done
// steps in the success color, the current step in the accent color and
// the rest faint.
func stepper(t *ui.Theme, steps []string, current int) string {
	g := t.Glyphs
	parts := make([]string, 0, len(steps))

	for i, s := range steps {
		switch {
		case i < current:
			parts = append(parts, t.Success.Render(g.OK+" "+s))
		case i == current:
			parts = append(parts, t.TabActive.Render(g.Dot+" "+s))
		default:
			parts = append(parts, t.Faint.Render(g.Hollow+" "+s))
		}
	}

	return strings.Join(parts, t.Rule.Render(" "+g.RuleLight+g.RuleLight+" "))
}

// brandText renders the mark "▶┊◀ seamark".
func brandText(t *ui.Theme) string {
	g := t.Glyphs

	return t.Header.Render(g.HeaderLeft) + t.HeaderMark.Render(g.HeaderMid) +
		t.HeaderMark.Bold(true).Render(g.HeaderRight) + " " + t.Header.Render("seamark")
}
