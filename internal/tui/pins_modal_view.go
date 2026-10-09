package tui

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file renders the stages of the retarget and prune preview. The
// preview states what is selected, the state of every selected pin, the
// exact file and ledger change, and what WILL and WON'T happen. Every
// text from the pin file, the ledger, a model or an error passes through
// oneLine, multiLine or errText here, at the leaf, because the
// maintenance engine returns it raw (internal/maintain package doc).

// maintLabelW is the label column of the labelled rows of a preview, for
// example "Regions   internal/db". It fits the longest labels, "Retained"
// and "Recorded", with a gap of two cells, so no label touches its value.
const maintLabelW = 10

// content builds the modal of the current stage in w cells: the border
// labels, the body that scrolls, the scope summary that stays (Tail),
// and the actions.
func (m *maintModal) content(w int) modalOpts {
	switch m.stage {
	case flowPreparing:
		return m.preparingView(w)
	case flowCommitting:
		return m.committingView(w)
	case flowResult:
		return m.resultView(w)
	}

	switch {
	case m.err != nil:
		return m.planErrView(w)
	case m.recovering:
		return m.recoveryView(w)
	}

	return m.planView(w)
}

// preparingView waits for a plan read. The read writes nothing.
func (m *maintModal) preparingView(w int) modalOpts {
	what := "the " + m.req.Action.String() + " preview"
	read := "Seamark reads the pin file, the ledger and the write gate"

	switch {
	case m.recovering:
		what = "the ledger completion"
		read = "Seamark reads the pin file and the ledger again, to check that the result of this session still holds"
	case m.req.Action == maintain.ActionRetarget:
		read += ", and computes the scope from today's evidence"
	}

	return flowWaitModal(m.st, "Preparing "+what, read+". Nothing is written.", true, w)
}

// committingView waits for a commit. It offers no key, because the
// commit cannot stop safely between the file and the ledger.
func (m *maintModal) committingView(w int) modalOpts {
	note := "Seamark checks the plan again, replaces " + pinfile.File + ", then records the ledger. The operation " +
		"cannot stop safely; the result appears here."
	if m.recovering {
		note = "Seamark checks the proof again and records the ledger only. " + pinfile.File + " stays as it is. " +
			"The result appears here."
	}

	return flowWaitModal(m.st, m.st.mutation, note, false, w)
}

// planErrView explains a plan read that failed. Nothing changed,
// because a plan only reads.
func (m *maintModal) planErrView(w int) modalOpts {
	what := "The " + m.req.Action.String() + " preview"
	if m.recovering {
		what = "The ledger completion preview"
	}

	var hint string

	switch {
	case errors.Is(m.err, ErrNoLedger), errors.Is(m.err, maintain.ErrNoLedger):
		hint = "Proposals live in the index database. Build it from Overview (press 1, then b)."
	case errors.Is(m.err, maintain.ErrNothingSelected):
		hint = "Select pins with space in Pins first."
	default:
		hint = "Fix the cause named above, for example a malformed file, then refresh with r and try again."
	}

	return previewFailedModal(m.st.theme, what, m.err, hint, "Nothing was changed.", m.previewActions(false), w)
}

// planView renders a retarget or prune plan: the lead, the blockers,
// the selected pins, the ledger change, the effects and the diff.
func (m *maintModal) planView(w int) modalOpts {
	p := m.preview

	body := m.planLead(w)

	if note := m.staleIndexNote(); note != "" {
		body = append(body, "")
		body = append(body, propNote(m.st.theme, ToneAttention, note, w)...)
	}

	body = append(body, m.planBlockers(w)...)
	body = append(body, m.selectionLines(w)...)
	body = append(body, maintLedgerLines(m.st.theme, p, w)...)
	body = append(body, m.planEffects(w)...)
	body = append(body, m.diffLines(w)...)

	return modalOpts{
		Title: m.planTitle(), Right: m.planRight(), Body: body, Tail: m.planTail(w),
		Actions: m.previewActions(false),
	}
}

// staleIndexNote returns the note of a retarget whose scope advice
// reads a stale index, or "". A plan accepts a stale index (contracts
// D-16), but the computed regions then miss the newest history, so the
// preview says so. The note reads the index state of the header, so
// both show the same freshness.
func (m *maintModal) staleIndexNote() string {
	if m.preview == nil || m.preview.Action != maintain.ActionRetarget || !m.st.index.loaded {
		return ""
	}

	v := m.st.index.value
	if v.Probe.State != workspace.IndexReady || v.Status == nil || v.Status.Freshness != status.FreshStale {
		return ""
	}

	built := ""
	if v.Probe.IndexedAt > 0 {
		built = " built " + ageSince(v.Probe.IndexedAt)
	}

	return "Scope advice reads a stale index" + built + ". Build it from Overview (press 1, then b) to compute " +
		"the regions from the current history."
}

// planTitle names the action and the selection. A prune verb takes the
// failure style, as Dismiss does, because it removes guidance.
func (m *maintModal) planTitle() string {
	t := m.st.theme

	verb := t.TabActive.Render("Retarget")
	if m.preview.Action == maintain.ActionPrune {
		verb = t.Failure.Bold(true).Render("Prune")
	}

	return verb + " " + t.Body.Render(maintSubject(t, m.preview.Items))
}

// planRight labels the plan in the border. A broader or repository-wide
// retarget comes first, because it delivers the pin to more paths. A
// plan that y cannot confirm says so; otherwise the count shows.
func (m *maintModal) planRight() string {
	t := m.st.theme

	switch scope := maintScopeLabel(m.preview); {
	case scope == maintScopeRepoWide || scope == maintScopeBroader:
		return t.Badge(ToneAttention, scope)
	case !m.preview.Confirmable:
		return t.Badge(ToneAttention, "preview only")
	case scope != "":
		return t.Muted.Render(scope)
	}

	return t.Muted.Render(propPlural(len(m.preview.Items), "pin", "pins"))
}

// The scope labels of a retarget plan.
const (
	maintScopeRepoWide = "repository-wide"
	maintScopeBroader  = "broader"
	maintScopeNarrower = "narrower"
)

// maintScopeLabel returns the widest scope change among the ready items
// of a retarget, or an empty string.
func maintScopeLabel(p *maintain.Plan) string {
	if p.Action != maintain.ActionRetarget {
		return ""
	}

	label := ""

	for _, it := range p.Items {
		switch {
		case it.Status != maintain.ItemReady:
		case it.RepoWide:
			return maintScopeRepoWide
		case it.Broader:
			label = maintScopeBroader
		case it.Pin.Readings.Scope.Advice.Narrower && label == "":
			label = maintScopeNarrower
		}
	}

	return label
}

