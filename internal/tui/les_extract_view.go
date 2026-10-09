package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// This file renders a trigger extraction record: the Preview step (the
// preflight of a paid run), the run, the result, and the stop question.
// It reuses the distillation vocabulary and the shared parts of
// les_agent.go, so both paid runs read alike.
// Every proposal text, path, reason and error comes from a file, the
// ledger or a model, so each one is sanitized where it is rendered.

// Column widths of the Proposals panes.
const (
	// extIDMinW fits "p" and four digits with a gap.
	extIDMinW = 6
	// extStateW fits "pending" and "applied" with a gap.
	extStateW = 9
	// extReasonW fits "older negative" with a gap.
	extReasonW = 15
	// extReasonMin is the narrowest box that shows the reason column.
	// A narrower box gives the room to the rule; the detail shows the
	// reason.
	extReasonMin = 56
	// extRuleMin is the least rule text beside the "note edited" word
	// that keeps the reason column. It fits a short rule name.
	extRuleMin = 16
	// extOutcomeMin is the narrowest outcome column.
	extOutcomeMin = 18
)

// extSentText says what leaves the machine. The console sends what the
// CLI sends (contracts D-18). The words follow the shared prompt
// builder: it writes the item ID, the rule, the note and the evidence
// paths. The finding metadata has no line number and no finding text.
const extSentText = "Sent per proposal: its ID, rule, recorded note and cited evidence paths; no finding text. " +
	"The agent can also load the project instructions. No cost figure is available."

// extNoteEdited marks a row whose live pin note differs from the
// recorded note. It is a word, not a glyph, because the run table uses
// "!" for outcome states (spec R-14). Pins uses the same word.
const extNoteEdited = "note edited"

// extNoteLegend explains extNoteEdited under a table. It holds for every
// outcome, also for a row that the run never asked.
const extNoteLegend = "the live note in lessons.yaml differs from the recorded note that the question sends"

// view renders the context line, a blank line and the step. The boxes
// leave the last row blank above the footer, as the other sub-views do.
func (r *extRecord) view(w, h int) string {
	boxH := max(4, h-3)

	var body string

	switch r.step {
	case stepPreparing, stepPreview:
		body = r.previewView(w, boxH)
	default:
		body = r.runView(w, boxH)
	}

	return strings.Join([]string{r.contextLine(w), "", body}, "\n")
}

// contextLine renders the facts of the record at the left and the step
// trail at the right. A narrow line keeps only the current step, for
// example "● Review 1/3", and cuts the facts to fit it.
func (r *extRecord) contextLine(w int) string {
	t := r.st.theme
	g := t.Glyphs
	left := " " + r.facts(w < wideWidth)

	steps, cur := runTrail(false), r.step.stepper(false)

	trail := t.stepper(steps, cur)
	if width(left)+width(trail)+3 > w {
		trail = t.TabActive.Render(g.Dot+" "+steps[cur]) + t.Faint.Render(" "+itoa(cur+1)+"/"+itoa(len(steps)))
	}

	room := w - width(trail) - 3

	switch {
	case width(left) <= room:
	case room >= agentFactsMin:
		left = truncate(left, room, g.Ellipsis)
	default:
		return truncate(left, w, g.Ellipsis)
	}

	return spread(left, trail+" ", w)
}

// facts renders the context facts: the selection, the agent and the
// time of the observation. compact joins the values without labels.
func (r *extRecord) facts(compact bool) string {
	t := r.st.theme
	sep := " " + t.Glyphs.Sep + " "

	type fact struct{ label, value string }

	n := itoa(r.selected())
	if r.cursor {
		// D-15: a preview names the cursor row that stands in for an
		// empty selection.
		n += " (cursor row)"
	}

	facts := []fact{{"selected", n}}

	if compact {
		facts[0] = fact{"", itoa(r.selected()) + " selected"}

		if r.cursor {
			facts[0].value += " (cursor row)"
		}
	}

	if p := r.prev.preview; p != nil && r.prev.err == nil {
		facts = append(facts, fact{"agent", oneLine(p.Invocation.Name)})
	}

	switch {
	case r.step == stepPreparing:
		facts = append(facts, fact{"", "planning" + t.Glyphs.Ellipsis})
	case r.step == stepRunning:
		facts = append(facts, fact{"started", agentClock(r.run.started)})
	case r.step == stepResult:
		facts = append(facts, fact{"finished", agentClock(r.run.ended)})
	case r.prev.preview != nil && !r.prev.preview.PlannedAt.IsZero():
		facts = append(facts, fact{"planned", agentClock(r.prev.preview.PlannedAt)})
	}

	parts := make([]string, 0, len(facts))

	for _, f := range facts {
		switch {
		case compact || f.label == "":
			parts = append(parts, t.Muted.Render(f.value))
		default:
			parts = append(parts, t.Faint.Render(f.label)+" "+t.Muted.Render(f.value))
		}
	}

	if compact {
		return strings.Join(parts, t.Muted.Render(sep))
	}

	return t.Title.Render("Trigger extraction") + "     " + strings.Join(parts, "     ")
}

// previewView renders the Preparing wait, a plan failure, the empty
// preview, or the plan with its proposals.
func (r *extRecord) previewView(w, h int) string {
	t := r.st.theme
	pl := &r.prev

	switch {
	case r.step == stepPreparing:
		o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

		return integBoxed(t, o, ToneNeutral, "Planning the trigger extraction"+t.Glyphs.Ellipsis,
			"Seamark reads the index, the pins and the agent configuration. Nothing is sent or written.",
			t.hints([]key.Binding{withHelp(nkeys.Back, "esc", "cancel")}))
	case pl.err != nil:
		return r.planErrBox(w, h)
	case pl.preview.NoOp():
		return r.nothingBox(w, h)
	}

	if r.st.wide() {
		leftW := max(34, setupSplit(w, agentPlanW))

		return hjoin(1, r.planBox(leftW, h, pl.planFocus), r.itemsBox(w-leftW-1, h, !pl.planFocus, false))
	}

	// The compact Proposals pane replaces the Plan pane, so it pins the
	// scope summary and the start line itself (contracts D-24).
	if pl.listOpen {
		return r.itemsBox(w, h, true, true)
	}

	return r.planBox(w, h, true)
}

// planErrBox explains a preview that could not be made. Each cause
// names its route: Settings or the pin file for a configuration error,
// the index build for an index that is not ready. Nothing was sent.
func (r *extRecord) planErrBox(w, h int) string {
	t := r.st.theme
	err := r.prev.err
	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

	title := "Could not plan the trigger extraction"
	body := errText(err) + "\n\nNothing was sent or written."

	// A configuration or an index that blocks the plan is a blocked
	// state, not a failure (design map §9), so it takes the attention
	// color. Other errors keep the failure color.
	tone := ToneFailure

	var cfg *learning.ExtractConfigError

	switch {
	case errors.As(err, &cfg):
		route := "Edit " + oneLine(cfg.Path) + ", then plan again."

		switch cfg.Path {
		case settingsFile:
			route = "Press o to fix it in Settings, or edit " + oneLine(cfg.Path) + ", then plan again."
		case pinfile.File:
			route = "Edit " + oneLine(cfg.Path) + ", then plan again. Press o to see where Pins fails to read it."
		}

		title, tone = "The configuration blocks the plan", ToneAttention
		body = errText(err) + "\n\nThe console does not fall back to defaults for a paid run. " + route +
			" Nothing was sent or written."
	case errors.Is(err, learning.ErrExtractNotReady):
		// The title says that the index is not ready, so the body shows
		// only the cause. The minimum layout then keeps the last sentence.
		cause := errText(err)
		if c, ok := strings.CutPrefix(cause, learning.ErrExtractNotReady.Error()+": "); ok && c != "" {
			cause = c
		}

		title, tone = "The index is not ready", ToneAttention
		body = cause + "\n\nPress o to build it from Overview, then plan again. Nothing was sent or written."
	case errors.Is(err, distill.ErrStale):
		title = "The workspace changed while planning"
		body = extStaleText(err) + "\n\nPlan again to read the current state. Nothing was sent or written."
	}

	next := t.hints([]key.Binding{
		withHelp(nkeys.Enter, "enter", "plan again"), r.linkKey(), withHelp(nkeys.Back, "esc", "close"),
	})

	return integBoxed(t, o, tone, title, body, next)
}

// planBox renders the Plan pane. It shows the notes that stop a start,
// the facts, the effects, the sent data and the possible scope changes.
// The scope summary stays pinned above the start line.
func (r *extRecord) planBox(w, h int, focused bool) string {
	t := r.st.theme
	inner := boxInner(w)
	p := r.prev.preview

	// The notes that stop a start come first, so a short box shows them.
	body := r.planNotes(inner)
	body = append(body, r.planFacts(inner)...)
	body = append(body, t.effectLines(r.planWill(), nil, inner)...)
	body = append(body, "", integHeading(t, "Sent data"))
	body = append(body, integLines(t.Body, extSentText, inner)...)
	body = append(body, "", integHeading(t, "Scope"))
	body = append(body, r.scopeLines(inner)...)
	body = append(body, t.effectLines(nil, r.planWont(), inner)...)

	tail := r.previewTail(inner, len(body), h)

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Plan", focused),
		Right: t.Muted.Render(propPlural(len(p.Plan.Batches), "batch", "batches")),
	}

	return t.scrollBox(o, body, tail, &r.prev.notes, [2]int{-1, -1}, false)
}

// scopeSummary is the confirmation scope of the review, for example
// "Sends 12 proposals in 2 batches · 3 ineligible". The Plan pane pins
// it above the start line, so the minimum layout shows what y starts.
func (r *extRecord) scopeSummary() string {
	p := r.prev.preview

	text := "Sends " + propPlural(len(p.Plan.IDs()), "proposal", "proposals")
	if b := len(p.Plan.Batches); b > 0 {
		text += " in " + propPlural(b, "batch", "batches")
	}

	return text + countSep(r.st.theme) + itoa(len(p.Ineligible())) + " ineligible"
}

