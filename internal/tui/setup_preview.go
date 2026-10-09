package tui

import (
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/skills"
)

// The review step shows the plan: every affected path with its change
// in the Plan box, what the plan will and will not do, and the diff of
// the selected path in the diff box. The review is the confirmation:
// "y" applies exactly this plan, and a stale plan blocks it.

// setupEntryKind names what one path row of the plan describes.
type setupEntryKind int

const (
	setupEntryWrite setupEntryKind = iota
	setupEntrySkill
	setupEntryKeep
)

// setupEntry is one path row of the Plan box.
type setupEntry struct {
	kind setupEntryKind
	path string
	// consumers are the client IDs that contribute; empty for a shared
	// Seamark file.
	consumers []string
	detail    string
	write     *integration.WritePreview
	skill     *skills.Entry
}

// setupEntries lists the paths of a plan: the document writes in
// document order, the skill writes, then the paths that stay as they
// are. A no-op path stays visible, because the spec asks for kept and
// conflicting entries in the preview.
func setupEntries(p *SetupPreview) []setupEntry {
	if p == nil || p.Plan == nil {
		return nil
	}

	plan := p.Plan

	var writes, keeps []setupEntry

	seen := map[string]bool{}

	add := func(rel string) {
		if seen[rel] {
			return
		}

		for i := range plan.Writes {
			if fw := plan.Writes[i]; fw.Path == rel {
				seen[rel] = true
				writes = append(writes, setupEntry{kind: setupEntryWrite, path: rel, consumers: fw.Consumers,
					detail: fw.Detail, write: setupWritePreview(p, rel)})

				return
			}
		}

		for _, k := range plan.Kept {
			if k.Path == rel {
				seen[rel] = true
				keeps = append(keeps, setupEntry{kind: setupEntryKeep, path: rel, consumers: k.Consumers, detail: k.Detail})

				return
			}
		}
	}

	for _, g := range plan.Reads {
		add(g.Path)
	}

	// A write or keep without a guard cannot come from PlanSetup; list it
	// anyway, because a preview must never hide a path.
	for _, fw := range plan.Writes {
		add(fw.Path)
	}

	for _, k := range plan.Kept {
		add(k.Path)
	}

	var skillWrites, skillKeeps []setupEntry

	for i := range plan.Skills {
		entry := &plan.Skills[i]
		e := setupEntry{kind: setupEntrySkill, path: entry.Rel, skill: entry, consumers: setupSkillConsumers(plan, entry.Rel)}

		if entry.Writes() {
			skillWrites = append(skillWrites, e)
		} else {
			skillKeeps = append(skillKeeps, e)
		}
	}

	return slices.Concat(writes, skillWrites, keeps, skillKeeps)
}

// setupWritePreview returns the preview of one planned write.
func setupWritePreview(p *SetupPreview, rel string) *integration.WritePreview {
	for i := range p.Writes {
		if p.Writes[i].Path == rel {
			return &p.Writes[i]
		}
	}

	return nil
}

// setupSkillConsumers returns the clients that read a skill directory.
func setupSkillConsumers(plan *integration.SetupPlan, rel string) []string {
	for _, d := range plan.Destinations {
		if d.Dir == path.Dir(rel) {
			return d.Consumers
		}
	}

	return nil
}

// setupBlocked reports a preview that apply would refuse: a file
// changed after planning, or a current file could not be read.
func setupBlocked(p *SetupPreview) bool {
	return setupBlockReason(p) != ""
}

// setupBlockReason says why apply would refuse the plan, or "".
func setupBlockReason(p *SetupPreview) string {
	if p == nil {
		return ""
	}

	for _, wp := range p.Writes {
		if wp.Err != nil {
			return "A current file cannot be read. Fix it, then press p to plan again."
		}
	}

	for _, wp := range p.Writes {
		if wp.Stale {
			return "Files changed since planning. Press p to plan again."
		}
	}

	return ""
}

