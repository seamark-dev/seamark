package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// This file holds the trigger extraction record of Lessons › Run. It
// keeps the record, the messages and the keys. Its commands plan, run
// and stop. extract_view.go renders the Preview step, the run and
// the result. run_agent.go holds the steps, the Preview step state, the
// run state and the helpers that the record shares with the
// distillation record. So a paid run reads and stops the same way in
// both workflows.

// extKeys are the letter keys of an extraction record that a
// distillation does not bind; agentKeys holds the shared letters. The
// other keys come from nkeys, so every list moves the same way. The
// record binds no switch key of Lessons (p, m, s, e, u) and no bracket,
// because the Run sub-view uses them.
var extKeys = struct {
	Remaining key.Binding
}{
	// Remaining prepares a new preview of the open questions. It never
	// starts work: the new review needs its own y.
	Remaining: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "prepare remaining")),
}

// extRecord is one trigger extraction of this session, from the review
// to the result. It implements runRecord, runDraft and runOpener. It is
// no runGenerator, because an extraction creates no proposal. Planning
// only reads; the run starts only on y from a ready preview.
type extRecord struct {
	st *state

	// req is the selection that the user made. The record never adds a
	// proposal to it.
	req learning.ExtractRequest
	// cursor is true when the cursor row stands in for an empty
	// selection. The review names it, because contracts D-15 asks every
	// preview to say so. Only the sender of the open request knows it.
	cursor bool
	// browsed is true only while the record renders as an earlier run.
	// An earlier record gets no enter or n, so the result then shows no
	// key for them.
	browsed bool

	// step is never stepScope: the selection of the user is the scope.
	step runStep
	prev agentPreview[learning.ExtractPreview]
	run  agentProgress[distill.ExtractExecution, distill.ExtractBatchOutcome]

	// warned is the time of the last refusal message of the record. A
	// plan or a start that succeeds soon after clears that message.
	warned time.Time
}

// newExtractRecord builds the review of req. It reads nothing: the
// record plans in opened, when the Run sub-view accepts it, because
// only the current record gets messages.
func newExtractRecord(st *state, req learning.ExtractRequest) *extRecord {
	return &extRecord{
		st: st, req: req, step: stepPreparing,
		run: idleAgentProgress[distill.ExtractExecution, distill.ExtractBatchOutcome](),
	}
}

// active reports work that runs now.
func (r *extRecord) active() bool { return r.step == stepRunning }

// draft reports a record that started no work yet. The Run sub-view
// drops a replaced draft, so a review leaves no trace in the history.
func (r *extRecord) draft() bool { return r.step < stepRunning }

// title names the record by the size of its selection.
func (r *extRecord) title() string {
	return "Trigger extraction " + r.st.theme.Glyphs.Sep + " " +
		propPlural(r.selected(), "selected item", "selected items")
}

// selected counts the selected items of the request: the proposals and
// the Pins entries without exactly one linked proposal.
func (r *extRecord) selected() int {
	n := len(r.req.IDs)

	for _, s := range r.req.Skipped {
		if s.ID == 0 || !slices.Contains(r.req.IDs, s.ID) {
			n++
		}
	}

	return n
}

// capturing reports the stop question, which owns every key.
func (r *extRecord) capturing() bool { return r.run.stopAsk }

// opened starts the read-only preview once the Run sub-view accepts the
// record.
func (r *extRecord) opened() tea.Cmd {
	if r.step != stepPreparing || r.prev.seq.started() {
		return nil
	}

	return r.startPlan()
}

// The messages of a trigger extraction: a preview result, a progress
// event, and the end of a run. run_agent.go explains their fields. A
// preview result leaves cwd empty, because the preview names its own
// directory.
type (
	extPlannedMsg = agentPlannedMsg[learning.ExtractPreview]
	extEventMsg   = agentEventMsg[distill.ExtractEvent]
	extDoneMsg    = agentDoneMsg[distill.ExtractExecution]
)

// focusProposalMsg asks the Proposals sub-view to show proposal id in
// the list of its state. A result row of an extraction sends it with
// a navigation to Proposals. It selects nothing and opens no preview.
type focusProposalMsg struct {
	id int64
}

// focusProposal returns a command that shows proposal id in Proposals.
func focusProposal(id int64) tea.Cmd {
	return tea.Batch(navigate(destLessons, "proposals"), func() tea.Msg { return focusProposalMsg{id: id} })
}

