package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The helpers in this file build the content lines of the Integrations
// boxes and the setup wizard. A box clips every line on its own, so each
// helper returns lines that already fit and carry their own styles.

// integLines wraps plain text to w cells and styles every line on its
// own. A style that spans a line break would also paint the box border
// between the lines, so no style may cross a line end.
func integLines(style lipgloss.Style, s string, w int) []string {
	if s == "" {
		return nil
	}

	lines := strings.Split(wrap(s, max(1, w)), "\n")
	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	return lines
}

// integHanging wraps plain text to w cells with a styled lead on the
// first line. The continuation lines start under the text, so a bullet
// or a label keeps its column.
func integHanging(lead string, style lipgloss.Style, s string, w int) []string {
	pad := width(lead)
	lines := integLines(style, s, max(1, w-pad))

	for i := range lines {
		if i == 0 {
			lines[i] = lead + lines[i]

			continue
		}

		lines[i] = strings.Repeat(" ", pad) + lines[i]
	}

	if len(lines) == 0 {
		return []string{lead}
	}

	return lines
}

// integLabeled renders a muted label column of labelW cells and a value
// that wraps in the rest of the width, as in the "Now" and "After" rows
// of the setup design.
func integLabeled(t *Theme, label string, labelW int, value string, style lipgloss.Style, w int) []string {
	return integHanging(t.Muted.Render(padRight(label, labelW)), style, value, w)
}

// integRow lays out styled cells in fixed columns. Every cell except the
// last is clipped one cell short of its column, so a full cell never
// touches the next one. The last cell takes its width without a gap.
func integRow(t *Theme, cells ...colCell) string {
	var b strings.Builder

	for i, c := range cells {
		last := i == len(cells)-1

		room := c.w
		if !last {
			room--
		}

		text := truncate(c.text, max(0, room), t.Glyphs.Ellipsis)
		if !last {
			text = padRight(text, c.w)
		}

		b.WriteString(text)
	}

	return b.String()
}

// integHeading renders an upper-case section label.
func integHeading(t *Theme, label string) string {
	return t.Heading.Render(strings.ToUpper(label))
}

// integRule renders a horizontal rule of w cells in the line color.
func integRule(t *Theme, w int) string {
	return t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, max(0, w)))
}

// integPaneTitle renders a box title. The pane with focus, or the only
// pane, gets the title style; a secondary pane gets the muted style. It
// sanitizes the title, because it can hold a client name from an
// inspection.
func integPaneTitle(t *Theme, title string, primary bool) string {
	return boxTitle(t, oneLine(title), primary)
}

// integBoxed renders a centered state block, for example a loading or
// an error state, inside a box of w by h cells.
func integBoxed(t *Theme, o boxOpts, tone Tone, title, body, next string) string {
	block := t.empty(tone, title, body, next, boxInner(o.W), boxRows(o.H))
	o.Lines = strings.Split(block, "\n")

	return t.box(o)
}

// integCommandLines renders a corrective command or edit as selectable
// text after the arrow glyph, wrapped to w cells. The console never runs
// the text; it is reference text only.
func integCommandLines(t *Theme, s string, w int) []string {
	return integHanging(t.Muted.Render(t.Glyphs.Arrow+" "), t.Code, oneLine(s), w)
}

// integIndent puts pad spaces before every line.
func integIndent(lines []string, pad int) []string {
	prefix := strings.Repeat(" ", pad)
	out := make([]string, len(lines))

	for i, l := range lines {
		out[i] = prefix + l
	}

	return out
}
