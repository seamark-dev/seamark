package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The client and choice steps of the setup wizard. The choice step shows
// the additions in the "Choose additions" box and the focused addition
// in a second box: what it does, its state now, and its state after the
// plan. Every value comes from the inspection or the client descriptor.

// Column widths of the choice table. The Detail column takes the rest.
const (
	setupMarkW   = 2
	setupItemW   = 24
	setupChangeW = 10
	// setupLabelW is the label column of the "Now" and "After" rows.
	setupLabelW = 7
)

// setupSplit returns the outer width of the left box of a two-box step
// layout. The left box has the design width at 120 columns and takes
// half of a narrower terminal; a wider terminal grows the right box.
func setupSplit(w, design int) int {
	return min(design, w*design/120)
}

// clientView lists the registered clients with their current summary.
// The wide layout shows what the selected client supports beside it.
func (wz *setupWizard) clientView(w, h int) string {
	t := wz.st.theme

	if len(wz.clients) == 0 {
		return integBoxed(t, ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Choose a client", true)},
			ui.ToneNeutral, "No clients registered",
			"This build registers no coding agent clients, so there is nothing to set up.", "")
	}

	items := make([]integItem, 0, len(wz.clients))

	for _, c := range wz.clients {
		item := integItem{name: ui.OneLine(c.Name), badge: t.Unknown.Render(t.Glyphs.Unknown) + " " +
			t.Muted.Render("reading"), summary: []string{"reading the configuration"}}

		if insp, ok := integFind(wz.st, c.ID); ok {
			item = integClientItem(t, insp)
		}

		items = append(items, item)
	}

	wz.pick.Clamp(len(items), 0)
	sel := wz.clients[wz.pick.Cursor]
	note := "Setup adds or repairs configuration. It never removes the configuration of another client."

	if w >= ui.WideWidth {
		leftW := setupSplit(w, 60)
		rightW := w - leftW - 1
		inner := ui.BoxInner(rightW)

		lines := integLines(t.Body, ui.OneLine(sel.Name)+" supports "+ui.OneLine(sel.DescribeSupport())+".", inner)
		lines = append(lines, "", integHeading(t, "Notes"))
		lines = append(lines, integLines(t.Muted, note, inner)...)

		left := t.Box(ui.BoxOpts{W: leftW, H: h, Focus: true, Title: integPaneTitle(t, "Choose a client", true),
			Right: t.Muted.Render(strconv.Itoa(len(items))),
			Lines: integRenderClients(t, items, &wz.pick, ui.BoxInner(leftW), ui.BoxRows(h), true)})
		right := t.Box(ui.BoxOpts{W: rightW, H: h, Title: integPaneTitle(t, sel.Name, false), Lines: lines})

		return ui.HJoin(1, left, right)
	}

	// The list comes first and may use every row of the box, so each
	// client stays reachable. A paragraph follows only when it fits
	// whole, so a short terminal never shows a sentence cut in half.
	inner, rows := ui.BoxInner(w), ui.BoxRows(h)
	lines := integRenderClients(t, items, &wz.pick, inner, rows, true)

	for _, p := range [][]string{
		integLines(t.Muted, ui.OneLine(sel.Name)+" supports "+ui.OneLine(sel.DescribeSupport())+".", inner),
		integLines(t.Muted, note, inner),
	} {
		if len(lines)+1+len(p) > rows {
			break
		}

		lines = append(append(lines, ""), p...)
	}

	return t.Box(ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Choose a client", true),
		Right: t.Muted.Render(strconv.Itoa(len(items))), Lines: lines})
}

// choicesView renders the additions with their change and the detail of
// the focused row. The wide layout puts the detail in a second box; the
// narrow layout puts it under the rows, and "i" shows all of it.
func (wz *setupWizard) choicesView(w, h int) string {
	t := wz.st.theme
	title := integPaneTitle(t, "Choose additions", true)

	insp, ok := wz.inspection()
	if !ok {
		o := ui.BoxOpts{W: w, H: h, Focus: true, Title: title}

		if err := wz.st.integrations.err; err != nil {
			return integBoxed(t, o, ui.ToneFailure, "Could not read the current configuration", ui.ErrText(err),
				t.Muted.Render("Press ")+t.Key.Render("esc")+t.Muted.Render(" to go back."))
		}

		return integBoxed(t, o, ui.ToneNeutral, "Reading the current configuration"+t.Glyphs.Ellipsis,
			"The choices depend on what "+ui.OneLine(wz.client.Name)+" already has. Nothing is written.", "")
	}

	rows := setupRows(wz.client, insp)
	wz.rows.Clamp(len(rows), len(rows))
	sel := rows[wz.rows.Cursor]

	if wz.aboutOpen {
		return wz.aboutBox(sel, insp, w, h)
	}

	if w >= ui.WideWidth {
		leftW := setupSplit(w, 60)
		rightW := w - leftW - 1
		inner, boxH := ui.BoxInner(rightW), ui.BoxRows(h)

		left := t.Box(ui.BoxOpts{W: leftW, H: h, Focus: true, Title: title,
			Lines: wz.choiceLines(rows, ui.BoxInner(leftW), true)})
		right := t.Box(ui.BoxOpts{W: rightW, H: h, Title: integPaneTitle(t, setupIntentLabels[sel.intent], false),
			Lines: setupClip(t, wz.aboutLines(sel, insp, inner), inner, boxH)})

		return ui.HJoin(1, left, right)
	}

	// The narrow layout puts the explanation under the rows. It gets the
	// rows that are left; the details key shows the rest.
	inner, boxH := ui.BoxInner(w), ui.BoxRows(h)
	lines := wz.choiceLines(rows, inner, false)
	lines = append(lines, "")
	lines = append(lines, setupClip(t, wz.aboutLines(sel, insp, inner), inner, boxH-len(lines))...)

	return t.Box(ui.BoxOpts{W: w, H: h, Focus: true, Title: title, Lines: lines})
}

