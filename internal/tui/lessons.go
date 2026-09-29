package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// lesSub is the visible sub-view of the Lessons destination.
type lesSub int

const (
	lesProposals lesSub = iota
	lesStats
)

// lesKeys are the Lessons bindings. The package values stay enabled,
// because key.Matches ignores a disabled binding; keys() returns
// copies that carry the current availability for the footer.
var lesKeys = struct {
	Proposals, Stats                   key.Binding
	Clear, State, Region, Open         key.Binding
	Apply, Dismiss, Reconcile, Confirm key.Binding
}{
	Proposals: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "proposals")),
	Stats:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stats")),
	Clear:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "clear selection")),
	State:     key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "state filter")),
	Region:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "region filter")),
	Open:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open PR")),
	Apply:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply")),
	Dismiss:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dismiss")),
	Reconcile: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "record as applied")),
	Confirm:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
}

// lessons is the Lessons destination. It holds two sub-views: the
// proposal decisions and the delivery statistics.
type lessons struct {
	st    *state
	sub   lesSub
	props *propView
	stats *statsView
}

func newLessons(st *state) *lessons {
	return &lessons{st: st, props: newPropView(st), stats: newStatsView(st)}
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

// open selects the sub-view that an intent names. The "proposals"
// intent also shows pending proposals, because Overview links here
// from its pending count. Unknown intents keep the current view.
func (s *lessons) open(intent string) tea.Cmd {
	switch intent {
	case "proposals":
		s.sub = lesProposals

		return s.props.showPending()
	case "stats":
		s.sub = lesStats
	}

	return nil
}

// update routes a message. Every non-key message reaches both
// sub-views, so background results land even in the hidden one. Key
// messages reach the visible sub-view only.
func (s *lessons) update(msg tea.Msg) tea.Cmd {
	km, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return tea.Batch(s.props.update(msg), s.stats.update(msg))
	}

	if s.sub == lesStats {
		if !s.stats.capturing() && key.Matches(km, lesKeys.Proposals) {
			s.sub = lesProposals

			return nil
		}

		return s.stats.update(msg)
	}

	if !s.props.capturing() && key.Matches(km, lesKeys.Stats) {
		s.sub = lesStats

		return nil
	}

	return s.props.update(msg)
}

// view renders the sub-view line and the visible sub-view under it.
func (s *lessons) view(w, h int) string {
	head := s.subTabs(w)

	if s.sub == lesStats {
		return head + "\n" + s.stats.view(w, h-1)
	}

	return head + "\n" + s.props.view(w, h-1)
}

// modal returns the open decision of the Proposals sub-view. The shell
// dims the frame under it, so the preview reads as a separate layer.
func (s *lessons) modal(w, h int) (string, bool) {
	if s.sub != lesProposals {
		return "", false
	}

	return s.props.modal(w, h)
}

// keys returns the visible sub-view's keys and the switch key. The
// switch key is hidden while the sub-view captures input. On Stats the
// switch key comes first, as in the 1a footer.
func (s *lessons) keys() []key.Binding {
	if s.sub == lesStats {
		if s.stats.capturing() {
			return s.stats.keys()
		}

		return append([]key.Binding{lesKeys.Proposals}, s.stats.keys()...)
	}

	ks := s.props.keys()
	if !s.props.capturing() {
		ks = append(ks, lesKeys.Stats)
	}

	return ks
}

func (s *lessons) capturing() bool {
	if s.sub == lesStats {
		return s.stats.capturing()
	}

	return s.props.capturing()
}

// propState is the proposal state filter.
type propState int

const (
	propPending propState = iota
	propApplied
	propDismissed
	propStateCount
)

