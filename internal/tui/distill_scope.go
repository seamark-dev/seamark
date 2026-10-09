package tui

import (
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the frame parts that every distillation step shares
// (the context line and the scrolling box) and the scope form (design
// map §4).

// distLabelW is the label column of the scope form: "Runs in" and the
// other field names fit with a gap.
const distLabelW = 12

// view renders the context line, a blank line and the step. The boxes
// leave the last row blank above the footer, as the other sub-views do.
func (d *distRecord) view(w, h int) string {
	boxH := max(4, h-3)

	var body string

	switch d.step {
	case stepScope:
		body = d.scopeView(w, boxH)
	case stepPreparing, stepPreview:
		body = d.previewView(w, boxH)
	default:
		body = d.runView(w, boxH)
	}

	return strings.Join([]string{d.contextLine(w), "", body}, "\n")
}

// contextLine renders the facts of the step at the left and the step
// trail at the right. Lessons has no banner row, so the trail sits on
// the context line. A narrow line keeps only the current step, for
// example "● Scope 1/4", and cuts the facts to fit it. Only a line too
// narrow for agentFactsMin drops the trail.
func (d *distRecord) contextLine(w int) string {
	t := d.st.theme
	g := t.Glyphs
	left := " " + d.facts(w < ui.WideWidth)

	steps, cur := runTrail(true), d.step.stepper(true)

	trail := stepper(t, steps, cur)
	if ui.Width(left)+ui.Width(trail)+3 > w {
		trail = t.TabActive.Render(g.Dot+" "+steps[cur]) + t.Faint.Render(" "+strconv.Itoa(cur+1)+"/"+strconv.Itoa(len(steps)))
	}

	room := w - ui.Width(trail) - 3

	switch {
	case ui.Width(left) <= room:
	case room >= agentFactsMin:
		left = ui.Truncate(left, room, g.Ellipsis)
	default:
		return ui.Truncate(left, w, g.Ellipsis)
	}

	return ui.Spread(left, trail+" ", w)
}

// facts renders the context facts of the step. They are the scope, the
// limit, the agent and the time of the observation. compact joins the
// values without their labels.
func (d *distRecord) facts(compact bool) string {
	t := d.st.theme
	sep := " " + t.Glyphs.Sep + " "

	if d.step == stepScope {
		// The short form leaves room for the step trail at 60 columns.
		state := "nothing is planned or sent yet"
		if compact {
			state = "nothing sent yet"
		}

		return t.Title.Render("Distillation preparation") + t.Faint.Render(sep+state)
	}

	type fact struct{ label, value string }

	facts := []fact{{"scope", d.scopeLabel()}, {"limit", distLimitLabel(d.req)}}

	if p := d.prev.preview; p != nil && d.prev.err == nil {
		facts = append(facts, fact{"agent", ui.OneLine(p.Invocation.Name)})
	}

	switch {
	case d.step == stepPreparing:
		facts = append(facts, fact{"", "planning" + t.Glyphs.Ellipsis})
	case d.step == stepRunning:
		facts = append(facts, fact{"started", agentClock(d.run.started)})
	case d.step == stepResult:
		facts = append(facts, fact{"finished", agentClock(d.run.ended)})
	case d.prev.preview != nil && !d.prev.preview.PlannedAt.IsZero():
		facts = append(facts, fact{"planned", agentClock(d.prev.preview.PlannedAt)})
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

	return strings.Join(parts, "     ")
}

// scopeView renders the form and its explanation: two boxes in the wide
// layout, one box in the compact layout. The compact box names the key
// that shows the full explanation.
func (d *distRecord) scopeView(w, h int) string {
	t := d.st.theme

	if !d.st.wide() {
		if d.form.about {
			o := ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "About this run", true)}

			return d.st.theme.ScrollBox(o, d.aboutLines(ui.BoxInner(w)), nil, &d.form.aboutSc, [2]int{-1, -1}, false)
		}

		return d.formBox(w, h, true)
	}

	leftW := setupSplit(w, 60)
	about := t.Box(ui.BoxOpts{
		W: w - leftW - 1, H: h, Title: integPaneTitle(t, "About this run", false),
		Lines: d.aboutLines(ui.BoxInner(w - leftW - 1)),
	})

	return ui.HJoin(1, d.formBox(leftW, h, false), about)
}

