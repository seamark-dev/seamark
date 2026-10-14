package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the selection sample (sample.go) as a modal. The
// modal takes the place of the modal of its review while the sample is
// open. Every text from a path, the index, the evidence, the lessons
// file or the engine passes through a sanitizer at the leaf. The
// sanitizers are pinEscaped, pinEscapedLine, ui.OneLine and
// settings.MuteText.

// sampleHeader is the fixed lead of the sample (contracts §4.4). It
// says what a sample does not show, so no reader takes it for a
// delivery.
const sampleHeader = "Local selection for these paths, before repeat suppression. Nothing is recorded or delivered. " +
	"An agent session can receive less: the hook gets its paths from the edit, and once-per-context mode " +
	"drops repeats."

// The side titles and the notes of a computed sample (contracts §4.4).
const (
	sampleSavedTitle  = "Saved configuration"
	sampleReviewTitle = "This review"
	// sampleModeNote explains a review that changes only the mode: the
	// two sides then select the same lessons.
	sampleModeNote = "hook_delivery changes repetition, which a sample does not simulate."
	// sampleStaleNote explains a saved side that no longer matches the
	// file on disk (services.SampleView.Stale).
	sampleStaleNote = pinfile.File + " changed after this review read it. " + sampleSavedTitle +
		" shows the text that the review started from."
)

// sampleLabelW is the label column of the rows: "Surface" and "Paths"
// fit with a gap of two cells.
const sampleLabelW = 9

// modal renders the sample in a w by h area. A cursor move asks the
// modal to show the cursor row, because only the modal knows the height
// of its window.
func (s *sample) modal(w, h int) string {
	o := s.content(ui.ModalBodyWidth(ui.ModalMinW, w))
	o.Follow = s.follow
	s.follow = false

	return s.st.theme.Modal(o, &s.scroll, w, h)
}

// content builds the modal in w cells. The body holds the header, the
// note of a mode-only review, the surface, the path rows and the last
// result. Keep holds the line range of the cursor row.
func (s *sample) content(w int) ui.ModalOpts {
	t := s.st.theme

	body := propLines(t.Muted, sampleHeader, w)

	// The note belongs to the review, not to a result. The sample opens
	// with no path, so an unavailable result must still show it.
	if s.modeOnly {
		body = append(body, propNote(t, ui.ToneNeutral, sampleModeNote, w)...)
	}

	body = append(body, "")
	body = append(body, s.surfaceLines(w)...)

	rows, keep := s.pathLines(w)
	keep = [2]int{keep[0] + len(body), keep[1] + len(body)}

	body = append(body, rows...)
	body = append(body, "")
	body = append(body, s.resultLines(w)...)

	return ui.ModalOpts{
		Title: t.Title.Render("Selection sample"), Right: t.Muted.Render(s.req.Surface.String()),
		Body: body, Actions: s.keys(true), Keep: keep,
	}
}

// surfaceLines renders the surface radio. space switches it from every
// row, so it has no cursor row of its own.
func (s *sample) surfaceLines(w int) []string {
	t := s.st.theme
	hook := s.req.Surface == services.SampleHook

	return []string{
		formRow(t, false, "Surface", sampleLabelW, radio(t, hook, "Hook selection"), w),
		strings.Repeat(" ", 2+sampleLabelW) + radio(t, !hook, "Change-set selection"),
	}
}

// pathLines renders one row per path and the add row, with the rows of
// the pin form (formRows). Only the first row has the label. It returns
// the line range of the cursor row.
func (s *sample) pathLines(w int) (lines []string, keep [2]int) {
	t := s.st.theme
	vw := max(1, w-2-sampleLabelW)
	out := formRows{t: t, labelW: sampleLabelW, w: w, cursor: s.row}
	label := "Paths"

	for i, p := range s.req.Paths {
		value := propLines(t.Body, pinEscapedLine(p), vw)
		if s.editing && s.editAt == i {
			value = s.inputLines(vw)
		}

		out.add(i, label, value, nil)
		label = ""
	}

	add := []string{t.Faint.Render("+ add a path")}
	if s.editing && s.editAt < 0 {
		add = s.inputLines(vw)
	}

	out.add(len(s.req.Paths), label, add, nil)

	return out.lines, out.keep
}

// inputLines renders the focused path input and its notice.
func (s *sample) inputLines(w int) []string {
	t := s.st.theme

	out := pinInputLines(s.input.View(t), w)
	if n := s.input.Notice(); n != "" {
		out = append(out, propNote(t, ui.ToneAttention, ui.OneLine(n), w)...)
	}

	return out
}

// resultLines renders the result of the last read: a wait, the reason of
// an unavailable sample, or the snapshot, the files and the two sides.
// An unavailable sample shows its reason only, so it never reads as two
// empty selections (contracts §4.4).
func (s *sample) resultLines(w int) []string {
	t := s.st.theme

	if !s.answers() {
		return []string{t.Muted.Render("Reading the evidence" + t.Glyphs.Ellipsis)}
	}

	v := s.view

	// The hook can reject every path. The rejected paths then say why,
	// so they show under the reason.
	if v.Unavailable != "" {
		return append(propNote(t, ui.ToneAttention, ui.OneLine(v.Unavailable), w), s.rejectedLines(v, w)...)
	}

	snapshot := sampleSnapshot(v)
	if s.reading {
		snapshot += " " + t.Glyphs.Sep + " reading again" + t.Glyphs.Ellipsis
	}

	out := propLines(t.Muted, snapshot, w)

	if v.Stale {
		out = append(out, propNote(t, ui.ToneAttention, sampleStaleNote, w)...)
	}

	out = append(out, "")
	out = append(out, s.fileLines(v, w)...)
	out = append(out, "")

	saved, review := sampleSides(v)
	pw := pairWidth(w)
	left := s.sideLines(saved, v.Current, v.CurrentThreshold, pw)
	right := s.sideLines(review, v.Candidate, v.CandidateThreshold, pw)

	return append(out, pair(t, t.Title.Render(sampleSavedTitle), t.Title.Render(sampleReviewTitle), left, right, w)...)
}

