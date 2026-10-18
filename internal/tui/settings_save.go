package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the review and the save of one settings group, and
// the questions about a draft. The key s plans the patch in the
// background and opens the diff preview. Only y in that preview writes,
// under the mutation slot. The key esc before y changes nothing.
//
// The draft rules:
//
//   - Each group keeps its own draft, and one save writes one group. The
//     drafts of other groups stay, because each group has its own diff.
//   - A draft never saves by itself and never affects a run.
//   - Leaving Settings with any draft asks: keep the drafts, discard them
//     all, or stay. A quit with a draft asks before it discards, because
//     a draft is unsaved work.
//   - While another mutation runs, drafting works but s waits, and the y
//     of an open diff waits too, because the console runs one mutation at
//     a time.
//   - A save tells every screen about the written file:
//     settingsSavedMsg for config.yaml, lessonsSavedMsg for lessons.yaml.
//     Each screen then drops the previews that bind the old file. A
//     changed setting needs its own later run.
//   - A save routes on its outcome (contracts P4-18). Written drops the
//     draft, tells every screen, and reloads; a failed reload shows a
//     message and never repeats the save. Unknown keeps the draft, tells
//     every screen, and offers only a new review. NotWritten keeps the
//     draft and tells nobody.

// settingsFlow is the state of one preview and save. The embedded flow
// holds the stage and the previewed plan. flowClosed shows no modal, and
// flowResult explains a save that wrote nothing. A successful save
// closes the flow and shows a toast instead. The request generation
// lives on the screen, because each review is a new flow value.
type settingsFlow struct {
	previewFlow[settings.Plan]

	group settings.Group
	// res and saveErr are the outcome of a save that wrote nothing.
	res     settings.Result
	saveErr error
	// sample is the open selection sample of a lessons review, or nil.
	// closeFlow zeroes the flow, so the sample closes with its review.
	sample *sample
}

// settingsSaveNote records the last successful save of the session.
type settingsSaveNote struct {
	group settings.Group
	at    time.Time
}

// settingsPlannedMsg carries a plan back to the event loop.
type settingsPlannedMsg struct {
	seq  int
	plan *settings.Plan
	err  error
}

// settingsSaveDoneMsg carries the outcome of a save back to the event
// loop. at is the end of the save.
type settingsSaveDoneMsg struct {
	group settings.Group
	plan  *settings.Plan
	res   settings.Result
	err   error
	at    time.Time
}

// settingsPromptKind names a question about a draft.
type settingsPromptKind int

const (
	// settingsPromptLeave asks before the user leaves with a draft.
	settingsPromptLeave settingsPromptKind = iota
	// settingsPromptDiscard asks before x drops the draft of a group.
	settingsPromptDiscard
	// settingsPromptMuteOp asks about the mute draft before a second
	// mute operation, because one save holds one operation (R-11).
	settingsPromptMuteOp
)

// settingsPrompt is an open question about a draft.
type settingsPrompt struct {
	kind settingsPromptKind
	// next and intent are the destination that the user asked for.
	next   destination
	intent string
	// group is the group whose draft x drops.
	group settings.Group
	// mute is the second mute operation that waits for the answer.
	mute settingsMuteAsk
}

// saveBlocked returns the reason that s cannot review group g, or "".
func (s *settingsScreen) saveBlocked(g settings.Group) string {
	snap := s.snap()

	switch {
	case snap == nil:
		return "The settings are not read yet."
	case s.draftCount(g) == 0:
		return settingsGroupTitle(g) + " has no unsaved change."
	case snap.Editable(g) != nil:
		return settingsBlockSentence(settingsGroupTitle(g)+" cannot be saved here", snap.Editable(g), g.Document())
	case s.st.busy():
		return "Saving waits for the running operation: " + ui.OneLine(s.st.mutation) + "."
	}

	return ""
}

// review plans the draft of g in the background and opens the preview.
// The plan reads the file and writes nothing, so it takes no slot.
func (s *settingsScreen) review(g settings.Group) tea.Cmd {
	if reason := s.saveBlocked(g); reason != "" {
		tone := ui.ToneAttention
		if s.draftCount(g) == 0 {
			tone = ui.ToneNeutral
		}

		return toast(tone, reason)
	}

	snap, edits := s.snap(), s.groupEdits(g)
	s.excludeErrs = nil
	s.flow = settingsFlow{group: g}
	s.flow.startRead()
	seq := s.seq.start()

	var svc services.Settings = s.st.svc

	return func() tea.Msg {
		p, err := svc.PlanSettings(snap, edits)

		return settingsPlannedMsg{seq: seq, plan: p, err: err}
	}
}