// previewTail returns the pinned tail of the Preview step: the scope
// summary and the start line under a rule. Both of its panes pin it, so
// the minimum layout always shows what y starts. A box that cannot hold
// body rows beside the tail gives up the blank line above the rule
// first.
func (r *extRecord) previewTail(inner, bodyRows, h int) []string {
	t := r.st.theme

	tail := []string{"", integRule(t, inner)}
	tail = append(tail, integLines(t.Title, r.scopeSummary(), inner)...)

	if r.runnable() {
		tail = append(tail, t.Body.Render("Nothing is sent until you press ")+t.Key.Render("y")+t.Body.Render("."))
	} else {
		tail = append(tail, integLines(t.Muted, "Nothing was sent. Press enter to plan again.", inner)...)
	}

	if bodyRows+len(tail) > boxRows(h) {
		tail = tail[1:]
	}

	return tail
}

// planFacts renders the key facts of the preview. Every count comes from
// the engine plan; the prompt size is the engine's estimate.
func (r *extRecord) planFacts(w int) []string {
	t := r.st.theme
	p := r.prev.preview
	pl := p.Plan

	sizes := make([]string, len(pl.Batches))
	for i, b := range pl.Batches {
		sizes[i] = itoa(len(b.Proposals))
	}

	// The sizes follow a colon, because the separator glyph reads as a
	// fraction in ASCII ("1 / 5").
	unit := " proposals"
	if len(pl.IDs()) == 1 {
		unit = " proposal"
	}

	agent := agentText(t, p.Invocation.Argv, agentKind(p.Invocation.Name, !r.prev.preset), 0)

	selected := t.Body.Render(itoa(len(pl.IDs())) + " eligible of " + itoa(r.selected()))
	if r.cursor {
		// The maintenance preview uses the same words (D-15).
		selected += t.Faint.Render(" " + t.Glyphs.Sep + " the cursor row; no row is marked")
	}

	rows := [][2]string{
		{"Selected", selected},
		{"Batches", t.Body.Render(propPlural(len(pl.Batches), "batch", "batches") + ": " + strings.Join(sizes, " + ") +
			unit)},
		{"Prompt", t.Body.Render(agentTokens(pl.Preflight.PromptChars)+" tokens") + " " + t.Faint.Render("(estimated)")},
		{"Agent", agent},
		{"Runs in", runsInText(t, p.Invocation.Dir, p.WorkDir, p.RepoRoot)},
	}

	return propKV(t, rows, w)
}

// planNotes renders what stops the start: a blocker of the preview, or
// the refusal of an earlier start. Each note says what to do.
func (r *extRecord) planNotes(w int) []string {
	t := r.st.theme

	var out []string

	// A blocker stops the start but is not a failure: design map §9
	// gives it the attention color, as the distillation review does.
	// Each kind of blocker names the step that removes it.
	for _, b := range r.prev.preview.Blockers {
		advice := ". Install it, or press o to choose another agent in Settings. Then press enter to plan again."
		if r.prev.preview.IsWorkDirBlocker(b) {
			advice = ". The agent runs in the directory of the console, so Settings cannot change it. Quit, then " +
				"start Seamark again from an existing directory."
		}

		out = append(out, propNote(t, ToneAttention, oneLine(b)+advice, w)...)
		out = append(out, "")
	}

	if err := r.prev.stale; err != nil {
		text := "The run did not start: " + errText(err) + ". Nothing was sent. Press enter to plan again."

		var saved extSettingsStale

		switch {
		case errors.As(err, &saved):
			// Nobody pressed y, so the note reports no refused start.
			text = "This preview is out of date: " + errText(err) + ". Nothing was sent. Press enter to plan again."
		case errors.Is(err, distill.ErrStale):
			text = "The preview is out of date: " + extStaleText(err) + ". Nothing was sent. Press enter to plan again."
		case errors.Is(err, ErrBusy):
			text = "Another operation holds the index: " + errText(err) + ". Nothing was sent. Press enter to plan " +
				"again when it ends."
		}

		out = append(out, propNote(t, ToneAttention, text, w)...)
		out = append(out, "")
	}

	return out
}

// planWill lists the effects of the run.
func (r *extRecord) planWill() []effect {
	t := r.st.theme
	g := t.Glyphs
	p := r.prev.preview

	// A custom agent.argv has no preset name, so the effect names the
	// setting, as the Agent fact does.
	agent := "the agent"
	if name := oneLine(p.Invocation.Name); name != "" {
		agent = name
	}

	if !r.prev.preset {
		agent = "the custom agent.argv command"
	}

	n, b := len(p.Plan.IDs()), len(p.Plan.Batches)

	return []effect{
		{glyph: g.Add, tone: ToneSuccess, text: "ask " + agent + " about " + propPlural(n, "proposal", "proposals") +
			" in " + propPlural(b, "call", "calls") + ", one per batch, in the order listed"},
		{glyph: g.Add, tone: ToneSuccess, text: "save each answer and its question stamp when its batch returns"},
	}
}

// scopeLines tells what the answers can change: the regions of pending
// proposals, and never the scope of an applied pin. A live note that
// differs from the recorded note is named here, so the compact layout
// shows it without the Proposals pane.
func (r *extRecord) scopeLines(w int) []string {
	t := r.st.theme
	p := r.prev.preview

	var pending, applied []int64

	type change struct {
		id       int64
		from, to []string
	}

	var changes []change

	for _, b := range p.Plan.Batches {
		for j, row := range b.Proposals {
			if row.Status == domain.ProposalApplied {
				applied = append(applied, row.ID)

				continue
			}

			pending = append(pending, row.ID)

			if it := b.Items[j]; it.CoverageChanges {
				changes = append(changes, change{id: row.ID, from: row.RegionSet(), to: it.Coverage})
			}
		}
	}

	var out []string

	// The words follow distill.RecomputeRegions. A confirmed trigger
	// gives its own region, which can be outside the current regions. An
	// answer without a confirmed trigger falls back to the coverage.
	if len(pending) > 0 {
		out = append(out, integLines(t.Body, "Pending "+propIDList(pending)+": a confirmed trigger replaces the "+
			"regions with its own region, which can be outside the current regions. A negative or unconfirmed "+
			"answer sets the evidence coverage. Apply stays a separate decision.", w)...)

		if len(changes) > 0 {
			out = append(out, t.Muted.Render("A negative or unconfirmed answer sets:"))

			for _, c := range changes {
				out = append(out, t.arrowRow(propID(c.id), extRegions(c.from), extRegions(c.to), extIDMinW, w)...)
			}
		}
	}

	if len(applied) > 0 {
		if len(out) > 0 {
			out = append(out, "")
		}

		out = append(out, integLines(t.Body, "Applied "+propIDList(applied)+": answers store trigger paths as advice "+
			"only. Installed pin scopes never change here; a retarget is a separate preview in Lessons "+
			t.Glyphs.Arrow+" Pins.", w)...)
	}

	for _, id := range r.liveNoteIDs() {
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, propID(id)+": the live note differs from the recorded note. "+
			"The question uses the recorded note.", w)...)
	}

	return out
}

// planWont lists what the run leaves alone.
func (r *extRecord) planWont() []string {
	return []string{
		"change an applied pin's scope or lessons.yaml",
		"ask a proposal whose question is already answered",
		"apply, dismiss or retarget a proposal",
		"retry a failed batch by itself",
	}
}

// liveNoteIDs returns the planned proposals whose live pin note differs
// from the recorded note, in send order.
func (r *extRecord) liveNoteIDs() []int64 {
	p := r.prev.preview
	if p == nil || p.Plan == nil || len(p.LiveNotes) == 0 {
		return nil
	}

	var ids []int64

	for _, id := range p.Plan.IDs() {
		if _, ok := p.LiveNotes[id]; ok {
			ids = append(ids, id)
		}
	}

	return ids
}

// liveNote reports whether the live note of proposal id differs from its
// recorded note in the reviewed preview.
func (r *extRecord) liveNote(id int64) bool {
	p := r.prev.preview
	if p == nil {
		return false
	}

	_, ok := p.LiveNotes[id]

	return ok
}

// extRegions names a region set in one line: "repo-wide" for the empty
// set, as the proposal list does.
func extRegions(set []string) string {
	if len(set) == 0 {
		return "repo-wide"
	}

	out := make([]string, len(set))
	for i, s := range set {
		out[i] = oneLine(s)
	}

	return strings.Join(out, ", ")
}

// extCols is the column layout of a Proposals pane. A zero width hides
// an optional column.
type extCols struct {
	idW, ruleW, reasonW int
}

// itemCols fits the review columns into w cells. A narrow pane drops the
// reason column, so the rule keeps a readable width.
func (r *extRecord) itemCols(w int) extCols {
	c := extCols{idW: extIDMinW, reasonW: extReasonW}

	for _, ref := range r.plannedItems() {
		c.idW = max(c.idW, width(propID(r.plannedRow(ref).ID))+1)
	}

	if w < extReasonMin {
		c.reasonW = 0
	}

	// The "note edited" word takes room from the rule of its row. A
	// narrow pane gives that row too little rule, so it drops the reason
	// column; the detail still shows the reason.
	if len(r.liveNoteIDs()) > 0 && w-2-c.idW-extStateW-c.reasonW-1-width(extNoteSuffix(r.st.theme)) < extRuleMin {
		c.reasonW = 0
	}

	c.ruleW = max(8, w-2-c.idW-extStateW-c.reasonW)

	return c
}

