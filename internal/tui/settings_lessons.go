package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the two Settings groups of .seamark/lessons.yaml
// (contracts §4.3): lesson delivery and lesson mutes. They use the
// draft, the review, and the save of the config.yaml groups, keyed on
// settings.Field and settings.Group. The settings package owns every
// rule: the defaults, the effective values, the activation texts, the
// number check, the mute check, the duplicates, and the overlaps. This
// file only asks it.
//
// Lesson delivery has four fields. threshold, pin_budget, and
// change_budget take the radio default or a typed positive number.
// hook_delivery takes default, always, or once-per-context. Default
// removes the key, so the reader default applies.
//
// Lesson mutes has one field, the mute list. Its editor lists every
// entry of the file. The record form adds or changes one entry, and x
// removes one. A draft holds one mute operation, because one save
// changes one entry (spec R-11). A second operation asks first.

// Texts of the lessons groups. Each one is a fixed sentence that more
// than one place shows.
const (
	// settingsMuteNoReset answers d on the mute list. The planner has no
	// reset of the whole list, because one save changes one entry.
	settingsMuteNoReset = "The mute list has no reset: one save changes one entry. Press enter to open the " +
		"mute editor."
	// settingsForeignRootRoute is the route of a lessons group that the
	// index of another repository blocks (P4-16). A fix by hand would
	// edit the file of the other repository.
	settingsForeignRootRoute = "Build the index of this workspace in Overview, then review again; o in the " +
		"Settings list opens it."
	// settingsReplaceText states which file metadata a lessons save keeps
	// (contracts P4-17). The save replaces the file by rename.
	settingsReplaceText = "The save replaces the file: it keeps the permission bits, not an access control " +
		"list, extended attributes, or another owner."
	// settingsPinsNeverMuted ends every match description. A mute hides
	// mined lessons only, so the routes that change a pin are in Pins.
	settingsPinsNeverMuted = "pins are never muted: d Remove or - Prune in Lessons › Pins"
)

// settingsFileBlock returns the block of the whole document of g: the
// config.yaml block or the lessons.yaml block, or nil. A block of one
// document never blocks a group of the other.
func settingsFileBlock(snap *settings.Snapshot, g settings.Group) error {
	if g.Document() != pinfile.File {
		return snap.Blocked
	}

	if snap.Lessons == nil {
		return nil
	}

	return snap.Lessons.Blocked
}

// settingsFileExists reports whether the document of g exists.
func settingsFileExists(snap *settings.Snapshot, g settings.Group) bool {
	if g.Document() != pinfile.File {
		return snap.Guard.Exists
	}

	return snap.Lessons != nil && snap.Lessons.Exists
}

// foreignRoot reports a selected lessons group whose block has the route
// of the index build (P4-16). o then opens that build. The key reads the
// route of settingsBlock, so it follows the route that the note shows.
func (s *settingsScreen) foreignRoot() bool {
	snap := s.snap()
	if snap == nil {
		return false
	}

	err := snap.Editable(s.field().Group())

	return err != nil && settingsBlock(err, s.field().Document()).route == settingsForeignRootRoute
}

// lessonsChoices returns the radio options of a delivery field. The
// default option removes the key; st.Default names the value that then
// applies, from the reader constants.
func lessonsChoices(f settings.Field, st settings.FieldState) []settingsChoice {
	def := settingsChoice{kind: settingsChoiceLessonsDefault, label: "default", note: ui.OneLine(st.Default), def: true}

	if f != settings.HookDelivery {
		return []settingsChoice{def, {kind: settingsChoiceNumber, label: "number", note: "a whole number above 0"}}
	}

	def.note += ", removes the key"

	return []settingsChoice{
		def,
		{kind: settingsChoiceMode, label: string(reviews.HookDeliveryAlways), mode: string(reviews.HookDeliveryAlways),
			note: "every matching edit"},
		{kind: settingsChoiceMode, label: string(reviews.HookDeliveryOncePerContext),
			mode: string(reviews.HookDeliveryOncePerContext), note: "see REPETITION"},
	}
}

// settingsNumberChoice reports a choice whose row holds a typed number:
// the review window days or a lessons number.
func settingsNumberChoice(k settingsChoiceKind) bool {
	return k == settingsChoiceWindowDays || k == settingsChoiceNumber
}