// planned opens the preview of a plan. A stale file reads again at
// once, so the next s plans against the file as it is now.
func (s *settingsScreen) planned(msg settingsPlannedMsg) tea.Cmd {
	if !s.flow.readDone(s.seq.current(msg.seq), msg.plan, msg.err, "the planner returned no plan") {
		return nil
	}

	switch {
	case errors.Is(msg.err, settings.ErrStale):
		return refresh(secSettings)
	case errors.Is(msg.err, settings.ErrInvalidEdit):
		s.markPatternErrors(msg.err)
	case msg.err == nil && msg.plan != nil && msg.plan.NoOp:
		// The file already holds the draft, so the draft is noise.
		s.dropDraft(s.flow.group)
	}

	return nil
}

// markPatternErrors puts a plan error under the exclude pattern that it
// names. The planner quotes the pattern with %q.
func (s *settingsScreen) markPatternErrors(err error) {
	text := err.Error()

	for _, p := range s.excludeList(s.snap()) {
		if strings.Contains(text, strconv.Quote(p)) {
			if s.excludeErrs == nil {
				s.excludeErrs = map[string]string{}
			}

			s.excludeErrs[p] = ui.ErrText(err)
		}
	}
}

// confirmable reports a preview that y may save now.
func (s *settingsScreen) confirmable() bool {
	return s.flow.showsPreview() && !s.flow.preview.NoOp && !s.st.busy()
}

// confirm saves the previewed plan under the mutation slot.
func (s *settingsScreen) confirm() tea.Cmd {
	if !s.confirmable() {
		return nil
	}

	p, g := s.flow.preview, s.flow.group

	var svc services.Settings = s.st.svc

	cmd, ok := s.flow.startCommit(s.st, "Saving settings", func() tea.Msg {
		res, err := svc.SaveSettings(p)

		return settingsSaveDoneMsg{group: g, plan: p, res: res, err: err, at: time.Now()}
	})
	if !ok {
		return toast(ui.ToneAttention, "Saving waits for the running operation: "+ui.OneLine(s.st.mutation)+".")
	}

	return cmd
}

// saveDone frees the slot and applies the outcome (P4-18). A save that
// wrote the file drops the draft of the group and reloads the sources
// that the file feeds. It watches that reload, so a failed read says
// "Saved; the refresh failed" without a new save. A written or unknown
// outcome also tells every screen, so a screen drops the previews that
// bind the old file.
func (s *settingsScreen) saveDone(msg settingsSaveDoneMsg) tea.Cmd {
	err := msg.err
	if err == nil {
		err = msg.res.Err
	}

	file := settingsPlanFile(msg)

	// A write error with the reviewed bytes in the file is a Written
	// outcome: the readback proved the bytes (settings.SaveWith).
	if err == nil || msg.res.Outcome == settings.Written {
		s.dropDraft(msg.group)
		s.flow = settingsFlow{}
		s.saved = &settingsSaveNote{group: msg.group, at: msg.at}

		var reload tea.Cmd
		s.check, reload = s.st.watchRefresh(file, nil, settingsRefreshKinds(msg.group)...)
		s.checkGroup = msg.group

		note := toast(ui.ToneSuccess, settingsSavedText(msg.plan))
		if err != nil {
			note = toast(ui.ToneAttention, settingsSavedText(msg.plan)+" The write also reported an error: "+
				ui.ErrText(err))
		}

		return tea.Batch(mutationDone, reload, s.savedMsg(msg, file), note)
	}

	// No key can leave a running save, so the result needs no stage
	// check (commitDone).
	s.flow.toResult()
	s.flow.res, s.flow.saveErr = msg.res, err

	// The user can leave during the save, so the notice row also says
	// that nothing was saved. The result waits in Settings.
	note := toast(ui.ToneAttention, "Settings were not saved: "+ui.ErrText(err))

	switch {
	case msg.res.Outcome == settings.Unknown:
		// The file may hold the new bytes, so every preview of it is
		// stale, and the snapshot reads again. The draft stays.
		note = toast(ui.ToneAttention, unknownOutcomeText(file))

		return tea.Batch(mutationDone, refresh(secSettings), s.savedMsg(msg, file), note)
	case errors.Is(err, settings.ErrStale):
		return tea.Batch(mutationDone, refresh(secSettings), note)
	}

	return tea.Batch(mutationDone, note)
}

