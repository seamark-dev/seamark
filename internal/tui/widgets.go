package tui

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Layout thresholds. A terminal at least wideWidth cells wide shows a
// list and its detail side by side; a narrower one shows one pane at a
// time. Below minWidth by minHeight the shell asks for more space.
// minHeight is 20, because at 16 rows the confirmation dialogs lost
// their safety notes and the empty states lost their next action.
const (
	wideWidth = 100
	minWidth  = 60
	minHeight = 20
)

// listState is the cursor and scroll offset of one list. Screens keep
// one per list so resize and refresh never reset the position.
type listState struct {
	cursor int
	offset int
}

// clamp keeps the cursor inside n rows and the cursor row inside a
// window of h rows.
func (l *listState) clamp(n, h int) {
	if n <= 0 {
		l.cursor, l.offset = 0, 0

		return
	}

	l.cursor = max(0, min(l.cursor, n-1))

	if h <= 0 {
		return
	}

	if l.cursor < l.offset {
		l.offset = l.cursor
	}

	if l.cursor >= l.offset+h {
		l.offset = l.cursor - h + 1
	}

	l.offset = max(0, min(l.offset, max(0, n-h)))
}

// move shifts the cursor by delta rows inside n rows.
func (l *listState) move(delta, n int) {
	if n == 0 {
		return
	}

	l.cursor = max(0, min(l.cursor+delta, n-1))
}

// renderList renders rows in a window of h lines with the cursor row
// marked by a glyph and the selection style. The glyph keeps the
// cursor visible without color. focused=false renders the cursor
// without the highlight, so the active pane stays distinguishable.
// A list that scrolls gives its last line to a position note, so the
// window holds h-1 rows and the note never hides the cursor row.
func (t *Theme) renderList(rows []string, l *listState, w, h int, focused bool) string {
	window := h

	scrolls := len(rows) > h && h >= 2
	if scrolls {
		window = h - 1
	}

	l.clamp(len(rows), window)

	var b strings.Builder

	end := min(len(rows), l.offset+window)

	for i := l.offset; i < end; i++ {
		prefix := "  "

		line := truncate(rows[i], w-2, t.Glyphs.Ellipsis)

		if i == l.cursor {
			prefix = t.Glyphs.Cursor + " "

			if focused {
				line = t.Selected.Render(padRight(line, w-2))
			}
		}

		b.WriteString(prefix + line)

		if i < end-1 {
			b.WriteString("\n")
		}
	}

	if scrolls {
		b.WriteString("\n" + t.scrollNote(l.offset, window, len(rows), w))
	}

	return b.String()
}

// scrollNote renders the "a–b of n" position of a scrolling window,
// right-aligned, so hidden rows are never a surprise.
func (t *Theme) scrollNote(offset, window, n, w int) string {
	dash := "–"
	if t.ASCII {
		dash = "-"
	}

	note := strconv.Itoa(offset+1) + dash + strconv.Itoa(min(n, offset+window)) + " of " + strconv.Itoa(n)

	return t.Faint.Render(padLeft(note, w))
}

// paneTitle renders a pane heading. The focused pane gets the focus
// glyph as well as the accent color, so focus survives NO_COLOR.
func (t *Theme) paneTitle(title string, focused bool, w int) string {
	if focused {
		return truncate(t.RuleOn.Render(t.Glyphs.Focus)+" "+t.Title.Render(title), w, t.Glyphs.Ellipsis)
	}

	return truncate("  "+t.Muted.Render(title), w, t.Glyphs.Ellipsis)
}

// section renders a heading over a body with consistent spacing.
func (t *Theme) section(title, body string) string {
	if body == "" {
		return t.Heading.Render(strings.ToUpper(title))
	}

	return t.Heading.Render(strings.ToUpper(title)) + "\n" + body
}

