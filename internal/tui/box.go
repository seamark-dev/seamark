package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// boxOpts describes one bordered pane of the 1a layout. The title, the
// right label and the footer sit inside the border lines, so a pane
// spends no content line on its own heading.
type boxOpts struct {
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

// boxInner returns the content width of a box of outer width w: the two
// border cells and one cell of padding on each side.
func boxInner(w int) int {
	return max(1, w-4)
}

// boxRows returns the content height of a box of outer height h.
func boxRows(h int) int {
	return max(0, h-2)
}

// box renders a bordered pane of exactly W by H cells.
func (t *Theme) box(o boxOpts) string {
	g := t.Glyphs
	w := max(4, o.W)
	h := max(2, o.H)

	bc := t.Rule
	if o.Focus {
		bc = t.RuleOn
	}

	out := make([]string, 0, h)
	out = append(out, t.boxTop(o.Title, o.Right, w, bc))

	inner := boxInner(w)
	v := bc.Render(g.Vertical)

	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(o.Lines) {
			line = o.Lines[i]
		}

		out = append(out, v+" "+padRight(truncate(line, inner, g.Ellipsis), inner)+" "+v)
	}

	out = append(out, t.boxBottom(o.Foot, w, bc))

	if o.Fill {
		for i, l := range out {
			out[i] = t.highlight(l, w, bgSurf)
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
		room := w - 4 - width(rt) - 2
		if room < 1 {
			rt = ""
			room = w - 6
		}

		tt = " " + truncate(title, max(1, room), g.Ellipsis) + " "
	}

	fill := max(0, w-3-width(tt)-width(rt)-1)

	return bc.Render(g.CornerTL+g.RuleLight) + tt + bc.Render(strings.Repeat(g.RuleLight, fill)) +
		rt + bc.Render(g.RuleLight+g.CornerTR)
}

// boxBottom renders "╰──── foot ─╯".
func (t *Theme) boxBottom(foot string, w int, bc lipgloss.Style) string {
	g := t.Glyphs

	var ft string
	if foot != "" {
		ft = " " + truncate(foot, max(1, w-6), g.Ellipsis) + " "
	}

	fill := max(0, w-3-width(ft))

	return bc.Render(g.CornerBL+strings.Repeat(g.RuleLight, fill)) + ft + bc.Render(g.RuleLight+g.CornerBR)
}

// card renders a summary card: a short box whose title is a dim
// upper-case label and whose right label carries the state.
func (t *Theme) card(w, h int, title, right string, lines []string) string {
	return t.box(boxOpts{W: w, H: h, Title: t.Heading.Render(title), Right: right, Lines: lines})
}

// hjoin places rendered blocks side by side with gap cells between
// them. Blocks of different heights are padded at the bottom.
func hjoin(gap int, blocks ...string) string {
	parts := make([]string, 0, 2*len(blocks))

	for i, b := range blocks {
		if i > 0 && gap > 0 {
			parts = append(parts, strings.Repeat(" ", gap))
		}

		parts = append(parts, b)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// overlay places a rendered block on a dimmed copy of base, centered in
// w by h cells. The base keeps its text around the block, so the user
// still sees where the modal came from.
func (t *Theme) overlay(base, block string, w, h int) string {
	baseLines := strings.Split(fit(base, w, h), "\n")
	blockLines := strings.Split(block, "\n")

	bw := 0
	for _, l := range blockLines {
		bw = max(bw, width(l))
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
		mid := padRight(truncate(blockLines[i-y], bw, ""), bw)

		baseLines[i] = t.Rule.Render(padRight(left, x)) + mid + t.Rule.Render(right)
	}

	return strings.Join(baseLines, "\n")
}

// bar renders a horizontal bar of w cells: the filled part in the tone
// color and the rest in the line color. The count beside it carries the
// value, so the bar is a visual aid only.
func (t *Theme) bar(n, total, w int, tone Tone) string {
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

// columns renders a column chart of h rows. Each value takes two cells
// with a one-cell gap. The top cell of a column uses an eighth-block
// glyph, so small differences stay visible at a low height.
func (t *Theme) columns(vals []int, h int, tone Tone) []string {
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

// stepper renders "✓ Client ── ● Choose ── ○ Review ── ○ Apply": done
// steps in the success color, the current step in the accent color and
// the rest faint.
func (t *Theme) stepper(steps []string, current int) string {
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

// spread places left and right text on one line of w cells.
func spread(left, right string, w int) string {
	gap := w - width(left) - width(right)
	if gap < 1 {
		return truncate(left, max(0, w-width(right)-1), "…") + " " + right
	}

	return left + strings.Repeat(" ", gap) + right
}

// cols lays out cells in fixed-width columns. Each cell is clipped with
// an ellipsis and padded, so a table never wraps a cell.
func (t *Theme) cols(cells ...colCell) string {
	var b strings.Builder

	for _, c := range cells {
		b.WriteString(padRight(truncate(c.text, c.w, t.Glyphs.Ellipsis), c.w))
	}

	return b.String()
}

// colCell is one cell of a fixed-width table row.
type colCell struct {
	text string
	w    int
}

// cell builds a colCell.
func cell(text string, w int) colCell {
	return colCell{text: text, w: w}
}
