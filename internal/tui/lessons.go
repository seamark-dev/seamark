package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
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

	return itoa(n)
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

// propState is the proposal state filter.
type propState int

const (
	propPending propState = iota
	propApplied
	propDismissed
	// propSuperseded is the read-only history of proposals whose pins a
	// prune removed. f reaches it last, so the decision states come
	// first.
	propSuperseded
	propStateCount
)

// label returns the state name that the filter bar shows.
func (f propState) label() string {
	switch f {
	case propApplied:
		return "applied"
	case propDismissed:
		return "dismissed"
	case propSuperseded:
		return "superseded"
	default:
		return "pending"
	}
}

// propStage is the step of a decision modal.
type propStage int

const (
	// propStagePreparing waits for the preview.
	propStagePreparing propStage = iota + 1
	// propStagePreview shows the preview or its failure.
	propStagePreview
	// propStageCommitting waits for the commit. It cannot stop.
	propStageCommitting
	// propStageResult shows the commit outcome.
	propStageResult
)

// lesScroll is the vertical position of one scrollable text. The view
// records the page size and the last offset, because the key handler
// cannot know how tall the rendered text is.
type lesScroll struct {
	off  int
	page int
	last int
}

// by moves the position by delta lines inside the recorded bounds.
func (sc *lesScroll) by(delta int) {
	sc.off = max(0, min(sc.off+delta, sc.last))
}

// key applies a movement key and reports whether the key was one.
func (sc *lesScroll) key(msg tea.KeyMsg) bool {
	page := max(1, sc.page)

	switch {
	case key.Matches(msg, nkeys.Up):
		sc.by(-1)
	case key.Matches(msg, nkeys.Down):
		sc.by(1)
	case key.Matches(msg, nkeys.PageUp):
		sc.by(-page)
	case key.Matches(msg, nkeys.PageDown):
		sc.by(page)
	case key.Matches(msg, nkeys.Top):
		sc.off = 0
	case key.Matches(msg, nkeys.Bottom):
		sc.off = sc.last
	default:
		return false
	}

	return true
}

// scrolls reports text that is taller than its window.
func (sc *lesScroll) scrolls() bool {
	return sc.last > 0
}

// propDecision is one decision in progress: the request, the preview,
// and the commit outcome. It lives only while its modal is open.
type propDecision struct {
	stage  propStage
	action proposals.Action
	ids    []int64
	// implicit is true when the cursor row stands in for an empty
	// selection. The preview says so, because the user did not mark it.
	implicit bool

	pv  *proposals.Preview
	err error // the preview failure, or the commit refusal

	committed bool
	res       proposals.Result

	scroll lesScroll
}

// propPreviewMsg carries a preview result. seq drops the result of a
// preview that the user cancelled.
type propPreviewMsg struct {
	seq int
	pv  *proposals.Preview
	err error
}

// propCommitMsg carries a commit result.
type propCommitMsg struct {
	pv  *proposals.Preview
	res proposals.Result
	err error
}

// propView is the Lessons → Proposals sub-view: filters, the list, the
// detail pane, and the decision modals.
type propView struct {
	st *state

	filter  propState
	region  string
	input   propInput
	editing bool

	list     listState
	cursorID int64
	// focusDetail is the focused pane in the wide layout and the shown
	// pane in the narrow layout. Resize keeps it.
	focusDetail bool
	detail      lesScroll
	detailID    int64

	// selected holds the multi-selection by proposal ID. IDs survive a
	// refresh only while the proposal stays pending and visible.
	selected map[int64]bool
	// acted holds IDs that the last commit moved. The next refresh
	// does not report them as lost, because the user decided them.
	acted map[int64]bool
	// pinned holds pending IDs whose pins exist in the pin file, as the
	// last apply preview or a partial apply reported. A newer Pins
	// inspection removes a hint that it contradicts (dropPinHints). c
	// works only for these IDs and for the pending matches of the Pins
	// inspection (hasPin), because c records existing pins only.
	pinned map[int64]bool
	// run holds the proposals of one run of this session for the run
	// filter; nil without a run filter.
	run *propRunFilter
	// visible reports that the list is on screen. A refresh reports a
	// lost cursor row only then, because the note names the list that
	// the user sees. nil means always visible.
	visible func() bool

	dec *propDecision
	seq generation

	// listH comes from the last render, so page keys move by the rows
	// that the user sees.
	listH int
}

func newPropView(st *state) *propView {
	return &propView{
		st:       st,
		selected: map[int64]bool{},
		acted:    map[int64]bool{},
		pinned:   map[int64]bool{},
	}
}

// capturing reports the region input or an open decision modal.
func (p *propView) capturing() bool {
	return p.editing || p.dec != nil
}

// ledger returns the current ledger snapshot, or nil.
func (p *propView) ledger() *proposals.Ledger {
	if !p.st.ledger.loaded {
		return nil
	}

	return p.st.ledger.value
}

// stateRows returns the proposals of the current state filter.
func (p *propView) stateRows() []domain.Proposal {
	l := p.ledger()
	if l == nil {
		return nil
	}

	return propStateRows(l, p.filter)
}

// propStateRows returns the proposals of one state of the ledger.
func propStateRows(l *proposals.Ledger, f propState) []domain.Proposal {
	switch f {
	case propApplied:
		return l.Applied
	case propDismissed:
		return l.Dismissed
	case propSuperseded:
		return l.Superseded
	default:
		return l.Pending
	}
}

