package tui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the distillation record of Lessons › Run. It keeps
// the record, the scope form and the messages. Its commands plan, run,
// stop and clean up. run_agent.go holds the steps, the Preview step
// state, the run state and the helpers that the record shares with the
// trigger extraction record. distill_keys.go handles the keys. The
// views live in distill_scope.go (scope form), distill_plan.go
// (Preview step) and distill_run.go (run progress and result).

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

// distForm is the scope draft. It holds typed text only; the form plans
// nothing before enter.
type distForm struct {
	region     ui.LineInput
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
	custom     ui.LineInput
	customWas  string
	editCustom bool

	// row is the cursor row: one of the distRow constants.
	row int
	// about shows the full explanation in the compact layout.
	about bool
	// sc scrolls the form in a short box; aboutSc scrolls the
	// explanation.
	sc      ui.ScrollState
	aboutSc ui.ScrollState

	// note is a message about the draft, for example a plan that a
	// settings save dropped. noteTone gives its glyph.
	note     string
	noteTone ui.Tone

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

// distCleanup is the stale-pending cleanup of a cleanup-only plan: a
// plan without an eligible group. It runs no agent. It belongs to its
// plan, so every new plan resets it.
type distCleanup struct {
	// ask shows the confirmation; running is true while the work runs.
	ask     bool
	running bool
	// done reports the result: removed rows, or the error err.
	done    bool
	removed int
	err     error
	scroll  ui.ScrollState
}

// distSwept is the cleanup that a run reports with its cleanup event.
// The engine removes the stale pending rows before the first group.
type distSwept struct {
	// done reports that the cleanup event arrived.
	done    bool
	removed int
}

// distRecord is one distillation of this session, from the scope draft
// to the result. It implements runRecord, runDraft, runGenerator and
// runOpener. Planning only reads; the run starts only on y from a
// ready preview.
type distRecord struct {
	st *state

	step runStep
	form distForm
	prev agentPreview[learning.DistillPreview]
	// req is the scope of the plan. "Prepare remaining work" plans it
	// again. dropPlan resets it with the plan.
	req learning.DistillRequest
	// clean is the cleanup of a cleanup-only plan. dropPlan resets it
	// with the plan, so a new plan never shows an old cleanup.
	clean distCleanup
	run   agentProgress[distill.Execution, distill.GroupOutcome]
	// swept is the cleanup that the run reports. start resets it with
	// the run.
	swept distSwept

	// autoPlan makes the record plan once when the Run sub-view accepts
	// it. "Prepare remaining work" uses it: the new record plans the
	// same scope again from the current state, and y still starts it.
	autoPlan bool

	// warned is the time of the last refusal message of the record. A
	// plan or a start that succeeds soon after clears that message, so
	// the status row never says "did not start" beside a running run.
	warned time.Time
}

// newDistillDraft returns the scope step of a new distillation. The Run
// sub-view calls it for the "distill" intent. prefill is the Proposals
// region filter, or "".
func newDistillDraft(st *state, prefill string) runRecord {
	d := newDistRecord(st)

	if region := strings.TrimSpace(prefill); region != "" {
		d.form.region.Set(region)
		d.form.prefill = region
	}

	return d
}

// newDistRecord returns an empty scope draft with the input options of
// the form.
func newDistRecord(st *state) *distRecord {
	d := &distRecord{st: st, run: idleAgentProgress[distill.Execution, distill.GroupOutcome]()}

	d.form.region.Placeholder = "empty for everywhere, for example internal/api"
	d.form.custom.Placeholder = "a whole number above 0"
	d.form.custom.Limit = distCustomDigits
	d.form.custom.Accept = func(r rune) bool { return r >= '0' && r <= '9' }

	return d
}

// newDistillFrom returns a record with the scope of req that plans once
// the Run sub-view accepts it. It serves "prepare remaining work": the
// same inputs, a fresh read-only plan, and no start without y.
func newDistillFrom(st *state, req learning.DistillRequest) *distRecord {
	d := newDistRecord(st)
	d.form.region.Set(req.RegionInput)

	switch {
	case req.AllGroups:
		d.form.limit = distLimitAll
		d.form.row = distRowAll
	case req.Limit > 0 && req.Limit != distill.DefaultGroupLimit:
		d.form.limit = distLimitCustom
		d.form.custom.Set(strconv.Itoa(req.Limit))
		d.form.row = distRowCustom
	default:
		d.form.row = distRowDefault
	}

	d.autoPlan = true

	return d
}

// active reports work that runs now: the agent run, or the cleanup.
func (d *distRecord) active() bool {
	return d.step == stepRunning || d.clean.running
}

// draft reports a record that did no work yet. A cleanup-only record
// did durable work, so it is no draft after its cleanup.
func (d *distRecord) draft() bool {
	return d.step < stepRunning && !d.clean.running && !d.clean.done
}

// title names the record by its scope.
func (d *distRecord) title() string {
	return "Distillation " + d.st.theme.Glyphs.Sep + " " + d.scopeLabel()
}

// generated returns the proposal IDs of a finished run, its label and
// its end. A draft, a running record, a refused start and a cleanup have
// no run result, so the time stays zero.
func (d *distRecord) generated() (ids []int64, label string, at time.Time) {
	if d.step != stepResult || d.run.exec == nil || d.run.ended.IsZero() {
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
	if d == nil || d.prev.preview == nil || d.prev.preview.Plan == nil {
		return nil
	}

	return distProposalIDs(d.prev.preview.Plan.Cleanup)
}

// outcomes returns the group outcomes: the final ones after the run,
// else the live ones from the events.
func (d *distRecord) outcomes() []distill.GroupOutcome {
	if d.run.exec != nil && len(d.run.exec.Groups) > 0 {
		return d.run.exec.Groups
	}

	return d.run.units
}

// capturing reports a text input or an overlay. The shell then sends
// every key to the record, so typed digits never switch the destination.
func (d *distRecord) capturing() bool {
	return d.form.editRegion || d.form.editCustom || d.run.stopAsk || d.clean.ask
}

// opened plans once for a record that "prepare remaining work" made.
func (d *distRecord) opened() tea.Cmd {
	if !d.autoPlan {
		return nil
	}

	d.autoPlan = false

	return d.startPlan()
}

// The messages of a distillation: a plan result, a progress event, and
// the end of a run. run_agent.go explains their fields.
type (
	distPlannedMsg = agentPlannedMsg[learning.DistillPreview]
	distEventMsg   = agentEventMsg[distill.Event]
	distDoneMsg    = agentDoneMsg[distill.Execution]
)

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

		return agentWait(msg.ch)
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
	req = learning.DistillRequest{RegionInput: strings.TrimSpace(d.form.region.Value())}

	switch d.form.limit {
	case distLimitAll:
		req.AllGroups = true
	case distLimitCustom:
		n, err := strconv.Atoi(d.form.custom.Value())
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

	return toast(ui.ToneAttention, text)
}

// settle clears a recent refusal message of the record after a plan or
// a start succeeds. The status row shows a message only while its text
// is not empty, so an empty message clears it. An older refusal is gone
// already, and a clear would remove another message.
func (d *distRecord) settle() tea.Cmd {
	if d.warned.IsZero() || time.Since(d.warned) > agentWarnTTL {
		return nil
	}

	d.warned = time.Time{}

	return toast(ui.ToneNeutral, "")
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

	d.dropPlan(req)
	d.form.note = ""
	d.step = stepPreparing

	seq := d.prev.seq.start()

	var svc Learning = d.st.svc

	return func() tea.Msg {
		p, err := svc.PlanDistill(req)

		msg := distPlannedMsg{rec: d, seq: seq, preview: p, err: err}

		// The directory and the preset list are read here, in the
		// background, because View and Update must not do I/O.
		msg.cwd, _ = distGetwd()

		if p != nil {
			msg.preset = isPreset(p.Invocation.Name)
		}

		return msg
	}
}

// planned shows a plan result. A region or limit error goes back to the
// form, because the form holds the value to fix.
func (d *distRecord) planned(msg distPlannedMsg) tea.Cmd {
	if !d.prev.seq.current(msg.seq) || d.step != stepPreparing {
		return nil
	}

	switch {
	case errors.Is(msg.err, distill.ErrRegionEscapes):
		d.backToScope(ui.ToneFailure, "The region is outside the workspace. Enter a path inside the repository.")

		return nil
	case errors.Is(msg.err, distill.ErrInvalidLimit):
		d.backToScope(ui.ToneFailure, "The group limit is not valid: "+ui.ErrText(msg.err))

		return nil
	}

	d.step = stepPreview
	d.prev.preview, d.prev.err = msg.preview, msg.err
	d.prev.cwd, d.prev.preset = msg.cwd, msg.preset
	d.prev.follow = true

	if msg.err == nil && msg.preview == nil {
		d.prev.err = errors.New("the planner returned no plan")
	}

	if d.prev.err != nil {
		return nil
	}

	return d.settle()
}

// dropPlan forgets the plan, its scope and its cleanup. It keeps the
// generation, so a late result of the dropped plan never lands. req is
// the scope of the next plan; zero when the record returns to the form.
func (d *distRecord) dropPlan(req learning.DistillRequest) {
	d.prev = agentPreview[learning.DistillPreview]{seq: d.prev.seq}
	d.req, d.clean = req, distCleanup{}
}

// backToScope returns to the form with a note. It drops the plan, and
// the cancel drops a late plan result.
func (d *distRecord) backToScope(tone ui.Tone, note string) {
	d.dropPlan(learning.DistillRequest{})
	d.prev.seq.cancel()
	d.step = stepScope
	d.form.note, d.form.noteTone = note, tone
}

// runnable reports a ready preview that y may start: groups to send, no
// blocker, and no refusal of an earlier start.
func (d *distRecord) runnable() bool {
	p := d.prev.preview

	return d.step == stepPreview && d.prev.err == nil && d.prev.stale == nil && p != nil && p.Plan != nil &&
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
	p := d.prev.preview
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

	units := make([]distill.GroupOutcome, n)
	for i := range n {
		units[i] = distill.GroupOutcome{Plan: p.Plan.Preflight.Groups[i]}
	}

	d.st.lastRunKind = runDistill
	d.step = stepRunning
	d.run = newAgentProgress[distill.Execution](units)
	d.swept = distSwept{}

	ch := agentQueue(n)

	var svc Learning = d.st.svc

	work := d.st.goWork(func() tea.Msg {
		defer cancel()

		distRunWork(ctx, svc, d, p, ch)

		return nil
	})

	return tea.Batch(work, agentWait(ch), d.settle())
}

// distRunWork runs the preview with svc and streams its events, then
// its end, through ch. agentWork turns a panic into a failed end.
func distRunWork(ctx context.Context, svc Learning, rec *distRecord, p *learning.DistillPreview, ch chan tea.Msg) {
	agentWork(rec, ch, func() (*distill.Execution, error) {
		return svc.RunDistill(ctx, p, distForward(rec, ch))
	})
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
	if d.step != stepRunning {
		return
	}

	ev := msg.ev
	r := &d.run

	switch ev.Kind {
	case distill.EventCleanupDone:
		r.checked, d.swept = true, distSwept{done: true, removed: ev.Removed}
		r.log(msg.at, "stale pending proposals removed: "+strconv.Itoa(ev.Removed))
	case distill.EventGroupStarted:
		if !r.checked {
			r.checked = true
			r.log(msg.at, "plan checked: no change since the preview")
		}

		r.sending = ev.Index

		if !r.moved {
			r.cursor = ev.Index
		}
	case distill.EventGroupFinished:
		if ev.Outcome == nil || ev.Index < 0 || ev.Index >= len(r.units) {
			return
		}

		r.units[ev.Index] = *ev.Outcome
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
		r.log(msg.at, "group "+strconv.Itoa(ev.Index+1)+" "+distOutcomeWords(*ev.Outcome))
	case distill.EventStopped:
		r.stopped, r.stopWhy = true, ev.Stop
		r.sending = -1
		r.log(msg.at, "run ended early: "+agentStopWords(ev.Stop))
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

// done records the end of a run, frees the slot, and reloads the
// sections that the run changed. A run that the service refused before
// any work returns to the review with the reason; only a new plan helps.
// A run that ended without a result after its work began shows a failed
// result from the events, because groups can be sent and saved already.
func (d *distRecord) done(msg distDoneMsg) tea.Cmd {
	if d.step != stepRunning {
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
		d.step = stepPreview
		d.prev.stale = msg.err

		if d.prev.stale == nil {
			d.prev.stale = errors.New("the service returned no result")
		}

		return tea.Batch(mutationDone, reload, d.warn("The distillation did not start: "+distStaleText(d.prev.stale)+"."))
	}

	if d.run.internal && msg.err == nil {
		msg.err = errors.New("the service returned no result")
	}

	d.step = stepResult
	d.run.exec, d.run.err, d.run.ended = x, msg.err, msg.at
	d.run.sending = -1
	d.run.follow = true

	if !d.run.moved {
		d.run.cursor = distFirstOpen(x.Groups)
	}

	// The compact result opens at its top, so the verdict and its lead
	// show first. The wide Groups pane follows the cursor again.
	d.run.moved = false
	d.run.list = ui.ScrollState{}

	v := d.verdict()

	return tea.Batch(mutationDone, reload, toast(v.tone, v.toast))
}

// salvage builds the result of a run that ended on an internal error
// from the events that arrived. The group that was sending keeps no
// status, and lost marks it, because its reply can be saved or not.
func (d *distRecord) salvage() *distill.Execution {
	r := &d.run
	r.internal, r.lost = true, r.sending

	x := &distill.Execution{Groups: slices.Clone(r.units), Removed: d.swept.removed}
	x.CleanupIDs = distProposalIDs(d.prev.preview.Plan.Cleanup)

	return x
}

// stop asks the running work to stop. The engine starts no new group
// and cancels the running agent call; the record shows "stopping" until
// the work ends.
func (d *distRecord) stop() tea.Cmd {
	d.run.stopAsk = false

	if d.step != stepRunning || d.stopping() {
		return nil
	}

	if !d.st.requestStop() {
		return nil
	}

	d.run.stopping = true
	d.run.stopAt = d.run.sending

	detail := "waiting for the run to end"
	if k := d.run.stopAt; k >= 0 {
		detail = "waiting for the agent call of group " + strconv.Itoa(k+1)
	}

	d.st.updateRun(func(n *runNote) { n.detail = detail })
	d.run.log(time.Now(), "stop requested: "+detail)

	return nil
}

// stopping reports a requested stop of the running work: from the stop
// question here, or from the quit question of the shell.
func (d *distRecord) stopping() bool {
	return d.run.stopping || (d.step == stepRunning && d.st.run != nil && d.st.run.stopping)
}

// cleanup claims the mutation slot and removes the planned stale pending
// proposals of a cleanup-only plan. It runs no agent.
func (d *distRecord) cleanup() tea.Cmd {
	d.clean.ask = false

	p := d.prev.preview
	if p == nil || !p.CleanupOnly() || d.clean.done || d.clean.running {
		return nil
	}

	n := len(p.Plan.Cleanup)
	if !d.st.begin("Removing " + propPlural(n, "stale pending proposal", "stale pending proposals")) {
		return d.warn("Another operation is running: " + d.st.mutation)
	}

	d.clean.running = true

	var svc Learning = d.st.svc

	return func() tea.Msg {
		removed, err := svc.CleanupPending(p)

		return distCleanedMsg{rec: d, removed: removed, err: err}
	}
}

// cleanedUp records the cleanup result, frees the slot and reloads the
// ledger.
func (d *distRecord) cleanedUp(msg distCleanedMsg) tea.Cmd {
	if !d.clean.running {
		return nil
	}

	d.clean.running, d.clean.done = false, true
	d.clean.removed, d.clean.err = msg.removed, msg.err

	note := toast(ui.ToneSuccess, "Removed "+propPlural(msg.removed, "stale pending proposal", "stale pending proposals")+".")
	if msg.err != nil {
		note = toast(ui.ToneFailure, "The cleanup removed nothing: "+ui.ErrText(msg.err))
	}

	return tea.Batch(mutationDone, refresh(secLedger, secStats, secLearning, secPins), note)
}

// settingsSaved drops a plan that a settings save made out of date. The
// preview binds the whole configuration file, so a run refuses it after
// any save. A drop at once avoids a y that can only fail. A running
// record keeps going: the engine checks the file before each group and
// stops with a conflict.
func (d *distRecord) settingsSaved(msg settingsSavedMsg) {
	if d.step != stepPreparing && d.step != stepPreview {
		return
	}

	if d.clean.running || d.clean.done {
		return
	}

	d.backToScope(ui.ToneAttention, "Settings changed ("+msg.group.String()+"), so the plan was dropped. "+
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
	if p := d.prev.preview; p != nil && d.step >= stepPreview {
		return distScopeLabel(p.Region)
	}

	region, err := d.region()
	if err != nil {
		return ui.OneLine(strings.TrimSpace(d.form.region.Value()))
	}

	return distScopeLabel(region)
}

// region returns the normalized region of the input and a path outside
// the workspace as an error. The service normalizes the input again
// when it plans, so this is a preview of the scope, not its authority.
func (d *distRecord) region() (string, error) {
	n := &d.form.norm
	in, root := d.form.region.Value(), d.root()

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
		return "The agent configuration is invalid: " + ui.ErrText(err)
	}

	if snap.InvocationErr != nil {
		return "The agent configuration is invalid: " + ui.ErrText(snap.InvocationErr)
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
	text := ui.ErrText(err)

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

	return ui.OneLine(region)
}

// distScopeIn names a normalized region in a sentence, for example
// "in the whole repository".
func distScopeIn(region string) string {
	if region == "" {
		return "in the whole repository"
	}

	return "in " + ui.OneLine(region)
}

// distRegionLabel names the region of one evidence group. A group
// without a region holds root-level evidence, so it reads "repo-wide",
// as a proposal without a region does.
func distRegionLabel(region string) string {
	if region == "" {
		return "repo-wide"
	}

	return ui.OneLine(region)
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
		return "failed: " + agentFailWords(o.Failure)
	case distill.GroupCancelled:
		return "cancelled"
	case distill.GroupSkipped:
		return "skipped: recorded by another writer"
	default:
		return "not attempted"
	}
}
