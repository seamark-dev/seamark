package tui

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the state and the key handling of Lessons › Pins. The
// sub-view lists the entries of .seamark/lessons.yaml in file order and
// relates each one to the proposal ledger. The maintenance keys w, - and
// t plan and write nothing here: they only send the typed requests of
// pins_msgs.go, and the preview or the review reads the state again. The
// authoring keys n, i and d open the pin form and the pin review of
// pins_author.go, which plan and commit through services. The rendering
// lives in pins_view.go. The cursor, the pane keys and the filter input
// come from listpane.go, which Proposals shares.
//
// The view computes no link itself. It shows the links of
// maintain.Inspect, which applies pinfile.Classify. So Pins and
// seamark lessons name the same pin for each proposal row.

// pinKeys are the bindings that only the Pins sub-view uses. The shared
// keys (space, x, f, /, t) keep their meaning from Proposals. n, i and
// d open the authoring of pins_author.go; they act on the cursor row
// only, never on the marks.
var pinKeys = struct {
	Search, Compare, Retarget, Prune key.Binding
	New, Edit, Remove                key.Binding
}{
	Search:   key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "search")),
	Compare:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "compare")),
	Retarget: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "retarget")),
	Prune:    key.NewBinding(key.WithKeys("-"), key.WithHelp("-", "prune")),
	New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new pin")),
	Edit:     key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "edit")),
	Remove:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
}

// pinFilter is the list filter of the Pins sub-view. The installed pins
// come first, because the file is the authority for current guidance.
// The other two filters show ledger history and are read-only (D-22).
type pinFilter int

const (
	// pinInstalled lists the entries of the pin file.
	pinInstalled pinFilter = iota
	// pinNotPresent lists the applied proposals whose pin no entry carries.
	pinNotPresent
	// pinSuperseded lists the proposals whose pins a prune removed.
	pinSuperseded
	pinFilterCount
)

// label returns the filter name that the filter line shows.
func (f pinFilter) label() string {
	switch f {
	case pinNotPresent:
		return "not present"
	case pinSuperseded:
		return "superseded"
	default:
		return "installed"
	}
}

// pinRow is one list row: an installed pin, or a proposal row of the
// history filters.
type pinRow struct {
	// pin is the installed pin; nil for a proposal row.
	pin *maintain.Pin
	// n is the position of pin in Inspection.Pins.
	n int
	// prop is the proposal of a not-present or superseded row.
	prop domain.Proposal
}

// pinMarks is the multi-selection of the Pins sub-view: each marked
// entry binding with the link that it had when the user marked it.
type pinMarks map[pinfile.EntryRef]pinfile.Link

// has reports whether ref is marked.
func (m pinMarks) has(ref pinfile.EntryRef) bool {
	_, ok := m[ref]

	return ok
}

// pinID identifies a row across refreshes: the entry binding of a pin,
// or the ID of a proposal row. It never holds a list position, because a
// refresh can move every row (D-15).
type pinID struct {
	ref pinfile.EntryRef
	id  int64
}

// ident returns the identity of r.
func (r pinRow) ident() pinID {
	if r.pin != nil {
		return pinID{ref: r.pin.Entry.Ref()}
	}

	return pinID{id: r.prop.ID}
}

// pinsView is the Lessons › Pins sub-view. It holds the filter line, the
// list of installed pins or ledger history, the detail pane, and the
// read-only comparison of two similar pins. The embedded listPane holds
// the cursor, the focus and the detail scroll, and filterInput holds the
// region input and the text search.
type pinsView struct {
	listPane[pinRow, pinID]
	filterInput

	st *state

	// host holds the open maintenance preview (pins_msgs.go).
	host maintHost
	// author holds the authoring drafts, the open pin form and the open
	// pin review (pins_author.go).
	author pinAuthor

	filter pinFilter
	region string
	text   string

	// selected holds the marked entries by their binding, with the link
	// that the user saw when marking. A refresh keeps an entry only while
	// its content and its link stay the same.
	selected pinMarks
	// acted holds the entries that the last maintenance commit changed.
	// The next refresh does not report them as lost, because the user
	// caused the change.
	acted map[pinfile.EntryRef]bool
	// focusID is a proposal whose pin a link asked for before the first
	// inspection arrived; 0 when none waits.
	focusID int64

	// compare is the open comparison of two similar pins, or nil.
	compare *pinCompare
}

// newPinsView builds the Pins sub-view over the shared state. The list
// counts as hidden until lessons.go sets visible, so a refresh reports a
// cursor change only in a list that the user sees. A moved pin keeps
// the cursor by its content (pinFind).
func newPinsView(st *state) *pinsView {
	return &pinsView{
		listPane: listPane[pinRow, pinID]{
			visible: func() bool { return false },
			wide:    st.wide,
			ident:   pinRow.ident,
			find:    pinFind,
		},
		filterInput: filterInput{textHint: "text in the rule or the note"},
		st:          st,
		host:        maintHost{st: st},
		author:      pinAuthor{edits: map[pinfile.EntryRef]*pinDraft{}},
		selected:    pinMarks{},
		acted:       map[pinfile.EntryRef]bool{},
	}
}

