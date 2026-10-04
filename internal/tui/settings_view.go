package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/settings"
)

// This file renders the Settings destination. The list box shows the
// fields and a tail. The editor box shows the choices, the values and
// the effect of a save. Every value comes from the snapshot or from the
// draft.

// Layout of the Settings destination.
const (
	// settingsListDesignW is the outer width of the list box at 120
	// columns. A wider terminal grows the editor box.
	settingsListDesignW = 60
	// The columns of the list table. The "Set by" column takes the rest.
	settingsMarkW  = 2
	settingsFieldW = 22
	settingsValueW = 18
	// settingsLabelW is the label column of the VALUE rows: the widest
	// label, "Next refresh", and the gap of two cells of the 1a rows.
	settingsLabelW = 14
	// settingsFormW is the pattern column of the FORMS examples.
	settingsFormW = 16
)

// settingsDescriptions says what each field controls.
var settingsDescriptions = map[settings.Field]string{
	settings.AgentCLI:       "Selects the preset command that distillation and trigger extraction run.",
	settings.AgentArgv:      "A custom command that replaces the agent.cli preset.",
	settings.DistillWrite:   "Lets a reviewed apply, retarget or prune write pins.",
	settings.ReviewWindow:   "How old a mined review comment may be.",
	settings.IndexGenerated: `Files with a "Code generated ... DO NOT EDIT." header.`,
	settings.IndexExclude:   "Globs that the index build skips, on top of .gitignore and the built-in skip directories.",
}

// settingsTakesEffect says when a saved group takes effect. A save
// never starts that work itself (spec R-13). The pin-write text names
// all three previews, because the Pins retarget and prune read the same
// gate as the proposal apply (D-14). The inference text names trigger
// extraction, because it runs the same resolved command as distillation.
var settingsTakesEffect = map[settings.Group]string{
	settings.GroupInference: "At the next prepared distillation or trigger extraction. Saving does not run a " +
		"model, does not authenticate and does not change the Claude Code or Codex integration files.",
	settings.GroupPinWrite: "At the next apply, retarget or prune preview. Saving writes no pin; a new " +
		"preview then offers the write.",
	settings.GroupEvidence: "At the next confirmed evidence refresh. The stored findings stay until a refresh " +
		"succeeds.",
	settings.GroupIndexing: "At the next index build. Saving does not scan, delete or rebuild anything. " +
		"b on Overview builds the index.",
}

// settingsForms are the pattern forms that the index reader supports,
// with an example of each.
var settingsForms = [][2]string{
	{"*.pb.go", "any file with this base name"},
	{"**/testdata", "this base name in any directory"},
	{"internal/gen/", "a directory prefix from the root"},
	{"docs/*.md", "a path glob; * stays in one directory"},
}

// listBox renders the Settings box: the fields by group, then the tail
// with the draft summary and the reasons that block a save.
func (s *settingsScreen) listBox(w, h int, focused bool) string {
	t := s.st.theme

	return t.box(boxOpts{
		W: w, H: h, Focus: focused,
		Title: integPaneTitle(t, "Settings", focused), Right: t.Code.Render(settingsFile),
		Lines: s.listLines(boxInner(w), boxRows(h), focused),
	})
}

// listLines fits the table and the tail into rows lines. It drops the
// blank lines between groups first, then the column header. When the
// table still does not fit, the tail keeps the first message in one row
// and the table scrolls with the cursor.
func (s *settingsScreen) listLines(w, rows int, focused bool) []string {
	t := s.st.theme
	items := s.tailItems(w)
	rule := integRule(t, w)
	wide := s.st.width >= wideWidth

	var tail []string
	for _, it := range items {
		tail = append(tail, it.lines...)
	}

	for _, l := range []struct{ sep, header bool }{{wide, true}, {false, true}, {false, false}} {
		table, _ := s.tableLines(w, l.sep, l.header, focused)
		if len(table)+2+len(tail) <= rows {
			return append(append(table, "", rule), tail...)
		}
	}

	// A cut row ends with an ellipsis, so the user sees that text is
	// missing. A larger terminal shows the full message.
	tail = nil
	if len(items) > 0 {
		tail = []string{truncate(items[0].row, w, t.Glyphs.Ellipsis)}
	}

	table, cur := s.tableLines(w, false, false, focused)
	room := max(1, rows-1-len(tail))

	if len(table) > room {
		s.table.cursor = cur
		s.table.clamp(len(table), room)
		table = table[s.table.offset : s.table.offset+room]
	}

	return append(append(table, rule), tail...)
}