// update routes a message by type. Every result message names its
// record, so a result never lands in another record.
func (r *extRecord) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return r.key(msg)
	case extPlannedMsg:
		if msg.rec == r {
			return r.planned(msg)
		}
	case extEventMsg:
		// The wait continues for every event, so the worker never
		// blocks on a full queue.
		if msg.rec == r {
			r.event(msg)
		}

		return agentWait(msg.ch)
	case extDoneMsg:
		if msg.rec == r {
			return r.done(msg)
		}
	case settingsSavedMsg:
		r.settingsSaved(msg)
	}

	return nil
}

// warn shows a refusal in the status row and records its time.
func (r *extRecord) warn(text string) tea.Cmd {
	r.warned = time.Now()

	return toast(ToneAttention, text)
}

// settle clears a recent refusal message of the record after a plan or
// a start succeeds. An older refusal is gone already, and a clear would
// remove another message.
func (r *extRecord) settle() tea.Cmd {
	if r.warned.IsZero() || time.Since(r.warned) > agentWarnTTL {
		return nil
	}

	r.warned = time.Time{}

	return toast(ToneNeutral, "")
}

// startPlan starts the read-only preview of the selection. The service
// reads the index, the pins and the configuration; it never runs the
// agent and never stamps a row.
func (r *extRecord) startPlan() tea.Cmd {
	r.prev = agentPreview[learning.ExtractPreview]{seq: r.prev.seq}
	r.step = stepPreparing

	seq := r.prev.seq.start()
	req := r.req

	var svc Learning = r.st.svc

	return func() tea.Msg {
		p, err := svc.PlanExtract(req)

		msg := extPlannedMsg{rec: r, seq: seq, preview: p, err: err}

		// The preset list is read here, in the background, because View
		// and Update do no I/O.
		if p != nil {
			msg.preset = isPreset(p.Invocation.Name)
		}

		return msg
	}
}

// planned shows a preview result.
func (r *extRecord) planned(msg extPlannedMsg) tea.Cmd {
	if !r.prev.seq.current(msg.seq) || r.step != stepPreparing {
		return nil
	}

	r.step = stepPreview
	r.prev.preview, r.prev.err, r.prev.preset = msg.preview, msg.err, msg.preset
	r.prev.follow = true

	if msg.err == nil && msg.preview == nil {
		r.prev.err = errors.New("the planner returned no preview")
	}

	if r.prev.err != nil {
		return nil
	}

	return r.settle()
}

// plannedItems returns the planned items in send order with their
// batch.
func (r *extRecord) plannedItems() []extItemRef {
	p := r.prev.preview
	if p == nil || p.Plan == nil {
		return nil
	}

	var out []extItemRef

	for bi, b := range p.Plan.Batches {
		for ii := range b.Proposals {
			out = append(out, extItemRef{batch: bi, item: ii})
		}
	}

	return out
}

// extItemRef locates one item: the batch index and the item index in
// that batch.
type extItemRef struct {
	batch, item int
}

// runnable reports a ready preview that y may start: proposals to send,
// no blocker, and no refusal of an earlier start.
func (r *extRecord) runnable() bool {
	p := r.prev.preview

	return r.step == stepPreview && r.prev.err == nil && r.prev.stale == nil && p != nil && p.Plan != nil &&
		!p.NoOp() && len(p.Blockers) == 0
}

// start claims the mutation slot and runs exactly the reviewed preview.
// startRun registers the work with the shell, so an exit stops it and
// waits for it.
func (r *extRecord) start() tea.Cmd {
	if !r.runnable() {
		return nil
	}

	if r.st.busy() {
		return r.warn("Another operation is running: " + r.st.mutation)
	}

	p := r.prev.preview
	n := len(p.Plan.IDs())

	ch := agentQueue(len(p.Plan.Batches))

	var svc Learning = r.st.svc

	note := runNote{
		verb: extractRunVerb, detail: r.batchesText(), total: n,
		home: navigateMsg{dest: destLessons, intent: "run"},
	}

	work, ok := r.st.startRun(runExtract, extractRunName, note, func(ctx context.Context) tea.Msg {
		extRunWork(ctx, svc, r, p, ch)

		return nil
	})
	if !ok {
		return r.warn("Another operation is running: " + r.st.mutation)
	}

	r.step = stepRunning
	r.run = newAgentProgress[distill.ExtractExecution](extOutcomes(p.Plan))

	return tea.Batch(work, agentWait(ch), r.settle())
}

