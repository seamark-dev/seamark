package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Layout thresholds. A terminal at least WideWidth cells wide shows a
// list and its detail side by side; a narrower one shows one pane at a
// time. Below MinWidth by MinHeight the shell asks for more space.
// MinHeight is 20, because at 16 rows the confirmation dialogs lost
// their safety notes and the empty states lost their next action.
const (
	WideWidth = 100
	MinWidth  = 60
	MinHeight = 20
)

// PositionNote returns the plain "a–b of n" position of a scrolled
// window. first and last are the 1-based visible rows, and total is the
// row count. Callers add the style and the words around it. The ASCII
// glyph set gets a plain hyphen, because a view that renders outside
// the shell skips the frame-wide ASCII swap.
func (t *Theme) PositionNote(first, last, total int) string {
	dash := "–"
	if t.ASCII {
		dash = "-"
	}

	return strconv.Itoa(first) + dash + strconv.Itoa(last) + " of " + strconv.Itoa(total)
}

// ScrollNote renders the "a–b of n" position of a scrolling window,
// right-aligned, so hidden rows are never a surprise.
func (t *Theme) ScrollNote(offset, window, n, w int) string {
	note := t.PositionNote(offset+1, min(n, offset+window), n)

	return t.Faint.Render(PadLeft(note, w))
}

// RangeNote renders the faint "3–12 of 40" position of a scrolled
// window. from is the 0-based first row and to the end of the window,
// as the list offsets give them.
func (t *Theme) RangeNote(from, to, n int) string {
	return t.Faint.Render(t.PositionNote(from+1, to, n))
}

// Hints renders key bindings as "key action" pairs for the footer.
//
// Consecutive unavailable entries (see Unavailable) form one faint group
// that starts with the word n/a, for example "n/a: w retarget, v
// compare". The word carries the state without color. One word for the
// group costs fewer cells than one word for each key.
func (t *Theme) Hints(bindings []key.Binding) string {
	return t.hintRow(bindings, false)
}

// ShortHints renders the hints as Hints does, but each n/a group shows
// only its keys, for example "n/a: w t v". A footer that cannot hold
// every hint uses it, so the unavailable keys stay in view at a cost of
// two cells for each key.
func (t *Theme) ShortHints(bindings []key.Binding) string {
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
			parts = append(parts, t.Faint.Render(UnavailableWord+": "+strings.Join(off, sep)))
			off = nil
		}
	}

	for _, b := range bindings {
		if !Listed(b) {
			continue
		}

		h := b.Help()

		if IsUnavailable(b) {
			entry := t.KeyLabel(h.Key)
			if !short {
				entry += " " + h.Desc
			}

			off = append(off, entry)

			continue
		}

		flush()

		parts = append(parts, t.Key.Render(t.KeyLabel(h.Key))+" "+t.Muted.Render(h.Desc))
	}

	flush()

	// Three spaces between hints, as in the 1a footer, so a key never
	// reads as the label of its neighbour.
	return strings.Join(parts, "   ")
}

// asciiKeys spells arrow glyphs in key labels as words for the ASCII
// glyph set, whose fonts may lack the arrows.
var asciiKeys = strings.NewReplacer("↑↓", "up/down", "↑/↓", "up/down", "←→", "left/right", "↑", "up", "↓", "down", "←", "left", "→", "right")

// KeyLabel returns a key label in the session glyph set.
func (t *Theme) KeyLabel(k string) string {
	if t.ASCII {
		return asciiKeys.Replace(k)
	}

	return k
}

// DialogMinBox is the narrowest modal box, so the text inside keeps a
// usable width even in a narrow area.
const DialogMinBox = 12

// blockLines renders s at w cells and returns its lines, so a caller can
// count and clip the lines exactly as the box shows them. It returns nil
// for an empty s.
func blockLines(s string, w int) []string {
	if s == "" {
		return nil
	}

	return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}

