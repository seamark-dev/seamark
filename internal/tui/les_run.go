package tui

import (
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
)

// runKeep is the number of earlier finished records that the Run
// sub-view keeps. The records live only in this session, and a small
// bound keeps the memory of a long session flat.
const runKeep = 4

// runRecord is one run of this session in the Run sub-view: a
// distillation from its scope step to its result, an evidence refresh,
// or a trigger extraction. The owning workflow implements it in its own
// file. The Run sub-view holds the record, shows it, and routes messages
// to it.
type runRecord interface {
	// active reports work that runs now. The Run sub-view never
	// replaces an active record, because its progress must stay
	// visible until the work ends.
	active() bool
	// title names the record in one short line, for example
	// "Distillation · internal/api".
	title() string
	// view renders the record in w by h cells under the sub-view line.
	// The first two rows are the context line and a blank row, as in
	// the other sub-views.
	view(w, h int) string
	// keys returns the footer keys of the record. The Run sub-view adds
	// the sub-view switch keys while the record does not capture.
	keys() []key.Binding
	// update handles a message. Keys arrive only while the Run sub-view
	// is visible. Every other message arrives always, so progress and
	// results land while the user looks at another view.
	update(msg tea.Msg) tea.Cmd
	// capturing reports a text input or an overlay of the record. The
	// shell then sends every key to the record.
	capturing() bool
	// modal returns the overlay of the record, for example the stop
	// confirmation. The shell dims the frame under it.
	modal(w, h int) (string, bool)
}

// runDraft is an optional runRecord method. It reports a record that
// started no work yet, for example a distillation scope step. A repeated
// "distill" intent keeps such a draft, so typed input survives. The
// Run sub-view drops a replaced draft, because the session list keeps
// finished records only.
type runDraft interface {
	draft() bool
}

// runGenerator is an optional runRecord method of a distillation
// record. It returns the proposal IDs that the run created, a label for
// the run, and its end time. A zero time means that the run did not
// finish yet, so it has no result to review.
type runGenerator interface {
	generated() (ids []int64, label string, at time.Time)
}

// runOpener is an optional runRecord method. The Run sub-view calls it
// when it accepts the record, and it runs the returned command. A record
// starts its first read there, because only the current record gets
// messages. A read that starts earlier can end first, and its result
// then reaches the previous record.
type runOpener interface {
	opened() tea.Cmd
}

// openRunMsg gives a record to the Run sub-view. show also opens
// Lessons › Run, from any destination. The sub-view refuses the record
// while another record is active.
type openRunMsg struct {
	rec  runRecord
	show bool
}

// openRun returns a command that gives rec to the Run sub-view.
func openRun(rec runRecord, show bool) tea.Cmd {
	return func() tea.Msg { return openRunMsg{rec: rec, show: show} }
}

// closeRunMsg asks the Run sub-view to drop a draft, for example a
// distillation scope that the user closes. The newest earlier record
// becomes current again, and Lessons returns to the sub-view that opened
// the draft (lessons.runDropped). The view keeps a record that did work,
// because its result belongs to the session list.
type closeRunMsg struct {
	rec runRecord
}

// closeRun returns a command that drops the draft rec.
func closeRun(rec runRecord) tea.Cmd {
	return func() tea.Msg { return closeRunMsg{rec: rec} }
}

// isDraft reports a record that started no work yet.
func isDraft(rec runRecord) bool {
	d, ok := rec.(runDraft)

	return ok && d.draft()
}

// runKeys step through the records of this session. No record binds
// the brackets. A record that takes text receives them as text, so they
// step only while the record on view does not capture.
var runKeys = struct {
	Earlier, Later key.Binding
}{
	Earlier: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "earlier run")),
	Later:   key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "later run")),
}

// runView is the Lessons › Run sub-view: the current record of this
// session, and a short list of the earlier finished records. The
// records own their content. The view routes the messages to the
// current record, and [ and ] show an earlier record read-only. Without
// a record it shows where a run starts.
type runView struct {
	st *state

	// cur is the current record; nil before the first one.
	cur runRecord
	// past holds the earlier finished records, newest first, at most
	// runKeep. The console keeps no run history after exit.
	past []runRecord
	// back is the shown record: 0 shows cur, and k > 0 shows past[k-1].
	// The current record keeps all messages while an earlier record
	// shows, so a run that works goes on.
	back int

	// region is the region filter of the Proposals sub-view when the
	// "distill" intent arrives. A new draft shows it as a labelled
	// prefill, so the filter never becomes a run scope unseen.
	region string
}