// lessonsSelects reports whether the draft or the file selects the
// lessons choice c of f. A drafted reset selects default. Without a
// draft, the reader mapping decides. An absent or null key and a stored
// value that the reader replaces with its default select default, as a
// nonpositive review window selects unlimited.
func (s *settingsScreen) lessonsSelects(f settings.Field, c settingsChoice) bool {
	if e, ok := s.edits[f]; ok {
		switch c.kind {
		case settingsChoiceLessonsDefault:
			return e.Reset
		case settingsChoiceNumber:
			return !e.Reset
		default:
			return !e.Reset && e.String == c.mode
		}
	}

	cfg := s.snap().Lessons.Config

	switch c.kind {
	case settingsChoiceNumber:
		_, ok := s.lessonsNumber(f)

		return ok
	case settingsChoiceMode:
		return string(cfg.Delivery) == c.mode
	}

	// The default choice: no number, or no mode that the reader reads.
	if f == settings.HookDelivery {
		return cfg.Delivery == ""
	}

	_, ok := s.lessonsNumber(f)

	return !ok
}

// lessonsNumber returns the positive number that the draft or the file
// selects for a number field. A stored value that the reader replaces
// with its default selects no number (settings.LessonsPart.Number). The
// file value is the value that the reader uses, so a stored 0x10 reads
// as 16, as the hook reads it.
func (s *settingsScreen) lessonsNumber(f settings.Field) (int, bool) {
	if e, ok := s.edits[f]; ok {
		return e.Int, !e.Reset
	}

	snap := s.snap()
	if snap == nil {
		return 0, false
	}

	return snap.Lessons.Number(f)
}

// lessonsRedundant reports a delivery edit that leaves the stored text
// as it is. It compares with the stored text of the node, not with the
// effective value. The reader reads a stored 0 as its default, but a
// save of the default value still writes a real key.
func lessonsRedundant(st settings.FieldState, e settings.Edit) bool {
	if !st.Stored.Present {
		return false
	}

	if e.Field == settings.HookDelivery {
		return st.Stored.Raw == e.String
	}

	return st.Stored.Raw == strconv.Itoa(e.Int)
}

// keepLessonsNumber records a typed lessons number. It asks the rule of
// the planner (settings.CheckNumber), so the editor never keeps a value
// that the save refuses. A refusal stays inline, and the typed text
// stays for a fix. The input holds at most ten digits, so a text with a
// digit always converts, and the rule refuses an overflow as a number.
func (s *settingsScreen) keepLessonsNumber() tea.Cmd {
	f := s.ed.number

	n, err := strconv.Atoi(s.ed.input.Value())
	if err != nil {
		s.ed.inputErr = "Enter a whole number above 0."

		return nil
	}

	if err := settings.CheckNumber(f, n); err != nil {
		s.ed.inputErr = ui.ErrText(err) + "."

		return nil
	}

	s.ed.typing = false
	s.setEdit(settings.Edit{Field: f, Int: n})

	return nil
}

// lessonsExplanation returns the console text of a lessons field. The
// reader notes and the activation texts follow it, so it never repeats
// them. st names the default of hook_delivery, from the reader.
func lessonsExplanation(f settings.Field, st settings.FieldState) string {
	switch f {
	case settings.Threshold:
		return "Mined lessons only: a pin never depends on the threshold. The number choice takes a whole " +
			"number above 0; default removes the key."
	case settings.PinBudget:
		return "Pins come first in the hook. The number choice takes a whole number above 0; default removes " +
			"the key."
	case settings.ChangeBudget:
		return "One budget for all files of one answer, not one per file. The number choice takes a whole " +
			"number above 0; default removes the key."
	case settings.HookDelivery:
		return "Default removes the key, so " + ui.OneLine(st.Default) + " applies. The mode changes repetition " +
			"only. The hook selects the same lessons in each mode."
	case settings.Mutes:
		return "A mute hides mined lessons. A rule matches in any case. " + muteRegionRule
	}

	return ""
}

// muteRegionRule states the region rule of the mute matcher
// (reviews.RegionMatches). The detail and the editor help share it, so
// both name the trailing "/" that leaves the region itself out.
const muteRegionRule = "A region matches that region and every region under it. A region with a trailing " +
	`"/" matches only the regions under it.`

// muteOpenHelp names the key that opens the mute editor. The detail
// shows it only while the editor is closed, because the open editor
// names its own keys.
const muteOpenHelp = "enter opens the mute editor."