// IsBlank reports a line that shows nothing but spaces.
func IsBlank(line string) bool {
	return strings.TrimSpace(ansi.Strip(line)) == ""
}

// collapseBlankRuns replaces each run of blank lines with one blank
// line, and removes blank lines at the start and the end.
func collapseBlankRuns(lines []string) []string {
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		if IsBlank(line) && (len(out) == 0 || IsBlank(out[len(out)-1])) {
			continue
		}

		out = append(out, line)
	}

	for len(out) > 0 && IsBlank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}

	return out
}

// DropBlankLines removes up to n blank lines, the last ones first. The
// blank lines near the top stay, because they separate the lead text
// that the reader sees first.
func DropBlankLines(lines []string, n int) []string {
	out := append([]string(nil), lines...)

	for i := len(out) - 1; i >= 0 && n > 0; i-- {
		if IsBlank(out[i]) {
			out = append(out[:i], out[i+1:]...)
			n--
		}
	}

	return out
}

// Action renders one next action with its key, for example
// "enter Apply". primary=true gives it the prominent style.
func (t *Theme) Action(k, label string, primary bool) string {
	if primary {
		return t.Key.Render(k) + " " + t.Button.Render(label)
	}

	return t.Key.Render(k) + " " + t.Ghost.Render(label)
}

// Empty renders a centered empty or error state: a title, an
// explanation, and the next valid action.
//
// The title and the next action are the most important lines, so they
// stay visible when the block is taller than h. The body gives up its
// lines first, and its last kept line ends with an ellipsis. The next
// action is clipped only when it does not fit next to the title.
func (t *Theme) Empty(tone Tone, title, body, next string, w, h int) string {
	inner := max(1, min(w-4, 72))

	head := blockLines(t.ToneStyle(tone).Render(t.ToneGlyph(tone))+" "+t.Title.Render(title), inner)

	var text []string
	if body != "" {
		text = strings.Split(Wrap(body, inner), "\n")
	}

	act := blockLines(next, inner)

	full := JoinLineBlocks(1, head, t.MutedLines(text), act)
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
	text = ClipWithEllipsis(collapseBlankRuns(text), room, inner, t.Glyphs.Ellipsis)

	lines := JoinLineBlocks(gap, head, t.MutedLines(text), act)
	if len(lines) > h {
		lines = lines[:max(0, h)]
	}

	block := lipgloss.NewStyle().Width(inner).Render(strings.Join(lines, "\n"))

	// The compact block fills the area, so it starts at the top.
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Top, block)
}

// MutedLines renders plain body lines in the muted style.
func (t *Theme) MutedLines(lines []string) []string {
	out := make([]string, len(lines))

	for i, line := range lines {
		out[i] = t.Muted.Render(line)
	}

	return out
}

// ClipWithEllipsis keeps the first n plain lines. When lines are cut,
// the last kept line ends with the ellipsis, so the cut is visible.
func ClipWithEllipsis(lines []string, n, w int, ellipsis string) []string {
	if len(lines) <= n {
		return lines
	}

	if n <= 0 {
		return nil
	}

	out := append([]string(nil), lines[:n]...)
	last := strings.TrimRight(out[n-1], " ")

	if Width(last)+Width(ellipsis) > w {
		last = Truncate(last, max(0, w-Width(ellipsis)), "")
	}

	out[n-1] = last + ellipsis

	return out
}

// JoinLineBlocks joins the non-empty line blocks with gap blank lines
// between them.
func JoinLineBlocks(gap int, blocks ...[]string) []string {
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

// SpinnerFrame returns the activity glyph for a tick count. The count
// comes from state.tick, the one injectable tick of the shell, so the
// console does not use bubbles/spinner (input.go gives the reasons).
func (t *Theme) SpinnerFrame(tick int) string {
	frames := t.Glyphs.Spinner

	return frames[tick%len(frames)]
}