func newRunView(st *state) *runView { return &runView{st: st} }

// showCurrent shows the current record. The "run" intent, a new record
// and a closed draft call it, because they concern the current run. The
// "run" intent comes from the notice row and from the links to a run.
func (v *runView) showCurrent() { v.back = 0 }

// prepare opens the scope step of a new distillation for the "distill"
// intent. It keeps a current distillation draft, so a repeated intent
// does not clear the typed scope. While a record is active, the view
// keeps it, because its progress must stay visible. The scope step
// plans nothing until the user asks for it.
func (v *runView) prepare() tea.Cmd {
	v.showCurrent()

	if v.cur != nil && v.cur.active() {
		return toast(ToneAttention, "A run is in progress: "+oneLine(v.cur.title())+
			". Prepare the next distillation after it ends.")
	}

	if d, ok := v.cur.(*distRecord); ok && d.draft() {
		return nil
	}

	v.set(newDistillDraft(v.st, v.region))

	return nil
}

// present reports a record, so Lessons offers the Run sub-view and its
// key u, also for a draft before the first run.
func (v *runView) present() bool { return v.cur != nil }

// records returns every record of the session, newest first.
func (v *runView) records() []runRecord {
	if v.cur == nil {
		return v.past
	}

	return append([]runRecord{v.cur}, v.past...)
}

// shown returns the record on view: the current record, or the earlier
// record that [ selected.
func (v *runView) shown() runRecord {
	if v.browsing() {
		return v.past[v.back-1]
	}

	return v.cur
}

// browsing reports an earlier record on view.
func (v *runView) browsing() bool {
	return v.back > 0 && v.back <= len(v.past)
}

// generated returns the proposal IDs that a finished distillation of
// this session created, its label, and its end time. An earlier record
// on view comes first, because enter on its result reviews its own
// proposals. Else the newest finished distillation answers. A draft or
// a running distillation has no result, so the search skips it. Without
// a finished distillation the IDs are empty.
func (v *runView) generated() (ids []int64, label string, at time.Time) {
	recs := v.records()
	if v.browsing() {
		recs = append([]runRecord{v.shown()}, recs...)
	}

	for _, rec := range recs {
		g, ok := rec.(runGenerator)
		if !ok {
			continue
		}

		if runIDs, runLabel, end := g.generated(); !end.IsZero() {
			return runIDs, runLabel, end
		}
	}

	return nil, "", time.Time{}
}

// set makes rec the current record and shows it. A finished current
// record moves to the front of the session list, and a draft goes away.
// rec leaves the list when it was in it, so no record shows twice.
func (v *runView) set(rec runRecord) {
	v.showCurrent()

	if rec == v.cur {
		return
	}

	v.past = slices.DeleteFunc(v.past, func(r runRecord) bool { return r == rec })

	if v.cur != nil && !isDraft(v.cur) {
		v.past = slices.Insert(v.past, 0, v.cur)
		v.past = v.past[:min(len(v.past), runKeep)]
	}

	v.cur = rec
}

// accept takes the record of an openRunMsg. It refuses the record while
// another record is active. A refused record never runs here; the slot
// of the shell refuses its work too. A new record that implements
// runOpener starts its first read now.
func (v *runView) accept(msg openRunMsg) tea.Cmd {
	if msg.rec == nil {
		return nil
	}

	if v.cur != nil && v.cur != msg.rec && v.cur.active() {
		return toast(ToneAttention, "A run is in progress: "+oneLine(v.cur.title())+
			". Its record stays in Lessons › Run.")
	}

	fresh := msg.rec != v.cur
	v.set(msg.rec)

	var cmds []tea.Cmd

	if o, ok := msg.rec.(runOpener); ok && fresh {
		cmds = append(cmds, o.opened())
	}

	if msg.show {
		cmds = append(cmds, navigate(destLessons, "run"))
	}

	return tea.Batch(cmds...)
}

// drop removes the draft rec when it is the current record. The newest
// finished record takes its place, so the result of the last run shows
// again. A record that did work stays, because its result belongs to the
// session list.
func (v *runView) drop(rec runRecord) {
	if rec == nil || rec != v.cur || !isDraft(rec) {
		return
	}

	v.showCurrent()
	v.cur = nil

	if len(v.past) > 0 {
		v.cur = v.past[0]
		v.past = v.past[1:]
	}
}