// setupCounts counts the changes of a plan: document writes, kept
// documents, skill directories to write and skill directories kept.
func setupCounts(p *SetupPreview) (writes, kept, skillWrites, skillKept int) {
	if p == nil || p.Plan == nil {
		return 0, 0, 0, 0
	}

	for _, e := range p.Plan.Skills {
		if e.Writes() {
			skillWrites++
		} else {
			skillKept++
		}
	}

	return len(p.Plan.Writes), len(p.Plan.Kept), skillWrites, skillKept
}

// setupPlural returns "1 file" or "3 files".
func setupPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return itoa(n) + " " + many
}

// setupCountLabel names the changes of a plan for the Plan box border,
// for example "2 files · 1 skill".
func setupCountLabel(p *SetupPreview, sep string) string {
	writes, _, skillWrites, _ := setupCounts(p)

	switch {
	case writes+skillWrites == 0:
		return "no changes"
	case skillWrites == 0:
		return setupPlural(writes, "file", "files")
	case writes == 0:
		return setupPlural(skillWrites, "skill", "skills")
	default:
		return setupPlural(writes, "file", "files") + " " + sep + " " + setupPlural(skillWrites, "skill", "skills")
	}
}

// canApply reports a preview that apply may take: planned, not stale,
// with at least one change.
func (wz *setupWizard) canApply() bool {
	if wz.planning || wz.planErr != nil || wz.preview == nil || setupBlocked(wz.preview) {
		return false
	}

	writes, _, skillWrites, _ := setupCounts(wz.preview)

	return writes+skillWrites > 0
}

func (wz *setupWizard) previewKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, setupKeys.Apply):
		return wz.applyKey()
	case key.Matches(msg, setupKeys.Back):
		// The draft stays: the choices keep every explicit toggle. The
		// cancel drops the result of a plan that still runs.
		wz.seq.cancel()
		wz.planning = false
		wz.forceSkills = false
		wz.pathOpen = false
		wz.step = setupStepChoices

		return nil
	case key.Matches(msg, setupKeys.Plan):
		if wz.planning {
			return nil
		}

		return wz.plan(wz.setups)
	case key.Matches(msg, nkeys.Back):
		if !wz.st.wide() && wz.pathOpen {
			wz.pathOpen = false

			return nil
		}

		// Cancel before apply writes nothing. A closed wizard drops the
		// result of a plan that still runs.
		wz.seq.cancel()
		wz.closed = true

		return nil
	}

	if wz.planning || wz.preview == nil {
		return nil
	}

	entries := setupEntries(wz.preview)

	if !wz.st.wide() && wz.pathOpen {
		wz.pager.scroll(msg)

		return nil
	}

	if wz.st.wide() {
		if key.Matches(msg, nkeys.Next, nkeys.Prev) {
			wz.notesFocus = !wz.notesFocus

			return nil
		}

		if wz.notesFocus {
			wz.scrollNotes(msg)

			return nil
		}
	}

	switch {
	case key.Matches(msg, nkeys.Up):
		wz.paths.move(-1, len(entries))
	case key.Matches(msg, nkeys.Down):
		wz.paths.move(1, len(entries))
	case key.Matches(msg, nkeys.Top):
		wz.paths.cursor = 0
	case key.Matches(msg, nkeys.Bottom):
		wz.paths.cursor = max(0, len(entries)-1)
	case key.Matches(msg, nkeys.PageUp, nkeys.PageDown):
		wz.scrollPlan(msg)
	case key.Matches(msg, nkeys.Enter):
		if !wz.st.wide() && len(entries) > 0 {
			wz.pathOpen = true
		}
	}

	return nil
}

// scrollPlan applies a page key. The wide layout scrolls the diff box;
// the narrow list has no diff box, so the key scrolls the plan notes.
func (wz *setupWizard) scrollPlan(msg tea.KeyMsg) {
	if wz.st.wide() {
		wz.pager.scroll(msg)

		return
	}

	step := max(1, (wz.st.height-chromeHeight)/2)
	if key.Matches(msg, nkeys.PageUp) {
		step = -step
	}

	// planLines clamps the offset against the rows it can show.
	wz.planOff = max(0, wz.planOff+step)
}