// settingsRefreshKinds returns the sources that read the file of g, so
// a save of g reloads them. Every config.yaml save changes the index
// fingerprint, so the index and Doctor read again. The evidence view
// shows the review window, and the pin readings depend on the index
// state. The ledger snapshot carries the write gate. The index
// fingerprint does not cover lessons.yaml, so a lessons save reloads no
// index. Doctor, Stats, the evidence view, and Pins read lessons.yaml.
func settingsRefreshKinds(g settings.Group) []sectionKind {
	if g.Document() == pinfile.File {
		return []sectionKind{secSettings, secDoctor, secStats, secLearning, secPins}
	}

	kinds := []sectionKind{secSettings, secDoctor, secIndex, secLearning, secPins}
	if g == settings.GroupPinWrite {
		kinds = append(kinds, secLedger)
	}

	return kinds
}

// settingsPlanFile returns the file of a saved plan, relative to the
// root (settingsPlanDocument). A result without a plan names the
// document of its group.
func settingsPlanFile(msg settingsSaveDoneMsg) string {
	if msg.plan != nil {
		return settingsPlanDocument(msg.plan)
	}

	return msg.group.Document()
}

// settingsPlanDocument returns the file of a plan, relative to the root:
// settings.Plan.File, or the document of its group for a plan without
// one. Every text about a plan names this file.
func settingsPlanDocument(p *settings.Plan) string {
	if p.File != "" {
		return p.File
	}

	return p.Group.Document()
}

// savedMsg tells every screen about a write of file: settingsSavedMsg
// for config.yaml, lessonsSavedMsg for lessons.yaml (P4-12). A receiver
// of one never reads the other file, so each message goes only with its
// own file.
func (s *settingsScreen) savedMsg(msg settingsSaveDoneMsg, file string) tea.Cmd {
	switch file {
	case settingsFile:
		saved := settingsSavedMsg{group: msg.group, at: msg.at}

		return func() tea.Msg { return saved }
	case pinfile.File:
		return sendLessonsSaved(msg.at, "Settings "+s.st.theme.Glyphs.Arrow+" "+msg.group.String())
	}

	return nil
}

// settingsSavedText is the toast of a successful save: what changed,
// and when it takes effect. A lessons save names its fields only: each
// consumer reads the file at its next run, and the review said which
// consumers read each field.
func settingsSavedText(p *settings.Plan) string {
	if p == nil {
		return "Configuration updated."
	}

	var fields []string
	for _, e := range p.Edits {
		fields = append(fields, string(e.Field))
	}

	if settingsPlanDocument(p) == pinfile.File {
		return "Lesson settings updated: " + strings.Join(fields, ", ") + "."
	}

	text := "Configuration updated: " + strings.Join(fields, ", ") + "."

	// The only edit that a plan can keep under a custom command is the
	// removal of the agent.cli key. A time of effect would suggest a
	// new command, so the toast says that the command stays.
	if settingsKeepsArgv(p) {
		return text + " " + settingsArgvStaysText + "."
	}

	if len(p.Activation) > 0 {
		verb := "It takes"
		if len(fields) > 1 {
			verb = "They take"
		}

		text += " " + verb + " effect at the " + p.Activation[0].When + "."
	}

	return text
}

// closeFlow closes the preview or the result. Nothing is written.
func (s *settingsScreen) closeFlow() {
	s.flow = settingsFlow{}
	s.seq.cancel()
}

// flowKey handles the keys of the preview and the result.
func (s *settingsScreen) flowKey(msg tea.KeyMsg) tea.Cmd {
	switch s.flow.stage {
	case flowPreparing:
		if key.Matches(msg, ui.Nav.Back) {
			s.closeFlow()
		}
	case flowPreview:
		if cmd, ok := s.sampleKey(msg); ok {
			return cmd
		}

		switch {
		case key.Matches(msg, settingsKeys.Confirm):
			return s.confirm()
		case key.Matches(msg, ui.Nav.Back):
			s.closeFlow()
		case key.Matches(msg, ui.Nav.Enter) && s.flow.showsPreview() && s.flow.preview.NoOp:
			s.closeFlow()
		default:
			s.flow.scroll.Key(msg)
		}
	case flowCommitting:
		s.flow.scroll.Key(msg)
	case flowResult:
		if key.Matches(msg, ui.Nav.Enter, ui.Nav.Back) {
			s.closeFlow()

			return nil
		}

		s.flow.scroll.Key(msg)
	}

	return nil
}