// label returns the state name that the filter bar shows.
func (f propState) label() string {
	switch f {
	case propApplied:
		return "applied"
	case propDismissed:
		return "dismissed"
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
	// last apply preview or a partial apply reported. The footer offers
	// c only for them, because c records existing pins only.
	pinned map[int64]bool

	dec *propDecision
	seq int

	// wide and listH come from the last render, so key handling uses
	// the layout that the user sees.
	wide  bool
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

	switch p.filter {
	case propApplied:
		return l.Applied
	case propDismissed:
		return l.Dismissed
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
		if proposals.MatchesRegion(pr, p.region) {
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
// find the same proposal again by identity.
func (p *propView) syncCursorID(rows []domain.Proposal) {
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
func (p *propView) showPending() tea.Cmd {
	if p.filter == propPending || p.dec != nil || p.editing {
		return nil
	}

	p.filter = propPending

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
		if msg.kind == secLedger && msg.err == nil {
			return p.reconcile()
		}
	case propPreviewMsg:
		return p.previewed(msg)
	case propCommitMsg:
		return p.committedMsg(msg)
	}

	return nil
}

// browseKey handles keys while no modal and no input has focus.
func (p *propView) browseKey(msg tea.KeyMsg) tea.Cmd {
	rows := p.rows()

	// The screen gets the full terminal width, so the terminal size
	// gives the layout even before the first render.
	if p.st.width > 0 {
		p.wide = p.st.width >= wideWidth
	}

	switch {
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		if p.wide && len(rows) > 0 {
			p.focusDetail = !p.focusDetail
		}
	case key.Matches(msg, nkeys.Enter):
		if len(rows) > 0 {
			p.focusDetail = true
		}
	case key.Matches(msg, nkeys.Back):
		p.focusDetail = false
	case key.Matches(msg, nkeys.Toggle):
		p.toggle(rows)
	case key.Matches(msg, lesKeys.Clear):
		if len(p.selected) > 0 {
			p.selected = map[int64]bool{}

			return toast(ToneNeutral, "Selection cleared.")
		}
	case key.Matches(msg, lesKeys.State):
		p.filter = (p.filter + 1) % propStateCount

		return p.filterChanged()
	case key.Matches(msg, lesKeys.Region):
		p.editing = true
		p.input.set(p.region)

		return nil
	case key.Matches(msg, lesKeys.Apply):
		return p.startDecision(proposals.ActionApply)
	case key.Matches(msg, lesKeys.Dismiss):
		return p.startDecision(proposals.ActionDismiss)
	case key.Matches(msg, lesKeys.Reconcile):
		return p.startDecision(proposals.ActionReconcile)
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
	return len(rows) > 0 && (p.wide || p.focusDetail)
}

// detailKey scrolls the detail pane. The page keys always scroll it in
// the wide layout, as the "pgdn" hint under the pane says; the other
// movement keys scroll it only while it has focus. It reports whether
// the key scrolled.
func (p *propView) detailKey(msg tea.KeyMsg) bool {
	paging := key.Matches(msg, nkeys.PageUp, nkeys.PageDown)
	if !p.focusDetail && (!p.wide || !paging) {
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
// movement.
type propInput struct {
	runes []rune
	pos   int
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
// sequence or reorder text.
func (in *propInput) key(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		for _, r := range msg.Runes {
			if s := string(r); oneLine(s) != s || len(in.runes) >= propInputLimit {
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
		return t.Accent.Render("_") + t.Faint.Render(" path, for example internal/api")
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

	var left, outside []int64

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
		}
	}

	var notes []string

	if len(left) > 0 {
		notes = append(notes, propIDList(left)+" left the selection: no longer pending")
	}

	if len(outside) > 0 {
		notes = append(notes, propIDList(outside)+" left the selection: outside the region filter")
	}

	rows := p.rows()
	gone := p.cursorID

	if gone == 0 {
		// The first snapshot: nothing was under the cursor yet.
		p.syncCursorID(rows)
	} else if found := p.relocate(rows, true); !found && !p.acted[gone] {
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
			return toast(ToneNeutral, "Applied and dismissed proposals are read-only. Press f to show pending ones.")
		}

		return nil
	}

	return p.preview(a, ids, implicit)
}

// preview opens the preparing modal and starts the preview read.
func (p *propView) preview(a proposals.Action, ids []int64, implicit bool) tea.Cmd {
	p.seq++
	seq := p.seq
	p.dec = &propDecision{stage: propStagePreparing, action: a, ids: ids, implicit: implicit}

	svc := p.st.svc

	return func() tea.Msg {
		pv, err := svc.PreviewDecision(a, ids)

		return propPreviewMsg{seq: seq, pv: pv, err: err}
	}
}

// previewed shows a preview result, unless the user cancelled it.
func (p *propView) previewed(msg propPreviewMsg) tea.Cmd {
	if p.dec == nil || msg.seq != p.seq || p.dec.stage != propStagePreparing {
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

// decisionKey handles keys while a decision modal is open.
func (p *propView) decisionKey(msg tea.KeyMsg) tea.Cmd {
	d := p.dec

	switch d.stage {
	case propStagePreparing:
		if key.Matches(msg, nkeys.Back) {
			// The read may still finish; seq drops its result.
			p.seq++
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
	svc := p.st.svc

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
	for _, id := range msg.res.Moved {
		delete(p.selected, id)
		delete(p.pinned, id)
		p.acted[id] = true
	}

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

	return tea.Batch(mutationDone, refresh(secLedger, secStats, secIndex))
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
// explains why. The order follows the 1a footer.
func (p *propView) keys() []key.Binding {
	if p.editing {
		return []key.Binding{withHelp(nkeys.Enter, "enter", "apply filter"), withHelp(nkeys.Back, "esc", "cancel")}
	}

	if d := p.dec; d != nil {
		return p.decisionKeys(d)
	}

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

	clearSel := withHelp(lesKeys.Clear, "x", "clear")
	clearSel.SetEnabled(len(p.selected) > 0)

	// c stays bound for every target, because a pin can also come from
	// a hand edit. The footer shows it only for known existing pins, so
	// it does not invite a record that fails for a missing pin.
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

	return []key.Binding{sel, apply, dismiss, p.paneKey(hasRow), withHelp(lesKeys.State, "f", "state"),
		withHelp(lesKeys.Region, "/", "region"), open, clearSel, reconcile}
}

// paneKey names the key that changes the visible or focused pane.
func (p *propView) paneKey(hasRow bool) key.Binding {
	switch {
	case !hasRow:
		b := withHelp(nkeys.Enter, "enter", "detail")
		b.SetEnabled(false)

		return b
	case p.wide && p.focusDetail:
		return withHelp(nkeys.Next, "tab", "list")
	case p.wide:
		return withHelp(nkeys.Next, "tab", "detail")
	case p.focusDetail:
		return withHelp(nkeys.Back, "esc", "back")
	default:
		return withHelp(nkeys.Enter, "enter", "detail")
	}
}

// hasPinned reports whether any of ids has a known existing pin.
func (p *propView) hasPinned(ids []int64) bool {
	return slices.ContainsFunc(ids, func(id int64) bool { return p.pinned[id] })
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
			return []key.Binding{withHelp(nkeys.Enter, "enter", "review again"), withHelp(nkeys.Back, "esc", "close"),
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
		return "apply " + propPlural(n, "pin", "pins")
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