// inspection returns the last pin inspection, or nil before the first.
func (p *pinsView) inspection() *maintain.Inspection {
	return p.st.pinInspection()
}

// pinInspection returns the last pin inspection, or nil before the
// first. The Pins sub-view and its pin review both read it.
func (s *state) pinInspection() *maintain.Inspection {
	if !s.pins.loaded {
		return nil
	}

	return s.pins.value
}

// pinLinksKnown reports an inspection whose every link is known: the pin
// file and the ledger both read.
func pinLinksKnown(in *maintain.Inspection) bool {
	return in != nil && in.LinkErr == nil && in.Links != nil && in.DocErr == nil && !in.Doc.Unreadable()
}

// actionable reports a list that can offer maintenance: installed pins
// with known links. An unreadable file or ledger hides every action
// (D-5), because no plan could bind to it.
func (p *pinsView) actionable() bool {
	return p.filter == pinInstalled && pinLinksKnown(p.inspection())
}

// pinLinked reports a pin with exactly one linked applied proposal. Only
// such a pin allows an automated action (D-4).
func pinLinked(pin *maintain.Pin) bool {
	return pin.Link.Link == pinfile.LinkLinked && len(pin.Link.Applied) == 1
}

// editBlock returns why the file editor cannot change pin, or "" when it
// can. A retarget and a prune both need a linked pin whose entry and file
// pass the editing gate (D-4, D-6). A copied checkout also stops both,
// because one write would then change two repositories (pinsForeignRoot).
func (p *pinsView) editBlock(pin *maintain.Pin) string {
	in := p.inspection()

	switch {
	case !pinLinked(pin):
		return "it has " + pinLinkNoun(pin.Link.Link)
	case pin.Entry.Block != "":
		return "the entry cannot be edited safely (" + ui.Diag(pin.Entry.Block) + ")"
	case in == nil || in.Doc == nil:
		return "the pin file is not read"
	case pinsForeignRoot(p.st) != "":
		return "the index records another repository, " + ui.OneLine(pinsForeignRoot(p.st))
	case in.Doc.FileBlock != "":
		return "the pin file cannot be edited (" + ui.Diag(in.Doc.FileBlock) + ")"
	}

	return ""
}

// pinsForeignRoot returns the repository root that the index records
// when it is not the workspace root, else "". It is the rule of the
// retarget and the prune. The maintenance engine reads and writes the
// pin file under the recorded root, as the CLI does. In a copied
// checkout that root names the original repository, but the ledger is
// the database of the copy. So one operation would change two
// repositories. A --db flag makes another root normal, so the check
// applies only to the default database path (services ownRoot).
func pinsForeignRoot(st *state) string {
	if st.svc.DBPath() != store.DefaultPath(st.svc.Root()) {
		return ""
	}

	return authorForeignRoot(st)
}

// authorForeignRoot returns the repository root that the index records
// when it is not the workspace root, else "". It is the rule of the pin
// authoring engine, which refuses another root also with --db (services
// foreignRoot, P4-16). So the form and the engine agree. The comparison
// does no I/O, because Update and View must not read the disk. So,
// unlike the engine, it does not resolve a symbolic link.
func authorForeignRoot(st *state) string {
	if !st.pins.loaded || st.pins.value == nil {
		return ""
	}

	root, ws := st.pins.value.Index.Root, st.svc.Root()

	if root == "" || ws == "" || filepath.Clean(root) == filepath.Clean(ws) {
		return ""
	}

	return root
}

// retargetBlock returns why a retarget of pin is not offered, or "" when
// it is. R-5 allows a retarget only for a computed scope that differs
// from the live regions. The planner gives no confirmable item for a
// current, an uncomputable or an unknown scope, or for computed regions
// that another pin or proposal already has. So the key would only open
// a preview without y.
//
// A known scope needs an indexed database, because Inspect computes no
// scope advice without one. A computable scope needs one living cited
// finding. Without it the advice is distill.ScopeNoEvidence.
func (p *pinsView) retargetBlock(pin *maintain.Pin) string {
	if why := p.editBlock(pin); why != "" {
		return why
	}

	s := pin.Readings.Scope

	switch {
	case !s.Known && s.Reason == "":
		return "its scope is unknown"
	case !s.Known:
		return "its scope is unknown: " + pinReason(s.Reason)
	case s.Advice.Kind == distill.ScopeCurrent:
		return "its regions are already current"
	case s.Advice.Kind == distill.ScopeNoEvidence:
		return "no cited finding is living, so no scope can be computed"
	}

	if owner := pinTargetOwner(p.inspection(), pin, s.Advice.Regions); owner != "" {
		return owner
	}

	return ""
}