// rows returns the visible proposals in ascending ID order. The ledger
// is already ordered; the sort guards the deterministic order that the
// spec requires.
func (p *propView) rows() []domain.Proposal {
	var out []domain.Proposal

	for _, pr := range p.stateRows() {
		if proposals.MatchesRegion(pr, p.region) && p.inRun(pr.ID) {
			out = append(out, pr)
		}
	}

	if !slices.IsSortedFunc(out, propByID) {
		slices.SortFunc(out, propByID)
	}

	return out
}

// propByID orders proposals by ascending ID.
func propByID(a, b domain.Proposal) int {
	switch {
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	default:
		return 0
	}
}

// current returns the proposal under the cursor.
func (p *propView) current(rows []domain.Proposal) (domain.Proposal, bool) {
	if len(rows) == 0 {
		return domain.Proposal{}, false
	}

	p.list.clamp(len(rows), 0)

	return rows[p.list.cursor], true
}

// syncCursorID records the proposal under the cursor, so a refresh can
// find the same proposal again by identity. An empty list records no
// proposal. Else a refresh would report the last lost row again and
// again, because it never comes back.
func (p *propView) syncCursorID(rows []domain.Proposal) {
	p.cursorID = 0

	if cur, ok := p.current(rows); ok {
		p.cursorID = cur.ID
	}
}

// relocate puts the cursor on the recorded proposal when it is visible.
// Else a refresh keeps the cursor position, which shows the neighbor of
// the lost row, and a filter change starts at the first row. It
// reports whether the proposal was found.
func (p *propView) relocate(rows []domain.Proposal, keepPosition bool) bool {
	for i, pr := range rows {
		if pr.ID == p.cursorID {
			p.list.cursor = i

			return true
		}
	}

	if !keepPosition {
		p.list.cursor, p.list.offset = 0, 0
	}

	p.syncCursorID(rows)

	return false
}

// selectedIDs returns the selection in ascending order.
func (p *propView) selectedIDs() []int64 {
	ids := make([]int64, 0, len(p.selected))
	for id := range p.selected {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids
}

// targets returns the IDs a decision acts on: the selection, or the
// pending cursor row when nothing is selected. implicit reports the
// second case.
func (p *propView) targets() (ids []int64, implicit bool) {
	if p.filter != propPending {
		return nil, false
	}

	if len(p.selected) > 0 {
		return p.selectedIDs(), false
	}

	if cur, ok := p.current(p.rows()); ok {
		return []int64{cur.ID}, true
	}

	return nil, false
}

// showPending switches to the pending state filter, for the Overview
// link. A switch is a filter change, so it clears the selection too.
// The link means every pending proposal, so it also ends a run filter.
func (p *propView) showPending() tea.Cmd {
	if (p.filter == propPending && p.run == nil) || p.dec != nil || p.editing {
		return nil
	}

	p.filter = propPending
	p.run = nil

	return p.filterChanged()
}

func (p *propView) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case p.editing:
			return p.editKey(msg)
		case p.dec != nil:
			return p.decisionKey(msg)
		default:
			return p.browseKey(msg)
		}
	case loadedMsg:
		// A failed reload keeps the previous snapshot, so only a new
		// snapshot needs reconciliation.
		if msg.err != nil {
			return nil
		}

		switch msg.kind {
		case secLedger:
			return p.reconcile()
		case secPins:
			p.dropPinHints()
		}
	case propPreviewMsg:
		return p.previewed(msg)
	case propCommitMsg:
		return p.committedMsg(msg)
	case focusProposalMsg:
		return p.focus(msg.id)
	case settingsSavedMsg:
		return p.settingsSaved(msg)
	case distEventMsg:
		// The cleanup of a run removes the stale pending proposals that
		// the plan named, and the user confirmed the plan.
		if msg.ev.Kind == distill.EventCleanupDone {
			p.markActed(msg.rec.cleanupIDs())
		}
	case distCleanedMsg:
		p.markActed(msg.rec.cleanupIDs())
	}

	return nil
}

// browseKey handles keys while no modal and no input has focus.
func (p *propView) browseKey(msg tea.KeyMsg) tea.Cmd {
	rows := p.rows()

	switch {
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		if p.st.wide() && len(rows) > 0 {
			p.focusDetail = !p.focusDetail
		}
	case key.Matches(msg, nkeys.Enter):
		if len(rows) > 0 {
			p.focusDetail = true
		}
	case key.Matches(msg, nkeys.Back):
		// esc first leaves the detail, then ends a run filter.
		if !p.focusDetail && p.run != nil {
			return p.clearRun()
		}

		p.focusDetail = false
	case key.Matches(msg, nkeys.Toggle):
		p.toggle(rows)
	case key.Matches(msg, lesKeys.Clear):
		if len(p.selected) > 0 {
			p.selected = map[int64]bool{}

			return toast(ToneNeutral, "Selection cleared.")
		}
	case key.Matches(msg, lesKeys.State):
		// Every other filter ends the run filter, so the list never
		// mixes a run with another narrowing unseen.
		p.run = nil
		p.filter = (p.filter + 1) % propStateCount

		return p.filterChanged()
	case key.Matches(msg, lesKeys.Region):
		cmd := p.clearRun()
		p.editing = true
		p.input.set(p.region)

		return cmd
	case key.Matches(msg, lesKeys.Apply):
		return p.startDecision(proposals.ActionApply)
	case key.Matches(msg, lesKeys.Dismiss):
		return p.startDecision(proposals.ActionDismiss)
	case key.Matches(msg, lesKeys.Reconcile):
		return p.startRecord()
	case key.Matches(msg, extractKey):
		return p.startExtract()
	case key.Matches(msg, lesKeys.Open):
		return p.openLink(rows)
	case p.showsDetail(rows) && p.detailKey(msg):
	default:
		p.moveCursor(msg, rows)
	}

	return nil
}