// extNoteSuffix is the "note edited" word that follows the text of a
// table cell.
func extNoteSuffix(t *Theme) string {
	return t.Attention.Render(" " + t.Glyphs.Sep + " " + extNoteEdited)
}

// extWithNote appends the "note edited" word to the text of a table cell
// with room cells. The text gives up its room first, so the word always
// shows when the cell is narrow.
func extWithNote(t *Theme, text string, room int) string {
	return extWithTail(t, text, extNoteSuffix(t), t.Attention.Render(extNoteEdited), room)
}

// extWithTail appends tail to the text of a table cell with room cells.
// The text gives up its room first, so the tail always shows when the
// cell is narrow. A cell that cannot hold four cells of text beside the
// tail shows short alone.
func extWithTail(t *Theme, text, tail, short string, room int) string {
	if room-width(tail) < 4 {
		return truncate(short, room, t.Glyphs.Ellipsis)
	}

	return truncate(text, room-width(tail), t.Glyphs.Ellipsis) + tail
}

// extNoteLegendLine explains the "note edited" word under a table.
func extNoteLegendLine(t *Theme, w int) []string {
	return integHanging(t.Attention.Render(extNoteEdited)+t.Faint.Render(": "), t.Faint, extNoteLegend, w)
}

// plannedRow returns the frozen row of a planned item.
func (r *extRecord) plannedRow(ref extItemRef) domain.Proposal {
	return r.prev.preview.Plan.Batches[ref.batch].Proposals[ref.item]
}

// itemsBox renders the Proposals pane of the review. It lists the
// planned items by batch and the selected items that are not asked.
// The detail of the cursor item follows. pinned adds the tail of the
// Plan pane, for the compact layout that shows this pane alone.
func (r *extRecord) itemsBox(w, h int, focused, pinned bool) string {
	t := r.st.theme
	inner := boxInner(w)
	refs := r.plannedItems()

	r.prev.cursor = max(0, min(r.prev.cursor, len(refs)-1))

	// The window keeps the cursor row and as much of its detail as fits,
	// because the detail holds the notes that the question sends.
	body, keep := r.itemLines(inner, focused)
	body = append(body, "")
	body = append(body, r.itemDetail(inner)...)
	keep[1] = len(body) - 1

	var tail []string
	if pinned {
		tail = r.previewTail(inner, len(body), h)
	}

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Proposals", focused),
		Right: t.Faint.Render("send order"),
	}

	return t.scrollBox(o, body, tail, &r.prev.list, keep, r.prev.follow)
}

// itemLines renders the planned items under their batch and the items
// that are not asked. It returns the line range of the cursor row.
func (r *extRecord) itemLines(w int, focused bool) (lines []string, keep [2]int) {
	t := r.st.theme
	g := t.Glyphs
	p := r.prev.preview
	c := r.itemCols(w)

	row := func(marker, id, state, rule, reason string) string {
		cells := []colCell{cell(marker, 2), cell(id, c.idW), cell(state, extStateW), cell(rule, c.ruleW)}
		if c.reasonW > 0 {
			cells = append(cells, cell(reason, c.reasonW))
		}

		return integRow(t, cells...)
	}

	lines = []string{t.Faint.Render(row("", "ID", "State", "Rule", "Asked because"))}
	keep = [2]int{-1, -1}
	pos := 0

	for bi, b := range p.Plan.Batches {
		// The first row of a batch keeps its heading in view, and the
		// first batch keeps the column header too (extKeepFrom).
		head := extKeepFrom(bi, len(lines))

		lines = append(lines, integHeading(t, "Batch "+itoa(bi+1))+t.Faint.Render("  "+
			propPlural(len(b.Proposals), "proposal", "proposals")+countSep(t)+agentTokens(b.PromptChars)+
			" tokens (estimated)"))

		for j, pr := range b.Proposals {
			cursor := pos == r.prev.cursor

			marker, style := "", t.Body
			if cursor {
				marker, style = t.Key.Render(g.Cursor), t.Title
			}

			// The rule cell is not the last cell, so integRow gives it
			// one cell less than its width.
			rule := style.Render(oneLine(pr.Rule))
			if r.liveNote(pr.ID) {
				rule = extWithNote(t, rule, c.ruleW-1)
			}

			line := row(marker, t.Muted.Render(propID(pr.ID)), extStateWord(t, pr.Status), rule,
				t.Muted.Render(extAskedWord(b.Items[j].Reason)))

			if cursor && focused {
				line = t.highlight(line, w, bgSel)
			}

			if cursor {
				keep = [2]int{len(lines), len(lines)}
				if j == 0 {
					keep[0] = head
				}
			}

			lines = append(lines, line)
			pos++
		}

		lines = append(lines, "")
	}

	lines = append(lines, r.notAskedLines(w)...)

	if len(r.liveNoteIDs()) > 0 {
		lines = append(lines, "")
		lines = append(lines, extNoteLegendLine(t, w)...)
	}

	return lines, keep
}

// extKeepFrom returns the first line that the window keeps for the
// first row of batch bi. The batch heading starts at line at. The first
// batch also keeps the column header at line 0, so a list that opens on
// its first row shows its labels. A later row keeps only itself,
// because its detail needs the room more.
func extKeepFrom(bi, at int) int {
	if bi == 0 {
		return 0
	}

	return at
}

// notAskedLines lists the selected items that the run does not ask,
// with their reasons. An entry without a proposal shows "pin".
func (r *extRecord) notAskedLines(w int) []string {
	t := r.st.theme
	skips := r.prev.preview.Ineligible()

	if len(skips) == 0 {
		return []string{integHeading(t, "Not asked") + t.Faint.Render("  none: every selected proposal is asked")}
	}

	idW := extIDMinW
	for _, s := range skips {
		idW = max(idW, width(extSkipID(t, s))+1)
	}

	out := []string{integHeading(t, "Not asked") + t.Faint.Render("  "+propPlural(len(skips), "selected item",
		"selected items"))}

	for _, s := range skips {
		out = append(out, integHanging("  "+t.Muted.Render(padRight(extSkipID(t, s), idW)), t.Muted,
			oneLine(s.Reason), w)...)
	}

	return out
}

// extSkipID names a skipped item: its proposal ID, or a dash for a Pins
// entry without a proposal, whose reason names the entry.
func extSkipID(t *Theme, s distill.ExtractSkip) string {
	if s.ID == 0 {
		return t.Glyphs.Dash
	}

	return propID(s.ID)
}

// extStateWord renders the ledger state of a row. A planned row is
// pending or applied. A result row without a saved answer can show the
// state that another writer gave it during the run.
func extStateWord(t *Theme, status string) string {
	return extStateStyle(t, status).Render(extStatusWord(status))
}

// extStateDot renders the state line of an item detail, for example
// "● pending".
func extStateDot(t *Theme, status string) string {
	return extStateStyle(t, status).Render(t.Glyphs.Dot + " " + extStatusWord(status))
}

// extStatusWord names a ledger status in the words of Proposals.
func extStatusWord(status string) string {
	switch status {
	case domain.ProposalApplied:
		return "applied"
	case domain.ProposalProposed:
		return "pending"
	case domain.ProposalDismissed:
		return "dismissed"
	case domain.ProposalSuperseded:
		return "superseded"
	default:
		return oneLine(status)
	}
}

// extStateStyle returns the style of a ledger status. A dismissed or
// superseded row has no question to ask, so it is faint.
func extStateStyle(t *Theme, status string) lipgloss.Style {
	switch status {
	case domain.ProposalApplied:
		return t.Success
	case domain.ProposalProposed:
		return t.Attention
	default:
		return t.Faint
	}
}

// extAskedWord names why a question is open, in the short form of the
// list. The detail shows the engine's full phrase.
func extAskedWord(r distill.TriggerReason) string {
	if r == distill.TriggerOpenObsoleteNegative {
		return "older negative"
	}

	return r.String()
}

// itemDetail renders the cursor item of the review. It shows the
// question, the paths that the prompt cites, and what an answer can
// change. The recorded note follows, beside the live note when they
// differ.
func (r *extRecord) itemDetail(w int) []string {
	t := r.st.theme
	refs := r.plannedItems()

	if len(refs) == 0 {
		return nil
	}

	ref := refs[r.prev.cursor]
	b := r.prev.preview.Plan.Batches[ref.batch]
	pr, it := b.Proposals[ref.item], b.Items[ref.item]

	out := r.itemHead(pr, "", ref.batch, w)

	cited := t.Muted.Render("none: the prompt path budget ran out before this item")
	if len(it.Evidence) > 0 {
		paths := make([]string, len(it.Evidence))
		for i, e := range it.Evidence {
			paths[i] = oneLine(e)
		}

		cited = t.Code.Render(strings.Join(paths, ", "))
	}

	answer := "trigger paths are stored as advice; the pin scope stays " + extRegions(pr.RegionSet())
	if pr.Status != domain.ProposalApplied {
		answer = "a confirmed trigger replaces the regions with its own region"
		if it.CoverageChanges {
			answer += "; a negative or unconfirmed answer sets " + extRegions(it.Coverage)
		}
	}

	out = append(out, propKV(t, [][2]string{
		{"Question", t.Body.Render(oneLine(it.Reason.String()))},
		{"Regions", t.Body.Render(extRegions(pr.RegionSet()))},
		{"Cited", cited},
		{"If answered", t.Body.Render(answer)},
	}, w)...)

	return append(out, r.noteLines(pr, w)...)
}