// lessonsSaved implements lessonsBound (contracts P4-12). A write of
// lessons.yaml makes a lessons review stale, so a review that reads or
// waits for y closes, and its draft stays. A config.yaml review binds
// another file, so it stays. The snapshot reads again in every case,
// because Settings observes lessons.yaml and the writer of Pins does
// not reload it. Settings itself sends the message after its own save,
// and that save already reads the snapshot again.
func (s *settingsScreen) lessonsSaved(msg lessonsSavedMsg) tea.Cmd {
	var cmds []tea.Cmd

	if g := s.flow.group; s.flow.beforeCommit() && g.Document() == pinfile.File {
		s.closeFlow()
		cmds = append(cmds, toast(ui.ToneAttention, lessonsChanged(msg)+", so the "+settingsGroupTitle(g)+
			" review closed. The draft stays; press s to review it again."))
	}

	if s.saved == nil || !s.saved.at.Equal(msg.at) {
		cmds = append(cmds, refresh(secSettings))
	}

	return tea.Batch(cmds...)
}

// sampleKey routes the keys of the selection sample (contracts §4.4).
// An open sample takes every key, and esc returns to the review. In the
// mute review and the delivery review, s opens the sample of
// plan.Bytes.Before and After, and of no other text. A config.yaml
// review has no sample, because a sample selects from lessons.yaml only.
func (s *settingsScreen) sampleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	f := &s.flow

	switch {
	case f.sample != nil:
		cmd, closed := f.sample.key(msg)
		if closed {
			f.sample = nil
		}

		return cmd, true
	case key.Matches(msg, sampleKeys.Open) && s.samples():
		var cmd tea.Cmd

		p := f.preview
		f.sample, cmd = newSample(s.st, p.Bytes.Before, p.Bytes.After, settingsModeOnly(p))

		return cmd, true
	}

	return nil, false
}

// samples reports a review that s can sample: a shown plan of
// lessons.yaml that changes the file. A no-op plan has one text, so it
// has nothing to compare.
func (s *settingsScreen) samples() bool {
	return s.flow.showsPreview() && s.flow.preview.File == pinfile.File && !s.flow.preview.NoOp
}

// contextHelp implements contextHelper: the help of the mute editor, of
// a review that y can save, and of an open selection sample. A text
// field, the record form, and a question have the keys of their own, so
// they return no page, and ? reaches them. A failed or empty review
// offers no y, so its page would name a key that does nothing.
func (s *settingsScreen) contextHelp() (helpPage, bool) {
	switch {
	case s.prompt != nil, s.ed.typing, s.ed.rec != nil:
		return helpPage{}, false
	case s.flow.sample != nil:
		return s.flow.sample.help()
	case s.flow.active() && (!s.flow.showsPreview() || s.flow.preview.NoOp):
		// A wait, a failed review, an empty review, or a result: the
		// modal names its own keys.
		return helpPage{}, false
	case s.flow.active():
		text := []string{
			"The review shows the exact change of one group: the stored values before and after, the " +
				"effect, the diff of the file, and when the change takes effect.",
			"y writes the file. esc returns to the draft and writes nothing. A write of the same file " +
				"by another view closes the review; the draft stays.",
		}

		// The text names s only where the keys offer it.
		if s.samples() {
			text = append(text, sampleOpenHelp)
		}

		return helpPage{title: "Save review", text: text, keys: s.reviewHelpKeys()}, true
	case s.muteEditing():
		return helpPage{
			title: "Lesson mutes",
			text: []string{
				"A mute hides mined lessons by rule, by region, or by both. A rule matches in any case. " +
					muteRegionRule + " A mute never hides a pin: d Remove and - Prune in Lessons › Pins change pins.",
				"One save changes one entry, so the draft holds one operation: an add, a change, or a " +
					"removal. A second operation asks you to save or discard the first.",
				"An entry that matches nothing, a duplicate, and a flow entry show as they are. A flow entry " +
					"and an entry without keys can only be removed.",
			},
			keys: s.muteEditorKeys(),
		}, true
	}

	return helpPage{}, false
}

// reviewHelpKeys returns the keys of the "Save review" help page. A
// review of lessons.yaml also offers s, the selection sample.
func (s *settingsScreen) reviewHelpKeys() []key.Binding {
	out := []key.Binding{ui.WithHelp(settingsKeys.Confirm, "y", "save")}
	if s.samples() {
		out = append(out, sampleKeys.Open)
	}

	return append(out, ui.WithHelp(ui.Nav.Back, "esc", "back"))
}

// The mute editor. It opens with enter on the mute field and captures
// the keys while it is open. Its rows are the entries of the file in
// file order, the drafted new entry, and the add row. The draft holds at
// most one mute operation.

