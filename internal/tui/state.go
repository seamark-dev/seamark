package tui

import (
	"context"
	"errors"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// sectionKind names one independently loaded data source. Each source
// loads, fails and goes stale on its own, so one failed reader never
// disables an unrelated view.
type sectionKind int

const (
	secDoctor sectionKind = iota
	secIndex
	secIntegrations
	secLedger
	secStats
	// secLearning is the stored evidence of the Evidence view.
	secLearning
	// secSettings is the focused settings of the Settings destination.
	secSettings
	secCount
)

// allSections lists every source for a full view refresh. Every loader
// only reads: none fetches, runs an agent, or writes.
var allSections = []sectionKind{
	secDoctor, secIndex, secIntegrations, secLedger, secStats, secLearning, secSettings,
}

// section is the last snapshot of one source. After a failed reload the
// previous value stays visible and is marked stale.
type section[T any] struct {
	value   T
	err     error
	at      time.Time
	loaded  bool // a value arrived at least once
	loading bool
}

// stale reports a failed reload over an earlier good snapshot.
func (s *section[T]) stale() bool {
	return s.loaded && s.err != nil
}

// set records a load result. A failure keeps the previous value.
func (s *section[T]) set(v T, err error, at time.Time) {
	s.loading = false
	s.err = err

	if err == nil {
		s.value, s.loaded, s.at = v, true, at
	}
}

// state is the data every screen shares: the services, the theme, the
// snapshots, and the single mutation slot.
type state struct {
	svc   Services
	theme *Theme

	width, height int

	doctor       section[*doctor.Report]
	index        section[IndexView]
	integrations section[[]integration.Inspection]
	ledger       section[*proposals.Ledger]
	stats        section[lessonstats.Snapshot]
	learning     section[learning.Snapshot]
	settings     section[*settings.Snapshot]

	// gens drops out-of-order results: only the newest load of a
	// source may replace its snapshot.
	gens [secCount]int

	// mutation names the running mutation; "" when idle.
	mutation string
	// run describes the running mutation when it can stop; nil for
	// work that cannot stop safely, and when idle. The notice row and
	// the quit question read it, because the user can hide the owning
	// screen.
	run *runNote
	// lastRunKind is the kind of the latest stoppable run of this
	// session; runNone before the first one. The screen that starts a
	// run sets it, and Lessons offers the Run sub-view from then on.
	lastRunKind runKind

	// ctx is the root context of the session. Stoppable work derives its
	// context from it, and Run cancels it at exit.
	ctx context.Context
	// work counts the stoppable workers, so Run can wait for them at
	// exit and no gh or agent child outlives the console.
	work workerSet

	// tick drives the activity glyph while work runs.
	tick int

	// copyText sends text to the system clipboard of the terminal. It is
	// nil when no terminal output exists, for example in tests.
	copyText func(text string) error
	// openURL opens a web page in the browser after an explicit key.
	openURL func(url string) error
}

// errNoClipboard reports a copy request without a terminal output.
var errNoClipboard = errors.New("no terminal output for the clipboard")

// copyToClipboard runs st.copyText, or reports that it is unavailable.
func (s *state) copyToClipboard(text string) error {
	if s.copyText == nil {
		return errNoClipboard
	}

	return s.copyText(text)
}

// loadedMsg carries one source's load result back to the event loop.
// Workers never touch the model; they return messages.
type loadedMsg struct {
	kind  sectionKind
	gen   int
	value any
	err   error
	at    time.Time
}

// navigateMsg asks the shell to open a destination. intent names a
// workflow to start there, for example "setup" or "setup:codex".
type navigateMsg struct {
	dest   destination
	intent string
}

// toastMsg shows a status line message.
type toastMsg struct {
	tone Tone
	text string
}

// refreshMsg asks the shell to reload sources, for example after a
// mutation changed them.
type refreshMsg struct {
	kinds []sectionKind
}

// mutationDoneMsg tells the shell that the running mutation ended.
// Screens send it from their own result handlers.
type mutationDoneMsg struct{}

// settingsSavedMsg tells every screen that one settings group was saved.
// The Settings destination broadcasts it after a successful save. A
// screen then drops each preview that the group binds, because the
// user reviewed it under the old values. For example, a distillation
// preview binds the inference group.
type settingsSavedMsg struct {
	group settings.Group
	// at is the end of the save. A snapshot older than at still shows
	// the old values.
	at time.Time
}

// tickMsg advances the activity glyph.
type tickMsg struct{}

// toast returns a command that shows a status line message.
func toast(tone Tone, text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{tone: tone, text: text} }
}

// refresh returns a command that reloads the named sources.
func refresh(kinds ...sectionKind) tea.Cmd {
	return func() tea.Msg { return refreshMsg{kinds: kinds} }
}