// extOutcomes returns the outcomes of a plan before its run: every item
// unattempted, with its region set before and after.
func extOutcomes(pl *distill.ExtractPlan) []distill.ExtractBatchOutcome {
	out := make([]distill.ExtractBatchOutcome, len(pl.Batches))

	for i, b := range pl.Batches {
		out[i] = distill.ExtractBatchOutcome{Index: i, Items: make([]distill.ExtractItemOutcome, len(b.Proposals))}

		for j, row := range b.Proposals {
			set := row.RegionSet()
			out[i].Items[j] = distill.ExtractItemOutcome{Proposal: row, RegionsBefore: set, RegionsAfter: slices.Clone(set)}
		}
	}

	return out
}

// extRunWork runs the preview with svc and streams its events, then
// its end, through ch. Every event always arrives, because the queue
// holds them all. agentWork turns a panic into a failed end.
func extRunWork(ctx context.Context, svc Learning, rec *extRecord, p *learning.ExtractPreview, ch chan tea.Msg) {
	agentWork(rec, ch, func() (*distill.ExtractExecution, error) {
		return svc.RunExtract(ctx, p, func(ev distill.ExtractEvent) {
			ch <- extEventMsg{rec: rec, ch: ch, ev: ev, at: time.Now()}
		})
	})
}

// event records one progress event of the running record.
func (r *extRecord) event(msg extEventMsg) {
	if r.step != stepRunning {
		return
	}

	ev := msg.ev
	run := &r.run

	switch ev.Kind {
	case distill.ExtractBatchStarted:
		if !run.checked {
			run.checked = true
			run.log(msg.at, "plan checked: no change since the preview")
		}

		run.sending = ev.Index

		if !run.moved {
			run.cursor = r.firstItemOf(ev.Index)
		}

		detail := "batch " + itoa(ev.Index+1) + " of " + itoa(len(run.units))
		r.st.updateRun(func(n *runNote) {
			if !n.stopping {
				n.detail = detail
			}
		})
	case distill.ExtractBatchDone:
		if ev.Batch == nil || ev.Index < 0 || ev.Index >= len(run.units) {
			return
		}

		run.units[ev.Index] = *ev.Batch
		run.finished++
		run.sending = -1
		done := r.itemsDone()

		r.st.updateRun(func(n *runNote) { n.done = done })
		run.log(msg.at, "batch "+itoa(ev.Index+1)+" "+extBatchWords(*ev.Batch))
	case distill.ExtractStopped:
		run.stopped, run.stopWhy = true, ev.Stop
		run.sending = -1
		run.log(msg.at, "run ended early: "+agentStopWords(ev.Stop))
	}
}

// itemsDone counts the items of the batches with a done event.
func (r *extRecord) itemsDone() int {
	n := 0

	for _, b := range r.run.units {
		if b.Status != distill.BatchUnattempted {
			n += len(b.Items)
		}
	}

	return n
}

// firstItemOf returns the send-order position of the first item of
// batch i.
func (r *extRecord) firstItemOf(i int) int {
	n := 0

	for k := 0; k < i && k < len(r.run.units); k++ {
		n += len(r.run.units[k].Items)
	}

	return n
}

// done records the end of a run, frees the slot, and reloads the
// sections that the run changed. A run that the service refused before
// any work returns to the review with the reason; only a new preview
// helps. A run that ended without a result after its work began shows a
// failed result from the events, because answers can be saved already.
func (r *extRecord) done(msg extDoneMsg) tea.Cmd {
	if r.step != stepRunning {
		return nil
	}

	reload := refresh(extractRefresh...)

	// The shell clears its run record after this message, so the stop
	// request of the quit question is read now.
	r.run.stopping = r.stopping()
	r.run.stopAsk = false

	x := msg.exec

	switch {
	case x == nil && (msg.panicked || r.run.began()):
		x = r.salvage()
	case x == nil:
		r.step = stepPreview
		r.prev.stale = msg.err

		if r.prev.stale == nil {
			r.prev.stale = errors.New("the service returned no result")
		}

		return tea.Batch(mutationDone, reload, r.warn("The trigger extraction did not start: "+
			extStaleText(r.prev.stale)+"."))
	}

	if r.run.internal && msg.err == nil {
		msg.err = errors.New("the service returned no result")
	}

	r.step = stepResult
	r.run.exec, r.run.err, r.run.ended = x, msg.err, msg.at
	r.run.sending = -1
	r.run.follow = true

	if !r.run.moved {
		r.run.cursor = r.firstOpen()
	}

	// The compact result opens at its top, so the verdict and its lead
	// show first. The wide Proposals pane follows the cursor again.
	r.run.moved = false
	r.run.list = scrollState{}

	v := r.verdict()

	return tea.Batch(mutationDone, reload, toast(v.tone, v.toast))
}

