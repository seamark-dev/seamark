package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file renders the plan review of a distillation (design map §5).
// The Plan pane says what WILL and WON'T happen. The Groups pane lists
// the groups in engine order. The file also renders the plan failures
// and "Nothing new to distill" with its separately confirmed cleanup.

// Column widths of the Groups pane of the review.
const (
	distIdxW  = 4
	distFindW = 9
	distTokW  = 15
	// distEvPad indents the evidence rows under their group.
	distEvPad = 6
	// distIDMinW is the narrowest finding ID column: "#" and five digits
	// with a gap. Longer IDs widen the column.
	distIDMinW = 7
	// distPathMinW is the path room that a long ID leaves at least. The
	// source column gives way first.
	distPathMinW = 12
)

// previewView renders the Preparing wait, a plan failure, the empty
// plan, or the plan with its groups.
func (d *distRecord) previewView(w, h int) string {
	t := d.st.theme
	pl := &d.prev

	switch {
	case d.step == stepPreparing:
		o := ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

		return integBoxed(t, o, ui.ToneNeutral, "Planning the distillation"+t.Glyphs.Ellipsis,
			"Seamark reads the index, the pins and the agent configuration. Nothing is sent or written.",
			t.Hints([]key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "back to the scope")}))
	case pl.err != nil:
		return d.planErrBox(w, h)
	case pl.preview.Plan == nil || len(pl.preview.Plan.Groups) == 0:
		return d.nothingBox(w, h)
	}

	if d.st.wide() {
		leftW := max(34, setupSplit(w, agentPlanW))

		return ui.HJoin(1, d.planBox(leftW, h, pl.planFocus), d.groupsBox(w-leftW-1, h, !pl.planFocus))
	}

	if pl.listOpen {
		return d.groupsBox(w, h, true)
	}

	return d.planBox(w, h, true)
}

// planErrBox explains a plan that could not be made. Each cause names
// its route: Settings or the file for a configuration error, the index
// build for an index that is not ready. Nothing was sent or written.
func (d *distRecord) planErrBox(w, h int) string {
	t := d.st.theme
	err := d.prev.err
	o := ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

	title := "Could not plan the distillation"
	body := ui.ErrText(err) + "\n\nNothing was sent or written."

	// A configuration or an index that blocks the plan is a blocked
	// state, not a failure (design map §9), so it takes the attention
	// color. Other errors keep the failure color.
	tone := ui.ToneFailure

	var cfg *learning.DistillConfigError

	switch {
	case errors.As(err, &cfg):
		// Settings edits config.yaml only; lessons.yaml has no form.
		route := "Edit " + ui.OneLine(cfg.Path)
		if cfg.Path == settingsFile {
			route = "Press o to fix it in Settings, or edit " + ui.OneLine(cfg.Path)
		}

		title, tone = "The configuration blocks the plan", ui.ToneAttention
		body = ui.ErrText(err) + "\n\nThe console does not fall back to defaults for a paid run. " + route +
			", then plan again. Nothing was sent or written."
	case errors.Is(err, learning.ErrDistillNotReady):
		title, tone = "The index is not ready", ui.ToneAttention
		body = ui.ErrText(err) + "\n\nPress o to build it from Overview, then plan again. A distillation " +
			"never builds the index by itself. Nothing was sent or written."
	case errors.Is(err, distill.ErrStale):
		title = "The workspace changed while planning"
		body = ui.ErrText(err) + "\n\nPlan again to read the current state. Nothing was sent or written."
	}

	next := t.Hints([]key.Binding{
		ui.WithHelp(ui.Nav.Enter, "enter", "plan again"), d.linkKey(), ui.WithHelp(distKeys.Back, "n", "back to the scope"),
	})

	return integBoxed(t, o, tone, title, body, next)
}

