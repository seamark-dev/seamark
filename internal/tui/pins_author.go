package tui

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the authoring of pins in Lessons › Pins (contracts
// §4.2): the drafts, the pin form, the pin review and its commit. The
// rendering lives in pins_author_view.go.
//
// The keys n, i and d act on the cursor row only. They never read the
// marks or targets(), because an authoring operation changes one entry
// that the user chose (R-4). The marks stay as they are.
//
// The form drafts a create or an edit. It holds the typed values and
// sends them to maintain.CheckDraft on every render, so the form shows
// the verdicts of the planner live. A field counts as set only when its
// value differs from the entry, so an untouched legacy text never meets
// a new check (lossless load).
//
// The review is a previewFlow holder. It plans in a command, commits
// under the mutation slot as registered work, and shows the outcome of
// the one file write (P4-18). It never retries a write: a refusal or an
// unknown outcome offers only a fresh review.
//
// The drafts live in the session only. One new-pin draft and one edit
// draft per entry exist. An edit draft follows its entry across a
// reload by its content, never by its position (rebindDrafts).

// authorKeys are the keys of the pin form and the pin review besides
// the shared keys of ui.Nav, lesKeys.Confirm and maintKeys.Settings.
var authorKeys = struct {
	Review, Add, Discard, DiscardYes, Copy key.Binding
}{
	Review:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "review")),
	Add:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add region")),
	Discard:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "discard")),
	DiscardYes: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "discard")),
	Copy:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
}

// The words of a read-only row (contracts §4.2). A row that the editor
// cannot hold exactly stays read-only, because an edit would change
// characters that the user never touched. pinByHand ends the reason of
// every row that its stored text locks (pinStoredLock), because only a
// hand edit of the file can change that text.
const (
	pinByHand      = "; edit " + pinfile.File + " by hand"
	pinTextLock    = "this text holds characters the editor cannot show" + pinByHand
	pinUnknownNote = "relationship unknown: a linked pin allows only a note edit; saving needs a readable index of " +
		"this workspace"
	pinRegionsHelp = "A region matches that file, or everything under that directory; no wildcards."
	pinMarksNote   = "Marks are ignored: this acts on the cursor pin only, and the marks stay."
)

// pinField names the text field of the form that has the keys.
type pinField int

const (
	// pinFieldNone means that the rows have the keys.
	pinFieldNone pinField = iota
	pinFieldRule
	pinFieldNote
	pinFieldRegion
)

// The fixed rows of the form. The region rows follow them, and the row
// after the last region adds one.
const (
	pinRowRule = iota
	pinRowNote
	pinRowRepoWide
	pinRowRegions
	pinRowFirstRegion
)

// pinDraft is one unsaved authoring draft: a create or an edit. A
// removal has no draft, because it has no field to type.
type pinDraft struct {
	op maintain.AuthorOp
	// ref binds an edit to its entry, as the last inspection read it. It
	// follows a moved or changed entry (rebindDrafts).
	ref pinfile.EntryRef
	// base is the decoded entry that ref binds; zero for a create.
	base reviews.PinRule

	rule    ui.LineInput
	note    ui.TextArea
	scope   maintain.ScopeChoice
	regions []string

	// ruleLock, noteLock and scopeLock say why a row is read-only, or "".
	ruleLock, noteLock, scopeLock string
	// unknown is true while the inspection cannot tell the relationship.
	unknown bool
	// changed is true after a rebind by key alone: the entry changed
	// since the draft started.
	changed bool
	// orphan says why the draft lost its entry, or "". An orphaned draft
	// offers copy and discard only.
	orphan string
}

// newCreateDraft returns an empty new-pin draft. The scope starts
// unset, because a blank form must never read as repository-wide.
func newCreateDraft() *pinDraft {
	return &pinDraft{op: maintain.AuthorCreate, rule: pinRuleInput(), note: pinNoteArea()}
}

// newEditDraft returns the edit draft of pin. known reports whether the
// inspection knows the relationship of the pin.
func newEditDraft(pin *maintain.Pin, known bool) *pinDraft {
	d := &pinDraft{op: maintain.AuthorEdit}
	d.load(pin, known)

	return d
}

// pinRuleInput returns the rule input. It refuses a paste with line
// breaks and an input past the limit, so a rule never changes silently.
func pinRuleInput() ui.LineInput {
	return ui.LineInput{
		Placeholder: "a short label, for example wrap-sql-errors", RefuseMultiline: true, RefuseOverLimit: true,
	}
}

// pinNoteArea returns the note input.
func pinNoteArea() ui.TextArea {
	return ui.TextArea{Placeholder: "the guidance that an agent reads"}
}

// pinBaseScope returns the scope choice and the region rows of an entry.
// An entry without a region is repository-wide.
func pinBaseScope(p reviews.PinRule) (scope maintain.ScopeChoice, regions []string) {
	set := p.AllRegions()
	if len(set) == 0 {
		return maintain.ScopeRepoWide, nil
	}

	return maintain.ScopeRegions, set
}

// pinRefusedRune reports a character that the shared text check refuses
// in a note: a control, a line or paragraph separator, or a bidi mark.
// A line break and a tab are note text. One predicate decides the
// read-only rows and the U+XXXX escapes, so no such character shows raw.
func pinRefusedRune(r rune) bool {
	return r != '\n' && r != '\t' && reviews.CheckAuthoredText(reviews.TextPinNote, string(r)) != nil
}

// pinNoteOnly reports a pin that allows a note edit only: a linked pin
// whose relationship the authoring keys know (contracts §4.2). load
// locks its rule and scope, and follow drops their edits.
func pinNoteOnly(pin *maintain.Pin, known bool) bool {
	return known && pinLinked(pin)
}

// pinEditable reports a base value that the form can edit: the widget
// holds it exactly, and it has no character that the text check refuses.
// Holds already refuses the hidden runes of the shared check. The second
// test keeps the lock equal to the shared check if that check refuses
// more later (contracts §4.2).
func pinEditable(s string, holds func(string) bool) bool {
	return holds(s) && !strings.ContainsFunc(s, pinRefusedRune)
}

// pinLengthLock returns the reason of a row whose stored text is longer
// than limit runes, the bound of its input (ui.LineInput.MaxRunes). The
// input refuses every edit of such a text, because an edit could only
// shorten it by a cut.
func pinLengthLock(limit int) string {
	return "this text is longer than " + strconv.Itoa(limit) + " characters" + pinByHand
}

// pinInputLock puts the stored text s into in and returns why in cannot
// edit it, or "". A refused character comes first, because a shorter
// text would still stay read-only. The length check is the check of the
// input itself (ui.LineInput.ReadOnly), so the form never copies the
// bound.
func pinInputLock(in *ui.LineInput, s string) string {
	in.Set(s)

	switch {
	case !pinEditable(s, in.Holds):
		return pinTextLock
	case in.ReadOnly():
		return pinLengthLock(in.MaxRunes())
	}

	return ""
}

// pinStoredLock reports a lock that load sets from the stored text of a
// row (pinInputLock), not from the link of the pin. A typed value
// replaces the stored text, so follow removes such a lock.
func pinStoredLock(lock string) bool {
	return strings.HasSuffix(lock, pinByHand)
}

