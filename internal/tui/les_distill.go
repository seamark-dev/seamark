package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/settings"
)

// This file holds the distillation record of Lessons › Run. It keeps
// the steps, the state and the messages. Its commands plan, run, stop
// and clean up. les_distill_keys.go handles the keys. The views live in
// les_distill_scope.go (scope form), les_distill_plan.go (plan review)
// and les_distill_run.go (run progress and result).

// distStep is the step of one distillation record. The record moves
// forward only on an explicit key, and the paid start needs y.
type distStep int

const (
	// distScope is the draft form: the region and the group limit.
	distScope distStep = iota
	// distPlanning waits for the read-only plan.
	distPlanning
	// distReview shows the plan, its failure, or "nothing to distill".
	distReview
	// distRunning runs the reviewed plan.
	distRunning
	// distResult shows the outcome of the run.
	distResult
)

// distSteps names the steps of the stepper at the right of the context
// line.
var distSteps = []string{"Scope", "Review", "Run", "Result"}

// stepper returns the stepper position of the step.
func (s distStep) stepper() int {
	switch s {
	case distScope:
		return 0
	case distPlanning, distReview:
		return 1
	case distRunning:
		return 2
	default:
		return 3
	}
}

// distLimitOpt is the group limit choice of the scope form.
type distLimitOpt int

const (
	// distLimitDefault is the CLI default, distill.DefaultGroupLimit.
	distLimitDefault distLimitOpt = iota
	// distLimitCustom is a typed whole number above 0.
	distLimitCustom
	// distLimitAll is every eligible group. It maps to the engine's 0
	// only through this explicit choice, because an unbounded paid run
	// must be deliberate (spec R-5).
	distLimitAll
)

// The rows of the scope form that the cursor visits: the region, then
// one row for each limit choice.
const (
	distRowRegion = iota
	distRowDefault
	distRowCustom
	distRowAll
	distRowCount
)

// distCustomDigits bounds the typed group limit. Five digits allow
// 99999 groups, far above any real corpus, so a longer value is a typing
// accident.
const distCustomDigits = 5

// distGetwd reads the process directory for the plan review. It is a
// variable, so a test can fix the directory that a capture shows.
var distGetwd = os.Getwd

// distActivityKeep bounds the recent activity lines, as the index build
// does, so the run detail never grows with the run.
const distActivityKeep = 4

// distForm is the scope draft. It holds typed text only; the form plans
// nothing before enter.
type distForm struct {
	region     propInput
	regionWas  string
	editRegion bool
	// prefill is the Proposals region filter that filled the region
	// input; "" when none did. The form labels it, so the filter never
	// becomes a run scope unseen.
	prefill string

	limit distLimitOpt
	// limitWas is the limit choice before the custom input opened, so
	// esc in that input restores it.
	limitWas   distLimitOpt
	custom     propInput
	customWas  string
	editCustom bool

	// row is the cursor row: one of the distRow constants.
	row int
	// about shows the full explanation in the compact layout.
	about bool
	// sc scrolls the form in a short box; aboutSc scrolls the
	// explanation.
	sc      scrollState
	aboutSc scrollState

	// note is a message about the draft, for example a plan that a
	// settings save dropped. noteTone gives its glyph.
	note     string
	noteTone Tone

	// norm caches the normalized region of the input.
	norm distNorm

	// customChecked shows the rule of the custom limit. It turns on when
	// the input closes or a plan is asked for, so an empty input that
	// just opened shows its placeholder and no error.
	customChecked bool
}

// distNorm is the normalized form of one region input under one root.
// An absolute input resolves symbolic links, which reads the file
// system, so the form normalizes once per change and never per frame.
type distNorm struct {
	input, root string
	region      string
	err         error
	ok          bool
}

