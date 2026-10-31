package integration

// The write preview. It pairs each planned write with the file as it is
// now, so a reviewer sees the exact change before apply. The diff is for
// display only: apply writes the planned bytes, never the diff.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/seamark-dev/seamark/internal/render"
)

// previewContext is the number of unchanged lines around each change.
// Three lines is the unified diff default that reviewers expect.
const previewContext = 3

// noNewlineMarker stands for a missing newline at the end of a file.
// It is a line of its own, so a change of only the final newline shows
// in the diff, and every line keeps its "+", "-" or " " marker.
const noNewlineMarker = `\ No newline at end of file` + "\n"

// WritePreview pairs one planned write with the current file content so
// a reviewer sees the exact change before apply. Native client files
// hold the tokens of other servers. So the diff hides credential values
// by key and by table (see preview_redact.go). Then it applies
// redact.Secrets and a block sanitizer to every line.
type WritePreview struct {
	// Path is the repository-relative path of the write.
	Path string
	// Created is true when the file does not exist yet.
	Created bool
	// Diff is a unified diff with explicit "+" and "-" line markers.
	Diff string
	// Added and Removed count the changed lines.
	Added, Removed int
	// Consumers names the clients that contribute to the write; empty
	// for shared Seamark documents.
	Consumers []string
	// Detail is the planner's one-line description of the write.
	Detail string
	// Stale is true when the file changed after planning, so the plan
	// can no longer apply.
	Stale bool
	// Err holds a read failure of the current file.
	Err error
}

// PreviewWrites reads the current content of every planned write and
// returns one preview per write, in plan order. It writes nothing.
//
// It reads each file by the rule of its plan guard, the same rule that
// apply uses. A file that differs from its guard makes the preview
// stale. A read failure also makes it stale, because apply refuses the
// plan for the same failure.
func PreviewWrites(plan *SetupPlan) []WritePreview {
	if plan == nil {
		return nil
	}

	previews := make([]WritePreview, 0, len(plan.Writes))

	for _, write := range plan.Writes {
		previews = append(previews, previewWrite(plan, write))
	}

	return previews
}

// previewWrite builds the preview of one planned write.
func previewWrite(plan *SetupPlan, write FileWrite) WritePreview {
	preview := WritePreview{
		Path:      write.Path,
		Consumers: slices.Clone(write.Consumers),
		Detail:    render.Sanitize(write.Detail),
	}

	i := guardIndex(plan.Reads, write.Path)
	if i < 0 {
		// PlanSetup refuses such a write, so only a hand-made plan gets here.
		preview.Err = fmt.Errorf("%s: the write has no read guard", write.Path)

		return preview
	}

	planned := plan.Reads[i]
	preview.Created = !planned.Exists

	now, current, err := observeContent(plan.Root, planned)
	if err != nil {
		preview.Stale, preview.Err = true, err

		return preview
	}

	preview.Stale = now != planned
	preview.Diff, preview.Added, preview.Removed = UnifiedDiff(write.Path, current, write.After)

	return preview
}

// UnifiedDiff renders the display diff between two versions of the file
// at rel, with the same redaction and sanitizing as PreviewWrites. A
// planner calls it when its write is blocked, for example by a link, so
// the user still sees the exact change. Added and removed count the
// changed lines. The diff is for display only; never apply it.
func UnifiedDiff(rel string, before, after []byte) (diff string, added, removed int) {
	return unifiedDiff(rel, newDiffSide(rel, before), newDiffSide(rel, after))
}

// diffLines splits text into lines that each end with a newline. A
// last line without a newline gets one, followed by noNewlineMarker,
// so the diff never joins two lines and still shows the difference.
func diffLines(text string) []string {
	if text == "" {
		return nil
	}

	lines := strings.SplitAfter(text, "\n")

	last := lines[len(lines)-1]
	if last == "" {
		return lines[:len(lines)-1]
	}

	return append(lines[:len(lines)-1], last+"\n", noNewlineMarker)
}

// unifiedDiff renders the diff between two sides in the unified
// format of difflib and counts the added and removed lines. It writes
// the lines itself, because each line needs the hidden ranges of its
// own side, and difflib prints only the compared text. The line
// matching still compares the real lines, so the hunks and counts
// describe the real change.
//
// The counts come from the same hunks as the text, so the line
// matching runs once. A hunk holds every change in full, because
// difflib trims only the unchanged ranges.
//
// Every line goes through scrubLine on its own, because a quoted value
// can span lines and must not join two lines into one match. The
// result is then sanitized, so control sequences in a file stay inert
// in a terminal.
func unifiedDiff(rel string, before, after diffSide) (diff string, added, removed int) {
	groups := difflib.NewMatcher(before.lines, after.lines).GetGroupedOpCodes(previewContext)
	if len(groups) == 0 {
		return "", 0, 0
	}

	var out strings.Builder

	// The first byte is the line marker. The redaction must never
	// remove it, so only the rest of the line is scrubbed.
	line := func(marker byte, text string, hide []maskRange) {
		out.WriteByte(marker)
		out.WriteString(scrubLine(rel, applyMasks(text, hide)))
	}

	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", rel, rel)

	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", unifiedRange(first.I1, last.I2), unifiedRange(first.J1, last.J2))

		for _, op := range group {
			if op.Tag == 'e' {
				// The line shows once, so it hides what either side hides.
				for i, j := op.I1, op.J1; i < op.I2; i, j = i+1, j+1 {
					line(' ', before.lines[i], mergeMasks(before.hideAt(i), after.hideAt(j)))
				}

				continue
			}

			// A delete has an empty range on the after side, and an insert
			// has one on the before side. So one rule prints every change.
			for i := op.I1; i < op.I2; i++ {
				line('-', before.lines[i], before.hideAt(i))
			}

			for j := op.J1; j < op.J2; j++ {
				line('+', after.lines[j], after.hideAt(j))
			}

			removed += fileLineCount(before.lines[op.I1:op.I2])
			added += fileLineCount(after.lines[op.J1:op.J2])
		}
	}

	return render.SanitizeBlock(out.String()), added, removed
}

// unifiedRange formats a hunk range as difflib does: the first line
// number, and the length when it is not one. An empty range starts at
// the line before it.
func unifiedRange(start, stop int) string {
	first, length := start+1, stop-start

	switch length {
	case 1:
		return strconv.Itoa(first)
	case 0:
		first--
	}

	return fmt.Sprintf("%d,%d", first, length)
}

// fileLineCount counts the diff lines that are lines of the file. The
// missing-newline marker is not a line of the file, so it does not
// count.
func fileLineCount(lines []string) int {
	n := 0

	for _, line := range lines {
		if line != noNewlineMarker {
			n++
		}
	}

	return n
}