// load puts the entry of pin into the draft as its base and its values.
// Each widget gets the raw text only when it can edit it (pinEditable),
// so an untouched field compares equal to the entry. Any other field is
// read-only, and its lock names the reason (pinInputLock). A linked pin
// allows a note edit only.
func (d *pinDraft) load(pin *maintain.Pin, known bool) {
	e := pin.Entry
	d.ref = e.Ref()
	d.base = reviews.PinRule{Rule: e.Rule, Region: e.Region, Regions: e.Regions, Note: e.Note}
	d.unknown = !known
	d.ruleLock, d.noteLock, d.scopeLock = "", "", ""

	d.rule = pinRuleInput()
	d.ruleLock = pinInputLock(&d.rule, e.Rule)

	d.note = pinNoteArea()
	if pinEditable(e.Note, d.note.Holds) {
		d.note.SetValue(e.Note)
	} else {
		d.noteLock = pinTextLock
	}

	d.scope, d.regions = pinBaseScope(d.base)

	// The probe is the input that edits a region (startRegion). So the
	// form names the reason when it opens, not after the first edit key.
	// One lock covers every region row. So the loop checks every region
	// and keeps the order of pinInputLock: a refused character in any
	// region comes before a length.
	probe := pathInput()

	for _, r := range d.regions {
		lock := pinInputLock(&probe, r)
		if lock == pinTextLock {
			d.scopeLock = lock

			break
		}

		if d.scopeLock == "" {
			d.scopeLock = lock
		}
	}

	if pinNoteOnly(pin, known) {
		// The rule is the recorded rule of the proposal, so a new rule
		// is a new pin. The scope has its own route, the retarget.
		linked := "linked to proposal #" + strconv.FormatInt(pin.Link.Applied[0].ID, 10)
		d.ruleLock = linked + "; to change the rule, use - Prune and add a new pin with n"
		d.scopeLock = linked + "; change the scope with w Retarget"
	}
}

// ruleValue returns the rule of the draft.
func (d *pinDraft) ruleValue() string {
	return d.rule.Value()
}

// noteValue returns the note of the draft. A note that the text area
// cannot hold stays the raw entry note, because the area would drop
// some of its characters.
func (d *pinDraft) noteValue() string {
	if d.noteLock == pinTextLock {
		return d.base.Note
	}

	return d.note.Value()
}

// request returns the authoring request of the draft. An edit sets a
// field only when its value differs from the entry, so the planner
// never sees an untouched field (lossless load).
func (d *pinDraft) request() maintain.AuthorRequest {
	req := maintain.AuthorRequest{
		Op: d.op, Ref: d.ref, Rule: d.ruleValue(), Note: d.noteValue(), Scope: d.scope,
		Regions: slices.Clone(d.regions),
	}

	if d.op == maintain.AuthorEdit {
		scope, regions := pinBaseScope(d.base)
		req.RuleSet = req.Rule != d.base.Rule
		req.NoteSet = req.Note != d.base.Note
		req.ScopeSet = d.scope != scope || !slices.Equal(d.regions, regions)
	}

	return req
}

// issues returns the verdicts of the shared draft check.
func (d *pinDraft) issues() []maintain.FieldIssue {
	var base *reviews.PinRule
	if d.op == maintain.AuthorEdit {
		base = &d.base
	}

	return maintain.CheckDraft(d.request(), base)
}

// dirty reports a draft that holds a change. Only such a draft counts
// as unsaved work, so a form that the user opens and closes leaves no
// draft behind.
func (d *pinDraft) dirty() bool {
	if d.op == maintain.AuthorCreate {
		return d.ruleValue() != "" || d.noteValue() != "" || d.scope != maintain.ScopeUnset || len(d.regions) > 0
	}

	req := d.request()

	return req.RuleSet || req.NoteSet || req.ScopeSet
}

// follow binds the draft to pin after a reload. how is the kind of the
// match (pinDraftMatch). Every field keeps its draft value when the user
// edited it, and takes the current value otherwise. A field that keeps
// its value also keeps its widget, so the cursor, the scroll and a
// notice stay where the user left them. A change by key alone marks the
// draft as changed, so the review shows the current value next to the
// draft (contracts §4.2).
//
// A pin that the reload shows as linked allows a note edit only. So its
// rule and scope edits go away, because the rows are read-only now and
// the review would refuse them. follow returns the names of the fields
// that lost an edit, for the message of reconcile.
func (d *pinDraft) follow(pin *maintain.Pin, known bool, how pinRebind) (dropped []string) {
	old := d.base
	oldScope, oldRegions := pinBaseScope(old)
	ruleW, noteW := d.rule, d.note

	ruleEdited := d.ruleValue() != old.Rule
	noteEdited := d.noteValue() != old.Note
	scope, regions := d.scope, slices.Clone(d.regions)
	scopeEdited := scope != oldScope || !slices.Equal(regions, oldRegions)

	d.load(pin, known)

	if how == rebindChanged {
		d.changed = true
	}

	linked := pinNoteOnly(pin, known)

	// An edited value is typed text, so the widgets hold it. A lock of
	// the stored text of the current entry (pinStoredLock), for its
	// characters or its length, then no longer applies to the field.
	switch {
	case ruleEdited && linked:
		dropped = append(dropped, "rule")
	case ruleEdited:
		d.rule = ruleW

		if pinStoredLock(d.ruleLock) {
			d.ruleLock = ""
		}
	case ruleW.Value() == d.rule.Value():
		d.rule = ruleW
	}

	switch {
	case noteEdited:
		d.note = noteW

		if pinStoredLock(d.noteLock) {
			d.noteLock = ""
		}
	case noteW.Value() == d.note.Value():
		d.note = noteW
	}

	switch {
	case scopeEdited && linked:
		dropped = append(dropped, "scope")
	case scopeEdited:
		d.scope, d.regions = scope, regions

		if pinStoredLock(d.scopeLock) {
			d.scopeLock = ""
		}
	}

	return dropped
}

// pinRebind is the kind of match of an edit draft after a reload.
type pinRebind int

const (
	// rebindSame finds the entry at its binding.
	rebindSame pinRebind = iota
	// rebindMoved finds the entry by its key and its own lines at
	// another position.
	rebindMoved
	// rebindChanged finds the entry by its key alone: its lines changed.
	rebindChanged
	// rebindOrphan finds no unique entry.
	rebindOrphan
)

// pinDraftMatch returns the position of the entry that an edit draft
// with binding ref edits now, and the kind of the match (contracts
// §4.2). The key and the own lines must match exactly one entry; else
// the key alone must match exactly one entry. The position alone never
// counts, because another pin can sit at that position now. So the
// rule differs from pinMatch, which accepts an exact binding among two
// equal entries: two equal entries cannot tell which one the draft
// edits.
func pinDraftMatch(pins []maintain.Pin, ref pinfile.EntryRef) (int, pinRebind) {
	var same, byKey []int

	for i := range pins {
		r := pins[i].Entry.Ref()
		if r.Key != ref.Key {
			continue
		}

		byKey = append(byKey, i)

		if r.RawSHA == ref.RawSHA {
			same = append(same, i)
		}
	}

	switch {
	case len(same) == 1 && pins[same[0]].Entry.Ref() == ref:
		return same[0], rebindSame
	case len(same) == 1:
		return same[0], rebindMoved
	case len(same) == 0 && len(byKey) == 1:
		return byKey[0], rebindChanged
	}

	return -1, rebindOrphan
}