// distPlan is the read-only plan and its review state.
type distPlan struct {
	// seq drops the result of a plan that the user left.
	seq generation
	req learning.DistillRequest

	preview *learning.DistillPreview
	err     error
	// stale is the refusal of a run that did not start, for example a
	// changed input. The preview is then out of date, and only a new
	// plan helps.
	stale error
	// cwd is the process directory at the time of the plan. An agent
	// command without its own directory runs there.
	cwd string
	// preset is true when the command is a registered preset, not a
	// custom agent.argv.
	preset bool

	cursor int
	follow bool
	groups scrollState
	notes  scrollState
	// planFocus gives the keys to the Plan pane in the wide layout;
	// groupsOpen shows the Groups pane in the compact layout.
	planFocus  bool
	groupsOpen bool

	// The stale-pending cleanup of a plan without an eligible group.
	cleanAsk bool
	cleaning bool
	cleaned  bool
	removed  int
	cleanErr error
	cleanSc  scrollState
}

// distRun is the progress and the outcome of one run.
type distRun struct {
	started time.Time

	// outcomes are parallel to the planned groups. Each entry starts as
	// unattempted and changes with the finish event of its group.
	outcomes []distill.GroupOutcome
	finished int
	// sending is the group whose agent call runs; -1 when none.
	sending int
	// checked reports that the start checks passed: the first cleanup
	// or group event proves it.
	checked bool
	cleanup bool
	removed int
	stopped *distill.Event

	activity []string

	stopAsk  bool
	stopping bool
	// stopAt is the group that ran when the user asked to stop; -1 when
	// no group ran.
	stopAt int

	exec  *distill.Execution
	err   error
	ended time.Time
	// internal reports a run that ended on an internal error after its
	// work began. The engine sent no final outcome, so the result comes
	// from the events that arrived.
	internal bool
	// lost is the group whose agent call ran when the internal error
	// ended the run; -1 when none ran. Its result is not known.
	lost int

	cursor int
	follow bool
	// moved is true after the user moved the cursor. Until then the
	// cursor follows the group that runs, and after the run it goes to
	// the first group that did not complete. The result clears it, so
	// the compact result opens at its top and follows the cursor only
	// after a move.
	moved    bool
	groups   scrollState
	summary  scrollState
	sumFocus bool
	// detail shows the group pane in the compact layout.
	detail bool
}

// distRecord is one distillation of this session, from the scope draft
// to the result. It implements runRecord, runDraft, runGenerator and
// runOpener. Planning only reads; the run starts only on y from a
// ready preview.
type distRecord struct {
	st *state

	step distStep
	form distForm
	plan distPlan
	run  distRun

	// autoPlan makes the record plan once when the Run sub-view accepts
	// it. "Prepare remaining work" uses it: the new record plans the
	// same scope again from the current state, and y still starts it.
	autoPlan bool

	// warned is the time of the last refusal message of the record. A
	// plan or a start that succeeds soon after clears that message, so
	// the status row never says "did not start" beside a running run.
	warned time.Time
}

// distWarnTTL is how long the shell shows an attention message
// (app.go). A clear after that time would remove a newer message of
// another workflow.
const distWarnTTL = 15 * time.Second

// newDistillDraft returns the scope step of a new distillation. The Run
// sub-view calls it for the "distill" intent. prefill is the Proposals
// region filter, or "".
func newDistillDraft(st *state, prefill string) runRecord {
	d := newDistRecord(st)

	if region := strings.TrimSpace(prefill); region != "" {
		d.form.region.set(region)
		d.form.prefill = region
	}

	return d
}

// newDistRecord returns an empty scope draft with the input options of
// the form.
func newDistRecord(st *state) *distRecord {
	d := &distRecord{st: st}

	d.form.region.placeholder = "empty for everywhere, for example internal/api"
	d.form.custom.placeholder = "a whole number above 0"
	d.form.custom.limit = distCustomDigits
	d.form.custom.accept = func(r rune) bool { return r >= '0' && r <= '9' }
	d.run.sending, d.run.stopAt, d.run.lost = -1, -1, -1

	return d
}

// newDistillFrom returns a record with the scope of req that plans once
// the Run sub-view accepts it. It serves "prepare remaining work": the
// same inputs, a fresh read-only plan, and no start without y.
func newDistillFrom(st *state, req learning.DistillRequest) *distRecord {
	d := newDistRecord(st)
	d.form.region.set(req.RegionInput)

	switch {
	case req.AllGroups:
		d.form.limit = distLimitAll
		d.form.row = distRowAll
	case req.Limit > 0 && req.Limit != distill.DefaultGroupLimit:
		d.form.limit = distLimitCustom
		d.form.custom.set(strconv.Itoa(req.Limit))
		d.form.row = distRowCustom
	default:
		d.form.row = distRowDefault
	}

	d.autoPlan = true

	return d
}