// scrollNotes moves the plan notes of the wide review by a line or a
// page. planLines clamps the offset against the rows it can show.
func (wz *setupWizard) scrollNotes(msg tea.KeyMsg) {
	page := max(1, (wz.st.height-chromeHeight)/2)

	switch {
	case key.Matches(msg, nkeys.Up):
		wz.planOff--
	case key.Matches(msg, nkeys.Down):
		wz.planOff++
	case key.Matches(msg, nkeys.PageUp):
		wz.planOff -= page
	case key.Matches(msg, nkeys.PageDown):
		wz.planOff += page
	case key.Matches(msg, nkeys.Top):
		wz.planOff = 0
	}

	wz.planOff = max(0, wz.planOff)
}

// applyKey applies the reviewed plan. It never applies a stale plan or
// a plan without a change; it says why instead.
func (wz *setupWizard) applyKey() tea.Cmd {
	if wz.planning || wz.preview == nil || wz.planErr != nil {
		return nil
	}

	if reason := setupBlockReason(wz.preview); reason != "" {
		return toast(ToneAttention, "Apply is blocked. "+reason)
	}

	if !wz.canApply() {
		return toast(ToneNeutral, "Nothing to apply: every selected item is already current.")
	}

	return wz.apply()
}

func (wz *setupWizard) previewKeys() []key.Binding {
	apply := setupKeys.Apply
	apply.SetEnabled(wz.canApply())

	plan := setupKeys.Plan
	plan.SetEnabled(!wz.planning && (wz.planErr != nil || setupBlocked(wz.preview)))

	back := setupKeys.Back
	cancel := withHelp(nkeys.Back, "esc", "cancel")

	if wz.planning || wz.preview == nil {
		return []key.Binding{plan, back, cancel}
	}

	switch {
	case wz.st.wide() && wz.notesFocus:
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "scroll notes"), withHelp(nkeys.Next, "tab", "files"),
			apply, back, cancel, plan}
	case wz.st.wide():
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "file"), withHelp(nkeys.PageDown, "pgup/pgdn", "scroll"),
			apply, back, withHelp(nkeys.Next, "tab", "notes"), cancel, plan}
	case wz.pathOpen:
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "scroll"), withHelp(nkeys.Back, "esc", "files"),
			apply, back, plan}
	default:
		shows := "details"
		if wz.selectedHasDiff() {
			shows = "diff"
		}

		return []key.Binding{withHelp(nkeys.Down, "↑↓", "file"), withHelp(nkeys.Enter, "enter", shows),
			apply, back, cancel, plan}
	}
}

// selectedHasDiff reports whether the path under the cursor shows a
// diff. A write without a preview or with an unreadable file has none.
func (wz *setupWizard) selectedHasDiff() bool {
	entries := setupEntries(wz.preview)
	if wz.paths.cursor < 0 || wz.paths.cursor >= len(entries) {
		return false
	}

	e := entries[wz.paths.cursor]

	return e.kind == setupEntryWrite && e.write != nil && e.write.Err == nil
}

// previewView renders the Plan box and the diff box side by side in a
// wide terminal, and one of them at a time in a narrow one.
func (wz *setupWizard) previewView(w, h int) string {
	wide := wz.st.wide() && w >= wideWidth

	// A plan without a setup plan cannot come from PlanSetup; the box
	// then says that no plan exists instead of an empty list.
	if wz.planning || wz.planErr != nil || wz.preview == nil || wz.preview.Plan == nil {
		return wz.planStateBox(w, h)
	}

	entries := setupEntries(wz.preview)

	if len(entries) == 0 {
		return wz.planBox(entries, w, h, true, wide)
	}

	wz.paths.clamp(len(entries), 0)
	sel := entries[wz.paths.cursor]

	if wide {
		leftW := max(34, setupSplit(w, 42))

		return hjoin(1, wz.planBox(entries, leftW, h, true, true), wz.entryBox(sel, w-leftW-1, h, false))
	}

	if wz.pathOpen {
		return wz.entryBox(sel, w, h, true)
	}

	return wz.planBox(entries, w, h, true, false)
}