// showsDetail reports a visible detail pane: beside the list in the
// wide layout, or alone in the narrow layout.
func (p *propView) showsDetail(rows []domain.Proposal) bool {
	return len(rows) > 0 && (p.st.wide() || p.focusDetail)
}

// detailKey scrolls the detail pane. The page keys always scroll it in
// the wide layout, as the "pgdn" hint under the pane says; the other
// movement keys scroll it only while it has focus. It reports whether
// the key scrolled.
func (p *propView) detailKey(msg tea.KeyMsg) bool {
	paging := key.Matches(msg, nkeys.PageUp, nkeys.PageDown)
	if !p.focusDetail && (!p.st.wide() || !paging) {
		return false
	}

	return p.detail.key(msg)
}

// moveCursor moves the list cursor and records the proposal under it.
func (p *propView) moveCursor(msg tea.KeyMsg, rows []domain.Proposal) {
	page := max(1, p.listH-1)

	switch {
	case key.Matches(msg, nkeys.Up):
		p.list.move(-1, len(rows))
	case key.Matches(msg, nkeys.Down):
		p.list.move(1, len(rows))
	case key.Matches(msg, nkeys.PageUp):
		p.list.move(-page, len(rows))
	case key.Matches(msg, nkeys.PageDown):
		p.list.move(page, len(rows))
	case key.Matches(msg, nkeys.Top):
		p.list.move(-len(rows), len(rows))
	case key.Matches(msg, nkeys.Bottom):
		p.list.move(len(rows), len(rows))
	default:
		return
	}

	p.syncCursorID(rows)
}

// toggle marks or unmarks the cursor row. Only pending rows can be
// selected: applied and dismissed proposals are read-only.
func (p *propView) toggle(rows []domain.Proposal) {
	cur, ok := p.current(rows)
	if !ok || p.filter != propPending {
		return
	}

	if p.selected[cur.ID] {
		delete(p.selected, cur.ID)
	} else {
		p.selected[cur.ID] = true
	}
}

// filterChanged keeps the cursor on the same proposal when it stays
// visible, and clears the selection. A hidden selected proposal could
// otherwise be mutated by accident, so the clearing is announced.
func (p *propView) filterChanged() tea.Cmd {
	p.relocate(p.rows(), false)

	if len(p.selected) == 0 {
		return nil
	}

	p.selected = map[int64]bool{}

	return toast(ToneAttention, "Selection cleared: filter changed")
}

// propLink is the first link of a proposal's cited evidence. pr is
// true when the finding names a pull request, so the key can say
// "open PR" and not "open link" for a commit.
type propLink struct {
	url string
	pr  bool
}

// link returns the first available link of the proposal's findings in
// citation order. Unavailable findings have no link.
func (p *propView) link(pr domain.Proposal) (propLink, bool) {
	l := p.ledger()
	if l == nil || l.EvidenceErr != nil {
		return propLink{}, false
	}

	for _, id := range pr.Members {
		if f, ok := l.Evidence[id]; ok && strings.TrimSpace(f.URL) != "" {
			return propLink{url: strings.TrimSpace(f.URL), pr: f.PR > 0}, true
		}
	}

	return propLink{}, false
}

// openLink opens the first evidence link of the cursor row in the
// browser. It runs only on the explicit key, and the opener runs in a
// command, so a slow opener never blocks the view. The toast reports
// the result.
func (p *propView) openLink(rows []domain.Proposal) tea.Cmd {
	cur, ok := p.current(rows)
	if !ok {
		return nil
	}

	link, ok := p.link(cur)
	if !ok {
		return toast(ToneNeutral, "No cited finding of "+propID(cur.ID)+" has a link.")
	}

	open := p.st.openURL

	return func() tea.Msg {
		if open == nil {
			return toastMsg{tone: ToneAttention, text: "This session cannot open a browser. The link is " + oneLine(link.url)}
		}

		if err := open(link.url); err != nil {
			return toastMsg{tone: ToneFailure, text: "Could not open " + oneLine(link.url) + ": " + errText(err)}
		}

		return toastMsg{tone: ToneNeutral, text: "Opened " + oneLine(link.url) + " in the browser."}
	}
}

// editKey handles the focused region input. Enter applies the filter,
// esc restores the previous one.
func (p *propView) editKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter):
		p.editing = false

		region := propNormalizeRegion(p.input.value())
		if region == p.region {
			return nil
		}

		p.region = region

		return p.filterChanged()
	case key.Matches(msg, nkeys.Back):
		p.editing = false

		return nil
	}

	p.input.key(msg)

	return nil
}

// propInputLimit bounds the region filter length. A path scope longer
// than this is a paste accident, not a filter.
const propInputLimit = 256

// propInput is a minimal single-line text input for the region filter.
// bubbles/textinput needs a clipboard module that the build does not
// carry, and a path filter needs only typing, deletion and cursor
// movement. The optional fields let other forms reuse it; their zero
// values keep the region filter behaviour.
type propInput struct {
	runes []rune
	pos   int

	// placeholder is the faint hint of an empty input. "" keeps the
	// region filter hint.
	placeholder string
	// limit bounds the value in runes. 0 keeps propInputLimit.
	limit int
	// accept filters typed runes, for example digits only for a day
	// count. nil accepts every rune that the sanitizer keeps.
	accept func(rune) bool
}

// maxRunes returns the length bound of the value.
func (in *propInput) maxRunes() int {
	if in.limit > 0 {
		return in.limit
	}

	return propInputLimit
}

// accepts reports whether the accept filter lets r into the value.
func (in *propInput) accepts(r rune) bool {
	return in.accept == nil || in.accept(r)
}