// active reports work that runs now: the agent run, or the cleanup.
func (d *distRecord) active() bool {
	return d.step == distRunning || d.plan.cleaning
}

// draft reports a record that did no work yet. A cleanup-only record
// did durable work, so it is no draft after its cleanup.
func (d *distRecord) draft() bool {
	return d.step < distRunning && !d.plan.cleaning && !d.plan.cleaned
}

// title names the record by its scope.
func (d *distRecord) title() string {
	return "Distillation " + d.st.theme.Glyphs.Sep + " " + d.scopeLabel()
}

// generated returns the proposal IDs of a finished run, its label and
// its end. A draft, a running record, a refused start and a cleanup have
// no run result, so the time stays zero.
func (d *distRecord) generated() (ids []int64, label string, at time.Time) {
	if d.step != distResult || d.run.exec == nil || d.run.ended.IsZero() {
		return nil, "", time.Time{}
	}

	return d.newIDs(), "run " + d.run.ended.Format("15:04"), d.run.ended
}

// newIDs returns the IDs of the proposals that the run saved, in group
// order.
func (d *distRecord) newIDs() []int64 {
	var ids []int64

	for _, o := range d.outcomes() {
		for _, pr := range o.Proposals {
			ids = append(ids, pr.ID)
		}
	}

	return ids
}

// cleanupIDs returns the IDs of the stale pending proposals that the
// plan removes; nil without a plan.
func (d *distRecord) cleanupIDs() []int64 {
	if d == nil || d.plan.preview == nil || d.plan.preview.Plan == nil {
		return nil
	}

	return distProposalIDs(d.plan.preview.Plan.Cleanup)
}

// outcomes returns the group outcomes: the final ones after the run,
// else the live ones from the events.
func (d *distRecord) outcomes() []distill.GroupOutcome {
	if d.run.exec != nil && len(d.run.exec.Groups) > 0 {
		return d.run.exec.Groups
	}

	return d.run.outcomes
}

// capturing reports a text input or an overlay. The shell then sends
// every key to the record, so typed digits never switch the destination.
func (d *distRecord) capturing() bool {
	return d.form.editRegion || d.form.editCustom || d.run.stopAsk || d.plan.cleanAsk
}

// opened plans once for a record that "prepare remaining work" made.
func (d *distRecord) opened() tea.Cmd {
	if !d.autoPlan {
		return nil
	}

	d.autoPlan = false

	return d.startPlan()
}

// distPlannedMsg carries a plan result. seq drops the result of a plan
// that the user left.
type distPlannedMsg struct {
	rec     *distRecord
	seq     int
	preview *learning.DistillPreview
	err     error
	cwd     string
	preset  bool
}

// distEventMsg carries one typed progress event of a run. ch lets the
// receiver wait for the next message.
type distEventMsg struct {
	rec *distRecord
	ch  <-chan tea.Msg
	ev  distill.Event
	at  time.Time
}

// distDoneMsg carries the end of a run. exec is nil when the service
// refused the preview before any work, or when the run panicked.
// panicked tells the two apart, because a panic can come after paid
// agent calls and saved proposals.
type distDoneMsg struct {
	rec      *distRecord
	exec     *distill.Execution
	err      error
	panicked bool
	at       time.Time
}

// distCleanedMsg carries the result of the stale-pending cleanup.
type distCleanedMsg struct {
	rec     *distRecord
	removed int
	err     error
}

// update routes a message by type. Every result message names its
// record, so a result never lands in another record.
func (d *distRecord) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		cmd := d.key(msg)

		// A key can change the region input, so Update normalizes the
		// scope preview here, and View never does.
		_, _ = d.region()

		return cmd
	case loadedMsg:
		// A new index or evidence snapshot can change the root.
		_, _ = d.region()
	case distPlannedMsg:
		if msg.rec == d {
			return d.planned(msg)
		}
	case distEventMsg:
		// The wait continues for every event, so the worker never
		// blocks on a full queue.
		if msg.rec == d {
			d.event(msg)
		}

		return distWait(msg.ch)
	case distDoneMsg:
		if msg.rec == d {
			return d.done(msg)
		}
	case distCleanedMsg:
		if msg.rec == d {
			return d.cleanedUp(msg)
		}
	case settingsSavedMsg:
		d.settingsSaved(msg)
	}

	return nil
}