// planLead is the first paragraph: what y does, or that y is off.
func (m *maintModal) planLead(w int) []string {
	t := m.st.theme
	p := m.preview

	if !p.Confirmable {
		// A plan without a ready pin has no file change, so the lead does
		// not promise an exact change that the DIFF section cannot show.
		lead := "This preview shows the exact change, but it cannot be confirmed, so y is off. Nothing was changed."
		if p.Change == nil {
			lead = "This preview has no change to confirm, so y is off. Nothing was changed."
		}

		return propLines(t.Body, lead, w)
	}

	n := propPlural(len(p.Items), "pin", "pins")
	ids := propIDList(maintLedgerIDs(p))

	lead := "Seamark sets the regions of " + n + " in " + pinfile.File + " to the scope that today's evidence " +
		"supports, and records them for " + ids + " in the ledger."
	if p.Action == maintain.ActionPrune {
		lead = "Seamark removes " + n + " from " + pinfile.File + " and records " + ids + " as superseded in the " +
			"ledger."
	}

	out := propLines(t.Body, lead+" distill.write is on. Nothing changes before you press y.", w)

	if m.st.busy() {
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, m.st.mutation+" is running. y is off until it ends.", w)...)
	}

	return out
}

// planBlockers explains why y is off, each with its route. It reads the
// typed plan fields, so every reason names its own next step. A plan
// that is not confirmable for another reason shows the engine reasons.
func (m *maintModal) planBlockers(w int) []string {
	t := m.st.theme
	p := m.preview

	if p.Confirmable {
		return nil
	}

	var out []string

	note := func(text string) {
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, text, w)...)
	}

	open := maintOpenKey(p.Action)

	if m.foreign != "" {
		note("The index of this workspace records another repository, " + oneLine(m.foreign) + ", so y is off. " +
			"A commit would write " + oneLine(filepath.Join(m.foreign, pinfile.File)) + " and the ledger of this " +
			"workspace, not " + oneLine(filepath.Join(m.st.svc.Root(), pinfile.File)) + ". Run seamark index in " +
			"this checkout, or build the index from Overview (press 1, then b).")
	}

	// A cursor-row plan has no marked row. Space would mark the same row,
	// so its route is another pin, not an unmark.
	switch k := maintNotReady(p.Items); {
	case k > 0 && p.Request.Cursor:
		note("The cursor row cannot change, so y is off. Press esc, move to another pin in Pins, then press " +
			open + " again.")
	case k > 0:
		note(itoa(k) + " of " + propPlural(len(p.Items), "selected pin", "selected pins") + " cannot change, so " +
			"y is off: revise the selection. Unmark them in Pins with space, then press " + open + " again.")
	}

	if p.FileBlock != "" {
		note("The console cannot change " + pinfile.File + ": " + oneLine(p.FileBlock) + ". Edit the file by hand.")
	}

	switch {
	case p.GateErr != nil:
		note("distill.write cannot be read: " + errText(p.GateErr) + ". Fix " + settingsFile + " first; a reader " +
			"error never counts as on.")
	case !p.Write:
		note("distill.write is off, so this preview changes nothing. Press o for Settings " + t.Glyphs.Arrow +
			" Pin writing; after a save, press " + open + " in Pins for a new preview.")
	}

	if len(out) == 0 {
		for _, b := range p.Blocked {
			note(ovSentenceEnd(oneLine(b)))
		}
	}

	return out
}

// selectionLines names the selected pins, each with its state and its
// change or its reason.
func (m *maintModal) selectionLines(w int) []string {
	t := m.st.theme
	p := m.preview

	head := t.Heading.Render("SELECTED") + "  " + t.Faint.Render(propPlural(len(p.Items), "pin", "pins"))
	if p.Request.Cursor {
		head += t.Faint.Render(" " + t.Glyphs.Sep + " the cursor row; no row is marked")
	}

	out := []string{"", head}

	for i, it := range p.Items {
		if i > 0 {
			out = append(out, "")
		}

		out = append(out, m.itemLines(it, w)...)
	}

	if p.Action == maintain.ActionRetarget && maintNotReady(p.Items) < len(p.Items) {
		// Neutral notes carry no glyph: the Info glyph reads "-" in ASCII,
		// like a removed region.
		out = append(out, "")
		out = append(out, propLines(t.Muted, "Delivery history restarts under the new pin identity: the outcome "+
			"of a retargeted pin reads untested until it fires again.", w)...)
	}

	return out
}

// itemLines renders one selected pin: its rule with the state badge,
// where it is, and the part that its state needs.
func (m *maintModal) itemLines(it maintain.PlanItem, w int) []string {
	t := m.st.theme

	out := maintHeadLines(t, maintRule(it), maintItemBadge(t, it.Status, false), w)

	if it.Pin.Entry.Line == 0 {
		where, reason := m.goneText(it)
		out = append(out, t.Faint.Render(truncate(where, w, t.Glyphs.Ellipsis)))

		return append(out, propNote(t, ToneAttention, reason, w)...)
	}

	out = append(out, t.Faint.Render(truncate(maintWhere(t, it), w, t.Glyphs.Ellipsis)))

	switch it.Status {
	case maintain.ItemReady:
		if m.preview.Action == maintain.ActionPrune {
			return append(out, m.pruneItem(it, w)...)
		}

		return append(out, m.retargetItem(it, w)...)
	case maintain.ItemNoOp:
		out = append(out, integLabeled(t, "Regions", maintLabelW, maintRegionText(it.From), t.Body, w)...)

		return append(out, propLines(t.Muted, ovSentenceEnd(oneLine(it.Reason)), w)...)
	}

	out = append(out, m.blockedScope(it, w)...)

	return append(out, propNote(t, ToneAttention, ovSentenceEnd(oneLine(it.Reason)), w)...)
}

// blockedScope shows the computed regions of a blocked retarget and the
// pin or the proposal that already has them. The planner refuses such a
// target (pinfile ErrCollision), and its reason names only the key. So
// the user sees what the retarget would have done and which pin to
// compare. Any other blocked item has no computed regions.
func (m *maintModal) blockedScope(it maintain.PlanItem, w int) []string {
	t := m.st.theme
	s := it.Pin.Readings.Scope

	if m.preview.Action != maintain.ActionRetarget || !s.Known || s.Advice.Kind != distill.ScopeChanged {
		return nil
	}

	out := t.regionDelta(it.From, s.Advice.Regions, w)

	in := m.st.pins.value
	if !m.st.pins.loaded {
		in = nil
	}

	if owner := pinTargetOwner(in, &it.Pin, s.Advice.Regions); owner != "" {
		out = append(out, propLines(t.Muted, ovSentenceEnd(owner), w)...)
	}

	return out
}

// goneText returns the location and the reason of an item whose entry
// changed after the Pins list was read. The plan holds no live content
// for it, so the current Pins snapshot tells a removed pin from a
// changed one. The refresh route shows only while that snapshot still
// holds the reviewed entry, because a newer snapshot needs no refresh.
func (m *maintModal) goneText(it maintain.PlanItem) (where, reason string) {
	sep := " " + m.st.theme.Glyphs.Sep + " "
	changed := pinfile.File + sep + "changed since the list was read"

	in := m.st.pins.value
	if !m.st.pins.loaded || in == nil {
		return changed, ovSentenceEnd(oneLine(it.Reason))
	}

	refAt := func(i int) pinfile.EntryRef { return in.Pins[i].Entry.Ref() }
	if _, ok := pinMatch(len(in.Pins), refAt, it.Ref); ok {
		return changed, "The pin changed since the list was read. Press r in Pins to read the list again."
	}

	if slices.ContainsFunc(in.Pins, func(p maintain.Pin) bool { return p.Entry.Key == it.Ref.Key }) {
		return changed, "The pin changed since the list was read. The Pins list shows its current content."
	}

	return pinfile.File + sep + "no longer in the file", "No pin in " + pinfile.File + " has this rule and " +
		"these regions now. It was removed, or its rule or regions changed."
}