// planBox renders the Plan pane: the facts, the effects, the data that
// leaves the machine, and the line that says how to start.
func (d *distRecord) planBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := ui.BoxInner(w)
	p := d.prev.preview

	// The notes that stop a start come first, so a short box shows them.
	// The data disclosure comes before WON'T, because it matters more.
	body := d.planNotes(inner)
	body = append(body, d.planFacts(inner)...)
	body = append(body, effectLines(t, d.planWill(), nil, inner)...)
	body = append(body, "", integHeading(t, "Sent data"))
	body = append(body, integLines(t.Body, "Findings hold code and review text. The agent can also load the "+
		"project instructions. Later prompts can grow with labels learned in this run, so usage can exceed the "+
		"estimate. No cost figure is available.", inner)...)
	body = append(body, effectLines(t, nil, d.planWont(), inner)...)

	tail := []string{"", integRule(t, inner)}

	if d.runnable() {
		tail = append(tail, t.Body.Render("Nothing is sent until you press ")+t.Key.Render("y")+t.Body.Render("."))
	} else {
		tail = append(tail, integLines(t.Muted, "Nothing was sent. Press enter to plan again.", inner)...)
	}

	if len(body)+len(tail) > ui.BoxRows(h) {
		tail = tail[1:]
	}

	o := ui.BoxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Plan", focused),
		Right: t.Muted.Render(propPlural(len(p.Plan.Groups), "group", "groups")),
	}

	return d.st.theme.ScrollBox(o, body, tail, &d.prev.notes, [2]int{-1, -1}, false)
}

// planFacts renders the key facts of the plan. Every count comes from
// the engine plan; the prompt size is the engine's estimate.
func (d *distRecord) planFacts(w int) []string {
	t := d.st.theme
	p := d.prev.preview
	pl := p.Plan

	eligible := len(pl.Groups) + len(pl.Deferred)

	rows := [][2]string{
		{"Groups", t.Body.Render(strconv.Itoa(len(pl.Groups)) + " of " + strconv.Itoa(eligible) + " eligible")},
		{"Findings", t.Body.Render(strconv.Itoa(pl.Preflight.Findings))},
		{"Prompt", t.Body.Render(agentTokens(pl.Preflight.PromptChars)+" tokens") + " " + t.Faint.Render("(estimated)")},
		{"Skipped", t.Body.Render(propPlural(len(pl.Skipped), "group", "groups") + " already distilled")},
	}

	if n := len(pl.Cleanup); n > 0 {
		rows = append(rows, [2]string{"Cleanup", t.Body.Render(strconv.Itoa(n)+" stale pending: ") +
			t.Attention.Render(propIDList(distProposalIDs(pl.Cleanup)))})
	}

	// The value wraps in its column, so the kind needs no cut here.
	agent := agentText(t, p.Invocation.Argv, agentKind(p.Invocation.Name, !d.prev.preset), 0)

	runsIn := runsInText(t, p.Invocation.Dir, d.prev.cwd, p.RepoRoot)
	rows = append(rows, [2]string{"Agent", agent}, [2]string{"Runs in", runsIn})

	return propKV(t, rows, w)
}

// runsInText names the directory where an agent process starts: dir is
// the directory of the invocation, cwd the directory of the console,
// and root the repository root. A preset without its own directory
// inherits the directory of the console, which can differ from the
// repository root (seamark -C). The distillation and the extraction
// reviews share it, so both disclose the directory the same way.
func runsInText(t *ui.Theme, dir, cwd, root string) string {
	switch {
	case dir != "":
		return t.Body.Render(ui.OneLine(ui.DisplayPath(dir)))
	case cwd == "":
		return t.Muted.Render("the directory of the console, not known")
	case cwd != root:
		return t.Body.Render(ui.OneLine(ui.DisplayPath(cwd))) + " " +
			t.Attention.Render("(the console directory, not the repository root)")
	default:
		return t.Body.Render(ui.OneLine(ui.DisplayPath(cwd)))
	}
}