// itemHead renders the rule with its ID and the state line of a planned
// or answered item. now is the status that another writer gave the row
// during the run, or "". The line then names the current status first,
// because a new preview reads that status.
func (r *extRecord) itemHead(pr domain.Proposal, now string, batch, w int) []string {
	t := r.st.theme
	id := propID(pr.ID)

	out := propLines(t.Title, oneLine(pr.Rule), w)
	if last := len(out) - 1; width(out[last])+2+width(id) <= w {
		out[last] += "  " + t.Faint.Render(id)
	} else {
		out = append(out, t.Faint.Render(id))
	}

	state := extStateDot(t, pr.Status)
	if now != "" {
		state = extStateDot(t, now) + t.Faint.Render(" now "+t.Glyphs.Sep+" planned as "+extStatusWord(pr.Status))
	}

	return append(out, state+"   "+t.Muted.Render("batch "+itoa(batch+1)), "")
}

// noteLines renders the recorded note that the question sends. When the
// live pin note differs, both notes show as a pair. A narrow pane stacks
// them. A note then says which one the question uses.
func (r *extRecord) noteLines(pr domain.Proposal, w int) []string {
	t := r.st.theme
	recorded := strings.TrimSpace(pr.Note)

	live, differs := "", false
	if p := r.prev.preview; p != nil {
		live, differs = p.LiveNotes[pr.ID]
	}

	note := func(text string, style func(string) []string) []string {
		if strings.TrimSpace(text) == "" {
			return []string{t.Muted.Render("No note is recorded.")}
		}

		return style(text)
	}

	if !differs {
		out := []string{"", integHeading(t, "Recorded note") + t.Faint.Render("  the question sends it")}

		return append(out, note(recorded, func(s string) []string { return propLines(t.Body, multiLine(s), w) })...)
	}

	pw := pairWidth(w)
	out := []string{""}
	out = append(out, t.pair(
		integHeading(t, "Recorded note")+t.Faint.Render("  sent"),
		integHeading(t, "Live note")+t.Faint.Render("  lessons.yaml, not sent"),
		note(recorded, func(s string) []string { return propLines(t.Body, multiLine(s), pw) }),
		note(live, func(s string) []string { return propLines(t.Muted, multiLine(s), pw) }), w)...)

	out = append(out, "")

	return append(out, propNote(t, ToneAttention, "The question uses the recorded note. Review any trigger "+
		"advice against the live note.", w)...)
}

// nothingBox explains a preview without an eligible proposal. It lists
// every selected item with its reason, and it offers no start.
func (r *extRecord) nothingBox(w, h int) string {
	t := r.st.theme
	inner := boxInner(w)
	g := t.Glyphs

	reason := "No selected item has an open trigger question with living evidence, so no agent call can " +
		"start. Each item shows its reason below."
	if r.selected() == 0 {
		reason = "Nothing was selected, so nothing can be asked. Mark proposals in Proposals or Pins, then press t."
	}

	lines := []string{t.Muted.Render(g.Info+" ") + t.Title.Render("Nothing to extract"), ""}
	lines = append(lines, integLines(t.Body, reason, inner)...)

	// An empty selection has no item to list, and "every selected
	// proposal is asked" would contradict the reason above.
	if r.selected() > 0 {
		lines = append(lines, "")
		lines = append(lines, r.notAskedLines(inner)...)
	}

	lines = append(lines, "", t.Faint.Render("Nothing was sent to the agent."))

	tail := []string{integRule(t, inner)}
	tail = append(tail, integLines(t.Title, r.scopeSummary(), inner)...)

	o := boxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Trigger extraction", true),
		Right: t.Badge(ToneNeutral, "nothing to send"),
	}

	return t.scrollBox(o, lines, tail, &r.prev.notes, [2]int{-1, -1}, false)
}

// extCounts counts the item outcomes of a run by class. The classes
// never merge: a negative answer is a completed question, a failed item
// is an open one (design map §2.6).
type extCounts struct {
	items, stored, negative, omitted, conflict, failed, cancelled, unattempted, unknown int
	// asked counts the items whose agent call started: their batch metered
	// prompt traffic, or an internal error hid its result. A batch that a
	// stop cancelled before its call metered nothing (contracts D-20).
	asked int
	// named counts the items whose answer named a path as the model wrote
	// it (NamedRaw), as the table and the detail count it. valid counts
	// the items whose answer named a path that exists.
	named, valid int
	// changed lists the pending rows whose regions changed; applied the
	// applied rows that stored trigger paths; conflicts the rows that
	// changed during the agent call.
	changed, applied, conflicts []int64
	// closed counts the items without a saved answer whose question is
	// closed now (extRecord.closedNow), per outcome class. The text then
	// never says that their questions stay open.
	closedFailed, closedCancelled, closedConflict int

	// batches counts the batches. The batch of an internal error counts
	// as unknown, because its call started.
	batches unitCounts
}

// saved counts the items with a saved answer, positive or negative.
func (c extCounts) saved() int { return c.stored + c.negative }

// counts counts the outcomes of the record. An internal error can hide
// the items of one batch. Their call started, so they count as unknown,
// never as not attempted.
func (r *extRecord) counts() extCounts {
	var c extCounts

	for bi, b := range r.outcomes() {
		lost := bi == r.lostBatch()

		switch {
		case lost:
			c.batches.unknown++
		case b.Status == distill.BatchCompleted:
			c.batches.completed++
		case b.Status == distill.BatchFailed:
			c.batches.failed++
		case b.Status == distill.BatchCancelled:
			c.batches.cancelled++
		default:
			c.batches.unattempted++
		}

		if lost || b.PromptChars > 0 {
			c.asked += len(b.Items)
		}

		for _, item := range b.Items {
			c.items++
			c.count(item, lost)

			if _, closed := r.closedNow(item); closed {
				c.closedOf(item.Status)
			}
		}
	}

	return c
}

// closedOf adds one item of status s whose question is closed now.
func (c *extCounts) closedOf(s distill.ExtractItemStatus) {
	switch s {
	case distill.ExtractFailed:
		c.closedFailed++
	case distill.ExtractCancelled:
		c.closedCancelled++
	case distill.ExtractConflict:
		c.closedConflict++
	}
}

// count adds one item outcome.
func (c *extCounts) count(item distill.ExtractItemOutcome, lost bool) {
	if item.NamedRaw > 0 {
		c.named++
	}

	if len(item.Valid) > 0 {
		c.valid++
	}

	if item.RegionsChanged {
		c.changed = append(c.changed, item.Proposal.ID)
	}

	switch item.Status {
	case distill.ExtractStored:
		c.stored++

		if item.Proposal.Status == domain.ProposalApplied {
			c.applied = append(c.applied, item.Proposal.ID)
		}
	case distill.ExtractNegative:
		c.negative++

		if item.Omitted {
			c.omitted++
		}
	case distill.ExtractConflict:
		c.conflict++
		c.conflicts = append(c.conflicts, item.Proposal.ID)
	case distill.ExtractFailed:
		c.failed++
	case distill.ExtractCancelled:
		c.cancelled++
	default:
		if lost {
			c.unknown++
		} else {
			c.unattempted++
		}
	}
}

// lostBatch returns the batch whose result an internal error hid, or -1.
func (r *extRecord) lostBatch() int {
	outs := r.outcomes()
	k := r.run.lost

	if !r.run.internal || k < 0 || k >= len(outs) || outs[k].Status != distill.BatchUnattempted {
		return -1
	}

	return k
}

// verdict classifies the finished run from its typed outcome.
func (r *extRecord) verdict() agentVerdict {
	x := r.run.exec
	c := r.counts()

	saved := propPlural(c.saved(), "answer is", "answers are") + " saved"
	if c.saved() > 0 {
		saved += ": " + itoa(c.stored) + " with trigger paths, " + itoa(c.negative) + " negative"
	}

	saved += "."
	short := propPlural(c.saved(), "answer", "answers") + " saved"

	switch {
	case r.run.internal:
		// Answers can be sent and saved before the error, so the lead
		// never says that nothing was sent.
		lead := "The run stopped on an internal error: " + errText(r.run.err) + ". " + saved
		if c.unknown > 0 {
			lead += " Batch " + itoa(r.run.lost+1) + " was sending when the error happened, so its answers are " +
				"not known. The Proposals view shows what was saved."
		}

		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Trigger extraction stopped on an internal error",
			lead: lead + extUnattemptedText(c), toast: "Trigger extraction stopped on an internal error: " + short + ".",
		}
	case x.Stop == distill.StopStoreError || r.run.err != nil:
		cause := r.run.err
		if cause == nil {
			cause = x.StopErr
		}

		// A store error in the last batch leaves no batch behind, so the
		// lead counts the proposals that were not attempted, if any.
		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Trigger extraction stopped on a store error",
			lead: "A store read or write failed: " + errText(cause) + ". " + saved + extOpenText(c, true) +
				extUnattemptedText(c),
			toast: "Trigger extraction stopped on a store error: " + short + ".",
		}
	case x.Stop == distill.StopCancelled:
		return agentVerdict{
			tone: ToneAttention, word: "stopped", head: "Trigger extraction stopped",
			lead:  "You stopped the run. " + saved + extOpenText(c, true) + extUnattemptedText(c),
			toast: "Trigger extraction stopped: " + short + ".",
		}
	case extLastBatchConflict(x, c):
		// The conflict came in the last batch, so no batch was left out.
		// The run still holds an unsaved answer, so it is no success.
		return agentVerdict{
			tone: ToneAttention, word: "partial", head: "Trigger extraction finished with a conflict",
			lead:  extConflictLead(x, c) + ". " + saved + extOpenText(c, false),
			toast: "Trigger extraction finished with a conflict: " + short + ".",
		}
	case x.Stop == distill.StopConflict:
		return agentVerdict{
			tone: ToneAttention, word: "stopped", head: "Trigger extraction stopped: an input changed",
			lead: extConflictLead(x, c) + ". " + saved + extOpenText(c, false) + extUnattemptedText(c) +
				" Prepare the remaining proposals for a new preview from the current state.",
			toast: "Trigger extraction stopped because an input changed: " + short + ".",
		}
	case c.saved() == 0 && c.failed > 0:
		stays := " The questions stay open, so a new preview can include them."
		if c.closedFailed > 0 {
			stays = extStaysText(c.failed-c.closedFailed, c.closedFailed)
		}

		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Trigger extraction failed",
			lead:  "Every batch failed, so no answer was saved." + stays,
			toast: "Trigger extraction failed: no answer was saved.",
		}
	case c.failed > 0 || c.conflict > 0:
		return agentVerdict{
			tone: ToneAttention, word: "partial", head: "Trigger extraction finished with open questions",
			lead:  saved + extOpenText(c, true),
			toast: "Trigger extraction finished with open questions: " + short + ".",
		}
	}

	lead := "Every planned proposal was asked. " + saved
	if r.run.stopping {
		lead += " The stop came after the last reply, so every batch finished."
	}

	return agentVerdict{
		tone: ToneSuccess, word: "completed", head: "Trigger extraction completed", lead: lead,
		toast: "Trigger extraction completed: " + short + ".",
	}
}