// retargetItem renders a ready retarget: the region change, a warning
// for a wider scope, the facts that support it, and both notes when
// the live note was edited.
func (m *maintModal) retargetItem(it maintain.PlanItem, w int) []string {
	t := m.st.theme

	out := t.regionDelta(it.From, it.To, w)

	switch {
	case it.RepoWide:
		out = append(out, propNote(t, ToneAttention, "Repository-wide: the pin reaches every path in the "+
			"repository, not only the regions that it names now.", w)...)
	case it.Broader:
		out = append(out, propNote(t, ToneAttention, "Broader: the new regions reach paths that the current "+
			"regions do not.", w)...)
	case it.Pin.Readings.Scope.Advice.Narrower:
		out = append(out, propLines(t.Muted, "Narrower: the new regions reach fewer paths than the current regions.",
			w)...)
	}

	for i, f := range it.Facts {
		label := ""
		if i == 0 {
			label = "Why"
		}

		out = append(out, integLabeled(t, label, maintLabelW, oneLine(maintFactText(it, f)), t.Muted, w)...)
	}

	if it.Pin.Link.NoteChanged {
		out = append(out, m.noteCompare(it, w)...)
	}

	return out
}

// maintFactText returns the console words of one engine fact. A fact
// that is the BlockedLine of a trigger ends with a hand edit route, and
// the CLI keeps that text (D-1). This preview is the retarget itself, so
// the console words drop the route (pinTriggerFact).
func maintFactText(it maintain.PlanItem, fact string) string {
	for _, f := range it.Pin.Readings.Trigger.Blocked {
		if f.BlockedLine() == fact {
			return pinTriggerFact(f, false)
		}
	}

	return fact
}

// noteCompare shows the live note beside the recorded note. A retarget
// keeps the live note in the file and the recorded note in the ledger,
// so neither replaces the other.
func (m *maintModal) noteCompare(it maintain.PlanItem, w int) []string {
	t := m.st.theme
	pw := pairWidth(w)

	var (
		recorded string
		id       = "the proposal"
	)

	if len(it.Rows) > 0 {
		recorded, id = it.Rows[0].Note, propID(it.Rows[0].ID)
	}

	lh := t.Heading.Render("LIVE NOTE") + "  " + t.Faint.Render("stays in the file")
	rh := t.Heading.Render("RECORDED") + "  " + t.Faint.Render(id)

	out := []string{""}
	out = append(out, t.pair(lh, rh, propLines(t.Body, pinNoteText(it.Pin.Entry.Note), pw),
		propLines(t.Muted, pinNoteText(recorded), pw), w)...)
	out = append(out, propLines(t.Muted, "The note was edited after distillation. The retarget keeps the live note; "+
		"the recorded note of "+id+" stays as history.", w)...)

	return out
}

// pruneItem renders a ready prune: the full live content that leaves
// the file, then the retained alternatives or the retirement note.
func (m *maintModal) pruneItem(it maintain.PlanItem, w int) []string {
	t := m.st.theme
	e := it.Pin.Entry

	out := integLabeled(t, "Regions", maintLabelW, maintRegionText(it.From), t.Body, w)
	out = append(out, integLabeled(t, "Note", maintLabelW, pinNoteText(e.Note), t.Body, w)...)

	// An edited live note is what leaves the file. The recorded note
	// shows too, so the user sees that the prune removes the edit.
	if it.Pin.Link.NoteChanged && len(it.Rows) > 0 {
		out = append(out, integLabeled(t, "Recorded", maintLabelW, pinNoteText(it.Rows[0].Note)+" ("+
			propID(it.Rows[0].ID)+", as distilled; the live note above differs)", t.Muted, w)...)
	}

	// The words follow the Pins detail ("unknown key owner"), so the same
	// key reads the same on both screens and never as "unknown owner".
	for i, k := range e.Unknown {
		label := ""
		if i == 0 {
			label = "Keys"
		}

		out = append(out, integLabeled(t, label, maintLabelW, "unknown key "+oneLine(k)+
			" (no reader uses it; it leaves with the entry)", t.Muted, w)...)
	}

	return append(out, maintKeptLines(t, it.Kept, w)...)
}

// maintKeptLines names the similar pins that stay linked after a prune.
// They are advice: similar wording does not prove the same coverage. A
// prune without them is a deliberate retirement (contracts D-23).
func maintKeptLines(t *Theme, kept []maintain.Pin, w int) []string {
	if len(kept) == 0 {
		return propNote(t, ToneAttention, "No retained equivalent is established; this is a deliberate retirement.", w)
	}

	var out []string

	for i, k := range kept {
		label := ""
		if i == 0 {
			label = "Retained"
		}

		where := pinfile.File + ":" + itoa(k.Entry.Line)
		if ids := maintAppliedIDs(k.Link.Applied); len(ids) > 0 {
			where += " " + t.Glyphs.Sep + " " + propIDList(ids)
		}

		out = append(out, integLabeled(t, label, maintLabelW, oneLine(k.Entry.Rule)+" ("+where+"): "+
			maintRegionText(maintEntryRegions(k.Entry)), t.Body, w)...)

		// A rule is often a short slug, and the note holds the guidance.
		// The note shows, so the user can compare it with the note that
		// leaves the file before an irreversible prune.
		note := pinNoteText(k.Entry.Note)
		if strings.TrimSpace(note) == "" {
			note = "No note is set."
		}

		out = append(out, integLabeled(t, "", maintLabelW, note, t.Muted, w)...)
	}

	return append(out, propLines(t.Muted, "Advisory: similar wording stays in "+
		propPlural(len(kept), "linked pin", "linked pins")+". It does not prove the same coverage; compare the "+
		"notes and the regions.", w)...)
}

// maintLedgerLines shows each planned ledger row change, for example
// "p4  internal/store › internal/store/sql". A plan that y cannot
// confirm labels the section as a preview only.
func maintLedgerLines(t *Theme, p *maintain.Plan, w int) []string {
	type row struct{ id, old, next string }

	var rows []row

	for _, c := range p.Retarget {
		rows = append(rows, row{propID(c.Guard.ID), maintGuardRegions(c.Guard), maintRegionText(c.Regions)})
	}

	for _, g := range p.Supersede {
		rows = append(rows, row{propID(g.ID), oneLine(g.Status), domain.ProposalSuperseded})
	}

	if len(rows) == 0 {
		return nil
	}

	head := t.Heading.Render("LEDGER")
	if !p.Confirmable {
		head += "  " + t.Faint.Render("preview only")
	}

	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, width(r.id)+2)
	}

	out := []string{"", head}
	for _, r := range rows {
		out = append(out, t.arrowRow(r.id, r.old, r.next, labelW, w)...)
	}

	if len(p.Retarget) > 0 {
		out = append(out, propLines(t.Faint, "The proposals stay applied; only their stored regions change.", w)...)
	}

	return out
}