// update routes a message. openRunMsg and closeRunMsg change the
// record, and openExtractMsg opens a trigger extraction record like an
// openRunMsg. Every other message goes to the current record: keys
// arrive only while the view is visible, because Lessons sends keys to
// the visible sub-view only. Earlier records hold finished work, so they
// get no messages; [ and ] show them, and they get only the keys that
// browseKey passes.
func (v *runView) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case openRunMsg:
		return v.accept(m)
	case openExtractMsg:
		// The record is a draft that reads nothing until the view
		// accepts it, so a refused request costs no read. The flag
		// goes on before the first render, so the review names the
		// cursor row from its first frame.
		rec := newExtractRecord(v.st, m.req)
		rec.cursor = m.cursor

		return v.accept(openRunMsg{rec: rec, show: true})
	case closeRunMsg:
		v.drop(m.rec)

		return nil
	case tea.KeyMsg:
		if v.stepKey(m) {
			return nil
		}

		if v.browsing() {
			return v.browseKey(m)
		}
	}

	if v.cur == nil {
		return nil
	}

	return v.cur.update(msg)
}

// stepKey applies [ and ] and reports whether the key was one of them.
// A capturing record receives the brackets as text.
func (v *runView) stepKey(msg tea.KeyMsg) bool {
	if v.capturing() {
		return false
	}

	switch {
	case key.Matches(msg, runKeys.Earlier):
		v.back = min(v.back+1, len(v.past))
	case key.Matches(msg, runKeys.Later):
		v.back = max(v.back-1, 0)
	default:
		return false
	}

	return true
}

// runMoveKeys scroll a record or change its pane. An earlier record
// accepts only these keys and enter, because its other keys plan or
// start work, and the work of an earlier record would get no messages.
var runMoveKeys = []key.Binding{
	nkeys.Up, nkeys.Down, nkeys.PageUp, nkeys.PageDown, nkeys.Top, nkeys.Bottom, nkeys.Next, nkeys.Prev,
}

// browseKey handles a key while an earlier record shows. The movement
// keys reach the record. enter opens the proposals of an earlier
// distillation, which are persisted, so the link stays valid.
func (v *runView) browseKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, runMoveKeys...):
		return v.shown().update(msg)
	case key.Matches(msg, nkeys.Enter):
		if len(v.shownIDs()) > 0 {
			return navigate(destLessons, "proposals:run")
		}
	}

	return nil
}

// shownIDs returns the proposal IDs of the record on view, when it is a
// finished distillation; nil otherwise.
func (v *runView) shownIDs() []int64 {
	g, ok := v.shown().(runGenerator)
	if !ok {
		return nil
	}

	ids, _, at := g.generated()
	if at.IsZero() {
		return nil
	}

	return ids
}

// keys returns the keys of the record, the record steps, and then the
// switch keys. The switch keys give way while the record captures,
// because the record receives those letters then. Without a record, the
// switch keys lead to the views that start a run.
func (v *runView) keys() []key.Binding {
	if v.cur == nil {
		return []key.Binding{lesKeys.Proposals, lesKeys.Pins, lesKeys.Stats, lesKeys.Evidence}
	}

	if v.capturing() {
		return slices.Clone(v.cur.keys())
	}

	var ks []key.Binding

	if v.browsing() {
		ks = v.browseKeys()
	} else {
		ks = slices.Clone(v.cur.keys())
	}

	earlier := runKeys.Earlier
	earlier.SetEnabled(v.back < len(v.past))

	later := runKeys.Later
	later.SetEnabled(v.back > 0)

	return append(ks, later, earlier, lesKeys.Proposals, lesKeys.Pins, lesKeys.Evidence, lesKeys.Stats)
}

// browseKeys names the keys of an earlier record: enter for its
// proposals, and its own movement keys with their labels. A compact
// footer has room for the short enter label only, as in resultKeys.
func (v *runView) browseKeys() []key.Binding {
	var ks []key.Binding

	if len(v.shownIDs()) > 0 {
		review := withHelp(nkeys.Enter, "enter", "review proposals")
		if v.st.width < wideWidth {
			review = withHelp(nkeys.Enter, "enter", "review")
		}

		ks = append(ks, review)
	}

	for _, b := range v.shown().keys() {
		if runMovement(b) {
			ks = append(ks, b)
		}
	}

	return ks
}

