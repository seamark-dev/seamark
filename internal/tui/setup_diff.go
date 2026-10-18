package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The diff of the review step. Each row has a gutter with the old and
// the new line number, a sign column, and the text. The numbers come
// from the hunk headers of the unified diff, so they match the files.

// setupDiffRow is one line of a unified diff with its line numbers. A
// zero number means that the line does not exist on that side.
type setupDiffRow struct {
	sign     byte
	old, new int
	text     string
}

// setupParseDiff reads a unified diff into rows. It drops the file
// header lines, because the box title already names the file. A line
// outside a hunk keeps its sign and gets no numbers.
func setupParseDiff(diff string) []setupDiffRow {
	text := strings.TrimRight(ui.MultiLine(diff), "\n")
	if text == "" {
		return nil
	}

	var (
		rows     []setupDiffRow
		old, now int
		inHunk   bool
	)

	for _, line := range strings.Split(text, "\n") {
		if !inHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			continue
		}

		if line == "" {
			line = " "
		}

		sign, rest := line[0], line[1:]

		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			old, now = setupHunkStart(line)
			rows = append(rows, setupDiffRow{sign: '@', text: line})
		case sign == '+':
			rows = append(rows, setupDiffRow{sign: '+', new: now, text: rest})
			now++
		case sign == '-':
			rows = append(rows, setupDiffRow{sign: '-', old: old, text: rest})
			old++
		case sign == ' ':
			rows = append(rows, setupDiffRow{sign: ' ', old: old, new: now, text: rest})
			old++
			now++
		default:
			// A "\ No newline at end of file" marker belongs to the line
			// before it and has no number of its own.
			rows = append(rows, setupDiffRow{sign: sign, text: rest})
		}
	}

	return rows
}

// setupHunkStart returns the first old and new line numbers of a hunk
// header "@@ -a,b +c,d @@". An empty range names the line before it, so
// its first line is one more. A header it cannot read starts at line 1.
func setupHunkStart(header string) (old, now int) {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return 1, 1
	}

	return setupRangeStart(strings.TrimPrefix(fields[1], "-")), setupRangeStart(strings.TrimPrefix(fields[2], "+"))
}

// setupRangeStart reads one "start,length" range of a hunk header.
func setupRangeStart(r string) int {
	startText, lengthText, hasLength := strings.Cut(r, ",")

	start, err := strconv.Atoi(startText)
	if err != nil {
		return 1
	}

	if hasLength {
		if n, err := strconv.Atoi(lengthText); err == nil && n == 0 {
			return start + 1
		}
	}

	return max(1, start)
}

// setupDiffLines renders a unified diff as box lines of w cells. A long
// line wraps onto rows with an empty gutter and the same sign, so the
// review hides no part of a change and every row keeps its sign when
// color is off. Added and removed rows get a tinted background.
func setupDiffLines(t *ui.Theme, diff string, w int) []string {
	rows := setupParseDiff(diff)
	if len(rows) == 0 {
		return integLines(t.Muted, "The diff is empty: the file content does not change.", w)
	}

	numW := 3
	for _, r := range rows {
		numW = max(numW, len(strconv.Itoa(max(r.old, r.new))))
	}

	gutterW := 2*numW + 2
	textW := max(4, w-gutterW-2)
	blank := strings.Repeat(" ", gutterW)

	var lines []string

	for _, r := range rows {
		gutter := t.Faint.Render(setupNumber(r.old, numW) + " " + setupNumber(r.new, numW) + " ")
		sign, textStyle, bg := setupSignStyle(t, r.sign)

		for i, piece := range strings.Split(ansi.Hardwrap(r.text, textW, true), "\n") {
			lead := gutter
			if i > 0 {
				lead = blank
			}

			line := lead + sign + textStyle.Render(piece)
			if bg >= 0 {
				line = t.Highlight(line, w, bg)
			}

			lines = append(lines, line)
		}
	}

	return lines
}

// setupNumber right-aligns a line number in w cells; zero is blank.
func setupNumber(n, w int) string {
	if n == 0 {
		return strings.Repeat(" ", w)
	}

	return ui.PadLeft(strconv.Itoa(n), w)
}

// setupSignStyle returns the styled sign column, the text style and the
// row background of one diff sign. The sign keeps the meaning without
// color: "+" added, "−" removed, "@" a hunk header.
func setupSignStyle(t *ui.Theme, sign byte) (signCell string, text lipgloss.Style, bg int) {
	g := t.Glyphs

	switch sign {
	case '+':
		return t.Success.Render(g.Add) + " ", t.Body, ui.BgAdd
	case '-':
		return t.Failure.Render(g.Del) + " ", t.Body, ui.BgDel
	case '@':
		return t.Info.Render("@") + " ", t.Info, -1
	case ' ':
		return "  ", t.Muted, -1
	default:
		return t.Faint.Render(string(sign)) + " ", t.Faint, -1
	}
}