// planEffects states what y does and what it never does. A plan that
// y cannot confirm has no effects, so it shows none.
func (m *maintModal) planEffects(w int) []string {
	t := m.st.theme
	p := m.preview

	if !p.Confirmable {
		return nil
	}

	n := propPlural(len(p.Items), "pin", "pins")
	ids := propIDList(maintLedgerIDs(p))
	change := "~"

	if p.Action == maintain.ActionPrune {
		retire := "promise the same coverage: no remaining pin is proven to replace a removed one"
		if maintAnyKept(p.Items) {
			retire = "promise the same coverage from a similar pin"
		}

		// Only the IDs of existing comments show, so a mixed selection
		// says which pins carry one.
		remove := "remove " + n + " from " + pinfile.File

		switch named := maintCommentIDs(p.Items); {
		case len(named) == 1:
			remove += ", with the provenance comment that names " + propIDList(named)
		case len(named) > 1:
			remove += ", with the provenance comments that name " + propIDList(named)
		}

		return t.effectLines([]effect{
			{glyph: t.Glyphs.Del, tone: ToneFailure, text: remove},
			{glyph: t.Glyphs.Del, tone: ToneFailure, text: "move " + ids + " from applied to superseded in the ledger"},
		}, []string{
			"change other pins, also pins with the same rule in other regions",
			"dismiss a proposal, or delete findings, signatures or the firing log",
			retire,
			"offer an undo: a superseded proposal stays history",
			"write anything before you press y",
		}, w)
	}

	return t.effectLines([]effect{
		{glyph: change, tone: ToneAttention, text: "change the region keys of " + n + " in " + pinfile.File},
		{glyph: change, tone: ToneAttention, text: "record the new regions of " + ids + " in the ledger"},
	}, []string{
		"change the rule, the live note, the comments or the position of a pin",
		"change other pins, mutes or settings in the file",
		"change the recorded note of a proposal",
		"write anything before you press y",
	}, w)
}

// maintCommentIDs returns the proposals whose provenance comment a prune
// removes. The prune removes only a comment that exists above the entry
// and names the pruned row (pinfile boundProvenance). A pin without such
// a comment, for example a hand-installed pin, loses only its own lines.
func maintCommentIDs(items []maintain.PlanItem) []int64 {
	var ids []int64

	for _, it := range items {
		if it.Status != maintain.ItemReady || len(it.Rows) == 0 {
			continue
		}

		// The engine prunes the first linked row, so its ID selects the
		// comment.
		if id := it.Rows[0].ID; slices.Contains(it.Pin.Entry.ProvenanceIDs, id) {
			ids = append(ids, id)
		}
	}

	return ids
}

// maintAnyKept reports a prune item with a retained alternative.
func maintAnyKept(items []maintain.PlanItem) bool {
	for _, it := range items {
		if len(it.Kept) > 0 {
			return true
		}
	}

	return false
}

// diffLines renders the exact file change with its line counts. A
// partial plan diffs only its ready pins, and the heading says so.
func (m *maintModal) diffLines(w int) []string {
	t := m.st.theme
	p := m.preview
	c := p.Change

	if c == nil {
		return append([]string{"", t.Heading.Render("DIFF")},
			propLines(t.Muted, "No file change: no selected pin is ready.", w)...)
	}

	head := t.Heading.Render("DIFF") + "  " + maintCounts(t, c.Added, c.Removed)
	if !p.Confirmable {
		head += "  " + t.Faint.Render("preview only")
	}

	out := []string{"", head}

	if maintNotReady(p.Items) > 0 {
		out = append(out, propLines(t.Muted, "The diff holds the ready pins only.", w)...)
	}

	out = append(out, setupDiffLines(t, c.Diff, w)...)

	return append(out, propLines(t.Faint, "The diff hides text that looks like a secret; the file gets the exact "+
		"bytes.", w)...)
}

// planTail is the scope summary under the rule. It names the action,
// the count and what y does, or why y is off, so the minimum layout
// shows them while the body scrolls.
func (m *maintModal) planTail(w int) []string {
	t := m.st.theme
	p := m.preview
	sep := " " + t.Glyphs.Sep + " "

	scope := "Retarget " + propPlural(len(p.Items), "pin", "pins")

	switch {
	case p.Action == maintain.ActionPrune:
		scope = "Prune " + propPlural(len(p.Items), "pin", "pins")
		if ids := maintLedgerIDs(p); len(ids) > 0 {
			scope += sep + maintTailIDList(t, ids) + " " + t.Glyphs.Arrow + " superseded"
		}
	case maintScopeLabel(p) == maintScopeRepoWide || maintScopeLabel(p) == maintScopeBroader:
		scope += sep + maintScopeLabel(p)
	}

	stale := m.staleIndexNote() != ""
	if stale {
		scope += sep + "index stale"
	}

	var state string

	switch {
	case p.Confirmable && m.st.busy():
		state = "y waits: another operation is running"
	case p.Confirmable && stale:
		return propNote(t, ToneAttention, scope+sep+"y writes lessons.yaml and the ledger", w)
	case p.Confirmable:
		return propLines(t.Body, scope+sep+"y writes lessons.yaml and the ledger", w)
	case m.foreign != "":
		state = "y is off: the index records another repository"
	case maintNotReady(p.Items) > 0 && p.Request.Cursor:
		state = "y is off: the cursor row cannot change"
	case maintNotReady(p.Items) > 0:
		state = "y is off: revise the selection"
	case p.FileBlock != "":
		state = "y is off: the pin file cannot be changed"
	case p.GateErr != nil:
		state = "y is off: distill.write cannot be read"
	case !p.Write:
		state = "y is off: distill.write is off"
	default:
		state = "y is off"
	}

	return propNote(t, ToneAttention, scope+sep+state, w)
}

// maintTailIDs is the number of proposal IDs that a pinned tail names.
// The tail never scrolls, and the modal cuts a tail that outgrows its
// height. A long list would then cut the clause that says what y does.
// The LEDGER section of the body lists every ID.
const maintTailIDs = 3

// maintTailIDList names at most maintTailIDs proposals and counts the
// others, for example "p1000, p1001, p1002, … (+148)".
func maintTailIDList(t *Theme, ids []int64) string {
	if len(ids) <= maintTailIDs {
		return propIDList(ids)
	}

	return propIDList(ids[:maintTailIDs]) + ", " + t.Glyphs.Ellipsis + " (+" + itoa(len(ids)-maintTailIDs) + ")"
}