// runMovement reports a binding whose keys all belong to runMoveKeys.
func runMovement(b key.Binding) bool {
	if len(b.Keys()) == 0 {
		return false
	}

	for _, k := range b.Keys() {
		if !slices.ContainsFunc(runMoveKeys, func(m key.Binding) bool { return slices.Contains(m.Keys(), k) }) {
			return false
		}
	}

	return true
}

// capturing delegates to the record on view. Without a record the view
// never captures, so navigation stays free. An earlier record is
// finished, so it has no input or overlay.
func (v *runView) capturing() bool {
	return v.cur != nil && !v.browsing() && v.cur.capturing()
}

// modal returns the overlay of the current record. Lessons asks the
// visible sub-view for it, and the shell dims the frame under it. An
// earlier record has no overlay.
func (v *runView) modal(w, h int) (string, bool) {
	if v.cur == nil || v.browsing() {
		return "", false
	}

	return v.cur.modal(w, h)
}

// view renders the record on view, or the empty state without one. An
// earlier record says so in the blank row under its context line, so
// its result never reads as the current run. The empty state is one box
// under an empty context line, so the box starts on the same row as the
// boxes of the other sub-views.
func (v *runView) view(w, h int) string {
	t := v.st.theme

	if v.browsing() {
		rows := strings.SplitN(v.browseView(w, h), "\n", 3)
		if len(rows) == 3 {
			rows[1] = v.browseLine(w)
		}

		return strings.Join(rows, "\n")
	}

	if v.cur != nil {
		return v.cur.view(w, h)
	}

	o := boxOpts{W: w, H: max(4, h-3), Focus: true, Title: t.Title.Render("Run")}
	next := t.hints([]key.Binding{
		withHelp(lesKeys.Evidence, "e", "open Evidence"), withHelp(lesKeys.Pins, "m", "open Pins"),
	})

	// The extraction key comes from its binding, so the text names the
	// key that Pins and Proposals bind.
	box := integBoxed(t, o, ToneNeutral, "No run in this session",
		"Refresh the evidence or prepare a distillation from Evidence, or extract trigger information with "+
			extractKey.Help().Key+" in Pins or Proposals. The progress and the result of the last "+itoa(runKeep+1)+
			" runs show here until Seamark exits, and [ and ] step through them.", next)

	return strings.Join([]string{"", "", box}, "\n")
}

// runReadOnly is an optional runRecord method. It renders the record as
// an earlier run. browseKey passes an earlier record only the movement
// keys, so such a view leaves out the keys that would start or plan
// work, for example enter and n on an extraction result.
type runReadOnly interface {
	readOnlyView(w, h int) string
}

// browseView renders the earlier record on view. A record without a
// read-only form renders as usual.
func (v *runView) browseView(w, h int) string {
	if r, ok := v.shown().(runReadOnly); ok {
		return r.readOnlyView(w, h)
	}

	return v.shown().view(w, h)
}

// browseLine names the earlier record on view and the key back to the
// current record, for example "Earlier run 1 of 2 · read-only · ] later".
func (v *runView) browseLine(w int) string {
	t := v.st.theme
	sep := " " + t.Glyphs.Sep + " "

	line := " " + t.Attention.Render("Earlier run "+itoa(v.back)+" of "+itoa(len(v.past))) +
		t.Muted.Render(sep+"read-only"+sep) + t.Key.Render("]") + t.Muted.Render(" later")

	return truncate(line, w, t.Glyphs.Ellipsis)
}

// propRunFilter narrows the proposal list to the proposals that one run
// of this session created. It is a filter, not a selection, so it never
// makes a row the target of an action. Every other filter action ends
// it: f, /, esc, and the Overview link to all pending proposals.
type propRunFilter struct {
	ids   map[int64]bool
	label string
	// at is the end of the run. A ledger snapshot older than at cannot
	// hold the proposals of the run yet.
	at time.Time
}

// showRun shows the pending proposals of one run. It selects the
// pending state, clears the region filter, and puts the cursor on the
// first returned ID. No IDs means no run result, so the filters stay as
// they are. The list shows the returned IDs only, not every pending row,
// because a pending row can come from an earlier run.
func (p *propView) showRun(ids []int64, label string, at time.Time) tea.Cmd {
	if len(ids) == 0 {
		return nil
	}

	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}

	p.run = &propRunFilter{ids: set, label: label, at: at}
	p.filter = propPending
	p.region = ""
	p.focusDetail = false
	p.cursorID = slices.Min(ids)

	return p.filterChanged()
}