// tableLines renders the field table and returns the line of the
// cursor. sep puts a blank line between the groups; header adds the
// column names.
func (s *settingsScreen) tableLines(w int, sep, header, focused bool) (lines []string, cur int) {
	t := s.st.theme
	f := t.Faint
	setW := max(1, w-settingsMarkW-settingsFieldW-settingsValueW)
	snap := s.snap()

	if header {
		lines = append(lines, integRow(t, cell("", settingsMarkW), cell(f.Render("Field"), settingsFieldW),
			cell(f.Render("Value"), settingsValueW), cell(f.Render("Set by"), setW)))
	}

	for i, g := range settingsGroups {
		if i > 0 && sep {
			lines = append(lines, "")
		}

		// A block of one group marks its heading. A block of the whole
		// file marks no heading, because the tail explains it once.
		heading := integHeading(t, settingsGroupTitle(g))
		if snap.Blocked == nil && snap.Editable(g) != nil {
			heading += "  " + t.Attention.Render(t.Glyphs.Warn+" not editable here")
		}

		lines = append(lines, heading)

		for _, fd := range g.Fields() {
			current := fd == s.field()
			if current {
				cur = len(lines)
			}

			lines = append(lines, s.fieldRow(fd, current, focused, w, setW))
		}
	}

	return lines, cur
}

// fieldRow renders one field: the cursor, the name, the value and its
// source. A draft value and the draft marker use the attention color;
// the words keep the meaning without color.
func (s *settingsScreen) fieldRow(f settings.Field, current, focused bool, w, setW int) string {
	t := s.st.theme

	cursor := ""
	if current {
		cursor = t.Key.Render(t.Glyphs.Cursor)
	}

	name := t.Body.Render(string(f))

	switch {
	case current:
		name = t.Title.Render(string(f))
	case f == settings.AgentArgv:
		// The console only shows or removes the custom command.
		name = t.Muted.Render(string(f))
	}

	row := integRow(t, cell(cursor, settingsMarkW), cell(name, settingsFieldW),
		cell(s.valueCell(f), settingsValueW), cell(s.setByCell(f), setW))

	if current && focused {
		row = t.highlight(row, w, bgSel)
	}

	return row
}

// valueCell renders the value column: the draft value, or the saved
// value, or "unknown" when the reader fails.
func (s *settingsScreen) valueCell(f settings.Field) string {
	t := s.st.theme

	if e, ok := s.drafts[f]; ok {
		return t.Attention.Render(s.draftValue(e))
	}

	text, known := s.currentValue(f)

	switch {
	case !known:
		return t.Failure.Render(text)
	case text == "not set":
		return t.Muted.Render(text)
	}

	return t.Body.Render(text)
}

// setByCell renders the "Set by" column: the draft marker, a failure,
// the custom command that overrides the preset, a key that a block
// hides, the file, or the default.
func (s *settingsScreen) setByCell(f settings.Field) string {
	t := s.st.theme
	g := t.Glyphs

	if _, ok := s.drafts[f]; ok {
		return t.Attention.Render(g.Dot + " draft")
	}

	snap := s.snap()

	st, ok := snap.Field(f)

	switch {
	case !ok:
		return ""
	case st.Err != nil && snap.ReaderErrs[f.Reader()] != nil:
		return t.Failure.Render(g.Fail + " unreadable")
	case st.Err != nil:
		return t.Failure.Render(g.Fail + " invalid")
	case st.Source == settings.SourceShadowed:
		return t.Attention.Render("agent.argv")
	case !s.located(st):
		// The value can come from the file or from the default. The
		// settings package cannot tell while the group is blocked.
		return t.Attention.Render(g.Warn + " not editable")
	case f == settings.AgentArgv && !settingsArgvSet(snap):
		return t.Faint.Render(g.Dash)
	case st.Source == settings.SourceFile:
		return t.Faint.Render("config.yaml")
	default:
		return t.Faint.Render("default")
	}
}