// settingsMuteRowKind names one row of the mute editor.
type settingsMuteRowKind int

const (
	// settingsMuteRowEntry is an entry of the file.
	settingsMuteRowEntry settingsMuteRowKind = iota
	// settingsMuteRowDraft is the new entry of an add draft.
	settingsMuteRowDraft
	// settingsMuteRowAdd adds an entry. A blocked group has no add row.
	settingsMuteRowAdd
)

// settingsMuteRow is one row of the mute editor.
type settingsMuteRow struct {
	kind  settingsMuteRowKind
	entry settings.MuteEntry
}

// settingsMuteAsk is one requested mute operation: an add (entry unset),
// or a change or a removal of entry.
type settingsMuteAsk struct {
	op    settings.MuteOp
	entry settings.MuteEntry
}

// settingsMuteForm is the open record form of the mute editor: the typed
// rule and region of an add or a change. The form takes every key but
// ctrl+c, so a digit or q types into the field.
type settingsMuteForm struct {
	op settings.MuteOp
	// entry is the changed entry; zero for an add.
	entry settings.MuteEntry
	// rule and region are the typed values. focus is 0 for the rule and
	// 1 for the region.
	rule, region ui.LineInput
	focus        int
	// err explains why enter kept nothing.
	err string
}

// focused returns the input that has the keys.
func (f *settingsMuteForm) focused() *ui.LineInput {
	if f.focus == 1 {
		return &f.region
	}

	return &f.rule
}

// muteEditing reports the open mute editor.
func (s *settingsScreen) muteEditing() bool {
	return s.editing && s.field() == settings.Mutes
}

// muteDraft returns the mute operation of the draft, or nil.
func (s *settingsScreen) muteDraft() *settings.MuteEdit {
	if e, ok := s.edits[settings.Mutes]; ok {
		return e.Mute
	}

	return nil
}

// muteEntries returns the entries of the mute list in file order. The
// entries are file text: a view sanitizes them.
func (s *settingsScreen) muteEntries() []settings.MuteEntry {
	snap := s.snap()
	if snap == nil || snap.Lessons == nil {
		return nil
	}

	return snap.Lessons.Mutes
}

// muteEntryAt returns the entry at YAML item index i. A null item counts
// in the index, so the lookup never maps a position of reviews.Config.
func muteEntryAt(entries []settings.MuteEntry, i int) (settings.MuteEntry, bool) {
	for _, e := range entries {
		if e.Index == i {
			return e, true
		}
	}

	return settings.MuteEntry{}, false
}

// muteRows returns the rows of the mute editor in display order.
func (s *settingsScreen) muteRows() []settingsMuteRow {
	var out []settingsMuteRow

	for _, e := range s.muteEntries() {
		out = append(out, settingsMuteRow{kind: settingsMuteRowEntry, entry: e})
	}

	if d := s.muteDraft(); d != nil && d.Op == settings.MuteAdd {
		out = append(out, settingsMuteRow{kind: settingsMuteRowDraft})
	}

	if snap := s.snap(); snap != nil && snap.Editable(settings.GroupMutes) == nil {
		out = append(out, settingsMuteRow{kind: settingsMuteRowAdd})
	}

	return out
}

// muteEditorKey handles the keys of the mute editor: the movement keys,
// a add, enter change, x remove, s review, and r after a failed reload.
// editorKey handles esc, tab, and the page keys before.
func (s *settingsScreen) muteEditorKey(msg tea.KeyMsg) tea.Cmd {
	rows := s.muteRows()
	last := max(0, len(rows)-1)
	s.ed.item = max(0, min(s.ed.item, last))

	switch {
	case key.Matches(msg, gkeys.Refresh) && s.check.shows():
		// The editor stays open after a save and captures the keys. The
		// message of a failed reload says "r reads again", so r reaches
		// the read of B1 here (state.readAgain).
		var cmd tea.Cmd
		s.check, cmd = s.st.readAgain(s.check, settingsRefreshKinds(s.checkGroup)...)

		return cmd
	case key.Matches(msg, ui.Nav.Up):
		s.ed.item = max(0, s.ed.item-1)
	case key.Matches(msg, ui.Nav.Down):
		s.ed.item = min(last, s.ed.item+1)
	case key.Matches(msg, ui.Nav.Top):
		s.ed.item = 0
	case key.Matches(msg, ui.Nav.Bottom):
		s.ed.item = last
	case key.Matches(msg, settingsKeys.Save):
		return s.review(settings.GroupMutes)
	default:
		return s.muteEditKey(msg, rows)
	}

	s.follow = true

	return nil
}