// navigate returns a command that opens a destination.
func navigate(dest destination, intent string) tea.Cmd {
	return func() tea.Msg { return navigateMsg{dest: dest, intent: intent} }
}

// mutationDone returns a command that frees the mutation slot.
func mutationDone() tea.Msg { return mutationDoneMsg{} }

// begin claims the single mutation slot. It returns false while another
// mutation runs, so a repeated key press cannot start a duplicate.
func (s *state) begin(name string) bool {
	if s.mutation != "" {
		return false
	}

	s.mutation = name

	return true
}

// busy reports a running mutation.
func (s *state) busy() bool {
	return s.mutation != ""
}

// runKind names the kind of a stoppable run.
type runKind int

const (
	runNone runKind = iota
	// runEvidence is an evidence refresh from local fixes or reviews.
	runEvidence
	// runDistill is a distillation run of the configured agent.
	runDistill
)

// runNote is the shared record of a running stoppable operation. The
// owning screen updates it; the shell renders it in the notice row and
// asks to stop it before an exit.
type runNote struct {
	// verb names the operation, for example "Distilling".
	verb string
	// detail is a short fact, for example the region. The notice row
	// sanitizes it.
	detail string
	// done and total count the finished units. total is 0 when the
	// denominator is unknown; the notice row then shows no count.
	done, total int
	// started is the start time for the elapsed time.
	started time.Time
	// stop cancels the context of the work.
	stop context.CancelFunc
	// stopping is true after a stop request, until the work ends.
	stopping bool
	// home opens the run detail, for example Lessons › Run.
	home navigateMsg
}

// beginRun claims the single mutation slot for work that can stop, and
// records the run. It returns false while another mutation runs, like
// begin. A zero start time becomes the current time.
func (s *state) beginRun(name string, n runNote) bool {
	if !s.begin(name) {
		return false
	}

	if n.started.IsZero() {
		n.started = time.Now()
	}

	s.run = &n

	return true
}

// updateRun changes the running record, for example after a progress
// event. It does nothing when no stoppable run exists, so a late event
// after the end cannot bring the record back.
func (s *state) updateRun(fn func(*runNote)) {
	if s.run != nil {
		fn(s.run)
	}
}

// stoppable reports running work that can stop.
func (s *state) stoppable() bool {
	return s.run != nil && s.run.stop != nil
}

// requestStop asks the running work to stop. It cancels the context of
// the work once and reports false when no stoppable work runs. The run
// stays in the slot until the work ends, because the work decides when
// it is safe to end.
func (s *state) requestStop() bool {
	if !s.stoppable() {
		return false
	}

	if !s.run.stopping {
		s.run.stopping = true
		s.run.stop()
	}

	return true
}

// runContext returns a context for new stoppable work and its cancel
// function. It derives from the root context, so an exit also stops
// the work. A state without a root context, for example in a test,
// uses the background context.
func (s *state) runContext() (context.Context, context.CancelFunc) {
	root := s.ctx
	if root == nil {
		root = context.Background()
	}

	return context.WithCancel(root)
}

// goWork returns a command that runs fn as registered stoppable work.
// Run waits for registered work at exit. After that wait began, the
// command does not run fn and returns nil, because no new external work
// may start during an exit.
func (s *state) goWork(fn func() tea.Msg) tea.Cmd {
	w := &s.work

	return func() tea.Msg {
		if !w.start() {
			return nil
		}

		defer w.done()

		return fn()
	}
}

// workerSet counts the running stoppable workers. Its zero value is
// ready to use.
type workerSet struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
}

// start registers one worker. It reports false after wait began. The
// lock orders every Add before the Wait, as sync.WaitGroup requires.
func (w *workerSet) start() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return false
	}

	w.wg.Add(1)

	return true
}

// done unregisters one worker.
func (w *workerSet) done() {
	w.wg.Done()
}

// wait refuses new workers and waits at most d for the registered ones.
// It reports whether all of them ended.
func (w *workerSet) wait(d time.Duration) bool {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()

	ended := make(chan struct{})

	go func() {
		w.wg.Wait()
		close(ended)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ended:
		return true
	case <-timer.C:
		// Work that ended at the same moment still counts as ended.
		select {
		case <-ended:
			return true
		default:
			return false
		}
	}
}

// loading reports any source that is loading.
func (s *state) loading() bool {
	return s.doctor.loading || s.index.loading || s.integrations.loading ||
		s.ledger.loading || s.stats.loading || s.learning.loading || s.settings.loading
}