// settingsTail is one message under the table. lines is the wrapped
// form. row is the one-line form of the minimum size, which keeps only
// the first message.
type settingsTail struct {
	lines []string
	row   string
}

// tailItems returns the messages under the table, most important first,
// because the minimum size keeps only the first one.
func (s *settingsScreen) tailItems(w int) []settingsTail {
	t := s.st.theme
	g := t.Glyphs
	snap := s.snap()
	group := s.field().Group()

	var out []settingsTail

	plain := func(style lipgloss.Style, text string) {
		out = append(out, settingsTail{lines: integLines(style, text, w), row: style.Render(text)})
	}

	warn := func(text string) {
		out = append(out, settingsTail{lines: propNote(t, ToneAttention, text, w), row: settingsNoteRow(t, ToneAttention, text)})
	}

	switch {
	case snap.Blocked != nil:
		out = append(out, s.blockTail("The file", snap.Blocked, w))
	case snap.Editable(group) != nil:
		out = append(out, s.blockTail(settingsGroupTitle(group), snap.Editable(group), w))
	}

	if n := s.draftTotal(); n > 0 {
		if s.st.busy() {
			warn("Saving waits for the running operation: " + oneLine(s.st.mutation) + ".")
		}

		out = append(out, s.draftSummary(n, w))
		plain(t.Faint, "A draft never saves by itself and never affects a run.")
	}

	if s.saved != nil {
		text := "Saved " + settingsGroupTitle(s.saved.group) + " at " + s.saved.at.Format("15:04:05") +
			". The index reads as stale until the next index build."
		lead := t.Success.Render(g.OK + " ")

		out = append(out, settingsTail{lines: integHanging(lead, t.Body, text, w), row: lead + t.Body.Render(text)})
	}

	if snap.Blocked == nil && !snap.Guard.Exists {
		plain(t.Faint, "No "+settingsFile+" yet. Saving creates it with the chosen keys.")
	}

	if s.st.settings.stale() {
		warn("stale: " + errText(s.st.settings.err))
	}

	if len(out) == 0 {
		plain(t.Faint, "A change stays a draft until you save it with s and y.")
	}

	return out
}

// blockTail renders a block as a tail message. Its one-line form puts
// the file location first, so the minimum size keeps the place to fix.
func (s *settingsScreen) blockTail(scope string, err error, w int) settingsTail {
	t := s.st.theme
	reason, where := settingsBlock(err)

	return settingsTail{
		lines: s.blockNote(scope, err, w),
		row:   settingsNoteRow(t, ToneAttention, "Fix "+where+" by hand: "+reason),
	}
}

// blockNote renders a block: the reason, the file location to open, and
// the manual route. The route shows once, because BlockedError.Error
// adds its own route and the view does not use that text.
func (s *settingsScreen) blockNote(scope string, err error, w int) []string {
	t := s.st.theme
	reason, where := settingsBlock(err)

	out := propNote(t, ToneAttention, scope+" cannot be edited here: "+reason+".", w)
	out = append(out, integCommandLines(t, where, w)...)

	return append(out, integLines(t.Muted, "Fix the file by hand; the other views stay usable.", w)...)
}

// settingsBlock splits a block into its reason and the file location to
// open, for example ".seamark/config.yaml:3". The reason comes from the
// file, so diag makes it safe.
func settingsBlock(err error) (reason, where string) {
	where = settingsFile

	var b *settings.BlockedError
	if !errors.As(err, &b) {
		return strings.TrimSuffix(errText(err), "."), where
	}

	if b.Line > 0 {
		where += ":" + itoa(b.Line)
	}

	return strings.TrimSuffix(diag(b.Reason), "."), where
}

// settingsBlockSentence renders a block in one sentence for a toast, for
// example "Inference cannot be edited here: <reason>; fix
// .seamark/config.yaml:3 by hand."
func settingsBlockSentence(subject string, err error) string {
	reason, where := settingsBlock(err)

	return subject + ": " + reason + "; fix " + where + " by hand."
}