// muteEditKey handles the keys that change the draft. A blocked group
// answers each of them with its refusal, because no save can write a
// draft of it.
func (s *settingsScreen) muteEditKey(msg tea.KeyMsg, rows []settingsMuteRow) tea.Cmd {
	if !key.Matches(msg, settingsKeys.Add, ui.Nav.Enter, settingsKeys.Remove) {
		return nil
	}

	if refusal := s.editable(settings.Mutes); refusal != nil {
		return refusal
	}

	var row settingsMuteRow
	if s.ed.item < len(rows) {
		row = rows[s.ed.item]
	}

	switch {
	case key.Matches(msg, settingsKeys.Add), row.kind == settingsMuteRowAdd && key.Matches(msg, ui.Nav.Enter):
		return s.startMuteOp(settingsMuteAsk{op: settings.MuteAdd})
	case row.kind == settingsMuteRowDraft && key.Matches(msg, ui.Nav.Enter):
		d := s.muteDraft()
		s.openRecord(settings.MuteAdd, settings.MuteEntry{}, d.Rule, d.Region)
	case row.kind == settingsMuteRowDraft:
		return s.askDiscard(settings.GroupMutes)
	case row.kind == settingsMuteRowEntry && key.Matches(msg, ui.Nav.Enter):
		return s.startMuteOp(settingsMuteAsk{op: settings.MuteChange, entry: row.entry})
	case row.kind == settingsMuteRowEntry:
		return s.startMuteOp(settingsMuteAsk{op: settings.MuteRemove, entry: row.entry})
	}

	return nil
}

// muteEditorKeys returns the footer keys of the mute editor. A blocked
// group keeps only the keys that move and close. The keys of a row
// follow the row, so the footer never names a key that the row refuses.
// r shows only while a reload after a save failed.
func (s *settingsScreen) muteEditorKeys() []key.Binding {
	move := ui.WithHelp(ui.Nav.Down, "↑↓", "move")
	done := ui.WithHelp(ui.Nav.Back, "esc", "done")

	var out []key.Binding

	if s.blocked() {
		out = []key.Binding{move, done}
	} else {
		out = append([]key.Binding{move, settingsKeys.Add}, s.muteRowKeys()...)
		out = append(out, settingsKeys.Save, done)
	}

	if s.check.shows() {
		out = append(out, ui.WithHelp(gkeys.Refresh, "r", "read again"))
	}

	return out
}

// muteRowKeys returns the keys of the cursor row. enter changes an
// entry only when the planner can change it (LessonsPart.Changeable).
// enter on the add row adds, and x on the drafted entry discards it.
func (s *settingsScreen) muteRowKeys() []key.Binding {
	rows := s.muteRows()
	if len(rows) == 0 {
		return nil
	}

	switch row := rows[max(0, min(s.ed.item, len(rows)-1))]; row.kind {
	case settingsMuteRowEntry:
		if s.snap().Lessons.Changeable(row.entry.Ref()) != nil {
			return []key.Binding{settingsKeys.Remove}
		}

		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "change"), settingsKeys.Remove}
	case settingsMuteRowDraft:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "change"), settingsKeys.Discard}
	default:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "add")}
	}
}

// sameMuteOp reports whether ask repeats the operation of the draft d:
// a change or a removal of the same entry. The reference holds the text
// of the entry, so an entry that replaced the drafted one at its index
// is another entry. A second add is a second operation; the drafted new
// entry has its own row.
func sameMuteOp(d *settings.MuteEdit, ask settingsMuteAsk) bool {
	return d.Op == ask.op && ask.op != settings.MuteAdd && d.Ref == ask.entry.Ref()
}

// startMuteOp runs ask, or asks about the draft first when the draft
// holds another mute operation. One save changes one entry (R-11).
func (s *settingsScreen) startMuteOp(ask settingsMuteAsk) tea.Cmd {
	if d := s.muteDraft(); d != nil && !sameMuteOp(d, ask) {
		s.prompt = &settingsPrompt{kind: settingsPromptMuteOp, group: settings.GroupMutes, mute: ask}

		return nil
	}

	return s.runMuteOp(ask)
}

