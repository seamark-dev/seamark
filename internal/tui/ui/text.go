package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// OneLine makes untrusted text safe for a single terminal line.
// Repository content, stored text and file names can hold escape
// sequences; the sanitizer keeps them inert.
func OneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s)

	return render.SanitizeBlock(s)
}

// MultiLine makes untrusted multi-line text safe and keeps its line
// breaks. Tabs become spaces because they break width arithmetic.
func MultiLine(s string) string {
	return strings.ReplaceAll(render.SanitizeBlock(s), "\t", "    ")
}

// Diag makes a diagnostic safe: it redacts secrets first, because
// reader errors and native settings can quote tokens.
func Diag(s string) string {
	return OneLine(redact.Secrets(s))
}

// ErrText returns a safe one-line error message, or "" for nil.
func ErrText(err error) string {
	if err == nil {
		return ""
	}

	return Diag(err.Error())
}

// Width returns the display width of s in terminal cells.
func Width(s string) int {
	return lipgloss.Width(s)
}

// Truncate shortens styled or plain text to w cells with an ellipsis.
func Truncate(s string, w int, ellipsis string) string {
	if w <= 0 {
		return ""
	}

	return ansi.Truncate(s, w, ellipsis)
}

// TruncateLeft shortens s from the left, which keeps the most specific
// part of a path visible.
func TruncateLeft(s string, w int, ellipsis string) string {
	if Width(s) <= w {
		return s
	}

	if w <= Width(ellipsis) {
		return Truncate(s, w, "")
	}

	return ellipsis + ansi.TruncateLeft(s, Width(s)-w+Width(ellipsis), "")
}

// Wrap word-wraps text to w cells. It breaks a line only at a space,
// so a command, a flag or a path that fits on one line stays whole.
// ansi.Wrap is not used, because it always breaks after a '-'. Each
// input line wraps on its own, so existing line breaks stay.
func Wrap(s string, w int) string {
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
	if Width(line) <= w {
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

		ww := Width(word)

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

			curW, gap = Width(last), 0

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

// Fit clips or pads rendered text to exactly w cells by h lines. The
// shell needs exact sizes so a pane never pushes the footer away.
func Fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}

	for i, l := range lines {
		lines[i] = PadRight(Truncate(l, w, ""), w)
	}

	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}

	return strings.Join(lines, "\n")
}

// PadRight pads s with spaces to w cells.
func PadRight(s string, w int) string {
	if gap := w - Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}

	return s
}

// PadLeft pads s on the left to w cells, for right-aligned counts.
func PadLeft(s string, w int) string {
	if gap := w - Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}

	return s
}

// DisplayPath shortens an absolute path for the header: the home
// directory becomes "~".
func DisplayPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}

	return p
}

// JoinBlocks joins non-empty blocks with a blank line between them.
func JoinBlocks(blocks ...string) string {
	var out []string

	for _, b := range blocks {
		if b != "" {
			out = append(out, b)
		}
	}

	return strings.Join(out, "\n\n")
}