// pinTargetOwner returns a clause that names the other pin or the
// applied proposal that has the rule of pin with regions, or "". The
// clause can end a sentence. The pin file planner refuses such a
// retarget, because two pins or two proposals would then share one
// identity (pinfile ErrCollision). An applied row counts by its
// reviews.ProposalKey, as maintain.appliedKeys counts it. Keep the two
// rules the same, so the key and the planner never disagree.
func pinTargetOwner(in *maintain.Inspection, pin *maintain.Pin, regions []string) string {
	if in == nil {
		return ""
	}

	target := reviews.NewPinKey(pin.Entry.Rule, "", regions)

	for i := range in.Pins {
		if e := in.Pins[i].Entry; e.Index != pin.Entry.Index && e.Key == target {
			return "pin " + strconv.Itoa(i+1) + pinLineSuffix(e) + " already has this rule with the computed regions"
		}
	}

	// Each applied row matches an entry or is in Links.NotPresent, also a
	// row with disagreeing fields (pinfile.Classify). So these lists hold
	// every applied row. The own row of pin is no other owner.
	own := pinProposalIDs(pin.Link.Applied)
	rows := [][]domain.Proposal{}

	for i := range in.Pins {
		rows = append(rows, in.Pins[i].Link.Applied)
	}

	if in.Links != nil {
		rows = append(rows, in.Links.NotPresent)
	}

	for _, list := range rows {
		for _, pr := range list {
			if slices.Contains(own, pr.ID) {
				continue
			}

			if reviews.ProposalKey(pr) == target {
				return "the applied proposal " + propID(pr.ID) + " already records this rule with the computed regions"
			}
		}
	}

	return ""
}

// pinLineSuffix returns " (line L)" for an entry whose line is known.
func pinLineSuffix(e pinfile.Entry) string {
	if e.Line <= 0 {
		return ""
	}

	return " (line " + strconv.Itoa(e.Line) + ")"
}

// pinExtractBlock returns why a trigger extraction of pin asks nothing,
// or "" when the review can ask. The readings apply the rules of the
// extraction planner (distill.TriggerQuestionOpen, living citations), so
// Pins and Proposals offer t for the same rows. An unknown reading lets
// the review decide, because the review reads the rows again.
func pinExtractBlock(pin *maintain.Pin) string {
	r := pin.Readings

	switch {
	case !pinLinked(pin):
		return "it has " + pinLinkNoun(pin.Link.Link)
	case r.Trigger.Known && !r.Trigger.Open:
		return pinAnsweredReason(r.Trigger.State)
	case r.Confidence.Known && len(r.Confidence.Living) == 0:
		return "no cited finding is living"
	}

	return ""
}

// pinAnsweredReason returns why an answered trigger question asks
// nothing. The words are shorter than the shared reason phrase, so the
// refusal "No trigger extraction: <reason>" fits an 80-column notice
// row. The Triggers reading keeps the shared phrase.
func pinAnsweredReason(r distill.TriggerReason) string {
	switch r {
	case distill.TriggerAnsweredPositive:
		return "its trigger paths are already stored"
	case distill.TriggerAnsweredCurrentNegative:
		return "its current trigger question has a negative answer"
	}

	return "the trigger question is " + r.String()
}

// pinEntryRegions returns the effective region set of an entry, as pin
// delivery reads it. An empty set is repository-wide.
func pinEntryRegions(e pinfile.Entry) []string {
	return reviews.PinRule{Rule: e.Rule, Region: e.Region, Regions: e.Regions, Note: e.Note}.AllRegions()
}

// matchesPin applies the region filter and the text search to a pin.
// Both read the live entry, because the file is what agents receive.
func (p *pinsView) matchesPin(pin *maintain.Pin) bool {
	e := pin.Entry

	return proposals.MatchesRegionSet(pinEntryRegions(e), p.region) && proposals.MatchesText(e.Rule, e.Note, p.text)
}

// matchesProp applies the filters to a proposal row of the history
// filters, over its recorded fields.
func (p *pinsView) matchesProp(pr domain.Proposal) bool {
	return proposals.MatchesRegion(pr, p.region) && proposals.MatchesText(pr.Rule, pr.Note, p.text)
}

// rows returns the visible rows of the current filter. Installed pins
// keep file order, because the order decides delivery ties. History rows
// keep the ascending ID order of the ledger.
func (p *pinsView) rows() []pinRow {
	in := p.inspection()
	if in == nil {
		return nil
	}

	var out []pinRow

	switch p.filter {
	case pinNotPresent:
		if in.Links == nil {
			return nil
		}

		for _, pr := range in.Links.NotPresent {
			if p.matchesProp(pr) {
				out = append(out, pinRow{prop: pr})
			}
		}
	case pinSuperseded:
		for _, pr := range in.Superseded {
			if p.matchesProp(pr) {
				out = append(out, pinRow{prop: pr})
			}
		}
	default:
		for i := range in.Pins {
			if p.matchesPin(&in.Pins[i]) {
				out = append(out, pinRow{pin: &in.Pins[i], n: i})
			}
		}
	}

	return out
}

