package tui

import (
	"slices"
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// lesSub is the visible sub-view of the Lessons destination. A new
// sub-view goes last, so the zero value keeps Proposals as the landing
// view. The order of the constants is not the tab order: subTabs sets
// that.
type lesSub int

const (
	lesProposals lesSub = iota
	lesStats
	lesEvidence
	lesRun
	// lesPins lists the installed pins of .seamark/lessons.yaml.
	lesPins
)

// lesKeys are the Lessons bindings. The package values stay enabled,
// because key.Matches ignores a disabled binding; keys() returns
// copies that carry the current availability for the footer.
var lesKeys = struct {
	Proposals, Pins, Stats, Evidence, Run key.Binding
	Clear, State, Region, Open            key.Binding
	Apply, Dismiss, Reconcile, Confirm    key.Binding
}{
	Proposals: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "proposals")),
	// m switches to Pins in every sub-view that does not capture, so no
	// sub-view may use the letter for its own keys.
	Pins:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "pins")),
	Stats:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stats")),
	Evidence:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "evidence")),
	Run:       key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "run")),
	Clear:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "clear selection")),
	State:     key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "state filter")),
	Region:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "region filter")),
	Open:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open PR")),
	Apply:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply")),
	Dismiss:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dismiss")),
	Reconcile: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "record as applied")),
	Confirm:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
}

// lesPane is one sub-view of Lessons. A sub-view with an overlay also
// implements modalScreen.
type lesPane interface {
	update(msg tea.Msg) tea.Cmd
	view(w, h int) string
	keys() []key.Binding
	capturing() bool
}

// lessons is the Lessons destination. It holds five sub-views: the
// proposal decisions, the installed pins, the delivery statistics, the
// evidence sources, and the runs of this session.
type lessons struct {
	st    *state
	sub   lesSub
	props *propView
	pins  *pinsView
	stats *statsView
	evid  *evidenceView
	runv  *runView

	// active reports that Lessons is the destination on screen. The
	// shell calls open when Lessons opens and leave when another
	// destination opens.
	active bool

	// from is the sub-view that asked for the current Run record, for
	// example Pins for an extraction review. esc on a review drops the
	// record, and the user then returns to that sub-view.
	from runOrigin
}

// runOrigin names the sub-view that opened a Run record. A zero value
// names no record.
type runOrigin struct {
	rec runRecord
	sub lesSub
}

func newLessons(st *state) *lessons {
	s := &lessons{
		st: st, props: newPropView(st), pins: newPinsView(st), stats: newStatsView(st),
		evid: newEvidenceView(st), runv: newRunView(st),
	}

	// The cursor note of a refresh concerns the visible list only.
	s.props.visible = func() bool { return s.active && s.sub == lesProposals }
	s.pins.visible = func() bool { return s.active && s.sub == lesPins }

	return s
}

func (s *lessons) title() string { return "Lessons" }

// tabCount is the pending proposal count beside the tab. An unknown
// count hides the number, because it must never render as zero.
func (s *lessons) tabCount() string {
	n := s.st.pendingCount()
	if n < 0 {
		return ""
	}

	return strconv.Itoa(n)
}

// pane returns the visible sub-view.
func (s *lessons) pane() lesPane {
	switch s.sub {
	case lesPins:
		return s.pins
	case lesStats:
		return s.stats
	case lesEvidence:
		return s.evid
	case lesRun:
		return s.runv
	default:
		return s.props
	}
}

// runShown reports whether Lessons offers the Run sub-view and its key u.
// That is true after the first stoppable run of the session, or while
// the Run sub-view holds its own work, for example a distillation
// draft. Before that, the sub-view would have nothing to show.
func (s *lessons) runShown() bool {
	return s.st.lastRunKind != runNone || s.runv.present()
}

// open selects the sub-view that an intent names. The "proposals"
// intent also shows pending proposals, because Overview links here
// from its pending count. "pins" opens the installed pins, "distill"
// the scope step of a new distillation, "run" the run detail, and
// "proposals:run" the proposals of the last run. Unknown intents keep
// the current view.
func (s *lessons) open(intent string) tea.Cmd {
	s.active = true

	switch intent {
	case "proposals":
		s.sub = lesProposals

		return s.props.showPending()
	case "pins":
		s.sub = lesPins
	case "stats":
		s.sub = lesStats
	case "evidence":
		s.sub = lesEvidence
	case "distill":
		// A new draft shows the Proposals region filter as a labelled
		// prefill, never as a hidden run scope.
		s.runv.region = s.props.region
		cmd := s.runv.prepare()
		if s.runShown() {
			s.sub = lesRun
		}

		return cmd
	case "run":
		if s.runShown() {
			s.sub = lesRun
			s.runv.showCurrent()
		}
	case "proposals:run":
		s.sub = lesProposals
		ids, label, at := s.runv.generated()

		return s.props.showRun(ids, label, at)
	}

	return nil
}

