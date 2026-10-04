package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds the evidence refresh run (design map §6 to §8). It
// starts the run from a confirmed preview and runs the worker. It
// records the typed progress, the stop and the result in Lessons › Run.
// les_evidence_run_view.go renders the record. The run holds the
// single mutation slot from y until its result arrives.

// evEventBuffer bounds the queue between the worker and the event loop.
// The loop drains it all the time; the bound only matters when the loop
// stalls, and then the progress drops, never the result.
const evEventBuffer = 256

// evActivityKeep is the number of activity lines that a record keeps.
// The view shows fewer; a small bound keeps raw detail from taking over
// the screen.
const evActivityKeep = 8

// evActivityShow is the number of activity lines that the record shows,
// as in the index build panel.
const evActivityShow = ovLogKeep

// evKeys are the keys of the run record. The package values stay
// enabled; keys() returns copies with the current availability.
var evKeys = struct {
	Stop, Again key.Binding
}{
	Stop:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
	Again: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "plan again")),
}

// evPhase is one step of an evidence run as the record shows it.
type evPhase int

const (
	evPhMine evPhase = iota
	evPhSaveFixes
	evPhFetch
	evPhSaveReviews
	evPhCount
)

// evPhaseLabels name the steps in the engine order: the fix phase ends
// with its write before any GitHub work starts.
var evPhaseLabels = [evPhCount]string{
	"Mine local fixes", "Save local fixes", "Fetch GitHub reviews", "Save review findings",
}

// evEventMsg carries one progress event of run id to the event loop.
type evEventMsg struct {
	id int
	ev index.RefreshEvent
}

// evDoneMsg carries the result of run id. It is the last message on the
// channel of the run, so every event that the queue kept arrives first.
type evDoneMsg struct {
	id  int
	out learning.Outcome
	err error
}

// evWaitMsg wraps a message that the wait command read from the channel
// of run id. Only a wrapped event asks for the next message, so a test
// can inject events without a second reader of the channel.
type evWaitMsg struct {
	id  int
	msg tea.Msg
}

// evRun is the record of one evidence refresh in Lessons › Run. The
// Evidence view feeds it events and the result; the Run sub-view shows
// it and sends it keys.
type evRun struct {
	st   *state
	id   int
	plan *learning.Plan
	ch   chan tea.Msg
	// again prepares the same refresh again from the current state.
	again func(index.SourceMode) tea.Cmd

	started, finished time.Time
	running           bool
	// stopAsked is true after y in the stop question of this record. A
	// stop from the quit question shows through the shared run note.
	stopAsked bool
	// confirm is true while the stop question is open.
	confirm bool

	phase [evPhCount]phaseState
	note  [evPhCount]string
	// seen marks the steps that an event started, so the result can
	// tell which step a failure or a stop ended.
	seen [evPhCount]bool
	// fetched is true after the fetch reported its comment counts.
	fetched bool
	// reported holds the state that each source reported at its end.
	// The Sources pane shows it while the run goes on.
	reported map[string]index.SourceState
	activity []string

	out learning.Outcome
	err error

	// right is true while the Sources pane has the focus.
	right  bool
	scroll [2]scrollState
}

// newEvRun starts the record of run id for plan.
func newEvRun(st *state, id int, plan *learning.Plan, again func(index.SourceMode) tea.Cmd, now time.Time) *evRun {
	r := &evRun{
		st: st, id: id, plan: plan, again: again, ch: make(chan tea.Msg, evEventBuffer),
		started: now, running: true, reported: map[string]index.SourceState{},
	}

	r.log(now, "run started: "+evModeTitle(plan.Mode))

	return r
}