// pinFind returns the position of the row with identity id. A proposal
// row matches by its ID, and a pin by pinMatch.
func pinFind(rows []pinRow, id pinID) (int, bool) {
	if id.ref == (pinfile.EntryRef{}) {
		i := slices.IndexFunc(rows, func(r pinRow) bool { return r.ident() == id })

		return max(i, 0), i >= 0
	}

	return pinMatch(len(rows), func(i int) pinfile.EntryRef { return rows[i].ident().ref }, id.ref)
}

// pinMatch returns the position of the pin with binding ref among n
// pins, where refAt gives each binding. A pin can move but keep its key
// and its own lines. It is then the same pin, so it matches when exactly
// one pin has that content. The list, the selection and the comparison
// all use this rule, so a move never loses a pin in one of them (D-15).
func pinMatch(n int, refAt func(int) pinfile.EntryRef, ref pinfile.EntryRef) (int, bool) {
	at, same := 0, 0

	for i := range n {
		r := refAt(i)
		if r == ref {
			return i, true
		}

		if r.Key == ref.Key && r.RawSHA == ref.RawSHA {
			at, same = i, same+1
		}
	}

	return at, same == 1
}

// capturing reports an open pin form or review, an open preview, a
// focused input, or an open comparison. Each takes every key, so the
// Lessons switch keys wait.
func (p *pinsView) capturing() bool {
	return p.author.open() || p.host.open() || p.editing != filterNone || p.compare != nil
}

// modal returns the open pin review or discard question, the open
// preview, or the comparison.
func (p *pinsView) modal(w, h int) (string, bool) {
	if view, ok := p.authorModal(w, h); ok {
		return view, true
	}

	if p.host.open() {
		return p.host.view(w, h)
	}

	if p.compare != nil {
		return p.compareModal(w, h), true
	}

	return "", false
}

// update routes msg to the pin authoring first, then to the open
// preview, as maintHost asks. Then it handles the keys and the
// refreshes of the sub-view.
func (p *pinsView) update(msg tea.Msg) tea.Cmd {
	authored, done := p.authorRoute(msg)
	if done {
		return authored
	}

	cmd, done := p.host.route(msg)
	cmd = tea.Batch(authored, cmd)
	p.markActed(p.host.takeActed())

	if done {
		return cmd
	}

	// A preview takes the place of a comparison, so two overlays never
	// stack.
	if p.host.open() {
		p.compare = nil
	}

	switch m := msg.(type) {
	case tea.KeyMsg:
		switch {
		case p.editing != filterNone:
			return tea.Batch(cmd, p.editKey(m))
		case p.compare != nil:
			return tea.Batch(cmd, p.compareKey(m))
		default:
			return tea.Batch(cmd, p.browseKey(m))
		}
	case loadedMsg:
		// Every inspection replaces the last one, so each one needs the
		// reconciliation of the selection and the cursor.
		if m.kind == secPins && m.err == nil {
			return tea.Batch(cmd, p.reconcile())
		}
	case focusPinMsg:
		return tea.Batch(cmd, p.focus(m.id))
	}

	return cmd
}

// markActed drops the entries that a maintenance commit changed from the
// selection, and keeps them so the next load reports no loss for them.
// The user caused the change: a prune removes an entry, and a retarget
// changes its key.
func (p *pinsView) markActed(refs []pinfile.EntryRef) {
	for _, ref := range refs {
		delete(p.selected, ref)
		p.acted[ref] = true
	}
}

// browseKey handles keys while no input, comparison or preview has
// focus. The pane keys of listPane come last (navKey).
func (p *pinsView) browseKey(msg tea.KeyMsg) tea.Cmd {
	rows := p.rows()

	switch {
	case key.Matches(msg, ui.Nav.Toggle):
		p.toggle(rows)
	case key.Matches(msg, lesKeys.Clear):
		if len(p.selected) > 0 {
			p.selected = pinMarks{}

			return toast(ui.ToneNeutral, "Selection cleared.")
		}
	case key.Matches(msg, lesKeys.State):
		if p.inspection() == nil {
			return nil
		}

		p.filter = (p.filter + 1) % pinFilterCount

		return p.filterChanged()
	case key.Matches(msg, lesKeys.Region):
		if p.inspection() != nil {
			p.startEdit(filterRegion, p.region)
		}
	case key.Matches(msg, pinKeys.Search):
		if p.inspection() != nil {
			p.startEdit(filterText, p.text)
		}
	case key.Matches(msg, pinKeys.Compare):
		return p.openCompare(rows)
	case key.Matches(msg, pinKeys.New):
		return p.newPin()
	case key.Matches(msg, pinKeys.Edit):
		return p.editPin()
	case key.Matches(msg, pinKeys.Remove):
		return p.removePin()
	case key.Matches(msg, pinKeys.Retarget):
		return p.requestMaint(maintain.ActionRetarget)
	case key.Matches(msg, pinKeys.Prune):
		return p.requestMaint(maintain.ActionPrune)
	case key.Matches(msg, extractKey):
		return p.extract()
	default:
		p.navKey(msg, rows)
	}

	return nil
}