// planStateBox renders the Plan box while planning runs or after a plan
// failure. Nothing was written in either state, because planning only
// reads.
func (wz *setupWizard) planStateBox(w, h int) string {
	t := wz.st.theme
	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

	if wz.planning {
		return integBoxed(t, o, ToneNeutral, "Planning the setup"+t.Glyphs.Ellipsis,
			"Seamark reads the native configuration of "+oneLine(wz.client.Name)+
				" and composes every change. Nothing is written.",
			t.Muted.Render("Press ")+t.Key.Render("n")+t.Muted.Render(" to go back to the choices."))
	}

	err := wz.planErr
	if err == nil {
		return integBoxed(t, o, ToneUnknown, "No plan", "The planner returned no plan. Nothing was written.",
			t.hints([]key.Binding{withHelp(setupKeys.Plan, "p", "plan again"), withHelp(setupKeys.Back, "n", "back")}))
	}

	title := "Could not plan the setup"
	next := "Fix or move the named file, then press p to plan again. Nothing was written."

	switch {
	case errors.Is(err, integration.ErrPlanConflict):
		title = "The plan has conflicting changes"
		next = "Two planners want different content for one file. Plan fewer additions, " +
			"or fix the file, then press p. Nothing was written."
	case errors.Is(err, integration.ErrStalePlan):
		title = "A file changed while planning"
		next = "Press p to plan again. Nothing was written."
	}

	inner := min(boxInner(w)-4, 72)
	body := t.Failure.Render(wrap(errText(err), inner)) + "\n\n" + t.Muted.Render(wrap(next, inner))

	return integBoxed(t, o, ToneFailure, title, "", body+"\n\n"+t.hints([]key.Binding{
		withHelp(setupKeys.Plan, "p", "plan again"), withHelp(setupKeys.Back, "n", "back to choices")}))
}

// planBox renders the path rows, then what the plan will and will not
// do, and the line that says how to apply.
func (wz *setupWizard) planBox(entries []setupEntry, w, h int, focused, wide bool) string {
	t := wz.st.theme

	return t.box(boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Plan", true),
		Right: t.Muted.Render(setupCountLabel(wz.preview, t.Glyphs.Sep)),
		Lines: wz.planLines(entries, boxInner(w), boxRows(h), focused, wide),
	})
}

// planLines fits the path rows and the plan notes in rows lines. The
// path rows come first and keep at least three lines; the notes give
// way from their end. The last lines, which say how to apply, always
// stay visible.
func (wz *setupWizard) planLines(entries []setupEntry, w, rows int, focused, wide bool) []string {
	body, tail := wz.planNotes(entries, w)

	// The path rows keep at least half of the free rows, because the
	// diff box follows the selected path.
	n := len(entries)
	window := n

	if n+len(body)+len(tail) > rows {
		free := rows - len(tail)
		window = min(n, max(min(n, 3), free/2, free-len(body)))
	}

	list := wz.pathRows(entries, w, window, focused)

	avail := rows - len(list) - len(tail)
	if len(body) > avail {
		body = wz.clipNotes(body, avail, w, wide)
	}

	return slices.Concat(list, body, tail)
}

// clipNotes shows avail lines of the plan notes from the scroll offset,
// with a last line that tells which lines show and how to scroll. The
// narrow layout scrolls them with the page keys; the wide layout gives
// the page keys to the diff box, so tab moves the keys to the notes.
func (wz *setupWizard) clipNotes(body []string, avail, w int, wide bool) []string {
	t := wz.st.theme

	if avail <= 0 {
		return nil
	}

	shown := avail - 1

	wz.planOff = max(0, min(wz.planOff, len(body)-shown))

	how := "pgup/pgdn"

	switch {
	case wide && wz.notesFocus:
		how = "↑↓ scroll " + t.Glyphs.Sep + " tab files"
	case wide:
		how = "tab to scroll"
	}

	note := t.Glyphs.Ellipsis + " notes " + t.positionNote(wz.planOff+1, wz.planOff+shown, len(body)) + " " +
		t.Glyphs.Sep + " " + how

	return append(slices.Clone(body[wz.planOff:wz.planOff+shown]), t.Faint.Render(truncate(note, w, t.Glyphs.Ellipsis)))
}