// leave implements leaveGuard. Lessons never keeps the user; it only
// records that it is no longer on screen, so a refresh does not report
// a cursor change in a list that nobody sees.
func (s *lessons) leave(destination, string) (tea.Cmd, bool) {
	s.active = false

	return nil, true
}

// showsIntent reports whether the visible sub-view is the one that
// intent opens. The shell asks it, because the notice row leaves out
// the hint to the run home on that sub-view.
func (s *lessons) showsIntent(intent string) bool {
	switch intent {
	case "proposals":
		return s.sub == lesProposals
	case "proposals:run":
		return s.sub == lesProposals && s.props.run != nil
	case "pins":
		return s.sub == lesPins
	case "stats":
		return s.sub == lesStats
	case "evidence":
		return s.sub == lesEvidence
	case "run", "distill":
		return s.sub == lesRun
	}

	return false
}

// update routes a message. Every non-key message reaches every
// sub-view, so background results land even in a hidden one. Key
// messages reach the visible sub-view only, after the sub-view switch
// keys, which act while the visible sub-view does not capture. A
// maintenance preview opens over Pins, and a link to a pin shows its
// entry, so Pins shows for both; the Run sub-view opens an extraction
// record itself (openExtractMsg). A dropped draft returns the view to
// the sub-view that opened it (runDropped), so the visible sub-view
// always has its tab.
func (s *lessons) update(msg tea.Msg) tea.Cmd {
	km, isKey := msg.(tea.KeyMsg)
	if !isKey {
		// The run records change inside the fan-out, so the checks of
		// the Run sub-view compare the state before and after it.
		cur, sub := s.runv.cur, s.sub
		dropped := false

		switch m := msg.(type) {
		case openMaintMsg, focusPinMsg:
			s.sub = lesPins
		case closeRunMsg:
			dropped = m.rec != nil && m.rec == cur && isDraft(m.rec)
		}

		cmd := tea.Batch(s.props.update(msg), s.pins.update(msg), s.stats.update(msg), s.evid.update(msg),
			s.runv.update(msg))

		switch m := msg.(type) {
		case openRunMsg, openExtractMsg:
			if s.runv.cur != cur {
				s.from = runOrigin{rec: s.runv.cur, sub: s.originOf(sub)}
			}
		case closeRunMsg:
			if dropped {
				s.runDropped(m.rec)
			}
		}

		return cmd
	}

	pane := s.pane()

	if !pane.capturing() {
		switch {
		case key.Matches(km, lesKeys.Proposals):
			s.sub = lesProposals

			return nil
		case key.Matches(km, lesKeys.Pins):
			s.sub = lesPins

			return nil
		case key.Matches(km, lesKeys.Stats):
			s.sub = lesStats

			return nil
		case key.Matches(km, lesKeys.Evidence):
			s.sub = lesEvidence

			return nil
		case key.Matches(km, lesKeys.Run) && s.runShown():
			// u keeps the record on view, so a look at the proposals of
			// an earlier run returns to that run.
			s.sub = lesRun

			return nil
		}
	}

	return pane.update(msg)
}

// originOf returns the sub-view to return to when the record that sub
// asked for closes as a draft. A request from the Run sub-view, for
// example a review of the remaining targets, keeps the origin of the
// record before it.
func (s *lessons) originOf(sub lesSub) lesSub {
	switch {
	case sub != lesRun:
		return sub
	case s.from.rec != nil:
		return s.from.sub
	default:
		return lesProposals
	}
}

// runDropped keeps a tab under the visible sub-view after the Run
// sub-view dropped the draft rec. A dropped draft returns to the
// sub-view that asked for it, for example Pins for an extraction review.
// Any other dropped draft that leaves no record hides the Run tab, so
// the view moves to Proposals instead of a sub-view that the tab line
// does not list. A distillation scope also navigates to Evidence itself.
func (s *lessons) runDropped(rec runRecord) {
	from := s.from
	if from.rec == rec {
		s.from = runOrigin{}
	}

	if s.sub != lesRun {
		return
	}

	switch {
	case from.rec == rec:
		s.sub = from.sub
	case !s.runShown():
		s.sub = lesProposals
	}
}