// start claims the mutation slot and starts the confirmed plan. It
// refuses without a ready plan, and while another mutation runs, with
// the reason. The preview closes, so a repeated y cannot start the run
// twice.
func (v *evidenceView) start() tea.Cmd {
	if !v.ready() {
		return nil
	}

	if v.st.busy() {
		return toast(ToneAttention, "Another operation is running: "+oneLine(v.st.mutation)+
			". The refresh can start after it ends.")
	}

	plan := v.prev.plan
	ctx, cancel := v.st.runContext()

	note := runNote{
		verb: "Refreshing evidence", detail: evModeTitle(plan.Mode), stop: cancel,
		home: navigateMsg{dest: destLessons, intent: "run"},
	}

	if !v.st.beginRun("Refreshing evidence", note) {
		cancel()

		return toast(ToneAttention, "Another operation is running: "+oneLine(v.st.mutation))
	}

	v.st.lastRunKind = runEvidence
	v.closePreview("")

	v.runs++
	r := newEvRun(v.st, v.runs, plan, v.prepareAgain, time.Now())
	v.run = r

	svc, id, ch := v.st.svc, r.id, r.ch

	work := v.st.goWork(func() tea.Msg {
		defer cancel()

		evWork(ctx, svc, plan, id, ch)

		return nil
	})

	return tea.Batch(openRun(r, true), work, evWait(id, ch))
}

// prepareAgain opens Lessons › Evidence and plans mode again. The plan
// reads the current state, so a stale or partial run never repeats old
// facts.
func (v *evidenceView) prepareAgain(mode index.SourceMode) tea.Cmd {
	if v.prev != nil {
		return navigate(destLessons, "evidence")
	}

	return tea.Batch(v.plan(mode), navigate(destLessons, "evidence"))
}

// evWork runs the plan and sends its events and then its result through
// ch. It runs in a command goroutine, never in Update. A panic becomes an
// error result, so the mutation slot always frees. The record names that
// error, because no source reason explains it.
func evWork(ctx context.Context, svc Services, plan *learning.Plan, id int, ch chan<- tea.Msg) {
	defer close(ch)

	var (
		out learning.Outcome
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the evidence refresh stopped on an internal error: %v", r)
			}
		}()

		out, err = svc.RunEvidence(ctx, plan, evForward(id, ch))
	}()

	// A result without source states would read as known zero counts,
	// so it becomes an honest "not attempted" with unknown counts.
	if out.Fixes.State == "" {
		out.Fixes.State = index.SourceNotAttempted
	}

	if out.Reviews.State == "" {
		out.Reviews.State = index.SourceNotAttempted
	}

	if err != nil && out.After.CountsErr == nil && out.After.Probe.State != workspace.IndexReady {
		out.After.CountsErr = err
	}

	ch <- evDoneMsg{id: id, out: out, err: err}
}

// evForward adapts engine events to run messages. The worker is the only
// sender, so a slot that is free before a send stays free for the
// result. The forwarder keeps that last slot, so the result never waits.
func evForward(id int, ch chan<- tea.Msg) func(index.RefreshEvent) {
	return func(ev index.RefreshEvent) {
		if len(ch) >= cap(ch)-1 {
			return
		}

		ch <- evEventMsg{id: id, ev: ev}
	}
}

// evWait returns a command that delivers the next message of run id. It
// returns nil after the last one, so the chain ends by itself.
func evWait(id int, ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}

		return evWaitMsg{id: id, msg: msg}
	}
}

// waited handles a message from the channel of the current run and asks
// for the next one after an event.
func (v *evidenceView) waited(msg evWaitMsg) tea.Cmd {
	r := v.run
	if r == nil || msg.id != r.id {
		return nil
	}

	switch m := msg.msg.(type) {
	case evEventMsg:
		v.event(m)

		return evWait(r.id, r.ch)
	case evDoneMsg:
		return v.done(m)
	}

	return nil
}

// event records one progress event of the current run.
func (v *evidenceView) event(msg evEventMsg) {
	if r := v.run; r != nil && msg.id == r.id {
		r.apply(msg.ev, time.Now())
	}
}