// set replaces the value and puts the cursor at its end.
func (in *propInput) set(s string) {
	in.runes = []rune(s)
	in.pos = len(in.runes)
}

// value returns the typed text.
func (in *propInput) value() string {
	return string(in.runes)
}

// key applies one key press. The sanitizer rejects control and
// direction characters, so the filter bar cannot print an escape
// sequence or reorder text. The length bound and the accept filter
// drop further runes.
func (in *propInput) key(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		for _, r := range msg.Runes {
			if s := string(r); oneLine(s) != s || len(in.runes) >= in.maxRunes() || !in.accepts(r) {
				continue
			}

			in.runes = slices.Insert(in.runes, in.pos, r)
			in.pos++
		}
	case tea.KeyBackspace:
		if in.pos > 0 {
			in.runes = slices.Delete(in.runes, in.pos-1, in.pos)
			in.pos--
		}
	case tea.KeyDelete:
		if in.pos < len(in.runes) {
			in.runes = slices.Delete(in.runes, in.pos, in.pos+1)
		}
	case tea.KeyCtrlU:
		in.runes = in.runes[in.pos:]
		in.pos = 0
	case tea.KeyLeft:
		in.pos = max(0, in.pos-1)
	case tea.KeyRight:
		in.pos = min(len(in.runes), in.pos+1)
	case tea.KeyHome, tea.KeyCtrlA:
		in.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		in.pos = len(in.runes)
	}
}

// view renders the value with the cursor as a glyph. NO_COLOR removes
// every style, reverse video too, so only a glyph stays visible. At the
// end of the text the cursor is an underscore. Inside the text it is a
// bar between two characters, where typing inserts. A bar is rare in a
// path, but an underscore is common there.
func (in *propInput) view(t *Theme) string {
	if len(in.runes) == 0 {
		hint := in.placeholder
		if hint == "" {
			hint = "path, for example internal/api"
		}

		return t.Accent.Render("_") + t.Faint.Render(" "+oneLine(hint))
	}

	before := t.Body.Render(string(in.runes[:in.pos]))

	if in.pos == len(in.runes) {
		return before + t.Accent.Render("_")
	}

	return before + t.Accent.Render("|") + t.Body.Render(string(in.runes[in.pos:]))
}

// propNormalizeRegion trims a typed region to the repository-relative form
// that proposals use. "*" means every region, like an empty filter.
func propNormalizeRegion(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "./")
	s = strings.Trim(s, "/")

	if s == "*" {
		return ""
	}

	return s
}

// reconcile applies a new ledger snapshot. The selection keeps only
// IDs that are still pending and visible, and the cursor stays on the
// same proposal. Every loss is reported: a refresh must never change
// what an action targets without telling the user.
func (p *propView) reconcile() tea.Cmd {
	pending := map[int64]domain.Proposal{}

	if l := p.ledger(); l != nil {
		for _, pr := range l.Pending {
			pending[pr.ID] = pr
		}
	}

	// A pin hint is useful only while its proposal stays pending.
	for id := range p.pinned {
		if _, ok := pending[id]; !ok {
			delete(p.pinned, id)
		}
	}

	var left, outside, outsideRun []int64

	for _, id := range p.selectedIDs() {
		pr, ok := pending[id]

		switch {
		case !ok:
			delete(p.selected, id)

			if !p.acted[id] {
				left = append(left, id)
			}
		case !proposals.MatchesRegion(pr, p.region):
			delete(p.selected, id)
			outside = append(outside, id)
		case !p.inRun(id):
			delete(p.selected, id)
			outsideRun = append(outsideRun, id)
		}
	}

	var notes []string

	if len(left) > 0 {
		notes = append(notes, propIDList(left)+" left the selection: no longer pending")
	}

	if len(outside) > 0 {
		notes = append(notes, propIDList(outside)+" left the selection: outside the region filter")
	}

	if len(outsideRun) > 0 {
		notes = append(notes, propIDList(outsideRun)+" left the selection: outside the run filter")
	}

	rows := p.rows()
	gone := p.cursorID

	if gone == 0 {
		// Nothing was under the cursor yet: the first snapshot, or an
		// empty list.
		p.syncCursorID(rows)
	} else if found := p.relocate(rows, true); !found && !p.acted[gone] && p.shown() {
		note := propID(gone) + " left this list"
		if cur, ok := p.current(rows); ok {
			note += "; the cursor moved to " + propID(cur.ID)
		}

		notes = append(notes, note)
	}

	p.acted = map[int64]bool{}

	if len(notes) == 0 {
		return nil
	}

	return toast(ToneAttention, strings.Join(notes, "; "))
}

// shown reports that the list is on screen.
func (p *propView) shown() bool {
	return p.visible == nil || p.visible()
}

// markActed records IDs that the console itself removes or moves. The
// next refresh does not report them as lost, because the user confirmed
// the change.
func (p *propView) markActed(ids []int64) {
	for _, id := range ids {
		delete(p.selected, id)
		delete(p.pinned, id)
		p.acted[id] = true
	}
}

// propIDList renders IDs in the ledger's pN form.
func propIDList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = propID(id)
	}

	return strings.Join(parts, ", ")
}

// startDecision asks for a preview of an action on the current
// targets. The preview reads state only; the commit comes later and
// only after an explicit confirmation.
func (p *propView) startDecision(a proposals.Action) tea.Cmd {
	ids, implicit := p.targets()
	if len(ids) == 0 {
		if p.filter != propPending {
			return toast(ToneNeutral, "Applied, dismissed and superseded proposals are read-only. Press f to show "+
				"pending ones.")
		}

		return nil
	}

	return p.preview(a, ids, implicit)
}