// runMuteOp starts the operation ask. An add or a change opens the record
// form; a removal goes into the draft at once, because it takes no
// input. The review shows each of them before a write.
func (s *settingsScreen) runMuteOp(ask settingsMuteAsk) tea.Cmd {
	e := ask.entry

	switch ask.op {
	case settings.MuteAdd:
		s.openRecord(settings.MuteAdd, settings.MuteEntry{}, "", "")
	case settings.MuteChange:
		return s.openChange(e)
	case settings.MuteRemove:
		if d := s.muteDraft(); d != nil && sameMuteOp(d, ask) {
			return toast(ui.ToneNeutral, "The draft already removes the entry at line "+strconv.Itoa(e.Line)+
				". Press s to review the save.")
		}

		s.setEdit(settings.Edit{Field: settings.Mutes, Mute: &settings.MuteEdit{Op: settings.MuteRemove, Ref: e.Ref()}})
	}

	return nil
}

// openChange opens the record form of a change of e. The planner owns
// the rule of a change (LessonsPart.Changeable). So a flow item and a
// null item get the refusal of the planner, and no form opens. A value
// that the input cannot hold exactly stays in the file, because the form
// would change it unseen.
func (s *settingsScreen) openChange(e settings.MuteEntry) tea.Cmd {
	line := strconv.Itoa(e.Line)

	if err := s.snap().Lessons.Changeable(e.Ref()); err != nil {
		b := settingsBlock(err, pinfile.File)

		return toast(ui.ToneNeutral, "The entry at line "+line+" cannot change: "+b.reason+". x removes it, and a "+
			"adds a new one.")
	}

	rule, region := e.Rule, e.Region

	if d := s.muteDraft(); d != nil && sameMuteOp(d, settingsMuteAsk{op: settings.MuteChange, entry: e}) {
		rule, region = d.Rule, d.Region
	}

	probe := settingsMuteInput("")
	if !probe.Holds(rule) || !probe.Holds(region) {
		return toast(ui.ToneAttention, "The entry at line "+line+" holds characters the editor cannot show; edit "+
			pinfile.File+" by hand.")
	}

	s.openRecord(settings.MuteChange, e, rule, region)

	return nil
}

// settingsMuteInput returns one input of the record form. A rule and a
// region are one line each. The input refuses a value past the limit
// and never cuts it, so the saved mute is the typed mute.
func settingsMuteInput(placeholder string) ui.LineInput {
	return ui.LineInput{Placeholder: placeholder, RefuseMultiline: true, RefuseOverLimit: true}
}

// openRecord opens the record form with the values rule and region.
func (s *settingsScreen) openRecord(op settings.MuteOp, e settings.MuteEntry, rule, region string) {
	f := &settingsMuteForm{
		op: op, entry: e,
		rule:   settingsMuteInput("rule, for example F541"),
		region: settingsMuteInput("region, for example api"),
	}

	f.rule.Set(rule)
	f.region.Set(region)

	s.ed.rec = f
	s.follow = true
}

// recordKey handles the keys of the record form. tab, shift+tab, up, and
// down switch the field. enter keeps the record in the draft, and esc
// closes the form without a change of the draft. Every other key goes
// to the field, so a digit, q, or ? types.
func (s *settingsScreen) recordKey(msg tea.KeyMsg) tea.Cmd {
	f := s.ed.rec

	switch {
	case key.Matches(msg, ui.Nav.Back):
		s.ed.rec = nil
	case key.Matches(msg, ui.Nav.Enter):
		return s.keepRecord()
	case key.Matches(msg, ui.Nav.Next, ui.Nav.Prev), msg.Type == tea.KeyUp, msg.Type == tea.KeyDown:
		f.focus = 1 - f.focus
	default:
		f.focused().Key(msg)
		f.err = ""
	}

	s.follow = true

	return nil
}

// recordKeys returns the footer keys of the record form.
func (s *settingsScreen) recordKeys() []key.Binding {
	return []key.Binding{
		ui.WithHelp(ui.Nav.Next, "tab", "next field"), ui.WithHelp(ui.Nav.Enter, "enter", "keep"),
		ui.WithHelp(ui.Nav.Back, "esc", "cancel"),
	}
}

// settingsMuteVerdict is the live check of the record form. It holds
// the verdict of settings.CheckMute for each field and the duplicate
// that the planner refuses. It also holds the overlaps that the review
// notes.
type settingsMuteVerdict struct {
	in                   settings.MuteInput
	ruleErr, regionErr   string
	formErr              string
	duplicate            settings.MuteEntry
	hasDuplicate, filled bool
	overlaps             []settings.MuteEntry
}

