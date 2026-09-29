package tui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/proposals"
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
	secCount
)

// allSections lists every source for a full view refresh.
var allSections = []sectionKind{secDoctor, secIndex, secIntegrations, secLedger, secStats}

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

	// gens drops out-of-order results: only the newest load of a
	// source may replace its snapshot.
	gens [secCount]int

	// mutation names the running mutation; "" when idle.
	mutation string

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

// loading reports any source that is loading.
func (s *state) loading() bool {
	return s.doctor.loading || s.index.loading || s.integrations.loading ||
		s.ledger.loading || s.stats.loading
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