// formBox renders the scope form with the confirmation tail. The compact
// layout adds the start of the explanation and the key that shows all.
func (d *distRecord) formBox(w, h int, compact bool) string {
	t := d.st.theme
	inner := ui.BoxInner(w)

	body, keep := d.formLines(inner)

	tail := []string{"", integRule(t, inner)}
	tail = append(tail, integLines(t.Body, "Planning reads only. The agent runs only after you review the plan "+
		"and press y.", inner)...)

	// A short box gives up the blank line above the rule first, so the
	// fields keep one more row.
	if compact || len(body)+len(tail) > ui.BoxRows(h) {
		tail = tail[1:]
	}

	if compact {
		room := ui.BoxRows(h) - len(body) - len(tail) - 1
		if about := d.aboutLines(inner); room > 0 {
			body = append(body, "")
			body = append(body, setupClip(t, about, inner, room)...)
		}
	}

	o := ui.BoxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Distillation scope", true),
		Right: t.Attention.Render("draft"),
	}

	return d.st.theme.ScrollBox(o, body, tail, &d.form.sc, keep, true)
}

// formLines renders the field rows. It returns the line range of the
// cursor row, so a short box keeps it visible.
func (d *distRecord) formLines(w int) (lines []string, keep [2]int) {
	t := d.st.theme
	f := &d.form

	lines = []string{t.Faint.Render(ui.PadRight("  Field", distLabelW+2) + "Value")}
	keep = [2]int{-1, -1}

	row := func(r int, label, value string, notes ...string) {
		if r == f.row {
			keep = [2]int{len(lines), len(lines) + len(notes)}
		}

		lines = append(lines, d.formRow(r, label, value, w))

		for _, n := range notes {
			lines = append(lines, strings.Repeat(" ", distLabelW+2)+n)
		}
	}

	row(distRowRegion, "Region", d.regionValue(), d.regionNotes()...)
	row(distRowDefault, "Limit", radio(t, f.limit == distLimitDefault,
		strconv.Itoa(distill.DefaultGroupLimit)+" groups (default)"))
	row(distRowCustom, "", d.customValue(), d.customNotes()...)
	row(distRowAll, "", radio(t, f.limit == distLimitAll, "All eligible groups"))

	agent, dir := d.agentRows(w)
	lines = append(lines, d.formRow(-1, "Agent", agent, w), d.formRow(-1, "Runs in", dir, w))

	if f.note != "" {
		lines = append(lines, "")
		lines = append(lines, propNote(t, f.noteTone, f.note, w)...)
	}

	return lines, keep
}

// formRow renders one field row with the cursor column. The cursor row
// has the cursor glyph, a bold label and the selection background.
func (d *distRecord) formRow(r int, label, value string, w int) string {
	t := d.st.theme

	if r >= 0 && r == d.form.row {
		line := t.Key.Render(t.Glyphs.Cursor) + " " + t.Title.Render(ui.PadRight(label, distLabelW)) + value

		return t.Highlight(line, w, ui.BgSel)
	}

	return "  " + t.Muted.Render(ui.PadRight(label, distLabelW)) + value
}

// regionValue renders the region input or its value. An empty region
// reads "everywhere", because the run then covers the whole repository.
func (d *distRecord) regionValue() string {
	t := d.st.theme
	f := &d.form

	switch {
	case f.editRegion:
		return f.region.View(t)
	case strings.TrimSpace(f.region.Value()) == "":
		return t.Muted.Render("everywhere")
	default:
		return t.Body.Render(ui.OneLine(f.region.Value()))
	}
}

// regionNotes shows the normalized scope before any plan, a path outside
// the workspace, and the prefill source.
func (d *distRecord) regionNotes() []string {
	t := d.st.theme
	f := &d.form
	sep := " " + t.Glyphs.Sep + " "

	region, err := d.region()
	if err != nil {
		return []string{t.Failure.Render(t.Glyphs.Fail + " outside the workspace")}
	}

	scope := "the whole repository"
	if region != "" {
		scope = "scope " + region
	}

	notes := []string{t.Faint.Render(ui.OneLine(scope) + sep + "/ edits")}

	if f.prefill != "" && strings.TrimSpace(f.region.Value()) == f.prefill {
		notes = append(notes, t.Faint.Render("prefilled from the Proposals region filter"))
	}

	return notes
}