// toggle marks or unmarks the pin under the cursor. Only installed pins
// with known links can be marked: history rows are read-only, and an
// unreadable file or ledger offers no action. A marked pin without one
// linked proposal stays allowed, so the preview can name why it is
// blocked (D-15).
func (p *pinsView) toggle(rows []pinRow) {
	cur, ok := p.cursorRow(rows)
	if !ok || cur.pin == nil || !p.actionable() {
		return
	}

	ref := cur.pin.Entry.Ref()

	if p.selected.has(ref) {
		delete(p.selected, ref)
	} else {
		p.selected[ref] = cur.pin.Link.Link
	}
}

// filterChanged keeps the cursor on the same row when it stays visible,
// and clears the selection. A hidden marked pin could otherwise be
// changed by accident, so the clearing is announced.
func (p *pinsView) filterChanged() tea.Cmd {
	if note := p.applyFilter(); note != "" {
		return toast(ui.ToneAttention, note)
	}

	return nil
}

// applyFilter relocates the cursor after a filter change and clears the
// selection. It returns the note that announces the clearing, or "".
// It also starts the detail at the top. Proposals keeps the detail
// offset, so the reset stays here and not in listPane.
func (p *pinsView) applyFilter() string {
	p.relocate(p.rows(), false)
	p.detail = ui.ScrollState{}

	if len(p.selected) == 0 {
		return ""
	}

	p.selected = pinMarks{}

	return "Selection cleared: filter changed"
}

// editKey handles the focused input. Enter applies the value, esc keeps
// the previous one.
func (p *pinsView) editKey(msg tea.KeyMsg) tea.Cmd {
	field, value, done := p.inputKey(msg)

	switch {
	case !done:
		return nil
	case field == filterText && value != p.text:
		p.text = value
	case field == filterRegion && value != p.region:
		p.region = value
	default:
		return nil
	}

	return p.filterChanged()
}

// targets returns the pins that an action acts on. These are the marked
// pins in file order, or the cursor pin when nothing is marked. cursor
// reports the second case, so the preview can say so (D-15). No case
// returns every pin of the list. A change to several pins needs marks,
// so one key press never changes a pin that the user did not choose.
func (p *pinsView) targets() (pins []*maintain.Pin, cursor bool) {
	if !p.actionable() {
		return nil, false
	}

	in := p.inspection()

	if len(p.selected) > 0 {
		for i := range in.Pins {
			if p.selected.has(in.Pins[i].Entry.Ref()) {
				pins = append(pins, &in.Pins[i])
			}
		}

		return pins, false
	}

	if cur, ok := p.cursorRow(p.rows()); ok && cur.pin != nil {
		return []*maintain.Pin{cur.pin}, true
	}

	return nil, false
}

// offers reports a target for which block gives no reason. The footer
// shows an action key as available only then. Otherwise it lists the
// key as n/a, and the press shows the reason. So no key leads to a
// preview or a review without an eligible item (contracts §3.2).
func (p *pinsView) offers(block func(*maintain.Pin) string) bool {
	pins, _ := p.targets()

	return slices.ContainsFunc(pins, func(pin *maintain.Pin) bool { return block(pin) == "" })
}

// actionGate returns the name and the eligibility rule of a maintenance
// action.
func (p *pinsView) actionGate(a maintain.Action) (name string, block func(*maintain.Pin) string) {
	if a == maintain.ActionRetarget {
		return "retarget", p.retargetBlock
	}

	return "prune", p.editBlock
}

// refusal explains an unavailable action key in one line. It plans
// nothing and opens nothing. The reason comes first and the pin last:
// the notice row cuts a long line at its end, and an 80-column row must
// still show the reason. Without marks the line names the cursor pin,
// because that pin is the target (D-15). With marks it names the first
// selected pin, whose reason it gives.
func (p *pinsView) refusal(action string, block func(*maintain.Pin) string) tea.Cmd {
	pins, cursor := p.targets()

	switch {
	case len(pins) == 0:
		return toast(ui.ToneNeutral, p.noTarget(action))
	case cursor:
		return toast(ui.ToneNeutral, pinRefusal(action, block(pins[0]), p.quoteRule(pins[0].Entry.Rule)))
	default:
		return toast(ui.ToneNeutral, pinRefusal(action, block(pins[0]), p.quoteRule(pins[0].Entry.Rule)+", "+
			pinSelectionNoun(len(pins))))
	}
}

// pinRefusal returns the line "No <action>: <why> (<who>).", for example
// `No retarget: its regions are already current ("wrap-sql-errors").`
// The MAINTENANCE detail starts its lines with the same "No <action>:",
// so the toast and the detail read the same.
func pinRefusal(action, why, who string) string {
	return "No " + action + ": " + why + " (" + who + ")."
}

// pinSelectionNoun names the pin that a refusal with marks explains: the
// only selected pin, or the first of n selected pins.
func pinSelectionNoun(n int) string {
	if n == 1 {
		return "the selected pin"
	}

	return "the first of " + strconv.Itoa(n) + " selected pins"
}