// pathRows renders a window of the path rows around the cursor. A list
// that does not fit gives its last line to a position note.
func (wz *setupWizard) pathRows(entries []setupEntry, w, window int, focused bool) []string {
	t := wz.st.theme

	if len(entries) == 0 {
		return integLines(t.Muted, "The plan names no path.", w)
	}

	shown := window
	if len(entries) > window && window >= 2 {
		shown = window - 1
	}

	wz.paths.clamp(len(entries), shown)

	var lines []string

	for i := wz.paths.offset; i < min(len(entries), wz.paths.offset+shown); i++ {
		lines = append(lines, wz.pathRow(entries[i], i == wz.paths.cursor, w, focused))
	}

	if shown < len(entries) {
		lines = append(lines, t.scrollNote(wz.paths.offset, shown, len(entries), w))
	}

	return lines
}

// pathRow renders one path: the cursor, the change mark, the path, and
// the line counts or the state at the right. The mark keeps the meaning
// without color: "A" new, "M" modified, "=" kept, "!" a problem.
func (wz *setupWizard) pathRow(e setupEntry, current bool, w int, focused bool) string {
	t := wz.st.theme

	cursor := "  "
	if current {
		cursor = t.Key.Render(t.Glyphs.Cursor) + " "
	}

	mark, right := setupEntryMarks(t, e)
	style := t.Body

	switch {
	case current:
		style = t.Title
	case e.kind == setupEntryKeep || (e.kind == setupEntrySkill && e.skill.State == skills.Current):
		style = t.Muted
	}

	right += " "
	room := max(4, w-width(cursor)-2-width(right)-1)
	left := cursor + mark + " " + style.Render(truncateLeft(oneLine(e.path), room, t.Glyphs.Ellipsis))

	row := spread(left, right, w)
	if current && focused {
		row = t.highlight(row, w, bgSel)
	}

	return row
}

// setupEntryMarks returns the change mark of a path row and the label
// at its right.
func setupEntryMarks(t *Theme, e setupEntry) (mark, right string) {
	g := t.Glyphs

	switch e.kind {
	case setupEntryWrite:
		wp := e.write

		switch {
		case wp == nil:
			return t.Unknown.Render(g.Unknown), t.Unknown.Render("no diff")
		case wp.Err != nil:
			return t.Failure.Render(g.Fail), t.Failure.Render("unreadable")
		case wp.Stale:
			return t.Failure.Render(g.Fail), t.Failure.Render("stale")
		case wp.Created:
			return t.Success.Render("A"), setupLineCounts(t, wp)
		default:
			return t.Attention.Render("M"), setupLineCounts(t, wp)
		}
	case setupEntrySkill:
		switch e.skill.State {
		case skills.Absent:
			return t.Success.Render("A"), t.Faint.Render("new")
		case skills.Stale:
			return t.Attention.Render("M"), t.Faint.Render("refresh")
		case skills.Foreign:
			return t.Attention.Render(g.Warn), t.Attention.Render("not managed")
		default:
			return t.Faint.Render("="), t.Faint.Render("kept")
		}
	default:
		return t.Faint.Render("="), t.Faint.Render("kept")
	}
}

// setupLineCounts renders "+11 −1" for a planned write. A side without
// a change is left out, as in the design.
func setupLineCounts(t *Theme, wp *integration.WritePreview) string {
	var parts []string

	if wp.Added > 0 {
		parts = append(parts, t.Success.Render(t.Glyphs.Add+itoa(wp.Added)))
	}

	if wp.Removed > 0 {
		parts = append(parts, t.Failure.Render(t.Glyphs.Del+itoa(wp.Removed)))
	}

	if len(parts) == 0 {
		return t.Faint.Render("0 lines")
	}

	return strings.Join(parts, " ")
}