// done records the result of the current run, frees the mutation slot
// and reloads every source that the evidence feeds. It never retries.
func (v *evidenceView) done(msg evDoneMsg) tea.Cmd {
	r := v.run
	if r == nil || msg.id != r.id || !r.running {
		return nil
	}

	r.finish(msg.out, msg.err, time.Now())

	return tea.Batch(mutationDone, refresh(secLearning, secLedger, secIndex, secStats, secDoctor, secPins), r.toast())
}

// apply records one progress event. The engine sends no event when it
// leaves a step, so a later step closes the earlier ones.
func (r *evRun) apply(ev index.RefreshEvent, now time.Time) {
	if !r.running {
		return
	}

	slug := evRepoShort(r.plan.Repository, r.plan.RepoState)
	sep := " " + r.st.theme.Glyphs.Sep + " "

	switch ev.Phase {
	case index.PhaseMineFixes:
		r.begin(evPhMine)
		r.log(now, "mining the fix commits reachable from HEAD")
		r.detail(evNameFixes)
	case index.PhasePersist:
		if ev.Source == index.SourceReviews {
			r.close(evPhFetch)
			r.begin(evPhSaveReviews)
			r.log(now, "saving the review findings")
			r.detail("saving " + evNameReviews)

			break
		}

		r.close(evPhMine)
		r.begin(evPhSaveFixes)
		r.log(now, "saving the local fix findings")
		r.detail("saving " + evNameFixes)
	case index.PhaseSourceDone:
		o := index.SourceOutcome{State: ev.State}
		r.reported[ev.Source] = ev.State
		r.settle(ev.Source, o)
		r.log(now, evSourceName(ev.Source)+": "+evOutcomeState(o).word)
	case index.PhaseFetchReviews:
		r.begin(evPhFetch)
		r.note[evPhFetch] = slug
		r.log(now, "fetching the review comments of "+slug+" through gh")
		r.detail(evNameReviews)
	case index.PhaseFetchBytes:
		r.note[evPhFetch] = evBytes(ev.Bytes) + " received"
		r.detail(evNameReviews + sep + evBytes(ev.Bytes) + " received")
	case index.PhaseFetched:
		r.fetched = true
		r.note[evPhFetch] = propPlural(ev.Comments, "comment", "comments") + sep + itoa(ev.Kept) + " in the window"
		r.log(now, propPlural(ev.Comments, "review comment", "review comments")+" fetched, "+itoa(ev.Kept)+
			" in the window")
		r.detail(evNameReviews + sep + propPlural(ev.Comments, "comment", "comments") + " fetched")
	case index.PhaseCluster:
		// The fetch step keeps its comment counts as its note. A done step
		// shows its note as its result, and progress text is wrong there.
		// The run note and the activity show the grouping instead.
		r.fetched = true
		r.log(now, "grouping the review comments into lessons")
		r.detail(evNameReviews + sep + "grouping")
	}
}

// begin marks a step active.
func (r *evRun) begin(p evPhase) {
	r.phase[p], r.seen[p] = phaseActive, true
}

// close marks an active or seen step done.
func (r *evRun) close(p evPhase) {
	if r.seen[p] {
		r.phase[p] = phaseDone
	}
}

// detail sets the fact of the shared run note, which the notice row
// shows on every destination. It carries activity only: a fetch has no
// known total, so the note never shows a count of a total.
func (r *evRun) detail(text string) {
	if r.running {
		r.st.updateRun(func(n *runNote) { n.detail = text })
	}
}

// log keeps one activity line with its time, within evActivityKeep.
func (r *evRun) log(now time.Time, text string) {
	r.activity = append(r.activity, now.Format("15:04:05")+"  "+text)
	if len(r.activity) > evActivityKeep {
		r.activity = r.activity[len(r.activity)-evActivityKeep:]
	}
}

// evSourceName names an engine source for the record.
func evSourceName(source string) string {
	if source == index.SourceReviews {
		return evNameReviews
	}

	return evNameFixes
}

