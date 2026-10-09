package ui

import (
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// BoxOpts describes one bordered pane of the 1a layout. The title, the
// right label and the footer sit inside the border lines, so a pane
// spends no content line on its own heading.
type BoxOpts struct {
	// W and H are the outer size in cells, borders included.
	W, H int
	// Title is the styled label at the left of the top border.
	Title string
	// Right is the styled label at the right of the top border, for
	// example a count or a state.
	Right string
	// Foot is the styled label at the right of the bottom border, for
	// example a scroll position.
	Foot string
	// Focus draws the border in the accent color. Only the pane that
	// receives the movement keys has focus.
	Focus bool
	// Lines are the styled content lines. Each line is clipped to the
	// inner width with an ellipsis; lines past the inner height are
	// dropped, so callers scroll before they render.
	Lines []string
	// Fill paints the modal surface under the whole box.
	Fill bool
}

// BoxInner returns the content width of a box of outer width w: the two
// border cells and one cell of padding on each side.
func BoxInner(w int) int {
	return max(1, w-4)
}

// BoxRows returns the content height of a box of outer height h.
func BoxRows(h int) int {
	return max(0, h-2)
}

// BoxTitle styles a box title: the focused pane in the title style, the
// other panes muted, as in the 1a design. The caller sanitizes the
// title, because most titles are fixed words.
func BoxTitle(t *Theme, title string, focused bool) string {
	if focused {
		return t.Title.Render(title)
	}

	return t.Muted.Render(title)
}

// Box renders a bordered pane of exactly W by H cells.
func (t *Theme) Box(o BoxOpts) string {
	g := t.Glyphs
	w := max(4, o.W)
	h := max(2, o.H)

	bc := t.Rule
	if o.Focus {
		bc = t.RuleOn
	}

	out := make([]string, 0, h)
	out = append(out, t.boxTop(o.Title, o.Right, w, bc))

	inner := BoxInner(w)
	v := bc.Render(g.Vertical)

	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(o.Lines) {
			line = o.Lines[i]
		}

		out = append(out, v+" "+PadRight(Truncate(line, inner, g.Ellipsis), inner)+" "+v)
	}

	out = append(out, t.boxBottom(o.Foot, w, bc))

	if o.Fill {
		for i, l := range out {
			out[i] = t.Highlight(l, w, bgSurf)
		}
	}

	return strings.Join(out, "\n")
}

// boxTop renders "╭─ Title ──── right ─╮". A long title gives way to the
// right label first, because the right label often carries a count.
func (t *Theme) boxTop(title, right string, w int, bc lipgloss.Style) string {
	g := t.Glyphs

	var rt string
	if right != "" {
		rt = " " + right + " "
	}

	var tt string
	if title != "" {
		room := w - 4 - Width(rt) - 2
		if room < 1 {
			rt = ""
			room = w - 6
		}

		tt = " " + Truncate(title, max(1, room), g.Ellipsis) + " "
	}

	fill := max(0, w-3-Width(tt)-Width(rt)-1)

	return bc.Render(g.CornerTL+g.RuleLight) + tt + bc.Render(strings.Repeat(g.RuleLight, fill)) +
		rt + bc.Render(g.RuleLight+g.CornerTR)
}

// boxBottom renders "╰──── foot ─╯".
func (t *Theme) boxBottom(foot string, w int, bc lipgloss.Style) string {
	g := t.Glyphs

	var ft string
	if foot != "" {
		ft = " " + Truncate(foot, max(1, w-6), g.Ellipsis) + " "
	}

	fill := max(0, w-3-Width(ft))

	return bc.Render(g.CornerBL+strings.Repeat(g.RuleLight, fill)) + ft + bc.Render(g.RuleLight+g.CornerBR)
}

// Modal geometry: 80 cells at the 120-column design size, wider on a
// wide terminal so long lines fold less.
const (
	ModalMinW = 80
	// modalMargin keeps one dimmed row above and below the modal, so
	// the user still sees the screen that opened it.
	modalMargin = 2
)