// startRecord asks for the ledger-only preview that records the targets
// as applied. A target without a known pin gets a toast with the reason
// and no preview, so c never opens what the footer hides and the help
// marks n/a (user decision of 2026-10-04). A read-only row keeps the
// toast of startDecision.
func (p *propView) startRecord() tea.Cmd {
	ids, _ := p.targets()
	if len(ids) == 0 || p.hasPinned(ids) {
		return p.startDecision(proposals.ActionReconcile)
	}

	return toast(ToneNeutral, p.noPinText(ids))
}

// noPinText says why c records nothing. The console knows a pin only from
// its last read of the pin file. After a good read, the text names r:
// only an edit of the file since that read can add the pin. With marks,
// the text counts the marked rows without a known pin, because a list of
// IDs can be too long for the notice row. A row whose region fields
// disagree needs no read of the file, because the record refuses it.
func (p *propView) noPinText(ids []int64) string {
	if i := slices.IndexFunc(ids, p.fieldsDisagree); i >= 0 {
		return "No record applied: the stored region fields of " + propID(ids[i]) +
			" disagree, so no pin is proven to be its own."
	}

	// Without a good read, the console must not say that the file has no
	// pin. The Pins state line names the read error, so the text sends
	// the user there.
	switch {
	case !p.st.pins.loaded:
		return "No record applied: the console has not read " + proposals.LessonsFile + " yet. Press r to read it."
	case !pinLinksKnown(p.st.pins.value):
		return "No record applied: the pin file or the ledger could not be read; m (Pins) names the error. " +
			"Press r after you fix it."
	}

	const next = " yet. Press r after you edit the file."

	if len(p.selected) == 0 {
		return "No record applied: no pin in " + proposals.LessonsFile + " carries " + propID(ids[0]) + next
	}

	n := p.unpinned(ids)

	verb := " have"
	if n == 1 {
		verb = " has"
	}

	return "No record applied: " + strconv.Itoa(n) + " of " + propPlural(len(ids), "marked proposal",
		"marked proposals") + verb + " no pin in " + proposals.LessonsFile + next
}

// preview opens the preparing modal and starts the preview read.
func (p *propView) preview(a proposals.Action, ids []int64, implicit bool) tea.Cmd {
	seq := p.seq.start()
	p.dec = &propDecision{stage: propStagePreparing, action: a, ids: ids, implicit: implicit}

	var svc Decisions = p.st.svc

	return func() tea.Msg {
		pv, err := svc.PreviewDecision(a, ids)

		return propPreviewMsg{seq: seq, pv: pv, err: err}
	}
}

// previewed shows a preview result, unless the user cancelled it.
func (p *propView) previewed(msg propPreviewMsg) tea.Cmd {
	if p.dec == nil || !p.seq.current(msg.seq) || p.dec.stage != propStagePreparing {
		return nil
	}

	p.dec.stage = propStagePreview
	p.dec.pv, p.dec.err = msg.pv, msg.err

	if msg.err == nil && msg.pv == nil {
		p.dec.err = errors.New("the preview came back empty")
	}

	p.notePins(msg.pv, msg.err)

	p.dec.scroll = lesScroll{}

	return nil
}

// notePins updates the pin hints from a preview. An apply preview
// checks the pin file for every proposal it covers, so it replaces
// their hints. A record-as-applied preview succeeds only when every
// pin exists.
func (p *propView) notePins(pv *proposals.Preview, err error) {
	if err != nil || pv == nil || pv.Action == proposals.ActionDismiss {
		return
	}

	if pv.Action == proposals.ActionApply {
		for _, pr := range pv.Proposals {
			delete(p.pinned, pr.ID)
		}
	}

	for _, id := range pv.AlreadyPinned {
		p.pinned[id] = true
	}
}

// dropPinHints removes the pin hints that a new Pins inspection
// contradicts. The inspection is the newer read of the pin file, so a pin
// that it does not find is gone, for example after a hand edit and r.
// Without this step, c opens a record preview that the record refuses.
// A partial apply keeps its hints, because its refresh reads the written
// pins as pending matches.
func (p *propView) dropPinHints() {
	in := p.st.pins.value

	// An inspection without a read of the pin file or the ledger knows no
	// link, so it cannot contradict a hint.
	if !p.st.pins.loaded || !pinLinksKnown(in) {
		return
	}

	// A row that the inspection does not list keeps its hint. The
	// inspection then read the ledger at another time than the list, so
	// it says nothing about the pin of the row.
	for id := range p.pinned {
		if link, listed := in.Links.ByProposal[id]; listed && link != pinfile.LinkPendingMatch {
			delete(p.pinned, id)
		}
	}
}

// decisionKey handles keys while a decision modal is open.
func (p *propView) decisionKey(msg tea.KeyMsg) tea.Cmd {
	d := p.dec

	switch d.stage {
	case propStagePreparing:
		if key.Matches(msg, nkeys.Back) {
			// The read may still finish; seq drops its result.
			p.seq.cancel()
			p.dec = nil
		}
	case propStagePreview:
		return p.previewKey(msg)
	case propStageCommitting:
		// The commit cannot stop safely. Keys wait for the result.
	case propStageResult:
		return p.resultKey(msg)
	}

	return nil
}

// previewKey handles the preview modal. Esc returns to the list with
// the selection intact; only y commits.
func (p *propView) previewKey(msg tea.KeyMsg) tea.Cmd {
	d := p.dec

	switch {
	case key.Matches(msg, nkeys.Back):
		p.dec = nil
	case key.Matches(msg, lesKeys.Confirm):
		if p.confirmable() {
			return p.commit()
		}
	case key.Matches(msg, lesKeys.Reconcile):
		// Existing pins come first: c then records them, because a copy
		// of the block would paste the existing pins again.
		if ids := p.reconcileOffer(); len(ids) > 0 {
			return p.preview(proposals.ActionReconcile, ids, false)
		}

		if p.copyable() {
			return p.copyYAML()
		}
	default:
		d.scroll.key(msg)
	}

	return nil
}