// aboutBox shows the full explanation of one addition in a scrolling
// box, with the scroll position in the bottom border.
func (wz *setupWizard) aboutBox(r setupRow, insp integration.Inspection, w, h int) string {
	t := wz.st.theme
	inner, rows := ui.BoxInner(w), ui.BoxRows(h)

	lines := wz.about.Lines("about:"+strconv.Itoa(int(r.intent)), wz.aboutLines(r, insp, inner), inner, rows)

	return t.Box(ui.BoxOpts{W: w, H: h, Focus: true,
		Title: integPaneTitle(t, "Details "+t.Glyphs.Sep+" "+setupIntentLabels[r.intent], true),
		Foot:  wz.about.Foot(t), Lines: lines})
}

// choiceLines renders the table of additions, the selection summary,
// the inline hint and the note on kept rows. withNote=false leaves the
// note out, because the narrow layout needs the rows for the detail.
func (wz *setupWizard) choiceLines(rows []setupRow, w int, withNote bool) []string {
	t := wz.st.theme
	f := t.Faint
	detailW := max(1, w-setupMarkW-setupItemW-setupChangeW)

	lines := []string{integRow(t, ui.Cell("", setupMarkW), ui.Cell(f.Render("Item"), setupItemW),
		ui.Cell(f.Render("Change"), setupChangeW), ui.Cell(f.Render("Detail"), detailW))}

	selected, kept, unsupported := 0, 0, 0

	for i, r := range rows {
		switch {
		case wz.checked(r):
			selected++
		case r.kind == setupRowKept:
			kept++
		case r.kind == setupRowUnsupported:
			unsupported++
		}

		lines = append(lines, wz.rowLine(r, i == wz.rows.Cursor, w, detailW))
	}

	sep := f.Render(" " + t.Glyphs.Sep + " ")
	summary := t.Body.Render(strconv.Itoa(selected)+" selected") + sep + t.Muted.Render(strconv.Itoa(kept)+" kept")

	if unsupported > 0 {
		summary += sep + t.Muted.Render(strconv.Itoa(unsupported)+" not supported")
	}

	lines = append(lines, "", integRule(t, w), summary)

	if wz.hint != "" {
		lines = append(lines, integHanging(t.Attention.Render(t.Glyphs.Warn+" "), t.Attention, wz.hint, w)...)
	}

	if wz.st.integrations.stale() {
		lines = append(lines, integHanging(t.Attention.Render(t.Glyphs.Warn+" "), t.Attention,
			"stale: "+ui.ErrText(wz.st.integrations.err), w)...)
	}

	if withNote {
		lines = append(lines, "")
		lines = append(lines, integLines(t.Faint, t.Glyphs.Kept+" rows are already configured. "+
			"Setup keeps them as they are: it never uninstalls or revokes.", w)...)
	}

	return lines
}

// rowLine renders one addition: the cursor, the mark and the label, the
// change the plan makes, and a detail from the inspection. The cursor
// row gets the selection background.
func (wz *setupWizard) rowLine(r setupRow, current bool, w, detailW int) string {
	t := wz.st.theme
	g := t.Glyphs

	cursor := ""
	if current {
		cursor = t.Key.Render(g.Cursor)
	}

	label := setupIntentLabels[r.intent]

	var mark, item, change, detail string

	switch r.kind {
	case setupRowKept:
		mark, item = t.Faint.Render(g.Kept), t.Muted.Render(label)
		change, detail = t.Success.Render("kept"), t.Faint.Render("already configured")
	case setupRowUnsupported:
		mark, item = t.Faint.Render(g.Locked), t.Muted.Render(label)
		change, detail = t.Faint.Render("n/a"), t.Faint.Render("not supported by "+ui.OneLine(wz.client.Name))
	default:
		mark, item = g.Uncheck, label
		change, detail = t.Faint.Render("no change"), t.Muted.Render(setupRowDetail(r))

		if wz.checked(r) {
			mark, change = g.Check, t.Body.Render("add")

			if r.kind == setupRowRepair {
				change = t.Attention.Render("update")
			}
		}

		if current {
			item = t.Title.Render(label)
		}
	}

	row := integRow(t, ui.Cell(cursor, setupMarkW), ui.Cell(mark+" "+item, setupItemW), ui.Cell(change, setupChangeW),
		ui.Cell(detail, detailW))

	if current {
		row = t.Highlight(row, w, ui.BgSel)
	}

	return row
}