// salvage builds the result of a run that ended on an internal error
// from the events that arrived. The batch that was sending keeps its
// unattempted items, and lost marks it, because its reply can be saved
// or not.
func (r *extRecord) salvage() *distill.ExtractExecution {
	run := &r.run
	run.internal, run.lost = true, run.sending

	return &distill.ExtractExecution{Batches: slices.Clone(run.units)}
}

// stop asks the running work to stop. The engine starts no new batch and
// cancels the running agent call; the record shows "stopping" until the
// work ends.
func (r *extRecord) stop() tea.Cmd {
	r.run.stopAsk = false

	if r.step != stepRunning || r.stopping() {
		return nil
	}

	if !r.st.requestStop() {
		return nil
	}

	r.run.stopping = true
	r.run.stopAt = r.run.sending

	detail := "waiting for the run to end"
	if k := r.run.stopAt; k >= 0 {
		detail = "waiting for the agent call of batch " + itoa(k+1)
	}

	r.st.updateRun(func(n *runNote) { n.detail = detail })
	r.run.log(time.Now(), "stop requested: "+detail)

	return nil
}

// stopping reports a requested stop of the running work: from the stop
// question here, or from the quit question of the shell.
func (r *extRecord) stopping() bool {
	return r.run.stopping || (r.step == stepRunning && r.st.run != nil && r.st.run.stopping)
}

// settingsSaved marks a preview out of date after a settings save. The
// preview binds the whole configuration file, so a run refuses it after
// any save. The mark avoids a y that can only fail. A running record
// keeps going: the engine checks the file before each batch and stops
// with a conflict.
func (r *extRecord) settingsSaved(msg settingsSavedMsg) {
	if r.step != stepPreparing && r.step != stepPreview {
		return
	}

	r.prev.stale = extSettingsStale{group: msg.group.String()}
}

// extSettingsStale marks a preview that a settings save made out of
// date. It has its own type, because the review must not report it as
// a refused start: nobody pressed y.
type extSettingsStale struct {
	// group names the saved settings group, for example "inference".
	group string
}

// Error describes the save in the words of the review.
func (e extSettingsStale) Error() string {
	return "Settings changed (" + e.group + ")"
}

// remaining returns the planned proposals whose question is still open
// after the run: failed, cancelled, conflicting, unattempted and unknown
// items, in send order. It leaves out a row whose question closed during
// the run, because another writer can dismiss or answer a row then.
// "Prepare remaining" plans exactly these again.
func (r *extRecord) remaining() []int64 {
	if r.step != stepResult {
		return nil
	}

	var ids []int64

	for _, b := range r.outcomes() {
		for _, item := range b.Items {
			if _, closed := r.closedNow(item); extSaved(item) || closed {
				continue
			}

			ids = append(ids, item.Proposal.ID)
		}
	}

	return ids
}

// extSaved reports an item with a saved answer, positive or negative. A
// saved answer closes the question of its row.
func extSaved(item distill.ExtractItemOutcome) bool {
	return item.Status == distill.ExtractStored || item.Status == distill.ExtractNegative
}

// liveRow returns the current row of proposal id from the ledger
// snapshot. ok is false when the snapshot is not loaded or does not hold
// the row.
func (r *extRecord) liveRow(id int64) (row domain.Proposal, ok bool) {
	sec := &r.st.ledger
	if !sec.loaded || sec.value == nil {
		return domain.Proposal{}, false
	}

	for f := propPending; f < propStateCount; f++ {
		rows := propStateRows(sec.value, f)
		if i := slices.IndexFunc(rows, func(p domain.Proposal) bool { return p.ID == id }); i >= 0 {
			return rows[i], true
		}
	}

	return domain.Proposal{}, false
}

// closedNow reports that the question of a result item without a saved
// answer is closed now, and the reason. The end of the run reloads the
// ledger, so the snapshot shows a row that another writer dismissed or
// answered during the run. An unknown row counts as open, because a new
// preview checks every rule again.
func (r *extRecord) closedNow(item distill.ExtractItemOutcome) (why string, closed bool) {
	if r.step != stepResult || extSaved(item) {
		return "", false
	}

	row, ok := r.liveRow(item.Proposal.ID)
	if !ok {
		return "", false
	}

	open, why := extQuestion(r.st, r.st.ledger.value, row)

	return why, !open
}