// planNotes renders what stops the start: a blocker of the preview, or
// the refusal of an earlier start. Each note says what to do.
func (d *distRecord) planNotes(w int) []string {
	t := d.st.theme

	var out []string

	// A blocker stops the start but is not a failure: design map §9
	// gives it the attention color, as the Evidence view does.
	for _, b := range d.prev.preview.Blockers {
		out = append(out, propNote(t, ui.ToneAttention, ui.OneLine(b)+". Install it, or press o to choose another "+
			"agent in Settings. Then press enter to plan again.", w)...)
		out = append(out, "")
	}

	if err := d.prev.stale; err != nil {
		text := "The run did not start: " + ui.ErrText(err) + ". Nothing was sent. Press enter to plan again."
		if errors.Is(err, distill.ErrStale) {
			text = "The plan is out of date: " + distStaleText(err) + ". Nothing was sent. Press enter to plan again."
		}

		out = append(out, propNote(t, ui.ToneAttention, text, w)...)
		out = append(out, "")
	}

	return out
}

// planWill lists the effects of the run.
func (d *distRecord) planWill() []effect {
	t := d.st.theme
	g := t.Glyphs
	p := d.prev.preview

	// A custom agent.argv has no preset name, so the effect names the
	// setting, as the Agent fact does.
	agent := "the agent"
	if name := ui.OneLine(p.Invocation.Name); name != "" {
		agent = name
	}

	if !d.prev.preset {
		agent = "the custom agent.argv command"
	}

	var will []effect

	if c := p.Plan.Cleanup; len(c) > 0 {
		why := ": their evidence group is gone"
		if len(c) == 1 {
			why = ": its evidence group is gone"
		}

		will = append(will, effect{glyph: g.Del, tone: ui.ToneFailure, text: "first remove the stale pending " +
			propIDList(distProposalIDs(c)) + why})
	}

	return append(will,
		effect{glyph: g.Add, tone: ui.ToneSuccess, text: "send " + propPlural(len(p.Plan.Groups), "group", "groups") +
			" to " + agent + ", one call each, in engine order"},
		effect{glyph: g.Add, tone: ui.ToneSuccess, text: "save the proposals and the signature of each group " +
			"when it finishes"})
}

// planWont lists what the run leaves alone.
func (d *distRecord) planWont() []string {
	wont := []string{
		"apply pins or change lessons.yaml",
		"add groups found after this plan",
		"retry a failed group by itself",
	}

	if len(d.prev.preview.Plan.Cleanup) > 0 {
		wont = append(wont, "prune applied pins: the cleanup removes pending proposals only")
	}

	return wont
}

// groupsBox renders the planned groups in engine order with their
// evidence, then the groups over the limit. The cursor group and its
// evidence stay visible while the cursor moves.
func (d *distRecord) groupsBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := ui.BoxInner(w)
	pl := d.prev.preview.Plan

	d.prev.cursor = max(0, min(d.prev.cursor, len(pl.Groups)-1))

	body, keep := d.groupLines(inner, focused)

	o := ui.BoxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Groups", focused),
		Right: t.Faint.Render("engine order"),
	}

	return d.st.theme.ScrollBox(o, body, nil, &d.prev.list, keep, d.prev.follow)
}

