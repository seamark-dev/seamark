package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/settings"
)

// This file renders the frame parts that every distillation step shares
// (the context line and the scrolling box) and the scope form (design
// map §4).

// distLabelW is the label column of the scope form: "Runs in" and the
// other field names fit with a gap.
const distLabelW = 12

// distFactsMin is the narrowest context facts next to the compact step
// trail. The facts give way to the trail down to this width, so every
// supported terminal shows the step (design map §4).
const distFactsMin = 20

// view renders the context line, a blank line and the step. The boxes
// leave the last row blank above the footer, as the other sub-views do.
func (d *distRecord) view(w, h int) string {
	boxH := max(4, h-3)

	var body string

	switch d.step {
	case distScope:
		body = d.scopeView(w, boxH)
	case distPlanning, distReview:
		body = d.reviewView(w, boxH)
	default:
		body = d.runView(w, boxH)
	}

	return strings.Join([]string{d.contextLine(w), "", body}, "\n")
}

// contextLine renders the facts of the step at the left and the step
// trail at the right. Lessons has no banner row, so the trail sits on
// the context line. A narrow line keeps only the current step, for
// example "● Scope 1/4", and cuts the facts to fit it. Only a line too
// narrow for distFactsMin drops the trail.
func (d *distRecord) contextLine(w int) string {
	t := d.st.theme
	g := t.Glyphs
	left := " " + d.facts(w < wideWidth)

	cur := d.step.stepper()

	trail := t.stepper(distSteps, cur)
	if width(left)+width(trail)+3 > w {
		trail = t.TabActive.Render(g.Dot+" "+distSteps[cur]) + t.Faint.Render(" "+itoa(cur+1)+"/"+itoa(len(distSteps)))
	}

	room := w - width(trail) - 3

	switch {
	case width(left) <= room:
	case room >= distFactsMin:
		left = truncate(left, room, g.Ellipsis)
	default:
		return truncate(left, w, g.Ellipsis)
	}

	return spread(left, trail+" ", w)
}

// facts renders the context facts of the step. They are the scope, the
// limit, the agent and the time of the observation. compact joins the
// values without their labels.
func (d *distRecord) facts(compact bool) string {
	t := d.st.theme
	sep := " " + t.Glyphs.Sep + " "

	if d.step == distScope {
		// The short form leaves room for the step trail at 60 columns.
		state := "nothing is planned or sent yet"
		if compact {
			state = "nothing sent yet"
		}

		return t.Title.Render("Distillation preparation") + t.Faint.Render(sep+state)
	}

	type fact struct{ label, value string }

	facts := []fact{{"scope", d.scopeLabel()}, {"limit", distLimitLabel(d.plan.req)}}

	if p := d.plan.preview; p != nil && d.plan.err == nil {
		facts = append(facts, fact{"agent", oneLine(p.Invocation.Name)})
	}

	switch {
	case d.step == distPlanning:
		facts = append(facts, fact{"", "planning" + t.Glyphs.Ellipsis})
	case d.step == distRunning:
		facts = append(facts, fact{"started", distClock(d.run.started)})
	case d.step == distResult:
		facts = append(facts, fact{"finished", distClock(d.run.ended)})
	case d.plan.preview != nil && !d.plan.preview.PlannedAt.IsZero():
		facts = append(facts, fact{"planned", distClock(d.plan.preview.PlannedAt)})
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

	if !d.wide() {
		if d.form.about {
			o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "About this run", true)}

			return d.st.theme.scrollBox(o, d.aboutLines(boxInner(w)), nil, &d.form.aboutSc, [2]int{-1, -1}, false)
		}

		return d.formBox(w, h, true)
	}

	leftW := setupSplit(w, 60)
	about := t.box(boxOpts{
		W: w - leftW - 1, H: h, Title: integPaneTitle(t, "About this run", false),
		Lines: d.aboutLines(boxInner(w - leftW - 1)),
	})

	return hjoin(1, d.formBox(leftW, h, false), about)
}

// formBox renders the scope form with the confirmation tail. The compact
// layout adds the start of the explanation and the key that shows all.
func (d *distRecord) formBox(w, h int, compact bool) string {
	t := d.st.theme
	inner := boxInner(w)

	body, keep := d.formLines(inner)

	tail := []string{"", integRule(t, inner)}
	tail = append(tail, integLines(t.Body, "Planning reads only. The agent runs only after you review the plan "+
		"and press y.", inner)...)

	// A short box gives up the blank line above the rule first, so the
	// fields keep one more row.
	if compact || len(body)+len(tail) > boxRows(h) {
		tail = tail[1:]
	}

	if compact {
		room := boxRows(h) - len(body) - len(tail) - 1
		if about := d.aboutLines(inner); room > 0 {
			body = append(body, "")
			body = append(body, setupClip(t, about, inner, room)...)
		}
	}

	o := boxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Distillation scope", true),
		Right: t.Attention.Render("draft"),
	}

	return d.st.theme.scrollBox(o, body, tail, &d.form.sc, keep, true)
}