// ModalOpts is the content of one modal: the border labels, the body
// that scrolls, and the actions that the bottom row names.
type ModalOpts struct {
	Title, Right string
	Body         []string
	// Actions are the keys under the rule at the bottom. The row stays
	// visible while the body scrolls.
	Actions []key.Binding
	// Tail is the summary of a confirmation, for example "Removes 2
	// lines from one file". It stays under the rule, above the
	// actions, and never scrolls, so the minimum layout still shows what
	// y confirms. The caller wraps each line to ModalBodyWidth; the box
	// clips a longer line. A summary taller than the area keeps its first
	// lines, so the actions and the border stay (capTail). Nil keeps the
	// layout without a summary.
	Tail []string
	// MinW is the narrowest modal on a wide terminal; 0 selects
	// ModalMinW. A small terminal still gets a narrower modal.
	MinW int
}

// modalWidth returns the outer width of a modal in a w-wide area.
func modalWidth(minW, w int) int {
	if minW <= 0 {
		minW = ModalMinW
	}

	return min(w-4, max(minW, w*2/3))
}

// ModalBodyWidth returns the width of the body lines of a modal in a
// w-wide area. A caller wraps the body to this width, so the box shows
// each line whole.
func ModalBodyWidth(minW, w int) int {
	return BoxInner(modalWidth(minW, w))
}

// Modal renders the modal of the 1a design. It draws a filled box with
// an accent border, a body that scrolls in sc, and the actions under a
// rule. It records the scroll bounds in sc, because a key handler
// cannot know the rendered height. The shell dims the frame and
// centers the block.
func (t *Theme) Modal(o ModalOpts, sc *ScrollState, w, h int) string {
	if sc == nil {
		sc = &ScrollState{}
	}

	mw := modalWidth(o.MinW, w)
	inner := BoxInner(mw)

	// The rule separates the body that scrolls from the rows that stay:
	// the scope summary, then the actions.
	var tail []string
	if len(o.Actions) > 0 || len(o.Tail) > 0 {
		// The two border rows, one body row, the blank row, the rule and
		// the actions row must fit, so the summary gets the rest.
		room := h - modalMargin - 2 - 1 - 2
		if len(o.Actions) > 0 {
			room--
		}

		tail = append([]string{"", t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner))},
			t.capTail(o.Tail, room, inner)...)
	}

	if len(o.Actions) > 0 {
		tail = append(tail, t.Hints(o.Actions))
	}

	window := min(len(o.Body), max(1, h-modalMargin-2-len(tail)))
	from, to := sc.Fit(len(o.Body), window)
	lines := append(append([]string{}, o.Body[from:to]...), tail...)

	var foot string
	if sc.Scrolls() {
		foot = t.RangeNote(from, to, len(o.Body))
	}

	return t.Box(BoxOpts{
		W: mw, H: len(lines) + 2, Focus: true, Fill: true, Lines: lines,
		Title: o.Title, Right: o.Right, Foot: foot,
	})
}

// capTail keeps at most n lines of a modal scope summary in inner cells.
// The summary never scrolls, so a long one would push the actions and
// the bottom border out of the area. A cut summary ends its last kept
// line with the ellipsis, so the user sees that it goes on. The modal
// keeps at least one line, because the summary names what y confirms.
func (t *Theme) capTail(lines []string, n, inner int) []string {
	if len(lines) <= n {
		return lines
	}

	n = max(1, n)
	kept := slices.Clone(lines[:n])
	ell := t.Glyphs.Ellipsis
	kept[n-1] = Truncate(kept[n-1], max(0, inner-Width(ell)-1), "") + " " + ell

	return kept
}

// ScrollBox renders body lines in a box that scrolls in sc, with the
// tail lines always visible under the body. When follow is set, the
// window moves so that the lines keep[0] to keep[1] show, for example
// the cursor group and its detail lines. A box that scrolls shows its
// position in the bottom border.
func (t *Theme) ScrollBox(o BoxOpts, body, tail []string, sc *ScrollState, keep [2]int, follow bool) string {
	return t.StickyBox(o, nil, body, tail, sc, keep, follow)
}

// StickyBox is ScrollBox with head lines that stay above the body, for
// example the headline of a long operation. Only the body scrolls, and
// keep counts body lines. Several screens share it, so every scrolling
// box keeps the same rules.
func (t *Theme) StickyBox(o BoxOpts, head, body, tail []string, sc *ScrollState, keep [2]int, follow bool) string {
	rows := BoxRows(o.H)
	fixed := len(head) + len(tail)

	window := len(body)
	if len(body)+fixed > rows {
		window = max(1, rows-fixed)
	}

	if follow {
		sc.Reveal(keep[0], keep[1], window)
	}

	from, to := sc.Fit(len(body), window)
	o.Lines = append(append(slices.Clone(head), body[from:to]...), tail...)

	if sc.Scrolls() && o.Foot == "" {
		o.Foot = t.RangeNote(from, to, len(body))
	}

	return t.Box(o)
}