// view renders the sub-view line and the visible sub-view under it.
func (s *lessons) view(w, h int) string {
	return s.subTabs(w) + "\n" + s.pane().view(w, h-1)
}

// modal returns the open overlay of the visible sub-view. The shell
// dims the frame under it, so a preview reads as a separate layer.
func (s *lessons) modal(w, h int) (string, bool) {
	if ms, ok := s.pane().(modalScreen); ok {
		return ms.modal(w, h)
	}

	return "", false
}

// keys returns the visible sub-view's keys and every switch key that
// works there. The help lists these keys, and "? help" must lead to
// every key. The switch keys are hidden while the sub-view captures
// input. On Stats the Proposals key comes first, as in the 1a footer.
// The other switch keys go last, in tab order, because a narrow footer
// drops hints from the end, so the compact footers keep their 1a hints.
// Evidence and Run name their own first switch keys, because their
// footers differ by state.
func (s *lessons) keys() []key.Binding {
	if s.pane().capturing() {
		return s.pane().keys()
	}

	// u works only while Lessons offers the Run sub-view.
	var run []key.Binding
	if s.runShown() {
		run = []key.Binding{lesKeys.Run}
	}

	switch s.sub {
	case lesPins:
		return slices.Concat(s.pins.keys(), []key.Binding{lesKeys.Proposals, lesKeys.Stats, lesKeys.Evidence}, run)
	case lesStats:
		ks := append([]key.Binding{lesKeys.Proposals}, s.stats.keys()...)

		return append(append(ks, lesKeys.Pins, lesKeys.Evidence), run...)
	case lesEvidence:
		return append(s.evid.keys(), run...)
	case lesRun:
		return s.runv.keys()
	}

	return append(append(s.props.keys(), lesKeys.Pins, lesKeys.Stats, lesKeys.Evidence), run...)
}

// helpKeys returns the keys that the help lists for Lessons: the keys of
// the visible sub-view, then every switch key in tab order. The footer
// leaves out the switch key of the visible sub-view, because it changes
// nothing there. The help lists it anyway, so it names the key of every
// sub-view (user report of 2026-10-04: in Pins the help had no m). Pins
// and Proposals also list their unavailable keys, marked n/a.
func (s *lessons) helpKeys() []key.Binding {
	if s.pane().capturing() {
		return s.keys()
	}

	switches := []key.Binding{lesKeys.Proposals, lesKeys.Pins, lesKeys.Stats, lesKeys.Evidence}
	if s.runShown() {
		switches = append(switches, lesKeys.Run)
	}

	own := s.keys()

	switch s.sub {
	case lesPins:
		own = s.pins.helpKeys()
	case lesProposals:
		own = s.props.helpKeys()
	}

	// isSwitch matches the package bindings by value, because keys()
	// appends them without a change.
	isSwitch := func(b key.Binding) bool {
		return slices.ContainsFunc(switches, func(sw key.Binding) bool {
			return sw.Help() == b.Help() && slices.Equal(sw.Keys(), b.Keys())
		})
	}

	return append(slices.DeleteFunc(slices.Clone(own), isSwitch), switches...)
}

func (s *lessons) capturing() bool {
	return s.pane().capturing()
}

// panes returns the five sub-views. The draft and help methods ask each
// of them, because each sub-view keeps its own drafts.
func (s *lessons) panes() []lesPane {
	return []lesPane{s.props, s.pins, s.stats, s.evid, s.runv}
}

// drafts implements draftHolder for the sub-views. A sub-view that
// holds drafts implements draftHolder itself, and Lessons collects its
// files, so the quit question names them.
func (s *lessons) drafts() []string {
	var files []string

	for _, p := range s.panes() {
		if h, ok := p.(draftHolder); ok {
			files = append(files, h.drafts()...)
		}
	}

	return files
}

// discardDrafts implements draftHolder: it drops the drafts of every
// sub-view that holds drafts.
func (s *lessons) discardDrafts() {
	for _, p := range s.panes() {
		if h, ok := p.(draftHolder); ok {
			h.discardDrafts()
		}
	}
}

// contextHelp implements contextHelper for the visible sub-view. A
// sub-view with a form, a review or a sample implements contextHelper
// itself; the others have no help of their own.
func (s *lessons) contextHelp() (helpPage, bool) {
	if h, ok := s.pane().(contextHelper); ok {
		return h.contextHelp()
	}

	return helpPage{}, false
}