// request returns the scope of the form as a service request, or the
// reason that the form cannot plan.
func (d *distRecord) request() (req learning.DistillRequest, reason string) {
	req = learning.DistillRequest{RegionInput: strings.TrimSpace(d.form.region.value())}

	switch d.form.limit {
	case distLimitAll:
		req.AllGroups = true
	case distLimitCustom:
		n, err := strconv.Atoi(d.form.custom.value())
		if err != nil || n <= 0 {
			return req, "Enter a whole number above 0."
		}

		req.Limit = n
	default:
		req.Limit = distill.DefaultGroupLimit
	}

	if _, err := d.region(); err != nil {
		return req, "The region is outside the workspace."
	}

	if msg := d.agentBlock(); msg != "" {
		return req, msg
	}

	return req, ""
}

// warn shows a refusal in the status row and records its time.
func (d *distRecord) warn(text string) tea.Cmd {
	d.warned = time.Now()

	return toast(ToneAttention, text)
}

// settle clears a recent refusal message of the record after a plan or
// a start succeeds. The status row shows a message only while its text
// is not empty, so an empty message clears it. An older refusal is gone
// already, and a clear would remove another message.
func (d *distRecord) settle() tea.Cmd {
	if d.warned.IsZero() || time.Since(d.warned) > distWarnTTL {
		return nil
	}

	d.warned = time.Time{}

	return toast(ToneNeutral, "")
}

// startPlan starts the read-only plan of the form. The service reads
// the index and the configuration; it never runs the agent or writes.
func (d *distRecord) startPlan() tea.Cmd {
	req, reason := d.request()
	if reason != "" {
		// A plan request shows the rule of the custom limit.
		d.form.customChecked = true

		return d.warn(reason)
	}

	d.plan = distPlan{seq: d.plan.seq, req: req}
	d.form.note = ""
	d.step = distPlanning

	seq := d.plan.seq.start()

	var svc Learning = d.st.svc

	return func() tea.Msg {
		p, err := svc.PlanDistill(req)

		msg := distPlannedMsg{rec: d, seq: seq, preview: p, err: err}

		// The directory and the preset list are read here, in the
		// background, because View and Update must not do I/O.
		msg.cwd, _ = distGetwd()

		if p != nil {
			msg.preset = slices.Contains(integration.Builtin().InvocationIDs(), p.Invocation.Name)
		}

		return msg
	}
}

// planned shows a plan result. A region or limit error goes back to the
// form, because the form holds the value to fix.
func (d *distRecord) planned(msg distPlannedMsg) tea.Cmd {
	if !d.plan.seq.current(msg.seq) || d.step != distPlanning {
		return nil
	}

	switch {
	case errors.Is(msg.err, distill.ErrRegionEscapes):
		d.backToScope(ToneFailure, "The region is outside the workspace. Enter a path inside the repository.")

		return nil
	case errors.Is(msg.err, distill.ErrInvalidLimit):
		d.backToScope(ToneFailure, "The group limit is not valid: "+errText(msg.err))

		return nil
	}

	d.step = distReview
	d.plan.preview, d.plan.err = msg.preview, msg.err
	d.plan.cwd, d.plan.preset = msg.cwd, msg.preset
	d.plan.follow = true

	if msg.err == nil && msg.preview == nil {
		d.plan.err = errors.New("the planner returned no plan")
	}

	if d.plan.err != nil {
		return nil
	}

	return d.settle()
}

// backToScope returns to the form with a note. It drops the plan, and
// the cancel drops a late plan result.
func (d *distRecord) backToScope(tone Tone, note string) {
	d.plan = distPlan{seq: d.plan.seq}
	d.plan.seq.cancel()
	d.step = distScope
	d.form.note, d.form.noteTone = note, tone
}