// extLastBatchConflict reports a conflict in the last batch: the engine
// stopped with StopConflict, but no batch was left out. The run then
// finished every batch, so the result reports no stop.
func extLastBatchConflict(x *distill.ExtractExecution, c extCounts) bool {
	return x.Stop == distill.StopConflict && c.unattempted == 0 && c.cancelled == 0
}

// extConflictLead names the cause of a conflict stop. The engine error
// names only the first row that changed during the agent call, so
// several such rows get one sentence with every ID.
func extConflictLead(x *distill.ExtractExecution, c extCounts) string {
	if len(c.conflicts) < 2 {
		return extStaleText(x.StopErr)
	}

	return itoa(len(c.conflicts)) + " proposals changed during the agent call (" + propIDList(c.conflicts) +
		"); their answers were not saved"
}

// extOpenText tells that failed, conflicting and cancelled questions
// stay open, because none of them has a stamp. A row whose question
// closed during the run is named apart. A lead that already names the
// conflict passes conflict false, so no fact shows twice.
func extOpenText(c extCounts, conflict bool) string {
	var parts []string

	open, closed := c.failed+c.cancelled, c.closedFailed+c.closedCancelled

	if c.failed > 0 {
		parts = append(parts, itoa(c.failed)+" failed")
	}

	if conflict && c.conflict > 0 {
		parts = append(parts, itoa(c.conflict)+" changed during the agent call")
		open += c.conflict
		closed += c.closedConflict
	}

	switch {
	case c.cancelled == 1:
		parts = append(parts, "1 was cancelled")
	case c.cancelled > 1:
		parts = append(parts, itoa(c.cancelled)+" were cancelled")
	}

	if len(parts) == 0 {
		return ""
	}

	return " Of the other proposals, " + propJoin(parts) + "." + extStaysText(open-closed, closed)
}

// extStaysText tells which questions without a saved answer stay open.
// open counts the rows that a new preview can ask, closed the rows whose
// question closed during the run.
func extStaysText(open, closed int) string {
	switch {
	case closed == 0 && open == 1:
		return " Its question stays open, so a new preview can include it."
	case closed == 0:
		return " Their questions stay open, so a new preview can include them."
	}

	text := " " + propPlural(closed, "proposal is", "proposals are") + " closed now, so a new preview does not ask "
	if closed == 1 {
		text += "it."
	} else {
		text += "them."
	}

	switch {
	case open == 1:
		text += " The other question stays open, so a new preview can include it."
	case open > 1:
		text += " The other questions stay open, so a new preview can include them."
	}

	return text
}

// extUnattemptedText counts the proposals that were never sent.
func extUnattemptedText(c extCounts) string {
	if c.unattempted == 0 {
		return ""
	}

	return " " + propPlural(c.unattempted, "proposal was", "proposals were") + " not attempted."
}

// runView renders the run or its result: the summary and the proposals
// side by side, or one of them in the compact layout.
func (r *extRecord) runView(w, h int) string {
	if r.st.wide() {
		leftW := setupSplit(w, 60)

		return hjoin(1, r.summaryBox(leftW, h, r.run.sumFocus), r.runItemsBox(w-leftW-1, h, !r.run.sumFocus))
	}

	if r.run.detail {
		return r.runItemsBox(w, h, true)
	}

	return r.compactBox(w, h)
}

// stateLabel renders the state at the right of the summary border. The
// result comes first, because a stop request stays set after the end.
func (r *extRecord) stateLabel() string {
	if r.step == stepResult {
		v := r.verdict()

		return r.st.theme.Badge(v.tone, v.word)
	}

	return runStateLabel(r.st.theme, r.st.tick, r.stopping(), r.run.started)
}

// headline renders the first line of the summary.
func (r *extRecord) headline() string {
	t := r.st.theme
	g := t.Glyphs
	spin := t.spinnerFrame(r.st.tick)
	run := &r.run
	n := len(run.units)

	if r.step == stepResult {
		v := r.verdict()

		return t.ToneStyle(v.tone).Render(t.ToneGlyph(v.tone)+" ") + t.Title.Render(v.head)
	}

	batch := func(k int) string {
		return "batch " + itoa(k+1) + " of " + itoa(n) + " " + g.Sep + " " +
			propPlural(len(run.units[k].Items), "proposal", "proposals")
	}

	// A stop from the quit question sets no stop batch, so the running
	// batch stands in for it.
	stopAt := run.stopAt
	if stopAt < 0 {
		stopAt = run.sending
	}

	switch {
	case r.stopping() && stopAt >= 0:
		return t.Attention.Render(spin+" ") + t.Title.Render("Stopping after "+batch(stopAt))
	case r.stopping():
		return t.Attention.Render(spin+" ") + t.Title.Render("Stopping at the next check")
	case run.sending >= 0:
		return t.Accent.Render(spin+" ") + t.Title.Render("Sending "+batch(run.sending))
	case !run.checked:
		return t.Accent.Render(spin+" ") + t.Title.Render("Checking the plan against the current state")
	default:
		return t.Accent.Render(spin+" ") + t.Title.Render("Between batches "+g.Sep+" "+itoa(run.finished)+" of "+
			itoa(n)+" done")
	}
}