// fileLines lists the files of the selection as the surface names them,
// each with its labels, then the paths that the hook rejects. The
// labels say that a lesson can still match such a path.
func (s *sample) fileLines(v *services.SampleView, w int) []string {
	t := s.st.theme
	sep := " " + t.Glyphs.Sep + " "
	out := []string{t.Heading.Render("FILES")}

	for _, p := range v.Paths {
		var labels []string

		if slices.Contains(v.NoFile, p) {
			labels = append(labels, "no file yet")
		}

		if slices.Contains(v.Unindexed, p) {
			labels = append(labels, "not in the index")
		}

		text := pinEscapedLine(p)
		if len(labels) > 0 {
			text += sep + strings.Join(labels, sep)
		}

		out = append(out, propLines(t.Body, text, w)...)
	}

	return append(out, s.rejectedLines(v, w)...)
}

// rejectedLines lists the paths that the hook rejects, each with its
// reason. The engine keeps the first few only (delivery.NormalizePaths).
func (s *sample) rejectedLines(v *services.SampleView, w int) []string {
	t := s.st.theme
	sep := " " + t.Glyphs.Sep + " "

	var out []string

	for _, r := range v.Rejected {
		out = append(out, propNote(t, ui.ToneAttention, pinEscapedLine(r.Path)+sep+"rejected: "+
			ui.OneLine(string(r.Reason)), w)...)
	}

	return out
}

// sideLines renders one side in w cells: each selected lesson with its
// marker, the held-back count, and each excluded mined lesson with every
// reason. threshold is the threshold of this side.
func (s *sample) sideLines(rows []sampleRow, sel report.Selection, threshold, w int) []string {
	t := s.st.theme
	sep := " " + t.Glyphs.Sep + " "

	var out []string

	if len(rows) == 0 {
		out = append(out, propLines(t.Faint, "No lesson selected for these paths.", w)...)
	}

	for _, r := range rows {
		out = append(out, propLines(t.Body, s.rowText(r), w)...)

		mark := r.mark.String()
		if mark == "" {
			continue
		}

		// A removal names the reason of the other side. An added lesson
		// shows its earlier reason under the saved side's exclusions.
		if r.why != "" && r.mark == sampleRemoved {
			mark += sep + ui.OneLine(r.why)
		}

		out = append(out, propIndent("  ", propLines(sampleMarkStyle(t, r.mark), mark, w-2))...)
	}

	out = append(out, "")
	out = append(out, propLines(t.Muted, sampleHeldText(s.view.Surface, sel.HeldBack), w)...)

	if len(sel.Excluded) == 0 {
		return out
	}

	out = append(out, "", t.Heading.Render("EXCLUDED MINED LESSONS"))

	for _, ex := range sel.Excluded {
		out = append(out, propLines(t.Body, sampleMinedText(t, ex.Lesson), w)...)
		out = append(out, propIndent("  ", propLines(t.Muted, ui.OneLine(sampleExclusionWords(ex, threshold)), w-2))...)
	}

	return out
}

// rowText renders a selected lesson as the surfaces list it: a pin with
// the delivery rendering of pinGuidance, a mined lesson with its
// recurrence tag.
func (s *sample) rowText(r sampleRow) string {
	if r.pin != nil {
		return pinGuidance(s.st.theme, *r.pin)
	}

	return sampleMinedText(s.st.theme, r.lesson)
}

// sampleMinedText renders a mined lesson as "×N · region · symptom",
// with the recurrence tag of the hook. The text comes from review
// comments, so pinEscapedLine shows each hidden character as an escape.
func sampleMinedText(t *ui.Theme, l domain.Lesson) string {
	sep := " " + t.Glyphs.Sep + " "

	return pinEscapedLine("×" + strconv.Itoa(l.Occurrences) + sep + l.Region + sep + l.Symptom)
}

// sampleMarkStyle returns the style of a marker. The words carry the
// marker, so the style only adds emphasis.
func sampleMarkStyle(t *ui.Theme, m sampleMark) lipgloss.Style {
	switch m {
	case sampleAdded:
		return t.ToneStyle(ui.ToneSuccess)
	case sampleRemoved, sampleReworded:
		return t.ToneStyle(ui.ToneAttention)
	}

	return t.Muted
}

// sampleSnapshot labels the evidence of a result: the time of the read
// and the time of the last index build (contracts §4.4).
func sampleSnapshot(v *services.SampleView) string {
	text := "evidence read " + sampleClock(v.ReadAt, v.ReadAt)
	if v.IndexedAt.IsZero() {
		return text + ", index build time unknown"
	}

	return text + ", index built " + sampleClock(v.IndexedAt, v.ReadAt)
}

// sampleClock returns the clock time of at. It adds the date when at is
// on another day than ref, so an old index build never reads as today.
func sampleClock(at, ref time.Time) string {
	if at.Year() == ref.Year() && at.YearDay() == ref.YearDay() {
		return at.Format("15:04")
	}

	return at.Format("2006-01-02 15:04")
}