// pinForm is the open pin form of one draft.
type pinForm struct {
	draft *pinDraft
	// row is the cursor row.
	row int
	// field is the text field that has the keys.
	field pinField
	// region is the input of a region row; regionAt is the region that it
	// replaces, or -1 for a new region.
	region   ui.LineInput
	regionAt int
	// prompt is the open question before x discards the draft.
	prompt bool
	// sc is the position of the form box.
	sc ui.ScrollState
}

// newPinForm opens the form of d at its first row.
func newPinForm(d *pinDraft) *pinForm {
	return &pinForm{draft: d, regionAt: -1}
}

// rows returns the number of rows: the fixed rows, the regions and the
// row that adds a region.
func (f *pinForm) rows() int {
	return pinRowFirstRegion + len(f.draft.regions) + 1
}

// regionIndex returns the region of the cursor row, or -1.
func (f *pinForm) regionIndex() int {
	if i := f.row - pinRowFirstRegion; i >= 0 && i < len(f.draft.regions) {
		return i
	}

	return -1
}

// addRow returns the row that adds a region.
func (f *pinForm) addRow() int {
	return pinRowFirstRegion + len(f.draft.regions)
}

// pinAuthor holds the authoring state of the Pins sub-view: the drafts,
// the open form and the open review.
type pinAuthor struct {
	newDraft *pinDraft
	// edits holds the edit drafts by the binding of their entry.
	edits map[pinfile.EntryRef]*pinDraft
	// orphans holds the edit drafts that lost their entry.
	orphans []*pinDraft

	form   *pinForm
	review *pinReview
}

// open reports an open form or review. Either takes every key.
func (a *pinAuthor) open() bool {
	return a.form != nil || a.review != nil
}

// drop forgets the draft d.
func (a *pinAuthor) drop(d *pinDraft) {
	if a.newDraft == d {
		a.newDraft = nil
	}

	for ref, e := range a.edits {
		if e == d {
			delete(a.edits, ref)
		}
	}

	a.orphans = slices.DeleteFunc(a.orphans, func(o *pinDraft) bool { return o == d })

	if a.form != nil && a.form.draft == d {
		a.form = nil
	}
}

// all returns every draft: the new-pin draft, the edit drafts in file
// order, then the orphaned drafts.
func (a *pinAuthor) all() []*pinDraft {
	var out []*pinDraft

	if a.newDraft != nil {
		out = append(out, a.newDraft)
	}

	out = append(out, pinEditDrafts(a.edits)...)

	return append(out, a.orphans...)
}

// pinEditDrafts returns the edit drafts in the order of their entries,
// so every reload treats them in one stable order.
func pinEditDrafts(edits map[pinfile.EntryRef]*pinDraft) []*pinDraft {
	out := make([]*pinDraft, 0, len(edits))
	for _, d := range edits {
		out = append(out, d)
	}

	slices.SortFunc(out, func(a, b *pinDraft) int { return a.ref.Index - b.ref.Index })

	return out
}

// pinReview is the review of one authoring operation. The embedded flow
// holds the stage and the shown plan. The review keeps its own
// generation. Each review is a new value, so a late plan of an earlier
// review must not reach it. The plan message also names its review.
type pinReview struct {
	previewFlow[maintain.AuthorPlan]

	st  *state
	req maintain.AuthorRequest
	// draft is the draft of a create or an edit; nil for a removal.
	draft *pinDraft
	seq   generation
	// marks is true when the Pins list had marks at the open. The review
	// then says that the marks are ignored.
	marks bool
	// where names the entry, for example "pin 3 at line 12"; "" for a
	// create.
	where string
	// sample is the open selection sample of the plan, or nil. It takes
	// every key while it is open, and esc returns to the review.
	sample *sample

	out *pinOutcome
}

// pinOutcome is the last commit outcome that the result shows.
type pinOutcome struct {
	plan *maintain.AuthorPlan
	res  maintain.AuthorResult
	// err is a refusal before the write: nothing was written.
	err error
	// check watches the reload after a write that landed; nil for every
	// other outcome (P4-18).
	check *refreshCheck
}

// pinPlannedMsg carries a plan read. r names the review that asked:
// authorRoute drops the plan of a review that is no longer open. seq
// drops the result of a read that the review left or replaced.
type pinPlannedMsg struct {
	r    *pinReview
	seq  int
	plan *maintain.AuthorPlan
	err  error
}

// pinCommittedMsg carries the outcome of a commit. plan is the plan
// that the user confirmed.
type pinCommittedMsg struct {
	r    *pinReview
	plan *maintain.AuthorPlan
	res  maintain.AuthorResult
	err  error
}

// newPinReview opens the review of req. It reads nothing; prepare does.
func newPinReview(st *state, req maintain.AuthorRequest, d *pinDraft, marks bool, where string) *pinReview {
	return &pinReview{st: st, req: req, draft: d, marks: marks, where: where}
}

// prepare starts the plan read. A review of a draft reads the draft
// again, so a fresh review sends its current values and binding.
//
// A removal has no draft that a reload rebinds (rebindDrafts). So it
// finds its entry again by the rule of pinMatch: an entry that moved
// with the same key and lines is the same pin. Without a match, the old
// binding stays, and the plan names the entry as gone or changed.
//
// The position text follows the new binding of an edit and of a
// removal, so the review names the entry that the plan binds. Without a
// match, the old text stays, as the old binding does.
func (r *pinReview) prepare() tea.Cmd {
	seq := r.seq.start()
	r.startRead()

	in := r.st.pinInspection()
	pin, _, found := inspectedPin(in, r.req.Ref)

	switch {
	case r.draft != nil:
		r.req = r.draft.request()
	case found:
		r.req.Ref = pin.Entry.Ref()
	}

	if r.req.Op != maintain.AuthorCreate {
		if where := pinWhereIn(in, r.req.Ref); where != "" {
			r.where = where
		}
	}

	req := r.req

	var svc services.Maintenance = r.st.svc

	return func() tea.Msg {
		plan, err := svc.PlanAuthor(req)

		return pinPlannedMsg{r: r, seq: seq, plan: plan, err: err}
	}
}

// planned shows a plan read, unless the user left the read.
func (r *pinReview) planned(msg pinPlannedMsg) {
	r.readDone(r.seq.current(msg.seq), msg.plan, msg.err, "the review came back empty")
}

// confirmable reports a review that y commits now: a confirmable plan
// and a free mutation slot.
func (r *pinReview) confirmable() bool {
	return r.showsPreview() && r.preview.Confirmable && !r.st.busy()
}

// routes reports a blocker that o can open (A4 hand-off: route by
// BlockKind). The ledger and the root lead to the index build. The gate
// leads to Settings only while it reads, because a gate that does not
// read has no setting to change.
func (r *pinReview) routes(b maintain.AuthorBlocker) bool {
	switch b.Kind {
	case maintain.BlockLedger, maintain.BlockForeignRoot:
		return true
	case maintain.BlockGate:
		return r.preview.GateErr == nil
	}

	return false
}

// routed returns the index of the blocker that o opens, or -1. The first
// routed blocker in plan order wins, and the planner lists the ledger
// and the root before the gate, so the index build leads.
func (r *pinReview) routed() int {
	if !r.showsPreview() {
		return -1
	}

	return slices.IndexFunc(r.preview.Blockers, r.routes)
}

// route returns the destination that o opens, its words, and whether a
// blocker has one.
func (r *pinReview) route() (dest destination, intent, words string, ok bool) {
	i := r.routed()

	switch {
	case i < 0:
		return 0, "", "", false
	case r.preview.Blockers[i].Kind == maintain.BlockGate:
		return destSettings, "group:pinwrite", "Settings " + r.st.theme.Glyphs.Arrow + " Pin writing", true
	}

	return destOverview, ovIntentBuild, "Overview " + r.st.theme.Glyphs.Arrow + " build", true
}