// formLines renders the field rows. It returns the line range of the
// cursor row, so a short box keeps it visible.
func (d *distRecord) formLines(w int) (lines []string, keep [2]int) {
	t := d.st.theme
	f := &d.form

	lines = []string{t.Faint.Render(padRight("  Field", distLabelW+2) + "Value")}
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
	row(distRowDefault, "Limit", t.radio(f.limit == distLimitDefault,
		itoa(distill.DefaultGroupLimit)+" groups (default)"))
	row(distRowCustom, "", d.customValue(), d.customNotes()...)
	row(distRowAll, "", t.radio(f.limit == distLimitAll, "All eligible groups"))

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
		line := t.Key.Render(t.Glyphs.Cursor) + " " + t.Title.Render(padRight(label, distLabelW)) + value

		return t.highlight(line, w, bgSel)
	}

	return "  " + t.Muted.Render(padRight(label, distLabelW)) + value
}

// regionValue renders the region input or its value. An empty region
// reads "everywhere", because the run then covers the whole repository.
func (d *distRecord) regionValue() string {
	t := d.st.theme
	f := &d.form

	switch {
	case f.editRegion:
		return f.region.view(t)
	case strings.TrimSpace(f.region.value()) == "":
		return t.Muted.Render("everywhere")
	default:
		return t.Body.Render(oneLine(f.region.value()))
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

	notes := []string{t.Faint.Render(oneLine(scope) + sep + "/ edits")}

	if f.prefill != "" && strings.TrimSpace(f.region.value()) == f.prefill {
		notes = append(notes, t.Faint.Render("prefilled from the Proposals region filter"))
	}

	return notes
}

// customValue renders the custom limit choice with its number.
func (d *distRecord) customValue() string {
	t := d.st.theme
	f := &d.form

	on := f.limit == distLimitCustom
	out := t.radio(on, "custom")

	switch {
	case f.editCustom:
		out += t.Muted.Render(": ") + f.custom.view(t)
	case on && f.custom.value() != "":
		// The input takes digits only, so the value is a number.
		n, _ := strconv.Atoi(f.custom.value())
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
	kind := distAgentKind(snap.Invocation.Name, custom)
	room := w - 2 - distLabelW

	agent = distAgentText(t, snap.Invocation.Argv, kind, 0) + hint
	if width(agent) > room {
		agent = distAgentText(t, snap.Invocation.Argv, kind, room)
	}

	if snap.Invocation.Dir != "" {
		return agent, t.Body.Render(oneLine(displayPath(snap.Invocation.Dir)))
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
		"or cost budget. "+itoa(distill.DefaultGroupLimit)+" is the CLI default. All eligible groups sends every "+
		"group without a recorded signature, so choose it on purpose.")
	section("What is sent", "Each group sends its findings, which hold code and review text, to the agent. The "+
		"agent can also load the project instructions of this workspace.")
	section("Next", "enter builds a read-only plan: the groups, their evidence and an approximate prompt size. "+
		"No agent call happens.")

	return out
}

// distArgv renders an agent command line for display. Arguments can
// hold tokens, so the helper redacts them before it sanitizes the text.
// A secret-shaped value that holds shell syntax stays visible. It is
// code that runs after y, and "[REDACTED]" would hide it behind a word
// that reads as a safety measure.
func distArgv(argv []string) string {
	return oneLine(redact.SecretsExcept(strings.Join(argv, " "), shellActive))
}

// shellActive reports text that a shell runs or redirects: a command
// substitution, a backquote, a command separator, a pipe, a redirection
// or a line break. A password that holds such a character then shows
// too; the trade-off favors the code that runs.
func shellActive(s string) bool {
	return strings.Contains(s, "$(") || strings.ContainsAny(s, "`;|&<>\n\r")
}

// distAgentKind names where the agent command comes from: a registered
// preset by its name, or the custom agent.argv of the configuration.
func distAgentKind(name string, custom bool) string {
	if custom {
		return "custom agent.argv"
	}

	return "preset " + oneLine(name)
}

// distAgentText renders the agent as "<argv> · <kind>" (design map §4
// and §5). The Scope and the Review steps share it, so one agent reads
// the same on both. room > 0 cuts a long command line before the kind,
// so the kind always shows; room <= 0 leaves the cut to the caller.
func distAgentText(t *Theme, argv []string, kind string, room int) string {
	tail := " " + t.Glyphs.Sep + " " + kind
	cmd := distArgv(argv)

	if room > 0 && width(cmd)+width(tail) > room {
		cmd = truncate(cmd, max(1, room-width(tail)), t.Glyphs.Ellipsis)
	}

	return t.Body.Render(cmd) + t.Muted.Render(tail)
}

// distClock renders a time of day for the facts and the activity lines.
func distClock(at time.Time) string {
	return at.Format("15:04:05")
}