// requestMaint sends the preview request of action a over the targets. The
// request carries every target, also the blocked ones, so the preview
// lists them and offers no y (D-15). It reads and writes nothing itself.
func (p *pinsView) requestMaint(a maintain.Action) tea.Cmd {
	if name, block := p.actionGate(a); !p.offers(block) {
		return p.refusal(name, block)
	}

	pins, cursor := p.targets()
	refs := make([]pinfile.EntryRef, len(pins))

	for i, pin := range pins {
		refs[i] = pin.Entry.Ref()
	}

	return openMaint(maintain.Request{Action: a, Refs: refs, Cursor: cursor})
}

// extract sends the extraction review request of the targets. Linked
// pins give their proposal IDs; every other target goes to Skipped with
// its reason, so the review lists it as ineligible (contracts §2.3b).
func (p *pinsView) extract() tea.Cmd {
	if !p.offers(pinExtractBlock) {
		return p.refusal("trigger extraction", pinExtractBlock)
	}

	pins, cursor := p.targets()

	var req learning.ExtractRequest

	for _, pin := range pins {
		if pinLinked(pin) {
			req.IDs = append(req.IDs, pin.Link.Applied[0].ID)

			continue
		}

		req.Skipped = append(req.Skipped, distill.ExtractSkip{ID: pinSkipID(pin), Reason: p.skipReason(pin)})
	}

	// The review names the cursor pin when nothing is marked, as the
	// maintenance preview does (contracts D-15).
	return openExtract(req, cursor)
}

// pinSkipID returns the one proposal that a non-linked pin relates to,
// or 0 when it relates to none or to several.
func pinSkipID(pin *maintain.Pin) int64 {
	rows := append(slices.Clone(pin.Link.Applied), pin.Link.Pending...)
	if len(rows) == 1 {
		return rows[0].ID
	}

	return 0
}

// skipReason names a non-linked pin and its link reason for the
// extraction review. The rule is sanitized here as well, because the
// reason is display text.
func (p *pinsView) skipReason(pin *maintain.Pin) string {
	return "pin " + p.quoteRule(pin.Entry.Rule) + ": " + pin.Link.Link.Reason()
}

// pinNoteRuleW bounds a rule inside a sentence, so one long rule cannot
// push the rest of a note off the line.
const pinNoteRuleW = 40

// quoteRule renders a rule in quotes for a sentence, sanitized and cut to
// a readable length.
func (p *pinsView) quoteRule(rule string) string {
	r := ui.OneLine(rule)
	if r == "" {
		r = "(no rule)"
	}

	return `"` + ui.Truncate(r, pinNoteRuleW, p.st.theme.Glyphs.Ellipsis) + `"`
}

// noTarget explains in one line why the list offers no target for
// action at all: a history filter, no inspection yet, an unreadable
// source, or an empty list. A history list names the route back first,
// so an 80-column notice row still shows "Press f".
func (p *pinsView) noTarget(action string) string {
	in := p.inspection()

	switch {
	case p.filter != pinInstalled:
		return "No " + action + " in a history list. Press f to show installed pins."
	case in == nil:
		return "No " + action + " here. The pins are still loading."
	case !pinLinksKnown(in):
		return "No " + action + " here. The pin file or the ledger could not be read."
	default:
		return "No " + action + " here. No pin is in the list."
	}
}

// pinByRef returns the pin with binding ref in the current inspection,
// by the rule of pinMatch.
func (p *pinsView) pinByRef(ref pinfile.EntryRef) (*maintain.Pin, int, bool) {
	return inspectedPin(p.inspection(), ref)
}

// inspectedPin returns the pin with binding ref in the inspection in,
// and its position, by the rule of pinMatch. A nil inspection holds no
// pin.
func inspectedPin(in *maintain.Inspection, ref pinfile.EntryRef) (*maintain.Pin, int, bool) {
	if in == nil {
		return nil, 0, false
	}

	i, ok := pinMatch(len(in.Pins), func(i int) pinfile.EntryRef { return in.Pins[i].Entry.Ref() }, ref)
	if !ok {
		return nil, 0, false
	}

	return &in.Pins[i], i, true
}

// pinListGap names the source whose read error keeps filter f from
// listing every row, or "" when the list is complete. A row that such a
// list lacks is unknown, not gone (D-5).
func pinListGap(in *maintain.Inspection, f pinFilter) string {
	const (
		file   = "the pin file could not be read"
		ledger = "the ledger could not be read"
	)

	if in == nil {
		return "the pins are not read yet"
	}

	fileGap := in.DocErr != nil || in.Doc.Unreadable()

	switch f {
	case pinNotPresent:
		switch {
		case fileGap:
			return file
		case in.Links == nil || in.Links.NotPresentErr != nil:
			return ledger
		}
	case pinSuperseded:
		if in.LedgerErr != nil {
			return ledger
		}
	default:
		if fileGap {
			return file
		}
	}

	return ""
}