// setupRowDetail returns the detail cell of a selectable row: the hooks
// that are missing when some hooks already run, else the current state
// in the shared diagnostic words.
func setupRowDetail(r setupRow) string {
	if r.intent == setupHooks && r.kind == setupRowRepair && len(r.missing) > 0 {
		return setupMissingPhrase(r.missing)
	}

	return ui.OneLine(r.describe)
}

// setupMissingPhrase names the hooks a plan adds, for example
// "+ context reset hook".
func setupMissingPhrase(missing []string) string {
	noun := " hook"
	if len(missing) > 1 {
		noun = " hooks"
	}

	return "+ " + joinWords(missing) + noun
}

// aboutLines explains the focused addition: what it does, its state now
// and after the plan, the skill directories when it installs skills,
// then the notes.
func (wz *setupWizard) aboutLines(r setupRow, insp integration.Inspection, w int) []string {
	t := wz.st.theme
	what, note := wz.aboutText(r, insp)

	lines := integLines(t.Body, what, w)
	lines = append(lines, "")

	now := ui.OneLine(r.describe)
	if now == "" {
		now = "unknown"
	}

	lines = append(lines, integLabeled(t, "Now", setupLabelW, now, t.Body, w)...)

	after, style := wz.afterText(r)
	lines = append(lines, integLabeled(t, "After", setupLabelW, after, style, w)...)

	if r.intent == setupSkills && r.kind != setupRowUnsupported && len(wz.client.SkillDirs) > 0 {
		label := "Dir"
		if len(wz.client.SkillDirs) > 1 {
			label = "Dirs"
		}

		lines = append(lines, integLabeled(t, label, setupLabelW, ui.OneLine(strings.Join(wz.client.SkillDirs, ", ")), t.Code, w)...)
	}

	lines = append(lines, "", integHeading(t, "Notes"))

	if note != "" {
		lines = append(lines, integLines(t.Muted, note, w)...)
		lines = append(lines, "")
	}

	return append(lines, integLines(t.Muted, "Unchecked rows stay as they are. Adding a client never removes "+
		"the configuration of another client and never changes agent.cli or agent.argv. "+
		"Nothing is written before you confirm the plan.", w)...)
}

// aboutText returns what an addition does and the note that belongs to
// it, for example the scope of tool grants.
func (wz *setupWizard) aboutText(r setupRow, insp integration.Inspection) (what, note string) {
	name := ui.OneLine(wz.client.Name)

	switch r.intent {
	case setupMCP:
		what = "Registers the seamark MCP server in the project configuration of " + name +
			", so the agent can call the Seamark tools."
	case setupHooks:
		what = "Installs the " + setupHookWords(wz.client) + " hooks in the project settings of " + name +
			". " + name + " then runs seamark on its native edit and shell events."
		note = "Installed hooks keep their gate mode"

		if insp.ManagedGateMode != "" {
			note += " (" + ui.OneLine(insp.ManagedGateMode) + ")"
		}

		note += ". New hooks use warn. The console never switches to or from enforcement."
	case setupSkills:
		what = "Installs or refreshes the managed Seamark skills that " + name + " reads."
		note = setupSharedNote(wz.clients, wz.client) +
			"A refresh rewrites only the shipped files and keeps extra files. A directory without the Seamark marker is never touched."
	case setupGrants:
		what = "Adds the exact per-tool grants for the Seamark MCP tools."
		note = "Tool grants are project configuration only: user or managed policy can still prompt. " +
			"Seamark never grants native trust."
	}

	return what, note
}

// afterText returns the state of an addition after the plan and its
// style. A row that stays as it is says so, because setup only adds or
// repairs.
func (wz *setupWizard) afterText(r setupRow) (string, lipgloss.Style) {
	t := wz.st.theme

	switch {
	case r.kind == setupRowKept:
		return "kept as it is; setup never uninstalls or revokes", t.Muted
	case r.kind == setupRowUnsupported:
		return ui.OneLine(wz.client.Name) + " does not support this addition, so setup cannot plan it.", t.Muted
	case !wz.checked(r):
		return "unchanged; this row is not selected", t.Muted
	case r.intent == setupHooks && len(r.missing) > 0:
		return setupMissingPhrase(r.missing), t.Success
	case r.kind == setupRowRepair:
		// The plan decides what a repair can change, so this row names
		// the intent and leaves the exact change to the review.
		return "a planned repair of the " + r.state.String() + " configuration; the review shows the exact change",
			t.Attention
	default:
		return "+ " + setupIntentWords[r.intent], t.Success
	}
}