// reviewAgain reports a result that a fresh review can resolve: every
// outcome but a written file. An orphaned draft opens no review.
func (r *pinReview) reviewAgain() bool {
	if r.out == nil || (r.draft != nil && r.draft.orphan != "") {
		return false
	}

	return r.out.err != nil || r.out.res.Outcome != maintain.Written
}

// key handles a key of the current stage. A review that closes reaches
// flowClosed, and the Pins sub-view then drops it (afterReview).
func (r *pinReview) key(msg tea.KeyMsg) tea.Cmd {
	switch r.stage {
	case flowPreparing:
		if key.Matches(msg, ui.Nav.Back) {
			// The read may still finish; seq drops its result.
			r.seq.cancel()
			r.close()
		}
	case flowPreview:
		return r.previewKey(msg)
	case flowCommitting:
		// The commit cannot stop safely. Keys wait for the result.
	case flowResult:
		return r.resultKey(msg)
	}

	return nil
}

// previewKey handles the review. esc closes it with no change: the form
// of a draft shows again, and a removal returns to the list. Only y
// commits, and only a confirmable plan. An open sample takes every key
// first.
func (r *pinReview) previewKey(msg tea.KeyMsg) tea.Cmd {
	if r.sample != nil {
		cmd, closed := r.sample.key(msg)
		if closed {
			r.sample = nil
		}

		return cmd
	}

	switch {
	case key.Matches(msg, ui.Nav.Back):
		r.close()
	case key.Matches(msg, lesKeys.Confirm):
		// A y during another operation does nothing. The review already
		// says that y waits.
		if r.confirmable() {
			return r.commit()
		}
	case key.Matches(msg, maintKeys.Settings):
		// A review of the old gate or of no index never becomes
		// confirmable, so it closes; s builds a new one later.
		if dest, intent, _, ok := r.route(); ok {
			r.close()

			return navigate(dest, intent)
		}
	case key.Matches(msg, sampleKeys.Open) && r.samples():
		// The sample reads the bytes of this plan only (contracts §4.4).
		var cmd tea.Cmd

		r.sample, cmd = newSample(r.st, r.preview.Before, r.preview.After, false)

		return cmd
	default:
		r.scroll.Key(msg)
	}

	return nil
}

// samples reports a plan that s can sample: a shown plan with a
// candidate. A removal plan has a candidate too; a plan without one has
// nothing to compare.
func (r *pinReview) samples() bool {
	return r.showsPreview() && r.preview.After != nil
}

// resultKey handles the result. enter reviews again after an outcome
// that wrote nothing or that is unknown; it never repeats the commit. r
// reads the sources of the commit again (pinWriteRefresh) while the
// result shows a failed reload.
func (r *pinReview) resultKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, gkeys.Refresh) && r.out != nil && r.out.check.shows():
		var cmd tea.Cmd

		r.out.check, cmd = r.st.readAgain(r.out.check, pinWriteRefresh...)

		return cmd
	case key.Matches(msg, ui.Nav.Enter) && r.reviewAgain():
		return r.prepare()
	case key.Matches(msg, ui.Nav.Enter, ui.Nav.Back):
		r.close()
	default:
		r.scroll.Key(msg)
	}

	return nil
}

// commit claims the mutation slot and runs the confirmed plan as
// registered work, so a signal exit waits for the write.
func (r *pinReview) commit() tea.Cmd {
	plan := r.preview

	var svc services.Maintenance = r.st.svc

	cmd, ok := r.startCommit(r.st, pinWorkName(plan.Request.Op), r.st.goWork(func() tea.Msg {
		res, err := svc.CommitAuthor(plan)

		return pinCommittedMsg{r: r, plan: plan, res: res, err: err}
	}))
	if !ok {
		return toast(ui.ToneAttention, "Another operation is running: "+r.st.mutation)
	}

	// The wait starts at the top, as the maintenance commit does.
	r.scroll = ui.ScrollState{}

	return cmd
}

// pinWorkName is the mutation name of a commit. The notice row and the
// quit question show it.
func pinWorkName(op maintain.AuthorOp) string {
	switch op {
	case maintain.AuthorRemove:
		return "Removing a pin"
	case maintain.AuthorEdit:
		return "Editing a pin"
	default:
		return "Writing a new pin"
	}
}

// reopen names the key that opens a new review: s in the pin form of a
// draft, or d in Pins for a removal. The text names the screen, because
// a toast can show on another screen where the key means something else.
func (r *pinReview) reopen() string {
	if r.draft == nil {
		return "press d in Pins to review it again"
	}

	return "press s in the pin form to review again"
}

// lessonsSaved closes a review that reads or shows a plan after a write
// of lessons.yaml (lessonsBound), because the plan read the old bytes.
// The draft stays; a result stays too.
func (r *pinReview) lessonsSaved(msg lessonsSavedMsg) tea.Cmd {
	return r.closeStale(lessonsChanged(msg) + ", so the pin review closed. " + ovSentence(r.reopen()) + ".")
}

// settingsSaved closes a review before its commit after a save of
// Settings › Pin writing (contracts P4-12). The plan read the old gate.
// Other groups do not bind the plan.
func (r *pinReview) settingsSaved(msg settingsSavedMsg) tea.Cmd {
	if msg.group != settings.GroupPinWrite {
		return nil
	}

	return r.closeStale("Settings " + r.st.theme.Glyphs.Arrow + " Pin writing changed; " + r.reopen() + ".")
}

// closeStale closes a review before its commit and returns the message
// that says so. A commit and a result stay, because they no longer
// depend on the plan.
func (r *pinReview) closeStale(text string) tea.Cmd {
	if !r.beforeCommit() {
		return nil
	}

	// The sample read the same old bytes, so it closes with the review.
	r.seq.cancel()
	r.sample = nil
	r.close()

	return toast(ui.ToneAttention, text)
}

// authorRoute gives msg to the authoring state of the Pins sub-view. It
// reports done when the sub-view must not handle msg as well. Such a
// message is a plan or a commit of a review, a sample read, or a key
// while a form or a review is open.
// A sample read goes only to the open sample of the open review. A load
// of the index, the ledger or the pins also reads that sample again.
// A save of lessons.yaml or of the gate also reaches the other parts of
// the sub-view, so it is not done here. A new inspection rebinds the
// drafts in reconcile (rebindDrafts).
func (p *pinsView) authorRoute(msg tea.Msg) (tea.Cmd, bool) {
	a := &p.author

	switch m := msg.(type) {
	case pinPlannedMsg:
		// A review that closed is detached, so its late plan changes
		// nothing.
		if a.review != nil && a.review == m.r {
			m.r.planned(m)
		}

		return nil, true
	case pinCommittedMsg:
		return p.authorCommitted(m), true
	case sampledMsg:
		// Only the open sample of the open review takes its read.
		if r := a.review; r != nil && r.sample == m.owner {
			m.owner.done(m)
		}

		return nil, true
	case loadedMsg:
		// A new snapshot of the evidence reads the open sample again. The
		// rest of the sub-view handles the load too.
		if r := a.review; r != nil && r.sample != nil {
			return r.sample.reload(m), false
		}
	case tea.KeyMsg:
		if !a.open() {
			return nil, false
		}

		return p.authorKey(m), true
	case lessonsSavedMsg:
		if a.review == nil {
			return nil, false
		}

		return p.afterReview(a.review.lessonsSaved(m)), false
	case settingsSavedMsg:
		if a.review == nil {
			return nil, false
		}

		return p.afterReview(a.review.settingsSaved(m)), false
	}

	return nil, false
}