// ok reports a record that enter can keep.
func (v settingsMuteVerdict) ok() bool {
	return v.ruleErr == "" && v.regionErr == "" && v.formErr == "" && !v.hasDuplicate
}

// muteVerdict checks the record form with the rules of the planner. The
// form shows the verdict while the user types (P4-10).
func (s *settingsScreen) muteVerdict(f *settingsMuteForm) settingsMuteVerdict {
	var v settingsMuteVerdict

	v.filled = f.rule.Value() != "" || f.region.Value() != ""

	in, err := settings.CheckMute(f.rule.Value(), f.region.Value())

	switch {
	case errors.Is(err, reviews.ErrAuthoredText):
		v.ruleErr = ui.ErrText(err)
	case errors.Is(err, reviews.ErrRegionInput):
		v.regionErr = ui.ErrText(err)
	case err != nil:
		v.formErr = ui.ErrText(err)
	}

	if err != nil {
		return v
	}

	v.in = in

	skip := -1
	if f.op == settings.MuteChange {
		skip = f.entry.Index
	}

	if lp := s.snap().Lessons; lp != nil {
		v.duplicate, v.hasDuplicate = lp.Duplicate(in.Rule, in.Region, skip)
		v.overlaps = lp.Overlaps(in.Rule, in.Region, skip)
	}

	return v
}

// keepRecord puts the record into the draft when the verdict allows it.
// A change that keeps the values of its entry leaves the draft without
// that change.
func (s *settingsScreen) keepRecord() tea.Cmd {
	f := s.ed.rec
	v := s.muteVerdict(f)

	if !v.ok() {
		f.err = "Nothing was kept: fix the fields above first."

		return nil
	}

	if f.op == settings.MuteChange && v.in.Rule == f.entry.Rule && v.in.Region == f.entry.Region {
		if d := s.muteDraft(); d != nil && sameMuteOp(d, settingsMuteAsk{op: settings.MuteChange, entry: f.entry}) {
			delete(s.edits, settings.Mutes)
		}

		s.ed.rec = nil

		return toast(ui.ToneNeutral, "The entry at line "+strconv.Itoa(f.entry.Line)+" keeps its values, so the "+
			"draft holds no change of it.")
	}

	m := &settings.MuteEdit{Op: f.op, Rule: f.rule.Value(), Region: f.region.Value()}
	if f.op == settings.MuteChange {
		m.Ref = f.entry.Ref()
	}

	s.ed.rec = nil
	s.setEdit(settings.Edit{Field: settings.Mutes, Mute: m})

	// The cursor follows a new entry to its draft row, after the entries
	// of the file.
	if f.op == settings.MuteAdd {
		s.ed.item = len(s.muteEntries())
	}

	return nil
}

// muteOpPromptKey answers the question about the mute draft before a
// second operation. s reviews the draft. d discards it and runs the
// second operation. esc keeps the draft and drops the second one.
func (s *settingsScreen) muteOpPromptKey(msg tea.KeyMsg) tea.Cmd {
	ask := s.prompt.mute

	switch {
	case key.Matches(msg, settingsKeys.Save):
		s.prompt = nil

		return s.review(settings.GroupMutes)
	case key.Matches(msg, settingsKeys.DropDrafts):
		s.prompt = nil
		s.dropDraft(settings.GroupMutes)

		return s.runMuteOp(ask)
	case key.Matches(msg, ui.Nav.Back):
		s.prompt = nil
	}

	return nil
}

// settingsMuteOpKeys returns the keys of the question about the mute
// draft.
func settingsMuteOpKeys() []key.Binding {
	return []key.Binding{
		ui.WithHelp(settingsKeys.Save, "s", "review the draft"), ui.WithHelp(settingsKeys.DropDrafts, "d", "discard it"),
		ui.WithHelp(ui.Nav.Back, "esc", "keep"),
	}
}

// muteOpPromptText returns the title and the body of the question about
// the mute draft.
func (s *settingsScreen) muteOpPromptText() (title, body string) {
	e := s.edits[settings.Mutes]
	next := "add an entry"

	switch ask := s.prompt.mute; ask.op {
	case settings.MuteChange:
		next = "change the entry at line " + strconv.Itoa(ask.entry.Line)
	case settings.MuteRemove:
		next = "remove the entry at line " + strconv.Itoa(ask.entry.Line)
	}

	return "Save or discard the mute draft?", "The draft already holds one mute operation: " + s.muteDraftDetail(e) +
		". One save changes one entry. Press s to review and save it, or d to discard it and " + next + "."
}