// settingsNoteRow renders a tone glyph and text in one line, the
// one-line form of propNote.
func settingsNoteRow(t *Theme, tone Tone, text string) string {
	style := t.ToneStyle(tone)

	return style.Render(t.ToneGlyph(tone)) + " " + style.Render(text)
}

// draftSummary renders "1 unsaved change in Inference · s reviews and
// saves". It breaks the line before the key hint when the pane is
// narrow. The hint shows only while s can review a draft (spec R-15).
func (s *settingsScreen) draftSummary(n, w int) settingsTail {
	t := s.st.theme
	groups := s.draftGroups()

	head := propPlural(n, "unsaved change", "unsaved changes") + " in "
	hint := " reviews and saves"

	if len(groups) == 1 {
		head += settingsGroupTitle(groups[0])
	} else {
		head += itoa(len(groups)) + " groups"
		hint = " reviews the group under the cursor"
	}

	sep := " " + t.Glyphs.Sep + " "

	if !slices.ContainsFunc(groups, func(g settings.Group) bool { return s.saveBlocked(g) == "" }) {
		return s.draftBlockedSummary(head, sep, w)
	}

	row := t.Attention.Render(head) + t.Muted.Render(sep) + t.Key.Render("s") + t.Muted.Render(hint)

	if width(head+sep+"s"+hint) <= w {
		return settingsTail{lines: []string{row}, row: row}
	}

	lines := append([]string{t.Attention.Render(head)}, integHanging(t.Key.Render("s"), t.Muted, hint, w)...)

	return settingsTail{lines: lines, row: row}
}

// draftBlockedSummary renders the draft count when s can review no
// draft. A busy slot shows its reason in the line above, so the count
// stands alone. A group that cannot be edited needs a fix of the file,
// and the line says so in place of the key hint.
func (s *settingsScreen) draftBlockedSummary(head, sep string, w int) settingsTail {
	t := s.st.theme

	if s.st.busy() {
		row := t.Attention.Render(head)

		return settingsTail{lines: integLines(t.Attention, head, w), row: row}
	}

	// The words are short, so the line fits the list pane of the wide
	// layout. The block note in the tail or the group heading gives the
	// reason.
	const why = "fix the file first"

	row := t.Attention.Render(head) + t.Muted.Render(sep+why)

	if width(head+sep+why) <= w {
		return settingsTail{lines: []string{row}, row: row}
	}

	return settingsTail{lines: append(integLines(t.Attention, head, w), integLines(t.Muted, why, w)...), row: row}
}

// editorBox renders the editor of the selected field. The wide layout
// shows it beside the list at all times; the list has the keys until
// enter. The compact layout shows it alone while it has the keys.
func (s *settingsScreen) editorBox(w, h int, focused, compact bool) string {
	t := s.st.theme
	inner, rows := boxInner(w), boxRows(h)
	f := s.field()

	lines, focusAt := s.editorLines(f, inner, focused)

	sc := &s.detail
	window := max(1, rows)
	sc.page, sc.last = window, max(0, len(lines)-window)

	// The editor cursor stays visible after a move. A page key moves the
	// window freely until the next cursor move.
	if s.follow && focusAt >= 0 {
		if focusAt < sc.off {
			sc.off = focusAt
		}

		if focusAt >= sc.off+window {
			sc.off = focusAt - window + 1
		}

		s.follow = false
	}

	sc.by(0)
	end := min(len(lines), sc.off+window)

	var foot string
	if sc.scrolls() {
		foot = propRange(t, sc.off, end, len(lines))
	}

	// A narrow footer can drop esc, so the border names the way back.
	if compact && !s.ed.typing {
		back := t.Faint.Render("esc back to the list")
		if foot != "" {
			back = foot + t.Faint.Render(" "+t.Glyphs.Sep+" ") + back
		}

		foot = back
	}

	title := string(f)
	if f == settings.IndexExclude && focused {
		title += " " + t.Glyphs.Sep + " editing"
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused, Foot: foot,
		Title: integPaneTitle(t, title, focused), Lines: lines[sc.off:end],
	})
}