// flowActions returns the keys of the preview and the result, or of
// the open sample. The footer shows the same keys as the modal. The
// modal row of a sample leaves out ↑↓ (sample.keys).
func (s *settingsScreen) flowActions() []key.Binding {
	f := &s.flow

	if f.sample != nil {
		return f.sample.keys(false)
	}

	switch f.stage {
	case flowPreparing:
		return []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	case flowCommitting:
		return nil
	case flowResult:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "close")}
	}

	if f.showsPreview() && f.preview.NoOp {
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "close")}
	}

	var out []key.Binding

	if s.confirmable() {
		label := "save"
		if settingsRemovesArgv(f.preview) {
			label = "save and remove agent.argv"
		}

		out = append(out, ui.WithHelp(settingsKeys.Confirm, "y", label))
	}

	if s.samples() {
		out = append(out, sampleKeys.Open)
	}

	return append(out, ui.WithHelp(ui.Nav.Back, "esc", "keep editing"))
}

// flowView renders the preview or the result as a modal.
func (s *settingsScreen) flowView(w, h int) string {
	return s.st.theme.Modal(s.flowContent(ui.ModalBodyWidth(ui.ModalMinW, w)), &s.flow.scroll, w, h)
}

// flowContent builds the modal of the current stage.
func (s *settingsScreen) flowContent(w int) ui.ModalOpts {
	t := s.st.theme

	file := s.flow.group.Document()

	switch s.flow.stage {
	case flowPreparing:
		return flowWaitModal(s.st, "Preparing the save preview", "Seamark reads "+file+", applies the "+
			"draft to a copy and checks the copy with the shared readers. Nothing is written.", true, w)
	case flowCommitting:
		return flowWaitModal(s.st, "Saving settings", "Seamark writes "+file+". The result appears here.",
			false, w)
	case flowResult:
		return s.resultModal(w)
	}

	switch {
	case s.flow.err != nil:
		return s.previewErrModal(s.flow.err, w)
	case s.flow.preview.NoOp:
		return ui.ModalOpts{
			Title: t.Attention.Render("Nothing to save"),
			Body: propLines(t.Body, "The file already holds these values, so a save would change nothing. "+
				"The draft of "+settingsGroupTitle(s.flow.group)+" is dropped.", w),
			Actions: s.flowActions(),
		}
	}

	return s.previewModal(s.flow.preview, w)
}

// previewErrModal explains a plan that failed. A plan only reads, so
// nothing changed, and the draft stays for a new attempt.
func (s *settingsScreen) previewErrModal(err error, w int) ui.ModalOpts {
	t := s.st.theme
	acts := s.flowActions()
	b := settingsBlock(err, s.flow.group.Document())

	switch {
	case b.route != "", errors.Is(err, settings.ErrBlocked):
		// The reason leaves out the route of BlockedError.Error, because
		// the modal names the route and the location once. A block with a
		// route of its own has no fault in the file to fix.
		var body []string

		if b.route != "" {
			body = propNote(t, ui.ToneAttention, "The lessons settings cannot be saved here: "+b.reason+".", w)
			body = append(body, "")
			body = append(body, propLines(t.Body, b.route, w)...)
		} else {
			body = propNote(t, ui.ToneAttention, "The file cannot be edited safely: "+b.reason+".", w)
			body = append(body, "")
			body = append(body, propLines(t.Body, "The console does not change a file that it cannot edit safely. "+
				"Open the file at this place and fix it by hand:", w)...)
			body = append(body, integCommandLines(t, b.where, w)...)
		}

		body = append(body, "")
		body = append(body, propLines(t.Muted, "The draft stays. Nothing was written.", w)...)

		return ui.ModalOpts{Title: t.Attention.Render("Settings cannot be saved here"), Body: body, Actions: acts}
	case errors.Is(err, settings.ErrStale):
		body := propNote(t, ui.ToneAttention, "The file changed after Seamark read it, so this preview would show "+
			"old values.", w)
		body = append(body, "")
		body = append(body, propLines(t.Body, s.staleNext("review them against the file as it is now"), w)...)
		body = append(body, "")
		body = append(body, propLines(t.Muted, "Nothing was written.", w)...)

		return ui.ModalOpts{Title: t.Attention.Render("The file changed"), Body: body, Actions: acts}
	case errors.Is(err, settings.ErrInvalidEdit):
		body := propNote(t, ui.ToneFailure, ui.ErrText(err), w)
		body = append(body, "")
		body = append(body, propLines(t.Body, "Change the draft and press s again. Nothing was written.", w)...)

		return ui.ModalOpts{Title: t.Failure.Render("The draft is not valid"), Body: body, Actions: acts}
	}

	return previewFailedModal(t, "The save preview", err, "", "Nothing was written.", acts, w)
}