// runnable reports a ready preview that y may start: groups to send, no
// blocker, and no refusal of an earlier start.
func (d *distRecord) runnable() bool {
	p := d.plan.preview

	return d.step == distReview && d.plan.err == nil && d.plan.stale == nil && p != nil && p.Plan != nil &&
		len(p.Plan.Groups) > 0 && len(p.Blockers) == 0
}

// start claims the mutation slot and runs exactly the reviewed preview.
// The work runs registered with the shell, so an exit stops it and
// waits for it.
func (d *distRecord) start() tea.Cmd {
	if !d.runnable() {
		return nil
	}

	if d.st.busy() {
		return d.warn("Another operation is running: " + d.st.mutation)
	}

	// The outcomes follow the preflight groups, which the engine keeps
	// parallel to the planned groups.
	p := d.plan.preview
	n := len(p.Plan.Preflight.Groups)

	ctx, cancel := d.st.runContext()

	note := runNote{
		verb: "Distilling", detail: d.scopeLabel(), total: n, stop: cancel,
		home: navigateMsg{dest: destLessons, intent: "run"},
	}
	if !d.st.beginRun("Distilling proposals", note) {
		cancel()

		return d.warn("Another operation is running: " + d.st.mutation)
	}

	d.st.lastRunKind = runDistill
	d.step = distRunning
	d.run = distRun{started: time.Now(), sending: -1, stopAt: -1, lost: -1, follow: true}
	d.run.outcomes = make([]distill.GroupOutcome, n)

	for i := range n {
		d.run.outcomes[i] = distill.GroupOutcome{Plan: p.Plan.Preflight.Groups[i]}
	}

	// The queue holds every event that must arrive: a start and a finish
	// per group, the cleanup, the stop and the end. So the worker never
	// blocks while nobody reads, as in the synchronous test harness.
	ch := make(chan tea.Msg, 2*n+4)

	var svc Learning = d.st.svc

	work := d.st.goWork(func() tea.Msg {
		defer cancel()

		distRunWork(ctx, svc, d, p, ch)

		return nil
	})

	return tea.Batch(work, distWait(ch), d.settle())
}

// distWait returns a command that delivers the next run message. It
// returns nil after the last one, so the chain ends by itself.
func distWait(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}

		return msg
	}
}

// distRunWork runs the preview and streams its events, then its end,
// through ch. It runs in a command goroutine, never in Update. A panic
// becomes a failed end, so the mutation slot always frees. The end
// marks the panic, because work can have started before it.
func distRunWork(ctx context.Context, svc Learning, rec *distRecord, p *learning.DistillPreview, ch chan tea.Msg) {
	defer close(ch)

	var (
		x        *distill.Execution
		err      error
		panicked bool
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				x, panicked = nil, true
				err = fmt.Errorf("panic: %v", r)
			}
		}()

		x, err = svc.RunDistill(ctx, p, distForward(rec, ch))
	}()

	ch <- distDoneMsg{rec: rec, exec: x, err: err, panicked: panicked, at: time.Now()}
}

// distForward adapts engine events to messages. A duplicate event is
// dropped when the queue is full: the group outcome lists every
// duplicate again, so nothing is lost. Every other event always arrives.
func distForward(rec *distRecord, ch chan tea.Msg) func(distill.Event) {
	return func(ev distill.Event) {
		msg := distEventMsg{rec: rec, ch: ch, ev: ev, at: time.Now()}

		if ev.Kind == distill.EventDuplicate {
			select {
			case ch <- msg:
			default:
			}

			return
		}

		ch <- msg
	}
}