// nowStatus returns the current ledger status of a result item without a
// saved answer, when it differs from the planned status. A saved answer
// passed its guard, so its row had the planned status at the save.
func (r *extRecord) nowStatus(item distill.ExtractItemOutcome) (status string, changed bool) {
	if r.step != stepResult || extSaved(item) {
		return "", false
	}

	row, ok := r.liveRow(item.Proposal.ID)
	if !ok || row.Status == item.Proposal.Status {
		return "", false
	}

	return row.Status, true
}

// outcomes returns the batch outcomes: the final ones after the run,
// else the live ones from the events.
func (r *extRecord) outcomes() []distill.ExtractBatchOutcome {
	if r.run.exec != nil && len(r.run.exec.Batches) > 0 {
		return r.run.exec.Batches
	}

	return r.run.units
}

// runItems returns the items of the outcomes in send order with their
// position.
func (r *extRecord) runItems() []extItemRef {
	var out []extItemRef

	for bi, b := range r.outcomes() {
		for ii := range b.Items {
			out = append(out, extItemRef{batch: bi, item: ii})
		}
	}

	return out
}

// firstOpen returns the first item whose question stays open. The result
// opens on it, because it needs attention. It returns 0 when every item
// has a saved answer.
func (r *extRecord) firstOpen() int {
	for i, ref := range r.runItems() {
		if !extSaved(r.outcomes()[ref.batch].Items[ref.item]) {
			return i
		}
	}

	return 0
}

// cursorItem returns the outcome under the cursor of the run or the
// result, and false without one.
func (r *extRecord) cursorItem() (distill.ExtractItemOutcome, int, bool) {
	refs := r.runItems()
	if len(refs) == 0 {
		return distill.ExtractItemOutcome{}, -1, false
	}

	r.run.cursor = max(0, min(r.run.cursor, len(refs)-1))
	ref := refs[r.run.cursor]

	return r.outcomes()[ref.batch].Items[ref.item], ref.batch, true
}

// link returns the view that the result links for the item under the
// cursor, and its name. An applied row that stored trigger paths links
// to its pin, where a retarget is a separate preview. A pending row with
// a saved answer links to its proposal, whose regions can have changed.
// The links only navigate: they never start a retarget or an apply.
func (r *extRecord) link() (cmd tea.Cmd, name string, ok bool) {
	if r.step != stepResult {
		return nil, "", false
	}

	item, _, found := r.cursorItem()
	if !found {
		return nil, "", false
	}

	id := item.Proposal.ID
	arrow := " " + r.st.theme.Glyphs.Arrow + " "

	switch {
	case item.Proposal.Status == domain.ProposalApplied && item.Status == distill.ExtractStored:
		return openPins(id), "Lessons" + arrow + "Pins", true
	case item.Proposal.Status == domain.ProposalProposed &&
		(item.Status == distill.ExtractStored || item.Status == distill.ExtractNegative):
		return focusProposal(id), "Lessons" + arrow + "Proposals", true
	}

	return nil, "", false
}

// blockerLink returns the place that fixes the blocker of the review
// and its name. ok is false without such a place. The index build
// opens its own confirmation, so the link never builds anything.
func (r *extRecord) blockerLink() (to navigateMsg, name string, ok bool) {
	if r.step != stepPreview {
		return navigateMsg{}, "", false
	}

	inference := navigateMsg{dest: destSettings, intent: "group:inference"}
	arrow := " " + r.st.theme.Glyphs.Arrow + " "

	var cfg *learning.ExtractConfigError

	switch err := r.prev.err; {
	case errors.As(err, &cfg) && cfg.Path == settingsFile:
		return inference, "Settings" + arrow + "Inference", true
	case errors.As(err, &cfg) && cfg.Path == pinfile.File:
		// Settings has no form for the pin file. Pins shows where the
		// file fails to read.
		return navigateMsg{dest: destLessons, intent: "pins"}, "Lessons" + arrow + "Pins", true
	case errors.Is(err, learning.ErrExtractNotReady):
		return navigateMsg{dest: destOverview, intent: ovIntentBuild}, "Overview" + arrow + "build", true
	case err == nil && r.agentBlocked():
		// A refused start (learning.ErrExtractBlocked) repeats the
		// blockers of the preview, so this case covers it too.
		return inference, "Settings" + arrow + "Inference", true
	}

	return navigateMsg{}, "", false
}