// staleNext says what happens to the draft after a stale refusal. The
// reload drops each edit that the file now holds (see normalize), so
// the text reads the draft at each render. The words stay true before
// and after the reload arrives. then names the next step of s.
func (s *settingsScreen) staleNext(then string) string {
	if s.draftCount(s.flow.group) == 0 {
		return "Seamark read the file again. The file now holds every value of the draft, so the draft is " +
			"dropped and nothing is left to save."
	}

	return "Seamark reads the file again now. The draft keeps only the edits that still change the file; " +
		"press s to " + then + "."
}

// previewModal renders the diff preview of a plan. It shows the changed
// values, what the save does and does not do, the exact file diff, and
// when the change takes effect.
func (s *settingsScreen) previewModal(p *settings.Plan, w int) ui.ModalOpts {
	t := s.st.theme
	file := settingsPlanDocument(p)

	kept := "Other sections, comments and unknown keys stay as they are."
	if file == pinfile.File {
		kept = "The pins, the other keys, comments and unknown keys stay as they are."
	}

	lead := "Seamark writes " + propPlural(len(p.Edits), "change", "changes") + " to " + file + ". " + kept +
		" Nothing is written before you press y."
	right := t.Attention.Render("modified")

	if p.Preview.Created {
		lead = "Seamark creates " + file + " with the chosen keys only. Nothing is written before " +
			"you press y."
		right = t.Success.Render("new file")
	}

	body := propLines(t.Body, lead, w)

	// The absolute path names the file that the plan binds to. It can
	// differ from the console root when the index records another root.
	body = append(body, "")
	// The workspace directory name can hold control characters, so the
	// path goes through ui.OneLine like every other path on the screen.
	body = append(body, integLabeled(t, "File", s.changeLabelW(p), ui.OneLine(ui.DisplayPath(p.Path())), t.Code, w)...)

	if file == pinfile.File {
		// The save replaces the file by rename (contracts P4-17), so a
		// reader must know which metadata stays.
		body = append(body, propIndent(strings.Repeat(" ", s.changeLabelW(p)),
			propLines(t.Muted, settingsReplaceText, w-s.changeLabelW(p)))...)
	}

	if s.st.busy() {
		body = append(body, "")
		body = append(body, propNote(t, ui.ToneAttention, "Saving waits for the running operation: "+
			ui.OneLine(s.st.mutation)+". The preview stays open.", w)...)
	}

	body = append(body, "", t.Heading.Render("CHANGE"))

	if p.Group == settings.GroupMutes {
		body = append(body, s.muteChangeRows(p, w)...)
	} else {
		body = append(body, s.changeRows(p, w)...)
	}

	if rows := s.effectiveRows(p, w); len(rows) > 0 {
		body = append(body, "", t.Heading.Render("EFFECTIVE"))
		body = append(body, rows...)
	}

	switch {
	case settingsRemovesArgv(p):
		body = append(body, "")
		body = append(body, propNote(t, ui.ToneAttention, "The save removes the custom command agent.argv. "+
			"Without the removal, agent.cli has no effect, because agent.argv overrides it.", w)...)
	case settingsKeepsArgv(p):
		// The spec asks to explain a reset that an override makes
		// ineffective, so the note tells how to change the command.
		body = append(body, "")
		body = append(body, propNote(t, ui.ToneAttention, "agent.argv overrides agent.cli, so this save does not "+
			"change the command that runs. To run a preset, choose it on agent.cli; the preview then removes "+
			"agent.argv.", w)...)
	}

	body = append(body, effectLines(t, s.willLines(p), s.wontLines(p.Group), w)...)

	body = append(body, "", t.Heading.Render("DIFF")+"  "+t.Faint.Render(t.Glyphs.Add+strconv.Itoa(p.Preview.Added)+
		" "+t.Glyphs.Del+strconv.Itoa(p.Preview.Removed)))
	body = append(body, setupDiffLines(t, p.Preview.Diff, w)...)

	body = append(body, "", t.Heading.Render("TAKES EFFECT"))
	body = append(body, s.activationLines(p, w)...)

	return ui.ModalOpts{
		Title:   t.TabActive.Render("Save") + " " + t.Body.Render(file),
		Right:   right,
		Body:    body,
		Actions: s.flowActions(),
	}
}