// Card renders a summary card: a short box whose title is a dim
// upper-case label and whose right label carries the state.
func (t *Theme) Card(w, h int, title, right string, lines []string) string {
	return t.Box(BoxOpts{W: w, H: h, Title: t.Heading.Render(title), Right: right, Lines: lines})
}

// HJoin places rendered blocks side by side with gap cells between
// them. Blocks of different heights are padded at the bottom.
func HJoin(gap int, blocks ...string) string {
	parts := make([]string, 0, 2*len(blocks))

	for i, b := range blocks {
		if i > 0 && gap > 0 {
			parts = append(parts, strings.Repeat(" ", gap))
		}

		parts = append(parts, b)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// Overlay places a rendered block on a dimmed copy of base, centered in
// w by h cells. The base keeps its text around the block, so the user
// still sees where the modal came from.
func (t *Theme) Overlay(base, block string, w, h int) string {
	baseLines := strings.Split(Fit(base, w, h), "\n")
	blockLines := strings.Split(block, "\n")

	bw := 0
	for _, l := range blockLines {
		bw = max(bw, Width(l))
	}

	bw = min(bw, w)
	bh := min(len(blockLines), h)
	x := max(0, (w-bw)/2)
	y := max(0, (h-bh)/2)

	for i := range baseLines {
		plain := ansi.Strip(baseLines[i])

		if i < y || i >= y+bh {
			baseLines[i] = t.Rule.Render(plain)

			continue
		}

		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+bw, "")
		mid := PadRight(Truncate(blockLines[i-y], bw, ""), bw)

		baseLines[i] = t.Rule.Render(PadRight(left, x)) + mid + t.Rule.Render(right)
	}

	return strings.Join(baseLines, "\n")
}

// Bar renders a horizontal bar of w cells: the filled part in the tone
// color and the rest in the line color. The count beside it carries the
// value, so the bar is a visual aid only.
func (t *Theme) Bar(n, total, w int, tone Tone) string {
	if w <= 0 {
		return ""
	}

	filled := 0
	if total > 0 && n > 0 {
		filled = max(1, min(w, int(math.Round(float64(n)/float64(total)*float64(w)))))
	}

	return t.ToneStyle(tone).Render(strings.Repeat(t.Glyphs.BarFull, filled)) +
		t.Rule.Render(strings.Repeat(t.Glyphs.BarEmpty, w-filled))
}

// Columns renders a column chart of h rows. Each value takes two cells
// with a one-cell gap. The top cell of a column uses an eighth-block
// glyph, so small differences stay visible at a low height.
func (t *Theme) Columns(vals []int, h int, tone Tone) []string {
	peak := 0
	for _, v := range vals {
		peak = max(peak, v)
	}

	spark := t.Glyphs.Spark
	rows := make([]string, 0, h)

	for r := h - 1; r >= 0; r-- {
		var b strings.Builder

		for _, v := range vals {
			ch := " "

			if peak > 0 {
				eighths := int(math.Round(float64(v)/float64(peak)*float64(h*8))) - r*8

				switch {
				case eighths >= 8:
					ch = spark[len(spark)-1]
				case eighths > 0:
					ch = spark[eighths-1]
				}
			}

			b.WriteString(ch + ch + " ")
		}

		rows = append(rows, t.ToneStyle(tone).Render(b.String()))
	}

	return rows
}

// Spread places left and right text on one line of w cells.
func Spread(left, right string, w int) string {
	gap := w - Width(left) - Width(right)
	if gap < 1 {
		return Truncate(left, max(0, w-Width(right)-1), "…") + " " + right
	}

	return left + strings.Repeat(" ", gap) + right
}

// Cols lays out cells in fixed-width columns. Each cell is clipped with
// an ellipsis and padded, so a table never wraps a cell.
func (t *Theme) Cols(cells ...ColCell) string {
	var b strings.Builder

	for _, c := range cells {
		b.WriteString(PadRight(Truncate(c.Text, c.W, t.Glyphs.Ellipsis), c.W))
	}

	return b.String()
}

// ColCell is one cell of a fixed-width table row.
type ColCell struct {
	Text string
	W    int
}

// Cell builds a ColCell.
func Cell(text string, w int) ColCell {
	return ColCell{Text: text, W: w}
}