// agentBlocked reports a preview blocker that Settings › Inference can
// fix: a blocker of the agent command, not of the working directory.
// The engine marks the working-directory blocker
// (learning.ExtractPreview.IsWorkDirBlocker), so the review never
// matches its text.
func (r *extRecord) agentBlocked() bool {
	p := r.prev.preview
	if p == nil {
		return false
	}

	return slices.ContainsFunc(p.Blockers, func(b string) bool { return !p.IsWorkDirBlocker(b) })
}

// key routes a key press by step.
func (r *extRecord) key(msg tea.KeyMsg) tea.Cmd {
	switch r.step {
	case stepPreparing:
		if key.Matches(msg, nkeys.Back) {
			return closeRun(r)
		}
	case stepPreview:
		return r.previewKey(msg)
	case stepRunning:
		return r.runKey(msg)
	case stepResult:
		return r.resultKey(msg)
	}

	return nil
}

// previewKey handles the Preview step. y starts only a ready preview;
// enter plans again only when no preview can start. esc drops the
// record, because it did no work.
func (r *extRecord) previewKey(msg tea.KeyMsg) tea.Cmd {
	pl := &r.prev

	switch {
	case key.Matches(msg, agentKeys.Start):
		return r.start()
	case key.Matches(msg, agentKeys.Link):
		if to, _, ok := r.blockerLink(); ok {
			return navigate(to.dest, to.intent)
		}
	case key.Matches(msg, nkeys.Back):
		return closeRun(r)
	case key.Matches(msg, nkeys.Enter):
		if !r.runnable() {
			return r.startPlan()
		}
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		switch {
		case !r.hasItems():
		case r.st.wide():
			pl.planFocus = !pl.planFocus
		default:
			pl.listOpen = !pl.listOpen
		}
	default:
		r.previewMove(msg)
	}

	return nil
}

// hasItems reports a preview with planned items to review.
func (r *extRecord) hasItems() bool {
	return r.prev.err == nil && len(r.plannedItems()) > 0
}

// planKeys reports that the movement keys scroll the Plan pane: it has
// the focus in the wide layout, or it shows in the compact layout.
func (r *extRecord) planKeys() bool {
	if !r.hasItems() {
		return true
	}

	if r.st.wide() {
		return r.prev.planFocus
	}

	return !r.prev.listOpen
}

// previewMove moves the item cursor or scrolls the focused pane.
func (r *extRecord) previewMove(msg tea.KeyMsg) {
	pl := &r.prev

	if r.planKeys() {
		pl.notes.key(msg)

		return
	}

	if cursorKey(msg, &pl.cursor, len(r.plannedItems())) {
		pl.follow = true

		return
	}

	if pl.list.key(msg) {
		pl.follow = false
	}
}

// runKey handles the running record: x asks before it stops.
func (r *extRecord) runKey(msg tea.KeyMsg) tea.Cmd {
	run := &r.run

	if run.stopAsk {
		switch {
		case key.Matches(msg, agentKeys.Start):
			return r.stop()
		case key.Matches(msg, nkeys.Back):
			run.stopAsk = false
		}

		return nil
	}

	if key.Matches(msg, agentKeys.Stop) {
		if !r.stopping() {
			run.stopAsk = true
		}

		return nil
	}

	r.runMove(msg)

	return nil
}

// resultKey handles the result. enter opens the view that the cursor
// item links, and n prepares a new preview of the open questions.
// Nothing repeats the run, and nothing chains into a retarget or an
// apply.
func (r *extRecord) resultKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter):
		if cmd, _, ok := r.link(); ok {
			return cmd
		}
	case key.Matches(msg, extKeys.Remaining):
		if ids := r.remaining(); len(ids) > 0 {
			// The run chose these IDs, not a cursor row.
			return openExtract(learning.ExtractRequest{IDs: ids}, false)
		}
	default:
		r.runMove(msg)
	}

	return nil
}

// runMove moves the item cursor, scrolls the focused pane, or changes
// the pane with tab.
func (r *extRecord) runMove(msg tea.KeyMsg) {
	run := &r.run

	if key.Matches(msg, nkeys.Next, nkeys.Prev) {
		if r.st.wide() {
			run.sumFocus = !run.sumFocus
		} else {
			run.detail = !run.detail
		}

		return
	}

	if r.st.wide() && run.sumFocus {
		run.summary.key(msg)

		return
	}

	if cursorKey(msg, &run.cursor, len(r.runItems())) {
		run.follow, run.moved = true, true

		return
	}

	if run.list.key(msg) {
		run.follow = false
	}
}