// event records one progress event of the running record.
func (d *distRecord) event(msg distEventMsg) {
	if d.step != distRunning {
		return
	}

	ev := msg.ev
	r := &d.run

	switch ev.Kind {
	case distill.EventCleanupDone:
		r.checked, r.cleanup, r.removed = true, true, ev.Removed
		d.log(msg.at, "stale pending proposals removed: "+itoa(ev.Removed))
	case distill.EventGroupStarted:
		if !r.checked {
			r.checked = true
			d.log(msg.at, "plan checked: no change since the preview")
		}

		r.sending = ev.Index

		if !r.moved {
			r.cursor = ev.Index
		}
	case distill.EventGroupFinished:
		if ev.Outcome == nil || ev.Index < 0 || ev.Index >= len(r.outcomes) {
			return
		}

		r.outcomes[ev.Index] = *ev.Outcome
		r.finished++
		r.sending = -1
		done, total := r.finished, ev.Total

		// The event carries the frozen plan size, the known denominator
		// of the notice count.
		d.st.updateRun(func(n *runNote) {
			n.done = done

			if total > 0 {
				n.total = total
			}
		})
		d.log(msg.at, "group "+itoa(ev.Index+1)+" "+distOutcomeWords(*ev.Outcome))
	case distill.EventStopped:
		stopped := ev
		r.stopped = &stopped
		r.sending = -1
		d.log(msg.at, "run ended early: "+distStopWords(ev.Stop))
	}
}

// distFirstOpen returns the first group that did not complete, so the
// result opens on the group that needs attention; 0 when all completed.
func distFirstOpen(groups []distill.GroupOutcome) int {
	for i, g := range groups {
		if g.Status == distill.GroupFailed || g.Status == distill.GroupCancelled || g.Status == distill.GroupSkipped {
			return i
		}
	}

	return 0
}

// log appends one activity line with its time. The lines hold typed
// facts only, never agent output, and stay bounded.
func (d *distRecord) log(at time.Time, line string) {
	d.run.activity = append(d.run.activity, distClock(at)+"  "+line)

	if len(d.run.activity) > distActivityKeep {
		d.run.activity = d.run.activity[len(d.run.activity)-distActivityKeep:]
	}
}

// done records the end of a run, frees the slot, and reloads the
// sections that the run changed. A run that the service refused before
// any work returns to the review with the reason; only a new plan helps.
// A run that ended without a result after its work began shows a failed
// result from the events, because groups can be sent and saved already.
func (d *distRecord) done(msg distDoneMsg) tea.Cmd {
	if d.step != distRunning {
		return nil
	}

	reload := refresh(secLedger, secStats, secIndex, secLearning, secPins)

	// The shell clears its run record after this message, so the stop
	// request of the quit question is read now.
	d.run.stopping = d.stopping()
	d.run.stopAsk = false

	x := msg.exec

	switch {
	case x == nil && (msg.panicked || d.run.began()):
		x = d.salvage()
	case x == nil:
		d.step = distReview
		d.plan.stale = msg.err

		if d.plan.stale == nil {
			d.plan.stale = errors.New("the service returned no result")
		}

		return tea.Batch(mutationDone, reload, d.warn("The distillation did not start: "+distStaleText(d.plan.stale)+"."))
	}

	if d.run.internal && msg.err == nil {
		msg.err = errors.New("the service returned no result")
	}

	d.step = distResult
	d.run.exec, d.run.err, d.run.ended = x, msg.err, msg.at
	d.run.sending = -1
	d.run.follow = true

	if !d.run.moved {
		d.run.cursor = distFirstOpen(x.Groups)
	}

	// The compact result opens at its top, so the verdict and its lead
	// show first. The wide Groups pane follows the cursor again.
	d.run.moved = false
	d.run.groups = scrollState{}

	v := d.verdict()

	return tea.Batch(mutationDone, reload, toast(v.tone, v.toast))
}

// began reports that the run passed its start checks: an event of the
// cleanup, of a group, or of a stop arrived.
func (r *distRun) began() bool {
	return r.checked || r.cleanup || r.finished > 0 || r.stopped != nil
}

// salvage builds the result of a run that ended on an internal error
// from the events that arrived. The group that was sending keeps no
// status, and lost marks it, because its reply can be saved or not.
func (d *distRecord) salvage() *distill.Execution {
	r := &d.run
	r.internal, r.lost = true, r.sending

	x := &distill.Execution{Groups: slices.Clone(r.outcomes), Removed: r.removed}
	x.CleanupIDs = distProposalIDs(d.plan.preview.Plan.Cleanup)

	return x
}