// confirmable reports a preview that y may commit: eligible proposals,
// and for apply an open write gate with no existing pins.
func (p *propView) confirmable() bool {
	d := p.dec
	if d == nil || d.stage != propStagePreview || d.err != nil || d.pv == nil || len(d.pv.Proposals) == 0 {
		return false
	}

	if d.action == proposals.ActionApply {
		return d.pv.Write && len(d.pv.AlreadyPinned) == 0
	}

	return true
}

// reconcileOffer returns the IDs whose pins already exist, when an
// apply preview found them. Recording them as applied avoids a
// duplicate pin.
func (p *propView) reconcileOffer() []int64 {
	d := p.dec
	if d == nil || d.stage != propStagePreview || d.err != nil || d.pv == nil || d.action != proposals.ActionApply {
		return nil
	}

	return d.pv.AlreadyPinned
}

// copyable reports an apply preview whose pin block c copies. A preview
// with existing pins is not copyable: its block would duplicate them.
func (p *propView) copyable() bool {
	d := p.dec

	return d != nil && d.stage == propStagePreview && d.err == nil && d.pv != nil &&
		d.action == proposals.ActionApply && d.pv.YAML != "" && len(d.pv.Proposals) > 0 &&
		len(d.pv.AlreadyPinned) == 0
}

// copyYAML sends the exact pin block of the preview to the terminal
// clipboard (OSC 52). It copies Preview.YAML, not the folded display,
// so the pasted block is the text that apply would write. The terminal
// decides whether it honours the request, so the toast reports the
// request, not a confirmed copy.
func (p *propView) copyYAML() tea.Cmd {
	text := p.dec.pv.YAML
	st := p.st

	return func() tea.Msg {
		if err := st.copyToClipboard(text); err != nil {
			return toastMsg{tone: ToneFailure, text: "Could not copy the pin block: " + errText(err)}
		}

		return toastMsg{tone: ToneNeutral, text: "Sent to the terminal clipboard (OSC 52)"}
	}
}

// commit claims the mutation slot and runs the reviewed decision.
func (p *propView) commit() tea.Cmd {
	d := p.dec
	n := len(d.pv.Proposals)

	var name string

	switch d.action {
	case proposals.ActionDismiss:
		name = "Dismissing " + propPlural(n, "proposal", "proposals")
	case proposals.ActionReconcile:
		name = "Recording " + propPlural(n, "proposal", "proposals") + " as applied"
	default:
		name = "Applying " + propPlural(n, "proposal", "proposals")
	}

	if !p.st.begin(name) {
		return toast(ToneAttention, "Another operation is running: "+p.st.mutation)
	}

	d.stage = propStageCommitting
	pv := d.pv

	var svc Decisions = p.st.svc

	return func() tea.Msg {
		res, err := svc.CommitDecision(pv)

		return propCommitMsg{pv: pv, res: res, err: err}
	}
}

// committedMsg shows a commit result. It always frees the mutation
// slot and refreshes the affected sources, because a refusal also
// means that the state changed.
func (p *propView) committedMsg(msg propCommitMsg) tea.Cmd {
	// Decided proposals leave the selection now, so the refresh does
	// not report them as lost. Not moved IDs stay selected for the
	// ledger-only reconciliation.
	p.markActed(msg.res.Moved)

	// After a pin write without a file error, the not moved proposals
	// have their pins. Only a ledger-only record completes them.
	if msg.err == nil && msg.pv != nil && msg.pv.Action == proposals.ActionApply &&
		msg.res.FileWritten && msg.res.FileErr == nil {
		for _, id := range msg.res.NotMoved {
			p.pinned[id] = true
		}
	}

	if d := p.dec; d != nil && d.stage == propStageCommitting && d.pv == msg.pv {
		d.stage = propStageResult
		d.committed = true
		d.res, d.err = msg.res, msg.err
		d.scroll = lesScroll{}
	}

	return tea.Batch(mutationDone, refresh(secLedger, secStats, secIndex, secPins))
}

// reviewAgain reports a refusal that a new preview can resolve.
func (d *propDecision) reviewAgain() bool {
	return d.committed && d.err != nil && !errors.Is(d.err, proposals.ErrNothingSelected)
}

// resultKey handles the result modal. Enter builds a new preview after
// a refusal; it never repeats the commit.
func (p *propView) resultKey(msg tea.KeyMsg) tea.Cmd {
	d := p.dec

	switch {
	case key.Matches(msg, nkeys.Enter):
		if d.reviewAgain() {
			return p.preview(d.action, d.ids, d.implicit)
		}

		p.dec = nil
	case key.Matches(msg, nkeys.Back):
		p.dec = nil
	default:
		d.scroll.key(msg)
	}

	return nil
}

// keys returns the bindings of the current state. Unavailable actions
// are disabled, so they vanish from the footer; the detail pane
// explains why, and the help marks them n/a (see helpKeys). The order
// follows the 1a footer.
func (p *propView) keys() []key.Binding {
	if p.editing {
		return []key.Binding{withHelp(nkeys.Enter, "enter", "apply filter"), withHelp(nkeys.Back, "esc", "cancel")}
	}

	if d := p.dec; d != nil {
		return p.decisionKeys(d)
	}

	return p.listKeys("clear")
}