// keys returns the footer keys of the current step. The Run sub-view
// adds the sub-view switch keys while the record does not capture. The
// action of a step comes first, because a narrow footer drops hints
// from the end.
func (r *extRecord) keys() []key.Binding {
	switch r.step {
	case stepPreparing:
		return []key.Binding{withHelp(nkeys.Back, "esc", "cancel")}
	case stepPreview:
		return r.previewKeys()
	case stepRunning:
		return r.runKeys()
	default:
		return r.resultKeys()
	}
}

// previewKeys names the keys of the Preview step for its state. y shows
// only for a ready preview, so a blocked or empty preview offers no
// start.
func (r *extRecord) previewKeys() []key.Binding {
	pl := &r.prev

	if r.runnable() {
		move, pane := withHelp(nkeys.Down, "↑↓", "proposal"), withHelp(nkeys.Next, "tab", "plan")
		if r.planKeys() {
			move, pane = withHelp(nkeys.Down, "↑↓", "scroll"), withHelp(nkeys.Next, "tab", "proposals")
		}

		return []key.Binding{
			withHelp(agentKeys.Start, "y", "start run"), pane, move,
			withHelp(nkeys.PageDown, "pgup/pgdn", "scroll"), withHelp(nkeys.Back, "esc", "close"),
		}
	}

	again := withHelp(nkeys.Enter, "enter", "plan again")
	if !r.st.wide() {
		again = withHelp(nkeys.Enter, "enter", "plan")
	}

	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(pl.notes.scrolls() || pl.list.scrolls() || r.hasItems())

	pane := withHelp(nkeys.Next, "tab", "pane")
	pane.SetEnabled(r.hasItems())

	return []key.Binding{again, r.linkKey(), pane, scroll, withHelp(nkeys.Back, "esc", "close")}
}

// linkKey names the blocker link in the footer; disabled without one. A
// compact footer has room for a short label only, so it names the
// destination alone.
func (r *extRecord) linkKey() key.Binding {
	to, name, ok := r.blockerLink()

	b := withHelp(agentKeys.Link, "o", "open "+name)

	switch {
	case r.st.wide():
	case to.dest == destOverview:
		b = withHelp(agentKeys.Link, "o", "overview")
	case to.dest == destLessons:
		b = withHelp(agentKeys.Link, "o", "pins")
	default:
		b = withHelp(agentKeys.Link, "o", "settings")
	}

	b.SetEnabled(ok)

	return b
}

// runKeys names the keys of the running record. x comes first, so the
// stop key of a paid run survives a narrow footer.
func (r *extRecord) runKeys() []key.Binding {
	if r.run.stopAsk {
		return []key.Binding{withHelp(agentKeys.Start, "y", "stop the run"), withHelp(nkeys.Back, "esc", "keep running")}
	}

	stop := withHelp(agentKeys.Stop, "x", "stop")
	stop.SetEnabled(!r.stopping())

	return []key.Binding{stop, withHelp(nkeys.Down, "↑↓", "proposal"), r.paneKey()}
}

// resultKeys names the keys of the result. enter shows only when the
// cursor item links a view, and n only when a question stays open.
func (r *extRecord) resultKeys() []key.Binding {
	_, name, ok := r.link()

	open := withHelp(nkeys.Enter, "enter", "open "+strings.ToLower(extLinkTail(name)))
	if !r.st.wide() {
		open = withHelp(nkeys.Enter, "enter", "open")
	}

	open.SetEnabled(ok)

	again := withHelp(extKeys.Remaining, "n", "prepare remaining")
	if !r.st.wide() {
		again = withHelp(extKeys.Remaining, "n", "remaining")
	}

	again.SetEnabled(len(r.remaining()) > 0)

	return []key.Binding{open, again, withHelp(nkeys.Down, "↑↓", "proposal"), r.paneKey()}
}

// extLinkTail returns the last part of a place name, for example "Pins"
// of "Lessons › Pins".
func extLinkTail(name string) string {
	if i := strings.LastIndex(name, " "); i >= 0 {
		return name[i+1:]
	}

	return name
}

// paneKey names what tab shows next in the run and result views.
func (r *extRecord) paneKey() key.Binding {
	switch {
	case r.st.wide():
		return withHelp(nkeys.Next, "tab", "pane")
	case r.run.detail:
		return withHelp(nkeys.Next, "tab", "summary")
	default:
		return withHelp(nkeys.Next, "tab", "proposals")
	}
}