// groupLines renders the group rows, the evidence rows under each group,
// and the deferred groups. It returns the line range of the cursor group.
func (d *distRecord) groupLines(w int, focused bool) (lines []string, keep [2]int) {
	t := d.st.theme
	pl := d.prev.preview.Plan
	regW := max(8, w-2-distIdxW-distFindW-distTokW)

	lines = []string{t.Faint.Render(integRow(t, ui.Cell("", 2), ui.Cell(ui.PadLeft("#", distIdxW-1), distIdxW),
		ui.Cell("Region", regW), ui.Cell(ui.PadLeft("Findings", distFindW-1), distFindW), ui.Cell("  Tokens (est.)", distTokW)))}
	keep = [2]int{-1, -1}

	for i, g := range pl.Preflight.Groups {
		cursor := i == d.prev.cursor
		start := len(lines)

		marker, region := "", t.Body
		if cursor {
			marker, region = t.Key.Render(t.Glyphs.Cursor), t.Title
		}

		row := integRow(t, ui.Cell(marker, 2), ui.Cell(t.Muted.Render(ui.PadLeft(strconv.Itoa(i+1), distIdxW-1)), distIdxW),
			ui.Cell(region.Render(distRegionLabel(g.Region)), regW),
			ui.Cell(t.Body.Render(ui.PadLeft(strconv.Itoa(g.Findings), distFindW-1)), distFindW),
			ui.Cell(t.Body.Render("  "+agentTokens(g.PromptChars)), distTokW))

		if cursor && focused {
			row = t.Highlight(row, w, ui.BgSel)
		}

		lines = append(lines, row)
		lines = append(lines, distEvidenceLines(t, g.Evidence, w, distEvPad)...)

		if cursor {
			keep = [2]int{start, len(lines) - 1}
		}
	}

	if n := len(pl.Deferred); n > 0 {
		lines = append(lines, "", integHeading(t, "Over the limit")+t.Faint.Render("  "+
			propPlural(n, "group waits", "groups wait")+" for another run"))

		for i, g := range pl.Deferred {
			lines = append(lines, t.Faint.Render(integRow(t, ui.Cell("", 2), ui.Cell(ui.PadLeft(strconv.Itoa(len(pl.Groups)+i+1),
				distIdxW-1), distIdxW), ui.Cell(distRegionLabel(g.Region), regW),
				ui.Cell(ui.PadLeft(strconv.Itoa(g.Findings), distFindW-1), distFindW))))
		}
	}

	// The cleanup rows show with their rules, because y deletes them.
	// They are pending proposals, never applied pins.
	if c := pl.Cleanup; len(c) > 0 {
		lines = append(lines, "", integHeading(t, "Stale pending")+t.Faint.Render("  removed before the first "+
			"group; applied pins stay"))

		for _, p := range c {
			lines = append(lines, integRow(t, ui.Cell("", 2), ui.Cell(t.Attention.Render(propID(p.ID)), distIdxW+2),
				ui.Cell(t.Body.Render(ui.OneLine(p.Rule)), max(4, regW-2)), ui.Cell(t.Muted.Render(propRegionLabel(p)),
					distFindW+distTokW)))
		}
	}

	return lines, keep
}

// distEvidenceLines renders the evidence of one group without its text.
// Each row shows the finding ID, the source, the pull request and the
// path. pad indents the rows under their group row.
func distEvidenceLines(t *ui.Theme, ev []distill.FindingPlan, w, pad int) []string {
	w -= pad

	// The ID column fits the longest ID of the group. A cut ID cannot be
	// matched with the CLI output, and a fix ID has up to 19 digits.
	idW := distIDMinW
	for _, f := range ev {
		idW = max(idW, len(distFindingID(f))+1)
	}

	// The source column gets a third of the row, between a short
	// source word and "fix:conventional". It gives way to a long ID
	// before the path does.
	prW := 8
	srcW := min(18, max(10, w/3))

	if short := distPathMinW - (w - idW - srcW - prW); short > 0 {
		srcW = max(10, srcW-short)
	}

	pathW := max(4, w-idW-srcW-prW)
	indent := strings.Repeat(" ", pad)

	lines := make([]string, 0, len(ev))

	for _, f := range ev {
		pr := ""
		if f.PR > 0 {
			pr = "PR #" + strconv.Itoa(f.PR)
		}

		row := integRow(t, ui.Cell(t.Faint.Render(distFindingID(f)), idW),
			ui.Cell(t.Muted.Render(ui.OneLine(f.Source)), srcW), ui.Cell(t.Muted.Render(pr), prW),
			ui.Cell(t.Code.Render(ui.TruncateLeft(distFindingPath(f), pathW, t.Glyphs.Ellipsis)), pathW))

		lines = append(lines, indent+row)
	}

	return lines
}

// distFindingID renders the full finding ID, as the CLI prints it.
func distFindingID(f distill.FindingPlan) string {
	return "#" + strconv.FormatInt(f.ID, 10)
}

// distFindingPath names the file of a finding. A finding with several
// files shows the first and the count of the others.
func distFindingPath(f distill.FindingPlan) string {
	switch {
	case f.Path != "":
		return ui.OneLine(f.Path)
	case len(f.Paths) == 1:
		return ui.OneLine(f.Paths[0])
	case len(f.Paths) > 1:
		return ui.OneLine(f.Paths[0]) + " +" + strconv.Itoa(len(f.Paths)-1)
	default:
		return "no path"
	}
}