// afterReview drops a review that closed and passes cmd on.
func (p *pinsView) afterReview(cmd tea.Cmd) tea.Cmd {
	if r := p.author.review; r != nil && !r.active() {
		p.author.review = nil
	}

	return cmd
}

// authorKey routes a key to the open review, the discard question, or
// the form.
func (p *pinsView) authorKey(msg tea.KeyMsg) tea.Cmd {
	a := &p.author

	switch {
	case a.review != nil:
		return p.afterReview(a.review.key(msg))
	case a.form.prompt:
		return p.promptKey(msg)
	case a.form.field != pinFieldNone:
		a.form.fieldKey(msg)

		return nil
	case a.form.draft.orphan != "":
		return p.orphanKey(msg)
	default:
		return p.formKey(msg)
	}
}

// formKey handles the rows of the form. The form takes every key, so
// the global keys, the Lessons switch keys and the Pins keys never act
// under it (contracts §4.2).
func (p *pinsView) formKey(msg tea.KeyMsg) tea.Cmd {
	f := p.author.form

	switch {
	case key.Matches(msg, ui.Nav.Back):
		p.closeForm()
	case key.Matches(msg, ui.Nav.Up):
		f.row = max(0, f.row-1)
	case key.Matches(msg, ui.Nav.Down):
		f.row = min(f.rows()-1, f.row+1)
	case key.Matches(msg, ui.Nav.Top):
		f.row = 0
	case key.Matches(msg, ui.Nav.Bottom):
		f.row = f.rows() - 1
	case key.Matches(msg, authorKeys.Review):
		return p.reviewDraft()
	case key.Matches(msg, authorKeys.Add):
		return f.startRegion(-1)
	case key.Matches(msg, authorKeys.Discard):
		if i := f.regionIndex(); i >= 0 {
			return f.removeRegion(i)
		}

		f.prompt = true
	case key.Matches(msg, ui.Nav.Enter, ui.Nav.Toggle):
		return f.activate(key.Matches(msg, ui.Nav.Toggle))
	}

	return nil
}

// activate is enter on a row: it opens the text field of the row or
// chooses a scope. space only chooses a scope, because it is a typed
// character in a field.
func (f *pinForm) activate(space bool) tea.Cmd {
	d := f.draft
	refused := func(lock string) tea.Cmd { return toast(ui.ToneNeutral, "Read-only: "+ui.OneLine(lock)+".") }

	switch row := f.row; {
	case row == pinRowRepoWide || row == pinRowRegions:
		if d.scopeLock != "" {
			return refused(d.scopeLock)
		}

		d.scope = maintain.ScopeRegions
		if row == pinRowRepoWide {
			d.scope = maintain.ScopeRepoWide
		}
	case space:
	case row == pinRowRule && d.ruleLock != "":
		return refused(d.ruleLock)
	case row == pinRowRule:
		f.field = pinFieldRule
	case row == pinRowNote && d.noteLock != "":
		return refused(d.noteLock)
	case row == pinRowNote:
		f.field = pinFieldNote
	default:
		return f.startRegion(f.regionIndex())
	}

	return nil
}

// startRegion opens the input of region i, or of a new region for -1.
func (f *pinForm) startRegion(i int) tea.Cmd {
	d := f.draft
	if d.scopeLock != "" {
		return toast(ui.ToneNeutral, "Read-only: "+ui.OneLine(d.scopeLock)+".")
	}

	f.region = pathInput()
	f.regionAt = i
	f.field = pinFieldRegion
	f.row = f.addRow()

	if i >= 0 {
		f.region.Set(d.regions[i])
		f.row = pinRowFirstRegion + i
	}

	return nil
}

// removeRegion removes region i. The draft keeps the scope choice,
// because the choice is a separate decision of the user.
func (f *pinForm) removeRegion(i int) tea.Cmd {
	d := f.draft
	if d.scopeLock != "" {
		return toast(ui.ToneNeutral, "Read-only: "+ui.OneLine(d.scopeLock)+".")
	}

	d.regions = slices.Delete(d.regions, i, i+1)
	f.row = min(f.row, f.rows()-1)

	return nil
}

// fieldKey gives a key to the focused text field. esc leaves the field
// and keeps the text. enter also leaves a one-line field; in the note
// it adds a line break.
func (f *pinForm) fieldKey(msg tea.KeyMsg) {
	d := f.draft
	done := key.Matches(msg, ui.Nav.Back) || (f.field != pinFieldNote && key.Matches(msg, ui.Nav.Enter))

	switch {
	case done && f.field == pinFieldRegion:
		f.keepRegion()
	case done:
		f.field = pinFieldNone
	case f.field == pinFieldRule:
		d.rule.Key(msg)
	case f.field == pinFieldNote:
		d.note.Key(msg)
	default:
		f.region.Key(msg)
	}
}

// keepRegion puts the typed region into its row with the rule of
// keepRowValue. An empty value removes the row. The text stays as typed:
// maintain.CheckDraft names every normalization, so the form shows it
// before the review.
func (f *pinForm) keepRegion() {
	d := f.draft
	f.field = pinFieldNone

	// A reload can remove the row that the input replaces. The typed
	// value then becomes a new row, so the text stays.
	if f.regionAt >= len(d.regions) {
		f.regionAt = -1
	}

	var added int

	d.regions, added = keepRowValue(d.regions, f.regionAt, f.region.Value())
	if added >= 0 {
		f.row = pinRowFirstRegion + added
	}

	f.row = min(f.row, f.rows()-1)
}

// dropLockedFocus leaves a focused field whose row is read-only now, and
// keeps the cursor on a row. A reload can lock a row under the cursor,
// and a read-only row takes no typing.
func (f *pinForm) dropLockedFocus() {
	d := f.draft

	switch {
	case f.field == pinFieldRule && d.ruleLock != "",
		f.field == pinFieldNote && d.noteLock != "",
		f.field == pinFieldRegion && d.scopeLock != "":
		f.field = pinFieldNone
	}

	f.row = min(f.row, f.rows()-1)
}

// promptKey handles the question before a discard: d discards the
// draft, esc keeps it.
func (p *pinsView) promptKey(msg tea.KeyMsg) tea.Cmd {
	f := p.author.form

	switch {
	case key.Matches(msg, authorKeys.DiscardYes):
		p.author.drop(f.draft)

		return toast(ui.ToneNeutral, "Draft discarded. "+pinfile.File+" did not change.")
	case key.Matches(msg, ui.Nav.Back):
		f.prompt = false
	}

	return nil
}

// orphanKey handles the form of an orphaned draft: copy and discard
// only, because its entry is unknown and no review can bind to it.
func (p *pinsView) orphanKey(msg tea.KeyMsg) tea.Cmd {
	f := p.author.form

	switch {
	case key.Matches(msg, ui.Nav.Back):
		p.closeForm()
	case key.Matches(msg, authorKeys.Copy):
		return p.copyDraft(f.draft)
	case key.Matches(msg, authorKeys.Discard):
		f.prompt = true
	}

	return nil
}