// extStaleText describes a refusal. A changed input wraps
// distill.ErrExtractStale with its cause. The sentence names only the
// cause, because the prefix repeats what the console says around it.
func extStaleText(err error) string {
	text := errText(err)

	if errors.Is(err, distill.ErrStale) {
		if cause, ok := strings.CutPrefix(text, distill.ErrExtractStale.Error()+": "); ok && cause != "" {
			return cause
		}
	}

	return text
}

// extQuestion reports whether t may ask proposal p, and the reason in
// words. It applies the rules that the snapshots of the console can
// check. These are the status, the question, the region fields, the pin
// link, and the living evidence. An unknown input counts as open,
// because the preview checks every rule again and lists the reason. The
// Proposals sub-view uses it, so t shows only with an eligible target.
func extQuestion(st *state, l *proposals.Ledger, p domain.Proposal) (open bool, why string) {
	switch p.Status {
	case domain.ProposalProposed, domain.ProposalApplied:
	case domain.ProposalDismissed:
		return false, "dismissed"
	case domain.ProposalSuperseded:
		return false, "superseded"
	default:
		return false, "status " + oneLine(p.Status)
	}

	asked, reason := distill.TriggerQuestionOpen(p)
	if !asked {
		return false, reason.String()
	}

	if !pinfile.FieldsAgree(p) {
		return false, pinfile.LinkAmbiguousFields.Reason()
	}

	if link, known := extLink(st, p.ID); known {
		switch {
		case p.Status == domain.ProposalApplied && link != pinfile.LinkLinked:
			return false, link.Reason()
		case p.Status == domain.ProposalProposed && link == pinfile.LinkPendingMatch:
			return false, "a pin already carries it" + pendingMatchStep(l, p)
		}
	}

	if l != nil && l.EvidenceErr == nil && !slices.ContainsFunc(p.Members, func(id int64) bool {
		_, ok := l.Evidence[id]

		return ok
	}) {
		return false, "no living evidence"
	}

	return true, reason.String()
}

// pendingMatchStep gives the next step for pending row p, whose pin is
// already in the pin file. The usual step is the record with c. The
// record refuses a row whose pin key an applied proposal records
// (proposals.CheckAppliedKeys), and c is n/a for it. So the step then is
// the dismiss of the preview refusal. The text names no key, because d
// acts on the marks, and the DECISION section gives the key for them.
func pendingMatchStep(l *proposals.Ledger, p domain.Proposal) string {
	if l != nil {
		// The caller checks that the region fields of p agree, as the
		// check needs.
		if taken := proposals.CheckAppliedKeys(l.Applied, []domain.Proposal{p}); taken != nil {
			return ", and " + taken.Error()
		}
	}

	return "; record it as applied first"
}

// appliedPinText tells where the pin of applied proposal id is, for the
// DECISION section of Proposals. An applied proposal is a ledger state,
// not proof that a pin exists (contracts vocabulary), so the text follows
// the link of the Pins snapshot. An unknown link gets text without a
// claim about the pin.
func appliedPinText(st *state, id int64) string {
	pins := "Lessons " + st.theme.Glyphs.Arrow + " Pins"

	link, _ := extLink(st, id)

	switch link {
	case pinfile.LinkLinked:
		return "The pin of this proposal is in " + pins + " (press m), which previews a retarget or a prune of it."
	case pinfile.LinkNotPresent:
		return "No entry of " + proposals.LessonsFile + " carries the pin of this proposal. " + pins +
			" lists it under not present (press m, then f). Nothing is superseded automatically."
	case pinfile.LinkAmbiguousFile, pinfile.LinkAmbiguousLedger, pinfile.LinkAmbiguousFields:
		return pins + " (press m) shows every match (" + link.Reason() + "). Retarget and prune stay blocked " +
			"until the match is resolved."
	default:
		return pins + " (press m) shows the installed pins and previews a retarget or a prune of a linked pin."
	}
}

// extLink returns the pin link of proposal id from the Pins snapshot.
// known is false when the snapshot or its links are not available, or
// when the snapshot does not hold the row yet.
func extLink(st *state, id int64) (link pinfile.Link, known bool) {
	sec := &st.pins
	if !sec.loaded || sec.value == nil || sec.value.Links == nil {
		return pinfile.LinkUnknown, false
	}

	link, known = sec.value.Links.ByProposal[id]
	if !known || link == pinfile.LinkUnknown {
		return pinfile.LinkUnknown, false
	}

	return link, true
}