// helpKeys returns every key of the list for the help. Each key that
// does not work now is an unavailable entry. So the help names every
// Proposals action and marks the unavailable ones n/a. The user decided
// this on 2026-10-04 for the help only: the footer keeps the keys of
// Phase 2, so it uses keys(). esc is the one exception: the help lists
// it only with a run filter (see listKeys). An overlay or the region
// input keeps its own keys.
func (p *propView) helpKeys() []key.Binding {
	if p.capturing() {
		return p.keys()
	}

	// The help also lists x when nothing is selected. So its label names
	// what x clears; the footer keeps the short label of Phase 2.
	return listAll(p.listKeys("clear selection"))
}

// listKeys returns the bindings of the list. Each binding carries its
// availability for the cursor row, the selection, and the filters.
// clearLabel is the action text of x, because the footer and the help
// describe x differently.
func (p *propView) listKeys(clearLabel string) []key.Binding {
	rows := p.rows()
	cur, hasRow := p.current(rows)
	ids, _ := p.targets()

	sel := withHelp(nkeys.Toggle, "space", "select")
	sel.SetEnabled(hasRow && p.filter == propPending)

	// The label follows the gate that the ledger snapshot reports. The
	// preview reads the gate again, so a stale label changes nothing.
	applyLabel := "apply"
	if propGateOff(p.ledger()) {
		applyLabel = "YAML"
	}

	apply := withHelp(lesKeys.Apply, "a", applyLabel)
	apply.SetEnabled(len(ids) > 0)

	dismiss := withHelp(lesKeys.Dismiss, "d", "dismiss")
	dismiss.SetEnabled(len(ids) > 0)

	// t shows only when a target has an open question, so the footer
	// never offers an extraction that the review would refuse.
	extract := extractKey
	extract.SetEnabled(p.canExtract())

	clearSel := withHelp(lesKeys.Clear, "x", clearLabel)
	clearSel.SetEnabled(len(p.selected) > 0)

	// c works only when every target has a known existing pin, because
	// the record refuses a target without its pin. The press follows the
	// same rule (see startRecord), so the footer, the help, and the key
	// cannot disagree.
	reconcile := withHelp(lesKeys.Reconcile, "c", "record applied")
	reconcile.SetEnabled(p.hasPinned(ids))

	openLabel := "open PR"

	link, hasLink := propLink{}, false
	if hasRow {
		link, hasLink = p.link(cur)
	}

	if hasLink && !link.pr {
		openLabel = "open link"
	}

	open := withHelp(lesKeys.Open, "o", openLabel)
	open.SetEnabled(hasLink)

	out := []key.Binding{sel, apply, dismiss, p.paneKey(hasRow)}

	// esc ends a run filter once the detail pane has given back the
	// focus, so the footer names it only then. Without a run filter, esc
	// has nothing to end. So the list leaves it out, and the help does
	// not list it as n/a (user decision of 2026-10-04). The footer drops
	// a disabled key anyway, so it does not change.
	if p.run != nil {
		allRuns := withHelp(nkeys.Back, "esc", "all proposals")
		allRuns.SetEnabled(!p.focusDetail)

		out = append(out, allRuns)
	}

	// t follows f and /, because a narrow footer drops hints from the
	// end. So the 120-column footer keeps the keys of the 1a layout, and
	// the help still lists t.
	return append(out, withHelp(lesKeys.State, "f", "state"), withHelp(lesKeys.Region, "/", "region"), extract,
		open, clearSel, reconcile)
}

// paneKey names the key that changes the visible or focused pane.
func (p *propView) paneKey(hasRow bool) key.Binding {
	switch {
	case !hasRow:
		b := withHelp(nkeys.Enter, "enter", "detail")
		b.SetEnabled(false)

		return b
	case p.st.wide() && p.focusDetail:
		return withHelp(nkeys.Next, "tab", "list")
	case p.st.wide():
		return withHelp(nkeys.Next, "tab", "detail")
	case p.focusDetail:
		return withHelp(nkeys.Back, "esc", "back")
	default:
		return withHelp(nkeys.Enter, "enter", "detail")
	}
}

// hasPinned reports whether ids is not empty and every one of them has a
// known existing pin. The ledger-only record refuses the whole selection
// when one pin is missing, so one known pin is not enough.
func (p *propView) hasPinned(ids []int64) bool {
	return len(ids) > 0 && p.unpinned(ids) == 0
}

// unpinned counts the IDs of ids without a known existing pin.
func (p *propView) unpinned(ids []int64) int {
	n := 0

	for _, id := range ids {
		if !p.hasPin(id) {
			n++
		}
	}

	return n
}

// hasPin reports whether pending proposal id has a known existing pin.
// The pinned set knows only what a preview or a partial apply reported.
// The Pins inspection also knows a pending match before any preview, so
// the hint and c show on the first visit, as the Pins route says (D-4).
// A row whose region fields disagree has no proven pin, and the record
// refuses it, so c stays off for it.
func (p *propView) hasPin(id int64) bool {
	if p.fieldsDisagree(id) {
		return false
	}

	if p.pinned[id] {
		return true
	}

	link, known := extLink(p.st, id)

	return known && link == pinfile.LinkPendingMatch
}

// fieldsDisagree reports whether the ledger snapshot holds pending
// proposal id with stored region fields that disagree
// (pinfile.FieldsAgree). The apply preview can still find the pin that
// its apply writes, so this check also covers the pinned set.
func (p *propView) fieldsDisagree(id int64) bool {
	l := p.ledger()
	if l == nil {
		return false
	}

	i := slices.IndexFunc(l.Pending, func(pr domain.Proposal) bool { return pr.ID == id })

	return i >= 0 && !pinfile.FieldsAgree(l.Pending[i])
}