// editorLines renders the editor of f and returns the line of its
// cursor, or -1. The order is the same for every field: the name, the
// controls, the explanation, the values, and when a save takes effect.
func (s *settingsScreen) editorLines(f settings.Field, w int, focused bool) (out []string, focusAt int) {
	t := s.st.theme
	g := t.Glyphs
	snap := s.snap()
	st, _ := snap.Field(f)

	head := t.Title.Render(string(f))
	if _, ok := s.drafts[f]; ok {
		head = spread(head, t.Attention.Render(g.Dot+" draft"), w)
	}

	out = []string{head}
	out = append(out, integLines(t.Muted, settingsDescriptions[f], w)...)

	if err := snap.Editable(f.Group()); err != nil {
		out = append(out, "")
		out = append(out, s.blockNote(settingsGroupTitle(f.Group()), err, w)...)
	}

	if f == settings.AgentCLI && settingsArgvSet(snap) {
		argv, _ := snap.Field(settings.AgentArgv)
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, "A custom agent.argv overrides the preset:", w)...)
		out = append(out, integHanging("  ", t.Code, oneLine(argv.Effective), w)...)
	}

	controls, at := s.controlLines(f, w, focused && s.editing)

	focusAt = -1
	if at >= 0 {
		focusAt = len(out) + 1 + at
	}

	if len(controls) > 0 {
		out = append(out, "")
		out = append(out, controls...)
	}

	explain := integLines(t.Muted, s.explanation(f), w)
	for _, n := range st.Notes {
		explain = append(explain, integHanging(t.Faint.Render(g.Sep+" "), t.Muted, oneLine(n), w)...)
	}

	if len(explain) > 0 {
		out = append(out, "")
		out = append(out, explain...)
	}

	out = append(out, "", t.Heading.Render("VALUE"))
	out = append(out, s.valueLines(f, st, w)...)
	out = append(out, "", t.Heading.Render("TAKES EFFECT"))
	out = append(out, integLines(t.Body, settingsTakesEffect[f.Group()], w)...)

	return out, focusAt
}

// controlLines renders the choices of a radio editor, or the pattern
// list of index.exclude, and returns the line of the cursor, or -1.
func (s *settingsScreen) controlLines(f settings.Field, w int, active bool) (out []string, at int) {
	if f == settings.IndexExclude {
		return s.patternLines(w, active)
	}

	t := s.st.theme

	cs := s.choices(f)
	if len(cs) == 0 {
		return nil, -1
	}

	labelW := 0
	for _, c := range cs {
		labelW = max(labelW, width(c.label))
	}

	sel := s.selectedChoice(f)
	out = []string{t.Heading.Render("CHOOSE")}
	at = -1

	for i, c := range cs {
		current := active && i == s.ed.choice
		if current {
			at = len(out)
		}

		out = append(out, s.choiceRow(c, i == sel, current, labelW, w))

		if c.kind == settingsChoiceWindowDays && s.ed.typing && s.ed.target == settingsInputDays && s.ed.inputErr != "" {
			out = append(out, propIndent("    ", propNote(t, ToneFailure, s.ed.inputErr, w-4))...)
		}
	}

	// A reader failure marks no choice. The line says why, so an empty
	// radio list does not read as a fault of the console.
	if sel < 0 && s.valueUnknown(f) {
		out = append(out, integLines(t.Muted, "No choice is marked, because the saved value is unknown.", w)...)
	}

	return out, at
}