// inRun reports whether the run filter shows the proposal id. Without a
// run filter every proposal shows.
func (p *propView) inRun(id int64) bool {
	return p.run == nil || p.run.ids[id]
}

// clearRun ends the run filter and keeps the cursor on its proposal. The
// list only grows, so no selected row becomes hidden and the selection
// stays.
func (p *propView) clearRun() tea.Cmd {
	if p.run == nil {
		return nil
	}

	p.run = nil
	p.relocate(p.rows(), true)

	return toast(ToneNeutral, "Run filter cleared: every pending proposal shows.")
}

// runBehind reports a ledger snapshot older than the end of the filtered
// run. Such a snapshot cannot hold the proposals of the run yet, so a
// missing ID is not yet a missing proposal.
func (p *propView) runBehind() bool {
	return p.run != nil && p.st.ledger.at.Before(p.run.at)
}

// runChip renders the visible run filter of the filter bar, for example
// "run 14:02 · 6 proposals".
func (p *propView) runChip() string {
	t := p.st.theme

	return t.Accent.Render(oneLine(p.run.label)) +
		t.Muted.Render(" "+t.Glyphs.Sep+" "+propPlural(len(p.run.ids), "proposal", "proposals"))
}

// runMessage explains a run filter without a visible row. A ledger older
// than the run cannot show the proposals yet. Else the message counts
// the states of the run proposals and names the key that clears it.
func (p *propView) runMessage(l *proposals.Ledger) propMessage {
	t := p.st.theme
	g := t.Glyphs
	label := oneLine(p.run.label)

	if p.runBehind() {
		if sec := &p.st.ledger; !sec.loading && sec.err != nil {
			return propMessage{
				head: []string{t.Attention.Render(g.Warn) + " " + t.Title.Render("The ledger is older than "+label)},
				body: "The last ledger read failed: " + errText(sec.err) + ". Press r to read it again. " +
					"Press esc to show every pending proposal.",
			}
		}

		return propMessage{
			head: []string{t.Accent.Render(t.spinnerFrame(p.st.tick)) + " " +
				t.Title.Render("Reading the proposals of "+label+g.Ellipsis)},
			body: "The ledger snapshot is older than the run, so it cannot show them yet. Press r if this stays.",
		}
	}

	var applied, dismissed, superseded, missing int

	for id := range p.run.ids {
		has := func(pr domain.Proposal) bool { return pr.ID == id }

		switch {
		case slices.ContainsFunc(l.Applied, has):
			applied++
		case slices.ContainsFunc(l.Dismissed, has):
			dismissed++
		case slices.ContainsFunc(l.Superseded, has):
			// A prune supersedes an applied proposal. The row stays in the
			// ledger as history, so it is not missing.
			superseded++
		case !slices.ContainsFunc(l.Pending, has):
			missing++
		}
	}

	facts := []string{itoa(applied) + " applied", itoa(dismissed) + " dismissed"}
	if superseded > 0 {
		facts = append(facts, itoa(superseded)+" superseded")
	}

	if missing > 0 {
		facts = append(facts, itoa(missing)+" no longer in the ledger")
	}

	return propMessage{
		head: []string{t.Muted.Render(g.Info) + " " + t.Title.Render("No pending proposals from "+label)},
		body: "Of its " + propPlural(len(p.run.ids), "proposal", "proposals") + ", " + strings.Join(facts, ", ") +
			". Press esc to show every pending proposal.",
	}
}

// settingsSaved drops an open apply preview after a save of the pin
// writing setting. The preview named the old write gate, so a y could
// confirm an effect that the user never saw. A commit that runs stays.
func (p *propView) settingsSaved(msg settingsSavedMsg) tea.Cmd {
	d := p.dec
	if msg.group != settings.GroupPinWrite || d == nil || d.action != proposals.ActionApply {
		return nil
	}

	if d.stage != propStagePreparing && d.stage != propStagePreview {
		return nil
	}

	p.seq++
	p.dec = nil

	return toast(ToneAttention, "Pin writing changed in Settings, so the apply preview closed. Press a to "+
		"preview it again.")
}