// planNotes returns the notes under the path rows: what the plan will
// do, what it will not do, the gate hooks after apply and the findings.
// tail is the rule and the line that says how to apply; it always shows.
func (wz *setupWizard) planNotes(entries []setupEntry, w int) (body, tail []string) {
	t := wz.st.theme
	p := wz.preview

	body = append(body, "", integHeading(t, "Will"))
	body = append(body, wz.willLines(entries, w)...)

	body = append(body, "", integHeading(t, "Won't"))
	body = append(body, wz.wontLines(w)...)

	if len(p.Plan.GateHooks) > 0 {
		body = append(body, "", integHeading(t, "Gate after apply"))
		body = append(body, setupGateHookLines(t, p.Plan.GateHooks, w)...)
	}

	if notes := wz.planFindings(w); len(notes) > 0 {
		body = append(body, "", integHeading(t, "Notes"))
		body = append(body, notes...)
	}

	tail = []string{"", integRule(t, w)}

	writes, _, skillWrites, _ := setupCounts(p)

	switch reason := setupBlockReason(p); {
	case reason != "":
		tail = append(tail, integHanging(t.Failure.Render(t.Glyphs.Fail+" "), t.Failure, reason, w)...)
	case writes+skillWrites == 0:
		tail = append(tail, integHanging(t.Success.Render(t.Glyphs.OK+" "), t.Body,
			"Nothing to change: every selected item is already current.", w)...)
	default:
		tail = append(tail, t.Body.Render("Nothing is written until you press ")+t.Key.Render("y")+t.Body.Render("."))
	}

	return body, tail
}

// willLines lists every change of the plan: "+" for a new file or
// skill, "~" for a modified one. The words come from the planner's
// description of each write.
func (wz *setupWizard) willLines(entries []setupEntry, w int) []string {
	t := wz.st.theme
	added := t.Success.Render("+") + " "
	changed := t.Attention.Render("~") + " "

	var lines []string

	for _, e := range entries {
		switch {
		case e.kind == setupEntryWrite:
			sign := changed
			if e.write != nil && e.write.Created {
				sign = added
			}

			text := oneLine(e.detail)
			if text == "" {
				text = "write " + oneLine(e.path)
			}

			lines = append(lines, integHanging(sign, t.Body, text, w)...)
		case e.kind == setupEntrySkill && e.skill.State == skills.Absent:
			lines = append(lines, integHanging(added, t.Body, "install the "+oneLine(e.skill.Name)+" skill", w)...)
		case e.kind == setupEntrySkill && e.skill.State == skills.Stale:
			lines = append(lines, integHanging(changed, t.Body, "refresh the "+oneLine(e.skill.Name)+" skill", w)...)
		}
	}

	if len(lines) == 0 {
		return integHanging(t.Faint.Render(t.Glyphs.Info+" "), t.Muted, "no change: every selected item is current", w)
	}

	return lines
}

// wontLines says what the plan leaves as it is: the additions that the
// user did not request, the configuration of the other clients, and the
// inference command.
func (wz *setupWizard) wontLines(w int) []string {
	t := wz.st.theme
	dot := t.Faint.Render(t.Glyphs.Info) + " "

	var cs integration.ClientSetup
	if len(wz.setups) > 0 {
		cs = wz.setups[0]
	}

	requested := map[setupIntent]bool{
		setupMCP: cs.RegisterMCP, setupHooks: cs.Hooks, setupSkills: cs.Skills, setupGrants: cs.ApproveTools,
	}

	var skipped []string

	if insp, ok := wz.inspection(); ok {
		for _, r := range setupRows(wz.client, insp) {
			if r.kind != setupRowUnsupported && !requested[r.intent] {
				skipped = append(skipped, setupIntentWords[r.intent])
			}
		}
	}

	var others []string

	for _, c := range wz.clients {
		if c.ID != wz.client.ID {
			others = append(others, oneLine(c.Name))
		}
	}

	var lines []string

	if len(skipped) > 0 {
		lines = append(lines, integHanging(dot, t.Muted, "touch "+strings.Join(skipped, ", "), w)...)
	}

	if len(others) > 0 {
		lines = append(lines, integHanging(dot, t.Muted, "touch the configuration of "+setupJoinWords(others), w)...)
	}

	return append(lines, integHanging(dot, t.Muted, "change agent.cli or agent.argv", w)...)
}

