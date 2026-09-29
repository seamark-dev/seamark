package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// oneLine makes untrusted text safe for a single terminal line.
// Repository content, proposal text and file names can hold escape
// sequences; the sanitizer keeps them inert.
func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s)

	return render.SanitizeBlock(s)
}

// multiLine makes untrusted multi-line text safe and keeps its line
// breaks. Tabs become spaces because they break width arithmetic.
func multiLine(s string) string {
	return strings.ReplaceAll(render.SanitizeBlock(s), "\t", "    ")
}

// diag makes a diagnostic safe: it redacts secrets first, because
// reader errors and native settings can quote tokens.
func diag(s string) string {
	return oneLine(redact.Secrets(s))
}

// errText returns a safe one-line error message, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}

	return diag(err.Error())
}

// width returns the display width of s in terminal cells.
func width(s string) int {
	return lipgloss.Width(s)
}

// truncate shortens styled or plain text to w cells with an ellipsis.
func truncate(s string, w int, ellipsis string) string {
	if w <= 0 {
		return ""
	}

	return ansi.Truncate(s, w, ellipsis)
}

// truncateLeft shortens s from the left, which keeps the most specific
// part of a path visible.
func truncateLeft(s string, w int, ellipsis string) string {
	if width(s) <= w {
		return s
	}

	if w <= width(ellipsis) {
		return truncate(s, w, "")
	}

	return ellipsis + ansi.TruncateLeft(s, width(s)-w+width(ellipsis), "")
}

// wrap word-wraps text to w cells. It breaks a line only at a space,
// so a command, a flag or a path that fits on one line stays whole.
// ansi.Wrap is not used, because it always breaks after a '-'. Each
// input line wraps on its own, so existing line breaks stay.
func wrap(s string, w int) string {
	if w <= 0 {
		return ""
	}

	lines := strings.Split(s, "\n")

	for i, line := range lines {
		lines[i] = wrapLine(line, w)
	}

	return strings.Join(lines, "\n")
}

// wrapLine wraps one line to w cells. Leading spaces stay as the
// indentation of the first output line; the spaces at a break go away.
// A token wider than w starts a new line and breaks at the cell limit,
// because no line may overflow the pane.
func wrapLine(line string, w int) string {
	if width(line) <= w {
		return line
	}

	var (
		out  []string
		cur  strings.Builder
		curW int
	)

	flush := func() {
		out = append(out, cur.String())
		cur.Reset()

		curW = 0
	}

	// gap counts the spaces before the next word. strings.Split gives
	// one empty token for each extra space in a run.
	gap := 0

	for i, word := range strings.Split(line, " ") {
		if i > 0 {
			gap++
		}

		if word == "" {
			continue
		}

		ww := width(word)

		if curW > 0 && curW+gap+ww > w {
			flush()
		}

		// A new output line drops the spaces at the break. Only the
		// first line keeps its indentation, and only when it fits.
		if curW == 0 && (len(out) > 0 || gap+ww > w) {
			gap = 0
		}

		if ww > w {
			pieces := strings.Split(ansi.Hardwrap(word, w, true), "\n")
			out = append(out, pieces[:len(pieces)-1]...)
			last := pieces[len(pieces)-1]

			cur.WriteString(last)

			curW, gap = width(last), 0

			continue
		}

		cur.WriteString(strings.Repeat(" ", gap) + word)

		curW += gap + ww
		gap = 0
	}

	if gap > 0 && curW > 0 && curW+gap <= w {
		cur.WriteString(strings.Repeat(" ", gap))
	}

	flush()

	return strings.Join(out, "\n")
}

// fit clips or pads rendered text to exactly w cells by h lines. The
// shell needs exact sizes so a pane never pushes the footer away.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}

	for i, l := range lines {
		lines[i] = padRight(truncate(l, w, ""), w)
	}

	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}

	return strings.Join(lines, "\n")
}

// padRight pads s with spaces to w cells.
func padRight(s string, w int) string {
	if gap := w - width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}

	return s
}

// padLeft pads s on the left to w cells, for right-aligned counts.
func padLeft(s string, w int) string {
	if gap := w - width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}

	return s
}

// displayPath shortens an absolute path for the header: the home
// directory becomes "~".
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}

	return p
}

// joinBlocks joins non-empty blocks with a blank line between them.
func joinBlocks(blocks ...string) string {
	var out []string

	for _, b := range blocks {
		if b != "" {
			out = append(out, b)
		}
	}

	return strings.Join(out, "\n\n")
}