// copyDraft sends the text of a draft to the terminal clipboard (OSC
// 52), as the apply preview copies its pin block.
func (p *pinsView) copyDraft(d *pinDraft) tea.Cmd {
	text := "rule: " + d.ruleValue() + "\nnote: " + d.noteValue() + "\nregions: " + strings.Join(d.regions, ", ") + "\n"

	return clipboardCmd(p.st, text, "the draft")
}

// closeForm closes the form and keeps its draft. A draft without a
// change goes away, so it never asks a quit question.
func (p *pinsView) closeForm() {
	if d := p.author.form.draft; !d.dirty() {
		p.author.drop(d)
	}

	p.author.form = nil
}

// newPin opens the form of the new-pin draft. It works in every state,
// also without an index or a readable pin file: the review names what
// blocks the write.
func (p *pinsView) newPin() tea.Cmd {
	a := &p.author
	if a.newDraft == nil {
		a.newDraft = newCreateDraft()
	}

	a.form = newPinForm(a.newDraft)

	return nil
}

// editPin opens the edit draft of the cursor pin. A pin without a draft
// gets a new one. i acts on the cursor pin only (contracts §4.2), so an
// orphaned draft shows only where the cursor has no pin row: a history
// list or an empty list. No entry leads to an orphaned draft any more.
func (p *pinsView) editPin() tea.Cmd {
	a := &p.author

	pin, refusal := p.authorTarget(maintain.AuthorEdit)
	if pin == nil && len(a.orphans) > 0 && !p.authorRow() {
		a.form = newPinForm(a.orphans[0])

		return nil
	}

	if pin == nil {
		return toast(ui.ToneNeutral, refusal)
	}

	ref := pin.Entry.Ref()

	d := a.edits[ref]
	if d == nil {
		d = newEditDraft(pin, p.authorKnown(p.inspection()))
		a.edits[ref] = d
	}

	a.form = newPinForm(d)

	return nil
}

// removePin opens the removal review of the cursor pin. A removal has no
// field, so it skips the form.
func (p *pinsView) removePin() tea.Cmd {
	pin, refusal := p.authorTarget(maintain.AuthorRemove)
	if pin == nil {
		return toast(ui.ToneNeutral, refusal)
	}

	req := maintain.AuthorRequest{Op: maintain.AuthorRemove, Ref: pin.Entry.Ref()}
	p.author.review = newPinReview(p.st, req, nil, len(p.selected) > 0, p.pinWhere(pin.Entry.Ref()))

	return p.author.review.prepare()
}

// reviewDraft opens the review of the draft of the open form. An
// orphaned draft opens no review.
func (p *pinsView) reviewDraft() tea.Cmd {
	d := p.author.form.draft
	if d.orphan != "" {
		return nil
	}

	where := ""
	if d.op == maintain.AuthorEdit {
		where = p.pinWhere(d.ref)
	}

	marks := d.op == maintain.AuthorEdit && len(p.selected) > 0
	p.author.review = newPinReview(p.st, d.request(), d, marks, where)

	return p.author.review.prepare()
}

// pinWhere names the entry with binding ref for the review, for example
// "pin 3 at line 12". It returns "" when the inspection does not hold
// the entry.
func (p *pinsView) pinWhere(ref pinfile.EntryRef) string {
	return pinWhereIn(p.inspection(), ref)
}

// pinWhereIn names the entry with binding ref in the inspection in, as
// pinWhere does. A review calls it when it binds its entry again
// (pinReview.prepare).
func pinWhereIn(in *maintain.Inspection, ref pinfile.EntryRef) string {
	pin, i, ok := inspectedPin(in, ref)
	if !ok {
		return ""
	}

	where := "pin " + strconv.Itoa(i+1)
	if pin.Entry.Line > 0 {
		where += " at line " + strconv.Itoa(pin.Entry.Line)
	}

	return where
}

// authorTarget returns the cursor pin when op can act on it, else a
// refusal line. It reads the cursor row only, never the marks.
func (p *pinsView) authorTarget(op maintain.AuthorOp) (pin *maintain.Pin, refusal string) {
	action := "edit"
	if op == maintain.AuthorRemove {
		action = "remove"
	}

	in := p.inspection()
	cur, ok := p.cursorRow(p.rows())

	switch {
	case p.filter != pinInstalled:
		return nil, "No " + action + " in a history list. Press f to show installed pins."
	case in == nil:
		return nil, "No " + action + " here. The pins are still loading."
	case !ok || cur.pin == nil:
		return nil, "No " + action + " here. No pin is in the list."
	}

	if why := pinAuthorBlock(in, cur.pin, op, p.authorKnown(in)); why != "" {
		return nil, pinRefusal(action, why, p.quoteRule(cur.pin.Entry.Rule))
	}

	return cur.pin, ""
}

// authorOffers reports whether op can act on the cursor pin. The footer
// shows i and d as available only then (contracts §4.2). i also works in
// a list without a pin row while an orphaned draft waits, because i then
// shows that draft (editPin).
func (p *pinsView) authorOffers(op maintain.AuthorOp) bool {
	if pin, _ := p.authorTarget(op); pin != nil {
		return true
	}

	return op == maintain.AuthorEdit && len(p.author.orphans) > 0 && !p.authorRow()
}

// authorRow reports a cursor row that holds an installed pin. Only such
// a row is a target of i and d; the keys never reach past it.
func (p *pinsView) authorRow() bool {
	cur, ok := p.cursorRow(p.rows())

	return p.filter == pinInstalled && p.inspection() != nil && ok && cur.pin != nil
}

// authorKnown reports whether the authoring keys know the relationship
// of the pins. A foreign root counts as unknown, as a missing ledger
// does (contracts §4.2). The index then records another repository, so
// a save needs an index of this workspace (P4-16). The rule has no --db
// exception, because the engine has none (authorForeignRoot).
func (p *pinsView) authorKnown(in *maintain.Inspection) bool {
	return pinLinksKnown(in) && authorForeignRoot(p.st) == ""
}

// pinAuthorBlock returns why op cannot act on pin, with its route, or
// "". known is the result of authorKnown. An unknown relationship does
// not block: the form and the review say that a save needs a readable
// index (P4-8). Only a pending or an ambiguous link, an unreadable pin
// file, or an entry block refuse the keys (contracts §4.2). A linked pin
// allows a note edit only, so it refuses a removal.
func pinAuthorBlock(in *maintain.Inspection, pin *maintain.Pin, op maintain.AuthorOp, known bool) string {
	l := pin.Link

	switch {
	case in.DocErr != nil || in.Doc.Unreadable():
		return "the pin file could not be read; fix it, then press r"
	case pin.Entry.Block != "":
		return "the entry cannot be edited safely (" + ui.Diag(pin.Entry.Block) + "); edit it by hand in " + pinfile.File
	case !known, l.Link == pinfile.LinkNoProposal:
		return ""
	case pinLinked(pin) && op == maintain.AuthorRemove:
		return "it is linked to " + propID(l.Applied[0].ID) + "; use - Prune"
	case pinLinked(pin):
		return ""
	case l.Link == pinfile.LinkPendingMatch && len(l.Pending) == 1:
		return "only " + propID(l.Pending[0].ID) + " matches, and it is pending; record it as applied in Proposals first"
	case l.Link == pinfile.LinkPendingMatch:
		return "only pending proposals match; record one as applied in Proposals first"
	}

	return "its link is ambiguous; edit " + pinfile.File + " by hand"
}