// summaryBox renders the left pane: the headline, the stop note or the
// result lead, then the phases or what happened, and the next steps.
func (r *extRecord) summaryBox(w, h int, focused bool) string {
	t := r.st.theme
	inner := boxInner(w)

	var body []string

	if r.step == stepResult {
		body = r.resultLines(inner)
	} else {
		body = r.progressLines(inner)
	}

	o := boxOpts{W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Trigger extraction", true), Right: r.stateLabel()}

	return t.scrollBox(o, body, nil, &r.run.summary, [2]int{-1, -1}, false)
}

// progressLines renders the running summary.
func (r *extRecord) progressLines(w int) []string {
	t := r.st.theme
	run := &r.run

	lines := []string{r.headline(), ""}
	lines = append(lines, integLines(t.Muted, r.stopNote(), w)...)
	lines = append(lines, "", integHeading(t, "Phases"))
	lines = append(lines, t.phaseRows(r.phases(), r.st.tick, w)...)
	lines = append(lines, "")
	lines = append(lines, r.barLines(w)...)
	lines = append(lines, "", integHeading(t, "Results so far"))
	lines = append(lines, propKV(t, r.soFar(), w)...)

	if len(run.activity) > 0 {
		lines = append(lines, "", integHeading(t, "Recent activity"))
		lines = append(lines, t.activityLines(run.activity, agentActivityKeep, w)...)
	}

	return lines
}

// stopNote says what x does, or what a requested stop waits for.
func (r *extRecord) stopNote() string {
	switch {
	case r.stopping() && r.run.stopAt >= 0:
		k := itoa(r.run.stopAt + 1)

		return "Seamark waits for the agent process of batch " + k + " to end. No new batch starts. The result " +
			"shows whether the answers of batch " + k + " were saved before the stop."
	case r.stopping():
		return "No new batch starts. Seamark waits for the running work to end; the result shows what was saved."
	default:
		return "x stops the run: no new batch starts and the running agent call is cancelled. Saved answers stay " +
			"saved. A provider can still bill a cancelled call."
	}
}

// phases renders the steps of the run from the typed events.
func (r *extRecord) phases() []phaseRow {
	run := &r.run
	n := len(run.units)

	check := phaseRow{label: "Check the plan", state: phaseActive}

	switch {
	case run.checked:
		check.state, check.note = phaseDone, "no change since the preview"
	case run.stopped:
		check.state, check.note = phaseStopped, agentStopWords(run.stopWhy)
	}

	ask := phaseRow{label: "Ask the agent", note: itoa(run.finished) + " of " + propPlural(n, "batch", "batches") +
		" done"}
	if run.checked && !run.stopped {
		ask.state = phaseActive
	}

	return []phaseRow{check, ask, {label: "Report the result"}}
}

// barLines renders the bar of the saved answers of a running record.
// The denominator is the frozen plan, so the bar shows real progress. It
// fills with saved answers only, because the success color must never
// count a failed or cancelled item. The result has no bar: its counts
// tell each outcome apart.
func (r *extRecord) barLines(w int) []string {
	t := r.st.theme
	c := r.counts()

	count := itoa(c.saved()) + " / " + itoa(c.items)
	barW := max(4, min(40, w-width(count)-4))

	return []string{
		t.Heading.Render("ANSWERS") + t.Faint.Render("  "+itoa(c.saved())+" of "+itoa(c.items)+" saved"),
		t.bar(c.saved(), c.items, barW, ToneSuccess) + "    " + t.Muted.Render(count),
	}
}

// barLine renders the bar of the saved answers in one line, for the
// compact box, where every row counts.
func (r *extRecord) barLine(w int) string {
	t := r.st.theme
	c := r.counts()

	count := itoa(c.saved()) + " of " + itoa(c.items) + " saved"
	barW := max(4, min(40, w-width(count)-4))

	return t.bar(c.saved(), c.items, barW, ToneSuccess) + "    " + t.Muted.Render(count)
}

// soFar renders the durable results of a running record.
func (r *extRecord) soFar() [][2]string {
	t := r.st.theme
	c := r.counts()

	answers := t.Muted.Render("none yet")
	if c.saved() > 0 {
		answers = t.Body.Render(itoa(c.stored) + " with trigger paths" + countSep(t) + itoa(c.negative) +
			" negative")
	}

	return [][2]string{
		{"Answers", answers},
		{"Pending", r.pendingText(c)},
		{"Applied", r.appliedText(c)},
		{"Usage", r.usageText()},
	}
}

// pendingText names the pending proposals whose regions changed.
func (r *extRecord) pendingText(c extCounts) string {
	t := r.st.theme

	if len(c.changed) == 0 {
		return t.Muted.Render("no region changed")
	}

	return t.Body.Render(propPlural(len(c.changed), "region set changed", "region sets changed") + ": " +
		propIDList(c.changed) + " (still pending)")
}

// appliedText names the applied proposals that stored trigger paths. The
// paths are advice: the pin scope stays until a reviewed retarget. A row
// whose live note differs needs review against that note.
func (r *extRecord) appliedText(c extCounts) string {
	t := r.st.theme

	if len(c.applied) == 0 {
		return t.Muted.Render("no new trigger paths")
	}

	text := propIDList(c.applied) + " stored trigger paths as advice; the pin scope stays (see Lessons " +
		t.Glyphs.Arrow + " Pins)"

	var live []int64

	for _, id := range c.applied {
		if r.liveNote(id) {
			live = append(live, id)
		}
	}

	if len(live) > 0 {
		text += "; review " + propIDList(live) + " against the live note"
	}

	return t.Body.Render(text)
}

// usageText renders the estimated agent traffic. The engine meters
// characters; the tokens are its estimate. A finished call meters its
// prompt and its reply. The prompt of the batch that sends is already
// with the agent, so its frozen size counts as sent before the reply.
func (r *extRecord) usageText() string {
	t := r.st.theme
	sep := countSep(t)

	sent, back := 0, 0

	for _, b := range r.outcomes() {
		sent += b.PromptChars
		back += b.ReplyChars
	}

	pending, k := 0, r.run.sending
	if p := r.prev.preview; r.step == stepRunning && k >= 0 && p != nil && p.Plan != nil && k < len(p.Plan.Batches) {
		pending = p.Plan.Batches[k].PromptChars
	}

	switch {
	case sent == 0 && pending == 0:
		return t.Muted.Render("no agent traffic yet")
	case sent == 0:
		return t.Body.Render(agentTokens(pending)+" tokens sent to batch "+itoa(k+1)) + " " +
			t.Faint.Render("(estimated)") + t.Muted.Render("; reply pending")
	}

	text := t.Body.Render(agentTokens(sent+pending)+" tokens sent"+sep+agentTokens(back)+" received") + " " +
		t.Faint.Render("(estimated)")

	if pending > 0 {
		text += t.Muted.Render("; batch " + itoa(k+1) + " reply pending")
	}

	if r.step == stepResult {
		text += t.Muted.Render(sep + runElapsed(r.run.ended.Sub(r.run.started)))
	}

	return text
}

// resultLines renders the result summary.
func (r *extRecord) resultLines(w int) []string {
	t := r.st.theme
	v := r.verdict()

	lines := []string{r.headline(), ""}
	lines = append(lines, integLines(t.Body, v.lead, w)...)
	lines = append(lines, "", integHeading(t, "What happened"))
	lines = append(lines, propKV(t, r.happened(), w)...)
	lines = append(lines, "", integHeading(t, "By proposal"))
	lines = append(lines, propKV(t, r.byOutcome(), w)...)
	lines = append(lines, r.nextSection(w)...)
	lines = append(lines, "")

	return append(lines, integLines(t.Faint, extResultFoot, w)...)
}

// nextSection renders the NEXT heading and the keys of the result. An
// earlier record gets no enter and no n, so it shows no NEXT section.
func (r *extRecord) nextSection(w int) []string {
	if r.browsed {
		return nil
	}

	return append([]string{"", integHeading(r.st.theme, "Next")}, r.nextLines(w)...)
}

// readOnlyView renders the record as an earlier run of the session. The
// Run sub-view passes an earlier record only the movement keys, so the
// view leaves out the result keys enter and n. The flag holds only
// during this call, because the same record can become current again.
func (r *extRecord) readOnlyView(w, h int) string {
	r.browsed = true
	defer func() { r.browsed = false }()

	return r.view(w, h)
}

// extResultFoot closes every result. It says what this run did, not
// what the file holds: another writer can change lessons.yaml during a
// run. A new preview reads the current state, and no result starts more
// work by itself.
const extResultFoot = "This run changed no installed pin scope and did not write lessons.yaml. A new preview " +
	"starts from the current state. Nothing is retried automatically, and nothing chains into a retarget or an " +
	"apply."

// happened renders the facts of the finished run.
func (r *extRecord) happened() [][2]string {
	t := r.st.theme
	x := r.run.exec
	c := r.counts()

	answers := t.Muted.Render("none saved")
	if c.saved() > 0 {
		text := itoa(c.saved()) + " saved: " + itoa(c.stored) + " with trigger paths, " + itoa(c.negative) + " negative"
		if c.omitted > 0 {
			text += " (" + itoa(c.omitted) + " omitted from the reply)"
		}

		answers = t.Body.Render(text)
	}

	rows := [][2]string{
		{"Batches", t.Body.Render(strings.Join(c.batches.parts("completed"), countSep(t)))},
		{"Answers", answers},
		{"Paths", t.Body.Render("named for " + propPlural(c.named, "proposal", "proposals") + ", valid for " +
			itoa(c.valid) + ", stored for " + itoa(c.stored))},
		{"Pending", r.pendingText(c)},
		{"Applied", r.appliedText(c)},
		{"Usage", r.usageText()},
	}

	// A conflict in the last batch stopped nothing, so the verdict says
	// "finished" and no Stop row contradicts it.
	switch {
	case r.run.internal:
		rows = append(rows, [2]string{"Stop", t.Failure.Render("internal error")})
	case x.Stop != distill.StopNone && !extLastBatchConflict(x, c):
		rows = append(rows, [2]string{"Stop", t.Attention.Render(agentStopWords(x.Stop))})
	}

	return rows
}

// byOutcome lists the proposal IDs of each outcome class. Every class
// that holds an ID gets its own row, so no ID hides in a total.
func (r *extRecord) byOutcome() [][2]string {
	t := r.st.theme

	var stored, negative, omitted, conflict, cancelled, unattempted, unknown []int64

	failed := map[distill.FailureKind][]int64{}
	closed := map[string][]int64{}

	var kinds []distill.FailureKind

	var whys []string

	for bi, b := range r.outcomes() {
		for _, item := range b.Items {
			id := item.Proposal.ID

			if why, ok := r.closedNow(item); ok {
				if _, seen := closed[why]; !seen {
					whys = append(whys, why)
				}

				closed[why] = append(closed[why], id)
			}

			switch item.Status {
			case distill.ExtractStored:
				stored = append(stored, id)
			case distill.ExtractNegative:
				negative = append(negative, id)

				if item.Omitted {
					omitted = append(omitted, id)
				}
			case distill.ExtractConflict:
				conflict = append(conflict, id)
			case distill.ExtractFailed:
				if _, ok := failed[item.Failure]; !ok {
					kinds = append(kinds, item.Failure)
				}

				failed[item.Failure] = append(failed[item.Failure], id)
			case distill.ExtractCancelled:
				cancelled = append(cancelled, id)
			default:
				if bi == r.lostBatch() {
					unknown = append(unknown, id)
				} else {
					unattempted = append(unattempted, id)
				}
			}
		}
	}

	var rows [][2]string

	add := func(label string, ids []int64, tail string) {
		if len(ids) > 0 {
			rows = append(rows, [2]string{label, t.Body.Render(propIDList(ids) + tail)})
		}
	}

	add("Stored", stored, "")

	if len(omitted) > 0 {
		add("Negative", negative, "; omitted from the reply: "+propIDList(omitted))
	} else {
		add("Negative", negative, "")
	}

	add("Conflict", conflict, ": changed during the agent call; not saved")

	if len(kinds) > 0 {
		parts := make([]string, len(kinds))
		for i, k := range kinds {
			parts[i] = propIDList(failed[k]) + " (" + agentFailWords(k) + ")"
		}

		rows = append(rows, [2]string{"Failed", t.Body.Render(strings.Join(parts, "; "))})
	}

	add("Cancelled", cancelled, "")
	add("Not attempted", unattempted, "")
	add("Unknown", unknown, ": the call started, then an internal error ended the run")

	// Another writer closed these questions during the run. They show
	// apart, because "prepare remaining" leaves them out.
	if len(whys) > 0 {
		parts := make([]string, len(whys))
		for i, why := range whys {
			parts[i] = propIDList(closed[why]) + " (" + oneLine(why) + ")"
		}

		rows = append(rows, [2]string{"Closed now", t.Body.Render(strings.Join(parts, "; "))})
	}

	if len(rows) == 0 {
		rows = append(rows, [2]string{"None", t.Muted.Render("no proposal was planned")})
	}

	return rows
}

// nextLines names the keys of the result. enter shows only when the
// cursor item links a view, and n only while a question stays open.
func (r *extRecord) nextLines(w int) []string {
	t := r.st.theme

	row := func(k, text string) []string {
		return integHanging(t.Key.Render(padRight(k, 7)), t.Body, text, w)
	}

	var lines []string

	if _, name, ok := r.link(); ok {
		item, _, _ := r.cursorItem()

		text := "show " + propID(item.Proposal.ID) + " in " + name
		if item.Proposal.Status == domain.ProposalApplied {
			text += "; a retarget is a separate preview there"
		}

		lines = append(lines, row("enter", text)...)
	}

	if ids := r.remaining(); len(ids) > 0 {
		lines = append(lines, row("n", "prepare a new preview of the "+propPlural(len(ids), "open question",
			"open questions")+" ("+propIDList(ids)+"); it starts only with y")...)
	}

	if len(lines) == 0 {
		lines = append(lines, integLines(t.Muted, "No question stays open. Move to a proposal to open its view.", w)...)
	}

	return lines
}

// runItemsBox renders the Proposals pane of the run: every planned item
// with its state, then the detail of the cursor item.
func (r *extRecord) runItemsBox(w, h int, focused bool) string {
	t := r.st.theme
	inner := boxInner(w)
	c := r.counts()

	// The window keeps the cursor row and as much of its detail as fits,
	// so the outcome of the cursor item shows beside the table.
	body, keep := r.runItemLines(inner, focused)
	body = append(body, "")
	body = append(body, r.runItemDetail(inner)...)
	keep[1] = len(body) - 1

	right := itoa(r.run.finished) + " of " + propPlural(len(r.outcomes()), "batch", "batches") + " done"
	if r.step == stepResult {
		right = itoa(c.asked) + " of " + itoa(c.items) + " asked"
	}

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Proposals", focused), Right: t.Muted.Render(right),
	}

	return t.scrollBox(o, body, nil, &r.run.list, keep, r.run.follow)
}