// recoveryView renders the ledger-only completion of a partial
// operation. A fresh read proved the result of this session, or the
// view says which proof failed.
func (m *maintModal) recoveryView(w int) modalOpts {
	t := m.st.theme
	p := m.preview
	ids := propIDList(maintLedgerIDs(p))
	sep := " " + t.Glyphs.Sep + " "
	gate := maintRecoveryGate(p)

	var lead string

	// A gate that alone blocks y leaves the proof valid. The lead says
	// so, because a user who reads "not proven" can drop the only proof.
	switch {
	case gate != "":
		lead = "A fresh read proved the result of the earlier operation, but " + gate + ", so y is off. " +
			"Nothing was changed."
	case !p.Confirmable:
		lead = "The result of the earlier operation is not proven now, so y is off. Nothing was changed."
	case p.Action == maintain.ActionPrune:
		lead = "The pruned pin is still absent from " + pinfile.File + ", and this session removed it. Seamark " +
			"records " + ids + " as superseded in the ledger only; the pin file stays as it is. A fresh read " +
			"proved this just now, and the commit checks it again. Nothing changes before you press y."
	default:
		lead = "The pin file still holds the result of this session's retarget. Seamark records the new regions " +
			"of " + ids + " in the ledger only; the pin file stays as it is. A fresh read proved this just now, " +
			"and the commit checks it again. Nothing changes before you press y."
	}

	body := propLines(t.Body, lead, w)

	if p.Confirmable && m.st.busy() {
		body = append(body, "")
		body = append(body, propNote(t, ToneAttention, m.st.mutation+" is running. y is off until it ends.", w)...)
	}

	body = append(body, m.recoveryBlockers(w)...)
	body = append(body, "", t.Heading.Render("PROOF"))

	for i, it := range p.Items {
		if i > 0 {
			body = append(body, "")
		}

		body = append(body, m.proofLines(it, w)...)
	}

	body = append(body, maintLedgerLines(t, p, w)...)

	if p.Confirmable {
		will := effect{glyph: "~", tone: ToneAttention, text: "record the new regions of " + ids + " in the ledger"}
		if p.Action == maintain.ActionPrune {
			will = effect{glyph: t.Glyphs.Del, tone: ToneFailure, text: "move " + ids +
				" from applied to superseded in the ledger"}
		}

		body = append(body, t.effectLines([]effect{will}, []string{
			"change " + pinfile.File, "retry by itself after a failure", "write anything before you press y",
		}, w)...)
	}

	// The pinned tail names a bounded ID list, as the plan tail does.
	tail := propLines(t.Body, "Ledger only"+sep+maintTailIDList(t, maintLedgerIDs(p))+sep+
		"y completes the ledger update", w)

	switch {
	case gate != "":
		tail = propNote(t, ToneAttention, "Ledger only"+sep+"y is off: "+gate, w)
	case !p.Confirmable:
		tail = propNote(t, ToneAttention, "Ledger only"+sep+"y is off: the result is not proven", w)
	case m.st.busy():
		tail = propNote(t, ToneAttention, "Ledger only"+sep+"y waits: another operation is running", w)
	}

	return modalOpts{
		Title: t.TabActive.Render("Complete") + " " + t.Body.Render("the ledger update"+sep+ids),
		Right: t.Muted.Render("ledger only"), Body: body, Tail: tail, Actions: m.previewActions(false),
	}
}

// recoveryBlockers explains why a completion cannot be confirmed and
// names the manual route. Esc keeps the offer, so the user can check
// the proof again later in this session.
func (m *maintModal) recoveryBlockers(w int) []string {
	t := m.st.theme
	p := m.preview

	if p.Confirmable {
		return nil
	}

	var out []string

	note := func(text string) {
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, text, w)...)
	}

	switch {
	case p.GateErr != nil:
		note("distill.write cannot be read: " + errText(p.GateErr) + ". Fix " + settingsFile + " first. esc " +
			"returns to the result, which keeps this offer; press c there to check the proof again.")
	case !p.Write:
		note("distill.write is off. Press o for Settings " + t.Glyphs.Arrow + " Pin writing; after a save, press " +
			"c on the result to check the proof again.")
	}

	if maintNotReady(p.Items) > 0 {
		note("Reconcile by hand. " + maintManualRoute(p.Action, propIDList(maintLedgerIDs(p))) + " esc returns to " +
			"the result, which keeps this offer until esc there drops it.")
	}

	return out
}

// maintRecoveryGate names the write gate when it alone blocks a
// completion: every item is proven, and distill.write is off or cannot be
// read. It returns "" for a failed proof, because the proof decides
// first: a valid gate cannot help an unproven result.
func maintRecoveryGate(p *maintain.Plan) string {
	switch {
	case p.Confirmable, len(p.Items) == 0, maintNotReady(p.Items) > 0:
		return ""
	case p.GateErr != nil:
		return "distill.write cannot be read"
	case !p.Write:
		return "distill.write is off"
	}

	return ""
}

// maintManualRoute is the manual reconciliation of a partial operation.
// It names both sides, because the file and the ledger disagree, and the
// Pins key of a new preview. The warning against a repeat holds only
// while the file keeps the change, so it says so.
func maintManualRoute(a maintain.Action, ids string) string {
	check := "Check the file with git diff " + pinfile.File + ". "

	if a == maintain.ActionPrune {
		return check + "If a pin is gone while the ledger still lists its proposal (" + ids + ") as applied, Pins " +
			"shows that proposal under not present. To reconcile, restore the entry with git if it is gone, then " +
			"press " + maintPruneKey + " in Pins for a new preview; or keep the file and accept the mismatch. Do not " +
			"prune again while the file keeps the change."
	}

	return check + "If the file holds the new regions while the ledger still records the earlier ones (" + ids +
		"), Pins shows the mismatch. To reconcile, restore the earlier entry with git if it changed, then press " +
		maintRetargetKey + " in Pins for a new preview; or keep the file and accept the mismatch. Do not retarget " +
		"again while the file keeps the change."
}

// proofLines renders the proof of one item of a completion.
func (m *maintModal) proofLines(it maintain.PlanItem, w int) []string {
	t := m.st.theme

	out := maintHeadLines(t, maintRule(it), maintItemBadge(t, it.Status, true), w)

	if it.Status != maintain.ItemReady {
		return append(out, propNote(t, ToneAttention, ovSentenceEnd(oneLine(it.Reason)), w)...)
	}

	proof := "The file holds the reviewed entry, and its earlier regions are gone."
	if m.preview.Action == maintain.ActionPrune {
		proof = "The pin is absent from the file, and this session removed it."
	}

	return append(out, propLines(t.Muted, proof, w)...)
}