// The words of a draft that lost its entry, and the route to it.
const (
	pinOrphanWhy   = "its pin left " + pinfile.File + ", or another entry now has its rule and regions"
	pinOrphanRoute = "press f, then i, to see it"
)

// rebindDrafts binds each edit draft to its entry in a new inspection
// (contracts §4.2). A draft follows an entry that moved or changed, and
// it is orphaned without a unique match. A review of a draft whose
// binding or request changed closes, because its plan names the old
// entry or the old edits. A file that does not read binds nothing: a
// missing entry is then unknown, not gone. It returns the notes for the
// message of reconcile.
func (p *pinsView) rebindDrafts() []string {
	a := &p.author
	in := p.inspection()

	if in == nil || in.DocErr != nil || in.Doc.Unreadable() || len(a.edits) == 0 {
		return nil
	}

	known := p.authorKnown(in)
	next := map[pinfile.EntryRef]*pinDraft{}

	var notes []string

	for _, d := range pinEditDrafts(a.edits) {
		i, how := pinDraftMatch(in.Pins, d.ref)
		if how != rebindOrphan && next[in.Pins[i].Entry.Ref()] != nil {
			how = rebindOrphan
		}

		if how != rebindSame && a.closeReview(d) {
			notes = append(notes, pinReviewPinChanged)
		}

		label := p.quoteRule(d.base.Rule)
		f := a.form
		open := f != nil && f.draft == d

		// A region that the user typed but did not confirm belongs to the
		// draft. It joins the rows before they change or lock, so no typed
		// text goes away unseen.
		linked := how != rebindOrphan && pinNoteOnly(&in.Pins[i], known)
		if open && f.field == pinFieldRegion && (how == rebindChanged || how == rebindOrphan || linked) {
			f.keepRegion()
		}

		if how == rebindOrphan {
			if note := a.orphan(d, label, open); note != "" {
				notes = append(notes, note)
			}

			continue
		}

		if how == rebindChanged {
			notes = append(notes, "The pin of the edit draft of "+label+" changed since the draft started")
		}

		dropped := d.follow(&in.Pins[i], known, how)

		// A reload can change the request at the same binding: a new
		// link drops the rule and scope edits. The plan of the review then
		// shows edits that the draft no longer holds, so it closes too.
		if r := a.review; r != nil && r.draft == d && !sameAuthorRequest(d.request(), r.req) && a.closeReview(d) {
			notes = append(notes, "The pin review closed, because the reload changed its draft")
		}

		if len(dropped) > 0 {
			notes = append(notes, pinDroppedNote(dropped, label, in.Pins[i].Link.Applied[0].ID))
		}

		if open {
			f.dropLockedFocus()
		}

		next[d.ref] = d
	}

	a.edits = next

	return notes
}

// pinReviewPinChanged is the note of a review that a reload closed,
// because its entry left the binding of its plan.
const pinReviewPinChanged = "The pin review closed, because its pin changed"

// closeReview closes the open review of draft d before its commit, and
// reports whether it closed. d is nil for a removal. A commit and a
// result stay, because they no longer depend on the plan.
func (a *pinAuthor) closeReview(d *pinDraft) bool {
	r := a.review
	if r == nil || r.draft != d || !r.beforeCommit() {
		return false
	}

	r.seq.cancel()
	r.close()
	a.review = nil

	return true
}

// rebindRemoval closes an open removal review before its commit when a
// new inspection no longer holds its entry at its binding. The plan
// names that binding, as the plan of a draft does (rebindDrafts). A
// result stays, and enter finds the entry again (pinReview.prepare). A
// file that does not read binds nothing, as for the drafts.
func (p *pinsView) rebindRemoval() []string {
	in := p.inspection()
	r := p.author.review

	if in == nil || in.DocErr != nil || in.Doc.Unreadable() || r == nil || r.draft != nil {
		return nil
	}

	if slices.ContainsFunc(in.Pins, func(pin maintain.Pin) bool { return pin.Entry.Ref() == r.req.Ref }) {
		return nil
	}

	if !p.author.closeReview(nil) {
		return nil
	}

	return []string{pinReviewPinChanged}
}

// sameAuthorRequest reports two equal requests. A nil and an empty list
// of regions are equal, because both hold no region row.
func sameAuthorRequest(a, b maintain.AuthorRequest) bool {
	if len(a.Regions) == 0 {
		a.Regions = nil
	}

	if len(b.Regions) == 0 {
		b.Regions = nil
	}

	return reflect.DeepEqual(a, b)
}

// pinDroppedNote names the edits that a new link removed from a draft,
// and the route that changes the scope of a linked pin.
func pinDroppedNote(dropped []string, label string, id int64) string {
	edits := " edit"
	if len(dropped) > 1 {
		edits = " edits"
	}

	return "The " + strings.Join(dropped, " and ") + edits + " of " + label + " went away: the pin is linked to " +
		propID(id) + ", so only its note edits; change the scope with w Retarget"
}

// orphan handles an edit draft without a unique entry and returns the
// note for reconcile. A draft with a change keeps its text as an
// orphan: it offers copy and discard only. A draft without a change is
// no lost work, so it goes away with its form.
func (a *pinAuthor) orphan(d *pinDraft, label string, open bool) string {
	if !d.dirty() {
		a.drop(d)

		if open {
			return "The edit form of " + label + " closed: " + pinOrphanWhy
		}

		return ""
	}

	d.orphan = pinOrphanWhy
	a.orphans = append(a.orphans, d)

	// An orphaned draft takes no more typing; its text stays.
	if open {
		a.form.field = pinFieldNone
	}

	return "The edit draft of " + label + " lost its pin; " + pinOrphanRoute
}

// authorCommitted shows a commit outcome. It always frees the mutation
// slot and reads the sources of pinWriteRefresh again, because a refusal
// also means that the state changed.
//
// A written file drops the draft and tells every other preview of
// lessons.yaml (P4-12). It also watches the reload, so the result can
// name a failed read (P4-18). An unknown outcome keeps
// the draft and tells the other previews too, because the file may have
// changed. A refusal or a file that the write did not change keeps the
// draft and tells nobody.
func (p *pinsView) authorCommitted(msg pinCommittedMsg) tea.Cmd {
	r := msg.r
	reload := refresh(pinWriteRefresh...)
	written := msg.err == nil && msg.res.Outcome == maintain.Written
	unknown := msg.err == nil && msg.res.Outcome == maintain.Unknown

	if written {
		p.authorWritten(msg)
	}

	if r.commitDone() {
		out := &pinOutcome{plan: msg.plan, res: msg.res, err: msg.err}
		r.out = out

		if written {
			shown := func() bool { return p.author.review == r && r.stage == flowResult && r.out == out }
			out.check, reload = p.st.watchRefresh(pinfile.File, shown, pinWriteRefresh...)
		}
	}

	cmds := []tea.Cmd{mutationDone, reload}
	if written || unknown {
		cmds = append(cmds, sendLessonsSaved(time.Now(), "Lessons "+p.st.theme.Glyphs.Arrow+" Pins"))
	}

	return tea.Batch(cmds...)
}