// settle sets the two steps of a source from its state. The seen steps
// tell which step a failure or a stop ended. A write that started ends
// in the write step. A fetch that returned its counts ended before the
// write.
func (r *evRun) settle(source string, o index.SourceOutcome) {
	work, save := evPhMine, evPhSaveFixes
	if source == index.SourceReviews {
		work, save = evPhFetch, evPhSaveReviews
	}

	notTried := func(p evPhase) {
		r.phase[p], r.note[p] = phasePending, "not attempted"
	}

	// ended marks the step that the state ended: the write when it
	// started, else the work step.
	ended := func(state phaseState, beforeSave bool) {
		if r.seen[save] || beforeSave {
			r.phase[work], r.phase[save] = phaseDone, state

			return
		}

		r.phase[work] = state
		notTried(save)
	}

	switch o.State {
	case index.SourceRefreshed, index.SourceEmpty:
		r.phase[work], r.phase[save] = phaseDone, phaseDone
	case index.SourceUnavailable:
		r.phase[work], r.note[work] = phaseSkipped, evCauseWord(o.Cause)
		notTried(save)
	case index.SourceFailed:
		// A store or refusal failure happens at the open before the
		// write, after the work step ended.
		ended(phaseFailed, r.seen[work] && (o.Cause == index.CauseStore || o.Cause == index.CauseRefused))
	case index.SourceStopped:
		// A stop after the work step arrives at the hook before the
		// open. The fix mine always ends first. A fetch that returned
		// its counts ended too.
		after := r.seen[work] && (source == index.SourceFixes || r.fetched)
		ended(phaseStopped, after)
	default:
		notTried(work)
		notTried(save)
	}
}

// finish records the result and closes every step from the typed
// source outcomes. The steps then agree with the result, also when the
// queue dropped events.
func (r *evRun) finish(out learning.Outcome, err error, now time.Time) {
	r.running, r.confirm, r.finished = false, false, now
	r.out, r.err = out, err

	r.settle(index.SourceFixes, out.Fixes)
	r.note[evPhSaveFixes] = evStoredNote(out.Fixes, false, r.note[evPhSaveFixes])

	if r.reviewMode() {
		r.settle(index.SourceReviews, out.Reviews)
		r.note[evPhSaveReviews] = evStoredNote(out.Reviews, true, r.note[evPhSaveReviews])
	}

	// A failed step names its typed cause instead of its last activity,
	// because the cause decides the next step.
	for _, p := range []evPhase{evPhMine, evPhSaveFixes, evPhFetch, evPhSaveReviews} {
		if cause := evCauseWord(r.outcome(p).Cause); r.phase[p] == phaseFailed && cause != "" {
			r.note[p] = cause
		}
	}

	r.log(now, "finished: "+r.verdict().head)
}

// outcome returns the source outcome that owns a step.
func (r *evRun) outcome(p evPhase) index.SourceOutcome {
	if p >= evPhFetch {
		return r.out.Reviews
	}

	return r.out.Fixes
}

// evStoredNote is the note of a write step after a success: the stored
// counts. Other states keep the note they have.
func evStoredNote(o index.SourceOutcome, reviews bool, keep string) string {
	if !evSucceeded(o) {
		return keep
	}

	if reviews {
		return propPlural(o.Stored, "finding", "findings") + ", " + propPlural(o.Lessons, "lesson", "lessons")
	}

	return propPlural(o.Stored, "finding", "findings")
}

// reviewMode reports a run that includes the GitHub reviews.
func (r *evRun) reviewMode() bool {
	return r.plan.Mode == index.SourcesReviewsAndLocal
}

// stopping reports a requested stop while the worker still runs. The
// quit question can also request it, through the shared run note.
func (r *evRun) stopping() bool {
	return r.running && (r.stopAsked || (r.st.run != nil && r.st.run.stopping))
}