// extractTargets returns the proposals that t asks about: the marked
// pending rows, or the cursor row of the pending or the applied list
// when nothing is marked. Dismissed and superseded rows have no
// question to ask, so they give no target.
func (p *propView) extractTargets() []domain.Proposal {
	rows := p.rows()

	switch p.filter {
	case propPending:
		if len(p.selected) > 0 {
			var out []domain.Proposal

			for _, pr := range rows {
				if p.selected[pr.ID] {
					out = append(out, pr)
				}
			}

			return out
		}
	case propApplied:
	default:
		return nil
	}

	if cur, ok := p.current(rows); ok {
		return []domain.Proposal{cur}
	}

	return nil
}

// extractMarked reports that the marked rows are the targets of t. Else
// the cursor row stands in for an empty selection (contracts D-15).
func (p *propView) extractMarked() bool {
	return p.filter == propPending && len(p.selected) > 0
}

// canExtract reports a target whose trigger question the console can
// ask, so t shows in the footer.
func (p *propView) canExtract() bool {
	return slices.ContainsFunc(p.extractTargets(), func(pr domain.Proposal) bool {
		ok, _ := extQuestion(p.st, p.ledger(), pr)

		return ok
	})
}

// startExtract opens the trigger extraction review of the targets. The
// request keeps the whole selection, because the review lists every
// target that it does not ask, with its reason. A target set without an
// open question opens nothing; the toast says why.
func (p *propView) startExtract() tea.Cmd {
	targets := p.extractTargets()
	if len(targets) == 0 {
		if p.filter == propDismissed || p.filter == propSuperseded {
			return toast(ToneNeutral, "Dismissed and superseded proposals have no trigger question to ask.")
		}

		return nil
	}

	if !p.canExtract() {
		_, why := extQuestion(p.st, p.ledger(), targets[0])
		if len(targets) == 1 {
			return toast(ToneNeutral, "Nothing to extract for "+propID(targets[0].ID)+": "+why+".")
		}

		return toast(ToneNeutral, "No selected proposal has an open trigger question; "+propID(targets[0].ID)+": "+
			why+".")
	}

	ids := make([]int64, len(targets))
	for i, pr := range targets {
		ids[i] = pr.ID
	}

	// Without marks the cursor row is the target, and the review must
	// say so (contracts D-15).
	return openExtract(learning.ExtractRequest{IDs: ids}, !p.extractMarked())
}

// focus shows proposal id in the list of its state, for a link from an
// extraction result. It ends a run filter and a region filter that hide
// the row. Such a change clears the selection with its note, as every
// filter change does. Without the row in the snapshot, nothing changes.
func (p *propView) focus(id int64) tea.Cmd {
	l := p.ledger()
	if l == nil || p.dec != nil || p.editing {
		return nil
	}

	for f := propPending; f < propStateCount; f++ {
		rows := propStateRows(l, f)

		i := slices.IndexFunc(rows, func(pr domain.Proposal) bool { return pr.ID == id })
		if i < 0 {
			continue
		}

		changed := p.filter != f || p.run != nil || !proposals.MatchesRegion(rows[i], p.region)

		p.filter, p.run = f, nil

		if !proposals.MatchesRegion(rows[i], p.region) {
			p.region = ""
		}

		p.cursorID = id
		p.focusDetail = false

		// The list changes only with a filter, so only then does the
		// selection go, with its note. Else the cursor moves alone.
		if changed {
			return p.filterChanged()
		}

		p.relocate(p.rows(), false)

		return nil
	}

	return nil
}

// decisionKeys returns the footer bindings of one decision stage. The
// modal shows the same keys with longer labels.
func (p *propView) decisionKeys(d *propDecision) []key.Binding {
	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(d.scroll.scrolls())

	switch d.stage {
	case propStagePreparing:
		return []key.Binding{withHelp(nkeys.Back, "esc", "cancel")}
	case propStageCommitting:
		return nil
	case propStageResult:
		if d.reviewAgain() {
			return []key.Binding{withHelp(nkeys.Enter, "enter", "preview again"), withHelp(nkeys.Back, "esc", "close"),
				scroll}
		}

		return []key.Binding{withHelp(nkeys.Enter, "enter", "close"), scroll}
	}

	var out []key.Binding

	if p.confirmable() {
		out = append(out, withHelp(lesKeys.Confirm, "y", propConfirmWord(d.action)))
	}

	switch {
	case len(p.reconcileOffer()) > 0:
		out = append(out, withHelp(lesKeys.Reconcile, "c", "record applied"))
	case p.copyable():
		out = append(out, withHelp(lesKeys.Reconcile, "c", "copy"))
	}

	back := "close"
	if p.confirmable() {
		back = "cancel"
	}

	return append(out, withHelp(nkeys.Back, "esc", back), scroll)
}

// confirmLabel names the confirmed action with its count.
func (p *propView) confirmLabel() string {
	d := p.dec
	n := len(d.pv.Proposals)

	switch d.action {
	case proposals.ActionDismiss:
		return "dismiss " + propPlural(n, "proposal", "proposals")
	case proposals.ActionReconcile:
		return "record " + propPlural(n, "proposal", "proposals") + " as applied"
	default:
		// Apply writes one entry for each pin key of the selection.
		return "apply " + propPlural(n-len(d.pv.SharedPin), "pin", "pins")
	}
}

// propConfirmWord is the short footer label of y for an action.
func propConfirmWord(a proposals.Action) string {
	switch a {
	case proposals.ActionDismiss:
		return "dismiss"
	case proposals.ActionReconcile:
		return "record"
	default:
		return "apply"
	}
}

// propPlural renders a count with the matching noun.
func propPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return strconv.Itoa(n) + " " + many
}