// choiceRow renders one radio option, for example
// "› (●) claude   claude -p   default". A long note is cut before the
// default tag, because the tag is the only mark of the default choice.
func (s *settingsScreen) choiceRow(c settingsChoice, on, current bool, labelW, w int) string {
	t := s.st.theme
	g := t.Glyphs
	snap := s.snap()

	mark := "  "
	if current {
		mark = t.Key.Render(g.Cursor) + " "
	}

	var note string
	if c.note != "" {
		note = t.Muted.Render(oneLine(c.note))
	}

	if c.kind == settingsChoiceWindowDays {
		_, days := s.windowChoice(snap)

		switch {
		case s.ed.typing && s.ed.target == settingsInputDays:
			note = t.Faint.Render("[") + s.ed.input.view(t) + t.Faint.Render("]")
		case on:
			note = t.Body.Render(settingsDays(days))
		}
	}

	// The default choice of the review window names itself, so a tag
	// would repeat its label.
	var tag string
	if c.def && c.kind != settingsChoiceWindowDefault {
		tag = t.Faint.Render("default")
	}

	row := mark + t.radio(on, padRight(c.label, labelW))

	room := w - width(row) - 2
	if tag != "" {
		room -= width(tag) + 2
	}

	if note != "" && room > 0 {
		row += "  " + truncate(note, room, g.Ellipsis)
	}

	if tag != "" {
		row += "  " + tag
	}

	row = truncate(row, w, g.Ellipsis)

	if current {
		row = t.highlight(row, w, bgSel)
	}

	return row
}

// patternLines renders the exclude list: numbered patterns, the input
// of a new or changed pattern, the add row, and the supported forms.
func (s *settingsScreen) patternLines(w int, active bool) (out []string, at int) {
	t := s.st.theme
	g := t.Glyphs
	snap := s.snap()
	list := s.excludeList(snap)
	numW := width(itoa(max(1, len(list)+1)))
	pad := strings.Repeat(" ", settingsMarkW+numW+2)

	// An unknown list has no count: the reader failure is not zero
	// patterns.
	unknown := s.valueUnknown(settings.IndexExclude)

	head := t.Heading.Render("PATTERNS")
	if !unknown {
		head += "  " + t.Faint.Render(itoa(len(list)))
	}

	out = []string{head}
	at = -1

	row := func(n int, text string, current bool) {
		mark := "  "
		if current {
			mark = t.Key.Render(g.Cursor) + " "
			at = len(out)
		}

		line := truncate(mark+t.Faint.Render(padLeft(itoa(n), numW))+"  "+text, w, g.Ellipsis)
		if current {
			line = t.highlight(line, w, bgSel)
		}

		out = append(out, line)
	}

	failure := func(text string) {
		out = append(out, propIndent(pad, propNote(t, ToneFailure, text, w-width(pad)))...)
	}

	pattern := s.ed.typing && s.ed.target == settingsInputPattern

	for i, p := range list {
		typing := pattern && s.ed.editIdx == i

		text := t.Code.Render(oneLine(p))
		if typing {
			text = s.ed.input.view(t)
		}

		row(i+1, text, active && s.ed.item == i)

		switch {
		case typing && s.ed.inputErr != "":
			failure(s.ed.inputErr)
		case s.excludeErrs[p] != "":
			failure(s.excludeErrs[p])
		}
	}

	adding := pattern && s.ed.editIdx < 0

	if adding {
		row(len(list)+1, s.ed.input.view(t), true)

		if s.ed.inputErr != "" {
			failure(s.ed.inputErr)
		}
	}

	switch {
	case unknown:
		out = append(out, integLines(t.Muted, "The patterns are unknown: the index reader rejects the file.", w)...)
	case len(list) == 0 && !adding:
		out = append(out, integLines(t.Muted, "No patterns: the build skips only .gitignore entries and the "+
			"built-in skip directories.", w)...)
	}

	// A blocked list offers no add row, because no draft can change it.
	if active && !adding && snap.Editable(settings.GroupIndexing) == nil {
		mark := "  "
		current := s.ed.item >= len(list)

		if current {
			mark = t.Key.Render(g.Cursor) + " "
			at = len(out)
		}

		line := mark + t.Success.Render(g.Add) + " " + t.Faint.Render("add a pattern")
		if current {
			line = t.highlight(line, w, bgSel)
		}

		out = append(out, line)
	}

	out = append(out, "", t.Heading.Render("FORMS"))

	for _, form := range settingsForms {
		out = append(out, integRow(t, cell(t.Code.Render(form[0]), settingsFormW),
			cell(t.Muted.Render(form[1]), max(1, w-settingsFormW))))
	}

	return out, at
}