// setupGateHookLines lists the gate hooks that run after apply, one line
// each: the mode, the file and who manages it. The list covers unmanaged
// hooks too, because one enforcing hook blocks whatever the managed hook
// says. The wizard plans one client, so the line needs no client name.
func setupGateHookLines(t *Theme, hooks []integration.GateHook, w int) []string {
	var lines []string

	for _, g := range hooks {
		owner := " " + t.Glyphs.Sep + " managed"
		if !g.Managed {
			owner = " " + t.Glyphs.Sep + " not managed by setup"
		}

		lead := t.Title.Render(oneLine(g.Mode)) + " "
		room := max(4, w-width(lead)-width(owner))
		lines = append(lines, truncate(lead+t.Code.Render(truncateLeft(oneLine(g.Path), room, t.Glyphs.Ellipsis))+
			t.Muted.Render(owner), w, t.Glyphs.Ellipsis))
	}

	return lines
}

// planFindings renders the findings of the plan and the scope note of
// tool grants. Both name steps that stay with the user, such as native
// trust, so they show before the user presses y.
func (wz *setupWizard) planFindings(w int) []string {
	t := wz.st.theme
	p := wz.preview

	var lines []string

	if len(p.Plan.Findings) > 0 {
		lines = append(lines, integFindingLines(t, p.Plan.Findings, w, false)...)
	}

	if slices.ContainsFunc(p.Setups, func(cs integration.ClientSetup) bool { return cs.ApproveTools }) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}

		lines = append(lines, integHanging(t.Faint.Render(t.Glyphs.Info)+" ", t.Muted,
			"Tool grants are project configuration only: user or managed policy can still prompt. "+
				"Seamark never grants native trust.", w)...)
	}

	return lines
}

// entryBox renders the selected path: the diff of a write, or the
// details of a skill directory or a kept file. The bottom border shows
// the visible range.
func (wz *setupWizard) entryBox(e setupEntry, w, h int, focused bool) string {
	t := wz.st.theme
	inner, rows := boxInner(w), boxRows(h)

	content := wz.pager.render(itoa(int(e.kind))+":"+e.path, strings.Join(wz.entryLines(e, inner), "\n"), inner, rows)

	foot := ""
	if span := wz.pager.span(t); span != "" {
		foot = t.Faint.Render(span)
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused, Foot: foot, Right: setupPathState(t, e), Lines: strings.Split(content, "\n"),
		Title: t.Title.Render(truncateLeft(oneLine(e.path), max(8, inner-14), t.Glyphs.Ellipsis)),
	})
}

// setupPathState returns the state word of a path for the diff box
// border: "new", "modified", "kept" or the problem.
func setupPathState(t *Theme, e setupEntry) string {
	switch e.kind {
	case setupEntryWrite:
		wp := e.write

		switch {
		case wp == nil:
			return t.Unknown.Render("no diff")
		case wp.Err != nil:
			return t.Failure.Render("unreadable")
		case wp.Stale:
			return t.Failure.Render("stale")
		case wp.Created:
			return t.Success.Render("new")
		default:
			return t.Attention.Render("modified")
		}
	case setupEntrySkill:
		switch e.skill.State {
		case skills.Absent:
			return t.Success.Render("new")
		case skills.Stale:
			return t.Attention.Render("refresh")
		case skills.Foreign:
			return t.Attention.Render("not managed")
		}
	}

	return t.Faint.Render("kept")
}