// reconcile applies a new inspection. The selection keeps only entries
// whose content stays the same. The cursor stays on the same row. A
// comparison closes when one of its pins changed. Every loss that the
// user did not cause gets a note, because a refresh must never change
// what an action targets without telling the user.
func (p *pinsView) reconcile() tea.Cmd {
	// The drafts and the removal review rebind first, so one message
	// names every change.
	notes := append(p.rebindDrafts(), p.rebindRemoval()...)

	in := p.inspection()

	if p.compare != nil && !p.rebindCompare() {
		p.compare = nil

		why := "a compared pin changed"
		if gap := pinListGap(in, pinInstalled); gap != "" {
			why = gap
		}

		notes = append(notes, "The comparison closed: "+why)
	}

	notes = append(notes, p.reconcileSelection()...)

	rows := p.rows()

	if gone, lost := p.reconcileCursor(rows); lost {
		gap := pinListGap(in, p.filter)

		switch {
		case gap != "" && len(rows) == 0:
			// A read error emptied the list, and its empty state says so.
			// The identity stays, so the next good read finds the row.
			p.cursorID = gone
		case p.acted[gone.ref] || !p.shown():
			// The user caused the change, or the list is not on screen.
		default:
			notes = append(notes, p.lossNote(gone, gap, rows))
		}
	}

	p.acted = map[pinfile.EntryRef]bool{}

	var cmds []tea.Cmd

	if p.focusID != 0 {
		cmds = append(cmds, p.focus(p.focusID))
	}

	if len(notes) > 0 {
		cmds = append(cmds, toast(ui.ToneAttention, strings.Join(notes, "; ")))
	}

	return tea.Batch(cmds...)
}

// lossNote names a cursor row that a refresh did not find, and the row
// that the cursor moved to. gap names the read error of a list that is
// not complete. The row is then unknown, not gone, so the note names the
// error instead (D-5).
func (p *pinsView) lossNote(gone pinID, gap string, rows []pinRow) string {
	note := p.pinIDLabel(gone) + " left this list"
	if gap != "" {
		note = p.pinIDLabel(gone) + " is unknown: " + gap
	}

	if cur, ok := p.cursorRow(rows); ok {
		note += "; the cursor moved to " + p.pinIDLabel(cur.ident())
	}

	return note
}

// reconcileSelection rebinds the marked entries to the new inspection.
// An entry that moved but kept its content stays marked under its new
// binding. Any other entry leaves the selection with a note: it changed,
// it left the file, or its link changed. The user marked it under what
// the list showed then.
func (p *pinsView) reconcileSelection() []string {
	if len(p.selected) == 0 {
		return nil
	}

	old := p.selected
	refs := make([]pinfile.EntryRef, 0, len(old))

	for ref := range old {
		refs = append(refs, ref)
	}

	slices.SortFunc(refs, func(a, b pinfile.EntryRef) int { return a.Index - b.Index })

	p.selected = pinMarks{}

	if !p.actionable() {
		return []string{"Selection cleared: the pin file or the ledger could not be read"}
	}

	rows := p.rows()

	var gone, relinked []string

	for _, ref := range refs {
		i, ok := pinFind(rows, pinID{ref: ref})

		switch {
		case ok && rows[i].pin.Link.Link == old[ref]:
			p.selected[rows[i].ident().ref] = old[ref]
		case p.acted[ref]:
			// The user changed this entry; the commit result says so.
		case ok:
			relinked = append(relinked, p.quoteRule(ref.Key.Rule))
		default:
			gone = append(gone, p.quoteRule(ref.Key.Rule))
		}
	}

	var notes []string

	if len(gone) > 0 {
		notes = append(notes, strings.Join(gone, ", ")+" left the selection: changed or no longer in the pin file")
	}

	if len(relinked) > 0 {
		notes = append(notes, strings.Join(relinked, ", ")+" left the selection: the link to the ledger changed")
	}

	return notes
}

// pinIDLabel names a row identity in a note: the quoted rule of a pin,
// or the pN form of a proposal row.
func (p *pinsView) pinIDLabel(id pinID) string {
	if id.ref != (pinfile.EntryRef{}) {
		return p.quoteRule(id.ref.Key.Rule)
	}

	return propID(id.id)
}

// focus puts the cursor on the pin that proposal id links to, for a link
// from an extraction result. It shows the installed pins and clears a
// filter that hides the pin, with a visible note. It never selects the
// pin and never opens a preview. Before the first inspection it waits.
func (p *pinsView) focus(id int64) tea.Cmd {
	in := p.inspection()
	if in == nil {
		p.focusID = id

		return nil
	}

	p.focusID = 0
	hasID := func(pr domain.Proposal) bool { return pr.ID == id }

	for i := range in.Pins {
		if slices.ContainsFunc(in.Pins[i].Link.Applied, hasID) {
			return p.focusRow(pinRow{pin: &in.Pins[i], n: i}, pinInstalled)
		}
	}

	if in.Links != nil {
		if i := slices.IndexFunc(in.Links.NotPresent, hasID); i >= 0 {
			return p.focusRow(pinRow{prop: in.Links.NotPresent[i]}, pinNotPresent)
		}
	}

	return toast(ui.ToneNeutral, "No installed pin links "+propID(id)+" now.")
}