// explanation returns the console text of a field. The reader notes
// follow it, so it never repeats them.
func (s *settingsScreen) explanation(f settings.Field) string {
	snap := s.snap()

	switch f {
	case settings.AgentCLI:
		switch {
		case snap.Agent == nil:
			return "The agent reader rejects the file, so Seamark cannot tell whether a custom agent.argv " +
				"overrides this preset."
		case settingsArgvSet(snap):
			return "Choosing a preset removes agent.argv in the save preview. The console never edits the " +
				"custom command itself."
		}

		return "Custom agent.argv is not set. When it is set, it overrides this preset. The console shows it " +
			"but does not edit it."
	case settings.AgentArgv:
		switch {
		case snap.Agent == nil:
			return "The agent reader rejects the file, so Seamark cannot tell whether a custom command is set."
		case settingsArgvSet(snap):
			return "Removing the custom command shows in the save preview before anything is written."
		}

		return "No custom command is set. A custom command goes in " + settingsFile + " by hand; the console " +
			"does not write one."
	case settings.DistillWrite:
		return "Saving on writes no pin; a new apply, retarget or prune preview then offers the write."
	case settings.ReviewWindow:
		return "The window is not a strict cutoff."
	case settings.IndexGenerated:
		return "Generated files stay out by default, because protoc, stringer and mockgen output inflates " +
			"the graph."
	}

	return ""
}

// valueLines renders the VALUE rows: stored, draft, default, effective
// and source, the resolved command, and a reader failure.
func (s *settingsScreen) valueLines(f settings.Field, st settings.FieldState, w int) []string {
	t := s.st.theme
	snap := s.snap()

	var out []string

	// add renders one row. ToneAccent marks a command, which uses the
	// code style of paths and commands.
	add := func(label, value string, tone Tone) {
		style := t.ToneStyle(tone)
		if tone == ToneAccent {
			style = t.Code
		}

		out = append(out, integLabeled(t, label, settingsLabelW, value, style, w)...)
	}

	stored, storedTone := "not set", ToneUnsupported

	switch {
	case st.Stored.Present:
		stored, storedTone = oneLine(st.Stored.Raw), ToneNeutral

		if stored == "" {
			stored = "(no value)"
		}
	case !s.located(st):
		// A block hides the key, so "not set" could be false.
		stored, storedTone = "unknown", ToneUnknown
	}

	add("Stored", stored, storedTone)

	if e, ok := s.drafts[f]; ok {
		add("Draft", s.draftDetail(e), ToneAttention)
	}

	add("Default", oneLine(st.Default), ToneNeutral)

	effTone := ToneNeutral
	if st.Err != nil {
		effTone = ToneFailure
	}

	add("Effective", oneLine(st.Effective), effTone)
	add("Source", s.sourceText(st), ToneNeutral)

	if f == settings.AgentCLI && snap.InvocationErr == nil && len(snap.Invocation.Argv) > 0 {
		add("Runs", settingsCommand(snap.Invocation.Argv, snap.Root), ToneAccent)
	}

	if f == settings.ReviewWindow && st.Err == nil {
		// Unsaved drafts never affect a run, so the next refresh uses
		// the saved value.
		add("Next refresh", oneLine(st.Effective)+", the saved value", ToneNeutral)
	}

	if st.Err != nil {
		out = append(out, "")
		out = append(out, propNote(t, ToneFailure, "Seamark cannot read this value: "+errText(st.Err), w)...)
	}

	return out
}

// sourceText says where the effective value comes from. A key that a
// block hides has no known source: the value can come from the file or
// from the default.
func (s *settingsScreen) sourceText(st settings.FieldState) string {
	snap := s.snap()

	switch {
	case st.Source == settings.SourceShadowed:
		return "overridden by agent.argv"
	case st.Source == settings.SourceFile:
		return settingsFile + ", line " + itoa(st.Stored.Line)
	case st.Stored.Present:
		return "built-in default; the key in line " + itoa(st.Stored.Line) + " has no value"
	case !s.located(st):
		return "unknown while the group is not editable"
	case !snap.Guard.Exists:
		return "built-in default; no " + settingsFile + " yet"
	default:
		return "built-in default; no key in the file"
	}
}