// settingsRemovesArgv reports a plan that removes the custom command.
func settingsRemovesArgv(p *settings.Plan) bool {
	return p != nil && slices.ContainsFunc(p.Edits, func(e settings.Edit) bool {
		return e.Field == settings.AgentArgv && e.Reset
	})
}

// settingsKeepsArgv reports an inference plan that leaves the custom
// command in effect. The planner refuses a preset under agent.argv, so
// such a plan only removes the agent.cli key.
func settingsKeepsArgv(p *settings.Plan) bool {
	return p != nil && p.Group == settings.GroupInference && settingsArgvSet(p.Snapshot) && !settingsRemovesArgv(p)
}

// settingsFieldIn returns the state of f in a list of field states.
func settingsFieldIn(states []settings.FieldState, f settings.Field) (settings.FieldState, bool) {
	i := slices.IndexFunc(states, func(st settings.FieldState) bool { return st.Field == f })
	if i < 0 {
		return settings.FieldState{}, false
	}

	return states[i], true
}

// settingsStored renders the stored side of a change: the raw value, or
// "not set" with the default that applies.
func settingsStored(st settings.FieldState) string {
	if st.Stored.Present {
		if raw := ui.OneLine(st.Stored.Raw); raw != "" {
			return raw
		}

		return "(no value)"
	}

	if st.Field == settings.AgentArgv {
		return "not set"
	}

	return "not set (default " + ui.OneLine(st.Default) + ")"
}

// changeRows renders "agent.cli   not set (default claude) › codex" for
// every edited field, from the stored values before and after the plan.
func (s *settingsScreen) changeRows(p *settings.Plan, w int) []string {
	var out []string

	labelW := s.changeLabelW(p)

	for _, e := range p.Edits {
		before, _ := settingsFieldIn(p.Before, e.Field)
		after, _ := settingsFieldIn(p.After, e.Field)

		next := settingsStored(after)
		if before.Stored.Present && !after.Stored.Present {
			next = "removed"
		}

		out = append(out, arrowRow(s.st.theme, string(e.Field), settingsStored(before), next, labelW, w)...)
	}

	return out
}

// effectiveRows renders the effective values that the plan changes.
func (s *settingsScreen) effectiveRows(p *settings.Plan, w int) []string {
	var out []string

	labelW := s.changeLabelW(p)

	for _, before := range p.Before {
		after, ok := settingsFieldIn(p.After, before.Field)
		if !ok || after.Effective == before.Effective {
			continue
		}

		out = append(out, arrowRow(s.st.theme, string(before.Field), ui.OneLine(before.Effective), ui.OneLine(after.Effective),
			labelW, w)...)
	}

	return out
}

// changeLabelW returns the label column of the change rows: the widest
// field name of the group and two cells.
func (s *settingsScreen) changeLabelW(p *settings.Plan) int {
	w := 0
	for _, f := range p.Group.Fields() {
		w = max(w, ui.Width(string(f)))
	}

	return w + 2
}

// willLines lists what the save changes in the file.
func (s *settingsScreen) willLines(p *settings.Plan) []effect {
	t := s.st.theme
	g := t.Glyphs

	var out []effect

	if p.Preview.Created {
		out = append(out, effect{glyph: g.Add, tone: ui.ToneSuccess, text: "create " + settingsPlanDocument(p) +
			" with these keys only"})
	}

	for _, e := range p.Edits {
		before, _ := settingsFieldIn(p.Before, e.Field)

		switch {
		case e.Field == settings.Mutes:
			out = append(out, s.muteEffect(p, e))
		case e.Field == settings.AgentArgv:
			out = append(out, effect{glyph: g.Del, tone: ui.ToneFailure, text: "remove agent.argv, the custom command"})
		case e.Field == settings.AgentCLI && e.Reset && settingsKeepsArgv(p):
			out = append(out, effect{glyph: g.Del, tone: ui.ToneFailure, text: "remove agent.cli; agent.argv stays in effect"})
		case e.Reset:
			out = append(out, effect{glyph: g.Del, tone: ui.ToneFailure, text: "remove " + string(e.Field) +
				"; the default " + ui.OneLine(before.Default) + " applies"})
		default:
			out = append(out, effect{glyph: g.Add, tone: ui.ToneSuccess, text: "set " + string(e.Field) + ": " +
				settingsYAMLValue(e)})
		}
	}

	return out
}