// resultView reports what a commit changed. The file, the ledger and a
// restore have separate rows, and the title never claims a change that
// did not happen. An offered recovery is named in the lead, the Recovery
// row and the tail, which also say that esc drops it: the tail stays
// visible at every size, and the proof cannot come back.
func (m *maintModal) resultView(w int) modalOpts {
	t := m.st.theme
	o := m.out
	v := modalOpts{Actions: m.resultActions(false)}

	if o.err != nil {
		return m.refusalView(w)
	}

	title, tone, lead := m.outcome(o)
	v.Title = t.ToneStyle(tone).Render(title)

	body := propNote(t, tone, lead, w)
	body = append(body, "", t.Heading.Render("WHAT HAPPENED"))
	body = append(body, propKV(t, m.resultFacts(o), w)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	v.Body = body
	v.Tail = m.resultTail(o, tone, w)

	return v
}

// refusalView reports a commit that wrote nothing.
func (m *maintModal) refusalView(w int) modalOpts {
	t := m.st.theme
	o := m.out

	body := propNote(t, ToneAttention, maintRefusal(t, o.err), w)
	body = append(body, "")
	body = append(body, propLines(t.Muted, "Details: "+errText(o.err), w)...)

	// The title and the tail say "written", not "changed": the lead can
	// say that something else changed since the preview.
	next := "Nothing was written"

	switch {
	case m.reviewAgain():
		body = append(body, "")
		body = append(body, propLines(t.Body, "Press enter to build a new preview from the current state.", w)...)
		next += " " + t.Glyphs.Sep + " enter previews again"
	case m.offer != nil:
		body = append(body, "")
		body = append(body, propLines(t.Body, "Press c to check the proof again. esc drops the completion, and it "+
			"cannot come back.", w)...)
		next += " " + t.Glyphs.Sep + " c checks the proof again " + t.Glyphs.Sep + " esc drops it"
	}

	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return modalOpts{
		Title: t.Attention.Render("Not written"), Body: body, Actions: m.resultActions(false),
		Tail: propNote(t, ToneAttention, next, w),
	}
}

// maintRefusal maps a commit refusal to one sentence. Every refusal
// means that nothing was written.
func maintRefusal(t *Theme, err error) string {
	switch {
	case errors.Is(err, maintain.ErrStale):
		return "Something changed since the preview" + propDash(t) + "preview again. Nothing was written."
	case errors.Is(err, maintain.ErrWriteDisabled):
		return "The preview was made while distill.write was off, so nothing was written. Only a new preview " +
			"can authorize a write."
	case errors.Is(err, maintain.ErrNotConfirmable):
		return "The preview cannot be confirmed, so nothing was written. Revise the selection and preview again."
	case errors.Is(err, ErrBusy):
		return "Another operation is running, so this one did not start."
	case errors.Is(err, ErrNoLedger), errors.Is(err, maintain.ErrNoLedger):
		return "The index database is not readable, so nothing was changed."
	default:
		return "The operation was refused before any write."
	}
}

// outcome titles a commit outcome and states what holds now and the
// next safe step.
func (m *maintModal) outcome(o *maintOutcome) (title string, tone Tone, lead string) {
	res := o.res
	n := propPlural(len(o.plan.Items), "pin", "pins")
	ids := propIDList(maintLedgerIDs(o.plan))
	file := pinfile.File
	prune := o.plan.Action == maintain.ActionPrune
	open := maintOpenKey(o.plan.Action)

	done := "records the new regions"
	if prune {
		done = "is superseded"
	}

	switch {
	case o.recovery && res.LedgerDone:
		return "Ledger completed", ToneSuccess, "The ledger now matches the pin file: " + ids + " " + done + "."
	case o.recovery:
		return "Ledger update failed again", ToneFailure, "Nothing changed. Press c to check the proof again. esc " +
			"drops the completion, and it cannot come back."
	case res.LedgerDone && len(res.Moved) > 0 && prune:
		return "Pruned " + n, ToneSuccess, "Removed " + n + " from " + file + " and recorded " +
			propIDList(res.Moved) + " as superseded. Review the diff and commit it."
	case res.LedgerDone && len(res.Moved) > 0:
		return "Retargeted " + n, ToneSuccess, "Wrote the new regions of " + n + " to " + file + " and recorded " +
			"them for " + propIDList(res.Moved) + ". Review the diff and commit it."
	case res.LedgerDone && res.FileErr != nil:
		// The write reported a failure, and the result cannot tell whether
		// the file holds the change. So the lead claims no file change.
		return "Ledger completed elsewhere", ToneAttention, "Another writer already recorded the same ledger " +
			"change. The pin file write reported a failure, so Seamark cannot say what the file holds. Check it " +
			"with git diff " + file + " before you rely on it: commit the change if it is there; else Pins shows " +
			"the mismatch."
	case res.LedgerDone:
		return "Ledger completed elsewhere", ToneAttention, "The pin file changed, and another writer already " +
			"recorded the same ledger change. Nothing more is needed; review the diff and commit it."
	case res.Restored:
		step, row := "pin file write", "File error"
		if res.LedgerErr != nil {
			step, row = "ledger update", "Ledger error"
		}

		return "Nothing changed", ToneAttention, "The " + step + " failed, so Seamark put the earlier pin file back. " +
			"The pin file and the ledger hold their earlier state. Fix the cause named under " + row + ", then " +
			"press " + open + " in Pins for a new preview."
	case res.Partial():
		// The engine offers a recovery only after it saw its own write
		// land. A write error without one means a file that nobody read.
		return maintPartialOutcome(o.plan.Action, ids, m.offer != nil, res.FileErr != nil && res.Recovery == nil)
	case res.FileErr != nil:
		lead = "The pin file was not changed, and the ledger was not changed."
		if errors.Is(res.FileErr, maintain.ErrStale) {
			lead += " Another writer changed " + file + " first. Press enter for a new preview from the current file."
		} else {
			lead += " Fix the cause named under File error, then press " + open + " in Pins for a new preview."
		}

		return "Pin file not written", ToneFailure, lead
	}

	return "Nothing changed", ToneAttention, "No pin and no proposal changed."
}

// maintPartialOutcome titles a commit that changed the pin file, or could
// not check it, and did not complete the ledger. offer is a session
// recovery, which proves the file change. unread is a failed write that
// no read confirmed, so its title and lead claim no file change. Without
// an offer, the lead names both sides and the manual route.
func maintPartialOutcome(a maintain.Action, ids string, offer, unread bool) (title string, tone Tone, lead string) {
	verb := "retarget"

	title = "Regions written, ledger incomplete"
	if a == maintain.ActionPrune {
		verb, title = "prune", "Pin removed, ledger incomplete"
	}

	switch {
	case offer:
		return title, ToneFailure, "The pin file changed, but the ledger update did not complete. Do not " + verb +
			" again: the change is already in the file. Press c to preview the ledger-only completion; it checks " +
			"the file and the rows again before any write. esc drops the completion, and it cannot come back."
	case unread:
		return "Pin file state unknown, ledger incomplete", ToneFailure, "The pin file write reported a failure, " +
			"and the file could not be read again, so its state is unknown. This operation did not change the " +
			"ledger. No completion can be proven, so none is offered. " + maintManualRoute(a, ids)
	}

	return title, ToneFailure, "The pin file changed, but the ledger update did not complete. No completion can " +
		"be proven, so none is offered. " + maintManualRoute(a, ids)
}

// resultFacts lists the file, the ledger and the restore outcomes on
// separate rows, then the rows as read again and the engine note. The
// file row uses the "Pin file" label of the Proposals result, so the
// two results of Lessons name the file the same way.
func (m *maintModal) resultFacts(o *maintOutcome) [][2]string {
	res := o.res
	prune := o.plan.Action == maintain.ActionPrune

	var rows [][2]string

	switch {
	case o.recovery:
		rows = append(rows, [2]string{"Pin file", "unchanged: a ledger-only completion never writes it"})
	case res.Restored:
		rows = append(rows, [2]string{"Pin file", "restored: it holds its earlier content again"})
	case res.FileWritten && res.FileErr != nil:
		rows = append(rows, [2]string{"Pin file", "changed or not checked: the write reported a failure"})
	case res.FileWritten:
		removed := "retargeted"
		if prune {
			removed = "removed"
		}

		rows = append(rows, [2]string{"Pin file", "written: " + pinfile.File + " (" +
			propPlural(len(o.plan.Items), "pin", "pins") + " " + removed + ")"})
	default:
		rows = append(rows, [2]string{"Pin file", "not changed"})
	}

	if res.FileErr != nil {
		rows = append(rows, [2]string{"File error", maintErrText(res.FileErr)})
	}

	ledger, rowsNow := m.ledgerFact(o)
	rows = append(rows, [2]string{"Ledger", ledger})

	if res.LedgerErr != nil {
		rows = append(rows, [2]string{"Ledger error", maintErrText(res.LedgerErr)})
	}

	if row, ok := maintRestoreRow(o); ok {
		rows = append(rows, [2]string{"Restore", row})
	}

	switch {
	case res.ObservedErr != nil:
		rows = append(rows, [2]string{"Rows now", "unknown: " + maintErrText(res.ObservedErr)})
	case len(res.Observed) > 0 || rowsNow:
		rows = append(rows, [2]string{"Rows now", maintObserved(o.plan, res.Observed)})
	}

	if res.Note != "" {
		rows = append(rows, [2]string{"Note", ovSentenceEnd(oneLine(res.Note))})
	}

	switch {
	case m.offer != nil:
		rows = append(rows, [2]string{"Recovery", "available: c previews the ledger-only completion; esc drops it"})
	case res.Partial():
		rows = append(rows, [2]string{"Recovery", "none can be proven: reconcile by hand"})
	}

	return rows
}

// ledgerFact returns the Ledger row of a result. It says that the rows
// keep their earlier values only when that is known: the file was
// restored or never written, or the rows were read again and each one
// holds its planned values. Otherwise another writer may have changed a
// row, so the row points to the rows as read again (rowsNow).
func (m *maintModal) ledgerFact(o *maintOutcome) (text string, rowsNow bool) {
	res := o.res
	ids := propIDList(maintLedgerIDs(o.plan))

	switch {
	case res.LedgerDone && len(res.Moved) > 0:
		return maintTargetText(m.st.theme, o.plan, res.Moved), false
	case res.LedgerDone:
		return "completed by another writer: " + maintTargetText(m.st.theme, o.plan, maintLedgerIDs(o.plan)), false
	}

	still := "still records the earlier regions"
	if o.plan.Action == maintain.ActionPrune {
		still = "still applied"
	}

	// The engine owns the row comparison, so the result and the guarded
	// transaction never disagree on a row.
	held, _ := maintain.RowsHoldPlanned(o.plan, res.Observed)

	switch {
	case res.Restored, !o.recovery && !res.FileWritten:
		// The engine restores only over rows that hold their planned
		// values, and a file that was not written never reaches the
		// ledger step.
		return "no change: " + ids + " " + still, false
	case res.ObservedErr != nil:
		return "not changed by this operation; current state unknown", false
	case held:
		return "no change: " + ids + " " + still, false
	}

	return "not changed by this operation; current rows: see Rows now", true
}

// maintTargetText renders the target values of the planned ledger rows in
// ids, for example "p4 internal/store › internal/store/sql" or "p16 ›
// superseded". A retarget names its regions, so the result shows the
// values that the ledger holds now, as the preview LEDGER section did.
func maintTargetText(t *Theme, p *maintain.Plan, ids []int64) string {
	arrow := " " + t.Glyphs.Arrow + " "

	if p.Action == maintain.ActionPrune {
		return propIDList(ids) + arrow + domain.ProposalSuperseded
	}

	var parts []string

	for _, id := range ids {
		k := slices.IndexFunc(p.Retarget, func(c store.RegionChange) bool { return c.Guard.ID == id })
		if k < 0 {
			continue
		}

		c := p.Retarget[k]
		parts = append(parts, propID(id)+" "+maintGuardRegions(c.Guard)+arrow+maintRegionText(c.Regions))
	}

	// A moved row outside the plan has no planned regions to name.
	if len(parts) < len(ids) {
		return propIDList(ids) + arrow + "new regions"
	}

	return strings.Join(parts, "; ")
}

// maintErrText renders an error for a result row. errors.Join breaks its
// parts with newlines, and oneLine would run them into one clause. So
// each part of a join ends with "; ", and the user sees where one
// failure ends and the next starts.
func maintErrText(err error) string {
	parts := maintJoined(err)
	texts := make([]string, 0, len(parts))

	for _, e := range parts {
		texts = append(texts, errText(e))
	}

	return strings.Join(texts, "; ")
}

// maintJoined returns the parts of an errors.Join error, else err alone.
// fmt.Errorf with several %w also unwraps to a list, but its text is one
// sentence. So only a list whose texts join to the error text counts.
func maintJoined(err error) []error {
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}

	errs := j.Unwrap()
	texts := make([]string, len(errs))

	for i, e := range errs {
		texts[i] = e.Error()
	}

	if len(errs) == 0 || strings.Join(texts, "\n") != err.Error() {
		return []error{err}
	}

	var out []error
	for _, e := range errs {
		out = append(out, maintJoined(e)...)
	}

	return out
}