// runItemLines renders the item table by batch. It returns the line
// range of the cursor row.
func (r *extRecord) runItemLines(w int, focused bool) (lines []string, keep [2]int) {
	t := r.st.theme
	g := t.Glyphs
	outs := r.outcomes()

	// A row that another writer dismissed or superseded during the run
	// shows that state, so the state column grows to fit its word.
	idW, stateW := extIDMinW, extStateW
	for _, ref := range r.runItems() {
		item := outs[ref.batch].Items[ref.item]
		idW = max(idW, width(propID(item.Proposal.ID))+1)
		stateW = max(stateW, width(extStatusWord(r.itemStatus(item)))+1)
	}

	outW := max(extOutcomeMin, w-2-agentGlyphW-idW-stateW)

	row := func(marker, glyph, id, state, outcome string) string {
		return integRow(t, cell(marker, 2), cell(glyph, agentGlyphW), cell(id, idW), cell(state, stateW),
			cell(outcome, outW))
	}

	lines = []string{t.Faint.Render(row("", "", "ID", "State", "Outcome"))}
	keep = [2]int{-1, -1}
	pos := 0

	for bi, b := range outs {
		head := extKeepFrom(bi, len(lines))

		lines = append(lines, integHeading(t, "Batch "+itoa(bi+1))+t.Faint.Render("  "+
			propPlural(len(b.Items), "proposal", "proposals"))+r.batchState(bi, b))

		for j, item := range b.Items {
			cursor := pos == r.run.cursor

			marker := ""
			if cursor {
				marker = t.Key.Render(g.Cursor)
			}

			// The live note gets a word after the outcome, never a glyph:
			// the glyph column already uses "!" for outcome states. The
			// outcome is the last cell, so it gets its whole width. A
			// region change and a live note keep their room, as the
			// reason gives way first. Only a pending row changes regions
			// and only an applied row has a live note, so a row never
			// carries both marks.
			glyph, words, mark := r.outcomeCell(bi, item)
			if mark != "" {
				words = extWithTail(t, words, mark, mark, outW)
			}

			if r.liveNote(item.Proposal.ID) {
				words = extWithNote(t, words, outW)
			}

			line := row(marker, glyph, t.Muted.Render(propID(item.Proposal.ID)), extStateWord(t, r.itemStatus(item)),
				words)

			if cursor && focused {
				line = t.highlight(line, w, bgSel)
			}

			if cursor {
				keep = [2]int{len(lines), len(lines)}
				if j == 0 {
					keep[0] = head
				}
			}

			lines = append(lines, line)
			pos++
		}
	}

	if len(r.liveNoteIDs()) > 0 {
		lines = append(lines, "")
		lines = append(lines, extNoteLegendLine(t, w)...)
	}

	return lines, keep
}

// batchState renders the state of one batch after its heading. A failed
// batch names its failure kind once, so its rows can stay short.
func (r *extRecord) batchState(bi int, b distill.ExtractBatchOutcome) string {
	t := r.st.theme
	sep := t.Faint.Render(countSep(t))

	switch {
	case bi == r.lostBatch():
		return sep + t.Attention.Render("result unknown")
	case b.Status == distill.BatchCompleted:
		return sep + t.Success.Render("completed")
	case b.Status == distill.BatchFailed:
		return sep + t.Failure.Render("failed: "+agentFailWords(b.Failure))
	case b.Status == distill.BatchCancelled:
		return sep + t.Attention.Render("cancelled")
	case r.step == stepRunning && bi == r.run.sending:
		return sep + t.Accent.Render("sending"+t.Glyphs.Ellipsis)
	case r.step == stepRunning:
		return sep + t.Faint.Render("waiting")
	default:
		return sep + t.Attention.Render("not attempted")
	}
}

// itemStatus returns the ledger status that the run table and the
// detail show for an item: the current status of a result row that
// another writer changed during the run, else the planned status.
func (r *extRecord) itemStatus(item distill.ExtractItemOutcome) string {
	if now, ok := r.nowStatus(item); ok {
		return now
	}

	return item.Proposal.Status
}

// outcomeCell returns the state glyph, the outcome words and the region
// mark of one item (design map §2.6). The mark is "· new regions" for a
// saved answer that changed pending regions, else "". A waiting item
// reads "waiting" only while the run is active; after it, the same item
// reads "not attempted". Every state has its own word, because failed
// and stopped share the glyph "!".
func (r *extRecord) outcomeCell(bi int, item distill.ExtractItemOutcome) (glyph, words, mark string) {
	t := r.st.theme
	g := t.Glyphs

	changed := ""
	if item.RegionsChanged {
		changed = " " + g.Sep + " new regions"
	}

	switch item.Status {
	case distill.ExtractStored:
		return t.Success.Render(g.OK), t.Body.Render("stored: " + propPlural(len(item.Valid), "path", "paths")),
			t.Body.Render(changed)
	case distill.ExtractNegative:
		return t.Faint.Render(g.Info), t.Muted.Render(extNegativeWords(item)), t.Muted.Render(changed)
	case distill.ExtractConflict:
		return t.Attention.Render(g.Warn), t.Attention.Render("conflict: not saved"), ""
	case distill.ExtractFailed:
		return t.Failure.Render(g.Fail), t.Failure.Render("failed: " + agentFailWords(item.Failure)), ""
	case distill.ExtractCancelled:
		return t.Attention.Render(g.Warn), t.Attention.Render("cancelled"), ""
	}

	switch {
	case bi == r.lostBatch():
		return t.Attention.Render(g.Warn), t.Attention.Render("result unknown"), ""
	case r.step == stepRunning && bi == r.run.sending:
		return t.Accent.Render(t.spinnerFrame(r.st.tick)), t.Accent.Render("sending" + g.Ellipsis), ""
	case r.step == stepRunning:
		return t.Faint.Render(g.Hollow), t.Faint.Render("waiting"), ""
	default:
		return t.Attention.Render(g.Warn), t.Attention.Render("not attempted"), ""
	}
}

// extNegativeWords says why an answer is negative, in the short form of
// the table. The item was not in the reply (A3a: "negative (omitted)"),
// the named paths failed the checks, or nothing was named. "named"
// counts the paths as the model wrote them (NamedRaw), as the summary
// and the detail do (spec R-12). The detail and the summary use the
// full words.
func extNegativeWords(item distill.ExtractItemOutcome) string {
	switch {
	case item.Omitted:
		return "negative (omitted)"
	case item.NamedRaw > 0:
		named := "negative: " + propPlural(item.NamedRaw, "path", "paths") + " named, "

		switch {
		case !extAllClean(item):
			return named + "none valid"
		case item.NamedRaw == 1:
			return named + "not found"
		default:
			return named + "none found"
		}
	default:
		return "negative: none named"
	}
}

// extAllClean reports an answer whose named paths all passed the syntax
// check. A negative answer of such paths names only paths that do not
// exist in the working tree.
func extAllClean(item distill.ExtractItemOutcome) bool {
	return len(item.Named) > 0 && len(item.Named) == item.NamedRaw
}

// extNegativeNote explains a negative answer in a sentence for the
// detail of an item. It uses the same cases as extNegativeWords.
func extNegativeNote(item distill.ExtractItemOutcome) string {
	switch {
	case item.Omitted:
		return "negative: the item was omitted from the reply, so the agent named no trigger for it."
	case item.NamedRaw > 0 && extAllClean(item):
		return "negative: no named path exists in the working tree."
	case item.NamedRaw > 0:
		return "negative: no named entry is a valid repository path."
	default:
		return "negative: the agent named no trigger path."
	}
}