// customValue renders the custom limit choice with its number.
func (d *distRecord) customValue() string {
	t := d.st.theme
	f := &d.form

	on := f.limit == distLimitCustom
	out := radio(t, on, "custom")

	switch {
	case f.editCustom:
		out += t.Muted.Render(": ") + f.custom.View(t)
	case on && f.custom.Value() != "":
		// The input takes digits only, so the value is a number.
		n, _ := strconv.Atoi(f.custom.Value())
		out += t.Muted.Render(": ") + t.Body.Render(propPlural(n, "group", "groups"))
	}

	return out
}

// customNotes explains an invalid custom limit once the input closed or
// a plan was asked for. A just opened input shows its placeholder only,
// because the user did not type yet. enter stays off until the number
// is valid.
func (d *distRecord) customNotes() []string {
	t := d.st.theme

	if d.form.limit != distLimitCustom || !d.form.customChecked {
		return nil
	}

	if _, reason := d.request(); reason == "Enter a whole number above 0." {
		return []string{t.Failure.Render(t.Glyphs.Fail + " " + reason)}
	}

	return nil
}

// agentRows renders the agent and its directory from the settings
// snapshot. The plan resolves both again, and the review shows the
// result; this row only previews them. An invalid configuration blocks
// the plan, because a paid run must not fall back to a default.
func (d *distRecord) agentRows(w int) (agent, dir string) {
	t := d.st.theme
	hint := t.Faint.Render("   Settings " + t.Glyphs.Arrow)

	// A blocked configuration is not a failure of the run: design map §9
	// gives a blocked state the warning glyph and the attention color.
	if reason := d.agentBlock(); reason != "" {
		return t.Attention.Render(t.Glyphs.Warn+" "+reason) + hint, t.Muted.Render("unknown")
	}

	snap := d.settingsSnap()
	if snap == nil || len(snap.Invocation.Argv) == 0 {
		return t.Muted.Render("resolved when you plan") + hint, t.Muted.Render("resolved when you plan")
	}

	custom := false
	if f, ok := snap.Field(settings.AgentArgv); ok && f.Source == settings.SourceFile {
		custom = true
	}

	// A long command line needs the row, so the Settings hint gives way
	// first. Then the command line is cut before the kind.
	kind := agentKind(snap.Invocation.Name, custom)
	room := w - 2 - distLabelW

	agent = agentText(t, snap.Invocation.Argv, kind, 0) + hint
	if ui.Width(agent) > room {
		agent = agentText(t, snap.Invocation.Argv, kind, room)
	}

	if snap.Invocation.Dir != "" {
		return agent, t.Body.Render(ui.OneLine(ui.DisplayPath(snap.Invocation.Dir)))
	}

	return agent, t.Muted.Render("the directory of the console")
}

// aboutLines explains the scope, the limit, what is sent and what enter
// does (design map §4).
func (d *distRecord) aboutLines(w int) []string {
	t := d.st.theme

	var out []string

	section := func(head, text string) {
		if len(out) > 0 {
			out = append(out, "")
		}

		out = append(out, integHeading(t, head))
		out = append(out, integLines(t.Body, text, w)...)
	}

	section("Region", "The region limits the evidence groups to one directory. An empty region means the whole "+
		"repository. A path outside the workspace is refused.")
	section("Group limit", "The limit caps the evidence groups sent to the agent in this run. It is not a token "+
		"or cost budget. "+strconv.Itoa(distill.DefaultGroupLimit)+" is the CLI default. All eligible groups sends every "+
		"group without a recorded signature, so choose it on purpose.")
	section("What is sent", "Each group sends its findings, which hold code and review text, to the agent. The "+
		"agent can also load the project instructions of this workspace.")
	section("Next", "enter builds a read-only plan: the groups, their evidence and an approximate prompt size. "+
		"No agent call happens.")

	return out
}