// kv renders aligned label and value rows. Values wrap under their own
// column, so long paths stay readable.
func (t *Theme) kv(rows [][2]string, w int) string {
	labelW := 0

	for _, r := range rows {
		labelW = max(labelW, width(r[0]))
	}

	valueW := max(10, w-labelW-2)

	var b strings.Builder

	for i, r := range rows {
		lines := strings.Split(wrap(r[1], valueW), "\n")

		for j, line := range lines {
			label := ""
			if j == 0 {
				label = r[0]
			}

			b.WriteString(t.Muted.Render(padRight(label, labelW)) + "  " + line)

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

// hints renders key bindings as "key action" pairs for the footer.
//
// Consecutive unavailable entries (see unavailable) form one faint group
// that starts with the word n/a, for example "n/a: w retarget, v
// compare". The word carries the state without color. One word for the
// group costs fewer cells than one word for each key.
func (t *Theme) hints(bindings []key.Binding) string {
	return t.hintRow(bindings, false)
}

// shortHints renders the hints as hints does, but each n/a group shows
// only its keys, for example "n/a: w t v". A footer that cannot hold
// every hint uses it, so the unavailable keys stay in view at a cost of
// two cells for each key.
func (t *Theme) shortHints(bindings []key.Binding) string {
	return t.hintRow(bindings, true)
}

// hintRow renders the hints, with or without the actions of the n/a
// groups.
func (t *Theme) hintRow(bindings []key.Binding, short bool) string {
	var parts, off []string

	// The labelled group separates its entries with commas, because each
	// entry has two words. The short group has only keys, so spaces are
	// enough.
	sep := ", "
	if short {
		sep = " "
	}

	// flush closes the current group of unavailable entries.
	flush := func() {
		if len(off) > 0 {
			parts = append(parts, t.Faint.Render(unavailableWord+": "+strings.Join(off, sep)))
			off = nil
		}
	}

	for _, b := range bindings {
		if !listed(b) {
			continue
		}

		h := b.Help()

		if isUnavailable(b) {
			entry := t.keyLabel(h.Key)
			if !short {
				entry += " " + h.Desc
			}

			off = append(off, entry)

			continue
		}

		flush()

		parts = append(parts, t.Key.Render(t.keyLabel(h.Key))+" "+t.Muted.Render(h.Desc))
	}

	flush()

	// Three spaces between hints, as in the 1a footer, so a key never
	// reads as the label of its neighbour.
	return strings.Join(parts, "   ")
}

// asciiKeys spells arrow glyphs in key labels as words for the ASCII
// glyph set, whose fonts may lack the arrows.
var asciiKeys = strings.NewReplacer("↑↓", "up/down", "↑/↓", "up/down", "←→", "left/right", "↑", "up", "↓", "down", "←", "left", "→", "right")

// keyLabel returns a key label in the session glyph set.
func (t *Theme) keyLabel(k string) string {
	if t.ASCII {
		return asciiKeys.Replace(k)
	}

	return k
}

// Dialog geometry. dialogMinBox is the narrowest box, so the text
// inside keeps a usable width even in a narrow area.
const (
	dialogMaxBox = 84
	dialogMinBox = 12
	// dialogChrome is the border and the horizontal padding around
	// the text of a dialog: two border cells and four padding cells.
	dialogChrome = 6
)

// dialogWidth returns the outer width of a dialog box in a w-wide area.
// The box keeps two cells of margin on each side, and it is never
// wider than the area.
func dialogWidth(w int) int {
	boxW := min(w-4, max(48, min(dialogMaxBox, w*3/4)))

	return min(w, max(boxW, dialogMinBox))
}

// dialogInner returns the text width inside a dialog for a w-wide area.
func dialogInner(w int) int {
	return max(1, dialogWidth(w)-dialogChrome)
}

// dialog renders a bordered overlay box centered in w by h. Overlays
// are the only bordered element, so they read as a separate layer.
//
// The title and the action row always stay visible, because the action
// row names the only keys that leave some dialogs. When the box is
// taller than h, the dialog first removes the vertical padding and the
// extra blank lines of the body, then as many other blank body lines as
// needed. If the box is still too tall, the dialog clips the body and
// puts a note directly above the action row, so the user knows that
// text is hidden.
func (t *Theme) dialog(title, body, actions string, w, h int) string {
	boxW := dialogWidth(w)
	inner := dialogInner(w)

	d := dialogBox{
		head: t.Title.Render(truncate(title, inner, t.Glyphs.Ellipsis)),
		body: blockLines(body, inner),
		acts: blockLines(actions, inner),
		padY: 1,
		gap:  1,
	}

	if d.height() > h {
		d.padY = 0
		d.body = collapseBlankRuns(d.body)
	}

	if excess := d.height() - h; excess > 0 {
		d.body = dropBlankLines(d.body, excess)
	}

	if d.height() > h {
		d.clip(t.Faint.Render(truncate(t.dialogMoreNote(), inner, t.Glyphs.Ellipsis)), h)
	}

	box := t.Dialog.Padding(d.padY, 2).Width(boxW - 2).Render(d.content())

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// dialogMoreNote tells that the dialog body is clipped. The dialog does
// not scroll, so a larger terminal is the only way to show the rest.
func (t *Theme) dialogMoreNote() string {
	return "more below " + t.Glyphs.Dash + " enlarge the terminal"
}

// dialogBox is the line layout of one dialog: the title, the body, the
// action row, and the spacing between them. Each part is a list of
// lines that already fit the text width.
type dialogBox struct {
	head string
	body []string
	acts []string
	// note is the clip note. It is empty while the whole body fits.
	note string

	// padY is the vertical padding inside the border; gap is the count
	// of blank lines between two parts.
	padY, gap int
}

// height returns the rendered box height, the border included.
func (d *dialogBox) height() int {
	return len(d.lines()) + 2 + 2*d.padY
}

// clip keeps the body lines that fit in h rows above the note and the
// action row. When not even one body line fits, the blank line under
// the title goes first, so the title and the action row stay on screen
// as long as the border fits. A dialog without a body hides no text, so
// it gets no note.
func (d *dialogBox) clip(note string, h int) {
	if len(d.body) == 0 {
		d.gap = 0

		return
	}

	d.note = note

	// room is h minus the border, the padding, the title, the gap, the
	// note and the action row.
	room := func() int {
		return h - 2 - 2*d.padY - 1 - d.gap - 1 - len(d.acts)
	}

	if room() < 1 {
		d.gap = 0
	}

	d.body = d.body[:min(len(d.body), max(0, room()))]
}

// lines returns the content lines inside the padding. A clipped box
// puts the note directly above the action row.
func (d *dialogBox) lines() []string {
	out := []string{d.head}

	if len(d.body) > 0 {
		out = append(out, make([]string, d.gap)...)
		out = append(out, d.body...)
	}

	switch {
	case d.note != "":
		if len(d.body) == 0 {
			out = append(out, make([]string, d.gap)...)
		}

		out = append(out, d.note)
		out = append(out, d.acts...)
	case len(d.acts) > 0:
		out = append(out, make([]string, d.gap)...)
		out = append(out, d.acts...)
	}

	return out
}

// content joins the content lines for the dialog style.
func (d *dialogBox) content() string {
	return strings.Join(d.lines(), "\n")
}

// blockLines renders s at w cells and returns its lines, so a caller can
// count and clip the lines exactly as the box shows them. It returns nil
// for an empty s.
func blockLines(s string, w int) []string {
	if s == "" {
		return nil
	}

	return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}

// isBlank reports a line that shows nothing but spaces.
func isBlank(line string) bool {
	return strings.TrimSpace(ansi.Strip(line)) == ""
}

// collapseBlankRuns replaces each run of blank lines with one blank
// line, and removes blank lines at the start and the end.
func collapseBlankRuns(lines []string) []string {
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		if isBlank(line) && (len(out) == 0 || isBlank(out[len(out)-1])) {
			continue
		}

		out = append(out, line)
	}

	for len(out) > 0 && isBlank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}

	return out
}

// dropBlankLines removes up to n blank lines, the last ones first. The
// blank lines near the top stay, because they separate the lead text
// that the reader sees first.
func dropBlankLines(lines []string, n int) []string {
	out := append([]string(nil), lines...)

	for i := len(out) - 1; i >= 0 && n > 0; i-- {
		if isBlank(out[i]) {
			out = append(out[:i], out[i+1:]...)
			n--
		}
	}

	return out
}

// action renders one dialog action with its key, for example
// "enter Apply". primary=true gives it the prominent style.
func (t *Theme) action(k, label string, primary bool) string {
	if primary {
		return t.Key.Render(k) + " " + t.Button.Render(label)
	}

	return t.Key.Render(k) + " " + t.Ghost.Render(label)
}

// actions joins dialog actions with even spacing.
func (t *Theme) actions(items ...string) string {
	return strings.Join(items, "    ")
}

// empty renders a centered empty or error state: a title, an
// explanation, and the next valid action.
//
// The title and the next action are the most important lines, so they
// stay visible when the block is taller than h. The body gives up its
// lines first, and its last kept line ends with an ellipsis. The next
// action is clipped only when it does not fit next to the title.
func (t *Theme) empty(tone Tone, title, body, next string, w, h int) string {
	inner := max(1, min(w-4, 72))

	head := blockLines(t.ToneStyle(tone).Render(t.ToneGlyph(tone))+" "+t.Title.Render(title), inner)

	var text []string
	if body != "" {
		text = strings.Split(wrap(body, inner), "\n")
	}

	act := blockLines(next, inner)

	full := joinLineBlocks(1, head, t.mutedLines(text), act)
	if len(full) <= h {
		block := lipgloss.NewStyle().Width(inner).Render(strings.Join(full, "\n"))

		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
	}

	act = collapseBlankRuns(act)

	// The blank lines between the parts stay only when at least one
	// body line fits with them.
	gap := 1
	if len(head)+len(act)+2*gap >= h {
		gap = 0
	}

	room := h - len(head) - len(act) - 2*gap
	text = clipWithEllipsis(collapseBlankRuns(text), room, inner, t.Glyphs.Ellipsis)

	lines := joinLineBlocks(gap, head, t.mutedLines(text), act)
	if len(lines) > h {
		lines = lines[:max(0, h)]
	}

	block := lipgloss.NewStyle().Width(inner).Render(strings.Join(lines, "\n"))

	// The compact block fills the area, so it starts at the top.
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Top, block)
}

// mutedLines renders plain body lines in the muted style.
func (t *Theme) mutedLines(lines []string) []string {
	out := make([]string, len(lines))

	for i, line := range lines {
		out[i] = t.Muted.Render(line)
	}

	return out
}

// clipWithEllipsis keeps the first n plain lines. When lines are cut,
// the last kept line ends with the ellipsis, so the cut is visible.
func clipWithEllipsis(lines []string, n, w int, ellipsis string) []string {
	if len(lines) <= n {
		return lines
	}

	if n <= 0 {
		return nil
	}

	out := append([]string(nil), lines[:n]...)
	last := strings.TrimRight(out[n-1], " ")

	if width(last)+width(ellipsis) > w {
		last = truncate(last, max(0, w-width(ellipsis)), "")
	}

	out[n-1] = last + ellipsis

	return out
}

// joinLineBlocks joins the non-empty line blocks with gap blank lines
// between them.
func joinLineBlocks(gap int, blocks ...[]string) []string {
	var out []string

	for _, b := range blocks {
		if len(b) == 0 {
			continue
		}

		if len(out) > 0 {
			out = append(out, make([]string, gap)...)
		}

		out = append(out, b...)
	}

	return out
}

// command renders a selectable CLI command or path. It is display text
// only: the console never runs a command taken from diagnostics.
func (t *Theme) command(s string) string {
	return t.Code.Render(oneLine(s))
}

// scrollState is the vertical position of one scrollable text. It is
// the scroll type of Lessons under a shared name, so every modal and
// pane scrolls the same way.
type scrollState = lesScroll

// radio renders one choice of a single-choice list, for example
// "(●) codex". The glyph shows the choice without color.
func (t *Theme) radio(on bool, label string) string {
	if on {
		return t.Accent.Render(t.Glyphs.RadioOn) + " " + t.Body.Render(oneLine(label))
	}

	return t.Muted.Render(t.Glyphs.RadioOff) + " " + t.Muted.Render(oneLine(label))
}

// effect is one WILL line of a preview: its glyph, the glyph tone, and
// the text.
type effect struct {
	glyph string
	tone  Tone
	text  string
}

// effectLines renders the WILL and WON'T sections of a preview. A WON'T
// line is as explicit as a WILL line, because a reader must see what
// stays.
func (t *Theme) effectLines(will []effect, wont []string, w int) []string {
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
func (t *Theme) arrowRow(label, old, next string, labelW, w int) []string {
	arrow := " " + t.Glyphs.Arrow + " "

	if labelW+width(old+arrow+next) <= w {
		return []string{t.Muted.Render(padRight(label, labelW)) + t.Muted.Render(old) + t.Faint.Render(arrow) +
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
func (t *Theme) pair(lh, rh string, l, r []string, w int) []string {
	ell := t.Glyphs.Ellipsis

	if w < pairMinW {
		out := []string{truncate(lh, w, ell)}

		for _, line := range l {
			out = append(out, truncate(line, w, ell))
		}

		out = append(out, "", truncate(rh, w, ell))

		for _, line := range r {
			out = append(out, truncate(line, w, ell))
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

		out = append(out, padRight(truncate(a, lw, ell), lw)+div+truncate(b, rw, ell))
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
	tone               Tone
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
func (t *Theme) regionDelta(before, after []string, w int) []string {
	g := t.Glyphs
	was, now := deltaSet(before), deltaSet(after)

	var rows []deltaRow

	for _, r := range was {
		if !slices.Contains(now, r) {
			rows = append(rows, deltaRow{sign: g.Del, region: r, word: deltaRemoved, tone: ToneFailure})
		}
	}

	for _, r := range now {
		if !slices.Contains(was, r) {
			rows = append(rows, deltaRow{sign: g.Add, region: r, word: deltaAdded, tone: ToneSuccess})
		}
	}

	for _, r := range was {
		if slices.Contains(now, r) {
			rows = append(rows, deltaRow{sign: " ", region: r, word: deltaKept, tone: ToneNeutral})
		}
	}

	// The region column takes the longest region that fits beside the
	// sign and the longest word, so the words share one column.
	wordW := width(deltaRemoved)
	room := max(1, w-2-3-wordW)

	regW := 0
	for _, r := range rows {
		regW = max(regW, width(deltaLabel(r.region)))
	}

	regW = min(regW, room)

	out := make([]string, 0, len(rows))

	for _, r := range rows {
		out = append(out, t.deltaLines(r, regW)...)
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
func (t *Theme) deltaLines(r deltaRow, regW int) []string {
	sign := t.ToneStyle(r.tone).Render(r.sign)

	// Only an added region reads in the body style: it is what the
	// change delivers. The word of a kept row stays faint.
	text, word := t.Muted, t.ToneStyle(r.tone)
	if r.tone == ToneSuccess && r.region != "" {
		text = t.Body
	}

	if r.tone == ToneNeutral {
		word = t.Faint
	}

	segs := deltaWrap(deltaLabel(r.region), regW, max(1, regW-deltaIndent))
	out := make([]string, 0, len(segs))
	indent := strings.Repeat(" ", deltaIndent)

	for i, seg := range segs {
		if i == 0 {
			out = append(out, sign+" "+text.Render(padRight(seg, regW))+"   "+word.Render(r.word))

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
	if width(region) <= first {
		return []string{region}
	}

	var (
		rows []string
		cur  string
	)

	limit := first

	for _, part := range deltaParts(region) {
		if cur != "" && width(cur)+width(part) > limit {
			rows = append(rows, cur)
			cur, limit = "", rest
		}

		for cur == "" && width(part) > limit {
			head := truncate(part, limit, "")
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
		if r = oneLine(r); r != "" && !slices.Contains(out, r) {
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
func (t *Theme) phaseRows(rows []phaseRow, tick, w int) []string {
	g := t.Glyphs

	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, width(r.label))
	}

	lines := make([]string, 0, len(rows))

	for _, r := range rows {
		var glyph, word string

		style := t.Body

		switch r.state {
		case phaseActive:
			glyph = t.Accent.Render(t.spinnerFrame(tick))
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

		lines = append(lines, truncate(glyph+" "+style.Render(padRight(r.label, labelW))+"  "+
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
func (t *Theme) activityLines(lines []string, keep, w int) []string {
	if keep > 0 && len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, t.Muted.Render(truncate(diag(l), w, t.Glyphs.Ellipsis)))
	}

	return out
}

// spinnerFrame returns the activity glyph for a tick count.
func (t *Theme) spinnerFrame(tick int) string {
	frames := t.Glyphs.Spinner

	return frames[tick%len(frames)]
}

// itoa formats an int for labels and counts.
func itoa(n int) string {
	return strconv.Itoa(n)
}