// authorWritten applies a written file to the sub-view in the same
// Update as the result. The draft goes away. The changed entries count
// as acted, so the reload reports no loss for them, and the cursor
// follows the new or edited entry.
func (p *pinsView) authorWritten(msg pinCommittedMsg) {
	if d := msg.r.draft; d != nil {
		p.author.drop(d)
	}

	var refs []pinfile.EntryRef

	if msg.plan != nil && msg.plan.Request.Op != maintain.AuthorCreate {
		refs = append(refs, msg.plan.Request.Ref)
	}

	if ref := msg.res.NewRef; ref != nil {
		refs = append(refs, *ref)
		p.cursorID = pinID{ref: *ref}
	}

	p.markActed(refs)
}

// authorModal returns the open pin review, or the discard question over
// the form. An open selection sample takes the place of its review.
func (p *pinsView) authorModal(w, h int) (string, bool) {
	a := &p.author

	switch {
	case a.review != nil && a.review.sample != nil:
		return a.review.sample.modal(w, h), true
	case a.review != nil:
		return p.st.theme.Modal(a.review.content(ui.ModalBodyWidth(ui.ModalMinW, w)), &a.review.scroll, w, h), true
	case a.form != nil && a.form.prompt:
		return p.st.theme.Modal(p.promptContent(ui.ModalBodyWidth(ui.ModalMinW, w)), nil, w, h), true
	}

	return "", false
}

// drafts implements draftHolder: every unsaved draft of a pin changes
// .seamark/lessons.yaml, so the quit question names that file.
func (p *pinsView) drafts() []string {
	if slices.ContainsFunc(p.author.all(), (*pinDraft).dirty) {
		return []string{pinfile.File}
	}

	return nil
}

// discardDrafts implements draftHolder. The shell calls it when the
// console exits.
func (p *pinsView) discardDrafts() {
	p.author = pinAuthor{edits: map[pinfile.EntryRef]*pinDraft{}}
}

// contextHelp implements contextHelper for the pin form, the pin review
// and an open selection sample. A focused text field gets ? as a typed
// character, and the discard question has no help of its own.
func (p *pinsView) contextHelp() (helpPage, bool) {
	a := &p.author

	switch {
	case a.review != nil && a.review.sample != nil:
		return a.review.sample.help()
	case a.review != nil:
		// A removal opens no form, and a written draft closes its form.
		// esc then returns to the list, so the help must not name the form.
		esc := pinReviewEscForm
		if a.form == nil {
			esc = pinReviewEscList
		}

		text := []string{pinReviewHelp[0], pinReviewHelp[1] + " " + esc}
		if a.review.samples() {
			text = append(text, sampleOpenHelp)
		}

		return helpPage{title: "Pin review", text: text, keys: a.review.keys()}, true
	case a.form == nil, a.form.prompt, a.form.field != pinFieldNone:
		return helpPage{}, false
	}

	return helpPage{title: "Pin form", text: pinFormHelp, keys: p.authorBindings()}, true
}

// The paragraphs of the context help.
var (
	pinFormHelp = []string{
		"The form drafts one pin of " + pinfile.File + ". Nothing is written before the review and its y.",
		"Rule is a short label on one line. Note is the guidance that an agent reads; enter adds a line break. " +
			"Scope is repository-wide or a list of regions. A new pin needs all three.",
		pinRegionsHelp,
		"esc leaves a text field and keeps the text. esc on a row closes the form; the draft stays until you " +
			"discard it or quit.",
	}

	// pinReviewHelp has two paragraphs. contextHelp adds the esc sentence
	// to the second one.
	pinReviewHelp = [2]string{
		"The review shows the exact change of " + pinfile.File + ": the operation, the entry, the guidance " +
			"before and after, the diff, and every reason that blocks the write.",
		"y writes the file. Pin authoring never changes the proposal ledger. o opens the route of a blocker.",
	}
)

// The esc sentences of the review help. A review over an open form
// returns to it; every other review returns to the list.
const (
	pinReviewEscForm = "esc returns to the form and keeps the draft."
	pinReviewEscList = "esc closes the review and returns to the Pins list."
)

// authorBindings returns the footer keys of the open review, the discard
// question, the focused field, or the rows.
func (p *pinsView) authorBindings() []key.Binding {
	a := &p.author
	back := func(label string) key.Binding { return ui.WithHelp(ui.Nav.Back, "esc", label) }

	if a.review != nil {
		return a.review.keys()
	}

	f := a.form

	switch {
	case f.prompt:
		return []key.Binding{authorKeys.DiscardYes, back("keep")}
	case f.field == pinFieldNote:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "new line"), back("done")}
	case f.field != pinFieldNone:
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "done"), back("done")}
	case f.draft.orphan != "":
		return []key.Binding{authorKeys.Copy, authorKeys.Discard, back("close")}
	}

	discard := authorKeys.Discard
	if f.regionIndex() >= 0 {
		discard = ui.WithHelp(authorKeys.Discard, "x", "remove region")
	}

	// The footer is the only key row of the form, and a narrow footer
	// drops the hints from the end. So esc comes second, and esc close
	// stays in view at 60 columns. ↑↓ comes last, as in the review.
	return []key.Binding{
		ui.WithHelp(ui.Nav.Enter, "enter", f.rowAction()), back("close"), authorKeys.Review, authorKeys.Add, discard,
		ui.WithHelp(ui.Nav.Down, "↑↓", "rows"),
	}
}

// rowAction names what enter does on the cursor row.
func (f *pinForm) rowAction() string {
	switch {
	case f.row == pinRowRepoWide || f.row == pinRowRegions:
		return "choose"
	case f.row == f.addRow():
		return "add region"
	default:
		return "edit"
	}
}

// keys returns the footer keys of the current stage, or of the open
// sample. The modal names the same keys with longer labels. The modal
// row of a sample leaves out ↑↓ (sample.keys).
func (r *pinReview) keys() []key.Binding {
	if r.sample != nil {
		return r.sample.keys(false)
	}

	scroll := ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")
	scroll.SetEnabled(r.scroll.Scrolls())

	switch r.stage {
	case flowPreparing:
		return []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	case flowCommitting:
		return nil
	case flowResult:
		return append(r.resultActions(), scroll)
	}

	return append(r.previewActions(true), scroll)
}

// previewActions returns the keys of a review. short selects the footer
// labels. y shows only while it commits; s shows only while the plan has
// a candidate; o shows only while a blocker has a route.
func (r *pinReview) previewActions(short bool) []key.Binding {
	var out []key.Binding

	if r.confirmable() {
		label := r.req.Op.String()
		if !short {
			label += " the pin"
		}

		out = append(out, ui.WithHelp(lesKeys.Confirm, "y", label))
	}

	if r.samples() {
		out = append(out, sampleKeys.Open)
	}

	if _, _, words, ok := r.route(); ok {
		label := "open " + words
		if short {
			label = "route"
		}

		out = append(out, ui.WithHelp(maintKeys.Settings, "o", label))
	}

	back := "back"
	if r.draft == nil {
		back = "close"
	}

	return append(out, ui.WithHelp(ui.Nav.Back, "esc", back))
}

// resultActions returns the keys of a result: enter reviews again after
// an outcome that a fresh review can resolve, else it closes.
func (r *pinReview) resultActions() []key.Binding {
	if r.reviewAgain() {
		back := "back"
		if r.draft == nil {
			back = "close"
		}

		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "review again"), ui.WithHelp(ui.Nav.Back, "esc", back)}
	}

	return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "close")}
}

// pinStale returns the differences of a stale commit, or nil for any
// other error.
func pinStale(err error) []string {
	var stale *maintain.StaleError
	if errors.As(err, &stale) {
		return stale.Differences
	}

	return nil
}