// muteDraftValue renders a mute draft in the short form of the list.
func (s *settingsScreen) muteDraftValue(e settings.Edit) string {
	m := e.Mute
	if m == nil {
		return ""
	}

	switch m.Op {
	case settings.MuteAdd:
		return "add an entry"
	case settings.MuteChange:
		return "change " + s.muteLine(m.Ref)
	case settings.MuteRemove:
		return "remove " + s.muteLine(m.Ref)
	}

	return ""
}

// muteDraftEntry returns the entry that a change or a removal binds. It
// asks the planner (LessonsPart.Target). So an entry that replaced the
// drafted one at its index is never the entry of the draft.
func (s *settingsScreen) muteDraftEntry(ref settings.MuteRef) (settings.MuteEntry, bool) {
	snap := s.snap()
	if snap == nil {
		return settings.MuteEntry{}, false
	}

	e, err := snap.Lessons.Target(ref)

	return e, err == nil
}

// muteDraftStale reports a change or a removal whose entry no longer
// holds the text of the draft. The planner refuses such a draft.
func (s *settingsScreen) muteDraftStale() bool {
	d := s.muteDraft()
	if d == nil || d.Op == settings.MuteAdd {
		return false
	}

	_, ok := s.muteDraftEntry(d.Ref)

	return !ok
}

// muteLine names the entry of ref by its dash line, for example "line
// 4". An entry that changed since the draft started has no line that
// the draft can name, so the text says that it changed.
func (s *settingsScreen) muteLine(ref settings.MuteRef) string {
	if e, ok := s.muteDraftEntry(ref); ok {
		return "line " + strconv.Itoa(e.Line)
	}

	return "a changed entry"
}

// muteDraftDetail renders a mute draft for the Draft row and the
// questions, for example "remove line 4: rule F541 · region api". The
// values are file or typed text, so they go through ui.OneLine.
func (s *settingsScreen) muteDraftDetail(e settings.Edit) string {
	m := e.Mute
	if m == nil {
		return ""
	}

	text := muteDraftText(m)

	switch m.Op {
	case settings.MuteAdd:
		return "add " + text
	case settings.MuteChange:
		return "change " + s.muteLine(m.Ref) + " to " + text
	case settings.MuteRemove:
		if old, ok := s.muteDraftEntry(m.Ref); ok {
			return "remove line " + strconv.Itoa(old.Line) + ": " + ui.OneLine(settings.MuteText(old.Rule, old.Region))
		}

		return "remove " + s.muteLine(m.Ref)
	}

	return ""
}

// muteDraftText renders the mute of an add or a change as the planner
// writes it: the region after settings.CheckMute. A draft that the check
// refuses shows as typed, and the review then shows the refusal.
func muteDraftText(m *settings.MuteEdit) string {
	rule, region := m.Rule, m.Region

	if in, err := settings.CheckMute(m.Rule, m.Region); err == nil {
		rule, region = in.Rule, in.Region
	}

	return ui.OneLine(settings.MuteText(rule, region))
}

// muteMatchText describes what a checked mute hides, in the words of the
// matcher: the rule in any case, the region and every region under it.
// It ends with the routes of a pin, because a mute never hides one.
func muteMatchText(in settings.MuteInput) string {
	rule, region := ui.OneLine(in.Rule), ui.OneLine(in.Region)

	// A lesson region has no trailing "/", and the matcher compares the
	// exact region first. So "api/" leaves out a lesson on api itself,
	// and the text says so before the save.
	if dir, ok := strings.CutSuffix(region, "/"); ok && dir != "" {
		region += ", not " + dir + " itself"
	}

	var what string

	switch {
	case in.Rule != "" && in.Region != "":
		what = "mined lessons with rule " + rule + " (any case) under " + region
	case in.Rule != "":
		what = "mined lessons with rule " + rule + " (any case) in every region"
	default:
		what = "every mined lesson under " + region
	}

	return what + "; " + settingsPinsNeverMuted
}

// muteOverlapNotes returns one note per entry that hides a lesson that
// the record also hides. The planner owns the words
// (settings.OverlapNote), so the form and the review say the same.
func muteOverlapNotes(overlaps []settings.MuteEntry) []string {
	out := make([]string, 0, len(overlaps))

	for _, e := range overlaps {
		out = append(out, ui.OneLine(settings.OverlapNote(e)))
	}

	return out
}
