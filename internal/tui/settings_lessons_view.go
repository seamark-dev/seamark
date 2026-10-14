package tui

import (
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the lessons groups of Settings (contracts §4.3). It
// renders the activation of a lessons field and the repetition help of
// hook_delivery. It also renders the mute editor, its record form, and
// the mute parts of a review. Every mute value is file text or typed
// text, and every client fact is inspection text. So each one goes
// through ui.OneLine or ui.Diag.

// settingsRecordLabelW is the label column of the record form: the
// cursor mark, "Region", and two cells.
const settingsRecordLabelW = 10

// settingsFactLabelW is the label column of the reset facts: an indent
// of two cells, "verification", and two cells.
const settingsFactLabelW = 16

// lessonsActivationLines renders when a lessons field takes effect: the
// consumers that read it (settings.Field.Activation), then its notes.
// The settings package owns the texts, so the detail and the review say
// the same.
func (s *settingsScreen) lessonsActivationLines(f settings.Field, w int) []string {
	t := s.st.theme
	a := f.Activation()

	out := propLines(t.Body, "Affects "+a.When+".", w)

	for _, n := range a.Notes {
		out = append(out, integHanging(t.Faint.Render(t.Glyphs.Sep+" "), t.Muted, n, w)...)
	}

	return out
}

// settingsRepetitionRule is part one of the hook_delivery help: the
// rule of the delivery engine, which holds for every client.
const settingsRepetitionRule = "The hook drops a repeat only when the edit event names a receiving " +
	"context that has a supported reset. Otherwise it repeats each reminder within the budget, also in " +
	"once-per-context mode."

// settingsRepetitionRoute is part three of the hook_delivery help.
const settingsRepetitionRoute = "See Integrations (2) and Doctor (4) for the full checks of each client."

// deliveryModeLines renders the help of hook_delivery in three fixed
// parts (contracts §4.3, F08). The parts are the engine rule, the
// inspection facts of each client as they are, and the route to
// Integrations and Doctor. It
// never states that a client supports a reset. It never reads a declared
// capability or an executable on PATH, because neither proves that a
// reset reaches the hook.
func (s *settingsScreen) deliveryModeLines(w int) []string {
	t := s.st.theme

	out := []string{t.Heading.Render("REPETITION")}
	out = append(out, propLines(t.Body, settingsRepetitionRule, w)...)
	out = append(out, "")
	out = append(out, s.resetFactLines(w)...)
	out = append(out, "")

	return append(out, propLines(t.Muted, settingsRepetitionRoute, w)...)
}

// resetFactLines quotes the reset facts of the client inspections. The
// facts are the state of the reset hook, the verification level and the
// detail of the resets capability, and every finding. An unread
// inspection says so.
func (s *settingsScreen) resetFactLines(w int) []string {
	t := s.st.theme
	sec := &s.st.integrations

	switch {
	case sec.err != nil && !sec.loaded:
		return propLines(t.Muted, "The client inspections could not be read: "+ui.ErrText(sec.err), w)
	case !sec.loaded:
		return propLines(t.Muted, "The client inspections are not read yet.", w)
	case len(sec.value) == 0:
		return propLines(t.Muted, "The inspections name no client.", w)
	}

	var out []string

	for i, insp := range sec.value {
		if i > 0 {
			out = append(out, "")
		}

		name := insp.Name
		if name == "" {
			name = insp.ClientID
		}

		out = append(out, t.Title.Render(ui.OneLine(name)))

		for _, fact := range settingsResetFacts(insp) {
			out = append(out, integLabeled(t, "  "+fact[0], settingsFactLabelW, fact[1], t.Body, w)...)
		}

		out = append(out, integIndent(integFindingLines(t, insp.Findings, max(1, w-2), true), 2)...)
	}

	return out
}

// settingsResetFacts returns the reset facts of one inspection as label
// and value pairs. Each value is the name that the inspection gives. The
// Integrations table shows the same state names, so both views agree.
func settingsResetFacts(insp integration.Inspection) [][2]string {
	c, ok := insp.Entry(integration.CapabilityResets)
	if !ok {
		return [][2]string{{"reset hook", "no inspection of the reset hook"}}
	}

	// An unsupported capability has no configuration state. The words
	// are the words of CapabilityInspection.Describe.
	state := c.State.String()
	if !c.Supported {
		state = "not supported"
	}

	out := [][2]string{
		{"reset hook", state},
		{"verification", c.Verification.Level.String()},
	}

	if d := ui.Diag(c.Detail); d != "" {
		out = append(out, [2]string{"detail", d})
	}

	return out
}

// muteLines renders the mute editor: one row per entry of the file, the
// drafted new entry, the add row, and the open record form. It returns
// the line of the cursor, or -1, and the last line that belongs to the
// cursor. That last line is the last note of the cursor row, or the end
// of an open record form. So the verdicts show with the cursor. active
// is true while the editor has the keys; only then do the cursor and the
// add row show.
func (s *settingsScreen) muteLines(w int, active bool) (out []string, at, end int) {
	t := s.st.theme
	entries := s.muteEntries()

	// A reader failure without a parsed list is not an empty list.
	unknown := s.valueUnknown(settings.Mutes) && len(entries) == 0

	head := t.Heading.Render("MUTES")
	if !unknown {
		head += "  " + t.Faint.Render(strconv.Itoa(len(entries)))
	}

	r := newSettingsListRows(t, w, len(entries)+1, head)
	rows := s.muteRows()
	draft := s.muteDraft()
	end = -1

	for i, row := range rows {
		current := active && s.ed.item == i && s.ed.rec == nil

		switch row.kind {
		case settingsMuteRowEntry:
			s.muteEntryRow(r, row.entry, i+1, current, draft)
		case settingsMuteRowDraft:
			r.row(i+1, t.Attention.Render(muteDraftText(draft)), current)
			r.note(ui.ToneAttention, "draft: a new entry after the last one")
		case settingsMuteRowAdd:
			if active {
				r.addRow("add mute", current)
			}
		}

		// The notes under a row hold its verdict, for example "duplicate
		// of line 3". A cursor row at the window edge keeps them in view.
		if current {
			end = len(r.out) - 1
		}
	}

	switch {
	case unknown:
		r.out = append(r.out, integLines(t.Muted, "The entries are unknown: the lessons reader rejects the file.", w)...)
	case len(entries) == 0 && draft == nil:
		r.out = append(r.out, integLines(t.Muted, "No mutes: every mined lesson above the threshold can surface.", w)...)
	case s.muteDraftStale():
		// The planner refuses such a draft, so the editor says so before
		// the review.
		r.out = append(r.out, propNote(t, ui.ToneAttention, "The entry of the draft changed since the draft "+
			"started, so a save refuses it. x in the Settings list discards the draft; then choose the entry again.",
			w)...)
	}

	out, at = r.out, r.at

	if f := s.ed.rec; f != nil && active {
		lines, focus := s.recordLines(f, w)
		at = len(out) + 1 + focus
		out = append(out, "")
		out = append(out, lines...)

		return out, at, len(out) - 1
	}

	return out, at, end
}

// muteEntryRow renders one entry of the file: its match text, then a
// note with its line and its labels, then the draft that changes it.
func (s *settingsScreen) muteEntryRow(r *settingsListRows, e settings.MuteEntry, n int, current bool, draft *settings.MuteEdit) {
	t := s.st.theme

	r.row(n, t.Code.Render(ui.OneLine(settings.MuteText(e.Rule, e.Region))), current)

	labels := []string{"line " + strconv.Itoa(e.Line)}

	if e.NoOp {
		labels = append(labels, "matches nothing")
	}

	if e.DuplicateOf >= 0 {
		if first, ok := muteEntryAt(s.muteEntries(), e.DuplicateOf); ok {
			labels = append(labels, "duplicate of line "+strconv.Itoa(first.Line))
		}
	}

	// A flow list blocks the whole group, and its block note gives the
	// route. Only a flow item in a block list can go by x.
	if e.Flow && s.snap().Editable(settings.GroupMutes) == nil {
		labels = append(labels, "flow entry: remove only")
	}

	if len(e.Extra) > 0 {
		labels = append(labels, "other keys stay: "+ui.OneLine(strings.Join(e.Extra, ", ")))
	}

	r.note(ui.ToneNeutral, strings.Join(labels, " "+t.Glyphs.Sep+" "))

	if draft == nil || draft.Op == settings.MuteAdd || draft.Ref != e.Ref() {
		return
	}

	if draft.Op == settings.MuteRemove {
		r.note(ui.ToneAttention, "draft: remove this entry")

		return
	}

	r.note(ui.ToneAttention, "draft: change to "+muteDraftText(draft))
}

// recordLines renders the record form: the heading, the rule and region
// fields with the verdict of each, the duplicate, the overlaps, and the
// match description. It returns the line of the focused field.
func (s *settingsScreen) recordLines(f *settingsMuteForm, w int) (out []string, focus int) {
	t := s.st.theme
	v := s.muteVerdict(f)

	heading := "NEW ENTRY"
	if f.op == settings.MuteChange {
		heading = "CHANGE LINE " + strconv.Itoa(f.entry.Line)
	}

	out = []string{t.Heading.Render(heading)}

	field := func(i int, label string, in *ui.LineInput, errText string, notes []string) {
		mark := "  "
		value := t.Code.Render(ui.OneLine(in.Value()))

		switch {
		case f.focus == i:
			mark = t.Key.Render(t.Glyphs.Cursor) + " "
			value = in.View(t)
			focus = len(out)
		case in.Value() == "":
			value = t.Faint.Render("none")
		}

		out = append(out, ui.Truncate(mark+t.Muted.Render(ui.PadRight(label, settingsRecordLabelW-2))+value, w,
			t.Glyphs.Ellipsis))

		pad := strings.Repeat(" ", settingsRecordLabelW)

		if errText != "" {
			out = append(out, propIndent(pad, propNote(t, ui.ToneFailure, errText, w-settingsRecordLabelW))...)
		}

		for _, n := range notes {
			out = append(out, propIndent(pad, propLines(t.Muted, ui.OneLine(n), w-settingsRecordLabelW))...)
		}

		if n := in.Notice(); n != "" {
			out = append(out, propIndent(pad, propNote(t, ui.ToneAttention, n, w-settingsRecordLabelW))...)
		}
	}

	field(0, "Rule", &f.rule, v.ruleErr, nil)
	field(1, "Region", &f.region, v.regionErr, v.in.Notes)

	out = append(out, "")
	out = append(out, s.verdictLines(f, v, w)...)

	return out, focus
}

// verdictLines renders the verdict of the whole record. It names the
// missing side, the duplicate that the planner refuses, and the overlaps
// that the review notes. Then it shows the match description and why
// enter kept nothing.
func (s *settingsScreen) verdictLines(f *settingsMuteForm, v settingsMuteVerdict, w int) []string {
	t := s.st.theme

	var out []string

	switch {
	case v.formErr != "" && !v.filled:
		out = append(out, propLines(t.Muted, "Type a rule, a region, or both.", w)...)
	case v.formErr != "":
		out = append(out, propNote(t, ui.ToneFailure, v.formErr+".", w)...)
	case v.hasDuplicate:
		d := v.duplicate
		out = append(out, propNote(t, ui.ToneFailure, "Duplicate of line "+strconv.Itoa(d.Line)+" ("+
			ui.OneLine(settings.MuteText(d.Rule, d.Region))+"): a save would change nothing that it hides.", w)...)
	case v.ruleErr == "" && v.regionErr == "":
		out = append(out, propLines(t.Body, "Hides "+muteMatchText(v.in)+".", w)...)

		for _, n := range muteOverlapNotes(v.overlaps) {
			out = append(out, propNote(t, ui.ToneAttention, n, w)...)
		}
	}

	if f.err != "" {
		out = append(out, propNote(t, ui.ToneFailure, f.err, w)...)
	}

	return out
}

// muteChangeRows renders the CHANGE rows of a mute review: the one
// operation and the entry that it changes, with the line of the entry.
func (s *settingsScreen) muteChangeRows(p *settings.Plan, w int) []string {
	t := s.st.theme

	var out []string

	for _, e := range p.Edits {
		m := e.Mute
		if m == nil {
			continue
		}

		text := muteDraftText(m)
		old, line := planMuteEntry(p, m.Ref)
		oldText := ui.OneLine(settings.MuteText(old.Rule, old.Region))

		// The label column fits the label of this row and two cells.
		switch m.Op {
		case settings.MuteAdd:
			out = append(out, integLabeled(t, "add", ui.Width("add")+2, text, t.Body, w)...)
		case settings.MuteChange:
			out = append(out, arrowRow(t, line, oldText, text, ui.Width(line)+2, w)...)
		case settings.MuteRemove:
			out = append(out, integLabeled(t, "remove", ui.Width("remove")+2, line+": "+oldText, t.Body, w)...)
		}
	}

	return out
}

// planMuteEntry returns the entry of ref in the snapshot of the plan and
// its line in words, for example "line 4". The planner found the entry
// in this snapshot (LessonsPart.Target), so the lookup asks it too.
func planMuteEntry(p *settings.Plan, ref settings.MuteRef) (entry settings.MuteEntry, line string) {
	if p.Snapshot == nil {
		return settings.MuteEntry{}, "entry"
	}

	e, err := p.Snapshot.Lessons.Target(ref)
	if err != nil {
		return settings.MuteEntry{}, "entry"
	}

	return e, "line " + strconv.Itoa(e.Line)
}

// muteEffect is the WILL line of a mute operation.
func (s *settingsScreen) muteEffect(p *settings.Plan, e settings.Edit) effect {
	g := s.st.theme.Glyphs
	m := e.Mute

	if m == nil {
		return effect{glyph: g.Dash, tone: ui.ToneNeutral, text: "change no mute"}
	}

	text := muteDraftText(m)
	old, line := planMuteEntry(p, m.Ref)

	switch m.Op {
	case settings.MuteAdd:
		return effect{glyph: g.Add, tone: ui.ToneSuccess, text: "add the mute " + text + " after the last entry"}
	case settings.MuteChange:
		return effect{glyph: g.Add, tone: ui.ToneSuccess, text: "change the mute at " + line + " to " + text +
			"; its other keys and comments stay"}
	default:
		return effect{glyph: g.Del, tone: ui.ToneFailure, text: "remove the mute at " + line + ": " +
			ui.OneLine(settings.MuteText(old.Rule, old.Region))}
	}
}