// stop asks the running work to stop. The engine starts no new group
// and cancels the running agent call; the record shows "stopping" until
// the work ends.
func (d *distRecord) stop() tea.Cmd {
	d.run.stopAsk = false

	if d.step != distRunning || d.stopping() {
		return nil
	}

	if !d.st.requestStop() {
		return nil
	}

	d.run.stopping = true
	d.run.stopAt = d.run.sending

	detail := "waiting for the run to end"
	if k := d.run.stopAt; k >= 0 {
		detail = "waiting for the agent call of group " + itoa(k+1)
	}

	d.st.updateRun(func(n *runNote) { n.detail = detail })
	d.log(time.Now(), "stop requested: "+detail)

	return nil
}

// stopping reports a requested stop of the running work: from the stop
// question here, or from the quit question of the shell.
func (d *distRecord) stopping() bool {
	return d.run.stopping || (d.step == distRunning && d.st.run != nil && d.st.run.stopping)
}

// cleanup claims the mutation slot and removes the planned stale pending
// proposals of a cleanup-only plan. It runs no agent.
func (d *distRecord) cleanup() tea.Cmd {
	d.plan.cleanAsk = false

	p := d.plan.preview
	if p == nil || !p.CleanupOnly() || d.plan.cleaned || d.plan.cleaning {
		return nil
	}

	n := len(p.Plan.Cleanup)
	if !d.st.begin("Removing " + propPlural(n, "stale pending proposal", "stale pending proposals")) {
		return d.warn("Another operation is running: " + d.st.mutation)
	}

	d.plan.cleaning = true

	var svc Learning = d.st.svc

	return func() tea.Msg {
		removed, err := svc.CleanupPending(p)

		return distCleanedMsg{rec: d, removed: removed, err: err}
	}
}

// cleanedUp records the cleanup result, frees the slot and reloads the
// ledger.
func (d *distRecord) cleanedUp(msg distCleanedMsg) tea.Cmd {
	if !d.plan.cleaning {
		return nil
	}

	d.plan.cleaning, d.plan.cleaned = false, true
	d.plan.removed, d.plan.cleanErr = msg.removed, msg.err

	note := toast(ToneSuccess, "Removed "+propPlural(msg.removed, "stale pending proposal", "stale pending proposals")+".")
	if msg.err != nil {
		note = toast(ToneFailure, "The cleanup removed nothing: "+errText(msg.err))
	}

	return tea.Batch(mutationDone, refresh(secLedger, secStats, secLearning, secPins), note)
}

// settingsSaved drops a plan that a settings save made out of date. The
// preview binds the whole configuration file, so a run refuses it after
// any save. A drop at once avoids a y that can only fail. A running
// record keeps going: the engine checks the file before each group and
// stops with a conflict.
func (d *distRecord) settingsSaved(msg settingsSavedMsg) {
	if d.step != distPlanning && d.step != distReview {
		return
	}

	if d.plan.cleaning || d.plan.cleaned {
		return
	}

	d.backToScope(ToneAttention, "Settings changed ("+msg.group.String()+"), so the plan was dropped. "+
		"Press enter to plan again with the saved settings.")
}

// root returns the repository root for the region preview. The index
// records the root that planning uses; the workspace root is the last
// fallback. The service normalizes the region again when it plans.
func (d *distRecord) root() string {
	if sec := &d.st.learning; sec.loaded && sec.value.Probe.Root != "" {
		return sec.value.Probe.Root
	}

	if sec := &d.st.index; sec.loaded && sec.value.Probe.Root != "" {
		return sec.value.Probe.Root
	}

	return d.st.svc.Root()
}

// scopeLabel names the scope of the record: the planned region when a
// plan exists, else the typed region in its normalized form.
func (d *distRecord) scopeLabel() string {
	if p := d.plan.preview; p != nil && d.step >= distReview {
		return distScopeLabel(p.Region)
	}

	region, err := d.region()
	if err != nil {
		return oneLine(strings.TrimSpace(d.form.region.value()))
	}

	return distScopeLabel(region)
}

// region returns the normalized region of the input and a path outside
// the workspace as an error. The service normalizes the input again
// when it plans, so this is a preview of the scope, not its authority.
func (d *distRecord) region() (string, error) {
	n := &d.form.norm
	in, root := d.form.region.value(), d.root()

	if !n.ok || n.input != in || n.root != root {
		region, err := distill.NormalizeRegion(root, in)
		*n = distNorm{input: in, root: root, region: region, err: err, ok: true}
	}

	return n.region, n.err
}