// settingsYAMLValue renders the value that an edit writes, in YAML words.
func settingsYAMLValue(e settings.Edit) string {
	switch e.Field {
	case settings.AgentCLI:
		return ui.OneLine(e.String)
	case settings.DistillWrite, settings.IndexGenerated:
		return strconv.FormatBool(e.Bool)
	case settings.ReviewWindow:
		if e.Unlimited {
			return "0 (unlimited)"
		}

		return strconv.Itoa(e.Days)
	case settings.IndexExclude:
		return settingsPatterns(len(e.List)) + ", in this order"
	case settings.Threshold, settings.PinBudget, settings.ChangeBudget:
		return strconv.Itoa(e.Int)
	case settings.HookDelivery:
		return ui.OneLine(e.String)
	}

	return ""
}

// wontLines lists what the save leaves alone. A save never starts the
// work that uses the new value (spec R-13). The delivery notes of the
// plan say what a lessons save never does to hooks, trust and delivery
// state. So these lines do not repeat them.
func (s *settingsScreen) wontLines(g settings.Group) []string {
	out := []string{"change other sections, comments, unknown keys or the file mode"}

	switch g {
	case settings.GroupInference:
		out = append(out, "run a model, log in, or change the Claude Code or Codex files")
	case settings.GroupPinWrite:
		out = append(out, "write a pin: a new preview offers the write ("+settings.PinWriteScope+")")
	case settings.GroupEvidence:
		out = append(out, "fetch reviews or change the stored findings")
	case settings.GroupIndexing:
		out = append(out, "scan, remove or index a file")
	case settings.GroupDelivery, settings.GroupMutes:
		out = []string{"change the pins, other keys, comments, unknown keys or the file mode"}

		if g == settings.GroupMutes {
			out = append(out, "mute a pin, or delete a finding, a proposal or the delivery history")
		}
	}

	return append(out, "start an index build, an evidence refresh or a distillation")
}

// activationLines renders when the saved fields take effect, then the
// notes of the plan once each. The notes include the index staleness
// that every save causes (decision D-13).
func (s *settingsScreen) activationLines(p *settings.Plan, w int) []string {
	t := s.st.theme

	var (
		out, whens, notes []string
		fields            = map[string][]string{}
	)

	// One line per moment, because the fields of one group usually
	// take effect together.
	for _, a := range p.Activation {
		if _, ok := fields[a.When]; !ok {
			whens = append(whens, a.When)
		}

		fields[a.When] = append(fields[a.When], string(a.Field))

		for _, n := range a.Notes {
			if !slices.Contains(notes, n) {
				notes = append(notes, n)
			}
		}
	}

	// A lessons field names its consumers, not one moment, so its line
	// says what the field affects.
	for _, when := range whens {
		line := strings.Join(fields[when], ", ") + ": at the " + when + "."
		if settingsPlanDocument(p) == pinfile.File {
			line = strings.Join(fields[when], ", ") + " affects " + when + "."
		}

		out = append(out, propLines(t.Body, line, w)...)
	}

	for _, n := range p.Notes {
		if !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}

	for _, n := range notes {
		out = append(out, integHanging(t.Faint.Render(t.Glyphs.Sep+" "), t.Muted, ui.OneLine(n), w)...)
	}

	return out
}