// nothingBox explains a plan without an eligible group and offers the
// cleanup of stale pending proposals as its own confirmed action. No
// agent call happens from here.
func (d *distRecord) nothingBox(w, h int) string {
	t := d.st.theme
	inner := ui.BoxInner(w)
	p := d.prev.preview
	g := t.Glyphs

	lines := []string{t.Success.Render(g.OK+" ") + t.Title.Render("Nothing new to distill"), ""}
	lines = append(lines, integLines(t.Body, d.nothingReason(), inner)...)

	if p.Plan != nil && len(p.Plan.Cleanup) > 0 {
		lines = append(lines, "")
		lines = append(lines, d.cleanupLines(inner)...)
	}

	lines = append(lines, "", t.Faint.Render("Nothing was sent to the agent."))

	o := ui.BoxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Distillation", true),
		Right: t.Success.Render(g.OK + " nothing to do"),
	}

	return d.st.theme.ScrollBox(o, lines, nil, &d.prev.notes, [2]int{-1, -1}, false)
}

// nothingReason tells why no group is eligible: no findings in scope,
// findings without a group, or every group already recorded. The last
// case says that a repeated run would send nothing new.
func (d *distRecord) nothingReason() string {
	p := d.prev.preview
	scope := distScopeIn(p.Region)

	if p.Plan == nil {
		return "The planner found no evidence group " + scope + "."
	}

	pl := p.Plan

	switch {
	case pl.Corpus == 0:
		return "The index holds no findings " + scope + ". Refresh the evidence first: press e for Evidence."
	case pl.Total == 0:
		return "The " + propPlural(pl.Corpus, "finding", "findings") + " " + scope +
			" form no evidence group, so there is nothing to send."
	case len(pl.Skipped) >= pl.Total:
		return propPlural(pl.Total, "group", "groups") + " " + scope + " already have recorded signatures, " +
			"so no agent call is needed. A repeated run would send no new evidence."
	default:
		return "No evidence group " + scope + " is eligible for this run."
	}
}

// cleanupLines offers or reports the stale-pending cleanup. The offer
// names every ID, because the user confirms exactly these rows.
func (d *distRecord) cleanupLines(w int) []string {
	t := d.st.theme
	pl := &d.prev
	ids := distProposalIDs(pl.preview.Plan.Cleanup)
	n := len(ids)

	switch {
	case d.clean.running:
		return []string{t.Accent.Render(t.SpinnerFrame(d.st.tick)) + " " +
			t.Title.Render("Removing "+propPlural(n, "stale pending proposal", "stale pending proposals")+t.Glyphs.Ellipsis)}
	case d.clean.done && d.clean.err != nil:
		out := propNote(t, ui.ToneFailure, "The cleanup removed nothing: "+ui.ErrText(d.clean.err), w)

		if errors.Is(d.clean.err, distill.ErrStale) {
			out = append(out, integIndent(integLines(t.Muted, "The ledger changed since the plan. Press enter "+
				"to plan again.", w-2), 2)...)
		}

		return out
	case d.clean.done:
		text := "Removed " + propPlural(d.clean.removed, "stale pending proposal", "stale pending proposals") +
			" of " + propIDList(ids) + "."
		if d.clean.removed < n {
			text += " The other rows were decided meanwhile, so they stay."
		}

		return propNote(t, ui.ToneSuccess, text, w)
	}

	out := propNote(t, ui.ToneAttention, propPlural(n, "stale pending proposal", "stale pending proposals")+": "+
		propIDList(ids), w)

	why := "Their evidence group no longer exists, so a run would remove them first. c removes them"
	if n == 1 {
		why = "Its evidence group no longer exists, so a run would remove it first. c removes it"
	}

	return append(out, integIndent(integLines(t.Muted, why+" after a separate confirmation. No agent call "+
		"happens, and applied pins stay: this is not pin pruning.", w-2), 2)...)
}

// distProposalIDs returns the IDs of proposals in their order.
func distProposalIDs(ps []domain.Proposal) []int64 {
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}

	return ids
}