// agentBlock returns why the configured agent blocks a plan, from the
// settings snapshot; "" when it does not block or is not known yet. The
// console is strict here: a paid run must not fall back to a default.
func (d *distRecord) agentBlock() string {
	snap := d.settingsSnap()
	if snap == nil {
		return ""
	}

	if err := snap.ReaderErrs[settings.ReaderAgent]; err != nil {
		return "The agent configuration is invalid: " + errText(err)
	}

	if snap.InvocationErr != nil {
		return "The agent configuration is invalid: " + errText(snap.InvocationErr)
	}

	return ""
}

// settingsSnap returns the settings snapshot after a good load, else nil.
func (d *distRecord) settingsSnap() *settings.Snapshot {
	sec := &d.st.settings
	if !sec.loaded || sec.err != nil {
		return nil
	}

	return sec.value
}

// distStaleText describes a refusal. A changed input wraps
// distill.ErrStale with its cause, and the sentence names only the
// cause, because "changed since the plan" and "changed since the
// preview" say the same thing twice.
func distStaleText(err error) string {
	text := errText(err)

	if errors.Is(err, distill.ErrStale) {
		if cause, ok := strings.CutPrefix(text, distill.ErrStale.Error()+": "); ok && cause != "" {
			return cause
		}
	}

	return text
}

// distScopeLabel names a normalized region: "everywhere" for the whole
// repository.
func distScopeLabel(region string) string {
	if region == "" {
		return "everywhere"
	}

	return oneLine(region)
}

// distScopeIn names a normalized region in a sentence, for example
// "in the whole repository".
func distScopeIn(region string) string {
	if region == "" {
		return "in the whole repository"
	}

	return "in " + oneLine(region)
}

// distRegionLabel names the region of one evidence group. A group
// without a region holds root-level evidence, so it reads "repo-wide",
// as a proposal without a region does.
func distRegionLabel(region string) string {
	if region == "" {
		return "repo-wide"
	}

	return oneLine(region)
}

// distLimitLabel names the group limit of a request.
func distLimitLabel(req learning.DistillRequest) string {
	switch {
	case req.AllGroups:
		return "all eligible groups"
	case req.Limit > 0:
		return propPlural(req.Limit, "group", "groups")
	default:
		return propPlural(distill.DefaultGroupLimit, "group", "groups")
	}
}

// distOutcomeWords describes a group outcome for the activity log and
// the group rows. The words never merge two outcomes (design map §7).
func distOutcomeWords(o distill.GroupOutcome) string {
	switch o.Status {
	case distill.GroupCompleted:
		switch {
		case len(o.Proposals) > 0:
			return "saved: " + propPlural(len(o.Proposals), "proposal", "proposals")
		case len(o.Duplicates) > 0:
			return "saved: duplicates only"
		default:
			return "saved: 0 proposals"
		}
	case distill.GroupFailed:
		return "failed: " + distFailWords(o.Failure)
	case distill.GroupCancelled:
		return "cancelled"
	case distill.GroupSkipped:
		return "skipped: recorded by another writer"
	default:
		return "not attempted"
	}
}

// distFailWords names a failure kind for a reader.
func distFailWords(k distill.FailureKind) string {
	switch k {
	case distill.FailAgent:
		return "agent error"
	case distill.FailTimeout:
		return "timeout"
	case distill.FailInvalidReply:
		return "invalid reply"
	case distill.FailStore:
		return "store error"
	default:
		return k.String()
	}
}

// distStopWords names why a run ended before its last group.
func distStopWords(r distill.StopReason) string {
	switch r {
	case distill.StopCancelled:
		return "stopped by you"
	case distill.StopConflict:
		return "an input changed"
	case distill.StopStoreError:
		return "store error"
	default:
		return r.String()
	}
}

// distTokens renders an estimated token count of chars characters with
// the engine's own estimate, so the console and the CLI agree.
func distTokens(chars int) string {
	return "~" + distill.Preflight{PromptChars: chars}.Tokens()
}