// active implements runRecord: the record is active until the result
// arrives, so the Run sub-view keeps its progress visible.
func (r *evRun) active() bool { return r.running }

// title implements runRecord.
func (r *evRun) title() string {
	return "Evidence refresh " + r.st.theme.Glyphs.Sep + " " + evModeTitle(r.plan.Mode)
}

// capturing implements runRecord: the stop question takes every key.
func (r *evRun) capturing() bool { return r.confirm }

// succeeded counts the requested sources that replaced their corpus.
func (r *evRun) succeeded() int {
	n := 0

	for _, o := range r.sources() {
		if evSucceeded(o) {
			n++
		}
	}

	return n
}

// sources returns the outcomes of the requested sources in the engine
// order.
func (r *evRun) sources() []index.SourceOutcome {
	if r.reviewMode() {
		return []index.SourceOutcome{r.out.Fixes, r.out.Reviews}
	}

	return []index.SourceOutcome{r.out.Fixes}
}

// conflict returns the stale-plan error of the result, or nil.
func (r *evRun) conflict() error {
	if r.out.Conflict != nil {
		return r.out.Conflict
	}

	if errors.Is(r.err, learning.ErrConflict) {
		return r.err
	}

	return nil
}

// keys implements runRecord. x comes first, so the stop key survives a
// narrow footer.
func (r *evRun) keys() []key.Binding {
	if r.confirm {
		return []key.Binding{withHelp(lesKeys.Confirm, "y", "stop"), withHelp(nkeys.Back, "esc", "keep running")}
	}

	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(r.scroll[r.pane()].scrolls())

	pane := withHelp(nkeys.Next, "tab", "pane")
	pane.SetEnabled(r.st.width >= wideWidth)

	if r.running {
		stop := evKeys.Stop
		stop.SetEnabled(!r.stopping())

		return []key.Binding{stop, scroll, pane}
	}

	// A compact footer has room for a short label only. The NEXT block
	// in the box keeps the whole sentence.
	distill := withHelp(nkeys.Enter, "enter", "prepare distillation")
	if r.st.width < wideWidth {
		distill = withHelp(nkeys.Enter, "enter", "distill")
	}

	distill.SetEnabled(r.succeeded() > 0 && r.conflict() == nil)

	return []key.Binding{distill, evKeys.Again, scroll, pane}
}

// pane returns the pane that scrolls: the Sources pane when it has the
// focus in a wide layout, else the main pane.
func (r *evRun) pane() int {
	if r.right && r.st.width >= wideWidth {
		return 1
	}

	return 0
}

// update implements runRecord. It handles keys only: the Evidence view
// feeds the events and the result, so no message applies twice.
func (r *evRun) update(msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}

	if r.confirm {
		switch {
		case key.Matches(km, lesKeys.Confirm):
			r.confirm = false

			return r.stop()
		case key.Matches(km, nkeys.Back):
			r.confirm = false
		}

		return nil
	}

	switch {
	case key.Matches(km, evKeys.Stop):
		if r.running && !r.stopping() {
			r.confirm = true
		}
	case key.Matches(km, nkeys.Enter):
		if !r.running && r.succeeded() > 0 && r.conflict() == nil {
			return navigate(destLessons, "distill")
		}
	case key.Matches(km, evKeys.Again):
		if !r.running && r.again != nil {
			return r.again(r.plan.Mode)
		}
	case key.Matches(km, nkeys.Next):
		r.right = !r.right
	default:
		r.scroll[r.pane()].key(km)
	}

	return nil
}

// stop asks the worker to stop. The run keeps the slot until the worker
// returns, because the engine decides where it is safe to end.
func (r *evRun) stop() tea.Cmd {
	if !r.running {
		return nil
	}

	r.stopAsked = true

	if !r.st.requestStop() {
		return toast(ToneAttention, "The evidence refresh cannot stop at this point.")
	}

	r.log(time.Now(), "stop requested: the run ends at the next safe point")

	return nil
}