// maintRestoreRow returns the restore outcome of a commit, when a
// restore was due: never "restored" unless the restore write succeeded.
func maintRestoreRow(o *maintOutcome) (string, bool) {
	res := o.res

	switch {
	case o.recovery:
		return "", false
	case res.Restored:
		return "done: the earlier pin file is back", true
	case res.RestoreRefused != "":
		return "refused: " + oneLine(res.RestoreRefused), true
	case res.RestoreErr != nil:
		return "failed: " + maintErrText(res.RestoreErr), true
	case res.Partial():
		return "not attempted: nothing was restored", true
	}

	return "", false
}

// maintObserved renders the planned ledger rows as a commit read them
// again, for example "p4 applied, internal/store". A planned row that the
// read did not return shows as missing, so a gone row is never hidden.
func maintObserved(p *maintain.Plan, rows []domain.Proposal) string {
	row := func(r domain.Proposal) string {
		return propID(r.ID) + " " + oneLine(r.Status) + ", " + maintRegionText(r.RegionSet())
	}

	var parts []string

	planned := maintLedgerIDs(p)

	for _, id := range planned {
		k := slices.IndexFunc(rows, func(r domain.Proposal) bool { return r.ID == id })
		if k < 0 {
			parts = append(parts, propID(id)+" not in the ledger")

			continue
		}

		parts = append(parts, row(rows[k]))
	}

	// The engine reads only the planned rows, so this shows a row outside
	// the plan only when a caller passes one.
	for _, r := range rows {
		if !slices.Contains(planned, r.ID) {
			parts = append(parts, row(r))
		}
	}

	return strings.Join(parts, "; ")
}