// load starts background loads of the named sources.
func (s *state) load(kinds ...sectionKind) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(kinds))

	for _, k := range kinds {
		s.gens[k]++
		gen := s.gens[k]
		svc := s.svc

		switch k {
		case secDoctor:
			s.doctor.loading = true
			cmds = append(cmds, func() tea.Msg {
				return loadedMsg{kind: k, gen: gen, value: svc.Doctor(), at: time.Now()}
			})
		case secIndex:
			s.index.loading = true
			good := s.index.loaded && s.index.value.Status != nil
			cmds = append(cmds, func() tea.Msg {
				v := svc.Index()

				return loadedMsg{kind: k, gen: gen, value: v, err: indexReloadErr(v, good), at: time.Now()}
			})
		case secIntegrations:
			s.integrations.loading = true
			cmds = append(cmds, func() tea.Msg {
				return loadedMsg{kind: k, gen: gen, value: svc.Integrations(), at: time.Now()}
			})
		case secLedger:
			s.ledger.loading = true
			cmds = append(cmds, func() tea.Msg {
				l, err := svc.Ledger()

				return loadedMsg{kind: k, gen: gen, value: l, err: err, at: time.Now()}
			})
		case secStats:
			s.stats.loading = true
			good := s.stats.loaded && s.stats.value.LogErr == nil
			cmds = append(cmds, func() tea.Msg {
				v := svc.Stats()

				return loadedMsg{kind: k, gen: gen, value: v, err: statsReloadErr(v, good), at: time.Now()}
			})
		case secLearning:
			s.learning.loading = true
			good := s.learning.loaded && s.learning.value.CountsErr == nil
			cmds = append(cmds, func() tea.Msg {
				v := svc.Learning()

				return loadedMsg{kind: k, gen: gen, value: v, err: learningReloadErr(v, good), at: time.Now()}
			})
		case secSettings:
			s.settings.loading = true
			cmds = append(cmds, func() tea.Msg {
				v, err := svc.Settings()

				return loadedMsg{kind: k, gen: gen, value: v, err: err, at: time.Now()}
			})
		}
	}

	return tea.Batch(cmds...)
}

// Index and Stats put a read failure inside the value instead of
// returning an error. When a good snapshot exists, the two helpers below
// return that failure as the load error. Then section.set keeps the good
// snapshot and marks it stale, as the spec asks for an unreadable
// section. Without a good snapshot there is nothing to keep, so the new
// value with its failure replaces the old one. load reads the old
// snapshot before it starts the worker, because a worker must not read
// the model.

// indexReloadErr returns the semantic health read failure of a ready
// index when good reports an earlier snapshot with semantic health. A
// new probe state is new information, not a failed read, so it always
// replaces the snapshot.
func indexReloadErr(v IndexView, good bool) error {
	if !good || v.Probe.State != workspace.IndexReady {
		return nil
	}

	return v.StatusErr
}

// statsReloadErr returns the delivery log read failure when good
// reports an earlier snapshot that read the log.
func statsReloadErr(v lessonstats.Snapshot, good bool) error {
	if !good {
		return nil
	}

	return v.LogErr
}

// learningReloadErr returns the evidence count read failure of a ready
// index when good reports an earlier snapshot with readable counts. A
// new probe state is new information, as for the index, so it always
// replaces the snapshot.
func learningReloadErr(v learning.Snapshot, good bool) error {
	if !good || v.Probe.State != workspace.IndexReady {
		return nil
	}

	return v.CountsErr
}

// store records a load result unless a newer load of the same source
// started meanwhile.
func (s *state) store(msg loadedMsg) bool {
	if msg.gen != s.gens[msg.kind] {
		return false
	}

	switch msg.kind {
	case secDoctor:
		v, _ := msg.value.(*doctor.Report)
		s.doctor.set(v, msg.err, msg.at)
	case secIndex:
		v, _ := msg.value.(IndexView)
		s.index.set(v, msg.err, msg.at)
	case secIntegrations:
		v, _ := msg.value.([]integration.Inspection)
		s.integrations.set(v, msg.err, msg.at)
	case secLedger:
		v, _ := msg.value.(*proposals.Ledger)
		s.ledger.set(v, msg.err, msg.at)
	case secStats:
		v, _ := msg.value.(lessonstats.Snapshot)
		s.stats.set(v, msg.err, msg.at)
	case secLearning:
		v, _ := msg.value.(learning.Snapshot)
		s.learning.set(v, msg.err, msg.at)
	case secSettings:
		v, _ := msg.value.(*settings.Snapshot)
		s.settings.set(v, msg.err, msg.at)
	}

	return true
}

// pendingCount returns the pending proposal count, or -1 when unknown.
// An unknown count must never render as zero.
func (s *state) pendingCount() int {
	if !s.ledger.loaded || s.ledger.value == nil {
		return -1
	}

	return len(s.ledger.value.Pending)
}