// entryLines renders the content of the diff box for one path.
func (wz *setupWizard) entryLines(e setupEntry, w int) []string {
	switch e.kind {
	case setupEntryWrite:
		return wz.writeLines(e, w)
	case setupEntrySkill:
		return wz.skillLines(e, w)
	default:
		return wz.keepLines(e, w)
	}
}

// writeLines renders one planned write: who uses the file, what the
// write adds, and the diff. A stale or unreadable file says why apply
// refuses the plan.
func (wz *setupWizard) writeLines(e setupEntry, w int) []string {
	t := wz.st.theme
	wp := e.write

	meta := "Used by " + integConsumers(wz.clients, e.consumers)
	if e.detail != "" {
		meta += " " + t.Glyphs.Sep + " " + oneLine(e.detail)
	}

	lines := integLines(t.Muted, meta, w)
	fail := t.Failure.Render(t.Glyphs.Fail + " ")

	switch {
	case wp == nil:
		return append(append(lines, ""), integHanging(t.Unknown.Render(t.Glyphs.Unknown+" "), t.Body,
			"No diff is available for this write.", w)...)
	case wp.Err != nil:
		lines = append(lines, "")
		lines = append(lines, integHanging(fail, t.Failure, "The current file cannot be read: "+errText(wp.Err), w)...)

		return append(lines, integLines(t.Muted, "Apply refuses this plan. Fix the file, then press p to plan again.", w)...)
	}

	if wp.Stale {
		lines = append(lines, "")
		lines = append(lines, integHanging(fail, t.Failure,
			"This file changed since planning. Apply refuses this plan; press p to plan again.", w)...)
	}

	return append(append(lines, ""), setupDiffLines(t, wp.Diff, w)...)
}

// keepLines renders one kept document.
func (wz *setupWizard) keepLines(e setupEntry, w int) []string {
	t := wz.st.theme

	lines := integLabeled(t, "Change", 9, "kept: this file stays as it is", t.Body, w)
	lines = append(lines, integLabeled(t, "Used by", 9, integConsumers(wz.clients, e.consumers), t.Body, w)...)

	if e.detail != "" {
		lines = append(lines, integLabeled(t, "Why", 9, oneLine(e.detail), t.Body, w)...)
	}

	return lines
}

// skillLines renders one skill directory of the plan. Setup never shows
// a diff of a skill, because it writes the shipped files as they are.
func (wz *setupWizard) skillLines(e setupEntry, w int) []string {
	t := wz.st.theme
	entry := e.skill

	change := map[skills.State]string{
		skills.Absent:  "install: the directory is new",
		skills.Stale:   "refresh: rewrites the shipped files and keeps extra files",
		skills.Current: "kept: the managed copy is current",
		skills.Foreign: "kept: the directory is not managed by Seamark (" + oneLine(entry.Reason) + ")",
	}[entry.State]

	lines := integLabeled(t, "Skill", 9, oneLine(entry.Name), t.Body, w)
	lines = append(lines, integLabeled(t, "Change", 9, change, t.Body, w)...)
	lines = append(lines, integLabeled(t, "Read by", 9, integConsumers(wz.clients, e.consumers), t.Body, w)...)

	if entry.State == skills.Foreign {
		lines = append(lines, "")
		lines = append(lines, integLines(t.Muted, "This directory has no Seamark marker. Setup never touches it.", w)...)
	}

	return lines
}

// apply claims the mutation slot and applies the reviewed plan. A busy
// slot keeps the review; nothing starts.
func (wz *setupWizard) apply() tea.Cmd {
	name := oneLine(wz.client.Name)

	if !wz.st.begin("Applying setup for " + name) {
		return toast(ToneAttention, "Another operation is running: "+wz.st.mutation)
	}

	seq := wz.seq.start()
	wz.step = setupStepApplying
	preview := wz.preview

	var svc Setup = wz.st.svc

	// An empty refresh reloads nothing. The shell then starts the
	// activity ticker, which a key press alone does not start.
	return tea.Batch(refresh(), func() tea.Msg {
		res, err := svc.ApplySetup(preview)

		return setupAppliedMsg{owner: wz, seq: seq, result: res, err: err}
	})
}