// runItemDetail renders the cursor item of the run or the result. It
// shows the outcome, the answer counts, the stored paths, the regions
// and the live-note mark. The cause of a failure can hold agent output,
// so errText redacts and sanitizes it.
func (r *extRecord) runItemDetail(w int) []string {
	t := r.st.theme

	item, bi, ok := r.cursorItem()
	if !ok {
		return nil
	}

	pr := item.Proposal
	now, _ := r.nowStatus(item)
	out := r.itemHead(pr, now, bi, w)

	if note := r.itemNote(bi, item); note != "" {
		out = append(out, propNote(t, r.itemTone(bi, item), note, w)...)
		out = append(out, "")
	}

	sep := " " + t.Glyphs.Sep + " "

	var rows [][2]string

	if extSaved(item) || item.NamedRaw > 0 {
		rows = append(rows, [2]string{"Answer", t.Body.Render(itoa(item.NamedRaw) + " named" + countSep(t) +
			itoa(len(item.Valid)) + " valid")})
	}

	for i, f := range extFacts(item) {
		label := ""
		if i == 0 {
			label = "Paths"
		}

		rows = append(rows, [2]string{label, t.Code.Render(oneLine(f.Path)) + t.Muted.Render(sep+extFactWords(f))})
	}

	if !item.RegionsChanged {
		rows = append(rows, [2]string{"Regions", t.Body.Render(extRegionsAfter(item))})
	}

	out = append(out, propKV(t, rows, w)...)

	if item.RegionsChanged {
		out = append(out, "", integHeading(t, "Pending regions")+t.Faint.Render("  changed; Apply is still separate"))
		out = append(out, t.regionDelta(item.RegionsBefore, item.RegionsAfter, w)...)
	}

	if r.liveNote(pr.ID) {
		out = append(out, "")
		out = append(out, propNote(t, ToneAttention, "The live note differs from the recorded note that the question "+
			"used. Review this advice against the live note in Lessons "+t.Glyphs.Arrow+" Pins.", w)...)
	}

	// An earlier record gets no enter, so it names no link.
	if _, name, ok := r.link(); ok && !r.browsed {
		out = append(out, "")
		out = append(out, integLines(t.Faint, "enter shows "+propID(pr.ID)+" in "+name+". It opens no preview "+
			"and changes nothing.", w)...)
	}

	return out
}

// itemNote explains the outcome of one item in a sentence; "" for a
// stored or negative answer, whose rows say enough. A row whose
// question closed during the run says so instead of "stays open".
func (r *extRecord) itemNote(bi int, item distill.ExtractItemOutcome) string {
	why, closed := r.closedNow(item)

	closedText := "Its question is closed now (" + oneLine(why) + "), so a new preview does not ask it."

	switch item.Status {
	case distill.ExtractStored:
		return ""
	case distill.ExtractNegative:
		return extNegativeNote(item)
	case distill.ExtractConflict:
		// The row changed during the agent call, so only a new preview
		// can tell whether its question is still open.
		if closed {
			return "conflict: " + extStaleText(item.Err) + ". " + closedText
		}

		return "conflict: " + extStaleText(item.Err) + ". A new preview decides whether it can be asked."
	case distill.ExtractFailed:
		text := "failed: " + agentFailWords(item.Failure) + ": " + extFailText(item.Err) + ". "
		if closed {
			return text + closedText
		}

		return text + "The question stays open, so a new preview can include it."
	case distill.ExtractCancelled:
		if closed {
			return "cancelled before its answer was saved. " + closedText
		}

		return "cancelled before its answer was saved; the question stays open."
	}

	switch {
	case bi == r.lostBatch():
		return "result unknown: the agent call started, then the run stopped on an internal error. The Proposals " +
			"view shows what was saved."
	case r.step == stepRunning:
		return ""
	case closed:
		return "not attempted: the run ended before this batch. " + closedText
	default:
		return "not attempted: the run ended before this batch, so the question stays open."
	}
}

// extFailText describes the cause of a failed item. A reply without a
// "triggers" key gets the words of the console: the shared engine text
// says that "the batch retries", but nothing here retries by itself.
func extFailText(err error) string {
	if errors.Is(err, distill.ErrNoTriggersKey) {
		return `the reply has no "triggers" key, so it is not a negative answer`
	}

	return errText(err)
}

// itemTone returns the tone of the outcome note of one item. A negative
// answer is a completed question, so its note is neutral.
func (r *extRecord) itemTone(bi int, item distill.ExtractItemOutcome) Tone {
	switch {
	case item.Status == distill.ExtractNegative:
		return ToneNeutral
	case item.Status == distill.ExtractFailed && bi != r.lostBatch():
		return ToneFailure
	default:
		return ToneAttention
	}
}

// extFacts returns the facts of the valid paths of an item, one per
// path. A valid path without a fact still shows, as "unverified".
func extFacts(item distill.ExtractItemOutcome) []distill.TriggerFact {
	out := make([]distill.TriggerFact, 0, len(item.Valid))

	for _, p := range item.Valid {
		i := slices.IndexFunc(item.Facts, func(f distill.TriggerFact) bool { return f.Path == p })
		if i < 0 {
			out = append(out, distill.TriggerFact{Path: p})

			continue
		}

		out = append(out, item.Facts[i])
	}

	return out
}

// extFactWords names how history supports a trigger path: cited by the
// evidence, confirmed by co-change, or unverified.
func extFactWords(f distill.TriggerFact) string {
	switch {
	case f.Direct:
		return "cited by the evidence"
	case f.Together > 0:
		return "co-change, " + propPlural(f.Together, "shared commit", "shared commits")
	default:
		return "unverified"
	}
}

// extRegionsAfter names the regions of an item whose answer changed no
// region. An applied row says that its scope never changes here; an
// answered pending row says that it kept its regions.
func extRegionsAfter(item distill.ExtractItemOutcome) string {
	text := extRegions(item.RegionsBefore)

	switch {
	case item.Proposal.Status == domain.ProposalApplied:
		return text + " (applied scopes never change here)"
	case item.Status == distill.ExtractStored || item.Status == distill.ExtractNegative:
		return text + " (unchanged)"
	default:
		return text
	}
}

// compactBox renders the run in one box for a narrow terminal. The
// headline stays above the lines that scroll, so the state of a paid run
// never scrolls away. While the run works, the short stop note stays
// under it. A result shows its lead, what happened, the outcome rows,
// the item table and the next steps. tab shows the Proposals pane.
func (r *extRecord) compactBox(w, h int) string {
	t := r.st.theme
	inner := boxInner(w)
	run := &r.run

	head := []string{r.headline()}
	body := []string{""}
	follow := run.follow

	if r.step == stepResult {
		body = append(body, integLines(t.Body, r.verdict().lead, inner)...)
		body = append(body, "", integHeading(t, "What happened"))
		body = append(body, propKV(t, r.happened(), inner)...)
		body = append(body, "", integHeading(t, "By proposal"))
		body = append(body, propKV(t, r.byOutcome(), inner)...)
		body = append(body, "")
		follow = run.follow && run.moved
	} else {
		head = append(head, integLines(t.Muted, r.shortStopNote(), inner)...)
		body = append(body, r.barLine(inner), "")
	}

	table, keep := r.runItemLines(inner, true)
	keep = [2]int{keep[0] + len(body), keep[1] + len(body)}
	body = append(body, table...)

	switch {
	case r.step == stepResult:
		body = append(body, r.nextSection(inner)...)
		body = append(body, "")
		body = append(body, integLines(t.Faint, extResultFoot, inner)...)
	case len(run.activity) > 0:
		body = append(body, "")
		body = append(body, t.activityLines(run.activity, 1, inner)...)
	}

	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Trigger extraction", true), Right: r.stateLabel()}

	return t.pinnedBox(o, head, body, nil, &run.list, keep, follow)
}

// shortStopNote is the one-line stop note of the compact box. The stop
// question gives the full text, with the billing note.
func (r *extRecord) shortStopNote() string {
	switch {
	case r.stopping() && r.run.stopAt >= 0:
		return "Seamark waits for the agent call of batch " + itoa(r.run.stopAt+1) + " to end."
	case r.stopping():
		return "Seamark waits for the running work to end."
	default:
		return "x stops the run. Saved answers stay saved."
	}
}

// batchesText names the size of the run for the notice row before the
// first batch starts, for example "12 proposals in 2 batches".
func (r *extRecord) batchesText() string {
	p := r.prev.preview

	return propPlural(len(p.Plan.IDs()), "proposal", "proposals") + " in " +
		propPlural(len(p.Plan.Batches), "batch", "batches")
}

// extBatchWords describes a batch outcome for the activity log. The
// words never merge two outcomes.
func extBatchWords(b distill.ExtractBatchOutcome) string {
	switch b.Status {
	case distill.BatchCompleted:
		var c extCounts

		for _, item := range b.Items {
			c.count(item, false)
		}

		text := "answered: " + itoa(c.stored) + " stored, " + itoa(c.negative) + " negative"
		if c.conflict > 0 {
			text += ", " + itoa(c.conflict) + " conflict"
		}

		return text
	case distill.BatchFailed:
		return "failed: " + agentFailWords(b.Failure)
	case distill.BatchCancelled:
		return "cancelled"
	default:
		return "not attempted"
	}
}

// modal returns the stop question of a running record.
func (r *extRecord) modal(w, h int) (string, bool) {
	if r.step == stepRunning && r.run.stopAsk {
		return r.stopModal(w, h), true
	}

	return "", false
}

// stopModal asks before a stop. It says what stops, what stays, and that
// a provider can still bill the cancelled call. It is at most 64 cells
// wide, like the stop question of a distillation.
func (r *extRecord) stopModal(w, h int) string {
	text := "Seamark starts no new batch and ends the run at the next check. Answers saved so far stay saved. " +
		"Nothing is retried."
	if k := r.run.sending; k >= 0 {
		text = "Seamark cancels the agent call of batch " + itoa(k+1) + " and starts no new batch. Answers saved " +
			"so far stay saved. A provider can still bill the cancelled call. Nothing is retried."
	}

	return runStopModal(r.st.theme, "Stop the trigger extraction?", text, withHelp(agentKeys.Start, "y", "stop the run"),
		w, h)
}