// resultModal explains a save that did not succeed. Every refusal of
// the settings package leaves the file as it was. A write with an
// unknown outcome may have changed it, so its result says so.
func (s *settingsScreen) resultModal(w int) ui.ModalOpts {
	t := s.st.theme
	err := s.flow.saveErr

	title, tone := "Nothing was saved", ui.ToneAttention

	var lead, next string

	switch {
	case errors.Is(err, settings.ErrStale):
		lead = "The file changed after the preview, so Seamark did not write it."
		next = s.staleNext("build a new preview from the file as it is now")
	case errors.Is(err, services.ErrBusy):
		lead = "Another operation is running, so the save did not start."
		next = "The draft stays. Press s again when the operation ends."
	case errors.Is(err, services.ErrForeignRoot):
		lead = "The index records another repository root, so Seamark did not write " +
			s.flow.group.Document() + "."
		next = "The draft stays. " + settingsBlock(err, s.flow.group.Document()).route
	case s.flow.res.Outcome == settings.Unknown:
		// The save never runs again by itself; only a new review of the
		// file as it is now can follow (P4-18).
		title, tone = "Result unknown", ui.ToneFailure
		lead = unknownOutcomeText(s.flow.group.Document())
		next = "The write failed, and Seamark could not read the file again. The draft stays. Press s to " +
			"review it against the file as it is now."
	default:
		title, tone = "Save failed", ui.ToneFailure
		lead = "Seamark did not replace the file, so it is unchanged."
		next = "Fix the cause below, then press s to review the draft again."
	}

	body := propNote(t, tone, lead, w)
	body = append(body, "")
	body = append(body, propLines(t.Body, next, w)...)
	body = append(body, "")
	body = append(body, propLines(t.Muted, "Details: "+ui.ErrText(err), w)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return ui.ModalOpts{Title: t.ToneStyle(tone).Render(title), Body: body, Actions: s.flowActions()}
}

// askDiscard opens the question before x drops the draft of g.
func (s *settingsScreen) askDiscard(g settings.Group) tea.Cmd {
	if s.draftCount(g) == 0 {
		return toast(ui.ToneNeutral, settingsGroupTitle(g)+" has no unsaved change.")
	}

	s.prompt = &settingsPrompt{kind: settingsPromptDiscard, group: g}

	return nil
}

// promptKey answers the open question. Keep and discard continue to the
// destination that the user asked for; esc stays here.
func (s *settingsScreen) promptKey(msg tea.KeyMsg) tea.Cmd {
	p := s.prompt

	switch p.kind {
	case settingsPromptDiscard:
		switch {
		case key.Matches(msg, settingsKeys.DropDrafts, settingsKeys.Confirm):
			s.prompt = nil
			s.dropDraft(p.group)

			return toast(ui.ToneNeutral, "Discarded the "+settingsGroupTitle(p.group)+" draft. "+p.group.Document()+
				" is unchanged.")
		case key.Matches(msg, ui.Nav.Back):
			s.prompt = nil
		}

		return nil
	case settingsPromptMuteOp:
		return s.muteOpPromptKey(msg)
	}

	switch {
	case key.Matches(msg, settingsKeys.Keep):
		s.prompt = nil
		s.passLeave = true

		return navigate(p.next, p.intent)
	case key.Matches(msg, settingsKeys.DropDrafts):
		s.prompt = nil
		s.discardDrafts()
		s.passLeave = true

		return navigate(p.next, p.intent)
	case key.Matches(msg, ui.Nav.Back):
		s.prompt = nil
	}

	return nil
}

// promptKeys returns the keys of the open question.
func (s *settingsScreen) promptKeys() []key.Binding {
	switch s.prompt.kind {
	case settingsPromptDiscard:
		return []key.Binding{ui.WithHelp(settingsKeys.DropDrafts, "d", "discard draft"),
			ui.WithHelp(ui.Nav.Back, "esc", "keep")}
	case settingsPromptMuteOp:
		return settingsMuteOpKeys()
	}

	return []key.Binding{settingsKeys.Keep, settingsKeys.DropDrafts, ui.WithHelp(ui.Nav.Back, "esc", "stay here")}
}

// promptView renders the open question as a modal of at most 64 cells,
// the size of the quit question.
func (s *settingsScreen) promptView(w, h int) string {
	t := s.st.theme
	bw := min(w-4, 64)
	inner := ui.BoxInner(bw)

	var title, body string

	switch s.prompt.kind {
	case settingsPromptDiscard:
		g := s.prompt.group
		title = "Discard the " + settingsGroupTitle(g) + " draft?"
		body = "It holds " + propPlural(s.draftCount(g), "unsaved change", "unsaved changes") + ": " +
			s.draftFields(g) + ". The file does not change."
	case settingsPromptMuteOp:
		title, body = s.muteOpPromptText()
	default:
		title = "Leave with an unsaved draft?"
		body = s.leaveText() + " A draft never saves by itself and never affects a run."
	}

	lines := integLines(t.Body, body, inner)
	lines = append(lines, "", t.Hints(s.promptKeys()))

	return t.Box(ui.BoxOpts{
		W: bw, H: min(h, len(lines)+2), Focus: true, Fill: true, Lines: lines,
		Title: t.Title.Render(title),
	})
}

// leaveText names the drafts that the user leaves, for example
// "Inference has 1 unsaved change: agent.cli."
func (s *settingsScreen) leaveText() string {
	groups := s.draftGroups()

	if len(groups) == 1 {
		g := groups[0]

		return settingsGroupTitle(g) + " has " + propPlural(s.draftCount(g), "unsaved change", "unsaved changes") +
			": " + s.draftFields(g) + "."
	}

	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, settingsGroupTitle(g)+" ("+s.draftFields(g)+")")
	}

	return strconv.Itoa(len(groups)) + " groups have unsaved changes: " + strings.Join(parts, ", ") + "."
}