// focusRow shows row under filter f and puts the cursor on it. A filter
// that hides the row is cleared, and one note names every change, so
// the user sees why the list looks different.
func (p *pinsView) focusRow(row pinRow, f pinFilter) tea.Cmd {
	p.focusDetail = false

	var hidden bool
	if row.pin != nil {
		hidden = !p.matchesPin(row.pin)
	} else {
		hidden = !p.matchesProp(row.prop)
	}

	changed := p.filter != f || hidden
	p.filter = f
	p.cursorID = row.ident()

	if !changed {
		p.relocate(p.rows(), false)

		return nil
	}

	var notes []string

	if hidden {
		p.region, p.text = "", ""
		notes = append(notes, "Filters cleared to show "+p.pinIDLabel(row.ident()))
	}

	if note := p.applyFilter(); note != "" {
		notes = append(notes, note)
	}

	if len(notes) == 0 {
		return nil
	}

	return toast(ui.ToneAttention, strings.Join(notes, "; "))
}

// keys returns the keys of the open pin form or review, of the open
// preview, of the focused input or the comparison, or of the list.
//
// The action keys always show (user decision of 2026-10-04: "Always
// list, mark unavailable"). An action that no target can use is an
// unavailable entry, so the user can still find its key. The press then
// explains the refusal and opens nothing. The actions keep the order
// space n i d - w t v (contracts §4.2): the authoring keys first, then
// the maintenance keys. The available actions lead and the n/a group
// follows. A footer that is too narrow for every hint shows the n/a
// group, and then the keys at the end, by their keys only. So the
// 120-column footer holds every action key while one of them is n/a.
// With all seven on, it cuts from the end as other footers do, and
// "? help" lists them. A list without a
// selectable pin offers no target, so its list keys come first and the
// n/a group follows them: f is the route out of a history list. Lessons
// adds its switch keys at the end.
func (p *pinsView) keys() []key.Binding {
	if p.author.open() {
		return p.authorBindings()
	}

	if p.host.open() {
		return p.host.keys()
	}

	switch {
	case p.editing != filterNone:
		return p.editKeys()
	case p.compare != nil:
		return p.compareKeys()
	}

	in := p.inspection()
	rows := p.rows()
	cur, hasRow := p.cursorRow(rows)

	sel := ui.WithHelp(ui.Nav.Toggle, "space", "select")
	sel.SetEnabled(hasRow && cur.pin != nil && p.actionable())

	var on, off []key.Binding

	// target is true when a key acts on the cursor pin or the marks. n
	// needs no target, because a new pin binds no entry. i without a pin
	// row opens an orphaned draft (editPin), so it has no target either.
	target := false

	// t reads "extract" here, as in the help. With two or more keys n/a,
	// the short label keeps t with its action in the 120-column footer.
	for _, a := range []struct {
		b          key.Binding
		ok, target bool
	}{
		{pinKeys.New, true, false},
		{pinKeys.Edit, p.authorOffers(maintain.AuthorEdit), p.authorRow()},
		{pinKeys.Remove, p.authorOffers(maintain.AuthorRemove), true},
		{pinKeys.Prune, p.offers(p.editBlock), true},
		{pinKeys.Retarget, p.offers(p.retargetBlock), true},
		{ui.WithHelp(extractKey, "t", "extract"), p.offers(pinExtractBlock), true},
		{pinKeys.Compare, hasRow && cur.pin != nil && p.compareBlock(cur.pin) == "", true},
	} {
		switch {
		case a.ok:
			on = append(on, a.b)
			target = target || a.target
		default:
			off = append(off, ui.Unavailable(a.b))
		}
	}

	// The label names what x clears, because the help lists x also when
	// nothing is selected.
	clearSel := ui.WithHelp(lesKeys.Clear, "x", "clear marks")
	clearSel.SetEnabled(len(p.selected) > 0)

	loaded := in != nil

	show := ui.WithHelp(lesKeys.State, "f", "show")
	show.SetEnabled(loaded)

	region := ui.WithHelp(lesKeys.Region, "/", "region")
	region.SetEnabled(loaded)

	search := pinKeys.Search
	search.SetEnabled(loaded)

	list := []key.Binding{p.paneKey(hasRow), show, region, search}

	if sel.Enabled() || target {
		return slices.Concat([]key.Binding{sel}, on, off, list, []key.Binding{clearSel})
	}

	return slices.Concat([]key.Binding{sel}, on, list, off, []key.Binding{clearSel})
}

// helpKeys returns every key of the current state for the help. In the
// list, each key that does not work now is an unavailable entry, so the
// help names every Pins key and marks the ones that wait for a target.
func (p *pinsView) helpKeys() []key.Binding {
	if p.capturing() {
		return p.keys()
	}

	return ui.ListAll(p.keys())
}