// resultTail is the outcome summary under the rule: the file state, the
// ledger state, and the next key.
func (m *maintModal) resultTail(o *maintOutcome, tone Tone, w int) []string {
	t := m.st.theme
	res := o.res
	sep := " " + t.Glyphs.Sep + " "

	var text string

	// An offered recovery names esc too: the tail stays visible at every
	// size, and esc drops a proof that cannot come back. A file write
	// that reported a failure claims no file change.
	switch {
	case o.recovery && res.LedgerDone:
		text = "Ledger completed" + sep + "file unchanged"
	case o.recovery && m.offer != nil:
		text = "Ledger unchanged" + sep + "c checks the proof again" + sep + "esc drops it"
	case o.recovery:
		text = "Ledger unchanged"
	case res.LedgerDone && len(res.Moved) > 0:
		text = "File written" + sep + "ledger updated"
	case res.LedgerDone && res.FileErr != nil:
		text = "File changed or not checked" + sep + "ledger completed elsewhere"
	case res.LedgerDone:
		text = "File written" + sep + "ledger completed elsewhere"
	case res.Restored:
		text = "File restored" + sep + "ledger unchanged"
	case res.Partial() && m.offer != nil:
		text = "File changed" + sep + "ledger incomplete" + sep + "c previews completion" + sep + "esc drops it"
	case res.Partial() && res.FileErr != nil:
		text = "File not checked" + sep + "ledger incomplete" + sep + "fix by hand"
	case res.Partial():
		text = "File changed" + sep + "ledger incomplete" + sep + "fix by hand"
	default:
		text = "File unchanged" + sep + "ledger unchanged"
	}

	return propNote(t, tone, text, w)
}

// maintHeadLines renders an item head: the rule, wrapped so it shows in
// full, with the state badge at the right of the first line.
func maintHeadLines(t *Theme, rule, badge string, w int) []string {
	room := max(4, w-width(badge)-2)
	segs := strings.Split(wrap(rule, room), "\n")

	out := []string{spread(t.Title.Render(segs[0]), badge, w)}
	for _, s := range segs[1:] {
		out = append(out, t.Title.Render(s))
	}

	return out
}

// maintItemBadge is the state of a selected pin as a glyph and a word,
// so it reads without color. proof selects the words of a completion.
func maintItemBadge(t *Theme, s maintain.ItemStatus, proof bool) string {
	switch {
	case s == maintain.ItemReady && proof:
		return t.Badge(ToneSuccess, "proven")
	case s == maintain.ItemReady:
		return t.Badge(ToneSuccess, "ready")
	case s == maintain.ItemNoOp:
		return t.Badge(ToneNeutral, "no-op")
	case proof:
		return t.Badge(ToneAttention, "not proven")
	}

	return t.Badge(ToneAttention, "blocked")
}

// maintWhere names where a selected pin is and the proposals it links,
// for example ".seamark/lessons.yaml:12 · linked p4". An entry without a
// line changed after the review; goneText names it instead.
func maintWhere(t *Theme, it maintain.PlanItem) string {
	e := it.Pin.Entry

	where := pinfile.File + ":" + itoa(e.Line)
	if ids := maintItemIDs(it); len(ids) > 0 {
		where += " " + t.Glyphs.Sep + " linked " + propIDList(ids)
	}

	return where
}

// maintRule returns the sanitized rule of an item. An entry that changed
// since the review has no live content, so the reviewed key names it.
func maintRule(it maintain.PlanItem) string {
	if it.Pin.Entry.Rule != "" {
		return oneLine(it.Pin.Entry.Rule)
	}

	return oneLine(it.Ref.Key.Rule)
}

// maintSubject names the selection in a title: the ID and the rule of
// one pin, or the count of several.
func maintSubject(t *Theme, items []maintain.PlanItem) string {
	if len(items) != 1 {
		return propPlural(len(items), "pin", "pins")
	}

	it := items[0]
	if ids := maintItemIDs(it); len(ids) > 0 {
		return propIDList(ids) + " " + t.Glyphs.Sep + " " + maintRule(it)
	}

	return maintRule(it)
}

// maintItemIDs returns the applied proposals that an item links.
func maintItemIDs(it maintain.PlanItem) []int64 {
	if len(it.Rows) > 0 {
		return maintAppliedIDs(it.Rows)
	}

	return maintAppliedIDs(it.Pin.Link.Applied)
}

// maintAppliedIDs returns the IDs of proposals in their order.
func maintAppliedIDs(ps []domain.Proposal) []int64 {
	ids := make([]int64, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}

	return ids
}

// maintLedgerIDs returns the proposals of the planned ledger changes.
func maintLedgerIDs(p *maintain.Plan) []int64 {
	var ids []int64

	for _, c := range p.Retarget {
		ids = append(ids, c.Guard.ID)
	}

	for _, g := range p.Supersede {
		ids = append(ids, g.ID)
	}

	return ids
}

// maintNotReady counts the items that y cannot change.
func maintNotReady(items []maintain.PlanItem) int {
	n := 0

	for _, it := range items {
		if it.Status != maintain.ItemReady {
			n++
		}
	}

	return n
}

// maintRegionText renders a region set for prose, sanitized. An empty
// set is repository-wide.
func maintRegionText(set []string) string {
	parts := make([]string, 0, len(set))

	for _, r := range set {
		if r = oneLine(r); r != "" {
			parts = append(parts, r)
		}
	}

	if len(parts) == 0 {
		return deltaRepoWide
	}

	return strings.Join(parts, ", ")
}

// maintGuardRegions renders the region set that a ledger row records.
func maintGuardRegions(g store.RowGuard) string {
	return maintRegionText(domain.Proposal{Region: g.Region, Regions: g.Regions}.RegionSet())
}

// maintEntryRegions returns the region set of a live entry, as the
// delivery reader reads it.
func maintEntryRegions(e pinfile.Entry) []string {
	return reviews.PinRule{Rule: e.Rule, Region: e.Region, Regions: e.Regions}.AllRegions()
}

// maintCounts renders the added and removed line counts of a diff, each
// with its sign, so the count reads without color.
func maintCounts(t *Theme, added, removed int) string {
	var parts []string

	if added > 0 {
		parts = append(parts, t.Success.Render(t.Glyphs.Add+itoa(added)))
	}

	if removed > 0 {
		parts = append(parts, t.Failure.Render(t.Glyphs.Del+itoa(removed)))
	}

	if len(parts) == 0 {
		return t.Faint.Render("0 lines")
	}

	return strings.Join(parts, " ")
}
